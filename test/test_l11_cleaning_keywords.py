#!/usr/bin/env python3
"""L11 清洗关键词库 接口测试。

打的是本 lane 自建容器（127.0.0.1:18089），**不是** :3210（那是 main 分支镜像，
本 lane 的 /api/v1/cleaning/* 路由在那里必然 404）。

前置：
    cd /root/lane-wt/L11
    docker build -t lane-l11:test -f deployments/docker/api.Dockerfile .
    docker rm -f lane-l11-api
    docker run -d --rm --name lane-l11-api --network llm_default -p 18089:8080 \
      -e APP_ENCRYPTION_KEY="$(docker exec llm-api-1 printenv APP_ENCRYPTION_KEY)" lane-l11:test

认证：cookie 会话（POST /api/v1/auth/login），不是 Bearer token。
可重入性：本 lane 的接口都是幂等 CRUD（无异步入队），因此不涉及 dedup 去重键。
    关键词测试用带唯一后缀的 pattern（基于 pid），并在结束时清理，避免多轮残留互相干扰。

运行：python3 test/test_l11_cleaning_keywords.py
"""

import json
import os
import sys
import urllib.error
import urllib.request
from http.cookiejar import CookieJar
from typing import Any

BASE = os.environ.get("L11_BASE", "http://127.0.0.1:18089")
EMAIL = "admin@company.com"
PASSWORD = "admin123456"

# 本次运行的唯一标记，保证测试可重入（不撞上一轮的残留数据）。
RUN_TAG = f"l11-{os.getpid()}"

_opener = urllib.request.build_opener(urllib.request.HTTPCookieProcessor(CookieJar()))

results = []


def call(method, path, body=None, expect=None) -> tuple[int, Any]:
    """发请求并返回 (status, parsed_json_or_text)。"""
    data = json.dumps(body).encode() if body is not None else None
    req = urllib.request.Request(BASE + path, data=data, method=method)
    if data is not None:
        req.add_header("Content-Type", "application/json")
    try:
        with _opener.open(req, timeout=30) as resp:
            raw = resp.read().decode()
            status = resp.status
    except urllib.error.HTTPError as e:
        raw = e.read().decode()
        status = e.code
    try:
        parsed = json.loads(raw)
    except json.JSONDecodeError:
        parsed = raw
    if expect is not None and status != expect:
        raise AssertionError(f"{method} {path} 期望 {expect}，实际 {status}：{raw[:300]}")
    return status, parsed


def check(name, condition, detail=""):
    results.append((name, bool(condition), detail))
    mark = "PASS" if condition else "FAIL"
    print(f"[{mark}] {name}" + (f" — {detail}" if detail else ""))
    return bool(condition)


def login():
    call("POST", "/api/v1/auth/login", {"email": EMAIL, "password": PASSWORD}, expect=200)
    print(f"已登录 {EMAIL}")


# ---------------------------------------------------------------- 测试项

def t1_seed_builtin():
    """T1 内置关键词库 seed：幂等，total >= 40。"""
    status, body = call("POST", "/api/v1/cleaning/keywords/seed", {}, expect=200)
    check("T1 seed 返回 inserted/total 字段",
          "inserted" in body and "total" in body, f"body={body}")
    check("T1 内置关键词总数 >= 40", body.get("total", 0) >= 40, f"total={body.get('total')}")

    # 再 seed 一次：inserted 必须为 0（幂等），total 不变。
    _, again = call("POST", "/api/v1/cleaning/keywords/seed", {}, expect=200)
    check("T1 seed 幂等（二次 inserted == 0）", again.get("inserted") == 0,
          f"第二次 inserted={again.get('inserted')}")
    check("T1 seed 二次 total 不变", again.get("total") == body.get("total"),
          f"{body.get('total')} -> {again.get('total')}")


def t2_list_keywords():
    """T2 列表查询 + 分类过滤 + 内置标记。"""
    _, items = call("GET", "/api/v1/cleaning/keywords", expect=200)
    check("T2 返回关键词数组", isinstance(items, list) and len(items) > 0,
          f"count={len(items) if isinstance(items, list) else 'n/a'}")

    refusal = [i for i in items if i.get("category") == "refusal"]
    check("T2 存在 refusal 分类", len(refusal) > 0, f"refusal={len(refusal)}")

    builtin = [i for i in items if i.get("isBuiltin")]
    check("T2 内置关键词带 isBuiltin=true", len(builtin) >= 40, f"builtin={len(builtin)}")

    # 分类过滤
    _, only_refusal = call("GET", "/api/v1/cleaning/keywords?category=refusal", expect=200)
    check("T2 category 过滤生效",
          isinstance(only_refusal, list) and all(i["category"] == "refusal" for i in only_refusal),
          f"count={len(only_refusal) if isinstance(only_refusal, list) else 'n/a'}")

    # 中文 pattern 必须完好（验证 JSON 传输没把 UTF-8 弄坏）
    patterns = {i["pattern"] for i in items}
    check("T2 含中文拒答词「对不起」", "对不起" in patterns)
    check("T2 含中文拒答词「我不能」", "我不能" in patterns)


def t3_create_and_update():
    """T3 自定义关键词增改查。"""
    pattern = f"测试拒答短语-{RUN_TAG}"
    status, created = call("POST", "/api/v1/cleaning/keywords",
                           {"pattern": pattern, "category": "refusal", "severity": "block"},
                           expect=200)
    check("T3 新增返回完整记录", created.get("pattern") == pattern and created.get("id", 0) > 0,
          f"id={created.get('id')}")
    check("T3 用户新增的不是内置", created.get("isBuiltin") is False)
    kid = created["id"]

    # 更新 severity -> warn
    _, updated = call("PUT", "/api/v1/cleaning/keywords",
                      {"id": kid, "pattern": pattern, "category": "refusal",
                       "matchMode": "contains", "severity": "warn", "isActive": True},
                      expect=200)
    check("T3 更新 severity 生效", updated.get("severity") == "warn",
          f"severity={updated.get('severity')}")

    # 重复创建同一 (pattern, category) 不应产生第二条
    _, dup = call("POST", "/api/v1/cleaning/keywords",
                  {"pattern": pattern, "category": "refusal", "severity": "block"}, expect=200)
    check("T3 同 pattern+category 幂等（返回同一 id）", dup.get("id") == kid,
          f"first={kid} second={dup.get('id')}")

    # 删除
    _, deleted = call("DELETE", f"/api/v1/cleaning/keywords/{kid}", expect=200)
    check("T3 删除返回 deleted=true", deleted.get("deleted") is True, f"body={deleted}")

    # 删后确实查不到
    _, after = call("GET", "/api/v1/cleaning/keywords?category=refusal", expect=200)
    check("T3 删除后不再出现在列表",
          all(i["id"] != kid for i in after), f"still_present={any(i['id'] == kid for i in after)}")


def t4_builtin_delete_rejected():
    """T4 内置关键词禁止删除（需求要求 4xx + 明确错误信息）。"""
    _, items = call("GET", "/api/v1/cleaning/keywords?category=refusal", expect=200)
    builtin = [i for i in items if i.get("isBuiltin")]
    assert builtin, "没有内置关键词，无法测试删除保护"
    kid = builtin[0]["id"]

    status, body = call("DELETE", f"/api/v1/cleaning/keywords/{kid}")
    check("T4 删除内置关键词返回 4xx", 400 <= status < 500, f"status={status}")
    check("T4 错误信息明确（含「内置」）",
          isinstance(body, dict) and "内置" in str(body.get("error", "")),
          f"error={body.get('error') if isinstance(body, dict) else body}")

    # 内置词仍在
    _, after = call("GET", "/api/v1/cleaning/keywords?category=refusal", expect=200)
    check("T4 内置关键词未被删除", any(i["id"] == kid for i in after))


def t5_import():
    """T5 批量导入：inserted/skipped 计数正确且幂等。"""
    p1, p2 = f"导入测试甲-{RUN_TAG}", f"导入测试乙-{RUN_TAG}"

    _, first = call("POST", "/api/v1/cleaning/keywords/import",
                    {"patterns": [p1, p2], "category": "refusal", "severity": "block"},
                    expect=200)
    check("T5 首次导入 inserted=2", first.get("inserted") == 2, f"body={first}")

    _, second = call("POST", "/api/v1/cleaning/keywords/import",
                     {"patterns": [p1, p2], "category": "refusal", "severity": "block"},
                     expect=200)
    check("T5 重复导入 inserted=0 / skipped=2",
          second.get("inserted") == 0 and second.get("skipped") == 2, f"body={second}")

    # 批次内重复也要去重
    p3 = f"导入测试丙-{RUN_TAG}"
    _, third = call("POST", "/api/v1/cleaning/keywords/import",
                    {"patterns": [p3, p3], "category": "refusal", "severity": "block"},
                    expect=200)
    check("T5 批次内重复只插入 1 条",
          third.get("inserted") == 1 and third.get("skipped") == 1, f"body={third}")

    # 清理
    _, items = call("GET", "/api/v1/cleaning/keywords?category=refusal", expect=200)
    for item in items:
        if item["pattern"] in (p1, p2, p3):
            call("DELETE", f"/api/v1/cleaning/keywords/{item['id']}")


def t6_import_validation():
    """T6 导入参数校验。"""
    status, body = call("POST", "/api/v1/cleaning/keywords/import",
                        {"patterns": [], "category": "refusal"})
    check("T6 空 patterns 返回 400", status == 400, f"status={status} body={body}")


def t7_rules_crud():
    """T7 清洗规则增改查。"""
    name = f"测试规则-{RUN_TAG}"
    _, created = call("POST", "/api/v1/cleaning/rules",
                      {"name": name, "stageScope": ["answer"], "minHits": 2,
                       "action": "drop", "priority": 10, "isActive": True},
                      expect=200)
    check("T7 新增规则返回记录", created.get("name") == name and created.get("id", 0) > 0,
          f"id={created.get('id')}")
    check("T7 stageScope 往返正确", created.get("stageScope") == ["answer"],
          f"stageScope={created.get('stageScope')}")
    check("T7 minHits 往返正确", created.get("minHits") == 2, f"minHits={created.get('minHits')}")

    # 按 name 更新
    _, updated = call("PUT", "/api/v1/cleaning/rules",
                      {"name": name, "stageScope": ["answer", "reasoning"],
                       "minHits": 3, "action": "flag", "priority": 20, "isActive": True},
                      expect=200)
    check("T7 更新规则生效（minHits=3 / action=flag）",
          updated.get("minHits") == 3 and updated.get("action") == "flag",
          f"minHits={updated.get('minHits')} action={updated.get('action')}")

    _, rules = call("GET", "/api/v1/cleaning/rules", expect=200)
    check("T7 规则列表包含刚写入的规则", any(r["name"] == name for r in rules),
          f"count={len(rules) if isinstance(rules, list) else 'n/a'}")
    check("T7 规则列表按 priority 升序",
          all(rules[i]["priority"] <= rules[i + 1]["priority"] for i in range(len(rules) - 1)))

    # 契约没有 DELETE /rules，但这条测试规则会污染共享库（L12 扫描时会读到），
    # 因此必须停用掉，否则会改变另一条 lane 的扫描行为。
    call("PUT", "/api/v1/cleaning/rules",
         {"name": name, "stageScope": ["answer"], "minHits": 3,
          "action": "flag", "priority": 20, "isActive": False}, expect=200)
    _, after = call("GET", "/api/v1/cleaning/rules", expect=200)
    check("T7 测试规则已停用（不污染 L12 扫描）",
          all(r["isActive"] is False for r in after if r["name"] == name),
          f"still_active={[r['name'] for r in after if r['name'] == name and r['isActive']]}")


def t8_auth_required():
    """T8 未认证请求必须被拒（接口不能裸奔）。"""
    anon = urllib.request.build_opener()  # 无 cookie
    req = urllib.request.Request(BASE + "/api/v1/cleaning/keywords")
    try:
        with anon.open(req, timeout=15) as resp:
            status = resp.status
    except urllib.error.HTTPError as e:
        status = e.code
    check("T8 未认证访问返回 401", status == 401, f"status={status}")


def t9_active_filter():
    """T9 active 过滤与参数校验。"""
    pattern = f"停用测试-{RUN_TAG}"
    _, created = call("POST", "/api/v1/cleaning/keywords",
                      {"pattern": pattern, "category": "refusal", "severity": "warn"},
                      expect=200)
    kid = created["id"]
    call("PUT", "/api/v1/cleaning/keywords",
         {"id": kid, "pattern": pattern, "category": "refusal",
          "matchMode": "contains", "severity": "warn", "isActive": False}, expect=200)

    _, active_only = call("GET", "/api/v1/cleaning/keywords?active=true", expect=200)
    check("T9 active=true 不含已停用词", all(i["id"] != kid for i in active_only))

    _, inactive_only = call("GET", "/api/v1/cleaning/keywords?active=false", expect=200)
    check("T9 active=false 含已停用词", any(i["id"] == kid for i in inactive_only))

    status, _ = call("GET", "/api/v1/cleaning/keywords?active=maybe")
    check("T9 非法 active 值返回 400", status == 400, f"status={status}")

    call("DELETE", f"/api/v1/cleaning/keywords/{kid}")


def main():
    print(f"目标：{BASE}（本 lane 自建容器；:3210 是 main 镜像，测不到新路由）")
    try:
        login()
    except Exception as exc:  # noqa: BLE001
        print(f"登录失败，无法继续：{exc}")
        return 2

    for fn in (t1_seed_builtin, t2_list_keywords, t3_create_and_update,
               t4_builtin_delete_rejected, t5_import, t6_import_validation,
               t7_rules_crud, t8_auth_required, t9_active_filter):
        try:
            fn()
        except Exception as exc:  # noqa: BLE001
            check(f"{fn.__name__} 未抛异常", False, f"{type(exc).__name__}: {exc}")

    passed = sum(1 for _, ok, _ in results if ok)
    failed = len(results) - passed
    print(f"\n通过 {passed} · 失败 {failed} · 输入缺失 0")
    for name, ok, detail in results:
        if not ok:
            print(f"  FAILED: {name} — {detail}")
    return 0 if failed == 0 else 1


if __name__ == "__main__":
    sys.exit(main())
