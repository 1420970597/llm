import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { Button, Card, Empty, Input, InputNumber, Modal, Select, Space, Switch, Table, Tag, Toast, Typography } from '@douyinfe/semi-ui'
import { Pencil, RefreshCw } from 'lucide-react'
import { consoleApi, type CleaningRule } from '../../lib/api'
import { actionLabel, CLEANING_STAGES, sortRulesByPriority, stageScopeLabel } from './cleaningMeta'

const { Text, Title } = Typography

const ACTION_OPTIONS = [
  { value: 'drop', label: '丢弃样本（drop）' },
  { value: 'flag', label: '标记待复查（flag）' },
  { value: 'retry', label: '重新生成（retry）' },
]

type RuleDraft = Partial<CleaningRule>

/**
 * 清洗规则面板（L14 独占）。
 *
 * 规则语义来自 internal/cleaning/keywords.go 的 EvaluateRules：
 * 过滤出「已启用且阶段范围覆盖当前阶段」的规则，按 priority **降序**（数字大的先判），
 * 取第一条满足「命中数 >= minHits」的规则作为最终动作；都没命中则按 flag 处理。
 * 顺序依据是运行时路径 internal/cleaning/scanner.go 的 decideAction。
 */
export function CleaningRulePanel({
  rules,
  loading,
  onRefresh,
  createRequestSignal = 0,
}: {
  rules: CleaningRule[]
  loading: boolean
  onRefresh: () => Promise<void>
  /**
   * 递增计数器：值变化时打开「新建规则」弹窗（issue #106）。
   *
   * 为什么用计数器而不是 `openCreate={boolean}`：父组件需要能**反复**触发同一动作
   * （用户在空状态里点了两次「去新建清洗规则」，第二次也必须打开弹窗）。
   * 布尔 prop 在第二次把 true 改成 true 时不会触发任何变化；递增计数器的每次
   * 自增都是新值，effect 必定重跑。这是 React 里传递命令式意图的惯用做法。
   * 默认 0 表示父组件不使用这个入口。
   */
  createRequestSignal?: number
}) {
  const [draft, setDraft] = useState<RuleDraft | null>(null)
  const [saving, setSaving] = useState(false)
  const [busyId, setBusyId] = useState<number | null>(null)

  const openCreate = useCallback(
    () => setDraft({ name: '', stageScope: [], minHits: 1, action: 'flag', priority: 100, isActive: true, config: {} }),
    [],
  )

  // 响应父组件的「去新建规则」请求。跳过首渲染（signal 初始为 0），
  // 否则页面一加载就会弹出新建弹窗。
  const lastSignal = useRef(createRequestSignal)
  useEffect(() => {
    if (createRequestSignal !== lastSignal.current) {
      lastSignal.current = createRequestSignal
      openCreate()
    }
  }, [createRequestSignal, openCreate])

  const ordered = useMemo(() => sortRulesByPriority(rules), [rules])

  const saveRule = async () => {
    if (!draft) {
      return
    }
    const name = (draft.name ?? '').trim()
    if (!name) {
      Toast.warning('请先填写规则名称')
      return
    }
    const minHits = Number(draft.minHits ?? 1)
    if (!Number.isFinite(minHits) || minHits < 1) {
      Toast.warning('最少命中数必须是不小于 1 的整数')
      return
    }
    setSaving(true)
    try {
      await consoleApi.saveCleaningRule({
        ...draft,
        name,
        minHits: Math.floor(minHits),
        stageScope: draft.stageScope ?? [],
        priority: Number(draft.priority ?? 100),
        action: draft.action ?? 'flag',
      })
      setDraft(null)
      await onRefresh()
      Toast.success('规则已保存')
    } catch (error) {
      Toast.error((error as Error).message)
    } finally {
      setSaving(false)
    }
  }

  const toggleActive = async (rule: CleaningRule, next: boolean) => {
    setBusyId(rule.id)
    try {
      await consoleApi.saveCleaningRule({ ...rule, isActive: next })
      await onRefresh()
      Toast.success(next ? '规则已启用' : '规则已停用')
    } catch (error) {
      Toast.error((error as Error).message)
    } finally {
      setBusyId(null)
    }
  }

  const columns = useMemo(
    () => [
      { title: '规则名称', dataIndex: 'name', render: (value: string) => <Text strong>{value}</Text> },
      {
        title: '优先级',
        dataIndex: 'priority',
        render: (value: number) => <Tag color="blue">{value}</Tag>,
      },
      {
        title: '生效阶段',
        dataIndex: 'stageScope',
        render: (value: string[]) => stageScopeLabel(value ?? []),
      },
      {
        title: '最少命中数',
        dataIndex: 'minHits',
        render: (value: number) => `命中 ≥ ${value} 次`,
      },
      {
        title: '命中后动作',
        dataIndex: 'action',
        render: (value: string) => <Tag color={value === 'drop' ? 'red' : value === 'retry' ? 'purple' : 'orange'}>{actionLabel(value)}</Tag>,
      },
      {
        title: '启用',
        dataIndex: 'isActive',
        render: (value: boolean, record: CleaningRule) => (
          <Switch checked={value} size="small" loading={busyId === record.id} onChange={(next) => void toggleActive(record, next)} />
        ),
      },
      {
        title: '操作',
        dataIndex: 'operate',
        render: (_: unknown, record: CleaningRule) => (
          <Button size="small" icon={<Pencil size={14} />} onClick={() => setDraft({ ...record, stageScope: record.stageScope ?? [] })}>编辑</Button>
        ),
      },
    ],
    [busyId],
  )

  return (
    <Card className="console-panel" bodyStyle={{ padding: 20 }}>
      <div className="flex flex-col gap-4 lg:flex-row lg:items-start lg:justify-between">
        <div>
          <Title heading={4} className="!mb-0">清洗规则</Title>
          <Text className="mt-2 block console-caption">
            规则回答「命中多少词之后该怎么处理」。判定顺序：先按优先级从大到小逐条看，第一条满足
            「命中次数 ≥ 最少命中数」的规则生效；一条都不满足时样本会被标记为待复查。停用的规则不参与判定。
          </Text>
        </div>
        <Space>
          <Button icon={<RefreshCw size={14} />} loading={loading} onClick={() => void onRefresh()}>刷新</Button>
          <Button
            theme="solid"
            type="primary"
            onClick={openCreate}
          >
            新建规则
          </Button>
        </Space>
      </div>

      <div className="mt-4">
        {ordered.length === 0 ? (
          <div className="console-empty">
            <Empty description="还没有清洗规则。点右上角「新建规则」定义：命中多少词之后，样本该被丢弃还是先标记复查。" />
          </div>
        ) : (
          <Table columns={columns} dataSource={ordered} pagination={false} rowKey="id" />
        )}
      </div>

      <Modal
        title={draft?.id ? '编辑规则' : '新建规则'}
        visible={draft !== null}
        onCancel={() => setDraft(null)}
        onOk={() => void saveRule()}
        confirmLoading={saving}
        okText="保存"
        cancelText="取消"
        width={640}
      >
        {draft ? (
          <div className="console-stack">
            <div>
              <Text className="mb-2 block font-medium">规则名称</Text>
              <Input value={draft.name ?? ''} placeholder="例如 单阶段两次命中即丢弃" onChange={(value) => setDraft({ ...draft, name: value })} />
            </div>
            <div className="console-card-grid-2">
              <div>
                <Text className="mb-2 block font-medium">优先级（数字越大越先判定）</Text>
                <InputNumber value={draft.priority ?? 100} min={1} onChange={(value) => setDraft({ ...draft, priority: Number(value ?? 100) })} style={{ width: '100%' }} />
              </div>
              <div>
                <Text className="mb-2 block font-medium">最少命中数</Text>
                <InputNumber value={draft.minHits ?? 1} min={1} onChange={(value) => setDraft({ ...draft, minHits: Number(value ?? 1) })} style={{ width: '100%' }} />
              </div>
            </div>
            <div>
              <Text className="mb-2 block font-medium">命中后动作</Text>
              <Select value={draft.action ?? 'flag'} optionList={ACTION_OPTIONS} onChange={(value) => setDraft({ ...draft, action: String(value) })} style={{ width: '100%' }} />
            </div>
            <div>
              <Text className="mb-2 block font-medium">生效阶段（不选 = 全部阶段）</Text>
              <Select
                multiple
                value={draft.stageScope ?? []}
                optionList={CLEANING_STAGES.map((stage) => ({ value: stage.key, label: stage.label }))}
                onChange={(value) => setDraft({ ...draft, stageScope: (value as string[]) ?? [] })}
                placeholder="全部阶段"
                style={{ width: '100%' }}
              />
            </div>
            <div>
              <Text className="mb-2 block font-medium">启用</Text>
              <Switch checked={draft.isActive ?? true} onChange={(next) => setDraft({ ...draft, isActive: next })} />
            </div>
          </div>
        ) : null}
      </Modal>
    </Card>
  )
}

/** 供发起清洗时展示「可选的规则」：只列出启用中的规则。 */
export function activeRuleOptions(rules: CleaningRule[]) {
  return sortRulesByPriority(rules)
    .filter((rule) => rule.isActive)
    .map((rule) => ({
      value: rule.id,
      label: `${rule.name}（优先级 ${rule.priority} · 命中 ≥ ${rule.minHits} · ${actionLabel(rule.action)}）`,
    }))
}

