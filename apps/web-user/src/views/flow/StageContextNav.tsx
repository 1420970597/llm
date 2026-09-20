/**
 * 任务上下文步骤导航（阶段页 / 任务详情页顶部）。
 *
 * 为什么需要它
 * ------------
 * 阶段页是从任务详情页进入的，侧边栏里并不存在这 5 个阶段，因此
 * **同一条流水线的两个阶段页，侧边栏看起来完全一样**——用户看不出
 * 「我在第几步 / 哪几步已完成 / 下一步去哪」。
 *
 * 本组件在阶段页内部补上任务上下文，而不是把这些阶段塞进全局侧边栏：
 * NN/g《Progressive Disclosure》指出流程步骤属于 staged disclosure（用户
 * 必然要走完），不应与「按需展开」的全局导航混在一起，否则全局菜单会
 * 承担流程语义，这正是改造前的问题。
 *
 * 设计对齐 NN/g《Wizards》三条准则：
 *   - 准则 2：显示步骤列表并高亮当前步
 *   - 准则 3：强制顺序——后续步骤锁死，不得跳过前置步骤
 *   - 准则 4：显式 next 按钮，且标签带信息量（不用笼统的「下一步」）
 *
 * 纯计算部分（buildContextSteps 等）在 stageFlow.ts，另有单元测试覆盖。
 */

import { Check, ChevronRight, Circle, Lock, AlertTriangle } from 'lucide-react'
import { clsx } from 'clsx'

import {
  buildContextSteps,
  nextStageOf,
  type StageKey,
  type StepState,
} from './stageFlow'

type StageContextNavProps = {
  /** 当前任务状态（如 `questions_generated`），用于推导每步的完成情况 */
  datasetStatus: string | undefined
  /** 当前所在阶段页的 key */
  currentStageKey: StageKey
  /** 跳转回调（由 App.tsx 注入 navigate，保持本组件不依赖路由库） */
  onNavigate: (route: string) => void
}

/** 每个状态对应的图标。todo 用空心圆表示「还没到」。 */
function StepIcon({ state }: { state: StepState }) {
  if (state === 'done') return <Check size={13} />
  if (state === 'failed') return <AlertTriangle size={13} />
  if (state === 'current') return <Circle size={13} />
  return <Circle size={13} />
}

const STATE_LABEL: Record<StepState, string> = {
  done: '已完成',
  current: '进行中',
  todo: '待开始',
  failed: '失败',
}

export function StageContextNav({
  datasetStatus,
  currentStageKey,
  onNavigate,
}: StageContextNavProps) {
  const steps = buildContextSteps(datasetStatus, currentStageKey)
  const nextStage = nextStageOf(currentStageKey)
  // 必须用 `done` 而不是 `state`：当前步的 state 是 `current`，
  // 直接比较会掩盖「本阶段已完成」的事实，导致「下一步」恒为禁用。
  const currentStep = steps.find((step) => step.key === currentStageKey)
  const currentDone = currentStep?.done ?? false

  return (
    <nav className="stage-context-nav" aria-label="任务阶段">
      <ol className="stage-context-steps">
        {steps.map((step, index) => {
          const isLast = index === steps.length - 1
          return (
            <li
              key={step.key}
              className={clsx('stage-context-step', `is-${step.state}`, {
                'is-clickable': step.clickable,
              })}
            >
              <button
                type="button"
                className="stage-context-step-btn"
                // 未完成的后续步骤不可点：既是 NN/g 准则 3 的强制顺序，
                // 也避免用户点进去后撞到后端 409。
                disabled={!step.clickable}
                title={step.clickable ? `进入「${step.label}」` : step.lockedReason}
                aria-current={step.state === 'current' ? 'step' : undefined}
                onClick={() => step.clickable && onNavigate(step.route)}
              >
                <span className="stage-context-icon" aria-hidden>
                  {step.clickable ? <StepIcon state={step.state} /> : <Lock size={12} />}
                </span>
                <span className="stage-context-text">
                  <span className="stage-context-label">
                    {step.step}. {step.label}
                  </span>
                  <span className="stage-context-state">{STATE_LABEL[step.state]}</span>
                </span>
              </button>
              {!isLast ? (
                <ChevronRight className="stage-context-arrow" size={13} aria-hidden />
              ) : null}
            </li>
          )
        })}
      </ol>

      {nextStage ? (
        <button
          type="button"
          className={clsx('stage-context-next', { 'is-disabled': !currentDone })}
          disabled={!currentDone}
          // 标签带信息量：NN/g 准则 4 明确反对笼统的「下一步」
          title={currentDone ? `进入「${nextStage.label}」` : '完成本阶段后可进入下一阶段'}
          onClick={() => currentDone && onNavigate(nextStage.route)}
        >
          {currentDone ? (
            <>
              下一步：{nextStage.label}
              <ChevronRight size={14} aria-hidden />
            </>
          ) : (
            '完成本阶段后可进入下一阶段'
          )}
        </button>
      ) : (
        <span className="stage-context-next is-final">已是最后一步</span>
      )}
    </nav>
  )
}
