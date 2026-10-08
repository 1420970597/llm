/**
 * #204 判别探针：节点到底是「被右栏覆盖」还是「被画布裁掉 + 需要横向滚动」。
 *
 * 方法：沿每个节点的水平中线采样，记录每个采样点 elementFromPoint 的归属，
 * 并算出该节点在**画布可见盒内**的可见宽度占比。
 */
import { createRequire } from 'node:module'
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

const result = await page.evaluate(() => {
  const canvas = document.querySelector('.blueprint-canvas')
  const inspector = document.querySelector('.blueprint-inspector')
  const cr = canvas.getBoundingClientRect()
  const ir = inspector.getBoundingClientRect()
  const nodes = [...document.querySelectorAll('.blueprint-node')]
  const report = []
  for (const node of nodes) {
    const r = node.getBoundingClientRect()
    const cy = r.y + r.height / 2
    // 节点与画布可见盒的交集宽度
    const overlapLeft = Math.max(r.left, cr.left)
    const overlapRight = Math.min(r.right, cr.right)
    const visibleWidth = Math.max(0, overlapRight - overlapLeft)
    // 在交集内采样，看命中归属
    const samples = []
    if (visibleWidth > 4) {
      for (let t = 0; t <= 4; t += 1) {
        const x = overlapLeft + (visibleWidth * t) / 4
        const hit = document.elementFromPoint(x, cy)
        samples.push({
          x: Math.round(x),
          hit: hit ? (hit.className || hit.tagName).toString().slice(0, 44) : null,
          isSelf: hit ? hit.closest('.blueprint-node') === node : false,
        })
      }
    }
    // 节点与右栏的交集
    const inspOverlap = Math.max(0, Math.min(r.right, ir.right) - Math.max(r.left, ir.left))
    report.push({
      label: node.querySelector('.blueprint-node__label')?.textContent?.trim(),
      nodeX: Math.round(r.left), nodeRight: Math.round(r.right),
      visibleInCanvasWidth: Math.round(visibleWidth),
      visibleRatio: Math.round((visibleWidth / r.width) * 100) / 100,
      inspectorOverlapWidth: Math.round(inspOverlap),
      samples,
    })
  }
  return {
    canvas: { left: Math.round(cr.left), right: Math.round(cr.right), clientWidth: canvas.clientWidth, scrollWidth: canvas.scrollWidth, overflowX: getComputedStyle(canvas).overflowX, overflowY: getComputedStyle(canvas).overflowY },
    inspector: { left: Math.round(ir.left), right: Math.round(ir.right) },
    canvasBottomY: Math.round(cr.bottom),
    viewportHeight: window.innerHeight,
    nodes: report,
  }
})

console.log(JSON.stringify(result, null, 2))
await browser.close()
