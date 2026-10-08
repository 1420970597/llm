import { useEffect, useState } from 'react'
import { Link, useSearchParams } from 'react-router-dom'
import type { ReactNode } from 'react'
import { Button, Card, Empty, Spin, Typography } from '@douyinfe/semi-ui'
import { RefreshCw } from 'lucide-react'
import { consoleApi, type Dataset } from '../../lib/api'
import { parseProjectResourceId, settingsApi } from '../../lib/api/studio'
import type { ProjectListItem } from '../../lib/api/studio'
import { CleaningView, type CleaningNavigation } from '../../views/CleaningView'
import { EvaluationView } from '../../views/EvaluationView'
import { useBlueprintContext } from '../blueprintContext'

const { Title, Text } = Typography

/**
 * 旧控制台的评估/清洗实现已经有完整的真实 API 链路（L8-L14）。
 *
 * 这些页面只负责把数据集上下文接到 Atelier 的辅助层：不复制业务状态，
 * 也不伪造项目数据。这样旧版深链和新入口共享同一套运行、报告及重试语义，
 * 后续替换视觉层时也不会出现两套清洗/评估结果。
 */
function useLegacyDatasets() {
  const [datasets, setDatasets] = useState<Dataset[]>([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)

  const load = async () => {
    setLoading(true)
    setError(null)
    try {
      setDatasets(await consoleApi.listDatasets())
    } catch (loadError) {
      setError(loadError instanceof Error ? loadError.message : '加载数据项目失败')
    } finally {
      setLoading(false)
    }
  }

  useEffect(() => {
    void load()
  }, [])

  return { datasets, loading, error, reload: load }
}

/**
 * 历史数据集链路由显式「历史数据集」模式承载；默认工作台读取原生项目蓝图。
 */
function LegacyToolFrame({
  title,
  description,
  children,
  projectLinks = [],
}: {
  title: string
  description: string
  children: ReactNode
  projectLinks?: Array<{ name: string; href: string; legacyDatasetId: number }>
}) {
  const [linksOpen, setLinksOpen] = useState(false)
  return (
    <div className="console-page" data-studio-page="legacy-tool">
      <div className="console-page__header">
        <div>
          <Title heading={4} className="!mb-1">{title}</Title>
          <Text type="tertiary">{description}</Text>
        </div>
        <Text type="tertiary" size="small">Atelier 辅助工作台 · 真实数据</Text>
        <Link to={title === '评估工作台' ? '/tools/evaluation' : '/tools/cleaning'}>切换到项目蓝图</Link>
      </div>
      <Card className="console-card mb-3" bodyStyle={{ padding: 14 }} data-tool-scope="true">
        <Text strong size="small" className="block">
          这个工作台处理的是什么
        </Text>
        <Text type="tertiary" size="small" className="block mt-1">
          本页以<strong>旧数据集</strong>为单位（迁移前的历史资产），不读取项目的蓝图快照。
          新项目的质量结论请用项目内的「质量」页 —— 那里的实验会冻结蓝图、
          覆盖、标准与素材的版本，因此结论可复现。
        </Text>
        {projectLinks.length > 0 ? (
          <div className="mt-2">
            <Button
              size="small"
              theme="borderless"
              onClick={() => setLinksOpen((current) => !current)}
              data-tool-project-links-toggle="true"
            >
              {linksOpen ? '收起相关项目' : `查看 ${projectLinks.length} 个相关项目 →`}
            </Button>
            {linksOpen ? (
              <ul className="review-evidence mt-2" data-tool-project-links="true">
                {projectLinks.map((link) => (
                  <li key={link.href}>
                    <a href={link.href}>{link.name}</a>
                    <Text type="tertiary" size="small" className="block">
                      已绑定旧数据集 #{link.legacyDatasetId}
                    </Text>
                  </li>
                ))}
              </ul>
            ) : null}
          </div>
        ) : (
          <Text type="tertiary" size="small" className="block mt-2">
            还没有项目绑定过旧数据集。迁移旧资产后，这里会列出对应的项目。
          </Text>
        )}
      </Card>
      {children}
    </div>
  )
}

/** 读取「绑定了旧数据集」的项目，用于把工作台接回 Atelier 主线。 */
function useLegacyLinkedProjects() {
  const [links, setLinks] = useState<Array<{ name: string; href: string; legacyDatasetId: number }>>([])
  useEffect(() => {
    let cancelled = false
    void (async () => {
      try {
        const page = await settingsApi.projects({ limit: 50 })
        if (cancelled) return
        setLinks(
          (page.items ?? [])
            .filter((project: ProjectListItem) => Number(project.data?.legacyDatasetId ?? 0) > 0)
            .map((project: ProjectListItem) => ({
              name: project.data.name || `项目 #${project.data.id}`,
              href: `/p/${project.data.id}/overview`,
              legacyDatasetId: Number(project.data.legacyDatasetId),
            })),
        )
      } catch {
        // 读不到相关项目不影响本页主功能（旧数据集的评估/清洗链路独立可用），
        // 因此这里静默降级为「没有相关项目」，而不是把整页变成错误态。
        if (!cancelled) setLinks([])
      }
    })()
    return () => {
      cancelled = true
    }
  }, [])
  return links
}

function DatasetLoadingState({
  loading,
  error,
  onReload,
}: {
  loading: boolean
  error: string | null
  onReload: () => void
}) {
  if (loading) {
    return (
      <div className="atelier-inline-state" data-tool-loading="true">
        <Spin tip="正在加载数据项目" />
      </div>
    )
  }
  if (error) {
    return (
      <Card className="console-card" bodyStyle={{ padding: 18 }} data-tool-error="true">
        <Text type="danger" className="block">{error}</Text>
        <Button className="mt-3" size="small" icon={<RefreshCw size={14} />} onClick={onReload}>重试</Button>
      </Card>
    )
  }
  return null
}

/** Atelier 原生入口：多模型评估、维度管理、运行与报告。 */
function LegacyEvaluationTool() {
  const { datasets, loading, error, reload } = useLegacyDatasets()
  const projectLinks = useLegacyLinkedProjects()
  const [searchParams] = useSearchParams()
  const requestedDatasetId = Number(searchParams.get('datasetId'))
  const initialDatasetId = Number.isSafeInteger(requestedDatasetId) && requestedDatasetId > 0 ? requestedDatasetId : null
  return (
    <LegacyToolFrame
      title="评估工作台"
      description="从评估维度与裁判配置，到运行进度、逐条证据和最终报告，完整保留原有质量评估能力。"
      projectLinks={projectLinks}
    >
      <DatasetLoadingState loading={loading} error={error} onReload={() => void reload()} />
      {!loading && !error ? <EvaluationView datasets={datasets} initialDatasetId={initialDatasetId} /> : null}
    </LegacyToolFrame>
  )
}

/** Atelier 原生入口：关键词/规则配置、异步扫描与命中报告。 */
function LegacyCleaningTool() {
  const { datasets, loading, error, reload } = useLegacyDatasets()
  const projectLinks = useLegacyLinkedProjects()
  const [searchParams] = useSearchParams()
  const requestedDatasetId = Number(searchParams.get('datasetId'))
  const initialDatasetId = Number.isSafeInteger(requestedDatasetId) && requestedDatasetId > 0 ? requestedDatasetId : null
  const navigation: CleaningNavigation = {
    planning: '/new',
    tasks: '/projects',
    evaluation: '/tools/evaluation',
    cleaning: '/tools/cleaning',
    results: '/deliveries',
    home: '/today',
  }
  return (
    <LegacyToolFrame
      title="清洗工作台"
      description="配置拒答关键词和清洗规则，发起真实扫描，查看命中证据并把干净结果送往交付。"
      projectLinks={projectLinks}
    >
      <DatasetLoadingState loading={loading} error={error} onReload={() => void reload()} />
      {!loading && !error ? <CleaningView datasets={datasets} initialDatasetId={initialDatasetId} navigation={navigation} /> : null}
    </LegacyToolFrame>
  )
}

function ProjectToolPage({ kind }: { kind: 'evaluation' | 'cleaning' }) {
  const [params, setParams] = useSearchParams()
  const [projects, setProjects] = useState<ProjectListItem[]>([])
  const [cursor, setCursor] = useState<string>()
  const [nextCursor, setNextCursor] = useState('')
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)
  const [retry, setRetry] = useState(0)
  const projectId = parseProjectResourceId(params.get('projectId') ?? '')
  const blueprint = useBlueprintContext(projectId || undefined, params.get('blueprintVersionId'))
  useEffect(() => {
    let cancelled = false
    setLoading(true); setError(null)
    void settingsApi.projects({ limit: 50, cursor }).then((page) => {
      if (cancelled) return
      setProjects((previous) => cursor ? [...previous, ...page.items] : page.items)
      setNextCursor(page.nextCursor ?? '')
    }).catch((loadError) => { if (!cancelled) setError(loadError instanceof Error ? loadError.message : '项目读取失败') })
      .finally(() => { if (!cancelled) setLoading(false) })
    return () => { cancelled = true }
  }, [cursor, retry])
  const nodes = (blueprint.current?.payload.nodes ?? {}) as Record<string, Record<string, unknown>>
  const evaluation = nodes.evaluation ?? {}
  const rules = nodes.rules ?? {}
  const context = new URLSearchParams()
  if (projectId) context.set('projectId', String(projectId))
  if (blueprint.current) context.set('blueprintVersionId', String(blueprint.current.id))
  const base = projectId ? `/p/${encodeURIComponent(String(projectId))}` : ''
  return <div className="console-page" data-studio-page="project-tool" data-tool-kind={kind}>
    <div className="console-page__header"><div><Title heading={4}>{kind === 'evaluation' ? '评估工作台' : '清洗工作台'}</Title><Text type="tertiary">选择项目与蓝图版本，沿用该版本的裁判配置或清洗策略。</Text></div><Link to={`?mode=legacy`}>历史数据集工具</Link></div>
    <Card className="console-card mb-3" bodyStyle={{ padding: 18 }} data-tool-scope="project">
      <div className="wizard-fields">
        <label className="wizard-field"><span className="wizard-field__label">项目</span><select aria-label="工作台项目" className="wizard-input" value={projectId ?? ''} onChange={(event) => { const next = new URLSearchParams(); next.set('projectId', event.target.value); setParams(next) }}><option value="">请选择项目</option>{projects.map((project) => <option key={project.data.id} value={project.data.id}>{project.data.name || `项目 ${project.data.id}`}</option>)}</select></label>
        <label className="wizard-field"><span className="wizard-field__label">蓝图版本</span><select aria-label="工作台蓝图版本" className="wizard-input" disabled={!projectId || blueprint.loading || !blueprint.versions.length} value={blueprint.current?.id ?? ''} onChange={(event) => { const next = new URLSearchParams(params); next.set('blueprintVersionId', event.target.value); setParams(next) }}><option value="">请选择蓝图版本</option>{blueprint.versions.map((version) => <option key={version.id} value={version.id}>v{version.version} · {version.changeReason || '蓝图配置'}</option>)}</select></label>
      </div>
      {nextCursor ? <Button className="mt-2" disabled={loading} onClick={() => setCursor(nextCursor)}>加载更多项目</Button> : null}
      {loading || blueprint.loading ? <div className="atelier-inline-state" data-tool-loading="true"><Spin tip="正在加载项目与蓝图" /></div> : null}
      {error || blueprint.error ? <div className="source-error" role="alert" data-tool-error="true">{error || blueprint.error}<Button onClick={() => { setRetry((value) => value + 1); blueprint.reload() }}>重试</Button></div> : null}
      {!loading && !error && !projects.length ? <Empty description="还没有项目。创建项目后配置蓝图，再运行评估或规则预览。"><Link to="/new">创建项目</Link></Empty> : null}
      {projectId && !blueprint.loading && !blueprint.error && !blueprint.current ? <Empty description="当前项目还没有蓝图版本。"><Link to={`${base}/blueprint`}>配置蓝图</Link></Empty> : null}
      {!projectId && projects.length ? <Text type="tertiary" className="block mt-3">请选择要处理的项目，工作台会读取该项目的真实蓝图。</Text> : null}
    </Card>
    {projectId && blueprint.current && !blueprint.error ? <Card className="console-card" bodyStyle={{ padding: 18 }} data-tool-blueprint-context="true">
      <Text strong className="block mb-2">蓝图 v{blueprint.current.version} · {blueprint.current.changeReason || '已保存配置'}</Text>
      {kind === 'evaluation' ? <Text className="block mb-3">裁判配置：{Array.isArray(evaluation.judgeConnectionIds) && evaluation.judgeConnectionIds.length ? `${evaluation.judgeConnectionIds.length} 名（独立性由服务端验证）` : '尚未配置'} · 抽样种子：{String(evaluation.samplingSeed ?? 42)} · 缺分处理：{({ exclude: '排除缺分', zero: '计为零分', not_applicable: '不适用', fail_experiment: '实验失败' } as Record<string, string>)[String(evaluation.missingScorePolicy)] ?? '排除缺分'}</Text> : <Text className="block mb-3">清洗策略：{Number(rules.qualityPolicyVersionId) > 0 ? '已引用不可变策略版本' : '尚未配置'}。规则预览读取此蓝图所引用的策略，预览不会改写样本。</Text>}
      <div className="console-page__actions"><Link to={`${base}/${kind === 'evaluation' ? 'quality/new' : 'rules'}?${context}`}>{kind === 'evaluation' ? '使用此蓝图创建质量实验' : '使用此蓝图预览清洗规则'}</Link><Link to={`${base}/blueprint?version=${blueprint.current.version}&node=${kind === 'evaluation' ? 'evaluation' : 'rules'}`}>查看所选蓝图配置</Link><Link to={`${base}/quality?${context}`}>项目质量报告</Link></div>
    </Card> : null}
  </div>
}

export function EvaluationToolPage() {
  const [params] = useSearchParams()
  return params.get('mode') === 'legacy' || params.has('datasetId') ? <LegacyEvaluationTool /> : <ProjectToolPage kind="evaluation" />
}

export function CleaningToolPage() {
  const [params] = useSearchParams()
  return params.get('mode') === 'legacy' || params.has('datasetId') ? <LegacyCleaningTool /> : <ProjectToolPage kind="cleaning" />
}
