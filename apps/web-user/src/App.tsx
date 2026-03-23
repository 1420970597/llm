import { FormEvent, useEffect, useMemo, useState } from 'react'
import { motion } from 'framer-motion'
import { ArrowRight, BrainCircuit, DatabaseZap, GitBranch, Save, ShieldCheck, Sparkles } from 'lucide-react'
import { Dataset, DatasetGraph, Domain, Provider, Question, StorageProfile, Strategy, userApi } from './lib/api'

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
      <svg viewBox={`0 0 ${width} ${height}`} className="graph-svg" role="img" aria-label="domain graph preview">
        {positioned.map((node) => (
          <line key={`line-${node.id}`} x1={centerX} y1={centerY} x2={node.x} y2={node.y} stroke="rgba(163, 204, 255, 0.28)" strokeWidth="1.5" />
        ))}
        <circle cx={centerX} cy={centerY} r="48" fill="rgba(148, 189, 255, 0.16)" stroke="rgba(190, 214, 255, 0.55)" />
        <text x={centerX} y={centerY} textAnchor="middle" dominantBaseline="middle" className="graph-root-text">{rootKeyword}</text>
        {positioned.map((node) => (
          <g key={node.id}>
            <circle cx={node.x} cy={node.y} r="30" fill="rgba(12, 22, 40, 0.88)" stroke="rgba(148, 189, 255, 0.55)" />
            <text x={node.x} y={node.y} textAnchor="middle" dominantBaseline="middle" className="graph-node-text">{node.name.slice(0, 10)}</text>
          </g>
        ))}
      </svg>
      {domains.length > visible.length ? <p className="graph-caption">Showing 16 of {domains.length} generated domains in the preview graph.</p> : null}
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

  const loadBootstrap = async () => {
    const [strategyData, providerData, storageData, datasetData] = await Promise.all([
      userApi.listStrategies(),
      userApi.listProviders(),
      userApi.listStorageProfiles(),
      userApi.listDatasets(),
    ])
    setStrategies(strategyData)
    setProviders(providerData)
    setStorageProfiles(storageData)
    setDatasets(datasetData)
    setFormState((current) => ({
      ...current,
      strategyId: current.strategyId || strategyData[0]?.id || 0,
      providerId: current.providerId || providerData[0]?.id || 0,
      storageProfileId: current.storageProfileId || storageData[0]?.id || 0,
    }))
  }

  useEffect(() => {
    if (!authenticated) {
      return
    }
    void loadBootstrap().catch((error: Error) => setMessage(error.message))
  }, [authenticated])

  const plannerCards = useMemo(() => {
    if (!estimate) {
      return []
    }
    return [
      { label: 'Domains', value: estimate.domainCount },
      { label: 'Questions / domain', value: estimate.questionsPerDomain },
      { label: 'Estimated questions', value: estimate.estimatedQuestions },
      { label: 'Estimated samples', value: estimate.estimatedSamples },
    ]
  }, [estimate])

  const submitLogin = (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault()
    setAuthenticated(true)
    setMessage('Workspace unlocked. Configure planning inputs and generate a domain graph.')
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
      setMessage('Dataset scale estimate refreshed.')
    } catch (error) {
      setMessage((error as Error).message)
    } finally {
      setLoading(false)
    }
  }

  const createDataset = async () => {
    if (!estimate) {
      setMessage('Please estimate the dataset plan first.')
      return
    }
    setLoading(true)
    try {
      const created = await userApi.createDataset({
        name: formState.name || `${formState.rootKeyword} dataset`,
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
      setDatasets(await userApi.listDatasets())
      setMessage('Dataset created. You can now generate its domain graph.')
    } catch (error) {
      setMessage((error as Error).message)
    } finally {
      setLoading(false)
    }
  }

  const generateDomains = async () => {
    if (!graph) {
      setMessage('Create a dataset before generating domains.')
      return
    }
    setLoading(true)
    try {
      const nextGraph = await userApi.generateDomains(graph.dataset.id)
      setGraph(nextGraph)
      setMessage(`Generated ${nextGraph.domains.length} domains for review.`)
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
    if (!graph) {
      return
    }
    setLoading(true)
    try {
      await userApi.updateGraph(graph.dataset.id, graph.domains)
      setMessage('Graph edits saved.')
    } catch (error) {
      setMessage((error as Error).message)
    } finally {
      setLoading(false)
    }
  }

  const confirmDomains = async () => {
    if (!graph) {
      return
    }
    setLoading(true)
    try {
      await userApi.confirmDomains(graph.dataset.id)
      const refreshed = await userApi.getDataset(graph.dataset.id)
      setGraph(refreshed)
      setMessage('Domains confirmed. The dataset is ready for question generation.')
    } catch (error) {
      setMessage((error as Error).message)
    } finally {
      setLoading(false)
    }
  }

  const generateQuestions = async () => {
    if (!graph) {
      return
    }
    setLoading(true)
    try {
      await userApi.generateQuestions(graph.dataset.id)
      setMessage('Question generation job queued. Refresh questions in a moment to see the results.')
    } catch (error) {
      setMessage((error as Error).message)
    } finally {
      setLoading(false)
    }
  }

  const refreshQuestions = async () => {
    if (!graph) {
      return
    }
    setLoading(true)
    try {
      setQuestions(await userApi.listQuestions(graph.dataset.id))
      setMessage('Question preview refreshed.')
    } catch (error) {
      setMessage((error as Error).message)
    } finally {
      setLoading(false)
    }
  }

  return (
    <div className="page-shell">
      <header className="topbar">
        <div className="brand-mark">
          <Sparkles size={18} strokeWidth={1.8} />
          <span>LLM Data Factory</span>
        </div>
        <nav>
          <a href="#planner">Planner</a>
          <a href="#graph">Graph</a>
          <a href="http://localhost:3211">Admin</a>
        </nav>
      </header>

      <main>
        <section className="hero-grid">
          <motion.div className="hero-card hero-primary" initial={{ opacity: 0, y: 24 }} animate={{ opacity: 1, y: 0 }} transition={{ duration: 0.6, ease: 'easeOut' }}>
            <p className="eyebrow">Enterprise data generation control plane</p>
            <h1>Plan dataset scope, generate a governed domain graph, and prepare the pipeline for large-scale reasoning data.</h1>
            <p className="hero-copy">The user workspace now estimates sample volume, creates datasets, generates domain graphs, and supports human-in-the-loop graph confirmation before question fan-out.</p>
            <div className="hero-actions">
              <button className="primary-action" onClick={() => void estimatePlan()} disabled={loading}>
                Estimate plan
                <ArrowRight size={16} />
              </button>
              <button className="secondary-action" onClick={() => void createDataset()} disabled={loading}>Create dataset</button>
            </div>
            {message ? <p className="status-banner">{message}</p> : null}
          </motion.div>

          <motion.aside className="hero-card hero-metrics" initial={{ opacity: 0, x: 24 }} animate={{ opacity: 1, x: 0 }} transition={{ duration: 0.7, delay: 0.1, ease: 'easeOut' }}>
            {!authenticated ? (
              <form className="login-card" onSubmit={submitLogin}>
                <div>
                  <span className="eyebrow">Portal access</span>
                  <h2>Sign in to create and review dataset plans.</h2>
                  <p>Phase 3 unlocks a working planner connected to the live API and admin-configured generation strategies.</p>
                </div>
                <label>
                  Workspace email
                  <input type="email" placeholder="operator@company.com" required />
                </label>
                <label>
                  Access key
                  <input type="password" placeholder="Enter your secure key" required />
                </label>
                <button type="submit" className="primary-action full-width">
                  Enter workspace
                  <ArrowRight size={16} />
                </button>
              </form>
            ) : (
              <div className="planner-panel" id="planner">
                <div className="planner-grid">
                  <label>
                    Dataset name
                    <input value={formState.name} onChange={(event) => setFormState({ ...formState, name: event.target.value })} placeholder="Military reasoning factory" />
                  </label>
                  <label>
                    Root keyword
                    <input value={formState.rootKeyword} onChange={(event) => setFormState({ ...formState, rootKeyword: event.target.value })} />
                  </label>
                  <label>
                    Target size
                    <input type="number" value={formState.targetSize} onChange={(event) => setFormState({ ...formState, targetSize: Number(event.target.value) })} />
                  </label>
                  <label>
                    Strategy
                    <select value={formState.strategyId} onChange={(event) => setFormState({ ...formState, strategyId: Number(event.target.value) })}>
                      {strategies.map((strategy) => <option key={strategy.id} value={strategy.id}>{strategy.name}</option>)}
                    </select>
                  </label>
                  <label>
                    Provider
                    <select value={formState.providerId} onChange={(event) => setFormState({ ...formState, providerId: Number(event.target.value) })}>
                      {providers.map((provider) => <option key={provider.id} value={provider.id}>{provider.name}</option>)}
                    </select>
                  </label>
                  <label>
                    Storage profile
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
          </motion.aside>
        </section>

        <section className="section-block" id="workflow">
          <div className="section-heading">
            <span className="eyebrow">Workflow structure</span>
            <h2>Phase-aligned product shell for future generation tasks</h2>
          </div>
          <div className="pillar-grid">
            {[
              { icon: BrainCircuit, title: 'Plan estimate', description: 'Translate target sample size into domain and question quotas before any expensive generation step.' },
              { icon: DatabaseZap, title: 'Dataset creation', description: 'Persist the dataset plan with selected strategy, provider, and storage profile.' },
              { icon: ShieldCheck, title: 'Graph review', description: 'Generate draft domains, refine names, and confirm the graph before downstream question generation.' },
            ].map(({ icon: Icon, title, description }, index) => (
              <motion.article key={title} className="glass-card" initial={{ opacity: 0, y: 16 }} animate={{ opacity: 1, y: 0 }} transition={{ duration: 0.45, delay: 0.12 * index }}>
                <div className="icon-chip"><Icon size={18} strokeWidth={1.9} /></div>
                <h3>{title}</h3>
                <p>{description}</p>
              </motion.article>
            ))}
          </div>
        </section>

        <section className="section-block split-layout" id="graph">
          <div className="glass-card timeline-card">
            <div className="section-heading compact">
              <span className="eyebrow">Domain graph review</span>
              <h2>Inspect generated domains before fan-out</h2>
            </div>
            {graph ? (
              <>
                <GraphPreview rootKeyword={graph.dataset.rootKeyword} domains={graph.domains} />
                <div className="graph-actions">
                  <button className="secondary-action" onClick={() => void generateDomains()} disabled={loading}>Generate or refresh domains</button>
                  <button className="secondary-action" onClick={() => void saveGraph()} disabled={loading}><Save size={15} /> Save edits</button>
                  <button className="primary-action" onClick={() => void confirmDomains()} disabled={loading}>Confirm domains</button>
                </div>
              </>
            ) : (
              <p className="hero-copy">Create a dataset to unlock graph generation and review.</p>
            )}
          </div>

          <div className="glass-card graph-preview">
            <div className="section-heading compact">
              <span className="eyebrow">Domain list editor</span>
              <h2>Human-in-the-loop naming adjustments</h2>
            </div>
            {graph ? (
              <div className="domain-list">
                {graph.domains.slice(0, 20).map((domain) => (
                  <label key={domain.id} className="domain-item">
                    <span>{domain.source}</span>
                    <input value={domain.name} onChange={(event) => renameDomain(domain.id, event.target.value)} />
                  </label>
                ))}
                {graph.domains.length > 20 ? <p className="graph-caption">Only the first 20 domains are editable in this Phase 3 UI slice; all {graph.domains.length} domains stay persisted.</p> : null}
              </div>
            ) : (
              <p className="hero-copy">No dataset graph loaded yet.</p>
            )}
          </div>
        </section>

        <section className="section-block">
          <div className="section-heading compact">
            <span className="eyebrow">Recent datasets</span>
            <h2>Created planning runs</h2>
          </div>
          <div className="dataset-grid">
            {datasets.map((dataset) => (
              <button key={dataset.id} className="glass-card dataset-card" onClick={async () => { setGraph(await userApi.getDataset(dataset.id)); setQuestions(await userApi.listQuestions(dataset.id)); }}>
                <div>
                  <span className="eyebrow">{dataset.status}</span>
                  <h3>{dataset.name}</h3>
                </div>
                <p>{dataset.rootKeyword} · {dataset.estimate.estimatedSamples} estimated samples</p>
                <div className="dataset-meta"><GitBranch size={15} /> Dataset #{dataset.id}</div>
              </button>
            ))}
          </div>
        </section>

        <section className="section-block split-layout">
          <div className="glass-card timeline-card">
            <div className="section-heading compact">
              <span className="eyebrow">Question generation</span>
              <h2>Asynchronous fan-out from confirmed domains</h2>
            </div>
            <div className="graph-actions">
              <button className="primary-action" onClick={() => void generateQuestions()} disabled={loading || !graph}>Queue question generation</button>
              <button className="secondary-action" onClick={() => void refreshQuestions()} disabled={loading || !graph}>Refresh questions</button>
            </div>
            <p className="hero-copy">Phase 4 pushes question generation into the Redis-backed worker so the user flow stays responsive.</p>
          </div>

          <div className="glass-card graph-preview">
            <div className="section-heading compact">
              <span className="eyebrow">Question preview</span>
              <h2>Generated dataset questions</h2>
            </div>
            <div className="domain-list">
              {questions.slice(0, 12).map((question) => (
                <div key={question.id} className="question-item">
                  <span>{question.domainName}</span>
                  <p>{question.content}</p>
                </div>
              ))}
              {questions.length === 0 ? <p className="hero-copy">No generated questions yet.</p> : null}
            </div>
          </div>
        </section>
      </main>
    </div>
  )
}
