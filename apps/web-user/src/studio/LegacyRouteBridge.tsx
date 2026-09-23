import { useEffect } from 'react'
import { Navigate, useLocation, useNavigate, useParams, useSearchParams } from 'react-router-dom'
import { Card, Spin } from '@douyinfe/semi-ui'
import { client } from '../lib/api'
import { fillRoutePathByKey } from './routes'

/** Response returned by the T31 legacy dataset mapping endpoint. */
export type LegacyProjectMapping = {
  datasetId: number
  projectId?: number
  pagePath?: string
  migrationStatus: 'mapped' | 'not_mapped' | string
  message: string
}

/** Native Atelier destination for a legacy console route. */
export type LegacyRouteTarget =
  | 'project.overview'
  | 'project.blueprint'
  | 'project.data'
  | 'project.quality'
  | 'project.rules'
  | 'project.releases'

export type LegacyRouteBridgeProps = {
  datasetId: number | null | undefined
  /** Optional query string from the old detail URL. */
  search?: string
}

const HISTORY_TAB_BY_TARGET: Record<LegacyRouteTarget, 'overview' | 'structure' | 'samples' | 'artifacts'> = {
  'project.overview': 'overview',
  'project.blueprint': 'structure',
  'project.data': 'samples',
  'project.quality': 'samples',
  'project.rules': 'samples',
  'project.releases': 'artifacts',
}

/**
 * Resolve an old task to its Atelier project destination.
 *
 * This is deliberately a read-only bridge. It never calls an old POST/PUT/
 * DELETE endpoint and it never treats a dataset id as a project id. A missing
 * mapping is a valid migration state and goes to the historical read-only
 * page, preserving the original dataset id for inspection.
 */
export function legacyHistoryPath(datasetId: number): string {
  return `/legacy/history/${encodeURIComponent(String(datasetId))}`
}

export function legacyHistoryPathForTarget(datasetId: number, target: LegacyRouteTarget): string {
  return `${legacyHistoryPath(datasetId)}?tab=${HISTORY_TAB_BY_TARGET[target]}`
}

export function nativePathForMapping(mapping: LegacyProjectMapping, target: LegacyRouteTarget): string | null {
  const projectId = mapping.projectId
  if (mapping.migrationStatus !== 'mapped' || typeof projectId !== 'number' || !Number.isSafeInteger(projectId) || projectId <= 0) return null
  return fillRoutePathByKey(target, { projectId })
}

function safeDatasetId(value: number | null | undefined): number | null {
  return Number.isSafeInteger(value) && (value ?? 0) > 0 ? value as number : null
}

/**
 * Route component used by the compatibility shell for task-detail deep links.
 * It renders a small loading state while the mapping is fetched, then replaces
 * the URL with either a native Atelier page or the read-only history page.
 */
export function LegacyRouteBridge({ datasetId: rawDatasetId, search }: LegacyRouteBridgeProps) {
  const navigate = useNavigate()
  const datasetId = safeDatasetId(rawDatasetId)

  useEffect(() => {
    let cancelled = false
    if (!datasetId) {
      navigate('/legacy/history', { replace: true })
      return () => {
        cancelled = true
      }
    }

    void client
      .get<LegacyProjectMapping>(`/v1/legacy/datasets/${datasetId}/project`)
      .then((response) => {
        if (cancelled) return
        const mapping = response.data
        if (mapping.datasetId !== datasetId) {
          navigate(legacyHistoryPath(datasetId), { replace: true })
          return
        }
        const nativePath = nativePathForMapping(mapping, 'project.overview')
        if (nativePath) {
          const params = new URLSearchParams(search ?? '')
          params.delete('taskId')
          params.set('legacyDatasetId', String(datasetId))
          const query = params.toString()
          navigate(query ? `${nativePath}?${query}` : nativePath, { replace: true })
          return
        }
        navigate(legacyHistoryPath(datasetId), { replace: true })
      })
      .catch((requestError: unknown) => {
        if (cancelled) return
        // A mapping read failure must not send users to an unrelated project.
        // Preserve the dataset in the read-only history view. The history page
        // performs its own mapping read and presents a recoverable error.
        navigate(legacyHistoryPath(datasetId), { replace: true, state: { bridgeError: requestError instanceof Error ? requestError.message : undefined } })
      })
    return () => {
      cancelled = true
    }
  }, [datasetId, navigate, search])

  return (
    <Card className="console-card" bodyStyle={{ padding: 20 }} data-legacy-route-bridge="loading">
      {datasetId ? <Spin tip="正在确认历史任务的项目映射" /> : <Spin tip="正在打开历史资产索引" />}
    </Card>
  )
}

/** Route wrappers keep taskId parsing in one place and reject malformed ids. */
export function LegacyTaskBridgeRoute() {
  const { taskId } = useParams()
  const location = useLocation()
  return <LegacyRouteBridge datasetId={parseRouteId(taskId)} search={location.search} />
}

export function LegacyStageBridgeRoute({ target }: { target: LegacyRouteTarget }) {
  const [searchParams] = useSearchParams()
  const datasetId = parseRouteId(searchParams.get('taskId') ?? undefined)
  if (!datasetId) return <Navigate to="/legacy/history" replace />
  return <Navigate to={legacyHistoryPathForTarget(datasetId, target)} replace />
}

function parseRouteId(raw: string | undefined): number | null {
  if (!raw || !/^\d+$/.test(raw)) return null
  const id = Number(raw)
  return Number.isSafeInteger(id) && id > 0 ? id : null
}
