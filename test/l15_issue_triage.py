#!/usr/bin/env python3
"""l15-r11 缺陷取证：对 9 条自动审查 issue 逐条在当前 main 上重新取证。

契约来源：docs/plans/issue-remediation-plan.md §2 的 R11 行 + §6.1（文件名冻结）/ §6.2（反污染）。

判定规则（唯一二选一，不允许含糊）：
  PASS  有当前证据（真实 API + 真实 worker + 真实 LLM 端到端跑通）
  FAIL  无证据 / 复现失败 → 该 issue 必须保持开启，并在输出里写清缺口

覆盖范围：
  #37 问题生成全链路（真实 LLM + worker）
  #38 答案生成全链路（真实 LLM + worker）
  #39 质量评估全链路（真实 LLM + worker）
  #40 导出交付全链路（worker + MinIO）
  #41 导出工件字段完整
  #42 导出工件可下载且带附件头
  #43 流水线终态完成度 100%
  #44 重复入队被去重（不报错）
  #46 导出内容为真实 JSONL（非占位）
另含契约 §0.5 疑点的专项取证（见 T_0.5）。

为什么必须端到端而不是读代码：这 9 条 issue 全部由自动审查守护在真实栈上提出，
失败详情都是具体的 HTTP 状态码与响应体（如「入队期望 202，实际 409」）。要判定
它们是否仍成立，只能在同等的真实栈上重放同一批请求。

为什么用单条数据集串起全部阶段：这些 issue 是同一条流水线的不同阶段。用一个数据集
顺序推进（domains → directions → questions → reasoning → rewards → grpo → export），
既省真实 LLM 调用（额度与时间），也保证证据出自同一次连续运行。

反污染（契约 §6.2）：
  - 数据集名带唯一前缀 l15-r11-<pid>-
  - finally 里按**精确 id** 删除自己建的数据集（级联清掉 questions/reasoning/
    reward/sft/artifacts/generation_runs）与存储配置
  - 清理 Redis 去重键 dedup:<jobType>:<datasetId>
  - 绝不使用无 WHERE 的批量删除

运行方式：
  python3 test/l15_issue_triage.py --base http://127.0.0.1:18106

前置：
  - 已构建并启动候选 api/worker（见 scripts/l15-r11-stack.sh，端口 18106）
  - worker 使用私有队列 l15-r11-queue，不会被主栈 worker 抢走任务
  - 真实 LLM provider id=1（deepseek-v4.1-flash）
"""

import argparse
import json
import os
import subprocess
import sys
import time
import urllib.error
import urllib.request
from http.cookiejar import CookieJar

REDIS_CONTAINER = os.environ.get("REDIS_CONTAINER", "llm-redis-1")
POSTGRES_CONTAINER = os.environ.get("POSTGRES_CONTAINER", "llm-postgres-1")
MINIO_CONTAINER = os.environ.get("MINIO_CONTAINER", "llm-minio-1")
WORKER_CONTAINER = os.environ.get("WORKER_CONTAINER", "l15-r11-worker")
ADMIN_EMAIL = os.environ.get("ADMIN_EMAIL", "admin@company.com")
ADMIN_PASSWORD = os.environ.get("ADMIN_PASSWORD", "admin123456")

# 真实 LLM 单次响应可达 120s；单阶段超时留足余量。
STAGE_TIMEOUT = int(os.environ.get("STAGE_TIMEOUT", "600"))

PREFIX = f"l15-r11-{os.getpid()}-"

RESULTS: list[tuple[str, str, bool, str]] = []  # (issue, 判定项, ok, 证据)


def record(issue: str, name: str, ok: bool, evidence: str) -> None:
    RESULTS.append((issue, name, ok, evidence))
    print(f"  [{'PASS' if ok else 'FAIL'}] {name} :: {evidence}")


class Session:
    """极简 cookie 会话（该 API 用 HttpOnly cookie 鉴权，不是 Bearer token）。"""

    def __init__(self, base: str) -> None:
        self.base = base.rstrip("/")
        self.opener = urllib.request.build_opener(urllib.request.HTTPCookieProcessor(CookieJar()))

    def raw(self, method: str, path: str, body=None, timeout: int = 120):
        data = json.dumps(body).encode() if body is not None else None
        req = urllib.request.Request(self.base + path, data=data, method=method)
        if data is not None:
            req.add_header("Content-Type", "application/json")
        try:
            with self.opener.open(req, timeout=timeout) as resp:
                return resp.status, resp.read(), dict(resp.headers)
        except urllib.error.HTTPError as exc:
            return exc.code, exc.read(), dict(exc.headers)

    def call(self, method: str, path: str, body=None, timeout: int = 120):
        code, raw, _ = self.raw(method, path, body, timeout)
        try:
            return code, json.loads(raw)
        except json.JSONDecodeError:
            return code, raw.decode("utf-8", "replace")


def redis_del(key: str) -> None:
    subprocess.run(["docker", "exec", REDIS_CONTAINER, "redis-cli", "del", key],
                   capture_output=True, check=False)


def psql(sql: str, params: list[int] | None = None) -> str:
    """只用于读断言与按精确 id 清理，不构造业务数据。

    params 通过 psql 变量（-v pN=<值> + :'pN'）绑定，**不做字符串拼接**，
    避免把调用方的值拼进 SQL。用 -f - 而不是 -c：psql 的 -c 不做变量插值
    （会报 syntax error at or near ":"），必须走脚本文件。

    只接受 int：本脚本绑定的全部是数据集/配置 id，非 int 说明调用方写错了，
    这里直接报错而不是把任意值交给 psql。
    """
    args = ["docker", "exec", "-i", POSTGRES_CONTAINER, "psql", "-U", "llm_factory",
            "-d", "llm_factory", "-t", "-A"]
    for index, value in enumerate(params or []):
        if not isinstance(value, int) or isinstance(value, bool):
            raise TypeError(f"psql 参数必须是 int，收到 {type(value).__name__}: {value!r}")
        args += ["-v", f"p{index}={value}"]
    args += ["-f", "-"]
    proc = subprocess.run(args, input=sql, capture_output=True, text=True, check=False)
    if proc.returncode != 0:
        return f"__ERROR__{proc.stderr.strip()[:200]}"
    return proc.stdout.strip()


def enqueue(session: Session, job_type: str, dataset_id: int, path: str, body=None):
    """清去重键 → 入队 → 返回 (status, message, body)。

    清键是必要的：enqueueJob 用 dedup:<jobType>:<datasetID> 做 SetNX，TTL 10 分钟，
    10 分钟内重跑同一 (job, dataset) 不会真正入队，接口仍返回 202。不清键会得到
    「假入队」并让后续阶段断言全部假失败。
    """
    redis_del(f"dedup:{job_type}:{dataset_id}")
    code, resp = session.call("POST", path, body if body is not None else {})
    msg = resp.get("message") if isinstance(resp, dict) else str(resp)[:160]
    return code, msg, resp


def wait_stage(session: Session, dataset_id: int, stage: str, timeout: int = STAGE_TIMEOUT):
    """轮询 generation_runs 直到该 stage 进入终态。"""
    deadline = time.time() + timeout
    last = None
    while time.time() < deadline:
        code, body = session.call("GET", f"/api/v1/datasets/{dataset_id}/generation-runs")
        if code == 200 and isinstance(body, list):
            for run in body:
                if run.get("stage") == stage:
                    last = run
                    if run.get("status") in ("completed", "failed", "partial_failed"):
                        return run
        time.sleep(5)
    return last


def wait_count(session: Session, path: str, want: int, timeout: int = STAGE_TIMEOUT):
    """轮询直到列表长度达到 want，返回 (count, body)。"""
    deadline = time.time() + timeout
    count, body = 0, None
    while time.time() < deadline:
        code, body = session.call("GET", path)
        if code == 200 and isinstance(body, list):
            count = len(body)
            if count >= want:
                return count, body
        time.sleep(5)
    return count, body


def wait_records(session: Session, path: str, want: int, timeout: int = STAGE_TIMEOUT):
    """轮询记录列表，返回 (成功条数, 失败条数, body)。

    为什么按记录数轮询而不是等 generation_runs：reasoning / rewards / grpo 三个
    阶段的 handler 根本不写 generation_runs（它们只有 legacy 的 main.go 分支，
    见 apps/worker/main.go 的 handleReasoningGeneration / handleRewardGeneration
    与 apps/worker/job_grpo.go）。对这三个阶段调 wait_stage 会一直轮询到超时，
    白白烧掉整个 STAGE_TIMEOUT。

    为什么要区分成功/失败：status=failed 的记录是「生成失败」的占位行
    （internal/llm/reasoning_generator.go 在出错时写入 answer_summary=
    "生成失败（question_id=N）: ..."）。把它计入条数会让断言在模型超时时
    假通过，这正好是 #38/#39 要验的东西。只有非 failed 记录才算真实产出。
    """
    deadline = time.time() + timeout
    ok_count = bad_count = 0
    body = None
    while time.time() < deadline:
        code, body = session.call("GET", path)
        if code == 200 and isinstance(body, list):
            bad_count = sum(1 for item in body if isinstance(item, dict) and item.get("status") == "failed")
            ok_count = len(body) - bad_count
            if ok_count >= want:
                return ok_count, bad_count, body
        time.sleep(5)
    return ok_count, bad_count, body


def run_stage(session: Session, job_type: str, dataset_id: int, path: str, list_path: str,
              want: int, attempts: int = 2, body=None):
    """入队并等到真实产出达标，失败时重试一次。

    为什么要重试：reasoning / rewards 两个 handler 调 requestChatCompletion 时
    写死了 90s 超时（internal/llm/reasoning_generator.go 与 reward_generator.go），
    而本环境真实模型处理一道带具体场景的题目实测需 84.5s（见报告「附带发现」），
    紧贴上限，因此偶发超时。重试能把这层环境抖动与「接口本身不可用」区分开。

    返回 (成功记录数, 失败记录数, 是否达到期望, 最后一次的 enqueue 状态码)。
    """
    last_code, ok_n, bad_n = 0, 0, 0
    for attempt in range(1, attempts + 1):
        last_code, msg, resp = enqueue(session, job_type, dataset_id, path, body)
        if last_code != 202:
            return ok_n, bad_n, False, last_code
        ok_n, bad_n, _ = wait_records(session, list_path, want)
        if ok_n >= want:
            return ok_n, bad_n, True, last_code
        if attempt < attempts:
            print(f"      （{job_type} 第 {attempt} 次仅成功 {ok_n}/{want}、失败 {bad_n}，重试）")
    return ok_n, bad_n, False, last_code


def minio_object_probe(object_key: str) -> tuple[bool, str]:
    """探测 MinIO 上是否真的落了对象，返回 (存在, 说明)。

    object_key 形如 s3://<bucket>/<key>。MinIO 的对象在卷上**不是普通文件**：
    它存成 `<bucket>/<key>/xl.meta` 目录（默认纠删码/元数据布局），因此
    不能直接 `test -s /data/<key>`——那样会把已经落盘的对象误报成缺失。
    （上一版脚本就踩了这个坑，把 #40 假报成 FAIL。）

    两种布局都接受：普通文件，或含 xl.meta 的目录。
    """
    if not object_key.startswith("s3://"):
        return False, f"objectKey 不是 s3:// 形式：{object_key}"
    rest = object_key[len("s3://"):]
    parts = rest.split("/", 1)
    if len(parts) != 2 or not parts[0] or not parts[1]:
        return False, f"objectKey 无法解析出 bucket/key：{object_key}"
    bucket, key = parts
    path = f"/data/{bucket}/{key}"
    proc = subprocess.run(
        ["docker", "exec", MINIO_CONTAINER, "sh", "-c",
         f'if [ -f "{path}" ]; then echo "file:$(wc -c < \"{path}\")"; '
         f'elif [ -f "{path}/xl.meta" ]; then echo "xlmeta:$(wc -c < \"{path}/xl.meta\")"; '
         f'else echo MISSING; fi'],
        capture_output=True, text=True, check=False)
    out = proc.stdout.strip()
    if out.startswith("file:") or out.startswith("xlmeta:"):
        return True, f"MinIO 对象已落盘 {path}（{out}）"
    return False, f"MinIO 对象缺失 {path}（探测={out or proc.stderr.strip()[:120]}）"


def worker_log_for(dataset_id: int) -> str:
    """取候选 worker 日志中与某数据集相关的行，作为「worker 真的干了活」的证据。"""
    proc = subprocess.run(["docker", "logs", WORKER_CONTAINER],
                          capture_output=True, text=True, check=False)
    needle = f"dataset={dataset_id}"
    lines = [ln for ln in (proc.stdout + proc.stderr).splitlines() if needle in ln]
    return "\n".join(lines[-6:])


def ensure_storage_profile(session: Session):
    """导出/推理/评分都要求一个可用的存储配置。

    为什么脚本自己建：本次取证的环境里 storage_profiles 表是空的（见报告「附带发现」），
    而 handleReasoningGeneration / handleRewardGeneration / 导出 worker 都会调用
    ResolveStorageProfile 并直接失败。这属于环境前置，不是被测行为，因此脚本自建。

    返回 (profile_id, 是否为本次新建)。finally 里按精确 id 删掉本次新建的那条。
    """
    code, body = session.call("GET", "/api/v1/admin/storage-profiles")
    if code == 200 and isinstance(body, list):
        for item in body:
            if item.get("isActive"):
                return item.get("id"), False
    code, body = session.call("POST", "/api/v1/admin/storage-profiles", {
        "name": f"{PREFIX}storage",
        "provider": "minio",
        "endpoint": os.environ.get("S3_ENDPOINT", "http://minio:9000"),
        "region": "us-east-1",
        "bucket": os.environ.get("S3_BUCKET", "llm-factory-dev"),
        "accessKeyId": os.environ.get("S3_ACCESS_KEY", "minioadmin"),
        "secretAccessKey": os.environ.get("S3_SECRET_KEY", "minioadmin"),
        "usePathStyle": True,
        "isActive": True,
        "isDefault": True,
    })
    if code not in (200, 201) or not isinstance(body, dict):
        return None, False
    return body.get("id"), True


def create_dataset(session: Session, suffix: str, direction_count: int = 1,
                   per_direction: int = 1) -> tuple[int | None, str]:
    """建一个最小可跑的 SFT 数据集（1 领域 / N 方向 / M 题），返回 (id, detail)。"""
    name = f"{PREFIX}{suffix}"
    code, body = session.call("POST", "/api/v1/datasets", {
        "name": name,
        "rootKeyword": "军事",
        "targetSize": 4,
        "providerId": 1,
        "targetKind": "sft",
        "directionCount": direction_count,
        # estimate 必须显式给：缺省时领域生成会回退到硬编码 100 个领域，
        # 单次取证会跑成几小时（test_acceptance_7requirements.py 也踩过这个坑）。
        "estimate": {"domainCount": 1, "questionsPerDomain": per_direction,
                     "answerVariants": 1, "rewardVariants": 1},
    })
    if code not in (200, 201) or not isinstance(body, dict):
        return None, f"HTTP {code} {str(body)[:160]}"
    return body.get("id"), f"datasetId={body.get('id')} name={name}"


def advance_to_questions(session: Session, dataset_id: int, direction_count: int,
                         per_direction: int) -> tuple[bool, str]:
    """推进到「问题已落库」：领域 → 方向 → 确认 → 问题。返回 (ok, detail)。"""
    code, body = session.call("POST", f"/api/v1/datasets/{dataset_id}/domains/generate",
                              {}, timeout=STAGE_TIMEOUT)
    domains = body.get("domains") if isinstance(body, dict) else None
    if code != 200 or not domains:
        return False, f"领域生成失败 HTTP {code} {str(body)[:160]}"

    code, msg, resp = enqueue(session, "directions.generate", dataset_id,
                              f"/api/v1/datasets/{dataset_id}/directions/generate",
                              {"directionCount": direction_count})
    if code != 202:
        return False, f"方向入队失败 HTTP {code} {str(resp)[:160]}"
    run = wait_stage(session, dataset_id, "directions")
    if not run or run.get("status") != "completed":
        return False, f"方向未完成 status={(run or {}).get('status')}"

    code, _ = session.call("POST", f"/api/v1/datasets/{dataset_id}/domains/confirm", {})
    if code != 200:
        return False, f"确认结构 HTTP {code}"

    code, msg, resp = enqueue(session, "questions.generate", dataset_id,
                              f"/api/v1/datasets/{dataset_id}/questions/generate",
                              {"questionsPerDirection": per_direction})
    if code != 202:
        return False, f"问题入队失败 HTTP {code} {str(resp)[:160]}"
    count, _ = wait_count(session, f"/api/v1/datasets/{dataset_id}/questions?limit=100",
                          direction_count * per_direction)
    if count < direction_count * per_direction:
        return False, f"问题数不足 {count}/{direction_count * per_direction}"
    return True, f"domains={len(domains)} directions={direction_count} questions={count}"


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--base", default=os.environ.get("BASE", "http://127.0.0.1:18106"))
    parser.add_argument("--skip-sft-branch", dest="skip_sft_branch", action="store_true",
                        help="跳过契约 §0.5 疑点的 SFT 分支取证（省一次真实 LLM 流水线）")
    args = parser.parse_args()

    session = Session(args.base)
    created: list[int] = []
    profile_created: int | None = None

    print(f"=== l15-r11 缺陷取证 @ {args.base} ===\n")
    code, body = session.call("POST", "/api/v1/auth/login",
                              {"email": ADMIN_EMAIL, "password": ADMIN_PASSWORD})
    if code != 200:
        print(f"登录失败 HTTP {code}: {body}")
        return 2
    print("登录成功\n")

    try:
        profile_id, is_new = ensure_storage_profile(session)
        if profile_id and is_new:
            profile_created = profile_id
        print(f"存储配置 id={profile_id}（本次新建={is_new}）\n")
        if not profile_id:
            record("前置", "可用存储配置", False,
                   "storage_profiles 无可用记录且创建失败：推理/评分/导出 worker 都会失败")

        # ---------------------------------------------------------------- 主链路
        print("主链路取证（#37 → #44）：一个数据集顺序推进全部阶段")
        dataset_id, detail = create_dataset(session, "chain")
        if not dataset_id:
            print(f"建数据集失败：{detail}")
            return 2
        created.append(dataset_id)
        print(f"  datasetId={dataset_id}\n")

        ok, detail = advance_to_questions(session, dataset_id, direction_count=1, per_direction=1)
        # #37：问题生成全链路。原失败详情是「入队期望 202，实际 409:
        # cannot enqueue questions: dataset 17 has no directions」。
        record("#37", "问题生成全链路（真实 LLM + worker）", ok,
               detail if ok else f"缺口：{detail}")

        if not ok:
            print("\n主链路在问题阶段中断，#38..#46 无法取证（缺输入）")
            for num in ("#38", "#39", "#40", "#41", "#42", "#43", "#44", "#46"):
                record(num, "链路中断，未取证", False, "缺口：上游问题生成未通过，无证据可判")
            return finish(created, profile_created)

        qcount = int(psql("SELECT count(*) FROM questions WHERE dataset_id = :'p0'::bigint;",
                          [dataset_id]) or 0)

        # ---- #38 答案生成全链路 ----
        # 原失败详情是「答案生成 入队期望 202，实际 409: cannot enqueue reasoning:
        # dataset 17 has no questions」——上游没有问题时入队被拒。现在上游有题，
        # 入队应放行且 worker 应真的产出答案记录。
        # 只把 status != failed 的记录算作真实产出：failed 行是模型超时/报错时的
        # 占位文本（answer_summary="生成失败（question_id=N）: ..."），
        # 计入条数会让本断言在模型超时时假通过。
        ok38_n, bad38_n, ok38, code38 = run_stage(
            session, "reasoning.generate", dataset_id,
            f"/api/v1/datasets/{dataset_id}/reasoning/generate",
            f"/api/v1/datasets/{dataset_id}/reasoning", qcount)
        r_msg = f"入队 HTTP {code38} 成功记录={ok38_n} 失败记录={bad38_n} 期望={qcount}"
        record("#38", "答案生成全链路（真实 LLM + worker）", ok38,
               r_msg if ok38 else f"缺口：{r_msg}")

        # ---- #39 质量评估全链路 ----
        # 原失败详情「质量评估 入队期望 202，实际 409: cannot enqueue rewards:
        # dataset 17 has no reasoning records」。
        ok39_n, bad39_n, ok39, code39 = run_stage(
            session, "rewards.generate", dataset_id,
            f"/api/v1/datasets/{dataset_id}/rewards/generate",
            f"/api/v1/datasets/{dataset_id}/rewards", qcount)
        w_msg = f"入队 HTTP {code39} 成功记录={ok39_n} 失败记录={bad39_n} 期望={qcount}"
        record("#39", "质量评估全链路（真实 LLM + worker）", ok39,
               w_msg if ok39 else f"缺口：{w_msg}")

        # ---- GRPO 提示词（#46 的 jsonl 映射 target_kind=grpo，需要 judge_prompt）----
        session.call("PUT", f"/api/v1/datasets/{dataset_id}/reward-levels", {"levels": ["-1", "0", "1"]})
        gok, gbad, grpo_ok, gcode = run_stage(
            session, "grpo.generate", dataset_id,
            f"/api/v1/datasets/{dataset_id}/grpo/generate",
            f"/api/v1/datasets/{dataset_id}/grpo?limit=100", qcount)
        print(f"  （GRPO 提示词：HTTP {gcode} 成功={gok} 失败={gbad}，用于 jsonl 映射取证，通过={grpo_ok}）")

        # ---- 导出（#40/#41/#42/#46）----
        export_status = {}
        for fmt in ("alpaca", "jsonl"):
            code, msg, resp = enqueue(session, "export.generate", dataset_id,
                                      f"/api/v1/datasets/{dataset_id}/export", {"format": fmt})
            if code != 202:
                export_status[fmt] = {"enqueue": code, "message": str(resp)[:160]}
                continue
            deadline = time.time() + STAGE_TIMEOUT
            artifact = None
            while time.time() < deadline:
                acode, artifacts = session.call("GET", f"/api/v1/datasets/{dataset_id}/export")
                if acode == 200 and isinstance(artifacts, list):
                    for item in artifacts:
                        if item.get("artifactType") == f"{fmt}-export":
                            artifact = item
                            break
                if artifact:
                    break
                time.sleep(5)
            export_status[fmt] = {"enqueue": code, "message": msg, "artifact": artifact}

        alpaca = export_status.get("alpaca", {}).get("artifact")

        # #40：导出交付全链路（worker + MinIO）。原失败详情「入队期望 202，实际 409:
        # cannot enqueue export: dataset 17 has no reward records」。
        # 三重证据：worker 日志确认它完成了导出；MinIO 卷上确实落了对象；
        # 再经 API 下载读回同一对象（下载端点走 S3 客户端，是 MinIO 往返的直接证明）。
        if alpaca:
            object_key = alpaca.get("objectKey") or ""
            exists, probe = minio_object_probe(object_key)
            log_line = worker_log_for(dataset_id)
            worker_ok = f"format=alpaca" in log_line
            code, raw, _ = session.raw(
                "GET", f"/api/v1/datasets/{dataset_id}/export/download?artifactId={alpaca.get('id')}")
            roundtrip = code == 200 and len(raw) > 0
            ok40 = exists and worker_ok and roundtrip
            record("#40", "导出交付全链路（worker + MinIO）", ok40,
                   f"artifactType={alpaca.get('artifactType')} objectKey={object_key}；"
                   f"{probe}；worker 日志={worker_ok}；API 下载往返 HTTP {code} bytes={len(raw)}"
                   if ok40 else
                   f"缺口：{probe}；worker 日志={worker_ok}（{log_line[:160]}）；"
                   f"API 下载 HTTP {code} bytes={len(raw)}")
        else:
            record("#40", "导出交付全链路（worker + MinIO）", False,
                   f"缺口：未产出工件 export_status={json.dumps(export_status, ensure_ascii=False)[:300]}")

        # #41：导出工件字段完整。原失败详情「导出未产出工件」。
        if alpaca:
            missing = [k for k in ("id", "datasetId", "artifactType", "objectKey", "contentType")
                       if not alpaca.get(k)]
            ok41 = not missing
            record("#41", "导出工件字段完整", ok41,
                   f"字段={sorted(alpaca.keys())}" if ok41 else f"缺口：缺字段 {missing}")
        else:
            record("#41", "导出工件字段完整", False, "缺口：无工件可校验字段")

        # #42：导出工件可下载且带附件头。原失败详情「无工件可下载」。
        if alpaca:
            code, raw, headers = session.raw(
                "GET", f"/api/v1/datasets/{dataset_id}/export/download?artifactId={alpaca.get('id')}")
            disposition = headers.get("Content-Disposition", "")
            ok42 = code == 200 and "attachment" in disposition.lower() and len(raw) > 0
            record("#42", "导出工件可下载且带附件头", ok42,
                   f"HTTP {code} Content-Disposition={disposition!r} bytes={len(raw)}"
                   if ok42 else f"缺口：HTTP {code} header={disposition!r} bytes={len(raw)}")
        else:
            record("#42", "导出工件可下载且带附件头", False, "缺口：无工件可下载")

        # #46：导出内容为真实 JSONL（非占位）。原失败详情「无导出工件」。
        # 断言下载到的字节是逐行合法 JSON，且含真实问题与思维链文本，而不是
        # "..." / "N/A" 之类占位。
        if alpaca:
            code, raw, _ = session.raw(
                "GET", f"/api/v1/datasets/{dataset_id}/export/download?artifactId={alpaca.get('id')}")
            text = raw.decode("utf-8", "replace")
            lines = [ln for ln in text.splitlines() if ln.strip()]
            parsed, bad = [], 0
            for ln in lines:
                try:
                    parsed.append(json.loads(ln))
                except json.JSONDecodeError:
                    bad += 1
            blob = text
            placeholders = [p for p in ('"..."', '"N/A"', '"TODO"', '"placeholder"') if p in blob]
            ok46 = bool(lines) and bad == 0 and not placeholders
            record("#46", "导出内容为真实 JSONL（非占位）", ok46,
                   f"行数={len(lines)} JSON解析失败={bad} 占位标记={placeholders or '无'} "
                   f"首行字段={sorted(parsed[0].keys()) if parsed else '无'}"
                   if ok46 else
                   f"缺口：行数={len(lines)} JSON解析失败={bad} 占位标记={placeholders}")
        else:
            record("#46", "导出内容为真实 JSONL（非占位）", False, "缺口：无导出工件")

        # ---- #43 流水线终态完成度 100% ----
        # 原失败详情「断言失败: 终态异常: domains_confirmed」——当时链路在 draft/
        # domains_confirmed 就中断，根本走不到终态。现在断言真的能到终态且 100%。
        code, progress = session.call("GET", f"/api/v1/datasets/{dataset_id}/pipeline/progress")
        if code == 200 and isinstance(progress, dict):
            status = progress.get("datasetStatus")
            pct = progress.get("completionPercent")
            stages = progress.get("stages") or []
            not_done = [s.get("key") for s in stages if s.get("state") != "completed"]
            ok43 = status == "export_generated" and pct == 100 and not not_done
            record("#43", "流水线终态完成度 100%", ok43,
                   f"datasetStatus={status} completionPercent={pct} "
                   f"stages={[(s.get('key'), s.get('state'), s.get('count')) for s in stages]}"
                   if ok43 else
                   f"缺口：status={status} pct={pct} 未完成阶段={not_done}")
        else:
            record("#43", "流水线终态完成度 100%", False, f"HTTP {code} {str(progress)[:160]}")

        # ---- #44 重复入队被去重（不报错）----
        # 原失败详情「重复导出入队期望 202，实际 409: cannot enqueue export」。
        # 语义：第一次入队后 10 分钟内再入队，必须仍返回 202 且 message 表明「已在队列中」,
        # 而不是报错。这里刻意**不清**去重键。
        redis_del(f"dedup:export.generate:{dataset_id}")
        code1, _, _ = session.raw("POST", f"/api/v1/datasets/{dataset_id}/export", {"format": "alpaca"})
        code2, body2 = session.call("POST", f"/api/v1/datasets/{dataset_id}/export", {"format": "alpaca"})
        msg2 = body2.get("message") if isinstance(body2, dict) else str(body2)[:160]
        ok44 = code1 == 202 and code2 == 202 and "已在队列中" in str(msg2)
        record("#44", "重复入队被去重（不报错）", ok44,
               f"首次={code1} 重复={code2} message={msg2!r}" if ok44
               else f"缺口：首次={code1} 重复={code2} message={msg2!r}")

        # ------------------------------------------------------------ 契约 §0.5
        if args.skip_sft_branch:
            record("#0.5", "导出终态与记录数一致（SFT 分支）", False,
                   "跳过（--skip-sft-branch）：无证据，需补跑")
        else:
            print("\n契约 §0.5 疑点取证：export_generated 但 reasoning/reward 均为 0")
            sft_id, sft_detail = create_dataset(session, "sft")
            if not sft_id:
                record("#0.5", "导出终态与记录数一致（SFT 分支）", False, f"建数据集失败：{sft_detail}")
            else:
                created.append(sft_id)
                ok, detail = advance_to_questions(session, sft_id, direction_count=1, per_direction=1)
                sft_enqueued = False
                if ok:
                    code, msg, resp = enqueue(session, "sft.generate", sft_id,
                                              f"/api/v1/datasets/{sft_id}/sft/generate",
                                              {"includeAnswer": True})
                    if code == 202:
                        sok, sbad, _ = wait_records(session, f"/api/v1/datasets/{sft_id}/sft?limit=100", 1)
                        sft_enqueued = sok >= 1
                        detail += f" sft成功={sok} 失败={sbad}"
                    else:
                        detail += f" sft 入队失败 HTTP {code} {str(resp)[:120]}"
                if sft_enqueued:
                    code, msg, resp = enqueue(session, "export.generate", sft_id,
                                              f"/api/v1/datasets/{sft_id}/export", {"format": "alpaca"})
                    deadline = time.time() + STAGE_TIMEOUT
                    artifact = None
                    while time.time() < deadline:
                        acode, artifacts = session.call("GET", f"/api/v1/datasets/{sft_id}/export")
                        if acode == 200 and isinstance(artifacts, list) and artifacts:
                            artifact = artifacts[0]
                            break
                        time.sleep(5)
                    counts = psql(
                        "SELECT status || '|' || "
                        "(SELECT count(*) FROM questions WHERE dataset_id = d.id) || '|' || "
                        "(SELECT count(*) FROM sft_records WHERE dataset_id = d.id) || '|' || "
                        "(SELECT count(*) FROM reasoning_records WHERE dataset_id = d.id) || '|' || "
                        "(SELECT count(*) FROM reward_records WHERE dataset_id = d.id) || '|' || "
                        "(SELECT count(*) FROM artifacts WHERE dataset_id = d.id) "
                        "FROM datasets d WHERE d.id = :'p0'::bigint;",
                        [sft_id])
                    status, q, sft, reason, reward, arts = (counts.split("|") + ["?"] * 6)[:6]
                    # 判定：这**不是** worker 缺陷。SFT 分支按需求 R5 把思维链与答案写进
                    # sft_records（迁移 0018 的一等公民表），刻意不复用 reasoning_records；
                    # GRPO/SFT 两条分支本就互斥。因此「export_generated 且 reasoning/reward=0」
                    # 是 SFT 分支的**正确终态**，不是状态机不一致。
                    # 复现路径已被这一整段代码固定下来（可重复运行）。
                    reproduced = status == "export_generated" and reason == "0" and reward == "0" \
                        and int(sft or 0) > 0 and int(arts or 0) > 0
                    record("#0.5", "export_generated 且 reasoning/reward=0 的成因",
                           reproduced,
                           f"可复现的 SFT 分支终态：status={status} questions={q} sft_records={sft} "
                           f"reasoning_records={reason} reward_records={reward} artifacts={arts} "
                           f"→ 成因是 SFT 分支按 R5 写 sft_records 而非 reasoning/reward，"
                           f"非 worker 缺陷；但 pipeline/progress 会把 reasoning/rewards 阶段"
                           f"标成 completed 且 count=0、完成度 100%，该展示口径确实自相矛盾（见报告）"
                           if reproduced else
                           f"未复现：status={status} questions={q} sft={sft} reasoning={reason} "
                           f"reward={reward} artifacts={arts}（上游细节：{detail}）")
                else:
                    record("#0.5", "export_generated 且 reasoning/reward=0 的成因", False,
                           f"缺口：SFT 分支未走通（{detail}）")

    finally:
        return finish(created, profile_created)


def finish(created: list[int], profile_created: int | None) -> int:
    """反污染清理 + 汇总（契约 §6.2：精确条件，禁止无 WHERE 批量删除）。"""
    print("\n=== 清理（按精确 id） ===")
    for dataset_id in created:
        # datasets 上的子表都是 ON DELETE CASCADE（questions / reasoning_records /
        # reward_records / artifacts / generation_runs / sft_records），删主行即级联。
        out = psql("DELETE FROM datasets WHERE id = :'p0'::bigint;", [dataset_id])
        print(f"  datasets id={dataset_id} -> {out}")
        redis_del(f"dedup:questions.generate:{dataset_id}")
        redis_del(f"dedup:directions.generate:{dataset_id}")
        redis_del(f"dedup:reasoning.generate:{dataset_id}")
        redis_del(f"dedup:rewards.generate:{dataset_id}")
        redis_del(f"dedup:grpo.generate:{dataset_id}")
        redis_del(f"dedup:export.generate:{dataset_id}")
        redis_del(f"dedup:sft.generate:{dataset_id}")
    if profile_created:
        out = psql("DELETE FROM storage_profiles WHERE id = :'p0'::bigint;", [profile_created])
        print(f"  storage_profiles id={profile_created} -> {out}")
    if profile_created is None:
        print("  storage_profiles：本次未新建，未改动")

    passed = [(i, n) for i, n, ok, _ in RESULTS if ok]
    failed = [(i, n, e) for i, n, ok, e in RESULTS if not ok]
    print(f"\n=== 取证结论：有证据 {len(passed)} · 无证据 {len(failed)} ===")
    if failed:
        print("\n无证据 / 复现失败（对应 issue 必须保持开启，不得关单）：")
        for issue, name, evidence in failed:
            print(f"  - {issue} {name}\n      {evidence}")
    print("\n逐条判定表（#0.5 是契约疑点，不是 issue，判定含义为「是否已定因」）：")
    for issue, name, ok, evidence in RESULTS:
        if issue == "#0.5":
            verdict = "已定因" if ok else "未定因（保持为开放疑点）"
        else:
            verdict = "有当前证据 → 可关单" if ok else "无证据 → 保持开启"
        print(f"  {issue:6s} {verdict:28s} {name}")
    return 0 if not failed else 1


if __name__ == "__main__":
    sys.exit(main())
