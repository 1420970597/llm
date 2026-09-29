/**
 * #191 第 2 轮：全路由内部键泄漏扫描（判定「这一缺陷类是否还有未覆盖的渲染路径」）。
 *
 * 为什么需要它：#191 是「中文界面漏出内部英文键」的**缺陷类**（不是单点）。
 * 第 1 轮列出的 7 条渲染路径已全部修复，但只验证那 7 条等于「按清单找」——
 * 清单本身可能不全（这正是 #191→#206→#211 反复复现的成因）。
 * 因此这里做**与清单无关**的扫描：遍历主要路由的可见文本，
 * 判断是否出现 snake_case 内部键或 `BatchXxx` 驼峰事件键。
 *
 * 本脚本在 2026-09-29 的第 2 轮里**确实抓到了两条清单之外的泄漏**
 * （评估工作台的维度分类表头、批次失败卡的 error_class），
 * 它们随后由 `repro-extra-leaks.mjs` 逐条复现并修复。这就是「不按清单扫」的价值。
 *
 * 排除项（否则会产生误报，而误报的扫描器会被直接忽略）：
 *   - **资源标识**（`s_5` / `b_1`）：界面**有意**显示的对象身份（与第 1 轮复现
 *     脚本里「排除 `xxx #123` 里的 ID」同一取舍）。它不是状态机取值。
 *   - **内容 hash 片段**：排查线索，刻意保留。
 *   - 技术词（`snake_case` 等）与用户自写内容。
 *
 * 用法：node docs/audit/issue-191/sweep-internal-keys.mjs
 * 产物：docs/audit/issue-191/sweep.json
 * 退出码：0 = 无泄漏；2 = 发现泄漏
 */
import { createRequire } from 'node:module'
import { writeFileSync } from 'node:fs'
import { dirname, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

const require = createRequire(import.meta.url)
const { chromium } = require(process.env.PLAYWRIGHT_PATH ?? '/root/.pi/agent/npm/node_modules/playwright')

const here = dirname(fileURLToPath(import.meta.url))
const BASE = process.env.BASE_URL ?? 'http://127.0.0.1:3210'
const PROJECT_ID = process.env.PROJECT_ID ?? '1'

// 与第 1 轮评论里逐项列出的渲染路径 + 若干同类页（防止清单漏项）。
const ROUTES = [
  ['activity', '/activity'],
  ['tools-cleaning', '/tools/cleaning'],
  ['tools-evaluation', '/tools/evaluation'],
  ['legacy-history', '/legacy/history'],
  ['legacy-console-audit', '/console/admin/audit'],
  ['legacy-console-operations', '/console/operations'],
  ['batch-detail-b1', `/p/${PROJECT_ID}/runs/b_1`],
  ['batch-detail-b2', `/p/${PROJECT_ID}/runs/b_2`],
  ['batch-failures-b1', `/p/${PROJECT_ID}/runs/b_1/failures`],
  ['runs', `/p/${PROJECT_ID}/runs`],
  ['data', `/p/${PROJECT_ID}/data`],
  ['review', `/p/${PROJECT_ID}/review`],
  ['sample-detail', `/p/${PROJECT_ID}/data/s_5`],
  ['quality', `/p/${PROJECT_ID}/quality`],
  ['quality-new', `/p/${PROJECT_ID}/quality/new`],
  ['rules', `/p/${PROJECT_ID}/rules`],
  ['releases', `/p/${PROJECT_ID}/releases`],
  ['release-new', `/p/${PROJECT_ID}/releases/new`],
  ['today', '/today'],
  ['projects', '/projects'],
  ['settings-team', '/settings/team'],
  ['help', '/help'],
]

/** 资源标识（`s_5` / `b_1`）：界面有意显示的对象身份，不是状态机取值。 */
const RESOURCE_ID = /^[sb]_[0-9]+$/
/** 技术词：帮助文案里会出现这些词，它们不是泄漏。 */
const TECH_TOKENS = new Set(['snake_case', 'jsonschema', 'json_schema', 'chain_of_thought'])
/** 内容 hash / 版本号片段（纯十六进制或纯数字）。 */
const HASH_LIKE = /^[0-9a-f]{8,}$/

const SNAKE = /(^|[^A-Za-z0-9_])([a-z][a-z0-9]*(?:_[a-z0-9]+){1,})(?![A-Za-z0-9_])/g
const CAMEL_EVENT = /\b(Batch[A-Z][A-Za-z]*)\b/g

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

const isNoise = (value) => TECH_TOKENS.has(value) || RESOURCE_ID.test(value) || HASH_LIKE.test(value)
const results = []
for (const [key, route] of ROUTES) {
  await page.goto(`${BASE}${route}`, { waitUntil: 'networkidle' })
  await page.waitForTimeout(1200)
  // 只扫「可见文本」：用 title 属性保留原始键是**刻意**的（便于排查），不算泄漏。
  const text = await page.evaluate(() => document.body.innerText)
  const leaks = [...new Set([
    ...[...text.matchAll(SNAKE)].map((m) => m[2]),
    ...[...text.matchAll(CAMEL_EVENT)].map((m) => m[1]),
  ])].filter((value) => !isNoise(value))
  results.push({ key, route, leaks })
  console.log(`[${leaks.length === 0 ? ' OK ' : 'LEAK'}] ${key.padEnd(26)} ${leaks.length ? leaks.join(', ') : ''}`)
}

const report = {
  base: BASE,
  scannedAt: new Date().toISOString(),
  routes: results,
  totalLeaks: results.reduce((sum, item) => sum + item.leaks.length, 0),
  consoleErrors,
}
writeFileSync(resolve(here, 'sweep.json'), `${JSON.stringify(report, null, 2)}\n`)
await browser.close()

console.log(`\n[sweep] 扫描 ${results.length} 条路由，泄漏 ${report.totalLeaks} 处`)
process.exit(report.totalLeaks > 0 ? 2 : 0)
