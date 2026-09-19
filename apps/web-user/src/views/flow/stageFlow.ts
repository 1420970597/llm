/**
 * 任务阶段流转的单一事实来源（single source of truth）。
 *
 * 解决的问题（issue #90「页面设计说明书」）：
 *   同一个 5 步流程，在三处入口指向了不同的页面：
 *
 *   | 导航入口        | 「质量评估」指向        | 「导出」指向        |
 *   |----------------|----------------------|-------------------|
 *   | 侧边栏          | /console/evaluation  | 无（在数据资产内）   |
 *   | 任务详情阶段卡片  | /console/rewards     | /console/exports  |
 *   | 数据清洗流程条    | /console/evaluation  | /console/results  |
 *
 *   并且侧边栏的「质量评估」(`/console/evaluation`，多模型互评) 与阶段页的
 *   「第 4 步：质量评估」(`/console/rewards`，流水线自动评分) **同名但不同页**，
 *   用户无法判断何时该用哪个。
 *
 * 本文件把「流水线的 5 个阶段」定义成唯一一份有序声明，并提供：
 *   - 阶段之间的前进关系（供阶段页渲染「下一步」按钮，解决 #84 的 5 次往返）
 *   - 每个阶段的权威路由（供侧边栏 / 阶段卡片 / 流程条统一取用，消除三处漂移）
 *
 * 注意：本文件只描述**任务流水线内部**的阶段。`/console/evaluation`（多模型互评）
 * 是独立于单任务流水线的分析工具，不属于这条链，因此不在本表内——这正是要消除
 * 「同名不同页」混淆的关键：两者在概念上必须分开命名与导航。
 */

/** 流水线阶段的稳定标识。 */
export type StageKey = 'domains' | 'questions' | 'reasoning' | 'rewards' | 'export'

/** 单个阶段的声明。 */
export type StageDefinition = {
  /** 阶段标识 */
  key: StageKey
  /** 步骤序号，从 1 开始（用于「第 N 步」文案） */
  step: number
  /** 阶段短名，用于侧边栏 / 卡片标题，如「主题结构」 */
  label: string
  /** 阶段页路由（权威来源，所有入口都必须用它） */
  route: string
  /** 该阶段的主操作按钮文案，如「生成方向结构」 */
  actionLabel: string
  /** 该阶段处于哪个任务状态下表示「已完成」 */
  doneStatuses: string[]
  /** 该阶段处于哪个任务状态下表示「进行中/排队中」 */
  activeStatuses: string[]
  /** 该阶段处于哪个任务状态下表示「失败」 */
  failedStatuses: string[]
}

/**
 * 流水线 5 个阶段，**顺序即流转顺序**。
 *
 * doneStatuses / activeStatuses / failedStatuses 取自后端 datasets.status 的既有取值，
 * 与 apps/web-user/src/App.tsx 中 statusLabel/statusToActionRoute 的判定保持一致。
 */
export const PIPELINE_STAGES: readonly StageDefinition[] = [
  {
    key: 'domains',
    step: 1,
    label: '主题结构',
    route: '/console/domains',
    actionLabel: '生成方向结构',
    doneStatuses: [
      'domains_confirmed',
      'questions_queued',
      'questions_generated',
      'reasoning_queued',
      'reasoning_generated',
      'rewards_queued',
      'rewards_generated',
      'export_queued',
      'export_generated',
    ],
    activeStatuses: ['draft'],
    failedStatuses: [],
  },
  {
    key: 'questions',
    step: 2,
    label: '问题生成',
    route: '/console/questions',
    actionLabel: '开始生成题目',
    doneStatuses: [
      'questions_generated',
      'reasoning_queued',
      'reasoning_generated',
      'rewards_queued',
      'rewards_generated',
      'export_queued',
      'export_generated',
    ],
    activeStatuses: ['questions_queued'],
    failedStatuses: ['questions_failed'],
  },
  {
    key: 'reasoning',
    step: 3,
    label: '答案内容',
    route: '/console/reasoning',
    actionLabel: '开始生成答案',
    doneStatuses: [
      'reasoning_generated',
      'rewards_queued',
      'rewards_generated',
      'export_queued',
      'export_generated',
    ],
    activeStatuses: ['reasoning_queued'],
    failedStatuses: ['reasoning_failed', 'reasoning_partial'],
  },
  {
    key: 'rewards',
    step: 4,
    label: '质量评分',
    route: '/console/rewards',
    actionLabel: '开始质量评估',
    doneStatuses: ['rewards_generated', 'export_queued', 'export_generated'],
    activeStatuses: ['rewards_queued'],
    failedStatuses: ['rewards_failed', 'rewards_partial'],
  },
  {
    key: 'export',
    step: 5,
    label: '导出交付',
    route: '/console/exports',
    actionLabel: '开始导出结果',
    doneStatuses: ['export_generated'],
    activeStatuses: ['export_queued'],
    failedStatuses: ['export_failed'],
  },
] as const

/** 阶段总数，用于「第 N 步（共 M 步）」文案。 */
export const PIPELINE_STAGE_COUNT = PIPELINE_STAGES.length

/** 按 key 取阶段定义。 */
export function stageByKey(key: StageKey): StageDefinition | undefined {
  return PIPELINE_STAGES.find((stage) => stage.key === key)
}

/** 按路由取阶段定义（阶段页据此判断自己在流水线中的位置）。 */
export function stageByRoute(route: string): StageDefinition | undefined {
  return PIPELINE_STAGES.find((stage) => stage.route === route)
}

/**
 * 取某阶段的下一阶段。最后一个阶段返回 undefined。
 *
 * 供阶段页渲染「下一步」按钮使用（issue #84：5 个阶段页原本都没有前进入口，
 * 用户必须绕回任务详情页，造成 5 次往返）。
 */
export function nextStageOf(key: StageKey): StageDefinition | undefined {
  const index = PIPELINE_STAGES.findIndex((stage) => stage.key === key)
  if (index < 0 || index >= PIPELINE_STAGES.length - 1) return undefined
  return PIPELINE_STAGES[index + 1]
}

/** 取某阶段的前一阶段。第一个阶段返回 undefined。 */
export function prevStageOf(key: StageKey): StageDefinition | undefined {
  const index = PIPELINE_STAGES.findIndex((stage) => stage.key === key)
  if (index <= 0) return undefined
  return PIPELINE_STAGES[index - 1]
}

/**
 * 判断某阶段在给定任务状态下是否已完成。
 *
 * 用于阶段页决定「下一步」按钮是否可点（未完成时不应放行，否则用户会撞到 409）。
 */
export function isStageDone(key: StageKey, datasetStatus: string | undefined): boolean {
  const stage = stageByKey(key)
  if (!stage || !datasetStatus) return false
  return stage.doneStatuses.includes(datasetStatus)
}

/** 判断某阶段是否失败。 */
export function isStageFailed(key: StageKey, datasetStatus: string | undefined): boolean {
  const stage = stageByKey(key)
  if (!stage || !datasetStatus) return false
  return stage.failedStatuses.includes(datasetStatus)
}

/**
 * 根据任务状态推断「当前应该做哪个阶段」。
 *
 * 这是 statusToActionRoute 的声明式替代：原本是 App.tsx 里一个 20 行的 switch，
 * 与阶段表各写一份、容易漂移。改为从阶段声明推导后，新增阶段只需改一处。
 *
 * 规则：按顺序找第一个「未完成」的阶段；全部完成则停在最后一个阶段（导出）。
 * 失败状态视为「未完成」，因此失败后会回到该阶段重试——这与既有
 * statusToActionRoute 把 *_failed 映射回对应阶段页的行为一致。
 */
export function currentStageFor(datasetStatus: string | undefined): StageDefinition {
  if (!datasetStatus) return PIPELINE_STAGES[0]
  for (const stage of PIPELINE_STAGES) {
    if (!isStageDone(stage.key, datasetStatus)) return stage
  }
  return PIPELINE_STAGES[PIPELINE_STAGES.length - 1]
}
