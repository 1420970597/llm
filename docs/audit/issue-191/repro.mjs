// issue #191 第 4 条渲染路径复现：旧控制台「系统设置 → 操作记录」的「资源」列
// 直接渲染内部英文 resource_type（workspace_member / blueprint_version / ...）。
//
// 复现手段（SOP §5）：真实 Chromium + 真实容器栈（127.0.0.1:3210 + admin 账号）。
// 输出：docs/audit/issue-191/<stage>.png 与 同目录 <stage>.json 的实测读数。
//
// 用法：node docs/audit/issue-191/repro.mjs before
//       node docs/audit/issue-191/repro.mjs after
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

const browser = await chromium.launch({ headless: true })
const ctx = await browser.newContext({ viewport: { width: 1600, height: 1000 }, locale: 'zh-CN' })
const page = await ctx.newPage()
const errors = []
page.on('pageerror', (e) => errors.push(`pageerror: ${String(e).slice(0, 160)}`))
page.on('response', (r) => { if (r.status() >= 500) errors.push(`HTTP ${r.status()} ${r.url().slice(0, 90)}`) })

const report = { stage, base: BASE }

// 登录（与既有采集器同账号、同视口，保证前后同条件对比）。
await page.goto(`${BASE}/login`, { waitUntil: 'networkidle' })
await page.getByPlaceholder('请输入邮箱').fill(EMAIL)
await page.getByPlaceholder('请输入密码').fill(PASSWORD)
await page.getByRole('button', { name: '进入今日工作' }).click()
await page.waitForURL(/\/today/, { timeout: 20000 })
await page.waitForTimeout(1000)

// 旧控制台 → 系统设置 → 操作记录。
await page.goto(`${BASE}/console/admin/audit`, { waitUntil: 'networkidle' })
await page.waitForTimeout(1500)

// 「资源」列：表头第 3 列（操作人 / 操作 / 资源 / 详情 / 时间）。
const table = page.locator('.semi-table').first()
report.tableHead = await table.locator('thead th').allInnerTexts().catch(() => [])

// 采集「资源」列每个可见单元格的文本与 title 属性 —— 二者都必须是人话。
report.resourceCells = await table.locator('tbody tr').evaluateAll((rows) => rows.slice(0, 12).map((row) => {
  const cells = row.querySelectorAll('td')
  const cell = cells[2]
  if (!cell) return null
  return { text: (cell.textContent ?? '').trim(), title: cell.getAttribute('title') ?? '' }
}).filter(Boolean))

// 机器判定：资源列文本里不得出现 snake_case 内部键（排除 `xxx #123` 里的 ID）。
const snake = /(^|[^A-Za-z0-9_])([a-z][a-z0-9]*(?:_[a-z0-9]+)+)(?![A-Za-z0-9_])/
report.visibleEnumKeys = report.resourceCells
  .map((c) => snake.exec(c.text)?.[2] ?? null)
  .filter(Boolean)

report.consoleErrors = errors

const shot = resolve(here, `${stage}.png`)
await page.screenshot({ path: shot, fullPage: false })
writeFileSync(resolve(here, `${stage}.json`), `${JSON.stringify(report, null, 2)}\n`)

await browser.close()
console.log(JSON.stringify({
  stage: report.stage,
  tableHead: report.tableHead,
  resourceCells: report.resourceCells,
  visibleEnumKeys: report.visibleEnumKeys,
  consoleErrors: report.consoleErrors,
  shot,
}, null, 2))
process.exit(report.visibleEnumKeys.length > 0 ? 2 : 0)
