#!/usr/bin/env python3
"""issue #58 端到端回归：POST /api/v1/datasets 必须拒绝不存在的 providerId。

打真实运行的 API（本 lane 自建容器，默认 http://127.0.0.1:18107），不 mock：

    python3 test/l15_dataset_provider.py
    python3 test/l15_dataset_provider.py --base http://127.0.0.1:18107

断言：
  T1  providerId=999999（不存在）           -> 400，error 为 "指定的 AI 服务不存在"
  T2  T1 之后数据库里没有留下该名字的数据集  -> 校验发生在 INSERT 之前
  T3  providerId=<真实存在的 id>            -> 201，且响应回显同一个 providerId
  T4  providerId=0（暂不绑定）              -> 201，既有语义不变
  T5  合法请求落库的数据集确实存在           -> 证明 T3/T4 走的是真实写入路径，不是被拦掉

反污染（契约 §6.2）：
  - 所有数据集名带唯一前缀 l15-r7-<pid>-；
  - 结束（含失败路径 finally）按精确名字逐个清理自己创建的行，禁止无 WHERE 删除；
  - 清理走 /api/v1/datasets/{id} 没有删除端点，因此直连 llm-postgres-1 用 WHERE name = $1；
  - 库里有该名字的残留行时，脚本在输出中写明 id 便于人工清理。
"""

import argparse
import json
import os
import subprocess
import sys
import urllib.error
import urllib.request

ADMIN_EMAIL = os.environ.get("ADMIN_EMAIL", "admin@company.com")
ADMIN_PASSWORD = os.environ.get("ADMIN_PASSWORD", "admin123456")
PG_CONTAINER = os.environ.get("L15_PG_CONTAINER", "llm-postgres-1")

PREFIX = f"l15-r7-{os.getpid()}-"
UNKNOWN_PROVIDER_ID = 999999999

PASS, FAIL = [], []
CREATED_NAMES = []
LEAKED_ROWS = []


def record(name, ok, detail=""):
    (PASS if ok else FAIL).append(name)
    print(f"  [{'PASS' if ok else 'FAIL'}] {name}" + (f" :: {detail}" if detail else ""))


def psql(sql):
    """在共享开发库上执行一条 SQL，返回 stdout（-tA：无表头、无对齐）。"""
    proc = subprocess.run(
        ["docker", "exec", PG_CONTAINER, "psql", "-U", "llm_factory", "-d", "llm_factory", "-tAc", sql],
        capture_output=True, text=True,
    )
    if proc.returncode != 0:
        raise RuntimeError(f"psql 失败: {proc.stderr.strip()}")
    return proc.stdout.strip()


class Session:
    """cookie 会话（该 API 用 HttpOnly cookie 鉴权）。"""

    def __init__(self, base):
        self.base = base.rstrip("/")
        self.cookie = ""

    def call(self, method, path, payload=None):
        if not self.base.startswith(("http://", "https://")):
            raise ValueError(f"--base 必须是 http(s) URL，收到 {self.base!r}")
        data = json.dumps(payload).encode() if payload is not None else None
        req = urllib.request.Request(
            self.base + path, data=data, method=method,
            headers={"Content-Type": "application/json",
                     **({"Cookie": self.cookie} if self.cookie else {})},
        )
        try:
            with urllib.request.urlopen(req, timeout=60) as resp:  # nosec B310 — scheme 已在上方校验
                raw = resp.read().decode()
                for header in resp.headers.get_all("Set-Cookie") or []:
                    self.cookie = header.split(";")[0]
                return resp.status, (json.loads(raw) if raw.strip() else {})
        except urllib.error.HTTPError as exc:
            raw = exc.read().decode()
            try:
                return exc.code, (json.loads(raw) if raw.strip() else {})
            except json.JSONDecodeError:
                return exc.code, {"error": raw}
        except Exception as exc:  # noqa: BLE001 — 网络错误按失败报告
            return 0, {"error": str(exc)}


def count_rows_named(name):
    return int(psql(f"SELECT COUNT(*) FROM datasets WHERE name = '{name.replace(chr(39), chr(39) * 2)}'"))


def cleanup():
    """按精确名字清理本脚本创建的数据集（含子表级联）。"""
    for name in CREATED_NAMES:
        escaped = name.replace("'", "''")
        try:
            psql(f"DELETE FROM datasets WHERE name = '{escaped}'")
        except RuntimeError as exc:
            print(f"  [WARN] 清理 {name} 失败：{exc}")
    for name in LEAKED_ROWS:
        escaped = name.replace("'", "''")
        try:
            psql(f"DELETE FROM datasets WHERE name = '{escaped}'")
        except RuntimeError as exc:
            print(f"  [WARN] 清理泄漏行 {name} 失败：{exc}")


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--base", default=os.environ.get("L15_R7_BASE", "http://127.0.0.1:18107"))
    args = parser.parse_args()

    session = Session(args.base)
    status, body = session.call("POST", "/api/v1/auth/login",
                               {"email": ADMIN_EMAIL, "password": ADMIN_PASSWORD})
    if status != 200:
        print(f"无法登录被测 api：status={status} body={str(body)[:200]}")
        return 2

    # 取一个真实存在的 provider id。不写死 1：干净库、或 provider 被重建后
    # 写死的 id 会让 T3 变成假失败。
    status, providers = session.call("GET", "/api/v1/admin/providers")
    if status != 200 or not isinstance(providers, list) or not providers:
        print(f"无法取得 provider 列表：status={status} body={str(providers)[:200]}")
        return 2
    real_provider_id = providers[0]["id"]
    print(f"  [INFO] 真实 provider id = {real_provider_id}，共 {len(providers)} 个")

    name_unknown = f"{PREFIX}unknown-provider"
    name_ok = f"{PREFIX}valid-provider"
    name_zero = f"{PREFIX}zero-provider"

    try:
        # ---- T1：不存在的 providerId 必须被拒绝 ----
        status, body = session.call("POST", "/api/v1/datasets", {
            "name": name_unknown, "rootKeyword": "军事", "targetSize": 3,
            "providerId": UNKNOWN_PROVIDER_ID,
        })
        error_text = body.get("error") if isinstance(body, dict) else str(body)
        record(
            "T1 不存在的 providerId 返回 400",
            status == 400,
            f"status={status} error={error_text!r}",
        )
        record(
            "T1 error 文案为「指定的 AI 服务不存在」",
            error_text == "指定的 AI 服务不存在",
            f"error={error_text!r}",
        )

        # ---- T2：拒绝时不得留下半成品数据集 ----
        leaked = 0
        if status == 201 and isinstance(body, dict) and body.get("id"):
            # 极端情况：服务端仍然放行了。先登记名字，让 finally 能清掉。
            LEAKED_ROWS.append(name_unknown)
            leaked = count_rows_named(name_unknown)
        else:
            leaked = count_rows_named(name_unknown)
        record(
            "T2 被拒绝的请求没有落库（校验在 INSERT 之前）",
            leaked == 0,
            f"datasets.name={name_unknown!r} 命中 {leaked} 行",
        )

        # ---- T3：真实存在的 providerId 仍然 201 ----
        CREATED_NAMES.append(name_ok)
        status, body = session.call("POST", "/api/v1/datasets", {
            "name": name_ok, "rootKeyword": "军事", "targetSize": 3,
            "providerId": real_provider_id,
        })
        echoed = body.get("providerId") if isinstance(body, dict) else None
        record(
            "T3 存在的 providerId 返回 201",
            status == 201,
            f"status={status} body={str(body)[:160]}",
        )
        record(
            "T3 响应回显同一个 providerId",
            echoed == real_provider_id,
            f"providerId={echoed} want={real_provider_id}",
        )

        # ---- T4：providerId=0 的既有语义不变 ----
        CREATED_NAMES.append(name_zero)
        status, body = session.call("POST", "/api/v1/datasets", {
            "name": name_zero, "rootKeyword": "测试", "targetSize": 3,
            "providerId": 0,
        })
        record(
            "T4 providerId=0（暂不绑定）仍可创建，返回 201",
            status == 201,
            f"status={status} body={str(body)[:160]}",
        )

        # ---- T5：合法请求确实写进了库 ----
        ok_rows = count_rows_named(name_ok)
        zero_rows = count_rows_named(name_zero)
        record(
            "T5 合法请求真实落库（证明 201 走的是写入路径）",
            ok_rows == 1 and zero_rows == 1,
            f"{name_ok}:{ok_rows} 行, {name_zero}:{zero_rows} 行",
        )
    finally:
        cleanup()

    # ---- 清理自检 ----
    leftover = sum(count_rows_named(n) for n in (name_unknown, name_ok, name_zero))
    record("清理自检：本脚本没有留下数据集", leftover == 0, f"残留 {leftover} 行")

    print(f"\n通过 {len(PASS)}，失败 {len(FAIL)}")
    if FAIL:
        for name in FAIL:
            print(f"  FAILED: {name}")
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main())
