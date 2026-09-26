import { useEffect, useState } from 'react'
import { useSearchParams } from 'react-router-dom'
import type { ReactNode } from 'react'
import { Button, Card, Spin, Typography } from '@douyinfe/semi-ui'
import { RefreshCw } from 'lucide-react'
import { consoleApi, type Dataset } from '../../lib/api'
import { settingsApi } from '../../lib/api/studio'
import type { ProjectListItem } from '../../lib/api/studio'
import { CleaningView, type CleaningNavigation } from '../../views/CleaningView'
import { EvaluationView } from '../../views/EvaluationView'

const { Title, Text } = Typography

/**
 * 旧控制台的评估/清洗实现已经有完整的真实 API 链路（L8-L14）。
 *
 * 这些页面只负责把数据集上下文接到 Atelier 的辅助层：不复制业务状态，
 * 也不伪造项目数据。这样旧版深链和新入口共享同一套运行、报告及重试语义，
 * 后续替换视觉层时也不会出现两套清洗/评估结果。
 */
function useLegacyDatasets() {
  const [datasets, setDatasets] = useState<Dataset[]>([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)

  const load = async () => {
    setLoading(true)
    setError(null)
    try {
      setDatasets(await consoleApi.listDatasets())
    } catch (loadError) {
      setError(loadError instanceof Error ? loadError.message : '加载数据项目失败')
    } finally {
      setLoading(false)
    }
  }

  useEffect(() => {
    void load()
  }, [])

  return { datasets, loading, error, reload: load }
}

/**
 * 工作台与当前蓝图的连接（issue #197 第 9 条）。
 *
 * 缺陷形态：评估工作台与清洗工作台以「旧数据集」为单位，字面上不出现
 * 「蓝图 / 思维标准」，用户看不出这些工具与自己的项目是什么关系 ——
 * 两个页面成了信息孤岛。
 *
 * 修复方式不是把旧链路改掉（那会破坏 L8–L14 已验收的真实链路），
 * 而是在页头**明确说明归属与边界**，并给出通往蓝图/项目的入口：
 *   1. 说清「这页处理的是旧数据集（导入前的历史资产），不是新项目的批次」；
 *   2. 给出可点的项目入口，让用户能回到 Atelier 主线；
 *   3. 项目若绑定过旧数据集（`legacyDatasetId`），直接把它接到本页上下文。
 */
function LegacyToolFrame({
  title,
  description,
  children,
  projectLinks = [],
}: {
  title: string
  description: string
  children: ReactNode
  projectLinks?: Array<{ name: string; href: string; legacyDatasetId: number }>
}) {
  const [linksOpen, setLinksOpen] = useState(false)
  return (
    <div className="console-page" data-studio-page="legacy-tool">
      <div className="console-page__header">
        <div>
          <Title heading={4} className="!mb-1">{title}</Title>
          <Text type="tertiary">{description}</Text>
        </div>
        <Text type="tertiary" size="small">Atelier 辅助工作台 · 真实数据</Text>
      </div>
      <Card className="console-card mb-3" bodyStyle={{ padding: 14 }} data-tool-scope="true">
        <Text strong size="small" className="block">
          这个工作台处理的是什么
        </Text>
        <Text type="tertiary" size="small" className="block mt-1">
          本页以<strong>旧数据集</strong>为单位（迁移前的历史资产），不读取项目的蓝图快照。
          新项目的质量结论请用项目内的「质量」页 —— 那里的实验会冻结蓝图、
          覆盖、标准与素材的版本，因此结论可复现。
        </Text>
        {projectLinks.length > 0 ? (
          <div className="mt-2">
            <Button
              size="small"
              theme="borderless"
              onClick={() => setLinksOpen((current) => !current)}
              data-tool-project-links-toggle="true"
            >
              {linksOpen ? '收起相关项目' : `查看 ${projectLinks.length} 个相关项目 →`}
            </Button>
            {linksOpen ? (
              <ul className="review-evidence mt-2" data-tool-project-links="true">
                {projectLinks.map((link) => (
                  <li key={link.href}>
                    <a href={link.href}>{link.name}</a>
                    <Text type="tertiary" size="small" className="block">
                      已绑定旧数据集 #{link.legacyDatasetId}
                    </Text>
                  </li>
                ))}
              </ul>
            ) : null}
          </div>
        ) : (
          <Text type="tertiary" size="small" className="block mt-2">
            还没有项目绑定过旧数据集。迁移旧资产后，这里会列出对应的项目。
          </Text>
        )}
      </Card>
      {children}
    </div>
  )
}

/** 读取「绑定了旧数据集」的项目，用于把工作台接回 Atelier 主线。 */
function useLegacyLinkedProjects() {
  const [links, setLinks] = useState<Array<{ name: string; href: string; legacyDatasetId: number }>>([])
  useEffect(() => {
    let cancelled = false
    void (async () => {
      try {
        const page = await settingsApi.projects({ limit: 50 })
        if (cancelled) return
        setLinks(
          (page.items ?? [])
            .filter((project: ProjectListItem) => Number(project.data?.legacyDatasetId ?? 0) > 0)
            .map((project: ProjectListItem) => ({
              name: project.data.name || `项目 #${project.data.id}`,
              href: `/p/${project.data.id}/overview`,
              legacyDatasetId: Number(project.data.legacyDatasetId),
            })),
        )
      } catch {
        // 读不到相关项目不影响本页主功能（旧数据集的评估/清洗链路独立可用），
        // 因此这里静默降级为「没有相关项目」，而不是把整页变成错误态。
        if (!cancelled) setLinks([])
      }
    })()
    return () => {
      cancelled = true
    }
  }, [])
  return links
}

function DatasetLoadingState({
  loading,
  error,
  onReload,
}: {
  loading: boolean
  error: string | null
  onReload: () => void
}) {
  if (loading) {
    return (
      <div className="atelier-inline-state" data-tool-loading="true">
        <Spin tip="正在加载数据项目" />
      </div>
    )
  }
  if (error) {
    return (
      <Card className="console-card" bodyStyle={{ padding: 18 }} data-tool-error="true">
        <Text type="danger" className="block">{error}</Text>
        <Button className="mt-3" size="small" icon={<RefreshCw size={14} />} onClick={onReload}>重试</Button>
      </Card>
    )
  }
  return null
}

/** Atelier 原生入口：多模型评估、维度管理、运行与报告。 */
export function EvaluationToolPage() {
  const { datasets, loading, error, reload } = useLegacyDatasets()
  const projectLinks = useLegacyLinkedProjects()
  const [searchParams] = useSearchParams()
  const requestedDatasetId = Number(searchParams.get('datasetId'))
  const initialDatasetId = Number.isSafeInteger(requestedDatasetId) && requestedDatasetId > 0 ? requestedDatasetId : null
  return (
    <LegacyToolFrame
      title="评估工作台"
      description="从评估维度与裁判配置，到运行进度、逐条证据和最终报告，完整保留原有质量评估能力。"
      projectLinks={projectLinks}
    >
      <DatasetLoadingState loading={loading} error={error} onReload={() => void reload()} />
      {!loading && !error ? <EvaluationView datasets={datasets} initialDatasetId={initialDatasetId} /> : null}
    </LegacyToolFrame>
  )
}

/** Atelier 原生入口：关键词/规则配置、异步扫描与命中报告。 */
export function CleaningToolPage() {
  const { datasets, loading, error, reload } = useLegacyDatasets()
  const projectLinks = useLegacyLinkedProjects()
  const [searchParams] = useSearchParams()
  const requestedDatasetId = Number(searchParams.get('datasetId'))
  const initialDatasetId = Number.isSafeInteger(requestedDatasetId) && requestedDatasetId > 0 ? requestedDatasetId : null
  const navigation: CleaningNavigation = {
    planning: '/new',
    tasks: '/projects',
    evaluation: '/tools/evaluation',
    cleaning: '/tools/cleaning',
    results: '/deliveries',
    home: '/today',
  }
  return (
    <LegacyToolFrame
      title="清洗工作台"
      description="配置拒答关键词和清洗规则，发起真实扫描，查看命中证据并把干净结果送往交付。"
      projectLinks={projectLinks}
    >
      <DatasetLoadingState loading={loading} error={error} onReload={() => void reload()} />
      {!loading && !error ? <CleaningView datasets={datasets} initialDatasetId={initialDatasetId} navigation={navigation} /> : null}
    </LegacyToolFrame>
  )
}
