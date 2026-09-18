import type { ComponentProps } from 'react'
import type { Tag } from '@douyinfe/semi-ui'
import type { ApiError } from '../../lib/api'

/** Semi Tag 的合法颜色值（TagColor 未从包入口导出，这里从组件 props 反推）。 */
export type TagTone = NonNullable<ComponentProps<typeof Tag>['color']>

/** 把 API 错误统一转成可展示的中文文案（api.ts 拦截器已给出可读 message）。 */
export function errorMessage(error: unknown): string {
  if (error instanceof Error) return (error as ApiError).message || '请求失败'
  return String(error)
}

/** 评估运行状态 → 中文标签与 Semi Tag 颜色。 */
export function runStatusMeta(status: string): { label: string; color: TagTone } {
  switch (status) {
    case 'draft':
      return { label: '草稿', color: 'grey' }
    case 'queued':
      return { label: '已入队', color: 'blue' }
    case 'running':
      return { label: '评估中', color: 'orange' }
    case 'partial_failed':
      return { label: '部分失败', color: 'orange' }
    case 'failed':
      return { label: '失败', color: 'red' }
    case 'completed':
      return { label: '已完成', color: 'green' }
    default:
      return { label: status || '未知', color: 'grey' }
  }
}

/** 裁判/条目打分状态 → 中文标签与颜色。 */
export function scoreStatusMeta(status: string): { label: string; color: TagTone } {
  switch (status) {
    case 'completed':
    case 'scored':
      return { label: '已完成', color: 'green' }
    case 'running':
      return { label: '打分中', color: 'orange' }
    case 'failed':
      return { label: '失败', color: 'red' }
    case 'skipped':
      return { label: '已跳过', color: 'grey' }
    default:
      return { label: status || '待处理', color: 'blue' }
  }
}

/** 运行未结束（需要继续轮询进度）的状态集合。 */
export function isRunActive(status: string): boolean {
  return status === 'queued' || status === 'running'
}

export function formatScore(value: number): string {
  if (!Number.isFinite(value)) return '-'
  return value.toFixed(2)
}

export function formatPercent(value: number): string {
  if (!Number.isFinite(value)) return '-'
  return `${Math.round(value * 100)}%`
}

/** 一致性 ≥ 该阈值视为可信；低于阈值在 UI 上给出警示。 */
export const AGREEMENT_WARNING_THRESHOLD = 0.7
/** 一致性低于该阈值视为严重不一致。 */
export const AGREEMENT_CRITICAL_THRESHOLD = 0.5

export function agreementLevel(value: number): 'high' | 'medium' | 'low' | 'not_applicable' {
  // 负值表示后端「不适用」哨兵（样本不足，一致性无从计算），不能当成低一致性报警。
  if (!Number.isFinite(value) || value < 0) return 'not_applicable'
  if (value >= AGREEMENT_WARNING_THRESHOLD) return 'high'
  if (value >= AGREEMENT_CRITICAL_THRESHOLD) return 'medium'
  return 'low'
}

/** 评估运行列表轮询间隔（毫秒）。 */
export const RUN_POLL_INTERVAL_MS = 5000

export type SamplingMode = 'full' | 'ratio' | 'count'

/**
 * 抽样参数校验：只校验当前模式对应的输入（不相关输入不应参与校验）。
 * 返回中文错误文案，合法时返回 null。
 */
export function samplingInputError(mode: SamplingMode, sampleRatio: number, sampleSize: number): string | null {
  if (mode === 'ratio' && !(sampleRatio > 0 && sampleRatio <= 1)) return '抽样比例必须落在 0~1 之间'
  if (mode === 'count' && !(sampleSize > 0)) return '抽样条数必须大于 0'
  return null
}

/** 维度选择：按 category 聚合，供「按分类全选」使用。 */
export function groupByCategory<T>(items: T[], categoryOf: (item: T) => string): Array<[string, T[]]> {
  const map = new Map<string, T[]>()
  for (const item of items) {
    const key = categoryOf(item) || '未分类'
    const bucket = map.get(key)
    if (bucket) bucket.push(item)
    else map.set(key, [item])
  }
  return Array.from(map.entries()).sort((a, b) => a[0].localeCompare(b[0]))
}
