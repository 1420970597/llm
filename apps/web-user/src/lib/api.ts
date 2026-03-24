const API_BASE = import.meta.env.VITE_API_BASE_URL ?? '/api'

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
  planningMode: string
}

export type Provider = {
  id: number
  name: string
  model: string
  providerType: string
  apiKeyMasked?: string
}

export type StorageProfile = {
  id: number
  name: string
  bucket: string
  provider: string
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

export type RuntimeStatus = {
  datasetCount: number
  questionCount: number
  reasoningCount: number
  rewardCount: number
  artifactCount: number
  queueDepth: number
}

async function request<T>(path: string, init?: RequestInit): Promise<T> {
  const response = await fetch(`${API_BASE}${path}`, {
    headers: {
      'Content-Type': 'application/json',
      ...(init?.headers ?? {}),
    },
    ...init,
  })
  if (!response.ok) {
    const payload = (await response.json().catch(() => ({ error: 'Request failed' }))) as { error?: string }
    throw new Error(payload.error ?? `Request failed: ${response.status}`)
  }
  return response.json() as Promise<T>
}

export const userApi = {
  estimatePlan: (payload: { rootKeyword: string; targetSize: number; strategyId: number }) => request<Estimate>('/v1/datasets/plans/estimate', { method: 'POST', body: JSON.stringify(payload) }),
  createDataset: (payload: Record<string, unknown>) => request<Dataset>('/v1/datasets', { method: 'POST', body: JSON.stringify(payload) }),
  listDatasets: () => request<Dataset[]>('/v1/datasets'),
  getDataset: (id: number) => request<DatasetGraph>(`/v1/datasets/${id}`),
  generateDomains: (id: number) => request<DatasetGraph>(`/v1/datasets/${id}/domains/generate`, { method: 'POST' }),
  updateGraph: (id: number, domains: Domain[]) => request<{ updated: number }>(`/v1/datasets/${id}/domains/graph`, { method: 'POST', body: JSON.stringify({ domains }) }),
  confirmDomains: (id: number) => request<{ status: string }>(`/v1/datasets/${id}/domains/confirm`, { method: 'POST' }),
  generateQuestions: (id: number) => request<{ queued: boolean; datasetId: number }>(`/v1/datasets/${id}/questions/generate`, { method: 'POST' }),
  listQuestions: (id: number) => request<Question[]>(`/v1/datasets/${id}/questions`),
  generateReasoning: (id: number) => request<{ queued: boolean; datasetId: number }>(`/v1/datasets/${id}/reasoning/generate`, { method: 'POST' }),
  listReasoning: (id: number) => request<ReasoningRecord[]>(`/v1/datasets/${id}/reasoning`),
  generateRewards: (id: number) => request<{ queued: boolean; datasetId: number }>(`/v1/datasets/${id}/rewards/generate`, { method: 'POST' }),
  listRewards: (id: number) => request<RewardRecord[]>(`/v1/datasets/${id}/rewards`),
  generateExport: (id: number) => request<{ queued: boolean; datasetId: number }>(`/v1/datasets/${id}/export`, { method: 'POST' }),
  listArtifacts: (id: number) => request<Artifact[]>(`/v1/datasets/${id}/export`),
  runtimeStatus: () => request<RuntimeStatus>('/v1/platform/runtime'),
  listStrategies: () => request<Strategy[]>('/v1/admin/generation-strategies'),
  listProviders: () => request<Provider[]>('/v1/admin/providers'),
  listStorageProfiles: () => request<StorageProfile[]>('/v1/admin/storage-profiles'),
}
