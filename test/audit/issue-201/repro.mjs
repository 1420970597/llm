/**
 * issue #201 复现/验证：批次「已完成」却只产出 1/4 且无恢复入口。
 *
 * 缺陷形态（原文）：`b_2` 计划 4 条，`batch_items` 里只有 1 行成功，状态却是
 * `completed`；`BatchCapabilitiesFor('completed')` 返回全 false，因而界面上
 * **连一个可点的按钮都没有**，用户拿不到任何出口。#190 只修了推导路径
 * （planned > completed 时不再置 completed），没有修**历史终态路径**：
 * 状态一旦已经是 `completed`，缺口永远不会被自我纠正。
 *
 * 本脚本做的是**可控的单变量实验**（这是它存在的理由）：
 *
 *   1. 把 `b_2` 的库内状态手工置回缺陷形态（`completed` + `completed_units=1`）；
 *   2. 等 `studioMaintenanceInterval`（30s）的一个完整维护轮次；
 *   3. 读权威读数（API）+ 界面事实（可点按钮、缺口卡、时间线）。
 *
 * 因此 before/after 的差异**只来自部署的代码**：同一份数据、同一个脚本、
 * 同一视口、同一账号。若维护循环不存在或未收敛，缺陷形态会原样保留。
 *
 * 判定（全部是机器事实）：
 *   * 收敛：status 从 `completed` → `partial_failed`；
 *   * 出口：可点按钮从 0 个 → 至少 1 个（含「补齐缺口」）；
 *   * 可解释：时间线出现带 correctionOf 的 BatchPartialFailed 事件；
 *   * 文案：缺口卡不再把原因归于「覆盖率不足」（#208 的误导形态）。
 *
 * 运行：
 *   node test/audit/issue-201/repro.mjs --phase before|after
 * 产物：
 *   docs/audit/issue-201/<phase>.json
 *   docs/audit/issue-201/<phase>-b2-detail.png
 *   docs/audit/issue-201/<phase>-runs-list.png
 *
 * 防覆盖：已存在的证据默认不覆盖（与 issue-200/205/212/213/217-b1 同一约定）。
 */
import { createRequire } from 'node:module'
import { existsSync, mkdirSync, writeFileSync } from 'node:fs'
import { execFileSync } from 'node:child_process'
import path from 'node:path'
import { fileURLToPath } from 'node:url'

const require = createRequire('/root/llm/package.json')
const { chromium } = require('/root/.pi/agent/npm/node_modules/playwright')

const HERE = path.dirname(fileURLToPath(import.meta.url))
const OUT = path.resolve(HERE, '../../../docs/audit/issue-201')
mkdirSync(OUT, { recursive: true })

const phaseArg = process.argv.indexOf('--phase')
const PHASE = phaseArg >= 0 ? process.argv[phaseArg + 1] : 'before'
const FORCE = process.argv.includes('--force')

if (!FORCE) {
  const existing = [`${PHASE}.json`, `${PHASE}-b2-detail.png`]
    .filter((name) => existsSync(path.join(OUT, name)))
  if (existing.length > 0) {
    console.error(`[issue-201] 拒绝覆盖已存在的证据（${PHASE}）：${existing.join(', ')}`)
    console.error('  若确实要重采，显式传 --force。')
    process.exit(2)
  }
}

const BASE = process.env.BASE_URL ?? 'http://127.0.0.1:3210'
const EMAIL = process.env.ADMIN_EMAIL ?? 'admin@company.com'
const PASSWORD = process.env.ADMIN_PASSWORD ?? 'admin123456'
const PROJECT_ID = process.env.PROJECT_ID ?? '1'
const CONTAINER = process.env.PG_CONTAINER ?? 'llm-postgres-1'
const MAINTENANCE_WAIT_MS = Number(process.env.MAINTENANCE_WAIT_MS ?? 45000)

/** 在真实 Postgres 上把 b_2 置回缺陷形态（b_2 的 batch_items 本就只有 1 行）。 */
function armDefect() {
  const sql = "UPDATE batches SET status='completed', completed_units=1, failed_units=0, "
    + "in_flight_units=0, finished_at=NOW(), updated_at=NOW() WHERE id=2"
  execFileSync('docker', ['exec', CONTAINER, 'psql', '-U', 'llm_factory', '-d', 'llm_factory', '-c', sql], { stdio: 'pipe' })
  const read = execFileSync('docker', ['exec', CONTAINER, 'psql', '-U', 'llm_factory', '-d', 'llm_factory', '-t', '-A', '-F', '|',
    '-c', 'SELECT status, planned_units, completed_units FROM batches WHERE id=2'], { encoding: 'utf8' }).trim()
  const units = execFileSync('docker', ['exec', CONTAINER, 'psql', '-U', 'llm_factory', '-d', 'llm_factory', '-t', '-A',
    '-c', 'SELECT COUNT(*) FROM batch_items WHERE batch_id=2'], { encoding: 'utf8' }).trim()
  return { armedRow: read, batchItemRows: Number(units) }
}

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

const loginOk = page.url().includes('/today')

/** 数时间线里「从终态被纠正」的事件条数（用于测本次运行内的增量）。 */
async function countCorrections() {
  const items = await page.evaluate(async ({ projectId, batchId }) => {
    const response = await fetch(`/api/v1/projects/${projectId}/batches/${batchId}/events?limit=50`, { credentials: 'include' })
    const body = await response.json()
    return (body.items ?? [])
  }, { projectId: PROJECT_ID, batchId: 'b_2' })
  return items.filter((item) => item.eventType === 'BatchPartialFailed'
    && item.detail && item.detail.correctionOf === 'completed').length
}

// ---- 1) 手工置回缺陷形态（同一份数据，before/after 完全一致）----
const armed = armDefect()

// 时间线上有**历史**纠正事件（缺陷当初被修复时写下的），因此不能只查
// 「存不存在 correctionOf 事件」—— 那会让 before 相位也误判为收敛。
// 必须测**本次运行内的增量**：只有新写下的纠正事件才能证明当前部署的代码
// 真的发现了矛盾状态。
const correctionsBefore = await countCorrections()

// ---- 2) 等一个完整维护轮次（30s interval，留足余量）----
await page.waitForTimeout(MAINTENANCE_WAIT_MS)

const correctionsAfter = await countCorrections()
const newCorrectionEvents = correctionsAfter - correctionsBefore

// ---- 3) 读权威读数 + 界面事实 ----
const readBatch = async (batchId) => page.evaluate(async ({ projectId, batchId }) => {
  const response = await fetch(`/api/v1/projects/${projectId}/batches/${batchId}`, { credentials: 'include' })
  const body = await response.json()
  return body.data?.batch ?? null
}, { projectId: PROJECT_ID, batchId })

const events = await page.evaluate(async ({ projectId, batchId }) => {
  const response = await fetch(`/api/v1/projects/${projectId}/batches/${batchId}/events?limit=20`, { credentials: 'include' })
  const body = await response.json()
  return (body.items ?? []).map((item) => ({
    eventType: item.eventType,
    eventTypeLabel: item.eventTypeLabel,
    detail: item.detail ?? null,
  }))
}, { projectId: PROJECT_ID, batchId: 'b_2' })

await page.goto(`${BASE}/p/${PROJECT_ID}/runs/b_2`, { waitUntil: 'networkidle' })
await page.waitForTimeout(2200)

const clickable = await page.evaluate(() => {
  const labels = []
  for (const button of document.querySelectorAll('button')) {
    const rect = button.getBoundingClientRect()
    if (rect.width < 2 || rect.height < 2) continue
    const hit = document.elementFromPoint(rect.x + rect.width / 2, rect.y + rect.height / 2)
    if (hit && (hit === button || button.contains(hit))) labels.push((button.innerText ?? '').trim())
  }
  return labels.filter((label) => label !== '')
})

const detail = await page.evaluate(() => ({
  shortfallCardText: (document.querySelector('[data-batch-shortfall="true"]')?.innerText ?? '').trim(),
  bodyText: document.body.innerText,
}))

const batch = await readBatch('b_2')
// 本**次运行内**新增的纠正事件（而不是 09-30 遗留的历史事件）。
const correctionEvent = newCorrectionEvents > 0
  ? events.find((event) => event.eventType === 'BatchPartialFailed'
    && event.detail && event.detail.correctionOf === 'completed') ?? null
  : null

await page.screenshot({ path: path.join(OUT, `${PHASE}-b2-detail.png`), fullPage: true })
await page.goto(`${BASE}/p/${PROJECT_ID}/runs`, { waitUntil: 'networkidle' })
await page.waitForTimeout(1500)
await page.screenshot({ path: path.join(OUT, `${PHASE}-runs-list.png`), fullPage: true })

const result = {
  issue: 201,
  phase: PHASE,
  base: BASE,
  viewport: '1600x1000',
  account: EMAIL,
  deployedVersion: await page.evaluate(async () => {
    try {
      const r = await fetch('/version.json')
      return (await r.json()).version
    } catch { return null }
  }),
  maintenanceWaitMs: MAINTENANCE_WAIT_MS,
  armed,
  batch: batch && {
    status: batch.status,
    plannedUnits: batch.plannedUnits,
    completedUnits: batch.completedUnits,
    failedUnits: batch.failedUnits,
    inFlightUnits: batch.inFlightUnits,
    shortfallUnits: batch.shortfallUnits,
    shortfallNote: batch.shortfallNote,
    capabilities: batch.capabilities,
  },
  clickableActionButtons: clickable,
  shortfallCardText: detail.shortfallCardText,
  noteBlamesCoverage: /覆盖率不足|方向配额/.test(detail.shortfallCardText),
  showsCompletedDespiteShortfall: batch?.shortfallUnits > 0 && /已完成/.test(detail.bodyText),
  correctionEvent: correctionEvent ?? null,
  correctionsBefore,
  correctionsAfter,
  newCorrectionEvents,
  pageErrors: pageErrors.slice(0, 6),
}

// 判定：收敛 + 有出口 + 时间线可解释。
result.ok = Boolean(
  loginOk &&
  batch &&
  batch.status === 'partial_failed' &&
  batch.shortfallUnits === 3 &&
  clickable.some((label) => label.includes('缺口') || label.includes('恢复') || label.includes('重试')) &&
  newCorrectionEvents > 0,
)

writeFileSync(path.join(OUT, `${PHASE}.json`), `${JSON.stringify(result, null, 2)}\n`)

console.log(`[issue-201] phase=${PHASE} deployed=${result.deployedVersion} armed=${armed.armedRow} units=${armed.batchItemRows}`)
console.log(`  status=${batch?.status} shortfall=${batch?.shortfallUnits} 可点按钮=${JSON.stringify(clickable)}`)
console.log(`  归因覆盖率=${result.noteBlamesCoverage} 本次新增纠正事件=${newCorrectionEvents}（历史 ${correctionsBefore} 条）ok=${result.ok}`)
console.log(`  缺口卡=${JSON.stringify(detail.shortfallCardText.slice(0, 160))}`)

await browser.close()
process.exit(result.ok ? 0 : 1)
