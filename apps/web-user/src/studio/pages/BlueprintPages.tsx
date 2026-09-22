import { useCallback, useEffect, useMemo, useState } from 'react'
import { useNavigate, useSearchParams } from 'react-router-dom'
import { Button, Card, Empty, Spin, Tag, TextArea, Typography } from '@douyinfe/semi-ui'
import { AlertTriangle, Copy, History, Save } from 'lucide-react'
import { client } from '../../lib/api'
import { newIdempotencyKey, projectPath } from '../../lib/api/studio'
import { useProjectScope } from '../ProjectLayout'

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

  // `?node=` 与 `?version=` 都来自 URL：分享链接要能指向同一个节点与版本。
  const activeNodeKey = searchParams.get('node') ?? ''
  const viewingVersion = searchParams.get('version')

  const load = useCallback(async (versionOverride?: string | null) => {
    setLoading(true)
    setError(null)
    try {
      const [nodesResponse, versionsResponse] = await Promise.all([
        client.get<BlueprintNodesResponse>(`${projectPath(scope.projectId)}/blueprint-nodes`),
        client.get<VersionsResponse>(`${projectPath(scope.projectId)}/blueprint-versions?limit=50`),
      ])
      setSpecs(nodesResponse.data.items ?? [])
      const list = versionsResponse.data.items ?? []
      setVersions(list)
      setHeadRevision(versionsResponse.data.document?.revision ?? 0)
      setCanEdit(versionsResponse.data.canEdit === true)

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
    void load()
  }, [load])

  const activeSpec = useMemo(
    () => specs.find((spec) => spec.key === activeNodeKey) ?? specs[0],
    [specs, activeNodeKey],
  )

  // URL 中存在 version 就代表「历史查看」；current 此时恰好也是被查看的
  // 历史版本，拿两者比较会把只读状态错误地判成可编辑。
  const isReadOnly = viewingVersion !== null

  const save = useCallback(async () => {
    if (!draft || !activeSpec || !canEdit) return
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
  const relatedPage = activeSpec?.key === 'coverage'
    ? { route: 'project.coverage', label: '覆盖矩阵' }
    : activeSpec?.key === 'standard'
      ? { route: 'project.standard', label: '思维标准' }
      : activeSpec?.key === 'rules'
        ? { route: 'project.rules', label: '规则策略' }
        : null

  return (
    <div className="console-page blueprint-page" data-studio-page="blueprint">
      {isReadOnly ? (
        <div className="blueprint-readonly-banner" data-readonly="true">
          <History size={14} aria-hidden />
          <Text size="small">
            正在查看历史版本 v{compareVersion}（只读）。复制它或直接保存会产生**新版本**，
            旧版本永不覆盖。
          </Text>
        </div>
      ) : null}

      <header className="atelier-page-intro blueprint-page-intro">
        <div>
          <div className="eyebrow">DESIGN / BLUEPRINT</div>
          <h1>生产蓝图</h1>
          <Text type="tertiary">先看清步骤关系，再调整当前步骤。改动保存为新方案，不覆盖已运行批次。</Text>
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
              <span className="blueprint-node__icon" aria-hidden>◇</span>
              <span className="blueprint-node__copy"><span className="blueprint-node__label">{spec.label}</span><span className="blueprint-node__caption">{spec.caption}</span></span>
              {spec.availability === 'planned' ? (
                <span className="blueprint-node__badge" title={`由 ${spec.task} 交付`}>
                  待交付
                </span>
              ) : null}
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
                  onChange={(name, value) => {
                    if (!draft || !activeSpec) return
                    const nextValues = { ...nodeValuesForActive, [name]: value }
                    setDraft(withNodeValues(draft, activeSpec, nextValues))
                  }}
                />
              )}

              <div className="mt-4">
                <Text type="tertiary" size="small" className="block mb-1">
                  变更理由（必填，写入版本历史）
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

              <div className="mt-3 flex gap-2">
                <Button
                  theme="solid"
                  type="primary"
                  icon={<Save size={14} />}
                  loading={saving}
                  disabled={isReadOnly || !canEdit}
                  onClick={() => void save()}
                >
                  保存为新版本
                </Button>
                {current && canEdit ? (
                  <Button
                    icon={<Copy size={14} />}
                    onClick={() => {
                      // 复制 = 把当前版本内容作为新草稿（仍要填理由、仍会新建版本）。
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
                    <span className="blueprint-history__hash">{version.contentHash.slice(0, 8)}</span>
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
              当前版本内容 hash：{current.contentHash}
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
 * 只实现「可安全编辑」的控件类型；`id`/`idList`/`json`/`ratioMap` 用只读
 * 文本展示当前值 —— 它们需要真实的候选列表（连接、量表、映射版本）与
 * 结构化编辑器，而那些属于 T28/T20 的页面。**显示当前值**而不是隐藏它们，
 * 是为了让用户至少能看到「现在引用的是什么」，而不是以为字段不存在。
 */
function NodeFields({
  spec,
  values,
  disabled,
  onChange,
}: {
  spec: NodeSpec
  values: Record<string, unknown>
  disabled: boolean
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
        // id/idList/ratioMap 仍只读：它们需要真实候选列表（连接/量表/维度），
        // 给一个能编辑但无法选值的输入框会制造「看起来能配」的错觉。
        const complex = ['id', 'idList', 'ratioMap'].includes(field.kind)
        const isJSONField = field.kind === 'json'
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
              // JSON 字段用文本域编辑：内容必须是**合法 JSON**，
              // 非法时保留原值而不是写入坏数据（服务端也会再校验一次）。
              <textarea
                id={`blueprint-${spec.key}-${field.name}`}
                className="blueprint-input blueprint-input--json"
                rows={4}
                value={jsonFieldText(value)}
                disabled={disabled}
                onChange={(event) => {
                  const parsed = parseJSONField(event.target.value)
                  if (parsed.ok) onChange(field.name, parsed.value)
                  else onChange(field.name, { __invalid: event.target.value })
                }}
              />
            ) : complex ? (
              // 复杂引用字段先只读展示：给一个能编辑但无法选值的输入框
              // 会制造「看起来能配、其实配不了」的错觉。
              <Text type="tertiary" size="small">
                当前值：{display || '（未设置）'}（候选选择器在 T28/T20 提供）
              </Text>
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
                  if (field.kind === 'stringList') {
                    onChange(field.name, raw === '' ? [] : raw.split(',').map((item) => item.trim()))
                    return
                  }
                  if (field.kind === 'enum') {
                    onChange(field.name, raw)
                    return
                  }
                  onChange(field.name, raw)
                }}
              />
            )}
            {field.kind === 'enum' && field.options ? (
              <Text type="tertiary" size="small" className="block mt-1">
                可选：{field.options.join(' / ')}
              </Text>
            ) : null}
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
  const [payload, setPayload] = useState<Record<string, unknown> | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [loading, setLoading] = useState(true)

  useEffect(() => {
    let cancelled = false
    void (async () => {
      try {
        const response = await client.get<VersionsResponse>(
          `${projectPath(scope.projectId)}/coverage-versions?limit=1`,
        )
        const latest = (response.data.items ?? [])[0]
        if (!cancelled) setPayload(latest?.payload ?? null)
      } catch (loadError) {
        if (!cancelled) {
          setError(loadError instanceof Error ? loadError.message : '加载覆盖方案失败')
        }
      } finally {
        if (!cancelled) setLoading(false)
      }
    })()
    return () => {
      cancelled = true
    }
  }, [scope.projectId])

  if (loading) {
    return (
      <div className="flex justify-center py-10">
        <Spin tip="正在加载覆盖方案" />
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

  const domains = (payload?.domains as Array<Record<string, unknown>> | undefined) ?? []
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
            领域与方向使用**稳定 ID**：删除草稿方向不会破坏已引用该版本的批次。
          </Text>
        </div>
      </div>

      {domains.length === 0 ? (
        <Card className="console-card">
          <Empty description="还没有覆盖方案。先在蓝图的「覆盖范围」节点引用或保存一份覆盖版本。" />
        </Card>
      ) : (
        <>
          <Card className="console-card mb-3" bodyStyle={{ padding: 16 }}>
            <Text type="tertiary" size="small">
              共 {domains.length} 个领域，方向配额合计 {totalQuota}（**计划单元数**，不是已产出）
            </Text>
          </Card>
          <div className="coverage-matrix">
            {domains.map((domain, domainIndex) => {
              const directions = (domain.directions as Array<Record<string, unknown>> | undefined) ?? []
              return (
                <Card key={String(domain.stableId ?? domainIndex)} className="console-card" bodyStyle={{ padding: 14 }}>
                  <div className="flex items-center justify-between gap-2">
                    <Text strong>{String(domain.name ?? '(未命名领域)')}</Text>
                    <Tag size="small">{directions.length} 个方向</Tag>
                  </div>
                  <ul className="coverage-directions">
                    {directions.map((direction, directionIndex) => (
                      <li key={String(direction.stableId ?? directionIndex)}>
                        <span className="coverage-direction__name">{String(direction.name ?? '')}</span>
                        <span className="coverage-direction__quota">配额 {String(direction.quota ?? 0)}</span>
                        <button
                          type="button"
                          className="coverage-gap"
                          data-coverage-gap="true"
                          onClick={() => {
                            // 只进入**规划**（新批次），不触发任何重生成：
                            // 重跑已有内容会重复计费，也不会改善覆盖（T11 验收项）。
                            navigate(
                              `/p/${scope.projectId}/pilot?slice=${encodeURIComponent(
                                String(direction.stableId ?? ''),
                              )}`,
                            )
                          }}
                        >
                          以此方向规划新批次
                        </button>
                      </li>
                    ))}
                  </ul>
                </Card>
              )
            })}
          </div>
        </>
      )}
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
  const [payload, setPayload] = useState<Record<string, unknown> | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [loading, setLoading] = useState(true)

  useEffect(() => {
    let cancelled = false
    void (async () => {
      try {
        const response = await client.get<VersionsResponse>(
          `${projectPath(scope.projectId)}/standard-versions?limit=1`,
        )
        const latest = (response.data.items ?? [])[0]
        if (!cancelled) setPayload(latest?.payload ?? null)
      } catch (loadError) {
        if (!cancelled) setError(loadError instanceof Error ? loadError.message : '加载思维标准失败')
      } finally {
        if (!cancelled) setLoading(false)
      }
    })()
    return () => {
      cancelled = true
    }
  }, [scope.projectId])

  if (loading) {
    return (
      <div className="flex justify-center py-10">
        <Spin tip="正在加载思维标准" />
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

  const steps = (payload?.steps as Array<Record<string, unknown>> | undefined) ?? []
  return (
    <div className="console-page" data-studio-page="standard">
      <div className="console-page__header">
        <div>
          <Title heading={4} className="!mb-1">
            思维标准
          </Title>
          <Text type="tertiary">步骤顺序即执行顺序；每一步都应有检查点以便自查。</Text>
        </div>
      </div>
      {steps.length === 0 ? (
        <Card className="console-card">
          <Empty description="还没有标准步骤。在蓝图的「思维标准」节点编辑并保存为新版本。" />
        </Card>
      ) : (
        <ol className="standard-steps">
          {steps.map((step, index) => (
            <li key={String(step.id ?? index)}>
              <Card className="console-card" bodyStyle={{ padding: 14 }}>
                <div className="flex items-center gap-2">
                  <span className="standard-step__index">{index + 1}</span>
                  <Text strong>{String(step.title ?? step.name ?? '(未命名步骤)')}</Text>
                </div>
                {step.checkpoint ? (
                  <Text type="tertiary" size="small" className="block mt-1">
                    检查点：{String(step.checkpoint)}
                  </Text>
                ) : (
                  <Text type="warning" size="small" className="block mt-1">
                    该步骤没有检查点：缺检查点的步骤无法在生成时自查。
                  </Text>
                )}
              </Card>
            </li>
          ))}
        </ol>
      )}
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
