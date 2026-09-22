import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { useNavigate, useParams, useSearchParams } from 'react-router-dom'
import { Button, Card, Checkbox, Empty, Input, Select, Spin, Tag, TextArea, Typography } from '@douyinfe/semi-ui'
import { AlertTriangle, ChevronLeft, ChevronRight, Copy, RefreshCw, Save } from 'lucide-react'
import { client } from '../../lib/api'
import { projectPath, studioApi } from '../../lib/api/studio'
import type { ApiBlocker, Page, ReviewDecision, ReviewProjection, SampleSummary, SampleVersionView } from '../../lib/api/studio'
import { useProjectScope } from '../ProjectLayout'
import { CommentPanel } from '../CommentsPanel'
import { currentActorID, enqueue, pendingCount } from '../../lib/pendingQueue'

/**
 * 数据工作区页面（Issue #160 T17）：样本列表、三栏审阅、版本与来源历史。
 *
 * 契约：docs/plans/atelier-implementation.md §3（D01–D03/Q04）、§3.2（URL 参数）、
 * docs/plans/atelier-api-contract.md §2.7、§3.1。
 *
 * T17 验收项里最容易被忽略的三条，在本文件里是显式实现而不是「顺带满足」：
 *
 *  1. **选择范围默认只含当前页**：跨页静默累积会让用户以为选了 40 条、
 *     实际提交 12 条（或反过来）。因此选择状态 = 当前页的勾选，
 *     并明确显示「已选 N（当前页）」。大范围选择走**服务端快照**，
 *     URL 里只出现快照 ID。
 *  2. **请求竞态不得把上一条的证据显示到下一条**：切样本时用请求序号
 *     丢弃过期响应（`latestRequest` ref）。没有它，快速点「下一条」时
 *     先回来的慢响应会覆盖后点开那一条的三栏内容 —— 而用户会据此做出
 *     针对错误样本的判断。
 *  3. **保存判断刷新队列但保留条件与返回位置**：筛选条件来自 URL（search
 *     params），保存后只刷新数据、不动 URL，因此返回时回到同一屏。
 */

type ReviewStatusFilter = '' | 'pending' | 'accepted' | 'quarantined' | 'conflict'

const REVIEW_STATUS_LABEL: Record<string, string> = {
  pending: '待判断',
  accepted: '已接纳',
  quarantined: '已隔离',
  conflict: '存在冲突',
}

function statusColor(status: string): 'amber' | 'green' | 'red' | 'violet' | 'grey' {
  switch (status) {
    case 'accepted':
      return 'green'
    case 'quarantined':
      return 'red'
    case 'conflict':
      return 'violet'
    case 'pending':
      return 'amber'
    default:
      return 'grey'
  }
}

/** 从 URL 读审阅状态筛选（默认只看待判断：队列的第一屏应当是待办）。 */
function reviewStatusFrom(searchParams: URLSearchParams): ReviewStatusFilter {
  const raw = searchParams.get('status')
  if (raw === 'pending' || raw === 'accepted' || raw === 'quarantined' || raw === 'conflict') {
    return raw
  }
  // 未指定时默认待判断；显式 `status=all` 表示看全部。
  if (raw === 'all') return ''
  return 'pending'
}

// ---------------------------------------------------------------------------
// 样本列表（D01）/ 审阅队列（Q04）
// ---------------------------------------------------------------------------

export function SampleListPage({ queueMode = false }: { queueMode?: boolean }) {
  const scope = useProjectScope()
  const navigate = useNavigate()
  const [searchParams, setSearchParams] = useSearchParams()
  const { Title, Text } = Typography

  const reviewStatus = reviewStatusFrom(searchParams)
  const search = searchParams.get('q') ?? ''

  const [samples, setSamples] = useState<SampleSummary[]>([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)
  const [nextCursor, setNextCursor] = useState('')
  // 选择只针对**当前页**（见文件头说明）。
  const [selected, setSelected] = useState<Set<number>>(new Set())
  const [snapshotNotice, setSnapshotNotice] = useState<string | null>(null)
  const [snapshotID, setSnapshotID] = useState<number | null>(null)

  // 请求序号：丢弃过期响应，避免「先发出的慢响应覆盖后发出的结果」。
  const latestRequest = useRef(0)

  const load = useCallback(
    async (cursor: string, append: boolean) => {
      const requestID = latestRequest.current + 1
      latestRequest.current = requestID
      setLoading(true)
      setError(null)
      try {
        const params = new URLSearchParams({ limit: '20' })
        if (reviewStatus !== '') params.set('status', reviewStatus)
        if (search.trim() !== '') params.set('q', search.trim())
        if (cursor !== '') params.set('cursor', cursor)
        const response = await client.get<Page<SampleSummary>>(
          `${projectPath(scope.projectId)}/samples?${params.toString()}`,
        )
        if (requestID !== latestRequest.current) return // 过期响应：丢弃
        const items = response.data.items ?? []
        setSamples((previous) => (append ? [...previous, ...items] : items))
        setNextCursor(response.data.nextCursor ?? '')
        if (!append) setSelected(new Set())
      } catch (loadError) {
        if (requestID !== latestRequest.current) return
        setError(loadError instanceof Error ? loadError.message : '加载样本失败')
      } finally {
        if (requestID === latestRequest.current) setLoading(false)
      }
    },
    [reviewStatus, scope.projectId, search],
  )

  useEffect(() => {
    void load('', false)
  }, [load])

  // 选择快照、质量实验与发布命令都接受 sample_versions.id；样本身份
  // sampleId 只用于页面路由和展示，不能作为冻结范围的键。
  const toggle = useCallback((sampleVersionID: number, checked: boolean) => {
    setSelected((previous) => {
      const next = new Set(previous)
      if (checked) next.add(sampleVersionID)
      else next.delete(sampleVersionID)
      return next
    })
  }, [])

  /**
   * 把当前工作区的范围交给服务端冻结，然后直达发布准备。
   *
   * 选择快照是发布流程的边界：页面不能把样本身份 ID 或当前筛选
   * 直接带到候选命令里。服务端会在创建快照时重新校验项目作用域，
   * 发布页只接收一个短的 `selection` ID。
   */
  const freezeForRelease = useCallback(
    async (sampleVersionIDs?: number[]) => {
      setSnapshotNotice(null)
      try {
        const snapshot = await studioApi.createSelectionSnapshot(scope.projectId, {
          purpose: 'release',
          ...(sampleVersionIDs && sampleVersionIDs.length > 0
            ? { sampleVersionIds: sampleVersionIDs }
            : {
                fromFilter: {
                  reviewStatus: reviewStatus === '' ? undefined : reviewStatus,
                  search: search.trim() === '' ? undefined : search.trim(),
                },
              }),
        })
        setSnapshotID(snapshot.id)
        navigate(`/p/${scope.projectId}/releases/new?selection=${encodeURIComponent(String(snapshot.id))}`)
      } catch (snapshotError) {
        setSnapshotNotice(snapshotError instanceof Error ? snapshotError.message : '冻结选择范围失败')
      }
    },
    [navigate, reviewStatus, scope.projectId, search],
  )

  /** 大范围选择：让**服务端**按当前筛选解析并冻结成快照。 */
  const snapshotAll = useCallback(async () => {
    await freezeForRelease()
  }, [freezeForRelease])

  /** 小范围选择：只冻结当前页明确勾选的内容版本。 */
  const snapshotSelected = useCallback(async () => {
    if (selected.size === 0) {
      setSnapshotNotice('请先选择至少一个内容版本，再准备发布')
      return
    }
    await freezeForRelease(Array.from(selected).sort((left, right) => left - right))
  }, [freezeForRelease, selected])

  const title = queueMode ? '审阅队列' : '样本工作区'

  return (
    <div className="console-page" data-studio-page={queueMode ? 'review-queue' : 'sample-list'}>
      <div className="console-page__header">
        <div>
          <Title heading={4} className="!mb-1">
            {title}
          </Title>
          <Text type="tertiary">
            按审阅状态与关键词在**服务端**筛选与分页；按钮上的数量是服务端统计，不是当前页条目数。
          </Text>
        </div>
        <div className="flex flex-wrap items-center gap-2">
          <Input
            value={search}
            placeholder="按标题或样本键搜索"
            style={{ width: 200 }}
            aria-label="搜索样本"
            onChange={(value) => {
              setSearchParams((params) => {
                if (value.trim() === '') params.delete('q')
                else params.set('q', value)
                return params
              })
            }}
          />
          <Select
            value={reviewStatus === '' ? 'all' : reviewStatus}
            style={{ width: 150 }}
            aria-label="按审阅状态筛选"
            optionList={[
              { value: 'pending', label: '待判断' },
              { value: 'accepted', label: '已接纳' },
              { value: 'quarantined', label: '已隔离' },
              { value: 'conflict', label: '存在冲突' },
              { value: 'all', label: '全部' },
            ]}
            onChange={(value) => {
              setSearchParams((params) => {
                // 写进 URL：刷新/返回都恢复同一筛选（T17 验收项）。
                params.set('status', String(value))
                return params
              })
            }}
          />
          <Button icon={<RefreshCw size={14} />} onClick={() => void load('', false)} disabled={loading}>
            刷新
          </Button>
        </div>
      </div>

      {selected.size > 0 ? (
        <Card className="console-card mb-3" bodyStyle={{ padding: 12 }} data-selection-summary="true">
          <Text size="small">
            已选 {selected.size} 条（**当前页**）。跨页选择请用下方「按筛选条件冻结范围」——
            它由服务端解析，因此不会出现「以为选了 40 条、实际提交 12 条」。
          </Text>
          <div className="mt-2 flex gap-2">
            <Button size="small" theme="solid" type="primary" onClick={() => void snapshotSelected()} data-selection-release="true">
              导出所选并准备发布
            </Button>
            <Button size="small" onClick={() => void snapshotAll()} data-snapshot-all="true">
              按筛选条件全选并准备发布
            </Button>
            <Button size="small" onClick={() => setSelected(new Set())}>
              清空当前页选择
            </Button>
          </div>
        </Card>
      ) : (
        <div className="mb-3">
          <Button size="small" onClick={() => void snapshotAll()} data-snapshot-all="true">
            按当前筛选冻结并准备发布（服务端解析）
          </Button>
        </div>
      )}

      {snapshotNotice ? (
        <Card className="console-card mb-3" bodyStyle={{ padding: 12 }} data-snapshot-notice="true">
          <Text size="small">{snapshotNotice}</Text>
          {snapshotID !== null ? (
            <div className="mt-2">
              <Text type="tertiary" size="small">
                快照 ID：{snapshotID}（URL 只需带它，不需要带 ID 列表）
              </Text>
            </div>
          ) : null}
        </Card>
      ) : null}

      {loading && samples.length === 0 ? (
        <div className="flex justify-center py-10">
          <Spin tip="正在加载样本" />
        </div>
      ) : error ? (
        <Card className="console-card">
          <Text strong className="block">
            加载失败
          </Text>
          <Text type="tertiary">{error}</Text>
          <div className="mt-3">
            <Button size="small" onClick={() => void load('', false)}>
              重试
            </Button>
          </div>
        </Card>
      ) : samples.length === 0 ? (
        <Card className="console-card">
          <Empty
            description={
              queueMode
                ? '这个筛选下没有待办。已接纳的内容不会再出现在默认队列里。'
                : '还没有样本。先运行一个批次产生内容。'
            }
          />
        </Card>
      ) : (
        <>
          <div className="sample-table" data-sample-table="true">
            <div className="sample-row sample-row--head">
              <span />
              <span>样本</span>
              <span>版本</span>
              <span>审阅状态</span>
              <span>操作</span>
            </div>
            {samples.map((sample) => (
              <div key={sample.sampleId} className="sample-row" data-sample-id={sample.resourceId}>
                <Checkbox
                  checked={sample.latestVersionId > 0 && selected.has(sample.latestVersionId)}
                  disabled={sample.latestVersionId <= 0}
                  aria-label={`选择 ${sample.title || sample.sampleKey}`}
                  onChange={(event) => {
                    if (sample.latestVersionId <= 0) return
                    toggle(sample.latestVersionId, Boolean(event.target.checked))
                  }}
                />
                <span>
                  <Text strong>{sample.title || sample.sampleKey}</Text>
                  <Text type="tertiary" size="small" className="block">
                    {sample.resourceId}
                  </Text>
                </span>
                <span>v{sample.latestVersion}（版本 ID {sample.latestVersionId || '暂无'}）</span>
                <span>
                  <Tag size="small" color={statusColor(sample.reviewStatus)}>
                    {REVIEW_STATUS_LABEL[sample.reviewStatus] ?? sample.reviewStatus}
                  </Tag>
                  {sample.aggregateReviewRevision > 0 ? (
                    <Text type="tertiary" size="small" className="block">
                      判断 {sample.aggregateReviewRevision} 次
                    </Text>
                  ) : null}
                </span>
                <span className="flex gap-2">
                  <Button
                    size="small"
                    theme="solid"
                    type="primary"
                    onClick={() =>
                      navigate(
                        `/p/${scope.projectId}/data/${sample.resourceId}` +
                          (searchParams.toString() ? `?${searchParams.toString()}` : ''),
                      )
                    }
                  >
                    审阅
                  </Button>
                  <Button
                    size="small"
                    onClick={() => navigate(`/p/${scope.projectId}/data/${sample.resourceId}/history`)}
                  >
                    来源
                  </Button>
                </span>
              </div>
            ))}
          </div>
          {nextCursor !== '' ? (
            <div className="mt-3 flex justify-center">
              <Button loading={loading} onClick={() => void load(nextCursor, true)}>
                加载更多
              </Button>
            </div>
          ) : null}
        </>
      )}
    </div>
  )
}

// ---------------------------------------------------------------------------
// 三栏审阅（D02）
// ---------------------------------------------------------------------------

export function SampleReviewPage() {
  const scope = useProjectScope()
  const params = useParams()
  const navigate = useNavigate()
  const [searchParams] = useSearchParams()
  const { Title, Text } = Typography
  const sampleID = params.sampleId ?? ''

  const [detail, setDetail] = useState<{ sample: SampleSummary; version: SampleVersionView } | null>(null)
  const [decisions, setDecisions] = useState<ReviewDecision[]>([])
  const [projection, setProjection] = useState<ReviewProjection | null>(null)
  const [blockers, setBlockers] = useState<ApiBlocker[]>([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)
  const [action, setAction] = useState<'accepted' | 'quarantined'>('accepted')
  const [reason, setReason] = useState('')
  const [submitting, setSubmitting] = useState(false)
  const [submitError, setSubmitError] = useState<string | null>(null)
  const [savedNotice, setSavedNotice] = useState<string | null>(null)
  // 离线待同步（T29）：提交失败时**不显示成功**，而是提供「保存为本地草稿」。
  const [offlineNotice, setOfflineNotice] = useState<string | null>(null)
  const [pending, setPending] = useState(0)

  // 竞态防护：切样本时丢弃过期响应（见文件头说明）。
  const latestRequest = useRef(0)
  const contentRef = useRef<HTMLDivElement | null>(null)

  const refreshPending = useCallback(() => {
    const actorId = currentActorID()
    setPending(actorId > 0 ? pendingCount(actorId, scope.projectId) : 0)
  }, [scope.projectId])

  useEffect(() => {
    refreshPending()
  }, [refreshPending])

  const load = useCallback(async () => {
    const requestID = latestRequest.current + 1
    latestRequest.current = requestID
    setLoading(true)
    setError(null)
    try {
      const response = await client.get<{ data?: { sample: SampleSummary; version: SampleVersionView } }>(
        `${projectPath(scope.projectId)}/samples/${sampleID}`,
      )
      if (requestID !== latestRequest.current) return
      setDetail(response.data.data ?? null)

      const version = response.data.data?.version
      if (version) {
        const decisionResponse = await studioApi.listDecisions(scope.projectId, sampleID, version.version)
        if (requestID !== latestRequest.current) return
        setDecisions(decisionResponse.items ?? [])
        setProjection(decisionResponse.projection ?? null)
      }
    } catch (loadError) {
      if (requestID !== latestRequest.current) return
      setError(loadError instanceof Error ? loadError.message : '加载内容失败')
    } finally {
      if (requestID === latestRequest.current) setLoading(false)
    }
  }, [sampleID, scope.projectId])

  useEffect(() => {
    void load()
  }, [load])

  /** 下一条 / 上一条：沿用当前筛选条件（`searchParams`）。 */
  const goRelative = useCallback(
    async (direction: 'next' | 'prev') => {
      const params2 = new URLSearchParams(searchParams)
      params2.set('limit', '50')
      const response = await client.get<Page<SampleSummary>>(
        `${projectPath(scope.projectId)}/samples?${params2.toString()}`,
      )
      const items = response.data.items ?? []
      const index = items.findIndex((item) => item.resourceId === sampleID)
      const target = direction === 'next' ? items[index + 1] : items[index - 1]
      if (!target) {
        // 「最后一条」必须可解释：明确告知，而不是静默什么都不做。
        setSavedNotice(direction === 'next' ? '已经是当前筛选下的最后一条' : '已经是第一条')
        return
      }
      setSavedNotice(null)
      setReason('')
      navigate(
        `/p/${scope.projectId}/data/${target.resourceId}` +
          (searchParams.toString() ? `?${searchParams.toString()}` : ''),
      )
    },
    [navigate, sampleID, scope.projectId, searchParams],
  )

  const submit = useCallback(async () => {
    if (!detail) return
    if (reason.trim() === '') {
      setSubmitError('理由必填：没有理由的判断无法被复核')
      return
    }
    setSubmitting(true)
    setSubmitError(null)
    try {
      const result = await studioApi.submitDecision(scope.projectId, sampleID, detail.version.version, {
        // 两套序号都必须带：证据版本防「旧证据迟到提交」，
        // 个人序号防「同一人并发更正」（后到者 409 并保留理由）。
        evidenceRevision: projection?.evidenceRevision ?? 0,
        reviewerRevision: (projection?.decisionCount ?? 0) + 1,
        action,
        reason: reason.trim(),
      })
      setBlockers(result.blockers ?? [])
      setReason('')
      setSavedNotice('判断已保存')
      // 刷新数据但**不动 URL**：因此返回时回到同一筛选与同一屏（T17 验收项）。
      await load()
    } catch (submitErrorValue) {
      // 409 时必须保留用户输入 —— 清空理由会让用户重打一遍，
      // 而那正是「过期返回 409 并保留输入」要避免的。
      setSubmitError(submitErrorValue instanceof Error ? submitErrorValue.message : '提交失败')
    } finally {
      setSubmitting(false)
    }
  }, [action, detail, load, projection, reason, sampleID, scope.projectId])

  /**
   * 保存为本地草稿（T29）。
   *
   * 只在**提交失败**时提供：离线写入不显示成功，这里返回的语义是
   * 「待同步（未提交）」。判断类意图可以入队（非收费、可安全重放），
   * 而启动运行/发布等操作由 pendingQueue 的允许清单直接拒绝。
   */
  const saveOfflineDraft = useCallback(() => {
    const actorId = currentActorID()
    const result = enqueue({
      kind: 'review_decision_draft',
      actorId,
      workspaceId: scope.projectId,
      objectRef: `sample_version:${detail?.version.versionId ?? 0}`,
      revision: projection?.evidenceRevision ?? 0,
      payload: { body: reason.trim(), action },
    })
    if (result.status === 'pending') {
      setOfflineNotice('已保存为本机草稿（待同步，**未提交**）：联网后需先登录并确认，系统不会后台自动提交')
      refreshPending()
    } else {
      setSubmitError(result.reason)
    }
  }, [action, detail, projection, reason, refreshPending, scope.projectId])

  const copyContent = useCallback(async () => {
    if (!detail) return
    const text = JSON.stringify(detail.version.payload, null, 2)
    try {
      if (navigator.clipboard?.writeText) {
        await navigator.clipboard.writeText(text)
        setSavedNotice('内容已复制')
        return
      }
      throw new Error('clipboard unavailable')
    } catch {
      // 复制失败必须有回退路径：只弹一句「复制失败」而不给替代方案，
      // 用户就只能在长文本里手工选中。
      setSavedNotice('浏览器不允许自动复制：请手动选中下方只读内容')
    }
  }, [detail])

  if (loading && !detail) {
    return (
      <div className="flex justify-center py-10">
        <Spin tip="正在加载内容" />
      </div>
    )
  }
  if (error || !detail) {
    return (
      <Card className="console-card">
        <Text strong className="block">
          内容加载失败
        </Text>
        <Text type="tertiary">{error ?? '未知错误'}</Text>
        <div className="mt-3">
          <Button size="small" onClick={() => void load()}>
            重试
          </Button>
        </div>
      </Card>
    )
  }

  const effective = projection?.effectiveAction ?? 'pending'

  return (
    <div className="console-page review-pane" data-studio-page="sample-review" data-effective-action={effective}>
      <div className="console-page__header">
        <div>
          <Title heading={4} className="!mb-1">
            {detail.sample.title || detail.sample.sampleKey}
          </Title>
          <Text type="tertiary">
            {detail.sample.resourceId} · v{detail.version.version} · 内容 hash{' '}
            {detail.version.contentHash.slice(0, 12)}
          </Text>
        </div>
        <div className="flex flex-wrap items-center gap-2">
          <Tag color={statusColor(effective)} data-effective-tag="true">
            {REVIEW_STATUS_LABEL[effective] ?? effective}
          </Tag>
          <Button size="small" icon={<ChevronLeft size={14} />} onClick={() => void goRelative('prev')}>
            上一条
          </Button>
          <Button size="small" icon={<ChevronRight size={14} />} onClick={() => void goRelative('next')}>
            下一条
          </Button>
          <Button size="small" onClick={() => navigate(`/p/${scope.projectId}/data/${sampleID}/history`)}>
            版本与来源
          </Button>
        </div>
      </div>

      {savedNotice ? (
        <Card className="console-card mb-3" bodyStyle={{ padding: 10 }} data-review-notice="true">
          <Text size="small">{savedNotice}</Text>
        </Card>
      ) : null}

      {/* 三栏：队列 / 内容 / 证据。每栏独立，因此「内容 focus」不会因
          判断保存而丢失（保存只刷新数据，不卸载内容栏）。 */}
      <div className="review-pane__columns">
        <section className="review-pane__column" aria-label="待判断内容">
          <Text strong className="block mb-2">
            队列
          </Text>
          <ul className="review-queue">
            {decisions.length === 0 ? (
              <li className="review-queue__empty">还没有判断</li>
            ) : (
              decisions.map((decision) => (
                <li key={decision.id} data-decision-id={decision.id}>
                  <Tag size="small" color={statusColor(decision.action)}>
                    {decision.action === 'accepted' ? '接纳' : '隔离'}
                  </Tag>
                  <Text size="small" className="block">
                    {decision.reason}
                  </Text>
                  <Text type="tertiary" size="small">
                    审阅者 {decision.reviewerId} · 第 {decision.reviewerRevision} 次
                    {decision.supersedes ? ` · 更正 #${decision.supersedes}` : ''}
                    {decision.resolutionOf ? ` · 协调 #${decision.resolutionOf}` : ''}
                  </Text>
                </li>
              ))
            )}
          </ul>
        </section>

        <section
          className="review-pane__column review-pane__content"
          aria-label="只读内容"
          ref={contentRef}
          tabIndex={-1}
          data-content-focus="true"
        >
          <div className="flex items-center justify-between mb-2">
            <Text strong>内容（只读）</Text>
            <Button size="small" icon={<Copy size={13} />} onClick={() => void copyContent()}>
              复制
            </Button>
          </div>
          {/* 内容只读：本区没有任何输入控件，判断也不会改写它。 */}
          <pre className="review-content" data-content-readonly="true">
            {JSON.stringify(detail.version.payload, null, 2)}
          </pre>
        </section>

        <section className="review-pane__column" aria-label="证据与判断">
          <Text strong className="block mb-2">
            证据与判断
          </Text>
          <ul className="review-evidence">
            <li>
              必需证据版本：<code>{projection?.evidenceRevision ?? 0}</code>
            </li>
            <li>
              判断次数：<code>{projection?.decisionCount ?? 0}</code> · 聚合序号{' '}
              <code>{projection?.aggregateReviewRevision ?? 0}</code>
            </li>
            <li>
              生成来源：<code>{detail.version.source.blueprintContentHash.slice(0, 8) || '（未记录）'}</code>
            </li>
          </ul>

          {blockers.length > 0 ? (
            <div className="mt-2" data-review-blockers="true">
              {blockers.map((blocker) => (
                <div key={blocker.code} className="flex items-start gap-2 mb-1">
                  <AlertTriangle size={14} className="mt-1 text-amber-500" aria-hidden />
                  <Text size="small">{blocker.message}</Text>
                </div>
              ))}
            </div>
          ) : null}

          {effective === 'conflict' ? (
            <Card className="console-card mb-2" bodyStyle={{ padding: 10 }} data-conflict-notice="true">
              <Text size="small">
                存在相反判断：需要项目负责人在此追加协调决定后才能解除发布阻塞。
              </Text>
            </Card>
          ) : null}

          <div className="mt-2">
            <Text type="tertiary" size="small" className="block mb-1">
              处置
            </Text>
            <Select
              value={action}
              style={{ width: '100%' }}
              aria-label="选择处置"
              optionList={[
                { value: 'accepted', label: '接纳' },
                { value: 'quarantined', label: '隔离' },
              ]}
              onChange={(value) => setAction(value === 'quarantined' ? 'quarantined' : 'accepted')}
            />
            <Text type="tertiary" size="small" className="block mt-2 mb-1">
              理由（必填）
            </Text>
            <TextArea
              value={reason}
              onChange={(value) => setReason(value)}
              autosize={{ minRows: 3, maxRows: 6 }}
              placeholder="例如：规则命中为误报，推理链完整"
              data-field="review-reason"
            />
            {submitError ? (
              <div className="wizard-field__error mt-1" role="alert" data-review-submit-error="true">
                {submitError}
              </div>
            ) : null}
            {/* 离线待同步（T29）：提交失败时提供本地草稿，并明确「未提交」。 */}
            {submitError ? (
              <div className="mt-1">
                <Button size="small" theme="borderless" onClick={saveOfflineDraft} data-review-offline-draft="true">
                  保存为本地草稿（待同步）
                </Button>
              </div>
            ) : null}
            {offlineNotice ? (
              <Text type="warning" size="small" className="block mt-1" data-review-offline-notice="true">
                {offlineNotice}
              </Text>
            ) : null}
            {pending > 0 ? (
              <Text type="tertiary" size="small" className="block mt-1" data-review-pending-count="true">
                待同步（未提交）：{pending} 条。联网后请重新登录并确认，系统不会后台自动提交。
              </Text>
            ) : null}
            <div className="mt-2">
              <Button
                theme="solid"
                type="primary"
                icon={<Save size={14} />}
                loading={submitting}
                onClick={() => void submit()}
              >
                保存判断
              </Button>
            </div>
          </div>

          {/* 评论面板（T27）：锚定**当前内容版本**，与判断分开 —— 讨论不改处置。 */}
          {detail?.version?.versionId ? (
            <div className="mt-3">
              <CommentPanel
                projectId={scope.projectId}
                anchorKind="sample_version"
                anchorId={detail.version.versionId}
              />
            </div>
          ) : null}
        </section>
      </div>
    </div>
  )
}

// ---------------------------------------------------------------------------
// 版本与来源（D03）
// ---------------------------------------------------------------------------

export function SampleHistoryPage() {
  const scope = useProjectScope()
  const params = useParams()
  const { Title, Text } = Typography
  const sampleID = params.sampleId ?? ''
  const [versions, setVersions] = useState<SampleVersionView[]>([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)

  useEffect(() => {
    let cancelled = false
    void (async () => {
      try {
        const response = await client.get<Page<SampleVersionView>>(
          `${projectPath(scope.projectId)}/samples/${sampleID}/history?limit=50`,
        )
        if (!cancelled) setVersions(response.data.items ?? [])
      } catch (loadError) {
        if (!cancelled) setError(loadError instanceof Error ? loadError.message : '加载版本历史失败')
      } finally {
        if (!cancelled) setLoading(false)
      }
    })()
    return () => {
      cancelled = true
    }
  }, [sampleID, scope.projectId])

  const latest = useMemo(() => versions[0], [versions])

  if (loading) {
    return (
      <div className="flex justify-center py-10">
        <Spin tip="正在加载版本历史" />
      </div>
    )
  }
  if (error) {
    return (
      <Card className="console-card">
        <Text strong className="block">
          加载失败
        </Text>
        <Text type="tertiary">{error}</Text>
      </Card>
    )
  }

  return (
    <div className="console-page" data-studio-page="sample-history">
      <div className="console-page__header">
        <div>
          <Title heading={4} className="!mb-1">
            版本与来源
          </Title>
          <Text type="tertiary">
            内容只追加，永不覆盖；每一版都记录它生成时引用的标准与蓝图 hash。
          </Text>
        </div>
      </div>
      {versions.length === 0 ? (
        <Card className="console-card">
          <Empty description="还没有内容版本。" />
        </Card>
      ) : (
        <ol className="version-timeline">
          {versions.map((version) => (
            <li key={version.versionId} data-version={version.version}>
              <Card className="console-card" bodyStyle={{ padding: 14 }}>
                <div className="flex items-center justify-between gap-2">
                  <Text strong>
                    v{version.version}
                    {latest && version.version === latest.version ? '（最新）' : ''}
                  </Text>
                  <Text type="tertiary" size="small">
                    {version.createdAt}
                  </Text>
                </div>
                <ul className="review-evidence">
                  <li>
                    内容 hash：<code>{version.contentHash}</code>
                  </li>
                  <li>
                    来源批次：<code>{version.batchId ?? '（无）'}</code> · 单元{' '}
                    <code>{version.batchItemId ?? '（无）'}</code> · 第 {version.attempt} 次尝试
                  </li>
                  <li>
                    标准 hash：<code>{version.source.standardContentHash || '（未引用）'}</code>
                  </li>
                  <li>
                    蓝图 hash：<code>{version.source.blueprintContentHash || '（未引用）'}</code>
                  </li>
                </ul>
              </Card>
            </li>
          ))}
        </ol>
      )}
    </div>
  )
}
