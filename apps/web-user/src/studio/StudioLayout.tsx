import { useEffect, useMemo, useState } from 'react'
import { Link, NavLink, Outlet, useLocation, useNavigate } from 'react-router-dom'
import { Avatar, Button, Typography } from '@douyinfe/semi-ui'
import {
  Activity,
  BookOpen,
  ChevronRight,
  Compass,
  FolderCog,
  HardDriveDownload,
  LayoutDashboard,
  LogOut,
  PanelLeftClose,
  PanelLeftOpen,
  Settings,
  Users,
} from 'lucide-react'
import type { LucideIcon } from 'lucide-react'
import {
  activeNavKey,
  auxiliaryRoutes,
  breadcrumbsFor,
  globalRoutes,
  fillRoutePathByKey,
  menuRoutes,
} from './routes'
import { newIdempotencyKey } from '../lib/api/studio'
import { CommandSearch } from './pages/TodayPages'
import { clearForActor, currentActorID } from '../lib/pendingQueue'

/**
 * 全局壳（Issue #160 T09）：4 全局入口 + 辅助入口 + 目录评审（仅非生产）。
 *
 * 与旧控制台壳的区别（契约 §3.1「三层清楚分开」）：
 *
 *	第一层  全局四入口   今日工作 / 数据项目 / 方案库 / 交付库
 *	第二层  项目六工作区  由 ProjectLayout 承担（进入某个项目之后）
 *	第三层  辅助入口     动态 / 设置 / 帮助（不与主流程争菜单位置）
 *
 * 旧控制台把**所有**功能平铺在一条侧边栏里（7 个用户项 + 6 个管理项 +
 * 阶段页），因此用户无法从菜单判断「我现在在哪一层」。这里把层级做成结构，
 * 而不是靠文案说明。
 *
 * 导航项**全部**由 routes.ts 派生：本文件不写任何路由字符串字面量，
 * 于是「菜单与路由漂移」在结构上不可能发生（test/l15_studio_shell.mjs 断言）。
 */

const GLOBAL_ICONS: Record<string, LucideIcon> = {
  today: LayoutDashboard,
  projects: Compass,
  recipes: BookOpen,
  deliveries: HardDriveDownload,
}

const AUXILIARY_ICONS: Record<string, LucideIcon> = {
  activity: Activity,
  'settings.connections': FolderCog,
  'settings.team': Users,
  help: Settings,
}

export type StudioLayoutProps = {
  userEmail: string
  isAdmin: boolean
  onLogout: () => void
}

export function StudioLayout({ userEmail, isAdmin, onLogout }: StudioLayoutProps) {
  const location = useLocation()
  const navigate = useNavigate()
  const breadcrumbs = useBreadcrumbs()
  const [collapsed, setCollapsed] = useState(false)

  // 当前高亮项：由元数据派生。子页高亮到自己的 navParent，
  // 因此「扩量规划」不会让侧边栏掉回默认项（issue #61 的形态）。
  const activeKey = useMemo(() => activeNavKey(location.pathname), [location.pathname])

  // 页面标题与焦点：T09 验收项要求「标题焦点」。路由变化时把标题与
  // 屏幕阅读器播报一起更新，键盘/听觉用户才不会「失焦」。
  const [announcement, setAnnouncement] = useState('')
  useEffect(() => {
    const current = menuRoutes().find((route) => route.key === activeKey)
    const label = current?.label ?? ''
    setAnnouncement(label ? `已进入${label}` : '')
    document.title = label ? `${label} · Atelier · 数据项目工作室` : 'Atelier · 数据项目工作室'
  }, [activeKey])

  return (
    <div className="app-layout atelier-shell">
      <nav
        className="app-layout__sidebar"
        aria-label="主导航"
        style={{ width: collapsed ? 48 : 232 }}
      >
        {collapsed ? (
          <button
            type="button"
            className="sidebar-collapse-button"
            aria-label="展开导航"
            onClick={() => setCollapsed(false)}
          >
            <PanelLeftOpen size={16} />
          </button>
        ) : (
          <>
            <div className="sidebar-workspace-header">
              <div className="sidebar-workspace-info">
                <div className="sidebar-workspace-name">Atelier</div>
                <div className="sidebar-workspace-plan">数据项目工作室 · {isAdmin ? '管理员' : '普通用户'}</div>
              </div>
              <button
                type="button"
                className="sidebar-collapse-button"
                aria-label="收起导航"
                onClick={() => setCollapsed(true)}
              >
                <PanelLeftClose size={16} />
              </button>
            </div>

            {/* 命令搜索（T27）：Esc 关闭、回车打开第一条、关闭后焦点回到触发点。 */}
            <div className="sidebar-nav-section atelier-command-search-slot" data-command-search-slot="true">
              <CommandSearch />
            </div>

            {/* 第一层：全局四入口。 */}
            <div className="sidebar-nav-section">
              <div className="sidebar-nav-heading">工作区</div>
              {globalRoutes.map((route) => renderNavItem(route.path, GLOBAL_ICONS[route.key], route.label, route.caption, activeKey === route.key))}
            </div>

            {/* 第三层：辅助入口。刻意放在下方且样式更轻，不与主流程争位置。 */}
            <div className="sidebar-nav-section">
              <div className="sidebar-nav-heading">辅助</div>
              {auxiliaryRoutes.map((route) =>
                renderNavItem(route.path, AUXILIARY_ICONS[route.key], route.label, route.caption, activeKey === route.key),
              )}
            </div>

            <div className="sidebar-footer">
              <div className="sidebar-account" title={userEmail}>
                {userEmail}
              </div>
              <Button
                size="small"
                icon={<LogOut size={14} />}
                onClick={() => {
                  // 退出账号时清理本机待同步队列（T29）：
                  // 敏感正文不在本机留存；下一个登录的用户不会看到上一个人的草稿。
                  const actorId = currentActorID()
                  if (actorId > 0) clearForActor(actorId)
                  onLogout()
                  navigate('/login')
                }}
              >
                退出
              </Button>
            </div>
          </>
        )}
      </nav>

      <main className="app-layout__content" id="studio-main" tabIndex={-1}>
        <header className="atelier-topbar">
          <div className="atelier-topbar__crumbs"><Breadcrumbs items={breadcrumbs} /></div>
          <div className="atelier-topbar__actions"><CommandSearch /><Avatar color="purple" size="small">{userEmail.slice(0, 1).toUpperCase() || 'A'}</Avatar></div>
        </header>
        {/* 屏幕阅读器播报当前层级：视觉用户从高亮看出所在位置，
            而听觉用户需要等价的信号（T29 的无障碍要求，T09 先打地桩）。 */}
        <span className="sr-only" role="status" aria-live="polite">
          {announcement}
        </span>
        <Outlet />
      </main>
    </div>
  )
}

function renderNavItem(
  path: string,
  Icon: LucideIcon | undefined,
  label: string,
  caption: string,
  active: boolean,
) {
  if (!Icon) return null
  return (
    <NavLink
      key={path}
      to={path}
      className={active ? 'sidebar-nav-item sidebar-nav-item--active' : 'sidebar-nav-item'}
      aria-current={active ? 'page' : undefined}
      title={caption}
    >
      <Icon size={16} />
      <span className="sidebar-nav-item__label">{label}</span>
    </NavLink>
  )
}

/** 供测试引用：当前路由下的面包屑。 */
export function useBreadcrumbs(): ReturnType<typeof breadcrumbsFor> {
  const location = useLocation()
  return useMemo(() => {
    const params: Record<string, string> = {}
    const matched = location.pathname.match(/^\/p\/([^/]+)/)
    if (matched) params.projectId = matched[1]
    return breadcrumbsFor(location.pathname, params)
  }, [location.pathname])
}

/** 供项目壳使用：生成一个在本次导航内保持稳定的幂等键。 */
export function useStableIdempotencyKey(seed: string): string {
  const [key] = useState(() => `${seed}:${newIdempotencyKey()}`)
  return key
}

/** 面包屑展示（由 ProjectLayout 与 StudioLayout 共用的渲染器）。 */
export function Breadcrumbs({ items }: { items: { label: string; path?: string }[] }) {
  const { Text } = Typography
  return (
    <div className="console-breadcrumbs" aria-label="面包屑">
      {items.map((item, index) => (
        <span key={`${item.label}-${index}`} className="console-breadcrumbs__item">
          {index > 0 ? <ChevronRight size={12} aria-hidden /> : null}
          {item.path ? (
            <Link to={item.path}>{item.label}</Link>
          ) : (
            <Text strong>{item.label}</Text>
          )}
        </span>
      ))}
    </div>
  )
}

/** 供页面引用的路由构造器（避免各处手写 `/p/${id}/...`）。 */
export function projectHref(key: string, projectId: number | string): string {
  try {
    return fillRoutePathByKey(key, { projectId })
  } catch {
    return fillRoutePathByKey('projects', {})
  }
}
