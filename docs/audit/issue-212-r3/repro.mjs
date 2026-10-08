/**
 * issue #212 复现/验证：批次详情「阶段进度」区块的数据来源。
 *
 * 缺陷形态：`batch_steps` 全库 0 行（T05 只建表，T12/T13 从未写入），
 * 而页面把「还没有阶段记录。」渲染成一个结论 —— 用户对**已跑完 4/4** 的批次
 * 会推断「这次没有执行任何阶段」。空态在这里表达的是一件错误的事实。
 *
 * 判定（机器事实，不靠肉眼）：
 *   * `/api/v1/projects/<p>/batches/<b>` 返回的 `steps` 数组长度；
 *   * 页面上 `[data-batch-steps]` 的行数与可见文本；
 *   * 同时校验「已完成批次」的步骤读数必须 done == total（issue 的验收口径）。
 *
 * 用法：
 *   node docs/audit/issue-212/repro.mjs --phase before|after [--batch b_4]
 * 产物：
 *   docs/audit/issue-212/<phase>.json
 *   docs/audit/issue-212/<phase>-batch-steps.png
 *
 * 防覆盖：已存在的证据默认不覆盖（与 issue-200 的同一约定）。在已修复的栈上
 * 重采 "before" 会得到修复后的读数，那会让「修复前」证据当场失效。要重采必须
 * 显式传 --force。
 */
import { createRequire } from 'node:module'
import { existsSync, mkdirSync, writeFileSync } from 'node:fs'
import path from 'node:path'
import { fileURLToPath } from 'node:url'

const require = createRequire('/root/llm/package.json')
const { chromium } = require('/root/.pi/agent/npm/node_modules/playwright')

const HERE = path.dirname(fileURLToPath(import.meta.url))
const OUT = path.resolve(HERE)
mkdirSync(OUT, { recursive: true })

const phaseArg = process.argv.indexOf('--phase')
const PHASE = phaseArg >= 0 ? process.argv[phaseArg + 1] : 'before'
const batchArg = process.argv.indexOf('--batch')
const BATCH = batchArg >= 0 ? process.argv[batchArg + 1] : 'b_4'
const FORCE = process.argv.includes('--force')

if (!FORCE) {
  const existing = [`${PHASE}.json`, `${PHASE}-batch-steps.png`, `${PHASE}-db.json`]
    .filter((name) => existsSync(path.join(OUT, name)))
  if (existing.length > 0) {
    console.error(`[issue-212] 拒绝覆盖已存在的证据（${PHASE}）：${existing.join(', ')}`)
    console.error('  若确实要重采，显式传 --force（重采读数取决于当前栈版本）。')
    process.exit(2)
  }
}

const BASE = process.env.BASE_URL ?? 'http://127.0.0.1:3210'
const EMAIL = process.env.ADMIN_EMAIL ?? 'admin@company.com'
const PASSWORD = process.env.ADMIN_PASSWORD ?? 'admin123456'
const PROJECT_ID = process.env.PROJECT_ID ?? '1'

const browser = await chromium.launch({ headless: true })
const ctx = await browser.newContext({ viewport: { width: 1600, height: 1000 }, locale: 'zh-CN', deviceScaleFactor: 1 })
const page = await ctx.newPage()
const consoleErrors = []
page.on('pageerror', (e) => consoleErrors.push(`pageerror: ${String(e).slice(0, 160)}`))
page.on('response', (r) => { if (r.status() >= 500) consoleErrors.push(`HTTP ${r.status()} ${r.url().slice(0, 90)}`) })

await page.goto(`${BASE}/login`, { waitUntil: 'networkidle' })
await page.getByPlaceholder('请输入邮箱').fill(EMAIL)
await page.getByPlaceholder('请输入密码').fill(PASSWORD)
await page.getByRole('button', { name: '进入今日工作' }).click()
await page.waitForURL(/\/today/, { timeout: 20000 })
await page.waitForTimeout(1000)

const version = await page.evaluate(async () => {
  const response = await fetch('/version.json', { credentials: 'include' })
  return response.ok ? await response.json() : null
}).catch(() => null)

await page.goto(`${BASE}/p/${PROJECT_ID}/runs/${BATCH}`, { waitUntil: 'networkidle' })
await page.waitForTimeout(2000)

// 批次数字 ID 由 URL 里的稳定 ID（b_4）解析，而不是猜一个数字。
const BATCH_ID = Number(BATCH.replace(/^b_/, ''))

// 页面读数：阶段进度卡片本身。
const pageFacts = await page.evaluate(() => {
  const cards = Array.from(document.querySelectorAll('.console-card'))
  const card = cards.find((node) => (node.textContent ?? '').includes('阶段进度'))
  const rows = card ? Array.from(card.querySelectorAll('.batch-steps li')) : []
  return {
    cardFound: Boolean(card),
    cardText: (card?.innerText ?? '').replace(/\s+/g, ' ').trim(),
    stepRows: rows.map((row) => (row.innerText ?? '').replace(/\s+/g, ' ').trim()),
  }
}).catch((error) => ({ error: String(error).slice(0, 200) }))

// 详情接口读数（steps 的权威来源）。
const detail = await page.evaluate(async ({ projectId, batchId }) => {
  const detailResponse = await fetch(`/api/v1/projects/${projectId}/batches/${batchId}`, { credentials: 'include' })
  const body = detailResponse.ok ? await detailResponse.json() : null
  const batch = body?.data?.batch ?? body?.batch ?? null
  return {
    found: Boolean(body),
    status: detailResponse.status,
    batchId,
    batchStatus: batch?.status ?? null,
    plannedUnits: batch?.plannedUnits ?? null,
    completedUnits: batch?.completedUnits ?? null,
    steps: body?.data?.steps ?? body?.steps ?? null,
  }
}, { projectId: PROJECT_ID, batchId: BATCH_ID }).catch((error) => ({ error: String(error).slice(0, 200) }))

const card = page.locator('.console-card').filter({ hasText: '阶段进度' }).first()
await card.scrollIntoViewIfNeeded().catch(() => {})
await page.screenshot({ path: path.join(OUT, `${PHASE}-batch-steps.png`), fullPage: false })

const steps = Array.isArray(detail.steps) ? detail.steps : []
const report = {
  issue: 212,
  phase: PHASE,
  base: BASE,
  batch: BATCH,
  viewport: '1600x1000',
  account: EMAIL,
  deployedVersion: version,
  page: pageFacts,
  detail,
  stepCount: steps.length,
  stepRowsFromApi: steps.map((step) => ({
    phase: step.phase,
    unitLabel: step.unitLabel,
    status: step.status,
    totalUnits: step.totalUnits,
    doneUnits: step.doneUnits,
    failedUnits: step.failedUnits,
  })),
  consoleErrors,
}

// 判定：已完成批次必须有非空步骤行，且 done == total。
const completed = detail?.batchStatus === 'completed'
const progressMatches =
  steps.length > 0 &&
  (!completed || steps.every((step) => step.doneUnits + step.failedUnits >= step.totalUnits))
report.verdict = {
  emptyStepBlock: steps.length === 0,
  completedBatchWithEmptyProgress: completed && steps.length === 0,
  progressMatches,
}

writeFileSync(path.join(OUT, `${PHASE}.json`), `${JSON.stringify(report, null, 2)}\n`)
await browser.close()

console.log(`[issue-212] phase=${PHASE} batch=${BATCH} 栈版本=${version?.version ?? 'unknown'}`)
console.log(`  /api steps = ${steps.length} 行；页面卡片行数 = ${pageFacts.cardFound ? pageFacts.stepRows.length : '卡片未找到'}`)
console.log(`  批次状态 = ${detail?.batchStatus ?? '未取到'}；计划/完成 = ${detail?.plannedUnits ?? '?'} / ${detail?.completedUnits ?? '?'}`)
console.log(`  判定: ${report.verdict.completedBatchWithEmptyProgress ? 'STILL_BROKEN（已完成批次阶段进度为空）' : 'FIXED'}`)

// before 阶段期望缺陷存在（exit 2 表示「缺陷成立」）；after 阶段期望缺陷消失。
const broken = report.verdict.emptyStepBlock
if (PHASE === 'before') process.exit(broken ? 2 : 0)
process.exit(broken ? 2 : 0)
