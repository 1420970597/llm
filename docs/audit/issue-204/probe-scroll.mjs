/**
 * #204 深挖探针：区分「被右栏覆盖」与「只是被画布滚动裁掉」。
 *
 * 判定逻辑：
 *   - 若把画布滚到最右后，节点命中测试通过 → 缺陷只是「横向滚动不可发现」，
 *     不是覆盖；
 *   - 若滚到最右后节点仍被 inspector 命中 / 仍不可达 → 覆盖成立。
 *
 * 用法：node docs/audit/issue-204/probe-scroll.mjs
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

async function measure(tag) {
  return page.evaluate((tag) => {
    const canvas = document.querySelector('.blueprint-canvas')
    const nodes = [...document.querySelectorAll('.blueprint-node')]
    const c = canvas.getBoundingClientRect()
    return {
      tag,
      canvas: { left: Math.round(c.left), right: Math.round(c.right), clientWidth: canvas.clientWidth, scrollWidth: canvas.scrollWidth, scrollLeft: canvas.scrollLeft },
      nodes: nodes.map((node) => {
        const r = node.getBoundingClientRect()
        const cx = r.x + r.width / 2, cy = r.y + r.height / 2
        // 该中心是否落在画布的可见盒内（未被滚动裁掉）
        const insideCanvasBox = cx >= c.left && cx <= c.right && cy >= c.top && cy <= c.bottom
        const hit = insideCanvasBox ? document.elementFromPoint(cx, cy) : null
        return {
          label: node.querySelector('.blueprint-node__label')?.textContent?.trim(),
          x: Math.round(r.x), right: Math.round(r.right),
          visibleInCanvas: insideCanvasBox,
          hitClass: hit ? (hit.className || hit.tagName).toString().slice(0, 50) : null,
          hitIsSelf: hit ? hit.closest('.blueprint-node') === node : false,
        }
      }),
    }
  }, tag)
}

const initial = await measure('initial')
console.log('INITIAL:', JSON.stringify(initial, null, 2))

// 把画布滚到最右
await page.evaluate(() => { const c = document.querySelector('.blueprint-canvas'); c.scrollLeft = c.scrollWidth })
await page.waitForTimeout(600)
const scrolled = await measure('scrolled-to-end')
console.log('SCROLLED:', JSON.stringify(scrolled, null, 2))

await page.screenshot({ path: resolve(here, '01b-blueprint-scrolled.png') })
await browser.close()
