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

  /** `GET P/selection-snapshots/{id}`：**重新鉴权**后解析范围。 */
  getSelectionSnapshot: (projectId: number, snapshotId: number) =>
    client
      .get<{ snapshot: SelectionSnapshot; items: number[]; count: number }>(
        `${projectPath(projectId)}/selection-snapshots/${snapshotId}`,
      )
      .then((response) => response.data),
}
