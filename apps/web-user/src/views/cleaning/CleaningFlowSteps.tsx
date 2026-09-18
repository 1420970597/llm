import clsx from 'clsx'
import { Card, Space, Tag, Typography } from '@douyinfe/semi-ui'
import { ArrowRight, CircleCheck, CircleDashed, Compass } from 'lucide-react'

const { Text, Title } = Typography

/**
 * 任务流程步骤条（L14 独占）。
 *
 * 目的：把「建任务 → 出数据 → 质量评估 → 数据清洗 → 交付」这条主线讲清楚，
 * 让用户知道数据清洗处在第几步、前置是什么、产出物去哪里看。
 * 导航入口固定在 App.tsx（不得修改），因此这里只做流程位置感与引导链接。
 */
export type FlowStep = {
  key: string
  label: string
  detail: string
  route: string
}

export const TASK_FLOW_STEPS: FlowStep[] = [
  { key: 'planning', label: '新建任务', detail: '填写主题与目标规模', route: '/console/planning' },
  { key: 'tasks', label: '生成数据', detail: '问题、思维链、答案逐步产出', route: '/console/tasks' },
  { key: 'evaluation', label: '质量评估', detail: '多模型互评打分', route: '/console/evaluation' },
  { key: 'cleaning', label: '数据清洗', detail: '拦截拒答与异常样本', route: '/console/cleaning' },
  { key: 'results', label: '导出交付', detail: '在数据资产导出成品', route: '/console/results' },
]

export const CLEANING_STEP_INDEX = TASK_FLOW_STEPS.findIndex((step) => step.key === 'cleaning')

export function CleaningFlowSteps({ onNavigate }: { onNavigate: (route: string) => void }) {
  return (
    <Card className="console-panel" bodyStyle={{ padding: 20 }}>
      <Space align="center" className="mb-3">
        <Compass size={16} />
        <Title heading={5} className="!mb-0">这一步在任务流程里的位置</Title>
      </Space>
      <Text className="block console-caption">
        数据清洗是第 {CLEANING_STEP_INDEX + 1} 步（共 {TASK_FLOW_STEPS.length} 步）：先生成数据、再做质量评估，
        然后在这里拦截拒答与异常样本，最后到「数据资产」导出成品。清洗只改样本状态，不改写内容。
      </Text>
      <div className="grid grid-cols-2 md:grid-cols-5 gap-3 mt-4">
        {TASK_FLOW_STEPS.map((step, index) => {
          const isCurrent = index === CLEANING_STEP_INDEX
          const isDone = index < CLEANING_STEP_INDEX
          return (
            <div
              key={step.key}
              className={clsx('console-domain-item grid gap-1.5', isCurrent && 'ring-2 ring-blue-400', isDone && 'ring-1 ring-green-300')}
            >
              <div className="flex items-center gap-2">
                <span className="inline-flex items-center gap-1 text-xs font-bold text-blue-600">
                  {isDone ? <CircleCheck size={14} /> : isCurrent ? <ArrowRight size={14} /> : <CircleDashed size={14} />}
                  {index + 1}
                </span>
                <Text strong>{step.label}</Text>
                {isCurrent ? <Tag color="blue" size="small">当前位置</Tag> : null}
              </div>
              <Text className="text-xs console-caption">{step.detail}</Text>
              {!isCurrent ? (
                <button type="button" className="link-button" onClick={() => onNavigate(step.route)}>
                  前往{step.label}
                </button>
              ) : null}
            </div>
          )
        })}
      </div>
    </Card>
  )
}
