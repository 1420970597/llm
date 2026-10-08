/**
 * #204 子缺陷探针：`?node=` 深链（契约 §3.2「当前节点可分享」）指向的节点
 * 是否真的**可见**。
 *
 * 契约：docs/plans/atelier-implementation.md §3.2 —— `node` 参数可分享；
 * docs/architecture/blueprint-workflow-rearchitecture.md §3.2 —— 画布内部横向滚动。
 * 若深链到 `?node=delivery` 时交付节点被滚出画布可见区（用户看不到自己打开的那个节点），
 * 那么「可分享」只是 URI 层面的真，界面层面是假的。
 *
 * 用法：node docs/audit/issue-204/probe-activenode.mjs
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

const results = []
for (const key of ['coverage', 'generation', 'evaluation', 'rules', 'human_review', 'delivery']) {
  await page.goto(`${BASE}/p/1/blueprint?node=${key}`, { waitUntil: 'networkidle' })
  await page.waitForTimeout(1200)
  const info = await page.evaluate((nodeKey) => {
    const canvas = document.querySelector('.blueprint-canvas')
    const node = document.querySelector(`.blueprint-node[data-node-key="${nodeKey}"]`)
    if (!canvas || !node) return { error: 'missing node/canvas' }
    const c = canvas.getBoundingClientRect()
    const r = node.getBoundingClientRect()
    const cx = r.x + r.width / 2
    // 节点的几何中心是否落在画布的**可见矩形**内（未被横向滚动裁掉）。
    const visibleInCanvas = cx >= c.left && cx <= c.right
    const inspectorTitle = (document.querySelector('.blueprint-inspector h5')?.textContent ?? '').trim()
    return {
      nodeX: Math.round(r.x), nodeRight: Math.round(r.right),
      canvasLeft: Math.round(c.left), canvasRight: Math.round(c.right),
      canvasScrollLeft: Math.round(canvas.scrollLeft),
      visibleInCanvas,
      inspectorTitle,
    }
  }, key)
  results.push({ key, ...info })
}

const report = { base: BASE, results, invisibleActiveNodes: results.filter((r) => r.visibleInCanvas === false).map((r) => r.key) }
writeFileSync(resolve(here, 'probe-activenode.json'), `${JSON.stringify(report, null, 2)}\n`)
console.log(JSON.stringify(report, null, 2))
await browser.close()
console.log(`\n[probe-204-activenode] 深链后不可见的当前节点 = ${report.invisibleActiveNodes.length}/${results.length}`)
