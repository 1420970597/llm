/**
 * #214 子项 #213 复现/验证：交付映射「必填」复选框的可访问名 +
 * 发布表单的多字段错误展示。
 *
 * 判定（两条机器事实）：
 *   A. 映射区每个 `input[type=checkbox]` 都必须有**非空且互不相同**的可访问名。
 *      缺陷形态实测：3 行完全相同（`aria-label: null`、无 id、无 label 关联），
 *      读屏只会念出三个一模一样的「必填」。
 *   B. 同时在「范围为空」与「用途为空」两种条件下提交，两次的字段错误都必须
 *      **同时可见**（而不是后一条覆盖前一条）。修复前只有最后一条能留在页面上。
 *
 * 用法：node docs/audit/issue-214/repro-213.mjs before|after
 * 产物：docs/audit/issue-214/<NN>-213-mapping-a11y.png、<NN>-213-field-errors.png
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

await page.goto(`${BASE}/p/${PROJECT_ID}/releases/new`, { waitUntil: 'networkidle' })
await page.waitForTimeout(2000)

const report = { stage, base: BASE }

// ------------------------------------------------ A. 可访问名 ----
// 可访问名 = aria-label，或经 label[for] / 包裹 label 关联的可见文本。
report.checkboxes = await page.locator('.document-editor__checkbox input[type="checkbox"]').evaluateAll((inputs) => inputs.map((input) => {
  const id = input.getAttribute('id') ?? ''
  const aria = input.getAttribute('aria-label') ?? ''
  const labelFor = id ? document.querySelector(`label[for="${CSS.escape(id)}"]`) : null
  const wrapped = input.closest('label')
  const text = (labelFor?.textContent ?? wrapped?.textContent ?? '').trim()
  return { id, ariaLabel: aria, labelText: text, accessibleName: aria || text }
}))
report.checkboxNames = report.checkboxes.map((item) => item.accessibleName)
report.accessibleNameIssues = report.checkboxes
  .filter((item) => !item.accessibleName)
  .length
report.duplicateNames = report.checkboxNames.length !== new Set(report.checkboxNames).size

// Playwright 自身的可访问性快照：这是「读屏会念什么」的权威读数
// （与手写 DOM 提取相比，它能抓到「名字来自列头」这类间接关联）。
//
// 注意：本机装的 Playwright 1.63 已移除旧的 `page.accessibility.snapshot()`，
// 改为面向 locator 的 `ariaSnapshot()`（返回 YAML 文本）。用它而不是降级成
// 只看 `aria-label` 属性 —— 后者抓不到「名字来自列头 / 包裹 label」的关联，
// 而那正是本缺陷要区分的两类行。
report.a11ySnapshot = await page.locator('.document-editor__checkbox')
  .first()
  .ariaSnapshot()
  .then((text) => text.split('\n').map((line) => line.trim()).filter(Boolean))
  .catch((err) => [`ariaSnapshot failed: ${String(err).slice(0, 120)}`])
report.a11ySnapshotAll = await Promise.all(
  Array.from({ length: report.checkboxes.length }, (_, index) =>
    page.locator('.document-editor__checkbox').nth(index).ariaSnapshot()
      .then((text) => text.split('\n').map((line) => line.trim()).filter(Boolean).join(' '))
      .catch(() => '')),
)

await page.locator('#mapping-editor').scrollIntoViewIfNeeded()
await page.screenshot({ path: resolve(here, `${KEY}-213-mapping-a11y.png`) })

// ------------------------------------------------ B. 多字段错误 ----
// 先什么都不选、什么都不填直接提交：期望「发布范围」的字段级错误出现。
await page.getByRole('button', { name: '创建发布候选' }).click()
await page.waitForTimeout(1200)
report.emptySubmitText = await page.evaluate(() => document.body.innerText)
report.emptySubmitFieldError = await page.locator('[data-range-error="true"]').innerText().catch(() => '')
report.emptySubmitHasRangeAnchor = await page.locator('[data-range-error="true"]').count()

// 再勾一条内容版本、但用途留空：期望「用途」的字段级错误出现，
// 且（修复后）范围那条不会因为同一个 `error` 变量被覆盖而消失。
const firstCheckbox = page.locator('.sample-table input[type="checkbox"]').first()
await firstCheckbox.check().catch(() => {})
await page.getByRole('button', { name: '创建发布候选' }).click()
await page.waitForTimeout(1200)
report.intendedUseError = await page.locator('#intended-use-error').innerText().catch(() => '')
report.intendedUseErrorCount = await page.locator('#intended-use-error').count()
report.intendedUseAriaInvalid = await page.locator('#intended-use').getAttribute('aria-invalid').catch(() => null)
report.intendedUseDescribedBy = await page.locator('#intended-use').getAttribute('aria-describedby').catch(() => null)

await page.screenshot({ path: resolve(here, `${KEY}-213-field-errors.png`) })

report.consoleErrors = consoleErrors
writeFileSync(resolve(here, `${stage}-213.json`), `${JSON.stringify(report, null, 2)}\n`)
await browser.close()

const broken = report.accessibleNameIssues + (report.duplicateNames ? 1 : 0)
  + (report.intendedUseError ? 0 : 1)
console.log(JSON.stringify({
  stage,
  checkboxes: report.checkboxes,
  a11yCheckboxes: report.a11ySnapshot,
  a11yCheckboxesAll: report.a11ySnapshotAll,
  accessibleNameIssues: report.accessibleNameIssues,
  duplicateNames: report.duplicateNames,
  emptySubmitHasRangeAnchor: report.emptySubmitHasRangeAnchor,
  intendedUseError: report.intendedUseError,
  intendedUseDescribedBy: report.intendedUseDescribedBy,
  consoleErrors,
}, null, 2))
console.log(`\n[repro-213] ${stage}: 未收口信号 = ${broken} 条`)
process.exit(broken > 0 ? 2 : 0)
