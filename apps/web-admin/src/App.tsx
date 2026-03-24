import { useEffect, useMemo, useState } from 'react'
import { motion } from 'framer-motion'
import {
  App as AntApp,
  Button,
  Card,
  Form,
  Input,
  InputNumber,
  List,
  Progress,
  Segmented,
  Space,
  Switch,
  Table,
  Tag,
} from 'antd'
import type { ColumnsType } from 'antd/es/table'
import {
  Activity,
  Boxes,
  Database,
  FileText,
  FolderCog,
  LayoutDashboard,
  LockKeyhole,
  Logs,
  Menu,
  PanelLeftClose,
  PanelLeftOpen,
  RefreshCw,
  Rocket,
  ServerCog,
  ShieldCheck,
  Sparkles,
  Workflow,
  X,
  type LucideIcon,
} from 'lucide-react'
import {
  type AuditRecord,
  type DashboardRecord,
  type PromptRecord,
  type ProviderRecord,
  type StorageProfileRecord,
  type StrategyRecord,
  adminApi,
} from './lib/api'

const planningModeOptions = [
  { label: '平衡', value: 'balanced' },
  { label: '优先广度', value: 'wide-first' },
  { label: '优先深度', value: 'deep-first' },
  { label: '成本优先', value: 'cost-saving' },
]

type AdminPageKey = 'dashboard' | 'providers' | 'storage' | 'strategies' | 'prompts' | 'audit'

type AdminPageDefinition = {
  key: AdminPageKey
  label: string
  caption: string
  icon: LucideIcon
  group: string
}

const pageDefinitions: AdminPageDefinition[] = [
  { key: 'dashboard', label: '总览', caption: '查看治理全景与关键运行信号', icon: LayoutDashboard, group: '控制台' },
  { key: 'providers', label: '模型提供方', caption: '维护模型网关、路由与并发参数', icon: Database, group: '资源接入' },
  { key: 'storage', label: '存储配置', caption: '维护 S3 / MinIO / OSS 存储目标', icon: FolderCog, group: '资源接入' },
  { key: 'strategies', label: '生成策略', caption: '定义领域规模、问题量与奖励变体', icon: Workflow, group: '生成治理' },
  { key: 'prompts', label: '提示词模板', caption: '治理各阶段系统提示词与版本', icon: Sparkles, group: '生成治理' },
  { key: 'audit', label: '审计日志', caption: '追踪配置变更与治理事件', icon: Logs, group: '审计中心' },
]

const pageKeys = new Set(pageDefinitions.map((item) => item.key))

const providerColumns: ColumnsType<ProviderRecord> = [
  { title: '名称', dataIndex: 'name', key: 'name' },
  { title: '基础 URL', dataIndex: 'baseUrl', key: 'baseUrl' },
  { title: '模型', dataIndex: 'model', key: 'model' },
  { title: '类型', dataIndex: 'providerType', key: 'providerType', render: (value) => <Tag color="blue">{value}</Tag> },
  { title: 'API 密钥', dataIndex: 'apiKeyMasked', key: 'apiKeyMasked', render: (value) => value || '未设置' },
]

const storageColumns: ColumnsType<StorageProfileRecord> = [
  { title: '名称', dataIndex: 'name', key: 'name' },
  { title: '提供方', dataIndex: 'provider', key: 'provider', render: (value) => <Tag color="cyan">{value}</Tag> },
  { title: '端点', dataIndex: 'endpoint', key: 'endpoint' },
  { title: '存储桶', dataIndex: 'bucket', key: 'bucket' },
  { title: '密钥', dataIndex: 'secretKeyMasked', key: 'secretKeyMasked', render: (value) => value || '未设置' },
]

const strategyColumns: ColumnsType<StrategyRecord> = [
  { title: '名称', dataIndex: 'name', key: 'name' },
  { title: '模式', dataIndex: 'planningMode', key: 'planningMode', render: (value) => <Tag color="purple">{value}</Tag> },
  { title: '领域数', dataIndex: 'domainCount', key: 'domainCount' },
  { title: '每领域问题数', dataIndex: 'questionsPerDomain', key: 'questionsPerDomain' },
  { title: '答案变体', dataIndex: 'answerVariants', key: 'answerVariants' },
  { title: '奖励变体', dataIndex: 'rewardVariants', key: 'rewardVariants' },
]

const promptColumns: ColumnsType<PromptRecord> = [
  { title: '名称', dataIndex: 'name', key: 'name' },
  { title: '阶段', dataIndex: 'stage', key: 'stage', render: (value) => <Tag color="gold">{value}</Tag> },
  { title: '版本', dataIndex: 'version', key: 'version' },
  { title: '启用状态', dataIndex: 'isActive', key: 'isActive', render: (value) => <Tag color={value ? 'green' : 'default'}>{value ? '启用' : '停用'}</Tag> },
]

const auditColumns: ColumnsType<AuditRecord> = [
  { title: '操作人', dataIndex: 'actor', key: 'actor' },
  { title: '操作', dataIndex: 'action', key: 'action' },
  { title: '资源', dataIndex: 'resourceType', key: 'resourceType' },
  { title: '详情', dataIndex: 'detail', key: 'detail' },
  { title: '创建时间', dataIndex: 'createdAt', key: 'createdAt' },
]

function readPageFromHash(): AdminPageKey {
  if (typeof window === 'undefined') return 'dashboard'
  const raw = window.location.hash.replace('#', '')
  return pageKeys.has(raw as AdminPageKey) ? (raw as AdminPageKey) : 'dashboard'
}

function SectionHeader({ eyebrow, title, description }: { eyebrow: string; title: string; description?: string }) {
  return (
    <div className="section-header">
      <span className="eyebrow">{eyebrow}</span>
      <h2>{title}</h2>
      {description ? <p>{description}</p> : null}
    </div>
  )
}

function PageHeader({ eyebrow, title, description, actions }: { eyebrow: string; title: string; description: string; actions?: React.ReactNode }) {
  return (
    <div className="page-header">
      <div>
        <span className="eyebrow">{eyebrow}</span>
        <h1>{title}</h1>
        <p>{description}</p>
      </div>
      {actions ? <div className="page-header-actions">{actions}</div> : null}
    </div>
  )
}

function StatCard({ icon: Icon, label, value, helper }: { icon: LucideIcon; label: string; value: string | number; helper: string }) {
  return (
    <article className="stat-card">
      <div className="stat-card-head">
        <span>{label}</span>
        <div className="stat-card-icon"><Icon size={16} strokeWidth={1.9} /></div>
      </div>
      <strong>{value}</strong>
      <p>{helper}</p>
    </article>
  )
}

function SpinnerIcon({ spinning }: { spinning: boolean }) {
  return <RefreshCw size={16} strokeWidth={1.9} className={spinning ? 'is-spinning' : undefined} />
}

function useAdminData(authenticated: boolean) {
  const [dashboard, setDashboard] = useState<DashboardRecord | null>(null)
  const [providers, setProviders] = useState<ProviderRecord[]>([])
  const [storageProfiles, setStorageProfiles] = useState<StorageProfileRecord[]>([])
  const [strategies, setStrategies] = useState<StrategyRecord[]>([])
  const [prompts, setPrompts] = useState<PromptRecord[]>([])
  const [auditLogs, setAuditLogs] = useState<AuditRecord[]>([])
  const [loading, setLoading] = useState(false)

  const load = async () => {
    if (!authenticated) return
    setLoading(true)
    try {
      const [dashboardData, providerData, storageData, strategyData, promptData, auditData] = await Promise.all([
        adminApi.dashboard(),
        adminApi.listProviders(),
        adminApi.listStorageProfiles(),
        adminApi.listStrategies(),
        adminApi.listPrompts(),
        adminApi.listAuditLogs(),
      ])
      setDashboard(dashboardData)
      setProviders(providerData)
      setStorageProfiles(storageData)
      setStrategies(strategyData)
      setPrompts(promptData)
      setAuditLogs(auditData)
    } finally {
      setLoading(false)
    }
  }

  useEffect(() => {
    void load()
  }, [authenticated])

  return { dashboard, providers, storageProfiles, strategies, prompts, auditLogs, loading, reload: load }
}

export default function App() {
  const [authenticated, setAuthenticated] = useState(false)
  const [activePage, setActivePage] = useState<AdminPageKey>(readPageFromHash())
  const [sidebarCollapsed, setSidebarCollapsed] = useState(false)
  const [mobileNavOpen, setMobileNavOpen] = useState(false)
  const [providerForm] = Form.useForm<ProviderRecord>()
  const [storageForm] = Form.useForm<StorageProfileRecord>()
  const [strategyForm] = Form.useForm<StrategyRecord>()
  const [promptForm] = Form.useForm<PromptRecord>()
  const { message } = AntApp.useApp()
  const data = useAdminData(authenticated)

  const userHref = typeof window !== 'undefined'
    ? `${window.location.protocol}//${window.location.hostname}:3210`
    : '/'

  const setPage = (page: AdminPageKey) => {
    setActivePage(page)
    setMobileNavOpen(false)
    if (typeof window !== 'undefined') {
      window.history.replaceState(null, '', `#${page}`)
    }
  }

  useEffect(() => {
    const handleHashChange = () => {
      setActivePage(readPageFromHash())
    }
    window.addEventListener('hashchange', handleHashChange)
    return () => window.removeEventListener('hashchange', handleHashChange)
  }, [])

  useEffect(() => {
    if (!authenticated) return
    if (!pageKeys.has(activePage)) {
      setPage('dashboard')
    }
  }, [authenticated, activePage])

  const groupedPages = pageDefinitions.reduce<Record<string, AdminPageDefinition[]>>((acc, page) => {
    acc[page.group] = [...(acc[page.group] ?? []), page]
    return acc
  }, {})
  const currentPageDefinition = pageDefinitions.find((item) => item.key === activePage) ?? pageDefinitions[0]
  const topNavItems = pageDefinitions.filter((item) => ['dashboard', 'providers', 'strategies', 'audit'].includes(item.key))

  const statCards = useMemo(() => {
    return [
      { icon: Database, label: '模型提供方', value: `${data.dashboard?.providerCount ?? 0}`, helper: `${data.dashboard?.activeProviderCount ?? 0} 条活动模型路由` },
      { icon: FolderCog, label: '存储配置', value: `${data.dashboard?.storageProfileCount ?? 0}`, helper: 'S3 / MinIO / OSS 兼容目标地址' },
      { icon: Workflow, label: '生成策略', value: `${data.dashboard?.strategyCount ?? 0}`, helper: '控制领域规模与问题产出方式' },
      { icon: Sparkles, label: '提示词模板', value: `${data.dashboard?.promptCount ?? 0}`, helper: '管理各阶段系统提示词版本' },
    ]
  }, [data.dashboard])

  const deliveryConfidence = data.dashboard
    ? Math.min(100, 32 + data.dashboard.activeProviderCount * 14 + data.dashboard.promptCount * 6 + data.dashboard.strategyCount * 8)
    : 18

  const renderDashboardPage = () => (
    <>
      <PageHeader
        eyebrow="治理总览"
        title="统一治理模型、存储、策略、提示词与审计"
        description="按 /root/new-api 的后台结构重做为头部导航、侧栏导航与分页工作区；不再把所有管理动作塞进单一长页。"
        actions={(
          <>
            <button type="button" className="shell-button secondary" onClick={() => setPage('providers')}>
              <Database size={16} strokeWidth={1.9} /> 进入资源配置
            </button>
            <button type="button" className="shell-button primary" onClick={() => void data.reload()} disabled={data.loading}>
              <SpinnerIcon spinning={data.loading} /> 刷新后台
            </button>
          </>
        )}
      />

      <div className="stat-grid four-up">
        {statCards.map((card) => <StatCard key={card.label} {...card} />)}
      </div>

      <div className="page-layout-grid align-start">
        <Card className="admin-panel hero-panel">
          <SectionHeader eyebrow="治理工作台" title="把配置治理拆成可切换的独立页面" description="参考 new-api 的后台框架，把资源接入、生成治理、审计中心分别做成明确页面。" />
          <div className="summary-list">
            {[
              '模型提供方与存储配置采用左右双栏：左读右写。',
              '策略与提示词以版本化方式统一管理。',
              '审计页专门查看治理事件，不再与编辑表单混排。',
            ].map((item) => (
              <div key={item} className="summary-row">
                <ShieldCheck size={16} strokeWidth={1.9} />
                <span>{item}</span>
              </div>
            ))}
          </div>
          <div className="quick-page-grid">
            {pageDefinitions.filter((page) => page.key !== 'dashboard').map((page) => (
              <button key={page.key} type="button" className="quick-page-card" onClick={() => setPage(page.key)}>
                <div className="quick-page-icon"><page.icon size={18} strokeWidth={1.9} /></div>
                <div>
                  <strong>{page.label}</strong>
                  <span>{page.caption}</span>
                </div>
              </button>
            ))}
          </div>
        </Card>

        <Card className="admin-panel side-panel">
          <SectionHeader eyebrow="交付信心" title="当前治理覆盖度" description="根据活动提供方、策略与提示词数量估算当前平台治理完备度。" />
          <Progress percent={deliveryConfidence} strokeColor={{ '0%': '#60a5fa', '100%': '#2563eb' }} />
          <div className="summary-list compact-list">
            <div className="summary-row"><span>活动模型路由</span><strong>{data.dashboard?.activeProviderCount ?? 0}</strong></div>
            <div className="summary-row"><span>提示词模板</span><strong>{data.dashboard?.promptCount ?? 0}</strong></div>
            <div className="summary-row"><span>审计记录</span><strong>{data.dashboard?.auditLogCount ?? 0}</strong></div>
          </div>
        </Card>
      </div>

      <div className="page-layout-grid align-start">
        <Card className="admin-panel wide-panel">
          <SectionHeader eyebrow="最近治理事件" title="审计快照" description="总览页保留最近事件速览，完整日志则单独进入审计页。" />
          <Table columns={auditColumns} dataSource={data.auditLogs} pagination={{ pageSize: 5 }} rowKey="id" loading={data.loading} />
        </Card>

        <div className="stack-column">
          <Card className="admin-panel side-panel">
            <SectionHeader eyebrow="能力清单" title="当前后台能力" />
            <List
              dataSource={[
                { icon: Database, label: '维护 OpenAI 协议与第三方模型网关' },
                { icon: FolderCog, label: '管理对象存储端点与默认写入目标' },
                { icon: Sparkles, label: '治理领域 / 问题 / 推理 / 奖励四阶段提示词' },
              ]}
              renderItem={(item) => (
                <List.Item className="plain-list-item">
                  <item.icon size={18} strokeWidth={1.9} />
                  <span>{item.label}</span>
                </List.Item>
              )}
            />
          </Card>

          <Card className="admin-panel side-panel">
            <SectionHeader eyebrow="运行建议" title="推荐操作顺序" />
            <div className="summary-list compact-list">
              {['先维护提供方与存储，再创建策略。', '提示词模板更新后建议立刻查看审计日志。', '交付前至少确保一个提供方处于活动状态。'].map((item) => (
                <div key={item} className="summary-row">
                  <Rocket size={16} strokeWidth={1.9} />
                  <span>{item}</span>
                </div>
              ))}
            </div>
          </Card>
        </div>
      </div>
    </>
  )

  const renderProvidersPage = () => (
    <>
      <PageHeader
        eyebrow="模型提供方"
        title="维护模型网关、路由与并发参数"
        description="用户侧调用的数据集生成接口会依赖这里配置的模型提供方与 OpenAI 兼容参数。"
        actions={(
          <button type="button" className="shell-button primary" onClick={() => void data.reload()} disabled={data.loading}>
            <SpinnerIcon spinning={data.loading} /> 刷新提供方列表
          </button>
        )}
      />

      <div className="page-layout-grid align-start">
        <Card className="admin-panel wide-panel">
          <SectionHeader eyebrow="当前列表" title="模型提供方台账" description="支持官方 OpenAI、代理网关与第三方兼容接口。" />
          <Table columns={providerColumns} dataSource={data.providers} pagination={false} rowKey="id" loading={data.loading} />
        </Card>

        <Card className="admin-panel side-panel">
          <SectionHeader eyebrow="新增 / 更新" title="保存模型提供方" />
          <Form
            form={providerForm}
            layout="vertical"
            initialValues={{ providerType: 'openai-compatible', maxConcurrency: 4, timeoutSeconds: 120, isActive: true }}
            onFinish={async (values) => {
              await adminApi.saveProvider(values)
              providerForm.resetFields()
              await data.reload()
              message.success('模型提供方已保存')
            }}
            className="editor-form"
          >
            <Form.Item name="name" label="提供方名称" rules={[{ required: true }]}><Input /></Form.Item>
            <Form.Item name="baseUrl" label="基础 URL" rules={[{ required: true }]}><Input /></Form.Item>
            <Form.Item name="model" label="模型" rules={[{ required: true }]}><Input /></Form.Item>
            <Form.Item name="providerType" label="类型" rules={[{ required: true }]}><Input /></Form.Item>
            <Space size={12} className="inline-controls" wrap>
              <Form.Item name="maxConcurrency" label="最大并发数"><InputNumber min={1} /></Form.Item>
              <Form.Item name="timeoutSeconds" label="超时秒数"><InputNumber min={1} /></Form.Item>
            </Space>
            <Form.Item name="apiKey" label="API 密钥"><Input.Password /></Form.Item>
            <Form.Item name="isActive" label="启用" valuePropName="checked"><Switch /></Form.Item>
            <Button type="primary" htmlType="submit" size="large" block>保存提供方</Button>
          </Form>
        </Card>
      </div>
    </>
  )

  const renderStoragePage = () => (
    <>
      <PageHeader
        eyebrow="存储配置"
        title="维护 S3 / MinIO / OSS 存储目标"
        description="长思维链、奖励数据与导出工件都依赖这里定义的对象存储端点与桶配置。"
        actions={(
          <button type="button" className="shell-button primary" onClick={() => void data.reload()} disabled={data.loading}>
            <SpinnerIcon spinning={data.loading} /> 刷新存储配置
          </button>
        )}
      />

      <div className="page-layout-grid align-start">
        <Card className="admin-panel wide-panel">
          <SectionHeader eyebrow="当前列表" title="对象存储台账" description="统一维护端点、地域、桶、路径风格与默认写入配置。" />
          <Table columns={storageColumns} dataSource={data.storageProfiles} pagination={false} rowKey="id" loading={data.loading} />
        </Card>

        <Card className="admin-panel side-panel">
          <SectionHeader eyebrow="新增 / 更新" title="保存存储配置" />
          <Form
            form={storageForm}
            layout="vertical"
            initialValues={{ provider: 'minio', usePathStyle: true, isDefault: true }}
            onFinish={async (values) => {
              await adminApi.saveStorageProfile(values)
              storageForm.resetFields()
              await data.reload()
              message.success('存储配置已保存')
            }}
            className="editor-form"
          >
            <Form.Item name="name" label="配置名称" rules={[{ required: true }]}><Input /></Form.Item>
            <Form.Item name="provider" label="提供方" rules={[{ required: true }]}><Input /></Form.Item>
            <Form.Item name="endpoint" label="端点" rules={[{ required: true }]}><Input /></Form.Item>
            <Form.Item name="region" label="地域"><Input /></Form.Item>
            <Form.Item name="bucket" label="存储桶" rules={[{ required: true }]}><Input /></Form.Item>
            <Form.Item name="accessKeyId" label="访问密钥 ID" rules={[{ required: true }]}><Input /></Form.Item>
            <Form.Item name="secretAccessKey" label="访问密钥 Secret"><Input.Password /></Form.Item>
            <Space size={12} className="inline-controls" wrap>
              <Form.Item name="usePathStyle" label="使用 Path Style" valuePropName="checked"><Switch /></Form.Item>
              <Form.Item name="isDefault" label="默认" valuePropName="checked"><Switch /></Form.Item>
            </Space>
            <Button type="primary" htmlType="submit" size="large" block>保存存储配置</Button>
          </Form>
        </Card>
      </div>
    </>
  )

  const renderStrategiesPage = () => (
    <>
      <PageHeader
        eyebrow="生成策略"
        title="定义领域规模、问题量与奖励变体"
        description="用户侧输入目标数据集规模后，会在这里定义的策略约束内推导领域数量和各阶段体量。"
        actions={(
          <button type="button" className="shell-button primary" onClick={() => void data.reload()} disabled={data.loading}>
            <SpinnerIcon spinning={data.loading} /> 刷新策略
          </button>
        )}
      />

      <div className="page-layout-grid align-start">
        <Card className="admin-panel wide-panel">
          <SectionHeader eyebrow="当前列表" title="生成策略台账" description="管理领域数量、每领域问题数、答案变体与奖励变体。" />
          <Table columns={strategyColumns} dataSource={data.strategies} pagination={false} rowKey="id" loading={data.loading} />
        </Card>

        <Card className="admin-panel side-panel">
          <SectionHeader eyebrow="新增 / 更新" title="保存生成策略" />
          <Form
            form={strategyForm}
            layout="vertical"
            initialValues={{ planningMode: 'balanced', domainCount: 1000, questionsPerDomain: 10, answerVariants: 1, rewardVariants: 1, isDefault: true }}
            onFinish={async (values) => {
              await adminApi.saveStrategy(values)
              strategyForm.resetFields()
              await data.reload()
              message.success('生成策略已保存')
            }}
            className="editor-form"
          >
            <Form.Item name="name" label="策略名称" rules={[{ required: true }]}><Input /></Form.Item>
            <Form.Item name="description" label="说明"><Input.TextArea rows={3} /></Form.Item>
            <Form.Item name="planningMode" label="规划模式"><Segmented options={planningModeOptions} block /></Form.Item>
            <Space size={12} className="inline-controls" wrap>
              <Form.Item name="domainCount" label="领域数"><InputNumber min={1} /></Form.Item>
              <Form.Item name="questionsPerDomain" label="每领域问题数"><InputNumber min={1} /></Form.Item>
              <Form.Item name="answerVariants" label="答案变体数"><InputNumber min={1} /></Form.Item>
              <Form.Item name="rewardVariants" label="奖励变体数"><InputNumber min={1} /></Form.Item>
            </Space>
            <Form.Item name="isDefault" label="默认" valuePropName="checked"><Switch /></Form.Item>
            <Button type="primary" htmlType="submit" size="large" block>保存策略</Button>
          </Form>
        </Card>
      </div>
    </>
  )

  const renderPromptsPage = () => (
    <>
      <PageHeader
        eyebrow="提示词模板"
        title="治理各阶段系统提示词与版本"
        description="领域生成、问题生成、长链推理和奖励评估都依赖这里维护的系统提示词与用户提示词模板。"
        actions={(
          <button type="button" className="shell-button primary" onClick={() => void data.reload()} disabled={data.loading}>
            <SpinnerIcon spinning={data.loading} /> 刷新提示词
          </button>
        )}
      />

      <div className="page-layout-grid align-start">
        <Card className="admin-panel wide-panel">
          <SectionHeader eyebrow="当前列表" title="提示词模板台账" description="统一维护阶段、版本、系统提示词与用户提示词。" />
          <Table columns={promptColumns} dataSource={data.prompts} pagination={false} rowKey="id" loading={data.loading} />
        </Card>

        <Card className="admin-panel side-panel">
          <SectionHeader eyebrow="新增 / 更新" title="保存提示词模板" />
          <Form
            form={promptForm}
            layout="vertical"
            initialValues={{ stage: 'domain-generation', version: 'v1', isActive: true }}
            onFinish={async (values) => {
              await adminApi.savePrompt(values)
              promptForm.resetFields()
              await data.reload()
              message.success('提示词模板已保存')
            }}
            className="editor-form"
          >
            <Form.Item name="name" label="提示词名称" rules={[{ required: true }]}><Input /></Form.Item>
            <Form.Item name="stage" label="阶段" rules={[{ required: true }]}><Input /></Form.Item>
            <Form.Item name="version" label="版本" rules={[{ required: true }]}><Input /></Form.Item>
            <Form.Item name="systemPrompt" label="系统提示词" rules={[{ required: true }]}><Input.TextArea rows={4} /></Form.Item>
            <Form.Item name="userPrompt" label="用户提示词" rules={[{ required: true }]}><Input.TextArea rows={4} /></Form.Item>
            <Form.Item name="isActive" label="启用" valuePropName="checked"><Switch /></Form.Item>
            <Button type="primary" htmlType="submit" size="large" block>保存提示词</Button>
          </Form>
        </Card>
      </div>
    </>
  )

  const renderAuditPage = () => (
    <>
      <PageHeader
        eyebrow="审计中心"
        title="追踪配置变更与治理事件"
        description="审计页独立出来，专门用于回看谁在什么时候修改了哪一类治理配置。"
        actions={(
          <button type="button" className="shell-button primary" onClick={() => void data.reload()} disabled={data.loading}>
            <SpinnerIcon spinning={data.loading} /> 刷新审计日志
          </button>
        )}
      />

      <div className="page-layout-grid align-start">
        <Card className="admin-panel wide-panel">
          <SectionHeader eyebrow="完整日志" title="治理事件列表" description="建议在模型、存储、策略和提示词发生变更后立刻回到这里复核。" />
          <Table columns={auditColumns} dataSource={data.auditLogs} pagination={{ pageSize: 10 }} rowKey="id" loading={data.loading} />
        </Card>

        <div className="stack-column">
          <Card className="admin-panel side-panel">
            <SectionHeader eyebrow="审计摘要" title="最近统计" />
            <div className="summary-list compact-list">
              <div className="summary-row"><span>审计记录</span><strong>{data.dashboard?.auditLogCount ?? 0}</strong></div>
              <div className="summary-row"><span>提示词模板</span><strong>{data.dashboard?.promptCount ?? 0}</strong></div>
              <div className="summary-row"><span>生成策略</span><strong>{data.dashboard?.strategyCount ?? 0}</strong></div>
            </div>
          </Card>

          <Card className="admin-panel side-panel">
            <SectionHeader eyebrow="治理原则" title="建议保留的审计习惯" />
            <List
              dataSource={[
                '更新提供方密钥后立刻检查审计日志。',
                '更换默认存储路由后建议执行导出链路回归。',
                '新增提示词模板版本后建议记录切换原因。',
              ]}
              renderItem={(item) => <List.Item className="plain-list-item">{item}</List.Item>}
            />
          </Card>
        </div>
      </div>
    </>
  )

  const renderCurrentPage = () => {
    switch (activePage) {
      case 'providers':
        return renderProvidersPage()
      case 'storage':
        return renderStoragePage()
      case 'strategies':
        return renderStrategiesPage()
      case 'prompts':
        return renderPromptsPage()
      case 'audit':
        return renderAuditPage()
      case 'dashboard':
      default:
        return renderDashboardPage()
    }
  }

  if (!authenticated) {
    return (
      <div className="admin-landing-shell">
        <header className="admin-landing-header">
          <div className="admin-brand-lockup">
            <div className="admin-brand-badge"><ShieldCheck size={18} strokeWidth={1.9} /></div>
            <div>
              <strong>LLM Data Factory Console</strong>
              <p>面向运营与平台管理员的治理后台</p>
            </div>
          </div>
          <a className="shell-button secondary link-button" href={userHref}>用户工作台</a>
        </header>

        <main className="admin-landing-main">
          <section className="admin-landing-hero">
            <div className="hero-copy">
              <span className="eyebrow">治理后台</span>
              <h1>统一维护模型、存储、策略、提示词与审计。</h1>
              <p>后台页面结构完全按 /root/new-api 的控制台方式重做为头部导航、侧栏导航与分页工作区，专门服务企业级数据工厂的资源治理与变更审计。</p>
              <div className="hero-tags">
                <Tag color="processing">模型路由治理</Tag>
                <Tag color="green">对象存储治理</Tag>
                <Tag color="purple">提示词版本治理</Tag>
              </div>
            </div>

            <Card className="admin-panel login-panel">
              <SectionHeader eyebrow="安全入口" title="登录后台工作台" description="登录后进入重构后的分页管理控制台。" />
              <Form layout="vertical" onFinish={() => { setAuthenticated(true); setPage('dashboard') }} className="editor-form">
                <Form.Item name="email" label="管理员邮箱" rules={[{ required: true, message: '请输入管理员邮箱。' }]}>
                  <Input size="large" placeholder="admin@company.com" />
                </Form.Item>
                <Form.Item name="password" label="访问密钥" rules={[{ required: true, message: '请输入访问密钥。' }]}>
                  <Input.Password size="large" placeholder="请输入安全访问密钥" />
                </Form.Item>
                <Button type="primary" htmlType="submit" size="large" block>进入管理工作台</Button>
              </Form>
            </Card>
          </section>

          <section className="admin-landing-grid">
            {[
              { icon: Database, title: '模型提供方', description: '维护兼容 OpenAI 协议与第三方模型路由。' },
              { icon: FolderCog, title: '对象存储', description: '统一管理 S3 / MinIO / OSS 存储落点。' },
              { icon: Workflow, title: '生成策略', description: '定义领域数、问题数和样本扩增方式。' },
              { icon: FileText, title: '审计日志', description: '记录关键治理动作，支持后台复核。' },
            ].map((item) => (
              <Card key={item.title} className="admin-panel feature-panel">
                <div className="feature-icon"><item.icon size={18} strokeWidth={1.9} /></div>
                <h2>{item.title}</h2>
                <p>{item.description}</p>
              </Card>
            ))}
          </section>
        </main>
      </div>
    )
  }

  return (
    <div className={`admin-shell ${sidebarCollapsed ? 'sidebar-collapsed' : ''} ${mobileNavOpen ? 'mobile-nav-open' : ''}`}>
      <header className="admin-header">
        <div className="admin-header-inner">
          <div className="header-leading">
            <button type="button" className="icon-button mobile-only" onClick={() => setMobileNavOpen((current) => !current)}>
              {mobileNavOpen ? <X size={18} strokeWidth={1.9} /> : <Menu size={18} strokeWidth={1.9} />}
            </button>
            <div className="admin-brand-lockup">
              <div className="admin-brand-badge"><ShieldCheck size={18} strokeWidth={1.9} /></div>
              <div>
                <strong>LLM Data Factory Console</strong>
                <p>{currentPageDefinition.label} / {currentPageDefinition.caption}</p>
              </div>
            </div>
          </div>

          <nav className="admin-topnav desktop-only">
            {topNavItems.map((item) => (
              <button key={item.key} type="button" className={`admin-topnav-item ${activePage === item.key ? 'active' : ''}`} onClick={() => setPage(item.key)}>
                <item.icon size={15} strokeWidth={1.9} />
                <span>{item.label}</span>
              </button>
            ))}
          </nav>

          <div className="header-actions">
            <button type="button" className="icon-button desktop-only" onClick={() => setSidebarCollapsed((current) => !current)}>
              {sidebarCollapsed ? <PanelLeftOpen size={18} strokeWidth={1.9} /> : <PanelLeftClose size={18} strokeWidth={1.9} />}
            </button>
            <button type="button" className="icon-button" onClick={() => void data.reload()} disabled={data.loading}>
              <SpinnerIcon spinning={data.loading} />
            </button>
            <a className="shell-button secondary link-button" href={userHref}>用户工作台</a>
          </div>
        </div>
      </header>

      <aside className="admin-sidebar">
        <div className="admin-sidebar-content">
          <Card className="admin-panel sidebar-panel">
            <SectionHeader eyebrow="工作区导航" title="后台控制台" description="按照参考项目的结构，把治理动作拆成清晰的分页工作区。" />
          </Card>

          {Object.entries(groupedPages).map(([group, pages]) => (
            <div key={group} className="sidebar-group">
              <span className="sidebar-group-label">{group}</span>
              <div className="sidebar-nav-list">
                {pages.map((page) => (
                  <button key={page.key} type="button" className={`sidebar-nav-item ${activePage === page.key ? 'active' : ''}`} onClick={() => setPage(page.key)}>
                    <div className="sidebar-nav-icon"><page.icon size={18} strokeWidth={1.9} /></div>
                    <div className="sidebar-nav-copy">
                      <strong>{page.label}</strong>
                      <span>{page.caption}</span>
                    </div>
                  </button>
                ))}
              </div>
            </div>
          ))}

          <Card className="admin-panel sidebar-panel">
            <SectionHeader eyebrow="平台摘要" title="关键计数" />
            <div className="summary-list compact-list">
              <div className="summary-row"><span>活动路由</span><strong>{data.dashboard?.activeProviderCount ?? 0}</strong></div>
              <div className="summary-row"><span>存储配置</span><strong>{data.dashboard?.storageProfileCount ?? 0}</strong></div>
              <div className="summary-row"><span>提示词模板</span><strong>{data.dashboard?.promptCount ?? 0}</strong></div>
            </div>
          </Card>
        </div>
      </aside>

      <button type="button" className="sidebar-backdrop" onClick={() => setMobileNavOpen(false)} aria-label="关闭侧栏" />

      <main className="admin-main">
        <motion.div
          key={activePage}
          initial={{ opacity: 0, y: 18 }}
          animate={{ opacity: 1, y: 0 }}
          transition={{ duration: 0.22 }}
          className="admin-page"
        >
          {renderCurrentPage()}
        </motion.div>
      </main>
    </div>
  )
}
