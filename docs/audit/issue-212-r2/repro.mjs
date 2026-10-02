/**
 * issue #212 第 2 轮：**当前 origin/main 是否仍含该缺陷**的活体取证。
 *
 * 关键分辨（本轮必须说清，否则会重复上一轮的错误结论）：
 *   * 修复本体在 PR #241，**未合并**；`origin/main` 上：
 *       - 无 `RefreshBatchSteps` / `ListBatchIDsWithStaleSteps`（0 命中）；
 *       - `UpsertBatchStep` 除定义外**无任何调用点**（批次链路从不写 batch_steps）；
 *       - `RunPages.tsx:760` 仍是「还没有阶段记录。」这句关于事实的断言。
 *   * 活体库里 `batch_steps` 有 8 行，是 **PR #241 分支在被共用的 live DB 上测试**时
 *     由分支的维护循环写入的**残留**，不是「main 已含修复」的证据。
 *     共用可变 DB ≠ 源码事实 —— 源码事实以 `origin/main` 内容为准。
 *
 * 因此本脚本采集两层读数：
 *   A. 部署栈的 `GET /batches/{id}` 实际返回的 steps 行数（说明库里有没有残留行）；
 *   B. 读页面「阶段进度」区块文本，确认它读的是**同一个** batch_steps 数据源。
 *
 * 产物：docs/audit/issue-212-r2/01-current-progress.png + current.json
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
const BATCH = process.env.BATCH_RESOURCE ?? 'b_4'

const browser = await chromium.launch({ headless: true })
const ctx = await browser.newContext({ viewport: { width: 1600, height: 1000 }, locale: 'zh-CN' })
const page = await ctx.newPage()
await page.goto(`${BASE}/login`, { waitUntil: 'networkidle' })
await page.getByPlaceholder('请输入邮箱').fill(process.env.ADMIN_EMAIL ?? 'admin@company.com')
await page.getByPlaceholder('请输入密码').fill(process.env.ADMIN_PASSWORD ?? 'admin123456')
await page.getByRole('button', { name: '进入今日工作' }).click()
await page.waitForURL(/\/today/, { timeout: 20000 })
await page.waitForTimeout(1200)

await page.goto(`${BASE}/p/${PROJECT}/runs/${BATCH}`, { waitUntil: 'networkidle' })
await page.waitForTimeout(1800)

const report = await page.evaluate(async ({ batch, projectId }) => {
  const resp = await fetch(`/api/v1/projects/${projectId}/batches/${batch}`, { credentials: 'include' })
  const body = resp.ok ? await resp.json() : null
  const steps = body?.steps ?? body?.data?.steps ?? null
  const cards = Array.from(document.querySelectorAll('.console-card'))
  const progressCard = cards.find((c) => /阶段进度/.test(c.textContent ?? ''))
  return {
    apiStatus: resp.status,
    apiStepCount: Array.isArray(steps) ? steps.length : null,
    progressCardText: (progressCard?.innerText ?? '').replace(/\s+/g, ' ').trim().slice(0, 200),
  }
}, { batch: BATCH, projectId: PROJECT })

report.deployedVersion = await page.evaluate(async () => (await fetch('/version.json')).json())
report.sourceFactOnMain = {
  fixSymbolsOnMain: 0,
  upsertCallSiteOnMain: 'none (only definition)',
  emptyStateAssertion: 'apps/web-user/src/studio/pages/RunPages.tsx:760 "还没有阶段记录。"',
}
writeFileSync(path.join(HERE, 'current.json'), JSON.stringify(report, null, 2))
await page.screenshot({ path: path.join(HERE, '01-current-progress.png'), fullPage: true })
console.log(JSON.stringify(report, null, 2))
await browser.close()
