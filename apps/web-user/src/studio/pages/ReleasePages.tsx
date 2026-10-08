import { useCallback, useEffect, useState } from 'react'
import { Link, useNavigate, useParams, useSearchParams } from 'react-router-dom'
import type { ReactNode } from 'react'
import { Button, Card, Empty, Input, Modal, Select, Spin, Tag, TextArea, Typography } from '@douyinfe/semi-ui'
import { AlertTriangle, Download, RefreshCw } from 'lucide-react'
import { client } from '../../lib/api'
import type { ApiFieldError } from '../../lib/api/studio'
import { projectNumericId, projectPath, studioApi } from '../../lib/api/studio'
import type {
  BatchSummary,
  DeliveryItem,
  Page,
  ReleaseArtifact,
  ReleaseBlocker,
  ReleaseCard,
  ReleaseCapabilities,
  ReleaseRecord,
  ProjectCapabilities,
  SampleSummary,
  SelectionComposition,
} from '../../lib/api/studio'
import { useProjectScope } from '../ProjectLayout'
import { projectHref } from '../StudioLayout'
import { CopyVersionButton, DocumentHistory, DocumentSaveBar, MappingPayloadEditor, useVersionedDocument } from '../DocumentEditors'

type BlockerLinkProps = {
  link: string
  children: ReactNode
}

/** 服务端可返回页面链接或外部链接；项目内页面必须保留 Atelier 壳与上下文。 */
function BlockerLink({ link, children }: BlockerLinkProps) {
  if (link.startsWith('/')) {
    return <Link className="console-link" to={link}>{children}</Link>
  }
  return <a className="console-link" href={link}>{children}</a>
}

/**
 * 发布与交付页面（Issue #160 T22）：发布列表、准备发布、候选数据卡、交付库。
 *
 * 契约：docs/plans/atelier-api-contract.md §2.8–§2.10、§3（L01–L03/B03）。
 *
 * 三条来自 T22 验收项的落点：
 *
 *  1. **URL 用稳定 releaseId**：页面的所有数据由路由参数取，版本名只用于展示
 *     与文件名。因此「同一版本名改了之后旧链接仍指向同一发布」成立。
 *  2. **阻塞项链到具体对象**：每条 blocker 带 link（服务端给出），
 *     用户不必在几万条内容里自己找。
 *  3. **downloaded 用统一会话与错误处理**：下载 URL 走同源 `/api`，
 *     401/403 由既有拦截器给出可理解的中文提示；文件名由服务端设置，
 *     含类型与版本名而不叫 latest。
 */

const RELEASE_STATUS_LABEL: Record<string, string> = {
  candidate: '候选（未发布）',
  blocked: '被门槛阻塞',
  building: '构建中',
  published: '已发布',
  build_failed: '构建失败（可幂等续推）',
}

/**
 * 发布格式是候选配置的一部分，而不是构建失败后才发现的实现细节。
 * SFT 只展示本轮产品承诺的三种编码；GRPO 由服务端 schema 约束为 JSONL。
 */
const SFT_FORMAT_OPTIONS = [
  { value: 'jsonl', label: 'JSONL' },
  { value: 'alpaca', label: 'Alpaca JSON' },
  { value: 'csv', label: 'CSV' },
]
const GRPO_FORMAT_OPTIONS = [{ value: 'jsonl', label: 'JSONL（GRPO）' }]

type GroundingSummary = { chunks: number; missing: number; groundedSamples: number; ungroundedSamples: number; externalImports: number }
function groundingSummaryOf(manifest: unknown): GroundingSummary | null {
  if (!manifest || typeof manifest !== 'object' || !('grounding' in manifest)) return null
  const summary = manifest.grounding
  if (!summary || typeof summary !== 'object') return null
  const fields = ['chunks', 'missing', 'groundedSamples', 'ungroundedSamples', 'externalImports'] as const
  if (fields.some((key) => !(key in summary) || typeof (summary as Record<string, unknown>)[key] !== 'number' || !Number.isFinite((summary as Record<string, number>)[key]))) return null
  return summary as GroundingSummary
}

function statusColor(status: string): 'green' | 'red' | 'amber' | 'grey' {
  switch (status) {
    case 'published':
      return 'green'
    case 'blocked':
    case 'build_failed':
      return 'red'
    case 'building':
      return 'amber'
    default:
      return 'grey'
  }
}

/**
 * 把快照构成摘成一句人话（issue #203）。
 *
 * 为什么要它：区块标题以前写死「已接纳」，而冻结范围其实含未审阅内容。
 * 这里把服务端下发的构成展开成可核对的数字，回答用户最关心的一件事：
 * 「我这一份发布范围里，到底有多少条是我真审过的」。
 * 构成缺失（旧后端）时返回空串 —— 此时标题给中性文案，**不编造「已接纳」**。
 */
function selectionCompositionSummary(composition: SelectionComposition | null): string {
  if (!composition) return ''
  const parts = [`已接纳 ${composition.accepted} 条`]
  if (composition.pending > 0) parts.push(`未审阅 ${composition.pending} 条`)
  if (composition.quarantined > 0) parts.push(`已隔离 ${composition.quarantined} 条`)
  if (composition.conflict > 0) parts.push(`存在冲突 ${composition.conflict} 条`)
  return parts.join(' · ')
}

// ---------------------------------------------------------------------------
// 发布列表（L01）
// ---------------------------------------------------------------------------

export function ReleasesListPage() {
  const scope = useProjectScope()
  const navigate = useNavigate()
  const { Title, Text } = Typography
  const [releases, setReleases] = useState<ReleaseRecord[]>([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)
  const [canPublish, setCanPublish] = useState(false)

  const load = useCallback(async () => {
    setLoading(true)
    setError(null)
    try {
      const [response, overview] = await Promise.all([
        studioApi.listReleases(scope.projectId),
        studioApi.overviewEnvelope(scope.projectId),
      ])
      setReleases(response.items ?? [])
      setCanPublish(overview.capabilities.canPublish === true)
    } catch (loadError) {
      setError(loadError instanceof Error ? loadError.message : '加载发布列表失败')
    } finally {
      setLoading(false)
    }
  }, [scope.projectId])

  useEffect(() => {
    void load()
  }, [load])

  if (loading) {
    return (
      <div className="flex justify-center py-10">
        <Spin tip="正在加载发布版本" />
      </div>
    )
  }
  if (error) {
    return (
      <Card className="console-card">
        <Text strong className="block">加载失败</Text>
        <Text type="tertiary">{error}</Text>
      </Card>
    )
  }

  return (
    <div className="console-page" data-studio-page="releases">
      <div className="console-page__header">
        <div>
          <Title heading={4} className="!mb-1">发布版本</Title>
          <Text type="tertiary">
            候选与已发布分开显示；已发布的内容、映射、数据卡与 hash 只读。
          </Text>
        </div>
        <div className="flex gap-2">
          <Button icon={<RefreshCw size={14} />} onClick={() => void load()}>刷新发布列表</Button>
          {canPublish ? (
            <Button theme="solid" type="primary" onClick={() => navigate(projectHref('project.newRelease', scope.projectId))}>
              准备发布
            </Button>
          ) : null}
        </div>
      </div>

      {releases.length === 0 ? (
        <Card className="console-card">
          <Empty description="还没有发布版本。先完成质量检查与人工判断，再准备发布。" />
        </Card>
      ) : (
        <div className="batch-table" data-release-table="true">
          <div className="batch-row batch-row--head">
            <span>发布 ID</span>
            <span>版本名</span>
            <span>状态</span>
            <span>用途</span>
            <span>操作</span>
          </div>
          {releases.map((release) => (
            <div key={release.id} className="batch-row" data-release-id={release.id}>
              <span>{release.id}</span>
              <span>{release.releaseName}</span>
              <span>
                <Tag size="small" color={statusColor(release.status)}>
                  {RELEASE_STATUS_LABEL[release.status] ?? release.status}
                </Tag>
              </span>
              <span>{release.intendedUse || '（未填写）'}</span>
              <span>
                <Button size="small" onClick={() => navigate(projectHref('project.releaseCard', scope.projectId, { releaseId: release.id }))}>
                  数据卡
                </Button>
              </span>
            </div>
          ))}
        </div>
      )}
    </div>
  )
}

// ---------------------------------------------------------------------------
// 准备发布（L02）
// ---------------------------------------------------------------------------

/**
 * 服务端字段名 → 页面上那个「错误落点」的锚点（issue #213）。
 *
 * 为什么需要一层翻译：服务端的字段名是**请求体字段**（`sampleVersionIds` /
 * `selectionSnapshotId`），而页面上并没有叫这个名字的输入框 —— 它们共同对应
 * 「发布范围」那一区。直接拿服务端字段名去查锚点会静默漏掉提示（正是缺陷本身）。
 *
 * 未列入的字段名原样传递：未知字段保守地当作同名锚点，命中不到时页面上看不到该提示
 * —— 这比丢掉错误好，但不如显式登记，因此新增服务端字段时必须同步本表。
 */
const RELEASE_FIELD_ANCHORS: Record<string, string> = {
  sampleVersionIds: 'range',
  selectionSnapshotId: 'range',
  range: 'range',
  intendedUse: 'intendedUse',
  releaseName: 'releaseName',
  format: 'format',
  mappingVersionId: 'mappingVersionId',
}

export function ReleaseNewPage() {
  const scope = useProjectScope()
  const navigate = useNavigate()
  const [searchParams] = useSearchParams()
  const { Title, Text } = Typography
  const mappingState = useVersionedDocument(scope.projectId, 'mapping-versions')

  // `?selection=` 指向服务端选择快照（T17）：URL 只带快照 ID，不带 ID 列表。
  // 非法值必须在页面层被识别为错误，不能悄悄回退成手工范围。
  const selectionParam = searchParams.get('selection')
  const parsedSelectionID = selectionParam ? Number(selectionParam) : 0
  const selectionSnapshotID = Number.isSafeInteger(parsedSelectionID) && parsedSelectionID > 0 ? parsedSelectionID : 0
  const hasInvalidSelectionParam = Boolean(selectionParam) && selectionSnapshotID === 0
  const numericProjectId = projectNumericId(scope.projectId) ?? 0

  const [samples, setSamples] = useState<SampleSummary[]>([])
  const [batches, setBatches] = useState<BatchSummary[]>([])
  const [samplesNextCursor, setSamplesNextCursor] = useState('')
  const [samplesLoading, setSamplesLoading] = useState(false)
  const [selected, setSelected] = useState<number[]>([])
  const [selectionSnapshotItems, setSelectionSnapshotItems] = useState<number[] | null>(null)
  /**
   * 快照范围的实际构成（issue #203）。
   *
   * 缺陷形态：区块标题写死「发布范围（已接纳的内容版本）」，而冻结范围其实含
   * 未审阅内容 —— 语义与事实不一致，比数值错误更危险。构成由**服务端**统计下发
   * （`GET P/selection-snapshots/{id}` 的 `composition`），前端不再凭标题自行断言。
   * `null` 表示快照未就绪或后端未下发：此时标题给中性文案，**不写死「已接纳」**。
   */
  const [selectionComposition, setSelectionComposition] = useState<SelectionComposition | null>(null)
  const [selectionSnapshotState, setSelectionSnapshotState] = useState<'none' | 'loading' | 'ready' | 'invalid'>('none')
  const [releaseName, setReleaseName] = useState('v1.0')
  const [mappingVersionId, setMappingVersionId] = useState('')
  const [format, setFormat] = useState('jsonl')
  const [intendedUse, setIntendedUse] = useState('')
  const [limitations, setLimitations] = useState('')
  const [snapshotNotice, setSnapshotNotice] = useState<string | null>(null)
  // GRPO 的发布格式与映射要求与 SFT 不同（T25）：界面必须提示用户，
  // 而不是让他在构建失败后才从错误里推出来。
  const [targetKind, setTargetKind] = useState('sft')
  const [projectCapabilities, setProjectCapabilities] = useState<ProjectCapabilities | null>(null)
  const [blockers, setBlockers] = useState<ReleaseBlocker[]>([])
  const [error, setError] = useState<string | null>(null)
  /**
   * 字段级错误（issue #213）。
   *
   * 缺陷形态：页面只有一个 `error` 字符串，**多字段错误时只显示最后一条** ——
   * 用户修完「用途」才发现「发布范围」也是空的，而在长页面里还得自己找那个字段。
   * 服务端**本来就**返回结构化的 `fieldErrors[]`（带 `field`），只是被丢了。
   *
   * 键是服务端字段名（`intendedUse` / `releaseName` / `format` / `range`）。
   */
  const [fieldErrors, setFieldErrors] = useState<Record<string, string>>({})
  const [busy, setBusy] = useState(false)

  /**
   * 已接纳范围也必须走游标分页。此前只取前 100 条并在 UI 截断到 50 条，
   * 会让用户误以为「可发布范围」只有首屏内容，且无法完成大项目的精确选择。
   */
  const loadAcceptedSamples = useCallback(async (cursor = '', append = false) => {
    setSamplesLoading(true)
    try {
      const params = new URLSearchParams({ status: 'accepted', limit: '100' })
      if (cursor !== '') params.set('cursor', cursor)
      const response = await client.get<Page<SampleSummary>>(
        `${projectPath(scope.projectId)}/samples?${params.toString()}`,
      )
      setSamples((previous) => (append ? [...previous, ...(response.data.items ?? [])] : (response.data.items ?? [])))
      setSamplesNextCursor(response.data.nextCursor ?? '')
    } catch (loadError) {
      setError(loadError instanceof Error ? loadError.message : '加载可选范围失败')
    } finally {
      setSamplesLoading(false)
    }
  }, [scope.projectId])

  useEffect(() => {
    let cancelled = false
    void (async () => {
      try {
        const batchResponse = await client.get<Page<BatchSummary>>(`${projectPath(scope.projectId)}/batches?limit=50`)
        if (!cancelled) setBatches(batchResponse.data.items ?? [])
        if (!cancelled) await loadAcceptedSamples()
      } catch (loadError) {
        if (!cancelled) setError(loadError instanceof Error ? loadError.message : '加载可选范围失败')
      }
    })()
    return () => { cancelled = true }
  }, [loadAcceptedSamples, scope.projectId])

  useEffect(() => {
    let cancelled = false
    void (async () => {
      try {
        const response = await studioApi.overviewEnvelope(scope.projectId)
        if (!cancelled) {
          const nextTargetKind = response.data.targetKind ?? 'sft'
          setTargetKind(nextTargetKind)
          setProjectCapabilities(response.capabilities)
          setMappingVersionId((previous) => previous || (
            response.data.versions.mapping && response.data.versions.mapping.versionId > 0
              ? String(response.data.versions.mapping.versionId)
              : ''
          ))
          if (nextTargetKind === 'grpo') setFormat('jsonl')
        }
      } catch {
        // 读取失败时回退 SFT：该值只影响提示文案，不参与服务端校验，
        // 因此失败方向是「少一条提示」而不是「提交错格式」。
      }
    })()
    return () => { cancelled = true }
  }, [scope.projectId])

  useEffect(() => {
    if (mappingState.current && mappingVersionId === '') {
      setMappingVersionId(String(mappingState.current.id))
    }
  }, [mappingState.current, mappingVersionId])

  // 从服务端选择快照恢复范围（**重新鉴权**由服务端完成）。
  useEffect(() => {
    if (selectionParam && selectionSnapshotID <= 0) {
      setSelectionSnapshotState('invalid')
      setSelectionSnapshotItems(null)
      setSelectionComposition(null)
      setSnapshotNotice('发布范围快照链接无效，请从样本工作区重新选择范围')
      return
    }
    if (selectionSnapshotID <= 0) {
      setSelectionSnapshotState('none')
      setSelectionSnapshotItems(null)
      setSelectionComposition(null)
      setSnapshotNotice(null)
      return
    }
    setSelectionSnapshotState('loading')
    setSelectionSnapshotItems(null)
    setSelectionComposition(null)
    let cancelled = false
    void (async () => {
      try {
        const resolved = await studioApi.getSelectionSnapshot(scope.projectId, selectionSnapshotID)
        if (cancelled) return
        if (resolved.snapshot?.projectId !== numericProjectId || resolved.snapshot?.id !== selectionSnapshotID) {
          setSelectionSnapshotState('invalid')
          setSnapshotNotice('选择范围与当前项目不一致，请从样本工作区重新选择')
          return
        }
        if (resolved.snapshot?.purpose !== 'release') {
          setSelectionSnapshotState('invalid')
          setSnapshotNotice('这份选择范围不是发布用途，不能用于创建发布候选；请重新冻结发布范围')
          return
        }
        const items = resolved.items ?? []
        if (resolved.count !== items.length || resolved.snapshot.itemCount !== resolved.count) {
          setSelectionSnapshotState('invalid')
          setSnapshotNotice('选择范围明细不完整，已停止提交；请重新冻结发布范围')
          return
        }
        // 快照存的是**内容版本行 ID**；恢复时直接作为候选范围，不能只显示数量。
        setSelectionSnapshotItems(items)
        // issue #203：构成由服务端下发，标题按它渲染。后端未下发时置 null，
        // 标题就不写死「已接纳」（旧后端也不应显示错误断言）。
        setSelectionComposition(resolved.composition ?? null)
        setSelectionSnapshotState('ready')
        const summary = selectionCompositionSummary(resolved.composition ?? null)
        setSnapshotNotice(`已从服务端选择范围恢复 ${resolved.count} 个内容版本（快照 ${selectionSnapshotID}）${summary ? `：${summary}` : ''}。`)
      } catch (snapshotError) {
        if (!cancelled) {
          setSelectionSnapshotState('invalid')
          setSelectionSnapshotItems(null)
          setSelectionComposition(null)
          setSnapshotNotice(snapshotError instanceof Error ? snapshotError.message : '选择范围已过期，请重新选择')
        }
      }
    })()
    return () => { cancelled = true }
  }, [numericProjectId, scope.projectId, selectionParam, selectionSnapshotID])

  /**
   * 只设一个字段的错误（issue #213）。
   *
   * 前端校验的错误与后端 `fieldErrors` 走**同一份状态**：两者都渲染在对应输入框
   * 下方（而不是页底一行字），否则本地提示与远端提示会出现在两个不同位置，
   * 用户得在两处找原因。`range` 不是输入框而是筛选区，因此用它自己的锚点。
   */
  const setFieldError = useCallback((field: string, message: string) => {
    setError(message)
    setFieldErrors(message ? { [field]: message } : {})
  }, [])

  /** 把服务端返回的 fieldErrors 落成字段级提示；返回是否真的用上了字段级。 */
  const applyServerFieldErrors = useCallback((errors: ApiFieldError[] | undefined): boolean => {
    if (!errors || errors.length === 0) return false
    const next: Record<string, string> = {}
    for (const item of errors) {
      if (!item?.field) continue
      const anchor = RELEASE_FIELD_ANCHORS[item.field] ?? item.field
      // 同一字段多条只留第一条：输入框下方只放一行，多行会把布局推得很难读，
      // 而服务端的第一条就是最根本的那条（与向导页的 `groupServerErrors` 同一取舍）。
      next[anchor] ??= item.message
    }
    if (Object.keys(next).length === 0) return false
    setFieldErrors(next)
    setError(errors.map((item) => item.message).filter(Boolean).join('；'))
    return true
  }, [])

  const submit = useCallback(async () => {
    setError(null)
    setFieldErrors({})
    setBlockers([])
    if (!projectCapabilities?.canPublish) {
      setError('当前项目没有发布权限；请联系项目负责人')
      return
    }
    if (hasInvalidSelectionParam || selectionSnapshotState === 'invalid') {
      setFieldError('range', '发布范围快照无效或已过期，请返回样本工作区重新选择')
      return
    }
    if (selectionSnapshotID > 0 && selectionSnapshotState === 'loading') {
      setFieldError('range', '正在恢复服务端发布范围，请稍候再提交')
      return
    }
    if (selectionSnapshotID > 0 && selectionSnapshotState !== 'ready') {
      setFieldError('range', '尚未恢复服务端发布范围，请返回样本工作区重新选择')
      return
    }
    const selectedVersionIDs = selectionSnapshotID > 0 ? selectionSnapshotItems ?? [] : selected
    if (selectedVersionIDs.length === 0) {
      setFieldError('range', '发布范围不能为空：请选择要发布的内容版本')
      return
    }
    if (intendedUse.trim() === '') {
      setFieldError('intendedUse', '必须填写用途：数据卡要能说清这份数据用来做什么')
      return
    }
    if (targetKind === 'grpo' && format !== 'jsonl') {
      setFieldError('format', 'GRPO 只能发布 JSONL；请切换格式后再提交')
      return
    }
    if (format.trim() === '') {
      setFieldError('format', '请选择发布格式')
      return
    }
    setBusy(true)
    try {
      const result = await studioApi.createReleaseCandidate(scope.projectId, {
        releaseName: releaseName.trim(),
        // 从审阅页进入时只提交服务端快照 ID；后端会在候选事务内
        // 重新鉴权并解析明细，客户端不能通过篡改版本列表改变发布范围。
        ...(selectionSnapshotID > 0
          ? { selectionSnapshotId: selectionSnapshotID }
          : {
              // 手工范围提交的是**sample_versions 行 ID**；样本身份 ID
              // 与样本内版本号都不能替代它。
              sampleVersionIds: selectedVersionIDs,
            }),
        mappingVersionId: Number(mappingVersionId) || 0,
        format,
        intendedUse: intendedUse.trim(),
        limitations: limitations
          .split('\n')
          .map((line) => line.trim())
          .filter((line) => line !== ''),
      })
      setBlockers(result.blockers ?? [])
      // 导航到**服务端分配的**稳定 releaseId。
      navigate(projectHref('project.releaseCard', scope.projectId, { releaseId: result.release.id }))
    } catch (submitError) {
      // 服务端返回结构化 fieldErrors 时**逐字段渲染**（issue #213）；
      // 只有拿不到时才退回单行总体提示 —— 而不是把多字段错误压成一句话。
      const apiError = submitError as { fieldErrors?: ApiFieldError[] }
      if (!applyServerFieldErrors(apiError?.fieldErrors)) {
        setError(submitError instanceof Error ? submitError.message : '创建发布候选失败')
      }
    } finally {
      setBusy(false)
    }
  }, [
    hasInvalidSelectionParam,
    format,
    intendedUse,
    limitations,
    mappingVersionId,
    navigate,
    releaseName,
    scope.projectId,
    selected,
    selectionSnapshotID,
    selectionSnapshotItems,
    selectionSnapshotState,
    targetKind,
    applyServerFieldErrors,
    setFieldError,
    projectCapabilities?.canPublish,
  ])

  return (
    <div className="console-page" data-studio-page="release-new">
      <div className="console-page__header">
        <div>
          <Title heading={4} className="!mb-1">准备发布</Title>
          <Text type="tertiary">
            候选创建时同时分配候选 ID、稳定的发布 ID 与项目内唯一的版本名；
            发布失败重试沿用同一身份。
          </Text>
        </div>
      </div>

      <div id="mapping-editor">
        <Card className="console-card mb-3" bodyStyle={{ padding: 16 }} data-mapping-editor="true">
          <div className="console-page__header document-editor__embedded-header">
            <div>
              <Text strong>交付映射</Text>
              <Text type="tertiary" size="small" className="block">先在这里维护字段对应关系，下面的发布候选会引用你保存的版本。</Text>
            </div>
            <CopyVersionButton state={mappingState} />
          </div>
          {mappingState.loading ? <Spin tip="正在加载映射版本" /> : <>
            <MappingPayloadEditor payload={mappingState.payload ?? { schemaVersion: 'mapping.v1', format: targetKind === 'grpo' ? 'jsonl' : 'jsonl', fields: [] }} disabled={mappingState.isReadOnly || !mappingState.canEdit} onChange={mappingState.setPayload} />
            <DocumentSaveBar state={mappingState} label="交付映射" />
            <DocumentHistory state={mappingState} />
          </>}
        </Card>
      </div>

      {snapshotNotice ? (
        <Card className="console-card mb-3" bodyStyle={{ padding: 12 }} data-selection-restored="true">
          <Text size="small">{snapshotNotice}</Text>
        </Card>
      ) : null}

      <Card className="console-card mb-3" bodyStyle={{ padding: 16 }}>
        <div className="wizard-fields">
          {/*
            issue #213：每个字段的错误渲染在**它自己的输入框下方**，并用
            `aria-describedby` 关联，而不是只在页底给一行字。页底提示在长页面里
            要求用户自己找字段，而读屏用户拿不到任何关联。
          */}
          <div className="wizard-field" data-field="releaseName" data-invalid={fieldErrors.releaseName ? 'true' : undefined}>
            <label className="wizard-field__label" htmlFor="release-name">版本名</label>
            <Input id="release-name" value={releaseName} onChange={(value) => setReleaseName(value)}
              placeholder="例如 v1.2（不能叫 latest）"
              aria-invalid={fieldErrors.releaseName ? true : undefined}
              aria-describedby={fieldErrors.releaseName ? 'release-name-error' : undefined} />
            <Text type="tertiary" size="small" className="block mt-1">
              版本名用于展示与文件名；下载路径只用稳定的发布 ID。
            </Text>
            {fieldErrors.releaseName ? (
              <div className="wizard-field__error" id="release-name-error" role="alert">{fieldErrors.releaseName}</div>
            ) : null}
          </div>
          <div className="wizard-field">
            <label className="wizard-field__label" htmlFor="mapping-version">用于本次发布的映射版本</label>
            <Select id="mapping-version" value={mappingVersionId || undefined}
              optionList={mappingState.versions.map((version) => ({ value: String(version.id), label: `v${version.version} · ${version.changeReason || '未填写理由'}` }))}
              onChange={(value) => setMappingVersionId(String(value))}
              placeholder={mappingState.versions.length > 0 ? '请选择已保存的映射版本' : '暂无可用映射版本'}
              disabled={mappingState.versions.length === 0} />
            <Text type="tertiary" size="small" className="block mt-1">
              发布会冻结这个版本的字段映射；后续修改需要保存为新版本。
            </Text>
            {mappingState.versions.length === 0 ? (
              <div className="version-choice-empty" role="status">
                <Text type="tertiary" size="small">还没有保存映射版本，请先在上方编辑并保存交付映射。</Text>
                <Button size="small" theme="borderless" onClick={() => document.getElementById('mapping-editor')?.scrollIntoView({ behavior: 'smooth', block: 'start' })}>去创建映射版本 →</Button>
              </div>
            ) : null}
          </div>
          <div className="wizard-field" data-field="format" data-invalid={fieldErrors.format ? 'true' : undefined}>
            <label className="wizard-field__label" htmlFor="release-format">交付格式</label>
            <Select
              id="release-format"
              value={format}
              optionList={targetKind === 'grpo' ? GRPO_FORMAT_OPTIONS : SFT_FORMAT_OPTIONS}
              onChange={(value) => setFormat(String(value))}
              aria-label="选择交付格式"
              aria-invalid={fieldErrors.format ? true : undefined}
              aria-describedby={fieldErrors.format ? 'release-format-error' : undefined}
              style={{ width: '100%' }}
            />
            <Text type="tertiary" size="small" className="block mt-1">
              {targetKind === 'grpo'
                ? 'GRPO 只允许 JSONL，服务端会逐行校验教师评判字段。'
                : 'SFT 可选择 JSONL、Alpaca JSON 或 CSV；格式会写入不可变发布清单。'}
            </Text>
            {fieldErrors.format ? (
              <div className="wizard-field__error" id="release-format-error" role="alert">{fieldErrors.format}</div>
            ) : null}
            {targetKind === 'grpo' ? (
              <Text type="tertiary" size="small" className="block mt-1" data-grpo-release-hint="true">
                映射必须包含 question / judge_prompt / levels / level_rubrics；
                levels 与 level_rubrics 必须保留数组与对象结构，不能压成逗号字符串。
              </Text>
            ) : null}
          </div>
          <div className="wizard-field" data-field="intendedUse" data-invalid={fieldErrors.intendedUse ? 'true' : undefined}>
            <label className="wizard-field__label" htmlFor="intended-use">用途</label>
            <Input id="intended-use" value={intendedUse} onChange={(value) => setIntendedUse(value)}
              placeholder="例如 SFT 训练"
              aria-invalid={fieldErrors.intendedUse ? true : undefined}
              aria-describedby={fieldErrors.intendedUse ? 'intended-use-error' : undefined} />
            {fieldErrors.intendedUse ? (
              <div className="wizard-field__error" id="intended-use-error" role="alert">{fieldErrors.intendedUse}</div>
            ) : null}
          </div>
          <div className="wizard-field">
            <label className="wizard-field__label" htmlFor="limitations">限制（每行一条）</label>
            <TextArea id="limitations" value={limitations} onChange={(value) => setLimitations(value)}
              autosize={{ minRows: 2, maxRows: 4 }} placeholder="例如：仅覆盖冷链领域" />
          </div>
        </div>
      </Card>

      <Card className="console-card mb-3" bodyStyle={{ padding: 16 }} data-range-picker="true">
        {/* issue #203：标题必须按**实际构成**渲染。旧实现写死「已接纳的内容版本」，
            而冻结范围含未审阅内容 —— 用户因此建立「进了候选就已审过」的错误心智模型。 */}
        <Text strong className="block mb-2">
          {selectionSnapshotID > 0 && selectionComposition
            ? `发布范围（快照 ${selectionSnapshotID}：${selectionCompositionSummary(selectionComposition)}）`
            : '发布范围'}
        </Text>
        {selectionSnapshotID > 0 && selectionComposition && selectionComposition.pending > 0 ? (
          <Text type="warning" size="small" className="block mb-2" data-range-unreviewed-warning="true">
            这份范围含 {selectionComposition.pending} 条未审阅内容；候选门槛会拦住未接纳的内容，
            但它们已进入你的心智模型 —— 请确认这确实是你想发布的范围。
          </Text>
        ) : null}
        <Text type="tertiary" size="small" className="block mb-2">
          已选 {selectionSnapshotID > 0 ? selectionSnapshotItems?.length ?? 0 : selected.length} 条
          {selectionSnapshotID > 0 ? '（来自服务端冻结快照，范围已锁定）' : '（当前页）'}。候选保存的是具体内容版本，不是筛选条件。
        </Text>
        {/*
          issue #213：「范围为空」不是一个输入框的错，而是这一整块筛选区的错。
          因此错误渲染在这一区（而不是页底），并给出 `data-field="range"`
          供守卫与焦点定位使用。
        */}
        {fieldErrors.range ? (
          <div className="wizard-field__error mb-2" role="alert" data-range-error="true">{fieldErrors.range}</div>
        ) : null}
        {samples.length === 0 ? (
          <Empty description="还没有已接纳的内容。请先在审阅队列中完成判断。" />
        ) : (
          <div className="sample-table">
            <div className="sample-row sample-row--head"><span /><span>内容版本</span><span>批次</span></div>
            {samples.map((sample) => (
              <div key={sample.sampleId} className="sample-row">
                <input type="checkbox" aria-label={`选择 ${sample.title || sample.sampleKey}`}
                  checked={sample.latestVersionId > 0 && selected.includes(sample.latestVersionId)}
                  disabled={selectionSnapshotID > 0 || sample.latestVersionId <= 0}
                  onChange={(event) => {
                    const versionID = sample.latestVersionId
                    if (versionID <= 0) return
                    setSelected((previous) => event.target.checked
                      ? [...previous, versionID]
                      : previous.filter((id) => id !== versionID))
                  }} />
                <span>{sample.title || sample.sampleKey} · v{sample.latestVersion}
                  {sample.latestVersionId > 0 ? `（版本 ID ${sample.latestVersionId}）` : '（暂无内容版本）'}
                </span>
                <span>{sample.originBatchId ? `#${sample.originBatchId}` : '—'}</span>
              </div>
            ))}
          </div>
        )}
        {samplesNextCursor !== '' ? (
          <div className="mt-3 flex justify-center">
            <Button
              size="small"
              loading={samplesLoading}
              onClick={() => void loadAcceptedSamples(samplesNextCursor, true)}
            >
              加载更多已接纳内容
            </Button>
          </div>
        ) : null}
        {batches.length === 0 ? null : (
          <Text type="tertiary" size="small" className="block mt-2">
            提示：同一批次的输出通常一起发布；跨批次混合会扩大数据卡的覆盖范围。
          </Text>
        )}
      </Card>

      {blockers.length > 0 ? (
        <Card className="console-card mb-3" bodyStyle={{ padding: 14 }} data-candidate-blockers="true">
          <Text strong className="block mb-1">候选门槛未通过</Text>
          {blockers.map((blocker, index) => (
            <div key={`${blocker.code}-${index}`} className="flex items-start gap-2">
              <AlertTriangle size={14} className="mt-1 text-amber-500" aria-hidden />
              {blocker.link ? (
                <BlockerLink link={blocker.link}>{blocker.message}</BlockerLink>
              ) : (
                <Text size="small">{blocker.message}</Text>
              )}
            </div>
          ))}
        </Card>
      ) : null}

      {/*
        issue #213：字段级提示已经在各自的输入框下方渲染，因此页底这行只在
        「错误没有对应字段」时才出现。否则同一句话会在页面顶部与底部各出现一次，
        而用户会以为发生了两件事。
      */}
      {error && Object.keys(fieldErrors).length === 0 ? (
        <div className="wizard-field__error mb-3" role="alert" data-release-error="true">{error}</div>
      ) : null}

      <Button theme="solid" type="primary" loading={busy} disabled={!projectCapabilities?.canPublish} onClick={() => void submit()}>
        创建发布候选
      </Button>
    </div>
  )
}

// ---------------------------------------------------------------------------
// 数据卡（L03）
// ---------------------------------------------------------------------------

export function ReleaseCardPage() {
  const scope = useProjectScope()
  const params = useParams()
  const navigate = useNavigate()
  const { Title, Text } = Typography
  // 稳定 releaseId 来自路由：版本名改了也不影响这个链接。
  const releaseID = Number(params.releaseId ?? 0)

  const [card, setCard] = useState<ReleaseCard | null>(null)
  const [capabilities, setCapabilities] = useState<ReleaseCapabilities>({
    canPublish: false,
    canDownload: false,
    canCreateNext: false,
  })
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)
  const [actionError, setActionError] = useState<string | null>(null)
  const [busy, setBusy] = useState(false)

  const load = useCallback(async () => {
    setLoading(true)
    setError(null)
    try {
      const response = await studioApi.getReleaseCardEnvelope(scope.projectId, releaseID)
      setCard(response.data)
      setCapabilities(response.capabilities)
    } catch (loadError) {
      setError(loadError instanceof Error ? loadError.message : '加载数据卡失败')
    } finally {
      setLoading(false)
    }
  }, [releaseID, scope.projectId])

  useEffect(() => {
    void load()
  }, [load])

  const publish = useCallback(async () => {
    setBusy(true)
    setActionError(null)
    try {
      await studioApi.publishRelease(scope.projectId, releaseID)
      await load()
    } catch (publishError) {
      // 门槛未过返回 409 + 结构化 blocker；保留它们让用户能直接跳到
      // 样本/证据，而不是把服务端给出的可执行链压成一句错误文本。
      const apiError = publishError as { message?: string; blockers?: ReleaseBlocker[] }
      if (Array.isArray(apiError.blockers) && apiError.blockers.length > 0) {
        setCard((previous) => previous ? { ...previous, blockers: apiError.blockers ?? previous.blockers } : previous)
      }
      setActionError(apiError.message ?? '发布失败')
    } finally {
      setBusy(false)
    }
  }, [load, releaseID, scope.projectId])

  const confirmPublish = useCallback(() => {
    Modal.confirm({
      title: `冻结并发布「${card?.release.releaseName ?? `#${releaseID}`}」？`,
      content: (
        <div className="console-stack">
          <Text className="block">
            发布会冻结当前候选的内容版本、映射、质量证据与限制，并开始构建不可变交付文件。
          </Text>
          <Text type="tertiary" className="block">
            冻结后不能直接修改范围；需要调整时请创建下一版候选。构建失败会保留同一个发布 ID，可安全重试。
          </Text>
        </div>
      ),
      okText: '确认冻结并发布',
      cancelText: '返回检查',
      onOk: publish,
    })
  }, [card?.release.releaseName, publish, releaseID])

  const createNext = useCallback(async () => {
    setBusy(true)
    setActionError(null)
    try {
      const result = await studioApi.createNextCandidate(scope.projectId, releaseID)
      navigate(projectHref('project.releaseCard', scope.projectId, { releaseId: result.release.id }))
    } catch (nextError) {
      setActionError(nextError instanceof Error ? nextError.message : '创建下一版失败')
    } finally {
      setBusy(false)
    }
  }, [navigate, releaseID, scope.projectId])

  if (loading) {
    return <div className="flex justify-center py-10"><Spin tip="正在加载数据卡" /></div>
  }
  if (error || !card) {
    return (
      <Card className="console-card">
        <Text strong className="block">数据卡加载失败</Text>
        <Text type="tertiary">{error ?? '未知错误'}</Text>
        <div className="mt-3"><Button size="small" onClick={() => void load()}>重试</Button></div>
      </Card>
    )
  }

  const { release } = card
  const published = release.status === 'published'
  const grounding = groundingSummaryOf(card.manifest)

  return (
    <div className="console-page" data-studio-page="release-card" data-release-status={release.status}>
      <div className="console-page__header">
        <div>
          <Title heading={4} className="!mb-1">
            发布 {release.releaseName}
          </Title>
          <Text type="tertiary">
            稳定发布 ID：<code>{release.id}</code> · 候选修订 {release.candidateRevision}
            {card.manifestHash ? ` · manifest ${card.manifestHash.slice(0, 20)}…` : ''}
          </Text>
        </div>
        <div className="flex flex-wrap items-center gap-2">
          <Tag color={statusColor(release.status)}>{RELEASE_STATUS_LABEL[release.status] ?? release.status}</Tag>
          {!published && capabilities.canPublish ? (
            <Button theme="solid" type="primary" loading={busy} onClick={confirmPublish}>
              冻结并发布
            </Button>
          ) : published && capabilities.canCreateNext ? (
            <Button loading={busy} onClick={() => void createNext()}>创建下一版</Button>
          ) : null}
        </div>
      </div>

      {actionError ? <div className="wizard-field__error mb-3" role="alert">{actionError}</div> : null}

      {published ? (
        <Card className="console-card mb-3" bodyStyle={{ padding: 12 }} data-published-notice="true">
          <Text size="small">
            该版本已发布：内容、映射、数据卡与指纹只读。之后修改项目配置、隔离样本或切换默认存储，
            都不会改变这些文件；后续风险通过独立警告表达。
          </Text>
        </Card>
      ) : null}

      {/* 阻塞项：每条链到具体对象（服务端给 link）。 */}
      {card.blockers.length > 0 ? (
        <Card className="console-card mb-3" bodyStyle={{ padding: 14 }} data-release-blockers="true">
          <Text strong className="block mb-1">发布阻塞项</Text>
          {card.blockers.map((blocker, index) => (
            <div key={`${blocker.code}-${index}`} className="flex items-start gap-2 mb-1">
              <AlertTriangle size={14} className="mt-1 text-amber-500" aria-hidden />
              {blocker.link ? (
                <BlockerLink link={blocker.link}>{blocker.message}</BlockerLink>
              ) : (
                <Text size="small">{blocker.message}</Text>
              )}
            </div>
          ))}
        </Card>
      ) : null}

      <div className="console-stat-grid">
        <StatTile label="用途" value={release.intendedUse || '（未填写）'} hint="数据卡必须写清用途" />
        <StatTile label="格式" value={release.format} hint={release.targetKind === 'grpo' ? 'GRPO 仅支持 JSONL，保留档位与判据数组' : 'SFT 支持 JSONL/CSV/Alpaca'} />
        <StatTile label="限制" value={String(release.limitations.length)} hint="每行一条；空限制表示无声明" />
        <StatTile label="制品" value={String(card.artifacts.length)} hint="注册/校验/失败三态" />
      </div>

      <Card className="console-card mb-3" bodyStyle={{ padding: 14 }}>
        <Text strong className="block mb-2">交付文件</Text>
        {card.artifacts.length === 0 ? (
          <Text type="tertiary" size="small">
            还没有制品。{published ? '' : '发布后由构建作业产出并校验。'}
          </Text>
        ) : (
          card.artifacts.map((artifact: ReleaseArtifact) => (
            <div key={artifact.id} className="flex flex-wrap items-center gap-2 mb-2"
              data-artifact-id={artifact.id} data-artifact-state={artifact.state}>
              <Tag size="small" color={artifact.state === 'verified' ? 'green' : artifact.state === 'failed' ? 'red' : 'grey'}>
                {artifact.state === 'verified' ? '已校验' : artifact.state === 'failed' ? '失败' : '待校验'}
              </Tag>
              <Text size="small">
                {artifact.format} · {artifact.sizeBytes} 字节 · 文件指纹 {artifact.artifactHash.slice(0, 16)}…
              </Text>
              {artifact.state === 'verified' && capabilities.canDownload ? (
                // 下载走同源 /api，因此复用统一会话与错误处理（401/403 有中文提示）。
                // 文件名由服务端设置（含类型与版本名，不叫 latest）。
                <a className="console-link" href={studioApi.downloadArtifactURL(scope.projectId, release.id, artifact.id)}>
                  <Download size={13} aria-hidden /> 下载
                </a>
              ) : null}
              {artifact.errorMessage ? (
                <Text type="tertiary" size="small">失败原因：{artifact.errorMessage}</Text>
              ) : null}
            </div>
          ))
        )}
      </Card>

      <Card className="console-card" bodyStyle={{ padding: 14 }} data-manifest-panel="true">
        {grounding ? <div data-grounding-summary="true" className="mb-3">
          <Text strong className="block mb-1">素材依据</Text>
          <Text className="block">引用 {grounding.chunks} 个素材块，缺失 {grounding.missing} 个；素材可追溯样本 {grounding.groundedSamples} 条，缺少完整素材依据 {grounding.ungroundedSamples} 条，其中成品导入 {grounding.externalImports} 条。</Text>
          <Text type="tertiary" size="small">素材关联说明来源；答案质量仍需规则检查、独立评估与人工判断。</Text>
        </div> : <Text type="tertiary" className="block mb-3">此版本未记录素材依据摘要。</Text>}
        <Text strong className="block mb-1">发布清单（manifest）</Text>
        <Text type="tertiary" size="small" className="block mb-2">
          manifest 记录这一版发布了什么：清单项、映射与编码器版本、用途与限制。
          指纹分层：内容指纹 → 清单指纹 → 文件指纹 → manifest 指纹（不含它自己）。
        </Text>
        <pre className="review-content">{JSON.stringify(card.manifest, null, 2)}</pre>
      </Card>
    </div>
  )
}

// ---------------------------------------------------------------------------
// 交付库（B03）
// ---------------------------------------------------------------------------

export function DeliveriesPage() {
  const navigate = useNavigate()
  const { Title, Text } = Typography
  const [items, setItems] = useState<DeliveryItem[]>([])
  const [search, setSearch] = useState('')
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)

  const load = useCallback(async (query: string) => {
    setLoading(true)
    setError(null)
    try {
      const response = await studioApi.listDeliveries(query.trim() === '' ? undefined : { q: query.trim() })
      setItems(response.items ?? [])
    } catch (loadError) {
      setError(loadError instanceof Error ? loadError.message : '加载交付库失败')
    } finally {
      setLoading(false)
    }
  }, [])

  useEffect(() => {
    void load('')
  }, [load])

  return (
    <div className="console-page" data-studio-page="deliveries">
      <div className="console-page__header">
        <div>
          <Title heading={4} className="!mb-1">交付库</Title>
          <Text type="tertiary">
            只显示已发布且你有权访问的版本；候选不是交付物，不会出现在这里。
          </Text>
        </div>
        <div className="flex gap-2">
          <Input value={search} onChange={(value) => setSearch(value)} placeholder="按版本名或用途搜索"
            style={{ width: 220 }} aria-label="搜索交付" />
          <Button icon={<RefreshCw size={14} />} onClick={() => void load(search)}>搜索</Button>
        </div>
      </div>

      {loading ? (
        <div className="flex justify-center py-10"><Spin tip="正在加载交付库" /></div>
      ) : error ? (
        <Card className="console-card">
          <Text strong className="block">加载失败</Text>
          <Text type="tertiary">{error}</Text>
        </Card>
      ) : items.length === 0 ? (
        <Card className="console-card">
          <Empty description="还没有可交付的已发布版本。" />
        </Card>
      ) : (
        <div className="batch-table" data-delivery-table="true">
          <div className="batch-row batch-row--head">
            <span>发布 ID</span><span>版本名</span><span>项目</span><span>用途</span><span>操作</span>
          </div>
          {items.map((item) => (
            <div key={`${item.projectId}-${item.releaseId}`} className="batch-row" data-delivery-id={item.releaseId}>
              <span>{item.releaseId}</span>
              <span>{item.releaseName}</span>
              <span>{item.projectId}</span>
              <span>{item.intendedUse || '（未填写）'}</span>
              <span>
                <Button size="small" onClick={() => navigate(item.page)}>打开数据卡</Button>
              </span>
            </div>
          ))}
        </div>
      )}
    </div>
  )
}

function StatTile({ label, value, hint }: { label: string; value: string; hint: string }) {
  const { Text } = Typography
  return (
    <Card className="console-card" bodyStyle={{ padding: 14 }}>
      <Text type="tertiary" size="small" className="block">{label}</Text>
      <div className="console-stat-value">{value}</div>
      <Text type="tertiary" size="small" className="block">{hint}</Text>
    </Card>
  )
}
