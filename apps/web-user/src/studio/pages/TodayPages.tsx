import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { useNavigate } from 'react-router-dom'
import { Button, Card, Empty, Input, Spin, Tag, Typography } from '@douyinfe/semi-ui'
import { Bell, RefreshCw, Search } from 'lucide-react'
import { activityApi } from '../../lib/api/studio'
import type { ActivityItem, SearchHit, TodoItem, WorkspaceOverview } from '../../lib/api/studio'
import { allStudioRoutes, fillRoutePathByKey } from '../routes'

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
  pending_review: '待判断',
  pilot_comparable: '试制可比',
  failed_recovery: '失败恢复',
  release_blocked: '候选阻塞',
  unread_activity: '动态未读',
}

function todoHref(todo: TodoItem): string | undefined {
  return todo.links?.page
}

function studioPath(key: string): string {
  return fillRoutePathByKey(key, {})
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

  // 项目名称不在 /v1/today 契约中，因此只展示服务端返回的项目 ID 和待办事实，
  // 不用示例项目名或猜测项目详情链接。
  const projectTodos = useMemo(() => {
    const grouped = new Map<number, TodoItem[]>()
    for (const todo of todos) {
      if (todo.projectId <= 0) continue
      const current = grouped.get(todo.projectId) ?? []
      current.push(todo)
      grouped.set(todo.projectId, current)
    }
    return Array.from(grouped.entries())
      .map(([projectId, items]) => {
        const latest = items.reduce((candidate, item) => {
          if (!candidate) return item
          return Date.parse(item.updatedAt) > Date.parse(candidate.updatedAt) ? item : candidate
        }, items[0])
        const href = items.map(todoHref).find((value): value is string => Boolean(value))
        return { projectId, items, latest, href }
      })
      .sort((left, right) => Date.parse(right.latest.updatedAt) - Date.parse(left.latest.updatedAt))
      .slice(0, 4)
  }, [todos])

  const decisionTodos = useMemo(
    () => todos.filter((todo) => todo.kind !== 'unread_activity'),
    [todos],
  )
  const unreadTodo = useMemo(
    () => todos.find((todo) => todo.kind === 'unread_activity'),
    [todos],
  )

  const releaseTodo = useMemo(
    () => todos.find((todo) => todo.kind === 'release_blocked'),
    [todos],
  )
  const releaseHref = releaseTodo ? todoHref(releaseTodo) : undefined

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
    <div className="console-page atelier-today-page" data-studio-page="today">
      <section className="atelier-today-hero">
        <div>
          <div className="eyebrow">TODAY / YOUR WORKSPACE</div>
          <Text type="tertiary">你的工作区 · 今日工作</Text>
          <h1>把下一份训练数据，<br />做得更有把握。</h1>
          <Text type="tertiary" className="atelier-hero-copy">先解决值得你关注的决定，再继续生产。</Text>
          <div className="atelier-action-row">
            <Button theme="solid" type="primary" onClick={() => navigate(studioPath('new'))}>＋ 开始一个数据项目</Button>
            <Button onClick={() => navigate(studioPath('recipes'))}>浏览生产方案</Button>
            <Button type="tertiary" icon={<RefreshCw size={14} />} onClick={() => void load()} disabled={loading}>刷新</Button>
          </div>
        </div>
        <div className="atelier-hero-mark">
          <button
            type="button"
            className="atelier-hero-journey-cta"
            aria-label="查看从目标到证据再到交付的项目流程"
            title="查看项目流程"
            onClick={() => navigate(studioPath('projects'))}
          >
            <span>目标 → 证据 → 交付</span>
            <small>查看项目流程 →</small>
          </button>
        </div>
      </section>

      {error ? (
        <Card className="console-card mb-3" bodyStyle={{ padding: 14 }} data-today-error="true">
          <Text type="danger">{error}</Text>
        </Card>
      ) : null}

      {/*
        issue #197 第 10 条：甲方原话是「今日工作展示的数据不对，应当是一个
        工作台总览的效果」。以前全页只有一个数字（7 条未读动态）。
        这里的每个数字都可点进对应列表，且与列表页读的是同一份事实。
      */}
      {overview ? (
        <section className="atelier-overview-grid" data-today-overview="true">
          <a className="atelier-overview-tile" href={studioPath('projects')} data-overview-tile="projects">
            <span className="eyebrow">数据项目</span>
            <strong>{overview.projectCount}</strong>
            <small>你可见的项目</small>
          </a>
          <a className="atelier-overview-tile" href={studioPath('today')} data-overview-tile="running">
            <span className="eyebrow">进行中批次</span>
            <strong>{overview.runningBatches}</strong>
            <small>排队 / 运行 / 暂停请求中</small>
          </a>
          <a className="atelier-overview-tile" href={studioPath('today')} data-overview-tile="shortfall">
            <span className="eyebrow">产出缺口</span>
            <strong className={overview.batchesWithShortfall > 0 ? 'atelier-overview-tile--alert' : undefined}>
              {overview.batchesWithShortfall}
            </strong>
            <small>
              计划 {overview.totalPlannedUnits} · 完成 {overview.totalCompletedUnits}
            </small>
          </a>
          <a className="atelier-overview-tile" href={studioPath('activity')} data-overview-tile="pending">
            <span className="eyebrow">待人工判断</span>
            <strong className={overview.pendingReview > 0 ? 'atelier-overview-tile--alert' : undefined}>
              {overview.pendingReview}
            </strong>
            <small>进入项目「审阅」处理</small>
          </a>
          <a className="atelier-overview-tile" href={studioPath('today')} data-overview-tile="produced">
            <span className="eyebrow">近 7 天产出</span>
            <strong>{overview.producedLast7Days}</strong>
            <small>新增样本版本数</small>
          </a>
          <a className="atelier-overview-tile" href={studioPath('deliveries')} data-overview-tile="releases">
            <span className="eyebrow">交付</span>
            <strong>{overview.publishedReleases}</strong>
            <small>已发布 · 被挡住 {overview.blockedReleases}</small>
          </a>
        </section>
      ) : null}

      <div className="atelier-today-grid">
        <section className="atelier-decision-panel">
          <div className="atelier-section-heading"><div><div className="eyebrow">DECISIONS</div><h2>需要你的决定</h2></div></div>
          {loading ? (
            <div className="atelier-inline-state"><Spin tip="正在汇总待办" /></div>
          ) : decisionTodos.length === 0 ? (
            <div className="atelier-inline-state" data-today-empty="true"><Empty description="当前没有需要你处理的待办。" /></div>
          ) : (
            <div className="atelier-todo-list" data-today-todos="true">
              {decisionTodos.map((todo) => {
                const href = todoHref(todo)
                return (
                  <div key={`${todo.kind}-${todo.projectId}`} className="atelier-todo-row" data-todo-kind={todo.kind}>
                    <div><Tag size="small" color={todo.kind === 'unread_activity' ? 'grey' : 'amber'}>{TODO_TITLES[todo.kind] ?? todo.kind}</Tag><strong>{todo.summary}</strong><Text type="tertiary" size="small">{todo.count} 项待处理</Text></div>
                    {href ? <Button size="small" onClick={() => navigate(href)}>去处理 →</Button> : null}
                  </div>
                )
              })}
            </div>
          )}
        </section>
        <div className="atelier-today-side">
          <aside className="atelier-calendar-panel">
            <div className="eyebrow">THIS WEEK</div>
            <h2>交付日历</h2>
            {releaseTodo ? (
              <>
                <strong className="atelier-calendar-date">候选更新 · {formatTodoDate(releaseTodo.updatedAt)}</strong>
                <Text type="tertiary">项目 #{releaseTodo.projectId}</Text>
                <Text type="tertiary" size="small">{releaseTodo.summary}</Text>
                {releaseHref ? <Button theme="borderless" onClick={() => navigate(releaseHref)}>查看发布候选 →</Button> : null}
              </>
            ) : (
              <>
                <strong className="atelier-calendar-date">暂无排期</strong>
                <Text type="tertiary">还没有服务端返回的交付候选。</Text>
                <Button theme="borderless" onClick={() => navigate(studioPath('projects'))}>查看项目 →</Button>
              </>
            )}
          </aside>
          <aside className="atelier-workstyle-panel">
            <div className="eyebrow">YOUR WAY OF WORKING</div>
            <h2>你的工作方式</h2>
            <p>设计方案 → 小批试制 → 扩量 → 审阅 → 发布。</p>
            <Text type="tertiary" size="small">每次运行独立记录，每次发布固定内容。你可以随时回到修改前一版。</Text>
            <Button theme="borderless" onClick={() => navigate(studioPath('help'))}>了解 Atelier 旅程 →</Button>
          </aside>
        </div>
      </div>

      <section className="atelier-continue-panel">
        <div className="atelier-section-heading"><div><div className="eyebrow">PROJECTS</div><h2>继续项目</h2></div><Button theme="borderless" onClick={() => navigate(studioPath('projects'))}>查看全部 →</Button></div>
        {projectTodos.length === 0 ? (
          <div className="atelier-inline-state">暂无可继续的项目。</div>
        ) : (
          <div className="atelier-project-mini-grid">
            {projectTodos.map(({ projectId, items, latest, href }) => (
              <button key={projectId} type="button" disabled={!href} onClick={() => { if (href) navigate(href) }}>
                <strong>项目 #{projectId}</strong>
                <Text type="tertiary">{items.map((item) => TODO_TITLES[item.kind] ?? item.kind).join(' · ')}</Text>
                <small>{latest.summary} · 最近更新 {formatTodoDate(latest.updatedAt)}</small>
              </button>
            ))}
          </div>
        )}
      </section>
      {/* 保留原始动态提示与已读动作，但视觉上从决策内容中分离。 */}
      {!loading && unreadTodo ? (
        <div className="mt-3 flex flex-wrap items-center gap-3" data-today-unread="true">
          <Tag size="small" color="grey">动态未读</Tag>
          <Text type="tertiary" size="small">{unreadTodo.summary}</Text>
          {todoHref(unreadTodo) ? <Button size="small" theme="borderless" onClick={() => navigate(todoHref(unreadTodo) as string)}>查看动态 →</Button> : null}
          <Button size="small" icon={<Bell size={14} />} disabled={busy} onClick={() => void markRead()} data-today-mark-read="true">全部已读</Button>
          {notes.map((note) => <Text key={note} type="tertiary" size="small">{note}</Text>)}
        </div>
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
