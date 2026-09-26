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
  const showAllStatuses = searchParams.get('status') === 'all'
  const search = searchParams.get('q') ?? ''

  const [samples, setSamples] = useState<SampleSummary[]>([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)
  const [nextCursor, setNextCursor] = useState('')
  // 选择只针对**当前页**（见文件头说明）。
  const [selected, setSelected] = useState<Set<number>>(new Set())
  const [snapshotNotice, setSnapshotNotice] = useState<string | null>(null)
  const [snapshotID, setSnapshotID] = useState<number | null>(null)
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
        else if (reviewStatus !== '') params.set('status', reviewStatus)
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
        navigate(`${projectHref('project.newRelease', scope.projectId)}?selection=${encodeURIComponent(String(snapshot.id))}`)
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
  const description = queueMode
    ? '这里只列需要你判断的样本（默认「待判断」，按等待时长排序）。点「审阅」进入三栏判断界面。'
    : '浏览这一版里的全部样本内容与版本来源（默认包含已接纳与已隔离）。需要判断时切到「审阅队列」。'

  return (
    <div className="console-page" data-studio-page={queueMode ? 'review-queue' : 'sample-list'}>
      <div className="console-page__header">
        <div>
          <Title heading={4} className="!mb-1">
            {title}
          </Title>
          <Text type="tertiary">{description}</Text>
          <Text type="tertiary" size="small" className="block mt-1">
            按审阅状态与关键词在<strong>服务端</strong>筛选与分页；按钮上的数量是服务端统计，不是当前页条目数。
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
            刷新样本
          </Button>
        </div>
      </div>

      {selected.size > 0 ? (
        <Card className="console-card mb-3" bodyStyle={{ padding: 12 }} data-selection-summary="true">
          <Text size="small">
            已选 {selected.size} 条（当前页）。跨页选择请用下方「按筛选条件冻结范围」——
            它由服务端解析，因此不会出现「以为选了 40 条、实际提交 12 条」。
          </Text>
          <div className="mt-2 flex gap-2">
            {projectCapabilities?.canPublish ? (
              <Button size="small" theme="solid" type="primary" onClick={() => void snapshotSelected()} data-selection-release="true">
                导出所选并准备发布
              </Button>
            ) : null}
            {projectCapabilities?.canPublish ? (
              <Button size="small" onClick={() => void snapshotAll()} data-snapshot-all="true">
                按筛选条件全选并准备发布
              </Button>
            ) : null}
            <Button size="small" onClick={() => setSelected(new Set())}>
              清空当前页选择
            </Button>
          </div>
        </Card>
      ) : queueMode ? (
        /* 审阅队列的主操作是「判断」，不是「导出」：默认整体冻结会让用户在
           还没看内容的情况下就进入发布流程（issue #194 的 CTA 完全相同的成因）。 */
        <div className="mb-3">
          <Text type="tertiary" size="small">
            待判断 {samples.length} 条{samples.length > 0 ? '，点每行的「审阅」逐条判断' : ''}。判断完成后可用下方「按筛选条件冻结」把同一范围交给发布流程。
          </Text>
        </div>
      ) : (
        <div className="mb-3">
          {projectCapabilities?.canPublish ? (
            <Button size="small" onClick={() => void snapshotAll()} data-snapshot-all="true">
              按当前筛选冻结并准备发布（服务端解析）
            </Button>
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
          <Empty
            description={
              queueMode
                ? '这个筛选下没有待判断的样本。已接纳的内容不会再出现在队列里；要回看它们请切到「数据」。'
                : '这个项目还没有任何样本版本。先在「生产」里跑一个批次产生内容。'
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
                  {/* 「数据」里的主操作是**预览内容**（#197 第 3 条：数据应当可以预览），
                      「审阅」里的主操作才是判断。两个入口的行操作因此不同名同形。 */}
                  <Button
                    size="small"
                    theme={queueMode && sample.capabilities?.canReview ? 'solid' : 'borderless'}
                    type={queueMode && sample.capabilities?.canReview ? 'primary' : 'tertiary'}
                    onClick={() =>
                      navigate(
                        `${projectHref('project.sample', scope.projectId, { sampleId: sample.resourceId })}` +
                          (searchParams.toString() ? `?${searchParams.toString()}` : ''),
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
    const text = typeof value === 'string' ? value : JSON.stringify(value, null, 2)
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
  const [focusContent, setFocusContent] = useState(false)
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
  const [copyFallback, setCopyFallback] = useState<string | null>(null)

  // 竞态防护：切样本时丢弃过期响应（见文件头说明）。
  const latestRequest = useRef(0)
  const contentRef = useRef<HTMLDivElement | null>(null)

  // 队列筛选沿用数据页 URL。默认只取待判断，显式 status=all 才查看全部。
  const rawQueueStatus = searchParams.get('status')
  const queueStatus: ReviewStatusFilter =
    rawQueueStatus === 'accepted' || rawQueueStatus === 'quarantined' || rawQueueStatus === 'conflict' || rawQueueStatus === 'all'
      ? rawQueueStatus === 'all' ? '' : rawQueueStatus
      : 'pending'
  const queueSearch = searchParams.get('q')?.trim() ?? ''
  const queueRequest = useRef(0)

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
  }, [loadQueue])

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
    const query = searchParams.toString()
    navigate(`${projectHref('project.sample', scope.projectId, { sampleId: resourceId })}${query ? `?${query}` : ''}`)
  }, [navigate, scope.projectId, searchParams])

  /** 下一条 / 上一条：沿用当前筛选条件，并用服务端游标走完队列。 */
  const goRelative = useCallback(
    async (direction: 'next' | 'prev') => {
      // 取消此前的「加载更多」响应，避免它在相对导航完成后覆盖完整队列。
      queueRequest.current += 1
      setQueueLoading(true)
      setQueueError(null)
      const items: SampleSummary[] = []
      let cursor = ''
      try {
        // 相对导航不能只看首屏：游标分页后的样本也必须可以到达。
        for (let pageIndex = 0; pageIndex < 100; pageIndex += 1) {
          const page = await studioApi.listSamples(scope.projectId, {
            limit: 50,
            status: queueStatus === '' ? 'all' : queueStatus,
            q: queueSearch || undefined,
            cursor: cursor || undefined,
          })
          items.push(...(page.items ?? []))
          if (!page.nextCursor) break
          cursor = page.nextCursor
        }
      } catch (relativeError) {
        setQueueError(relativeError instanceof Error ? relativeError.message : '加载审阅队列失败')
        setQueueLoading(false)
        return
      }
      const deduped = items.filter((item, index, all) => all.findIndex((candidate) => candidate.resourceId === item.resourceId) === index)
      setQueueItems(deduped)
      setQueueCursor('')
      setQueueLoading(false)
      const index = deduped.findIndex((item) => item.resourceId === sampleID)
      const target = direction === 'next' ? deduped[index + 1] : deduped[index - 1]
      if (!target) {
        // 「最后一条」必须可解释：明确告知，而不是静默什么都不做。
        setSavedNotice(direction === 'next' ? '已经是当前筛选下的最后一条' : '已经是第一条')
        return
      }
      setSavedNotice(null)
      setReason('')
      navigate(
        `${projectHref('project.sample', scope.projectId, { sampleId: target.resourceId })}` +
          (searchParams.toString() ? `?${searchParams.toString()}` : ''),
      )
    },
    [navigate, queueSearch, queueStatus, sampleID, scope.projectId, searchParams],
  )

  const submit = useCallback(async () => {
    if (!detail) return
    if (!capabilities.canReview) {
      setSubmitError('当前账号没有审阅权限；内容保持只读')
      return
    }
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
      await loadQueue()
    } catch (submitErrorValue) {
      // 409 时必须保留用户输入 —— 清空理由会让用户重打一遍，
      // 而那正是「过期返回 409 并保留输入」要避免的。
      setSubmitError(submitErrorValue instanceof Error ? submitErrorValue.message : '提交失败')
    } finally {
      setSubmitting(false)
    }
  }, [action, capabilities.canReview, detail, load, loadQueue, projection, reason, sampleID, scope.projectId])

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
          <Button size="small" onClick={() => navigate(projectHref('project.sampleHistory', scope.projectId, { sampleId: sampleID }))}>
            版本与来源
          </Button>
          <Button
            size="small"
            icon={focusContent ? <Minimize2 size={14} /> : <Expand size={14} />}
            title={focusContent ? '显示队列与证据' : '专注内容'}
            aria-label={focusContent ? '显示队列与证据' : '专注内容'}
            onClick={() => setFocusContent((current) => !current)}
          >
            {focusContent ? '显示队列与证据' : '专注内容'}
          </Button>
        </div>
      </div>

      {savedNotice ? (
        <Card className="console-card mb-3" bodyStyle={{ padding: 10 }} data-review-notice="true">
          <Text size="small">{savedNotice}</Text>
        </Card>
      ) : null}

      {!capabilities.canReview ? (
        <Card className="console-card mb-3" bodyStyle={{ padding: 10 }} data-capability-readonly="review">
          <Text size="small">当前账号可以查看内容与来源，但没有提交人工判断的权限。</Text>
        </Card>
      ) : null}

      {/* 三栏：队列 / 内容 / 证据。每栏独立，因此「内容 focus」不会因
          判断保存而丢失（保存只刷新数据，不卸载内容栏）。 */}
      <div
        className="review-pane__columns"
        style={focusContent ? { gridTemplateColumns: 'minmax(0, 1fr)' } : undefined}
        data-review-focus={focusContent ? 'content' : 'all'}
      >
        {!focusContent ? (
          <section className="review-pane__column" aria-label="待判断内容">
            <div className="flex items-center justify-between mb-2">
              <Text strong>队列</Text>
              <Tag size="small" color="amber">
                {queueItems.length}{queueCursor ? '+' : ''} 条
              </Tag>
            </div>
            {queueError ? (
              <div className="wizard-field__error mb-2" role="alert" data-review-queue-error="true">
                {queueError}
                <Button size="small" theme="borderless" onClick={() => void loadQueue()}>
                  重试
                </Button>
              </div>
            ) : null}
            {queueLoading && queueItems.length === 0 ? <Spin size="small" tip="正在加载队列" /> : null}
            <ul className="review-queue" data-review-queue="true">
              {queueWithCurrent.length === 0 && !queueLoading ? (
                <li className="review-queue__empty">这个筛选下没有待判断内容</li>
              ) : (
                queueWithCurrent.map((item) => {
                  const active = item.resourceId === sampleID
                  return (
                    <li
                      key={item.resourceId}
                      className={active ? 'review-queue__item--active' : undefined}
                      style={active ? { border: '1px solid #d6cdf7', borderRadius: 8, background: '#faf8ff' } : undefined}
                    >
                      <button
                        type="button"
                        className="review-queue__button"
                        aria-current={active ? 'page' : undefined}
                        aria-label={`打开 ${item.title || item.sampleKey}`}
                        onClick={() => navigateToQueueItem(item.resourceId)}
                        style={{
                          display: 'block',
                          width: '100%',
                          padding: 0,
                          border: 0,
                          background: 'transparent',
                          textAlign: 'left',
                          cursor: active ? 'default' : 'pointer',
                        }}
                      >
                        <div className="flex items-center justify-between gap-2">
                          <Text strong size="small">{item.title || item.sampleKey}</Text>
                          <Tag size="small" color={statusColor(item.reviewStatus)}>
                            {REVIEW_STATUS_LABEL[item.reviewStatus] ?? item.reviewStatus}
                          </Tag>
                        </div>
                        <Text type="tertiary" size="small" className="block mt-1">
                          {item.resourceId} · v{item.latestVersion}
                          {item.aggregateReviewRevision > 0 ? ` · 判断 ${item.aggregateReviewRevision} 次` : ''}
                        </Text>
                      </button>
                    </li>
                  )
                })
              )}
            </ul>
            {queueCursor ? (
              <Button size="small" className="mt-2" loading={queueLoading} onClick={() => void loadQueue(queueCursor, true)}>
                加载更多队列
              </Button>
            ) : null}
          </section>
        ) : null}

        <section
          className="review-pane__column review-pane__content"
          aria-label="只读内容"
          ref={contentRef}
          tabIndex={-1}
          data-content-focus="true"
        >
          <div className="flex items-center justify-between mb-2">
            <Text strong>
              {detail.sample.targetKind.toLowerCase().includes('grpo') ? '教师提示词与奖励判据（只读）' : '内容（只读）'}
            </Text>
            <Button size="small" icon={<Copy size={13} />} onClick={() => void copyContent()}>
              复制
            </Button>
          </div>
          {/* 内容只读：本区没有任何输入控件，判断也不会改写它。
              默认「人话视图」，原始 JSON 作为可切换的次要视图（issue #197 第 3 条）。 */}
          <PayloadPreview payload={detail.version.payload} />
          {copyFallback !== null ? (
            <div className="mt-3" data-copy-fallback="true">
              <div className="flex items-center justify-between mb-1">
                <Text type="tertiary" size="small">手动复制（只读）</Text>
                <Button size="small" theme="borderless" onClick={() => setCopyFallback(null)}>
                  关闭
                </Button>
              </div>
              <TextArea
                value={copyFallback}
                readOnly
                autosize={{ minRows: 6, maxRows: 16 }}
                aria-label="手动复制内容"
              />
            </div>
          ) : null}
        </section>

        {!focusContent ? <section className="review-pane__column" aria-label="证据与判断">
          <Text strong className="block mb-2">
            证据与判断
          </Text>
          <div className="flex flex-wrap gap-2 mb-2" data-review-evidence-links="true">
            <Button
              size="small"
              theme="borderless"
              onClick={() => navigate(`${projectHref('project.rules', scope.projectId)}?sampleVersionId=${encodeURIComponent(String(detail.version.versionId))}`)}
            >
              查看策略
            </Button>
            <Button
              size="small"
              theme="borderless"
              onClick={() => navigate(`${projectHref('project.quality', scope.projectId)}?sampleVersionId=${encodeURIComponent(String(detail.version.versionId))}`)}
            >
              完整评估
            </Button>
          </div>
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

          <div className="mt-3" data-review-decision-history="true">
            <Text type="tertiary" size="small" className="block mb-1">
              判断历史（{decisions.length}）
            </Text>
            {decisions.length === 0 ? (
              <Text type="tertiary" size="small">还没有判断记录</Text>
            ) : (
              <ul className="review-evidence">
                {decisions.map((decision) => (
                  <li key={decision.id}>
                    <Tag size="small" color={statusColor(decision.action)}>
                      {decision.action === 'accepted' ? '接纳' : '隔离'}
                    </Tag>{' '}
                    <Text size="small">{decision.reason}</Text>
                    <Text type="tertiary" size="small" className="block">
                      审阅者 {decision.reviewerId} · 第 {decision.reviewerRevision} 次
                      {decision.supersedes ? ` · 更正 #${decision.supersedes}` : ''}
                      {decision.resolutionOf ? ` · 协调 #${decision.resolutionOf}` : ''}
                    </Text>
                  </li>
                ))}
              </ul>
            )}
          </div>

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
            {capabilities.canReview ? <Select
              value={action}
              style={{ width: '100%' }}
              aria-label="选择处置"
              optionList={[
                { value: 'accepted', label: '接纳' },
                { value: 'quarantined', label: '隔离' },
              ]}
              onChange={(value) => setAction(value === 'quarantined' ? 'quarantined' : 'accepted')}
            /> : null}
            <Text type="tertiary" size="small" className="block mt-2 mb-1">
              理由（必填）
            </Text>
            {capabilities.canReview ? <TextArea
              value={reason}
              onChange={(value) => setReason(value)}
              autosize={{ minRows: 3, maxRows: 6 }}
              placeholder="例如：规则命中为误报，推理链完整"
              data-field="review-reason"
            /> : null}
            {capabilities.canReview && submitError ? (
              <div className="wizard-field__error mt-1" role="alert" data-review-submit-error="true">
                {submitError}
              </div>
            ) : null}
            {/* 离线待同步（T29）：提交失败时提供本地草稿，并明确「未提交」。 */}
            {capabilities.canReview && submitError ? (
              <div className="mt-1">
                <Button size="small" theme="borderless" onClick={saveOfflineDraft} data-review-offline-draft="true">
                  保存为本地草稿（待同步）
                </Button>
              </div>
            ) : null}
            {capabilities.canReview && offlineNotice ? (
              <Text type="warning" size="small" className="block mt-1" data-review-offline-notice="true">
                {offlineNotice}
              </Text>
            ) : null}
            {capabilities.canReview && pending > 0 ? (
              <Text type="tertiary" size="small" className="block mt-1" data-review-pending-count="true">
                待同步（未提交）：{pending} 条。联网后请重新登录并确认，系统不会后台自动提交。
              </Text>
            ) : null}
            {capabilities.canReview ? <div className="mt-2">
              <Button
                theme="solid"
                type="primary"
                icon={<Save size={14} />}
                loading={submitting}
                onClick={() => void submit()}
              >
                保存判断
              </Button>
            </div> : null}
          </div>

          {/* 评论面板（T27）：锚定**当前内容版本**，与判断分开 —— 讨论不改处置。 */}
          {detail?.version?.versionId && projectNumericId(scope.projectId) ? (
            <div className="mt-3">
              <CommentPanel
                projectId={projectNumericId(scope.projectId) ?? 0}
                anchorKind="sample_version"
                anchorId={detail.version.versionId}
              />
            </div>
          ) : null}
        </section> : null}
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
