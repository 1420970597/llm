#!/usr/bin/env python3
"""L1 方向生成接口测试。

契约：docs/plans/eval-and-cleaning-plan.md 第 3.1 节。

被测目标：本 lane worktree 构建出的 api 实例（默认 http://127.0.0.1:18091），
它与 llm_default 网络里的 postgres / redis / minio 共用同一份数据。

测试项：
  T1  POST /datasets/{id}/directions/generate  入队返回 202 + StageEnqueueResult
  T2  GET  /datasets/{id}/generation-runs      返回运行记录，stage=directions
  T3  POST /datasets/{id}/generation-runs/directions/resume  续跑返回 202
  T4  POST .../generation-runs/unknown-stage/resume  非法阶段返回 400
  T5  GET  /datasets/{id}/directions           返回 level=2 方向，parentId 指向领域
  T6  POST .../directions/generate  在无领域的数据集上返回 409
  T7  GET  /datasets/{id}/directions  方向数达到 n*m（真实 LLM 生成，慢）

真实 LLM 调用需要 provider.APIKey/BaseURL（已在本机 .env 配置）。
若 provider 未配置，T7 会明确标记「输入缺失」而不是伪造通过。
"""

import os
import sys
import time

import requests

BASE = os.environ.get("L1_BASE_URL", "http://127.0.0.1:18091")
ADMIN_EMAIL = os.environ.get("L1_ADMIN_EMAIL", "admin@company.com")
ADMIN_PASSWORD = os.environ.get("L1_ADMIN_PASSWORD", "admin123456")

RESULTS: list[tuple[str, bool, str]] = []


def record(name: str, ok: bool, detail: str) -> None:
    RESULTS.append((name, ok, detail))
    print(f"[{'PASS' if ok else 'FAIL'}] {name}: {detail}")


def as_dict(payload: object) -> dict:
    return payload if isinstance(payload, dict) else {}


def as_list(payload: object) -> list:
    return payload if isinstance(payload, list) else []


def json_body(res: requests.Response) -> object:
    """安全解析响应体。404 等错误路径返回 text/plain，直接 .json() 会抛异常。"""
    try:
        return res.json()
    except ValueError:
        return {"raw": res.text[:200]}


def main() -> int:
    session = requests.Session()
    login = session.post(
        f"{BASE}/api/v1/auth/login",
        json={"email": ADMIN_EMAIL, "password": ADMIN_PASSWORD},
        timeout=30,
    )
    if login.status_code != 200 or not session.cookies.get("llm_session"):
        print(f"无法登录被测 api：status={login.status_code} body={login.text[:200]}")
        return 2

    stamp = int(time.time())

    # 建一个测试数据集，绑定已配置的 provider（id=1）。
    created = session.post(
        f"{BASE}/api/v1/datasets",
        json={
            "name": f"l1-direction-test-{stamp}",
            "rootKeyword": "军事",
            "targetSize": 6,
            "providerId": 1,
            "directionCount": 2,
        },
        timeout=60,
    )
    if created.status_code != 201:
        record("前置：创建测试数据集", False, f"status={created.status_code} body={created.text[:300]}")
        return 1
    dataset_id = as_dict(json_body(created)).get("id")
    record("前置：创建测试数据集", True, f"dataset_id={dataset_id}")

    # T6：没有领域时入队方向生成应返回 409。
    res = session.post(
        f"{BASE}/api/v1/datasets/{dataset_id}/directions/generate",
        json={"directionCount": 2},
        timeout=60,
    )
    record(
        "T6 无领域时返回 409",
        res.status_code == 409,
        f"status={res.status_code} error={as_dict(json_body(res)).get('error')}",
    )

    # T4：非法阶段续跑应返回 400。
    res = session.post(
        f"{BASE}/api/v1/datasets/{dataset_id}/generation-runs/unknown-stage/resume",
        json={},
        timeout=60,
    )
    record(
        "T4 非法阶段返回 400",
        res.status_code == 400,
        f"status={res.status_code} error={as_dict(json_body(res)).get('error')}",
    )

    # 先真实生成领域（legacy 同步路由），为方向生成准备 parent。
    res = session.post(f"{BASE}/api/v1/datasets/{dataset_id}/domains/generate", json={}, timeout=900)
    if res.status_code != 200:
        record("前置：生成领域（真实 LLM）", False, f"status={res.status_code} body={res.text[:300]}")
        return 1
    domains = as_list(as_dict(json_body(res)).get("domains"))
    record("前置：生成领域（真实 LLM）", len(domains) > 0, f"领域数={len(domains)}")

    # T1：入队方向生成。
    res = session.post(
        f"{BASE}/api/v1/datasets/{dataset_id}/directions/generate",
        json={"directionCount": 2},
        timeout=60,
    )
    body = as_dict(json_body(res))
    record(
        "T1 入队返回 202 + StageEnqueueResult",
        res.status_code == 202 and body.get("stage") == "directions",
        f"status={res.status_code} stage={body.get('stage')} state={body.get('state')}",
    )

    # T2：运行记录里应出现 directions 阶段。
    res = session.get(f"{BASE}/api/v1/datasets/{dataset_id}/generation-runs", timeout=60)
    runs = as_list(json_body(res))
    direction_runs = [r for r in runs if isinstance(r, dict) and r.get("stage") == "directions"]
    record(
        "T2 generation-runs 含 directions 记录",
        res.status_code == 200 and len(direction_runs) >= 1,
        f"status={res.status_code} directions_runs={len(direction_runs)} "
        f"total_units={direction_runs[0].get('totalUnits') if direction_runs else 'n/a'}",
    )

    # T3：续跑返回 202（同一阶段）。
    res = session.post(
        f"{BASE}/api/v1/datasets/{dataset_id}/generation-runs/directions/resume",
        json={},
        timeout=60,
    )
    body = as_dict(json_body(res))
    record(
        "T3 续跑返回 202",
        res.status_code == 202 and body.get("stage") == "directions",
        f"status={res.status_code} stage={body.get('stage')} msg={body.get('message')}",
    )

    # T7：等 worker 处理完后，方向应达到 n*m 且 parentId 指向领域。
    expected = len(domains) * 2
    deadline = time.time() + 900
    directions: list = []
    while time.time() < deadline:
        res = session.get(f"{BASE}/api/v1/datasets/{dataset_id}/directions", timeout=60)
        directions = as_list(json_body(res))
        if len(directions) >= expected:
            break
        time.sleep(15)

    if not directions:
        record(
            "T7 方向达到 n*m（真实 LLM）",
            False,
            "输入缺失或未完成：provider 需配置 APIKey/BaseURL，且 worker 需消费 directions.generate",
        )
    else:
        domain_ids = {d.get("id") for d in domains if isinstance(d, dict)}
        parents_ok = all(d.get("parentId") in domain_ids for d in directions if isinstance(d, dict))
        levels_ok = all(d.get("level") == 2 for d in directions if isinstance(d, dict))
        ok = len(directions) >= expected and parents_ok and levels_ok
        record(
            "T7 方向达到 n*m（真实 LLM）",
            ok,
            f"方向数={len(directions)} 期望={expected} level全为2={levels_ok} "
            f"parentId指向领域={parents_ok} 样例={[d.get('name') for d in directions[:4]]}",
        )

    print("\n===== 汇总 =====")
    passed = sum(1 for _, ok, _ in RESULTS if ok)
    for name, ok, _ in RESULTS:
        print(f"  {'PASS' if ok else 'FAIL'}  {name}")
    print(f"通过 {passed}/{len(RESULTS)}")
    return 0 if passed == len(RESULTS) else 1


if __name__ == "__main__":
    sys.exit(main())
