#!/usr/bin/env python3
"""L14 前端清洗 UI 的数据链路接口测试。

契约：docs/plans/eval-and-cleaning-plan.md 第 3.11 / 3.12 节 + 第 4.2 节。
前端页面 CleaningView 的每个区块都直接消费下列接口，本脚本按 UI 的调用顺序
把它们逐个打通，证明 UI 展示的是真实数据而不是占位。

被测目标：本 lane worktree 构建出的 api + worker（默认 http://127.0.0.1:18093）。
端口取自 tasks/PREAMBLE.md 的分配表（L14=18093），与 llm_default 网络里的
postgres / redis 共用数据，但使用独立队列 WORKER_QUEUE_NAME=lane-l14-queue，
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
并注入 3 条典型拒答文本，保证三个阶段都必然命中。内容真实（非伪造），
且不污染既有数据集。

## 测试项
  T1  关键词库列表：按 category 分组所需字段齐全（severity/matchMode/isActive/isBuiltin）
  T2  批量导入：首次导入 inserted 正确，重复导入 skipped 正确（UI 需展示这两个数）
  T3  关键词启用/停用：isActive 切换真实落库
  T4  规则列表：priority / minHits / stageScope / action 齐全（UI 需展示这些字段）
  T5  新建规则：POST 返回带 id 的规则
  T6  入队清洗：202 + message 含「已入队」，stages 三阶段
  T7  运行列表：本次 run 落库并最终 completed
  T8  报告：stages 三阶段统计齐全，conclusions 非空（UI 必须完整渲染结论文字）
  T9  findings：非空且覆盖三阶段，字段含 keywordId / matchedText / snippet（UI 明细列）
  T10 findings 按 stage 过滤只返回该阶段
  T11 严重度分布（UI 客户端 join 的数据源）：findings.keywordId 全部能在关键词库里 join 到 severity
  T12 未完成的 run：报告返回「清洗尚未完成」结论而非全零假报告
  T13 报告 run 是陈旧快照（status 恒为 queued），运行列表才是权威值
      —— 前端据此覆盖，否则会显示成「排队中 · 0 条」且轮询永不停止
  T14 编辑态只提交可写字段（前端 buildKeywordSavePayload 契约）时修改真实落库
  T15 后端 UPDATE 分支不写 category（冻结文件缺口，前端因此把该输入框置为只读）
"""

import os
import subprocess
import sys
import time

import requests

BASE = os.environ.get("L14_BASE_URL", "http://127.0.0.1:18093")
ADMIN_EMAIL = os.environ.get("L14_ADMIN_EMAIL", "admin@company.com")
ADMIN_PASSWORD = os.environ.get("L14_ADMIN_PASSWORD", "admin123456")

# 本测试专用关键词，避免与 L11 的测试数据互相干扰。
TEST_CATEGORY = "refusal"
TEST_PATTERNS = ["对不起", "我不能", "我无法提供", "作为人工智能", "I cannot"]

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
    """预置本测试关键词（真实落库，UI 会从 GET /cleaning/keywords 读到）。"""
    values = ",".join(
        f"('{pattern}','{TEST_CATEGORY}','block',TRUE)" for pattern in TEST_PATTERNS
    )
    ok, detail = psql(
        "INSERT INTO cleaning_keywords (pattern, category, severity, is_active) VALUES "
        f"{values} ON CONFLICT (pattern, category) DO UPDATE SET is_active = TRUE;"
    )
    if not ok:
        print(f"  （预置关键词失败：{detail}）")
    return ok


def setup_dataset(stamp: int) -> tuple[int | None, str]:
    """建测试数据集，复制 dataset 1 的真实问题/思维链/答案，并注入拒答样本。"""
    name = f"l14-cleaning-ui-{stamp}"
    ok, detail = psql(f"""
        INSERT INTO datasets (name, root_keyword, target_size, status)
        VALUES ('{name}', '军事', 10, 'draft');
    """)
    if not ok:
        return None, f"建数据集失败：{detail}"

    ok, out = psql(f"SELECT id FROM datasets WHERE name = '{name}';")
    if not ok or not out.strip():
        return None, f"读回数据集 id 失败：{out}"
    dataset_id = int(out.strip().splitlines()[0])

    ok, detail = psql(f"""
        INSERT INTO questions (dataset_id, domain_id, content, canonical_hash, status, difficulty)
        SELECT {dataset_id}, domain_id, content, canonical_hash || '-l14-{stamp}', 'generated', difficulty
        FROM questions WHERE dataset_id = 1 ORDER BY id LIMIT 10;
    """)
    if not ok:
        return None, f"复制问题失败：{detail}"

    # 复制的题目拿不到原 question_id，因此用 canonical_hash 后缀回连原题再复制其推理记录。
    ok, detail = psql(f"""
        INSERT INTO reasoning_records (dataset_id, question_id, answer_summary, reasoning, object_key, status)
        SELECT {dataset_id}, q2.id, r.answer_summary, r.reasoning, 'l14/{stamp}/' || q2.id, 'generated'
        FROM questions q1
        JOIN reasoning_records r ON r.question_id = q1.id AND r.dataset_id = 1
        JOIN questions q2
          ON q2.dataset_id = {dataset_id}
         AND q2.canonical_hash = q1.canonical_hash || '-l14-{stamp}'
        ON CONFLICT (question_id) DO NOTHING;
    """)
    if not ok:
        return None, f"复制推理记录失败：{detail}"

    # 注入拒答样本：question / reasoning / answer 三个阶段各至少 1 条。
    ok, detail = psql(f"""
        UPDATE questions SET content = content || ' 对不起，我不能回答这个。'
        WHERE id = (SELECT id FROM questions WHERE dataset_id = {dataset_id} ORDER BY id LIMIT 1);
        UPDATE reasoning_records SET reasoning = reasoning || ' 作为人工智能，我无法提供此类内容。'
        WHERE question_id = (SELECT id FROM questions WHERE dataset_id = {dataset_id} ORDER BY id LIMIT 1);
        UPDATE reasoning_records SET answer_summary = '对不起，我不能帮你做这个。'
        WHERE question_id = (SELECT id FROM questions WHERE dataset_id = {dataset_id} ORDER BY id OFFSET 1 LIMIT 1);
    """)
    if not ok:
        return None, f"注入拒答样本失败：{detail}"

    ok, out = psql(f"SELECT count(*) FROM questions WHERE dataset_id = {dataset_id};")
    return dataset_id, f"dataset_id={dataset_id} questions={out}"


def poll_run(session: requests.Session, dataset_id: int, run_id: int, timeout: float = 180.0) -> dict | None:
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


def cleanup(dataset_id: int | None) -> None:
    """清掉本测试写入的数据。

    必要性：本测试会新建规则并置为启用（T5），而 worker 按 is_active=TRUE 加载
    *全部* 规则（apps/worker/job_cleaning.go 的 loadCleaningRules）。留着启用规则会
    改变同一共享 postgres 上其他 lane 的清洗结果，因此测试结束必须回收。
    """
    if dataset_id is not None:
        psql(f"DELETE FROM datasets WHERE id = {dataset_id};")
    psql("DELETE FROM cleaning_rules WHERE name LIKE 'l14-ui-rule-%';")
    psql("DELETE FROM cleaning_keywords WHERE pattern LIKE 'l14-导入探针%';")


def summarize() -> int:
    failed = [name for name, ok, _ in RESULTS if not ok]
    print("")
    if failed:
        print(f"RESULT: {len(RESULTS) - len(failed)}/{len(RESULTS)} 通过，未通过：{', '.join(failed)}")
        return 1
    print(f"RESULT: {len(RESULTS)}/{len(RESULTS)} 全部通过")
    return 0


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
        record("前置：预置关键词库", False, "关键词落库失败")
        return 1
    record("前置：预置关键词库", True, f"{len(TEST_PATTERNS)} 条")

    # T1 关键词库列表（UI 关键词面板的数据源）。
    res = session.get(f"{BASE}/api/v1/cleaning/keywords", timeout=30)
    keywords = [as_dict(item) for item in as_list(json_body(res))]
    required = {"id", "pattern", "category", "matchMode", "severity", "isBuiltin", "isActive"}
    missing = [key for key in required if any(key not in item for item in keywords)]
    categories = {str(item.get("category")) for item in keywords}
    record("T1 关键词库字段齐全且分类可分组",
           res.status_code == 200 and len(keywords) >= len(TEST_PATTERNS) and not missing
           and {"refusal", "english_refusal", "safety"} <= categories,
           f"status={res.status_code} 共 {len(keywords)} 条，分类={sorted(categories)}，缺字段={missing}")

    # T2 批量导入：首次 inserted 正确，重复导入 skipped 正确。
    batch = [f"l14-导入探针-{stamp}", f"l14-导入探针B-{stamp}"]
    res = session.post(
        f"{BASE}/api/v1/cleaning/keywords/import",
        json={"patterns": batch, "category": TEST_CATEGORY, "severity": "block"},
        timeout=30,
    )
    body = as_dict(json_body(res))
    first_ok = res.status_code == 200 and body.get("inserted") == len(batch) and body.get("skipped") == 0
    res2 = session.post(
        f"{BASE}/api/v1/cleaning/keywords/import",
        json={"patterns": batch, "category": TEST_CATEGORY, "severity": "block"},
        timeout=30,
    )
    body2 = as_dict(json_body(res2))
    second_ok = res2.status_code == 200 and body2.get("inserted") == 0 and body2.get("skipped") == len(batch)
    record("T2 批量导入返回真实 inserted/skipped（UI 需展示这两个数）",
           first_ok and second_ok,
           f"首次 inserted={body.get('inserted')} skipped={body.get('skipped')}；"
           f"重复 inserted={body2.get('inserted')} skipped={body2.get('skipped')}")

    # T3 启用/停用（UI 开关直接调 saveCleaningKeyword）。
    # 请求体与前端 buildKeywordSavePayload 的编辑态一致：只带后端 UPDATE 分支真正
    # 会写入的字段（match_mode / severity / is_active / note）+ 定位用的 id/pattern。
    def editable_payload(record: dict, **changes: object) -> dict:
        payload = {key: record.get(key) for key in ("id", "pattern", "matchMode", "severity", "isActive", "note")}
        payload.update(changes)
        return payload

    target = next((item for item in keywords if str(item.get("pattern")) in TEST_PATTERNS), None)
    if target is None:
        record("T3 关键词启用/停用落库", False, "找不到可切换的测试关键词")
    else:
        res = session.put(
            f"{BASE}/api/v1/cleaning/keywords",
            json=editable_payload(target, isActive=False),
            timeout=30,
        )
        off_ok = res.status_code == 200 and as_dict(json_body(res)).get("isActive") is False
        res = session.put(
            f"{BASE}/api/v1/cleaning/keywords",
            json=editable_payload(target, isActive=True),
            timeout=30,
        )
        on_ok = res.status_code == 200 and as_dict(json_body(res)).get("isActive") is True
        record("T3 关键词启用/停用落库", off_ok and on_ok,
               f"停用→{off_ok}，启用→{on_ok}，pattern={target.get('pattern')}")

    # T14 编辑态只提交可写字段时，修改必须真实落库（前端 buildKeywordSavePayload 的契约）。
    # 背景：后端 UPDATE 分支只写 match_mode / severity / is_active / note，且以
    # id + pattern 定位。前端因此不再提交 category / 不再允许改 pattern。
    if target is None:
        record("T14 编辑态可写字段真实落库", False, "找不到可切换的测试关键词")
    else:
        flipped = "warn" if str(target.get("severity")) == "block" else "block"
        res = session.put(
            f"{BASE}/api/v1/cleaning/keywords",
            json=editable_payload(target, severity=flipped, note="l14-编辑探针"),
            timeout=30,
        )
        saved = as_dict(json_body(res))
        res = session.get(f"{BASE}/api/v1/cleaning/keywords", timeout=30)
        reread = next(
            (item for item in (as_dict(x) for x in as_list(json_body(res))) if item.get("id") == target.get("id")),
            {},
        )
        ok = (
            res.status_code == 200
            and saved.get("severity") == flipped
            and reread.get("severity") == flipped
            and reread.get("note") == "l14-编辑探针"
        )
        record("T14 编辑态提交的 severity/note 真实落库（非假成功）", ok,
               f"status={res.status_code} 响应 severity={saved.get('severity')} "
               f"重新读取 severity={reread.get('severity')} note={reread.get('note')}")
        session.put(
            f"{BASE}/api/v1/cleaning/keywords",
            json=editable_payload(target, severity=target.get("severity"), note=target.get("note")),
            timeout=30,
        )

        # T15 后端确实不写 category（冻结文件缺口，前端据此禁用该输入框）。
        # 这条断言记录的是*现状*：接口 200 但分类不变。若未来后端补上 category，
        # 本项会失败并提醒前端可以放开编辑。
        other_category = "safety" if str(target.get("category")) != "safety" else "refusal"
        res = session.put(
            f"{BASE}/api/v1/cleaning/keywords",
            json={**editable_payload(target), "category": other_category},
            timeout=30,
        )
        echoed = as_dict(json_body(res)).get("category")
        record("T15 后端 UPDATE 不写 category（缺口，前端因此禁用编辑）",
               res.status_code == 200 and echoed == target.get("category"),
               f"status={res.status_code} 请求 category={other_category} 响应 category={echoed} "
               f"（与当前值 {target.get('category')} 相同即证明被静默丢弃）")

    # T4 规则列表（UI 规则面板的数据源）。
    res = session.get(f"{BASE}/api/v1/cleaning/rules", timeout=30)
    rules = [as_dict(item) for item in as_list(json_body(res))]
    rule_fields = {"id", "name", "priority", "minHits", "stageScope", "action", "isActive"}
    rule_missing = [key for key in rule_fields if any(key not in item for item in rules)]
    record("T4 规则列表字段齐全（UI 需展示 priority/minHits/阶段/动作）",
           res.status_code == 200 and not rule_missing,
           f"status={res.status_code} 共 {len(rules)} 条，缺字段={rule_missing}")

    # T5 新建规则。
    rule_name = f"l14-ui-rule-{stamp}"
    res = session.post(
        f"{BASE}/api/v1/cleaning/rules",
        json={
            "name": rule_name,
            "stageScope": ["question", "reasoning", "answer"],
            "minHits": 1,
            "action": "flag",
            "priority": 5,
            "isActive": True,
        },
        timeout=30,
    )
    created_rule = as_dict(json_body(res))
    record("T5 新建规则返回带 id 的规则",
           res.status_code in (200, 201) and int(created_rule.get("id") or 0) > 0,
           f"status={res.status_code} id={created_rule.get('id')} name={created_rule.get('name')}")

    # 准备数据集。
    dataset_id, detail = setup_dataset(stamp)
    if dataset_id is None:
        record("前置：准备测试数据集", False, detail)
        cleanup(None)
        return summarize()
    record("前置：准备测试数据集", True, detail)

    # T6 入队清洗。
    clear_dedup(dataset_id)
    res = session.post(
        f"{BASE}/api/v1/datasets/{dataset_id}/cleaning/run",
        json={"stages": ["question", "reasoning", "answer"], "ruleIds": []},
        timeout=60,
    )
    body = as_dict(json_body(res))
    record("T6 清洗入队返回 202 + 已入队（未被去重抑制）",
           res.status_code == 202 and "已入队" in str(body.get("message", "")),
           f"status={res.status_code} message={body.get('message')}")

    # T7 运行列表 + 轮询完成。
    res = session.get(f"{BASE}/api/v1/datasets/{dataset_id}/cleaning/runs", timeout=30)
    runs = [as_dict(item) for item in as_list(json_body(res))]
    if not runs:
        record("T7 清洗运行落库", False, f"无 run 记录，body={res.text[:200]}")
        cleanup(dataset_id)
        return summarize()
    run_id = int(runs[0]["id"])
    run = poll_run(session, dataset_id, run_id)
    record("T7 清洗运行落库并完成",
           bool(run) and run.get("status") == "completed",
           f"run_id={run_id} status={run.get('status') if run else None} "
           f"scanned={run.get('scannedItems') if run else None} flagged={run.get('flaggedItems') if run else None}")
    if not run or run.get("status") != "completed":
        print(f"  运行未完成：{run.get('errorSummary') if run else '未知'}")
        cleanup(dataset_id)
        return summarize()

    # T8 报告（UI 报告面板）。
    res = session.get(f"{BASE}/api/v1/cleaning/runs/{run_id}/report", timeout=30)
    report = as_dict(json_body(res))
    stages = [as_dict(item) for item in as_list(report.get("stages"))]
    stage_names = {str(item.get("stage")) for item in stages}
    conclusions = as_list(report.get("conclusions"))
    record("T8 报告三阶段统计 + 结论文字非空（UI 必须完整渲染）",
           res.status_code == 200 and stage_names == {"question", "reasoning", "answer"} and len(conclusions) > 0,
           f"status={res.status_code} stages={sorted(stage_names)} conclusions={len(conclusions)} 条，"
           f"首条={conclusions[0] if conclusions else '无'}")

    # T9 findings 明细（UI 明细表）。
    res = session.get(f"{BASE}/api/v1/cleaning/runs/{run_id}/findings", timeout=30)
    findings = [as_dict(item) for item in as_list(json_body(res))]
    stages_seen = {str(item.get("stage")) for item in findings}
    finding_fields = {"keywordId", "matchedText", "snippet", "stage", "questionId", "action"}
    finding_missing = [key for key in finding_fields if any(key not in item for item in findings)]
    record("T9 findings 非空、覆盖三阶段、字段齐全",
           len(findings) > 0 and stages_seen == {"question", "reasoning", "answer"} and not finding_missing,
           f"{len(findings)} 条，阶段={sorted(stages_seen)}，缺字段={finding_missing}")

    # T10 按 stage 过滤。
    res = session.get(f"{BASE}/api/v1/cleaning/runs/{run_id}/findings", params={"stage": "answer"}, timeout=30)
    answer_findings = [as_dict(item) for item in as_list(json_body(res))]
    record("T10 findings 支持按 stage 过滤（UI 阶段筛选器）",
           res.status_code == 200 and len(answer_findings) > 0
           and all(item.get("stage") == "answer" for item in answer_findings),
           f"status={res.status_code} answer 阶段 {len(answer_findings)} 条")

    # T11 严重度分布数据源：findings.keywordId 能在关键词库 join 到 severity。
    res = session.get(f"{BASE}/api/v1/cleaning/keywords", timeout=30)
    fresh_keywords = [as_dict(item) for item in as_list(json_body(res))]
    severity_by_id = {int(item["id"]): str(item.get("severity")) for item in fresh_keywords}
    joined = [severity_by_id.get(int(item["keywordId"])) for item in findings]
    unknown = [item["keywordId"] for item, severity in zip(findings, joined) if severity is None]
    distribution = {"block": joined.count("block"), "warn": joined.count("warn")}
    record("T11 严重度分布可客户端 join（findings.keywordId ↔ 关键词 severity）",
           len(findings) > 0 and not unknown and sum(distribution.values()) == len(findings),
           f"block={distribution['block']} warn={distribution['warn']} 未知={unknown}")

    # T12 未完成 run 的报告必须明确说明，不能是全零假报告。
    ok, out = psql(f"""
        INSERT INTO cleaning_runs (dataset_id, stages, status)
        VALUES ({dataset_id}, '["question"]'::jsonb, 'queued') RETURNING id;
    """)
    # psql -tA 会把 RETURNING 结果与 INSERT 0 1 一起输出，取第一个纯数字行。
    pending_id = 0
    if ok:
        for line in out.splitlines():
            line = line.strip()
            if line.isdigit():
                pending_id = int(line)
                break
    if pending_id:
        res = session.get(f"{BASE}/api/v1/cleaning/runs/{pending_id}/report", timeout=30)
        pending_report = as_dict(json_body(res))
        pending_conclusions = as_list(pending_report.get("conclusions"))
        record("T12 未完成 run 返回「清洗尚未完成」结论",
               res.status_code == 200 and any("清洗尚未完成" in str(c) for c in pending_conclusions),
               f"status={res.status_code} conclusions={pending_conclusions}")
    else:
        record("T12 未完成 run 返回「清洗尚未完成」结论", False, f"插入 queued run 失败：{out}")

    # T13 报告里的 run 是过期快照（worker 在 MarkDone 之前写入），前端必须用
    # 运行列表的同一 run 覆盖它，否则界面会显示成「排队中 · 0 条」。
    # 这里验证前端修复所依赖的前提：报告的 run 确实陈旧、而列表里是新的。
    res = session.get(f"{BASE}/api/v1/datasets/{dataset_id}/cleaning/runs", timeout=30)
    fresh_runs = [as_dict(item) for item in as_list(json_body(res))]
    listed = next((item for item in fresh_runs if int(item.get("id")) == run_id), None)
    snapshot = as_dict(report.get("run"))
    record("T13 报告 run 为陈旧快照、运行列表为权威值（前端需覆盖）",
           listed is not None
           and snapshot.get("status") != listed.get("status")
           and int(listed.get("scannedItems") or 0) > int(snapshot.get("scannedItems") or 0),
           f"报告快照 status={snapshot.get('status')} scanned={snapshot.get('scannedItems')}；"
           f"运行列表 status={listed.get('status') if listed else None} "
           f"scanned={listed.get('scannedItems') if listed else None}")

    cleanup(dataset_id)
    return summarize()


if __name__ == "__main__":
    sys.exit(main())
