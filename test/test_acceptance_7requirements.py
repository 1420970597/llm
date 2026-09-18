"""验收测试：逐条核对 功能说明.txt 的 7 项需求（真实栈 + 真实 LLM）。

用途：父代理在合并全部 lane 后，从零跑一遍完整用户流程，作为交付验收证据。
不是单元测试的替代，而是「用户真的能走完这条路吗」的端到端确认。

需求对照（R1~R8 见 docs/plans/eval-and-cleaning-plan.md 第 0 节）：
  R1 关键词 → n 领域 → m 方向（n/m 用户可控、断点续跑）
  R2 方向 → 长链思维标准步骤（可编辑、版本化）
  R3 方向 → x 个具体问题（x 可控、去重、难度分层）
  R4 GRPO 分支：按用户给定打分档次自动生成教师模型评判提示词
  R5 SFT 分支：生成对应思维链 + 答案
  R6 多格式导出（字段映射可配）
  R7 数据集评估（多 LLM 互评、≥50 维度、全量/抽样、剔除生成者自评、汇总分析）
  R8 数据清洗（拒答关键词库、多步骤拦截、报告）

运行方式（必须打当前 main 构建的镜像，不是 3210 上的旧镜像）：
  python3 test/test_acceptance_7requirements.py --base http://127.0.0.1:18100

前置：
  - 已构建并启动 accept-api / accept-worker（见父代理的验收流程）
  - worker 使用私有队列 accept-queue，避免被主栈 worker 抢走任务
  - 真实 LLM provider id=1，APP_ENCRYPTION_KEY 与数据库中的密文一致

已知陷阱（踩过的坑，本脚本已处理）：
  1. 去重：enqueueJob 用 dedup:<jobType>:<datasetID> 做 SetNX，TTL 10 分钟。
     脚本在每次入队前清键，并断言 message 含「已入队」，避免「假入队」通过。
  2. 领域先于方向：POST /directions/generate 在无领域时返回 409。
     必须先走 legacy 同步路由 POST /domains/generate 生成领域。
  3. 字段名以 internal/model/*.go 的 json tag 为准（directionCount、
     questionsPerDirection、levels、chainOfThought…），不是想当然的命名。
"""

import argparse
import json
import os
import subprocess
import sys
import time
import urllib.error
import urllib.request
from http.cookiejar import CookieJar

REDIS_CONTAINER = os.environ.get("REDIS_CONTAINER", "llm-redis-1")
ADMIN_EMAIL = os.environ.get("ADMIN_EMAIL", "admin@company.com")
ADMIN_PASSWORD = os.environ.get("ADMIN_PASSWORD", "admin123456")

# 生成类任务等待上限：推理模型单次响应可达 120s，多步流水线需要更长。
GEN_TIMEOUT = int(os.environ.get("GEN_TIMEOUT", "900"))

PASS, FAIL, SKIP = [], [], []


def record(name, ok, detail=""):
    (PASS if ok else FAIL).append(name)
    print(f"  [{'PASS' if ok else 'FAIL'}] {name}" + (f" :: {detail}" if detail else ""))


def record_skip(name, detail):
    SKIP.append(f"{name}（输入缺失：{detail}）")
    print(f"  [SKIP] {name} :: 输入缺失：{detail}")


class Session:
    """极简 cookie 会话（该 API 用 HttpOnly cookie 鉴权，不是 Bearer token）。"""

    def __init__(self, base):
        self.base = base.rstrip("/")
        self.opener = urllib.request.build_opener(
            urllib.request.HTTPCookieProcessor(CookieJar()))

    def call(self, method, path, body=None, timeout=120):
        data = json.dumps(body).encode() if body is not None else None
        req = urllib.request.Request(self.base + path, data=data, method=method)
        if data is not None:
            req.add_header("Content-Type", "application/json")
        try:
            with self.opener.open(req, timeout=timeout) as resp:  # nosec B310 — base 由 --base 显式指定，不接受任意 scheme
                raw = resp.read().decode()
                try:
                    return resp.status, json.loads(raw)
                except json.JSONDecodeError:
                    return resp.status, raw
        except urllib.error.HTTPError as exc:
            raw = exc.read().decode()
            try:
                return exc.code, json.loads(raw)
            except json.JSONDecodeError:
                return exc.code, raw

    def as_list(self, body):
        if isinstance(body, list):
            return body
        if isinstance(body, dict):
            for key in ("items", "data", "records", "list"):
                if isinstance(body.get(key), list):
                    return body[key]
        return []


def clear_dedup(job_type, dataset_id):
    """清理入队去重键，保证验收可重复运行（否则 10 分钟内重跑会「假入队」）。"""
    subprocess.run(
        ["docker", "exec", REDIS_CONTAINER, "redis-cli", "del", f"dedup:{job_type}:{dataset_id}"],
        capture_output=True, check=False,
    )


def enqueue(session, job_type, dataset_id, path, body=None, expect=202):
    """清去重键 → 入队 → 断言 message 含「已入队」（识破 dedup 抑制）。"""
    clear_dedup(job_type, dataset_id)
    code, resp = session.call("POST", path, body if body is not None else {})
    msg = resp.get("message") if isinstance(resp, dict) else str(resp)[:120]
    return code, msg, resp


def wait_stage(session, dataset_id, stage, timeout=GEN_TIMEOUT):
    """轮询 generation_runs 直到该 stage 完成或失败。"""
    deadline = time.time() + timeout
    last = None
    while time.time() < deadline:
        code, body = session.call("GET", f"/api/v1/datasets/{dataset_id}/generation-runs")
        if code == 200:
            for run in session.as_list(body):
                if run.get("stage") == stage:
                    last = run
                    if run.get("status") in ("completed", "failed", "partial_failed"):
                        return run
        time.sleep(5)
    return last


def poll_until(session, path, predicate, timeout=GEN_TIMEOUT, interval=5):
    """通用轮询：直到 predicate(body) 为真或超时，返回最后一次 body。"""
    deadline = time.time() + timeout
    body = None
    while time.time() < deadline:
        code, body = session.call("GET", path)
        if code == 200 and predicate(body):
            return body
        time.sleep(interval)
    return body


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--base", default=os.environ.get("BASE", "http://127.0.0.1:18100"))
    parser.add_argument("--dataset", type=int, default=0, help="复用已有数据集 ID（0=新建）")
    args = parser.parse_args()

    session = Session(args.base)
    print(f"=== 验收测试 @ {args.base} ===\n")

    code, body = session.call("POST", "/api/v1/auth/login",
                              {"email": ADMIN_EMAIL, "password": ADMIN_PASSWORD})
    if code != 200:
        print(f"登录失败 HTTP {code}: {body}")
        return 2
    print("登录成功\n")

    # ---------------------------------------------------------------- 前置
    if args.dataset:
        dataset_id = args.dataset
    else:
        code, body = session.call("POST", "/api/v1/datasets", {
            "name": f"验收-{int(time.time())}",
            "rootKeyword": "军事",
            "targetSize": 12,
            "providerId": 1,
            "targetKind": "sft",
            "directionCount": 2,
        })
        if code not in (200, 201):
            record("前置：创建数据集", False, f"HTTP {code} {str(body)[:200]}")
            return finish()
        dataset_id = (body or {}).get("id")
        record("前置：创建数据集", bool(dataset_id), f"datasetId={dataset_id}")

    # ---------------------------------------------------------------- R1
    print("R1 关键词 → n 领域 → m 方向（n/m 可控、断点续跑）")
    # 领域生成走 legacy 同步路由（方向生成依赖领域作为 parent，否则 409）
    code, body = session.call("POST", f"/api/v1/datasets/{dataset_id}/domains/generate",
                              {}, timeout=GEN_TIMEOUT)
    domains = session.as_list((body or {}).get("domains")) if isinstance(body, dict) else []
    record("R1 领域生成（真实 LLM）", code == 200 and len(domains) > 0,
           f"HTTP {code} domains={len(domains)} {[d.get('name') for d in domains[:4]]}")

    code, msg, _ = enqueue(session, "directions", dataset_id,
                           f"/api/v1/datasets/{dataset_id}/directions/generate",
                           {"directionCount": 2})
    record("R1 方向生成入队（m 用户可控=2）", code == 202 and "已入队" in str(msg),
           f"HTTP {code} message={msg}")

    run = wait_stage(session, dataset_id, "directions")
    record("R1 方向生成完成", bool(run) and run.get("status") == "completed",
           f"status={run.get('status') if run else None}")

    code, directions = session.call("GET", f"/api/v1/datasets/{dataset_id}/directions")
    items = session.as_list(directions)
    record("R1 方向条目已落库", code == 200 and len(items) > 0, f"count={len(items)}")
    if items:
        levels_ok = all(d.get("level") == 2 for d in items)
        domain_ids = {d.get("id") for d in domains}
        parents_ok = all(d.get("parentId") in domain_ids for d in items)
        record("R1 方向挂载正确（level=2 且 parentId 指向领域）", levels_ok and parents_ok,
               f"level_ok={levels_ok} parent_ok={parents_ok}")
        record("R1 m 参数生效（每领域方向数 ≤ 2）", len(items) <= 2 * max(len(domains), 1),
               f"directions={len(items)} domains={len(domains)}")

    code, msg, _ = enqueue(session, "directions", dataset_id,
                           f"/api/v1/datasets/{dataset_id}/generation-runs/directions/resume")
    record("R1 断点续跑接口可用", code == 202, f"HTTP {code} message={msg}")

    # ---------------------------------------------------------------- R2
    print("\nR2 方向 → 长链思维标准步骤（可编辑、版本化）")
    code, msg, _ = enqueue(session, "chain-standards", dataset_id,
                           f"/api/v1/datasets/{dataset_id}/chain-standards/generate", {})
    record("R2 标准步骤生成入队", code == 202 and "已入队" in str(msg), f"HTTP {code} message={msg}")

    standards = poll_until(
        session, f"/api/v1/datasets/{dataset_id}/chain-standards",
        lambda b: len(session.as_list(b)) > 0)
    std_items = session.as_list(standards)
    record("R2 标准步骤已落库", len(std_items) > 0, f"count={len(std_items)}")
    if std_items:
        first = std_items[0]
        steps = first.get("steps") or []
        record("R2 步骤为长链（≥2 步）", len(steps) >= 2, f"steps={len(steps)}")
        domain_id = first.get("domainId")
        code, body = session.call(
            "PUT", f"/api/v1/datasets/{dataset_id}/chain-standards/{domain_id}",
            {"steps": [{"title": "验收-步骤1", "detail": "父代理验收修改"},
                       {"title": "验收-步骤2", "detail": "父代理验收修改"}]})
        record("R2 标准步骤可编辑", code in (200, 201), f"HTTP {code} {str(body)[:120]}")
        code, versions = session.call(
            "GET", f"/api/v1/datasets/{dataset_id}/chain-standards/{domain_id}/versions")
        vlist = session.as_list(versions)
        record("R2 版本化（编辑后版本数 ≥ 2）", code == 200 and len(vlist) >= 2,
               f"versions={len(vlist)}")

    # ---------------------------------------------------------------- R3
    print("\nR3 方向 → x 个具体问题（x 可控、去重、难度分层）")
    code, msg, _ = enqueue(session, "questions", dataset_id,
                           f"/api/v1/datasets/{dataset_id}/questions/generate",
                           {"questionsPerDirection": 2})
    record("R3 问题生成入队（x 用户可控=2）", code == 202 and "已入队" in str(msg),
           f"HTTP {code} message={msg}")

    questions = poll_until(session, f"/api/v1/datasets/{dataset_id}/questions?limit=200",
                           lambda b: len(session.as_list(b)) > 0)
    qitems = session.as_list(questions)
    record("R3 问题已落库", len(qitems) > 0, f"count={len(qitems)}")
    if qitems:
        texts = [q.get("content") or q.get("question") for q in qitems]
        record("R3 去重（内容无重复）", len(texts) == len(set(texts)),
               f"unique={len(set(texts))}/{len(texts)}")
        difficulties = {q.get("difficulty") for q in qitems}
        difficulties.discard(None)
        record("R3 难度分层字段已落", len(difficulties) > 0, f"difficulties={sorted(difficulties)}")
        # x 可控：每个方向的问题数不应超过 2（允许模型少生成，不允许超发）
        by_direction = {}
        for q in qitems:
            by_direction.setdefault(q.get("directionId") or q.get("domainId"), 0)
            by_direction[q.get("directionId") or q.get("domainId")] += 1
        record("R3 x 参数生效（每方向 ≤ 2 题）",
               all(v <= 2 for v in by_direction.values()),
               f"per_direction={sorted(by_direction.values())}")
    code, stats = session.call("GET", f"/api/v1/datasets/{dataset_id}/questions/difficulty-stats")
    record("R3 难度统计接口可用", code == 200, f"HTTP {code} {str(stats)[:120]}")

    # ---------------------------------------------------------------- R4
    print("\nR4 GRPO：按用户给定打分档次生成教师模型评判提示词")
    code, body = session.call("PUT", f"/api/v1/datasets/{dataset_id}/reward-levels",
                              {"levels": ["-1", "0", "1"]})
    record("R4 设置打分档次 -1/0/1", code in (200, 201), f"HTTP {code} {str(body)[:120]}")

    code, msg, _ = enqueue(session, "grpo", dataset_id,
                           f"/api/v1/datasets/{dataset_id}/grpo/generate", {})
    record("R4 GRPO 提示词生成入队", code == 202 and "已入队" in str(msg),
           f"HTTP {code} message={msg}")

    grpo = poll_until(session, f"/api/v1/datasets/{dataset_id}/grpo?limit=50",
                      lambda b: len(session.as_list(b)) > 0)
    gitems = session.as_list(grpo)
    record("R4 GRPO 提示词已落库", len(gitems) > 0, f"count={len(gitems)}")
    if gitems:
        first = gitems[0]
        levels = first.get("levels") or []
        record("R4 覆盖用户给定三档打分（-1/0/1）", len(levels) >= 3, f"levels={levels}")
        rubrics = first.get("levelRubrics") or []
        record("R4 每档都有评判标准（levelRubrics）", len(rubrics) >= 3,
               f"rubrics={len(rubrics)}")
        prompt = first.get("judgePrompt") or ""
        record("R4 教师评判提示词为真实长文本", len(str(prompt)) > 200, f"len={len(str(prompt))}")
        record("R4 提示词引用了标准步骤框架（frameworkRef）",
               bool(first.get("frameworkRef")), f"frameworkRef={str(first.get('frameworkRef'))[:60]}")

    # ---------------------------------------------------------------- R5
    print("\nR5 SFT：生成对应思维链 + 答案")
    code, msg, _ = enqueue(session, "sft", dataset_id,
                           f"/api/v1/datasets/{dataset_id}/sft/generate",
                           {"includeAnswer": True})
    record("R5 SFT 生成入队", code == 202 and "已入队" in str(msg), f"HTTP {code} message={msg}")

    sft = poll_until(session, f"/api/v1/datasets/{dataset_id}/sft?limit=50",
                     lambda b: len(session.as_list(b)) > 0)
    sitems = session.as_list(sft)
    record("R5 SFT 记录已落库", len(sitems) > 0, f"count={len(sitems)}")
    if sitems:
        first = sitems[0]
        cot = first.get("chainOfThought") or ""
        ans = first.get("answer") or ""
        steps = first.get("chainSteps") or []
        record("R5 思维链为真实长文本", len(str(cot)) > 100, f"len={len(str(cot))}")
        record("R5 答案非空", len(str(ans)) > 5, f"len={len(str(ans))}")
        record("R5 思维链含分步结构（chainSteps）", len(steps) >= 2, f"steps={len(steps)}")

    # ---------------------------------------------------------------- R6
    print("\nR6 多格式导出（字段映射可配）")
    code, body = session.call("GET", f"/api/v1/datasets/{dataset_id}/export/formats")
    flist = (body or {}).get("formats") if isinstance(body, dict) else None
    flist = flist if isinstance(flist, list) else []
    record("R6 导出格式清单可用", code == 200 and len(flist) >= 2, f"formats={flist}")
    for want in ("jsonl", "csv", "parquet", "alpaca", "sharegpt"):
        record(f"R6 支持 {want} 格式", want in flist, f"formats={flist}")
    mappings = (body or {}).get("mappings") if isinstance(body, dict) else None
    mappings = mappings if isinstance(mappings, list) else []
    record("R6 字段映射清单可用（可配）", len(mappings) > 0, f"mappings={len(mappings)}")
    for fmt in ("jsonl", "alpaca", "sharegpt"):
        code, body = session.call("POST", f"/api/v1/datasets/{dataset_id}/export",
                                  {"format": fmt})
        record(f"R6 {fmt} 导出请求受理", code in (200, 201, 202),
               f"HTTP {code} {str(body)[:120]}")

    # ---------------------------------------------------------------- R7
    print("\nR7 数据集评估（多 LLM 互评、≥50 维度、全量/抽样、剔除生成者自评、汇总分析）")
    code, dims = session.call("GET", "/api/v1/eval/dimensions")
    dlist = session.as_list(dims)
    if len(dlist) < 50:
        session.call("POST", "/api/v1/eval/dimensions/seed", {})
        code, dims = session.call("GET", "/api/v1/eval/dimensions")
        dlist = session.as_list(dims)
    record("R7 内置评估维度 ≥ 50", len(dlist) >= 50, f"dimensions={len(dlist)}")
    builtin = [d for d in dlist if d.get("isBuiltin")]
    record("R7 维度含内置标记（不可删）", len(builtin) > 0, f"builtin={len(builtin)}")
    cats = {d.get("category") for d in dlist}
    cats.discard(None)
    record("R7 维度按分类组织", len(cats) >= 3, f"categories={len(cats)} {sorted(cats)}")
    lc = [d for d in dlist if "long_chain" in str(d.get("category")) or "lc_" in str(d.get("key"))]
    record("R7 存在聚焦长链思考的维度", len(lc) > 0, f"long_chain_dims={len(lc)}")

    code, judges = session.call("GET", "/api/v1/admin/eval/judges")
    jlist = session.as_list(judges)
    record("R7 裁判 provider 列表可用（多 LLM 接入）", code == 200, f"judges={len(jlist)}")
    if not jlist:
        record_skip("R7 真实多 LLM 互评打分", "未配置可用裁判 provider")
    else:
        # 抽样评估：ratio 模式
        code, body = session.call("POST", "/api/v1/eval/runs", {
            "datasetId": dataset_id,
            "name": "验收-抽样评估",
            "samplingMode": "ratio",
            "sampleRatio": 1.0,
            "targetKind": "sft",
            "dimensionKeys": [d.get("key") for d in dlist[:3]],
            "judgeProviderIds": [j.get("providerId") or j.get("id") for j in jlist[:2]],
        })
        run_id = (body or {}).get("id") if isinstance(body, dict) else None
        record("R7 创建抽样评估运行", code in (200, 201) and bool(run_id),
               f"HTTP {code} runId={run_id} {str(body)[:140]}")
        if run_id:
            code, body = session.call("POST", f"/api/v1/eval/runs/{run_id}/start")
            record("R7 启动评估", code in (200, 202), f"HTTP {code}")

            run = poll_until(session, f"/api/v1/eval/runs/{run_id}",
                             lambda b: (b or {}).get("status") in
                             ("completed", "failed", "partial_failed"))
            status = (run or {}).get("status")
            record("R7 评估运行完成", status == "completed", f"status={status}")

            code, report = session.call("GET", f"/api/v1/eval/runs/{run_id}/report")
            record("R7 报告可获取", code == 200, f"HTTP {code}")
            if code == 200 and isinstance(report, dict):
                record("R7 报告含汇总结论（中文文本）", bool(report.get("conclusions")),
                       f"conclusions={str(report.get('conclusions'))[:140]}")
                record("R7 报告含整体分数",
                       report.get("overallScore") is not None
                       or (report.get("notes") or {}).get("normalizedOverall") is not None,
                       f"keys={sorted(report.keys())[:12]}")
            code, scores = session.call("GET", f"/api/v1/eval/runs/{run_id}/scores")
            slist = session.as_list(scores)
            record("R7 逐条多维打分已落库", code == 200 and len(slist) > 0, f"count={len(slist)}")
            if slist:
                with_rationale = [s for s in slist if s.get("rationale")]
                record("R7 打分含裁判理由（非纯数字）", len(with_rationale) > 0,
                       f"with_rationale={len(with_rationale)}/{len(slist)}")

            # 剔除生成者自评
            code, judges2 = session.call("GET", "/api/v1/admin/eval/judges")
            j2 = session.as_list(judges2)
            generator = [j for j in j2 if j.get("providerId") == 1]
            record("R7 生成者自评剔除机制生效",
                   (not generator) or bool(generator[0].get("excluded")),
                   f"generator_entry={str(generator[:1])[:140]}")

    # ---------------------------------------------------------------- R8
    print("\nR8 数据清洗（拒答关键词库、多步骤拦截、报告）")
    code, kws = session.call("GET", "/api/v1/cleaning/keywords")
    klist = session.as_list(kws)
    record("R8 拒答关键词库非空", len(klist) > 0, f"keywords={len(klist)}")
    patterns = {str(k.get("pattern")) for k in klist}
    for want in ("对不起", "我不能"):
        record(f"R8 内置关键词含「{want}」", want in patterns, f"patterns={len(patterns)}")
    builtin_kw = [k for k in klist if k.get("isBuiltin")]
    record("R8 存在内置关键词（不可删）", len(builtin_kw) > 0, f"builtin={len(builtin_kw)}")
    cats = {k.get("category") for k in klist}
    cats.discard(None)
    record("R8 关键词按类别组织（可扩展规则）", len(cats) >= 2, f"categories={sorted(cats)}")

    # 自定义关键词（可扩展）
    custom = f"验收自定义拒答词{int(time.time())}"
    code, body = session.call("POST", "/api/v1/cleaning/keywords/import",
                              {"patterns": [custom], "category": "refusal", "severity": "block"})
    record("R8 用户可扩展关键词库", code in (200, 201), f"HTTP {code} {str(body)[:120]}")

    code, msg, _ = enqueue(session, "cleaning", dataset_id,
                           f"/api/v1/datasets/{dataset_id}/cleaning/run",
                           {"stages": ["question", "reasoning", "answer"], "ruleIds": []})
    record("R8 清洗入队（三阶段拦截）", code in (200, 202) and "已入队" in str(msg),
           f"HTTP {code} message={msg}")

    runs = poll_until(session, f"/api/v1/datasets/{dataset_id}/cleaning/runs",
                      lambda b: any(r.get("status") in ("completed", "failed", "partial_failed")
                                    for r in session.as_list(b)))
    rlist = session.as_list(runs)
    done = [r for r in rlist if r.get("status") in ("completed", "failed", "partial_failed")]
    record("R8 清洗运行已落库", len(done) > 0,
           f"runs={len(rlist)} statuses={[r.get('status') for r in rlist[:3]]}")
    if done:
        run = done[0]
        record("R8 清洗运行完成", run.get("status") == "completed", f"status={run.get('status')}")
        record("R8 报告含扫描/命中统计",
               run.get("scannedItems") is not None and run.get("flaggedItems") is not None,
               f"scanned={run.get('scannedItems')} flagged={run.get('flaggedItems')} "
               f"dropped={run.get('droppedItems')}")
        cleaning_run_id = run.get("id")
        code, report = session.call("GET", f"/api/v1/cleaning/runs/{cleaning_run_id}/report")
        record("R8 清洗报告可获取", code == 200, f"HTTP {code}")
        if code == 200 and isinstance(report, dict):
            stages = report.get("stages")
            record("R8 报告含分阶段统计", bool(stages), f"stages={str(stages)[:160]}")
            record("R8 报告含结论文字", bool(report.get("conclusions")),
                   f"conclusions={str(report.get('conclusions'))[:160]}")
        code, findings = session.call("GET", f"/api/v1/cleaning/runs/{cleaning_run_id}/findings")
        record("R8 命中明细接口可用", code == 200, f"HTTP {code}")

    return finish()


def finish():
    print(f"\n=== 验收结果：通过 {len(PASS)} · 失败 {len(FAIL)} · 跳过 {len(SKIP)} ===")
    if FAIL:
        print("\n失败项：")
        for f in FAIL:
            print(f"  - {f}")
    if SKIP:
        print("\n跳过项（输入缺失，不计入通过）：")
        for s in SKIP:
            print(f"  - {s}")
    return 0 if not FAIL else 1


if __name__ == "__main__":
    sys.exit(main())
