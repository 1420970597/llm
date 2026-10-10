import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { useNavigate, useParams, useSearchParams } from 'react-router-dom'
import { Button, Card, Empty, Input, InputNumber, Modal, Select, Spin, Tag, TextArea, Typography } from '@douyinfe/semi-ui'
// AlertTriangle 来自 lucide-react（图标库），不是 semi-ui 的组件。
import { AlertTriangle } from 'lucide-react'
import { client } from '../../lib/api'
import { newIdempotencyKey, projectPath, settingsApi, studioApi } from '../../lib/api/studio'
// #211：审阅状态列原先直接渲染后端 `effective_action`（pending/accepted），
// 中文界面里漏出内部枚举；同时页面未说明「未审阅内容能否纳入评测」。
import { describeReviewStatus, reviewStatusColor, unreviewedScopeNotice } from '../../lib/enumLabels'
import type { BatchSummary, ConnectionProviderOption, CreateExperimentRequest, Experiment, ExperimentDetail, Page, SampleSummary, RulePreviewResult } from '../../lib/api/studio'
import { useProjectScope } from '../ProjectLayout'
import { projectHref } from '../StudioLayout'
import { CopyVersionButton, DocumentHistory, DocumentSaveBar, QualityPolicyPayloadEditor, useVersionedDocument } from '../DocumentEditors'
import { useBlueprintContext } from '../blueprintContext'

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
 *  3. **风险计数与筛选列表一致**：被评测数据集/缺失/出错都来自报告的同一份统计，
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
            质量实验
          </Title>
          {/* issue #197 第 14 条：不用「分母/分子」，改用「被评测数据集」表达。
              统计口径**没有变**（依旧是实验创建时冻结的样本版本数），
              变的是说法：甲方看到「分母」不知道它在说什么。 */}
        </div>
        <Button theme="solid" type="primary" disabled={!canRun} onClick={() => navigate(projectHref('project.qualityNew', scope.projectId))}>
          新建质量实验
        </Button>
      </div>

      {experiments.length === 0 ? (
        <Card className="console-card">
          <Empty description="还没有质量实验" />
        </Card>
      ) : (
        <div className="batch-table" data-experiment-table="true">
          <div className="batch-row batch-row--head">
            <span>实验</span>
            <span>状态</span>
            <span>被评测数据集</span>
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
                  onClick={() => navigate(projectHref('project.qualityReport', scope.projectId, { experimentId: experiment.id }))}
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
  const [searchParams] = useSearchParams()
  const blueprintId = searchParams.get('blueprintVersionId')
  const blueprint = useBlueprintContext(blueprintId ? scope.projectId : undefined, blueprintId)
  const { Title, Text } = Typography

  const [batches, setBatches] = useState<BatchSummary[]>([])
  const [samples, setSamples] = useState<SampleSummary[]>([])
  // #211：未审阅的内容版本数（用于在「检查范围」处显式说明它们能否纳入评测）。
  // 派生而不是另存一份 state：另存一份会与 samples 漂移。
  const unreviewedCount = useMemo(
    () => samples.filter((sample) => sample.reviewStatus === 'pending').length,
    [samples],
  )
  // #211 方向 2：**已勾选**里有多少条尚未人工判断。
  //
  // 为什么与 unreviewedCount 分开：那是「页面上有多少条未审阅」的**事实陈述**，
  // 这是「你这次评测将要纳入多少条未审阅内容」的**行动后果**。
  // 用户需要的是后者 —— 它决定了提交前提示要不要出现。
  // 声明必须在 selected 之后（useMemo 的依赖会被立即求值）。
  const [batchID, setBatchID] = useState('')
  const [selected, setSelected] = useState<number[]>([])
  const selectedUnreviewedCount = useMemo(
    () =>
      samples.filter(
        (sample) =>
          sample.latestVersionId > 0 &&
          selected.includes(sample.latestVersionId) &&
          sample.reviewStatus === 'pending',
      ).length,
    [samples, selected],
  )
  const formContext = `${scope.projectId}:${blueprintId ?? 'manual'}`
  const [judgeSelection, setJudgeSelection] = useState<{ context: string; value: string } | null>(null)
  const [judgeMaxTokens, setJudgeMaxTokens] = useState(4096)
  /**
   * 裁判模型候选（issue #197 第 14 条）。
   *
   * 缺陷形态：这里是**手填数字 ID** 的文本框（placeholder「例如 5」），
   * 甲方既不知道有哪些模型，也不知道该填哪个数字；填错只会在提交时
   * 得到一句服务端错误。改为从连接目录（只含**已启用**连接）下拉选择。
   */
  const [judgeOptions, setJudgeOptions] = useState<ConnectionProviderOption[]>([])
  const [judgeOptionsLoading, setJudgeOptionsLoading] = useState(true)
  const [judgeOptionsError, setJudgeOptionsError] = useState<string | null>(null)
  const [judgeOptionsReload, setJudgeOptionsReload] = useState(0)

  useEffect(() => {
    let cancelled = false
    setJudgeOptionsLoading(true)
    setJudgeOptionsError(null)
    void (async () => {
      try {
        const response = await settingsApi.connectionOptions()
        if (cancelled) return
        setJudgeOptions((response.providers ?? []).filter((provider) => provider.isActive))
      } catch (loadError) {
        if (!cancelled) {
          setJudgeOptionsError(loadError instanceof Error ? loadError.message : '加载模型连接失败')
        }
      } finally {
        if (!cancelled) setJudgeOptionsLoading(false)
      }
    })()
    return () => {
      cancelled = true
    }
  }, [judgeOptionsReload])
  const [seedSelection, setSeedSelection] = useState<{ context: string; value: number } | null>(null)
  const hasBlueprintSelection = Boolean(blueprintId && String(blueprint.current?.id) === blueprintId)
  // 蓝图配置直接从当前冻结版本派生，手动输入绑定当前上下文。
  // 避免用 effect / ref 清空旧值时让显示、提交和用户刚做的选择发生竞态。
  const blueprintConfig = useMemo(() => {
    const nodes = (hasBlueprintSelection ? blueprint.current?.payload.nodes : undefined) as Record<string, Record<string, unknown>> | undefined
    const config = nodes?.evaluation ?? {}
    const judges = Array.isArray(config.judgeConnectionIds) ? config.judgeConnectionIds.filter((value): value is number => typeof value === 'number' && value > 0) : []
    const weights = config.weights && typeof config.weights === 'object' ? config.weights as Record<string, number> : {}
    const labels: Record<string, string> = { accuracy: '准确', reasoning: '推理', completeness: '完整', relevance: '相关', consistency: '一致' }
    const dimensions = Object.entries(weights).map(([key, weight]) => ({ key, label: labels[key] ?? '自定义维度', weight, min: 0, max: 10 }))
    return {
      judges, seed: Number(config.samplingSeed ?? 42) || 0,
      missingScorePolicy: config.missingScorePolicy === 'fail_experiment' ? 'fail_experiment' as const : 'exclude' as const,
      policyError: config.missingScorePolicy && !['exclude', 'fail_experiment'].includes(String(config.missingScorePolicy)) ? '所选蓝图的缺分策略尚不支持质量实验。请在蓝图中选择“排除缺分”后保存新版本，避免改变评测含义。' : null,
      rubric: dimensions.length ? { dimensions } : undefined,
    }
  }, [blueprint.current, hasBlueprintSelection])
  const hasJudgeOverride = judgeSelection?.context === formContext
  const effectiveJudgeID = hasJudgeOverride ? judgeSelection.value : blueprintConfig.judges[0] ? String(blueprintConfig.judges[0]) : ''
  const blueprintJudges = hasJudgeOverride ? [] : blueprintConfig.judges
  const effectiveSeed = seedSelection?.context === formContext ? seedSelection.value : blueprintConfig.seed
  const missingScorePolicy = blueprintConfig.missingScorePolicy
  const blueprintRubric = blueprintConfig.rubric
  const blueprintPolicyError = blueprintConfig.policyError
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
  const grpoBlueprintError = isGRPO && blueprintRubric ? '所选蓝图包含自定义维度权重，但 GRPO 实验仅支持服务端内置量表。请清除该蓝图的自定义权重并保存新版本后再创建实验。' : null

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
    if (blueprintId && (!hasBlueprintSelection || blueprint.loading || blueprint.error || blueprintPolicyError || grpoBlueprintError)) {
      setError(blueprintPolicyError ?? grpoBlueprintError ?? '请先成功读取所选蓝图版本，再创建实验'); return
    }
    if (!canRun) {
      setError('当前项目没有运行质量实验的权限；请联系项目负责人')
      return
    }
    if (selected.length === 0) {
      setError('实验范围不能为空：请至少选择一个样本版本（空数据集无法得出任何结论）')
      return
    }
    if (effectiveJudgeID.trim() === '') {
      setError('请先选择裁判模型，再创建实验（独立性由服务端校验）')
      document.getElementById('judge-connection')?.focus()
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
        samplingSeed: effectiveSeed,
        // GRPO 省略量表 → 服务端用内置 GRPO 量表（档位覆盖 / 边界稳定性 /
        // 评分解释一致性）。SFT 必须显式给出。
        rubric: isGRPO
          ? undefined
          : (hasBlueprintSelection ? blueprintRubric : undefined) ?? { dimensions: [{ key: 'accuracy', label: '准确', weight: 1, min: 0, max: 10 }] },
        judgeConnectionIds: hasBlueprintSelection && blueprintJudges.length ? blueprintJudges : [Number(effectiveJudgeID)],
        judgeMaxTokens,
        missingScorePolicy: hasBlueprintSelection ? missingScorePolicy : 'exclude',
        batchId: batchID.trim() === '' ? undefined : Number(batchID),
        targetConfig,
      }, { idempotencyKey: idempotencyKeyRef.current })
      // 202 后进入报告页：此时状态是排队/运行中，**不显示**最终分数。
      navigate(projectHref('project.qualityReport', scope.projectId, { experimentId: experiment.id }))
    } catch (submitError) {
      setError(submitError instanceof Error ? submitError.message : '创建实验失败')
    } finally {
      setBusy(false)
    }
  }, [baselineAnswerVersion, batchID, blueprint.error, blueprint.loading, blueprintId, blueprintJudges, blueprintPolicyError, blueprintRubric, boundaryReferenceJSON, canRun, effectiveJudgeID, effectiveSeed, grpoBlueprintError, hasBlueprintSelection, isGRPO, judgeMaxTokens, missingScorePolicy, navigate, scope.projectId, selected, teacherPromptVersion])

  /**
   * #211 方向 2：勾选了未审阅内容时，**提交前**显式提示一次。
   *
   * 为什么必须在提交前而不是只靠页顶一句说明：页顶的说明是常驻背景信息，
   * 用户点「创建并冻结实验」时不会再读一遍；而未审阅内容的后果（结论不得作为
   * 发布证据、发布时仍会被 PENDING_REVIEW 拦住）是**这次操作**带来的。
   * 用 Modal.confirm 而不是直接拦截：评测本来就可以纳入未审阅内容（它只是度量），
   * 因此这里要的是「知情确认」而不是「禁止」。
   */
  const confirmSubmit = useCallback(() => {
    if (selectedUnreviewedCount === 0) {
      void submit()
      return
    }
    Modal.confirm({
      title: `本次将纳入 ${selectedUnreviewedCount} 个尚未人工判断的内容版本`,
      content: (
        <div className="console-stack">
          <Text className="block">
            评测可以纳入未审阅内容（评测只是度量），实验照常创建与冻结。
          </Text>
          <Text type="tertiary" className="block">
            但这些版本的结论不作为发布证据：发布时仍会被未审阅门槛（PENDING_REVIEW）拦住。
            若希望评测结论能直接支撑发布，请先到「审阅」页完成判断。
          </Text>
        </div>
      ),
      okText: '仍然创建实验',
      cancelText: '返回先审阅',
      onOk: () => void submit(),
    })
  }, [selectedUnreviewedCount, submit])

  return (
    <div className="console-page" data-studio-page="quality-new">
      <div className="console-page__header">
        <div>
          <Title heading={4} className="!mb-1">
            新建质量实验
          </Title>
          <Text type="tertiary">
            创建即冻结：范围、抽样编号、量表与裁判都会写进实验，此后改配置不影响它。
          </Text>
        </div>
      </div>

      {blueprintId ? <Card className="console-card mb-3" bodyStyle={{ padding: 16 }} data-quality-blueprint-context="true">
        {blueprint.loading ? <Spin tip="正在读取工作台所选蓝图" /> : blueprint.error ? <div role="alert">{blueprint.error}<Button onClick={blueprint.reload}>重试</Button></div> : <Text>来自蓝图 v{blueprint.current?.version}：已带入 {blueprintJudges.length} 名裁判、抽样种子{!isGRPO && blueprintRubric ? '、维度权重' : ''}与缺分策略。{isGRPO ? 'GRPO 使用服务端内置量表。' : ''}创建时冻结下方范围与实际配置。</Text>}
        {blueprintPolicyError || grpoBlueprintError ? <Text type="danger" className="block" role="alert">{blueprintPolicyError || grpoBlueprintError}</Text> : null}
      </Card> : null}

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
              边界稳定性需要冻结的边界参考集；没有参考集时该维度记缺分（缺证据），
              不会用 0 分凑一个结论。
            </Text>
          </>
        ) : (
          <Text type="tertiary" size="small">
            {blueprintRubric ? `蓝图维度：${blueprintRubric.dimensions.map((dimension) => `${dimension.label}（权重 ${dimension.weight}）`).join('、')}；范围 0–10。` : '维度：accuracy（准确，权重 1，范围 0–10）。'}改量表需要新建实验。
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
          检查范围（冻结为「被评测数据集」）
        </Text>
        <Text type="tertiary" size="small" className="block mb-2">
          已选 {selected.length} 个内容版本。
        </Text>
        {/* #211：必须说明未审阅内容能否纳入评测 —— 否则用户不知道该不该勾选，
            而发布门槛（PENDING_REVIEW）实际会拦住未审阅内容，两者需要一个显式交代。 */}
        {unreviewedCount > 0 ? (
          <Text type="warning" size="small" className="block mb-2" data-scope-unreviewed-notice="true">
            {unreviewedScopeNotice(unreviewedCount)}
          </Text>
        ) : null}
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
                <span title={sample.reviewStatus}>
                  <Tag size="small" color={reviewStatusColor(sample.reviewStatus)}>
                    {describeReviewStatus(sample.reviewStatus)}
                  </Tag>
                </span>
                {/* #211 方向 2：已勾选的未审阅内容要**行内**标出代价，
                    而不是只靠页顶一句背景说明 —— 行内标记能回答
                    「我刚勾的这条算不算已验证证据」。 */}
                {sample.reviewStatus === 'pending' &&
                sample.latestVersionId > 0 &&
                selected.includes(sample.latestVersionId) ? (
                  <Tag
                    size="small"
                    color="orange"
                    data-scope-unreviewed-row="true"
                  >
                    未审阅：结论不作为发布证据
                  </Tag>
                ) : null}
              </span>
            </div>
          ))}
        </div>
        {samples.length === 0 ? <div role="status"><Text type="tertiary">没有可评测样本。</Text><Button onClick={() => navigate(scope.href('project.runs'))}>开始生产</Button><Button onClick={() => navigate(scope.href('project.sourceImport'))}>导入数据集</Button></div> : null}
      </Card>

      <Card className="console-card mb-3" bodyStyle={{ padding: 16 }}>
        <div className="wizard-fields">
          <div className="wizard-field" data-field="judge-connection" data-selected-judge={effectiveJudgeID}>
            <label className="wizard-field__label" htmlFor="judge-connection">
              裁判模型
            </label>
            {/* 只列**已启用**的连接：停用的连接选了也跑不起来。 */}
            <Select
              key={formContext}
              id="judge-connection"
              value={effectiveJudgeID}
              style={{ width: '100%' }}
              placeholder={
                judgeOptionsLoading
                  ? '正在加载模型连接…'
                  : judgeOptions.length > 0
                    ? '选择一个已启用的模型连接'
                    : '没有可用的模型连接'
              }
              disabled={!canRun || judgeOptionsLoading || judgeOptions.length === 0}
              optionList={judgeOptions.map((provider) => ({
                value: String(provider.id),
                label: `${provider.name || `未命名连接 #${provider.id}`}（${provider.model}）${provider.configIssues?.length ? ' · 配置不完整' : ''}`,
                disabled: (provider.configIssues?.length ?? 0) > 0,
              }))}
              onChange={(value) => setJudgeSelection({ context: formContext, value: String(value ?? '') })}
              data-judge-connection-select="true"
            />
            {judgeOptionsError ? (
              <Text type="danger" size="small" className="block mt-1" data-judge-options-error="true">
                {judgeOptionsError}
                <Button size="small" onClick={() => setJudgeOptionsReload((value) => value + 1)}>重新读取裁判模型</Button>
              </Text>
            ) : null}
            {!judgeOptionsLoading && judgeOptions.length === 0 ? (
              <Text type="tertiary" size="small" className="block mt-1" data-judge-options-empty="true">
                还没有已启用的模型连接。请先在「设置 › 连接与存储」里启用一个连接，再回来创建实验。
                <Button size="small" onClick={() => navigate('/settings/connections')}>设置模型连接</Button>
              </Text>
            ) : null}
            <Text type="tertiary" size="small" className="block mt-1">
              服务端会再次检查独立性与同源别名：与生成来源同一接入点的连接不能自评。
            </Text>
          </div>
          <div className="wizard-field" data-field="judge-max-tokens">
            <label className="wizard-field__label" htmlFor="judge-max-tokens">裁判输出上限（token）</label>
            <InputNumber id="judge-max-tokens" min={512} max={32768} precision={0} value={judgeMaxTokens} onChange={(value) => setJudgeMaxTokens(Number(value ?? 0))} disabled={!canRun} />
            <Text type="tertiary" size="small">随实验冻结，用于预留预算；每次裁判请求单独记录费用。</Text>
          </div>
          <div className="wizard-field" data-field="sampling-seed">
            <label className="wizard-field__label" htmlFor="sampling-seed">
              可复现抽样编号
            </label>
            <InputNumber
              id="sampling-seed"
              value={effectiveSeed}
              onChange={(value) => setSeedSelection({ context: formContext, value: Number(value ?? 0) })}
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

      <Button theme="solid" type="primary" loading={busy} disabled={!canRun} onClick={confirmSubmit}>
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

      {/* 固定范围：报告的可解释性来自这里。用「被评测数据集」而不是「分母」（#197 第 14 条）。 */}
      <div className="console-stat-grid">
        <StatTile label="被评测数据集" value={String(report.stats.inspected)} hint="实验创建时冻结的样本版本数，不随筛选/隔离变化" />
        <StatTile label="已评分" value={String(report.stats.scored)} hint="被评测数据集里已得出分数的部分" />
        <StatTile label="缺分" value={String(report.stats.missing)} hint="缺分不是 0 分，不参与均值" />
        <StatTile label="出错" value={String(report.stats.error)} hint="需要重跑，不等于评得差" />
      </div>

      {running ? (
        <Card className="console-card mb-3" bodyStyle={{ padding: 14 }} data-running-notice="true">
          <div className="flex items-start gap-2">
            <AlertTriangle size={16} className="mt-1 text-amber-500" aria-hidden />
            <Text size="small">
              实验仍在进行：当前只显示完成覆盖，不显示均值 ——
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
                  href={`${projectHref('project.sample', scope.projectId, { sampleId: `s_${item.sampleId}` })}?version=${encodeURIComponent(String(item.sampleVersionId))}`}
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
  const documentState = useVersionedDocument(scope.projectId, 'quality-policy-versions')
  const [searchParams] = useSearchParams()
  const blueprintId = searchParams.get('blueprintVersionId')
  const blueprint = useBlueprintContext(blueprintId ? scope.projectId : undefined, blueprintId)
  const [preview, setPreview] = useState<RulePreviewResult | null>(null)
  const [policyVersionID, setPolicyVersionID] = useState('')
  const [sampleVersionIDs, setSampleVersionIDs] = useState('')
  const [error, setError] = useState<string | null>(null)
  const [busy, setBusy] = useState(false)

  const blueprintNodes = blueprint.current?.payload.nodes as Record<string, Record<string, unknown>> | undefined
  const referencedPolicyID = Number(blueprintNodes?.rules?.qualityPolicyVersionId)
  const selectedPolicyVersionID = blueprintId ? (referencedPolicyID > 0 ? String(referencedPolicyID) : '') : policyVersionID

  useEffect(() => {
    if (!blueprintId && documentState.current && policyVersionID === '') {
      setPolicyVersionID(String(documentState.current.id))
    }
  }, [blueprintId, documentState.current, policyVersionID])

  const runPreview = useCallback(async () => {
    setError(null)
    if (blueprintId && (!blueprint.current || blueprint.loading || blueprint.error)) { setError('请先成功读取所选蓝图版本，再预览规则'); return }
    // 目录只有最近一页。历史蓝图的冻结引用由服务端校验项目归属，不能因不在这一页而拒绝。
    if (!Number.isSafeInteger(Number(selectedPolicyVersionID)) || Number(selectedPolicyVersionID) <= 0 || (!blueprintId && !documentState.versions.some((version) => String(version.id) === selectedPolicyVersionID))) { setError('请选择当前项目可用的规则版本；蓝图引用的策略可能尚未配置。'); return }
    setBusy(true)
    try {
      const ids = sampleVersionIDs
        .split(',')
        .map((value) => Number(value.trim()))
        .filter((value) => Number.isFinite(value) && value > 0)
      const result = await studioApi.previewRules(scope.projectId, {
        qualityPolicyVersionId: Number(selectedPolicyVersionID),
        sampleVersionIds: ids,
      })
      setPreview(result)
    } catch (previewError) {
      setError(previewError instanceof Error ? previewError.message : '预览失败')
    } finally {
      setBusy(false)
    }
  }, [blueprint.current, blueprint.error, blueprint.loading, blueprintId, documentState.versions, selectedPolicyVersionID, sampleVersionIDs, scope.projectId])

  return (
    <div className="console-page" data-studio-page="rules">
      <div className="console-page__header">
        <div>
          <Title heading={4} className="!mb-1">
            清洗策略与规则预览
          </Title>
          <Text type="tertiary">
            先编辑并保存规则版本，再用下方预览检查它会命中哪些内容。预览不会修改内容或处置结果。
          </Text>
        </div>
        <div className="console-page__actions"><CopyVersionButton state={documentState} /></div>
      </div>

      {blueprintId ? <Card className="console-card mb-3" bodyStyle={{ padding: 16 }} data-rule-blueprint-context="true">{blueprint.loading ? <Spin tip="正在读取工作台所选蓝图" /> : blueprint.error ? <div role="alert">{blueprint.error}<Button onClick={blueprint.reload}>重试</Button></div> : <Text>来自蓝图 v{blueprint.current?.version}：规则预览已选择该蓝图引用的策略版本。上方编辑保存策略不会改写该蓝图或既有批次。</Text>}</Card> : null}

      {documentState.loading ? <Card className="console-card"><Spin tip="正在加载规则版本" /></Card> : null}
      {documentState.error ? <Card className="console-card"><Text type="danger">{documentState.error}</Text><Button size="small" className="mt-2" onClick={() => void documentState.reload()}>重试</Button></Card> : null}
      {!documentState.loading && !documentState.error ? <>
        <QualityPolicyPayloadEditor payload={documentState.payload ?? { schemaVersion: 'quality_policy.v1', rules: [] }} disabled={documentState.isReadOnly || !documentState.canEdit} onChange={documentState.setPayload} />
        <DocumentSaveBar state={documentState} label="规则策略" />
        <DocumentHistory state={documentState} />
      </> : null}

      <Card className="console-card mb-3" bodyStyle={{ padding: 16 }} data-rule-preview-form="true">
        <div className="wizard-fields">
          <div className="wizard-field">
            <label className="wizard-field__label" htmlFor="policy-version">
              用于预览的规则版本
            </label>
            <Select id="policy-version" aria-label="用于预览的规则版本" value={selectedPolicyVersionID || undefined} onChange={(value) => setPolicyVersionID(String(value))} optionList={[
              ...documentState.versions.map((version) => ({ value: String(version.id), label: `v${version.version} · ${version.changeReason || '未填写理由'}` })),
              ...(blueprintId && selectedPolicyVersionID && !documentState.versions.some((version) => String(version.id) === selectedPolicyVersionID) ? [{ value: selectedPolicyVersionID, label: `蓝图冻结策略（版本 ID ${selectedPolicyVersionID}）` }] : []),
            ]} placeholder="选择规则版本" disabled={documentState.versions.length === 0 || Boolean(blueprintId)} />
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
