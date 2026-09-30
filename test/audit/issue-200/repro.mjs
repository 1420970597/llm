/**
 * issue #200 复现与修复后取证（真实栈 + 真实 Chromium）。
 *
 * 缺陷形态：同一份「待判断」事实在界面上出现**互相矛盾**的读数 ——
 * 直接数 `review_projections` 的行在「一次判断都没做过的项目」上恒为 0
 * （新项目没有投影行），而审阅队列把未判断内容都算作待判断。
 *
 * 本 issue 有三条渲染路径，本轮统一到**同一条共享谓词**：
 *   1. `samples_query.go` 的 `latestReviewProjectionJoin` /
 *      `effectiveReviewStatusSQL`（共享常量，唯一来源）；
 *   2. `/today` 磁贴「待人工判断」与 DECISIONS「需要你的决定」列表；
 *   3. `/p/{id}/overview` 概览页「待处理决定」指标。
 *
 * 判据（全部是可核对的读数，不是「看起来修好了」）：
 *   * 磁贴 == 队列条数；
 *   * DECISIONS 列表存在 `pending_review` 待办且其计数 == 队列条数；
 *   * 概览页指标 == 队列条数。
 *
 * 运行：
 *   node test/audit/issue-200/repro.mjs --phase before|after
 * 产物：
 *   docs/audit/issue-200/<phase>-today.png
 *   docs/audit/issue-200/<phase>-overview.png
 *   docs/audit/issue-200/<phase>.json
 */
import { createRequire } from 'node:module'
import { mkdirSync, writeFileSync } from 'node:fs'
import path from 'node:path'
import { fileURLToPath } from 'node:url'

const require = createRequire('/root/llm/package.json')
const { chromium } = require('/root/.pi/agent/npm/node_modules/playwright')

const HERE = path.dirname(fileURLToPath(import.meta.url))
const OUT = path.resolve(HERE, '../../../docs/audit/issue-200')
mkdirSync(OUT, { recursive: true })

const phaseArg = process.argv.indexOf('--phase')
const PHASE = phaseArg >= 0 ? process.argv[phaseArg + 1] : 'before'
const BASE = process.env.BASE_URL ?? 'http://127.0.0.1:3210'
const EMAIL = process.env.ADMIN_EMAIL ?? 'admin@company.com'
const PASSWORD = process.env.ADMIN_PASSWORD ?? 'admin123456'
const PROJECT_ID = process.env.PROJECT_ID ?? '1'

const browser = await chromium.launch({ headless: true })
const ctx = await browser.newContext({
  viewport: { width: 1600, height: 1000 },
  locale: 'zh-CN',
  deviceScaleFactor: 1,
})
const page = await ctx.newPage()

await page.goto(`${BASE}/login`, { waitUntil: 'networkidle' })
await page.getByPlaceholder('请输入邮箱').fill(EMAIL)
await page.getByPlaceholder('请输入密码').fill(PASSWORD)
await page.getByRole('button', { name: '进入今日工作' }).click()
await page.waitForURL(/\/today/, { timeout: 20000 })
await page.waitForTimeout(1500)

// ---- 路径 2：/today（磁贴 + DECISIONS 待办列表） ----
const today = await page.evaluate(async (projectId) => {
  const [todayRes, queueRes] = await Promise.all([
    fetch('/api/v1/today', { credentials: 'include' }),
    fetch(`/api/v1/projects/${projectId}/samples?status=pending`, { credentials: 'include' }),
  ])
  const todayBody = todayRes.ok ? await todayRes.json() : null
  const queueBody = queueRes.ok ? await queueRes.json() : null
  const tile = document.querySelector('[data-overview-tile="pending"]')
  const rows = Array.from(document.querySelectorAll('[data-today-todos="true"] [data-todo-kind]'))
  return {
    todayStatus: todayRes.status,
    queueStatus: queueRes.status,
    overviewPendingReview: todayBody?.overview?.pendingReview ?? null,
    todos: todayBody?.todos ?? [],
    queueItems: Array.isArray(queueBody?.items) ? queueBody.items.length : null,
    tileEyebrow: tile?.querySelector('.eyebrow')?.textContent?.trim() ?? null,
    tileValue: tile?.querySelector('strong')?.textContent?.trim() ?? null,
    decisionRows: rows.map((row) => ({
      kind: row.getAttribute('data-todo-kind'),
      text: (row.innerText ?? '').replace(/\s+/g, ' ').trim(),
    })),
  }
}, PROJECT_ID)

await page.screenshot({ path: path.join(OUT, `${PHASE}-today.png`), fullPage: true })

// ---- 路径 3：/p/{id}/overview（待处理决定指标） ----
await page.goto(`${BASE}/p/${PROJECT_ID}/overview`, { waitUntil: 'networkidle' })
await page.waitForTimeout(1800)

const overview = await page.evaluate(async (projectId) => {
  const response = await fetch(`/api/v1/projects/${projectId}/overview`, { credentials: 'include' })
  const body = response.ok ? await response.json() : null
  const metrics = Array.from(document.querySelectorAll('.atelier-overview-metrics > div'))
  const metric = metrics.find((node) => (node.textContent ?? '').includes('待处理决定'))
  return {
    status: response.status,
    statsPendingReview: body?.data?.stats?.pendingReview ?? null,
    metricText: (metric?.textContent ?? '').replace(/\s+/g, ' ').trim(),
  }
}, PROJECT_ID)

await page.screenshot({ path: path.join(OUT, `${PHASE}-overview.png`), fullPage: true })

const pendingTodo = today.todos.find((todo) => todo.kind === 'pending_review') ?? null
const tileMatchesQueue =
  today.overviewPendingReview !== null &&
  today.queueItems !== null &&
  today.overviewPendingReview === today.queueItems
const pendingTodoMatchesQueue =
  pendingTodo !== null && today.queueItems !== null && pendingTodo.count === today.queueItems
const cardRendersPending = today.decisionRows.some((row) => row.kind === 'pending_review')
const overviewMatchesQueue =
  overview.statsPendingReview !== null &&
  today.queueItems !== null &&
  overview.statsPendingReview === today.queueItems

const report = {
  issue: 200,
  phase: PHASE,
  base: BASE,
  viewport: '1600x1000',
  account: EMAIL,
  queueItems: today.queueItems,
  todaySurface: {
    route: '/today',
    overviewPendingReview: today.overviewPendingReview,
    pendingReviewTodoCount: pendingTodo?.count ?? null,
    pendingReviewTodoSummary: pendingTodo?.summary ?? null,
    tileEyebrow: today.tileEyebrow,
    tileValue: today.tileValue,
    decisionRows: today.decisionRows,
    tileMatchesQueue,
    pendingTodoMatchesQueue,
    cardRendersPending,
  },
  overviewSurface: {
    route: `/p/${PROJECT_ID}/overview`,
    statsPendingReview: overview.statsPendingReview,
    metricText: overview.metricText,
    overviewMatchesQueue,
  },
  ok: tileMatchesQueue && pendingTodoMatchesQueue && cardRendersPending && overviewMatchesQueue,
}

writeFileSync(path.join(OUT, `${PHASE}.json`), `${JSON.stringify(report, null, 2)}\n`)
await browser.close()

console.log(`[issue-200] phase=${PHASE}`)
console.log(`  队列=${today.queueItems} · /today 磁贴=${today.overviewPendingReview} ` +
  `DECISIONS 待判断行=${pendingTodo?.count ?? '缺失'} · /p/1/overview 待处理决定=${overview.statsPendingReview}`)
console.log(`  磁贴==队列: ${tileMatchesQueue} · 待办==队列: ${pendingTodoMatchesQueue} ` +
  `· 概览==队列: ${overviewMatchesQueue}`)
console.log(`  判定: ${report.ok ? 'FIXED' : 'STILL_BROKEN'}`)
if (!report.ok) process.exit(1)
