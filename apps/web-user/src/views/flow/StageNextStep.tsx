import { Button, Space, Typography } from '@douyinfe/semi-ui'
import { ArrowRight, CircleCheck } from 'lucide-react'

const { Text } = Typography

/**
 * 阶段页的「下一步」导航条。
 *
 * 解决 issue #84 的核心问题：
 * 5 个任务阶段页（/console/domains、/console/questions、/console/reasoning、
 * /console/rewards、/console/exports）此前都没有前进入口，用户完成本阶段后
 * 只能点「返回当前任务」绕回任务详情页，再从 5 张阶段卡片里找下一张——
 * 实测完成一次流水线需要 5 次「返回详情 → 点卡片」的往返。
 *
 * 同时对应 issue #90 的「每个阶段页必须提供下一步」建议：
 * 阶段页应在 PageHeader 区域直接给出下一阶段的直达按钮，
 * 让「一条流水线」在读感上连续，而不是 5 个互相孤立的页面。
 *
 * 设计要点（与调研结论一致）：
 * - 阶段未完成时按钮禁用并给出原因，而不是让用户点了才发现不满足前置条件；
 * - 组件本身不做路由跳转，由父代理注入 onNavigate，便于接入既有 navigate 与数据刷新。
 */
export type StageNextStepProps = {
  /** 下一阶段名称，如「问题生成」 */
  nextLabel: string
  /** 下一阶段路由，如 /console/questions */
  nextRoute: string
  /** 当前阶段是否已完成，未完成时禁用「下一步」 */
  currentStageDone: boolean
  /** 可选：点击「下一步」时的额外副作用（父代理用于刷新数据） */
  onNavigate?: (route: string) => void
  /** 可选：当前阶段未完成时的提示文案 */
  blockedHint?: string
}

const DEFAULT_BLOCKED_HINT = '完成本阶段后可进入下一步'

export function StageNextStep({
  nextLabel,
  nextRoute,
  currentStageDone,
  onNavigate,
  blockedHint,
}: StageNextStepProps) {
  const hint = blockedHint ?? DEFAULT_BLOCKED_HINT

  return (
    <Space align="center" spacing="medium" wrap>
      <Button
        theme="solid"
        type="primary"
        icon={currentStageDone ? <ArrowRight size={16} /> : <CircleCheck size={16} />}
        iconPosition="right"
        disabled={!currentStageDone}
        onClick={() => {
          if (!currentStageDone) return
          onNavigate?.(nextRoute)
        }}
      >
        {`下一步：${nextLabel}`}
      </Button>
      {currentStageDone ? (
        <Text className="console-caption">本阶段已完成，可直接进入「{nextLabel}」。</Text>
      ) : (
        <Text className="console-caption">{hint}</Text>
      )}
    </Space>
  )
}
