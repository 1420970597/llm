import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { useNavigate, useParams, useSearchParams } from 'react-router-dom'
import { Button, Card, Empty, Input, InputNumber, Select, Spin, Tag, Typography } from '@douyinfe/semi-ui'
import { AlertTriangle, ArrowLeftRight, Pause, Play, RefreshCw, RotateCcw } from 'lucide-react'
import { newIdempotencyKey, projectPath, studioApi } from '../../lib/api/studio'
import type {
  BatchDetail,
  BatchCapabilities,
  BatchFailure,
  BatchSnapshot,
  BatchSummary,
  CreateBatchRequest,
  AdoptedBatch,
  Page,
} from '../../lib/api/studio'
import { client } from '../../lib/api'
import { useProjectScope } from '../ProjectLayout'
import { projectHref } from '../StudioLayout'

/**
 * 生产工作区页面（Issue #160 T13）：批次列表、详情、异常恢复、试制与扩量规划。
 *
 * 契约：docs/plans/atelier-implementation.md §2.4/§2.6、§3；
 * docs/plans/atelier-api-contract.md §2.3/§2.4/§3。
 *
 * 四条来自 T13 验收项的核心语义，都体现在这里：
 *
 *  1. **计划数 / 实际数 / 在途 / 失败必须可区分**（§2.1 禁止相互冒充）：
 *     四个数字分列展示，不合成「进度百分比」。
 *  2. **暂停不撤回在途**：暂停后显示「已停止提交新请求」+ **在途数量**，
 *     而不是显示「已暂停」让人以为不再花钱。
 *  3. **恢复只提交失败/未完成项**：按钮文案与结果都明确说明「重置了 N 项」，
 *     并且反复点击不会重复成果（幂等由服务端保证，界面不重复发请求）。
 *  4. **改模型/标准必须新建批次**：详情页显式说明，并把「复制为新批次」
 *     作为唯一入口 —— 直接改已提交批次的快照会让「已有成功内容」失去可复现性。
 */

type BatchStatusTone = 'running' | 'paused' | 'failed' | 'done' | 'queued'

const STATUS_LABEL: Record<string, string> = {
  queued: '排队中',
  running: '运行中',
  pause_requested: '暂停请求中',
  paused: '已暂停（在途仍会计费）',
  partial_failed: '部分失败',
  completed: '已完成',
  failed: '失败',
}

function statusTone(status: string): BatchStatusTone {
  switch (status) {
    case 'running':
    case 'pause_requested':
      return 'running'
    case 'paused':
      return 'paused'
    case 'partial_failed':
    case 'failed':
      return 'failed'
    case 'completed':
      return 'done'
    default:
      return 'queued'
  }
}

// ---------------------------------------------------------------------------
// 批次列表（R01）
// ---------------------------------------------------------------------------

export function RunsPage() {
  const scope = useProjectScope()
  const navigate = useNavigate()
  const { Title, Text } = Typography
  const [batches, setBatches] = useState<BatchSummary[]>([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)
  const [actionError, setActionError] = useState<string | null>(null)
  const [busy, setBusy] = useState<number | null>(null)
  const [canRun, setCanRun] = useState(false)

  const load = useCallback(async () => {
    setLoading(true)
    setError(null)
    try {
      const [response, overview] = await Promise.all([
        client.get<Page<BatchSummary>>(`${projectPath(scope.projectId)}/batches?limit=50`),
        studioApi.overviewEnvelope(scope.projectId),
      ])
      setBatches(response.data.items ?? [])
      setCanRun(overview.capabilities.canRun === true)
    } catch (loadError) {
      setError(loadError instanceof Error ? loadError.message : '加载批次失败')
    } finally {
      setLoading(false)
    }
  }, [scope.projectId])

  useEffect(() => {
    void load()
  }, [load])

  const control = useCallback(
    async (batchId: number, action: 'pause' | 'resume' | 'retry-failed') => {
      setBusy(batchId)
      setActionError(null)
      try {
        const url = `${projectPath(scope.projectId)}/batches/b_${batchId}/${action}`
        await client.post(url)
        await load()
      } catch (controlError) {
        // 409 = 状态机不允许该动作。文案来自服务端（它知道当前是什么状态），
        // 因此直接展示而不是替换成一句含糊的「操作失败」。
        setActionError(controlError instanceof Error ? controlError.message : '操作失败')
      } finally {
        setBusy(null)
      }
    },
    [load, scope.projectId],
  )

  return (
    <div className="console-page" data-studio-page="runs">
      <div className="console-page__header">
        <div>
          <Title heading={4} className="!mb-1">
            生产批次
          </Title>
          <Text type="tertiary">
            同一项目可以有多个并行批次；列表按最近创建排序，不按「最大 ID 猜当前运行」。
          </Text>
        </div>
        <div className="flex gap-2">
          <Button icon={<RefreshCw size={14} />} onClick={() => void load()} disabled={loading}>
            刷新
          </Button>
          {canRun ? <Button onClick={() => navigate(projectHref('project.pilot', scope.projectId))}>新建试制</Button> : null}
          {canRun ? (
            <Button theme="solid" type="primary" onClick={() => navigate(projectHref('project.runNew', scope.projectId))}>
              扩量规划
            </Button>
          ) : null}
        </div>
      </div>

      {actionError ? (
        <div className="wizard-field__error mb-3" role="alert">
          {actionError}
        </div>
      ) : null}

      {loading ? (
        <div className="flex justify-center py-10">
          <Spin tip="正在加载批次" />
        </div>
      ) : error ? (
        <Card className="console-card">
          <Text strong className="block">
            批次列表加载失败
          </Text>
          <Text type="tertiary">{error}</Text>
          <div className="mt-3">
            <Button size="small" onClick={() => void load()}>
              重试
            </Button>
          </div>
        </Card>
      ) : batches.length === 0 ? (
        <Card className="console-card">
          <Empty description="还没有批次。先跑一次小批试制验证方案，再规划扩量。" />
        </Card>
      ) : (
        <div className="batch-table" data-batch-table="true">
          <div className="batch-row batch-row--head">
            <span>批次</span>
            <span>用途</span>
            <span>状态</span>
            <span>计划</span>
            <span>完成</span>
            <span>失败</span>
            <span>在途</span>
            <span>操作</span>
          </div>
          {batches.map((batch) => (
            <div key={batch.batchId} className="batch-row" data-batch-id={batch.resourceId}>
              <button
                type="button"
                className="batch-row__link"
                onClick={() => navigate(projectHref('project.runDetail', scope.projectId, { batchId: batch.resourceId }))}
              >
                {batch.resourceId}
              </button>
              <span>{batch.purpose === 'pilot' ? '试制' : '扩量'}</span>
              <span className={`batch-status batch-status--${statusTone(batch.status)}`}>
                {STATUS_LABEL[batch.status] ?? batch.status}
              </span>
              {/* 四个数字分列：计划量是意图，完成/失败/在途是事实。 */}
              <span data-count="planned">{batch.plannedUnits}</span>
              <span data-count="completed">{batch.completedUnits}</span>
              <span data-count="failed">{batch.failedUnits}</span>
              <span data-count="inFlight">{batch.inFlightUnits}</span>
              <span className="batch-row__actions">
                {batch.capabilities?.canPause && (batch.status === 'running' || batch.status === 'queued') ? (
                  <Button
                    size="small"
                    icon={<Pause size={13} />}
                    loading={busy === batch.batchId}
                    onClick={() => void control(batch.batchId, 'pause')}
                  >
                    暂停
                  </Button>
                ) : null}
                {batch.capabilities?.canResume && (batch.status === 'paused' || batch.status === 'pause_requested') ? (
                  <Button
                    size="small"
                    icon={<Play size={13} />}
                    loading={busy === batch.batchId}
                    onClick={() => void control(batch.batchId, 'resume')}
                  >
                    继续
                  </Button>
                ) : null}
                {batch.capabilities?.canRetryFailed && batch.failedUnits > 0 ? (
                  <Button
                    size="small"
                    icon={<RotateCcw size={13} />}
                    loading={busy === batch.batchId}
                    onClick={() => void control(batch.batchId, 'retry-failed')}
                  >
                    恢复失败项
                  </Button>
                ) : null}
              </span>
            </div>
          ))}
        </div>
      )}
    </div>
  )
}

// ---------------------------------------------------------------------------
// 批次详情（R02/R04/R06）
// ---------------------------------------------------------------------------

type BatchEvent = {
  id: number
  eventType: string
  sequence: number
  detail?: unknown
  createdAt: string
}

export function BatchDetailPage() {
  const scope = useProjectScope()
  const params = useParams()
  const navigate = useNavigate()
  const { Title, Text } = Typography
  const batchId = params.batchId ?? ''
  const [detail, setDetail] = useState<BatchDetail | null>(null)
  const [capabilities, setCapabilities] = useState<BatchCapabilities>({
    canPause: false,
    canResume: false,
    canRetryFailed: false,
  })
  const [events, setEvents] = useState<BatchEvent[]>([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)
  const [actionError, setActionError] = useState<string | null>(null)
  const [busy, setBusy] = useState(false)

  const load = useCallback(async () => {
    setLoading(true)
    setError(null)
    try {
      const [detailResponse, eventsResponse] = await Promise.all([
        studioApi.getBatchEnvelope(scope.projectId, batchId),
        client.get<Page<BatchEvent>>(
          `${projectPath(scope.projectId)}/batches/${batchId}/events?limit=30`,
        ),
      ])
      setDetail(detailResponse.data)
      setCapabilities(detailResponse.capabilities)
      setEvents(eventsResponse.data.items ?? [])
    } catch (loadError) {
      setError(loadError instanceof Error ? loadError.message : '加载批次详情失败')
    } finally {
      setLoading(false)
    }
  }, [batchId, scope.projectId])

  useEffect(() => {
    void load()
  }, [load])

  const control = useCallback(
    async (action: 'pause' | 'resume' | 'retry-failed') => {
      setBusy(true)
      setActionError(null)
      try {
        await client.post(`${projectPath(scope.projectId)}/batches/${batchId}/${action}`)
        await load()
      } catch (controlError) {
        setActionError(controlError instanceof Error ? controlError.message : '操作失败')
      } finally {
        setBusy(false)
      }
    },
    [batchId, load, scope.projectId],
  )

  const status = detail?.batch.status ?? ''
  const isRunning = status === 'running' || status === 'queued'
  const isPaused = status === 'paused' || status === 'pause_requested'

  if (loading) {
    return (
      <div className="flex justify-center py-10">
        <Spin tip="正在加载批次" />
      </div>
    )
  }
  if (error || !detail) {
    return (
      <Card className="console-card">
        <Text strong className="block">
          批次加载失败
        </Text>
        <Text type="tertiary">{error ?? '未知错误'}</Text>
        <div className="mt-3">
          <Button size="small" onClick={() => void load()}>
            重试
          </Button>
        </div>
      </Card>
    )
  }

  return (
    <div className="console-page" data-studio-page="batch-detail" data-batch-status={status}>
      <div className="console-page__header">
        <div>
          <Title heading={4} className="!mb-1">
            批次 {detail.batch.resourceId}（{detail.batch.purpose === 'pilot' ? '试制' : '扩量'}）
          </Title>
          <Text type="tertiary">
            {STATUS_LABEL[status] ?? status} · 计划 {detail.batch.plannedUnits} · 完成{' '}
            {detail.batch.completedUnits} · 失败 {detail.batch.failedUnits} · 在途{' '}
            {detail.batch.inFlightUnits}
          </Text>
        </div>
        <div className="flex gap-2">
          {capabilities.canPause && isRunning ? (
            <Button
              icon={<Pause size={14} />}
              loading={busy}
              onClick={() => void control('pause')}
            >
              暂停
            </Button>
          ) : null}
          {capabilities.canResume && isPaused ? (
            <Button icon={<Play size={14} />} loading={busy} onClick={() => void control('resume')}>
              继续
            </Button>
          ) : null}
          {capabilities.canRetryFailed && detail.batch.failedUnits > 0 ? (
            <Button
              icon={<RotateCcw size={14} />}
              loading={busy}
              onClick={() => void control('retry-failed')}
            >
              恢复失败项
            </Button>
          ) : null}
          <Button onClick={() => navigate(projectHref('project.runFailures', scope.projectId, { batchId }))}>
            异常恢复
          </Button>
        </div>
      </div>

      {actionError ? (
        <div className="wizard-field__error mb-3" role="alert">
          {actionError}
        </div>
      ) : null}

      {/* 暂停语义必须显式说明：用户点完暂停会以为不再花钱（§2.4）。 */}
      {isPaused ? (
        <Card className="console-card mb-3" bodyStyle={{ padding: 14 }} data-pause-notice="true">
          <div className="flex items-start gap-2">
            <AlertTriangle size={16} className="mt-1 text-amber-500" aria-hidden />
            <div>
              <Text strong className="block">
                已停止提交新请求
              </Text>
              <Text type="tertiary" size="small">
                已在途的 {detail.batch.inFlightUnits} 个请求仍会完成并计费；恢复只提交失败与未完成项。
              </Text>
            </div>
          </div>
        </Card>
      ) : null}

      {/* 完成后的下一步提示：T13 验收项「批次完成后提示质量实验或比较」。 */}
      {status === 'completed' ? (
        <Card className="console-card mb-3" bodyStyle={{ padding: 14 }} data-next-step="true">
          <Text strong className="block">
            本批已完成
          </Text>
          <Text type="tertiary" size="small">
            下一步可以创建质量实验（固定范围与量表）或与另一个试制批次做同基准比较。
          </Text>
          <div className="mt-3 flex gap-2">
            <Button size="small" onClick={() => navigate(projectHref('project.qualityNew', scope.projectId))}>
              创建质量实验
            </Button>
            <Button size="small" onClick={() => navigate(projectHref('project.compare', scope.projectId))}>
              与试制比较
            </Button>
          </div>
        </Card>
      ) : null}

      <div className="batch-detail-grid">
        <Card className="console-card" bodyStyle={{ padding: 16 }}>
          <Text strong className="block mb-2">
            配置快照（本批实际使用的配置）
          </Text>
          <ul className="batch-snapshot">
            <li>
              输出内容类型：<code>{detail.generationConfig.schemaVersion || '（未设置）'}</code>
            </li>
            <li>
              模型连接：<code>{detail.generationConfig.modelConnectionId || '（未设置）'}</code>
              （非秘密标识，凭证每次现取）
            </li>
            <li>
              并发 / 输出上限：{detail.generationConfig.concurrency} /{' '}
              {detail.generationConfig.maxTokens}
            </li>
            <li>
              蓝图内容指纹：<code>{detail.snapshot.blueprintContentHash.slice(0, 12) || '（未引用）'}</code>
            </li>
            <li>
              标准内容指纹：<code>{detail.snapshot.standardContentHash.slice(0, 12) || '（未引用）'}</code>
            </li>
          </ul>
          <div className="mt-2 flex items-center gap-2">
            <ArrowLeftRight size={14} aria-hidden />
            <Text type="tertiary" size="small" data-snapshot-immutable="true">
              快照不可就地修改：改模型或标准必须复制为新批次，否则已有成功内容无法复现。
            </Text>
          </div>
        </Card>

        <Card className="console-card" bodyStyle={{ padding: 16 }}>
          <Text strong className="block mb-2">
            阶段进度（按单位，不编造总体百分比）
          </Text>
          {detail.steps.length === 0 ? (
            <Text type="tertiary" size="small">
              还没有阶段记录。
            </Text>
          ) : (
            <ul className="batch-steps">
              {detail.steps.map((step) => (
                <li key={step.id}>
                  <span>{step.unitLabel || step.phase}</span>
                  <span>
                    {step.doneUnits} / {step.totalUnits}
                    {step.failedUnits > 0 ? `（失败 ${step.failedUnits}）` : ''}
                  </span>
                </li>
              ))}
            </ul>
          )}
          <Text type="tertiary" size="small" className="block mt-2">
            批次预算：上限 {detail.batchBudget.limitMinor} 分 · 在途{' '}
            {detail.batchBudget.reservedMinor} · 已结算 {detail.batchBudget.settledMinor} · 未知{' '}
            {detail.batchBudget.uncertainMinor}
          </Text>
        </Card>
      </div>

      <Card className="console-card mt-3" bodyStyle={{ padding: 16 }}>
        <Text strong className="block mb-2">
          事件时间线
        </Text>
        {events.length === 0 ? (
          <Text type="tertiary" size="small">
            还没有事件。
          </Text>
        ) : (
          <ul className="batch-events">
            {events.map((event) => (
              <li key={event.id}>
                <span className="batch-events__type">{event.eventType}</span>
                <span className="batch-events__time">{event.createdAt}</span>
              </li>
            ))}
          </ul>
        )}
      </Card>
    </div>
  )
}

// ---------------------------------------------------------------------------
// 异常恢复（R03）
// ---------------------------------------------------------------------------

export function FailuresPage() {
  const scope = useProjectScope()
  const params = useParams()
  const { Title, Text } = Typography
  const batchId = params.batchId ?? ''
  const [failures, setFailures] = useState<BatchFailure[]>([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)
  const [resetItems, setResetItems] = useState<number | null>(null)
  const [busy, setBusy] = useState(false)
  const [canRetryFailed, setCanRetryFailed] = useState(false)

  const load = useCallback(async () => {
    setLoading(true)
    setError(null)
    try {
      const [response, batch] = await Promise.all([
        client.get<Page<BatchFailure>>(`${projectPath(scope.projectId)}/batches/${batchId}/failures?limit=50`),
        studioApi.getBatchEnvelope(scope.projectId, batchId),
      ])
      setFailures(response.data.items ?? [])
      setCanRetryFailed(batch.capabilities.canRetryFailed === true)
    } catch (loadError) {
      setError(loadError instanceof Error ? loadError.message : '加载失败项失败')
    } finally {
      setLoading(false)
    }
  }, [batchId, scope.projectId])

  useEffect(() => {
    void load()
  }, [load])

  const retry = useCallback(async () => {
    setBusy(true)
    try {
      const response = await client.post<{ data?: { resetItems?: number } }>(
        `${projectPath(scope.projectId)}/batches/${batchId}/retry-failed`,
      )
      const reset = (response.data as { data?: { resetItems?: number } }).data?.resetItems ?? 0
      setResetItems(reset)
      await load()
    } catch (retryError) {
      setError(retryError instanceof Error ? retryError.message : '恢复失败')
    } finally {
      setBusy(false)
    }
  }, [batchId, load, scope.projectId])

  return (
    <div className="console-page" data-studio-page="failures">
      <div className="console-page__header">
        <div>
          <Title heading={4} className="!mb-1">
            异常恢复
          </Title>
          <Text type="tertiary">
            只恢复可重试的失败单元；成功内容保留，因此反复点击不会重复产出。
          </Text>
        </div>
        {canRetryFailed ? (
          <Button icon={<RotateCcw size={14} />} loading={busy} onClick={() => void retry()}>
            恢复失败项
          </Button>
        ) : null}
      </div>

      {/* 「恢复了 0 项」与「点了没反应」必须能区分。 */}
      {resetItems !== null ? (
        <Card className="console-card mb-3" bodyStyle={{ padding: 12 }} data-reset-count={resetItems}>
          <Text size="small">
            已重置 {resetItems} 个可重试单元
            {resetItems === 0 ? '：当前没有可重试的失败项（不可重试的错误需要先修配置或新建批次）' : ''}
          </Text>
        </Card>
      ) : null}

      {loading ? (
        <div className="flex justify-center py-10">
          <Spin tip="正在加载失败项" />
        </div>
      ) : error ? (
        <Card className="console-card">
          <Text strong className="block">
            加载失败
          </Text>
          <Text type="tertiary">{error}</Text>
        </Card>
      ) : failures.length === 0 ? (
        <Card className="console-card">
          <Empty description="没有失败单元。" />
        </Card>
      ) : (
        <div className="failure-list">
          {failures.map((failure) => (
            <Card key={failure.itemId} className="console-card" bodyStyle={{ padding: 14 }}>
              <div className="flex items-start justify-between gap-2">
                <Text strong>{failure.itemKey}</Text>
                <Tag size="small" color={failure.retryable ? 'orange' : 'red'}>
                  {failure.retryable ? '可重试' : '不可自动重试'}
                </Tag>
              </div>
              <Text type="tertiary" size="small" className="block mt-1">
                错误类别：{failure.errorClass} · 尝试 {failure.attempts} 次
              </Text>
              {/* 可操作建议：只显示机器码会让用户不知道下一步做什么。 */}
              <Text size="small" className="block mt-1">
                建议：{failure.suggestedAction}
              </Text>
              <Text type="tertiary" size="small" className="block mt-1">
                原始信息：{failure.errorMessage}
              </Text>
            </Card>
          ))}
        </div>
      )}
    </div>
  )
}

// ---------------------------------------------------------------------------
// 试制与扩量规划（P05 / R05）
// ---------------------------------------------------------------------------

const MAX_PILOT_UNITS = 100
const MAX_SCALE_UNITS = 100000

type PlanningVersion = { id: number; version: number; changeReason?: string }
type PlanningVersionList = { items?: PlanningVersion[] }

type BlueprintPlanningPayload = {
  nodes?: {
    coverage?: { coverageVersionId?: number }
    standard?: { standardVersionId?: number }
    generation?: {
      modelConnectionId?: number
      schemaVersion?: string
      concurrency?: number
      maxTokens?: number
    }
    evaluation?: { judgeConnectionIds?: number[]; rubricVersionId?: number }
    rules?: { qualityPolicyVersionId?: number }
    humanReview?: { assignment?: string; requiredEvidence?: string[] }
    delivery?: { mappingVersionId?: number; format?: string; intendedUse?: string }
  }
}

type PlanningBlueprintVersion = PlanningVersion & { payload?: BlueprintPlanningPayload }

const BLUEPRINT_EXECUTION_REQUIREMENTS = [
  '模型服务、输出内容类型、并发和单次输出上限',
  '独立检查模型与量表',
  '质量策略、交付映射、输出格式和用途',
  '人工检查方式和必需依据',
]

function planningVersionLabel(items: PlanningVersion[], id: string): string {
  const version = items.find((item) => String(item.id) === id)
  return version ? `v${version.version}` : '未选择'
}

export function BatchPlanningPage({ purpose }: { purpose: 'pilot' | 'scale' }) {
  const scope = useProjectScope()
  const navigate = useNavigate()
  const [searchParams] = useSearchParams()
  const { Title, Text } = Typography

  /**
   * `?slice=` 来自覆盖矩阵的缺口链接：把「哪个方向缺内容」带进规划页，
   * 而不是让用户自己回忆（T11 验收项「覆盖缺口点击带切片进入下一批规划」）。
   * 它只影响提示文案与 coverageSlice 的初值，**不触发**任何重生成。
   */
  const slice = searchParams.get('slice') ?? ''
  const baselineID = Number(searchParams.get('baselineId') ?? 0)
  const fromBatchID = Number(searchParams.get('fromBatchId') ?? 0)

  const [unitCount, setUnitCount] = useState(purpose === 'pilot' ? '12' : '500')
  const [budgetLimitMinor, setBudgetLimitMinor] = useState('')
  const [blueprintVersionId, setBlueprintVersionId] = useState('')
  const [coverageVersionId, setCoverageVersionId] = useState('')
  const [standardVersionId, setStandardVersionId] = useState('')
  const [qualityPolicyVersionId, setQualityPolicyVersionId] = useState('')
  const [mappingVersionId, setMappingVersionId] = useState('')
  const [blueprintPayload, setBlueprintPayload] = useState<BlueprintPlanningPayload | null>(null)
  const [blueprintPayloadError, setBlueprintPayloadError] = useState<string | null>(null)
  const [versionOptions, setVersionOptions] = useState({
    blueprint: [] as PlanningVersion[],
    coverage: [] as PlanningVersion[],
    standard: [] as PlanningVersion[],
    qualityPolicy: [] as PlanningVersion[],
    mapping: [] as PlanningVersion[],
  })
  /**
   * 采用指针只保存批次/基准 ID；版本快照必须再从批次详情读取。
   * 不把版本 ID 放在 URL 或 localStorage，避免用户改 URL 后把另一套配置
   * 伪装成比较采用的方案。五类版本都是该批次真正执行时冻结的值。
   */
  const [adoptedBatch, setAdoptedBatch] = useState<(AdoptedBatch & { snapshot: BatchSnapshot }) | null>(null)
  const [adoptionNotice, setAdoptionNotice] = useState<string | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [submitting, setSubmitting] = useState(false)
  const [canRun, setCanRun] = useState(false)
  const selectedBlueprint = versionOptions.blueprint.find((version) => String(version.id) === blueprintVersionId)
  // A retry after a network timeout must replay the same command. Generating
  // the key inside submit would turn an uncertain retry into a second paid run.
  const idempotencyKeyRef = useRef(newIdempotencyKey())

  useEffect(() => {
    let cancelled = false
    void Promise.all([
      studioApi.overviewEnvelope(scope.projectId),
      client.get<PlanningVersionList>(`${projectPath(scope.projectId)}/blueprint-versions?limit=50`),
      client.get<PlanningVersionList>(`${projectPath(scope.projectId)}/coverage-versions?limit=50`),
      client.get<PlanningVersionList>(`${projectPath(scope.projectId)}/standard-versions?limit=50`),
      client.get<PlanningVersionList>(`${projectPath(scope.projectId)}/quality-policy-versions?limit=50`),
      client.get<PlanningVersionList>(`${projectPath(scope.projectId)}/mapping-versions?limit=50`),
    ]).then(([overview, blueprint, coverage, standard, qualityPolicy, mapping]) => {
      if (cancelled) return
      setCanRun(overview.capabilities.canRun === true)
      const next = {
        blueprint: blueprint.data.items ?? [],
        coverage: coverage.data.items ?? [],
        standard: standard.data.items ?? [],
        qualityPolicy: qualityPolicy.data.items ?? [],
        mapping: mapping.data.items ?? [],
      }
      setVersionOptions(next)
      const latestID = (items: PlanningVersion[]) => items[0] && String(items[0].id)
      setBlueprintVersionId((value) => value || latestID(next.blueprint) || '')
      setCoverageVersionId((value) => value || latestID(next.coverage) || '')
      setStandardVersionId((value) => value || latestID(next.standard) || '')
      setQualityPolicyVersionId((value) => value || latestID(next.qualityPolicy) || '')
      setMappingVersionId((value) => value || latestID(next.mapping) || '')
    }).catch(() => {
      if (!cancelled) setCanRun(false)
    })
    return () => { cancelled = true }
  }, [scope.projectId])

  // 选择蓝图不应要求用户再逐项复述一次已经在蓝图中保存的引用。读取版本
  // 本体后，把其中的覆盖、标准、规则和映射带入本次快照；用户仍可以在下面
  // 显式改为另一版，以支持同一生成方案的对照试制。
  useEffect(() => {
    if (!blueprintVersionId || !selectedBlueprint?.version) {
      setBlueprintPayload(null)
      setBlueprintPayloadError(null)
      return
    }
    let cancelled = false
    setBlueprintPayloadError(null)
    void client.get<{ data?: PlanningBlueprintVersion; version?: PlanningBlueprintVersion; payload?: BlueprintPlanningPayload }>(
      `${projectPath(scope.projectId)}/blueprint-versions/${selectedBlueprint.version}`,
    ).then((response) => {
      if (cancelled) return
      const body = response.data
      const version = body?.data
        ?? (body?.version && typeof body.version === 'object'
          ? body.version
          : body as unknown as PlanningBlueprintVersion)
      const payload = version.payload ?? {}
      setBlueprintPayload(payload)
      const nodes = payload.nodes
      const setFromBlueprint = (value: number | undefined, setter: (next: string) => void) => {
        // 清掉上一个蓝图留下的自动默认值；否则选择一个尚未配置映射的
        // 蓝图，会悄悄沿用项目最新映射，形成不可见的跨版本引用。
        setter(value && value > 0 ? String(value) : '')
      }
      setFromBlueprint(nodes?.coverage?.coverageVersionId, setCoverageVersionId)
      setFromBlueprint(nodes?.standard?.standardVersionId, setStandardVersionId)
      setFromBlueprint(nodes?.rules?.qualityPolicyVersionId, setQualityPolicyVersionId)
      setFromBlueprint(nodes?.delivery?.mappingVersionId, setMappingVersionId)
    }).catch((loadError) => {
      if (cancelled) return
      setBlueprintPayload(null)
      setBlueprintPayloadError(loadError instanceof Error ? loadError.message : '无法读取所选蓝图的配置')
    })
    return () => { cancelled = true }
  }, [blueprintVersionId, scope.projectId, selectedBlueprint?.version])

  const maxUnits = purpose === 'pilot' ? MAX_PILOT_UNITS : MAX_SCALE_UNITS
  const title = purpose === 'pilot' ? '小批试制' : '扩量规划'

  // 采用比较方案后，扩量规划页必须恢复服务端保存的采用指针。
  // URL 只携带比较/来源上下文；版本快照仍由服务端返回，避免客户端伪造配置。
  useEffect(() => {
    if (purpose !== 'scale' || (baselineID <= 0 && fromBatchID <= 0)) return
    let cancelled = false
    void studioApi.adoptedBatch(scope.projectId).then(async (adopted) => {
      if (cancelled || !adopted.adopted || !adopted.batchId) return
      if (fromBatchID > 0 && adopted.batchId !== fromBatchID) return

      // `/adopted-batch` 是服务端持久化的采用指针，批次详情则是唯一可信的
      // 五类版本快照来源。先读指针再读详情，不能让客户端通过 query 参数
      // 自己拼 blueprint/coverage/standard/quality/mapping 版本。
      const detail = await studioApi.getBatch(scope.projectId, `b_${adopted.batchId}`)
      if (cancelled) return
      setAdoptedBatch({ ...adopted, snapshot: detail.snapshot })
      const snapshot = detail.snapshot
      const formatVersion = (value: number | undefined): string =>
        value && value > 0 ? `#${value}` : '未引用'
      setBlueprintVersionId(snapshot.blueprintVersionId && snapshot.blueprintVersionId > 0 ? String(snapshot.blueprintVersionId) : '')
      setCoverageVersionId(snapshot.coverageVersionId && snapshot.coverageVersionId > 0 ? String(snapshot.coverageVersionId) : '')
      setStandardVersionId(snapshot.standardVersionId && snapshot.standardVersionId > 0 ? String(snapshot.standardVersionId) : '')
      setQualityPolicyVersionId(snapshot.qualityPolicyVersionId && snapshot.qualityPolicyVersionId > 0 ? String(snapshot.qualityPolicyVersionId) : '')
      setMappingVersionId(snapshot.mappingVersionId && snapshot.mappingVersionId > 0 ? String(snapshot.mappingVersionId) : '')
      setAdoptionNotice(
        `已带入比较基准 #${adopted.baselineId ?? baselineID} 采用的批次 ${adopted.batchId ?? fromBatchID}；已恢复蓝图 ${formatVersion(snapshot.blueprintVersionId)}、覆盖 ${formatVersion(snapshot.coverageVersionId)}、标准 ${formatVersion(snapshot.standardVersionId)}、质量策略 ${formatVersion(snapshot.qualityPolicyVersionId)}、映射 ${formatVersion(snapshot.mappingVersionId)}。请补充本次扩量范围与预算。`,
      )
    }).catch(() => {
      if (!cancelled) setAdoptionNotice('比较采用记录暂时无法读取，请确认版本快照后再提交扩量。')
    })
    return () => { cancelled = true }
  }, [baselineID, fromBatchID, purpose, scope.projectId])

  /**
   * 执行前核对（T13 要求「扩量：执行前核对」）。
   *
   * 逐项列出将会发生什么，而不是一个笼统的「确认」：扩量会真实产生费用，
   * 让用户看到「计划单元数 / 预算上限 / 是否只跑某切片」比一句
   * 「确定要执行吗」更能防止误操作。
   */
  const checklist = useMemo(() => {
    const units = Number(unitCount)
    const items: Array<{ label: string; ok: boolean; detail: string }> = []
    items.push({
      label: '计划单元数在范围内',
      ok: Number.isInteger(units) && units >= 1 && units <= maxUnits,
      detail: `1–${maxUnits}`,
    })
    items.push({
      label: '已选择蓝图版本',
      ok: blueprintVersionId.trim() !== '',
      detail: blueprintVersionId.trim() === '' ? '缺少蓝图版本，批次无法固定执行配置' : `${planningVersionLabel(versionOptions.blueprint, blueprintVersionId)} 已固定`,
    })
    const blueprintNodes = blueprintPayload?.nodes
    items.push({
      label: '覆盖范围已固定',
      ok: coverageVersionId.trim() !== '',
      detail: coverageVersionId.trim() === ''
        ? '请先在“覆盖范围”配置领域和方向，再回到这里。'
        : `${planningVersionLabel(versionOptions.coverage, coverageVersionId)}${blueprintNodes?.coverage?.coverageVersionId ? '（来自蓝图）' : '（本次选择）'}`,
    })
    items.push({
      label: '思维标准已固定',
      ok: standardVersionId.trim() !== '',
      detail: standardVersionId.trim() === ''
        ? '请先在“思维标准”写清步骤和检查点，再回到这里。'
        : `${planningVersionLabel(versionOptions.standard, standardVersionId)}${blueprintNodes?.standard?.standardVersionId ? '（来自蓝图）' : '（本次选择）'}`,
    })
    items.push({
      label: '生成设置已检查',
      ok: Boolean(blueprintNodes?.generation?.modelConnectionId && blueprintNodes.generation.schemaVersion && blueprintNodes.generation.concurrency && blueprintNodes.generation.maxTokens),
      detail: !blueprintPayload
        ? '正在读取蓝图设置。'
        : '模型服务、输出类型、并发和单次输出上限都必须在蓝图中设置。',
    })
    items.push({
      label: '独立评估已设置',
      ok: Boolean(blueprintNodes?.evaluation?.judgeConnectionIds?.length && blueprintNodes.evaluation.rubricVersionId),
      detail: Boolean(blueprintNodes?.evaluation?.judgeConnectionIds?.length && blueprintNodes.evaluation.rubricVersionId)
        ? '已有独立检查模型与量表。'
        : '请在蓝图的“独立评估”步骤选择检查模型和量表。',
    })
    items.push({
      label: '规则与交付已设置',
      ok: Boolean(blueprintNodes?.rules?.qualityPolicyVersionId && blueprintNodes?.delivery?.mappingVersionId && blueprintNodes.delivery.format && blueprintNodes.delivery.intendedUse),
      detail: Boolean(blueprintNodes?.rules?.qualityPolicyVersionId && blueprintNodes?.delivery?.mappingVersionId && blueprintNodes.delivery.format && blueprintNodes.delivery.intendedUse)
        ? '质量策略、映射、格式和用途均已固定。'
        : '请补齐蓝图的规则检查和版本交付步骤。',
    })
    items.push({
      label: '人工检查点已设置',
      ok: Boolean(blueprintNodes?.humanReview?.assignment && blueprintNodes.humanReview.requiredEvidence?.length),
      detail: Boolean(blueprintNodes?.humanReview?.assignment && blueprintNodes.humanReview.requiredEvidence?.length)
        ? '人工检查方式和必需依据已明确。'
        : '请在蓝图的“人工检查点”写清分派方式和必需依据。',
    })
    items.push({
      label: '预算上限合法',
      ok:
        budgetLimitMinor.trim() === '' ||
        (Number.isInteger(Number(budgetLimitMinor)) && Number(budgetLimitMinor) >= 0 &&
          (Number(budgetLimitMinor) === 0 || Number(budgetLimitMinor) >= 100)),
      detail: '留空或 0 表示不设上限；非 0 至少 100 分',
    })
    items.push({
      label: '试制不覆盖生产',
      ok: true,
      detail: purpose === 'pilot' ? '试制是独立批次，不会改动已有生产批次的内容' : '扩量是新的独立批次',
    })
    return items
  }, [blueprintPayload, blueprintVersionId, budgetLimitMinor, coverageVersionId, maxUnits, purpose, standardVersionId, unitCount, versionOptions.blueprint, versionOptions.coverage, versionOptions.standard])

  const ready = checklist.every((item) => item.ok)

  const submit = useCallback(async () => {
    if (!canRun) {
      setError('当前项目没有运行权限；请联系项目负责人')
      return
    }
    if (!ready) {
      setError('请先修正核对项中的问题')
      return
    }
    if (!blueprintPayload?.nodes?.generation?.modelConnectionId) {
      setError('所选蓝图还没有模型服务，请先打开设计区的“生成”步骤完成配置。')
      return
    }
    setSubmitting(true)
    setError(null)
    const payload: CreateBatchRequest = {
      purpose,
      blueprintVersionId: Number(blueprintVersionId),
      coverageVersionId: coverageVersionId.trim() === '' ? 0 : Number(coverageVersionId),
      standardVersionId: standardVersionId.trim() === '' ? 0 : Number(standardVersionId),
      qualityPolicyVersionId: qualityPolicyVersionId.trim() === '' ? 0 : Number(qualityPolicyVersionId),
      mappingVersionId: mappingVersionId.trim() === '' ? 0 : Number(mappingVersionId),
      unitCount: Number(unitCount),
      budget: { currency: 'CNY' },
    }
    const limit = budgetLimitMinor.trim()
    if (limit !== '') {
      payload.budget.limitMinor = Number(limit)
    }
    if (slice !== '') {
      payload.coverageSlice = { directionStableId: slice }
    }
    try {
      const response = await client.post<{ data?: { batchId?: number } }>(
        `${projectPath(scope.projectId)}/batches`,
        payload,
        // 幂等键在本次提交内稳定：双击不会建出两个批次。
        { headers: { 'Idempotency-Key': idempotencyKeyRef.current } },
      )
      // 导航到**服务端分配**的批次 ID（不从本地状态拼，也不硬编码原型里的示例 ID）。
      const created = (response.data as { data?: { batchId?: number } }).data
      const batchId = created?.batchId
      if (!batchId) {
        throw new Error('创建成功但没有返回批次 ID，请联系管理员并提供 requestId')
      }
      navigate(projectHref('project.runDetail', scope.projectId, { batchId: `b_${batchId}` }))
    } catch (submitError) {
      setError(submitError instanceof Error ? submitError.message : '创建批次失败')
    } finally {
      setSubmitting(false)
    }
  }, [
    canRun,
    blueprintVersionId,
    budgetLimitMinor,
    coverageVersionId,
    mappingVersionId,
    navigate,
    purpose,
    ready,
    blueprintPayload,
    qualityPolicyVersionId,
    scope.projectId,
    slice,
    standardVersionId,
    unitCount,
  ])

  return (
    <div className="console-page" data-studio-page={purpose === 'pilot' ? 'pilot' : 'scale-planning'}>
      <div className="console-page__header">
        <div>
          <Title heading={4} className="!mb-1">
            {title}
          </Title>
          <Text type="tertiary">
            {purpose === 'pilot'
              ? '先用小批验证方案；结果不会覆盖主生产。蓝图里未完成的步骤会在执行前明确拦截。'
              : '按范围与预算规划一次扩量；执行前会核对蓝图中的每个配置步骤。'}
          </Text>
        </div>
      </div>

      {slice !== '' ? (
        <Card className="console-card mb-3" bodyStyle={{ padding: 12 }} data-slice-context={slice}>
          <Text size="small">
            来自覆盖矩阵的缺口方向：<code>{slice}</code>。本次规划只针对该切片，
            不会触发任何已有内容的重生成。
          </Text>
        </Card>
      ) : null}

      {adoptionNotice ? (
        <Card className="console-card mb-3" bodyStyle={{ padding: 12 }} data-adoption-context="true">
          <Text size="small">{adoptionNotice}</Text>
          {adoptedBatch?.side ? (
            <Text type="tertiary" size="small" className="block mt-1">
              采用侧：{adoptedBatch.side === 'left' ? '左侧方案' : '右侧方案'}；本页只创建新的扩量批次，不会修改原试制批次。
            </Text>
          ) : null}
        </Card>
      ) : null}

      {blueprintPayloadError ? (
        <div className="wizard-field__error mb-3" role="alert">无法带入蓝图配置：{blueprintPayloadError}</div>
      ) : null}

      <Card className="console-card" bodyStyle={{ padding: 20 }}>
        <div className="planning-configuration-note" role="note">
          <strong>这次批次会固定蓝图里的配置</strong>
          <span>蓝图负责定义生成、评估、规则、人工检查和交付边界；本页只决定本次范围、数量和预算。</span>
          <span>执行前需要完成：{BLUEPRINT_EXECUTION_REQUIREMENTS.join('；')}。</span>
        </div>
        <div className="wizard-fields">
          <Field label="生产蓝图版本" required fieldId="plan-blueprint" action={<Button size="small" theme="borderless" onClick={() => navigate(scope.href('project.blueprint'))}>编辑蓝图</Button>}>
            <Select
              id="plan-blueprint"
              value={blueprintVersionId}
              optionList={versionOptions.blueprint.map((version) => ({ value: String(version.id), label: `v${version.version} · ${version.changeReason || '未填写理由'}` }))}
              onChange={(value) => setBlueprintVersionId(String(value))}
              placeholder="选择蓝图版本"
            />
            {selectedBlueprint ? <Text type="tertiary" size="small">当前方案：v{selectedBlueprint.version}。选择后会自动带入蓝图中保存的覆盖、标准、规则与交付引用。</Text> : null}
          </Field>
          <Field label="覆盖范围版本" required fieldId="plan-coverage" action={<Button size="small" theme="borderless" onClick={() => navigate(scope.href('project.coverage'))}>编辑覆盖范围</Button>}>
            <Select
              id="plan-coverage"
              value={coverageVersionId || undefined}
              optionList={versionOptions.coverage.map((version) => ({ value: String(version.id), label: `v${version.version} · ${version.changeReason || '未填写理由'}` }))}
              onChange={(value) => setCoverageVersionId(String(value))}
              placeholder="选择覆盖版本"
            />
            <Text type="tertiary" size="small">蓝图已配置时会自动带入；这里改动只影响本次批次。</Text>
          </Field>
          <Field label="思维标准版本" required fieldId="plan-standard" action={<Button size="small" theme="borderless" onClick={() => navigate(scope.href('project.standard'))}>编辑思维标准</Button>}>
            <Select
              id="plan-standard"
              value={standardVersionId || undefined}
              optionList={versionOptions.standard.map((version) => ({ value: String(version.id), label: `v${version.version} · ${version.changeReason || '未填写理由'}` }))}
              onChange={(value) => setStandardVersionId(String(value))}
              placeholder="选择标准版本"
            />
            <Text type="tertiary" size="small">步骤和检查点来自这里的固定版本，已运行批次不会被修改。</Text>
          </Field>
          <Field label="质量策略版本" fieldId="plan-quality-policy" action={<Button size="small" theme="borderless" onClick={() => navigate(scope.href('project.rules'))}>编辑规则策略</Button>}>
            <Select
              id="plan-quality-policy"
              value={qualityPolicyVersionId || undefined}
              optionList={versionOptions.qualityPolicy.map((version) => ({ value: String(version.id), label: `v${version.version} · ${version.changeReason || '未填写理由'}` }))}
              onChange={(value) => setQualityPolicyVersionId(String(value))}
              placeholder="质量策略会在规则检查时使用"
            />
          </Field>
          <Field label="交付映射版本" fieldId="plan-mapping" action={<Button size="small" theme="borderless" onClick={() => navigate(scope.href('project.newRelease'))}>编辑交付映射</Button>}>
            <Select
              id="plan-mapping"
              value={mappingVersionId || undefined}
              optionList={versionOptions.mapping.map((version) => ({ value: String(version.id), label: `v${version.version} · ${version.changeReason || '未填写理由'}` }))}
              onChange={(value) => setMappingVersionId(String(value))}
              placeholder="交付映射会在发布时使用"
            />
          </Field>
          <Field label={`计划单元数（1–${maxUnits}）`} required fieldId="plan-units">
            <InputNumber
              id="plan-units"
              value={Number(unitCount) || undefined}
              min={1}
              max={maxUnits}
              onChange={(value) => setUnitCount(value === undefined ? '' : String(value))}
            />
          </Field>
          <Field label="预算上限（分）" fieldId="plan-budget">
            <Input
              id="plan-budget"
              value={budgetLimitMinor}
              onChange={(value) => setBudgetLimitMinor(value)}
              placeholder="留空或 0 = 不设上限"
            />
          </Field>
        </div>
      </Card>

      <Card className="console-card mt-3" bodyStyle={{ padding: 16 }} data-preflight="true">
        <Text strong className="block mb-2">
          执行前核对
        </Text>
        <ul className="preflight-list">
          {checklist.map((item) => (
            <li key={item.label} data-preflight-ok={item.ok ? 'true' : 'false'}>
              <span className={item.ok ? 'preflight-ok' : 'preflight-block'}>
                {item.ok ? '✓' : '✗'}
              </span>
              <span>{item.label}</span>
              <span className="preflight-detail">{item.detail}</span>
            </li>
          ))}
        </ul>
      </Card>

      {error ? (
        <div className="wizard-field__error mt-3" role="alert">
          {error}
        </div>
      ) : null}

      <div className="mt-3">
        <Button
          theme="solid"
          type="primary"
          disabled={!ready || !canRun}
          loading={submitting}
          onClick={() => void submit()}
        >
          {purpose === 'pilot' ? '启动试制批次' : '启动扩量批次'}
        </Button>
      </div>
    </div>
  )
}

function Field({
  label,
  required,
  fieldId,
  action,
  children,
}: {
  label: string
  required?: boolean
  fieldId: string
  action?: React.ReactNode
  children: React.ReactNode
}) {
  return (
    <div className="wizard-field" data-field={fieldId}>
      <div className="planning-field__heading">
        <label className="wizard-field__label" htmlFor={fieldId}>
          {label}
          {required ? <span className="wizard-field__required"> *</span> : null}
        </label>
        {action}
      </div>
      {children}
    </div>
  )
}
