/**
 * Atelier 路由元数据（Issue #160 T09）。
 *
 * 契约来源：`docs/plans/atelier-implementation.md` §3（路由清单）、§3.1（导航层级）、
 * §3.2（URL 参数契约）。
 *
 * 本文件是 **路由 → 导航 → 面包屑 → 权限 → 实现状态** 的唯一来源。
 *
 * 为什么必须只有一份（读一下这段，它解释了这个文件存在的全部理由）：
 * 旧控制台的侧边栏归属、阶段路由表与面包屑各写了一份，三者互相漂移过 ——
 * issue #61 的根因就是「把阶段路由改成自指重定向，只改了路由表没改侧边栏归属」，
 * 结果是用户点进一个阶段后侧边栏高亮到不相关的条目。
 * 因此这里所有派生（导航分组、面包屑、当前项）都由同一份数组算出，
 * `test/l15_studio_shell.mjs` 会断言不存在第二张映射表。
 *
 * 另一条同样重要的约束：**未实现的模块只显示诚实的能力状态**（#160 T09 验收项
 * 「未实现模块只显示诚实能力状态，不显示演示分数」）。因此每条路由都带
 * `moduleStatus` 与 `task`（负责的任务号）—— 界面据此显示「该能力由 T14 交付，
 * 目前不可用」，而不是一个看起来能用的空页面或一个编出来的数字。
 */

export type StudioRouteKind =
  /** 全局入口（不依赖具体项目）。 */
  | 'global'
  /** 项目工作区（必须在 `/p/:projectId` 之下）。 */
  | 'project'
  /** 辅助入口（不与主流程争菜单位置）。 */
  | 'auxiliary'
  /** 仅设计/验收使用，**不进入生产菜单**。 */
  | 'catalog'

/** 模块实现状态。`planned` 的模块必须显示其负责的任务号，不得伪造数据。 */
export type StudioModuleStatus = 'available' | 'planned'

/** 路由要求的项目权限（与服务端 `AuthzAction` 同名，仅用于 UI 呈现）。 */
export type StudioRoutePermission = 'read' | 'design' | 'run' | 'review' | 'publish'

export type StudioRouteMeta = {
  /** 稳定键，用于导航选中与测试断言。 */
  key: string
  /** 路由模式（React Router 形式）。项目路由必须包含 `:projectId`。 */
  path: string
  /** 导航与标题用的中文标签。 */
  label: string
  /** 一句话说明这个页面做什么（悬停与空状态用）。 */
  caption: string
  kind: StudioRouteKind
  moduleStatus: StudioModuleStatus
  /** 负责交付该模块的任务号（planned 时界面必须显示）。 */
  task: string
  /** 该路由在侧边栏里的归属父项（阶段/子页用；为空表示自己是顶级项）。 */
  navParent?: string
  /** 进入该页所需的最低项目权限。 */
  permission?: StudioRoutePermission
}

/**
 * 全局四入口（契约 §3.1）。
 *
 * 顺序即菜单顺序：今日工作在最前（它是默认落点），
 * 数据项目是主线，方案库与交付库是横向复用入口。
 */
export const globalRoutes: StudioRouteMeta[] = [
  {
    key: 'today',
    path: '/today',
    label: '今日工作',
    caption: '待判断、可比较、失败恢复与候选阻塞',
    kind: 'global',
    moduleStatus: 'planned',
    task: 'T27',
    permission: 'read',
  },
  {
    key: 'projects',
    path: '/projects',
    label: '数据项目',
    caption: '按项目组织设计与运行',
    kind: 'global',
    moduleStatus: 'available',
    task: 'T10',
    permission: 'read',
  },
  {
    key: 'recipes',
    path: '/recipes',
    label: '方案库',
    caption: '可复用的覆盖/标准/质量组合',
    kind: 'global',
    moduleStatus: 'planned',
    task: 'T26',
    permission: 'read',
  },
  {
    key: 'deliveries',
    path: '/deliveries',
    label: '交付库',
    caption: '仅已发布且你有权访问的版本',
    kind: 'global',
    moduleStatus: 'planned',
    task: 'T22',
    permission: 'read',
  },
]

/**
 * 项目六工作区（契约 §3.1）。
 *
 * 顺序对应数据流：先设计与范围，再跑生产，然后看数据与质量，最后发布。
 * 「概览」在第一位，因为它是进入项目后的默认落点（P01）。
 */
export const projectRoutes: StudioRouteMeta[] = [
  {
    key: 'project.overview',
    path: '/p/:projectId/overview',
    label: '概览',
    caption: '真实版本、批次与下一决定',
    kind: 'project',
    moduleStatus: 'available',
    task: 'T10',
    permission: 'read',
  },
  {
    key: 'project.blueprint',
    path: '/p/:projectId/blueprint',
    label: '设计',
    caption: '蓝图节点、覆盖矩阵与思维标准',
    kind: 'project',
    moduleStatus: 'available',
    task: 'T11',
    permission: 'design',
  },
  {
    key: 'project.runs',
    path: '/p/:projectId/runs',
    label: '生产',
    caption: '批次进度、暂停与失败恢复',
    kind: 'project',
    moduleStatus: 'available',
    task: 'T13',
    permission: 'run',
  },
  {
    key: 'project.data',
    path: '/p/:projectId/data',
    label: '数据',
    caption: '样本内容、版本来源与审阅队列',
    kind: 'project',
    moduleStatus: 'available',
    task: 'T17',
    permission: 'read',
  },
  {
    key: 'project.quality',
    path: '/p/:projectId/quality',
    label: '质量',
    caption: '冻结论据、规则检查与人工判断',
    kind: 'project',
    moduleStatus: 'available',
    task: 'T19',
    permission: 'review',
  },
  {
    key: 'project.releases',
    path: '/p/:projectId/releases',
    label: '发布',
    caption: '候选门槛、不可变制品与交付',
    kind: 'project',
    moduleStatus: 'planned',
    task: 'T22',
    permission: 'publish',
  },
]

/**
 * 项目壳里的子页面（不是标签，靠标签页内的下钻进入）。
 *
 * `navParent` 指向所属标签，用于面包屑与侧边栏高亮 —— 这正是 issue #61
 * 的修复形态：归属只能有一个来源。
 */
export const projectDetailRoutes: StudioRouteMeta[] = [
  {
    key: 'project.pilot',
    path: '/p/:projectId/pilot',
    label: '小批试制',
    caption: '低成本验证方案',
    kind: 'project',
    moduleStatus: 'available',
    task: 'T13',
    navParent: 'project.runs',
    permission: 'run',
  },
  {
    key: 'project.compare',
    path: '/p/:projectId/compare',
    label: '试制对比',
    caption: '同基准 A/B 与采用方案',
    kind: 'project',
    moduleStatus: 'available',
    task: 'T18',
    navParent: 'project.runs',
    permission: 'run',
  },
  {
    key: 'project.runNew',
    path: '/p/:projectId/runs/new',
    label: '扩量规划',
    caption: '范围、预算与执行前核对',
    kind: 'project',
    moduleStatus: 'available',
    task: 'T13',
    navParent: 'project.runs',
    permission: 'run',
  },
  {
    key: 'project.sample',
    path: '/p/:projectId/data/:sampleId',
    label: '三栏审阅',
    caption: '队列、只读内容与版本化证据三栏独立',
    kind: 'project',
    moduleStatus: 'available',
    task: 'T17',
    navParent: 'project.data',
    permission: 'read',
  },
  {
    key: 'project.sampleHistory',
    path: '/p/:projectId/data/:sampleId/history',
    label: '版本与来源',
    caption: '内容只追加，每版记录引用的标准与蓝图 hash',
    kind: 'project',
    moduleStatus: 'available',
    task: 'T17',
    navParent: 'project.data',
    permission: 'read',
  },
  {
    key: 'project.runDetail',
    path: '/p/:projectId/runs/:batchId',
    label: '批次详情',
    caption: '配置快照、阶段进度与事件时间线',
    kind: 'project',
    moduleStatus: 'available',
    task: 'T13',
    navParent: 'project.runs',
    permission: 'run',
  },
  {
    key: 'project.runFailures',
    path: '/p/:projectId/runs/:batchId/failures',
    label: '异常恢复',
    caption: '失败单元、错误类别与恢复入口',
    kind: 'project',
    moduleStatus: 'available',
    task: 'T13',
    navParent: 'project.runs',
    permission: 'run',
  },
  {
    key: 'project.coverage',
    path: '/p/:projectId/coverage',
    label: '覆盖矩阵',
    caption: '领域、方向与配额',
    kind: 'project',
    moduleStatus: 'available',
    task: 'T11',
    navParent: 'project.blueprint',
    permission: 'design',
  },
  {
    key: 'project.standard',
    path: '/p/:projectId/standard',
    label: '思维标准',
    caption: '可排序步骤与检查点',
    kind: 'project',
    moduleStatus: 'available',
    task: 'T11',
    navParent: 'project.blueprint',
    permission: 'design',
  },
  {
    key: 'project.review',
    path: '/p/:projectId/review',
    label: '审阅队列',
    caption: '三栏审阅与判断',
    kind: 'project',
    moduleStatus: 'available',
    task: 'T17',
    navParent: 'project.data',
    permission: 'review',
  },
  {
    key: 'project.qualityNew',
    path: '/p/:projectId/quality/new',
    label: '新建实验',
    caption: '固定范围与量表',
    kind: 'project',
    moduleStatus: 'available',
    task: 'T19',
    navParent: 'project.quality',
    permission: 'review',
  },
  {
    key: 'project.qualityReport',
    path: '/p/:projectId/quality/:experimentId',
    label: '质量报告',
    caption: '固定范围与分母、缺分与裁判分歧、证据链接',
    kind: 'project',
    moduleStatus: 'available',
    task: 'T19',
    navParent: 'project.quality',
    permission: 'read',
  },
  {
    key: 'project.rules',
    path: '/p/:projectId/rules',
    label: '清洗策略',
    caption: '规则预览与命中证据',
    kind: 'project',
    moduleStatus: 'available',
    task: 'T15',
    navParent: 'project.quality',
    permission: 'review',
  },
  {
    key: 'project.newRelease',
    path: '/p/:projectId/releases/new',
    label: '准备发布',
    caption: '范围、用途与限制',
    kind: 'project',
    moduleStatus: 'planned',
    task: 'T20',
    navParent: 'project.releases',
    permission: 'publish',
  },
]

/**
 * 项目向导的三步（W03–W05）。
 *
 * 刻意**不是**导航项：用户从「数据项目」页的「新建项目」进入，
 * 不存在「直接跳到第二步」这种独立入口。但它们仍然需要元数据 ——
 * 面包屑、权限与实现状态都由同一份表派生，否则这三页会成为
 * 「元数据之外的例外」，而那正是漂移的起点。
 */
export const wizardRoutes: StudioRouteMeta[] = [
  {
    key: 'new',
    path: '/new',
    label: '新建项目',
    caption: '意图与目标类型',
    kind: 'global',
    moduleStatus: 'available',
    task: 'T10',
    permission: 'design',
  },
  {
    key: 'new.coverage',
    path: '/new/coverage',
    label: '目标量与覆盖',
    caption: 'n × m × x 与试制数量',
    kind: 'global',
    moduleStatus: 'available',
    task: 'T10',
    navParent: 'new',
    permission: 'design',
  },
  {
    key: 'new.quality',
    path: '/new/quality',
    label: '质量与预算',
    caption: '接纳率目标与预算上限',
    kind: 'global',
    moduleStatus: 'available',
    task: 'T10',
    navParent: 'new',
    permission: 'design',
  },
]

/**
 * 辅助入口（契约 §3.1）。刻意不与主流程争菜单位置。
 */
export const auxiliaryRoutes: StudioRouteMeta[] = [
  {
    key: 'activity',
    path: '/activity',
    label: '动态',
    caption: '事件、评论与未读水位',
    kind: 'auxiliary',
    moduleStatus: 'planned',
    task: 'T27',
    permission: 'read',
  },
  {
    key: 'settings.connections',
    path: '/settings/connections',
    label: '连接设置',
    caption: '模型连接、存储与预算',
    kind: 'auxiliary',
    moduleStatus: 'planned',
    task: 'T28',
    permission: 'read',
  },
  {
    key: 'settings.team',
    path: '/settings/team',
    label: '成员与角色',
    caption: '项目成员与工作区治理',
    kind: 'auxiliary',
    moduleStatus: 'planned',
    task: 'T28',
    permission: 'read',
  },
  {
    key: 'help',
    path: '/help',
    label: '帮助',
    caption: '旅程、术语与快捷键',
    kind: 'auxiliary',
    moduleStatus: 'planned',
    task: 'T28',
    permission: 'read',
  },
]

/**
 * 仅设计/验收使用的目录评审页（契约 §3 的 S05）。
 *
 * **不进入生产菜单**：`isCatalogRouteVisible()` 在 `import.meta.env.PROD`
 * 下恒为 false。这里仍然声明它，是为了让「它存在但不在菜单里」这件事
 * 是**数据**而不是散落在 JSX 里的条件 —— 后者会让「生产里到底挂没挂」
 * 无法被测试断言。
 */
export const catalogRoutes: StudioRouteMeta[] = [
  {
    key: 'catalog',
    path: '/catalog',
    label: '目录评审',
    caption: '设计/验收工具，不进入生产菜单',
    kind: 'catalog',
    moduleStatus: 'planned',
    task: 'T01、T09',
  },
]

/** 全部路由（含目录评审与向导步骤），用于注册、匹配与实际渲染。 */
export const allStudioRoutes: StudioRouteMeta[] = [
  // 向导步骤放在最前：`/new` 与 `/new/coverage` 段数不同，matchRoute 按
  // 段数精确匹配，因此顺序不影响结果；放在前面只是让「入口优先」可见。
  ...wizardRoutes,
  ...globalRoutes,
  ...projectRoutes,
  ...projectDetailRoutes,
  ...auxiliaryRoutes,
  ...catalogRoutes,
]

/**
 * 导航可见的路由（生产构建下不含目录评审）。
 *
 * 向导步骤也被排除：它们是「新建项目」的下钻页，不是菜单项 ——
 * 出现在侧边栏里会让「第一步」看起来像一个常驻工作区。
 */
export function navVisibleRoutes(): StudioRouteMeta[] {
  return allStudioRoutes.filter((route) => route.kind !== 'catalog')
}

/** 侧边栏菜单项（全局入口 + 辅助入口），由元数据派生。 */
export function menuRoutes(): StudioRouteMeta[] {
  const wizardKeys = new Set(wizardRoutes.map((route) => route.key))
  return navVisibleRoutes().filter((route) => !wizardKeys.has(route.key))
}

/**
 * 目录评审页是否应该被挂载。
 *
 * 生产构建里恒不挂载：它是设计期的自检工具，带着原型静态数据，
 * 出现在生产菜单里会让用户以为自己能用（#160 T09 验收项
 * 「生产不带 /catalog、重置演示、身份切换控件」）。
 *
 * **防御式读取** `import.meta.env`（与 buildInfo.ts 的同一处修复，issue #112）：
 * 它是 Vite 注入的对象，**只在 Vite 构建里存在**。仓库的 UI 守卫
 *（test/l15_stage_routes.mjs 等）用 esbuild + React 渲染真实组件树，
 * 而 esbuild 不注入它 —— 直接访问 `.PROD` 会抛
 * `Cannot read properties of undefined`，把整个渲染腿打挂。
 * 这在本次改动里确实发生了（守卫当场报错），因此这里必须在访问前判断宿主。
 *
 * 语义：拿不到宿主时按**开发**处理（挂载目录评审页），因为「拿不到环境」
 * 只可能发生在本地/测试渲染，而把设计工具误挂到生产是更严重的错误 ——
 * 那一侧由构建产物断言（守卫的第 2 层）兜住。
 */
export function isCatalogRouteMounted(): boolean {
  const env = (import.meta as { env?: { PROD?: boolean } }).env
  if (!env || typeof env !== 'object') return true
  return !env.PROD
}

/**
 * 按当前 pathname 找最匹配的路由元数据。
 *
 * 匹配规则：把 `:param` 段与任意非空段对齐，取**最长**匹配。
 * 为什么取最长：`/p/1/runs` 与 `/p/1/runs/new` 都能与 `/p/:projectId/runs`
 * 的模式前缀匹配，只有最长匹配才能让「扩量规划」不会把侧边栏高亮到「生产」。
 */
export function matchRoute(pathname: string): StudioRouteMeta | undefined {
  const segments = splitPath(pathname)
  let best: StudioRouteMeta | undefined
  let bestScore = -1
  for (const route of allStudioRoutes) {
    const pattern = splitPath(route.path)
    if (pattern.length !== segments.length) continue
    let matched = true
    for (let index = 0; index < pattern.length; index += 1) {
      const expected = pattern[index]
      if (expected.startsWith(':')) continue
      if (expected !== segments[index]) {
        matched = false
        break
      }
    }
    if (!matched) continue
    if (pattern.length > bestScore) {
      best = route
      bestScore = pattern.length
    }
  }
  return best
}

/** 拆路径为段（忽略空段与末尾斜杠）。 */
function splitPath(path: string): string[] {
  return path.split('?')[0].split('#')[0].split('/').filter(Boolean)
}

/**
 * 把路由模式填上参数，得到可跳转的路径。
 *
 * 前端**不自行拼 URL**（契约 §1.1 的同一原则）：所有跳转都经过这里，
 * 于是路由规则变化只需要改元数据一处。
 */
export function fillRoutePath(pattern: string, params: Record<string, string | number>): string {
  return pattern
    .split('/')
    .map((segment) => {
      if (!segment.startsWith(':')) return segment
      const name = segment.slice(1)
      const value = params[name]
      return value === undefined || value === null ? '' : String(value)
    })
    .filter((segment) => segment !== '')
    .join('/')
    .replace(/^/, '/')
}

/**
 * 按路由 key 填充路径。
 *
 * 组件里**不得**出现 `'/p/:projectId/...'` 这类字面量：那等于在元数据之外
 * 又存了一份路由事实，而两份事实必然漂移。所有跳转都经过这个函数或
 * `projectHref`，于是改路由只需要改元数据一处。
 */
export function fillRoutePathByKey(key: string, params: Record<string, string | number>): string {
  const route = allStudioRoutes.find((item) => item.key === key)
  if (!route) {
    throw new Error(`未知的路由键：${key}（跳转目标必须来自 routes.ts 的元数据）`)
  }
  return fillRoutePath(route.path, params)
}

/**
 * 面包屑：从顶级入口一路到当前页。
 *
 * 由元数据派生而**不是**每个页面自己声明：前者不可能漂移，
 * 后者在新增子页时必然有人忘记加面包屑项。
 */
export type BreadcrumbItem = {
  label: string
  path?: string
}

export function breadcrumbsFor(pathname: string, params: Record<string, string | number>): BreadcrumbItem[] {
  const current = matchRoute(pathname)
  if (!current) {
    return [{ label: '数据项目', path: fillRoutePath('/projects', {}) }]
  }
  if (current.kind === 'project') {
    const items: BreadcrumbItem[] = [
      { label: '数据项目', path: '/projects' },
      {
        label: `项目 ${params.projectId ?? ''}`.trim(),
        path: fillRoutePath('/p/:projectId/overview', params),
      },
    ]
    // 子页（navParent 非空）先给出所属标签，再给出自己。
    if (current.navParent) {
      const parent = allStudioRoutes.find((route) => route.key === current.navParent)
      if (parent) items.push({ label: parent.label, path: fillRoutePath(parent.path, params) })
    }
    // 用 `items[items.length - 1]` 而不是 `items.at(-1)`：tsconfig 的
    // target/lib 是 ES2020，`.at()` 需要 ES2022 —— 用它会让 tsc 直接报错，
    // 而「为了过风格建议改坏构建」是本末倒置。
    if (items[items.length - 1]?.label !== current.label) {
      items.push({ label: current.label })
    }
    return items
  }
  return [{ label: current.label }]
}

/**
 * 当前 pathname 下侧边栏应高亮的项。
 *
 * 子页高亮到自己的 `navParent`（issue #61 的修复形态）：
 * 「扩量规划」页高亮「生产」，而不是让侧边栏掉回默认项。
 */
export function activeNavKey(pathname: string): string {
  const current = matchRoute(pathname)
  if (!current) return ''
  return current.navParent ?? current.key
}
