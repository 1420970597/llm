#!/usr/bin/env python3
"""L5 SFT 分支（思维链 + 答案）—— 接口测试。

契约：docs/plans/eval-and-cleaning-plan.md 第 3.5 节。

测试项：
  T1  POST /api/v1/auth/login                     登录拿 session cookie
  T2  GET  /api/v1/datasets/{id}/sft              200 + 数组（空数据集也应返回 []）
  T3  POST /api/v1/datasets/{id}/sft/generate     无 provider 的数据集返回 409（不得空转）
  T4  POST /api/v1/datasets/{id}/sft/generate     无问题的数据集返回 409
  T5  POST /api/v1/datasets/{id}/sft/generate     202 + StageEnqueueResult，且**真的入队**
  T6  轮询 GET /sft 等待真实 LLM 生成，断言 chainOfThought 非空且为长链推理
  T7  断言 answer 非空（includeAnswer=true 时）
  T8  断言 chainOfThought 覆盖全部长链标准步骤（逐步对齐，非「首先其次最后」套话）
  T9  POST /sft/generate {"includeAnswer": false} 后 answer 必须为空
  T10 断言 generation_runs 记录了 sft.generate 阶段（断点续跑基础设施真实生效）
  T11 未认证访问 GET /sft 返回 401（证明路由存在且受保护，而非 404）

运行目标：**必须打自己的 lane 容器**，不是 main 分支的镜像。
  http://127.0.0.1:3210 上跑的是 main 镜像，没有本 lane 新加的路由，
  GET /sft 会返回数据集详情（不是数组）、POST /sft/generate 得 404 ——
  那是打错目标，不是代码缺陷。
  正确做法：用 worktree 构建镜像，起独立容器，端口按 lane 错开。
  本 lane（L5）用 18085。

可重入性（踩过的坑）：
  apps/api/http_util.go 的 enqueueJob 用 `dedup:<jobType>:<datasetID>` 做 SetNX，
  TTL **10 分钟**。若不清掉该键，10 分钟内重跑测试会命中去重、静默不入队
  （仍返回 202，但 message 是「已在队列中」），worker 永远收不到任务，
  T6 干等到超时。因此：
    - T5 发 POST 前先清 dedup 键（见 clear_dedup）
    - T5 断言 message 必须含「已入队」，否则 dedup 命中会被误判为通过
  此外 sft_records 有 UNIQUE(dataset_id, question_id)，重跑会覆盖旧样本，
  所以断言只依赖本次生成的内容，不依赖行数递增。

真实 LLM 调用需要 provider.APIKey/BaseURL（本机 provider id=1 已配置）。
若 provider 未配置或未产出样本，T6~T10 会明确标记「输入缺失」而不是伪造通过。

用法：
  python3 test/test_l5_sft.py --base http://127.0.0.1:18085 --dataset 1 --wait 900
"""

import argparse
import json
import os
import subprocess
import sys
import time

import requests

DEFAULT_BASE = "http://127.0.0.1:18085"
REDIS_CONTAINER = "llm-redis-1"
# 本地开发栈的管理员账号（compose 初始化时写入）。允许用环境变量覆盖，
# 避免把凭据硬编码进仓库用于非本地环境。
ADMIN_EMAIL = os.environ.get("L5_ADMIN_EMAIL", "admin@company.com")
ADMIN_PASSWORD = os.environ.get("L5_ADMIN_PASSWORD", "admin123456")

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


def clear_dedup(job_type, dataset_id):
    """清理入队去重键，保证测试可重复运行。

    enqueueJob 用 dedup:<jobType>:<datasetID> 做 SetNX 且 TTL 10 分钟，
    若不清掉，10 分钟内重跑会静默不入队（202 但无任务），
    T5 会假通过而 T6 必然超时。
    """
    result = subprocess.run(
        ["docker", "exec", REDIS_CONTAINER, "redis-cli", "del",
         f"dedup:{job_type}:{dataset_id}"],
        capture_output=True, check=False,
    )
    deleted = result.stdout.decode("utf-8", "replace").strip()
    print(f"  （清理去重键 dedup:{job_type}:{dataset_id} → deleted={deleted}）")
    return deleted


class Session:
    """极简 cookie 会话（该 API 用 HttpOnly cookie 鉴权，不是 Bearer token）。"""

    def __init__(self, base):
        if not base.startswith(("http://", "https://")):
            raise ValueError(f"base 必须是 http(s) 地址，得到 {base!r}")
        self.base = base.rstrip("/")
        self.session = requests.Session()

    def request(self, method, path, body=None, timeout=60):
        url = self.base + path
        # 只允许 http/https，避免误传 file: 等本地路径。
        if not url.startswith(("http://", "https://")):
            raise ValueError(f"拒绝非 http(s) 地址：{url!r}")
        try:
            res = self.session.request(method, url, json=body, timeout=timeout)
        except requests.RequestException as err:
            return 0, json.dumps({"error": f"请求失败: {err}"})
        return res.status_code, res.text

    def json(self, method, path, body=None, timeout=60):
        status, raw = self.request(method, path, body, timeout)
        try:
            return status, json.loads(raw)
        except json.JSONDecodeError:
            return status, {"_raw": raw}


def as_list(value):
    return value if isinstance(value, list) else []


def count_aligned_steps(chain_of_thought, steps):
    """与 Go 侧 llm.CountAlignedSteps 同语义：命中步骤标题或「第N步」即算对齐。"""
    aligned = 0
    for index, step in enumerate(steps, start=1):
        title = (step.get("title") or "").strip()
        if title and title in chain_of_thought:
            aligned += 1
            continue
        if f"第{index}步" in chain_of_thought:
            aligned += 1
    return aligned


def print_summary():
    print(f"\n{'=' * 60}")
    print(f"通过 {len(PASSED)} · 失败 {len(FAILED)} · 跳过 {len(SKIPPED)}")
    if FAILED:
        print("\n失败项：")
        for name, detail in FAILED:
            print(f"  - {name}\n    {detail}")
    if SKIPPED:
        print("\n跳过项（输入缺失，未伪造通过）：")
        for name, reason in SKIPPED:
            print(f"  - {name}: {reason}")
    print("=" * 60)


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--base", default=DEFAULT_BASE)
    parser.add_argument("--dataset", type=int, default=1,
                        help="数据集 id，需已有问题且配置了 provider")
    parser.add_argument("--wait", type=int, default=900,
                        help="等待真实 LLM 生成的最长秒数")
    args = parser.parse_args()

    if not args.base.startswith(("http://", "https://")):
        print(f"--base 必须是 http(s) 地址，得到 {args.base!r}")
        return 2

    session = Session(args.base)

    print("\n[T1] 登录获取 session cookie")
    status, payload = session.json("POST", "/api/v1/auth/login",
                                   {"email": ADMIN_EMAIL, "password": ADMIN_PASSWORD})
    if status != 200 or not session.session.cookies:
        skip("T1 登录", f"HTTP {status} —— 本地栈未启动或账号不匹配: {payload}")
        print_summary()
        return 1
    record("T1 登录", True)

    dataset_id = args.dataset

    # T11：未认证访问必须 401（证明路由存在且受保护，而不是 404）。
    print("\n[T11] 未认证访问 GET /sft 返回 401")
    anonymous = Session(args.base)
    status, _ = anonymous.json("GET", f"/api/v1/datasets/{dataset_id}/sft")
    record("T11 未认证返回 401（路由存在且受保护）",
           status == 401,
           f"期望 401，实际 HTTP {status}（404 说明打到了 main 镜像或路由未注册）")

    print("\n[T2] GET /api/v1/datasets/{id}/sft 返回 200 + 数组")
    status, body = session.json("GET", f"/api/v1/datasets/{dataset_id}/sft")
    record("T2 GET /sft 返回 200 且为数组",
           status == 200 and isinstance(body, list),
           f"HTTP {status} body={str(body)[:200]}")

    # T3：无 provider 的数据集应返回 409。
    print("\n[T3] POST /sft/generate 无 provider 的数据集返回 409")
    stamp = int(time.time())
    status, created = session.json("POST", "/api/v1/datasets",
                                   {"name": f"l5-no-provider-{stamp}", "rootKeyword": "测试",
                                    "targetSize": 3, "providerId": 0}, timeout=60)
    if status == 201 and isinstance(created, dict) and created.get("id"):
        status, body = session.json("POST", f"/api/v1/datasets/{created['id']}/sft/generate",
                                    {"includeAnswer": True})
        record("T3 无 provider 返回 409", status == 409,
               f"期望 409，实际 HTTP {status} body={str(body)[:200]}")
    else:
        skip("T3 无 provider 返回 409", f"前置创建数据集失败 HTTP {status}")

    # T4：无问题的数据集应返回 409。
    print("\n[T4] POST /sft/generate 无问题的数据集返回 409")
    status, empty = session.json("POST", "/api/v1/datasets",
                                 {"name": f"l5-empty-{stamp}", "rootKeyword": "空数据集",
                                  "targetSize": 1, "providerId": 1}, timeout=60)
    if status == 201 and isinstance(empty, dict) and empty.get("id"):
        status, body = session.json("POST", f"/api/v1/datasets/{empty['id']}/sft/generate",
                                    {"includeAnswer": True})
        record("T4 无问题数据集返回 409", status == 409,
               f"期望 409，实际 HTTP {status} body={str(body)[:200]}")
    else:
        skip("T4 无问题数据集返回 409", f"前置创建数据集失败 HTTP {status}")

    print("\n[T5] POST /sft/generate 返回 202 且真的入队")
    clear_dedup("sft.generate", dataset_id)
    status, body = session.json("POST", f"/api/v1/datasets/{dataset_id}/sft/generate",
                                {"includeAnswer": True})
    message = body.get("message", "") if isinstance(body, dict) else ""
    enqueued_ok = (
        status == 202
        and body.get("datasetId") == dataset_id
        and body.get("stage") == "sft"
        and body.get("state") == "queued"
        # 关键：dedup 命中时 message 是「已在队列中」，说明本次并未入队，
        # 不能算通过，否则 T6 会空等超时。
        and "已入队" in message
    )
    record("T5 POST /sft/generate 返回 202 且真的入队", enqueued_ok,
           f"HTTP {status} message={message!r} body={str(body)[:300]}")
    if not enqueued_ok:
        print_summary()
        return 1

    print(f"\n[T6] 轮询 GET /sft 等待真实 LLM 生成（最长 {args.wait}s）")
    deadline = time.time() + args.wait
    records = []
    while time.time() < deadline:
        status, body = session.json("GET", f"/api/v1/datasets/{dataset_id}/sft")
        if status == 200 and isinstance(body, list) and len(body) > 0:
            records = body
            break
        time.sleep(15)

    if not records:
        skip("T6 真实生成", f"{args.wait}s 内未产出 SFT 样本 —— provider 不可用或生成过慢，"
                            "请检查 model_providers 与 worker 日志")
        print_summary()
        return 1

    record("T6 真实生成出 SFT 样本", True, f"共 {len(records)} 条")
    first = records[0]
    cot = first.get("chainOfThought") or ""

    record("T6b chainOfThought 非空",
           len(cot) > 200,
           f"长度仅 {len(cot)}")

    print("\n[T7] answer 非空（includeAnswer=true）")
    answer = first.get("answer") or ""
    record("T7 answer 非空", len(answer) > 0,
           f"answer 长度 {len(answer)}")

    print("\n[T8] chainOfThought 逐步对齐长链标准步骤")
    steps = as_list(first.get("chainSteps"))
    if not steps:
        # 该方向尚无 L2 生成的标准步骤时，生成器要求模型自行拆解 5~8 步，
        # 此时没有可对齐的基准，如实标记而不是伪造通过。
        skip("T8 逐步对齐标准步骤",
             f"数据集 {dataset_id} 的方向尚未生成长链标准步骤（chain_steps 为空），"
             "需先跑 L2 的 chain-standards.generate")
    else:
        aligned = count_aligned_steps(cot, steps)
        record("T8 chainOfThought 覆盖全部标准步骤",
               aligned == len(steps),
               f"对齐 {aligned}/{len(steps)} 步；steps 标题={[s.get('title') for s in steps]}")

    print("\n[T9] includeAnswer=false 时 answer 必须为空")
    clear_dedup("sft.generate", dataset_id)
    status, body = session.json("POST", f"/api/v1/datasets/{dataset_id}/sft/generate",
                                {"includeAnswer": False})
    message = body.get("message", "") if isinstance(body, dict) else ""
    if status == 202 and "已入队" in message:
        # 等该问题被重新生成为「仅思维链」。
        deadline = time.time() + args.wait
        without_answer = None
        while time.time() < deadline:
            status, body = session.json("GET", f"/api/v1/datasets/{dataset_id}/sft")
            items = as_list(body)
            if items:
                without_answer = items[0]
                if not (without_answer.get("answer") or ""):
                    break
            time.sleep(15)
        if without_answer is None:
            skip("T9 includeAnswer=false", f"{args.wait}s 内未产出样本")
        else:
            record("T9 includeAnswer=false 时 answer 为空",
                   not (without_answer.get("answer") or ""),
                   f"answer 长度 {len(without_answer.get('answer') or '')}（应为 0）")
    else:
        skip("T9 includeAnswer=false", f"入队失败 HTTP {status} message={message!r}")

    print("\n[T10] generation_runs 记录了 sft.generate 阶段")
    # dataset_id 来自 argparse 的 int，这里再显式转一次整数，确保拼进 SQL 的
    # 只可能是数字，不依赖调用方的类型自律。
    run_query = (
        "select stage||'|'||status||'|'||done_units||'/'||total_units "
        "from generation_runs where dataset_id=" + str(int(dataset_id)) +
        " and stage='sft.generate' order by id desc limit 1"
    )
    result = subprocess.run(
        ["docker", "exec", "llm-postgres-1", "psql", "-U", "llm_factory", "-d", "llm_factory",
         "-t", "-A", "-c", run_query],
        capture_output=True, check=False,
    )
    row = result.stdout.decode("utf-8", "replace").strip()
    record("T10 generation_runs 有 sft.generate 记录",
           row.startswith("sft.generate|"),
           f"查询结果={row!r}（空说明断点续跑基础设施未生效）")

    print_summary()
    return 0 if not FAILED else 1


if __name__ == "__main__":
    sys.exit(main())
