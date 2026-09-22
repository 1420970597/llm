import { useCallback, useEffect, useState } from 'react'
import { useNavigate } from 'react-router-dom'
import { Button, Card, Empty, Input, Spin, Tag, Typography } from '@douyinfe/semi-ui'
import { AlertTriangle, CirclePlus, RefreshCw } from 'lucide-react'
import { client } from '../../lib/api'
import { studioApi } from '../../lib/api/studio'
import type { ProjectOverviewData } from '../../lib/api/studio'
import { useProjectScope } from '../ProjectLayout'
import { projectHref } from '../StudioLayout'

/**
 * 项目列表页（Issue #160 T09 的入口页 + T10 的最小可用形态）。
 *
 * T09 只需要它把「数据项目」这个全局入口变成可直达的页面；搜索、分页与
 * 草稿恢复属于 T10。这里刻意**只做真实数据**：不显示任何演示数字，
 * 空列表就是空列表（T09 验收项「未实现模块只显示诚实能力状态」的同一原则）。
 */

type ProjectEnvelope = {
  id: string
  status: string
  revision: number
  updatedAt: string
  capabilities: { canEdit: boolean; canRun: boolean; canPublish: boolean }
  data: {
    id: number
    name: string
    goal: string
    targetKind: string
    status: string
    domainCount: number
    directionsPerDomain: number
    questionsPerDirection: number
    pilotSize: number
  }
}

type PageEnvelope<T> = {
  items: T[]
  nextCursor: string
  sortKey: string
}

export function ProjectsPage() {
  const navigate = useNavigate()
  const { Title, Text } = Typography
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)
  const [projects, setProjects] = useState<ProjectEnvelope[]>([])
  // 搜索与分页都走**服务端**（T10 验收项「项目列表服务端搜索/分页」）：
  // 前端过滤会让「搜索结果数量」与真实数量不一致，而用户会据此判断
  // 项目是否被删除。
  const [search, setSearch] = useState('')
  // nextCursor 是「下一页的游标」；当前页不需要单独保存（翻页只往后走，
  // 前向游标由上一页的响应给出）。多存一个 cursor 状态会让两个值有机会
  // 不一致，而那种不一致表现为「加载更多加载出重复内容」。
  const [nextCursor, setNextCursor] = useState('')
  const [pageIndex, setPageIndex] = useState(1)

  const load = useCallback(
    async (query: string, pageCursor: string, append: boolean) => {
      setLoading(true)
      setError(null)
      try {
        const params = new URLSearchParams({ limit: '20' })
        if (query.trim() !== '') params.set('q', query.trim())
        if (pageCursor !== '') params.set('cursor', pageCursor)
        const response = await client.get<PageEnvelope<ProjectEnvelope>>(
          `/v1/projects?${params.toString()}`,
        )
        const items = response.data.items ?? []
        setProjects((previous) => (append ? [...previous, ...items] : items))
        setNextCursor(response.data.nextCursor ?? '')
      } catch (loadError) {
        // 错误文案已经由拦截器本地化；这里只负责把它显示出来，
        // 并且**不**把失败伪装成「没有项目」—— 那会让用户以为数据丢了。
        setError(loadError instanceof Error ? loadError.message : '加载项目失败')
      } finally {
        setLoading(false)
      }
    },
    [],
  )

  useEffect(() => {
    setPageIndex(1)
    void load(search, '', false)
  }, [load, search])

  return (
    <div className="console-page" data-studio-page="projects">
      <div className="console-page__header">
        <div>
          <Title heading={4} className="!mb-1">
            数据项目
          </Title>
          <Text type="tertiary">按项目组织设计、运行与发布；一个项目可以有多个并行批次。</Text>
        </div>
        <div className="flex gap-2">
          <Input
            value={search}
            onChange={(value) => setSearch(value)}
            placeholder="按名称或目标搜索"
            style={{ width: 220 }}
            aria-label="搜索项目"
          />
          <Button
            icon={<RefreshCw size={14} />}
            onClick={() => void load(search, '', false)}
            disabled={loading}
          >
            刷新
          </Button>
          <Button
            theme="solid"
            type="primary"
            icon={<CirclePlus size={14} />}
            onClick={() => navigate('/new')}
          >
            新建项目
          </Button>
        </div>
      </div>

      {loading ? (
        <div className="flex justify-center py-10">
          <Spin tip="正在加载项目" />
        </div>
      ) : error ? (
        <Card className="console-card">
          <div className="flex items-start gap-2">
            <AlertTriangle size={18} className="mt-1 text-amber-500" aria-hidden />
            <div>
              <Text strong className="block">
                项目列表加载失败
              </Text>
              <Text type="tertiary">{error}</Text>
              <div className="mt-3">
                <Button size="small" onClick={() => void load(search, '', false)}>
                  重试
                </Button>
              </div>
            </div>
          </div>
        </Card>
      ) : projects.length === 0 ? (
        <Card className="console-card">
          <Empty description="还没有数据项目。创建第一个项目只建立草稿，不会调用模型，也不要求已配置连接。" />
        </Card>
      ) : (
        <div className="project-card-grid">
          {projects.map((project) => (
            // 用 button 而不是给 Card 加 onClick：Card 不接受 onClick，
            // 而给它套一层 div+onClick 会丢掉键盘可达性（Tab/Enter 不可用）。
            // T09 验收项要求「键盘焦点」可测，因此这里必须是真实的可聚焦控件。
            <button
              type="button"
              key={project.id}
              className="console-card project-card project-card--button"
              onClick={() => navigate(projectHref('project.overview', project.data.id))}
              aria-label={`打开项目 ${project.data.name}`}
            >
              <div className="flex items-start justify-between gap-2">
                <Text strong>{project.data.name}</Text>
                <Tag size="small" color={project.data.targetKind === 'grpo' ? 'violet' : 'blue'}>
                  {project.data.targetKind === 'grpo' ? 'GRPO' : 'SFT'}
                </Tag>
              </div>
              <Text type="tertiary" className="mt-1 block">
                {project.data.goal || '（未填写目标）'}
              </Text>
              <div className="mt-3 flex flex-wrap gap-3">
                <Text type="tertiary" size="small">
                  领域 {project.data.domainCount} · 每领域方向 {project.data.directionsPerDomain} · 每方向问题{' '}
                  {project.data.questionsPerDirection}
                </Text>
              </div>
              <div className="mt-2 flex items-center gap-2">
                <Tag size="small">{project.status}</Tag>
                {!project.capabilities.canRun ? (
                  <Tag size="small" color="grey">
                    只读
                  </Tag>
                ) : null}
              </div>
            </button>
          ))}
        </div>
      )}

      {nextCursor !== '' ? (
        <div className="mt-3 flex justify-center">
          <Button
            onClick={() => {
              // 游标分页：只追加，不重新拉第一页 —— 那会在并发写入下
              // 重复显示同一批项目（契约 §1.5 的「翻页无重复无遗漏」）。
              setPageIndex((index) => index + 1)
              void load(search, nextCursor, true)
            }}
            loading={loading}
          >
            加载更多（第 {pageIndex + 1} 页）
          </Button>
        </div>
      ) : null}
    </div>
  )
}

/**
 * 项目概览页（P01）。
 *
 * 只用真实数据填充，三条与契约 §3.1 相关的规则体现在这里：
 *
 *  1. 计划量与实际产出**分列**显示，不合成「完成度」；
 *  2. 接纳率在分母为 0 时显示「无结论」而不是 100%（文案来自服务端）；
 *  3. 「下一决定」由服务端按事实与权限给出，前端不自己猜。
 */
export function ProjectOverviewPage() {
  const scope = useProjectScope()
  const navigate = useNavigate()
  const { Text } = Typography
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)
  const [overview, setOverview] = useState<ProjectOverviewData | null>(null)

  const load = useCallback(async () => {
    setLoading(true)
    setError(null)
    try {
      const response = await studioApi.overview(scope.projectId)
      setOverview(response)
    } catch (loadError) {
      setError(loadError instanceof Error ? loadError.message : '加载项目概览失败')
    } finally {
      setLoading(false)
    }
  }, [scope.projectId])

  useEffect(() => {
    void load()
  }, [load])

  if (loading) {
    return (
      <div className="flex justify-center py-10">
        <Spin tip="正在加载概览" />
      </div>
    )
  }

  if (error || !overview) {
    return (
      <Card className="console-card" data-studio-page="overview-error">
        <Text strong className="block">
          概览加载失败
        </Text>
        <Text type="tertiary">{error ?? '未知错误'}</Text>
        <div className="mt-3">
          <Button size="small" onClick={() => void load()}>
            重试
          </Button>
        </div>
      </Card>
    )
  }

  return (
    <div className="console-page" data-studio-page="overview">
      <Card className="console-card mb-3" bodyStyle={{ padding: 16 }}>
        <Text strong className="block">
          下一决定
        </Text>
        <Text type="tertiary" className="block mt-1">
          {overview.nextAction.message}
        </Text>
        <div className="mt-3">
          <Button size="small" theme="solid" type="primary" onClick={() => navigate(overview.nextAction.href)}>
            前往
          </Button>
        </div>
      </Card>

      <div className="console-stat-grid">
        <StatTile label="计划问题数（n×m×x）" value={String(overview.stats.plannedQuestions)} hint="这是计划量，不是已产出" />
        <StatTile label="已生成样本版本" value={String(overview.stats.generated)} hint="按内容版本计，重生成会产生新版本" />
        <StatTile label="纳入检查" value={String(overview.stats.inspected)} hint="由质量实验冻结的范围决定（T14）" />
        <StatTile
          label="接纳率"
          value={overview.stats.acceptanceRateDisplay}
          hint="分母为 0 时显示「无结论」，不是 100%"
        />
      </div>

      <div className="console-stat-grid">
        <StatTile label="批次总数" value={String(overview.batches.total)} hint={`试制 ${overview.batches.pilot} · 扩量 ${overview.batches.scale}`} />
        <StatTile label="运行中" value={String(overview.batches.running)} hint="含排队与暂停请求中" />
        <StatTile label="失败/部分失败" value={String(overview.batches.failed)} hint="成功内容已保留，可只恢复失败项" />
        <StatTile
          label="预算占用"
          value={formatMinor(overview.budget.settledMinor + overview.budget.uncertainMinor + overview.budget.reservedMinor)}
          hint={overview.budget.limitMinor > 0 ? `上限 ${formatMinor(overview.budget.limitMinor)}` : '未设上限'}
        />
      </div>

      <Card className="console-card" bodyStyle={{ padding: 16 }}>
        <Text strong className="block mb-2">
          版本
        </Text>
        <div className="flex flex-wrap gap-3">
          {[
            ['蓝图', overview.versions.blueprint],
            ['覆盖', overview.versions.coverage],
            ['标准', overview.versions.standard],
            ['质量策略', overview.versions.qualityPolicy],
            ['映射', overview.versions.mapping],
          ].map(([label, summary]) => {
            const version = summary as ProjectOverviewData['versions']['blueprint']
            return (
              <div key={String(label)} className="flex items-center gap-2">
                <Text type="tertiary" size="small">
                  {String(label)}
                </Text>
                {version ? (
                  <Tag size="small">v{version.version}</Tag>
                ) : (
                  <Tag size="small" color="grey">
                    未保存
                  </Tag>
                )}
              </div>
            )
          })}
        </div>
      </Card>
    </div>
  )
}

function StatTile({ label, value, hint }: { label: string; value: string; hint: string }) {
  const { Text } = Typography
  return (
    <Card className="console-card" bodyStyle={{ padding: 14 }}>
      <Text type="tertiary" size="small" className="block">
        {label}
      </Text>
      <div className="console-stat-value">{value}</div>
      <Text type="tertiary" size="small" className="block">
        {hint}
      </Text>
    </Card>
  )
}

/**
 * 金额显示。
 *
 * 后端一律用整数最小货币单位（分）传输（契约 §2.4）。前端**只在展示时**
 * 转成元，且不使用浮点运算做累加 —— 这里的输入已经是服务端算好的整数和。
 */
function formatMinor(minor: number): string {
  const negative = minor < 0
  const value = Math.abs(minor)
  const yuan = Math.floor(value / 100)
  const cents = String(value % 100).padStart(2, '0')
  return `${negative ? '-' : ''}${yuan}.${cents} 元`
}
