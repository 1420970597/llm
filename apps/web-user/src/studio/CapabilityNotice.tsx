import { Card, Tag, Typography } from '@douyinfe/semi-ui'
import { AlertTriangle } from 'lucide-react'
import type { StudioRouteMeta } from './routes'

/**
 * 未实现能力的**诚实状态**（Issue #160 T09 验收项）。
 *
 * 契约原文：「未实现模块只显示诚实能力状态，不显示演示分数」。
 *
 * 为什么需要这个组件（而不是先做一个「大致能用」的页面）：
 * 一个填了假数据或空表的页面会让 M1 的退出门槛「未实现能力不能伪装可用」
 * 失效 —— 用户看到一张漂亮的空表格，无法区分「没有数据」与「功能没做」，
 * 于是会去报告「为什么没有数据」的 bug，而真正的事实是 T14 还没交付。
 *
 * 因此这里显式给出两件事：**这个模块做什么**、**由哪个任务交付**。
 */
export function CapabilityNotice({
  route,
}: {
  route: StudioRouteMeta
}) {
  const { Title, Text } = Typography
  return (
    <Card className="console-card" bodyStyle={{ padding: 24 }} data-studio-capability-notice={route.key}>
      <div className="flex items-start gap-3">
        <AlertTriangle size={20} className="mt-1 text-amber-500" aria-hidden />
        <div>
          <Title heading={5} className="!mb-1">
            {route.label}尚未交付
          </Title>
          <Text type="tertiary" className="block">
            {route.caption}
          </Text>
          <div className="mt-3 flex flex-wrap items-center gap-2">
            <Tag color="amber" size="small">
              由 {route.task} 交付
            </Tag>
            <Text type="tertiary" size="small">
              当前不提供该能力的任何数据与操作，以免把「功能未做」显示成「没有数据」。
            </Text>
          </div>
        </div>
      </div>
    </Card>
  )
}
