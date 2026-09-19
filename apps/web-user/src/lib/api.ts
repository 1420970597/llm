import axios from 'axios'

export type ApiError = Error & {
  statusCode?: number
  /**
   * 后端返回的**原文**（可能是英文内部文案）。
   *
   * 为什么保留：拦截器会把给用户看的 message 本地化（issue #103 / #107），
   * 而排查问题时需要第一手信息。原文只进 console / 埋点，**不上面向用户的界面**。
   */
  rawMessage?: string
}

const client = axios.create({
  baseURL: '/api',
  withCredentials: true,
  headers: { 'Content-Type': 'application/json' },
})

export type User = {
  id: number
  email: string
  role: 'admin' | 'user' | string
}

export type Estimate = {
  domainCount: number
  questionsPerDomain: number
  answerVariants: number
  rewardVariants: number
  estimatedQuestions: number
  estimatedSamples: number
}

export type Strategy = {
  id: number
  name: string
  description: string
  domainCount: number
  questionsPerDomain: number
  answerVariants: number
  rewardVariants: number
  planningMode: string
  isDefault?: boolean
}

export type Provider = {
  id: number
  name: string
  baseUrl: string
  model: string
  providerType: string
  reasoningEffort: string
  maxConcurrency: number
  timeoutSeconds: number
  isActive: boolean
  apiKeyMasked?: string
}

export type ProviderModelInfo = {
  id: string
}

export type ProviderModelsResponse = {
  models: ProviderModelInfo[]
}

export type ProviderConnectivityResult = {
  ok: boolean
  statusCode: number
  latencyMs: number
  message: string
  modelFound: boolean
  availableModels?: string[]
}

export type StorageProfile = {
  id: number
  name: string
  provider: string
  endpoint: string
  region: string
  bucket: string
  accessKeyId: string
  secretKeyMasked?: string
  usePathStyle: boolean
  isActive: boolean
  isDefault: boolean
}

export type PromptRecord = {
  id?: number
  name: string
  stage: string
  version: string
  systemPrompt: string
  userPrompt: string
  isActive: boolean
}

export type AuditRecord = {
  id: number
  actor: string
  action: string
  resourceType: string
  resourceId: string
  detail: string
  createdAt: string
}

export type DashboardRecord = {
  providerCount: number
  activeProviderCount: number
  storageProfileCount: number
  strategyCount: number
  promptCount: number
  auditLogCount: number
}

export type Domain = {
  id: number
  datasetId: number
  name: string
  canonicalName: string
  level: number
  parentId?: number
  source: string
  reviewStatus: string
}

export type DomainEdge = {
  id: number
  datasetId: number
  sourceId: number
  targetId: number
  relation: string
}

export type Dataset = {
  id: number
  name: string
  rootKeyword: string
  targetSize: number
  status: string
  strategyId: number
  providerId: number
  storageProfileId: number
  targetKind: 'sft' | 'grpo' | string
  directionCount: number
  questionsPerDirection: number
  rewardLevels: string[]
  cleaningEnabled: boolean
  /** 最后一次失败的**用户可见**中文原因（issue #83）；空串表示无失败原因。 */
  failureReason: string
  estimate: Estimate
  createdAt: string
  updatedAt: string
}

export type DatasetGraph = {
  dataset: Dataset
  domains: Domain[]
  edges: DomainEdge[]
}

export type Question = {
  id: number
  datasetId: number
  domainId: number
  domainName: string
  directionDomainId: number
  content: string
  canonicalHash: string
  dedupeKey: string
  difficulty: 'easy' | 'medium' | 'hard' | string
  difficultyScore: number
  source: string
  cleaningStatus: 'clean' | 'flagged' | 'dropped' | string
  status: string
  createdAt: string
  updatedAt: string
}

export type DifficultyStats = {
  levels: Record<string, number>
  total: number
}

export type ChainStep = {
  index: number
  title: string
  description: string
  checkpoint: string
}

export type ChainStandard = {
  id: number
  datasetId: number
  domainId: number
  domainName: string
  directionKey: string
  currentVersion: number
  status: string
  steps: ChainStep[]
  createdAt: string
  updatedAt: string
}

export type ChainStandardVersion = {
  id: number
  standardId: number
  version: number
  steps: ChainStep[]
  source: string
  changeNote: string
  createdBy: number
  createdAt: string
}

export type GrpoLevelRubric = {
  level: string
  label: string
  criteria: string
  acceptCase: string
  rejectCase: string
}

export type GrpoPrompt = {
  id: number
  datasetId: number
  questionId: number
  questionText: string
  domainId: number
  domainName: string
  levels: string[]
  judgePrompt: string
  levelRubrics: GrpoLevelRubric[]
  frameworkRef: string
  status: string
  createdAt: string
  updatedAt: string
}

export type SftRecord = {
  id: number
  datasetId: number
  questionId: number
  questionText: string
  domainId: number
  domainName: string
  chainOfThought: string
  answer: string
  chainSteps: ChainStep[]
  status: string
  createdAt: string
  updatedAt: string
}

export type GenerationRun = {
  id: number
  datasetId: number
  stage: string
  status: string
  cursor: Record<string, unknown>
  totalUnits: number
  doneUnits: number
  attempts: number
  errorSummary: string
  startedAt?: string
  finishedAt?: string
  createdAt: string
  updatedAt: string
}

export type ExportMapping = {
  id: number
  name: string
  format: string
  targetKind: string
  fieldMap: Record<string, unknown>
  options: Record<string, unknown>
  isBuiltin: boolean
  isDefault: boolean
  createdAt: string
  updatedAt: string
}

export type ExportFormatList = {
  formats: string[]
  mappings: ExportMapping[]
}

export type EvalDimension = {
  id: number
  key: string
  name: string
  category: string
  description: string
  rubric: string
  scaleMin: number
  scaleMax: number
  isBuiltin: boolean
  isActive: boolean
  weight: number
  createdAt: string
  updatedAt: string
}

export type EvalRun = {
  id: number
  datasetId: number
  name: string
  samplingMode: 'full' | 'ratio' | 'count' | string
  sampleRatio: number
  sampleSize: number
  targetKind: string
  dimensionKeys: string[]
  judgeProviderIds: number[]
  generatorProviderId: number
  status: string
  totalItems: number
  scoredItems: number
  errorSummary: string
  createdBy: number
  createdAt: string
  updatedAt: string
}

export type EvalRunJudge = {
  id: number
  evalRunId: number
  providerId: number
  providerName: string
  model: string
  excluded: boolean
  excludeReason: string
  status: string
  scoredItems: number
  errorSummary: string
  createdAt: string
}

export type EvalJudgeOption = {
  providerId: number
  providerName: string
  model: string
  isActive: boolean
  excluded: boolean
  excludeReason: string
}

/** 某数据集的裁判候选集：excluded 已按该数据集的生成者算好。 */
export type DatasetJudgeOptions = {
  datasetId: number
  generatorProviderId: number
  judges: EvalJudgeOption[]
}

export type EvalItem = {
  id: number
  evalRunId: number
  datasetId: number
  questionId: number
  itemIndex: number
  payload: Record<string, unknown>
  createdAt: string
}

export type EvalItemScore = {
  id: number
  evalRunId: number
  evalItemId: number
  judgeProviderId: number
  dimensionKey: string
  score: number
  rationale: string
  rawResponse: string
  status: string
  createdAt: string
}

export type EvalDimensionStat = {
  dimensionKey: string
  name: string
  category: string
  score: number
  sampleCount: number
  stdDev: number
  min: number
  max: number
}

export type EvalItemScoreBrief = {
  questionId: number
  itemIndex: number
  score: number
}

export type EvalJudgeStat = {
  providerId: number
  providerName: string
  model: string
  score: number
  sampleCount: number
  dimensions: EvalDimensionStat[]
  itemScores: EvalItemScoreBrief[]
}

export type EvalReport = {
  evalRun: EvalRun
  datasetName: string
  overallScore: number
  judgeAgreement: number
  sampleCount: number
  judges: EvalJudgeStat[]
  dimensions: EvalDimensionStat[]
  weakestItems: EvalItemScoreBrief[]
  conclusions: string[]
  generatedAt: string
}

export type EvalRunDetail = {
  run: EvalRun
  judges: EvalRunJudge[]
  dimensions: EvalDimension[]
}

export type CleaningKeyword = {
  id: number
  pattern: string
  category: string
  matchMode: 'contains' | 'regex' | 'prefix' | string
  severity: 'block' | 'warn' | string
  isBuiltin: boolean
  isActive: boolean
  note: string
  createdAt: string
  updatedAt: string
}

export type CleaningRule = {
  id: number
  name: string
  stageScope: string[]
  minHits: number
  action: 'drop' | 'flag' | 'retry' | string
  priority: number
  isActive: boolean
  config: Record<string, unknown>
  createdAt: string
  updatedAt: string
}

export type CleaningRun = {
  id: number
  datasetId: number
  stages: string[]
  status: string
  scannedItems: number
  flaggedItems: number
  droppedItems: number
  report: Record<string, unknown>
  errorSummary: string
  createdAt: string
  updatedAt: string
}

export type CleaningFinding = {
  id: number
  cleaningRunId: number
  datasetId: number
  questionId: number
  stage: string
  keywordId: number
  matchedText: string
  snippet: string
  action: string
  createdAt: string
}

export type CleaningStageStat = {
  stage: string
  scannedItems: number
  flaggedItems: number
  droppedItems: number
  hitRate: number
}

export type CleaningKeywordStat = {
  keywordId: number
  pattern: string
  category: string
  hits: number
  sampleSnippet: string
}

export type CleaningReport = {
  run: CleaningRun
  stages: CleaningStageStat[]
  topKeywords: CleaningKeywordStat[]
  conclusions: string[]
  generatedAt: string
}

export type ReasoningRecord = {
  id: number
  datasetId: number
  questionId: number
  questionText: string
  answerSummary: string
  reasoning: string
  objectKey: string
  status: string
  createdAt: string
  updatedAt: string
}

export type RewardRecord = {
  id: number
  datasetId: number
  questionId: number
  questionText: string
  score: number
  objectKey: string
  status: string
  createdAt: string
  updatedAt: string
}

export type Artifact = {
  id: number
  datasetId: number
  artifactType: string
  objectKey: string
  contentType: string
  createdAt: string
}

export type ArtifactDownload = {
  downloadUrl: string
}

export type PipelineStageStatus = {
  key: string
  label: string
  state: 'pending' | 'queued' | 'in_progress' | 'completed' | 'failed'
  count: number
  summary: string
}

export type PipelineProgress = {
  datasetId: number
  datasetStatus: string
  currentStage: string
  completionPercent: number
  stages: PipelineStageStatus[]
  questionCount: number
  reasoningCount: number
  rewardCount: number
  artifactCount: number
}

export type StageEnqueueResult = {
  datasetId: number
  stage: string
  state: string
  message: string
  acceptedAt: string
}

export type RuntimeStatus = {
  datasetCount: number
  questionCount: number
  reasoningCount: number
  rewardCount: number
  artifactCount: number
  queueDepth: number
}

function unwrap<T>(promise: Promise<{ data: T }>): Promise<T> {
  return promise.then((response) => response.data)
}

export const authApi = {
  login: (payload: { email: string; password: string }) => unwrap(client.post<{ user: User }>('/v1/auth/login', payload)),
  me: () => unwrap(client.get<{ user: User }>('/v1/auth/me')),
  logout: () => unwrap(client.post<{ ok: boolean }>('/v1/auth/logout')),
}

export const consoleApi = {
  dashboard: () => unwrap(client.get<DashboardRecord>('/v1/admin/dashboard')),
  listProviders: () => unwrap(client.get<Provider[]>('/v1/admin/providers')),
  saveProvider: (payload: Partial<Provider> & { apiKey?: string }) => unwrap(client.request<Provider>({ url: '/v1/admin/providers', method: payload.id ? 'PUT' : 'POST', data: payload })),
  fetchProviderModels: (payload: Partial<Provider> & { apiKey?: string }) => unwrap(client.post<ProviderModelsResponse>('/v1/admin/providers/models', payload)),
  testProviderConnectivity: (payload: Partial<Provider> & { apiKey?: string }) => unwrap(client.post<ProviderConnectivityResult>('/v1/admin/providers/test', payload)),
  listStorageProfiles: () => unwrap(client.get<StorageProfile[]>('/v1/admin/storage-profiles')),
  saveStorageProfile: (payload: Partial<StorageProfile> & { secretAccessKey?: string }) => unwrap(client.request<StorageProfile>({ url: '/v1/admin/storage-profiles', method: payload.id ? 'PUT' : 'POST', data: payload })),
  listStrategies: () => unwrap(client.get<Strategy[]>('/v1/admin/generation-strategies')),
  saveStrategy: (payload: Partial<Strategy>) => unwrap(client.request<Strategy>({ url: '/v1/admin/generation-strategies', method: payload.id ? 'PUT' : 'POST', data: payload })),
  listPrompts: () => unwrap(client.get<PromptRecord[]>('/v1/admin/prompts')),
  savePrompt: (payload: PromptRecord) => unwrap(client.request<PromptRecord>({ url: '/v1/admin/prompts', method: payload.id ? 'PUT' : 'POST', data: payload })),
  listAuditLogs: () => unwrap(client.get<AuditRecord[]>('/v1/admin/audit-logs')),
  estimatePlan: (payload: { rootKeyword: string; targetSize: number; strategyId: number }) => unwrap(client.post<Estimate>('/v1/datasets/plans/estimate', payload)),
  createDataset: (payload: Record<string, unknown>) => unwrap(client.post<Dataset>('/v1/datasets', payload)),
  listDatasets: () => unwrap(client.get<Dataset[]>('/v1/datasets')),
  getDataset: (id: number) => unwrap(client.get<DatasetGraph>(`/v1/datasets/${id}`)),
  generateDomains: (id: number) => unwrap(client.post<DatasetGraph>(`/v1/datasets/${id}/domains/generate`)),
  updateGraph: (id: number, domains: Domain[]) => unwrap(client.post<{ updated: number }>(`/v1/datasets/${id}/domains/graph`, { domains })),
  confirmDomains: (id: number) => unwrap(client.post<{ status: string }>(`/v1/datasets/${id}/domains/confirm`)),
  generateQuestions: (id: number) => unwrap(client.post<StageEnqueueResult>(`/v1/datasets/${id}/questions/generate`)),
  listQuestions: (id: number) => unwrap(client.get<Question[]>(`/v1/datasets/${id}/questions`)),
  generateReasoning: (id: number) => unwrap(client.post<StageEnqueueResult>(`/v1/datasets/${id}/reasoning/generate`)),
  listReasoning: (id: number) => unwrap(client.get<ReasoningRecord[]>(`/v1/datasets/${id}/reasoning`)),
  generateRewards: (id: number) => unwrap(client.post<StageEnqueueResult>(`/v1/datasets/${id}/rewards/generate`)),
  listRewards: (id: number) => unwrap(client.get<RewardRecord[]>(`/v1/datasets/${id}/rewards`)),
  generateExport: (id: number) => unwrap(client.post<StageEnqueueResult>(`/v1/datasets/${id}/export`)),
  listArtifacts: (id: number) => unwrap(client.get<Artifact[]>(`/v1/datasets/${id}/export`)),
  artifactDownloadUrl: (datasetId: number, artifactId: number) => `/api/v1/datasets/${datasetId}/export/download?artifactId=${artifactId}`,
  /**
   * 下载导出工件（二进制响应）。
   *
   * 为什么走共享的 axios client 而不是裸 fetch：
   * 本模块全部 74 个接口都走 `client`，它带着
   *   - `withCredentials: true`（同源 Cookie）
   *   - 响应拦截器（401 → 「登录状态已失效」、403 → 权限提示、统一中文错误文案）
   * 而裸 fetch 会**绕过整个拦截器**：会话过期时用户看到的是裸 HTTP 错误，
   * 而不是可理解的中文提示；错误文案也与全站不一致。
   *
   * `responseType: 'blob'`：工件是二进制（jsonl 等），不能按 JSON 解析。
   * 拦截器对 blob 错误体的处理已在下面注释说明。
   */
  downloadArtifactBlob: (datasetId: number, artifactId: number) =>
    client
      .get<Blob>(`/v1/datasets/${datasetId}/export/download`, {
        params: { artifactId },
        responseType: 'blob',
      })
      .then((response) => response),
  /** 从 Content-Disposition 解析文件名；缺失时回退到对象键的末段。 */
  artifactFileName: (disposition: string | undefined, fallbackObjectKey: string) => {
    const matched = (disposition ?? '').match(/filename="?([^";]+)"?/)
    if (matched?.[1]) return matched[1]
    return fallbackObjectKey.split('/').pop() || 'dataset-export.jsonl'
  },
  pipelineProgress: (id: number) => unwrap(client.get<PipelineProgress>(`/v1/datasets/${id}/pipeline/progress`)),
  runtimeStatus: () => unwrap(client.get<RuntimeStatus>('/v1/platform/runtime')),

  // ---- L1 方向生成（n 领域 → m 方向，断点续跑） ----
  generateDirections: (id: number, directionCount?: number) =>
    unwrap(client.post<StageEnqueueResult>(`/v1/datasets/${id}/directions/generate`, { directionCount: directionCount ?? 0 })),
  listDirections: (id: number) => unwrap(client.get<Domain[]>(`/v1/datasets/${id}/directions`)),
  listGenerationRuns: (id: number) => unwrap(client.get<GenerationRun[]>(`/v1/datasets/${id}/generation-runs`)),
  resumeStage: (id: number, stage: string) =>
    unwrap(client.post<StageEnqueueResult>(`/v1/datasets/${id}/generation-runs/${stage}/resume`)),

  // ---- L2 长链思维标准步骤（可编辑、版本化） ----
  generateChainStandards: (id: number, domainIds: number[] = []) =>
    unwrap(client.post<StageEnqueueResult>(`/v1/datasets/${id}/chain-standards/generate`, { domainIds })),
  listChainStandards: (id: number) => unwrap(client.get<ChainStandard[]>(`/v1/datasets/${id}/chain-standards`)),
  updateChainStandard: (id: number, domainId: number, steps: ChainStep[], changeNote: string) =>
    unwrap(client.put<ChainStandard>(`/v1/datasets/${id}/chain-standards/${domainId}`, { steps, changeNote })),
  listChainStandardVersions: (id: number, domainId: number) =>
    unwrap(client.get<ChainStandardVersion[]>(`/v1/datasets/${id}/chain-standards/${domainId}/versions`)),

  // ---- L3 问题生成（x 可控、去重、难度分层） ----
  generateQuestionsV2: (id: number, questionsPerDirection: number, difficultyMix?: Record<string, number>) =>
    unwrap(client.post<StageEnqueueResult>(`/v1/datasets/${id}/questions/generate`, {
      questionsPerDirection,
      difficultyMix: difficultyMix ?? {},
    })),
  questionDifficultyStats: (id: number) =>
    unwrap(client.get<DifficultyStats>(`/v1/datasets/${id}/questions/difficulty-stats`)),

  // ---- L4 GRPO 教师模型评判提示词 ----
  setRewardLevels: (id: number, levels: string[]) =>
    unwrap(client.put<{ levels: string[] }>(`/v1/datasets/${id}/reward-levels`, { levels })),
  generateGrpo: (id: number, levels: string[] = []) =>
    unwrap(client.post<StageEnqueueResult>(`/v1/datasets/${id}/grpo/generate`, { levels })),
  listGrpo: (id: number) => unwrap(client.get<GrpoPrompt[]>(`/v1/datasets/${id}/grpo`)),

  // ---- L5 SFT 思维链 + 答案 ----
  generateSft: (id: number, includeAnswer = true) =>
    unwrap(client.post<StageEnqueueResult>(`/v1/datasets/${id}/sft/generate`, { includeAnswer })),
  listSft: (id: number) => unwrap(client.get<SftRecord[]>(`/v1/datasets/${id}/sft`)),

  // ---- L6 多格式导出 ----
  exportFormats: (id: number) => unwrap(client.get<ExportFormatList>(`/v1/datasets/${id}/export/formats`)),
  exportDataset: (id: number, format: string, mappingId = 0, filters: Record<string, unknown> = {}) =>
    unwrap(client.post<StageEnqueueResult>(`/v1/datasets/${id}/export`, { format, mappingId, filters })),
  listExportMappings: () => unwrap(client.get<ExportMapping[]>('/v1/admin/export-mappings')),
  saveExportMapping: (payload: Partial<ExportMapping>) =>
    unwrap(client.request<ExportMapping>({ url: '/v1/admin/export-mappings', method: payload.id ? 'PUT' : 'POST', data: payload })),

  // ---- L7 评估裁判模型接入 ----
  /** 全局候选列表：没有数据集上下文，excluded 恒为 false。做选择器请用 listDatasetEvalJudges。 */
  listEvalJudges: () => unwrap(client.get<EvalJudgeOption[]>('/v1/admin/eval/judges')),
  /** 按数据集取候选，excluded/excludeReason 已按该数据集生成者计算。 */
  listDatasetEvalJudges: (datasetId: number) =>
    unwrap(client.get<DatasetJudgeOptions>(`/v1/datasets/${datasetId}/eval-judges`)),
  setEvalRunJudges: (runId: number, providerIds: number[]) =>
    unwrap(client.put<EvalRunJudge[]>(`/v1/eval/runs/${runId}/judges`, { providerIds })),

  // ---- L8 评估维度 ----
  listEvalDimensions: (params: { category?: string; builtin?: boolean } = {}) =>
    unwrap(client.get<EvalDimension[]>('/v1/eval/dimensions', { params })),
  saveEvalDimension: (payload: Partial<EvalDimension>) =>
    unwrap(client.request<EvalDimension>({ url: '/v1/eval/dimensions', method: payload.id ? 'PUT' : 'POST', data: payload })),
  deleteEvalDimension: (id: number) => unwrap(client.delete<{ deleted: boolean }>(`/v1/eval/dimensions/${id}`)),
  seedEvalDimensions: () => unwrap(client.post<{ inserted: number; total: number }>('/v1/eval/dimensions/seed')),
  evalDimensionCategories: () => unwrap(client.get<{ categories: string[] }>('/v1/eval/dimensions/categories')),

  // ---- L9 评估运行（全量 / 抽样 / 逐条多维打分） ----
  createEvalRun: (payload: {
    datasetId: number
    name: string
    samplingMode: 'full' | 'ratio' | 'count'
    sampleRatio?: number
    sampleSize?: number
    targetKind?: string
    dimensionKeys: string[]
    judgeProviderIds: number[]
    generatorProviderId?: number
  }) => unwrap(client.post<EvalRun>('/v1/eval/runs', payload)),
  listEvalRuns: (datasetId?: number) =>
    unwrap(client.get<EvalRun[]>('/v1/eval/runs', { params: datasetId ? { datasetId } : {} })),
  getEvalRun: (id: number) => unwrap(client.get<EvalRunDetail>(`/v1/eval/runs/${id}`)),
  startEvalRun: (id: number) => unwrap(client.post<StageEnqueueResult>(`/v1/eval/runs/${id}/start`)),
  listEvalItems: (id: number, limit = 50, offset = 0) =>
    unwrap(client.get<EvalItem[]>(`/v1/eval/runs/${id}/items`, { params: { limit, offset } })),

  // ---- L10 评估汇总统计与结论 ----
  evalReport: (id: number) => unwrap(client.get<EvalReport>(`/v1/eval/runs/${id}/report`)),
  evalScores: (id: number, judgeProviderId?: number, dimensionKey?: string) =>
    unwrap(client.get<EvalItemScore[]>(`/v1/eval/runs/${id}/scores`, {
      params: { judgeProviderId, dimensionKey },
    })),

  // ---- L11 清洗关键词库与规则 ----
  listCleaningKeywords: (params: { category?: string; active?: boolean } = {}) =>
    unwrap(client.get<CleaningKeyword[]>('/v1/cleaning/keywords', { params })),
  saveCleaningKeyword: (payload: Partial<CleaningKeyword>) =>
    unwrap(client.request<CleaningKeyword>({ url: '/v1/cleaning/keywords', method: payload.id ? 'PUT' : 'POST', data: payload })),
  deleteCleaningKeyword: (id: number) => unwrap(client.delete<{ deleted: boolean }>(`/v1/cleaning/keywords/${id}`)),
  importCleaningKeywords: (patterns: string[], category = 'refusal', severity = 'block') =>
    unwrap(client.post<{ inserted: number; skipped: number }>('/v1/cleaning/keywords/import', { patterns, category, severity })),
  listCleaningRules: () => unwrap(client.get<CleaningRule[]>('/v1/cleaning/rules')),
  saveCleaningRule: (payload: Partial<CleaningRule>) =>
    unwrap(client.request<CleaningRule>({ url: '/v1/cleaning/rules', method: payload.id ? 'PUT' : 'POST', data: payload })),

  // ---- L12 分步清洗与报告 ----
  runCleaning: (id: number, stages: string[] = ['question', 'reasoning', 'answer'], ruleIds: number[] = []) =>
    unwrap(client.post<StageEnqueueResult>(`/v1/datasets/${id}/cleaning/run`, { stages, ruleIds })),
  listCleaningRuns: (id: number) => unwrap(client.get<CleaningRun[]>(`/v1/datasets/${id}/cleaning/runs`)),
  cleaningReport: (runId: number) => unwrap(client.get<CleaningReport>(`/v1/cleaning/runs/${runId}/report`)),
  listCleaningFindings: (runId: number, stage?: string, limit = 100) =>
    unwrap(client.get<CleaningFinding[]>(`/v1/cleaning/runs/${runId}/findings`, { params: { stage, limit } })),
}

client.interceptors.response.use(
  (response) => response,
  (error) => {
    const statusCode = error?.response?.status
    let fallbackMessage = '请求失败'
    if (statusCode === 401) {
      fallbackMessage = '登录状态已失效，请重新登录。'
    } else if (statusCode === 403) {
      fallbackMessage = '你没有执行该操作的权限，请联系管理员。'
    }
    const rawMessage: string = String(error?.response?.data?.error ?? error?.message ?? fallbackMessage)
    // 统一在这里把后端文案转成面向中文用户的可执行提示（issue #103 / #107）。
    //
    // 为什么放在拦截器而不是各调用点：后端会对同一类前置条件返回不同句式
    // （`cannot enqueue directions/...`、、`email and password are required` 等，共 10+ 处），
    // 在各调用点分别翻译必然漂移（有的改了、有的漏了），这正是 issue #103 的成因。
    // 拦截器是**唯一入口**，新增后端文案时只需在这里补一条。
    const message = localizeApiMessage(rawMessage, statusCode, fallbackMessage)
    const nextError: ApiError = new Error(message)
    nextError.statusCode = statusCode
    // 保留原文供排查：界面上不带它，但 console / 埋点可拿到第一手信息。
    nextError.rawMessage = rawMessage
    return Promise.reject(nextError)
  },
)

/**
 * 后端英文错误 → 面向中文用户的可执行提示。
 *
 * 后端把内部前置条件直接用英文报回来（含接口路径与字段名，如
 * `cannot enqueue directions: dataset 134 has no domains, run domains/generate first`）。
 * 直接展示给用户有三个问题：看不懂、泄漏内部实现、且给的不是他能执行的指令。
 *
 * 匹配策略：**先按已知句式精确翻译**（可控、可审），
 * 再兜底「整条消息里一个汉字都没有」的情况给通用中文提示。
 * 兜底很重要：后端将来新增一句英文文案时，界面不会再直接露英文。
 *
 * 原文始终保留在 `ApiError.rawMessage`，不丢排查线索。
 */
/**
 * 后端英文错误 → 面向中文用户的可执行提示。
 *
 * 后端把内部前置条件直接用英文报回来（含接口路径与字段名，如
 * `cannot enqueue directions: dataset 134 has no domains, run domains/generate first`）。
 * 直接展示给用户有三个问题：看不懂、泄漏内部实现、且给的不是他能执行的指令。
 *
 * 实现是**有序规则表**而不是长 if 链：新增/调整文案时只改表格，不动控制流。
 * 顺序敏感 —— 更具体的规则必须排在更宽泛的前面（例如
 * `no directions (level=2 domains)` 要排在 `cannot enqueue questions` 之前，
 * 因为后者的报文里可能就包含前者）。
 *
 * 兜底规则放在最后：整条消息里**一个汉字都没有**时给通用中文提示。
 * 这样后端将来新增一句英文文案时，界面不会再直接露英文，
 * 同时不会覆盖后端已经写好的中文提示。
 *
 * 原文始终保留在 `ApiError.rawMessage`，不丢排查线索。
 */
interface ApiMessageRule {
  test: RegExp
  /** 固定文案，或按「已完成/总数」比例生成文案。 */
  message: string | ((ratio: RegExpMatchArray | null) => string)
}

/** 从形如 `(3/8)` 的报文里取「已完成/总数」比例。 */
const COMPLETION_RATIO = /\((\d+)\/(\d+)\)/

const API_MESSAGE_RULES: ApiMessageRule[] = [
  // 前置条件未就绪（后端用 409 表达「上游还没做完」）。
  // 顺序敏感：更具体的必须排在更宽泛的前面。
  { test: /no directions \(level=2 domains\)/i, message: '请先生成方向，再生成问题。' },
  { test: /cannot enqueue directions/i, message: '请先完成并确认主题结构，再生成方向。' },
  {
    test: /cannot enqueue rewards[\s\S]*incomplete/i,
    message: (ratio) =>
      ratio
        ? `答案尚未全部完成（已完成 ${ratio[1]}/${ratio[2]}），请等全部完成后再做质量评估。`
        : '答案尚未全部完成，请等全部完成后再做质量评估。',
  },
  {
    test: /cannot enqueue export[\s\S]*incomplete/i,
    message: (ratio) =>
      ratio
        ? `质量评估尚未全部完成（已完成 ${ratio[1]}/${ratio[2]}），请等全部完成后再导出。`
        : '质量评估尚未全部完成，请等全部完成后再导出。',
  },
  { test: /cannot enqueue questions/i, message: '请先生成并确认主题结构（并确保已有方向），再生成问题。' },
  { test: /cannot enqueue reasoning/i, message: '请先生成问题，再生成答案。' },
  { test: /cannot enqueue rewards/i, message: '请先生成答案，再做质量评估。' },
  { test: /cannot enqueue export/i, message: '请先完成质量评估，再导出交付文件。' },
  { test: /cannot enqueue grpo/i, message: '请先生成问题，再生成教师评判提示词。' },
  { test: /cannot enqueue sft/i, message: '请先生成问题，再生成 SFT 记录。' },
  // 登录相关（issue #107）
  { test: /email and password are required/i, message: '请输入邮箱与密码。' },
  { test: /invalid email or password/i, message: '邮箱或密码不正确，请重新输入。' },
]

/** 整条消息不含任何汉字 —— 说明还没有中文化。 */
function hasNoChinese(text: string): boolean {
  return !/[\u4e00-\u9fa5]/.test(text)
}

function localizeApiMessage(raw: string, statusCode: number | undefined, fallback: string): string {
  const text = raw.trim()

  for (const rule of API_MESSAGE_RULES) {
    if (!rule.test.test(text)) continue
    return typeof rule.message === 'function' ? rule.message(text.match(COMPLETION_RATIO)) : rule.message
  }

  if (hasNoChinese(text)) {
    // 401/403 已有更准确的语义，不要被通用文案覆盖。
    if (statusCode === 401) return '登录状态已失效，请重新登录。'
    if (statusCode === 403) return '你没有执行该操作的权限，请联系管理员。'
    return `${fallback}（如反复出现，请联系管理员并提供时间点）`
  }

  return text
}
