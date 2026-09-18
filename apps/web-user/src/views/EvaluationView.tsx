import { useState } from 'react'
import { Card, TabPane, Tabs, Typography } from '@douyinfe/semi-ui'
import type { Dataset } from '../lib/api'
import { DimensionManager } from './eval/DimensionManager'
import { EvalRunForm } from './eval/EvalRunForm'
import { EvalRunList } from './eval/EvalRunList'
import { EvalRunDetail } from './eval/EvalRunDetail'

const { Title, Text } = Typography

/**
 * 质量评估模块入口（L13 独占实现）。
 *
 * 契约：docs/plans/eval-and-cleaning-plan.md 第 4.2 节，props 签名保持 foundation 版本不变。
 * 四个子页签对应：维度管理（L8）、抽样配置与启动（L9）、运行列表（L9）、结果与报告（L10）。
 */
export function EvaluationView({ datasets }: { datasets: Dataset[] }) {
  const [selectedRunId, setSelectedRunId] = useState<number | null>(null)
  const [tabKey, setTabKey] = useState('dimensions')

  return (
    <div className="console-stack">
      <Card className="console-panel" bodyStyle={{ padding: 20 }}>
        <div className="console-route-banner">
          <span className="console-chip">多模型互评</span>
          <Title heading={2} className="!mb-0 console-page-title">质量评估</Title>
          <Text className="console-page-subtitle">
            维护评估维度、配置抽样与裁判模型、查看逐条打分与汇总结论。当前可评估数据集 {datasets.length} 个。
          </Text>
        </div>
      </Card>

      <Tabs type="line" activeKey={tabKey} onChange={setTabKey}>
        <TabPane tab="维度管理" itemKey="dimensions">
          <DimensionManager />
        </TabPane>
        <TabPane tab="新建评估" itemKey="create">
          <EvalRunForm
            datasets={datasets}
            onCreated={(runId) => {
              setSelectedRunId(runId)
              setTabKey('runs')
            }}
          />
        </TabPane>
        <TabPane tab="运行列表" itemKey="runs">
          <EvalRunList datasets={datasets} selectedRunId={selectedRunId} onSelect={setSelectedRunId} />
        </TabPane>
        <TabPane tab="结果与报告" itemKey="report">
          {selectedRunId === null ? (
            <Card className="console-panel" bodyStyle={{ padding: 20 }}>
              <Text className="console-muted">请先在「运行列表」中选择一条评估运行以查看报告。</Text>
            </Card>
          ) : (
            <EvalRunDetail runId={selectedRunId} />
          )}
        </TabPane>
      </Tabs>
    </div>
  )
}

export default EvaluationView
