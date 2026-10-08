/**
 * #204 结构探针：画布的滚动可达性（是否真的能滚到后面的节点）。
 *
 * 判定：滚动条是否可见/可用；scrollLeft 能否到 scrollWidth-clientWidth；
 * 画布的 overflow 规则。用于区分「设计内的横向滚动」与「真正不可达」。
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

const info = await page.evaluate(() => {
  const c = document.querySelector('.blueprint-canvas')
  const cs = getComputedStyle(c)
  const before = c.scrollLeft
  c.scrollLeft = 99999
  const after = c.scrollLeft
  c.scrollLeft = 0
  return {
    className: c.className,
    overflowX: cs.overflowX,
    overflowY: cs.overflowY,
    scrollbarWidth: cs.scrollbarWidth,
    clientWidth: c.clientWidth,
    clientHeight: c.clientHeight,
    offsetHeight: c.offsetHeight,
    offsetWidth: c.offsetWidth,
    scrollWidth: c.scrollWidth,
    scrollHeight: c.scrollHeight,
    maxScrollLeft: c.scrollWidth - c.clientWidth,
    canScroll: c.scrollWidth > c.clientWidth,
    horizontalScrollbarVisible: c.offsetHeight > c.clientHeight,
    scrollLeftBefore: before,
    scrollLeftAfter: after,
  }
})
console.log(JSON.stringify(info, null, 2))
await page.screenshot({ path: resolve(here, 'probe-canvas-scroll.png') })
await browser.close()
