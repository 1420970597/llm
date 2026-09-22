import { useCallback, useEffect, useMemo, useState } from 'react'
import { useNavigate, useSearchParams } from 'react-router-dom'
import { Button, Card, Empty, Select, Spin, Tag, TextArea, Typography } from '@douyinfe/semi-ui'
import { AlertTriangle, ArrowRight, RefreshCw } from 'lucide-react'
import { client } from '../../lib/api'
import { projectPath, studioApi } from '../../lib/api/studio'
import type { ComparisonDetail, Page, BatchSummary } from '../../lib/api/studio'
import { useProjectScope } from '../ProjectLayout'

/**
 * 试制对比页（Issue #160 T18 的 P06）。
 *
 * 契约：docs/plans/atelier-implementation.md §3；internal/model/comparison.go。
 *
 * 这个页面最重要的职责不是「画表格」，而是**把可比性说清楚**：
 *
 *   * `comparability.label` 与 `disclaimers` 来自服务端（报告的一部分），
 *     页面必须原样显示 —— 包括「不做显著性/最佳方案断言」这句限定；
 *   * 不可比时**不提供采用按钮**：采用一个不可比的比较，等于用一个无法解释的
 *     差异做决定；
 *   * 「采用」只更新指针并给出扩量入口，**不自动运行或发布**。
 *
 * URL 参数 `?baselineId=&left=&right=` 让报告可分享（§3.2 的同一原则）。
 */

export function ComparePage() {
  const scope = useProjectScope()
  const navigate = useNavigate()
  const [searchParams, setSearchParams] = useSearchParams()
  const { Title, Text } = Typography

  const baselineIDParam = searchParams.get('baselineId')
  const baselineID = baselineIDParam ? Number(baselineIDParam) : 0

  const [baselines, setBaselines] = useState<ComparisonDetail['baseline'][]>([])
  const [batches, setBatches] = useState<BatchSummary[]>([])
  const [detail, setDetail] = useState<ComparisonDetail | null>(null)
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)
  const [busy, setBusy] = useState(false)

  // 创建表单（页面内选择两侧批次）。
  const [leftBatch, setLeftBatch] = useState('')
  const [rightBatch, setRightBatch] = useState('')
  const [createError, setCreateError] = useState<string | null>(null)

  // 采用表单。
  const [side, setSide] = useState<'left' | 'right'>('right')
  const [reason, setReason] = useState('')
  const [adoptError, setAdoptError] = useState<string | null>(null)
  const [nextStep, setNextStep] = useState<{ label: string; href: string } | null>(null)

  const load = useCallback(async () => {
    setLoading(true)
    setError(null)
    try {
      const [listResponse, batchResponse] = await Promise.all([
        studioApi.listComparisonBaselines(scope.projectId),
        client.get<Page<BatchSummary>>(`${projectPath(scope.projectId)}/batches?limit=50`),
      ])
      setBaselines(listResponse.items ?? [])
      setBatches(batchResponse.data.items ?? [])

      if (baselineID > 0) {
        const comparison = await studioApi.getComparisonBaseline(scope.projectId, baselineID)
        setDetail(comparison)
      } else {
        setDetail(null)
      }
    } catch (loadError) {
      setError(loadError instanceof Error ? loadError.message : '加载比较数据失败')
    } finally {
      setLoading(false)
    }
  }, [baselineID, scope.projectId])

  useEffect(() => {
    void load()
  }, [load])

  /** 两个 pilot 批次必须都是试制、且互不相同（判据在服务端，这里只做粗筛）。 */
  const pilotBatches = useMemo(
    () => batches.filter((batch) => batch.purpose === 'pilot'),
    [batches],
  )

  const createBaseline = useCallback(async () => {
    setCreateError(null)
    const left = Number(leftBatch)
    const right = Number(rightBatch)
    if (!left || !right) {
      setCreateError('请选择两侧批次')
      return
    }
    setBusy(true)
    try {
      const created = await studioApi.createComparisonBaseline(scope.projectId, {
        name: `试制比较 ${new Date().toISOString().slice(0, 10)}`,
        metric: 'paired',
        // 逐题配对必须固定输入问题版本；这里用覆盖单元键作为输入参照
        //（两侧基于同一份覆盖时会得到相同的键空间）。
        inputRef: `units:${left}vs${right}`,
        samplingSeed: 42,
        rubric: {
          dimensions: [{ key: 'accuracy', label: '准确', weight: 1, min: 0, max: 10 }],
        },
        judges: [],
        leftBatchId: left,
        rightBatchId: right,
      })
      setSearchParams((params) => {
        params.set('baselineId', String(created.id))
        return params
      })
    } catch (createErrorValue) {
      setCreateError(createErrorValue instanceof Error ? createErrorValue.message : '创建比较基准失败')
    } finally {
      setBusy(false)
    }
  }, [leftBatch, rightBatch, scope.projectId, setSearchParams])

  const adopt = useCallback(async () => {
    if (!detail) return
    setAdoptError(null)
    if (reason.trim() === '') {
      setAdoptError('采用方案必须写明依据（没有依据的决定无法在以后复核）')
      return
    }
    setBusy(true)
    try {
      const result = await studioApi.adoptComparison(scope.projectId, detail.baseline.id, {
        side,
        reason: reason.trim(),
      })
      // 只把服务端返回的批次/基准指针放进 URL。版本 ID 等配置仍由
      // `/adopted-batch` + `/batches/{id}` 读取，避免把可编辑的 query
      // 参数当成执行配置。不要直接使用服务端 href：这里固定为本项目
      // 的 SPA 路由，防止 API 链接或外部地址把用户带出 Atelier。
      const planningParams = new URLSearchParams()
      const prefill = result.nextStep.prefill ?? {}
      for (const key of ['fromBatchId', 'baselineId'] as const) {
        const value = Number(prefill[key])
        if (Number.isSafeInteger(value) && value > 0) planningParams.set(key, String(value))
      }
      const query = planningParams.toString()
      setNextStep({
        label: result.nextStep.label,
        href: `/p/${scope.projectId}/runs/new${query ? `?${query}` : ''}`,
      })
      setReason('')
      await load()
    } catch (adoptErrorValue) {
      setAdoptError(adoptErrorValue instanceof Error ? adoptErrorValue.message : '采用失败')
    } finally {
      setBusy(false)
    }
  }, [detail, load, reason, scope.projectId, side])

  if (loading) {
    return (
      <div className="flex justify-center py-10">
        <Spin tip="正在加载比较数据" />
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
        <div className="mt-3">
          <Button size="small" onClick={() => void load()}>
            重试
          </Button>
        </div>
      </Card>
    )
  }

  return (
    <div className="console-page" data-studio-page="compare">
      <div className="console-page__header">
        <div>
          <Title heading={4} className="!mb-1">
            试制对比
          </Title>
          <Text type="tertiary">
            比较的是**相同输入下**两个方案的输出；不是拿两个任意批次的百分比相减。
          </Text>
        </div>
        <div className="flex flex-wrap items-center gap-2">
          <Select
            value={baselineID > 0 ? String(baselineID) : undefined}
            placeholder="选择比较基准"
            style={{ width: 220 }}
            aria-label="选择比较基准"
            optionList={baselines.map((baseline) => ({
              value: String(baseline.id),
              label: `${baseline.name || `#${baseline.id}`}（${baseline.metric}）`,
            }))}
            onChange={(value) => {
              setSearchParams((params) => {
                params.set('baselineId', String(value))
                return params
              })
            }}
          />
          <Button icon={<RefreshCw size={14} />} onClick={() => void load()}>
            刷新
          </Button>
        </div>
      </div>

      {/* 创建比较基准：选两个 pilot 批次。 */}
      <Card className="console-card mb-3" bodyStyle={{ padding: 14 }} data-create-baseline="true">
        <Text strong className="block mb-2">
          新建比较基准（选两个试制批次）
        </Text>
        <div className="flex flex-wrap items-center gap-2">
          <Select
            value={leftBatch || undefined}
            placeholder="左侧批次"
            style={{ width: 180 }}
            aria-label="左侧批次"
            optionList={pilotBatches.map((batch) => ({
              value: String(batch.batchId),
              label: `${batch.resourceId}（计划 ${batch.plannedUnits}）`,
            }))}
            onChange={(value) => setLeftBatch(String(value))}
          />
          <Select
            value={rightBatch || undefined}
            placeholder="右侧批次"
            style={{ width: 180 }}
            aria-label="右侧批次"
            optionList={pilotBatches.map((batch) => ({
              value: String(batch.batchId),
              label: `${batch.resourceId}（计划 ${batch.plannedUnits}）`,
            }))}
            onChange={(value) => setRightBatch(String(value))}
          />
          <Button loading={busy} onClick={() => void createBaseline()}>
            创建并比较
          </Button>
        </div>
        {createError ? (
          <div className="wizard-field__error mt-2" role="alert">
            {createError}
          </div>
        ) : null}
      </Card>

      {!detail ? (
        <Card className="console-card">
          <Empty description="还没有可看的比较。选择两个试制批次创建比较基准。" />
        </Card>
      ) : (
        <>
          {/* 可比性必须**最先**显示：它决定下面所有数字怎么读。 */}
          <Card
            className="console-card mb-3"
            bodyStyle={{ padding: 14 }}
            data-comparability={detail.report.comparability.comparable ? 'true' : 'false'}
          >
            <div className="flex items-start gap-2">
              {detail.report.comparability.comparable ? null : (
                <AlertTriangle size={16} className="mt-1 text-amber-500" aria-hidden />
              )}
              <div>
                <Text strong className="block">
                  {detail.report.comparability.label}
                </Text>
                {detail.report.comparability.disclaimers.map((disclaimer) => (
                  <Text key={disclaimer} type="tertiary" size="small" className="block mt-1">
                    · {disclaimer}
                  </Text>
                ))}
              </div>
            </div>
          </Card>

          <div className="console-stat-grid">
            <StatTile
              label="配对完成数（有效样本量）"
              value={String(detail.report.pairedCount)}
              hint="不是两侧批次的总数，也不是筛选总数"
            />
            <StatTile
              label="只在一侧有分"
              value={`${detail.report.leftOnlyCount} / ${detail.report.rightOnlyCount}`}
              hint="左 / 右；不计入差异，也不补 0 分"
            />
            <StatTile
              label="左 / 右 已结算成本"
              value={`${detail.report.costs.leftActualMinor} / ${detail.report.costs.rightActualMinor}`}
              hint={`单位：分（${detail.report.costs.currency}）`}
            />
            <StatTile
              label="左 / 右 未知费用"
              value={`${detail.report.costs.leftUncertainMinor} / ${detail.report.costs.rightUncertainMinor}`}
              hint="超时或断连的调用可能已收费：未知不记 0"
            />
          </div>

          <Card className="console-card mb-3" bodyStyle={{ padding: 14 }}>
            <Text strong className="block mb-2">
              维度差异（右 − 左，正数表示右侧更好）
            </Text>
            {detail.report.dimensions.length === 0 ? (
              <Text type="tertiary" size="small">
                没有维度差异可显示（可能两侧都没有评分）。
              </Text>
            ) : (
              <div className="comparison-table" data-comparison-table="true">
                <div className="comparison-row comparison-row--head">
                  <span>维度</span>
                  <span>左侧均值</span>
                  <span>右侧均值</span>
                  <span>差异</span>
                  <span>配对数</span>
                  <span>未配对</span>
                </div>
                {detail.report.dimensions.map((dimension) => (
                  <div key={dimension.dimension} className="comparison-row" data-dimension={dimension.dimension}>
                    <span>{dimension.dimension}</span>
                    <span>{dimension.leftMean.toFixed(3)}</span>
                    <span>{dimension.rightMean.toFixed(3)}</span>
                    <span className={dimension.delta > 0 ? 'comparison-delta--up' : 'comparison-delta--down'}>
                      {dimension.delta > 0 ? '+' : ''}
                      {dimension.delta.toFixed(3)}
                    </span>
                    <span>{dimension.pairs}</span>
                    <span>{dimension.missing}</span>
                  </div>
                ))}
              </div>
            )}
          </Card>

          {detail.report.risks.length > 0 ? (
            <Card className="console-card mb-3" bodyStyle={{ padding: 14 }} data-comparison-risks="true">
              <Text strong className="block mb-1">
                必须与结论一起看的事实
              </Text>
              {detail.report.risks.map((risk) => (
                <Text key={risk} type="tertiary" size="small" className="block">
                  · {risk}
                </Text>
              ))}
            </Card>
          ) : null}

          {/* 采用：只更新指针并给出扩量入口，不自动运行或发布。 */}
          {detail.report.comparability.comparable ? (
            <Card className="console-card" bodyStyle={{ padding: 14 }} data-adopt-panel="true">
              <Text strong className="block mb-2">
                采用哪一侧
              </Text>
              <div className="flex flex-wrap items-center gap-2">
                <Select
                  value={side}
                  style={{ width: 160 }}
                  aria-label="采用哪一侧"
                  optionList={[
                    { value: 'left', label: '左侧方案' },
                    { value: 'right', label: '右侧方案' },
                  ]}
                  onChange={(value) => setSide(value === 'left' ? 'left' : 'right')}
                />
                <Tag size="small">采用只更新项目采用指针，不会自动运行或发布</Tag>
              </div>
              <div className="mt-2">
                <Text type="tertiary" size="small" className="block mb-1">
                  依据（必填）
                </Text>
                <TextArea
                  value={reason}
                  onChange={(value) => setReason(value)}
                  autosize={{ minRows: 2, maxRows: 4 }}
                  placeholder="例如：右侧在准确维度更好且成本相近；样本量只够作方向性参考"
                  data-field="adopt-reason"
                />
              </div>
              {adoptError ? (
                <div className="wizard-field__error mt-1" role="alert">
                  {adoptError}
                </div>
              ) : null}
              <div className="mt-2">
                <Button theme="solid" type="primary" loading={busy} onClick={() => void adopt()}>
                  采用并规划扩量
                </Button>
              </div>
              {nextStep ? (
                <div className="mt-2 flex items-center gap-2" data-adopt-next-step="true">
                  <Text size="small">已采用。下一步：</Text>
                  <Button
                    size="small"
                    icon={<ArrowRight size={13} />}
                    onClick={() => navigate(nextStep.href)}
                  >
                    {nextStep.label}
                  </Button>
                </div>
              ) : null}
            </Card>
          ) : (
            // 不可比时**不提供**采用入口（这句话本身也是提示）。
            <Card className="console-card" bodyStyle={{ padding: 14 }} data-adopt-blocked="true">
              <Text type="tertiary" size="small">
                该比较不可比，因此不提供采用入口。请先修正比较前提
                （固定输入问题版本、确保两侧都有评分）后重新比较。
              </Text>
            </Card>
          )}
        </>
      )}
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
