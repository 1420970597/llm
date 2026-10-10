import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { Link, NavLink, Outlet, useLocation, useNavigate } from 'react-router-dom'
import { Avatar, Button, Dropdown, Typography } from '@douyinfe/semi-ui'
import {
  Activity,
  Archive,
  BookOpen,
  ChevronRight,
  ChevronDown,
  Compass,
  Filter,
  FlaskConical,
  FolderCog,
  HardDriveDownload,
  LayoutDashboard,
  LogOut,
  Menu,
  Settings,
  Users,
  Wrench,
} from 'lucide-react'
import type { LucideIcon } from 'lucide-react'
import {
  activeNavKey,
  auxiliaryRoutes,
  breadcrumbsFor,
  globalRoutes,
  fillRoutePathByKey,
  matchRoute,
} from './routes'
import { newIdempotencyKey, parseProjectResourceId } from '../lib/api/studio'
import type { ProjectResourceId } from '../lib/api/studio'
import { CommandSearch } from './pages/TodayPages'
import { clearForActor, currentActorID } from '../lib/pendingQueue'
import { useProjectName } from './projectName'
import { client } from '../lib/api'

/**
 * 项目优先的全局导航。辅助能力集中在工具菜单，项目内主线由 ProjectLayout 展示。
 * 链接与能力状态仍只从 routes.ts 派生；不复制另一套业务路由。
 */

const GLOBAL_ICONS: Record<string, LucideIcon> = {
  today: LayoutDashboard,
  projects: Compass,
  recipes: BookOpen,
  deliveries: HardDriveDownload,
}

const AUXILIARY_ICONS: Record<string, LucideIcon> = {
  'tools.evaluation': FlaskConical,
  'tools.cleaning': Filter,
  'legacy.history': Archive,
  'legacy.history.detail': Archive,
  activity: Activity,
  'settings.connections': FolderCog,
  'settings.team': Users,
  help: Settings,
  'settings.capabilities': Wrench,
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
  const [mobileNavOpen, setMobileNavOpen] = useState(false)
  const [toolsOpen, setToolsOpen] = useState(false)
  const [legacyMigrationComplete, setLegacyMigrationComplete] = useState(false)
  useEffect(() => {
    let cancelled = false
    setLegacyMigrationComplete(false)
    void client.get<{ scope?: string; migrationComplete: boolean; legacyDatasets: number; pendingDatasets: number }>('/v1/legacy/migration-status')
      .then((response) => {
        const status = response.data
        if (!cancelled) setLegacyMigrationComplete(status.scope === 'visible_legacy_assets' && status.migrationComplete === true && status.legacyDatasets > 0 && status.pendingDatasets === 0)
      }).catch(() => { if (!cancelled) setLegacyMigrationComplete(false) })
    return () => { cancelled = true }
  }, [userEmail, location.pathname])
  const mobileMenuRef = useRef<HTMLButtonElement>(null)
  const mobileNavFocusTimer = useRef<number | null>(null)
  const toolsTriggerRef = useRef<HTMLButtonElement>(null)

  const primaryRoutes = useMemo(() => ['projects', 'today', 'recipes', 'deliveries']
    .flatMap((key) => globalRoutes.filter((route) => route.key === key)), [])
  const toolRoutes = useMemo(() => auxiliaryRoutes
    .filter((route) => !route.navParent && !(route.key === 'legacy.history' && legacyMigrationComplete)), [legacyMigrationComplete])

  const openMobileNav = useCallback(() => {
    setMobileNavOpen(true)
    if (mobileNavFocusTimer.current !== null) window.clearTimeout(mobileNavFocusTimer.current)
    // 等点击默认焦点与侧栏过渡稳定，再移入导航首项。
    mobileNavFocusTimer.current = window.setTimeout(() => {
      document.querySelector<HTMLElement>('#studio-main-navigation a[href]')?.focus()
      mobileNavFocusTimer.current = null
    }, 500)
  }, [])

  const closeMobileNav = useCallback((restoreFocus = true) => {
    if (mobileNavFocusTimer.current !== null) {
      window.clearTimeout(mobileNavFocusTimer.current)
      mobileNavFocusTimer.current = null
    }
    setToolsOpen(false)
    setMobileNavOpen(false)
    if (restoreFocus) window.setTimeout(() => mobileMenuRef.current?.focus(), 0)
  }, [])

  useEffect(() => {
    if (!mobileNavOpen) return

    const closeOnEscape = (event: KeyboardEvent) => {
      if (event.key !== 'Escape') return
      if (event.defaultPrevented) return
      event.preventDefault()
      closeMobileNav()
    }
    document.addEventListener('keydown', closeOnEscape)
    return () => document.removeEventListener('keydown', closeOnEscape)
  }, [closeMobileNav, mobileNavOpen])

  useEffect(() => {
    if (!toolsOpen) return
    const timer = window.setTimeout(() => {
      document.querySelector<HTMLElement>('[data-studio-tools-menu] [role="menuitem"]')?.focus()
    }, 0)
    return () => window.clearTimeout(timer)
  }, [toolsOpen])

  useEffect(() => () => {
    if (mobileNavFocusTimer.current !== null) window.clearTimeout(mobileNavFocusTimer.current)
  }, [])

  const handleLogout = () => {
    // 退出账号时清理本机待同步队列（T29）：敏感正文不在本机留存；
    // 下一个登录的用户不会看到上一个人的草稿或离线决定。
    const actorId = currentActorID()
    if (actorId > 0) clearForActor(actorId)
    onLogout()
    navigate('/login')
  }

  // 当前高亮项：由元数据派生。子页高亮到自己的 navParent，
  // 因此「扩量规划」不会让侧边栏掉回默认项（issue #61 的形态）。
  const activeKey = useMemo(() => activeNavKey(location.pathname), [location.pathname])

  // 页面标题与焦点：T09 验收项要求「标题焦点」。路由变化时把标题与
  // 屏幕阅读器播报一起更新，键盘/听觉用户才不会「失焦」。
  const [announcement, setAnnouncement] = useState('')
  useEffect(() => {
    const current = matchRoute(location.pathname)
    const label = current?.key === 'today' ? '待处理' : current?.label ?? ''
    setAnnouncement(label ? `已进入${label}` : '')
    document.title = label ? `${label} · Atelier · 数据项目工作室` : 'Atelier · 数据项目工作室'
  }, [location.pathname])

  return (
    <div className="app-layout atelier-shell">
      <nav
        id="studio-main-navigation"
        className="app-layout__sidebar"
        aria-label="主导航"
        data-mobile-open={mobileNavOpen ? 'true' : 'false'}
      >
        <div className="sidebar-workspace-header">
          <Link className="sidebar-workspace-name" to={fillRoutePathByKey('projects', {})} onClick={() => closeMobileNav(false)}>Atelier</Link>
        </div>

        <div className="sidebar-nav-section studio-primary-navigation">
          {primaryRoutes.map((route) => renderNavItem(
            route.path,
            GLOBAL_ICONS[route.key],
            route.key === 'today' ? '待处理' : route.label,
            route.caption,
            activeKey === route.key || (route.key === 'projects' && activeKey.startsWith('project.')),
            () => closeMobileNav(false),
          ))}
        </div>

        <div className="sidebar-footer">
          <Dropdown
            trigger="click"
            motion={false}
            position="bottomRight"
            visible={toolsOpen}
            onVisibleChange={setToolsOpen}
            closeOnEsc
            onEscKeyDown={(event) => {
              event.preventDefault()
              event.stopPropagation()
              setToolsOpen(false)
              toolsTriggerRef.current?.focus()
            }}
            render={
              <Dropdown.Menu data-studio-tools-menu="true">
                {toolRoutes.map((route) => {
                  const Icon = AUXILIARY_ICONS[route.key]
                  return <Dropdown.Item
                    key={route.key}
                    icon={Icon ? <Icon size={16} aria-hidden /> : undefined}
                    active={activeKey === route.key}
                    forwardRef={(element) => { element?.setAttribute('aria-label', route.label) }}
                    onClick={() => {
                      setToolsOpen(false)
                      closeMobileNav(false)
                      navigate(route.path)
                    }}
                  >{route.label}</Dropdown.Item>
                })}
              </Dropdown.Menu>
            }
          >
            <button
              type="button"
              className="studio-tools-trigger"
              aria-label="工具与设置"
              aria-haspopup="menu"
              aria-expanded={toolsOpen}
              ref={toolsTriggerRef}
              onKeyDown={(event) => {
                if (event.key !== 'ArrowDown') return
                event.preventDefault()
                setToolsOpen(true)
              }}
            >
              <Settings size={16} aria-hidden />
              <span>工具与设置</span>
              <ChevronDown size={14} aria-hidden />
            </button>
          </Dropdown>
          <div className="sidebar-account" title={`${userEmail} · ${isAdmin ? '管理员' : '普通用户'}`}>
            <Avatar color="blue" size="small">{userEmail.slice(0, 1).toUpperCase() || 'A'}</Avatar>
            <span className="studio-account-email">{userEmail}</span>
            <span className="sr-only">{isAdmin ? '管理员' : '普通用户'}</span>
          </div>
          <Button
            size="small"
            icon={<LogOut size={14} />}
            aria-label="退出登录"
            title="退出登录"
            onClick={handleLogout}
          >
            退出
          </Button>
        </div>
      </nav>

      {mobileNavOpen ? (
        <button
          type="button"
          className="atelier-mobile-backdrop"
          aria-label="关闭主导航"
          onClick={() => closeMobileNav()}
        />
      ) : null}

      <main className="app-layout__content" id="studio-main" tabIndex={-1}>
        <header className="atelier-topbar">
          <div className="atelier-topbar__leading">
            <button
              type="button"
              className="atelier-mobile-menu"
              aria-label={mobileNavOpen ? '关闭主导航' : '打开主导航'}
              aria-controls="studio-main-navigation"
              aria-expanded={mobileNavOpen}
              onClick={() => (mobileNavOpen ? closeMobileNav(false) : openMobileNav())}
              ref={mobileMenuRef}
            >
              <Menu size={18} aria-hidden />
            </button>
            <div className="atelier-topbar__crumbs"><Breadcrumbs items={breadcrumbs} /></div>
          </div>
          <div className="atelier-topbar__actions">
            <CommandSearch />
          </div>
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
  onNavigate?: () => void,
) {
  if (!Icon) return null
  return (
    <NavLink
      key={path}
      to={path}
      className={active ? 'sidebar-nav-item sidebar-nav-item--active' : 'sidebar-nav-item'}
      aria-current={active ? 'page' : undefined}
      title={caption}
      onClick={onNavigate}
    >
      <Icon size={16} />
      <span className="sidebar-nav-item__label">{label}</span>
    </NavLink>
  )
}

/** 供测试引用：当前路由下的面包屑。 */
export function useBreadcrumbs(): ReturnType<typeof breadcrumbsFor> {
  const location = useLocation()
  const matched = location.pathname.match(/^\/p\/([^/]+)/)
  const projectId = matched ? parseProjectResourceId(matched[1]) : null
  const projectName = useProjectName(projectId)
  return useMemo(() => {
    const params: Record<string, string> = {}
    if (matched) params.projectId = matched[1]
    return breadcrumbsFor(location.pathname, params, projectName ?? '项目')
  }, [location.pathname, projectName, matched?.[1]])
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
export function projectHref(
  key: string,
  projectId: ProjectResourceId,
  extra: Record<string, string | number> = {},
): string {
  try {
    return fillRoutePathByKey(key, { projectId, ...extra })
  } catch {
    return fillRoutePathByKey('projects', {})
  }
}
