import { useCallback, useEffect, useMemo, useState } from 'react'
import { useNavigate } from 'react-router-dom'
import { Banner, Button, Card, Empty, Select, Space, Spin, Tag, Toast, Typography } from '@douyinfe/semi-ui'
import { ArrowRight, Filter, RefreshCw } from 'lucide-react'
import {
  consoleApi,
  type CleaningFinding,
  type CleaningKeyword,
  type CleaningReport,
  type CleaningRule,
  type CleaningRun,
  type Dataset,
} from '../lib/api'
import { CleaningFlowSteps } from './cleaning/CleaningFlowSteps'
import { CleaningKeywordPanel } from './cleaning/CleaningKeywordPanel'
import { CleaningReportPanel } from './cleaning/CleaningReportPanel'
import { CleaningRulePanel } from './cleaning/CleaningRulePanel'
import { CleaningRunPanel } from './cleaning/CleaningRunPanel'
import { datasetStatusLabel, runStatusColor, runStatusLabel } from './cleaning/cleaningMeta'

const { Text, Title } = Typography

const POLL_INTERVAL_MS = 5000

/** Navigation targets can be remapped by the Atelier wrapper while the
 * legacy console keeps its original URLs. The cleaning API/state remains
 * shared; only cross-workbench links differ. */
export type CleaningNavigation = {
  planning: string
  tasks: string
  evaluation: string
  cleaning: string
  results: string
  home: string
}

const LEGACY_CLEANING_NAVIGATION: CleaningNavigation = {
  planning: '/console/planning',
  tasks: '/console/tasks',
  evaluation: '/console/evaluation',
  cleaning: '/console/cleaning',
  results: '/console/results',
  home: '/console/home',
}

/**
 * 数据清洗模块（L14 独占实现）。
 *
 * 契约：docs/plans/eval-and-cleaning-plan.md 第 4.2 节，props 签名固定为
 * `{ datasets: Dataset[] }`，不得修改。
 *
 * 页面分四块，对应「配置 → 发起 → 看报告」的真实链路：
 *   1. 关键词库（GET/POST/PUT/DELETE /v1/cleaning/keywords*）
 *   2. 清洗规则（GET/POST/PUT /v1/cleaning/rules）
 *   3. 发起清洗（POST /v1/datasets/{id}/cleaning/run）
 *   4. 清洗报告（GET /v1/datasets/{id}/cleaning/runs + /v1/cleaning/runs/{id}/report + findings）
 *
 * 全部数据来自真实接口，无 mock。清洗是异步任务：运行中每 5 秒轮询一次，
 * 组件卸载时清理定时器。
 */
export function CleaningView({
  datasets,
  initialDatasetId,
  navigation = LEGACY_CLEANING_NAVIGATION,
}: {
  datasets: Dataset[]
  initialDatasetId?: number | null
  navigation?: CleaningNavigation
}) {
  const navigate = useNavigate()

  const [keywords, setKeywords] = useState<CleaningKeyword[]>([])
  const [keywordsLoading, setKeywordsLoading] = useState(false)
  const [rules, setRules] = useState<CleaningRule[]>([])
  const [rulesLoading, setRulesLoading] = useState(false)

  const [datasetId, setDatasetId] = useState<number | null>(null)
  const [runs, setRuns] = useState<CleaningRun[]>([])
  const [runsLoading, setRunsLoading] = useState(false)

  const [selectedRunId, setSelectedRunId] = useState<number | null>(null)
  const [report, setReport] = useState<CleaningReport | null>(null)
  const [reportLoading, setReportLoading] = useState(false)
  const [findings, setFindings] = useState<CleaningFinding[]>([])
  const [findingsLoading, setFindingsLoading] = useState(false)
  const [findingsStage, setFindingsStage] = useState('')

  // issue #106：无规则时，让用户从「发起清洗」面板直达「新建规则」弹窗。
  // 用递增计数器传递命令式意图（同一动作需要能被反复触发），
  // 而不是用一个布尔 prop（第二次 true→true 不会触发变化）。
  const [createRuleSignal, setCreateRuleSignal] = useState(0)

  const loadKeywords = useCallback(async () => {
    setKeywordsLoading(true)
    try {
      setKeywords(await consoleApi.listCleaningKeywords())
    } catch (error) {
      Toast.error((error as Error).message)
    } finally {
      setKeywordsLoading(false)
    }
  }, [])

  const loadRules = useCallback(async () => {
    setRulesLoading(true)
    try {
      setRules(await consoleApi.listCleaningRules())
    } catch (error) {
      Toast.error((error as Error).message)
    } finally {
      setRulesLoading(false)
    }
  }, [])

  const loadFindings = useCallback(async (runId: number, stage: string) => {
    setFindingsLoading(true)
    try {
      setFindings(await consoleApi.listCleaningFindings(runId, stage || undefined, 200))
    } catch (error) {
      Toast.error((error as Error).message)
    } finally {
      setFindingsLoading(false)
    }
  }, [])

  const loadReport = useCallback(async (runId: number, stage: string) => {
    setReportLoading(true)
    try {
      setReport(await consoleApi.cleaningReport(runId))
      await loadFindings(runId, stage)
    } catch (error) {
      setReport(null)
      setFindings([])
      Toast.error((error as Error).message)
    } finally {
      setReportLoading(false)
    }
  }, [loadFindings])

  const loadRuns = useCallback(async (targetDatasetId: number) => {
    setRunsLoading(true)
    try {
      const items = await consoleApi.listCleaningRuns(targetDatasetId)
      setRuns(items)
      return items
    } catch (error) {
      Toast.error((error as Error).message)
      return [] as CleaningRun[]
    } finally {
      setRunsLoading(false)
    }
  }, [])

  useEffect(() => {
    void loadKeywords()
    void loadRules()
  }, [loadKeywords, loadRules])

  // 默认选中第一个任务；任务列表变化（例如新建任务）时保持已有选择。
  useEffect(() => {
    if (datasets.length === 0) {
      setDatasetId(null)
      return
    }
    setDatasetId((current) => {
      if (initialDatasetId && datasets.some((item) => item.id === initialDatasetId)) return initialDatasetId
      return current && datasets.some((item) => item.id === current) ? current : datasets[0].id
    })
  }, [datasets, initialDatasetId])

  useEffect(() => {
    if (!datasetId) {
      setRuns([])
      setSelectedRunId(null)
      setReport(null)
      setFindings([])
      return
    }
    void loadRuns(datasetId)
  }, [datasetId, loadRuns])

  // 只看运行列表：报告里的 run 是 worker 写入快照（status 恒为 queued），
  // 用它判断会导致轮询永不停止。列表在同一轮询里被刷新，是权威来源。
  const unfinished = useMemo(
    () => runs.some((run) => run.status === 'queued' || run.status === 'running'),
    [runs],
  )

  // 清洗是异步任务：有未完成的运行时每 5 秒轮询一次，卸载或完成时清掉定时器。
  useEffect(() => {
    if (!unfinished || !datasetId) {
      return
    }
    const timer = window.setInterval(() => {
      void loadRuns(datasetId).then((items) => {
        if (selectedRunId && items.some((run) => run.id === selectedRunId)) {
          void loadReport(selectedRunId, findingsStage)
        }
      })
    }, POLL_INTERVAL_MS)
    return () => window.clearInterval(timer)
  }, [unfinished, datasetId, selectedRunId, findingsStage, loadRuns, loadReport])

  const selectRun = useCallback(
    (runId: number) => {
      setSelectedRunId(runId)
      setFindingsStage('')
      void loadReport(runId, '')
    },
    [loadReport],
  )

  const changeFindingsStage = useCallback(
    (stage: string) => {
      setFindingsStage(stage)
      if (selectedRunId) {
        void loadFindings(selectedRunId, stage)
      }
    },
    [loadFindings, selectedRunId],
  )

  const refreshAll = useCallback(async () => {
    await Promise.all([loadKeywords(), loadRules()])
    if (datasetId) {
      const items = await loadRuns(datasetId)
      if (selectedRunId && items.some((run) => run.id === selectedRunId)) {
        await loadReport(selectedRunId, findingsStage)
      }
    }
    Toast.success('清洗数据已刷新')
  }, [datasetId, findingsStage, loadKeywords, loadReport, loadRules, loadRuns, selectedRunId])

  const activeDataset = datasets.find((item) => item.id === datasetId) ?? null
  const latestRun = runs[0] ?? null
  const latestFinished = latestRun?.status === 'completed' ? latestRun : null

  if (datasets.length === 0) {
    return (
      <div className="console-page-shell">
        <div className="console-route-banner">
          <span className="console-chip">数据清洗</span>
          <Title heading={2} className="!mb-0 console-page-title">拦截拒答与异常样本</Title>
          <Text className="console-page-subtitle">在数据交付前，把模型因安全限制拒答、含糊推脱或留下占位文本的样本挑出来。</Text>
        </div>
        <CleaningFlowSteps onNavigate={(route) => navigate(mapCleaningFlowRoute(route, navigation))} />
        <Card className="console-panel" bodyStyle={{ padding: 20 }}>
          <div className="console-empty">
            <Empty description="你还没有任何任务，所以暂时没有数据可以清洗。先到「新建任务」创建任务并生成数据，再回来这里。" />
          </div>
          <Space className="mt-4">
            <Button theme="solid" type="primary" onClick={() => navigate(navigation.planning)}>去新建任务</Button>
            <Button onClick={() => navigate(navigation.home)}>回到工作台</Button>
          </Space>
        </Card>
      </div>
    )
  }

  return (
    <div className="console-page-shell">
      <div className="flex flex-col gap-4 lg:flex-row lg:items-end lg:justify-between">
        <div className="console-route-banner">
          <span className="console-chip">数据清洗</span>
          <Title heading={2} className="!mb-0 console-page-title">拦截拒答与异常样本</Title>
          <Text className="console-page-subtitle">
            数据清洗是任务流程的第 4 步：先在这里配置要拦截的词与规则，再对某个任务发起清洗，最后在「数据资产」导出干净数据。
          </Text>
        </div>
        <Space wrap>
          <Button icon={<RefreshCw size={14} />} loading={keywordsLoading || rulesLoading || runsLoading} onClick={() => void refreshAll()}>
            刷新
          </Button>
          <Button icon={<Filter size={14} />} onClick={() => navigate(navigation.results)}>去数据资产导出</Button>
        </Space>
      </div>

      <CleaningFlowSteps onNavigate={(route) => navigate(mapCleaningFlowRoute(route, navigation))} />

      <Card className="console-panel" bodyStyle={{ padding: 20 }}>
        <div className="flex flex-col gap-4 lg:flex-row lg:items-start lg:justify-between">
          <div>
            <Text className="mb-2 block font-medium">当前查看的任务</Text>
            <Select
              value={datasetId ?? undefined}
              optionList={datasets.map((dataset) => ({ value: dataset.id, label: `${dataset.name}（任务 #${dataset.id}）` }))}
              onChange={(value) => setDatasetId(Number(value))}
              style={{ minWidth: 280 }}
            />
          </div>
          <div className="console-summary-grid flex-1 min-w-[320px]">
            <div className="console-summary-row"><span>任务状态</span><Text strong>{activeDataset ? datasetStatusLabel(activeDataset.status) : '—'}</Text></div>
            <div className="console-summary-row"><span>清洗记录</span><Text strong>{runs.length} 次</Text></div>
            <div className="console-summary-row">
              <span>最近一次</span>
              {latestRun ? (
                <Space>
                  <Tag color={runStatusColor(latestRun.status)}>{runStatusLabel(latestRun.status)}</Tag>
                  <Text className="console-caption">#{latestRun.id}</Text>
                </Space>
              ) : (
                <Text strong>还没清洗过</Text>
              )}
            </div>
          </div>
        </div>
      </Card>

      {latestFinished ? (
        <Banner
          type="success"
          closeIcon={null}
          description={
            <span>
              清洗 #{latestFinished.id} 已完成：检查 {latestFinished.scannedItems} 条，命中 {latestFinished.flaggedItems} 条，
              丢弃 {latestFinished.droppedItems} 条。下一步：到「数据资产」查看并导出干净数据。
            </span>
          }
        />
      ) : null}

      {runsLoading && runs.length === 0 ? (
        <Card className="console-panel" bodyStyle={{ padding: 20 }}>
          <div className="console-empty"><Spin /></div>
        </Card>
      ) : null}

      <CleaningRunPanel
        datasets={datasets}
        rules={rules}
        onRulesChanged={loadRules}
        onRequestCreateRule={() => setCreateRuleSignal((current) => current + 1)}
        onEnqueued={(targetDatasetId) => {
          if (targetDatasetId !== datasetId) {
            setDatasetId(targetDatasetId)
          }
          void loadRuns(targetDatasetId)
        }}
      />

      <CleaningReportPanel
        runs={runs}
        runsLoading={runsLoading}
        selectedRunId={selectedRunId}
        report={report}
        reportLoading={reportLoading}
        findings={findings}
        findingsLoading={findingsLoading}
        findingsStage={findingsStage}
        keywords={keywords}
        onSelectRun={selectRun}
        onRefreshRuns={async () => {
          if (datasetId) {
            await loadRuns(datasetId)
          }
        }}
        onChangeFindingsStage={changeFindingsStage}
      />

      <CleaningKeywordPanel keywords={keywords} loading={keywordsLoading} onRefresh={loadKeywords} />

      <CleaningRulePanel rules={rules} loading={rulesLoading} onRefresh={loadRules} createRequestSignal={createRuleSignal} />

      <Card className="console-panel" bodyStyle={{ padding: 20 }}>
        <Title heading={5} className="!mb-2">清洗之后去哪里</Title>
        <Text className="block console-caption">
          清洗只改样本状态，不改写内容：被丢弃的样本不会再进入导出，被标记的样本仍可在数据资产里核对。
        </Text>
        <Space className="mt-4" wrap>
          <Button icon={<ArrowRight size={14} />} onClick={() => navigate(navigation.results)}>去数据资产导出</Button>
          <Button onClick={() => navigate(navigation.evaluation)}>回到质量评估</Button>
          <Button onClick={() => navigate(navigation.tasks)}>回到我的任务</Button>
        </Space>
      </Card>
    </div>
  )
}

export default CleaningView

function mapCleaningFlowRoute(route: string, navigation: CleaningNavigation): string {
  switch (route) {
    case '/console/planning':
      return navigation.planning
    case '/console/tasks':
      return navigation.tasks
    case '/console/evaluation':
      return navigation.evaluation
    case '/console/cleaning':
      return navigation.cleaning
    case '/console/results':
      return navigation.results
    default:
      return route
  }
}
