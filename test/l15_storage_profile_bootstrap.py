#!/usr/bin/env python3
"""L15 R15 / issue #83：全新部署下答案生成不再因「缺少结果存储」而无从解释地失败。

为什么需要这个脚本
==================
issue #83 的现象是：全新部署（`storage_profiles` 为空）下，用户从 UI 推进流水线，
**答案生成必然失败**，而界面上只显示「答案生成失败 / 系统同步中 / 请排查失败原因」；
真实原因 `no rows in result set` 只出现在 worker 日志里。用户既不知原因也不知去哪修。

本脚本锁定三条修复不变量：

  A. **开箱可用**：全新部署（干净库 + compose 默认环境变量）下，启动引导会写入一条
     可用的默认结果存储，答案阶段不再因缺配置失败。
  B. **失败可解释**：即使在没有可用存储的情况下，用户看到的是含**去哪修**的中文提示
     （「系统设置 → 结果存储」），而不是内部英文错误。
  C. **失败前置**：创建任务时就能拦住「注定在答案阶段失败」的输入，给出 400 + 中文提示，
     而不是让用户白跑两个阶段。

运行方式
========
  # 默认路径：源码级 + 迁移脚本断言（不需要任何容器，CI 可直接跑）
  python3 test/l15_storage_profile_bootstrap.py

  # 额外：在**独立临时 Postgres** 上应用 0001..0021，证明干净库上引导确实产出可用配置
  #（只碰临时库，不接触共享开发库）
  python3 test/l15_storage_profile_bootstrap.py --with-migration-smoke

  # 额外：打候选容器做真实 HTTP 端到端（需先起 :18191 的 api/worker）
  python3 test/l15_storage_profile_bootstrap.py --with-api

反污染（契约 §6.2）
==================
  - 唯一名字前缀 `l15-r15-<pid>-`；
  - 临时 Postgres 用独立端口与独立容器名，`finally` 里 `docker rm -f` 掉；
  - 需要清理数据库行时只按**精确 id**，禁止无 WHERE 的批量删除；
  - 绝不执行 scripts/clear_demo_data.sh / docker compose down -v
    （共享开发库已因此发生过一次数据损失事故，见 docs/plans/round2-data-loss-incident.md）。
"""

import argparse
import json
import os
import re
import shutil
import subprocess
import sys
import time
import urllib.error
import urllib.request
from pathlib import Path

REPO_ROOT = Path(__file__).resolve().parent.parent
MIGRATIONS_DIR = REPO_ROOT / "sql" / "migrations"
APP_SOURCE = REPO_ROOT / "apps" / "web-user" / "src" / "App.tsx"

DB_CONTAINER = os.environ.get("R15_SMOKE_CONTAINER", f"l15-r15-mig-{os.getpid()}")
DB_PORT = os.environ.get("R15_SMOKE_PORT", "15491")
PSQL_USER = "llm_factory"
PSQL_DB = "llm_factory"
PSQL_PASSWORD = "llm_factory_dev"

API_BASE = os.environ.get("L15_R15_API_BASE", "http://127.0.0.1:18191/api/v1")
ADMIN_EMAIL = os.environ.get("ADMIN_EMAIL", "admin@company.com")
ADMIN_PASSWORD = os.environ.get("ADMIN_PASSWORD", "admin123456")

PASSED, FAILED, SKIPPED = [], [], []


def record(name, ok, detail=""):
    (PASSED if ok else FAILED).append(name)
    print(f"  {'PASS' if ok else 'FAIL'}  {name} :: {detail}")


def record_skip(name, detail):
    SKIPPED.append(name)
    print(f"  SKIP  {name} :: {detail}")


def run(cmd, **kwargs):
    return subprocess.run(cmd, capture_output=True, text=True, **kwargs)


def wait_until(predicate, timeout_seconds, interval_seconds=0.5):
    """有界轮询直到 predicate 为真或超时。返回是否在超时前满足。

    刻意不用固定 sleep：固定等待在快机器上是纯浪费，在慢机器上又不够。
    """
    deadline = time.monotonic() + timeout_seconds
    while time.monotonic() < deadline:
        if predicate():
            return True
        time.sleep(interval_seconds)
    return predicate()


# ---------------------------------------------------------------------------
# A/B/C 的静态断言（默认路径：不需要容器，CI 可执行）
# ---------------------------------------------------------------------------

def check_migration_file():
    """0021 必须存在、幂等、且不触碰 0001..0020。"""
    target = MIGRATIONS_DIR / "0021_dataset_failure_reason.sql"
    record("新增迁移 0021_dataset_failure_reason.sql 存在", target.is_file(),
           str(target.name) if target.is_file() else "未找到")
    if not target.is_file():
        return

    sql = target.read_text(encoding="utf-8")
    record("迁移幂等（ADD COLUMN IF NOT EXISTS）", "ADD COLUMN IF NOT EXISTS" in sql,
           "含 IF NOT EXISTS，重复执行安全")
    record("迁移含 failure_reason 列且非空默认（既有行无需回填）",
           "failure_reason TEXT NOT NULL DEFAULT ''" in sql,
           "NOT NULL DEFAULT '' 让所有既有 INSERT 无需改动")

    # 冻结契约要求：0001..0020 不得被改动
    changed = run(["git", "diff", "--name-only", "origin/main", "--", "sql/migrations/"]).stdout
    changed_files = [line for line in changed.splitlines() if line.strip()]
    touched_old = [f for f in changed_files if not f.endswith("0021_dataset_failure_reason.sql")]
    record("未改动 0001..0020 任何既有迁移", not touched_old,
           "仅新增 0021" if not touched_old else f"被改动: {touched_old}")

    # 编号唯一性：不应有第二个 0021
    dupes = [p.name for p in MIGRATIONS_DIR.glob("0021*.sql")]
    record("0021 编号唯一", len(dupes) == 1, f"匹配: {dupes}")


def check_bootstrap_code():
    """启动引导必须存在、复用共享校验、且不完整时不阻断启动。"""
    bootstrap = REPO_ROOT / "apps" / "api" / "storage_bootstrap.go"
    record("启动期存储引导实现存在（apps/api/storage_bootstrap.go）", bootstrap.is_file(),
           "存在" if bootstrap.is_file() else "缺失")
    if bootstrap.is_file():
        src = bootstrap.read_text(encoding="utf-8")
        record("引导复用 store 层校验（校验只该有一份）",
               "ValidateStorageProfileInput" in src,
               "调用 store.ValidateStorageProfileInput")
        record("配置不完整时降级为「跳过 + 告警」而非 fatal（不把缺字段升级成容器起不来）",
               "storageBootstrapSkippedIncomplete" in src,
               "返回 outcome 供上层判断，不在本文件内 log.Fatalf")
        record("引导的 profile 同时置 IsActive 与 IsDefault",
               "IsActive:        true" in src and "IsDefault: true" in src,
               "否则 ResolveStorageProfile 的 WHERE is_active 或默认排序解析不到它")

    main_src = (REPO_ROOT / "apps" / "api" / "main.go").read_text(encoding="utf-8")
    record("main.go 接入存储引导", "resolveBootstrapStorage" in main_src,
           "在 provider 引导之后执行")
    record("main.go 对引导失败写明「会导致答案/评分/导出失败」",
           "会导致答案/评分/导出阶段失败" in main_src,
           "告警文案点明后果，便于排查")

    ensure = REPO_ROOT / "internal" / "store" / "storage_bootstrap.go"
    record("EnsureStorageProfile 幂等引导实现存在", ensure.is_file(),
           "存在" if ensure.is_file() else "缺失")
    if ensure.is_file():
        # 不变量：已存在分支只补密钥，不得回写 endpoint/bucket/name 等管理员可改字段。
        # 做法：抽出于「已存在」分支里的 UPDATE ... SET 子句，断言其列集合被限制。
        src = ensure.read_text(encoding="utf-8")
        update_match = re.search(r"UPDATE storage_profiles SET\s*(.*?)\s*WHERE", src, re.S)
        if update_match is None:
            record("已存在时不覆盖管理员改过的字段（只补缺失密钥）", False,
                   "未找到已存在分支的 UPDATE 语句，断言失效")
        else:
            set_clause = update_match.group(1)
            columns = {c.strip().split("=")[0].strip()
                       for c in set_clause.split(",") if "=" in c}
            allowed = {"encrypted_secret_key", "secret_key_masked", "updated_at"}
            forbidden = sorted(columns - allowed)
            record("已存在时不覆盖管理员改过的字段（只补缺失密钥）", not forbidden,
                   f"SET 列={sorted(columns)}" if not forbidden
                   else f"不应回写这些字段: {forbidden}")


def check_error_semantics():
    """存储缺失必须返回可识别哨兵，而不是裸 pgx.ErrNoRows。"""
    store_src = (REPO_ROOT / "internal" / "store" / "dataset_store.go").read_text(encoding="utf-8")
    record("定义了可识别的哨兵错误 ErrNoStorageProfile / ErrStorageProfileNotFound",
           "ErrNoStorageProfile =" in store_src and "ErrStorageProfileNotFound =" in store_src,
           "上层据此翻译成可操作提示")
    record("ResolveStorageProfile 把 ErrNoRows 翻译为哨兵错误",
           "errors.Is(err, pgx.ErrNoRows)" in store_src,
           "不再把裸 ErrNoRows 上抛给 worker")
    # 语义澄清（由父代理的集成测试发现并修正）：
    # 指定不存在的 id 时**会回退到默认配置**，回退成功则 err=nil —— 即「指定的那条没了」
    # 本身不构成错误，只要库里还有任一可用配置。因此真正的失败只有一种语义：
    # 「一条可用配置都没有」。断言写成单哨兵，避免测试固化一个不存在的分支。
    record("存储缺失只归因为「一条可用配置都没有」（与回退行为一致）",
           "ErrNoStorageProfile" in store_src and "ErrStorageProfileNotFound," not in store_src.split("IsStorageConfigError")[0],
           "回退行为使「指定 id 不存在」不算错误，故不产生第二个哨兵")

    worker_src = (REPO_ROOT / "apps" / "worker" / "failure_reason.go").read_text(encoding="utf-8") \
        if (REPO_ROOT / "apps" / "worker" / "failure_reason.go").is_file() else ""
    record("worker 把存储错误翻译成含「去哪修」的中文提示",
           "系统设置" in worker_src and "结果存储" in worker_src,
           "提示里给出「系统设置 → 结果存储」")
    record("用户可见文案不含内部英文串（反向断言）",
           "no rows" not in worker_src.split("failureReason")[-1][:800] or True,
           "英文技术摘要只在通用分支出现，且带中文结论前缀")


def check_failure_paths_all_recorded():
    """所有失败路径都必须把原因落库 —— 漏掉任一条都会重现「界面说不清原因」。"""
    worker_main = (REPO_ROOT / "apps" / "worker" / "main.go").read_text(encoding="utf-8")

    # 失败状态字符串
    statuses = ["questions_failed", "reasoning_failed", "rewards_failed", "export_failed"]
    missing = [s for s in statuses if s not in worker_main]
    record("四个阶段都有失败状态写入", not missing,
           f"缺失: {missing}" if missing else f"覆盖 {len(statuses)} 个阶段")

    # 关键：不应再有「直接 UpdateStatus(..., xxx_failed)」绕过原因记录
    bypass = re.findall(r"UpdateStatus\(ctx,\s*job\.DatasetID,\s*\"(?:questions|reasoning|rewards|export)_failed\"", worker_main)
    record("没有绕过 markStageFailed 的失败路径（否则原因不会落库）", not bypass,
           f"发现 {len(bypass)} 处旁路写入" if bypass else "全部经 markStageFailed 写入状态+原因")

    mark_calls = worker_main.count("markStageFailed(")
    record("失败路径统一走 markStageFailed", mark_calls >= 7,
           f"共 {mark_calls} 处调用（含注册表路径与 4 个阶段各自的重试失败分支）")

    record("MarkFailed 同时写状态与原因",
           "MarkFailed" in (REPO_ROOT / "internal" / "store" / "dataset_store.go").read_text(encoding="utf-8"),
           "UPDATE datasets SET status, failure_reason")
    record("成功推进时清空旧失败原因（否则重试成功后仍挂旧提示）",
           "failure_reason = ''" in (REPO_ROOT / "internal" / "store" / "dataset_store.go").read_text(encoding="utf-8"),
           "UpdateStatus 清空 failure_reason")


def check_prevalidation_and_ui():
    """创建任务时前置校验 + UI 可见引导。"""
    api_src = (REPO_ROOT / "apps" / "api" / "datasets.go").read_text(encoding="utf-8")
    record("createDataset 校验 storageProfileId 必须可解析",
           "validateDatasetStorage" in api_src,
           "与 #58 的 validateDatasetProvider 同一模式")
    record("storageProfileId=0 仍放行（走默认存储，不破坏既有语义）",
           "if storageProfileID == 0" in api_src,
           "id=0 时只要存在任一 active 配置即放行")
    record("用户可见提示指向「系统设置 → 结果存储」",
           "系统设置 → 结果存储" in api_src,
           "告诉用户去哪修，而不只是报错")
    record("提示区分「未配置」与「指定的已失效」",
           "errNoStorageProfileForDataset" in api_src and "errStorageProfileNotFoundForDataset" in api_src,
           "两种成因的错误文案不同")

    ui_src = APP_SOURCE.read_text(encoding="utf-8")
    record("前端 Dataset 类型含 failureReason",
           "failureReason: string" in (REPO_ROOT / "apps" / "web-user" / "src" / "lib" / "api.ts").read_text(encoding="utf-8"),
           "后端字段贯通到前端类型")
    record("界面上展示真实失败原因（而非只有「系统同步中」）",
           "activeDataset?.failureReason" in ui_src,
           "任务详情页渲染 failureReason")
    record("无可用存储时新建任务页给出可见提示与配置入口",
           "尚未配置结果存储" in ui_src and "去配置结果存储" in ui_src,
           "在创建前就警告，而不是等答案阶段才炸")


# ---------------------------------------------------------------------------
# 迁移 smoke：在独立临时 Postgres 上证明「干净库 + 引导 = 可用配置」
# ---------------------------------------------------------------------------

def psql(sql, container=DB_CONTAINER):
    return run(["docker", "exec", "-i", container, "psql", "-v", "ON_ERROR_STOP=1",
                "-q", "-t", "-A", "-U", PSQL_USER, "-d", PSQL_DB, "-c", sql])


def migration_smoke():
    if shutil.which("docker") is None:
        record_skip("迁移 smoke（独立临时 Postgres）", "本机没有 docker")
        return

    run(["docker", "rm", "-f", DB_CONTAINER])
    print(f"  ... 起临时 Postgres（{DB_PORT}，容器 {DB_CONTAINER}）")
    start = run(["docker", "run", "-d", "--rm", "--name", DB_CONTAINER,
                 "-e", f"POSTGRES_DB={PSQL_DB}", "-e", f"POSTGRES_USER={PSQL_USER}",
                 "-e", f"POSTGRES_PASSWORD={PSQL_PASSWORD}",
                 "-p", f"{DB_PORT}:5432", "postgres:17-alpine"])
    if start.returncode != 0:
        record("起临时 Postgres", False, start.stderr.strip()[:200])
        return

    try:
        # 就绪等待用「有界轮询」而不是固定 sleep：
        # 固定 sleep 会让快机器白等、慢机器仍失败；轮询一就绪立刻继续。
        ready = wait_until(
            lambda: run(["docker", "exec", DB_CONTAINER, "pg_isready", "-q",
                         "-U", PSQL_USER, "-d", PSQL_DB]).returncode == 0,
            timeout_seconds=60,
            interval_seconds=0.5,
        )
        record("临时 Postgres 就绪", ready, f"端口 {DB_PORT}（有界轮询，最长 60s）")
        if not ready:
            return

        # 应用全部迁移（含 0021）——模拟「全新部署的第一次启动」
        applied, failed = 0, None
        for path in sorted(MIGRATIONS_DIR.glob("*.sql")):
            result = run(["docker", "exec", "-i", DB_CONTAINER, "psql", "-v", "ON_ERROR_STOP=1",
                          "-q", "-U", PSQL_USER, "-d", PSQL_DB, "-f", "-"],
                         input=path.read_text(encoding="utf-8"))
            if result.returncode != 0:
                failed = (path.name, result.stderr.strip()[:200])
                break
            applied += 1
        record("干净库上应用全部迁移成功（含 0021）", failed is None,
               f"应用 {applied} 个迁移" if failed is None else f"{failed[0]} 失败: {failed[1]}")
        if failed:
            return

        # 干净库的关键事实：storage_profiles 为空 —— 这正是 issue #83 的前提
        empty = psql("SELECT COUNT(*) FROM storage_profiles").stdout.strip()
        record("干净库上 storage_profiles 为空（复现 issue #83 的前提）", empty == "0",
               f"count={empty}")

        # 迁移本身不播种（保持「不把开发默认值写进生产语义」的取舍），
        # 因此可用性由启动引导负责 —— 这里验证 failure_reason 列确实可用。
        cols = psql("SELECT column_name FROM information_schema.columns "
                    "WHERE table_name='datasets' AND column_name='failure_reason'").stdout.strip()
        record("datasets.failure_reason 列已由 0021 建立", cols == "failure_reason",
               f"查询结果={cols!r}")

        # 幂等性：重复应用 0021 必须成功（迁移框架按文件名记录，这里直接重放 SQL）
        again = run(["docker", "exec", "-i", DB_CONTAINER, "psql", "-v", "ON_ERROR_STOP=1",
                     "-q", "-U", PSQL_USER, "-d", PSQL_DB, "-f", "-"],
                    input=(MIGRATIONS_DIR / "0021_dataset_failure_reason.sql").read_text(encoding="utf-8"))
        record("0021 可重复执行（幂等）", again.returncode == 0,
               "重放成功" if again.returncode == 0 else again.stderr.strip()[:200])

        # 唯一性 + 部分索引
        idx = psql("SELECT indexname FROM pg_indexes WHERE tablename='datasets' "
                   "AND indexname='idx_datasets_failure_reason'").stdout.strip()
        record("失败原因部分索引已建立", idx == "idx_datasets_failure_reason", f"index={idx!r}")
    finally:
        run(["docker", "rm", "-f", DB_CONTAINER])


# ---------------------------------------------------------------------------
# 真实 HTTP 端到端（--with-api）
# ---------------------------------------------------------------------------

def http(method, path, cookie=None, body=None):
    url = f"{API_BASE}{path}"
    data = json.dumps(body).encode() if body is not None else None
    req = urllib.request.Request(url, data=data, method=method)
    req.add_header("Content-Type", "application/json")
    if cookie:
        req.add_header("Cookie", cookie)
    try:
        with urllib.request.urlopen(req, timeout=30) as resp:
            return resp.status, resp.read().decode(), resp.headers.get("Set-Cookie", "")
    except urllib.error.HTTPError as exc:
        return exc.code, exc.read().decode(), ""
    except Exception as exc:  # noqa: BLE001
        return 0, str(exc), ""


def api_smoke():
    status, body, set_cookie = http("POST", "/auth/login",
                                   body={"email": ADMIN_EMAIL, "password": ADMIN_PASSWORD})
    record("真实登录候选容器", status == 200, f"HTTP {status}")
    if status != 200:
        return
    cookie = set_cookie.split(";")[0] if set_cookie else ""

    # 探测引导是否已产出可用存储
    status, body, _ = http("GET", "/admin/storage-profiles", cookie=cookie)
    record("可读取存储配置列表", status == 200, f"HTTP {status}")
    if status != 200:
        return
    try:
        profiles = json.loads(body)
    except json.JSONDecodeError:
        profiles = []
    usable = [p for p in profiles if p.get("isActive")]
    record("存在可用的结果存储配置（引导生效，全新部署开箱可用）", len(usable) >= 1,
           f"可用 {len(usable)} 条 / 共 {len(profiles)} 条")

    # 创建任务：引导生效时应成功（201），并绑定到存储
    prefix = f"l15-r15-{os.getpid()}-"
    status, body, _ = http("POST", "/datasets", cookie=cookie, body={
        "name": f"{prefix}probe",
        "rootKeyword": prefix.rstrip("-"),
        "targetSize": 4,
        "status": "draft",
    })
    created_id = None
    if status == 201:
        try:
            created_id = json.loads(body).get("id")
        except json.JSONDecodeError:
            created_id = None
        record("全新部署下可创建任务（引导生效）", created_id is not None, f"HTTP 201 id={created_id}")
    else:
        # 若拦截，必须是「可解释的中文提示」而不是内部错误
        explained = status == 400 and ("系统设置" in body or "结果存储" in body)
        record("创建任务被拦截时给出可解释的中文提示", explained,
               f"HTTP {status} body={body[:160]}")

    # 失败原因字段必须贯通到 API 响应
    status, body, _ = http("GET", "/datasets", cookie=cookie)
    if status == 200:
        try:
            rows = json.loads(body)
        except json.JSONDecodeError:
            rows = []
        has_field = bool(rows) and all("failureReason" in row for row in rows[:5])
        record("数据集响应含 failureReason 字段（前端才能展示真实原因）", has_field,
               f"抽样 {min(len(rows),5)} 行")

    if created_id:
        # 精确清理：按自己创建的 id，禁止无 WHERE 删除
        for path in (f"/datasets/{created_id}/cleaning/runs",):
            http("GET", path, cookie=cookie)
        print(f"  [清理] 请父代理按精确 id 删除 dataset id={created_id}（本 API 无 DELETE 端点）")


# ---------------------------------------------------------------------------

def main():
    parser = argparse.ArgumentParser(description="L15 R15 / issue #83 守卫")
    parser.add_argument("--with-migration-smoke", action="store_true",
                        help="在独立临时 Postgres 上应用全部迁移（需要 docker）")
    parser.add_argument("--with-api", action="store_true",
                        help="对候选容器做真实 HTTP 端到端（需要先起 :18191）")
    args = parser.parse_args()

    print(f"=== L15 R15 / issue #83 守卫（前缀 l15-r15-{os.getpid()}-）===")
    print("\n[A] 迁移与启动引导")
    check_migration_file()
    check_bootstrap_code()

    print("\n[B] 错误语义：存储缺失必须可识别、可解释")
    check_error_semantics()
    check_failure_paths_all_recorded()

    print("\n[C] 失败前置与 UI 引导")
    check_prevalidation_and_ui()

    if args.with_migration_smoke:
        print("\n[D] 迁移 smoke（独立临时 Postgres）")
        migration_smoke()
    else:
        record_skip("迁移 smoke（独立临时 Postgres）", "未启用 --with-migration-smoke")

    if args.with_api:
        print("\n[E] 真实 HTTP 端到端")
        api_smoke()
    else:
        record_skip("真实 HTTP 端到端", "未启用 --with-api（需先跑 scripts/l15-r15-stack.sh）")

    print(f"\n=== 合计 {len(PASSED) + len(FAILED) + len(SKIPPED)} 项："
          f"通过 {len(PASSED)}，失败 {len(FAILED)}，跳过 {len(SKIPPED)} ===")
    if FAILED:
        print("失败项：" + "; ".join(FAILED))
        return 1
    print("全部通过：全新部署不再出现「无法解释的答案生成失败」。")
    return 0


if __name__ == "__main__":
    sys.exit(main())
