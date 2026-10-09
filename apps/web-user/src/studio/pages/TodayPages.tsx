import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { useNavigate } from 'react-router-dom'
import { Button, Card, Empty, Input, Spin, Tag, Typography } from '@douyinfe/semi-ui'
import { Bell, RefreshCw, Search } from 'lucide-react'
import { activityApi } from '../../lib/api/studio'
import type { ActivityItem, SearchHit, TodoItem, WorkspaceOverview } from '../../lib/api/studio'
import { allStudioRoutes, fillRoutePathByKey } from '../routes'
import { projectHref } from '../StudioLayout'

/**
 * 今日工作与动态（Issue #160 T27）。
 *
 * 三条与契约直接相关的界面决定：
 *
 *  1. **待办一律带跳转**。没有链接的待办在几十个项目下等于不可用（用户还得自己找）。
 *     服务端已经给出 `links.page`，这里只负责渲染按钮。
 *  2. **未读 != 已处理**。页面上明确写出这一点，并且「全部已读」不改变任何待办数量
 *     （业务待办由事实派生）。把它画成「清空待办」会让用户以为事情做完了。
 *  3. **增量轮询而不是假装实时**。「刷新」按 head 游标重新取第一页并按键去重合并；
 *     没有 SSE，也不会假装推送（T27 明确 SSE 属后续优化）。
 */

const TODO_TITLES: Record<string, string> = {
  pending_review: '待审阅',
  pilot_comparable: '待比较',
  failed_recovery: '生产异常',
  release_blocked: '发布阻塞',
  unread_activity: '未读动态',
}

const TODO_ACTIONS: Record<string, { routeKey: string; label: string }> = {
  pending_review: { routeKey: 'project.review', label: '开始审阅' },
  pilot_comparable: { routeKey: 'project.compare', label: '比较试制' },
  failed_recovery: { routeKey: 'project.runs', label: '恢复批次' },
  release_blocked: { routeKey: 'project.releases', label: '处理阻塞' },
}

/** 保留服务端对象深链；聚合待办先选项目，但不丢失要处理的业务入口。 */
export function todoHref(todo: TodoItem): string | undefined {
  if (todo.links?.page) return todo.links.page
  const action = TODO_ACTIONS[todo.kind]
  if (action) return projectActionHref(todo.projectId, action.routeKey)
  return todo.kind === 'unread_activity' ? studioPath('activity') : undefined
}

function studioPath(key: string): string {
  return fillRoutePathByKey(key, {})
}

/**
 * 总览磁贴的跳转目标（issue #205）。
 *
 * 缺陷形态：6 个磁贴里有 3 个写死了 `studioPath('today')`，也就是**当前页自身**
 * （点下去原地不动），而同一页自己写着「点进去看到的是同一份事实」——
 * 一半的磁贴直接推翻了这句话。
 *
 * 三条取值规则（把「点得进去」变成结构事实，而不是靠逐个改死值）：
 *  1. **项目内页**：数据都是项目内对象的聚合，因此优先落到项目页；
 *  2. **恰好一个项目时才能深链**：`/p/{id}/runs` 只能指向一个项目。
 *     多个项目时先去 `/projects`（让用户选）—— 不猜一个项目，那会在界面上
 *     展示一个属于别人项目的数字；
 *  3. **无项目时**（新工作区）回项目列表：那里正是「去建一个项目」。
 */
function overviewProjectID(overview: WorkspaceOverview): number {
  return overview.projectCount === 1 && overview.scopedProjectIds?.length === 1
    ? overview.scopedProjectIds[0]
    : 0
}

function projectActionHref(projectID: number, projectRouteKey: string): string {
  return Number.isSafeInteger(projectID) && projectID > 0
    ? projectHref(projectRouteKey, projectID)
    : `${studioPath('projects')}?${new URLSearchParams({ next: projectRouteKey })}`
}

/** 单项目直达任务；多项目先选择项目，再自动进入同一任务。 */
export function overviewProjectHref(overview: WorkspaceOverview, projectRouteKey: string): string {
  return projectActionHref(overviewProjectID(overview), projectRouteKey)
}

function formatTodoDate(value: string): string {
  const timestamp = Date.parse(value)
  if (!Number.isFinite(timestamp)) return '时间未记录'
  return new Intl.DateTimeFormat('zh-CN', { month: 'short', day: 'numeric' }).format(new Date(timestamp))
}

// ---------------------------------------------------------------------------
// 今日工作（W01）
// ---------------------------------------------------------------------------

export function TodayPage() {
  const navigate = useNavigate()
  const { Text } = Typography
  const [todos, setTodos] = useState<TodoItem[]>([])
  const [notes, setNotes] = useState<string[]>([])
  /** 工作台总览（issue #197 第 10 条）：全页只有一个未读数字的形态已修正。 */
  const [overview, setOverview] = useState<WorkspaceOverview | null>(null)
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)
  const [busy, setBusy] = useState(false)

  const decisionTodos = useMemo(
    () => todos.filter((todo) => todo.kind !== 'unread_activity'),
    [todos],
  )
  const unreadTodo = useMemo(
    () => todos.find((todo) => todo.kind === 'unread_activity'),
    [todos],
  )

  const load = useCallback(async () => {
    setLoading(true)
    setError(null)
    try {
      const response = await activityApi.today()
      setTodos(response.todos ?? [])
      setNotes(response.notes ?? [])
      setOverview(response.overview ?? null)
    } catch (loadError) {
      setError(loadError instanceof Error ? loadError.message : '加载今日工作失败')
    } finally {
      setLoading(false)
    }
  }, [])

  useEffect(() => {
    void load()
  }, [load])

  const markRead = useCallback(async () => {
    setBusy(true)
    setError(null)
    try {
      await activityApi.markRead()
      await load()
    } catch (markError) {
      setError(markError instanceof Error ? markError.message : '标记已读失败')
    } finally {
      setBusy(false)
    }
  }, [load])

  return (
    <div className="console-page atelier-today-page atelier-action-center" data-studio-page="today">
      <header className="console-page__header">
        <div>
          <h1>待处理</h1>
          <Text type="tertiary">审阅、生产异常与发布阻塞。</Text>
        </div>
        <div className="console-page__actions">
          <Button icon={<RefreshCw size={14} />} onClick={() => void load()} disabled={loading}>刷新待办</Button>
          <Button onClick={() => navigate(studioPath('projects'))}>查看项目</Button>
        </div>
      </header>

      {error ? (
        <div role="alert" data-today-error="true">
          <Card className="console-card mb-3" bodyStyle={{ padding: 14 }}>
            <Text type="danger">{error}</Text>
            <Button size="small" theme="borderless" onClick={() => void load()} disabled={loading}>重试</Button>
          </Card>
        </div>
      ) : null}

      {/*
        issue #197 第 10 条：甲方原话是「今日工作展示的数据不对，应当是一个
        工作台总览的效果」。以前全页只有一个数字（7 条未读动态）。
        这里的每个数字都可点进对应列表，且与列表页读的是同一份事实。
      */}
      {overview ? (
        <section className="atelier-overview-grid atelier-action-metrics" data-today-overview="true" aria-label="工作区事实">
          <a className="atelier-overview-tile" href={studioPath('projects')} data-overview-tile="projects">
            <span className="eyebrow">数据项目</span>
            <strong>{overview.projectCount}</strong>
            <small>你可见的项目</small>
          </a>
          <a className="atelier-overview-tile" href={overviewProjectHref(overview, 'project.runs')} data-overview-tile="running">
            <span className="eyebrow">进行中批次</span>
            <strong>{overview.runningBatches}</strong>
            <small>排队 / 运行 / 暂停请求中</small>
          </a>
          <a className="atelier-overview-tile" href={overviewProjectHref(overview, 'project.runs')} data-overview-tile="shortfall">
            <span className="eyebrow">产出缺口</span>
            <strong className={overview.batchesWithShortfall > 0 ? 'atelier-overview-tile--alert' : undefined}>
              {overview.batchesWithShortfall}
            </strong>
            <small>
              计划 {overview.totalPlannedUnits} · 完成 {overview.totalCompletedUnits}
            </small>
          </a>
          {/*
            issue #205：「待人工判断」以前指向 `/activity`（动态），那是**语义错误** ——
            这个数字来自审阅投影，而「动态」不会显示任何待判断样本，用户点进去
            看到的是另一份事实。应去项目的审阅队列。
          */}
          <a className="atelier-overview-tile" href={overviewProjectHref(overview, 'project.review')} data-overview-tile="pending">
            <span className="eyebrow">待审阅</span>
            <strong className={overview.pendingReview > 0 ? 'atelier-overview-tile--alert' : undefined}>
              {overview.pendingReview}
            </strong>
            <small>开始审阅 →</small>
          </a>
          <a className="atelier-overview-tile" href={overviewProjectHref(overview, 'project.data')} data-overview-tile="produced">
            <span className="eyebrow">近 7 天产出</span>
            <strong>{overview.producedLast7Days}</strong>
            <small>新增样本版本数</small>
          </a>
          {/*
            issue #205 第二条：「交付」磁贴写「已发布 0 · 被挡住 1」却整体链到
            `/deliveries`，而交付库按定义**只显示已发布版本** —— 被挡住的那 1 条
            在那个页面里根本不存在，用户找不到任何出口。
            两个事实必须给两个目标：主体 → 交付库（已发布）；
            「被挡住 N」→ 项目「发布」（那里才能处理门槛阻塞）。

            为什么这个磁贴是 `div` 而不是 `a`：内层要放**第二个链接**，而 `<a>`
            里嵌 `<a>` 是非法 HTML（浏览器会拆开它）。其余磁贴只有单一目标，
            保持 `a` 即可。
          */}
          <div className="atelier-overview-tile atelier-overview-tile--split" data-overview-tile="releases">
            <a className="atelier-overview-tile__main" href={studioPath('deliveries')}>
              <span className="eyebrow">交付</span>
              <strong>{overview.publishedReleases}</strong>
              <small>
                已发布
                {overview.blockedReleases > 0
                  ? ` · 被挡住 ${overview.blockedReleases}`
                  : ' · 无阻塞候选'}
              </small>
            </a>
            {overview.blockedReleases > 0 ? (
              <a
                className="atelier-overview-tile__blocked"
                data-overview-blocked-link="true"
                href={overviewProjectHref(overview, 'project.releases')}
              >
                处理被挡住的 {overview.blockedReleases} 个候选 →
              </a>
            ) : null}
          </div>
        </section>
      ) : null}

      <section className="atelier-decision-panel atelier-action-queue" aria-labelledby="today-queue-heading">
          <div className="atelier-section-heading"><h2 id="today-queue-heading">待办队列</h2></div>
          {loading ? (
            <div className="atelier-inline-state"><Spin tip="正在汇总待办" /></div>
          ) : error && decisionTodos.length === 0 ? (
            <div className="atelier-inline-state">待办未加载，请重试。</div>
          ) : decisionTodos.length === 0 ? (
            <div className="atelier-inline-state" data-today-empty="true"><Empty description="暂无待办"><Button onClick={() => navigate(studioPath('projects'))}>继续项目</Button></Empty></div>
          ) : (
            <div className="atelier-todo-list" data-today-todos="true">
              {decisionTodos.map((todo) => {
                const href = todoHref(todo)
                return (
                  <div key={`${todo.kind}-${todo.projectId}`} className="atelier-todo-row" data-todo-kind={todo.kind}>
                    <div className="atelier-todo-row__content"><Tag size="small" color="amber">{TODO_TITLES[todo.kind] ?? '待处理'}</Tag><strong>{todo.summary}</strong><Text type="tertiary" size="small">{todo.projectId > 0 ? `项目 #${todo.projectId} · ` : ''}{todo.count} 项 · {formatTodoDate(todo.updatedAt)}</Text></div>
                    {href ? <Button size="small" theme="solid" type="primary" onClick={() => navigate(href)}>{TODO_ACTIONS[todo.kind]?.label ?? '去处理'}</Button> : null}
                  </div>
                )
              })}
            </div>
          )}
      </section>
      {!loading && (unreadTodo || notes.length > 0) ? (
        <details className="atelier-activity-disclosure mt-3" data-today-unread="true">
          <summary>动态{unreadTodo ? ` · ${unreadTodo.count} 条未读` : ''}</summary>
          <div className="mt-2 flex flex-wrap items-center gap-3">
            {unreadTodo ? <Text type="tertiary" size="small">{unreadTodo.summary}</Text> : null}
            <Button size="small" theme="borderless" onClick={() => navigate(unreadTodo ? todoHref(unreadTodo) ?? studioPath('activity') : studioPath('activity'))}>查看动态</Button>
            {unreadTodo ? <Button size="small" icon={<Bell size={14} />} disabled={busy || loading} onClick={() => void markRead()} data-today-mark-read="true">全部已读</Button> : null}
            {notes.map((note) => <Text key={note} type="tertiary" size="small">{note}</Text>)}
          </div>
        </details>
      ) : null}
    </div>
  )
}

// ---------------------------------------------------------------------------
// 动态（S01）
// ---------------------------------------------------------------------------

export function ActivityPage() {
  const { Title, Text } = Typography
  const [items, setItems] = useState<ActivityItem[]>([])
  const [notes, setNotes] = useState<string[]>([])
  const [cursor, setCursor] = useState('')
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)
  const [busy, setBusy] = useState(false)

  const activityKey = useCallback((item: ActivityItem) => item.groupKey || `${item.source}#${item.eventId}`, [])

  const merge = useCallback((incoming: ActivityItem[]) => {
    setItems((previous) => {
      const seen = new Set(previous.map(activityKey))
      const merged = [...previous]
      for (const item of incoming) {
        const key = activityKey(item)
        if (!seen.has(key)) {
          seen.add(key)
          merged.push(item)
        }
      }
      return merged
    })
  }, [activityKey])

  const loadHead = useCallback(async () => {
    setError(null)
    try {
      const response = await activityApi.activity({ limit: 30 })
      setItems(response.items ?? [])
      setCursor(response.nextCursor ?? '')
      setNotes(response.notes ?? [])
    } catch (loadError) {
      setError(loadError instanceof Error ? loadError.message : '加载动态失败')
    }
  }, [])

  const loadMore = useCallback(async () => {
    if (!cursor) return
    setBusy(true)
    try {
      const response = await activityApi.activity({ cursor, limit: 30 })
      merge(response.items ?? [])
      setCursor(response.nextCursor ?? '')
    } catch (loadError) {
      setError(loadError instanceof Error ? loadError.message : '加载更多失败')
    } finally {
      setBusy(false)
    }
  }, [cursor, merge])

  useEffect(() => {
    setLoading(true)
    void loadHead().finally(() => setLoading(false))
  }, [loadHead])

  // 增量轮询：30 秒按 head 刷新一次，按 (source, eventId) 去重合并。
  // 断线期间不会丢事件（下次刷新会把它们一起带回来）。
  useEffect(() => {
    const timer = window.setInterval(() => {
      void loadHead()
    }, 30000)
    return () => window.clearInterval(timer)
  }, [loadHead])

  return (
    <div className="console-page" data-studio-page="activity">
      <div className="console-page__header">
        <div>
          <Title heading={4} className="!mb-1">
            动态
          </Title>
          <Text type="tertiary">项目里发生过的事（批次生命周期与治理操作），按时间倒序。</Text>
        </div>
        <div className="flex items-center gap-2">
          <Button size="small" icon={<RefreshCw size={14} />} onClick={() => void loadHead()}>
            刷新
          </Button>
          <Button size="small" onClick={() => void activityApi.markRead().then(loadHead)} data-activity-mark-read="true">
            全部已读
          </Button>
        </div>
      </div>

      {error ? (
        <Card className="console-card mb-3" bodyStyle={{ padding: 14 }} data-activity-error="true">
          <Text type="danger">{error}</Text>
        </Card>
      ) : null}

      {loading ? (
        <div className="flex justify-center py-10">
          <Spin tip="正在加载动态" />
        </div>
      ) : items.length === 0 ? (
        <Card className="console-card" bodyStyle={{ padding: 24 }} data-activity-empty="true">
          <Empty description="还没有动态。" />
        </Card>
      ) : (
        <Card className="console-card" bodyStyle={{ padding: 14 }} data-activity-items="true">
          <ul className="review-evidence">
            {items.map((item) => (
              <li key={activityKey(item)} data-activity-item={item.source}>
                <Text size="small">
                  {item.unread ? <Tag size="small" color="amber">未读</Tag> : null} {item.summary}
                </Text>
                <Text type="tertiary" size="small" className="block">
                  {new Date(item.createdAt).toLocaleString()}
                  {item.links?.page ? (
                    <>
                      {' · '}
                      <a href={item.links.page}>查看</a>
                    </>
                  ) : null}
                </Text>
              </li>
            ))}
          </ul>
          {cursor ? (
            <div className="mt-2">
              <Button size="small" loading={busy} onClick={() => void loadMore()} data-activity-more="true">
                加载更多
              </Button>
            </div>
          ) : null}
          {notes.map((note) => (
            <Text key={note} type="tertiary" size="small" className="block mt-2">
              {note}
            </Text>
          ))}
        </Card>
      )}
    </div>
  )
}

// ---------------------------------------------------------------------------
// 命令搜索（S02 的一部分：Esc / 回车 / 焦点返回）
// ---------------------------------------------------------------------------

export function CommandSearch() {
  const navigate = useNavigate()
  const { Text } = Typography
  const [open, setOpen] = useState(false)
  const [keyword, setKeyword] = useState('')
  const [hits, setHits] = useState<SearchHit[]>([])
  const [error, setError] = useState<string | null>(null)
  const triggerRef = useRef<HTMLButtonElement | null>(null)

  const routeHits = useMemo(() => {
    if (keyword.trim() === '') return []
    const needle = keyword.trim().toLowerCase()
    return allStudioRoutes
      .filter((route) => route.path.includes(':') === false)
      .filter((route) => route.label.toLowerCase().includes(needle) || route.caption.toLowerCase().includes(needle))
      .slice(0, 5)
      .map((route) => ({ kind: 'page', label: route.label, caption: route.caption, pagePath: route.path }))
  }, [keyword])

  useEffect(() => {
    if (!open) return
    const trimmed = keyword.trim()
    if (trimmed === '') {
      setHits([])
      return
    }
    let cancelled = false
    const timer = window.setTimeout(() => {
      void (async () => {
        try {
          const response = await activityApi.search(trimmed)
          if (!cancelled) setHits(response.items ?? [])
        } catch (searchError) {
          if (!cancelled) setError(searchError instanceof Error ? searchError.message : '搜索失败')
        }
      })()
    }, 200)
    return () => {
      cancelled = true
      window.clearTimeout(timer)
    }
  }, [keyword, open])

  const close = useCallback(() => {
    setOpen(false)
    setKeyword('')
    setError(null)
    // 焦点返回触发点：键盘用户不应该在关闭搜索后「掉到页面开头」。
    triggerRef.current?.focus()
  }, [])

  const go = useCallback(
    (path: string) => {
      navigate(path)
      close()
    },
    [close, navigate],
  )

  if (!open) {
    return (
      <Button
        size="small"
        type="tertiary"
        icon={<Search size={14} />}
        data-command-search-trigger="true"
        aria-label="打开命令搜索"
        title="打开命令搜索"
        onClick={() => setOpen(true)}
        ref={triggerRef as never}
      >
        搜索
      </Button>
    )
  }

  const results: SearchHit[] = [...routeHits, ...hits]

  return (
    <Card className="console-card" bodyStyle={{ padding: 12 }} data-command-search="true">
      {/* Esc 只在输入框聚焦时生效：绑在容器上会让页面里任何地方的 Esc 都关掉搜索，
          而用户可能只是想退出别的浮层。 */}
      <div
        onKeyDown={(event: React.KeyboardEvent) => {
          if (event.key === 'Escape') {
            event.stopPropagation()
            close()
          }
        }}
      >
      <Input
        autoFocus
        value={keyword}
        placeholder="搜索页面、项目、样本、批次（Esc 关闭，回车打开第一条）"
        aria-label="命令搜索"
        onChange={setKeyword}
        onKeyDown={(event) => {
          if (event.key === 'Enter' && results.length > 0) {
            event.preventDefault()
            go(results[0].pagePath)
          }
        }}
      />
      {error ? (
        <Text type="danger" size="small" className="block mt-1">
          {error}
        </Text>
      ) : null}
      <ul className="review-evidence mt-2" data-command-search-results="true">
        {results.length === 0 ? (
          <li>
            <Text type="tertiary" size="small">
              {keyword.trim() === '' ? '输入关键字开始搜索' : '没有匹配的页面或对象（只搜索你有权访问的内容）'}
            </Text>
          </li>
        ) : (
          results.map((hit) => (
            <li key={`${hit.kind}-${hit.pagePath}`}>
              <button
                type="button"
                className="sidebar-nav-item"
                data-command-search-hit={hit.kind}
                onClick={() => go(hit.pagePath)}
              >
                {hit.label}
                {hit.caption ? <span className="sidebar-nav-item__label">{hit.caption}</span> : null}
              </button>
            </li>
          ))
        )}
      </ul>
        <div className="mt-1">
          <Button size="small" theme="borderless" onClick={close} data-command-search-close="true">
            关闭（Esc）
          </Button>
        </div>
      </div>
    </Card>
  )
}
