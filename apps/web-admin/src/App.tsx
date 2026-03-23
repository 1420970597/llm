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

const providerColumns: ColumnsType<ProviderRecord> = [
  { title: 'Name', dataIndex: 'name', key: 'name' },
  { title: 'Base URL', dataIndex: 'baseUrl', key: 'baseUrl' },
  { title: 'Model', dataIndex: 'model', key: 'model' },
  { title: 'Type', dataIndex: 'providerType', key: 'providerType', render: (value) => <Tag color="blue">{value}</Tag> },
  { title: 'API key', dataIndex: 'apiKeyMasked', key: 'apiKeyMasked', render: (value) => value || 'Not set' },
]

const storageColumns: ColumnsType<StorageProfileRecord> = [
  { title: 'Name', dataIndex: 'name', key: 'name' },
  { title: 'Provider', dataIndex: 'provider', key: 'provider', render: (value) => <Tag color="cyan">{value}</Tag> },
  { title: 'Endpoint', dataIndex: 'endpoint', key: 'endpoint' },
  { title: 'Bucket', dataIndex: 'bucket', key: 'bucket' },
  { title: 'Secret', dataIndex: 'secretKeyMasked', key: 'secretKeyMasked', render: (value) => value || 'Not set' },
]

const strategyColumns: ColumnsType<StrategyRecord> = [
  { title: 'Name', dataIndex: 'name', key: 'name' },
  { title: 'Mode', dataIndex: 'planningMode', key: 'planningMode', render: (value) => <Tag color="purple">{value}</Tag> },
  { title: 'Domains', dataIndex: 'domainCount', key: 'domainCount' },
  { title: 'Questions / domain', dataIndex: 'questionsPerDomain', key: 'questionsPerDomain' },
  { title: 'Answers', dataIndex: 'answerVariants', key: 'answerVariants' },
  { title: 'Rewards', dataIndex: 'rewardVariants', key: 'rewardVariants' },
]

const promptColumns: ColumnsType<PromptRecord> = [
  { title: 'Name', dataIndex: 'name', key: 'name' },
  { title: 'Stage', dataIndex: 'stage', key: 'stage', render: (value) => <Tag color="gold">{value}</Tag> },
  { title: 'Version', dataIndex: 'version', key: 'version' },
  { title: 'Active', dataIndex: 'isActive', key: 'isActive', render: (value) => <Tag color={value ? 'green' : 'default'}>{value ? 'Active' : 'Inactive'}</Tag> },
]

const auditColumns: ColumnsType<AuditRecord> = [
  { title: 'Actor', dataIndex: 'actor', key: 'actor' },
  { title: 'Action', dataIndex: 'action', key: 'action' },
  { title: 'Resource', dataIndex: 'resourceType', key: 'resourceType' },
  { title: 'Detail', dataIndex: 'detail', key: 'detail' },
  { title: 'Created', dataIndex: 'createdAt', key: 'createdAt' },
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
    if (!authenticated) {
      return
    }
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

  return {
    dashboard,
    providers,
    storageProfiles,
    strategies,
    prompts,
    auditLogs,
    loading,
    reload: load,
  }
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
    if (!data.dashboard) {
      return []
    }
    return [
      { title: 'Providers', value: `${data.dashboard.providerCount}`, detail: `${data.dashboard.activeProviderCount} active model routes` },
      { title: 'Storage profiles', value: `${data.dashboard.storageProfileCount}`, detail: 'S3 / MinIO / OSS-compatible destinations' },
      { title: 'Prompt templates', value: `${data.dashboard.promptCount}`, detail: `${data.dashboard.strategyCount} generation strategies governed` },
    ]
  }, [data.dashboard])

  return (
    <div className="admin-shell">
      <motion.section className="admin-hero" initial={{ opacity: 0, y: 20 }} animate={{ opacity: 1, y: 0 }} transition={{ duration: 0.5 }}>
        <div>
          <span className="eyebrow">Admin control plane</span>
          <h1>Govern prompts, providers, storage routes, and async dataset throughput.</h1>
          <p>The Phase 2 control plane now persists real configuration data, encrypted secrets, strategies, and audit records.</p>
        </div>
      </motion.section>

      {!authenticated ? (
        <Card className="glass-panel login-panel">
          <div className="card-header">
            <div>
              <span className="eyebrow">Secure entry</span>
              <h2>Sign in to unlock the admin workspace.</h2>
            </div>
            <LockKeyhole size={18} strokeWidth={1.8} />
          </div>
          <Form layout="vertical" onFinish={() => setAuthenticated(true)}>
            <Row gutter={[18, 0]}>
              <Col xs={24} md={12}>
                <Form.Item name="email" label="Administrator email" rules={[{ required: true, message: 'Enter an administrator email.' }]}>
                  <Input size="large" placeholder="admin@company.com" />
                </Form.Item>
              </Col>
              <Col xs={24} md={12}>
                <Form.Item name="password" label="Access key" rules={[{ required: true, message: 'Enter your access key.' }]}>
                  <Input.Password size="large" placeholder="Enter your secure key" />
                </Form.Item>
              </Col>
            </Row>
            <Button type="primary" htmlType="submit" size="large">Enter admin workspace</Button>
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
                    <span className="eyebrow">Pipeline monitor</span>
                    <h2>Configuration and governance center</h2>
                  </div>
                  <Workflow size={18} strokeWidth={1.8} />
                </div>
                <Tabs
                  defaultActiveKey="providers"
                  items={[
                    {
                      key: 'providers',
                      label: 'Providers',
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
                                message.success('Provider saved')
                              }}
                            >
                              <Form.Item name="name" label="Provider name" rules={[{ required: true }]}><Input /></Form.Item>
                              <Form.Item name="baseUrl" label="Base URL" rules={[{ required: true }]}><Input /></Form.Item>
                              <Form.Item name="model" label="Model" rules={[{ required: true }]}><Input /></Form.Item>
                              <Form.Item name="providerType" label="Type" rules={[{ required: true }]}><Input /></Form.Item>
                              <Space size={12} className="inline-controls">
                                <Form.Item name="maxConcurrency" label="Max concurrency"><InputNumber min={1} /></Form.Item>
                                <Form.Item name="timeoutSeconds" label="Timeout seconds"><InputNumber min={1} /></Form.Item>
                              </Space>
                              <Form.Item name="apiKey" label="API key"><Input.Password /></Form.Item>
                              <Form.Item name="isActive" label="Active" valuePropName="checked"><Switch /></Form.Item>
                              <Button type="primary" htmlType="submit">Save provider</Button>
                            </Form>
                          </Card>
                        </div>
                      ),
                    },
                    {
                      key: 'storage',
                      label: 'Storage',
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
                                message.success('Storage profile saved')
                              }}
                            >
                              <Form.Item name="name" label="Profile name" rules={[{ required: true }]}><Input /></Form.Item>
                              <Form.Item name="provider" label="Provider" rules={[{ required: true }]}><Input /></Form.Item>
                              <Form.Item name="endpoint" label="Endpoint" rules={[{ required: true }]}><Input /></Form.Item>
                              <Form.Item name="region" label="Region"><Input /></Form.Item>
                              <Form.Item name="bucket" label="Bucket" rules={[{ required: true }]}><Input /></Form.Item>
                              <Form.Item name="accessKeyId" label="Access key ID" rules={[{ required: true }]}><Input /></Form.Item>
                              <Form.Item name="secretAccessKey" label="Secret access key"><Input.Password /></Form.Item>
                              <Space size={12} className="inline-controls switch-row">
                                <Form.Item name="usePathStyle" label="Use path style" valuePropName="checked"><Switch /></Form.Item>
                                <Form.Item name="isDefault" label="Default" valuePropName="checked"><Switch /></Form.Item>
                              </Space>
                              <Button type="primary" htmlType="submit">Save storage profile</Button>
                            </Form>
                          </Card>
                        </div>
                      ),
                    },
                    {
                      key: 'strategies',
                      label: 'Strategies',
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
                                message.success('Strategy saved')
                              }}
                            >
                              <Form.Item name="name" label="Strategy name" rules={[{ required: true }]}><Input /></Form.Item>
                              <Form.Item name="description" label="Description"><Input.TextArea rows={3} /></Form.Item>
                              <Form.Item name="planningMode" label="Planning mode">
                                <Segmented options={['balanced', 'wide-first', 'deep-first', 'cost-saving']} />
                              </Form.Item>
                              <Space size={12} wrap className="inline-controls">
                                <Form.Item name="domainCount" label="Domains"><InputNumber min={1} /></Form.Item>
                                <Form.Item name="questionsPerDomain" label="Questions / domain"><InputNumber min={1} /></Form.Item>
                                <Form.Item name="answerVariants" label="Answer variants"><InputNumber min={1} /></Form.Item>
                                <Form.Item name="rewardVariants" label="Reward variants"><InputNumber min={1} /></Form.Item>
                              </Space>
                              <Form.Item name="isDefault" label="Default" valuePropName="checked"><Switch /></Form.Item>
                              <Button type="primary" htmlType="submit">Save strategy</Button>
                            </Form>
                          </Card>
                        </div>
                      ),
                    },
                    {
                      key: 'prompts',
                      label: 'Prompts',
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
                                message.success('Prompt saved')
                              }}
                            >
                              <Form.Item name="name" label="Prompt name" rules={[{ required: true }]}><Input /></Form.Item>
                              <Form.Item name="stage" label="Stage" rules={[{ required: true }]}><Input /></Form.Item>
                              <Form.Item name="version" label="Version" rules={[{ required: true }]}><Input /></Form.Item>
                              <Form.Item name="systemPrompt" label="System prompt" rules={[{ required: true }]}><Input.TextArea rows={4} /></Form.Item>
                              <Form.Item name="userPrompt" label="User prompt" rules={[{ required: true }]}><Input.TextArea rows={4} /></Form.Item>
                              <Form.Item name="isActive" label="Active" valuePropName="checked"><Switch /></Form.Item>
                              <Button type="primary" htmlType="submit">Save prompt</Button>
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
                      <span className="eyebrow">Audit trail</span>
                      <h2>Recent governance events</h2>
                    </div>
                    <Logs size={18} strokeWidth={1.8} />
                  </div>
                  <Table columns={auditColumns} dataSource={data.auditLogs} pagination={{ pageSize: 5 }} rowKey="id" loading={data.loading} size="small" />
                </Card>
                <Card className="glass-panel">
                  <div className="card-header">
                    <div>
                      <span className="eyebrow">Foundation modules</span>
                      <h2>Phase 2 scope</h2>
                    </div>
                    <Layers3 size={18} strokeWidth={1.8} />
                  </div>
                  <List
                    dataSource={[
                      { icon: FolderCog, label: 'Provider and storage configuration CRUD' },
                      { icon: Database, label: 'Encrypted secret persistence in PostgreSQL' },
                      { icon: ShieldEllipsis, label: 'Audit-ready admin information architecture' },
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
                      <span className="eyebrow">Readiness</span>
                      <h2>Phase completion confidence</h2>
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
