import { useCallback, useEffect, useRef, useState } from 'react'
import { useSearchParams } from 'react-router-dom'
import { Button, Card, Empty, Input, Select, TextArea, Typography } from '@douyinfe/semi-ui'
import { ChevronDown, ChevronUp, Copy, Plus, Save, Trash2 } from 'lucide-react'
import { client } from '../lib/api'
import { newIdempotencyKey, projectPath, type ProjectResourceId } from '../lib/api/studio'

const Text = Typography.Text

export type VersionedDocument = {
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

type VersionListResponse = {
  items: VersionedDocument[]
  document?: { revision: number; currentVersion: number }
  canEdit?: boolean
}

type DocumentEditorState = {
  payload: Record<string, unknown> | null
  setPayload: (payload: Record<string, unknown>) => void
  current: VersionedDocument | null
  versions: VersionedDocument[]
  loading: boolean
  error: string | null
  saving: boolean
  saveError: string | null
  canEdit: boolean
  isReadOnly: boolean
  dirty: boolean
  changeReason: string
  setChangeReason: (reason: string) => void
  save: () => Promise<boolean>
  copyCurrentToDraft: () => void
  reload: () => Promise<void>
}

function readVersion(body: unknown): VersionedDocument {
  if (body && typeof body === 'object') {
    const record = body as { version?: VersionedDocument; data?: VersionedDocument }
    if (record.version) return record.version
    if (record.data) return record.data
  }
  return body as VersionedDocument
}

/**
 * 五类 Atelier 文档共用的版本读取/保存状态机。
 * 保存永远创建新版本，历史版本由 URL 的 ?version= 进入只读态。
 */
export function useVersionedDocument(projectId: ProjectResourceId, segment: string): DocumentEditorState {
  const [searchParams, setSearchParams] = useSearchParams()
  const viewingVersion = searchParams.get('version')
  const [payload, setPayload] = useState<Record<string, unknown> | null>(null)
  const [current, setCurrent] = useState<VersionedDocument | null>(null)
  const [versions, setVersions] = useState<VersionedDocument[]>([])
  const [headRevision, setHeadRevision] = useState(0)
  const [canEdit, setCanEdit] = useState(false)
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)
  const [saving, setSaving] = useState(false)
  const [saveError, setSaveError] = useState<string | null>(null)
  const [changeReason, setChangeReason] = useState('')
  const preserveDraftAfterNavigation = useRef(false)

  const load = useCallback(async (versionOverride?: string | null) => {
    setLoading(true)
    setError(null)
    try {
      const listResponse = await client.get<VersionListResponse>(`${projectPath(projectId)}/${segment}?limit=50`)
      const list = listResponse.data.items ?? []
      setVersions(list)
      setHeadRevision(listResponse.data.document?.revision ?? 0)
      setCanEdit(listResponse.data.canEdit === true)
      const requestedVersion = versionOverride === undefined ? viewingVersion : versionOverride
      if (requestedVersion) {
        const response = await client.get(`${projectPath(projectId)}/${segment}/${requestedVersion}`)
        const version = readVersion(response.data)
        setCurrent(version)
        setPayload(version.payload)
      } else if (list.length > 0) {
        const response = await client.get(`${projectPath(projectId)}/${segment}/${list[0].version}`)
        const version = readVersion(response.data)
        setCurrent(version)
        setPayload(version.payload)
      } else {
        setCurrent(null)
        setPayload(null)
      }
    } catch (loadError) {
      setError(loadError instanceof Error ? loadError.message : '加载配置失败')
    } finally {
      setLoading(false)
    }
  }, [projectId, segment, viewingVersion])

  useEffect(() => {
    // Removing ?version after copying a historical version is a navigation,
    // but it must not reload the latest payload over the draft we just copied.
    if (preserveDraftAfterNavigation.current && viewingVersion === null) {
      preserveDraftAfterNavigation.current = false
      return
    }
    void load()
  }, [load, viewingVersion])

  const dirty = payload !== null && JSON.stringify(payload) !== JSON.stringify(current?.payload ?? null)
  const isReadOnly = viewingVersion !== null

  const save = useCallback(async () => {
    if (!payload || !canEdit || isReadOnly) return false
    if (changeReason.trim() === '') {
      setSaveError('请填写变更理由，方便团队知道这次配置为什么调整。')
      return false
    }
    setSaving(true)
    setSaveError(null)
    try {
      await client.post(`${projectPath(projectId)}/${segment}`, {
        expectedRevision: headRevision,
        logicalId: 'main',
        changeReason: changeReason.trim(),
        payload,
      }, { headers: { 'Idempotency-Key': newIdempotencyKey() } })
      setChangeReason('')
      setSearchParams((params) => {
        params.delete('version')
        return params
      })
      await load(null)
      return true
    } catch (saveErrorValue) {
      const apiError = saveErrorValue as { statusCode?: number; message?: string }
      setSaveError(apiError.statusCode === 409
        ? '版本已被其他人修改。请重新加载后比较，再保存你的草稿。'
        : apiError.message ?? '保存失败，请检查字段后重试。')
      return false
    } finally {
      setSaving(false)
    }
  }, [canEdit, changeReason, headRevision, isReadOnly, load, payload, projectId, segment, setSearchParams])

  const copyCurrentToDraft = useCallback(() => {
    if (!current) return
    preserveDraftAfterNavigation.current = viewingVersion !== null
    setPayload({ ...current.payload })
  }, [current, viewingVersion])

  return { payload, setPayload, current, versions, loading, error, saving, saveError, canEdit, isReadOnly, dirty, changeReason, setChangeReason, save, copyCurrentToDraft, reload: () => load() }
}

export function DocumentSaveBar({ state, label }: { state: DocumentEditorState; label: string }) {
  return (
    <Card className="document-editor__save-card" bodyStyle={{ padding: 16 }}>
      <div className="document-editor__save-head">
        <div>
          <Text strong>{state.isReadOnly ? `历史版本 v${state.current?.version ?? '—'}（只读）` : state.dirty ? '有未保存修改' : '当前版本已保存'}</Text>
          <Text type="tertiary" size="small" className="block">
            {state.current ? `当前为 v${state.current.version}，指纹 ${state.current.contentHash.slice(0, 10)}` : '尚未保存版本'}
          </Text>
        </div>
        <Button theme="solid" type="primary" icon={<Save size={14} />} loading={state.saving} disabled={state.isReadOnly || !state.canEdit || !state.dirty} onClick={() => void state.save()}>
          保存{label}新版本
        </Button>
      </div>
      {!state.isReadOnly ? (
        <div className="document-editor__reason">
          <label className="wizard-field__label" htmlFor={`document-change-reason-${label}`}>变更理由</label>
          <Input id={`document-change-reason-${label}`} value={state.changeReason} disabled={!state.canEdit} onChange={state.setChangeReason} placeholder="例如：增加冷链异常场景，补齐方向配额" />
        </div>
      ) : null}
      {state.saveError ? <div className="wizard-field__error" role="alert">{state.saveError}</div> : null}
    </Card>
  )
}

export function DocumentHistory({ state }: { state: DocumentEditorState }) {
  if (state.versions.length === 0) return null
  return (
    <div className="document-editor__history" aria-label="配置版本历史">
      <Text strong size="small">版本历史</Text>
      <div className="document-editor__history-list">
        {state.versions.map((version) => (
          <a key={version.id} href={`?version=${version.version}`} className={version.version === state.current?.version ? 'document-editor__history-item is-current' : 'document-editor__history-item'}>
            <span>v{version.version}</span>
            <span>{version.changeReason || '未填写理由'}</span>
          </a>
        ))}
      </div>
    </div>
  )
}

function asRecord(value: unknown): Record<string, unknown> {
  return value && typeof value === 'object' && !Array.isArray(value) ? value as Record<string, unknown> : {}
}

function asArray(value: unknown): Array<Record<string, unknown>> {
  return Array.isArray(value) ? value.map(asRecord) : []
}

function updateAt<T>(items: T[], index: number, patch: Partial<T>): T[] {
  return items.map((item, itemIndex) => itemIndex === index ? { ...item as object, ...patch } as T : item)
}

function removeAt<T>(items: T[], index: number): T[] { return items.filter((_, itemIndex) => itemIndex !== index) }

function addButton(label: string, onClick: () => void, disabled: boolean) {
  return <Button size="small" icon={<Plus size={13} />} disabled={disabled} onClick={onClick}>{label}</Button>
}

export function CoveragePayloadEditor({ payload, disabled, onChange }: { payload: Record<string, unknown>; disabled: boolean; onChange: (payload: Record<string, unknown>) => void }) {
  const domains = asArray(payload.domains)
  const updateDomains = (next: Array<Record<string, unknown>>) => onChange({ ...payload, schemaVersion: 'coverage.v1', domains: next })
  return (
    <div className="document-editor document-editor--coverage">
      <div className="document-editor__intro"><Text strong>覆盖范围编辑器</Text><Text type="tertiary" size="small">把领域拆成可执行方向，并为每个方向设置计划数量。稳定 ID 用于让历史批次继续指向原版本。</Text></div>
      {domains.length === 0 ? <Empty description="还没有领域，点击下方按钮开始配置。" /> : domains.map((domain, domainIndex) => {
        const directions = asArray(domain.directions)
        return <Card className="document-editor__section" key={`${String(domain.stableId)}-${domainIndex}`} bodyStyle={{ padding: 14 }}>
          <div className="document-editor__section-head"><strong>领域 {domainIndex + 1}</strong><Button type="tertiary" icon={<Trash2 size={14} />} aria-label="删除领域" disabled={disabled || domains.length <= 1} onClick={() => updateDomains(removeAt(domains, domainIndex))} /></div>
          <div className="document-editor__grid document-editor__grid--three">
            <label className="wizard-field"><span className="wizard-field__label">领域名称</span><Input value={String(domain.name ?? '')} disabled={disabled} onChange={(value) => updateDomains(updateAt(domains, domainIndex, { name: value }))} /></label>
            <label className="wizard-field"><span className="wizard-field__label">稳定 ID</span><Input value={String(domain.stableId ?? '')} disabled={disabled} onChange={(value) => updateDomains(updateAt(domains, domainIndex, { stableId: value }))} /></label>
          </div>
          <div className="document-editor__subhead"><strong>方向与配额</strong>{addButton('添加方向', () => updateDomains(updateAt(domains, domainIndex, { directions: [...directions, { stableId: `direction-${directions.length + 1}`, name: '', quota: 1, source: 'manual' }] })), disabled)}</div>
          {directions.map((direction, directionIndex) => <div className="document-editor__row" key={`${String(direction.stableId)}-${directionIndex}`}>
            <Input aria-label="方向名称" placeholder="方向名称" value={String(direction.name ?? '')} disabled={disabled} onChange={(value) => updateDomains(updateAt(domains, domainIndex, { directions: updateAt(directions, directionIndex, { name: value }) }))} />
            <Input aria-label="方向稳定 ID" placeholder="稳定 ID" value={String(direction.stableId ?? '')} disabled={disabled} onChange={(value) => updateDomains(updateAt(domains, domainIndex, { directions: updateAt(directions, directionIndex, { stableId: value }) }))} />
            <Input aria-label="计划数量" type="number" min={1} value={String(direction.quota ?? 1)} disabled={disabled} onChange={(value) => updateDomains(updateAt(domains, domainIndex, { directions: updateAt(directions, directionIndex, { quota: Number(value) || 0 }) }))} />
            <Input aria-label="来源说明" placeholder="来源说明（可选）" value={String(direction.source ?? '')} disabled={disabled} onChange={(value) => updateDomains(updateAt(domains, domainIndex, { directions: updateAt(directions, directionIndex, { source: value }) }))} />
            <Button type="tertiary" icon={<Trash2 size={14} />} aria-label="删除方向" disabled={disabled || directions.length <= 1} onClick={() => updateDomains(updateAt(domains, domainIndex, { directions: removeAt(directions, directionIndex) }))} />
          </div>)}
        </Card>
      })}
      <div className="document-editor__actions">{addButton('添加领域', () => updateDomains([...domains, { stableId: `domain-${domains.length + 1}`, name: '', directions: [{ stableId: 'direction-1', name: '', quota: 1, source: 'manual' }] }]), disabled)}</div>
    </div>
  )
}

export function StandardPayloadEditor({ payload, disabled, onChange }: { payload: Record<string, unknown>; disabled: boolean; onChange: (payload: Record<string, unknown>) => void }) {
  const steps = asArray(payload.steps)
  const updateSteps = (next: Array<Record<string, unknown>>) => onChange({ ...payload, schemaVersion: 'standard.v1', steps: next.map((step, index) => ({ ...step, order: index + 1 })) })
  const move = (index: number, direction: -1 | 1) => {
    const target = index + direction
    if (target < 0 || target >= steps.length) return
    const next = [...steps]
    const [item] = next.splice(index, 1)
    next.splice(target, 0, item)
    updateSteps(next)
  }
  return <div className="document-editor document-editor--standard">
    <div className="document-editor__intro"><Text strong>思维步骤编辑器</Text><Text type="tertiary" size="small">每一步都要写清“做什么”和“怎么判断完成”，否则生产过程无法自查。</Text></div>
    {steps.map((step, index) => <Card className="document-editor__section" key={`${String(step.id)}-${index}`} bodyStyle={{ padding: 14 }}>
      <div className="document-editor__section-head"><strong>第 {index + 1} 步</strong><div className="document-editor__icon-actions"><Button type="tertiary" icon={<ChevronUp size={14} />} aria-label="上移步骤" disabled={disabled || index === 0} onClick={() => move(index, -1)} /><Button type="tertiary" icon={<ChevronDown size={14} />} aria-label="下移步骤" disabled={disabled || index === steps.length - 1} onClick={() => move(index, 1)} /><Button type="tertiary" icon={<Trash2 size={14} />} aria-label="删除步骤" disabled={disabled || steps.length <= 1} onClick={() => updateSteps(removeAt(steps, index))} /></div></div>
      <div className="document-editor__grid document-editor__grid--two">
        <label className="wizard-field"><span className="wizard-field__label">步骤 ID</span><Input value={String(step.id ?? '')} disabled={disabled} onChange={(value) => updateSteps(updateAt(steps, index, { id: value }))} /></label>
        <label className="wizard-field"><span className="wizard-field__label">步骤名称</span><Input value={String(step.title ?? '')} disabled={disabled} onChange={(value) => updateSteps(updateAt(steps, index, { title: value }))} /></label>
      </div>
      <label className="wizard-field"><span className="wizard-field__label">具体做法</span><TextArea value={String(step.detail ?? '')} disabled={disabled} autosize={{ minRows: 2, maxRows: 5 }} onChange={(value) => updateSteps(updateAt(steps, index, { detail: value }))} /></label>
      <label className="wizard-field"><span className="wizard-field__label">完成检查点 *</span><TextArea value={String(step.checkpoint ?? '')} disabled={disabled} autosize={{ minRows: 2, maxRows: 5 }} onChange={(value) => updateSteps(updateAt(steps, index, { checkpoint: value }))} placeholder="例如：每个结论都有对应证据，未知项明确标记" /></label>
    </Card>)}
    <div className="document-editor__actions">{addButton('添加步骤', () => updateSteps([...steps, { id: `step-${steps.length + 1}`, title: '', detail: '', checkpoint: '', order: steps.length + 1 }]), disabled)}</div>
  </div>
}

const MATCH_OPTIONS = [{ value: 'contains', label: '包含关键词' }, { value: 'regex', label: '正则表达式' }, { value: 'field_check', label: '字段检查' }, { value: 'structure', label: '结构检查' }]
const SEVERITY_OPTIONS = [{ value: 'info', label: '提示' }, { value: 'warning', label: '警告' }, { value: 'error', label: '错误' }]
const ACTION_OPTIONS = [{ value: 'review', label: '进入人工检查' }, { value: 'suggest_quarantine', label: '建议隔离，等待人工确认' }]

export function QualityPolicyPayloadEditor({ payload, disabled, onChange }: { payload: Record<string, unknown>; disabled: boolean; onChange: (payload: Record<string, unknown>) => void }) {
  const rules = asArray(payload.rules)
  const updateRules = (next: Array<Record<string, unknown>>) => onChange({ ...payload, schemaVersion: 'quality_policy.v1', rules: next })
  return <div className="document-editor document-editor--rules">
    <div className="document-editor__intro"><Text strong>规则策略编辑器</Text><Text type="tertiary" size="small">规则只提出建议或把内容送入人工检查，不会悄悄替你修改内容。</Text></div>
    {rules.map((rule, index) => <Card className="document-editor__section" key={`${String(rule.id)}-${index}`} bodyStyle={{ padding: 14 }}>
      <div className="document-editor__section-head"><strong>规则 {index + 1}</strong><Button type="tertiary" icon={<Trash2 size={14} />} aria-label="删除规则" disabled={disabled || rules.length <= 1} onClick={() => updateRules(removeAt(rules, index))} /></div>
      <div className="document-editor__grid document-editor__grid--three">
        <label className="wizard-field"><span className="wizard-field__label">规则 ID</span><Input value={String(rule.id ?? '')} disabled={disabled} onChange={(value) => updateRules(updateAt(rules, index, { id: value }))} /></label>
        <label className="wizard-field"><span className="wizard-field__label">规则名称</span><Input value={String(rule.name ?? '')} disabled={disabled} onChange={(value) => updateRules(updateAt(rules, index, { name: value }))} /></label>
        <label className="wizard-field"><span className="wizard-field__label">检查方式</span><Select value={String(rule.matchType ?? '')} disabled={disabled} optionList={MATCH_OPTIONS} onChange={(value) => updateRules(updateAt(rules, index, { matchType: String(value) }))} /></label>
      </div>
      <div className="document-editor__grid document-editor__grid--three">
        <label className="wizard-field"><span className="wizard-field__label">检查字段</span><Input value={String(rule.field ?? '')} disabled={disabled} onChange={(value) => updateRules(updateAt(rules, index, { field: value }))} /></label>
        <label className="wizard-field"><span className="wizard-field__label">严重程度</span><Select value={String(rule.severity ?? '')} disabled={disabled} optionList={SEVERITY_OPTIONS} onChange={(value) => updateRules(updateAt(rules, index, { severity: String(value) }))} /></label>
        <label className="wizard-field"><span className="wizard-field__label">命中后的建议</span><Select value={String(rule.suggestedAction ?? '')} disabled={disabled} optionList={ACTION_OPTIONS} onChange={(value) => updateRules(updateAt(rules, index, { suggestedAction: String(value) }))} /></label>
      </div>
      <label className="wizard-field"><span className="wizard-field__label">表达式</span><Input value={String(rule.expression ?? '')} disabled={disabled} onChange={(value) => updateRules(updateAt(rules, index, { expression: value }))} placeholder="包含匹配填文本；正则匹配填表达式" /></label>
      <label className="wizard-field"><span className="wizard-field__label">关键词（逗号分隔）</span><Input value={Array.isArray(rule.keywords) ? rule.keywords.map(String).join(', ') : ''} disabled={disabled} onChange={(value) => updateRules(updateAt(rules, index, { keywords: value.split(',').map((item) => item.trim()).filter(Boolean) }))} /></label>
    </Card>)}
    <div className="document-editor__actions">{addButton('添加规则', () => updateRules([...rules, { id: `rule-${rules.length + 1}`, name: '', matchType: 'contains', expression: '', field: 'answer', severity: 'warning', suggestedAction: 'review', keywords: [] }]), disabled)}</div>
  </div>
}

const FORMAT_OPTIONS = [{ value: 'jsonl', label: 'JSONL' }, { value: 'alpaca', label: 'Alpaca JSON' }, { value: 'csv', label: 'CSV' }]

export function MappingPayloadEditor({ payload, disabled, onChange }: { payload: Record<string, unknown>; disabled: boolean; onChange: (payload: Record<string, unknown>) => void }) {
  const fields = asArray(payload.fields)
  const updateFields = (next: Array<Record<string, unknown>>) => onChange({ ...payload, schemaVersion: 'mapping.v1', fields: next })
  return <div className="document-editor document-editor--mapping">
    <div className="document-editor__intro"><Text strong>交付映射编辑器</Text><Text type="tertiary" size="small">把内部内容字段映射到交付文件字段。必需字段缺失会在发布前被明确拦截。</Text></div>
    <label className="wizard-field"><span className="wizard-field__label">交付格式</span><Select value={String(payload.format ?? 'jsonl')} disabled={disabled} optionList={FORMAT_OPTIONS} onChange={(value) => onChange({ ...payload, schemaVersion: 'mapping.v1', format: String(value) })} /></label>
    <div className="document-editor__subhead"><strong>字段映射</strong>{addButton('添加字段', () => updateFields([...fields, { targetField: '', sourceField: '', required: false }]), disabled)}</div>
    {fields.length === 0 ? <Empty description="还没有字段映射，请添加至少一个输出字段。" /> : fields.map((field, index) => <div className="document-editor__row document-editor__row--mapping" key={`${String(field.targetField)}-${index}`}>
      <Input aria-label="交付字段" placeholder="交付字段" value={String(field.targetField ?? '')} disabled={disabled} onChange={(value) => updateFields(updateAt(fields, index, { targetField: value }))} />
      <Input aria-label="来源字段" placeholder="来源字段" value={String(field.sourceField ?? '')} disabled={disabled} onChange={(value) => updateFields(updateAt(fields, index, { sourceField: value }))} />
      <label className="document-editor__checkbox"><input type="checkbox" checked={Boolean(field.required)} disabled={disabled} onChange={(event) => updateFields(updateAt(fields, index, { required: event.target.checked }))} /> 必填</label>
      <Button type="tertiary" icon={<Trash2 size={14} />} aria-label="删除字段" disabled={disabled || fields.length <= 1} onClick={() => updateFields(removeAt(fields, index))} />
    </div>)}
  </div>
}

export function CopyVersionButton({ state, disabled }: { state: DocumentEditorState; disabled?: boolean }) {
  const [, setSearchParams] = useSearchParams()
  if (!state.current) return null
  return <Button icon={<Copy size={14} />} disabled={disabled || !state.canEdit} onClick={() => {
    state.copyCurrentToDraft()
    setSearchParams((params) => {
      params.delete('version')
      return params
    })
  }}>复制当前版本为草稿</Button>
}
