import { useEffect, useRef, useState } from 'react'
import { Button, Empty, Input, Spin, Typography } from '@douyinfe/semi-ui'
import { sourceApi } from '../lib/api/source'
import type { SourceChunk, SourcePage } from '../lib/api/source'
import type { ProjectResourceId } from '../lib/api/studio'

const { Text } = Typography
const PAGE_SIZE = 10

/** 按真实来源版本分页选择。已选择的跨页素材继续可见，并可独立取消。 */
export function SourceChunkSelector({ projectId, sourceVersionId, value, disabled, onChange }: {
  projectId: ProjectResourceId; sourceVersionId?: number; value: number[]; disabled: boolean; onChange: (ids: number[]) => void
}) {
  const [expanded, setExpanded] = useState(false)
  const [query, setQuery] = useState('')
  const [offset, setOffset] = useState(0)
  const [retry, setRetry] = useState(0)
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [page, setPage] = useState<SourcePage<SourceChunk>>({ items: [], total: 0, limit: PAGE_SIZE, offset: 0 })
  const [labels, setLabels] = useState<Record<number, string>>({})
  const known = useRef(new Set<number>())
  const selectedKey = value.join(',')

  useEffect(() => {
    let cancelled = false
    const missing = value.filter((id) => !known.current.has(id))
    if (missing.length === 0) return
    void Promise.allSettled(missing.map((id) => sourceApi.getChunk(projectId, id))).then((results) => {
      if (cancelled) return
      const next: Record<number, string> = {}
      results.forEach((result, index) => {
        const id = missing[index]
        if (result.status === 'fulfilled') { next[id] = result.value.headingPath || `素材块 ${result.value.ordinal}`; known.current.add(id) }
        else next[id] = '素材块无法读取'
      })
      setLabels((current) => ({ ...current, ...next }))
    })
    return () => { cancelled = true }
  }, [projectId, selectedKey])

  useEffect(() => {
    let cancelled = false
    if (!expanded || !sourceVersionId) return
    setLoading(true)
    setError(null)
    void sourceApi.listChunks(projectId, { sourceVersionId, q: query, offset, limit: PAGE_SIZE }).then((result) => {
      if (cancelled) return
      setPage(result)
      result.items.forEach((chunk) => known.current.add(chunk.id))
      setLabels((current) => ({ ...current, ...Object.fromEntries(result.items.map((chunk) => [chunk.id, chunk.headingPath || `素材块 ${chunk.ordinal}`])) }))
    }).catch((reason) => { if (!cancelled) setError(reason instanceof Error ? reason.message : '素材块读取失败') })
      .finally(() => { if (!cancelled) setLoading(false) })
    return () => { cancelled = true }
  }, [expanded, offset, projectId, query, retry, sourceVersionId])

  return <div className="source-chunk-selector" data-source-chunk-selector="true">
    <div className="source-panel-head"><Text size="small">已关联 {value.length} 个素材块</Text><Button size="small" disabled={disabled} onClick={() => setExpanded(!expanded)}>{expanded ? '收起素材选择' : '选择素材块'}</Button></div>
    {value.length ? <ul className="source-selected-chunks">{value.map((id) => <li key={id}><span>{labels[id] || '正在读取素材'} · #{id}</span><Button size="small" disabled={disabled} aria-label={`取消素材块 ${id}`} onClick={() => onChange(value.filter((item) => item !== id))}>取消关联</Button></li>)}</ul> : <Text type="warning" size="small">请选择实际素材块，保存后才能按文档来源生成。</Text>}
    {expanded ? <div className="source-selector-results">
      {!sourceVersionId ? <Empty description="还没有已解析的素材，请先到素材来源上传文档。" /> : <>
        <Input aria-label="搜索可关联素材" placeholder="搜索素材内容" value={query} disabled={disabled} onChange={(next) => { setQuery(next); setOffset(0) }} />
        {loading ? <Spin tip="正在读取素材" /> : error ? <div className="source-error" role="alert">{error}<Button size="small" onClick={() => setRetry(retry + 1)}>重试</Button></div> : page.items.length === 0 ? <Empty description={query ? '没有匹配的素材，请调整关键词。' : '来源尚未完成解析。'} /> : <ul>{page.items.map((chunk) => <li key={chunk.id}><label><input type="checkbox" aria-label={`关联素材块 ${chunk.id}`} disabled={disabled} checked={value.includes(chunk.id)} onChange={(event) => onChange(event.target.checked ? [...value, chunk.id] : value.filter((id) => id !== chunk.id))} /><span><strong>{chunk.headingPath || `素材块 ${chunk.ordinal}`} · #{chunk.id}</strong><small>{chunk.content.slice(0, 180)}{chunk.content.length > 180 ? '…' : ''}</small></span></label></li>)}</ul>}
        <div className="source-pagination"><Button size="small" disabled={disabled || loading || offset === 0} onClick={() => setOffset(Math.max(0, offset - PAGE_SIZE))}>上一页素材</Button><Text size="small">共 {page.total} 块</Text><Button size="small" disabled={disabled || loading || offset + PAGE_SIZE >= page.total} onClick={() => setOffset(offset + PAGE_SIZE)}>下一页素材</Button></div>
      </>}
    </div> : null}
  </div>
}
