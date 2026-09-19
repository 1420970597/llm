#!/usr/bin/env python3
"""L15 多 LLM 互评 —— 功能说明.txt「数据集评估」需求的真实端到端验证。

为什么需要这个脚本
==================
功能说明.txt 对评估能力提出 5 条硬要求：

  (1) 接入多个 llm；
  (2) 由 A 生成的数据集 A1，评估 A1 时必须**排除 A**，用其他 llm 评；
  (3) 内置**不少于 50 个**评估维度（聚焦长链思考），用户也可增加；
  (4) 支持「全量」与「抽样」（按比例或具体数量）两种评估方式；
  (5) 每条数据多维度打分 → 汇总为该 llm 对该数据集的整体分数 → 再汇总其他 llm 的
      整体分数 → 统计、分析、展示，得出结论。

在本 lane 之前，环境里只有 **1 个** provider（deepseek-v4.1-flash），而它正是数据集的
生成者。按自评剔除规则它必须被排除，于是可用裁判数为 0，评估根本跑不起来。
既有验收脚本 test_acceptance_7requirements.py 因此对该需求只能记 SKIP（输入缺失）——
这是诚实的，但意味着 (1)(2)(5) 三条**从未被真正验证过**。

本 lane 的前提变化：环境已接入 3 个**真实可用** provider（都指向真实网关、
各自真实可响应）：

  id=1   deepseek-v4.1-flash   model=global:deepseek-v4.1-flash   <- 作为生成者
  id=7   gpt-5.6-sol-judge     model=gpt-5.6-sol
  id=11  hy3-judge             model=global:hy3

生成者 = id 1，因此 id 7 与 id 11 是两个真实的、与生成者不同的裁判模型，
(1)(2)(5) 现在可以真正验证。若它们缺失，脚本按契约 §3.5 记「输入缺失」并如实
报告，**不伪造通过**。

测试项
======
  T1  登录
  T2  多 provider 就绪：/admin/eval/judges 至少 2 个候选，且含 2 个「非生成者」可用裁判
  T3  自评剔除（需求 2，核心）：/datasets/{id}/eval-judges 把生成者标 excluded=true + 中文原因
  T4  维度数量（需求 3）：内置维度 >= 50，并打印实际数字
  T5  内置维度聚焦长链思考（需求 3）：long_chain 分类存在且有实质数量
  T6  用户可新增维度（需求 3）：POST 新建 -> 列表可见 -> 精确删除
  T7  抽样参数校验（需求 4）：full / ratio / count 三种模式创建运行均受理；
      ratio=0 与 count=0 被拒（不静默当全量）
  T8  真实互评打分（需求 1+2+5，核心）：用生成者之外的两个 llm 对同一批数据打分，
      断言：每个样本都有多维度分数；每个裁判都实际出分；被剔除的生成者**没有**出分
  T9  多 LLM 汇总（需求 5，核心）：报告给出每个 llm 对该数据集的整体分数（逐裁判
      汇总）与跨裁判一致性，并有中文结论
  T10 逐条打分明细含真实中文理由（需求 5：非纯数字）

运行方式（必须打本 lane 自建的候选容器，不是 :3210 的 main 镜像）
================================================================
  bash scripts/l15-evalmulti-stack.sh          # 起 api :18170 + worker（私有队列）
  python3 test/l15_eval_multi_judge.py

反污染（契约 §6.2）
==================
  - 所有自建数据用唯一前缀 l15-evalmulti-<pid>-；
  - 结束（含失败路径 finally）按**精确 id** 清理 datasets 及其级联子表与自己新建的维度；
  - 禁止无 WHERE 的 DELETE/TRUNCATE/UPDATE；
  - 绝不执行 scripts/clear_demo_data.sh / docker compose down -v
    （共享开发库已因此发生过一次数据损失事故，见 docs/plans/round2-data-loss-incident.md）。
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

DEFAULT_BASE = "http://127.0.0.1:18170"

# 生成者 provider：数据集由它生成，评估时必须被排除。
GENERATOR_PROVIDER_ID = 1

# 评估耗时长：每样本 × 每维度 × 每裁判 都是一次真实 LLM 调用。
EVAL_TIMEOUT = int(os.environ.get("EVAL_TIMEOUT", "2400"))
GEN_TIMEOUT = int(os.environ.get("GEN_TIMEOUT", "900"))

# 抽样评估的样本数。
#
# 为什么是 3 而不是 1：
#   - 1 条无法证明「每条数据都被多维度打分」的覆盖面，也无法让两个裁判各自
#     产出有意义的整体分数；
#   - 2 条虽能出分数，但裁判一致性用的是 **Spearman 秩相关**，n=2 时 rho 只能
#     取 ±1，一致性结果退化为 0 或 1，无法反映真实分歧（实测 n=2 时得到 0，
#     听起来像「两个模型完全不一致」，其实只是秩相关的自由度不够）；
#   - 3 条是使「多 LLM 汇总 + 一致性」这两个结论都可解释的最小样本量。
# 总调用量 = 3 样本 × DIMENSION_COUNT 维度 × 2 裁判，仍在分钟级。
SAMPLE_SIZE = int(os.environ.get("SAMPLE_SIZE", "3"))

# 打分维度取前 3 个。3 样本 × 3 维度 × 2 裁判 = 18 次真实 LLM 调用，
# 这是「多维度」结论的最小充分规模；跑满 58 维度会让测试拖到小时级。
DIMENSION_COUNT = int(os.environ.get("DIMENSION_COUNT", "3"))

# 每个方向生成多少道题。它决定可评估数据的条数，必须 >= SAMPLE_SIZE，
# 否则抽样只能取到全部现有数据，「按数量抽样」这条就验不出来了。
QUESTIONS_PER_DIRECTION = int(os.environ.get("QUESTIONS_PER_DIRECTION", "3"))

PASSED = []
FAILED = []
SKIPPED = []


def record(name, ok, detail=""):
    if ok:
        PASSED.append(name)
        print(f"  PASS  {name} :: {detail}", flush=True)
    else:
        FAILED.append(name)
        print(f"  FAIL  {name} :: {detail}", flush=True)


def record_skip(name, detail=""):
    SKIPPED.append(name)
    print(f"  SKIP  {name} :: {detail}  （输入缺失，不伪造通过）", flush=True)


def sql(query, check=False):
    """在共享 postgres 上执行 SQL。只用于夹具读写与清理。"""
    result = subprocess.run(
        ["docker", "exec", "llm-postgres-1", "psql", "-U", "llm_factory",
         "-d", "llm_factory", "-t", "-A", "-c", query],
        capture_output=True, text=True, check=check,
    )
    if result.returncode != 0 and check:
        raise RuntimeError(result.stderr.strip())
    return result.stdout.strip()


def sql_scalar(query):
    """取单值查询的第一行（psql 会连带打印命令标签，如 '3\\nINSERT 0 1'）。"""
    out = sql(query)
    return out.splitlines()[0].strip() if out else ""


def clear_dedup(job_type, dataset_id):
    """清入队去重键，使测试可重复运行。

    入队走 SetNX dedup:<jobType>:<datasetID>，TTL 10 分钟；不清键时接口仍返回
    202 但 message 变成「已在队列中」，任务不会被真正执行（假入队）。
    """
    subprocess.run(
        ["docker", "exec", "llm-redis-1", "redis-cli", "del", f"dedup:{job_type}:{dataset_id}"],
        capture_output=True, check=False,
    )


class Session:
    def __init__(self, base):
        self.base = base.rstrip("/")
        self.jar = CookieJar()
        self.opener = urllib.request.build_opener(urllib.request.HTTPCookieProcessor(self.jar))

    def call(self, method, path, body=None, timeout=180):
        url = self.base + path if path.startswith("/") else path
        data = json.dumps(body).encode() if body is not None else None
        req = urllib.request.Request(url, data=data, method=method)
        if data is not None:
            req.add_header("Content-Type", "application/json")
        try:
            with self.opener.open(req, timeout=timeout) as resp:
                raw = resp.read().decode()
                return resp.status, (json.loads(raw) if raw else None)
        except urllib.error.HTTPError as exc:
            raw = exc.read().decode()
            try:
                return exc.code, (json.loads(raw) if raw else None)
            except json.JSONDecodeError:
                return exc.code, raw
        except Exception as exc:  # 连接失败也要给出可诊断信息，而不是抛栈退出
            return 0, f"{type(exc).__name__}: {exc}"

    @staticmethod
    def as_list(body):
        if isinstance(body, list):
            return body
        if isinstance(body, dict):
            for key in ("items", "data", "list", "rows", "judges", "dimensions", "runs"):
                if isinstance(body.get(key), list):
                    return body[key]
        return []


def wait_stage(session, dataset_id, stage, timeout=GEN_TIMEOUT):
    deadline = time.time() + timeout
    last = None
    while time.time() < deadline:
        code, body = session.call("GET", f"/api/v1/datasets/{dataset_id}/generation-runs")
        if code == 200:
            for run in Session.as_list(body):
                if run.get("stage") == stage:
                    last = run
                    if run.get("status") in ("completed", "failed", "partial_failed"):
                        return run
        time.sleep(5)
    return last


def poll_until(session, path, predicate, timeout=EVAL_TIMEOUT, interval=8):
    deadline = time.time() + timeout
    body = None
    while time.time() < deadline:
        code, body = session.call("GET", path)
        if code == 200 and predicate(body):
            return body
        time.sleep(interval)
    return body


def judges_for_dataset(session, dataset_id):
    """按数据集取裁判候选（含剔除标注）。

    必须用这个接口而不是 /admin/eval/judges：后者的生成者只有权威来源
    datasets.provider_id，没有数据集上下文时按设计恒返回 excluded=false，
    因此无法验证「剔除生成者自评」这条需求。
    """
    code, body = session.call("GET", f"/api/v1/datasets/{dataset_id}/eval-judges")
    if code != 200 or not isinstance(body, dict):
        return 0, []
    return body.get("generatorProviderId", 0) or 0, body.get("judges") or []


def build_dataset(session, prefix):
    """走真实 API 建数据集并跑通「问题 → SFT 思维链+答案」，产出可评估的数据。

    为什么不用裸 SQL 造数据：评估对象是「llm 生成的数据集」，用真实流水线产出的
    内容才有意义；手写假样本会让「多 LLM 互评」变成对假数据打分。
    """
    code, body = session.call("POST", "/api/v1/datasets", {
        "name": f"{prefix}dataset",
        "rootKeyword": "军事",
        "targetSize": 2,
        "providerId": GENERATOR_PROVIDER_ID,
        "targetKind": "sft",
        "directionCount": 1,
        "questionsPerDirection": QUESTIONS_PER_DIRECTION,
        # estimate 必须显式给：缺省时领域生成会回退到硬编码的 100 个领域，
        # 整个流水线会跑成小时级。
        "estimate": {"domainCount": 1, "questionsPerDomain": 1,
                     "answerVariants": 1, "rewardVariants": 1},
    })
    if code not in (200, 201) or not isinstance(body, dict) or not body.get("id"):
        return None, f"创建数据集失败 HTTP {code} {str(body)[:200]}"
    dataset_id = body["id"]

    # 领域走 legacy 同步路由：方向生成依赖领域作为 parent，没有领域会 409。
    code, body = session.call("POST", f"/api/v1/datasets/{dataset_id}/domains/generate",
                              {}, timeout=GEN_TIMEOUT)
    domains = Session.as_list((body or {}).get("domains")) if isinstance(body, dict) else []
    if code != 200 or not domains:
        return dataset_id, f"领域生成失败 HTTP {code} domains={len(domains)}"

    code, msg = enqueue(session, "directions", dataset_id,
                        f"/api/v1/datasets/{dataset_id}/directions/generate",
                        {"directionCount": 1})
    if code != 202:
        return dataset_id, f"方向生成入队失败 HTTP {code} {msg}"
    run = wait_stage(session, dataset_id, "directions")
    if not run or run.get("status") != "completed":
        return dataset_id, f"方向生成未完成 status={run and run.get('status')}"

    code, msg = enqueue(session, "questions", dataset_id,
                        f"/api/v1/datasets/{dataset_id}/questions/generate",
                        {"questionsPerDirection": QUESTIONS_PER_DIRECTION})
    if code != 202:
        return dataset_id, f"问题生成入队失败 HTTP {code} {msg}"
    questions = poll_until(
        session, f"/api/v1/datasets/{dataset_id}/questions?limit=50",
        lambda b: len(Session.as_list(b)) > 0)
    qitems = Session.as_list(questions)
    if not qitems:
        return dataset_id, "问题未落库"

    # SFT 产出「思维链 + 答案」，这是评估真正要打分的两段内容。
    code, msg = enqueue(session, "sft", dataset_id,
                        f"/api/v1/datasets/{dataset_id}/sft/generate",
                        {"includeAnswer": True})
    if code != 202:
        return dataset_id, f"SFT 生成入队失败 HTTP {code} {msg}"
    sft = poll_until(session, f"/api/v1/datasets/{dataset_id}/sft?limit=50",
                     lambda b: len(Session.as_list(b)) > 0, timeout=GEN_TIMEOUT)
    sitems = Session.as_list(sft)
    if not sitems:
        return dataset_id, "SFT 记录未落库"

    return dataset_id, (f"datasetId={dataset_id} questions={len(qitems)} sft={len(sitems)}")


def enqueue(session, job_type, dataset_id, path, body=None):
    clear_dedup(job_type, dataset_id)
    code, resp = session.call("POST", path, body if body is not None else {})
    msg = resp.get("message") if isinstance(resp, dict) else str(resp)[:120]
    return code, msg


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--base", default=os.environ.get("L15_BASE", DEFAULT_BASE))
    parser.add_argument("--keep", action="store_true",
                        help="保留夹具数据用于人工查看（默认清理）")
    args = parser.parse_args()

    prefix = f"l15-evalmulti-{os.getpid()}-"
    session = Session(args.base)
    dataset_id = None
    created_dimension_keys = []

    print(f"=== L15 多 LLM 互评端到端验证 @ {args.base} ===")
    print(f"    夹具前缀 {prefix}（结束时精确清理）\n")

    try:
        # ---------------------------------------------------------------- T1
        code, body = session.call("POST", "/api/v1/auth/login",
                                  {"email": "admin@company.com", "password": "admin123456"})
        record("T1 登录", code == 200, f"HTTP {code}")
        if code != 200:
            return finish()

        # ---------------------------------------------------------------- T2
        # 需求 1「接入多个 llm」：至少 2 个候选，且至少 2 个不是生成者（否则无从互评）。
        code, options = session.call("GET", "/api/v1/admin/eval/judges")
        opts = Session.as_list(options)
        active_opts = [o for o in opts if o.get("isActive")]
        non_generator = [o for o in active_opts if o.get("providerId") != GENERATOR_PROVIDER_ID]
        record("T2 接入多个 llm（候选 ≥ 2）", code == 200 and len(opts) >= 2,
               f"HTTP {code} candidates={len(opts)} active={len(active_opts)}")
        record("T2 存在 2 个非生成者可用裁判（可互评）", len(non_generator) >= 2,
               f"non_generator={[(o.get('providerId'), o.get('model')) for o in non_generator]}")

        if len(non_generator) < 2:
            record_skip("T3 生成者自评剔除", "缺少第二个真实 provider，无法构造互评场景")
            record_skip("T8 真实互评打分", "缺少第二个真实 provider")
            record_skip("T9 多 LLM 汇总", "缺少第二个真实 provider")
            return finish_with_dimensions(session, created_dimension_keys)

        # ---------------------------------------------------------------- 维度（需求 3）
        code, dims = session.call("GET", "/api/v1/eval/dimensions")
        dlist = Session.as_list(dims)
        builtin = [d for d in dlist if d.get("isBuiltin")]
        record("T4 内置评估维度 ≥ 50（需求：不少于 50 个）", len(builtin) >= 50,
               f"内置={len(builtin)} 总计={len(dlist)}")
        long_chain = [d for d in dlist if str(d.get("category")) == "long_chain"]
        record("T5 维度聚焦长链思考（long_chain 分类有实质数量）", len(long_chain) >= 10,
               f"long_chain={len(long_chain)}，示例={[d.get('key') for d in long_chain[:3]]}")

        # 用户可增加维度：新建 -> 可见 -> 精确删除（T6）。
        # weight 是必填（store 要求 > 0，前端 DimensionManager 默认 1），漏传会 400。
        new_dim_key = f"{prefix}dim"
        code, created = session.call("POST", "/api/v1/eval/dimensions", {
            "key": new_dim_key,
            "name": "L15 用户自定义维度",
            "category": "long_chain",
            "description": "验证用户可自行增加评估维度",
            "rubric": "1-5 分：5 分为完全满足。",
            "scaleMin": 1,
            "scaleMax": 5,
            "weight": 1,
            "isActive": True,
        })
        if code in (200, 201):
            created_dimension_keys.append(new_dim_key)
        code2, dims2 = session.call("GET", "/api/v1/eval/dimensions")
        visible = any(d.get("key") == new_dim_key for d in Session.as_list(dims2))
        record("T6 用户可新增评估维度且列表可见", code in (200, 201) and visible,
               f"create={code} visible={visible} key={new_dim_key}")
        # 新建的用户维度必须是非内置（is_builtin=FALSE），否则会污染「内置 ≥50」的口径。
        created_row = next((d for d in Session.as_list(dims2) if d.get("key") == new_dim_key), None)
        record("T6 用户维度标记为非内置（不污染内置维度口径）",
               created_row is not None and created_row.get("isBuiltin") is False,
               f"isBuiltin={created_row and created_row.get('isBuiltin')}")

        # ---------------------------------------------------------------- 夹具数据集
        dataset_id, detail = build_dataset(session, prefix)
        if not dataset_id:
            record("前置：构建可评估数据集", False, detail)
            return finish_with_dimensions(session, created_dimension_keys)
        record("前置：构建可评估数据集（真实流水线）", detail.startswith("datasetId="), detail)

        # ---------------------------------------------------------------- T3（需求 2 核心）
        generator_id, judges = judges_for_dataset(session, dataset_id)
        record("T3 按数据集获取裁判候选（含剔除标注）", len(judges) > 0,
               f"generator={generator_id} judges={len(judges)}")
        generator_judge = next((j for j in judges if j.get("providerId") == generator_id), None)
        record("T3 生成者被标为 excluded=true（需求：A1 不得由 A 评）",
               generator_judge is not None and generator_judge.get("excluded") is True,
               f"excluded={generator_judge and generator_judge.get('excluded')}")
        record("T3 剔除原因有中文说明（可追溯）",
               bool(generator_judge and generator_judge.get("excludeReason")),
               f"reason={generator_judge and generator_judge.get('excludeReason')!r}")

        usable = [j for j in judges if not j.get("excluded")]
        judge_ids = [j["providerId"] for j in usable[:2]]
        record("T3 可用裁判 ≥ 2 且都不等于生成者",
               len(judge_ids) >= 2 and generator_id not in judge_ids,
               f"usable={judge_ids} generator={generator_id}")

        # T3b（需求 2 的最强形式）：**用户显式选中生成者**时，系统必须仍把它剔除。
        # 这才是「A1 不得由 A 评」的真实含义：不能只靠「用户不会选它」来实现剔除。
        # 断言两件事：创建被拦住或落库后标 excluded；且被剔除项带中文原因。
        code_self, self_run = session.call("POST", "/api/v1/eval/runs", {
            "datasetId": dataset_id,
            "name": f"{prefix}self-judge-probe",
            "samplingMode": "count", "sampleSize": 1, "targetKind": "sft",
            "dimensionKeys": [d["key"] for d in long_chain[:1]],
            "judgeProviderIds": [generator_id],
        })
        self_probe = self_run if isinstance(self_run, dict) else {}
        if code_self in (200, 201) and self_probe.get("id"):
            # 已知缺陷路径：创建阶段就应拦住「只选了生成者」——
            # resolveRunJudges 在 usable==0 时会返回 400。若这里 200，说明
            # 创建时未拦，必须进一步要求落库标注 excluded，否则就是真缺陷。
            _, self_detail = session.call("GET", f"/api/v1/eval/runs/{self_probe['id']}")
            self_judges = (self_detail or {}).get("judges", []) if isinstance(self_detail, dict) else []
            gen_entry = next((j for j in self_judges if j.get("providerId") == generator_id), None)
            record("T3b 只选生成者时被拦住（400）或落库标注 excluded",
                   gen_entry is not None and gen_entry.get("excluded") is True,
                   f"HTTP {code_self} excluded={gen_entry and gen_entry.get('excluded')}")
        else:
            # 期望路径：创建即被 400 拦住，且错误里说明生成者禁止自评。
            record("T3b 只选生成者时创建被拒（400）且原因可读",
                   code_self == 400,
                   f"HTTP {code_self} {str(self_run)[:160]}")

        # 维度键：优先用聚焦长链思考的维度，让打分结果与需求主题相关。
        dimension_keys = [d["key"] for d in long_chain[:DIMENSION_COUNT]]
        if len(dimension_keys) < DIMENSION_COUNT:
            dimension_keys = [d["key"] for d in dlist[:DIMENSION_COUNT]]

        # ---------------------------------------------------------------- T7（需求 4）
        code_full, run_full = session.call("POST", "/api/v1/eval/runs", {
            "datasetId": dataset_id, "name": f"{prefix}full",
            "samplingMode": "full", "targetKind": "sft",
            "dimensionKeys": dimension_keys[:1], "judgeProviderIds": judge_ids[:1],
        })
        record("T7 全量模式（full）创建运行受理",
               code_full in (200, 201) and isinstance(run_full, dict),
               f"HTTP {code_full} samplingMode={run_full.get('samplingMode') if isinstance(run_full, dict) else None}")

        code_ratio, run_ratio = session.call("POST", "/api/v1/eval/runs", {
            "datasetId": dataset_id, "name": f"{prefix}ratio",
            "samplingMode": "ratio", "sampleRatio": 0.5, "targetKind": "sft",
            "dimensionKeys": dimension_keys[:1], "judgeProviderIds": judge_ids[:1],
        })
        record("T7 抽样模式（ratio 比例）创建运行受理",
               code_ratio in (200, 201) and isinstance(run_ratio, dict) and run_ratio.get("samplingMode") == "ratio",
               f"HTTP {code_ratio} sampleRatio={run_ratio.get('sampleRatio') if isinstance(run_ratio, dict) else None}")

        code_bad_ratio, bad_ratio = session.call("POST", "/api/v1/eval/runs", {
            "datasetId": dataset_id, "name": f"{prefix}bad-ratio",
            "samplingMode": "ratio", "sampleRatio": 0, "targetKind": "sft",
            "dimensionKeys": dimension_keys[:1], "judgeProviderIds": judge_ids[:1],
        })
        record("T7 ratio=0 被拒（不静默当全量）", code_bad_ratio == 400,
               f"HTTP {code_bad_ratio} {str(bad_ratio)[:100]}")

        code_bad_count, bad_count = session.call("POST", "/api/v1/eval/runs", {
            "datasetId": dataset_id, "name": f"{prefix}bad-count",
            "samplingMode": "count", "sampleSize": 0, "targetKind": "sft",
            "dimensionKeys": dimension_keys[:1], "judgeProviderIds": judge_ids[:1],
        })
        record("T7 count=0 被拒（不静默当全量）", code_bad_count == 400,
               f"HTTP {code_bad_count} {str(bad_count)[:100]}")

        # ---------------------------------------------------------------- T8+T9（需求 1/2/5 核心）
        # count 抽样 2 条：样本量最小充分（足以证明「每条都被多维度打分」+「每个 llm 都出整体分数」）。
        code, run = session.call("POST", "/api/v1/eval/runs", {
            "datasetId": dataset_id,
            "name": f"{prefix}judge-run",
            "samplingMode": "count",
            "sampleSize": SAMPLE_SIZE,
            "targetKind": "sft",
            "dimensionKeys": dimension_keys,
            "judgeProviderIds": judge_ids,
        })
        run_id = run.get("id") if isinstance(run, dict) else None
        record("T8 创建抽样评估运行（count 指定数量）",
               code in (200, 201) and bool(run_id),
               f"HTTP {code} runId={run_id} sampleSize={SAMPLE_SIZE} "
               f"judges={judge_ids} dimensions={len(dimension_keys)}")
        if not run_id:
            return finish_with_dimensions(session, created_dimension_keys)

        code, detail = session.call("GET", f"/api/v1/eval/runs/{run_id}")
        run_judges = (detail or {}).get("judges", []) if isinstance(detail, dict) else []
        run_judge_ids = {j.get("providerId") for j in run_judges}
        # 本运行只选了非生成者，因此生成者不应出现在运行裁判里。
        # 注意：不要在此断言「生成者被剔除」——它根本未被选中，excluded 列表为空
        # 才是正确行为。「选中生成者也必须被剔除」由下面的 T3b 单独验证。
        record("T8 只选非生成者时，生成者不出现在运行裁判里",
               generator_id not in run_judge_ids,
               f"run_judges={sorted(run_judge_ids)} generator={generator_id}")
        record("T8 运行裁判均为未被剔除的可用裁判",
               all(not j.get("excluded") for j in run_judges),
               f"excluded={[j.get('providerId') for j in run_judges if j.get('excluded')]}")

        clear_dedup("eval.run", dataset_id)
        code, enq = session.call("POST", f"/api/v1/eval/runs/{run_id}/start")
        msg = enq.get("message", "") if isinstance(enq, dict) else str(enq)
        record("T8 启动评估返回 202", code == 202, f"HTTP {code}")
        record("T8 入队 message 含「已入队」（识破去重抑制）", "已入队" in msg, f"message={msg!r}")
        print(f"    ... 等待真实多 LLM 打分（{SAMPLE_SIZE} 样本 × {len(dimension_keys)} 维度 "
              f"× {len(judge_ids)} 裁判 = {SAMPLE_SIZE * len(dimension_keys) * len(judge_ids)} 次 LLM 调用）",
              flush=True)
        detail = poll_until(
            session, f"/api/v1/eval/runs/{run_id}",
            lambda b: (b if isinstance(b, dict) else {}).get("run", {}).get("status")
            in ("completed", "failed", "partial_failed"),
            timeout=EVAL_TIMEOUT)
        run_final = (detail or {}).get("run", {}) if isinstance(detail, dict) else {}
        status = run_final.get("status")
        record("T8 评估运行收敛到 completed",
               status == "completed",
               f"status={status} scored={run_final.get('scoredItems')}/{run_final.get('totalItems')} "
               f"error={str(run_final.get('errorSummary'))[:160]}")

        code, scores = session.call("GET", f"/api/v1/eval/runs/{run_id}/scores")
        slist = Session.as_list(scores)
        record("T8 逐条多维打分已落库", code == 200 and len(slist) > 0, f"count={len(slist)}")

        if slist:
            scorers = {s.get("judgeProviderId") for s in slist}
            record("T8 两个 llm 均实际打分（非单裁判）",
                   len(scorers & set(judge_ids)) >= 2,
                   f"scored_by={sorted(scorers)} requested={judge_ids}")
            record("T8 每个样本都被多维度打分（>1 个维度 key）",
                   len({s.get("dimensionKey") for s in slist}) > 1,
                   f"dimensions_scored={len({s.get('dimensionKey') for s in slist})}")
            record("T8 生成者模型未参与打分（需求 2 的核心断言）",
                   generator_id not in scorers,
                   f"scored_by={sorted(scorers)} generator={generator_id}")
            record("T10 打分含真实中文理由（非纯数字）",
                   any(s.get("rationale") and any("\u4e00" <= ch <= "\u9fff" for ch in str(s.get("rationale")))
                       for s in slist),
                   f"with_rationale={sum(1 for s in slist if s.get('rationale'))}/{len(slist)}")

        # ---------------------------------------------------------------- T9（需求 5）
        code, report = session.call("GET", f"/api/v1/eval/runs/{run_id}/report")
        record("T9 报告可获取", code == 200 and isinstance(report, dict), f"HTTP {code}")
        if code == 200 and isinstance(report, dict):
            judge_stats = report.get("judges") or []
            reported_judges = {j.get("providerId") for j in judge_stats}
            record("T9 报告给出每个 llm 对该数据集的整体分数（逐裁判汇总）",
                   len(judge_stats) >= 2 and len(reported_judges & set(judge_ids)) >= 2,
                   f"judges={[(j.get('providerId'), j.get('score')) for j in judge_stats]}")
            record("T9 每个裁判确有样本被打分（整体分数有依据）",
                   all((j.get("sampleCount") or 0) > 0 for j in judge_stats) if judge_stats else False,
                   f"sampleCounts={[(j.get('providerId'), j.get('sampleCount')) for j in judge_stats]}")
            record("T9 报告含整体分数（跨 llm 汇总）",
                   report.get("overallScore") is not None,
                   f"overallScore={report.get('overallScore')}")
            record("T9 报告含多 LLM 一致性统计",
                   report.get("judgeAgreement") is not None,
                   f"judgeAgreement={report.get('judgeAgreement')}")
            # 一致性用的是 Spearman 秩相关。样本数 < 3 时它退化为 0/1（自由度不够），
            # 因此把样本数与数值一起报出来，避免读者把退化值当成「模型真的吵架了」。
            agreement = report.get("judgeAgreement")
            record(f"T9 一致性在样本数 {SAMPLE_SIZE} 下可解释（样本数 ≥ 3 才有真实分歧含义）",
                   SAMPLE_SIZE >= 3 and isinstance(agreement, (int, float)) and 0 <= agreement <= 1,
                   f"sampleSize={SAMPLE_SIZE} judgeAgreement={agreement}")
            record("T9 报告含维度级汇总（多维度统计）",
                   len(report.get("dimensions") or []) >= 1,
                   f"dimensions={len(report.get('dimensions') or [])}")
            conclusions = report.get("conclusions") or []
            record("T9 报告给出中文分析结论（得出结论）",
                   bool(conclusions) and any(
                       "\u4e00" <= ch <= "\u9fff" for ch in "".join(str(c) for c in conclusions)),
                   f"conclusions={str(conclusions)[:160]}")
            record("T9 报告未把被剔除的生成者算作裁判",
                   generator_id not in reported_judges,
                   f"reported_judges={sorted(reported_judges)} generator={generator_id}")
    finally:
        if not args.keep:
            # 精确清理：按 id 删自己的数据集（级联子表），按 key 删自己建的维度。
            # 禁止无 WHERE 的批量删除（契约 §6.2）。
            if dataset_id:
                sql(f"DELETE FROM datasets WHERE id = {dataset_id};")
            for key in created_dimension_keys:
                sql(f"DELETE FROM eval_dimensions WHERE key = '{key}';")
            print(f"\n[清理] dataset_id={dataset_id} 维度={created_dimension_keys}（精确条件）")
        else:
            print(f"\n[保留] dataset_id={dataset_id}（--keep，供人工查看）")

    return finish_with_dimensions(session, created_dimension_keys)


def finish_with_dimensions(session, created_dimension_keys):
    for key in created_dimension_keys:
        sql(f"DELETE FROM eval_dimensions WHERE key = '{key}';")
    return finish()


def finish():
    print(f"\n=== 合计 {len(PASSED) + len(FAILED) + len(SKIPPED)} 项："
          f"通过 {len(PASSED)}，失败 {len(FAILED)}，跳过 {len(SKIPPED)} ===")
    if FAILED:
        print("失败项：" + "; ".join(FAILED))
    if SKIPPED:
        print("跳过项（输入缺失，非伪造通过）：" + "; ".join(SKIPPED))
    return 1 if FAILED else 0


if __name__ == "__main__":
    sys.exit(main())
