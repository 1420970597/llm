/**
 * 清洗页派生逻辑的自检（无框架，直接 node 运行）。
 *
 * 覆盖三处容易静默出错、且没有后端断言能兜住的客户端逻辑：
 *   1. mergeReportRun：报告自带的 run 是 MarkDone 之前的快照，必须用运行列表覆盖，
 *      否则界面显示「排队中 · 检查 0 条」且轮询永不停止；
 *   2. deriveSeverityDistribution：findings.keywordId 与关键词库 severity 的 join；
 *   3. sortRulesByPriority：必须与 cleaning.EvaluateRules 的判定顺序一致。
 *
 * 运行：
 *   node test/l14_meta_selfcheck.mjs
 */

import { mkdtempSync, rmSync, writeFileSync } from 'node:fs'
import { createRequire } from 'node:module'
import { tmpdir } from 'node:os'
import path from 'node:path'
import { fileURLToPath } from 'node:url'

const REPO_ROOT = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..')
const WEB_ROOT = path.join(REPO_ROOT, 'apps', 'web-user')

const webRequire = createRequire(path.join(WEB_ROOT, 'package.json'))
const esbuild = webRequire('esbuild')

const workDir = mkdtempSync(path.join(tmpdir(), 'l14-meta-'))
const outfile = path.join(workDir, 'meta.cjs')
esbuild.buildSync({
  entryPoints: [path.join(WEB_ROOT, 'src/views/cleaning/cleaningMeta.ts')],
  bundle: true,
  format: 'cjs',
  platform: 'node',
  outfile,
  logLevel: 'warning',
})

const { mergeReportRun, deriveSeverityDistribution, sortRulesByPriority } = createRequire(import.meta.url)(outfile)

let failures = 0
function check(name, ok, detail) {
  console.log(`[${ok ? 'PASS' : 'FAIL'}] ${name}: ${detail}`)
  if (!ok) {
    failures += 1
  }
}

try {
  // --- mergeReportRun：陈旧快照必须被列表值覆盖 ---
  const staleReport = {
    run: { id: 9, datasetId: 162, status: 'queued', scannedItems: 0, flaggedItems: 0, droppedItems: 0, stages: [], report: {}, errorSummary: '', createdAt: '', updatedAt: '' },
    stages: [],
    topKeywords: [],
    conclusions: ['清洗尚未完成，当前状态：queued'],
    generatedAt: '',
  }
  const freshRuns = [
    { id: 9, datasetId: 162, status: 'completed', scannedItems: 30, flaggedItems: 4, droppedItems: 3, stages: ['question'], report: {}, errorSummary: '', createdAt: '', updatedAt: '' },
  ]
  const merged = mergeReportRun(staleReport, freshRuns)
  check(
    'mergeReportRun 用列表最新值覆盖陈旧快照',
    merged.status === 'completed' && merged.scannedItems === 30 && merged.droppedItems === 3,
    `status=${merged.status} scanned=${merged.scannedItems} dropped=${merged.droppedItems}`,
  )
  check(
    'mergeReportRun 在列表缺失时退回快照（不丢信息）',
    mergeReportRun(staleReport, []).status === 'queued',
    '列表为空时保留快照',
  )
  check('mergeReportRun 对 null 报告返回 null', mergeReportRun(null, freshRuns) === null, 'null 安全')

  // --- deriveSeverityDistribution：客户端 join ---
  const keywords = [
    { id: 1, severity: 'block' },
    { id: 2, severity: 'warn' },
  ]
  const findings = [
    { keywordId: 1 },
    { keywordId: 1 },
    { keywordId: 2 },
    { keywordId: 999 },
  ]
  const dist = deriveSeverityDistribution(findings, keywords)
  check(
    'deriveSeverityDistribution 正确 join 并保留未知来源',
    dist.block === 2 && dist.warn === 1 && dist.unknown === 1 && dist.total === 4,
    `block=${dist.block} warn=${dist.warn} unknown=${dist.unknown} total=${dist.total}`,
  )

  // --- sortRulesByPriority：与 EvaluateRules 判定顺序一致（数字小的先判）---
  const rules = [
    { id: 3, priority: 50 },
    { id: 1, priority: 10 },
    { id: 2, priority: 10 },
  ]
  const ordered = sortRulesByPriority(rules).map((rule) => rule.id)
  check(
    'sortRulesByPriority 优先级升序、同级按 id 稳定',
    ordered.join(',') === '1,2,3',
    `顺序=${ordered.join(',')}`,
  )
} finally {
  rmSync(workDir, { recursive: true, force: true })
}

console.log('')
if (failures > 0) {
  console.error(`SELF-CHECK FAILED: ${failures} 项未通过`)
  process.exit(1)
}
console.log('SELF-CHECK OK: 全部通过')
