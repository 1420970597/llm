import { useMemo, useState } from 'react'
import { Banner, Button, Card, Empty, Select, Space, Spin, Table, Tag, Typography } from '@douyinfe/semi-ui'
import { FileText, RefreshCw } from 'lucide-react'
import type { CleaningFinding, CleaningKeyword, CleaningReport, CleaningRun } from '../../lib/api'
import {
  categoryLabel,
  deriveCategoryHits,
  deriveSeverityDistribution,
  formatCleaningTime,
  percentLabel,
  runStatusColor,
  runStatusLabel,
  stageLabel,
} from './cleaningMeta'

const { Text, Title } = Typography

/**
 * 清洗报告面板（L14 独占）。
 *
 * 数据来源全部为真实接口：
 * - 运行列表：GET /v1/datasets/{id}/cleaning/runs
 * - 报告详情：GET /v1/cleaning/runs/{runId}/report
 * - 命中明细：GET /v1/cleaning/runs/{runId}/findings?stage=&limit=
 *
 * 严重度分布是**客户端派生**：报告结构体里没有 severity 字段，因此用
 * findings.keywordId 与关键词库 severity 做 join 统计（见 cleaningMeta.ts）。
 */
export function CleaningReportPanel({
  runs,
  runsLoading,
  selectedRunId,
  report,
  reportLoading,
  findings,
  findingsLoading,
  findingsStage,
  keywords,
  onSelectRun,
  onRefreshRuns,
  onChangeFindingsStage,
}: {
  runs: CleaningRun[]
  runsLoading: boolean
  selectedRunId: number | null
  report: CleaningReport | null
  reportLoading: boolean
  findings: CleaningFinding[]
  findingsLoading: boolean
  findingsStage: string
  keywords: CleaningKeyword[]
  onSelectRun: (runId: number) => void
  onRefreshRuns: () => Promise<void>
  onChangeFindingsStage: (stage: string) => void
}) {
  const [detailLimit, setDetailLimit] = useState(100)

  const severity = useMemo(() => deriveSeverityDistribution(findings, keywords), [findings, keywords])
  const categoryHits = useMemo(() => deriveCategoryHits(keywords, findings), [keywords, findings])
  const maxKeywordHits = useMemo(
    () => report?.topKeywords.reduce((max, item) => Math.max(max, item.hits), 0) ?? 0,
    [report],
  )

  const visibleFindings = useMemo(() => findings.slice(0, detailLimit), [findings, detailLimit])

  const runColumns = useMemo(
    () => [
      {
        title: '运行',
        dataIndex: 'id',
        render: (value: number) => <Text strong>#{value}</Text>,
      },
      {
        title: '状态',
        dataIndex: 'status',
        render: (value: string) => <Tag color={runStatusColor(value)}>{runStatusLabel(value)}</Tag>,
      },
      {
        title: '阶段',
        dataIndex: 'stages',
        render: (value: string[]) => (value ?? []).map((stage) => <Tag key={stage} color="blue" size="small">{stageLabel(stage)}</Tag>),
      },
      { title: '检查样本', dataIndex: 'scannedItems' },
      { title: '命中样本', dataIndex: 'flaggedItems' },
      { title: '丢弃样本', dataIndex: 'droppedItems' },
      {
        title: '发起时间',
        dataIndex: 'createdAt',
        render: (value: string) => formatCleaningTime(value),
      },
      {
        title: '操作',
        dataIndex: 'operate',
        render: (_: unknown, record: CleaningRun) => (
          <Button size="small" onClick={() => onSelectRun(record.id)} disabled={record.id === selectedRunId}>
            {record.id === selectedRunId ? '当前查看' : '查看报告'}
          </Button>
        ),
      },
    ],
    [onSelectRun, selectedRunId],
  )

  return (
    <div className="console-stack">
      <Card className="console-panel" bodyStyle={{ padding: 20 }}>
        <div className="flex flex-col gap-4 lg:flex-row lg:items-start lg:justify-between">
          <div>
            <Title heading={4} className="!mb-0">清洗记录</Title>
            <Text className="mt-2 block console-caption">每次清洗都会留一条记录，点「查看报告」看这一轮拦下了什么。</Text>
          </div>
          <Button icon={<RefreshCw size={14} />} loading={runsLoading} onClick={() => void onRefreshRuns()}>刷新记录</Button>
        </div>
        <div className="mt-4">
          {runs.length === 0 ? (
            <div className="console-empty">
              <Empty description="这个任务还没有清洗过。在上面「发起一次清洗」里选择阶段与规则，点「开始清洗」。" />
            </div>
          ) : (
            <Table columns={runColumns} dataSource={runs} pagination={false} rowKey="id" />
          )}
        </div>
      </Card>

      {selectedRunId ? (
        <Card className="console-panel" bodyStyle={{ padding: 20 }}>
          <Space className="mb-2">
            <FileText size={16} />
            <Title heading={4} className="!mb-0">清洗报告 #{selectedRunId}</Title>
          </Space>

          {reportLoading ? (
            <div className="console-empty"><Spin /></div>
          ) : !report ? (
            <div className="console-empty"><Empty description="暂时读不到这份报告，请点「刷新记录」后重试。" /></div>
          ) : (
            <>
              {report.conclusions.length > 0 ? (
                <Banner
                  className="mt-3"
                  type={report.run.status === 'failed' ? 'danger' : report.run.status === 'completed' ? 'success' : 'info'}
                  closeIcon={null}
                  description={
                    <div className="console-stack">
                      {report.conclusions.map((line) => (
                        <span key={line}>{line}</span>
                      ))}
                    </div>
                  }
                />
              ) : null}

              <div className="console-summary-grid mt-4">
                <div className="console-summary-row"><span>状态</span><Text strong>{runStatusLabel(report.run.status)}</Text></div>
                <div className="console-summary-row"><span>检查样本</span><Text strong>{report.run.scannedItems}</Text></div>
                <div className="console-summary-row"><span>命中样本</span><Text strong>{report.run.flaggedItems}</Text></div>
                <div className="console-summary-row"><span>丢弃样本</span><Text strong>{report.run.droppedItems}</Text></div>
                <div className="console-summary-row"><span>报告生成时间</span><Text strong>{formatCleaningTime(report.generatedAt)}</Text></div>
                {report.run.errorSummary ? (
                  <div className="console-summary-row"><span>错误摘要</span><Text strong>{report.run.errorSummary}</Text></div>
                ) : null}
              </div>

              <div className="mt-5">
                <Title heading={5} className="!mb-2">按阶段拦截统计</Title>
                {report.stages.length === 0 ? (
                  <Text className="console-caption">这一轮还没有产出分阶段统计（清洗未完成时会是这样）。</Text>
                ) : (
                  <Table
                    pagination={false}
                    rowKey="stage"
                    dataSource={report.stages}
                    columns={[
                      { title: '阶段', dataIndex: 'stage', render: (value: string) => stageLabel(value) },
                      { title: '检查样本', dataIndex: 'scannedItems' },
                      { title: '命中样本', dataIndex: 'flaggedItems' },
                      { title: '丢弃样本', dataIndex: 'droppedItems' },
                      { title: '命中率', dataIndex: 'hitRate', render: (value: number) => percentLabel(value) },
                    ]}
                  />
                )}
              </div>

              <div className="console-card-grid-2 mt-5">
                <div>
                  <Title heading={5} className="!mb-2">命中关键词分布（Top {report.topKeywords.length}）</Title>
                  {report.topKeywords.length === 0 ? (
                    <Text className="console-caption">这一轮没有任何关键词命中。</Text>
                  ) : (
                    <div className="grid gap-3">
                      {report.topKeywords.map((item) => (
                        <div key={item.keywordId} className="grid gap-1.5">
                          <div className="flex items-center justify-between gap-3">
                            <Text strong>{item.pattern}</Text>
                            <Space>
                              <Tag color="blue" size="small">{categoryLabel(item.category)}</Tag>
                              <Text className="console-caption">命中 {item.hits} 次</Text>
                            </Space>
                          </div>
                          <div className="h-2 rounded-full bg-gray-200 overflow-hidden">
                            <div
                              className="h-full rounded-full bg-blue-500"
                              style={{ width: `${maxKeywordHits > 0 ? Math.max(4, (item.hits / maxKeywordHits) * 100) : 0}%` }}
                            />
                          </div>
                          {item.sampleSnippet ? <Text className="text-xs console-caption break-all">{item.sampleSnippet}</Text> : null}
                        </div>
                      ))}
                    </div>
                  )}
                </div>

                <div>
                  <Title heading={5} className="!mb-2">严重度分布（block vs warn）</Title>
                  <Text className="block console-caption">
                    报告接口不返回严重度字段，这里用命中明细的 keywordId 与关键词库的 severity 做客户端关联统计
                    （样本 {severity.total} 条命中）。
                  </Text>
                  <div className="grid grid-cols-2 md:grid-cols-3 gap-3 mt-3 mt-3">
                    <div className="console-domain-item ring-2 ring-red-300">
                      <Text className="console-caption">拦截（block）</Text>
                      <div className="text-3xl font-bold">{severity.block}</div>
                      <Text className="console-caption">{severity.total > 0 ? percentLabel(severity.block / severity.total) : '—'}</Text>
                    </div>
                    <div className="console-domain-item ring-2 ring-orange-300">
                      <Text className="console-caption">告警（warn）</Text>
                      <div className="text-3xl font-bold">{severity.warn}</div>
                      <Text className="console-caption">{severity.total > 0 ? percentLabel(severity.warn / severity.total) : '—'}</Text>
                    </div>
                    {severity.unknown > 0 ? (
                      <div className="console-domain-item">
                        <Text className="console-caption">未知来源</Text>
                        <div className="text-3xl font-bold">{severity.unknown}</div>
                        <Text className="console-caption">对应关键词已被删除</Text>
                      </div>
                    ) : null}
                  </div>

                  <Title heading={6} className="!mb-2 mt-5">命中分类分布</Title>
                  {categoryHits.length === 0 ? (
                    <Text className="console-caption">当前明细里还没有命中，分布会在清洗完成后出现。</Text>
                  ) : (
                    <div className="grid gap-3">
                      {categoryHits.map((item) => (
                        <div key={item.category} className="grid gap-1.5">
                          <div className="flex items-center justify-between gap-3">
                            <Text>{item.label}</Text>
                            <Text className="console-caption">{item.hits} 次</Text>
                          </div>
                        </div>
                      ))}
                    </div>
                  )}
                </div>
              </div>

              <div className="mt-5">
                <div className="flex flex-col gap-4 lg:flex-row lg:items-start lg:justify-between">
                  <div>
                    <Title heading={5} className="!mb-0">命中明细</Title>
                    <Text className="mt-2 block console-caption">
                      每一条都是某个样本的某一步被命中的原文片段。命中最多、最常误伤的词，就是下一步该收紧或补充的地方。
                    </Text>
                  </div>
                  <Space>
                    <Select
                      value={findingsStage}
                      onChange={(value) => onChangeFindingsStage(String(value))}
                      style={{ width: 160 }}
                      optionList={[
                        { value: '', label: '全部阶段' },
                        { value: 'question', label: '问题' },
                        { value: 'reasoning', label: '思维链' },
                        { value: 'answer', label: '答案' },
                      ]}
                    />
                    <Button loading={findingsLoading} onClick={() => onChangeFindingsStage(findingsStage)}>刷新明细</Button>
                  </Space>
                </div>

                {findings.length === 0 ? (
                  <div className="console-empty">
                    <Empty description={findingsStage ? '该阶段没有命中记录。' : '这一轮没有命中记录。'} />
                  </div>
                ) : (
                  <>
                    <Table
                      className="mt-3"
                      rowKey="id"
                      pagination={false}
                      dataSource={visibleFindings}
                      columns={[
                        { title: '阶段', dataIndex: 'stage', render: (value: string) => <Tag color="blue" size="small">{stageLabel(value)}</Tag> },
                        { title: '题目', dataIndex: 'questionId', render: (value: number) => `#${value}` },
                        { title: '命中词', dataIndex: 'matchedText', render: (value: string) => <Text strong>{value}</Text> },
                        { title: '处理动作', dataIndex: 'action', render: (value: string) => <Tag color={value === 'drop' ? 'red' : 'orange'} size="small">{value}</Tag> },
                        {
                          title: '上下文片段',
                          dataIndex: 'snippet',
                          render: (value: string) => <span className="text-xs console-caption break-all">{value}</span>,
                        },
                      ]}
                    />
                    {findings.length > visibleFindings.length ? (
                      <Space className="mt-3">
                        <Text className="console-caption">已显示 {visibleFindings.length} / {findings.length} 条</Text>
                        <Button size="small" onClick={() => setDetailLimit((current) => current + 100)}>再看 100 条</Button>
                      </Space>
                    ) : null}
                  </>
                )}
              </div>
            </>
          )}
        </Card>
      ) : null}
    </div>
  )
}
