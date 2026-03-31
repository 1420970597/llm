import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
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
  CirclePlus,
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
  type ApiError,
  type Artifact,
  type AuditRecord,
  type DashboardRecord,
  type Dataset,
  type DatasetGraph,
  type Domain,
  type PipelineProgress,
  type PromptRecord,
  type Provider,
  type ProviderConnectivityResult,
  type ProviderModelInfo,
  type Question,
  type ReasoningRecord,
  type RewardRecord,
  type RuntimeStatus,
  type StageEnqueueResult,
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
  { label: '工作台', route: '/console/tasks', icon: LayoutDashboard, caption: '查看当前任务、待办动作与最近进展' },
  { label: '新建任务', route: '/console/planning', icon: CirclePlus, caption: '快速创建任务，先填主题和目标规模' },
  { label: '我的任务', route: '/console/tasks', icon: Target, caption: '进入任务列表并继续推进具体任务' },
  { label: '数据资产', route: '/console/results', icon: HardDriveDownload, caption: '统一查看结果、质量状态与交付文件' },
  { label: '账户与帮助', route: '/console/help', icon: Users, caption: '查看帮助、恢复指引与当前账号状态' },
]

const stageRouteNavMap: Record<string, string> = {
  '/console/planning': '/console/planning',
  '/console/domains': '/console/tasks',
  '/console/questions': '/console/results',
  '/console/reasoning': '/console/results',
  '/console/rewards': '/console/results',
  '/console/exports': '/console/results',
}

const taskWorkbenchPages: NavPage[] = [
  { label: '主题结构', route: '/console/domains', icon: GitBranch, caption: '生成主题结构并确认可执行层级' },
]

const resultWorkbenchPages: NavPage[] = [
  { label: '问题生成', route: '/console/questions', icon: Layers3, caption: '批量生成问题并检查覆盖情况' },
  { label: '答案内容', route: '/console/reasoning', icon: BrainCircuit, caption: '生成答案摘要并确认可读性' },
  { label: '质量评估', route: '/console/rewards', icon: ShieldCheck, caption: '查看质量评估并判断是否可交付' },
  { label: '导出交付', route: '/console/exports', icon: HardDriveDownload, caption: '导出最终产物并完成任务闭环' },
]

const adminPages: NavPage[] = [
  { label: '运营监控', route: '/console/operations', icon: ServerCog, caption: '查看队列、配置健康度与最近操作', adminOnly: true },
  { label: 'AI 服务', route: '/console/admin/providers', icon: Database, caption: '配置生成内容所用的 AI 服务', adminOnly: true },
  { label: '结果存储', route: '/console/admin/storage', icon: FolderCog, caption: '配置结果保存位置（S3 / MinIO / OSS）', adminOnly: true },
  { label: '生成规则', route: '/console/admin/strategies', icon: Workflow, caption: '设置主题数量、题目数量和评分样本数', adminOnly: true },
  { label: '生成指令', route: '/console/admin/prompts', icon: Sparkles, caption: '设置各步骤使用的生成指令文本', adminOnly: true },
  { label: '操作记录', route: '/console/admin/audit', icon: Settings, caption: '查看配置变更记录', adminOnly: true },
]

function statusLabel(status: string) {
  switch (status) {
    case 'draft':
      return '待确认主题结构'
    case 'domains_confirmed':
      return '主题结构已确认，待生成问题'
    case 'questions_queued':
      return '问题生成排队中'
    case 'questions_generated':
      return '问题已就绪，待生成答案'
    case 'questions_failed':
      return '问题生成失败'
    case 'reasoning_queued':
      return '答案生成排队中'
    case 'reasoning_generated':
      return '答案已就绪，待质量评分'
    case 'reasoning_partial':
      return '答案部分生成，需复核'
    case 'reasoning_failed':
      return '答案生成失败'
    case 'rewards_queued':
      return '质量评分排队中'
    case 'rewards_generated':
      return '评分完成，可导出交付'
    case 'rewards_partial':
      return '评分部分完成，需复核'
    case 'rewards_failed':
      return '质量评分失败'
    case 'export_queued':
      return '导出任务排队中'
    case 'export_generated':
      return '导出已完成'
    case 'export_failed':
      return '导出失败'
    default:
      return status
  }
}

function progressPercent(status: string) {
  switch (status) {
    case 'draft':
      return 15
    case 'domains_confirmed':
    case 'questions_queued':
      return 35
    case 'questions_generated':
    case 'reasoning_queued':
      return 55
    case 'reasoning_generated':
    case 'rewards_queued':
      return 75
    case 'rewards_generated':
    case 'export_queued':
      return 90
    case 'export_generated':
      return 100
    default:
      return 10
  }
}

function nextActionLabel(status: string) {
  switch (status) {
    case 'draft':
      return '先确认主题结构，再启动问题生成'
    case 'domains_confirmed':
      return '启动问题生成，补齐任务素材'
    case 'questions_queued':
      return '等待问题生成完成后进入答案生成阶段'
    case 'questions_generated':
      return '启动答案生成，形成可评审内容'
    case 'reasoning_queued':
      return '等待答案生成完成后进入质量评分'
    case 'reasoning_generated':
      return '启动质量评分，准备交付结论'
    case 'rewards_queued':
      return '等待质量评分完成后执行导出'
    case 'rewards_generated':
      return '导出结果包并通知验收'
    case 'export_queued':
      return '等待导出结果准备完成'
    case 'export_generated':
      return '下载结果文件并继续下一步'
    default:
      return '刷新任务状态后继续'
  }
}

function waitingStateLabel(status: string, queueDepth: number) {
  if (queueDepth > 0 && status.endsWith('_queued')) {
    return `系统正在处理队列（前方约 ${queueDepth} 个任务）`
  }
  switch (status) {
    case 'draft':
      return '等待你确认主题结构'
    case 'domains_confirmed':
      return '等待你启动问题生成'
    case 'questions_queued':
      return '题目生成处理中'
    case 'questions_generated':
      return '等待你启动答案生成'
    case 'reasoning_queued':
      return '答案生成处理中'
    case 'reasoning_generated':
      return '等待你启动质量评分'
    case 'rewards_queued':
      return '质量评分处理中'
    case 'rewards_generated':
      return '等待你执行结果导出'
    case 'export_queued':
      return '导出处理中'
    case 'export_generated':
      return '导出已完成，可下载交付'
    default:
      return '等待状态同步'
  }
}

function waitingReasonLabel(status: string, queueDepth: number) {
  if (status.endsWith('_queued')) {
    return queueDepth > 0
      ? `该阶段已入队，当前前方约 ${queueDepth} 个任务，系统会按顺序执行。`
      : '该阶段已入队，正在等待执行资源分配。'
  }
  switch (status) {
    case 'draft':
      return '系统尚未开始生成，因为主题结构还未确认。'
    case 'domains_confirmed':
      return '方向已确认，等待你手动启动问题生成。'
    case 'questions_generated':
      return '问题结果已准备好，等待你启动答案生成。'
    case 'reasoning_generated':
      return '答案结果已准备好，等待你启动质量评分。'
    case 'rewards_generated':
      return '评分结果已准备好，等待你启动导出。'
    case 'export_generated':
      return '导出文件已生成，等待你下载并交付。'
    default:
      return '系统正在同步阶段信息，请稍后刷新。'
  }
}

function waitingActionLabel(status: string) {
  switch (status) {
    case 'draft':
      return '前往“主题结构”，确认后继续。'
    case 'domains_confirmed':
      return '前往“问题生成”，点击“开始生成题目”。'
    case 'questions_queued':
    case 'reasoning_queued':
    case 'rewards_queued':
    case 'export_queued':
      return '无需停留本页，可先处理其他步骤，再按刷新建议回看。'
    case 'questions_generated':
      return '前往“答案生成”，点击“开始生成答案”。'
    case 'questions_failed':
      return '请先回到“题目结果”排查失败原因，再重新生成。'
    case 'reasoning_generated':
    case 'reasoning_partial':
      return '前往“质量评分”，确认可用后再启动评分。'
    case 'reasoning_failed':
      return '请先回到“答案结果”排查失败原因，再重新生成。'
    case 'rewards_generated':
    case 'rewards_partial':
      return '前往“结果交付”，确认可用后再导出。'
    case 'rewards_failed':
      return '请先回到“质量评分”排查失败原因，再重新评分。'
    case 'export_generated':
      return '进入“结果交付”下载文件，并完成交付确认。'
    case 'export_failed':
      return '请重新发起导出，或先检查上游评分结果是否完整。'
    default:
      return '点击刷新同步状态后继续。'
  }
}

function refreshExpectationLabel(status: string, queueDepth: number) {
  if (status.endsWith('_queued')) {
    if (queueDepth > 3) return '建议 2~3 分钟后刷新一次；频繁刷新不会加速处理。'
    return '建议 60~90 秒后刷新一次；频繁刷新不会更快。'
  }
  if (status === 'export_generated' || status.endsWith('_generated')) {
    return '当前阶段已完成，可直接进入下一步。'
  }
  return '当前阶段无需频繁刷新，状态变化后再同步即可。'
}

function trustMessageLabel(status: string) {
  if (status.endsWith('_queued')) {
    return '任务已进入后台持续处理，可先切换到其他页面，进度不会丢失。'
  }
  if (status.endsWith('_generated') || status === 'export_generated') {
    return '当前阶段结果已落库，可放心继续后续操作。'
  }
  return '系统会自动保存当前任务上下文，可按节奏推进。'
}

type StageKey = 'questions' | 'reasoning' | 'rewards' | 'export'

function statusStageKey(status: string): StageKey | null {
  switch (status) {
    case 'questions_queued':
    case 'questions_generated':
      return 'questions'
    case 'reasoning_queued':
    case 'reasoning_generated':
      return 'reasoning'
    case 'rewards_queued':
    case 'rewards_generated':
      return 'rewards'
    case 'export_queued':
    case 'export_generated':
      return 'export'
    default:
      return null
  }
}

function minutesSince(value?: string) {
  if (!value) return null
  const parsed = Date.parse(value)
  if (Number.isNaN(parsed)) return null
  const elapsedMinutes = Math.floor((Date.now() - parsed) / 60000)
  return elapsedMinutes >= 0 ? elapsedMinutes : null
}

function etaBaseWindow(status: string) {
  switch (status) {
    case 'questions_queued':
      return { min: 2, max: 8 }
    case 'reasoning_queued':
      return { min: 4, max: 12 }
    case 'rewards_queued':
      return { min: 2, max: 6 }
    case 'export_queued':
      return { min: 1, max: 4 }
    default:
      return null
  }
}

function etaLabel(status: string, queueDepth: number, acceptedAt?: string) {
  if (status === 'export_generated') return '已完成，可立即下载交付'
  if (status.endsWith('_generated')) return '当前阶段已完成，可立即推进下一步'
  if (status.endsWith('_queued')) {
    const base = etaBaseWindow(status)
    if (!base) return '预计处理中'
    const queuePenalty = Math.min(20, Math.max(0, queueDepth) * 2)
    const estimatedMin = base.min + Math.floor(queuePenalty / 2)
    const estimatedMax = base.max + queuePenalty
    const elapsedMinutes = minutesSince(acceptedAt)

    if (elapsedMinutes === null) {
      return `预计还需 ${estimatedMin}~${estimatedMax} 分钟`
    }

    const remainingMin = Math.max(1, estimatedMin - elapsedMinutes)
    const remainingMax = Math.max(1, estimatedMax - elapsedMinutes)
    if (elapsedMinutes >= estimatedMax) {
      return `已等待 ${elapsedMinutes} 分钟，预计接近完成，请刷新确认`
    }
    return `预计还需 ${remainingMin}~${remainingMax} 分钟（已等待 ${elapsedMinutes} 分钟）`
  }
  if (status === 'draft') return '确认方向结构后即可生成 ETA'
  if (status === 'domains_confirmed') return '启动问题生成后即可看到 ETA'
  return '刷新后更新 ETA'
}

function stageStateStyle(state: 'pending' | 'queued' | 'in_progress' | 'completed' | 'failed') {
  switch (state) {
    case 'completed':
      return { label: '已完成', color: 'green' as const, percent: 100 }
    case 'in_progress':
      return { label: '进行中', color: 'blue' as const, percent: 65 }
    case 'queued':
      return { label: '排队中', color: 'cyan' as const, percent: 35 }
    case 'failed':
      return { label: '失败', color: 'red' as const, percent: 100 }
    default:
      return { label: '待开始', color: 'grey' as const, percent: 10 }
  }
}

function stageKeyLabel(key: string) {
  switch (key) {
    case 'domains':
      return '方向整理'
    case 'questions':
      return '问题生成'
    case 'reasoning':
      return '推理生成'
    case 'rewards':
      return '质量评分'
    case 'export':
      return '导出交付'
    default:
      return key
  }
}

function asApiError(error: unknown): ApiError {
  return error as ApiError
}

function isSessionExpiredError(error: unknown) {
  return asApiError(error).statusCode === 401
}

function isForbiddenError(error: unknown) {
  return asApiError(error).statusCode === 403
}

function sourceLabel(source: string) {
  switch (source) {
    case 'ai':
      return '模型生成'
    default:
      return source
  }
}

function reviewStatusLabel(status: string) {
  switch (status) {
    case 'approved':
      return '已确认'
    case 'pending':
      return '待复核'
    default:
      return status || '待复核'
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

function taskRouteDatasetId(pathname: string) {
  const matched = pathname.match(/^\/console\/tasks\/(\d+)(?:\/)?$/)
  if (!matched) return null
  const parsed = Number(matched[1])
  return Number.isFinite(parsed) ? parsed : null
}

function questionStatusLabel(status: string) {
  switch (status) {
    case 'generated':
      return { text: '已生成', color: 'green' as const }
    case 'queued':
      return { text: '排队中', color: 'blue' as const }
    case 'failed':
      return { text: '生成失败', color: 'red' as const }
    default:
      return { text: '处理中', color: 'grey' as const }
  }
}

function reasoningQualityLabel(summary: string) {
  const length = summary.trim().length
  if (length >= 140) return { text: '完整', color: 'green' as const, note: '信息较完整，可直接用于评分。' }
  if (length >= 70) return { text: '可用', color: 'blue' as const, note: '关键信息齐全，建议抽检。' }
  return { text: '待补充', color: 'orange' as const, note: '摘要较短，建议重跑或复核。' }
}

function rewardQualityLabel(score: number) {
  if (score >= 0.85) return { text: '高质量', color: 'green' as const, note: '可以直接进入导出阶段。' }
  if (score >= 0.7) return { text: '可交付', color: 'blue' as const, note: '建议抽样复核后导出。' }
  if (score >= 0.5) return { text: '待优化', color: 'orange' as const, note: '建议补充推理后再评估。' }
  return { text: '风险', color: 'red' as const, note: '不建议直接导出，请先回修。' }
}

function artifactDisplayName(objectKey: string) {
  return objectKey.split('/').pop() || objectKey
}

function artifactContentTypeLabel(contentType: string) {
  switch (contentType) {
    case 'application/jsonl':
      return '训练数据（JSONL）'
    case 'application/x-ndjson':
      return '训练数据（NDJSON）'
    case 'application/json':
      return '结构化数据（JSON）'
    default:
      return contentType || '未知类型'
  }
}

function artifactContentTypeHint(contentType: string) {
  switch (contentType) {
    case 'application/jsonl':
      return '可直接用于主流训练平台导入。'
    case 'application/x-ndjson':
      return '按行组织样本，适合流式处理任务。'
    case 'application/json':
      return '结构化结果包，适合验收和复核。'
    default:
      return '请在交付前确认下游系统是否支持该文件类型。'
  }
}

function artifactUsageCategory(artifact: Artifact): 'delivery' | 'review' | 'other' {
  if (artifact.contentType === 'application/jsonl' || artifact.artifactType === 'jsonl-export') return 'delivery'
  if (artifact.contentType === 'application/json' || artifact.contentType === 'application/x-ndjson') return 'review'
  return 'other'
}

function artifactUsageLabel(category: 'delivery' | 'review' | 'other') {
  switch (category) {
    case 'delivery':
      return '交付优先'
    case 'review':
      return '复核资料'
    default:
      return '其他类型'
  }
}

function artifactSourceVersionHint(artifact: Artifact) {
  return `来源任务 #${artifact.datasetId} · 生成于 ${formatTime(artifact.createdAt)}；同一任务多次导出时，默认以最新时间作为交付版本。`
}

function artifactDeliveryNote(artifact: Artifact) {
  const category = artifactUsageCategory(artifact)
  if (category === 'delivery') return '可直接作为标准交付包给下游训练或评测流程。'
  if (category === 'review') return '建议先用于人工复核或验收，再决定是否进入正式交付。'
  return '建议先确认格式兼容性与用途，再安排对外交付。'
}

function artifactDownloadDecisionHint(artifact: Artifact) {
  const category = artifactUsageCategory(artifact)
  if (category === 'delivery') return '建议优先下载：可直接进入下游流程。'
  if (category === 'review') return '按需下载：用于抽检、验收或问题排查。'
  return '谨慎下载：先确认接收方能处理该类型。'
}

function DirectionStructurePreview({ rootKeyword, domains }: { rootKeyword: string; domains: Domain[] }) {
  const topLevelDomains = domains.filter((domain) => !domain.parentId)
  const childDomains = domains.reduce<Map<number, Domain[]>>((map, domain) => {
    if (!domain.parentId) return map
    const siblings = map.get(domain.parentId) ?? []
    siblings.push(domain)
    map.set(domain.parentId, siblings)
    return map
  }, new Map())
  const pendingCount = domains.filter((domain) => domain.reviewStatus !== 'approved').length

  return (
    <div className="console-stack">
      <div className="console-summary-grid">
        <div className="console-summary-row"><span>核心主题</span><Text strong>{rootKeyword}</Text></div>
        <div className="console-summary-row"><span>一级方向</span><Text strong>{topLevelDomains.length}</Text></div>
        <div className="console-summary-row"><span>待复核方向</span><Text strong>{pendingCount}</Text></div>
      </div>
      {topLevelDomains.length > 0 ? (
        <div className="console-card-grid-2">
          {topLevelDomains.map((domain) => {
            const children = childDomains.get(domain.id) ?? []
            return (
              <div key={domain.id} className="console-domain-item">
                <div className="flex items-center justify-between gap-3">
                  <Text strong>{domain.name}</Text>
                  <Tag color={domain.reviewStatus === 'approved' ? 'green' : 'blue'}>{reviewStatusLabel(domain.reviewStatus)}</Tag>
                </div>
                <Text className="mt-2 block console-caption">{sourceLabel(domain.source)} · 下级方向 {children.length} 个</Text>
                {children.length > 0 ? (
                  <div className="mt-3 flex flex-wrap gap-2">
                    {children.map((child) => (
                      <Tag key={child.id} color="grey">{child.name}</Tag>
                    ))}
                  </div>
                ) : (
                  <Text className="mt-3 block console-caption">暂无下级方向，可直接进入命名复核。</Text>
                )}
              </div>
            )
          })}
        </div>
      ) : (
        <EmptyCard title="尚未生成方向结构" description="点击“生成方向结构”后，这里会展示可复核的方向树。" />
      )}
    </div>
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

type TrustSignal = {
  tone: 'info' | 'success' | 'warning'
  title: string
  detail: string
  recoveryHint?: string
  nextStep?: { label: string; route: string }
}

function TrustSignalCard({
  signal,
  onDismiss,
  onNavigate,
}: {
  signal: TrustSignal
  onDismiss: () => void
  onNavigate: (route: string) => void
}) {
  return (
    <Card className="console-panel" bodyStyle={{ padding: 16 }}>
      <Space vertical align="start" spacing="medium" style={{ width: '100%' }}>
        <Space>
          <Tag color={signal.tone === 'success' ? 'green' : signal.tone === 'warning' ? 'orange' : 'blue'}>
            {signal.tone === 'success' ? '已完成' : signal.tone === 'warning' ? '需处理' : '提示'}
          </Tag>
          <Text strong>{signal.title}</Text>
        </Space>
        <Text>{signal.detail}</Text>
        {signal.recoveryHint ? <Text className="console-caption">恢复建议：{signal.recoveryHint}</Text> : null}
        <Space>
          {signal.nextStep ? <Button theme="solid" type="primary" onClick={() => onNavigate(signal.nextStep!.route)}>{signal.nextStep.label}</Button> : null}
          <Button onClick={onDismiss}>我已知晓</Button>
        </Space>
      </Space>
    </Card>
  )
}

function LoginPage({
  onSubmit,
  loading,
  signal,
  onDismissSignal,
  onNavigate,
}: {
  onSubmit: (email: string, password: string) => Promise<void>
  loading: boolean
  signal: TrustSignal | null
  onDismissSignal: () => void
  onNavigate: (route: string) => void
}) {
  const [email, setEmail] = useState('')
  const [password, setPassword] = useState('')

  return (
    <div className="console-login-shell flex items-center justify-center px-4 py-10">
      <div className="grid w-full max-w-6xl gap-6 lg:grid-cols-[1.2fr,0.8fr]">
        <Card className="console-panel" bodyStyle={{ padding: 28 }}>
          <span className="console-chip">企业数据工厂</span>
          <Title heading={1} className="!mb-0 mt-4">先创建任务，再持续推进交付。</Title>
          <Text className="mt-4 block console-page-subtitle">
            现在的控制台按“工作台 → 新建任务 → 我的任务 → 数据资产”组织。
            登录后先建立任务，再回到任务页持续推进生成、评估与导出。
          </Text>
          <div className="console-card-grid-2 mt-6">
            {[
              { icon: CirclePlus, title: '新建任务更靠前', text: '首次进入先看到主入口，不需要先理解内部阶段名词。' },
              { icon: Target, title: '任务推进更清晰', text: '已有任务统一从“我的任务”进入，避免在首页混排全部流程。' },
              { icon: HardDriveDownload, title: '结果集中查看', text: '交付文件、质量状态与复核资产都统一收敛到数据资产页。' },
              { icon: ShieldCheck, title: '状态持续可见', text: '登录后可以继续上次进度，并在工作台看到当前待办。' },
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
              <Title heading={4} className="!mb-0">登录你的账号</Title>
              <Text className="console-caption">登录后先进入工作台，再从新建任务或我的任务开始。</Text>
            </div>
          </div>
          {signal ? (
            <div className="mt-5">
              <TrustSignalCard signal={signal} onDismiss={onDismissSignal} onNavigate={onNavigate} />
            </div>
          ) : null}
          <div className="mt-5 grid gap-4">
            <div>
              <Text className="mb-2 block font-medium">邮箱</Text>
              <Input value={email} onChange={setEmail} size="large" placeholder="请输入邮箱" />
            </div>
            <div>
              <Text className="mb-2 block font-medium">密码</Text>
              <Input value={password} onChange={setPassword} mode="password" size="large" placeholder="请输入密码" />
            </div>
            <Button theme="solid" type="primary" size="large" loading={loading} onClick={() => void onSubmit(email, password)}>
进入任务中心
            </Button>
          </div>
          <div className="mt-6 console-summary-grid">
            <div className="console-summary-row"><span>登录后第一步</span><Text strong>先点击“新建任务”</Text></div>
            <div className="console-summary-row"><span>已有任务</span><Text strong>从“我的任务”继续推进</Text></div>
            <div className="console-summary-row"><span>交付完成后</span><Text strong>去“数据资产”查看结果和下载文件</Text></div>
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
  const [bootstrapLoading, setBootstrapLoading] = useState(false)
  const [workspaceLoading, setWorkspaceLoading] = useState(false)
  const [actionLoading, setActionLoading] = useState(false)
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
  const [exportFilter, setExportFilter] = useState<'all' | 'delivery' | 'review' | 'other'>('delivery')
  const [showAdvancedGraphView, setShowAdvancedGraphView] = useState(false)
  const [pipelineProgress, setPipelineProgress] = useState<PipelineProgress | null>(null)
  const [stageRunMeta, setStageRunMeta] = useState<Partial<Record<StageKey, StageEnqueueResult>>>({})

  const [plannerForm, setPlannerForm] = useState({
    name: '',
    rootKeyword: '',
    targetSize: 0,
    strategyId: 0,
    providerId: 0,
    storageProfileId: 0,
  })
  const showAdvancedPlanning = false
  const [trustSignal, setTrustSignal] = useState<TrustSignal | null>(null)

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
  const activePipeline = useMemo(
    () => (pipelineProgress && pipelineProgress.datasetId === activeDatasetId ? pipelineProgress : null),
    [pipelineProgress, activeDatasetId],
  )
  const activeStageKey = activeDataset ? statusStageKey(activeDataset.status) : null
  const activeStageRun = activeStageKey ? stageRunMeta[activeStageKey] : undefined
  const exportDeliveryPending = Boolean(
    activeDataset?.status === 'export_generated' &&
    activePipeline?.stages.some((stage) => stage.key === 'export' && stage.state !== 'completed'),
  )
  const activeEta = activeDataset
    ? exportDeliveryPending
      ? '预计 1~3 分钟内完成交付文件落盘'
      : etaLabel(activeDataset.status, runtime?.queueDepth ?? 0, activeStageRun?.acceptedAt)
    : '请先创建任务'
  const activeTaskDetailRoute = activeDataset ? `/console/tasks/${activeDataset.id}` : '/console/tasks'
  const activeTaskNavLabel = activeDataset ? '返回当前任务' : '返回任务列表'
  const canGenerateQuestions = activeDataset?.status === 'domains_confirmed'
  const canGenerateReasoning = activeDataset?.status === 'questions_generated'
  const canGenerateRewards = activeDataset?.status === 'reasoning_generated' || activeDataset?.status === 'reasoning_partial'
  const canGenerateExport = activeDataset?.status === 'rewards_generated' || activeDataset?.status === 'rewards_partial'
  const filteredArtifacts = useMemo(() => {
    if (exportFilter === 'delivery') return artifacts.filter((item) => artifactUsageCategory(item) === 'delivery')
    if (exportFilter === 'review') return artifacts.filter((item) => artifactUsageCategory(item) === 'review')
    if (exportFilter === 'other') return artifacts.filter((item) => artifactUsageCategory(item) === 'other')
    return artifacts
  }, [artifacts, exportFilter])
  const visibleUserPages = useMemo(() => userPages.filter((page) => !page.adminOnly || isAdmin), [isAdmin])
  const visiblePages = useMemo(() => [...visibleUserPages, ...(isAdmin ? adminPages : [])], [isAdmin, visibleUserPages])
  const activeNav = useMemo(
    () => visiblePages.find((page) => location.pathname === page.route || location.pathname.startsWith(`${page.route}/`))?.route ?? stageRouteNavMap[location.pathname] ?? '/console/tasks',
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

  const handleRequestError = useCallback((error: unknown) => {
    const apiError = asApiError(error)

    if (isSessionExpiredError(error)) {
      const message = apiError.message || '登录状态已失效，请重新登录。'
      setTrustSignal({
        tone: 'warning',
        title: '登录状态已过期',
        detail: message,
        recoveryHint: '请重新登录后继续操作；若反复过期，请联系管理员检查会话时长配置。',
        nextStep: { label: '重新登录', route: '/login' },
      })
      Toast.error(message)
      setUser(null)
      navigate('/login', { replace: true })
      return true
    }

    if (isForbiddenError(error)) {
      const message = apiError.message || '你没有执行该操作的权限，请联系管理员。'
      setTrustSignal({
        tone: 'warning',
        title: '权限不足，操作未执行',
        detail: message,
        recoveryHint: '确认你当前账号角色；若需要高级权限，请联系管理员开通。',
        nextStep: { label: '查看恢复指南', route: '/console/help' },
      })
      Toast.warning(message)
      return true
    }

    return false
  }, [navigate])

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
    setBootstrapLoading(true)
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
      await loadAdminData()
      if (successMessage) Toast.success(successMessage)
    } catch (error) {
      if (!handleRequestError(error)) Toast.error((error as Error).message)
    } finally {
      setBootstrapLoading(false)
    }
  }, [loadAdminData])

  const loadDatasetWorkspace = useCallback(async (datasetId: number, successMessage?: string) => {
    setWorkspaceLoading(true)
    try {
      const [nextGraph, nextQuestions, nextReasoning, nextRewards, nextArtifacts, nextPipeline, nextRuntime, nextDatasets] = await Promise.all([
        consoleApi.getDataset(datasetId),
        consoleApi.listQuestions(datasetId),
        consoleApi.listReasoning(datasetId),
        consoleApi.listRewards(datasetId),
        consoleApi.listArtifacts(datasetId),
        consoleApi.pipelineProgress(datasetId),
        consoleApi.runtimeStatus(),
        consoleApi.listDatasets(),
      ])
      setGraph(nextGraph)
      setQuestions(nextQuestions)
      setReasoning(nextReasoning)
      setRewards(nextRewards)
      setArtifacts(nextArtifacts)
      setPipelineProgress(nextPipeline)
      setStageRunMeta({})
      setRuntime(nextRuntime)
      setDatasets(nextDatasets)
      setActiveDatasetId(datasetId)
      if (successMessage) Toast.success(successMessage)
    } catch (error) {
      if (!handleRequestError(error)) Toast.error((error as Error).message)
    } finally {
      setWorkspaceLoading(false)
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
    if (!user) return
    const routeDatasetId = taskRouteDatasetId(location.pathname)
    if (!routeDatasetId) return
    if (routeDatasetId === activeDatasetId && graph?.dataset.id === routeDatasetId) return
    void loadDatasetWorkspace(routeDatasetId)
  }, [activeDatasetId, graph?.dataset.id, loadDatasetWorkspace, location.pathname, user])

  useEffect(() => {
    if (sessionLoading) return
    if (!user && location.pathname !== '/login') {
      navigate('/login', { replace: true })
      return
    }
    if (user && location.pathname === '/login') {
      navigate('/console/tasks', { replace: true })
      return
    }
    if (user && !isAdmin && location.pathname.startsWith('/console/admin/')) {
      navigate('/console/tasks', { replace: true })
    }
  }, [isAdmin, location.pathname, navigate, sessionLoading, user])

  const handleLogin = async (email: string, password: string) => {
    setAuthSubmitting(true)
    try {
      const result = await authApi.login({ email, password })
      setUser(result.user)
      setTrustSignal(null)
      Toast.success(`欢迎回来，${result.user.email}`)
      navigate('/console/tasks', { replace: true })
    } catch (error) {
      const message = (error as Error).message
      setTrustSignal({
        tone: 'warning',
        title: '登录未成功',
        detail: `系统返回：${message}`,
        recoveryHint: '请确认账号密码，若连续失败请联系管理员重置账号。',
      })
      Toast.error(message)
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
      setPipelineProgress(null)
      navigate('/login', { replace: true })
    }
  }

  const estimatePlan = async () => {
    setActionLoading(true)
    try {
      const data = await consoleApi.estimatePlan({
        rootKeyword: plannerForm.rootKeyword,
        targetSize: Number(plannerForm.targetSize),
        strategyId: Number(plannerForm.strategyId),
      })
      setEstimate(data)
      Toast.success('计划估算已刷新')
    } catch (error) {
      if (!handleRequestError(error)) Toast.error((error as Error).message)
    } finally {
      setActionLoading(false)
    }
  }

  const createDataset = async () => {
    if (!plannerForm.rootKeyword.trim() || Number(plannerForm.targetSize) <= 0) {
      setTrustSignal({
        tone: 'warning',
        title: '创建前还有必填项',
        detail: '任务主题和目标规模是创建任务的最小条件。',
        recoveryHint: '先补齐上述两项，再点击“创建任务”。',
        nextStep: { label: '回到任务创建', route: '/console/planning' },
      })
      Toast.warning('请至少填写任务主题和目标规模')
      return
    }

    setActionLoading(true)
    try {
      const estimateSnapshot = estimate ?? await consoleApi.estimatePlan({
        rootKeyword: plannerForm.rootKeyword,
        targetSize: Number(plannerForm.targetSize),
        strategyId: Number(plannerForm.strategyId),
      })

      if (!estimate) {
        setEstimate(estimateSnapshot)
      }

      const created = await consoleApi.createDataset({
        name: plannerForm.name || `${plannerForm.rootKeyword} 数据集`,
        rootKeyword: plannerForm.rootKeyword,
        targetSize: Number(plannerForm.targetSize),
        strategyId: Number(plannerForm.strategyId),
        providerId: Number(plannerForm.providerId),
        storageProfileId: Number(plannerForm.storageProfileId),
        status: 'draft',
        estimate: estimateSnapshot,
      })
      setActiveDatasetId(created.id)
      setTrustSignal({
        tone: 'success',
        title: '任务创建成功',
        detail: '你现在可以进入“整理主题”继续下一步。',
        recoveryHint: '如果页面未自动刷新，可点击右上角刷新按钮同步状态。',
        nextStep: { label: '进入整理主题', route: '/console/domains' },
      })
      navigate(`/console/tasks/${created.id}`)
      Toast.success('任务已创建')
      await loadBootstrap()
    } catch (error) {
      const message = (error as Error).message
      setTrustSignal({
        tone: 'warning',
        title: '创建任务失败',
        detail: `系统返回：${message}`,
        recoveryHint: '请检查策略、AI 服务和存储配置是否可用，再重试。',
        nextStep: { label: '查看配置', route: '/console/planning' },
      })
      Toast.error(message)
    } finally {
      setActionLoading(false)
    }
  }

  const generateDomains = async () => {
    if (!activeDatasetId) return Toast.warning('请先选择任务')
    setActionLoading(true)
    try {
      const nextGraph = await consoleApi.generateDomains(activeDatasetId)
      setGraph(nextGraph)
      Toast.success(`已生成 ${nextGraph.domains.length} 个方向`)
    } catch (error) {
      if (handleRequestError(error)) return

      const message = (error as Error).message
      const normalizedMessage = message.toLowerCase()
      const isProviderDecodeError = normalizedMessage.includes('provider returned undecodable response')
        || normalizedMessage.includes('provider returned no choices')
        || normalizedMessage.includes('chat.completion.chunk')

      if (isProviderDecodeError) {
        setTrustSignal({
          tone: 'warning',
          title: 'AI 服务返回异常，方向未生成',
          detail: `系统返回：${message}`,
          recoveryHint: isAdmin
            ? '请到“AI 服务”页切换模型（建议优先使用标准 chat/completions 兼容模型）后重试；也可先回到任务创建页缩小主题范围。'
            : '请联系管理员切换模型后重试；你也可以先回到任务创建页缩小主题范围，再次发起生成。',
          nextStep: isAdmin
            ? { label: '去 AI 服务', route: '/console/admin/providers' }
            : { label: '查看恢复指南', route: '/console/help' },
        })
      } else {
        setTrustSignal({
          tone: 'warning',
          title: '主题结构生成失败',
          detail: `系统返回：${message}`,
          recoveryHint: '可先回到任务创建页调整主题和规模后重试；若持续失败，请查看帮助恢复页。',
          nextStep: { label: '回到任务创建', route: '/console/planning' },
        })
      }

      Toast.error(message)
    } finally {
      setActionLoading(false)
    }
  }

  const saveGraph = async () => {
    if (!graph) return
    setActionLoading(true)
    try {
      await consoleApi.updateGraph(graph.dataset.id, graph.domains)
      Toast.success('方向命名修改已保存')
    } catch (error) {
      if (!handleRequestError(error)) Toast.error((error as Error).message)
    } finally {
      setActionLoading(false)
    }
  }

  const confirmDomains = async () => {
    if (!graph) return
    setActionLoading(true)
    try {
      await consoleApi.confirmDomains(graph.dataset.id)
      Toast.success('主题结构已确认')
      navigate('/console/questions')
      await loadDatasetWorkspace(graph.dataset.id)
    } catch (error) {
      const message = (error as Error).message
      setTrustSignal({
        tone: 'warning',
        title: '确认主题结构失败',
        detail: `系统返回：${message}`,
        recoveryHint: '请先保存命名修改，并确认当前数据集仍可访问后再重试。',
        nextStep: { label: '返回整理主题', route: '/console/domains' },
      })
      Toast.error(message)
    } finally {
      setActionLoading(false)
    }
  }

  const generateQuestions = async () => {
    if (!activeDatasetId) return
    setActionLoading(true)
    try {
      const result = await consoleApi.generateQuestions(activeDatasetId)
      setStageRunMeta((current) => ({ ...current, questions: result }))
      setTrustSignal({
        tone: 'info',
        title: '题目生成已开始',
        detail: `${result.message}，当前阶段 ETA：${etaLabel('questions_queued', runtime?.queueDepth ?? 0, result.acceptedAt)}。你可以先切换到其他页面继续操作，后台会持续处理。`,
        recoveryHint: '建议 60~90 秒后刷新一次；如果等待任务数较高，可改为 2~3 分钟再刷新。',
        nextStep: { label: '查看题目页', route: '/console/questions' },
      })
      Toast.success(result.message)
      await loadDatasetWorkspace(activeDatasetId)
    } catch (error) {
      const message = (error as Error).message
      setTrustSignal({
        tone: 'warning',
        title: '题目生成未启动',
        detail: `系统返回：${message}`,
        recoveryHint: '先确认主题已确认，再重新发起生成。',
        nextStep: { label: '返回整理主题', route: '/console/domains' },
      })
      Toast.error(message)
    } finally {
      setActionLoading(false)
    }
  }

  const generateReasoning = async () => {
    if (!activeDatasetId) return
    setActionLoading(true)
    try {
      const result = await consoleApi.generateReasoning(activeDatasetId)
      setStageRunMeta((current) => ({ ...current, reasoning: result }))
      setTrustSignal({
        tone: 'info',
        title: '答案生成已开始',
        detail: `${result.message}，当前阶段 ETA：${etaLabel('reasoning_queued', runtime?.queueDepth ?? 0, result.acceptedAt)}。你可以先切换到其他页面，后台会持续处理并保留进度。`,
        recoveryHint: '建议 60~90 秒后刷新一次；若等待任务数较高，可改为 2~3 分钟再刷新。',
        nextStep: { label: '查看推理页', route: '/console/reasoning' },
      })
      Toast.success(result.message)
      await loadDatasetWorkspace(activeDatasetId)
    } catch (error) {
      const message = (error as Error).message
      setTrustSignal({
        tone: 'warning',
        title: '答案生成未启动',
        detail: `系统返回：${message}`,
        recoveryHint: '请先确认题目数量充足，再重新发起。',
        nextStep: { label: '检查题目', route: '/console/questions' },
      })
      Toast.error(message)
    } finally {
      setActionLoading(false)
    }
  }

  const generateRewards = async () => {
    if (!activeDatasetId) return
    setActionLoading(true)
    try {
      const result = await consoleApi.generateRewards(activeDatasetId)
      setStageRunMeta((current) => ({ ...current, rewards: result }))
      setTrustSignal({
        tone: 'info',
        title: '质量评估已开始',
        detail: `${result.message}，当前阶段 ETA：${etaLabel('rewards_queued', runtime?.queueDepth ?? 0, result.acceptedAt)}。你可以先切换到其他页面，后台会持续处理并保留进度。`,
        recoveryHint: '建议 60~90 秒后刷新一次；若等待任务数较高，可改为 2~3 分钟再刷新。',
        nextStep: { label: '查看质量评估', route: '/console/rewards' },
      })
      Toast.success(result.message)
      await loadDatasetWorkspace(activeDatasetId)
    } catch (error) {
      const message = (error as Error).message
      setTrustSignal({
        tone: 'warning',
        title: '质量评估未启动',
        detail: `系统返回：${message}`,
        recoveryHint: '请先确认答案记录已生成，再重新发起。',
        nextStep: { label: '检查推理结果', route: '/console/reasoning' },
      })
      Toast.error(message)
    } finally {
      setActionLoading(false)
    }
  }

  const generateExport = async () => {
    if (!activeDatasetId) return
    setActionLoading(true)
    try {
      const result = await consoleApi.generateExport(activeDatasetId)
      setStageRunMeta((current) => ({ ...current, export: result }))
      setTrustSignal({
        tone: 'info',
        title: '导出任务已开始',
        detail: `${result.message}，当前阶段 ETA：${etaLabel('export_queued', runtime?.queueDepth ?? 0, result.acceptedAt)}。你可以先切换到其他页面，后台会持续处理并保留进度。`,
        recoveryHint: '建议 60~90 秒后刷新一次；若等待任务数较高，可改为 2~3 分钟再刷新。',
        nextStep: { label: '前往结果交付', route: '/console/exports' },
      })
      Toast.success(result.message)
      await loadDatasetWorkspace(activeDatasetId)
    } catch (error) {
      const message = (error as Error).message
      setTrustSignal({
        tone: 'warning',
        title: '导出任务未启动',
        detail: `系统返回：${message}`,
        recoveryHint: '先确认评估阶段已完成，再重新发起导出。',
        nextStep: { label: '返回质量评估', route: '/console/rewards' },
      })
      Toast.error(message)
    } finally {
      setActionLoading(false)
    }
  }

  const copyArtifactKey = async (objectKey: string) => {
    try {
      await navigator.clipboard.writeText(objectKey)
      Toast.success('文件标识已复制，可交给运维或下游下载')
    } catch {
      Toast.warning('复制失败，请手动记录文件标识后下载交付')
    }
  }

  const downloadArtifact = async (artifact: Artifact) => {
    if (!artifact.datasetId || !artifact.id) return
    try {
      const response = await fetch(consoleApi.artifactDownloadUrl(artifact.datasetId, artifact.id), {
        credentials: 'include',
      })
      if (!response.ok) {
        const message = await response.text()
        throw new Error(message || '下载失败，请稍后重试')
      }
      const blob = await response.blob()
      const disposition = response.headers.get('Content-Disposition') || ''
      const matched = disposition.match(/filename="?([^";]+)"?/)
      const fileName = matched?.[1] || artifactDisplayName(artifact.objectKey)
      const url = window.URL.createObjectURL(blob)
      const link = document.createElement('a')
      link.href = url
      link.download = fileName
      document.body.appendChild(link)
      link.click()
      document.body.removeChild(link)
      window.URL.revokeObjectURL(url)
      Toast.success('结果文件下载已开始')
    } catch (error) {
      const message = error instanceof Error ? error.message : '下载失败，请稍后重试'
      setTrustSignal({
        tone: 'warning',
        title: '结果文件下载失败',
        detail: `系统返回：${message}`,
        recoveryHint: '请先刷新结果页，确认交付文件已生成；若重复失败，请联系管理员检查存储配置。',
        nextStep: { label: '返回结果交付', route: '/console/exports' },
      })
      Toast.error(message)
    }
  }

  const saveProvider = async () => {
    setActionLoading(true)
    try {
      await consoleApi.saveProvider(providerDraft)
      Toast.success('AI 服务已保存')
      setProviderDraft(makeEmptyProviderDraft())
      setProviderModels([])
      setProviderTestResult(null)
      setProviderModalVisible(false)
      await loadBootstrap()
    } catch (error) {
      if (!handleRequestError(error)) Toast.error((error as Error).message)
    } finally {
      setActionLoading(false)
    }
  }

  const validateProviderDraftForRemoteAction = () => {
    const providerType = String(providerDraft.providerType ?? '').trim()
    const baseURL = String(providerDraft.baseUrl ?? '').trim()

    if (!providerType) {
      Toast.warning('请先选择或填写服务类型')
      return false
    }

    if (!baseURL) {
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
      if (!handleRequestError(error)) Toast.error((error as Error).message)
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
      if (!handleRequestError(error)) Toast.error((error as Error).message)
    } finally {
      setProviderTestLoading(false)
    }
  }

  const saveStorage = async () => {
    setActionLoading(true)
    try {
      await consoleApi.saveStorageProfile(storageDraft)
      Toast.success('存储配置已保存')
      setStorageDraft(makeEmptyStorageDraft())
      setStorageModalVisible(false)
      await loadBootstrap()
    } catch (error) {
      if (!handleRequestError(error)) Toast.error((error as Error).message)
    } finally {
      setActionLoading(false)
    }
  }

  const saveStrategy = async () => {
    setActionLoading(true)
    try {
      await consoleApi.saveStrategy(strategyDraft)
      Toast.success('生成策略已保存')
      setStrategyDraft(makeEmptyStrategyDraft())
      setStrategyModalVisible(false)
      await loadBootstrap()
    } catch (error) {
      if (!handleRequestError(error)) Toast.error((error as Error).message)
    } finally {
      setActionLoading(false)
    }
  }

  const savePrompt = async () => {
    setActionLoading(true)
    try {
      await consoleApi.savePrompt(promptDraft)
      Toast.success('生成指令模板已保存')
      setPromptDraft(makeEmptyPromptDraft())
      setPromptModalVisible(false)
      await loadBootstrap()
    } catch (error) {
      if (!handleRequestError(error)) Toast.error((error as Error).message)
    } finally {
      setActionLoading(false)
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
    { icon: Database, label: '任务总数', value: runtime?.datasetCount ?? 0, helper: '当前正在跟进的任务数量' },
    { icon: Layers3, label: '题目结果', value: runtime?.questionCount ?? 0, helper: '已经生成的题目数量' },
    { icon: BrainCircuit, label: '答案结果', value: runtime?.reasoningCount ?? 0, helper: '已经生成的答案与思路数量' },
    { icon: HardDriveDownload, label: '结果文件', value: runtime?.artifactCount ?? 0, helper: '已经准备好的导出结果文件' },
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

  const renderOverview = () => {
    const queueDepth = runtime?.queueDepth ?? 0
    const recentDatasets = [...datasets]
      .sort((left, right) => Date.parse(right.updatedAt) - Date.parse(left.updatedAt))
      .slice(0, 4)
    const recentArtifacts = [...artifacts]
      .sort((left, right) => Date.parse(right.createdAt) - Date.parse(left.createdAt))
      .slice(0, 5)
    const lowScoreCount = rewards.filter((item) => item.score < 0.5).length
    const queuedMinutes = activeDataset?.status.endsWith('_queued') ? minutesSince(activeStageRun?.acceptedAt) : null

    const riskItems: Array<{ level: 'high' | 'medium' | 'low'; title: string; detail: string }> = []
    if (lowScoreCount > 0) {
      riskItems.push({
        level: 'high',
        title: '低分样本需要回修',
        detail: `当前有 ${lowScoreCount} 条评分低于 0.5，建议先回看答案阶段并重新评估。`,
      })
    }
    if (activeDataset?.status.endsWith('_queued') && queuedMinutes !== null && queuedMinutes >= 15) {
      riskItems.push({
        level: 'high',
        title: '排队时长偏高',
        detail: `当前阶段已等待 ${queuedMinutes} 分钟，建议刷新状态并检查上游配置。`,
      })
    }
    if (queueDepth >= 8) {
      riskItems.push({
        level: 'medium',
        title: '系统队列压力较高',
        detail: `前方约 ${queueDepth} 个任务，建议降低刷新频率并优先处理已产出内容。`,
      })
    }
    if (exportDeliveryPending) {
      riskItems.push({
        level: 'medium',
        title: '导出交付仍在落盘',
        detail: '导出计算已完成，正在写入交付文件，请勿重复触发导出。',
      })
    }
    if (activeDataset?.status === 'export_generated' && !exportDeliveryPending && artifacts.length === 0) {
      riskItems.push({
        level: 'high',
        title: '导出完成但未发现交付文件',
        detail: '建议刷新导出页；若仍为空，请检查存储配置与权限。',
      })
    }
    if (riskItems.length === 0) {
      riskItems.push({
        level: 'low',
        title: '当前无明显风险',
        detail: '流程状态稳定，可按下一步动作继续推进。',
      })
    }

    const nextRoute = (() => {
      if (!activeDataset) return '/console/planning'
      switch (activeDataset.status) {
        case 'draft':
          return '/console/domains'
        case 'domains_confirmed':
        case 'questions_queued':
        case 'questions_generated':
          return '/console/questions'
        case 'reasoning_queued':
        case 'reasoning_generated':
          return '/console/reasoning'
        case 'rewards_queued':
        case 'rewards_generated':
          return '/console/rewards'
        case 'export_queued':
        case 'export_generated':
          return '/console/exports'
        default:
          return '/console/tasks'
      }
    })()

    const hasHighRisk = riskItems.some((item) => item.level === 'high')

    return (
      <div className="console-page-shell">
        <PageHeader
          badge="任务中心 / 辅助总览"
          title="围绕任务、结果、风险与动作做日常推进"
          description="首页聚合我的任务、最近结果、风险提醒与待办动作，帮助你快速判断今天先做什么。"
          actions={
            <>
              <Button theme="solid" type="primary" icon={<CirclePlus size={16} />} onClick={() => navigate('/console/planning')}>
                新建任务
              </Button>
              <Button loading={bootstrapLoading} icon={<RefreshCw size={16} />} onClick={() => void loadBootstrap('工作台数据已刷新')}>
                刷新工作台
              </Button>
              <Button icon={<Target size={16} />} onClick={() => navigate('/console/tasks')}>查看我的任务</Button>
            </>
          }
        />

        <Banner
          type={hasHighRisk ? 'warning' : 'info'}
          icon={<Bell size={16} />}
          description={hasHighRisk ? '检测到高优先级风险，请先处理风险提醒区后再继续推进。' : '当前工作台状态稳定，可按待办动作继续执行。'}
        />

        <div className="console-card-grid-2">
          <Card className="console-panel" bodyStyle={{ padding: 20 }}>
            <Title heading={4} className="!mb-0">我的任务</Title>
            <Text className="mt-2 block console-caption">固定显示当前任务上下文，并支持从最近任务快速切换。</Text>
            <div className="mt-5 console-summary-grid">
              <div className="console-summary-row"><span>当前任务</span><Text strong>{activeDataset?.name ?? '尚未创建任务'}</Text></div>
              <div className="console-summary-row"><span>任务 ID</span><Text strong>{activeDataset?.id ?? '—'}</Text></div>
              <div className="console-summary-row"><span>阶段状态</span><Text strong>{activeDataset ? (exportDeliveryPending ? '导出收尾中' : statusLabel(activeDataset.status)) : '—'}</Text></div>
              <div className="console-summary-row"><span>完成进度</span><Text strong>{activePipeline ? `${activePipeline.completionPercent}%` : activeDataset ? `${progressPercent(activeDataset.status)}%` : '—'}</Text></div>
              <div className="console-summary-row"><span>当前 ETA</span><Text strong>{activeEta}</Text></div>
              <div className="console-summary-row"><span>等待状态</span><Text strong>{activeDataset ? (exportDeliveryPending ? '导出状态已完成，正在确认交付文件，请稍后刷新' : waitingStateLabel(activeDataset.status, queueDepth)) : '创建任务后显示'}</Text></div>
            </div>
            <div className="mt-5 console-stack">
              <Text strong>最近任务</Text>
              {recentDatasets.length > 0 ? (
                recentDatasets.map((dataset) => (
                  <div key={dataset.id} className="console-domain-item">
                    <div className="flex items-center justify-between gap-3">
                      <Space>
                        <Tag color="blue">任务 #{dataset.id}</Tag>
                        <Tag color="cyan">{statusLabel(dataset.status)}</Tag>
                      </Space>
                      <Button size="small" onClick={() => void loadDatasetWorkspace(dataset.id, `已切换到任务「${dataset.name}」`)}>切换</Button>
                    </div>
                    <Text className="mt-2 block" strong>{dataset.name}</Text>
                    <Text className="mt-1 block console-caption">{dataset.rootKeyword} · 更新于 {formatTime(dataset.updatedAt)}</Text>
                  </div>
                ))
              ) : (
                <div className="console-stack">
                  <Empty description="暂无任务" />
                  <Button theme="solid" type="primary" icon={<CirclePlus size={16} />} onClick={() => navigate('/console/planning')}>新建任务</Button>
                </div>
              )}
            </div>
          </Card>

          <Card className="console-panel" bodyStyle={{ padding: 20 }}>
            <Title heading={4} className="!mb-0">最近结果</Title>
            <Text className="mt-2 block console-caption">查看最近结果和交付文件，避免遗漏可直接使用的成果。</Text>
            <div className="mt-5 console-summary-grid">
              <div className="console-summary-row"><span>题目结果</span><Text strong>{questions.length}</Text></div>
              <div className="console-summary-row"><span>答案结果</span><Text strong>{reasoning.length}</Text></div>
              <div className="console-summary-row"><span>评分结果</span><Text strong>{rewards.length}</Text></div>
              <div className="console-summary-row"><span>交付文件</span><Text strong>{artifacts.length}</Text></div>
            </div>
            <div className="mt-5 console-stack">
              <Text strong>最近产出文件</Text>
              {recentArtifacts.length > 0 ? (
                recentArtifacts.map((artifact) => (
                  <div key={artifact.id ?? artifact.objectKey} className="console-domain-item">
                    <div className="flex items-center justify-between gap-3">
                      <Space>
                        <Tag color="green">{artifactLabel(artifact.artifactType)}</Tag>
                        <Tag color="grey">{artifactContentTypeLabel(artifact.contentType)}</Tag>
                      </Space>
                      <Button size="small" onClick={() => void downloadArtifact(artifact)}>下载</Button>
                    </div>
                    <Text className="mt-2 block" strong>{artifactDisplayName(artifact.objectKey)}</Text>
                    <Text className="mt-1 block console-caption">{artifactUsageLabel(artifactUsageCategory(artifact))} · {formatTime(artifact.createdAt)}</Text>
                  </div>
                ))
              ) : (
                <Empty description="暂无导出结果" />
              )}
            </div>
          </Card>
        </div>

        <div className="console-card-grid-2">
          <Card className="console-panel" bodyStyle={{ padding: 20 }}>
            <Title heading={4} className="!mb-0">风险提醒</Title>
            <Text className="mt-2 block console-caption">高风险优先处理，中低风险按节奏跟进。</Text>
            <div className="mt-5 console-stack">
              {riskItems.map((risk) => (
                <div key={`${risk.level}-${risk.title}`} className="console-domain-item">
                  <div className="flex items-center justify-between gap-3">
                    <Text strong>{risk.title}</Text>
                    <Tag color={risk.level === 'high' ? 'red' : risk.level === 'medium' ? 'orange' : 'green'}>
                      {risk.level === 'high' ? '高风险' : risk.level === 'medium' ? '中风险' : '低风险'}
                    </Tag>
                  </div>
                  <Text className="mt-2 block console-caption">{risk.detail}</Text>
                </div>
              ))}
            </div>
          </Card>

          <Card className="console-panel" bodyStyle={{ padding: 20 }}>
            <Title heading={4} className="!mb-0">待办动作</Title>
            <Text className="mt-2 block console-caption">按推荐顺序执行，减少重复刷新与无效等待。</Text>
            <div className="mt-5 console-summary-grid">
              <div className="console-summary-row"><span>下一步</span><Text strong>{activeDataset ? (exportDeliveryPending ? '等待交付文件出现后再下载结果' : nextActionLabel(activeDataset.status)) : '先点击“新建任务”'}</Text></div>
              <div className="console-summary-row"><span>等待原因</span><Text strong>{activeDataset ? (exportDeliveryPending ? '导出计算已完成，系统正在将结果写入交付存储。' : waitingReasonLabel(activeDataset.status, queueDepth)) : '创建任务后显示'}</Text></div>
              <div className="console-summary-row"><span>建议动作</span><Text strong>{activeDataset ? (exportDeliveryPending ? '暂时无需重复触发导出，等待交付文件落盘后再下载。' : waitingActionLabel(activeDataset.status)) : '点击顶部“新建任务”开始'}</Text></div>
              <div className="console-summary-row"><span>刷新节奏</span><Text strong>{activeDataset ? (exportDeliveryPending ? '系统会每 20 秒自动刷新；检测到交付文件后将自动变为可下载。' : refreshExpectationLabel(activeDataset.status, queueDepth)) : '创建任务后显示'}</Text></div>
              <div className="console-summary-row"><span>进度保障</span><Text strong>{activeDataset ? (exportDeliveryPending ? '任务已完成导出计算，正在落盘交付文件，请勿重复触发导出。' : trustMessageLabel(activeDataset.status)) : '创建任务后显示'}</Text></div>
            </div>
            <Space className="mt-5" wrap>
              <Button theme="solid" type="primary" onClick={() => navigate(nextRoute)}>{activeDataset ? nextActionLabel(activeDataset.status) : '新建任务'}</Button>
              <Button onClick={() => navigate('/console/results')}>查看最近结果</Button>
              <Button onClick={() => navigate('/console/help')}>查看恢复指引</Button>
            </Space>
          </Card>
        </div>

        <Card className="console-panel" bodyStyle={{ padding: 20 }}>
          <Title heading={4} className="!mb-0">当前系统状态</Title>
          <Text className="mt-2 block console-caption">仅保留与你当前任务相关的轻量系统信号，详细运行态请进入运营监控。</Text>
          <div className="mt-5 console-card-grid-4">
            {overviewCards.map((item) => <StatCard key={item.label} {...item} />)}
          </div>
          <div className="mt-6 console-summary-grid">
            <div className="console-summary-row"><span>等待任务</span><Text strong>{runtime?.queueDepth ?? 0}</Text></div>
            <div className="console-summary-row"><span>活跃 AI 服务</span><Text strong>{dashboard?.activeProviderCount ?? 0}</Text></div>
            <div className="console-summary-row"><span>存储配置数</span><Text strong>{dashboard?.storageProfileCount ?? 0}</Text></div>
            <div className="console-summary-row"><span>策略数量</span><Text strong>{dashboard?.strategyCount ?? 0}</Text></div>
            <div className="console-summary-row"><span>指令模板数</span><Text strong>{dashboard?.promptCount ?? 0}</Text></div>
          </div>
        </Card>
      </div>
    )
  }


  const renderTaskIndex = () => {
    const recentDatasets = [...datasets]
      .sort((left, right) => Date.parse(right.updatedAt) - Date.parse(left.updatedAt))
      .slice(0, 10)

    return (
      <div className="console-page-shell">
        <PageHeader
          badge="工作台 / 我的任务"
          title="先看当前待办，再决定新建任务或继续已有任务"
          description="这里优先展示当前任务、推荐动作和最近进展；要创建新任务就直接进入“新建任务”，已有任务则从列表继续推进。"
          actions={
            <>
              <Button theme="solid" type="primary" icon={<CirclePlus size={16} />} onClick={() => navigate('/console/planning')}>新建任务</Button>
              <Button onClick={() => navigate('/console/results')}>查看数据资产</Button>
              <Button icon={<RefreshCw size={16} />} loading={bootstrapLoading} onClick={() => void loadBootstrap('工作台已刷新')}>刷新工作台</Button>
            </>
          }
        />

        <div className="console-card-grid-2">
          <Card className="console-panel" bodyStyle={{ padding: 20 }}>
            <Title heading={4} className="!mb-0">当前待办</Title>
            <Text className="mt-2 block console-caption">把最常见的两个动作固定在这里：新建任务，或继续当前任务。</Text>
            <div className="mt-5 console-summary-grid">
              <div className="console-summary-row"><span>当前任务总数</span><Text strong>{datasets.length}</Text></div>
              <div className="console-summary-row"><span>最近活跃任务</span><Text strong>{recentDatasets[0]?.name ?? '暂无任务'}</Text></div>
              <div className="console-summary-row"><span>等待任务数</span><Text strong>{runtime?.queueDepth ?? 0}</Text></div>
              <div className="console-summary-row"><span>推荐动作</span><Text strong>{recentDatasets.length > 0 ? '继续当前任务，或新建一个任务' : '先新建任务'}</Text></div>
            </div>
            <Space className="mt-5" wrap>
              <Button theme="solid" type="primary" icon={<CirclePlus size={16} />} onClick={() => navigate('/console/planning')}>新建任务</Button>
              <Button onClick={() => navigate(recentDatasets[0] ? `/console/tasks/${recentDatasets[0].id}` : '/console/tasks')}>继续当前任务</Button>
            </Space>
          </Card>

          <Card className="console-panel" bodyStyle={{ padding: 20 }}>
            <Title heading={4} className="!mb-0">任务列表</Title>
            <Text className="mt-2 block console-caption">点击任意任务后进入它的独立详情页，避免同页来回滚动。</Text>
            <div className="mt-5 console-stack">
              {recentDatasets.length > 0 ? recentDatasets.map((dataset) => (
                <div key={dataset.id} className="console-domain-item">
                  <div className="flex items-center justify-between gap-3">
                    <Space>
                      <Tag color="blue">任务 #{dataset.id}</Tag>
                      <Tag color={dataset.id === activeDatasetId ? 'green' : 'cyan'}>{dataset.id === activeDatasetId ? '当前任务' : statusLabel(dataset.status)}</Tag>
                    </Space>
                    <Button size="small" onClick={() => navigate(`/console/tasks/${dataset.id}`)}>{dataset.id === activeDatasetId ? '进入详情' : '继续任务'}</Button>
                  </div>
                  <Text className="mt-2 block" strong>{dataset.name}</Text>
                  <Text className="mt-1 block console-caption">{dataset.rootKeyword} · 更新于 {formatTime(dataset.updatedAt)}</Text>
                </div>
              )) : <Empty description="暂无任务" />}
            </div>
          </Card>
        </div>
      </div>
    )
  }

  const renderTaskDetail = () => {
    const queueDepth = runtime?.queueDepth ?? 0
    const datasetStatus = activeDataset?.status ?? 'draft'

    if (!activeDataset) {
      return (
        <div className="console-page-shell">
          <PageHeader
            badge="任务中心 / 任务详情"
            title="未找到任务详情"
            description="请先从任务列表进入某个任务，或先创建新任务。"
            actions={<Button onClick={() => navigate('/console/tasks')}>返回任务列表</Button>}
          />
          <EmptyCard title="暂无任务详情" description="进入 /console/tasks 查看任务列表后，再打开具体任务。" />
        </div>
      )
    }

    const pipelineStageMap = new Map((activePipeline?.stages ?? []).map((stage) => [stage.key, stage]))
    const questionsStage = pipelineStageMap.get('questions')
    const reasoningStage = pipelineStageMap.get('reasoning')
    const rewardsStage = pipelineStageMap.get('rewards')
    const exportStage = pipelineStageMap.get('export')

    const inferStageState = (stage: StageKey): 'pending' | 'queued' | 'in_progress' | 'completed' => {
      switch (stage) {
        case 'questions':
          if (datasetStatus === 'questions_queued') return 'queued'
          if (['questions_generated', 'reasoning_queued', 'reasoning_generated', 'rewards_queued', 'rewards_generated', 'export_queued', 'export_generated'].includes(datasetStatus)) return 'completed'
          return 'pending'
        case 'reasoning':
          if (datasetStatus === 'reasoning_queued') return 'queued'
          if (['reasoning_generated', 'rewards_queued', 'rewards_generated', 'export_queued', 'export_generated'].includes(datasetStatus)) return 'completed'
          return 'pending'
        case 'rewards':
          if (datasetStatus === 'rewards_queued') return 'queued'
          if (['rewards_generated', 'export_queued', 'export_generated'].includes(datasetStatus)) return 'completed'
          return 'pending'
        case 'export':
          if (datasetStatus === 'export_queued') return 'queued'
          if (datasetStatus === 'export_generated') return 'completed'
          return 'pending'
        default:
          return 'pending'
      }
    }

    const stageCards: Array<{
      key: string
      label: string
      route: string
      state: 'pending' | 'queued' | 'in_progress' | 'completed' | 'failed'
      summary: string
      count: number
    }> = [
      {
        key: 'domains',
        label: '第 1 步：主题结构',
        route: '/console/domains',
        state: activeDataset.status === 'draft' ? 'in_progress' : 'completed',
        summary: activeDataset.status === 'draft' ? '先确认主题结构，系统才能继续生成问题。' : '主题结构已确认，可以进入下一步。',
        count: graph?.domains.length ?? 0,
      },
      {
        key: 'questions',
        label: '第 2 步：问题生成',
        route: '/console/questions',
        state: questionsStage?.state ?? inferStageState('questions'),
        summary: questionsStage?.summary ?? (datasetStatus === 'domains_confirmed' ? '主题结构已确认，下一步应开始生成问题。' : datasetStatus === 'questions_generated' ? '问题已经生成完成，可以继续生成答案内容。' : '进入本阶段查看问题生成结果与覆盖情况。'),
        count: questionsStage?.count ?? questions.length,
      },
      {
        key: 'reasoning',
        label: '第 3 步：答案内容',
        route: '/console/reasoning',
        state: reasoningStage?.state ?? inferStageState('reasoning'),
        summary: reasoningStage?.summary ?? (datasetStatus === 'questions_generated' ? '问题已准备好，下一步应生成答案内容。' : datasetStatus === 'reasoning_generated' ? '答案内容已经生成完成，可以继续质量评估。' : '进入本阶段查看答案内容是否完整可用。'),
        count: reasoningStage?.count ?? reasoning.length,
      },
      {
        key: 'rewards',
        label: '第 4 步：质量评估',
        route: '/console/rewards',
        state: rewardsStage?.state ?? inferStageState('rewards'),
        summary: rewardsStage?.summary ?? (datasetStatus === 'reasoning_generated' ? '答案内容已准备好，下一步应做质量评估。' : datasetStatus === 'rewards_generated' ? '质量评估已完成，可以进入导出交付。' : '进入本阶段查看质量评估结果与风险项。'),
        count: rewardsStage?.count ?? rewards.length,
      },
      {
        key: 'export',
        label: '第 5 步：导出交付',
        route: '/console/exports',
        state: exportStage?.state ?? inferStageState('export'),
        summary: exportDeliveryPending
          ? '导出计算已完成，交付文件正在落盘。'
          : exportStage?.summary ?? (datasetStatus === 'rewards_generated' ? '质量评估已完成，下一步应导出交付文件。' : datasetStatus === 'export_generated' ? '导出交付已完成，可以下载文件。' : '进入本阶段查看导出结果和交付文件。'),
        count: exportStage?.count ?? artifacts.length,
      },
    ]

    const progressValue = activePipeline ? activePipeline.completionPercent : progressPercent(activeDataset.status)
    const currentStageLabel = exportDeliveryPending ? '导出收尾中' : statusLabel(activeDataset.status)
    const warningQuestionCount = questions.filter((item) => item.status !== 'generated').length
    const missingReasoningCount = reasoning.filter((item) => !item.reasoning.trim()).length
    const lowRewardCount = rewards.filter((item) => item.score < 0.5).length
    const deliveryArtifactCount = artifacts.filter((item) => artifactUsageCategory(item) === 'delivery').length
    const reviewArtifactCount = artifacts.filter((item) => artifactUsageCategory(item) === 'review').length
    const otherArtifactCount = artifacts.filter((item) => artifactUsageCategory(item) === 'other').length
    const workbenchStats = [
      { label: '主题结构', value: graph?.domains.length ?? 0, helper: '当前任务主题节点' },
      { label: '问题结果', value: questions.length, helper: '待进入答案阶段的问题数' },
      { label: '答案内容', value: reasoning.length, helper: '含最终答案与思考过程' },
      { label: '质量评估', value: rewards.length, helper: '已完成评分的记录数' },
      { label: '导出文件', value: artifacts.length, helper: '可交付或可复核资产' },
      { label: '等待任务', value: queueDepth, helper: '当前排队中的后台任务' },
    ]

    return (
      <div className="console-page-shell">
        <PageHeader
          badge={`我的任务 / 任务 #${activeDataset.id}`}
          title={activeDataset.name}
          description="把任务推进、质量抽检与导出交付收口到单页工作台；先看当前待办，再按生命周期继续推进。"
          actions={
            <>
              <Button onClick={() => navigate('/console/tasks')}>返回我的任务</Button>
              <Button icon={<RefreshCw size={16} />} loading={workspaceLoading} onClick={() => void loadDatasetWorkspace(activeDataset.id, '任务工作台已刷新')}>刷新任务</Button>
            </>
          }
        />

        <Card className="console-focus-card" bodyStyle={{ padding: 24 }}>
          <div className="console-workbench-hero">
            <div>
              <div className="flex flex-wrap items-center gap-2">
                <Tag color="blue">任务主题：{activeDataset.rootKeyword}</Tag>
                <Tag color="cyan">当前阶段：{currentStageLabel}</Tag>
                <Tag color="green">完成进度：{progressValue}%</Tag>
              </div>
              <Title heading={3} className="!mb-0 mt-4">当前任务工作台</Title>
              <Text className="mt-2 block console-caption">先看整体状态和下一步动作，再按生命周期、抽检重点和交付资产推进；所有现有功能仍保留在当前任务页。</Text>
            </div>
            <div className="console-workbench-hero-actions">
              <Button theme="solid" type="primary" onClick={() => document.getElementById('stage-lifecycle')?.scrollIntoView({ behavior: 'smooth', block: 'start' })}>{nextActionLabel(activeDataset.status)}</Button>
              <Button onClick={() => document.getElementById('stage-quality')?.scrollIntoView({ behavior: 'smooth', block: 'start' })}>查看质量抽检</Button>
              <Button onClick={() => document.getElementById('stage-assets')?.scrollIntoView({ behavior: 'smooth', block: 'start' })}>查看导出资产</Button>
            </div>
          </div>
          <div className="console-workbench-stat-grid mt-5">
            {workbenchStats.map((item) => (
              <div key={item.label} className="console-workbench-stat-card">
                <Text className="console-caption">{item.label}</Text>
                <div className="console-stat-value mt-2">{item.value}</div>
                <Text className="mt-2 block console-caption">{item.helper}</Text>
              </div>
            ))}
          </div>
        </Card>

        <div className="console-workbench-layout">
          <div className="console-workbench-sidebar">
            <Card className="console-panel" bodyStyle={{ padding: 20 }}>
              <Title heading={5} className="!mb-0">任务上下文</Title>
              <div className="mt-4 console-summary-grid">
                <div className="console-summary-row"><span>任务 ID</span><Text strong>{activeDataset.id}</Text></div>
                <div className="console-summary-row"><span>最新更新时间</span><Text strong>{formatTime(activeDataset.updatedAt)}</Text></div>
                <div className="console-summary-row"><span>阶段 ETA</span><Text strong>{activeEta}</Text></div>
                <div className="console-summary-row"><span>等待状态</span><Text strong>{waitingStateLabel(activeDataset.status, queueDepth)}</Text></div>
              </div>
            </Card>

            <Card className="console-panel" bodyStyle={{ padding: 20 }}>
              <Title heading={5} className="!mb-0">生命周期导航</Title>
              <div className="mt-4 console-stack">
                {stageCards.map((stage) => {
                  const style = stageStateStyle(stage.state)
                  return (
                    <button
                      key={stage.key}
                      type="button"
                      className={clsx('console-workbench-stage-nav', `is-${stage.state}`)}
                      onClick={() => document.getElementById(`stage-${stage.key}`)?.scrollIntoView({ behavior: 'smooth', block: 'start' })}
                    >
                      <div className="flex items-center justify-between gap-3">
                        <Text strong>{stage.label}</Text>
                        <Tag color={style.color}>{style.label}</Tag>
                      </div>
                      <Progress percent={style.percent} showInfo={false} stroke="#3b82f6" className="mt-3" />
                      <Text className="mt-2 block console-caption">{stage.count} 条记录</Text>
                    </button>
                  )
                })}
              </div>
            </Card>

            <Card className="console-panel" bodyStyle={{ padding: 20 }}>
              <Title heading={5} className="!mb-0">恢复与帮助</Title>
              <div className="mt-4 console-next-step-list">
                <Text className="console-caption">• 先刷新当前任务，确认是否仍处于排队或处理中。</Text>
                <Text className="console-caption">• 优先检查当前阶段输入是否完整，再决定是否重跑。</Text>
                <Text className="console-caption">• 连续异常时进入帮助页按恢复指引处理。</Text>
              </div>
              <Space className="mt-4" wrap>
                <Button onClick={() => void loadDatasetWorkspace(activeDataset.id, '任务状态已刷新')}>立即刷新</Button>
                <Button onClick={() => navigate('/console/help')}>恢复指引</Button>
              </Space>
            </Card>
          </div>

          <div className="console-workbench-main">
            <Card className="console-panel" bodyStyle={{ padding: 20 }}>
              <Title heading={4} className="!mb-0">当前最该做什么</Title>
              <Text className="mt-2 block console-caption">参考主流数据运营台做法，把动作、等待原因和风险提示固定在主工作区顶部。</Text>
              <div className="mt-5 console-summary-grid">
                <div className="console-summary-row"><span>下一步动作</span><Text strong>{nextActionLabel(activeDataset.status)}</Text></div>
                <div className="console-summary-row"><span>等待说明</span><Text strong>{waitingReasonLabel(activeDataset.status, queueDepth)}</Text></div>
                <div className="console-summary-row"><span>刷新建议</span><Text strong>{refreshExpectationLabel(activeDataset.status, queueDepth)}</Text></div>
                <div className="console-summary-row"><span>进度保障</span><Text strong>{trustMessageLabel(activeDataset.status)}</Text></div>
              </div>
            </Card>

            <section id="stage-lifecycle" className="console-stack">
              <Card className="console-focus-card" bodyStyle={{ padding: 20 }}>
                <Title heading={4} className="!mb-0">生命周期主轴</Title>
                <Text className="mt-2 block console-caption">按“主题结构 → 问题 → 答案 → 评分 → 导出”串起整个任务，不删功能，只重排信息密度。</Text>
                <div className="mt-5 console-stack">
                  {stageCards.map((stage) => {
                    const style = stageStateStyle(stage.state)
                    return (
                      <div key={stage.key} id={`stage-${stage.key}`} className="console-workbench-stage-card">
                        <div className="flex flex-wrap items-center justify-between gap-3">
                          <div>
                            <Text strong>{stage.label}</Text>
                            <Text className="mt-2 block console-caption">{stage.summary}</Text>
                          </div>
                          <Space>
                            <Tag color={style.color}>{style.label}</Tag>
                            <Tag color="blue">{stage.count} 条记录</Tag>
                          </Space>
                        </div>
                        <Progress percent={style.percent} showInfo={false} stroke="#3b82f6" className="mt-4" />
                      </div>
                    )
                  })}
                </div>
              </Card>
            </section>

            <section id="stage-quality" className="console-card-grid-2">
              <Card className="console-panel" bodyStyle={{ padding: 20 }}>
                <Title heading={4} className="!mb-0">质量抽检</Title>
                <Text className="mt-2 block console-caption">把抽检入口和风险信号并排放在主工作区，减少阶段跳转成本。</Text>
                <div className="mt-5 console-summary-grid">
                  <div className="console-summary-row"><span>问题抽检</span><Space><Tag color={warningQuestionCount > 0 ? 'orange' : 'green'}>{warningQuestionCount > 0 ? `${warningQuestionCount} 条待关注` : '状态正常'}</Tag><Button size="small" onClick={() => navigate('/console/questions')}>去审核</Button></Space></div>
                  <div className="console-summary-row"><span>答案抽检</span><Space><Tag color={missingReasoningCount > 0 ? 'orange' : 'green'}>{missingReasoningCount > 0 ? `${missingReasoningCount} 条缺少思考` : '结构完整'}</Tag><Button size="small" onClick={() => navigate('/console/reasoning')}>去审核</Button></Space></div>
                  <div className="console-summary-row"><span>评分复核</span><Space><Tag color={lowRewardCount > 0 ? 'red' : 'green'}>{lowRewardCount > 0 ? `${lowRewardCount} 条低分` : '评分稳定'}</Tag><Button size="small" onClick={() => navigate('/console/rewards')}>去审核</Button></Space></div>
                  <div className="console-summary-row"><span>交付复核</span><Space><Tag color={deliveryArtifactCount > 0 ? 'green' : 'grey'}>{deliveryArtifactCount > 0 ? `${deliveryArtifactCount} 个可交付` : '暂无交付包'}</Tag><Button size="small" onClick={() => navigate('/console/exports')}>去审核</Button></Space></div>
                </div>
              </Card>

              <Card className="console-panel" bodyStyle={{ padding: 20 }}>
                <Title heading={4} className="!mb-0">风险与恢复</Title>
                <Text className="mt-2 block console-caption">把等待与恢复提示紧邻质量动作展示，符合运营台的处置路径。</Text>
                <div className="mt-5 console-summary-grid">
                  <div className="console-summary-row"><span>等待任务数</span><Text strong>{queueDepth}</Text></div>
                  <div className="console-summary-row"><span>导出状态</span><Text strong>{exportDeliveryPending ? '导出落盘中' : '按当前阶段推进'}</Text></div>
                  <div className="console-summary-row"><span>建议动作</span><Text strong>{nextActionLabel(activeDataset.status)}</Text></div>
                  <div className="console-summary-row"><span>恢复入口</span><Button size="small" onClick={() => navigate('/console/help')}>查看帮助</Button></div>
                </div>
              </Card>
            </section>

            <section id="stage-assets">
              <Card className="console-panel" bodyStyle={{ padding: 20 }}>
                <Title heading={4} className="!mb-0">导出与交付资产</Title>
                <Text className="mt-2 block console-caption">保留原有导出功能，但在任务页中直接暴露交付状态与资产规模，符合数据资产平台常见布局。</Text>
                <div className="mt-5 console-card-grid-3">
                  <div className="console-workbench-stat-card">
                    <Text className="console-caption">最新交付</Text>
                    <div className="console-stat-value mt-2">{deliveryArtifactCount}</div>
                    <Text className="mt-2 block console-caption">优先下载并交付下游</Text>
                  </div>
                  <div className="console-workbench-stat-card">
                    <Text className="console-caption">版本复核</Text>
                    <div className="console-stat-value mt-2">{reviewArtifactCount}</div>
                    <Text className="mt-2 block console-caption">用于内部复核与抽样</Text>
                  </div>
                  <div className="console-workbench-stat-card">
                    <Text className="console-caption">其他格式</Text>
                    <div className="console-stat-value mt-2">{otherArtifactCount}</div>
                    <Text className="mt-2 block console-caption">按需确认兼容性</Text>
                  </div>
                </div>
                <div className="mt-5 console-summary-grid">
                  <div className="console-summary-row"><span>默认交付视图</span><Text strong>{deliveryArtifactCount > 0 ? '优先显示最新交付包' : '等待生成交付包'}</Text></div>
                  <div className="console-summary-row"><span>版本策略</span><Text strong>先看最新版本，再进入导出中心查看历史版本与文件详情</Text></div>
                  <div className="console-summary-row"><span>交付说明</span><Text strong>{reviewArtifactCount > 0 ? '存在复核资料，建议先内部确认再对外交付' : '当前可直接以交付包为主继续推进'}</Text></div>
                </div>
                <Space className="mt-5" wrap>
                  <Button theme="solid" type="primary" onClick={() => navigate('/console/exports')}>进入导出中心</Button>
                  <Button onClick={() => document.getElementById('stage-lifecycle')?.scrollIntoView({ behavior: 'smooth', block: 'start' })}>返回生命周期</Button>
                </Space>
              </Card>
            </section>
          </div>

          <div className="console-workbench-delivery">
            <Card className="console-panel" bodyStyle={{ padding: 20 }}>
              <Title heading={5} className="!mb-0">最新交付</Title>
              <Text className="mt-2 block console-caption">交付资产持续可见，但不打断当前阶段处理。</Text>
              <div className="mt-4 console-summary-grid">
                <div className="console-summary-row"><span>最新交付包</span><Text strong>{deliveryArtifactCount > 0 ? `${deliveryArtifactCount} 个可交付` : '暂无交付包'}</Text></div>
                <div className="console-summary-row"><span>版本复核</span><Text strong>{reviewArtifactCount > 0 ? `${reviewArtifactCount} 个复核资产` : '暂无复核资产'}</Text></div>
                <div className="console-summary-row"><span>导出状态</span><Text strong>{exportDeliveryPending ? '导出落盘中' : '可按当前阶段推进'}</Text></div>
                <div className="console-summary-row"><span>版本策略</span><Text strong>优先最新版本</Text></div>
              </div>
              <Space className="mt-4" wrap>
                <Button theme="solid" type="primary" onClick={() => navigate('/console/exports')}>进入导出中心</Button>
                <Button onClick={() => document.getElementById('stage-assets')?.scrollIntoView({ behavior: 'smooth', block: 'start' })}>查看资产区</Button>
              </Space>
            </Card>

            <Card className="console-panel" bodyStyle={{ padding: 20 }}>
              <Title heading={5} className="!mb-0">交付判断</Title>
              <div className="mt-4 console-summary-grid">
                <div className="console-summary-row"><span>默认交付视图</span><Text strong>{deliveryArtifactCount > 0 ? '先看最新交付包' : '等待导出产物'}</Text></div>
                <div className="console-summary-row"><span>对外交付前</span><Text strong>{reviewArtifactCount > 0 ? '建议先完成内部复核' : '可直接以交付包为主继续推进'}</Text></div>
                <div className="console-summary-row"><span>其他格式</span><Text strong>{otherArtifactCount > 0 ? `${otherArtifactCount} 个待确认格式` : '无额外格式风险'}</Text></div>
              </div>
            </Card>
          </div>
        </div>
      </div>
    )
  }

  const renderPlanning = () => (
    <div className="console-page-shell">
      <PageHeader
        badge="新建任务"
        title="先填主题和目标规模，立即创建新任务"
        description="普通用户默认只需要任务主题和目标规模。高级配置继续保留，但不干扰首次创建路径。"
        actions={
          <>
            <Button onClick={() => navigate('/console/tasks')}>返回我的任务</Button>
            <Button icon={<RefreshCw size={16} />} loading={bootstrapLoading} onClick={() => void loadBootstrap('新建任务页已刷新')}>刷新配置</Button>
          </>
        }
      />

      <div className="console-card-grid-2">
        <Card className="console-panel" bodyStyle={{ padding: 20 }}>
          <Title heading={4} className="!mb-0">任务基础信息</Title>
          <Text className="mt-2 block console-caption">先完成这 3 个核心字段即可创建任务；高级配置继续保留在下方，不打断主流程。</Text>
          <div className="console-card-grid-2 mt-5">
            <div>
              <Text className="mb-2 block font-medium">任务名称（可选）</Text>
              <Input value={plannerForm.name} onChange={(value) => setPlannerForm((current) => ({ ...current, name: value }))} placeholder="例如：行业研究问答任务" />
            </div>
            <div>
              <Text className="mb-2 block font-medium">任务主题</Text>
              <Input value={plannerForm.rootKeyword} onChange={(value) => setPlannerForm((current) => ({ ...current, rootKeyword: value }))} />
            </div>
            <div>
              <Text className="mb-2 block font-medium">目标样本数（条）</Text>
              <InputNumber value={plannerForm.targetSize} onChange={(value) => setPlannerForm((current) => ({ ...current, targetSize: Number(value ?? 0) }))} min={1} style={{ width: '100%' }} />
              <Text className="mt-2 block console-caption">建议先从小规模开始，确认质量后再扩大任务规模。</Text>
            </div>
            {isAdmin || showAdvancedPlanning ? (
              <>
                <div>
                  <Text className="mb-2 block font-medium">生成策略</Text>
                  <Select value={plannerForm.strategyId} optionList={strategies.map((item) => ({ value: item.id, label: item.name }))} onChange={(value) => setPlannerForm((current) => ({ ...current, strategyId: Number(value) }))} style={{ width: '100%' }} />
                </div>
                <div>
                  <Text className="mb-2 block font-medium">AI 服务</Text>
                  <Select value={plannerForm.providerId} optionList={providers.map((item) => ({ value: item.id, label: item.name }))} onChange={(value) => setPlannerForm((current) => ({ ...current, providerId: Number(value) }))} style={{ width: '100%' }} />
                </div>
                <div>
                  <Text className="mb-2 block font-medium">存储配置</Text>
                  <Select value={plannerForm.storageProfileId} optionList={activeStorageProfiles.map((item) => ({ value: item.id, label: item.name }))} onChange={(value) => setPlannerForm((current) => ({ ...current, storageProfileId: Number(value) }))} style={{ width: '100%' }} />
                </div>
              </>
            ) : null}
          </div>
          <Space className="mt-6" spacing="medium">
            <Button icon={<Target size={16} />} loading={actionLoading} onClick={() => void estimatePlan()}>估算规模</Button>
            <Button theme="solid" type="primary" icon={<FileOutput size={16} />} loading={actionLoading} onClick={() => void createDataset()}>创建任务</Button>
          </Space>
        </Card>

        <Card className="console-panel" bodyStyle={{ padding: 20 }}>
          <Title heading={4} className="!mb-0">估算结果</Title>
          <Text className="mt-2 block console-caption">根据目标规模估算方向数量与题目数量，帮助你快速判断任务是否合理。</Text>
          {planningCards.length > 0 ? (
            <div className="console-card-grid-2 mt-5">
              {planningCards.map((item) => <StatCard key={item.label} {...item} />)}
            </div>
          ) : (
            <EmptyCard title="尚未生成估算" description="填写参数后点击“估算规模”，再决定是否创建任务。" />
          )}
        </Card>
      </div>
    </div>
  )

  const renderDomains = () => {
    const domainItems = graph?.domains ?? []
    const pendingDomains = domainItems.filter((domain) => domain.reviewStatus !== 'approved')
    const duplicateNames = domainItems.reduce<Record<string, number>>((accumulator, domain) => {
      const key = domain.name.trim().toLowerCase()
      if (!key) return accumulator
      accumulator[key] = (accumulator[key] ?? 0) + 1
      return accumulator
    }, {})
    const duplicateCount = Object.values(duplicateNames).filter((count) => count > 1).length
    const emptyNameCount = domainItems.filter((domain) => !domain.name.trim()).length
    const orphanCount = domainItems.filter((domain) => domain.level > 1 && !domain.parentId).length
    const structureWarnings = [
      emptyNameCount > 0 ? `有 ${emptyNameCount} 个方向名称为空，建议先补齐再确认。` : null,
      duplicateCount > 0 ? `有 ${duplicateCount} 组方向名称重复，建议先合并或重命名。` : null,
      orphanCount > 0 ? `有 ${orphanCount} 个非一级方向缺少父级关系，建议先复核结构。` : null,
      pendingDomains.length > 0 ? `还有 ${pendingDomains.length} 个方向未标记为“已确认”。` : null,
    ].filter(Boolean) as string[]

    const markAllDomains = (reviewStatus: string) => {
      setGraph((current) => current ? {
        ...current,
        domains: current.domains.map((domain) => ({ ...domain, reviewStatus })),
      } : current)
    }

    return (
      <div className="console-page-shell">
        <PageHeader
          badge="任务中心 / 主题结构"
          title="生成主题结构并完成确认"
          description="先按树状方式浏览方向分组，再逐项复核命名，最后确认进入题目生成。"
          actions={
            <>
              <Button onClick={() => navigate(activeTaskDetailRoute)}>返回当前任务</Button>
              <Button onClick={() => navigate('/console/tasks')}>返回任务列表</Button>
              <Button icon={<RefreshCw size={16} />} loading={workspaceLoading} onClick={() => activeDatasetId && void loadDatasetWorkspace(activeDatasetId, '方向结构已刷新')}>刷新方向结构</Button>
              <Button theme="solid" type="primary" icon={<GitBranch size={16} />} loading={workspaceLoading} onClick={() => void generateDomains()}>生成方向结构</Button>
            </>
          }
        />

        <div className="console-card-grid-2">
          <Card className="console-panel" bodyStyle={{ padding: 20 }}>
            <Title heading={4} className="!mb-0">方向结构预览</Title>
            <Text className="mt-2 block console-caption">先确认方向是否覆盖完整，再批量校对命名，避免后续题目分布偏移。</Text>
            {graph ? (
              <div className="mt-5 console-stack">
                {structureWarnings.length > 0 ? (
                  <Banner
                    type="warning"
                    icon={<Bell size={16} />}
                    description={
                      <div className="console-next-step-list">
                        {structureWarnings.map((warning) => <Text key={warning} className="console-caption">• {warning}</Text>)}
                      </div>
                    }
                  />
                ) : (
                  <Banner type="success" icon={<ShieldCheck size={16} />} description="当前方向结构没有明显异常，可以继续保存并确认。" />
                )}
                <DirectionStructurePreview rootKeyword={graph.dataset.rootKeyword} domains={graph.domains} />
                <Card className="console-toolbar-card" bodyStyle={{ padding: 16 }}>
                  <div className="flex flex-col gap-3 lg:flex-row lg:items-center lg:justify-between">
                    <div>
                      <Text strong>高级模式</Text>
                      <Text className="mt-1 block console-caption">图形关系视图默认隐藏，只有在排查复杂结构时才建议展开。</Text>
                    </div>
                    <Switch checked={showAdvancedGraphView} onChange={(checked) => setShowAdvancedGraphView(checked)} checkedText="已开启" uncheckedText="已关闭" />
                  </div>
                  {showAdvancedGraphView ? (
                    graph.edges.length > 0 ? (
                      <div className="console-next-step-list mt-4">
                        {graph.edges.map((edge) => {
                          const source = graph.domains.find((item) => item.id === edge.sourceId)?.name ?? `节点 ${edge.sourceId}`
                          const target = graph.domains.find((item) => item.id === edge.targetId)?.name ?? `节点 ${edge.targetId}`
                          return <Text key={edge.id} className="console-caption">• {source} → {target}（{edge.relation || '关联'}）</Text>
                        })}
                      </div>
                    ) : (
                      <Text className="mt-4 block console-caption">当前没有额外图形关系，仅保留树状结构即可完成审核。</Text>
                    )
                  ) : null}
                </Card>
                <Space wrap>
                  <Button onClick={() => markAllDomains('approved')}>批量标记为已确认</Button>
                  <Button onClick={() => markAllDomains('pending')}>批量标记为待复核</Button>
                  <Button icon={<RefreshCw size={16} />} loading={workspaceLoading} onClick={() => void saveGraph()}>保存编辑</Button>
                  <Button theme="solid" type="primary" loading={workspaceLoading} onClick={() => void confirmDomains()}>确认方向结构</Button>
                </Space>
              </div>
            ) : (
              <EmptyCard title="尚未生成方向结构" description="先创建任务，再点击“生成方向结构”。" />
            )}
          </Card>

          <Card className="console-focus-card" bodyStyle={{ padding: 20 }}>
            <Title heading={4} className="!mb-0">当前任务上下文</Title>
            <Text className="mt-2 block console-caption">这里持续展示当前任务上下文，避免在不同步骤间丢失状态。</Text>
            <div className="mt-5 console-summary-grid">
              <div className="console-summary-row"><span>数据集</span><Text strong>{activeDataset?.name ?? '未选择'}</Text></div>
              <div className="console-summary-row"><span>任务主题</span><Text strong>{activeDataset?.rootKeyword ?? '—'}</Text></div>
              <div className="console-summary-row"><span>当前状态</span><Text strong>{activeDataset ? (exportDeliveryPending ? '导出收尾中' : statusLabel(activeDataset.status)) : '—'}</Text></div>
              <div className="console-summary-row"><span>方向数量</span><Text strong>{graph?.domains.length ?? 0}</Text></div>
              <div className="console-summary-row"><span>待复核方向</span><Text strong>{pendingDomains.length}</Text></div>
              <div className="console-summary-row"><span>结构异常</span><Text strong>{structureWarnings.length}</Text></div>
            </div>
          </Card>
        </div>

        <Card className="console-panel" bodyStyle={{ padding: 20 }}>
          <Title heading={4} className="!mb-0">方向命名复核</Title>
          <Text className="mt-2 block console-caption">按列表逐项修订方向名称并保存，再执行“确认方向结构”进入下一步。</Text>
          {graph ? (
            <div className="console-card-grid-2 mt-5">
              {graph.domains.map((domain) => {
                const duplicate = Boolean(domain.name.trim()) && duplicateNames[domain.name.trim().toLowerCase()] > 1
                const emptyName = !domain.name.trim()
                return (
                  <div key={domain.id} className="console-domain-item">
                    <div className="flex items-center justify-between gap-3">
                      <Text className="console-caption">{sourceLabel(domain.source)}</Text>
                      <Space>
                        {duplicate ? <Tag color="orange">名称重复</Tag> : null}
                        {emptyName ? <Tag color="red">名称为空</Tag> : null}
                        <Tag color={domain.reviewStatus === 'approved' ? 'green' : 'blue'}>{reviewStatusLabel(domain.reviewStatus)}</Tag>
                      </Space>
                    </div>
                    <Input value={domain.name} onChange={(value) => setGraph((current) => current ? { ...current, domains: current.domains.map((item) => item.id === domain.id ? { ...item, name: value } : item) } : current)} />
                    <div className="mt-3 flex flex-wrap gap-2">
                      <Button size="small" theme={domain.reviewStatus === 'approved' ? 'solid' : 'light'} onClick={() => setGraph((current) => current ? { ...current, domains: current.domains.map((item) => item.id === domain.id ? { ...item, reviewStatus: 'approved' } : item) } : current)}>标记为已确认</Button>
                      <Button size="small" theme={domain.reviewStatus !== 'approved' ? 'solid' : 'light'} onClick={() => setGraph((current) => current ? { ...current, domains: current.domains.map((item) => item.id === domain.id ? { ...item, reviewStatus: 'pending' } : item) } : current)}>保留待复核</Button>
                    </div>
                  </div>
                )
              })}
            </div>
          ) : (
            <EmptyCard title="暂无方向列表" description="生成方向结构后，这里会显示可编辑的方向列表。" />
          )}
        </Card>
      </div>
    )
  }

  const renderRecordPage = ({
    badge,
    title,
    description,
    actionLabel,
    onGenerate,
    onRefresh,
    generateDisabled,
    records,
    emptyTitle,
    emptyDescription,
    summaryTitle,
    summaryCards,
    nextStepTips,
    exceptionHint,
    renderRecord,
  }: {
    badge: string
    title: string
    description: string
    actionLabel: string
    onGenerate: () => Promise<void>
    onRefresh: () => Promise<void>
    generateDisabled?: boolean
    records: Array<Question | ReasoningRecord | RewardRecord | Artifact>
    emptyTitle: string
    emptyDescription: string
    summaryTitle: string
    summaryCards: Array<{ icon: LucideIcon; label: string; value: string | number; helper: string }>
    nextStepTips: string[]
    exceptionHint: string
    renderRecord: (record: any) => React.ReactNode
  }) => (
    <div className="console-page-shell">
      <PageHeader
        badge={badge}
        title={title}
        description={description}
        actions={
          <>
            <Button onClick={() => navigate(activeTaskDetailRoute)}>{activeTaskNavLabel}</Button>
            {activeDataset ? <Button onClick={() => navigate('/console/tasks')}>返回任务列表</Button> : null}
            <Button icon={<RefreshCw size={16} />} loading={workspaceLoading} onClick={() => void onRefresh()}>刷新结果</Button>
            <Button theme="solid" type="primary" loading={actionLoading} disabled={generateDisabled || actionLoading || workspaceLoading} onClick={() => void onGenerate()}>{actionLabel}</Button>
          </>
        }
      />
      <div className="console-card-grid-3">
        {summaryCards.map((item) => <StatCard key={item.label} {...item} />)}
      </div>
      <div className="console-card-grid-2">
        <Card className="console-record-card" bodyStyle={{ padding: 20 }}>
          <Title heading={4} className="!mb-0">结果中心预览</Title>
          <Text className="mt-2 block console-caption">共 {records.length} 条可见结果，优先展示可直接决策的信息。</Text>
          {records.length > 0 ? <div className="console-record-list mt-5">{records.map(renderRecord)}</div> : <EmptyCard title={emptyTitle} description={emptyDescription} />}
        </Card>
        <Card className="console-focus-card" bodyStyle={{ padding: 20 }}>
          <Title heading={4} className="!mb-0">{summaryTitle}</Title>
          <div className="mt-5 console-summary-grid">
            <div className="console-summary-row"><span>活动数据集</span><Text strong>{activeDataset?.name ?? '未选择'}</Text></div>
            <div className="console-summary-row"><span>当前阶段</span><Text strong>{activeDataset ? (exportDeliveryPending ? '导出收尾中' : statusLabel(activeDataset.status)) : '未开始'}</Text></div>
            <div className="console-summary-row"><span>阶段结果数</span><Text strong>{records.length}</Text></div>
            <div className="console-summary-row"><span>等待任务数</span><Text strong>{runtime?.queueDepth ?? 0}</Text></div>
            <div className="console-summary-row"><span>等待状态</span><Text strong>{activeDataset ? (exportDeliveryPending ? '导出状态已完成，正在确认交付文件，请稍后刷新' : waitingStateLabel(activeDataset.status, runtime?.queueDepth ?? 0)) : '请先创建任务'}</Text></div>
            <div className="console-summary-row"><span>等待原因</span><Text strong>{activeDataset ? (exportDeliveryPending ? '导出计算已完成，系统正在将结果写入交付存储。' : waitingReasonLabel(activeDataset.status, runtime?.queueDepth ?? 0)) : '创建任务后显示等待原因'}</Text></div>
            <div className="console-summary-row"><span>建议操作</span><Text strong>{activeDataset ? (exportDeliveryPending ? '暂时无需重复触发导出，等待交付文件落盘后再下载。' : waitingActionLabel(activeDataset.status)) : '创建任务后显示可执行操作'}</Text></div>
            <div className="console-summary-row"><span>当前阶段 ETA</span><Text strong>{activeEta}</Text></div>
            <div className="console-summary-row"><span>刷新建议</span><Text strong>{activeDataset ? (exportDeliveryPending ? '系统会每 20 秒自动刷新；检测到交付文件后将自动变为可下载。' : refreshExpectationLabel(activeDataset.status, runtime?.queueDepth ?? 0)) : '创建任务后显示刷新建议'}</Text></div>
          </div>
          <Card className="console-toolbar-card mt-4" bodyStyle={{ padding: 16 }}>
            <Text strong>异常说明</Text>
            <Text className="mt-2 block console-caption">{exceptionHint}</Text>
          </Card>
          <Card className="console-toolbar-card mt-4" bodyStyle={{ padding: 16 }}>
            <Text strong>下一步建议</Text>
            <div className="console-next-step-list mt-3">
              {nextStepTips.map((tip) => <Text key={tip} className="console-caption">• {tip}</Text>)}
            </div>
          </Card>
        </Card>
      </div>
    </div>
  )

  const renderResultsHub = () => (
    <div className="console-page-shell">
      <PageHeader
        badge="数据资产"
        title="统一查看结果、质量状态与交付文件"
        description="这里先回答“产出了什么、哪些内容可交付、哪些结果还需要复核”，再进入具体结果页面处理。"
        actions={
          <>
            <Button onClick={() => navigate(activeTaskDetailRoute)}>返回当前任务</Button>
            <Button onClick={() => navigate('/console/tasks')}>返回我的任务</Button>
            <Button icon={<RefreshCw size={16} />} loading={workspaceLoading} onClick={() => activeDatasetId && void loadDatasetWorkspace(activeDatasetId, '数据资产页已刷新')}>刷新结果</Button>
          </>
        }
      />

      <div className="console-card-grid-4">
        <StatCard icon={Layers3} label="题目结果" value={questions.length} helper="查看题目覆盖情况与待关注项" />
        <StatCard icon={BrainCircuit} label="答案结果" value={reasoning.length} helper="确认答案摘要是否完整可用" />
        <StatCard icon={ShieldCheck} label="质量评分" value={rewards.length} helper="判断哪些内容已经可以交付" />
        <StatCard icon={HardDriveDownload} label="导出文件" value={artifacts.length} helper="确认已生成可下载交付包" />
      </div>

      <div className="console-card-grid-2">
        <Card className="console-panel" bodyStyle={{ padding: 20 }}>
          <Title heading={4} className="!mb-0">结果总览</Title>
          <Text className="mt-2 block console-caption">这里主要回答“已经产出了什么、哪些结果可以继续处理或交付”。</Text>
          <div className="mt-5 console-summary-grid">
            <div className="console-summary-row"><span>当前任务</span><Text strong>{activeDataset?.name ?? '未选择'}</Text></div>
            <div className="console-summary-row"><span>题目结果</span><Text strong>{questions.length}</Text></div>
            <div className="console-summary-row"><span>答案结果</span><Text strong>{reasoning.length}</Text></div>
            <div className="console-summary-row"><span>质量评估</span><Text strong>{rewards.length}</Text></div>
            <div className="console-summary-row"><span>导出文件</span><Text strong>{artifacts.length}</Text></div>
            <div className="console-summary-row"><span>建议动作</span><Text strong>{activeDataset ? nextActionLabel(activeDataset.status) : '先前往任务中心创建任务'}</Text></div>
          </div>
        </Card>

        <Card className="console-focus-card" bodyStyle={{ padding: 20 }}>
          <Title heading={4} className="!mb-0">结果工作台</Title>
          <Text className="mt-2 block console-caption">进入具体结果页处理异常、抽检内容、继续评分或导出交付。</Text>
          <div className="console-card-grid-2 mt-5">
            {resultWorkbenchPages.map((page) => (
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
    </div>
  )

  const renderOperations = () => {
    const recentAuditLogs = auditLogs.slice(0, 8)
    const recentDatasets = [...datasets]
      .sort((left, right) => Date.parse(right.updatedAt) - Date.parse(left.updatedAt))
      .slice(0, 6)
    const activeProviders = providers.filter((item) => item.isActive)
    const activeStorageCount = storageProfiles.filter((item) => item.isActive).length
    const queueDepth = runtime?.queueDepth ?? 0

    return (
      <div className="console-page-shell">
        <PageHeader
          badge="系统设置 / 运营监控"
          title="集中查看队列、配置健康度与最近操作"
          description="把运行态信息放到管理员工作台里统一查看，普通用户不需要在一级导航里理解流水线细节。"
          actions={
            <>
              <Button icon={<RefreshCw size={16} />} loading={workspaceLoading} onClick={() => void loadBootstrap('运营监控数据已刷新')}>刷新监控</Button>
              <Button theme="solid" type="primary" onClick={() => navigate('/console/admin/audit')}>查看完整审计</Button>
            </>
          }
        />

        <div className="console-card-grid-4">
          <StatCard icon={ServerCog} label="等待任务" value={queueDepth} helper="队列积压越高，阶段返回越慢" />
          <StatCard icon={Database} label="活跃 AI 服务" value={activeProviders.length} helper="仅统计当前可用于生产的服务" />
          <StatCard icon={FolderCog} label="可用存储" value={activeStorageCount} helper="可正常用于交付结果落盘的存储配置" />
          <StatCard icon={Settings} label="最近审计数" value={recentAuditLogs.length} helper="便于巡检近期配置与操作变更" />
        </div>

        <div className="console-card-grid-2">
          <Card className="console-panel" bodyStyle={{ padding: 20 }}>
            <Title heading={4} className="!mb-0">运行态摘要</Title>
            <Text className="mt-2 block console-caption">用于判断系统是否适合继续发起生成、评估与交付。</Text>
            <div className="mt-5 console-summary-grid">
              <div className="console-summary-row"><span>等待任务</span><Text strong>{queueDepth}</Text></div>
              <div className="console-summary-row"><span>任务总数</span><Text strong>{runtime?.datasetCount ?? 0}</Text></div>
              <div className="console-summary-row"><span>题目结果</span><Text strong>{runtime?.questionCount ?? 0}</Text></div>
              <div className="console-summary-row"><span>答案结果</span><Text strong>{runtime?.reasoningCount ?? 0}</Text></div>
              <div className="console-summary-row"><span>评分结果</span><Text strong>{runtime?.rewardCount ?? 0}</Text></div>
              <div className="console-summary-row"><span>交付文件</span><Text strong>{runtime?.artifactCount ?? 0}</Text></div>
              <div className="console-summary-row"><span>活跃 AI 服务</span><Text strong>{dashboard?.activeProviderCount ?? activeProviders.length}</Text></div>
              <div className="console-summary-row"><span>存储配置</span><Text strong>{dashboard?.storageProfileCount ?? storageProfiles.length}</Text></div>
              <div className="console-summary-row"><span>规则数量</span><Text strong>{dashboard?.strategyCount ?? strategies.length}</Text></div>
              <div className="console-summary-row"><span>指令模板</span><Text strong>{dashboard?.promptCount ?? prompts.length}</Text></div>
            </div>
          </Card>

          <Card className="console-focus-card" bodyStyle={{ padding: 20 }}>
            <Title heading={4} className="!mb-0">运营建议</Title>
            <Text className="mt-2 block console-caption">先看队列与配置健康，再决定是否继续大批量执行。</Text>
            <div className="mt-5 console-next-step-list">
              <Text className="console-caption">• 队列过高时，优先处理已产出资产，避免重复触发生成。</Text>
              <Text className="console-caption">• 活跃 AI 服务不足时，先检查服务连通性与模型配置。</Text>
              <Text className="console-caption">• 交付文件缺失时，优先检查存储配置与导出阶段状态。</Text>
              <Text className="console-caption">• 发生异常后，先看最近审计与帮助页，再决定是否重试。</Text>
            </div>
            <Card className="console-toolbar-card mt-4" bodyStyle={{ padding: 16 }}>
              <Text strong>快捷入口</Text>
              <Space className="mt-3" wrap>
                <Button onClick={() => navigate('/console/tasks')}>回到我的任务</Button>
                <Button onClick={() => navigate('/console/results')}>查看数据资产</Button>
                <Button onClick={() => navigate('/console/help')}>查看恢复指引</Button>
              </Space>
            </Card>
          </Card>
        </div>

        <div className="console-card-grid-2">
          <Card className="console-panel" bodyStyle={{ padding: 20 }}>
            <Title heading={4} className="!mb-0">最近任务活动</Title>
            <Text className="mt-2 block console-caption">快速了解最近有哪些任务仍在推进，避免遗漏待处理项。</Text>
            <div className="mt-5 console-stack">
              {recentDatasets.length > 0 ? recentDatasets.map((dataset) => (
                <div key={dataset.id} className="console-domain-item">
                  <div className="flex items-center justify-between gap-3">
                    <Space>
                      <Tag color="blue">任务 #{dataset.id}</Tag>
                      <Tag color="cyan">{statusLabel(dataset.status)}</Tag>
                    </Space>
                    <Space>
                      <Text className="console-caption">{formatTime(dataset.updatedAt)}</Text>
                      <Button size="small" onClick={() => navigate(`/console/tasks/${dataset.id}`)}>查看任务</Button>
                    </Space>
                  </div>
                  <Text className="mt-2 block" strong>{dataset.name}</Text>
                  <Text className="mt-1 block console-caption">{dataset.rootKeyword}</Text>
                </div>
              )) : <EmptyCard title="暂无任务活动" description="创建任务后，这里会显示最新推进记录。" />}
            </div>
          </Card>

          <Card className="console-panel" bodyStyle={{ padding: 20 }}>
            <Title heading={4} className="!mb-0">最近操作</Title>
            <Text className="mt-2 block console-caption">便于回看谁在何时修改了关键配置或执行了重要动作。</Text>
            {recentAuditLogs.length > 0 ? (
              <Table columns={auditColumns} dataSource={recentAuditLogs} pagination={false} />
            ) : (
              <EmptyCard title="暂无操作记录" description="产生配置变更或关键动作后，这里会显示最近记录。" />
            )}
          </Card>
        </div>
      </div>
    )
  }

  const renderHelp = () => {
    const glossary = [
      { term: '任务状态', description: '表示任务所处阶段，例如“待确认方向”“问题生成排队中”。' },
      { term: '等待任务数', description: '当前系统排队中的任务数量。数字越大，结果返回通常越慢。' },
      { term: '结果中心', description: '集中查看题目、答案内容、质量评估和导出交付的页面。' },
      { term: '恢复建议', description: '当任务失败或卡住时，页面给出的可执行下一步动作。' },
    ]

    const recoveryChecklist = [
      '先看页面顶部提示卡中的“恢复建议”并按按钮跳转。',
      '确认当前阶段前置结果已生成，再发起下一步。',
      '若长时间无变化，点击“刷新数据”同步最新状态。',
      '若提示权限不足，请使用管理员账号或联系管理员开通权限。',
      '若提示登录失效，请重新登录后继续原任务。',
    ]

    const highRiskActions = [
      '重新生成答案或评分：可能覆盖你刚刚抽检过的结果，建议先记录当前结论。',
      '再次触发导出：若文件仍在落盘，重复触发通常没有收益，先刷新确认。',
      '切换 AI 服务或策略：会改变后续结果口径，建议在新任务中使用。',
      '复制交付标识后立即通知下游：建议先确认导出时间和文件类型，避免发错版本。',
    ]

    return (
      <div className="console-page-shell">
        <PageHeader
          badge="账户与帮助"
          title="自助排错、恢复流程与理解关键术语"
          description="遇到登录过期、权限不足、排队等待或结果异常时，先按这里的恢复路径处理，再决定是否重试。"
          actions={
            <Space>
              <Button icon={<RefreshCw size={16} />} loading={workspaceLoading} onClick={() => void loadBootstrap('帮助信息与任务状态已刷新')}>刷新数据</Button>
              <Button theme="solid" type="primary" onClick={() => navigate('/console/tasks')}>返回我的任务</Button>
            </Space>
          }
        />

        <div className="console-card-grid-2">
          <Card className="console-panel" bodyStyle={{ padding: 20 }}>
            <Title heading={4} className="!mb-0">失败恢复清单</Title>
            <Text className="mt-2 block console-caption">建议按顺序执行，通常可在 1-2 次操作内恢复流程。</Text>
            <div className="mt-4 console-next-step-list">
              {recoveryChecklist.map((item) => <Text key={item} className="console-caption">• {item}</Text>)}
            </div>
            <Card className="console-toolbar-card mt-4" bodyStyle={{ padding: 16 }}>
              <Text strong>快速操作</Text>
              <Space className="mt-3" wrap>
                {activeDataset ? <Button onClick={() => navigate(activeTaskDetailRoute)}>返回当前任务</Button> : null}
                <Button onClick={() => navigate('/console/planning')}>去新建任务</Button>
                <Button onClick={() => navigate('/console/results')}>查看数据资产</Button>
                <Button onClick={() => activeDatasetId ? void loadDatasetWorkspace(activeDatasetId, '当前任务状态已刷新') : void loadBootstrap('控制台数据已刷新')}>刷新当前任务状态</Button>
              </Space>
            </Card>
          </Card>

          <Card className="console-panel" bodyStyle={{ padding: 20 }}>
            <Title heading={4} className="!mb-0">术语解释</Title>
            <Text className="mt-2 block console-caption">减少系统术语理解成本，帮助你更快判断下一步。</Text>
            <List
              className="mt-4"
              dataSource={glossary}
              renderItem={(item) => (
                <List.Item>
                  <Space vertical align="start" spacing="tight">
                    <Text strong>{item.term}</Text>
                    <Text className="console-caption">{item.description}</Text>
                  </Space>
                </List.Item>
              )}
            />
            <Card className="console-toolbar-card mt-4" bodyStyle={{ padding: 16 }}>
              <Text strong>高风险动作说明</Text>
              <div className="console-next-step-list mt-3">
                {highRiskActions.map((item) => <Text key={item} className="console-caption">• {item}</Text>)}
              </div>
            </Card>
          </Card>
        </div>
      </div>
    )
  }

  const renderProviders = () => (
    <div className="console-page-shell">
      <PageHeader
        badge="系统设置 / AI 服务"
        title="管理 AI 服务配置"
        description="先查看服务列表，再通过弹窗修改配置、获取模型列表并测试连接。"
        actions={
          <Space wrap>
            <Button icon={<RefreshCw size={16} />} loading={workspaceLoading} onClick={() => void loadBootstrap('系统配置已刷新')}>刷新配置</Button>
            <Button theme="solid" type="primary" onClick={() => openCreateProviderModal()}>新增 AI 服务</Button>
          </Space>
        }
      />
      <Card className="console-panel" bodyStyle={{ padding: 20 }}>
        <div className="flex flex-col gap-4 lg:flex-row lg:items-center lg:justify-between">
          <div>
            <Title heading={4} className="!mb-0">AI 服务列表</Title>
            <Text className="mt-2 block console-caption">你可以先搜索服务，再在弹窗内修改参数、获取可用模型并测试连接。</Text>
          </div>
          <Input
            value={providerSearchKeyword}
            onChange={setProviderSearchKeyword}
            placeholder="搜索服务名称、地址、模型或思考强度"
            style={{ width: 360, maxWidth: '100%' }}
          />
        </div>
        <div className="mt-5">
          <Table columns={providerColumns} dataSource={filteredProviders} pagination={false} />
        </div>
      </Card>

      <Modal
        title={providerDraft.id ? '编辑 AI 服务' : '新增 AI 服务'}
        visible={providerModalVisible}
        onCancel={closeProviderModal}
        footer={
          <Space>
            <Button onClick={closeProviderModal}>取消</Button>
            <Button disabled={providerRemoteActionDisabled} loading={providerModelsLoading} onClick={() => void fetchProviderModels()}>获取模型列表</Button>
            <Button disabled={providerRemoteActionDisabled} loading={providerTestLoading} onClick={() => void testProviderConnectivity()}>模型连通性测试</Button>
            <Button theme="solid" type="primary" loading={workspaceLoading} onClick={() => void saveProvider()}>保存</Button>
          </Space>
        }
        width={920}
      >
        <div className="console-stack">
          <div className="console-card-grid-2">
            <div>
              <Text className="mb-2 block font-medium">服务名称</Text>
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
            <Text className="mb-2 block font-medium">访问密钥</Text>
            <Input value={providerDraft.apiKey ?? ''} onChange={(value) => setProviderDraft((current) => ({ ...current, apiKey: value }))} placeholder="系统会加密保存；留空则继续使用当前密钥" mode="password" />
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
        badge="系统设置 / 结果存储"
        title="管理结果存储位置"
        description="统一列表工作流：先查看列表，再新增或编辑。"
        actions={
          <Space wrap>
            <Button icon={<RefreshCw size={16} />} loading={workspaceLoading} onClick={() => void loadBootstrap('存储配置已刷新')}>刷新配置</Button>
            <Button theme="solid" type="primary" onClick={() => openCreateStorageModal()}>新增存储</Button>
          </Space>
        }
      />
      <Card className="console-panel" bodyStyle={{ padding: 20 }}>
        <div className="flex flex-col gap-4 lg:flex-row lg:items-center lg:justify-between">
          <div>
            <Title heading={4} className="!mb-0">结果存储列表</Title>
            <Text className="mt-2 block console-caption">已预置本地 MinIO 连接参数，也支持继续添加多个 S3 / MinIO / OSS 来源，并通过启用开关控制是否可用于计划编排。</Text>
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
            <Button theme="solid" type="primary" loading={workspaceLoading} onClick={() => void saveStorage()}>保存</Button>
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
        badge="系统设置 / 生成规则"
        title="管理生成规则"
        description="统一列表工作流：支持搜索、新增与编辑。"
        actions={
          <Space wrap>
            <Button icon={<RefreshCw size={16} />} loading={workspaceLoading} onClick={() => void loadBootstrap('生成策略已刷新')}>刷新策略</Button>
            <Button theme="solid" type="primary" onClick={() => openCreateStrategyModal()}>新增策略</Button>
          </Space>
        }
      />
      <Card className="console-panel" bodyStyle={{ padding: 20 }}>
        <div className="flex flex-col gap-4 lg:flex-row lg:items-center lg:justify-between">
          <div>
            <Title heading={4} className="!mb-0">生成规则列表</Title>
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
            <Button theme="solid" type="primary" loading={workspaceLoading} onClick={() => void saveStrategy()}>保存</Button>
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
        badge="系统设置 / 生成指令"
        title="管理生成指令模板"
        description="统一列表工作流：支持搜索、新增与编辑。"
        actions={
          <Space wrap>
            <Button icon={<RefreshCw size={16} />} loading={workspaceLoading} onClick={() => void loadBootstrap('生成指令模板已刷新')}>刷新模板</Button>
            <Button theme="solid" type="primary" onClick={() => openCreatePromptModal()}>新增模板</Button>
          </Space>
        }
      />
      <Card className="console-panel" bodyStyle={{ padding: 20 }}>
        <div className="flex flex-col gap-4 lg:flex-row lg:items-center lg:justify-between">
          <div>
            <Title heading={4} className="!mb-0">生成指令列表</Title>
            <Text className="mt-2 block console-caption">主题生成、题目生成、答案生成和质量评估用到的生成指令都在这里统一维护。</Text>
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
        title={promptDraft.id ? '编辑生成指令模板' : '新增生成指令模板'}
        visible={promptModalVisible}
        onCancel={closePromptModal}
        footer={
          <Space>
            <Button onClick={closePromptModal}>取消</Button>
            <Button theme="solid" type="primary" loading={workspaceLoading} onClick={() => void savePrompt()}>保存</Button>
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
            <Text className="mb-2 block font-medium">系统指令</Text>
            <TextArea rows={6} value={promptDraft.systemPrompt} onChange={(value) => setPromptDraft((current) => ({ ...current, systemPrompt: value }))} placeholder="系统指令" />
          </div>
          <div>
            <Text className="mb-2 block font-medium">用户指令</Text>
            <TextArea rows={6} value={promptDraft.userPrompt} onChange={(value) => setPromptDraft((current) => ({ ...current, userPrompt: value }))} placeholder="用户指令" />
          </div>
        </div>
      </Modal>
    </div>
  )

  const renderAudit = () => (
    <div className="console-page-shell">
      <PageHeader badge="系统设置 / 操作记录" title="查看配置变更记录" description="在同一界面查看配置变更记录，无需切换后台。" />
      <div className="console-card-grid-2">
        <Card className="console-panel" bodyStyle={{ padding: 20 }}>
          <Title heading={4} className="!mb-0">审计事件列表</Title>
          <Table columns={auditColumns} dataSource={auditLogs} pagination={{ pageSize: 8 }} />
        </Card>
        <Card className="console-focus-card" bodyStyle={{ padding: 20 }}>
          <Title heading={4} className="!mb-0">配置摘要</Title>
          <div className="mt-5 console-summary-grid">
            <div className="console-summary-row"><span>已启用 AI 服务</span><Text strong>{dashboard?.activeProviderCount ?? 0}</Text></div>
            <div className="console-summary-row"><span>存储配置</span><Text strong>{dashboard?.storageProfileCount ?? 0}</Text></div>
            <div className="console-summary-row"><span>策略数量</span><Text strong>{dashboard?.strategyCount ?? 0}</Text></div>
            <div className="console-summary-row"><span>指令模板数</span><Text strong>{dashboard?.promptCount ?? 0}</Text></div>
            <div className="console-summary-row"><span>审计记录</span><Text strong>{dashboard?.auditLogCount ?? 0}</Text></div>
          </div>
          <div className="mt-6">
            <Text className="console-caption">建议在修改 AI 服务、存储、规则或指令模板后，立即回到本页复核记录。</Text>
          </div>
        </Card>
      </div>
    </div>
  )

  if (sessionLoading) {
    return (
      <div className="flex min-h-screen items-center justify-center">
        <Spin size="large" tip="正在初始化企业工作台" />
      </div>
    )
  }

  return (
    <Routes>
      <Route
        path="/login"
        element={
          user ? (
            <Navigate to="/console/tasks" replace />
          ) : (
            <LoginPage
              onSubmit={handleLogin}
              loading={authSubmitting}
              signal={trustSignal}
              onDismissSignal={() => setTrustSignal(null)}
              onNavigate={(route) => navigate(route)}
            />
          )
        }
      />
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
                      <div className="font-semibold">企业数据工厂</div>
                      <div className="text-xs console-nav-caption">{visiblePages.find((page) => page.route === activeNav)?.caption ?? '围绕任务推进生产与交付'}</div>
                    </div>
                  </div>
                  <div className="console-header-actions flex items-center gap-3">
                    <Tag color={isAdmin ? 'green' : 'blue'}>{isAdmin ? '管理员' : '普通用户'}</Tag>
                    <Text>{user.email}</Text>
                    <Button icon={<RefreshCw size={16} />} loading={workspaceLoading} onClick={() => void loadBootstrap('控制台数据已刷新')} />
                    <Button icon={<LogOut size={16} />} onClick={() => void handleLogout()}>退出</Button>
                  </div>
                </div>
              </Header>
              <Layout style={{ paddingTop: 64 }}>
                <Sider className="app-sider" style={{ position: 'fixed', top: 64, left: 0, border: 'none', width: 'var(--sidebar-current-width)', zIndex: 30 }}>
                  <div className="console-nav-shell h-full px-3 py-4">
                    <Card className="console-sidebar-card mb-4" bodyStyle={{ padding: 16 }}>
                      <Text strong>工作入口</Text>
                      <Text className="mt-2 block console-caption">优先从“新建任务”开始；已有任务统一在“我的任务”继续推进，交付结果进入“数据资产”查看。</Text>
                      <Space className="mt-4" wrap>
                        <Button theme="solid" type="primary" icon={<CirclePlus size={16} />} onClick={() => navigate('/console/planning')}>新建任务</Button>
                        <Button onClick={() => navigate('/console/tasks')}>我的任务</Button>
                      </Space>
                    </Card>
                    <Nav
                      bodyStyle={{ paddingBottom: 12 }}
                      selectedKeys={[activeNav]}
                      items={[
                        {
                          itemKey: 'primary-group',
                          text: '业务导航',
                          items: visibleUserPages.map((page) => ({
                            itemKey: page.route,
                            text: page.label,
                            icon: <page.icon size={16} />,
                          })),
                        },
                        ...(isAdmin
                          ? [
                              {
                                itemKey: 'admin-group',
                                text: '系统设置',
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
                              <div className="console-summary-row"><span>任务总数</span><Text strong>{runtime?.datasetCount ?? 0}</Text></div>
                              <div className="console-summary-row"><span>等待任务数</span><Text strong>{runtime?.queueDepth ?? 0}</Text></div>
                              <div className="console-summary-row"><span>当前建议</span><Text strong>{runtime?.datasetCount ? '继续我的任务' : '先新建任务'}</Text></div>
                            </div>
                          </Card>
                        </div>
                      }
                    />
                  </div>
                </Sider>
                <Layout style={{ marginLeft: 'var(--sidebar-current-width)' }}>
                  <Content style={{ padding: 24 }}>
                    {trustSignal ? (
                      <div className="mb-4">
                        <TrustSignalCard signal={trustSignal} onDismiss={() => setTrustSignal(null)} onNavigate={(route) => navigate(route)} />
                      </div>
                    ) : null}
                    <Routes>
                      <Route path="/console/home" element={<Navigate to="/console/tasks" replace />} />
                      <Route path="/console/overview" element={<Navigate to="/console/tasks" replace />} />
                      <Route path="/console/tasks" element={renderTaskIndex()} />
                      <Route path="/console/tasks/:taskId" element={renderTaskDetail()} />
                      <Route path="/console/planning" element={renderPlanning()} />
                      <Route path="/console/results" element={renderResultsHub()} />
                      {isAdmin ? <Route path="/console/operations" element={renderOperations()} /> : null}
                      <Route path="/console/domains" element={<Navigate to={activeTaskDetailRoute} replace />} />
                      <Route path="/console/questions" element={<Navigate to={activeTaskDetailRoute} replace />} />
                      <Route path="/console/reasoning" element={<Navigate to={activeTaskDetailRoute} replace />} />
                      <Route path="/console/rewards" element={<Navigate to={activeTaskDetailRoute} replace />} />
                      <Route path="/console/exports" element={<Navigate to={activeTaskDetailRoute} replace />} />
                      <Route path="/console/help" element={renderHelp()} />
                      {isAdmin ? <Route path="/console/admin/providers" element={renderProviders()} /> : null}
                      {isAdmin ? <Route path="/console/admin/storage" element={renderStorage()} /> : null}
                      {isAdmin ? <Route path="/console/admin/strategies" element={renderStrategies()} /> : null}
                      {isAdmin ? <Route path="/console/admin/prompts" element={renderPrompts()} /> : null}
                      {isAdmin ? <Route path="/console/admin/audit" element={renderAudit()} /> : null}
                      <Route path="*" element={<Navigate to="/console/tasks" replace />} />
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
