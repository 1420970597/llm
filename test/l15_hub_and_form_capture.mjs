/**
 * R17 枢纽页（任务详情）与新建任务表单的补充采集。
 *
 * 为什么单独一个脚本：#90 的两个核心结论落在两处真实 DOM 上 ——
 *   1. 「任务详情页是唯一枢纽，5 张阶段卡片都从这里发散」；
 *   2. 「新建任务页 isAdmin 时一次暴露 6 个字段，其中 3 个是管理员配置」。
 * 这两点必须用真实渲染取证（字段是否可见取决于 isAdmin；卡片是否出现取决于是否有任务）。
 *
 * 运行：node test/l15_hub_and_form_capture.mjs
 * 产物：test/artifacts/page-structure/hub-and-form.json
 */

import { mkdirSync, writeFileSync } from 'node:fs'
import { createRequire } from 'node:module'
import path from 'node:path'
import { fileURLToPath } from 'node:url'

const REPO_ROOT = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..')
const ARTIFACT_DIR = path.join(REPO_ROOT, 'test', 'artifacts', 'page-structure')
const BASE_URL = process.env.BASE_URL ?? 'http://127.0.0.1:18195'
const ADMIN_EMAIL = process.env.L15_ADMIN_EMAIL ?? 'admin@company.com'
const ADMIN_PASSWORD = process.env.L15_ADMIN_PASSWORD ?? 'admin123456'

const require = createRequire(path.join(REPO_ROOT, 'package.json'))
let chromium

try {
  ;({ chromium } = require('/root/.pi/agent/npm/node_modules/playwright'))
} catch {
  ;({ chromium } = require('playwright'))
}

// CI 可执行性是硬要求（CI 不起容器）：先探活，不可达就明确跳过并 exit 0。
const reachable = await fetch(`${BASE_URL}/login`, { method: 'GET' })
  .then((r) => r.status === 200)
  .catch(() => false)
if (!reachable) {
  console.log(`[SKIP] 采集需要真实前端服务，${BASE_URL} 当前不可达。`)
  console.log('       复现：先 npm run build -w apps/web-user，再用 nginx 静态托管 dist（见 l15_page_structure_capture.mjs 的提示）。')
  process.exit(0)
}

mkdirSync(ARTIFACT_DIR, { recursive: true })
const browser = await chromium.launch({ headless: true })
const context = await browser.newContext({ viewport: { width: 1440, height: 960 }, locale: 'zh-CN' })
const page = await context.newPage()

const out = { baseUrl: BASE_URL, capturedAt: new Date().toISOString() }

try {
  // ---- 登录 ----
  await page.goto(`${BASE_URL}/login`, { waitUntil: 'load' })
  await page.locator('input').first().fill(ADMIN_EMAIL)
  await page.locator('input[type=password]').fill(ADMIN_PASSWORD)
  await page.locator('button[type=submit], .semi-button-primary').first().click()
  await page.waitForURL(/\/console\//, { timeout: 20_000 })
  await page.waitForTimeout(1500)

  // ---- 1. 新建任务表单：可见输入字段 ----
  await page.goto(`${BASE_URL}/console/planning`, { waitUntil: 'load' })
  await page.waitForTimeout(2500)
  out.planningForm = await page.evaluate(() => {
    const visible = (el) => {
      const r = el.getBoundingClientRect()
      const s = window.getComputedStyle(el)
      return (r.width > 0 || r.height > 0) && s.visibility !== 'hidden' && s.display !== 'none'
    }
    const textOf = (el) => (el.innerText || el.textContent || '').replace(/\s+/g, ' ').trim()
    // Semi 的 Input/InputNumber/Select 都是 <input> 或 role=combobox
    const fields = []
    for (const el of document.querySelectorAll('input, textarea, [role="combobox"]')) {
      if (!visible(el)) continue
      // 找该控件上方最近的标签文案
      let label = ''
      let node = el.closest('div')
      for (let i = 0; i < 4 && node; i++) {
        const t = Array.from(node.children).find((c) => /font-medium|Text/.test(c.className || ''))
        if (t && textOf(t)) { label = textOf(t); break }
        node = node.parentElement
      }
      fields.push({ tag: el.tagName.toLowerCase(), type: el.type || el.getAttribute('role') || '', label })
    }
    const labels = Array.from(document.querySelectorAll('.font-medium'))
      .filter(visible).map(textOf).filter(Boolean)
    return { fieldCount: fields.length, fields, labels: [...new Set(labels)] }
  })

  // ---- 2. 任务详情（枢纽页）：阶段卡片与转发入口 ----
  const datasets = await page.evaluate(async () => {
    const r = await fetch('/api/v1/datasets', { credentials: 'include' })
    return (await r.json()).slice(0, 3).map((d) => ({ id: d.id, name: d.name, status: d.status }))
  })
  out.sampleDatasets = datasets

  if (datasets.length > 0) {
    const target = datasets[0]
    await page.goto(`${BASE_URL}/console/tasks/${target.id}`, { waitUntil: 'load' })
    await page.waitForTimeout(3000)
    out.taskDetail = await page.evaluate(() => {
      const visible = (el) => {
        const r = el.getBoundingClientRect()
        const s = window.getComputedStyle(el)
        return (r.width > 0 || r.height > 0) && s.visibility !== 'hidden' && s.display !== 'none'
      }
      const textOf = (el) => (el.innerText || el.textContent || '').replace(/\s+/g, ' ').trim()
      const sidebar = document.querySelector('.app-layout__sidebar')
      const controls = []
      for (const el of document.querySelectorAll('button, [role="button"], a[href]')) {
        if (sidebar && sidebar.contains(el)) continue
        if (!visible(el)) continue
        const t = textOf(el)
        if (!t) continue
        controls.push({ tag: el.tagName.toLowerCase(), text: t })
      }
      // 阶段卡片：「第 N 步：xxx」
      const bodyText = textOf(document.body)
      const stageCards = (bodyText.match(/第 [1-5] 步：[^ ]+/g) || [])
      const title = document.querySelector('.console-page-title')
      return {
        finalPath: window.location.pathname,
        title: title ? textOf(title) : '',
        stageCards: [...new Set(stageCards)],
        controls: controls.slice(0, 40),
      }
    })
    out.taskDetail.datasetId = target.id
    out.taskDetail.datasetStatus = target.status
  }

  // ---- 3. 从任务详情出发的实际跳转目标（逐个点阶段卡片）----
  if (out.taskDetail && out.taskDetail.datasetId) {
    const cardMap = []
    for (const label of ['第 1 步：主题结构', '第 2 步：问题生成', '第 3 步：答案内容', '第 4 步：质量评估', '第 5 步：导出交付']) {
      await page.goto(`${BASE_URL}/console/tasks/${out.taskDetail.datasetId}`, { waitUntil: 'load' })
      await page.waitForTimeout(2000)
      const card = page.locator(`text=${label}`).first()
      if ((await card.count()) === 0) { cardMap.push({ label, target: null, note: '卡片不存在' }); continue }
      try {
        await card.click()
        await page.waitForTimeout(1200)
        cardMap.push({ label, target: new URL(page.url()).pathname })
      } catch (error) {
        cardMap.push({ label, target: null, note: String(error?.message ?? error).slice(0, 120) })
      }
    }
    out.stageCardTargets = cardMap
  }
} catch (error) {
  out.error = String(error?.message ?? error)
} finally {
  const file = path.join(ARTIFACT_DIR, 'hub-and-form.json')
  writeFileSync(file, JSON.stringify(out, null, 2))
  console.log(`结果：${file}`)
  console.log(JSON.stringify(out, null, 2).slice(0, 3000))
  await browser.close()
}
