import { useCallback, useEffect, useMemo, useState } from 'react'
import { Button, Card, Checkbox, Input, InputNumber, Select, Space, Tag, Toast, Typography } from '@douyinfe/semi-ui'
import { PlayCircle, RefreshCw } from 'lucide-react'
import {
  consoleApi,
  type Dataset,
  type EvalDimension,
  type EvalJudgeOption,
} from '../../lib/api'
import { errorMessage, groupByCategory, samplingInputError, type SamplingMode } from './evalShared'

const { Title, Text } = Typography

/** L13：抽样配置 + 启动评估（维度多选、按分类全选、被排除裁判禁用）。 */
export function EvalRunForm({
  datasets,
  onCreated,
}: {
  datasets: Dataset[]
  onCreated: (runId: number) => void
}) {
  const [datasetId, setDatasetId] = useState<number | null>(datasets[0]?.id ?? null)
  const [name, setName] = useState('')
  const [samplingMode, setSamplingMode] = useState<SamplingMode>('full')
  const [sampleRatio, setSampleRatio] = useState(0.3)
  const [sampleSize, setSampleSize] = useState(20)
  const [targetKind, setTargetKind] = useState('')

  const [dimensions, setDimensions] = useState<EvalDimension[]>([])
  const [judges, setJudges] = useState<EvalJudgeOption[]>([])
  const [selectedDimensions, setSelectedDimensions] = useState<string[]>([])
  const [selectedJudges, setSelectedJudges] = useState<number[]>([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState('')
  const [submitting, setSubmitting] = useState(false)

  const selectedDataset = useMemo(
    () => datasets.find((item) => item.id === datasetId) ?? null,
    [datasets, datasetId],
  )

  const load = useCallback(async () => {
    setLoading(true)
    setError('')
    try {
      const [dimensionList, judgeList] = await Promise.all([
        consoleApi.listEvalDimensions(),
        consoleApi.listEvalJudges(),
      ])
      setDimensions(dimensionList.filter((item) => item.isActive))
      setJudges(judgeList)
      // 被排除的裁判（生成者自评）不可选中，因此默认不预选。
      setSelectedJudges((current) => current.filter((id) => judgeList.some((judge) => judge.providerId === id && !judge.excluded)))
    } catch (err) {
      setError(errorMessage(err))
    } finally {
      setLoading(false)
    }
  }, [])

  useEffect(() => {
    void load()
  }, [load])

  // 父组件异步加载 datasets：首帧可能为空，加载完成后补齐默认数据集。
  useEffect(() => {
    if (datasetId === null && datasets.length > 0) setDatasetId(datasets[0].id)
  }, [datasets, datasetId])

  const groupedDimensions = useMemo(() => groupByCategory(dimensions, (item) => item.category), [dimensions])

  function toggleDimension(key: string, checked: boolean) {
    setSelectedDimensions((current) =>
      checked ? Array.from(new Set([...current, key])) : current.filter((item) => item !== key),
    )
  }

  function toggleCategory(items: EvalDimension[], checked: boolean) {
    const keys = items.map((item) => item.key)
    setSelectedDimensions((current) =>
      checked ? Array.from(new Set([...current, ...keys])) : current.filter((item) => !keys.includes(item)),
    )
  }

  function toggleJudge(providerId: number, checked: boolean) {
    setSelectedJudges((current) =>
      checked ? Array.from(new Set([...current, providerId])) : current.filter((item) => item !== providerId),
    )
  }

  async function submit() {
    if (!selectedDataset) {
      Toast.error('请先选择目标数据集')
      return
    }
    const samplingError = samplingInputError(samplingMode, sampleRatio, sampleSize)
    if (samplingError) {
      Toast.error(samplingError)
      return
    }
    if (selectedDimensions.length === 0) {
      Toast.error('至少选择一个评估维度')
      return
    }
    if (selectedJudges.length === 0) {
      Toast.error('至少选择一个裁判模型')
      return
    }

    setSubmitting(true)
    try {
      const run = await consoleApi.createEvalRun({
        datasetId: selectedDataset.id,
        name: name.trim() || `${selectedDataset.name} 评估`,
        samplingMode,
        sampleRatio: samplingMode === 'ratio' ? sampleRatio : undefined,
        sampleSize: samplingMode === 'count' ? sampleSize : undefined,
        targetKind: targetKind || selectedDataset.targetKind,
        dimensionKeys: selectedDimensions,
        judgeProviderIds: selectedJudges,
        generatorProviderId: selectedDataset.providerId || undefined,
      })
      const enqueue = await consoleApi.startEvalRun(run.id)
      Toast.success(enqueue.message || '评估任务已入队')
      onCreated(run.id)
    } catch (err) {
      Toast.error(errorMessage(err))
    } finally {
      setSubmitting(false)
    }
  }

  return (
    <Card className="console-panel" bodyStyle={{ padding: 20 }}>
      <div className="flex flex-wrap items-center justify-between gap-3">
        <div>
          <Title heading={5} className="!mb-0">新建评估运行</Title>
          <Text className="console-caption">选择数据集与抽样方式，指定维度与裁判模型后入队执行。</Text>
        </div>
        <Button icon={<RefreshCw size={14} />} loading={loading} onClick={() => void load()}>刷新维度与裁判</Button>
      </div>

      {error ? (
        <div className="mt-4">
          <Text type="danger">加载维度/裁判失败：{error}</Text>
        </div>
      ) : null}

      <div className="form-grid mt-4">
        <label>
          <span>目标数据集</span>
          <Select
            value={datasetId ?? undefined}
            placeholder="选择数据集"
            style={{ width: '100%' }}
            onChange={(value) => setDatasetId(Number(value))}
            optionList={datasets.map((item) => ({ label: `${item.name}（#${item.id}）`, value: item.id }))}
          />
        </label>
        <label>
          <span>运行名称</span>
          <Input value={name} onChange={setName} placeholder="留空则自动命名" />
        </label>
        <label>
          <span>抽样模式</span>
          <Select
            value={samplingMode}
            style={{ width: '100%' }}
            onChange={(value) => setSamplingMode(value as SamplingMode)}
            optionList={[
              { label: '全量评估', value: 'full' },
              { label: '按比例抽样', value: 'ratio' },
              { label: '按条数抽样', value: 'count' },
            ]}
          />
        </label>
        <label>
          <span>目标类型</span>
          <Select
            value={targetKind || selectedDataset?.targetKind || 'sft'}
            style={{ width: '100%' }}
            onChange={(value) => setTargetKind(String(value))}
            optionList={[
              { label: 'SFT', value: 'sft' },
              { label: 'GRPO', value: 'grpo' },
            ]}
          />
        </label>
        {samplingMode === 'ratio' ? (
          <label>
            <span>抽样比例（0~1）</span>
            <InputNumber
              value={sampleRatio}
              min={0}
              max={1}
              step={0.05}
              style={{ width: '100%' }}
              onChange={(value) => setSampleRatio(Number(value ?? 0))}
            />
          </label>
        ) : null}
        {samplingMode === 'count' ? (
          <label>
            <span>抽样条数</span>
            <InputNumber
              value={sampleSize}
              min={1}
              style={{ width: '100%' }}
              onChange={(value) => setSampleSize(Number(value ?? 0))}
            />
          </label>
        ) : null}
      </div>
      {samplingMode === 'full' ? (
        <Text className="console-caption mt-2 block">全量模式将对数据集内全部条目打分，不额外填写抽样参数。</Text>
      ) : null}

      <div className="mt-5">
        <div className="flex flex-wrap items-center justify-between gap-2">
          <Text strong>评估维度（已选 {selectedDimensions.length} / {dimensions.length}）</Text>
          <Space>
            <Button size="small" onClick={() => setSelectedDimensions(dimensions.map((item) => item.key))}>全选</Button>
            <Button size="small" onClick={() => setSelectedDimensions([])}>清空</Button>
          </Space>
        </div>
        {dimensions.length === 0 && !loading ? (
          <Text className="console-caption mt-2 block">暂无可用维度，请先在「评估维度管理」中导入内置维度。</Text>
        ) : null}
        <div className="console-stack mt-3">
          {groupedDimensions.map(([category, items]) => {
            const allChecked = items.every((item) => selectedDimensions.includes(item.key))
            return (
              <div key={category} className="console-record-card">
                <div className="flex flex-wrap items-center justify-between gap-2">
                  <Text strong>{category}</Text>
                  <Checkbox
                    checked={allChecked}
                    onChange={(event) => toggleCategory(items, Boolean(event.target.checked))}
                  >
                    全选本分类
                  </Checkbox>
                </div>
                <div className="mt-3 flex flex-wrap gap-3">
                  {items.map((dimension) => (
                    <Checkbox
                      key={dimension.id}
                      checked={selectedDimensions.includes(dimension.key)}
                      onChange={(event) => toggleDimension(dimension.key, Boolean(event.target.checked))}
                    >
                      {dimension.name}
                    </Checkbox>
                  ))}
                </div>
              </div>
            )
          })}
        </div>
      </div>

      <div className="mt-5">
        <Text strong>裁判模型（已选 {selectedJudges.length}）</Text>
        <div className="console-stack mt-3">
          {judges.length === 0 && !loading ? (
            <Text className="console-caption">暂无可用的裁判模型。</Text>
          ) : null}
          {judges.map((judge) => (
            <div key={judge.providerId} className="console-record-item">
              <div className="console-record-item-top flex flex-wrap items-center gap-2">
                <Checkbox
                  disabled={judge.excluded}
                  checked={selectedJudges.includes(judge.providerId)}
                  onChange={(event) => toggleJudge(judge.providerId, Boolean(event.target.checked))}
                >
                  {judge.providerName}
                </Checkbox>
                <Text className="console-caption">{judge.model}</Text>
                {judge.excluded ? <Tag color="red">已排除</Tag> : <Tag color="green">可用</Tag>}
                {!judge.isActive ? <Tag color="grey">未启用</Tag> : null}
              </div>
              {judge.excluded && judge.excludeReason ? (
                <Text className="mt-2 block console-caption" type="danger">排除原因：{judge.excludeReason}</Text>
              ) : null}
            </div>
          ))}
        </div>
      </div>

      <div className="mt-5">
        <Button theme="solid" type="primary" icon={<PlayCircle size={14} />} loading={submitting} onClick={() => void submit()}>
          创建并启动评估
        </Button>
      </div>
    </Card>
  )
}
