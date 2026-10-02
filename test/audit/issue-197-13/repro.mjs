/**
 * issue #197 第 13 条残余子项复现/验证：数据集分析「长度」口径不可见
 * （真实栈 + 真实 Chromium）。
 *
 * 缺陷形态（上一轮真机复核时记为「已知限制」但未收口）：
 *
 *   生产批次详情页的「数据集结构与内容分析」里，「长度中位 / P90」是
 *   `question + reasoning + answer (+teacherPrompt)` 的**合并字符数**：
 *
 *   1. 服务端 `fieldCount` **硬编码为 1**（`internal/studio/dataset_analysis.go`），
 *      而 SFT 实际参与统计的是 3 个字段 —— 这个数字从不反映事实；
 *   2. 卡片里的 hint 只写「最短 / 最长 / 均值（字符数）」，
 *      **没有任何口径说明**能告诉用户这是三字段合计；
 *   3. `notes`（口径说明）列了分位法、重复率、接地率，**唯独漏了长度口径**。
 *
 * 后果与 issue 第 13 条的诉求相反：「分析数据集的长度…使用户清晰可见」——
 * 数字可见但口径不可见，用户看到「长度中位 1082」会当成单条内容（例如只看问题）
 * 的长度，而它是问题+推理+答案之和。读得懂数字、读不懂含义，与 #191 的
 * 「内部键泄漏」是同一类失败：把存储表示当成了用户概念。
 *
 * 判据全部是**可核对的读数**（接口字段、卡片文本），不是「看起来修好了」。
 *
 * 运行：
 *   node test/audit/issue-197-13/repro.mjs --phase before|after
 * 产物：
 *   docs/audit/issue-197-13/<phase>.json
 *   docs/audit/issue-197-13/<phase>-analysis.png
 *
 * 防覆盖：已存在的证据默认不覆盖（与 issue-200 / 205 / 212 / 213 / 217-b1 同一约定），
 * 要重采必须显式传 --force。重采「before」在已修复的栈上会得到修复后的读数。
 */
import { createRequire } from 'node:module'
import { existsSync, mkdirSync, writeFileSync } from 'node:fs'
import path from 'node:path'
import { fileURLToPath } from 'node:url'

const require = createRequire('/root/llm/package.json')
const { chromium } = require('/root/.pi/agent/npm/node_modules/playwright')

const HERE = path.dirname(fileURLToPath(import.meta.url))
const OUT = path.resolve(HERE, '../../../docs/audit/issue-197-13')
mkdirSync(OUT, { recursive: true })

const phaseArg = process.argv.indexOf('--phase')
const PHASE = phaseArg >= 0 ? process.argv[phaseArg + 1] : 'before'
const FORCE = process.argv.includes('--force')

if (!FORCE) {
  const existing = [`${PHASE}.json`, `${PHASE}-analysis.png`]
    .filter((name) => existsSync(path.join(OUT, name)))
  if (existing.length > 0) {
    console.error(`[issue-197-13] 拒绝覆盖已存在的证据（${PHASE}）：${existing.join(', ')}`)
    console.error('  若确实要重采，显式传 --force（注意：在已修复的栈上采 before 会得到修复后的读数）。')
    process.exit(2)
  }
}

const BASE = process.env.BASE_URL ?? 'http://127.0.0.1:3210'
const EMAIL = process.env.ADMIN_EMAIL ?? 'admin@company.com'
const PASSWORD = process.env.ADMIN_PASSWORD ?? 'admin123456'
const PROJECT_ID = process.env.PROJECT_ID ?? '1'
// b_3 是唯一完整产出 4/4 的批次，因此它是「有内容可分析」的那一条。
const BATCH_ID = process.env.BATCH_ID ?? 'b_3'

const browser = await chromium.launch({ headless: true })
const ctx = await browser.newContext({ viewport: { width: 1600, height: 1000 }, locale: 'zh-CN', deviceScaleFactor: 1 })
const page = await ctx.newPage()

const pageErrors = []
page.on('pageerror', (e) => pageErrors.push(String(e).slice(0, 160)))

await page.goto(`${BASE}/login`, { waitUntil: 'networkidle' })
await page.getByPlaceholder('请输入邮箱').fill(EMAIL)
await page.getByPlaceholder('请输入密码').fill(PASSWORD)
await page.getByRole('button', { name: '进入今日工作' }).click()
await page.waitForURL(/\/today/, { timeout: 20000 })
await page.waitForTimeout(1200)

// ---- 权威读数：直接从真实端点读，不靠界面文案反推 ----
const analysis = await page.evaluate(async ({ projectId, batchId }) => {
  const response = await fetch(`/api/v1/projects/${projectId}/batches/${batchId}/analysis`, { credentials: 'include' })
  const body = await response.json()
  return { status: response.status, data: body.data ?? null }
}, { projectId: PROJECT_ID, batchId: BATCH_ID })

// ---- 界面读数：真实跳转到批次详情并读「数据集结构与内容分析」卡片 ----
await page.goto(`${BASE}/p/${PROJECT_ID}/runs/${BATCH_ID}`, { waitUntil: 'networkidle' })
await page.waitForTimeout(2500)

const tile = page.locator('[data-stat-tile*="长度"]').first()
const ui = {
  route: `/p/${PROJECT_ID}/runs/${BATCH_ID}`,
  analysisCardPresent: await page.locator('[data-batch-analysis="true"]').count(),
  lengthTileLabel: (await tile.innerText().catch(() => '')).replace(/\n+/g, ' | ').trim(),
  notesText: '',
  // 口径是否在界面上可见：hint 或口径说明里出现「字段」类表述才算。
  lengthScopeDisclosedOnUi: false,
}
ui.notesText = (await page.locator('[data-batch-analysis="true"]').innerText().catch(() => ''))
  .split('\n').filter((line) => line.includes('口径说明')).join(' | ')

const length = analysis.data?.length ?? null
const notes = analysis.data?.notes ?? []
const combined = [
  ui.lengthTileLabel,
  ...notes,
].join(' ')
ui.lengthScopeDisclosedOnUi = /字段|合计|之和/.test(combined)

// 字段数这一事实：SFT 的 payload 参与长度统计的字段（question/reasoning/answer）。
const fieldCountClaimed = length?.fieldCount ?? null
const fieldBreakdownPresent = Array.isArray(length?.fields) && length.fields.length > 0

const result = {
  issue: 197,
  subitem: '13-length-scope',
  phase: PHASE,
  base: BASE,
  viewport: '1600x1000',
  account: EMAIL,
  batch: BATCH_ID,
  httpStatus: analysis.status,
  length,
  notes,
  ui,
  fieldCountClaimed,
  fieldBreakdownPresent,
  // 判定：长度口径在 API 与界面双双可见，且字段数不是硬编码的 1。
  ok: Boolean(
    length &&
    Array.isArray(length.fields) &&
    length.fields.length >= 3 &&
    fieldCountClaimed === length.fields.length &&
    ui.lengthScopeDisclosedOnUi,
  ),
  pageErrors: pageErrors.slice(0, 6),
}

await page.locator('[data-batch-analysis="true"]').first().screenshot({
  path: path.join(OUT, `${PHASE}-analysis.png`),
})
writeFileSync(path.join(OUT, `${PHASE}.json`), `${JSON.stringify(result, null, 2)}\n`)

console.log(`[issue-197-13] phase=${PHASE}`)
console.log(`  长度读数=${JSON.stringify(length)}`)
console.log(`  fieldCount 声称=${fieldCountClaimed} · 字段明细=${fieldBreakdownPresent ? `${length.fields.length} 项` : '缺失'}`)
console.log(`  界面口径可见=${ui.lengthScopeDisclosedOnUi} · ok=${result.ok}`)
console.log(`  界面读到的长度磁贴=${ui.lengthTileLabel}`)

await browser.close()
process.exit(result.ok ? 0 : 1)
