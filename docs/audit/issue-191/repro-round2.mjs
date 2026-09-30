/**
 * #191 第 2 轮：把「第 1 轮未收口的两条渲染路径」一次性机器化判定。
 *
 * 两条路径（第 1 轮评论里逐项列出）：
 *   A. 旧控制台「系统设置 → 操作记录」的「资源」列（`/console/admin/audit`）
 *      —— 第 1 轮已修，本轮复跑确认没有回归；
 *   B. 批次详情「事件时间线」（`/p/1/runs/b_1`）
 *      —— 第 1 轮明确标注「未修复」，已由 #206 单独 tracked，
 *         其修复随 PR #220 合入 main。本轮复跑确认它真的不再漏出内部键。
 *
 * 判定（与第 1 轮同条件：同账号、同视口、同路由）：
 *   - 资源列的**可见文本**不得含 snake_case 内部键；
 *   - 时间线的**可见文本**不得含 `BatchXxx` 驼峰内部键。
 *
 * 用法：node docs/audit/issue-191/repro-round2.mjs before|after
 * 产物：docs/audit/issue-191/round2-<stage>-audit-resource.png / -event-timeline.png
 *       docs/audit/issue-191/round2-<stage>.json
 */
import { createRequire } from 'node:module'
import { writeFileSync } from 'node:fs'
import { dirname, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

const require = createRequire(import.meta.url)
const { chromium } = require(process.env.PLAYWRIGHT_PATH ?? '/root/.pi/agent/npm/node_modules/playwright')

const here = dirname(fileURLToPath(import.meta.url))
const stage = process.argv[2] === 'after' ? 'after' : 'before'
const BASE = process.env.ISSUE191_BASE ?? 'http://127.0.0.1:3210'
const EMAIL = process.env.ADMIN_EMAIL ?? 'admin@company.com'
const PASSWORD = process.env.ADMIN_PASSWORD ?? 'admin123456'
const BATCH = process.env.BATCH_ID ?? 'b_1'

const browser = await chromium.launch({ headless: true })
const ctx = await browser.newContext({ viewport: { width: 1600, height: 1000 }, locale: 'zh-CN' })
const page = await ctx.newPage()
const errors = []
page.on('pageerror', (e) => errors.push(`pageerror: ${String(e).slice(0, 160)}`))
page.on('response', (r) => { if (r.status() >= 500) errors.push(`HTTP ${r.status()} ${r.url().slice(0, 90)}`) })

const report = { stage, base: BASE }

await page.goto(`${BASE}/login`, { waitUntil: 'networkidle' })
await page.getByPlaceholder('请输入邮箱').fill(EMAIL)
await page.getByPlaceholder('请输入密码').fill(PASSWORD)
await page.getByRole('button', { name: '进入今日工作' }).click()
await page.waitForURL(/\/today/, { timeout: 20000 })
await page.waitForTimeout(800)

// ---------------------------------------------------------------- 路径 A ----
await page.goto(`${BASE}/console/admin/audit`, { waitUntil: 'networkidle' })
await page.waitForTimeout(1500)
const auditTable = page.locator('.semi-table').first()
report.audit = { tableHead: await auditTable.locator('thead th').allInnerTexts().catch(() => []) }
report.audit.resourceCells = await auditTable.locator('tbody tr').evaluateAll((rows) => rows.slice(0, 10).map((row) => {
  const cell = row.querySelectorAll('td')[2]
  if (!cell) return null
  return { text: (cell.textContent ?? '').trim(), title: cell.getAttribute('title') ?? '' }
}).filter(Boolean))
const snake = /(^|[^A-Za-z0-9_])([a-z][a-z0-9]*(?:_[a-z0-9]+)+)(?![A-Za-z0-9_])/
report.audit.visibleEnumKeys = report.audit.resourceCells.map((c) => snake.exec(c.text)?.[2] ?? null).filter(Boolean)
await page.screenshot({ path: resolve(here, `round2-${stage}-audit-resource.png`) })

// ---------------------------------------------------------------- 路径 B ----
await page.goto(`${BASE}/p/1/runs/${BATCH}`, { waitUntil: 'networkidle' })
await page.waitForTimeout(1800)
const timeline = page.locator('.batch-events')
report.timeline = { batch: BATCH }
report.timeline.eventRows = await timeline.locator('li').evaluateAll((rows) => rows.slice(0, 8).map((row) => ({
  type: row.querySelector('.batch-events__type')?.textContent?.trim() ?? '',
  typeTitle: row.querySelector('.batch-events__type')?.getAttribute('title') ?? '',
})))
report.timeline.visibleEnumKeys = report.timeline.eventRows
  .map((r) => (/^Batch[A-Z][A-Za-z]*$/.test(r.type) ? r.type : null))
  .filter(Boolean)
await timeline.scrollIntoViewIfNeeded().catch(() => {})
await page.screenshot({ path: resolve(here, `round2-${stage}-event-timeline.png`) })

report.consoleErrors = errors
writeFileSync(resolve(here, `round2-${stage}.json`), `${JSON.stringify(report, null, 2)}\n`)
await browser.close()

const leaked = report.audit.visibleEnumKeys.length + report.timeline.visibleEnumKeys.length
console.log(JSON.stringify(report, null, 2))
process.exit(leaked > 0 ? 2 : 0)
