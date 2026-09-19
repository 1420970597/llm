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
  ChevronRight,
  CirclePlus,
  Database,
  FileOutput,
  Filter,
  FolderCog,
  GitBranch,
  HardDriveDownload,
  LayoutDashboard,
  Layers3,
  LogOut,
  Network,
  PanelLeftClose,
  PanelLeftOpen,
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
  type ChainStandard,
  type DashboardRecord,
  type Dataset,
  type DatasetGraph,
  type DifficultyStats,
  type Domain,
  type ExportMapping,
  type GrpoPrompt,
  type ExportFormatList,
  type PipelineProgress,
  type GenerationRun,
  type PromptRecord,
  type Provider,
  type ProviderConnectivityResult,
  type ProviderModelInfo,
  type Question,
  type ReasoningRecord,
  type RewardRecord,
  type RuntimeStatus,
  type SftRecord,
  type StageEnqueueResult,
  type StorageProfile,
  type Strategy,
  type User,
} from './lib/api'
import { withActiveDataset, resolveDatasetId } from './lib/taskGuard'
import {
  DATASET_STATUS_PROGRESS,
  DATASET_STATUS_ROUTE,
  describeDatasetStatus,
  type DatasetStatus,
} from './lib/datasetStatus'
import {
  describeArtifactContentType,
  describeArtifactType,
  describeDomainReviewStatus,
} from './lib/enumLabels'
import { APP_BUILD_TIME, APP_VERSION_SHORT, APP_VERSION_UNKNOWN } from './buildInfo'
import { CleaningView } from './views/CleaningView'
import { EvaluationView } from './views/EvaluationView'

const { Title, Text } = Typography

// 新增生成策略时的默认规模（issue #108）。
//
// 为什么不是 1000：原先弹窗把「领域数」预填为 1000，用户只填个名称就保存会得到一条
// 按 1000 个领域规划的策略（这正是 issue #63 里 domainCount=1000 脏数据的来源），
// 而用户从未做过这个决策 —— 下游估算与生成的成本会直接失控。
//
// 为什么不直接用 0：后端 `ValidateStrategyInput` 拒绝 domainCount < 1，
// 默认 0 会让用户「点开、填个名称、保存」就直接拿到「领域数必须大于 0」的报错，
// 只是把一个坏体验换成另一个。
//
// 取 5 的依据：
//   1. 与仓库里既有的小规模验证实践一致（
//      测试数据与文档示例普遍用个位数领域）；
//   2. 5 × 10（每领域问题数）= 50 条问题，是一次真实 LLM 生成可承受的量级；
//   3. 它是「可解释的小值」而不是「看起来像推荐的魔法值」，
//      配合界面提示「先小规模验证，再逐步放大」引导用户主动改大。
const DEFAULT_STRATEGY_DOMAIN_COUNT = 5
const DEFAULT_STRATEGY_QUESTIONS_PER_DOMAIN = 10

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
  { label: '工作台', route: '/console/home', icon: LayoutDashboard, caption: '待办与进展' },
  { label: '新建任务', route: '/console/planning', icon: CirclePlus, caption: '创建新任务' },
  { label: '我的任务', route: '/console/tasks', icon: Target, caption: '查看任务' },
  { label: '数据资产', route: '/console/results', icon: HardDriveDownload, caption: '结果与交付文件' },
  { label: '质量评估', route: '/console/evaluation', icon: ShieldCheck, caption: '多模型互评与打分' },
  { label: '数据清洗', route: '/console/cleaning', icon: Filter, caption: '拒答与异常拦截' },
  { label: '账户与帮助', route: '/console/help', icon: Users, caption: '帮助与恢复' },
]

// 阶段工作台页面。阶段路由不是侧边栏项，用户是从任务详情页的阶段卡片进入的，
// 因此每个阶段必须额外声明它在侧边栏里的归属父项（navParent），否则处于该阶段时
// 侧边栏会高亮到不相关的默认项。
type StageWorkbenchPage = NavPage & { navParent: string }

const taskWorkbenchPages: StageWorkbenchPage[] = [
  { label: '主题结构', route: '/console/domains', icon: GitBranch, caption: '生成并确认主题结构', navParent: '/console/tasks' },
]

const resultWorkbenchPages: StageWorkbenchPage[] = [
  { label: '问题生成', route: '/console/questions', icon: Layers3, caption: '查看问题覆盖', navParent: '/console/results' },
  { label: '答案内容', route: '/console/reasoning', icon: BrainCircuit, caption: '查看答案完整性', navParent: '/console/results' },
  { label: '质量评估', route: '/console/rewards', icon: ShieldCheck, caption: '查看评分状态', navParent: '/console/results' },
  { label: '导出交付', route: '/console/exports', icon: HardDriveDownload, caption: '查看导出与交付', navParent: '/console/results' },
]

// 阶段路由 → 侧边栏高亮项。
//
// 这张表必须与阶段路由表保持一致：5b90c2e 把阶段路由改成自指重定向、又只改了路由没改这里，
// 于是三处（路由表 / 侧边栏归属 / 面包屑）各写一份、互相漂移（issue #61）。
// 现在改为从阶段工作台声明派生，任一阶段路由的归属只能有一个来源。
const stageRouteNavMap: Record<string, string> = Object.fromEntries(
  [...taskWorkbenchPages, ...resultWorkbenchPages].map((page) => [page.route, page.navParent]),
)

const adminPages: NavPage[] = [
  { label: '运营监控', route: '/console/operations', icon: ServerCog, caption: '查看队列与运行状态', adminOnly: true },
  { label: 'AI 服务', route: '/console/admin/providers', icon: Database, caption: '管理 AI 服务', adminOnly: true },
  { label: '结果存储', route: '/console/admin/storage', icon: FolderCog, caption: '管理结果存储', adminOnly: true },
  { label: '生成规则', route: '/console/admin/strategies', icon: Workflow, caption: '管理生成规则', adminOnly: true },
  { label: '生成指令', route: '/console/admin/prompts', icon: Sparkles, caption: '管理模板与版本', adminOnly: true },
  { label: '操作记录', route: '/console/admin/audit', icon: Settings, caption: '查看变更记录', adminOnly: true },
]

// 状态文案改由 lib/datasetStatus.ts 提供（单一事实来源，见该文件头部的 issue #98 说明）。
// 函数签名保持不变，因此所有调用点无需改动。
//
// 关键变化：**default 不再返回原始状态串**。原实现 `default: return status`
// 会把 directions_completed 这类内部英文标识直接显示给用户；现在未知状态走
// describeDatasetStatus 的中性兜底文案。
function statusLabel(status: string) {
  return describeDatasetStatus(status).label
}

function progressPercent(status: string) {
  if (status in DATASET_STATUS_PROGRESS) {
    return DATASET_STATUS_PROGRESS[status as DatasetStatus]
  }
  // 已知后缀的未知状态：至少给一个「已经开始」的非零值，
  // 不要像原实现那样落 default=10 却被 pipeline 的 completionPercent 盖成 0%。
  if (status.endsWith('_generated') || status.endsWith('_completed')) return 100
  if (status.endsWith('_queued')) return 35
  if (status.endsWith('_failed') || status.endsWith('_partial_failed')) return 35
  return 15
}

function nextActionLabel(status: string) {
  switch (status) {
    case 'draft':
      return '先确认主题结构，再启动问题生成'
    case 'domains_confirmed':
    case 'directions_completed':
      return '启动问题生成，补齐任务素材'
    case 'directions_partial_failed':
      return '先复核方向结果，再继续下一步'
    case 'directions_queued':
    case 'chain_standards_queued':
    case 'grpo_queued':
    case 'sft_queued':
      return '等待后台处理完成后继续'
    case 'questions_queued':
      return '等待问题后进入答案生成'
    case 'questions_generated':
      return '启动答案生成，形成可评审内容'
    case 'reasoning_queued':
      return '等待答案后进入质量评估'
    case 'reasoning_generated':
      return '启动质量评分，准备交付结论'
    case 'rewards_queued':
      return '等待质量评估后导出'
    case 'rewards_generated':
      return '导出结果包并通知验收'
    case 'export_queued':
      return '等待导出结果准备完成'
    case 'export_generated':
      return '下载结果后进入下一步'
    default:
      return '刷新状态后继续'
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
    case 'directions_completed':
      return '等待你启动问题生成'
    case 'directions_queued':
    case 'chain_standards_queued':
      return '方向生成处理中'
    case 'directions_partial_failed':
      return '方向结果不完整，等待你复核'
    case 'grpo_queued':
    case 'sft_queued':
      return '等待后台处理中'
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
      return '状态同步中'
  }
}

function waitingReasonLabel(status: string, queueDepth: number) {
  if (status.endsWith('_queued')) {
    return queueDepth > 0
      ? '结果已入队，系统按顺序执行。'
      : '该阶段已入队，正在等待执行资源分配。'
  }
  switch (status) {
    case 'draft':
      return '结构未确认，尚未开始生成。'
    case 'domains_confirmed':
      return '结构已确认，等待你启动方向与问题生成。'
    case 'directions_completed':
      return '方向结果已生成，等待你启动问题生成。'
    case 'directions_partial_failed':
      return '部分方向生成失败，建议复核后再继续。'
    case 'questions_generated':
      return '问题结果已准备好，等待你启动答案生成。'
    case 'reasoning_generated':
      return '答案结果已准备好，等待你启动质量评分。'
    case 'rewards_generated':
      return '评分结果已准备好，等待你启动导出。'
    case 'export_generated':
      return '导出文件已生成，等待你下载并交付。'
    default:
      return '系统同步中，请稍后刷新。'
  }
}

function waitingActionLabel(status: string) {
  switch (status) {
    case 'draft':
      return '前往「主题结构」'
    case 'domains_confirmed':
    case 'directions_completed':
      return '前往「问题生成」，开始生成题目。'
    case 'directions_partial_failed':
      return '回到「主题结构」复核方向结果。'
    case 'questions_queued':
    case 'reasoning_queued':
    case 'rewards_queued':
    case 'export_queued':
      return '先处理其他步骤，再按刷新建议回看'
    case 'questions_generated':
      return '前往「答案生成」，点击「开始生成答案」。'
    case 'questions_failed':
      return '请先回到「题目结果」排查失败原因，再重新生成。'
    case 'reasoning_generated':
    case 'reasoning_partial':
      return '前往「质量评分」，确认可用后再启动评分。'
    case 'reasoning_failed':
      return '请先回到「答案结果」排查失败原因，再重新生成。'
    case 'rewards_generated':
    case 'rewards_partial':
      return '前往「结果交付」，确认可用后再导出。'
    case 'rewards_failed':
      return '请先回到「质量评分」排查失败原因，再重新评分。'
    case 'export_generated':
      return '进入「结果交付」下载并确认'
    case 'export_failed':
      return '请重新发起导出，或先检查上游评分结果是否完整。'
    default:
      return '刷新后继续'
  }
}

function refreshExpectationLabel(status: string, queueDepth: number) {
  if (status.endsWith('_queued')) {
    if (queueDepth > 3) return '2~3 分钟后刷新。'
    return '60~90 秒后刷新。'
  }
  if (status === 'export_generated' || status.endsWith('_generated')) {
    return '当前阶段已完成，可进入下一步'
  }
  return '阶段无需频繁刷新，状态变化后再同步。'
}

function trustMessageLabel(status: string) {
  if (status.endsWith('_queued')) {
    return '任务已转入后台处理，可先切换到其他页面。'
  }
  if (status.endsWith('_generated') || status === 'export_generated') {
    return '结果已落库，可继续后续操作。'
  }
  return '系统会自动保存任务上下文'
}

type StageKey = 'questions' | 'reasoning' | 'rewards' | 'export'

function statusToActionRoute(status: string): string {
  if (status in DATASET_STATUS_ROUTE) {
    return DATASET_STATUS_ROUTE[status as DatasetStatus]
  }
  // 未知状态的去向：按后缀推断阶段，而不是一律回主题结构。
  // 一律回主题结构是 issue #98 的第四个症状（方向已生成却把用户送回起点）。
  if (status.includes('reasoning')) return '/console/reasoning'
  if (status.includes('rewards') || status.includes('eval')) return '/console/rewards'
  if (status.includes('export')) return '/console/exports'
  if (status.includes('question')) return '/console/questions'
  return '/console/domains'
}

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
    case 'directions_queued':
      return { min: 3, max: 10 }
    case 'chain_standards_queued':
      return { min: 2, max: 8 }
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
  if (status.endsWith('_generated') || status.endsWith('_completed')) return '阶段已完成，可进入下一步'
  if (status.endsWith('_partial_failed')) return '部分失败，需复核后重试'
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
  if (status === 'draft') return '确认方向结构后显示 ETA'
  if (status === 'domains_confirmed') return '启动问题生成后显示 ETA'
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

// 阶段名文案。取值域来自 internal/store/dataset_store.go:438-442 的 5 个 Key，
// 当前被完整覆盖；但 `default: return key` 与 issue #98 是同一形态
// （后端新增阶段时会把英文 key 显示给用户），因此兜底改为中文。
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
      return '其他阶段'
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

// 领域来源文案。后端当前只写 "ai"（internal/llm/domain_generator.go:123）
// 且 migration 默认也是 'ai'（0003_dataset_graph.sql:15），因此今天不会泄漏。
// 但保留 `default: return source` 会让后端将来新增来源时再次暴露英文标识，
// 故与 #98 一并消除该形态。
function sourceLabel(source: string) {
  switch (source) {
    case 'ai':
      return '模型生成'
    default:
      return '其他来源'
  }
}

// 复核状态文案改由 lib/enumLabels.ts 提供（单一事实来源）。
//
// 这是 issue #98 的**同类缺陷**，父代理在检查同族函数时发现：
// 旧实现只处理 approved / pending，而 `draft` 是数据库默认值
// （sql/migrations/0003_dataset_graph.sql:16 的 DEFAULT 'draft'），
// 于是落到 `default: return status`，把英文 "draft" 显示给用户。
// 实测：库里全部 230 个 domain 的 review_status 都是 draft。
function reviewStatusLabel(status: string) {
  return describeDomainReviewStatus(status)
}

// 工件类型文案同样改为单一事实来源。
//
// 旧实现只处理 'jsonl-export'，但 artifact_type 的实际取值是
// `spec.Format + "-export"`（apps/worker/job_export_multi.go:113），
// 格式取 internal/exporter/exporter.go:48 的 canonicalFormats
// = [jsonl csv parquet alpaca sharegpt]。
// 实测：库里已有 sharegpt-export 与 alpaca-export，用户看到的就是英文原始串。
function artifactLabel(type: string) {
  return describeArtifactType(type)
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
  if (length >= 140) return { text: '完整', color: 'green' as const, note: '可用于评分。' }
  if (length >= 70) return { text: '可用', color: 'blue' as const, note: '关键信息齐全，可抽检。' }
  return { text: '待补充', color: 'orange' as const, note: '摘要较短，需重跑或复核。' }
}

function rewardQualityLabel(score: number) {
  if (score >= 0.85) return { text: '高质量', color: 'green' as const, note: '可导出。' }
  if (score >= 0.7) return { text: '可交付', color: 'blue' as const, note: '抽样复核后导出。' }
  if (score >= 0.5) return { text: '待优化', color: 'orange' as const, note: '补充推理后再评估。' }
  return { text: '风险', color: 'red' as const, note: '不宜直接导出，先回修。' }
}

function artifactDisplayName(objectKey: string) {
  return objectKey.split('/').pop() || objectKey
}

// 内容类型文案同样收敛到单一事实来源：旧 default 是 `return contentType || '未知类型'`，
// 会把 `application/octet-stream` 这类原始 MIME 头展示给用户。
function artifactContentTypeLabel(contentType: string) {
  return describeArtifactContentType(contentType)
}

function artifactContentTypeHint(contentType: string) {
  switch (contentType) {
    case 'application/jsonl':
      return '可直接导入主流训练平台。'
    case 'application/x-ndjson':
      return '按行组织样本，适合流式处理任务。'
    case 'application/json':
      return '结构化结果包，适合验收和复核。'
    default:
      return '请在交付前确认下游系统是否支持该文件类型。'
  }
}

// artifactUsageCategory 把工件归入「交付 / 复核 / 其他」，供导出页筛选与统计使用。
//
// 为什么以 artifactType 的 `-export` 后缀为主判据（而不是 content_type）：
//
// 之前只看 content_type，写成「`application/jsonl` 或 artifactType === 'jsonl-export' 才是交付」，
// 而**没有任何导出器产出 `application/jsonl`** —— 后端 5 个导出器的实际类型是：
//   alpaca   -> application/x-ndjson   （供交付用）
//   jsonl    -> application/x-ndjson   （供交付用）
//   sharegpt -> application/x-ndjson   （供交付用）
//   csv      -> text/csv
//   parquet  -> application/x-parquet-jsonl
// 于是交付类**永远为空**；而导出页默认筛选恰好是「交付优先」，
// 用户导出成功后打开导出页看到的是「尚未生成导出」—— 明明文件已经落盘。
// （父代理实测：dataset 50 的 status=export_generated 且有 1 个 artifact，
//   但 /console/exports 默认视图渲染出空状态。）
//
// 后端在 apps/worker/job_export_multi.go:113 构造 `artifactType = spec.Format + "-export"`，
// legacy 路径（apps/worker/main.go:464）也用 "jsonl-export"。
// 因此「用户主动触发的导出产物」有一个**可靠标记**：artifactType 以 `-export` 结尾。
// 以它为主判据，content_type 只用于在「其他」里细分，语义与后端一致且不会再漂移。
function artifactUsageCategory(artifact: Artifact): 'delivery' | 'review' | 'other' {
  // 导出产物 = 交付件。所有格式（jsonl/alpaca/sharegpt/csv/parquet）都算交付。
  if (artifact.artifactType?.endsWith('-export')) return 'delivery'
  // 非导出产物：JSON 类可读记录作为复核资料。
  if (artifact.contentType === 'application/json' || artifact.contentType === 'application/x-ndjson') return 'review'
  return 'other'
}

function artifactUsageLabel(category: 'delivery' | 'review' | 'other') {
  switch (category) {
    case 'delivery':
      return '交付'
    case 'review':
      return '复核资料'
    default:
      return '其他类型'
  }
}

function artifactSourceVersionHint(artifact: Artifact) {
  return `来源任务 #${artifact.datasetId} · 生成于 ${formatTime(artifact.createdAt)}；默认以最新时间为交付版本。`
}

function artifactDeliveryNote(artifact: Artifact) {
  const category = artifactUsageCategory(artifact)
  if (category === 'delivery') return '可直接作为标准交付包。'
  if (category === 'review') return '先用于人工复核或验收，再决定是否正式交付。'
  return '先确认格式兼容性与用途，再安排对外交付。'
}

function artifactDownloadDecisionHint(artifact: Artifact) {
  const category = artifactUsageCategory(artifact)
  if (category === 'delivery') return '优先下载：可进入下游流程。'
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
        <EmptyCard title="尚未生成方向结构" description="点击「生成方向结构」开始。" />
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

  // issue #107：本地必填 / 格式校验。
  //
  // 原先两个字段都为空时也直接发请求，把后端的英文
  // `email and password are required` 原样展示在告警卡与 toast 两处 ——
  // 既绕了一圈网络，又给了用户看不懂的英文。
  //
  // 原则（功能说明.txt 的「符合人机交互习惯」）：
  //   - 本地能判的不要往返服务端；
  //   - 提示要指出**具体哪个字段**错了，而不是一句笼统的「登录失败」；
  //   - 校验发生在提交前，错误就地显示在字段下方。
  const [fieldError, setFieldError] = useState<{ email?: string; password?: string }>({})

  const validate = (): boolean => {
    const next: { email?: string; password?: string } = {}
    const trimmedEmail = email.trim()
    if (!trimmedEmail) {
      next.email = '请输入邮箱'
    } else if (!/^[^\s@]+@[^\s@]+\.[^\s@]+$/.test(trimmedEmail)) {
      // 只做「像不像邮箱」的前置拦截；真实合法性仍由服务端判定。
      next.email = '邮箱格式不正确，请检查是否缺少 @ 或域名'
    }
    if (!password) {
      next.password = '请输入密码'
    }
    setFieldError(next)
    return Object.keys(next).length === 0
  }

  const handleSubmit = () => {
    // 本地校验没过就**不发请求**（这是 issue #107 的核心）。
    if (!validate()) return
    void onSubmit(email.trim(), password)
  }

  return (
    <div className="console-login-shell flex items-center justify-center px-4 py-10">
      <div className="grid w-full max-w-6xl gap-6 lg:grid-cols-[1.2fr,0.8fr]">
        <Card className="console-panel" bodyStyle={{ padding: 28 }}>
          <span className="console-chip">企业数据工厂</span>
          <Title heading={1} className="!mb-0 mt-4">先创建任务，再持续推进交付。</Title>
          <Text className="mt-4 block console-page-subtitle">
            控制台按「工作台 → 新建任务 → 我的任务 → 数据资产」组织。
            登录后先创建任务，再继续推进和交付。
          </Text>
          <div className="console-card-grid-2 mt-6">
            {[
              { icon: CirclePlus, title: '新建任务前置', text: '主入口更醒目' },
              { icon: Target, title: '任务推进清晰', text: '已有任务从「我的任务」继续' },
              { icon: HardDriveDownload, title: '结果集中看', text: '交付与复核集中' },
              { icon: ShieldCheck, title: '状态持续可见', text: '登录后可接续进度' },
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
              <Text className="console-caption">先进入工作台，再从新建任务或我的任务开始</Text>
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
              <Input
                value={email}
                onChange={(value) => { setEmail(value); if (fieldError.email) setFieldError((c) => ({ ...c, email: undefined })) }}
                size="large"
                placeholder="请输入邮箱"
              />
              {fieldError.email ? <Text className="mt-2 block" type="danger">{fieldError.email}</Text> : null}
            </div>
            <div>
              <Text className="mb-2 block font-medium">密码</Text>
              <Input
                value={password}
                onChange={(value) => { setPassword(value); if (fieldError.password) setFieldError((c) => ({ ...c, password: undefined })) }}
                mode="password"
                size="large"
                placeholder="请输入密码"
                onEnterPress={handleSubmit}
              />
              {fieldError.password ? <Text className="mt-2 block" type="danger">{fieldError.password}</Text> : null}
            </div>
            <Button theme="solid" type="primary" size="large" loading={loading} onClick={handleSubmit}>
进入我的任务
            </Button>
          </div>
          <div className="mt-6 console-summary-grid">
            <div className="console-summary-row"><span>登录后第一步</span><Text strong>先点击「新建任务」</Text></div>
            <div className="console-summary-row"><span>已有任务</span><Text strong>从「我的任务」继续</Text></div>
            <div className="console-summary-row"><span>交付完成后</span><Text strong>去「数据资产」查看和下载</Text></div>
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
  const [sidebarWidth, setSidebarWidth] = useState(260)
  const sidebarResizing = useRef(false)
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
  // 进阶能力（issue #65）：这些后端能力不在 5 个主阶段的线性流程里，
  // 此前 lib/api.ts 里有方法但**没有任何视图调用**（孤儿方法），用户无法触达。
  const [chainStandards, setChainStandards] = useState<ChainStandard[]>([])
  const [difficultyStats, setDifficultyStats] = useState<DifficultyStats | null>(null)
  const [grpoPrompts, setGrpoPrompts] = useState<GrpoPrompt[]>([])
  const [sftRecords, setSftRecords] = useState<SftRecord[]>([])
  const [exportMappings, setExportMappings] = useState<ExportMapping[]>([])
  const [generationRuns, setGenerationRuns] = useState<GenerationRun[]>([])
  const [exportFormats, setExportFormats] = useState<ExportFormatList | null>(null)
  const [showAdvancedGraphView, setShowAdvancedGraphView] = useState(false)
  const [pipelineProgress, setPipelineProgress] = useState<PipelineProgress | null>(null)
  const [stageRunMeta, setStageRunMeta] = useState<Partial<Record<StageKey, StageEnqueueResult>>>({})

  const [plannerForm, setPlannerForm] = useState({
    name: '',
    rootKeyword: '',
    targetSize: 0,
    strategyId: 0,
    providerId: 0,
    // n / m / x：功能说明.txt 步骤 1、3 要求这三者**用户可控**。
    // n = 领域数（domainCount，由策略/估算决定规模）
    // m = 每个领域下的方向数（directionCount，落到 datasets.direction_count）
    // x = 每个方向的问题数（questionsPerDirection，落到 datasets.questions_per_direction）
    // 留 0 表示「用后端默认」，避免把「未设置」与「显式设为 0」混为一谈。
    domainCount: 0,
    directionCount: 0,
    questionsPerDirection: 0,
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
    // issue #108：原先默认预填 1000，用户「只填名称就保存」会得到一条按 1000 个领域
    // 规划的策略（正是 issue #63 里 domainCount=1000 脏数据的来源），
    // 而用户从未做过这个决策。改为小规模可解释的默认值，配合界面文案
    //「先小规模验证，再逐步放大」——与创建任务页的规模提示保持一致。
    domainCount: DEFAULT_STRATEGY_DOMAIN_COUNT,
    questionsPerDomain: DEFAULT_STRATEGY_QUESTIONS_PER_DOMAIN,
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
  const activeTaskNavLabel = activeDataset ? '返回当前任务' : '返回我的任务'
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
    const currentWidth = sidebarCollapsed ? 48 : sidebarWidth
    document.documentElement.style.setProperty('--sidebar-current-width', `${currentWidth}px`)
    return () => document.body.classList.remove('sidebar-collapsed')
  }, [sidebarCollapsed, sidebarWidth])

  const handleSidebarResizeStart = useCallback((e: React.MouseEvent) => {
    e.preventDefault()
    sidebarResizing.current = true
    const onMove = (ev: MouseEvent) => {
      if (!sidebarResizing.current) return
      const newWidth = Math.min(400, Math.max(200, ev.clientX))
      setSidebarWidth(newWidth)
      document.documentElement.style.setProperty('--sidebar-current-width', `${newWidth}px`)
    }
    const onUp = () => {
      sidebarResizing.current = false
      document.removeEventListener('mousemove', onMove)
      document.removeEventListener('mouseup', onUp)
      document.body.style.cursor = ''
      document.body.style.userSelect = ''
    }
    document.body.style.cursor = 'col-resize'
    document.body.style.userSelect = 'none'
    document.addEventListener('mousemove', onMove)
    document.addEventListener('mouseup', onUp)
  }, [])

  const breadcrumbLabel = useMemo(() => {
    const currentPage = visiblePages.find(
      (page) => location.pathname === page.route || location.pathname.startsWith(`${page.route}/`),
    )
    if (currentPage) return currentPage.label
    const taskDetailMatch = location.pathname.match(/^\/console\/tasks\/(\d+)/)
    if (taskDetailMatch) return `任务 #${taskDetailMatch[1]}`
    const stagePages = [...taskWorkbenchPages, ...resultWorkbenchPages]
    const stagePage = stagePages.find((p) => location.pathname === p.route)
    if (stagePage) return stagePage.label
    return '控制台'
  }, [location.pathname, visiblePages])

  const breadcrumbParent = useMemo(() => {
    if (location.pathname.startsWith('/console/admin/')) return '系统设置'
    if (location.pathname.match(/^\/console\/tasks\/\d+/)) return '我的任务'
    return null
  }, [location.pathname])

  const pipelineStages = useMemo(() => {
    if (!activePipeline) return null
    return activePipeline.stages.map((stage) => ({
      key: stage.key,
      label: stageKeyLabel(stage.key),
      state: stage.state,
    }))
  }, [activePipeline])

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
    domainCount: DEFAULT_STRATEGY_DOMAIN_COUNT,
    questionsPerDomain: DEFAULT_STRATEGY_QUESTIONS_PER_DOMAIN,
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
        recoveryHint: '登录后继续；反复过期时联系管理员。',
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
        recoveryHint: '确认当前角色；需要更高权限时联系管理员。',
        nextStep: { label: '查看恢复指南', route: '/console/help' },
      })
      Toast.warning(message)
      return true
    }

    return false
  }, [navigate])

  // 统一的「未选中任务」提示通道（issue #64）。
  //
  // 与 handleRequestError 的分工：那个处理「请求失败了」，这个处理「压根没法发请求 ——
  // 缺前置条件」。两者都必须用户可见，否则就是静默失败。
  //
  // 同时给 Toast 与页内提示卡：Toast 负责「此刻看得见」，提示卡负责「划走之后还能找回」。
  const notifyNoActiveTask = useCallback((message: string) => {
    Toast.warning(message)
    setTrustSignal({
      tone: 'warning',
      title: '尚未选择任务',
      detail: message,
      recoveryHint: '先到「我的任务」选一个任务进入详情，再回到本页执行该操作。',
      nextStep: { label: '去选择任务', route: '/console/tasks' },
    })
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
    setBootstrapLoading(true)
    try {
      // 关键（issue #101）：providers / storage-profiles / strategies 三个接口
      // 是**管理员专用**（后端返 403）。此前它们与 datasets / runtime 放在同一个
      // Promise.all 里，于普通用户下**任一 403 会让整个 Promise.all reject**：
      //   - 弹「权限不足，操作未执行 / admin privileges required」告警；
      //   - datasets 的返回值被丢弃 → 任务列表永远为空、侧边栏「任务 0」；
      //   - 新建任务页的「AI 服务 / 存储配置」下拉框为空。
      //   即普通用户拿到的是「一进首页就报错、看不到数据、也建不了任务」的空壳。
      //
      // 修法：按角色分组，普通用户**根本不发**这些请求（也就不会产生 403），
      // 而不是让它们失败后再静默吞错 —— 后者会掩盖真实权限问题。
      const [datasetData, runtimeData] = await Promise.all([
        consoleApi.listDatasets(),
        consoleApi.runtimeStatus(),
      ])
      setDatasets(datasetData)
      setRuntime(runtimeData)

      if (isAdmin) {
        const [providerData, storageData, strategyData] = await Promise.all([
          consoleApi.listProviders(),
          consoleApi.listStorageProfiles(),
          consoleApi.listStrategies(),
        ])
        setProviders(providerData)
        setStorageProfiles(storageData)
        setStrategies(strategyData)
        setPlannerForm((current) => ({
          ...current,
          strategyId: current.strategyId || strategyData[0]?.id || 0,
          providerId: current.providerId || providerData[0]?.id || 0,
          storageProfileId: current.storageProfileId || storageData.find((item) => item.isActive)?.id || storageData[0]?.id || 0,
        }))
        await loadAdminData()
      }
      if (successMessage) Toast.success(successMessage)
    } catch (error) {
      if (!handleRequestError(error)) Toast.error((error as Error).message)
    } finally {
      setBootstrapLoading(false)
    }
  }, [isAdmin, loadAdminData])

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

  // 进阶能力的数据加载。与主工作区分开：这些能力在部分数据集上不可用
  //（例如未确认主题就没有标准步骤），失败不应把整个工作区拉成错误态。
  const loadCapabilityData = useCallback(async (datasetId: number) => {
    const settle = async <T,>(task: Promise<T>, label: string): Promise<T | null> => {
      try {
        return await task
      } catch {
        // 单项不可用是常态（前置阶段未完成），不是错误；但不要静默：
        // 在控制台留下可排查的痕迹。
        console.warn(`[capability] ${label} 加载失败（前置阶段可能未完成）`)
        return null
      }
    }
    const [standards, difficulty, grpo, sft, mappings, runs, formats] = await Promise.all([
      settle(consoleApi.listChainStandards(datasetId), 'chain-standards'),
      settle(consoleApi.questionDifficultyStats(datasetId), 'difficulty-stats'),
      settle(consoleApi.listGrpo(datasetId), 'grpo'),
      settle(consoleApi.listSft(datasetId), 'sft'),
      settle(consoleApi.listExportMappings(), 'export-mappings'),
      settle(consoleApi.listGenerationRuns(datasetId), 'generation-runs'),
      settle(consoleApi.exportFormats(datasetId), 'export-formats'),
    ])
    setChainStandards(standards ?? [])
    setDifficultyStats(difficulty)
    setGrpoPrompts(grpo ?? [])
    setSftRecords(sft ?? [])
    setExportMappings(mappings ?? [])
    setGenerationRuns(runs ?? [])
    setExportFormats(formats)
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
    // 进阶能力数据单独加载：其中几项在前置阶段未完成时不可用，
    // 失败不应把主工作区拉成错误态（loadCapabilityData 内部逐项 settle）。
    void loadCapabilityData(routeDatasetId)
  }, [activeDatasetId, graph?.dataset.id, loadCapabilityData, loadDatasetWorkspace, location.pathname, user])

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
        recoveryHint: '账号密码错误时联系管理员重置。',
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
        detail: '任务主题和目标规模是必填项。',
        recoveryHint: '先补齐上述两项，再点击「创建任务」。',
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
        // m / x 直接落到 datasets.direction_count / questions_per_direction。
        // 传 0 时后端回退到内置默认（m=3, x=5），保持「未设置」语义。
        directionCount: Number(plannerForm.directionCount) || 0,
        questionsPerDirection: Number(plannerForm.questionsPerDirection) || 0,
        // n（领域数）不在 datasets 列上，而是通过 estimate.domainCount 生效：
        // internal/llm/domain_generator.go 的 GenerateDomains 读 dataset.Estimate.DomainCount。
        // 用户显式填了 n 就覆盖估算值，否则用估算值（保持原行为）。
        estimate: plannerForm.domainCount > 0
          ? { ...estimateSnapshot, domainCount: Number(plannerForm.domainCount) }
          : estimateSnapshot,
      })
      setActiveDatasetId(created.id)
      setTrustSignal({
        tone: 'success',
        title: '任务创建成功',
        detail: '进入「整理主题」继续下一步。',
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
        recoveryHint: '检查策略、AI 服务和存储配置后重试。',
        nextStep: { label: '查看配置', route: '/console/planning' },
      })
      Toast.error(message)
    } finally {
      setActionLoading(false)
    }
  }

  // 生成方向结构。
  //
  // m（每领域方向数）不在此处传参：worker 从 dataset.direction_count 读
  //（见 apps/api/routes_directions.go 的 directionCount 回退链），
  // 而该值在创建任务时由用户设定（见 createDataset 与「每领域方向数 m」输入框）。
  const generateDomains = async () => {
    const datasetId = resolveDatasetId(activeDatasetId, notifyNoActiveTask, '生成方向结构')
    if (!datasetId) return
    setActionLoading(true)
    try {
      const nextGraph = await consoleApi.generateDomains(datasetId)
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
            ? '请到「AI 服务」页切换模型（建议优先使用标准 chat/completions 兼容模型）后重试；也可先回到任务创建页缩小主题范围。'
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
        recoveryHint: '保存修改后再确认当前数据集可访问。',
        nextStep: { label: '返回整理主题', route: '/console/domains' },
      })
      Toast.error(message)
    } finally {
      setActionLoading(false)
    }
  }

  const generateQuestions = async () => {
    const datasetId = resolveDatasetId(activeDatasetId, notifyNoActiveTask, '生成题目')
    if (!datasetId) return
    setActionLoading(true)
    try {
      // x（每方向问题数）不在请求体里传：worker 从 dataset.questions_per_direction 读，
      // 而该值在创建任务时由用户设定（见 createDataset）。在这里再传一遍会造成
      // 「两个真相源」：任务页改了、但任务配置没变，下次重跑又回旧值。
      const result = await consoleApi.generateQuestions(datasetId)
      setStageRunMeta((current) => ({ ...current, questions: result }))
      setTrustSignal({
        tone: 'info',
        title: '题目生成已开始',
        detail: `${result.message}，ETA：${etaLabel('questions_queued', runtime?.queueDepth ?? 0, result.acceptedAt)}。可先切换到其他页面。`,
        recoveryHint: '60~90 秒后刷新；等待任务多时可延后到 2~3 分钟。',
        nextStep: { label: '查看题目页', route: '/console/questions' },
      })
      Toast.success(result.message)
      await loadDatasetWorkspace(datasetId)
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
    const datasetId = resolveDatasetId(activeDatasetId, notifyNoActiveTask, '生成答案')
    if (!datasetId) return
    setActionLoading(true)
    try {
      const result = await consoleApi.generateReasoning(datasetId)
      setStageRunMeta((current) => ({ ...current, reasoning: result }))
      setTrustSignal({
        tone: 'info',
        title: '答案生成已开始',
        detail: `${result.message}，ETA：${etaLabel('reasoning_queued', runtime?.queueDepth ?? 0, result.acceptedAt)}。可先切换到其他页面。`,
        recoveryHint: '60~90 秒后刷新；等待任务多时可延后到 2~3 分钟。',
        nextStep: { label: '查看推理页', route: '/console/reasoning' },
      })
      Toast.success(result.message)
      await loadDatasetWorkspace(datasetId)
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
    const datasetId = resolveDatasetId(activeDatasetId, notifyNoActiveTask, '生成质量评估')
    if (!datasetId) return
    setActionLoading(true)
    try {
      const result = await consoleApi.generateRewards(datasetId)
      setStageRunMeta((current) => ({ ...current, rewards: result }))
      setTrustSignal({
        tone: 'info',
        title: '质量评估已开始',
        detail: `${result.message}，ETA：${etaLabel('rewards_queued', runtime?.queueDepth ?? 0, result.acceptedAt)}。可先切换到其他页面。`,
        recoveryHint: '60~90 秒后刷新；等待任务多时可延后到 2~3 分钟。',
        nextStep: { label: '查看质量评估', route: '/console/rewards' },
      })
      Toast.success(result.message)
      await loadDatasetWorkspace(datasetId)
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
    const datasetId = resolveDatasetId(activeDatasetId, notifyNoActiveTask, '导出结果')
    if (!datasetId) return
    setActionLoading(true)
    try {
      const result = await consoleApi.generateExport(datasetId)
      setStageRunMeta((current) => ({ ...current, export: result }))
      setTrustSignal({
        tone: 'info',
        title: '导出任务已开始',
        detail: `${result.message}，ETA：${etaLabel('export_queued', runtime?.queueDepth ?? 0, result.acceptedAt)}。可先切换到其他页面。`,
        recoveryHint: '60~90 秒后刷新；等待任务多时可延后到 2~3 分钟。',
        nextStep: { label: '前往结果交付', route: '/console/exports' },
      })
      Toast.success(result.message)
      await loadDatasetWorkspace(datasetId)
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
      // 走共享的 axios client（见 lib/api.ts 的 downloadArtifactBlob）：
      // 它带 withCredentials 与响应拦截器，会话过期时会得到「登录状态已失效」
      // 而不是裸 HTTP 错误。之前的裸 fetch 绕过了整个拦截器。
      const response = await consoleApi.downloadArtifactBlob(artifact.datasetId, artifact.id)
      const blob = response.data
      if (!(blob instanceof Blob) || blob.size === 0) {
        throw new Error('交付文件为空，请先在结果页确认导出已完成')
      }
      const fileName = consoleApi.artifactFileName(
        response.headers?.['content-disposition'],
        artifact.objectKey,
      )
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
    { icon: Database, label: '任务总数', value: runtime?.datasetCount ?? 0, helper: '当前任务数' },
    { icon: Layers3, label: '题目结果', value: runtime?.questionCount ?? 0, helper: '已生成题目' },
    { icon: BrainCircuit, label: '答案结果', value: runtime?.reasoningCount ?? 0, helper: '已生成答案' },
    { icon: HardDriveDownload, label: '结果文件', value: runtime?.artifactCount ?? 0, helper: '已生成文件' },
  ]

  const planningCards = estimate
    ? [
        { icon: Network, label: '领域数', value: estimate.domainCount, helper: '建议领域数' },
        { icon: Layers3, label: '每领域问题数', value: estimate.questionsPerDomain, helper: '单领域问题量' },
        { icon: BrainCircuit, label: '预计问题总量', value: estimate.estimatedQuestions, helper: '待生成问题' },
        { icon: FileOutput, label: '预计样本总量', value: estimate.estimatedSamples, helper: '最终样本规模' },
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
        detail: `当前有 ${lowScoreCount} 条评分低于 0.5，先回看答案阶段。`,
      })
    }
    if (activeDataset?.status.endsWith('_queued') && queuedMinutes !== null && queuedMinutes >= 15) {
      riskItems.push({
        level: 'high',
        title: '排队时长偏高',
        detail: `当前阶段已等待 ${queuedMinutes} 分钟，建议刷新状态。`,
      })
    }
    if (queueDepth >= 8) {
      riskItems.push({
        level: 'medium',
        title: '系统队列压力较高',
        detail: `前方约 ${queueDepth} 个任务，先降低刷新频率。`,
      })
    }
    if (exportDeliveryPending) {
      riskItems.push({
        level: 'medium',
        title: '导出交付仍在落盘',
        detail: '结果正在写入交付文件。',
      })
    }
    if (activeDataset?.status === 'export_generated' && !exportDeliveryPending && artifacts.length === 0) {
      riskItems.push({
        level: 'high',
        title: '导出完成但未发现交付文件',
        detail: '刷新导出页；若仍为空，检查存储配置。',
      })
    }
    if (riskItems.length === 0) {
      riskItems.push({
        level: 'low',
        title: '当前无明显风险',
        detail: '流程状态稳定，可继续推进。',
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
          badge="工作台"
          title="任务与待办"
          description="只看任务、风险、结果和待办"
          actions={
            <>
              <Button theme="solid" type="primary" icon={<CirclePlus size={16} />} onClick={() => navigate('/console/planning')}>
                新建任务
              </Button>
              <Button icon={<Target size={16} />} onClick={() => navigate('/console/tasks')}>查看我的任务</Button>
              <Button loading={bootstrapLoading} icon={<RefreshCw size={16} />} onClick={() => void loadBootstrap('工作台数据已刷新')}>
                刷新工作台
              </Button>
            </>
          }
        />

        <Banner
          type={hasHighRisk ? 'warning' : 'info'}
          icon={<Bell size={16} />}
          description={hasHighRisk ? '先处理高风险。' : '状态稳定，可推进。'}
        />

        <div className="console-card-grid-2">
          <Card className="console-panel" bodyStyle={{ padding: 20 }}>
            <Title heading={4} className="!mb-0">任务摘要</Title>
            <Text className="mt-2 block console-caption">只保留任务、动向和入口。</Text>
            <div className="mt-5 console-summary-grid">
              <div className="console-summary-row"><span>当前任务</span><Text strong>{activeDataset?.name ?? '尚未创建任务'}</Text></div>
              <div className="console-summary-row"><span>任务 ID</span><Text strong>{activeDataset?.id ?? '—'}</Text></div>
              <div className="console-summary-row"><span>阶段状态</span><Text strong>{activeDataset ? (exportDeliveryPending ? '导出收尾中' : statusLabel(activeDataset.status)) : '—'}</Text></div>
              <div className="console-summary-row"><span>完成进度</span><Text strong>{activePipeline ? `${activePipeline.completionPercent}%` : activeDataset ? `${progressPercent(activeDataset.status)}%` : '—'}</Text></div>
              <div className="console-summary-row"><span>当前 ETA</span><Text strong>{activeEta}</Text></div>
              <div className="console-summary-row"><span>最近活跃任务</span><Text strong>{recentDatasets[0]?.name ?? '暂无任务'}</Text></div>
              <div className="console-summary-row"><span>最近更新时间</span><Text strong>{recentDatasets[0] ? formatTime(recentDatasets[0].updatedAt) : '—'}</Text></div>
              <div className="console-summary-row"><span>下一步入口</span><Button size="small" onClick={() => navigate('/console/tasks')}>进入我的任务</Button></div>
            </div>
          </Card>

          <Card className="console-panel" bodyStyle={{ padding: 20 }}>
            <Title heading={4} className="!mb-0">最近结果</Title>
            <Text className="mt-2 block console-caption">结果规模与最新交付。</Text>
            <div className="mt-5 console-summary-grid">
              <div className="console-summary-row"><span>题目结果</span><Text strong>{questions.length}</Text></div>
              <div className="console-summary-row"><span>答案结果</span><Text strong>{reasoning.length}</Text></div>
              <div className="console-summary-row"><span>评分结果</span><Text strong>{rewards.length}</Text></div>
              <div className="console-summary-row"><span>交付文件</span><Text strong>{artifacts.length}</Text></div>
              <div className="console-summary-row"><span>最新产出</span><Text strong>{recentArtifacts[0] ? artifactDisplayName(recentArtifacts[0].objectKey) : '暂无导出结果'}</Text></div>
              <div className="console-summary-row"><span>产出时间</span><Text strong>{recentArtifacts[0] ? formatTime(recentArtifacts[0].createdAt) : '—'}</Text></div>
            </div>
            <Space className="mt-5" wrap>
              <Button theme="solid" type="primary" onClick={() => navigate('/console/results')}>进入数据资产</Button>
              {recentArtifacts[0] ? <Button onClick={() => void downloadArtifact(recentArtifacts[0])}>下载最新产出</Button> : null}
            </Space>
          </Card>
        </div>

        <div className="console-card-grid-2">
          <Card className="console-panel" bodyStyle={{ padding: 20 }}>
            <Title heading={4} className="!mb-0">风险提醒</Title>
            <Text className="mt-2 block console-caption">风险优先，其他按节奏处理。</Text>
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

          {activeDataset?.failureReason ? (
            <Banner
              type="danger"
              className="mt-4"
              closeIcon={null}
              title={`${statusLabel(activeDataset.status)}：${activeDataset.failureReason}`}
              description={
                <Text>
                  这是本阶段的真实失败原因（不是「系统同步中」）。处理完上方提示后，
                  可在下方「待办动作」重新发起本阶段。
                </Text>
              }
            />
          ) : null}

          <Card className="console-panel" bodyStyle={{ padding: 20 }}>
            <Title heading={4} className="!mb-0">待办动作</Title>
            <Text className="mt-2 block console-caption">按顺序执行，减少无效刷新。</Text>
            <div className="mt-5 console-summary-grid">
              <div className="console-summary-row"><span>下一步</span><Text strong>{activeDataset ? (exportDeliveryPending ? '等待交付文件出现后再下载结果' : nextActionLabel(activeDataset.status)) : '先点击「新建任务」'}</Text></div>
              <div className="console-summary-row"><span>等待原因</span><Text strong>{activeDataset ? (exportDeliveryPending ? '导出计算已完成，系统正在将结果写入交付存储。' : waitingReasonLabel(activeDataset.status, queueDepth)) : '创建任务后显示'}</Text></div>
              <div className="console-summary-row"><span>建议动作</span><Text strong>{activeDataset ? (exportDeliveryPending ? '暂时无需重复触发导出，等待交付文件落盘后再下载。' : waitingActionLabel(activeDataset.status)) : '点击顶部「新建任务」开始'}</Text></div>
              <div className="console-summary-row"><span>刷新节奏</span><Text strong>{activeDataset ? (exportDeliveryPending ? '系统会每 20 秒自动刷新；检测到交付文件后将自动变为可下载。' : refreshExpectationLabel(activeDataset.status, queueDepth)) : '创建任务后显示'}</Text></div>
              <div className="console-summary-row"><span>进度保障</span><Text strong>{activeDataset ? (exportDeliveryPending ? '任务已完成导出计算，正在落盘交付文件，请勿重复触发导出。' : trustMessageLabel(activeDataset.status)) : '创建任务后显示'}</Text></div>
            </div>
            <Space className="mt-5" wrap>
              <Button theme="solid" type="primary" onClick={() => navigate(nextRoute)}>{activeDataset ? nextActionLabel(activeDataset.status) : '新建任务'}</Button>
              <Button onClick={() => navigate('/console/tasks')}>查看我的任务</Button>
              <Button onClick={() => navigate('/console/results')}>查看最近结果</Button>
            </Space>
          </Card>
        </div>

        <Card className="console-panel" bodyStyle={{ padding: 20 }}>
          <Title heading={4} className="!mb-0">系统信号</Title>
          <Text className="mt-2 block console-caption">保留当前任务相关的轻量系统信号。</Text>
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
          badge="我的任务"
          title="任务列表与处理"
          description="看任务列表并处理"
          actions={
            <>
              <Button theme="solid" type="primary" icon={<CirclePlus size={16} />} onClick={() => navigate('/console/planning')}>新建任务</Button>
              <Button icon={<RefreshCw size={16} />} loading={bootstrapLoading} onClick={() => void loadBootstrap('我的任务页已刷新')}>刷新列表</Button>
              <Button onClick={() => navigate('/console/results')}>查看数据资产</Button>
            </>
          }
        />

        <div className="console-card-grid-2">
          <Card className="console-panel" bodyStyle={{ padding: 20 }}>
            <Title heading={4} className="!mb-0">任务列表概览</Title>
            <Text className="mt-2 block console-caption">看任务列表与处理。</Text>
            <div className="mt-5 console-summary-grid">
              <div className="console-summary-row"><span>任务总数</span><Text strong>{datasets.length}</Text></div>
              <div className="console-summary-row"><span>当前任务</span><Text strong>{activeDataset?.name ?? '暂无任务'}</Text></div>
              <div className="console-summary-row"><span>最近活跃</span><Text strong>{recentDatasets[0]?.name ?? '暂无任务'}</Text></div>
              <div className="console-summary-row"><span>等待任务数</span><Text strong>{runtime?.queueDepth ?? 0}</Text></div>
            </div>
            <Space className="mt-5" wrap>
              <Button theme="solid" type="primary" icon={<CirclePlus size={16} />} onClick={() => navigate('/console/planning')}>新建任务</Button>
              <Button onClick={() => navigate(recentDatasets[0] ? `/console/tasks/${recentDatasets[0].id}` : '/console/tasks')}>继续当前任务</Button>
            </Space>
          </Card>

          <Card className="console-panel" bodyStyle={{ padding: 20 }}>
            <Title heading={4} className="!mb-0">任务列表</Title>
            <Text className="mt-2 block console-caption">按最近更新时间浏览任务并进入详情。</Text>
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

  // 进阶能力入口（issue #65）。
  //
  // 每一项对应一个后端已实现、但此前在 UI 上**无法触达**的能力。
  // 共同约定：
  //   1. 未选中任务时给明确提示，不静默 return（与 #64 的守卫约定一致）；
  //   2. run() 发**真实请求**（不是占位），成功后刷新对应数据；
  //   3. 失败走既有 handleRequestError，不自己吞错。
  const capabilityActions: Array<{
    key: string
    label: string
    badge: string
    badgeColor: 'green' | 'grey' | 'orange' | 'blue'
    description: string
    statusText: string
    actionLabel: string
    run: () => Promise<unknown>
    refresh: () => Promise<unknown>
  }> = (() => {
    const requireDataset = (label: string): number | null => {
      if (!activeDatasetId) {
        Toast.warning(`请先选择任务，再${label}`)
        return null
      }
      return activeDatasetId
    }

    const wrap = async (label: string, task: () => Promise<unknown>, reload = true) => {
      setActionLoading(true)
      try {
        await task()
        Toast.success(`${label}已触发`)
        if (reload && activeDatasetId) await loadCapabilityData(activeDatasetId)
      } catch (error) {
        if (!handleRequestError(error)) Toast.error((error as Error).message)
      } finally {
        setActionLoading(false)
      }
    }

    return [
      {
        key: 'directions',
        label: 'R1 方向生成（n 领域 → m 方向）',
        badge: graph?.domains?.length ? `已生成 ${graph.domains.length} 个方向` : '未生成',
        badgeColor: (graph?.domains?.length ? 'green' : 'grey') as 'green' | 'grey',
        description: '调用 llm 根据已确认的领域生成其下属方向（m 由任务参数控制），支持断点续跑。',
        statusText: generationRuns.length > 0
          ? `最近 ${generationRuns.length} 次生成运行记录；可对失败/部分失败运行续跑。`
          : '尚无生成运行记录；需要先有领域。',
        actionLabel: '生成方向',
        run: async () => {
          const id = requireDataset('生成方向')
          if (id === null) return
          await wrap('方向生成', () => consoleApi.generateDirections(id))
        },
        refresh: async () => {
          if (!activeDatasetId) return Toast.warning('请先选择任务，再刷新方向')
          await loadCapabilityData(activeDatasetId)
        },
      },
      {
        key: 'chain-standards',
        label: 'L2 长链思维标准步骤',
        badge: chainStandards.length > 0 ? `${chainStandards.length} 个领域` : '未生成',
        badgeColor: chainStandards.length > 0 ? 'green' : 'grey',
        description: '为每个领域生成可编辑、可版本化的长链思维标准步骤（后续问题与答案生成会引用它）。',
        statusText: chainStandards.length > 0
          ? `已覆盖 ${chainStandards.length} 个领域；当前版本号最高 ${Math.max(...chainStandards.map((item) => item.currentVersion ?? 0), 0)}`
          : '尚未生成；需要先确认主题结构。',
        actionLabel: '生成长链标准步骤',
        run: async () => {
          const id = requireDataset('生成长链标准步骤')
          if (id === null) return
          await wrap('长链标准步骤生成', () => consoleApi.generateChainStandards(id))
        },
        refresh: async () => {
          if (!activeDatasetId) return Toast.warning('请先选择任务，再刷新标准步骤')
          await loadCapabilityData(activeDatasetId)
        },
      },
      {
        key: 'difficulty',
        label: 'L3 难度分层统计',
        badge: difficultyStats ? `${difficultyStats.total ?? 0} 题` : '无数据',
        badgeColor: difficultyStats && (difficultyStats.levels?.hard ?? 0) > 0 ? 'green' : 'orange',
        description: '查看问题在简单/中等/困难三档上的实际分布，验证难度配比是否落地。',
        statusText: difficultyStats
          ? `简单 ${difficultyStats.levels?.easy ?? 0} · 中等 ${difficultyStats.levels?.medium ?? 0} · 困难 ${difficultyStats.levels?.hard ?? 0}（共 ${difficultyStats.total ?? 0} 题）`
          : '尚无难度数据；需要先生成问题。',
        actionLabel: '刷新难度统计',
        run: async () => {
          const id = requireDataset('刷新难度统计')
          if (id === null) return
          await wrap('难度统计刷新', async () => { setDifficultyStats(await consoleApi.questionDifficultyStats(id)) }, false)
        },
        refresh: async () => {
          if (!activeDatasetId) return Toast.warning('请先选择任务，再刷新难度统计')
          await loadCapabilityData(activeDatasetId)
        },
      },
      {
        key: 'grpo',
        label: 'L4 GRPO 教师评判提示词',
        badge: grpoPrompts.length > 0 ? `${grpoPrompts.length} 条` : '未生成',
        badgeColor: grpoPrompts.length > 0 ? 'green' : 'grey',
        description: '按用户设定的打分档次，为每个问题自动生成供教师模型打分的评判提示词。',
        statusText: grpoPrompts.length > 0
          ? `已生成 ${grpoPrompts.length} 条评判提示词`
          : '尚未生成；需要先有质量评估结果或已设定打分档次。',
        actionLabel: '生成 GRPO 提示词',
        run: async () => {
          const id = requireDataset('生成 GRPO 提示词')
          if (id === null) return
          await wrap('GRPO 提示词生成', () => consoleApi.generateGrpo(id))
        },
        refresh: async () => {
          if (!activeDatasetId) return Toast.warning('请先选择任务，再刷新 GRPO 提示词')
          await loadCapabilityData(activeDatasetId)
        },
      },
      {
        key: 'sft',
        label: 'L5 SFT 思维链与答案',
        badge: sftRecords.length > 0 ? `${sftRecords.length} 条` : '未生成',
        badgeColor: sftRecords.length > 0 ? 'green' : 'grey',
        description: '为每个问题单独生成对应的思维链与答案（SFT 分支，与 GRPO 分支互斥）。',
        statusText: sftRecords.length > 0
          ? `已生成 ${sftRecords.length} 条 SFT 记录`
          : '尚未生成；需要先生成问题。',
        actionLabel: '生成 SFT 记录',
        run: async () => {
          const id = requireDataset('生成 SFT 记录')
          if (id === null) return
          await wrap('SFT 记录生成', () => consoleApi.generateSft(id))
        },
        refresh: async () => {
          if (!activeDatasetId) return Toast.warning('请先选择任务，再刷新 SFT 记录')
          await loadCapabilityData(activeDatasetId)
        },
      },
      {
        key: 'export-formats',
        label: 'L6 导出格式与字段映射',
        badge: exportFormats?.formats?.length
          ? `${exportFormats.formats.length} 种格式`
          : exportMappings.length > 0 ? `${exportMappings.length} 个映射` : '无数据',
        badgeColor: (exportFormats?.formats?.length || exportMappings.length > 0 ? 'green' : 'grey') as 'green' | 'grey',
        description: '查看支持的导出格式与字段映射配置，决定导出时用哪套字段；可直接发起一次真实导出。',
        statusText: exportFormats?.formats?.length
          ? `可用格式：${exportFormats.formats.slice(0, 8).join('、')}；已配置 ${exportMappings.length} 套字段映射`
          : '尚无格式/字段映射；需要先在管理后台配置。',
        actionLabel: '按默认格式导出',
        run: async () => {
          const id = requireDataset('发起导出')
          if (id === null) return
          setActionLoading(true)
          try {
            // 不传 format（空串）时后端委派给 legacy enqueueExport：
            // 它带奖励完整性校验，worker 侧回退到 legacy 导出，字段集与旧调用方完全一致。
            // 因此「导出未完成质量评估」会得到 409 —— 这是正确行为，不是缺陷。
            await consoleApi.exportDataset(id, '')
            Toast.success('导出任务已触发')
            if (activeDatasetId) await loadCapabilityData(activeDatasetId)
          } catch (error) {
            if (!handleRequestError(error)) Toast.error((error as Error).message)
          } finally {
            setActionLoading(false)
          }
        },
        refresh: async () => {
          if (!activeDatasetId) return Toast.warning('请先选择任务，再刷新导出格式与映射')
          await loadCapabilityData(activeDatasetId)
        },
      },
      {
        key: 'eval-judges',
        label: 'R1 评估裁判配置',
        badge: '多 LLM 互评',
        badgeColor: 'blue',
        description: '查看本数据集可用的评估裁判：生成者会被自动剔除，避免自评。',
        statusText: '剔除规则：生成者模型及其同源模型禁止自评；至少需要 2 个非生成者裁判才能互评。',
        actionLabel: '查看可用裁判',
        run: async () => {
          const id = requireDataset('查看评估裁判')
          if (id === null) return
          setActionLoading(true)
          try {
            // 必须用**带数据集上下文**的接口：/admin/eval/judges 没有数据集上下文，
            // excluded 恒为 false，用它判断剔除会把生成者误当成可用裁判。
            const options = await consoleApi.listDatasetEvalJudges(id)
            const usable = options.judges.filter((judge) => !judge.excluded)
            const excluded = options.judges.filter((judge) => judge.excluded)
            if (usable.length === 0) {
              Toast.warning(`没有可用裁判（生成者 provider=${options.generatorProviderId}）：${excluded.map((item) => item.excludeReason).filter(Boolean).join('；') || '全部被剔除'}`)
            } else {
              Toast.success(`可用裁判 ${usable.length} 个（已剔除 ${excluded.length} 个，生成者 provider=${options.generatorProviderId}）`)
            }
          } catch (error) {
            if (!handleRequestError(error)) Toast.error((error as Error).message)
          } finally {
            setActionLoading(false)
          }
        },
        refresh: async () => {
          if (!activeDatasetId) return Toast.warning('请先选择任务，再刷新评估裁判')
          await loadCapabilityData(activeDatasetId)
        },
      },
    ]
  })()

  const renderTaskDetail = () => {
    const queueDepth = runtime?.queueDepth ?? 0
    const datasetStatus = activeDataset?.status ?? 'draft'

    if (!activeDataset) {
      return (
        <div className="console-page-shell">
          <PageHeader
            badge="我的任务 / 任务详情"
            title="未找到任务详情"
            description="先从「我的任务」进入具体任务，或先创建任务。"
            actions={<Button onClick={() => navigate('/console/tasks')}>返回我的任务</Button>}
          />
          <EmptyCard title="暂无任务详情" description="先从「我的任务」进入具体任务。" />
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
        summary: activeDataset.status === 'draft' ? '先确认主题结构。' : '主题结构已确认。',
        count: graph?.domains.length ?? 0,
      },
      {
        key: 'questions',
        label: '第 2 步：问题生成',
        route: '/console/questions',
        state: questionsStage?.state ?? inferStageState('questions'),
        summary: questionsStage?.summary ?? (datasetStatus === 'domains_confirmed' ? '主题结构已确认，下一步生成问题。' : datasetStatus === 'questions_generated' ? '问题已生成，可继续答案内容。' : '查看问题结果与覆盖情况。'),
        count: questionsStage?.count ?? questions.length,
      },
      {
        key: 'reasoning',
        label: '第 3 步：答案内容',
        route: '/console/reasoning',
        state: reasoningStage?.state ?? inferStageState('reasoning'),
        summary: reasoningStage?.summary ?? (datasetStatus === 'questions_generated' ? '问题已准备好，下一步生成答案内容。' : datasetStatus === 'reasoning_generated' ? '答案已生成，可继续质量评估。' : '查看答案内容是否完整可用。'),
        count: reasoningStage?.count ?? reasoning.length,
      },
      {
        key: 'rewards',
        label: '第 4 步：质量评估',
        route: '/console/rewards',
        state: rewardsStage?.state ?? inferStageState('rewards'),
        summary: rewardsStage?.summary ?? (datasetStatus === 'reasoning_generated' ? '答案已准备好，下一步做质量评估。' : datasetStatus === 'rewards_generated' ? '质量评估已完成，可进入导出交付。' : '查看质量评估结果与风险项。'),
        count: rewardsStage?.count ?? rewards.length,
      },
      {
        key: 'export',
        label: '第 5 步：导出交付',
        route: '/console/exports',
        state: exportStage?.state ?? inferStageState('export'),
        summary: exportDeliveryPending
          ? '导出计算已完成，交付文件正在落盘。'
          : exportStage?.summary ?? (datasetStatus === 'rewards_generated' ? '质量评估已完成，下一步导出交付文件。' : datasetStatus === 'export_generated' ? '导出交付已完成，可下载文件。' : '查看导出结果和交付文件。'),
        count: exportStage?.count ?? artifacts.length,
      },
    ]

    const progressValue = activePipeline ? activePipeline.completionPercent : progressPercent(activeDataset.status)
    const currentStageLabel = exportDeliveryPending ? '导出收尾中' : statusLabel(activeDataset.status)
    const warningQuestionCount = questions.filter((item) => item.status !== 'generated').length
    const missingReasoningCount = reasoning.filter((item) => !item.reasoning.trim()).length
    const lowRewardCount = rewards.filter((item) => item.score < 0.5).length
    const deliveryArtifactCount = artifacts.filter((item) => artifactUsageCategory(item) === 'delivery').length

    return (
      <div className="console-page-shell">
        <PageHeader
          badge={`我的任务 / 任务 #${activeDataset.id}`}
          title={activeDataset.name}
          description={`主题：${activeDataset.rootKeyword} · ${currentStageLabel} · 进度 ${progressValue}%`}
          actions={
            <>
              <Button onClick={() => navigate('/console/tasks')}>返回列表</Button>
              <Button icon={<RefreshCw size={16} />} loading={workspaceLoading} onClick={() => void loadDatasetWorkspace(activeDataset.id, '任务已刷新')}>刷新</Button>
              <Button theme="solid" type="primary" onClick={() => navigate(statusToActionRoute(activeDataset.status))}>{nextActionLabel(activeDataset.status)}</Button>
            </>
          }
        />

        <Card className="console-panel" bodyStyle={{ padding: 20 }}>
          <div className="flex flex-wrap items-center gap-2">
            <Tag color="blue">{currentStageLabel}</Tag>
            <Tag color="cyan">进度 {progressValue}%</Tag>
            {queueDepth > 0 ? <Tag color="orange">排队 {queueDepth}</Tag> : null}
            <Tag color="grey">ETA: {activeEta}</Tag>
            <Tag color="grey">更新 {formatTime(activeDataset.updatedAt)}</Tag>
          </div>
          <Progress percent={progressValue} showInfo={false} stroke="#3b82f6" className="mt-4" />
          <Text className="mt-3 block console-caption">{waitingReasonLabel(activeDataset.status, queueDepth)}</Text>
        </Card>

        <div className="console-card-grid-2">
          {stageCards.map((stage) => {
            const style = stageStateStyle(stage.state)
            return (
              <div
                key={stage.key}
                className="console-panel"
                style={{ cursor: 'pointer', borderRadius: 20, border: '1px solid rgba(var(--semi-grey-2), 0.12)', background: 'color-mix(in srgb, var(--semi-color-bg-1) 86%, white 14%)' }}
                onClick={() => navigate(stage.route)}
              >
                <div style={{ padding: 18 }}>
                  <div className="flex items-center justify-between gap-3">
                    <Text strong>{stage.label}</Text>
                    <Tag color={style.color}>{style.label}</Tag>
                  </div>
                  <Progress percent={style.percent} showInfo={false} stroke="#3b82f6" className="mt-3" />
                  <Text className="mt-3 block console-caption">{stage.summary}</Text>
                  <Text className="mt-1 block console-caption">{stage.count} 条记录 · 点击进入</Text>
                </div>
              </div>
            )
          })}
        </div>

        <Card className="console-panel" bodyStyle={{ padding: 20 }}>
          <Title heading={5} className="!mb-0">进阶能力</Title>
          <Text className="mt-2 block console-caption">
            这些后端能力已实现，但不在 5 个主阶段的线性流程里，需要按需单独触发。
            每一项都会发起真实请求，不是占位入口。
          </Text>
          <div className="console-card-grid-2 mt-4">
            {capabilityActions.map((capability) => (
              <div key={capability.key} className="console-panel" style={{ borderRadius: 16, padding: 16 }}>
                <div className="flex items-center justify-between gap-3">
                  <Text strong>{capability.label}</Text>
                  <Tag color={capability.badgeColor ?? 'blue'}>{capability.badge}</Tag>
                </div>
                <Text className="mt-2 block console-caption">{capability.description}</Text>
                <Text className="mt-1 block console-caption">{capability.statusText}</Text>
                <Space className="mt-3" wrap>
                  <Button
                    size="small"
                    theme="solid"
                    type="primary"
                    loading={actionLoading}
                    onClick={() => void capability.run()}
                  >
                    {capability.actionLabel}
                  </Button>
                  <Button size="small" theme="light" loading={workspaceLoading} onClick={() => void capability.refresh()}>
                    刷新
                  </Button>
                </Space>
              </div>
            ))}
          </div>
        </Card>

        <Card className="console-panel" bodyStyle={{ padding: 20 }}>
          <Title heading={5} className="!mb-0">质量与交付</Title>
          <div className="console-card-grid-2 mt-4">
            <div className="console-summary-grid">
              <div className="console-summary-row">
                <span>问题</span>
                <Space>
                  <Tag color={warningQuestionCount > 0 ? 'orange' : 'green'}>{warningQuestionCount > 0 ? `${warningQuestionCount} 待关注` : '正常'}</Tag>
                  <Button size="small" onClick={() => navigate('/console/questions')}>查看</Button>
                </Space>
              </div>
              <div className="console-summary-row">
                <span>答案</span>
                <Space>
                  <Tag color={missingReasoningCount > 0 ? 'orange' : 'green'}>{missingReasoningCount > 0 ? `${missingReasoningCount} 缺失` : '完整'}</Tag>
                  <Button size="small" onClick={() => navigate('/console/reasoning')}>查看</Button>
                </Space>
              </div>
            </div>
            <div className="console-summary-grid">
              <div className="console-summary-row">
                <span>评分</span>
                <Space>
                  <Tag color={lowRewardCount > 0 ? 'red' : 'green'}>{lowRewardCount > 0 ? `${lowRewardCount} 低分` : '稳定'}</Tag>
                  <Button size="small" onClick={() => navigate('/console/rewards')}>查看</Button>
                </Space>
              </div>
              <div className="console-summary-row">
                <span>交付包</span>
                <Space>
                  <Tag color={deliveryArtifactCount > 0 ? 'green' : 'grey'}>{deliveryArtifactCount > 0 ? `${deliveryArtifactCount} 个` : '待生成'}</Tag>
                  <Button size="small" onClick={() => navigate('/console/exports')}>查看</Button>
                </Space>
              </div>
            </div>
          </div>
          <Space className="mt-4" wrap>
            <Button onClick={() => void loadDatasetWorkspace(activeDataset.id, '任务状态已刷新')}>刷新状态</Button>
            <Button onClick={() => navigate('/console/help')}>恢复指引</Button>
            {deliveryArtifactCount > 0 ? <Button theme="solid" type="primary" onClick={() => navigate('/console/exports')}>进入导出中心</Button> : null}
          </Space>
        </Card>
      </div>
    )
  }

  const renderPlanning = () => (
    <div className="console-page-shell">
      <PageHeader
        badge="新建任务"
        title="先填主题和目标规模，立即创建新任务"
        description="只填任务主题和目标规模即可。"
        actions={
          <>
            <Button onClick={() => navigate('/console/tasks')}>返回我的任务</Button>
            <Button icon={<RefreshCw size={16} />} loading={bootstrapLoading} onClick={() => void loadBootstrap('新建任务页已刷新')}>刷新</Button>
          </>
        }
      />

      <div className="console-card-grid-2">
        <Card className="console-panel" bodyStyle={{ padding: 20 }}>
          <Title heading={4} className="!mb-0">任务基础信息</Title>
          <Text className="mt-2 block console-caption">先填任务主题和目标规模，再决定是否估算或创建。</Text>
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
              <Text className="mt-2 block console-caption">先小规模验证，再逐步放大。</Text>
            </div>
            <div>
              <Text className="mb-2 block font-medium">领域数 n</Text>
              <InputNumber value={plannerForm.domainCount} onChange={(value) => setPlannerForm((current) => ({ ...current, domainCount: Number(value ?? 0) }))} min={0} max={200} style={{ width: '100%' }} placeholder="留空用策略默认值" />
              <Text className="mt-2 block console-caption">关键词下要生成多少个领域；留空（0）表示沿用生成策略里的领域数。</Text>
            </div>
            <div>
              <Text className="mb-2 block font-medium">每领域方向数 m</Text>
              <InputNumber value={plannerForm.directionCount} onChange={(value) => setPlannerForm((current) => ({ ...current, directionCount: Number(value ?? 0) }))} min={0} max={50} style={{ width: '100%' }} placeholder="默认 3" />
              <Text className="mt-2 block console-caption">每个领域下再生成多少个具体方向；n × m 就是方向总条目数。留空（0）用默认值 3。</Text>
            </div>
            <div>
              <Text className="mb-2 block font-medium">每方向问题数 x</Text>
              <InputNumber value={plannerForm.questionsPerDirection} onChange={(value) => setPlannerForm((current) => ({ ...current, questionsPerDirection: Number(value ?? 0) }))} min={0} max={50} style={{ width: '100%' }} placeholder="默认 5" />
              <Text className="mt-2 block console-caption">每个方向生成多少道具体问题；n × m × x 就是问题总量。留空（0）用默认值 5。</Text>
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
          {activeStorageProfiles.length === 0 ? (
            <Banner
              type="warning"
              className="mt-4"
              closeIcon={null}
              title="尚未配置结果存储，现在创建的任务会在「答案生成」阶段失败"
              description={
                <Space vertical align="start" spacing="tight">
                  <Text>
                    答案、质量评分与导出三个阶段都需要把结果写入对象存储。
                    当前系统里没有任何可用配置，请先到「系统设置 → 结果存储」新增一条并设为可用，
                    然后刷新本页再创建任务。
                  </Text>
                  {isAdmin ? (
                    <Button size="small" theme="solid" type="primary" onClick={() => navigate('/console/admin/storage')}>
                      去配置结果存储
                    </Button>
                  ) : (
                    <Text className="console-caption">需要管理员权限：请联系管理员完成配置。</Text>
                  )}
                </Space>
              }
            />
          ) : null}
          <Space className="mt-6" spacing="medium">
            <Button icon={<Target size={16} />} loading={actionLoading} onClick={() => void estimatePlan()}>估算规模</Button>
            <Button theme="solid" type="primary" icon={<FileOutput size={16} />} loading={actionLoading} onClick={() => void createDataset()}>创建任务</Button>
          </Space>
        </Card>

        <Card className="console-panel" bodyStyle={{ padding: 20 }}>
          <Title heading={4} className="!mb-0">估算结果</Title>
            <Text className="mt-2 block console-caption">看方向数和题目量，再决定是否创建。</Text>
          {planningCards.length > 0 ? (
            <div className="console-card-grid-2 mt-5">
              {planningCards.map((item) => <StatCard key={item.label} {...item} />)}
            </div>
          ) : (
            <EmptyCard title="尚未生成估算" description="填写参数后点击「估算规模」。" />
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
      pendingDomains.length > 0 ? `还有 ${pendingDomains.length} 个方向未标记为「已确认」。` : null,
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
          badge="我的任务 / 主题结构"
          title="生成主题结构并完成确认"
          description="先看结构，再复核命名，最后确认"
          actions={
            <>
              <Button onClick={() => navigate(activeTaskDetailRoute)}>返回当前任务</Button>
              <Button onClick={() => navigate('/console/tasks')}>返回我的任务</Button>
              <Button icon={<RefreshCw size={16} />} loading={workspaceLoading} onClick={() => withActiveDataset(activeDatasetId, notifyNoActiveTask, (id) => loadDatasetWorkspace(id, '方向结构已刷新'))}>刷新结构</Button>
              <Button theme="solid" type="primary" icon={<GitBranch size={16} />} loading={workspaceLoading} onClick={() => void generateDomains()}>生成方向结构</Button>
            </>
          }
        />

        <div className="console-card-grid-2">
          <Card className="console-panel" bodyStyle={{ padding: 20 }}>
            <Title heading={4} className="!mb-0">方向结构预览</Title>
            <Text className="mt-2 block console-caption">先确认覆盖，再批量校对命名。</Text>
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
                  <Banner type="success" icon={<ShieldCheck size={16} />} description="当前结构无明显异常，可继续确认。" />
                )}
                <DirectionStructurePreview rootKeyword={graph.dataset.rootKeyword} domains={graph.domains} />
                <Card className="console-toolbar-card" bodyStyle={{ padding: 16 }}>
                  <div className="flex flex-col gap-3 lg:flex-row lg:items-center lg:justify-between">
                    <div>
                      <Text strong>高级模式</Text>
                      <Text className="mt-1 block console-caption">排查复杂结构时再展开图形关系。</Text>
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
                      <Text className="mt-4 block console-caption">没有额外图形关系，保留树状结构即可。</Text>
                    )
                  ) : null}
                </Card>
                <Space wrap>
                  <Button onClick={() => markAllDomains('approved')}>批量确认</Button>
                  <Button onClick={() => markAllDomains('pending')}>批量待复核</Button>
                  <Button icon={<RefreshCw size={16} />} loading={workspaceLoading} onClick={() => void saveGraph()}>保存</Button>
                  <Button theme="solid" type="primary" loading={workspaceLoading} onClick={() => void confirmDomains()}>确认结构</Button>
                </Space>
              </div>
            ) : (
              <EmptyCard title="尚未生成方向结构" description="先创建任务，再生成方向结构。" />
            )}
          </Card>

          <Card className="console-focus-card" bodyStyle={{ padding: 20 }}>
            <Title heading={4} className="!mb-0">当前任务上下文</Title>
            <Text className="mt-2 block console-caption">持续显示任务状态和复核压力。</Text>
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
          <Text className="mt-2 block console-caption">按列表修订名称并保存，再确认方向结构。</Text>
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
            <EmptyCard title="暂无方向列表" description="生成方向结构后再编辑。" />
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
            {activeDataset ? <Button onClick={() => navigate('/console/tasks')}>返回我的任务</Button> : null}
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
          <Title heading={4} className="!mb-0">阶段结果预览</Title>
            <Text className="mt-2 block console-caption">共 {records.length} 条结果，优先展示关键信息。</Text>
          {records.length > 0 ? <div className="console-record-list mt-5">{records.map(renderRecord)}</div> : <EmptyCard title={emptyTitle} description={emptyDescription} />}
        </Card>
        <Card className="console-focus-card" bodyStyle={{ padding: 20 }}>
          <Title heading={4} className="!mb-0">{summaryTitle}</Title>
          <div className="mt-5 console-summary-grid">
            <div className="console-summary-row"><span>当前任务</span><Text strong>{activeDataset?.name ?? '未选择'}</Text></div>
            <div className="console-summary-row"><span>当前阶段</span><Text strong>{activeDataset ? (exportDeliveryPending ? '导出收尾中' : statusLabel(activeDataset.status)) : '未开始'}</Text></div>
            <div className="console-summary-row"><span>阶段结果数</span><Text strong>{records.length}</Text></div>
            <div className="console-summary-row"><span>等待任务数</span><Text strong>{runtime?.queueDepth ?? 0}</Text></div>
            <div className="console-summary-row"><span>等待状态</span><Text strong>{activeDataset ? (exportDeliveryPending ? '导出状态已完成，正在确认交付文件，请稍后刷新' : waitingStateLabel(activeDataset.status, runtime?.queueDepth ?? 0)) : '请先创建任务'}</Text></div>
            <div className="console-summary-row"><span>等待原因</span><Text strong>{activeDataset ? (exportDeliveryPending ? '导出结果正在写入交付存储。' : waitingReasonLabel(activeDataset.status, runtime?.queueDepth ?? 0)) : '创建任务后显示'}</Text></div>
            <div className="console-summary-row"><span>建议操作</span><Text strong>{activeDataset ? (exportDeliveryPending ? '等待交付文件落盘后再下载。' : waitingActionLabel(activeDataset.status)) : '创建任务后显示'}</Text></div>
            <div className="console-summary-row"><span>当前阶段 ETA</span><Text strong>{activeEta}</Text></div>
            <div className="console-summary-row"><span>刷新建议</span><Text strong>{activeDataset ? (exportDeliveryPending ? '系统会自动刷新，检测到交付文件后可下载。' : refreshExpectationLabel(activeDataset.status, runtime?.queueDepth ?? 0)) : '创建任务后显示'}</Text></div>
          </div>
          <Card className="console-toolbar-card mt-4" bodyStyle={{ padding: 16 }}>
            <Text strong>异常说明</Text>
            <Text className="mt-2 block console-caption">{exceptionHint}</Text>
          </Card>
          <Card className="console-toolbar-card mt-4" bodyStyle={{ padding: 16 }}>
            <Text strong>下一步建议</Text>
            <div className="console-next-step-list mt-3">
              {nextStepTips.slice(0, 3).map((tip) => <Text key={tip} className="console-caption">• {tip}</Text>)}
            </div>
          </Card>
        </Card>
      </div>
    </div>
  )

  const renderQuestionStage = () => (
    renderRecordPage({ badge: '结果中心 / 题目结果', title: '题目生成结果中心', description: '查看题目生成质量、异常状态，并决定是否进入答案生成。', actionLabel: '开始生成题目', onGenerate: generateQuestions, onRefresh: async () => { withActiveDataset(activeDatasetId, notifyNoActiveTask, (id) => loadDatasetWorkspace(id, '问题结果已刷新')) }, records: questions, emptyTitle: '尚未生成题目', emptyDescription: '请先确认主题，再开始生成题目。', summaryTitle: '题目阶段摘要', summaryCards: [{ icon: Layers3, label: '题目总数', value: questions.length, helper: '当前可用于后续步骤的题目数量' }, { icon: ShieldCheck, label: '状态正常', value: questions.filter((item) => item.status === 'generated').length, helper: '状态为“已生成”的题目数量' }, { icon: Bell, label: '待关注', value: questions.filter((item) => item.status !== 'generated').length, helper: '状态异常或处理中，建议优先复查' }], nextStepTips: ['优先复核“待关注”题目，确认是否需要重跑。', '抽检不同方向题目，避免主题覆盖不均。', '确认题目质量后再进入答案生成。'], exceptionHint: '若状态长时间停留在“处理中/排队中”，通常是等待任务较多或上游任务未完成，先刷新并查看等待任务数。', renderRecord: (record: Question) => { const state = questionStatusLabel(record.status); return <div key={record.id} className="console-record-item"><div className="flex items-center justify-between gap-3"><Space><Tag color="blue">{record.domainName}</Tag><Tag color={state.color}>{state.text}</Tag></Space><Text className="console-caption">{formatTime(record.createdAt)}</Text></div><Text className="mt-3 block">{record.content}</Text></div> } })
  )

  const renderReasoningStage = () => (
    renderRecordPage({ badge: '结果中心 / 答案结果', title: '答案与思路结果中心', description: '聚焦答案摘要质量，而非底层对象字段。', actionLabel: '开始生成答案', onGenerate: generateReasoning, onRefresh: async () => { withActiveDataset(activeDatasetId, notifyNoActiveTask, (id) => loadDatasetWorkspace(id, '推理结果已刷新')) }, records: reasoning, emptyTitle: '尚未生成答案', emptyDescription: '请先完成题目生成，再开始生成答案。', summaryTitle: '答案阶段摘要', summaryCards: [{ icon: BrainCircuit, label: '答案总数', value: reasoning.length, helper: '已返回的答案与思路记录' }, { icon: ShieldCheck, label: '完整摘要', value: reasoning.filter((item) => reasoningQualityLabel(item.answerSummary).text === '完整').length, helper: '摘要信息完整，可直接进入评估' }, { icon: Bell, label: '待补充', value: reasoning.filter((item) => reasoningQualityLabel(item.answerSummary).text === '待补充').length, helper: '摘要过短，建议重试或人工复核' }], nextStepTips: ['先处理“待补充”答案，再批量进入质量评估。', '检查答案是否覆盖题目核心要点。', '确认摘要稳定后再触发奖励评估。'], exceptionHint: '若摘要内容明显过短或重复，通常是模型输出被截断或输入上下文不足，建议重跑该批次。', renderRecord: (record: ReasoningRecord) => { const quality = reasoningQualityLabel(record.answerSummary); return <div key={record.id} className="console-record-item"><div className="flex items-center justify-between gap-3"><Space><Tag color="cyan">答案摘要</Tag><Tag color={quality.color}>{quality.text}</Tag></Space><Text className="console-caption">{formatTime(record.createdAt)}</Text></div><Text className="mt-3 block">{record.answerSummary}</Text><Text className="mt-2 block console-caption">{quality.note}</Text><Text className="mt-2 block console-caption">题目：{record.questionText}</Text></div> } })
  )

  const renderRewardStage = () => (
    renderRecordPage({ badge: '结果中心 / 质量评估', title: '质量评分结果中心', description: '展示评分等级、风险提示与建议动作，支持快速决策。', actionLabel: '开始质量评估', onGenerate: generateRewards, onRefresh: async () => { withActiveDataset(activeDatasetId, notifyNoActiveTask, (id) => loadDatasetWorkspace(id, '奖励结果已刷新')) }, records: rewards, emptyTitle: '尚未生成质量评估', emptyDescription: '先完成答案生成，再触发质量评估。', summaryTitle: '评估阶段摘要', summaryCards: [{ icon: ShieldCheck, label: '评分记录', value: rewards.length, helper: '已生成的质量评分条目' }, { icon: Sparkles, label: '高质量', value: rewards.filter((item) => item.score >= 0.85).length, helper: '可直接进入导出候选' }, { icon: Bell, label: '风险项', value: rewards.filter((item) => item.score < 0.5).length, helper: '建议先回修再继续流程' }], nextStepTips: ['优先处理“风险”与“待优化”记录。', '对“可交付”记录执行抽样复核。', '高质量样本可直接推进导出。'], exceptionHint: '若低分记录突然增多，通常意味着上游答案质量波动，建议回看答案阶段并抽样检查。', renderRecord: (record: RewardRecord) => { const quality = rewardQualityLabel(record.score); return <div key={record.id} className="console-record-item"><div className="flex items-center justify-between gap-3"><Space><Tag color={quality.color}>{quality.text}</Tag><Tag color="green">评分 {record.score.toFixed(2)}</Tag></Space><Text className="console-caption">{formatTime(record.createdAt)}</Text></div><Text className="mt-3 block">{record.questionText}</Text><Text className="mt-2 block console-caption">{quality.note}</Text></div> } })
  )

  const renderExportStage = () => (
    renderRecordPage({
                          badge: '结果中心 / 导出交付',
                          title: '导出结果中心',
                          description: '展示交付用途、来源版本与下载建议，帮助快速决定交付动作。',
                          actionLabel: '开始导出结果',
                          onGenerate: generateExport,
                          onRefresh: async () => {
                            withActiveDataset(activeDatasetId, notifyNoActiveTask, (id) => loadDatasetWorkspace(id, '导出结果已刷新'))
                          },
                          records: filteredArtifacts,
                          emptyTitle: '尚未生成导出结果',
                          emptyDescription: '完成质量评估后，即可触发导出。',
                          summaryTitle: '导出阶段摘要',
                          summaryCards: [
                            {
                              icon: HardDriveDownload,
                              label: '可见导出文件',
                              value: filteredArtifacts.length,
                              helper: '按当前筛选展示的可处理文件数量',
                            },
                            {
                              icon: FileOutput,
                              label: '交付优先',
                              value: artifacts.filter((item) => artifactUsageCategory(item) === 'delivery').length,
                              helper: '推荐优先下载并交付下游的标准文件',
                            },
                            {
                              icon: Bell,
                              label: '需先确认',
                              value: artifacts.filter((item) => artifactUsageCategory(item) !== 'delivery').length,
                              helper: '建议先做复核或兼容性确认',
                            },
                          ],
                          nextStepTips: [
                            '先看“来源与版本”确认是否为当前任务的最新导出。',
                            '根据“交付说明”判断文件用于正式交付还是内部复核。',
                            '下载前按“下载建议”确认优先级，避免误发非目标格式。',
                            '交付后同步文件标识，并要求下游反馈验收结果。',
                          ],
                          exceptionHint:
                            '若导出后列表为空，多数是任务仍在队列中或上游质量评估未完成；若多次刷新仍为空，请先回到质量评分页确认已完成。',
                          renderRecord: (record: Artifact) => {
                            const usageCategory = artifactUsageCategory(record)
                            return (
                              <div key={record.id} className="console-record-item">
                                <div className="flex items-center justify-between gap-3">
                                  <Space>
                                    <Tag color="violet">{artifactLabel(record.artifactType)}</Tag>
                                    <Tag color={usageCategory === 'delivery' ? 'green' : usageCategory === 'review' ? 'blue' : 'grey'}>{artifactUsageLabel(usageCategory)}</Tag>
                                  </Space>
                                  <Text className="console-caption">{formatTime(record.createdAt)}</Text>
                                </div>
                                <Text className="mt-3 block">{artifactDisplayName(record.objectKey)}</Text>
                                <Text className="mt-2 block console-caption">交付格式：{artifactContentTypeLabel(record.contentType)}</Text>
                                <Text className="mt-1 block console-caption">来源与版本：{artifactSourceVersionHint(record)}</Text>
                                <Text className="mt-1 block console-caption">格式说明：{artifactContentTypeHint(record.contentType)}</Text>
                                <Text className="mt-1 block console-caption">交付说明：{artifactDeliveryNote(record)}</Text>
                                <Text className="mt-1 block console-caption">下载建议：{artifactDownloadDecisionHint(record)}</Text>
                                <div className="mt-3 flex flex-wrap gap-2">
                                  <Button size="small" theme={exportFilter === 'all' ? 'solid' : 'light'} onClick={() => setExportFilter('all')}>查看全部</Button>
                                  <Button size="small" theme={exportFilter === 'delivery' ? 'solid' : 'light'} onClick={() => setExportFilter('delivery')}>交付优先</Button>
                                  <Button size="small" theme={exportFilter === 'review' ? 'solid' : 'light'} onClick={() => setExportFilter('review')}>复核资料</Button>
                                  <Button size="small" theme={exportFilter === 'other' ? 'solid' : 'light'} onClick={() => setExportFilter('other')}>其他格式</Button>
                                </div>
                                <div className="mt-3 flex flex-wrap gap-2">
                                  <Button size="small" theme="solid" type="primary" onClick={() => downloadArtifact(record)}>下载结果</Button>
                                  <Button size="small" theme="light" onClick={() => void copyArtifactKey(record.objectKey)}>复制交付标识</Button>
                                </div>
                              </div>
                            )
                          },
                        })
  )

  const renderResultsHub = () => {
    // 交付件（issue #104）：本页要能直接下载，所以在这里先筛出「用户主动导出的成品」。
    // 口径与导出页一致 —— 复用同一个 artifactUsageCategory，不另立一份判定。
    const deliveryArtifacts = artifacts.filter((item) => artifactUsageCategory(item) === 'delivery')
    return (
    <div className="console-page-shell">
      <PageHeader
        badge="数据资产"
        title="查看结果与交付文件"
        description="只看结果、评分和交付文件"
        actions={
          <>
            <Button onClick={() => navigate(activeTaskDetailRoute)}>返回当前任务</Button>
            <Button onClick={() => navigate('/console/tasks')}>返回我的任务</Button>
            <Button icon={<RefreshCw size={16} />} loading={workspaceLoading} onClick={() => withActiveDataset(activeDatasetId, notifyNoActiveTask, (id) => loadDatasetWorkspace(id, '数据资产页已刷新'))}>刷新结果</Button>
          </>
        }
      />

      <div className="console-card-grid-4">
        <StatCard icon={Layers3} label="题目结果" value={questions.length} helper="问题数量" />
        <StatCard icon={BrainCircuit} label="答案结果" value={reasoning.length} helper="答案数量" />
        <StatCard icon={ShieldCheck} label="质量评分" value={rewards.length} helper="评分数量" />
        <StatCard icon={HardDriveDownload} label="导出文件" value={artifacts.length} helper="交付文件" />
      </div>

      <div className="console-card-grid-2">
        <Card className="console-panel" bodyStyle={{ padding: 20 }}>
          <Title heading={4} className="!mb-0">数据资产总览</Title>
          <Text className="mt-2 block console-caption">结果、评分与交付。</Text>
          <div className="mt-5 console-summary-grid">
            <div className="console-summary-row"><span>当前任务</span><Text strong>{activeDataset?.name ?? '未选择'}</Text></div>
            <div className="console-summary-row"><span>题目结果</span><Text strong>{questions.length}</Text></div>
            <div className="console-summary-row"><span>答案结果</span><Text strong>{reasoning.length}</Text></div>
            <div className="console-summary-row"><span>质量评估</span><Text strong>{rewards.length}</Text></div>
            <div className="console-summary-row"><span>导出文件</span><Text strong>{artifacts.length}</Text></div>
            <div className="console-summary-row"><span>建议动作</span><Text strong>{activeDataset ? nextActionLabel(activeDataset.status) : '先前往新建任务创建任务'}</Text></div>
          </div>
        </Card>

        <Card className="console-focus-card" bodyStyle={{ padding: 20 }}>
          <Title heading={4} className="!mb-0">交付与复核入口</Title>
          <Text className="mt-2 block console-caption">总览与交付入口</Text>
          <div className="mt-5 console-summary-grid">
            <div className="console-summary-row"><span>问题生成</span><Text strong>{questions.length > 0 ? '可进入查看覆盖情况' : '暂无结果'}</Text></div>
            <div className="console-summary-row"><span>答案内容</span><Text strong>{reasoning.length > 0 ? '可进入查看完整性' : '暂无结果'}</Text></div>
            <div className="console-summary-row"><span>质量评估</span><Text strong>{rewards.length > 0 ? '可进入查看评分状态' : '暂无结果'}</Text></div>
            <div className="console-summary-row"><span>导出交付</span><Text strong>{artifacts.length > 0 ? '已有交付文件可下载' : '等待生成交付文件'}</Text></div>
          </div>
          <Space className="mt-5" wrap>
            <Button theme="solid" type="primary" onClick={() => navigate('/console/exports')}>查看导出交付</Button>
            <Button onClick={() => navigate('/console/rewards')}>查看质量评估</Button>
            <Button onClick={() => navigate('/console/reasoning')}>查看答案内容</Button>
          </Space>
        </Card>
      </div>

      {/* issue #104：本页自我定位包含「交付文件」，却曾整页没有任何下载入口 ——
          用户在主入口拿不到自己的交付文件。这里直接列出可下载的交付件，
          并**复用** downloadArtifact（与导出页同一份实现，见 lib/api.ts 的 downloadArtifactBlob），
          不在这里重写一遍 blob 下载逻辑。 */}
      <Card className="console-panel mt-5" bodyStyle={{ padding: 20 }}>
        <div className="flex items-center justify-between gap-3 flex-wrap">
          <div>
            <Title heading={4} className="!mb-0">交付文件下载</Title>
            <Text className="mt-2 block console-caption">
              交付件是用户主动导出的成品（与「复核资料」区分），可直接下载。
            </Text>
          </div>
        </div>
        {deliveryArtifacts.length > 0 ? (
          <div className="mt-5 console-summary-grid">
            {deliveryArtifacts.map((artifact) => (
              <div key={artifact.id} className="console-summary-row">
                <span>
                  <Tag color="violet">{artifactLabel(artifact.artifactType)}</Tag>
                  <Text className="ml-2">{artifactDisplayName(artifact.objectKey)}</Text>
                </span>
                <Space>
                  <Text className="console-caption">{formatTime(artifact.createdAt)}</Text>
                  <Button size="small" theme="solid" type="primary" onClick={() => void downloadArtifact(artifact)}>
                    下载结果
                  </Button>
                </Space>
              </div>
            ))}
          </div>
        ) : (
          <div className="mt-5">
            <EmptyCard
              title="暂无可下载的交付文件"
              description={
                activeDataset
                  ? '本任务还没有交付件。先完成质量评估，再到「导出交付」生成文件。'
                  : '先到「我的任务」选一个任务，或到「新建任务」创建一个。'
              }
            />
            <div className="mt-4">
              <Button onClick={() => navigate(activeDataset ? '/console/exports' : '/console/tasks')}>
                {activeDataset ? '去生成导出交付' : '去选择任务'}
              </Button>
            </div>
          </div>
        )}
      </Card>
    </div>
    )
  }

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
        title="系统运行态"
          description="把运行态信息集中到管理员工作台。"
          actions={
            <>
              <Button icon={<RefreshCw size={16} />} loading={workspaceLoading} onClick={() => void loadBootstrap('运营监控数据已刷新')}>刷新监控</Button>
              <Button theme="solid" type="primary" onClick={() => navigate('/console/admin/audit')}>查看完整审计</Button>
            </>
          }
        />

        <div className="console-card-grid-4">
          <StatCard icon={ServerCog} label="等待任务" value={queueDepth} helper="队列等待" />
          <StatCard icon={Database} label="活跃 AI 服务" value={activeProviders.length} helper="可用服务" />
          <StatCard icon={FolderCog} label="可用存储" value={activeStorageCount} helper="可用存储" />
          <StatCard icon={Settings} label="最近审计数" value={recentAuditLogs.length} helper="最近变更" />
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
            <Text className="mt-2 block console-caption">先看队列、服务和交付状态，再决定是否批量执行。</Text>
            <div className="mt-5 console-summary-grid">
              <div className="console-summary-row"><span>队列压力</span><Text strong>{queueDepth > 8 ? '偏高，先处理已产出资产' : '可继续推进'}</Text></div>
              <div className="console-summary-row"><span>AI 服务</span><Text strong>{activeProviders.length > 0 ? '可用' : '需检查连通性'}</Text></div>
              <div className="console-summary-row"><span>交付状态</span><Text strong>{runtime?.artifactCount ? '已有交付文件' : '优先检查导出与存储'}</Text></div>
              <div className="console-summary-row"><span>异常处理</span><Text strong>先看审计与帮助页，再决定是否重试</Text></div>
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
            <Text className="mt-2 block console-caption">看任务活动与处理。</Text>
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
            <Text className="mt-2 block console-caption">回看近期配置与操作。</Text>
            {recentAuditLogs.length > 0 ? (
              <Table columns={auditColumns} dataSource={recentAuditLogs} pagination={false} />
            ) : (
              <EmptyCard title="暂无操作记录" description="发生变更后再查看。" />
            )}
          </Card>
        </div>
      </div>
    )
  }

  const renderHelp = () => {
    const glossary = [
      { term: '任务状态', description: '表示任务当前所处阶段。' },
      { term: '等待任务数', description: '表示当前排队中的任务数量。' },
      { term: '数据资产', description: '集中查看结果、评分和导出文件。' },
      { term: '恢复建议', description: '当前推荐动作。' },
    ]

    const recoveryChecklist = [
      '先看恢复建议并按按钮跳转。',
      '确认前置结果已生成。',
      '长时间无变化时刷新数据。',
      '权限不足时切换管理员或联系管理员。',
      '登录失效时重新登录后继续。',
    ]

    const highRiskActions = [
      '重跑答案或评分前，先记录当前结论。',
      '导出落盘时不要重复触发。',
      '切换服务或策略前，优先新建任务。',
      '通知下游前，先确认版本与时间。',
    ]

    return (
      <div className="console-page-shell">
        <PageHeader
          badge="账户与帮助"
          title="帮助与术语"
            description="异常时按这里的恢复路径处理"
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
            <Text className="mt-2 block console-caption">按 4 步恢复：看提示、补前置结果、刷新、确认权限或重登。</Text>
            <div className="mt-4 console-next-step-list">
              {recoveryChecklist.map((item) => <Text key={item} className="console-caption">• {item}</Text>)}
            </div>
            <Card className="console-toolbar-card mt-4" bodyStyle={{ padding: 16 }}>
              <Text strong>快速操作</Text>
              <Space className="mt-3" wrap>
                {activeDataset ? <Button onClick={() => navigate(activeTaskDetailRoute)}>返回当前任务</Button> : null}
                <Button onClick={() => navigate('/console/planning')}>去新建任务</Button>
                <Button onClick={() => navigate('/console/results')}>查看数据资产</Button>
                <Button onClick={() => {
                  // 这一处在修复前就有可见反馈（回退刷新控制台数据 + Toast），但文案说的是
                  // 「控制台数据已刷新」，而按钮写的是「刷新当前任务状态」—— 用户以为刷了新任务，
                  // 其实刷的是控制台，属「反馈与意图不符」。
                  // 这里先走统一守卫发出明确提示；未选中任务时保留原有的控制台刷新回退能力，
                  // 但**不再谎报成功**（删掉回退的 successMessage）：
                  // 用户只看到一条真话（当前没任务），数据依旧被刷新，不会出现两条互相矛盾的 Toast。
                  if (!withActiveDataset(activeDatasetId, notifyNoActiveTask, (id) => loadDatasetWorkspace(id, '当前任务状态已刷新'))) {
                    void loadBootstrap()
                  }
                }}>刷新当前任务状态</Button>
              </Space>
            </Card>
          </Card>

          <Card className="console-panel" bodyStyle={{ padding: 20 }}>
            <Title heading={4} className="!mb-0">术语解释</Title>
            <Text className="mt-2 block console-caption">只保留关键术语。</Text>
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

        <Card className="console-panel mt-6" bodyStyle={{ padding: 20 }}>
          <Title heading={4} className="!mb-0">构建版本</Title>
          <Text className="mt-2 block console-caption">
            用于确认「当前页面是哪一版源码构建的」（issue #88：部署镜像曾落后于仓库 HEAD，页面上无法察觉）。
          </Text>
          <div className="mt-4 console-summary-grid">
            <div className="console-summary-row">
              <span>前端版本</span>
              <Tag color={APP_VERSION_UNKNOWN ? 'orange' : 'green'}>
                {APP_VERSION_UNKNOWN ? '未注入' : APP_VERSION_SHORT}
              </Tag>
            </div>
            <div className="console-summary-row">
              <span>构建时间</span>
              <Text className="console-caption">{APP_BUILD_TIME || '未知'}</Text>
            </div>
          </div>
          {APP_VERSION_UNKNOWN ? (
            <Text className="mt-3 block console-caption" style={{ color: 'var(--semi-color-warning)' }}>
              本次构建未传入 GIT_SHA，无法自证与源码的对应关系。请用
              {' '}<Text code>./scripts/check-deployed-version.sh</Text>{' '}
              比对，或在重建时传入构建参数；不要假定它等于当前 HEAD。
            </Text>
          ) : (
            <Text className="mt-3 block console-caption">
              完整校验：{' '}
              <Text code>./scripts/check-deployed-version.sh</Text>{' '}
              会把本页版本与 <Text code>git rev-parse HEAD</Text> 直接对比。
            </Text>
          )}
        </Card>

        <Card className="console-panel mt-6" bodyStyle={{ padding: 20 }}>
          <Title heading={4} className="!mb-0">部署自检</Title>
          <Text className="mt-2 block console-caption">
            若界面行为与当前源码不符，先用下面两条命令确认跑的是哪一版，再排查业务问题。
          </Text>
          <div className="console-next-step-list mt-3">
            <Text className="console-caption">• <Text code>curl -s http://localhost:3210/version.json</Text> —— 看部署中的前端版本</Text>
            <Text className="console-caption">• <Text code>git rev-parse HEAD</Text> —— 看本地源码版本</Text>
            <Text className="console-caption">• 两者不一致 → 执行 <Text code>docker compose up -d --build</Text> 重建，不要继续用旧镜像做验收</Text>
          </div>
        </Card>
      </div>
    )
  }

  const renderProviders = () => (
    <div className="console-page-shell">
      <PageHeader
        badge="系统设置 / AI 服务"
        title="管理 AI 服务"
        description="看服务列表后再编辑、取模型或测试。"
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
            <Text className="mt-2 block console-caption">搜索、编辑、测试或保存</Text>
          </div>
          <Input
            value={providerSearchKeyword}
            onChange={setProviderSearchKeyword}
            placeholder="搜索服务 / 地址 / 模型"
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
              <Input value={providerDraft.model ?? ''} onChange={(value) => setProviderDraft((current) => ({ ...current, model: value }))} placeholder="例如 gpt-4o-mini" />
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
        title="结果存储"
        description="先看列表，再新增或编辑。"
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
            <Text className="mt-2 block console-caption">默认存储、搜索配置，以及新增与编辑。</Text>
          </div>
          <Input
            value={storageSearchKeyword}
            onChange={setStorageSearchKeyword}
            placeholder="搜索名称 / 提供方 / 存储桶"
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
            <Input value={storageDraft.secretAccessKey ?? ''} onChange={(value) => setStorageDraft((current) => ({ ...current, secretAccessKey: value }))} placeholder="留空则沿用当前密钥" mode="password" />
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
        title="生成规则"
        description="支持搜索、新增与编辑。"
        actions={
          <Space wrap>
            <Button icon={<RefreshCw size={16} />} loading={workspaceLoading} onClick={() => void loadBootstrap('规则页已刷新')}>刷新列表</Button>
            <Button theme="solid" type="primary" onClick={() => openCreateStrategyModal()}>新增策略</Button>
          </Space>
        }
      />
      <Card className="console-panel" bodyStyle={{ padding: 20 }}>
        <div className="flex flex-col gap-4 lg:flex-row lg:items-center lg:justify-between">
          <div>
            <Title heading={4} className="!mb-0">生成规则列表</Title>
            <Text className="mt-2 block console-caption">维护规则数量、问题量和模式</Text>
          </div>
          <Input
            value={strategySearchKeyword}
            onChange={setStrategySearchKeyword}
            placeholder="搜索策略 / 模式"
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
            <Input value={strategyDraft.description ?? ''} onChange={(value) => setStrategyDraft((current) => ({ ...current, description: value }))} placeholder="描述适用场景" />
          </div>
          <div className="console-card-grid-2">
            <div>
              <Text className="mb-2 block font-medium">领域数</Text>
              <InputNumber value={strategyDraft.domainCount ?? DEFAULT_STRATEGY_DOMAIN_COUNT} onChange={(value) => setStrategyDraft((current) => ({ ...current, domainCount: Number(value ?? 0) }))} style={{ width: '100%' }} />
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
        title="生成指令"
        description="支持搜索、新增与编辑。"
        actions={
          <Space wrap>
            <Button icon={<RefreshCw size={16} />} loading={workspaceLoading} onClick={() => void loadBootstrap('模板页已刷新')}>刷新列表</Button>
            <Button theme="solid" type="primary" onClick={() => openCreatePromptModal()}>新增模板</Button>
          </Space>
        }
      />
      <Card className="console-panel" bodyStyle={{ padding: 20 }}>
        <div className="flex flex-col gap-4 lg:flex-row lg:items-center lg:justify-between">
          <div>
            <Title heading={4} className="!mb-0">生成指令列表</Title>
            <Text className="mt-2 block console-caption">维护阶段模板与版本</Text>
          </div>
          <Input
            value={promptSearchKeyword}
            onChange={setPromptSearchKeyword}
            placeholder="搜索模板 / 阶段 / 版本"
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
              <Input value={promptDraft.stage} onChange={(value) => setPromptDraft((current) => ({ ...current, stage: value }))} placeholder="例如 question-generation" />
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
            <TextArea rows={6} value={promptDraft.systemPrompt} onChange={(value) => setPromptDraft((current) => ({ ...current, systemPrompt: value }))} placeholder="填写系统指令" />
          </div>
          <div>
            <Text className="mb-2 block font-medium">用户指令</Text>
            <TextArea rows={6} value={promptDraft.userPrompt} onChange={(value) => setPromptDraft((current) => ({ ...current, userPrompt: value }))} placeholder="填写用户指令" />
          </div>
        </div>
      </Modal>
    </div>
  )

  const renderAudit = () => (
    <div className="console-page-shell">
      <PageHeader badge="系统设置 / 操作记录" title="配置记录" description="最近配置与操作变更。" />
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
            <Text className="mt-2 block console-caption">修改后回到这里复核记录。</Text>
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
            <div className="app-layout">
              <div className="app-layout__banners">
                {trustSignal ? (
                  <Banner
                    type={trustSignal.tone === 'success' ? 'success' : trustSignal.tone === 'warning' ? 'warning' : 'info'}
                    description={trustSignal.title}
                    closeIcon={null}
                  />
                ) : null}
              </div>
              <div className="app-layout__sidebar" style={{ width: sidebarCollapsed ? 48 : sidebarWidth, position: 'relative' }}>
                {!sidebarCollapsed ? (
                  <>
                    <div className="sidebar-workspace-header">
                      <Avatar color="blue" size="small">L</Avatar>
                      <div className="sidebar-workspace-info">
                        <div className="sidebar-workspace-name">企业数据工厂</div>
                        <div className="sidebar-workspace-plan">{isAdmin ? '管理员' : '普通用户'}</div>
                      </div>
                    </div>
                    <div className="sidebar-nav-section">
                      <Card className="console-sidebar-card mb-3" bodyStyle={{ padding: 12 }}>
                        <Space wrap>
                          <Button theme="solid" type="primary" size="small" icon={<CirclePlus size={14} />} onClick={() => navigate('/console/planning')}>新建任务</Button>
                          <Button size="small" onClick={() => navigate('/console/tasks')}>我的任务</Button>
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
                      />
                    </div>
                    <div className="sidebar-user-area">
                      <div className="sidebar-stats-card">
                        <div className="console-summary-row"><span>任务</span><Text strong>{runtime?.datasetCount ?? 0}</Text></div>
                        <div className="console-summary-row"><span>队列</span><Text strong>{runtime?.queueDepth ?? 0}</Text></div>
                      </div>
                      <div className="sidebar-user-info">
                        <Avatar color="blue" size="extra-small">{user.email?.[0]?.toUpperCase() ?? 'U'}</Avatar>
                        <Text className="sidebar-user-email">{user.email}</Text>
                        <Button size="small" icon={<LogOut size={14} />} theme="borderless" onClick={() => void handleLogout()} />
                      </div>
                    </div>
                    <div className="sidebar-resize-handle" onMouseDown={handleSidebarResizeStart} />
                  </>
                ) : (
                  <div className="sidebar-collapsed-content">
                    <Button
                      icon={<PanelLeftOpen size={18} />}
                      theme="borderless"
                      className="sidebar-toggle-btn"
                      onClick={() => setSidebarCollapsed(false)}
                    />
                    <div className="sidebar-collapsed-icons">
                      {visibleUserPages.map((page) => (
                        <Button
                          key={page.route}
                          icon={<page.icon size={18} />}
                          theme={activeNav === page.route ? 'light' : 'borderless'}
                          type={activeNav === page.route ? 'primary' : 'tertiary'}
                          onClick={() => navigate(page.route)}
                        />
                      ))}
                      {isAdmin ? (
                        <>
                          <div className="sidebar-collapsed-divider" />
                          {adminPages.map((page) => (
                            <Button
                              key={page.route}
                              icon={<page.icon size={18} />}
                              theme={activeNav === page.route ? 'light' : 'borderless'}
                              type={activeNav === page.route ? 'primary' : 'tertiary'}
                              onClick={() => navigate(page.route)}
                            />
                          ))}
                        </>
                      ) : null}
                    </div>
                    <div className="sidebar-collapsed-bottom">
                      <Button
                        icon={<LogOut size={18} />}
                        theme="borderless"
                        type="tertiary"
                        onClick={() => void handleLogout()}
                      />
                    </div>
                  </div>
                )}
              </div>
              <div className="app-layout__header">
                <Button
                  icon={sidebarCollapsed ? <PanelLeftOpen size={16} /> : <PanelLeftClose size={16} />}
                  theme="borderless"
                  type="tertiary"
                  onClick={() => setSidebarCollapsed((current) => !current)}
                />
                <div className="app-header-breadcrumb">
                  {breadcrumbParent ? (
                    <>
                      <span>{breadcrumbParent}</span>
                      <ChevronRight size={12} />
                    </>
                  ) : null}
                  <span className="current">{breadcrumbLabel}</span>
                </div>
                {pipelineStages && location.pathname.match(/^\/console\/tasks\/\d+/) ? (
                  <div className="pipeline-stage-bar">
                    {pipelineStages.map((stage) => (
                      <div
                        key={stage.key}
                        className={clsx('pipeline-stage-item', {
                          active: stage.state === 'in_progress' || stage.state === 'queued',
                          done: stage.state === 'completed',
                        })}
                      >
                        {stage.state === 'completed' ? <ShieldCheck size={12} /> : null}
                        {stage.label}
                      </div>
                    ))}
                  </div>
                ) : null}
                <div className="app-header-actions">
                  <Button icon={<RefreshCw size={14} />} theme="borderless" type="tertiary" loading={workspaceLoading} onClick={() => void loadBootstrap('控制台数据已刷新')} />
                </div>
              </div>
              <div className="app-layout__content">
                    {trustSignal ? (
                      <div className="mb-4">
                        <TrustSignalCard signal={trustSignal} onDismiss={() => setTrustSignal(null)} onNavigate={(route) => navigate(route)} />
                      </div>
                    ) : null}
                    <Routes>
                      <Route path="/console/home" element={renderOverview()} />
                      <Route path="/console/overview" element={<Navigate to="/console/home" replace />} />
                      <Route path="/console/tasks" element={renderTaskIndex()} />
                      <Route path="/console/tasks/:taskId" element={renderTaskDetail()} />
                      <Route path="/console/planning" element={renderPlanning()} />
                      <Route path="/console/results" element={renderResultsHub()} />
                      <Route path="/console/evaluation" element={<EvaluationView datasets={datasets} />} />
                      <Route path="/console/cleaning" element={<CleaningView datasets={datasets} />} />
                      {isAdmin ? <Route path="/console/operations" element={renderOperations()} /> : null}
                      <Route path="/console/domains" element={renderDomains()} />
                      <Route path="/console/questions" element={renderQuestionStage()} />
                      <Route path="/console/reasoning" element={renderReasoningStage()} />
                      <Route path="/console/rewards" element={renderRewardStage()} />
                      <Route path="/console/exports" element={renderExportStage()} />
                      <Route path="/console/help" element={renderHelp()} />
                      {isAdmin ? <Route path="/console/admin/providers" element={renderProviders()} /> : null}
                      {isAdmin ? <Route path="/console/admin/storage" element={renderStorage()} /> : null}
                      {isAdmin ? <Route path="/console/admin/strategies" element={renderStrategies()} /> : null}
                      {isAdmin ? <Route path="/console/admin/prompts" element={renderPrompts()} /> : null}
                      {isAdmin ? <Route path="/console/admin/audit" element={renderAudit()} /> : null}
                      <Route path="*" element={<Navigate to="/console/home" replace />} />
                    </Routes>
              </div>
              <div className="app-layout__aside" />
            </div>
          ) : (
            <Navigate to="/login" replace />
          )
        }
      />
    </Routes>
  )
}
