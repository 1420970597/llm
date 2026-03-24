import type { FormEvent, ReactNode } from 'react'
import { useEffect, useMemo, useState } from 'react'
import { AnimatePresence, motion } from 'framer-motion'
import { useSearchParams } from 'react-router-dom'
import {
  Activity,
  ArrowRight,
  BellRing,
  Blocks,
  BrainCircuit,
  CheckCircle2,
  ChevronLeft,
  ChevronRight,
  Clock3,
  DatabaseZap,
  FileOutput,
  FolderGit2,
  GitBranch,
  HardDriveDownload,
  Layers3,
  LayoutDashboard,
  Menu,
  Network,
  PanelTop,
  RefreshCw,
  Rocket,
  Save,
  ServerCog,
  ShieldCheck,
  Sparkles,
  SquareKanban,
  Target,
  Waypoints,
  Workflow,
  X,
  type LucideIcon,
} from 'lucide-react'
import {
  type Artifact,
  type Dataset,
  type DatasetGraph,
  type Domain,
  type Provider,
  type Question,
  type ReasoningRecord,
  type RewardRecord,
  type RuntimeStatus,
  type StorageProfile,
  type Strategy,
  userApi,
} from './lib/api'

function statusLabel(status: string) {
  switch (status) {
    case 'draft':
      return '草稿'
    case 'domains_confirmed':
      return '领域已确认'
    case 'questions_generated':
      return '问题已生成'
    case 'reasoning_generated':
      return '推理已生成'
    case 'rewards_generated':
      return '奖励数据已生成'
    default:
      return status
  }
}

function sourceLabel(source: string) {
  switch (source) {
    case 'ai':
      return '模型生成'
    case 'mock':
      return '模拟数据'
    default:
      return source
  }
}

function artifactTypeLabel(type: string) {
  switch (type) {
    case 'jsonl-export':
      return 'JSONL 导出包'
    default:
      return type
  }
}

function formatDateLabel(value?: string) {
  if (!value) return '—'
  return new Intl.DateTimeFormat('zh-CN', {
    month: 'numeric',
    day: 'numeric',
    hour: '2-digit',
    minute: '2-digit',
    hour12: false,
  }).format(new Date(value))
}

type UserPageKey = 'dashboard' | 'planner' | 'domains' | 'questions' | 'reasoning' | 'rewards' | 'exports'

type PageDefinition = {
  key: UserPageKey
  label: string
  caption: string
  icon: LucideIcon
  group: string
}

const pageDefinitions: PageDefinition[] = [
  { key: 'dashboard', label: '总览', caption: '查看整体运行态与最近任务', icon: LayoutDashboard, group: '工作台' },
  { key: 'planner', label: '计划编排', caption: '配置规模、策略、模型与存储', icon: SquareKanban, group: '工作台' },
  { key: 'domains', label: '领域图谱', caption: '生成并人工修订领域结构', icon: GitBranch, group: '生成链路' },
  { key: 'questions', label: '问题生成', caption: '为已确认领域生成问题集', icon: Layers3, group: '生成链路' },
  { key: 'reasoning', label: '推理生成', caption: '生成长思考过程与答案摘要', icon: BrainCircuit, group: '生成链路' },
  { key: 'rewards', label: '奖励数据', caption: '生成强化学习奖励评估记录', icon: ShieldCheck, group: '生成链路' },
  { key: 'exports', label: '导出交付', caption: '查看工件与运行态收口', icon: HardDriveDownload, group: '生成链路' },
]

const pageKeys = new Set(pageDefinitions.map((item) => item.key))

function GraphPreview({ rootKeyword, domains }: { rootKeyword: string; domains: Domain[] }) {
  const visible = domains.slice(0, 18)
  const width = 820
  const height = 420
  const centerX = width / 2
  const centerY = height / 2
  const radius = 158

  const positioned = visible.map((domain, index) => {
    const angle = (Math.PI * 2 * index) / Math.max(visible.length, 1)
    return {
      ...domain,
      x: centerX + Math.cos(angle) * radius,
      y: centerY + Math.sin(angle) * radius,
    }
  })

  return (
    <div className="graph-surface">
      <svg viewBox={`0 0 ${width} ${height}`} className="graph-svg" role="img" aria-label="领域图谱预览">
        <defs>
          <radialGradient id="graphGlow" cx="50%" cy="50%" r="50%">
            <stop offset="0%" stopColor="rgba(37, 99, 235, 0.24)" />
            <stop offset="100%" stopColor="rgba(37, 99, 235, 0)" />
          </radialGradient>
        </defs>
        <circle cx={centerX} cy={centerY} r="176" fill="url(#graphGlow)" />
        {positioned.map((node) => (
          <line
            key={`line-${node.id}`}
            x1={centerX}
            y1={centerY}
            x2={node.x}
            y2={node.y}
            stroke="rgba(59, 130, 246, 0.18)"
            strokeWidth="1.2"
          />
        ))}
        <circle cx={centerX} cy={centerY} r="64" fill="#ffffff" stroke="rgba(37, 99, 235, 0.4)" strokeWidth="2" />
        <text x={centerX} y={centerY - 4} textAnchor="middle" dominantBaseline="middle" className="graph-root-text">
          {rootKeyword}
        </text>
        <text x={centerX} y={centerY + 18} textAnchor="middle" dominantBaseline="middle" className="graph-root-subtext">
          核心主题
        </text>
        {positioned.map((node) => (
          <g key={node.id}>
            <circle cx={node.x} cy={node.y} r="34" fill="#ffffff" stroke="rgba(59, 130, 246, 0.22)" strokeWidth="1.5" />
            <text x={node.x} y={node.y - 4} textAnchor="middle" dominantBaseline="middle" className="graph-node-text">
              {node.name.slice(0, 8)}
            </text>
            <text x={node.x} y={node.y + 14} textAnchor="middle" dominantBaseline="middle" className="graph-node-subtext">
              {sourceLabel(node.source)}
            </text>
          </g>
        ))}
      </svg>
      {domains.length > visible.length ? <p className="graph-caption">当前图谱仅展示前 {visible.length} 个节点，实际已生成 {domains.length} 个领域。</p> : null}
    </div>
  )
}

function PageHeader({ eyebrow, title, description, actions }: { eyebrow: string; title: string; description: string; actions?: ReactNode }) {
  return (
    <div className="page-header">
      <div>
        <span className="eyebrow">{eyebrow}</span>
        <h1>{title}</h1>
        <p>{description}</p>
      </div>
      {actions ? <div className="page-header-actions">{actions}</div> : null}
    </div>
  )
}

function SectionHeader({ eyebrow, title, description }: { eyebrow: string; title: string; description?: string }) {
  return (
    <div className="section-header">
      <span className="eyebrow">{eyebrow}</span>
      <h2>{title}</h2>
      {description ? <p>{description}</p> : null}
    </div>
  )
}

function EmptyState({ title, description }: { title: string; description: string }) {
  return (
    <div className="empty-state">
      <div className="empty-state-icon">
        <Workflow size={18} strokeWidth={1.9} />
      </div>
      <strong>{title}</strong>
      <p>{description}</p>
    </div>
  )
}

function MetricCard({ icon: Icon, label, value, helper }: { icon: LucideIcon; label: string; value: string | number; helper: string }) {
  return (
    <article className="metric-card">
      <div className="metric-card-head">
        <span>{label}</span>
        <div className="metric-card-icon"><Icon size={16} strokeWidth={1.9} /></div>
      </div>
      <strong>{value}</strong>
      <p>{helper}</p>
    </article>
  )
}

function WorkflowStage({ icon: Icon, title, description, summary, tone }: { icon: LucideIcon; title: string; description: string; summary: string; tone: 'done' | 'active' | 'pending' }) {
  return (
    <article className={`workflow-stage ${tone}`}>
      <div className="workflow-stage-icon"><Icon size={18} strokeWidth={1.9} /></div>
      <div>
        <div className="workflow-stage-head">
          <h3>{title}</h3>
          <span>{tone === 'done' ? '已完成' : tone === 'active' ? '进行中' : '待执行'}</span>
        </div>
        <p>{description}</p>
        <strong>{summary}</strong>
      </div>
    </article>
  )
}

function ToneBadge({ tone = 'default', children }: { tone?: 'default' | 'accent' | 'success' | 'warning'; children: ReactNode }) {
  return <span className={`tone-badge ${tone}`}>{children}</span>
}

function Panel({ className, children }: { className?: string; children: ReactNode }) {
  return <section className={`panel ${className ?? ''}`.trim()}>{children}</section>
}

function SpinnerIcon({ spinning, icon: Icon }: { spinning: boolean; icon: LucideIcon }) {
  return <Icon size={16} strokeWidth={1.9} className={spinning ? 'is-spinning' : undefined} />
}

function listPreview<T>({
  items,
  render,
  emptyTitle,
  emptyDescription,
}: {
  items: T[]
  render: (item: T) => ReactNode
  emptyTitle: string
  emptyDescription: string
}) {
  if (items.length === 0) {
    return <EmptyState title={emptyTitle} description={emptyDescription} />
  }

  return <div className="record-list">{items.map(render)}</div>
}

export default function App() {
  const [searchParams, setSearchParams] = useSearchParams()
  const [authenticated, setAuthenticated] = useState(false)
  const [sidebarCollapsed, setSidebarCollapsed] = useState(false)
  const [mobileNavOpen, setMobileNavOpen] = useState(false)
  const [strategies, setStrategies] = useState<Strategy[]>([])
  const [providers, setProviders] = useState<Provider[]>([])
  const [storageProfiles, setStorageProfiles] = useState<StorageProfile[]>([])
  const [datasets, setDatasets] = useState<Dataset[]>([])
  const [graph, setGraph] = useState<DatasetGraph | null>(null)
  const [questions, setQuestions] = useState<Question[]>([])
  const [reasoning, setReasoning] = useState<ReasoningRecord[]>([])
  const [rewards, setRewards] = useState<RewardRecord[]>([])
  const [artifacts, setArtifacts] = useState<Artifact[]>([])
  const [runtime, setRuntime] = useState<RuntimeStatus | null>(null)
  const [message, setMessage] = useState('')
  const [estimate, setEstimate] = useState<Dataset['estimate'] | null>(null)
  const [loading, setLoading] = useState(false)
  const [formState, setFormState] = useState({
    name: '',
    rootKeyword: '军事',
    targetSize: 12000,
    strategyId: 0,
    providerId: 0,
    storageProfileId: 0,
  })

  const pageParam = searchParams.get('page')
  const currentPage: UserPageKey = pageKeys.has(pageParam as UserPageKey) ? (pageParam as UserPageKey) : 'dashboard'

  const adminHref = typeof window !== 'undefined'
    ? `${window.location.protocol}//${window.location.hostname}:3211`
    : '/'

  const setPage = (page: UserPageKey) => {
    const next = new URLSearchParams(searchParams)
    next.set('page', page)
    setSearchParams(next)
    setMobileNavOpen(false)
  }

  const activeDataset = graph?.dataset ?? null

  const loadBootstrap = async (nextMessage?: string) => {
    setLoading(true)
    try {
      const [strategyData, providerData, storageData, datasetData, runtimeData] = await Promise.all([
        userApi.listStrategies(),
        userApi.listProviders(),
        userApi.listStorageProfiles(),
        userApi.listDatasets(),
        userApi.runtimeStatus(),
      ])

      setStrategies(strategyData)
      setProviders(providerData)
      setStorageProfiles(storageData)
      setDatasets(datasetData)
      setRuntime(runtimeData)
      setFormState((current) => ({
        ...current,
        strategyId: current.strategyId || strategyData[0]?.id || 0,
        providerId: current.providerId || providerData[0]?.id || 0,
        storageProfileId: current.storageProfileId || storageData[0]?.id || 0,
      }))

      const preferredDatasetId = datasetData.some((dataset) => dataset.id === graph?.dataset.id)
        ? graph?.dataset.id
        : datasetData[0]?.id

      if (preferredDatasetId) {
        const [nextGraph, nextQuestions, nextReasoning, nextRewards, nextArtifacts] = await Promise.all([
          userApi.getDataset(preferredDatasetId),
          userApi.listQuestions(preferredDatasetId),
          userApi.listReasoning(preferredDatasetId),
          userApi.listRewards(preferredDatasetId),
          userApi.listArtifacts(preferredDatasetId),
        ])
        setGraph(nextGraph)
        setQuestions(nextQuestions)
        setReasoning(nextReasoning)
        setRewards(nextRewards)
        setArtifacts(nextArtifacts)
      } else {
        setGraph(null)
        setQuestions([])
        setReasoning([])
        setRewards([])
        setArtifacts([])
      }

      if (nextMessage) {
        setMessage(preferredDatasetId ? `${nextMessage} 已同步最近的数据集。` : nextMessage)
      }
    } catch (error) {
      setMessage((error as Error).message)
    } finally {
      setLoading(false)
    }
  }

  const loadDatasetWorkspace = async (datasetId: number, nextMessage?: string) => {
    setLoading(true)
    try {
      const [nextGraph, nextQuestions, nextReasoning, nextRewards, nextArtifacts, nextRuntime, nextDatasets] = await Promise.all([
        userApi.getDataset(datasetId),
        userApi.listQuestions(datasetId),
        userApi.listReasoning(datasetId),
        userApi.listRewards(datasetId),
        userApi.listArtifacts(datasetId),
        userApi.runtimeStatus(),
        userApi.listDatasets(),
      ])
      setGraph(nextGraph)
      setQuestions(nextQuestions)
      setReasoning(nextReasoning)
      setRewards(nextRewards)
      setArtifacts(nextArtifacts)
      setRuntime(nextRuntime)
      setDatasets(nextDatasets)
      if (nextMessage) {
        setMessage(nextMessage)
      }
    } catch (error) {
      setMessage((error as Error).message)
    } finally {
      setLoading(false)
    }
  }

  useEffect(() => {
    if (!authenticated) return
    void loadBootstrap('控制台基础数据已加载，可以开始编排任务。')
  }, [authenticated])

  useEffect(() => {
    if (!authenticated) return
    if (!pageParam || !pageKeys.has(pageParam as UserPageKey)) {
      setPage('dashboard')
    }
  }, [authenticated, pageParam])

  useEffect(() => {
    if (typeof window !== 'undefined') {
      window.scrollTo({ top: 0, behavior: 'smooth' })
    }
  }, [currentPage])

  const plannerCards = useMemo(() => {
    if (!estimate) return []
    return [
      { icon: Network, label: '领域数', value: estimate.domainCount, helper: '建议拆分出的一级领域数量' },
      { icon: Layers3, label: '每领域问题数', value: estimate.questionsPerDomain, helper: '单个领域建议生成的问题数量' },
      { icon: SquareKanban, label: '预计问题数', value: estimate.estimatedQuestions, helper: '进入问题队列的总问题体量' },
      { icon: Sparkles, label: '预计样本数', value: estimate.estimatedSamples, helper: '最终产出的训练样本规模' },
    ]
  }, [estimate])

  const runtimeCards = useMemo(() => ([
    { icon: DatabaseZap, label: '数据集', value: runtime?.datasetCount ?? 0, helper: '当前在管任务总数' },
    { icon: Layers3, label: '问题', value: runtime?.questionCount ?? 0, helper: '已进入存储的问题条目' },
    { icon: BrainCircuit, label: '奖励记录', value: runtime?.rewardCount ?? 0, helper: '完成奖励评估的样本数量' },
    { icon: Activity, label: '队列深度', value: runtime?.queueDepth ?? 0, helper: '后台异步任务待执行数' },
  ]), [runtime])

  const workflowStages = useMemo(() => ([
    {
      icon: Target,
      title: '计划估算',
      description: '先锁定目标样本规模，再反推领域数与问题配额。',
      summary: estimate ? `预计 ${estimate.estimatedSamples} 个样本` : authenticated ? '等待运行估算' : '等待登录控制台',
      tone: estimate ? 'done' : authenticated ? 'active' : 'pending',
    },
    {
      icon: GitBranch,
      title: '领域图谱',
      description: '生成领域节点后先命名治理，再确认图谱。',
      summary: graph ? `${graph.domains.length} 个领域节点` : '等待创建数据集',
      tone: activeDataset?.status && activeDataset.status !== 'draft' ? 'done' : graph ? 'active' : 'pending',
    },
    {
      icon: BrainCircuit,
      title: '问题与推理',
      description: '按领域生成问题，再异步补齐长思维链与答案。',
      summary: reasoning.length > 0 ? `${reasoning.length} 条推理记录` : questions.length > 0 ? `${questions.length} 个问题待扩展` : '等待领域确认',
      tone: reasoning.length > 0 ? 'done' : questions.length > 0 ? 'active' : 'pending',
    },
    {
      icon: FileOutput,
      title: '奖励与导出',
      description: '完成奖励评估后导出 JSONL 工件并回收运行态信号。',
      summary: artifacts.length > 0 ? `${artifacts.length} 个导出工件` : rewards.length > 0 ? `${rewards.length} 条奖励记录` : '等待推理完成',
      tone: artifacts.length > 0 ? 'done' : rewards.length > 0 ? 'active' : 'pending',
    },
  ] satisfies Array<{ icon: LucideIcon; title: string; description: string; summary: string; tone: 'done' | 'active' | 'pending' }>), [activeDataset?.status, artifacts.length, authenticated, estimate, graph, questions.length, reasoning.length, rewards.length])

  const selectedStrategy = strategies.find((strategy) => strategy.id === Number(formState.strategyId))
  const selectedProvider = providers.find((provider) => provider.id === Number(formState.providerId))
  const selectedStorage = storageProfiles.find((profile) => profile.id === Number(formState.storageProfileId))

  const currentPageDefinition = pageDefinitions.find((item) => item.key === currentPage) ?? pageDefinitions[0]
  const topNavItems = pageDefinitions.filter((item) => ['dashboard', 'planner', 'domains', 'exports'].includes(item.key))
  const groupedPages = pageDefinitions.reduce<Record<string, PageDefinition[]>>((acc, page) => {
    acc[page.group] = [...(acc[page.group] ?? []), page]
    return acc
  }, {})
  const recentDatasets = datasets.slice(0, 4)
  const quickFacts = [
    `主题：${formState.rootKeyword}`,
    `策略：${selectedStrategy?.name ?? '待同步'}`,
    `模型：${selectedProvider?.model ?? '待同步'}`,
    `存储：${selectedStorage?.name ?? '待同步'}`,
  ]

  const submitLogin = (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault()
    setAuthenticated(true)
    setPage('dashboard')
    setMessage('控制台已解锁，请先进入“计划编排”页面估算规模。')
  }

  const estimatePlan = async () => {
    setLoading(true)
    try {
      const nextEstimate = await userApi.estimatePlan({
        rootKeyword: formState.rootKeyword,
        targetSize: Number(formState.targetSize),
        strategyId: Number(formState.strategyId),
      })
      setEstimate(nextEstimate)
      setMessage('规模估算已刷新，可以继续创建数据集。')
    } catch (error) {
      setMessage((error as Error).message)
    } finally {
      setLoading(false)
    }
  }

  const createDataset = async () => {
    if (!estimate) {
      setMessage('请先完成计划估算，再创建数据集。')
      return
    }
    setLoading(true)
    try {
      const created = await userApi.createDataset({
        name: formState.name || `${formState.rootKeyword} 数据集`,
        rootKeyword: formState.rootKeyword,
        targetSize: Number(formState.targetSize),
        strategyId: Number(formState.strategyId),
        providerId: Number(formState.providerId),
        storageProfileId: Number(formState.storageProfileId),
        status: 'draft',
        estimate,
      })
      await loadDatasetWorkspace(created.id, '数据集已创建，接下来可以生成并治理领域图谱。')
      setPage('domains')
    } catch (error) {
      setMessage((error as Error).message)
      setLoading(false)
    }
  }

  const generateDomains = async () => {
    if (!graph) {
      setMessage('请先创建或选中一个数据集，再生成领域。')
      return
    }
    setLoading(true)
    try {
      const nextGraph = await userApi.generateDomains(graph.dataset.id)
      setGraph(nextGraph)
      setMessage(`已生成 ${nextGraph.domains.length} 个领域，建议先进行命名审核。`)
    } catch (error) {
      setMessage((error as Error).message)
    } finally {
      setLoading(false)
    }
  }

  const renameDomain = (domainId: number, name: string) => {
    setGraph((current) => current ? {
      ...current,
      domains: current.domains.map((domain) => domain.id === domainId ? { ...domain, name } : domain),
    } : current)
  }

  const saveGraph = async () => {
    if (!graph) return
    setLoading(true)
    try {
      await userApi.updateGraph(graph.dataset.id, graph.domains)
      setMessage('图谱命名修改已保存。')
    } catch (error) {
      setMessage((error as Error).message)
    } finally {
      setLoading(false)
    }
  }

  const confirmDomains = async () => {
    if (!graph) return
    setLoading(true)
    try {
      await userApi.confirmDomains(graph.dataset.id)
      await loadDatasetWorkspace(graph.dataset.id, '领域已确认，数据集已进入问题生成阶段。')
      setPage('questions')
    } catch (error) {
      setMessage((error as Error).message)
      setLoading(false)
    }
  }

  const generateQuestions = async () => {
    if (!graph) return
    setLoading(true)
    try {
      await userApi.generateQuestions(graph.dataset.id)
      setMessage('问题生成任务已入队，可以稍后刷新查看结果。')
    } catch (error) {
      setMessage((error as Error).message)
    } finally {
      setLoading(false)
    }
  }

  const refreshQuestions = async () => {
    if (!graph) return
    setLoading(true)
    try {
      const [nextQuestions, nextRuntime] = await Promise.all([
        userApi.listQuestions(graph.dataset.id),
        userApi.runtimeStatus(),
      ])
      setQuestions(nextQuestions)
      setRuntime(nextRuntime)
      setMessage('问题预览已刷新。')
    } catch (error) {
      setMessage((error as Error).message)
    } finally {
      setLoading(false)
    }
  }

  const generateReasoning = async () => {
    if (!graph) return
    setLoading(true)
    try {
      await userApi.generateReasoning(graph.dataset.id)
      setMessage('推理生成任务已入队，请稍后刷新查看长文本结果。')
    } catch (error) {
      setMessage((error as Error).message)
    } finally {
      setLoading(false)
    }
  }

  const refreshReasoning = async () => {
    if (!graph) return
    setLoading(true)
    try {
      const [nextReasoning, nextRuntime] = await Promise.all([
        userApi.listReasoning(graph.dataset.id),
        userApi.runtimeStatus(),
      ])
      setReasoning(nextReasoning)
      setRuntime(nextRuntime)
      setMessage('推理预览已刷新。')
    } catch (error) {
      setMessage((error as Error).message)
    } finally {
      setLoading(false)
    }
  }

  const generateRewards = async () => {
    if (!graph) return
    setLoading(true)
    try {
      await userApi.generateRewards(graph.dataset.id)
      setMessage('奖励数据生成任务已入队，可以稍后刷新查看评分结果。')
    } catch (error) {
      setMessage((error as Error).message)
    } finally {
      setLoading(false)
    }
  }

  const refreshRewards = async () => {
    if (!graph) return
    setLoading(true)
    try {
      const [nextRewards, nextRuntime] = await Promise.all([
        userApi.listRewards(graph.dataset.id),
        userApi.runtimeStatus(),
      ])
      setRewards(nextRewards)
      setRuntime(nextRuntime)
      setMessage('奖励数据预览已刷新。')
    } catch (error) {
      setMessage((error as Error).message)
    } finally {
      setLoading(false)
    }
  }

  const generateExport = async () => {
    if (!graph) return
    setLoading(true)
    try {
      await userApi.generateExport(graph.dataset.id)
      setMessage('导出任务已入队，请稍后刷新查看打包结果。')
    } catch (error) {
      setMessage((error as Error).message)
    } finally {
      setLoading(false)
    }
  }

  const refreshArtifacts = async () => {
    if (!graph) return
    setLoading(true)
    try {
      const [nextArtifacts, nextRuntime] = await Promise.all([
        userApi.listArtifacts(graph.dataset.id),
        userApi.runtimeStatus(),
      ])
      setArtifacts(nextArtifacts)
      setRuntime(nextRuntime)
      setMessage('导出工件预览已刷新。')
    } catch (error) {
      setMessage((error as Error).message)
    } finally {
      setLoading(false)
    }
  }

  const renderDashboardPage = () => (
    <>
      <PageHeader
        eyebrow="数据工厂总览"
        title="统一观察计划、治理、生成与导出"
        description="按 /root/new-api 的控制台结构重新拆分页面：顶部导航、左侧侧栏、右侧分页工作区，避免把全部操作堆在同一屏。"
        actions={(
          <>
            <button type="button" className="secondary-button" onClick={() => setPage('planner')}>
              <SquareKanban size={16} strokeWidth={1.9} /> 进入计划编排
            </button>
            <button type="button" className="primary-button" onClick={() => void loadBootstrap('控制台基础数据已刷新。')} disabled={loading}>
              <SpinnerIcon spinning={loading} icon={RefreshCw} /> 刷新总览
            </button>
          </>
        )}
      />

      <div className="page-grid page-grid-hero">
        <Panel className="hero-panel hero-panel-wide">
          <ToneBadge tone="accent">用户工作台</ToneBadge>
          <h2>先看运行态，再推进动作，把数据集生产流程拆成清晰的独立页面。</h2>
          <p>
            参考 new-api 的头部、侧栏与工作区布局，把计划估算、领域治理、问题生成、推理生成、奖励生成与导出交付拆成独立入口，减少滚动和跨区跳转成本。
          </p>
          <div className="button-row">
            <button type="button" className="primary-button" onClick={() => setPage('planner')}>
              开始新任务 <ArrowRight size={16} strokeWidth={1.9} />
            </button>
            <button type="button" className="secondary-button" onClick={() => setPage(activeDataset ? 'domains' : 'planner')}>
              {activeDataset ? '继续当前数据集' : '先完成计划估算'}
            </button>
          </div>
          {message ? <div className="notice-banner"><BellRing size={16} strokeWidth={1.9} /> {message}</div> : null}
          <div className="chip-row">
            {quickFacts.map((fact) => (
              <span key={fact} className="info-chip">{fact}</span>
            ))}
          </div>
        </Panel>

        <Panel className="focus-panel">
          <SectionHeader eyebrow="当前焦点" title={activeDataset?.name ?? '尚未选中数据集'} description={activeDataset ? `${activeDataset.rootKeyword} · ${statusLabel(activeDataset.status)}` : '先进入计划编排页创建首个数据集。'} />
          {activeDataset ? (
            <div className="focus-summary">
              <div className="focus-row">
                <span>预计样本</span>
                <strong>{activeDataset.estimate.estimatedSamples}</strong>
              </div>
              <div className="focus-row">
                <span>创建时间</span>
                <strong>{formatDateLabel(activeDataset.createdAt)}</strong>
              </div>
              <div className="focus-row">
                <span>更新时间</span>
                <strong>{formatDateLabel(activeDataset.updatedAt)}</strong>
              </div>
              <div className="focus-row">
                <span>当前状态</span>
                <strong>{statusLabel(activeDataset.status)}</strong>
              </div>
            </div>
          ) : (
            <EmptyState title="还没有活动数据集" description="完成计划估算并创建数据集后，这里会显示当前任务上下文。" />
          )}
        </Panel>
      </div>

      <div className="metric-grid four-up">
        {runtimeCards.map((card) => (
          <MetricCard key={card.label} {...card} />
        ))}
      </div>

      <div className="page-grid page-grid-2-1">
        <Panel>
          <SectionHeader eyebrow="阶段进度" title="流程总览" description="每一个阶段都以独立页推进，但总览页会持续显示当前链路状态。" />
          <div className="workflow-stage-list">
            {workflowStages.map((stage) => (
              <WorkflowStage key={stage.title} {...stage} />
            ))}
          </div>
        </Panel>

        <Panel>
          <SectionHeader eyebrow="操作纪律" title="执行检查表" description="建议按顺序推进，避免在前置结果未落库时提前触发后续阶段。" />
          <div className="checklist-grid">
            {[
              '先估算规模，再创建数据集。',
              '图谱确认前只做命名治理，不提前跑问题队列。',
              '推理与奖励阶段都建议在上一阶段刷新成功后再继续。',
              '导出前先检查队列深度与工件数量。',
            ].map((item) => (
              <div key={item} className="checklist-item">
                <CheckCircle2 size={16} strokeWidth={1.9} />
                <span>{item}</span>
              </div>
            ))}
          </div>
        </Panel>
      </div>

      <Panel>
        <SectionHeader eyebrow="最近的数据集" title="任务切换" description="保留 new-api 式的工作台信息密度，在总览页直接切换最近任务。" />
        {datasets.length > 0 ? (
          <div className="dataset-grid">
            {datasets.map((dataset) => {
              const active = dataset.id === activeDataset?.id
              return (
                <button
                  key={dataset.id}
                  type="button"
                  className={`dataset-card ${active ? 'active' : ''}`}
                  onClick={() => void loadDatasetWorkspace(dataset.id, `已切换到数据集「${dataset.name}」。`)}
                >
                  <div className="dataset-card-top">
                    <ToneBadge tone={active ? 'accent' : 'default'}>{statusLabel(dataset.status)}</ToneBadge>
                    {active ? <span className="dataset-current-tag">当前任务</span> : null}
                  </div>
                  <h3>{dataset.name}</h3>
                  <p>{dataset.rootKeyword} · 预计 {dataset.estimate.estimatedSamples} 个样本</p>
                  <div className="dataset-card-meta">
                    <span><FolderGit2 size={14} strokeWidth={1.9} /> 数据集 #{dataset.id}</span>
                    <span>更新于 {formatDateLabel(dataset.updatedAt)}</span>
                  </div>
                </button>
              )
            })}
          </div>
        ) : (
          <EmptyState title="还没有可切换的数据集" description="先进入计划编排页创建数据集，任务卡片会自动出现在这里。" />
        )}
      </Panel>
    </>
  )

  const renderPlannerPage = () => (
    <>
      <PageHeader
        eyebrow="计划编排"
        title="配置数据集规模、策略、模型与存储"
        description="把 new-api 风格的工作区结构映射到本项目：左侧填写配置，右侧查看就绪度与估算结果。"
        actions={(
          <>
            <button type="button" className="secondary-button" onClick={() => void loadBootstrap('控制台基础数据已刷新。')} disabled={loading}>
              <SpinnerIcon spinning={loading} icon={RefreshCw} /> 同步资源
            </button>
            <button type="button" className="primary-button" onClick={() => void estimatePlan()} disabled={loading}>
              <Target size={16} strokeWidth={1.9} /> 更新估算
            </button>
          </>
        )}
      />

      <div className="page-grid page-grid-2-1 align-start">
        <Panel>
          <SectionHeader eyebrow="任务入口" title="创建一个新的训练数据集" description="先确定根关键词与目标规模，再选择策略、模型提供方和对象存储。" />
          <div className="form-grid two-columns">
            <label>
              <span>数据集名称</span>
              <input value={formState.name} onChange={(event) => setFormState({ ...formState, name: event.target.value })} placeholder="军事长链思考训练集" />
            </label>
            <label>
              <span>根关键词</span>
              <input value={formState.rootKeyword} onChange={(event) => setFormState({ ...formState, rootKeyword: event.target.value })} placeholder="军事" />
            </label>
            <label>
              <span>目标规模</span>
              <input type="number" value={formState.targetSize} onChange={(event) => setFormState({ ...formState, targetSize: Number(event.target.value) })} />
            </label>
            <label>
              <span>生成策略</span>
              <select value={formState.strategyId} onChange={(event) => setFormState({ ...formState, strategyId: Number(event.target.value) })}>
                {strategies.map((strategy) => <option key={strategy.id} value={strategy.id}>{strategy.name}</option>)}
              </select>
            </label>
            <label>
              <span>模型提供方</span>
              <select value={formState.providerId} onChange={(event) => setFormState({ ...formState, providerId: Number(event.target.value) })}>
                {providers.map((provider) => <option key={provider.id} value={provider.id}>{provider.name}</option>)}
              </select>
            </label>
            <label>
              <span>存储配置</span>
              <select value={formState.storageProfileId} onChange={(event) => setFormState({ ...formState, storageProfileId: Number(event.target.value) })}>
                {storageProfiles.map((profile) => <option key={profile.id} value={profile.id}>{profile.name}</option>)}
              </select>
            </label>
          </div>
          <div className="button-row">
            <button type="button" className="secondary-button" onClick={() => void estimatePlan()} disabled={loading}>
              <Target size={16} strokeWidth={1.9} /> 先做估算
            </button>
            <button type="button" className="primary-button" onClick={() => void createDataset()} disabled={loading}>
              <Rocket size={16} strokeWidth={1.9} /> 创建数据集
            </button>
          </div>
        </Panel>

        <div className="stack-column">
          <Panel>
            <SectionHeader eyebrow="资源就绪度" title="当前资源选择" description="在创建数据集前，先检查策略、模型与存储是否齐备。" />
            <div className="readiness-list">
              <div className="readiness-item">
                <div className="readiness-icon"><SquareKanban size={18} strokeWidth={1.9} /></div>
                <div>
                  <strong>{selectedStrategy?.name ?? '尚未选择策略'}</strong>
                  <p>{selectedStrategy?.description ?? '请先同步策略列表。'}</p>
                </div>
              </div>
              <div className="readiness-item">
                <div className="readiness-icon"><ServerCog size={18} strokeWidth={1.9} /></div>
                <div>
                  <strong>{selectedProvider?.name ?? '尚未选择提供方'}</strong>
                  <p>{selectedProvider ? `${selectedProvider.providerType} · ${selectedProvider.model}` : '请先同步模型提供方。'}</p>
                </div>
              </div>
              <div className="readiness-item">
                <div className="readiness-icon"><DatabaseZap size={18} strokeWidth={1.9} /></div>
                <div>
                  <strong>{selectedStorage?.name ?? '尚未选择存储'}</strong>
                  <p>{selectedStorage ? `${selectedStorage.provider} · ${selectedStorage.bucket}` : '请先同步对象存储配置。'}</p>
                </div>
              </div>
            </div>
          </Panel>

          <Panel>
            <SectionHeader eyebrow="计划估算" title="估算结果" description="估算结果用于决定领域数量、问题规模与最终样本产出。" />
            {plannerCards.length > 0 ? (
              <div className="metric-grid two-up compact-grid">
                {plannerCards.map((card) => <MetricCard key={card.label} {...card} />)}
              </div>
            ) : (
              <EmptyState title="尚未生成估算结果" description="填写策略与目标规模后，先运行一次估算，再创建数据集。" />
            )}
          </Panel>
        </div>
      </div>
    </>
  )

  const renderDomainsPage = () => (
    <>
      <PageHeader
        eyebrow="领域图谱"
        title="生成、审阅并确认领域结构"
        description="图谱页对应参考项目的控制台内容页：主画布展示结构，侧边和下方承担治理动作与清单。"
        actions={(
          <>
            <button type="button" className="secondary-button" onClick={() => void saveGraph()} disabled={loading || !graph}>
              <Save size={16} strokeWidth={1.9} /> 保存图谱
            </button>
            <button type="button" className="primary-button" onClick={() => void generateDomains()} disabled={loading || !activeDataset}>
              <SpinnerIcon spinning={loading} icon={GitBranch} /> 生成领域
            </button>
          </>
        )}
      />

      <div className="page-grid page-grid-2-1 align-start">
        <Panel>
          <SectionHeader eyebrow="图谱主画布" title="领域结构预览" description="中央节点表示根关键词，外环节点表示已生成的一级领域。" />
          {graph ? (
            <>
              <GraphPreview rootKeyword={graph.dataset.rootKeyword} domains={graph.domains} />
              <div className="toolbar-row">
                <ToneBadge tone="accent">{statusLabel(graph.dataset.status)}</ToneBadge>
                <div className="button-row compact-row">
                  <button type="button" className="secondary-button" onClick={() => void saveGraph()} disabled={loading}><Save size={16} strokeWidth={1.9} /> 保存编辑</button>
                  <button type="button" className="primary-button" onClick={() => void confirmDomains()} disabled={loading}>确认领域</button>
                </div>
              </div>
            </>
          ) : (
            <EmptyState title="尚未加载领域图谱" description="先在计划编排页创建数据集，再回来生成领域结构。" />
          )}
        </Panel>

        <Panel>
          <SectionHeader eyebrow="图谱上下文" title={activeDataset?.name ?? '没有活动数据集'} description={activeDataset ? `${activeDataset.rootKeyword} · 预计 ${activeDataset.estimate.estimatedSamples} 个样本` : '请选择或创建一个数据集。'} />
          {activeDataset ? (
            <div className="focus-summary">
              <div className="focus-row"><span>状态</span><strong>{statusLabel(activeDataset.status)}</strong></div>
              <div className="focus-row"><span>领域数</span><strong>{graph?.domains.length ?? 0}</strong></div>
              <div className="focus-row"><span>创建时间</span><strong>{formatDateLabel(activeDataset.createdAt)}</strong></div>
              <div className="focus-row"><span>更新时间</span><strong>{formatDateLabel(activeDataset.updatedAt)}</strong></div>
            </div>
          ) : (
            <EmptyState title="没有可治理的图谱" description="请先创建数据集并生成领域。" />
          )}
        </Panel>
      </div>

      <Panel>
        <SectionHeader eyebrow="人工修订" title="领域命名润色" description="优先审核前 24 个领域，必要时先保存，再继续生成后续问题。" />
        {graph ? (
          <div className="domain-editor-grid">
            {graph.domains.slice(0, 24).map((domain) => (
              <label key={domain.id} className="domain-editor-item">
                <span>{sourceLabel(domain.source)}</span>
                <input value={domain.name} onChange={(event) => renameDomain(domain.id, event.target.value)} />
              </label>
            ))}
          </div>
        ) : (
          <EmptyState title="还没有领域可以编辑" description="点击“生成领域”后，这里会出现可编辑的领域命名列表。" />
        )}
        {graph && graph.domains.length > 24 ? <p className="panel-footnote">当前界面展示前 24 个领域；全部 {graph.domains.length} 个领域都会被完整持久化保存。</p> : null}
      </Panel>
    </>
  )

  const renderQuestionsPage = () => (
    <>
      <PageHeader
        eyebrow="问题生成"
        title="按领域批量生成问题集合"
        description="问题页拆成独立工作区，触发动作、列表预览和当前上下文不再混在一张超长页面中。"
        actions={(
          <>
            <button type="button" className="secondary-button" onClick={() => void refreshQuestions()} disabled={loading || !graph}>
              <SpinnerIcon spinning={loading} icon={RefreshCw} /> 刷新问题
            </button>
            <button type="button" className="primary-button" onClick={() => void generateQuestions()} disabled={loading || !graph}>
              <Layers3 size={16} strokeWidth={1.9} /> 加入问题队列
            </button>
          </>
        )}
      />

      <div className="page-grid page-grid-2-1 align-start">
        <Panel>
          <SectionHeader eyebrow="问题预览" title="已生成问题" description="展示最新生成的问题记录，便于确认领域覆盖是否符合预期。" />
          {listPreview<Question>({
            items: questions.slice(0, 12),
            emptyTitle: '尚未生成问题',
            emptyDescription: '请先确认领域，再将问题生成任务加入后台队列。',
            render: (question) => (
              <article key={question.id} className="record-item">
                <div className="record-item-top">
                  <ToneBadge>{question.domainName}</ToneBadge>
                  <span>{formatDateLabel(question.createdAt)}</span>
                </div>
                <p>{question.content}</p>
                <small>{question.canonicalHash}</small>
              </article>
            ),
          })}
        </Panel>

        <Panel>
          <SectionHeader eyebrow="当前阶段" title="问题生成信号" description="问题数量、领域数量与平台队列深度会在这里集中显示。" />
          <div className="focus-summary">
            <div className="focus-row"><span>活动数据集</span><strong>{activeDataset?.name ?? '未选择'}</strong></div>
            <div className="focus-row"><span>领域数量</span><strong>{graph?.domains.length ?? 0}</strong></div>
            <div className="focus-row"><span>问题数量</span><strong>{questions.length}</strong></div>
            <div className="focus-row"><span>队列深度</span><strong>{runtime?.queueDepth ?? 0}</strong></div>
          </div>
        </Panel>
      </div>
    </>
  )

  const renderReasoningPage = () => (
    <>
      <PageHeader
        eyebrow="推理生成"
        title="异步生成长思维链与答案摘要"
        description="为已经入库的问题生成长推理与答案摘要，结果落对象存储并回写元数据。"
        actions={(
          <>
            <button type="button" className="secondary-button" onClick={() => void refreshReasoning()} disabled={loading || !graph}>
              <SpinnerIcon spinning={loading} icon={RefreshCw} /> 刷新推理
            </button>
            <button type="button" className="primary-button" onClick={() => void generateReasoning()} disabled={loading || !graph || questions.length === 0}>
              <BrainCircuit size={16} strokeWidth={1.9} /> 加入推理队列
            </button>
          </>
        )}
      />

      <div className="page-grid page-grid-2-1 align-start">
        <Panel>
          <SectionHeader eyebrow="推理记录" title="长思维链输出预览" description="展示对象存储键与答案摘要，便于快速确认生成任务是否正常落盘。" />
          {listPreview<ReasoningRecord>({
            items: reasoning.slice(0, 12),
            emptyTitle: '尚未生成推理记录',
            emptyDescription: '先生成问题，再把推理生成任务加入队列。',
            render: (record) => (
              <article key={record.id} className="record-item">
                <div className="record-item-top">
                  <ToneBadge tone="accent">{record.objectKey}</ToneBadge>
                  <span>{formatDateLabel(record.createdAt)}</span>
                </div>
                <p>{record.answerSummary}</p>
                <small>{record.questionText}</small>
              </article>
            ),
          })}
        </Panel>

        <Panel>
          <SectionHeader eyebrow="当前阶段" title="推理生成信号" description="问题记录足够后再触发推理生成，避免前置数据不足。" />
          <div className="focus-summary">
            <div className="focus-row"><span>问题数量</span><strong>{questions.length}</strong></div>
            <div className="focus-row"><span>推理记录</span><strong>{reasoning.length}</strong></div>
            <div className="focus-row"><span>工件数量</span><strong>{artifacts.length}</strong></div>
            <div className="focus-row"><span>队列深度</span><strong>{runtime?.queueDepth ?? 0}</strong></div>
          </div>
        </Panel>
      </div>
    </>
  )

  const renderRewardsPage = () => (
    <>
      <PageHeader
        eyebrow="奖励数据"
        title="生成强化学习奖励评估记录"
        description="奖励页紧跟推理页之后，便于在同一套工作流里完成质量打分与样本评估。"
        actions={(
          <>
            <button type="button" className="secondary-button" onClick={() => void refreshRewards()} disabled={loading || !graph}>
              <SpinnerIcon spinning={loading} icon={RefreshCw} /> 刷新奖励数据
            </button>
            <button type="button" className="primary-button" onClick={() => void generateRewards()} disabled={loading || !graph || reasoning.length === 0}>
              <ShieldCheck size={16} strokeWidth={1.9} /> 加入奖励队列
            </button>
          </>
        )}
      />

      <div className="page-grid page-grid-2-1 align-start">
        <Panel>
          <SectionHeader eyebrow="奖励记录" title="评分结果预览" description="展示对象键、问题文本与分数，便于对高分低分样本做抽检。" />
          {listPreview<RewardRecord>({
            items: rewards.slice(0, 12),
            emptyTitle: '尚未生成奖励记录',
            emptyDescription: '请先完成推理生成，再触发奖励评估。',
            render: (record) => (
              <article key={record.id} className="record-item">
                <div className="record-item-top">
                  <ToneBadge tone="success">评分 {record.score.toFixed(2)}</ToneBadge>
                  <span>{formatDateLabel(record.createdAt)}</span>
                </div>
                <p>{record.questionText}</p>
                <small>{record.objectKey}</small>
              </article>
            ),
          })}
        </Panel>

        <Panel>
          <SectionHeader eyebrow="当前阶段" title="奖励生成信号" description="奖励生成依赖已有的推理记录，并会进一步影响导出阶段。" />
          <div className="focus-summary">
            <div className="focus-row"><span>推理记录</span><strong>{reasoning.length}</strong></div>
            <div className="focus-row"><span>奖励记录</span><strong>{rewards.length}</strong></div>
            <div className="focus-row"><span>导出工件</span><strong>{artifacts.length}</strong></div>
            <div className="focus-row"><span>队列深度</span><strong>{runtime?.queueDepth ?? 0}</strong></div>
          </div>
        </Panel>
      </div>
    </>
  )

  const renderExportsPage = () => (
    <>
      <PageHeader
        eyebrow="导出交付"
        title="查看 JSONL 工件与运行态收口"
        description="最后一个工作页专门承载导出工件和平台运行态，避免与前序生成逻辑混杂。"
        actions={(
          <>
            <button type="button" className="secondary-button" onClick={() => void refreshArtifacts()} disabled={loading || !graph}>
              <SpinnerIcon spinning={loading} icon={RefreshCw} /> 刷新工件
            </button>
            <button type="button" className="primary-button" onClick={() => void generateExport()} disabled={loading || !graph || rewards.length === 0}>
              <FileOutput size={16} strokeWidth={1.9} /> 加入导出队列
            </button>
          </>
        )}
      />

      <div className="metric-grid four-up compact-grid">
        {runtimeCards.map((card) => <MetricCard key={card.label} {...card} />)}
      </div>

      <div className="page-grid page-grid-2-1 align-start">
        <Panel>
          <SectionHeader eyebrow="导出工件" title="已生成产物" description="输出对象键与工件类型，便于交付前确认最终文件是否已经打包。" />
          {listPreview<Artifact>({
            items: artifacts,
            emptyTitle: '尚未生成导出工件',
            emptyDescription: '完成奖励生成后即可触发导出；产物会在这里集中展示。',
            render: (artifact) => (
              <article key={artifact.id} className="record-item">
                <div className="record-item-top">
                  <ToneBadge tone="accent">{artifactTypeLabel(artifact.artifactType)}</ToneBadge>
                  <span>{formatDateLabel(artifact.createdAt)}</span>
                </div>
                <p>{artifact.objectKey}</p>
                <small>{artifact.contentType}</small>
              </article>
            ),
          })}
        </Panel>

        <Panel>
          <SectionHeader eyebrow="交付检查" title="导出前后的收口动作" description="建议在导出页确认奖励记录、工件数量与当前队列状态后再结束任务。" />
          <div className="checklist-grid">
            {[
              `奖励记录：${rewards.length}`,
              `工件数量：${artifacts.length}`,
              `队列深度：${runtime?.queueDepth ?? 0}`,
              `活动数据集：${activeDataset?.name ?? '未选择'}`,
            ].map((item) => (
              <div key={item} className="checklist-item compact-check">
                <Clock3 size={16} strokeWidth={1.9} />
                <span>{item}</span>
              </div>
            ))}
          </div>
        </Panel>
      </div>
    </>
  )

  const renderCurrentPage = () => {
    switch (currentPage) {
      case 'planner':
        return renderPlannerPage()
      case 'domains':
        return renderDomainsPage()
      case 'questions':
        return renderQuestionsPage()
      case 'reasoning':
        return renderReasoningPage()
      case 'rewards':
        return renderRewardsPage()
      case 'exports':
        return renderExportsPage()
      case 'dashboard':
      default:
        return renderDashboardPage()
    }
  }

  if (!authenticated) {
    return (
      <div className="landing-shell">
        <header className="landing-header">
          <div className="brand-lockup">
            <div className="brand-badge"><Sparkles size={18} strokeWidth={1.9} /></div>
            <div>
              <strong>LLM 数据工厂</strong>
              <p>企业级长链思考训练数据控制台</p>
            </div>
          </div>
          <nav className="landing-nav">
            <a href="#capabilities">能力</a>
            <a href="#workflow">流程</a>
            <a href="#login-panel">进入控制台</a>
          </nav>
          <a className="secondary-button link-button" href={adminHref}>管理员后台</a>
        </header>

        <main className="landing-main">
          <section className="landing-hero-block">
            <div className="landing-copy">
              <ToneBadge tone="accent">LLM Data Factory</ToneBadge>
              <h1>把关键词拆解成领域、问题、长思维链与奖励数据的企业级数据工厂。</h1>
              <p>
                用户输入目标数据集规模与领域关键词，系统自动估算领域数、异步生成问题与长思维链答案，再继续生成强化学习奖励数据，并把大文本与工件落在 S3 / MinIO / PostgreSQL / Redis 体系里。
              </p>
              <div className="button-row">
                <a className="primary-button link-button" href="#login-panel">进入用户控制台</a>
                <a className="secondary-button link-button" href={adminHref}>配置管理后台</a>
              </div>
              <div className="chip-row">
                {['领域图谱治理', '异步问题生成', '长链推理落盘', '奖励评估', 'JSONL 导出交付'].map((item) => (
                  <span key={item} className="info-chip">{item}</span>
                ))}
              </div>
            </div>

            <div className="landing-visual">
              <Panel>
                <SectionHeader eyebrow="作业流程" title="从计划到交付的七段式页面结构" description="参照 /root/new-api 的页面骨架重建为明确的多页控制台。" />
                <div className="workflow-stage-list compact-workflow">
                  {pageDefinitions.map((page, index) => (
                    <WorkflowStage
                      key={page.key}
                      icon={page.icon}
                      title={`${index + 1}. ${page.label}`}
                      description={page.caption}
                      summary={page.group}
                      tone={index === 0 ? 'active' : 'pending'}
                    />
                  ))}
                </div>
              </Panel>
            </div>
          </section>

          <section className="landing-grid" id="capabilities">
            {[
              { icon: Target, title: '目标规模反推', description: '根据目标样本量自动估算领域数与每领域问题数。' },
              { icon: GitBranch, title: '图谱治理', description: '生成领域图谱并支持人工命名修订与确认。' },
              { icon: BrainCircuit, title: '长链推理', description: '异步生成问题对应的长思维链与答案摘要。' },
              { icon: FileOutput, title: '奖励与导出', description: '生成奖励评估数据并打包 JSONL 工件用于训练。' },
            ].map((item) => (
              <Panel key={item.title} className="feature-panel" >
                <div className="feature-icon"><item.icon size={18} strokeWidth={1.9} /></div>
                <h2>{item.title}</h2>
                <p>{item.description}</p>
              </Panel>
            ))}
          </section>

          <section className="landing-grid landing-grid-login" id="workflow">
            <Panel>
              <SectionHeader eyebrow="用户流程" title="用户侧工作流" description="用户侧完全围绕数据集生成生命周期展开。" />
              <div className="checklist-grid">
                {[
                  '输入目标规模与根关键词',
                  '估算领域数量与问题规模',
                  '创建数据集并生成领域图谱',
                  '确认领域后生成问题 / 推理 / 奖励 / 导出',
                ].map((item) => (
                  <div key={item} className="checklist-item">
                    <CheckCircle2 size={16} strokeWidth={1.9} />
                    <span>{item}</span>
                  </div>
                ))}
              </div>
            </Panel>

            <Panel className="login-panel" >
              <SectionHeader eyebrow="控制台入口" title="登录后进入分页工作区" description="用户端与管理端都采用顶部导航 + 左侧侧栏 + 右侧工作区的布局。" />
              <form id="login-panel" className="login-form" onSubmit={submitLogin}>
                <label>
                  <span>运营邮箱</span>
                  <input type="email" placeholder="operator@company.com" required />
                </label>
                <label>
                  <span>访问密钥</span>
                  <input type="password" placeholder="请输入控制台访问密钥" required />
                </label>
                <button type="submit" className="primary-button full-width">
                  进入用户控制台 <ArrowRight size={16} strokeWidth={1.9} />
                </button>
              </form>
            </Panel>
          </section>
        </main>
      </div>
    )
  }

  return (
    <div className={`console-shell ${sidebarCollapsed ? 'sidebar-collapsed' : ''} ${mobileNavOpen ? 'mobile-nav-open' : ''}`}>
      <header className="console-header">
        <div className="console-header-inner">
          <div className="header-leading">
            <button type="button" className="icon-button mobile-only" onClick={() => setMobileNavOpen((current) => !current)}>
              {mobileNavOpen ? <X size={18} strokeWidth={1.9} /> : <Menu size={18} strokeWidth={1.9} />}
            </button>
            <div className="brand-lockup">
              <div className="brand-badge"><Sparkles size={18} strokeWidth={1.9} /></div>
              <div>
                <strong>LLM 数据工厂</strong>
                <p>{currentPageDefinition.label} / {currentPageDefinition.caption}</p>
              </div>
            </div>
          </div>

          <nav className="header-nav desktop-only">
            {topNavItems.map((item) => (
              <button key={item.key} type="button" className={`header-nav-item ${currentPage === item.key ? 'active' : ''}`} onClick={() => setPage(item.key)}>
                <item.icon size={15} strokeWidth={1.9} />
                <span>{item.label}</span>
              </button>
            ))}
          </nav>

          <div className="header-actions">
            <button type="button" className="icon-button desktop-only" onClick={() => setSidebarCollapsed((current) => !current)}>
              {sidebarCollapsed ? <ChevronRight size={18} strokeWidth={1.9} /> : <ChevronLeft size={18} strokeWidth={1.9} />}
            </button>
            <button type="button" className="icon-button" onClick={() => void loadBootstrap('控制台基础数据已刷新。')} disabled={loading}>
              <SpinnerIcon spinning={loading} icon={RefreshCw} />
            </button>
            <a className="header-link" href={adminHref}>管理后台</a>
          </div>
        </div>
      </header>

      <aside className="console-sidebar">
        <div className="sidebar-content">
          <Panel className="sidebar-panel">
            <SectionHeader eyebrow="工作区导航" title="用户控制台" description="保持和参考项目一致的头部 + 侧栏 + 工作区结构。" />
          </Panel>

          {Object.entries(groupedPages).map(([group, pages]) => (
            <div key={group} className="sidebar-group">
              <span className="sidebar-group-label">{group}</span>
              <div className="sidebar-nav-list">
                {pages.map((item) => (
                  <button key={item.key} type="button" className={`sidebar-nav-item ${currentPage === item.key ? 'active' : ''}`} onClick={() => setPage(item.key)}>
                    <div className="sidebar-nav-icon"><item.icon size={18} strokeWidth={1.9} /></div>
                    <div className="sidebar-nav-copy">
                      <strong>{item.label}</strong>
                      <span>{item.caption}</span>
                    </div>
                  </button>
                ))}
              </div>
            </div>
          ))}

          <Panel className="sidebar-panel">
            <SectionHeader eyebrow="运行摘要" title="平台信号" />
            <div className="focus-summary compact-summary">
              <div className="focus-row"><span>数据集</span><strong>{runtime?.datasetCount ?? 0}</strong></div>
              <div className="focus-row"><span>问题</span><strong>{runtime?.questionCount ?? 0}</strong></div>
              <div className="focus-row"><span>奖励</span><strong>{runtime?.rewardCount ?? 0}</strong></div>
              <div className="focus-row"><span>队列</span><strong>{runtime?.queueDepth ?? 0}</strong></div>
            </div>
          </Panel>

          <Panel className="sidebar-panel">
            <SectionHeader eyebrow="最近任务" title="快速切换" />
            {recentDatasets.length > 0 ? (
              <div className="sidebar-dataset-list">
                {recentDatasets.map((dataset) => (
                  <button key={dataset.id} type="button" className={`sidebar-dataset-item ${dataset.id === activeDataset?.id ? 'active' : ''}`} onClick={() => void loadDatasetWorkspace(dataset.id, `已切换到数据集「${dataset.name}」。`)}>
                    <strong>{dataset.name}</strong>
                    <span>{dataset.rootKeyword} · {statusLabel(dataset.status)}</span>
                  </button>
                ))}
              </div>
            ) : (
              <p className="sidebar-empty">暂无数据集，先到“计划编排”页面创建一个任务。</p>
            )}
          </Panel>
        </div>
      </aside>

      <button type="button" className="sidebar-backdrop" onClick={() => setMobileNavOpen(false)} aria-label="关闭侧栏" />

      <main className="console-main">
        <AnimatePresence mode="wait">
          <motion.div
            key={currentPage}
            initial={{ opacity: 0, y: 18 }}
            animate={{ opacity: 1, y: 0 }}
            exit={{ opacity: 0, y: -12 }}
            transition={{ duration: 0.22 }}
            className="console-page"
          >
            {renderCurrentPage()}
          </motion.div>
        </AnimatePresence>
      </main>
    </div>
  )
}
