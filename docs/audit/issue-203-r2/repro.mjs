/**
 * issue #203 第 2 轮：**当前 origin/main 仍存在该缺陷**的活体取证（真实栈 + 真实 Chromium）。
 *
 * 本轮不新增修复：修复本体已在 PR #234 上完成并取证，但 PR **未合并**。
 * 按更正的关闭规则第 ④ 条「修复已进入 origin/main」，本 issue 必须保持开启。
 * 因此本脚本的职责是证明「部署中的 main 仍然把全量冻结范围称作『已接纳』」。
 *
 * 产物：docs/audit/issue-203-r2/01-main-buggy-title.png + main-defect.json
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

// 冻结：直接走数据页按钮，观察请求体里有没有范围意图
const freezeBody = await page.evaluate(async (projectId) => {
  const response = await fetch(`/api/v1/projects/${projectId}/selection-snapshots`, {
    method: 'POST', credentials: 'include',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ purpose: 'release', fromFilter: {} }),
  })
  return { status: response.status, body: response.ok ? await response.json() : null }
}, PROJECT)

// 服务端解析出的范围构成（有没有 pending）
const samples = await page.evaluate(async (projectId) => {
  const [pending, accepted] = await Promise.all([
    fetch(`/api/v1/projects/${projectId}/samples?status=pending`, { credentials: 'include' }),
    fetch(`/api/v1/projects/${projectId}/samples?status=accepted`, { credentials: 'include' }),
  ])
  const count = async (r) => (r.ok ? ((await r.json()).items?.length ?? 0) : null)
  return { pending: await count(pending), accepted: await count(accepted) }
}, PROJECT)

// 候选页标题（写死的「已接纳」）
const snapshotId = freezeBody.body?.id ?? freezeBody.body?.data?.id ?? ''
await page.goto(`${BASE}/p/${PROJECT}/releases/new?selection=${snapshotId}`, { waitUntil: 'networkidle' })
await page.waitForTimeout(1800)
const title = await page.evaluate(() =>
  Array.from(document.querySelectorAll('*'))
    .filter((el) => el.children.length === 0 && /发布范围/.test(el.textContent ?? ''))
    .map((el) => el.textContent.trim())[0] ?? null)

const report = {
  deployedVersion: await page.evaluate(async () => (await fetch('/version.json')).json()),
  originMainContentProof: 'git show origin/main:apps/web-user/src/studio/pages/ReleasePages.tsx | sed -n 616p',
  freezeRequest: { status: freezeBody.status, fromFilter: { purpose: 'release', fromFilter: {} } },
  resolvedScopeComposition: samples,
  candidatePageRangeTitle: title,
  // 缺陷判据：范围含未审阅内容，标题却整体称「已接纳」
  defectPresent: Boolean(title && /已接纳/.test(title) && (samples.pending ?? 0) > 0 && !/快照/.test(title)),
}
writeFileSync(path.join(HERE, 'main-defect.json'), JSON.stringify(report, null, 2))
await page.screenshot({ path: path.join(HERE, '01-main-buggy-title.png'), fullPage: true })
console.log(JSON.stringify(report, null, 2))
await browser.close()
