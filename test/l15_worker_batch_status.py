#!/usr/bin/env python3
"""R2 端到端测试：worker 批处理状态推进（issue #5）。

断言的目标：**状态不得领先于实际记录数**。

原缺陷（#5）：apps/worker/main.go 把 reasoningStore.Insert / rewardStore.Insert
放在逐题循环内，而 Insert 的语义是「整批完成 + 推进数据集状态」。于是第 1 题写完
就把 datasets.status 写死成 reasoning_generated，后面几题无论成败都改不回真实状态。

本脚本用**真实链路**验证修复：
  1. 造一个小规模数据集（1 领域 + 2 方向 + N 题），走真实 API 入队；
  2. 订阅答案生成与评分生成，边跑边**高频采样** datasets.status 与记录数；
  3. 断言在整批跑完之前，status 不得表示「已完成」；
  4. 断言结束后 status 与实际记录数一致（不领先）。

反污染（契约 §6.2）：
  - 唯一名字前缀 l15-r2-<pid>-；
  - 结束（含失败路径）按精确 id 删除自己创建的行，级联清掉子表；
  - 禁止无 WHERE 的批量删除；
  - 保留证据时在输出里写明保留的 id。

跑法：
    python3 test/l15_worker_batch_status.py --base http://127.0.0.1:18102

前置：lane 自己的候选 api + worker 容器已启动（见 scripts/l15-r2-stack.sh），
且 worker 与 api 共享同一个 WORKER_QUEUE_NAME。
"""

import argparse
import json
import os
import subprocess
import sys
import time
import urllib.error
import urllib.request
from typing import Any

BASE = "http://127.0.0.1:18102"
ADMIN_EMAIL = "admin@company.com"
ADMIN_PASSWORD = "admin123456"

POSTGRES_CONTAINER = os.environ.get("POSTGRES_CONTAINER", "llm-postgres-1")
REDIS_CONTAINER = os.environ.get("REDIS_CONTAINER", "llm-redis-1")

# 真实推理模型单次响应可达 120s；整条链路（题目→答案→评分）需要更久。
STAGE_TIMEOUT = int(os.environ.get("STAGE_TIMEOUT", "900"))

RESULTS: list[tuple[str, bool, str]] = []

# 本脚本创建的数据集 id，用于 finally 精确清理。
CREATED_DATASET_IDS: list[int] = []


class Session:
    """极简 cookie 会话（认证走 llm_session cookie，不是 Bearer token）。"""

    def __init__(self, base: str) -> None:
        self.base = base
        self.cookie: str | None = None

    def request(self, method: str, path: str, body: Any = None,
                timeout: int = 60) -> tuple[int, str]:
        url = self.base + path
        data: bytes | None = None
        headers: dict[str, str] = {}
        if body is not None:
            data = json.dumps(body).encode()
            headers["Content-Type"] = "application/json"
        if self.cookie:
            headers["Cookie"] = self.cookie
        req = urllib.request.Request(url, data=data, headers=headers, method=method)
        try:
            with urllib.request.urlopen(req, timeout=timeout) as res:
                set_cookie = res.headers.get("Set-Cookie")
                if set_cookie:
                    self.cookie = set_cookie.split(";")[0]
                return res.status, res.read().decode("utf-8", "replace")
        except urllib.error.HTTPError as err:
            return err.code, err.read().decode("utf-8", "replace")

    def json(self, method: str, path: str, body: Any = None,
             timeout: int = 60) -> tuple[int, Any]:
        status, raw = self.request(method, path, body, timeout)
        try:
            return status, json.loads(raw)
        except json.JSONDecodeError:
            return status, {"_raw": raw}


def check(name: str, condition: Any, detail: str = "") -> bool:
    RESULTS.append((name, bool(condition), detail))
    mark = "PASS" if condition else "FAIL"
    print(f"[{mark}] {name}" + (f" — {detail}" if detail else ""), flush=True)
    return bool(condition)


def psql(sql: str) -> str:
    """执行只读查询，返回去空白的单值。"""
    result = subprocess.run(
        ["docker", "exec", POSTGRES_CONTAINER, "psql", "-U", "llm_factory",
         "-d", "llm_factory", "-tAc", sql],
        capture_output=True, text=True, check=False,
    )
    return result.stdout.strip()


def clear_dedup(job_type: str, dataset_id: int) -> None:
    """清入队去重键，保证本脚本可重复运行。"""
    subprocess.run(
        ["docker", "exec", REDIS_CONTAINER, "redis-cli", "del",
         f"dedup:{job_type}:{dataset_id}"],
        capture_output=True, check=False,
    )


def login(session: Session) -> bool:
    status, payload = session.json("POST", "/api/v1/auth/login",
                                   {"email": ADMIN_EMAIL, "password": ADMIN_PASSWORD})
    return check("T0 管理员登录成功（cookie 认证）",
                 status == 200 and payload.get("user", {}).get("role") == "admin",
                 f"HTTP {status}, role={payload.get('user', {}).get('role')}")


def build_fixture(session: Session, prefix: str, question_count: int) -> int | None:
    """建一个小规模夹具：1 数据集 + 1 领域 + 1 方向 + N 道题。

    直接写库而不是调 LLM 生成领域/方向/题目：本 lane 验证的是**状态推进**，
    不是生成质量。这样把真实 LLM 调用集中在答案与评分两步（#5 的实际发生地），
    既省额度又让断言聚焦。

    全部写入用精确 id / 唯一前缀，不使用无 WHERE 的批量语句。
    """
    name = f"{prefix}dataset"
    status, payload = session.json("POST", "/api/v1/datasets", {
        "name": name,
        "rootKeyword": f"{prefix}军事",
        "targetSize": question_count,
        "providerId": 1,
        "status": "draft",
    })
    if status not in (200, 201):
        print(f"   创建数据集失败: HTTP {status} {json.dumps(payload, ensure_ascii=False)[:200]}")
        return None
    raw_id = payload.get("id") if isinstance(payload, dict) else None
    if not isinstance(raw_id, int):
        print(f"   创建数据集响应缺少 id: {payload}")
        return None
    dataset_id: int = raw_id
    CREATED_DATASET_IDS.append(dataset_id)

    domain_name = f"{prefix}领域"
    psql(
        "INSERT INTO domains (dataset_id, name, canonical_name, level) "
        f"VALUES ({dataset_id}, '{domain_name}', '{prefix}domain', 1);"
    )
    domain_id = psql(
        f"SELECT id FROM domains WHERE dataset_id = {dataset_id} AND level = 1 LIMIT 1;"
    )
    if not domain_id:
        print("   领域写入失败")
        return None

    direction_name = f"{prefix}方向"
    psql(
        "INSERT INTO domains (dataset_id, name, canonical_name, level, parent_id) "
        f"VALUES ({dataset_id}, '{direction_name}', '{prefix}direction', 2, {domain_id});"
    )
    direction_id = psql(
        f"SELECT id FROM domains WHERE dataset_id = {dataset_id} AND level = 2 LIMIT 1;"
    )
    if not direction_id:
        print("   方向写入失败")
        return None

    for index in range(question_count):
        psql(
            "INSERT INTO questions (dataset_id, domain_id, direction_domain_id, content, "
            "canonical_hash, status) "
            f"VALUES ({dataset_id}, {domain_id}, {direction_id}, "
            f"'{prefix}问题{index}：在某海域执行巡逻任务，请给出规划。', "
            f"'{prefix}hash{index}', 'generated');"
        )
    stored = psql(f"SELECT COUNT(*) FROM questions WHERE dataset_id = {dataset_id};")
    if stored != str(question_count):
        print(f"   题目写入数量不符：期望 {question_count}，实际 {stored}")
        return None

    # 数据集推进到「题目已生成」，这是答案生成的合法前置状态。
    psql(f"UPDATE datasets SET status = 'questions_generated' WHERE id = {dataset_id};")
    return dataset_id


def dataset_status(dataset_id: int) -> str:
    return psql(f"SELECT status FROM datasets WHERE id = {dataset_id};")


def record_counts(dataset_id: int) -> tuple[int, int]:
    reasoning = psql(f"SELECT COUNT(*) FROM reasoning_records WHERE dataset_id = {dataset_id};")
    rewards = psql(f"SELECT COUNT(*) FROM reward_records WHERE dataset_id = {dataset_id};")
    return int(reasoning or "0"), int(rewards or "0")


def count_questions(dataset_id: int) -> int:
    return int(psql(f"SELECT COUNT(*) FROM questions WHERE dataset_id = {dataset_id};") or "0")


def enqueue(session: Session, job_type: str, dataset_id: int, path: str) -> tuple[int, str]:
    """清去重键 → 入队 → 返回 (状态码, message)。"""
    clear_dedup(job_type, dataset_id)
    status, payload = session.json("POST", f"/api/v1/datasets/{dataset_id}{path}", {})
    message = payload.get("message", "") if isinstance(payload, dict) else ""
    return status, message


def poll_stage(session: Session, dataset_id: int, stage: str,
               timeout: int) -> tuple[str, list[str]]:
    """轮询**用户可见的 API**，直到该阶段整批跑完。

    为什么不用 generation_runs 判定完成：legacy 的 reasoning.generate /
    rewards.generate 路径**不写 generation_runs**（那条表由 v2 阶段使用）。
    实测确认：答案生成跑完 3 题后 generation_runs 里没有对应行，
    因此以它作为完成信号会一直等到超时（本脚本初版即踩此坑）。

    完成信号改为「记录数 == 题目数」，这正是 issue #5 关心的事实：
    整批记录是否都已落库。

    返回值：(最终 dataset.status, 违例列表)。
    违例 = 记录数还没齐时，status 就已经声称「已完成」——即 #5 的形态。
    """
    deadline = time.time() + timeout
    total_questions = count_questions(dataset_id)
    generated_status = "reasoning_generated" if stage == "reasoning" else "rewards_generated"
    list_path = "/reasoning" if stage == "reasoning" else "/rewards"

    premature: list[str] = []
    last_status = ""
    last_count = 0

    while time.time() < deadline:
        last_status = dataset_status(dataset_id)
        code, records = session.json("GET", f"/api/v1/datasets/{dataset_id}{list_path}")
        if code == 200 and isinstance(records, list):
            last_count = len(records)

        # 核心采样：status 声称完成，但记录数还没齐。
        if last_status == generated_status and last_count < total_questions:
            violation = f"status={last_status} 但 {stage} 记录只有 {last_count}/{total_questions}"
            if not premature or premature[-1] != violation:
                premature.append(violation)

        if last_count >= total_questions:
            return last_status, premature

        # 整批失败时状态会变成 *_failed，记录数可能永远到不了题目数；
        # 此时立即返回，由断言去判定「状态是否领先」。
        if last_status in (f"{stage}_failed",):
            return last_status, premature

        time.sleep(2)

    print(f"   [警告] {stage} 阶段在 {timeout}s 内未跑完（status={last_status}, "
          f"records={last_count}/{total_questions}）", flush=True)
    return last_status, premature


def run_stage_case(session: Session, prefix: str, question_count: int, stage: str) -> None:
    """跑一个阶段（reasoning 或 rewards）的完整断言。"""
    label = "答案生成" if stage == "reasoning" else "评分生成"
    job_type = "reasoning.generate" if stage == "reasoning" else "rewards.generate"
    path = "/reasoning/generate" if stage == "reasoning" else "/rewards/generate"
    generated_status = "reasoning_generated" if stage == "reasoning" else "rewards_generated"
    partial_status = "reasoning_partial" if stage == "reasoning" else "rewards_partial"
    failed_status = "reasoning_failed" if stage == "reasoning" else "rewards_failed"

    print(f"\n--- {label}（{question_count} 题，真实 LLM）---", flush=True)
    dataset_id = build_fixture(session, prefix, question_count)
    if not dataset_id:
        check(f"{stage}: 夹具创建成功", False, "见上方输出")
        return
    check(f"{stage}: 夹具创建成功（dataset_id={dataset_id}）", True)

    # 评分阶段需要先有答案记录。
    if stage == "rewards":
        code, message = enqueue(session, "reasoning.generate", dataset_id, "/reasoning/generate")
        if not check(f"{stage}: 前置答案生成入队成功", code == 202,
                     f"HTTP {code}, message={message}"):
            return
        prereq_status, _ = poll_stage(session, dataset_id, "reasoning", STAGE_TIMEOUT)
        reasoning_count, _ = record_counts(dataset_id)
        if not check(f"{stage}: 前置答案已落库（{reasoning_count} 条）", reasoning_count > 0,
                     f"status={prereq_status}"):
            return

    code, message = enqueue(session, job_type, dataset_id, path)
    if not check(f"{stage}: 入队返回 202 且提示已入队", code == 202,
                 f"HTTP {code}, message={message}"):
        return

    final_status_polled, premature = poll_stage(session, dataset_id, stage, STAGE_TIMEOUT)

    total_questions = count_questions(dataset_id)
    reasoning_count, reward_count = record_counts(dataset_id)
    done_records = reasoning_count if stage == "reasoning" else reward_count

    # 核心断言 1：不得在整批记录落库前就把 status 写成「已完成」。
    check(f"{stage}: 整批跑完前 status 未提前声称完成（issue #5 核心断言）",
          not premature,
          f"违例 {len(premature)} 次" + (f"，首次：{premature[0]}" if premature else ""))

    # 核心断言 2：每条题目都必须有记录（记录数不得落后于题目数）。
    check(f"{stage}: 记录数与题目数一致（{done_records}/{total_questions}）",
          done_records == total_questions,
          f"reasoning={reasoning_count}, rewards={reward_count}, questions={total_questions}")

    # 核心断言 3：结束状态必须与记录数一致 —— status 不得领先于实际记录数。
    final_status = dataset_status(dataset_id)
    if done_records < total_questions:
        check(f"{stage}: 记录不齐时 status 不得是「已生成」（{final_status}）",
              final_status != generated_status,
              f"status={final_status}, records={done_records}/{total_questions}")
    else:
        check(f"{stage}: 记录齐备时 status 为终态之一（{final_status}）",
              final_status in (generated_status, partial_status, failed_status),
              f"status={final_status}")

    # 核心断言 4：不能出现「声称完成但零记录」——契约 §0.5 点名的状态不一致。
    if final_status == generated_status:
        check(f"{stage}: 声称已生成时记录数必须 > 0（覆盖契约 §0.5 疑点）",
              done_records > 0,
              f"status={final_status}, records={done_records}")

    # 附带取证：真实记录的状态分布，用于说明 invalid/failed 是否被正确计入。
    column = "reasoning_records" if stage == "reasoning" else "reward_records"
    distribution = psql(
        f"SELECT status || '=' || COUNT(*) FROM {column} "
        f"WHERE dataset_id = {dataset_id} GROUP BY status ORDER BY status;"
    )
    print(f"   {column} 状态分布: {distribution.replace(chr(10), ', ')}")


def cleanup() -> None:
    """按精确 id 删除本脚本创建的数据集（级联清掉子表）。"""
    if not CREATED_DATASET_IDS:
        return
    print("\n--- 清理 ---", flush=True)
    for dataset_id in CREATED_DATASET_IDS:
        subprocess.run(
            ["docker", "exec", POSTGRES_CONTAINER, "psql", "-U", "llm_factory",
             "-d", "llm_factory", "-c",
             f"DELETE FROM datasets WHERE id = {dataset_id};"],
            capture_output=True, check=False,
        )
        remaining = psql(f"SELECT COUNT(*) FROM datasets WHERE id = {dataset_id};")
        mark = "OK" if remaining == "0" else "残留"
        print(f"   dataset_id={dataset_id} 清理 {mark}", flush=True)


def summarize() -> int:
    passed = sum(1 for _, ok, _ in RESULTS if ok)
    failed = [(name, detail) for name, ok, detail in RESULTS if not ok]
    print("\n" + "=" * 72)
    print(f"结果：{passed}/{len(RESULTS)} 通过")
    if failed:
        print("失败项：")
        for name, detail in failed:
            print(f"  - {name}" + (f" — {detail}" if detail else ""))
    print("=" * 72)
    return 0 if not failed else 1


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--base", default=BASE, help="被测服务地址")
    parser.add_argument("--questions", type=int, default=3,
                        help="夹具题目数（越多越能暴露「第一条写完就推进」，但更慢）")
    parser.add_argument("--keep", action="store_true",
                        help="保留夹具数据用于人工核查（会打印保留的 id）")
    args = parser.parse_args()

    session = Session(args.base)
    prefix = f"l15-r2-{os.getpid()}-"

    print("=" * 72)
    print(f"R2 批处理状态端到端测试 — 被测服务 {args.base}")
    print(f"唯一前缀 {prefix}（契约 §6.2）")
    print("=" * 72)

    try:
        if not login(session):
            print("\n登录失败，后续用例无法执行。请确认容器已启动且账号可用。")
            return summarize()

        run_stage_case(session, prefix, args.questions, "reasoning")
        run_stage_case(session, prefix, args.questions, "rewards")
    finally:
        if args.keep:
            print(f"\n[保留] 未清理，数据集 id：{CREATED_DATASET_IDS}")
        else:
            cleanup()

    return summarize()


if __name__ == "__main__":
    sys.exit(main())
