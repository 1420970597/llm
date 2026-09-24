/**
 * 甲方验收审计：登录 + 路由可达性烟雾测试。
 */
import { createRequire } from 'node:module'
const require = createRequire('/root/llm/package.json')
const { chromium } = require('/root/.pi/agent/npm/node_modules/playwright')

const BASE = process.env.BASE_URL ?? 'http://127.0.0.1:3210'
const EMAIL = 'admin@company.com'
const PASSWORD = 'admin123456'

const browser = await chromium.launch({ headless: true })
const ctx = await browser.newContext({ viewport: { width: 1440, height: 960 }, locale: 'zh-CN' })
const page = await ctx.newPage()
const errors = []
page.on('console', (m) => { if (m.type() === 'error') errors.push(`console: ${m.text().slice(0, 200)}`) })
page.on('pageerror', (e) => errors.push(`pageerror: ${String(e).slice(0, 200)}`))
page.on('response', (r) => { if (r.status() >= 400) errors.push(`http ${r.status()} ${r.url().slice(0, 160)}`) })

await page.goto(`${BASE}/login`, { waitUntil: 'networkidle' })
console.log('login url:', page.url())
await page.getByPlaceholder('请输入邮箱').fill(EMAIL)
await page.getByPlaceholder('请输入密码').fill(PASSWORD)
await page.getByRole('button', { name: '进入今日工作' }).click()
await page.waitForTimeout(3000)
console.log('after login url:', page.url())
console.log('title:', await page.title())
const body = await page.locator('body').innerText()
console.log('---BODY (first 1500)---')
console.log(body.slice(0, 1500))
console.log('---ERRORS---')
console.log([...new Set(errors)].slice(0, 30).join('\n') || '(none)')
await browser.close()
