#!/usr/bin/env python3
"""L15-R5 数据集子路由契约测试：未知子路径必须 404，已知路径行为不变（issue #8）。

契约：docs/plans/issue-remediation-plan.md 的 R5 行；接口契约见
docs/plans/eval-and-cleaning-plan.md 第 1.1 节。

背景：#8 的核心危害是 routeDatasetGet / routeDatasetActions 的 default 分支把
**任意未知子路径**静默交给 getDataset，返回 200 + 数据集图。于是：

  GET /datasets/6/whatever              -> 200 数据集图
  GET /datasets/6/questions/difficulty-stats（未注册时）-> 200 数据集图

一个不存在的路径和一个未实现的端点返回完全一样 —— 前端 axios 收到 200、
TS 类型断言通过编译、页面渲染出 undefined 字段而不报错。测试如果只断言状态码
就会全绿。本测试断言的不只是状态码，还有**响应体形状**：未知子路径不得返回
数据集图对象。

被测目标：本 lane worktree 构建出的 api 实例（默认 http://127.0.0.1:18105）。
数据准备：真实创建数据集（不伪造），结束在 finally 里按精确 id 删除。

## 反污染（契约 §6.2）
  - 唯一名字前缀 l15-r5-<pid>-
  - 结束（含失败路径）按精确 id 删除 datasets 及其级联子表
  - 禁止无 WHERE 的批量删除

## 测试项
  T1   GET  /datasets/{id}                      -> 200 且 body 含 dataset/domains（精确路径不变）
  T2   GET  /datasets/{id}/                     -> 200（尾斜杠同样指向数据集本身）
  T3   GET  /datasets/{id}/definitely-not-a-real-subresource -> 404 + {"error":"未找到该子资源"}
  T4   T3 的响应体不得是数据集图（不得含 dataset 键）
  T5   GET  /datasets/{id}/questions/nope       -> 404（已注册段下的未知 rest）
  T6   GET  /datasets/{id}/export/formats       -> 200（L6 多段 suffix 端点，注册表可表达）
  T7   GET  /datasets/{id}/questions/difficulty-stats -> 200（L3 多段 suffix 端点）
  T8   GET  /datasets/{id}/questions            -> 200 列表（legacy 语义未被注册表抢走）
  T9   GET  /datasets/{id}/domains              -> 200 且 body 含 domains（换 404 兜底后的回归守卫）
  T10  GET  /datasets/{id}/pipeline/progress    -> 200（legacy 分支不变）
  T11  POST /datasets/{id}/definitely-not-a-real-post-action -> 404
  T12  POST /datasets/{id}/questions/generate    -> 非 404（legacy POST 分支不变）
  T13  GET  /datasets/{id}/export/nope          -> 404（已注册 export 段的未知 rest）
  T14  GET  /datasets/{id}/reward-levels        -> 404（该路径只注册了 PUT，GET 不该静默成功）

## 两类 404 的区别（读断言时注意）
  1. **未注册的首段**（T3/T11/T14）：之前落 default 返回 200 + 数据集图，是 issue #8
     本体。本 lane 修正为 404 + 契约冻结文案 {"error":"未找到该子资源"}。
  2. **已注册段下的未知 rest**（T5/T13）：由各 lane 自己的 router（routes_questions_v2.go /
     routes_export_formats.go）的 default 分支处理，修复前就已是 404（不是静默 200）。
     它们回的是 Go 标准的 text/plain "404 page not found"，不是契约的 JSON 文案。
     这些 router 不属于本 lane 认领文件（契约 §2 只给了 routes.go / main.go 的 default），
     因此本测试只断言状态码，并把文案差异记在 PR 的「未包含的改动及理由」里。
"""

import argparse
import json
import os
import subprocess
import sys
import time
import urllib.error
import urllib.request

ADMIN_EMAIL = os.environ.get("ADMIN_EMAIL", "admin@company.com")
ADMIN_PASSWORD = os.environ.get("ADMIN_PASSWORD", "admin123456")

PG_CONTAINER = os.environ.get("L15_PG_CONTAINER", "llm-postgres-1")
PG_USER = os.environ.get("L15_PG_USER", "llm_factory")
PG_DB = os.environ.get("L15_PG_DB", "llm_factory")

PASS, FAIL = [], []

# 契约 §1.1 冻结的未知子路径文案。
NOT_FOUND_MESSAGE = "未找到该子资源"


def record(name, ok, detail=""):
    (PASS if ok else FAIL).append(name)
    print(f"  [{'PASS' if ok else 'FAIL'}] {name}" + (f" :: {detail}" if detail else ""))


class Session:
    """cookie 会话（该 API 用 HttpOnly cookie 鉴权）。"""

    def __init__(self, base):
        self.base = base.rstrip("/")
        self.cookie = ""

    def call(self, method, path, payload=None, timeout=30):
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
            with urllib.request.urlopen(req, timeout=timeout) as resp:  # nosec B310 — scheme 已在上方校验为 http(s)
                raw = resp.read().decode()
                for header in resp.headers.get_all("Set-Cookie") or []:
                    self.cookie = header.split(";")[0]
                return resp.status, self._parse(raw)
        except urllib.error.HTTPError as exc:
            raw = exc.read().decode()
            return exc.code, self._parse(raw)
        except Exception as exc:  # noqa: BLE001 - 网络错误按失败报告
            return 0, {"error": str(exc)}

    @staticmethod
    def _parse(raw):
        try:
            return json.loads(raw) if raw.strip() else {}
        except json.JSONDecodeError:
            return {"raw": raw[:400]}


def psql(sql):
    """在共享 Postgres 里执行一条 SQL（只用于清理自己创建的行）。"""
    cmd = ["docker", "exec", "-i", PG_CONTAINER,
           "psql", "-U", PG_USER, "-d", PG_DB, "-v", "ON_ERROR_STOP=1", "-tAc", sql]
    proc = subprocess.run(cmd, capture_output=True, text=True)
    if proc.returncode != 0:
        raise RuntimeError(f"psql 失败: {proc.stderr.strip()}")
    return proc.stdout.strip()


def cleanup(dataset_id, prefix):
    """按精确 id 删除本测试创建的数据集（级联子表由外键 ON DELETE CASCADE 处理）。

    只删自己建的：先确认该行的 name 带本测试的唯一前缀，避免误删。
    """
    if not dataset_id:
        return
    try:
        name = psql(f"SELECT name FROM datasets WHERE id = {int(dataset_id)}")
        if not name:
            print(f"  [cleanup] dataset {dataset_id} 已不存在")
            return
        if not name.startswith(prefix):
            print(f"  [cleanup] ⚠️ dataset {dataset_id} 名字 {name!r} 不含本测试前缀，拒绝删除")
            return
        psql(f"DELETE FROM datasets WHERE id = {int(dataset_id)}")
        print(f"  [cleanup] 已删除测试数据集 id={dataset_id} name={name}")
    except Exception as exc:  # noqa: BLE001 - 清理失败只报告，不掩盖测试结论
        print(f"  [cleanup] ⚠️ 清理 dataset {dataset_id} 失败: {exc}")


def is_dataset_graph(body):
    """判断响应体是否是「数据集图」（issue #8 里未知子路径被误返回的那个对象）。"""
    return isinstance(body, dict) and "dataset" in body and "domains" in body


def expect_not_found(session, name, method, path, timeout=30):
    code, body = session.call(method, path, timeout=timeout)
    ok = code == 404
    detail = f"HTTP {code} body={str(body)[:120]}"
    if not ok:
        detail += "（期望 404：未知子路径不得静默成功，见 issue #8）"
    record(name, ok, detail)
    return code, body


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--base", default=os.environ.get("L15_R5_BASE_URL", "http://127.0.0.1:18105"))
    args = parser.parse_args()

    prefix = f"l15-r5-{os.getpid()}-"
    session = Session(args.base)

    code, body = session.call("POST", "/api/v1/auth/login",
                              {"email": ADMIN_EMAIL, "password": ADMIN_PASSWORD})
    if code != 200:
        print(f"登录失败 HTTP {code} {body}；无法继续（输入缺失）")
        return 2
    print(f"登录成功，base={args.base}\n")

    dataset_id = None
    try:
        # ---- 前置：真实创建数据集（绑定已引导的 provider id=1）----
        code, body = session.call("POST", "/api/v1/datasets", {
            "name": f"{prefix}{int(time.time())}",
            "rootKeyword": "军事",
            "targetSize": 4,
            "providerId": 1,
            "directionCount": 1,
        })
        if code != 201 or not isinstance(body, dict) or not body.get("id"):
            print(f"创建数据集失败 HTTP {code} {str(body)[:200]}；无法继续（输入缺失）")
            return 2
        dataset_id = body["id"]
        print(f"测试数据集 id={dataset_id}（前缀 {prefix}）\n")

        print("T1-T2 精确路径仍返回数据集图（修复不得矫枉过正）")
        code, body = session.call("GET", f"/api/v1/datasets/{dataset_id}")
        record("T1 GET /datasets/{id} -> 200 数据集图",
               code == 200 and is_dataset_graph(body),
               f"HTTP {code} keys={sorted(body)[:5] if isinstance(body, dict) else type(body).__name__}")
        code, body = session.call("GET", f"/api/v1/datasets/{dataset_id}/")
        record("T2 GET /datasets/{id}/ -> 200 数据集图（尾斜杠仍指向数据集本身）",
               code == 200 and is_dataset_graph(body), f"HTTP {code}")

        print("\nT3-T4 未知子路径必须 404，且不得返回数据集图")
        _, body = expect_not_found(session, "T3 GET /datasets/{id}/definitely-not-a-real-subresource -> 404",
                                   "GET", f"/api/v1/datasets/{dataset_id}/definitely-not-a-real-subresource")
        record("T4 未知子路径响应体不是数据集图",
               not is_dataset_graph(body),
               f"body={str(body)[:120]}")
        record("T4b 404 文案为契约冻结值",
               isinstance(body, dict) and body.get("error") == NOT_FOUND_MESSAGE,
               f"error={body.get('error') if isinstance(body, dict) else body!r}")

        print("\nT5 已注册段下的未知 rest 也必须 404（修复前即已是 404，此处防回归）")
        expect_not_found(session, "T5 GET /datasets/{id}/questions/nope -> 404",
                         "GET", f"/api/v1/datasets/{dataset_id}/questions/nope")

        print("\nT6-T7 多段 suffix 端点（#8 曾被单段接口挡住的两条）")
        code, body = session.call("GET", f"/api/v1/datasets/{dataset_id}/export/formats")
        record("T6 GET /datasets/{id}/export/formats -> 200（L6 多段 suffix 可表达）",
               code == 200 and isinstance(body, dict) and "formats" in body,
               f"HTTP {code} keys={sorted(body)[:5] if isinstance(body, dict) else type(body).__name__}")
        code, body = session.call("GET", f"/api/v1/datasets/{dataset_id}/questions/difficulty-stats")
        record("T7 GET /datasets/{id}/questions/difficulty-stats -> 200（L3 多段 suffix 可表达）",
               code == 200 and not is_dataset_graph(body),
               f"HTTP {code} body={str(body)[:120]}")

        print("\nT8-T10 legacy 分支行为不变（回归守卫）")
        code, body = session.call("GET", f"/api/v1/datasets/{dataset_id}/questions")
        record("T8 GET /datasets/{id}/questions -> 200 列表（legacy 语义未被注册表抢走）",
               code == 200 and isinstance(body, list), f"HTTP {code} type={type(body).__name__}")
        code, body = session.call("GET", f"/api/v1/datasets/{dataset_id}/domains")
        record("T9 GET /datasets/{id}/domains -> 200 且含 domains 字段",
               code == 200 and isinstance(body, dict) and isinstance(body.get("domains"), list),
               f"HTTP {code} keys={sorted(body)[:5] if isinstance(body, dict) else type(body).__name__}")
        code, body = session.call("GET", f"/api/v1/datasets/{dataset_id}/pipeline/progress")
        record("T10 GET /datasets/{id}/pipeline/progress -> 200",
               code == 200, f"HTTP {code} body={str(body)[:120]}")

        print("\nT11-T14 POST 与已注册段的未知子路径")
        expect_not_found(session, "T11 POST /datasets/{id}/definitely-not-a-real-post-action -> 404",
                         "POST", f"/api/v1/datasets/{dataset_id}/definitely-not-a-real-post-action")
        code, body = session.call("POST", f"/api/v1/datasets/{dataset_id}/questions/generate", {})
        # 用 questions/generate 而不是 domains/generate：前者是 legacy 入队分支，
        # 对 draft 数据集会快速返回 409（无 directions），不需要真跑 LLM。
        # 断言必须同时要求 code != 0，否则网络超时会被 "code != 404" 空洞地判为通过。
        record("T12 POST /datasets/{id}/questions/generate 非 404（legacy POST 分支不变）",
               code != 0 and code != 404,
               f"HTTP {code} body={str(body)[:120]}")
        expect_not_found(session, "T13 GET /datasets/{id}/export/nope -> 404（export 段未知 rest）",
                         "GET", f"/api/v1/datasets/{dataset_id}/export/nope")
        expect_not_found(session, "T14 GET /datasets/{id}/reward-levels -> 404（该路径只注册了 PUT）",
                         "GET", f"/api/v1/datasets/{dataset_id}/reward-levels")

    finally:
        cleanup(dataset_id, prefix)

    print(f"\n===== 结果：通过 {len(PASS)} / 失败 {len(FAIL)} =====")
    for name in FAIL:
        print(f"  失败：{name}")
    return 0 if not FAIL else 1


if __name__ == "__main__":
    sys.exit(main())
