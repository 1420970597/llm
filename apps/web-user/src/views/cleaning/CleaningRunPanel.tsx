import clsx from 'clsx'
import { useMemo, useState } from 'react'
import { Button, Card, Checkbox, Empty, Select, Space, Switch, Tag, Toast, Typography } from '@douyinfe/semi-ui'
import { PlayCircle } from 'lucide-react'
import { consoleApi, type CleaningRule, type Dataset } from '../../lib/api'
import { actionLabel, CLEANING_STAGES, runStatusLabel, sortRulesByPriority } from './cleaningMeta'

const { Text, Title } = Typography

/**
 * 发起清洗面板（L14 独占）。
 *
 * 两个选择维度都是真实生效的：
 * - 拦截阶段 → POST /v1/datasets/{id}/cleaning/run 的 stages，worker 按此扫描对应阶段。
 * - 启用规则 → 直接切换规则的 isActive。worker 只加载 is_active=TRUE 的规则
 *   （internal/store/cleaning_store_runs.go 的 ActiveRules），因此开关立刻生效，
 *   对本次与后续清洗都成立。
 *
 * 已知缺口：请求体里的 ruleIds 字段后端从未消费（apps/api/routes_cleaning_runs.go
 * 只读 input.Stages）。因此这里**不**提供「只对本次选规则」的控件——那会是一个
 * 选了却静默无效的假控件。改用真实的启用/停用，并在界面文案里讲清楚。
 */
export function CleaningRunPanel({
  datasets,
  rules,
  onRulesChanged,
  onEnqueued,
  onRequestCreateRule,
}: {
  datasets: Dataset[]
  rules: CleaningRule[]
  onRulesChanged: () => Promise<void>
  onEnqueued: (datasetId: number) => void
  /** 无规则时，把用户直接带到「新建规则」弹窗，而不是用文字描述那个按钮（issue #106）。 */
  onRequestCreateRule: () => void
}) {
  const [datasetId, setDatasetId] = useState<number | null>(datasets[0]?.id ?? null)
  const [stages, setStages] = useState<string[]>(CLEANING_STAGES.map((stage) => stage.key))
  const [submitting, setSubmitting] = useState(false)
  const [busyRuleId, setBusyRuleId] = useState<number | null>(null)

  const orderedRules = useMemo(() => sortRulesByPriority(rules), [rules])
  const datasetOptions = useMemo(
    () => datasets.map((dataset) => ({ value: dataset.id, label: `${dataset.name}（任务 #${dataset.id}）` })),
    [datasets],
  )

  const toggleStage = (stage: string, checked: boolean) => {
    setStages((current) => (checked ? [...current, stage] : current.filter((item) => item !== stage)))
  }

  const toggleRule = async (rule: CleaningRule, next: boolean) => {
    setBusyRuleId(rule.id)
    try {
      await consoleApi.saveCleaningRule({ ...rule, isActive: next })
      await onRulesChanged()
      Toast.success(next ? `已启用规则「${rule.name}」` : `已停用规则「${rule.name}」`)
    } catch (error) {
      Toast.error((error as Error).message)
    } finally {
      setBusyRuleId(null)
    }
  }

  const submit = async () => {
    if (!datasetId) {
      Toast.warning('请先选择一个任务')
      return
    }
    if (stages.length === 0) {
      Toast.warning('至少要勾选一个拦截阶段')
      return
    }
    setSubmitting(true)
    try {
      const result = await consoleApi.runCleaning(datasetId, stages, [])
      if (!result.message.includes('已入队')) {
        Toast.warning(result.message)
      } else {
        Toast.success(result.message)
      }
      onEnqueued(datasetId)
    } catch (error) {
      Toast.error((error as Error).message)
    } finally {
      setSubmitting(false)
    }
  }

  const enabledRules = orderedRules.filter((rule) => rule.isActive)

  return (
    <Card className="console-panel" bodyStyle={{ padding: 20 }}>
      <Title heading={4} className="!mb-0">发起一次清洗</Title>
      <Text className="mt-2 block console-caption">
        选一个任务，勾选要检查的阶段，确认要启用哪些规则，然后开始。清洗在后台执行，可以离开页面，完成后回到本页看报告。
      </Text>

      {datasets.length === 0 ? (
        <div className="console-empty">
          <Empty description="还没有可清洗的任务。先到「新建任务」创建任务并生成数据，再回来清洗。" />
        </div>
      ) : (
        <>
          <div className="grid grid-cols-1 lg:grid-cols-[1fr_1.4fr] gap-4 mt-4 mt-4">
            <div>
              <Text className="mb-2 block font-medium">要清洗的任务</Text>
              <Select
                value={datasetId ?? undefined}
                optionList={datasetOptions}
                onChange={(value) => setDatasetId(Number(value))}
                style={{ width: '100%' }}
                placeholder="选择一个任务"
              />
            </div>

            <div>
              <Text className="mb-2 block font-medium">拦截阶段（可多选）</Text>
              <div className="grid gap-2">
                {CLEANING_STAGES.map((stage) => (
                  <label key={stage.key} className={clsx('console-domain-item grid gap-1 cursor-pointer', stages.includes(stage.key) && 'ring-2 ring-blue-400')}>
                    <Checkbox
                      checked={stages.includes(stage.key)}
                      onChange={(event) => toggleStage(stage.key, Boolean(event.target.checked))}
                    >
                      <span className="font-semibold">{stage.label}</span>
                    </Checkbox>
                    <Text className="text-xs console-caption pl-6">{stage.description}</Text>
                  </label>
                ))}
              </div>
            </div>
          </div>

          <div className="mt-5">
            <Text className="mb-2 block font-medium">本次启用哪些规则</Text>
            <Text className="mb-3 block console-caption">
              开关会立即保存，清洗时只会使用启用中的规则（当前 {enabledRules.length} / {orderedRules.length} 条启用）。
              一条规则都没启用时，命中关键词的样本不会被丢弃，但会被标记为待复查。
            </Text>
            {orderedRules.length === 0 ? (
              // issue #106：这里原先用 <Banner>。Semi 的 Banner 会**无条件**渲染
              // role="alert"（见 @douyinfe/semi-ui/lib/es/banner/index.js：
              //   React.createElement("div", { className: wrapper, style: style, role: "alert" }, ...)
              // 且 BannerProps 没有任何 prop 可以覆盖它）。后果有三层：
              //   1. 屏幕阅读器 / 无障碍工具在**页面加载时**就把这条静态空状态当成实时告警播报；
              //   2. 自动化无障碍巡检（例如按 role=alert 收集通知）把它识别成“toast”，
              //      于是表现为「打开页面就弹无关 toast」；
              //   3. 它真的内联在本面板里，却长得像全局告警，与所在面板无关
              //      （用户在「清洗报告」面板时也会看到这句讲“清洗规则”的话）。
              //
              // 空状态应当**就地**展示，并且带一个**可点击的下一步**，
              // 而不是用文字描述一个已经在页面上的按钮（避免让用户去找）。
              <div className="console-empty">
                <Empty
                  description={
                    <span>
                      还没有清洗规则，所以暂时不需要选。规则决定「命中多少词之后，样本该被丢弃还是先标记复查」。
                      <br />
                      先建一条规则再回来发起清洗。
                    </span>
                  }
                />
                <Button className="mt-3" theme="solid" type="primary" onClick={onRequestCreateRule}>
                  去新建清洗规则
                </Button>
              </div>
            ) : (
              <div className="grid gap-2">
                {orderedRules.map((rule) => (
                  <div key={rule.id} className="console-domain-item flex items-center justify-between gap-3">
                    <div className="grid gap-1">
                      <Text strong>{rule.name}</Text>
                      <Space className="mt-1" wrap>
                        <Tag color="blue" size="small">优先级 {rule.priority}</Tag>
                        <Tag color="cyan" size="small">命中 ≥ {rule.minHits}</Tag>
                        <Tag color={rule.action === 'drop' ? 'red' : 'orange'} size="small">{actionLabel(rule.action)}</Tag>
                        <Tag color="grey" size="small">{rule.stageScope.length === 0 ? '全部阶段' : rule.stageScope.join(' / ')}</Tag>
                      </Space>
                    </div>
                    <Switch
                      checked={rule.isActive}
                      size="small"
                      loading={busyRuleId === rule.id}
                      onChange={(next) => void toggleRule(rule, next)}
                    />
                  </div>
                ))}
              </div>
            )}
          </div>

          <Space className="mt-5">
            <Button theme="solid" type="primary" icon={<PlayCircle size={16} />} loading={submitting} onClick={() => void submit()}>
              开始清洗
            </Button>
            <Text className="console-caption">已选 {stages.length} 个阶段 · 启用 {enabledRules.length} 条规则</Text>
          </Space>
        </>
      )}
    </Card>
  )
}

/** 运行列表中的一行摘要文案，供父组件与测试复用。 */
export function runSummaryLine(run: { status: string; scannedItems: number; flaggedItems: number; droppedItems: number }): string {
  return `${runStatusLabel(run.status)} · 检查 ${run.scannedItems} 条 · 命中 ${run.flaggedItems} 条 · 丢弃 ${run.droppedItems} 条`
}
