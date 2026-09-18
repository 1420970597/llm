#!/usr/bin/env python3
"""issue #66 回归测试：不存在的资源必须 404，且响应体不得是英文兜底文案。

背景：store 层用 pgx.ErrNoRows 表示「记录不存在」，handler 一律按 500 上报，
writeError 又把 5xx 统一替换成 "internal server error" —— 两层叠加让客户端
错误变成 500 + 英文。修复在共享出口 writeError 里集中识别 ErrNoRows。

这条测试打真实运行的 API，不 mock：
    python3 test/test_api_error_status.py --base http://127.0.0.1:18100

断言：
  1. GET /api/v1/datasets/{不存在}            -> 404（不是 500）
  2. GET /api/v1/datasets/{不存在}/pipeline/progress -> 404
  3. 上述响应体不得含 "internal server error" 字样
  4. 对照项：GET /api/v1/datasets/{存在}      -> 200（证明不是把所有请求都变成 404）
  5. 4xx 的业务提示原样透出（错误映射没有过度改写）
"""

import argparse
import json
import os
import sys
import urllib.error
import urllib.request

ADMIN_EMAIL = os.environ.get("ADMIN_EMAIL", "admin@company.com")
ADMIN_PASSWORD = os.environ.get("ADMIN_PASSWORD", "admin123456")

PASS, FAIL = [], []


def record(name, ok, detail=""):
    (PASS if ok else FAIL).append(name)
    print(f"  [{'PASS' if ok else 'FAIL'}] {name}" + (f" :: {detail}" if detail else ""))


class Session:
    """cookie 会话（该 API 用 HttpOnly cookie 鉴权）。"""

    def __init__(self, base):
        self.base = base.rstrip("/")
        self.cookie = ""

    def call(self, method, path, payload=None):
        # base 来自命令行且默认是 http，仍显式校验 scheme，避免被传成 file:/ 等。
        if not self.base.startswith(("http://", "https://")):
            raise ValueError(f"--base 必须是 http(s) URL，收到 {self.base!r}")
        data = json.dumps(payload).encode() if payload is not None else None
        req = urllib.request.Request(
            self.base + path, data=data, method=method,
            headers={"Content-Type": "application/json",
                     **({"Cookie": self.cookie} if self.cookie else {})},
        )
        try:
            with urllib.request.urlopen(req, timeout=30) as resp:  # nosec B310 — scheme 已在上方校验为 http(s)
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
        except Exception as exc:  # noqa: BLE001 - 网络错误按失败报告
            return 0, {"error": str(exc)}


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--base", default="http://127.0.0.1:18100")
    args = parser.parse_args()

    session = Session(args.base)
    code, body = session.call("POST", "/api/v1/auth/login",
                              {"email": ADMIN_EMAIL, "password": ADMIN_PASSWORD})
    if code != 200:
        print(f"登录失败 HTTP {code} {body}；无法继续（输入缺失）")
        return 2

    # 1-3. 不存在的任务：必须 404，且不得出现英文兜底文案。
    for path in ("/api/v1/datasets/99999", "/api/v1/datasets/99999/pipeline/progress"):
        code, body = session.call("GET", path)
        text = json.dumps(body, ensure_ascii=False)
        record(f"{path} 返回 404（而非 500）", code == 404, f"HTTP {code} {text[:120]}")
        record(f"{path} 响应体不含英文兜底文案",
               "internal server error" not in text.lower(), text[:120])

    # 4. 对照：存在的任务仍返回 200，确认修复没有把一切变成 404。
    code, body = session.call("GET", "/api/v1/datasets")
    items = body if isinstance(body, list) else (body or {}).get("items", [])
    record("GET /api/v1/datasets 仍可用", code == 200, f"HTTP {code}")
    if items:
        ds = items[0]
        ds_id = ds.get("id") if isinstance(ds, dict) else None
        if ds_id:
            code, _ = session.call("GET", f"/api/v1/datasets/{ds_id}")
            record(f"存在的任务 #{ds_id} 返回 200（未误伤）", code == 200, f"HTTP {code}")

    # 5. 4xx 业务提示原样透出，不被改写。
    code, body = session.call("POST", "/api/v1/eval/runs", {"name": "验收-缺字段"})
    text = json.dumps(body, ensure_ascii=False)
    record("4xx 业务提示原样透出（非兜底文案）",
           code in (400, 422) and "internal server error" not in text.lower(),
           f"HTTP {code} {text[:140]}")

    print(f"\n=== 结果：通过 {len(PASS)} · 失败 {len(FAIL)} ===")
    if FAIL:
        print("失败项：")
        for item in FAIL:
            print(f"  - {item}")
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main())
