/**
 * 数据集状态（dataset.status）的单一事实来源。
 *
 * ---------------------------------------------------------------------------
 * 为什么需要这个模块（issue #98）
 * ---------------------------------------------------------------------------
 * 后端 worker 会写入前端不认识的状态值。最典型的是 `directions_completed`
 * （`apps/worker/job_directions.go:86` / `:166`），它在 `App.tsx` 里的出现次数是 **0**：
 * 所有 `switch (status)` 都落到 `default`，而 `statusLabel` 的 default 是
 * `return status` —— 于是**原始英文内部状态串被直接显示给用户**：
 *
 *   主题：行业研究 · directions_completed · 进度 0%
 *
 * 同时进度归零、ETA 失效、主按钮指错页面（落到 `statusToActionRoute` 的 default
 * `/console/domains`，而用户此时应去「问题生成」）。这违反 功能说明.txt 的
 * 「系统设计必须符合人机交互习惯」，也违反 todo.md §14「用户页面禁止默认暴露内部实现字段」。
 *
 * ---------------------------------------------------------------------------
 * 两层防复发（两层都能在 CI 里跑，这是关键）
 * ---------------------------------------------------------------------------
 * 第 1 层（本文件，由 **tsc** 强制）：
 *   `DatasetStatus` 是**穷举联合类型**，`DATASET_STATUS_LABELS` 是
 *   `Record<DatasetStatus, string>`。于是「新增一个状态却忘了写文案」会变成
 *   **编译错误**（TS2739），CI 的 Frontend job 直接红。
 *   这与既有 `tsconfig.json` 里刚开启的 `noUnusedLocals` 是同一思路：
 *   把「靠人记得」的事交给编译器。
 *
 * 第 2 层（`test/dataset_status_coverage_test.go`，由 **go test** 强制）：
 *   从后端 Go 源码里提取**全部**会写入的状态值，断言它们都能被本模块处理。
 *   为什么必须从后端源码提取而不是前端自己声明：状态的所有权在后端，
 *   前端声明只能证明「前端自己和自己一致」，证明不了「和后端一致」——
 *   而漂移恰恰发生在两者之间。
 *
 * ---------------------------------------------------------------------------
 * 关于动态状态（重要）
 * ---------------------------------------------------------------------------
 * `apps/worker/main.go:127` 写的是 `job.Type + "_failed"`，是**运行时拼出来的**，
 * 取值取决于注册表里有哪些 job：
 *   chain-standards.generate_failed / directions.generate_failed / grpo.generate_failed /
 *   questions.generate_failed / sft.generate_failed / cleaning.run_failed /
 *   eval.run_failed / export.generate_failed
 * 这类值**不可能**逐个塞进联合类型（每加一个 job 就要改前端）。
 * 因此它们由下方 `SUFFIX_RULES` 的**后缀规则**兜住，并由第 2 层守卫断言
 * 「后端的动态拼接形式被某条后缀规则覆盖」。
 *
 * 真实库里已观察到 `chain-standards.generate_failed`，证明这条路径不是理论上的。
 */

/**
 * 前端**显式**认识的数据集状态（与后端一一对应，见各来源文件）。
 *
 * 新增状态时必须同时补 `DATASET_STATUS_LABELS` 的文案，否则 tsc 报错。
 */
export type DatasetStatus =
  // 初始（apps/api/datasets.go createDataset）
  | 'draft'
  // 主题结构确认（internal/store/dataset_store.go:334）
  | 'domains_confirmed'
  // 方向生成（apps/api/routes_directions.go:150 入队；apps/worker/job_directions.go:86/:166 完成）
  | 'directions_queued'
  | 'directions_completed'
  // 方向生成部分失败（apps/worker/job_directions.go:158 字面量）
  | 'directions_partial_failed'
  // 长链标准步骤入队（apps/api/routes_chain_standards.go:109）
  | 'chain_standards_queued'
  // 问题生成（apps/api/questions.go:30；apps/worker/job_questions_v2.go:101）
  | 'questions_queued'
  | 'questions_generated'
  | 'questions_failed'
  // 答案生成（internal/store/reasoning_store.go 的 nextStatus）
  | 'reasoning_queued'
  | 'reasoning_generated'
  | 'reasoning_partial'
  | 'reasoning_failed'
  // 质量评分（internal/store/reward_store.go 的 nextStatus）
  | 'rewards_queued'
  | 'rewards_generated'
  | 'rewards_partial'
  | 'rewards_failed'
  // 导出（apps/api/exports.go:41；apps/worker/job_export_multi.go:123）
  | 'export_queued'
  | 'export_generated'
  | 'export_failed'
  // GRPO / SFT 入队（apps/api/routes_grpo.go:111、routes_sft.go:109）
  | 'grpo_queued'
  | 'sft_queued'
  // GRPO / SFT 完成态（issue #139）。
  // 此前这两个阶段只有 queued、没有完成态 —— 因为 worker 处理器压根不推进
  // datasets.status，数据全部产出后仍显示「排队中」（实测停留 23 小时）。
  // 处理器补上推进逻辑后，这里同步补完成态文案。
  | 'sft_generated'
  | 'sft_partial'
  | 'grpo_generated'
  | 'grpo_partial'

/**
 * 状态 → 用户可见的中文文案。
 *
 * 类型是 `Record<DatasetStatus, string>`：漏一个 key 就编译不过（TS2739）。
 * 文案风格对齐既有 `statusLabel` 的措辞习惯（「…已就绪，待…」「…失败」）。
 */
export const DATASET_STATUS_LABELS: Record<DatasetStatus, string> = {
  draft: '待确认主题结构',
  domains_confirmed: '结构已确认，待生成问题',

  directions_queued: '方向生成排队中',
  // 该状态历史上直接以原始英文串显示给用户（issue #98）。
  // 文案要与 `domains_confirmed` 区分开：这里方向（level=2）也已经有了，
  // 用户接下来该去「问题生成」，而不是回到「主题结构」。
  directions_completed: '方向已生成，待生成问题',
  directions_partial_failed: '方向部分失败，需复核',

  chain_standards_queued: '长链标准步骤生成排队中',

  questions_queued: '问题生成排队中',
  questions_generated: '问题已就绪，待生成答案',
  questions_failed: '问题生成失败',

  reasoning_queued: '答案生成排队中',
  reasoning_generated: '答案已就绪，待质量评估',
  reasoning_partial: '答案部分生成，需复核',
  reasoning_failed: '答案生成失败',

  rewards_queued: '质量评分排队中',
  rewards_generated: '评估完成，可导出交付',
  rewards_partial: '评分部分完成，需复核',
  rewards_failed: '质量评分失败',

  export_queued: '导出任务排队中',
  export_generated: '导出已完成',
  export_failed: '导出失败',

  grpo_queued: 'GRPO 提示词生成排队中',
  sft_queued: 'SFT 数据生成排队中',

  // 完成态：措辞与其它阶段的「…已就绪，待…」保持一致。
  sft_generated: 'SFT 数据已就绪，待质量评估',
  sft_partial: 'SFT 数据部分生成，需复核',
  grpo_generated: 'GRPO 提示词已就绪，可导出交付',
  grpo_partial: 'GRPO 提示词部分生成，需复核',
}

/**
 * 后缀规则：兜住**运行时拼接**的状态值（见文件头「关于动态状态」）。
 *
 * 顺序敏感：`_failed` 必须在 `_queued`/`_generated` 之前判断，
 * 否则 `xxx_failed` 不会被这条规则命中（虽然当前后缀不重叠，仍显式定序以防将来加规则时踩坑）。
 */
const SUFFIX_RULES: ReadonlyArray<{ suffix: string; label: (stage: string) => string }> = [
  { suffix: '_failed', label: (stage) => `${stage || '任务'}失败` },
  { suffix: '_partial_failed', label: (stage) => `${stage || '任务'}部分失败，需复核` },
  { suffix: '_queued', label: (stage) => `${stage || '任务'}排队中` },
  { suffix: '_generated', label: (stage) => `${stage || '任务'}已完成` },
  { suffix: '_completed', label: (stage) => `${stage || '任务'}已完成` },
]

/**
 * 把后端状态串翻译成**稳定的 key**，供后缀规则用。
 *
 * 后端的 job 名带点号（`grpo.generate_failed`、`cleaning.run_failed`），
 * 直接当「阶段名」显示给用户不合适，因此取点号后的最后一段。
 * 例：`chain-standards.generate_failed` -> 去掉 `_failed` 得 `chain-standards.generate`
 *     -> 取最后一段 `generate`，但这样太笼统，因此**优先用点号前的部分**：
 *     `chain-standards` -> 展示为「长链标准步骤」。
 */
const STAGE_ALIASES: Record<string, string> = {
  'chain-standards': '长链标准步骤',
  directions: '方向',
  questions: '问题',
  reasoning: '答案',
  rewards: '质量评分',
  export: '导出',
  grpo: 'GRPO 提示词',
  sft: 'SFT 数据',
  cleaning: '清洗',
  eval: '评估',
  generate: '生成',
}

/** 从原始状态串里取出「阶段」标识，用于后缀规则的文案。 */
function stageFromRaw(raw: string): string {
  // 去掉后缀，留下 job/阶段名
  const withoutSuffix = raw.replace(/(_partial_failed|_failed|_queued|_generated|_completed)$/, '')
  // job 名形如 "grpo.generate" / "cleaning.run"：取点号前的部分作为阶段
  const stageKey = withoutSuffix.includes('.') ? withoutSuffix.split('.')[0] : withoutSuffix
  return STAGE_ALIASES[stageKey] ?? ''
}

/**
 * 状态描述结果。
 *
 * `known` 为 false 表示这条状态**没有**显式文案，是靠后缀规则或兜底文案展示的。
 * 保留这个字段是为了让测试能断言「没有原始英文泄漏」，而不是只能断言返回非空。
 */
export type DatasetStatusDescription = {
  /** 用户可见的中文文案；**保证不等于原始状态串**（除非原始串本身就是中文）。 */
  label: string
  /** 后端是否有前端显式认识的状态值。 */
  known: boolean
}

/**
 * 把任意后端状态串翻译成用户可见文案。
 *
 * 契约（由 `.mjs` 与 Go 守卫共同锁定）：
 *   1. 已知状态 → `DATASET_STATUS_LABELS` 的文案，`known: true`；
 *   2. 未知但匹配后缀规则 → 规则文案，`known: false`；
 *   3. 完全不认识 → 中性文案「状态同步中」，`known: false`；
 *   4. **任何情况下都不返回原始英文状态串**。
 */
export function describeDatasetStatus(raw: string): DatasetStatusDescription {
  if (raw in DATASET_STATUS_LABELS) {
    return { label: DATASET_STATUS_LABELS[raw as DatasetStatus], known: true }
  }
  for (const rule of SUFFIX_RULES) {
    if (raw.endsWith(rule.suffix)) {
      return { label: rule.label(stageFromRaw(raw)), known: false }
    }
  }
  return { label: '状态同步中', known: false }
}

/**
 * 状态 → 进度百分比。
 *
 * `directions_completed` 与 `domains_confirmed` 同属「结构相关阶段完成」，
 * 但方向层比领域层更进一步，因此给略高的进度。
 * 原实现把它交给 `default: return 10`，叠加 `pipeline/progress` 的 completionPercent
 * 后页面显示 0%（issue #98 的第二个症状）。
 */
export const DATASET_STATUS_PROGRESS: Record<DatasetStatus, number> = {
  draft: 15,
  domains_confirmed: 35,
  directions_queued: 35,
  // 方向（level=2）已生成：比 domains_confirmed 更进一层，但问题还没开始
  directions_completed: 45,
  directions_partial_failed: 45,
  chain_standards_queued: 45,
  questions_queued: 55,
  questions_generated: 55,
  questions_failed: 55,
  reasoning_queued: 75,
  reasoning_generated: 75,
  reasoning_partial: 75,
  reasoning_failed: 75,
  rewards_queued: 90,
  rewards_generated: 90,
  rewards_partial: 90,
  rewards_failed: 90,
  export_queued: 100,
  export_generated: 100,
  export_failed: 100,
  grpo_queued: 55,
  sft_queued: 55,
  // 完成态（issue #139）：与后端 completionByStatus 保持一致。
  sft_generated: 55,
  sft_partial: 55,
  grpo_generated: 90,
  grpo_partial: 90,
}

/**
 * 状态 → 用户该去的页面。
 *
 * `directions_completed` 与 `directions_partial_failed` 都归到「问题生成」/「主题结构」：
 * 前者方向齐了、该去生成问题；后者方向不完整、该回主题结构复核。
 * 原实现落到 `default: return '/console/domains'`，于是 `directions_completed` 会把
 * 用户送回主题结构页（issue #98 的第四个症状）。
 */
export const DATASET_STATUS_ROUTE: Record<DatasetStatus, string> = {
  draft: '/console/domains',
  domains_confirmed: '/console/questions',
  directions_queued: '/console/domains',
  directions_completed: '/console/questions',
  directions_partial_failed: '/console/domains',
  chain_standards_queued: '/console/domains',
  questions_queued: '/console/questions',
  questions_generated: '/console/questions',
  questions_failed: '/console/questions',
  reasoning_queued: '/console/reasoning',
  reasoning_generated: '/console/reasoning',
  reasoning_partial: '/console/reasoning',
  reasoning_failed: '/console/reasoning',
  rewards_queued: '/console/rewards',
  rewards_generated: '/console/rewards',
  rewards_partial: '/console/rewards',
  rewards_failed: '/console/rewards',
  export_queued: '/console/exports',
  export_generated: '/console/exports',
  export_failed: '/console/exports',
  grpo_queued: '/console/questions',
  sft_queued: '/console/questions',
  // 完成态（issue #139）：SFT 就绪后下一步是质量评估；
  // GRPO 提示词就绪后即可进入导出交付。
  sft_generated: '/console/rewards',
  sft_partial: '/console/rewards',
  grpo_generated: '/console/exports',
  grpo_partial: '/console/exports',
}
