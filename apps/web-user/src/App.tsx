import type { FormEvent } from 'react'
import { useEffect, useMemo, useState } from 'react'
import { motion } from 'framer-motion'
import {
  ArrowRight,
  BellRing,
  BrainCircuit,
  CheckCircle2,
  Clock3,
  DatabaseZap,
  FileOutput,
  GitBranch,
  HardDriveDownload,
  Layers3,
  LayoutDashboard,
  PanelTop,
  RefreshCw,
  Save,
  ServerCog,
  ShieldCheck,
  Sparkles,
  Target,
  Waypoints,
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

function GraphPreview({ rootKeyword, domains }: { rootKeyword: string; domains: Domain[] }) {
  const visible = domains.slice(0, 16)
  const width = 680
  const height = 420
  const centerX = width / 2
  const centerY = height / 2
  const radius = 148

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
            <stop offset="0%" stopColor="rgba(89, 121, 255, 0.38)" />
            <stop offset="100%" stopColor="rgba(89, 121, 255, 0)" />
          </radialGradient>
        </defs>
        <circle cx={centerX} cy={centerY} r="160" fill="url(#graphGlow)" />
        {positioned.map((node) => (
          <line key={`line-${node.id}`} x1={centerX} y1={centerY} x2={node.x} y2={node.y} stroke="rgba(111, 146, 255, 0.24)" strokeWidth="1.3" />
        ))}
        <circle cx={centerX} cy={centerY} r="54" fill="rgba(8, 16, 34, 0.92)" stroke="rgba(94, 120, 255, 0.58)" />
        <text x={centerX} y={centerY} textAnchor="middle" dominantBaseline="middle" className="graph-root-text">{rootKeyword}</text>
        {positioned.map((node) => (
          <g key={node.id}>
            <circle cx={node.x} cy={node.y} r="31" fill="rgba(8, 16, 34, 0.94)" stroke="rgba(99, 139, 255, 0.32)" />
            <text x={node.x} y={node.y} textAnchor="middle" dominantBaseline="middle" className="graph-node-text">{node.name.slice(0, 8)}</text>
          </g>
        ))}
      </svg>
      {domains.length > visible.length ? <p className="graph-caption">当前图谱仅展示前 16 个领域节点，实际已生成 {domains.length} 个领域。</p> : null}
    </div>
  )
}

function SectionHeading({ eyebrow, title, description }: { eyebrow: string; title: string; description?: string }) {
  return (
    <div className="section-heading">
      <span className="eyebrow">{eyebrow}</span>
      <h2>{title}</h2>
      {description ? <p>{description}</p> : null}
    </div>
  )
}

function MetricCard({ label, value, helper }: { label: string; value: string | number; helper: string }) {
  return (
    <article className="metric-card">
      <span>{label}</span>
      <strong>{value}</strong>
      <p>{helper}</p>
    </article>
  )
}

function EmptyState({ title, description }: { title: string; description: string }) {
  return (
    <div className="empty-state">
      <p className="empty-title">{title}</p>
      <p>{description}</p>
    </div>
  )
}

type WorkflowStageItem = {
  icon: LucideIcon
  title: string
  description: string
  tone: 'done' | 'active' | 'pending'
  summary: string
}

function WorkflowStage({ icon: Icon, title, description, tone, summary }: WorkflowStageItem) {
  return (
    <article className={`workflow-stage ${tone}`}>
      <div className="workflow-stage-icon"><Icon size={18} strokeWidth={1.9} /></div>
      <div>
        <div className="workflow-stage-headline">
          <h3>{title}</h3>
          <span>{tone === 'done' ? '已完成' : tone === 'active' ? '进行中' : '待推进'}</span>
        </div>
        <p>{description}</p>
        <strong>{summary}</strong>
      </div>
    </article>
  )
}

export default function App() {
  const [authenticated, setAuthenticated] = useState(false)
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

  const adminHref = typeof window !== 'undefined'
    ? `${window.location.protocol}//${window.location.hostname}:3211`
    : '/'

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
    void loadBootstrap('控制台基础数据已加载，可以开始配置任务。')
  }, [authenticated])

  const plannerCards = useMemo(() => {
    if (!estimate) return []
    return [
      { label: '领域数', value: estimate.domainCount, helper: '建议拆分出的一级领域数量' },
      { label: '每领域问题数', value: estimate.questionsPerDomain, helper: '单个领域建议生成的问题体量' },
      { label: '预计问题数', value: estimate.estimatedQuestions, helper: '进入问题队列的总问题数' },
      { label: '预计样本数', value: estimate.estimatedSamples, helper: '最终产出的训练样本规模' },
    ]
  }, [estimate])

  const runtimeCards = useMemo(() => ([
    { label: '数据集', value: runtime?.datasetCount ?? 0, helper: '当前平台在管任务数' },
    { label: '问题', value: runtime?.questionCount ?? 0, helper: '已入库的问题条目' },
    { label: '奖励', value: runtime?.rewardCount ?? 0, helper: '完成评分的奖励记录' },
    { label: '队列深度', value: runtime?.queueDepth ?? 0, helper: '等待执行的后台任务' },
  ]), [runtime])

  const workflowStages = useMemo<WorkflowStageItem[]>(() => ([
    {
      icon: Target,
      title: '计划估算',
      description: '先锁定目标样本规模，再反推领域与问题配额。',
      tone: estimate ? 'done' : authenticated ? 'active' : 'pending',
      summary: estimate ? `预计 ${estimate.estimatedSamples} 个样本` : authenticated ? '等待运行估算' : '先解锁控制台',
    },
    {
      icon: Waypoints,
      title: '图谱审核',
      description: '生成领域节点后进行命名润色和人工确认。',
      tone: activeDataset?.status && activeDataset.status !== 'draft' ? 'done' : graph ? 'active' : 'pending',
      summary: graph ? `${graph.domains.length} 个领域待治理` : '等待创建数据集',
    },
    {
      icon: BrainCircuit,
      title: '问题与推理',
      description: '按领域触发问题生成，再补齐推理与答案摘要。',
      tone: reasoning.length > 0 ? 'done' : questions.length > 0 ? 'active' : 'pending',
      summary: reasoning.length > 0 ? `${reasoning.length} 条推理记录` : questions.length > 0 ? `${questions.length} 个问题待扩展` : '等待图谱确认后推进',
    },
    {
      icon: FileOutput,
      title: '奖励与导出',
      description: '完成打分后打包 JSONL 工件并回收运行态数据。',
      tone: artifacts.length > 0 ? 'done' : rewards.length > 0 ? 'active' : 'pending',
      summary: artifacts.length > 0 ? `${artifacts.length} 个导出工件` : rewards.length > 0 ? `${rewards.length} 条奖励记录` : '等待推理阶段结束',
    },
  ]), [activeDataset?.status, artifacts.length, authenticated, estimate, graph, questions.length, reasoning.length, rewards.length])

  const navGroups = [
    {
      label: '控制台',
      items: [
        { href: '#overview', icon: PanelTop, label: '总览态势' },
        { href: '#planner', icon: LayoutDashboard, label: '计划编排' },
      ],
    },
    {
      label: '数据流',
      items: [
        { href: '#graph', icon: GitBranch, label: '领域图谱' },
        { href: '#pipeline', icon: BrainCircuit, label: '生成流水线' },
        { href: '#exports', icon: HardDriveDownload, label: '导出中心' },
      ],
    },
  ]

  const selectedStrategy = strategies.find((strategy) => strategy.id === Number(formState.strategyId))
  const selectedProvider = providers.find((provider) => provider.id === Number(formState.providerId))
  const selectedStorage = storageProfiles.find((profile) => profile.id === Number(formState.storageProfileId))

  const quickFacts = [
    `当前主题：${formState.rootKeyword}`,
    `策略：${selectedStrategy?.name ?? '待同步'}`,
    `模型：${selectedProvider?.model ?? '待同步'}`,
    `存储：${selectedStorage?.name ?? '待同步'}`,
  ]

  const submitLogin = (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault()
    setAuthenticated(true)
    setMessage('控制台已解锁，请先完成计划估算，再创建数据集与领域图谱。')
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
      setLoading(false)
      await loadDatasetWorkspace(created.id, '数据集已创建，接下来可以生成并治理领域图谱。')
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
      setLoading(false)
      await loadDatasetWorkspace(graph.dataset.id, '领域已确认，数据集已进入问题生成阶段。')
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

  return (
    <div className="workspace-shell">
      <aside className="sidebar-frame">
        <div className="sidebar-brand">
          <div className="brand-badge"><Sparkles size={18} strokeWidth={1.8} /></div>
          <div>
            <strong>LLM 数据工厂</strong>
            <p>企业级训练数据控制台</p>
          </div>
        </div>

        <div className="sidebar-mode-card">
          <span className="sidebar-label">门户模式</span>
          <strong>数据生产作战台</strong>
          <p>以控制台方式统一管理计划、图谱治理、生成流水线与导出交付。</p>
        </div>

        {navGroups.map((group) => (
          <div key={group.label} className="sidebar-nav-group">
            <span className="sidebar-group-title">{group.label}</span>
            <nav className="sidebar-navlist">
              {group.items.map(({ href, icon: Icon, label }) => (
                <a key={href} href={href}><Icon size={16} /> {label}</a>
              ))}
            </nav>
          </div>
        ))}

        <div className="sidebar-summary">
          <span className="sidebar-label">运行概况</span>
          <div className="sidebar-metric"><span>数据集</span><strong>{runtime?.datasetCount ?? 0}</strong></div>
          <div className="sidebar-metric"><span>问题数</span><strong>{runtime?.questionCount ?? 0}</strong></div>
          <div className="sidebar-metric"><span>队列深度</span><strong>{runtime?.queueDepth ?? 0}</strong></div>
          <div className="sidebar-metric"><span>工件数</span><strong>{runtime?.artifactCount ?? 0}</strong></div>
        </div>

        <div className="sidebar-note">
          <span className="sidebar-label">操作纪律</span>
          <p>先估算、后建库、再审核图谱。确认领域前，不要提前触发问题、推理和导出队列。</p>
        </div>

        <a className="admin-jump" href={adminHref}>进入管理后台</a>
      </aside>

      <main className="workspace-content" id="overview">
        <section className="topbar-card">
          <div>
            <p className="eyebrow">参考后台控制台重构</p>
            <h1>把计划、治理、生成与导出收束到一套更深色、更密集的企业控制面板。</h1>
          </div>
          <div className="topbar-actions">
            <button className="ghost-action" onClick={() => void loadBootstrap('控制台基础数据已刷新。')} disabled={loading || !authenticated}>
              <RefreshCw size={16} /> 刷新运行态
            </button>
            <a className="ghost-action" href="#pipeline">
              <Layers3 size={16} /> 跳转流水线
            </a>
          </div>
        </section>

        <section className="hero-grid">
          <section className="hero-card command-card">
            <motion.div initial={{ opacity: 0, y: 18 }} animate={{ opacity: 1, y: 0 }} transition={{ duration: 0.45 }}>
              <p className="eyebrow">训练数据控制台 / 指挥视图</p>
              <h2>让运营人员先看见状态，再执行动作：更像一套真实的企业级作业台。</h2>
              <p className="headline-copy">对齐参考前端的控制台感：强化左侧导航、顶栏调度、深色面板、分层信息架构和更明确的状态提示，让数据集工作流像后台产品而不是营销页。</p>
              <div className="headline-actions">
                <button className="primary-action" onClick={() => void estimatePlan()} disabled={loading || !authenticated}>估算计划 <ArrowRight size={16} /></button>
                <button className="secondary-action" onClick={() => void createDataset()} disabled={loading || !authenticated}>创建数据集</button>
              </div>
              {message ? <p className="status-banner"><BellRing size={16} /> {message}</p> : null}
              <div className="quick-facts-grid">
                {quickFacts.map((fact) => <div key={fact} className="fact-chip">{fact}</div>)}
              </div>
            </motion.div>
          </section>

          <section className="operations-card">
            <SectionHeading eyebrow="值班席位" title="作业态势面板" description="把当前项目、运行指标和阶段信号并排摆放，减少来回滚动查找。" />
            <div className="operations-stack">
              <div className="operations-highlight">
                <span className="sidebar-label">当前项目</span>
                <strong>{activeDataset?.name ?? '尚未选中数据集'}</strong>
                <p>{activeDataset ? `${activeDataset.rootKeyword} · 状态 ${statusLabel(activeDataset.status)}` : '先完成计划估算并创建首个任务。'}</p>
              </div>
              <div className="operations-mini-grid">
                {runtimeCards.map((item) => (
                  <MetricCard key={item.label} label={item.label} value={item.value} helper={item.helper} />
                ))}
              </div>
            </div>
          </section>
        </section>

        <section className="board-grid">
          <section className="content-card workflow-board">
            <SectionHeading eyebrow="阶段状态" title="流程总览" description="按后台作业台的方式追踪当前阶段，明确哪里已完成、哪里正在推进。" />
            <div className="workflow-stage-list">
              {workflowStages.map((stage) => (
                <WorkflowStage key={stage.title} {...stage} />
              ))}
            </div>
          </section>

          <section className="content-card active-dataset-card">
            <SectionHeading eyebrow="当前数据集" title="执行中的项目上下文" description="始终把正在处理的数据集放在右侧固定面板，便于快速判断上下文。" />
            <div className="active-dataset-panel">
              <div className="active-dataset-head">
                <span className="sidebar-label">数据集状态</span>
                {activeDataset ? <span className="status-chip">{statusLabel(activeDataset.status)}</span> : null}
              </div>
              {activeDataset ? (
                <>
                  <h3>{activeDataset.name}</h3>
                  <p>{activeDataset.rootKeyword} · 预计 {activeDataset.estimate.estimatedSamples} 个样本</p>
                  <div className="active-dataset-meta">
                    <span>创建时间：{formatDateLabel(activeDataset.createdAt)}</span>
                    <span>更新时间：{formatDateLabel(activeDataset.updatedAt)}</span>
                  </div>
                </>
              ) : (
                <EmptyState title="尚未选中数据集" description="创建新数据集，或从下方最近任务中切换一个已有项目。" />
              )}
            </div>
          </section>
        </section>

        <section className="content-grid" id="planner">
          <div className="content-card planner-card">
            <SectionHeading eyebrow="计划编排" title="主题、规模、策略与资源配置" description="先锁定数据集任务入口，再将模型、存储和策略放进同一套控制表单。" />
            {!authenticated ? (
              <form className="login-card" onSubmit={submitLogin}>
                <label>
                  运营邮箱
                  <input type="email" placeholder="operator@company.com" required />
                </label>
                <label>
                  控制台访问密钥
                  <input type="password" placeholder="请输入访问密钥" required />
                </label>
                <button type="submit" className="primary-action full-width">解锁控制台 <ArrowRight size={16} /></button>
              </form>
            ) : (
              <>
                <div className="planner-grid">
                  <label>
                    数据集名称
                    <input value={formState.name} onChange={(event) => setFormState({ ...formState, name: event.target.value })} placeholder="军事推理数据工厂" />
                  </label>
                  <label>
                    根关键词
                    <input value={formState.rootKeyword} onChange={(event) => setFormState({ ...formState, rootKeyword: event.target.value })} />
                  </label>
                  <label>
                    目标规模
                    <input type="number" value={formState.targetSize} onChange={(event) => setFormState({ ...formState, targetSize: Number(event.target.value) })} />
                  </label>
                  <label>
                    生成策略
                    <select value={formState.strategyId} onChange={(event) => setFormState({ ...formState, strategyId: Number(event.target.value) })}>
                      {strategies.map((strategy) => <option key={strategy.id} value={strategy.id}>{strategy.name}</option>)}
                    </select>
                  </label>
                  <label>
                    模型提供方
                    <select value={formState.providerId} onChange={(event) => setFormState({ ...formState, providerId: Number(event.target.value) })}>
                      {providers.map((provider) => <option key={provider.id} value={provider.id}>{provider.name}</option>)}
                    </select>
                  </label>
                  <label>
                    存储配置
                    <select value={formState.storageProfileId} onChange={(event) => setFormState({ ...formState, storageProfileId: Number(event.target.value) })}>
                      {storageProfiles.map((profile) => <option key={profile.id} value={profile.id}>{profile.name}</option>)}
                    </select>
                  </label>
                </div>
                <div className="planner-actions">
                  <button className="secondary-action" onClick={() => void loadBootstrap('控制台基础数据已刷新。')} disabled={loading}>
                    <RefreshCw size={16} /> 同步资源
                  </button>
                  <button className="primary-action" onClick={() => void estimatePlan()} disabled={loading}>
                    更新估算
                  </button>
                </div>
                {plannerCards.length > 0 ? (
                  <div className="planner-cards">
                    {plannerCards.map((item) => (
                      <MetricCard key={item.label} label={item.label} value={item.value} helper={item.helper} />
                    ))}
                  </div>
                ) : (
                  <EmptyState title="尚未生成估算结果" description="填写策略和目标规模后，先运行一次估算，再决定是否创建数据集。" />
                )}
              </>
            )}
          </div>

          <div className="content-card readiness-card">
            <SectionHeading eyebrow="执行准备度" title="策略、模型与存储就绪检查" description="把核心配置浓缩成右侧控制面板，避免误选资源后再回头排查。" />
            <div className="readiness-list">
              <article className="readiness-item">
                <div className="readiness-icon"><Target size={18} strokeWidth={1.8} /></div>
                <div>
                  <strong>{selectedStrategy?.name ?? '尚未选择策略'}</strong>
                  <p>{selectedStrategy?.description ?? '请先同步并选择可用的生成策略。'}</p>
                </div>
              </article>
              <article className="readiness-item">
                <div className="readiness-icon"><ServerCog size={18} strokeWidth={1.8} /></div>
                <div>
                  <strong>{selectedProvider?.name ?? '尚未选择提供方'}</strong>
                  <p>{selectedProvider ? `${selectedProvider.providerType} · ${selectedProvider.model}` : '请先同步模型提供方列表。'}</p>
                </div>
              </article>
              <article className="readiness-item">
                <div className="readiness-icon"><ShieldCheck size={18} strokeWidth={1.8} /></div>
                <div>
                  <strong>{selectedStorage?.name ?? '尚未选择存储'}</strong>
                  <p>{selectedStorage ? `${selectedStorage.provider} · ${selectedStorage.bucket}` : '请先同步存储配置。'}</p>
                </div>
              </article>
            </div>
          </div>
        </section>

        <section className="content-card dataset-strip" id="datasets">
          <SectionHeading eyebrow="最近的数据集" title="快速切换任务" description="以更像企业后台卡片列表的形式展示最近项目，方便在多主题任务之间切换。" />
          {datasets.length > 0 ? (
            <div className="dataset-grid">
              {datasets.map((dataset) => {
                const active = dataset.id === activeDataset?.id
                return (
                  <button
                    key={dataset.id}
                    className={`dataset-card ${active ? 'active' : ''}`}
                    onClick={() => void loadDatasetWorkspace(dataset.id, `已切换到数据集「${dataset.name}」。`)}
                  >
                    <div className="dataset-card-header">
                      <span className="eyebrow">{statusLabel(dataset.status)}</span>
                      {active ? <span className="status-chip status-chip-strong">当前项目</span> : null}
                    </div>
                    <h3>{dataset.name}</h3>
                    <p>{dataset.rootKeyword} · 预计 {dataset.estimate.estimatedSamples} 个样本</p>
                    <div className="dataset-meta"><GitBranch size={15} /> 数据集 #{dataset.id} · 更新于 {formatDateLabel(dataset.updatedAt)}</div>
                  </button>
                )
              })}
            </div>
          ) : (
            <EmptyState title="还没有可切换的数据集" description="解锁控制台并创建首个项目后，这里会显示最近的任务卡片。" />
          )}
        </section>

        <section className="content-grid graph-layout" id="graph">
          <div className="content-card span-two graph-card">
            <SectionHeading eyebrow="领域图谱" title="图谱生成与审核" description="使用深色控制台画布承载领域结构，强化后台作业感和节点可读性。" />
            {graph ? (
              <>
                <GraphPreview rootKeyword={graph.dataset.rootKeyword} domains={graph.domains} />
                <div className="graph-toolbar">
                  <div className="graph-toolbar-copy">
                    <span className="status-chip">{statusLabel(graph.dataset.status)}</span>
                    <p>先生成领域，再做命名润色、保存编辑，最后执行确认。</p>
                  </div>
                  <div className="graph-actions">
                    <button className="secondary-action" onClick={() => void generateDomains()} disabled={loading}>生成或刷新领域</button>
                    <button className="secondary-action" onClick={() => void saveGraph()} disabled={loading}><Save size={15} /> 保存编辑</button>
                    <button className="primary-action" onClick={() => void confirmDomains()} disabled={loading}>确认领域</button>
                  </div>
                </div>
              </>
            ) : (
              <EmptyState title="尚未加载领域图谱" description="请先创建或切换到一个数据集，再开始生成领域节点。" />
            )}
          </div>

          <div className="content-card review-card">
            <SectionHeading eyebrow="领域列表" title="人工命名润色" description="优先审阅前 20 个领域，适配真实运营场景下的快速人工校对。" />
            {graph ? (
              <div className="domain-list compact-list">
                {graph.domains.slice(0, 20).map((domain) => (
                  <label key={domain.id} className="domain-item">
                    <span>{sourceLabel(domain.source)}</span>
                    <input value={domain.name} onChange={(event) => renameDomain(domain.id, event.target.value)} />
                  </label>
                ))}
                {graph.domains.length > 20 ? <p className="graph-caption">当前界面仅支持编辑前 20 个领域；全部 {graph.domains.length} 个领域仍会完整持久化保存。</p> : null}
              </div>
            ) : (
              <EmptyState title="尚未生成领域节点" description="创建数据集后点击“生成或刷新领域”，再回来逐条审核命名。" />
            )}
          </div>
        </section>

        <section className="content-grid pipeline-grid" id="pipeline">
          <div className="content-card pipeline-card">
            <SectionHeading eyebrow="问题生成" title="问题流水线" description="把触发按钮与问题预览收敛到一张深色业务卡中，贴近控制台面板体验。" />
            <div className="graph-actions">
              <button className="primary-action" onClick={() => void generateQuestions()} disabled={loading || !graph}>加入问题生成队列</button>
              <button className="secondary-action" onClick={() => void refreshQuestions()} disabled={loading || !graph}><RefreshCw size={15} /> 刷新问题</button>
            </div>
            <div className="domain-list">
              {questions.slice(0, 8).map((question) => (
                <div key={question.id} className="question-item">
                  <span>{question.domainName}</span>
                  <p>{question.content}</p>
                </div>
              ))}
              {questions.length === 0 ? <EmptyState title="尚未生成问题" description="先确认领域，再把问题生成任务加入后台队列。" /> : null}
            </div>
          </div>

          <div className="content-card pipeline-card">
            <SectionHeading eyebrow="推理生成" title="推理与答案摘要" description="与问题面板使用同一套节奏和层级，降低操作切换成本。" />
            <div className="graph-actions">
              <button className="primary-action" onClick={() => void generateReasoning()} disabled={loading || !graph || questions.length === 0}>加入推理生成队列</button>
              <button className="secondary-action" onClick={() => void refreshReasoning()} disabled={loading || !graph}><RefreshCw size={15} /> 刷新推理</button>
            </div>
            <div className="domain-list">
              {reasoning.slice(0, 8).map((record) => (
                <div key={record.id} className="question-item">
                  <span>{record.objectKey}</span>
                  <p>{record.answerSummary}</p>
                </div>
              ))}
              {reasoning.length === 0 ? <EmptyState title="尚未生成推理记录" description="先准备好问题列表，再把推理生成任务加入队列。" /> : null}
            </div>
          </div>

          <div className="content-card pipeline-card">
            <SectionHeading eyebrow="奖励数据" title="评估与奖励" description="将评分结果与问题文本并列展示，更适合后台运营快速抽检。" />
            <div className="graph-actions">
              <button className="primary-action" onClick={() => void generateRewards()} disabled={loading || !graph || reasoning.length === 0}>加入奖励生成队列</button>
              <button className="secondary-action" onClick={() => void refreshRewards()} disabled={loading || !graph}><RefreshCw size={15} /> 刷新奖励数据</button>
            </div>
            <div className="domain-list">
              {rewards.slice(0, 8).map((record) => (
                <div key={record.id} className="question-item">
                  <span>{record.objectKey}</span>
                  <p>评分：{record.score.toFixed(2)} · {record.questionText}</p>
                </div>
              ))}
              {rewards.length === 0 ? <EmptyState title="尚未生成奖励记录" description="推理完成后再触发奖励生成，结果会在这里回显。" /> : null}
            </div>
          </div>
        </section>

        <section className="content-grid export-grid" id="exports">
          <div className="content-card export-card">
            <SectionHeading eyebrow="导出与运行态" title="工件打包" description="将导出动作与平台运行态指标合并到一个交付面板，适合企业后台的收口动作。" />
            <div className="graph-actions">
              <button className="primary-action" onClick={() => void generateExport()} disabled={loading || !graph || rewards.length === 0}>加入导出队列</button>
              <button className="secondary-action" onClick={() => void refreshArtifacts()} disabled={loading || !graph}><RefreshCw size={15} /> 刷新工件</button>
            </div>
            {runtime ? (
              <div className="planner-cards runtime-grid">
                <MetricCard label="数据集" value={runtime.datasetCount} helper="当前管理中的项目数量" />
                <MetricCard label="队列深度" value={runtime.queueDepth} helper="等待执行的后台任务" />
                <MetricCard label="工件数" value={runtime.artifactCount} helper="已经生成的导出文件" />
                <MetricCard label="奖励记录" value={runtime.rewardCount} helper="完成评分的数据条目" />
              </div>
            ) : (
              <EmptyState title="尚未拉取运行态" description="解锁控制台后同步基础数据，即可查看平台最新运行概况。" />
            )}
          </div>

          <div className="content-card export-card">
            <SectionHeading eyebrow="工件预览" title="已打包产物" description="以更接近控制台列表的方式展示导出结果，突出类型和对象键。" />
            <div className="domain-list">
              {artifacts.map((artifact) => (
                <div key={artifact.id} className="question-item artifact-item">
                  <span>{artifactTypeLabel(artifact.artifactType)}</span>
                  <p>{artifact.objectKey}</p>
                </div>
              ))}
              {artifacts.length === 0 ? <EmptyState title="尚未生成导出工件" description="完成奖励生成后即可触发导出；产物会在这里集中展示。" /> : null}
            </div>
          </div>
        </section>

        <section className="completion-strip">
          <div className="completion-card">
            <div className="completion-icon"><CheckCircle2 size={18} strokeWidth={1.9} /></div>
            <div>
              <strong>当前目标</strong>
              <p>{activeDataset ? `继续推进「${activeDataset.name}」的后续阶段。` : '先完成估算并创建首个数据集。'}</p>
            </div>
          </div>
          <div className="completion-card">
            <div className="completion-icon"><Clock3 size={18} strokeWidth={1.9} /></div>
            <div>
              <strong>推荐节奏</strong>
              <p>每完成一个阶段就刷新对应面板，确认结果落库后再推进下一步。</p>
            </div>
          </div>
        </section>
      </main>
    </div>
  )
}
