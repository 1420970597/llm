import { useEffect, useMemo, useState } from 'react'
import { Link } from 'react-router-dom'
import { Card, Tag, Typography } from '@douyinfe/semi-ui'
import { ArrowUpRight, ShieldCheck } from 'lucide-react'
import { client } from '../../lib/api'
import { legacyRouteGroups, legacyRoutes } from '../legacyCapabilities'

/**
 * Transitional directory for capabilities that still live in the legacy
 * console. It keeps the migration reversible without making the old shell a
 * competing top-level navigation tree.
 */
export function LegacyCapabilitiesPage() {
  const { Title, Text } = Typography
  const [isAdmin, setIsAdmin] = useState(false)

  useEffect(() => {
    let cancelled = false
    void client.get<{ user?: { role?: string } }>('/v1/auth/me').then((response) => {
      if (!cancelled) setIsAdmin(response.data?.user?.role === 'admin')
    }).catch(() => {
      // The authenticated shell already owns session errors; keep admin links hidden.
    })
    return () => {
      cancelled = true
    }
  }, [])

  const visibleRoutes = useMemo(
    () => legacyRoutes.filter((route) => !route.adminOnly || isAdmin),
    [isAdmin],
  )

  return (
    <div className="console-page" data-studio-page="legacy-capabilities">
      <div className="console-page__header">
        <div>
          <Title heading={4} className="!mb-1">兼容功能</Title>
          <Text type="tertiary">Atelier 迁移期间仍可使用的旧版入口。新项目请优先使用数据项目工作区。</Text>
        </div>
        <Tag color="grey" prefixIcon={<ShieldCheck size={13} />}>Atelier 原生索引</Tag>
      </div>

      {legacyRouteGroups.map((group) => {
        const entries = visibleRoutes.filter((route) => route.group === group.key)
        return (
          <section key={group.key} className="mb-4" data-legacy-group={group.key}>
            <div className="mb-2 flex items-center gap-2">
              <Title heading={5} className="!mb-0">{group.label}</Title>
              <Text type="tertiary" size="small">{entries.length} 个入口</Text>
            </div>
            <div className="console-card-grid-2">
              {entries.map((entry) => (
                <Card key={entry.key} className="console-card" bodyStyle={{ padding: 16 }}>
                  <div className="flex items-start justify-between gap-3">
                    <div>
                      <Text strong className="block">{entry.label}</Text>
                      <Text type="tertiary" size="small">{entry.caption}</Text>
                      <Text type="tertiary" size="small" className="block mt-1">目标：{entry.route}</Text>
                    </div>
                    <Tag size="small" color="grey">{entry.status === 'compat' ? '兼容旧版' : 'Atelier 原生'}</Tag>
                  </div>
                  {entry.note ? <Text type="tertiary" size="small" className="block mt-3">{entry.note}</Text> : null}
                  <div className="mt-3 flex flex-wrap items-center gap-x-4 gap-y-2">
                    {entry.nativeHref ? (
                      <Link className="console-link inline-flex items-center gap-1" to={entry.nativeHref}>
                        在 Atelier 中打开 <ArrowUpRight size={14} aria-hidden />
                      </Link>
                    ) : null}
                    <Link className="console-link inline-flex items-center gap-1" to={entry.indexHref ?? entry.route}>
                      {entry.visibility === 'detail' ? '进入我的任务' : entry.indexHref ? '先选择任务' : '打开兼容入口'} <ArrowUpRight size={14} aria-hidden />
                    </Link>
                  </div>
                </Card>
              ))}
            </div>
          </section>
        )
      })}
    </div>
  )
}
