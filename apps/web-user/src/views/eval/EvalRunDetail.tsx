import { useCallback, useEffect, useMemo, useState } from 'react'
import { Banner, Button, Card, Empty, Progress, Space, Table, Tag, Typography } from '@douyinfe/semi-ui'
import { AlertTriangle, RefreshCw } from 'lucide-react'
import {
  consoleApi,
  type EvalJudgeStat,
  type EvalReport,
  type EvalRunDetail as EvalRunDetailPayload,
} from '../../lib/api'
import {
  agreementLevel,
  AGREEMENT_WARNING_THRESHOLD,
  errorMessage,
  formatPercent,
  formatScore,
  isRunActive,
  runStatusMeta,
  scoreStatusMeta,
  RUN_POLL_INTERVAL_MS,
} from './evalShared'
import { EvalScoreTable } from './EvalScoreTable'

const { Title, Text } = Typography

/** L13：评估运行详情（裁判/维度）与汇总报告。 */
export function EvalRunDetail({ runId }: { runId: number }) {
  const [detail, setDetail] = useState<EvalRunDetailPayload | null>(null)
  const [report, setReport] = useState<EvalReport | null>(null)
  const [reportError, setReportError] = useState('')
  const [error, setError] = useState('')
  const [loading, setLoading] = useState(true)

  const load = useCallback(async () => {
    try {
      const next = await consoleApi.getEvalRun(runId)
      setDetail(next)
      setError('')
    } catch (err) {
      setError(errorMessage(err))
      setLoading(false)
      return
    }
    try {
      const nextReport = await consoleApi.evalReport(runId)
      setReport(nextReport)
      setReportError('')
    } catch (err) {
      setReport(null)
      setReportError(errorMessage(err))
    } finally {
      setLoading(false)
    }
  }, [runId])

  useEffect(() => {
    setLoading(true)
    setDetail(null)
    setReport(null)
    void load()
  }, [load])

  // 运行未结束时轮询详情与报告；卸载或收敛后清理定时器。
  useEffect(() => {
    if (!detail) return undefined
    if (!isRunActive(detail.run.status)) return undefined
    const timer = window.setInterval(() => {
      void load()
    }, RUN_POLL_INTERVAL_MS)
    return () => window.clearInterval(timer)
  }, [detail, load])

  const weakestSet = useMemo(() => new Set((report?.weakestItems ?? []).map((item) => item.questionId)), [report])

  if (loading && !detail) {
    return (
      <Card className="console-panel" bodyStyle={{ padding: 20 }}>
        <Text className="console-muted">评估运行 #{runId} 加载中…</Text>
      </Card>
    )
  }

  if (error || !detail) {
    return (
      <Card className="console-panel" bodyStyle={{ padding: 20 }}>
        <Text type="danger">加载评估运行 #{runId} 失败：{error || '无数据'}</Text>
      </Card>
    )
  }

  const run = detail.run
  const meta = runStatusMeta(run.status)
  const percent = run.totalItems > 0 ? Math.min(100, Math.round((run.scoredItems / run.totalItems) * 100)) : 0
  const level = report ? agreementLevel(report.judgeAgreement) : 'not_applicable'

  return (
    <div className="console-stack">
      <Card className="console-panel" bodyStyle={{ padding: 20 }}>
        <div className="flex flex-wrap items-center justify-between gap-3">
          <div>
            <Title heading={5} className="!mb-0">运行 #{run.id} · {run.name}</Title>
            <Text className="console-caption">
              数据集 #{run.datasetId} · 抽样 {run.samplingMode} · 目标类型 {run.targetKind} · 创建于 {run.createdAt}
            </Text>
          </div>
          <Space>
            <Tag color={meta.color}>{meta.label}</Tag>
            <Button icon={<RefreshCw size={14} />} loading={loading} onClick={() => void load()}>刷新</Button>
          </Space>
        </div>
        <div className="mt-4">
          <Progress percent={percent} showInfo />
        </div>
        <Text className="mt-2 block console-caption">已打分 {run.scoredItems} / 共 {run.totalItems} 条</Text>
        {run.errorSummary ? (
          <Text className="mt-2 block console-caption" type="danger">错误摘要：{run.errorSummary}</Text>
        ) : null}

        <div className="mt-5">
          <Text strong>裁判模型（{detail.judges.length}）</Text>
          <div className="console-record-list mt-3">
            {detail.judges.length === 0 ? (
              <Text className="console-caption">该运行尚未绑定裁判模型。</Text>
            ) : null}
            {detail.judges.map((judge) => {
              const judgeMeta = scoreStatusMeta(judge.status)
              return (
                <div key={judge.id} className="console-record-item">
                  <div className="console-record-item-top flex flex-wrap items-center gap-2">
                    <Text strong>{judge.providerName}</Text>
                    <Text className="console-caption">{judge.model}</Text>
                    <Tag color={judgeMeta.color}>{judgeMeta.label}</Tag>
                    {judge.excluded ? <Tag color="red">已排除（不参与打分）</Tag> : <Tag color="green">参与打分</Tag>}
                    <Text className="console-caption">已打分 {judge.scoredItems} 条</Text>
                  </div>
                  {judge.excluded && judge.excludeReason ? (
                    <Text className="mt-2 block console-caption" type="danger">排除原因：{judge.excludeReason}</Text>
                  ) : null}
                  {judge.errorSummary ? (
                    <Text className="mt-2 block console-caption" type="danger">错误摘要：{judge.errorSummary}</Text>
                  ) : null}
                </div>
              )
            })}
          </div>
        </div>

        <div className="mt-5">
          <Text strong>评估维度（{detail.dimensions.length}）</Text>
          <div className="mt-3 flex flex-wrap gap-2">
            {detail.dimensions.length === 0 ? <Text className="console-caption">该运行未选择任何维度。</Text> : null}
            {detail.dimensions.map((dimension) => (
              <span key={dimension.id} className="console-chip">
                {dimension.name}（{dimension.category}，{dimension.scaleMin}~{dimension.scaleMax}）
              </span>
            ))}
          </div>
        </div>
      </Card>

      {reportError ? (
        <Card className="console-panel" bodyStyle={{ padding: 20 }}>
          <Text type="danger">报告加载失败：{reportError}</Text>
        </Card>
      ) : null}

      {report ? (
        <>
          <Card className="console-panel" bodyStyle={{ padding: 20 }}>
            <Title heading={5} className="!mb-0">汇总报告</Title>
            <Text className="console-caption">
              数据集 {report.datasetName} · 样本 {report.sampleCount} 条 · 生成于 {report.generatedAt}
            </Text>

            <div className="console-card-grid-2 mt-4">
              <Card className="console-stat-card" bodyStyle={{ padding: 20 }}>
                <Text className="console-muted">综合得分</Text>
                <div className="mt-3 console-stat-value">{formatScore(report.overallScore)}</div>
                <Text className="mt-2 block console-caption">所有裁判在所有维度上的加权平均</Text>
              </Card>
              <Card className="console-stat-card" bodyStyle={{ padding: 20 }}>
                <Text className="console-muted">裁判一致性</Text>
                <div className="mt-3 console-stat-value">{formatPercent(report.judgeAgreement)}</div>
                <Text className="mt-2 block console-caption">多裁判打分的一致程度，越高越可信</Text>
              </Card>
            </div>

            {level === 'not_applicable' ? (
              <div className="mt-4">
                <Text className="console-caption">
                  一致性不适用（后端返回 {report.judgeAgreement}）：样本不足或尚未产出打分，暂不评判裁判分歧。
                </Text>
              </div>
            ) : level !== 'high' ? (
              <div className="mt-4">
                <Banner
                  type={level === 'low' ? 'danger' : 'warning'}
                  icon={<AlertTriangle size={16} />}
                  title={level === 'low' ? '裁判一致性严重偏低' : '裁判一致性偏低'}
                  description={`当前一致性 ${formatPercent(report.judgeAgreement)}，低于可信阈值 ${formatPercent(AGREEMENT_WARNING_THRESHOLD)}。多裁判评分分歧较大，结论需人工复核。`}
                  closeIcon={null}
                />
              </div>
            ) : (
              <div className="mt-4">
                <Text className="console-caption">
                  一致性 {formatPercent(report.judgeAgreement)}，达到可信阈值 {formatPercent(AGREEMENT_WARNING_THRESHOLD)}。
                </Text>
              </div>
            )}

            <div className="mt-5">
              <Text strong>各裁判得分</Text>
              <Table<EvalJudgeStat>
                className="mt-3"
                pagination={false}
                size="small"
                rowKey="providerId"
                dataSource={report.judges}
                empty="暂无裁判打分数据"
                columns={[
                  { title: '裁判模型', dataIndex: 'providerName' },
                  { title: '模型', dataIndex: 'model' },
                  { title: '得分', render: (_value, judge) => formatScore(judge.score) },
                  { title: '样本数', dataIndex: 'sampleCount' },
                ]}
              />
            </div>

            <div className="mt-5">
              <Text strong>逐维度明细</Text>
              <Table
                className="mt-3"
                pagination={false}
                size="small"
                rowKey="dimensionKey"
                dataSource={report.dimensions}
                empty="暂无维度统计"
                columns={[
                  { title: '维度', dataIndex: 'name' },
                  { title: '分类', dataIndex: 'category' },
                  { title: '得分', render: (_value, dimension) => formatScore(dimension.score) },
                  { title: '样本数', dataIndex: 'sampleCount' },
                  { title: '标准差', render: (_value, dimension) => formatScore(dimension.stdDev) },
                  { title: '最低', render: (_value, dimension) => formatScore(dimension.min) },
                  { title: '最高', render: (_value, dimension) => formatScore(dimension.max) },
                ]}
              />
            </div>

            <div className="mt-5">
              <Text strong>最弱条目（{report.weakestItems.length}）</Text>
              <div className="console-record-list mt-3">
                {report.weakestItems.length === 0 ? (
                  <Text className="console-caption">没有需要特别关注的弱条目。</Text>
                ) : null}
                {report.weakestItems.map((item) => (
                  <div key={`${item.questionId}-${item.itemIndex}`} className="console-record-item">
                    <div className="console-record-item-top flex flex-wrap items-center gap-2">
                      <Text strong>题目 #{item.questionId}</Text>
                      <Tag color={weakestSet.has(item.questionId) ? 'red' : 'grey'}>条目序号 {item.itemIndex}</Tag>
                      <Text className="console-caption">得分 {formatScore(item.score)}</Text>
                    </div>
                  </div>
                ))}
              </div>
            </div>

            <div className="mt-5">
              <Text strong>分析结论</Text>
              {report.conclusions.length === 0 ? (
                <div className="console-empty mt-3">
                  <Empty title="暂无分析结论" description="后端未返回结论文本，可能是样本不足或运行尚未完成。" />
                </div>
              ) : (
                <ul className="mt-3">
                  {report.conclusions.map((conclusion, index) => (
                    <li key={index} className="mb-2">
                      <Text>{conclusion}</Text>
                    </li>
                  ))}
                </ul>
              )}
            </div>
          </Card>

          <EvalScoreTable run={run} judges={detail.judges} dimensions={detail.dimensions} />
        </>
      ) : null}
    </div>
  )
}
