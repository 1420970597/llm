import { useEffect, useState } from 'react'
import { studioApi } from '../lib/api/studio'

// ProjectLayout 与全局壳会同时需要名称。只复用进行中的请求，避免打开项目
// 时为同一资源发两次 GET P；请求完成后不保留跨会话缓存，避免项目改名或
// 退出登录后把旧项目名称带到下一位用户的壳里。
const projectNameRequests = new Map<number, Promise<string>>()

function requestProjectName(projectId: number): Promise<string> {
  const pending = projectNameRequests.get(projectId)
  if (pending) return pending

  const request = studioApi.getProject(projectId).then((envelope) => {
    const name = envelope.data.name.trim()
    if (name === '') throw new Error('项目名称为空')
    return name
  })
  projectNameRequests.set(projectId, request)
  // 请求完成后从去重表移除；失败请求允许下一次进入页面重试。
  void request.then(
    () => projectNameRequests.delete(projectId),
    () => projectNameRequests.delete(projectId),
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
export function useProjectName(projectId: number): string | undefined {
  const [state, setState] = useState<{ projectId: number; name?: string }>(() => ({
    projectId,
  }))

  useEffect(() => {
    let cancelled = false
    setState({ projectId })
    if (projectId <= 0) return () => {
      cancelled = true
    }

    void requestProjectName(projectId)
      .then((name) => {
        if (!cancelled) setState({ projectId, name })
      })
      .catch(() => {
        // 页面主体会独立呈现加载错误；壳层保留中性「项目」占位，避免
        // 将资源 ID 或后端内部错误泄漏到导航标题。
        if (!cancelled) setState({ projectId })
      })

    return () => {
      cancelled = true
    }
  }, [projectId])

  return state.projectId === projectId ? state.name : undefined
}
