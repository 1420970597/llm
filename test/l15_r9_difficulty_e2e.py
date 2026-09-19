#!/usr/bin/env python3
"""R9 真实 LLM 端到端证据：小 total 下困难档不再恒为 0（issue #10 / #11）。

跑法（先起本 lane 的候选栈）：
    bash scripts/l15-r9-stack.sh up
    python3 test/l15_r9_difficulty_e2e.py

被测服务默认 http://127.0.0.1:18109 —— 本 lane 自己构建的镜像，**不是** :3210
（那是 main 分支的镜像，缺陷仍在那里）。

本脚本验证的是**真实链路**：
    真实 HTTP 入队 → 真实 worker 出队 → 真实 LLM 调用 → 真实落库
断言只看落库的 questions.difficulty 分布，不看任何中间变量。

## 夹具与反污染（契约 §6.2）

每个方向生成 2 个问题会真实调用 LLM，代价高，因此夹具只造**最小充分样本**：
1 个数据集 + 1 个 level=2 方向（方向行由 SQL 直接插入，属夹具准备，不是伪造结果；
被断言的是真实 LLM 产出的问题难度）。

所有写入行都用唯一前缀 `l15-r9-<pid>-`，结束（含失败路径）按 id 精确清理，
不做无 WHERE 的批量删除。需要保留证据时在输出里打印保留 id。
"""

import json
import os
import subprocess
import sys
import time
import urllib.error
import urllib.request

BASE = os.environ.get("R9_BASE", "http://127.0.0.1:18109")
ADMIN_EMAIL = "admin@company.com"
ADMIN_PASSWORD = "admin123456"
PG = ["docker", "exec", "llm-postgres-1", "psql", "-U", "llm_factory", "-d", "llm_factory", "-tAc"]

RUN_ID = f"l15-r9-{os.getpid()}-"
RESULTS: list[tuple[str, bool, str]] = []


def check(name: str, ok: bool, detail: str = "") -> bool:
    RESULTS.append((name, bool(ok), detail))
    print(f"[{'PASS' if ok else 'FAIL'}] {name}" + (f" — {detail}" if detail else ""), flush=True)
    return bool(ok)


def psql(sql: str) -> str:
    out = subprocess.run(PG + [sql], capture_output=True, text=True, timeout=60)
    if out.returncode != 0:
        raise RuntimeError(f"psql failed: {out.stderr.strip()}")
    return out.stdout.strip()


class Session:
    """极简 cookie 会话（认证走 llm_session cookie）。"""

    def __init__(self, base: str) -> None:
        self.base = base
        self.cookie: str | None = None

    def request(self, method: str, path: str, body=None, timeout: int = 60):
        headers = {}
        data = None
        if body is not None:
            data = json.dumps(body).encode()
            headers["Content-Type"] = "application/json"
        if self.cookie:
            headers["Cookie"] = self.cookie
        req = urllib.request.Request(self.base + path, data=data, headers=headers, method=method)
        try:
            with urllib.request.urlopen(req, timeout=timeout) as res:
                set_cookie = res.headers.get("Set-Cookie")
                if set_cookie:
                    self.cookie = set_cookie.split(";")[0]
                return res.status, res.read().decode("utf-8", "replace")
        except urllib.error.HTTPError as err:
            return err.code, err.read().decode("utf-8", "replace")

    def json(self, method: str, path: str, body=None, timeout: int = 60):
        status, raw = self.request(method, path, body, timeout)
        try:
            return status, json.loads(raw)
        except json.JSONDecodeError:
            return status, {"_raw": raw}


def cleanup(dataset_id: int | None) -> None:
    """按 id 精确清理本脚本创建的行（datasets 级联子表）。"""
    if not dataset_id:
        return
    psql(f"DELETE FROM datasets WHERE id = {int(dataset_id)} AND name LIKE '{RUN_ID}%';")
    print(f"[cleanup] 已删除 dataset id={dataset_id}（name LIKE '{RUN_ID}%'）", flush=True)


def create_fixture(session: Session, provider_id: int, tag: str) -> int | None:
    """建一个最小夹具：1 个数据集 + 1 个 level=2 方向，返回 dataset id。

    方向行由 SQL 直接插入，属夹具准备而非伪造结果；被断言的是真实 LLM 产出的难度。
    每次调用都用独立数据集：Redis 去重键 dedup:questions.generate:<id>（10 分钟 TTL）
    使同一数据集在窗口内只有首次入队会真正投递任务。
    """
    status, created = session.json(
        "POST",
        "/api/v1/datasets",
        {
            "name": f"{RUN_ID}{tag}",
            "rootKeyword": "海上巡逻",
            "targetSize": 4,
            "providerId": provider_id,
            "questionsPerDirection": 2,
            "status": "draft",
        },
    )
    if status not in (200, 201) or not isinstance(created, dict):
        check(f"夹具[{tag}] 创建数据集", False, f"HTTP {status} body={json.dumps(created, ensure_ascii=False)[:160]}")
        return None
    dataset_id = int(created["id"])
    psql(
        f"INSERT INTO domains (dataset_id, name, canonical_name, level, source, review_status) "
        f"VALUES ({dataset_id}, '{RUN_ID}方向-海上巡逻', '{RUN_ID}方向-海上巡逻', 2, 'ai', 'draft');"
    )
    count = int(psql(f"SELECT count(*) FROM domains WHERE dataset_id={dataset_id} AND level=2;") or "0")
    if not check(f"夹具[{tag}] 数据集含 1 个 level=2 方向", count == 1, f"level=2 方向数={count}"):
        return None
    print(f"[fixture] tag={tag} dataset id={dataset_id} provider_id={provider_id}", flush=True)
    return dataset_id


def fetch_difficulty(dataset_id: int, want: int, timeout_seconds: int = 600) -> list[tuple[str, int]]:
    """轮询落库的 difficulty，直到凑够 want 条或超时。"""
    deadline = time.time() + timeout_seconds
    rows: list[tuple[str, int]] = []
    while time.time() < deadline:
        raw = psql(
            f"SELECT difficulty || '|' || difficulty_score FROM questions "
            f"WHERE dataset_id={dataset_id} ORDER BY id;"
        )
        rows = []
        for line in raw.splitlines():
            line = line.strip()
            if "|" in line:
                level, score = line.split("|", 1)
                rows.append((level, int(score)))
        if len(rows) >= want:
            break
        time.sleep(10)
    return rows


def run_scenario(session: Session, provider_id: int, per_direction: int, mix: dict) -> None:
    """一个完整场景：独立数据集 → 真实入队 → 真实 LLM → 断言落库难度分布。"""
    tag = f"difficulty-x{per_direction}"
    dataset_id = create_fixture(session, provider_id, tag)
    if dataset_id is None:
        return
    try:
        status, enq = session.json(
            "POST",
            f"/api/v1/datasets/{dataset_id}/questions/generate",
            {"questionsPerDirection": per_direction, "difficultyMix": mix},
        )
        if not check(f"[x{per_direction}] 入队返回 202 queued",
                     status == 202 and isinstance(enq, dict) and enq.get("state") == "queued",
                     f"HTTP {status} body={json.dumps(enq, ensure_ascii=False)[:200]}"):
            return
        print(f"[enqueue] x={per_direction} difficultyMix={mix}", flush=True)

        rows = fetch_difficulty(dataset_id, per_direction)
        counts: dict[str, int] = {"easy": 0, "medium": 0, "hard": 0}
        for level, _score in rows:
            counts[level] = counts.get(level, 0) + 1
        print(f"[result] x={per_direction} 落库难度分布 = {counts}  rows={rows}", flush=True)

        if not check(f"[x{per_direction}] 真实 LLM 落库 {per_direction} 条问题",
                     len(rows) >= per_direction, f"落库 {len(rows)} 条；rows={rows}"):
            return

        # 契约 §1.3：total >= 档位数（3）时每档至少 1 条。
        if per_direction >= 3:
            check(f"契约[x{per_direction}] 每档至少 1 条（契约 §1.3）",
                  all(counts[level] >= 1 for level in ("easy", "medium", "hard")),
                  f"分布={counts} rows={rows}")
        # 小 total：按难度降序保留，困难档必须出现（issue #10 生产形状）。
        else:
            check(f"核心[x{per_direction}] 困难档不再恒为 0（issue #10）", counts["hard"] >= 1,
                  f"hard={counts['hard']} 分布={counts} rows={rows}")
            check(f"契约[x{per_direction}] total=2 按难度降序保留困难+中等",
                  counts["hard"] == 1 and counts["medium"] == 1 and counts["easy"] == 0,
                  f"分布={counts}")

        score_by_level = {"easy": 1, "medium": 2, "hard": 3}
        bad = [(l, sc) for l, sc in rows if score_by_level.get(l) != sc]
        check(f"[x{per_direction}] difficulty_score 与 difficulty 一致", not bad, f"不一致={bad}")

        status, stats = session.json("GET", f"/api/v1/datasets/{dataset_id}/questions/difficulty-stats")
        levels = (stats or {}).get("levels") if isinstance(stats, dict) else None
        check(f"[x{per_direction}] difficulty-stats 如实反映落库分布",
              status == 200 and isinstance(levels, dict)
              and all(levels.get(level, 0) == counts[level] for level in ("easy", "medium", "hard")),
              f"HTTP {status} levels={levels} 落库={counts}")
    finally:
        cleanup(dataset_id)


def main() -> int:
    session = Session(BASE)
    print("=" * 78)
    print(f"R9 难度分层真实 LLM 端到端 — {BASE}（唯一前缀 {RUN_ID}）")
    print("=" * 78)

    try:
        try:
            status, _ = session.json("GET", "/api/v1/health")
        except Exception as err:  # noqa: BLE001 - 需要把连接失败如实报出
            check("前置 候选服务可达", False, f"{BASE} 不可达：{err}；先跑 bash scripts/l15-r9-stack.sh up")
            return 1
        check("前置 候选服务健康检查可达", status != 0, f"HTTP {status}")

        status, _ = session.json(
            "POST", "/api/v1/auth/login", {"email": ADMIN_EMAIL, "password": ADMIN_PASSWORD}
        )
        if not check("前置 管理员登录成功", status == 200, f"HTTP {status}"):
            return 1

        # provider_id 取已引导的真实可用 Provider（deepseek-v4.1-flash）。
        provider_id = int(psql("SELECT id FROM model_providers ORDER BY id LIMIT 1;") or "1")
        mix = {"easy": 0.3, "medium": 0.5, "hard": 0.2}

        # x=2 锁定 issue #10 的生产形状（每个方向 2 个问题）。
        run_scenario(session, provider_id, 2, mix)
        # x=3 锁定契约 §1.3「total >= 档位数时每档至少 1 条」。
        run_scenario(session, provider_id, 3, mix)

        return 0 if all(ok for _, ok, _ in RESULTS) else 1
    finally:
        passed = sum(1 for _, ok, _ in RESULTS if ok)
        print("-" * 78)
        print(f"结果：{passed}/{len(RESULTS)} 通过")
        for name, ok, detail in RESULTS:
            if not ok:
                print(f"  FAIL {name} — {detail}")


if __name__ == "__main__":
    sys.exit(main())
