/**
 * #204 复现/验证：蓝图画布靠后节点被裁在可视区外，`?node=` 深链指向的当前节点看不见。
 *
 * =========================================================================
 * 与 issue 原始描述的差异（本轮实测修正，不照抄结论）
 * =========================================================================
 * issue #204 原文写「节点被右栏**覆盖**，点击命中 inspector」。本轮在
 * `origin/main @ a889b43` 用真实 Chromium 复跑得到的机制**不同**：
 *   - 画布 `.blueprint-canvas` 是 `overflow-x: auto` 的横向流程带，宽 790px，
 *     `scrollWidth=1886`；检查器在其右侧（gap 22px），**二者不重叠**；
 *   - 第 4~7 个节点的布局坐标落在画布可见区之外，因此是被**横向滚动裁掉**，
 *     而不是被 inspector 遮挡。对布局坐标直接 `elementFromPoint` 会落到
 *     inspector 上（那里确实是该坐标处的顶层元素），这正是原描述「命中右栏」的来源；
 *   - 但节点**滚入可视区后可以正常点击与选中**（probe-click.json：7/7 一致）。
 *
 * 因此真正站得住的缺陷是：**靠后节点不可发现，且 `?node=` 深链（契约 §3.2
 * 明确「当前节点可分享」）打开后当前节点不可见**——用户拿到一个指向
 * 「版本交付」的链接，看到的却是被截断的左端。
 *
 * 判定（机器事实）：对每个 `?node=<key>` 深链，取该节点几何中心，断言它落在
 * 画布的**可见矩形**内。任一不可见即缺陷成立。
 *
 * 用法：node docs/audit/issue-204/repro-node-reachability.mjs before|after
 * 产物：docs/audit/issue-204/<NN>-node-reachability.png + <stage>-node-reachability.json
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
// 用节点键深链；覆盖「首屏可见」与「靠后不可见」两类，兼顾边界。
const NODES = ['coverage', 'generation', 'evaluation', 'rules', 'human_review', 'delivery']

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

const results = []
for (const key of NODES) {
  await page.goto(`${BASE}/p/${PROJECT_ID}/blueprint?node=${key}`, { waitUntil: 'networkidle' })
  await page.waitForTimeout(1400)
  const info = await page.evaluate((nodeKey) => {
    const canvas = document.querySelector('.blueprint-canvas')
    const node = document.querySelector(`.blueprint-node[data-node-key="${nodeKey}"]`)
    if (!canvas || !node) return { error: 'missing node/canvas' }
    const c = canvas.getBoundingClientRect()
    const r = node.getBoundingClientRect()
    const cx = r.x + r.width / 2
    const cy = r.y + r.height / 2
    // 当前节点是否完整落在画布可见矩形内（未被横向滚动裁掉、且纵向在画布内）。
    const withinX = cx >= c.left && cx <= c.right
    const withinY = cy >= c.top && cy <= c.bottom
    return {
      nodeX: Math.round(r.x), nodeRight: Math.round(r.right),
      canvasLeft: Math.round(c.left), canvasRight: Math.round(c.right),
      canvasScrollLeft: Math.round(canvas.scrollLeft),
      activeVisible: withinX && withinY,
      activeTagged: node.classList.contains('blueprint-node--active'),
      inspectorTitle: (document.querySelector('.blueprint-inspector h5')?.textContent ?? '').trim(),
    }
  }, key)
  results.push({ key, ...info })
}

const report = {
  stage, base: BASE, viewport: { width: 1600, height: 1000 },
  nodes: results,
  invisibleActiveNodes: results.filter((r) => r.activeVisible === false).map((r) => r.key),
  consoleErrors,
}
report.defectPresent = report.invisibleActiveNodes.length > 0

// 截图固定在最后一个（最靠后、最容易不可见的）深链上，便于前后对比同一画面。
await page.goto(`${BASE}/p/${PROJECT_ID}/blueprint?node=delivery`, { waitUntil: 'networkidle' })
await page.waitForTimeout(1400)
await page.locator('.blueprint-canvas').scrollIntoViewIfNeeded().catch(() => {})
await page.screenshot({ path: resolve(here, `${KEY}-node-reachability.png`) })
writeFileSync(resolve(here, `${stage}-node-reachability.json`), `${JSON.stringify(report, null, 2)}\n`)
await browser.close()

console.log(JSON.stringify(report, null, 2))
console.log(`\n[repro-204] ${stage}: 深链后不可见的当前节点 = ${report.invisibleActiveNodes.length}/${results.length}`)
process.exit(report.defectPresent ? 2 : 0)
