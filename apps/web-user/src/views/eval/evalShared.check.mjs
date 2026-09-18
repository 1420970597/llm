/**
 * 评估模块纯逻辑自检（L13）。
 *
 * 运行：cd apps/web-user && node src/views/eval/evalShared.check.mjs
 * 覆盖三处非平凡分支：抽样参数校验、裁判一致性分级、状态标签映射。
 * 后缀 .mjs 是为了让 tsc（未开启 allowJs）跳过它，避免为此改冻结的 tsconfig。
 */
import assert from 'node:assert/strict'
import { agreementLevel, formatPercent, groupByCategory, isRunActive, runStatusMeta, samplingInputError } from './evalShared.ts'

assert.equal(samplingInputError('full', 0, 0), null)
assert.equal(samplingInputError('ratio', 1.2, 20), '抽样比例必须落在 0~1 之间')
assert.equal(samplingInputError('count', 0.3, 0), '抽样条数必须大于 0')
assert.equal(samplingInputError('count', 0.3, 20), null)
assert.equal(agreementLevel(0.7), 'high')
assert.equal(agreementLevel(0.5), 'medium')
assert.equal(agreementLevel(0.49), 'low')
assert.equal(agreementLevel(-1), 'not_applicable')
assert.equal(runStatusMeta('completed').label, '已完成')
assert.equal(isRunActive('queued'), true)
assert.equal(isRunActive('completed'), false)
assert.equal(formatPercent(0.735), '74%')
assert.deepEqual(groupByCategory([{ c: 'b' }, { c: '' }, { c: 'b' }], (x) => x.c).map(([k]) => k), ['b', '未分类'])
console.log('L13 evalShared self-check OK')
