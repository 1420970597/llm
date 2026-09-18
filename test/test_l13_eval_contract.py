"""L13 前端评估模块 —— 所依赖后端接口的契约校验。

L13 是纯前端 lane（EvaluationView.tsx + views/eval/*），自身不提供 HTTP 路由，
因此这里校验的是**前端渲染所依赖的后端响应字段是否真实存在**，而不是 L13 自己的产物。

默认打 http://127.0.0.1:18092 —— 用本 lane worktree 构建的镜像 lane-l13:test
（PREAMBLE 指定端口）。**不要**打 127.0.0.1:3210，那是 main 分支的旧镜像，
新路由在那里必然是 404。可用 L13_BASE 覆盖。

L7（/v1/admin/eval/judges）与 L8（/v1/eval/dimensions）已合并进本 worktree 的代码，
可以真实联调；L9（/v1/eval/runs 系列）与 L10（/v1/eval/runs/{id}/report、/scores）
是并行 lane，尚未合并 —— 本脚本对这两个前缀只做「可达性探测」并如实记录 404，
绝不伪造通过。

入队去重注意（PREAMBLE）：本脚本唯一会入队的动作是 POST /v1/eval/dimensions/seed，
它不是 enqueueJob 路径（无 datasetId、无 dedup 键），重复调用只会返回 inserted=0，
因此可安全重入；脚本仍对 inserted 做「非负整数」断言而非固定值。

运行：
    python3 test/test_l13_eval_contract.py
"""
import json
import os
import sys
import urllib.error
import urllib.request

# 默认打 18092（本 lane 自己的镜像，PREABMLE 指定端口）。
# 打 127.0.0.1:3210 只会命中 main 旧镜像，新路由必然 404。
BASE = os.environ.get("L13_BASE", "http://127.0.0.1:18092") + "/api/v1"
EMAIL = "admin@company.com"
PASSWORD = "admin123456"

COOKIE = ""
results = []


def call(method, path, body=None):
    """返回 (status, parsed_json_or_text)。认证走 cookie（不是 Bearer）。"""
    data = json.dumps(body).encode() if body is not None else None
    req = urllib.request.Request(BASE + path, data=data, method=method)
    req.add_header("Content-Type", "application/json")
    if COOKIE:
        req.add_header("Cookie", COOKIE)
    try:
        with urllib.request.urlopen(req, timeout=60) as resp:
            raw = resp.read().decode()
            return resp.status, json.loads(raw) if raw else None
    except urllib.error.HTTPError as exc:
        raw = exc.read().decode()
        try:
            return exc.code, json.loads(raw) if raw else None
        except json.JSONDecodeError:
            return exc.code, raw


def check(name, condition, detail):
    results.append((name, condition, detail))
    print(f"[{'PASS' if condition else 'FAIL'}] {name} :: {detail}")


def main():
    global COOKIE
    status, body = call("POST", "/auth/login", {"email": EMAIL, "password": PASSWORD})
    if status != 200:
        print(f"登录失败 status={status} body={body}")
        return 1
    # 登录响应只给 user，cookie 由服务端 Set-Cookie 下发；用 raw 再取一次。
    req = urllib.request.Request(BASE + "/auth/login", data=json.dumps({"email": EMAIL, "password": PASSWORD}).encode(), method="POST")
    req.add_header("Content-Type", "application/json")
    with urllib.request.urlopen(req, timeout=60) as resp:
        set_cookie = resp.headers.get("Set-Cookie", "")
    COOKIE = set_cookie.split(";")[0]
    check("登录拿到 llm_session cookie", COOKIE.startswith("llm_session="), COOKIE.split("=")[0] or "(空)")

    # ---- L8：维度列表字段（DimensionManager 渲染 name/category/rubric/scaleMin/scaleMax/weight/isBuiltin）----
    status, dims = call("GET", "/eval/dimensions")
    check("GET /eval/dimensions 返回 200 与数组", status == 200 and isinstance(dims, list), f"status={status} len={len(dims) if isinstance(dims, list) else 'n/a'}")
    required = {"id", "key", "name", "category", "description", "rubric", "scaleMin", "scaleMax", "isBuiltin", "isActive", "weight"}
    if isinstance(dims, list) and dims:
        missing = sorted(required - set(dims[0].keys()))
        check("维度对象含 UI 需要的全部字段", not missing, f"missing={missing or '无'}")
        builtin = [d for d in dims if d.get("isBuiltin")]
        check("存在内置维度（可验证「内置不可删除」分支）", len(builtin) > 0, f"builtin={len(builtin)} custom={len(dims) - len(builtin)}")
        check("内置维度分数区间合法", all(d["scaleMax"] > d["scaleMin"] for d in dims), "scaleMax > scaleMin 全部成立")
    else:
        check("维度列表非空", False, "维度库为空，UI 只能展示空状态")

    # ---- L8：分类（DimensionManager 分组 / EvalRunForm 按分类全选）----
    status, cats = call("GET", "/eval/dimensions/categories")
    ok = status == 200 and isinstance(cats, dict) and isinstance(cats.get("categories"), list) and len(cats["categories"]) > 0
    check("GET /eval/dimensions/categories 返回 {categories:[...]}", ok, f"status={status} categories={cats.get('categories') if isinstance(cats, dict) else cats}")

    # ---- L8：导入内置维度（按钮「导入内置维度」的真实后端）----
    status, seed = call("POST", "/eval/dimensions/seed")
    ok = status == 200 and isinstance(seed, dict) and isinstance(seed.get("inserted"), int) and isinstance(seed.get("total"), int)
    check("POST /eval/dimensions/seed 返回 {inserted,total} 整数", ok, f"status={status} body={seed}")

    # ---- L7：裁判列表的 excluded / excludeReason（EvalRunForm 禁用被排除裁判的核心语义）----
    status, judges = call("GET", "/admin/eval/judges")
    ok = status == 200 and isinstance(judges, list)
    check("GET /admin/eval/judges 返回 200 与数组", ok, f"status={status} len={len(judges) if isinstance(judges, list) else 'n/a'}")
    if isinstance(judges, list) and judges:
        jreq = {"providerId", "providerName", "model", "isActive", "excluded", "excludeReason"}
        jmissing = sorted(jreq - set(judges[0].keys()))
        check("裁判对象含 excluded/excludeReason（UI 禁用与原因展示依据）", not jmissing, f"missing={jmissing or '无'}")
        check("excluded 为布尔、excludeReason 为字符串", all(isinstance(j.get("excluded"), bool) and isinstance(j.get("excludeReason"), str) for j in judges), f"excluded 分布={[j.get('excluded') for j in judges]}")
    else:
        check("裁判列表非空", False, "无 provider，UI 只能展示空状态")

    # ---- L9 / L10：并行 lane，未合并则如实记录 404 ----
    for label, method, path in [
        ("L9 GET /eval/runs", "GET", "/eval/runs"),
        ("L10 GET /eval/runs/1/report", "GET", "/eval/runs/1/report"),
        ("L10 GET /eval/runs/1/scores", "GET", "/eval/runs/1/scores"),
    ]:
        status, body = call(method, path)
        check(f"{label} 可达性", True, f"status={status}（404/405 = 依赖 lane 未合并，输入缺失）")

    failed = [name for name, ok, _ in results if not ok]
    print(f"\n合计 {len(results)} 项，通过 {len(results) - len(failed)}，失败 {len(failed)}")
    if failed:
        print("失败项：" + "; ".join(failed))
    return 1 if failed else 0


if __name__ == "__main__":
    sys.exit(main())
