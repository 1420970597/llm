import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { useNavigate, useSearchParams } from 'react-router-dom'
import { Button, Card, Empty, Input, Select, Spin, TextArea, Typography } from '@douyinfe/semi-ui'
import { AlertTriangle, ArrowDown, ArrowUp, CheckCircle2, Copy, FileCog, History, Maximize2, Minus, Plus, Save, Trash2, WandSparkles, XCircle } from 'lucide-react'
import { client } from '../../lib/api'
import { newIdempotencyKey, projectPath } from '../../lib/api/studio'
import { useProjectScope } from '../ProjectLayout'
import { CopyVersionButton, DocumentHistory, DocumentSaveBar, StandardPayloadEditor, useVersionedDocument } from '../DocumentEditors'
export { TargetStructurePage as CoveragePage } from './TargetStructurePage'

/**
 * 设计区页面（Issue #160 T11）：蓝图节点检查器、覆盖矩阵、标准历史。
 *
 * 契约：docs/plans/atelier-implementation.md §5（节点配置规范）、§3.2（URL 参数）。
 *
 * 三条核心设计决定：
 *
 *  1. **检查器由服务端元数据驱动**（`GET P/blueprint-nodes`），不按节点
 *     手写七个表单。字段名、约束、必填、由哪个任务交付都是数据 ——
 *     于是「服务端加了字段而界面没跟上」不可能发生，而「计划中的节点」
 *     可以诚实地只读展示（§5：未接入的 GRPO 节点先只读）。
 *  2. **`?node=` 与 `?version=` 都可分享**（§3.2）：当前节点与所看版本写进
 *     URL，因此「把设计稿发给同事」不会变成「发给对方一个默认页」。
 *  3. **历史版本只读，可复制可 diff**：`?version=` 指向的版本是只读的，
 *     保存总是**新建版本**（§2.2「旧版本保持只读，可读/比较/复制」），
 *     因此界面上不存在「覆盖已有版本」的操作。
 */

type NodeFieldSpec = {
  name: string
  label: string
  kind: string
  required: boolean
  min?: number
  max?: number
  options?: string[]
  help?: string
}

type NodeSpec = {
  key: string
  payloadField: string
  label: string
  caption: string
  availability: string
  task: string
  requiresGeneration: boolean
  fields: NodeFieldSpec[]
  /** 一句人话回答「这个节点会做什么」（issue #197 第 5 条）。 */
  purpose?: string
  /** 该节点按执行顺序列出的小步骤（同样是人话）。 */
  steps?: string[]
}

type BlueprintNodesResponse = {
  items: NodeSpec[]
  nodeKeys: string[]
}

type DocumentVersion = {
  id: number
  documentId: number
  projectId: number
  kind: string
  version: number
  schemaVersion: string
  payload: Record<string, unknown>
  contentHash: string
  changeReason: string
  createdBy?: number
  createdAt: string
}

type DocumentHead = {
  id: number
  projectId: number
  kind: string
  logicalId: string
  currentVersion: number
  /** 文档头的乐观锁 revision，而不是当前版本号。 */
  revision: number
}

type VersionsResponse = {
  items: DocumentVersion[]
  nextCursor?: string
  sortKey: string
  /** 没有保存过版本时服务端省略该字段。 */
  document?: DocumentHead
  canEdit?: boolean
}

type BlueprintChoice = {
  value: string
  label: string
  meta?: string
  version?: number
  /**
   * 该选项不可选（issue #209）。
   *
   * 只用于**配置不完整**的连接：它们在服务端一定不能用（选中后会在批次
   * 已开跑时才报 `model connection unavailable`），因此在下拉里灰显并禁止选择。
   * 「已停用」刻意不在此列：保留可选性，因为已有蓝图可能正引用它，
   * 用户需要能重新指回那条连接（文案已标「已停用」）。
   */
  disabled?: boolean
  /** 配置不完整的具体原因（与服务端 configIssues 同源，供节点健康直接引用）。 */
  configIssues?: string[]
}

type BlueprintChoices = {
  versions: Record<string, BlueprintChoice[]>
  connections: BlueprintChoice[]
}

type CanvasPoint = { x: number; y: number }
const NODE_SIZE = { width: 236, height: 124 }
const INITIAL_POSITIONS: Record<string, CanvasPoint> = {
  coverage: { x: 30, y: 30 }, standard: { x: 30, y: 230 },
  source: { x: 30, y: 410 },
  generation: { x: 350, y: 130 }, evaluation: { x: 670, y: 30 },
  rules: { x: 670, y: 230 }, human_review: { x: 990, y: 130 },
  delivery: { x: 1310, y: 130 },
}
// These are configuration/data dependencies of the existing pipeline, not an
// editable execution DAG. Evaluation and rules both provide review evidence.
const BLUEPRINT_DEPENDENCIES = [
  ['coverage', 'generation'], ['standard', 'generation'],
  ['source', 'generation'],
  ['generation', 'evaluation'], ['generation', 'rules'],
  ['evaluation', 'human_review'], ['rules', 'human_review'],
  ['human_review', 'delivery'],
]

const FIELD_STEPS: Record<string, Array<{ key: string; label: string; fields: string[] }>> = {
  coverage: [{ key: 'scope', label: '规划领域、方向与数量', fields: ['coverageVersionId'] }],
  standard: [
    { key: 'reference', label: '选择思维标准', fields: ['standardVersionId'] },
    { key: 'reasoning', label: '配置思考步骤与检查点', fields: ['steps'] },
  ],
  generation: [
    { key: 'input', label: '素材输入', fields: ['sourceVersionId'] },
    { key: 'model', label: '选择生成模型', fields: ['modelConnectionId', 'modelVersion'] },
    { key: 'output', label: '定义输出结构', fields: ['schemaVersion', 'jsonSchema'] },
    { key: 'limits', label: '配置生成边界', fields: ['maxTokens', 'temperature'] },
    { key: 'dispatch', label: '并发与失败处理', fields: ['concurrency', 'failurePolicy'] },
  ],
  evaluation: [
    { key: 'judges', label: '独立裁判', fields: ['judgeConnectionIds'] },
    { key: 'rubric', label: '评分标准与权重', fields: ['rubricVersionId', 'weights'] },
    { key: 'sampling', label: '抽样与缺分处理', fields: ['samplingSeed', 'missingScorePolicy'] },
  ],
  rules: [{ key: 'policy', label: '规则与风险检查', fields: ['qualityPolicyVersionId'] }],
  human_review: [
    { key: 'assignment', label: '安排人工检查', fields: ['assignment', 'riskScope', 'sampleRate'] },
    { key: 'evidence', label: '确认必需证据', fields: ['requiredEvidence'] },
  ],
  delivery: [
    { key: 'mapping', label: '字段映射与文件格式', fields: ['mappingVersionId', 'format'] },
    { key: 'use', label: '用途与限制', fields: ['intendedUse', 'limitations'] },
  ],
}

function configurableSteps(spec: NodeSpec) {
  const groups = (FIELD_STEPS[spec.key] ?? []).map((step) => ({
    ...step, fields: spec.fields.filter((field) => step.fields.includes(field.name)),
  })).filter((step) => step.fields.length > 0)
  const known = new Set(groups.flatMap((group) => group.fields.map((field) => field.name)))
  const remaining = spec.fields.filter((field) => !known.has(field.name))
  if (remaining.length) groups.push({ key: 'other', label: '其他配置', fields: remaining })
  return groups
}

/** 版本详情端点返回 `{ document, version, references, readOnly }`。 */
function versionFromResponse(body: unknown): DocumentVersion {
  if (body && typeof body === 'object') {
    const record = body as { version?: DocumentVersion; data?: DocumentVersion }
    if (record.version) return record.version
    if (record.data) return record.data
  }
  return body as DocumentVersion
}

/**
 * 模型连接选项的副标题（issue #209）。
 *
 * 为什么把「已停用」与「配置不完整」分开写：它们是**两种不同的阻断原因**，
 * 用户的下一步动作也不同 —— 停用要「启用」，配置不完整要「去连接设置补必填字段」。
 * 混成一句话会让用户按错的提示去操作。
 *
 * `configIssues` 由服务端下发（与保存校验同一份规则），前端**不重算**。
 */
function connectionMeta(item: { model: string; isActive: boolean; configIssues?: string[] }): string {
  const parts: string[] = []
  if (item.model) parts.push(item.model)
  if (!item.isActive) parts.push('已停用')
  if ((item.configIssues?.length ?? 0) > 0) {
    parts.push(`配置不完整，不可用于生成（${item.configIssues?.join('；')}）`)
  }
  return parts.join(' · ')
}

/** 节点 payload 的读写：按 payloadField 取该节点在 Nodes 下的对象。 */
function nodeValues(payload: Record<string, unknown> | null, spec: NodeSpec): Record<string, unknown> {
  if (!payload) return {}
  const nodes = payload.nodes as Record<string, unknown> | undefined
  const value = nodes?.[spec.payloadField]
  if (value && typeof value === 'object' && !Array.isArray(value)) {
    return value as Record<string, unknown>
  }
  return {}
}

/** 把编辑后的节点写回 payload（不可变更新，避免影响历史版本对象）。 */
function withNodeValues(
  payload: Record<string, unknown>,
  spec: NodeSpec,
  values: Record<string, unknown>,
): Record<string, unknown> {
  const nodes = { ...((payload.nodes as Record<string, unknown>) ?? {}) }
  nodes[spec.payloadField] = values
  return { ...payload, nodes }
}

export function BlueprintPage() {
  const scope = useProjectScope()
  const navigate = useNavigate()
  const [searchParams, setSearchParams] = useSearchParams()
  const { Title, Text } = Typography

  const [specs, setSpecs] = useState<NodeSpec[]>([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)
  const [versions, setVersions] = useState<DocumentVersion[]>([])
  const [current, setCurrent] = useState<DocumentVersion | null>(null)
  // 文档头 revision 与版本号是两个不同的概念：保存命令必须携带前者。
  const [headRevision, setHeadRevision] = useState(0)
  const [draft, setDraft] = useState<Record<string, unknown> | null>(null)
  const [saving, setSaving] = useState(false)
  const [saveError, setSaveError] = useState<string | null>(null)
  const [changeReason, setChangeReason] = useState('')
  const [compareVersion, setCompareVersion] = useState<number | null>(null)
  const [canEdit, setCanEdit] = useState(false)
  const [choices, setChoices] = useState<BlueprintChoices>({ versions: {}, connections: [] })
  const [standardVersions, setStandardVersions] = useState<DocumentVersion[]>([])
  const [standardRevision, setStandardRevision] = useState(0)
  const [positions, setPositions] = useState<Record<string, CanvasPoint>>(INITIAL_POSITIONS)
  const [zoom, setZoom] = useState(1)
  const [autoSave, setAutoSave] = useState(true)
  const canvasRef = useRef<HTMLDivElement | null>(null)
  const configurationNavRef = useRef<HTMLElement | null>(null)
  const stepHeadingRef = useRef<HTMLHeadingElement | null>(null)
  const draftRef = useRef(draft)
  draftRef.current = draft
  const savingRef = useRef(false)
  const pendingStandard = useRef<{ fingerprint: string; version: DocumentVersion } | null>(null)
  const [pendingStandardVersion, setPendingStandardVersion] = useState<number | null>(null)
  const dragging = useRef<{ key: string; start: CanvasPoint; origin: CanvasPoint } | null>(null)
  const dragged = useRef(false)
  const bootstrapAttempted = useRef(false)
  const preserveDraftAfterNavigation = useRef(false)
  /**
   * 画布节点的 DOM 句柄（issue #204）。
   *
   * 缺陷形态：`?node=` 是契约 §3.2 里**可分享**的参数，但画布是横向滚动容器
   * （`overflow-x: auto`），深链到靠后的节点时它被裁在可视区外 ——
   * 用户打开了「版本交付」却看不到它，也不知道要横向滚动。《实测评测：4/6 个常见的
   * 深链目标不可见》。因此当前节点必须被**滚入可视区**，而不仅是写入 URL。
   */
  const nodeRefs = useRef<Record<string, HTMLButtonElement | null>>({})

  // `?node=` 与 `?version=` 都来自 URL：分享链接要能指向同一个节点与版本。
  const activeNodeKey = searchParams.get('node') ?? ''
  const viewingVersion = searchParams.get('version')
  const adoptedCoverageId = searchParams.get('coverageVersionId')
  const adoptedStandardId = searchParams.get('standardVersionId')

  const load = useCallback(async (versionOverride?: string | null) => {
    setLoading(true)
    setError(null)
    try {
      const [nodesResponse, versionsResponse, coverageResponse, standardResponse, qualityResponse, mappingResponse, connectionsResponse] = await Promise.all([
        client.get<BlueprintNodesResponse>(`${projectPath(scope.projectId)}/blueprint-nodes`),
        client.get<VersionsResponse>(`${projectPath(scope.projectId)}/blueprint-versions?limit=50`),
        client.get<VersionsResponse>(`${projectPath(scope.projectId)}/coverage-versions?limit=50`),
        client.get<VersionsResponse>(`${projectPath(scope.projectId)}/standard-versions?limit=50`),
        client.get<VersionsResponse>(`${projectPath(scope.projectId)}/quality-policy-versions?limit=50`),
        client.get<VersionsResponse>(`${projectPath(scope.projectId)}/mapping-versions?limit=50`),
        client.get<{ providers?: Array<{ id: number; name: string; model: string; isActive: boolean; configIssues?: string[] }> }>('/v1/settings/connection-options'),
      ])
      setSpecs(nodesResponse.data.items ?? [])
      const list = versionsResponse.data.items ?? []
      const adoptSavedDocuments = (payload: Record<string, unknown>) => {
        let next = payload
        const adopt = (key: string, field: string, id: string | null, response: VersionsResponse) => {
          if (!id || !(response.items ?? []).some((item) => String(item.id) === id)) return
          const spec = nodesResponse.data.items.find((item) => item.key === key)
          if (!spec) return
          const values = { ...nodeValues(next, spec), [field]: Number(id) }
          if (key === 'standard') delete values.steps
          next = withNodeValues(next, spec, values)
        }
        adopt('coverage', 'coverageVersionId', adoptedCoverageId, coverageResponse.data)
        adopt('standard', 'standardVersionId', adoptedStandardId, standardResponse.data)
        return next
      }
      setVersions(list)
      setHeadRevision(versionsResponse.data.document?.revision ?? 0)
      setStandardVersions(standardResponse.data.items ?? [])
      setStandardRevision(standardResponse.data.document?.revision ?? 0)
      const editable = versionsResponse.data.canEdit === true
      setCanEdit(editable)
      const versionChoices = (response: VersionsResponse): BlueprintChoice[] =>
        (response.items ?? []).map((item) => ({
          value: String(item.id),
          label: `v${item.version}${item.changeReason ? ` · ${item.changeReason}` : ''}`,
          meta: item.contentHash ? item.contentHash.slice(0, 8) : undefined,
          version: item.version,
        }))
      const hasSources = nodesResponse.data.items?.some((spec) => spec.fields.some((field) => field.name === 'sourceVersionId'))
      const sourceChoices = hasSources
        ? versionChoices((await client.get<VersionsResponse>(`${projectPath(scope.projectId)}/source-versions?limit=50`)).data)
        : []
      setChoices({
        versions: {
          coverageVersionId: versionChoices(coverageResponse.data),
          standardVersionId: versionChoices(standardResponse.data),
          qualityPolicyVersionId: versionChoices(qualityResponse.data),
          mappingVersionId: versionChoices(mappingResponse.data),
          // 量表版本目前与质量策略共用项目版本目录；服务端保存的仍是明确的版本行 ID。
          rubricVersionId: versionChoices(qualityResponse.data),
          ...(hasSources ? { sourceVersionId: sourceChoices } : {}),
        },
        connections: (connectionsResponse.data.providers ?? []).map((item) => ({
          value: String(item.id),
          // issue #209：名称为空的连接以前渲染成一个**没有任何文字**的选项。
          // 这里给可读兼底 + 把服务端下发的配置问题拼进标签，
          // 用户在**选择前**就能看出哪个不能用，且不可选（灰显）。
          label: item.name || `未命名连接 #${item.id}`,
          meta: connectionMeta(item),
          disabled: (item.configIssues?.length ?? 0) > 0,
          configIssues: item.configIssues ?? [],
        })),
      })

      // Projects created before native document bootstrap may have an empty
      // blueprint catalogue. Repair that state through the real API once,
      // then load the resulting version IDs into the selectors.
      if (list.length === 0 && editable && !bootstrapAttempted.current) {
        bootstrapAttempted.current = true
        try {
          await client.post(`${projectPath(scope.projectId)}/documents/bootstrap`)
          await load(versionOverride)
          return
        } catch {
          // Keep the honest empty state if the project is archived or the
          // server cannot bootstrap; the page remains retryable.
        }
      }

      // 保存后调用 load(null) 时不能依赖仍捕获着旧 URL 的 viewingVersion。
      const requestedVersion = versionOverride === undefined ? viewingVersion : versionOverride

      if (requestedVersion) {
        // 历史版本：按 URL 拉取那一版（只读），并把它作为对比基准。
        const response = await client.get<{ data?: DocumentVersion } & DocumentVersion>(
          `${projectPath(scope.projectId)}/blueprint-versions/${requestedVersion}`,
        )
        const version = versionFromResponse(response.data)
        setCurrent(version)
        setDraft(version.payload)
        setCompareVersion(version.version)
      } else if (list.length > 0) {
        const latest = list[0]
        setVersions(list)
        const response = await client.get<{ data?: DocumentVersion } & DocumentVersion>(
          `${projectPath(scope.projectId)}/blueprint-versions/${latest.version}`,
        )
        const version = versionFromResponse(response.data)
        setCurrent(version)
        setDraft(adoptSavedDocuments(version.payload))
        setCompareVersion(null)
      } else {
        // 还没有任何版本：允许从空蓝图开始（§5 允许保存只填了一部分的草稿）。
        setCurrent(null)
        setDraft({ schemaVersion: 'blueprint.v1', nodes: {} })
      }
    } catch (loadError) {
      setError(loadError instanceof Error ? loadError.message : '加载蓝图失败')
    } finally {
      setLoading(false)
    }
  }, [adoptedCoverageId, adoptedStandardId, scope.projectId, viewingVersion])

  useEffect(() => {
    if (preserveDraftAfterNavigation.current && viewingVersion === null) {
      preserveDraftAfterNavigation.current = false
      return
    }
    void load()
  }, [load, viewingVersion])

  const activeSpec = useMemo(
    () => specs.find((spec) => spec.key === activeNodeKey) ?? specs[0],
    [specs, activeNodeKey],
  )

  /**
   * 把当前节点滚入画布可视区（issue #204）。
   *
   * 为什么需要：画布是 `overflow-x: auto` 的横向流程带，窄屏下只有前几个节点
   * 在可视区内。`?node=` 是契约 §3.2 的**可分享**参数，但以前它只改了 URL，
   * 界面没跟上 —— 用户打开一个指向「版本交付」的链接，看到的却是被截断的左端，
   * 而唯一的横向滚动条在画布底部（首屏往往看不到）。
   *
   * `inline: 'nearest'` 而不是 `'center'`：已经在可视区内的节点不应被强制居中，
   * 否则点击节点会让画布自己跳动（变成本次修复引入的新“不稳定”）。
   */
  useEffect(() => {
    // `loading` 必须在依赖里：加载中页面提前 return <Spin>，节点尚未挂载
    // （refs 为空）；若只在 `activeSpec` 变化时跑，深链场景下 specs 到达后
    // activeSpec 已定型、而节点要等 `loading` 变 false 才挂载 —— 那次不会重跑，
    // 当前节点就永远滚不进来。
    if (loading || !activeSpec) return
    const node = nodeRefs.current[activeSpec.key]
    if (!node) return
    node.scrollIntoView({ behavior: 'auto', block: 'nearest', inline: 'nearest' })
  }, [activeSpec, loading, zoom])

  // URL 中存在 version 就代表「历史查看」；current 此时恰好也是被查看的
  // 历史版本，拿两者比较会把只读状态错误地判成可编辑。
  const isReadOnly = viewingVersion !== null
  const changed = draft !== null && JSON.stringify(draft) !== JSON.stringify(current?.payload ?? { schemaVersion: 'blueprint.v1', nodes: {} })
  const activeSteps = activeSpec ? configurableSteps(activeSpec) : []
  const selectedStep = searchParams.get('step')
  const activeStep = activeSteps.find((step) => step.key === selectedStep) ?? activeSteps[0]
  const configurationSteps = specs.flatMap((spec) => configurableSteps(spec).map((step) => ({ spec, step })))
  const configurationIndex = configurationSteps.findIndex(({ spec, step }) => spec.key === activeSpec?.key && step.key === activeStep?.key)
  useEffect(() => {
    if (loading) return
    const node = configurationNavRef.current?.querySelector<HTMLElement>('[aria-current="step"]')
    if (node) node.scrollIntoView({ behavior: 'auto', block: 'nearest', inline: 'nearest' })
    stepHeadingRef.current?.focus({ preventScroll: true })
    if (window.matchMedia('(max-width: 767px)').matches) stepHeadingRef.current?.scrollIntoView({ block: 'center' })
  }, [activeSpec?.key, activeStep?.key, loading])

  const fitCanvas = useCallback(() => {
    if (!canvasRef.current) return
    setZoom(Math.max(0.25, Math.min(1, (canvasRef.current.clientWidth - 24) / 1580)))
  }, [])

  useEffect(() => {
    if (loading || !canvasRef.current) return
    let previousWidth = 0
    const resize = () => {
      const width = canvasRef.current?.clientWidth ?? 0
      if (width === previousWidth) return
      previousWidth = width
      setZoom(width < 600 ? 1 : 0.75)
    }
    const observer = new ResizeObserver(resize)
    observer.observe(canvasRef.current)
    resize()
    return () => observer.disconnect()
  }, [loading])

  const save = useCallback(async () => {
    if (!changed) return true
    if (!draft || !activeSpec || !canEdit || isReadOnly || savingRef.current) return false
    if (containsInvalidJSONMarker(draft)) {
      setSaveError('请先修正 JSON 格式，再保存蓝图。')
      return false
    }
    // 提交前清理「非法 JSON 中间态」标记：它是编辑器的临时状态，
    // 不能进入 payload（那会让服务端看到一个不认识的字段）。
    const submittedDraft = draft
    let cleaned = stripInvalidJSONMarkers(draft)
    const changedLabels = specs.filter((spec) => JSON.stringify(nodeValues(current?.payload ?? null, spec)) !== JSON.stringify(nodeValues(cleaned, spec))).map((spec) => spec.label)
    const reason = changeReason.trim() || `调整${changedLabels.join('、') || '蓝图配置'}`
    savingRef.current = true
    setSaving(true)
    setSaveError(null)
    try {
      const standardSpec = specs.find((spec) => spec.key === 'standard')
      const standard = standardSpec ? nodeValues(cleaned, standardSpec) : {}
      if (standardSpec && Array.isArray(standard.steps)) {
        const referenced = standardVersions.find((version) => version.id === Number(standard.standardVersionId))
        const steps: Array<Record<string, unknown>> = (standard.steps as Array<Record<string, unknown>>).map((step, index) => ({ ...step, order: index + 1 }))
        if (steps.length === 0 || steps.some((step) => !String(step.title ?? '').trim() || !String(step.checkpoint ?? '').trim())) {
          throw new Error('思考步骤至少保留一步，每步都需要填写「做什么」和「完成检查点」。草稿已保留。')
        }
        const referencedSteps = Array.isArray(referenced?.payload?.steps)
          ? (referenced.payload.steps as Array<Record<string, unknown>>).map((step, index) => ({ ...step, order: index + 1 })) : []
        if (JSON.stringify(steps) !== JSON.stringify(referencedSteps)) {
          const fingerprint = JSON.stringify(steps)
          let savedStandard = pendingStandard.current?.fingerprint === fingerprint ? pendingStandard.current.version : null
          if (!savedStandard) {
            const standardResult = await client.post<{ revision: number; data: { version: DocumentVersion } }>(
              `${projectPath(scope.projectId)}/standard-versions`,
              { expectedRevision: standardRevision, logicalId: 'main', changeReason: reason,
                payload: { ...(referenced?.payload ?? {}), schemaVersion: 'standard.v1', steps } },
              { headers: { 'Idempotency-Key': newIdempotencyKey() } },
            )
            savedStandard = standardResult.data.data.version
            setStandardRevision(standardResult.data.revision)
            setStandardVersions((items) => [savedStandard!, ...items])
            pendingStandard.current = { fingerprint, version: savedStandard }
            setPendingStandardVersion(savedStandard.version)
          }
          cleaned = withNodeValues(cleaned, standardSpec, { ...standard, standardVersionId: savedStandard.id, steps })
        } else {
          cleaned = withNodeValues(cleaned, standardSpec, { ...standard, steps })
        }
      }
      // Editing a form can remove derived step order fields. Restore the
      // canonical shape before deciding whether a real version is needed.
      if (JSON.stringify(cleaned) === JSON.stringify(current?.payload)) {
        if (draftRef.current === submittedDraft) setDraft(cleaned)
        setAutoSave(true)
        return true
      }
      const response = await client.post<{ revision: number; data: { version: DocumentVersion } }>(
        `${projectPath(scope.projectId)}/blueprint-versions`,
        {
          // expectedRevision 用**当前头记录**的 revision：不匹配返回 409 并保留草稿
          //（契约 §1.4「不匹配返回 409，并保留用户草稿」）。
          expectedRevision: headRevision,
          logicalId: 'main',
          changeReason: reason,
          payload: cleaned,
        },
        { headers: { 'Idempotency-Key': newIdempotencyKey() } },
      )
      const saved = response.data.data.version
      setAutoSave(true)
      setHeadRevision(response.data.revision)
      setCurrent(saved)
      setVersions((items) => [saved, ...items])
      if (draftRef.current === submittedDraft) {
        setDraft(saved.payload)
      } else if (standardSpec && draftRef.current) {
        // Preserve edits made while the request was in flight while adopting
        // the standard version created by this request. Otherwise a second
        // autosave of unrelated fields would duplicate that standard version.
        const latest = draftRef.current
        const latestStandard = nodeValues(latest, standardSpec)
        const submittedStandard = nodeValues(submittedDraft, standardSpec)
        if (latestStandard.standardVersionId === submittedStandard.standardVersionId) {
          const savedStandard = nodeValues(saved.payload, standardSpec)
          const steps = JSON.stringify(latestStandard.steps) === JSON.stringify(submittedStandard.steps)
            ? savedStandard.steps : latestStandard.steps
          setDraft(withNodeValues(latest, standardSpec, { ...latestStandard, standardVersionId: savedStandard.standardVersionId, ...(steps ? { steps } : {}) }))
        }
      }
      pendingStandard.current = null
      setPendingStandardVersion(null)
      setChangeReason('')
      // 保存后**留在当前节点**（T11 验收项），只刷新版本列表与 current。
      setSearchParams((params) => {
        params.delete('version')
        if (params.has('coverageVersionId') || params.has('standardVersionId')) preserveDraftAfterNavigation.current = true
        params.delete('coverageVersionId')
        params.delete('standardVersionId')
        return params
      })
      return true
    } catch (saveErrorValue) {
      setAutoSave(false)
      const apiError = saveErrorValue as { statusCode?: number; message?: string }
      if (apiError.statusCode === 409) {
        setSaveError('版本已被其他人修改（乐观锁冲突）。你的草稿已保留，请刷新后比较差异再保存。')
      } else {
        setSaveError(apiError.message ?? '保存失败')
      }
      return false
    } finally {
      savingRef.current = false
      setSaving(false)
    }
  }, [activeSpec, canEdit, changed, changeReason, current, draft, headRevision, isReadOnly, scope.projectId, setSearchParams, specs, standardRevision, standardVersions])

  useEffect(() => {
    if (loading || saving || !autoSave || !changed || isReadOnly || !canEdit || containsInvalidJSONMarker(draft)) return
    const timer = window.setTimeout(() => void save(), 1200)
    return () => window.clearTimeout(timer)
  }, [autoSave, canEdit, changed, draft, isReadOnly, loading, save, saving])

  if (loading) {
    return (
      <div className="flex justify-center py-10">
        <Spin tip="正在加载蓝图" />
      </div>
    )
  }

  if (error) {
    return (
      <Card className="console-card">
        <Text strong className="block">
          蓝图加载失败
        </Text>
        <Text type="tertiary">{error}</Text>
        <div className="mt-3">
          <Button size="small" onClick={() => void load()}>
            重试
          </Button>
        </div>
      </Card>
    )
  }

  const nodeValuesForActive = activeSpec ? nodeValues(draft, activeSpec) : {}
  const nodeHealth = activeSpec ? getNodeHealth(activeSpec, nodeValuesForActive, choices) : null
  const hasInvalidJSON = draft ? containsInvalidJSONMarker(draft) : false
  const dirty = draft !== null && (current === null || JSON.stringify(draft) !== JSON.stringify(current.payload))
  const generationSpec = specs.find((spec) => spec.key === 'generation')
  const sourceVersionId = generationSpec ? nodeValues(draft, generationSpec).sourceVersionId : undefined
  const relatedPage = activeSpec?.key === 'coverage'
    ? { route: 'project.coverage', label: '覆盖矩阵' }
    : activeSpec?.key === 'standard'
      ? { route: 'project.standard', label: '思维标准' }
      : activeSpec?.key === 'generation' && activeStep?.key === 'input'
        ? { route: 'project.sources', label: '素材来源' }
      : activeSpec?.key === 'generation' || activeSpec?.key === 'evaluation'
        ? { route: 'settings.connections', label: '模型连接' }
      : activeSpec?.key === 'rules'
        ? { route: 'project.rules', label: '规则策略' }
        : activeSpec?.key === 'delivery'
          ? { route: 'project.newRelease', label: '交付映射' }
        : null
  const relatedVersion = activeSpec
    ? choices.versions[(activeStep?.fields ?? activeSpec.fields).find((field) => field.kind === 'id')?.name ?? '']?.find(
      (option) => String(nodeValuesForActive[(activeStep?.fields ?? activeSpec.fields).find((field) => field.kind === 'id')?.name ?? '']) === option.value,
    )
    : undefined
  const selectedConnections = activeSpec?.key === 'generation' && activeStep?.key !== 'input'
    ? choices.connections.filter((option) => option.value === String(nodeValuesForActive.modelConnectionId))
    : activeSpec?.key === 'evaluation'
      ? choices.connections.filter((option) => Array.isArray(nodeValuesForActive.judgeConnectionIds) && nodeValuesForActive.judgeConnectionIds.map(String).includes(option.value))
      : []
  const selectConfigurationStep = (index: number) => {
    const target = configurationSteps[index]
    if (!target) return
    setSearchParams((params) => { params.set('node', target.spec.key); params.set('step', target.step.key); return params })
  }
  const continueConfiguration = async () => {
    if (dirty && !(await save())) return
    if (configurationIndex + 1 < configurationSteps.length) selectConfigurationStep(configurationIndex + 1)
    else navigate(scope.href('project.pilot'))
  }
  const advancedNames = new Set(['maxTokens', 'temperature', 'concurrency', 'failurePolicy', 'samplingSeed', 'missingScorePolicy'])
  const visibleFields = activeStep?.fields ?? activeSpec?.fields ?? []
  const updateNodeField = (name: string, value: unknown) => {
    if (!draft || !activeSpec) return
    const nextValues = { ...nodeValuesForActive, [name]: value }
    if (activeSpec.key === 'standard' && name === 'standardVersionId') delete nextValues.steps
    const nextDraft = withNodeValues(draft, activeSpec, nextValues)
    setDraft(nextDraft)
    if (JSON.stringify(nextDraft) === JSON.stringify(current?.payload)) setAutoSave(true)
    setSaveError(null)
  }

  return (
    <div className="console-page blueprint-page blueprint-configuration-page" data-studio-page="blueprint">
      {isReadOnly ? (
        <div className="blueprint-readonly-banner" data-readonly="true">
          <History size={14} aria-hidden />
          <Text size="small">
            正在查看历史版本 v{compareVersion}（只读）。复制它或直接保存会产生新版本，
            旧版本永不覆盖。
          </Text>
        </div>
      ) : null}

      <header className="atelier-page-intro blueprint-page-intro">
        <div>
          <h1>配置生产方案</h1>
          <Text type="tertiary">步骤 {Math.max(configurationIndex + 1, 1)} / {configurationSteps.length} · {saving ? '保存中' : dirty ? '有未保存修改' : `方案 v${current?.version ?? '—'}`}</Text>
        </div>
      </header>
      <div className="blueprint-layout blueprint-configuration-layout">
        <nav className="blueprint-configuration-steps" aria-label="生产方案配置步骤" ref={configurationNavRef}>
          {configurationSteps.map(({ spec, step }, index) => {
            const health = getNodeHealth(spec, nodeValues(draft, spec), choices)
            return <button key={`${spec.key}:${step.key}`} type="button" aria-current={index === configurationIndex ? 'step' : undefined} data-configuration-step={`${spec.key}:${step.key}`} onClick={() => selectConfigurationStep(index)}>
              <span>{index + 1}</span><strong>{step.label}</strong><small>{spec.label} · {health.label}</small>
            </button>
          })}
        </nav>
        <details className="blueprint-dependency-diagram">
          <summary>查看配置依赖图</summary>
        <section className="blueprint-canvas" aria-label="生产流程画布">
          <div className="blueprint-canvas__eyebrow">ATELIER / PRODUCTION BLUEPRINT / v{current?.version ?? '—'}</div>
          <div className="blueprint-canvas__tools" role="toolbar" aria-label="画布工具">
            <Button size="small" icon={<Minus size={14} />} aria-label="缩小画布" onClick={() => setZoom((value) => Math.max(0.25, value - 0.1))} />
            <span aria-live="polite">{Math.round(zoom * 100)}%</span>
            <Button size="small" icon={<Plus size={14} />} aria-label="放大画布" onClick={() => setZoom((value) => Math.min(1.5, value + 0.1))} />
            <Button size="small" icon={<Maximize2 size={14} />} onClick={fitCanvas}>适应画布</Button>
            <Button size="small" theme="borderless" onClick={() => setPositions(INITIAL_POSITIONS)}>重置布局</Button>
          </div>
          <div className="blueprint-canvas__hint">
            先定范围与标准，再生成内容；独立评估与规则检查提供不同证据。可横向滚动或适应画布查看全部流程，拖动节点整理布局，Alt + 方向键也可移动。
          </div>
          <div className="blueprint-canvas__viewport" ref={canvasRef}>
          <div className="blueprint-canvas__extent" style={{ width: 1580 * zoom, height: 490 * zoom }}>
          <div className="blueprint-nodes" role="list" style={{ width: 1580, height: 490, transform: `scale(${zoom})` }}>
          <svg className="blueprint-edges" width="1580" height="490" aria-label="配置与证据依赖">
            <defs><marker id="blueprint-arrow" viewBox="0 0 10 10" refX="9" refY="5" markerWidth="7" markerHeight="7" orient="auto"><path d="M 0 0 L 10 5 L 0 10 z" /></marker></defs>
            {BLUEPRINT_DEPENDENCIES.map(([from, to]) => {
              if (from === 'source' && !choices.versions.sourceVersionId) return null
              const a = positions[from]; const b = positions[to]
              if (!a || !b) return null
              const start = { x: a.x + NODE_SIZE.width, y: a.y + NODE_SIZE.height / 2 }
              const end = { x: b.x, y: b.y + NODE_SIZE.height / 2 }
              return <path key={`${from}-${to}`} data-dependency={`${from}:${to}`} d={`M${start.x},${start.y} C${start.x + 45},${start.y} ${end.x - 45},${end.y} ${end.x},${end.y}`} markerEnd="url(#blueprint-arrow)" />
            })}
          </svg>
          {choices.versions.sourceVersionId ? <div className="blueprint-source-input" style={{ left: INITIAL_POSITIONS.source.x, top: INITIAL_POSITIONS.source.y }}>
            <strong>素材来源</strong>
            <span>{choices.versions.sourceVersionId.find((choice) => choice.value === String(sourceVersionId))?.label ?? '尚未选择素材来源版本'}</span>
            <Button size="small" onClick={() => navigate(scope.href('project.sources'))}>管理素材</Button>
          </div> : null}
          {specs.map((spec) => (
            <div key={spec.key} className="blueprint-node-group" style={{ left: positions[spec.key]?.x ?? 30, top: positions[spec.key]?.y ?? 30 }}>
            <button
              key={spec.key}
              type="button"
              data-node-key={spec.key}
              className={
                spec.key === activeSpec?.key ? 'blueprint-node blueprint-node--active' : 'blueprint-node'
              }
              aria-current={spec.key === activeSpec?.key ? 'true' : undefined}
              ref={(element) => { nodeRefs.current[spec.key] = element }}
              draggable={!isReadOnly}
              onDragStart={(event) => event.preventDefault()}
              onPointerDown={(event) => {
                if (isReadOnly || event.button !== 0) return
                dragging.current = { key: spec.key, start: { x: event.clientX, y: event.clientY }, origin: positions[spec.key] ?? { x: 30, y: 30 } }
                dragged.current = false
                event.currentTarget.setPointerCapture(event.pointerId)
              }}
              onPointerMove={(event) => {
                const drag = dragging.current
                if (!drag || drag.key !== spec.key) return
                const dx = (event.clientX - drag.start.x) / zoom
                const dy = (event.clientY - drag.start.y) / zoom
                if (Math.abs(dx) + Math.abs(dy) < 5) return
                dragged.current = true
                setPositions((previous) => ({ ...previous, [spec.key]: { x: Math.max(0, Math.min(1340, drag.origin.x + dx)), y: Math.max(0, Math.min(260, drag.origin.y + dy)) } }))
              }}
              onPointerUp={() => { dragging.current = null }}
              onPointerCancel={() => { dragging.current = null }}
              onKeyDown={(event) => {
                if (!event.altKey || isReadOnly || !['ArrowLeft', 'ArrowRight', 'ArrowUp', 'ArrowDown'].includes(event.key)) return
                event.preventDefault()
                const delta = { x: event.key === 'ArrowLeft' ? -20 : event.key === 'ArrowRight' ? 20 : 0, y: event.key === 'ArrowUp' ? -20 : event.key === 'ArrowDown' ? 20 : 0 }
                setPositions((previous) => ({ ...previous, [spec.key]: { x: Math.max(0, Math.min(1340, (previous[spec.key]?.x ?? 30) + delta.x)), y: Math.max(0, Math.min(260, (previous[spec.key]?.y ?? 30) + delta.y)) } }))
              }}
              onClick={() => {
                if (dragged.current) { dragged.current = false; return }
                setSearchParams((params) => {
                  params.set('node', spec.key)
                  params.delete('step')
                  return params
                })
              }}
            >
              <span className={`blueprint-node__icon blueprint-node__icon--${getNodeHealth(spec, nodeValues(draft, spec), choices).state}`} aria-hidden>
                {getNodeHealth(spec, nodeValues(draft, spec), choices).state === 'ready' ? <CheckCircle2 size={16} /> : getNodeHealth(spec, nodeValues(draft, spec), choices).state === 'blocked' ? <XCircle size={16} /> : <FileCog size={16} />}
              </span>
              <span className="blueprint-node__copy"><span className="blueprint-node__label">{spec.label}</span><span className="blueprint-node__caption">{spec.caption}</span></span>
              <span className={`blueprint-node__status blueprint-node__status--${getNodeHealth(spec, nodeValues(draft, spec), choices).state}`}>
                {getNodeHealth(spec, nodeValues(draft, spec), choices).label}
              </span>
            </button>
            {spec.key === activeSpec?.key ? <div className="blueprint-substeps" aria-label={`${spec.label}配置步骤`}>
              {configurableSteps(spec).map((step) => <button key={step.key} type="button" aria-current={activeStep?.key === step.key ? 'step' : undefined} onClick={() => setSearchParams((params) => { params.set('node', spec.key); params.set('step', step.key); return params })}>{step.label}</button>)}
            </div> : null}
            </div>
          ))}
          </div>
          </div>
          </div>
        </section>
        </details>

        <section className="blueprint-inspector" aria-label="步骤检查器">
          {activeSpec ? (
            <>
              <Title heading={5} className="!mb-1">
                {activeSpec.label}
              </Title>

              {/*
                issue #197 第 5 条：术语（量表版本 / 维度权重 / 缺分策略 /
                抽样编号）本身没错，错的是它们**同屏出现且没有一句上下文**。
                这里在字段之前先给出「它会做什么」与「执行顺序」，让下面的
                参数有地方挂靠。
              */}
              {activeSpec.purpose ? (
                <details className="blueprint-node-purpose" data-node-purpose="true">
                  <summary>步骤说明</summary>
                  <Text strong size="small" className="block mb-1">
                    这个节点会做什么
                  </Text>
                  <Text size="small" className="block">
                    {activeSpec.purpose}
                  </Text>
                </details>
              ) : null}

              {activeSpec.steps && activeSpec.steps.length > 0 ? (
                <details className="blueprint-node-steps" data-node-steps="true">
                  <summary className="blueprint-node-steps__summary">
                    <Text strong size="small">
                      执行顺序（{activeSpec.steps.length} 步）
                    </Text>
                  </summary>
                  <ol className="blueprint-node-steps__list">
                    {activeSpec.steps.map((step) => (
                      <li key={step}>{step}</li>
                    ))}
                  </ol>
                </details>
              ) : null}

              {nodeHealth ? (
                <div className={`blueprint-health blueprint-health--${nodeHealth.state}`} role="status">
                  {nodeHealth.state === 'ready' ? <CheckCircle2 size={15} aria-hidden /> : nodeHealth.state === 'blocked' ? <XCircle size={15} aria-hidden /> : <FileCog size={15} aria-hidden />}
                  <div>
                    <strong>{nodeHealth.label}</strong>
                    <span>{nodeHealth.detail}</span>
                  </div>
                </div>
              ) : null}

              {activeSpec.availability === 'planned' ? (
                <Card className="console-card mb-3" bodyStyle={{ padding: 16 }}>
                  <div className="flex items-start gap-2">
                    <AlertTriangle size={16} className="mt-1 text-amber-500" aria-hidden />
                    <div>
                      <Text strong className="block">
                        该节点尚未接入
                      </Text>
                      <Text type="tertiary" size="small">
                        由 {activeSpec.task} 交付。现在只显示能力状态，不提供可保存的配置 ——
                        以免把「功能未做」显示成「已配置」。
                      </Text>
                    </div>
                  </div>
                </Card>
              ) : (
                <>
                <h3 className="blueprint-step-title" tabIndex={-1} ref={stepHeadingRef}>{activeStep?.label}</h3>
                <NodeFields
                  spec={{ ...activeSpec, fields: visibleFields.filter((field) => !advancedNames.has(field.name)) }}
                  values={activeSpec.key === 'standard' && !Array.isArray(nodeValuesForActive.steps)
                    ? { ...nodeValuesForActive, steps: standardVersions.find((version) => version.id === Number(nodeValuesForActive.standardVersionId))?.payload?.steps ?? [] }
                    : nodeValuesForActive}
                  disabled={isReadOnly || !canEdit}
                  choices={choices}
                  onChange={updateNodeField}
                />
                {visibleFields.some((field) => advancedNames.has(field.name)) ? <details className="blueprint-advanced-settings" open={visibleFields.every((field) => advancedNames.has(field.name))}>
                  <summary>高级参数</summary>
                  <NodeFields spec={{ ...activeSpec, fields: visibleFields.filter((field) => advancedNames.has(field.name)) }} values={nodeValuesForActive} disabled={isReadOnly || !canEdit} choices={choices} onChange={updateNodeField} />
                </details> : null}
                </>
              )}

              {relatedPage ? (
                <div className="blueprint-related-config">
                  <div>
                    <strong>{selectedConnections.length > 0
                      ? `当前使用：${selectedConnections.map((connection) => connection.label).join('、')}`
                      : relatedVersion ? `当前引用配置 ${relatedVersion.label}` : '尚未选择配置'}</strong>
                    <span>{selectedConnections.length > 0
                      ? '连接的密钥不会显示在蓝图中；如需新增或启用连接，请到连接设置处理。'
                      : relatedVersion?.meta ? `内容指纹 ${relatedVersion.meta}` : '可先完成配置，再回到此处选择要固定的版本。'}</span>
                  </div>
                  <Button size="small" icon={<FileCog size={14} />} onClick={() => navigate(relatedPage.route === 'settings.connections'
                    ? '/settings/connections'
                    : scope.href(relatedPage.route))}>
                    {relatedPage.route === 'settings.connections' ? '查看模型连接' : `编辑${relatedPage.label}`}
                  </Button>
                </div>
              ) : null}

              <details className="blueprint-change-details mt-4">
                <summary>变更说明（选填）</summary>
                <Text type="tertiary" size="small" className="block mb-1">
                  变更说明（可选，留空时自动记录修改的步骤）
                </Text>
                <TextArea
                  value={changeReason}
                  disabled={!canEdit}
                  onChange={(value) => setChangeReason(value)}
                  placeholder="例如：把并发从 8 提到 12"
                  autosize={{ minRows: 2, maxRows: 4 }}
                  data-field="blueprint-change-reason"
                />
              </details>

              {saveError ? (
                <div className="wizard-field__error mt-2" role="alert">
                  {saveError}
                  <Button size="small" onClick={async () => {
                    try {
                      const [blueprintHead, standardHead] = await Promise.all([
                        client.get<VersionsResponse>(`${projectPath(scope.projectId)}/blueprint-versions?limit=1`),
                        client.get<VersionsResponse>(`${projectPath(scope.projectId)}/standard-versions?limit=1`),
                      ])
                      setHeadRevision(blueprintHead.data.document?.revision ?? 0)
                      setStandardRevision(standardHead.data.document?.revision ?? 0)
                      setSaveError('已重读最新版本。你的草稿仍保留，请确认修改后点击保存重试。')
                    } catch { setSaveError('读取最新版本失败，草稿已保留。请恢复连接后重试。') }
                  }}>重读版本并保留草稿</Button>
                </div>
              ) : null}
              {pendingStandardVersion !== null ? <div role="status" className="blueprint-save-notice">思维标准 v{pendingStandardVersion} 已保存；蓝图关联保存中。若保存失败，重试会继续关联这一版本。</div> : null}

              {hasInvalidJSON ? (
                <div className="blueprint-validation-error" role="alert">
                  <XCircle size={14} aria-hidden /> JSON 结构还没有完成，修正后才能保存。
                </div>
              ) : null}

              <div className="blueprint-save-bar blueprint-configuration-actions mt-3">
                <span className={dirty ? 'blueprint-dirty' : 'blueprint-clean'} role="status">{saving ? '正在保存新版本' : dirty ? autoSave ? '等待自动保存' : '草稿保留，请重试保存' : '已保存'}</span>
                <Button
                  theme="light"
                  type="primary"
                  icon={<Save size={14} />}
                  loading={saving}
                  disabled={isReadOnly || !canEdit || hasInvalidJSON || !dirty}
                  onClick={() => void save()}
                >
                  保存为新版本
                </Button>
                <Button disabled={configurationIndex <= 0 || saving} onClick={() => selectConfigurationStep(configurationIndex - 1)}>上一步</Button>
                <Button theme="solid" type="primary" loading={saving} disabled={saving || isReadOnly || !canEdit || hasInvalidJSON} onClick={() => void continueConfiguration()}>{configurationIndex + 1 < configurationSteps.length ? '保存并继续' : '保存并进入试制'}</Button>
                {current && canEdit ? (
                  <Button
                    icon={<Copy size={14} />}
                    onClick={() => {
                      // Copy immutable history into the current editable draft.
                      preserveDraftAfterNavigation.current = viewingVersion !== null
                      setDraft(current.payload)
                      setSearchParams((params) => {
                        params.delete('version')
                        return params
                      })
                    }}
                  >
                    复制此版本
                  </Button>
                ) : null}
              </div>
            </>
          ) : (
            <Empty description="没有可用的节点定义" />
          )}
        </section>

        {/*
          issue #197 第 4、6 条：版本历史以前是**常驻**的整行区块，把一个
          单节点配置页撑到 1561px（1.56 屏），而且用户一进来就被要求理解
          「历史版本」与「强制关联」。
          现在：
            - 默认折叠（`details`，键盘可用、不需要额外的 JS 状态）；
            - 标题直接说明「改配置不需要先理解版本」；
            - 展开后仍是只读浏览（编辑走「复制当前版本为草稿」，已有入口）。
        */}
        <details className="blueprint-history" aria-label="版本历史（只读，可折叠）">
          <summary className="blueprint-history__summary">
            <Text strong size="small">
              版本历史（只读）· 共 {versions.length} 版
            </Text>
            <Text type="tertiary" size="small">
              改配置不需要先理解版本：直接编辑并保存就是新版本，历史只用于回看与排查。
            </Text>
          </summary>
          <div className="blueprint-history__body">
          {versions.length === 0 ? (
            <Text type="tertiary" size="small">
              还没有保存过任何版本。
            </Text>
          ) : (
            <ul className="blueprint-history__list">
              {versions.map((version) => (
                <li key={version.id}>
                  <button
                    type="button"
                    className={
                      version.version === current?.version
                        ? 'blueprint-history__item blueprint-history__item--active'
                        : 'blueprint-history__item'
                    }
                    onClick={() => {
                      setSearchParams((params) => {
                        params.set('version', String(version.version))
                        return params
                      })
                    }}
                  >
                    <span>v{version.version}</span>
                    <span className="blueprint-history__hash">指纹 {version.contentHash.slice(0, 8)}</span>
                    <span className="blueprint-history__reason">{version.changeReason || '（未填写理由）'}</span>
                  </button>
                </li>
              ))}
            </ul>
          )}
          {relatedPage ? (
            <div className="mt-3">
              <Button size="small" onClick={() => navigate(scope.href(relatedPage.route))}>
                查看{relatedPage.label}
              </Button>
            </div>
          ) : null}
          {current ? (
            <Text type="tertiary" size="small" className="block mt-3">
              当前版本内容指纹：{current.contentHash}
            </Text>
          ) : null}
          </div>
        </details>
      </div>
    </div>
  )
}

/**
 * NodeFields 按元数据渲染检查器。
 *
 * 所有字段都提供真实编辑控件。引用字段从服务端候选目录选择，权重与列表
 * 使用可增删行编辑器；保存仍由服务端做最终类型与依赖校验。
 */
function NodeFields({
  spec,
  values,
  disabled,
  choices,
  onChange,
}: {
  spec: NodeSpec
  values: Record<string, unknown>
  disabled: boolean
  choices: BlueprintChoices
  onChange: (name: string, value: unknown) => void
}) {
  const { Text } = Typography
  return (
    <div className="wizard-fields">
      {spec.fields.map((field) => {
        const value = values[field.name]
        const display = value === undefined || value === null ? '' : String(value)
        // `json` 从「只读展示」改为可编辑（T23 的真实缺口）：
        // GRPO 的档位配置就走生成节点的 jsonSchema 字段，把它设为只读会让
        // 「GRPO 需要至少两档」这件事在界面上根本无法满足 ——
        // 而服务端会因缺档拒绝执行，用户却找不到填写的地方。
        const isJSONField = field.kind === 'json'
        const isConnectionField = field.name === 'modelConnectionId' || field.name === 'judgeConnectionIds'
        const options = isConnectionField ? choices.connections : (choices.versions[field.name] ?? [])
        const selectValue = field.kind === 'idList'
          ? (Array.isArray(value) ? value.map(String) : [])
          : field.kind === 'id'
            ? Number(value) > 0 ? String(value) : undefined
            : value === undefined || value === null || value === '' ? undefined : String(value)
        return (
          <div
            key={field.name}
            className="wizard-field"
            data-field={`blueprint-${spec.key}-${field.name}`}
            data-kind={field.kind}
            data-required={field.required ? 'true' : undefined}
          >
            <label className="wizard-field__label" htmlFor={`blueprint-${spec.key}-${field.name}`}>
              {field.label}
              {field.required ? <span className="wizard-field__required"> *</span> : null}
            </label>
            {isJSONField ? (
              field.name === 'steps' ? (
                /* issue #197 第 2 条：思考步骤是**给模型看的中文提示词模板**，
                   而 JSON 只是存储表示。让用户在蓝图里手写
                   `{"id":…,"title":…,"checkpoint":…}` 等于把序列化格式当界面 ——
                   而同一个实体在「思维标准」页早就有自然语言分步编辑器。
                   这里复用同一套分步表单（默认表单视图，JSON 作为可切换视图），
                   保证「同一实体只有一套编辑体验」。 */
                <StandardStepsEditor
                  id={`blueprint-${spec.key}-${field.name}`}
                  value={value}
                  disabled={disabled}
                  onChange={(next) => onChange(field.name, next)}
                />
              ) : (
                <JSONFieldEditor
                  id={`blueprint-${spec.key}-${field.name}`}
                  value={value}
                  disabled={disabled}
                  fieldName={field.name}
                  onChange={(next) => onChange(field.name, next)}
                />
              )
            ) : field.kind === 'id' || field.kind === 'idList' ? (
              <Select
                id={`blueprint-${spec.key}-${field.name}`}
                className="blueprint-select"
                multiple={field.kind === 'idList'}
                value={selectValue as string | string[] | undefined}
                placeholder={options.length > 0 ? '选择已保存版本' : '暂无可选版本'}
                disabled={disabled}
                optionList={options.map((option) => ({ value: option.value, label: option.label, extra: option.meta, disabled: option.disabled }))}
                onChange={(next) => {
                  if (field.kind === 'idList') {
                    onChange(field.name, Array.isArray(next) ? next.map(Number) : [])
                  } else {
                    onChange(field.name, next === undefined || next === '' ? undefined : Number(next))
                  }
                }}
              />
            ) : field.kind === 'ratioMap' ? (
              <RatioMapEditor value={value} disabled={disabled} onChange={(next) => onChange(field.name, next)} />
            ) : field.kind === 'stringList' ? (
              <StringListEditor value={value} disabled={disabled} onChange={(next) => onChange(field.name, next)} />
            ) : field.kind === 'enum' ? (
              <Select
                id={`blueprint-${spec.key}-${field.name}`}
                className="blueprint-select"
                value={selectValue as string | undefined}
                placeholder="请选择"
                disabled={disabled}
                optionList={(field.options ?? []).map((option) => ({ value: option, label: enumLabel(field.name, option) }))}
                onChange={(next) => onChange(field.name, next === undefined || next === '' ? undefined : String(next))}
              />
            ) : (
              <input
                id={`blueprint-${spec.key}-${field.name}`}
                className="blueprint-input"
                type={field.kind === 'int' || field.kind === 'float' || field.kind === 'ratio' ? 'number' : 'text'}
                min={field.min}
                max={field.max}
                value={display}
                disabled={disabled}
                onChange={(event) => {
                  const raw = event.target.value
                  if (field.kind === 'int') {
                    onChange(field.name, raw === '' ? undefined : Number.parseInt(raw, 10))
                    return
                  }
                  if (field.kind === 'float' || field.kind === 'ratio') {
                    onChange(field.name, raw === '' ? undefined : Number(raw))
                    return
                  }
                  onChange(field.name, raw)
                }}
              />
            )}
            {field.help ? (
              <Text type="tertiary" size="small" className="block mt-1">
                {field.help}
              </Text>
            ) : null}
          </div>
        )
      })}
    </div>
  )
}

const ENUM_LABELS: Record<string, Record<string, string>> = {
  schemaVersion: { 'sft.sample.v1': '指令微调样本（SFT）', 'grpo.sample.v1': '偏好评估样本（GRPO）' },
  failurePolicy: { retry_then_skip: '重试后跳过', stop_batch: '停止本批次' },
  missingScorePolicy: { exclude: '排除缺分项', fail_experiment: '实验标记失败' },
  assignment: { 'risk-based': '按风险分派', all: '全部人工检查', sampled: '按比例抽检' },
  format: { jsonl: 'JSONL', csv: 'CSV', json: 'JSON' },
}

function enumLabel(fieldName: string, option: string): string {
  return ENUM_LABELS[fieldName]?.[option] ?? option
}

function hasValue(value: unknown): boolean {
  if (value === undefined || value === null) return false
  if (typeof value === 'string') return value.trim() !== ''
  if (Array.isArray(value)) return value.length > 0
  if (typeof value === 'object') return Object.keys(value as Record<string, unknown>).length > 0
  return true
}

function hasRequiredFieldValue(field: NodeFieldSpec, value: unknown): boolean {
  if (field.kind === 'id') return Number(value) > 0
  if (field.kind === 'idList') return Array.isArray(value) && value.some((item) => Number(item) > 0)
  return hasValue(value)
}

type NodeHealth = {
  state: 'ready' | 'incomplete' | 'blocked' | 'planned'
  label: string
  detail: string
  missing: string[]
}

function getNodeHealth(spec: NodeSpec, values: Record<string, unknown>, choices: BlueprintChoices): NodeHealth {
  if (spec.availability === 'planned') {
    return { state: 'planned', label: '待交付', detail: `该步骤由 ${spec.task} 负责接入，当前不能保存执行配置。`, missing: [] }
  }
  const missing = spec.fields.filter((field) => field.required && !hasRequiredFieldValue(field, values[field.name])).map((field) => field.label)
  if (spec.requiresGeneration) {
    const connectionID = values.modelConnectionId
    const connection = choices.connections.find((item) => item.value === String(connectionID))
    if (!hasValue(connectionID)) {
      return { state: 'blocked', label: '缺少模型服务', detail: '先在“连接设置”启用模型服务，生成步骤才能执行。', missing: ['模型服务'] }
    }
    // issue #209：已保存的蓝图可能引用了一条配置不完整的连接（历史数据）。
    // 必须在**保存/执行之前**就说清楚，而不是等到批次已开跑才报
    // `model connection unavailable` —— 那时已经花掉了真实的时间与额度。
    //
    // 这道判定必须排在「已停用」**之前**：不完整的连接在迁移 0040 之后
    // 同时是停用的（我们把它停用了），而那种情况下「请启用」是错误的指引 ——
    // 保存路径会直接拒掉启用（必填字段还是空的）。真正的下一步是补齐字段。
    if (connection?.disabled) {
      return {
        state: 'blocked',
        label: '模型服务配置不完整',
        detail: `当前引用的模型服务不能用于生成：${(connection.configIssues ?? []).join('；')}。`
          + '请到“连接设置”补齐必填字段，或换一个可用连接。',
        missing: [],
      }
    }
    if (connection?.meta?.includes('已停用')) {
      return { state: 'blocked', label: '模型服务已停用', detail: '当前引用的模型服务已停用，请换一个可用连接。', missing: [] }
    }
  }
  if (missing.length > 0) {
    return { state: 'incomplete', label: `待补齐 ${missing.length} 项`, detail: `还需要设置：${missing.join('、')}。`, missing }
  }
  return { state: 'ready', label: '可执行', detail: '必填配置已齐全，保存后可用于试制。', missing: [] }
}

function containsInvalidJSONMarker(value: unknown): boolean {
  if (Array.isArray(value)) return value.some(containsInvalidJSONMarker)
  if (!value || typeof value !== 'object') return false
  if (Object.prototype.hasOwnProperty.call(value, '__invalid')) return true
  return Object.values(value as Record<string, unknown>).some(containsInvalidJSONMarker)
}

function jsonExample(fieldName: string): string {
  if (fieldName === 'steps') return JSON.stringify([{ title: '提出问题', checkpoint: '问题已明确且可验证' }], null, 2)
  if (fieldName === 'jsonSchema') return JSON.stringify({ type: 'object', required: ['question', 'reasoning', 'answer'] }, null, 2)
  return '{\n  "key": "value"\n}'
}

/**
 * 分步表单里的一个思考步骤（issue #197 第 2 条）。
 *
 * 字段与「思维标准」页保持一致（步骤 ID / 步骤名称 / 具体做法 / 完成检查点），
 * 因为它们是**同一个实体**的两处入口。
 */
type StepDraft = { id: string; title: string; detail: string; checkpoint: string }

/** 把任意来源的 steps 值规整成可编辑的步骤列表。 */
function toStepDrafts(value: unknown): StepDraft[] {
  if (!Array.isArray(value)) return []
  return value.flatMap((item, index) => {
    if (item === null || item === undefined) return []
    if (typeof item === 'string') {
      // 历史数据里 steps 可能是纯字符串数组；转成「标题」而不是丢弃。
      return [{ id: `step-${index + 1}`, title: item, detail: '', checkpoint: '' }]
    }
    if (typeof item !== 'object') return []
    const record = item as Record<string, unknown>
    const asText = (key: string) => (typeof record[key] === 'string' ? String(record[key]) : '')
    return [{
      id: asText('id') || `step-${index + 1}`,
      title: asText('title'),
      detail: asText('detail'),
      checkpoint: asText('checkpoint'),
    }]
  })
}

/** 步骤列表 → 落库形状。 */
function fromStepDrafts(steps: StepDraft[]): Array<Record<string, string>> {
  return steps.map((step, index) => ({
    id: step.id.trim() || `step-${index + 1}`,
    title: step.title.trim(),
    detail: step.detail.trim(),
    checkpoint: step.checkpoint.trim(),
  }))
}

/**
 * 自然语言分步编辑器（issue #197 第 2 条）。
 *
 * 默认**表单视图**：写「做什么」与「怎么算完成」的中文，而不是 JSON。
 * JSON 仍可切换查看/编辑（排查与批量粘贴时必需），但它是次要视图 ——
 * 反过来就等于「默认把存储表示给用户看」，而那正是这条缺陷的定义。
 *
 * 校验与「思维标准」页一致：标题与检查点必填。**没有检查点的步骤无法被验证**，
 * 因此这里对空检查点给出内联提示，而不是静默保存一个不可验证的步骤。
 */
function StandardStepsEditor({
  id,
  value,
  disabled,
  onChange,
}: {
  id: string
  value: unknown
  disabled: boolean
  onChange: (value: unknown) => void
}) {
  const { Text } = Typography
  const steps = useMemo(() => toStepDrafts(value), [value])
  const [raw, setRaw] = useState(false)
  const update = (next: StepDraft[]) => onChange(fromStepDrafts(next))
  const patch = (index: number, changes: Partial<StepDraft>) =>
    update(steps.map((step, position) => (position === index ? { ...step, ...changes } : step)))
  const dragIndex = useRef<number | null>(null)
  const moveStep = (from: number, to: number) => {
    if (disabled || from === to || from < 0 || to < 0 || to >= steps.length) return
    const next = [...steps]
    const [step] = next.splice(from, 1)
    next.splice(to, 0, step)
    update(next)
  }
  const missingCheckpoint = steps.filter((step) => step.title.trim() !== '' && step.checkpoint.trim() === '').length
  return (
    <div className="blueprint-steps-editor" data-steps-editor="true">
      <div className="flex flex-wrap items-center gap-2 mb-2">
        <Button
          size="small"
          icon={<Plus size={13} />}
          disabled={disabled}
          onClick={() => update([...steps, { id: `step-${steps.length + 1}`, title: '', detail: '', checkpoint: '' }])}
          data-steps-add="true"
        >
          添加步骤
        </Button>
        <Button size="small" theme="borderless" onClick={() => setRaw((current) => !current)} data-steps-raw-toggle="true">
          {raw ? '用表单编辑' : '看 JSON'}
        </Button>
        <Text type="tertiary" size="small">
          共 {steps.length} 步 · 每步都要有「完成检查点」，没有检查点的步骤无法被验证
        </Text>
      </div>
      {raw ? (
        <JSONFieldEditor id={id} value={value} disabled={disabled} fieldName="steps" onChange={onChange} />
      ) : steps.length === 0 ? (
        <Text type="tertiary" size="small" data-steps-empty="true">
          还没有思考步骤。用「添加步骤」写下模型应当怎么做，以及每步怎么算完成。
        </Text>
      ) : (
        <div className="blueprint-steps-editor__list">
          {steps.map((step, index) => (
            <div className="blueprint-steps-editor__row" key={`${step.id}-${index}`} data-step-index={index}
              draggable={!disabled}
              onDragStart={() => { dragIndex.current = index }}
              onDragOver={(event) => { if (!disabled) event.preventDefault() }}
              onDrop={(event) => { event.preventDefault(); if (dragIndex.current !== null) moveStep(dragIndex.current, index); dragIndex.current = null }}
              onDragEnd={() => { dragIndex.current = null }}>
              <div className="wizard-field">
                <label className="wizard-field__label" htmlFor={`${id}-title-${index}`}>
                  步骤 {index + 1}：做什么
                </label>
                <Input
                  id={`${id}-title-${index}`}
                  value={step.title}
                  disabled={disabled}
                  placeholder="例如 识别题目里的约束条件"
                  onChange={(next) => patch(index, { title: next })}
                />
              </div>
              <div className="wizard-field">
                <label className="wizard-field__label" htmlFor={`${id}-checkpoint-${index}`}>
                  怎么算完成（检查点）
                </label>
                <Input
                  id={`${id}-checkpoint-${index}`}
                  value={step.checkpoint}
                  disabled={disabled}
                  aria-invalid={step.title.trim() !== '' && step.checkpoint.trim() === '' ? 'true' : undefined}
                  placeholder="例如 已列出全部约束且标注了来源"
                  onChange={(next) => patch(index, { checkpoint: next })}
                />
              </div>
              <div className="wizard-field">
                <label className="wizard-field__label" htmlFor={`${id}-detail-${index}`}>
                  具体做法（可选）
                </label>
                <Input
                  id={`${id}-detail-${index}`}
                  value={step.detail}
                  disabled={disabled}
                  placeholder="补充提示词细节"
                  onChange={(next) => patch(index, { detail: next })}
                />
              </div>
              <div className="blueprint-steps-editor__actions">
                <Button size="small" type="tertiary" icon={<ArrowUp size={13} />} aria-label={`上移步骤 ${index + 1}`} disabled={disabled || index === 0} onClick={() => moveStep(index, index - 1)} />
                <Button size="small" type="tertiary" icon={<ArrowDown size={13} />} aria-label={`下移步骤 ${index + 1}`} disabled={disabled || index === steps.length - 1} onClick={() => moveStep(index, index + 1)} />
                <Button
                  size="small"
                  type="tertiary"
                  icon={<Trash2 size={13} />}
                  aria-label={`删除步骤 ${index + 1}`}
                  disabled={disabled || steps.length <= 1}
                  onClick={() => update(steps.filter((_, position) => position !== index))}
                />
              </div>
            </div>
          ))}
        </div>
      )}
      {missingCheckpoint > 0 ? (
        <Text type="warning" size="small" className="block mt-2" data-steps-missing-checkpoint="true">
          有 {missingCheckpoint} 步只写了「做什么」而没有「怎么算完成」。补齐检查点后才能保存。
        </Text>
      ) : null}
    </div>
  )
}

function JSONFieldEditor({
  id,
  value,
  disabled,
  fieldName,
  onChange,
}: {
  id: string
  value: unknown
  disabled: boolean
  fieldName: string
  onChange: (value: unknown) => void
}) {
  const [text, setText] = useState(() => jsonFieldText(value))
  const invalid = value && typeof value === 'object' && Object.prototype.hasOwnProperty.call(value, '__invalid')
  useEffect(() => setText(jsonFieldText(value)), [value])
  return (
    <div className="blueprint-json-editor" data-json-state={invalid ? 'invalid' : 'valid'}>
      <textarea
        id={id}
        className="blueprint-input blueprint-input--json"
        rows={6}
        value={text}
        disabled={disabled}
        aria-invalid={invalid ? 'true' : undefined}
        onChange={(event) => {
          const nextText = event.target.value
          setText(nextText)
          const parsed = parseJSONField(nextText)
          onChange(parsed.ok ? parsed.value : { __invalid: nextText })
        }}
      />
      <div className="blueprint-json-editor__toolbar">
        <Button size="small" icon={<WandSparkles size={13} />} disabled={disabled} onClick={() => {
          const parsed = parseJSONField(text)
          if (parsed.ok) {
            const formatted = parsed.value === undefined ? '' : JSON.stringify(parsed.value, null, 2)
            setText(formatted)
            onChange(parsed.value)
          }
        }}>格式化</Button>
        <Button size="small" disabled={disabled} onClick={() => {
          const example = jsonExample(fieldName)
          setText(example)
          onChange(JSON.parse(example))
        }}>填入示例</Button>
        <span className={invalid ? 'blueprint-json-editor__status blueprint-json-editor__status--invalid' : 'blueprint-json-editor__status'}>
          {invalid ? '格式未完成' : text.trim() ? '格式正确' : '可选'}
        </span>
      </div>
    </div>
  )
}

function StringListEditor({ value, disabled, onChange }: { value: unknown; disabled: boolean; onChange: (value: string[]) => void }) {
  const items = Array.isArray(value) ? value.map((item) => String(item)) : ['']
  return (
    <div className="blueprint-list-editor">
      {items.map((item, index) => (
        <div className="blueprint-list-editor__row" key={`${index}-${item}`}>
          <input className="blueprint-input" value={item} disabled={disabled} onChange={(event) => {
            const next = [...items]
            next[index] = event.target.value
            onChange(next.filter((entry, itemIndex) => entry.trim() !== '' || itemIndex === next.length - 1))
          }} />
          <button type="button" className="blueprint-icon-button" aria-label="删除这一项" disabled={disabled || items.length <= 1} onClick={() => onChange(items.filter((_, itemIndex) => itemIndex !== index))}>×</button>
        </div>
      ))}
      <button type="button" className="blueprint-add-row" disabled={disabled} onClick={() => onChange([...items, ''])}>+ 添加一项</button>
    </div>
  )
}

function RatioMapEditor({ value, disabled, onChange }: { value: unknown; disabled: boolean; onChange: (value: Record<string, number>) => void }) {
  const entries = value && typeof value === 'object' && !Array.isArray(value) ? Object.entries(value as Record<string, unknown>) : [['', 0]]
  const total = entries.reduce((sum, [, item]) => sum + (Number(item) || 0), 0)
  return (
    <div className="blueprint-ratio-editor">
      {entries.map(([key, item], index) => (
        <div className="blueprint-ratio-editor__row" key={`${index}-${key}`}>
          <input className="blueprint-input" aria-label={`权重维度 ${index + 1}`} placeholder="维度 key" value={key} disabled={disabled} onChange={(event) => {
            const next = Object.fromEntries(entries.map(([entryKey, entryValue], entryIndex) => [entryIndex === index ? event.target.value : entryKey, Number(entryValue) || 0]).filter(([entryKey]) => String(entryKey).trim() !== ''))
            onChange(next)
          }} />
          <input className="blueprint-input blueprint-input--number" aria-label={`权重值 ${index + 1}`} type="number" min={0} max={1} step={0.01} value={Number(item) || 0} disabled={disabled} onChange={(event) => {
            const next = Object.fromEntries(entries.map(([entryKey, entryValue], entryIndex) => [String(entryKey), entryIndex === index ? Number(event.target.value) || 0 : Number(entryValue) || 0]).filter(([entryKey]) => String(entryKey).trim() !== ''))
            onChange(next)
          }} />
          <button type="button" className="blueprint-icon-button" aria-label="删除这一项" disabled={disabled || entries.length <= 1} onClick={() => onChange(Object.fromEntries(entries.filter((_, itemIndex) => itemIndex !== index)))}>×</button>
        </div>
      ))}
      <div className={Math.abs(total - 1) < 0.001 ? 'blueprint-ratio-total blueprint-ratio-total--valid' : 'blueprint-ratio-total'}>合计 {total.toFixed(2)}（需等于 1.00）</div>
      <button type="button" className="blueprint-add-row" disabled={disabled} onClick={() => onChange({ ...Object.fromEntries(entries), '': 0 })}>+ 添加维度</button>
    </div>
  )
}

/**
 * 覆盖矩阵页（P03）。
 *
 * T11 的验收项里有一条专属要求：**「覆盖缺口点击带切片进入下一批规划，
 * 不能触发旧内容重生成」**。因此缺口链接指向 `/pilot`（规划新批次），
 * 而不是任何「重新生成」入口 —— 后者会让已有成功内容被重跑并重复计费。
 */
/**
 * 思维标准页（P04）。
 *
 * 只读展示步骤顺序与检查点：编辑发生在蓝图页的标准节点（保存为新版本）。
 * 这里显式显示**顺序编号**，因为「步骤顺序会被执行侧沿用」是契约的一部分，
 * 而一个没有编号的列表无法让用户确认顺序。
 */
export function StandardPage() {
  const scope = useProjectScope()
  const navigate = useNavigate()
  const { Title, Text } = Typography
  const state = useVersionedDocument(scope.projectId, 'standard-versions')
  if (state.loading) {
    return (
      <div className="flex justify-center py-10">
        <Spin tip="正在加载思维标准" />
      </div>
    )
  }
  if (state.error) {
    return (
      <Card className="console-card">
        <Text strong className="block">
          加载失败
        </Text>
        <Text type="tertiary">{state.error}</Text>
        <Button size="small" className="mt-3" onClick={() => void state.reload()}>重试</Button>
      </Card>
    )
  }

  const payload = state.payload ?? { schemaVersion: 'standard.v1', steps: [] }
  return (
    <div className="console-page" data-studio-page="standard">
      <div className="console-page__header">
        <div>
          <Title heading={4} className="!mb-1">
            思维标准
          </Title>
        </div>
        <div className="console-page__actions"><CopyVersionButton state={state} /></div>
      </div>
      <StandardPayloadEditor payload={payload} disabled={state.isReadOnly || !state.canEdit} onChange={state.setPayload} />
      <DocumentSaveBar state={state} label="思维标准" />
      <details className="product-disclosure"><summary>版本历史</summary><DocumentHistory state={state} /></details>
      <div className="product-stage-footer"><span>{state.dirty ? '保存修改后继续' : '配置生成所需的步骤与检查点'}</span><Button theme="solid" type="primary" disabled={state.dirty || !state.current || state.isReadOnly || !state.canEdit} onClick={() => navigate(`${scope.href('project.blueprint')}?node=standard&standardVersionId=${state.current!.id}`)}>继续配置生成 →</Button></div>
    </div>
  )
}

/**
 * jsonFieldText 把字段值渲染成可编辑文本。
 *
 * 「非法 JSON 中间态」以 `{__invalid: text}` 的形式存在，这里原样回显：
 * 用户在敲 JSON 的过程中不该丢失已输入的内容。
 */
function jsonFieldText(value: unknown): string {
  if (value === undefined || value === null || value === '') return ''
  if (typeof value === 'object' && value !== null && '__invalid' in (value as Record<string, unknown>)) {
    return String((value as Record<string, unknown>).__invalid)
  }
  try {
    return JSON.stringify(value, null, 2)
  } catch {
    return String(value)
  }
}

/**
 * parseJSONField 解析用户输入的 JSON。
 *
 * 返回 ok=false 时**不写入**解析结果，而是以 `{__invalid: text}` 保留原文 ——
 * 这样「输入到一半的 JSON」不会被丢掉，而服务端仍会拒绝把非法 JSON 落库
 *（本地校验只是辅助，T15 的同一原则）。
 */
function parseJSONField(text: string): { ok: boolean; value: unknown } {
  const trimmed = text.trim()
  if (trimmed === '') return { ok: true, value: undefined }
  try {
    return { ok: true, value: JSON.parse(trimmed) }
  } catch {
    return { ok: false, value: undefined }
  }
}

/**
 * stripInvalidJSONMarkers 递归去掉编辑器临时标记 `__invalid`。
 *
 * 为什么必须清理：它是「用户输到一半」的中间态，直接提交会让服务端看到一个
 * 契约里不存在的字段。放在提交路径上只出现一次，避免漏清。
 *
 * 泛型 `<T>` 而不是裸 `unknown`：本函数**保持结构不变**（只删键、不改形状），
 * 因此调用方的 `Record<string, unknown>` 草稿可以直接拿回同一类型，
 * 不需要在调用点再断言一次。
 */
function stripInvalidJSONMarkers<T>(value: T): T {
  if (Array.isArray(value)) {
    // SAFETY: 运行时的 `value` 已由 Array.isArray 证明是数组；本函数对元素只
    // 删 `__invalid` 键、不改变元素形状，因此映射结果与原数组同形状。TS 无法
    // 把「T 是数组」表达成对 `.map` 结果的可判定收窄，故必须显式断言。
    return value.map(stripInvalidJSONMarkers) as unknown as T
  }
  if (value && typeof value === 'object') {
    const result: Record<string, unknown> = {}
    for (const [key, item] of Object.entries(value as Record<string, unknown>)) {
      if (key === '__invalid') continue
      result[key] = stripInvalidJSONMarkers(item)
    }
    // SAFETY: 同上 —— 对象分支重建的是同一形状的对象，只是少了 `__invalid` 键。
    return result as T
  }
  return value
}
