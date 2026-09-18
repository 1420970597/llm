"""L13 前端评估模块 —— L9/L10 端到端联调（真实 LLM 打分 + 报告）。

L13 自身是纯前端 lane，本脚本验证的是**前端 UI 所消费的 L9/L10 响应结构**在真实
运行中确实成立：创建运行 → 启动 → 轮询 → 报告 → 逐条打分明细 → 更新裁判。

前置条件（缺一即如实报「输入缺失」，不得伪造通过）：
  - 后端镜像由「lane/L13 + lane/L9 + lane/L10」合并后的代码构建（L9/L10 尚未合并进 main）。
  - worker 与本 API 使用同一个私有队列名 WORKER_QUEUE_NAME=lane-l13-queue，
    否则任务会被主栈 llm-worker-1 抢走。
  - 真实 provider（id=1）可用；打分是真实 LLM 调用，单条可能耗时 30~120 秒。

入队去重注意（PREAMBLE）：L9 的 start 走 enqueueJob，去重键是
dedup:eval.run:<datasetId>（jobType=eval.run，键里是 datasetID 而不是 runId），
TTL 10 分钟。脚本在 POST start 前按真实键清理，并断言 message 含「已入队」，
以免「被去重抑制」被误判为成功。

运行：
    L13_BASE=http://127.0.0.1:18092 python3 test/test_l13_eval_e2e.py
"""
import json
import os
import subprocess
import sys
import time
import urllib.error
import urllib.request

BASE = os.environ.get("L13_BASE", "http://127.0.0.1:18092") + "/api/v1"
DATASET_ID = int(os.environ.get("L13_DATASET_ID", "98"))
JUDGE_IDS = [int(x) for x in os.environ.get("L13_JUDGE_IDS", "1,2").split(",")]
DIMENSIONS = os.environ.get("L13_DIMENSIONS", "long_chain_coherence,faithfulness").split(",")
POLL_TIMEOUT_S = int(os.environ.get("L13_POLL_TIMEOUT", "900"))

COOKIE = ""
results = []


def call(method, path, body=None):
    data = json.dumps(body).encode() if body is not None else None
    req = urllib.request.Request(BASE + path, data=data, method=method)
    req.add_header("Content-Type", "application/json")
    if COOKIE:
        req.add_header("Cookie", COOKIE)
    try:
        with urllib.request.urlopen(req, timeout=120) as resp:
            raw = resp.read().decode()
            return resp.status, (json.loads(raw) if raw else None)
    except urllib.error.HTTPError as exc:
        raw = exc.read().decode()
        try:
            return exc.code, (json.loads(raw) if raw else None)
        except json.JSONDecodeError:
            return exc.code, raw


def clear_dedup(job_type, dataset_id):
    """清理入队去重键，保证测试可重复运行。键 = dedup:<jobType>:<datasetId>。"""
    subprocess.run(
        ["docker", "exec", "llm-redis-1", "redis-cli", "del", f"dedup:{job_type}:{dataset_id}"],
        capture_output=True, check=False,
    )


def check(name, condition, detail):
    results.append((name, condition, detail))
    print(f"[{'PASS' if condition else 'FAIL'}] {name} :: {detail}", flush=True)


def login():
    global COOKIE
    req = urllib.request.Request(
        BASE + "/auth/login",
        data=json.dumps({"email": "admin@company.com", "password": "admin123456"}).encode(),
        method="POST",
    )
    req.add_header("Content-Type", "application/json")
    with urllib.request.urlopen(req, timeout=60) as resp:
        COOKIE = resp.headers.get("Set-Cookie", "").split(";")[0]
    return COOKIE.startswith("llm_session=")


def main():
    if not login():
        print("登录失败，无法继续")
        return 1
    print(f"已登录；目标 {BASE}，dataset={DATASET_ID}，judges={JUDGE_IDS}，dimensions={DIMENSIONS}", flush=True)

    # ---- L9：创建运行 ----
    payload = {
        "datasetId": DATASET_ID,
        "name": f"L13-UI-联调-{int(time.time())}",
        "samplingMode": "full",
        "dimensionKeys": DIMENSIONS,
        "judgeProviderIds": JUDGE_IDS,
    }
    status, run = call("POST", "/eval/runs", payload)
    if status not in (200, 201) or not isinstance(run, dict):
        check("POST /eval/runs 创建运行", False, f"status={status} body={run}")
        return 1
    check("POST /eval/runs 创建运行", True, f"status={status} runId={run.get('id')} status={run.get('status')}")
    run_id = run["id"]
    for field in ("samplingMode", "sampleRatio", "sampleSize", "dimensionKeys", "judgeProviderIds", "totalItems", "scoredItems"):
        check(f"EvalRun 含字段 {field}", field in run, f"{field}={run.get(field)!r}")

    # ---- L9：启动（必须清理去重键，否则 202 但未真正入队）----
    clear_dedup("eval.run", DATASET_ID)
    status, enqueue = call("POST", f"/eval/runs/{run_id}/start")
    message = enqueue.get("message", "") if isinstance(enqueue, dict) else ""
    check("POST /eval/runs/{id}/start 返回 202", status == 202, f"status={status} body={enqueue}")
    check("入队 message 含「已入队」（识破去重抑制）", "已入队" in message, f"message={message!r}")

    # ---- 轮询至完成 ----
    deadline = time.time() + POLL_TIMEOUT_S
    detail = None
    while time.time() < deadline:
        _, detail = call("GET", f"/eval/runs/{run_id}")
        state = detail.get("run", {}).get("status") if isinstance(detail, dict) else None
        scored = detail.get("run", {}).get("scoredItems") if isinstance(detail, dict) else None
        total = detail.get("run", {}).get("totalItems") if isinstance(detail, dict) else None
        print(f"    ... status={state} scored={scored}/{total}", flush=True)
        if state in ("completed", "failed", "partial_failed"):
            break
        time.sleep(10)
    run_state = detail.get("run", {}).get("status") if isinstance(detail, dict) else None
    check("运行收敛到终态", run_state in ("completed", "failed", "partial_failed"), f"status={run_state}")

    # ---- L9：运行详情结构（EvalRunDetail 渲染依据）----
    check("GET /eval/runs/{id} 含 run/judges/dimensions", isinstance(detail, dict) and {"run", "judges", "dimensions"} <= set(detail.keys()), f"keys={sorted(detail.keys()) if isinstance(detail, dict) else detail}")
    judges = detail.get("judges", []) if isinstance(detail, dict) else []
    if judges:
        jreq = {"providerId", "providerName", "model", "excluded", "excludeReason", "status", "scoredItems"}
        check("运行裁判含 excluded/excludeReason/status/scoredItems", not (jreq - set(judges[0].keys())), f"missing={sorted(jreq - set(judges[0].keys())) or '无'}")
    else:
        check("运行裁判非空", False, "详情未返回裁判")
    dims = detail.get("dimensions", []) if isinstance(detail, dict) else []
    check("运行维度非空且与请求一致", len(dims) == len(DIMENSIONS), f"返回 {len(dims)} 个，请求 {len(DIMENSIONS)} 个")

    # ---- L9：评估条目 ----
    status, items = call("GET", f"/eval/runs/{run_id}/items?limit=50&offset=0")
    check("GET /eval/runs/{id}/items 返回数组", status == 200 and isinstance(items, list), f"status={status} len={len(items) if isinstance(items, list) else items}")
    if isinstance(items, list) and items:
        ireq = {"id", "evalRunId", "datasetId", "questionId", "itemIndex", "payload"}
        check("EvalItem 字段完整（EvalScoreTable 关联题目用）", not (ireq - set(items[0].keys())), f"missing={sorted(ireq - set(items[0].keys())) or '无'}")

    # ---- L10：报告 ----
    status, report = call("GET", f"/eval/runs/{run_id}/report")
    if status != 200 or not isinstance(report, dict):
        check("GET /eval/runs/{id}/report 返回 200", False, f"status={status} body={str(report)[:200]}")
        return 1
    check("GET /eval/runs/{id}/report 返回 200", True, f"overallScore={report.get('overallScore')} judgeAgreement={report.get('judgeAgreement')} sampleCount={report.get('sampleCount')}")
    rreq = {"evalRun", "datasetName", "overallScore", "judgeAgreement", "sampleCount", "judges", "dimensions", "weakestItems", "conclusions", "generatedAt"}
    check("EvalReport 字段完整（报告页渲染依据）", not (rreq - set(report.keys())), f"missing={sorted(rreq - set(report.keys())) or '无'}")
    check("conclusions 为后端生成的中文结论文本", isinstance(report.get("conclusions"), list), f"conclusions={report.get('conclusions')}")
    if report.get("conclusions"):
        check("结论确实为中文文本（非占位）", any("\u4e00" <= ch <= "\u9fff" for ch in "".join(report["conclusions"])), f"首条={report['conclusions'][0][:80]}")
    # -1 是后端「不适用」哨兵（样本不足）；只有非负值才要求落在 0~1。
    agreement = report.get("judgeAgreement")
    check(
        "judgeAgreement 为 -1 哨兵或 0~1 之间的数值",
        isinstance(agreement, (int, float)) and (agreement == -1 or 0 <= agreement <= 1),
        f"judgeAgreement={agreement}",
    )
    check("overallScore 为有限数值", isinstance(report.get("overallScore"), (int, float)), f"overallScore={report.get('overallScore')}")
    if report.get("judges"):
        j0 = report["judges"][0]
        jreq2 = {"providerId", "providerName", "model", "score", "sampleCount", "dimensions", "itemScores"}
        check("EvalJudgeStat 字段完整", not (jreq2 - set(j0.keys())), f"missing={sorted(jreq2 - set(j0.keys())) or '无'}")
        check("该裁判确有样本被打分", j0.get("sampleCount", 0) > 0, f"sampleCount={j0.get('sampleCount')} score={j0.get('score')}")
    if report.get("dimensions"):
        d0 = report["dimensions"][0]
        dreq = {"dimensionKey", "name", "category", "score", "sampleCount", "stdDev", "min", "max"}
        check("EvalDimensionStat 字段完整", not (dreq - set(d0.keys())), f"missing={sorted(dreq - set(d0.keys())) or '无'}")
    check("weakestItems 为数组", isinstance(report.get("weakestItems"), list), f"len={len(report.get('weakestItems') or [])}")

    # ---- L10：逐条打分（可按裁判/维度过滤）----
    status, scores = call("GET", f"/eval/runs/{run_id}/scores")
    check("GET /eval/runs/{id}/scores 返回数组", status == 200 and isinstance(scores, list), f"status={status} len={len(scores) if isinstance(scores, list) else scores}")
    if isinstance(scores, list) and scores:
        sreq = {"id", "evalRunId", "evalItemId", "judgeProviderId", "dimensionKey", "score", "rationale", "status"}
        check("EvalItemScore 字段完整（明细表渲染依据）", not (sreq - set(scores[0].keys())), f"missing={sorted(sreq - set(scores[0].keys())) or '无'}")
        check("打分 rationale 非空（真实 LLM 理由）", any(s.get("rationale") for s in scores), f"有理由的条数={sum(1 for s in scores if s.get('rationale'))}/{len(scores)}")
        status_f, filtered_j = call("GET", f"/eval/runs/{run_id}/scores?judgeProviderId={JUDGE_IDS[0]}")
        check("按裁判过滤生效", isinstance(filtered_j, list) and all(s["judgeProviderId"] == JUDGE_IDS[0] for s in filtered_j), f"过滤后 {len(filtered_j) if isinstance(filtered_j, list) else filtered_j} 条")
        status_f, filtered_d = call("GET", f"/eval/runs/{run_id}/scores?dimensionKey={DIMENSIONS[0]}")
        check("按维度过滤生效", isinstance(filtered_d, list) and all(s["dimensionKey"] == DIMENSIONS[0] for s in filtered_d), f"过滤后 {len(filtered_d) if isinstance(filtered_d, list) else filtered_d} 条")

    # ---- L7：更新裁判 ----
    status, updated = call("PUT", f"/eval/runs/{run_id}/judges", {"providerIds": JUDGE_IDS})
    check("PUT /eval/runs/{id}/judges 返回裁判数组", status == 200 and isinstance(updated, list), f"status={status} len={len(updated) if isinstance(updated, list) else updated}")

    # ---- L7：生成者自评剔除（UI 必须禁用被排除裁判并展示原因）----
    status, excluded_run = call("POST", "/eval/runs", {
        "datasetId": DATASET_ID,
        "name": f"L13-排除裁判-{int(time.time())}",
        "samplingMode": "full",
        "dimensionKeys": [DIMENSIONS[0]],
        "judgeProviderIds": JUDGE_IDS,
        "generatorProviderId": JUDGE_IDS[0],
    })
    if status in (200, 201) and isinstance(excluded_run, dict):
        _, ex_detail = call("GET", f"/eval/runs/{excluded_run['id']}")
        ex_judges = ex_detail.get("judges", []) if isinstance(ex_detail, dict) else []
        generator_judge = next((j for j in ex_judges if j.get("providerId") == JUDGE_IDS[0]), None)
        check(
            "生成者模型被判为 excluded 且有中文原因",
            bool(generator_judge) and generator_judge.get("excluded") is True and bool(generator_judge.get("excludeReason")),
            f"excluded={generator_judge and generator_judge.get('excluded')} reason={generator_judge and generator_judge.get('excludeReason')!r}",
        )
    else:
        check("创建带 generatorProviderId 的运行", False, f"status={status} body={excluded_run}")

    failed = [name for name, ok, _ in results if not ok]
    print(f"\n合计 {len(results)} 项，通过 {len(results) - len(failed)}，失败 {len(failed)}", flush=True)
    if failed:
        print("失败项：" + "; ".join(failed))
    return 1 if failed else 0


if __name__ == "__main__":
    sys.exit(main())
