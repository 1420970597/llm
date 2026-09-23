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
| 2026-09-21 | T09 | §7 的 C1/C2 要求把 L15 守卫从「源码布局假设」迁到「行为/路由契约断言」，但未指定新壳的守卫形态 | 新增 `test/l15_studio_shell.mjs`（已加入 CI 的 UI guards 步骤，与既有 8 个守卫同层），三层结构：第 1 层源码级（路由元数据唯一性、available/planned 状态与任务号、nginx try_files、不依赖全局任务选中态），第 2 层用 esbuild 打包**生产模块** `src/studio/routes.ts` 并断言 `matchRoute`/`activeNavKey`/`breadcrumbsFor`/`isCatalogRouteMounted` 的真实行为，第 3 层变异自证（改坏元数据必须被捕获）。**旧的 8 个 L15 守卫全部保留且继续在 CI 运行**（旧入口是过渡期的兼容读，不得因换菜单而失去回归保护）|
| 2026-09-21 | T09 | 框架约束：React Router v6 的 `<Routes>` 只接受 `<Route>` 或 `<React.Fragment>` 作为直接子元素 | `studioRouteTree` 必须是**函数**（调用方写 `{studioRouteTree(props)}`），不能是组件。写成组件会在运行期抛 `[X] is not a <Route> component`，而 `tsc`/`vite build` **都不会**发现它 —— 实际由 `test/l15_stage_routes.mjs` 在真实渲染里报出。已在该函数与守卫里注明，避免后续重构成组件|
| 2026-09-21 | T09 | 再次遇到 `import.meta.env` 只在 Vite 构建里存在（#112 的同一形态） | 新壳里读取 `import.meta.env.PROD` 时必须**先判断宿主存在**（与 `buildInfo.ts` 的防御式读取一致），否则所有用 esbuild 渲染真实组件树的 UI 守卫都会以 `Cannot read properties of undefined` 挂掉 —— 本次改动真的触发了一次（`l15_stage_routes.mjs` 当场失败）。守卫第 2 层用 `define: { 'import.meta.env': ... }` 注入（定义宿主而不是只定义 `.PROD`），使「生产不挂载目录评审页」测的是真实行为 |
| 2026-09-21 | T10 | §3.2 的 URL 参数契约未规定向导草稿的存放位置 | 向导草稿存浏览器本地（`studio.wizard.draft.v1.u<userId>`），**键必须含用户 ID**：固定键会让下一个登录的用户看到上一个人的项目名称与目标（隐私 + 建出属于别人内容的双重问题）。不做服务端草稿表：一段未提交的表单不是业务事实，把它当项目一样做乐观锁与权限只会让向导多一层失败面；代价（换浏览器不恢复）已在界面上写明 |
| 2026-09-21 | T10 | §2.1 的创建命令字段与前端表单需要一致，但契约未规定一致性如何保证 | 前端校验常量与 Go 常量由 `test/l15_studio_wizard.mjs` **直接读两侧源码比对**（`MinPilotSize`/`MaxPilotSize`/名称上限/预算下限），而不是人工同步。前端校验只是为了「不必往返一次」，判定仍在服务端；服务端返回的 `fieldErrors` 会按字段归到向导步骤并跳回最早出错的那一步，避免用户看到「本页没有这个字段」的错误 |
| 2026-09-21 | T11 | §5 的节点表未规定「节点键」与「payload 字段名」是否同一字符串 | **明确区分**：节点键是 snake_case（`human_review`，用于 URL 的 `?node=` 与校验分派，复用 T04 冻结的 `BlueprintNode*` 常量），payload 字段名是 camelCase（`humanReview`，属于已冻结的文档 schema）。新增 `BlueprintNodeSpec.PayloadField` 承载映射，并由测试双向断言（结构体字段↔元数据字段、payload 字段↔节点覆盖）。理由：把两者混为一个值会让「`?node=human_review` 读不到配置」这类缺陷出现在界面上却看不出原因 |
| 2026-09-21 | T11 | §5 未说明「保存版本」与「执行前」的必填是否同一套 | 明确为**两套**：`NodeFieldSpec.Required` 只表达「**执行前**必须设置」，保存版本允许不完整（§5 允许先定结构再逐项补内容，用户要能保存只填了一部分的蓝图草稿）。节点字段约束的元数据由 `GET P/blueprint-nodes` 暴露，前端检查器**由它驱动**而不是按节点手写七个表单，于是「服务端加了字段而界面没跟上」不会发生 |
| 2026-09-21 | T12 | §6.3 未规定「覆盖分配」的算法位置与确定性要求 | 放在 `internal/studio/batch_runner.go` 的纯函数 `AllocateUnits`：按覆盖版本的领域/方向**顺序**展开、每方向按 `quota` 取 ordinal、直到计划单元数用尽。**必须确定** —— item_key = `domain/direction#ordinal`，因此「恢复失败项」命中同一批 item（配合 T05 的 UNIQUE(batch_id,item_key)），不会把已完成的工作重跑一遍。缺覆盖版本时退化为 `unit/default#n`，使「无覆盖方案的手动扩量」仍可执行 |
| 2026-09-21 | T12 | §4.2 未规定「单元级重试」归谁 | 明确**不**在 runner 内重试：重试由作业层按 attempt/退避/上限统一管理。runner 内再重试会与作业层的 attempt 计数重复，同一笔费用会得到两次机会，`max_attempts` 也失去意义。runner 只做「抢占 → 生成 → 提交或记录失败」，不可重试的错误类别显式标 `retryable=false`，使「恢复失败项」不会重复花在必然失败的输入上 |
| 2026-09-21 | T12 | §2.2 的 `reasoning` 与旧生成器的 `chainOfThought` 命名不同 | 在 worker 侧做**显式适配**（`internal/llm` 的旧生成器不动，属第一轮冻结契约）：旧名直接透传会被 T05 的 schema 校验拒绝，而静默接受一个不在契约里的字段更糟。`StandardStep`→`ChainStep` 同理按显式 `order` 稳定排序（新 schema 的顺序是数据，旧类型用数组下标当顺序） |
| 2026-09-21 | T13 | §3.2 未规定批次详情/异常恢复的路由形态 | 落实为生产标签的**子页**（`navParent: project.runs`）：`/p/:projectId/runs/:batchId` 与 `/runs/:batchId/failures`，因此侧边栏高亮与面包屑都指回「生产」而不是掉回默认项（issue #61 的同一修复形态）。`?slice=` 从覆盖矩阵带缺口方向进入规划页，**只影响提示与初值**，不触发任何重生成 |
| 2026-09-21 | T13 | §4.2 未规定「暂停中」在界面上如何表达 | 冻结为：暂停后必须**同时**显示「已停止提交新请求」与「在途 N 个请求仍会完成并计费」（`data-pause-notice` 可断言）。只显示「已暂停」会让用户以为不再花钱，而 §2.4 明确暂停不撤回在途请求 |
| 2026-09-21 | T14 | §2.6 只说「缺分与真实 0 分区分」，未规定表结构如何保证 | 用**可空列**保证而不是靠约定：`experiment_scores.raw_score` 为 NULL 表示「这次没拿到分」，`experiment_items.status` 区分 `missing`（缺分）/`error`（裁判出错）/`not_applicable`（不适用）三种事实 —— 压成 0 会让「没评」显示成「评得很差」。另用 `score_state` 标注每一格是什么 |
| 2026-09-21 | T14 | §2.3 要求「隔离不缩小分母」，但未规定样本版本可否被删除 | `experiment_items.sample_version_id` 用 **ON DELETE RESTRICT**：用 CASCADE 会让「删一个样本」静默缩小历史实验的分母，而那正是「分母可以被做小」的形态。分母因此永久等于创建时冻结的行数 |
| 2026-09-21 | T14 | 独立性判定缺少可依据的字段 | 冻结判据为**endpoint 指纹不同**（不是连接 ID 不同）：同一真实来源的别名连接（主/备用账号指向同一 endpoint）不算两名裁判。指纹缺失时**保守判为不独立** —— 反过来会让独立性在配置不全时静默失效，而失效方向是「本该拦住的自评被放行」。`experiment_items.generator_source/generator_fingerprint` **由样本来源推导**并冻结，不接受客户端传入（否则请求体里带一个 generatorConnectionId 就能绕过检查）|
| 2026-09-21 | T14 | T14 尚未全部交付（本轮完成迁移 0028 + 模型判据与测试；store/worker/报告的落库与执行路径仍待续） | 已交付且经 门禁验证的部分：迁移 0028（experiments/experiment_items/experiment_scores，含可空分值、RESTRICT 外键、只追加的评分与部分唯一索引）、`internal/model/experiment.go` 的判据与统计（独立性、固定分母、零分母无结论、归一化、量表校验、GRPO 在 T24 前不可运行）、`internal/model/experiment_test.go`。**已交付**：`internal/store/experiment_store.go`（创建即冻结、只追加的评分与 supersede、固定分母报告、续跑清单）、`internal/store/sample_query.go` 的按 ID 读取（执行侧按**冻结的版本 ID**取内容）、`internal/studio/experiment_runner.go`（逐项执行、缺分与出错区分、续跑不重评）与三个测试文件。**归属他处**：项目实验 API 与质量页按契约 §3 由 T19 增量交付|
| 2026-09-21 | T15 | §2.6 要求「预览不写处置、不入收费模型队列」，但未规定如何保证 | **不为预览建表、预览端点在 store 里只有 SELECT**。理由：「预览前后无变化」不能靠「记得回滚」，而要靠「根本没有写路径」。响应显式带 `sideEffects: false`，使前端与验收脚本能**断言**这一点。命中数达上限时返回 `truncated: true`（否则用户会以为「就这么多命中」）|
| 2026-09-21 | T15 | 规则与证据的取值集合已在 §5/T04 冻结，T15 未说明是否复用 | **复用** `RuleMatch*`/`RuleSeverity*`/`RuleAction*`（studio_docs.go）：另立一套会让「质量策略版本」与「规则证据」对同一概念用不同字符串，而那种不一致只在运行时以「评估时规则被跳过」的形式出现。同时明确：匹配方式是**闭合集合**（§5 禁止任意脚本节点）；建议动作里**没有「自动隔离」**，它们只是建议，不自动执行 |
| 2026-09-21 | T15 | 规则命中证据要保留什么，契约未逐列规定 | 冻结为：规则**表达式快照**、匹配方式、字段、严重度、建议动作、命中偏移（以**字符/rune** 计，字节偏移会让中文高亮错位）与片段。只存 rule_id 会让历史命中在规则被改后显示成新规则的结果，而「当时为什么拦下它」是申诉与复核的唯一依据。`sample_version_id` 与 `quality_policy_version_id` 都用 ON DELETE RESTRICT：证据与其引用的版本不得被静默删除 |
| 2026-09-21 | T15 | T15 尚未全部交付（已交付迁移 0029 与模型判据；纯预览端点、证据追加与测试仍待续）| **已交付**：迁移 0029（rule_evaluations / rule_evidence，含表达式快照、rune 偏移、RESTRICT 外键）、`internal/model/rule_evidence.go`（校验即编译 regex、服务端长度/命中上限、证据冻结）。**新增交付**：`internal/store/rule_store.go`（`PreviewRules` 纯 SELECT、`RecordRuleEvaluation` 只追加证据、`ListRuleEvidence`）与测试；`internal/cleaning` 暴露 `MatchStart/MatchEnd`（本来就是它算出来的 rune 偏移）并新增 `MatchKeywordsAll`（全部命中位置，而 `MatchKeywords` 的「每关键词首处」行为保持不变，以免影响已冻结的清洗语义）。**未交付**：项目规则 API 与规则页（T19）|
| 2026-09-21 | T16 | §4.3 列出三个 revision 但未说明投影如何维护 | 「有效处置」落在**投影表** `review_projections`（可从 review_decisions 重建），而不是每次聚合查询：列表页要按有效处置筛选分页，聚合查询走不了索引；且 T20 冻结候选需要在**一个可串行化的点**上读「有效处置 + 聚合序号」——投影行锁就是那个点。投影可重建这一点很重要：它意味着投影更新逻辑即使有 bug 也能重放修复，不会丢判断历史 |
| 2026-09-21 | T16 | 契约未规定「同 reviewer_revision、不同内容」的处置 | 按 §1.3 的幂等语义：**同序号 + 同内容 = 幂等重放**（网络重试/双击），**同序号 + 不同内容 = 409**。无条件回放会静默丢弃用户真正想提交的那次更正，而「过期返回 409 并保留输入」正是要防这件事（由测试发现并修复）|
| 2026-09-21 | T16 | 契约未规定协调决定与普通判断的关系 | 协调决定用**同一张表 + `resolution_of` 标记**：它同样需要理由、同样只追加、同样确认证据版本。投影计算里**协调决定优先**（取最新一条确认当前证据的），否则相反意见仍有效 → 冲突永远解除不了。证据集变化后旧协调同样失效，避免「新风险沿用旧结论」从协调这条路绕回来 |
| 2026-09-21 | T16 | §4.1 未规定「有效接纳」是否受内容版本影响 | 受影响：投影记录 `content_hash`，与当前内容 hash 不同即回到 `pending`（原因 `new_version`）。判断是针对**某一版内容**的，内容变了就必须重新判断（T16 验收项「新内容版本同样需要新判断」）|
| 2026-09-21 | T17 | §3.2 的 `selection` 参数未规定大范围选择如何承载，§7.1 的迁移编号表到 0034 截止 | 新增迁移 **0035**（`sample_selection_snapshots`/`sample_selection_items`）：URL 只带快照 ID，ID 列表留服务端。理由：数万 ID 会超出浏览器/代理长度上限（表现为「点了发布什么都没发生」），且 URL 里的 ID 列表是**客户端可改的**，而发布范围必须是服务端认可的集合。取 0035 而不是重编号已冻结的 0031–0034：重编号会让已写完的任务记录与迁移文件对不上。快照有过期时间（默认 2 小时）：不过期会让「上周选的」直接用于发布 |
| 2026-09-21 | T17 | §3 的 `status`/`risk` 筛选曾是 T08 显式拒绝的项，T17 未说明如何接线 | `status` 接入为**投影的有效处置**筛选（`review_projections.effective_action`），用 LEFT JOIN 使从未判断过的内容按 `pending` 出现——INNER JOIN 会把它们全部排除，得到一个永远空着的待审阅队列。`risk` 仍显式拒绝（它需要按规则命中证据聚合，属 T15 证据面），保持「不静默忽略」的既定原则 |
| 2026-09-21 | T17 | 契约未规定「选择范围」与「筛选总数」的关系 | 冻结为：**勾选只针对当前页**并在界面上写明「已选 N（当前页）」；跨页/大范围选择必须走服务端快照。理由：跨页静默累积与「把筛选总数当已选数」两种错法都不报错，只在提交后表现为范围不符——那时已产生副作用。守卫 `test/l15_studio_review.mjs` 断言这条，并含变异自证 |
| 2026-09-21 | T18 | §3 的 P06 未规定「可比」的判据与口径的关系 | 冻结为**由口径决定能说什么**：`paired`（逐题配对）必须固定输入问题版本，标签为「同一题在两方案下的差异」；`coverage`（覆盖生成）的标签必须写明「两侧输入不同」，并在响应里**显式禁止**逐题配对的说法。口径缺失时拒绝「可比」标签而不是猜一个。配对完成数为 0 时判为**不可比**（而不是「差异 0」）。另：比较基准编号取 **0036**（0035 已被 T17 的选译快照占用，同样不重编号已冻结的 0031–0034）|
| 2026-09-21 | T18 | 契约未规定「只有一侧有分的观测」如何参与差异 | 冻结为**不计入配对也不补 0**：它们单独计为 LeftOnly/RightOnly 并写入风险说明。理由：把「只有一侧有分」算成 0（或算进配对）会凭空制造差异，而那正是「拿两个任意批次百分比相减」在逐题层面的形态 |
| 2026-09-21 | T18 | T18 的比较口径与前端呈现 | 已交付：迁移 0036、`internal/model/comparison.go`（判据）、`internal/store/comparison_store.go`（配对观测与采用）、`apps/api/routes_studio_compare.go`（创建基准/读报告/采用/采用指针）、`ComparePage.tsx`（可比性优先显示 + 维度差异 + 风险 + 成本分列 + 采用表单）。**关键决定**：配对键用 `samples.sample_key`（单元键），评分取当前有效且 `score_state='scored'` 的行，归一化在 Go 侧按**基准冻结的量表**做；采用只写「只追加的依据 + 一行指针」，并返回扩量预填参数而**不**自动创建批次 |
| 2026-09-21 | T19 | §3 的 Q01–Q05 与 T14/T15 的数据面如何划分 | 实验/规则的**命令与读模型**落在 `apps/api/routes_studio_quality.go`（实验创建/列表/报告、`POST P/rule-previews`），因为 T14/T15 交付的是数据与判据，而端点服务的是页面。页面落在 `QualityPages.tsx`（列表/创建/报告/规则）。**关键决定**：报告页在 `queued`/`running` 时**不显示均值**，只显示完成覆盖 —— 基于部分样本的均值会被当成结论（T19 验收项）；实验 ID 只来自路由参数，因此刷新与分享恢复同一实验，不依赖内存里的 selectedRunId/tabKey |
| 2026-09-21 | T19 | 裁判身份如何传入才不能被绕过 | 前端只传 `judgeConnectionIds`，**指纹由服务端从连接的当前配置读取**并生成裁判快照。不接受客户端传 fingerprint —— 那会让「同源自评」只需伪造一个指纹就能绕过独立性检查（T14 验收项要求独立性判定不可被客户端绕过）|
| 2026-09-21 | T20 | §2.5 的「发布名」与「发布 ID」在表结构上如何共存 | `releases.id` 就是稳定的 releaseId（表主键，因此不存在「重新分配」的可能），`release_name` + `release_name_key`（规范化键）承担项目内唯一。规范化键是必需的：直接用 release_name 会让 `V1.2` 与 `v1.2` 同时存在，而用户会以为是同一版。版本名**禁止** `latest`（下载路径禁止 latest 回退，而叫 latest 的版本名会让用户以为文件总是最新）|
| 2026-09-21 | T20 | §2.9 的门槛检查时点未规定 | 冻结事务**重新判定门槛**（用当前判断/证据），而不是信任创建时的 blockers：blockers 是**创建那一刻**的检查结果，而发布可能发生在几分钟甚至几天后，期间有人隔离了一条或发现了新风险。同时校验每条清单项的 `aggregate_review_revision`/`evidence_revision` 与当前一致，不一致返回 409 并要求重新确认。门槛未通过时**不写入清单**：写入会让「被挡住的候选」拥有一份看起来可发布的文件范围 |
| 2026-09-21 | T20 | §2.8 的候选清单与「排除项」的关系 | 清单存**具体 sample_version + 内容 hash + 来源 hash**；被排除项以 `excluded_reason` 标记并**仍计入分母**（§2.3「发布范围可缩小但保留原范围指标」）。分子（接纳数）按冻结范围内的接纳统计，因此「排除几条差的」不能提高接纳率。来源 hash 取自 `sample_versions` 自身字段，**不**取「当前项目采用版本」——后者会让发布时改一次蓝图把历史内容的来源改写成新版本（T20 验收项禁止「以当前项目版本冒充样本来源版本」）|
| 2026-09-21 | T20 | T20 尚未全部交付（已交付迁移 0031、门槛判据与 store；项目命令 API、发布作业与页面属 T21/T22）| **已交付**：迁移 0031（releases/release_candidates/release_items/release_gates）、`internal/model/release.go`（六类门槛判据与 blocker）、`internal/store/release_store.go`（身份分配、抗并发冻结、幂等重复发布、outbox 同事务）与两侧测试。**未交付**：项目发布命令 API、T21 的制品/manifest/hash 与发布作业执行、T22 的发布页与交付库 |
| 2026-09-21 | T21 | §7.1 的编号表未给 T21 预留编号（0031–0034 已分配、0035/0036 已被 T17/T18 占用）| 新增 **0037**（release_manifests / release_artifacts）。不重编号已冻结的 0031–0034：重编号会让已写完的任务记录与迁移文件对不上 |
| 2026-09-21 | T21 | 契约要求「manifest hash 不把自己包含进 hash 输入」但未规定规范化细节 | 冻结为：hash 输入 = `CanonicalManifestBytes`（条目按 (sampleId, sampleVersionId) 排序、Limitations 去重排序、ItemCount 从 Items 派生、无缩进），且**不含任何 hash 字段**。排序是必需的：清单顺序取决于数据库返回顺序（无 ORDER BY 时未定义），不排序会让同样内容的两份 manifest 产出不同 hash，于是「重试是否得到同一份文件」无法验证 |
| 2026-09-21 | T21 | 「已发布文件不可变」如何由对象路径保证 | `ArtifactObjectKey` = `releases/{id}/r{revision}/export-{format}-{hash前16位}.jsonl`：路径含内容 hash，因此同内容重传写同一位置（幂等），而内容不同自然写到另一 key（覆盖等于写入另一内容）。路径**不含** `latest`（下载禁止 latest 回退，路径也不给它留位置）。另：`RegisterArtifact` 在同 hash 时回放既有行，在**已确认**时拒绝不同 hash 的登记 —— 那正是「重复消息产生两个有效发布」的形态 |
| 2026-09-21 | T21 | 发布作业的执行顺序与幂等续接 | 顺序固定为「读冻结清单 → 编码算 hash → 写对象（同 hash 路径）→ **读回校验** → 登记制品与 manifest → 判定发布」。读回校验是必需的：PUT 返回成功不等于对象真的写成功（超时/存储不足/代理截断都会让它看起来成功）。执行前先 `FindArtifactByHash` 查既有制品：上传成功但 DB 失败时重试命中它而不是重传（重传会覆盖对象）。DB 只在制品确认后才可能发布 —— `PublishReleaseIfReady` 是唯一写 `published` 的语句 |
| 2026-09-21 | T21 | T21 的收尾状态 | **已交付**：迁移 0037、hash 规则、制品 store、编码层、发布作业执行。**未交付**：下载端点（`GET P/releases/{id}/artifacts/{artifactId}/download`）与 T22 的页面 | **已交付**：迁移 0037、`internal/model/release_artifact.go`（hash 分层与规范化、可发布判定、上传校验、对象路径）、`internal/store/release_artifact_store.go`（manifest/制品登记幂等、状态机、`PublishReleaseIfReady` 唯一发布入口）、`internal/studio/release_build.go`（编码）与 `apps/worker/studio_release.go`（作业执行）及测试。**新增交付**：`internal/studio/release_build.go`（编码与校验）与其测试 —— 读**冻结清单**、按冻结的 `mapping_version_id` 取映射、JSONL **逐行分块编码**（峰值内存与批大小有关而非文件大小）、`reasoning` 字段命名映射、档位保留数组、显式的 `MaxArtifactBytes` 上限。**新增交付**：`apps/worker/studio_release.go`（`studio.release.build` 作业：读冻结清单 → 编码 → 写对象 → **读回校验** → 登记 → 发布）与 `ReleaseStore.GetReleaseByID`（作业载荷只有 releaseId，需要一条不经项目作用域的系统级读取路径；API 层一律用 GetRelease）。**未交付**：下载端点与 T22 的页面 |
| 2026-09-21 | T22 | §2.10 下载路径如何在使用「当前存储凭证」的同时保证「旧文件不漂移」 | 拆分两者：**凭证**从当前配置取（会轮换），**endpoint/bucket** 用制品记录里固化的值（切换默认存储不该让旧文件找不到）。下载前按 `artifact_hash` 校验字节：不符即拒绝下载并提示受控重建，**不回退导出最新**。文件名由服务端给出，含版本名与类型而**不含** `latest` |
| 2026-09-21 | T22 | §3 的 B03 交付库的范围 | **只含 `published` 且用户可访问的版本**：候选不是交付物，出现在交付库里会让用户下载到尚未确认的文件。搜索按版本名与用途在**服务端**过滤（前端过滤会让结果数量与真实数量不一致） |
| 2026-09-21 | T22 | 路由匹配的优先级（由守卫发现的真实缺陷）| `matchRoute` 原先只按「段数相同」匹配，于是 `/p/:projectId/releases/:releaseId` 与 `/p/:projectId/releases/new` 会互相抢：先出现的带参数路由会赢，点「准备发布」会打开某个发布的数据卡（`releaseId` 被当成 `"new"`）。已改为**字面段多者优先**（参数路径让位于固定路径），与路由框架的既定行为一致 |
| 2026-09-21 | T22 | T22 的交付状态 | **已交付**：`apps/api/routes_studio_releases.go`（创建候选/列表/数据卡/发布/下一版/固定下载/交付库）与 `ReleasePages.tsx`（列表/准备/数据卡/交付库四页）。**未交付**：M4 的端到端「设计→两个 pilot→比较→扩量→实验→判断→发布→改项目→旧下载校验」实测（属 T34 的用户任务验收），以及后续 T23–T34 |
| 2026-09-21 | T23 | §2.2 要求 GRPO 的 `levels` 至少两档，但 §5 的节点表未规定档位配在哪里 | 档位配在**蓝图生成节点的 `jsonSchema.levels`**（字符串数组）：`jsonSchema` 本来就是「输出字段约束」的自由载体，而档位列表正是 GRPO 的输出结构约束。`BlueprintGenerationNode` 属 T04 冻结 schema，新增 typed 字段需改冻结契约，故不新增。读不到档位时**报错**而不是退化到默认档位 —— 默认档位会让「用户没配」静默变成「系统替他决定了判分标准」 |
| 2026-09-21 | T23 | GRPO 与 SFT 的代码分支边界 | 共用**同一条 runner 生命周期**（批次/单元/样本版本/恢复/暂停语义一律复用 T12），只在生成器上分叉：`handleStudioBatchGenerate` 按**批次记录里的 target_kind** 选择 `grpoUnitGenerator` 或 `sftUnitGenerator`，未知类型显式失败。类别判断不取「项目当前值」：历史批次应始终按它当时的目标执行 |
| 2026-09-21 | T23 | 「不得转写 reward_records 或伪造 SFT answer」如何保证 | GRPO payload **只含** `question`/`judgePrompt`/`levels`/`levelRubrics`/`frameworkRef`，**不含** `answer`/`reasoning`/`chainOfThought`/`rewardScore`；并由测试逐字段断言。组装后立刻用 T05 的 `ValidateGRPOSamplePayload` **自检**，使「生成器认为合规、落库被拒」不可能发生（不合规在写入之前失败，而不是花了两次模型调用之后）|
| 2026-09-21 | T23 | T11 把 `json` 类型字段设为只读展示，导致 GRPO 档位在界面上无法配置 | 将 `jsonSchema` 字段改为**可编辑文本域**（`id`/`idList`/`ratioMap` 仍只读，它们需要真实候选列表）。非法 JSON 以 `{__invalid: text}` 保留原文，提交前由 `stripInvalidJSONMarkers` 清理 —— 既不丢用户输入，也不把中间态写进 payload |
| 2026-09-21 | T23 | 交付状态 | **已交付**：`apps/worker/studio_grpo.go`（GRPO 生成适配 + payload 组装与自检）、按目标类型选择生成器、蓝图 `jsonSchema` 字段可编辑，及测试。**未交付**：T24 的 GRPO 质量适配器与 T25 的 GRPO 发布 JSONL |
| 2026-09-22 | T24 | §6.3 把落点写作 `internal/eval/grpo_adapter.go` 与 `apps/worker/job_studio_eval.go`，但未规定 GRPO 量表与 SFT 量表如何在**执行侧**分离 | 明确按**维度所有权**分离：`model.LocalDimensionKeys(targetKind)` 声明确定性维度（GRPO 只有 `level_coverage`，由 `LocalJudge` 本地计算），`ExperimentRunner` 据此把量表拆成「确定性维度」与「裁判维度」两组。确定性维度只记**一行**（`judge_connection_id = 0`），模型裁判只回答其余维度（`JudgeRequest.JudgedDimensions`）。理由：把确定性维度也交给模型会得到两个来源的分，而报告无法判断该信哪个；反过来静默跳过会让它永远缺分，报告看起来只是「覆盖不足」——两种错法在界面上都不报错。runner 在量表含确定性维度但未注入 `LocalJudge` 时**显式失败**（`RunExperiment` 提前拒绝，不等逐项失败）|
| 2026-09-22 | T24 | T24 要求「缺参考样例显示缺证据，不生成假统计」，但未规定边界参考集存在哪里 | 冻结在 `experiments.target_config`（迁移 **0038**）的 typed `GRPOTargetConfig`：教师提示词版本、基准回答版本、边界参考集本体及其**内容 hash**。hash 由服务端复算并与客户端声明比对（声明一个 hash 却传另一份参考集会让「冻结来源」落空）。没有参考集不是错误：`boundary_stability` 记 `missing` 并写明原因，且**不调用模型**（不花那笔钱）；「判据覆盖不足」是真实分数（1–5 分的下界 1），只有「没有依据可判」才记缺分 |
| 2026-09-22 | T24 | §2.2 的 `levels` 至少两档已由 T23 的 payload 校验保证，但未规定「档位覆盖」的质量维度判什么 | 冻结为**结构性 + 文本级**的可计算判据（不调模型）：每一档是否在 `level_rubrics` 里有判据文本与至少一个边界例，且档位名是否出现在 `judge_prompt` 里。最后一条是必需的：判据写了而提示词没提，模型实际上不会去区分那个档位。文本级检查可能漏判同义表达，但漏判方向是「显示覆盖不足」而不是「显示覆盖充足」，可以接受。分数 = 通过档位数 / 总档位数（0–1）|
| 2026-09-22 | T24 | 契约把实验执行归给 T14，但 T14 只交付了 runner **类**：`JobKindExperimentRun` 已定义却没有任何 handler，`POST P/experiments` 也不入队 —— 实验会永远停在 `queued`，而界面显示的是「排队中」 | T24 补齐这一环：新增 `apps/worker/job_studio_eval.go`（注册 `studio.experiment.run`、真实裁判 `studioConnectionJudge`、确定性判据 `grpoLocalJudge`），并把 `CreateExperiment` 扩成 `CreateExperimentWithJob`，使实验与作业在**同一事务**内创建（T06 的硬要求）。同时给 API 加命令幂等（`experiment.create`）与 202 回放的同一信封。作业载荷只带 `experimentId`（由 store 在同事务内回填）：判据/量表/裁判/范围全部从冻结快照读，重投不会用一份过期载荷执行 |
| 2026-09-22 | T24 | 交付状态 | **已交付**：迁移 0038、`internal/model/grpo_quality.go`（GRPO 量表、typed payload、确定性档位覆盖、边界参考集与 hash、target_config）、`internal/eval/grpo_adapter.go`（GRPO 裁判提示词与逐条比对）、`internal/studio/experiment_runner.go` 的维度所有权分离、`apps/worker/job_studio_eval.go`（实验作业执行 + 真实裁判）、`apps/api/routes_studio_quality.go`（按 `target_kind` 校验、命令幂等、同事务入队）、`QualityPages.tsx` 的 GRPO 量表显示与维度中文标签，及模型/eval/studio/store 与前端测试。**新增工具**：`scripts/go-test-postgres.sh`（临时 Postgres + 全量迁移 + 容器内 `go test`），因为「`go test` 成功但集成测试全部 Skip」不满足 T32。**未交付**：GRPO 发布 JSONL（T25）、GRPO 项目在**真实 provider** 下的端到端实测（属 T34）|
| 2026-09-22 | T25 | `Record` 已有 `RewardLevels`，但 `recordFields` 把它 `strings.Join` 成逗号字符串，且没有任何 `level_rubrics` 字段 | 新增**并列**的导出字段而不改旧名：`levels`（字符串数组，原始类型）、`level_rubrics`（对象数组，`model.GRPORubricExport`）、`framework_ref`；`reward_levels`（逗号串）保留原样。理由：`{{levels}}` 是**单占位符**，`resolveAny` 会返回原始类型，因此结构不会被 `stringify` 压平；而 `reward_levels` 是第一轮冻结契约的字段名（旧 dataset 导出仍在用，且它在旧数据里本来就没有逐档判据，丢掉它不会换来更好的文件）|
| 2026-09-22 | T25 | 契约要求「新增映射与旧导出隔离」，但未规定内置映射怎么共存 | 新增内置映射 `grpo-jsonl-v2`（结构化形状），**不**就地修改旧的 `grpo-jsonl`。理由：在旧映射上就地改字段会让第一轮导出多出一个永远为 `null` 的 `level_rubrics`（旧数据里没有逐档判据），而旧导出无法修复这一点；同时 `SeedBuiltins` 的 `ON CONFLICT DO NOTHING` 是既有约定（不覆盖用户对内置映射的改动），就地改也影响不到已有部署。另：新增导出形状的字段校验器 `model.VerifyGRPOExportLine(s)`，逐行解码并断言「levels 是数组、level_rubrics 与档位一一对应、每档有判据」|
| 2026-09-22 | T25 | 「禁止 `strings.Join` 丢类型」如何由结构保证，而不是靠约定 | 三层拦截：① **候选创建时**（`store.validateReleaseTargetFormatTx`）拒绝 GRPO + 非 JSONL，并要求映射版本的格式与发布格式一致、包含四个必需字段；② **编码前**（`studio.validateGRPORelease`）在还没花掉上传配额时就再次拒绝；③ **编码后**（`model.VerifyGRPOExportLines`）逐行解码，任一行的 `levels` 不是数组（例如被裸字段名/模板拼接压成空串）即**中止发布**，并额外对账「有效行数 = 清单条数」。三层都需要：第一层给用户可操作的错误，第二层避免白花钱，第三层是唯一能拦住「字段名对了但占位符写法错了」的检查 |
| 2026-09-22 | T25 | 交付状态 | **已交付**：`internal/model/grpo_export.go`（导出 typed 形状与逐行结构校验）、`internal/exporter` 的 `levels`/`level_rubrics`/`framework_ref` 字段与 `grpo-jsonl-v2` 内置映射（与旧的 `grpo-jsonl` 隔离）、`internal/studio/release_build.go` 的 GRPO 编码前/后双重校验与 typed `loadRecord`、`internal/store/release_store.go` 的候选创建期格式与映射校验、`ReleasePages.tsx` 的 GRPO 发布提示，及模型/exporter/studio 测试。**未交付**：GRPO 项目在**真实 provider** 下「目标 → 试制 → 评估 → 判断 → 发布 → 下载」的实测（属 T34）|
| 2026-09-22 | T32 | T32 要求「Playwright BrowserRouter E2E」 | **不引入 Playwright**，改用仓库既有的「esbuild 打包生产源码 + react-dom/server 真实渲染」路线，并把路由器换成 **BrowserRouter**（安装最小 `window.history/location` 垫片）。理由：本机与 CI 运行镜像都没有 chromium，且仓库对 UI 守卫的冻结约定是「不为守卫新增依赖」（新增依赖会污染其它 lane 共享的 node_modules）；而 T32 真正要的是「不是只把新菜单设为默认」，即**真实执行路由解析**。用 BrowserRouter 而非 MemoryRouter 是必需的：只有它会走 `window.location`，因此才能覆盖「粘贴深链接/刷新」这类场景。局限已写在 `test/l15_studio_browser_router.mjs` 文件头：SSR 不执行 `useEffect`，看不到数据加载后的界面；滚动/焦点/390px 仍属 T29 与人工验收 |
| 2026-09-22 | T32 | 发现真实缺口：`internal/store/question_store_v2_test.go` 读的是 `POSTGRES_DSN`，而仓库其余集成测试与 CI 用的是 `LLM_TEST_POSTGRES_DSN` | 统一为「优先 `LLM_TEST_POSTGRES_DSN`，回退旧名 `POSTGRES_DSN`」。这个缺口意味着该文件 6 个用例**从未在 CI 跑过**，而 `go test` 仍然全绿 —— 正是 T32 验收项「检查必须执行的测试不是 Skip」指出的形态。修复后真实 DB 下 SKIP 从 11 项降到 2 项（1052 PASS）|
| 2026-09-22 | T32 | 迁移检查原来只有 `psql -c '\dt'`，只证明「有表」 | 新增 `internal/migrate/migrate_integration_test.go`（真实 Postgres）与 CI 的 `integration` job：① 先用 0021 及以前的迁移建库、插入 0021 形状夹具，再跑完剩余迁移，断言**旧数据仍在**；② 断言 `schema_migrations` 行数与目录文件数**一一对应**；③ 断言关键约束/索引/触发器存在（实验评分部分唯一索引、实验项 RESTRICT 外键、至少一名 owner 触发器、批次与样本版本的跨项目复合外键）；④ 重复执行不增加记录。CI 的 stack job 也补上 `schema_migrations` 与关键约束核对（同时查 `pg_class`/`pg_trigger`/`pg_constraint`）|
| 2026-09-22 | T32 | 交付状态 | **已交付**：`scripts/go-test-postgres.sh`（临时 Postgres + 全量迁移 + 容器内执行，修掉了 `exec` 绕过 trap 导致容器泄漏的缺陷）、`scripts/check-integration-tests.sh`（必需集成测试必须出现 `--- PASS`，否则失败）、`internal/migrate/migrate_integration_test.go`、`test/l15_studio_browser_router.mjs`（含变异自证与 artifact 报告）、CI 新增 `integration` job + UI guards 纳入浏览器路由守卫 + artifact 上传 + stack job 迁移核对。**未交付**：可控 provider/Redis/MinIO 的**全流程** API/Worker 集成（本轮只做到真实 DB + 迁移 + 已存在的集成测试集；MinIO/Redis 真实故障注入仍属缺口）、真实浏览器 E2E（需 chromium，保留 `--with-browser` 可选路径）|
| 2026-09-22 | T30 | 契约把盘点工具落点写作 `cmd/studio-migrate/`，但未规定逻辑放哪 | 逻辑落在 `internal/legacy/inventory.go`（可测、可被 T31 的导入复用），`cmd/studio-migrate/main.go` 只是薄 CLI。理由：合同的验收项（「dry-run 不写业务数据」「不调用模型」「输出各类数量与示例 ID」）必须能被测试直接断言，而写在 `main` 包里就无法从测试稳定驱动 |
| 2026-09-22 | T30 | 契约要求「dry-run 不写业务数据、不调用模型」，但未规定如何**保证**而不是靠约定 | 两条结构性保证：① 全部查询跑在 `BEGIN READ ONLY` 事务里（任何写操作被 Postgres 拒绝，测试用「在同样选项的事务里 `CREATE TEMP TABLE` 必须失败」冻结这条机制）；② `internal/legacy` 不 import `internal/llm`/`internal/eval`/`internal/studio`/`internal/exporter`，且不含写语句关键字，由源码级测试断言（去掉注释后检查）。**没有 `-apply` 开关**：T30 的原文要求就是 dry-run，而提供一个写开关再要求别人别按是把保证建立在人的记性上；真实导入属于 T31 |
| 2026-09-22 | T30 | 实测发现的真实状态：开发库迁移不完整（`schema_migrations` 21 行 vs 目录 35 个文件） | 工具由此增加一条明确行为：跳过缺失的表、在 `notes` 里以「数据库迁移不完整：缺少 N 张表（…）」显式写明，并**不产出逐个 dataset 的决策**。理由：决策依赖多张表，缺一张就会得出「没有证据 → 导入快照」这类看似合理其实错误的结论；而直接失败会让一次本可部分完成的盘点变成一个也拿不到的错误。另外 `domains` 用的是 `review_status` 而不是 `status`、`eval_runs.dimension_keys` 是 JSONB（不是 `text[]`）——两处都由真实执行踩出，已写进实现注释 |
| 2026-09-22 | T30 | 交付状态 | **已交付**：`internal/legacy/inventory.go`（29 张表状态/大小/行数盘点 + 10 类映射决策 + 显式 notes）、`cmd/studio-migrate/main.go`（CLI）、`internal/legacy/inventory_integration_test.go`（4 个真实 DB / 源码级测试）、`docs/plans/legacy-migration-report.md`（映射策略与实测结论）。**未交付**：对象存储字节存在性验证（工具不持有 MinIO/S3 凭证，已在报告里列为已知未知）、真实旧库的完整盘点（需先把目标库迁移到最新；见报告第 4 节）|
| 2026-09-22 | T33 | 契约要求「配置 `studio_enabled` 与项目级 rollout」，未规定回退的**粒度与方向** | 冻结为三条：① **零值 = 全部启用**（忘传 rollout 不能等于全禁用，否则一次漏传在生产上以「功能整体不可用」爆炸）；② **只拦写入**（read/download/manage_members/manage_workspace 始终可用，「关掉后连已发布文件都下不了」的开关会让运维不敢用）；③ **未知动作按写入处理**（将来新增的写动作不得默认绕过开关）。回退用 503 + `DEPENDENCY_UNAVAILABLE`（可重试语义）而不是 404（那会让用户以为数据丢并重复创建）。项目级名单用环境变量（`STUDIO_DISABLED_PROJECT_IDS`，支持 `id:原因`）而不是表：回退必须在**不依赖数据库写权限**的前提下可用 |
| 2026-09-22 | T33 | 契约要求观测「排队年龄/租约回收/未派发 outbox/未知成本/预算预留/评估缺分/发布失败」，未规定形态 | 做成**一个读模型**（`studio.LoadStudioHealth` + `GET /api/v1/studio/health`，管理员专属），而不是散落的指标查询：这些数字只有互相印证时才有诊断价值（「发布失败 3 + uncertain 很高 + 队列年龄 0」指向烧钱不产出；「发布失败 3 + 队列 40 分钟 + 租约回收 12 次」指向 worker 反复崩溃）。所有计数来自**既有表**，不新增埋点写入路径（埋点写失败会污染业务事务）。`notes[]` 把「怎么读」写进响应而不是只写文档：值班的人看的是接口。队列年龄与 outbox 年龄分开（两个循环是否停滞的不同证据）|
| 2026-09-22 | T33 | 契约要求「API/Worker job schema 兼容版本在部署前检查」 | 新增 `scripts/check-schema-compat.sh`：**库里有代码不认识的迁移 → 失败**（库比代码新，二进制会按旧 schema 假设写数据）；**仓库里有库未应用的迁移 → 只警告**（加性迁移由 `migrate.Run` 在启动时应用，警告是为了让运维知道这次部署会顺带执行 N 个迁移）。作业种类兼容性无法由 schema 检查得出，因此把顺序写进 runbook：迁移 → worker → API 写入口（顺序反了会造成「已排队但没人能执行」）|
| 2026-09-22 | T33 | 交付状态 | **已交付**：`internal/studio/rollout.go`（开关判据 + 状态模型 + 配置解析）、`internal/studio/observability.go`（运维快照 + `LogContext` 关联字段）、`apps/api/routes_studio_rollout.go`（`GET /api/v1/studio/rollout`、`GET /api/v1/studio/health`，管理员专属）、`Service.Authorize` 与项目创建处的写入拦截、worker 侧「在 BRPOP 之前停下」的暂停语义、`scripts/check-schema-compat.sh`、`docs/plans/studio-rollout-runbook.md`（开关/观测/部署顺序/灰度/回退/演练清单/已知缺口），及单元/真实 DB/HTTP 三层测试。**未交付**：真实环境的灰度与演练（runbook 第 6 节逐项尚未执行，因此 T33 对应的验收项不得勾选）、SLO 阈值标定（已明确写出「未标定」，不编数字）|
| 2026-09-22 | T34 | 契约要求真实用户任务验收（5–8 人、记录首次点击/迷路/阻塞/帮助次数），未规定「无法执行时」如何交付 | 冻结为：**方案 + 证据清单 + 状态对账三件套**，并把「未执行」写进文档本身（`docs/plans/atelier-acceptance-protocol.md` 开头即声明真实会话尚未执行，因此对应验收项与 DoD 条目不勾选）。理由：T34 的原文明确禁止「用模拟用户或未发生的访谈填充」，所以能交付的不是「验收结论」，而是「一份别人能照着执行且结论可被核查的方案」。第 5 节把 DoD 每一条映射到**已有自动化证据**或**待人工证据**，避免「看起来很完整其实没有证据」 |
| 2026-09-22 | T34 | 契约要求文档含「模型/费用未知边界、操作/恢复/迁移 runbook、字段 schema、版本与数据保留策略」，但未规定落点 | 落成两份新文档：`docs/plans/atelier-field-schema-and-retention.md`（样本/导出字段 schema、版本语义、保留与删除策略、模型与费用的未知边界、质量结论的置信边界）与已有的 `docs/plans/studio-rollout-runbook.md`（操作/恢复）＋`docs/plans/legacy-migration-report.md`（迁移）。另外把 T24–T33 新增的端点与字段登记进 `docs/plans/atelier-api-contract.md` §8，标明「增量交付、不改变 §1–§7」，避免新端点只存在于代码里 |
| 2026-09-22 | T34 | 交付状态 | **已交付（方案与文档部分）**：`docs/plans/atelier-acceptance-protocol.md`（参与者/任务脚本/三角色越权验证/记录表/DoD 证据清单/阻塞分级/收尾条件）、`docs/plans/atelier-field-schema-and-retention.md`（字段 schema + 版本语义 + 保留策略 + 未知费用边界 + 置信边界）、`atelier-api-contract.md` §8（T24–T33 增量端点登记）、`todo.md` 的 T01–T34 状态表（与本节一致）。**未交付（并如实标注）**：5–8 名真实参与者的会话、阻塞级问题的处置记录、真实 provider/对象存储故障注入。因此 **T34 不能勾选，总 Issue #160 也不能关闭** |
| 2026-09-22 | T26 | 契约要求「以方案创建项目复制实际配置，保留 sourceRecipeVersionId」，未规定「复制」的边界 | 冻结为：复制的是方案版本里的**内容快照**，并把它写成**新项目自己的文档版本**，同时把蓝图里的四类版本引用（覆盖/标准/质量策略/映射）**重映射**到新项目里刚创建的版本行。理由：来源蓝图里的 `coverageVersionId` 等指向来源项目的版本行，原样带过来要么违反「引用必须同项目」的复合外键（复制直接失败），要么指向不属于本项目的版本（看起来复制成功、其实引用是错的）。占位字段 `SourceRecipeVersionID` 因此从「只校验形态」升级为真实输入，并**参与创建幂等摘要**（同键配不同方案版本 → 409，而不是把另一版方案的项目当回放返回）|
| 2026-09-22 | T27 | 契约要求「动态读取持久化事件」但未规定是否新建事件表 | **不新建事件表**：动态直接读取既有的 `batch_events` 与 `audit_logs`。另建一张事件表意味着每条现有写路径都要多写一行，而漏写一处的表现是「某类事件在动态里永远不出现」——那种缺口不会报错，只会让用户以为没发生。代价是合并流的分页游标必须带**来源**（见下一条）|
| 2026-09-22 | T27 | 契约未规定多来源合并流的分页键 | 新增 `model.ActivityCursor{Time, Source, ID}`，**不复用**共享的 `Cursor{Time, ID}`：`batch_events` 与 `audit_logs` 的主键各自递增，`(时间, ID)` 不是全序，会漏行或重复行。测试逐页拉取（limit=2）断言 7 条事件**无重复无遗漏**，并含「游标未前进」的收敛保护 |
| 2026-09-22 | T27 | 「评论更正」如何与部分唯一索引共存（实测踩到 23505 与 23503） | 采用与 `experiment_scores` 相同的顺序：先 `nextval` 预分配新行 id → 标记旧行 `superseded_by = 新 id` → 用显式 id 插入新行。若先插入再标记，两行在那一刻都是 `superseded_by IS NULL`，部分唯一索引立刻报 23505；而「先标记」又需要外键**推迟到提交时检查**，因此 0033 把修订链的两个外键写成 `DEFERRABLE INITIALLY DEFERRED` |
| 2026-09-22 | T27 | 交付状态 | **已交付**：迁移 0033、`internal/model/activity.go`（待办/动态/游标/评论）、`internal/store/activity_store.go`（待办聚合、动态分页、阅读水位、评论、搜索）、`apps/api/routes_studio_activity.go`（今日工作/动态/已读/搜索/评论 4 端点）、`TodayPages.tsx`（今日工作/动态/命令搜索）、`CommentsPanel.tsx`（审阅页讨论面板）、5 个真实 DB 测试 + 前端构建。**未交付**：SSE 实时推送（T27 明确属后续优化）、真实浏览器下的 390/768/1440 与键盘走查（属 T29/T34）|
| 2026-09-22 | T28 | T03 已有项目级成员命令，T28 未说明工作区成员如何与之分工 | 新增 `internal/store/workspace_member_store.go`：工作区成员列表/添加/角色变更/移除（要求工作区管理员），与项目成员管理**职责分开**。三条规则由测试冻结：最后一名工作区管理员不可移除或降级；仍是某项目最后一名 owner 时拒绝移除并列出阻塞项目（blocker 带 `Link` 可直达）；移除成功时**同时清理**其在本工作区的项目成员关系 —— 否则界面说「已移出」而数据仍可读 |
| 2026-09-22 | T28 | 「连接页复用 provider/storage 管理能力」如何不泄露密钥 | 新增独立的只读端点 `GET /api/v1/settings/connection-options`（返回掩码标识），**不**把 `/api/v1/admin/providers` 开放给普通用户：后者是可写资源，当公共选项列表等于把治理面入口发出去。前端连接页只读，并写明「新增/修改与测试连接在管理员页、测试与保存分离」|
| 2026-09-22 | T28 | 交付状态 | **已交付**：`workspace_member_store.go`、`apps/api/routes_studio_settings.go`（工作区成员 3 端点 + connection-options + `GET P/budget`）、`SettingsPages.tsx`（连接与存储/团队与角色/帮助）、`settingsApi`、`/settings/*` 与 `/help` 翻为 available，及 3 个真实 DB 测试。**未交付**：邮件邀请（T28 明确「邮件邀请未接入则不放假按钮」）、连接/存储的测试按钮（属管理员页既有能力，本轮未改）|
| 2026-09-22 | T29 | 契约要求「离线写入不显示成功」，但未规定哪些操作可以入队 | 冻结为**允许清单**（`queueableKinds` 只含非收费、可安全重放的草稿意图）：启动运行/发布/成员与连接变更**拒绝入队**。用允许清单而不是禁止清单：新增一种写操作时它默认不可入队，必须显式加进来；反过来会让将来新增的收费操作默认可以后台重放，而那是会真实花钱的。另：条目绑定 actor（键含 userId，冲刷时校验当前账号）、离线只返回「待同步（未提交）」、TTL ≤ 15 分钟、revision 冲突保留草稿、权限失败清理并停止同步、配额失败显式报错 |
| 2026-09-22 | T29 | 「换账号不误提交」如何保证 | 队列键含 userId（与 T10 向导草稿同一形态的隐私约束），读取时再按 actor 过滤，冲刷前校验当前账号一致；换账号冲刷不提交任何内容、也不返回成功。守卫 `test/l15_offline_queue.mjs` 对这条与「收费操作拒绝入队」做了**变异自证**（放宽允许清单必须被断言捕获） |
| 2026-09-22 | T29 | 交付状态 | **已交付**：`lib/pendingQueue.ts`（允许清单/TTL/actor 绑定/冲突保留/撤权清理/配额报错）、审阅页「保存为本地草稿（待同步）」接入与待同步计数、退出账号清理队列、无障碍与窄屏 CSS（`:focus-visible`、中文长文本 `overflow-wrap`、触屏目标下限、`prefers-reduced-motion`）、守卫 `test/l15_offline_queue.mjs`（已加入 CI）|
| 2026-09-22 | T29 | **未执行（如实标注）** | 真实浏览器断网切换与 390/768/1440 实测、键盘走查、10 万样本基准（设备/并发/响应时间）。T29 的验收项要求「拟定基准并记录实测数据、不预报未经测量的性能提升」，因此本行不勾选、文档也不给数值结论 |
| 2026-09-22 | T26 | rubric 版本引用无法复制，契约未规定如何处理 | `BlueprintEvaluationNode.RubricVersionID` 指向的版本行在本轮的五类文档里没有对应类型（没有 rubric 文档）。处理：**清空并在复制结果里标记** `clearedRubricVersion`，界面提示「请重新选择量表」。不原样保留是因为它必然指向跨项目版本（见上一条）；也不静默忽略，因为那会让用户以为评估节点还能直接用 |
| 2026-09-22 | T26 | §3.1 的「全局四入口」与方案详情路由的关系 | 详情页（`/recipes/:recipeId`）放进新的 `globalDetailRoutes` 而不是 `globalRoutes`：契约 §3.1 规定「四入口」是导航项，详情页不是第五个入口。`test/l15_studio_shell.mjs` 直接断言 `globalRoutes` 数量（防止产品目标被悄悄缩减），因此把详情页混进去会（正确地）让守卫变红；守卫的路线总数与分解注释同步更新为 34 |
| 2026-09-22 | T26 | 「只有已发布版本可复制」如何落实 | 三条一起：① store 的 `RecipeVersionForCopy` 拒绝草稿（`ErrRecipeVersionNotPublished` → 409）；② 保存版本必须给**变更理由**（否则使用者无法判断该选哪一版）；③ 界面列表把「已发布版本」单独一列，草稿行只提供「发布」按钮，不画「用它建项目」（把不可用动作画在界面上，用户点下去只会拿到 409）|
| 2026-09-22 | T26 | 交付状态 | **已交付**：迁移 0032（recipes/recipe_versions + `projects.source_recipe_version_id`）、`internal/model/recipe.go`（对象、组合 payload、校验、复制结果）、`internal/store/recipe_store.go`（CRUD/可见性/发布/`CreateProjectFromRecipe`）、`internal/store/document_store.go` 抽出 `saveVersionTx`（使项目与五类文档同事务）、`apps/api/routes_recipes.go`（5 个端点）+ `POST /api/v1/projects` 的方案分发、`RecipesPages.tsx` + `globalDetailRoutes`、`.tsx` 注册与 API 客户端，及 7 个真实 DB 测试 + 前端构建/守卫。**未交付**：从既有项目「另存为方案」的一键入口（本轮方案通过 API 创建；设计页的保存按钮属 T11 后续）、方案共享范围变更的独立审计界面（变更本身已写 audit_logs）|
| 2026-09-22 | T31 | 契约要求「幂等导入、支持暂停/续跑/重复执行」，未规定幂等由几层保证 | 冻结为**三层**（任何一层单独都不够）：① 台账唯一键 `legacy_imports(source_kind, source_key)`，`completed` 时直接回放；② **内容 hash 去重**（台账因中途失败未标完成时，按 `sample_key + content_hash` 判断已导入，避免把同一份历史内容追加成 version+1）；③ **确定性 sample_key**（`legacy-<datasetId>-<questionId>`，键不稳定时第二层无从命中）。「不覆盖已迁移后产生的新版本」由**写入路径只有只追加的 `AppendSampleVersion`** 保证 —— 结构上不可能，而不是靠约定 |
| 2026-09-22 | T31 | T30 的 `needs_owner`（blocker）与 `conflict`（warning）在导入路径上如何落地 | `needs_owner`：直接拒绝并给出原因（导入本身就是一次授权，默认不给任意用户读权）。`conflict`：**拒绝自动改名**，但支持 `-target-project` 显式指定目标项目 —— 自动改名会让「旧数据 → 项目」的对应关系只有系统知道，而运维在界面上无法判断哪个是哪一个；没有显式路径的 conflict 就是死胡同 |
| 2026-09-22 | T31 | 快照批次的状态如何表达「没有生成过程」 | 批次创建后立即标为 `completed`（`completed_units = planned_units`），且**没有任何 batch_items**；`generation_config` 为空、`legacy_imports` 记录来源。理由：停在 `queued` 会显示成「排队中」，用户会等一个永远不会发生的生成；而写 batch_items 则是在假装有过生成过程。SQL 上限制「只对没有 batch_items 的批次生效」，因此它不会掩盖真实进度 |
| 2026-09-22 | T31 | 「旧路由兼容」在 API 层怎么表达 | 新增 `GET /api/v1/legacy/datasets/{id}/project`：按 `projects.legacy_dataset_id` 反查；**未映射时返回 200 + `not_mapped` 而不是 404** —— T31 要求「无法确定对象的阶段入口保留只读历史列表，不跳错项目」，而 404 会让前端把它当成错误页，丢掉「这是历史资产」的语义。旧写入口冻结（`LEGACY_WRITES_FROZEN=true`）放在**中间件层**：旧端点数以十计，逐个加检查必然漏一个，而漏掉的那个会在迁移期间继续写旧库。判定放在认证之后（未登录先得 401，不泄露运维状态） |
| 2026-09-22 | T31 | 契约把导入落点写作 `legacy_imports`（迁移 0034），未规定逻辑放哪 | 逻辑落在 `internal/legacy/import.go`（可测、与 T30 的盘点同包），store 落在 `internal/store/legacy_import_store.go`，CLI 扩展 `cmd/studio-migrate -import-dataset ... -apply`。默认 dry-run：一次误执行的导入会在新库里留下一批看起来正常的样本，而它们与真实运行出来的内容无法区分 |
| 2026-09-22 | T31 | 交付状态 | **已交付**：迁移 0034（唯一来源键 + 游标 + 分列计数 + 前后对账快照 + `projects.legacy_dataset_id` 索引）、`internal/legacy/import.go`（幂等导入 + 对账 + 失败明细）、`internal/store/legacy_import_store.go`（台账/映射/内容 hash/批次完成）、`apps/api/routes_legacy.go`（映射端点 + 冻结判定）、`apps/api/auth.go` 中间件冻结、`cmd/studio-migrate` 的导入子命令、`CreateProjectInput.LegacyDatasetID` 接线，及 9 个真实 DB 测试 + HTTP 层测试。**未交付**：按项处理而非批量（10 万条会慢，已记录）、非 SFT 来源（reasoning/reward/grpo_prompts）仍留在旧库、文件下载字节对账（属 T30 的已知未知项）|
| 2026-09-23 | T03/T28 | 默认账号的工作区引导原先在 API 启动时无条件 upsert，管理员移除普通成员后重启会把权限悄悄授回 | 新增迁移 **0039** `workspace_bootstrap_members` 作为一次性引导台账；首次引导在同一事务写入成员与台账，后续启动只检查台账，不恢复被移除的成员。真实项目创建与 workspace member upsert 都继续由目标工作区的服务端授权复核，不把默认引导当权限凭证 |
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

```text
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

`main` 基线已应用到 `0021`；Atelier 迁移从 **`0022`** 起，当前目录最新为 `0039`。禁止重写已应用迁移；下表必须与 `sql/migrations/` 文件逐一对应。

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
| 0032 | `sql/migrations/0032_studio_recipes.sql` | T26（已交付；`projects.source_recipe_version_id` 也在这里）|
| 0033 | `sql/migrations/0033_studio_activity_comments.sql` | T27（已交付：活动水位 + 评论）|
| 0034 | `sql/migrations/0034_studio_legacy_imports.sql` | T31（T30 的盘点工具不建表）|
| 0035 | `sql/migrations/0035_studio_selection_snapshots.sql` | T17（服务端大范围选择快照；URL 不承载数万 ID）|
| 0036 | `sql/migrations/0036_studio_comparison.sql` | T18（同基准比较与采用依据）|
| 0037 | `sql/migrations/0037_studio_release_artifacts.sql` | T21（不可变 manifest/制品/hash）|
| 0038 | `sql/migrations/0038_studio_grpo_quality.sql` | T24 |
| 0039 | `sql/migrations/0039_workspace_bootstrap_members.sql` | T03/T28（一次性默认工作区成员引导，保留管理员撤权）|

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
