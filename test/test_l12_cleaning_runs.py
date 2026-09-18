#!/usr/bin/env python3
"""L12 数据清洗：问题 / 思维链 / 答案分步拦截 + 清洗报告 接口测试。

契约：docs/plans/eval-and-cleaning-plan.md 第 3.12 节。

被测目标：本 lane worktree 构建出的 api + worker（默认 http://127.0.0.1:18090）。
端口取自 tasks/PREAMBLE.md 的分配表（L12=18090），与 llm_default 网络里的
postgres / redis 共用数据，但使用独立队列 WORKER_QUEUE_NAME=lane-l12-queue，
避免与主栈 worker 抢任务。

## 可重入说明（必读）
所有生成类接口走 apps/api/http_util.go 的 enqueueJob，它用
`dedup:cleaning.run:<datasetID>` 做 SetNX，**TTL 10 分钟**。10 分钟内重跑同一
(datasetId)，SetNX 返回 false，任务根本不会 LPush，worker 收不到；但接口仍返回
202 + state=queued，只是 message 变成「已在队列中」。因此：
  1. 发 POST 前必须清 `dedup:cleaning.run:<datasetId>`；
  2. 断言必须检查 message 含「已入队」，识破被去重抑制的假入队。

## 测试数据
用 dataset 1 里**真实生成**的问题 / 思维链 / 答案，复制进本测试专用数据集，
保证：内容真实（非伪造）、不污染既有数据集、不依赖重新调 LLM 即可稳定复现。
关键词库由测试预置（L11 的 seed 端点在另一条并行 lane，本 worktree 未合并）。

## 测试项
  T1  POST /datasets/{id}/cleaning/run                  202 + message 含「已入队」
  T2  POST 重复入队                                     202 + message 含「已在队列中」
  T3  GET  /datasets/{id}/cleaning/runs                 含本次 run，状态 completed
  T4  GET  /cleaning/runs/{id}/report                   stages 三阶段齐全且统计正确
  T5  GET  /cleaning/runs/{id}/report                   conclusions 非空（中文结论）
  T6  GET  /cleaning/runs/{id}/findings                 命中明细非空，含拒答关键词
  T7  GET  /cleaning/runs/{id}/findings?stage=answer    只返回 answer 阶段
  T8  GET  /cleaning/runs/{id}/findings?stage=bogus     400（非法阶段名被拒绝）
  T9  POST /datasets/{id}/cleaning/run  stages=["bogus"] 400
  T10 清洗结果落库：questions.cleaning_status 被更新为 flagged/dropped
  T11 分阶段隔离：只选 question 阶段时 reasoning/answer 阶段不被扫描
  T12 GET  /cleaning/runs/99999999/report               404
"""

import os
import subprocess
import sys
import time

import requests

BASE = os.environ.get("L12_BASE_URL", "http://127.0.0.1:18090")
ADMIN_EMAIL = os.environ.get("L12_ADMIN_EMAIL", "admin@company.com")
ADMIN_PASSWORD = os.environ.get("L12_ADMIN_PASSWORD", "admin123456")

# 测试用拒答关键词：覆盖中文拒答、英文拒答、安全限制三类。
TEST_KEYWORDS = [
    ("对不起", "refusal", "block"),
    ("我不能", "refusal", "block"),
    ("我无法提供", "refusal", "block"),
    ("抱歉，我无法", "refusal", "block"),
    ("I cannot", "english_refusal", "block"),
    ("作为人工智能", "refusal", "block"),
    ("违反", "safety", "warn"),
]

RESULTS: list[tuple[str, bool, str]] = []


def record(name: str, ok: bool, detail: str) -> None:
    RESULTS.append((name, ok, detail))
    print(f"[{'PASS' if ok else 'FAIL'}] {name}: {detail}")


def as_dict(payload: object) -> dict:
    return payload if isinstance(payload, dict) else {}


def as_list(payload: object) -> list:
    return payload if isinstance(payload, list) else []


def json_body(res: requests.Response) -> object:
    try:
        return res.json()
    except ValueError:
        return {"raw": res.text[:200]}


def psql(sql: str) -> tuple[bool, str]:
    """在共享 postgres 容器里执行 SQL，返回 (成功, 输出)。"""
    try:
        out = subprocess.run(
            ["docker", "exec", "-i", "llm-postgres-1", "psql", "-U", "llm_factory",
             "-d", "llm_factory", "-tA", "-v", "ON_ERROR_STOP=1"],
            input=sql, capture_output=True, text=True, timeout=60,
        )
    except (OSError, subprocess.SubprocessError) as err:
        return False, str(err)
    if out.returncode != 0:
        return False, out.stderr.strip()[:400]
    return True, out.stdout.strip()


def clear_dedup(dataset_id: int) -> None:
    """清理入队去重键，保证测试可重复运行（见模块 docstring）。"""
    subprocess.run(
        ["docker", "exec", "llm-redis-1", "redis-cli", "del", f"dedup:cleaning.run:{dataset_id}"],
        capture_output=True, check=False, timeout=30,
    )


def seed_keywords() -> bool:
    """预置关键词库。L11 的 seed 端点属于另一条并行 lane，此处直接落库。"""
    values = ",".join(
        f"('{pattern}','{category}','{severity}')" for pattern, category, severity in TEST_KEYWORDS
    )
    sql = (
        "INSERT INTO cleaning_keywords (pattern, category, severity, is_active) VALUES "
        f"{values} ON CONFLICT (pattern, category) DO UPDATE SET is_active = TRUE;"
    )
    ok, detail = psql(sql)
    if not ok:
        print(f"  （预置关键词失败：{detail}）")
    return ok


def setup_dataset(stamp: int) -> tuple[int | None, str]:
    """建测试数据集，并复制 dataset 1 的真实问题/思维链/答案作为清洗素材。

    复制而非重新调 LLM：内容仍是真实生成结果，但测试不必等 30~120 秒的模型响应，
    也不受模型随机性影响。同时把一条答案改写成典型拒答，确保必然命中。
    """
    ok, detail = psql(f"""
        INSERT INTO datasets (name, root_keyword, target_size, status)
        VALUES ('l12-cleaning-test-{stamp}', '军事', 10, 'draft');
    """)
    if not ok:
        return None, f"建数据集失败：{detail}"

    ok, out = psql(f"SELECT id FROM datasets WHERE name = 'l12-cleaning-test-{stamp}';")
    if not ok or not out.strip():
        return None, f"读回数据集 id 失败：{out}"
    dataset_id = int(out.strip().splitlines()[0])

    # 复制真实问题（保留原 content / difficulty，作为清洗素材）。
    ok, detail = psql(f"""
        INSERT INTO questions (dataset_id, domain_id, content, canonical_hash, status, difficulty)
        SELECT {dataset_id}, domain_id, content, canonical_hash || '-l12-{stamp}', 'generated', difficulty
        FROM questions WHERE dataset_id = 1 ORDER BY id LIMIT 10;
    """)
    if not ok:
        return None, f"复制问题失败：{detail}"

    # 复制真实思维链与答案；把其中一条答案改成典型拒答文本，保证答案阶段必然命中。
    ok, detail = psql(f"""
        INSERT INTO reasoning_records (dataset_id, question_id, answer_summary, reasoning, object_key, status)
        SELECT {dataset_id}, q.id, r.answer_summary, r.reasoning, 'l12/{stamp}/' || q.id, 'generated'
        FROM questions q
        JOIN reasoning_records r ON r.question_id = q.id
        WHERE q.dataset_id = {dataset_id} AND r.dataset_id = 1
        ON CONFLICT (question_id) DO NOTHING;
    """)
    if not ok:
        return None, f"复制推理记录失败：{detail}"

    # 注入拒答样本：问题阶段 1 条、思维链阶段 1 条、答案阶段 2 条。
    ok, detail = psql(f"""
        UPDATE questions SET content = content || ' 对不起，我不能回答这个。'
        WHERE id = (SELECT id FROM questions WHERE dataset_id = {dataset_id} ORDER BY id LIMIT 1);
        UPDATE reasoning_records SET reasoning = reasoning || ' 作为人工智能，我无法提供此类内容。'
        WHERE question_id = (SELECT id FROM questions WHERE dataset_id = {dataset_id} ORDER BY id LIMIT 1);
        UPDATE reasoning_records SET answer_summary = '对不起，我不能帮你做这个。'
        WHERE question_id = (SELECT id FROM questions WHERE dataset_id = {dataset_id} ORDER BY id OFFSET 1 LIMIT 1);
        UPDATE reasoning_records SET answer_summary = answer_summary || ' I cannot help with that request.'
        WHERE question_id = (SELECT id FROM questions WHERE dataset_id = {dataset_id} ORDER BY id OFFSET 2 LIMIT 1);
    """)
    if not ok:
        return None, f"注入拒答样本失败：{detail}"

    ok, out = psql(f"SELECT count(*) FROM questions WHERE dataset_id = {dataset_id};")
    return dataset_id, f"dataset_id={dataset_id} questions={out}"


def poll_run_completed(session: requests.Session, dataset_id: int, run_id: int, timeout: float = 180.0) -> dict | None:
    """轮询直到本次 run 变成 completed/failed。"""
    deadline = time.time() + timeout
    last: dict = {}
    while time.time() < deadline:
        res = session.get(f"{BASE}/api/v1/datasets/{dataset_id}/cleaning/runs", timeout=30)
        for item in as_list(json_body(res)):
            entry = as_dict(item)
            if entry.get("id") == run_id:
                last = entry
                if entry.get("status") in ("completed", "failed"):
                    return entry
        time.sleep(3)
    return last or None


def main() -> int:
    session = requests.Session()
    login = session.post(
        f"{BASE}/api/v1/auth/login",
        json={"email": ADMIN_EMAIL, "password": ADMIN_PASSWORD},
        timeout=30,
    )
    if login.status_code != 200 or not session.cookies.get("llm_session"):
        print(f"无法登录被测 api：status={login.status_code} body={login.text[:200]}")
        return 2

    stamp = int(time.time())
    if not seed_keywords():
        record("前置：预置拒答关键词库", False, "关键词落库失败")
        return 1
    record("前置：预置拒答关键词库", True, f"{len(TEST_KEYWORDS)} 条")

    dataset_id, detail = setup_dataset(stamp)
    if dataset_id is None:
        record("前置：准备测试数据集", False, detail)
        return 1
    record("前置：准备测试数据集", True, detail)

    # T9 先跑：非法阶段名必须 400，且不应产生任何 run 记录。
    res = session.post(
        f"{BASE}/api/v1/datasets/{dataset_id}/cleaning/run",
        json={"stages": ["bogus"]}, timeout=60,
    )
    record("T9 非法阶段名返回 400", res.status_code == 400,
           f"status={res.status_code} error={as_dict(json_body(res)).get('error')}")

    # T1：正常入队。
    clear_dedup(dataset_id)
    res = session.post(
        f"{BASE}/api/v1/datasets/{dataset_id}/cleaning/run",
        json={"stages": ["question", "reasoning", "answer"]}, timeout=60,
    )
    body = as_dict(json_body(res))
    record("T1 入队返回 202 + 已入队",
           res.status_code == 202 and "已入队" in str(body.get("message", "")),
           f"status={res.status_code} message={body.get('message')}")

    # T2：去重键仍在有效期内，第二次 POST 必须走「已在队列中」分支。
    res2 = session.post(
        f"{BASE}/api/v1/datasets/{dataset_id}/cleaning/run",
        json={"stages": ["question", "reasoning", "answer"]}, timeout=60,
    )
    body2 = as_dict(json_body(res2))
    record("T2 重复入队返回已在队列中",
           res2.status_code == 202 and "已在队列中" in str(body2.get("message", "")),
           f"status={res2.status_code} message={body2.get('message')}")

    # T3：run 落库且最终 completed。
    res = session.get(f"{BASE}/api/v1/datasets/{dataset_id}/cleaning/runs", timeout=30)
    runs = [as_dict(item) for item in as_list(json_body(res))]
    if not runs:
        record("T3 清洗运行落库", False, f"无 run 记录，body={res.text[:200]}")
        return 1
    run = poll_run_completed(session, dataset_id, runs[0]["id"])
    run_id = runs[0]["id"]
    record("T3 清洗运行落库并完成",
           bool(run) and run.get("status") == "completed",
           f"run_id={run_id} status={run.get('status') if run else None}")

    if not run or run.get("status") != "completed":
        print(f"  运行未完成，错误：{run.get('errorSummary') if run else '未知'}")
        summarize()
        return 1

    # T4 / T5：报告内容。
    res = session.get(f"{BASE}/api/v1/cleaning/runs/{run_id}/report", timeout=30)
    report = as_dict(json_body(res))
    stages = [as_dict(item) for item in as_list(report.get("stages"))]
    stage_names = {str(item.get("stage")) for item in stages}
    scanned_total = sum(int(item.get("scannedItems") or 0) for item in stages)
    flagged_total = sum(int(item.get("flaggedItems") or 0) for item in stages)
    record("T4 报告含三阶段统计",
           res.status_code == 200 and stage_names == {"question", "reasoning", "answer"} and scanned_total > 0,
           f"status={res.status_code} stages={sorted(stage_names)} scanned={scanned_total} flagged={flagged_total}")

    hit_rates_ok = all(
        (0.0 <= float(item.get("hitRate") or 0) <= 1.0) for item in stages
    )
    record("T4b 各阶段 hitRate 在 [0,1] 区间", hit_rates_ok,
           f"hitRates={[item.get('hitRate') for item in stages]}")

    conclusions = as_list(report.get("conclusions"))
    record("T5 报告结论非空",
           len(conclusions) >= 3 and any("阶段" in str(c) for c in conclusions),
           f"{len(conclusions)} 条，首条：{conclusions[0] if conclusions else '无'}")

    top_keywords = [as_dict(item) for item in as_list(report.get("topKeywords"))]
    record("T5b Top 关键词按命中数倒序",
           bool(top_keywords) and all(
               int(top_keywords[i].get("hits") or 0) >= int(top_keywords[i + 1].get("hits") or 0)
               for i in range(len(top_keywords) - 1)
           ),
           f"{len(top_keywords)} 个，首位={top_keywords[0].get('pattern') if top_keywords else None}×{top_keywords[0].get('hits') if top_keywords else 0}")

    # T6：命中明细。
    res = session.get(f"{BASE}/api/v1/cleaning/runs/{run_id}/findings", timeout=30)
    findings = [as_dict(item) for item in as_list(json_body(res))]
    stages_seen = {str(item.get("stage")) for item in findings}
    patterns = {str(item.get("matchedText")) for item in findings}
    record("T6 命中明细非空且覆盖三阶段",
           len(findings) > 0 and stages_seen == {"question", "reasoning", "answer"},
           f"{len(findings)} 条，阶段={sorted(stages_seen)}，命中词={sorted(patterns)[:5]}")

    # T7：按 stage 过滤。
    res = session.get(f"{BASE}/api/v1/cleaning/runs/{run_id}/findings", params={"stage": "answer"}, timeout=30)
    answer_findings = [as_dict(item) for item in as_list(json_body(res))]
    record("T7 findings 支持 stage 过滤",
           res.status_code == 200 and len(answer_findings) > 0
           and all(item.get("stage") == "answer" for item in answer_findings),
           f"status={res.status_code} answer 阶段 {len(answer_findings)} 条")

    # T8：非法 stage 必须被拒绝，不能静默返回全量。
    res = session.get(f"{BASE}/api/v1/cleaning/runs/{run_id}/findings", params={"stage": "bogus"}, timeout=30)
    record("T8 非法 stage 返回 400", res.status_code == 400,
           f"status={res.status_code} error={as_dict(json_body(res)).get('error')}")

    # T10：清洗结果写回 questions.cleaning_status。
    ok, out = psql(
        f"SELECT cleaning_status, count(*) FROM questions WHERE dataset_id = {int(dataset_id)} "
        "GROUP BY cleaning_status ORDER BY cleaning_status;"
    )
    status_counts = dict(line.split("|") for line in out.splitlines() if "|" in line) if ok else {}
    touched = int(status_counts.get("flagged", 0)) + int(status_counts.get("dropped", 0))
    record("T10 cleaning_status 写回 questions", ok and touched > 0,
           f"状态分布={status_counts}")

    # T12：不存在的 run 返回 404。
    res = session.get(f"{BASE}/api/v1/cleaning/runs/99999999/report", timeout=30)
    record("T12 不存在的 run 返回 404", res.status_code == 404,
           f"status={res.status_code} error={as_dict(json_body(res)).get('error')}")

    # T11：分阶段隔离——只选 question 阶段时，其余阶段不应被扫描。
    qs_dataset_id, detail = setup_dataset(stamp + 1)
    if qs_dataset_id is None:
        record("T11 分阶段隔离", False, f"前置失败：{detail}")
    else:
        clear_dedup(qs_dataset_id)
        res = session.post(
            f"{BASE}/api/v1/datasets/{qs_dataset_id}/cleaning/run",
            json={"stages": ["question"]}, timeout=60,
        )
        body = as_dict(json_body(res))
        if res.status_code != 202 or "已入队" not in str(body.get("message", "")):
            record("T11 分阶段隔离", False, f"入队失败 status={res.status_code} message={body.get('message')}")
        else:
            runs_res = session.get(f"{BASE}/api/v1/datasets/{qs_dataset_id}/cleaning/runs", timeout=30)
            qs_runs = [as_dict(item) for item in as_list(json_body(runs_res))]
            qs_run = poll_run_completed(session, qs_dataset_id, qs_runs[0]["id"]) if qs_runs else None
            if not qs_run or qs_run.get("status") != "completed":
                record("T11 分阶段隔离", False, f"运行未完成：{qs_run.get('status') if qs_run else '无记录'}")
            else:
                rep = as_dict(json_body(session.get(
                    f"{BASE}/api/v1/cleaning/runs/{qs_run['id']}/report", timeout=30)))
                qs_stages = {as_dict(item).get("stage"): as_dict(item) for item in as_list(rep.get("stages"))}
                # 只选 question：reasoning/answer 的 scannedItems 必须为 0。
                isolated = (
                    int(qs_stages.get("question", {}).get("scannedItems") or 0) > 0
                    and int(qs_stages.get("reasoning", {}).get("scannedItems") or 0) == 0
                    and int(qs_stages.get("answer", {}).get("scannedItems") or 0) == 0
                )
                scanned = {k: v.get("scannedItems") for k, v in qs_stages.items()}
                record("T11 分阶段隔离：仅扫描选中阶段", isolated, f"scanned={scanned}")
    # 清理去重键，避免影响后续重跑。
    clear_dedup(dataset_id)
    return summarize()


def summarize() -> int:
    passed = sum(1 for _, ok, _ in RESULTS if ok)
    failed = len(RESULTS) - passed
    print(f"\n===== L12 接口测试：通过 {passed} · 失败 {failed} · 共 {len(RESULTS)} =====")
    for name, ok, detail in RESULTS:
        if not ok:
            print(f"  FAIL {name}: {detail}")
    return 0 if failed == 0 else 1


if __name__ == "__main__":
    sys.exit(main())
