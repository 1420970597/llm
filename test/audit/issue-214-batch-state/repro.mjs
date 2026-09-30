/**
 * #214 子项 #201/#202/#208 复现与修复后取证（真实栈 + 真实 Chromium）。
 *
 * 为什么一个脚本同时覆盖三条：它们的**根因是同一个** ——
 * `internal/store/batch_store.go` 的批次状态聚合把「已在库里的状态」当成不可变事实。
 *   #201：`completed` 且 planned > completed（b_2 计划 4 只产出 1）永不纠正；
 *   #202：`running` 且所有单元已定稿、无在途作业（b_1 12/12 失败）成为僵尸；
 *   #208：缺口文案写死「覆盖率不足」，与真实原因（config_error 缺模型连接）矛盾。
 * 分开写三个脚本会让「只修一处」看起来像修好了整个缺陷类 —— 那正是 #191 的教训。
 *
 * 判据全部是**可核对的读数**（状态字段、缺口文案、可点按钮数、button 命中），
 * 不是「看起来修好了」。
 *
 * 运行：
 *   node test/audit/issue-214-batch-state/repro.mjs [--phase before|after]
 * 产物：
 *   docs/audit/issue-214/<phase>-batch-state.json
 *   docs/audit/issue-214/<phase>-b1-zombie.png
 *   docs/audit/issue-214/<phase>-b2-shortfall.png
 */
import { mkdirSync, writeFileSync } from 'node:fs'
import { createRequire } from 'node:module'
import path from 'node:path'
import { fileURLToPath } from 'node:url'

const require = createRequire('/root/llm/package.json')
const { chromium } = require('/root/.pi/agent/npm/node_modules/playwright')

const HERE = path.dirname(fileURLToPath(import.meta.url))
const OUT = path.resolve(HERE, '../../../docs/audit/issue-214')
mkdirSync(OUT, { recursive: true })

const phaseArg = process.argv.indexOf('--phase')
const PHASE = phaseArg >= 0 ? process.argv[phaseArg + 1] : 'before'
const BASE = process.env.BASE_URL ?? 'http://127.0.0.1:3210'
const EMAIL = process.env.ADMIN_EMAIL ?? 'admin@company.com'
const PASSWORD = process.env.ADMIN_PASSWORD ?? 'admin123456'
const PROJECT_ID = process.env.PROJECT_ID ?? '1'

const browser = await chromium.launch({ headless: true })
const ctx = await browser.newContext({
  viewport: { width: 1600, height: 1000 },
  locale: 'zh-CN',
  deviceScaleFactor: 1,
})
const page = await ctx.newPage()

await page.goto(`${BASE}/login`, { waitUntil: 'networkidle' })
await page.getByPlaceholder('请输入邮箱').fill(EMAIL)
await page.getByPlaceholder('请输入密码').fill(PASSWORD)
await page.getByRole('button', { name: '进入今日工作' }).click()
await page.waitForURL(/\/today/, { timeout: 20000 })
await page.waitForTimeout(1200)

const findings = {}

/** 从真实的批次详情 API 读权威读数（不靠界面文案反推）。 */
async function readBatch(batchId) {
  return await page.evaluate(async ({ projectId, batchId }) => {
    const response = await fetch(`/api/v1/projects/${projectId}/batches/${batchId}`, {
      credentials: 'include',
    })
    const body = await response.json()
    return body.data?.batch ?? null
  }, { projectId: PROJECT_ID, batchId })
}

/** 从浏览器上下文里做真实命中测试：返回可点按钮的可见文本。 */
async function clickableButtons() {
  return await page.evaluate(() => {
    const labels = []
    for (const button of document.querySelectorAll('button')) {
      const rect = button.getBoundingClientRect()
      if (rect.width < 2 || rect.height < 2) continue
      const hit = document.elementFromPoint(rect.x + rect.width / 2, rect.y + rect.height / 2)
      if (hit && (hit === button || button.contains(hit))) {
        labels.push((button.innerText ?? '').trim())
      }
    }
    return labels.filter((label) => label !== '')
  })
}

for (const [batchId, key] of [['b_1', 'zombie'], ['b_2', 'shortfall']]) {
  await page.goto(`${BASE}/p/${PROJECT_ID}/runs/${batchId}`, { waitUntil: 'networkidle' })
  await page.waitForTimeout(1800)

  const api = await readBatch(batchId)
  const buttons = await clickableButtons()
  const visible = await page.evaluate(() => document.body.innerText)
  const shortfallCard = await page.evaluate(() => {
    const node = document.querySelector('[data-batch-shortfall="true"]')
    return node ? node.innerText.trim() : null
  })

  findings[key] = {
    route: `/p/${PROJECT_ID}/runs/${batchId}`,
    api: api && {
      status: api.status,
      controlState: api.controlState,
      plannedUnits: api.plannedUnits,
      completedUnits: api.completedUnits,
      failedUnits: api.failedUnits,
      inFlightUnits: api.inFlightUnits,
      shortfallUnits: api.shortfallUnits,
      shortfallNote: api.shortfallNote,
      capabilities: api.capabilities,
    },
    capabilities: api?.capabilities ?? null,
    clickableActionButtons: buttons,
    shortfallCardText: shortfallCard,
    // 缺口文案是否断言了覆盖率/配额（#208 的误导形态）。
    noteBlamesCoverage: /覆盖率不足|方向配额|素材接地/.test(shortfallCard ?? ''),
    // 状态列文案（列表页同口径）：是否在缺缺口的情况下出现「已完成」字样。
    showsCompletedDespiteShortfall:
      (api?.shortfallUnits ?? 0) > 0 && /已完成/.test(visible),
  }

  await page.screenshot({ path: path.join(OUT, `${PHASE}-b${batchId === 'b_1' ? '1' : '2'}-${key}.png`), fullPage: true })
}

// 失败单元的真实原因（#208 的另一半：缺口文案必须与它一致）。
findings.failureTruth = await page.evaluate(async ({ projectId, batchId }) => {
  const response = await fetch(`/api/v1/projects/${projectId}/batches/${batchId}/failures?limit=3`, {
    credentials: 'include',
  })
  const body = await response.json()
  return (body.items ?? []).map((item) => ({
    errorClass: item.errorClass,
    errorClassLabel: item.errorClassLabel,
    suggestedAction: item.suggestedAction,
    retryable: item.retryable,
  }))
}, { projectId: PROJECT_ID, batchId: 'b_1' })

writeFileSync(path.join(OUT, `${PHASE}-batch-state.json`), `${JSON.stringify(findings, null, 2)}\n`)
await browser.close()

console.log(`[repro] phase=${PHASE}`)
for (const [key, value] of Object.entries(findings)) {
  if (key === 'failureTruth') continue
  console.log(`  ${key}: status=${value.api?.status} shortfall=${value.api?.shortfallUnits} 可点按钮=${JSON.stringify(value.clickableActionButtons)} 归因覆盖率=${value.noteBlamesCoverage}`)
}
console.log(`  失败原因样本=${JSON.stringify(findings.failureTruth?.[0] ?? null)}`)
