#!/usr/bin/env python3
"""L2 方向 → 长链思维标准步骤 接口测试。

契约：docs/plans/eval-and-cleaning-plan.md 第 2 节（路由见该文档第 234~237 行）。

被测目标：本 lane worktree 构建出的 api + worker 实例（默认 http://127.0.0.1:18082），
与 llm_default 网络里的 postgres / redis 共用同一份数据，但使用独立队列
WORKER_QUEUE_NAME=lane-l2-test，避免抢主栈任务。

测试项：
  T1  GET  /datasets/{id}/chain-standards              200 + 数组
  T2  POST /datasets/{id}/chain-standards/generate     202 + StageEnqueueResult
  T3  GET  /datasets/{id}/generation-runs              出现 stage=chain-standards.generate 记录
  T4  PUT  /datasets/{id}/chain-standards/{domainId}   保存编辑，currentVersion 自增
  T5  GET  /datasets/{id}/chain-standards/{domainId}/versions  版本历史含 ai 与 user
  T6  PUT  /datasets/{id}/chain-standards/999999       不存在返回 404
  T7  PUT  空 steps                                    返回 400
  T8  POST 无 provider 的数据集                        返回 409
  T9  POST 无匹配方向的数据集                          返回 409
  T10 真实 LLM 生成：标准步骤落到 level=2 方向上，steps 非空

真实 LLM 调用需要 provider.APIKey/BaseURL（已在本机 .env 配置，provider id=1）。
若 provider 未配置，T10 会明确标记「输入缺失」而不是伪造通过。
"""

import os
import subprocess
import sys
import time

import requests

BASE = os.environ.get("L2_BASE_URL", "http://127.0.0.1:18082")
ADMIN_EMAIL = os.environ.get("L2_ADMIN_EMAIL", "admin@company.com")
ADMIN_PASSWORD = os.environ.get("L2_ADMIN_PASSWORD", "admin123456")

RESULTS: list[tuple[str, bool, str]] = []


def record(name: str, ok: bool, detail: str) -> None:
    RESULTS.append((name, ok, detail))
    print(f"[{'PASS' if ok else 'FAIL'}] {name}: {detail}")


def as_dict(payload: object) -> dict:
    return payload if isinstance(payload, dict) else {}


def as_list(payload: object) -> list:
    return payload if isinstance(payload, list) else []


def json_body(res: requests.Response) -> object:
    """安全解析响应体。404/405 等错误路径返回 text/plain，直接 .json() 会抛异常。"""
    try:
        return res.json()
    except ValueError:
        return {"raw": res.text[:200]}


def query_run_row(dataset_id: int) -> tuple | None:
    """直查 generation_runs，验证 L2 入队确实落了库。

    列出路由 GET /generation-runs 归 L1 lane（契约文档第 91 行），本 worktree 未合并 L1，
    因此用查库代替断言他人路由。SQL 走 stdin 让 psql 做 :ds 参数绑定（-c 不做变量替换）。
    """
    sql = (
        "select stage, status, total_units from generation_runs "
        "where dataset_id = :ds and stage = 'chain-standards.generate' "
        "order by id desc limit 1;"
    )
    try:
        out = subprocess.run(
            ["docker", "exec", "-i", "llm-postgres-1", "psql", "-U", "llm_factory", "-d", "llm_factory",
             "-v", f"ds={int(dataset_id)}", "-tA"],
            input=sql, capture_output=True, text=True, timeout=30,
        )
    except (OSError, subprocess.SubprocessError) as err:
        print(f"  （查库失败：{err}）")
        return None
    line = out.stdout.strip()
    if not line:
        return None
    return tuple(line.split("|"))


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

    # 建一个测试数据集，绑定已配置的 provider（id=1），并真实生成领域（作为方向父级）。
    created = session.post(
        f"{BASE}/api/v1/datasets",
        json={
            "name": f"l2-chain-standard-test-{stamp}",
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
    if not isinstance(dataset_id, int):
        record("前置：创建测试数据集", False, f"响应缺少整型 id：{created.text[:300]}")
        return 1
    record("前置：创建测试数据集", True, f"dataset_id={dataset_id}")

    # T8：无 provider 的数据集应返回 409（用 providerId=0 新建）。
    no_provider = session.post(
        f"{BASE}/api/v1/datasets",
        json={"name": f"l2-no-provider-{stamp}", "rootKeyword": "测试", "targetSize": 3, "providerId": 0},
        timeout=60,
    )
    if no_provider.status_code == 201:
        np_id = as_dict(json_body(no_provider)).get("id")
        res = session.post(f"{BASE}/api/v1/datasets/{np_id}/chain-standards/generate", json={}, timeout=60)
        record(
            "T8 无 provider 返回 409",
            res.status_code == 409,
            f"status={res.status_code} error={as_dict(json_body(res)).get('error')}",
        )
    else:
        record("T8 无 provider 返回 409", False, f"前置创建失败 status={no_provider.status_code}")

    # T9：尚无领域时入队应返回 409。
    res = session.post(f"{BASE}/api/v1/datasets/{dataset_id}/chain-standards/generate", json={}, timeout=60)
    record(
        "T9 无方向时返回 409",
        res.status_code == 409,
        f"status={res.status_code} error={as_dict(json_body(res)).get('error')}",
    )

    # 前置：真实生成领域（legacy 同步路由），作为方向父级。
    res = session.post(f"{BASE}/api/v1/datasets/{dataset_id}/domains/generate", json={}, timeout=900)
    if res.status_code != 200:
        record("前置：生成领域（真实 LLM）", False, f"status={res.status_code} body={res.text[:300]}")
        return 1
    domains = as_list(as_dict(json_body(res)).get("domains"))
    record("前置：生成领域（真实 LLM）", len(domains) > 0, f"领域数={len(domains)}")

    # 本 lane 的处理目标按契约是 level=2 方向。但 L1（方向生成）是独立 lane、
    # 尚未合并到本 worktree，因此 /directions/generate 在此构建里不存在。
    # L2 的 worker 对此有明确设计：chainStandardTargets() 优先取 level=2，
    # 若无方向则回退 level=1 领域，使本 lane 在 L1 合并前依然可用。
    # 因此这里直接测回退路径（真实行为），并如实标注目标层级。
    target_level = 1
    domain_id = domains[0].get("id")
    directions_res = session.get(f"{BASE}/api/v1/datasets/{dataset_id}/directions", timeout=60)
    if directions_res.status_code == 200 and as_list(json_body(directions_res)):
        target_level = 2
        domain_id = as_list(json_body(directions_res))[0].get("id")
    record(
        "前置：确定处理目标",
        domain_id is not None,
        f"层级={target_level}（1=领域回退路径，2=方向）domain_id={domain_id} "
        f"directions_status={directions_res.status_code}",
    )

    # T1：列表接口（此时应为空数组，尚未生成本 lane 数据）。
    res = session.get(f"{BASE}/api/v1/datasets/{dataset_id}/chain-standards", timeout=60)
    record(
        "T1 GET chain-standards 返回数组",
        res.status_code == 200 and isinstance(json_body(res), list),
        f"status={res.status_code} 条数={len(as_list(json_body(res)))}",
    )

    # T2：入队生成（只针对第一个方向，验证 domainIds 过滤真实生效）。
    res = session.post(
        f"{BASE}/api/v1/datasets/{dataset_id}/chain-standards/generate",
        json={"domainIds": [domain_id]},
        timeout=60,
    )
    body = as_dict(json_body(res))
    record(
        "T2 入队返回 202 + StageEnqueueResult",
        res.status_code == 202 and body.get("stage") == "chain-standards",
        f"status={res.status_code} stage={body.get('stage')} state={body.get('state')}",
    )

    # T3：入队应写入 generation_runs 记录（stage=chain-standards.generate）。
    # 注意：列出接口 GET /generation-runs 属于 L1 lane（契约文档第 91 行），
    # 本 worktree 尚未合并 L1，所以这里直接查库验证 L2 自己的写入结果，
    # 而不是断言 L1 的路由。
    row = query_run_row(dataset_id)
    record(
        "T3 入队写入 generation_runs 记录",
        row is not None and row[0] == "chain-standards.generate",
        f"stage={row[0] if row else 'n/a'} status={row[1] if row else 'n/a'} "
        f"total_units={row[2] if row else 'n/a'}（列出路由属 L1，故直接查库）",
    )

    # T10：等 worker 用真实 LLM 生成完，步骤应落到该方向上。
    deadline = time.time() + 900
    standards: list = []
    while time.time() < deadline:
        res = session.get(f"{BASE}/api/v1/datasets/{dataset_id}/chain-standards", timeout=60)
        standards = as_list(json_body(res))
        if standards:
            break
        time.sleep(15)

    if not standards:
        record(
            "T10 标准步骤落到目标（真实 LLM）",
            False,
            "输入缺失或未完成：provider 需配置 APIKey/BaseURL，且 worker 需消费 chain-standards.generate",
        )
    else:
        first = as_dict(standards[0])
        steps = as_list(first.get("steps"))
        ok = (
            len(standards) == 1
            and first.get("domainId") == domain_id
            and first.get("currentVersion") == 1
            and first.get("status") == "generated"
            and len(steps) > 0
            and all(isinstance(s, dict) and s.get("title") for s in steps)
        )
        record(
            "T10 标准步骤落到目标（真实 LLM）",
            ok,
            f"条数={len(standards)}（domainIds 过滤后应为 1）domainId={first.get('domainId')} "
            f"version={first.get('currentVersion')} status={first.get('status')} 步骤数={len(steps)} "
            f"样例标题={[s.get('title') for s in steps[:3] if isinstance(s, dict)]}",
        )

    # T4：编辑保存，版本应自增到 2，status 变 edited。
    edited_steps = [
        {"index": 1, "title": "接口测试改写步骤一", "description": "用户编辑", "checkpoint": "检查点一"},
        {"index": 2, "title": "接口测试改写步骤二", "description": "用户编辑", "checkpoint": "检查点二"},
    ]
    res = session.put(
        f"{BASE}/api/v1/datasets/{dataset_id}/chain-standards/{domain_id}",
        json={"steps": edited_steps, "changeNote": "接口测试编辑"},
        timeout=60,
    )
    body = as_dict(json_body(res))
    steps = as_list(body.get("steps"))
    record(
        "T4 PUT 编辑后 currentVersion 自增",
        res.status_code == 200
        and body.get("currentVersion") == 2
        and body.get("status") == "edited"
        and len(steps) == 2
        and steps[0].get("title") == "接口测试改写步骤一",
        f"status={res.status_code} version={body.get('currentVersion')} "
        f"state={body.get('status')} 步骤数={len(steps)} 首步标题={steps[0].get('title') if steps else 'n/a'}",
    )

    # T5：版本历史应含 ai（v1）与 user（v2）两条，倒序。
    res = session.get(
        f"{BASE}/api/v1/datasets/{dataset_id}/chain-standards/{domain_id}/versions", timeout=60
    )
    versions = as_list(json_body(res))
    sources = [v.get("source") for v in versions if isinstance(v, dict)]
    record(
        "T5 版本历史含 ai 与 user",
        res.status_code == 200 and len(versions) >= 2 and sources[:2] == ["user", "ai"],
        f"status={res.status_code} 版本数={len(versions)} sources={sources} "
        f"versions={[v.get('version') for v in versions if isinstance(v, dict)]}",
    )

    # T6：不存在的方向返回 404。
    res = session.put(
        f"{BASE}/api/v1/datasets/{dataset_id}/chain-standards/999999",
        json={"steps": edited_steps, "changeNote": "x"},
        timeout=60,
    )
    record(
        "T6 不存在的方向返回 404",
        res.status_code == 404,
        f"status={res.status_code} error={as_dict(json_body(res)).get('error')}",
    )

    # T7：空 steps 返回 400。
    res = session.put(
        f"{BASE}/api/v1/datasets/{dataset_id}/chain-standards/{domain_id}",
        json={"steps": [], "changeNote": "空"},
        timeout=60,
    )
    record(
        "T7 空 steps 返回 400",
        res.status_code == 400,
        f"status={res.status_code} error={as_dict(json_body(res)).get('error')}",
    )

    print("\n===== 汇总 =====")
    passed = sum(1 for _, ok, _ in RESULTS if ok)
    for name, ok, _ in RESULTS:
        print(f"  {'PASS' if ok else 'FAIL'}  {name}")
    print(f"通过 {passed}/{len(RESULTS)}")
    return 0 if passed == len(RESULTS) else 1


if __name__ == "__main__":
    sys.exit(main())
