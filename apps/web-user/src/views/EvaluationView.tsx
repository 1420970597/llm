import type { Dataset } from '../lib/api'

/**
 * 质量评估模块入口（L13 独占实现）。
 *
 * 契约：docs/plans/eval-and-cleaning-plan.md 第 4.2 节。
 * 该占位实现只负责保证 foundation 阶段可编译；L13 替换内部实现，但必须保持 props 签名不变。
 */
export function EvaluationView({ datasets }: { datasets: Dataset[] }) {
  return (
    <section>
      <h2>质量评估</h2>
      <p>当前可评估的数据集数量：{datasets.length}</p>
    </section>
  )
}

export default EvaluationView
