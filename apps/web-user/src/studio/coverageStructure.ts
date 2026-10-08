/**
 * 覆盖矩阵 `m × n × z` 结构推导（issue #197 第 11 条 §A）。
 *
 * 契约来源：`internal/model/studio_docs.go` 的 `CoverageCapacity`（可产出量的
 * 权威口径），以及 `docs/plans/issue-197-remediation-delivery.md` 第 11 条。
 *
 * 为什么单独成一个**无 React 依赖**的模块：
 *
 *  1. 这段推导以前内联在 `DocumentEditors.tsx` 组件里，只能靠浏览器点一遍来验，
 *     而它恰好出过一类「公式自己算不通」的缺陷：界面上写
 *     `m 1 × n 2 × z 4 = 4` —— `z` 取的是**全部配额之和**，结果又复用了同一个数，
 *     于是 `1 × 2 × 4 ≠ 4`。一个自称「数据集结构」的公式自相矛盾，用户无法
 *     用它预判「改方向数/配额会不会影响产出量」。
 *  2. `m × n × z` 只有在**每个领域的方向数相同、且每个方向的配额相同**时才是
 *     一个成立的乘积；否则 `n`、`z` 没有单一取值。把这件事显式建模，
 *     界面才能在「不是一个乘积」时给出诚实的表述，而不是硬凑一个算不通的等式。
 *
 * 与后端的口径：`capacity` 必须与 `model.CoverageCapacity` 逐字一致 ——
 * 显式来源方向的零配额不参与生产；旧版本未注明来源时保留 `quota ≤ 0`
 * 回退为 1 的历史语义，避免改变已保存批次的容量。
 */

/** 覆盖矩阵的 `m × n × z` 结构读数。 */
export type CoverageStructure = {
  /** m：领域数。 */
  domainCount: number
  /** n（合计口径）：方向总数，用于非乘积情形下的诚实描述。 */
  directionCount: number
  /** n（乘积口径）：每个领域的方向数；各领域不一致时为 `null`。 */
  uniformDirectionsPerDomain: number | null
  /** z：每个方向的配额；各方向不一致时为 `null`。 */
  uniformQuotaPerDirection: number | null
  /** 本版本最多能产出的单元数（= Σ 方向配额，与 `model.CoverageCapacity` 同口径）。 */
  capacity: number
}

function asDirections(value: unknown): Array<Record<string, unknown>> {
  return Array.isArray(value) ? value.filter((item): item is Record<string, unknown> => Boolean(item) && typeof item === 'object' && !Array.isArray(item)) : []
}

/** 与后端 CoverageCapacity / AllocateCoverageUnits 保持同一兼容口径。 */
export function coverageQuotaOf(direction: Record<string, unknown>): number {
  const quota = Number(direction.quota)
  if (direction.source && (!Number.isFinite(quota) || quota <= 0)) return 0
  return Number.isFinite(quota) && quota > 0 ? quota : 1
}

/**
 * 从覆盖版本 payload 推导 `m × n × z` 结构读数。
 *
 * 不抛异常：payload 来自服务端历史版本，字段可能缺失或形态不同
 * （例如旧版本没有 `quota`）。缺失时按后端口径取 1，而不是猜测一个数字。
 */
export function deriveCoverageStructure(payload: Record<string, unknown>): CoverageStructure {
  const domains = Array.isArray(payload.domains) ? payload.domains.filter((item): item is Record<string, unknown> => Boolean(item) && typeof item === 'object' && !Array.isArray(item)) : []
  const directionsPerDomain = domains.map((domain) => asDirections(domain.directions))
  const directionCount = directionsPerDomain.reduce((total, directions) => total + directions.length, 0)
  const quotas = directionsPerDomain.flatMap((directions) => directions.map(coverageQuotaOf))
  const capacity = quotas.reduce((total, quota) => total + quota, 0)
  return {
    domainCount: domains.length,
    directionCount,
    uniformDirectionsPerDomain: uniformOrNull(directionsPerDomain.map((directions) => directions.length)),
    uniformQuotaPerDirection: uniformOrNull(quotas),
    capacity,
  }
}

function uniformOrNull(values: number[]): number | null {
  if (values.length === 0) return null
  const first = values[0]
  return values.every((value) => value === first) ? first : null
}

/**
 * 结构公式的人话表述。
 *
 * 乘积成立时给 `m {m} × n {n} × z {z} = {capacity}`（这个等式**算术上必须成立**）；
 * 不成立时**不编造等式**，改为并列三个读数 —— 硬凑一个算不通的等式正是 §A 的缺陷形态。
 */
export function formatCoverageFormula(structure: CoverageStructure): string {
  const { domainCount, directionCount, uniformDirectionsPerDomain, uniformQuotaPerDirection, capacity } = structure
  if (uniformDirectionsPerDomain !== null && uniformQuotaPerDirection !== null) {
    return `m ${domainCount} × n ${uniformDirectionsPerDomain} × z ${uniformQuotaPerDirection} = ${capacity}`
  }
  return `m ${domainCount} 领域 · n ${directionCount} 方向 · 计划单元合计 ${capacity}`
}

/** 公式下方的解释文案；随「是否为单一乘积」切换，避免在非乘积情形下仍宣称 `m×n×z`。 */
export function describeCoverageFormula(structure: CoverageStructure): string {
  if (structure.uniformDirectionsPerDomain !== null && structure.uniformQuotaPerDirection !== null) {
    return '领域数 m × 每领域方向数 n × 每方向题数 z = 本版本最多能产出的单元数；启动批次时填的计划量不能超过它（否则会被拒绝，避免静默少交付）。'
  }
  return '各领域的方向数或各方向的配额不一致，无法用单一 m×n×z 乘积表达；上方读数是本版本最多能产出的单元数（Σ 方向配额）。启动批次时填的计划量不能超过它。'
}
