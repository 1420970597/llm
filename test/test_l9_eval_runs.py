#!/usr/bin/env python3
"""L9 评估运行接口测试 —— 打自己构建的 lane-l9 镜像（:18095），不是 main 镜像。

测试项（契约 docs/plans/eval-and-cleaning-plan.md 第 3.9 节）：
  T1  登录并拿到会话 cookie
  T2  POST /api/v1/eval/runs          创建运行，返回 draft 状态
  T3  GET  /api/v1/eval/runs?datasetId=  列出运行
  T4  GET  /api/v1/eval/runs/{id}     运行详情（含 judges/dimensions）
  T5  POST /api/v1/eval/runs/{id}/start   入队，202 + 「已入队」
  T6  GET  /api/v1/eval/runs/{id}/items   列出条目
  T7  抽样模式 full / ratio / count 各一条（创建时不报错）
  T8  非法抽样参数被拒（ratio=0、count=0、未知模式）
  T9  不存在的运行返回 404
  T10 不存在的 datasetId 返回 404

可重入性（父代理踩过的坑）：所有入队接口走 http_util.go 的 enqueueJob，
用 dedup:<jobType>:<datasetID> 做 SetNX，TTL 10 分钟。10 分钟内重跑同一个
(jobType, datasetID) 不会 LPush，但接口仍返回 202 + state=queued，只是 message
变成「已在队列中」。因此：
  - 每次发 POST 前先 clear_dedup("eval.run", dataset_id)
  - 断言里必须包含 message，识破「被去重抑制」的假入队

端到端（T11）：本地真实环境只有 1 个 provider（id=1）且它就是数据集生成者，
被自评剔除规则拦下，因此 start 路径永远走不到「真的评出分数」。
T11 因此插一个临时裁判 provider（复制 provider 1 的 base_url/model/加密后的
api_key），用它跑完整的 start -> worker -> 真实 LLM 打分 -> 落库链路。
仅 1 个维度 × 1 条数据，避免一次跑 58 个维度把测试拖到小时级。
夹具（临时 provider、评估运行）在测试结束时删除，不污染共享库。

运行方式：
  python3 test/test_l9_eval_runs.py
"""

import json
import subprocess
import sys
import time
import urllib.error
import urllib.request
from http.cookiejar import CookieJar

BASE = "http://127.0.0.1:18095"
EMAIL = "admin@company.com"
PASSWORD = "admin123456"

# T11 临时裁判使用的模型：与数据集生成者模型不同（否则被同源剔除规则拦下），
# 但走同一个网关、同一把加密后的 api_key。
E2E_JUDGE_MODEL = "global:hy3"

PASSED = []
FAILED = []


def clear_dedup(job_type, dataset_id):
    """清理入队去重键，保证测试可重复运行。"""
    subprocess.run(
        ["docker", "exec", "llm-redis-1", "redis-cli", "del",
         f"dedup:{job_type}:{dataset_id}"],
        capture_output=True, check=False,
    )


def sql(query):
    """在共享 postgres 上执行 SQL，返回 stdout。"""
    result = subprocess.run(
        ["docker", "exec", "llm-postgres-1", "psql", "-U", "llm_factory",
         "-d", "llm_factory", "-t", "-A", "-c", query],
        capture_output=True, text=True, check=False,
    )
    return result.stdout.strip()


def sql_scalar(query):
    """取单值查询的首行。psql 会把 INSERT/DELETE 的命令标签也打出来
    （如 "2\\nINSERT 0 1"），直接 int() 会炸，所以只取第一行。

    只调用一次 sql()：INSERT ... RETURNING 重复执行会多插一行。
    """
    output = sql(query)
    return output.splitlines()[0].strip() if output else ""


class Client:
    def __init__(self):
        self.jar = CookieJar()
        self.opener = urllib.request.build_opener(
            urllib.request.HTTPCookieProcessor(self.jar))

    def request(self, method, path, body=None):
        data = None
        headers = {}
        if body is not None:
            data = json.dumps(body).encode()
            headers["Content-Type"] = "application/json"
        req = urllib.request.Request(BASE + path, data=data, headers=headers, method=method)
        try:
            with self.opener.open(req, timeout=60) as resp:
                raw = resp.read().decode()
                return resp.status, (json.loads(raw) if raw.strip() else None)
        except urllib.error.HTTPError as exc:
            raw = exc.read().decode()
            try:
                return exc.code, json.loads(raw)
            except json.JSONDecodeError:
                return exc.code, {"raw": raw}

    def get(self, path):
        return self.request("GET", path)

    def post(self, path, body=None):
        return self.request("POST", path, body)


def check(name, condition, detail=""):
    if condition:
        PASSED.append(name)
        print(f"  PASS  {name}")
    else:
        FAILED.append(name)
        print(f"  FAIL  {name}  {detail}")


def run_e2e(client, dataset_id, generator_provider_id):
    """T11 端到端：start -> worker -> 真实 LLM 打分 -> 落库。

    需要自己的 worker 容器（lane-l9-worker）在跑，且与 API 共用
    WORKER_QUEUE_NAME=lane-l9-queue；否则任务会被主栈 worker 抢走。
    """
    print("T11 端到端评估（真实 LLM 打分）")

    if not generator_provider_id:
        print("  输入缺失: 数据集未设置 provider_id，无法构造裁判夹具")
        return

    dimension_key = sql_scalar(
        "SELECT key FROM eval_dimensions WHERE is_active ORDER BY id LIMIT 1;")
    if not dimension_key:
        print("  输入缺失: eval_dimensions 表里没有启用的维度（L8 的 seed 未跑）")
        return

    # 临时裁判：base_url 与加密后的 api_key 复制自生成者 provider（同一个网关、
    # 同一个 APP_ENCRYPTION_KEY，worker 能正常解密），但 **model 必须不同**。
    # 因为 L7 的剔除规则把「同 BaseURL + 同 Model」判为与生成者同源
    # （见 internal/eval/judge.go 的 sourceKey），同 model 的夹具会被直接剔除。
    judge_id = sql_scalar(
        "INSERT INTO model_providers "
        "(name, base_url, model, provider_type, is_active, api_key_masked, encrypted_api_key, timeout_seconds) "
        f"SELECT 'L9-e2e-judge', base_url, '{E2E_JUDGE_MODEL}', provider_type, TRUE, '***', "
        f"encrypted_api_key, timeout_seconds FROM model_providers WHERE id = {generator_provider_id} "
        "RETURNING id;")
    if not judge_id:
        print("  输入缺失: 无法复制生成者 provider 作为裁判夹具")
        return
    judge_id = int(judge_id)

    # 第二个临时裁判，**故意不选进 judgeProviderIds**。
    # 它专门验证 worker 只使用运行选定的裁判：早期实现用 LoadJudgeRefs 返回的
    # 全部可用 provider 打分，会把用户没选的模型也拉进来评。
    unselected_id = sql_scalar(
        "INSERT INTO model_providers "
        "(name, base_url, model, provider_type, is_active, api_key_masked, encrypted_api_key, timeout_seconds) "
        f"SELECT 'L9-e2e-judge-unselected', base_url, '{E2E_JUDGE_MODEL}', provider_type, TRUE, '***', "
        f"encrypted_api_key, timeout_seconds FROM model_providers WHERE id = {generator_provider_id} "
        "RETURNING id;")
    unselected_id = int(unselected_id) if unselected_id else 0

    run_id = None
    try:
        status, body = client.post("/api/v1/eval/runs", {
            "datasetId": dataset_id,
            "name": "L9-接口测试-e2e",
            "samplingMode": "count",
            "sampleSize": 1,
            "dimensionKeys": [dimension_key],
            "judgeProviderIds": [judge_id],
        })
        check("T11 创建 e2e 运行", status == 200 and isinstance(body, dict), f"status={status} body={body}")
        if status != 200:
            return
        run_id = body.get("id")
        check("T11 运行选定裁判落库",
              isinstance(body.get("judgeProviderIds"), list) and judge_id in body["judgeProviderIds"],
              f"judgeProviderIds={body.get('judgeProviderIds')}")

        status, detail = client.get(f"/api/v1/eval/runs/{run_id}")
        judges = detail.get("judges", []) if isinstance(detail, dict) else []
        mine = next((j for j in judges if j.get("providerId") == judge_id), None)
        check("T11 裁判未被剔除", mine is not None and mine.get("excluded") is False,
              f"judges={judges}")

        clear_dedup("eval.run", dataset_id)
        status, body = client.post(f"/api/v1/eval/runs/{run_id}/start")
        check("T11 start 返回 202", status == 202, f"status={status} body={body}")
        check("T11 start 含已入队消息",
              isinstance(body, dict) and "已入队" in body.get("message", ""),
              f"message={body.get('message') if isinstance(body, dict) else body}")
        if status != 202:
            return

        # 轮询到终态。真实推理模型单次响应 30~120 秒，1 条 × 1 维度留 600 秒余量。
        final = None
        deadline = time.time() + 600
        while time.time() < deadline:
            _, detail = client.get(f"/api/v1/eval/runs/{run_id}")
            final = detail.get("run", {}) if isinstance(detail, dict) else {}
            if final.get("status") in ("completed", "partial_failed", "failed"):
                break
            time.sleep(5)

        print(f"  运行终态: status={final.get('status')} totalItems={final.get('totalItems')} "
              f"scoredItems={final.get('scoredItems')} errorSummary={final.get('errorSummary')}")
        check("T11 抽样落库 1 条", final.get("totalItems") == 1, f"totalItems={final.get('totalItems')}")
        check("T11 进度真实递增到 1", final.get("scoredItems") == 1, f"scoredItems={final.get('scoredItems')}")
        check("T11 终态为 completed", final.get("status") == "completed", f"status={final.get('status')}")

        status, items = client.get(f"/api/v1/eval/runs/{run_id}/items")
        payload = items[0].get("payload", {}) if isinstance(items, list) and items else {}
        check("T11 item payload 含被评数据",
              bool(payload.get("question")) and "reasoning" in payload and "answer" in payload,
              f"payload keys={list(payload.keys())}")

        # 分数直接从库里核对：接口层没有暴露 scores（那是 L10 的 /report 与 /scores）。
        raw = sql(
            "SELECT count(*) || '|' || min(score) || '|' || max(score) || '|' || min(status) "
            f"|| '|' || min(length(rationale)) FROM eval_item_scores WHERE eval_run_id = {run_id};")
        print(f"  eval_item_scores: {raw}")
        sample = sql(
            "SELECT dimension_key || ' <- ' || left(replace(rationale, E'\\n', ' '), 160) "
            f"FROM eval_item_scores WHERE eval_run_id = {run_id} ORDER BY id LIMIT 1;")
        print(f"  样例评分: {sample}")
        parts = raw.split("|") if raw else []
        check("T11 分数已落库且状态为 scored",
              len(parts) == 5 and int(parts[0]) >= 1 and parts[3] == "scored", f"raw={raw}")

        # 核心回归断言：未选定的裁判一个分数都不应该有。
        stray = sql(
            "SELECT count(*) FROM eval_item_scores "
            f"WHERE eval_run_id = {run_id} AND judge_provider_id <> {judge_id};")
        check("T11 未选定的裁判未参与打分",
              len(parts) == 5 and int(parts[0]) == 1 and stray == "0",
              f"rows={parts[0] if parts else '?'} stray={stray} unselected={unselected_id}")
        chosen = sql(
            "SELECT DISTINCT judge_provider_id FROM eval_item_scores "
            f"WHERE eval_run_id = {run_id};")
        check("T11 分数只来自选定裁判", chosen == str(judge_id),
              f"chosen={chosen} expected={judge_id}")
        if len(parts) == 5 and parts[0] != "0":
            low, high = float(parts[1]), float(parts[2])
            check("T11 分数落在维度 scale 区间内（越界已夹紧）",
                  0 <= low <= 10 and 0 <= high <= 10, f"min={low} max={high}")
            check("T11 评分理由非空", int(parts[4]) > 0, f"rationale_len={parts[4]}")
    finally:
        # 清理夹具：删 run 会级联删掉 items/scores/judges。
        if run_id:
            sql(f"DELETE FROM eval_runs WHERE id = {run_id};")
        sql(f"DELETE FROM model_providers WHERE id IN ({judge_id}, {unselected_id});")
        print(f"  夹具已清理（run={run_id} judge_provider={judge_id} unselected={unselected_id}）")


def main():
    client = Client()

    # ---- T1 登录 ----
    print("T1 登录")
    status, body = client.post("/api/v1/auth/login", {"email": EMAIL, "password": PASSWORD})
    check("T1 登录成功", status == 200, f"status={status} body={body}")

    # 找一个真实存在的数据集用于创建运行。
    dataset_id = sql("SELECT id FROM datasets ORDER BY id ASC LIMIT 1;")
    if not dataset_id:
        print("输入缺失: 共享 postgres 里没有任何数据集，无法创建评估运行")
        return 2
    dataset_id = int(dataset_id)
    print(f"  使用数据集 id={dataset_id}")

    # 清理本测试可能遗留的旧运行，保证断言不受上轮残留影响。
    sql(f"DELETE FROM eval_runs WHERE name LIKE 'L9-接口测试%';")

    # ---- T2 创建运行 ----
    print("T2 创建评估运行")
    status, body = client.post("/api/v1/eval/runs", {
        "datasetId": dataset_id,
        "name": "L9-接口测试-创建",
        "samplingMode": "count",
        "sampleSize": 2,
        "targetKind": "sft",
    })
    check("T2 创建返回 200", status == 200, f"status={status} body={body}")
    run_id = body.get("id") if isinstance(body, dict) else None
    check("T2 返回运行 id", isinstance(run_id, int) and run_id > 0, f"body={body}")
    check("T2 初始状态为 draft", isinstance(body, dict) and body.get("status") == "draft",
          f"status={body.get('status') if isinstance(body, dict) else body}")
    check("T2 抽样参数回显", isinstance(body, dict) and body.get("samplingMode") == "count"
          and body.get("sampleSize") == 2, f"body={body}")
    check("T2 未指定维度时自动补齐",
          isinstance(body, dict) and len(body.get("dimensionKeys") or []) > 0,
          f"dimensionKeys={body.get('dimensionKeys') if isinstance(body, dict) else body}")

    if not isinstance(run_id, int) or run_id <= 0:
        print("\n无法继续：创建运行失败")
        return report()

    # ---- T3 列出运行 ----
    print("T3 列出运行")
    status, body = client.get(f"/api/v1/eval/runs?datasetId={dataset_id}")
    check("T3 列表返回 200", status == 200, f"status={status}")
    check("T3 列表包含刚创建运行",
          isinstance(body, list) and any(item.get("id") == run_id for item in body),
          f"body={body}")

    # ---- T4 运行详情 ----
    print("T4 运行详情")
    status, body = client.get(f"/api/v1/eval/runs/{run_id}")
    check("T4 详情返回 200", status == 200, f"status={status} body={body}")
    check("T4 详情含 run/judges/dimensions 三字段",
          isinstance(body, dict) and {"run", "judges", "dimensions"} <= set(body.keys()),
          f"keys={list(body.keys()) if isinstance(body, dict) else body}")
    check("T4 run.id 与请求一致",
          isinstance(body, dict) and body.get("run", {}).get("id") == run_id,
          f"body={body}")

    # ---- T7 三种抽样模式 ----
    print("T7 抽样模式 full / ratio / count")
    for mode, extra in (("full", {}), ("ratio", {"sampleRatio": 0.5}), ("count", {"sampleSize": 3})):
        status, body = client.post("/api/v1/eval/runs", {
            "datasetId": dataset_id,
            "name": f"L9-接口测试-{mode}",
            "samplingMode": mode,
            **extra,
        })
        check(f"T7 {mode} 模式创建成功", status == 200 and isinstance(body, dict)
              and body.get("samplingMode") == mode, f"status={status} body={body}")

    # ---- T8 非法抽样参数 ----
    print("T8 非法抽样参数被拒")
    for name, payload in (
        ("ratio=0", {"samplingMode": "ratio", "sampleRatio": 0}),
        ("ratio 负数", {"samplingMode": "ratio", "sampleRatio": -0.5}),
        ("count=0", {"samplingMode": "count", "sampleSize": 0}),
        ("未知模式", {"samplingMode": "whatever"}),
    ):
        status, body = client.post("/api/v1/eval/runs",
                                   {"datasetId": dataset_id, "name": f"L9-接口测试-非法-{name}", **payload})
        check(f"T8 拒绝 {name}", status == 400 and isinstance(body, dict) and "error" in body,
              f"status={status} body={body}")

    # ---- T9/T10 不存在资源 ----
    print("T9/T10 不存在的资源")
    status, body = client.get("/api/v1/eval/runs/999999999")
    check("T9 不存在的运行返回 404", status == 404, f"status={status} body={body}")
    status, body = client.get("/api/v1/eval/runs/999999999/items")
    check("T9 不存在运行的 items 返回 404", status == 404, f"status={status} body={body}")
    status, body = client.post("/api/v1/eval/runs", {"datasetId": 999999999, "name": "L9-接口测试-坏数据集"})
    check("T10 不存在的 datasetId 返回 404", status == 404, f"status={status} body={body}")

    # ---- T5 入队（必须在清掉去重键之后） ----
    print("T5 启动评估运行")
    clear_dedup("eval.run", dataset_id)

    # 先看该运行有没有可用裁判：本地只配了生成者 provider 时，
    # 自评剔除规则会把唯一候选剔除，接口按设计返回 400。
    status, body = client.get("/api/v1/admin/eval/judges")
    judge_options = body if isinstance(body, list) else []
    usable = [item for item in judge_options if item.get("isActive")]

    status, body = client.post(f"/api/v1/eval/runs/{run_id}/start")
    if status == 202:
        check("T5 启动返回 202", True)
        check("T5 响应含已入队消息",
              isinstance(body, dict) and "已入队" in body.get("message", ""),
              f"message={body.get('message') if isinstance(body, dict) else body}")
        check("T5 state 为 queued",
              isinstance(body, dict) and body.get("state") == "queued", f"body={body}")
        check("T5 stage 为 eval",
              isinstance(body, dict) and body.get("stage") == "eval", f"body={body}")
    elif status == 400:
        # 输入缺失：没有可用的裁判模型（生成者禁止自评）。
        # 这是环境配置限制，如实报告，不伪装成通过。
        print(f"  输入缺失: 启动被拒（{body}）—— 本地 provider 不足以充当裁判")
        check("T5 无可用裁判时给出可读原因",
              isinstance(body, dict) and "裁判" in body.get("error", ""), f"body={body}")
    else:
        check("T5 启动返回 202 或 400", False, f"status={status} body={body}")

    # ---- T6 列出条目 ----
    print("T6 列出被评条目")
    status, body = client.get(f"/api/v1/eval/runs/{run_id}/items?limit=10&offset=0")
    check("T6 items 返回 200", status == 200, f"status={status} body={body}")
    check("T6 items 为数组", isinstance(body, list), f"type={type(body).__name__}")

    status, body = client.get(f"/api/v1/eval/runs/{run_id}/items?limit=abc")
    check("T6 非法 limit 返回 400", status == 400, f"status={status}")

    # ---- T11 端到端（真实 LLM） ----
    generator_provider_id = sql_scalar(f"SELECT provider_id FROM datasets WHERE id = {dataset_id};")
    run_e2e(client, dataset_id, int(generator_provider_id) if generator_provider_id else 0)

    # ---- 清理 ----
    sql(f"DELETE FROM eval_runs WHERE name LIKE 'L9-接口测试%';")

    return report()


def report():
    print()
    print(f"通过 {len(PASSED)} · 失败 {len(FAILED)}")
    if FAILED:
        print("失败项：")
        for name in FAILED:
            print(f"  - {name}")
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main())
