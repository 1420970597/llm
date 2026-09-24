import { Button, Card, Typography } from '@douyinfe/semi-ui'
import { ArrowLeft, CircleAlert, Search } from 'lucide-react'
import { useLocation, useNavigate } from 'react-router-dom'

const { Title, Text } = Typography

/**
 * 统一处理 SPA 中「路由不存在」与「设计工具未在生产挂载」两类状态。
 *
 * 不能把未知地址重定向到首页：那会把拼写错误、过期深链接与真实首页
 * 混在一起，用户和监控都无法判断地址是否有效。原始 pathname 保留在页面上，
 * 便于复制链接的人直接核对路径。
 */
export function RouteStatusPage() {
  const location = useLocation()
  const navigate = useNavigate()
  const normalizedPath = location.pathname.replace(/\/+$/, '') || '/'
  const isCatalog = normalizedPath === '/catalog'

  return (
    <main className="route-status-page" data-route-status={isCatalog ? 'catalog-unmounted' : 'not-found'}>
      <Card className="console-panel route-status-page__card" bodyStyle={{ padding: 32 }}>
        <div className="route-status-page__icon" aria-hidden>
          <CircleAlert size={24} />
        </div>
        <Text className="eyebrow">{isCatalog ? '生产环境能力边界' : '404 · 页面不存在'}</Text>
        <Title heading={2} className="!mb-2">
          {isCatalog ? '目录评审未在生产环境挂载' : '这个页面不存在'}
        </Title>
        <Text type="tertiary" className="route-status-page__description">
          {isCatalog
            ? '目录评审是设计与验收工具，仅在开发构建中提供。当前生产环境没有这个入口，也不会伪装成今日工作。'
            : '链接可能已过期、路径可能拼写错误，或者该页面已被移除。'}
        </Text>
        <div className="route-status-page__path" aria-label="访问路径">
          <span>你访问的是</span>
          <code>{`${location.pathname}${location.search}${location.hash}`}</code>
        </div>
        <div className="route-status-page__actions">
          <Button
            theme="solid"
            type="primary"
            icon={<ArrowLeft size={15} />}
            onClick={() => navigate('/today')}
          >
            返回今日工作
          </Button>
          <Button
            theme="light"
            icon={<Search size={15} />}
            onClick={() => navigate('/projects')}
          >
            搜索数据项目
          </Button>
        </div>
      </Card>
    </main>
  )
}
