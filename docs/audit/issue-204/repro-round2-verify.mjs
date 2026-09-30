/**
 * #204 第 2 轮：在「含第 1 轮修复的分支上」做更宽的复核 + 残留项取证。
 *
 * 第 1 轮只在 1600×1000 单视口取证。本轮回答两个第 1 轮没回答的问题：
 *   A. 第 1 轮的深链修复在**全部受支持视口**（1600/1440/1280/1024/390）是否都成立？
 *   B. 残留项到底是什么、有多严重？—— 默认载入时画布**裁掉**多少节点，
 *      以及「还能横向滚动」这件事在首屏有没有任何提示。
 *
 * 判定全部是机器事实。输出用于决定本轮落点（继续迭代 / 升级人工），不替代截图。
 *
 * 用法：node docs/audit/issue-204/repro-round2-verify.mjs
 * 产物：docs/audit/issue-204/round2-verify.png + round2-verify.json
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
const NODES = ['coverage', 'standard', 'generation', 'evaluation', 'rules', 'human_review', 'delivery']
const VIEWPORTS = [
  { w: 1600, h: 1000 }, { w: 1440, h: 900 }, { w: 1280, h: 800 },
  { w: 1024, h: 768 }, { w: 390, h: 844 },
]

const browser = await chromium.launch({ headless: true })
const report = { base: BASE, viewports: [] }

for (const vp of VIEWPORTS) {
  const ctx = await browser.newContext({ viewport: { width: vp.w, height: vp.h }, locale: 'zh-CN' })
  const page = await ctx.newPage()
  await page.goto(`${BASE}/login`, { waitUntil: 'networkidle' })
  await page.getByPlaceholder('请输入邮箱').fill(process.env.ADMIN_EMAIL ?? 'admin@company.com')
  await page.getByPlaceholder('请输入密码').fill(process.env.ADMIN_PASSWORD ?? 'admin123456')
  await page.getByRole('button', { name: '进入今日工作' }).click()
  await page.waitForURL(/\/today/, { timeout: 20000 })

  // A. 深链可达性（每个 ?node= 的当前节点是否水平 + 垂直都可见）
  const deepLinks = []
  for (const key of NODES) {
    await page.goto(`${BASE}/p/${PROJECT_ID}/blueprint?node=${key}`, { waitUntil: 'networkidle' })
    await page.waitForTimeout(1100)
    const r = await page.evaluate((k) => {
      const canvas = document.querySelector('.blueprint-canvas')
      const node = document.querySelector(`.blueprint-node[data-node-key="${k}"]`)
      if (!canvas || !node) return { error: 'missing' }
      const c = canvas.getBoundingClientRect(), n = node.getBoundingClientRect()
      const cx = n.x + n.width / 2
      return {
        canvasScrollLeft: Math.round(canvas.scrollLeft),
        horizontallyVisible: cx >= c.left && cx <= c.right,
        verticallyVisible: n.top >= -1 && n.bottom <= innerHeight + 1,
      }
    }, key)
    deepLinks.push({ key, ...r })
  }

  // B. 默认载入：画布完整可见的节点数 + 是否有横向滚动提示
  await page.goto(`${BASE}/p/${PROJECT_ID}/blueprint`, { waitUntil: 'networkidle' })
  await page.waitForTimeout(1500)
  const discoverability = await page.evaluate(() => {
    const canvas = document.querySelector('.blueprint-canvas')
    const c = canvas.getBoundingClientRect()
    const nodes = [...document.querySelectorAll('.blueprint-node')].map((n) => {
      const r = n.getBoundingClientRect()
      return { key: n.dataset.nodeKey, fullyVisibleInCanvas: r.x >= c.left - 1 && r.right <= c.right + 1 }
    })
    const hint = (document.querySelector('.blueprint-canvas__hint')?.textContent ?? '').trim()
    return {
      nodeCount: nodes.length,
      fullyVisible: nodes.filter((n) => n.fullyVisibleInCanvas).length,
      clipped: nodes.filter((n) => !n.fullyVisibleInCanvas).map((n) => n.key),
      horizontalScrollbarBelowFold: c.bottom > innerHeight && canvas.scrollWidth > canvas.clientWidth,
      hintText: hint,
      hintMentionsHorizontalScroll: /横向|滚动|scroll/i.test(hint),
    }
  })

  report.viewports.push({
    viewport: `${vp.w}x${vp.h}`,
    deepLinkInvisible: deepLinks.filter((d) => !(d.horizontallyVisible && d.verticallyVisible)).map((d) => d.key),
    discoverability,
  })
  if (vp.w === 1600) {
    await page.screenshot({ path: resolve(here, 'round2-verify.png') })
  }
  await ctx.close()
}

report.deepLinkRegressionAnyViewport = report.viewports.some((v) => v.deepLinkInvisible.length > 0)
report.discoverabilityGapAnyViewport = report.viewports.some((v) => v.discoverability.clipped.length > 0)
writeFileSync(resolve(here, 'round2-verify.json'), `${JSON.stringify(report, null, 2)}\n`)
await browser.close()

console.log(JSON.stringify(report, null, 2))
console.log(`\n[repro-204-r2] 深链回归（任一视口）= ${report.deepLinkRegressionAnyViewport}；` +
  `默认被裁节点（任一视口）= ${report.discoverabilityGapAnyViewport}`)
