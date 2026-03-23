const API_BASE = import.meta.env.VITE_API_BASE_URL ?? 'http://localhost:38080'

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
  estimatePlan: (payload: { rootKeyword: string; targetSize: number; strategyId: number }) => request<Estimate>('/api/v1/datasets/plans/estimate', { method: 'POST', body: JSON.stringify(payload) }),
  createDataset: (payload: Record<string, unknown>) => request<Dataset>('/api/v1/datasets', { method: 'POST', body: JSON.stringify(payload) }),
  listDatasets: () => request<Dataset[]>('/api/v1/datasets'),
  getDataset: (id: number) => request<DatasetGraph>(`/api/v1/datasets/${id}`),
  generateDomains: (id: number) => request<DatasetGraph>(`/api/v1/datasets/${id}/domains/generate`, { method: 'POST' }),
  updateGraph: (id: number, domains: Domain[]) => request<{ updated: number }>(`/api/v1/datasets/${id}/domains/graph`, { method: 'POST', body: JSON.stringify({ domains }) }),
  confirmDomains: (id: number) => request<{ status: string }>(`/api/v1/datasets/${id}/domains/confirm`, { method: 'POST' }),
  listStrategies: () => request<Strategy[]>('/api/v1/admin/generation-strategies'),
  listProviders: () => request<Provider[]>('/api/v1/admin/providers'),
  listStorageProfiles: () => request<StorageProfile[]>('/api/v1/admin/storage-profiles'),
}
