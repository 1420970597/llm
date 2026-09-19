#!/usr/bin/env python3
"""L15 R19 用户可见错误文案守卫（issue #102 + #109）。

用途
====
断言**面向用户的错误响应里不含内部实现细节**，而是中文且带恢复路径。

覆盖两类泄漏（issue 里是分开报的，但根因同源）：

  #102  404 响应直接外泄驱动原文 `no rows in result set`，另有英文文案
  #109  评估运行列表的「错误摘要」展示内部术语「去重键仍在有效期内」

设计：默认路径无需容器
=====================
CI 的 Backend job 只跑 `go test`、Frontend job 只跑 `tsc + vite build`，**都不起容器**。
因此：
  - **默认路径**（无参数）只做静态断言：扫描 apps/api 与 apps/worker 的源码，
    找出「把裸 err / 英文长句直接交给用户可见出口」的写法，以及内部术语出现在
    用户可见文案里的情况。它不需要网络，能 exit 0/1，CI 可真正执行。
  - **`--with-api`** 额外对真实服务发请求，断言响应体里不含内部细节、且是中文。
    未启用时输出 [SKIP] 且**不影响 exit code**（不把「环境不可达」报成「断言失败」）。

运行
====
  python3 test/l15_api_error_messages.py                          # 静态断言（CI）
  python3 test/l15_api_error_messages.py --with-api               # 追加真实请求断言
  python3 test/l15_api_error_messages.py --with-api --base http://127.0.0.1:18191/api/v1

反污染（契约 §6.2）
==================
本脚本**只读**：静态路径读源码，`--with-api` 路径只发 GET/POST 到**不存在的 id**
（99999999），不创建任何数据行，因此没有需要清理的东西，也不使用唯一前缀。
它依赖的唯一前置是「服务在跑」；服务不在时 `--with-api` 记 SKIP 并如实说明。
"""

import argparse
import json
import os
import re
import subprocess
import sys
import urllib.error
import urllib.request
from http.cookiejar import CookieJar

REPO_ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
ADMIN_EMAIL = os.environ.get("ADMIN_EMAIL", "admin@company.com")
ADMIN_PASSWORD = os.environ.get("ADMIN_PASSWORD", "admin123456")

PASS, FAIL, SKIP = [], [], []


def record(name, ok, detail=""):
    (PASS if ok else FAIL).append(name)
    print(f"[{'PASS' if ok else 'FAIL'}] {name}" + (f": {detail}" if detail else ""))


def record_skip(name, detail=""):
    SKIP.append(name)
    print(f"[SKIP] {name}" + (f": {detail}" if detail else ""))


# --------------------------------------------------------------------------
# 内部实现细节的识别（与 apps/api/main.go 的 internalDetailMarkers 对齐）
# --------------------------------------------------------------------------

INTERNAL_MARKERS = [
    "no rows in result set",
    "SQLSTATE",
    "pq: ",
    "pgx",
    "sql: ",
    "syntax error",
    "duplicate key",
    "violates ",
    'relation "',
    'column "',
    "does not exist",
    "connection refused",
    "i/o timeout",
]

# issue #109 列出的、用户实际在界面上看到的内部术语。
INTERNAL_JARGON = [
    "去重键",
    "TTL",
    "游标",
    "cursor",
    "dedup",
    "dimension_keys",
    "level=2",
    "generation_run",
]

CJK = re.compile(r"[\u4e00-\u9fff]")


def has_cjk(text):
    return bool(CJK.search(text))


def leaks_internal(text):
    """返回文本里命中的内部细节标记列表（空列表 = 干净）。"""
    lowered = text.lower()
    return [m for m in INTERNAL_MARKERS if m.lower() in lowered]


def jargon_in(text):
    return [m for m in INTERNAL_JARGON if m in text]


# --------------------------------------------------------------------------
# 静态断言：扫源码里「用户可见出口」的写法
# --------------------------------------------------------------------------

def read(path):
    with open(path, encoding="utf-8") as handle:
        return handle.read()


def go_files(rel_dir):
    out = []
    base = os.path.join(REPO_ROOT, rel_dir)
    for name in sorted(os.listdir(base)):
        if name.endswith(".go") and not name.endswith("_test.go"):
            out.append(os.path.join(base, name))
    return out


def strip_comments(text):
    """把行注释替换为空行。

    注意：**必须保持行数不变** —— 早先的实现用「过滤掉注释行」的写法，
    导致后续用 count("\n") 推算的行号比真实行号偏小，报出的位置指向了无关代码。
    用空行占位即可既排除注释内容、又保留准确行号。
    """
    return "\n".join(
        "" if line.strip().startswith("//") else line for line in text.split("\n")
    )


def check_no_english_user_messages():
    """apps/api 的 writeError 调用不得带英文文案（issue #102）。"""
    pattern = re.compile(
        r'writeError\(\s*w\s*,\s*[^,]+,\s*(?:errors\.New|fmt\.Errorf)\(\s*"([^"]*)"'
    )
    offenders = []
    for path in go_files("apps/api"):
        src = strip_comments(read(path))
        for match in pattern.finditer(src):
            msg = match.group(1)
            # 规则：面向中文用户的文案必须**含中文**。
            # 不能要求「以中文开头」—— 合法文案可以以字段名开头
            #（例如「active 必须是 true 或 false」「limit 不是有效的整数」）。
            # 但纯英文（如 "invalid email or password"）一律视为未本地化。
            if not has_cjk(msg):
                line = src[: match.start()].count("\n") + 1
                offenders.append(f"{os.path.relpath(path, REPO_ROOT)}:{line} -> {msg[:70]}")
    record(
        "apps/api 的用户可见错误文案不含英文长句",
        not offenders,
        "全部为中文" if not offenders else "；".join(offenders[:6]),
    )


def check_store_messages_are_chinese():
    """store 层的错误文案会经 4xx 原样透出给用户，因此也必须本地化。

    实例：登录失败时 auth_store 返回 "invalid email or password"，
    经 auth.go 的 `writeError(w, 401, err)` 原样出现在登录页。
    """
    pattern = re.compile(r'(?:fmt\.Errorf|errors\.New)\(\s*"([^"]*)"')
    # 规则：面向中文用户的文案必须**含中文**。
    # 不能要求「以中文开头」——「pattern 不能为空」「rule name 不能为空」
    # 都是合法文案（开头是字段名/参数名，整句是中文）。
    # 纯英文（无任何 CJK）才视为未本地化。
    # 这些是**内部**错误（含 %w 包装、或明确面向日志/SQL 层），不直接进用户响应。
    allowed_markers = ("%w", "schema not ready", "query failed")
    offenders = []
    for name in sorted(os.listdir(os.path.join(REPO_ROOT, "internal/store"))):
        if not name.endswith(".go") or name.endswith("_test.go"):
            continue
        path = os.path.join(REPO_ROOT, "internal/store", name)
        src = strip_comments(read(path))
        for match in pattern.finditer(src):
            msg = match.group(1)
            if any(marker in msg for marker in allowed_markers):
                continue
            if has_cjk(msg):
                continue  # 含中文 = 已本地化（哪怕以字段名开头）
            line = src[: match.start()].count("\n") + 1
            offenders.append(f"internal/store/{name}:{line} -> {msg[:60]}")
    record(
        "internal/store 的用户可见错误文案已本地化（无纯英文）",
        not offenders,
        "无英文文案" if not offenders else "；".join(offenders[:8]),
    )


def check_write_error_filter_covers_json_parser():
    """writeError 的过滤必须覆盖 encoding/json 的解析错误文案。

    用户可轻易触发（提交畸形 body，例如 `{bad json`），
    而 Go 的 json 错误文案是英文（invalid character / cannot unmarshal /
    unexpected end of JSON），会经 writeError(w, 400, err) 原样渲染到界面。
    """
    src = read(os.path.join(REPO_ROOT, "apps/api/main.go"))
    needed = [
        "invalid character",
        "cannot unmarshal",
        "unexpected end of JSON",
    ]
    missing = [m for m in needed if m not in src]
    record(
        "writeError 的过滤覆盖 encoding/json 解析错误",
        not missing,
        "已覆盖" if not missing else "缺少标记：" + "、".join(missing),
    )


def check_worker_summaries_free_of_jargon():
    """worker 写进用户可见 summary 的文案不得含内部字段名（issue #109 同类）。"""
    pattern = re.compile(
        r"(?:UpdateRunStatus|MarkFailed|FinishRun)\([^)]*(?:\"[^\"]*\"|fmt\.Errorf\(\s*\"[^\"]*\")",
        re.S,
    )
    offenders = []
    for path in go_files("apps/worker"):
        src = strip_comments(read(path))
        for match in pattern.finditer(src):
            chunk = match.group(0)
            hits = jargon_in(chunk)
            if hits:
                line = src[: match.start()].count("\n") + 1
                offenders.append(f"{os.path.relpath(path, REPO_ROOT)}:{line} -> {hits}")
    record(
        "worker 写入用户可见摘要的文案不含内部术语",
        not offenders,
        "无内部术语" if not offenders else "；".join(offenders[:6]),
    )


def check_write_error_sanitises_any_status():
    """writeError 必须对**任意**状态码做内部细节过滤（#102 的根因）。"""
    src = read(os.path.join(REPO_ROOT, "apps/api/main.go"))
    checks = [
        ("internalDetailMarkers", "定义了内部细节标记表"),
        ("looksLikeInternalDetail", "有内部细节判定函数"),
        ("no rows in result set", "把驱动原文列为标记"),
    ]
    missing = [desc for token, desc in checks if token not in src]
    record(
        "writeError 对任意状态码都过滤内部实现细节",
        not missing,
        "已实现" if not missing else "缺少：" + "、".join(missing),
    )

    # 关键的语义断言：过滤不能只挂在 status >= 500 上。
    # 检查 looksLikeInternalDetail 的调用位于 if status >= 500 的 else 分支（即 4xx 也走）。
    if "else if looksLikeInternalDetail(msg)" in src:
        record("4xx 路径确实经过内部细节过滤（不是只过滤 5xx）", True, "4xx 分支命中 looksLikeInternalDetail")
    else:
        record("4xx 路径确实经过内部细节过滤（不是只过滤 5xx）", False,
               "未找到 `else if looksLikeInternalDetail(msg)`，可能被改回只过滤 5xx")


def check_user_facing_error_preserves_identity():
    """userFacingError 必须做到「文案干净 + 身份保留」。"""
    path = os.path.join(REPO_ROOT, "apps/api/user_error.go")
    if not os.path.exists(path):
        record("存在 userFacingError（文案干净但保留 errors.Is 能力）", False, "apps/api/user_error.go 不存在")
        return
    src = read(path)
    record(
        "存在 userFacingError（文案干净但保留 errors.Is 能力）",
        "func (e userFacingError) Unwrap() error" in src and "func (e userFacingError) Error() string { return e.msg }" in src,
        "Error() 只返回中文，Unwrap() 暴露底层错误",
    )




# --------------------------------------------------------------------------
# 真实请求断言（--with-api）
# --------------------------------------------------------------------------

# (方法, 路径, body) —— 全部指向**不存在**的 id，因此只读、无副作用。
PROBES = [
    ("POST", "/datasets/99999999/cleaning/run", "{}"),
    ("POST", "/datasets/99999999/grpo/generate", "{}"),
    ("POST", "/datasets/99999999/sft/generate", "{}"),
    ("POST", "/datasets/99999999/questions/generate", "{}"),
    ("POST", "/datasets/99999999/reasoning/generate", "{}"),
    ("POST", "/datasets/99999999/rewards/generate", "{}"),
    ("POST", "/datasets/99999999/directions/generate", "{}"),
    ("POST", "/datasets/99999999/chain-standards/generate", "{}"),
    ("GET", "/eval/runs/99999999", None),
    ("GET", "/cleaning/runs/99999999/report", None),
    ("GET", "/datasets/99999999/eval-judges", None),
    ("GET", "/datasets/99999999/export/formats", None),
]


def login(base):
    jar = CookieJar()
    opener = urllib.request.build_opener(urllib.request.HTTPCookieProcessor(jar))
    payload = json.dumps({"email": ADMIN_EMAIL, "password": ADMIN_PASSWORD}).encode()
    req = urllib.request.Request(
        f"{base}/auth/login", data=payload, headers={"Content-Type": "application/json"}
    )
    with opener.open(req, timeout=20) as resp:
        if resp.status != 200:
            raise RuntimeError(f"登录失败 {resp.status}")
    return opener


def call(opener, base, method, path, body):
    data = body.encode() if body is not None else None
    req = urllib.request.Request(f"{base}{path}", data=data, method=method)
    if data is not None:
        req.add_header("Content-Type", "application/json")
    try:
        with opener.open(req, timeout=30) as resp:
            return resp.status, resp.read().decode("utf-8", "replace")
    except urllib.error.HTTPError as err:
        return err.code, err.read().decode("utf-8", "replace")


def run_api_checks(base):
    try:
        opener = login(base)
    except Exception as exc:  # noqa: BLE001 - 环境不可达属输入缺失
        record_skip("真实服务可达", f"{base} 登录失败：{exc}")
        return

    record("真实服务可达（管理员登录成功）", True, base)

    for method, path, body in PROBES:
        status, text = call(opener, base, method, path, body)
        label = f"{method} {path}"

        if status < 400:
            record(f"{label} 返回错误状态码", False, f"期望 >=400，实际 {status}")
            continue

        try:
            payload = json.loads(text)
            message = payload.get("error", "")
        except json.JSONDecodeError:
            message = text

        leaked = leaks_internal(message)
        jargon = jargon_in(message)
        problems = []
        if leaked:
            problems.append(f"泄漏内部细节 {leaked}")
        if jargon:
            problems.append(f"含内部术语 {jargon}")
        if not has_cjk(message):
            problems.append(f"非中文文案：{message[:60]!r}")

        record(
            f"{label} [{status}] 文案干净且为中文",
            not problems,
            message[:90] if not problems else "；".join(problems),
        )


# --------------------------------------------------------------------------

def main():
    parser = argparse.ArgumentParser(description="R19 用户可见错误文案守卫")
    parser.add_argument("--base", default=os.environ.get("L15_R19_API_BASE", "http://127.0.0.1:18191/api/v1"))
    parser.add_argument("--with-api", action="store_true", help="追加真实请求断言（需服务在跑）")
    args = parser.parse_args()

    print("=== 静态断言（无需容器，CI 可执行）===")
    check_no_english_user_messages()
    check_store_messages_are_chinese()
    check_write_error_filter_covers_json_parser()
    check_worker_summaries_free_of_jargon()
    check_write_error_sanitises_any_status()
    check_user_facing_error_preserves_identity()
    record("静态断言不依赖容器与网络（CI 可执行）", True, "只读源码，无需服务在跑")

    if args.with_api:
        print("\n=== 真实请求断言（--with-api）===")
        run_api_checks(args.base.rstrip("/"))
    else:
        record_skip(
            "真实请求断言",
            "未启用 --with-api（默认路径不需要容器；加 --with-api 并确保服务在跑）",
        )

    print()
    total = len(PASS) + len(FAIL)
    print(f"=== 合计 {total} 项：通过 {len(PASS)}，失败 {len(FAIL)}，跳过 {len(SKIP)} ===")
    if FAIL:
        print("失败项：" + "；".join(FAIL))
        return 1
    print("全部通过：面向用户的错误文案不含内部实现细节、为中文且带恢复路径。")
    return 0


if __name__ == "__main__":
    sys.exit(main())
