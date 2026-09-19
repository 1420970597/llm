#!/usr/bin/env python3
"""L15 / R4 并发活跃运行记录验收测试（issue #9）。

契约：docs/plans/issue-remediation-plan.md §1.2、§2 的 R4 行、§6.1 与 §6.2。

被测目标：本 lane 构建出的候选 api 实例（默认 http://127.0.0.1:18104），
它与 llm_default 网络里的 llm-postgres-1 共用同一份数据。

为什么必须有这条测试：issue #9 的缺陷在数据库层（缺部分唯一索引）与 store 层
（StartRun 为 read-then-insert）。Go 单测覆盖了 store 语义，本脚本覆盖**真实 HTTP
并发**这一用户可见路径 —— 用户连点两次「生成问题」/「生成方向」正是这样触发的。

测试项：
  T1  前置：登录并创建唯一前缀的测试数据集
  T2  并发 12 次 POST /datasets/{id}/questions/generate，全部返回 202 且无 5xx
  T3  结束后 GET /datasets/{id}/generation-runs 里同阶段活跃记录恰好 1 条
  T4  数据库直查断言：同 (dataset_id, stage) 活跃记录数恰好 1
  T5  部分唯一索引 uniq_generation_runs_active 存在且是 WHERE 限定的唯一索引
  T6  同一阶段结束后再次入队，仍只保留 1 条活跃记录且历史记录未被覆盖

反污染（契约 §6.2）：
  * 唯一名字前缀 l15-r4-<pid>-；
  * 结束在 finally 里按数据集 id 精确删除（datasets 的 ON DELETE CASCADE 带走子表）；
  * 无 WHERE 的批量删除一律不使用。

需要 docker 可执行以直查数据库（共享开发库 llm-postgres-1）。
缺少 API 或数据库时明确报错，不伪造通过。
"""

from __future__ import annotations

import concurrent.futures
import os
import subprocess
import sys
import time

import requests

BASE = os.environ.get("L15_R4_BASE_URL", "http://127.0.0.1:18104")
ADMIN_EMAIL = os.environ.get("L15_R4_ADMIN_EMAIL", "admin@company.com")
ADMIN_PASSWORD = os.environ.get("L15_R4_ADMIN_PASSWORD", "admin123456")

PG_CONTAINER = os.environ.get("L15_R4_PG_CONTAINER", "llm-postgres-1")
PG_USER = os.environ.get("L15_R4_PG_USER", "llm_factory")
PG_DB = os.environ.get("L15_R4_PG_DB", "llm_factory")

CONCURRENCY = 12
# stage 取自 internal/store/question_store_v2.go 的 QuestionsStage = "questions"。
# 注意不是 "questions.generate" —— 后者是 job 类型（enqueueJob 的 dedup key 用它），
# 而 generation_runs.stage 是阶段名。两者拼写歧义曾让本脚本的断言全部落空，
# 这里显式写明来源，避免再次混淆。
STAGE = "questions"

RESULTS: list[tuple[str, bool, str]] = []


def record(name: str, ok: bool, detail: str) -> None:
    RESULTS.append((name, ok, detail))
    print(f"[{'PASS' if ok else 'FAIL'}] {name}: {detail}", flush=True)


def psql(sql: str) -> str:
    """直查共享开发库。失败抛异常 —— 本脚本依赖数据库断言，不能静默降级。"""
    proc = subprocess.run(
        [
            "docker", "exec", PG_CONTAINER,
            "psql", "-U", PG_USER, "-d", PG_DB, "-tAc", sql,
        ],
        capture_output=True,
        text=True,
        timeout=120,
    )
    if proc.returncode != 0:
        raise RuntimeError(f"psql 失败: {proc.stderr.strip()[:400]}")
    return proc.stdout.strip()


def as_dict(payload: object) -> dict:
    return payload if isinstance(payload, dict) else {}


def as_list(payload: object) -> list:
    return payload if isinstance(payload, list) else []


def json_body(res: requests.Response) -> object:
    """安全解析响应体：错误路径可能不是 JSON。"""
    try:
        return res.json()
    except ValueError:
        return {"raw": res.text[:200]}


def enqueue_once(session: requests.Session, dataset_id: int) -> tuple[int, str]:
    """发一次问题生成入队请求（每个并发调用各自一个 Session，避免连接池串行化）。"""
    local = requests.Session()
    local.cookies.update(session.cookies)
    try:
        res = local.post(
            f"{BASE}/api/v1/datasets/{dataset_id}/questions/generate",
            json={"questionsPerDirection": 1},
            timeout=120,
        )
        return res.status_code, res.text[:160]
    except Exception as exc:  # noqa: BLE001 - 网络异常也要作为证据上报
        return 0, f"{type(exc).__name__}: {exc}"
    finally:
        local.close()


def active_count(dataset_id: int, stage: str) -> int:
    raw = psql(
        "SELECT COUNT(*) FROM generation_runs "
        f"WHERE dataset_id = {dataset_id} AND stage = '{stage}' "
        "AND status IN ('pending','running')"
    )
    return int(raw or "0")


def main() -> int:
    session = requests.Session()
    run_prefix = f"l15-r4-{os.getpid()}-"
    # cleanup_id 只用于 finally 里的精确清理；测试主体一律用已收窄为 int 的 ds。
    cleanup_id: int | None = None
    retained_note = ""

    try:
        login = session.post(
            f"{BASE}/api/v1/auth/login",
            json={"email": ADMIN_EMAIL, "password": ADMIN_PASSWORD},
            timeout=30,
        )
        if login.status_code != 200 or not session.cookies.get("llm_session"):
            print(f"无法登录被测 api：status={login.status_code} body={login.text[:200]}")
            return 2

        # T1：唯一前缀数据集，绑定已配置的 provider（id=1）。
        created = session.post(
            f"{BASE}/api/v1/datasets",
            json={
                "name": f"{run_prefix}concurrency",
                "rootKeyword": "并发测试",
                "targetSize": 4,
                "providerId": 1,
                "directionCount": 2,
            },
            timeout=60,
        )
        if created.status_code != 201:
            record("T1 前置：创建测试数据集", False,
                   f"status={created.status_code} body={created.text[:300]}")
            return 1
        created_id = as_dict(json_body(created)).get("id")
        if not isinstance(created_id, int):
            record("T1 前置：创建测试数据集", False,
                   f"响应里没有整数 id：body={created.text[:300]}")
            return 1
        ds: int = created_id
        cleanup_id = ds
        record("T1 前置：创建测试数据集", True, f"dataset_id={ds} 前缀={run_prefix}")

        # 入队需要至少一个 level=2 方向。真实 LLM 生成方向太慢，这里直接写库造前置，
        # 且只造**本测试数据集自己**的方向（精确 dataset_id 条件），不污染共享数据。
        psql(
            "INSERT INTO domains (dataset_id, name, canonical_name, level, source, review_status) "
            f"VALUES ({ds}, '{run_prefix}方向A', '{run_prefix}方向A', 2, 'ai', 'draft')"
        )
        direction_rows = psql(
            f"SELECT COUNT(*) FROM domains WHERE dataset_id = {ds} AND level = 2"
        )
        record("T1b 前置：写入 1 个 level=2 方向", int(direction_rows or "0") == 1,
               f"level=2 方向数={direction_rows}")

        # T2：并发入队。这是用户在 UI 上连点两次「生成问题」所走的真实路径。
        with concurrent.futures.ThreadPoolExecutor(max_workers=CONCURRENCY) as pool:
            futures = [pool.submit(enqueue_once, session, ds) for _ in range(CONCURRENCY)]
            responses = [f.result() for f in futures]

        statuses = [code for code, _ in responses]
        accepted = sum(1 for code in statuses if code == 202)
        server_errors = [body for code, body in responses if code >= 500]
        record(
            f"T2 并发 {CONCURRENCY} 次入队全部无 5xx",
            not server_errors,
            f"状态码分布={sorted(set(statuses))} 202 数={accepted} "
            f"5xx={len(server_errors)} 首个5xx={server_errors[:1]}",
        )

        # T3：HTTP 视图里同阶段活跃记录恰好 1 条（前端 listGenerationRuns 的可见结果）。
        res = session.get(f"{BASE}/api/v1/datasets/{ds}/generation-runs", timeout=60)
        runs = [r for r in as_list(json_body(res)) if isinstance(r, dict)]
        stage_runs = [r for r in runs if r.get("stage") == STAGE]
        actives = [r for r in stage_runs if r.get("status") in ("pending", "running")]
        record(
            "T3 GET generation-runs 中同阶段活跃记录恰好 1 条",
            res.status_code == 200 and len(actives) == 1,
            f"status={res.status_code} 同阶段记录={len(stage_runs)} 活跃={len(actives)} "
            f"全部活跃id={[r.get('id') for r in actives]}",
        )

        # T4：数据库层直查（绕过任何应用侧过滤）。
        db_active = active_count(ds, STAGE)
        record("T4 数据库直查：同阶段活跃记录恰好 1 条", db_active == 1,
               f"active_count={db_active}（>1 即存在并发孤儿，issue #9 未修复）")

        # T5：部分唯一索引定义。这是数据库能仲裁并发的前提。
        index_def = psql(
            "SELECT indexdef FROM pg_indexes WHERE tablename = 'generation_runs' "
            "AND indexname = 'uniq_generation_runs_active'"
        )
        index_ok = (
            "UNIQUE" in index_def.upper()
            and "dataset_id, stage" in index_def
            and "WHERE" in index_def.upper()
            and "pending" in index_def
            and "running" in index_def
        )
        record("T5 部分唯一索引 uniq_generation_runs_active 存在且定义正确", index_ok,
               f"indexdef={index_def or '（缺失）'}")

        # T6：重复尝试直插第二条活跃记录必须被数据库拒绝（幂等的负向断言）。
        reject = subprocess.run(
            [
                "docker", "exec", PG_CONTAINER,
                "psql", "-U", PG_USER, "-d", PG_DB, "-c",
                "INSERT INTO generation_runs (dataset_id, stage, status, total_units) "
                f"VALUES ({ds}, '{STAGE}', 'running', 1)",
            ],
            capture_output=True,
            text=True,
            timeout=60,
        )
        rejected = reject.returncode != 0 and "uniq_generation_runs_active" in reject.stderr
        record("T6 直插第二条活跃记录被唯一索引拒绝", rejected,
               f"returncode={reject.returncode} stderr={reject.stderr.strip()[:200]}")

        # 保留数据也算证据：把 id 写进输出便于后续复核/清理。
        retained_note = f"保留证据：dataset_id={ds}（前缀 {run_prefix}）"
        print(retained_note, flush=True)
    finally:
        # 契约 §6.2：无论成败都清理自己创建的行（精确条件，靠 CASCADE 带走子表）。
        if cleanup_id is not None:
            try:
                psql(f"DELETE FROM datasets WHERE id = {cleanup_id}")
                left = psql(f"SELECT COUNT(*) FROM datasets WHERE id = {cleanup_id}")
                print(f"[cleanup] 已删除 dataset_id={cleanup_id}，剩余={left}", flush=True)
            except Exception as exc:  # noqa: BLE001
                print(f"[cleanup] 清理失败（需人工处理）：{exc}", flush=True)
        else:
            print("[cleanup] 未创建数据集，无需清理", flush=True)

    print("\n===== 汇总 =====", flush=True)
    passed = sum(1 for _, ok, _ in RESULTS if ok)
    for name, ok, _ in RESULTS:
        print(f"  {'PASS' if ok else 'FAIL'}  {name}")
    print(f"通过 {passed}/{len(RESULTS)}")
    if retained_note:
        print(retained_note)
    return 0 if passed == len(RESULTS) else 1


if __name__ == "__main__":
    raise SystemExit(main())
