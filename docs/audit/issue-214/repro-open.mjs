/**
 * #214 第 2 轮：四条未收口子单的一次性机器化复现/验证。
 *
 *   A. #205 —— 今日工作总览磁贴的 href（3 个指向 /today 自身；「被挡住 N」无出口）
 *   B. #207 —— 帮助页承诺的审阅 J/K 快捷键（实测按键前后 URL 是否变化）
 *   C. #210 —— 动态列表审计类事件的「查看」目标（19 条全部指向概览）
 *   D. #200 —— 「待人工判断」总览读数 vs 审阅队列条数（口径分叉）
 *
 * 四条判定都是**机器事实**（href 比较 / URL 变化 / API 数字相等），不靠肉眼。
 *
 * 用法：node docs/audit/issue-214/repro-open.mjs before|after
 * 产物：docs/audit/issue-214/<stage>-205-today-tiles.png 等 4 张图 + <stage>-open.json
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
const PROJECT_ID = process.env.PROJECT_ID ?? '1'
const SAMPLE_ID = process.env.SAMPLE_ID ?? 's_5'
const KEY = stage === 'after' ? '02' : '01'

const browser = await chromium.launch({ headless: true })
const ctx = await browser.newContext({ viewport: { width: 1600, height: 1000 }, locale: 'zh-CN' })
const page = await ctx.newPage()
const consoleErrors = []
page.on('pageerror', (e) => consoleErrors.push(`pageerror: ${String(e).slice(0, 160)}`))
page.on('response', (r) => { if (r.status() >= 500) consoleErrors.push(`HTTP ${r.status()} ${r.url().slice(0, 90)}`) })

await page.goto(`${BASE}/login`, { waitUntil: 'networkidle' })
await page.getByPlaceholder('请输入邮箱').fill(process.env.ADMIN_EMAIL ?? 'admin@company.com')
await page.getByPlaceholder('请输入密码').fill(process.env.ADMIN_PASSWORD ?? 'admin123456')
await page.getByRole('button', { name: '进入今日工作' }).click()
await page.waitForURL(/\/today/, { timeout: 20000 })
await page.waitForTimeout(1200)

const report = { stage, base: BASE }

// ---------------------------------------------------------------- A. #205 ----
const tiles = await page.locator('[data-overview-tile]').evaluateAll((nodes) => nodes.map((node) => ({
  key: node.getAttribute('data-overview-tile'),
  href: node.getAttribute('href'),
  text: (node.innerText ?? '').replace(/\n+/g, ' | ').trim(),
  // 「被挡住 N」的独立出口（#205 的第二项要求）：磁贴内部是否另有链接。
  innerLinks: [...node.querySelectorAll('a[href]')].map((a) => ({ href: a.getAttribute('href'), text: (a.textContent ?? '').trim() })),
})))
report.tiles = {
  items: tiles,
  selfLinks: tiles.filter((t) => t.href === '/today').map((t) => t.key),
  blockedBadge: tiles.find((t) => t.key === 'releases') ?? null,
}
await page.screenshot({ path: resolve(here, `${KEY}-205-today-tiles.png`) })

// ---------------------------------------------------------------- B. #207 ----
// 帮助页的承诺：先记录它到底写了什么（判定「文档承诺」是否还在）。
await page.goto(`${BASE}/help`, { waitUntil: 'networkidle' })
await page.waitForTimeout(800)
report.helpShortcutText = await page.locator('body').innerText()
  .then((text) => (text.match(/审阅队列[^\n]*/g) ?? []).join(' | '))
  .catch(() => '')

await page.goto(`${BASE}/p/${PROJECT_ID}/data/${SAMPLE_ID}`, { waitUntil: 'networkidle' })
await page.waitForTimeout(1500)
const urlBeforeJ = new URL(page.url()).pathname
await page.keyboard.press('j')
await page.waitForTimeout(1500)
const urlAfterJ = new URL(page.url()).pathname
await page.keyboard.press('k')
await page.waitForTimeout(1500)
const urlAfterK = new URL(page.url()).pathname
report.shortcuts = {
  start: urlBeforeJ,
  afterJ: urlAfterJ,
  afterK: urlAfterK,
  jWorks: urlAfterJ !== urlBeforeJ,
  kWorks: urlAfterK !== urlAfterJ,
}
await page.screenshot({ path: resolve(here, `${KEY}-207-review-shortcut.png`) })

// ---------------------------------------------------------------- C. #210 ----
await page.goto(`${BASE}/activity`, { waitUntil: 'networkidle' })
await page.waitForTimeout(1800)
const rows = await page.locator('[data-activity-item]').evaluateAll((nodes) => nodes.map((node) => ({
  source: node.getAttribute('data-activity-item'),
  text: (node.innerText ?? '').replace(/\n+/g, ' | ').trim().slice(0, 90),
  href: node.querySelector('a[href]')?.getAttribute('href') ?? '',
})))
report.activity = {
  rows,
  // 审计类事件全部指向项目概览即缺陷成立（#210）。
  auditToOverview: rows.filter((r) => r.source === 'audit' && /\/overview$/.test(r.href)).length,
  auditTotal: rows.filter((r) => r.source === 'audit').length,
  auditTargets: [...new Set(rows.filter((r) => r.source === 'audit').map((r) => r.href))],
}
await page.screenshot({ path: resolve(here, `${KEY}-210-activity-links.png`) })

// ---------------------------------------------------------------- D. #200 ----
report.pendingReview = await page.evaluate(async (projectId) => {
  const json = async (url) => {
    const response = await fetch(url, { credentials: 'include' })
    return { status: response.status, body: await response.json().catch(() => null) }
  }
  const today = await json('/api/v1/today')
  const queue = await json(`/api/v1/projects/${projectId}/samples?status=pending&limit=100`)
  return {
    overviewPendingReview: today.body?.overview?.pendingReview ?? null,
    queueItems: Array.isArray(queue.body?.items) ? queue.body.items.length : null,
    queueUrl: `/api/v1/projects/${projectId}/samples?status=pending`,
    todayStatus: today.status,
    queueStatus: queue.status,
  }
}, PROJECT_ID)
report.pendingReview.matches = report.pendingReview.overviewPendingReview === report.pendingReview.queueItems

report.consoleErrors = consoleErrors
writeFileSync(resolve(here, `${stage}-open.json`), `${JSON.stringify(report, null, 2)}\n`)
await browser.close()

const broken = report.tiles.selfLinks.length
  + (report.shortcuts.jWorks && report.shortcuts.kWorks ? 0 : 1)
  + report.activity.auditToOverview
  + (report.pendingReview.matches ? 0 : 1)
console.log(JSON.stringify(report, null, 2))
console.log(`\n[repro-open] ${stage}: 未收口信号 = ${broken} 条`)
process.exit(broken > 0 ? 2 : 0)
