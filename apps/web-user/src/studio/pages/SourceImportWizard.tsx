import { useEffect, useRef, useState } from 'react'
import { Link } from 'react-router-dom'
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
  const [format, setFormat] = useState<ProductImportRequest['format']>('alpaca')
  const [sourceKey, setSourceKey] = useState('')
  const [content, setContent] = useState('')
  const [changeReason, setChangeReason] = useState('')
  const [preview, setPreview] = useState<ProductImportPreview | null>(null)
  const [previewing, setPreviewing] = useState(false)
  const [importing, setImporting] = useState(false)
  const [readingFile, setReadingFile] = useState(false)
  const [canRun, setCanRun] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [result, setResult] = useState<SourceImportResult | null>(null)
  const [ledger, setLedger] = useState<SourceImportLedger | null>(null)
  const previewedInput = useRef('')
  const operation = useRef(0)

  useEffect(() => {
    let cancelled = false
    void studioApi.overviewEnvelope(scope.projectId).then((response) => {
      if (!cancelled) setCanRun(response.capabilities.canRun === true)
    }).catch(() => { if (!cancelled) setCanRun(false) })
    return () => { cancelled = true }
  }, [scope.projectId])

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
  const identity = JSON.stringify(input)
  const validated = preview !== null && previewedInput.current === identity
  const busy = previewing || importing || readingFile
  const edit = (setter: (value: string) => void, value: string) => { operation.current++; setPreview(null); setError(null); setter(value) }

  const validate = () => {
    if (!input.sourceKey) return '请填写导入名称，方便从导入记录中识别这份数据。'
    if (!content.trim()) return '请选择文件，或粘贴要导入的数据。'
    if (new TextEncoder().encode(content).byteLength > PRODUCT_LIMIT) return '外部数据集不能超过 20 MB，请拆分后导入。'
    return null
  }

  const check = async () => {
    setError(null)
    const invalid = validate()
    if (invalid) { setError(invalid); return }
    const serial = ++operation.current
    setPreviewing(true)
    try {
      const response = await sourceApi.previewProducts(scope.projectId, input)
      if (serial !== operation.current) return
      previewedInput.current = identity
      setPreview(response)
    } catch (previewError) { if (serial === operation.current) setError(previewError instanceof Error ? previewError.message : '格式校验失败') }
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
    if (file.size > PRODUCT_LIMIT || file.size === 0) { setError('请选择非空的 JSON/JSONL 文件，最多 20 MB。'); return }
    setReadingFile(true)
    const serial = ++operation.current
    try {
      const text = await file.text()
      if (serial !== operation.current) return
      setContent(text)
      if (!sourceKey) setSourceKey(file.name)
      if (/\.(jsonl|ndjson)$/i.test(file.name)) setFormat('jsonl')
      setPreview(null)
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
        <Text type="tertiary" className="block mb-3">导入的内容会进入人工审阅；外部成品不计为素材接地生成。</Text>
        <div className="console-page__actions"><Link to={scope.href('project.data')}>查看数据与审阅状态</Link><Link to={scope.href('project.review')}>进入审阅</Link><Button disabled={status === 'pending' || status === 'running'} onClick={() => { setResult(null); setLedger(null); setPreview(null); previewedInput.current = '' }}>继续导入另一份数据</Button></div>
      </Card>
    </div>
  }

  return <div className="console-page source-import-page" data-studio-page="source-import" data-source-import-state={error ? 'error' : validated ? 'preview' : content ? 'default' : 'empty'}>
    <div className="console-page__header"><div><Title heading={4}>导入外部数据集</Title><Text type="tertiary">可导入 Easy Dataset 等工具导出的 Alpaca、ShareGPT 和 JSONL 文件。</Text></div><Link to={scope.href('project.sources')}>返回素材来源</Link></div>
    <Card className="console-card mb-3" bodyStyle={{ padding: 18 }}>
      <Text strong className="block mb-3">1 · 选择格式与内容</Text>
      <div className="source-import-fields">
        <label><span>数据格式</span><Select aria-label="外部数据集格式" value={format} disabled={busy} optionList={Object.entries(FORMAT_DETAILS).map(([value, details]) => ({ value, label: details.label }))} onChange={(next) => { operation.current++; setFormat(next as ProductImportRequest['format']); setPreview(null) }} /></label>
        <label><span>导入名称</span><Input aria-label="导入名称" placeholder="例如：医疗问答导出 2026-10" value={sourceKey} disabled={busy} onChange={(next) => edit(setSourceKey, next)} /></label>
      </div>
      <Text type="tertiary" className="block my-3">{FORMAT_DETAILS[format].mapping}</Text>
      <label className="source-upload-control">选择 JSON/JSONL 文件（最多 20 MB）<input type="file" accept=".json,.jsonl,.ndjson,application/json" disabled={busy} aria-label="选择外部数据集文件" onChange={(event) => { const file = event.target.files?.[0]; if (file) void readFile(file); event.target.value = '' }} /></label>
      <label className="source-text-input"><span>数据内容</span><TextArea aria-label="外部数据集内容" value={content} disabled={busy} onChange={(next) => edit(setContent, next)} placeholder={FORMAT_DETAILS[format].example} autosize={{ minRows: 8, maxRows: 16 }} /></label>
      <Text type="tertiary" size="small">同内容重复导入会回放原结果。每次最多 5000 条记录；记录数由服务端校验。</Text>
      <label className="source-text-input"><span>导入说明（选填）</span><Input aria-label="导入说明" value={changeReason} disabled={busy} onChange={(next) => edit(setChangeReason, next)} /></label>
      {error ? <div className="source-error" role="alert">{error}</div> : null}
      <Button loading={previewing || readingFile} disabled={busy || !canRun} onClick={() => void check()}>校验并预览</Button>
      {!canRun ? <Text type="tertiary" className="block mt-2">当前项目没有导入权限，请联系项目负责人。</Text> : null}
    </Card>
    {validated ? <Card className="console-card" bodyStyle={{ padding: 18 }} data-source-import-preview="true">
      <Text strong>2 · 确认导入范围</Text>
      <div className="source-result-grid"><div>源记录<strong>{preview.sourceItems}</strong></div><div>有效记录<strong>{preview.validItems}</strong></div><div>重复内容<strong>{preview.duplicateItems}</strong></div><div>无法导入<strong>{preview.failedItems}</strong></div></div>
      {preview.failures.length ? <ul>{preview.failures.map((failure, index) => <li key={`${failure.sourceId}-${index}`}>第 {failure.sourceId} 条：{failure.reason}</li>)}</ul> : null}
      <Text type="tertiary" className="block mb-3">确认后仅导入有效内容；重复内容跳过，失败原因保留在导入记录中。内容进入待审阅状态。</Text>
      <Button theme="solid" type="primary" loading={importing} disabled={busy || preview.validItems === 0 || !canRun} onClick={() => void importData()}>确认导入 {preview.validItems} 条有效记录</Button>
    </Card> : null}
  </div>
}
