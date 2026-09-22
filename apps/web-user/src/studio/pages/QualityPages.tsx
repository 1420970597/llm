import { useCallback, useEffect, useRef, useState } from 'react'
import { useNavigate, useParams } from 'react-router-dom'
import { Button, Card, Empty, Input, InputNumber, Select, Spin, Tag, TextArea, Typography } from '@douyinfe/semi-ui'
// AlertTriangle 来自 lucide-react（图标库），不是 semi-ui 的组件。
import { AlertTriangle } from 'lucide-react'
import { client } from '../../lib/api'
import { newIdempotencyKey, projectPath, studioApi } from '../../lib/api/studio'
import type { BatchSummary, CreateExperimentRequest, Experiment, ExperimentDetail, Page, SampleSummary, RulePreviewResult } from '../../lib/api/studio'
import { useProjectScope } from '../ProjectLayout'

/**
 * 质量工作区页面（Issue #160 T19）：实验列表、创建页、报告页与规则页。
 *
 * 契约：docs/plans/atelier-api-contract.md §2.5、§3、§3.1。
 *
 * 三条 T19 验收项在实现上的落点：
 *
 *  1. **创建后排队页不提前显示最终分数**：创建返回 202 后进入报告页，
 *     而报告页在 `status` 仍为排队/运行时只显示「进行中」与已完成的计数，
 *     不显示均值 —— 提前显示一个基于部分样本的均值会被当成结论。
 *  2. **刷新/分享能恢复同一实验**：实验 ID 来自**路由参数**，页面所有数据由
 *     它取；不依赖内存里的 selectedRunId/tabKey。
 *  3. **风险计数与筛选列表一致**：分母/缺失/出错都来自报告的同一份统计，
 *     页面上不另算一套。
 */

const STATUS_LABEL: Record<string, string> = {
  queued: '排队中',
  running: '运行中',
  partial_failed: '部分失败（覆盖不完整）',
  completed: '已完成',
  failed: '失败',
}

function statusColor(status: string): 'amber' | 'green' | 'red' | 'grey' {
  switch (status) {
    case 'completed':
      return 'green'
    case 'partial_failed':
    case 'failed':
      return 'red'
    case 'running':
    case 'queued':
      return 'amber'
    default:
      return 'grey'
  }
}

/**
 * GRPO 内置量表的维度标签（T24）。
 *
 * 与后端 `model.GRPOQualityDimensions` 的 key 一一对应。未列出的 key 回退到
 * 原 key 而不是隐藏：隐藏会让「服务端加了维度而界面没跟上」表现为一行
 * 看不见的缺失，而报告的行数因此与分母统计对不上。
 */
const GRPO_DIMENSION_LABELS: Record<string, string> = {
  level_coverage: '档位覆盖（确定性：判据与裁判提示词是否覆盖每一档）',
  boundary_stability: '边界稳定性（按冻结参考集逐条判定）',
  explanation_consistency: '评分解释一致性',
}

function dimensionLabel(key: string): string {
  return GRPO_DIMENSION_LABELS[key] ?? key
}

// ---------------------------------------------------------------------------
// 实验列表（Q01）
// ---------------------------------------------------------------------------

export function QualityListPage() {
  const scope = useProjectScope()
  const navigate = useNavigate()
  const { Title, Text } = Typography
  const [experiments, setExperiments] = useState<Experiment[]>([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)
  const [canRun, setCanRun] = useState(false)

  const load = useCallback(async () => {
    setLoading(true)
    setError(null)
    try {
      const [response, overview] = await Promise.all([
        studioApi.listExperiments(scope.projectId),
        studioApi.overviewEnvelope(scope.projectId),
      ])
      setExperiments(response.items ?? [])
      setCanRun(overview.capabilities.canRun === true)
    } catch (loadError) {
      setError(loadError instanceof Error ? loadError.message : '加载实验失败')
    } finally {
      setLoading(false)
    }
  }, [scope.projectId])

  useEffect(() => {
    void load()
  }, [load])

  if (loading) {
    return (
      <div className="flex justify-center py-10">
        <Spin tip="正在加载实验" />
      </div>
    )
  }
  if (error) {
    return (
      <Card className="console-card">
        <Text strong className="block">
          加载失败
        </Text>
        <Text type="tertiary">{error}</Text>
      </Card>
    )
  }

  return (
    <div className="console-page" data-studio-page="quality-list">
      <div className="console-page__header">
        <div>
          <Title heading={4} className="!mb-1">
            质量实验室
          </Title>
          <Text type="tertiary">
            报告的分母是实验创建时**冻结**的样本版本数；待审阅不算接纳，隔离也不缩小分母。
          </Text>
        </div>
        <Button theme="solid" type="primary" disabled={!canRun} onClick={() => navigate(`/p/${scope.projectId}/quality/new`)}>
          新建质量实验
        </Button>
      </div>

      {experiments.length === 0 ? (
        <Card className="console-card">
          <Empty description="还没有质量实验。创建后会冻结本次检查的范围与量表。" />
        </Card>
      ) : (
        <div className="batch-table" data-experiment-table="true">
          <div className="batch-row batch-row--head">
            <span>实验</span>
            <span>状态</span>
            <span>分母</span>
            <span>已评分</span>
            <span>缺分</span>
            <span>出错</span>
            <span>操作</span>
          </div>
          {experiments.map((experiment) => (
            <div key={experiment.id} className="batch-row" data-experiment-id={experiment.id}>
              <span>#{experiment.id}</span>
              <span>
                <Tag size="small" color={statusColor(experiment.status)}>
                  {STATUS_LABEL[experiment.status] ?? experiment.status}
                </Tag>
              </span>
              <span data-count="inspected">{experiment.inspectedCount}</span>
              <span data-count="scored">{experiment.scoredCount}</span>
              <span data-count="missing">{experiment.missingCount}</span>
              <span data-count="error">{experiment.errorCount}</span>
              <span>
                <Button
                  size="small"
                  theme="solid"
                  type="primary"
                  onClick={() => navigate(`/p/${scope.projectId}/quality/${experiment.id}`)}
                >
                  查看报告
                </Button>
              </span>
            </div>
          ))}
        </div>
      )}
    </div>
  )
}

// ---------------------------------------------------------------------------
// 创建实验（Q02）
// ---------------------------------------------------------------------------

export function QualityNewPage() {
  const scope = useProjectScope()
  const navigate = useNavigate()
  const { Title, Text } = Typography

  const [batches, setBatches] = useState<BatchSummary[]>([])
  const [samples, setSamples] = useState<SampleSummary[]>([])
  const [batchID, setBatchID] = useState('')
  const [selected, setSelected] = useState<number[]>([])
  const [judgeID, setJudgeID] = useState('')
  const [seed, setSeed] = useState('42')
  const [teacherPromptVersion, setTeacherPromptVersion] = useState('')
  const [baselineAnswerVersion, setBaselineAnswerVersion] = useState('')
  const [boundaryReferenceJSON, setBoundaryReferenceJSON] = useState('')
  const [error, setError] = useState<string | null>(null)
  const [busy, setBusy] = useState(false)
  // 实验创建会冻结范围并入队，网络超时后的重试必须回放同一命令，
  // 不能因为重新点击而产生第二份实验/第二次裁判费用。
  const idempotencyKeyRef = useRef(newIdempotencyKey())
  // GRPO 与 SFT 的量表不同（T24）：GRPO 使用服务端内置量表，
  // 因此界面必须知道项目目标类型，而不是一律提交 SFT 的 accuracy 维度。
  const [targetKind, setTargetKind] = useState('sft')
  const [canRun, setCanRun] = useState(false)

  useEffect(() => {
    let cancelled = false
    void (async () => {
      try {
        const response = await studioApi.overviewEnvelope(scope.projectId)
        if (!cancelled) {
          setTargetKind(response.data.targetKind ?? 'sft')
          setCanRun(response.capabilities.canRun === true)
        }
      } catch {
        // 概览读取失败时回退到 SFT：SFT 路径要求**显式量表**，
        // 因此失败方向是「多填一个量表」而不是「用错量表」——
        // 后者会产出一份语义错误的质量结论，比多一次表单校验贵得多。
      }
    })()
    return () => {
      cancelled = true
    }
  }, [scope.projectId])
  const isGRPO = targetKind === 'grpo'

  useEffect(() => {
    let cancelled = false
    void (async () => {
      try {
        const [batchResponse, sampleResponse] = await Promise.all([
          client.get<Page<BatchSummary>>(`${projectPath(scope.projectId)}/batches?limit=50`),
          client.get<Page<SampleSummary>>(`${projectPath(scope.projectId)}/samples?status=all&limit=100`),
        ])
        if (cancelled) return
        setBatches(batchResponse.data.items ?? [])
        setSamples(sampleResponse.data.items ?? [])
      } catch (loadError) {
        if (!cancelled) setError(loadError instanceof Error ? loadError.message : '加载可选范围失败')
      }
    })()
    return () => {
      cancelled = true
    }
  }, [scope.projectId])

  const submit = useCallback(async () => {
    setError(null)
    if (!canRun) {
      setError('当前项目没有运行质量实验的权限；请联系项目负责人')
      return
    }
    if (selected.length === 0) {
      setError('实验范围不能为空：请至少选择一个样本版本（空范围的分母为 0，无法得出结论）')
      return
    }
    if (judgeID.trim() === '') {
      setError('实验至少需要一名裁判；请填写裁判连接 ID（独立性由服务端校验）')
      return
    }
    let targetConfig: CreateExperimentRequest['targetConfig'] | undefined
    if (isGRPO) {
      if (boundaryReferenceJSON.trim() !== '') {
        try {
          const parsed = JSON.parse(boundaryReferenceJSON) as NonNullable<CreateExperimentRequest['targetConfig']>['boundaryReference']
          if (!parsed || !Array.isArray(parsed.items)) throw new Error('边界参考集必须包含 items 数组')
          targetConfig = {
            teacherPromptVersion: teacherPromptVersion.trim() || undefined,
            baselineAnswerVersion: baselineAnswerVersion.trim() || undefined,
            boundaryReference: parsed,
          }
        } catch (parseError) {
          setError(parseError instanceof Error ? parseError.message : '边界参考集 JSON 无效')
          return
        }
      } else {
        targetConfig = {
          teacherPromptVersion: teacherPromptVersion.trim() || undefined,
          baselineAnswerVersion: baselineAnswerVersion.trim() || undefined,
        }
      }
    }
    setBusy(true)
    try {
      const experiment = await studioApi.createExperiment(scope.projectId, {
        // 提交的是**样本版本**（内容版本），不是样本：同一题的两版内容是两件事。
        sampleVersionIds: selected,
        samplingSeed: Number(seed) || 0,
        // GRPO 省略量表 → 服务端用内置 GRPO 量表（档位覆盖 / 边界稳定性 /
        // 评分解释一致性）。SFT 必须显式给出。
        rubric: isGRPO
          ? undefined
          : { dimensions: [{ key: 'accuracy', label: '准确', weight: 1, min: 0, max: 10 }] },
        judgeConnectionIds: [Number(judgeID)],
        missingScorePolicy: 'exclude',
        batchId: batchID.trim() === '' ? undefined : Number(batchID),
        targetConfig,
      }, { idempotencyKey: idempotencyKeyRef.current })
      // 202 后进入报告页：此时状态是排队/运行中，**不显示**最终分数。
      navigate(`/p/${scope.projectId}/quality/${experiment.id}`)
    } catch (submitError) {
      setError(submitError instanceof Error ? submitError.message : '创建实验失败')
    } finally {
      setBusy(false)
    }
  }, [baselineAnswerVersion, batchID, boundaryReferenceJSON, canRun, isGRPO, judgeID, navigate, scope.projectId, seed, selected, teacherPromptVersion])

  return (
    <div className="console-page" data-studio-page="quality-new">
      <div className="console-page__header">
        <div>
          <Title heading={4} className="!mb-1">
            新建质量实验
          </Title>
          <Text type="tertiary">
            创建即冻结：范围、seed、量表与裁判都会写进实验，此后改配置不影响它。
          </Text>
        </div>
      </div>

      <Card className="console-card mb-3" bodyStyle={{ padding: 16 }}>
        <Text type="tertiary" size="small" className="block mb-1">
          批次（可选：把实验挂在某个批次上，便于在批次详情里回看）
        </Text>
        <Select
          value={batchID || undefined}
          placeholder="不指定"
          style={{ width: 240 }}
          aria-label="选择批次"
          optionList={batches.map((batch) => ({
            value: String(batch.batchId),
            label: `${batch.resourceId}（${batch.purpose === 'pilot' ? '试制' : '扩量'}）`,
          }))}
          onChange={(value) => setBatchID(String(value))}
          disabled={!canRun}
        />
      </Card>

      <Card className="console-card mb-3" bodyStyle={{ padding: 16 }} data-rubric-preview="true">
        <Text strong className="block mb-2">
          量表（创建后冻结）
        </Text>
        {isGRPO ? (
          <>
            <Text type="tertiary" size="small" className="block mb-1">
              本项目是 GRPO：使用内置 GRPO 量表（服务端定义，不由界面拼装）。
            </Text>
            {Object.entries(GRPO_DIMENSION_LABELS).map(([key, label]) => (
              <Text key={key} type="tertiary" size="small" className="block" data-grpo-dimension={key}>
                · {label}
              </Text>
            ))}
            <Text type="tertiary" size="small" className="block mt-1">
              边界稳定性需要**冻结的边界参考集**；没有参考集时该维度记缺分（缺证据），
              不会用 0 分凑一个结论。
            </Text>
          </>
        ) : (
          <Text type="tertiary" size="small">
            维度：accuracy（准确，权重 1，范围 0–10）。改量表需要新建实验。
          </Text>
        )}
      </Card>

      {isGRPO ? (
        <Card className="console-card mb-3" bodyStyle={{ padding: 16 }} data-grpo-target-config="true">
          <Text strong className="block mb-2">GRPO 冻结配置（可选参考集）</Text>
          <Text type="tertiary" size="small" className="block mb-2">
            教师提示词与基准回答版本会随实验冻结；没有边界参考集时，边界稳定性记为缺分，不会伪造 0 分或满分。
          </Text>
          <div className="wizard-fields">
            <Input aria-label="教师提示词版本" value={teacherPromptVersion} onChange={setTeacherPromptVersion} placeholder="教师提示词版本（可选）" disabled={!canRun} />
            <Input aria-label="基准回答版本" value={baselineAnswerVersion} onChange={setBaselineAnswerVersion} placeholder="基准回答版本（可选）" disabled={!canRun} />
            <TextArea aria-label="边界参考集 JSON" value={boundaryReferenceJSON} onChange={setBoundaryReferenceJSON} autosize={{ minRows: 3, maxRows: 8 }} placeholder='{"id":"boundary-v1","source":"manual","sampled":true,"items":[{"level":"中","input":"示例","expected":"accept"}]}' disabled={!canRun} />
          </div>
        </Card>
      ) : null}

      <Card className="console-card mb-3" bodyStyle={{ padding: 16 }} data-scope-picker="true">
        <Text strong className="block mb-2">
          检查范围（冻结为分母）
        </Text>
        <Text type="tertiary" size="small" className="block mb-2">
          已选 {selected.length} 个内容版本。
        </Text>
        <div className="sample-table">
          <div className="sample-row sample-row--head">
            <span />
            <span>内容版本</span>
            <span>审阅状态</span>
          </div>
          {samples.map((sample) => (
            <div key={sample.sampleId} className="sample-row">
              <input
                type="checkbox"
                aria-label={`选择 ${sample.title || sample.sampleKey}`}
                checked={sample.latestVersionId > 0 && selected.includes(sample.latestVersionId)}
                disabled={!canRun || sample.latestVersionId <= 0}
                onChange={(event) => {
                  const versionID = sample.latestVersionId
                  if (versionID <= 0) return
                  setSelected((previous) =>
                    event.target.checked
                      ? [...previous, versionID]
                      : previous.filter((id) => id !== versionID),
                  )
                }}
              />
              <span>
                {sample.title || sample.sampleKey} · v{sample.latestVersion}
                {sample.latestVersionId > 0 ? `（版本 ID ${sample.latestVersionId}）` : '（暂无内容版本）'}
              </span>
              <span>
                <Tag size="small">{sample.reviewStatus}</Tag>
              </span>
            </div>
          ))}
        </div>
      </Card>

      <Card className="console-card mb-3" bodyStyle={{ padding: 16 }}>
        <div className="wizard-fields">
          <div className="wizard-field" data-field="judge-connection">
            <label className="wizard-field__label" htmlFor="judge-connection">
              裁判连接 ID
            </label>
            <Input
              id="judge-connection"
              value={judgeID}
              onChange={(value) => setJudgeID(value)}
              placeholder="例如 5"
              disabled={!canRun}
            />
            <Text type="tertiary" size="small" className="block mt-1">
              服务端会**再次**检查独立性与同源别名：与生成来源同一接入点的连接不能自评。
            </Text>
          </div>
          <div className="wizard-field" data-field="sampling-seed">
            <label className="wizard-field__label" htmlFor="sampling-seed">
              抽样 seed
            </label>
            <InputNumber
              id="sampling-seed"
              value={Number(seed) || 0}
              onChange={(value) => setSeed(String(value ?? 0))}
              disabled={!canRun}
            />
          </div>
        </div>
      </Card>

      {error ? (
        <div className="wizard-field__error mb-3" role="alert">
          {error}
        </div>
      ) : null}

      <Button theme="solid" type="primary" loading={busy} disabled={!canRun} onClick={() => void submit()}>
        创建并冻结实验
      </Button>
    </div>
  )
}

// ---------------------------------------------------------------------------
// 报告（Q03）
// ---------------------------------------------------------------------------

export function QualityReportPage() {
  const scope = useProjectScope()
  const params = useParams()
  const { Title, Text } = Typography
  // 实验 ID 来自**路由参数**：刷新与分享因此能恢复同一实验（T19 验收项）。
  const experimentID = Number(params.experimentId ?? 0)

  const [detail, setDetail] = useState<ExperimentDetail | null>(null)
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)

  const load = useCallback(async () => {
    setLoading(true)
    setError(null)
    try {
      const response = await studioApi.getExperiment(scope.projectId, experimentID)
      setDetail(response)
    } catch (loadError) {
      setError(loadError instanceof Error ? loadError.message : '加载报告失败')
    } finally {
      setLoading(false)
    }
  }, [experimentID, scope.projectId])

  useEffect(() => {
    void load()
  }, [load])

  if (loading) {
    return (
      <div className="flex justify-center py-10">
        <Spin tip="正在加载报告" />
      </div>
    )
  }
  if (error || !detail) {
    return (
      <Card className="console-card">
        <Text strong className="block">
          报告加载失败
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

  const { experiment, report } = detail
  // 「进行中」时不显示均值：基于部分样本的均值会被当成结论
  //（T19 验收项「创建后排队页不提前显示最终分数」）。
  const running = experiment.status === 'queued' || experiment.status === 'running'

  return (
    <div className="console-page" data-studio-page="quality-report" data-experiment-status={experiment.status}>
      <div className="console-page__header">
        <div>
          <Title heading={4} className="!mb-1">
            质量报告 #{experiment.id}
          </Title>
          <Text type="tertiary">{report.stats.coverageDisplay}</Text>
        </div>
        <Tag color={statusColor(experiment.status)}>{STATUS_LABEL[experiment.status] ?? experiment.status}</Tag>
      </div>

      {/* 固定范围与分母：报告的可解释性来自这里。 */}
      <div className="console-stat-grid">
        <StatTile label="分母（冻结范围）" value={String(report.stats.inspected)} hint="不随筛选/隔离变化" />
        <StatTile label="已评分" value={String(report.stats.scored)} hint="覆盖的分子" />
        <StatTile label="缺分" value={String(report.stats.missing)} hint="缺分不是 0 分，不参与均值" />
        <StatTile label="出错" value={String(report.stats.error)} hint="需要重跑，不等于评得差" />
      </div>

      {running ? (
        <Card className="console-card mb-3" bodyStyle={{ padding: 14 }} data-running-notice="true">
          <div className="flex items-start gap-2">
            <AlertTriangle size={16} className="mt-1 text-amber-500" aria-hidden />
            <Text size="small">
              实验仍在进行：当前只显示完成覆盖，**不显示均值** ——
              基于部分样本的均值会被当成结论。本页可刷新查看进度。
            </Text>
          </div>
        </Card>
      ) : (
        <Card className="console-card mb-3" bodyStyle={{ padding: 14 }} data-dimension-stats="true">
          <Text strong className="block mb-2">
            维度均值（归一化后，只统计已评分的格）
          </Text>
          {report.dimensions.length === 0 ? (
            <Text type="tertiary" size="small">
              还没有可用的维度统计。
            </Text>
          ) : (
            <div className="comparison-table">
              <div className="comparison-row comparison-row--head">
                <span>维度</span>
                <span>均值</span>
                <span>已评分</span>
                <span>缺分</span>
                <span>覆盖的样本版本</span>
              </div>
              {report.dimensions.map((dimension) => (
                <div key={dimension.dimension} className="comparison-row" data-dimension={dimension.dimension}>
                  <span>{dimensionLabel(dimension.dimension)}</span>
                  <span>{dimension.mean.toFixed(3)}</span>
                  <span>{dimension.scoredCount}</span>
                  <span>{dimension.missingCount}</span>
                  <span>{dimension.covered}</span>
                </div>
              ))}
            </div>
          )}
        </Card>
      )}

      {report.judgeNotes.length > 0 ? (
        <Card className="console-card mb-3" bodyStyle={{ padding: 14 }} data-judge-notes="true">
          <Text strong className="block mb-1">
            覆盖与分歧说明
          </Text>
          {report.judgeNotes.map((note) => (
            <Text key={note} type="tertiary" size="small" className="block">
              · {note}
            </Text>
          ))}
        </Card>
      ) : null}

      {/* 待判断项：从报告直达具体内容版本（不自动隔离 —— 那要人工判断）。 */}
      {detail.pendingItems.length > 0 ? (
        <Card className="console-card" bodyStyle={{ padding: 14 }} data-pending-items="true">
          <Text strong className="block mb-2">
            尚未完成的项（{detail.pendingItems.length}）
          </Text>
          <ul className="review-evidence">
            {detail.pendingItems.slice(0, 20).map((item) => (
              <li key={item.id}>
                <a
                  href={`/p/${scope.projectId}/data/s_${item.sampleId}?version=${item.sampleVersionId}`}
                  className="console-link"
                >
                  样本 s_{item.sampleId} 的版本 {item.sampleVersionId}
                </a>
                {item.errorClass ? ` · ${item.errorClass}` : ''}
              </li>
            ))}
          </ul>
          <Text type="tertiary" size="small" className="block mt-2">
            从分歧/缺分直达内容版本；是否隔离由人工判断决定，规则不会自动改写处置。
          </Text>
        </Card>
      ) : null}
    </div>
  )
}

// ---------------------------------------------------------------------------
// 规则页（Q05）
// ---------------------------------------------------------------------------

export function RulesPage() {
  const scope = useProjectScope()
  const { Title, Text } = Typography
  const [preview, setPreview] = useState<RulePreviewResult | null>(null)
  const [policyVersionID, setPolicyVersionID] = useState('')
  const [sampleVersionIDs, setSampleVersionIDs] = useState('')
  const [error, setError] = useState<string | null>(null)
  const [busy, setBusy] = useState(false)

  const runPreview = useCallback(async () => {
    setError(null)
    setBusy(true)
    try {
      const ids = sampleVersionIDs
        .split(',')
        .map((value) => Number(value.trim()))
        .filter((value) => Number.isFinite(value) && value > 0)
      const result = await studioApi.previewRules(scope.projectId, {
        qualityPolicyVersionId: Number(policyVersionID),
        sampleVersionIds: ids,
      })
      setPreview(result)
    } catch (previewError) {
      setError(previewError instanceof Error ? previewError.message : '预览失败')
    } finally {
      setBusy(false)
    }
  }, [policyVersionID, sampleVersionIDs, scope.projectId])

  return (
    <div className="console-page" data-studio-page="rules">
      <div className="console-page__header">
        <div>
          <Title heading={4} className="!mb-1">
            清洗策略与规则预览
          </Title>
          <Text type="tertiary">
            预览是**纯读**：不改内容、不改处置、不入收费模型队列。非法正则不会入库。
          </Text>
        </div>
      </div>

      <Card className="console-card mb-3" bodyStyle={{ padding: 16 }} data-rule-preview-form="true">
        <div className="wizard-fields">
          <div className="wizard-field">
            <label className="wizard-field__label" htmlFor="policy-version">
              质量策略版本 ID
            </label>
            <Input
              id="policy-version"
              value={policyVersionID}
              onChange={(value) => setPolicyVersionID(value)}
              placeholder="例如 12"
            />
          </div>
          <div className="wizard-field">
            <label className="wizard-field__label" htmlFor="preview-versions">
              样本版本 ID（逗号分隔）
            </label>
            <Input
              id="preview-versions"
              value={sampleVersionIDs}
              onChange={(value) => setSampleVersionIDs(value)}
              placeholder="例如 31,32,33"
            />
          </div>
        </div>
        {error ? (
          <div className="wizard-field__error mt-2" role="alert">
            {error}
          </div>
        ) : null}
        <div className="mt-2">
          <Button theme="solid" type="primary" loading={busy} onClick={() => void runPreview()}>
            预览命中
          </Button>
        </div>
      </Card>

      {preview ? (
        <Card className="console-card" bodyStyle={{ padding: 14 }} data-preview-result="true">
          <Text strong className="block mb-1">
            扫描 {preview.scannedCount} 个内容版本，命中 {preview.hits.length} 处
            {preview.truncated ? '（已达上限，结果被截断）' : ''}
          </Text>
          <Text type="tertiary" size="small" className="block mb-2">
            本次预览未产生任何副作用（sideEffects={String(preview.sideEffects)}）。
          </Text>
          {preview.hits.length === 0 ? (
            <Text type="tertiary" size="small">
              没有命中。
            </Text>
          ) : (
            <ul className="review-evidence">
              {preview.hits.slice(0, 50).map((hit, index) => (
                <li key={`${hit.ruleId}-${hit.matchStart}-${index}`}>
                  规则 {hit.ruleId}（{hit.severity}）命中 {hit.field} 的 {hit.matchStart}–{hit.matchEnd}：
                  <code>{hit.snippet}</code>
                </li>
              ))}
            </ul>
          )}
        </Card>
      ) : null}
    </div>
  )
}

function StatTile({ label, value, hint }: { label: string; value: string; hint: string }) {
  const { Text } = Typography
  return (
    <Card className="console-card" bodyStyle={{ padding: 14 }}>
      <Text type="tertiary" size="small" className="block">
        {label}
      </Text>
      <div className="console-stat-value">{value}</div>
      <Text type="tertiary" size="small" className="block">
        {hint}
      </Text>
    </Card>
  )
}
