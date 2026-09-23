import { useEffect, useState } from 'react'
import { projectNumericId, studioApi } from '../lib/api/studio'
import type { ProjectResourceId } from '../lib/api/studio'

// ProjectLayout 与全局壳会同时需要名称。只复用进行中的请求，避免打开项目
// 时为同一资源发两次 GET P；请求完成后不保留跨会话缓存，避免项目改名或
// 退出登录后把旧项目名称带到下一位用户的壳里。
const projectNameRequests = new Map<string, Promise<string>>()

function requestProjectName(projectId: ProjectResourceId): Promise<string> {
  const key = String(projectId)
  const pending = projectNameRequests.get(key)
  if (pending) return pending

  const request = studioApi.getProject(projectId).then((envelope) => {
    const name = envelope.data.name.trim()
    if (name === '') throw new Error('项目名称为空')
    return name
  })
  projectNameRequests.set(key, request)
  // 请求完成后从去重表移除；失败请求允许下一次进入页面重试。
  void request.then(
    () => projectNameRequests.delete(key),
    () => projectNameRequests.delete(key),
  )
  return request
}

/**
 * 读取项目显示名称。
 *
 * `undefined` 表示仍在读取或当前用户无法读取项目详情；调用方应显示中性
 * 占位，而不是把路由 ID 当作项目名称。项目 ID 为 0 时不发请求，便于全局
 * 壳在非项目路由上安全使用这个 hook。
 */
export function useProjectName(projectId: ProjectResourceId | null | undefined): string | undefined {
  const projectKey = projectId == null ? '' : String(projectId)
  const [state, setState] = useState<{ projectKey: string; name?: string }>(() => ({
    projectKey,
  }))

  useEffect(() => {
    let cancelled = false
    setState({ projectKey })
    if (projectId == null || projectNumericId(projectId) == null) return () => {
      cancelled = true
    }

    void requestProjectName(projectId)
      .then((name) => {
        if (!cancelled) setState({ projectKey, name })
      })
      .catch(() => {
        // 页面主体会独立呈现加载错误；壳层保留中性「项目」占位，避免
        // 将资源 ID 或后端内部错误泄漏到导航标题。
        if (!cancelled) setState({ projectKey })
      })

    return () => {
      cancelled = true
    }
  }, [projectId, projectKey])

  return state.projectKey === projectKey ? state.name : undefined
}
