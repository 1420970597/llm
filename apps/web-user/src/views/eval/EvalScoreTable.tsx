import { useCallback, useEffect, useMemo, useState } from 'react'
import { Button, Card, Empty, Select, Space, Table, Tag, Typography } from '@douyinfe/semi-ui'
import { RefreshCw } from 'lucide-react'
import {
  consoleApi,
  type EvalDimension,
  type EvalItem,
  type EvalItemScore,
  type EvalRun,
  type EvalRunJudge,
} from '../../lib/api'
import { errorMessage, formatScore, isRunActive, RUN_POLL_INTERVAL_MS, scoreStatusMeta } from './evalShared'

const { Title, Text } = Typography

const ITEM_PAGE_SIZE = 50

type ScoreRow = EvalItemScore & {
  questionId: number | null
  itemIndex: number | null
}

/** L13：逐条打分明细（按裁判与维度过滤）。 */
export function EvalScoreTable({
  run,
  judges,
  dimensions,
}: {
  run: EvalRun
  judges: EvalRunJudge[]
  dimensions: EvalDimension[]
}) {
  const [judgeProviderId, setJudgeProviderId] = useState<number | null>(null)
  const [dimensionKey, setDimensionKey] = useState<string | null>(null)
  const [scores, setScores] = useState<EvalItemScore[]>([])
  const [items, setItems] = useState<EvalItem[]>([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState('')

  const load = useCallback(async () => {
    try {
      const [scoreList, itemList] = await Promise.all([
        consoleApi.evalScores(run.id, judgeProviderId ?? undefined, dimensionKey ?? undefined),
        consoleApi.listEvalItems(run.id, ITEM_PAGE_SIZE, 0),
      ])
      setScores(scoreList)
      setItems(itemList)
      setError('')
    } catch (err) {
      setError(errorMessage(err))
    } finally {
      setLoading(false)
    }
  }, [run.id, judgeProviderId, dimensionKey])

  useEffect(() => {
    setLoading(true)
    void load()
  }, [load])

  // 运行未结束时轮询明细；卸载或收敛后清理定时器。
  useEffect(() => {
    if (!isRunActive(run.status)) return undefined
    const timer = window.setInterval(() => {
      void load()
    }, RUN_POLL_INTERVAL_MS)
    return () => window.clearInterval(timer)
  }, [run.status, load])

  const itemIndex = useMemo(() => {
    const map = new Map<number, EvalItem>()
    for (const item of items) map.set(item.id, item)
    return map
  }, [items])

  const judgeNames = useMemo(() => {
    const map = new Map<number, string>()
    for (const judge of judges) map.set(judge.providerId, judge.providerName)
    return map
  }, [judges])

  const dimensionNames = useMemo(() => {
    const map = new Map<string, string>()
    for (const dimension of dimensions) map.set(dimension.key, dimension.name)
    return map
  }, [dimensions])

  const rows: ScoreRow[] = useMemo(
    () =>
      scores.map((score) => {
        const item = itemIndex.get(score.evalItemId)
        return {
          ...score,
          questionId: item ? item.questionId : null,
          itemIndex: item ? item.itemIndex : null,
        }
      }),
    [scores, itemIndex],
  )

  return (
    <Card className="console-panel" bodyStyle={{ padding: 20 }}>
      <div className="flex flex-wrap items-center justify-between gap-3">
        <div>
          <Title heading={5} className="!mb-0">逐条打分明细</Title>
          <Text className="console-caption">
            共 {rows.length} 条打分记录；条目信息取自运行前 {ITEM_PAGE_SIZE} 条评估条目。
          </Text>
        </div>
        <Space>
          <Select
            value={judgeProviderId ?? 0}
            style={{ width: 220 }}
            onChange={(value) => setJudgeProviderId(Number(value) === 0 ? null : Number(value))}
            optionList={[
              { label: '全部裁判', value: 0 },
              ...judges.map((judge) => ({ label: judge.providerName, value: judge.providerId })),
            ]}
          />
          <Select
            value={dimensionKey ?? ''}
            style={{ width: 220 }}
            onChange={(value) => setDimensionKey(String(value) === '' ? null : String(value))}
            optionList={[
              { label: '全部维度', value: '' },
              ...dimensions.map((dimension) => ({ label: dimension.name, value: dimension.key })),
            ]}
          />
          <Button icon={<RefreshCw size={14} />} loading={loading} onClick={() => void load()}>刷新</Button>
        </Space>
      </div>

      {error ? (
        <div className="mt-4">
          <Text type="danger">加载打分明细失败：{error}</Text>
        </div>
      ) : null}

      {loading && rows.length === 0 && !error ? (
        <div className="mt-4"><Text className="console-muted">打分明细加载中…</Text></div>
      ) : null}

      {!loading && rows.length === 0 && !error ? (
        <div className="console-empty mt-4">
          <Empty title="暂无打分明细" description="当前过滤条件下没有打分记录，可切换裁判或维度，或等待评估运行产出结果。" />
        </div>
      ) : null}

      {rows.length > 0 ? (
        <Table<ScoreRow>
          className="mt-4"
          size="small"
          pagination={{ pageSize: 20 }}
          rowKey="id"
          dataSource={rows}
          columns={[
            { title: '题目', render: (_value, row) => (row.questionId === null ? '—' : `#${row.questionId}`) },
            { title: '条目序号', render: (_value, row) => (row.itemIndex === null ? '—' : row.itemIndex) },
            { title: '裁判', render: (_value, row) => judgeNames.get(row.judgeProviderId) ?? `#${row.judgeProviderId}` },
            { title: '维度', render: (_value, row) => dimensionNames.get(row.dimensionKey) ?? row.dimensionKey },
            { title: '得分', render: (_value, row) => formatScore(row.score) },
            {
              title: '状态',
              render: (_value, row) => {
                const meta = scoreStatusMeta(row.status)
                return <Tag color={meta.color}>{meta.label}</Tag>
              },
            },
            {
              title: '打分理由',
              render: (_value, row) => (
                <Text className="console-caption">{row.rationale || '（无理由文本）'}</Text>
              ),
            },
          ]}
        />
      ) : null}
    </Card>
  )
}
