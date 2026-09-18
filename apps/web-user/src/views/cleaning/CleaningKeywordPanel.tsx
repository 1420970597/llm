import { useMemo, useState } from 'react'
import {
  Banner,
  Button,
  Card,
  Empty,
  Input,
  Modal,
  Select,
  Space,
  Switch,
  Table,
  Tag,
  TextArea,
  Toast,
  Typography,
} from '@douyinfe/semi-ui'
import { Pencil, RefreshCw, Trash2, Upload } from 'lucide-react'
import { consoleApi, type CleaningKeyword } from '../../lib/api'
import { buildKeywordSavePayload, categoryLabel, matchModeLabel, severityLabel } from './cleaningMeta'

const { Text, Title } = Typography

/** 内置分类取值来自 internal/cleaning/keywords.go，不得随意增删。 */
const CATEGORY_OPTIONS = [
  { value: 'refusal', label: '中文拒答' },
  { value: 'safety', label: '安全与合规回避' },
  { value: 'uncertainty_evasion', label: '含糊推脱' },
  { value: 'english_refusal', label: '英文拒答' },
  { value: 'placeholder', label: '占位与空输出' },
]

const SEVERITY_OPTIONS = [
  { value: 'block', label: '拦截（block）' },
  { value: 'warn', label: '告警（warn）' },
]

const MATCH_MODE_OPTIONS = [
  { value: 'contains', label: '包含（contains）' },
  { value: 'prefix', label: '句首（prefix）' },
  { value: 'regex', label: '正则（regex）' },
]

type KeywordDraft = Partial<CleaningKeyword>

type ImportResult = { inserted: number; skipped: number; patterns: number }

export function CleaningKeywordPanel({
  keywords,
  loading,
  onRefresh,
}: {
  keywords: CleaningKeyword[]
  loading: boolean
  onRefresh: () => Promise<void>
}) {
  const [draft, setDraft] = useState<KeywordDraft | null>(null)
  const [saving, setSaving] = useState(false)
  const [busyId, setBusyId] = useState<number | null>(null)
  // 后端 UPDATE 分支按 id + pattern 定位，且不写 category（见 buildKeywordSavePayload 注释）。
  const editing = Boolean(draft?.id)

  const [importText, setImportText] = useState('')
  const [importCategory, setImportCategory] = useState('refusal')
  const [importSeverity, setImportSeverity] = useState('block')
  const [importing, setImporting] = useState(false)
  const [importResult, setImportResult] = useState<ImportResult | null>(null)

  const grouped = useMemo(() => {
    const buckets = new Map<string, CleaningKeyword[]>()
    keywords.forEach((keyword) => {
      const list = buckets.get(keyword.category) ?? []
      list.push(keyword)
      buckets.set(keyword.category, list)
    })
    return Array.from(buckets.entries())
      .map(([category, items]) => ({ category, items }))
      .sort((left, right) => left.category.localeCompare(right.category))
  }, [keywords])

  const openCreate = () => {
    setDraft({ pattern: '', category: 'refusal', matchMode: 'contains', severity: 'block', isActive: true, note: '' })
  }

  const saveKeyword = async () => {
    if (!draft) {
      return
    }
    const pattern = (draft.pattern ?? '').trim()
    if (!pattern) {
      Toast.warning('请先填写关键词内容')
      return
    }
    setSaving(true)
    try {
      await consoleApi.saveCleaningKeyword(buildKeywordSavePayload({ ...draft, pattern }))
      setDraft(null)
      await onRefresh()
      Toast.success('关键词已保存')
    } catch (error) {
      Toast.error((error as Error).message)
    } finally {
      setSaving(false)
    }
  }

  const toggleActive = async (keyword: CleaningKeyword, next: boolean) => {
    setBusyId(keyword.id)
    try {
      await consoleApi.saveCleaningKeyword({ ...keyword, isActive: next })
      await onRefresh()
      Toast.success(next ? '关键词已启用' : '关键词已停用')
    } catch (error) {
      Toast.error((error as Error).message)
    } finally {
      setBusyId(null)
    }
  }

  const removeKeyword = async (keyword: CleaningKeyword) => {
    if (keyword.isBuiltin) {
      Toast.warning('内置关键词不可删除，请改为停用')
      return
    }
    setBusyId(keyword.id)
    try {
      await consoleApi.deleteCleaningKeyword(keyword.id)
      await onRefresh()
      Toast.success('关键词已删除')
    } catch (error) {
      Toast.error((error as Error).message)
    } finally {
      setBusyId(null)
    }
  }

  const runImport = async () => {
    const patterns = importText
      .split('\n')
      .map((line) => line.trim())
      .filter((line) => line.length > 0)
    if (patterns.length === 0) {
      Toast.warning('请每行填写一个关键词')
      return
    }
    setImporting(true)
    try {
      const result = await consoleApi.importCleaningKeywords(patterns, importCategory, importSeverity)
      setImportResult({ inserted: result.inserted, skipped: result.skipped, patterns: patterns.length })
      setImportText('')
      await onRefresh()
      Toast.success(`导入完成：新增 ${result.inserted} 条，跳过 ${result.skipped} 条`)
    } catch (error) {
      Toast.error((error as Error).message)
    } finally {
      setImporting(false)
    }
  }

  const columns = useMemo(
    () => [
      {
        title: '关键词',
        dataIndex: 'pattern',
        render: (value: string) => <Text strong>{value}</Text>,
      },
      { title: '匹配方式', dataIndex: 'matchMode', render: (value: string) => matchModeLabel(value) },
      {
        title: '严重度',
        dataIndex: 'severity',
        render: (value: string) => <Tag color={value === 'block' ? 'red' : 'orange'}>{severityLabel(value)}</Tag>,
      },
      {
        title: '来源',
        dataIndex: 'isBuiltin',
        render: (value: boolean) => <Tag color={value ? 'purple' : 'cyan'}>{value ? '内置' : '自定义'}</Tag>,
      },
      {
        title: '启用',
        dataIndex: 'isActive',
        render: (value: boolean, record: CleaningKeyword) => (
          <Switch checked={value} size="small" loading={busyId === record.id} onChange={(next) => void toggleActive(record, next)} />
        ),
      },
      {
        title: '备注',
        dataIndex: 'note',
        render: (value: string) => (value ? value : <Text className="console-caption">—</Text>),
      },
      {
        title: '操作',
        dataIndex: 'operate',
        render: (_: unknown, record: CleaningKeyword) => (
          <Space>
            <Button size="small" icon={<Pencil size={14} />} onClick={() => setDraft({ ...record })}>编辑</Button>
            <Button
              size="small"
              type="danger"
              icon={<Trash2 size={14} />}
              disabled={record.isBuiltin}
              loading={busyId === record.id}
              onClick={() => void removeKeyword(record)}
            >
              删除
            </Button>
          </Space>
        ),
      },
    ],
    [busyId],
  )

  return (
    <div className="console-stack">
      <Card className="console-panel" bodyStyle={{ padding: 20 }}>
        <div className="flex flex-col gap-4 lg:flex-row lg:items-start lg:justify-between">
          <div>
            <Title heading={4} className="!mb-0">关键词库</Title>
            <Text className="mt-2 block console-caption">
              清洗时逐条匹配这些词。命中「拦截」的样本会被标记为丢弃，命中「告警」的只做标记供复查。
              内置词是系统基线，可以停用但不能删除。
            </Text>
          </div>
          <Space>
            <Button icon={<RefreshCw size={14} />} loading={loading} onClick={() => void onRefresh()}>刷新</Button>
            <Button theme="solid" type="primary" onClick={openCreate}>新增关键词</Button>
          </Space>
        </div>

        <div className="console-summary-grid mt-4">
          <div className="console-summary-row"><span>关键词总数</span><Text strong>{keywords.length}</Text></div>
          <div className="console-summary-row"><span>已启用</span><Text strong>{keywords.filter((item) => item.isActive).length}</Text></div>
          <div className="console-summary-row"><span>内置 / 自定义</span>
            <Text strong>
              {keywords.filter((item) => item.isBuiltin).length} / {keywords.filter((item) => !item.isBuiltin).length}
            </Text>
          </div>
        </div>

        <div className="mt-5 console-stack">
          {grouped.length === 0 ? (
            <div className="console-empty">
              <Empty description="关键词库还是空的。可以先点右上角「新增关键词」，或在下方批量粘贴一批词一次性导入。" />
            </div>
          ) : (
            grouped.map((group) => (
              <div key={group.category}>
                <Space className="mb-2">
                  <Tag color="blue">{categoryLabel(group.category)}</Tag>
                  <Text className="console-caption">{group.items.length} 条</Text>
                </Space>
                <Table columns={columns} dataSource={group.items} pagination={false} rowKey="id" />
              </div>
            ))
          )}
        </div>
      </Card>

      <Card className="console-panel" bodyStyle={{ padding: 20 }}>
        <Title heading={4} className="!mb-0">批量导入</Title>
        <Text className="mt-2 block console-caption">
          每行一个关键词，整批使用下方选择的分类与严重度。已经存在的词会被跳过，不会重复写入。
        </Text>
        <div className="grid grid-cols-1 lg:grid-cols-[2fr_1fr] gap-4 mt-4 mt-4">
          <div>
            <Text className="mb-2 block font-medium">关键词（每行一个）</Text>
            <TextArea
              value={importText}
              autosize={{ minRows: 6, maxRows: 14 }}
              placeholder={'对不起\n我不能\nAs an AI'}
              onChange={(value) => setImportText(value)}
            />
          </div>
          <div className="console-stack">
            <div>
              <Text className="mb-2 block font-medium">分类</Text>
              <Select value={importCategory} optionList={CATEGORY_OPTIONS} onChange={(value) => setImportCategory(String(value))} style={{ width: '100%' }} />
            </div>
            <div>
              <Text className="mb-2 block font-medium">严重度</Text>
              <Select value={importSeverity} optionList={SEVERITY_OPTIONS} onChange={(value) => setImportSeverity(String(value))} style={{ width: '100%' }} />
            </div>
            <Button theme="solid" type="primary" icon={<Upload size={14} />} loading={importing} onClick={() => void runImport()}>
              导入
            </Button>
          </div>
        </div>

        {importResult ? (
          <Banner
            className="mt-4"
            type={importResult.skipped > 0 ? 'warning' : 'success'}
            closeIcon={null}
            description={
              <span>
                本次提交 {importResult.patterns} 条：新增 <Text strong>{importResult.inserted}</Text> 条，
                跳过 <Text strong>{importResult.skipped}</Text> 条。
                {importResult.skipped > 0 ? '被跳过说明这些词库里已经存在（重复导入不会覆盖已有设置）。' : ''}
              </span>
            }
          />
        ) : null}
      </Card>

      <Modal
        title={editing ? '编辑关键词' : '新增关键词'}
        visible={draft !== null}
        onCancel={() => setDraft(null)}
        onOk={() => void saveKeyword()}
        confirmLoading={saving}
        okText="保存"
        cancelText="取消"
      >
        {draft ? (
          <div className="console-stack">
            {draft.isBuiltin ? (
              <Banner type="info" closeIcon={null} description="这是内置关键词，只能修改启用状态与严重度，不能删除。" />
            ) : null}
            <div>
              <Text className="mb-2 block font-medium">关键词内容</Text>
              <Input
                value={draft.pattern ?? ''}
                placeholder="例如 对不起"
                disabled={editing}
                onChange={(value) => setDraft({ ...draft, pattern: value })}
              />
              {editing ? (
                <Text className="mt-2 block console-caption">
                  关键词内容创建后不可修改。需要改词请删除后重新新增（内置词可停用）。
                </Text>
              ) : null}
            </div>
            <div className="console-card-grid-2">
              <div>
                <Text className="mb-2 block font-medium">分类</Text>
                {editing ? (
                  <>
                    <Input value={categoryLabel(draft.category ?? '')} disabled />
                    <Text className="mt-2 block console-caption">分类创建后不可修改。</Text>
                  </>
                ) : (
                  <Select value={draft.category ?? 'refusal'} optionList={CATEGORY_OPTIONS} onChange={(value) => setDraft({ ...draft, category: String(value) })} style={{ width: '100%' }} />
                )}
              </div>
              <div>
                <Text className="mb-2 block font-medium">匹配方式</Text>
                <Select value={draft.matchMode ?? 'contains'} optionList={MATCH_MODE_OPTIONS} onChange={(value) => setDraft({ ...draft, matchMode: String(value) })} style={{ width: '100%' }} />
              </div>
            </div>
            <div className="console-card-grid-2">
              <div>
                <Text className="mb-2 block font-medium">严重度</Text>
                <Select value={draft.severity ?? 'block'} optionList={SEVERITY_OPTIONS} onChange={(value) => setDraft({ ...draft, severity: String(value) })} style={{ width: '100%' }} />
              </div>
              <div>
                <Text className="mb-2 block font-medium">启用</Text>
                <Switch checked={draft.isActive ?? true} onChange={(next) => setDraft({ ...draft, isActive: next })} />
              </div>
            </div>
            <div>
              <Text className="mb-2 block font-medium">备注（可空）</Text>
              <Input value={draft.note ?? ''} placeholder="说明这条词为什么加进来" onChange={(value) => setDraft({ ...draft, note: value })} />
            </div>
          </div>
        ) : null}
      </Modal>
    </div>
  )
}
