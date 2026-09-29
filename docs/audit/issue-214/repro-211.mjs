/**
 * #214 子项 #211 复现/验证：新建质量实验「检查范围」的审阅状态列。
 *
 * 判定：审阅状态单元格文本里不得出现裸的内部英文枚举（pending/accepted/
 * quarantined/conflict）。原值应保留在 title 上供排查。
 *
 * 用法：node docs/audit/issue-214/repro-211.mjs before|after
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

await page.goto(`${BASE}/p/1/quality/new`, { waitUntil: 'networkidle' })
await page.waitForTimeout(1800)

const scope = page.locator('[data-scope-picker="true"]')
const report = { stage, statusCells: [] }
report.statusCells = await scope.locator('.sample-row').evaluateAll((rows) =>
  rows.filter((r) => !r.classList.contains('sample-row--head')).map((row) => {
    const cells = row.querySelectorAll(':scope > span')
    const cell = cells[cells.length - 1]
    if (!cell) return null
    // title 可能落在 Tag 上、也可能落在包住 Tag 的 span 上（两种写法本仓都有），
    // 因此从单元格起找**任意**带 title 的后代，而不是只查 `.semi-tag`。
    const titled = cell.querySelector('[title]')
    return {
      text: (cell.textContent ?? '').trim(),
      title: titled?.getAttribute('title') ?? cell.getAttribute('title') ?? '',
    }
  }).filter(Boolean))

report.visibleEnumKeys = report.statusCells
  .map((c) => (/^(pending|accepted|quarantined|conflict)$/.test(c.text) ? c.text : null))
  .filter(Boolean)
report.scopeExplanation = (await scope.innerText().catch(() => '')).replace(/\n+/g, ' | ').slice(0, 300)
report.consoleErrors = errors

await page.screenshot({ path: resolve(here, stage === 'after' ? '02-211-after.png' : '01-211-before.png') })
writeFileSync(resolve(here, stage === 'after' ? '02-211-after.json' : '01-211-before.json'),
  `${JSON.stringify(report, null, 2)}\n`)
await browser.close()

console.log(JSON.stringify({ stage, statusCells: report.statusCells, visibleEnumKeys: report.visibleEnumKeys,
  scopeExplanation: report.scopeExplanation, consoleErrors: report.consoleErrors }, null, 2))
process.exit(report.visibleEnumKeys.length > 0 ? 2 : 0)
