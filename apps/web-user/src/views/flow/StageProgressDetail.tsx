import { Banner, Progress, Tag, Typography } from '@douyinfe/semi-ui'
import { AlertTriangle, Clock } from 'lucide-react'

const { Text, Title } = Typography

/**
 * 等待/失败期间的真实进度展示。
 *
 * 解决的问题（issue #84「操作步骤不符合人类习惯」/ #83「失败不显示原因」）：
 *
 * #84 调研发现，任务在等待或失败期间，界面只给静态文案：
 *   「等待状态: 状态同步中」
 *   「等待原因: 系统同步中，请稍后刷新。」
 *
 * NN/g 的进度指示器研究明确反对这种「静态指示器」：
 *   - "Always give some type of immediate feedback."
 *   - 超过 10 秒的操作应展示 **百分比进度** 与 **ETA 估算**
 *   - "Static progress indicators: Don't use them." —— 系统若卡住，用户无从判断
 *
 * 而本系统其实**已经有**这些数据：`GET /datasets/{id}/pipeline/progress` 返回
 * `completionPercent`、各阶段 `state`/`count`/`summary`，运行时接口还提供
 * `queueDepth` 与 ETA。本组件的作用就是把这些已有数据真正展示出来。
 *
 * 同时对应 #83：worker 失败时（如 storage_profiles 未配置导致
 * `no rows in result set`），界面此前只显示「请排查失败原因」，
 * 用户无法知道真实原因。本组件在有 failureReason 时置顶醒目展示。
 *
 * 纯展示组件：props 进、无回调、不请求数据。
 */

export type StageProgressState = 'pending' | 'queued' | 'in_progress' | 'completed' | 'failed'

export type StageProgressItem = {
  key: string
  label: string
  state: StageProgressState
  count: number
  summary: string
}

export type StageProgressDetailProps = {
  /** 总进度 0-100 */
  completionPercent: number
  /** 阶段列表（来自 /pipeline/progress 的 stages 字段） */
  stages: StageProgressItem[]
  /** 队列深度 */
  queueDepth: number
  /** ETA 文案，如「约 3 分钟」；未知时传 '—' */
  eta: string
  /** 失败原因；有值时优先展示（对应 issue #83） */
  failureReason?: string
}

/** 阶段状态的展示样式。 */
function stageStateStyle(state: StageProgressState): { label: string; color: 'grey' | 'orange' | 'blue' | 'green' | 'red' } {
  switch (state) {
    case 'completed':
      return { label: '已完成', color: 'green' }
    case 'failed':
      return { label: '失败', color: 'red' }
    case 'in_progress':
      return { label: '进行中', color: 'blue' }
    case 'queued':
      return { label: '排队中', color: 'orange' }
    default:
      return { label: '待开始', color: 'grey' }
  }
}

export function StageProgressDetail({
  completionPercent,
  stages,
  queueDepth,
  eta,
  failureReason,
}: StageProgressDetailProps): JSX.Element {
  // 进度值做边界收敛，避免后端异常数据把进度条画到 0-100 之外。
  const percent = Math.max(0, Math.min(100, Math.round(completionPercent)))
  const hasFailure = Boolean(failureReason && failureReason.trim())
  const etaText = !eta || eta === '—' ? '预计时间待确认' : eta

  return (
    <div className="console-stack">
      {hasFailure ? (
        <Banner
          type="danger"
          icon={<AlertTriangle size={16} />}
          description={
            <div className="console-next-step-list">
              <Text strong>本阶段失败</Text>
              <Text className="mt-1 block console-caption">{failureReason}</Text>
              <Text className="mt-1 block console-caption">请按上方原因处理后重试；修复后本阶段可从当前进度继续。</Text>
            </div>
          }
        />
      ) : null}

      <div>
        <div className="flex flex-wrap items-center justify-between gap-2">
          <Text strong>总体进度</Text>
          <Text className="console-caption">{percent}%</Text>
        </div>
        <Progress
          percent={percent}
          showInfo={false}
          stroke={hasFailure ? '#ef4444' : '#3b82f6'}
          className="mt-2"
        />
        <div className="mt-2 flex flex-wrap items-center gap-3">
          <Text className="console-caption flex items-center gap-1">
            <Clock size={13} />
            {etaText}
          </Text>
          {queueDepth > 0 ? (
            <Text className="console-caption">队列中还有 {queueDepth} 个任务</Text>
          ) : (
            <Text className="console-caption">当前无排队任务</Text>
          )}
        </div>
      </div>

      <div>
        <Title heading={6} className="!mb-0">各阶段明细</Title>
        <div className="console-summary-grid mt-3">
          {stages.map((stage) => {
            const style = stageStateStyle(stage.state)
            return (
              <div key={stage.key} className="console-summary-row">
                <span>{stage.label}</span>
                <span className="flex flex-wrap items-center gap-2">
                  <Tag color={style.color} size="small">{style.label}</Tag>
                  <Text className="console-caption">{stage.count} 条</Text>
                </span>
              </div>
            )
          })}
        </div>
        {stages.length > 0 ? (
          <div className="mt-3 console-stack">
            {stages
              .filter((stage) => stage.summary)
              .map((stage) => (
                <Text key={`${stage.key}-summary`} className="console-caption">
                  • {stage.label}：{stage.summary}
                </Text>
              ))}
          </div>
        ) : (
          <Text className="mt-3 block console-caption">阶段进度尚未生成。</Text>
        )}
      </div>
    </div>
  )
}
