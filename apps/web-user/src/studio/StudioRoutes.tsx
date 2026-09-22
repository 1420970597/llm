import { Component, useEffect, useMemo, useState } from 'react'
import type { ErrorInfo, ReactNode } from 'react'
import { Navigate, Route, Routes, useLocation, useParams } from 'react-router-dom'
import { Button, Card, Typography } from '@douyinfe/semi-ui'
import { AlertTriangle } from 'lucide-react'
import { client } from '../lib/api'
import type { User } from '../lib/api'
import { CapabilityNotice } from './CapabilityNotice'
import { ProjectLayout } from './ProjectLayout'
import { StudioLayout } from './StudioLayout'
import { ProjectOverviewPage, ProjectsPage } from './pages/ProjectsPages'
import { NewProjectWizard } from './pages/NewProjectWizard'
import { BlueprintPage, CoveragePage, StandardPage } from './pages/BlueprintPages'
import { BatchDetailPage, BatchPlanningPage, FailuresPage, RunsPage } from './pages/RunPages'
import { SampleHistoryPage, SampleListPage, SampleReviewPage } from './pages/ReviewPages'
import { ComparePage } from './pages/ComparePage'
import { QualityListPage, QualityNewPage, QualityReportPage, RulesPage } from './pages/QualityPages'
import { DeliveriesPage, ReleaseCardPage, ReleaseNewPage, ReleasesListPage } from './pages/ReleasePages'
import {
  allStudioRoutes,
  auxiliaryRoutes,
  catalogRoutes,
  globalRoutes,
  isCatalogRouteMounted,
  projectDetailRoutes,
  projectRoutes,
  wizardRoutes,
  type StudioRouteMeta,
} from './routes'

/**
 * Atelier 路由树（Issue #160 T09）。
 *
 * 这一层是「元数据 → 路由」的唯一映射：**每个页面元素都由
 * `route.moduleStatus` 决定**，而不是由谁记得在 JSX 里写哪个组件。
 *
 *	available → 渲染注册表里的真实页面
 *	planned   → 渲染 CapabilityNotice（诚实说明由哪个任务交付）
 *
 * 为什么这样做（T09 验收项「未实现模块只显示诚实能力状态，不显示演示分数」）：
 * 手写 JSX 时，「新路由忘了接页面」会渲染一个空白页 —— 用户无法区分
 * 「没有数据」与「功能没做」。这里在**类型与运行期都兜住**：
 * 注册表少一个条目时，`assertAvailablePagesRegistered()` 会直接抛错，
 * 而生产上宁可显示「尚未交付」也不显示一个可疑的空页面。
 *
 * 目录评审页（`/catalog`）只在非生产构建挂载：它是设计/验收工具，
 * 带着原型静态数据，出现在生产里会被用户当成可用功能。
 */

/** 已交付页面组件的注册表。键必须与路由 `key` 一致。 */
const AVAILABLE_PAGES: Record<string, () => JSX.Element> = {
  projects: () => <ProjectsPage />,
  'project.overview': () => <ProjectOverviewPage />,
  'project.releases': () => <ReleasesListPage />,
  'project.newRelease': () => <ReleaseNewPage />,
  'project.releaseCard': () => <ReleaseCardPage />,
  deliveries: () => <DeliveriesPage />,
  'project.quality': () => <QualityListPage />,
  'project.qualityNew': () => <QualityNewPage />,
  'project.qualityReport': () => <QualityReportPage />,
  'project.rules': () => <RulesPage />,
  'project.compare': () => <ComparePage />,
  'project.data': () => <SampleListPage />,
  'project.review': () => <SampleListPage queueMode />,
  'project.sample': () => <SampleReviewPage />,
  'project.sampleHistory': () => <SampleHistoryPage />,
  'project.runs': () => <RunsPage />,
  'project.pilot': () => <BatchPlanningPage purpose="pilot" />,
  'project.runNew': () => <BatchPlanningPage purpose="scale" />,
  'project.runDetail': () => <BatchDetailPage />,
  'project.runFailures': () => <FailuresPage />,
  'project.blueprint': () => <BlueprintPage />,
  'project.coverage': () => <CoveragePage />,
  'project.standard': () => <StandardPage />,
  new: () => <WizardRoute step="basic" />,
  'new.coverage': () => <WizardRoute step="coverage" />,
  'new.quality': () => <WizardRoute step="quality" />,
}

/**
 * WizardRoute 把当前用户 ID 注入向导。
 *
 * 草稿按用户 ID 分键持久化（T10 验收项「草稿绑定当前用户，退出账号清理」），
 * 因此向导必须知道用户是谁。这里通过路径组件而不是全局单例注入：
 * 全局单例会让「切换账号后草稿仍是上一个人的」—— 那正是这条验收项要防的。
 */
function WizardRoute({ step }: { step: 'basic' | 'coverage' | 'quality' }) {
  const userId = useCurrentUserId()
  return <NewProjectWizard step={step} userId={userId} />
}

/**
 * useCurrentUserId 读取当前会话用户 ID。
 *
 * 为什么从路由布局外读而不是 prop 透传：向导是 `/new/*` 下的独立页面，
 * 与项目壳无父子关系。这里读的是登录时写入的会话缓存，它只用于
 * **草稿分键**（不是权限判定，权限一律由服务端判）。
 * 拿不到时返回 0 → 草稿键退化成 `u0`，仍是「确定的一把键」，
 * 不会读到别的用户的内容。
 */
function useCurrentUserId(): number {
  const [userId, setUserId] = useState(0)
  useEffect(() => {
    let cancelled = false
    void (async () => {
      try {
        const response = await client.get<{ user?: { id?: number } }>('/v1/auth/me')
        if (!cancelled) setUserId(response.data?.user?.id ?? 0)
      } catch {
        // 未登录/会话失效由壳层重定向处理，这里保持 0 即可。
      }
    })()
    return () => {
      cancelled = true
    }
  }, [])
  return userId
}

/**
 * 断言「所有 available 路由都注册了页面」。
 *
 * 在模块加载时检查（而不是等某个测试发现）：一个 available 但没有页面的
 * 路由会渲染空白内容，而那是最难排查的一种「看起来正常」的故障。
 */
function assertAvailablePagesRegistered(): void {
  const missing = allStudioRoutes
    .filter((route) => route.moduleStatus === 'available')
    .filter((route) => !AVAILABLE_PAGES[route.key])
    .map((route) => `${route.key}(${route.path})`)
  if (missing.length > 0) {
    throw new Error(`available 路由缺少页面组件：${missing.join('、')}`)
  }
}

assertAvailablePagesRegistered()

/** 把路由模式转成「嵌套在 ProjectLayout 下的相对路径」。 */
function relativeProjectPath(pattern: string): string {
  return pattern.replace('/p/:projectId/', '').replace('/p/:projectId', '')
}

export type StudioRouteTreeProps = {
  user: User | null
  onLogout: () => void
}

/**
 * studioRouteTree 返回全部 Atelier 路由元素。
 *
 * **为什么是函数而不是组件**（踩过一次的真实约束）：React Router v6 的
 * `<Routes>` 只接受 `<Route>` 或 `<React.Fragment>` 作为**直接子元素**，
 * 传入一个自定义组件会在运行期抛
 * `[X] is not a <Route> component`。因此调用方必须以
 * `{studioRouteTree(props)}` 的形式内联调用，让返回的 Fragment 成为
 * `<Routes>` 的直接子元素 —— 框架会把它摊平。
 *
 * 这个约束由 `test/l15_stage_routes.mjs` 在真实渲染里断言（它就是这么发现的），
 * 因此不要改成组件形式。
 *
 * 认证由 pathless 布局路由（AuthenticatedShell）承担，与 legacy 控制台的
 * `user ? ... : <Navigate to=/login>` 同一语义：未登录跳登录页，
 * 而不是渲染一个内容为空的主框架。
 */
export function studioRouteTree({ user, onLogout }: StudioRouteTreeProps) {
  return (
    <>
      <Route
        element={
          <AuthenticatedShell user={user} onLogout={onLogout}>
            <StudioLayout
              userEmail={user?.email ?? ''}
              isAdmin={user?.role === 'admin'}
              onLogout={onLogout}
            />
          </AuthenticatedShell>
        }
      >
        {/* 项目向导三步（W03–W05）：不是菜单项，但同样由元数据派生。 */}
        {wizardRoutes.map((route) => (
          <Route key={route.key} path={route.path} element={<ModuleElement route={route} />} />
        ))}

        {/* 全局四入口 */}
        {globalRoutes.map((route) => (
          <Route key={route.key} path={route.path} element={<ModuleElement route={route} />} />
        ))}

        {/* 辅助入口 */}
        {auxiliaryRoutes.map((route) => (
          <Route key={route.key} path={route.path} element={<ModuleElement route={route} />} />
        ))}

        {/* 目录评审：仅非生产构建挂载（生产不带 /catalog）。 */}
        {isCatalogRouteMounted()
          ? catalogRoutes.map((route) => (
              <Route key={route.key} path={route.path} element={<ModuleElement route={route} />} />
            ))
          : null}

        {/* 项目壳：六工作区 + 子页。`/p/:projectId` 本身重定向到概览。 */}
        <Route path="/p/:projectId" element={<ProjectLayout />}>
          <Route index element={<Navigate to="overview" replace />} />
          {projectRoutes.map((route) => (
            <Route
              key={route.key}
              path={relativeProjectPath(route.path)}
              element={<ModuleElement route={route} />}
            />
          ))}
          {projectDetailRoutes.map((route) => (
            <Route
              key={route.key}
              path={relativeProjectPath(route.path)}
              element={<ModuleElement route={route} />}
            />
          ))}
        </Route>
      </Route>
    </>
  )
}

/**
 * AuthenticatedShell 处理登录门禁与错误边界。
 *
 * 错误边界在这里而不是在每个页面：项目 ID 非法、概览接口返回意外结构
 * 这类错误发生在**壳层**（`useProjectScope` 会直接抛），
 * 没有边界时整页会白屏 —— 而白屏无法让用户知道下一步做什么。
 */
function AuthenticatedShell({
  user,
  onLogout,
  children,
}: {
  user: User | null
  onLogout: () => void
  children: ReactNode
}) {
  const location = useLocation()
  if (!user) {
    // 带上来源路径：登录后能回到用户原本要打开的对象（深链接语义）。
    const next = encodeURIComponent(`${location.pathname}${location.search}`)
    return <Navigate to={`/login?next=${next}`} replace />
  }
  return <StudioErrorBoundary onLogout={onLogout}>{children}</StudioErrorBoundary>
}

type StudioErrorBoundaryState = { message: string | null }

class StudioErrorBoundary extends Component<{ children: ReactNode; onLogout: () => void }, StudioErrorBoundaryState> {
  state: StudioErrorBoundaryState = { message: null }

  static getDerivedStateFromError(error: unknown): StudioErrorBoundaryState {
    return { message: error instanceof Error ? error.message : '页面渲染失败' }
  }

  componentDidCatch(error: Error, info: ErrorInfo) {
    // 只记录信息，不上报任何样本内容（T09 的「不含密钥/正文」要求）。
    console.error('studio shell error', error.message, info.componentStack)
  }

  render() {
    if (this.state.message === null) return this.props.children
    const { Title, Text } = Typography
    return (
      <Card className="console-card" bodyStyle={{ padding: 24 }}>
        <div className="flex items-start gap-3">
          <AlertTriangle size={20} className="mt-1 text-amber-500" aria-hidden />
          <div>
            <Title heading={5} className="!mb-1">
              这个地址打不开
            </Title>
            <Text type="tertiary" className="block">
              {this.state.message}
            </Text>
            <div className="mt-3 flex gap-2">
              <Button size="small" onClick={() => window.location.assign('/projects')}>
                返回数据项目
              </Button>
              <Button size="small" onClick={this.props.onLogout}>
                重新登录
              </Button>
            </div>
          </div>
        </div>
      </Card>
    )
  }
}

/** 按模块状态渲染真实页面或诚实的能力提示。 */
function ModuleElement({ route }: { route: StudioRouteMeta }) {
  const location = useLocation()
  const params = useParams()
  const page = useMemo(() => AVAILABLE_PAGES[route.key], [route.key])

  if (route.moduleStatus === 'available' && page) {
    return page()
  }
  return (
    <CapabilityNotice
      route={route}
      legacyHref={legacyHrefFor(route, params.projectId, location.search)}
    />
  )
}

/**
 * 过渡期内旧控制台里能做同样事情的入口（契约 §7 的兼容要求）。
 *
 * 只对**确实已有旧实现**的模块给出入口：写一个指向不存在页面的链接
 * 比不给链接更糟（用户点进去看到 404，会以为系统坏了）。
 */
function legacyHrefFor(route: StudioRouteMeta, projectId?: string, search = ''): string | undefined {
  if (!projectId) return undefined
  const withTask = (path: string) => `${path}?task=${projectId}${search ? `&${search.slice(1)}` : ''}`
  switch (route.key) {
    case 'project.runs':
    case 'project.pilot':
    case 'project.runNew':
      return withTask('/console/tasks')
    case 'project.data':
    case 'project.review':
      return withTask('/console/results')
    case 'project.quality':
    case 'project.rules':
    case 'project.qualityNew':
      return withTask('/console/evaluation')
    case 'project.blueprint':
    case 'project.coverage':
    case 'project.standard':
      return withTask('/console/domains')
    default:
      return undefined
  }
}

/**
 * StudioFallback 是 `/` 的落点。
 *
 * 落到 `/projects` 而不是 `/today`：今日工作（T27）尚未交付，
 * 让新用户一进来看到一个「尚未交付」的页面是最差的首次体验。
 */
export function studioDefaultPath(): string {
  return '/projects'
}

/** 供测试引用：当前构建下已挂载的目录评审路由数（生产必须为 0）。 */
export function mountedCatalogRouteCount(): number {
  return isCatalogRouteMounted() ? catalogRoutes.length : 0
}

/** 供测试引用：available 路由的键集合。 */
export function availableRouteKeys(): string[] {
  return allStudioRoutes.filter((route) => route.moduleStatus === 'available').map((route) => route.key)
}

// 保留 Routes 的引用以便本文件未来扩展为独立入口；当前由 App.tsx 的
// 外层 <Routes> 承载，因此这里显式标注它已被使用。
export const studioUsesOuterRoutes: typeof Routes = Routes
