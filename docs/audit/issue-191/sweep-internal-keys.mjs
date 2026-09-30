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
 *   - **显式标注的机器标识**（`key=aq_actionability`）：维度 key 是导出 schema
 *     里的字段名，管理员改维度时必须看得到；它与状态/枚举取值是两回事。
 *     排除带的是**上下文前缀**（`key=`），因此不会掩盖真实的裸码泄漏。
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

/**
 * 从一段可见文本里提取「真实泄漏」。
 *
 * 抽成函数是为了能对它做**变异自证**（见下方 selfCheck）：
 * 「exclusion 不会掩盖真泄漏」必须是被证明的，而不是被声称的 ——
 * 过度宽松的过滤器与没有扫描器一样危险（它给人以已受保护的错觉）。
 */
function extractLeaks(text) {
  // `key=<标识>` 是**刻意展示的机器标识**（维度管理页的 `key=aq_actionability`），
  // 与 `s_5` / `b_1` 同类：它是导出 schema 里的字段名，管理员改维度时必须看得到。
  // 排除它带的是**上下文前缀**（`key=`），因此不会掩盖真实的裸码泄漏。
  const labelledIdentifier = /key=\s*[a-z][a-z0-9_]*/g
  const keyIdentifiers = new Set((text.match(labelledIdentifier) ?? []).map((m) => m.replace(/^key=\s*/, '')))
  return [...new Set([
    ...[...text.matchAll(SNAKE)].map((m) => m[2]),
    ...[...text.matchAll(CAMEL_EVENT)].map((m) => m[1]),
  ])].filter((value) => !isNoise(value) && !keyIdentifiers.has(value))
}

// ---- 变异自证：过滤器不得掩盖真泄漏 ----
// 1. 带 `key=` 前缀的机器标识必须被排除（否则误报会把扫描器弄成永久红灯）；
// 2. **去掉前缀后必须被捕获**（否则排除规则过宽，真泄漏会被一起吞掉）；
// 3. 典型的真泄漏（错误码 / 事件键）在任何形式下都必须被捕获。
const selfCheck = [
  ['带 key= 前缀的机器标识被排除', extractLeaks('可执行性 key=aq_actionability 区间').length === 0],
  ['去掉 key= 前缀后必须被捕获', extractLeaks('可执行性 aq_actionability 区间').includes('aq_actionability')],
  ['裸错误码必须被捕获', extractLeaks('错误类别：config_error · 尝试 1 次').includes('config_error')],
  ['裸事件键必须被捕获', extractLeaks('BatchResumed 2026-09-28').includes('BatchResumed')],
  ['资源标识不被误报', extractLeaks('批次 b_1 · 样本 s_5').length === 0],
]
for (const [name, ok] of selfCheck) {
  console.log(`[${ok ? 'PASS' : 'FAIL'}] 扫描器自证：${name}`)
  if (!ok) process.exitCode = 1
}
if (selfCheck.some(([, ok]) => !ok)) {
  console.error('\n扫描器自证失败：排除规则可能有误，本轮结果不可信')
  process.exit(1)
}
const results = []
for (const [key, route] of ROUTES) {
  await page.goto(`${BASE}${route}`, { waitUntil: 'networkidle' })
  await page.waitForTimeout(1200)
  // 只扫「可见文本」：用 title 属性保留原始键是**刻意**的（便于排查），不算泄漏。
  const text = await page.evaluate(() => document.body.innerText)
  const leaks = extractLeaks(text)
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
