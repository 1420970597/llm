import type { Dataset } from '../lib/api'

/**
 * 数据清洗模块入口（L14 独占实现）。
 *
 * 契约：docs/plans/eval-and-cleaning-plan.md 第 4.2 节。
 * 该占位实现只负责保证 foundation 阶段可编译；L14 替换内部实现，但必须保持 props 签名不变。
 */
export function CleaningView({ datasets }: { datasets: Dataset[] }) {
  return (
    <section>
      <h2>数据清洗</h2>
      <p>当前可清洗的数据集数量：{datasets.length}</p>
    </section>
  )
}

export default CleaningView
