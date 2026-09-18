import type { CleaningFinding, CleaningKeyword, CleaningReport, CleaningRule, CleaningRun } from '../../lib/api'

/**
 * 清洗模块的展示元数据与派生计算（L14 独占）。
 *
 * 取值来源（不得凭猜测）：
 * - 分类 / 匹配模式 / 严重度 / 动作 / 阶段：internal/cleaning/keywords.go 的常量与
 *   cleaning.ValidStage。
 * - 运行状态：cleaning_runs.status，取值 queued / running / completed / failed
 *   （internal/store/cleaning_store_runs.go 的 INSERT / UPDATE 语句）。
 *
 * 未识别的取值一律回落到原始字符串，避免后端新增枚举后界面显示空白。
 */

export type CleaningStage = {
  key: string
  label: string
  description: string
}

/** 可拦截的三个阶段，与 cleaning.DefaultStages() 一致。 */
export const CLEANING_STAGES: CleaningStage[] = [
  { key: 'question', label: '问题', description: '提问本身出现拒答、安全回避或占位文本' },
  { key: 'reasoning', label: '思维链', description: '推理过程里出现拒答、含糊推脱或省略号占位' },
  { key: 'answer', label: '答案', description: '最终回答里出现拒答、免责声明或未完成占位' },
]

export const STAGE_KEYS = CLEANING_STAGES.map((stage) => stage.key)

const CATEGORY_LABELS: Record<string, string> = {
  refusal: '中文拒答',
  safety: '安全与合规回避',
  uncertainty_evasion: '含糊推脱',
  english_refusal: '英文拒答',
  placeholder: '占位与空输出',
}

const SEVERITY_LABELS: Record<string, string> = {
  block: '拦截（block）',
  warn: '告警（warn）',
}

const MATCH_MODE_LABELS: Record<string, string> = {
  contains: '包含（contains）',
  prefix: '句首（prefix）',
  regex: '正则（regex）',
}

const ACTION_LABELS: Record<string, string> = {
  drop: '丢弃样本',
  flag: '标记待复查',
  retry: '重新生成',
}

const RUN_STATUS_LABELS: Record<string, string> = {
  queued: '排队中',
  running: '清洗中',
  completed: '已完成',
  failed: '失败',
}

export function categoryLabel(category: string): string {
  return CATEGORY_LABELS[category] ?? category
}

export function severityLabel(severity: string): string {
  return SEVERITY_LABELS[severity] ?? severity
}

export function matchModeLabel(mode: string): string {
  return MATCH_MODE_LABELS[mode] ?? (mode || '包含（contains）')
}

export function actionLabel(action: string): string {
  return ACTION_LABELS[action] ?? action
}

export function runStatusLabel(status: string): string {
  return RUN_STATUS_LABELS[status] ?? status
}

/** 任务（datasets.status）的中文文案，取值与 App.tsx 的 statusLabel 保持一致。 */
const DATASET_STATUS_LABELS: Record<string, string> = {
  draft: '待确认主题结构',
  domains_confirmed: '结构已确认，待生成问题',
  questions_queued: '问题生成排队中',
  questions_generated: '问题已就绪，待生成答案',
  questions_failed: '问题生成失败',
  reasoning_queued: '答案生成排队中',
  reasoning_generated: '答案已就绪，待质量评估',
  reasoning_partial: '答案部分生成，需复核',
  reasoning_failed: '答案生成失败',
  rewards_queued: '质量评分排队中',
  rewards_generated: '评估完成，可导出交付',
  rewards_partial: '评分部分完成，需复核',
  rewards_failed: '质量评分失败',
  export_queued: '导出任务排队中',
  export_generated: '导出已完成',
  export_failed: '导出失败',
}

export function datasetStatusLabel(status: string): string {
  return DATASET_STATUS_LABELS[status] ?? status
}

export function stageLabel(stage: string): string {
  return CLEANING_STAGES.find((item) => item.key === stage)?.label ?? stage
}

export function runStatusColor(status: string): 'blue' | 'green' | 'orange' | 'red' | 'grey' {
  switch (status) {
    case 'completed':
      return 'green'
    case 'running':
      return 'blue'
    case 'queued':
      return 'orange'
    case 'failed':
      return 'red'
    default:
      return 'grey'
  }
}

/** 阶段范围为空 = 全部阶段生效（internal/cleaning/keywords.go 的 ruleAppliesToStage）。 */
export function stageScopeLabel(stageScope: string[]): string {
  if (!stageScope || stageScope.length === 0) {
    return '全部阶段'
  }
  return stageScope.map(stageLabel).join(' / ')
}

export function formatCleaningTime(value: string): string {
  if (!value) {
    return '—'
  }
  const parsed = new Date(value)
  if (Number.isNaN(parsed.getTime())) {
    return value
  }
  return parsed.toLocaleString('zh-CN', { hour12: false })
}

export function percentLabel(rate: number): string {
  if (!Number.isFinite(rate)) {
    return '—'
  }
  return `${(rate * 100).toFixed(1)}%`
}

export function runHasFinished(run: CleaningRun | null | undefined): boolean {
  return run?.status === 'completed' || run?.status === 'failed'
}

export type SeverityDistribution = {
  block: number
  warn: number
  unknown: number
  total: number
}

/**
 * 严重度分布 = findings.keywordId 与关键词库 severity 的**客户端 join**。
 *
 * 后端 CleaningReport 结构体（internal/model/cleaning.go:79-86）没有 severity 字段，
 * 因此这里用两个真实接口的数据做派生统计：findings 提供命中次数，关键词库提供严重度。
 * 关键词已删除导致 join 不上的命中计入 unknown，不静默丢弃。
 */
export function deriveSeverityDistribution(
  findings: CleaningFinding[],
  keywords: CleaningKeyword[],
): SeverityDistribution {
  const severityByKeywordId = new Map<number, string>()
  keywords.forEach((keyword) => severityByKeywordId.set(keyword.id, keyword.severity))

  const distribution: SeverityDistribution = { block: 0, warn: 0, unknown: 0, total: findings.length }
  findings.forEach((finding) => {
    const severity = severityByKeywordId.get(finding.keywordId)
    if (severity === 'block') {
      distribution.block += 1
    } else if (severity === 'warn') {
      distribution.warn += 1
    } else {
      distribution.unknown += 1
    }
  })
  return distribution
}

export type CategoryHit = { category: string; label: string; hits: number }

/** 按关键词分类聚合命中次数，供「哪类词最容易误伤」判断使用。 */
export function deriveCategoryHits(keywords: CleaningKeyword[], findings: CleaningFinding[]): CategoryHit[] {
  const categoryByKeywordId = new Map<number, string>()
  keywords.forEach((keyword) => categoryByKeywordId.set(keyword.id, keyword.category))

  const hitsByCategory = new Map<string, number>()
  findings.forEach((finding) => {
    const category = categoryByKeywordId.get(finding.keywordId) ?? 'unknown'
    hitsByCategory.set(category, (hitsByCategory.get(category) ?? 0) + 1)
  })

  return Array.from(hitsByCategory.entries())
    .map(([category, hits]) => ({ category, label: categoryLabel(category), hits }))
    .sort((left, right) => right.hits - left.hits)
}

/**
 * 按 worker 实际判定顺序排列规则：priority 数字**大的先判定**，同级按名称。
 *
 * 依据是运行时路径 cleaning.Scan → decideAction（internal/cleaning/scanner.go），
 * 它对 priority 做降序排序；worker 加载规则也是 ORDER BY priority DESC
 * （apps/worker/job_cleaning.go）。
 *
 * 注意：internal/cleaning/keywords.go 里的 EvaluateRules 是升序（数字小的先判），
 * 但它在生产路径上没有任何调用方，只有单测在用，因此不作为界面依据。
 * 该不一致已作为未修复项上报，不在本 lane 的文件归属范围内。
 */
export function sortRulesByPriority(rules: CleaningRule[]): CleaningRule[] {
  return [...rules].sort(
    (left, right) => right.priority - left.priority || left.name.localeCompare(right.name) || left.id - right.id,
  )
}

/**
 * 用运行列表里的最新值覆盖报告自带的 run 快照。
 *
 * 原因：报告接口里的 run 是 worker 在 MarkDone 之前写入的，status 恒为 queued、
 * 计数全 0（internal/cleaning/scanner.go 的 BuildReport 接收的是 MarkDone 前的 run）。
 * 直接用快照会把已完成的运行渲染成「排队中 · 检查 0 条」，
 * 而且以快照状态判断未完成会导致轮询永不停止。
 *
 * 找不到同 id 的列表项时（例如列表尚未加载）退回快照，不丢信息。
 */
export function mergeReportRun(report: CleaningReport | null, runs: CleaningRun[]): CleaningRun | null {
  if (!report) {
    return null
  }
  const fresh = runs.find((item) => item.id === report.run.id)
  return fresh ? { ...report.run, ...fresh } : report.run
}
