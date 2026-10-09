import { useMemo } from 'react'
import { NavLink, Outlet, useLocation, useParams } from 'react-router-dom'
import { Typography } from '@douyinfe/semi-ui'
import {
  FileOutput,
  FlaskConical,
  GitBranch,
  LayoutDashboard,
  PlayCircle,
} from 'lucide-react'
import {
  fillRoutePath,
  fillRoutePathByKey,
  projectDetailRoutes,
  projectRoutes,
  projectWorkflowStages,
  projectWorkflowForPath,
  matchRoute,
} from './routes'
import { useProjectName } from './projectName'
import { parseProjectResourceId } from '../lib/api/studio'
import type { ProjectResourceId } from '../lib/api/studio'

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

/**
 * 用户看到的是一条生产主线，而不是内部模块目录。
 *
 * `projectRoutes` 仍保留完整的旧元数据供深链和契约测试使用；这里定义
 * 的五个入口是项目壳层唯一展示的工作流阶段。阶段内的素材、标准、规则
 * 和样本页面通过 `navParent` 归入对应阶段。
 */
const WORKFLOW_ICONS = { 'project.overview': LayoutDashboard, 'project.blueprint': GitBranch, 'project.runs': PlayCircle, 'project.review': FlaskConical, 'project.releases': FileOutput }

/** 项目作用域上下文的值。 */
export type ProjectScope = {
  projectId: ProjectResourceId
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
  const projectId = parseProjectResourceId(raw)
  if (!projectId) {
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

  const stage = useMemo(() => projectWorkflowForPath(location.pathname), [location.pathname])
  const currentRoute = matchRoute(location.pathname)
  const taskRoutes = stage.taskKeys.flatMap((key) => [...projectRoutes, ...projectDetailRoutes].filter((route) => route.key === key))

  const projectTitle = projectName ?? '项目'

  return (
    // key=projectId：切换项目时整棵子树重新挂载，上一个项目的在飞请求
    // 与本地状态一并丢弃（T09 验收项「切项目清除旧请求/缓存泄漏」）。
    <div className="project-layout atelier-project-shell" key={scope.projectId} data-studio-project-id={scope.projectId}>
      <header className="project-layout__header atelier-project-header">
        <Title heading={4} className="!mb-0">
          {projectTitle}
        </Title>
        <Text type="tertiary">{stage.label}</Text>
      </header>

      <nav className="project-layout__tabs atelier-project-tabs" aria-label="项目工作区">
        {projectWorkflowStages.map((tab, index) => {
          const Icon = WORKFLOW_ICONS[tab.key]
          return (
          <NavLink
            key={tab.key}
            to={scope.href(tab.key)}
            className={
              tab.key === stage.key ? 'project-tab project-tab--active' : 'project-tab'
            }
            aria-current={tab.key === stage.key ? 'page' : undefined}
            data-workflow-stage={index + 1}
          >
            {index > 0 ? <span className="project-tab__step">{index}</span> : null}
            <Icon size={15} aria-hidden />
            <span>{tab.label}</span>
          </NavLink>
        )})}
      </nav>

      {taskRoutes.length > 0 ? <nav className="project-task-navigation" aria-label={`${stage.label}任务`}>
        {taskRoutes.map((route) => <NavLink key={route.key} to={scope.href(route.key)}
          className={currentRoute?.key === route.key || currentRoute?.navParent === route.key ? 'is-active' : ''}
          aria-current={currentRoute?.key === route.key || currentRoute?.navParent === route.key ? 'page' : undefined}>
          {route.label}
        </NavLink>)}
      </nav> : null}

      <div className="project-layout__body">
        <Outlet context={scope} />
      </div>
    </div>
  )
}
