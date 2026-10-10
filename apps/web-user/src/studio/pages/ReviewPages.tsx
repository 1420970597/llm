import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { useNavigate, useParams, useSearchParams } from 'react-router-dom'
import { Button, Card, Checkbox, Empty, Input, Select, Spin, Tag, TextArea, Typography } from '@douyinfe/semi-ui'
import { AlertTriangle, ChevronLeft, ChevronRight, Copy, Expand, Minimize2, RefreshCw, Save } from 'lucide-react'
import { client } from '../../lib/api'
import { projectNumericId, projectPath, studioApi } from '../../lib/api/studio'
import type {
  ApiBlocker,
  Page,
  ProjectCapabilities,
  ReviewDecision,
  ReviewProjection,
  SampleCapabilities,
  SampleSummary,
  SampleVersionView,
} from '../../lib/api/studio'
import { useProjectScope } from '../ProjectLayout'
import { projectHref } from '../StudioLayout'
import { CommentPanel } from '../CommentsPanel'
import { currentActorID, enqueue, pendingCount } from '../../lib/pendingQueue'
// #211：审阅状态文案与颜色收敛到 enumLabels 的单一来源。
// 原先本文件有一个本地 `REVIEW_STATUS_LABEL`，兜底是 `?? status`
// （未知值直接漏出内部枚举），而 QualityPages 干脆没有映射。
import { describeReviewStatus, reviewStatusColor } from '../../lib/enumLabels'

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

/**
 * 从 URL 读审阅状态筛选。
 *
 * 默认值**按页面语义分化**（issue #194）：审阅队列的第一屏应当是待办，
 * 而「数据」的第一屏应当是「这一版里有什么」—— 后者默认看全部状态，
 * 否则两个入口在观感上仍然是同一页。
 *
 * 显式 `status=all` 一律表示看全部（与默认值无关，保证 URL 可分享）。
 */
function reviewStatusFrom(searchParams: URLSearchParams, queueMode: boolean): ReviewStatusFilter {
  const raw = searchParams.get('status')
  if (raw === 'pending' || raw === 'accepted' || raw === 'quarantined' || raw === 'conflict') {
    return raw
  }
  if (raw === 'all') return ''
  return queueMode ? 'pending' : ''
}

// ---------------------------------------------------------------------------
// 样本列表（D01）/ 审阅队列（Q04）
// ---------------------------------------------------------------------------

export function SampleListPage({ queueMode = false }: { queueMode?: boolean }) {
  const scope = useProjectScope()
  const navigate = useNavigate()
  const [searchParams, setSearchParams] = useSearchParams()
  const { Title, Text } = Typography

  const reviewStatus = reviewStatusFrom(searchParams, queueMode)
  // 数据默认全量也必须显式传 all；服务端缺省 status 是待判断，不能靠缺省表达全量。
  const showAllStatuses = reviewStatus === ''
  const search = searchParams.get('q') ?? ''

  const [samples, setSamples] = useState<SampleSummary[]>([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)
  const [nextCursor, setNextCursor] = useState('')
  // 选择只针对**当前页**（见文件头说明）。
  const [selected, setSelected] = useState<Set<number>>(new Set())
  const [snapshotNotice, setSnapshotNotice] = useState<string | null>(null)
  const [snapshotID, setSnapshotID] = useState<number | null>(null)
  const [freezing, setFreezing] = useState(false)
  const [projectCapabilities, setProjectCapabilities] = useState<ProjectCapabilities | null>(null)

  useEffect(() => {
    let cancelled = false
    void studioApi.overviewEnvelope(scope.projectId).then((overview) => {
      if (!cancelled) setProjectCapabilities(overview.capabilities)
    }).catch(() => {
      if (!cancelled) setProjectCapabilities(null)
    })
    return () => { cancelled = true }
  }, [scope.projectId])

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
        // 空字符串有两种语义：未指定状态时后端默认 pending；显式
        // `status=all` 则必须把 all 传给服务端，才能真正查询全量。
        if (showAllStatuses) params.set('status', 'all')
        else params.set('status', reviewStatus)
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
    [reviewStatus, scope.projectId, search, showAllStatuses],
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
      if (freezing) return
      setSnapshotNotice(null)
      setFreezing(true)
      try {
        const snapshot = await studioApi.createSelectionSnapshot(scope.projectId, {
          purpose: 'release',
          ...(sampleVersionIDs && sampleVersionIDs.length > 0
            ? { sampleVersionIds: sampleVersionIDs }
            : {
                fromFilter: {
                  // issue #203：冻结时必须**显式带上意图**，不能靠「字段缺省」表达全量。
                  // 旧实现把「无筛选」（reviewStatus 为空串）填成 `{}`，服务端于是解析出
                  // 全量范围，而候选页却把它称为「已接纳」—— 两处从此永久不一致。
                  // 空筛选明确写为 `all`（服务端已有该语义），快照的解释字段因此能自证
                  // 「这份范围含未审阅内容」。
                  reviewStatus: reviewStatus === '' ? 'all' : reviewStatus,
                  search: search.trim() === '' ? undefined : search.trim(),
                },
              }),
        })
        setSnapshotID(snapshot.id)
        navigate(`${projectHref('project.newRelease', scope.projectId)}?selection=${encodeURIComponent(String(snapshot.id))}`)
      } catch (snapshotError) {
        setSnapshotNotice(snapshotError instanceof Error ? snapshotError.message : '冻结选择范围失败')
      } finally {
        setFreezing(false)
      }
    },
    [freezing, navigate, reviewStatus, scope.projectId, search],
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

  /**
   * 「数据」与「审阅」必须是两个不同的页面（issue #194 与 #197 第 12 条）。
   *
   * 缺陷形态：两个菜单项进入后标题不同，却是同一张表、同一段说明、同一个
   * 主操作按钮，甲方无法预期点进去看到什么。
   * 修复方式不是把其中一个删掉，而是让两者回答**不同的问题**：
   *   数据   = 这一版里有什么？（默认看全部状态，主操作是导出/发布）
   *   审阅   = 哪一条需要我判断？（默认只看待判断，主操作是逐条判断）
   * 两者共用同一份服务端查询与行渲染是有意的（同一个事实只有一个实现），
   * 但**默认值、说明文案、主操作与空状态**必须不同。
   */
  const title = queueMode ? '审阅队列' : '数据'
  const firstReviewable = samples.find((sample) => sample.reviewStatus === 'pending' && sample.capabilities?.canReview)
  const sampleQuery = new URLSearchParams(searchParams)
  if (!sampleQuery.has('status')) sampleQuery.set('status', showAllStatuses ? 'all' : reviewStatus)

  return (
    <div className="console-page" data-studio-page={queueMode ? 'review-queue' : 'sample-list'}>
      <div className="console-page__header">
        <div>
          <Title heading={4} className="!mb-1">
            {title}
          </Title>
          <Text type="tertiary">{queueMode ? '接纳合格样本，隔离不合格样本。' : '查看、审阅并发布内容版本。'}</Text>
        </div>
        {firstReviewable ? <Button theme="solid" type="primary" onClick={() => navigate(
          `${projectHref('project.sample', scope.projectId, { sampleId: firstReviewable.resourceId })}?${sampleQuery.toString()}`,
        )}>开始审阅</Button> : null}
      </div>
      <div className="product-data-toolbar">
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
          <Button aria-label="刷新样本" icon={<RefreshCw size={14} />} onClick={() => void load('', false)} disabled={loading}>
            刷新
          </Button>
        </div>
      </div>

      {selected.size > 0 ? (
        <Card className="console-card mb-3" bodyStyle={{ padding: 12 }} data-selection-summary="true">
          <Text size="small">
            已选 {selected.size} 条（当前页）
          </Text>
          <div className="mt-2 flex gap-2">
            {projectCapabilities?.canPublish ? (
              <Button size="small" theme="solid" type="primary" loading={freezing} onClick={() => void snapshotSelected()} data-selection-release="true">
                发布所选
              </Button>
            ) : null}
            {projectCapabilities?.canPublish ? (
              <Button size="small" disabled={freezing} onClick={() => void snapshotAll()} data-snapshot-all="true">
                发布当前筛选
              </Button>
            ) : null}
            <Button size="small" onClick={() => setSelected(new Set())}>
              清空选择
            </Button>
          </div>
        </Card>
      ) : queueMode ? (
        /* 审阅队列的主操作是「判断」，不是「导出」：默认整体冻结会让用户在
           还没看内容的情况下就进入发布流程（issue #194 的 CTA 完全相同的成因）。 */
        <div className="mb-3">
          <Text type="tertiary" size="small">
            已加载 {samples.length} 条{nextCursor ? '，还有更多' : ''}。
          </Text>
        </div>
      ) : (
        <div className="mb-3">
          {projectCapabilities?.canPublish ? (
            <>
              <Button size="small" loading={freezing} onClick={() => void snapshotAll()} data-snapshot-all="true">
                发布当前筛选
              </Button>
              {/* issue #203：按钮必须如实声明冻结范围的**语义**。
                  在「全部」筛选（reviewStatus 为空串）下，冻结的是全量，
                  含未审阅内容；旧实现把同一件事写成了「导出/发布当前筛选」，
                  于是用户把它误解为「已接纳」。 */}
              <Text
                type="tertiary"
                size="small"
                className="block mt-2"
                data-snapshot-scope-intent="true"
              >
                {reviewStatus === ''
                  ? '范围含未审阅内容；发布前会检查接纳状态。'
                  : `范围：${describeReviewStatus(reviewStatus)}`}
              </Text>
            </>
          ) : null}
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
          <Empty description={search.trim() ? '没有匹配的样本。' : reviewStatus === 'pending' ? '没有待判断样本。' : '当前筛选下没有样本。'} />
          <div className="console-page__actions">
            {search.trim() || reviewStatus !== '' ? <Button onClick={() => setSearchParams({ status: 'all' })}>查看全部数据</Button> : null}
            <Button onClick={() => navigate(projectHref('project.runs', scope.projectId))}>去生产</Button>
            {projectCapabilities?.canRun ? <Button onClick={() => navigate(projectHref('project.sourceImport', scope.projectId))}>导入数据集</Button> : null}
          </div>
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
                <span>v{sample.latestVersion}</span>
                <span>
                  <span title={sample.reviewStatus}>
                    <Tag size="small" color={reviewStatusColor(sample.reviewStatus)}>
                      {describeReviewStatus(sample.reviewStatus)}
                    </Tag>
                  </span>
                  {sample.aggregateReviewRevision > 0 ? (
                    <Text type="tertiary" size="small" className="block">
                      判断 {sample.aggregateReviewRevision} 次
                    </Text>
                  ) : null}
                </span>
                <span className="flex gap-2">
                  {/* 「数据」里的主操作是**预览内容**（#197 第 3 条：数据应当可以预览），
                      「审阅」里的主操作才是判断。两个入口的行操作因此不同名同形。 */}
                  <Button
                    size="small"
                    theme={queueMode && sample.capabilities?.canReview ? 'solid' : 'borderless'}
                    type={queueMode && sample.capabilities?.canReview ? 'primary' : 'tertiary'}
                    onClick={() =>
                      navigate(
                        `${projectHref('project.sample', scope.projectId, { sampleId: sample.resourceId })}` +
                          `?${sampleQuery.toString()}`,
                      )
                    }
                  >
                    {queueMode && sample.capabilities?.canReview ? '审阅' : '查看内容'}
                  </Button>
                  <Button
                    size="small"
                    onClick={() => navigate(projectHref('project.sampleHistory', scope.projectId, { sampleId: sample.resourceId }))}
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
// 内容预览（D02 的「人话」视图）
// ---------------------------------------------------------------------------

/** 字段 → 中文标题（issue #197 第 3 条）。未知字段保留原名并加「（其它字段）」。 */
const PAYLOAD_FIELD_LABELS: Record<string, string> = {
  question: '问题',
  reasoning: '推理过程',
  answer: '答案',
  teacherPrompt: '教师提示词',
  rewardRubric: '奖励判据',
  systemPrompt: '系统提示词',
  userPrompt: '用户提示词',
  source: '来源类型',
}

/** 预览面板展示的字段顺序（未知字段排在后面）。 */
const PAYLOAD_FIELD_ORDER = ['question', 'reasoning', 'answer', 'teacherPrompt', 'rewardRubric']

type PayloadEntry = { key: string; label: string; text: string }

/**
 * 把样本 payload 拆成「字段 → 可读文本」。
 *
 * 为什么需要它（issue #197 第 3 条）：审阅页的「内容」区以前直接
 * `JSON.stringify(payload, null, 2)`，于是用户看到的是
 * `{"answer": "...", "question": "...", "reasoning": "..."}` 连同 `\n` 转义。
 * 用户真正要判断的是「问法对不对、推理是否完整、答案是不是中文长链」，
 * 而不是 JSON 是否合法。原始 JSON 仍然保留（作为可切换的次要视图），
 * 因为排查格式问题时它是必需的。
 */
function payloadEntries(payload: unknown): PayloadEntry[] {
  if (!payload || typeof payload !== 'object' || Array.isArray(payload)) {
    return [{ key: '__raw__', label: '内容', text: typeof payload === 'string' ? payload : JSON.stringify(payload) }]
  }
  const record = payload as Record<string, unknown>
  const entries: PayloadEntry[] = []
  const push = (key: string) => {
    if (!(key in record)) return
    const value = record[key]
    if (value === null || value === undefined) return
    const text = key === 'source' && value === 'external_import' ? '外部数据集导入' : typeof value === 'string' ? value : JSON.stringify(value, null, 2)
    if (text.trim() === '') return
    entries.push({ key, label: PAYLOAD_FIELD_LABELS[key] ?? key, text })
  }
  for (const key of PAYLOAD_FIELD_ORDER) push(key)
  for (const key of Object.keys(record).sort()) {
    if (PAYLOAD_FIELD_ORDER.includes(key) || key === 'schemaVersion') continue
    push(key)
  }
  // schemaVersion 不展示：它是存储表示，不是用户要判断的内容。
  if (entries.length === 0) {
    entries.push({ key: '__raw__', label: '内容', text: JSON.stringify(record, null, 2) })
  }
  return entries
}

/** 字符串长度摘要（中文字符数足够回答「这条数据有多长」）。 */
function textLengthLabel(text: string): string {
  return `${Array.from(text).length} 字`
}

function PayloadPreview({ payload }: { payload: unknown }) {
  const { Text } = Typography
  const entries = useMemo(() => payloadEntries(payload), [payload])
  /**
   * 默认是**人话视图**；原始 JSON 是次要视图（可切换）。
   * 默认值的选择很重要：反过来就等于「默认把存储表示给用户看」，
   * 而那正是这条缺陷的定义。
   */
  const [raw, setRaw] = useState(false)
  return (
    <div data-payload-preview="true">
      <div className="flex flex-wrap items-center gap-2 mb-2">
        {entries.map((entry) => (
          <Tag key={entry.key} size="small" color="blue">
            {entry.label} {textLengthLabel(entry.text)}
          </Tag>
        ))}
        <Button size="small" theme="borderless" onClick={() => setRaw((value) => !value)} data-payload-raw-toggle="true">
          {raw ? '看分字段视图' : '看原始 JSON'}
        </Button>
      </div>
      {raw ? (
        <pre className="review-content" data-content-readonly="true">
          {JSON.stringify(payload, null, 2)}
        </pre>
      ) : (
        <div className="payload-preview" data-payload-fields="true">
          {entries.map((entry) => (
            <section key={entry.key} className="payload-preview__field">
              <Text strong size="small" className="block mb-1">
                {entry.label}
              </Text>
              {/* `white-space: pre-wrap` 保留换行但不保留 JSON 转义：
                  用户看到的是真正的多行推理，而不是一串 \n。 */}
              <div className="payload-preview__body">{entry.text}</div>
            </section>
          ))}
        </div>
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
  const [capabilities, setCapabilities] = useState<SampleCapabilities>({
    canReview: false,
    canViewHistory: false,
  })
  const [decisions, setDecisions] = useState<ReviewDecision[]>([])
  const [projection, setProjection] = useState<ReviewProjection | null>(null)
  const [blockers, setBlockers] = useState<ApiBlocker[]>([])
  const [queueItems, setQueueItems] = useState<SampleSummary[]>([])
  const [queueCursor, setQueueCursor] = useState('')
  const [queueLoading, setQueueLoading] = useState(false)
  const [queueError, setQueueError] = useState<string | null>(null)
  const [focusContent, setFocusContent] = useState(true)
  const [autoNext, setAutoNext] = useState(true)
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)
  const [action, setAction] = useState<'accepted' | 'quarantined'>('accepted')
  const [reason, setReason] = useState('')
  const [submitting, setSubmitting] = useState(false)
  const [submitError, setSubmitError] = useState<string | null>(null)
  const [canSaveOfflineDraft, setCanSaveOfflineDraft] = useState(false)
  const [savedNotice, setSavedNotice] = useState<string | null>(null)
  // 离线待同步（T29）：提交失败时**不显示成功**，而是提供「保存为本地草稿」。
  const [offlineNotice, setOfflineNotice] = useState<string | null>(null)
  const [pending, setPending] = useState(0)
  const [copyFallback, setCopyFallback] = useState<string | null>(null)
  const [reviewProjectCapabilities, setReviewProjectCapabilities] = useState<ProjectCapabilities | null>(null)
  const [reviewPermissionError, setReviewPermissionError] = useState<string | null>(null)
  const [reviewPermissionRefresh, setReviewPermissionRefresh] = useState(0)

  useEffect(() => {
    let cancelled = false
    setReviewProjectCapabilities(null)
    setReviewPermissionError(null)
    void studioApi.overviewEnvelope(scope.projectId).then((overview) => {
      if (!cancelled) setReviewProjectCapabilities(overview.capabilities)
    }).catch((permissionError: unknown) => {
      if (!cancelled) setReviewPermissionError(permissionError instanceof Error ? permissionError.message : '权限读取失败')
    })
    return () => { cancelled = true }
  }, [reviewPermissionRefresh, scope.projectId])

  // 竞态防护：切样本时丢弃过期响应（见文件头说明）。
  const latestRequest = useRef(0)
  const contentRef = useRef<HTMLDivElement | null>(null)
  const submittingRef = useRef(false)
  const draftDirty = reason.trim() !== '' || action !== 'accepted'

  useEffect(() => {
    if (!draftDirty) return
    const onBeforeUnload = (event: BeforeUnloadEvent) => {
      event.preventDefault()
      event.returnValue = ''
    }
    window.addEventListener('beforeunload', onBeforeUnload)
    return () => window.removeEventListener('beforeunload', onBeforeUnload)
  }, [draftDirty])

  const allowNavigation = useCallback(() => {
    if (submittingRef.current) return false
    return !draftDirty || window.confirm('判断尚未保存。放弃当前理由并切换样本？')
  }, [draftDirty])

  useEffect(() => {
    const guardLinkNavigation = (event: MouseEvent) => {
      const link = (event.target as Element | null)?.closest<HTMLAnchorElement>('a[href]')
      if (!link || event.defaultPrevented || link.target === '_blank') return
      const target = new URL(link.href, window.location.href)
      if (target.pathname === window.location.pathname && target.search === window.location.search) return
      if (!allowNavigation()) { event.preventDefault(); event.stopPropagation() }
    }
    document.addEventListener('click', guardLinkNavigation, true)
    return () => document.removeEventListener('click', guardLinkNavigation, true)
  }, [allowNavigation])

  useEffect(() => {
    // BrowserRouter 已为每个站内历史项写 idx；capture 必须先于它的 bubble 监听。
    const entryState = window.history.state as { idx?: number } | null
    const entryURL = window.location.href
    const guardHistoryNavigation = (event: PopStateEvent) => {
      if (allowNavigation()) return
      event.stopImmediatePropagation()
      event.preventDefault()
      // popstate 已经发生，preventDefault 本身不能撤销地址变化；恢复当前
      // entry 并停止 Router 的监听，避免脏理由随路由卸载。
      window.history.pushState(entryState, '', entryURL)
    }
    window.addEventListener('popstate', guardHistoryNavigation, true)
    return () => window.removeEventListener('popstate', guardHistoryNavigation, true)
  }, [allowNavigation, sampleID])

  // 队列筛选沿用数据页 URL。默认只取待判断，显式 status=all 才查看全部。
  const rawQueueStatus = searchParams.get('status')
  const queueStatus: ReviewStatusFilter =
    rawQueueStatus === 'accepted' || rawQueueStatus === 'quarantined' || rawQueueStatus === 'conflict' || rawQueueStatus === 'all'
      ? rawQueueStatus === 'all' ? '' : rawQueueStatus
      : 'pending'
  const queueSearch = searchParams.get('q')?.trim() ?? ''
  const queueRequest = useRef(0)

  useEffect(() => () => { queueRequest.current += 1; latestRequest.current += 1 }, [sampleID])

  const loadQueue = useCallback(async (cursor = '', append = false) => {
    const requestID = queueRequest.current + 1
    queueRequest.current = requestID
    setQueueLoading(true)
    setQueueError(null)
    try {
      const page = await studioApi.listSamples(scope.projectId, {
        limit: 50,
        status: queueStatus === '' ? 'all' : queueStatus,
        q: queueSearch || undefined,
        cursor: cursor || undefined,
      })
      if (requestID !== queueRequest.current) return
      setQueueItems((previous) => {
        const next = append ? [...previous, ...(page.items ?? [])] : (page.items ?? [])
        const seen = new Set<string>()
        return next.filter((item) => {
          if (seen.has(item.resourceId)) return false
          seen.add(item.resourceId)
          return true
        })
      })
      setQueueCursor(page.nextCursor ?? '')
    } catch (queueLoadError) {
      if (requestID !== queueRequest.current) return
      setQueueError(queueLoadError instanceof Error ? queueLoadError.message : '加载审阅队列失败')
    } finally {
      if (requestID === queueRequest.current) setQueueLoading(false)
    }
  }, [queueSearch, queueStatus, scope.projectId])

  useEffect(() => {
    void loadQueue()
  }, [loadQueue, sampleID])

  const refreshPending = useCallback(() => {
    const actorId = currentActorID()
    const numericProjectId = projectNumericId(scope.projectId) ?? 0
    setPending(actorId > 0 && numericProjectId > 0 ? pendingCount(actorId, numericProjectId) : 0)
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
      const response = await studioApi.getSampleEnvelope(scope.projectId, sampleID)
      if (requestID !== latestRequest.current) return
      setDetail(response.data)
      setCapabilities(response.capabilities)

      const version = response.data?.version
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

  useEffect(() => {
    // 切换样本时，判断理由和复制回退内容都属于上一条，不能带到新样本。
    setDetail(null)
    setDecisions([])
    setProjection(null)
    setBlockers([])
    setLoading(true)
    setAction('accepted')
    setReason('')
    setSubmitError(null)
    setCanSaveOfflineDraft(false)
    setSavedNotice(null)
    setOfflineNotice(null)
    setCopyFallback(null)
  }, [sampleID])

  const queueWithCurrent = useMemo(() => {
    if (!detail) return queueItems
    if (queueItems.some((item) => item.resourceId === detail.sample.resourceId)) return queueItems
    // 从其它页面（例如“全部”或质量报告）直达样本时，仍把当前对象保留在队列首位，
    // 但不伪造它属于当前筛选；后续刷新会由服务端结果替换。
    return [detail.sample, ...queueItems]
  }, [detail, queueItems])

  const navigateToQueueItem = useCallback((resourceId: string) => {
    if (resourceId === sampleID || !allowNavigation()) return
    const query = searchParams.toString()
    navigate(`${projectHref('project.sample', scope.projectId, { sampleId: resourceId })}${query ? `?${query}` : ''}`)
  }, [allowNavigation, navigate, sampleID, scope.projectId, searchParams])

  /** 下一条 / 上一条：沿用当前筛选条件，并用服务端游标走完队列。 */
  const goRelative = useCallback(
    async (direction: 'next' | 'prev', afterSave = false, preferredNext?: string) => {
      if (!afterSave && !allowNavigation()) return
      // 取消此前的「加载更多」响应，避免它在相对导航完成后覆盖完整队列。
      queueRequest.current += 1
      const requestID = queueRequest.current
      setQueueLoading(true)
      setQueueError(null)
      const items: SampleSummary[] = []
      let cursor = ''
      let remainingCursor = ''
      const relativeTarget = (candidates: SampleSummary[]) => {
        const index = candidates.findIndex((item) => item.resourceId === sampleID)
        const followingCurrent = index < 0 && afterSave && detail?.sample.createdAt
          ? candidates.find((item) => item.createdAt < detail.sample.createdAt || (item.createdAt === detail.sample.createdAt && item.sampleId < detail.sample.sampleId))
          : candidates[index + 1]
        return direction === 'next'
          ? (preferredNext ? candidates.find((item) => item.resourceId === preferredNext) : undefined) ?? followingCurrent
          : candidates[index - 1]
      }
      try {
        // 相对导航不能只看首屏：游标分页后的样本也必须可以到达。
        for (let pageIndex = 0; pageIndex < 100; pageIndex += 1) {
          const page = await studioApi.listSamples(scope.projectId, {
            limit: 50,
            status: queueStatus === '' ? 'all' : queueStatus,
            q: queueSearch || undefined,
            cursor: cursor || undefined,
          })
          if (requestID !== queueRequest.current) return
          items.push(...(page.items ?? []))
          remainingCursor = page.nextCursor ?? ''
          const currentIndex = items.findIndex((item) => item.resourceId === sampleID)
          // 只查到目标所需的页面；逐条审阅不能每次遍历整个大项目。
          if ((afterSave || currentIndex >= 0) && (relativeTarget(items) || (direction === 'prev' && currentIndex === 0))) break
          if (!page.nextCursor) break
          cursor = page.nextCursor
        }
      } catch (relativeError) {
        if (requestID !== queueRequest.current) return
        setQueueError(relativeError instanceof Error ? relativeError.message : '加载审阅队列失败')
        setQueueLoading(false)
        return
      }
      const deduped = items.filter((item, index, all) => all.findIndex((candidate) => candidate.resourceId === item.resourceId) === index)
      setQueueItems(deduped)
      setQueueCursor(remainingCursor)
      setQueueLoading(false)
      const target = relativeTarget(deduped) ?? (afterSave ? deduped.find((item) => item.resourceId !== sampleID) : undefined)
      if (!target) {
        // 「最后一条」必须可解释：明确告知，而不是静默什么都不做。
        setSavedNotice(afterSave ? '判断已保存，当前队列已完成。' : direction === 'next' ? '已经是当前筛选下的最后一条' : '已经是第一条')
        return
      }
      setSavedNotice(null)
      setReason('')
      navigate(
        `${projectHref('project.sample', scope.projectId, { sampleId: target.resourceId })}` +
          (searchParams.toString() ? `?${searchParams.toString()}` : ''),
      )
    },
    [allowNavigation, detail, navigate, queueSearch, queueStatus, sampleID, scope.projectId, searchParams],
  )

  /**
   * 键盘流：`J` 下一条 / `K` 上一条（issue #207）。
   *
   * 为什么必须实现而不是删掉帮助页那句话：审阅是**批量**动作，键盘流是吞吐量的
   * 关键，而帮助页的「快捷键」区已经把它当作既有能力承诺给用户。承诺了不做，
   * 用户按 J 没反应时只会认为自己操作错了 —— 那是比「没这个功能」更差的体验。
   *
   * 三条约束（每条都对应一个真实的误触发场景）：
   *  1. **焦点在输入控件里时不抢键**：审阅页有一整块判断理由输入框，
   *     在那里敲 `j` 必须输入字母 `j`，而不是跳走并丢掉写了一半的理由；
   *  2. **有修饰键时不抢**：⌘K/⌘J 属浏览器与系统（而 ⌘K 是本页命令搜索），
   *     `Shift+J` 也不是本快捷键的意图；
   *  3. **不与其他快捷键重复**：全仓只有 StudioLayout 的 Esc（关闭导航）与
   *     命令搜索的 Esc/回车，`J`/`K` 没有第二个使用者。
   *
   * 复用 `goRelative`（「上一条/下一条」按钮的同一函数）：两者共享服务端游标遍历、
   * 筛选条件保留与「已经是最后一条」提示，因此不可能出现「按钮能用、键盘不能用」。
   */
  const goRelativeRef = useRef(goRelative)
  useEffect(() => {
    goRelativeRef.current = goRelative
  }, [goRelative])
  useEffect(() => {
    const onKeyDown = (event: KeyboardEvent) => {
      if (event.key !== 'j' && event.key !== 'k' && event.key !== 'J' && event.key !== 'K') return
      if (event.altKey || event.ctrlKey || event.metaKey || event.shiftKey) return
      const target = event.target as HTMLElement | null
      if (target) {
        const tag = target.tagName
        if (tag === 'INPUT' || tag === 'TEXTAREA' || tag === 'SELECT' || target.isContentEditable) return
      }
      event.preventDefault()
      void goRelativeRef.current(event.key === 'j' || event.key === 'J' ? 'next' : 'prev')
    }
    document.addEventListener('keydown', onKeyDown)
    return () => document.removeEventListener('keydown', onKeyDown)
  }, [])

  const submit = useCallback(async () => {
    if (!detail || submittingRef.current || queueLoading) return
    setCanSaveOfflineDraft(false)
    if (!capabilities.canReview) {
      setSubmitError('当前账号没有审阅权限；内容保持只读')
      return
    }
    if (reason.trim() === '') {
      setSubmitError('理由必填：没有理由的判断无法被复核')
      document.getElementById('review-reason')?.focus()
      return
    }
    submittingRef.current = true
    setSubmitting(true)
    setSubmitError(null)
    const nextBeforeSave = queueWithCurrent[queueWithCurrent.findIndex((item) => item.resourceId === sampleID) + 1]?.resourceId
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
      await loadQueue()
      setAction('accepted')
      // 只有服务端确认成功且没有并发冲突才推进；离线 / 409 保留当前理由与位置。
      if (autoNext && !result.projection.conflict && result.projection.effectiveAction !== 'conflict') {
        await goRelative('next', true, nextBeforeSave)
      } else if (result.projection.conflict || result.projection.effectiveAction === 'conflict') {
        setSavedNotice('判断已保存，但存在相反判断。请先处理冲突。')
      }
    } catch (submitErrorValue) {
      // 409 时必须保留用户输入 —— 清空理由会让用户重打一遍，
      // 而那正是「过期返回 409 并保留输入」要避免的。
      setCanSaveOfflineDraft((submitErrorValue as { statusCode?: number }).statusCode === undefined)
      setSubmitError(submitErrorValue instanceof Error ? submitErrorValue.message : '提交失败')
    } finally {
      submittingRef.current = false
      setSubmitting(false)
    }
  }, [action, autoNext, capabilities.canReview, detail, goRelative, load, loadQueue, projection, queueLoading, queueWithCurrent, reason, sampleID, scope.projectId])

  const coordinateConflict = useCallback(async () => {
    if (!detail || submittingRef.current || queueLoading) return
    if (!reviewProjectCapabilities?.canPublish || !capabilities.canReview) {
      setSubmitError('只有项目负责人可以协调冲突')
      return
    }
    if (!reason.trim()) { setSubmitError('请填写协调理由'); return }
    const coordinatedDecisionID = decisions[decisions.length - 1]?.id
    if (!coordinatedDecisionID) { setSubmitError('无法读取冲突判断记录，请刷新后重试'); return }
    submittingRef.current = true
    setSubmitting(true)
    setSubmitError(null)
    try {
      const result = await studioApi.resolveConflict(scope.projectId, sampleID, detail.version.version, { action, reason: reason.trim(), supersedes: coordinatedDecisionID })
      setBlockers(result.blockers ?? [])
      setReason('')
      setAction('accepted')
      setSavedNotice(result.projection.conflict ? '协调决定已记录，冲突仍未解除，请核对判断历史。' : '冲突已协调')
      await load()
      await loadQueue()
      // 协调结果先留在当前内容，便于负责人复核；不把冲突当普通判断重放。
    } catch (coordinateError) {
      setSubmitError(coordinateError instanceof Error ? coordinateError.message : '协调失败，请重试')
    } finally {
      submittingRef.current = false
      setSubmitting(false)
    }
  }, [action, capabilities.canReview, decisions, detail, load, loadQueue, queueLoading, reason, reviewProjectCapabilities?.canPublish, sampleID, scope.projectId])

  /**
   * 保存为本地草稿（T29）。
   *
   * 只在**提交失败**时提供：离线写入不显示成功，这里返回的语义是
   * 「待同步（未提交）」。判断类意图可以入队（非收费、可安全重放），
   * 而启动运行/发布等操作由 pendingQueue 的允许清单直接拒绝。
   */
  const saveOfflineDraft = useCallback(() => {
    if (!capabilities.canReview) {
      setSubmitError('当前账号没有审阅权限；不能保存判断草稿')
      return
    }
    const actorId = currentActorID()
    const numericProjectId = projectNumericId(scope.projectId) ?? 0
    if (numericProjectId <= 0) {
      setSubmitError('项目地址无效；不能保存判断草稿')
      return
    }
    const result = enqueue({
      kind: 'review_decision_draft',
      actorId,
      workspaceId: numericProjectId,
      objectRef: `sample_version:${detail?.version.versionId ?? 0}`,
      revision: projection?.evidenceRevision ?? 0,
      payload: { body: reason.trim(), action },
    })
    if (result.status === 'pending') {
      setOfflineNotice('已保存为本机草稿（待同步，未提交）：联网后需先登录并确认，系统不会后台自动提交')
      refreshPending()
    } else {
      setSubmitError(result.reason)
    }
  }, [action, capabilities.canReview, detail, projection, reason, refreshPending, scope.projectId])

  const copyContent = useCallback(async () => {
    if (!detail) return
    const text = JSON.stringify(detail.version.payload, null, 2)
    try {
      if (navigator.clipboard?.writeText) {
        await navigator.clipboard.writeText(text)
        setCopyFallback(null)
        setSavedNotice('内容已复制')
        return
      }
      throw new Error('clipboard unavailable')
    } catch {
      // 复制失败必须有回退路径：只弹一句「复制失败」而不给替代方案，
      // 用户就只能在长文本里手工选中。
      setCopyFallback(text)
      setSavedNotice('浏览器不允许自动复制：请在只读文本框中手动复制')
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
  const canCoordinate = reviewProjectCapabilities?.canPublish === true && capabilities.canReview
  const canDecide = capabilities.canReview && (effective !== 'conflict' || canCoordinate)

  return (
    <div className="console-page review-pane product-review" data-studio-page="sample-review" data-effective-action={effective}>
      <div className="console-page__header">
        <div>
          <Title heading={4} className="!mb-1">{detail.sample.title || detail.sample.sampleKey}</Title>
          <Text type="tertiary">{detail.sample.resourceId} · v{detail.version.version}</Text>
        </div>
        <div className="flex flex-wrap items-center gap-2">
          <Tag color={reviewStatusColor(effective)} data-effective-tag="true">{describeReviewStatus(effective)}</Tag>
          <Button size="small" disabled={submitting || queueLoading} icon={<ChevronLeft size={14} />} onClick={() => void goRelative('prev')}>上一条</Button>
          <Button size="small" disabled={submitting || queueLoading} icon={<ChevronRight size={14} />} onClick={() => void goRelative('next')}>下一条</Button>
          <Button size="small" icon={focusContent ? <Expand size={14} /> : <Minimize2 size={14} />}
            aria-expanded={!focusContent} aria-controls="review-support"
            onClick={() => setFocusContent((current) => !current)}>{focusContent ? '队列与证据' : '收起辅助信息'}</Button>
        </div>
      </div>
      {savedNotice ? <div className="product-notice mb-3" role="status" data-review-notice="true">{savedNotice}</div> : null}
      {!capabilities.canReview ? (
        <div className="product-notice mb-3" data-capability-readonly="review">只读：当前账号没有审阅权限。</div>
      ) : null}

      <div className="review-pane__columns product-review__workspace" data-review-focus={focusContent ? 'content' : 'all'}>
        <section className="review-pane__column review-pane__content" aria-label="只读内容"
          ref={contentRef} tabIndex={-1} data-content-focus="true">
          <div className="flex items-center justify-between mb-2">
            <Text strong>{detail.sample.targetKind.toLowerCase().includes('grpo') ? '教师提示词与奖励判据' : '样本内容'}</Text>
            <Button size="small" icon={<Copy size={13} />} onClick={() => void copyContent()}>复制</Button>
          </div>
          <PayloadPreview payload={detail.version.payload} />
          {copyFallback !== null ? (
            <div className="mt-3" data-copy-fallback="true">
              <div className="flex items-center justify-between mb-1">
                <Text type="tertiary" size="small">手动复制（只读）</Text>
                <Button size="small" theme="borderless" onClick={() => setCopyFallback(null)}>关闭</Button>
              </div>
              <TextArea value={copyFallback} readOnly autosize={{ minRows: 6, maxRows: 16 }} aria-label="手动复制内容" />
            </div>
          ) : null}
        </section>
        <section className="review-pane__column product-review__decision" aria-label="审阅判断" data-review-decision-panel="true">
          <Text strong className="block mb-3">审阅判断</Text>
          {effective === 'conflict' ? (
            <div className="product-notice product-notice--warning mb-3" data-conflict-notice="true">
              {canCoordinate ? '存在相反判断，请确认最终处置并填写协调理由。' : '存在相反判断，请联系项目负责人协调后再发布。'}
            </div>
          ) : null}
          {effective === 'conflict' && reviewPermissionError ? <div role="alert" className="wizard-field__error mb-3">
            协调权限读取失败：{reviewPermissionError}<Button size="small" onClick={() => setReviewPermissionRefresh((previous) => previous + 1)}>重试权限读取</Button>
          </div> : null}
          {canDecide ? <>
            <div className="product-choice-group" role="group" aria-label="选择处置">
              <Button theme={action === 'accepted' ? 'solid' : 'outline'} type="primary" disabled={submitting}
                aria-pressed={action === 'accepted'} onClick={() => setAction('accepted')} data-review-action="accepted">{effective === 'conflict' ? '协调为接纳' : '接纳'}</Button>
              <Button theme={action === 'quarantined' ? 'solid' : 'outline'} type="danger" disabled={submitting}
                aria-pressed={action === 'quarantined'} onClick={() => setAction('quarantined')} data-review-action="quarantined">{effective === 'conflict' ? '协调为隔离' : '隔离'}</Button>
            </div>
            <label className="wizard-field__label mt-3" htmlFor="review-reason">{effective === 'conflict' ? '协调理由' : '判断理由'}</label>
            <TextArea id="review-reason" value={reason} disabled={submitting} onChange={(value) => { setReason(value); setSubmitError(null); setCanSaveOfflineDraft(false) }}
              autosize={{ minRows: 3, maxRows: 8 }} placeholder={action === 'accepted' ? '接纳依据（必填）' : '需要隔离的问题（必填）'}
              data-field="review-reason" aria-invalid={submitError ? true : undefined}
              aria-describedby={submitError ? 'review-submit-error' : undefined} />
            {submitError ? <div id="review-submit-error" className="wizard-field__error mt-2" role="alert" data-review-submit-error="true">{submitError}</div> : null}
            {submitError && canSaveOfflineDraft && effective !== 'conflict' ? <Button size="small" theme="borderless" onClick={saveOfflineDraft} data-review-offline-draft="true">保存为本地草稿（待同步）</Button> : null}
            {offlineNotice ? <Text type="warning" size="small" className="block mt-2" data-review-offline-notice="true">{offlineNotice}</Text> : null}
            {pending > 0 ? <Text type="tertiary" size="small" className="block mt-2" data-review-pending-count="true">待同步（未提交）：{pending} 条</Text> : null}
            <div className="product-review__save">
              {effective !== 'conflict' ? <Checkbox checked={autoNext} disabled={submitting} onChange={(event) => setAutoNext(Boolean(event.target.checked))}>保存后下一条</Checkbox> : null}
              <Button theme="solid" type="primary" icon={<Save size={14} />} loading={submitting}
                disabled={queueLoading} onClick={() => void (effective === 'conflict' ? coordinateConflict() : submit())}
                data-review-save="true" data-review-coordinate={effective === 'conflict' ? 'true' : undefined}>{effective === 'conflict' ? '保存协调决定' : autoNext ? '保存并下一条' : '保存判断'}</Button>
            </div>
            <Text type="tertiary" size="small" className="block mt-2">J 下一条 · K 上一条</Text>
          </> : <Text type="tertiary">{effective === 'conflict' ? '请联系项目负责人处理冲突。' : '可查看内容与证据，不能提交判断。'}</Text>}
          {blockers.length > 0 ? (
            <details className="product-disclosure mt-3" data-review-blockers="true">
              <summary>发布门槛（{blockers.length}）</summary>
              {blockers.map((blocker) => (
                <div key={blocker.code} className="flex items-start gap-2 mt-2">
                  <AlertTriangle size={14} aria-hidden /><Text size="small">{blocker.message}</Text>
                </div>
              ))}
            </details>
          ) : null}
          {detail.version.versionId && projectNumericId(scope.projectId) ? (
            <details className="product-disclosure mt-3">
              <summary>评论</summary>
              <CommentPanel projectId={projectNumericId(scope.projectId) ?? 0} anchorKind="sample_version" anchorId={detail.version.versionId} />
            </details>
          ) : null}
        </section>
      </div>

      {!focusContent ? <div id="review-support" className="product-review__support">
        <section className="review-pane__column" aria-label="待判断内容">
          <div className="flex items-center justify-between mb-2"><Text strong>审阅队列</Text><Tag size="small">{queueItems.length}{queueCursor ? '+' : ''} 条</Tag></div>
          {queueError ? <div className="wizard-field__error mb-2" role="alert" data-review-queue-error="true">{queueError}<Button size="small" theme="borderless" onClick={() => void loadQueue()}>重试</Button></div> : null}
          {queueLoading && queueItems.length === 0 ? <Spin size="small" /> : null}
          <ul className="review-queue" data-review-queue="true">
            {queueWithCurrent.length === 0 && !queueLoading ? <li>暂无待判断内容</li> : queueWithCurrent.map((item) => (
              <li key={item.resourceId} className={item.resourceId === sampleID ? 'review-queue__item--active' : undefined}>
                <button type="button" className="review-queue__button" disabled={submitting}
                  aria-current={item.resourceId === sampleID ? 'page' : undefined}
                  aria-label={`打开 ${item.title || item.sampleKey}`} onClick={() => navigateToQueueItem(item.resourceId)}>
                  <Text strong size="small">{item.title || item.sampleKey}</Text>
                  <Tag size="small" color={reviewStatusColor(item.reviewStatus)}>{describeReviewStatus(item.reviewStatus)}</Tag>
                </button>
              </li>
            ))}
          </ul>
          {queueCursor ? <Button size="small" loading={queueLoading} onClick={() => void loadQueue(queueCursor, true)}>加载更多</Button> : null}
        </section>
        <section className="review-pane__column" aria-label="证据与历史">
          <Text strong className="block mb-2">证据与历史</Text>
          <div className="flex flex-wrap gap-2 mb-2" data-review-evidence-links="true">
            <Button size="small" onClick={() => { if (allowNavigation()) navigate(`${projectHref('project.rules', scope.projectId)}?sampleVersionId=${detail.version.versionId}`) }}>查看策略</Button>
            <Button size="small" onClick={() => { if (allowNavigation()) navigate(`${projectHref('project.quality', scope.projectId)}?sampleVersionId=${detail.version.versionId}`) }}>完整评估</Button>
            <Button size="small" onClick={() => { if (allowNavigation()) navigate(projectHref('project.sampleHistory', scope.projectId, { sampleId: sampleID })) }}>版本与来源</Button>
          </div>
          <details className="product-disclosure">
            <summary>来源与证据版本</summary>
            <ul className="review-evidence">
              <li>证据版本：{projection?.evidenceRevision ?? 0} · 聚合序号 {projection?.aggregateReviewRevision ?? 0}</li>
              <li>内容指纹：<code>{detail.version.contentHash}</code></li>
              <li>蓝图指纹：<code>{detail.version.source.blueprintContentHash || '未记录'}</code></li>
              <li>素材块：{detail.version.source.sourceChunkIds?.join('、') || '未关联素材'}
                <Button size="small" theme="borderless" onClick={() => { if (allowNavigation()) navigate(projectHref('project.sources', scope.projectId)) }}>查看素材来源</Button>
              </li>
            </ul>
          </details>
          <details className="product-disclosure mt-2" data-review-decision-history="true">
            <summary>判断历史（{decisions.length}）</summary>
            {decisions.length === 0 ? <Text type="tertiary" size="small">暂无判断</Text> : (
              <ul className="review-evidence">{decisions.map((decision) => (
                <li key={decision.id}>
                  <Tag size="small" color={reviewStatusColor(decision.action)}>{decision.action === 'accepted' ? '接纳' : '隔离'}</Tag>{' '}
                  <Text size="small">{decision.reason}</Text>
                  <Text type="tertiary" size="small" className="block">审阅者 {decision.reviewerId} · 第 {decision.reviewerRevision} 次
                    {decision.supersedes ? ` · 更正 #${decision.supersedes}` : ''}{decision.resolutionOf ? ` · 协调 #${decision.resolutionOf}` : ''}</Text>
                </li>
              ))}</ul>
            )}
          </details>
        </section>
      </div> : null}
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
            内容只追加，永不覆盖；每一版都记录它生成时引用的标准与蓝图指纹。
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
                    内容指纹：<code>{version.contentHash}</code>
                  </li>
                  <li>
                    来源批次：<code>{version.batchId ?? '（无）'}</code> · 单元{' '}
                    <code>{version.batchItemId ?? '（无）'}</code> · 第 {version.attempt} 次尝试
                  </li>
                  <li>
                    标准指纹：<code>{version.source.standardContentHash || '（未引用）'}</code>
                  </li>
                  <li>
                    蓝图指纹：<code>{version.source.blueprintContentHash || '（未引用）'}</code>
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
