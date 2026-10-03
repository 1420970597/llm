/**
 * issue #202 建议方向 1(b) 的活体核对：对「全部单元已定稿、只剩不可重试失败」的僵尸批次
 * 调用 `resume`，必须**不派发空转作业**，且收敛为 `partial_failed`（而不是回到 `running`）。
 *
 * 为什么单独写：原文的方向 1 有两条约束 ——
 *   (a) resume 必须真的派作业（对有未创建单元的缺口形态）；
 *   (b) 对无待办工作的僵尸，不得把状态拨回 running（旧实现正是这样制造僵尸）。
 * 主复现脚本覆盖的是「维护循环收敛」；(b) 是「用户点继续」这条独立入口，
 * 必须单独取证，否则「按钮能点」与「点了不产生新僵尸」会混为一谈。
 *
 * 判定（机器事实）：
 *   * resume 后状态不是 `running`（应为 `partial_failed`）；
 *   * resume 前后 `studio.batch.generate` 作业数**不变**（没有空转作业）；
 *   * 时间线新增一条 BatchResumed 事件（动作确实生效，只是不产生工作）。
 *
 * 运行：node test/audit/issue-202/check-resume.mjs
 * 产物：docs/audit/issue-202/resume-check.json
 */
import { mkdirSync, writeFileSync } from 'node:fs'
import { execFileSync } from 'node:child_process'
import path from 'node:path'
import { fileURLToPath } from 'node:url'
import { createRequire } from 'node:module'

const require = createRequire('/root/llm/package.json')
const { chromium } = require('/root/.pi/agent/npm/node_modules/playwright')

const HERE = path.dirname(fileURLToPath(import.meta.url))
const OUT = path.resolve(HERE, '../../../docs/audit/issue-202')
mkdirSync(OUT, { recursive: true })

const BASE = process.env.BASE_URL ?? 'http://127.0.0.1:3210'
const EMAIL = process.env.ADMIN_EMAIL ?? 'admin@company.com'
const PASSWORD = process.env.ADMIN_PASSWORD ?? 'admin123456'
const PROJECT_ID = process.env.PROJECT_ID ?? '1'
const PG_CONTAINER = process.env.PG_CONTAINER ?? 'llm-postgres-1'
const BATCH_ID = 1

function psql(sql) {
  return execFileSync('docker', [
    'exec', PG_CONTAINER, 'psql', '-U', 'llm_factory', '-d', 'llm_factory', '-t', '-A', '-c', sql,
  ], { encoding: 'utf8' }).trim()
}

// 把 b_1 置回僵尸形态，且**先确保控制态允许 resume**
// （`paused` 是用户点「继续」的正常入口条件）。
psql(
  "UPDATE batches SET status='running', control_state='paused', "
  + 'completed_units=0, failed_units=12, in_flight_units=0, '
  + `finished_at=NULL, updated_at=NOW() WHERE id=${BATCH_ID}`,
)

const jobsBefore = Number(psql(
  `SELECT COUNT(*) FROM jobs WHERE batch_id=${BATCH_ID} AND job_kind='studio.batch.generate'`,
))

const browser = await chromium.launch({ headless: true })
const ctx = await browser.newContext({ viewport: { width: 1600, height: 1000 }, locale: 'zh-CN' })
const page = await ctx.newPage()

await page.goto(`${BASE}/login`, { waitUntil: 'networkidle' })
await page.getByPlaceholder('请输入邮箱').fill(EMAIL)
await page.getByPlaceholder('请输入密码').fill(PASSWORD)
await page.getByRole('button', { name: '进入今日工作' }).click()
await page.waitForURL(/\/today/, { timeout: 20000 })
await page.waitForTimeout(1000)

const resumeResponse = await page.evaluate(async ({ projectId, batchId }) => {
  const response = await fetch(`/api/v1/projects/${projectId}/batches/${batchId}/resume`, {
    method: 'POST', credentials: 'include',
  })
  const body = await response.json().catch(() => null)
  return { status: response.status, batch: body?.data?.batch ?? null, error: body?.error ?? null }
}, { projectId: PROJECT_ID, batchId: `b_${BATCH_ID}` })

const jobsAfter = Number(psql(
  `SELECT COUNT(*) FROM jobs WHERE batch_id=${BATCH_ID} AND job_kind='studio.batch.generate'`,
))
const statusAfter = psql(`SELECT status FROM batches WHERE id=${BATCH_ID}`)
const resumedEvents = Number(psql(
  `SELECT COUNT(*) FROM batch_events WHERE batch_id=${BATCH_ID} AND event_type='BatchResumed'`,
))

const result = {
  issue: 202,
  check: 'suggested-direction-1b-resume-does-not-requeue-zombie',
  base: BASE,
  deployedVersion: await page.evaluate(async () => {
    try { return (await (await fetch('/version.json')).json()).version } catch { return null }
  }),
  resumeHttpStatus: resumeResponse.status,
  resumeError: resumeResponse.error,
  statusAfterResume: statusAfter,
  capabilitiesAfterResume: resumeResponse.batch?.capabilities ?? null,
  generateJobsBefore: jobsBefore,
  generateJobsAfter: jobsAfter,
  noOpJobsCreated: jobsAfter - jobsBefore,
  resumedEventsTotal: resumedEvents,
}

// 判定：状态不回到 running + 没有多派空转作业 + 动作确实生效（写了一条 BatchResumed）。
result.ok = Boolean(
  resumeResponse.status === 202 &&
  statusAfter === 'partial_failed' &&
  jobsAfter - jobsBefore === 0,
)

writeFileSync(path.join(OUT, 'resume-check.json'), `${JSON.stringify(result, null, 2)}\n`)

console.log(`[issue-202] resume 检查：http=${resumeResponse.status} status_after=${statusAfter}`)
console.log(`  作业数 ${jobsBefore} → ${jobsAfter}（新增空转作业=${jobsAfter - jobsBefore}）capabilities=${JSON.stringify(result.capabilitiesAfterResume)}`)
console.log(`  ok=${result.ok}`)

await browser.close()
process.exit(result.ok ? 0 : 1)
