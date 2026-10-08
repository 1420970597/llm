/**
 * Issue #211 复现取证：「检查范围」的未审阅内容提示（方向 2）。
 *
 * 覆盖两个可观察行为：
 *   1. **行内标记**：勾选一条 `pending` 内容版本后，该行是否出现
 *      「未审阅：结论不作为发布证据」；
 *   2. **提交前提示**：点「创建并冻结实验」时是否出现知情确认弹窗
 *      （而不是直接提交）。
 *
 * 为什么必须拦掉提交请求：本脚本要比较「点提交之后发生什么」，
 * 而 `before` 版本的**真实后果**就是直接创建实验（会真的排队跑 LLM、消耗额度）。
 * 因此用 `page.route` 把 `POST .../experiments` abort 掉 ——
 * 这样两边都**不产生副作用**，但仍能观察到「有没有弹确认框」这一差异。
 *
 * 用法：
 *   node docs/audit/issue-211/repro-scope-notice.mjs                # 产出 01-before-*
 *   STAGE=after node docs/audit/issue-211/repro-scope-notice.mjs   # 产出 02-after-*
 */
import { mkdirSync, writeFileSync } from 'node:fs'
import { createRequire } from 'node:module'
import path from 'node:path'
import { fileURLToPath } from 'node:url'

const require = createRequire('/root/llm/package.json')
const { chromium } = require('/root/.pi/agent/npm/node_modules/playwright')

const HERE = path.dirname(fileURLToPath(import.meta.url))
const STAGE = process.env.STAGE === 'after' ? 'after' : 'before'
const PREFIX = STAGE === 'after' ? '02-after' : '01-before'
const BASE = process.env.BASE_URL ?? 'http://127.0.0.1:3210'
const PROJECT = process.env.PROJECT_ID ?? '1'
const VIEWPORT = { width: 1600, height: 1000 }

mkdirSync(HERE, { recursive: true })

const browser = await chromium.launch({ headless: true })
const ctx = await browser.newContext({ viewport: VIEWPORT, locale: 'zh-CN', deviceScaleFactor: 1 })
const page = await ctx.newPage()

const consoleErrors = []
page.on('console', (m) => { if (m.type() === 'error') consoleErrors.push(m.text().slice(0, 200)) })
page.on('pageerror', (e) => consoleErrors.push(String(e).slice(0, 200)))

// 阻断真实创建，避免脚本产生副作用（排队跑 LLM / 消耗额度）。
const blockedRequests = []
await page.route(`**/api/v1/projects/${PROJECT}/experiments`, async (route) => {
  blockedRequests.push({ method: route.request().method(), url: route.request().url() })
  await route.abort()
})

await page.goto(`${BASE}/login`, { waitUntil: 'networkidle' })
await page.getByPlaceholder('请输入邮箱').fill('admin@company.com')
await page.getByPlaceholder('请输入密码').fill('admin123456')
await page.getByRole('button', { name: '进入今日工作' }).click()
await page.waitForURL(/\/today/, { timeout: 20000 })
await page.waitForTimeout(1200)

await page.goto(`${BASE}/p/${PROJECT}/quality/new`, { waitUntil: 'networkidle' })
await page.waitForTimeout(2500)

const evidence = { stage: STAGE, base: BASE, project: PROJECT, viewport: VIEWPORT }

// ---- 1. 页顶说明（#211 方向 1） ----
evidence.pageLevelNotice = await page.evaluate(() =>
  document.querySelector('[data-scope-unreviewed-notice]')?.innerText?.trim() ?? null,
)

// ---- 2. 勾选一条 pending 内容版本，读行内标记 ----
const rows = page.locator('.sample-row:not(.sample-row--head)')
const rowCount = await rows.count()
let selectedPendingRow = null
for (let index = 0; index < rowCount; index += 1) {
  const row = rows.nth(index)
  const status = (await row.locator('.semi-tag').first().innerText().catch(() => '')).trim()
  if (!status.includes('待判断')) continue
  const checkbox = row.locator('input[type="checkbox"]')
  if (!(await checkbox.isEnabled().catch(() => false))) continue
  await checkbox.check({ force: true })
  await page.waitForTimeout(400)
  selectedPendingRow = {
    index,
    statusText: status,
    rowText: (await row.innerText().catch(() => '')).replace(/\s+/g, ' ').trim().slice(0, 140),
    inlineUnreviewedTag: await row.locator('[data-scope-unreviewed-row]').count(),
  }
  break
}
evidence.selectedPendingRow = selectedPendingRow ?? { error: '没有可勾选的 pending 内容版本（证据不可用）' }
evidence.selectedCountText = await page.evaluate(() => {
  const picker = document.querySelector('[data-scope-picker]')
  return (picker?.innerText ?? '').split('\n').find((line) => line.includes('已选'))?.trim() ?? null
})
await page.screenshot({ path: path.join(HERE, `${PREFIX}-scope-selected.png`), fullPage: true })

// ---- 3. 选一名裁判，点提交，观察是否出现知情确认 ----
// 注意：Semi 的 Select 会把 data-* 属性渲染到 `.semi-select` **根元素本身**
// （而不是包一层 div），因此不能写成 `[attr] .semi-select`。
const judge = page.locator('.semi-select[data-judge-connection-select]')
if ((await judge.count()) > 0) {
  await judge.first().click()
  await page.waitForTimeout(800)
  const options = page.locator('.semi-select-option')
  const optionCount = await options.count()
  if (optionCount > 0) {
    // 取第一个**未被禁用**的选项（已停用的连接选了也跑不起来）。
    let picked = false
    for (let index = 0; index < optionCount; index += 1) {
      const candidate = options.nth(index)
      const className = (await candidate.getAttribute('class')) ?? ''
      if (className.includes('disabled')) continue
      await candidate.click()
      picked = true
      break
    }
    if (!picked) await page.keyboard.press('Escape')
    await page.waitForTimeout(500)
  } else {
    await page.keyboard.press('Escape')
  }
}

const submitButton = page.getByRole('button', { name: '创建并冻结实验' })
evidence.submit = { clicked: false, confirmModalAppeared: false, confirmText: null }
if ((await submitButton.count()) > 0 && (await submitButton.isEnabled().catch(() => false))) {
  await submitButton.click()
  await page.waitForTimeout(1200)
  evidence.submit.clicked = true
  evidence.submit.confirmModalAppeared = (await page.locator('.semi-modal-content').count()) > 0
  evidence.submit.confirmText = await page.evaluate(() =>
    document.querySelector('.semi-modal-content')?.innerText?.replace(/\s+/g, ' ').trim().slice(0, 400) ?? null,
  )
  evidence.submit.blockedRequests = blockedRequests
  evidence.submit.postAttemptedWithoutConfirm = blockedRequests.length > 0
  await page.screenshot({ path: path.join(HERE, `${PREFIX}-submit.png`), fullPage: true })
} else {
  evidence.submit.error = '提交按钮不可用（无法验证方向 2 的提交前提示）'
}

evidence.consoleErrors = consoleErrors
writeFileSync(path.join(HERE, `${PREFIX}.json`), JSON.stringify(evidence, null, 2) + '\n')
console.log(JSON.stringify(evidence, null, 2))
await browser.close()
