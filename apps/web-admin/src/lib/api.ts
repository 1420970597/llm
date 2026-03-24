const API_BASE = import.meta.env.VITE_API_BASE_URL ?? '/api'

export type ProviderRecord = {
  id?: number
  name: string
  baseUrl: string
  model: string
  providerType: string
  maxConcurrency: number
  timeoutSeconds: number
  isActive: boolean
  apiKey?: string
  apiKeyMasked?: string
}

export type StorageProfileRecord = {
  id?: number
  name: string
  provider: string
  endpoint: string
  region: string
  bucket: string
  accessKeyId: string
  secretAccessKey?: string
  secretKeyMasked?: string
  usePathStyle: boolean
  isDefault: boolean
}

export type StrategyRecord = {
  id?: number
  name: string
  description: string
  domainCount: number
  questionsPerDomain: number
  answerVariants: number
  rewardVariants: number
  planningMode: string
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

export const adminApi = {
  dashboard: () => request<DashboardRecord>('/v1/admin/dashboard'),
  listProviders: () => request<ProviderRecord[]>('/v1/admin/providers'),
  saveProvider: (payload: ProviderRecord) => request<ProviderRecord>('/v1/admin/providers', { method: payload.id ? 'PUT' : 'POST', body: JSON.stringify(payload) }),
  listStorageProfiles: () => request<StorageProfileRecord[]>('/v1/admin/storage-profiles'),
  saveStorageProfile: (payload: StorageProfileRecord) => request<StorageProfileRecord>('/v1/admin/storage-profiles', { method: payload.id ? 'PUT' : 'POST', body: JSON.stringify(payload) }),
  listStrategies: () => request<StrategyRecord[]>('/v1/admin/generation-strategies'),
  saveStrategy: (payload: StrategyRecord) => request<StrategyRecord>('/v1/admin/generation-strategies', { method: payload.id ? 'PUT' : 'POST', body: JSON.stringify(payload) }),
  listPrompts: () => request<PromptRecord[]>('/v1/admin/prompts'),
  savePrompt: (payload: PromptRecord) => request<PromptRecord>('/v1/admin/prompts', { method: payload.id ? 'PUT' : 'POST', body: JSON.stringify(payload) }),
  listAuditLogs: () => request<AuditRecord[]>('/v1/admin/audit-logs'),
}
