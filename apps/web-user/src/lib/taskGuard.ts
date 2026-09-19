/**
 * 「未选中任务」时的统一守卫。
 *
 * ---------------------------------------------------------------------------
 * 为什么要有这个模块（issue #64）
 * ---------------------------------------------------------------------------
 * 修复前，全文有 5 处同类守卫写法不一致，其中 4 处是**静默 return**：
 *
 *   App.tsx:1437  generateDomains    if (!activeDatasetId) return Toast.warning('请先选择任务')   ✅ 有提示
 *   App.tsx:1517  generateQuestions  if (!activeDatasetId) return                                  ❌ 静默
 *   App.tsx:1547  generateReasoning  if (!activeDatasetId) return                                  ❌ 静默
 *   App.tsx:1577  generateRewards    if (!activeDatasetId) return                                  ❌ 静默
 *   App.tsx:1607  generateExport     if (!activeDatasetId) return                                  ❌ 静默
 *
 * 另外 3 处刷新按钮用短路表达式，`activeDatasetId` 为 null 时**整个表达式直接求值结束**，
 * 既不调用任何函数也不提示：
 *
 *   App.tsx:2557  「刷新结构」  onClick={() => activeDatasetId && void loadDatasetWorkspace(...)}
 *   App.tsx:2760  「刷新结果」  onClick={() => activeDatasetId && void loadDatasetWorkspace(...)}
 *   App.tsx:3048  「刷新当前任务状态」 onClick={() => activeDatasetId ? ... : void loadBootstrap(...)}
 *
 * 用户看到的是「点了没反应」，无法判断是自己点错、系统坏了、还是需要先做别的。
 * 这违反 todo.md 第 14 节设计原则：「任何异步动作都必须有可见状态」「任何失败都必须有恢复路径」。
 *
 * ---------------------------------------------------------------------------
 * 为什么抽成独立模块（而不是内联改 5 处）
 * ---------------------------------------------------------------------------
 * 契约 §1.4 要求「全文 5 处同类守卫必须行为一致」。把判定与提示抽成**单一实现**，
 * 「一致」就是结构上成立的，而不是靠人工比对齐。
 *
 * 更重要的原因：本仓没有 DOM 测试库（无 jsdom / playwright / chromium），
 * 而守卫位于 onClick 回调里 —— 纯 SSR 不会执行回调，因此**无法**用渲染断言证明
 * 「守卫真的提示了」。把判定写成不依赖 React 的纯函数后，测试可以直接执行
 * **生产代码本身**（注入可观测的提示记录器），断言：
 *   - 未选中任务时，提示通道确实被调用，且业务动作**没有**被调用（这才是「无静默 return」）；
 *   - 已选中任务时，提示不触发，业务动作拿到正确的 datasetId。
 *
 * 这一层可测试性是这次重构的主要收益：把「有没有提示」从人工检查变成机器可证的约束。
 */

/**
 * 未选中任务时的统一提示文案。
 *
 * 面向普通用户：说清「要做什么」而不是「缺什么参数」，且不含接口名/SQL/表名等技术术语。
 * 导出成常量而不是在多处写同一句话，避免文案漂移。
 */
export const NO_ACTIVE_TASK_MESSAGE = '请先进入具体任务再执行该操作'

/** 提示通道。生产实现是 Toast + 页内提示卡；测试注入一个可观测的记录器。 */
export type TaskNotice = (message: string) => void

/**
 * 校验 datasetId 是否可用。
 *
 * 把「0 / 负数 / null / undefined」统一视为未选中：后端 id 从 1 开始，
 * 传 0 进去只会拿到 404，不如在入口就拦下并给出可读提示。
 */
function isUsableDatasetId(value: number | null | undefined): value is number {
  return typeof value === 'number' && Number.isFinite(value) && value > 0
}

/**
 * 把「未选中任务」的判定与提示收敛到一处，返回可用的 datasetId 或 null。
 *
 * 用法（生成类动作需要 id 时）：
 *
 *   const datasetId = requireDatasetId('生成题目')
 *   if (!datasetId) return            // 此时提示已经发出，不是静默返回
 *   await consoleApi.generateQuestions(datasetId)
 *
 * @param activeDatasetId 当前选中的任务 id
 * @param notice          提示通道；未选中时会被调用恰好一次
 * @param actionLabel     动作名（如「生成题目」），用于拼出「做什么需要先选任务」
 */
export function resolveDatasetId(
  activeDatasetId: number | null | undefined,
  notice: TaskNotice,
  actionLabel: string,
): number | null {
  if (isUsableDatasetId(activeDatasetId)) {
    return activeDatasetId
  }
  // 注意文案顺序：先说用户要做的动作，再说前置条件，比「请先选择任务」更可执行。
  notice(`${actionLabel}需要先选择一个任务，${NO_ACTIVE_TASK_MESSAGE}`)
  return null
}

/**
 * 同 resolveDatasetId，但**直接执行**动作，供「刷新类」按钮使用。
 *
 * 返回值表示动作是否被发起，便于调用方/测试断言「没有静默 pass-through」。
 *
 * 用法（刷新类动作自带 id 即可）：
 *
 *   onClick={() => runWithDataset((id) => loadDatasetWorkspace(id, '数据资产页已刷新'))}
 *
 * @returns 动作用了真实 id 时为 true；未选中任务（并已提示）时为 false
 */
export function withActiveDataset(
  activeDatasetId: number | null | undefined,
  notice: TaskNotice,
  action: (datasetId: number) => void | Promise<void>,
): boolean {
  if (!isUsableDatasetId(activeDatasetId)) {
    notice(NO_ACTIVE_TASK_MESSAGE)
    return false
  }
  // 动作自己负责错误处理（与仓内既有 `void loadDatasetWorkspace(...)` 约定一致），
  // 因此这里不 await、也不吞掉 rejection。
  void action(activeDatasetId)
  return true
}
