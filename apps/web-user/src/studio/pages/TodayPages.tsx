import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { useNavigate } from 'react-router-dom'
import { Button, Card, Empty, Input, Spin, Tag, Typography } from '@douyinfe/semi-ui'
import { Bell, RefreshCw, Search } from 'lucide-react'
import { activityApi } from '../../lib/api/studio'
import type { ActivityItem, SearchHit, TodoItem } from '../../lib/api/studio'
import { allStudioRoutes } from '../routes'

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

// ---------------------------------------------------------------------------
// 今日工作（W01）
// ---------------------------------------------------------------------------

export function TodayPage() {
  const navigate = useNavigate()
  const { Title, Text } = Typography
  const [todos, setTodos] = useState<TodoItem[]>([])
  const [notes, setNotes] = useState<string[]>([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)
  const [busy, setBusy] = useState(false)

  const load = useCallback(async () => {
    setLoading(true)
    setError(null)
    try {
      const response = await activityApi.today()
      setTodos(response.todos ?? [])
      setNotes(response.notes ?? [])
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
    <div className="console-page" data-studio-page="today">
      <div className="console-page__header">
        <div>
          <Title heading={4} className="!mb-1">
            今日工作
          </Title>
          <Text type="tertiary">需要你做决定的事，每条都能直接跳到具体对象。</Text>
        </div>
        <Button size="small" icon={<RefreshCw size={14} />} onClick={() => void load()} disabled={loading}>
          刷新
        </Button>
      </div>

      {error ? (
        <Card className="console-card mb-3" bodyStyle={{ padding: 14 }} data-today-error="true">
          <Text type="danger">{error}</Text>
        </Card>
      ) : null}

      {loading ? (
        <div className="flex justify-center py-10">
          <Spin tip="正在汇总待办" />
        </div>
      ) : todos.length === 0 ? (
        <Card className="console-card" bodyStyle={{ padding: 24 }} data-today-empty="true">
          <Empty description="当前没有需要你处理的待办。" />
        </Card>
      ) : (
        <>
          <div className="comparison-table" data-today-todos="true">
            <div className="comparison-row comparison-row--head">
              <span>类型</span>
              <span>事项</span>
              <span>数量</span>
              <span>操作</span>
            </div>
            {todos.map((todo) => {
              const href = todoHref(todo)
              return (
                <div key={`${todo.kind}-${todo.projectId}`} className="comparison-row" data-todo-kind={todo.kind}>
                  <span>
                    <Tag size="small" color={todo.kind === 'unread_activity' ? 'grey' : 'amber'}>
                      {TODO_TITLES[todo.kind] ?? todo.kind}
                    </Tag>
                  </span>
                  <span>{todo.summary}</span>
                  <span>{todo.count}</span>
                  <span>
                    {href ? (
                      <Button
                        size="small"
                        theme="borderless"
                        onClick={() => navigate(href)}
                        data-todo-open={todo.kind}
                      >
                        去处理
                      </Button>
                    ) : (
                      <Text type="tertiary" size="small">
                        —
                      </Text>
                    )}
                  </span>
                </div>
              )
            })}
          </div>
          <div className="mt-3 flex items-center gap-3">
            <Button
              size="small"
              icon={<Bell size={14} />}
              disabled={busy}
              onClick={() => void markRead()}
              data-today-mark-read="true"
            >
              全部已读
            </Button>
            {notes.map((note) => (
              <Text key={note} type="tertiary" size="small">
                {note}
              </Text>
            ))}
          </div>
        </>
      )}
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

  const merge = useCallback((incoming: ActivityItem[]) => {
    setItems((previous) => {
      const seen = new Set(previous.map((item) => `${item.source}#${item.eventId}`))
      const merged = [...previous]
      for (const item of incoming) {
        const key = `${item.source}#${item.eventId}`
        if (!seen.has(key)) {
          seen.add(key)
          merged.push(item)
        }
      }
      return merged
    })
  }, [])

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
              <li key={`${item.source}-${item.eventId}`} data-activity-item={item.source}>
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
        icon={<Search size={14} />}
        data-command-search-trigger="true"
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
