import { useCallback, useEffect, useState } from 'react'
import { Button, Card, Empty, Progress, Select, Space, Tag, Typography } from '@douyinfe/semi-ui'
import { RefreshCw } from 'lucide-react'
import { consoleApi, type Dataset, type EvalRun } from '../../lib/api'
import { errorMessage, isRunActive, runStatusMeta, RUN_POLL_INTERVAL_MS } from './evalShared'

const { Title, Text } = Typography

function runPercent(run: EvalRun): number {
  if (run.totalItems <= 0) return 0
  return Math.min(100, Math.round((run.scoredItems / run.totalItems) * 100))
}

function samplingLabel(run: EvalRun): string {
  if (run.samplingMode === 'ratio') return `比例抽样 ${run.sampleRatio}`
  if (run.samplingMode === 'count') return `按条数抽样 ${run.sampleSize}`
  return '全量评估'
}

/** L13：评估运行列表（按数据集过滤 + 进度轮询，卸载时清理定时器）。 */
export function EvalRunList({
  datasets,
  selectedRunId,
  onSelect,
}: {
  datasets: Dataset[]
  selectedRunId: number | null
  onSelect: (runId: number) => void
}) {
  const [datasetId, setDatasetId] = useState<number | null>(null)
  const [runs, setRuns] = useState<EvalRun[]>([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState('')

  const load = useCallback(async () => {
    try {
      const list = await consoleApi.listEvalRuns(datasetId ?? undefined)
      setRuns(list)
      setError('')
    } catch (err) {
      setError(errorMessage(err))
    } finally {
      setLoading(false)
    }
  }, [datasetId])

  useEffect(() => {
    setLoading(true)
    void load()
  }, [load])

  // 有运行处于 queued/running 时轮询进度；组件卸载或状态收敛时清理定时器。
  useEffect(() => {
    if (!runs.some((run) => isRunActive(run.status))) return undefined
    const timer = window.setInterval(() => {
      void load()
    }, RUN_POLL_INTERVAL_MS)
    return () => window.clearInterval(timer)
  }, [runs, load])

  return (
    <Card className="console-panel" bodyStyle={{ padding: 20 }}>
      <div className="flex flex-wrap items-center justify-between gap-3">
        <div>
          <Title heading={5} className="!mb-0">评估运行</Title>
          <Text className="console-caption">运行中每 {RUN_POLL_INTERVAL_MS / 1000} 秒自动刷新进度。</Text>
        </div>
        <Space>
          <Select
            value={datasetId ?? 0}
            style={{ width: 220 }}
            onChange={(value) => setDatasetId(Number(value) === 0 ? null : Number(value))}
            optionList={[
              { label: '全部数据集', value: 0 },
              ...datasets.map((item) => ({ label: item.name, value: item.id })),
            ]}
          />
          <Button icon={<RefreshCw size={14} />} loading={loading} onClick={() => void load()}>刷新</Button>
        </Space>
      </div>

      {error ? (
        <div className="mt-4">
          <Text type="danger">加载评估运行失败：{error}</Text>
        </div>
      ) : null}

      {loading && runs.length === 0 ? (
        <div className="mt-4"><Text className="console-muted">评估运行加载中…</Text></div>
      ) : null}

      {!loading && runs.length === 0 && !error ? (
        <div className="console-empty mt-4">
          <Empty title="暂无评估运行" description="在上方表单中选择数据集与维度，创建并启动一次评估。" />
        </div>
      ) : null}

      <div className="console-record-list mt-4">
        {runs.map((run) => {
          const meta = runStatusMeta(run.status)
          return (
            <div key={run.id} className={`console-record-item${selectedRunId === run.id ? ' console-clickable' : ''}`}>
              <div className="console-record-item-top flex flex-wrap items-center gap-2">
                <Text strong>#{run.id} {run.name}</Text>
                <Tag color={meta.color}>{meta.label}</Tag>
                <Text className="console-caption">{samplingLabel(run)}</Text>
                <Text className="console-caption">维度 {run.dimensionKeys.length} 个 · 裁判 {run.judgeProviderIds.length} 个</Text>
                <Button size="small" onClick={() => onSelect(run.id)}>
                  {selectedRunId === run.id ? '已选中' : '查看详情'}
                </Button>
              </div>
              <div className="mt-3">
                <Progress percent={runPercent(run)} showInfo />
              </div>
              <Text className="mt-2 block console-caption">
                已打分 {run.scoredItems} / 共 {run.totalItems} 条
              </Text>
              {run.errorSummary ? (
                <Text className="mt-2 block console-caption" type="danger">错误摘要：{run.errorSummary}</Text>
              ) : null}
            </div>
          )
        })}
      </div>
    </Card>
  )
}
