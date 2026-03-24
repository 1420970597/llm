import { useEffect, useMemo, useState } from 'react'
import { motion } from 'framer-motion'
import {
  App as AntApp,
  Button,
  Card,
  Col,
  Form,
  Input,
  InputNumber,
  List,
  Progress,
  Row,
  Segmented,
  Space,
  Switch,
  Table,
  Tabs,
  Tag,
} from 'antd'
import type { ColumnsType } from 'antd/es/table'
import { Database, FolderCog, Layers3, LockKeyhole, Logs, ShieldEllipsis, Workflow } from 'lucide-react'
import { AuditRecord, DashboardRecord, PromptRecord, ProviderRecord, StorageProfileRecord, StrategyRecord, adminApi } from './lib/api'

const planningModeOptions = [
  { label: '平衡', value: 'balanced' },
  { label: '优先广度', value: 'wide-first' },
  { label: '优先深度', value: 'deep-first' },
  { label: '成本优先', value: 'cost-saving' },
]

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
  const [providerForm] = Form.useForm<ProviderRecord>()
  const [storageForm] = Form.useForm<StorageProfileRecord>()
  const [strategyForm] = Form.useForm<StrategyRecord>()
  const [promptForm] = Form.useForm<PromptRecord>()
  const { message } = AntApp.useApp()
  const data = useAdminData(authenticated)

  const metricCards = useMemo(() => {
    if (!data.dashboard) return []
    return [
      { title: '模型提供方', value: `${data.dashboard.providerCount}`, detail: `${data.dashboard.activeProviderCount} 条活动模型路由` },
      { title: '存储配置', value: `${data.dashboard.storageProfileCount}`, detail: 'S3 / MinIO / OSS 兼容目标地址' },
      { title: '提示词模板', value: `${data.dashboard.promptCount}`, detail: `${data.dashboard.strategyCount} 个受治理生成策略` },
    ]
  }, [data.dashboard])

  return (
    <div className="admin-shell">
      <motion.section className="admin-hero" initial={{ opacity: 0, y: 20 }} animate={{ opacity: 1, y: 0 }} transition={{ duration: 0.5 }}>
        <div>
          <span className="eyebrow">管理控制台</span>
          <h1>统一治理提示词、模型提供方、存储路由与异步数据集吞吐。</h1>
          <p>当前管理台已经可以持久化真实配置数据、加密密钥、生成策略和审计记录。</p>
        </div>
      </motion.section>

      {!authenticated ? (
        <Card className="glass-panel login-panel">
          <div className="card-header">
            <div>
              <span className="eyebrow">安全入口</span>
              <h2>登录后解锁管理工作台。</h2>
            </div>
            <LockKeyhole size={18} strokeWidth={1.8} />
          </div>
          <Form layout="vertical" onFinish={() => setAuthenticated(true)}>
            <Row gutter={[18, 0]}>
              <Col xs={24} md={12}>
                <Form.Item name="email" label="管理员邮箱" rules={[{ required: true, message: '请输入管理员邮箱。' }]}>
                  <Input size="large" placeholder="admin@company.com" />
                </Form.Item>
              </Col>
              <Col xs={24} md={12}>
                <Form.Item name="password" label="访问密钥" rules={[{ required: true, message: '请输入访问密钥。' }]}>
                  <Input.Password size="large" placeholder="请输入安全访问密钥" />
                </Form.Item>
              </Col>
            </Row>
            <Button type="primary" htmlType="submit" size="large">进入管理工作台</Button>
          </Form>
        </Card>
      ) : (
        <>
          <Row gutter={[18, 18]}>
            {metricCards.map((item, index) => (
              <Col xs={24} md={8} key={item.title}>
                <motion.div initial={{ opacity: 0, y: 18 }} animate={{ opacity: 1, y: 0 }} transition={{ delay: 0.08 * index }}>
                  <Card className="glass-panel metric-panel">
                    <div className="metric-title">{item.title}</div>
                    <strong>{item.value}</strong>
                    <p>{item.detail}</p>
                  </Card>
                </motion.div>
              </Col>
            ))}
          </Row>

          <Row gutter={[18, 18]} className="section-row">
            <Col xs={24} xl={16}>
              <Card className="glass-panel">
                <div className="card-header">
                  <div>
                    <span className="eyebrow">流水线总览</span>
                    <h2>配置与治理中心</h2>
                  </div>
                  <Workflow size={18} strokeWidth={1.8} />
                </div>
                <Tabs
                  defaultActiveKey="providers"
                  items={[
                    {
                      key: 'providers',
                      label: '模型提供方',
                      children: (
                        <div className="tab-grid">
                          <Table columns={providerColumns} dataSource={data.providers} pagination={false} rowKey="id" loading={data.loading} />
                          <Card className="sub-panel">
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
                            >
                              <Form.Item name="name" label="提供方名称" rules={[{ required: true }]}><Input /></Form.Item>
                              <Form.Item name="baseUrl" label="基础 URL" rules={[{ required: true }]}><Input /></Form.Item>
                              <Form.Item name="model" label="模型" rules={[{ required: true }]}><Input /></Form.Item>
                              <Form.Item name="providerType" label="类型" rules={[{ required: true }]}><Input /></Form.Item>
                              <Space size={12} className="inline-controls">
                                <Form.Item name="maxConcurrency" label="最大并发数"><InputNumber min={1} /></Form.Item>
                                <Form.Item name="timeoutSeconds" label="超时秒数"><InputNumber min={1} /></Form.Item>
                              </Space>
                              <Form.Item name="apiKey" label="API 密钥"><Input.Password /></Form.Item>
                              <Form.Item name="isActive" label="启用" valuePropName="checked"><Switch /></Form.Item>
                              <Button type="primary" htmlType="submit">保存提供方</Button>
                            </Form>
                          </Card>
                        </div>
                      ),
                    },
                    {
                      key: 'storage',
                      label: '存储配置',
                      children: (
                        <div className="tab-grid">
                          <Table columns={storageColumns} dataSource={data.storageProfiles} pagination={false} rowKey="id" loading={data.loading} />
                          <Card className="sub-panel">
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
                            >
                              <Form.Item name="name" label="配置名称" rules={[{ required: true }]}><Input /></Form.Item>
                              <Form.Item name="provider" label="提供方" rules={[{ required: true }]}><Input /></Form.Item>
                              <Form.Item name="endpoint" label="端点" rules={[{ required: true }]}><Input /></Form.Item>
                              <Form.Item name="region" label="地域"><Input /></Form.Item>
                              <Form.Item name="bucket" label="存储桶" rules={[{ required: true }]}><Input /></Form.Item>
                              <Form.Item name="accessKeyId" label="访问密钥 ID" rules={[{ required: true }]}><Input /></Form.Item>
                              <Form.Item name="secretAccessKey" label="访问密钥 Secret"><Input.Password /></Form.Item>
                              <Space size={12} className="inline-controls switch-row">
                                <Form.Item name="usePathStyle" label="使用 Path Style" valuePropName="checked"><Switch /></Form.Item>
                                <Form.Item name="isDefault" label="默认" valuePropName="checked"><Switch /></Form.Item>
                              </Space>
                              <Button type="primary" htmlType="submit">保存存储配置</Button>
                            </Form>
                          </Card>
                        </div>
                      ),
                    },
                    {
                      key: 'strategies',
                      label: '生成策略',
                      children: (
                        <div className="tab-grid">
                          <Table columns={strategyColumns} dataSource={data.strategies} pagination={false} rowKey="id" loading={data.loading} />
                          <Card className="sub-panel">
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
                            >
                              <Form.Item name="name" label="策略名称" rules={[{ required: true }]}><Input /></Form.Item>
                              <Form.Item name="description" label="说明"><Input.TextArea rows={3} /></Form.Item>
                              <Form.Item name="planningMode" label="规划模式">
                                <Segmented options={planningModeOptions} />
                              </Form.Item>
                              <Space size={12} wrap className="inline-controls">
                                <Form.Item name="domainCount" label="领域数"><InputNumber min={1} /></Form.Item>
                                <Form.Item name="questionsPerDomain" label="每领域问题数"><InputNumber min={1} /></Form.Item>
                                <Form.Item name="answerVariants" label="答案变体数"><InputNumber min={1} /></Form.Item>
                                <Form.Item name="rewardVariants" label="奖励变体数"><InputNumber min={1} /></Form.Item>
                              </Space>
                              <Form.Item name="isDefault" label="默认" valuePropName="checked"><Switch /></Form.Item>
                              <Button type="primary" htmlType="submit">保存策略</Button>
                            </Form>
                          </Card>
                        </div>
                      ),
                    },
                    {
                      key: 'prompts',
                      label: '提示词模板',
                      children: (
                        <div className="tab-grid">
                          <Table columns={promptColumns} dataSource={data.prompts} pagination={false} rowKey="id" loading={data.loading} />
                          <Card className="sub-panel">
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
                            >
                              <Form.Item name="name" label="提示词名称" rules={[{ required: true }]}><Input /></Form.Item>
                              <Form.Item name="stage" label="阶段" rules={[{ required: true }]}><Input /></Form.Item>
                              <Form.Item name="version" label="版本" rules={[{ required: true }]}><Input /></Form.Item>
                              <Form.Item name="systemPrompt" label="系统提示词" rules={[{ required: true }]}><Input.TextArea rows={4} /></Form.Item>
                              <Form.Item name="userPrompt" label="用户提示词" rules={[{ required: true }]}><Input.TextArea rows={4} /></Form.Item>
                              <Form.Item name="isActive" label="启用" valuePropName="checked"><Switch /></Form.Item>
                              <Button type="primary" htmlType="submit">保存提示词</Button>
                            </Form>
                          </Card>
                        </div>
                      ),
                    },
                  ]}
                />
              </Card>
            </Col>
            <Col xs={24} xl={8}>
              <Space direction="vertical" size={18} style={{ width: '100%' }}>
                <Card className="glass-panel">
                  <div className="card-header">
                    <div>
                      <span className="eyebrow">审计轨迹</span>
                      <h2>最近治理事件</h2>
                    </div>
                    <Logs size={18} strokeWidth={1.8} />
                  </div>
                  <Table columns={auditColumns} dataSource={data.auditLogs} pagination={{ pageSize: 5 }} rowKey="id" loading={data.loading} size="small" />
                </Card>
                <Card className="glass-panel">
                  <div className="card-header">
                    <div>
                      <span className="eyebrow">基础模块</span>
                      <h2>当前管理侧能力</h2>
                    </div>
                    <Layers3 size={18} strokeWidth={1.8} />
                  </div>
                  <List
                    dataSource={[
                      { icon: FolderCog, label: '模型提供方与存储配置的增删改查' },
                      { icon: Database, label: '在 PostgreSQL 中持久化加密密钥' },
                      { icon: ShieldEllipsis, label: '具备审计能力的管理信息架构' },
                    ]}
                    renderItem={(item) => (
                      <List.Item className="list-item">
                        <item.icon size={18} strokeWidth={1.8} />
                        <span>{item.label}</span>
                      </List.Item>
                    )}
                  />
                </Card>
                <Card className="glass-panel">
                  <div className="card-header">
                    <div>
                      <span className="eyebrow">完成度</span>
                      <h2>阶段完成信心</h2>
                    </div>
                    <Workflow size={18} strokeWidth={1.8} />
                  </div>
                  <Progress percent={authenticated ? 100 : 20} strokeColor="#9cc2ff" />
                </Card>
              </Space>
            </Col>
          </Row>
        </>
      )}
    </div>
  )
}
