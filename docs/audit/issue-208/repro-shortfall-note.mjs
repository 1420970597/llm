/**
 * #208 复核：批次缺口文案是否仍写死「覆盖率不足或无素材接地」并与真因矛盾。
 *
 * 判定是**机器事实**（两条，缺一不可）：
 *   1. 缺口卡文案**不得**再出现「覆盖率不足 / 方向配额 / 素材接地」这类
 *      与真因无关的归因（它们是 #208 的具体形态）；
 *   2. 缺口卡文案必须与 `/failures` 的 `suggestedAction` **指向同一动作**
 *      ——「两处各说一套」正是 #208 的业务影响。
 *
 * 与 #191 第 1 轮同一处理：本机部署的镜像已含修复（1381667 / PR #228），
 * 因此无法再产出真正的「修复前」图。before 复用既有归档
 * `docs/audit/issue-214/before-b1-zombie.png`（b_1 缺口卡写「覆盖矩阵的方向配额
 * 或素材接地不足」，与失败详情「缺模型连接」互相矛盾），不伪造 before。
 *
 * 用法：node docs/audit/issue-208/repro-shortfall-note.mjs after
 * 产物：docs/audit/issue-208/02-after-shortfall.png + after-shortfall.json
 */
import { createRequire } from 'node:module'
import { writeFileSync } from 'node:fs'
import { dirname, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

const require = createRequire(import.meta.url)
const { chromium } = require(process.env.PLAYWRIGHT_PATH ?? '/root/.pi/agent/npm/node_modules/playwright')

const here = dirname(fileURLToPath(import.meta.url))
const BASE = process.env.BASE_URL ?? 'http://127.0.0.1:3210'
const PROJECT_ID = process.env.PROJECT_ID ?? '1'
// b_1：12/12 单元 error_class=config_error（缺模型连接）—— #208 原文的取证对象。
const BATCH = process.env.BATCH_ID ?? 'b_1'
// b_1 是 issue 原文的取证对象，用规范文件名；其余批次加后缀，避免互相覆盖。
const SUFFIX = BATCH === 'b_1' ? '' : `-${BATCH}`

const browser = await chromium.launch({ headless: true })
const ctx = await browser.newContext({ viewport: { width: 1600, height: 1000 }, locale: 'zh-CN' })
const page = await ctx.newPage()
const consoleErrors = []
page.on('pageerror', (e) => consoleErrors.push(`pageerror: ${String(e).slice(0, 160)}`))
page.on('response', (r) => { if (r.status() >= 500) consoleErrors.push(`HTTP ${r.status()} ${r.url().replace(BASE, '').slice(0, 90)}`) })

await page.goto(`${BASE}/login`, { waitUntil: 'networkidle' })
await page.getByPlaceholder('请输入邮箱').fill(process.env.ADMIN_EMAIL ?? 'admin@company.com')
await page.getByPlaceholder('请输入密码').fill(process.env.ADMIN_PASSWORD ?? 'admin123456')
await page.getByRole('button', { name: '进入今日工作' }).click()
await page.waitForURL(/\/today/, { timeout: 20000 })

await page.goto(`${BASE}/p/${PROJECT_ID}/runs/${BATCH}`, { waitUntil: 'networkidle' })
await page.waitForTimeout(1800)

const banner = await page.locator('[data-batch-shortfall="true"]').first().innerText().catch(() => '')
const failures = await page.evaluate(async ({ base, projectId, batch }) => {
  const res = await fetch(`${base}/api/v1/projects/${projectId}/batches/${batch}/failures?limit=1`, { credentials: 'include' })
  const body = await res.json()
  const item = body.items?.[0] ?? null
  return item ? { errorClass: item.errorClass, errorMessage: item.errorMessage, suggestedAction: item.suggestedAction } : null
}, { base: BASE, projectId: PROJECT_ID, batch: BATCH })

const normalized = (s) => (s ?? '').replace(/\s+/g, '')
const report = {
  base: BASE, viewport: { width: 1600, height: 1000 }, batch: BATCH,
  shortfallCard: banner.replace(/\n+/g, ' / ').trim(),
  failure: failures,
  // 判定 1：不得再有与真因无关的覆盖率归因。
  blamesCoverage: /覆盖率不足|方向配额|素材接地/.test(banner),
  // 判定 2：缺口卡必须包含失败详情给出的「同一句建议」。
  agreesWithSuggestion: failures?.suggestedAction
    ? normalized(banner).includes(normalized(failures.suggestedAction))
    : null,
  consoleErrors,
}
report.defectPresent = report.blamesCoverage || report.agreesWithSuggestion === false

await page.screenshot({ path: resolve(here, `02-after-shortfall${SUFFIX}.png`) })
writeFileSync(resolve(here, `after-shortfall${SUFFIX}.json`), `${JSON.stringify(report, null, 2)}\n`)
await browser.close()

console.log(JSON.stringify(report, null, 2))
console.log(`\n[repro-208] 缺口卡归因覆盖率 = ${report.blamesCoverage}；与失败建议同句 = ${report.agreesWithSuggestion}（缺陷${report.defectPresent ? '仍在' : '已消失'}）`)
process.exit(report.defectPresent ? 2 : 0)
