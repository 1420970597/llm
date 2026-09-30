/**
 * #214 子项 #207 / #210 的**可见后果**取证（前后对比必须能看出差别）。
 *
 * 为什么需要单独一个脚本：
 *   这两条的缺陷在于 **href 的指向**，而不是页面上少了几个字。直接截「动态列表」
 *   或「审阅页」的两张图会**像素级相同**（DOM 变了、可见文本没变），
 *   `evidence-check` 会以 `IDENTICAL` 拒绝 —— 这是对的：那种图证明不了任何事。
 *   因此这里截的是**点击之后落到哪一页**：
 *
 *   - #207：在 `/p/1/data/s_5` 按 `J` 之后停在哪个样本。
 *     修复前：按 J 无变化（仍是 s_5）；修复后：跳到下一条（s_3）。
 *   - #210：在 `/activity` 点第一条审计记录的「查看」后落到哪一页。
 *     修复前：项目概览；修复后：该操作的对象页（批次详情等）。
 *
 * 两次采集用**完全相同的脚本与视口**，因此差异只可能来自被测行为。
 *
 * 用法：node docs/audit/issue-214/repro-open2.mjs before|after
 * 产物：docs/audit/issue-214/<NN>-207-keyboard-nav.png、<NN>-210-audit-target.png
 *       docs/audit/issue-214/<stage>-open2.json
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
const PROJECT_ID = process.env.PROJECT_ID ?? '1'
const START_SAMPLE = process.env.START_SAMPLE ?? 's_5'

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
await page.waitForTimeout(800)

const report = { stage, base: BASE }

// ----------------------------------------------------- #207 键盘导航 ----
await page.goto(`${BASE}/p/${PROJECT_ID}/data/${START_SAMPLE}`, { waitUntil: 'networkidle' })
await page.waitForTimeout(1500)
const startURL = new URL(page.url()).pathname
// 记录「当前样本标题」，这样截图里能直接看出是哪一条（而不用去读 URL）。
const titleOf = () => page.locator('.console-page__header h4').first().innerText().catch(() => '')
const startTitle = (await titleOf()).trim()
await page.keyboard.press('j')
await page.waitForTimeout(1800)
const afterJURL = new URL(page.url()).pathname
const afterJTitle = (await titleOf()).trim()
report.keyboard = { start: startURL, startTitle, afterJ: afterJURL, afterJTitle, moved: afterJURL !== startURL }
await page.screenshot({ path: resolve(here, `${KEY}-207-keyboard-nav.png`) })

// -------------------------------------------------- #210 审计事件出口 ----
await page.goto(`${BASE}/activity`, { waitUntil: 'networkidle' })
await page.waitForTimeout(1800)
// 找一条**审计类**记录并点它的「查看」：判断落到哪一页。
// 用 `data-activity-item="audit"` 定位而不是靠文案，避免文案变化让脚本失效。
const auditRow = page.locator('[data-activity-item="audit"]').filter({ has: page.locator('a[href]') }).first()
report.auditExit = { rowText: (await auditRow.innerText().catch(() => '')).replace(/\n+/g, ' | ').trim().slice(0, 120) }
report.auditExit.href = await auditRow.locator('a[href]').first().getAttribute('href').catch(() => '')
await auditRow.locator('a[href]').first().click()
await page.waitForTimeout(1800)
report.auditExit.landedOn = new URL(page.url()).pathname
report.auditExit.landedTitle = (await page.locator('h1, h2, h3, h4').first().innerText().catch(() => '')).trim().slice(0, 60)
report.auditExit.isOverview = /\/overview$/.test(report.auditExit.landedOn)
await page.screenshot({ path: resolve(here, `${KEY}-210-audit-target.png`) })

report.consoleErrors = consoleErrors
writeFileSync(resolve(here, `${stage}-open2.json`), `${JSON.stringify(report, null, 2)}\n`)
await browser.close()

console.log(JSON.stringify(report, null, 2))
const broken = (report.keyboard.moved ? 0 : 1) + (report.auditExit.isOverview ? 1 : 0)
console.log(`\n[repro-open2] ${stage}: 未收口信号 = ${broken} 条`)
process.exit(broken > 0 ? 2 : 0)
