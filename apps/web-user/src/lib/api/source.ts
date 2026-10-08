import { client } from '../api'
import { projectPath, type ProjectResourceId } from './studio'

export type SourceChunking = {
  algorithm: 'recursive' | 'text'
  separator: string
  maxLength: number
  minLength: number
  keepHeadingPath: boolean
}

export const DEFAULT_SOURCE_CHUNKING: SourceChunking = {
  algorithm: 'recursive', separator: '\n\n', maxLength: 2000, minLength: 200, keepHeadingPath: true,
}

export type SourceDocumentEntry = {
  stableId: string
  fileName: string
  kind: 'markdown' | 'txt'
  contentHash: string
  chunkCount: number
  chunkIds?: number[]
  parsedAt?: string
}

export type SourceChunk = {
  id: number
  projectId: number
  sourceDocumentStableId: string
  headingPath: string
  ordinal: number
  content: string
  contentHash: string
  createdAt: string
}

export type SourceImportCounts = {
  sourceItems: number
  importedVersions: number
  skippedExisting: number
  skippedNoContent: number
  failedItems: number
}

export type SourceImportResult = {
  importId: number
  sourceKind: string
  sourceKey: string
  status: string
  replay: boolean
  counts: SourceImportCounts
  jobId?: number
  warnings: string[]
}

export type SourceImportLedger = {
  id: number
  sourceKind: string
  sourceKey: string
  status: string
  counts: SourceImportCounts
  cursor: number
  contentHash: string
  batchId?: number
  failures: Array<{ sourceId: number; reason: string }> | null
  errorMessage: string
  createdAt: string
  updatedAt: string
  jobId?: number
}

export type SourcePage<T> = { items: T[]; total: number; limit: number; offset: number }
export type ProductImportRequest = {
  format: 'alpaca' | 'sharegpt' | 'jsonl'
  sourceKey: string
  content: string
  targetKind: 'sft'
  changeReason: string
}
export type ProductImportPreview = {
  sourceItems: number
  validItems: number
  duplicateItems: number
  failedItems: number
  failures: Array<{ sourceId: number; reason: string }>
}

export const SOURCE_IMPORT_STATUS: Record<string, string> = {
  pending: '等待处理', running: '正在处理', paused: '已暂停', completed: '已完成', failed: '失败',
}

export function sourceImportStatus(status: string): string {
  return SOURCE_IMPORT_STATUS[status] ?? '状态待确认'
}

export function sourceKindLabel(kind: string): string {
  return kind === 'source_document' ? '素材文档' : kind === 'source_product' ? '外部数据集' : '历史资产'
}

export const sourceApi = {
  upload: async (projectId: ProjectResourceId, file: File, expectedRevision: number, chunking: SourceChunking, changeReason: string) => {
    const form = new FormData()
    form.append('file', file)
    form.append('expectedRevision', String(expectedRevision))
    form.append('changeReason', changeReason)
    for (const [key, value] of Object.entries(chunking)) form.append(`chunking.${key}`, String(value))
    const response = await client.post<SourceImportResult>(`${projectPath(projectId)}/source-imports`, form, {
      headers: { 'Content-Type': undefined },
    })
    return response.data
  },
  listImports: (projectId: ProjectResourceId, offset = 0, limit = 20) => client
    .get<SourcePage<SourceImportLedger>>(`${projectPath(projectId)}/source-imports`, { params: { limit, offset } })
    .then((response) => response.data),
  getImport: (projectId: ProjectResourceId, importId: number) => client
    .get<SourceImportLedger>(`${projectPath(projectId)}/source-imports/${importId}`)
    .then((response) => response.data),
  listChunks: (projectId: ProjectResourceId, params: { sourceDocumentStableId?: string; sourceVersionId?: number; q?: string; offset?: number; limit?: number }) => client
    .get<SourcePage<SourceChunk>>(`${projectPath(projectId)}/source-chunks`, { params })
    .then((response) => response.data),
  getChunk: (projectId: ProjectResourceId, chunkId: number) => client
    .get<SourceChunk>(`${projectPath(projectId)}/source-chunks/${chunkId}`)
    .then((response) => response.data),
  previewProducts: (projectId: ProjectResourceId, input: ProductImportRequest) => client
    .post<ProductImportPreview>(`${projectPath(projectId)}/source-import-products/preview`, input)
    .then((response) => response.data),
  importProducts: (projectId: ProjectResourceId, input: ProductImportRequest) => client
    .post<SourceImportResult>(`${projectPath(projectId)}/source-import-products`, input)
    .then((response) => response.data),
}
