/**
 * issue #205 复现/验证：`/today` 总览磁贴的跳转目标。
 *
 * 缺陷形态：6 个磁贴里 3 个写死 `studioPath('today')`（当前页自身，点了原地不动），
 * 「待人工判断」指向 `/activity`（语义错误：那个数字来自审阅投影，动态页不显示
 * 任何待判断样本），「交付」磁贴写「被挡住 N」却整体链到 `/deliveries`
 *（交付库按定义只显示已发布版本，被挡住的候选在那里根本不存在）。
 *
 * 判定（机器事实）：
 *   * 每个 `[data-overview-tile]` 的 href 及其**内层**链接；
 *   * `selfLinks`：href === 当前路径（`/today`）的磁贴数，必须为 0；
 *   * 「被挡住 N > 0」时必须存在独立出口，且其目标能回答该数字。
 *
 * 用法：
 *   node docs/audit/issue-205/repro.mjs --phase before|after
 * 产物：
 *   docs/audit/issue-205/<phase>.json
 *   docs/audit/issue-205/<phase>-today-tiles.png
 *
 * 防覆盖：已存在的证据默认不覆盖（与 issue-200/212 的同一约定）。
 */
import { createRequire } from 'node:module'
import { existsSync, mkdirSync, writeFileSync } from 'node:fs'
import path from 'node:path'
import { fileURLToPath } from 'node:url'

const require = createRequire('/root/llm/package.json')
const { chromium } = require('/root/.pi/agent/npm/node_modules/playwright')

const HERE = path.dirname(fileURLToPath(import.meta.url))
mkdirSync(HERE, { recursive: true })

const phaseArg = process.argv.indexOf('--phase')
const PHASE = phaseArg >= 0 ? process.argv[phaseArg + 1] : 'before'
// `--stub-single-project`：用**受控输入**驱动单项目分支。
//
// 为什么需要它：真实栈上本工作区当前有 2 个项目，因此只会走到 approved 的多项目
// 回退（先去 `/projects` 让用户选）。而 issue 明确要求的磁贴目标表
//（`/p/{id}/runs`、`/p/{id}/review`、`/p/{id}/data`、`/p/{id}/releases`）
// 只在**恰好一个项目**时生效。用 stub 拦下 `/api/v1/today` 的 overview
// 就能在不改动共享数据库的前提下把这条分支变成可重复的机器事实。
// 证据会明确标注它是受控输入，不冒充真实数据。
const STUB_SINGLE = process.argv.includes('--stub-single-project')
const FORCE = process.argv.includes('--force')

if (!FORCE) {
  const name = STUB_SINGLE ? `${PHASE}-single-project` : PHASE
  const existing = [`${name}.json`, `${name}-today-tiles.png`]
    .filter((file) => existsSync(path.join(HERE, file)))
  if (existing.length > 0) {
    console.error(`[issue-205] 拒绝覆盖已存在的证据（${name}）：${existing.join(', ')}`)
    process.exit(2)
  }
}

const BASE = process.env.BASE_URL ?? 'http://127.0.0.1:3210'
const EMAIL = process.env.ADMIN_EMAIL ?? 'admin@company.com'
const PASSWORD = process.env.ADMIN_PASSWORD ?? 'admin123456'

const browser = await chromium.launch({ headless: true })
const ctx = await browser.newContext({ viewport: { width: 1600, height: 1000 }, locale: 'zh-CN', deviceScaleFactor: 1 })
const page = await ctx.newPage()
const consoleErrors = []
page.on('pageerror', (e) => consoleErrors.push(`pageerror: ${String(e).slice(0, 160)}`))
page.on('response', (r) => { if (r.status() >= 500) consoleErrors.push(`HTTP ${r.status()} ${r.url().slice(0, 90)}`) })

await page.goto(`${BASE}/login`, { waitUntil: 'networkidle' })
await page.getByPlaceholder('请输入邮箱').fill(EMAIL)
await page.getByPlaceholder('请输入密码').fill(PASSWORD)
await page.getByRole('button', { name: '进入今日工作' }).click()
await page.waitForURL(/\/today/, { timeout: 20000 })
await page.waitForTimeout(1500)

if (STUB_SINGLE) {
  // 只改 overview 的「项目数/作用域项目 ID」，其余响应体原样透传：
  // 这样磁贴的数字与真实数据一致，唯一变量是「工作区里有几个项目」。
  await page.route('**/api/v1/today', async (route) => {
    const response = await route.fetch()
    const body = await response.json()
    if (body?.overview) {
      body.overview.projectCount = 1
      body.overview.scopedProjectIds = [1]
    }
    await route.fulfill({ response, json: body })
  })
  await page.reload({ waitUntil: 'networkidle' })
  await page.waitForTimeout(1800)
}

const version = await page.evaluate(async () => {
  const response = await fetch('/version.json', { credentials: 'include' })
  return response.ok ? await response.json() : null
}).catch(() => null)

const report = { issue: 205, phase: PHASE, base: BASE, viewport: '1600x1000', account: EMAIL, deployedVersion: version,
  controlledInput: STUB_SINGLE ? 'stub: /api/v1/today overview → projectCount=1, scopedProjectIds=[1]' : null }
report.evidenceName = STUB_SINGLE ? `${PHASE}-single-project` : PHASE

// 磁贴全量读出：外层 href 与内层链接都要采（「被挡住 N」的出口是内层链接；
// 而该磁贴必须是 div 而不是 a —— a 里嵌 a 是非法 HTML）。
report.tiles = await page.locator('[data-overview-tile]').evaluateAll((nodes) => nodes.map((node) => ({
  key: node.getAttribute('data-overview-tile'),
  tag: node.tagName.toLowerCase(),
  href: node.getAttribute('href'),
  text: (node.innerText ?? '').replace(/\n+/g, ' | ').trim(),
  innerLinks: [...node.querySelectorAll('a[href]')].map((a) => ({
    href: a.getAttribute('href'),
    text: (a.textContent ?? '').replace(/\s+/g, ' ').trim(),
  })),
})))

const currentPath = new URL(page.url()).pathname
report.currentPath = currentPath
report.selfLinks = report.tiles.filter((tile) => tile.href === currentPath).map((tile) => tile.key)

// 工作区项目数决定深链形态（approved 规则，见 TodayPages.tsx 的 overviewProjectHref）：
// **恰好一个项目**时深链到 `/p/{id}/...`；多个项目时先到 `/projects`（不猜一个项目）。
// 因此判定必须把两种合法形态都接受，而单项目下的 `/today` 自链与 `/activity` 错靶
// 在两种形态下都是缺陷。
report.overview = await page.evaluate(async () => {
  const response = await fetch('/api/v1/today', { credentials: 'include' })
  const body = response.ok ? await response.json() : null
  return {
    status: response.status,
    projectCount: body?.overview?.projectCount ?? null,
    scopedProjectIds: body?.overview?.scopedProjectIds ?? null,
    blockedReleases: body?.overview?.blockedReleases ?? null,
  }
}).catch((error) => ({ error: String(error).slice(0, 200) }))
const multiProject = (report.overview.projectCount ?? 0) !== 1
report.multiProject = multiProject

// 「待人工判断」必须去能回答该数字的地方：单项目 → `/p/{id}/review`，多项目 → `/projects`。
// **不等于** `/activity`（动态页不显示任何待判断样本 —— 这是 issue 指出的语义错误）。
const pendingTile = report.tiles.find((tile) => tile.key === 'pending') ?? null
report.pendingTarget = pendingTile?.href ?? null
report.pendingPointsToActivity = report.pendingTarget === '/activity'
report.pendingReachable = (report.pendingTarget ?? '').endsWith('/review') ||
  (multiProject && report.pendingTarget === '/projects')

// 「被挡住 N」必须有自己的出口（不是磁贴主体那个 /deliveries）。
const blockedTile = report.tiles.find((tile) => tile.key === 'releases') ?? null
const exitLinks = (blockedTile?.innerLinks ?? []).filter((link) => link.href !== '/deliveries')
report.blocked = {
  tileText: blockedTile?.text ?? null,
  innerLinks: blockedTile?.innerLinks ?? [],
  // 单项目 → `/p/{id}/releases`（直接能处理）；多项目 → `/projects`（先选项目）。
  hasDedicatedExit: exitLinks.some((link) => (link.href ?? '').endsWith('/releases') ||
    (multiProject && link.href === '/projects')),
  dedicatedExitHref: exitLinks[0]?.href ?? null,
}
report.blockedCountInTile = Number((blockedTile?.text ?? '').match(/被挡住\s*(\d+)/)?.[1] ?? 0)

// 出口页真的能看到被挡住的候选吗（#205 的第二条要求）。
if (report.blocked.hasDedicatedExit) {
  const exitHref = report.blocked.dedicatedExitHref
  await page.goto(`${BASE}${exitHref}`, { waitUntil: 'networkidle' })
  await page.waitForTimeout(1500)
  report.blockedExitPage = {
    href: exitHref,
    bodyHasBlocked: /被门槛阻塞|被挡住|阻塞/.test(await page.locator('body').innerText()),
  }
  await page.goto(`${BASE}/today`, { waitUntil: 'networkidle' })
  await page.waitForTimeout(1200)
}

await page.locator('[data-today-overview="true"]').first().scrollIntoViewIfNeeded().catch(() => {})
await page.screenshot({ path: path.join(HERE, `${report.evidenceName}-today-tiles.png`), fullPage: false })

report.consoleErrors = consoleErrors
report.verdict = {
  selfLinkCount: report.selfLinks.length,
  pendingWrongTarget: report.pendingPointsToActivity,
  pendingReachable: report.pendingReachable,
  blockedWithoutExit: report.blockedCountInTile > 0 && !report.blocked.hasDedicatedExit,
}
report.ok = report.verdict.selfLinkCount === 0 && !report.verdict.pendingWrongTarget &&
  report.verdict.pendingReachable && !report.verdict.blockedWithoutExit

writeFileSync(path.join(HERE, `${report.evidenceName}.json`), `${JSON.stringify(report, null, 2)}\n`)
await browser.close()

console.log(`[issue-205] phase=${PHASE}${STUB_SINGLE ? ' (受控输入：单项目)' : ''} 栈版本=${version?.version ?? 'unknown'}`)
console.log(`  磁贴 href：${report.tiles.map((t) => `${t.key}=${t.href ?? '(div)'}`).join(' · ')}`)
console.log(`  selfLinks=${JSON.stringify(report.selfLinks)} · pending=${report.pendingTarget} · ` +
  `被挡住出口=${report.blocked.hasDedicatedExit}`)
console.log(`  判定: ${report.ok ? 'FIXED' : 'STILL_BROKEN'}`)
process.exit(report.ok ? 0 : 2)
