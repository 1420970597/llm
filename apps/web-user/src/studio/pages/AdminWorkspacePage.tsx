import { useCallback, useEffect, useMemo, useState } from 'react'
import type { ReactNode } from 'react'
import { useNavigate } from 'react-router-dom'
import {
  Button,
  Card,
  Empty,
  Input,
  InputNumber,
  Modal,
  Select,
  Space,
  Spin,
  Switch,
  TabPane,
  Table,
  Tabs,
  Tag,
  TextArea,
  Toast,
  Typography,
} from '@douyinfe/semi-ui'
import {
  Activity,
  Database,
  FileClock,
  FileJson,
  FolderCog,
  Plus,
  RefreshCw,
  ServerCog,
  Settings2,
  Sparkles,
  Trash2,
  Workflow,
} from 'lucide-react'
import {
  consoleApi,
  type AuditRecord,
  type DashboardRecord,
  type Dataset,
  type ExportMapping,
  type PromptRecord,
  type Provider,
  type RuntimeStatus,
  type StorageProfile,
  type Strategy,
} from '../../lib/api'

const { Title, Text } = Typography

type ProviderDraft = Partial<Provider> & { apiKey?: string }
type StorageDraft = Partial<StorageProfile> & { secretAccessKey?: string }
type MappingField = { key: string; expression: string }
type MappingDraft = Partial<ExportMapping> & {
  fieldMap: Record<string, unknown>
  options: Record<string, unknown>
}

type AdminTabKey = 'operations' | 'providers' | 'storage' | 'strategies' | 'prompts' | 'mappings' | 'audit'

const ADMIN_TAB_KEYS: AdminTabKey[] = ['operations', 'providers', 'storage', 'strategies', 'prompts', 'mappings', 'audit']

function adminTabFromHash(hash: string): AdminTabKey {
  const [, tab] = hash.replace(/^#/, '').split('/')
  return ADMIN_TAB_KEYS.includes(tab as AdminTabKey) ? tab as AdminTabKey : 'operations'
}

const emptyProvider: ProviderDraft = {
  name: '',
  baseUrl: '',
  model: '',
  providerType: 'openai-compatible',
  reasoningEffort: '',
  maxConcurrency: 4,
  timeoutSeconds: 120,
  isActive: true,
  apiKey: '',
}

const emptyStorage: StorageDraft = {
  name: '本地 MinIO',
  provider: 'minio',
  endpoint: 'http://minio:9000',
  region: 'us-east-1',
  bucket: '',
  accessKeyId: '',
  secretAccessKey: '',
  usePathStyle: true,
  isActive: true,
  isDefault: false,
}

const emptyStrategy: Partial<Strategy> = {
  name: '',
  description: '',
  domainCount: 5,
  questionsPerDomain: 10,
  answerVariants: 1,
  rewardVariants: 1,
  planningMode: 'balanced',
  isDefault: false,
}

const emptyPrompt: PromptRecord = {
  name: '',
  stage: 'domain-generation',
  version: 'v1',
  systemPrompt: '',
  userPrompt: '',
  isActive: true,
}

const EXPORT_FORMATS = ['jsonl', 'csv', 'parquet', 'alpaca', 'sharegpt']
const emptyMapping: MappingDraft = {
  name: '',
  format: 'jsonl',
  targetKind: 'sft',
  fieldMap: {},
  options: {},
  isBuiltin: false,
  isDefault: false,
}

/**
 * Atelier 管理工作区。
 *
 * 旧控制台的治理能力并不是演示页：这里直接调用原有 admin API，保留
 * 新建/编辑、连通性测试、模型发现、默认配置和审计读取。旧 /console/admin/*
 * 深链仍然存在，但管理员从 Atelier 也能完成同一组操作。
 */
export function AdminWorkspacePage() {
  const [activeTab, setActiveTab] = useState<AdminTabKey>(() => adminTabFromHash(window.location.hash))
  const [loading, setLoading] = useState(true)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [dashboard, setDashboard] = useState<DashboardRecord | null>(null)
  const [runtime, setRuntime] = useState<RuntimeStatus | null>(null)
  const [providers, setProviders] = useState<Provider[]>([])
  const [storageProfiles, setStorageProfiles] = useState<StorageProfile[]>([])
  const [strategies, setStrategies] = useState<Strategy[]>([])
  const [prompts, setPrompts] = useState<PromptRecord[]>([])
  const [auditLogs, setAuditLogs] = useState<AuditRecord[]>([])
  const [exportMappings, setExportMappings] = useState<ExportMapping[]>([])
  const [datasets, setDatasets] = useState<Dataset[]>([])
  const navigate = useNavigate()
  const recentDatasets = useMemo(
    () => [...datasets].sort((left, right) => (Date.parse(right.updatedAt) || 0) - (Date.parse(left.updatedAt) || 0)).slice(0, 6),
    [datasets],
  )

  const [providerDraft, setProviderDraft] = useState<ProviderDraft>(emptyProvider)
  const [providerModal, setProviderModal] = useState(false)
  const [providerTest, setProviderTest] = useState<string | null>(null)
  const [providerModels, setProviderModels] = useState<string[]>([])
  const [storageDraft, setStorageDraft] = useState<StorageDraft>(emptyStorage)
  const [storageModal, setStorageModal] = useState(false)
  const [strategyDraft, setStrategyDraft] = useState<Partial<Strategy>>(emptyStrategy)
  const [strategyModal, setStrategyModal] = useState(false)
  const [promptDraft, setPromptDraft] = useState<PromptRecord>(emptyPrompt)
  const [promptModal, setPromptModal] = useState(false)
  const [mappingDraft, setMappingDraft] = useState<MappingDraft>(emptyMapping)
  const [mappingFields, setMappingFields] = useState<MappingField[]>([{ key: '', expression: '' }])
  const [mappingOptionsText, setMappingOptionsText] = useState('{}')
  const [mappingModal, setMappingModal] = useState(false)
  const [mappingError, setMappingError] = useState<string | null>(null)

  const load = useCallback(async () => {
    setLoading(true)
    setError(null)
    try {
      const [nextDashboard, nextRuntime, nextProviders, nextStorage, nextStrategies, nextPrompts, nextAudit, nextMappings, nextDatasets] = await Promise.all([
        consoleApi.dashboard(),
        consoleApi.runtimeStatus(),
        consoleApi.listProviders(),
        consoleApi.listStorageProfiles(),
        consoleApi.listStrategies(),
        consoleApi.listPrompts(),
        consoleApi.listAuditLogs(),
        consoleApi.listExportMappings(),
        consoleApi.listDatasets(),
      ])
      setDashboard(nextDashboard)
      setRuntime(nextRuntime)
      setProviders(nextProviders)
      setStorageProfiles(nextStorage)
      setStrategies(nextStrategies)
      setPrompts(nextPrompts)
      setAuditLogs(nextAudit)
      setExportMappings(nextMappings)
      setDatasets(nextDatasets)
    } catch (loadError) {
      setError(loadError instanceof Error ? loadError.message : '加载管理数据失败')
    } finally {
      setLoading(false)
    }
  }, [])

  useEffect(() => {
    void load()
  }, [load])

  useEffect(() => {
    const syncTab = () => setActiveTab(adminTabFromHash(window.location.hash))
    window.addEventListener('hashchange', syncTab)
    return () => window.removeEventListener('hashchange', syncTab)
  }, [])

  const saveProvider = async () => {
    setBusy(true)
    try {
      await consoleApi.saveProvider(providerDraft)
      setProviderModal(false)
      await load()
      Toast.success('模型服务已保存')
    } catch (saveError) {
      Toast.error(saveError instanceof Error ? saveError.message : '保存模型服务失败')
    } finally {
      setBusy(false)
    }
  }

  const testProvider = async () => {
    setBusy(true)
    try {
      const result = await consoleApi.testProviderConnectivity(providerDraft)
      setProviderTest(result.ok ? `连接成功 · ${result.latencyMs} ms` : result.message || '连接失败')
      if (result.availableModels) setProviderModels(result.availableModels)
    } catch (testError) {
      setProviderTest(testError instanceof Error ? testError.message : '连接测试失败')
    } finally {
      setBusy(false)
    }
  }

  const fetchModels = async () => {
    setBusy(true)
    try {
      const result = await consoleApi.fetchProviderModels(providerDraft)
      setProviderModels((result.models ?? []).map((model) => model.id))
      Toast.success(`已读取 ${result.models?.length ?? 0} 个模型`)
    } catch (modelError) {
      Toast.error(modelError instanceof Error ? modelError.message : '读取模型列表失败')
    } finally {
      setBusy(false)
    }
  }

  const saveStorage = async () => {
    setBusy(true)
    try {
      await consoleApi.saveStorageProfile(storageDraft)
      setStorageModal(false)
      await load()
      Toast.success('存储配置已保存')
    } catch (saveError) {
      Toast.error(saveError instanceof Error ? saveError.message : '保存存储配置失败')
    } finally {
      setBusy(false)
    }
  }

  const saveStrategy = async () => {
    setBusy(true)
    try {
      await consoleApi.saveStrategy(strategyDraft)
      setStrategyModal(false)
      await load()
      Toast.success('生成策略已保存')
    } catch (saveError) {
      Toast.error(saveError instanceof Error ? saveError.message : '保存生成策略失败')
    } finally {
      setBusy(false)
    }
  }

  const savePrompt = async () => {
    setBusy(true)
    try {
      await consoleApi.savePrompt(promptDraft)
      setPromptModal(false)
      await load()
      Toast.success('提示词模板已保存')
    } catch (saveError) {
      Toast.error(saveError instanceof Error ? saveError.message : '保存提示词失败')
    } finally {
      setBusy(false)
    }
  }

  const openMappingEditor = (item?: ExportMapping, clone = false) => {
    const source = item ?? emptyMapping
    const fieldMap = source.fieldMap ?? {}
    const entries = Object.entries(fieldMap).map(([key, value]) => ({
      key,
      expression: typeof value === 'string' ? value : JSON.stringify(value),
    }))
    setMappingDraft({
      ...emptyMapping,
      ...source,
      ...(clone ? { id: undefined, name: `${source.name ?? '映射'} 副本`, isBuiltin: false, isDefault: false } : {}),
      fieldMap,
      options: source.options ?? {},
    })
    setMappingFields(entries.length > 0 ? entries : [{ key: '', expression: '' }])
    setMappingOptionsText(JSON.stringify(source.options ?? {}, null, 2))
    setMappingError(null)
    setMappingModal(true)
  }

  const saveMapping = async () => {
    const name = String(mappingDraft.name ?? '').trim()
    if (!name) {
      setMappingError('请填写映射名称')
      return
    }
    const fieldMap: Record<string, string> = {}
    for (const field of mappingFields) {
      const key = field.key.trim()
      const expression = field.expression.trim()
      if (!key && !expression) continue
      if (!key || !expression) {
        setMappingError('每一行都需要同时填写目标字段和来源表达式')
        return
      }
      if (fieldMap[key]) {
        setMappingError(`目标字段「${key}」重复，请合并后再保存`)
        return
      }
      fieldMap[key] = expression
    }
    let options: Record<string, unknown> = {}
    if (mappingOptionsText.trim()) {
      try {
        const parsed: unknown = JSON.parse(mappingOptionsText)
        if (!parsed || typeof parsed !== 'object' || Array.isArray(parsed)) {
          setMappingError('选项必须是 JSON 对象，例如 {"includeMetadata": true}')
          return
        }
        options = parsed as Record<string, unknown>
      } catch {
        setMappingError('选项 JSON 格式不正确，请检查逗号和引号')
        return
      }
    }
    setBusy(true)
    setMappingError(null)
    try {
      await consoleApi.saveExportMapping({
        ...mappingDraft,
        name,
        format: mappingDraft.format || 'jsonl',
        targetKind: mappingDraft.targetKind || 'sft',
        fieldMap,
        options,
      })
      setMappingModal(false)
      await load()
      Toast.success('导出映射已保存')
    } catch (saveError) {
      setMappingError(saveError instanceof Error ? saveError.message : '保存导出映射失败')
    } finally {
      setBusy(false)
    }
  }

  const providerColumns = useMemo(() => [
    { title: '名称', dataIndex: 'name', key: 'name' },
    { title: '模型', dataIndex: 'model', key: 'model' },
    { title: '地址', dataIndex: 'baseUrl', key: 'baseUrl' },
    { title: '状态', key: 'status', render: (_: unknown, item: Provider) => <Tag color={item.isActive ? 'green' : 'grey'}>{item.isActive ? '启用' : '停用'}</Tag> },
    { title: '操作', key: 'actions', render: (_: unknown, item: Provider) => <Button size="small" onClick={() => { setProviderDraft({ ...item, apiKey: '' }); setProviderTest(null); setProviderModels([]); setProviderModal(true) }}>编辑</Button> },
  ], [])

  const storageColumns = useMemo(() => [
    { title: '名称', dataIndex: 'name', key: 'name' },
    { title: '类型', dataIndex: 'provider', key: 'provider' },
    { title: 'Bucket', dataIndex: 'bucket', key: 'bucket' },
    { title: '状态', key: 'status', render: (_: unknown, item: StorageProfile) => <Space><Tag color={item.isActive ? 'green' : 'grey'}>{item.isActive ? '启用' : '停用'}</Tag>{item.isDefault ? <Tag color="blue">默认</Tag> : null}</Space> },
    { title: '操作', key: 'actions', render: (_: unknown, item: StorageProfile) => <Button size="small" onClick={() => { setStorageDraft({ ...item, secretAccessKey: '' }); setStorageModal(true) }}>编辑</Button> },
  ], [])

  const strategyColumns = useMemo(() => [
    { title: '名称', dataIndex: 'name', key: 'name' },
    { title: '规模', key: 'scale', render: (_: unknown, item: Strategy) => `${item.domainCount} 领域 × ${item.questionsPerDomain} 题` },
    { title: '规划模式', dataIndex: 'planningMode', key: 'planningMode' },
    { title: '状态', key: 'status', render: (_: unknown, item: Strategy) => item.isDefault ? <Tag color="blue">默认</Tag> : <Tag color="grey">普通</Tag> },
    { title: '操作', key: 'actions', render: (_: unknown, item: Strategy) => <Button size="small" onClick={() => { setStrategyDraft({ ...item }); setStrategyModal(true) }}>编辑</Button> },
  ], [])

  const promptColumns = useMemo(() => [
    { title: '名称', dataIndex: 'name', key: 'name' },
    { title: '阶段', dataIndex: 'stage', key: 'stage' },
    { title: '版本', dataIndex: 'version', key: 'version' },
    { title: '状态', key: 'status', render: (_: unknown, item: PromptRecord) => <Tag color={item.isActive ? 'green' : 'grey'}>{item.isActive ? '启用' : '停用'}</Tag> },
    { title: '操作', key: 'actions', render: (_: unknown, item: PromptRecord) => <Button size="small" onClick={() => { setPromptDraft({ ...item }); setPromptModal(true) }}>编辑</Button> },
  ], [])

  const mappingColumns = useMemo(() => [
    { title: '名称', dataIndex: 'name', key: 'name' },
    { title: '格式', dataIndex: 'format', key: 'format', render: (value: string) => <Tag color="blue">{value}</Tag> },
    { title: '训练类型', dataIndex: 'targetKind', key: 'targetKind', render: (value: string) => value === 'grpo' ? 'GRPO' : 'SFT' },
    { title: '字段', key: 'fields', render: (_: unknown, item: ExportMapping) => `${Object.keys(item.fieldMap ?? {}).length} 个` },
    { title: '状态', key: 'status', render: (_: unknown, item: ExportMapping) => <Space><Tag color={item.isDefault ? 'blue' : 'grey'}>{item.isDefault ? '默认' : '可选'}</Tag>{item.isBuiltin ? <Tag color="green">内置</Tag> : <Tag color="orange">自定义</Tag>}</Space> },
    {
      title: '操作',
      key: 'actions',
      render: (_: unknown, item: ExportMapping) => (
        <Space>
          <Button size="small" onClick={() => openMappingEditor(item)}>编辑</Button>
          <Button size="small" type="tertiary" onClick={() => openMappingEditor(item, true)}>复制</Button>
        </Space>
      ),
    },
  ], [])

  if (loading) {
    return <div className="flex justify-center py-10"><Spin tip="正在加载治理数据" /></div>
  }

  return (
    <div className="console-page" data-studio-page="admin-workspace">
      <div className="console-page__header">
        <div>
          <div className="eyebrow">WORKSPACE GOVERNANCE</div>
          <Title heading={4} className="!mb-1">系统治理工作区</Title>
          <Text type="tertiary">管理员在 Atelier 内完成运行监控、连接、策略、提示词、导出映射与审计。密钥只写入，不回显。</Text>
        </div>
        <Button icon={<RefreshCw size={14} />} loading={loading} onClick={() => void load()}>刷新</Button>
      </div>

      {error ? <Card className="console-card mb-3" bodyStyle={{ padding: 14 }}><Text type="danger">{error}</Text></Card> : null}

      <Tabs
        activeKey={activeTab}
        onChange={(key) => {
          const next = adminTabFromHash(`#admin-governance/${key}`)
          setActiveTab(next)
          window.history.replaceState(null, '', `${window.location.pathname}${window.location.search}#admin-governance/${next}`)
        }}
        type="line"
      >
        <TabPane tab={<span><Activity size={14} /> 运行监控</span>} itemKey="operations">
          <div className="console-card-grid-4">
            <Metric label="队列等待" value={runtime?.queueDepth ?? 0} icon={<ServerCog size={16} />} />
            <Metric label="活跃模型服务" value={dashboard?.activeProviderCount ?? 0} icon={<Database size={16} />} />
            <Metric label="可用存储" value={dashboard?.storageProfileCount ?? 0} icon={<FolderCog size={16} />} />
            <Metric label="审计记录" value={dashboard?.auditLogCount ?? 0} icon={<FileClock size={16} />} />
          </div>
          <Card className="console-card mt-3" bodyStyle={{ padding: 16 }}>
            <div className="console-summary-grid">
              <div className="console-summary-row"><span>任务总数</span><Text strong>{runtime?.datasetCount ?? 0}</Text></div>
              <div className="console-summary-row"><span>题目结果</span><Text strong>{runtime?.questionCount ?? 0}</Text></div>
              <div className="console-summary-row"><span>答案结果</span><Text strong>{runtime?.reasoningCount ?? 0}</Text></div>
              <div className="console-summary-row"><span>评分结果</span><Text strong>{runtime?.rewardCount ?? 0}</Text></div>
              <div className="console-summary-row"><span>交付文件</span><Text strong>{runtime?.artifactCount ?? 0}</Text></div>
              <div className="console-summary-row"><span>导出映射</span><Text strong>{exportMappings.length}</Text></div>
            </div>
          </Card>
          <div className="console-card-grid-2 mt-3">
            <Card className="console-card" bodyStyle={{ padding: 16 }}>
              <div className="flex items-center justify-between gap-3">
                <Title heading={6} className="!mb-0">最近任务活动</Title>
                <Button size="small" onClick={() => navigate('/console/tasks')}>全部旧任务</Button>
              </div>
              {recentDatasets.length ? (
                <div className="console-stack mt-3">
                  {recentDatasets.map((dataset) => (
                    <div className="console-domain-item" key={dataset.id}>
                      <div className="flex items-center justify-between gap-3">
                        <div className="min-w-0">
                          <Text strong className="block">{dataset.name}</Text>
                          <Text type="tertiary" size="small">#{dataset.id} · {dataset.status} · {new Date(dataset.updatedAt).toLocaleString('zh-CN', { hour12: false })}</Text>
                        </div>
                        <Button size="small" onClick={() => navigate(`/console/tasks/${dataset.id}`)}>查看任务</Button>
                      </div>
                    </div>
                  ))}
                </div>
              ) : <Empty description="暂无旧版任务活动" />}
            </Card>
            <Card className="console-card" bodyStyle={{ padding: 16 }}>
              <div className="flex items-center justify-between gap-3">
                <Title heading={6} className="!mb-0">最近操作</Title>
                <Button size="small" onClick={() => {
                  setActiveTab('audit')
                  window.history.replaceState(null, '', `${window.location.pathname}${window.location.search}#admin-governance/audit`)
                }}>完整审计</Button>
              </div>
              {auditLogs.length ? <Table
                size="small"
                columns={[
                  { title: '时间', dataIndex: 'createdAt', key: 'createdAt' },
                  { title: '操作者', dataIndex: 'actor', key: 'actor' },
                  { title: '动作', dataIndex: 'action', key: 'action' },
                  { title: '资源', key: 'resource', render: (_: unknown, item: AuditRecord) => `${item.resourceType} #${item.resourceId}` },
                ]}
                dataSource={auditLogs.slice(0, 6)}
                rowKey="id"
                pagination={false}
              /> : <Empty description="暂无操作记录" />}
            </Card>
          </div>
        </TabPane>

        <TabPane tab={<span><Database size={14} /> 模型服务</span>} itemKey="providers">
          <ResourceHeader title="AI 服务" action="新增服务" onAction={() => { setProviderDraft({ ...emptyProvider }); setProviderTest(null); setProviderModels([]); setProviderModal(true) }} />
          <Card className="console-card" bodyStyle={{ padding: 12 }}>
            {providers.length ? <Table columns={providerColumns} dataSource={providers} rowKey="id" pagination={false} /> : <Empty description="还没有模型服务" />}
          </Card>
        </TabPane>

        <TabPane tab={<span><FolderCog size={14} /> 结果存储</span>} itemKey="storage">
          <ResourceHeader title="结果存储" action="新增存储" onAction={() => { setStorageDraft({ ...emptyStorage }); setStorageModal(true) }} />
          <Card className="console-card" bodyStyle={{ padding: 12 }}>
            {storageProfiles.length ? <Table columns={storageColumns} dataSource={storageProfiles} rowKey="id" pagination={false} /> : <Empty description="还没有存储配置" />}
          </Card>
        </TabPane>

        <TabPane tab={<span><Workflow size={14} /> 生成策略</span>} itemKey="strategies">
          <ResourceHeader title="生成策略" action="新增策略" onAction={() => { setStrategyDraft({ ...emptyStrategy }); setStrategyModal(true) }} />
          <Card className="console-card" bodyStyle={{ padding: 12 }}>
            {strategies.length ? <Table columns={strategyColumns} dataSource={strategies} rowKey="id" pagination={false} /> : <Empty description="还没有生成策略" />}
          </Card>
        </TabPane>

        <TabPane tab={<span><Sparkles size={14} /> 生成指令</span>} itemKey="prompts">
          <ResourceHeader title="生成指令模板" action="新增模板" onAction={() => { setPromptDraft({ ...emptyPrompt }); setPromptModal(true) }} />
          <Card className="console-card" bodyStyle={{ padding: 12 }}>
            {prompts.length ? <Table columns={promptColumns} dataSource={prompts} rowKey="id" pagination={false} /> : <Empty description="还没有提示词模板" />}
          </Card>
        </TabPane>

        <TabPane tab={<span><FileJson size={14} /> 导出映射</span>} itemKey="mappings">
          <ResourceHeader title="导出字段映射" action="新增映射" onAction={() => openMappingEditor()} />
          <Card className="console-toolbar-card mb-3" bodyStyle={{ padding: 14 }}>
            <Text type="tertiary">
              映射决定发布文件的目标字段。来源表达式支持字段名、{'{{field}}'} 模板和 {'const:常量'}；保存后会被具体导出任务按映射 ID 冻结。
            </Text>
          </Card>
          <Card className="console-card" bodyStyle={{ padding: 12 }} data-admin-export-mappings="true">
            {exportMappings.length ? <Table columns={mappingColumns} dataSource={exportMappings} rowKey="id" pagination={false} /> : <Empty description="还没有导出映射" />}
          </Card>
        </TabPane>

        <TabPane tab={<span><Settings2 size={14} /> 操作记录</span>} itemKey="audit">
          <Card className="console-card" bodyStyle={{ padding: 12 }}>
            {auditLogs.length ? <Table columns={[
              { title: '时间', dataIndex: 'createdAt', key: 'createdAt' },
              { title: '操作者', dataIndex: 'actor', key: 'actor' },
              { title: '动作', dataIndex: 'action', key: 'action' },
              { title: '资源', key: 'resource', render: (_: unknown, item: AuditRecord) => `${item.resourceType} #${item.resourceId}` },
              { title: '详情', dataIndex: 'detail', key: 'detail' },
            ]} dataSource={auditLogs} rowKey="id" pagination={{ pageSize: 8 }} /> : <Empty description="还没有操作记录" />}
          </Card>
        </TabPane>
      </Tabs>

      <Modal title={providerDraft.id ? '编辑模型服务' : '新增模型服务'} visible={providerModal} onCancel={() => setProviderModal(false)} width={860} footer={
        <Space><Button onClick={() => setProviderModal(false)}>取消</Button><Button loading={busy} onClick={() => void fetchModels()}>获取模型列表</Button><Button loading={busy} onClick={() => void testProvider()}>测试连接</Button><Button theme="solid" type="primary" loading={busy} onClick={() => void saveProvider()}>保存</Button></Space>
      }>
        <EditorGrid>
          <Field label="服务名称"><Input value={providerDraft.name ?? ''} onChange={(value) => setProviderDraft((current) => ({ ...current, name: value }))} /></Field>
          <Field label="协议类型"><Select value={providerDraft.providerType ?? 'openai-compatible'} optionList={[{ value: 'openai-compatible', label: 'OpenAI Compatible' }, { value: 'custom', label: 'Custom Compatible' }]} onChange={(value) => setProviderDraft((current) => ({ ...current, providerType: String(value) }))} style={{ width: '100%' }} /></Field>
          <Field label="基础 URL"><Input value={providerDraft.baseUrl ?? ''} onChange={(value) => setProviderDraft((current) => ({ ...current, baseUrl: value }))} /></Field>
          <Field label="模型名称"><Input value={providerDraft.model ?? ''} onChange={(value) => setProviderDraft((current) => ({ ...current, model: value }))} /></Field>
          <Field label="最大并发"><InputNumber value={providerDraft.maxConcurrency ?? 4} onChange={(value) => setProviderDraft((current) => ({ ...current, maxConcurrency: Number(value ?? 0) }))} style={{ width: '100%' }} /></Field>
          <Field label="超时秒数"><InputNumber value={providerDraft.timeoutSeconds ?? 120} onChange={(value) => setProviderDraft((current) => ({ ...current, timeoutSeconds: Number(value ?? 0) }))} style={{ width: '100%' }} /></Field>
          <Field label="API 密钥"><Input mode="password" autoComplete="new-password" placeholder="留空则沿用已保存密钥" value={providerDraft.apiKey ?? ''} onChange={(value) => setProviderDraft((current) => ({ ...current, apiKey: value }))} /></Field>
          <Field label="启用"><Switch checked={providerDraft.isActive !== false} onChange={(checked) => setProviderDraft((current) => ({ ...current, isActive: checked }))} /></Field>
        </EditorGrid>
        {providerTest ? <Text type={providerTest.startsWith('连接成功') ? 'success' : 'danger'} className="block mt-3">{providerTest}</Text> : null}
        {providerModels.length ? <Text type="tertiary" className="block mt-2">可用模型：{providerModels.join('、')}</Text> : null}
      </Modal>

      <Modal title={storageDraft.id ? '编辑结果存储' : '新增结果存储'} visible={storageModal} onCancel={() => setStorageModal(false)} width={860} footer={<Space><Button onClick={() => setStorageModal(false)}>取消</Button><Button theme="solid" type="primary" loading={busy} onClick={() => void saveStorage()}>保存</Button></Space>}>
        <EditorGrid>
          <Field label="名称"><Input value={storageDraft.name ?? ''} onChange={(value) => setStorageDraft((current) => ({ ...current, name: value }))} /></Field>
          <Field label="类型"><Input value={storageDraft.provider ?? ''} onChange={(value) => setStorageDraft((current) => ({ ...current, provider: value }))} /></Field>
          <Field label="Endpoint"><Input value={storageDraft.endpoint ?? ''} onChange={(value) => setStorageDraft((current) => ({ ...current, endpoint: value }))} /></Field>
          <Field label="Region"><Input value={storageDraft.region ?? ''} onChange={(value) => setStorageDraft((current) => ({ ...current, region: value }))} /></Field>
          <Field label="Bucket"><Input value={storageDraft.bucket ?? ''} onChange={(value) => setStorageDraft((current) => ({ ...current, bucket: value }))} /></Field>
          <Field label="Access Key"><Input value={storageDraft.accessKeyId ?? ''} onChange={(value) => setStorageDraft((current) => ({ ...current, accessKeyId: value }))} /></Field>
          <Field label="Secret Key"><Input mode="password" autoComplete="new-password" placeholder="留空则沿用已保存密钥" value={storageDraft.secretAccessKey ?? ''} onChange={(value) => setStorageDraft((current) => ({ ...current, secretAccessKey: value }))} /></Field>
          <Field label="默认/启用"><Space><Switch checked={storageDraft.isDefault === true} onChange={(checked) => setStorageDraft((current) => ({ ...current, isDefault: checked }))} /><Switch checked={storageDraft.isActive !== false} onChange={(checked) => setStorageDraft((current) => ({ ...current, isActive: checked }))} /></Space></Field>
        </EditorGrid>
      </Modal>

      <Modal title={strategyDraft.id ? '编辑生成策略' : '新增生成策略'} visible={strategyModal} onCancel={() => setStrategyModal(false)} width={760} footer={<Space><Button onClick={() => setStrategyModal(false)}>取消</Button><Button theme="solid" type="primary" loading={busy} onClick={() => void saveStrategy()}>保存</Button></Space>}>
        <EditorGrid>
          <Field label="策略名称"><Input value={strategyDraft.name ?? ''} onChange={(value) => setStrategyDraft((current) => ({ ...current, name: value }))} /></Field>
          <Field label="规划模式"><Input value={strategyDraft.planningMode ?? ''} onChange={(value) => setStrategyDraft((current) => ({ ...current, planningMode: value }))} /></Field>
          <Field label="领域数"><InputNumber value={strategyDraft.domainCount ?? 5} onChange={(value) => setStrategyDraft((current) => ({ ...current, domainCount: Number(value ?? 0) }))} style={{ width: '100%' }} /></Field>
          <Field label="每领域问题数"><InputNumber value={strategyDraft.questionsPerDomain ?? 10} onChange={(value) => setStrategyDraft((current) => ({ ...current, questionsPerDomain: Number(value ?? 0) }))} style={{ width: '100%' }} /></Field>
          <Field label="答案变体数"><InputNumber value={strategyDraft.answerVariants ?? 1} onChange={(value) => setStrategyDraft((current) => ({ ...current, answerVariants: Number(value ?? 0) }))} style={{ width: '100%' }} /></Field>
          <Field label="奖励变体数"><InputNumber value={strategyDraft.rewardVariants ?? 1} onChange={(value) => setStrategyDraft((current) => ({ ...current, rewardVariants: Number(value ?? 0) }))} style={{ width: '100%' }} /></Field>
          <Field label="描述"><TextArea value={strategyDraft.description ?? ''} onChange={(value) => setStrategyDraft((current) => ({ ...current, description: value }))} autosize={{ minRows: 3, maxRows: 6 }} /></Field>
          <Field label="默认策略"><Switch checked={strategyDraft.isDefault === true} onChange={(checked) => setStrategyDraft((current) => ({ ...current, isDefault: checked }))} /></Field>
        </EditorGrid>
      </Modal>

      <Modal title={promptDraft.id ? '编辑生成指令' : '新增生成指令'} visible={promptModal} onCancel={() => setPromptModal(false)} width={900} footer={<Space><Button onClick={() => setPromptModal(false)}>取消</Button><Button theme="solid" type="primary" loading={busy} onClick={() => void savePrompt()}>保存</Button></Space>}>
        <EditorGrid>
          <Field label="模板名称"><Input value={promptDraft.name} onChange={(value) => setPromptDraft((current) => ({ ...current, name: value }))} /></Field>
          <Field label="阶段"><Input value={promptDraft.stage} onChange={(value) => setPromptDraft((current) => ({ ...current, stage: value }))} /></Field>
          <Field label="版本"><Input value={promptDraft.version} onChange={(value) => setPromptDraft((current) => ({ ...current, version: value }))} /></Field>
          <Field label="启用"><Switch checked={promptDraft.isActive} onChange={(checked) => setPromptDraft((current) => ({ ...current, isActive: checked }))} /></Field>
          <Field label="系统指令"><TextArea value={promptDraft.systemPrompt} onChange={(value) => setPromptDraft((current) => ({ ...current, systemPrompt: value }))} autosize={{ minRows: 5, maxRows: 12 }} /></Field>
          <Field label="用户指令"><TextArea value={promptDraft.userPrompt} onChange={(value) => setPromptDraft((current) => ({ ...current, userPrompt: value }))} autosize={{ minRows: 5, maxRows: 12 }} /></Field>
        </EditorGrid>
      </Modal>

      <Modal
        title={mappingDraft.id ? '编辑导出映射' : '新增导出映射'}
        visible={mappingModal}
        onCancel={() => setMappingModal(false)}
        width={900}
        footer={(
          <Space>
            <Button onClick={() => setMappingModal(false)}>取消</Button>
            <Button theme="solid" type="primary" loading={busy} onClick={() => void saveMapping()}>保存映射</Button>
          </Space>
        )}
      >
        <div className="console-stack" data-admin-export-mapping-editor="true">
          <EditorGrid>
            <Field label="映射名称"><Input value={mappingDraft.name ?? ''} onChange={(value) => { setMappingDraft((current) => ({ ...current, name: value })); setMappingError(null) }} placeholder="例如 sft-jsonl-production" /></Field>
            <Field label="导出格式"><Select value={mappingDraft.format ?? 'jsonl'} optionList={EXPORT_FORMATS.map((format) => ({ value: format, label: format }))} onChange={(value) => setMappingDraft((current) => ({ ...current, format: String(value) }))} style={{ width: '100%' }} /></Field>
            <Field label="训练类型"><Select value={mappingDraft.targetKind ?? 'sft'} optionList={[{ value: 'sft', label: 'SFT' }, { value: 'grpo', label: 'GRPO' }]} onChange={(value) => setMappingDraft((current) => ({ ...current, targetKind: String(value) }))} style={{ width: '100%' }} /></Field>
            <Field label="默认映射"><Switch checked={mappingDraft.isDefault === true} onChange={(checked) => setMappingDraft((current) => ({ ...current, isDefault: checked }))} /></Field>
          </EditorGrid>

          <div className="admin-mapping-editor__section">
            <div className="flex items-center justify-between gap-3">
              <div>
                <Text strong>字段规则</Text>
                <Text type="tertiary" className="block text-xs">目标字段会按名称排序输出；来源可写 question、{'{{answer}}'} 或 {'const:固定值'}。</Text>
              </div>
              <Button size="small" icon={<Plus size={14} />} onClick={() => setMappingFields((current) => [...current, { key: '', expression: '' }])}>添加字段</Button>
            </div>
            <div className="admin-mapping-fields" role="list">
              {mappingFields.map((field, index) => (
                <div className="admin-mapping-field-row" role="listitem" key={`${index}-${field.key}`}>
                  <Input aria-label={`目标字段 ${index + 1}`} value={field.key} onChange={(value) => setMappingFields((current) => current.map((item, itemIndex) => itemIndex === index ? { ...item, key: value } : item))} placeholder="目标字段，例如 instruction" />
                  <Input aria-label={`来源表达式 ${index + 1}`} value={field.expression} onChange={(value) => setMappingFields((current) => current.map((item, itemIndex) => itemIndex === index ? { ...item, expression: value } : item))} placeholder="来源表达式，例如 {{question}}" />
                  <Button type="tertiary" icon={<Trash2 size={14} />} aria-label={`删除字段 ${index + 1}`} disabled={mappingFields.length <= 1} onClick={() => setMappingFields((current) => current.filter((_item, itemIndex) => itemIndex !== index))} />
                </div>
              ))}
            </div>
          </div>

          <Field label="额外选项（JSON 对象，可选）"><TextArea value={mappingOptionsText} onChange={setMappingOptionsText} autosize={{ minRows: 3, maxRows: 8 }} placeholder={'{"includeMetadata": true}'} /></Field>
          {mappingDraft.isBuiltin ? <Text type="warning">这是内置映射。编辑会保留内置标记并更新当前配置；需要保留原值时请先使用“复制”。</Text> : null}
          {mappingError ? <Text type="danger" role="alert">{mappingError}</Text> : null}
        </div>
      </Modal>
    </div>
  )
}

function ResourceHeader({ title, action, onAction }: { title: string; action: string; onAction: () => void }) {
  return <div className="flex items-center justify-between mb-3"><Title heading={5} className="!mb-0">{title}</Title><Button theme="solid" type="primary" onClick={onAction}>{action}</Button></div>
}

function Field({ label, children }: { label: string; children: ReactNode }) {
  return <label className="block"><Text className="mb-2 block font-medium">{label}</Text>{children}</label>
}

function EditorGrid({ children }: { children: ReactNode }) {
  return <div className="grid grid-cols-1 gap-4 md:grid-cols-2">{children}</div>
}

function Metric({ label, value, icon }: { label: string; value: number; icon: ReactNode }) {
  return <Card className="console-card" bodyStyle={{ padding: 14 }}><div className="flex items-center gap-2 text-[var(--accent)]">{icon}<Text type="tertiary">{label}</Text></div><div className="mt-2 text-2xl font-semibold">{value.toLocaleString()}</div></Card>
}
