#!/usr/bin/env python3
"""L3 接口测试：方向 → x 个具体问题（x 可控 + 去重 + 难度分层）。

跑法：
    python3 test/test_l3_questions_v2.py

目标服务：本 lane 自己构建的容器（默认 http://127.0.0.1:18083），
**不是** :3210 上 main 分支的镜像 —— 新路由在那里不存在。

启动被测服务：
    cd <worktree> && docker build -t lane-l3:test -f deployments/docker/api.Dockerfile .
    docker run -d --rm --name lane-l3-api --network llm_default -p 18083:8080 \
      -e POSTGRES_HOST=postgres -e POSTGRES_DB=llm_factory \
      -e POSTGRES_USER=llm_factory -e POSTGRES_PASSWORD=llm_factory_dev \
      -e REDIS_HOST=redis -e WORKER_QUEUE_NAME=lane-l3-queue \
      -e APP_ENCRYPTION_KEY="$(docker exec llm-api-1 printenv APP_ENCRYPTION_KEY)" \
      lane-l3:test

真实 LLM 调用会消耗 provider 额度，且单次响应可达 120 秒，因此
端到端生成用例默认跳过；用 --with-llm 显式开启。
"""

import argparse
import json
import sys
import urllib.error
import urllib.request
from typing import Any

BASE = "http://127.0.0.1:18083"
ADMIN_EMAIL = "admin@company.com"
# 本地开发默认账号，与 .env.example 一致；非生产凭据。
ADMIN_PASSWORD = "admin123456"

RESULTS: list[tuple[str, bool, str]] = []


class Session:
    """极简 cookie 会话（认证走 llm_session cookie，不是 Bearer token）。"""

    def __init__(self, base: str) -> None:
        self.base = base
        self.cookie: str | None = None

    def request(self, method: str, path: str, body: Any = None, timeout: int = 60) -> tuple[int, str]:
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
            with urllib.request.urlopen(req, timeout=timeout) as res:
                set_cookie = res.headers.get("Set-Cookie")
                if set_cookie:
                    self.cookie = set_cookie.split(";")[0]
                raw = res.read().decode("utf-8", "replace")
                return res.status, raw
        except urllib.error.HTTPError as err:
            return err.code, err.read().decode("utf-8", "replace")

    def json(self, method: str, path: str, body: Any = None, timeout: int = 60) -> tuple[int, Any]:
        status, raw = self.request(method, path, body, timeout)
        try:
            return status, json.loads(raw)
        except json.JSONDecodeError:
            return status, {"_raw": raw}


def check(name: str, condition: Any, detail: str = "") -> bool:
    RESULTS.append((name, bool(condition), detail))
    mark = "PASS" if condition else "FAIL"
    print(f"[{mark}] {name}" + (f" — {detail}" if detail else ""))
    return bool(condition)


def login(session: Session) -> bool:
    status, payload = session.json("POST", "/api/v1/auth/login",
                                   {"email": ADMIN_EMAIL, "password": ADMIN_PASSWORD})
    return check("T0 管理员登录成功（cookie 认证）",
                 status == 200 and payload.get("user", {}).get("role") == "admin",
                 f"HTTP {status}, role={payload.get('user', {}).get('role')}")


def create_dataset(session: Session, name: str, root_keyword: str) -> int | None:
    """创建一个测试数据集，返回 id。"""
    status, payload = session.json("POST", "/api/v1/datasets", {
        "name": name,
        "rootKeyword": root_keyword,
        "targetSize": 20,
        "providerId": 1,
        "status": "draft",
    })
    if status not in (200, 201):
        return None
    return payload.get("id")


def pick_dataset_with_directions(session: Session) -> int | None:
    """挑一个已有 level=2 方向的数据集用于生成测试。

    用只读的 graph 接口探测，避免像入队接口那样产生副作用。
    """
    status, datasets = session.json("GET", "/api/v1/datasets")
    if status != 200 or not isinstance(datasets, list):
        return None
    for dataset in datasets:
        did = dataset.get("id")
        code, graph = session.json("GET", f"/api/v1/datasets/{did}")
        if code != 200 or not isinstance(graph, dict):
            continue
        domains = graph.get("domains") or []
        if any(d.get("level") == 2 for d in domains):
            return did
    return None


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--base", default=BASE, help="被测服务地址")
    parser.add_argument("--with-llm", action="store_true",
                        help="开启真实 LLM 端到端生成用例（慢，且消耗 provider 额度）")
    parser.add_argument("--dataset-id", type=int, default=None,
                        help="端到端用例使用的数据集 ID（必须已含 level=2 方向）。"
                             "建议先建一个小规模夹具，否则可能触发上百次 LLM 调用。")
    args = parser.parse_args()

    session = Session(args.base)

    print("=" * 72)
    print(f"L3 接口测试 — 被测服务 {args.base}")
    print("=" * 72)

    # ---------- T0 前置 ----------
    if not login(session):
        print("\n登录失败，后续用例无法执行。请确认容器已启动且账号可用。")
        return summarize()

    # ---------- T1 路由存在性 ----------
    status, _ = session.request("GET", "/api/v1/datasets/1/questions/difficulty-stats")
    check("T1a 新路由 difficulty-stats 已注册（非 404）",
          status != 404, f"HTTP {status}")

    status, _ = session.request("POST", "/api/v1/datasets/1/questions/generate", {})
    check("T1b 新路由 questions/generate 已注册（非 404/405）",
          status not in (404, 405), f"HTTP {status}")

    # ---------- T2 difficulty-stats 契约 ----------
    status, payload = session.json("GET", "/api/v1/datasets/1/questions/difficulty-stats")
    ok = status == 200 and isinstance(payload.get("levels"), dict) and "total" in payload
    check("T2a difficulty-stats 返回 {levels:{...}, total:N}", ok,
          f"HTTP {status}, keys={sorted(payload.keys()) if isinstance(payload, dict) else 'n/a'}")
    if ok:
        levels: dict[str, int] = payload["levels"]
        check("T2b levels 恒含 easy/medium/hard 三档",
              all(k in levels for k in ("easy", "medium", "hard")),
              f"levels={levels}")
        check("T2c total 等于三档之和",
              payload["total"] == sum(levels.values()),
              f"total={payload['total']}, sum={sum(levels.values())}")

    # ---------- T3 questions 列表契约 ----------
    status, questions = session.json("GET", "/api/v1/datasets/1/questions")
    ok = status == 200 and isinstance(questions, list)
    check("T3a questions 列表返回数组", ok, f"HTTP {status}, type={type(questions).__name__}")
    if ok and questions:
        first: dict[str, Any] = questions[0]
        required = ["id", "datasetId", "content", "difficulty", "difficultyScore", "dedupeKey"]
        missing = [k for k in required if k not in first]
        check("T3b 列表项含 difficulty/difficultyScore/dedupeKey 等契约字段",
              not missing, f"missing={missing}")
        valid_levels = {"easy", "medium", "hard"}
        bad = [q for q in questions if q.get("difficulty") not in valid_levels]
        check("T3c 所有 difficulty 取值合法（easy/medium/hard）",
              not bad, f"invalid={len(bad)}")

    # ---------- T4 无方向时拒绝入队 ----------
    probe_id = create_dataset(session, "l3-probe-no-directions", "空方向探针")
    if probe_id:
        status, payload = session.json("POST", f"/api/v1/datasets/{probe_id}/questions/generate",
                                       {"questionsPerDirection": 3})
        check("T4 无方向的数据集入队被拒（409 且给出原因）",
              status == 409 and "direction" in json.dumps(payload).lower(),
              f"HTTP {status}, body={json.dumps(payload, ensure_ascii=False)[:120]}")

    # ---------- T5 入队契约（有方向的数据集） ----------
    target_id = pick_dataset_with_directions(session)
    original_per_direction = None
    if target_id:
        _, graph = session.json("GET", f"/api/v1/datasets/{target_id}")
        original_per_direction = (graph.get("dataset") or {}).get("questionsPerDirection")

    if not target_id:
        check("T5 找到含方向的数据集用于入队测试", False,
              "无可用数据集（需要先跑 L1 方向生成）")
    else:
        status, payload = session.json("POST", f"/api/v1/datasets/{target_id}/questions/generate",
                                       {"questionsPerDirection": 2,
                                        "difficultyMix": {"easy": 0.5, "medium": 0.25, "hard": 0.25}})
        check("T5a 入队返回 202 + StageEnqueueResult",
              status == 202 and payload.get("state") == "queued" and "acceptedAt" in payload,
              f"HTTP {status}, body={json.dumps(payload, ensure_ascii=False)[:160]}")

        status, payload = session.json("POST", f"/api/v1/datasets/{target_id}/questions/generate",
                                       {"questionsPerDirection": 2})
        check("T5b 重复入队被 Redis 去重（仍返回 202，提示已在队列）",
              status == 202, f"HTTP {status}")

        status, graph = session.json("GET", f"/api/v1/datasets/{target_id}")
        dataset = graph.get("dataset", {}) if isinstance(graph, dict) else {}
        check("T5c 入队后 dataset.status 变为 questions_queued",
              status == 200 and dataset.get("status") == "questions_queued",
              f"status={dataset.get('status')}")

    # ---------- T6 x 持久化 ----------
    if target_id:
        status, graph = session.json("GET", f"/api/v1/datasets/{target_id}")
        dataset = graph.get("dataset", {}) if isinstance(graph, dict) else {}
        check("T6 入队时 x 持久化到 datasets.questions_per_direction",
              status == 200 and dataset.get("questionsPerDirection") == 2,
              f"questionsPerDirection={dataset.get('questionsPerDirection')}")

    # ---------- T7 空请求体兼容 legacy ----------
    if target_id:
        status, payload = session.json("POST", f"/api/v1/datasets/{target_id}/questions/generate")
        check("T7 空请求体不报 400（兼容 legacy 无 body 入队）",
              status == 202, f"HTTP {status}")

    # ---------- T8 非法难度配比被清洗 ----------
    if target_id:
        status, payload = session.json("POST", f"/api/v1/datasets/{target_id}/questions/generate",
                                       {"questionsPerDirection": 1,
                                        "difficultyMix": {"bogus": 9, "easy": -1}})
        check("T8 非法 difficultyMix 不导致 400（被清洗后回退默认）",
              status == 202, f"HTTP {status}")

    # ---------- T9 真实 LLM 端到端（可选） ----------
    if args.with_llm:
        print("\n--- 真实 LLM 端到端（可能耗时数分钟）---")
        if args.dataset_id:
            run_llm_case(session, args.dataset_id)
        else:
            print("[SKIP] T9 未指定 --dataset-id。端到端用例需一个含方向的**小规模**数据集，")
            print("       否则会对每个方向各发一次 LLM 调用（如 100 个方向 = 100 次）。")
            print("       夹具准备：建数据集后插入 1 个 level=1 领域 + 2 个 level=2 方向。")
    else:
        print("\n[SKIP] T9 真实 LLM 端到端生成 — 未加 --with-llm（消耗额度且慢）")

    # 本用例会改写共享测试数据集的 x，收尾时还原，避免影响其他 lane 的测试。
    if target_id and original_per_direction:
        status, _ = session.json("POST", f"/api/v1/datasets/{target_id}/questions/generate",
                                 {"questionsPerDirection": original_per_direction})
        print(f"\n[INFO] 已还原 dataset {target_id} 的 questions_per_direction={original_per_direction}")

    return summarize()


def run_llm_case(session: Session, dataset_id: int) -> None:
    """真实 LLM 端到端：入队 → 等 worker → 校验问题已落库、去重与难度分层。

    使用调用方指定的数据集（必须已含 level=2 方向），不对每个方向各发一次调用
    以外的额外开销。
    """
    import time

    status, graph = session.json("GET", f"/api/v1/datasets/{dataset_id}")
    if status != 200 or not isinstance(graph, dict):
        check("T9a 读取端到端数据集", False, f"HTTP {status}")
        return
    domains = graph.get("domains") or []
    direction_count = sum(1 for d in domains if d.get("level") == 2)
    if direction_count == 0:
        check("T9a 端到端数据集含 level=2 方向", False,
              f"dataset {dataset_id} 无方向（需先跑 L1 方向生成）—— 依赖缺失，非本 lane 缺陷")
        return
    check("T9a 端到端数据集含 level=2 方向", True,
          f"dataset_id={dataset_id}, directions={direction_count}")

    original = (graph.get("dataset") or {}).get("questionsPerDirection")
    per_direction = 2
    # 显式请求两档均分、hard 为 0，用于验证难度以用户配比为准而非模型自报。
    requested_mix = {"easy": 0.5, "medium": 0.5, "hard": 0.0}
    status, payload = session.json("POST", f"/api/v1/datasets/{dataset_id}/questions/generate",
                                   {"questionsPerDirection": per_direction,
                                    "difficultyMix": requested_mix})
    if status != 202:
        check("T9b 端到端入队", False, f"HTTP {status}, body={json.dumps(payload, ensure_ascii=False)[:160]}")
        return
    check("T9b 端到端入队返回 202", True,
          f"预期生成 {direction_count}×{per_direction}={direction_count * per_direction} 条")

    expected = direction_count * per_direction
    deadline = time.time() + 1800
    questions: list[Any] = []
    while time.time() < deadline:
        time.sleep(15)
        code, questions = session.json("GET", f"/api/v1/datasets/{dataset_id}/questions")
        if code == 200 and isinstance(questions, list) and len(questions) >= expected:
            break

    code, questions = session.json("GET", f"/api/v1/datasets/{dataset_id}/questions")
    check("T9c 端到端生成出问题", code == 200 and isinstance(questions, list) and len(questions) > 0,
          f"count={len(questions) if isinstance(questions, list) else 'n/a'}, expected={expected}")

    if code == 200 and isinstance(questions, list) and questions:
        keys = [q.get("dedupeKey") for q in questions]
        check("T9d 落库问题无重复 dedupeKey", len(keys) == len(set(keys)),
              f"total={len(keys)}, unique={len(set(keys))}")

        code, stats = session.json("GET", f"/api/v1/datasets/{dataset_id}/questions/difficulty-stats")
        levels = stats.get("levels", {}) if code == 200 and isinstance(stats, dict) else {}
        non_zero = [k for k, v in levels.items() if v > 0]
        check("T9e 难度分层生效（至少出现 2 档）", len(non_zero) >= 2, f"levels={levels}")

        # 难度必须以用户配比为准，而不是照抄模型自报的标签。
        # 实测模型经常不遵守提示词的难度要求（请求 hard=0 却返回一半 hard）。
        # 实现按「每个方向各自分配」再汇总，故这里也先算单方向再乘方向数。
        per_direction_plan = expected_plan(requested_mix, per_direction)
        expected_per_level = {k: v * direction_count for k, v in per_direction_plan.items()}
        check("T9g 实际难度分布等于请求的配比（难度以用户配比为准）",
              {k: v for k, v in levels.items() if v} == {k: v for k, v in expected_per_level.items() if v},
              f"actual={levels}, expected={expected_per_level}")

        check("T9f difficultyScore 与 difficulty 一致",
              all(score_of(q.get("difficulty")) == q.get("difficultyScore") for q in questions),
              "easy→1 / medium→2 / hard→3")

    if original:
        session.json("POST", f"/api/v1/datasets/{dataset_id}/questions/generate",
                     {"questionsPerDirection": original})


def score_of(level: Any) -> int:
    return {"easy": 1, "medium": 2, "hard": 3}.get(level, 2)


def expected_plan(mix: dict[str, float], total: int) -> dict[str, int]:
    """复现最大余数法的期望分配，用于端到端断言。

    与 Go 侧 AllocateDifficultyMix 同构：按配比取整后用最大余数补足总数。
    传入的 total 应为**单个方向**的问题数（实现按方向逐个分配）。
    """
    positive = {k: v for k, v in mix.items() if v > 0}
    if not positive or total <= 0:
        return {}
    weight_sum = sum(positive.values())
    normalized = {k: v / weight_sum for k, v in positive.items()}

    exact = {k: v * total for k, v in normalized.items()}
    floored = {k: int(v) for k, v in exact.items()}
    assigned = sum(floored.values())
    order = sorted(exact, key=lambda k: -(exact[k] - floored[k]))
    index = 0
    while assigned < total:
        floored[order[index % len(order)]] += 1
        assigned += 1
        index += 1
    return floored


def summarize() -> int:
    total = len(RESULTS)
    passed = sum(1 for _, ok, _ in RESULTS if ok)
    failed = [(n, d) for n, ok, d in RESULTS if not ok]
    print("\n" + "=" * 72)
    print(f"结果：{passed}/{total} 通过")
    for name, detail in failed:
        print(f"  FAIL {name} — {detail}")
    print("=" * 72)
    return 0 if not failed else 1


if __name__ == "__main__":
    sys.exit(main())
