#!/usr/bin/env python3
"""L7 裁判模型接入 —— 接口测试。

契约：docs/plans/eval-and-cleaning-plan.md 第 3.7 节。
  GET /api/v1/admin/eval/judges         -> EvalJudgeOption[]
  PUT /api/v1/eval/runs/{runId}/judges  body {"providerIds":[2,3]} -> EvalRunJudge[]

测试项：
  T1 登录拿 session cookie
  T2 GET  /api/v1/admin/eval/judges 返回 200 + 数组，字段含 providerId/model/isActive
  T3 PUT  /api/v1/eval/runs/{id}/judges 空 providerIds 返回 400
  T4 PUT  未知 provider id 返回 400（不得静默忽略）
  T5 PUT  选中生成者模型：返回 200，但该条 excluded=true 且原因非空（**核心需求**）
  T6 PUT  响应落库可回读：再 GET 一次裁判列表（经 DB）确认 excluded 已持久化
  T7 PUT  重复调用幂等：同一 run 连调两次，记录数不翻倍（UNIQUE 约束生效）
  T8 PUT  改选后旧裁判被移除（换选另一个 provider，原记录不再出现）
  T9 全量剔除场景：本地仅 1 个 provider 且即生成者时，可用裁判数=0（**正确行为**）
  T10 未认证访问 /api/v1/admin/eval/judges 返回 401
  T11 非 admin 前缀路由 /api/v1/eval/runs/{id}/judges 在未登录时返回 401

运行目标：**必须打自己的 lane 容器**，不是 main 分支的镜像。
  http://127.0.0.1:3210 上跑的是 main 镜像，没有本 lane 新加的路由，
  GET /api/v1/admin/eval/judges 与 PUT /api/v1/eval/runs/{id}/judges 都会 404。
  正确做法：用 worktree 构建镜像，起独立容器，端口按 lane 错开。
  本 lane（L7）用 18087。

fixture 说明：
  L9（创建评估运行的 API）尚未合并，本测试自行插入 eval_runs / model_providers
  夹具行，结束时删除。夹具用独立命名前缀 l7- 以便识别，不影响既有数据。

用法：
  python3 test/test_l7_eval_judges.py --base http://127.0.0.1:18087 --dataset 1
"""

import argparse
import json
import subprocess
import sys
import urllib.error
import urllib.request

DEFAULT_BASE = "http://127.0.0.1:18087"
PG_CONTAINER = "llm-postgres-1"
PG_USER = "llm_factory"
PG_DB = "llm_factory"
ADMIN_EMAIL = "admin@company.com"
ADMIN_PASSWORD = "admin123456"

FIXTURE_PREFIX = "l7-fixture"

PASSED = []
FAILED = []
SKIPPED = []


def record(name, ok, detail=""):
    if ok:
        PASSED.append(name)
        print(f"  PASS  {name}")
    else:
        FAILED.append((name, detail))
        print(f"  FAIL  {name}\n        {detail}")


def skip(name, reason):
    SKIPPED.append((name, reason))
    print(f"  SKIP  {name}\n        输入缺失: {reason}")


def as_int(value, label):
    """把值强制为 int，防止拼接进 SQL 的字符串。"""
    try:
        return int(value)
    except (TypeError, ValueError):
        raise RuntimeError(f"{label} 必须是整数，得到 {value!r}") from None


def psql(sql):
    """执行 SQL 并返回 stdout（-tA：无表头、无对齐，便于解析）。

    注意：本脚本只把 as_int 强转过的整数与硬编码字符串拼进 SQL，
    不接受外部自由文本，因此不引入注入面。
    """
    result = subprocess.run(
        ["docker", "exec", PG_CONTAINER, "psql", "-U", PG_USER, "-d", PG_DB, "-tAc", sql],
        capture_output=True, check=False,
    )
    if result.returncode != 0:
        raise RuntimeError(result.stderr.decode("utf-8", "replace").strip())
    return result.stdout.decode("utf-8", "replace").strip()


class Session:
    """极简 cookie 会话（该 API 用 HttpOnly cookie 鉴权，不是 Bearer token）。"""

    def __init__(self, base):
        self.base = base.rstrip("/")
        self.cookie = None

    def request(self, method, path, body=None, timeout=30):
        url = self.base + path
        data = None
        headers = {"Accept": "application/json"}
        if body is not None:
            data = json.dumps(body).encode("utf-8")
            headers["Content-Type"] = "application/json"
        if self.cookie:
            headers["Cookie"] = self.cookie
        req = urllib.request.Request(url, data=data, headers=headers, method=method)
        try:
            with urllib.request.urlopen(req, timeout=timeout) as res:
                raw = res.read().decode("utf-8", "replace")
                set_cookie = res.headers.get("Set-Cookie")
                if set_cookie:
                    self.cookie = set_cookie.split(";")[0]
                return res.status, raw
        except urllib.error.HTTPError as err:
            return err.code, err.read().decode("utf-8", "replace")

    def json(self, method, path, body=None, timeout=30):
        status, raw = self.request(method, path, body, timeout)
        try:
            return status, json.loads(raw)
        except json.JSONDecodeError:
            return status, {"_raw": raw}


def create_fixtures(dataset_id, generator_provider_id):
    """建夹具：一个评估运行 + 两个临时 provider。

    返回 (run_id, [provider_id, ...])。
    临时 provider 用于验证「多 LLM 互评」的剔除判定——本地真实环境只有 1 个
    provider，无法覆盖「部分可用、部分被剔除」这条路径。
    """
    run_id = as_int(psql(
        f"INSERT INTO eval_runs (dataset_id, name, generator_provider_id) "
        f"VALUES ({dataset_id}, '{FIXTURE_PREFIX}-run', {generator_provider_id}) "
        f"RETURNING id;"
    ), "run id")

    provider_ids = []
    for suffix, model_name in (("alpha", "l7-fixture-model-alpha"),
                               ("beta", "l7-fixture-model-beta")):
        pid = as_int(psql(
            f"INSERT INTO model_providers "
            f"(name, base_url, model, provider_type, is_active, api_key_masked) "
            f"VALUES ('{FIXTURE_PREFIX}-{suffix}', 'http://fixture-{suffix}.invalid/v1', "
            f"'{model_name}', 'openai-compatible', TRUE, '***') RETURNING id;"
        ), "provider id")
        provider_ids.append(pid)
    return run_id, provider_ids


def drop_fixtures(run_id, provider_ids):
    """清理夹具，避免污染共享库。"""
    ids = ",".join(str(pid) for pid in provider_ids) or "0"
    psql(f"DELETE FROM eval_run_judges WHERE eval_run_id = {run_id};")
    psql(f"DELETE FROM eval_runs WHERE id = {run_id};")
    psql(f"DELETE FROM model_providers WHERE id IN ({ids});")


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--base", default=DEFAULT_BASE)
    parser.add_argument("--dataset", type=int, default=1,
                        help="用于建夹具评估运行的数据集 id")
    args = parser.parse_args()

    if not args.base.startswith(("http://", "https://")):
        print(f"--base 必须是 http(s) 地址，得到 {args.base!r}")
        return 2

    session = Session(args.base)

    print("\n[T1] 登录获取 session cookie")
    status, payload = session.json("POST", "/api/v1/auth/login",
                                   {"email": ADMIN_EMAIL, "password": ADMIN_PASSWORD})
    if status != 200 or not session.cookie:
        skip("T1 登录", f"HTTP {status} —— 本地栈未启动或账号不匹配: {payload}")
        print_summary()
        return 1
    record("T1 登录", True)

    print("\n[T2] GET /api/v1/admin/eval/judges 返回 200 + 数组")
    status, options = session.json("GET", "/api/v1/admin/eval/judges")
    options_ok = (
        status == 200
        and isinstance(options, list)
        and all(
            {"providerId", "providerName", "model", "isActive"} <= set(item.keys())
            for item in options
        )
    )
    record("T2 GET /admin/eval/judges 返回选项数组",
           options_ok,
           f"HTTP {status} body={str(options)[:300]}")
    if not options_ok:
        print_summary()
        return 1

    print("\n[T2b] 未认证访问 /api/v1/admin/eval/judges 返回 401")
    anonymous = Session(args.base)
    status, body = anonymous.json("GET", "/api/v1/admin/eval/judges")
    record("T2b 未认证返回 401", status == 401,
           f"期望 401，实际 HTTP {status} body={str(body)[:200]}")

    # 生成者 id 取数据集实际使用的 provider——需求要求剔除的正是它。
    try:
        generator_id = as_int(psql(
            f"SELECT COALESCE(NULLIF(provider_id, 0), 0) FROM datasets WHERE id = {args.dataset};"
        ) or "0", "generator provider id")
    except RuntimeError as err:
        skip("读取数据集 provider", str(err))
        print_summary()
        return 1
    print(f"  （dataset id={args.dataset} 的生成者 provider id={generator_id}）")

    if generator_id == 0:
        skip("夹具准备", f"数据集 {args.dataset} 未设置 provider_id，无法判定生成者")
        print_summary()
        return 1

    run_id, fixture_providers = create_fixtures(args.dataset, generator_id)
    print(f"  （夹具 eval_run id={run_id}，临时 provider ids={fixture_providers}）")

    try:
        print("\n[T3] PUT 空 providerIds 返回 400")
        status, body = session.json("PUT", f"/api/v1/eval/runs/{run_id}/judges",
                                    {"providerIds": []})
        record("T3 空 providerIds 被拒绝（400）", status == 400,
               f"期望 400，实际 HTTP {status} body={str(body)[:200]}")

        print("\n[T4] PUT 未知 provider id 返回 400")
        status, body = session.json("PUT", f"/api/v1/eval/runs/{run_id}/judges",
                                    {"providerIds": [999999999]})
        record("T4 未知 provider id 被拒绝（400）", status == 400,
               f"期望 400，实际 HTTP {status} body={str(body)[:200]}")

        print("\n[T5] PUT 选中生成者模型 -> excluded=true 且原因非空（核心需求）")
        status, judges = session.json("PUT", f"/api/v1/eval/runs/{run_id}/judges",
                                      {"providerIds": [generator_id]})
        generator_entry = next(
            (j for j in judges if j.get("providerId") == generator_id), None
        ) if isinstance(judges, list) else None
        record("T5 生成者被标记剔除且给出原因",
               status == 200
               and generator_entry is not None
               and generator_entry.get("excluded") is True
               and bool(generator_entry.get("excludeReason", "").strip()),
               f"HTTP {status} entry={generator_entry} body={str(judges)[:300]}")

        print("\n[T6] PUT 结果已持久化（直接读库回验）")
        try:
            persisted = psql(
                f"SELECT excluded || '|' || exclude_reason FROM eval_run_judges "
                f"WHERE eval_run_id = {run_id} AND provider_id = {generator_id};"
            )
        except RuntimeError as err:
            persisted = f"ERROR {err}"
        record("T6 剔除标记已落库",
               persisted.startswith("t|") and len(persisted) > 3,
               f"库中值={persisted!r}")

        print("\n[T7] 选中两个临时 provider -> 均可用，且重复调用幂等")
        status, judges = session.json("PUT", f"/api/v1/eval/runs/{run_id}/judges",
                                      {"providerIds": fixture_providers})
        usable = [j for j in judges if not j.get("excluded")] if isinstance(judges, list) else []
        record("T7a 两个同源无关 provider 均可用",
               status == 200 and len(usable) == len(fixture_providers),
               f"HTTP {status} usable={usable} body={str(judges)[:300]}")

        status2, judges2 = session.json("PUT", f"/api/v1/eval/runs/{run_id}/judges",
                                        {"providerIds": fixture_providers})
        count = psql(f"SELECT COUNT(*) FROM eval_run_judges WHERE eval_run_id = {run_id};")
        record("T7b 重复调用不产生重复记录",
               status2 == 200 and count == str(len(fixture_providers)),
               f"HTTP {status2} 库中记录数={count} 期望={len(fixture_providers)}")

        print("\n[T8] 改选后旧裁判被移除")
        status, judges = session.json("PUT", f"/api/v1/eval/runs/{run_id}/judges",
                                      {"providerIds": [fixture_providers[0]]})
        remaining = psql(
            f"SELECT COUNT(*) FROM eval_run_judges "
            f"WHERE eval_run_id = {run_id} AND provider_id = {fixture_providers[1]};"
        )
        record("T8 取消勾选的裁判被删除",
               status == 200 and remaining == "0",
               f"HTTP {status} 残留记录数={remaining}")

        print("\n[T9] 全部剔除场景（仅生成者可选）-> 可用裁判数为 0")
        status, judges = session.json("PUT", f"/api/v1/eval/runs/{run_id}/judges",
                                      {"providerIds": [generator_id]})
        usable = [j for j in judges if not j.get("excluded")] if isinstance(judges, list) else []
        record("T9 生成者被剔除后可用裁判数为 0（正确行为，非缺陷）",
               status == 200 and len(usable) == 0,
               f"HTTP {status} usable={usable}")

        print("\n[T10] 未认证访问 /api/v1/eval/runs/{id}/judges 返回 401")
        status, body = anonymous.json("PUT", f"/api/v1/eval/runs/{run_id}/judges",
                                      {"providerIds": fixture_providers})
        record("T10 未认证返回 401", status == 401,
               f"期望 401，实际 HTTP {status} body={str(body)[:200]}")
    finally:
        try:
            drop_fixtures(run_id, fixture_providers)
            print("  （夹具已清理）")
        except RuntimeError as err:
            print(f"  ⚠️ 夹具清理失败，请手工删除 run={run_id} providers={fixture_providers}: {err}")

    print_summary()
    return 1 if FAILED else 0


def print_summary():
    print("\n" + "=" * 60)
    print(f"通过 {len(PASSED)} · 失败 {len(FAILED)} · 输入缺失 {len(SKIPPED)}")
    if FAILED:
        print("\n失败项：")
        for name, detail in FAILED:
            print(f"  - {name}: {detail}")
    if SKIPPED:
        print("\n输入缺失（不得视为通过）：")
        for name, reason in SKIPPED:
            print(f"  - {name}: {reason}")
    print("=" * 60)


if __name__ == "__main__":
    sys.exit(main())
