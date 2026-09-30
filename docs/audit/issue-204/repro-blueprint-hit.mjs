/**
 * #204 复现/验证：蓝图画布节点被右栏检查器遮挡，真实鼠标点击命中右栏。
 *
 * 判定（机器事实，不靠肉眼）：
 *   对每个 `.blueprint-node` 取几何中心，用 `document.elementFromPoint` 做命中
 *   测试。若命中的元素不落在该节点自身（`.closest('.blueprint-node') !== node`），
 *   该节点即「不可点击」—— 正是 issue 描述里 4/7 不可点的形态。
 *
 * 用法：node docs/audit/issue-204/repro-blueprint-hit.mjs before|after
 * 产物：docs/audit/issue-204/<NN>-blueprint-hit.png + <stage>-blueprint-hit.json
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
page.on('response', (r) => { if (r.status() >= 400) consoleErrors.push(`HTTP ${r.status()} ${r.url().replace(BASE, '').slice(0, 90)}`) })

await page.goto(`${BASE}/login`, { waitUntil: 'networkidle' })
await page.getByPlaceholder('请输入邮箱').fill(process.env.ADMIN_EMAIL ?? 'admin@company.com')
await page.getByPlaceholder('请输入密码').fill(process.env.ADMIN_PASSWORD ?? 'admin123456')
await page.getByRole('button', { name: '进入今日工作' }).click()
await page.waitForURL(/\/today/, { timeout: 20000 })

await page.goto(`${BASE}/p/${PROJECT_ID}/blueprint`, { waitUntil: 'networkidle' })
await page.waitForTimeout(2000)

const report = { stage, base: BASE, viewport: { width: 1600, height: 1000 } }

report.geometry = await page.evaluate(() => {
  const canvas = document.querySelector('.blueprint-canvas')
  const inspector = document.querySelector('.blueprint-inspector')
  const c = canvas?.getBoundingClientRect()
  const i = inspector?.getBoundingClientRect()
  return {
    canvas: c ? { x: Math.round(c.x), width: Math.round(c.width), scrollWidth: canvas.scrollWidth, overflowX: getComputedStyle(canvas).overflowX } : null,
    inspector: i ? { x: Math.round(i.x), width: Math.round(i.width) } : null,
  }
})

// 命中测试：只渲染在视口内、且其中心点在视口内的节点才做真实命中判定
// （视口外的节点用户本来就点不到，属于滚动范围问题，由 scrollWidth 一项表达）。
report.nodes = await page.evaluate(() => {
  const nodes = [...document.querySelectorAll('.blueprint-node')]
  return nodes.map((node, index) => {
    const r = node.getBoundingClientRect()
    const cx = r.x + r.width / 2
    const cy = r.y + r.height / 2
    const inViewport = r.width > 0 && r.height > 0 && cx >= 0 && cx <= window.innerWidth && cy >= 0 && cy <= window.innerHeight
    const hit = inViewport ? document.elementFromPoint(cx, cy) : null
    const hitNode = hit?.closest('.blueprint-node') ?? null
    const hitInspector = hit?.closest('.blueprint-inspector') ?? null
    return {
      index,
      key: node.getAttribute('data-node-key'),
      label: node.querySelector('.blueprint-node__label')?.textContent?.trim() ?? '',
      x: Math.round(r.x),
      right: Math.round(r.right),
      inViewport,
      hitClass: hit ? (hit.className || hit.tagName).toString().slice(0, 60) : null,
      hitIsSelf: hitNode === node,
      hitByInspector: hitInspector !== null,
    }
  })
})

report.clippedByInspector = report.nodes.filter((n) => n.inViewport && n.hitByInspector).map((n) => n.key)
report.unhittableInViewport = report.nodes.filter((n) => n.inViewport && !n.hitIsSelf).map((n) => n.key)
report.nodesInViewport = report.nodes.filter((n) => n.inViewport).length
report.nodesTotal = report.nodes.length
report.consoleErrors = consoleErrors

await page.locator('.blueprint-canvas').scrollIntoViewIfNeeded().catch(() => {})
await page.screenshot({ path: resolve(here, `${KEY}-blueprint-hit.png`) })
writeFileSync(resolve(here, `${stage}-blueprint-hit.json`), `${JSON.stringify(report, null, 2)}\n`)
await browser.close()

console.log(JSON.stringify(report, null, 2))
const broken = report.unhittableInViewport.length
console.log(`\n[repro-204] ${stage}: 视口内不可命中节点 = ${broken}（总节点 ${report.nodesTotal}，视口内 ${report.nodesInViewport}）`)
process.exit(broken > 0 ? 2 : 0)
