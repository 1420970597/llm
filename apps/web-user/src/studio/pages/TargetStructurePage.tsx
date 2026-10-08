import { useEffect, useState } from 'react'
import { Link, useNavigate } from 'react-router-dom'
import { Button, Card, Spin, Typography } from '@douyinfe/semi-ui'
import { client } from '../../lib/api'
import { projectPath } from '../../lib/api/studio'
import { CopyVersionButton, CoveragePayloadEditor, DocumentHistory, DocumentSaveBar, useVersionedDocument } from '../DocumentEditors'
import { coverageQuotaOf, deriveCoverageStructure } from '../coverageStructure'
import { useProjectScope } from '../ProjectLayout'
import './SourcePages.css'

const { Title, Text } = Typography

export function TargetStructurePage() {
  const scope = useProjectScope()
  const navigate = useNavigate()
  const state = useVersionedDocument(scope.projectId, 'coverage-versions')
  const [sourceVersionId, setSourceVersionId] = useState<number>()
  const [sourceError, setSourceError] = useState<string | null>(null)
  const [estimate, setEstimate] = useState<{ domains: number; directions: number; questions: number }>()

  useEffect(() => {
    let cancelled = false
    void client.get<{ items: Array<{ id: number }> }>(`${projectPath(scope.projectId)}/source-versions?limit=1`).then((response) => {
      if (!cancelled) { setSourceVersionId(response.data.items[0]?.id); setSourceError(null) }
    }).catch((error) => { if (!cancelled) setSourceError(error instanceof Error ? error.message : '来源版本读取失败') })
    void client.get<{ data: { domainCount: number; directionsPerDomain: number; questionsPerDirection: number } }>(projectPath(scope.projectId)).then((response) => {
      const project = response.data.data
      if (!cancelled) setEstimate({ domains: project.domainCount, directions: project.directionsPerDomain, questions: project.questionsPerDirection })
    }).catch(() => { /* 创建时估算读取失败不覆盖实际结构文档的状态。 */ })
    return () => { cancelled = true }
  }, [scope.projectId])

  if (state.loading) return <div className="source-state" data-source-state="loading"><Spin tip="正在加载目标结构" /></div>
  if (state.error) return <Card className="console-card" data-source-state="error"><Text type="danger">{state.error}</Text><Button onClick={() => void state.reload()}>重试</Button></Card>

  const payload = state.payload ?? { schemaVersion: 'coverage.v1', domains: [] }
  const domains = Array.isArray(payload.domains) ? payload.domains as Array<Record<string, unknown>> : []
  const structure = deriveCoverageStructure(payload)
  const planned = estimate ? estimate.domains * estimate.directions * estimate.questions : undefined
  const directions = domains.flatMap((domain) => (Array.isArray(domain.directions) ? domain.directions as Array<Record<string, unknown>> : []).map((direction) => ({ domain, direction })))
  const unresolved = directions.filter(({ direction }) => coverageQuotaOf(direction) > 0 && (direction.source === 'none' || direction.source === 'document' && (!Array.isArray(direction.sourceChunkIds) || direction.sourceChunkIds.length === 0)))

  return <div className="console-page source-target-page" data-studio-page="coverage" data-source-state={domains.length ? 'default' : 'empty'}>
    <div className="console-page__header"><div><Title heading={4}>目标结构</Title><Text type="tertiary">编辑领域、方向、配额与来源。保存新版本不会改写已经运行的批次。</Text></div><div className="console-page__actions"><Link to={scope.href('project.sources')}>素材来源</Link><CopyVersionButton state={state} /></div></div>
    <Card className="console-card mb-3" bodyStyle={{ padding: 16 }}>
      <Text strong>当前结构：{structure.domainCount} 个领域 · {structure.directionCount} 个方向 · {structure.capacity} 个计划单元</Text>
      <Text type="tertiary" size="small" className="block">计划单元数不代表已产出样本。显式零配额方向不参与生产。</Text>
      {estimate ? <Text size="small" className="block">创建时估算：{estimate.domains} × {estimate.directions} × {estimate.questions} = {planned}；{planned === structure.capacity ? '与当前结构一致。' : '与当前结构不同，请以实际保存的目标结构规划批次。'}</Text> : null}
      {unresolved.length ? <Text type="warning" className="block">{unresolved.length} 个方向尚未具备来源：选择文档素材并关联分块，或选择 AI 合成。未选择来源可先保存草稿；文档来源必须关联素材块。</Text> : null}
      {sourceError ? <Text type="danger" className="block">素材列表不可用：{sourceError}。请从素材来源页重新加载。</Text> : null}
    </Card>
    <CoveragePayloadEditor payload={payload} disabled={state.isReadOnly || !state.canEdit} projectId={scope.projectId} sourceVersionId={sourceVersionId} onChange={state.setPayload} />
    <DocumentSaveBar state={state} label="覆盖方案" />
    <DocumentHistory state={state} />
    {directions.length ? <Card className="console-card mt-3" bodyStyle={{ padding: 14 }}><Text strong className="block mb-2">从方向开始下一批试制</Text><div className="coverage-directions coverage-directions--actions">
      {directions.map(({ domain, direction }) => <button key={`${String(domain.stableId)}-${String(direction.stableId)}`} type="button" className="coverage-gap" data-coverage-gap="true" disabled={state.dirty || state.isReadOnly || coverageQuotaOf(direction) === 0 || unresolved.some((entry) => entry.direction === direction)} onClick={() => navigate(`${scope.href('project.pilot')}?slice=${encodeURIComponent(String(direction.stableId ?? ''))}`)}>以“{String(direction.name ?? '未命名方向')}”规划新批次</button>)}
    </div>{state.dirty ? <Text type="tertiary" size="small">先保存目标结构，再规划批次。</Text> : null}</Card> : null}
  </div>
}
