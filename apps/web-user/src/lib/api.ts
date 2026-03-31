import axios from 'axios'

export type ApiError = Error & { statusCode?: number }

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
  content: string
  canonicalHash: string
  status: string
  createdAt: string
  updatedAt: string
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
  pipelineProgress: (id: number) => unwrap(client.get<PipelineProgress>(`/v1/datasets/${id}/pipeline/progress`)),
  runtimeStatus: () => unwrap(client.get<RuntimeStatus>('/v1/platform/runtime')),
}

client.interceptors.response.use(
  (response) => response,
  (error) => {
    const statusCode = error?.response?.status
    const fallbackMessage =
      statusCode === 401
        ? '登录状态已失效，请重新登录。'
        : statusCode === 403
          ? '你没有执行该操作的权限，请联系管理员。'
          : '请求失败'
    const message = error?.response?.data?.error ?? error?.message ?? fallbackMessage
    const nextError: ApiError = new Error(message)
    nextError.statusCode = statusCode
    return Promise.reject(nextError)
  },
)
