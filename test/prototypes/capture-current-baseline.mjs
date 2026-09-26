/**
 * 现状取证采集器（Issues #165 / #197 改造前的基线）。
 *
 * 为什么必须采现状：本次改造的每一条主张都是对「现在长什么样」的判断。
 * 没有基线截图，读者只能相信文字描述；有了基线，任何一条都可以被推翻或确认。
 *
 * 与 test/audit/issue197.mjs 的区别：那个脚本按 #197 的 17 条逐条取证，
 * 这个脚本只采「本次方案直接改写的那几屏」，并额外记录画布几何事实
 * （节点是否可拖拽、画布宽度、是否 flex 纵向堆叠）——那些是「蓝图不是工作流」
 * 的可测量证据，而不是审美判断。
 *
 * 前置：docker compose up -d --build（真实栈，:3210）
 * 运行：node test/prototypes/capture-current-baseline.mjs
 */
import { mkdirSync, writeFileSync } from 'node:fs'
import { createRequire } from 'node:module'
import path from 'node:path'
import { fileURLToPath } from 'node:url'

const require = createRequire('/root/llm/package.json')
const { chromium } = require('/root/.pi/agent/npm/node_modules/playwright')

const REPO_ROOT = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '../..')
const OUT_DIR = path.join(REPO_ROOT, 'docs/screenshots/issue-165-197-current')
const BASE = process.env.BASE_URL ?? 'http://127.0.0.1:3210'
const EMAIL = process.env.ADMIN_EMAIL ?? 'admin@company.com'
const PASSWORD = process.env.ADMIN_PASSWORD ?? 'admin123456'
const PROJECT = process.env.PROJECT_ID ?? 'p_1'

mkdirSync(OUT_DIR, { recursive: true })

const browser = await chromium.launch({ headless: true })
const findings = []

async function collect(viewport, suffix, shots) {
  const ctx = await browser.newContext({ viewport, locale: 'zh-CN', deviceScaleFactor: 1 })
  const page = await ctx.newPage()
  page.on('pageerror', (e) => console.log('  [pageerror]', String(e).slice(0, 160)))
  await page.goto(`${BASE}/login`, { waitUntil: 'networkidle', timeout: 40000 })
  await page.getByPlaceholder('请输入邮箱').fill(EMAIL)
  await page.getByPlaceholder('请输入密码').fill(PASSWORD)
  await page.getByRole('button', { name: '进入今日工作' }).click()
  await page.waitForURL(/\/today/, { timeout: 25000 })
  await page.waitForTimeout(1200)

  for (const [route, key, probe] of shots) {
    const url = `${BASE}${route.replace(':p', PROJECT)}`
    const record = { key, route, url, viewport: suffix }
    try {
      await page.goto(url, { waitUntil: 'networkidle', timeout: 40000 })
    } catch (e) {
      record.navError = String(e).slice(0, 160)
    }
    await page.waitForTimeout(1800)
    // 布局是 body(100vh) > main.app-layout__content(overflow:auto)，fullPage 只能截视口高度。
    // 临时展开三层容器，否则「页面超长」这类事实会在证据里消失（同 issue197.mjs 的处理）。
    record.layout = await page.evaluate(() => {
      const scroller = document.querySelector('main.app-layout__content')
      if (!scroller) return { expanded: false, docHeight: document.documentElement.scrollHeight }
      const naturalHeight = scroller.scrollHeight
      for (const el of [document.documentElement, document.body, scroller.parentElement, scroller]) {
        if (!el) continue
        el.style.height = 'auto'
        el.style.maxHeight = 'none'
        el.style.overflow = 'visible'
      }
      return { expanded: true, naturalHeight, viewportHeight: window.innerHeight, overflowX: document.documentElement.scrollWidth - window.innerWidth }
    })
    if (probe) {
      try {
        record.probe = await page.evaluate(probe)
      } catch (e) {
        record.probeError = String(e).slice(0, 200)
      }
    }
    const file = path.join(OUT_DIR, `${key}-${suffix}.png`)
    await page.screenshot({ path: file, fullPage: true })
    record.screenshot = path.relative(REPO_ROOT, file)
    findings.push(record)
    console.log(`  ${record.screenshot}  natural=${record.layout.naturalHeight ?? '?'}  overflowX=${record.layout.overflowX ?? '?'}`)
  }
  await ctx.close()
}

const BLUEPRINT_PROBE = () => {
  const canvas = document.querySelector('.blueprint-canvas')
  const nodes = [...document.querySelectorAll('.blueprint-node')]
  const scroller = document.querySelector('main.app-layout__content')
  return {
    canvasWidth: canvas ? Math.round(canvas.getBoundingClientRect().width) : null,
    canvasHeight: canvas ? Math.round(canvas.getBoundingClientRect().height) : null,
    canvasDisplay: canvas ? getComputedStyle(canvas).display : null,
    canvasFlexDirection: canvas ? getComputedStyle(canvas).flexDirection : null,
    nodeCount: nodes.length,
    nodeKeys: nodes.map((n) => n.getAttribute('data-node-key')),
    nodesAreDraggable: nodes.some((n) => n.getAttribute('draggable') === 'true'),
    hasEdgeOrConnector: Boolean(document.querySelector('.blueprint-canvas svg, .blueprint-edge, [data-edge]')),
    hasCanvasZoom: Boolean(document.querySelector('[data-zoom], .blueprint-zoom')),
    viewportHeight: window.innerHeight,
    docHeight: scroller ? scroller.scrollHeight : document.documentElement.scrollHeight,
    inkOverlap: (() => {
      // 右栏长文本是否压住保存按钮：比较右栏内文本节点与按钮的矩形是否相交。
      const buttons = [...document.querySelectorAll('.blueprint-inspector button')]
      const texts = [...document.querySelectorAll('.blueprint-inspector span, .blueprint-inspector div')]
      let overlaps = 0
      for (const t of texts) {
        const rt = t.getBoundingClientRect()
        if (rt.width === 0 || rt.height === 0) continue
        for (const b of buttons) {
          const rb = b.getBoundingClientRect()
          if (rt.bottom > rb.top && rt.top < rb.bottom && rt.right > rb.left && rt.left < rb.right) overlaps += 1
        }
      }
      return overlaps
    })(),
  }
}

const SAME_PAGE_PROBE = () => ({
  heading: document.querySelector('h1')?.textContent?.trim() ?? null,
  tabs: [...document.querySelectorAll('.atelier-project-tabs a, nav a')].map((a) => a.textContent.trim()).filter(Boolean).slice(0, 12),
  bodyFingerprint: document.body.innerText.replace(/\s+/g, ' ').slice(0, 400),
})

const DESKTOP = [
  [`/p/:p/blueprint`, '11-current-blueprint-canvas', BLUEPRINT_PROBE],
  [`/p/:p/blueprint?node=generation`, '12-current-blueprint-generation-node', BLUEPRINT_PROBE],
  [`/p/:p/coverage`, '13-current-coverage-matrix', null],
  [`/p/:p/data`, '14a-current-project-data', SAME_PAGE_PROBE],
  [`/p/:p/review`, '14b-current-project-review', SAME_PAGE_PROBE],
  [`/p/:p/runs`, '15-current-batch-runs', null],
  [`/p/:p/quality/new`, '16-current-quality-new', null],
  ['/settings/connections', '17-current-connections', null],
  ['/settings/team', '18-current-team', null],
  ['/today', '19-current-today', null],
  ['/legacy/history', '20-current-legacy-history', null],
]

console.log('desktop 1600×1000')
await collect({ width: 1600, height: 1000 }, 'desktop', DESKTOP)
console.log('mobile 390×844')
await collect({ width: 390, height: 844 }, 'mobile', [
  [`/p/:p/blueprint`, '11-current-blueprint-canvas', BLUEPRINT_PROBE],
  [`/p/:p/data`, '14a-current-project-data', SAME_PAGE_PROBE],
  ['/settings/connections', '17-current-connections', null],
])

await browser.close()
writeFileSync(
  path.join(OUT_DIR, 'baseline.json'),
  JSON.stringify({ base: BASE, project: PROJECT, collectedAt: new Date().toISOString(), findings }, null, 2) + '\n',
)
console.log(`\n${findings.length} 张截图 + baseline.json`)
