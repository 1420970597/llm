/**
 * Issue #210 复核取证：审计类动态的「查看」是否指向**该操作的对象**。
 *
 * 为什么单独写一个而不是复用 issue-214 的脚本：那个脚本只点**第一条**审计记录，
 * 而 #210 的缺陷形态是「**19 条全部**指向概览」。只验一条无法排除
 * 「修好了第一条、其余仍回退」——那正是本 issue 的形态（全量而非抽样）。
 *
 * 因此这里做**全量扫描**：
 *   1. 取 `/activity` 上全部 `[data-activity-item="audit"]` 行，逐行读 href；
 *   2. 断言没有任何一条落到 `/p/{id}/overview`（#210 的缺陷判据）；
 *   3. 另做一次**真实点击**（不是只读 href），确认落点页面确实能回答该记录。
 *
 * 用法：
 *   node docs/audit/issue-210/repro-audit-links.mjs        # 产出 02-after-*.png/json
 */
import { mkdirSync, writeFileSync } from 'node:fs'
import { createRequire } from 'node:module'
import path from 'node:path'
import { fileURLToPath } from 'node:url'

const require = createRequire('/root/llm/package.json')
const { chromium } = require('/root/.pi/agent/npm/node_modules/playwright')

const HERE = path.dirname(fileURLToPath(import.meta.url))
const BASE = process.env.BASE_URL ?? 'http://127.0.0.1:3210'
const PREFIX = process.env.STAGE === 'before' ? '01-before' : '02-after'
const VIEWPORT = { width: 1600, height: 1000 }

mkdirSync(HERE, { recursive: true })

const browser = await chromium.launch({ headless: true })
const ctx = await browser.newContext({ viewport: VIEWPORT, locale: 'zh-CN', deviceScaleFactor: 1 })
const page = await ctx.newPage()

const consoleErrors = []
page.on('console', (m) => { if (m.type() === 'error') consoleErrors.push(m.text().slice(0, 200)) })
page.on('pageerror', (e) => consoleErrors.push(String(e).slice(0, 200)))

await page.goto(`${BASE}/login`, { waitUntil: 'networkidle' })
await page.getByPlaceholder('请输入邮箱').fill('admin@company.com')
await page.getByPlaceholder('请输入密码').fill('admin123456')
await page.getByRole('button', { name: '进入今日工作' }).click()
await page.waitForURL(/\/today/, { timeout: 20000 })
await page.waitForTimeout(1200)

await page.goto(`${BASE}/activity`, { waitUntil: 'networkidle' })
await page.waitForTimeout(2200)

// ---- 全量扫描：每条审计记录的「查看」目标 ----
const scan = await page.evaluate(() => {
  const rows = [...document.querySelectorAll('[data-activity-item="audit"]')]
  const entries = rows.map((row) => ({
    text: (row.innerText ?? '').replace(/\s+/g, ' ').trim().slice(0, 80),
    href: row.querySelector('a')?.getAttribute('href') ?? null,
  }))
  const isOverview = (href) => !!href && /\/overview$/.test(href)
  return {
    total: rows.length,
    withLink: entries.filter((e) => e.href).length,
    toOverview: entries.filter((e) => isOverview(e.href)).length,
    byTarget: entries.reduce((acc, e) => {
      const key = e.href ?? '(no-link)'
      acc[key] = (acc[key] ?? 0) + 1
      return acc
    }, {}),
    entries,
  }
})

// ---- 真实点击：确认落点确实回答该记录（不是只读 href） ----
const clickProbe = { attempted: false }
const clickable = page.locator('[data-activity-item="audit"]').filter({ has: page.locator('a[href]') })
if ((await clickable.count()) > 0) {
  const first = clickable.first()
  clickProbe.attempted = true
  clickProbe.rowText = (await first.innerText().catch(() => '')).replace(/\s+/g, ' ').trim().slice(0, 80)
  clickProbe.href = await first.locator('a[href]').first().getAttribute('href').catch(() => null)
  await first.locator('a[href]').first().click()
  await page.waitForTimeout(1800)
  clickProbe.landedOn = new URL(page.url()).pathname
  clickProbe.landedTitle = (await page.locator('h1, h2, h3, h4').first().innerText().catch(() => '')).trim().slice(0, 80)
  clickProbe.landedOnOverview = /\/overview$/.test(clickProbe.landedOn)
  await page.screenshot({ path: path.join(HERE, `${PREFIX}-audit-landing.png`), fullPage: true })
}

// ---- 概览页留档（用于「落点不是概览」的对照） ----
await page.goto(`${BASE}/activity`, { waitUntil: 'networkidle' })
await page.waitForTimeout(1600)
await page.screenshot({ path: path.join(HERE, `${PREFIX}-activity.png`), fullPage: true })

const evidence = {
  base: BASE, viewport: VIEWPORT, consoleErrors,
  scan, clickProbe,
  verdict: {
    // #210 的缺陷判据：审计类**存在**且**没有任何一条**指向 /overview。
    auditRowsPresent: scan.total > 0,
    anyAuditLinkToOverview: scan.toOverview > 0,
    passes: scan.total > 0 && scan.toOverview === 0,
  },
}
writeFileSync(path.join(HERE, `${PREFIX}.json`), JSON.stringify(evidence, null, 2) + '\n')
console.log(JSON.stringify(evidence, null, 2))
await browser.close()
