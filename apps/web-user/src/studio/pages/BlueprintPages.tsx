import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { useNavigate, useSearchParams } from 'react-router-dom'
import { Button, Card, Empty, Select, Spin, TextArea, Typography } from '@douyinfe/semi-ui'
import { AlertTriangle, CheckCircle2, Copy, FileCog, History, Save, WandSparkles, XCircle } from 'lucide-react'
import { client } from '../../lib/api'
import { newIdempotencyKey, projectPath } from '../../lib/api/studio'
import { useProjectScope } from '../ProjectLayout'
import { projectHref } from '../StudioLayout'
import { CopyVersionButton, CoveragePayloadEditor, DocumentHistory, DocumentSaveBar, StandardPayloadEditor, useVersionedDocument } from '../DocumentEditors'

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
}

type BlueprintChoices = {
  versions: Record<string, BlueprintChoice[]>
  connections: BlueprintChoice[]
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
  const bootstrapAttempted = useRef(false)
  const preserveDraftAfterNavigation = useRef(false)

  // `?node=` 与 `?version=` 都来自 URL：分享链接要能指向同一个节点与版本。
  const activeNodeKey = searchParams.get('node') ?? ''
  const viewingVersion = searchParams.get('version')

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
        client.get<{ providers?: Array<{ id: number; name: string; model: string; isActive: boolean }> }>('/v1/settings/connection-options'),
      ])
      setSpecs(nodesResponse.data.items ?? [])
      const list = versionsResponse.data.items ?? []
      setVersions(list)
      setHeadRevision(versionsResponse.data.document?.revision ?? 0)
      const editable = versionsResponse.data.canEdit === true
      setCanEdit(editable)
      const versionChoices = (response: VersionsResponse): BlueprintChoice[] =>
        (response.items ?? []).map((item) => ({
          value: String(item.id),
        label: `v${item.version}${item.changeReason ? ` · ${item.changeReason}` : ''}`,
        meta: item.contentHash ? item.contentHash.slice(0, 8) : undefined,
        version: item.version,
        }))
      setChoices({
        versions: {
          coverageVersionId: versionChoices(coverageResponse.data),
          standardVersionId: versionChoices(standardResponse.data),
          qualityPolicyVersionId: versionChoices(qualityResponse.data),
          mappingVersionId: versionChoices(mappingResponse.data),
          // 量表版本目前与质量策略共用项目版本目录；服务端保存的仍是明确的版本行 ID。
          rubricVersionId: versionChoices(qualityResponse.data),
        },
        connections: (connectionsResponse.data.providers ?? []).map((item) => ({
          value: String(item.id),
          label: item.name,
          meta: `${item.model}${item.isActive ? '' : ' · 已停用'}`,
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
        setDraft(version.payload)
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
  }, [scope.projectId, viewingVersion])

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

  // URL 中存在 version 就代表「历史查看」；current 此时恰好也是被查看的
  // 历史版本，拿两者比较会把只读状态错误地判成可编辑。
  const isReadOnly = viewingVersion !== null

  const save = useCallback(async () => {
    if (!draft || !activeSpec || !canEdit) return
    if (containsInvalidJSONMarker(draft)) {
      setSaveError('请先修正 JSON 格式，再保存蓝图。')
      return
    }
    // 提交前清理「非法 JSON 中间态」标记：它是编辑器的临时状态，
    // 不能进入 payload（那会让服务端看到一个不认识的字段）。
    const cleaned = stripInvalidJSONMarkers(draft)
    if (changeReason.trim() === '') {
      // 变更理由是契约 §2.2 的一部分：没有理由的历史版本无法解释
      // 「为什么当时这么改」，而那是回溯与审计的唯一线索。
      setSaveError('请填写变更理由（它会被写入版本历史，供以后回溯）')
      return
    }
    setSaving(true)
    setSaveError(null)
    try {
      await client.post(
        `${projectPath(scope.projectId)}/blueprint-versions`,
        {
          // expectedRevision 用**当前头记录**的 revision：不匹配返回 409 并保留草稿
          //（契约 §1.4「不匹配返回 409，并保留用户草稿」）。
          expectedRevision: headRevision,
          logicalId: 'main',
          changeReason: changeReason.trim(),
          payload: cleaned,
        },
        { headers: { 'Idempotency-Key': newIdempotencyKey() } },
      )
      setChangeReason('')
      // 保存后**留在当前节点**（T11 验收项），只刷新版本列表与 current。
      setSearchParams((params) => {
        params.delete('version')
        return params
      })
      await load(null)
    } catch (saveErrorValue) {
      const apiError = saveErrorValue as { statusCode?: number; message?: string }
      if (apiError.statusCode === 409) {
        setSaveError('版本已被其他人修改（乐观锁冲突）。你的草稿已保留，请刷新后比较差异再保存。')
      } else {
        setSaveError(apiError.message ?? '保存失败')
      }
    } finally {
      setSaving(false)
    }
  }, [activeSpec, canEdit, changeReason, draft, headRevision, load, scope.projectId, setSearchParams])

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
  const relatedPage = activeSpec?.key === 'coverage'
    ? { route: 'project.coverage', label: '覆盖矩阵' }
    : activeSpec?.key === 'standard'
      ? { route: 'project.standard', label: '思维标准' }
      : activeSpec?.key === 'generation' || activeSpec?.key === 'evaluation'
        ? { route: 'settings.connections', label: '模型连接' }
      : activeSpec?.key === 'rules'
        ? { route: 'project.rules', label: '规则策略' }
        : activeSpec?.key === 'delivery'
          ? { route: 'project.newRelease', label: '交付映射' }
        : null
  const relatedVersion = activeSpec
    ? choices.versions[activeSpec.fields.find((field) => field.kind === 'id')?.name ?? '']?.find(
      (option) => String(nodeValuesForActive[activeSpec.fields.find((field) => field.kind === 'id')?.name ?? '']) === option.value,
    )
    : undefined
  const selectedConnections = activeSpec?.key === 'generation'
    ? choices.connections.filter((option) => option.value === String(nodeValuesForActive.modelConnectionId))
    : activeSpec?.key === 'evaluation'
      ? choices.connections.filter((option) => Array.isArray(nodeValuesForActive.judgeConnectionIds) && nodeValuesForActive.judgeConnectionIds.map(String).includes(option.value))
      : []

  return (
    <div className="console-page blueprint-page" data-studio-page="blueprint">
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
          <div className="eyebrow">DESIGN / BLUEPRINT</div>
          <h1>生产蓝图</h1>
          <Text type="tertiary">先确认每一步需要什么，再保存为新的配置版本。已经运行的批次不会被覆盖。</Text>
        </div>
        <Button theme="solid" type="primary" onClick={() => navigate(scope.href('project.pilot'))}>小批试制 →</Button>
      </header>

      <div className="blueprint-layout">
        <section className="blueprint-canvas" aria-label="生产流程画布">
          <div className="blueprint-canvas__eyebrow">ATELIER / PRODUCTION BLUEPRINT / v{current?.version ?? '—'}</div>
          <div className="blueprint-canvas__hint">先看清步骤关系，再调整当前步骤。每个节点都是可键盘访问的按钮。</div>
          <div className="blueprint-nodes">
          {specs.map((spec) => (
            <button
              key={spec.key}
              type="button"
              data-node-key={spec.key}
              className={
                spec.key === activeSpec?.key ? 'blueprint-node blueprint-node--active' : 'blueprint-node'
              }
              aria-current={spec.key === activeSpec?.key ? 'true' : undefined}
              onClick={() => {
                setSearchParams((params) => {
                  params.set('node', spec.key)
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
          ))}
          </div>
        </section>

        <section className="blueprint-inspector" aria-label="步骤检查器">
          {activeSpec ? (
            <>
              <Title heading={5} className="!mb-1">
                {activeSpec.label}
              </Title>
              <Text type="tertiary" className="block mb-3">
                {activeSpec.caption}
              </Text>

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
                <NodeFields
                  spec={activeSpec}
                  values={nodeValuesForActive}
                  disabled={isReadOnly || !canEdit}
                  choices={choices}
                  onChange={(name, value) => {
                    if (!draft || !activeSpec) return
                    const nextValues = { ...nodeValuesForActive, [name]: value }
                    setDraft(withNodeValues(draft, activeSpec, nextValues))
                  }}
                />
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
                    : `${scope.href(relatedPage.route)}${relatedVersion?.version ? `?version=${relatedVersion.version}` : ''}`)}>
                    {selectedConnections.length > 0 || relatedVersion ? `查看${relatedPage.label}` : `去配置${relatedPage.label}`}
                  </Button>
                </div>
              ) : null}

              <div className="mt-4">
                <Text type="tertiary" size="small" className="block mb-1">
                  变更理由（必填，方便团队回溯）
                </Text>
                <TextArea
                  value={changeReason}
                  disabled={!canEdit}
                  onChange={(value) => setChangeReason(value)}
                  placeholder="例如：把并发从 8 提到 12"
                  autosize={{ minRows: 2, maxRows: 4 }}
                  data-field="blueprint-change-reason"
                />
              </div>

              {saveError ? (
                <div className="wizard-field__error mt-2" role="alert">
                  {saveError}
                </div>
              ) : null}

              {hasInvalidJSON ? (
                <div className="blueprint-validation-error" role="alert">
                  <XCircle size={14} aria-hidden /> JSON 结构还没有完成，修正后才能保存。
                </div>
              ) : null}

              <div className="blueprint-save-bar mt-3">
                <span className={dirty ? 'blueprint-dirty' : 'blueprint-clean'}>{dirty ? '有未保存修改' : '已保存'}</span>
                <Button
                  theme="solid"
                  type="primary"
                  icon={<Save size={14} />}
                  loading={saving}
                  disabled={isReadOnly || !canEdit || hasInvalidJSON}
                  onClick={() => void save()}
                >
                  保存为新版本
                </Button>
                {current && canEdit ? (
                  <Button
                    icon={<Copy size={14} />}
                    onClick={() => {
                      // 复制 = 把当前版本内容作为新草稿（仍要填理由、仍会新建版本）。
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

        <aside className="blueprint-history" aria-label="版本历史">
          <Text strong size="small" className="block mb-2">
            版本历史（只读）
          </Text>
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
        </aside>
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
              <JSONFieldEditor
                id={`blueprint-${spec.key}-${field.name}`}
                value={value}
                disabled={disabled}
                fieldName={field.name}
                onChange={(next) => onChange(field.name, next)}
              />
            ) : field.kind === 'id' || field.kind === 'idList' ? (
              <Select
                id={`blueprint-${spec.key}-${field.name}`}
                className="blueprint-select"
                multiple={field.kind === 'idList'}
                value={selectValue as string | string[] | undefined}
                placeholder={options.length > 0 ? '选择已保存版本' : '暂无可选版本'}
                disabled={disabled}
                optionList={options.map((option) => ({ value: option.value, label: option.label, extra: option.meta }))}
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
export function CoveragePage() {
  const scope = useProjectScope()
  const navigate = useNavigate()
  const { Title, Text } = Typography
  const state = useVersionedDocument(scope.projectId, 'coverage-versions')
  if (state.loading) {
    return (
      <div className="flex justify-center py-10">
        <Spin tip="正在加载覆盖方案" />
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

  const payload = state.payload ?? { schemaVersion: 'coverage.v1', domains: [] }
  const domains = Array.isArray(payload.domains) ? payload.domains as Array<Record<string, unknown>> : []
  const totalQuota = domains.reduce(
    (sum, domain) =>
      sum +
      ((domain.directions as Array<Record<string, unknown>> | undefined) ?? []).reduce(
        (inner, direction) => inner + Number(direction.quota ?? 0),
        0,
      ),
    0,
  )

  return (
    <div className="console-page" data-studio-page="coverage">
      <div className="console-page__header">
        <div>
          <Title heading={4} className="!mb-1">
            覆盖矩阵
          </Title>
          <Text type="tertiary">
            领域和方向使用稳定 ID；保存新版本不会改写已经运行的批次。
          </Text>
        </div>
        <div className="console-page__actions"><CopyVersionButton state={state} /></div>
      </div>

      <Card className="console-card mb-3" bodyStyle={{ padding: 16 }}>
        <Text type="tertiary" size="small">共 {domains.length} 个领域，方向配额合计 {totalQuota}（计划单元数，不是已产出数量）</Text>
      </Card>
      <CoveragePayloadEditor payload={payload} disabled={state.isReadOnly || !state.canEdit} onChange={state.setPayload} />
      <DocumentSaveBar state={state} label="覆盖方案" />
      <DocumentHistory state={state} />
      {domains.length > 0 ? <Card className="console-card mt-3" bodyStyle={{ padding: 14 }}>
        <Text strong className="block mb-2">从方向开始下一批试制</Text>
        <div className="coverage-directions coverage-directions--actions">
          {domains.flatMap((domain) => ((domain.directions as Array<Record<string, unknown>> | undefined) ?? []).map((direction) => <button key={`${String(domain.stableId)}-${String(direction.stableId)}`} type="button" className="coverage-gap" data-coverage-gap="true" onClick={() => navigate(`${projectHref('project.pilot', scope.projectId)}?slice=${encodeURIComponent(String(direction.stableId ?? ''))}`)}>以“{String(direction.name ?? '未命名方向')}”规划新批次</button>))}
        </div>
      </Card> : null}
    </div>
  )
}

/**
 * 思维标准页（P04）。
 *
 * 只读展示步骤顺序与检查点：编辑发生在蓝图页的标准节点（保存为新版本）。
 * 这里显式显示**顺序编号**，因为「步骤顺序会被执行侧沿用」是契约的一部分，
 * 而一个没有编号的列表无法让用户确认顺序。
 */
export function StandardPage() {
  const scope = useProjectScope()
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
          <Text type="tertiary">步骤顺序就是执行顺序；每一步都要有可核对的完成标准。</Text>
        </div>
        <div className="console-page__actions"><CopyVersionButton state={state} /></div>
      </div>
      <StandardPayloadEditor payload={payload} disabled={state.isReadOnly || !state.canEdit} onChange={state.setPayload} />
      <DocumentSaveBar state={state} label="思维标准" />
      <DocumentHistory state={state} />
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
 */
function stripInvalidJSONMarkers(value: unknown): unknown {
  if (Array.isArray(value)) return value.map(stripInvalidJSONMarkers)
  if (value && typeof value === 'object') {
    const result: Record<string, unknown> = {}
    for (const [key, item] of Object.entries(value as Record<string, unknown>)) {
      if (key === '__invalid') continue
      result[key] = stripInvalidJSONMarkers(item)
    }
    return result
  }
  return value
}
