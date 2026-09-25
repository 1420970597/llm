import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import type { ReactNode } from 'react'
import { Link, useNavigate, useSearchParams } from 'react-router-dom'
import { Button, Card, Empty, Modal, Select, Spin, TabPane, Tabs, Tag, Toast, Typography } from '@douyinfe/semi-ui'
import {
  AlertTriangle,
  Archive,
  ChevronLeft,
  ChevronRight,
  CircleAlert,
  Copy,
  Database,
  Download,
  FileArchive,
  History,
  RefreshCw,
  ShieldCheck,
  TableProperties,
} from 'lucide-react'
import { client, consoleApi } from '../../lib/api'
import type {
  Artifact,
  ChainStandard,
  ChainStandardVersion,
  Dataset,
  DatasetGraph,
  Domain,
  GenerationRun,
  GrpoPrompt,
  Question,
  ReasoningRecord,
  RewardRecord,
  SftRecord,
} from '../../lib/api'
import { describeDatasetStatus } from '../../lib/datasetStatus'
import { describeDomainReviewStatus } from '../../lib/enumLabels'
import './LegacyHistoryPage.css'

  const { Title, Text } = Typography

/**
 * 历史数据集到 Atelier 项目的映射，只用于显示对象关联与安全项目链接。
 */
type LegacyProjectMapping = {
  datasetId: number
  projectId?: number
  pagePath?: string
  migrationStatus: 'mapped' | 'not_mapped' | string
  message: string
}

type ResourceKey =
  | 'graph'
  | 'directions'
  | 'runs'
  | 'standards'
  | 'questions'
  | 'reasoning'
  | 'rewards'
  | 'grpo'
  | 'sft'
  | 'artifacts'

type ResourceErrorMap = Partial<Record<ResourceKey, string>>

type LegacyHistoryPageProps = {
  /** Optional route-provided id (for `/legacy/history/:datasetId`). */
  datasetId?: number | null
  /** Keeps a host route's query/path state in sync without owning routing here. */
  onDatasetChange?: (datasetId: number) => void
  /** Optional route-provided tab key, typically from `?tab=artifacts`. */
  initialTab?: 'overview' | 'structure' | 'samples' | 'artifacts'
}

type ResourceResult = {
  key: ResourceKey
  value?: unknown
  error?: string
}

const RESOURCE_LABELS: Record<ResourceKey, string> = {
  graph: '领域图谱',
  directions: '方向',
  runs: '生成批次',
  standards: '长链标准',
  questions: '问题',
  reasoning: '推理记录',
  rewards: '奖励记录',
  grpo: 'GRPO 提示词',
  sft: 'SFT 样本',
  artifacts: '导出制品',
}

const SAMPLE_PAGE_SIZE = 12

function validDatasetId(value: number | null | undefined): number | null {
  return Number.isSafeInteger(value) && (value ?? 0) > 0 ? value as number : null
}

function normalizeHistoryTab(value: string | null | undefined): 'overview' | 'structure' | 'samples' | 'artifacts' {
  switch (value?.toLowerCase()) {
    case 'structure':
    case 'console':
    case 'domains':
    case 'directions':
    case 'runs':
    case 'standards':
      return 'structure'
    case 'samples':
    case 'questions':
    case 'reasoning':
    case 'rewards':
    case 'grpo':
    case 'sft':
      return 'samples'
    case 'artifacts':
    case 'exports':
    case 'export':
      return 'artifacts'
    default:
      return 'overview'
  }
}

function errorMessage(error: unknown, fallback: string): string {
  return error instanceof Error && error.message ? error.message : fallback
}

function formatDate(value?: string): string {
  if (!value) return '未记录'
  const parsed = new Date(value)
  return Number.isNaN(parsed.getTime()) ? value : parsed.toLocaleString('zh-CN', { hour12: false })
}

function compactText(value: string | undefined, limit = 180): string {
  if (!value) return '未记录'
  const normalized = value.replace(/\s+/g, ' ').trim()
  return normalized.length > limit ? `${normalized.slice(0, limit)}…` : normalized
}

function jsonText(value: unknown): string {
  try {
    return JSON.stringify(value, null, 2)
  } catch {
    return String(value)
  }
}

function StateCard({ message, onRetry }: { message: string; onRetry?: () => void }) {
  return (
    <Card className="legacy-history-card legacy-history-state" bodyStyle={{ padding: 18 }}>
      <CircleAlert size={18} aria-hidden />
      <div>
        <Text strong className="block">历史资料暂时不可用</Text>
        <Text type="tertiary" size="small">{message}</Text>
        {onRetry ? <Button size="small" className="mt-3" icon={<RefreshCw size={14} />} onClick={onRetry}>重试</Button> : null}
      </div>
    </Card>
  )
}

function ResourceError({ message }: { message: string }) {
  return (
    <div className="legacy-history-resource-error" role="alert">
      <AlertTriangle size={15} aria-hidden />
      <span>{message}</span>
    </div>
  )
}

function ResourceCountCard({
  resource,
  count,
  state,
  onOpen,
}: {
  resource: ResourceKey
  count: number | null
  state?: string
  onOpen: () => void
}) {
  return (
    <button type="button" className="legacy-history-count-card" onClick={onOpen}>
      <span className="legacy-history-count-card__label">{RESOURCE_LABELS[resource]}</span>
      <strong>{count === null ? '—' : count}</strong>
      <span className="legacy-history-count-card__state">{state ?? '已读取'}</span>
    </button>
  )
}

function PaginatedList<T>({
  items,
  empty,
  renderItem,
}: {
  items: T[]
  empty: string
  renderItem: (item: T, index: number) => ReactNode
}) {
  const [page, setPage] = useState(0)
  const pageCount = Math.max(1, Math.ceil(items.length / SAMPLE_PAGE_SIZE))
  const safePage = Math.min(page, pageCount - 1)
  const pageItems = items.slice(safePage * SAMPLE_PAGE_SIZE, (safePage + 1) * SAMPLE_PAGE_SIZE)

  useEffect(() => {
    setPage(0)
  }, [items])

  if (items.length === 0) return <Empty description={empty} />

  return (
    <>
      <div className="legacy-history-list">
        {pageItems.map((item, index) => renderItem(item, safePage * SAMPLE_PAGE_SIZE + index))}
      </div>
      {pageCount > 1 ? (
        <div className="legacy-history-pagination" aria-label="历史资料分页">
          <Text type="tertiary" size="small">第 {safePage + 1} / {pageCount} 页 · 共 {items.length} 条</Text>
          <div className="flex items-center gap-2">
            <Button
              size="small"
              theme="borderless"
              icon={<ChevronLeft size={14} />}
              disabled={safePage === 0}
              aria-label="上一页"
              onClick={() => setPage((current) => Math.max(0, current - 1))}
            />
            <Button
              size="small"
              theme="borderless"
              icon={<ChevronRight size={14} />}
              disabled={safePage >= pageCount - 1}
              aria-label="下一页"
              onClick={() => setPage((current) => Math.min(pageCount - 1, current + 1))}
            />
          </div>
        </div>
      ) : null}
    </>
  )
}

function HistoryRow({
  title,
  meta,
  body,
  raw,
}: {
  title: string
  meta?: string
  body?: string
  raw?: unknown
}) {
  return (
    <article className="legacy-history-row">
      <div className="legacy-history-row__heading">
        <Text strong>{title}</Text>
        {meta ? <Text type="tertiary" size="small">{meta}</Text> : null}
      </div>
      {body ? <Text className="legacy-history-row__body">{body}</Text> : null}
      {raw !== undefined ? (
        <details className="legacy-history-row__details">
          <summary>查看原始字段</summary>
          <pre>{jsonText(raw)}</pre>
        </details>
      ) : null}
    </article>
  )
}

function MappingBoundary({
  mapping,
  error,
}: {
  mapping: LegacyProjectMapping | null
  error: string | null
}) {
  const mappedHref = mapping?.pagePath?.startsWith('/p/') ? mapping.pagePath : null
  return (
    <section className="legacy-history-boundary" data-migration-boundary="readonly">
      <div className="legacy-history-boundary__icon"><ShieldCheck size={18} aria-hidden /></div>
      <div className="legacy-history-boundary__copy">
        <div className="flex flex-wrap items-center gap-2">
        <Text strong>历史数据集</Text>
          <Tag size="small" color={mapping?.migrationStatus === 'mapped' ? 'blue' : 'grey'}>
            {mapping?.migrationStatus === 'mapped' ? '已关联项目' : '待关联'}
          </Tag>
        </div>
        {error ? <Text type="danger" size="small" className="block mt-1">映射状态读取失败：{error}</Text> : null}
        {mapping ? (
          <Text type="tertiary" size="small" className="block mt-1">
            {mapping.migrationStatus === 'mapped' ? '项目已建立关联。' : '尚未建立项目关联。'}
            {mapping.projectId && mappedHref ? (
              <> · <Link className="console-link" to={mappedHref}>打开项目 #{mapping.projectId}</Link></>
            ) : null}
          </Text>
        ) : null}
      </div>
    </section>
  )
}

function OverviewPanel({
  dataset,
  graph,
  data,
  errors,
  onOpenResource,
}: {
  dataset: Dataset | null
  graph: DatasetGraph | null
  data: HistoryData
  errors: ResourceErrorMap
  onOpenResource: (resource: ResourceKey) => void
}) {
  const resources: Array<{ key: ResourceKey; count: number | null }> = [
    { key: 'graph', count: graph ? graph.domains.length : null },
    { key: 'directions', count: data.directions.length },
    { key: 'runs', count: data.runs.length },
    { key: 'standards', count: data.standards.length },
    { key: 'questions', count: data.questions.length },
    { key: 'reasoning', count: data.reasoning.length },
    { key: 'rewards', count: data.rewards.length },
    { key: 'grpo', count: data.grpo.length },
    { key: 'sft', count: data.sft.length },
    { key: 'artifacts', count: data.artifacts.length },
  ]
  return (
    <div className="legacy-history-overview">
      <Card className="legacy-history-card legacy-history-dataset-card" bodyStyle={{ padding: 20 }}>
        <div className="legacy-history-section-heading">
          <div>
            <div className="eyebrow">LEGACY DATASET / READ ONLY</div>
            <Title heading={5} className="!mb-1">{dataset?.name ?? graph?.dataset.name ?? '历史数据集'}</Title>
            <Text type="tertiary" size="small">数据集 #{dataset?.id ?? graph?.dataset.id ?? '—'} · 最后更新 {formatDate(dataset?.updatedAt ?? graph?.dataset.updatedAt)}</Text>
          </div>
          <Tag color="grey">{(dataset?.targetKind ?? graph?.dataset.targetKind ?? 'unknown').toUpperCase()}</Tag>
        </div>
        <div className="legacy-history-meta-grid">
          <div><span>状态</span><strong>{describeDatasetStatus(dataset?.status ?? graph?.dataset.status ?? '').label}</strong></div>
          <div><span>根关键词</span><strong>{dataset?.rootKeyword || '未记录'}</strong></div>
          <div><span>目标规模</span><strong>{dataset?.targetSize ?? '未记录'}</strong></div>
          <div><span>创建时间</span><strong>{formatDate(dataset?.createdAt ?? graph?.dataset.createdAt)}</strong></div>
        </div>
      </Card>
      <div className="legacy-history-count-grid">
        {resources.map(({ key, count }) => (
          <ResourceCountCard
            key={key}
            resource={key}
            count={count}
            state={errors[key] ? '读取失败' : '已读取'}
            onOpen={() => onOpenResource(key)}
          />
        ))}
      </div>
      {dataset?.failureReason ? (
        <Card className="legacy-history-card" bodyStyle={{ padding: 18 }}>
          <Text strong className="block">历史失败原因</Text>
          <Text type="danger" size="small">{dataset.failureReason}</Text>
        </Card>
      ) : null}
    </div>
  )
}

function StructurePanel({
  graph,
  directions,
  runs,
  standards,
  errors,
  datasetId,
}: {
  graph: DatasetGraph | null
  directions: Domain[]
  runs: GenerationRun[]
  standards: ChainStandard[]
  errors: ResourceErrorMap
  datasetId: number
}) {
  const [versionsFor, setVersionsFor] = useState<number | null>(null)
  const [versions, setVersions] = useState<ChainStandardVersion[]>([])
  const [versionsLoading, setVersionsLoading] = useState(false)
  const [versionsError, setVersionsError] = useState<string | null>(null)

  const loadVersions = async (domainId: number) => {
    if (versionsFor === domainId) {
      setVersionsFor(null)
      return
    }
    setVersionsFor(domainId)
    setVersions([])
    setVersionsError(null)
    setVersionsLoading(true)
    try {
      setVersions(await consoleApi.listChainStandardVersions(datasetId, domainId))
    } catch (error) {
      setVersionsError(errorMessage(error, '加载标准版本失败'))
    } finally {
      setVersionsLoading(false)
    }
  }

  return (
    <div className="legacy-history-stack">
      <Card className="legacy-history-card" bodyStyle={{ padding: 18 }}>
        <div className="legacy-history-section-heading"><Title heading={6}>领域与方向</Title><Tag size="small">{(graph?.domains.length ?? 0) + directions.length} 条</Tag></div>
        {errors.graph ? <ResourceError message={errors.graph} /> : null}
        {errors.directions ? <ResourceError message={errors.directions} /> : null}
        <div className="legacy-history-table-wrap">
          <table className="legacy-history-table">
            <thead><tr><th>层级</th><th>名称</th><th>来源</th><th>状态</th><th>编号</th></tr></thead>
            <tbody>
              {[...(graph?.domains ?? []), ...directions].map((domain) => (
                <tr key={`${domain.level}-${domain.id}`}>
                  <td>{domain.level === 1 ? '领域' : '方向'}</td>
                  <td>{domain.name || domain.canonicalName || '未命名'}</td>
                  <td>{domain.source || '未记录'}</td>
                  <td><Tag size="small">{describeDomainReviewStatus(domain.reviewStatus ?? '')}</Tag></td>
                  <td>#{domain.id}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
        {!graph?.domains.length && !directions.length ? <Empty description="没有领域或方向历史记录" /> : null}
      </Card>

      <Card className="legacy-history-card" bodyStyle={{ padding: 18 }}>
        <div className="legacy-history-section-heading"><Title heading={6}>生成批次</Title><Tag size="small">{runs.length} 条</Tag></div>
        {errors.runs ? <ResourceError message={errors.runs} /> : null}
        <PaginatedList
          items={runs}
          empty="没有生成批次历史"
          renderItem={(run) => (
            <HistoryRow
              key={run.id}
              title={`批次 #${run.id} · ${run.stage || '未知阶段'}`}
              meta={`${run.status || '未知状态'} · ${run.doneUnits}/${run.totalUnits} 单元 · 尝试 ${run.attempts} 次 · 创建于 ${formatDate(run.createdAt)}`}
              body={run.errorSummary ? `失败摘要：${run.errorSummary}` : `更新时间：${formatDate(run.updatedAt)}`}
              raw={run}
            />
          )}
        />
      </Card>

      <Card className="legacy-history-card" bodyStyle={{ padding: 18 }}>
        <div className="legacy-history-section-heading"><Title heading={6}>长链标准与版本</Title><Tag size="small">{standards.length} 条</Tag></div>
        {errors.standards ? <ResourceError message={errors.standards} /> : null}
        <PaginatedList
          items={standards}
          empty="没有长链标准历史"
          renderItem={(standard) => (
            <article className="legacy-history-row" key={standard.id}>
              <div className="legacy-history-row__heading">
                <Text strong>{standard.domainName || `领域 #${standard.domainId}`} · {standard.directionKey || '未命名方向'}</Text>
                <Text type="tertiary" size="small">当前版本 v{standard.currentVersion} · {standard.status || '未知状态'} · 更新于 {formatDate(standard.updatedAt)}</Text>
              </div>
              <Text className="legacy-history-row__body">{standard.steps?.length ?? 0} 个步骤：{compactText(standard.steps?.map((step) => step.title).join(' → '), 220)}</Text>
              <Button size="small" theme="borderless" onClick={() => void loadVersions(standard.domainId)}>
                {versionsFor === standard.domainId ? '收起版本' : '查看版本历史'}
              </Button>
              {versionsFor === standard.domainId ? (
                <div className="legacy-history-nested">
                  {versionsLoading ? <Spin size="small" tip="正在加载版本" /> : null}
                  {versionsError ? <ResourceError message={versionsError} /> : null}
                  {!versionsLoading && !versionsError && versions.length === 0 ? <Text type="tertiary" size="small">没有单独记录的版本</Text> : null}
                  {versions.map((version) => (
                    <details key={version.id} className="legacy-history-version">
                      <summary>v{version.version} · {version.changeNote || '无变更说明'} · {formatDate(version.createdAt)}</summary>
                      <pre>{jsonText(version)}</pre>
                    </details>
                  ))}
                </div>
              ) : null}
            </article>
          )}
        />
      </Card>
    </div>
  )
}

function SamplesPanel({ data, errors }: { data: HistoryData; errors: ResourceErrorMap }) {
  return (
    <div className="legacy-history-stack">
      <SampleResource title="问题" resource="questions" items={data.questions} errors={errors} empty="没有问题历史" renderItem={(question) => (
        <HistoryRow key={question.id} title={`问题 #${question.id} · ${question.domainName || `领域 #${question.domainId}`}`} meta={`${question.difficulty || '未分级'} · ${question.cleaningStatus || '未清洗'} · ${formatDate(question.createdAt)}`} body={question.content} raw={question} />
      )} />
      <SampleResource title="推理记录" resource="reasoning" items={data.reasoning} errors={errors} empty="没有推理记录历史" renderItem={(item) => (
        <HistoryRow key={item.id} title={`推理 #${item.id} · 问题 #${item.questionId}`} meta={`${item.status || '未知状态'} · ${formatDate(item.createdAt)}`} body={`${compactText(item.questionText, 140)}\n${compactText(item.reasoning, 260)}`} raw={item} />
      )} />
      <SampleResource title="奖励记录" resource="rewards" items={data.rewards} errors={errors} empty="没有奖励记录历史" renderItem={(item) => (
        <HistoryRow key={item.id} title={`奖励 #${item.id} · 问题 #${item.questionId}`} meta={`分数 ${item.score} · ${item.status || '未知状态'} · ${formatDate(item.createdAt)}`} body={item.questionText} raw={item} />
      )} />
      <SampleResource title="GRPO 提示词" resource="grpo" items={data.grpo} errors={errors} empty="没有 GRPO 提示词历史" renderItem={(item) => (
        <HistoryRow key={item.id} title={`GRPO #${item.id} · 问题 #${item.questionId}`} meta={`${item.domainName || `领域 #${item.domainId}`} · ${item.status || '未知状态'} · ${formatDate(item.createdAt)}`} body={compactText(item.judgePrompt, 320)} raw={item} />
      )} />
      <SampleResource title="SFT 样本" resource="sft" items={data.sft} errors={errors} empty="没有 SFT 样本历史" renderItem={(item) => (
        <HistoryRow key={item.id} title={`SFT #${item.id} · 问题 #${item.questionId}`} meta={`${item.domainName || `领域 #${item.domainId}`} · ${item.status || '未知状态'} · ${formatDate(item.createdAt)}`} body={`问题：${compactText(item.questionText, 140)}\n答案：${compactText(item.answer, 260)}`} raw={item} />
      )} />
    </div>
  )
}

function SampleResource<T>({
  title,
  resource,
  items,
  errors,
  empty,
  renderItem,
}: {
  title: string
  resource: ResourceKey
  items: T[]
  errors: ResourceErrorMap
  empty: string
  renderItem: (item: T) => ReactNode
}) {
  return (
    <Card className="legacy-history-card" bodyStyle={{ padding: 18 }}>
      <div className="legacy-history-section-heading"><Title heading={6}>{title}</Title><Tag size="small">{items.length} 条</Tag></div>
      {errors[resource] ? <ResourceError message={errors[resource] as string} /> : null}
      <PaginatedList items={items} empty={empty} renderItem={(item) => renderItem(item)} />
    </Card>
  )
}

function ArtifactsPanel({
  artifacts,
  error,
  datasetId,
}: {
  artifacts: Artifact[]
  error?: string
  datasetId: number
}) {
  const [downloading, setDownloading] = useState<number | null>(null)
  const [downloadError, setDownloadError] = useState<string | null>(null)

  const copyObjectKey = async (objectKey: string) => {
    try {
      if (!navigator.clipboard?.writeText) throw new Error('clipboard unavailable')
      await navigator.clipboard.writeText(objectKey)
      Toast.success('文件标识已复制，可交给运维或下游下载')
    } catch {
      Toast.warning('复制失败，请手动记录文件标识后下载交付')
    }
  }

  const download = async (artifact: Artifact) => {
    setDownloading(artifact.id)
    setDownloadError(null)
    try {
      const response = await consoleApi.downloadArtifactBlob(datasetId, artifact.id)
      if (!(response.data instanceof Blob) || response.data.size === 0) {
        throw new Error('交付文件为空，请先确认该历史导出制品仍可用')
      }
      const url = URL.createObjectURL(response.data)
      const anchor = document.createElement('a')
      anchor.href = url
      anchor.download = consoleApi.artifactFileName(response.headers['content-disposition'], artifact.objectKey)
      document.body.appendChild(anchor)
      anchor.click()
      anchor.remove()
      window.setTimeout(() => URL.revokeObjectURL(url), 1000)
      Toast.success('历史制品下载已开始')
    } catch (errorValue) {
      setDownloadError(errorMessage(errorValue, '制品下载失败'))
    } finally {
      setDownloading(null)
    }
  }

  return (
    <Card className="legacy-history-card" bodyStyle={{ padding: 18 }}>
      <div className="legacy-history-section-heading">
        <div><Title heading={6}>导出制品</Title><Text type="tertiary" size="small" className="block">可下载已保存的导出对象。</Text></div>
        <Tag size="small" color="grey">{artifacts.length} 件</Tag>
      </div>
      {error ? <ResourceError message={error} /> : null}
      {downloadError ? <ResourceError message={downloadError} /> : null}
      {artifacts.length === 0 ? <Empty description="没有可下载的历史导出制品" /> : (
        <div className="legacy-history-table-wrap">
          <table className="legacy-history-table">
            <thead><tr><th>类型</th><th>对象键</th><th>格式</th><th>创建时间</th><th aria-label="操作" /></tr></thead>
            <tbody>
              {artifacts.map((artifact) => (
                <tr key={artifact.id}>
                  <td><Tag size="small" prefixIcon={<FileArchive size={12} />}>{artifact.artifactType || 'export'}</Tag></td>
                  <td className="legacy-history-object-key" title={artifact.objectKey}>
                    <span>{artifact.objectKey || '未记录'}</span>
                    {artifact.objectKey ? <Button size="small" theme="borderless" icon={<Copy size={13} />} aria-label="复制对象键" onClick={() => void copyObjectKey(artifact.objectKey)} /> : null}
                  </td>
                  <td>{artifact.contentType || '未知格式'}</td>
                  <td>{formatDate(artifact.createdAt)}</td>
                  <td><Button size="small" icon={<Download size={14} />} loading={downloading === artifact.id} onClick={() => void download(artifact)}>下载</Button></td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </Card>
  )
}

type HistoryData = {
  graph: DatasetGraph | null
  directions: Domain[]
  runs: GenerationRun[]
  standards: ChainStandard[]
  questions: Question[]
  reasoning: ReasoningRecord[]
  rewards: RewardRecord[]
  grpo: GrpoPrompt[]
  sft: SftRecord[]
  artifacts: Artifact[]
}

const EMPTY_HISTORY: HistoryData = {
  graph: null,
  directions: [],
  runs: [],
  standards: [],
  questions: [],
  reasoning: [],
  rewards: [],
  grpo: [],
  sft: [],
  artifacts: [],
}

export function LegacyHistoryPage({ datasetId: requestedDatasetId, onDatasetChange, initialTab }: LegacyHistoryPageProps) {
  const navigate = useNavigate()
  const [searchParams, setSearchParams] = useSearchParams()
  const [datasets, setDatasets] = useState<Dataset[]>([])
  const [selectedDatasetId, setSelectedDatasetId] = useState<number | null>(validDatasetId(requestedDatasetId))
  const [datasetLoading, setDatasetLoading] = useState(true)
  const [datasetError, setDatasetError] = useState<string | null>(null)
  const [historyLoading, setHistoryLoading] = useState(false)
  const [historyError, setHistoryError] = useState<string | null>(null)
  const [mapping, setMapping] = useState<LegacyProjectMapping | null>(null)
  const [mappingError, setMappingError] = useState<string | null>(null)
  const [data, setData] = useState<HistoryData>(EMPTY_HISTORY)
  const [errors, setErrors] = useState<ResourceErrorMap>({})
  const [activeTab, setActiveTab] = useState(() => normalizeHistoryTab(searchParams.get('tab') ?? initialTab))
  const [historyDatasetId, setHistoryDatasetId] = useState<number | null>(null)
  const historyRequestId = useRef(0)

  const loadDatasets = useCallback(async () => {
    setDatasetLoading(true)
    setDatasetError(null)
    try {
      const items = await consoleApi.listDatasets()
      setDatasets(items)
      setSelectedDatasetId((current) => {
        const requested = validDatasetId(requestedDatasetId)
        if (requested) return requested
        if (current && items.some((item) => item.id === current)) return current
        return items[0]?.id ?? null
      })
    } catch (error) {
      setDatasetError(errorMessage(error, '加载历史数据集失败'))
    } finally {
      setDatasetLoading(false)
    }
  }, [requestedDatasetId])

  useEffect(() => {
    void loadDatasets()
  }, [loadDatasets])

  useEffect(() => {
    const next = validDatasetId(requestedDatasetId)
    if (next && next !== selectedDatasetId) setSelectedDatasetId(next)
  }, [requestedDatasetId, selectedDatasetId])

  const requestedTab = normalizeHistoryTab(searchParams.get('tab') ?? initialTab)
  useEffect(() => {
    if (requestedTab !== activeTab) setActiveTab(requestedTab)
  }, [activeTab, requestedTab])

  const loadHistory = useCallback(async (id: number) => {
    const requestId = ++historyRequestId.current
    setHistoryLoading(true)
    setHistoryDatasetId(null)
    setHistoryError(null)
    setMapping(null)
    setMappingError(null)
    setErrors({})

    const resourceRequests: Array<{ key: ResourceKey; request: Promise<unknown> }> = [
      { key: 'graph', request: consoleApi.getDataset(id) },
      { key: 'directions', request: consoleApi.listDirections(id) },
      { key: 'runs', request: consoleApi.listGenerationRuns(id) },
      { key: 'standards', request: consoleApi.listChainStandards(id) },
      { key: 'questions', request: consoleApi.listQuestions(id) },
      { key: 'reasoning', request: consoleApi.listReasoning(id) },
      { key: 'rewards', request: consoleApi.listRewards(id) },
      { key: 'grpo', request: consoleApi.listGrpo(id) },
      { key: 'sft', request: consoleApi.listSft(id) },
      { key: 'artifacts', request: consoleApi.listArtifacts(id) },
    ]

    const [mappingResult, ...resourceResults] = await Promise.all([
      client.get<LegacyProjectMapping>(`/v1/legacy/datasets/${id}/project`).then((response) => response.data).catch((error) => ({ error: errorMessage(error, '读取迁移映射失败') })),
      ...resourceRequests.map(async ({ key, request }): Promise<ResourceResult> => {
        try {
          return { key, value: await request }
        } catch (error) {
          return { key, error: errorMessage(error, `读取${RESOURCE_LABELS[key]}失败`) }
        }
      }),
    ])

    if (requestId !== historyRequestId.current) return
    if ('error' in mappingResult) setMappingError(mappingResult.error)
    else setMapping(mappingResult)

    const nextData: HistoryData = { ...EMPTY_HISTORY }
    const nextErrors: ResourceErrorMap = {}
    for (const result of resourceResults) {
      if (result.error) {
        nextErrors[result.key] = result.error
        continue
      }
      switch (result.key) {
        case 'graph': nextData.graph = result.value as DatasetGraph; break
        case 'directions': nextData.directions = result.value as Domain[]; break
        case 'runs': nextData.runs = result.value as GenerationRun[]; break
        case 'standards': nextData.standards = result.value as ChainStandard[]; break
        case 'questions': nextData.questions = result.value as Question[]; break
        case 'reasoning': nextData.reasoning = result.value as ReasoningRecord[]; break
        case 'rewards': nextData.rewards = result.value as RewardRecord[]; break
        case 'grpo': nextData.grpo = result.value as GrpoPrompt[]; break
        case 'sft': nextData.sft = result.value as SftRecord[]; break
        case 'artifacts': nextData.artifacts = result.value as Artifact[]; break
      }
    }
    setData(nextData)
    setErrors(nextErrors)
    if (Object.keys(nextErrors).length === resourceRequests.length) setHistoryError('该数据集的历史资源均未能读取，请确认数据集仍存在或稍后重试。')
    setHistoryDatasetId(id)
    setHistoryLoading(false)
  }, [])

  useEffect(() => {
    if (selectedDatasetId) void loadHistory(selectedDatasetId)
    else {
      setData(EMPTY_HISTORY)
      setHistoryDatasetId(null)
      setMapping(null)
      setHistoryError(null)
    }
  }, [loadHistory, selectedDatasetId])

  const selectedDataset = useMemo(
    () => datasets.find((dataset) => dataset.id === selectedDatasetId) ?? data.graph?.dataset ?? null,
    [data.graph?.dataset, datasets, selectedDatasetId],
  )

  const selectDataset = (value: string | number | unknown[] | Record<string, unknown> | undefined) => {
    if (typeof value !== 'string' && typeof value !== 'number') return
    const next = validDatasetId(Number(value))
    if (!next) return
    setSelectedDatasetId(next)
    onDatasetChange?.(next)
    setActiveTab('overview')
    setSearchParams((params) => {
      params.set('tab', 'overview')
      return params
    })
  }

  const selectTab = (value: string | number | unknown[] | Record<string, unknown> | undefined) => {
    if (typeof value !== 'string' && typeof value !== 'number') return
    const next = normalizeHistoryTab(String(value))
    setActiveTab(next)
    setSearchParams((params) => {
      params.set('tab', next)
      return params
    })
  }

  const reload = () => {
    void loadDatasets()
    if (selectedDatasetId) void loadHistory(selectedDatasetId)
  }

  // 历史资产默认是只读的；任何旧版写操作都必须由用户显式确认，且始终
  // 带着当前 dataset id 进入兼容工作台，避免把历史对象误当成项目数据。
  const openLegacyOperations = () => {
    if (!selectedDatasetId) return
    const id = selectedDatasetId
    Modal.confirm({
      title: '打开旧版兼容操作？',
      content: `你将离开历史资产只读页并打开数据集 #${id} 的兼容工作台。旧版操作可能创建或修改旧数据，是否能提交由服务端 LEGACY_WRITES_FROZEN 配置决定。`,
      okText: '打开兼容操作',
      cancelText: '留在历史页',
      onOk: () => navigate(`/console/tasks/${id}/legacy?taskId=${id}`),
    })
  }


  const datasetOptions = useMemo(() => {
    if (!selectedDatasetId || datasets.some((dataset) => dataset.id === selectedDatasetId)) {
      return datasets.map((dataset) => ({ value: String(dataset.id), label: `#${dataset.id} ${dataset.name}` }))
    }
    return [
      { value: String(selectedDatasetId), label: `#${selectedDatasetId} 不在当前数据集索引中` },
      ...datasets.map((dataset) => ({ value: String(dataset.id), label: `#${dataset.id} ${dataset.name}` })),
    ]
  }, [datasets, selectedDatasetId])

  if (datasetLoading) {
    return <div className="legacy-history-page" data-studio-page="legacy-history"><div className="legacy-history-centered"><Spin tip="正在加载历史数据集" /></div></div>
  }

  if (datasetError) {
    return <div className="legacy-history-page" data-studio-page="legacy-history"><StateCard message={datasetError} onRetry={() => void loadDatasets()} /></div>
  }

  return (
    <div className="console-page legacy-history-page" data-studio-page="legacy-history">
      <div className="console-page__header legacy-history-page__header">
        <div>
          <div className="eyebrow"><History size={14} aria-hidden /> LEGACY / TRACE</div>
          <Title heading={4} className="!mb-1">历史资产</Title>
          <Text type="tertiary">查阅历史数据集、生成记录与导出制品。</Text>
        </div>
        <div className="legacy-history-toolbar">
          <Select
            value={selectedDatasetId ? String(selectedDatasetId) : undefined}
            placeholder="选择历史数据集"
            aria-label="选择历史数据集"
            style={{ minWidth: 260 }}
            optionList={datasetOptions}
            onChange={selectDataset}
          />
          <Button size="small" onClick={openLegacyOperations} disabled={!selectedDatasetId}>兼容操作</Button>
          <Button icon={<RefreshCw size={14} />} loading={historyLoading || datasetLoading} onClick={reload}>刷新</Button>
        </div>
      </div>

      {!datasets.length ? (
        <Card className="legacy-history-card"><Empty description="没有可查看的历史数据集" /></Card>
      ) : !selectedDatasetId ? (
        <Card className="legacy-history-card"><Empty description="请选择一个历史数据集" /></Card>
      ) : (
        <>
          <MappingBoundary mapping={mapping} error={mappingError} />
          {historyError ? <StateCard message={historyError} onRetry={() => void loadHistory(selectedDatasetId)} /> : null}
          {historyLoading || historyDatasetId !== selectedDatasetId ? (
            <div className="legacy-history-centered"><Spin tip="正在读取该数据集的历史资料" /></div>
          ) : (
          <Tabs activeKey={activeTab} onChange={(key) => selectTab(String(key))} type="line" className="legacy-history-tabs">
              <TabPane tab={<span><Archive size={14} /> 概览</span>} itemKey="overview">
                <OverviewPanel dataset={selectedDataset} graph={data.graph} data={data} errors={errors} onOpenResource={(key) => selectTab(key === 'graph' || key === 'directions' || key === 'runs' || key === 'standards' ? 'structure' : key === 'artifacts' ? 'artifacts' : 'samples')} />
              </TabPane>
              <TabPane tab={<span><Database size={14} /> 结构</span>} itemKey="structure">
                <StructurePanel graph={data.graph} directions={data.directions} runs={data.runs} standards={data.standards} errors={errors} datasetId={selectedDatasetId} />
              </TabPane>
              <TabPane tab={<span><TableProperties size={14} /> 样本</span>} itemKey="samples">
                <SamplesPanel data={data} errors={errors} />
              </TabPane>
              <TabPane tab={<span><FileArchive size={14} /> 制品</span>} itemKey="artifacts">
                <ArtifactsPanel artifacts={data.artifacts} error={errors.artifacts} datasetId={selectedDatasetId} />
              </TabPane>
            </Tabs>
          )}
        </>
      )}
    </div>
  )
}
