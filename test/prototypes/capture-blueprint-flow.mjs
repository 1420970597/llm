/**
 * 原型截图采集器（Issues #165 / #197 蓝图工作流化改造原型）。
 *
 * 与 docs/design/2026-09-21-data-studio 的既有约定一致：真实 Chromium（Playwright，
 * headless），桌面 1440×1024 与移动 390×844 两个视口，截图落
 * docs/prototypes/blueprint-workflow-rearchitecture/shots/。
 *
 * 为什么用真实浏览器而不是设计稿：原型是可点击的 HTML，截图必须证明
 * 「它真的渲染成这样」，包括 SVG 连线、响应式折叠与长文本省略。
 *
 * 前置：
 *   cd docs && python3 -m http.server 8899 --bind 127.0.0.1 &
 *   docker run --rm -v "$PWD":/w -w /w node:20-alpine node build.mjs
 * 运行：
 *   node test/prototypes/capture-blueprint-flow.mjs
 */
import { mkdirSync } from 'node:fs'
import { createRequire } from 'node:module'
import path from 'node:path'
import { fileURLToPath } from 'node:url'

const require = createRequire('/root/llm/package.json')
const { chromium } = require('/root/.pi/agent/npm/node_modules/playwright')

const REPO_ROOT = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '../..')
const OUT_DIR = path.join(REPO_ROOT, 'docs/prototypes/blueprint-workflow-rearchitecture/shots')
const BASE = process.env.PROTOTYPE_URL ?? 'http://127.0.0.1:8899/prototypes/blueprint-workflow-rearchitecture/prototype.html'

mkdirSync(OUT_DIR, { recursive: true })

const SCREENS = [
  ['s01', 's01-target-structure'],
  ['s02', 's02-workflow-canvas'],
  ['s03', 's03-source-documents'],
  ['s04', 's04-dataset-preview'],
  ['s05', 's05-data-review'],
  ['s06', 's06-quality-experiment'],
  ['s07', 's07-ux-fixes'],
  ['s08', 's08-five-states'],
  ['s09', 's09-grounded-questions'],
]

const browser = await chromium.launch({ headless: true })
const problems = []

async function capture(viewport, suffix, keys) {
  const ctx = await browser.newContext({ viewport, locale: 'zh-CN', deviceScaleFactor: 1 })
  const page = await ctx.newPage()
  page.on('pageerror', (e) => problems.push(`${suffix} pageerror: ${String(e).slice(0, 200)}`))
  page.on('console', (m) => {
    if (m.type() === 'error') problems.push(`${suffix} console: ${m.text().slice(0, 200)}`)
  })
  for (const [key, name] of keys) {
    await page.goto(`${BASE}#/${key}`, { waitUntil: 'load', timeout: 30000 })
    await page.waitForTimeout(350)
    const file = path.join(OUT_DIR, `${name}-${suffix}.png`)
    await page.screenshot({ path: file, fullPage: true })
    const height = await page.evaluate(() => document.documentElement.scrollHeight)
    const overflow = await page.evaluate(() => document.documentElement.scrollWidth - window.innerWidth)
    if (overflow > 1) problems.push(`${name}-${suffix}: 横向溢出 ${overflow}px`)
    console.log(`  ${path.relative(REPO_ROOT, file)}  (docHeight=${height})`)
  }
  await ctx.close()
}

console.log('desktop 1440×1024')
await capture({ width: 1440, height: 1024 }, 'desktop', SCREENS)
console.log('mobile 390×844')
await capture({ width: 390, height: 844 }, 'mobile', SCREENS)
await browser.close()

if (problems.length > 0) {
  console.error('\n发现 %d 个问题：', problems.length)
  for (const p of problems) console.error('  - ' + p)
  process.exitCode = 1
} else {
  console.log('\n无 console 错误、无横向溢出。')
}
