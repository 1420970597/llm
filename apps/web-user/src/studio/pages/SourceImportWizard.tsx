import { useEffect, useRef, useState } from 'react'
import { Link, useNavigate } from 'react-router-dom'
import { Button, Card, Input, Select, Spin, TextArea, Typography } from '@douyinfe/semi-ui'
import { studioApi } from '../../lib/api/studio'
import { sourceApi, sourceImportStatus } from '../../lib/api/source'
import type { ProductImportPreview, ProductImportRequest, SourceImportLedger, SourceImportResult } from '../../lib/api/source'
import { useProjectScope } from '../ProjectLayout'
import './SourcePages.css'

const { Title, Text } = Typography
const PRODUCT_LIMIT = 20 * 1024 * 1024
const FORMAT_DETAILS: Record<ProductImportRequest['format'], { label: string; mapping: string; example: string }> = {
  alpaca: { label: 'Alpaca', mapping: 'instruction + input → 问题；output → 答案；可选 reasoning → 推理过程。', example: '[{"instruction":"说明冷链断链的处理原则","input":"运输温度超过范围","output":"先隔离受影响批次，再按稳定性证据评估。","reasoning":"先确认影响范围，再按证据判断。"}]' },
  sharegpt: { label: 'ShareGPT', mapping: 'human/user 消息 → 问题；gpt/assistant 消息 → 答案；对话必须能形成问答配对。', example: '[{"conversations":[{"from":"human","value":"冷链发生断链时应如何处理？"},{"from":"gpt","value":"隔离批次并依据稳定性证据评估。"}]}]' },
  jsonl: { label: 'JSONL', mapping: '每行一条记录：question → 问题；answer → 答案；reasoning → 推理过程。', example: '{"question":"冷链断链如何处理？","reasoning":"先识别影响范围。","answer":"隔离并评估受影响批次。"}' },
}

export function SourceImportWizard() {
  const scope = useProjectScope()
  const navigate = useNavigate()
  const [format, setFormat] = useState<ProductImportRequest['format']>('alpaca')
  const [sourceKey, setSourceKey] = useState('')
  const [content, setContent] = useState('')
  const [changeReason, setChangeReason] = useState('')
  const [preview, setPreview] = useState<ProductImportPreview | null>(null)
  const [previewing, setPreviewing] = useState(false)
  const [importing, setImporting] = useState(false)
  const [readingFile, setReadingFile] = useState(false)
  const [canRun, setCanRun] = useState(false)
  const [capabilityLoading, setCapabilityLoading] = useState(true)
  const [capabilityError, setCapabilityError] = useState<string | null>(null)
  const [capabilityRetry, setCapabilityRetry] = useState(0)
  const [error, setError] = useState<string | null>(null)
  const [result, setResult] = useState<SourceImportResult | null>(null)
  const [ledger, setLedger] = useState<SourceImportLedger | null>(null)
  const previewedInput = useRef('')
  const operation = useRef(0)
  useEffect(() => () => { operation.current++ }, [])

  useEffect(() => {
    let cancelled = false
    setCanRun(false)
    setCapabilityLoading(true)
    setCapabilityError(null)
    void studioApi.overviewEnvelope(scope.projectId).then((response) => {
      if (!cancelled) setCanRun(response.capabilities.canRun === true)
    }).catch((loadError) => {
      if (!cancelled) setCapabilityError(loadError instanceof Error ? loadError.message : '权限读取失败')
    }).finally(() => { if (!cancelled) setCapabilityLoading(false) })
    return () => { cancelled = true }
  }, [scope.projectId, capabilityRetry])

  useEffect(() => {
    if (!result) return
    let cancelled = false
    let timer: number | undefined
    const poll = async () => {
      try {
        const current = await sourceApi.getImport(scope.projectId, result.importId)
        if (cancelled) return
        setLedger(current)
        if (current.status === 'pending' || current.status === 'running') timer = window.setTimeout(() => void poll(), 2000)
      } catch (pollError) { if (!cancelled) setError(pollError instanceof Error ? pollError.message : '导入结果读取失败，请到素材来源页刷新记录。') }
    }
    void poll()
    return () => { cancelled = true; if (timer) window.clearTimeout(timer) }
  }, [result, scope.projectId])

  const input: ProductImportRequest = { format, sourceKey: sourceKey.trim(), content, targetKind: 'sft', changeReason: changeReason.trim() || '导入外部数据集产物' }
  const inputRef = useRef(input)
  inputRef.current = input
  const identity = JSON.stringify(input)
  const validated = preview !== null && previewedInput.current === identity
  const busy = previewing || importing || readingFile
  const edit = (setter: (value: string) => void, value: string) => { operation.current++; setPreview(null); setError(null); setter(value) }

  const validate = (request: ProductImportRequest) => {
    if (!request.sourceKey) return '请填写导入名称。'
    if (!request.content.trim()) return '请选择文件，或粘贴数据。'
    if (new TextEncoder().encode(request.content).byteLength > PRODUCT_LIMIT) return '文件超过 20 MB，请拆分后导入。'
    return null
  }

  const check = async (request: ProductImportRequest = input) => {
    setError(null)
    const invalid = validate(request)
    if (invalid) { setError(invalid); return }
    const serial = ++operation.current
    setPreviewing(true)
    try {
      const response = await sourceApi.previewProducts(scope.projectId, request)
      // A select can emit a second change while its popup closes. If the
      // submitted request is still identical to the current form, keep the
      // response; only discard responses for an actually changed request.
      if (serial !== operation.current && JSON.stringify(request) !== JSON.stringify(inputRef.current)) return
      previewedInput.current = JSON.stringify(request)
      setPreview(response)
    } catch (previewError) {
      if (serial === operation.current || JSON.stringify(request) === JSON.stringify(inputRef.current)) setError(previewError instanceof Error ? previewError.message : '格式校验失败')
    }
    finally { setPreviewing(false) }
  }

  const importData = async () => {
    if (!validated || preview.validItems === 0 || !canRun) return
    setImporting(true)
    setError(null)
    try { setResult(await sourceApi.importProducts(scope.projectId, input)) }
    catch (importError) { setError(importError instanceof Error ? importError.message : '导入失败，请重试。') }
    finally { setImporting(false) }
  }

  const readFile = async (file: File) => {
    setError(null)
    const serial = ++operation.current
    setPreview(null)
    previewedInput.current = ''
    if (file.size > PRODUCT_LIMIT || file.size === 0) { setError('请选择非空的 JSON/JSONL 文件，最多 20 MB。'); return }
    setReadingFile(true)
    try {
      const text = await file.text()
      if (serial !== operation.current) return
      const fileFormat = detectProductFormat(file.name, text)
      const name = sourceKey.trim() || file.name
      setContent(text)
      setSourceKey(name)
      setFormat(fileFormat)
      setPreview(null)
      if (canRun) await check({ ...input, sourceKey: name, content: text, format: fileFormat })
    } catch { if (serial === operation.current) setError('无法读取文件，请重新选择。') }
    finally { setReadingFile(false) }
  }

  if (result) {
    const status = ledger?.status ?? result.status
    const counts = ledger?.counts ?? result.counts
    return <div className="console-page source-import-page" data-studio-page="source-import" data-source-import-state="result">
      <div className="console-page__header"><div><Title heading={4}>外部数据集导入</Title><Text type="tertiary">导入 {result.importId} · {sourceImportStatus(status)}</Text></div><Link to={scope.href('project.sources')}>查看全部导入记录</Link></div>
      <Card className="console-card" bodyStyle={{ padding: 20 }}>
        {status === 'pending' || status === 'running' ? <Spin tip="正在后台导入，可以离开此页" /> : <Title heading={5}>{result.replay ? '已回放之前的导入结果' : status === 'failed' ? '导入未完成' : '导入已完成'}</Title>}
        <div className="source-result-grid"><div>源记录<strong>{counts.sourceItems}</strong></div><div>新增内容版本<strong>{counts.importedVersions}</strong></div><div>已存在，已跳过<strong>{counts.skippedExisting}</strong></div><div>缺少内容<strong>{counts.skippedNoContent}</strong></div><div>失败<strong>{counts.failedItems}</strong></div></div>
        {ledger?.errorMessage || error ? <div className="source-error" role="alert">{ledger?.errorMessage || error}</div> : null}
        {ledger?.failures?.length ? <ul>{ledger.failures.map((failure, index) => <li key={`${failure.sourceId}-${index}`}>第 {failure.sourceId} 条：{failure.reason}</li>)}</ul> : null}
        <div className="console-page__actions"><Button theme="solid" type="primary" disabled={counts.importedVersions === 0 || status === 'pending' || status === 'running'} onClick={() => navigate(scope.href('project.review'))}>审阅导入数据</Button><Button disabled={status === 'pending' || status === 'running'} onClick={() => { setResult(null); setLedger(null); setPreview(null); previewedInput.current = '' }}>继续导入</Button></div>
      </Card>
    </div>
  }

  return <div className="console-page source-import-page product-import" data-studio-page="source-import" data-source-import-state={error || capabilityError ? 'error' : validated ? 'preview' : content ? 'default' : 'empty'}>
    <div className="console-page__header"><div><Title heading={4}>导入数据集</Title></div><Link to={scope.href('project.sources')}>素材来源</Link></div>
    <div className="product-import__workspace">
    <Card className="console-card product-import__input" bodyStyle={{ padding: 20 }}>
      <label className="product-import__upload" onDragOver={(event) => event.preventDefault()} onDrop={(event) => { event.preventDefault(); const file = event.dataTransfer.files[0]; if (file && !busy && canRun) void readFile(file) }}>
        <strong>{content ? sourceKey || '已选择数据' : '选择或拖入数据集文件'}</strong>
        <span>Alpaca / ShareGPT / JSONL · 最多 20 MB</span>
        <input type="file" accept=".json,.jsonl,.ndjson,application/json" disabled={busy || !canRun} aria-label="选择外部数据集文件" onChange={(event) => { const file = event.target.files?.[0]; if (file) void readFile(file); event.target.value = '' }} />
      </label>
      <div className="source-import-fields">
        <label><span id="source-product-format-label">外部数据集格式</span><Select aria-labelledby="source-product-format-label" value={format} disabled={busy} optionList={Object.entries(FORMAT_DETAILS).map(([value, details]) => ({ value, label: details.label }))} onChange={(next) => { if (next === format) return; operation.current++; setFormat(next as ProductImportRequest['format']); setPreview(null) }} /></label>
        <label><span>导入名称</span><Input aria-label="导入名称" placeholder="例如：医疗问答导出 2026-10" value={sourceKey} disabled={busy} onChange={(next) => edit(setSourceKey, next)} /></label>
      </div>
      <details className="product-disclosure">
      <summary>粘贴或编辑数据</summary>
      <label className="source-text-input"><span>数据内容</span><TextArea aria-label="外部数据集内容" value={content} disabled={busy} onChange={(next) => edit(setContent, next)} placeholder={FORMAT_DETAILS[format].example} autosize={{ minRows: 8, maxRows: 16 }} /></label>
      </details>
      <details className="product-disclosure"><summary>格式映射与导入说明</summary>
      <Text type="tertiary" className="block my-3">{FORMAT_DETAILS[format].mapping}</Text>
      <Text type="tertiary" size="small">每次最多 5000 条；重复内容自动跳过。</Text>
      <label className="source-text-input"><span>导入说明（选填）</span><Input aria-label="导入说明" value={changeReason} disabled={busy} onChange={(next) => edit(setChangeReason, next)} /></label>
      </details>
      {error ? <div className="source-error" role="alert">{error}</div> : null}
      <Button loading={previewing || readingFile} disabled={busy || !canRun} onClick={() => void check()}>校验并预览</Button>
      {capabilityLoading ? <Text type="tertiary" className="block mt-2">正在读取导入权限</Text> : capabilityError ? <div className="source-error" role="alert">权限读取失败：{capabilityError}<Button size="small" onClick={() => setCapabilityRetry((value) => value + 1)}>重试读取权限</Button></div> : !canRun ? <Text type="tertiary" className="block mt-2">当前项目没有导入权限，请联系项目负责人。</Text> : null}
    </Card>
    <Card className="console-card product-import__preview" bodyStyle={{ padding: 20 }}>
    {validated ? <div data-source-import-preview="true">
      <Text strong>导入预览</Text>
      <div className="source-result-grid"><div>源记录<strong>{preview.sourceItems}</strong></div><div>有效记录<strong>{preview.validItems}</strong></div><div>重复内容<strong>{preview.duplicateItems}</strong></div><div>无法导入<strong>{preview.failedItems}</strong></div></div>
      {preview.failures.length ? <ul>{preview.failures.map((failure, index) => <li key={`${failure.sourceId}-${index}`}>第 {failure.sourceId} 条：{failure.reason}</li>)}</ul> : null}
      <Text type="tertiary" className="block mb-3">有效内容导入后进入待审阅队列。</Text>
      <Button theme="solid" type="primary" loading={importing} disabled={busy || preview.validItems === 0 || !canRun} onClick={() => void importData()}>确认导入 {preview.validItems} 条有效记录</Button>
    </div> : <div className="source-state">{busy ? <Spin tip="正在校验数据" /> : <p>选择文件后自动识别格式并预览。粘贴数据后点击“校验并预览”。</p>}</div>}
    </Card>
    </div>
  </div>
}

/** File auto-detection only supplies a default; server validation is authoritative. */
function detectProductFormat(name: string, content: string): ProductImportRequest['format'] {
  if (/\.(jsonl|ndjson)$/i.test(name)) return 'jsonl'
  try {
    const parsed = JSON.parse(content) as unknown
    const first = Array.isArray(parsed) ? parsed[0] : parsed
    if (first && typeof first === 'object' && 'conversations' in first) return 'sharegpt'
    if (first && typeof first === 'object' && 'instruction' in first) return 'alpaca'
  } catch { /* The server will report malformed content during preview. */ }
  return 'jsonl'
}
