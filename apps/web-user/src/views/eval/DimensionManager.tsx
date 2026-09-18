import { useCallback, useEffect, useMemo, useState } from 'react'
import { Button, Card, Empty, Input, InputNumber, Modal, Space, Switch, Tag, TextArea, Toast, Typography } from '@douyinfe/semi-ui'
import { Pencil, Plus, RefreshCw, Trash2, Upload } from 'lucide-react'
import { consoleApi, type EvalDimension } from '../../lib/api'
import { errorMessage, groupByCategory } from './evalShared'

const { Title, Text } = Typography

type DimensionDraft = {
  id?: number
  key: string
  name: string
  category: string
  description: string
  rubric: string
  scaleMin: number
  scaleMax: number
  weight: number
  isActive: boolean
}

const emptyDraft: DimensionDraft = {
  key: '',
  name: '',
  category: '',
  description: '',
  rubric: '',
  scaleMin: 0,
  scaleMax: 5,
  weight: 1,
  isActive: true,
}

function toDraft(dimension: EvalDimension): DimensionDraft {
  return {
    id: dimension.id,
    key: dimension.key,
    name: dimension.name,
    category: dimension.category,
    description: dimension.description,
    rubric: dimension.rubric,
    scaleMin: dimension.scaleMin,
    scaleMax: dimension.scaleMax,
    weight: dimension.weight,
    isActive: dimension.isActive,
  }
}

/** L13：维度管理（列出/分组/统计/新建/编辑/删除/导入内置）。 */
export function DimensionManager({ onChanged }: { onChanged?: () => void }) {
  const [dimensions, setDimensions] = useState<EvalDimension[]>([])
  const [categories, setCategories] = useState<string[]>([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState('')
  const [saving, setSaving] = useState(false)
  const [seeding, setSeeding] = useState(false)
  const [seedResult, setSeedResult] = useState<{ inserted: number; total: number } | null>(null)
  const [draft, setDraft] = useState<DimensionDraft | null>(null)

  const load = useCallback(async () => {
    setLoading(true)
    setError('')
    try {
      const [list, categoryResult] = await Promise.all([
        consoleApi.listEvalDimensions(),
        consoleApi.evalDimensionCategories(),
      ])
      setDimensions(list)
      setCategories(categoryResult.categories ?? [])
    } catch (err) {
      setError(errorMessage(err))
    } finally {
      setLoading(false)
    }
  }, [])

  useEffect(() => {
    void load()
  }, [load])

  const builtinCount = useMemo(() => dimensions.filter((item) => item.isBuiltin).length, [dimensions])
  const customCount = dimensions.length - builtinCount
  const grouped = useMemo(() => groupByCategory(dimensions, (item) => item.category), [dimensions])

  const categoryOptions = useMemo(() => {
    const merged = new Set<string>([...categories, ...dimensions.map((item) => item.category)])
    return Array.from(merged).filter(Boolean).sort((a, b) => a.localeCompare(b))
  }, [categories, dimensions])

  async function submitDraft() {
    if (!draft) return
    if (!draft.key.trim() || !draft.name.trim() || !draft.category.trim()) {
      Toast.error('维度键、名称、分类均为必填')
      return
    }
    if (draft.scaleMax <= draft.scaleMin) {
      Toast.error('分数上限必须大于下限')
      return
    }
    setSaving(true)
    try {
      await consoleApi.saveEvalDimension({
        id: draft.id,
        key: draft.key.trim(),
        name: draft.name.trim(),
        category: draft.category.trim(),
        description: draft.description,
        rubric: draft.rubric,
        scaleMin: draft.scaleMin,
        scaleMax: draft.scaleMax,
        weight: draft.weight,
        isActive: draft.isActive,
      })
      Toast.success(draft.id ? '维度已更新' : '维度已创建')
      setDraft(null)
      await load()
      onChanged?.()
    } catch (err) {
      Toast.error(errorMessage(err))
    } finally {
      setSaving(false)
    }
  }

  function confirmDelete(dimension: EvalDimension) {
    if (dimension.isBuiltin) {
      Toast.warning('内置维度不可删除，如需停用请改为「停用」')
      return
    }
    Modal.confirm({
      title: `删除维度「${dimension.name}」？`,
      content: '删除后该维度不会再出现在新的评估运行中。',
      okText: '删除',
      cancelText: '取消',
      onOk: async () => {
        try {
          await consoleApi.deleteEvalDimension(dimension.id)
          Toast.success('维度已删除')
          await load()
          onChanged?.()
        } catch (err) {
          Toast.error(errorMessage(err))
        }
      },
    })
  }

  async function seedBuiltins() {
    setSeeding(true)
    try {
      const result = await consoleApi.seedEvalDimensions()
      setSeedResult(result)
      Toast.success(`已导入内置维度 ${result.inserted} 条，当前共 ${result.total} 条`)
      await load()
      onChanged?.()
    } catch (err) {
      Toast.error(errorMessage(err))
    } finally {
      setSeeding(false)
    }
  }

  return (
    <Card className="console-panel" bodyStyle={{ padding: 20 }}>
      <div className="flex flex-wrap items-center justify-between gap-3">
        <div>
          <Title heading={5} className="!mb-0">评估维度管理</Title>
          <Text className="console-caption">内置维度只读可停用，自定义维度可编辑与删除。</Text>
        </div>
        <Space>
          <Button icon={<RefreshCw size={14} />} loading={loading} onClick={() => void load()}>刷新</Button>
          <Button icon={<Upload size={14} />} loading={seeding} onClick={() => void seedBuiltins()}>导入内置维度</Button>
          <Button theme="solid" type="primary" icon={<Plus size={14} />} onClick={() => setDraft({ ...emptyDraft })}>新建自定义维度</Button>
        </Space>
      </div>

      <div className="mt-4 flex flex-wrap gap-2">
        <span className="console-chip">内置 {builtinCount} 条</span>
        <span className="console-chip">自定义 {customCount} 条</span>
        <span className="console-chip">分类 {grouped.length} 个</span>
        <span className="console-chip">接口返回分类 {categories.length} 个</span>
      </div>

      {seedResult ? (
        <div className="mt-3">
          <Text className="console-caption">最近一次导入：新增 {seedResult.inserted} 条，库内共 {seedResult.total} 条。</Text>
        </div>
      ) : null}

      {error ? (
        <div className="mt-4">
          <Text type="danger">加载维度失败：{error}</Text>
        </div>
      ) : null}

      {loading && dimensions.length === 0 ? (
        <div className="mt-4"><Text className="console-muted">维度加载中…</Text></div>
      ) : null}

      {!loading && dimensions.length === 0 && !error ? (
        <div className="console-empty mt-4">
          <Empty title="暂无评估维度" description="点击「导入内置维度」写入内置维度，或新建自定义维度。" />
        </div>
      ) : null}

      <div className="console-stack mt-4">
        {grouped.map(([category, items]) => (
          <div key={category} className="console-record-card">
            <div className="flex items-center justify-between gap-3">
              <Text strong>{category}</Text>
              <Text className="console-caption">{items.length} 个维度</Text>
            </div>
            <div className="console-record-list mt-3">
              {items.map((dimension) => (
                <div key={dimension.id} className="console-record-item">
                  <div className="console-record-item-top flex flex-wrap items-center gap-2">
                    <Text strong>{dimension.name}</Text>
                    <Tag color={dimension.isBuiltin ? 'blue' : 'green'}>{dimension.isBuiltin ? '内置' : '自定义'}</Tag>
                    <Tag color={dimension.isActive ? 'green' : 'grey'}>{dimension.isActive ? '启用' : '停用'}</Tag>
                    <Text className="console-caption">key={dimension.key}</Text>
                    <Text className="console-caption">区间 {dimension.scaleMin}~{dimension.scaleMax}</Text>
                    <Text className="console-caption">权重 {dimension.weight}</Text>
                    <Space>
                      <Button size="small" icon={<Pencil size={13} />} onClick={() => setDraft(toDraft(dimension))}>编辑</Button>
                      <Button
                        size="small"
                        type="danger"
                        theme="borderless"
                        icon={<Trash2 size={13} />}
                        disabled={dimension.isBuiltin}
                        onClick={() => confirmDelete(dimension)}
                      >
                        {dimension.isBuiltin ? '内置不可删除' : '删除'}
                      </Button>
                    </Space>
                  </div>
                  {dimension.description ? <Text className="mt-2 block">{dimension.description}</Text> : null}
                  {dimension.rubric ? (
                    <Text className="mt-2 block console-caption">评分细则：{dimension.rubric}</Text>
                  ) : null}
                </div>
              ))}
            </div>
          </div>
        ))}
      </div>

      <Modal
        title={draft?.id ? `编辑维度：${draft.name}` : '新建自定义维度'}
        visible={draft !== null}
        onCancel={() => setDraft(null)}
        onOk={() => void submitDraft()}
        confirmLoading={saving}
        okText="保存"
        cancelText="取消"
        width={640}
      >
        {draft ? (
          <div className="console-stack">
            <div className="form-grid">
              <label>
                <span>维度键 key</span>
                <Input
                  value={draft.key}
                  disabled={Boolean(draft.id) && Boolean(dimensions.find((item) => item.id === draft.id)?.isBuiltin)}
                  onChange={(value) => setDraft({ ...draft, key: value })}
                  placeholder="例如 long_chain_step_consistency"
                />
              </label>
              <label>
                <span>名称</span>
                <Input value={draft.name} onChange={(value) => setDraft({ ...draft, name: value })} placeholder="例如 长链步骤一致性" />
              </label>
              <label>
                <span>分类 category</span>
                <Input
                  value={draft.category}
                  onChange={(value) => setDraft({ ...draft, category: value })}
                  placeholder="例如 long_chain"
                />
              </label>
              <label>
                <span>权重</span>
                <InputNumber value={draft.weight} min={0} step={0.1} onChange={(value) => setDraft({ ...draft, weight: Number(value ?? 0) })} />
              </label>
              <label>
                <span>分数下限</span>
                <InputNumber value={draft.scaleMin} onChange={(value) => setDraft({ ...draft, scaleMin: Number(value ?? 0) })} />
              </label>
              <label>
                <span>分数上限</span>
                <InputNumber value={draft.scaleMax} onChange={(value) => setDraft({ ...draft, scaleMax: Number(value ?? 0) })} />
              </label>
            </div>
            {categoryOptions.length > 0 ? (
              <Text className="console-caption">已有分类：{categoryOptions.join(' / ')}</Text>
            ) : null}
            <label className="block">
              <span>描述</span>
              <TextArea value={draft.description} autosize={{ minRows: 2 }} onChange={(value) => setDraft({ ...draft, description: value })} />
            </label>
            <label className="block">
              <span>评分细则 rubric</span>
              <TextArea value={draft.rubric} autosize={{ minRows: 3 }} onChange={(value) => setDraft({ ...draft, rubric: value })} />
            </label>
            <div className="flex items-center gap-3">
              <Switch checked={draft.isActive} onChange={(checked) => setDraft({ ...draft, isActive: checked })} />
              <Text>启用该维度</Text>
            </div>
          </div>
        ) : null}
      </Modal>
    </Card>
  )
}
