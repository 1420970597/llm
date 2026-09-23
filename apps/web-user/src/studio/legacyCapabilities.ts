/**
 * Legacy console capabilities kept discoverable during the Atelier migration.
 *
 * These links are intentionally metadata rather than JSX in App.tsx: the
 * compatibility page and its static guard must be able to prove that every
 * old user, stage, result, and admin entry still has a visible destination.
 */
export type LegacyRouteGroup = 'user' | 'task' | 'result' | 'admin'
export type LegacyRouteVisibility = 'discoverable' | 'alias' | 'detail'

export type LegacyRouteMeta = {
  key: string
  route: string
  label: string
  caption: string
  group: LegacyRouteGroup
  /** `native` is reserved for future Atelier-native entries; old links are compat. */
  status: 'native' | 'compat'
  adminOnly?: boolean
  visibility?: LegacyRouteVisibility
  /** Safe destination when the original route needs a selected task/context. */
  indexHref?: string
  /** Atelier-native destination when the capability has been migrated. */
  nativeHref?: string
  note?: string
}

export const legacyRoutes: LegacyRouteMeta[] = [
  { key: 'legacy.home', route: '/console/home', label: '工作台', caption: '待办与进展', group: 'user', status: 'compat' },
  { key: 'legacy.overviewAlias', route: '/console/overview', label: '工作台旧别名', caption: '兼容旧书签并转到工作台', group: 'user', status: 'compat', visibility: 'alias', indexHref: '/console/home' },
  { key: 'legacy.planning', route: '/console/planning', label: '新建任务', caption: '创建新任务', group: 'user', status: 'compat' },
  { key: 'legacy.tasks', route: '/console/tasks', label: '我的任务', caption: '查看任务', group: 'user', status: 'compat' },
  { key: 'legacy.taskDetail', route: '/console/tasks/:taskId', label: '任务详情', caption: '从任务列表打开具体任务', group: 'user', status: 'compat', visibility: 'detail', indexHref: '/console/tasks', note: '需要先在我的任务中选择具体任务。' },
  { key: 'legacy.results', route: '/console/results', label: '数据资产', caption: '结果与交付文件', group: 'user', status: 'compat' },
  { key: 'legacy.evaluation', route: '/console/evaluation', label: '质量评估', caption: '多模型互评与打分', group: 'user', status: 'compat', nativeHref: '/tools/evaluation' },
  { key: 'legacy.cleaning', route: '/console/cleaning', label: '数据清洗', caption: '拒答与异常拦截', group: 'user', status: 'compat', nativeHref: '/tools/cleaning' },
  { key: 'legacy.help', route: '/console/help', label: '账户与帮助', caption: '帮助与恢复', group: 'user', status: 'compat' },
  { key: 'legacy.domains', route: '/console/domains', label: '主题结构', caption: '生成并确认主题结构', group: 'task', status: 'compat', indexHref: '/console/tasks', note: '先从我的任务选择任务，再进入主题结构。' },
  { key: 'legacy.questions', route: '/console/questions', label: '问题生成', caption: '查看问题覆盖', group: 'result', status: 'compat', indexHref: '/console/tasks', note: '先从我的任务选择任务，再进入问题生成。' },
  { key: 'legacy.reasoning', route: '/console/reasoning', label: '答案内容', caption: '查看答案完整性', group: 'result', status: 'compat', indexHref: '/console/tasks', note: '先从我的任务选择任务，再进入答案内容。' },
  { key: 'legacy.rewards', route: '/console/rewards', label: '质量评估', caption: '查看评分状态', group: 'result', status: 'compat', indexHref: '/console/tasks', note: '先从我的任务选择任务，再进入质量评估。' },
  { key: 'legacy.exports', route: '/console/exports', label: '导出交付', caption: '查看导出与交付', group: 'result', status: 'compat', indexHref: '/console/tasks', note: '先从我的任务选择任务，再进入导出交付。' },
  { key: 'legacy.operations', route: '/console/operations', label: '运营监控', caption: '查看队列与运行状态', group: 'admin', status: 'compat', adminOnly: true },
  { key: 'legacy.providers', route: '/console/admin/providers', label: 'AI 服务', caption: '管理 AI 服务', group: 'admin', status: 'compat', adminOnly: true },
  { key: 'legacy.storage', route: '/console/admin/storage', label: '结果存储', caption: '管理结果存储', group: 'admin', status: 'compat', adminOnly: true },
  { key: 'legacy.strategies', route: '/console/admin/strategies', label: '生成规则', caption: '管理生成规则', group: 'admin', status: 'compat', adminOnly: true },
  { key: 'legacy.prompts', route: '/console/admin/prompts', label: '生成指令', caption: '管理模板与版本', group: 'admin', status: 'compat', adminOnly: true },
  { key: 'legacy.audit', route: '/console/admin/audit', label: '操作记录', caption: '查看变更记录', group: 'admin', status: 'compat', adminOnly: true },
]

export const legacyRouteGroups: Array<{ key: LegacyRouteGroup; label: string }> = [
  { key: 'user', label: '基础工作' },
  { key: 'task', label: '任务阶段' },
  { key: 'result', label: '结果阶段' },
  { key: 'admin', label: '系统管理' },
]
