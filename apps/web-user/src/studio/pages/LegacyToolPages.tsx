import { useEffect, useState } from 'react'
import { useSearchParams } from 'react-router-dom'
import type { ReactNode } from 'react'
import { Button, Card, Spin, Typography } from '@douyinfe/semi-ui'
import { RefreshCw } from 'lucide-react'
import { consoleApi, type Dataset } from '../../lib/api'
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

function LegacyToolFrame({
  title,
  description,
  children,
}: {
  title: string
  description: string
  children: ReactNode
}) {
  return (
    <div className="console-page" data-studio-page="legacy-tool">
      <div className="console-page__header">
        <div>
          <Title heading={4} className="!mb-1">{title}</Title>
          <Text type="tertiary">{description}</Text>
        </div>
        <Text type="tertiary" size="small">Atelier 辅助工作台 · 真实数据</Text>
      </div>
      {children}
    </div>
  )
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
  const [searchParams] = useSearchParams()
  const requestedDatasetId = Number(searchParams.get('datasetId'))
  const initialDatasetId = Number.isSafeInteger(requestedDatasetId) && requestedDatasetId > 0 ? requestedDatasetId : null
  return (
    <LegacyToolFrame
      title="评估工作台"
      description="从评估维度与裁判配置，到运行进度、逐条证据和最终报告，完整保留原有质量评估能力。"
    >
      <DatasetLoadingState loading={loading} error={error} onReload={() => void reload()} />
      {!loading && !error ? <EvaluationView datasets={datasets} initialDatasetId={initialDatasetId} /> : null}
    </LegacyToolFrame>
  )
}

/** Atelier 原生入口：关键词/规则配置、异步扫描与命中报告。 */
export function CleaningToolPage() {
  const { datasets, loading, error, reload } = useLegacyDatasets()
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
    >
      <DatasetLoadingState loading={loading} error={error} onReload={() => void reload()} />
      {!loading && !error ? <CleaningView datasets={datasets} initialDatasetId={initialDatasetId} navigation={navigation} /> : null}
    </LegacyToolFrame>
  )
}
