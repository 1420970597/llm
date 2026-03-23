import { FormEvent, useState } from 'react'
import { motion } from 'framer-motion'
import { ArrowRight, BrainCircuit, DatabaseZap, GitBranch, ShieldCheck, Sparkles } from 'lucide-react'

const pillars = [
  {
    icon: BrainCircuit,
    title: 'Domain expansion engine',
    description: 'Turn a single seed keyword into a governed domain graph with tiered coverage and review checkpoints.',
  },
  {
    icon: DatabaseZap,
    title: 'Async data pipeline',
    description: 'Generate questions, reasoning traces, answers, and reward datasets through recoverable stages.',
  },
  {
    icon: ShieldCheck,
    title: 'Enterprise controls',
    description: 'Keep prompt versions, storage routes, quotas, and audit trails under admin control.',
  },
]

const milestones = [
  'Estimate dataset scale and cost before generation.',
  'Refine the domain graph with AI and human-in-the-loop edits.',
  'Launch asynchronous generation for questions, reasoning, and reward data.',
  'Export stable dataset artifacts from object storage-backed snapshots.',
]

export default function App() {
  const [authenticated, setAuthenticated] = useState(false)

  const handleSubmit = (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault()
    setAuthenticated(true)
  }

  return (
    <div className="page-shell">
      <header className="topbar">
        <div className="brand-mark">
          <Sparkles size={18} strokeWidth={1.8} />
          <span>LLM Data Factory</span>
        </div>
        <nav>
          <a href="#workflow">Workflow</a>
          <a href="#governance">Governance</a>
          <a href="http://localhost:3211">Admin</a>
        </nav>
      </header>

      <main>
        <section className="hero-grid">
          <motion.div
            className="hero-card hero-primary"
            initial={{ opacity: 0, y: 24 }}
            animate={{ opacity: 1, y: 0 }}
            transition={{ duration: 0.6, ease: 'easeOut' }}
          >
            <p className="eyebrow">Enterprise data generation control plane</p>
            <h1>Build long-chain reasoning datasets with governed scale and premium operator workflows.</h1>
            <p className="hero-copy">
              Plan dataset size, expand domains, review graph structure, and stage large asynchronous generation jobs
              with storage-aware, audit-ready foundations.
            </p>
            <div className="hero-actions">
              <button className="primary-action">
                Start planning
                <ArrowRight size={16} />
              </button>
              <button className="secondary-action">View system blueprint</button>
            </div>
          </motion.div>

          <motion.aside
            className="hero-card hero-metrics"
            initial={{ opacity: 0, x: 24 }}
            animate={{ opacity: 1, x: 0 }}
            transition={{ duration: 0.7, delay: 0.1, ease: 'easeOut' }}
          >
            {!authenticated ? (
              <form className="login-card" onSubmit={handleSubmit}>
                <div>
                  <span className="eyebrow">Portal access</span>
                  <h2>Sign in to plan and launch dataset generation.</h2>
                  <p>Phase 1 ships a focused login shell so later identity providers can plug in without rewiring the UI.</p>
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
              <>
                <div className="metric-card">
                  <span>Target orchestration</span>
                  <strong>Domain graph → QA → reasoning → reward</strong>
                </div>
                <div className="metric-card">
                  <span>Storage baseline</span>
                  <strong>S3 / PostgreSQL / Redis / MinIO</strong>
                </div>
                <div className="metric-card accent">
                  <span>Visual baseline</span>
                  <strong>Apple-style glass, depth, motion, premium icons</strong>
                </div>
              </>
            )}
          </motion.aside>
        </section>

        <section className="section-block" id="workflow">
          <div className="section-heading">
            <span className="eyebrow">Workflow structure</span>
            <h2>Phase-aligned product shell for future generation tasks</h2>
          </div>
          <div className="pillar-grid">
            {pillars.map(({ icon: Icon, title, description }, index) => (
              <motion.article
                key={title}
                className="glass-card"
                initial={{ opacity: 0, y: 16 }}
                animate={{ opacity: 1, y: 0 }}
                transition={{ duration: 0.45, delay: 0.12 * index }}
              >
                <div className="icon-chip">
                  <Icon size={18} strokeWidth={1.9} />
                </div>
                <h3>{title}</h3>
                <p>{description}</p>
              </motion.article>
            ))}
          </div>
        </section>

        <section className="section-block split-layout" id="governance">
          <div className="glass-card timeline-card">
            <div className="section-heading compact">
              <span className="eyebrow">Generation path</span>
              <h2>Guided from planning to export</h2>
            </div>
            <div className="timeline-list">
              {milestones.map((item, index) => (
                <div key={item} className="timeline-row">
                  <span className="timeline-index">0{index + 1}</span>
                  <p>{item}</p>
                </div>
              ))}
            </div>
          </div>

          <div className="glass-card graph-preview">
            <div className="section-heading compact">
              <span className="eyebrow">Domain graph preview</span>
              <h2>Structured branching before dataset fan-out</h2>
            </div>
            <div className="graph-constellation">
              <div className="graph-node root-node">
                <GitBranch size={16} strokeWidth={1.8} />
                <span>Root keyword</span>
              </div>
              <div className="graph-lane">
                <div className="graph-node">Maritime operations</div>
                <div className="graph-node">Aerospace tactics</div>
                <div className="graph-node">Logistics systems</div>
              </div>
              <div className="graph-lane muted">
                <div className="graph-node small">Scenario branch</div>
                <div className="graph-node small">Capability branch</div>
                <div className="graph-node small">Countermeasure branch</div>
              </div>
            </div>
          </div>
        </section>
      </main>
    </div>
  )
}
