/**
 * #206 复核：批次详情「事件时间线」事件文案。
 *
 * 同条件复跑（与原始取证一致：真实栈 127.0.0.1:3210 + 真实 Chromium 1600×1000，
 * 批次 b_1）。判定：`.batch-events__type` 的**可见文本**里不得出现内部英文事件键
 * （`/^Batch[A-Z][A-Za-z]*$/`）；`detail` 摘要不得出现内置错误类别原码。
 *
 * 用法：node docs/audit/issue-206/verify-timeline.mjs
 * 产物：docs/audit/issue-206/02-after.png + after.json
 */
import { createRequire } from 'node:module'
import { writeFileSync } from 'node:fs'
import { dirname, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

const require = createRequire(import.meta.url)
const { chromium } = require(process.env.PLAYWRIGHT_PATH ?? '/root/.pi/agent/npm/node_modules/playwright')

const here = dirname(fileURLToPath(import.meta.url))
const BASE = process.env.BASE_URL ?? 'http://127.0.0.1:3210'
const BATCH = process.env.BATCH_ID ?? 'b_1'
// 与 internal/model/batch.go 的 ErrorClass* 常量一致（#191 已修：detail 里的
// errorClass 也必须译好后再显示）。
const ERROR_CLASSES = ['config_error', 'provider_error', 'rate_limited', 'timeout',
  'empty_output', 'truncated', 'invalid_json', 'schema_violation', 'internal_error']

const browser = await chromium.launch({ headless: true })
const ctx = await browser.newContext({ viewport: { width: 1600, height: 1000 }, locale: 'zh-CN' })
const page = await ctx.newPage()
const consoleErrors = []
page.on('pageerror', (e) => consoleErrors.push(`pageerror: ${String(e).slice(0, 140)}`))
page.on('response', (r) => { if (r.status() >= 500) consoleErrors.push(`HTTP ${r.status()} ${r.url().replace(BASE, '').slice(0, 80)}`) })

await page.goto(`${BASE}/login`, { waitUntil: 'networkidle' })
await page.getByPlaceholder('请输入邮箱').fill(process.env.ADMIN_EMAIL ?? 'admin@company.com')
await page.getByPlaceholder('请输入密码').fill(process.env.ADMIN_PASSWORD ?? 'admin123456')
await page.getByRole('button', { name: '进入今日工作' }).click()
await page.waitForURL(/\/today/, { timeout: 20000 })

await page.goto(`${BASE}/p/1/runs/${BATCH}`, { waitUntil: 'networkidle' })
await page.waitForTimeout(2000)

const report = { stage: 'after', base: BASE, batch: BATCH }
report.eventRows = await page.locator('.batch-events li').evaluateAll((rows) => rows.slice(0, 8).map((row) => ({
  type: row.querySelector('.batch-events__type')?.textContent?.trim() ?? '',
  typeTitle: row.querySelector('.batch-events__type')?.getAttribute('title') ?? '',
  detail: row.querySelector('.batch-events__detail')?.textContent?.trim() ?? '',
})))
report.visibleEnumKeys = report.eventRows
  .map((r) => (/^Batch[A-Z][A-Za-z]*$/.test(r.type) ? r.type : null))
  .filter(Boolean)
// detail 泄漏检查（可见文本，不含 title）。
report.visibleErrorCodes = await page.evaluate((codes) => {
  const hits = []
  for (const el of document.querySelectorAll('.batch-events__detail, .batch-events__type')) {
    const text = (el.textContent ?? '').trim()
    for (const code of codes) if (text.includes(code)) hits.push(code)
  }
  return [...new Set(hits)]
}, ERROR_CLASSES)
report.consoleErrors = consoleErrors

await page.locator('.batch-events').scrollIntoViewIfNeeded().catch(() => {})
await page.screenshot({ path: resolve(here, '02-after.png') })
writeFileSync(resolve(here, 'after.json'), `${JSON.stringify(report, null, 2)}\n`)
await browser.close()

console.log(JSON.stringify(report, null, 2))
const broken = report.visibleEnumKeys.length + report.visibleErrorCodes.length
console.log(`\n[issue-206] after: 可见英文事件键 = ${report.visibleEnumKeys.length}，可见错误码 = ${report.visibleErrorCodes.length}`)
process.exit(broken > 0 ? 2 : 0)
