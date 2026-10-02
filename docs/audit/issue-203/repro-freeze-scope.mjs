/**
 * #203 复现/验证：冻结发布范围把未审阅内容当「已接纳」。
 *
 * 判定（机器事实）：
 *   在「数据」页点「按当前筛选冻结并准备发布（服务端解析）」，然后：
 *   1. 读请求体/快照：fromFilter 是否显式声明范围意图（`reviewStatus`）；
 *   2. 读快照构成：范围内 pending（未审阅）条数；
 *   3. 读候选页区块标题：是否仍写死「已接纳」。
 *
 * 缺陷成立当且仅当：范围内含 pending，且候选页标题把它称为「已接纳」。
 *
 * 用法：node docs/audit/issue-203/repro-freeze-scope.mjs before|after
 * 产物：docs/audit/issue-203/<NN>-freeze-scope.png + <stage>-freeze-scope.json
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

const browser = await chromium.launch({ headless: true })
const ctx = await browser.newContext({ viewport: { width: 1600, height: 1000 }, locale: 'zh-CN' })
const page = await ctx.newPage()
const consoleErrors = []
page.on('pageerror', (e) => consoleErrors.push(`pageerror: ${String(e).slice(0, 160)}`))
page.on('response', (r) => { if (r.status() >= 500) consoleErrors.push(`HTTP ${r.status()} ${r.url().replace(BASE, '').slice(0, 90)}`) })

await page.goto(`${BASE}/login`, { waitUntil: 'networkidle' })
await page.getByPlaceholder('请输入邮箱').fill(process.env.ADMIN_EMAIL ?? 'admin@company.com')
await page.getByPlaceholder('请输入密码').fill(process.env.ADMIN_PASSWORD ?? 'admin123456')
await page.getByRole('button', { name: '进入今日工作' }).click()
await page.waitForURL(/\/today/, { timeout: 20000 })

const report = { stage, base: BASE, projectId: PROJECT_ID }

// 记录冻结请求体（证明前端传了什么意图）。
let freezeRequest = null
page.on('request', (req) => {
  if (req.url().includes(`/projects/${PROJECT_ID}/selection-snapshots`) && req.method() === 'POST') {
    freezeRequest = req.postData() ?? ''
  }
})

await page.goto(`${BASE}/p/${PROJECT_ID}/data`, { waitUntil: 'networkidle' })
await page.waitForTimeout(1500)

// 按钮：数据页的冻结入口
const freezeButton = page.locator('[data-snapshot-all="true"]').first()
report.freezeButtonText = (await freezeButton.textContent().catch(() => ''))?.trim() ?? ''
await freezeButton.click()
await page.waitForTimeout(2500)

report.freezeRequest = freezeRequest
report.afterFreezeUrl = page.url()

// 候选页区块标题（写死「已接纳」即缺陷形态）
report.rangeHeading = await page.evaluate(() => {
  const card = document.querySelector('[data-range-picker="true"]')
  if (!card) return null
  const heading = card.querySelector('.semi-typography strong') ?? card.querySelector('strong')
  return (heading?.textContent ?? '').trim()
})
report.rangeNotice = await page.evaluate(() => {
  const notice = document.querySelector('[data-snapshot-notice="true"]')
  return notice ? (notice.textContent ?? '').trim() : null
})

// 服务端事实：快照构成（范围内 pending 条数）与筛选意图。
report.snapshot = await page.evaluate(async (projectId) => {
  const json = async (url) => {
    const r = await fetch(url, { credentials: 'include' })
    return { status: r.status, body: await r.json().catch(() => null) }
  }
  const match = new URL(location.href).searchParams.get('selection')
  if (!match) return { error: 'URL 缺少 selection 参数' }
  const resolved = await json(`/api/v1/projects/${projectId}/selection-snapshots/${match}`)
  const pending = await json(`/api/v1/projects/${projectId}/samples?status=pending&limit=200`)
  return {
    snapshotId: Number(match),
    filter: resolved.body?.snapshot?.filter ?? null,
    itemCount: resolved.body?.snapshot?.itemCount ?? null,
    composition: resolved.body?.composition ?? null,
    exists: resolved.body?.snapshot != null,
    pendingSamples: Array.isArray(pending.body?.items) ? pending.body.items.length : null,
  }
}, PROJECT_ID)

// 缺陷判定：标题必须如实披露范围构成。
//
// 旧形态的两条硬信号（任一命中即缺陷）：
//   (a) 标题写死「已接纳的内容版本」——把整个范围断言为已接纳；
//   (b) 范围里确有未审阅内容（pending > 0），而标题没有露任何「未审阅」字样。
const heading = report.rangeHeading ?? ''
const filterReviewStatus = report.snapshot?.filter?.reviewStatus ?? null
const composition = report.snapshot?.composition ?? null
const pendingInRange = composition?.pending ?? null
report.defect = {
  hardcodedAcceptedHeading: /已接纳的内容版本/.test(heading),
  filterReviewStatus,
  pendingInRange,
  headingDisclosesUnreviewed: /未审阅/.test(heading + (report.rangeNotice ?? '')),
  present: /已接纳的内容版本/.test(heading) ||
    (pendingInRange !== null && pendingInRange > 0 && !/未审阅/.test(heading)),
}
report.consoleErrors = consoleErrors

await page.locator('[data-range-picker="true"]').scrollIntoViewIfNeeded().catch(() => {})
await page.waitForTimeout(400)
await page.screenshot({ path: resolve(here, `${KEY}-freeze-scope.png`), fullPage: false })
writeFileSync(resolve(here, `${stage}-freeze-scope.json`), `${JSON.stringify(report, null, 2)}\n`)
await browser.close()

console.log(JSON.stringify(report, null, 2))
console.log(`\n[repro-203] ${stage}: 缺陷成立 = ${report.defect.present}`)
process.exit(report.defect.present ? 2 : 0)
