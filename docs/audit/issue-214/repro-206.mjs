/**
 * #214 子项 #206 复现/验证：批次详情「事件时间线」的事件文案。
 *
 * 判定：时间线的可见事件文本里不得出现内部英文事件键（BatchXxx）。
 *
 * 用法：node docs/audit/issue-214/repro-206.mjs before|after
 */
import { createRequire } from 'node:module'
import { writeFileSync } from 'node:fs'
import { dirname, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

const require = createRequire(import.meta.url)
const { chromium } = require(process.env.PLAYWRIGHT_PATH ?? '/root/.pi/agent/npm/node_modules/playwright')

const here = dirname(fileURLToPath(import.meta.url))
const stage = process.argv[2] === 'after' ? 'after' : 'before'
const BASE = process.env.BASE_URL ?? 'http://127.0.0.1:3210'
const BATCH = process.env.BATCH_ID ?? 'b_1'

const browser = await chromium.launch({ headless: true })
const ctx = await browser.newContext({ viewport: { width: 1600, height: 1000 }, locale: 'zh-CN' })
const page = await ctx.newPage()
const errors = []
page.on('pageerror', (e) => errors.push(String(e).slice(0, 120)))

await page.goto(`${BASE}/login`, { waitUntil: 'networkidle' })
await page.getByPlaceholder('请输入邮箱').fill(process.env.ADMIN_EMAIL ?? 'admin@company.com')
await page.getByPlaceholder('请输入密码').fill(process.env.ADMIN_PASSWORD ?? 'admin123456')
await page.getByRole('button', { name: '进入今日工作' }).click()
await page.waitForURL(/\/today/, { timeout: 20000 })

await page.goto(`${BASE}/p/1/runs/${BATCH}`, { waitUntil: 'networkidle' })
await page.waitForTimeout(1800)

const card = page.locator('.batch-events')
const report = { stage, batch: BATCH }
report.eventRows = await card.locator('li').evaluateAll((rows) => rows.slice(0, 8).map((row) => ({
  type: row.querySelector('.batch-events__type')?.textContent?.trim() ?? '',
  typeTitle: row.querySelector('.batch-events__type')?.getAttribute('title') ?? '',
  detail: row.querySelector('.batch-events__detail')?.textContent?.trim() ?? '',
})))

// 机器判定：可见事件文本里不得出现内部事件键形态（Batch 开头 + 驼峰）。
report.visibleEnumKeys = report.eventRows
  .map((r) => (/^Batch[A-Z][A-Za-z]*$/.test(r.type) ? r.type : null))
  .filter(Boolean)
report.consoleErrors = errors

await page.locator('.batch-events').scrollIntoViewIfNeeded().catch(() => {})
await page.screenshot({ path: resolve(here, stage === 'after' ? '02-206-after.png' : '01-206-before.png') })
writeFileSync(resolve(here, stage === 'after' ? '02-206-after.json' : '01-206-before.json'),
  `${JSON.stringify(report, null, 2)}\n`)
await browser.close()
console.log(JSON.stringify(report, null, 2))
process.exit(report.visibleEnumKeys.length > 0 ? 2 : 0)
