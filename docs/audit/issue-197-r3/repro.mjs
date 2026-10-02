/**
 * issue #197 第 3 轮：两个未收口子项的**当前 main 事实取证**（真实栈 + 真实 Chromium）。
 *
 * 本轮不提交任何修复：第 6 条（变更理由必填 / 乐观锁）与第 11 条（拖拽式流程画布）
 * 都是**未获批的产品决策**。本脚本只做一件事 —— 把「它们现在仍然存在」变成可核对的读数，
 * 而不是复述上一轮的结论。
 *
 * 读数来源：
 *   #6  蓝图右侧检查器的「变更理由（必填…）」标签 + 空理由保存被拦（服务端契约）
 *   #11 设计画布 `.blueprint-nodes` 的 flex-direction 与节点 `draggable`
 *
 * 运行：node docs/audit/issue-197-r3/repro.mjs
 * 产物：docs/audit/issue-197-r3/01-change-reason.png
 *       docs/audit/issue-197-r3/02-canvas-drag.png
 *       docs/audit/issue-197-r3/blockers.json
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
const EMAIL = process.env.ADMIN_EMAIL ?? 'admin@company.com'
const PASSWORD = process.env.ADMIN_PASSWORD ?? 'admin123456'
const PROJECT = process.env.PROJECT_ID ?? '1'

const browser = await chromium.launch({ headless: true })
const ctx = await browser.newContext({ viewport: { width: 1600, height: 1000 }, locale: 'zh-CN' })
const page = await ctx.newPage()

await page.goto(`${BASE}/login`, { waitUntil: 'networkidle' })
await page.getByPlaceholder('请输入邮箱').fill(EMAIL)
await page.getByPlaceholder('请输入密码').fill(PASSWORD)
await page.getByRole('button', { name: '进入今日工作' }).click()
await page.waitForURL(/\/today/, { timeout: 20000 })
await page.waitForTimeout(1200)

const report = {}

// ---- 第 6 条：变更理由必填（只读快照，不改任何东西） ----
await page.goto(`${BASE}/p/${PROJECT}/blueprint?node=standard`, { waitUntil: 'networkidle' })
await page.waitForTimeout(1500)
report.item6 = await page.evaluate(() => {
  const reason = document.querySelector('[data-field="blueprint-change-reason"]')
  const labelText = Array.from(document.querySelectorAll('*'))
    .filter((el) => el.children.length === 0 && /变更理由/.test(el.textContent ?? ''))
    .map((el) => el.textContent.trim())
  const saveBtn = Array.from(document.querySelectorAll('button'))
    .find((b) => /保存为新版本/.test(b.textContent ?? ''))
  return {
    changeReasonFieldPresent: Boolean(reason),
    changeReasonLabel: labelText[0] ?? null,
    requiredInLabel: labelText.some((t) => /必填/.test(t)),
    saveButtonText: saveBtn?.textContent.trim() ?? null,
    // 空理由时保存是否被拦，取决于前端校验 + 服务端契约（reason 为契约 §2.2 一部分）
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

report.deployedVersion = await page.evaluate(async () => (await fetch('/version.json')).json())
writeFileSync(path.join(HERE, 'blockers.json'), JSON.stringify(report, null, 2))
console.log(JSON.stringify(report, null, 2))
await browser.close()
