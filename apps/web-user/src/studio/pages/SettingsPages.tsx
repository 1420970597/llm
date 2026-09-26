import { useCallback, useEffect, useState } from 'react'
import { useNavigate } from 'react-router-dom'
import { Button, Card, Empty, Input, InputNumber, Modal, Select, Spin, Tag, Toast, Typography } from '@douyinfe/semi-ui'
import { Pencil, PlugZap, Plus, Power, ShieldCheck, Users } from 'lucide-react'
import { authApi, consoleApi } from '../../lib/api'
import type { Provider } from '../../lib/api'
import { settingsApi } from '../../lib/api/studio'
import type {
  ConnectionOptions,
  ConnectionProviderOption,
  ProjectCapabilities,
  ProjectIdentity,
  ProjectMemberRecord,
  TypedEnvelope,
  WorkspaceMemberRecord,
} from '../../lib/api/studio'
import { APP_BUILD_TIME, APP_VERSION, versionSummary } from '../../buildInfo'

/**
 * 设置页（Issue #160 T28）：连接与存储选项、团队与角色、帮助。
 *
 * 两条与契约直接相关的界面决定：
 *
 *  1. **连接页只读**。普通用户看到的是「有哪些可用连接」与掩码标识，
 *     不是管理员的密钥接口（T28：不拿管理员密钥接口当公共选项列表）。
 *     新增/修改与「测试连接」在管理员设置页，且**测试与保存分离**。
 *  2. **团队页把两层职责画清楚**：工作区管理员管成员与连接，项目 owner 管
 *     项目成员。界面上分两块并用文字写明，避免「我加了成员为什么他看不到项目」。
 */

// ---------------------------------------------------------------------------
// 连接与存储（S02）
// ---------------------------------------------------------------------------

/**
 * 模型连接的编辑草稿（issue #197 第 7、8 条）。
 *
 * `apiKey` 为**空串表示「不修改密钥」**：管理接口的语义是「不回显、
 * 传空则保留旧值」。用一个独立的 `hasNewKey` 标记，避免把「用户清空了
 * 输入框」误判成「用户想清空密钥」。
 */
type ProviderDraft = {
  id: number
  name: string
  baseUrl: string
  model: string
  providerType: string
  reasoningEffort: string
  maxConcurrency: number
  isActive: boolean
  apiKey: string
  apiKeyMasked: string
}

const EMPTY_PROVIDER_DRAFT: ProviderDraft = {
  id: 0,
  name: '',
  baseUrl: '',
  model: '',
  providerType: 'openai-compatible',
  reasoningEffort: '',
  maxConcurrency: 4,
  isActive: true,
  apiKey: '',
  apiKeyMasked: '',
}

function draftFromProvider(provider: Provider): ProviderDraft {
  return {
    id: provider.id,
    name: provider.name ?? '',
    baseUrl: provider.baseUrl ?? '',
    model: provider.model ?? '',
    providerType: provider.providerType || 'openai-compatible',
    reasoningEffort: provider.reasoningEffort ?? '',
    maxConcurrency: provider.maxConcurrency > 0 ? provider.maxConcurrency : 4,
    isActive: provider.isActive,
    // 密钥永不回显：编辑时留空即保留原密钥。
    apiKey: '',
    apiKeyMasked: provider.apiKeyMasked ?? '',
  }
}

export function ConnectionsPage() {
  const { Title, Text } = Typography
  const [options, setOptions] = useState<ConnectionOptions | null>(null)
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)
  const [isAdmin, setIsAdmin] = useState(false)

  /**
   * 连接管理在**同一张表内**用抽屉/弹窗完成（issue #197 第 7 条）：
   * 以前「新增或编辑」会跳到旧的 `/console/admin/providers`，
   * 用户被带离 Atelier 设置页且找不到回来的路。
   */
  const [draft, setDraft] = useState<ProviderDraft | null>(null)
  const [saving, setSaving] = useState(false)
  const [testing, setTesting] = useState(false)
  const [testResult, setTestResult] = useState<string | null>(null)
  const [rowBusy, setRowBusy] = useState<number | null>(null)

  const load = useCallback(async () => {
    setLoading(true)
    try {
      const [response, identity] = await Promise.all([
        settingsApi.connectionOptions(),
        authApi.me(),
      ])
      setOptions(response)
      setIsAdmin(identity.user.role === 'admin')
    } catch (loadError) {
      setError(loadError instanceof Error ? loadError.message : '加载连接选项失败')
    } finally {
      setLoading(false)
    }
  }, [])

  useEffect(() => {
    void load()
  }, [load])

  /**
   * 保存连接。密钥只在用户真的填写了新值时才提交：
   * 空串会让服务端沿用旧值，因此这里显式省略该字段。
   */
  const saveDraft = useCallback(async () => {
    if (!draft) return
    if (draft.name.trim() === '') {
      setError('连接名称不能为空。')
      return
    }
    if (draft.baseUrl.trim() === '') {
      setError('接入地址（Base URL）不能为空。')
      return
    }
    setSaving(true)
    setError(null)
    try {
      const payload: Partial<Provider> & { apiKey?: string } = {
        id: draft.id > 0 ? draft.id : undefined,
        name: draft.name.trim(),
        baseUrl: draft.baseUrl.trim(),
        model: draft.model.trim(),
        providerType: draft.providerType.trim() || 'openai-compatible',
        reasoningEffort: draft.reasoningEffort.trim(),
        maxConcurrency: draft.maxConcurrency,
        isActive: draft.isActive,
      }
      if (draft.apiKey.trim() !== '') {
        payload.apiKey = draft.apiKey.trim()
      }
      await consoleApi.saveProvider(payload)
      setDraft(null)
      setTestResult(null)
      Toast.success(draft.id > 0 ? '连接已更新。' : '连接已创建。')
      await load()
    } catch (saveError) {
      setError(saveError instanceof Error ? saveError.message : '保存连接失败')
    } finally {
      setSaving(false)
    }
  }, [draft, load])

  /** 测试连通性（与保存分离：测试失败不应阻止用户保存一个刚配置好的连接）。 */
  const testDraft = useCallback(async () => {
    if (!draft) return
    setTesting(true)
    setTestResult(null)
    try {
      const result = await consoleApi.testProviderConnectivity({
        id: draft.id > 0 ? draft.id : undefined,
        name: draft.name.trim(),
        baseUrl: draft.baseUrl.trim(),
        model: draft.model.trim(),
        providerType: draft.providerType.trim() || 'openai-compatible',
        ...(draft.apiKey.trim() !== '' ? { apiKey: draft.apiKey.trim() } : {}),
      })
      setTestResult(
        result.ok
          ? `连通成功（HTTP ${result.statusCode}，${result.latencyMs}ms${result.modelFound ? '，模型可用' : '，但未在模型列表里找到该模型'}）`
          : `连通失败（HTTP ${result.statusCode}，${result.latencyMs}ms）：${result.message}`,
      )
    } catch (testError) {
      setTestResult(testError instanceof Error ? testError.message : '测试连接失败')
    } finally {
      setTesting(false)
    }
  }, [draft])

  /**
   * 行内启用/停用：列表项的直接操作，不必进编辑弹窗（issue #197 第 8 条）。
   *
   * 这里必须回读**完整记录**再提交：管理接口的语义是整行覆盖，
   * 只传展示字段会把 baseUrl / 并发上限等字段清空（「启用一下把连接弄坏」）。
   */
  const toggleActive = useCallback(
    async (provider: ConnectionProviderOption) => {
      setRowBusy(provider.id)
      setError(null)
      try {
        const all = await consoleApi.listProviders()
        const full = all.find((item) => item.id === provider.id)
        if (!full) {
          setError('该连接已不存在（可能刚被其它管理员删除），列表已刷新。')
          await load()
          return
        }
        const nextDraft = draftFromProvider(full)
        await consoleApi.saveProvider({
          id: nextDraft.id,
          name: nextDraft.name,
          baseUrl: nextDraft.baseUrl,
          model: nextDraft.model,
          providerType: nextDraft.providerType,
          reasoningEffort: nextDraft.reasoningEffort,
          maxConcurrency: nextDraft.maxConcurrency,
          isActive: !provider.isActive,
        })
        Toast.success(provider.isActive ? `已停用 ${provider.name}` : `已启用 ${provider.name}`)
        await load()
      } catch (toggleError) {
        setError(toggleError instanceof Error ? toggleError.message : '切换连接状态失败')
      } finally {
        setRowBusy(null)
      }
    },
    [load],
  )

  return (
    <div className="console-page" data-studio-page="settings-connections">
      <div className="console-page__header">
        <div>
          <Title heading={4} className="!mb-1">
            连接与存储
          </Title>
          <Text type="tertiary">可用的模型连接与结果存储。</Text>
        </div>
        {isAdmin ? (
          <Button
            theme="solid"
            type="primary"
            icon={<Plus size={14} />}
            onClick={() => {
              // issue #197 第 7 条：不再跳转到旧兼容控制台，就在本页打开表单。
              setError(null)
              setTestResult(null)
              setDraft({ ...EMPTY_PROVIDER_DRAFT })
            }}
            data-connection-admin-action="true"
          >
            新增模型连接
          </Button>
        ) : null}
      </div>

      {error ? (
        <Card className="console-card mb-3" bodyStyle={{ padding: 14 }} data-connections-error="true">
          <Text type="danger">{error}</Text>
        </Card>
      ) : null}

      {loading ? (
        <div className="flex justify-center py-10">
          <Spin tip="正在加载连接选项" />
        </div>
      ) : options ? (
        <>
          <Card className="console-card mb-3" bodyStyle={{ padding: 14 }} data-connections-providers="true">
            <div className="flex flex-wrap items-start justify-between gap-3 mb-2">
              <div>
                <Text strong className="block">模型连接</Text>
                <Text type="tertiary" size="small">蓝图和质量实验只能选择已启用的连接；密钥不会在这里回显。</Text>
              </div>
              {isAdmin ? (
                <Button
                  size="small"
                  icon={<Plus size={14} />}
                  onClick={() => {
                    setError(null)
                    setTestResult(null)
                    setDraft({ ...EMPTY_PROVIDER_DRAFT })
                  }}
                  data-connection-manage="true"
                >
                  新增连接
                </Button>
              ) : null}
            </div>
            {options.providers.length === 0 ? (
              <Empty description="还没有可用的模型连接。" />
            ) : (
              <div className="comparison-table">
                <div className="comparison-row comparison-row--head">
                  <span>名称</span>
                  <span>模型</span>
                  <span>类型</span>
                  <span>密钥标识</span>
                  <span>状态</span>
                  {isAdmin ? <span>操作</span> : null}
                </div>
                {options.providers.map((provider) => (
                  <div key={provider.id} className="comparison-row" data-connection-id={provider.id}>
                    {/* data-label 是窄屏卡片布局的字段名（issue #194）：
                        390px 下表格无法横向展开时，如果只把 5 个单元格倒进两列，
                        用户看到的是「表头与数据行错位混合」。这里让每个单元格
                        自带字段名，行变成卡片式列表（表头整行隐藏）。 */}
                    <span data-label="名称">{provider.name}</span>
                    <span data-label="模型">{provider.model}</span>
                    <span data-label="类型">{provider.providerType}</span>
                    <span data-label="密钥标识">{provider.apiKeyMasked || '—'}</span>
                    <span data-label="状态">
                      <Tag size="small" color={provider.isActive ? 'green' : 'grey'}>
                        {provider.isActive ? '启用' : '停用'}
                      </Tag>
                    </span>
                    {/* issue #197 第 8 条：列表项必须有**元素级**编辑按钮。
                        以前 11 行连接的行内按钮数是 0，用户只能去旧控制台。 */}
                    {isAdmin ? (
                      <span data-label="操作" className="connection-row-actions">
                        <Button
                          size="small"
                          theme="borderless"
                          icon={<Pencil size={13} />}
                          loading={rowBusy === provider.id}
                          onClick={() => {
                            setError(null)
                            setTestResult(null)
                            // 编辑需要完整字段（含 baseUrl），而选项端点只给展示字段；
                            // 因此这里按 id 取完整记录，而不是拿展示字段拼一个残缺草稿。
                            void (async () => {
                              setRowBusy(provider.id)
                              try {
                                // 选项端点只返回展示字段（无 baseUrl / 并发上限），
                                // 而编辑表单需要完整记录。**拿不到就报错**，不用展示
                                // 字段拼一个残缺草稿 —— 那样保存会把 baseUrl 清空。
                                const all = await consoleApi.listProviders()
                                const full = all.find((item) => item.id === provider.id)
                                if (!full) {
                                  setError('该连接已不存在（可能刚被其它管理员删除），列表已刷新。')
                                  await load()
                                  return
                                }
                                setDraft(draftFromProvider(full))
                              } catch (loadError) {
                                setError(loadError instanceof Error ? loadError.message : '读取连接详情失败')
                              } finally {
                                setRowBusy(null)
                              }
                            })()
                          }}
                          data-connection-edit={provider.id}
                        >
                          编辑
                        </Button>
                        <Button
                          size="small"
                          theme="borderless"
                          icon={<Power size={13} />}
                          loading={rowBusy === provider.id}
                          onClick={() => void toggleActive(provider)}
                          data-connection-toggle={provider.id}
                        >
                          {provider.isActive ? '停用' : '启用'}
                        </Button>
                      </span>
                    ) : null}
                  </div>
                ))}
              </div>
            )}
          </Card>

          <Card className="console-card mb-3" bodyStyle={{ padding: 14 }} data-connections-storage="true">
            <Text strong className="block mb-2">
              结果存储
            </Text>
            {options.storageProfiles.length === 0 ? (
              <Empty description="还没有可用的结果存储。" />
            ) : (
              <div className="comparison-table">
                <div className="comparison-row comparison-row--head">
                  <span>名称</span>
                  <span>类型</span>
                  <span>Bucket</span>
                  <span>密钥标识</span>
                  <span>默认</span>
                </div>
                {options.storageProfiles.map((profile) => (
                  <div key={profile.id} className="comparison-row" data-storage-id={profile.id}>
                    <span data-label="名称">{profile.name}</span>
                    <span data-label="类型">{profile.provider}</span>
                    <span data-label="Bucket">{profile.bucket}</span>
                    <span data-label="密钥标识">{profile.secretKeyMasked || '—'}</span>
                    <span data-label="默认">
                      {profile.isDefault ? (
                        <Tag size="small" color="blue">
                          默认
                        </Tag>
                      ) : (
                        <Text type="tertiary" size="small">
                          —
                        </Text>
                      )}
                    </span>
                  </div>
                ))}
              </div>
            )}
          </Card>

        </>
      ) : null}

      {/*
        连接表单：与列表**同一个页面**（issue #197 第 7 条）。
        用 Modal 而不是路由跳转，是因为用户的心理模型是「在这一行上改」，
        跳页会丢失上下文（旧控制台还不能一键返回）。
      */}
      <Modal
        visible={draft !== null}
        title={draft && draft.id > 0 ? `编辑连接：${draft.name || `#${draft.id}`}` : '新增模型连接'}
        onCancel={() => {
          setDraft(null)
          setTestResult(null)
        }}
        onOk={() => void saveDraft()}
        okText="保存"
        cancelText="取消"
        confirmLoading={saving}
        width={620}
      >
        {draft ? (
          <div className="wizard-grid" data-connection-form="true">
            <div className="wizard-field">
              <label className="wizard-field__label" htmlFor="connection-name">名称</label>
              <Input
                id="connection-name"
                value={draft.name}
                onChange={(value) => setDraft({ ...draft, name: value })}
                placeholder="例如 主裁判模型"
              />
            </div>
            <div className="wizard-field">
              <label className="wizard-field__label" htmlFor="connection-base-url">接入地址（Base URL）</label>
              <Input
                id="connection-base-url"
                value={draft.baseUrl}
                onChange={(value) => setDraft({ ...draft, baseUrl: value })}
                placeholder="https://api.example.com/v1"
              />
            </div>
            <div className="wizard-field">
              <label className="wizard-field__label" htmlFor="connection-model">模型</label>
              <Input
                id="connection-model"
                value={draft.model}
                onChange={(value) => setDraft({ ...draft, model: value })}
                placeholder="模型名（留空则用供应商默认）"
              />
            </div>
            <div className="wizard-field">
              <label className="wizard-field__label" htmlFor="connection-type">类型</label>
              <Input
                id="connection-type"
                value={draft.providerType}
                onChange={(value) => setDraft({ ...draft, providerType: value })}
                placeholder="openai-compatible"
              />
            </div>
            <div className="wizard-field">
              <label className="wizard-field__label" htmlFor="connection-concurrency">最大并发</label>
              <InputNumber
                id="connection-concurrency"
                min={1}
                max={32}
                value={draft.maxConcurrency}
                onChange={(value) => setDraft({ ...draft, maxConcurrency: Number(value ?? 1) })}
                style={{ width: '100%' }}
              />
            </div>
            <div className="wizard-field">
              <label className="wizard-field__label" htmlFor="connection-key">
                密钥{draft.id > 0 ? '（留空表示不修改）' : ''}
              </label>
              <Input
                id="connection-key"
                mode="password"
                value={draft.apiKey}
                onChange={(value) => setDraft({ ...draft, apiKey: value })}
                placeholder={draft.id > 0 ? `当前密钥 ${draft.apiKeyMasked || '未设置'}，留空保留` : '粘贴密钥'}
              />
            </div>
            <div className="wizard-field">
              <label className="wizard-field__label" htmlFor="connection-active">状态</label>
              <Select
                id="connection-active"
                value={draft.isActive ? 'active' : 'inactive'}
                style={{ width: '100%' }}
                optionList={[
                  { value: 'active', label: '启用（可被蓝图与实验选择）' },
                  { value: 'inactive', label: '停用（保留配置但不出现在选择列表）' },
                ]}
                onChange={(value) => setDraft({ ...draft, isActive: value === 'active' })}
              />
            </div>
            <Text type="tertiary" size="small" className="block">
              密钥只写不回显：保存后这里只会显示掩码标识，服务端不会把明文传回浏览器。
            </Text>
            {/* 测试与保存分离：测试失败不应阻止保存一个刚配好的连接。 */}
            <div className="flex flex-wrap items-center gap-2">
              <Button
                size="small"
                icon={<PlugZap size={13} />}
                loading={testing}
                onClick={() => void testDraft()}
                data-connection-test="true"
              >
                测试连通性
              </Button>
              {testResult ? (
                <Text size="small" type={testResult.startsWith('连通成功') ? 'success' : 'danger'}>
                  {testResult}
                </Text>
              ) : null}
            </div>
          </div>
        ) : null}
      </Modal>
    </div>
  )
}

// ---------------------------------------------------------------------------
// 团队与角色（S03）
// ---------------------------------------------------------------------------

export function parseManageableProjectOptions(items: unknown): Array<{ id: number; name: string; canManageMembers: true }> {
  if (!Array.isArray(items)) return []
  return items.flatMap((value: unknown) => {
    if (!value || typeof value !== 'object') return []
    const item = value as Record<string, unknown>
    const data = item.data && typeof item.data === 'object' ? item.data as Record<string, unknown> : null
    const capabilities = item.capabilities && typeof item.capabilities === 'object'
      ? item.capabilities as Record<string, unknown>
      : null
    if (!data) return []
    const id = data.id
    const envelopeID = typeof item.id === 'string' ? item.id.match(/^p_(\d+)$/)?.[1] : undefined
    if (
      typeof id !== 'number' ||
      !Number.isSafeInteger(id) ||
      id <= 0 ||
      envelopeID !== String(id) ||
      capabilities?.canManageMembers !== true
    ) return []
    return [{ id, name: typeof data.name === 'string' && data.name ? data.name : `项目 #${id}`, canManageMembers: true as const }]
  })
}

export function isWorkspaceAdminMember(
  userId: number | null,
  members: ReadonlyArray<{ userId: number; role: string }>,
): boolean {
  return Number.isSafeInteger(userId) && (userId ?? 0) > 0 && members.some((member) => member.userId === userId && member.role === 'admin')
}

export function TeamPage() {
  const navigate = useNavigate()
  const { Title, Text } = Typography
  const [members, setMembers] = useState<WorkspaceMemberRecord[]>([])
  const [email, setEmail] = useState('')
  const [role, setRole] = useState('member')
  // 直接建号（issue #197 第 15 条）：管理员设初始密码，账号可立即登录。
  const [createEmail, setCreateEmail] = useState('')
  const [createPassword, setCreatePassword] = useState('')
  const [createRole, setCreateRole] = useState<'member' | 'admin'>('member')
  const [createUserRole, setCreateUserRole] = useState<'user' | 'admin'>('user')
  const [loading, setLoading] = useState(true)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [notice, setNotice] = useState<string | null>(null)
  const [currentUserId, setCurrentUserId] = useState<number | null>(null)
  const [canManageWorkspace, setCanManageWorkspace] = useState(false)
  const [projects, setProjects] = useState<Array<{ id: number; name: string; canManageMembers: boolean }>>([])
  const [selectedProject, setSelectedProject] = useState('')
  const [projectMembers, setProjectMembers] = useState<ProjectMemberRecord[]>([])
  const [projectBusy, setProjectBusy] = useState(false)
  const [projectMembersLoading, setProjectMembersLoading] = useState(false)
  const [projectMemberEmail, setProjectMemberEmail] = useState('')
  const [projectMemberRole, setProjectMemberRole] = useState('viewer')
  const [projectMemberReason, setProjectMemberReason] = useState('')
  const [projectListError, setProjectListError] = useState<string | null>(null)

  const load = useCallback(async () => {
    setLoading(true)
    setError(null)
    setCanManageWorkspace(false)
    const [membersResult, currentUserResult] = await Promise.allSettled([
      settingsApi.workspaceMembers(),
      authApi.me(),
    ])
    if (membersResult.status === 'rejected') {
      setMembers([])
      setCurrentUserId(null)
      setError(membersResult.reason instanceof Error ? membersResult.reason.message : '加载成员失败')
      setLoading(false)
      return
    }

    const workspaceMembers = membersResult.value.items ?? []
    setMembers(workspaceMembers)
    if (currentUserResult.status === 'fulfilled') {
      const userId = currentUserResult.value.user.id
      setCurrentUserId(userId)
      setCanManageWorkspace(isWorkspaceAdminMember(userId, workspaceMembers))
    } else {
      setCurrentUserId(null)
      setError('无法确认当前账号的工作区角色；成员管理操作已隐藏。')
    }
    setLoading(false)
  }, [])

  useEffect(() => {
    void load()
  }, [load])

  useEffect(() => {
    let cancelled = false
    void (async () => {
      try {
        type ProjectListItem = TypedEnvelope<ProjectIdentity, ProjectCapabilities>
        const collected: ProjectListItem[] = []
        const seenCursors = new Set<string>()
        let cursor = ''
        do {
          const response = await settingsApi.projects({ limit: 50, ...(cursor ? { cursor } : {}) })
          collected.push(...(response.items ?? []))
          cursor = response.nextCursor ?? ''
        } while (cursor && !seenCursors.has(cursor) && seenCursors.add(cursor))

        const manageable = parseManageableProjectOptions(collected)
        if (!cancelled) {
          setProjects(manageable)
          setSelectedProject((selected) => manageable.some((project) => String(project.id) === selected) ? selected : '')
          setProjectMembers([])
          setProjectListError(null)
        }
      } catch (loadError) {
        // 项目列表拿不到不影响工作区成员管理（它是辅助信息）。
        if (!cancelled) {
          setProjects([])
          setSelectedProject('')
          setProjectMembers([])
          setProjectListError(loadError instanceof Error ? loadError.message : '加载可管理项目失败')
        }
      }
    })()
    return () => {
      cancelled = true
    }
  }, [])

  const loadProjectMembers = useCallback(async (projectID: number) => {
    const project = projects.find((item) => item.id === projectID)
    if (projectID <= 0 || project?.canManageMembers !== true) {
      setProjectMembers([])
      setProjectMembersLoading(false)
      return
    }
    setError(null)
    setProjectMembersLoading(true)
    try {
      const response = await settingsApi.projectMembers(projectID)
      setProjectMembers(response.items ?? [])
    } catch (loadError) {
      setError(loadError instanceof Error ? loadError.message : '加载项目成员失败')
      setProjectMembers([])
    } finally {
      setProjectMembersLoading(false)
    }
  }, [projects])

  const upsertProjectMember = useCallback(async () => {
    const projectID = Number(selectedProject)
    const project = projects.find((item) => item.id === projectID)
    if (project?.canManageMembers !== true) return
    const targetEmail = projectMemberEmail.trim()
    if (!targetEmail) {
      setError('请填写要添加或更新的已有账号邮箱。')
      return
    }
    if (!projectMemberReason.trim()) {
      setError('请填写成员变更原因，此信息会写入项目审计记录。')
      return
    }

    setProjectBusy(true)
    setError(null)
    setNotice(null)
    try {
      await settingsApi.upsertProjectMember(projectID, {
        email: targetEmail,
        role: projectMemberRole as 'owner' | 'reviewer' | 'viewer',
        reason: projectMemberReason.trim(),
      })
      setProjectMemberEmail('')
      setProjectMemberReason('')
      setNotice('项目成员与角色已更新，变更原因已写入审计记录。')
      await loadProjectMembers(projectID)
    } catch (upsertError) {
      setError(upsertError instanceof Error ? upsertError.message : '更新项目成员失败')
    } finally {
      setProjectBusy(false)
    }
  }, [loadProjectMembers, projectMemberEmail, projectMemberReason, projectMemberRole, projects, selectedProject])

  const removeProjectMember = useCallback(async (projectID: number, userID: number) => {
    const project = projects.find((item) => item.id === projectID)
    if (project?.canManageMembers !== true) return
    if (!projectMemberReason.trim()) {
      setError('请填写成员移除原因，此信息会写入项目审计记录。')
      return
    }

    setProjectBusy(true)
    setError(null)
    setNotice(null)
    try {
      await settingsApi.removeProjectMember(projectID, userID, projectMemberReason.trim())
      setProjectMemberReason('')
      setNotice('项目成员已移除，变更原因已写入审计记录。')
      await loadProjectMembers(projectID)
    } catch (removeError) {
      setError(removeError instanceof Error ? removeError.message : '移除项目成员失败')
    } finally {
      setProjectBusy(false)
    }
  }, [loadProjectMembers, projectMemberReason, projects])

  const addMember = useCallback(async () => {
    if (!canManageWorkspace) {
      setError('只有工作区管理员可以添加成员。')
      return
    }
    if (email.trim() === '') {
      setError('请填写要添加的账号邮箱（首版只能选择已有账号）')
      return
    }
    setBusy(true)
    setError(null)
    setNotice(null)
    try {
      await settingsApi.upsertWorkspaceMember({ email: email.trim(), role })
      setEmail('')
      setNotice('已添加/更新成员。工作区管理员管理成员与连接；项目内容权限由项目成员决定。')
      await load()
    } catch (addError) {
      setError(addError instanceof Error ? addError.message : '添加成员失败')
    } finally {
      setBusy(false)
    }
  }, [canManageWorkspace, email, load, role])

  /**
   * 创建账号并加入工作区（issue #197 第 15 条）。
   *
   * 与 addMember 的关键差别：这里**创建账号**，而不是「按邮箱找已有账号」。
   * 甲方明确指出不能走邮件邀请（本部署无出站邮件），因此由管理员设初始密码。
   * 前端只做「不为空 + 两次一致」这类能当场判定的校验；强度与查重以服务端为准，
   * 并把字段级错误原样展示（避免前端与服务端两套规则漂移）。
   */
  const createAccount = useCallback(async () => {
    if (!canManageWorkspace) {
      setError('只有工作区管理员可以新建账号。')
      return
    }
    if (createEmail.trim() === '') {
      setError('请填写登录用户名（邮箱格式）。')
      return
    }
    if (createPassword.length < 8) {
      setError('初始密码至少 8 位。')
      return
    }
    if (createPassword === createEmail.trim()) {
      setError('初始密码不能与登录用户名相同。')
      return
    }
    setBusy(true)
    setError(null)
    setNotice(null)
    try {
      await settingsApi.createWorkspaceMemberDirect({
        email: createEmail.trim(),
        password: createPassword,
        role: createRole,
        userRole: createUserRole,
      })
      setCreateEmail('')
      setCreatePassword('')
      setNotice(
        `已创建账号 ${createEmail.trim()} 并加入工作区。请通过安全渠道转交初始密码，并提示本人首次登录后修改。`,
      )
      await load()
    } catch (createError) {
      setError(createError instanceof Error ? createError.message : '创建账号失败')
    } finally {
      setBusy(false)
    }
  }, [canManageWorkspace, createEmail, createPassword, createRole, createUserRole, load])

  const removeMember = useCallback(
    async (userId: number) => {
      if (!canManageWorkspace) {
        setError('只有工作区管理员可以移除成员。')
        return
      }
      setBusy(true)
      setError(null)
      setNotice(null)
      try {
        await settingsApi.removeWorkspaceMember(userId)
        setNotice('已移除成员（同时清理了他在本工作区的项目成员关系）')
        await load()
      } catch (removeError) {
        setError(removeError instanceof Error ? removeError.message : '移除成员失败')
      } finally {
        setBusy(false)
      }
    },
    [canManageWorkspace, load],
  )

  return (
    <div className="console-page" data-studio-page="settings-team">
      <div className="console-page__header">
        <div>
          <Title heading={4} className="!mb-1">
            团队与角色
          </Title>
          <Text type="tertiary">
            选择工作区成员或项目成员。
          </Text>
        </div>
      </div>

      {error ? (
        <Card className="console-card mb-3" bodyStyle={{ padding: 14 }} data-team-error="true">
          <Text type="danger">{error}</Text>
        </Card>
      ) : null}
      {notice ? (
        <Card className="console-card mb-3" bodyStyle={{ padding: 14 }} data-team-notice="true">
          <Text type="success">{notice}</Text>
        </Card>
      ) : null}

      <Card className="console-card mb-3" bodyStyle={{ padding: 14 }} data-team-workspace="true">
        <Text strong className="block mb-2">
          <Users size={14} aria-hidden /> 工作区成员（{members.length}）
        </Text>
        {loading ? (
          <Spin tip="正在加载成员" />
        ) : (
          <div className="comparison-table">
            <div className="comparison-row comparison-row--head">
              <span>账号</span>
              <span>工作区角色</span>
              {canManageWorkspace ? <span>操作</span> : null}
            </div>
            {members.map((member) => (
              <div key={member.userId} className="comparison-row" data-member-id={member.userId}>
                <span>{member.email}</span>
                <span>
                  <Tag size="small" color={member.role === 'admin' ? 'violet' : 'blue'}>
                    {member.role === 'admin' ? '工作区管理员' : '成员'}
                  </Tag>
                </span>
                {canManageWorkspace ? (
                  <span>
                    <Button
                      size="small"
                      theme="borderless"
                      disabled={busy}
                      onClick={() => void removeMember(member.userId)}
                      data-team-remove={member.userId}
                    >
                      移除
                    </Button>
                  </span>
                ) : null}
              </div>
            ))}
          </div>
        )}

        {canManageWorkspace ? (
          <>
            {/* 新建账号（issue #197 第 15 条）：本部署无出站邮件，因此不用邀请链接，
                而是由管理员设初始密码，账号可立即登录。 */}
            <hr className="settings-divider" />
            <Text strong className="block mt-3" data-team-create-heading="true">
              新建账号（用户名 + 初始密码）
            </Text>
            <Text type="tertiary" size="small" className="block mb-2">
              系统不会发送邮件邀请。请把用户名与初始密码通过安全渠道转交本人。
            </Text>
            <div className="wizard-grid">
              <div className="wizard-field">
                <label className="wizard-field__label" htmlFor="team-create-email">
                  登录用户名（邮箱）
                </label>
                <Input
                  id="team-create-email"
                  value={createEmail}
                  onChange={setCreateEmail}
                  placeholder="newuser@company.com"
                  data-team-create-email="true"
                />
              </div>
              <div className="wizard-field">
                <label className="wizard-field__label" htmlFor="team-create-password">
                  初始密码（至少 8 位）
                </label>
                <Input
                  id="team-create-password"
                  mode="password"
                  value={createPassword}
                  onChange={setCreatePassword}
                  placeholder="由你设置，首次登录后建议修改"
                  data-team-create-password="true"
                />
              </div>
              <div className="wizard-field">
                <label className="wizard-field__label" htmlFor="team-create-role">
                  工作区角色
                </label>
                <Select
                  id="team-create-role"
                  value={createRole}
                  style={{ width: '100%' }}
                  optionList={[
                    { value: 'member', label: '成员（可做项目内工作）' },
                    { value: 'admin', label: '工作区管理员（可管成员与连接）' },
                  ]}
                  onChange={(value) => setCreateRole(value === 'admin' ? 'admin' : 'member')}
                />
              </div>
              <div className="wizard-field">
                <label className="wizard-field__label" htmlFor="team-create-user-role">
                  系统角色
                </label>
                <Select
                  id="team-create-user-role"
                  value={createUserRole}
                  style={{ width: '100%' }}
                  optionList={[
                    { value: 'user', label: '普通用户（仅数据项目）' },
                    { value: 'admin', label: '管理员（可进兼容控制台）' },
                  ]}
                  onChange={(value) => setCreateUserRole(value === 'admin' ? 'admin' : 'user')}
                />
              </div>
            </div>
            <div className="mt-2">
              <Button
                size="small"
                theme="solid"
                type="primary"
                icon={<Users size={14} />}
                loading={busy}
                onClick={() => void createAccount()}
                data-team-create-submit="true"
              >
                创建账号并加入
              </Button>
            </div>

            <hr className="settings-divider" />
            <Text strong className="block mt-3">
              添加已有账号
            </Text>
            <div className="wizard-grid">
              <div className="wizard-field">
                <label className="wizard-field__label" htmlFor="team-email">
                  按邮箱添加已有账号
                </label>
                <Input id="team-email" value={email} onChange={setEmail} placeholder="someone@company.com" />
              </div>
              <div className="wizard-field">
                <label className="wizard-field__label" htmlFor="team-role">
                  角色
                </label>
                <Select
                  id="team-role"
                  value={role}
                  style={{ width: '100%' }}
                  optionList={[
                    { value: 'member', label: '成员' },
                    { value: 'admin', label: '工作区管理员' },
                  ]}
                  onChange={(value) => setRole(String(value))}
                />
              </div>
            </div>
            <div className="mt-2">
              <Button
                size="small"
                theme="solid"
                icon={<ShieldCheck size={14} />}
                loading={busy}
                onClick={() => void addMember()}
                data-team-add="true"
              >
                添加/更新成员
              </Button>
            </div>
          </>
        ) : (
          <Text type="tertiary" size="small" className="block mt-3" data-team-workspace-readonly="true">
            {currentUserId === null ? '当前账号的工作区角色未确认，成员管理操作不可用。' : '当前账号是工作区成员；仅工作区管理员可以添加或移除成员。'}
          </Text>
        )}
      </Card>

      <Card className="console-card" bodyStyle={{ padding: 14 }} data-team-project="true">
        <Text strong className="block mb-2">
          项目成员
        </Text>
        <Text type="tertiary" size="small" className="block mb-2">
          项目内容权限由项目成员角色决定。
        </Text>
        <Select
          value={selectedProject || undefined}
          placeholder="选择项目查看成员"
          style={{ width: 280 }}
          aria-label="选择项目"
          optionList={projects.map((project) => ({ value: String(project.id), label: project.name }))}
          onChange={(value) => {
            if (typeof value !== 'string' && typeof value !== 'number') return
            const nextID = Number(value)
            if (!Number.isSafeInteger(nextID) || !projects.some((project) => project.id === nextID && project.canManageMembers)) return
            const next = String(nextID)
            setError(null)
            setSelectedProject(next)
            void loadProjectMembers(nextID)
          }}
        />
        {projectListError ? <Text type="danger" size="small" className="block mt-2" data-team-project-error="true">{projectListError}</Text> : null}
        {!projectListError && projects.length === 0 ? (
          <Text type="tertiary" size="small" className="block mt-2" data-team-project-empty="true">
            当前账号没有可管理成员的项目。
          </Text>
        ) : null}
        {selectedProject && projects.some((project) => String(project.id) === selectedProject && project.canManageMembers) ? (
          <div className="mt-3" data-team-project-members="true">
            <div className="wizard-grid">
              <div className="wizard-field">
                <label className="wizard-field__label" htmlFor="project-member-email">
                  成员邮箱
                </label>
                <Input
                  id="project-member-email"
                  value={projectMemberEmail}
                  onChange={setProjectMemberEmail}
                  placeholder="已有账号的邮箱"
                  disabled={projectBusy || projectMembersLoading}
                />
              </div>
              <div className="wizard-field">
                <label className="wizard-field__label" htmlFor="project-member-role">
                  项目角色
                </label>
                <Select
                  id="project-member-role"
                  value={projectMemberRole}
                  style={{ width: '100%' }}
                  optionList={[
                    { value: 'owner', label: '负责人' },
                    { value: 'reviewer', label: '审阅者' },
                    { value: 'viewer', label: '只读成员' },
                  ]}
                  onChange={(value) => setProjectMemberRole(String(value))}
                  disabled={projectBusy || projectMembersLoading}
                />
              </div>
              <div className="wizard-field">
                <label className="wizard-field__label" htmlFor="project-member-reason">
                  变更原因（写入审计）
                </label>
                <Input
                  id="project-member-reason"
                  value={projectMemberReason}
                  onChange={setProjectMemberReason}
                  placeholder="例如：负责质量复核"
                  disabled={projectBusy || projectMembersLoading}
                />
              </div>
            </div>
            <div className="mt-2">
              <Button
                size="small"
                theme="solid"
                disabled={projectBusy || projectMembersLoading || !projectMemberEmail.trim() || !projectMemberReason.trim()}
                loading={projectBusy}
                onClick={() => void upsertProjectMember()}
                data-team-project-upsert="true"
              >
                添加或更新项目成员
              </Button>
            </div>
            <div className="comparison-table mt-3" data-team-project-roster="true">
              <div className="comparison-row comparison-row--head">
                <span>用户</span>
                <span>项目角色</span>
                <span>操作</span>
              </div>
              {projectMembersLoading ? <div className="py-3"><Spin tip="正在加载项目成员" /></div> : null}
              {!projectMembersLoading && projectMembers.length === 0 ? <Empty description="此项目还没有成员。" /> : null}
              {!projectMembersLoading && projectMembers.map((member) => (
                <div key={member.userId} className="comparison-row" data-project-member-id={member.userId}>
                  <span>{member.email ?? `用户 ${member.userId}`}</span>
                  <span>{member.role}</span>
                  <span>
                    <Button
                      size="small"
                      theme="borderless"
                      disabled={projectBusy || !projectMemberReason.trim()}
                      loading={projectBusy}
                      onClick={() => void removeProjectMember(Number(selectedProject), member.userId)}
                      data-team-project-remove={member.userId}
                    >
                      移除
                    </Button>
                  </span>
                </div>
              ))}
            </div>
          </div>
        ) : null}
        <div className="mt-2">
          <Button size="small" theme="borderless" onClick={() => navigate('/projects')}>
            去项目列表
          </Button>
        </div>
      </Card>
    </div>
  )
}

// ---------------------------------------------------------------------------
// 帮助（S04）
// ---------------------------------------------------------------------------

export function HelpPage() {
  const navigate = useNavigate()
  const { Title, Text } = Typography
  const shortcuts = [
    ['命令搜索', '侧边栏「搜索」：Esc 关闭、回车打开第一条'],
    ['审阅队列', 'J / K 在当前页内切换样本（保持筛选条件）'],
    ['返回来源', '浏览器返回会保留筛选与聚焦的样本'],
  ]
  const terms: [string, string][] = [
    ['试制（pilot）', '小批量验证方案；与扩量批次相互独立，不会覆盖生产'],
    ['扩量（scale）', '正式批量生产；改模型或标准必须新建批次'],
    ['接纳率', '接纳数 / 纳入检查数；待审阅不算接纳，隔离不缩小被评测数据集'],
    ['缺分', '裁判没给出分数。它不是 0 分，也不参与均值'],
    ['未知费用', '超时/断连但可能已收费：按当时预留金额占用额度，不记 0'],
    ['发布候选', '发布前的清单与门槛检查；只有制品校验成功才会 published'],
  ]
  const recoveryChecklist = [
    '先阅读当前页面显示的错误与恢复提示，再决定是否重试。',
    '重试运行前确认引用的蓝图、标准和范围版本仍是预期版本。',
    '长时间无变化时刷新页面；运行状态以服务端返回为准。',
    '遇到权限错误时联系项目 owner 或管理员，不要重复提交写操作。',
    '登录失效后重新登录，再从当前项目或历史资产链接继续。',
  ]
  const highRiskActions = [
    '启动运行、评估或发布前确认目标项目、输入版本与预算。',
    '超时但可能已计费的请求显示为未知费用；先核对运行记录，不要盲目重跑。',
    '旧版兼容操作是否允许提交，以服务端 LEGACY_WRITES_FROZEN 配置为准；历史资产页默认只读。',
  ]
  return (
    <div className="console-page" data-studio-page="help">
      <div className="console-page__header">
        <div>
          <Title heading={4} className="!mb-1">
            帮助
          </Title>
          <Text type="tertiary">旅程、术语与快捷键；构建信息在下方。</Text>
        </div>
      </div>

      <Card className="console-card mb-3" bodyStyle={{ padding: 14 }} data-help-journey="true">
        <Text strong className="block mb-2">
          一条数据的旅程
        </Text>
        <Text type="tertiary" size="small" className="block">
          创建项目（不调用模型）→ 设计（蓝图/覆盖/标准）→ 试制 → 同基准比较并采纳 →
          扩量 → 质量实验 → 人工判断 → 发布（冻结清单 + manifest/hash）→ 下载固定版本。
        </Text>
      </Card>

      <Card className="console-card mb-3" bodyStyle={{ padding: 14 }} data-help-terms="true">
        <Text strong className="block mb-2">
          术语
        </Text>
        {terms.map(([term, explanation]) => (
          <Text key={term} type="tertiary" size="small" className="block">
            · <strong>{term}</strong>：{explanation}
          </Text>
        ))}
      </Card>

      <Card className="console-card mb-3" bodyStyle={{ padding: 14 }} data-help-shortcuts="true">
        <Text strong className="block mb-2">
          快捷键
        </Text>
        {shortcuts.map(([name, description]) => (
          <Text key={name} type="tertiary" size="small" className="block">
            · <strong>{name}</strong>：{description}
          </Text>
        ))}
      </Card>

      <div className="console-card-grid-2 mb-3">
        <Card className="console-card" bodyStyle={{ padding: 14 }} data-help-recovery="true">
          <Text strong className="block mb-2">失败恢复清单</Text>
          {recoveryChecklist.map((item) => <Text key={item} type="tertiary" size="small" className="block">• {item}</Text>)}
          <div className="mt-3 flex flex-wrap gap-2">
            <Button size="small" onClick={() => navigate('/projects')}>查看数据项目</Button>
            <Button size="small" onClick={() => navigate('/today')}>返回今日工作</Button>
          </div>
        </Card>
        <Card className="console-card" bodyStyle={{ padding: 14 }} data-help-risk="true">
          <Text strong className="block mb-2">高风险操作</Text>
          {highRiskActions.map((item) => <Text key={item} type="tertiary" size="small" className="block">• {item}</Text>)}
        </Card>
      </div>

      <Card className="console-card" bodyStyle={{ padding: 14 }} data-help-build="true">
        <Text strong className="block mb-1">
          构建信息
        </Text>
        <Text type="tertiary" size="small">
          {versionSummary()} · 构建时间 {APP_BUILD_TIME || '未知'} · 版本 {APP_VERSION}
        </Text>
      </Card>
    </div>
  )
}
