#!/usr/bin/env python3
"""L8 数据集评估维度（内置 ≥50 个长链思考维度 + 用户自定义）接口测试。

契约：docs/plans/eval-and-cleaning-plan.md 第 3.8 节（评估维度路由）、
第 2 节 0011（eval_dimensions 表）。

被测目标：本 lane worktree 构建出的 api 实例（默认 http://127.0.0.1:18088），
与 llm_default 网络里的 postgres 共用同一份数据。本 lane 纯 CRUD、无 LLM 调用、
无 worker，因此不需要私有队列。

测试项：
  T1  POST /eval/dimensions/seed                  幂等 seed，total >= 56
  T2  GET  /eval/dimensions?builtin=true          内置维度数 >= 50，rubric 非空
  T3  GET  /eval/dimensions/categories            分类去重且含 7 个必需分类
  T4  POST /eval/dimensions                       新增用户自定义维度
  T5  POST /eval/dimensions（同 key 再发）        复用同一行而非重复插入
  T6  PUT  /eval/dimensions                       按 ID 更新
  T7  GET  /eval/dimensions?category=robustness   内置与自定义维度共存于同分类
  T8  GET  /eval/dimensions?builtin=false         只返回自定义维度
  T9  DELETE /eval/dimensions/{id}（内置）        返回 409 且记录仍在
  T10 DELETE /eval/dimensions/{id}（自定义）      返回 200 {deleted:true}
  T11 DELETE 不存在的 id                          返回 404
  T12 POST 非法输入（空 key / 分值区间倒置 / 权重为零）  返回 400，不落库

本测试不调用 LLM，因此不存在「输入缺失」情形。
"""

import os
import sys
import time

import requests

BASE = os.environ.get("L8_BASE_URL", "http://127.0.0.1:18088")
ADMIN_EMAIL = os.environ.get("L8_ADMIN_EMAIL", "admin@company.com")
ADMIN_PASSWORD = os.environ.get("L8_ADMIN_PASSWORD", "admin123456")

RESULTS: list[tuple[str, bool, str]] = []

# 必需分类（契约第 3.8 节 / 任务书要求覆盖的 7 类长链思考维度）。
REQUIRED_CATEGORIES = [
    "long_chain",
    "faithfulness",
    "instruction",
    "domain_fit",
    "answer_quality",
    "robustness",
    "efficiency",
]


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
    # 自定义维度的 key 必须全局唯一（表上有 UNIQUE 约束），带时间戳保证可重入。
    custom_key = f"l8test_custom_{stamp}"

    # T1：seed 幂等。共享库通常已 seed 过，因此 inserted 可能为 0，
    # 不变量是 total >= 56 且二次 seed 不新增。
    res = session.post(f"{BASE}/api/v1/eval/dimensions/seed", timeout=60)
    body = as_dict(json_body(res))
    total = body.get("total")
    ok_t1 = res.status_code == 200 and isinstance(total, int) and total >= 56
    record("T1 seed 返回 total >= 56", ok_t1, f"status={res.status_code} inserted={body.get('inserted')} total={total}")

    res2 = session.post(f"{BASE}/api/v1/eval/dimensions/seed", timeout=60)
    body2 = as_dict(json_body(res2))
    record(
        "T1b seed 幂等（二次不新增）",
        res2.status_code == 200 and body2.get("inserted") == 0 and body2.get("total") == total,
        f"status={res2.status_code} inserted={body2.get('inserted')} total={body2.get('total')}",
    )

    # T2：内置维度数 >= 50，且每个都带可操作 rubric。
    res = session.get(f"{BASE}/api/v1/eval/dimensions", params={"builtin": "true"}, timeout=60)
    builtins = as_list(json_body(res))
    all_builtin = all(as_dict(d).get("isBuiltin") for d in builtins)
    empty_rubric = [as_dict(d).get("key") for d in builtins if not str(as_dict(d).get("rubric") or "").strip()]
    record(
        "T2 内置维度 >= 50 且 rubric 非空",
        res.status_code == 200 and len(builtins) >= 50 and all_builtin and not empty_rubric,
        f"status={res.status_code} 内置数={len(builtins)} 全部isBuiltin={all_builtin} 空rubric={empty_rubric[:5]}",
    )

    # 内置维度必须真的聚焦长链思考：抽样检查 lc_ 前缀维度存在。
    lc_keys = [as_dict(d).get("key") for d in builtins if str(as_dict(d).get("key", "")).startswith("lc_")]
    record(
        "T2b 存在长链思考维度（lc_* 前缀）",
        len(lc_keys) >= 8,
        f"lc_* 维度数={len(lc_keys)} 样例={lc_keys[:5]}",
    )

    # T3：分类列表去重且覆盖 7 个必需分类。
    res = session.get(f"{BASE}/api/v1/eval/dimensions/categories", timeout=60)
    categories = as_list(as_dict(json_body(res)).get("categories"))
    missing = [c for c in REQUIRED_CATEGORIES if c not in categories]
    record(
        "T3 分类覆盖 7 个必需分组",
        res.status_code == 200 and not missing and len(categories) == len(set(categories)),
        f"status={res.status_code} 分类数={len(categories)} 缺失={missing}",
    )

    # T4：新增用户自定义维度。
    res = session.post(
        f"{BASE}/api/v1/eval/dimensions",
        json={
            "key": custom_key,
            "name": "接口测试自定义维度",
            "category": "robustness",
            "description": "由接口测试创建，用于验证用户可扩展维度",
            "rubric": "无对应内容记 1 分；部分满足记 3 分；完整满足且给出依据记 5 分。",
            "scaleMin": 1,
            "scaleMax": 5,
            "isActive": True,
            "weight": 1.25,
        },
        timeout=60,
    )
    created = as_dict(json_body(res))
    custom_id = created.get("id")
    record(
        "T4 新增自定义维度",
        res.status_code == 200
        and isinstance(custom_id, int)
        and custom_id > 0
        and created.get("isBuiltin") is False
        and created.get("key") == custom_key,
        f"status={res.status_code} id={custom_id} key={created.get('key')} isBuiltin={created.get('isBuiltin')}",
    )
    if not isinstance(custom_id, int):
        print("无法继续：自定义维度未创建成功")
        return 1

    # T5：同 key 再 POST 是更新而非重复插入（表上 key 唯一）。
    res = session.post(
        f"{BASE}/api/v1/eval/dimensions",
        json={
            "key": custom_key,
            "name": "接口测试自定义维度（修订）",
            "category": "robustness",
            "rubric": "修订后的分档判据：无 1 分，部分 3 分，完整且含依据 5 分。",
            "scaleMin": 1,
            "scaleMax": 5,
            "isActive": True,
            "weight": 1.5,
        },
        timeout=60,
    )
    updated = as_dict(json_body(res))
    record(
        "T5 同 key 重复 POST 复用同一行",
        res.status_code == 200
        and updated.get("id") == custom_id
        and updated.get("name") == "接口测试自定义维度（修订）"
        and updated.get("weight") == 1.5,
        f"status={res.status_code} id={updated.get('id')}（原 {custom_id}）name={updated.get('name')} weight={updated.get('weight')}",
    )

    # T6：按 ID 更新。
    res = session.put(
        f"{BASE}/api/v1/eval/dimensions",
        json={
            "id": custom_id,
            "key": custom_key,
            "name": "接口测试自定义维度（二次修订）",
            "category": "robustness",
            "rubric": "二次修订分档判据：无 1 分，部分 3 分，完整 5 分。",
            "scaleMin": 1,
            "scaleMax": 5,
            "isActive": True,
            "weight": 2.0,
        },
        timeout=60,
    )
    by_id = as_dict(json_body(res))
    record(
        "T6 按 ID 更新维度",
        res.status_code == 200 and by_id.get("id") == custom_id and by_id.get("weight") == 2.0,
        f"status={res.status_code} id={by_id.get('id')} weight={by_id.get('weight')}",
    )

    # T7：同分类下内置与自定义维度共存。
    res = session.get(f"{BASE}/api/v1/eval/dimensions", params={"category": "robustness"}, timeout=60)
    robustness = as_list(json_body(res))
    wrong_category = [as_dict(d).get("key") for d in robustness if as_dict(d).get("category") != "robustness"]
    saw_builtin = any(as_dict(d).get("isBuiltin") for d in robustness)
    saw_custom = any(as_dict(d).get("key") == custom_key for d in robustness)
    record(
        "T7 分类内内置与自定义维度共存",
        res.status_code == 200 and saw_builtin and saw_custom and not wrong_category,
        f"status={res.status_code} robustness 维度数={len(robustness)} 内置={saw_builtin} 自定义={saw_custom} 越界={wrong_category[:3]}",
    )

    # T8：builtin=false 只返回自定义维度。
    res = session.get(f"{BASE}/api/v1/eval/dimensions", params={"builtin": "false"}, timeout=60)
    customs = as_list(json_body(res))
    mixed = [as_dict(d).get("key") for d in customs if as_dict(d).get("isBuiltin")]
    record(
        "T8 builtin=false 只返回自定义维度",
        res.status_code == 200 and not mixed and any(as_dict(d).get("key") == custom_key for d in customs),
        f"status={res.status_code} 自定义数={len(customs)} 混入内置={mixed[:3]}",
    )

    # T9：删除内置维度必须被拒绝，且记录仍在。
    builtin_id = None
    for dim in builtins:
        item = as_dict(dim)
        if item.get("key") == "lc_step_sufficiency":
            builtin_id = item.get("id")
            break
    if isinstance(builtin_id, int):
        res = session.delete(f"{BASE}/api/v1/eval/dimensions/{builtin_id}", timeout=60)
        err = as_dict(json_body(res)).get("error", "")
        still_there = session.get(f"{BASE}/api/v1/eval/dimensions", params={"builtin": "true"}, timeout=60)
        still_ids = [as_dict(d).get("id") for d in as_list(json_body(still_there))]
        record(
            "T9 删除内置维度被拒绝（409）且记录仍在",
            res.status_code == 409 and "内置" in str(err) and builtin_id in still_ids,
            f"status={res.status_code} error={err} 记录仍在={builtin_id in still_ids}",
        )
    else:
        record("T9 删除内置维度被拒绝（409）且记录仍在", False, "未能在列表中定位内置维度 lc_step_sufficiency")

    # T10：删除自定义维度成功。
    res = session.delete(f"{BASE}/api/v1/eval/dimensions/{custom_id}", timeout=60)
    record(
        "T10 删除自定义维度返回 deleted=true",
        res.status_code == 200 and as_dict(json_body(res)).get("deleted") is True,
        f"status={res.status_code} body={json_body(res)}",
    )

    # T11：重复删除返回 404。
    res = session.delete(f"{BASE}/api/v1/eval/dimensions/{custom_id}", timeout=60)
    record(
        "T11 重复删除返回 404",
        res.status_code == 404,
        f"status={res.status_code} error={as_dict(json_body(res)).get('error')}",
    )

    # T12：非法输入被拒绝且不落库。
    bad_cases = [
        ("空 key", {"key": "  ", "name": "N", "category": "long_chain", "scaleMin": 1, "scaleMax": 5, "weight": 1}),
        (
            "分值区间倒置",
            {"key": f"{custom_key}_bad_scale", "name": "N", "category": "long_chain", "scaleMin": 5, "scaleMax": 1, "weight": 1},
        ),
        (
            "权重为零",
            {"key": f"{custom_key}_bad_weight", "name": "N", "category": "long_chain", "scaleMin": 1, "scaleMax": 5, "weight": 0},
        ),
    ]
    bad_results = []
    for label, payload in bad_cases:
        res = session.post(f"{BASE}/api/v1/eval/dimensions", json=payload, timeout=60)
        bad_results.append((label, res.status_code, as_dict(json_body(res)).get("error")))
    all_rejected = all(status == 400 for _, status, _ in bad_results)
    # 确认非法输入没有留下记录。
    after = session.get(f"{BASE}/api/v1/eval/dimensions", params={"builtin": "false"}, timeout=60)
    leftovers = [
        as_dict(d).get("key")
        for d in as_list(json_body(after))
        if str(as_dict(d).get("key", "")).startswith(f"{custom_key}_bad")
    ]
    record(
        "T12 非法输入返回 400 且不落库",
        all_rejected and not leftovers,
        f"结果={bad_results} 残留={leftovers}",
    )

    print("\n===== 汇总 =====")
    passed = sum(1 for _, ok, _ in RESULTS if ok)
    for name, ok, _ in RESULTS:
        print(f"  {'PASS' if ok else 'FAIL'}  {name}")
    print(f"通过 {passed}/{len(RESULTS)}")
    return 0 if passed == len(RESULTS) else 1


if __name__ == "__main__":
    sys.exit(main())
