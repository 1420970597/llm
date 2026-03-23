import { useState } from 'react'
import { motion } from 'framer-motion'
import { Button, Card, Col, Form, Input, List, Progress, Row, Table, Tag } from 'antd'
import type { ColumnsType } from 'antd/es/table'
import { Database, FolderCog, Layers3, LockKeyhole, ShieldEllipsis, Workflow } from 'lucide-react'

const metrics = [
  { title: 'Dataset runs', value: '12 active', detail: 'Orchestrated across planning, QA, reasoning, and reward stages.' },
  { title: 'Storage profiles', value: '3 connected', detail: 'MinIO, S3-compatible, and OSS-ready connection slots.' },
  { title: 'Provider health', value: '99.2%', detail: 'Reserved for multi-provider routing and failover control.' },
]

const pipelines = [
  { key: '1', name: 'Military strategy dataset', phase: 'Domain review', health: 68 },
  { key: '2', name: 'Financial reasoning batch', phase: 'Question generation', health: 42 },
  { key: '3', name: 'Medical alignment set', phase: 'Reward synthesis', health: 86 },
]

const columns: ColumnsType<(typeof pipelines)[number]> = [
  { title: 'Dataset', dataIndex: 'name', key: 'name' },
  {
    title: 'Phase',
    dataIndex: 'phase',
    key: 'phase',
    render: (value: string) => <Tag color="blue">{value}</Tag>,
  },
  {
    title: 'Progress',
    dataIndex: 'health',
    key: 'health',
    render: (value: number) => <Progress percent={value} size="small" strokeColor="#9cc2ff" />,
  },
]

export default function App() {
  const [authenticated, setAuthenticated] = useState(false)

  return (
    <div className="admin-shell">
      <motion.section
        className="admin-hero"
        initial={{ opacity: 0, y: 20 }}
        animate={{ opacity: 1, y: 0 }}
        transition={{ duration: 0.5 }}
      >
        <div>
          <span className="eyebrow">Admin control plane</span>
          <h1>Govern prompts, providers, storage routes, and async dataset throughput.</h1>
          <p>
            The Phase 1 admin shell provides the foundation for provider configuration, storage governance,
            audit visibility, and phase-by-phase delivery control.
          </p>
        </div>
      </motion.section>

      {!authenticated ? (
        <Card className="glass-panel login-panel">
          <div className="card-header">
            <div>
              <span className="eyebrow">Secure entry</span>
              <h2>Sign in to unlock the admin workspace.</h2>
            </div>
            <LockKeyhole size={18} strokeWidth={1.8} />
          </div>
          <Form layout="vertical" onFinish={() => setAuthenticated(true)}>
            <Row gutter={[18, 0]}>
              <Col xs={24} md={12}>
                <Form.Item
                  name="email"
                  label="Administrator email"
                  rules={[{ required: true, message: 'Enter an administrator email.' }]}
                >
                  <Input size="large" placeholder="admin@company.com" />
                </Form.Item>
              </Col>
              <Col xs={24} md={12}>
                <Form.Item
                  name="password"
                  label="Access key"
                  rules={[{ required: true, message: 'Enter your access key.' }]}
                >
                  <Input.Password size="large" placeholder="Enter your secure key" />
                </Form.Item>
              </Col>
            </Row>
            <Button type="primary" htmlType="submit" size="large">
              Enter admin workspace
            </Button>
          </Form>
        </Card>
      ) : null}

      <Row gutter={[18, 18]}>
        {metrics.map((item, index) => (
          <Col xs={24} md={8} key={item.title}>
            <motion.div initial={{ opacity: 0, y: 18 }} animate={{ opacity: 1, y: 0 }} transition={{ delay: 0.08 * index }}>
              <Card className="glass-panel metric-panel">
                <div className="metric-title">{item.title}</div>
                <strong>{item.value}</strong>
                <p>{item.detail}</p>
              </Card>
            </motion.div>
          </Col>
        ))}
      </Row>

      <Row gutter={[18, 18]} className="section-row">
        <Col xs={24} xl={16}>
          <Card className="glass-panel">
            <div className="card-header">
              <div>
                <span className="eyebrow">Pipeline monitor</span>
                <h2>Generation lanes</h2>
              </div>
              <Workflow size={18} strokeWidth={1.8} />
            </div>
            <Table columns={columns} dataSource={pipelines} pagination={false} rowKey="key" />
          </Card>
        </Col>
        <Col xs={24} xl={8}>
          <Card className="glass-panel">
            <div className="card-header">
              <div>
                <span className="eyebrow">Foundation modules</span>
                <h2>Phase 1 scope</h2>
              </div>
              <Layers3 size={18} strokeWidth={1.8} />
            </div>
            <List
              dataSource={[
                { icon: FolderCog, label: 'Provider and storage configuration' },
                { icon: Database, label: 'Compose-backed infrastructure topology' },
                { icon: ShieldEllipsis, label: 'Audit-ready admin information architecture' },
              ]}
              renderItem={(item) => (
                <List.Item className="list-item">
                  <item.icon size={18} strokeWidth={1.8} />
                  <span>{item.label}</span>
                </List.Item>
              )}
            />
          </Card>
        </Col>
      </Row>
    </div>
  )
}
