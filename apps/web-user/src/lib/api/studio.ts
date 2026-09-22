/**
 * Atelier 项目 API 的类型与调用（Issue #160 T08）。
 *
 * 契约来源：`docs/plans/atelier-api-contract.md`。本文件的类型与该契约
 * **逐字段一致**，并由 Go 侧的契约测试
 *（`apps/api/routes_studio_contract_test.go`）断言：
 * 它用反射读出 Go 结构体的 JSON 字段名，再解析本文件里的 interface 字段，
 * 两者必须完全相同。这样「后端加了字段但前端类型没跟上」会在 CI 里失败，
 * 而不是等到某个页面读不到数据时才发现。
 *
 * 复用 `../api` 的 axios client：拦截器是**唯一**的错误本地化入口，
 * 新端点自带中文文案，旧端点仍按既有规则本地化。
 */
import { client } from '../api'

// ---------------------------------------------------------------------------
// 通用响应信封（契约 §1.1）
// ---------------------------------------------------------------------------

export type ApiLinks = Record<string, string>

/**
 * 单对象响应的稳定外壳（契约 §1.1）。
 *
 * `capabilities` 用 `unknown` 而不是某个具体能力类型：契约 §4 为每类对象
 * 定义了**不同**的能力键（批次是 canPause/canResume/canRetryFailed，
 * 文档是 canEdit/canCopy/canCompare…）。用一个联合结构会让每类对象都
 * 声明一堆与自己无关的键，前端也就无法靠「键存在」判断操作是否可用。
 * 各资源的具体能力类型见下方各自的定义。
 *
 * `data` 是对象本身：刻意不把对象字段提升到顶层 —— 不同资源的同名字段
 * （例如都有 `status`）语义不同，提升会互相覆盖。
 */
export type Envelope = {
  id: string
  status: string
  revision: number
  updatedAt: string
  capabilities: unknown
  links: ApiLinks
  warnings: string[]
  data: unknown
}

/** 服务端判定的能力位：**仅辅助 UI**，不是安全边界（契约 §4）。 */
export type ProjectCapabilities = {
  canEdit: boolean
  canRun: boolean
  canReview: boolean
  canPublish: boolean
  canDownload: boolean
  canManageMembers: boolean
}

export type BatchCapabilities = {
  canPause: boolean
  canResume: boolean
  canRetryFailed: boolean
}

export type SampleCapabilities = {
  canReview: boolean
  canViewHistory: boolean
}

export type DocumentCapabilities = {
  canEdit: boolean
  canCopy: boolean
  canCompare: boolean
}

// ---------------------------------------------------------------------------
// 错误（契约 §1.2）
// ---------------------------------------------------------------------------

export type ApiFieldError = {
  field: string
  message: string
}

/** 可跳转的阻塞项（契约 §2.8）。`link` 由服务端给出，前端不自行拼 URL。 */
export type ApiBlocker = {
  code: string
  message: string
  link?: string
}

export type ApiErrorBody = {
  code: string
  message: string
  fieldErrors?: ApiFieldError[]
  blockers?: ApiBlocker[]
  requestId: string
  retryable: boolean
}

/** 错误响应外壳：错误体**嵌在 `error` 下**（契约 §1.2）。 */
export type ApiErrorResponse = {
  error: ApiErrorBody
}

/** 契约 §1.2 的错误码。前端按 code 分支，**不解析中文文案**。 */
export const API_ERROR_CODES = {
  validation: 'VALIDATION_FAILED',
  unauthorized: 'UNAUTHORIZED',
  forbidden: 'FORBIDDEN',
  notFound: 'NOT_FOUND',
  conflict: 'CONFLICT',
  revisionConflict: 'REVISION_CONFLICT',
  idempotencyReused: 'IDEMPOTENCY_KEY_REUSED',
  budgetExhausted: 'BUDGET_EXHAUSTED',
  dependencyUnavailable: 'DEPENDENCY_UNAVAILABLE',
  rateLimited: 'RATE_LIMITED',
} as const

// ---------------------------------------------------------------------------
// 分页（契约 §1.5）
// ---------------------------------------------------------------------------

/**
 * 列表页。
 *
 * `nextCursor` 为空表示到底；`sortKey` 必须与服务端游标比较所用的键一致，
 * 否则「翻页无重复无遗漏」无法验证。
 */
export type Page<T> = {
  items: T[]
  nextCursor: string
  sortKey: string
}

// ---------------------------------------------------------------------------
// 项目概览（契约 §3）
// ---------------------------------------------------------------------------

export type VersionSummary = {
  versionId: number
  version: number
  logicalId: string
  contentHash: string
  changeReason: string
  createdAt: string
  createdBy?: number
}

/** 五类版本摘要；没有的为 null，**不伪造**一个空版本。 */
export type OverviewVersions = {
  blueprint: VersionSummary | null
  coverage: VersionSummary | null
  standard: VersionSummary | null
  qualityPolicy: VersionSummary | null
  mapping: VersionSummary | null
}

/** 批次事实计数。**不按最大 ID 猜「当前运行」**（契约 §3）。 */
export type OverviewBatches = {
  total: number
  pilot: number
  scale: number
  running: number
  paused: number
  failed: number
  completed: number
}

export type BudgetSnapshot = {
  projectId: number
  currency: string
  limitMinor: number
  reservedMinor: number
  settledMinor: number
  uncertainMinor: number
  version: number
}

/**
 * 「下一决定」。
 *
 * 由服务端按真实事实与**权限**计算：viewer 不该被指向「去启动试制」。
 */
export type NextAction = {
  kind: string
  message: string
  href: string
}

export type ProjectOverview = {
  projectId: number
  targetKind: string
  status: string
  goal: string
  versions: OverviewVersions
  batches: OverviewBatches
  budget: BudgetSnapshot
  nextAction: NextAction
}

/**
 * 分列统计（契约 §3.1）。
 *
 * `acceptanceRate` 为 null 表示**无结论**（分母为 0），不是 100%。
 * 计划量与实际产出分列，任何字段都不得相互冒充。
 */
export type SampleStats = {
  plannedQuestions: number
  generated: number
  structureValid: number
  inspected: number
  scored: number
  accepted: number
  quarantined: number
  pendingReview: number
  acceptanceRate: number | null
  acceptanceRateDisplay: string
}

export type ProjectOverviewData = ProjectOverview & {
  stats: SampleStats
}

// ---------------------------------------------------------------------------
// 批次（契约 §2.3、§2.4、§3）
// ---------------------------------------------------------------------------

export type BatchSummary = {
  batchId: number
  resourceId: string
  purpose: string
  status: string
  controlState: string
  targetKind: string
  plannedUnits: number
  completedUnits: number
  failedUnits: number
  inFlightUnits: number
  budgetCurrency: string
  budgetLimitMinor: number
  createdAt: string
  updatedAt: string
}

/** 批次级预算台账（T07）：三个计数器与上限。 */
export type BatchBudgetView = {
  currency: string
  limitMinor: number
  reservedMinor: number
  settledMinor: number
  uncertainMinor: number
}

export type BatchStep = {
  id: number
  batchId: number
  phase: string
  unitLabel: string
  status: string
  totalUnits: number
  doneUnits: number
  failedUnits: number
  errorSummary: string
}

/** 批次快照：**行内 hash + 版本 ID**，不是「当前采用」指针。 */
export type BatchSnapshot = {
  blueprintVersionId?: number
  blueprintContentHash: string
  coverageVersionId?: number
  coverageContentHash: string
  standardVersionId?: number
  standardContentHash: string
  qualityPolicyVersionId?: number
  qualityPolicyContentHash: string
  mappingVersionId?: number
  mappingContentHash: string
}

/**
 * 生成配置快照。
 *
 * `modelConnectionId` 是**非秘密**标识：凭证单独取，不冻结进快照（§2.4）。
 */
export type BatchGenerationConfig = {
  modelConnectionId: number
  modelVersion: string
  concurrency: number
  maxTokens: number
  temperature?: number
  schemaVersion: string
}

export type BatchDetail = {
  batch: BatchSummary
  steps: BatchStep[]
  snapshot: BatchSnapshot
  generationConfig: BatchGenerationConfig
  failureCount: number
  batchBudget: BatchBudgetView
  pendingJobs: number
}

/** 失败项：带**可执行建议**，不只显示机器码。 */
export type BatchFailure = {
  itemId: number
  itemKey: string
  errorClass: string
  errorMessage: string
  retryable: boolean
  suggestedAction: string
  attempts: number
}

export type BatchEvent = {
  id: number
  batchId: number
  projectId: number
  eventType: string
  sequence: number
  detail?: unknown
  actorId?: number
  createdAt: string
}

export type BatchItem = {
  id: number
  batchId: number
  projectId: number
  itemKey: string
  sampleId?: number
  status: string
  attempt: number
  errorClass: string
  errorMessage: string
  retryable: boolean
  sampleVersionId?: number
  startedAt?: string
  finishedAt?: string
  createdAt: string
  updatedAt: string
}

/** 启动批次的命令体（契约 §2.3）。 */
export type CreateBatchRequest = {
  purpose: 'pilot' | 'scale'
  blueprintVersionId: number
  coverageVersionId: number
  standardVersionId: number
  qualityPolicyVersionId: number
  mappingVersionId: number
  unitCount: number
  budget: { currency: string; limitMinor?: number }
  coverageSlice?: unknown
}

// ---------------------------------------------------------------------------
// 样本（契约 §3）
// ---------------------------------------------------------------------------

export type SampleSummary = {
  sampleId: number
  resourceId: string
  sampleKey: string
  title: string
  targetKind: string
  latestVersion: number
  originBatchId?: number
  createdAt: string
  updatedAt: string
  /** 有效处置（审阅投影）。从未被判断过的内容为 `pending`。 */
  reviewStatus: 'pending' | 'accepted' | 'quarantined' | 'conflict' | string
  /** 聚合判断序号：显示「判断改过几次」，也是发布冻结时的竞争检测依据。 */
  aggregateReviewRevision: number
  reviewConflict: boolean
}

/** 版本来源：单独一页要能回答「谁生成的、用哪一版标准与蓝图」（§4.1）。 */
export type SampleVersionSource = {
  batchId?: number
  standardVersionId?: number
  standardContentHash: string
  blueprintVersionId?: number
  blueprintContentHash: string
}

export type SampleVersionView = {
  versionId: number
  sampleId: number
  version: number
  targetKind: string
  schemaVersion: string
  contentHash: string
  batchId?: number
  batchItemId?: number
  attempt: number
  source: SampleVersionSource
  createdBy?: number
  createdAt: string
  payload: unknown
}

export type SampleDetail = {
  sample: SampleSummary
  version: SampleVersionView
}

// ---------------------------------------------------------------------------
// 人工判断（契约 §2.7）
// ---------------------------------------------------------------------------

/**
 * 一条判断。
 *
 * `supersedes` 与 `resolutionOf` 是**可追溯性**的载体：前者说出「我更正了谁」，
 * 后者说出「我裁定的是哪个冲突」。列表返回**全部**判断（含被取代的），
 * 因此界面能显示「原来判过什么、后来谁改了」。
 */
export type ReviewDecision = {
  id: number
  projectId: number
  sampleId: number
  sampleVersionId: number
  contentHash: string
  evidenceRevision: number
  reviewerId: number
  reviewerRevision: number
  action: 'accepted' | 'quarantined' | string
  reason: string
  supersedes?: number
  resolutionOf?: number
  createdAt: string
}

/** 有效处置的投影（可从判断历史重建）。 */
export type ReviewProjection = {
  sampleVersionId: number
  projectId: number
  contentHash: string
  evidenceRevision: number
  aggregateReviewRevision: number
  effectiveAction: 'pending' | 'accepted' | 'quarantined' | 'conflict' | string
  conflict: boolean
  decisionCount: number
  pendingReason: string
  updatedAt: string
}

export type ReviewAssignment = {
  id: number
  projectId: number
  sampleId: number
  sampleVersionId: number
  riskKey: string
  assigneeId?: number
  assignedBy?: number
  status: string
  note: string
  createdAt: string
  resolvedAt?: string
}

/** 提交判断的命令体。**必须**带两套序号（见 store 的说明）。 */
export type SubmitDecisionRequest = {
  /** 提交者确认的必需证据版本；与库中不一致会 409。 */
  evidenceRevision: number
  /** 该审阅者的个人并发序号；带旧值会 409 并保留填写内容。 */
  reviewerRevision: number
  action: 'accepted' | 'quarantined'
  reason: string
  supersedes?: number
}

export type DecisionResult = {
  decision: ReviewDecision
  projection: ReviewProjection
  /** 保存后立刻可知「这一条还差什么」的阻塞项（T20 复用同一份判定）。 */
  blockers: ApiBlocker[]
}

// ---------------------------------------------------------------------------
// 大范围选择快照（契约 §3.2）
// ---------------------------------------------------------------------------

/**
 * 选择快照。
 *
 * URL 里只出现快照 ID，ID 列表留在服务端 —— 数万 ID 的 URL 会超出长度上限
 * （表现为「点了发布什么都没发生」），而且它是**客户端可改的**。
 */
export type SelectionSnapshot = {
  id: number
  projectId: number
  purpose: string
  filter: unknown
  itemCount: number
  createdBy?: number
  createdAt: string
  expiresAt?: string
}

export type CreateSelectionSnapshotRequest = {
  purpose?: 'release' | 'experiment' | 'export'
  /** 少量显式选择走这条路。 */
  sampleVersionIds?: number[]
  /** 「按筛选条件全选」：由服务端解析成具体 ID 并冻结。 */
  fromFilter?: { reviewStatus?: string; batchId?: number; search?: string }
}

// ---------------------------------------------------------------------------
// 发布与交付（契约 §2.8–§2.10、§3）
// ---------------------------------------------------------------------------

export type ReleaseStatus =
  | 'candidate'
  | 'blocked'
  | 'building'
  | 'published'
  | 'build_failed'

export type ReleaseBlocker = {
  code: string
  message: string
  /** 指向具体内容版本的链接（前端不自行拼 URL）。 */
  link?: string
  field?: string
}

export type ReleaseArtifact = {
  id: number
  releaseId: number
  revision: number
  artifactType: string
  format: string
  objectKey: string
  storageEndpoint: string
  storageBucket: string
  sizeBytes: number
  contentType: string
  artifactHash: string
  itemsContentHash: string
  encoderVersion: string
  state: 'registered' | 'verified' | 'failed' | string
  errorClass: string
  errorMessage: string
  verifiedAt?: string
}

export type ReleaseRecord = {
  id: number
  projectId: number
  releaseName: string
  status: ReleaseStatus | string
  targetKind: string
  intendedUse: string
  limitations: string[]
  format: string
  candidateId: number
  candidateRevision: number
  mappingVersionId?: number
  blockers: ReleaseBlocker[]
  publishedAt?: string
  createdAt: string
  updatedAt: string
}

/** 数据卡：原范围指标、排除数量与覆盖损失都保留（§2.3）。 */
export type ReleaseCard = {
  release: ReleaseRecord
  artifacts: ReleaseArtifact[]
  manifest: unknown
  manifestHash: string
  blockers: ReleaseBlocker[]
}

export type CreateReleaseCandidateRequest = {
  releaseName: string
  /** 具体内容版本（不是筛选条件）。 */
  sampleVersionIds: number[]
  mappingVersionId: number
  format?: string
  intendedUse: string
  limitations?: string[]
  provenance?: Record<string, unknown>
}

/** 交付库条目：**只含已发布且用户可访问的版本**。 */
export type DeliveryItem = {
  releaseId: number
  projectId: number
  releaseName: string
  status: string
  intendedUse: string
  format: string
  targetKind: string
  publishedAt?: string
  page: string
}

// ---------------------------------------------------------------------------
// 质量实验（契约 §2.5、§3、§3.1）
// ---------------------------------------------------------------------------

export type ExperimentItem = {
  id: number
  experimentId: number
  projectId: number
  sampleId: number
  sampleVersionId: number
  contentHash: string
  generatorSource: string
  generatorFingerprint: string
  status: string
  attempts: number
  errorClass: string
  errorMessage: string
}

export type Experiment = {
  id: number
  projectId: number
  batchId?: number
  targetKind: string
  status: string
  purpose: string
  samplingSeed: number
  /** 冻结的**分母**：实验创建时的样本版本数，此后不随筛选/隔离变化。 */
  inspectedCount: number
  scoredCount: number
  missingCount: number
  errorCount: number
  missingScorePolicy: string
  independenceCoverage?: Record<string, number[]>
  createdAt: string
  updatedAt: string
}

export type ExperimentDimensionStat = {
  dimension: string
  weight: number
  /** 归一化后（0–1）均值，只对已评分的格取平均（缺分不补 0）。 */
  mean: number
  scoredCount: number
  missingCount: number
  covered: number
}

/** 分列统计（§3.1）：分母固定，零分母显示「无结论」而不是 100%。 */
export type ExperimentStats = {
  inspected: number
  scored: number
  missing: number
  error: number
  notApplicable: number
  pending: number
  coverage: number
  coverageDisplay: string
  acceptanceRate: number | null
  acceptanceRateDisplay: string
}

export type QualityReport = {
  experimentId: number
  status: string
  targetKind: string
  stats: ExperimentStats
  dimensions: ExperimentDimensionStat[]
  judgeNotes: string[]
  independenceCoverage?: Record<string, number[]>
}

export type ExperimentDetail = {
  experiment: Experiment
  report: QualityReport
  pendingItems: ExperimentItem[]
}

export type CreateExperimentRequest = {
  sampleVersionIds: number[]
  samplingSeed?: number
  /**
   * 量表。SFT 必须显式给出：不同数据集的「好」标准不同。
   * GRPO 省略时服务端使用**内置 GRPO 量表**（档位覆盖 / 边界稳定性 /
   * 评分解释一致性）—— 用 SFT 量表评 GRPO 样本会产出「看起来正常、
   * 其实语义错误」的结论（T24）。
   */
  rubric?: { dimensions: Array<{ key: string; label: string; weight: number; min: number; max: number }> }
  judgeConnectionIds: number[]
  missingScorePolicy?: 'exclude' | 'fail_experiment'
  batchId?: number
  /**
   * GRPO 专属冻结配置（T24）：教师提示词版本、基准回答版本与边界参考集。
   * 参考集 hash 由服务端复算，客户端传入的 hash 不被信任。
   */
  targetConfig?: {
    teacherPromptVersion?: string
    baselineAnswerVersion?: string
    boundaryReference?: {
      id?: string
      source?: string
      sampled?: boolean
      items: Array<{ level: string; input: string; expected: 'accept' | 'reject'; note?: string }>
    }
  }
}

/** 规则（质量策略版本里的一项）。取值集合由 T04 冻结。 */
export type QualityRule = {
  id: string
  name: string
  matchType: string
  expression: string
  field: string
  severity: string
  suggestedAction: string
  keywords?: string[]
}

export type RulePreviewHit = {
  ruleId: string
  ruleName: string
  field: string
  severity: string
  suggestedAction: string
  matchStart: number
  matchEnd: number
  snippet: string
}

export type RulePreviewResult = {
  policyVersionId: number
  scannedCount: number
  hits: RulePreviewHit[]
  /** 恒为 false：预览是纯读（契约 §2.6）。 */
  sideEffects: boolean
  /** 命中达上限被截断时必须显式告知。 */
  truncated: boolean
}

// ---------------------------------------------------------------------------
// 同基准比较（契约 §3 的 P06）
// ---------------------------------------------------------------------------

/** 配对观测的维度差异。`delta` = 右 - 左（正数表示右侧更好）。 */
export type PairedDimensionDiff = {
  dimension: string
  leftMean: number
  rightMean: number
  delta: number
  /** 参与该维度计算的配对数（分母，必须与 pairedCount 一起看）。 */
  pairs: number
  /** 至少一侧缺分的观测数：不参与均值、也不补 0。 */
  missing: number
}

/**
 * 可比性说明。
 *
 * `label` 与 `disclaimers` 是**报告的一部分**（不是界面文案）：
 * 换一个界面仍然要说同一件事，因此它们随数据一起返回。
 */
export type Comparability = {
  comparable: boolean
  label: string
  disclaimers: string[]
}

export type ComparisonCosts = {
  leftActualMinor: number
  leftUncertainMinor: number
  rightActualMinor: number
  rightUncertainMinor: number
  currency: string
}

export type ComparisonReport = {
  baselineId: number
  metric: string
  comparability: Comparability
  /** 配对完成数：配对口径下的**唯一有效样本量**。 */
  pairedCount: number
  leftOnlyCount: number
  rightOnlyCount: number
  dimensions: PairedDimensionDiff[]
  risks: string[]
  costs: ComparisonCosts
}

export type ComparisonBaseline = {
  id: number
  projectId: number
  inputRef: string
  coverageSlice: Record<string, unknown>
  samplingSeed: number
  rubric: { dimensions: Array<{ key: string; label: string; weight: number; min: number; max: number }> }
  judges: Array<{ connectionId: number; label: string; endpointFingerprint: string }>
  metric: string
  leftBatchId?: number
  rightBatchId?: number
  name: string
  createdAt: string
}

export type ComparisonDetail = {
  baseline: ComparisonBaseline
  report: ComparisonReport
  adopted: boolean
}

export type CreateComparisonBaselineRequest = {
  name?: string
  metric: 'paired' | 'coverage'
  /** 逐题配对**必须**固定输入问题版本（否则差异主要来自输入）。 */
  inputRef?: string
  coverageSlice?: Record<string, unknown>
  samplingSeed?: number
  rubric: ComparisonBaseline['rubric']
  judges: ComparisonBaseline['judges']
  leftBatchId: number
  rightBatchId: number
}

export type AdoptComparisonResult = {
  baseline: ComparisonBaseline
  adoptedSide: 'left' | 'right'
  adoptedBatchId: number
  /** 采用只规划新批次：这里给出扩量入口与预填参数，**不**自动创建。 */
  nextStep: { label: string; href: string; prefill: Record<string, unknown> }
}

export type AdoptedBatch = {
  adopted: boolean
  batchId?: number
  baselineId?: number
  side?: 'left' | 'right'
}

// ---------------------------------------------------------------------------
// 请求辅助
// ---------------------------------------------------------------------------

/** 列表查询参数；未定义的值不会拼进 URL。 */
export type ListParams = {
  q?: string
  status?: string
  batch?: string
  risk?: string
  purpose?: string
  limit?: number
  cursor?: string
}

/** 组装查询串。 */
function queryString(params: ListParams | undefined): string {
  if (!params) return ''
  const search = new URLSearchParams()
  for (const [key, value] of Object.entries(params)) {
    if (value === undefined || value === null || value === '') continue
    search.set(key, String(value))
  }
  const text = search.toString()
  return text ? `?${text}` : ''
}

/**
 * 写命令的额外头。
 *
 * `Idempotency-Key` 由调用方生成并**在一次用户动作内保持不变**：
 * 换一个键重试就不再是「同键同请求」，于是会真的执行第二次
 * （对启动批次来说，第二次会真实花钱）。
 */
export type CommandOptions = {
  idempotencyKey?: string
  expectedRevision?: number
}

function commandHeaders(options: CommandOptions | undefined): Record<string, string> {
  const headers: Record<string, string> = {}
  if (options?.idempotencyKey) headers['Idempotency-Key'] = options.idempotencyKey
  if (options?.expectedRevision !== undefined) headers['If-Match'] = String(options.expectedRevision)
  return headers
}

/** 生成幂等键。用 crypto 存在时用它，否则退回时间戳+随机数。 */
export function newIdempotencyKey(): string {
  if (typeof crypto !== 'undefined' && typeof crypto.randomUUID === 'function') {
    return crypto.randomUUID()
  }
  return `idem-${Date.now()}-${Math.random().toString(36).slice(2, 10)}`
}

/** 项目 API 的基路径（`P` 在契约里就是它）。 */
export function projectPath(projectId: number): string {
  return `/v1/projects/${projectId}`
}

// ---------------------------------------------------------------------------
// 具体端点
// ---------------------------------------------------------------------------

export const studioApi = {
  /** `GET P/overview`（契约 §3）。 */
  overview: (projectId: number) =>
    client.get<ProjectOverviewData>(`${projectPath(projectId)}/overview`).then((response) => response.data),

  /** `GET P/batches`（契约 §3）：服务端分页，不按最大 ID 猜「当前运行」。 */
  listBatches: (projectId: number, params?: ListParams) =>
    client
      .get<Page<BatchSummary>>(`${projectPath(projectId)}/batches${queryString(params)}`)
      .then((response) => response.data),

  /** `GET P/batches/{batchId}`（契约 §3 的 R02）。 */
  getBatch: (projectId: number, batchId: string) =>
    client.get<BatchDetail>(`${projectPath(projectId)}/batches/${batchId}`).then((response) => response.data),

  /** `GET P/batches/{batchId}/failures`（契约 §3 的 R03）。 */
  listBatchFailures: (projectId: number, batchId: string, params?: ListParams) =>
    client
      .get<Page<BatchFailure>>(`${projectPath(projectId)}/batches/${batchId}/failures${queryString(params)}`)
      .then((response) => response.data),

  /** `GET P/batches/{batchId}/items`。 */
  listBatchItems: (projectId: number, batchId: string, params?: ListParams) =>
    client
      .get<Page<BatchItem>>(`${projectPath(projectId)}/batches/${batchId}/items${queryString(params)}`)
      .then((response) => response.data),

  /** `GET P/batches/{batchId}/events`。 */
  listBatchEvents: (projectId: number, batchId: string, params?: ListParams) =>
    client
      .get<Page<BatchEvent>>(`${projectPath(projectId)}/batches/${batchId}/events${queryString(params)}`)
      .then((response) => response.data),

  /** `POST P/batches`（契约 §2.3）：202 + batchId；**幂等键必填**。 */
  createBatch: (projectId: number, payload: CreateBatchRequest, options?: CommandOptions) =>
    client
      .post(`${projectPath(projectId)}/batches`, payload, { headers: commandHeaders(options) })
      .then((response) => response.data as BatchSummary),

  /** 批次控制（契约 §2.4）：暂停只阻止新提交，在途仍会计费。 */
  pauseBatch: (projectId: number, batchId: string) =>
    client.post(`${projectPath(projectId)}/batches/${batchId}/pause`).then((response) => response.data as BatchSummary),
  resumeBatch: (projectId: number, batchId: string) =>
    client.post(`${projectPath(projectId)}/batches/${batchId}/resume`).then((response) => response.data as BatchSummary),
  /** 恢复失败项：只重跑失败/未完成项，成功内容保留。 */
  retryFailed: (projectId: number, batchId: string) =>
    client
      .post(`${projectPath(projectId)}/batches/${batchId}/retry-failed`)
      .then((response) => response.data as { batch: BatchSummary; resetItems: number }),

  /** `GET P/samples`（契约 §3）：服务端分页；`status`/`risk` 尚未接入（T16/T17）。 */
  listSamples: (projectId: number, params?: ListParams) =>
    client
      .get<Page<SampleSummary>>(`${projectPath(projectId)}/samples${queryString(params)}`)
      .then((response) => response.data),

  /** `GET P/samples/{sampleId}`（契约 §3 的 D02：内容只读）。 */
  getSample: (projectId: number, sampleId: string) =>
    client.get<SampleDetail>(`${projectPath(projectId)}/samples/${sampleId}`).then((response) => response.data),

  /** `GET P/samples/{sampleId}/history`（契约 §3 的 D03）。 */
  listSampleHistory: (projectId: number, sampleId: string, params?: ListParams) =>
    client
      .get<Page<SampleVersionView>>(`${projectPath(projectId)}/samples/${sampleId}/history${queryString(params)}`)
      .then((response) => response.data),

  /** `GET P/samples/{sampleId}/versions/{version}`。 */
  getSampleVersion: (projectId: number, sampleId: string, version: number) =>
    client
      .get<SampleVersionView>(`${projectPath(projectId)}/samples/${sampleId}/versions/${version}`)
      .then((response) => response.data),

  /** `POST P/samples/{sampleId}/versions/{version}/decisions`（契约 §2.7）。 */
  submitDecision: (projectId: number, sampleId: string, version: number, payload: SubmitDecisionRequest) =>
    client
      .post<DecisionResult>(
        `${projectPath(projectId)}/samples/${sampleId}/versions/${version}/decisions`,
        payload,
      )
      .then((response) => response.data),

  /** `GET .../decisions`：返回**全部**判断（含被取代的）与当前投影。 */
  listDecisions: (projectId: number, sampleId: string, version: number) =>
    client
      .get<{ items: ReviewDecision[]; projection: ReviewProjection; sortKey: string }>(
        `${projectPath(projectId)}/samples/${sampleId}/versions/${version}/decisions`,
      )
      .then((response) => response.data),

  /** `POST .../resolve-conflict`：仅项目负责人可用。 */
  resolveConflict: (
    projectId: number,
    sampleId: string,
    version: number,
    payload: { action: 'accepted' | 'quarantined'; reason: string; supersedes?: number },
  ) =>
    client
      .post(
        `${projectPath(projectId)}/samples/${sampleId}/versions/${version}/resolve-conflict`,
        payload,
      )
      .then((response) => response.data),

  /** `POST .../assignments`：按风险聚合，重复分派更新同一条。 */
  assignReview: (
    projectId: number,
    sampleId: string,
    version: number,
    payload: { riskKey?: string; assigneeId?: number; note?: string },
  ) =>
    client
      .post(
        `${projectPath(projectId)}/samples/${sampleId}/versions/${version}/assignments`,
        payload,
      )
      .then((response) => response.data as { assignment: ReviewAssignment }),

  /** `GET P/review-assignments`：`mine=1` 只看分派给我的。 */
  listReviewAssignments: (projectId: number, mine = false) =>
    client
      .get<Page<ReviewAssignment>>(
        `${projectPath(projectId)}/review-assignments${mine ? '?mine=1' : ''}`,
      )
      .then((response) => response.data),

  /** `POST P/selection-snapshots`：冻结范围（URL 只带快照 ID）。 */
  createSelectionSnapshot: (projectId: number, payload: CreateSelectionSnapshotRequest) =>
    client
      .post(`${projectPath(projectId)}/selection-snapshots`, payload)
      .then((response) => response.data as SelectionSnapshot),

  /** `POST P/releases`：创建候选（同事务分配 candidateId + releaseId + 版本名）。 */
  createReleaseCandidate: (projectId: number, payload: CreateReleaseCandidateRequest) =>
    client
      .post(`${projectPath(projectId)}/releases`, payload)
      .then((response) => response.data as { data: { release: ReleaseRecord; blockers: ReleaseBlocker[] } }),

  listReleases: (projectId: number) =>
    client
      .get<Page<ReleaseRecord>>(`${projectPath(projectId)}/releases`)
      .then((response) => response.data),

  /** `GET P/releases/{releaseId}`：发布 + 数据卡 + 制品 + blocker 快照。 */
  getReleaseCard: (projectId: number, releaseId: number) =>
    client
      .get(`${projectPath(projectId)}/releases/${releaseId}`)
      .then((response) => response.data as { data: ReleaseCard; status: string }),

  /** `POST .../publish`：冻结并发布（202 + building；相同命令返回同一个 release）。 */
  publishRelease: (projectId: number, releaseId: number) =>
    client
      .post(`${projectPath(projectId)}/releases/${releaseId}/publish`)
      .then((response) => response.data as { data: { release: ReleaseRecord } }),

  /** `POST .../next-candidate`：复制候选但**不改原版**。 */
  createNextCandidate: (projectId: number, releaseId: number, releaseName?: string) =>
    client
      .post(`${projectPath(projectId)}/releases/${releaseId}/next-candidate`, { releaseName })
      .then((response) => response.data as { data: { release: ReleaseRecord } }),

  /** `GET P/releases/{releaseId}/artifacts/{artifactId}/download`。 */
  downloadArtifactURL: (projectId: number, releaseId: number, artifactId: number) =>
    `/api${projectPath(projectId)}/releases/${releaseId}/artifacts/${artifactId}/download`,

  /** `GET /api/v1/deliveries`：仅已发布且我可访问的版本。 */
  listDeliveries: (params?: ListParams) =>
    client.get<Page<DeliveryItem>>(`/v1/deliveries${queryString(params)}`).then((response) => response.data),

  /** `POST P/experiments`：冻结实验（202；执行是异步的）。 */
  createExperiment: (projectId: number, payload: CreateExperimentRequest) =>
    client
      .post(`${projectPath(projectId)}/experiments`, payload)
      .then((response) => response.data as Experiment),

  listExperiments: (projectId: number) =>
    client
      .get<Page<Experiment>>(`${projectPath(projectId)}/experiments`)
      .then((response) => response.data),

  /** `GET P/experiments/{id}`：实验 + 报告 + 待判断项。 */
  getExperiment: (projectId: number, experimentId: number) =>
    client
      .get(`${projectPath(projectId)}/experiments/${experimentId}`)
      .then((response) => response.data as ExperimentDetail),

  /** `POST P/rule-previews`：纯预览（不写处置、不入收费模型队列）。 */
  previewRules: (
    projectId: number,
    payload: { qualityPolicyVersionId: number; sampleVersionIds: number[]; maxHits?: number },
  ) =>
    client
      .post(`${projectPath(projectId)}/rule-previews`, payload)
      .then((response) => response.data as RulePreviewResult),

  /** `POST P/comparison-baselines`：冻结比较前提。 */
  createComparisonBaseline: (projectId: number, payload: CreateComparisonBaselineRequest) =>
    client
      .post(`${projectPath(projectId)}/comparison-baselines`, payload)
      .then((response) => response.data as ComparisonBaseline),

  /** `GET P/comparison-baselines/{id}`：基准 + 报告（一起返回，避免两秒内自相矛盾）。 */
  getComparisonBaseline: (projectId: number, baselineId: number) =>
    client
      .get(`${projectPath(projectId)}/comparison-baselines/${baselineId}`)
      .then((response) => response.data as ComparisonDetail),

  listComparisonBaselines: (projectId: number) =>
    client
      .get<Page<ComparisonBaseline>>(`${projectPath(projectId)}/comparison-baselines`)
      .then((response) => response.data),

  /** `POST .../adopt`：采用只更新指针并记录依据，不自动运行或发布。 */
  adoptComparison: (projectId: number, baselineId: number, payload: { side: 'left' | 'right'; reason: string }) =>
    client
      .post(`${projectPath(projectId)}/comparison-baselines/${baselineId}/adopt`, payload)
      .then((response) => response.data as AdoptComparisonResult),

  /** `GET P/adopted-batch`：`/runs/new` 用它预填版本。 */
  adoptedBatch: (projectId: number) =>
    client
      .get<AdoptedBatch>(`${projectPath(projectId)}/adopted-batch`)
      .then((response) => response.data),

  /** `GET P/selection-snapshots/{id}`：**重新鉴权**后解析范围。 */
  getSelectionSnapshot: (projectId: number, snapshotId: number) =>
    client
      .get<{ snapshot: SelectionSnapshot; items: number[]; count: number }>(
        `${projectPath(projectId)}/selection-snapshots/${snapshotId}`,
      )
      .then((response) => response.data),
}

// ---------------------------------------------------------------------------
// 方案库（T26）：工作区作用域对象，因此不挂在 projectPath 下
// ---------------------------------------------------------------------------

export type RecipeLimitation = string

export type Recipe = {
  id: number
  workspaceId: number
  name: string
  nameKey: string
  description: string
  targetKind: string
  visibility: 'private' | 'workspace'
  applicableScope: string
  limitations: RecipeLimitation[]
  latestVersion: number
  /** 0 表示还没有已发布版本（此时不能用于创建项目）。 */
  publishedVersion: number
  versionCount: number
  createdAt: string
  updatedAt: string
}

export type RecipeVersion = {
  id: number
  recipeId: number
  version: number
  status: 'draft' | 'published'
  contentHash: string
  changeReason: string
  publishedAt?: string
  createdAt: string
}

/** 方案详情信封（`data` 里是 recipe + versions）。 */
export type RecipeDetail = {
  id: string
  status: string
  revision: number
  updatedAt: string
  capabilities: { canEdit?: boolean; canPublish?: boolean; canCopy?: boolean }
  links: Record<string, string>
  warnings: string[]
  data: { recipe: Recipe; versions: RecipeVersion[] }
}

/** 以方案创建项目的结果（缺连接时需要用户绑定）。 */
export type RecipeCopyResult = {
  recipeId: number
  recipeVersionId: number
  recipeName: string
  version: number
  copiedDocuments: string[]
  /** 生成节点引用的连接不可用：界面必须提示绑定，否则项目看起来配置齐全。 */
  unboundModelConnection: boolean
  unboundJudgeConnections: number
  /** 评估节点的 rubric 引用因无法跨项目映射而被清空，需要重新选择。 */
  clearedRubricVersion: boolean
  limitations: string[]
}

export const recipeApi = {
  list: (params?: { targetKind?: string; limit?: number }) =>
    client
      .get<Page<Recipe>>(`/v1/recipes${queryString(params as ListParams)}`)
      .then((response) => response.data),

  get: (recipeId: number) =>
    client.get<RecipeDetail>(`/v1/recipes/${recipeId}`).then((response) => response.data),

  create: (payload: {
    name: string
    description?: string
    targetKind: string
    visibility?: 'private' | 'workspace'
    applicableScope?: string
    limitations?: string[]
    changeReason: string
    publish?: boolean
    payload: unknown
  }) => client.post<RecipeDetail>('/v1/recipes', payload).then((response) => response.data),

  saveVersion: (
    recipeId: number,
    payload: { payload: unknown; changeReason: string; publish?: boolean },
  ) => client.post<RecipeVersion>(`/v1/recipes/${recipeId}/versions`, payload).then((response) => response.data),

  publishVersion: (recipeId: number, version: number) =>
    client
      .post<RecipeVersion>(`/v1/recipes/${recipeId}/versions/${version}/publish`)
      .then((response) => response.data),

  /**
   * 以方案创建项目。
   *
   * 走的是 `POST /v1/projects`（带 sourceRecipeVersionId）：服务端在同一事务里
   * 复制方案的五类文档并建立项目。**不是**先建项目再复制 —— 那会留下一个
   * 看起来正常但缺配置的项目。
   */
  createProjectFromRecipe: (payload: {
    sourceRecipeVersionId: number
    name: string
    goal: string
    targetKind: string
    workspaceId?: number
  }) =>
    client
      .post<{ data: { project: { id: number; name: string }; recipeCopy?: RecipeCopyResult } }>(
        '/v1/projects',
        payload,
      )
      .then((response) => response.data),
}

// ---------------------------------------------------------------------------
// 今日工作、动态、搜索与评论（T27）：工作区作用域
// ---------------------------------------------------------------------------

export type TodoItem = {
  kind: string
  projectId: number
  count: number
  summary: string
  sampledIds?: number[]
  links: Record<string, string>
  updatedAt: string
}

export type ReadWatermark = {
  userId: number
  workspaceId: number
  lastSeenAt: string
  lastSeenEventId: number
}

export type ActivityItem = {
  source: string
  eventId: number
  projectId: number
  kind: string
  actorId?: number
  summary: string
  detail?: string
  createdAt: string
  links: Record<string, string>
  unread: boolean
}

export type SearchHit = {
  kind: string
  projectId?: number
  objectId?: number
  label: string
  caption?: string
  pagePath: string
}

export type StudioComment = {
  id: number
  projectId: number
  anchorKind: 'sample_version' | 'batch'
  anchorId: number
  body: string
  mentions: number[]
  revision: number
  supersedesId?: number
  supersededBy?: number
  authorId: number
  createdAt: string
  current: boolean
}

export const activityApi = {
  /** `GET /v1/today`：待办聚合（每条带具体对象链接）。 */
  today: (workspaceId?: number) =>
    client
      .get<{ todos: TodoItem[]; watermark: ReadWatermark; notes: string[] }>(
        `/v1/today${workspaceId ? `?workspaceId=${workspaceId}` : ''}`,
      )
      .then((response) => response.data),

  /** `GET /v1/activity`：带游标增量轮询。 */
  activity: (params?: { cursor?: string; limit?: number; workspaceId?: number }) =>
    client
      .get<Page<ActivityItem> & { notes?: string[] }>(`/v1/activity${queryString(params as ListParams)}`)
      .then((response) => response.data),

  /** `POST /v1/activity/read`：只更新**当前用户**的阅读水位。 */
  markRead: (workspaceId?: number) =>
    client
      .post<{ watermark: ReadWatermark; notes: string[] }>(
        `/v1/activity/read${workspaceId ? `?workspaceId=${workspaceId}` : ''}`,
      )
      .then((response) => response.data),

  /** `GET /v1/search`：只在可访问范围内搜索。 */
  search: (keyword: string, workspaceId?: number) =>
    client
      .get<{ items: SearchHit[]; notes: string[] }>(
        `/v1/search?q=${encodeURIComponent(keyword)}${workspaceId ? `&workspaceId=${workspaceId}` : ''}`,
      )
      .then((response) => response.data),

  listComments: (projectId: number, anchorKind: string, anchorId: number) =>
    client
      .get<{ items: StudioComment[]; notes: string[]; viewerId: number }>(
        `${projectPath(projectId)}/comments?anchorKind=${anchorKind}&anchorId=${anchorId}`,
      )
      .then((response) => response.data),

  createComment: (
    projectId: number,
    payload: { anchorKind: string; anchorId: number; body: string; mentions?: number[] },
  ) => client.post<StudioComment>(`${projectPath(projectId)}/comments`, payload).then((response) => response.data),

  mentionCandidates: (projectId: number) =>
    client
      .get<Page<{ userId: number; role: string }>>(`${projectPath(projectId)}/mention-candidates`)
      .then((response) => response.data),
}
