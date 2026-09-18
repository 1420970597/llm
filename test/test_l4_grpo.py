#!/usr/bin/env python3
"""L4 GRPO 教师模型评判提示词 —— 接口测试。

契约：docs/plans/eval-and-cleaning-plan.md 第 3.4 节。

测试项：
  T1 登录拿 session cookie
  T2 GET  /api/v1/datasets/{id}/grpo 返回 200 + 数组
  T3 PUT  /api/v1/datasets/{id}/reward-levels 回显给定档次
  T4 PUT  /reward-levels 只给一档时返回 400（档次不足需拒绝）
  T5 POST /api/v1/datasets/{id}/grpo/generate 返回 202 且**真的入队**
  T6 入队后轮询 GET /grpo，断言真实生成出 judgePrompt 与 levelRubrics
  T7 断言 judgePrompt 含六个必需段落（角色/框架/判据/场景/输出格式/档次列表）
  T8 断言 levelRubrics 恰好覆盖用户给定的全部档次
  T9 无问题的数据集触发 generate 返回 409（不得空转）

运行目标：**必须打自己的 lane 容器**，不是 main 分支的镜像。
  http://127.0.0.1:3210 上跑的是 main 镜像，没有本 lane 新加的路由，
  GET /grpo 会返回数据集详情（不是数组）、PUT /reward-levels 得 405、
  POST /grpo/generate 得 404 —— 那是打错目标，不是代码缺陷。
  正确做法：用 worktree 构建镜像，起独立容器，端口按 lane 错开。
  本 lane（L4）用 18084。

可重入性（踩过的坑）：
  apps/api/http_util.go 的 enqueueJob 用 `dedup:<jobType>:<datasetID>` 做 SetNX，
  TTL **10 分钟**。若不清掉该键，10 分钟内重跑测试会命中去重、静默不入队
  （仍返回 202，但 message 是「已在队列中」），worker 永远收不到任务，
  T6 干等到超时。因此：
    - T5 发 POST 前先清 dedup 键（见 clear_dedup）
    - T5 断言 message 必须含「已入队」，否则 dedup 命中会被误判为通过

用法：
  python3 test/test_l4_grpo.py --base http://127.0.0.1:18084 --dataset 1
"""

import argparse
import json
import subprocess
import sys
import time
import urllib.error
import urllib.request

DEFAULT_BASE = "http://127.0.0.1:18084"
REDIS_CONTAINER = "llm-redis-1"
ADMIN_EMAIL = "admin@company.com"
ADMIN_PASSWORD = "admin123456"

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


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--base", default=DEFAULT_BASE)
    parser.add_argument("--dataset", type=int, default=0,
                        help="数据集 id；0 表示自动挑选一个有问题的数据集")
    parser.add_argument("--wait", type=int, default=600,
                        help="等待真实 LLM 生成的最长秒数")
    args = parser.parse_args()

    # 只允许 http/https，避免误传 file: 等本地路径。
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

    dataset_id = args.dataset
    if dataset_id == 0:
        status, datasets = session.json("GET", "/api/v1/datasets")
        if status != 200 or not isinstance(datasets, list):
            skip("自动挑选数据集", f"HTTP {status}")
            print_summary()
            return 1
        best = 0
        for item in datasets:
            did = item.get("id")
            st, questions = session.json("GET", f"/api/v1/datasets/{did}/questions")
            if st == 200 and isinstance(questions, list) and len(questions) > 0:
                best = did
                break
        if best == 0:
            skip("自动挑选数据集", "没有任何数据集含问题，无法测试 GRPO 生成")
            print_summary()
            return 1
        dataset_id = best
    print(f"  （使用 dataset id={dataset_id}）")

    print("\n[T2] GET /api/v1/datasets/{id}/grpo 返回 200 + 数组")
    status, body = session.json("GET", f"/api/v1/datasets/{dataset_id}/grpo")
    record("T2 GET /grpo 返回 200 且为数组",
           status == 200 and isinstance(body, list),
           f"HTTP {status} body={str(body)[:200]}")

    print("\n[T3] PUT /reward-levels 回显给定档次")
    levels = ["-1", "0", "1"]
    status, body = session.json("PUT", f"/api/v1/datasets/{dataset_id}/reward-levels",
                                {"levels": levels})
    record("T3 PUT /reward-levels 回显档次",
           status == 200 and body.get("levels") == levels,
           f"HTTP {status} body={str(body)[:200]}")

    print("\n[T4] PUT /reward-levels 单档次时返回 400")
    status, body = session.json("PUT", f"/api/v1/datasets/{dataset_id}/reward-levels",
                                {"levels": ["1"]})
    record("T4 单档次被拒绝（400）",
           status == 400,
           f"期望 400，实际 HTTP {status} body={str(body)[:200]}")

    print("\n[T5] POST /grpo/generate 返回 202 且真的入队")
    clear_dedup("grpo.generate", dataset_id)
    status, body = session.json("POST", f"/api/v1/datasets/{dataset_id}/grpo/generate",
                                {"levels": levels})
    message = body.get("message", "") if isinstance(body, dict) else ""
    enqueued_ok = (
        status == 202
        and body.get("datasetId") == dataset_id
        and body.get("stage") == "grpo"
        and body.get("state") == "queued"
        # 关键：dedup 命中时 message 是「已在队列中」，说明本次并未入队，
        # 不能算通过，否则 T6 会空等超时。
        and "已入队" in message
    )
    record("T5 POST /grpo/generate 返回 202 且真的入队", enqueued_ok,
           f"HTTP {status} message={message!r} body={str(body)[:300]}")
    if not enqueued_ok:
        print_summary()
        return 1

    print(f"\n[T6] 轮询 GET /grpo 等待真实 LLM 生成（最长 {args.wait}s）")
    deadline = time.time() + args.wait
    prompts = []
    while time.time() < deadline:
        status, body = session.json("GET", f"/api/v1/datasets/{dataset_id}/grpo")
        if status == 200 and isinstance(body, list) and len(body) > 0:
            prompts = body
            break
        time.sleep(10)

    if not prompts:
        skip("T6 真实生成", f"{args.wait}s 内未产出提示词 —— provider 不可用或生成过慢，"
                            "请检查 model_providers 与 worker 日志")
        print_summary()
        return 1
    record("T6 真实生成出提示词", True, f"共 {len(prompts)} 条")

    first = prompts[0]
    prompt_text = first.get("judgePrompt") or ""
    record("T6b judgePrompt 非空",
           len(prompt_text) > 200,
           f"长度仅 {len(prompt_text)}")

    print("\n[T7] judgePrompt 含六个必需段落")
    required = {
        "角色设定": "你是资深的长链思考数据评审专家",
        "评审对象": "## 一、评审对象",
        "整体性思考框架": "## 二、整体性思考框架（必须逐步核对）",
        "打分档次与判据": "## 三、打分档次与判据",
        "结合场景的判断要求": "## 四、结合具体场景的判断要求",
        "强制输出格式": "## 五、输出格式（强制）",
    }
    missing = [name for name, needle in required.items() if needle not in prompt_text]
    record("T7 六个必需段落齐全", not missing,
           f"缺少段落: {missing}")

    print("\n[T8] levelRubrics 覆盖全部给定档次")
    rubrics = first.get("levelRubrics") or []
    rubric_levels = [r.get("level") for r in rubrics]
    record("T8 levelRubrics 覆盖全部档次",
           rubric_levels == levels,
           f"期望 {levels}，实际 {rubric_levels}")
    blank_criteria = [r.get("level") for r in rubrics if not (r.get("criteria") or "").strip()]
    record("T8b 每档均有非空判据", not blank_criteria,
           f"判据为空的档次: {blank_criteria}")
    record("T8c 提示词列出全部档次",
           all(f"档次 `{lv}`" in prompt_text for lv in levels),
           f"prompt 中未逐一列出 {levels}")

    print("\n[T9] 无问题的数据集触发 generate 返回 409")
    status, created = session.json("POST", "/api/v1/datasets", {
        "name": "l4-empty-probe", "rootKeyword": "空数据集探针", "targetSize": 1,
    })
    if status in (200, 201) and isinstance(created, dict) and created.get("id"):
        empty_id = created["id"]
        status, body = session.json("POST", f"/api/v1/datasets/{empty_id}/grpo/generate",
                                    {"levels": levels})
        record("T9 无问题数据集返回 409", status == 409,
               f"期望 409，实际 HTTP {status} body={str(body)[:200]}")
    else:
        skip("T9 无问题数据集 409", f"无法创建探针数据集 HTTP {status}")

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
