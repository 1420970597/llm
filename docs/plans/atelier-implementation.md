# Atelier 实施契约与追踪表（T01）

> 本文件是 Issue [#160](https://github.com/1420970597/llm/issues/160) 的任务 **T01** 交付物，
> 把讨论 [#159](https://github.com/1420970597/llm/discussions/159) 的产品目标冻结成
> 「#159 画面 → 路由 → 命令 → 数据 → 测试 → 任务号」的可核对追踪表。
>
> 冻结版本：`v1.0`。基线提交 `ef14a27`（`main`）。后续任务如因 `main` 更新改变实现方式，
> 必须在本文件的「基线差异」一节追加记录，**不得静默改变产品目标**。
>
> 本文件只定义契约，不声称任何功能已实现。34 个任务全部完成后才能关闭 #160。

---

## 0. 与既有文档的关系

| 文档 | 关系 |
|---|---|
| [#159 讨论](https://github.com/1420970597/llm/discussions/159) | 产品目标来源。本文件是它的实施追踪表，不是它的替代。 |
| `docs/design/2026-09-21-data-studio/` | #159 设计材料，已以文档 PR 纳入 `main`，作为**历史设计依据**保留。 |
| `docs/design/2026-09-20-console-rebuild/` | 前一版（#155/#157）设计材料，保留为历史，**不是本轮目标**。 |
| `docs/plans/eval-and-cleaning-plan.md` | 第一轮 lane 契约（L1–L15），**已冻结**，本轮不修改其内容。冲突见第 7 节。 |
| `docs/plans/issue-remediation-plan.md` | 第二轮（R1–R13）契约，**已冻结**，本轮不修改其内容。 |
| `docs/plans/atelier-api-contract.md` | 本文件的 API schema 附件。 |
| `todo.md` | 旧 TO C 任务中心叙事。T01 已在其中标明主线切换与旧任务状态，历史记录不删除。 |

设计材料入库位置：`docs/design/2026-09-21-data-studio/`（`01-research.md` ~ `06-atlas.md`、
`prototype/`、`assets/`）。其中的 `prototype/` 是**只读评审原型**，不参与生产构建，
也不进入生产菜单（见第 3 节 `/catalog`）。

---

## 1. 基线与差异记录

代码基线：`ef14a279d459d01006f075354580b4f793c41b69`（`main`，核查日期 2026-09-21）。

| 现有代码 | 已具备 | #159 所需变化 | 归属任务 |
|---|---|---|---|
| `apps/web-user/src/App.tsx` | React Router、Semi UI、Lucide、任务详情与阶段入口 | 拆全局壳/项目壳/独立页面；项目、批次、实验、样本、版本、筛选进 URL | T09 |
| `internal/model/dataset.go`、`apps/api/datasets.go` | n/m/x 参数、SFT/GRPO 类型、模型/存储引用、计划估算 | Dataset ≠ Project/Batch/Release；建项目草稿不因缺模型/存储失败、不调用模型 | T02、T10 |
| `apps/api/routes.go`、`apps/worker/registry.go` | `RegisterRoutes`、`RegisterJobHandler` 扩展点 | 新增项目 API 与新任务类型，保留旧契约适配 | T06、T08 |
| `internal/store/chain_standard_store.go` | 标准版本历史与版本事务思路 | 蓝图/覆盖/标准/质量/映射五类版本统一乐观锁模型 | T04 |
| `internal/store/sft_store.go`、`internal/store/grpo_store.go` | SFT/GRPO 独立表与现成生成器 | 按 `(dataset_id, question_id)` upsert 会覆盖历史；新路径必须追加内容版本 | T05、T12、T23 |
| `internal/store/generation_run_store.go`、`sql/migrations/0020_generation_runs_active_unique.sql` | 游标、尝试次数、同 dataset/stage 活跃唯一 | 唯一性不是租约；新作业必须带 batch/job/item/attempt ID | T06 |
| `apps/worker/main.go` | Redis BRPOP、有限重试、启动补入队 | 事务 outbox、DB 作业状态、租约/心跳/过期回收、幂等提交 | T06 |
| `apps/worker/job_eval.go`、`internal/store/eval_store_items.go`、`apps/web-user/src/views/EvaluationView.tsx` | 独立裁判选择、抽样、条目 payload、分数与汇总 | 实验入队前冻结内容/配置；新增 GRPO 适配、缺分语义、同基准 A/B | T14、T18、T19、T24 |
| `apps/worker/job_cleaning.go`、`internal/store/cleaning_store_runs.go` | 规则、关键词、命中明细、报告 | 拆「只读预览 / 不可变证据 / 人工 Decision」；规则命中不替人工处置 | T15 |
| `apps/worker/job_export_multi.go`、`internal/model/artifact.go` | JSONL/CSV/Alpaca/ShareGPT 编码器、映射、下载链路 | 新增候选、冻结清单、持久化导出任务、hash、按 release ID 下载 | T20、T21、T22 |
| `internal/exporter/exporter.go`、`internal/exporter/mapping.go` | GRPO 的 `judge_prompt`、`reward_levels` 部分支持 | 无 `level_rubrics`；`reward_levels` 被映射为逗号字符串 | T25 |
| `internal/exporter/parquet.go` | 注册了 `parquet` 名称 | `IsRealParquet=false`，实为列式 JSONL；本轮只承诺 SFT JSONL/CSV/Alpaca 与 GRPO JSONL | T21、T25 |
| `apps/api/auth.go` | 登录会话、admin 前缀角色检查 | 不等于项目级授权；新增 membership、对象归属校验、撤权即时生效 | T03 |
| `.github/workflows/ci.yml`、`internal/store/generation_run_store_test.go` | Go 测试、构建、文档校验、8 个 L15 UI 守卫、Compose 健康检查 | 新增真实 DB/浏览器门禁；迁移检查不能只 `\dt` | T32 |

### 1.1 基线差异追加区

（后续任务如发现 `main` 更新导致实现方式变化，在此逐条追加：日期、提交、差异、处置。）

| 日期 | 任务 | 差异 | 处置 |
|---|---|---|---|
| 2026-09-21 | T04 | §6.3 把 typed 文档 schema 的落点写作 `internal/model/pipeline_v2.go`，但该文件已由第一轮冻结契约（L1–L15，见 `docs/plans/eval-and-cleaning-plan.md`）占用，包含 `ChainStep`/`ChainStandard`/`GrpoPrompt`/`GrpoLevelRubric`/`SftRecord`/`ExportMapping`/`ExportFormatList`/`GenerationRun` 等在用类型 | 保留 §6.3 指定的**语义落点**（同一 `internal/model` 包的 typed 文档 schema），物理落到新文件 `internal/model/studio_docs.go`；`pipeline_v2.go` 只做零改动。GRPO 判据**复用**既有 `GrpoLevelRubric`（它已承载判据文本 + 接受/拒绝边界例），不新定义同概念类型。原因：混在一起会让两轮契约无法辨认，且本轮 diff 无法与旧类型分开审阅 |
| 2026-09-21 | T04 | §6.3 把文档读写的落点写作「版本 store/service」，未指定文件名 | 落到 `internal/store/document_store.go`（文档版本）与 `apps/api/routes_documents.go`（命令与读模型），均为新文件。§6.3 指定的 `internal/model/pipeline_v2.go` 保持不动 |
| 2026-09-21 | T04 | `ExportFormatList`/`canonicalFormats` 已含 `parquet`，而 §2.2 本轮只承诺 SFT JSONL/CSV/Alpaca 与 GRPO JSONL | 新增文档允许集（`model.KnownExportFormats`）**有意排除 parquet**：`internal/exporter/parquet.go` 的 `IsRealParquet=false`，实为列式 JSONL，允许它出现在新蓝图/映射里即构成假承诺。旧格式清单不动（兼容读路径） |
| 2026-09-21 | T06 | §6.3 把 worker 侧落点写作 `apps/worker/registry.go`，暗示复用现有 `RegisterJobHandler` | **不改动** `registry.go`（属于第一轮冻结契约）。新建 `apps/worker/studio_jobs.go` 内的 `RegisterStudioJobHandler`：理由是第一轮的 `jobHandler` 签名固定为 `func(ctx, *jobContext, jobPayload) error`，既拿不到 `model.Job`（租约/attempt/fencing token），也无法返回要写回 `jobs.payload` 的结果摘要；改它的签名会让所有旧 lane handler 一同重编。两套注册表共存的代价由「`studio.` 前缀 + 两条独立队列」限定在可辨认范围内 |
| 2026-09-21 | T06 | 契约未规定 Studio 队列名与单进程并发度的配置项 | 新增 `WORKER_STUDIO_QUEUE_NAME`（默认 `WORKER_QUEUE_NAME` + `-studio`）与 `WORKER_STUDIO_CONCURRENCY`（默认 2，上限 8）。原因为「旧消费者不得误吞新消息」需要队列名分离作为结构性事实，而串行会让一个长批次占满 worker；并发度只控制「同时几个批次在跑」，批次内并发仍由该批次的 `generation_config.concurrency` 决定 |
| 2026-09-21 | T07 | §2.4 四态费用列出后没有定义「与供应商账单核对」的状态（#160 T07 的原文把该项归给 T01） | 补齐并冻结在本节：`reconciliation ∈ {not_attempted, pending, confirmed, mismatch, impossible}`，存于 `usage_ledger.reconciliation`。其中 `impossible`（供应商不提供明细，例如超时后连请求 ID 都没有）是**必须存在**的终态 —— 把无法核对硬标成 `confirmed` 会让对账报告失去意义。同时固定两条派生规则：无用量或无价格时，结算自动标为 `impossible` 并写明原因；已验证未产生费用的释放自动标为 `confirmed` |
| 2026-09-21 | T07 | §4.1 的 Usage / Budget 行只列出 `usage_ledger` 与 `budget_reservations`，未规定粒度 | `budget_reservations` 落实为**每个 (project, currency) 一行**的预留台账（`limit/reserved/settled/uncertain` 四个整数计数器），逐请求事实在 `usage_ledger`（每个逻辑请求一行，`UNIQUE(idempotency_key)`）。不同粒度的原因：「不超卖」必须在一个可串行化的点上完成，而「按请求行做 SUM 聚合」在 10 万单元批次下是 O(n²) 且仍需额外的串行化点。**行为未变**：仍然在提交外部请求前事务预留、完成后结算/释放 |
| 2026-09-21 | T07 | §2.4 未定义「未知费用占用多少额度」 | 冻结为：`unknown` 按**当时预留的金额**占用（`budget_reservations.uncertain_minor`），不是 0。理由：预留是我们愿意为这次请求付出的上界，而超时/断连时供应商可能已收费；记 0 等于宣称「确定没花钱」，会让用户看到剩余额度却已被扣款。`estimated`（有价格有估算用量）同样计入 `uncertain_minor` 而**不**计入 `settled_minor` —— 后者只放精确值，否则「估计」会被当成账单凭证 |
| 2026-09-21 | T07 | §5 要求「模型参数默认取能力声明」但没有说能力声明存在哪里 | 新增表 `model_capabilities`（每个连接+模型一行：temperature/reasoning_effort/结构化输出/JSON 模式/token 上限）。代码侧 `internal/llm/capabilities.go` 提供按模型家族的内置**保守**默认（已知拒绝 temperature 的家族一律声明不支持），仅在数据库无声明时使用，且用 `ModelCapabilities.Source` 标明来源（`declaration` / `builtin-default`）。内置默认**不猜** token 上限（0=未声明）：猜小了会把合法配置误拒，猜大了等于没校验 |
| 2026-09-21 | T07 | §4.1 的 Batch 对象没有预算占用字段，但 §6.1 的批次创建命令接受自带 `budget` | 迁移 0027 给 `batches` 增加 `budget_reserved_minor/budget_settled_minor/budget_uncertain_minor` 三列，使「本批还剩多少」不必聚合 `usage_ledger`。批次上限与项目上限**两层都拦**，生效上限取二者中更严格者（`model.EffectiveLimitMinor`）；币种不一致返回字段级配置错误而不是 `ErrBudgetExhausted`，避免用户去加预算却修不好 |
| 2026-09-21 | T08 | §1.2 的错误响应是**嵌套**形状（`{"error": {...}}`），而 T02 实现成了扁平（顶层直接是 `code/message`） | 对齐契约：`writeAPIError` 改为写 `{"error": {...}}`，错误体类型收敛到 `studio.ErrorBody`，并加断言「顶层只有 error 一个键」。前端拦截器同时支持两种形状（旧端点仍是 `{"error": "文案"}` 字符串），契约 §7 的过渡期要求两者并存。变更理由：扁平形状下前端只能把 `data.error` 当字符串，新契约的 `code/fieldErrors/blockers` 全部拿不到，而 §6 明确要求「TS 类型与本节 schema 一致」|
| 2026-09-21 | T08 | §6.3 把契约层落点写作 `internal/studio/`，但未规定它与 `apps/api` 的分工 | 确立分工：`internal/studio` **不引用 `net/http`**，只定义信封/错误码/游标分页/能力位/读模型与命令层（授权、幂等）；HTTP 状态码由 handler 用 `studio.StatusFor` 做一次集中映射。理由：一个契约错误在不同资源下对应不同状态码（「不是成员」必须是资源隐藏型 404 而不是 403），而这一决定属于 HTTP 层；且 service 可以被 worker 与测试直接使用 |
| 2026-09-21 | T08 | §3 列出 `GET P/samples?status=&risk=`，但两者的判据来自人工判断与证据（T16/T17 才建表） | T08 **显式拒绝**这两个筛选（422 + 字段错误，点名负责的任务），而不是接受后忽略。理由：「传了筛选但没生效」会让用户以为自己看到的是筛过的结果，而列表看起来很正常 —— 那类错误在界面上完全不可见。同一原则适用于 T14/T20 才有的 experiments/releases 端点：它们不在 T08 先建一个空壳，而在各自任务里按同一契约**增量**交付（T08 验收项原文）|

---

## 2. 必须先定清的固定口径

这些口径是后续所有任务与测试的共同基准，任何任务不得自行另立一套。

### 2.1 n / m / x 与统计口径

| 符号 | 含义 |
|---|---|
| `n` | 领域数（domains） |
| `m` | 每领域方向数（directions per domain） |
| `x` | 每方向问题数（questions per direction） |

- `n × m × x` 是**计划问题数**（`plannedQuestions`），**不是**「当前已产出」。
- 实际产出、结构校验通过、纳入检查、已打分、已接纳、已隔离**分别统计**，不得相互冒充。
- **禁止**沿用旧 `PlanEstimate.AnswerVariants` / `RewardVariants` 偷乘目标量。
  旧字段仅作为历史兼容读模型存在，不参与新统计。

### 2.2 SFT / GRPO 样本版本字段

样本版本 payload 按项目 `target_kind` 二选一，`schema_version` 随类型固定：

| target_kind | schema_version | 必填字段 |
|---|---|---|
| `sft` | `sft.sample.v1` | `question`、`reasoning`、`answer` |
| `grpo` | `grpo.sample.v1` | `question`、`judge_prompt`、`levels`（字符串数组，至少 2 档、不重复）、`level_rubrics`（对象数组，与 `levels` 一一对应） |

- `levels` 与 `level_rubrics` **必须保留数组/对象结构**，禁止 `strings.Join` 成逗号字符串。
- 旧 `chainOfThought` 命名不再作为新契约字段；SFT 新路径统一用 `reasoning`。
- `level_rubrics` 每档至少包含判据文本与边界例，缺档、重复档、空规则、坏 JSON 均为校验失败。

### 2.3 质量分母与接纳率

- 接纳率 = **接纳数 / 纳入质量检查的样本版本数**（`accepted / inspected`）。
- 「纳入检查」由实验创建时冻结的 `sample_version` 列表定义，**不随之后筛选变化**。
- 待审阅**不算**接纳；隔离**不缩小**分母。
- 零分母显示「无结论」，**不是** 100%。
- 抽样结论必须标示范围与覆盖，不得声称全量通过。
- 发布范围可缩小，但数据卡**同时保留**原范围指标、排除数量与覆盖损失。

### 2.4 预算币种与精度

- 币种：`CNY`（默认，显式存入 `currency` 字段，不隐式假设）。
- 精度：**整数最小货币单位**（分，1 元 = 100 分），DB 列 `BIGINT`，Go 类型 `int64`。
- **禁止**用浮点表示金额；价格版本（`price_version`）与价格表单独版本化。
- 费用分四态：`estimated`（估计）、`actual`（实际）、`unknown`（未知）、`reserved`（预留）。
  超时但供应商可能已收费时记为 `unknown`，**不得直接记 0**。
- 与供应商账单核对的状态：`not_attempted` / `pending` / `confirmed` / `mismatch` / `impossible`
  五个取值，存于 `usage_ledger.reconciliation`。定义与派生规则见第 1.1 节 T07 行。
- 无法可靠预留时阻止新的自动执行，并要求补齐价格/上限。

### 2.5 发布名与发布 ID

| 概念 | 字段 | 用途 |
|---|---|---|
| 发布 ID | `release_id` | 稳定身份。URL、下载、API 路径、审计**只用它**。 |
| 发布版本名 | `release_name` | 项目内唯一的可读版本名（如 `v1.2`）。仅展示与引用。 |

- 创建候选时**同一事务**内分配 `candidateId` + `releaseId` 并预留项目内唯一 `release_name`。
- 修订候选与最终发布**沿用同一 `releaseId`**；发布失败重试不换身份。
- 下载路径禁止 `latest` 回退；文件名含类型与版本名，但不以 `latest` 命名。

### 2.6 统计单位与状态

- 批次状态：`queued` / `running` / `pause_requested` / `paused` / `partial_failed` / `completed` / `failed`。
- 恢复是**同一批次的新 attempt**，配置不变；改模型/标准必须新建批次。
- 阶段进度**按单位分别展示**，不让题数冒充方向数。
- 实验状态与批次状态**独立**；`missing` / `error` / `not_applicable` 与真实 0 分区分。
- 发布状态：`candidate` / `blocked` → `building` → `published` 或 `build_failed`。

---

## 3. 路由清单

正式路由使用真实 ID（`projectId`、`batchId`、`sampleId`、`experimentId`、`releaseId`），
原型中的 `aurora`、`b18`、`e9`、`v1.2` **不是**数据库主键。

| 屏幕 | 正式路由 | 归属任务 |
|---|---|---|
| W01 | `/today` | T10、T27 |
| W02 | `/projects` | T10 |
| W03 | `/new` | T10 |
| W04 | `/new/coverage` | T10 |
| W05 | `/new/quality` | T10 |
| P01 | `/p/:projectId/overview` | T10 |
| P02 | `/p/:projectId/blueprint` | T04、T11 |
| P03 | `/p/:projectId/coverage` | T04、T11 |
| P04 | `/p/:projectId/standard` | T04、T11 |
| P05 | `/p/:projectId/pilot` | T13 |
| P06 | `/p/:projectId/compare` | T18 |
| R01 | `/p/:projectId/runs` | T13 |
| R02 | `/p/:projectId/runs/:batchId` | T13 |
| R03 | `/p/:projectId/runs/:batchId/failures` | T13 |
| R04 | `/p/:projectId/runs/:batchId`（purpose=pilot，同 R02 路由） | T13 |
| R05 | `/p/:projectId/runs/new` | T13 |
| R06 | `/p/:projectId/runs/:batchId`（purpose=scale，同 R02 路由） | T13 |
| D01 | `/p/:projectId/data` | T17 |
| D02 | `/p/:projectId/data/:sampleId` | T17 |
| D03 | `/p/:projectId/data/:sampleId/history` | T17 |
| Q01 | `/p/:projectId/quality` | T19 |
| Q02 | `/p/:projectId/quality/new` | T19 |
| Q03 | `/p/:projectId/quality/:experimentId` | T19 |
| Q04 | `/p/:projectId/review` | T17、T19 |
| Q05 | `/p/:projectId/rules` | T15、T19 |
| L01 | `/p/:projectId/releases` | T22 |
| L02 | `/p/:projectId/releases/new` | T22 |
| L03 | `/p/:projectId/releases/:releaseId` | T20、T22 |
| G01 | `/p/:projectId/blueprint`（target_kind=grpo） | T23 |
| G02 | `/p/:projectId/data/:sampleId`（target_kind=grpo） | T23 |
| G03 | `/p/:projectId/releases/new`（target_kind=grpo） | T25 |
| B01 | `/recipes` | T26 |
| B02 | `/recipes/:recipeId` | T26 |
| B03 | `/deliveries` | T22 |
| S01 | `/activity` | T27 |
| S02 | `/settings/connections` | T28 |
| S03 | `/settings/team` | T28 |
| S04 | `/help` | T28 |
| S05 | `/catalog` | T01、T09（**仅设计/验收工具，不进入生产菜单**） |

统计：38 个业务画面 + 1 个目录评审页 = 39 屏。G01–G03 与 P02/D02/L02 共用路由，
按项目 `target_kind` 派生节点、字段与发布 schema，不新增重复路由。

### 3.1 导航层级

- **全局四入口**：今日工作、数据项目、方案库、交付库。
- **项目六工作区**：概览、设计、生产、数据、质量、发布。
- **辅助入口**：动态、设置、帮助（不与主流程争菜单位置）。
- **评审工具**：`/catalog`，仅设计/验收使用，生产构建不挂载入口。

### 3.2 URL 参数契约

| 参数 | 位置 | 用途 |
|---|---|---|
| `node` | `/blueprint?node=` | 蓝图右侧检查器当前节点，可分享 |
| `version` | `/blueprint?version=`、`/standard?version=` | 查看历史版本，只读 |
| `status`、`q`、`batch`、`risk` | `/data?...` | 样本筛选，写入 URL 以便返回恢复 |
| `selection` | `/releases/new?selection=` | 少量样本选择；大范围用服务端 selection snapshot |
| `baselineId`、`left`、`right` | `/compare?...` | 同基准 A/B 比较的固定输入 |

---

## 4. 对象与状态转换

### 4.1 核心对象

| 对象 | 不可变部分 | 可改变什么 | 建议表 |
|---|---|---|---|
| Workspace | `workspace_id`、名称 | 成员、连接 | `workspaces`、`workspace_members` |
| Project | `project_id`、`target_kind`、`goal`、`owner`、`budget_policy` | 描述、成员、采用指针 | `projects`、`project_members` |
| Blueprint / Coverage / Standard / QualityPolicy / Mapping | `(project_id, logical_id, version)`、内容 hash | 复制为新版本 | `blueprint_versions` 等五张版本表 |
| Batch | `batch_id`、`purpose`、输入快照 ID/hash | 暂停、恢复失败项、追加事件 | `batches`、`batch_steps`、`batch_items` |
| Sample / SampleVersion | `sample_id`、`version`、原始内容、来源批次 | 产生新版本；原始版本永不覆盖 | `samples`、`sample_versions` |
| Experiment | `experiment_id`、抽样快照、裁判、量表、规则版本 | 重跑创建新实验 | `experiments`、`experiment_items`、`evidence` |
| ComparisonBaseline | `baseline_id`、固定输入问题版本、口径 | 只读 | `comparison_baselines` |
| Decision | `decision_id`、操作者、时间 | 更正通过 `supersedes` 追加 | `review_decisions`、`review_assignments` |
| Release / Candidate | `release_id`、`release_name`、清单、mapping、manifest、hash | 发布后只读 | `release_candidates`、`release_items`、`releases`、`release_artifacts` |
| Recipe | `(recipe_id, version)`、适用范围、限制 | 发布新版本 | `recipes`、`recipe_versions` |
| Job / Outbox | 幂等键、`job_id`、事件 ID | 租约、attempt | `jobs`、`outbox`、`idempotency_records`、`job_attempts` |
| Usage / Budget | 请求 ID、价格版本 | 结算/释放 | `usage_ledger`、`budget_reservations` |
| Activity / Comment | 事件 ID、评论修订 | 已读水位 | `activity_reads`、`comments` |
| LegacyImport | 来源键、内容 hash | 游标 | `legacy_imports` |

所有跨对象引用在**服务层校验同项目/工作区**，关键关联用复合外键或等效 DB 约束防止串项目。

### 4.2 关键状态转换

```
Project:      draft ──save blueprint──> designed ──start pilot──> pilot_running
              ──pilot ready──> pilot_ready ──adopt──> scaling
              ──scale done──> review ──thresholds met──> candidate ──freeze──> published
              （任意状态可 archived：只阻止新运行，不删批次或发布）

Batch:        queued ──lease──> running ──pause cmd──> pause_requested ──drain──> paused
              running ──some item failed──> partial_failed ──retry──> running
              running ──all items done──> completed
              running ──fatal──> failed
              （恢复 = 同批次新 attempt，配置不变）

Experiment:   queued ──lease──> running ──done──> completed
              running ──some item failed──> partial_failed ──retry──> running
              running ──fatal──> failed

Release:      candidate/blocked ──publish cmd──> building ──verify hash──> published
              building ──upload/db fail──> build_failed ──idempotent retry──> building

Decision:     追加式。更正通过 supersedes；有效处置是审计日志之上的投影。
```

### 4.3 Revision 语义

| Revision | 作用域 | 递增时机 |
|---|---|---|
| `expectedRevision` | 版本化文档（蓝图/覆盖/标准/质量/映射） | 每次保存新版本 |
| `reviewer_revision` | 单个审阅者对单一样本版本 | 该审阅者每次提交/更正 |
| `aggregate_review_revision` | 样本版本的全体判断投影 | 任何有效判断变化（T20 冻结时检测竞争） |
| `evidence_revision` | 质量检查周期内的必需证据集 | 实验补齐、发现新风险、修订必需策略 |
| `candidate_revision` | 发布候选 | 每次修订候选 |

---

## 5. 蓝图节点配置规范

蓝图**不是**任意节点画布，也**不是**通用的「并发 + 说明」。每个节点有 typed schema，
节点依赖由服务端校验，**禁止任意脚本节点**。

| 节点 key | 名称 | typed 配置要点 | 归属任务 |
|---|---|---|---|
| `coverage` | 覆盖范围 | 引用 `coverage_version`；稳定领域/方向 ID、配额、难度配比、来源 | T04、T11 |
| `standard` | 思维标准 | 引用 `standard_version`；步骤可排序，每步有检查点 | T04、T11 |
| `generation` | 生成 | 引用 `blueprint_version` 的模型连接**非秘密标识**、`schema_version`、并发 1–32、`max_tokens` | T11、T12 |
| `evaluation` | 独立评估 | 裁判连接标识、量表/权重、抽样 seed；至少一名独立裁判 | T11、T14 |
| `rules` | 规则检查 | 引用 `quality_policy_version`；规则表达式、字段、严重度、建议动作 | T11、T15 |
| `human_review` | 人工检查点 | 分派策略、必需证据集、风险范围 | T11、T16 |
| `delivery` | 版本交付 | 引用 `mapping_version`、输出格式、用途/限制 | T11、T20 |

模型参数默认取**能力声明**，不一律允许 `temperature`（见 T07）。
未接入的 GRPO 节点先只读显示能力状态，T23–T25 开通；未接入前禁用且标明原因。

---

## 6. 追踪表：#159 画面 → 路由 → 命令 → 数据 → 测试 → 任务

### 6.1 命令契约（P = `/api/v1/projects/{projectId}`）

| 用户动作 | API | 返回/冲突 | 任务 |
|---|---|---|---|
| 创建项目 | `POST /api/v1/projects` | 201 + project ID；**无模型调用** | T02、T10 |
| 保存版本 | `POST P/blueprint-versions` 及同类 | 201 + 版本；`expectedRevision` 不匹配 409 | T04、T11 |
| 启动试制/扩量 | `POST P/batches`（purpose + snapshot ID） | 202 + batch/job ID + Location；`Idempotency-Key` 同键同请求返回原结果，同键不同请求 409 | T06、T13 |
| 暂停/继续/恢复失败项 | `POST P/batches/{id}/pause`、`/resume`、`/retry-failed` | 202/200 + 控制状态；在途数量明确；非法状态 409 | T13 |
| 创建质量实验 | `POST P/experiments` | 固定 selection/config 后 202；无独立裁判/空范围/错误量表 422 | T14 |
| 规则预览 | `POST P/rule-previews` | 200 + 命中位置；**不写处置、不入收费模型队列** | T15 |
| 记录判断 | `POST P/samples/{sampleId}/versions/{versionId}/decisions` | 201 + decision；重复提交幂等；版本变更 409 并保留用户草稿 | T16 |
| 创建/修订候选 | `POST P/release-candidates`、`PATCH P/release-candidates/{id}` | 返回 candidateId、稳定 releaseId、revision；每条 blocker 带可跳转对象 | T20 |
| 冻结并发布 | `POST P/release-candidates/{id}/publish` | 202 + release ID/building；相同命令返回同一 release；仅文件校验成功后 `published` | T20、T21 |
| 下载/下一版 | `GET P/releases/{id}/artifacts/{artifactId}/download`；`POST P/releases/{id}/next-candidate` | 下载不可变文件；下一版返回独立候选；禁止 `latest` 回退 | T21、T22 |

统一返回稳定 `id/status/revision/updatedAt/capabilities/links`；分页用稳定游标与排序键。
错误包含 `code/message/fieldErrors/blockers/requestId/retryable`，区分 401、403/资源隐藏型 404、
409、422、429、503。权限由服务端判定，`capabilities` 只辅助 UI。详见
`docs/plans/atelier-api-contract.md`。

### 6.2 画面归属表

| 画面 | 路由 | 主要命令 | 数据对象 | 测试归属 | 任务 |
|---|---|---|---|---|---|
| W01 今日工作 | `/today` | 无写命令 | `activity_reads`、待决定聚合 | 计数与列表对账 | T10、T27 |
| W02 数据项目 | `/projects` | `POST /api/v1/projects` | `projects` | 分页/搜索/归属 | T10 |
| W03–W05 新建向导 | `/new`、`/new/coverage`、`/new/quality` | `POST /api/v1/projects` | `projects`（draft） | 表单校验/幂等/草稿恢复 | T10 |
| P01 项目概览 | `/p/:id/overview` | 无写命令 | 版本/批次/待决定 | 下一决定定位 | T10、T27 |
| P02 生产蓝图 | `/p/:id/blueprint` | `POST P/blueprint-versions` | `blueprint_versions` | 乐观锁/节点 schema | T04、T11 |
| P03 覆盖矩阵 | `/p/:id/coverage` | `POST P/coverage-versions` | `coverage_versions` | 配比总和/稳定 ID | T04、T11 |
| P04 思维标准 | `/p/:id/standard` | `POST P/standard-versions` | `standard_versions` | 步骤排序/变更理由 | T04、T11 |
| P05 小批试制 | `/p/:id/pilot` | `POST P/batches`(pilot) | `batches` | 独立批次/不覆盖生产 | T13 |
| P06 试制对比 | `/p/:id/compare` | `POST P/comparison-baselines` | `comparison_baselines` | 基准一致性 | T18 |
| R01 生产批次 | `/p/:id/runs` | 无写命令 | `batches` | 独立身份列表 | T13 |
| R02/R04/R06 批次详情 | `/p/:id/runs/:batchId` | `/pause`、`/resume` | `batches`、`batch_steps` | 状态机/在途数量 | T13 |
| R03 异常恢复 | `/p/:id/runs/:batchId/failures` | `/retry-failed` | `batch_items`、`job_attempts` | 幂等恢复不重跑成功项 | T06、T13 |
| R05 扩量规划 | `/p/:id/runs/new` | `POST P/batches`(scale) | `batches` | 范围 1–100000/预算 | T13 |
| D01 样本工作区 | `/p/:id/data` | 无写命令 | `samples`、`sample_versions` | 筛选/分页/选择范围 | T17 |
| D02 三栏审阅 | `/p/:id/data/:sampleId` | `POST .../decisions` | `sample_versions`、`evidence` | 版本变更 409/内容只读 | T16、T17 |
| D03 版本与来源 | `/p/:id/data/:sampleId/history` | 无写命令 | `sample_versions`、`review_decisions` | 时间线完整性 | T17 |
| Q01 质量实验室 | `/p/:id/quality` | 无写命令 | `experiments`、`evidence` | 风险计数与筛选一致 | T19 |
| Q02 新建实验 | `/p/:id/quality/new` | `POST P/experiments` | `experiments` | 冻结快照/独立性检查 | T14、T19 |
| Q03 实验报告 | `/p/:id/quality/:experimentId` | 无写命令 | `experiments`、`experiment_items` | 分母/缺分/分歧 | T14、T19 |
| Q04 审阅队列 | `/p/:id/review` | `POST .../decisions` | `review_decisions` | 默认待审阅队列 | T16、T17 |
| Q05 清洗策略 | `/p/:id/rules` | `POST P/rule-previews` | `quality_policy_versions` | 预览无副作用/非法 regex | T15、T19 |
| L01 发布版本 | `/p/:id/releases` | 无写命令 | `releases` | 候选/已发布区分 | T22 |
| L02 准备发布 | `/p/:id/releases/new` | `POST P/release-candidates` | `release_candidates` | 字段映射匹配类型 | T20、T22 |
| L03 候选与数据卡 | `/p/:id/releases/:releaseId` | `/publish`、`next-candidate` | `releases`、`release_artifacts` | 门槛阻塞/冻结/下载 hash | T20、T21、T22 |
| G01 GRPO 蓝图 | `/p/:id/blueprint` | `POST P/blueprint-versions` | `blueprint_versions` | GRPO 节点/档位 schema | T23 |
| G02 GRPO 审阅 | `/p/:id/data/:sampleId` | `POST .../decisions` | `sample_versions`(grpo) | 档位/判据字段 | T23、T24 |
| G03 GRPO 发布 | `/p/:id/releases/new` | `POST P/release-candidates` | `release_candidates` | JSONL 专属字段 | T25 |
| B01 方案库 | `/recipes` | 无写命令 | `recipes` | 可见范围 | T26 |
| B02 方案详情 | `/recipes/:recipeId` | `POST /api/v1/projects`(sourceRecipeVersionId) | `recipe_versions` | 复制值来源 | T26 |
| B03 交付库 | `/deliveries` | 无写命令 | `releases` | 仅已发布/可访问 | T22 |
| S01 动态与待办 | `/activity` | `POST /api/v1/activity/read` | `activity_reads` | 未读水位/乱序 | T27 |
| S02 连接设置 | `/settings/connections` | 复用 provider/storage 管理 | `providers`、`storage_profiles` | 测试/保存分离/密钥不回显 | T28 |
| S03 成员与角色 | `/settings/team` | membership 命令 | `workspace_members`、`project_members` | 最后 owner 不可移除 | T03、T28 |
| S04 帮助 | `/help` | 无 | 静态 + 构建信息 | 构建版本自检 | T28 |
| S05 目录评审 | `/catalog` | 无 | 原型静态数据 | **不进入生产菜单** | T01、T09 |

所有页面的共同行为（脏表单、加载/空/失败、权限/不存在、离线待同步、键盘焦点、窄屏、URL 返回）
归属 **T09、T29、T32**，在每个页面的测试中体现。

### 6.3 任务 → 交付物 → 测试

| 任务 | 主要落点 | 测试归属 |
|---|---|---|
| T01 | 本文件、`docs/plans/atelier-api-contract.md`、`todo.md`、`scripts/check-docs.mjs` | `node scripts/check-docs.mjs` |
| T02 | 迁移 `0022`、`internal/model/project.go`、`internal/store/project_store.go`、`apps/api/routes_projects.go` | Go 测试：事务/分页/重复迁移 |
| T03 | `apps/api/auth.go`、授权 helper、`internal/store/auth_store.go`、audit | Go 测试：越权矩阵/撤权/最后 owner |
| T04 | `internal/model/pipeline_v2.go`、版本 store/service、迁移 `0024` | Go 测试：并发保存冲突/字段校验 |
| T05 | 迁移 `0025`、batch/sample model+store、`internal/studio/` | Go 测试：批次独立/幂等提交/跨项目拒绝 |
| T06 | `apps/worker/registry.go`、`apps/worker/studio_jobs.go`、dispatcher、`internal/store/job_store.go` | Go 测试：崩溃/重复消息/租约过期/Redis 重启 |
| T07 | `internal/llm/`、迁移 `0027` | Go 测试：预算边界/并发预留/未知费用 |
| T08 | `apps/api/routes_studio_*.go`、`internal/studio/`、`apps/web-user/src/lib/api/studio.ts` | 契约测试：TS 类型一致/错误码/分页 |
| T09 | `App.tsx` 拆分、`StudioLayout`、`ProjectLayout`、route metadata | L15 守卫迁移 + 浏览器 E2E |
| T10 | 向导/列表/概览页面模块 | 浏览器 E2E：W01–W05/P01 |
| T11 | 蓝图节点检查器/覆盖矩阵/标准历史 | 浏览器 E2E：节点/版本/diff |
| T12 | Batch 原生 SFT runner | Go 测试：可控 provider 全错误分支 |
| T13 | 试制/扩量/批次详情/暂停恢复 | 浏览器 E2E + Go 测试：幂等恢复 |
| T14 | 冻结实验与 SFT 评估执行 | Go 测试：入队后改配置不改实验 |
| T15 | 版本化规则、纯预览、命中证据 | Go 测试：预览零副作用/非法 regex |
| T16 | 人工判断、分派、冲突协调 | Go 测试：并发更正/相反意见/证据 revision |
| T17 | 样本列表、三栏审阅、专注模式 | 浏览器 E2E：D01–D03/Q04 |
| T18 | 同基准比较与采用方案 | Go 测试：基准不一致拒绝 + 浏览器 E2E |
| T19 | 质量实验室页面拆分 | 浏览器 E2E：Q01–Q05 |
| T20 | 发布候选、门槛、抗并发冻结 | Go 测试：并发隔离/双击发布 |
| T21 | 不可变制品、manifest/hash、发布作业 | Go 测试：重试单份/哈希一致/存储故障 |
| T22 | 发布页与交付库 | 浏览器 E2E：L01–L03/B03 |
| T23 | GRPO 蓝图与生成适配 | Go 测试：档位 schema |
| T24 | `internal/eval/grpo_adapter.go`、`apps/worker/job_studio_eval.go` | Go 测试：GRPO 量表/独立性 |
| T25 | GRPO JSONL 编码器与 UI | Go 测试：逐行解码字段结构 |
| T26 | recipe model/store/service、`apps/api/routes_recipes.go` | Go 测试：复制来源/可见范围 |
| T27 | `apps/api/routes_studio_activity.go`、comments API、activity store | Go 测试：游标/水位/撤权过滤 |
| T28 | 复用 `provider_admin.go`、members/connection-options/budget read API | Go 测试：无权限深链接/最后 owner |
| T29 | 离线队列、无障碍、虚拟化 | 浏览器 E2E：断网/窄屏/键盘 |
| T30 | `cmd/studio-migrate/`、迁移报告 | dry-run 零写入测试 |
| T31 | `legacy_imports` 幂等导入、旧路由兼容 | 对账测试：重复执行零重复 |
| T32 | `.github/workflows/ci.yml` 新增集成/浏览器 job | CI 本身 |
| T33 | `studio_enabled` 开关、可观测性、回退手册 | 演练记录 |
| T34 | 用户研究记录、runbook、`todo.md` 对账 | 证据清单 |

---

## 7. 与既有 L15 冻结契约的冲突

`docs/plans/eval-and-cleaning-plan.md` 与 `docs/plans/issue-remediation-plan.md` 已冻结。
本轮的 L15 守卫（`test/l15_*.mjs`）对 `App.tsx` **源码布局**有假设，T09 重构 `App.tsx` 时
这些假设会失效。冲突逐条列出如下，处置原则：**迁移到行为/路由契约断言，保留对旧入口的回归，
不能简单删掉守卫使 CI 变绿**（#160 §6 与 T32 的明确要求）。

| # | 冲突 | 冻结来源 | 处置 | 任务 |
|---|---|---|---|---|
| C1 | `test/l15_stage_routes.mjs` 断言 `App.tsx` 内 5 个阶段路由直接渲染页面、`render*` 不成死代码、`stageRouteNavMap` 由阶段工作台声明派生 | eval-and-cleaning-plan.md §1、issue #61 | 保留旧 `/console/*` 路由与守卫到过渡期；新增 `/p/*` 路由后，把断言从「源码布局」迁移为「路由契约 + 行为」，并保留旧入口回归 | T09、T32 |
| C2 | `test/l15_silent_guards.mjs` 断言 `App.tsx` 全文无 `if (!activeDatasetId) return` 等静默模式 | eval-and-cleaning-plan.md §1、issue #64 | 新项目壳**不依赖** `activeDatasetId`（T09 验收项）。守卫迁移为「新壳同样无静默 return」+ 旧壳回归 | T09、T32 |
| C3 | `test/l15_capability_entries.mjs` 断言 6 项能力在 UI 各有可达入口 | eval-and-cleaning-plan.md §1、issue #65 | 6 项能力在新壳中的入口由 T11/T19/T23 承接；守卫改为「新入口或旧入口至少一处可达」，不因换菜单丢失能力 | T09、T11、T19、T23 |
| C4 | `test/l15_nmx_params.mjs`、`test/l15_dataset_status.mjs` 依赖 dataset 中心的参数与状态语义 | eval-and-cleaning-plan.md §1、`docs/plans/round2-gap-nmx-params.md` | n/m/x 口径由本文件 §2.1 统一；旧 dataset 页过渡期保留，新项目口径另测，两者不互相冒充 | T01、T10、T32 |
| C5 | `test/l15_app_ux.mjs`、`test/l15_cleaning_ux.mjs` 断言 `EvaluationView.tsx`/`CleaningView.tsx` 的具体文案与控件 | eval-and-cleaning-plan.md §1、issues #103/#104/#107/#108 | 抽取可复用组件后断言迁移到新页面（T19/T15）；旧入口过渡期仍可访问，文案缺陷回归保留 | T15、T19、T32 |
| C6 | `test/l15_artifact_download.mjs` 断言按 dataset 的下载链路 | eval-and-cleaning-plan.md §1、issues #136/#137 | 新下载按 `releaseId`；旧下载过渡期保留为兼容读。守卫同时覆盖两者，且断言新路径拒绝 `latest` | T21、T22、T32 |
| C7 | `docs/plans/eval-and-cleaning-plan.md` §2 迁移编号区间 `0011–0019`「lane 不得新增迁移文件」 | eval-and-cleaning-plan.md §2 | 该约束只作用于**第一轮 lane**。本轮从 `0022` 起新增，不改写已应用迁移，并同步维护编号分配表 | T01 起各任务 |
| C8 | 旧 `PlanEstimate.AnswerVariants`/`RewardVariants` 参与计划量计算 | `internal/model/dataset.go` | 新统计口径见 §2.1，禁止偷乘；旧字段仅作历史兼容读模型 | T01、T05、T10 |

### 7.1 迁移编号分配

已应用到 `0021`。新迁移从 **`0022`** 起，禁止重写已应用迁移。

| 编号 | 文件 | 归属任务 |
|---|---|---|
| 0022 | `sql/migrations/0022_studio_workspaces_projects.sql` | T02 |
| 0023 | `sql/migrations/0023_studio_authz_audit.sql` | T03 |
| 0024 | `sql/migrations/0024_studio_versioned_docs.sql` | T04 |
| 0025 | `sql/migrations/0025_studio_batches_samples.sql` | T05 |
| 0026 | `sql/migrations/0026_studio_jobs_outbox.sql` | T06 |
| 0027 | `sql/migrations/0027_studio_usage_budget.sql` | T07 |
| 0028 | `sql/migrations/0028_studio_experiments.sql` | T14 |
| 0029 | `sql/migrations/0029_studio_rules_evidence.sql` | T15 |
| 0030 | `sql/migrations/0030_studio_review.sql` | T16 |
| 0031 | `sql/migrations/0031_studio_releases.sql` | T20 |
| 0032 | `sql/migrations/0032_studio_recipes.sql` | T26 |
| 0033 | `sql/migrations/0033_studio_activity_comments.sql` | T27 |
| 0034 | `sql/migrations/0034_studio_legacy_imports.sql` | T30 |

---

## 8. 完成定义（对齐 #160 的 DoD）

本追踪表服务于 #160 §4 的 34 个任务与 §6 的最终 DoD。每个任务关闭需附：
PR、自动化测试、实际交互证据。**只完成截图或前端按钮不能勾选。**

阶段退出门槛（#160 §3）：

| 阶段 | 退出门槛 |
|---|---|
| M0 | #159 各页面和动作都有任务归属；旧进度文档不再混淆目标 |
| M1 | 授权、版本冲突、作业恢复测试完成；未实现能力不能伪装可用 |
| M2 | 真实 provider 可接入；默认 CI 使用可控假 provider，不伪称真实产出 |
| M3 | 质量证据真实持久化，统计口径一致，判断不改内容 |
| M4 | Pxx 与 Bxx 独立；候选阻塞有效；发布后改项目/隔离样本不能改旧下载 |
| M5 | 不用 SFT 页面或固定统计填充 GRPO；权限和断网语义完成 |
| M6 | 全部门禁通过且迁移差异被处理；不是只把新菜单设为默认 |
