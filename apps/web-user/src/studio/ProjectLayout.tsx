import { useMemo } from 'react'
import { NavLink, Outlet, useLocation, useParams } from 'react-router-dom'
import { Typography } from '@douyinfe/semi-ui'
import {
  Boxes,
  FileOutput,
  FlaskConical,
  GitBranch,
  LayoutDashboard,
  PlayCircle,
} from 'lucide-react'
import type { LucideIcon } from 'lucide-react'
import {
  fillRoutePath,
  fillRoutePathByKey,
  projectDetailRoutes,
  projectRoutes,
} from './routes'
import { useProjectName } from './projectName'

/**
 * 项目壳（Issue #160 T09）：六项目工作区标签 + 面包屑 + 项目作用域。
 *
 * 这一层承担 T09 最容易被忽视、但后果最重的一条验收项：
 * **「粘贴深链接、刷新、前进/后退、开新标签不依赖 activeDatasetId」**。
 *
 * 做法是把项目 ID 的**唯一来源**固定为路由参数：
 *   * 本组件从 `useParams()` 取 `projectId`，并通过 `ProjectScopeProvider`
 *     只向下传递它；
 *   * 子树用 `key={projectId}` 挂载，于是切换项目会**卸载**上一个项目的
 *     全部状态（包括所有在飞请求的持有者）—— 这是「切项目清除旧请求/
 *     缓存泄漏」的结构性保证，而不是靠每个页面记得清理。
 *
 * 与旧控制台的关系：旧壳把「当前任务」放在一个全局 state 里，页面靠它取数，
 * 于是直接访问阶段路由会显示「当前任务：未选择」而内容为空
 *（issue #61/#104 的形态）。新壳不允许这种「页面依赖一个全局选中态」的写法：
 * 项目身份的**唯一**来源是路由参数，因此本文件里不出现任何全局任务选中态。
 */

const PROJECT_TAB_ICONS: Record<string, LucideIcon> = {
  'project.overview': LayoutDashboard,
  'project.blueprint': GitBranch,
  'project.runs': PlayCircle,
  'project.data': Boxes,
  'project.quality': FlaskConical,
  'project.releases': FileOutput,
}

/** 项目作用域上下文的值。 */
export type ProjectScope = {
  projectId: number
  /** 项目内链接构造器：`projectHref('project.data')`。 */
  href: (key: string) => string
}

/**
 * useProjectScope 从**路由**解析项目作用域。
 *
 * 导出成 hook 而不是把 projectId 塞进一个全局 store：全局 store 正是
 * 「深链接拿不到项目」的成因。这里每次渲染都从路由读，因此刷新、
 * 前进/后退、新标签打开都能拿到同一个值。
 */
export function useProjectScope(): ProjectScope {
  const params = useParams()
  const raw = params.projectId ?? ''
  const projectId = Number.parseInt(raw, 10)
  if (!Number.isFinite(projectId) || projectId <= 0) {
    // 非法项目 ID 不应该让页面渲染一个「看不见的错误」：
    // 抛出让 ErrorBoundary 显示明确的「地址不正确」文案。
    throw new Error(`项目地址不正确：${raw || '(空)'}`)
  }
  return useMemo(
    () => ({
      projectId,
      href: (key: string) => {
        // 子页与标签同源查找（都来自 routes.ts），因此这里不可能出现
        // 「链接指向一个元数据里不存在的键」—— 那正是漂移的形态。
        const route = [...projectRoutes, ...projectDetailRoutes].find((item) => item.key === key)
        // 退回概览而不是报错：`href('不存在的键')` 不该让整个页面崩掉，
        // 但也不该静默跳到一个无关页面 —— 概览是「项目首页」这一唯一合理解释。
        return route ? fillRoutePath(route.path, { projectId }) : fillRoutePathByKey('project.overview', { projectId })
      },
    }),
    [projectId],
  )
}

export function ProjectLayout() {
  const scope = useProjectScope()
  const location = useLocation()
  const { Title, Text } = Typography
  const projectName = useProjectName(scope.projectId)

  const activeTab = useMemo(() => {
    const meta = tabForPath(location.pathname)
    return meta?.key ?? 'project.overview'
  }, [location.pathname])

  const projectTitle = projectName ?? '项目'

  return (
    // key=projectId：切换项目时整棵子树重新挂载，上一个项目的在飞请求
    // 与本地状态一并丢弃（T09 验收项「切项目清除旧请求/缓存泄漏」）。
    <div className="project-layout atelier-project-shell" key={scope.projectId} data-studio-project-id={scope.projectId}>
      <header className="project-layout__header atelier-project-header">
        <Title heading={4} className="!mb-0">
          {projectTitle}
        </Title>
        <Text type="tertiary">{activeTabCaption(activeTab)}</Text>
      </header>

      <nav className="project-layout__tabs atelier-project-tabs" aria-label="项目工作区">
        {projectRoutes.map((route) => (
          <NavLink
            key={route.key}
            to={fillRoutePath(route.path, { projectId: scope.projectId })}
            className={
              route.key === activeTab ? 'project-tab project-tab--active' : 'project-tab'
            }
            aria-current={route.key === activeTab ? 'page' : undefined}
          >
            {renderTabIcon(route.key)}
            <span>{route.label}</span>
            {route.moduleStatus === 'planned' ? (
              <span className="project-tab__badge" title={`由 ${route.task} 交付`}>
                待交付
              </span>
            ) : null}
          </NavLink>
        ))}
      </nav>

      <div className="project-layout__body">
        <Outlet context={scope} />
      </div>
    </div>
  )
}

function renderTabIcon(key: string) {
  const Icon = PROJECT_TAB_ICONS[key]
  if (!Icon) return null
  return <Icon size={15} aria-hidden />
}

/**
 * 当前 pathname 属于哪个标签。
 *
 * 先看精确元数据（子页已经把 navParent 指到所属标签），再退回「项目级
 * 路径的第一段」匹配。两层都失败时默认概览 —— 但**不静默**：
 * 返回 undefined 会让标签全部不高亮，而那种界面状态无法解释；
 * 默认概览至少与 URL 的语义一致（`/p/:id` 就是概览）。
 */
function tabForPath(pathname: string): { key: string; caption: string } | undefined {
  const meta = routeMetaByPathSegment(pathname)
  if (!meta) return undefined
  const tabKey = meta.navParent ?? meta.key
  const tab = projectRoutes.find((route) => route.key === tabKey)
  if (!tab) return { key: meta.key, caption: meta.caption }
  return { key: tab.key, caption: tab.caption }
}

function routeMetaByPathSegment(pathname: string) {
  const segments = pathname.split('?')[0].split('/').filter(Boolean)
  // `/p/:projectId/<segment>` → 用第二段（segment）在元数据里找同前缀的项。
  if (segments.length < 3) return undefined
  const tail = segments.slice(2).join('/')
  const candidates = [...projectRoutes, ...projectDetailRoutes]
  return candidates.find((route) => route.path.endsWith(`/${tail}`)) ??
    candidates.find((route) => route.path.endsWith(`/${segments[2]}`))
}

function activeTabCaption(key: string): string {
  const tab = projectRoutes.find((route) => route.key === key)
  return tab?.caption ?? ''
}
