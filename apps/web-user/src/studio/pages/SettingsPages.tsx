import { useCallback, useEffect, useState } from 'react'
import { useNavigate } from 'react-router-dom'
import { Button, Card, Empty, Input, Select, Spin, Tag, Typography } from '@douyinfe/semi-ui'
import { ShieldCheck, Users } from 'lucide-react'
import { authApi } from '../../lib/api'
import { settingsApi } from '../../lib/api/studio'
import type {
  ConnectionOptions,
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

export function ConnectionsPage() {
  const { Title, Text } = Typography
  const [options, setOptions] = useState<ConnectionOptions | null>(null)
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)

  useEffect(() => {
    let cancelled = false
    void (async () => {
      try {
        const response = await settingsApi.connectionOptions()
        if (!cancelled) setOptions(response)
      } catch (loadError) {
        if (!cancelled) setError(loadError instanceof Error ? loadError.message : '加载连接选项失败')
      } finally {
        if (!cancelled) setLoading(false)
      }
    })()
    return () => {
      cancelled = true
    }
  }, [])

  return (
    <div className="console-page" data-studio-page="settings-connections">
      <div className="console-page__header">
        <div>
          <Title heading={4} className="!mb-1">
            连接与存储
          </Title>
          <Text type="tertiary">可用的模型连接与结果存储。</Text>
        </div>
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
            <Text strong className="block mb-2">
              模型连接
            </Text>
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
                </div>
                {options.providers.map((provider) => (
                  <div key={provider.id} className="comparison-row" data-connection-id={provider.id}>
                    <span>{provider.name}</span>
                    <span>{provider.model}</span>
                    <span>{provider.providerType}</span>
                    <span>{provider.apiKeyMasked || '—'}</span>
                    <span>
                      <Tag size="small" color={provider.isActive ? 'green' : 'grey'}>
                        {provider.isActive ? '启用' : '停用'}
                      </Tag>
                    </span>
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
                    <span>{profile.name}</span>
                    <span>{profile.provider}</span>
                    <span>{profile.bucket}</span>
                    <span>{profile.secretKeyMasked || '—'}</span>
                    <span>
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
            <div className="wizard-grid mt-3">
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
    ['接纳率', '接纳数 / 纳入检查数；待审阅不算接纳，隔离不缩小分母'],
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
