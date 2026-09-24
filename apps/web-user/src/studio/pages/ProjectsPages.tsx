import { useCallback, useEffect, useRef, useState } from 'react'
import { useNavigate, useSearchParams } from 'react-router-dom'
import { Button, Card, Empty, Input, Spin, Tag, Typography } from '@douyinfe/semi-ui'
import { AlertTriangle, CirclePlus, RefreshCw } from 'lucide-react'
import { client } from '../../lib/api'
import { parseProjectResourceId, studioApi } from '../../lib/api/studio'
import type { ProjectOverviewData } from '../../lib/api/studio'
import { useProjectScope } from '../ProjectLayout'
import { projectHref } from '../StudioLayout'
import { useProjectName } from '../projectName'
import { LegacyCapabilityWorkbench } from './LegacyCapabilityWorkbench'

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
  const [searchParams] = useSearchParams()
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
  const autoOpenedTarget = useRef('')
  const targetRequest = useRef(0)
  const [targetRetry, setTargetRetry] = useState(0)
  const [targetError, setTargetError] = useState<string | null>(null)

  // 兼容索引把旧阶段入口带到这里时，用户只需要选择项目一次；之后
  // 进入对应的 Atelier 工作区，而不是再回到项目首页手动寻找同一功能。
  const requestedRoute = searchParams.get('next')
  const projectTarget = requestedRoute && [
    'project.overview',
    'project.blueprint',
    'project.coverage',
    'project.standard',
    'project.runs',
    'project.pilot',
    'project.runNew',
    'project.data',
    'project.review',
    'project.quality',
    'project.rules',
    'project.releases',
  ].includes(requestedRoute) ? requestedRoute : 'project.overview'
  const requestedProjectId = parseProjectResourceId(searchParams.get('projectId'))
  const requestedContext = searchParams.toString()

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

  useEffect(() => {
    if (!requestedProjectId) return
    if (autoOpenedTarget.current === requestedContext) return
    autoOpenedTarget.current = requestedContext
    setTargetError(null)
    const requestId = ++targetRequest.current
    void studioApi.getProject(requestedProjectId)
      .then((response) => {
        if (requestId !== targetRequest.current) return
        // Keep the server-provided stable envelope ID (`p_<n>`). The nested
        // `data.id` is the legacy numeric database ID and must not become the
        // canonical project URL.
        const target = projectHref(projectTarget, response.id)
        const context = new URLSearchParams(requestedContext)
        context.delete('next')
        context.delete('projectId')
        const query = context.toString()
        navigate(query ? `${target}?${query}` : target)
      })
      .catch((loadError) => {
        if (requestId !== targetRequest.current) return
        setTargetError(loadError instanceof Error ? loadError.message : `无法打开项目 ${requestedProjectId}`)
      })
    return () => {
      if (targetRequest.current === requestId) {
        targetRequest.current += 1
        if (autoOpenedTarget.current === requestedContext) autoOpenedTarget.current = ''
      }
    }
  }, [navigate, projectTarget, requestedContext, requestedProjectId, targetRetry])

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

      {targetError ? (
        <div role="alert" data-project-target-error="true">
          <Card className="console-card mb-3">
            <div className="flex items-start gap-2">
              <AlertTriangle size={18} className="mt-1 text-amber-500" aria-hidden />
              <div>
                <Text strong className="block">无法打开指定项目 {requestedProjectId}</Text>
                <Text type="tertiary">{targetError}</Text>
                <Button
                  size="small"
                  className="mt-3"
                  onClick={() => {
                    autoOpenedTarget.current = ''
                    setTargetError(null)
                    setTargetRetry((previous) => previous + 1)
                  }}
                >
                  重试
                </Button>
              </div>
            </div>
          </Card>
        </div>
      ) : null}

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
              onClick={() => navigate(projectHref(projectTarget, project.id))}
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
  const projectName = useProjectName(scope.projectId)
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

  const versionRows = [
    ['蓝图', overview.versions.blueprint],
    ['覆盖', overview.versions.coverage],
    ['标准', overview.versions.standard],
    ['质量策略', overview.versions.qualityPolicy],
    ['映射', overview.versions.mapping],
  ] as const

  return (
    <div className="console-page atelier-overview-page" data-studio-page="overview">
      <header className="atelier-page-intro">
        <div>
          <div className="eyebrow">PROJECT / {overview.targetKind === 'grpo' ? 'GRPO' : 'SFT'}</div>
          <h1>{projectName ?? '数据项目'}</h1>
          <Text type="tertiary">目标：{overview.goal || '尚未填写交付目标'}</Text>
        </div>
        <Button theme="solid" type="primary" onClick={() => navigate(overview.nextAction.href)}>
          进入工作区 →
        </Button>
      </header>

      <section className="atelier-overview-metrics" aria-label="项目摘要">
        <div><span>目标问题</span><strong>{overview.stats.plannedQuestions.toLocaleString()}</strong><small>计划量，不是已产出</small></div>
        <div><span>当前方案</span><strong>v{overview.versions.blueprint?.version ?? '—'}</strong><small>{overview.versions.blueprint ? '所有历史版本保留' : '尚未保存'}</small></div>
        <div><span>待处理决定</span><strong>{overview.stats.pendingReview.toLocaleString()}</strong><small>等待人工判断的内容版本</small></div>
        <div><span>交付映射</span><strong>{overview.versions.mapping ? `v${overview.versions.mapping.version}` : '—'}</strong><small>{overview.stats.acceptanceRateDisplay}</small></div>
      </section>

      <div className="atelier-overview-grid">
        <section className="atelier-journey-panel">
          <div className="atelier-section-heading"><div><div className="eyebrow">PROJECT JOURNEY</div><h2>当前旅程</h2></div></div>
          <div className="atelier-journey-list">
            {[
              ['01 设计', '范围与方案已经就绪', '方案节点有独立职责，改动后可以先做试制。', 'project.blueprint', '生产蓝图 →'],
              ['02 试制', '比较方案，再投入下一批', `试制 ${overview.batches.pilot} 批 · 扩量 ${overview.batches.scale} 批`, 'project.compare', '查看试制对比 →'],
              ['03 数据与质量', '把质量证据转成具体判断', `${overview.stats.generated} 个已生成版本 · ${overview.stats.acceptanceRateDisplay}`, 'project.quality', '进入质量实验室 →'],
              ['04 发布', '冻结内容，交付可复现版本', `${overview.batches.failed} 个失败/部分失败批次仍可恢复`, 'project.releases', '准备发布 →'],
            ].map(([step, title, detail, route, action]) => (
              <div className="atelier-journey-row" key={step}>
                <div><Tag size="small">{step}</Tag><h3>{title}</h3><Text type="tertiary" size="small">{detail}</Text></div>
                <Button theme="borderless" onClick={() => navigate(scope.href(route))}>{action}</Button>
              </div>
            ))}
          </div>
        </section>

        <aside className="atelier-overview-side">
          <section className="atelier-detail-panel">
            <div className="eyebrow">PROJECT PROMISE</div><h2>项目约定</h2>
            <dl>
              <div><dt>训练类型</dt><dd>{overview.targetKind === 'grpo' ? 'GRPO' : 'SFT'}</dd></div>
              <div><dt>计划规模</dt><dd>{overview.stats.plannedQuestions.toLocaleString()} 题</dd></div>
              <div><dt>批次</dt><dd>{overview.batches.total}（试制 {overview.batches.pilot}）</dd></div>
              <div><dt>预算占用</dt><dd>{formatMinor(overview.budget.settledMinor + overview.budget.uncertainMinor + overview.budget.reservedMinor)}</dd></div>
            </dl>
          </section>
          <section className="atelier-detail-panel">
            <div className="eyebrow">NEXT DECISION</div><h2>{overview.nextAction.message}</h2>
            <Button theme="solid" type="primary" onClick={() => navigate(overview.nextAction.href)}>继续这一步</Button>
          </section>
          <section className="atelier-detail-panel">
            <div className="eyebrow">VERSION LEDGER</div><h2>当前版本</h2>
            <div className="atelier-version-list">
              {versionRows.map(([label, version]) => <div key={label}><span>{label}</span><strong>{version ? `v${version.version}` : '未保存'}</strong></div>)}
            </div>
          </section>
        </aside>
      </div>
      <LegacyCapabilityWorkbench />
    </div>
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
