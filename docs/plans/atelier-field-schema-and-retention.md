# Atelier 字段 schema、版本语义与数据保留策略（T34 交付物）

> 本文件是 Issue [#160](https://github.com/1420970597/llm/issues/160) 的任务 **T34**
> 要求的文档之一：「文档含模型/费用未知边界、操作/恢复/迁移 runbook、字段 schema、
> 版本与数据保留策略」。
>
> 配套阅读：[`atelier-api-contract.md`](atelier-api-contract.md)（端点与信封）、
> [`studio-rollout-runbook.md`](studio-rollout-runbook.md)（灰度/回滚/演练）、
> [`legacy-migration-report.md`](legacy-migration-report.md)（迁移盘点与导入）。
>
> 本文件描述**当前实现**的字段与保留语义；发现不一致时以代码与迁移为准，
> 并把差异补回本文件（「文档与实现不一致」本身就是一个缺陷）。

---

## 1. 样本 payload schema（§2.2）

样本版本（`sample_versions.payload`）按项目 `target_kind` 二选一，`schema_version` 随类型固定。

### 1.1 SFT（`sft.sample.v1`）

| 字段 | 类型 | 必填 | 说明 |
|---|---|---|---|
| `question` | string | 是 | 问题内容 |
| `reasoning` | string | 是 | 推理过程。**旧名 `chainOfThought` 不再是新契约字段**，导入时显式映射 |
| `answer` | string | 是 | 答案 |

### 1.2 GRPO（`grpo.sample.v1`）

| 字段 | 类型 | 必填 | 说明 |
|---|---|---|---|
| `question` | string | 是 | 问题内容 |
| `judgePrompt` | string | 是 | 裁判提示词（教师模板产物） |
| `levels` | string[] | 是 | 至少 2 档、不重复、非空。**必须保留数组结构** |
| `levelRubrics` | object[] | 是 | 与 `levels` **一一对应**；每项 `{level, criteria, acceptCase, rejectCase}`，`criteria` 非空，且至少一个边界例 |
| `frameworkRef` | string | 否 | 判据的框架来源（provenance） |

GRPO payload **不含** `answer`/`reasoning`/`chainOfThought`/`rewardScore`：
把判据转写成 SFT 答案（或用奖励分数冒充教师材料）会让两种训练数据混在一起，
而下游无法区分。

### 1.3 旧数据的字段来源

| 旧表 | 映射到 | 说明 |
|---|---|---|
| `questions.content` | `question` | 直接对应 |
| `sft_records.chain_of_thought` | `reasoning` | 显式改名 |
| `sft_records.answer` | `answer` | 直接对应 |
| `reasoning_records.*` | **不映射** | 保留为独立来源（不与 SFT 合并） |
| `reward_records.score` | **不映射** | 奖励信号不是判分标准 |
| `grpo_prompts.level_rubrics` | **不映射**（本轮） | 旧结构与本轮 schema 不同，需人工确认后重建 |

## 2. 导出文件 schema

| 格式 | 内容 |
|---|---|
| SFT JSONL | 每行一个对象，字段由**冻结的映射版本**决定；`reasoning` 为契约字段名 |
| SFT CSV / Alpaca | 同 SFT，列/字段由映射决定 |
| GRPO JSONL | 每行**必须**是 `question` / `judge_prompt` / `levels`（数组）/ `level_rubrics`（对象数组）；服务端逐行解码校验 |

导出文件（`release_artifacts`）不可变：对象路径为
`releases/{releaseId}/r{revision}/export-{format}-{hash前16位}.jsonl`，
路径含内容 hash、不含 `latest`。发布后修改蓝图/映射/标准、隔离样本或切换默认存储
都**不会**改变已发布文件的字节（凭证可从当前配置取，endpoint/bucket 用制品记录里的固化值）。

## 3. 版本语义

| 版本 | 载体 | 递增时机 | 可变性 |
|---|---|---|---|
| 文档版本（蓝图/覆盖/标准/质量策略/映射） | `document_versions` | 每次保存新版本 | 只追加；`expectedRevision` 不匹配返回 409 |
| 样本内容版本 | `sample_versions` | 每次写入新内容 | 只追加，无 UPDATE 路径；`(sample_id, version)` 唯一 |
| 批次的输入快照 | `batches.*_version_id` 与 `*_content_hash` | 创建批次时冻结 | 批次创建后不变；改配置必须新建批次 |
| `reviewer_revision` | `review_decisions` | 每个审阅者每次提交 | 只追加；更正通过 `supersedes` |
| `aggregate_review_revision` | `review_projections` | 有效判断变化 | 投影（可从 decisions 重建） |
| `evidence_revision` | 质量检查周期 | 实验补齐/新风险/策略修订 | 递增后旧接纳回到待判断 |
| 发布候选 revision | `release_candidates` | 每次修订候选 | 候选可修订；`release_id` 不变 |
| 制品/manifest hash | `release_manifests` 与 `release_artifacts` | 发布构建时 | 已确认后不可变；同 hash 幂等回放 |

**发布名与发布 ID 分离**：`releases.id` 是稳定身份（URL/下载/审计只用它）；
`release_name` 是项目内唯一可读名（规范化键比较，禁止 `latest`）。

## 4. 数据保留与删除策略

| 对象 | 删除/保留语义 |
|---|---|
| 已发布文件（对象存储） | 普通写路径**禁止**覆盖或删除；受控清理另行走独立流程并留审计 |
| `release_artifacts` / `release_manifests` | 发布确认后不可变；下载前按 `artifact_hash` 校验字节，不符即拒绝并提示受控重建（**不回退导出最新**） |
| `sample_versions` | 只追加；被实验引用的版本有 `ON DELETE RESTRICT` 保护（删它会静默缩小历史实验的分母） |
| `experiment_items` | `sample_version_id` 为 RESTRICT；实验分母 = 创建时冻结的行数，不随筛选/隔离变化 |
| `rule_evidence` | `sample_version_id` 与 `quality_policy_version_id` 为 RESTRICT（证据与其引用的版本不得被静默删除） |
| 项目归档 | 只阻止新运行；**不删**批次或发布 |
| 旧库源数据 | 迁移工具**不删除**任何源数据（T30/T31 的写入路径只有只追加的样本版本） |
| 数据库迁移 | 只做加法，**不 down-migrate 删表**；回退不回滚 schema |
| 审计日志 | 变更与审计在同一事务或 outbox；审计不含密钥与样本正文 |
| 评论/判断 | 只追加；编辑保留修订（不覆盖历史） |

## 5. 模型与费用的**未知边界**（必须如实展示，不得补 0）

| 情形 | 系统行为 |
|---|---|
| 供应商未回传 usage | `usage_ledger.usage_source = unknown`，token 字段为 NULL（不是 0） |
| 超时/断连但供应商可能已收费 | `amount_state = unknown`，按**当时预留的金额**占用额度（`uncertain_minor`），不是 0 |
| 有价格与估算用量 | `amount_state = estimated`，计入 `uncertain_minor`，**不**计入 `settled_minor` |
| 有精确金额 | `amount_state = actual`，计入 `settled_minor` |
| 无法与供应商账单核对 | `reconciliation = impossible`（供应商不提供明细或无请求 ID），是**必须存在**的终态 |
| 预算不足或无法可靠预留 | 阻止新的自动执行，要求补齐价格/上限；返回 429 + `BUDGET_EXHAUSTED` |
| 服务端不可用 | `DEPENDENCY_UNAVAILABLE`（503，可重试） |

系统**不承诺**：外部 LLM 恰好调用一次、只计费一次、在途费用绝不超预算。
它承诺的是：不把未知记成 0、不伪造精确成本、并让未知在运维快照里可见
（`GET /api/v1/studio/health` 的 `usage.unknownAmount24h` 与 `notes[]`）。

## 6. 质量结论的**置信边界**

- 分母 = 实验创建时冻结的样本版本数（`experiment_items` 行数），**不随筛选/隔离变化**。
- `missing`（缺分）/ `error`（裁判出错）/ `not_applicable`（不适用）与真实 0 分**区分**；
  缺分不参与均值，也不补 0。
- 零分母显示「无结论」，**不是** 100%。
- 接纳率 = 接纳数 / 纳入检查数；待审阅不算接纳。
- 抽样结论必须标示范围与覆盖（报告给出 `coverageDisplay`）。
- 裁判独立性以 **endpoint 指纹**判定；指纹缺失时保守判为**不独立**。
- 发布范围可缩小，但数据卡保留原范围指标、排除数量与覆盖损失。
