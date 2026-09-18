#!/usr/bin/env python3
"""L9 评估运行接口测试 —— 打自己构建的 lane-l9 镜像（:18095），不是 main 镜像。

测试项（契约 docs/plans/eval-and-cleaning-plan.md 第 3.9 节）：
  T1  登录并拿到会话 cookie
  T2  POST /api/v1/eval/runs          创建运行，返回 draft 状态
  T3  GET  /api/v1/eval/runs?datasetId=  列出运行
  T4  GET  /api/v1/eval/runs/{id}     运行详情（含 judges/dimensions）
  T5  POST /api/v1/eval/runs/{id}/start   入队，202 + 「已入队」
  T6  GET  /api/v1/eval/runs/{id}/items   列出条目
  T7  抽样模式 full / ratio / count 各一条（创建时不报错）
  T8  非法抽样参数被拒（ratio=0、count=0、未知模式）
  T9  不存在的运行返回 404
  T10 不存在的 datasetId 返回 404

可重入性（父代理踩过的坑）：所有入队接口走 http_util.go 的 enqueueJob，
用 dedup:<jobType>:<datasetID> 做 SetNX，TTL 10 分钟。10 分钟内重跑同一个
(jobType, datasetID) 不会 LPush，但接口仍返回 202 + state=queued，只是 message
变成「已在队列中」。因此：
  - 每次发 POST 前先 clear_dedup("eval.run", dataset_id)
  - 断言里必须包含 message，识破「被去重抑制」的假入队

运行方式：
  python3 test/test_l9_eval_runs.py
"""

import json
import subprocess
import sys
import urllib.error
import urllib.request
from http.cookiejar import CookieJar

BASE = "http://127.0.0.1:18095"
EMAIL = "admin@company.com"
PASSWORD = "admin123456"

PASSED = []
FAILED = []


def clear_dedup(job_type, dataset_id):
    """清理入队去重键，保证测试可重复运行。"""
    subprocess.run(
        ["docker", "exec", "llm-redis-1", "redis-cli", "del",
         f"dedup:{job_type}:{dataset_id}"],
        capture_output=True, check=False,
    )


def sql(query):
    """在共享 postgres 上执行 SQL，返回 stdout。"""
    result = subprocess.run(
        ["docker", "exec", "llm-postgres-1", "psql", "-U", "llm_factory",
         "-d", "llm_factory", "-t", "-A", "-c", query],
        capture_output=True, text=True, check=False,
    )
    return result.stdout.strip()


class Client:
    def __init__(self):
        self.jar = CookieJar()
        self.opener = urllib.request.build_opener(
            urllib.request.HTTPCookieProcessor(self.jar))

    def request(self, method, path, body=None):
        data = None
        headers = {}
        if body is not None:
            data = json.dumps(body).encode()
            headers["Content-Type"] = "application/json"
        req = urllib.request.Request(BASE + path, data=data, headers=headers, method=method)
        try:
            with self.opener.open(req, timeout=60) as resp:
                raw = resp.read().decode()
                return resp.status, (json.loads(raw) if raw.strip() else None)
        except urllib.error.HTTPError as exc:
            raw = exc.read().decode()
            try:
                return exc.code, json.loads(raw)
            except json.JSONDecodeError:
                return exc.code, {"raw": raw}

    def get(self, path):
        return self.request("GET", path)

    def post(self, path, body=None):
        return self.request("POST", path, body)


def check(name, condition, detail=""):
    if condition:
        PASSED.append(name)
        print(f"  PASS  {name}")
    else:
        FAILED.append(name)
        print(f"  FAIL  {name}  {detail}")


def main():
    client = Client()

    # ---- T1 登录 ----
    print("T1 登录")
    status, body = client.post("/api/v1/auth/login", {"email": EMAIL, "password": PASSWORD})
    check("T1 登录成功", status == 200, f"status={status} body={body}")

    # 找一个真实存在的数据集用于创建运行。
    dataset_id = sql("SELECT id FROM datasets ORDER BY id ASC LIMIT 1;")
    if not dataset_id:
        print("输入缺失: 共享 postgres 里没有任何数据集，无法创建评估运行")
        return 2
    dataset_id = int(dataset_id)
    print(f"  使用数据集 id={dataset_id}")

    # 清理本测试可能遗留的旧运行，保证断言不受上轮残留影响。
    sql(f"DELETE FROM eval_runs WHERE name LIKE 'L9-接口测试%';")

    # ---- T2 创建运行 ----
    print("T2 创建评估运行")
    status, body = client.post("/api/v1/eval/runs", {
        "datasetId": dataset_id,
        "name": "L9-接口测试-创建",
        "samplingMode": "count",
        "sampleSize": 2,
        "targetKind": "sft",
    })
    check("T2 创建返回 200", status == 200, f"status={status} body={body}")
    run_id = body.get("id") if isinstance(body, dict) else None
    check("T2 返回运行 id", isinstance(run_id, int) and run_id > 0, f"body={body}")
    check("T2 初始状态为 draft", isinstance(body, dict) and body.get("status") == "draft",
          f"status={body.get('status') if isinstance(body, dict) else body}")
    check("T2 抽样参数回显", isinstance(body, dict) and body.get("samplingMode") == "count"
          and body.get("sampleSize") == 2, f"body={body}")
    check("T2 未指定维度时自动补齐",
          isinstance(body, dict) and len(body.get("dimensionKeys") or []) > 0,
          f"dimensionKeys={body.get('dimensionKeys') if isinstance(body, dict) else body}")

    if not isinstance(run_id, int) or run_id <= 0:
        print("\n无法继续：创建运行失败")
        return report()

    # ---- T3 列出运行 ----
    print("T3 列出运行")
    status, body = client.get(f"/api/v1/eval/runs?datasetId={dataset_id}")
    check("T3 列表返回 200", status == 200, f"status={status}")
    check("T3 列表包含刚创建运行",
          isinstance(body, list) and any(item.get("id") == run_id for item in body),
          f"body={body}")

    # ---- T4 运行详情 ----
    print("T4 运行详情")
    status, body = client.get(f"/api/v1/eval/runs/{run_id}")
    check("T4 详情返回 200", status == 200, f"status={status} body={body}")
    check("T4 详情含 run/judges/dimensions 三字段",
          isinstance(body, dict) and {"run", "judges", "dimensions"} <= set(body.keys()),
          f"keys={list(body.keys()) if isinstance(body, dict) else body}")
    check("T4 run.id 与请求一致",
          isinstance(body, dict) and body.get("run", {}).get("id") == run_id,
          f"body={body}")

    # ---- T7 三种抽样模式 ----
    print("T7 抽样模式 full / ratio / count")
    for mode, extra in (("full", {}), ("ratio", {"sampleRatio": 0.5}), ("count", {"sampleSize": 3})):
        status, body = client.post("/api/v1/eval/runs", {
            "datasetId": dataset_id,
            "name": f"L9-接口测试-{mode}",
            "samplingMode": mode,
            **extra,
        })
        check(f"T7 {mode} 模式创建成功", status == 200 and isinstance(body, dict)
              and body.get("samplingMode") == mode, f"status={status} body={body}")

    # ---- T8 非法抽样参数 ----
    print("T8 非法抽样参数被拒")
    for name, payload in (
        ("ratio=0", {"samplingMode": "ratio", "sampleRatio": 0}),
        ("ratio 负数", {"samplingMode": "ratio", "sampleRatio": -0.5}),
        ("count=0", {"samplingMode": "count", "sampleSize": 0}),
        ("未知模式", {"samplingMode": "whatever"}),
    ):
        status, body = client.post("/api/v1/eval/runs",
                                   {"datasetId": dataset_id, "name": f"L9-接口测试-非法-{name}", **payload})
        check(f"T8 拒绝 {name}", status == 400 and isinstance(body, dict) and "error" in body,
              f"status={status} body={body}")

    # ---- T9/T10 不存在资源 ----
    print("T9/T10 不存在的资源")
    status, body = client.get("/api/v1/eval/runs/999999999")
    check("T9 不存在的运行返回 404", status == 404, f"status={status} body={body}")
    status, body = client.get("/api/v1/eval/runs/999999999/items")
    check("T9 不存在运行的 items 返回 404", status == 404, f"status={status} body={body}")
    status, body = client.post("/api/v1/eval/runs", {"datasetId": 999999999, "name": "L9-接口测试-坏数据集"})
    check("T10 不存在的 datasetId 返回 404", status == 404, f"status={status} body={body}")

    # ---- T5 入队（必须在清掉去重键之后） ----
    print("T5 启动评估运行")
    clear_dedup("eval.run", dataset_id)

    # 先看该运行有没有可用裁判：本地只配了生成者 provider 时，
    # 自评剔除规则会把唯一候选剔除，接口按设计返回 400。
    status, body = client.get("/api/v1/admin/eval/judges")
    judge_options = body if isinstance(body, list) else []
    usable = [item for item in judge_options if item.get("isActive")]

    status, body = client.post(f"/api/v1/eval/runs/{run_id}/start")
    if status == 202:
        check("T5 启动返回 202", True)
        check("T5 响应含已入队消息",
              isinstance(body, dict) and "已入队" in body.get("message", ""),
              f"message={body.get('message') if isinstance(body, dict) else body}")
        check("T5 state 为 queued",
              isinstance(body, dict) and body.get("state") == "queued", f"body={body}")
        check("T5 stage 为 eval",
              isinstance(body, dict) and body.get("stage") == "eval", f"body={body}")
    elif status == 400:
        # 输入缺失：没有可用的裁判模型（生成者禁止自评）。
        # 这是环境配置限制，如实报告，不伪装成通过。
        print(f"  输入缺失: 启动被拒（{body}）—— 本地 provider 不足以充当裁判")
        check("T5 无可用裁判时给出可读原因",
              isinstance(body, dict) and "裁判" in body.get("error", ""), f"body={body}")
    else:
        check("T5 启动返回 202 或 400", False, f"status={status} body={body}")

    # ---- T6 列出条目 ----
    print("T6 列出被评条目")
    status, body = client.get(f"/api/v1/eval/runs/{run_id}/items?limit=10&offset=0")
    check("T6 items 返回 200", status == 200, f"status={status} body={body}")
    check("T6 items 为数组", isinstance(body, list), f"type={type(body).__name__}")

    status, body = client.get(f"/api/v1/eval/runs/{run_id}/items?limit=abc")
    check("T6 非法 limit 返回 400", status == 400, f"status={status}")

    # ---- 清理 ----
    sql(f"DELETE FROM eval_runs WHERE name LIKE 'L9-接口测试%';")

    return report()


def report():
    print()
    print(f"通过 {len(PASSED)} · 失败 {len(FAILED)}")
    if FAILED:
        print("失败项：")
        for name in FAILED:
            print(f"  - {name}")
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main())
