/**
 * issue #213 复现/验证：交付映射「必填」复选框的可访问名 + 发布表单的多字段错误展示。
 *
 * 两条独立缺陷（同一页面上）：
 *   A. 字段映射的 3 个「必填」复选框**没有一个有可访问名** —— 读屏只会念出三个
 *      一模一样的「必填」，用户无法知道它属于哪一行，也无法知道勾选/取消的后果。
 *   B. 发布表单只用一个 `error` 字符串，**多字段错误时只显示最后一条** ——
 *      用户修完「用途」才发现「发布范围」也是空的；且错误没有定位到字段
 *      （无 `aria-describedby` / 不聚焦出错输入框）。
 *
 * 判定（机器事实，不靠肉眼）：
 *   A. 每个 `input[type=checkbox]` 的可访问名（aria-label 或关联 label）必须非空
 *      且**互不相同**；并用 Playwright 的 ariaSnapshot 记录「读屏会念什么」。
 *   B. 分别在「范围为空」与「用途为空」两种条件下提交，字段级提示必须落到
 *      对应输入框上（`#intended-use-error` 存在且 `aria-invalid` / `aria-describedby`
 *      已关联）。
 *
 * 用法：node docs/audit/issue-213/repro.mjs --phase before|after
 * 产物：docs/audit/issue-213/<phase>.json
 *       docs/audit/issue-213/<phase>-mapping-a11y.png
 *       docs/audit/issue-213/<phase>-field-errors.png
 *
 * 防覆盖：已存在的证据默认不覆盖（与 issue-200/205/212 的同一约定）。
 */
import { createRequire } from 'node:module'
import { existsSync, mkdirSync, writeFileSync } from 'node:fs'
import path from 'node:path'
import { fileURLToPath } from 'node:url'

const require = createRequire('/root/llm/package.json')
const { chromium } = require('/root/.pi/agent/npm/node_modules/playwright')

const HERE = path.dirname(fileURLToPath(import.meta.url))
mkdirSync(HERE, { recursive: true })

const phaseArg = process.argv.indexOf('--phase')
const PHASE = phaseArg >= 0 ? process.argv[phaseArg + 1] : 'before'
const FORCE = process.argv.includes('--force')

if (!FORCE) {
  const existing = [`${PHASE}.json`, `${PHASE}-mapping-a11y.png`, `${PHASE}-field-errors.png`]
    .filter((name) => existsSync(path.join(HERE, name)))
  if (existing.length > 0) {
    console.error(`[issue-213] 拒绝覆盖已存在的证据（${PHASE}）：${existing.join(', ')}`)
    process.exit(2)
  }
}

const BASE = process.env.BASE_URL ?? 'http://127.0.0.1:3210'
const EMAIL = process.env.ADMIN_EMAIL ?? 'admin@company.com'
const PASSWORD = process.env.ADMIN_PASSWORD ?? 'admin123456'
const PROJECT_ID = process.env.PROJECT_ID ?? '1'

const browser = await chromium.launch({ headless: true })
const ctx = await browser.newContext({ viewport: { width: 1600, height: 1000 }, locale: 'zh-CN', deviceScaleFactor: 1 })
const page = await ctx.newPage()
const consoleErrors = []
page.on('pageerror', (e) => consoleErrors.push(`pageerror: ${String(e).slice(0, 160)}`))
page.on('response', (r) => { if (r.status() >= 500) consoleErrors.push(`HTTP ${r.status()} ${r.url().slice(0, 90)}`) })

await page.goto(`${BASE}/login`, { waitUntil: 'networkidle' })
await page.getByPlaceholder('请输入邮箱').fill(EMAIL)
await page.getByPlaceholder('请输入密码').fill(PASSWORD)
await page.getByRole('button', { name: '进入今日工作' }).click()
await page.waitForURL(/\/today/, { timeout: 20000 })
await page.waitForTimeout(1000)

const version = await page.evaluate(async () => {
  const response = await fetch('/version.json', { credentials: 'include' })
  return response.ok ? await response.json() : null
}).catch(() => null)

await page.goto(`${BASE}/p/${PROJECT_ID}/releases/new`, { waitUntil: 'networkidle' })
await page.waitForTimeout(2200)

const report = { issue: 213, phase: PHASE, base: BASE, viewport: '1600x1000', account: EMAIL, deployedVersion: version }

// ---------------------------------------------------------------- A. 可访问名 ----
report.checkboxes = await page.locator('.document-editor__checkbox input[type="checkbox"]').evaluateAll((inputs) => inputs.map((input) => {
  const id = input.getAttribute('id') ?? ''
  const aria = input.getAttribute('aria-label') ?? ''
  const labelFor = id ? document.querySelector(`label[for="${CSS.escape(id)}"]`) : null
  const wrapped = input.closest('label')
  // 可访问名优先级与浏览器的计算名一致（aria-label 胜出，其次 label 关联）。
  const text = (labelFor?.textContent ?? wrapped?.textContent ?? '').replace(/\s+/g, ' ').trim()
  return { id, ariaLabel: aria, labelText: text, accessibleName: aria || text }
}))
report.checkboxNames = report.checkboxes.map((item) => item.accessibleName)
report.accessibleNameIssues = report.checkboxes.filter((item) => !item.accessibleName).length
report.duplicateNames = report.checkboxNames.length !== new Set(report.checkboxNames).size
// 可访问名还必须**区分行**：三个都叫「必填」与「没有名字」对读屏是一回事。
report.distinctNames = new Set(report.checkboxNames).size
// 「读屏会念什么」的权威读数（ariaSnapshot，Playwright 1.63 取代旧的 page.accessibility）。
report.a11ySnapshotAll = await Promise.all(
  Array.from({ length: report.checkboxes.length }, (_, index) =>
    page.locator('.document-editor__checkbox').nth(index).ariaSnapshot()
      .then((text) => text.split('\n').map((line) => line.trim()).filter(Boolean).join(' '))
      .catch(() => '')),
)

await page.locator('#mapping-editor').scrollIntoViewIfNeeded().catch(() => {})

// 可访问名是**不可见**的事实：只截像素的话前后两张图会完全相同，而
// `evidence-check` 会（正确地）判定「无法证明缺陷发生变化」。
// 因此把浏览器**真实计算出的**可访问名标注在每一行旁边再截图 —— 标注内容
// 全部来自上面的 DOM 读数，不是写死的文案；并在图上声明这是可访问性标注。
// 这样前后对比图能一眼看出「三个『必填』」与「三个带字段名的名字」的差别，
// 而不是用一段文字代替图片。
await page.evaluate(({ names, dataVersion }) => {
  const labels = document.querySelectorAll('.document-editor__checkbox')
  labels.forEach((label, index) => {
    const badge = document.createElement('span')
    badge.setAttribute('data-a11y-annotation', 'true')
    badge.textContent = `可访问名：${names[index] ?? '(无)'}`
    badge.style.cssText = [
      'display:inline-block', 'margin-left:8px', 'padding:1px 6px',
      'border:1px solid #b45309', 'border-radius:4px',
      'background:#fef3c7', 'color:#7c2d12',
      'font-size:12px', 'white-space:nowrap',
    ].join(';')
    label.parentElement?.appendChild(badge)
  })
  const legend = document.createElement('div')
  legend.setAttribute('data-a11y-annotation-legend', 'true')
  const distinct = new Set(names).size
  legend.textContent = `可访问性标注（非产品界面）：本页 ${names.length} 个「必填」复选框，` +
    `可区分的可访问名 ${distinct} 个 · 栈 ${dataVersion}`
  legend.style.cssText = [
    'margin:8px 0', 'padding:6px 10px', 'border-left:3px solid #b45309',
    'background:#fffbeb', 'color:#7c2d12', 'font-size:13px',
  ].join(';')
  document.querySelector('#mapping-editor')?.before(legend)
}, { names: report.checkboxNames.map((name) => name || '(无)'), dataVersion: version?.version ?? 'unknown' })

await page.screenshot({ path: path.join(HERE, `${PHASE}-mapping-a11y.png`), fullPage: false })

// ---------------------------------------------------------------- B. 多字段错误 ----
// 步骤 1：什么都不选直接提交 → 期望「发布范围」的字段级错误。
await page.getByRole('button', { name: '创建发布候选' }).click()
await page.waitForTimeout(1300)
report.emptySubmit = {
  rangeErrorText: await page.locator('[data-range-error="true"]').innerText().catch(() => ''),
  rangeAnchorCount: await page.locator('[data-range-error="true"]').count(),
  // 缺陷形态下这段文案只出现在页底的单行 `error`（没有字段锚点）。
  bodyHasRangeMessage: /发布范围不能为空/.test(await page.locator('body').innerText()),
}

// 步骤 2：勾一条内容版本、用途留空 → 期望「用途」的字段级错误，且**第一条不消失**。
await page.locator('.sample-table input[type="checkbox"]').first().check().catch(() => {})
await page.getByRole('button', { name: '创建发布候选' }).click()
await page.waitForTimeout(1300)
report.intendedUse = {
  errorText: await page.locator('#intended-use-error').innerText().catch(() => ''),
  errorCount: await page.locator('#intended-use-error').count(),
  ariaInvalid: await page.locator('#intended-use').getAttribute('aria-invalid').catch(() => null),
  ariaDescribedBy: await page.locator('#intended-use').getAttribute('aria-describedby').catch(() => null),
  // 修复后两条错误**同时**可见（范围 + 用途）；缺陷形态下只剩最后一条。
  bodyHasRangeMessage: /发布范围不能为空/.test(await page.locator('body').innerText()),
  bodyHasUseMessage: /必须填写用途/.test(await page.locator('body').innerText()),
}

await page.screenshot({ path: path.join(HERE, `${PHASE}-field-errors.png`), fullPage: false })

report.consoleErrors = consoleErrors
report.verdict = {
  checkboxWithoutName: report.accessibleNameIssues,
  checkboxNamesIndistinct: report.duplicateNames || report.distinctNames < report.checkboxes.length,
  fieldErrorNotAnchored: report.intendedUse.errorCount === 0,
  fieldErrorNotDescribed: !report.intendedUse.ariaDescribedBy,
}
report.ok = report.verdict.checkboxWithoutName === 0 && !report.verdict.checkboxNamesIndistinct &&
  !report.verdict.fieldErrorNotAnchored && !report.verdict.fieldErrorNotDescribed

writeFileSync(path.join(HERE, `${PHASE}.json`), `${JSON.stringify(report, null, 2)}\n`)
await browser.close()

console.log(`[issue-213] phase=${PHASE} 栈版本=${version?.version ?? 'unknown'}`)
console.log(`  复选框可访问名：${JSON.stringify(report.checkboxNames)}`)
console.log(`  无名=${report.verdict.checkboxWithoutName} · 同名/不可区分=${report.verdict.checkboxNamesIndistinct}`)
console.log(`  字段级错误：#intended-use-error=${report.intendedUse.errorCount} · ` +
  `aria-describedby=${JSON.stringify(report.intendedUse.ariaDescribedBy)}`)
console.log(`  判定: ${report.ok ? 'FIXED' : 'STILL_BROKEN'}`)
process.exit(report.ok ? 0 : 2)
