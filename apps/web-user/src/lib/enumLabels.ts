/**
 * 枚举字段 → 用户可见中文文案的单一事实来源。
 *
 * ---------------------------------------------------------------------------
 * 为什么有第二个这样的模块（issue #98 的同类问题）
 * ---------------------------------------------------------------------------
 * `lib/datasetStatus.ts` 解决的是 `dataset.status` 的泄漏。但在**用同一种方法
 * 检查同类函数**时，父代理发现另外两个字段有**完全相同的缺陷**，且都在真实库里
 * 正被命中：
 *
 * 1. `domain.reviewStatus`（`domains` 表，migration `0003_dataset_graph.sql:16`
 *    的 `DEFAULT 'draft'`）
 *    - 合法取值：`draft`（默认）/ `approved` / `pending`
 *    - 旧实现 `reviewStatusLabel` 只处理 `approved` 与 `pending`，
 *      `draft` 落 `default: return status` → **用户看到英文 "draft"**
 *    - 实测：库里**全部 230 个 domain 的 review_status 都是 `draft`** —— 也就是说
 *      主题结构页上每一个「方向/领域」标签**当前显示的就是英文 "draft"**
 *
 * 2. `artifact.artifactType`
 *    - 取值域由导出格式决定：`internal/exporter/exporter.go:48` 的
 *      `canonicalFormats = [jsonl csv parquet alpaca sharegpt]`，
 *      经 `apps/worker/job_export_multi.go:113` 的 `spec.Format + "-export"` 落库
 *    - 旧实现 `artifactLabel` **只处理 `jsonl-export`**，其余 4 种落 default
 *    - 实测：库里已有 `sharegpt-export` 与 `alpaca-export` 两种真实工件，
 *      用户在导出交付页看到的是英文原始串
 *
 * 这两个字段与 `dataset.status` 属于**同一类缺陷**：后端定义取值域，
 * 前端手写 `switch` 只覆盖一部分，`default` 又把原始串回传给用户。
 * 因此三者共用同一套「表驱动 + 类型约束」的写法，而不是各修各的。
 *
 * ---------------------------------------------------------------------------
 * 与 datasetStatus.ts 的区别：这里测的是「**前端自己**的取值域」
 * ---------------------------------------------------------------------------
 * `dataset.status` 的取值域完全由后端决定，因此那条守卫从**后端 Go 源码**提取。
 * 而 `reviewStatus` / `artifactType` 的取值域一部分由前端写入
 * （`markAllDomains('approved')` 之类）、一部分由数据库默认值或后端格式注册表决定，
 * 没有单一权威来源可 grep。
 *
 * 因此这里改为**显式声明取值域 + 运行时兜底**：
 *   - 声明 `Record<Union, string>`，漏 key 由 tsc 报错（TS2739）；
 *   - 兜底文案由有序规则推导，**永不返回原始串**；
 *   - 由 `test/dataset_status_coverage_test.go` 的
 *     `TestLabelFunctionsMustNotLeakRawValues` 从**源码层面**断言
 *     这一类函数不再出现 `default: return <参数>` 形态。
 */

// ---------------------------------------------------------------------------
// domain.reviewStatus
// ---------------------------------------------------------------------------

/**
 * 领域/方向的复核状态。
 *
 * `draft` 来自数据库默认值（`0003_dataset_graph.sql:16`），是**新建领域的初始态**，
 * 与 `pending`（用户显式点了「批量待复核」）语义不同，因此分开给文案。
 */
export type DomainReviewStatus = 'draft' | 'pending' | 'approved'

/** 复核状态 → 中文文案。`Record<DomainReviewStatus, string>` 保证不漏 key。 */
export const DOMAIN_REVIEW_STATUS_LABELS: Record<DomainReviewStatus, string> = {
  draft: '待确认',
  pending: '待复核',
  approved: '已确认',
}

/**
 * 复核状态 → 用户可见文案。
 *
 * 契约：**永不返回原始状态串**。未知值走中性兜底「待确认」，
 * 因为在复核语境下「待确认」是安全的方向（不会让用户以为已确认）。
 */
export function describeDomainReviewStatus(raw: string): string {
  if (raw in DOMAIN_REVIEW_STATUS_LABELS) {
    return DOMAIN_REVIEW_STATUS_LABELS[raw as DomainReviewStatus]
  }
  return '待确认'
}

// ---------------------------------------------------------------------------
// artifact.artifactType
// ---------------------------------------------------------------------------

/**
 * 导出格式（`internal/exporter/exporter.go:48` 的 canonicalFormats）。
 *
 * 单独声明格式列表而不是直接写 `xxx-export` 字面量，是为了让
 * 「新增导出格式」时**只需要改这一处**：artifactType 的文案由格式名派生。
 */
export type ExportFormat = 'jsonl' | 'csv' | 'parquet' | 'alpaca' | 'sharegpt'

/** 导出格式 → 中文名称。 */
export const EXPORT_FORMAT_LABELS: Record<ExportFormat, string> = {
  jsonl: 'JSONL',
  csv: 'CSV',
  parquet: 'Parquet',
  alpaca: 'Alpaca',
  sharegpt: 'ShareGPT',
}

/**
 * 工件的 `artifactType` → 用户可见文案。
 *
 * 后端写的是 `spec.Format + "-export"`（`apps/worker/job_export_multi.go:113`）
 * 与 legacy 的固定值 `jsonl-export`（`apps/worker/main.go:464`）。
 * 因此文案由格式名派生：已知格式给「XXX 导出包」，
 * 未知格式也要给**可读**的兜底（`未识别格式导出包`），绝不回传原始串。
 */
export function describeArtifactType(raw: string): string {
  const format = raw.replace(/-export$/, '')
  if (format in EXPORT_FORMAT_LABELS) {
    return `${EXPORT_FORMAT_LABELS[format as ExportFormat]} 导出包`
  }
  if (raw.endsWith('-export')) {
    // 形如 `newformat-export`：格式名可读但前端不认识该格式。
    // 此时把格式名原样嵌进中文文案是**可接受的**：它是格式标识（用户能理解，
    // 且下游交付时需要知道格式），与泄漏内部状态机取值是两回事。
    return `${format} 导出包`
  }
  return '未识别格式导出包'
}

// ---------------------------------------------------------------------------
// artifact.contentType
// ---------------------------------------------------------------------------

/**
 * 工件的内容类型 → 用户可见文案。
 *
 * 之前 `artifactContentTypeLabel` 的 `default` 是 `return contentType || '未知类型'`，
 * 会把 `application/octet-stream` 这类原始 MIME 显示给用户。
 * 这里补齐常见类型，并把兜底改成中性中文文案。
 */
const CONTENT_TYPE_LABELS: Record<string, string> = {
  'application/jsonl': '训练数据（JSONL）',
  'application/x-ndjson': '训练数据（NDJSON）',
  'application/json': '结构化数据（JSON）',
  'text/csv': '表格数据（CSV）',
  'application/vnd.apache.parquet': '列式数据（Parquet）',
  'application/octet-stream': '二进制数据',
}

/** 内容类型 → 中文文案。未知类型给「未知类型（<子类型>）」，不直接抛 MIME 全串。 */
export function describeArtifactContentType(raw: string): string {
  if (!raw) return '未知类型'
  if (raw in CONTENT_TYPE_LABELS) {
    return CONTENT_TYPE_LABELS[raw]
  }
  // 形如 `application/xxx`：取子类型作为可读标识，避免展示完整 MIME 头
  const subtype = raw.includes('/') ? raw.split('/').pop() ?? '' : raw
  return subtype ? `未知类型（${subtype}）` : '未知类型'
}

// ---------------------------------------------------------------------------
// review_projection.effective_action（issue #191 第四条渲染路径 / #211）
// ---------------------------------------------------------------------------

/**
 * 审阅投影的有效处置。
 *
 * 取值域由数据库约束决定：`review_projections.effective_action` 的
 * CHECK 约束只允许 `pending / accepted / quarantined / conflict`。
 * 注意它与上方的 `DomainReviewStatus` **不是同一个东西**：
 * 那个是「方向/领域」的复核状态（draft/pending/approved），
 * 这个是「内容版本」的人工判断结果。取值域碰巧都有 pending，但语义不同，
 * 因此分开两张表，而不是合并 —— 合并会让「领域待复核」与「样本待判断」
 * 显示成同一句话。
 */
export type ReviewEffectiveAction = 'pending' | 'accepted' | 'quarantined' | 'conflict'

/** 有效处置 → 中文文案。`Record<ReviewEffectiveAction, string>` 保证不漏 key。 */
export const REVIEW_EFFECTIVE_ACTION_LABELS: Record<ReviewEffectiveAction, string> = {
  pending: '待判断',
  accepted: '已接纳',
  quarantined: '已隔离',
  conflict: '存在冲突',
}

/** 有效处置 → Semi Tag 颜色（与文案同一张表，避免两处各自演化）。 */
export const REVIEW_EFFECTIVE_ACTION_COLORS: Record<ReviewEffectiveAction, 'amber' | 'green' | 'red' | 'violet'> = {
  pending: 'amber',
  accepted: 'green',
  quarantined: 'red',
  conflict: 'violet',
}

/**
 * 有效处置 → 用户可见文案。
 *
 * 契约：**永不返回原始 `effective_action`**。
 * 本函数是 `ReviewPages.tsx` 原先的本地 `REVIEW_STATUS_LABEL` 的单一来源版：
 * 原实现用 `LABEL[status] ?? status` 兜底，**未知值会把内部枚举直接漏出**；
 * 且 `QualityPages.tsx` 根本没有映射（直接渲染 `sample.reviewStatus`）——
 * 那正是 #211 的实测形态。两处现在共用本函数。
 */
export function describeReviewStatus(raw: string): string {
  if (raw in REVIEW_EFFECTIVE_ACTION_LABELS) {
    return REVIEW_EFFECTIVE_ACTION_LABELS[raw as ReviewEffectiveAction]
  }
  // 未知取值给中性中文：「结论未知」不会误导用户以为已接纳，
  // 也不会把内部枚举当文案（与 describeDomainReviewStatus 同一取向）。
  return raw ? '结论未知' : '待判断'
}

/** 有效处置 → Tag 颜色（未知值给中性灰）。 */
export function reviewStatusColor(raw: string): 'amber' | 'green' | 'red' | 'violet' | 'grey' {
  if (raw in REVIEW_EFFECTIVE_ACTION_COLORS) {
    return REVIEW_EFFECTIVE_ACTION_COLORS[raw as ReviewEffectiveAction]
  }
  return 'grey'
}

/**
 * 未审阅的内容版本能否纳入评测（#211 的第二个要求：页面必须说明）。
 *
 * 返回一句面向用户的说明，而不是布尔值：调用方需要的是「怎么告诉用户」，
 * 让每个调用点自己写文案会导致同一事实在不同页面有不同说法。
 *
 * 口径来自项目内其它页面的既有约定：发布门槛用 `PENDING_REVIEW` 拦住未审阅内容
 * （见 `internal/studio` 的门槛判据）。因此评测可以纳入未审阅内容（它只是**度量**，
 * 不是对外交付），但其结论**不得**当作已验证证据用于发布。
 */
export function unreviewedScopeNotice(unreviewedCount: number): string {
  if (unreviewedCount <= 0) return ''
  return `其中 ${unreviewedCount} 个内容版本尚未人工判断。评测可以纳入它们（评测只是度量），`
    + `但未审阅内容的结论不作为发布证据，发布时仍会被门槛拦截。`
}

// ---------------------------------------------------------------------------
// 内容长度口径（issue #197 第 13 条）
// ---------------------------------------------------------------------------

/**
 * 样本 payload 字段键 → 中文文案。
 *
 * 契约与 `REVIEW_EFFECTIVE_ACTION_LABELS` 一致：**永不把原始键当文案**。
 * 这些键来自服务端长度口径（`model.SampleLengthFields`），若直接拼接就会在
 * 「数据集结构与内容分析」里漏出 `question + reasoning + answer` 这样的内部英文
 * —— 那正是 #191 的同一形态缺陷。
 */
export const SAMPLE_FIELD_LABELS: Record<string, string> = {
  question: '问题',
  reasoning: '推理过程',
  answer: '答案',
  teacherPrompt: '教师提示词',
  judge_prompt: '评判提示词',
  rewardRubric: '奖励判据',
}

/** 单个字段键 → 中文（未登记时给中性中文而不是回传英文键）。 */
export function describeSampleField(raw: string): string {
  if (!raw) return '未命名字段'
  return SAMPLE_FIELD_LABELS[raw] ?? '其它字段'
}

/**
 * 长度口径的**一句话说明**：「长度中位」到底是谁的长度。
 *
 * 为什么必须在界面上给出（issue #197 第 13 条）：`length.p50` 是多个文本字段的
 * **合计**，而卡片上只有「长度中位 / P90」这个标签。用户无法从数字本身分辨
 * 「这是问题+推理+答案之和」还是「只是问题的长度」—— 数字可见但口径不可见，
 * 与把内部键当文案是同一类失败（读得懂数字，读不懂含义）。
 *
 * 字段清单来自服务端本次统计的真实事实（`length.fields`），因此不会与实现漂移。
 */
export function describeLengthScope(fields: readonly string[]): string {
  if (fields.length === 0) return ''
  const names = fields.map(describeSampleField)
  if (names.length === 1) return `长度是「${names[0]}」的字符数；不是整条内容的长度。`
  return `长度是 ${names.join(' + ')} 的字符数合计（共 ${names.length} 个字段）；不是单条内容的长度。`
}

// ---------------------------------------------------------------------------
// audit.action（issue #191）
// ---------------------------------------------------------------------------

/**
 * 审计动作 code → 中文文案。
 *
 * 与后端 `internal/store/activity_store.go` 的 `auditActionLabels` **同源**：
 * 同一个 `action` 在「动态」里被翻译、在「操作记录 / 审计」里却漏出英文 code，
 * 是同一缺陷的两个面（issue #191 的实测形态就是
 * `操作 blueprint_version_created`）。
 *
 * 两处都需要一份映射是因为它们的渲染路径不同（Go 侧拼 summary 字符串，
 * 前端渲染表格单元格），但**取值域必须一致**；新增动作时两处都要补，
 * 一致性由 `test/l15_activity_labels.mjs` 的源码级断言守住。
 */
const AUDIT_ACTION_LABELS: Record<string, string> = {
  // 项目与工作区
  project_created_from_recipe: '用方案创建项目',
  recipe_create: '创建方案',
  recipe_version_created: '保存方案新版本',
  recipe_version_published: '发布方案版本',
  document_version_created: '保存文档新版本',
  blueprint_version_created: '保存蓝图新版本',
  coverage_version_created: '保存覆盖方案新版本',
  source_version_created: '保存素材来源新版本',
  project_source_import_queued: '提交外部来源导入',
  standard_version_created: '保存思维标准新版本',
  quality_policy_version_created: '保存质量策略新版本',
  mapping_version_created: '保存交付映射新版本',
  member_upsert: '添加项目成员',
  member_remove: '移除项目成员',
  workspace_member_upsert: '添加工作区成员',
  workspace_member_removed: '移除工作区成员',
  // 批次与生产
  batch_create: '创建批次',
  batch_pause: '暂停批次',
  batch_resume: '恢复批次',
  batch_retry_failed: '重试失败项',
  // 数据、审阅与质量
  review_decision: '提交人工判断',
  review_conflict_resolved: '处理审阅冲突',
  comparison_adopt: '采纳比较结论',
  experiment_create: '创建质量实验',
  rule_evidence_recorded: '记录规则命中证据',
  comment_created: '发表评论',
  // 发布
  release_candidate_create: '创建发布候选',
  release_freeze: '冻结发布候选',
  release_published: '发布版本',
}

/**
 * 资源名 → 中文文案。
 *
 * 这张表同时服务两个取值域（issue #191 第四条渲染路径）：
 *
 *  1. **审计动作前缀**：`describeAuditAction` 把 `blueprint_version_created`
 *     拆成资源 `blueprint_version` + 动词「创建」时用它取中文资源名；
 *  2. **审计记录的资源列**：旧控制台「系统设置 → 操作记录」的「资源」列直接
 *     渲染后端 `audit_logs.resource_type`（`sample_version` / `blueprint_version` /
 *     `workspace_member`…），此前**完全没有经过任何映射**，是 #191 在
 *     修复动作列之后**仍然漏出**的那一半。
 *
 * 取值域由后端决定（`internal/store/*_store.go` 的 `WriteAuditLog` 与
 * `writeProjectAuditTx` 实参，以及 `document_store.go` 的 `"<kind>_version"`），
 * 因此 key 集合必须以后端写入值为准；一致性由 `test/audit_resource_labels_test.go`
 * 从 Go 源码提取后断言。
 */
const AUDIT_RESOURCE_LABELS: Record<string, string> = {
  // 项目与协作
  project: '项目',
  project_member: '项目成员',
  workspace_member: '工作区成员',
  member: '项目成员',
  comment: '评论',
  // 方案与版本化文档
  recipe: '方案',
  recipe_version: '方案版本',
  blueprint_version: '蓝图版本',
  coverage_version: '覆盖方案版本',
  source_version: '来源文档版本',
  standard_version: '思维标准版本',
  quality_policy_version: '质量策略版本',
  mapping_version: '交付映射版本',
  source_version: '来源文档版本',
  export_mapping: '导出映射',
  sample_version: '样本版本',
  chain_standard: '思维链标准',
  // 下面五个是**动作前缀别名**：`describeAuditAction` 会先把
  // `blueprint_version_deleted` 这类未登记动作裁成资源前缀 `blueprint`，
  // 再取中文名。它们与上面的 `*_version` 键指向同一种业务对象，
  // 保留两套 key 是因为两个取值域的粒度不同（动作前缀 vs 资源类型）。
  blueprint: '蓝图',
  coverage: '覆盖方案',
  standard: '思维标准',
  quality_policy: '质量策略',
  mapping: '交付映射',
  // 批次、比较与规则
  batch: '批次',
  comparison_baseline: '比较基线',
  rule_evaluation: '规则评估',
  experiment: '质量实验',
  release: '发布候选',
  // 数据与生成流水线
  dataset: '数据集',
  dataset_domains: '数据集领域',
  dataset_export: '数据集导出',
  dataset_reward_levels: '奖励等级',
  generation_run: '生成运行',
  generation_strategy: '生成策略',
  direction_generation: '方向生成任务',
  question_generation: '问题生成任务',
  reasoning_generation: '推理生成任务',
  reward_generation: '奖励生成任务',
  chain_standard_generation: '思维链标准生成任务',
  grpo_prompts: 'GRPO 提示词',
  sft_records: 'SFT 记录',
  // 评估
  eval_run: '评估运行',
  eval_dimension: '评估维度',
  eval_run_judges: '评估裁判',
  // 管理与发布
  model_provider: '模型服务',
  storage_profile: '结果存储',
  prompt_template: '生成指令模板',
}

/** 资源名后缀 → 中文类型名，顺序即优先级（更长的后缀必须排在前面）。 */
const AUDIT_RESOURCE_SUFFIXES: Array<{ code: string; label: string }> = [
  { code: '_generation', label: '生成任务' },
  { code: '_version', label: '版本' },
  { code: '_export', label: '导出' },
  { code: '_judges', label: '裁判' },
  { code: '_records', label: '记录' },
  { code: '_prompts', label: '提示词' },
  { code: '_levels', label: '等级' },
  { code: '_domains', label: '领域' },
]

/**
 * 审计记录的资源类型 → 用户可见文案。
 *
 * 契约：**永不返回原始 `resourceType`**（与 `describeAuditAction` 同一条契约）。
 * 未登记的资源按后缀派生中文；连后缀都不认识时给中性的「其他资源」。
 * 原始 code 由调用方保留在 `title` 上供排查，而不是丢给用户当文案。
 */
export function describeAuditResource(raw: string): string {
  if (!raw) return '未知资源'
  if (raw in AUDIT_RESOURCE_LABELS) {
    return AUDIT_RESOURCE_LABELS[raw]
  }
  for (const rule of AUDIT_RESOURCE_SUFFIXES) {
    if (!raw.endsWith(rule.code)) continue
    const base = raw.slice(0, raw.length - rule.code.length)
    const baseLabel = AUDIT_RESOURCE_LABELS[base]
    return baseLabel ? `${baseLabel}${rule.label}` : `配置${rule.label}`
  }
  return '其他资源'
}

/** 后缀 → 中文动词，顺序即优先级（`_version_created` 必须先于 `_created`）。 */
const AUDIT_ACTION_SUFFIXES: Array<{ code: string; label: string }> = [
  { code: '_version_created', label: '保存新版本' },
  { code: '_version_published', label: '发布版本' },
  { code: '_version_deleted', label: '删除版本' },
  { code: '_created', label: '创建' },
  { code: '_updated', label: '更新' },
  { code: '_deleted', label: '删除' },
  { code: '_removed', label: '移除' },
  { code: '_upsert', label: '添加' },
  { code: '_recorded', label: '记录' },
  { code: '_published', label: '发布' },
  { code: '_resolved', label: '处理' },
  { code: '_requested', label: '请求' },
  { code: '_paused', label: '暂停' },
  { code: '_resumed', label: '恢复' },
]

/**
 * 审计动作 → 用户可见文案。
 *
 * 契约：**永不返回原始 action code**。
 * 未登记的动作按「资源 + 动词」派生中文；连后缀都不认识时给中性的「配置变更」。
 * 这与 `describeDomainReviewStatus` 的处理方式一致：兜底文案可以损失信息量，
 * 但不能把内部 code 当成界面文案。
 */
export function describeAuditAction(raw: string): string {
  if (!raw) return '未知操作'
  if (raw in AUDIT_ACTION_LABELS) {
    return AUDIT_ACTION_LABELS[raw]
  }
  for (const rule of AUDIT_ACTION_SUFFIXES) {
    if (!raw.endsWith(rule.code)) continue
    const resource = raw.slice(0, raw.length - rule.code.length)
    const resourceLabel = AUDIT_RESOURCE_LABELS[resource]
    return resourceLabel ? `${resourceLabel}${rule.label}` : `配置${rule.label}`
  }
  return '配置变更'
}
