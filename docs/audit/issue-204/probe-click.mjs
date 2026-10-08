/**
 * #204 交互探针：把每个节点滚入画布可见区后，用**真实鼠标**点它中心，
 * 观察 URL 的 ?node= 与右栏检查器标题是否一致。
 *
 * 目的：区分两种缺陷
 *   (A) 节点根本无法点（真实覆盖/命中错误）—— 修复必须动布局；
 *   (B) 节点可达，但画布无「还有更多节点」的可发现性 —— 修复是加可达性/滚动提示。
 *
 * 用法：node docs/audit/issue-204/probe-click.mjs
 */
import { createRequire } from 'node:module'
import { writeFileSync } from 'node:fs'
import { dirname, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

const require = createRequire(import.meta.url)
const { chromium } = require(process.env.PLAYWRIGHT_PATH ?? '/root/.pi/agent/npm/node_modules/playwright')
const here = dirname(fileURLToPath(import.meta.url))
const BASE = process.env.BASE_URL ?? 'http://127.0.0.1:3210'

const browser = await chromium.launch({ headless: true })
const ctx = await browser.newContext({ viewport: { width: 1600, height: 1000 }, locale: 'zh-CN' })
const page = await ctx.newPage()
await page.goto(`${BASE}/login`, { waitUntil: 'networkidle' })
await page.getByPlaceholder('请输入邮箱').fill('admin@company.com')
await page.getByPlaceholder('请输入密码').fill('admin123456')
await page.getByRole('button', { name: '进入今日工作' }).click()
await page.waitForURL(/\/today/, { timeout: 20000 })
await page.goto(`${BASE}/p/1/blueprint`, { waitUntil: 'networkidle' })
await page.waitForTimeout(1500)

const results = []
const count = await page.locator('.blueprint-node').count()
for (let i = 0; i < count; i += 1) {
  const node = page.locator('.blueprint-node').nth(i)
  const meta = await node.evaluate((el) => ({ key: el.getAttribute('data-node-key'), label: el.querySelector('.blueprint-node__label')?.textContent?.trim() }))
  // 滚入画布可见区（用户要看到它才能点）
  await node.evaluate((el) => el.scrollIntoView({ block: 'nearest', inline: 'center' }))
  await page.waitForTimeout(250)
  const box = await node.boundingBox()
  const insideCanvas = await node.evaluate((el) => {
    const c = document.querySelector('.blueprint-canvas').getBoundingClientRect()
    const r = el.getBoundingClientRect()
    const cx = r.x + r.width / 2, cy = r.y + r.height / 2
    return cx >= c.left && cx <= c.right && cy >= c.top && cy <= c.bottom
  })
  let hitSelf = false
  let urlAfterClick = ''
  let inspectorTitle = ''
  if (box && insideCanvas) {
    hitSelf = await node.evaluate((el) => {
      const r = el.getBoundingClientRect()
      const hit = document.elementFromPoint(r.x + r.width / 2, r.y + r.height / 2)
      return hit?.closest('.blueprint-node') === el
    })
    // URL 归零，避免上一次点击残留
    await page.evaluate(() => { const u = new URL(location.href); u.searchParams.delete('node'); history.replaceState(null, '', u) })
    await page.mouse.click(box.x + box.width / 2, box.y + box.height / 2)
    await page.waitForTimeout(600)
    urlAfterClick = new URL(page.url()).searchParams.get('node') ?? ''
    inspectorTitle = await page.locator('.blueprint-inspector h5').first().textContent().catch(() => '')
  }
  results.push({ index: i, ...meta, insideCanvas, hitSelf, urlAfterClick, inspectorTitle: (inspectorTitle ?? '').trim(), urlMatchesNode: urlAfterClick === meta.key, inspectorMatchesNode: (inspectorTitle ?? '').trim() === meta.label })
}

const report = { base: BASE, nodes: results, failures: results.filter((r) => !r.urlMatchesNode || !r.inspectorMatchesNode) }
writeFileSync(resolve(here, 'probe-click.json'), `${JSON.stringify(report, null, 2)}\n`)
console.log(JSON.stringify(report, null, 2))
await browser.close()
console.log(`\n[probe-204-click] 与预期不一致的节点 = ${report.failures.length}/${results.length}`)
