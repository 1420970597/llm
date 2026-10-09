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
/**
 * 项目状态 → 用户可见文案（issue #191）。
 *
 * 后端取值来自 `internal/model/project.go` 的 `ProjectStatus*`。以前这里直接
 * 渲染 `project.status`，于是项目列表上显示 `pilot_running` 这类内部枚举。
 * 未知取值给「状态未知」，**不回传原始串**。
 */
const PROJECT_STATUS_LABEL: Record<string, string> = {
  draft: '草稿',
  designed: '设计完成',
  pilot_running: '试制进行中',
  pilot_ready: '试制已就绪',
  scaling: '扩量中',
  review: '审阅中',
  candidate: '待发布',
  published: '已发布',
  archived: '已归档',
}
function projectStatusLabel(status: string): string {
  return PROJECT_STATUS_LABEL[status] ?? '状态未知'
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
        </div>
        <div className="console-page__actions">
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
          <Empty description={search ? '没有匹配的项目' : '还没有项目'} />
          {!search ? <Button theme="solid" type="primary" onClick={() => navigate('/new')}>创建项目</Button> : <Button onClick={() => setSearch('')}>清除搜索</Button>}
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
                  计划 {(project.data.domainCount * project.data.directionsPerDomain * project.data.questionsPerDirection).toLocaleString()} 条
                </Text>
              </div>
              <span className="project-card__continue">进入项目 →</span>
              <div className="mt-2 flex items-center gap-2">
                <Tag size="small">{projectStatusLabel(project.status)}</Tag>
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

      {/*
        issue #197 第 1 条：分页控件以前**只在 nextCursor 非空时出现**，
        于是「一个项目、单页装得下」时页面上完全没有分页痕迹，用户无法回答
        「一共几个项目」「我是不是已经翻到底了」。
        现在**常显一行状态**（已显示 N 条 · 本页第 M 页 · 是否还有下一页），
        并且显式声明翻页方式（游标 / 只往后追加），而不是让用户猜。
        服务端返回的是游标分页且不提供总数，因此这里说的是「已显示 N 条」
        而不是编造一个「共 M 条」。
      */}
      <div className="project-list-footer" data-projects-pagination="true">
        <Text type="tertiary" size="small">
          已显示 {projects.length} 个项目 · 第 {pageIndex} 页
          {nextCursor !== '' ? ' · 还有更多' : ' · 已到底'}
        </Text>
        {nextCursor !== '' ? (
          <div className="mt-2 flex justify-center">
            <Button
              onClick={() => {
                // 游标分页：只追加，不重新拉第一页 —— 那会在并发写入下
                // 重复显示同一批项目（契约 §1.5 的「翻页无重复无遗漏」）。
                setPageIndex((index) => index + 1)
                void load(search, nextCursor, true)
              }}
              loading={loading}
              data-projects-load-more="true"
            >
              加载更多（第 {pageIndex + 1} 页）
            </Button>
          </div>
        ) : null}
      </div>
    </div>
  )
}
/**
 * 项目首页（P01）。
 *
 * 页面只保留一条主线：当前阶段、阻塞事实和下一步。详细配置仍由阶段
 * 页面承载，避免项目首页变成版本账本、统计报表和帮助文档的混合物。
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

  const designConfigured = Boolean(overview.versions.blueprint && overview.versions.coverage && overview.versions.standard)
  const hasProduction = overview.batches.total > 0
  const reviewComplete = overview.stats.pendingReview === 0 && overview.stats.accepted + overview.stats.quarantined > 0
  const published = overview.nextAction.kind === 'done'
  const stages = [
    { key: 'design', label: '设计', ready: designConfigured, value: designConfigured ? '已配置 · 执行前仍需校验' : '待配置', href: scope.href('project.blueprint') },
    { key: 'run', label: '生产', ready: hasProduction && overview.batches.completed === overview.batches.total, value: hasProduction ? `${overview.batches.completed}/${overview.batches.total} 批次完成` : '未开始 · 也可导入数据', href: scope.href('project.runs') },
    { key: 'review', label: '审阅', ready: reviewComplete, value: overview.stats.pendingReview > 0 ? `${overview.stats.pendingReview} 条待判断` : `${overview.stats.accepted} 条已接纳`, href: scope.href('project.review') },
    { key: 'release', label: '发布', ready: published, value: published ? '当前接纳数据已发布' : overview.stats.accepted > 0 ? '查看候选与质量门槛' : '暂无可发布数据', href: scope.href('project.releases') },
  ]
  const currentStage = stages.findIndex((stage) => stage.key === (published ? 'release' : overview.nextAction.kind))
  const currentStageIndex = currentStage >= 0 ? currentStage : stages.findIndex((stage) => !stage.ready)
  const blockers = [
    !overview.versions.blueprint ? { text: '完成生产蓝图', href: scope.href('project.blueprint') } : null,
    !overview.versions.coverage ? { text: '补充目标结构', href: scope.href('project.coverage') } : null,
    !overview.versions.standard ? { text: '设置思维标准', href: scope.href('project.standard') } : null,
    overview.batches.failed > 0 ? { text: `处理 ${overview.batches.failed} 个失败批次`, href: scope.href('project.runs') } : null,
  ].filter((item): item is { text: string; href: string } => Boolean(item))
  const nextLabel = overview.nextAction.message || '继续项目'
  return (
    <div className="console-page project-command-center" data-studio-page="overview">
      <header className="project-command-center__header">
        <div>
          <div className="eyebrow">项目 · {overview.targetKind === 'grpo' ? 'GRPO' : 'SFT'}</div>
          <h1>{projectName ?? '数据项目'}</h1>
          <p>{overview.goal || '尚未填写交付目标'}</p>
        </div>
      </header>

      <section className="project-command-progress" aria-label="项目进度">
        {stages.map((stage, index) => (
          <button type="button" className={index === currentStageIndex ? 'is-current' : stage.ready ? 'is-complete' : ''} key={stage.key} onClick={() => navigate(stage.href)}>
            <span>{index + 1}</span>
            <strong>{stage.label}</strong>
            <small>{stage.value}</small>
          </button>
        ))}
      </section>

      <div className="project-command-grid">
        <section className="project-command-next">
          <div className="project-command-section-head"><div><span className="eyebrow">下一步</span><h2>{nextLabel}</h2></div><Tag color="blue">阶段 {Math.max(1, currentStageIndex + 1)}/4</Tag></div>
          <p className="project-command-next__status">{published ? '查看已冻结的交付文件。' : '继续处理当前任务。'}</p>
          <Button theme="solid" type="primary" onClick={() => navigate(overview.nextAction.href)}>{nextLabel}</Button>
        </section>

        <section className="project-command-facts">
          <div className="project-command-section-head"><div><span className="eyebrow">关键事实</span><h2>项目现在的状态</h2></div></div>
          <div className="project-command-facts__grid">
            <div><strong>{overview.stats.plannedQuestions.toLocaleString()}</strong><span>计划题数</span></div>
            <div><strong>{overview.stats.generated.toLocaleString()}</strong><span>已生成</span></div>
            <div><strong>{overview.stats.pendingReview.toLocaleString()}</strong><span>待审阅</span></div>
            <div><strong>{overview.stats.acceptanceRateDisplay}</strong><span>接纳率</span></div>
          </div>
        </section>

        <section className="project-command-blockers">
          <div className="project-command-section-head"><div><h2>{blockers.length ? `${blockers.length} 项待处理` : '配置与批次'}</h2></div></div>
          {blockers.length ? <ul>{blockers.map((blocker) => <li key={blocker.href}><span>{blocker.text}</span><Button theme="borderless" size="small" onClick={() => navigate(blocker.href)}>查看 →</Button></li>)}</ul> : <p>没有缺失版本或失败批次。发布门槛以候选检查结果为准。</p>}
        </section>
      </div>
    </div>
  )
}
