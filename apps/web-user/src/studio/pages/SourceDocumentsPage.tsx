import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { Link } from 'react-router-dom'
import { Button, Card, Empty, Input, Select, Spin, Tag, Typography } from '@douyinfe/semi-ui'
import { FileText, RefreshCw, Upload } from 'lucide-react'
import { studioApi } from '../../lib/api/studio'
import { DEFAULT_SOURCE_CHUNKING, sourceApi, sourceImportStatus, sourceKindLabel } from '../../lib/api/source'
import type { SourceChunk, SourceChunking, SourceDocumentEntry, SourceImportLedger, SourcePage } from '../../lib/api/source'
import { DocumentHistory, DocumentSaveBar, useVersionedDocument } from '../DocumentEditors'
import { useProjectScope } from '../ProjectLayout'
import './SourcePages.css'

const { Title, Text } = Typography
const PAGE_SIZE = 10
const EMPTY_IMPORTS: SourcePage<SourceImportLedger> = { items: [], total: 0, limit: PAGE_SIZE, offset: 0 }

export function SourceChunkingFields({ value, disabled, onChange }: { value: SourceChunking; disabled: boolean; onChange: (value: SourceChunking) => void }) {
  return <div className="source-fields">
    <label><span>切分算法</span><Select aria-label="切分算法" value={value.algorithm} disabled={disabled} optionList={[
      { value: 'recursive', label: '章节感知递归切分' }, { value: 'text', label: '按分隔符切分' },
    ]} onChange={(next) => onChange({ ...value, algorithm: next as SourceChunking['algorithm'] })} /></label>
    <label><span>最小块长度（字符）</span><Input aria-label="最小块长度" type="number" min={1} value={String(value.minLength)} disabled={disabled} onChange={(next) => onChange({ ...value, minLength: Number(next) })} /></label>
    <label><span>最大块长度（字符）</span><Input aria-label="最大块长度" type="number" min={1} max={100000} value={String(value.maxLength)} disabled={disabled} onChange={(next) => onChange({ ...value, maxLength: Number(next) })} /></label>
    <label><span>分隔符（用 \\n 表示换行）</span><Input aria-label="切分分隔符" value={value.separator.split('\n').join('\\n')} disabled={disabled} onChange={(next) => onChange({ ...value, separator: next.split('\\n').join('\n') })} /></label>
    <label className="source-check"><input type="checkbox" checked={value.keepHeadingPath} disabled={disabled} onChange={(event) => onChange({ ...value, keepHeadingPath: event.target.checked })} />保留标题层级</label>
  </div>
}

export function SourceDocumentsPage() {
  const scope = useProjectScope()
  const state = useVersionedDocument(scope.projectId, 'source-versions')
  const fileInput = useRef<HTMLInputElement>(null)
  const [canRun, setCanRun] = useState(false)
  const [uploading, setUploading] = useState(false)
  const [uploadError, setUploadError] = useState<string | null>(null)
  const [notice, setNotice] = useState<string | null>(null)
  const [selectedDocument, setSelectedDocument] = useState('')
  const [query, setQuery] = useState('')
  const [chunkOffset, setChunkOffset] = useState(0)
  const [chunks, setChunks] = useState<SourcePage<SourceChunk>>({ items: [], total: 0, offset: 0, limit: PAGE_SIZE })
  const [chunksLoading, setChunksLoading] = useState(false)
  const [chunkError, setChunkError] = useState<string | null>(null)
  const [chunkRetry, setChunkRetry] = useState(0)
  const [imports, setImports] = useState(EMPTY_IMPORTS)
  const [importOffset, setImportOffset] = useState(0)
  const [importError, setImportError] = useState<string | null>(null)
  const seenStatuses = useRef(new Map<number, string>())
  const payload = state.payload ?? { schemaVersion: 'source.v1', documents: [], chunking: DEFAULT_SOURCE_CHUNKING }
  const documents = useMemo(() => Array.isArray(payload.documents) ? payload.documents as SourceDocumentEntry[] : [], [payload.documents])
  const chunking: SourceChunking = { ...DEFAULT_SOURCE_CHUNKING, ...(payload.chunking as Partial<SourceChunking> | undefined) }
  const selected = documents.find((entry) => entry.stableId === selectedDocument) ?? documents[0]
  const readOnly = state.isReadOnly || !state.canEdit

  useEffect(() => {
    let cancelled = false
    void studioApi.overviewEnvelope(scope.projectId).then((response) => {
      if (!cancelled) setCanRun(response.capabilities.canRun === true)
    }).catch(() => { if (!cancelled) setCanRun(false) })
    return () => { cancelled = true }
  }, [scope.projectId])

  const loadImports = useCallback(async () => {
    try {
      const page = await sourceApi.listImports(scope.projectId, importOffset, PAGE_SIZE)
      let completed = false
      for (const entry of page.items) {
        const previous = seenStatuses.current.get(entry.id)
        if (previous && previous !== entry.status && entry.status === 'completed') completed = true
        seenStatuses.current.set(entry.id, entry.status)
      }
      setImports(page)
      setImportError(null)
      if (completed && !state.dirty) await state.reload()
      else if (completed) setNotice('后台解析已完成。当前切分草稿已保留，请先保存或重新加载后查看最新来源。')
    } catch (error) { setImportError(error instanceof Error ? error.message : '导入记录读取失败') }
  }, [importOffset, scope.projectId, state.dirty, state.reload])

  // reload 的包装函数由 hook 每次渲染生成。用 ref 保持轮询的生命周期稳定。
  const loadImportsRef = useRef(loadImports)
  loadImportsRef.current = loadImports
  useEffect(() => { void loadImportsRef.current() }, [importOffset, scope.projectId])
  useEffect(() => { setChunkOffset(0); setQuery('') }, [state.current?.id])
  const hasRunningImports = imports.items.some((entry) => entry.status === 'pending' || entry.status === 'running')
  useEffect(() => {
    if (!hasRunningImports) return
    const timer = window.setInterval(() => void loadImportsRef.current(), 2500)
    return () => window.clearInterval(timer)
  }, [hasRunningImports])

  useEffect(() => {
    let cancelled = false
    if (!selected || selected.chunkCount === 0) {
      setChunks({ items: [], total: 0, offset: 0, limit: PAGE_SIZE })
      setChunkError(null)
      setChunksLoading(false)
      return
    }
    setChunksLoading(true)
    setChunkError(null)
    void sourceApi.listChunks(scope.projectId, { sourceDocumentStableId: selected.stableId, sourceVersionId: state.current?.id, q: query, offset: chunkOffset, limit: PAGE_SIZE }).then((page) => {
      if (cancelled) return
      setChunks(page)
    }).catch((error) => {
      if (!cancelled) setChunkError(error instanceof Error ? error.message : '素材块读取失败')
    }).finally(() => { if (!cancelled) setChunksLoading(false) })
    return () => { cancelled = true }
  }, [chunkOffset, chunkRetry, query, scope.projectId, state.current?.id, selected?.stableId, selected?.chunkCount])

  const upload = async (file: File) => {
    setUploadError(null)
    setNotice(null)
    if (!/\.(md|markdown|txt)$/i.test(file.name)) { setUploadError('目前支持 Markdown/TXT；PDF/DOCX 请先转换后上传。'); return }
    if (file.size > 200 * 1024 * 1024 || file.size === 0) { setUploadError('请选择非空文件，单个文件不能超过 200 MB。'); return }
    if (chunking.minLength < 1 || chunking.maxLength < chunking.minLength || chunking.maxLength > 100000) { setUploadError('切分长度需要满足：1 ≤ 最小长度 ≤ 最大长度 ≤ 100000。'); return }
    setUploading(true)
    try {
      const result = await sourceApi.upload(scope.projectId, file, state.headRevision, chunking, state.changeReason.trim() || `上传素材：${file.name}`)
      seenStatuses.current.set(result.importId, result.status)
      setNotice(`${file.name}：${result.replay ? '已导入过，已回放原结果' : '已进入后台解析'}。${result.warnings.join('；')}`)
      setImportOffset(0)
      await state.reload()
      await loadImportsRef.current()
    } catch (error) {
      const apiError = error as { statusCode?: number; message?: string }
      setUploadError(apiError.statusCode === 409 ? '素材版本已被其他人更新，请重新加载后再次上传；你的切分参数仍在当前草稿中。' : apiError.message ?? '上传失败，请重试。')
    } finally { setUploading(false) }
  }

  if (state.loading) return <div className="source-state" data-source-state="loading"><Spin tip="正在加载素材来源" /></div>
  if (state.error) return <Card className="console-card" data-source-state="error"><Text type="danger">{state.error}</Text><Button onClick={() => void state.reload()}>重新加载</Button></Card>

  return <div className="console-page source-page" data-studio-page="sources" data-source-state={documents.length ? 'default' : 'empty'}>
    <div className="console-page__header">
      <div><Title heading={4}>素材来源</Title></div>
      <div className="console-page__actions"><Link to={scope.href('project.coverage')}>目标结构</Link><Link to={scope.href('project.sourceImport')}>导入外部数据集</Link><Button icon={<Upload size={14} />} theme="solid" type="primary" loading={uploading} disabled={!canRun || readOnly || state.dirty} onClick={() => fileInput.current?.click()}>上传素材</Button></div>
      <input ref={fileInput} className="source-file-input" type="file" accept=".md,.markdown,.txt,text/plain,text/markdown" aria-label="选择 Markdown 或 TXT 素材" onChange={(event) => { const file = event.target.files?.[0]; if (file) void upload(file); event.target.value = '' }} />
    </div>
    {notice ? <Card className="console-card mb-3"><div role="status">{notice}</div></Card> : null}
    {uploadError ? <div className="source-error" role="alert">{uploadError}</div> : null}
    {state.dirty ? <Text type="warning" className="block mb-3">请先保存切分策略，再上传素材，避免尚未保存的参数与来源记录不一致。</Text> : null}
    <div className="source-workspace product-source-workspace">
      <Card className="console-card source-list" bodyStyle={{ padding: 16 }}>
        <Text strong>来源清单 · {documents.length}</Text>
        {documents.length === 0 ? <Empty description="暂无素材"><Button disabled={!canRun || readOnly} onClick={() => fileInput.current?.click()}>上传第一份素材</Button></Empty> : <ul>
          {documents.map((entry) => <li key={entry.stableId}><button type="button" className={selected?.stableId === entry.stableId ? 'source-document is-selected' : 'source-document'} onClick={() => { setSelectedDocument(entry.stableId); setChunkOffset(0); setQuery('') }}>
            <FileText size={16} /><span><strong title={entry.fileName}>{entry.fileName}</strong><small>{entry.parsedAt ? `${entry.chunkCount} 个素材块` : '等待解析'} · {entry.kind === 'markdown' ? 'Markdown' : '文本'}</small></span>
          </button></li>)}
        </ul>}
        <Text type="tertiary" size="small">支持 Markdown/TXT，单个文件最多 200 MB。</Text>
      </Card>
      <Card className="console-card source-preview" bodyStyle={{ padding: 16 }}>
        <div className="source-panel-head"><Text strong>{selected ? `素材预览 · ${selected.fileName}` : '素材预览'}</Text><Tag size="small">{selected?.chunkCount ?? 0} 块</Tag></div>
        <Input aria-label="搜索素材内容" placeholder="搜索素材内容" value={query} disabled={!selected} onChange={(next) => { setQuery(next); setChunkOffset(0) }} />
        {chunksLoading ? <div className="source-state"><Spin tip="正在读取素材块" /></div> : chunkError ? <div className="source-error" role="alert">{chunkError}<Button onClick={() => setChunkRetry((value) => value + 1)}>重新读取素材块</Button></div> : chunks.items.length === 0 ? <Empty description={selected && !selected.parsedAt ? '素材正在后台解析，完成后会自动更新。' : query ? '没有匹配的素材块，请调整关键词。' : '选择一个已解析的素材查看内容。'} /> : <div className="source-chunks">
          {chunks.items.map((chunk) => <article key={chunk.id} data-source-chunk-id={chunk.id}><Text strong>{chunk.headingPath || `素材块 ${chunk.ordinal}`}</Text><small>块编号 {chunk.id} · {[...chunk.content].length} 字符</small><pre>{chunk.content}</pre></article>)}
        </div>}
        <div className="source-pagination"><Button size="small" disabled={chunkOffset === 0 || chunksLoading} onClick={() => setChunkOffset(Math.max(0, chunkOffset - PAGE_SIZE))}>上一页</Button><Text size="small">{chunks.total ? `${chunkOffset + 1}–${Math.min(chunkOffset + PAGE_SIZE, chunks.total)} / ${chunks.total}` : '0 个素材块'}</Text><Button size="small" disabled={chunkOffset + PAGE_SIZE >= chunks.total || chunksLoading} onClick={() => setChunkOffset(chunkOffset + PAGE_SIZE)}>下一页</Button></div>
      </Card>
    </div>
    <details className="product-disclosure" open={state.dirty ? true : undefined}><summary>切分策略{state.dirty ? ' · 有未保存的修改' : ''}</summary>
      <Card className="console-card source-settings" bodyStyle={{ padding: 16 }}><SourceChunkingFields value={chunking} disabled={readOnly} onChange={(next) => state.setPayload({ ...payload, chunking: next })} /><Text type="tertiary" size="small">仅影响后续上传。</Text></Card>
      <DocumentSaveBar state={state} label="素材来源" />
    </details>
    <div className="product-stage-footer"><span>{documents.length} 份素材</span><Link className="product-primary-link" to={scope.href('project.coverage')}>关联到目标结构 →</Link></div>
    <details className="source-history"><summary>素材来源版本历史（只读）</summary><DocumentHistory state={state} /></details>
    <details className="product-disclosure"><summary>导入记录 · {imports.total} 次{importError ? ' · 读取失败' : ''}</summary>
    <Card className="console-card mt-3" bodyStyle={{ padding: 16 }}>
      <div className="source-panel-head"><Text strong>导入记录</Text><Button size="small" icon={<RefreshCw size={14} />} onClick={() => void loadImportsRef.current()}>刷新记录</Button></div>
      {importError ? <div className="source-error" role="alert">{importError}</div> : imports.items.length === 0 ? <Empty description="还没有导入记录。" /> : <ul className="source-ledgers">{imports.items.map((entry) => <li key={entry.id}>
        <div><strong>{sourceKindLabel(entry.sourceKind)} · 导入 {entry.id}</strong><Tag color={entry.status === 'failed' ? 'red' : entry.status === 'completed' ? 'green' : 'blue'}>{sourceImportStatus(entry.status)}</Tag></div>
        <Text size="small">源对象 {entry.counts.sourceItems} · 新增 {entry.counts.importedVersions} · 已存在 {entry.counts.skippedExisting} · 无内容 {entry.counts.skippedNoContent} · 失败 {entry.counts.failedItems}</Text>
        {entry.errorMessage ? <Text type="danger">{entry.errorMessage}</Text> : null}
        {entry.failures?.length ? <details><summary>查看失败明细（{entry.failures.length} 条）</summary><ul>{entry.failures.map((failure, index) => <li key={`${failure.sourceId}-${index}`}>第 {failure.sourceId} 条：{failure.reason}</li>)}</ul></details> : null}
      </li>)}</ul>}
      <div className="source-pagination"><Button size="small" disabled={importOffset === 0} onClick={() => setImportOffset(Math.max(0, importOffset - PAGE_SIZE))}>上一页</Button><Text size="small">共 {imports.total} 次导入</Text><Button size="small" disabled={importOffset + PAGE_SIZE >= imports.total} onClick={() => setImportOffset(importOffset + PAGE_SIZE)}>下一页</Button></div>
    </Card>
    </details>
  </div>
}
