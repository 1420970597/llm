import { useCallback, useEffect, useState } from 'react'
import { useNavigate } from 'react-router-dom'
import { Button, Card, Empty, Input, Select, Spin, Tag, Typography } from '@douyinfe/semi-ui'
import { ShieldCheck, Users } from 'lucide-react'
import { client } from '../../lib/api'
import { settingsApi } from '../../lib/api/studio'
import type {
  ConnectionOptions,
  Page,
  ProjectMemberRecord,
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
          <Text type="tertiary">这里只显示非秘密标识；新增/修改与「测试连接」在管理员设置页。</Text>
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

          {options.notes.map((note) => (
            <Text key={note} type="tertiary" size="small" className="block">
              {note}
            </Text>
          ))}
        </>
      ) : null}
    </div>
  )
}

// ---------------------------------------------------------------------------
// 团队与角色（S03）
// ---------------------------------------------------------------------------

export function TeamPage() {
  const navigate = useNavigate()
  const { Title, Text } = Typography
  const [members, setMembers] = useState<WorkspaceMemberRecord[]>([])
  const [notes, setNotes] = useState<string[]>([])
  const [email, setEmail] = useState('')
  const [role, setRole] = useState('member')
  const [loading, setLoading] = useState(true)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [notice, setNotice] = useState<string | null>(null)
  const [projects, setProjects] = useState<{ id: number; name: string }[]>([])
  const [selectedProject, setSelectedProject] = useState('')
  const [projectMembers, setProjectMembers] = useState<ProjectMemberRecord[]>([])

  const load = useCallback(async () => {
    setLoading(true)
    setError(null)
    try {
      const response = await settingsApi.workspaceMembers()
      setMembers(response.items ?? [])
      setNotes(response.notes ?? [])
    } catch (loadError) {
      setError(loadError instanceof Error ? loadError.message : '加载成员失败')
    } finally {
      setLoading(false)
    }
  }, [])

  useEffect(() => {
    void load()
  }, [load])

  useEffect(() => {
    let cancelled = false
    void (async () => {
      try {
        const response = await client.get<Page<{ id: number; name: string }>>('/v1/projects')
        if (!cancelled) setProjects((response.data.items ?? []).map((item) => ({ id: item.id, name: item.name })))
      } catch {
        // 项目列表拿不到不影响工作区成员管理（它是辅助信息）。
      }
    })()
    return () => {
      cancelled = true
    }
  }, [])

  const loadProjectMembers = useCallback(async (projectID: number) => {
    if (projectID <= 0) {
      setProjectMembers([])
      return
    }
    try {
      const response = await settingsApi.projectMembers(projectID)
      setProjectMembers(response.items ?? [])
    } catch (loadError) {
      setError(loadError instanceof Error ? loadError.message : '加载项目成员失败')
    }
  }, [])

  const addMember = useCallback(async () => {
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
  }, [email, load, role])

  const removeMember = useCallback(
    async (userId: number) => {
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
    [load],
  )

  return (
    <div className="console-page" data-studio-page="settings-team">
      <div className="console-page__header">
        <div>
          <Title heading={4} className="!mb-1">
            团队与角色
          </Title>
          <Text type="tertiary">
            工作区管理员管理成员与连接；项目 owner 管理项目成员 —— 两者职责分开。
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
              <span>操作</span>
            </div>
            {members.map((member) => (
              <div key={member.userId} className="comparison-row" data-member-id={member.userId}>
                <span>{member.email}</span>
                <span>
                  <Tag size="small" color={member.role === 'admin' ? 'violet' : 'blue'}>
                    {member.role === 'admin' ? '工作区管理员' : '成员'}
                  </Tag>
                </span>
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
              </div>
            ))}
          </div>
        )}

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
        {notes.map((note) => (
          <Text key={note} type="tertiary" size="small" className="block mt-2">
            {note}
          </Text>
        ))}
      </Card>

      <Card className="console-card" bodyStyle={{ padding: 14 }} data-team-project="true">
        <Text strong className="block mb-2">
          项目成员
        </Text>
        <Text type="tertiary" size="small" className="block mb-2">
          项目内容权限（owner / reviewer / viewer）由项目决定；工作区管理员**不自动**
          拥有项目内容读权。
        </Text>
        <Select
          value={selectedProject || undefined}
          placeholder="选择项目查看成员"
          style={{ width: 280 }}
          aria-label="选择项目"
          optionList={projects.map((project) => ({ value: String(project.id), label: project.name }))}
          onChange={(value) => {
            const next = String(value ?? '')
            setSelectedProject(next)
            void loadProjectMembers(Number.parseInt(next, 10) || 0)
          }}
        />
        {selectedProject ? (
          <div className="comparison-table mt-2" data-team-project-members="true">
            <div className="comparison-row comparison-row--head">
              <span>用户</span>
              <span>项目角色</span>
              <span>操作</span>
            </div>
            {projectMembers.map((member) => (
              <div key={member.userId} className="comparison-row" data-project-member-id={member.userId}>
                <span>{member.email ?? `用户 ${member.userId}`}</span>
                <span>{member.role}</span>
                <span>
                  <Button
                    size="small"
                    theme="borderless"
                    disabled={busy}
                    onClick={() =>
                      void settingsApi
                        .removeProjectMember(Number(selectedProject), member.userId)
                        .then(() => loadProjectMembers(Number(selectedProject)))
                        .catch((removeError: unknown) =>
                          setError(removeError instanceof Error ? removeError.message : '移除项目成员失败'),
                        )
                    }
                  >
                    移除
                  </Button>
                </span>
              </div>
            ))}
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
