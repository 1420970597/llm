/**
 * issue #202 复现/验证：批次「运行中」但全部单元失败且无在途作业 —— 僵尸批次。
 *
 * 缺陷形态（原文）：`b_1` 计划 12、`completed=0`、`failed=12`（全部 `config_error`、
 * `retryable=false`）、`in_flight=0`、`jobs` 表没有它的作业，状态却仍是 `running`。
 * 由于 `BatchCapabilitiesFor('running')` 只给 `canPause`，界面上只剩「暂停」按钮，
 * 而 12 条失败项 `retryable=false` 又让「恢复失败项」永不出现 —— 用户拿不到任何出口。
 *
 * 本脚本做的是**可控的单变量实验**（这是它存在的理由）：
 *
 *   1. 把 `b_1` 的库内状态手工置回缺陷形态
 *      （`running` + `control_state=run` + `finished_at=NULL`，单元事实不变：12 条 failed）；
 *   2. 等 `studioMaintenanceInterval`（30s）的一个完整维护轮次；
 *   3. 读权威读数（API）+ 界面事实（可点按钮、缺口卡）+ worker 日志。
 *
 * 因此 before/after 的差异**只来自部署的代码**：同一份数据、同一个脚本、
 * 同一视口、同一账号。若收敛路径不存在，缺陷形态会原样保留。
 *
 * 判定（全部是机器事实）：
 *   * 收敛：status 从 `running` → `partial_failed`；
 *   * 出口：能力位 `canResume`/`canRetryFailed` 从 false → true，
 *     可点按钮从「仅暂停」→ 出现「恢复失败项」「补齐缺口（继续本批次）」；
 *   * 可达：worker 维护日志出现 `studio.maintain.divergence_corrected batch=1`。
 *
 * 运行：
 *   node test/audit/issue-202/repro.mjs --phase before|after
 * 产物：
 *   docs/audit/issue-202/<phase>.json
 *   docs/audit/issue-202/<phase>-b1-detail.png
 *   docs/audit/issue-202/<phase>-runs-list.png
 *
 * 防覆盖：已存在的证据默认不覆盖（与 issue-200/201/205/212/213 同一约定）。
 */
import { createRequire } from 'node:module'
import { existsSync, mkdirSync, writeFileSync } from 'node:fs'
import { execFileSync } from 'node:child_process'
import path from 'node:path'
import { fileURLToPath } from 'node:url'

const require = createRequire('/root/llm/package.json')
const { chromium } = require('/root/.pi/agent/npm/node_modules/playwright')

const HERE = path.dirname(fileURLToPath(import.meta.url))
const OUT = path.resolve(HERE, '../../../docs/audit/issue-202')
mkdirSync(OUT, { recursive: true })

const phaseArg = process.argv.indexOf('--phase')
const PHASE = phaseArg >= 0 ? process.argv[phaseArg + 1] : 'before'
const FORCE = process.argv.includes('--force')

if (!FORCE) {
  const existing = [`${PHASE}.json`, `${PHASE}-b1-detail.png`]
    .filter((name) => existsSync(path.join(OUT, name)))
  if (existing.length > 0) {
    console.error(`[issue-202] 拒绝覆盖已存在的证据（${PHASE}）：${existing.join(', ')}`)
    console.error('  若确实要重采，显式传 --force。')
    process.exit(2)
  }
}

const BASE = process.env.BASE_URL ?? 'http://127.0.0.1:3210'
const EMAIL = process.env.ADMIN_EMAIL ?? 'admin@company.com'
const PASSWORD = process.env.ADMIN_PASSWORD ?? 'admin123456'
const PROJECT_ID = process.env.PROJECT_ID ?? '1'
const PG_CONTAINER = process.env.PG_CONTAINER ?? 'llm-postgres-1'
const WORKER_CONTAINER = process.env.WORKER_CONTAINER ?? 'llm-worker-1'
const BATCH_ID = 1
const MAINTENANCE_WAIT_MS = Number(process.env.MAINTENANCE_WAIT_MS ?? 45000)

function psql(sql, { tuplesOnly = true } = {}) {
  const args = ['exec', PG_CONTAINER, 'psql', '-U', 'llm_factory', '-d', 'llm_factory']
  args.push(...(tuplesOnly ? ['-t', '-A', '-F', '|'] : []), '-c', sql)
  return execFileSync('docker', args, { encoding: 'utf8' }).trim()
}

/**
 * 把 b_1 置回缺陷形态：状态指针拨回 running，但单元事实（12 条 failed、无在途）
 * 与作业事实（无活作业、无新作业）保持不变。
 *
 * 这正是原文第 3 步之后的形态：resume 把 status 拨回 running，而没有任何东西会再跑。
 */
function armDefect() {
  psql(
    "UPDATE batches SET status='running', control_state='run', "
    + 'completed_units=0, failed_units=12, in_flight_units=0, '
    + `finished_at=NULL, updated_at=NOW() WHERE id=${BATCH_ID}`,
  )
  return {
    armedRow: psql(
      `SELECT status, control_state, planned_units, completed_units, failed_units, `
      + `in_flight_units, finished_at IS NULL FROM batches WHERE id=${BATCH_ID}`,
    ),
    failedItems: Number(psql(
      `SELECT COUNT(*) FROM batch_items WHERE batch_id=${BATCH_ID} AND status='failed'`,
    )),
    retryableItems: Number(psql(
      `SELECT COUNT(*) FROM batch_items WHERE batch_id=${BATCH_ID} AND status='failed' AND retryable`,
    )),
    activeJobs: Number(psql(
      `SELECT COUNT(*) FROM jobs WHERE batch_id=${BATCH_ID} AND status IN ('pending','leased','running')`,
    )),
  }
}

/**
 * 本**次运行内**worker 维护循环是否纠正了该批次（而不是历史遗留的日志）。
 *
 * 必须 `2>&1` 合并 stderr：Go 的 `log` 包默认写 stderr，而 `execFileSync` 只返回
 * stdout —— 只收 stdout 会永远数到 0 条，把「维护循环真的纠正了」误报成「没纠正」。
 */
function divergenceCorrections() {
  const logs = execFileSync('sh', ['-c', `docker logs ${WORKER_CONTAINER} 2>&1`], { encoding: 'utf8' })
  return (logs.match(new RegExp(`studio\\.maintain\\.divergence_corrected batch=${BATCH_ID}\\b`, 'g')) ?? []).length
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

const readBatch = async (batchId) => page.evaluate(async ({ projectId, batchId }) => {
  const response = await fetch(`/api/v1/projects/${projectId}/batches/${batchId}`, { credentials: 'include' })
  const body = await response.json()
  return body.data?.batch ?? null
}, { projectId: PROJECT_ID, batchId })

// ---- 1) 手工置回缺陷形态（同一份数据，before/after 完全一致）----
const armed = armDefect()

// 维护纠正必须在**本次运行内**发生。历史日志里可能已经有 `divergence_corrected`
// （09-30 那轮维护循环写过），因此只查「存不存在」会让 before 相位也误判为收敛。
// 必须测本次运行内的增量。
const correctionsBefore = divergenceCorrections()

// ---- 2) 等一个完整维护轮次（30s interval，留足余量）----
await page.waitForTimeout(MAINTENANCE_WAIT_MS)

const correctionsAfter = divergenceCorrections()
const newCorrectionLogs = correctionsAfter - correctionsBefore

// ---- 3) 读权威读数 + 界面事实 ----
const batch = await readBatch(`b_${BATCH_ID}`)

await page.goto(`${BASE}/p/${PROJECT_ID}/runs/b_${BATCH_ID}`, { waitUntil: 'networkidle' })
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

const detailText = await page.evaluate(() => ({
  shortfallCardText: (document.querySelector('[data-batch-shortfall="true"]')?.innerText ?? '').trim(),
  bodyText: document.body.innerText,
}))

await page.screenshot({ path: path.join(OUT, `${PHASE}-b1-detail.png`), fullPage: true })
await page.goto(`${BASE}/p/${PROJECT_ID}/runs`, { waitUntil: 'networkidle' })
await page.waitForTimeout(1500)
await page.screenshot({ path: path.join(OUT, `${PHASE}-runs-list.png`), fullPage: true })

const result = {
  issue: 202,
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
    controlState: batch.controlState,
    plannedUnits: batch.plannedUnits,
    completedUnits: batch.completedUnits,
    failedUnits: batch.failedUnits,
    inFlightUnits: batch.inFlightUnits,
    shortfallUnits: batch.shortfallUnits,
    shortfallNote: batch.shortfallNote,
    capabilities: batch.capabilities,
  },
  clickableActionButtons: clickable,
  shortfallCardText: detailText.shortfallCardText,
  showsRunningWhileSettled: /运行中/.test(detailText.bodyText),
  correctionsBefore,
  correctionsAfter,
  newCorrectionLogs,
  pageErrors: pageErrors.slice(0, 6),
}

// 判定：僵尸形态被收敛，且界面出现出口。
result.ok = Boolean(
  loginOk &&
  batch &&
  batch.status === 'partial_failed' &&
  batch.capabilities?.canResume === true &&
  batch.capabilities?.canRetryFailed === true &&
  clickable.some((label) => label.includes('恢复失败项') || label.includes('补齐缺口')) &&
  newCorrectionLogs > 0,
)

writeFileSync(path.join(OUT, `${PHASE}.json`), `${JSON.stringify(result, null, 2)}\n`)

console.log(`[issue-202] phase=${PHASE} deployed=${result.deployedVersion} armed=${armed.armedRow}`)
console.log(`  单元事实: failed=${armed.failedItems} retryable=${armed.retryableItems} 活作业=${armed.activeJobs}`)
console.log(`  status=${batch?.status} capabilities=${JSON.stringify(batch?.capabilities)}`)
console.log(`  可点按钮=${JSON.stringify(clickable)}`)
console.log(`  本次新增维护纠正=${newCorrectionLogs}（历史 ${correctionsBefore} 条）ok=${result.ok}`)

await browser.close()
process.exit(result.ok ? 0 : 1)
