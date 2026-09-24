/**
 * 甲方验收审计：功能与流程深挖。
 * 目标：验证「能不能真的把活干完」，以及各类交互反馈是否诚实。
 */
import { mkdirSync, writeFileSync } from 'node:fs'
import { createRequire } from 'node:module'
import path from 'node:path'

const require = createRequire('/root/llm/package.json')
const { chromium } = require('/root/.pi/agent/npm/node_modules/playwright')

const OUT = '/root/llm/docs/audit/screenshots'
const BASE = 'http://127.0.0.1:3210'
mkdirSync(OUT, { recursive: true })

const browser = await chromium.launch({ headless: true })
const ctx = await browser.newContext({ viewport: { width: 1600, height: 1000 }, locale: 'zh-CN' })
const page = await ctx.newPage()
const log = []
let shotN = 100
async function shot(label) {
  const f = path.join(OUT, `${shotN++}-${label}.png`)
  await page.screenshot({ path: f, fullPage: true })
  return f
}
function note(t) { log.push(t); console.log(t) }

await page.goto(`${BASE}/login`, { waitUntil: 'networkidle' })
await page.getByPlaceholder('请输入邮箱').fill('admin@company.com')
await page.getByPlaceholder('请输入密码').fill('admin123456')
await page.getByRole('button', { name: '进入今日工作' }).click()
await page.waitForURL(/\/today/)

// ---------- A. 深链直入：未登录访问受保护路由 ----------
const fresh = await browser.newContext({ viewport: { width: 1440, height: 900 } })
const anon = await fresh.newPage()
for (const p of ['/projects', '/settings/team', '/p/1/blueprint', '/console/admin/providers', '/console/tasks/1']) {
  await anon.goto(`${BASE}${p}`, { waitUntil: 'networkidle' })
  await anon.waitForTimeout(600)
  note(`[匿名深链] ${p} -> ${anon.url().replace(BASE, '')}`)
}
await anon.goto(`${BASE}/settings/team`, { waitUntil: 'networkidle' })
await anon.screenshot({ path: path.join(OUT, `${shotN++}-anon-redirect.png`), fullPage: true })
await fresh.close()

// ---------- B. 未知路由 / catalog ----------
await page.goto(`${BASE}/totally-bogus-page`, { waitUntil: 'networkidle' })
await page.waitForTimeout(800)
note(`[未知路由] /totally-bogus-page -> ${page.url().replace(BASE, '')}  (无 404 页面)`)
await page.goto(`${BASE}/catalog`, { waitUntil: 'networkidle' })
await page.waitForTimeout(800)
note(`[目录评审] /catalog -> ${page.url().replace(BASE, '')}`)

// ---------- C. 蓝图「保存为新版本」是否可用 ----------
await page.goto(`${BASE}/p/1/blueprint`, { waitUntil: 'networkidle' })
await page.waitForTimeout(1500)
const blueprintBefore = await page.locator('body').innerText()
const coverSelect = page.locator('text=覆盖版本 *').locator('..')
const hasSelect = await page.locator('select, .semi-select').count()
note(`[蓝图] 页面上 select 控件数量=${hasSelect}`)
await page.getByPlaceholder('例如：把并发从 8 提到 12').fill('甲方验收测试：尝试保存')
const saveBtn = page.getByRole('button', { name: /保存为新版本/ })
if (await saveBtn.count()) {
  await saveBtn.first().click()
  await page.waitForTimeout(2500)
  const after = await page.locator('body').innerText()
  const banner = after.split('\n').filter((l) => /失败|错误|必填|无法|请先/.test(l)).slice(0, 6)
  note(`[蓝图] 点「保存为新版本」后提示：${JSON.stringify(banner)}`)
  note(`[蓝图] 文案是否变化：${blueprintBefore !== after}`)
}
await page.screenshot({ path: path.join(OUT, `${shotN++}-blueprint-save-attempt.png`), fullPage: true })

// ---------- D. 试制（pilot）能不能真的发起 ----------
await page.goto(`${BASE}/p/1/pilot`, { waitUntil: 'networkidle' })
await page.waitForTimeout(1800)
const pilotText = await page.locator('body').innerText()
note('[试制] 页面文本片段：' + pilotText.split('\n').filter(Boolean).slice(0, 40).join(' | ').slice(0, 1200))
await page.screenshot({ path: path.join(OUT, `${shotN++}-pilot-full.png`), fullPage: true })

// 尝试填写并提交
const pilotInputs = []
const all = page.locator('input, textarea')
for (let i = 0; i < await all.count(); i++) {
  const el = all.nth(i)
  if (await el.isVisible()) pilotInputs.push({ i, ph: await el.getAttribute('placeholder'), val: await el.inputValue() })
}
note('[试制] 可见输入：' + JSON.stringify(pilotInputs))
for (const btn of await page.locator('button').all()) {
  const t = (await btn.innerText()).trim()
  if (t && await btn.isVisible()) note(`[试制] 按钮: ${t}`)
}

// ---------- E. 数据页：表格/筛选是否工作 ----------
await page.goto(`${BASE}/p/1/data`, { waitUntil: 'networkidle' })
await page.waitForTimeout(1800)
await page.evaluate(() => window.scrollTo(0, 1200))
await page.waitForTimeout(500)
await page.screenshot({ path: path.join(OUT, `${shotN++}-data-scrolled.png`), fullPage: false })
const dataText = await page.locator('body').innerText()
note('[数据页] 主内容片段：' + dataText.split('\n').filter(Boolean).slice(0, 60).join(' | ').slice(0, 1400))

// ---------- F. 搜索功能 ----------
await page.goto(`${BASE}/today`, { waitUntil: 'networkidle' })
await page.waitForTimeout(1200)
const search = page.getByPlaceholder(/搜索页面/)
if (await search.count()) {
  await search.first().click()
  await search.first().fill('冷链')
  await page.waitForTimeout(1200)
  await page.screenshot({ path: path.join(OUT, `${shotN++}-command-search.png`), fullPage: false })
  note('[搜索] 输入「冷链」后页面片段：' + (await page.locator('body').innerText()).split('\n').slice(0, 25).join(' | '))
}

// ---------- G. 全局「搜索」按钮（顶栏） ----------
await page.goto(`${BASE}/projects`, { waitUntil: 'networkidle' })
await page.waitForTimeout(1000)
const topSearch = page.getByPlaceholder(/搜索/).last()
if (await topSearch.count()) {
  await topSearch.click()
  await topSearch.type('测', { delay: 50 })
  await page.waitForTimeout(1500)
  await page.screenshot({ path: path.join(OUT, `${shotN++}-topbar-search.png`), fullPage: false })
  note('[顶栏搜索] 输入「测」后 URL=' + page.url().replace(BASE, '') + ' 片段=' + (await page.locator('body').innerText()).split('\n').slice(0, 18).join(' | '))
}

// ---------- H. 刷新按钮一致性 ----------
await page.goto(`${BASE}/p/1/overview`, { waitUntil: 'networkidle' })
await page.waitForTimeout(1200)
const refreshBtns = await page.locator('button:has-text("刷新")').count()
note(`[概览] 「刷新」按钮数量=${refreshBtns}`)
await page.screenshot({ path: path.join(OUT, `${shotN++}-overview-refresh.png`), fullPage: false })

// ---------- I. 无障碍 / 键盘 ----------
const a11y = await page.evaluate(() => {
  const noAlt = [...document.querySelectorAll('img:not([alt])')].length
  const btns = [...document.querySelectorAll('button')]
  const unlabeled = btns.filter((b) => !(b.innerText || '').trim() && !b.getAttribute('aria-label') && !b.getAttribute('title')).length
  const inputs = [...document.querySelectorAll('input,textarea,select')]
  const noLabel = inputs.filter((i) => {
    if (i.getAttribute('aria-label') || i.getAttribute('aria-labelledby')) return false
    if (i.id && document.querySelector(`label[for="${i.id}"]`)) return false
    if (i.closest('label')) return false
    return !(i.getAttribute('placeholder') || '').trim()
  }).length
  return { noAlt, totalButtons: btns.length, unlabeledButtons: unlabeled, inputsNoLabel: noLabel, totalInputs: inputs.length }
})
note('[无障碍] ' + JSON.stringify(a11y))

// ---------- J. 已存在项目列表（数据持久性） ----------
await page.goto(`${BASE}/projects`, { waitUntil: 'networkidle' })
await page.waitForTimeout(1500)
note('[项目列表] ' + (await page.locator('body').innerText()).split('\n').filter(Boolean).slice(0, 30).join(' | '))
await page.screenshot({ path: path.join(OUT, `${shotN++}-projects-with-data.png`), fullPage: true })

// ---------- K. 移动端登录页（新 context） ----------
const mctx = await browser.newContext({ viewport: { width: 390, height: 844 }, locale: 'zh-CN' })
const mp = await mctx.newPage()
await mp.goto(`${BASE}/login`, { waitUntil: 'networkidle' })
await mp.waitForTimeout(1200)
await mp.screenshot({ path: path.join(OUT, `${shotN++}-m-login-real.png`), fullPage: true })
note('[移动端登录] title=' + await mp.title())
await mp.goto(`${BASE}/p/1/blueprint`, { waitUntil: 'networkidle' })
await mp.waitForTimeout(1500)
await mp.screenshot({ path: path.join(OUT, `${shotN++}-m-blueprint.png`), fullPage: true })
await mctx.close()

writeFileSync('/root/llm/docs/audit/functional.json', JSON.stringify(log, null, 2))
console.log('\n=== 完成 ===')
await browser.close()
