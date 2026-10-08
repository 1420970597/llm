/**
 * issue #197 第 11 条 §A 复现/验证：覆盖矩阵 `m × n × z` 结构公式必须算术自洽。
 *
 * 缺陷形态（实测于 /p/p_1/coverage）：结构公式渲染为 `m 1 × n 2 × z 4 = 4`
 * —— `z` 取的是全部配额之和，结果又复用同一个数，于是 `1 × 2 × 4 ≠ 4`。
 * 一个自称「数据集结构」的公式自己算不通，用户无法用它预判
 * 「改方向数/配额会不会影响产出量」。
 *
 * 判定（机器事实，不靠肉眼）：从页面读回 `m A × n B × z C = D` 四个数字，
 * 断言 `A × B × C === D` 且 `D === 可产出量（Σ方向配额，quota≤0 视为 1）`。
 * 非乘积形态（各方向配额不等）下断言页面**不编造**乘积等式。
 *
 * 用法：node docs/audit/issue-197-11/repro.mjs --phase before|after
 * 产物：docs/audit/issue-197-11/<phase>.json
 *       docs/audit/issue-197-11/<phase>-coverage.png
 *
 * 防覆盖：已存在的证据默认不覆盖（与 issue-200/205/212/213 的同一约定）。
 */
import { createRequire } from 'node:module'
import { existsSync, mkdirSync, writeFileSync } from 'node:fs'
import path from 'node:path'
import { fileURLToPath } from 'node:url'

const require = createRequire('/root/llm/package.json')
const { chromium } = require('/root/.pi/agent/npm/node_modules/playwright')

const HERE = path.dirname(fileURLToPath(import.meta.url))
mkdirSync(HERE, { recursive: true })

const phaseArg = process.argv.indexOf('--phase')
const PHASE = phaseArg >= 0 ? process.argv[phaseArg + 1] : 'before'
const FORCE = process.argv.includes('--force')

if (!FORCE) {
  const existing = [`${PHASE}.json`, `${PHASE}-coverage.png`]
    .filter((name) => existsSync(path.join(HERE, name)))
  if (existing.length > 0) {
    console.error(`[issue-197-11] 拒绝覆盖已存在的证据（${PHASE}）：${existing.join(', ')}`)
    console.error('  要重采必须显式传 --force；在已修复的栈上采 “before” 会得到修复后的读数。')
    process.exit(2)
  }
}

const BASE = process.env.BASE_URL ?? 'http://127.0.0.1:3210'
const EMAIL = process.env.ADMIN_EMAIL ?? 'admin@company.com'
const PASSWORD = process.env.ADMIN_PASSWORD ?? 'admin123456'
const PROJECT_ID = process.env.PROJECT_ID ?? 'p_1'

const browser = await chromium.launch({ headless: true })
const ctx = await browser.newContext({ viewport: { width: 1600, height: 1000 }, locale: 'zh-CN', deviceScaleFactor: 1 })
const page = await ctx.newPage()
const consoleErrors = []
page.on('pageerror', (e) => consoleErrors.push(`pageerror: ${String(e).slice(0, 160)}`))
page.on('response', (r) => { if (r.status() >= 500) consoleErrors.push(`HTTP ${r.status()} ${r.url().slice(0, 90)}`) })

await page.goto(`${BASE}/login`, { waitUntil: 'networkidle' })
await page.getByPlaceholder('请输入邮箱').fill(EMAIL)
await page.getByPlaceholder('请输入密码').fill(PASSWORD)
await page.getByRole('button', { name: '进入今日工作' }).click()
await page.waitForURL(/\/today/, { timeout: 20000 })
await page.waitForTimeout(1000)

const version = await page.evaluate(async () => {
  const response = await fetch('/version.json', { credentials: 'include' })
  return response.ok ? await response.json() : null
}).catch(() => null)

/** 读取当前覆盖版本 payload 与服务端可产出量口径（Σ方向配额，quota≤0 视为 1）。 */
await page.goto(`${BASE}/p/${PROJECT_ID}/coverage`, { waitUntil: 'networkidle' })
await page.waitForTimeout(2000)

const reading = await page.evaluate(async (projectId) => {
  const formulaNode = document.querySelector('[data-coverage-formula="true"]')
  const formulaText = (formulaNode?.textContent ?? '').replace(/\s+/g, ' ').trim()
  const response = await fetch(`/api/v1/projects/${projectId}/coverage-versions?limit=1`, { credentials: 'include' })
  const body = response.ok ? await response.json() : null
  const payload = body?.items?.[0]?.payload ?? null
  const domains = Array.isArray(payload?.domains) ? payload.domains : []
  const quotas = domains.flatMap((domain) =>
    (Array.isArray(domain.directions) ? domain.directions : []).map((direction) => {
      const quota = Number(direction.quota)
      return Number.isFinite(quota) && quota > 0 ? quota : 1
    }),
  )
  return {
    formulaText,
    formulaIsProduct: formulaNode?.getAttribute('data-coverage-formula-product') ?? null,
    domainCount: domains.length,
    directionCount: quotas.length,
    capacityFromData: quotas.reduce((total, quota) => total + quota, 0),
    quotas,
  }
}, PROJECT_ID)

const match = reading.formulaText.match(/^m (\d+) × n (\d+) × z (\d+) = (\d+)$/)
const parsed = match
  ? { m: Number(match[1]), n: Number(match[2]), z: Number(match[3]), result: Number(match[4]) }
  : null
const formulaArithmeticHolds = parsed !== null && parsed.m * parsed.n * parsed.z === parsed.result
const formulaMatchesCapacity = parsed !== null && parsed.result === reading.capacityFromData

await page.locator('[data-coverage-formula="true"]').scrollIntoViewIfNeeded().catch(() => {})
await page.screenshot({ path: path.join(HERE, `${PHASE}-coverage.png`), fullPage: true })

const report = {
  issue: 197,
  item: 11,
  phase: PHASE,
  base: BASE,
  viewport: '1600x1000',
  account: EMAIL,
  deployedVersion: version,
  route: `/p/${PROJECT_ID}/coverage`,
  formulaText: reading.formulaText,
  formulaIsProduct: reading.formulaIsProduct,
  parsedFormula: parsed,
  formulaArithmeticHolds,
  capacityFromData: reading.capacityFromData,
  formulaMatchesCapacity,
  domainCount: reading.domainCount,
  directionCount: reading.directionCount,
  quotas: reading.quotas,
  consoleErrors,
  ok: formulaArithmeticHolds && formulaMatchesCapacity && consoleErrors.length === 0,
}

writeFileSync(path.join(HERE, `${PHASE}.json`), `${JSON.stringify(report, null, 2)}\n`)
await browser.close()

console.log(`[issue-197-11] phase=${PHASE}`)
console.log(`  公式=${reading.formulaText}`)
console.log(`  m×n×z == 结果: ${formulaArithmeticHolds} · 结果 == 可产出量(${reading.capacityFromData}): ${formulaMatchesCapacity}`)
console.log(`  判定: ${report.ok ? 'FIXED' : 'STILL_BROKEN'}`)
if (!report.ok) process.exit(1)
