/**
 * 任务上下文导航（stageFlow 里的 buildContextSteps 等纯函数）单元测试。
 *
 * 位置说明：本目录不在 apps/web-user/tsconfig.json 的 `include`（仅 `src`）内，
 * 因此不参与 `tsc --noEmit` 与 vite 构建，可直接以 .ts 后缀导入被测文件。
 *
 * 之所以把被测逻辑放在 stageFlow.ts（而不是单独的 navFlow.ts）：后者需要
 * `import ... from './stageFlow'`，而 Node 24 的 ESM 不再支持无扩展名解析
 * （`--experimental-specifier-resolution` 已被移除），且 `allowImportingTsExtensions: false`
 * 又不允许写成 `./stageFlow.ts`。stageFlow.ts 自身零 import，可直接被测。
 * 这也顺带说明：**零依赖的纯模块是可测性的前提**。
 *
 * 运行：node --experimental-strip-types --test tests/*.test.ts
 */
import test from 'node:test'
import assert from 'node:assert/strict'

import {
  buildContextSteps,
  firstIncompleteBefore,
  shouldShowTaskContext,
  PIPELINE_STAGES,
} from '../src/views/flow/stageFlow.ts'

const stagesOf = (status?: string, key?: any) => buildContextSteps(status, key)

test('步骤条始终给出完整的 5 步，顺序与流水线一致', () => {
  const steps = stagesOf('draft', 'domains')
  assert.equal(steps.length, 5)
  assert.deepEqual(
    steps.map((s) => s.label),
    PIPELINE_STAGES.map((s) => s.label),
  )
  assert.deepEqual(
    steps.map((s) => s.step),
    [1, 2, 3, 4, 5],
  )
})

test('当前步标记为 current，已完成的前置步标记为 done', () => {
  // questions_generated ⇒ 主题结构(1) 与 问题生成(2) 都已完成
  const steps = stagesOf('questions_generated', 'questions')
  assert.equal(steps[0].state, 'done', '主题结构应已完成')
  assert.equal(steps[1].state, 'current', '问题生成应为当前步')
  assert.equal(steps[2].state, 'todo', '答案内容应未开始')
})

test('强制顺序（NN/g 准则 3）：未完成的前置步骤不可点', () => {
  const steps = stagesOf('questions_generated', 'questions')
  assert.equal(steps[0].clickable, true, '已完成的步骤可回看')
  assert.equal(steps[1].clickable, true, '当前步可点（相当于刷新）')
  assert.equal(steps[2].clickable, false, '未完成的后续步骤必须锁死')
  assert.equal(steps[3].clickable, false)
  assert.equal(steps[4].clickable, false)
})

test('锁死提示指向真正的前置缺口，而不是笼统的「上一步」', () => {
  // draft ⇒ 只有第 1 步未完成
  const steps = stagesOf('draft', 'domains')
  // 第 4 步锁死的真正原因仍是第 1 步没做完
  assert.equal(steps[3].clickable, false)
  assert.match(steps[3].lockedReason, /主题结构/)
  assert.doesNotMatch(steps[3].lockedReason, /质量评分/)

  // draft ⇒ 主题结构尚未完成
  const draft = 'draft'
  assert.equal(firstIncompleteBefore(3, draft), '主题结构')
  // directions_completed ⇒ 主题结构已完成、问题生成未完成，缺口应前移到问题生成
  assert.equal(firstIncompleteBefore(3, 'directions_completed'), '问题生成')
  // 第 1、2 步都完成时，第 3 步无缺口 → 回退文案
  assert.equal(firstIncompleteBefore(3, 'questions_generated'), '前置步骤')
})

test('失败阶段标记为 failed，且仍可点（必须能回去重试）', () => {
  const steps = stagesOf('questions_failed', 'questions')
  const current = steps.find((s) => s.key === 'questions')
  assert.equal(current?.state, 'failed')
  assert.equal(current?.clickable, true, '失败阶段必须可点，否则用户无法重试')

  // reasoning_partial 属于答案阶段的失败态
  const steps2 = stagesOf('reasoning_partial', 'reasoning')
  assert.equal(steps2.find((s) => s.key === 'reasoning')?.state, 'failed')
})

test('当前步优先于 done 显示（回到已完成步骤页时仍高亮为 current）', () => {
  // 已完成问题生成，但用户回看到「主题结构」页
  const steps = stagesOf('questions_generated', 'domains')
  assert.equal(steps[0].state, 'current', '所在页面应高亮为 current')
  assert.equal(steps[1].state, 'done', '更靠后的已完成步骤显示 done')
})

test('未知/空状态不崩溃，且不误判任何步骤可点', () => {
  for (const status of [undefined, '', 'unknown_state', 'directions_completed']) {
    const steps = stagesOf(status as any, 'domains')
    assert.equal(steps.length, 5)
    // 除当前步外，其余未完成步骤都不可点
    const clickableBeyondCurrent = steps.filter((s) => s.clickable && s.state !== 'current' && s.state !== 'done')
    assert.equal(clickableBeyondCurrent.length, 0, `${String(status)} 不应放行未完成步骤`)
  }
})

test('全部完成后每步都可回看', () => {
  const steps = stagesOf('export_generated', 'export')
  assert.equal(steps.every((s) => s.clickable), true)
  assert.equal(steps[4].state, 'current')
})

test('回归：当前步已完成时 done 为真，不能因 state=current 而误判为未完成', () => {
  // 数据集实际状态为 directions_completed（主题结构已完成），此时打开主题结构页：
  // state 是 current（因为就在这一页），但 done 必须为 true，
  // 否则「下一步」按钮会恒为禁用，用户永远无法前进。
  const steps = stagesOf('directions_completed', 'domains')
  const first = steps[0]
  assert.equal(first.state, 'current', '所在页面高亮为 current')
  assert.equal(first.done, true, '该步其实已完成，done 必须为 true')
})

test('回归：回退永不被锁（NN/g 准则 5：可中断恢复）', () => {
  // 在阶段 3 时，阶段 1、2 必须能回去看，否则用户会被困在当前页。
  const steps = stagesOf('directions_completed', 'reasoning')
  assert.equal(steps[0].clickable, true, '阶段1 可回退')
  assert.equal(steps[1].clickable, true, '阶段2 可回退（即使它尚未完成）')
  assert.equal(steps[2].clickable, true, '当前页可点')
  // 但向前仍受强制顺序限制
  assert.equal(steps[3].clickable, false, '阶段4 尚未完成，不得跳过')
  assert.equal(steps[4].clickable, false, '阶段5 尚未完成，不得跳过')
})

test('回归：向前跳步仍被阻止（准则 3 未被回退放行削弱）', () => {
  // 在阶段 1 时，任何后续步骤都不得可点。
  const steps = stagesOf('draft', 'domains')
  for (const step of steps.slice(1)) {
    assert.equal(step.clickable, false, `${step.label} 不得从阶段1 直接跳入`)
  }
})

test('步骤条只在任务上下文页面显示', () => {
  // 应显示：5 个阶段页 + 任务详情页
  for (const stage of PIPELINE_STAGES) {
    assert.equal(shouldShowTaskContext(stage.route), true, `${stage.route} 应显示步骤条`)
  }
  assert.equal(shouldShowTaskContext('/console/tasks/11'), true, '任务详情页应显示')

  // 不应显示：全局页面与管理页（避免流程语义扩散）
  for (const route of [
    '/console/home',
    '/console/tasks',
    '/console/planning',
    '/console/results',
    '/console/evaluation',
    '/console/cleaning',
    '/console/exports/extra',
    '/console/admin/providers',
  ]) {
    assert.equal(shouldShowTaskContext(route), false, `${route} 不应显示步骤条`)
  }
})
