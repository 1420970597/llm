import { FormEvent, useEffect, useMemo, useState } from 'react'
import { motion } from 'framer-motion'
import { ArrowRight, BrainCircuit, DatabaseZap, GitBranch, HardDriveDownload, LayoutDashboard, Save, ShieldCheck, Sparkles } from 'lucide-react'
import { Artifact, Dataset, DatasetGraph, Domain, Provider, Question, ReasoningRecord, RewardRecord, RuntimeStatus, StorageProfile, Strategy, userApi } from './lib/api'

function statusLabel(status: string) {
  switch (status) {
    case 'draft':
      return '草稿'
    case 'domains_confirmed':
      return '领域已确认'
    case 'questions_generated':
      return '问题已生成'
    case 'reasoning_generated':
      return '推理已生成'
    case 'rewards_generated':
      return '奖励数据已生成'
    default:
      return status
  }
}

function sourceLabel(source: string) {
  switch (source) {
    case 'ai':
      return '模型生成'
    case 'mock':
      return '模拟数据'
    default:
      return source
  }
}

function artifactTypeLabel(type: string) {
  switch (type) {
    case 'jsonl-export':
      return 'JSONL 导出包'
    default:
      return type
  }
}

function GraphPreview({ rootKeyword, domains }: { rootKeyword: string; domains: Domain[] }) {
  const visible = domains.slice(0, 16)
  const width = 620
  const height = 420
  const centerX = width / 2
  const centerY = height / 2
  const radius = 138

  const positioned = visible.map((domain, index) => {
    const angle = (Math.PI * 2 * index) / Math.max(visible.length, 1)
    return {
      ...domain,
      x: centerX + Math.cos(angle) * radius,
      y: centerY + Math.sin(angle) * radius,
    }
  })

  return (
    <div className="graph-surface">
      <svg viewBox={`0 0 ${width} ${height}`} className="graph-svg" role="img" aria-label="领域图谱预览">
        {positioned.map((node) => (
          <line key={`line-${node.id}`} x1={centerX} y1={centerY} x2={node.x} y2={node.y} stroke="rgba(80, 129, 255, 0.18)" strokeWidth="1.4" />
        ))}
        <circle cx={centerX} cy={centerY} r="48" fill="rgba(79, 109, 245, 0.12)" stroke="rgba(79, 109, 245, 0.45)" />
        <text x={centerX} y={centerY} textAnchor="middle" dominantBaseline="middle" className="graph-root-text">{rootKeyword}</text>
        {positioned.map((node) => (
          <g key={node.id}>
            <circle cx={node.x} cy={node.y} r="30" fill="#ffffff" stroke="rgba(79, 109, 245, 0.26)" />
            <text x={node.x} y={node.y} textAnchor="middle" dominantBaseline="middle" className="graph-node-text">{node.name.slice(0, 8)}</text>
          </g>
        ))}
      </svg>
      {domains.length > visible.length ? <p className="graph-caption">当前图谱仅展示前 16 个领域节点，实际已生成 {domains.length} 个领域。</p> : null}
    </div>
  )
}

export default function App() {
  const [authenticated, setAuthenticated] = useState(false)
  const [strategies, setStrategies] = useState<Strategy[]>([])
  const [providers, setProviders] = useState<Provider[]>([])
  const [storageProfiles, setStorageProfiles] = useState<StorageProfile[]>([])
  const [datasets, setDatasets] = useState<Dataset[]>([])
  const [graph, setGraph] = useState<DatasetGraph | null>(null)
  const [questions, setQuestions] = useState<Question[]>([])
  const [reasoning, setReasoning] = useState<ReasoningRecord[]>([])
  const [rewards, setRewards] = useState<RewardRecord[]>([])
  const [artifacts, setArtifacts] = useState<Artifact[]>([])
  const [runtime, setRuntime] = useState<RuntimeStatus | null>(null)
  const [message, setMessage] = useState<string>('')
  const [estimate, setEstimate] = useState<Dataset['estimate'] | null>(null)
  const [loading, setLoading] = useState(false)
  const [formState, setFormState] = useState({
    name: '',
    rootKeyword: '军事',
    targetSize: 12000,
    strategyId: 0,
    providerId: 0,
    storageProfileId: 0,
  })

  const adminHref = typeof window !== 'undefined'
    ? `${window.location.protocol}//${window.location.hostname}:3211`
    : '/'

  const activeDataset = graph?.dataset ?? null

  const loadBootstrap = async (nextMessage?: string) => {
    setLoading(true)
    try {
      const [strategyData, providerData, storageData, datasetData, runtimeData] = await Promise.all([
        userApi.listStrategies(),
        userApi.listProviders(),
        userApi.listStorageProfiles(),
        userApi.listDatasets(),
        userApi.runtimeStatus(),
      ])

      setStrategies(strategyData)
      setProviders(providerData)
      setStorageProfiles(storageData)
      setDatasets(datasetData)
      setRuntime(runtimeData)
      setFormState((current) => ({
        ...current,
        strategyId: current.strategyId || strategyData[0]?.id || 0,
        providerId: current.providerId || providerData[0]?.id || 0,
        storageProfileId: current.storageProfileId || storageData[0]?.id || 0,
      }))

      const preferredDatasetId = datasetData.some((dataset) => dataset.id === graph?.dataset.id)
        ? graph?.dataset.id
        : datasetData[0]?.id

      if (preferredDatasetId) {
        const [nextGraph, nextQuestions, nextReasoning, nextRewards, nextArtifacts] = await Promise.all([
          userApi.getDataset(preferredDatasetId),
          userApi.listQuestions(preferredDatasetId),
          userApi.listReasoning(preferredDatasetId),
          userApi.listRewards(preferredDatasetId),
          userApi.listArtifacts(preferredDatasetId),
        ])
        setGraph(nextGraph)
        setQuestions(nextQuestions)
        setReasoning(nextReasoning)
        setRewards(nextRewards)
        setArtifacts(nextArtifacts)
      } else {
        setGraph(null)
        setQuestions([])
        setReasoning([])
        setRewards([])
        setArtifacts([])
      }

      if (nextMessage) {
        setMessage(preferredDatasetId ? `${nextMessage} 已同步最近的数据集。` : nextMessage)
      }
    } catch (error) {
      setMessage((error as Error).message)
    } finally {
      setLoading(false)
    }
  }

  const loadDatasetWorkspace = async (datasetId: number, nextMessage?: string) => {
    setLoading(true)
    try {
      const [nextGraph, nextQuestions, nextReasoning, nextRewards, nextArtifacts, nextRuntime, nextDatasets] = await Promise.all([
        userApi.getDataset(datasetId),
        userApi.listQuestions(datasetId),
        userApi.listReasoning(datasetId),
        userApi.listRewards(datasetId),
        userApi.listArtifacts(datasetId),
        userApi.runtimeStatus(),
        userApi.listDatasets(),
      ])
      setGraph(nextGraph)
      setQuestions(nextQuestions)
      setReasoning(nextReasoning)
      setRewards(nextRewards)
      setArtifacts(nextArtifacts)
      setRuntime(nextRuntime)
      setDatasets(nextDatasets)
      if (nextMessage) {
        setMessage(nextMessage)
      }
    } catch (error) {
      setMessage((error as Error).message)
    } finally {
      setLoading(false)
    }
  }

  useEffect(() => {
    if (!authenticated) return
    void loadBootstrap().catch((error: Error) => setMessage(error.message))
  }, [authenticated])

  const plannerCards = useMemo(() => {
    if (!estimate) return []
    return [
      { label: '领域数', value: estimate.domainCount },
      { label: '每领域问题数', value: estimate.questionsPerDomain },
      { label: '预计问题数', value: estimate.estimatedQuestions },
      { label: '预计样本数', value: estimate.estimatedSamples },
    ]
  }, [estimate])

  const submitLogin = (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault()
    setAuthenticated(true)
    setMessage('工作台已解锁，请先完成计划配置并生成领域图谱。')
  }

  const estimatePlan = async () => {
    setLoading(true)
    try {
      const nextEstimate = await userApi.estimatePlan({
        rootKeyword: formState.rootKeyword,
        targetSize: Number(formState.targetSize),
        strategyId: Number(formState.strategyId),
      })
      setEstimate(nextEstimate)
      setMessage('数据集规模估算已刷新。')
    } catch (error) {
      setMessage((error as Error).message)
    } finally {
      setLoading(false)
    }
  }

  const createDataset = async () => {
    if (!estimate) {
      setMessage('请先完成数据集计划估算。')
      return
    }
    setLoading(true)
    try {
      const created = await userApi.createDataset({
        name: formState.name || `${formState.rootKeyword} 数据集`,
        rootKeyword: formState.rootKeyword,
        targetSize: Number(formState.targetSize),
        strategyId: Number(formState.strategyId),
        providerId: Number(formState.providerId),
        storageProfileId: Number(formState.storageProfileId),
        status: 'draft',
        estimate,
      })
      const nextGraph = await userApi.getDataset(created.id)
      setGraph(nextGraph)
      setQuestions([])
      setReasoning([])
      setRewards([])
      setArtifacts([])
      setDatasets(await userApi.listDatasets())
      setRuntime(await userApi.runtimeStatus())
      setMessage('数据集已创建，现在可以生成领域图谱。')
    } catch (error) {
      setMessage((error as Error).message)
    } finally {
      setLoading(false)
    }
  }

  const generateDomains = async () => {
    if (!graph) {
      setMessage('请先创建数据集，再生成领域。')
      return
    }
    setLoading(true)
    try {
      const nextGraph = await userApi.generateDomains(graph.dataset.id)
      setGraph(nextGraph)
      setMessage(`已生成 ${nextGraph.domains.length} 个领域，等待审核。`)
    } catch (error) {
      setMessage((error as Error).message)
    } finally {
      setLoading(false)
    }
  }

  const renameDomain = (domainId: number, name: string) => {
    setGraph((current) => current ? {
      ...current,
      domains: current.domains.map((domain) => domain.id === domainId ? { ...domain, name } : domain),
    } : current)
  }

  const saveGraph = async () => {
    if (!graph) return
    setLoading(true)
    try {
      await userApi.updateGraph(graph.dataset.id, graph.domains)
      setMessage('图谱编辑已保存。')
    } catch (error) {
      setMessage((error as Error).message)
    } finally {
      setLoading(false)
    }
  }

  const confirmDomains = async () => {
    if (!graph) return
    setLoading(true)
    try {
      await userApi.confirmDomains(graph.dataset.id)
      const refreshed = await userApi.getDataset(graph.dataset.id)
      setGraph(refreshed)
      setMessage('领域已确认，数据集已可进入问题生成阶段。')
    } catch (error) {
      setMessage((error as Error).message)
    } finally {
      setLoading(false)
    }
  }

  const generateQuestions = async () => {
    if (!graph) return
    setLoading(true)
    try {
      await userApi.generateQuestions(graph.dataset.id)
      setMessage('问题生成任务已入队，请稍后刷新查看结果。')
    } catch (error) {
      setMessage((error as Error).message)
    } finally {
      setLoading(false)
    }
  }

  const refreshQuestions = async () => {
    if (!graph) return
    setLoading(true)
    try {
      setQuestions(await userApi.listQuestions(graph.dataset.id))
      setRuntime(await userApi.runtimeStatus())
      setMessage('问题预览已刷新。')
    } catch (error) {
      setMessage((error as Error).message)
    } finally {
      setLoading(false)
    }
  }

  const generateReasoning = async () => {
    if (!graph) return
    setLoading(true)
    try {
      await userApi.generateReasoning(graph.dataset.id)
      setMessage('推理生成任务已入队，请稍后刷新查看长文本结果。')
    } catch (error) {
      setMessage((error as Error).message)
    } finally {
      setLoading(false)
    }
  }

  const refreshReasoning = async () => {
    if (!graph) return
    setLoading(true)
    try {
      setReasoning(await userApi.listReasoning(graph.dataset.id))
      setRuntime(await userApi.runtimeStatus())
      setMessage('推理预览已刷新。')
    } catch (error) {
      setMessage((error as Error).message)
    } finally {
      setLoading(false)
    }
  }

  const generateRewards = async () => {
    if (!graph) return
    setLoading(true)
    try {
      await userApi.generateRewards(graph.dataset.id)
      setMessage('奖励数据生成任务已入队，请稍后刷新查看评分结果。')
    } catch (error) {
      setMessage((error as Error).message)
    } finally {
      setLoading(false)
    }
  }

  const refreshRewards = async () => {
    if (!graph) return
    setLoading(true)
    try {
      setRewards(await userApi.listRewards(graph.dataset.id))
      setRuntime(await userApi.runtimeStatus())
      setMessage('奖励预览已刷新。')
    } catch (error) {
      setMessage((error as Error).message)
    } finally {
      setLoading(false)
    }
  }

  const generateExport = async () => {
    if (!graph) return
    setLoading(true)
    try {
      await userApi.generateExport(graph.dataset.id)
      setMessage('导出任务已入队，请稍后刷新查看打包后的数据集文件。')
    } catch (error) {
      setMessage((error as Error).message)
    } finally {
      setLoading(false)
    }
  }

  const refreshArtifacts = async () => {
    if (!graph) return
    setLoading(true)
    try {
      setArtifacts(await userApi.listArtifacts(graph.dataset.id))
      setRuntime(await userApi.runtimeStatus())
      setMessage('导出工件预览已刷新。')
    } catch (error) {
      setMessage((error as Error).message)
    } finally {
      setLoading(false)
    }
  }

  return (
    <div className="workspace-shell">
      <aside className="sidebar-frame">
        <div className="sidebar-brand">
          <div className="brand-badge"><Sparkles size={18} strokeWidth={1.8} /></div>
          <div>
            <strong>LLM 数据工厂</strong>
            <p>企业级训练数据工作台</p>
          </div>
        </div>

        <nav className="sidebar-navlist">
          <a href="#planner"><LayoutDashboard size={16} /> 工作台总览</a>
          <a href="#graph"><GitBranch size={16} /> 领域图谱</a>
          <a href="#pipeline"><BrainCircuit size={16} /> 生成流水线</a>
          <a href="#exports"><HardDriveDownload size={16} /> 导出与工件</a>
        </nav>

        <div className="sidebar-summary">
          <span className="sidebar-label">运行概况</span>
          <div className="sidebar-metric"><span>数据集</span><strong>{runtime?.datasetCount ?? 0}</strong></div>
          <div className="sidebar-metric"><span>队列深度</span><strong>{runtime?.queueDepth ?? 0}</strong></div>
          <div className="sidebar-metric"><span>工件数</span><strong>{runtime?.artifactCount ?? 0}</strong></div>
        </div>

        <a className="admin-jump" href={adminHref}>进入管理后台</a>
      </aside>

      <main className="workspace-content">
        <section className="headline-card">
          <motion.div initial={{ opacity: 0, y: 18 }} animate={{ opacity: 1, y: 0 }} transition={{ duration: 0.45 }}>
            <p className="eyebrow">企业级数据生成控制台</p>
            <h1>规划数据集规模，生成可治理的领域图谱，并驱动问题、推理、奖励与导出流水线。</h1>
            <p className="headline-copy">界面风格已调整为更接近参考项目的控制台布局：左侧导航、右侧工作区、更密集的信息卡片与更强的后台工具感。</p>
            <div className="headline-actions">
              <button className="primary-action" onClick={() => void estimatePlan()} disabled={loading}>估算计划 <ArrowRight size={16} /></button>
              <button className="secondary-action" onClick={() => void createDataset()} disabled={loading}>创建数据集</button>
            </div>
            {message ? <p className="status-banner">{message}</p> : null}
          </motion.div>
        </section>

        <section className="content-grid" id="planner">
          <div className="content-card planner-card">
            <div className="section-heading compact">
              <span className="eyebrow">计划配置</span>
              <h2>配置任务入口</h2>
            </div>
            {!authenticated ? (
              <form className="login-card" onSubmit={submitLogin}>
                <label>
                  工作台邮箱
                  <input type="email" placeholder="operator@company.com" required />
                </label>
                <label>
                  访问密钥
                  <input type="password" placeholder="请输入安全访问密钥" required />
                </label>
                <button type="submit" className="primary-action full-width">进入工作台 <ArrowRight size={16} /></button>
              </form>
            ) : (
              <div className="planner-panel">
                <div className="planner-grid">
                  <label>
                    数据集名称
                    <input value={formState.name} onChange={(event) => setFormState({ ...formState, name: event.target.value })} placeholder="军事推理数据工厂" />
                  </label>
                  <label>
                    根关键词
                    <input value={formState.rootKeyword} onChange={(event) => setFormState({ ...formState, rootKeyword: event.target.value })} />
                  </label>
                  <label>
                    目标规模
                    <input type="number" value={formState.targetSize} onChange={(event) => setFormState({ ...formState, targetSize: Number(event.target.value) })} />
                  </label>
                  <label>
                    策略
                    <select value={formState.strategyId} onChange={(event) => setFormState({ ...formState, strategyId: Number(event.target.value) })}>
                      {strategies.map((strategy) => <option key={strategy.id} value={strategy.id}>{strategy.name}</option>)}
                    </select>
                  </label>
                  <label>
                    模型提供方
                    <select value={formState.providerId} onChange={(event) => setFormState({ ...formState, providerId: Number(event.target.value) })}>
                      {providers.map((provider) => <option key={provider.id} value={provider.id}>{provider.name}</option>)}
                    </select>
                  </label>
                  <label>
                    存储配置
                    <select value={formState.storageProfileId} onChange={(event) => setFormState({ ...formState, storageProfileId: Number(event.target.value) })}>
                      {storageProfiles.map((profile) => <option key={profile.id} value={profile.id}>{profile.name}</option>)}
                    </select>
                  </label>
                </div>
                <div className="planner-cards">
                  {plannerCards.map((item) => (
                    <div className="metric-card" key={item.label}>
                      <span>{item.label}</span>
                      <strong>{item.value}</strong>
                    </div>
                  ))}
                </div>
              </div>
            )}
          </div>

          <div className="content-card highlight-card">
            <div className="section-heading compact">
              <span className="eyebrow">流程概览</span>
              <h2>分阶段闭环</h2>
            </div>
            <div className="pillar-grid two-col">
              {[
                { icon: BrainCircuit, title: '计划估算', description: '先按目标样本规模反推领域和问题配额。' },
                { icon: DatabaseZap, title: '数据集创建', description: '固化策略、模型与存储配置。' },
                { icon: ShieldCheck, title: '图谱审核', description: '先生领域，再人工修订并确认。' },
                { icon: HardDriveDownload, title: '工件导出', description: '将最终数据集打包为 JSONL 工件。' },
              ].map(({ icon: Icon, title, description }) => (
                <article key={title} className="feature-tile">
                  <div className="icon-chip"><Icon size={18} strokeWidth={1.9} /></div>
                  <h3>{title}</h3>
                  <p>{description}</p>
                </article>
              ))}
            </div>
          </div>
        </section>

        <section className="content-grid" id="graph">
          <div className="content-card span-two">
            <div className="section-heading compact">
              <span className="eyebrow">领域图谱</span>
              <h2>图谱生成与审核</h2>
            </div>
            {graph ? (
              <>
                <GraphPreview rootKeyword={graph.dataset.rootKeyword} domains={graph.domains} />
                <div className="graph-actions">
                  <button className="secondary-action" onClick={() => void generateDomains()} disabled={loading}>生成或刷新领域</button>
                  <button className="secondary-action" onClick={() => void saveGraph()} disabled={loading}><Save size={15} /> 保存编辑</button>
                  <button className="primary-action" onClick={() => void confirmDomains()} disabled={loading}>确认领域</button>
                </div>
              </>
            ) : (
              <p className="hero-copy">请先创建数据集，再启用领域图谱生成和审核。</p>
            )}
          </div>
          <div className="content-card">
            <div className="section-heading compact">
              <span className="eyebrow">领域列表</span>
              <h2>人工调整命名</h2>
            </div>
            {graph ? (
              <div className="domain-list compact-list">
                {graph.domains.slice(0, 20).map((domain) => (
                  <label key={domain.id} className="domain-item">
                    <span>{sourceLabel(domain.source)}</span>
                    <input value={domain.name} onChange={(event) => renameDomain(domain.id, event.target.value)} />
                  </label>
                ))}
                {graph.domains.length > 20 ? <p className="graph-caption">当前界面仅支持编辑前 20 个领域；全部 {graph.domains.length} 个领域仍会完整持久化保存。</p> : null}
              </div>
            ) : (
              <p className="hero-copy">尚未加载任何数据集图谱。</p>
            )}
          </div>
        </section>

        <section className="content-card dataset-strip">
          <div className="section-heading compact">
            <span className="eyebrow">最近的数据集</span>
            <h2>快速切换任务</h2>
          </div>
          <div className="dataset-grid">
            {datasets.map((dataset) => (
              <button key={dataset.id} className="dataset-card" onClick={async () => { setGraph(await userApi.getDataset(dataset.id)); setQuestions(await userApi.listQuestions(dataset.id)); setReasoning(await userApi.listReasoning(dataset.id)); setRewards(await userApi.listRewards(dataset.id)); setArtifacts(await userApi.listArtifacts(dataset.id)); setRuntime(await userApi.runtimeStatus()) }}>
                <div>
                  <span className="eyebrow">{statusLabel(dataset.status)}</span>
                  <h3>{dataset.name}</h3>
                </div>
                <p>{dataset.rootKeyword} · {dataset.estimate.estimatedSamples} 个预计样本</p>
                <div className="dataset-meta"><GitBranch size={15} /> 数据集 #{dataset.id}</div>
              </button>
            ))}
          </div>
        </section>

        <section className="content-grid" id="pipeline">
          <div className="content-card">
            <div className="section-heading compact"><span className="eyebrow">问题生成</span><h2>问题流水线</h2></div>
            <div className="graph-actions">
              <button className="primary-action" onClick={() => void generateQuestions()} disabled={loading || !graph}>加入问题生成队列</button>
              <button className="secondary-action" onClick={() => void refreshQuestions()} disabled={loading || !graph}>刷新问题</button>
            </div>
            <div className="domain-list">
              {questions.slice(0, 8).map((question) => (
                <div key={question.id} className="question-item"><span>{question.domainName}</span><p>{question.content}</p></div>
              ))}
              {questions.length === 0 ? <p className="hero-copy">尚未生成问题。</p> : null}
            </div>
          </div>

          <div className="content-card">
            <div className="section-heading compact"><span className="eyebrow">推理生成</span><h2>推理与答案</h2></div>
            <div className="graph-actions">
              <button className="primary-action" onClick={() => void generateReasoning()} disabled={loading || !graph || questions.length === 0}>加入推理生成队列</button>
              <button className="secondary-action" onClick={() => void refreshReasoning()} disabled={loading || !graph}>刷新推理</button>
            </div>
            <div className="domain-list">
              {reasoning.slice(0, 8).map((record) => (
                <div key={record.id} className="question-item"><span>{record.objectKey}</span><p>{record.answerSummary}</p></div>
              ))}
              {reasoning.length === 0 ? <p className="hero-copy">尚未生成推理记录。</p> : null}
            </div>
          </div>

          <div className="content-card">
            <div className="section-heading compact"><span className="eyebrow">奖励数据</span><h2>评估与奖励</h2></div>
            <div className="graph-actions">
              <button className="primary-action" onClick={() => void generateRewards()} disabled={loading || !graph || reasoning.length === 0}>加入奖励生成队列</button>
              <button className="secondary-action" onClick={() => void refreshRewards()} disabled={loading || !graph}>刷新奖励数据</button>
            </div>
            <div className="domain-list">
              {rewards.slice(0, 8).map((record) => (
                <div key={record.id} className="question-item"><span>{record.objectKey}</span><p>评分：{record.score.toFixed(2)} · {record.questionText}</p></div>
              ))}
              {rewards.length === 0 ? <p className="hero-copy">尚未生成奖励记录。</p> : null}
            </div>
          </div>
        </section>

        <section className="content-grid" id="exports">
          <div className="content-card">
            <div className="section-heading compact"><span className="eyebrow">导出与运行态</span><h2>工件打包</h2></div>
            <div className="graph-actions">
              <button className="primary-action" onClick={() => void generateExport()} disabled={loading || !graph || rewards.length === 0}>加入导出队列</button>
              <button className="secondary-action" onClick={() => void refreshArtifacts()} disabled={loading || !graph}>刷新工件</button>
            </div>
            {runtime ? (
              <div className="planner-cards">
                <div className="metric-card"><span>数据集</span><strong>{runtime.datasetCount}</strong></div>
                <div className="metric-card"><span>队列深度</span><strong>{runtime.queueDepth}</strong></div>
                <div className="metric-card"><span>工件数</span><strong>{runtime.artifactCount}</strong></div>
                <div className="metric-card"><span>奖励记录</span><strong>{runtime.rewardCount}</strong></div>
              </div>
            ) : null}
          </div>

          <div className="content-card">
            <div className="section-heading compact"><span className="eyebrow">工件预览</span><h2>已打包数据集产物</h2></div>
            <div className="domain-list">
              {artifacts.map((artifact) => (
                <div key={artifact.id} className="question-item"><span>{artifactTypeLabel(artifact.artifactType)}</span><p>{artifact.objectKey}</p></div>
              ))}
              {artifacts.length === 0 ? <p className="hero-copy">尚未生成导出工件。</p> : null}
            </div>
          </div>
        </section>
      </main>
    </div>
  )
}
