/**
 * issue #197 第 3 轮：**当前 origin/main 事实取证**（真实栈 + 真实 Chromium）。
 *
 * 本轮不提交任何修复，只把「哪些子项在 main 上仍未收口」变成可核对的读数：
 *   - 第 6 条（变更理由必填 / 乐观锁）：未获批的产品决策；
 *   - 第 11 条（拖拽式流程画布 + 参数 schema 先行）：未获批的产品定位级改造；
 *   - **第 11 条 §A（m×n×z 公式算术）**与**第 13 条（长度口径）**：修复本体分别在
 *     PR #245 / #247，二者**均未合并**，因此这两个缺陷在 main 上**依然存在**。
 *     这一点必须由本轮自己复核，不能因为上一轮评论写了「已修复」就采信。
 *
 * 运行：node docs/audit/issue-197-r3/repro.mjs
 * 产物：01-change-reason.png / 02-canvas-drag.png / 03-coverage-formula.png / 04-length-scope.png
 *       blockers.json
 */
import { createRequire } from 'node:module'
import { mkdirSync, writeFileSync } from 'node:fs'
import path from 'node:path'
import { fileURLToPath } from 'node:url'

const require = createRequire('/root/llm/package.json')
const { chromium } = require('/root/.pi/agent/npm/node_modules/playwright')

const HERE = path.dirname(fileURLToPath(import.meta.url))
mkdirSync(HERE, { recursive: true })
const BASE = process.env.BASE_URL ?? 'http://127.0.0.1:3210'
const PROJECT = process.env.PROJECT_ID ?? '1'

const browser = await chromium.launch({ headless: true })
const ctx = await browser.newContext({ viewport: { width: 1600, height: 1000 }, locale: 'zh-CN' })
const page = await ctx.newPage()
await page.goto(`${BASE}/login`, { waitUntil: 'networkidle' })
await page.getByPlaceholder('请输入邮箱').fill(process.env.ADMIN_EMAIL ?? 'admin@company.com')
await page.getByPlaceholder('请输入密码').fill(process.env.ADMIN_PASSWORD ?? 'admin123456')
await page.getByRole('button', { name: '进入今日工作' }).click()
await page.waitForURL(/\/today/, { timeout: 20000 })
await page.waitForTimeout(1200)

const report = {}
report.deployedVersion = await page.evaluate(async () => (await fetch('/version.json')).json())

// ---- 第 6 条：变更理由必填（只读快照） ----
await page.goto(`${BASE}/p/${PROJECT}/blueprint?node=standard`, { waitUntil: 'networkidle' })
await page.waitForTimeout(1500)
report.item6 = await page.evaluate(() => {
  const reason = document.querySelector('[data-field="blueprint-change-reason"]')
  const labels = Array.from(document.querySelectorAll('*'))
    .filter((el) => el.children.length === 0 && /变更理由/.test(el.textContent ?? ''))
    .map((el) => el.textContent.trim())
  const saveBtn = Array.from(document.querySelectorAll('button')).find((b) => /保存为新版本/.test(b.textContent ?? ''))
  return {
    changeReasonFieldPresent: Boolean(reason),
    changeReasonLabel: labels[0] ?? null,
    requiredInLabel: labels.some((t) => /必填/.test(t)),
    saveButtonText: saveBtn?.textContent.trim() ?? null,
  }
})
await page.screenshot({ path: path.join(HERE, '01-change-reason.png'), fullPage: true })

// ---- 第 11 条：画布仍不可拖拽 ----
await page.goto(`${BASE}/p/${PROJECT}/blueprint`, { waitUntil: 'networkidle' })
await page.waitForTimeout(1500)
report.item11 = await page.evaluate(() => {
  const nodes = document.querySelector('.blueprint-nodes')
  const buttons = Array.from(document.querySelectorAll('.blueprint-nodes button, .blueprint-node'))
  return {
    canvasFlexDirection: nodes ? getComputedStyle(nodes).flexDirection : null,
    nodeCount: buttons.length,
    draggableCount: buttons.filter((b) => b.getAttribute('draggable') === 'true').length,
    hasConnectorSvg: Boolean(document.querySelector('.blueprint-nodes svg')),
  }
})
await page.screenshot({ path: path.join(HERE, '02-canvas-drag.png'), fullPage: true })

// ---- 第 11 条 §A：覆盖公式在 main 上是否仍不自洽（PR #245 未合并） ----
await page.goto(`${BASE}/p/${PROJECT}/coverage`, { waitUntil: 'networkidle' })
await page.waitForTimeout(1500)
report.item11a = await page.evaluate(() => {
  const formula = Array.from(document.querySelectorAll('*'))
    .filter((el) => el.children.length === 0 && /^\s*m \d+ × n \d+ × z \d+ = \d+\s*$/.test(el.textContent ?? ''))
    .map((el) => el.textContent.trim())[0] ?? null
  const nums = (formula ?? '').match(/\d+/g)?.map(Number) ?? []
  // 公式形如 m×n×z = 结果：算术是否成立
  const arithmeticOk = nums.length === 4 ? nums[0] * nums[1] * nums[2] === nums[3] : null
  return { formula, arithmeticOk }
})
await page.screenshot({ path: path.join(HERE, '03-coverage-formula.png'), fullPage: true })

// ---- 第 13 条：长度口径在 main 上是否仍不可见（PR #247 未合并） ----
await page.goto(`${BASE}/p/${PROJECT}/runs/b_3`, { waitUntil: 'networkidle' })
await page.waitForTimeout(1800)
report.item13 = await page.evaluate(() => {
  const scope = document.querySelector('[data-analysis-length-scope]')
  const analysis = document.querySelector('[data-batch-analysis="true"]')
  return {
    hasLengthScopeElement: Boolean(scope),
    lengthScopeText: scope?.textContent?.trim() ?? null,
    analysisSnippet: (analysis?.innerText ?? '').replace(/\s+/g, ' ').slice(0, 220),
  }
})
await page.screenshot({ path: path.join(HERE, '04-length-scope.png'), fullPage: true })

writeFileSync(path.join(HERE, 'blockers.json'), JSON.stringify(report, null, 2))
console.log(JSON.stringify(report, null, 2))
await browser.close()
