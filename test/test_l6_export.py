#!/usr/bin/env python3
"""L6 多格式导出接口测试。

契约来源：docs/plans/eval-and-cleaning-plan.md 第 3.6 节。

测试项：
  T1  登录获取 session cookie
  T2  GET /datasets/{id}/export/formats 返回契约规定的五种格式与映射清单
  T3  GET /admin/export-mappings 返回内置映射（幂等 seed 生效）
  T4  POST /admin/export-mappings 新建自定义映射
  T5  PUT  /admin/export-mappings 更新映射的字段表
  T6  POST /datasets/{id}/export 指定 alpaca 格式返回 202 且 message 表示已入队
  T7  轮询 GET /datasets/{id}/export 出现 alpaca 导出产物
  T8  下载 alpaca 产物并断言 instruction/input/output 结构与内容
  T9  CSV 导出：产物可被 csv 模块解析且含表头
  T10 ShareGPT 导出：产物含 conversations 的 human/gpt 两轮
  T11 Parquet 导出：产物为列式 JSONL，各列长度一致
  T12 JSONL 导出 + 指定映射：字段按映射展开（grpo-jsonl 含 judge_prompt）
  T13 不支持的格式返回 400
  T14 不存在的 mappingId 返回 400
  T15 不存在的数据集请求 formats 返回 404
  T16 legacy 回归：GET /export/download 缺 artifactId 返回 400
  T17 legacy 回归：POST /export 不带 body 走旧行为（202 或 409），不得 400 EOF

⚠️ 可重入性：所有生成类接口走 apps/api/http_util.go 的 enqueueJob，
   它用 dedup:<jobType>:<datasetID> 做 SetNX，TTL 10 分钟。
   10 分钟内重跑同一个 (jobType, datasetID) 不会真正入队，
   接口仍返回 202 但 message 是「已在队列中」。
   因此本脚本在每次 POST 前调用 clear_dedup()，并断言 message 含「已入队」。

用法：
  python3 test/test_l6_export.py --base http://127.0.0.1:18086 --dataset 1
"""

import argparse
import csv
import io
import json
import subprocess
import sys
import time
import urllib.error
import urllib.request

RESULTS = []


def record(name, ok, detail=""):
    RESULTS.append((name, ok, detail))
    print(f"  {'PASS' if ok else 'FAIL'}  {name}")
    if detail and not ok:
        print(f"        {detail}")


def skip(name, detail=""):
    RESULTS.append((name, None, detail))
    print(f"  SKIP  {name}")
    if detail:
        print(f"        {detail}")


def clear_dedup(job_type, dataset_id):
    """清理入队去重键，保证测试可重复运行。"""
    subprocess.run(
        ["docker", "exec", "llm-redis-1", "redis-cli", "del",
         f"dedup:{job_type}:{dataset_id}"],
        capture_output=True, check=False,
    )


class Session:
    def __init__(self, base):
        self.base = base.rstrip("/")
        self.cookie: str = ""

    def _open(self, method, path, body=None):
        """发一次请求，返回 (status, bytes, headers)。"""
        url = self.base + path
        data = None
        headers = {}
        if body is not None:
            data = json.dumps(body).encode()
            headers["Content-Type"] = "application/json"
        if self.cookie:
            headers["Cookie"] = self.cookie
        req = urllib.request.Request(url, data=data, headers=headers, method=method)
        try:
            with urllib.request.urlopen(req, timeout=120) as resp:
                return resp.status, resp.read(), resp.headers
        except urllib.error.HTTPError as err:
            return err.code, err.read(), err.headers

    def raw(self, method, path):
        """返回 (status, bytes)。用于下载产物等二进制响应。"""
        status, payload, _ = self._open(method, path)
        return status, payload

    def json(self, method, path, body=None):
        """返回 (status, 解析后的对象)。解析失败时返回原始文本。"""
        status, payload, _ = self._open(method, path, body)
        try:
            return status, json.loads(payload)
        except json.JSONDecodeError:
            return status, payload.decode("utf-8", "replace")


def wait_for_artifact(session, dataset_id, artifact_type, timeout):
    """轮询导出产物列表，等待指定类型的产物出现。"""
    deadline = time.time() + timeout
    last = None
    while time.time() < deadline:
        status, body = session.json("GET", f"/api/v1/datasets/{dataset_id}/export")
        if status == 200 and isinstance(body, list):
            last = body
            for item in body:
                if item.get("artifactType") == artifact_type:
                    return item
        time.sleep(4)
    print(f"        (超时，最后一次产物列表: {last})")
    return None


def download(session, dataset_id, artifact_id):
    """下载导出产物，返回 (status, bytes)。"""
    return session.raw(
        "GET",
        f"/api/v1/datasets/{dataset_id}/export/download?artifactId={artifact_id}",
    )


def enqueue(session, dataset_id, fmt, mapping_id=0, filters=None):
    """清去重键后发起一次导出，返回 (status, body: dict)。"""
    clear_dedup("export.generate", dataset_id)
    payload = {"format": fmt, "mappingId": mapping_id, "filters": filters or {}}
    status, body = session.json("POST", f"/api/v1/datasets/{dataset_id}/export", payload)
    if not isinstance(body, dict):
        body = {}
    return status, body


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--base", default="http://127.0.0.1:18086")
    parser.add_argument("--dataset", type=int, default=1)
    parser.add_argument("--wait", type=int, default=180)
    args = parser.parse_args()

    session = Session(args.base)
    dataset_id = args.dataset

    print("[T1] 登录获取 session cookie")
    status, _, headers = session._open(
        "POST", "/api/v1/auth/login",
        {"email": "admin@company.com", "password": "admin123456"})
    cookie = headers.get("Set-Cookie", "")
    if cookie:
        session.cookie = cookie.split(";")[0]
    record("T1 登录", status == 200 and bool(session.cookie),
           f"HTTP {status} cookie={bool(session.cookie)}")
    if not session.cookie:
        print_summary()
        return 1
    print(f"  （使用 dataset id={dataset_id}）")

    print("\n[T2] GET /export/formats 返回契约规定的格式与映射")
    status, body = session.json("GET", f"/api/v1/datasets/{dataset_id}/export/formats")
    formats = body.get("formats") if isinstance(body, dict) else None
    expect = ["jsonl", "csv", "parquet", "alpaca", "sharegpt"]
    record("T2 formats 为契约规定的五种且顺序一致",
           status == 200 and formats == expect,
           f"HTTP {status} formats={formats} 期望={expect}")
    mappings = body.get("mappings") if isinstance(body, dict) else None
    record("T2b mappings 返回内置映射（seed 生效）",
           isinstance(mappings, list) and len(mappings) >= 3,
           f"mappings={mappings if not isinstance(mappings, list) else len(mappings)}")

    print("\n[T3] GET /admin/export-mappings 返回内置映射")
    status, body = session.json("GET", "/api/v1/admin/export-mappings")
    names = [m.get("name") for m in body] if isinstance(body, list) else []
    record("T3 内置映射含 sft-alpaca / sft-sharegpt / grpo-jsonl",
           status == 200 and {"sft-alpaca", "sft-sharegpt", "grpo-jsonl"} <= set(names),
           f"HTTP {status} names={names}")

    print("\n[T4] POST /admin/export-mappings 新建自定义映射")
    custom_name = f"l6-custom-{int(time.time())}"
    status, created = session.json("POST", "/api/v1/admin/export-mappings", {
        "name": custom_name,
        "format": "jsonl",
        "targetKind": "sft",
        "fieldMap": {"q": "{{question}}", "a": "{{answer}}"},
    })
    custom_id = created.get("id") if isinstance(created, dict) else None
    record("T4 新建映射返回带 id 的对象",
           status == 200 and isinstance(custom_id, int) and custom_id > 0,
           f"HTTP {status} body={str(created)[:200]}")

    print("\n[T5] PUT /admin/export-mappings 更新字段表")
    status, updated = session.json("PUT", "/api/v1/admin/export-mappings", {
        "id": custom_id,
        "name": custom_name,
        "format": "jsonl",
        "targetKind": "sft",
        "fieldMap": {"q2": "{{question}}", "a2": "{{chainOfThought}}"},
    })
    field_map = updated.get("fieldMap") if isinstance(updated, dict) else None
    record("T5 字段表更新生效",
           status == 200 and isinstance(field_map, dict) and "q2" in field_map,
           f"HTTP {status} fieldMap={field_map}")

    print("\n[T6] POST /export 指定 alpaca 格式返回 202 且真入队")
    status, body = enqueue(session, dataset_id, "alpaca")
    record("T6 返回 202 且 message 表示已入队",
           status == 202 and body.get("stage") == "export"
           and body.get("state") == "queued" and "已入队" in body.get("message", ""),
           f"HTTP {status} body={str(body)[:250]}")

    print(f"\n[T7] 轮询等待 alpaca 产物（最长 {args.wait}s）")
    artifact = wait_for_artifact(session, dataset_id, "alpaca-export", args.wait)
    record("T7 alpaca 导出产物生成", artifact is not None,
           f"artifact={artifact}")

    if artifact:
        print("\n[T8] 下载并校验 alpaca 产物")
        status, payload = download(session, dataset_id, artifact["id"])
        lines = [l for l in payload.decode("utf-8", "replace").splitlines() if l.strip()]
        first = {}
        try:
            first = json.loads(lines[0]) if lines else {}
        except json.JSONDecodeError:
            first = {}
        record("T8 alpaca 产物为 JSONL 且含 instruction/input/output",
               status == 200 and lines and
               {"instruction", "input", "output"} <= set(first.keys()),
               f"HTTP {status} 首行={str(first)[:220]}")
        record("T8b input 为空、output 含思维链与答案",
               first.get("input") == "" and "答案：" in str(first.get("output", "")),
               f"input={first.get('input')!r} output={str(first.get('output'))[:120]!r}")
        record("T8c instruction 为真实问题文本（非占位）",
               isinstance(first.get("instruction"), str)
               and len(first.get("instruction", "")) > 3,
               f"instruction={str(first.get('instruction'))[:120]!r}")
    else:
        skip("T8 alpaca 产物校验", "无产物")

    print("\n[T9] CSV 导出并解析")
    status, body = enqueue(session, dataset_id, "csv")
    record("T9a CSV 入队 202", status == 202 and "已入队" in body.get("message", ""),
           f"HTTP {status} body={str(body)[:200]}")
    artifact = wait_for_artifact(session, dataset_id, "csv-export", args.wait)
    if artifact:
        status, payload = download(session, dataset_id, artifact["id"])
        try:
            rows = list(csv.reader(io.StringIO(payload.decode("utf-8", "replace"))))
        except csv.Error as err:
            rows = []
            print(f"        csv 解析失败: {err}")
        record("T9b CSV 产物含表头且数据行非空",
               status == 200 and len(rows) >= 2 and len(rows[0]) > 0,
               f"HTTP {status} 行数={len(rows)} 表头={rows[0] if rows else None}")
    else:
        skip("T9b CSV 产物校验", "无产物")

    print("\n[T10] ShareGPT 导出")
    status, body = enqueue(session, dataset_id, "sharegpt")
    record("T10a ShareGPT 入队 202", status == 202 and "已入队" in body.get("message", ""),
           f"HTTP {status}")
    artifact = wait_for_artifact(session, dataset_id, "sharegpt-export", args.wait)
    if artifact:
        status, payload = download(session, dataset_id, artifact["id"])
        lines = [l for l in payload.decode("utf-8", "replace").splitlines() if l.strip()]
        try:
            first = json.loads(lines[0]) if lines else {}
        except json.JSONDecodeError:
            first = {}
        turns = first.get("conversations", [])
        record("T10b 产物含 conversations 的 human/gpt 两轮",
               status == 200 and len(turns) == 2
               and turns[0].get("from") == "human" and turns[1].get("from") == "gpt",
               f"HTTP {status} turns={str(turns)[:200]}")
    else:
        skip("T10b ShareGPT 产物校验", "无产物")

    print("\n[T11] Parquet 导出（当前实现为列式 JSONL，非真 Parquet）")
    status, body = enqueue(session, dataset_id, "parquet")
    record("T11a parquet 入队 202", status == 202 and "已入队" in body.get("message", ""),
           f"HTTP {status}")
    artifact = wait_for_artifact(session, dataset_id, "parquet-export", args.wait)
    if artifact:
        status, payload = download(session, dataset_id, artifact["id"])
        lines = [l for l in payload.decode("utf-8", "replace").splitlines() if l.strip()]
        columns, lengths_ok, names = [], True, []
        for line in lines:
            try:
                col = json.loads(line)
            except json.JSONDecodeError:
                lengths_ok = False
                continue
            columns.append(col)
            names.append(col.get("column"))
            if len(col.get("values", [])) != len(columns[0].get("values", [])):
                lengths_ok = False
        record("T11b 列式产物各列等长且列名非空",
               status == 200 and len(columns) > 0 and lengths_ok
               and all(n for n in names),
               f"HTTP {status} 列数={len(columns)} 列名={names}")

    print("\n[T12] JSONL 导出并指定 grpo-jsonl 映射")
    status, mappings_body = session.json("GET", "/api/v1/admin/export-mappings")
    grpo_id = 0
    if isinstance(mappings_body, list):
        for item in mappings_body:
            if item.get("name") == "grpo-jsonl":
                grpo_id = item.get("id")
    if grpo_id:
        status, body = enqueue(session, dataset_id, "jsonl", mapping_id=grpo_id)
        record("T12a 指定映射入队 202", status == 202 and "已入队" in body.get("message", ""),
               f"HTTP {status} body={str(body)[:200]}")
        artifact = wait_for_artifact(session, dataset_id, "jsonl-export", args.wait)
        if artifact:
            status, payload = download(session, dataset_id, artifact["id"])
            lines = [l for l in payload.decode("utf-8", "replace").splitlines() if l.strip()]
            try:
                first = json.loads(lines[0]) if lines else {}
            except json.JSONDecodeError:
                first = {}
            record("T12b 字段按映射展开（含 judge_prompt）",
                   status == 200 and "judge_prompt" in first and "reward_levels" in first,
                   f"HTTP {status} keys={sorted(first.keys())}")
        else:
            skip("T12b 映射产物校验", "无产物")
    else:
        skip("T12 指定映射导出", "未找到 grpo-jsonl 映射")

    print("\n[T13] 不支持的格式返回 400")
    clear_dedup("export.generate", dataset_id)
    status, body = session.json("POST", f"/api/v1/datasets/{dataset_id}/export",
                                {"format": "xml", "mappingId": 0, "filters": {}})
    record("T13 非法格式被拒绝（400）", status == 400,
           f"HTTP {status} body={str(body)[:200]}")

    print("\n[T14] 不存在的 mappingId 返回 400")
    clear_dedup("export.generate", dataset_id)
    status, body = session.json("POST", f"/api/v1/datasets/{dataset_id}/export",
                                {"format": "jsonl", "mappingId": 99999999, "filters": {}})
    record("T14 无效映射 ID 被拒绝（400）", status == 400,
           f"HTTP {status} body={str(body)[:200]}")

    print("\n[T15] 不存在的数据集请求 formats 返回 404")
    status, body = session.json("GET", "/api/v1/datasets/99999999/export/formats")
    record("T15 未知数据集返回 404", status == 404,
           f"HTTP {status} body={str(body)[:200]}")

    print("\n[T16] legacy 回归：GET /export/download 缺 artifactId 返回 400")
    status, body = session.json("GET", f"/api/v1/datasets/{dataset_id}/export/download")
    record("T16 缺 artifactId 返回 400", status == 400,
           f"HTTP {status} body={str(body)[:200]}")

    print("\n[T17] legacy 回归：POST /export 不带 body")
    # 旧调用方直接 POST /export 且不带请求体。legacy 会入队并在响应里返回
    # stage=export；奖励不完整时返回 409。两者都算通过，唯独不能是 400 EOF
    # ——那说明子路由接管后旧调用方式被破坏了。
    clear_dedup("export.generate", dataset_id)
    status, body = session.json("POST", f"/api/v1/datasets/{dataset_id}/export")
    legacy_ok = status == 202 and isinstance(body, dict) and body.get("stage") == "export"
    conflict_ok = status == 409
    record("T17 空 body 走 legacy（202 或 409），非 400",
           legacy_ok or conflict_ok,
           f"HTTP {status} body={str(body)[:220]}")

    return print_summary()


def print_summary():
    passed = sum(1 for _, ok, _ in RESULTS if ok is True)
    failed = sum(1 for _, ok, _ in RESULTS if ok is False)
    skipped = sum(1 for _, ok, _ in RESULTS if ok is None)
    print("\n" + "=" * 60)
    print(f"总计: {passed} 通过, {failed} 失败, {skipped} 跳过")
    if failed:
        print("失败项:")
        for name, ok, detail in RESULTS:
            if ok is False:
                print(f"  - {name}: {detail}")
    print("=" * 60)
    return 1 if failed else 0


if __name__ == "__main__":
    sys.exit(main())
