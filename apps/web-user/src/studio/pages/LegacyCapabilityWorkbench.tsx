import { useCallback, useEffect, useState } from 'react'
import { useNavigate } from 'react-router-dom'
import { Button, Card, Modal, Spin, Tag, Typography } from '@douyinfe/semi-ui'
import { AlertTriangle, ArrowRight, RefreshCw } from 'lucide-react'
import { consoleApi } from '../../lib/api'
import { studioApi } from '../../lib/api/studio'
import type { ProjectOverviewData } from '../../lib/api/studio'
import { useProjectScope } from '../ProjectLayout'
import { projectHref } from '../StudioLayout'

/**
 * 原数据能力迁移面板（T31）。
 *
 * 旧 API 写入口在迁移期可被冻结，因此旧数据只做只读来源索引。新建内容、
 * 运行、评估和发布统一下钻至项目 API；不把 projectId 冒充 datasetId，亦不
 * 通过旧 dataset API 写入新项目。
 */

export type LegacyCapabilitySurface = 'all' | 'design' | 'production' | 'data' | 'quality' | 'release'

type CoverageStatus = 'native' | 'legacy-readonly' | 'legacy-tool' | 'not-migrated'

type CapabilityRow = {
  key: string
  group: string
  capability: string
  status: CoverageStatus
  statusLabel: string
  detail: string
  target?: string
  targetLabel?: string
  historyTab?: 'structure' | 'samples' | 'artifacts'
  legacyOperation?: 'structure' | 'task' | 'questions' | 'reasoning' | 'rewards' | 'grpo' | 'exports'
}

type LegacyDataset = {
  id: number
  name: string
  status: string
  targetKind: string
  createdAt: string
  updatedAt: string
}

const CAPABILITY_ROWS: CapabilityRow[] = [
  { key: 'blueprint', group: '设计', capability: '项目蓝图与配置版本', status: 'native', statusLabel: 'Atelier 原生', detail: '新项目在蓝图中维护生成 schema、质量策略与格式映射；这是新的项目级配置模型。', target: 'project.blueprint', targetLabel: '打开设计' },
  { key: 'legacy-graph', group: '设计', capability: '旧领域图谱与生成批次记录', status: 'legacy-readonly', statusLabel: '旧数据只读', detail: '旧图谱、方向和生成批次可查历史；迁移不会把旧批次伪装成 Atelier run。', target: 'legacy-history', targetLabel: '查看历史结构', historyTab: 'structure' },
  { key: 'legacy-graph-write', group: '设计', capability: '旧主题图谱生成、重命名、逐项审核与确认', status: 'not-migrated', statusLabel: '未等价迁移', detail: 'Atelier 蓝图并不复刻旧 domain graph 的生成与审核语义；旧版操作单独保留并受服务端冻结策略控制。', target: 'project.blueprint', targetLabel: '查看新蓝图', legacyOperation: 'structure' },
  { key: 'directions', group: '设计', capability: '旧方向生成、恢复与按域编辑', status: 'not-migrated', statusLabel: '未等价迁移', detail: 'Atelier 用覆盖矩阵定位缺口并规划新批次；旧方向生成、恢复或按域编辑只在显式旧版工作台提供。', target: 'project.coverage', targetLabel: '查看覆盖矩阵', legacyOperation: 'task' },
  { key: 'standards', group: '设计', capability: '长链思维标准编辑与版本', status: 'native', statusLabel: 'Atelier 原生', detail: '项目级思维标准支持编辑并追加新版本；旧 dataset 标准及历史版本仍只读保留。', target: 'project.standard', targetLabel: '打开思维标准' },
  { key: 'legacy-standards', group: '设计', capability: '旧长链标准逐域配置与版本记录', status: 'legacy-readonly', statusLabel: '旧数据只读', detail: '旧标准记录可追溯，但旧的 per-domain 标准和版本不自动转成项目标准版本。', target: 'legacy-history', targetLabel: '查看历史结构', historyTab: 'structure' },
  { key: 'legacy-standard-write', group: '设计', capability: '旧长链标准逐域编辑', status: 'not-migrated', statusLabel: '未等价迁移', detail: 'Atelier 的单一项目级标准不是逐 domain 编辑器；旧版逐域操作保留为显式兼容入口。', target: 'project.standard', targetLabel: '查看项目标准', legacyOperation: 'task' },
  { key: 'questions', group: '生产', capability: '问题生成与覆盖规划', status: 'native', statusLabel: 'Atelier 原生', detail: '新问题在独立项目批次内生成，以 sample version 记录来源和历史。', target: 'project.runs', targetLabel: '打开生产批次' },
  { key: 'legacy-questions', group: '历史数据', capability: '旧问题及问题详情', status: 'legacy-readonly', statusLabel: '旧数据只读', detail: '旧问题可查；T31 只把旧问题和最新 SFT reasoning/answer 追加为样本版本，无内容的问题会跳过。', target: 'legacy-history', targetLabel: '查看旧样本', historyTab: 'samples', legacyOperation: 'questions' },
  { key: 'difficulty', group: '质量', capability: '旧问题难度统计', status: 'not-migrated', statusLabel: '未等价迁移', detail: '项目覆盖矩阵不是旧 difficulty-stats 的等价统计；旧难度统计从显式旧任务工作台读取。', legacyOperation: 'task' },
  { key: 'reasoning', group: '历史数据', capability: '旧 reasoning 与 SFT 记录', status: 'legacy-readonly', statusLabel: '旧数据只读', detail: '历史内容可查。T31 仅导入最新 SFT reasoning/answer；独立 reasoning 记录和其余 SFT 版本不转成样本版本。', target: 'legacy-history', targetLabel: '查看旧样本', historyTab: 'samples', legacyOperation: 'reasoning' },
  { key: 'sft-generation', group: '生产', capability: '新 SFT 思维链与答案生成', status: 'native', statusLabel: 'Atelier 原生', detail: '新内容由 SFT 项目批次生成并追加版本；导入快照与真实生成批次在来源上可区分。', target: 'project.runs', targetLabel: '打开生产批次' },
  { key: 'review', group: '质量', capability: '样本审阅、接纳与隔离判断', status: 'native', statusLabel: 'Atelier 原生', detail: '围绕固定 sample version 追加判断，不覆盖样本原文。', target: 'project.review', targetLabel: '打开审阅队列' },
  { key: 'evaluation', group: '质量', capability: '旧多模型评估、维度与裁判报告', status: 'legacy-tool', statusLabel: '旧工具兼容', detail: '完整评估工作台仍以旧 dataset 为上下文；项目质量实验不是它的一对一替代。旧写操作是否开放由服务端冻结配置决定。', target: 'tools.evaluation', targetLabel: '打开评估工作台' },
  { key: 'cleaning', group: '质量', capability: '旧关键词配置、清洗扫描与命中报告', status: 'legacy-tool', statusLabel: '旧工具兼容', detail: '完整清洗工具仍以旧 dataset 为上下文；项目规则页仅做纯预览，不替代旧清洗历史。旧写操作是否开放由服务端冻结配置决定。', target: 'tools.cleaning', targetLabel: '打开清洗工作台' },
  { key: 'rewards', group: '历史数据', capability: '旧 reward 评分记录', status: 'legacy-readonly', statusLabel: '旧数据只读', detail: '旧奖励分可查，但不会迁移成质量实验分数或改变新项目样本状态。', target: 'legacy-history', targetLabel: '查看旧样本', historyTab: 'samples', legacyOperation: 'rewards' },
  { key: 'grpo', group: '质量', capability: 'GRPO 训练样本与量表', status: 'native', statusLabel: 'Atelier 原生', detail: '新 GRPO 样本由 GRPO 项目批次生成，需在项目方案中配置 levels/rubrics；旧 GRPO prompt 不会迁移成新样本。', target: 'project.runs', targetLabel: '打开生产批次' },
  { key: 'legacy-grpo', group: '历史数据', capability: '旧 GRPO prompt 与生成记录', status: 'legacy-readonly', statusLabel: '旧数据只读', detail: '旧 prompts 与运行记录保留在历史库，不会改写成新的 GRPO payload；旧提示词生成仍在兼容工作台提供。', target: 'legacy-history', targetLabel: '查看旧样本', historyTab: 'samples', legacyOperation: 'grpo' },
  { key: 'export', group: '交付', capability: '旧 dataset 导出制品与下载', status: 'legacy-readonly', statusLabel: '旧制品只读', detail: '旧 artifact 可继续查阅/下载；交付库只列已发布 Atelier release，不包含旧 artifact 或发布候选。', target: 'legacy-history', targetLabel: '查阅旧制品', historyTab: 'artifacts', legacyOperation: 'exports' },
  { key: 'release', group: '交付', capability: '发布候选、不可变 manifest 与交付', status: 'native', statusLabel: 'Atelier 原生', detail: '按项目冻结样本版本、处理阻塞并发布；与旧 dataset 导出不是同一对象。', target: 'project.releases', targetLabel: '打开发布' },
]

const SURFACE_ROWS: Record<LegacyCapabilitySurface, string[]> = {
  all: CAPABILITY_ROWS.map((row) => row.key),
  design: ['blueprint', 'legacy-graph', 'legacy-graph-write', 'directions', 'standards', 'legacy-standards', 'legacy-standard-write'],
  production: ['questions', 'sft-generation'],
  data: ['legacy-questions', 'reasoning', 'rewards', 'legacy-grpo', 'export', 'review'],
  quality: ['difficulty', 'review', 'evaluation', 'cleaning', 'grpo', 'rewards'],
  release: ['export', 'release'],
}

const STATUS_TAG: Record<CoverageStatus, 'blue' | 'grey' | 'orange'> = {
  native: 'blue',
  'legacy-readonly': 'grey',
  'legacy-tool': 'orange',
  'not-migrated': 'orange',
}

const { Title, Text } = Typography

export function LegacyCapabilityWorkbench({ surface = 'all' }: { surface?: LegacyCapabilitySurface }) {
  const scope = useProjectScope()
  const navigate = useNavigate()
  const [overview, setOverview] = useState<ProjectOverviewData | null>(null)
  const [legacyDataset, setLegacyDataset] = useState<LegacyDataset | null>(null)
  const [loading, setLoading] = useState(true)
  const [refreshing, setRefreshing] = useState(false)
  const [error, setError] = useState<string | null>(null)

  const load = useCallback(async (initial = false) => {
    if (initial) setLoading(true)
    else setRefreshing(true)
    setError(null)
    try {
      const result = await studioApi.overview(scope.projectId)
      setOverview(result)
      if (result.legacyDatasetId) {
        try {
          const response = await consoleApi.getDataset(result.legacyDatasetId)
          setLegacyDataset(response.dataset)
        } catch {
          // A failed legacy read should not hide the successfully loaded project coverage.
          setLegacyDataset(null)
        }
      } else {
        setLegacyDataset(null)
      }
    } catch (loadError) {
      setError(loadError instanceof Error ? loadError.message : '加载项目能力失败')
      setOverview(null)
      setLegacyDataset(null)
    } finally {
      if (initial) setLoading(false)
      else setRefreshing(false)
    }
  }, [scope.projectId])

  useEffect(() => {
    void load(true)
  }, [load])

  if (loading) {
    return <Card className="console-card" data-legacy-workbench="loading"><Spin tip="正在加载项目能力" /></Card>
  }

  const legacyId = overview?.legacyDatasetId ?? null
  const overviewStats = overview?.stats
  const routeFor = (row: CapabilityRow) => {
    if (row.target === 'legacy-history') {
      const path = legacyId ? `/legacy/history/${legacyId}` : '/legacy/history'
      return legacyId && row.historyTab ? `${path}?tab=${row.historyTab}` : path
    }
    if (row.target === 'tools.evaluation') return `/tools/evaluation${legacyId ? `?datasetId=${legacyId}` : ''}`
    if (row.target === 'tools.cleaning') return `/tools/cleaning${legacyId ? `?datasetId=${legacyId}` : ''}`
    if (row.target === 'deliveries') return '/deliveries'
    return projectHref(row.target ?? 'project.overview', scope.projectId)
  }
  const legacyOperationPath = (operation: NonNullable<CapabilityRow['legacyOperation']>) => {
    if (!legacyId) return '/console/tasks'
    const query = `?taskId=${legacyId}`
    switch (operation) {
      case 'structure': return `/console/domains/legacy${query}`
      case 'task': return `/console/tasks/${legacyId}/legacy`
      case 'questions': return `/console/questions/legacy${query}`
      case 'reasoning': return `/console/reasoning/legacy${query}`
      case 'rewards': return `/console/rewards/legacy${query}`
      case 'grpo': return `/console/tasks/${legacyId}/legacy`
      case 'exports': return `/console/exports/legacy${query}`
    }
  }
  const openLegacyOperation = (operation: NonNullable<CapabilityRow['legacyOperation']>) => {
    const path = legacyOperationPath(operation)
    if (!legacyId) {
      navigate(path)
      return
    }
    Modal.confirm({
      title: '打开旧版兼容操作？',
      content: `你将离开项目能力矩阵并打开旧数据集 #${legacyId} 的兼容工作台。旧版操作可能创建或修改旧数据，是否能提交由服务端 LEGACY_WRITES_FROZEN 配置决定。`,
      okText: '打开兼容操作',
      cancelText: '留在项目工作区',
      onOk: () => navigate(path),
    })
  }
  const visibleRows = SURFACE_ROWS[surface]
    .map((key) => CAPABILITY_ROWS.find((row) => row.key === key))
    .filter((row): row is CapabilityRow => Boolean(row))
  const groups = [...new Set(visibleRows.map((row) => row.group))]

  return (
    <section className="legacy-capability-workbench" data-legacy-workbench="true" data-legacy-dataset-id={legacyId ?? undefined}>
      <div className="legacy-capability-workbench__header">
        <div>
          <div className="eyebrow">PROJECT / CAPABILITIES</div>
          <Title heading={5} className="!mb-1">能力覆盖与迁移边界</Title>
          <Text type="tertiary" size="small">逐项区分项目原生能力、旧数据只读兼容与尚未等价迁移的缺口；状态不代表仅有相似名称。</Text>
        </div>
        <Button size="small" icon={<RefreshCw size={14} />} loading={refreshing} onClick={() => void load(false)}>刷新</Button>
      </div>

      {error ? (
        <div className="legacy-capability-workbench__error" role="alert">
          <AlertTriangle size={16} aria-hidden />
          <Text type="danger">{error}</Text>
          <Button size="small" onClick={() => void load(false)}>重试</Button>
        </div>
      ) : null}

      {legacyId ? (
        <div className="legacy-capability-workbench__unbound" data-legacy-source="true">
          <Tag color="grey">只读历史来源</Tag>
          <Text>
            {legacyDataset?.name ?? `旧数据集 #${legacyId}`} · 数据集 #{legacyId} · {legacyDataset?.targetKind?.toUpperCase() ?? overview?.targetKind?.toUpperCase()} · {legacyDataset?.status ?? '状态未知'}
          </Text>
          <Text type="tertiary" size="small">旧 dataset 写入口是否冻结由服务端 LEGACY_WRITES_FROZEN 配置控制；新生成只进入 Atelier 批次。历史资产页只读 GET，可查看旧非 SFT 记录和 artifact。</Text>
        </div>
      ) : (
        <div className="legacy-capability-workbench__unbound" data-legacy-source="none">
          <Tag color="blue">Atelier 原生项目</Tag>
          <Text type="tertiary">无旧数据集映射；历史数据入口不会拿项目 ID 代替 dataset ID。适用于此项目的只有下方标记为 Atelier 原生的能力。</Text>
        </div>
      )}

      {overviewStats ? (
        <div className="legacy-capability-summary" aria-label="项目能力状态">
          <span>计划 {overviewStats.plannedQuestions} 题</span>
          <span>已生成 {overviewStats.generated} 版本</span>
          <span>待判断 {overviewStats.pendingReview}</span>
          <span>已接纳 {overviewStats.accepted}</span>
          <span>已隔离 {overviewStats.quarantined}</span>
        </div>
      ) : null}

      <div className="legacy-capability-legend" aria-label="能力状态图例">
        <span><i className="is-native" /> Atelier 原生</span>
        <span><i className="is-readonly" /> 旧数据只读</span>
        <span><i className="is-legacy-tool" /> 旧工具兼容（受服务端冻结策略控制）</span>
        <span><i className="is-gap" /> 未等价迁移</span>
      </div>

      <div className="legacy-capability-matrix" role="table" aria-label="旧能力覆盖矩阵">
        <div className="legacy-capability-matrix__head" role="row">
          <span role="columnheader">能力域</span><span role="columnheader">旧能力 / 替代能力</span><span role="columnheader">迁移状态与边界</span><span role="columnheader">入口</span>
        </div>
        {groups.map((group) => (
          <div className="legacy-capability-group" role="rowgroup" key={group} data-capability-group={group}>
            {visibleRows.filter((row) => row.group === group).map((row) => (
              <div className="legacy-capability-matrix__row" role="row" key={row.key} data-capability-status={row.status} data-capability-key={row.key}>
                <span className="legacy-capability-matrix__group" role="cell">{row.group}</span>
                <span className="legacy-capability-matrix__capability" role="cell">{row.capability}</span>
                <span className="legacy-capability-matrix__detail" role="cell">
                  <Tag size="small" color={STATUS_TAG[row.status]}>{row.statusLabel}</Tag>
                  <span>{row.detail}</span>
                </span>
                <span className="legacy-capability-matrix__action" role="cell">
                  {row.target && row.targetLabel ? (
                    <Button size="small" theme="borderless" type="tertiary" icon={<ArrowRight size={14} />} onClick={() => navigate(routeFor(row))}>
                      {row.targetLabel}
                    </Button>
                  ) : <Text type="tertiary" size="small">暂无等价入口</Text>}
                  {row.legacyOperation ? (
                    <Button size="small" theme="borderless" type="tertiary" onClick={() => openLegacyOperation(row.legacyOperation as NonNullable<CapabilityRow['legacyOperation']>)}>
                      {legacyId ? (row.legacyOperation === 'grpo' ? '旧 GRPO 操作' : '旧版操作') : '选择旧数据集'}
                    </Button>
                  ) : null}
                </span>
              </div>
            ))}
          </div>
        ))}
      </div>
    </section>
  )
}
