/**
 * #207 复核：帮助页承诺的审阅 J/K 快捷键是否真的可用。
 *
 * 判定是**机器事实**：在真实栈的真实 Chromium 里，记录按键前后的 URL 路径。
 * `J` 必须移向下一条、`K` 必须移回上一条；两者都不动即「文档承诺了不存在的功能」。
 *
 * 与 #191 第 1 轮同一处理：修复已进入 main，本机部署的镜像**已含修复**，
 * 因此**无法再产出真正的「修复前」图**。before 图复用既有归档
 * `docs/audit/issue-214/01-207-keyboard-nav.png`（由 `91e7e1a` 之前那次部署采集，
 * 附带 `before-open.json` 的 `jWorks:false` 读数），不伪造 before。
 *
 * 用法：node docs/audit/issue-207/repro-review-shortcut.mjs after
 * 产物：docs/audit/issue-207/02-after-keyboard-nav.png + after-keyboard.json
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
const SAMPLE_ID = process.env.SAMPLE_ID ?? 's_5'

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

// 帮助页当前到底承诺了什么（判定「文档承诺」是否还在）。
await page.goto(`${BASE}/help`, { waitUntil: 'networkidle' })
await page.waitForTimeout(800)
const helpText = await page.locator('body').innerText()
const helpPromise = (helpText.match(/审阅队列[^\n]*/g) ?? []).join(' | ')

await page.goto(`${BASE}/p/${PROJECT_ID}/data/${SAMPLE_ID}`, { waitUntil: 'networkidle' })
await page.waitForTimeout(1500)
// 可见的样本身份由页头副标题给出（`<resourceId> · v<version> · 内容 hash ...`）。
const identityOf = () => page.locator('.console-page__header > div').first().innerText().then((t) => t.replace(/\n+/g, ' · ').trim()).catch(() => '')
const start = new URL(page.url()).pathname
const startIdentity = await identityOf()
await page.keyboard.press('j')
await page.waitForTimeout(1500)
const afterJ = new URL(page.url()).pathname
const jIdentity = await identityOf()
await page.keyboard.press('k')
await page.waitForTimeout(1500)
const afterK = new URL(page.url()).pathname
const kIdentity = await identityOf()

// 输入框里按 j 不得抢键（这是「实现正确」的边界路径：不能吞掉用户输入）。
await page.goto(`${BASE}/p/${PROJECT_ID}/data/${SAMPLE_ID}`, { waitUntil: 'networkidle' })
await page.waitForTimeout(1200)
const beforeTyping = new URL(page.url()).pathname
const reasonBox = page.getByPlaceholder(/例如：规则命中为误报/).first()
await reasonBox.click()
await reasonBox.type('jk', { delay: 40 })
await page.waitForTimeout(800)
const afterTyping = new URL(page.url()).pathname
const typedValue = await reasonBox.inputValue().catch(() => '')

const report = {
  base: BASE, viewport: { width: 1600, height: 1000 },
  helpPromise,
  shortcuts: {
    start, afterJ, afterK, startIdentity, jIdentity, kIdentity,
    jWorks: afterJ !== start && afterJ !== afterK,
    kWorks: afterK === start,
  },
  inputGuard: { beforeTyping, afterTyping, typedValue, navigatedWhileTyping: beforeTyping !== afterTyping },
  consoleErrors,
}
report.defectPresent = !(report.shortcuts.jWorks && report.shortcuts.kWorks)

await page.goto(`${BASE}/p/${PROJECT_ID}/data/${SAMPLE_ID}`, { waitUntil: 'networkidle' })
await page.waitForTimeout(1200)
await page.keyboard.press('j')
await page.waitForTimeout(1200)
await page.screenshot({ path: resolve(here, '02-after-keyboard-nav.png') })
writeFileSync(resolve(here, 'after-keyboard.json'), `${JSON.stringify(report, null, 2)}\n`)
await browser.close()

console.log(JSON.stringify(report, null, 2))
console.log(`\n[repro-207] J: ${start} → ${afterJ}；K: → ${afterK}（缺陷${report.defectPresent ? '仍在' : '已消失'}）`)
process.exit(report.defectPresent ? 2 : 0)
