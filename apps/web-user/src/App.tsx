import { useCallback, useEffect, useMemo, useState } from 'react'
import { Navigate, Route, Routes, useLocation, useNavigate } from 'react-router-dom'
import {
  Avatar,
  Banner,
  Button,
  Card,
  Empty,
  Input,
  InputNumber,
  Layout,
  List,
  Modal,
  Nav,
  Progress,
  Select,
  Space,
  Spin,
  Switch,
  Table,
  Tag,
  TextArea,
  Toast,
  Typography,
} from '@douyinfe/semi-ui'
import {
  Bell,
  BrainCircuit,
  Database,
  FileOutput,
  FolderCog,
  GitBranch,
  HardDriveDownload,
  LayoutDashboard,
  Layers3,
  LogOut,
  Network,
  RefreshCw,
  ServerCog,
  Settings,
  ShieldCheck,
  Sparkles,
  Target,
  Users,
  Workflow,
  type LucideIcon,
} from 'lucide-react'
import clsx from 'clsx'
import {
  authApi,
  consoleApi,
  type Artifact,
  type AuditRecord,
  type DashboardRecord,
  type Dataset,
  type DatasetGraph,
  type Domain,
  type PromptRecord,
  type Provider,
  type ProviderConnectivityResult,
  type ProviderModelInfo,
  type Question,
  type ReasoningRecord,
  type RewardRecord,
  type RuntimeStatus,
  type StorageProfile,
  type Strategy,
  type User,
} from './lib/api'

const { Header, Sider, Content } = Layout
const { Title, Text } = Typography

type ProviderDraft = Partial<Provider> & { apiKey?: string }
type StorageDraft = Partial<StorageProfile> & { secretAccessKey?: string }

type NavPage = {
  label: string
  route: string
  icon: LucideIcon
  caption: string
  adminOnly?: boolean
}

const userPages: NavPage[] = [
  { label: '总览', route: '/console/overview', icon: LayoutDashboard, caption: '统一查看平台与数据集运行态' },
  { label: '计划编排', route: '/console/planning', icon: Target, caption: '配置目标规模与生成策略' },
  { label: '领域图谱', route: '/console/domains', icon: GitBranch, caption: '生成、修订并确认领域结构' },
  { label: '问题生成', route: '/console/questions', icon: Layers3, caption: '按领域批量生成训练问题' },
  { label: '推理生成', route: '/console/reasoning', icon: BrainCircuit, caption: '生成长思维链与答案' },
  { label: '奖励数据', route: '/console/rewards', icon: ShieldCheck, caption: '生成强化学习奖励评估数据' },
  { label: '导出交付', route: '/console/exports', icon: HardDriveDownload, caption: '打包工件并查看运行态收口' },
]

const adminPages: NavPage[] = [
  { label: '模型提供方', route: '/console/admin/providers', icon: Database, caption: '维护模型网关、路由与并发参数', adminOnly: true },
  { label: '存储配置', route: '/console/admin/storage', icon: FolderCog, caption: '维护 S3 / MinIO / OSS 存储目标', adminOnly: true },
  { label: '生成策略', route: '/console/admin/strategies', icon: Workflow, caption: '定义领域规模、问题量与奖励变体', adminOnly: true },
  { label: '提示词模板', route: '/console/admin/prompts', icon: Sparkles, caption: '治理各阶段系统提示词与版本', adminOnly: true },
  { label: '审计日志', route: '/console/admin/audit', icon: Settings, caption: '追踪配置变更与治理事件', adminOnly: true },
]

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

function artifactLabel(type: string) {
  switch (type) {
    case 'jsonl-export':
      return 'JSONL 导出包'
    default:
      return type
  }
}

function formatTime(value?: string) {
  if (!value) return '—'
  return new Intl.DateTimeFormat('zh-CN', {
    month: '2-digit',
    day: '2-digit',
    hour: '2-digit',
    minute: '2-digit',
    hour12: false,
  }).format(new Date(value))
}

function GraphPreview({ rootKeyword, domains }: { rootKeyword: string; domains: Domain[] }) {
  const visible = domains.slice(0, 16)
  const width = 880
  const height = 420
  const centerX = width / 2
  const centerY = height / 2
  const radius = 160

  const nodes = visible.map((domain, index) => {
    const angle = (Math.PI * 2 * index) / Math.max(visible.length, 1)
    return {
      ...domain,
      x: centerX + Math.cos(angle) * radius,
      y: centerY + Math.sin(angle) * radius,
    }
  })

  return (
    <svg viewBox={`0 0 ${width} ${height}`} className="console-graph">
      <defs>
        <radialGradient id="consoleGraphGlow" cx="50%" cy="50%" r="50%">
          <stop offset="0%" stopColor="rgba(59, 130, 246, 0.26)" />
          <stop offset="100%" stopColor="rgba(59, 130, 246, 0)" />
        </radialGradient>
      </defs>
      <circle cx={centerX} cy={centerY} r="180" fill="url(#consoleGraphGlow)" />
      {nodes.map((node) => (
        <line key={`line-${node.id}`} x1={centerX} y1={centerY} x2={node.x} y2={node.y} stroke="rgba(59,130,246,0.18)" strokeWidth="1.4" />
      ))}
      <circle cx={centerX} cy={centerY} r="64" fill="white" stroke="rgba(59,130,246,0.28)" strokeWidth="2" />
      <text x={centerX} y={centerY - 4} textAnchor="middle" dominantBaseline="middle" fontSize="18">{rootKeyword}</text>
      <text x={centerX} y={centerY + 18} textAnchor="middle" dominantBaseline="middle" fontSize="10" fill="var(--semi-color-text-2)">核心主题</text>
      {nodes.map((node) => (
        <g key={node.id}>
          <circle cx={node.x} cy={node.y} r="32" fill="white" stroke="rgba(59,130,246,0.18)" strokeWidth="1.5" />
          <text x={node.x} y={node.y - 2} textAnchor="middle" dominantBaseline="middle" fontSize="11">{node.name.slice(0, 8)}</text>
          <text x={node.x} y={node.y + 14} textAnchor="middle" dominantBaseline="middle" fontSize="9" fill="var(--semi-color-text-2)">{sourceLabel(node.source)}</text>
        </g>
      ))}
    </svg>
  )
}

function PageHeader({
  title,
  description,
  badge,
  actions,
}: {
  title: string
  description: string
  badge?: string
  actions?: React.ReactNode
}) {
  return (
    <div className="flex flex-col gap-4 lg:flex-row lg:items-end lg:justify-between">
      <div className="console-route-banner">
        {badge ? <span className="console-chip">{badge}</span> : null}
        <Title heading={2} className="!mb-0 console-page-title">{title}</Title>
        <Text className="console-page-subtitle">{description}</Text>
      </div>
      {actions ? <div className="flex flex-wrap gap-3">{actions}</div> : null}
    </div>
  )
}

function StatCard({ icon: Icon, label, value, helper }: { icon: LucideIcon; label: string; value: string | number; helper: string }) {
  return (
    <Card className="console-stat-card" bodyStyle={{ padding: 20 }}>
      <div className="flex items-center justify-between gap-3">
        <Text className="console-muted">{label}</Text>
        <div className="stat-card-icon"><Icon size={16} strokeWidth={1.9} /></div>
      </div>
      <div className="mt-5 console-stat-value">{value}</div>
      <Text className="mt-3 block console-caption">{helper}</Text>
    </Card>
  )
}

function EmptyCard({ title, description }: { title: string; description: string }) {
  return (
    <div className="console-empty">
      <Empty title={title} description={description} />
    </div>
  )
}

function LoginPage({ onSubmit, loading }: { onSubmit: (email: string, password: string) => Promise<void>; loading: boolean }) {
  const [email, setEmail] = useState('')
  const [password, setPassword] = useState('')

  return (
    <div className="console-login-shell flex items-center justify-center px-4 py-10">
      <div className="grid w-full max-w-6xl gap-6 lg:grid-cols-[1.15fr,0.85fr]">
        <Card className="console-panel" bodyStyle={{ padding: 28 }}>
          <span className="console-chip">与 new-api 对齐的统一控制台</span>
          <Title heading={1} className="!mb-0 mt-4">一个前端同时承载用户与管理员能力。</Title>
          <Text className="mt-4 block console-page-subtitle">
            最新参考代码已同步到 /root/new-api 的 {`2026-03-23 15:04:06 +08:00`} 提交。
            现在的控制台将用户任务链路和管理员配置治理合并到同一应用，通过登录角色区分可见功能。
          </Text>
          <div className="console-card-grid-3 mt-6">
            {[
              { icon: LayoutDashboard, title: '统一工作台', text: '总览、计划、图谱、问题、推理、奖励、导出在同一控制台切换。' },
              { icon: ShieldCheck, title: '角色鉴权', text: '普通用户只看生产链路，管理员额外拥有系统配置能力。' },
              { icon: Database, title: '同源 API', text: '继续通过 /api 访问后端，避免浏览器 localhost 和跨域问题。' },
            ].map((item) => (
              <Card key={item.title} className="console-quick-card" bodyStyle={{ padding: 18 }}>
                <div className="feature-icon"><item.icon size={18} strokeWidth={1.9} /></div>
                <Title heading={5} className="!mb-0 mt-4">{item.title}</Title>
                <Text className="mt-2 block console-caption">{item.text}</Text>
              </Card>
            ))}
          </div>
        </Card>

        <Card className="console-login-card" bodyStyle={{ padding: 28 }}>
          <div className="flex items-center gap-3">
            <div className="feature-icon"><Users size={18} strokeWidth={1.9} /></div>
            <div>
              <Title heading={4} className="!mb-0">登录统一控制台</Title>
              <Text className="console-caption">默认内置两个账号，便于首启验证。</Text>
            </div>
          </div>
          <div className="mt-5 grid gap-4">
            <div>
              <Text className="mb-2 block font-medium">邮箱</Text>
              <Input value={email} onChange={setEmail} size="large" />
            </div>
            <div>
              <Text className="mb-2 block font-medium">密码</Text>
              <Input value={password} onChange={setPassword} mode="password" size="large" />
            </div>
            <Button theme="solid" type="primary" size="large" loading={loading} onClick={() => void onSubmit(email, password)}>
              登录
            </Button>
          </div>
          <div className="mt-6 console-summary-grid">
            <div className="console-summary-row"><span>登录说明</span><Text strong>请使用已分配账号登录</Text></div>
          </div>
        </Card>
      </div>
    </div>
  )
}

export default function App() {
  const location = useLocation()
  const navigate = useNavigate()
  const [sessionLoading, setSessionLoading] = useState(true)
  const [authSubmitting, setAuthSubmitting] = useState(false)
  const [busy, setBusy] = useState(false)
  const [sidebarCollapsed, setSidebarCollapsed] = useState(false)
  const [user, setUser] = useState<User | null>(null)

  const [providers, setProviders] = useState<Provider[]>([])
  const [storageProfiles, setStorageProfiles] = useState<StorageProfile[]>([])
  const [strategies, setStrategies] = useState<Strategy[]>([])
  const [datasets, setDatasets] = useState<Dataset[]>([])
  const [dashboard, setDashboard] = useState<DashboardRecord | null>(null)
  const [prompts, setPrompts] = useState<PromptRecord[]>([])
  const [auditLogs, setAuditLogs] = useState<AuditRecord[]>([])
  const [runtime, setRuntime] = useState<RuntimeStatus | null>(null)
  const [estimate, setEstimate] = useState<Dataset['estimate'] | null>(null)

  const [activeDatasetId, setActiveDatasetId] = useState<number | null>(null)
  const [graph, setGraph] = useState<DatasetGraph | null>(null)
  const [questions, setQuestions] = useState<Question[]>([])
  const [reasoning, setReasoning] = useState<ReasoningRecord[]>([])
  const [rewards, setRewards] = useState<RewardRecord[]>([])
  const [artifacts, setArtifacts] = useState<Artifact[]>([])

  const [plannerForm, setPlannerForm] = useState({
    name: '',
    rootKeyword: '',
    targetSize: 0,
    strategyId: 0,
    providerId: 0,
    storageProfileId: 0,
  })

  const [providerDraft, setProviderDraft] = useState<ProviderDraft>({
    name: '',
    baseUrl: '',
    model: '',
    providerType: 'openai-compatible',
    reasoningEffort: '',
    maxConcurrency: 4,
    timeoutSeconds: 120,
    isActive: true,
    apiKey: '',
  })
  const [providerSearchKeyword, setProviderSearchKeyword] = useState('')
  const [providerModalVisible, setProviderModalVisible] = useState(false)
  const [providerModels, setProviderModels] = useState<ProviderModelInfo[]>([])
  const [providerModelsLoading, setProviderModelsLoading] = useState(false)
  const [providerTestLoading, setProviderTestLoading] = useState(false)
  const [providerTestResult, setProviderTestResult] = useState<ProviderConnectivityResult | null>(null)
  const [storageSearchKeyword, setStorageSearchKeyword] = useState('')
  const [storageModalVisible, setStorageModalVisible] = useState(false)
  const [storageDraft, setStorageDraft] = useState<StorageDraft>({
    name: '本地 MinIO',
    provider: 'minio',
    endpoint: 'http://minio:9000',
    region: 'us-east-1',
    bucket: 'llm-factory-local',
    accessKeyId: 'minioadmin',
    secretAccessKey: 'minioadmin',
    usePathStyle: true,
    isActive: true,
    isDefault: true,
  })
  const [strategySearchKeyword, setStrategySearchKeyword] = useState('')
  const [strategyModalVisible, setStrategyModalVisible] = useState(false)
  const [strategyDraft, setStrategyDraft] = useState<Partial<Strategy>>({
    name: '',
    description: '',
    domainCount: 1000,
    questionsPerDomain: 10,
    answerVariants: 1,
    rewardVariants: 1,
    planningMode: 'balanced',
    isDefault: true,
  })
  const [promptSearchKeyword, setPromptSearchKeyword] = useState('')
  const [promptModalVisible, setPromptModalVisible] = useState(false)
  const [promptDraft, setPromptDraft] = useState<PromptRecord>({
    name: '',
    stage: 'domain-generation',
    version: 'v1',
    systemPrompt: '',
    userPrompt: '',
    isActive: true,
  })

  const isAdmin = user?.role === 'admin'
  const activeDataset = useMemo(() => datasets.find((item) => item.id === activeDatasetId) ?? graph?.dataset ?? null, [datasets, activeDatasetId, graph?.dataset])
  const visiblePages = useMemo(() => [...userPages, ...(isAdmin ? adminPages : [])], [isAdmin])
  const activeNav = useMemo(
    () => visiblePages.find((page) => location.pathname === page.route || location.pathname.startsWith(`${page.route}/`))?.route ?? '/console/overview',
    [location.pathname, visiblePages],
  )

  useEffect(() => {
    document.body.classList.toggle('sidebar-collapsed', sidebarCollapsed)
    return () => document.body.classList.remove('sidebar-collapsed')
  }, [sidebarCollapsed])

  const makeEmptyProviderDraft = useCallback((): ProviderDraft => ({
    name: '',
    baseUrl: '',
    model: '',
    providerType: 'openai-compatible',
    reasoningEffort: '',
    maxConcurrency: 4,
    timeoutSeconds: 120,
    isActive: true,
    apiKey: '',
  }), [])

  const openCreateProviderModal = useCallback(() => {
    setProviderDraft(makeEmptyProviderDraft())
    setProviderModels([])
    setProviderTestResult(null)
    setProviderModalVisible(true)
  }, [makeEmptyProviderDraft])

  const openEditProviderModal = useCallback((provider: Provider) => {
    setProviderDraft({
      id: provider.id,
      name: provider.name,
      baseUrl: provider.baseUrl,
      model: provider.model,
      providerType: provider.providerType,
      reasoningEffort: provider.reasoningEffort ?? '',
      maxConcurrency: provider.maxConcurrency,
      timeoutSeconds: provider.timeoutSeconds,
      isActive: provider.isActive,
      apiKey: '',
    })
    setProviderModels([])
    setProviderTestResult(null)
    setProviderModalVisible(true)
  }, [])

  const closeProviderModal = useCallback(() => {
    setProviderModalVisible(false)
    setProviderModels([])
    setProviderTestResult(null)
  }, [])

  const makeEmptyStorageDraft = useCallback((): StorageDraft => ({
    name: '本地 MinIO',
    provider: 'minio',
    endpoint: 'http://minio:9000',
    region: 'us-east-1',
    bucket: 'llm-factory-local',
    accessKeyId: 'minioadmin',
    secretAccessKey: 'minioadmin',
    usePathStyle: true,
    isActive: true,
    isDefault: true,
  }), [])

  const openCreateStorageModal = useCallback(() => {
    setStorageDraft(makeEmptyStorageDraft())
    setStorageModalVisible(true)
  }, [makeEmptyStorageDraft])

  const openEditStorageModal = useCallback((storage: StorageProfile) => {
    setStorageDraft({
      id: storage.id,
      name: storage.name,
      provider: storage.provider,
      endpoint: storage.endpoint,
      region: storage.region,
      bucket: storage.bucket,
      accessKeyId: storage.accessKeyId,
      secretAccessKey: '',
      secretKeyMasked: storage.secretKeyMasked,
      usePathStyle: storage.usePathStyle,
      isActive: storage.isActive,
      isDefault: storage.isDefault,
    })
    setStorageModalVisible(true)
  }, [])

  const closeStorageModal = useCallback(() => {
    setStorageModalVisible(false)
  }, [])

  const makeEmptyStrategyDraft = useCallback((): Partial<Strategy> => ({
    name: '',
    description: '',
    domainCount: 1000,
    questionsPerDomain: 10,
    answerVariants: 1,
    rewardVariants: 1,
    planningMode: 'balanced',
    isDefault: true,
  }), [])

  const openCreateStrategyModal = useCallback(() => {
    setStrategyDraft(makeEmptyStrategyDraft())
    setStrategyModalVisible(true)
  }, [makeEmptyStrategyDraft])

  const openEditStrategyModal = useCallback((strategy: Strategy) => {
    setStrategyDraft({
      id: strategy.id,
      name: strategy.name,
      description: strategy.description,
      domainCount: strategy.domainCount,
      questionsPerDomain: strategy.questionsPerDomain,
      answerVariants: strategy.answerVariants,
      rewardVariants: strategy.rewardVariants,
      planningMode: strategy.planningMode,
      isDefault: strategy.isDefault,
    })
    setStrategyModalVisible(true)
  }, [])

  const closeStrategyModal = useCallback(() => {
    setStrategyModalVisible(false)
  }, [])

  const makeEmptyPromptDraft = useCallback((): PromptRecord => ({
    name: '',
    stage: 'domain-generation',
    version: 'v1',
    systemPrompt: '',
    userPrompt: '',
    isActive: true,
  }), [])

  const openCreatePromptModal = useCallback(() => {
    setPromptDraft(makeEmptyPromptDraft())
    setPromptModalVisible(true)
  }, [makeEmptyPromptDraft])

  const openEditPromptModal = useCallback((prompt: PromptRecord) => {
    setPromptDraft({
      id: prompt.id,
      name: prompt.name,
      stage: prompt.stage,
      version: prompt.version,
      systemPrompt: prompt.systemPrompt,
      userPrompt: prompt.userPrompt,
      isActive: prompt.isActive,
    })
    setPromptModalVisible(true)
  }, [])

  const closePromptModal = useCallback(() => {
    setPromptModalVisible(false)
  }, [])

  const loadAdminData = useCallback(async () => {
    if (!isAdmin) {
      setDashboard(null)
      setPrompts([])
      setAuditLogs([])
      return
    }
    const [dashboardData, promptData, auditData] = await Promise.all([
      consoleApi.dashboard(),
      consoleApi.listPrompts(),
      consoleApi.listAuditLogs(),
    ])
    setDashboard(dashboardData)
    setPrompts(promptData)
    setAuditLogs(auditData)
  }, [isAdmin])

  const loadBootstrap = useCallback(async (successMessage?: string) => {
    setBusy(true)
    try {
      const [providerData, storageData, strategyData, datasetData, runtimeData] = await Promise.all([
        consoleApi.listProviders(),
        consoleApi.listStorageProfiles(),
        consoleApi.listStrategies(),
        consoleApi.listDatasets(),
        consoleApi.runtimeStatus(),
      ])
      setProviders(providerData)
      setStorageProfiles(storageData)
      setStrategies(strategyData)
      setDatasets(datasetData)
      setRuntime(runtimeData)
      setPlannerForm((current) => ({
        ...current,
        strategyId: current.strategyId || strategyData[0]?.id || 0,
        providerId: current.providerId || providerData[0]?.id || 0,
        storageProfileId: current.storageProfileId || storageData.find((item) => item.isActive)?.id || storageData[0]?.id || 0,
      }))
      if (!activeDatasetId && datasetData[0]?.id) {
        setActiveDatasetId(datasetData[0].id)
      }
      await loadAdminData()
      if (successMessage) Toast.success(successMessage)
    } catch (error) {
      Toast.error((error as Error).message)
    } finally {
      setBusy(false)
    }
  }, [activeDatasetId, loadAdminData])

  const loadDatasetWorkspace = useCallback(async (datasetId: number, successMessage?: string) => {
    setBusy(true)
    try {
      const [nextGraph, nextQuestions, nextReasoning, nextRewards, nextArtifacts, nextRuntime, nextDatasets] = await Promise.all([
        consoleApi.getDataset(datasetId),
        consoleApi.listQuestions(datasetId),
        consoleApi.listReasoning(datasetId),
        consoleApi.listRewards(datasetId),
        consoleApi.listArtifacts(datasetId),
        consoleApi.runtimeStatus(),
        consoleApi.listDatasets(),
      ])
      setGraph(nextGraph)
      setQuestions(nextQuestions)
      setReasoning(nextReasoning)
      setRewards(nextRewards)
      setArtifacts(nextArtifacts)
      setRuntime(nextRuntime)
      setDatasets(nextDatasets)
      setActiveDatasetId(datasetId)
      if (successMessage) Toast.success(successMessage)
    } catch (error) {
      Toast.error((error as Error).message)
    } finally {
      setBusy(false)
    }
  }, [])

  useEffect(() => {
    let active = true
    void (async () => {
      try {
        const result = await authApi.me()
        if (!active) return
        setUser(result.user)
      } catch {
        if (!active) return
        setUser(null)
      } finally {
        if (active) setSessionLoading(false)
      }
    })()
    return () => {
      active = false
    }
  }, [])

  useEffect(() => {
    if (!user) return
    void loadBootstrap()
  }, [user, loadBootstrap])

  useEffect(() => {
    if (!user || !activeDatasetId) return
    void loadDatasetWorkspace(activeDatasetId)
  }, [user, activeDatasetId, loadDatasetWorkspace])

  useEffect(() => {
    if (sessionLoading) return
    if (!user && location.pathname !== '/login') {
      navigate('/login', { replace: true })
      return
    }
    if (user && location.pathname === '/login') {
      navigate('/console/overview', { replace: true })
      return
    }
    if (user && !isAdmin && location.pathname.startsWith('/console/admin/')) {
      navigate('/console/overview', { replace: true })
    }
  }, [isAdmin, location.pathname, navigate, sessionLoading, user])

  const handleLogin = async (email: string, password: string) => {
    setAuthSubmitting(true)
    try {
      const result = await authApi.login({ email, password })
      setUser(result.user)
      Toast.success(`欢迎回来，${result.user.email}`)
      navigate('/console/overview', { replace: true })
    } catch (error) {
      Toast.error((error as Error).message)
    } finally {
      setAuthSubmitting(false)
    }
  }

  const handleLogout = async () => {
    try {
      await authApi.logout()
    } finally {
      setUser(null)
      setGraph(null)
      setQuestions([])
      setReasoning([])
      setRewards([])
      setArtifacts([])
      navigate('/login', { replace: true })
    }
  }

  const estimatePlan = async () => {
    setBusy(true)
    try {
      const data = await consoleApi.estimatePlan({
        rootKeyword: plannerForm.rootKeyword,
        targetSize: Number(plannerForm.targetSize),
        strategyId: Number(plannerForm.strategyId),
      })
      setEstimate(data)
      Toast.success('计划估算已刷新')
    } catch (error) {
      Toast.error((error as Error).message)
    } finally {
      setBusy(false)
    }
  }

  const createDataset = async () => {
    if (!estimate) {
      Toast.warning('请先完成计划估算')
      return
    }
    setBusy(true)
    try {
      const created = await consoleApi.createDataset({
        name: plannerForm.name || `${plannerForm.rootKeyword} 数据集`,
        rootKeyword: plannerForm.rootKeyword,
        targetSize: Number(plannerForm.targetSize),
        strategyId: Number(plannerForm.strategyId),
        providerId: Number(plannerForm.providerId),
        storageProfileId: Number(plannerForm.storageProfileId),
        status: 'draft',
        estimate,
      })
      setActiveDatasetId(created.id)
      navigate('/console/domains')
      Toast.success('数据集已创建')
      await loadBootstrap()
    } catch (error) {
      Toast.error((error as Error).message)
    } finally {
      setBusy(false)
    }
  }

  const generateDomains = async () => {
    if (!activeDatasetId) return Toast.warning('请先选择数据集')
    setBusy(true)
    try {
      const nextGraph = await consoleApi.generateDomains(activeDatasetId)
      setGraph(nextGraph)
      Toast.success(`已生成 ${nextGraph.domains.length} 个领域`)
    } catch (error) {
      Toast.error((error as Error).message)
    } finally {
      setBusy(false)
    }
  }

  const saveGraph = async () => {
    if (!graph) return
    setBusy(true)
    try {
      await consoleApi.updateGraph(graph.dataset.id, graph.domains)
      Toast.success('图谱命名修改已保存')
    } catch (error) {
      Toast.error((error as Error).message)
    } finally {
      setBusy(false)
    }
  }

  const confirmDomains = async () => {
    if (!graph) return
    setBusy(true)
    try {
      await consoleApi.confirmDomains(graph.dataset.id)
      Toast.success('领域已确认')
      navigate('/console/questions')
      await loadDatasetWorkspace(graph.dataset.id)
    } catch (error) {
      Toast.error((error as Error).message)
    } finally {
      setBusy(false)
    }
  }

  const generateQuestions = async () => {
    if (!activeDatasetId) return
    setBusy(true)
    try {
      await consoleApi.generateQuestions(activeDatasetId)
      Toast.success('问题生成任务已入队')
    } catch (error) {
      Toast.error((error as Error).message)
    } finally {
      setBusy(false)
    }
  }

  const generateReasoning = async () => {
    if (!activeDatasetId) return
    setBusy(true)
    try {
      await consoleApi.generateReasoning(activeDatasetId)
      Toast.success('推理生成任务已入队')
    } catch (error) {
      Toast.error((error as Error).message)
    } finally {
      setBusy(false)
    }
  }

  const generateRewards = async () => {
    if (!activeDatasetId) return
    setBusy(true)
    try {
      await consoleApi.generateRewards(activeDatasetId)
      Toast.success('奖励数据生成任务已入队')
    } catch (error) {
      Toast.error((error as Error).message)
    } finally {
      setBusy(false)
    }
  }

  const generateExport = async () => {
    if (!activeDatasetId) return
    setBusy(true)
    try {
      await consoleApi.generateExport(activeDatasetId)
      Toast.success('导出任务已入队')
    } catch (error) {
      Toast.error((error as Error).message)
    } finally {
      setBusy(false)
    }
  }

  const saveProvider = async () => {
    setBusy(true)
    try {
      await consoleApi.saveProvider(providerDraft)
      Toast.success('模型提供方已保存')
      setProviderDraft(makeEmptyProviderDraft())
      setProviderModels([])
      setProviderTestResult(null)
      setProviderModalVisible(false)
      await loadBootstrap()
    } catch (error) {
      Toast.error((error as Error).message)
    } finally {
      setBusy(false)
    }
  }

  const validateProviderDraftForRemoteAction = () => {
    const providerType = String(providerDraft.providerType ?? '').trim()
    const baseURL = String(providerDraft.baseUrl ?? '').trim()

    if (!providerType) {
      Toast.warning('请先选择或填写提供方类型')
      return false
    }

    if (providerType !== 'mock' && !baseURL) {
      Toast.warning('请先填写基础 URL，再获取模型列表或执行连通性测试')
      return false
    }

    return true
  }

  const fetchProviderModels = async () => {
    if (!validateProviderDraftForRemoteAction()) {
      return
    }
    setProviderModelsLoading(true)
    try {
      const response = await consoleApi.fetchProviderModels(providerDraft)
      setProviderModels(response.models)
      if (!providerDraft.model && response.models[0]?.id) {
        setProviderDraft((current) => ({ ...current, model: response.models[0]?.id }))
      }
      Toast.success(`已获取 ${response.models.length} 个模型`)
    } catch (error) {
      Toast.error((error as Error).message)
    } finally {
      setProviderModelsLoading(false)
    }
  }

  const testProviderConnectivity = async () => {
    if (!validateProviderDraftForRemoteAction()) {
      return
    }
    setProviderTestLoading(true)
    try {
      const result = await consoleApi.testProviderConnectivity(providerDraft)
      setProviderTestResult(result)
      if (result.availableModels?.length) {
        setProviderModels(result.availableModels.map((id) => ({ id })))
      }
      Toast[result.ok ? 'success' : 'warning'](result.message)
    } catch (error) {
      setProviderTestResult(null)
      Toast.error((error as Error).message)
    } finally {
      setProviderTestLoading(false)
    }
  }

  const saveStorage = async () => {
    setBusy(true)
    try {
      await consoleApi.saveStorageProfile(storageDraft)
      Toast.success('存储配置已保存')
      setStorageDraft(makeEmptyStorageDraft())
      setStorageModalVisible(false)
      await loadBootstrap()
    } catch (error) {
      Toast.error((error as Error).message)
    } finally {
      setBusy(false)
    }
  }

  const saveStrategy = async () => {
    setBusy(true)
    try {
      await consoleApi.saveStrategy(strategyDraft)
      Toast.success('生成策略已保存')
      setStrategyDraft(makeEmptyStrategyDraft())
      setStrategyModalVisible(false)
      await loadBootstrap()
    } catch (error) {
      Toast.error((error as Error).message)
    } finally {
      setBusy(false)
    }
  }

  const savePrompt = async () => {
    setBusy(true)
    try {
      await consoleApi.savePrompt(promptDraft)
      Toast.success('提示词模板已保存')
      setPromptDraft(makeEmptyPromptDraft())
      setPromptModalVisible(false)
      await loadBootstrap()
    } catch (error) {
      Toast.error((error as Error).message)
    } finally {
      setBusy(false)
    }
  }

  const providerColumns = useMemo(
    () => [
      { title: '名称', dataIndex: 'name' },
      { title: '基础 URL', dataIndex: 'baseUrl' },
      { title: '模型', dataIndex: 'model' },
      { title: '类型', dataIndex: 'providerType', render: (value: string) => <Tag color="blue">{value}</Tag> },
      { title: '推理强度', dataIndex: 'reasoningEffort', render: (value: string) => value ? <Tag color="purple">{value}</Tag> : '默认' },
      { title: '状态', dataIndex: 'isActive', render: (value: boolean) => <Tag color={value ? 'green' : 'grey'}>{value ? '启用' : '停用'}</Tag> },
      {
        title: '操作',
        dataIndex: 'operate',
        render: (_: unknown, record: Provider) => (
          <Space>
            <Button size="small" onClick={() => openEditProviderModal(record)}>编辑</Button>
          </Space>
        ),
      },
    ],
    [openEditProviderModal],
  )
  const storageColumns = useMemo(
    () => [
      { title: '名称', dataIndex: 'name' },
      { title: '提供方', dataIndex: 'provider', render: (value: string) => <Tag color="cyan">{value}</Tag> },
      { title: '端点', dataIndex: 'endpoint' },
      { title: '存储桶', dataIndex: 'bucket' },
      { title: '启用', dataIndex: 'isActive', render: (value: boolean) => <Tag color={value ? 'green' : 'grey'}>{value ? '启用' : '停用'}</Tag> },
      { title: '默认', dataIndex: 'isDefault', render: (value: boolean) => <Tag color={value ? 'green' : 'grey'}>{value ? '是' : '否'}</Tag> },
      {
        title: '操作',
        dataIndex: 'operate',
        render: (_: unknown, record: StorageProfile) => (
          <Space>
            <Button size="small" onClick={() => openEditStorageModal(record)}>编辑</Button>
          </Space>
        ),
      },
    ],
    [openEditStorageModal],
  )
  const strategyColumns = useMemo(
    () => [
      { title: '名称', dataIndex: 'name' },
      { title: '模式', dataIndex: 'planningMode', render: (value: string) => <Tag color="purple">{value}</Tag> },
      { title: '领域数', dataIndex: 'domainCount' },
      { title: '每领域问题数', dataIndex: 'questionsPerDomain' },
      { title: '答案变体', dataIndex: 'answerVariants' },
      { title: '奖励变体', dataIndex: 'rewardVariants' },
      {
        title: '操作',
        dataIndex: 'operate',
        render: (_: unknown, record: Strategy) => (
          <Space>
            <Button size="small" onClick={() => openEditStrategyModal(record)}>编辑</Button>
          </Space>
        ),
      },
    ],
    [openEditStrategyModal],
  )
  const promptColumns = useMemo(
    () => [
      { title: '名称', dataIndex: 'name' },
      { title: '阶段', dataIndex: 'stage', render: (value: string) => <Tag color="amber">{value}</Tag> },
      { title: '版本', dataIndex: 'version' },
      { title: '状态', dataIndex: 'isActive', render: (value: boolean) => <Tag color={value ? 'green' : 'grey'}>{value ? '启用' : '停用'}</Tag> },
      {
        title: '操作',
        dataIndex: 'operate',
        render: (_: unknown, record: PromptRecord) => (
          <Space>
            <Button size="small" onClick={() => openEditPromptModal(record)}>编辑</Button>
          </Space>
        ),
      },
    ],
    [openEditPromptModal],
  )
  const auditColumns = useMemo(
    () => [
      { title: '操作人', dataIndex: 'actor' },
      { title: '操作', dataIndex: 'action' },
      { title: '资源', dataIndex: 'resourceType' },
      { title: '详情', dataIndex: 'detail' },
      { title: '时间', dataIndex: 'createdAt' },
    ],
    [],
  )

  const overviewCards = [
    { icon: Database, label: '数据集', value: runtime?.datasetCount ?? 0, helper: '当前平台管理中的数据集数量' },
    { icon: Layers3, label: '问题', value: runtime?.questionCount ?? 0, helper: '已经入库的问题样本总数' },
    { icon: BrainCircuit, label: '推理', value: runtime?.reasoningCount ?? 0, helper: '长思维链与答案记录总数' },
    { icon: HardDriveDownload, label: '工件', value: runtime?.artifactCount ?? 0, helper: '已经打包完成的导出工件' },
  ]

  const planningCards = estimate
    ? [
        { icon: Network, label: '领域数', value: estimate.domainCount, helper: '建议生成的一级领域数量' },
        { icon: Layers3, label: '每领域问题数', value: estimate.questionsPerDomain, helper: '单领域建议问题量' },
        { icon: BrainCircuit, label: '预计问题总量', value: estimate.estimatedQuestions, helper: '将被送入问题队列的数量' },
        { icon: FileOutput, label: '预计样本总量', value: estimate.estimatedSamples, helper: '最终训练样本总规模' },
      ] 
    : []

  const providerRemoteActionDisabled = (() => {
    const providerType = String(providerDraft.providerType ?? '').trim()
    const baseURL = String(providerDraft.baseUrl ?? '').trim()
    if (!providerType) {
      return true
    }
    if (providerType === 'mock') {
      return false
    }
    return !baseURL
  })()

  const filteredProviders = useMemo(() => {
    const keyword = providerSearchKeyword.trim().toLowerCase()
    if (!keyword) {
      return providers
    }
    return providers.filter((provider) =>
      [
        provider.name,
        provider.baseUrl,
        provider.model,
        provider.providerType,
        provider.reasoningEffort,
      ]
        .filter(Boolean)
        .some((value) => String(value).toLowerCase().includes(keyword)),
    )
  }, [providerSearchKeyword, providers])

  const activeStorageProfiles = useMemo(
    () => storageProfiles.filter((item) => item.isActive),
    [storageProfiles],
  )
  const filteredStorageProfiles = useMemo(() => {
    const keyword = storageSearchKeyword.trim().toLowerCase()
    if (!keyword) {
      return storageProfiles
    }
    return storageProfiles.filter((profile) =>
      [
        profile.name,
        profile.provider,
        profile.endpoint,
        profile.region,
        profile.bucket,
      ]
        .filter(Boolean)
        .some((value) => String(value).toLowerCase().includes(keyword)),
    )
  }, [storageProfiles, storageSearchKeyword])
  const filteredStrategies = useMemo(() => {
    const keyword = strategySearchKeyword.trim().toLowerCase()
    if (!keyword) {
      return strategies
    }
    return strategies.filter((strategy) =>
      [
        strategy.name,
        strategy.description,
        strategy.planningMode,
      ]
        .filter(Boolean)
        .some((value) => String(value).toLowerCase().includes(keyword)),
    )
  }, [strategies, strategySearchKeyword])
  const filteredPrompts = useMemo(() => {
    const keyword = promptSearchKeyword.trim().toLowerCase()
    if (!keyword) {
      return prompts
    }
    return prompts.filter((prompt) =>
      [
        prompt.name,
        prompt.stage,
        prompt.version,
      ]
        .filter(Boolean)
        .some((value) => String(value).toLowerCase().includes(keyword)),
    )
  }, [prompts, promptSearchKeyword])

  const renderOverview = () => (
    <div className="console-page-shell">
      <PageHeader
        badge="统一控制台 / 总览"
        title="一个界面同时覆盖用户链路与管理员治理"
        description="当前前端已按 /root/new-api 的页面结构重构为固定头部、左侧导航、右侧分页工作区；管理员登录后自动拥有全部用户链路与系统配置能力。"
        actions={
          <>
            <Button theme="solid" type="primary" loading={busy} icon={<RefreshCw size={16} />} onClick={() => void loadBootstrap('控制台数据已刷新')}>
              刷新数据
            </Button>
            <Button icon={<Target size={16} />} onClick={() => navigate('/console/planning')}>开始计划编排</Button>
          </>
        }
      />

      <Banner type="info" icon={<Bell size={16} />} description={`当前登录角色：${isAdmin ? '管理员' : '普通用户'}。${isAdmin ? '你可以使用全部用户功能并配置系统。' : '你可以使用数据集生产链路。'}`} />

      <div className="console-card-grid-4">
        {overviewCards.map((item) => <StatCard key={item.label} {...item} />)}
      </div>

      <div className="console-card-grid-2">
        <Card className="console-panel" bodyStyle={{ padding: 20 }}>
          <Title heading={4} className="!mb-0">当前活动数据集</Title>
          <Text className="mt-2 block console-caption">总览页会持续显示当前任务上下文，避免在新结构下迷失工作流位置。</Text>
          <div className="mt-5 console-summary-grid">
            <div className="console-summary-row"><span>数据集名称</span><Text strong>{activeDataset?.name ?? '尚未选择'}</Text></div>
            <div className="console-summary-row"><span>根关键词</span><Text strong>{activeDataset?.rootKeyword ?? '—'}</Text></div>
            <div className="console-summary-row"><span>状态</span><Text strong>{activeDataset ? statusLabel(activeDataset.status) : '—'}</Text></div>
            <div className="console-summary-row"><span>预计样本</span><Text strong>{activeDataset?.estimate.estimatedSamples ?? '—'}</Text></div>
          </div>
        </Card>

        <Card className="console-panel" bodyStyle={{ padding: 20 }}>
          <Title heading={4} className="!mb-0">快速入口</Title>
          <Text className="mt-2 block console-caption">管理员与用户都从同一个前端进入；管理员额外看到系统治理导航。</Text>
          <div className="console-card-grid-2 mt-5">
            {[...userPages.slice(1, 4), ...(isAdmin ? adminPages.slice(0, 1) : [])].map((page) => (
              <Card key={page.route} className="console-quick-card" bodyStyle={{ padding: 18 }}>
                <div className="cursor-pointer" onClick={() => navigate(page.route)}>
                  <div className="quick-page-icon"><page.icon size={18} strokeWidth={1.9} /></div>
                  <Title heading={6} className="!mb-0 mt-4">{page.label}</Title>
                  <Text className="mt-2 block console-caption">{page.caption}</Text>
                </div>
              </Card>
            ))}
          </div>
        </Card>
      </div>

      <Card className="console-panel" bodyStyle={{ padding: 20 }}>
        <Title heading={4} className="!mb-0">最近的数据集</Title>
        <Text className="mt-2 block console-caption">从统一控制台直接切换任务，不再在用户端与管理端之间跳转。</Text>
        {datasets.length > 0 ? (
          <div className="console-card-grid-3 mt-5">
            {datasets.map((dataset) => (
              <Card key={dataset.id} className={clsx('console-quick-card', { 'border-[var(--semi-color-primary)]': dataset.id === activeDatasetId })} bodyStyle={{ padding: 18 }}>
                <div className="cursor-pointer" onClick={() => void loadDatasetWorkspace(dataset.id, `已切换到数据集「${dataset.name}」`)}>
                  <Space vertical align="start" spacing="medium" style={{ width: '100%' }}>
                    <Tag color="blue">{statusLabel(dataset.status)}</Tag>
                    <Title heading={6} className="!mb-0">{dataset.name}</Title>
                    <Text className="console-caption">{dataset.rootKeyword} · 预计 {dataset.estimate.estimatedSamples} 个样本</Text>
                    <Text className="console-caption">更新于 {formatTime(dataset.updatedAt)}</Text>
                  </Space>
                </div>
              </Card>
            ))}
          </div>
        ) : (
          <EmptyCard title="暂无数据集" description="先进入计划编排页创建你的第一个数据集任务。" />
        )}
      </Card>
    </div>
  )

  const renderPlanning = () => (
    <div className="console-page-shell">
      <PageHeader
        badge="用户链路 / 计划编排"
        title="配置目标规模、策略、模型与存储"
        description="普通用户和管理员在同一页完成数据集编排；管理员只是在左侧额外看到系统治理导航。"
        actions={
          <>
            <Button icon={<RefreshCw size={16} />} loading={busy} onClick={() => void loadBootstrap('系统配置与数据集已刷新')}>同步配置</Button>
            <Button theme="solid" type="primary" icon={<Target size={16} />} loading={busy} onClick={() => void estimatePlan()}>更新估算</Button>
          </>
        }
      />

      <div className="console-card-grid-2">
        <Card className="console-panel" bodyStyle={{ padding: 20 }}>
          <Title heading={4} className="!mb-0">数据集创建表单</Title>
          <Text className="mt-2 block console-caption">策略、模型提供方和存储配置来自管理员预置的系统配置。</Text>
          <div className="console-card-grid-2 mt-5">
            <div>
              <Text className="mb-2 block font-medium">数据集名称</Text>
              <Input value={plannerForm.name} onChange={(value) => setPlannerForm((current) => ({ ...current, name: value }))} placeholder="军事长链思考训练集" />
            </div>
            <div>
              <Text className="mb-2 block font-medium">根关键词</Text>
              <Input value={plannerForm.rootKeyword} onChange={(value) => setPlannerForm((current) => ({ ...current, rootKeyword: value }))} />
            </div>
            <div>
              <Text className="mb-2 block font-medium">目标规模</Text>
              <InputNumber value={plannerForm.targetSize} onChange={(value) => setPlannerForm((current) => ({ ...current, targetSize: Number(value ?? 0) }))} style={{ width: '100%' }} />
            </div>
            <div>
              <Text className="mb-2 block font-medium">生成策略</Text>
              <Select value={plannerForm.strategyId} optionList={strategies.map((item) => ({ value: item.id, label: item.name }))} onChange={(value) => setPlannerForm((current) => ({ ...current, strategyId: Number(value) }))} style={{ width: '100%' }} />
            </div>
            <div>
              <Text className="mb-2 block font-medium">模型提供方</Text>
              <Select value={plannerForm.providerId} optionList={providers.map((item) => ({ value: item.id, label: item.name }))} onChange={(value) => setPlannerForm((current) => ({ ...current, providerId: Number(value) }))} style={{ width: '100%' }} />
            </div>
            <div>
              <Text className="mb-2 block font-medium">存储配置</Text>
              <Select value={plannerForm.storageProfileId} optionList={activeStorageProfiles.map((item) => ({ value: item.id, label: item.name }))} onChange={(value) => setPlannerForm((current) => ({ ...current, storageProfileId: Number(value) }))} style={{ width: '100%' }} />
            </div>
          </div>
          <Space className="mt-6" spacing="medium">
            <Button icon={<Target size={16} />} loading={busy} onClick={() => void estimatePlan()}>先做估算</Button>
            <Button theme="solid" type="primary" icon={<FileOutput size={16} />} loading={busy} onClick={() => void createDataset()}>创建数据集</Button>
          </Space>
        </Card>

        <Card className="console-panel" bodyStyle={{ padding: 20 }}>
          <Title heading={4} className="!mb-0">估算结果</Title>
          <Text className="mt-2 block console-caption">根据目标规模反推领域规模与问题量，作为后续生产链路的前置依据。</Text>
          {planningCards.length > 0 ? (
            <div className="console-card-grid-2 mt-5">
              {planningCards.map((item) => <StatCard key={item.label} {...item} />)}
            </div>
          ) : (
            <EmptyCard title="尚未生成估算" description="填写参数后点击“更新估算”，再决定是否创建数据集。" />
          )}
        </Card>
      </div>
    </div>
  )

  const renderDomains = () => (
    <div className="console-page-shell">
      <PageHeader
        badge="用户链路 / 领域图谱"
        title="生成、修订并确认领域结构"
        description="这一页直接对齐 new-api 的控制台内容布局：中心画布展示结构、侧边展示上下文、下方承载治理编辑。"
        actions={
          <>
            <Button icon={<RefreshCw size={16} />} loading={busy} onClick={() => activeDatasetId && void loadDatasetWorkspace(activeDatasetId, '图谱数据已刷新')}>刷新图谱</Button>
            <Button theme="solid" type="primary" icon={<GitBranch size={16} />} loading={busy} onClick={() => void generateDomains()}>生成领域</Button>
          </>
        }
      />

      <div className="console-card-grid-2">
        <Card className="console-panel" bodyStyle={{ padding: 20 }}>
          <Title heading={4} className="!mb-0">领域图谱主画布</Title>
          <Text className="mt-2 block console-caption">确认领域前建议先保存命名修订，再推进问题生成。</Text>
          {graph ? (
            <div className="mt-5 console-stack">
              <GraphPreview rootKeyword={graph.dataset.rootKeyword} domains={graph.domains} />
              <Space>
                <Button icon={<RefreshCw size={16} />} loading={busy} onClick={() => void saveGraph()}>保存编辑</Button>
                <Button theme="solid" type="primary" loading={busy} onClick={() => void confirmDomains()}>确认领域</Button>
              </Space>
            </div>
          ) : (
            <EmptyCard title="尚未加载领域图谱" description="先创建数据集，再点击“生成领域”。" />
          )}
        </Card>

        <Card className="console-focus-card" bodyStyle={{ padding: 20 }}>
          <Title heading={4} className="!mb-0">当前任务上下文</Title>
          <Text className="mt-2 block console-caption">管理员和普通用户都在同一条生产链路里推进当前数据集。</Text>
          <div className="mt-5 console-summary-grid">
            <div className="console-summary-row"><span>数据集</span><Text strong>{activeDataset?.name ?? '未选择'}</Text></div>
            <div className="console-summary-row"><span>根关键词</span><Text strong>{activeDataset?.rootKeyword ?? '—'}</Text></div>
            <div className="console-summary-row"><span>当前状态</span><Text strong>{activeDataset ? statusLabel(activeDataset.status) : '—'}</Text></div>
            <div className="console-summary-row"><span>领域数量</span><Text strong>{graph?.domains.length ?? 0}</Text></div>
          </div>
        </Card>
      </div>

      <Card className="console-panel" bodyStyle={{ padding: 20 }}>
        <Title heading={4} className="!mb-0">领域命名治理</Title>
        <Text className="mt-2 block console-caption">当前界面展示前 24 个领域进行人工修订；管理员不再需要切到另一个前端才能回看系统配置。</Text>
        {graph ? (
          <div className="console-card-grid-2 mt-5">
            {graph.domains.slice(0, 24).map((domain) => (
              <div key={domain.id} className="console-domain-item">
                <Text className="console-caption">{sourceLabel(domain.source)}</Text>
                <Input value={domain.name} onChange={(value) => setGraph((current) => current ? { ...current, domains: current.domains.map((item) => item.id === domain.id ? { ...item, name: value } : item) } : current)} />
              </div>
            ))}
          </div>
        ) : (
          <EmptyCard title="暂无领域列表" description="生成领域后，这里会显示可编辑的领域列表。" />
        )}
      </Card>
    </div>
  )

  const renderRecordPage = ({
    badge,
    title,
    description,
    actionLabel,
    onGenerate,
    onRefresh,
    records,
    emptyTitle,
    emptyDescription,
    renderRecord,
  }: {
    badge: string
    title: string
    description: string
    actionLabel: string
    onGenerate: () => Promise<void>
    onRefresh: () => Promise<void>
    records: Array<Question | ReasoningRecord | RewardRecord | Artifact>
    emptyTitle: string
    emptyDescription: string
    renderRecord: (record: any) => React.ReactNode
  }) => (
    <div className="console-page-shell">
      <PageHeader
        badge={badge}
        title={title}
        description={description}
        actions={
          <>
            <Button icon={<RefreshCw size={16} />} loading={busy} onClick={() => void onRefresh()}>刷新结果</Button>
            <Button theme="solid" type="primary" loading={busy} onClick={() => void onGenerate()}>{actionLabel}</Button>
          </>
        }
      />
      <div className="console-card-grid-2">
        <Card className="console-record-card" bodyStyle={{ padding: 20 }}>
          <Title heading={4} className="!mb-0">结果预览</Title>
          {records.length > 0 ? <div className="console-record-list mt-5">{records.map(renderRecord)}</div> : <EmptyCard title={emptyTitle} description={emptyDescription} />}
        </Card>
        <Card className="console-focus-card" bodyStyle={{ padding: 20 }}>
          <Title heading={4} className="!mb-0">当前阶段信号</Title>
          <div className="mt-5 console-summary-grid">
            <div className="console-summary-row"><span>活动数据集</span><Text strong>{activeDataset?.name ?? '未选择'}</Text></div>
            <div className="console-summary-row"><span>问题数量</span><Text strong>{questions.length}</Text></div>
            <div className="console-summary-row"><span>推理数量</span><Text strong>{reasoning.length}</Text></div>
            <div className="console-summary-row"><span>奖励数量</span><Text strong>{rewards.length}</Text></div>
            <div className="console-summary-row"><span>工件数量</span><Text strong>{artifacts.length}</Text></div>
            <div className="console-summary-row"><span>队列深度</span><Text strong>{runtime?.queueDepth ?? 0}</Text></div>
          </div>
        </Card>
      </div>
    </div>
  )

  const renderProviders = () => (
    <div className="console-page-shell">
      <PageHeader
        badge="管理员治理 / 模型提供方"
        title="渠道式管理模型提供方"
        description="这一页改成更接近 new-api 渠道管理的工作方式：先看列表，再通过弹窗编辑配置、获取模型列表、执行连通性测试。"
        actions={
          <Space wrap>
            <Button icon={<RefreshCw size={16} />} loading={busy} onClick={() => void loadBootstrap('系统配置已刷新')}>刷新配置</Button>
            <Button theme="solid" type="primary" onClick={() => openCreateProviderModal()}>新增提供方</Button>
          </Space>
        }
      />
      <Card className="console-panel" bodyStyle={{ padding: 20 }}>
        <div className="flex flex-col gap-4 lg:flex-row lg:items-center lg:justify-between">
          <div>
            <Title heading={4} className="!mb-0">模型提供方台账</Title>
            <Text className="mt-2 block console-caption">直接参考 new-api 渠道管理页的操作习惯：搜索、列表、弹窗编辑、弹窗内拉模型和测试连通性。</Text>
          </div>
          <Input
            value={providerSearchKeyword}
            onChange={setProviderSearchKeyword}
            placeholder="搜索提供方名称、地址、模型或推理强度"
            style={{ width: 360, maxWidth: '100%' }}
          />
        </div>
        <div className="mt-5">
          <Table columns={providerColumns} dataSource={filteredProviders} pagination={false} />
        </div>
      </Card>

      <Modal
        title={providerDraft.id ? '编辑模型提供方' : '新增模型提供方'}
        visible={providerModalVisible}
        onCancel={closeProviderModal}
        footer={
          <Space>
            <Button onClick={closeProviderModal}>取消</Button>
            <Button disabled={providerRemoteActionDisabled} loading={providerModelsLoading} onClick={() => void fetchProviderModels()}>获取模型列表</Button>
            <Button disabled={providerRemoteActionDisabled} loading={providerTestLoading} onClick={() => void testProviderConnectivity()}>模型连通性测试</Button>
            <Button theme="solid" type="primary" loading={busy} onClick={() => void saveProvider()}>保存</Button>
          </Space>
        }
        width={920}
      >
        <div className="console-stack">
          <div className="console-card-grid-2">
            <div>
              <Text className="mb-2 block font-medium">提供方名称</Text>
              <Input value={providerDraft.name ?? ''} onChange={(value) => setProviderDraft((current) => ({ ...current, name: value }))} placeholder="例如 OpenAI 生产网关" />
            </div>
            <div>
              <Text className="mb-2 block font-medium">协议类型</Text>
              <Select
                value={providerDraft.providerType ?? 'openai-compatible'}
                optionList={[
                  { value: 'openai-compatible', label: 'OpenAI Compatible' },
                  { value: 'custom', label: 'Custom Compatible' },
                ]}
                onChange={(value) => setProviderDraft((current) => ({ ...current, providerType: String(value ?? '') }))}
                style={{ width: '100%' }}
              />
            </div>
          </div>

          <div className="console-card-grid-2">
            <div>
              <Text className="mb-2 block font-medium">基础 URL</Text>
              <Input value={providerDraft.baseUrl ?? ''} onChange={(value) => setProviderDraft((current) => ({ ...current, baseUrl: value }))} placeholder="例如 https://api.openai.com/v1" />
            </div>
            <div>
              <Text className="mb-2 block font-medium">模型名称</Text>
              <Input value={providerDraft.model ?? ''} onChange={(value) => setProviderDraft((current) => ({ ...current, model: value }))} placeholder="可先获取模型列表再选择" />
            </div>
          </div>

          <div className="console-card-grid-2">
            <div>
              <Text className="mb-2 block font-medium">最大并发数</Text>
              <InputNumber value={providerDraft.maxConcurrency ?? 4} onChange={(value) => setProviderDraft((current) => ({ ...current, maxConcurrency: Number(value ?? 0) }))} style={{ width: '100%' }} />
            </div>
            <div>
              <Text className="mb-2 block font-medium">超时秒数</Text>
              <InputNumber value={providerDraft.timeoutSeconds ?? 120} onChange={(value) => setProviderDraft((current) => ({ ...current, timeoutSeconds: Number(value ?? 0) }))} style={{ width: '100%' }} />
            </div>
          </div>

          <div className="console-card-grid-2">
            <div>
              <Text className="mb-2 block font-medium">推理强度</Text>
              <Select
                value={providerDraft.reasoningEffort ?? ''}
                placeholder="推理强度（可选）"
                optionList={[
                  { value: '', label: '默认' },
                  { value: 'low', label: '低' },
                  { value: 'medium', label: '中' },
                  { value: 'high', label: '高' },
                  { value: 'xhigh', label: '超高' },
                ]}
                onChange={(value) => setProviderDraft((current) => ({ ...current, reasoningEffort: String(value ?? '') }))}
                style={{ width: '100%' }}
              />
            </div>
            <div>
              <Text className="mb-2 block font-medium">启用状态</Text>
              <div className="flex h-10 items-center rounded-[16px] border border-[var(--semi-color-border)] px-4">
                <Switch checked={providerDraft.isActive ?? true} onChange={(checked) => setProviderDraft((current) => ({ ...current, isActive: checked }))} />
              </div>
            </div>
          </div>

          <div>
            <Text className="mb-2 block font-medium">API Key</Text>
            <Input value={providerDraft.apiKey ?? ''} onChange={(value) => setProviderDraft((current) => ({ ...current, apiKey: value }))} placeholder="保存时会加密；留空则沿用已有密钥" mode="password" />
          </div>

          {providerRemoteActionDisabled ? <Text className="console-caption">请先填写基础 URL，再获取模型列表或执行连通性测试。</Text> : null}

          {providerModels.length > 0 ? (
            <Card className="console-toolbar-card" bodyStyle={{ padding: 16 }}>
              <Text strong>可用模型列表</Text>
              <div className="mt-3 flex flex-wrap gap-2">
                {providerModels.map((item) => (
                  <Tag key={item.id} color={providerDraft.model === item.id ? 'green' : 'grey'} onClick={() => setProviderDraft((current) => ({ ...current, model: item.id }))}>
                    {item.id}
                  </Tag>
                ))}
              </div>
            </Card>
          ) : null}

          {providerTestResult ? (
            <Banner
              type={providerTestResult.ok ? 'success' : 'warning'}
              description={`${providerTestResult.message} · HTTP ${providerTestResult.statusCode || 0} · ${providerTestResult.latencyMs}ms${providerTestResult.modelFound ? ' · 已匹配当前模型' : ' · 当前模型未命中返回列表'}`}
            />
          ) : null}
        </div>
      </Modal>
    </div>
  )

  const renderStorage = () => (
    <div className="console-page-shell">
      <PageHeader
        badge="管理员治理 / 存储配置"
        title="渠道式管理存储目标"
        description="改成与模型提供方一致的列表优先工作流：列表页展示，按钮新增，弹窗编辑。"
        actions={
          <Space wrap>
            <Button icon={<RefreshCw size={16} />} loading={busy} onClick={() => void loadBootstrap('存储配置已刷新')}>刷新配置</Button>
            <Button theme="solid" type="primary" onClick={() => openCreateStorageModal()}>新增存储</Button>
          </Space>
        }
      />
      <Card className="console-panel" bodyStyle={{ padding: 20 }}>
        <div className="flex flex-col gap-4 lg:flex-row lg:items-center lg:justify-between">
          <div>
            <Title heading={4} className="!mb-0">对象存储台账</Title>
            <Text className="mt-2 block console-caption">已内置本地 MinIO 地址与默认凭证，也支持继续添加多个 S3 / MinIO / OSS 来源，并通过启用开关控制是否可用于计划编排。</Text>
          </div>
          <Input
            value={storageSearchKeyword}
            onChange={setStorageSearchKeyword}
            placeholder="搜索名称、提供方、端点、地域或存储桶"
            style={{ width: 360, maxWidth: '100%' }}
          />
        </div>
        <div className="mt-5">
          <Table columns={storageColumns} dataSource={filteredStorageProfiles} pagination={false} />
        </div>
      </Card>

      <Modal
        title={storageDraft.id ? '编辑存储配置' : '新增存储配置'}
        visible={storageModalVisible}
        onCancel={closeStorageModal}
        footer={
          <Space>
            <Button onClick={closeStorageModal}>取消</Button>
            <Button theme="solid" type="primary" loading={busy} onClick={() => void saveStorage()}>保存</Button>
          </Space>
        }
        width={860}
      >
        <div className="console-stack">
          <div className="console-card-grid-2">
            <div>
              <Text className="mb-2 block font-medium">配置名称</Text>
              <Input value={storageDraft.name ?? ''} onChange={(value) => setStorageDraft((current) => ({ ...current, name: value }))} placeholder="例如 本地 MinIO / 生产 OSS" />
            </div>
            <div>
              <Text className="mb-2 block font-medium">提供方</Text>
              <Select
                value={storageDraft.provider ?? 'minio'}
                optionList={[
                  { value: 'minio', label: 'MinIO' },
                  { value: 's3', label: 'AWS S3' },
                  { value: 'oss', label: '阿里云 OSS' },
                  { value: 'custom', label: 'Custom S3 Compatible' },
                ]}
                onChange={(value) => setStorageDraft((current) => ({ ...current, provider: String(value ?? '') }))}
                style={{ width: '100%' }}
              />
            </div>
          </div>

          <div className="console-card-grid-2">
            <div>
              <Text className="mb-2 block font-medium">端点</Text>
              <Input value={storageDraft.endpoint ?? ''} onChange={(value) => setStorageDraft((current) => ({ ...current, endpoint: value }))} placeholder="例如 http://minio:9000" />
            </div>
            <div>
              <Text className="mb-2 block font-medium">地域</Text>
              <Input value={storageDraft.region ?? ''} onChange={(value) => setStorageDraft((current) => ({ ...current, region: value }))} placeholder="例如 us-east-1" />
            </div>
          </div>

          <div className="console-card-grid-2">
            <div>
              <Text className="mb-2 block font-medium">存储桶</Text>
              <Input value={storageDraft.bucket ?? ''} onChange={(value) => setStorageDraft((current) => ({ ...current, bucket: value }))} placeholder="例如 llm-factory-local" />
            </div>
            <div>
              <Text className="mb-2 block font-medium">Access Key ID</Text>
              <Input value={storageDraft.accessKeyId ?? ''} onChange={(value) => setStorageDraft((current) => ({ ...current, accessKeyId: value }))} placeholder="例如 minioadmin" />
            </div>
          </div>

          <div>
            <Text className="mb-2 block font-medium">Secret Access Key</Text>
            <Input value={storageDraft.secretAccessKey ?? ''} onChange={(value) => setStorageDraft((current) => ({ ...current, secretAccessKey: value }))} placeholder="留空则沿用已有密钥" mode="password" />
          </div>

          <div className="console-card-grid-3">
            <div>
              <Text className="mb-2 block font-medium">Path Style</Text>
              <div className="flex h-10 items-center rounded-[16px] border border-[var(--semi-color-border)] px-4">
                <Switch checked={storageDraft.usePathStyle ?? true} onChange={(checked) => setStorageDraft((current) => ({ ...current, usePathStyle: checked }))} />
              </div>
            </div>
            <div>
              <Text className="mb-2 block font-medium">启用状态</Text>
              <div className="flex h-10 items-center rounded-[16px] border border-[var(--semi-color-border)] px-4">
                <Switch checked={storageDraft.isActive ?? true} onChange={(checked) => setStorageDraft((current) => ({ ...current, isActive: checked }))} />
              </div>
            </div>
            <div>
              <Text className="mb-2 block font-medium">设为默认</Text>
              <div className="flex h-10 items-center rounded-[16px] border border-[var(--semi-color-border)] px-4">
                <Switch checked={storageDraft.isDefault ?? true} onChange={(checked) => setStorageDraft((current) => ({ ...current, isDefault: checked }))} />
              </div>
            </div>
          </div>
        </div>
      </Modal>
    </div>
  )

  const renderStrategies = () => (
    <div className="console-page-shell">
      <PageHeader
        badge="管理员治理 / 生成策略"
        title="列表化管理生成策略"
        description="与模型提供方相同，改成列表展示、搜索、按钮新增、弹窗编辑的治理模式。"
        actions={
          <Space wrap>
            <Button icon={<RefreshCw size={16} />} loading={busy} onClick={() => void loadBootstrap('生成策略已刷新')}>刷新策略</Button>
            <Button theme="solid" type="primary" onClick={() => openCreateStrategyModal()}>新增策略</Button>
          </Space>
        }
      />
      <Card className="console-panel" bodyStyle={{ padding: 20 }}>
        <div className="flex flex-col gap-4 lg:flex-row lg:items-center lg:justify-between">
          <div>
            <Title heading={4} className="!mb-0">生成策略台账</Title>
            <Text className="mt-2 block console-caption">用户在计划编排页消费这里的策略，管理员在这里维护策略数量、问题量和规划模式。</Text>
          </div>
          <Input
            value={strategySearchKeyword}
            onChange={setStrategySearchKeyword}
            placeholder="搜索策略名称、说明或规划模式"
            style={{ width: 360, maxWidth: '100%' }}
          />
        </div>
        <div className="mt-5">
          <Table columns={strategyColumns} dataSource={filteredStrategies} pagination={false} />
        </div>
      </Card>

      <Modal
        title={strategyDraft.id ? '编辑生成策略' : '新增生成策略'}
        visible={strategyModalVisible}
        onCancel={closeStrategyModal}
        footer={
          <Space>
            <Button onClick={closeStrategyModal}>取消</Button>
            <Button theme="solid" type="primary" loading={busy} onClick={() => void saveStrategy()}>保存</Button>
          </Space>
        }
        width={820}
      >
        <div className="console-stack">
          <div className="console-card-grid-2">
            <div>
              <Text className="mb-2 block font-medium">策略名称</Text>
              <Input value={strategyDraft.name ?? ''} onChange={(value) => setStrategyDraft((current) => ({ ...current, name: value }))} placeholder="例如 企业标准策略" />
            </div>
            <div>
              <Text className="mb-2 block font-medium">规划模式</Text>
              <Select value={strategyDraft.planningMode ?? 'balanced'} optionList={[
                { value: 'balanced', label: '平衡' },
                { value: 'wide-first', label: '优先广度' },
                { value: 'deep-first', label: '优先深度' },
                { value: 'cost-saving', label: '成本优先' },
              ]} onChange={(value) => setStrategyDraft((current) => ({ ...current, planningMode: String(value) }))} style={{ width: '100%' }} />
            </div>
          </div>
          <div>
            <Text className="mb-2 block font-medium">策略说明</Text>
            <Input value={strategyDraft.description ?? ''} onChange={(value) => setStrategyDraft((current) => ({ ...current, description: value }))} placeholder="用于说明该策略的适用场景" />
          </div>
          <div className="console-card-grid-2">
            <div>
              <Text className="mb-2 block font-medium">领域数</Text>
              <InputNumber value={strategyDraft.domainCount ?? 1000} onChange={(value) => setStrategyDraft((current) => ({ ...current, domainCount: Number(value ?? 0) }))} style={{ width: '100%' }} />
            </div>
            <div>
              <Text className="mb-2 block font-medium">每领域问题数</Text>
              <InputNumber value={strategyDraft.questionsPerDomain ?? 10} onChange={(value) => setStrategyDraft((current) => ({ ...current, questionsPerDomain: Number(value ?? 0) }))} style={{ width: '100%' }} />
            </div>
          </div>
          <div className="console-card-grid-2">
            <div>
              <Text className="mb-2 block font-medium">答案变体数</Text>
              <InputNumber value={strategyDraft.answerVariants ?? 1} onChange={(value) => setStrategyDraft((current) => ({ ...current, answerVariants: Number(value ?? 0) }))} style={{ width: '100%' }} />
            </div>
            <div>
              <Text className="mb-2 block font-medium">奖励变体数</Text>
              <InputNumber value={strategyDraft.rewardVariants ?? 1} onChange={(value) => setStrategyDraft((current) => ({ ...current, rewardVariants: Number(value ?? 0) }))} style={{ width: '100%' }} />
            </div>
          </div>
          <div>
            <Text className="mb-2 block font-medium">设为默认</Text>
            <div className="flex h-10 items-center rounded-[16px] border border-[var(--semi-color-border)] px-4">
              <Switch checked={Boolean(strategyDraft.isDefault ?? true)} onChange={(checked) => setStrategyDraft((current) => ({ ...current, isDefault: checked }))} />
            </div>
          </div>
        </div>
      </Modal>
    </div>
  )

  const renderPrompts = () => (
    <div className="console-page-shell">
      <PageHeader
        badge="管理员治理 / 提示词模板"
        title="列表化管理提示词模板"
        description="和模型提供方一致，改成列表展示、搜索、按钮新增、弹窗编辑的模式。"
        actions={
          <Space wrap>
            <Button icon={<RefreshCw size={16} />} loading={busy} onClick={() => void loadBootstrap('提示词模板已刷新')}>刷新模板</Button>
            <Button theme="solid" type="primary" onClick={() => openCreatePromptModal()}>新增模板</Button>
          </Space>
        }
      />
      <Card className="console-panel" bodyStyle={{ padding: 20 }}>
        <div className="flex flex-col gap-4 lg:flex-row lg:items-center lg:justify-between">
          <div>
            <Title heading={4} className="!mb-0">提示词模板台账</Title>
            <Text className="mt-2 block console-caption">领域生成、问题生成、推理生成与奖励评估的 Prompt 模板集中在这里治理。</Text>
          </div>
          <Input
            value={promptSearchKeyword}
            onChange={setPromptSearchKeyword}
            placeholder="搜索模板名称、阶段或版本"
            style={{ width: 360, maxWidth: '100%' }}
          />
        </div>
        <div className="mt-5">
          <Table columns={promptColumns} dataSource={filteredPrompts} pagination={false} />
        </div>
      </Card>

      <Modal
        title={promptDraft.id ? '编辑提示词模板' : '新增提示词模板'}
        visible={promptModalVisible}
        onCancel={closePromptModal}
        footer={
          <Space>
            <Button onClick={closePromptModal}>取消</Button>
            <Button theme="solid" type="primary" loading={busy} onClick={() => void savePrompt()}>保存</Button>
          </Space>
        }
        width={920}
      >
        <div className="console-stack">
          <div className="console-card-grid-2">
            <div>
              <Text className="mb-2 block font-medium">模板名称</Text>
              <Input value={promptDraft.name} onChange={(value) => setPromptDraft((current) => ({ ...current, name: value }))} placeholder="例如 领域生成模板" />
            </div>
            <div>
              <Text className="mb-2 block font-medium">阶段</Text>
              <Input value={promptDraft.stage} onChange={(value) => setPromptDraft((current) => ({ ...current, stage: value }))} placeholder="例如 domain-generation / question-generation" />
            </div>
          </div>
          <div className="console-card-grid-2">
            <div>
              <Text className="mb-2 block font-medium">版本号</Text>
              <Input value={promptDraft.version} onChange={(value) => setPromptDraft((current) => ({ ...current, version: value }))} placeholder="例如 v1" />
            </div>
            <div>
              <Text className="mb-2 block font-medium">启用状态</Text>
              <div className="flex h-10 items-center rounded-[16px] border border-[var(--semi-color-border)] px-4">
                <Switch checked={promptDraft.isActive} onChange={(checked) => setPromptDraft((current) => ({ ...current, isActive: checked }))} />
              </div>
            </div>
          </div>
          <div>
            <Text className="mb-2 block font-medium">系统提示词</Text>
            <TextArea rows={6} value={promptDraft.systemPrompt} onChange={(value) => setPromptDraft((current) => ({ ...current, systemPrompt: value }))} placeholder="系统提示词" />
          </div>
          <div>
            <Text className="mb-2 block font-medium">用户提示词</Text>
            <TextArea rows={6} value={promptDraft.userPrompt} onChange={(value) => setPromptDraft((current) => ({ ...current, userPrompt: value }))} placeholder="用户提示词" />
          </div>
        </div>
      </Modal>
    </div>
  )

  const renderAudit = () => (
    <div className="console-page-shell">
      <PageHeader badge="管理员治理 / 审计日志" title="追踪配置变更与治理事件" description="管理员在同一前端直接回看治理动作；不再切换到另一套后台。" />
      <div className="console-card-grid-2">
        <Card className="console-panel" bodyStyle={{ padding: 20 }}>
          <Title heading={4} className="!mb-0">审计事件列表</Title>
          <Table columns={auditColumns} dataSource={auditLogs} pagination={{ pageSize: 8 }} />
        </Card>
        <Card className="console-focus-card" bodyStyle={{ padding: 20 }}>
          <Title heading={4} className="!mb-0">治理摘要</Title>
          <div className="mt-5 console-summary-grid">
            <div className="console-summary-row"><span>活动模型路由</span><Text strong>{dashboard?.activeProviderCount ?? 0}</Text></div>
            <div className="console-summary-row"><span>存储配置</span><Text strong>{dashboard?.storageProfileCount ?? 0}</Text></div>
            <div className="console-summary-row"><span>策略数量</span><Text strong>{dashboard?.strategyCount ?? 0}</Text></div>
            <div className="console-summary-row"><span>Prompt 数量</span><Text strong>{dashboard?.promptCount ?? 0}</Text></div>
            <div className="console-summary-row"><span>审计记录</span><Text strong>{dashboard?.auditLogCount ?? 0}</Text></div>
          </div>
          <div className="mt-6">
            <Text className="console-caption">建议在修改模型、存储、策略或 Prompt 后，立即回到本页复核审计结果。</Text>
          </div>
        </Card>
      </div>
    </div>
  )

  if (sessionLoading) {
    return (
      <div className="flex min-h-screen items-center justify-center">
        <Spin size="large" tip="正在初始化统一控制台" />
      </div>
    )
  }

  return (
    <Routes>
      <Route path="/login" element={user ? <Navigate to="/console/overview" replace /> : <LoginPage onSubmit={handleLogin} loading={authSubmitting} />} />
      <Route
        path="/*"
        element={
          user ? (
            <Layout className="app-layout">
              <Header className="console-header-shell" style={{ padding: 0, position: 'fixed', inset: '0 0 auto 0', zIndex: 50 }}>
                <div className="mx-auto flex h-16 items-center justify-between gap-4 px-4 lg:px-6">
                  <div className="flex items-center gap-3">
                    <Button icon={<LayoutDashboard size={16} />} theme="borderless" onClick={() => setSidebarCollapsed((current) => !current)} />
                    <Avatar color="blue" size="small">L</Avatar>
                    <div>
                      <div className="font-semibold">LLM Data Factory Console</div>
                      <div className="text-xs console-nav-caption">{visiblePages.find((page) => page.route === activeNav)?.caption ?? '统一控制台'}</div>
                    </div>
                  </div>
                  <div className="console-header-actions flex items-center gap-3">
                    <Tag color={isAdmin ? 'green' : 'blue'}>{isAdmin ? '管理员' : '普通用户'}</Tag>
                    <Text>{user.email}</Text>
                    <Button icon={<RefreshCw size={16} />} loading={busy} onClick={() => void loadBootstrap('控制台数据已刷新')} />
                    <Button icon={<LogOut size={16} />} onClick={() => void handleLogout()}>退出</Button>
                  </div>
                </div>
              </Header>
              <Layout style={{ paddingTop: 64 }}>
                <Sider className="app-sider" style={{ position: 'fixed', top: 64, left: 0, border: 'none', width: 'var(--sidebar-current-width)', zIndex: 30 }}>
                  <div className="console-nav-shell h-full px-3 py-4">
                    <Card className="console-sidebar-card mb-4" bodyStyle={{ padding: 16 }}>
                      <Text strong>统一导航</Text>
                      <Text className="mt-2 block console-caption">管理员继承全部用户能力；普通用户只显示数据链路。</Text>
                    </Card>
                    <Nav
                      bodyStyle={{ paddingBottom: 12 }}
                      selectedKeys={[activeNav]}
                      items={[
                        {
                          itemKey: 'user-group',
                          text: '数据生产链路',
                          items: userPages.map((page) => ({
                            itemKey: page.route,
                            text: page.label,
                            icon: <page.icon size={16} />,
                          })),
                        },
                        ...(isAdmin
                          ? [
                              {
                                itemKey: 'admin-group',
                                text: '系统治理',
                                items: adminPages.map((page) => ({
                                  itemKey: page.route,
                                  text: page.label,
                                  icon: <page.icon size={16} />,
                                })),
                              },
                            ]
                          : []),
                      ]}
                      onSelect={(data) => navigate(String(data.itemKey))}
                      footer={
                        <div className="px-3 pb-3">
                          <Card className="console-sidebar-card" bodyStyle={{ padding: 14 }}>
                            <div className="console-summary-grid">
                              <div className="console-summary-row"><span>数据集</span><Text strong>{runtime?.datasetCount ?? 0}</Text></div>
                              <div className="console-summary-row"><span>队列深度</span><Text strong>{runtime?.queueDepth ?? 0}</Text></div>
                            </div>
                          </Card>
                        </div>
                      }
                    />
                  </div>
                </Sider>
                <Layout style={{ marginLeft: 'var(--sidebar-current-width)' }}>
                  <Content style={{ padding: 24 }}>
                    <Routes>
                      <Route path="/console/overview" element={renderOverview()} />
                      <Route path="/console/planning" element={renderPlanning()} />
                      <Route path="/console/domains" element={renderDomains()} />
                      <Route path="/console/questions" element={renderRecordPage({ badge: '用户链路 / 问题生成', title: '按领域批量生成问题集合', description: '领域确认后，在同一控制台继续推进问题生成。', actionLabel: '加入问题生成队列', onGenerate: generateQuestions, onRefresh: async () => { if (activeDatasetId) await loadDatasetWorkspace(activeDatasetId, '问题结果已刷新') }, records: questions, emptyTitle: '尚未生成问题', emptyDescription: '请先确认领域，然后将问题生成任务加入队列。', renderRecord: (record: Question) => <div key={record.id} className="console-record-item"><div className="flex items-center justify-between gap-3"><Tag color="blue">{record.domainName}</Tag><Text className="console-caption">{formatTime(record.createdAt)}</Text></div><Text className="mt-3 block">{record.content}</Text></div> })} />
                      <Route path="/console/reasoning" element={renderRecordPage({ badge: '用户链路 / 推理生成', title: '异步生成长思维链与答案摘要', description: '问题记录足够后，继续在本应用里推进推理生成。', actionLabel: '加入推理生成队列', onGenerate: generateReasoning, onRefresh: async () => { if (activeDatasetId) await loadDatasetWorkspace(activeDatasetId, '推理结果已刷新') }, records: reasoning, emptyTitle: '尚未生成推理', emptyDescription: '先完成问题生成，再加入推理队列。', renderRecord: (record: ReasoningRecord) => <div key={record.id} className="console-record-item"><div className="flex items-center justify-between gap-3"><Tag color="cyan">{record.objectKey}</Tag><Text className="console-caption">{formatTime(record.createdAt)}</Text></div><Text className="mt-3 block">{record.answerSummary}</Text><Text className="mt-2 block console-caption">{record.questionText}</Text></div> })} />
                      <Route path="/console/rewards" element={renderRecordPage({ badge: '用户链路 / 奖励数据', title: '生成强化学习奖励评估记录', description: '管理员和普通用户共享同一奖励数据页，管理员额外能返回左侧治理区。', actionLabel: '加入奖励生成队列', onGenerate: generateRewards, onRefresh: async () => { if (activeDatasetId) await loadDatasetWorkspace(activeDatasetId, '奖励结果已刷新') }, records: rewards, emptyTitle: '尚未生成奖励记录', emptyDescription: '先完成推理生成，再触发奖励评估。', renderRecord: (record: RewardRecord) => <div key={record.id} className="console-record-item"><div className="flex items-center justify-between gap-3"><Tag color="green">评分 {record.score.toFixed(2)}</Tag><Text className="console-caption">{formatTime(record.createdAt)}</Text></div><Text className="mt-3 block">{record.questionText}</Text><Text className="mt-2 block console-caption">{record.objectKey}</Text></div> })} />
                      <Route path="/console/exports" element={renderRecordPage({ badge: '用户链路 / 导出交付', title: '查看 JSONL 工件与运行态收口', description: '导出页作为生产链路收口动作，与管理员治理页面保持在同一应用中。', actionLabel: '加入导出队列', onGenerate: generateExport, onRefresh: async () => { if (activeDatasetId) await loadDatasetWorkspace(activeDatasetId, '导出结果已刷新') }, records: artifacts, emptyTitle: '尚未生成导出工件', emptyDescription: '完成奖励生成后，即可触发导出。', renderRecord: (record: Artifact) => <div key={record.id} className="console-record-item"><div className="flex items-center justify-between gap-3"><Tag color="violet">{artifactLabel(record.artifactType)}</Tag><Text className="console-caption">{formatTime(record.createdAt)}</Text></div><Text className="mt-3 block">{record.objectKey}</Text><Text className="mt-2 block console-caption">{record.contentType}</Text></div> })} />
                      {isAdmin ? <Route path="/console/admin/providers" element={renderProviders()} /> : null}
                      {isAdmin ? <Route path="/console/admin/storage" element={renderStorage()} /> : null}
                      {isAdmin ? <Route path="/console/admin/strategies" element={renderStrategies()} /> : null}
                      {isAdmin ? <Route path="/console/admin/prompts" element={renderPrompts()} /> : null}
                      {isAdmin ? <Route path="/console/admin/audit" element={renderAudit()} /> : null}
                      <Route path="*" element={<Navigate to="/console/overview" replace />} />
                    </Routes>
                  </Content>
                </Layout>
              </Layout>
            </Layout>
          ) : (
            <Navigate to="/login" replace />
          )
        }
      />
    </Routes>
  )
}
