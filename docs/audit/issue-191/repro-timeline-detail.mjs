/**
 * #191 第 2 轮扫描发现的**第三条**同类泄漏：批次详情「事件时间线」的 detail 摘要。
 *
 * 缺陷形态：时间线的事件类型已由服务端下发中文（#206 修的），但 `detail` 里的
 * `errorClass` 仍在界面上显示原始码 —— 实测可见
 * 「批次部分失败 单元 #12 · 错误类型 config_error · 不可重试」。
 *
 * 判定：时间线**可见文本**里不得出现内置错误类别码（原码应只存在于 title 或
 * 根本不显示）。
 *
 * 用法：node docs/audit/issue-191/repro-timeline-detail.mjs before|after
 * 产物：docs/audit/issue-191/<NN>-timeline-detail.png + <stage>-timeline-detail.json
 */
import { createRequire } from 'node:module'
import { writeFileSync } from 'node:fs'
import { dirname, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

const require = createRequire(import.meta.url)
const { chromium } = require(process.env.PLAYWRIGHT_PATH ?? '/root/.pi/agent/npm/node_modules/playwright')

const here = dirname(fileURLToPath(import.meta.url))
const stage = process.argv[2] === 'after' ? 'after' : 'before'
const KEY = stage === 'after' ? '02' : '01'
const BASE = process.env.BASE_URL ?? 'http://127.0.0.1:3210'
const BATCH = process.env.BATCH_ID ?? 'b_1'

// 与 internal/model/batch.go 的 ErrorClass* 常量一致。
const ERROR_CLASSES = ['config_error', 'provider_error', 'rate_limited', 'timeout',
  'empty_output', 'truncated', 'invalid_json', 'schema_violation', 'internal_error']

const browser = await chromium.launch({ headless: true })
const ctx = await browser.newContext({ viewport: { width: 1600, height: 1000 }, locale: 'zh-CN' })
const page = await ctx.newPage()
const consoleErrors = []
page.on('pageerror', (e) => consoleErrors.push(`pageerror: ${String(e).slice(0, 140)}`))
page.on('response', (r) => { if (r.status() >= 500) consoleErrors.push(`HTTP ${r.status()} ${r.url().slice(0, 80)}`) })

await page.goto(`${BASE}/login`, { waitUntil: 'networkidle' })
await page.getByPlaceholder('请输入邮箱').fill(process.env.ADMIN_EMAIL ?? 'admin@company.com')
await page.getByPlaceholder('请输入密码').fill(process.env.ADMIN_PASSWORD ?? 'admin123456')
await page.getByRole('button', { name: '进入今日工作' }).click()
await page.waitForURL(/\/today/, { timeout: 20000 })

await page.goto(`${BASE}/p/1/runs/${BATCH}`, { waitUntil: 'networkidle' })
await page.waitForTimeout(2000)

const report = { stage, base: BASE, batch: BATCH }
report.rows = await page.locator('.batch-events li').evaluateAll((rows) => rows.slice(0, 8).map((row) => ({
  type: row.querySelector('.batch-events__type')?.textContent?.trim() ?? '',
  detail: row.querySelector('.batch-events__detail')?.textContent?.trim() ?? '',
})))
// 可见文本里出现内置错误码即为泄漏（title 属性不算：见其它采集脚本的同一取舍）。
report.visibleEnumKeys = await page.evaluate((codes) => {
  const hits = []
  for (const el of document.querySelectorAll('.batch-events__detail, .batch-events__type')) {
    const text = (el.textContent ?? '').trim()
    for (const code of codes) if (text.includes(code)) hits.push(code)
  }
  return [...new Set(hits)]
}, ERROR_CLASSES)

await page.locator('.batch-events').scrollIntoViewIfNeeded().catch(() => {})
await page.screenshot({ path: resolve(here, `${KEY}-timeline-detail.png`) })
report.consoleErrors = consoleErrors
writeFileSync(resolve(here, `${stage}-timeline-detail.json`), `${JSON.stringify(report, null, 2)}\n`)
await browser.close()

console.log(JSON.stringify(report, null, 2))
console.log(`\n[repro-timeline-detail] ${stage}: 泄漏 = ${report.visibleEnumKeys.length} 处`)
process.exit(report.visibleEnumKeys.length > 0 ? 2 : 0)
