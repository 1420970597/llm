/**
 * 甲方验收审计：全路由截图采集器。
 *
 * 目标：真实 Chromium 登录后逐个访问路由，全页截图 + 采集可见文案、
 * console 错误、失败请求、以及关键结构指标（按钮数、空状态、表格行数）。
 *
 * 产物：docs/audit/screenshots/<n>-<key>.png 与 docs/audit/capture.json
 */
import { mkdirSync, writeFileSync } from 'node:fs'
import { createRequire } from 'node:module'
import path from 'node:path'
import { fileURLToPath } from 'node:url'

const require = createRequire('/root/llm/package.json')
const { chromium } = require('/root/.pi/agent/npm/node_modules/playwright')

const REPO_ROOT = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '../..')
const OUT_DIR = path.join(REPO_ROOT, 'docs/audit/screenshots')
const BASE = process.env.BASE_URL ?? 'http://127.0.0.1:3210'
const EMAIL = 'admin@company.com'
const PASSWORD = 'admin123456'
const VIEWPORT = { width: 1600, height: 1000 }

mkdirSync(OUT_DIR, { recursive: true })

const browser = await chromium.launch({ headless: true })
const ctx = await browser.newContext({ viewport: VIEWPORT, locale: 'zh-CN', deviceScaleFactor: 1 })
const page = await ctx.newPage()

let events = []
page.on('console', (m) => { if (m.type() === 'error') events.push({ kind: 'console', text: m.text().slice(0, 300) }) })
page.on('pageerror', (e) => events.push({ kind: 'pageerror', text: String(e).slice(0, 300) }))
page.on('response', (r) => {
  if (r.status() >= 400) events.push({ kind: 'http', status: r.status(), url: r.url().replace(BASE, '') })
})

// ---- login ----
await page.goto(`${BASE}/login`, { waitUntil: 'networkidle' })
await page.getByPlaceholder('请输入邮箱').fill(EMAIL)
await page.getByPlaceholder('请输入密码').fill(PASSWORD)
await page.getByRole('button', { name: '进入今日工作' }).click()
await page.waitForURL(/\/today/, { timeout: 15000 })
await page.waitForTimeout(1500)

const results = []
const shots = []

async function capture(key, routePath, opts = {}) {
  events = []
  const url = `${BASE}${routePath}`
  let status = 'ok'
  try {
    await page.goto(url, { waitUntil: 'networkidle', timeout: 30000 })
  } catch (e) {
    status = `nav-failed: ${String(e).slice(0, 120)}`
  }
  await page.waitForTimeout(opts.settle ?? 1800)

  const n = String(shots.length + 1).padStart(2, '0')
  const file = path.join(OUT_DIR, `${n}-${key}.png`)
  try {
    await page.screenshot({ path: file, fullPage: true })
    shots.push({ key, file })
  } catch (e) {
    status = `shot-failed: ${String(e).slice(0, 120)}`
  }

  const info = await page.evaluate(() => {
    const txt = (el) => (el?.innerText ?? '').trim()
    const visible = (el) => {
      if (!el) return false
      const r = el.getBoundingClientRect()
      const s = getComputedStyle(el)
      return r.width > 0 && r.height > 0 && s.visibility !== 'hidden' && s.display !== 'none'
    }
    const all = [...document.querySelectorAll('button, a[role="button"], [role="button"]')]
    const buttons = all.filter(visible).map((b) => txt(b)).filter(Boolean)
    return {
      title: document.title,
      h1: [...document.querySelectorAll('h1,h2,h3')].filter(visible).map((h) => txt(h)).slice(0, 12),
      buttons: [...new Set(buttons)].slice(0, 40),
      buttonCount: buttons.length,
      bodyText: document.body.innerText.replace(/\n{2,}/g, '\n').slice(0, 6000),
      bodyLen: document.body.innerText.length,
      tables: document.querySelectorAll('table').length,
      rows: document.querySelectorAll('tbody tr').length,
      inputs: [...document.querySelectorAll('input,textarea,select')].filter(visible).length,
      emptyHints: [...document.querySelectorAll('*')]
        .filter((e) => e.children.length === 0 && /暂无|还没有|没有.*数据|未配置|尚未/.test(e.textContent ?? ''))
        .map((e) => (e.textContent ?? '').trim())
        .filter((t) => t.length < 80)
        .slice(0, 10),
      docScrollHeight: document.documentElement.scrollHeight,
      viewportHeight: window.innerHeight,
    }
  })

  const dedup = []
  const seen = new Set()
  for (const e of events) {
    const k = JSON.stringify(e)
    if (!seen.has(k)) { seen.add(k); dedup.push(e) }
  }

  results.push({ key, routePath, finalUrl: page.url(), status, ...info, events: dedup })
  console.log(`[${status === 'ok' ? ' OK ' : 'WARN'}] ${key.padEnd(28)} ${routePath}  h=${info.docScrollHeight} btns=${info.buttonCount} err=${dedup.length}`)
}

// ===== 1. 全局入口 =====
const ROUTES = [
  ['today', '/today'],
  ['projects', '/projects'],
  ['recipes', '/recipes'],
  ['deliveries', '/deliveries'],
  ['new-step1', '/new'],
  ['new-step2', '/new/coverage'],
  ['new-step3', '/new/quality'],
  ['activity', '/activity'],
  ['tools-evaluation', '/tools/evaluation'],
  ['tools-cleaning', '/tools/cleaning'],
  ['legacy-history', '/legacy/history'],
  ['settings-connections', '/settings/connections'],
  ['settings-team', '/settings/team'],
  ['settings-capabilities', '/settings/capabilities'],
  ['help', '/help'],
  ['catalog', '/catalog'],
  ['legacy-console-home', '/console/home'],
  ['legacy-console-planning', '/console/planning'],
  ['legacy-console-tasks', '/console/tasks'],
  ['legacy-console-results', '/console/results'],
  ['legacy-console-evaluation', '/console/evaluation'],
  ['legacy-console-cleaning', '/console/cleaning'],
  ['legacy-console-operations', '/console/operations'],
  ['legacy-console-domains', '/console/domains'],
  ['legacy-console-domains-legacy', '/console/domains/legacy'],
  ['legacy-console-questions', '/console/questions/legacy'],
  ['legacy-console-reasoning', '/console/reasoning/legacy'],
  ['legacy-console-rewards', '/console/rewards/legacy'],
  ['legacy-console-exports', '/console/exports/legacy'],
  ['legacy-console-help', '/console/help'],
  ['legacy-admin-providers', '/console/admin/providers'],
  ['legacy-admin-storage', '/console/admin/storage'],
  ['legacy-admin-strategies', '/console/admin/strategies'],
  ['legacy-admin-prompts', '/console/admin/prompts'],
  ['legacy-admin-audit', '/console/admin/audit'],
  ['notfound', '/this-route-does-not-exist'],
]

for (const [key, p] of ROUTES) await capture(key, p)

// ===== 2. 创建项目（真实 UI 交互）=====
console.log('\n=== 通过向导真实创建一个项目 ===')
events = []
await page.goto(`${BASE}/new`, { waitUntil: 'networkidle' })
await page.waitForTimeout(1200)

async function fillWizard() {
  const nameInput = page.getByPlaceholder('例如：冷链问答数据')
  if (await nameInput.count()) await nameInput.fill('甲方验收-冷链问答')
  const goalInput = page.getByPlaceholder('例如：交付可用于 SFT 的冷链领域问答')
  if (await goalInput.count()) await goalInput.fill('交付可用于 SFT 的冷链领域问答数据')
}
await fillWizard()
await page.screenshot({ path: path.join(OUT_DIR, `${String(shots.length + 1).padStart(2, '0')}-new-step1-filled.png`), fullPage: true })
shots.push({ key: 'new-step1-filled' })

// 选目标类型（若有 radio）
const radios = page.locator('.semi-radio, label:has-text("SFT"), label:has-text("GRPO")')
if (await radios.count()) { try { await radios.first().click({ timeout: 3000 }) } catch {} }

// 下一步
let advanced = false
for (const label of ['下一步', '继续', '前往第二步']) {
  const btn = page.getByRole('button', { name: label })
  if (await btn.count()) { try { await btn.first().click({ timeout: 4000 }); advanced = true; break } catch {} }
}
await page.waitForTimeout(1500)
console.log('step2 url:', page.url(), 'advanced:', advanced)
await capture('new-step2-filled', new URL(page.url()).pathname)

// 填第二步
const nums = page.locator('input[type="text"], input:not([type])')
const visibleInputs = []
for (let i = 0; i < await nums.count(); i++) {
  const el = nums.nth(i)
  if (await el.isVisible()) visibleInputs.push(el)
}
console.log('step2 visible inputs:', visibleInputs.length)
for (const el of visibleInputs) {
  const v = await el.inputValue()
  if (!v) { try { await el.fill('4') } catch {} }
}
await page.screenshot({ path: path.join(OUT_DIR, `${String(shots.length + 1).padStart(2, '0')}-new-step2-filled.png`), fullPage: true })
shots.push({ key: 'new-step2-filled' })

for (const label of ['下一步', '继续']) {
  const btn = page.getByRole('button', { name: label })
  if (await btn.count()) { try { await btn.first().click({ timeout: 4000 }); break } catch {} }
}
await page.waitForTimeout(1500)
console.log('step3 url:', page.url())
await capture('new-step3-filled', new URL(page.url()).pathname)

// 提交
for (const label of ['创建项目', '创建', '完成']) {
  const btn = page.getByRole('button', { name: label })
  if (await btn.count()) { try { await btn.first().click({ timeout: 6000 }); console.log('clicked', label); break } catch (e) { console.log('click fail', label, String(e).slice(0,80)) } }
}
await page.waitForTimeout(3500)
const afterCreateUrl = page.url()
console.log('after create url:', afterCreateUrl)
await page.screenshot({ path: path.join(OUT_DIR, `${String(shots.length + 1).padStart(2, '0')}-after-create.png`), fullPage: true })
shots.push({ key: 'after-create' })
await capture('after-create-page', new URL(afterCreateUrl).pathname)

const m = afterCreateUrl.match(/\/p\/(\d+)/)
const projectId = m ? m[1] : null
console.log('projectId =', projectId)

// ===== 3. 项目内页 =====
if (projectId) {
  const PROJ = [
    ['p-overview', `/p/${projectId}/overview`],
    ['p-blueprint', `/p/${projectId}/blueprint`],
    ['p-coverage', `/p/${projectId}/coverage`],
    ['p-standard', `/p/${projectId}/standard`],
    ['p-runs', `/p/${projectId}/runs`],
    ['p-run-new', `/p/${projectId}/runs/new`],
    ['p-pilot', `/p/${projectId}/pilot`],
    ['p-compare', `/p/${projectId}/compare`],
    ['p-data', `/p/${projectId}/data`],
    ['p-review', `/p/${projectId}/review`],
    ['p-quality', `/p/${projectId}/quality`],
    ['p-quality-new', `/p/${projectId}/quality/new`],
    ['p-rules', `/p/${projectId}/rules`],
    ['p-releases', `/p/${projectId}/releases`],
    ['p-release-new', `/p/${projectId}/releases/new`],
  ]
  for (const [key, p] of PROJ) await capture(key, p)

  // 批次详情：先看有没有批次
  const batches = await fetch(`${BASE}/api/v1/projects/${projectId}/batches`, {
    headers: { cookie: (await ctx.cookies()).map((c) => `${c.name}=${c.value}`).join('; ') },
  }).then((r) => r.json()).catch(() => null)
  console.log('batches resp:', JSON.stringify(batches).slice(0, 500))
  const batchList = batches?.data ?? batches?.items ?? batches
  if (Array.isArray(batchList) && batchList.length) {
    const bid = batchList[0].id
    await capture('p-run-detail', `/p/${projectId}/runs/${bid}`)
    await capture('p-run-failures', `/p/${projectId}/runs/${bid}/failures`)
  }
}

// ===== 4. 方案库详情 / 交付库详情 / 历史资产详情 =====
await capture('recipes-after', '/recipes')
await capture('deliveries-after', '/deliveries')

// ===== 5. 移动端视图 =====
const mob = await ctx.newPage()
await mob.setViewportSize({ width: 390, height: 844 })
for (const [key, p] of [['m-today', '/today'], ['m-projects', '/projects'], ['m-new', '/new'], ['m-login', '/login']]) {
  await mob.goto(`${BASE}${p}`, { waitUntil: 'networkidle' }).catch(() => {})
  await mob.waitForTimeout(1500)
  const n = String(shots.length + 1).padStart(2, '0')
  const file = path.join(OUT_DIR, `${n}-${key}.png`)
  await mob.screenshot({ path: file, fullPage: true }).catch(() => {})
  shots.push({ key, file })
  console.log(`[mob] ${key} ${p}`)
}

writeFileSync(path.join(REPO_ROOT, 'docs/audit/capture.json'), JSON.stringify({ base: BASE, projectId, results, shots }, null, 2))
console.log(`\n完成：${shots.length} 张截图 → ${OUT_DIR}`)
await browser.close()
