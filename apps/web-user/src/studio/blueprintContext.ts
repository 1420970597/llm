import { useCallback, useEffect, useState } from 'react'
import { client } from '../lib/api'
import { projectPath, type ProjectResourceId } from '../lib/api/studio'
import type { VersionedDocument } from './DocumentEditors'

/** Read the immutable blueprint selected by a workbench deep link. */
export function useBlueprintContext(projectId: ProjectResourceId | undefined, requestedId: string | null) {
  const [versions, setVersions] = useState<VersionedDocument[]>([])
  const [current, setCurrent] = useState<VersionedDocument | null>(null)
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [retry, setRetry] = useState(0)
  const reload = useCallback(() => setRetry((value) => value + 1), [])
  useEffect(() => {
    let cancelled = false
    setCurrent(null); setVersions([]); setError(null)
    if (!projectId) { setLoading(false); return }
    setLoading(true)
    void (async () => {
      try {
        type VersionPage = { items: VersionedDocument[]; nextCursor?: string }
        let response = await client.get<VersionPage>(`${projectPath(projectId)}/blueprint-versions?limit=50`)
        if (cancelled) return
        const items = [...(response.data.items ?? [])]
        const cursors = new Set<string>()
        while (requestedId && !items.some((item) => String(item.id) === requestedId) && response.data.nextCursor) {
          const cursor = response.data.nextCursor
          if (cursors.has(cursor)) throw new Error('蓝图版本分页未能推进，请稍后重试。')
          cursors.add(cursor)
          response = await client.get<VersionPage>(`${projectPath(projectId)}/blueprint-versions?limit=50&cursor=${encodeURIComponent(cursor)}`)
          if (cancelled) return
          items.push(...(response.data.items ?? []))
        }
        setVersions(items)
        const selected = requestedId ? items.find((item) => String(item.id) === requestedId) : items[0]
        if (requestedId && !selected) throw new Error('所选蓝图版本不在当前项目中，请重新选择。')
        if (selected) {
          const detail = await client.get<{ version: VersionedDocument }>(`${projectPath(projectId)}/blueprint-versions/${selected.version}`)
          if (!cancelled) setCurrent(detail.data.version)
        }
      } catch (loadError) {
        if (!cancelled) setError(loadError instanceof Error ? loadError.message : '蓝图读取失败')
      } finally { if (!cancelled) setLoading(false) }
    })()
    return () => { cancelled = true }
  }, [projectId, requestedId, retry])
  return { versions, current, loading, error, reload }
}
