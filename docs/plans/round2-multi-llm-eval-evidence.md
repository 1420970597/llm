# 多 LLM 互评需求：真实端到端证据（父代理独立复核）

需求来源：`功能说明.txt`

> 还需要增加已生成好的数据集评估功能，采用多llm互评，首先需要接入多个llm，假设存在A、B、C三个llm，
> 存在由A生成的数据集A1，评估A1则需要除去A之外的llm进行多维度评估，评估维度工具内需要内置不少于
> 50个维度（聚焦长链思考），也可以由用户增加；评估的数据集采用"全量"和"抽样"评估……由llm将每一条
> 数据进行多维度评估打分后汇总对应该评估llm对该被评估数据集的整体分数，再汇总其他llm对该被评估
> 数据的整体分数，进行统计、分析、展示，得出结论。

## 为什么这条需求此前"从未被真正验证过"

环境原本只有 **1 个 provider**（`id=1 deepseek-v4.1-flash`），而它正是数据集的生成者。
按「生成者禁止自评」规则它必须被剔除，于是可用裁判数为 0，评估根本跑不起来。
既有验收脚本 `test/test_acceptance_7requirements.py` 因此对该需求只能记 **SKIP（输入缺失）** ——
这符合契约 §3.5 的诚实要求，但意味着需求条文里的 **「接入多个llm」「除去A之外的llm」「再汇总其他
llm的整体分数」三条从未被验证过**。

## 父代理的处置：补齐真实输入，把 SKIP 变成证据

### 1. 核实同一端点确实提供其他真实可用模型

```bash
cd /root/llm && set -a && . ./.env && set +a
curl -s "$APP_BOOTSTRAP_PROVIDER_BASE_URL/models" -H "Authorization: Bearer $APP_BOOTSTRAP_PROVIDER_API_KEY"
# 实测可用的两个（直接调 chat/completions 验证）：
#   gpt-5.6-sol  -> '可用'
#   global:hy3   -> '可用'
#   gpt-5.4-mini -> upstream_error（上游暂不可用，未采用）
```text

### 2. 引导第二、第三个真实 provider（均 `is_active=true`）

```text
id=1   deepseek-v4.1-flash   model=global:deepseek-v4.1-flash   <- 作为生成者
id=7   gpt-5.6-sol-judge     model=gpt-5.6-sol
id=11  hy3-judge             model=global:hy3
```text

`GET /api/v1/admin/eval/judges` 现返回 3 个候选，`excluded` 均为 false。

> **注意（既有脚本已记录的陷阱）**：剔除生成者自评必须用**带数据集上下文**的
> `GET /api/v1/datasets/{id}/eval-judges`。`/api/v1/admin/eval/judges` 没有数据集上下文，
> 按设计恒返回 `excluded=false`。

### 3. 内置维度数量（需求要求「不少于 50 个」）

```bash
docker exec llm-postgres-1 psql -U llm_factory -d llm_factory -tAc \
  "select category, count(*) from eval_dimensions where is_builtin group by category order by 2 desc"
# long_chain=12, faithfulness=9, answer_quality=8, domain_fit=8, robustness=8, instruction=7, efficiency=6
# 合计 58 个内置维度（>= 50，满足；其中聚焦长链思考的 long_chain 类 12 个）
```text

## 独立复核证据（父代理直查数据库，非读报告）

`eval_run_id=8`（`verify-7req-multi-judge`），数据集 id=77，真实流水线产物。

### 两个不同裁判各自独立打分

```bash
docker exec llm-postgres-1 psql -U llm_factory -d llm_factory -tAc \
  "select judge_provider_id, count(*) as scores, round(avg(score)::numeric,3) as avg_score
   from eval_item_scores where eval_run_id=8 group by 1 order by 1"
```text

```text
7|6|4.000      <- gpt-5.6-sol-judge 打了 6 条
11|6|4.333     <- hy3-judge         打了 6 条
```text

**这是需求「除去A之外的llm进行多维度评估」+「再汇总其他llm对该被评估数据的整体分数」的直接证据**：
生成者 `provider=1` **没有**出现在打分里；两个非生成者模型各自出了 6 条分数。

### 汇总表覆盖多种粒度（需求「进行统计、分析、展示」）

```bash
docker exec llm-postgres-1 psql -U llm_factory -d llm_factory -tAc \
  "select scope, count(*) from eval_summaries where eval_run_id=8 group by 1"
```text

```text
dimension|3    <- 维度级汇总
item|2         <- 逐条汇总
judge|2        <- 逐裁判汇总（每个 llm 对该数据集的整体分数）
overall|1      <- 跨 llm 总汇总
```text

### 运行状态与理由文本

```text
eval_runs: id=8 status=completed scored_items=2 total_items=2
eval_item_scores: 12 条全部带 rationale（非纯数字，需求「多维度评估打分」）
报告 GET /eval/runs/8/report 返回真实中文结论（非模板占位）：
  「整体质量良好（归一化得分 80/100），但存在明显短板维度…最弱维度是「表达精炼度」…」
```text

## 结论

| 需求条文 | 判定 | 证据 |
|---|---|---|
| 接入多个 llm | **满足** | 3 个真实 provider，均 `is_active=true`，`/admin/eval/judges` 返回 3 候选 |
| 由 A 生成的数据集必须除去 A | **满足** | `provider=1`（生成者）`excluded=true`，原因「生成者模型，禁止自评」；打分表里无 provider 1 |
| 内置不少于 50 个维度（聚焦长链思考） | **满足并超出** | 58 个内置维度，`long_chain` 类 12 个 |
| 用户可增加维度 | **满足** | 新增维度 `create=200` → 列表可见 → `isBuiltin=false`（不污染内置口径） |
| 全量 / 抽样两种评估 | **满足** | `full` / `ratio` / `count` 三种均受理；`ratio=0`、`count=0` 被拒（400，不静默当全量） |
| 多维度打分 → 逐 llm 整体分数 → 跨 llm 汇总 | **满足** | 2 裁判 × 6 条分数；`eval_summaries` 覆盖 dimension/item/judge/overall 四粒度 |
| 统计、分析、展示、得出结论 | **满足** | 报告含中文分析结论；`judgeAgreement` 有说明 |

### 残留限制（如实记录）

`judgeAgreement = -1` 且报告说明「某一侧打分无变化，秩相关分母为 0，无法计算」。
这是**样本量过小（2 条数据）**的数学结果，不是缺陷 —— 报告本身已正确标注
「仅 2 条数据参与评分，样本量偏小」。若要拿到有意义的一致性指标，需要更大样本，
本轮验收的样本量以「足以支撑结论的最小量」为准（契约 §3 的时间约束）。

## 复现方式

```bash
# 1. 起候选容器（api :18170 + worker，私有队列）
cd /root/llm && git checkout lane/eval-multi-judge
bash scripts/l15-evalmulti-stack.sh
# 2. 跑多 LLM 互评端到端验证（T1–T10，含上述全部断言）
python3 test/l15_eval_multi_judge.py
```text

> **前置**：环境需有 >= 2 个非生成者的真实可用 provider。缺失时脚本按契约 §3.5
> 记「输入缺失」并如实报告，**不伪造通过**。
