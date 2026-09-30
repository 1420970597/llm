/**
 * #191 第 2 轮扫描发现的两条**同类新泄漏**（清单之外）的复现/验证。
 *
 * 为什么需要它：`sweep-internal-keys.mjs` 在 22 条路由上做与手工清单无关的扫描，
 * 发现两条与 #191 同一缺陷类、但第 1 轮未逐项列出的渲染路径：
 *
 *   A. 评估工作台「维度管理」的分组标题直接渲染分类 key
 *      （`answer_quality` / `domain_fit` / `long_chain` …）
 *   B. 批次「异常恢复」页的失败卡直接渲染错误类别
 *      （「错误类别：config_error」）
 *
 * 判定：两页的**可见文本**里不得出现内部取值域 key。原码应保留在 `title` 上。
 *
 * 用法：node docs/audit/issue-191/repro-extra-leaks.mjs before|after
 * 产物：docs/audit/issue-191/<NN>-extra-eval-category.png、<NN>-extra-error-class.png
 */
import { createRequire } from 'node:module'
import { writeFileSync } from 'node:fs'
import { dirname, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

const require = createRequire(import.meta.url)
const { chromium } = require(process.env.PLAYWRIGHT_PATH ?? '/root/.pi/agent/npm/node_modules/playwright')

const here = dirname(fileURLToPath(import.meta.url))
const stage = process.argv[2] === 'after' ? 'after' : 'before'
const KEY = stage === 'after' ? '02' : '01'
const BASE = process.env.BASE_URL ?? 'http://127.0.0.1:3210'
const PROJECT_ID = process.env.PROJECT_ID ?? '1'

const browser = await chromium.launch({ headless: true })
const ctx = await browser.newContext({ viewport: { width: 1600, height: 1000 }, locale: 'zh-CN' })
const page = await ctx.newPage()
const consoleErrors = []
page.on('pageerror', (e) => consoleErrors.push(`pageerror: ${String(e).slice(0, 140)}`))
page.on('response', (r) => { if (r.status() >= 500) consoleErrors.push(`HTTP ${r.status()} ${r.url().slice(0, 80)}`) })

await page.goto(`${BASE}/login`, { waitUntil: 'networkidle' })
await page.getByPlaceholder('请输入邮箱').fill(process.env.ADMIN_EMAIL ?? 'admin@company.com')
await page.getByPlaceholder('请输入密码').fill(process.env.ADMIN_PASSWORD ?? 'admin123456')
await page.getByRole('button', { name: '进入今日工作' }).click()
await page.waitForURL(/\/today/, { timeout: 20000 })

const report = { stage, base: BASE }
/** 内置分类 key 与错误类别码：这两个取值域都在 `internal/` 里有常量定义。 */
const CATEGORY_KEYS = ['answer_quality', 'domain_fit', 'long_chain', 'faithfulness', 'instruction', 'robustness', 'efficiency']
const ERROR_CLASSES = ['config_error', 'provider_error', 'rate_limited', 'timeout', 'empty_output', 'truncated', 'invalid_json', 'schema_violation', 'internal_error']

// ------------------------------------- A. 维度管理的分类标题 ----
await page.goto(`${BASE}/tools/evaluation`, { waitUntil: 'networkidle' })
await page.waitForTimeout(2500)
// 只看**可见文本**；同时采出承载它的节点的 tag（便于定位是哪一行渲染的）。
report.category = await page.evaluate((keys) => {
  const visible = (el) => {
    const rect = el.getBoundingClientRect()
    return rect.width > 1 && rect.height > 1
  }
  const hits = []
  for (const el of document.querySelectorAll('*')) {
    if (el.children.length) continue
    const text = (el.textContent ?? '').trim()
    if (!keys.includes(text)) continue
    if (!visible(el)) continue
    hits.push({ tag: el.tagName.toLowerCase(), text, title: el.getAttribute('title') ?? '' })
  }
  return { hits, visibleEnumKeys: [...new Set(hits.map((h) => h.text))] }
}, CATEGORY_KEYS)
await page.screenshot({ path: resolve(here, `${KEY}-extra-eval-category.png`) })

// ------------------------------------- B. 异常恢复的错误类别 ----
// `/p/1/runs/b_1/failures`：b_1 的 12 条单元全部是 config_error（真实数据）。
await page.goto(`${BASE}/p/${PROJECT_ID}/runs/b_1/failures`, { waitUntil: 'networkidle' })
await page.waitForTimeout(2000)
report.errorClass = await page.evaluate((codes) => {
  const text = document.body.innerText
  const hits = []
  for (const el of document.querySelectorAll('*')) {
    if (el.children.length) continue
    const value = (el.textContent ?? '').trim()
    if (!value) continue
    const matched = codes.find((code) => value.includes(code))
    if (!matched) continue
    const rect = el.getBoundingClientRect()
    if (rect.width < 1) continue
    // 排除「保留在 title 上的原码」——那是刻意保留的排查线索，不是泄漏。
    if (el.getAttribute('title') === matched) continue
    hits.push({ tag: el.tagName.toLowerCase(), text: value.slice(0, 60), title: el.getAttribute('title') ?? '' })
  }
  return { hits: hits.slice(0, 8), visibleEnumKeys: [...new Set(hits.map((h) => h.text.match(codes.join('|'))?.[0]))].filter(Boolean) }
}, ERROR_CLASSES)
report.errorClass.bodyHasRawCode = ERROR_CLASSES.some((code) => new RegExp(`错误类别[:：]\\s*${code}`).test(report.errorClass.text ?? ''))
await page.screenshot({ path: resolve(here, `${KEY}-extra-error-class.png`) })

report.consoleErrors = consoleErrors
writeFileSync(resolve(here, `${stage}-extra-leaks.json`), `${JSON.stringify(report, null, 2)}\n`)
await browser.close()

const leaks = report.category.visibleEnumKeys.length + report.errorClass.visibleEnumKeys.length
console.log(JSON.stringify(report, null, 2))
console.log(`\n[repro-extra-leaks] ${stage}: 泄漏 = ${leaks} 处`)
process.exit(leaks > 0 ? 2 : 0)
