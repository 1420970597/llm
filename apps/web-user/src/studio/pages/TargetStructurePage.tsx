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
    <div className="console-page__header"><div><Title heading={4}>目标结构</Title></div><div className="console-page__actions"><Link to={scope.href('project.sources')}>素材来源</Link><CopyVersionButton state={state} /></div></div>
    <Card className="console-card mb-3" bodyStyle={{ padding: 16 }}>
      <Text strong>当前结构：{structure.domainCount} 个领域 · {structure.directionCount} 个方向 · {structure.capacity} 个计划单元</Text>
      {estimate && planned !== structure.capacity ? <details className="product-disclosure"><summary>与创建时估算不同</summary><Text size="small">创建时估算：{estimate.domains} × {estimate.directions} × {estimate.questions} = {planned}；以当前结构规划批次。零配额方向不参与生产。</Text></details> : null}
      {unresolved.length ? <Text type="warning" className="block">{unresolved.length} 个方向缺少来源：选择 AI 合成，或关联文档素材块。</Text> : null}
      {sourceError ? <Text type="danger" className="block">素材列表不可用：{sourceError}。请从素材来源页重新加载。</Text> : null}
    </Card>
    <CoveragePayloadEditor payload={payload} disabled={state.isReadOnly || !state.canEdit} projectId={scope.projectId} sourceVersionId={sourceVersionId} onChange={state.setPayload} />
    <DocumentSaveBar state={state} label="覆盖方案" />
    <details className="product-disclosure"><summary>版本历史</summary><DocumentHistory state={state} /></details>
    <div className="product-stage-footer"><span>{state.dirty ? '保存修改后继续' : state.current ? '目标结构已保存' : '先保存目标结构'}</span><Button theme="solid" type="primary" disabled={state.dirty || !state.current} onClick={() => navigate(scope.href('project.blueprint'))}>继续配置生成 →</Button></div>
    {directions.length ? <details className="product-disclosure"><summary>仅试制特定方向</summary><Card className="console-card mt-3" bodyStyle={{ padding: 14 }}><div className="coverage-directions coverage-directions--actions">
      {directions.map(({ domain, direction }) => <button key={`${String(domain.stableId)}-${String(direction.stableId)}`} type="button" className="coverage-gap" data-coverage-gap="true" disabled={state.dirty || state.isReadOnly || coverageQuotaOf(direction) === 0 || unresolved.some((entry) => entry.direction === direction)} onClick={() => navigate(`${scope.href('project.pilot')}?slice=${encodeURIComponent(String(direction.stableId ?? ''))}`)}>以“{String(direction.name ?? '未命名方向')}”规划新批次</button>)}
    </div>{state.dirty ? <Text type="tertiary" size="small">先保存目标结构，再规划批次。</Text> : null}</Card></details> : null}
  </div>
}
