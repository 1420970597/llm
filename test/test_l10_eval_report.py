#!/usr/bin/env python3
"""L10 接口测试：评估报告的汇总统计、分析与结论接口。

契约：docs/plans/eval-and-cleaning-plan.md 第 3.10 节。
  GET /api/v1/eval/runs/{id}/report   -> EvalReport
  GET /api/v1/eval/runs/{id}/scores   -> EvalItemScore[]

## 测试可重入说明（重要）

所有生成类接口走 apps/api/http_util.go 的 enqueueJob，它用
`dedup:<jobType>:<datasetID>` 做 SetNX，TTL 10 分钟。本 lane 的 report/scores
是**只读**接口，不走入队，因此不涉及去重键。

但 L10 依赖 L9 写入的 eval_runs / eval_items / eval_item_scores 数据。
L9 与 L10 并行开发，本 lane 的接口测试**自带造数**：直接用 SQL 插入
run + items + scores，不依赖 L9 是否已合并。这样测试是自包含的，
不会因为 L9 尚未合并而"输入缺失"。

## 覆盖的测试项

T1  未完成的 run -> report 返回骨架 + 结论明确说明未完成（不伪造完成）
T2  不存在的 run id -> report 404
T3  不存在的 run id -> scores 404
T4  非法 run id（非数字/0/负数）-> 400
T5  真实造数：两个裁判、两个维度 -> 验证加权总分（加权而非算术平均）
T6  真实造数：验证逐维度统计（均分/标准差/极值/样本数）
T7  真实造数：验证逐裁判统计（各裁判均分不同）
T8  真实造数：验证最弱条目按分数升序
T9  真实造数：验证裁判一致性（完全一致的两个裁判 -> 1.0）
T10 真实造数：验证低一致性被识别并给出分歧警告
T11 真实造数：验证单裁判 -> 一致性为不适用哨兵值 -1，且结论说明原因
T12 真实造数：验证权重为 0 的维度被排除在总分外且在结论中被告知
T13 真实造数：验证失败状态的打分被排除在统计外
T14 scores 接口按 judgeProviderId 过滤生效
T15 scores 接口按 dimensionKey 过滤生效
T16 scores 接口无过滤时返回全部
T17 scores 接口非法 judgeProviderId -> 400
T18 完成的 run -> 汇总行写入 eval_summaries（overall/judge/dimension/item 四个 scope）
"""

import json
import subprocess
import sys
import urllib.error
import urllib.request

BASE = "http://127.0.0.1:18096"
EMAIL = "admin@company.com"
PASSWORD = "admin123456"

POSTGRES_CONTAINER = "llm-postgres-1"
DB_USER = "llm_factory"
DB_NAME = "llm_factory"

PASSED = 0
FAILED = 0
FAILURES = []


def check(name, condition, detail=""):
    global PASSED, FAILED
    if condition:
        PASSED += 1
        print(f"  PASS  {name}")
    else:
        FAILED += 1
        FAILURES.append(name)
        print(f"  FAIL  {name}")
        if detail:
            print(f"        {detail}")


def psql(sql):
    """在共享 postgres 容器里执行 SQL，返回 stdout（已滤掉 INSERT/UPDATE 命令标签）。"""
    result = subprocess.run(
        ["docker", "exec", POSTGRES_CONTAINER, "psql", "-U", DB_USER, "-d", DB_NAME,
         "-t", "-A", "-F", "|", "-c", sql],
        capture_output=True, text=True, check=False,
    )
    if result.returncode != 0:
        raise RuntimeError(f"psql failed: {result.stderr.strip()}\nsql: {sql}")
    # psql 会把 "INSERT 0 1" / "UPDATE 3" / "DELETE 2" 这类命令标签写到 stdout，
    # 与 RETURNING 的结果混在一起，必须滤掉。
    lines = []
    for line in result.stdout.splitlines():
        stripped = line.strip()
        if not stripped:
            continue
        if stripped.startswith(("INSERT ", "UPDATE ", "DELETE ", "SELECT ")):
            continue
        lines.append(stripped)
    return "\n".join(lines)


def psql_int(sql):
    """执行 SQL 并返回单个整数结果。"""
    out = psql(sql)
    first = out.splitlines()[0].strip() if out.strip() else ""
    if not first:
        raise RuntimeError(f"psql 未返回结果\nsql: {sql}\nstdout: {out!r}")
    return int(first)


def login():
    """登录取 cookie。认证走 cookie，不是 Bearer token。"""
    body = json.dumps({"email": EMAIL, "password": PASSWORD}).encode()
    request = urllib.request.Request(
        f"{BASE}/api/v1/auth/login", data=body,
        headers={"Content-Type": "application/json"}, method="POST",
    )
    with urllib.request.urlopen(request, timeout=30) as response:
        cookies = response.headers.get_all("Set-Cookie") or []
    for cookie in cookies:
        if cookie.startswith("llm_session="):
            return cookie.split(";")[0]
    raise RuntimeError(f"登录未返回 llm_session cookie，实际：{cookies}")


def request(method, path, cookie, body=None):
    """返回 (status, parsed_json_or_text)。不抛 HTTPError，由调用方断言状态码。"""
    data = json.dumps(body).encode() if body is not None else None
    headers = {"Cookie": cookie}
    if data is not None:
        headers["Content-Type"] = "application/json"
    req = urllib.request.Request(f"{BASE}{path}", data=data, headers=headers, method=method)
    try:
        with urllib.request.urlopen(req, timeout=60) as response:
            raw = response.read().decode()
            try:
                return response.status, json.loads(raw)
            except json.JSONDecodeError:
                return response.status, raw
    except urllib.error.HTTPError as error:
        raw = error.read().decode()
        try:
            return error.code, json.loads(raw)
        except json.JSONDecodeError:
            return error.code, raw


# ---------------------------------------------------------------- 造数工具

def cleanup_run(run_id, dataset_id=None):
    """删除测试造的 run（CASCADE 会带走 items / scores / judges / summaries）。

    传 dataset_id 时一并删掉测试数据集，避免在共享库上留下垃圾数据。
    """
    psql(f"DELETE FROM eval_runs WHERE id = {run_id};")
    if dataset_id:
        psql(f"DELETE FROM datasets WHERE id = {dataset_id};")


def cleanup_dimensions():
    """清掉本 lane 造的全部测试维度与测试数据集。"""
    psql("DELETE FROM eval_dimensions WHERE key LIKE 'l10\\_%';")
    psql("DELETE FROM datasets WHERE root_keyword = 'l10-test';")


def make_dataset(name):
    """造一个测试数据集，返回 id。"""
    return psql_int(
        "INSERT INTO datasets (name, root_keyword, target_size, status) "
        f"VALUES ('{name}', 'l10-test', 100, 'draft') RETURNING id;"
    )


def make_dimensions(suffix, weight_a=3, weight_b=1):
    """造两个测试维度，返回 (key_a, key_b)。

    权重刻意设为 3:1，让加权平均与算术平均有明显差异 —— 这样才能真正
    验证接口返回的是加权总分。量表 0~10。
    """
    key_a = f"l10_depth_{suffix}"
    key_b = f"l10_accuracy_{suffix}"
    psql(
        "INSERT INTO eval_dimensions (key, name, category, description, rubric, "
        "scale_min, scale_max, is_builtin, is_active, weight) VALUES "
        f"('{key_a}', '长链深度', 'long_chain', '', '', 0, 10, FALSE, TRUE, {weight_a}),"
        f"('{key_b}', '答案准确性', 'answer_quality', '', '', 0, 10, FALSE, TRUE, {weight_b}) "
        "ON CONFLICT (key) DO UPDATE SET weight = EXCLUDED.weight;"
    )
    return key_a, key_b


def make_run(dataset_id, dim_keys, judge_ids, status="completed", total=0, scored=0):
    """造一个评估运行，返回 id。"""
    dims = json.dumps(dim_keys)
    judges = json.dumps(judge_ids)
    return psql_int(
        "INSERT INTO eval_runs (dataset_id, name, sampling_mode, target_kind, "
        "dimension_keys, judge_provider_ids, status, total_items, scored_items) VALUES "
        f"({dataset_id}, 'L10 接口测试', 'full', 'sft', '{dims}'::jsonb, "
        f"'{judges}'::jsonb, '{status}', {total}, {scored}) RETURNING id;"
    )


def make_items(run_id, dataset_id, count):
    """造 count 条被评条目，返回 [item_id]。"""
    values = ", ".join(
        f"({run_id}, {dataset_id}, {index + 1}, {index})" for index in range(count)
    )
    out = psql(
        "INSERT INTO eval_items (eval_run_id, dataset_id, question_id, item_index) "
        f"VALUES {values} RETURNING id;"
    )
    return [int(line) for line in out.splitlines() if line.strip().isdigit()]


def make_scores(run_id, item_ids, provider_id, dimension_key, values, status="scored"):
    """批量插入打分。values 与 item_ids 一一对应。"""
    if not values:
        return
    rows = ", ".join(
        f"({run_id}, {item_ids[index]}, {provider_id}, '{dimension_key}', "
        f"{values[index]}, '', '', '{status}')"
        for index in range(len(values))
    )
    psql(
        "INSERT INTO eval_item_scores (eval_run_id, eval_item_id, judge_provider_id, "
        f"dimension_key, score, rationale, raw_response, status) VALUES {rows};"
    )


def make_judge(run_id, provider_id, name, model):
    """登记一个参与评分的裁判。"""
    psql(
        "INSERT INTO eval_run_judges (eval_run_id, provider_id, provider_name, model, "
        f"status, scored_items) VALUES ({run_id}, {provider_id}, '{name}', '{model}', "
        "'completed', 0) ON CONFLICT (eval_run_id, provider_id) DO NOTHING;"
    )


# ---------------------------------------------------------------- 测试

def main():
    cookie = login()
    print(f"登录成功（cookie 长度 {len(cookie)}）\n")

    # ---- T1: 未完成的 run 不给质量结论
    print("T1  未完成的 run -> report 返回骨架 + 结论明确说明未完成")
    dataset_id = make_dataset("L10 未完成测试")
    key_a, key_b = make_dimensions("t1")
    run_id = make_run(dataset_id, [key_a, key_b], [], status="running", total=10, scored=3)
    try:
        status, report = request("GET", f"/api/v1/eval/runs/{run_id}/report", cookie)
        check("T1.1 状态码 200", status == 200, f"实际 {status}")
        check("T1.2 返回 evalRun 且状态为 running",
              report.get("evalRun", {}).get("status") == "running",
              f"实际 {report.get('evalRun', {}).get('status')}")
        conclusions = " ".join(report.get("conclusions") or [])
        check("T1.3 结论明确说明「尚未完成」", "尚未完成" in conclusions, f"结论：{conclusions}")
        check("T1.4 结论写出当前状态 running", "running" in conclusions, f"结论：{conclusions}")
        check("T1.5 未完成时不给质量档位判断",
              not any(word in conclusions for word in ["优秀", "良好", "偏低", "一般"]),
              f"结论：{conclusions}")
        check("T1.6 未完成时 judgeAgreement 为不适用哨兵值 -1",
              report.get("judgeAgreement") == -1,
              f"实际 {report.get('judgeAgreement')}")
    finally:
        cleanup_run(run_id, dataset_id)

    # ---- T2/T3: 404
    print("\nT2  不存在的 run id -> report 404")
    status, _ = request("GET", "/api/v1/eval/runs/999999999/report", cookie)
    check("T2.1 状态码 404", status == 404, f"实际 {status}")

    print("\nT3  不存在的 run id -> scores 404")
    status, _ = request("GET", "/api/v1/eval/runs/999999999/scores", cookie)
    check("T3.1 状态码 404", status == 404, f"实际 {status}")

    # ---- T4: 非法 id
    print("\nT4  非法 run id -> 400")
    for raw in ["abc", "0", "-5"]:
        status, _ = request("GET", f"/api/v1/eval/runs/{raw}/report", cookie)
        check(f"T4.{raw} 状态码 400", status == 400, f"实际 {status}")

    # ---- T5~T8: 真实造数验证聚合
    print("\nT5  真实造数：加权总分（不是算术平均）")
    dataset_id = make_dataset("L10 加权测试")
    key_a, key_b = make_dimensions("t5", weight_a=3, weight_b=1)
    run_id = make_run(dataset_id, [key_a, key_b], [100, 200], total=4, scored=4)
    try:
        item_ids = make_items(run_id, dataset_id, 4)
        make_judge(run_id, 100, "裁判甲", "model-a")
        make_judge(run_id, 200, "裁判乙", "model-b")

        # 维度 A（权重 3）：两个裁判各打 10 分 -> 均分 10
        # 维度 B（权重 1）：两个裁判各打 0 分  -> 均分 0
        # 加权 = (10*3 + 0*1)/4 = 7.5；算术 = 5.0
        make_scores(run_id, item_ids, 100, key_a, [10, 10, 10, 10])
        make_scores(run_id, item_ids, 200, key_a, [10, 10, 10, 10])
        make_scores(run_id, item_ids, 100, key_b, [0, 0, 0, 0])
        make_scores(run_id, item_ids, 200, key_b, [0, 0, 0, 0])

        status, report = request("GET", f"/api/v1/eval/runs/{run_id}/report", cookie)
        check("T5.1 状态码 200", status == 200, f"实际 {status}")
        overall = report.get("overallScore")
        check("T5.2 加权总分为 7.5（权重 3:1）",
              overall is not None and abs(overall - 7.5) < 1e-6, f"实际 {overall}")
        check("T5.3 总分不是算术平均 5.0（证明权重生效）",
              overall is not None and abs(overall - 5.0) > 1e-6, f"实际 {overall}")

        print("\nT6  真实造数：逐维度统计（均分/标准差/极值/样本数）")
        dimensions = {d["dimensionKey"]: d for d in report.get("dimensions") or []}
        check("T6.1 返回两个维度", len(dimensions) == 2, f"实际 {list(dimensions)}")
        stat_a = dimensions.get(key_a, {})
        check("T6.2 维度 A 均分 10", abs(stat_a.get("score", -1) - 10) < 1e-6,
              f"实际 {stat_a.get('score')}")
        check("T6.3 维度 A 样本数 8（4 条 × 2 裁判）", stat_a.get("sampleCount") == 8,
              f"实际 {stat_a.get('sampleCount')}")
        check("T6.4 维度 A 标准差 0（全同分）", abs(stat_a.get("stdDev", -1)) < 1e-9,
              f"实际 {stat_a.get('stdDev')}")
        check("T6.5 维度 A 极值 10~10",
              stat_a.get("min") == 10 and stat_a.get("max") == 10,
              f"实际 {stat_a.get('min')}~{stat_a.get('max')}")
        check("T6.6 维度名回填正确", stat_a.get("name") == "长链深度",
              f"实际 {stat_a.get('name')}")

        print("\nT7  真实造数：逐裁判统计")
        judges = {j["providerId"]: j for j in report.get("judges") or []}
        check("T7.1 返回两个裁判", len(judges) == 2, f"实际 {list(judges)}")
        # 裁判 100：A 维 10 分 × 4 + B 维 0 分 × 4 -> 均分 5
        check("T7.2 裁判 100 均分 5", abs(judges.get(100, {}).get("score", -1) - 5) < 1e-6,
              f"实际 {judges.get(100, {}).get('score')}")
        check("T7.3 裁判 100 样本数 8", judges.get(100, {}).get("sampleCount") == 8,
              f"实际 {judges.get(100, {}).get('sampleCount')}")
        check("T7.4 裁判 100 有 2 个维度明细",
              len(judges.get(100, {}).get("dimensions") or []) == 2,
              f"实际 {len(judges.get(100, {}).get('dimensions') or [])}")
        check("T7.5 裁判 100 有 4 条逐条分数",
              len(judges.get(100, {}).get("itemScores") or []) == 4,
              f"实际 {len(judges.get(100, {}).get('itemScores') or [])}")

        print("\nT8  真实造数：最弱条目")
        weakest = report.get("weakestItems") or []
        check("T8.1 最弱条目非空", len(weakest) > 0, f"实际 {len(weakest)}")
        check("T8.2 最弱条目按分数升序",
              all(weakest[i]["score"] <= weakest[i + 1]["score"] for i in range(len(weakest) - 1)),
              f"实际分数序列 {[w['score'] for w in weakest]}")
        check("T8.3 每条含 questionId 与 itemIndex",
              all("questionId" in w and "itemIndex" in w for w in weakest),
              f"实际 {weakest[:2]}")
    finally:
        cleanup_run(run_id, dataset_id)

    # ---- T9: 完全一致的裁判 -> 一致性 1.0
    print("\nT9  真实造数：两个完全一致的裁判 -> 一致性 1.0")
    dataset_id = make_dataset("L10 一致性测试")
    key_a, key_b = make_dimensions("t9")
    run_id = make_run(dataset_id, [key_a], [100, 200], total=5, scored=5)
    try:
        item_ids = make_items(run_id, dataset_id, 5)
        make_judge(run_id, 100, "裁判甲", "model-a")
        make_judge(run_id, 200, "裁判乙", "model-b")
        values = [1, 3, 5, 7, 9]
        make_scores(run_id, item_ids, 100, key_a, values)
        make_scores(run_id, item_ids, 200, key_a, values)

        status, report = request("GET", f"/api/v1/eval/runs/{run_id}/report", cookie)
        agreement = report.get("judgeAgreement")
        check("T9.1 一致性为 1.0", agreement is not None and abs(agreement - 1.0) < 1e-6,
              f"实际 {agreement}")
        conclusions = " ".join(report.get("conclusions") or [])
        check("T9.2 结论说明高度共识", "高度共识" in conclusions or "可信" in conclusions,
              f"结论：{conclusions}")
    finally:
        cleanup_run(run_id, dataset_id)

    # ---- T10: 排序相反的裁判 -> 低一致性 + 分歧警告
    print("\nT10 真实造数：排序相反的裁判 -> 低一致性 + 分歧警告")
    dataset_id = make_dataset("L10 分歧测试")
    key_a, key_b = make_dimensions("t10")
    run_id = make_run(dataset_id, [key_a], [100, 200], total=6, scored=6)
    try:
        item_ids = make_items(run_id, dataset_id, 6)
        make_judge(run_id, 100, "裁判甲", "model-a")
        make_judge(run_id, 200, "裁判乙", "model-b")
        values = [1, 2, 3, 4, 5, 6]
        make_scores(run_id, item_ids, 100, key_a, values)
        make_scores(run_id, item_ids, 200, key_a, list(reversed(values)))

        status, report = request("GET", f"/api/v1/eval/runs/{run_id}/report", cookie)
        agreement = report.get("judgeAgreement")
        check("T10.1 一致性为 0（排序完全相反）",
              agreement is not None and abs(agreement) < 1e-6, f"实际 {agreement}")
        conclusions = " ".join(report.get("conclusions") or [])
        check("T10.2 结论出现分歧警告", "分歧" in conclusions, f"结论：{conclusions}")
        check("T10.3 结论明确说明可信度受限", "可信度受限" in conclusions, f"结论：{conclusions}")
    finally:
        cleanup_run(run_id, dataset_id)

    # ---- T11: 单裁判 -> 哨兵值 + 说明
    print("\nT11 真实造数：单裁判 -> 一致性不适用哨兵值 -1")
    dataset_id = make_dataset("L10 单裁判测试")
    key_a, key_b = make_dimensions("t11")
    run_id = make_run(dataset_id, [key_a], [100], total=3, scored=3)
    try:
        item_ids = make_items(run_id, dataset_id, 3)
        make_judge(run_id, 100, "唯一裁判", "model-only")
        make_scores(run_id, item_ids, 100, key_a, [5, 7, 9])

        status, report = request("GET", f"/api/v1/eval/runs/{run_id}/report", cookie)
        agreement = report.get("judgeAgreement")
        check("T11.1 一致性为哨兵值 -1（不是 0）",
              agreement is not None and agreement == -1, f"实际 {agreement}")
        check("T11.2 一致性不等于 0（0 会被误读成完全不一致）",
              agreement != 0, f"实际 {agreement}")
        conclusions = " ".join(report.get("conclusions") or [])
        check("T11.3 结论说明一致性不适用", "不适用" in conclusions, f"结论：{conclusions}")
        check("T11.4 结论说明原因是只有 1 个裁判", "1 个裁判" in conclusions, f"结论：{conclusions}")
        check("T11.5 结论不出现「分歧较大」",
              "分歧较大" not in conclusions, f"结论：{conclusions}")
    finally:
        cleanup_run(run_id, dataset_id)

    # ---- T12: 权重 0 的维度被排除且被告知
    print("\nT12 真实造数：权重为 0 的维度被排除在总分外且在结论中告知")
    dataset_id = make_dataset("L10 零权重测试")
    key_a, key_b = make_dimensions("t12", weight_a=3, weight_b=0)
    run_id = make_run(dataset_id, [key_a, key_b], [100], total=3, scored=3)
    try:
        item_ids = make_items(run_id, dataset_id, 3)
        make_judge(run_id, 100, "裁判甲", "model-a")
        make_scores(run_id, item_ids, 100, key_a, [8, 8, 8])
        make_scores(run_id, item_ids, 100, key_b, [2, 2, 2])

        status, report = request("GET", f"/api/v1/eval/runs/{run_id}/report", cookie)
        overall = report.get("overallScore")
        check("T12.1 总分为 8（权重 0 的维度被排除）",
              overall is not None and abs(overall - 8) < 1e-6, f"实际 {overall}")
        check("T12.2 被排除的维度仍单独展示",
              len(report.get("dimensions") or []) == 2,
              f"实际 {len(report.get('dimensions') or [])}")
        conclusions = " ".join(report.get("conclusions") or [])
        check("T12.3 结论点名被排除的维度 key", key_b in conclusions, f"结论：{conclusions}")
        check("T12.4 结论说明原因是权重", "权重" in conclusions, f"结论：{conclusions}")
    finally:
        cleanup_run(run_id, dataset_id)

    # ---- T13: 失败打分被排除
    print("\nT13 真实造数：失败状态的打分被排除在统计外")
    dataset_id = make_dataset("L10 失败打分测试")
    key_a, key_b = make_dimensions("t13")
    run_id = make_run(dataset_id, [key_a], [100], total=2, scored=1)
    try:
        item_ids = make_items(run_id, dataset_id, 2)
        make_judge(run_id, 100, "裁判甲", "model-a")
        make_scores(run_id, item_ids, 100, key_a, [10, 0], status="scored")
        # 把第二条改成失败状态。
        psql(
            f"UPDATE eval_item_scores SET status = 'failed' "
            f"WHERE eval_run_id = {run_id} AND eval_item_id = {item_ids[1]};"
        )

        status, report = request("GET", f"/api/v1/eval/runs/{run_id}/report", cookie)
        overall = report.get("overallScore")
        check("T13.1 总分 10（失败记录的 0 分被排除）",
              overall is not None and abs(overall - 10) < 1e-6, f"实际 {overall}")
        conclusions = " ".join(report.get("conclusions") or [])
        check("T13.2 结论告知有 1 条打分未成功", "1 条打分未成功" in conclusions,
              f"结论：{conclusions}")
    finally:
        cleanup_run(run_id, dataset_id)

    # ---- T14~T17: scores 接口过滤
    print("\nT14 scores 接口按 judgeProviderId 过滤")
    dataset_id = make_dataset("L10 scores 过滤测试")
    key_a, key_b = make_dimensions("t14")
    run_id = make_run(dataset_id, [key_a], [100, 200], total=3, scored=3)
    try:
        item_ids = make_items(run_id, dataset_id, 3)
        make_scores(run_id, item_ids, 100, key_a, [1, 2, 3])
        make_scores(run_id, item_ids, 200, key_a, [4, 5, 6])

        status, scores = request("GET", f"/api/v1/eval/runs/{run_id}/scores", cookie)
        check("T14.1 无过滤返回 6 条", status == 200 and len(scores) == 6,
              f"状态 {status}，条数 {len(scores) if isinstance(scores, list) else scores}")

        status, scores = request(
            "GET", f"/api/v1/eval/runs/{run_id}/scores?judgeProviderId=100", cookie)
        check("T14.2 按裁判 100 过滤返回 3 条", status == 200 and len(scores) == 3,
              f"状态 {status}，条数 {len(scores) if isinstance(scores, list) else scores}")
        check("T14.3 过滤结果全为裁判 100",
              all(s["judgeProviderId"] == 100 for s in scores) if isinstance(scores, list) else False,
              f"实际 {scores}")

        print("\nT15 scores 接口按 dimensionKey 过滤")
        status, scores = request(
            "GET", f"/api/v1/eval/runs/{run_id}/scores?dimensionKey={key_a}", cookie)
        check("T15.1 按维度过滤返回 6 条", status == 200 and len(scores) == 6,
              f"状态 {status}，条数 {len(scores) if isinstance(scores, list) else scores}")
        status, scores = request(
            "GET", f"/api/v1/eval/runs/{run_id}/scores?dimensionKey=nonexistent_key", cookie)
        check("T15.2 不存在的维度返回空数组（不是报错）",
              status == 200 and scores == [], f"状态 {status}，内容 {scores}")

        print("\nT16 scores 接口同时按裁判与维度过滤")
        status, scores = request(
            "GET",
            f"/api/v1/eval/runs/{run_id}/scores?judgeProviderId=200&dimensionKey={key_a}",
            cookie)
        check("T16.1 返回 3 条且全为裁判 200",
              status == 200 and len(scores) == 3 and all(s["judgeProviderId"] == 200 for s in scores),
              f"状态 {status}，内容 {scores}")

        print("\nT17 scores 接口非法 judgeProviderId -> 400")
        for raw in ["abc", "-1"]:
            status, _ = request(
                "GET", f"/api/v1/eval/runs/{run_id}/scores?judgeProviderId={raw}", cookie)
            check(f"T17.{raw} 状态码 400", status == 400, f"实际 {status}")
    finally:
        cleanup_run(run_id, dataset_id)

    # ---- T18: 汇总行落库
    print("\nT18 完成的 run -> 汇总行写入 eval_summaries")
    dataset_id = make_dataset("L10 汇总行测试")
    key_a, key_b = make_dimensions("t18")
    run_id = make_run(dataset_id, [key_a, key_b], [100, 200], total=4, scored=4)
    try:
        item_ids = make_items(run_id, dataset_id, 4)
        make_judge(run_id, 100, "裁判甲", "model-a")
        make_judge(run_id, 200, "裁判乙", "model-b")
        make_scores(run_id, item_ids, 100, key_a, [8, 8, 8, 8])
        make_scores(run_id, item_ids, 200, key_a, [8, 8, 8, 8])
        make_scores(run_id, item_ids, 100, key_b, [4, 4, 4, 4])
        make_scores(run_id, item_ids, 200, key_b, [4, 4, 4, 4])

        status, _ = request("GET", f"/api/v1/eval/runs/{run_id}/report", cookie)
        check("T18.1 状态码 200", status == 200, f"实际 {status}")

        out = psql(
            f"SELECT scope, count(*) FROM eval_summaries WHERE eval_run_id = {run_id} "
            "GROUP BY scope ORDER BY scope;"
        )
        scopes = dict(line.split("|") for line in out.splitlines() if line.strip())
        check("T18.2 写入 overall 汇总行", scopes.get("overall") == "1", f"实际 {scopes}")
        check("T18.3 写入 judge 汇总行 2 条", scopes.get("judge") == "2", f"实际 {scopes}")
        check("T18.4 写入 dimension 汇总行 2 条", scopes.get("dimension") == "2", f"实际 {scopes}")
        check("T18.5 写入 item 汇总行", int(scopes.get("item", "0")) > 0, f"实际 {scopes}")

        # overall 行必须带 judgeAgreement。
        detail = psql(
            f"SELECT detail->>'judgeAgreement' FROM eval_summaries "
            f"WHERE eval_run_id = {run_id} AND scope = 'overall';"
        )
        check("T18.6 overall 汇总行含 judgeAgreement",
              detail.strip() not in ("", "null"), f"实际 {detail!r}")

        # 幂等：再请求一次，行数不应翻倍。
        request("GET", f"/api/v1/eval/runs/{run_id}/report", cookie)
        out = psql(
            f"SELECT count(*) FROM eval_summaries WHERE eval_run_id = {run_id};")
        check("T18.7 重复请求不产生重复行（upsert 幂等）",
              int(out.strip().splitlines()[0]) == 1 + 2 + 2 + len(item_ids),
              f"实际 {out.strip()}，期望 {1 + 2 + 2 + len(item_ids)}")
    finally:
        cleanup_run(run_id, dataset_id)
        cleanup_dimensions()

    print(f"\n{'=' * 60}")
    print(f"通过 {PASSED} 项，失败 {FAILED} 项")
    if FAILURES:
        print("失败项：")
        for name in FAILURES:
            print(f"  - {name}")
    print("=" * 60)
    return 1 if FAILED else 0


if __name__ == "__main__":
    sys.exit(main())
