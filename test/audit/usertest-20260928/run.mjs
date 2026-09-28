/**
 * 2026-09-28 真实浏览器全量系统使用测试（业务流程 / 视觉 / 布局 / 易用性 / 假数据）。
 *
 * 与既有 test/audit/capture.mjs 的分工：
 *   - capture.mjs：只采集「每一页长什么样」（截图 + 结构化 DOM 指标）。
 *   - 本脚本：在采集之上加**判定**：溢出裁剪、控件重叠、英文枚举/JSON/Markdown
 *     泄漏、假数据信号、死按钮、页面超长，并逐条落到可复现的复现步骤。
 *
 * 产物：test/artifacts/usertest-20260928/*.png + findings.json + REPORT.md
 */
import { mkdirSync, writeFileSync } from 'node:fs'
import { createRequire } from 'node:module'
import path from 'node:path'
import { fileURLToPath } from 'node:url'

const require = createRequire('/root/llm/package.json')
const { chromium } = require('/root/.pi/agent/npm/node_modules/playwright')

const HERE = path.dirname(fileURLToPath(import.meta.url))
const OUT = HERE
const BASE = process.env.BASE_URL ?? 'http://127.0.0.1:3210'
const EMAIL = process.env.ADMIN_EMAIL ?? 'admin@company.com'
const PASSWORD = process.env.ADMIN_PASSWORD ?? 'admin123456'
const PROJECT_ID = process.env.PROJECT_ID ?? '1'

mkdirSync(OUT, { recursive: true })

const findings = []
const routes = []

function addFinding(f) {
  findings.push(f)
  console.log(`  [FINDING:${f.severity}] ${f.id} ${f.title}`)
}

const browser = await chromium.launch({ headless: true })
const ctx = await browser.newContext({
  viewport: { width: 1600, height: 1000 },
  locale: 'zh-CN',
  deviceScaleFactor: 1,
})
const page = await ctx.newPage()

let events = []
page.on('console', (m) => { if (m.type() === 'error') events.push({ kind: 'console', text: m.text().slice(0, 300) }) })
page.on('pageerror', (e) => events.push({ kind: 'pageerror', text: String(e).slice(0, 300) }))
page.on('response', (r) => {
  if (r.status() >= 400) events.push({ kind: 'http', status: r.status(), url: r.url().replace(BASE, '') })
})

// ---------- 登录 ----------
await page.goto(`${BASE}/login`, { waitUntil: 'networkidle' })
await page.getByPlaceholder('请输入邮箱').fill(EMAIL)
await page.getByPlaceholder('请输入密码').fill(PASSWORD)
await page.getByRole('button', { name: '进入今日工作' }).click()
await page.waitForURL(/\/today/, { timeout: 20000 })
await page.waitForTimeout(1500)

let shotN = 0
async function shot(label) {
  const f = path.join(OUT, `${String(++shotN).padStart(2, '0')}-${label}.png`)
  await page.screenshot({ path: f, fullPage: true })
  return path.basename(f)
}

/**
 * 页面级 DOM 事实采集。返回可序列化对象，全部在浏览器上下文内算好，
 * 避免把 DOM 节点搬出来。
 */
const PROBE = () => {
  const txt = (el) => (el?.innerText ?? el?.textContent ?? '').trim()
  const visible = (el) => {
    if (!el) return false
    const r = el.getBoundingClientRect()
    const s = getComputedStyle(el)
    return r.width > 1 && r.height > 1 && s.visibility !== 'hidden' && s.display !== 'none' && Number(s.opacity) > 0.05
  }
  const interactives = [...document.querySelectorAll('button, a[href], input, select, textarea, [role="button"], [role="tab"], .semi-select, .semi-switch')].filter(visible)

  // 1) 文本被裁剪：内容宽度显著超出可视宽度且 overflow 不是可见
  const clipped = []
  for (const el of document.querySelectorAll('*')) {
    if (!visible(el)) continue
    if (el.children.length > 0) continue
    const t = txt(el)
    if (!t || t.length < 6) continue
    const s = getComputedStyle(el)
    const ov = `${s.overflow}${s.overflowX}${s.overflowY}`
    if (!/hidden|clip/.test(ov)) continue
    if (el.scrollWidth > el.clientWidth + 8) {
      clipped.push({
        tag: el.tagName.toLowerCase(),
        cls: (el.className || '').toString().slice(0, 80),
        text: t.slice(0, 60),
        scrollWidth: el.scrollWidth,
        clientWidth: el.clientWidth,
      })
    }
  }

  // 2) 元素溢出视口右边界（横向滚动条来源）
  const bodyOverflowX = document.documentElement.scrollWidth - document.documentElement.clientWidth
  const overflowing = []
  if (bodyOverflowX > 4) {
    for (const el of document.querySelectorAll('*')) {
      if (!visible(el)) continue
      const r = el.getBoundingClientRect()
      if (r.right > document.documentElement.clientWidth + 4 && r.width > 40 && r.height > 10 && el.children.length === 0) {
        overflowing.push({
          tag: el.tagName.toLowerCase(),
          cls: (el.className || '').toString().slice(0, 80),
          text: txt(el).slice(0, 60),
          right: Math.round(r.right),
        })
      }
      if (overflowing.length > 12) break
    }
  }

  // 3) 可交互元素互相重叠（点击命中风险）
  const overlaps = []
  const seen = new Set()
  for (let i = 0; i < interactives.length; i++) {
    const a = interactives[i]
    const ra = a.getBoundingClientRect()
    if (ra.width < 8 || ra.height < 8) continue
    for (let j = i + 1; j < interactives.length; j++) {
      const b = interactives[j]
      if (a.contains(b) || b.contains(a)) continue
      const rb = b.getBoundingClientRect()
      const ox = Math.min(ra.right, rb.right) - Math.max(ra.left, rb.left)
      const oy = Math.min(ra.bottom, rb.bottom) - Math.max(ra.top, rb.top)
      if (ox > 6 && oy > 6) {
        const area = ox * oy
        if (area < 200) continue
        const key = `${txt(a).slice(0, 20)}||${txt(b).slice(0, 20)}`
        if (seen.has(key)) continue
        seen.add(key)
        overlaps.push({
          a: `${a.tagName.toLowerCase()}:${txt(a).slice(0, 25)}`,
          b: `${b.tagName.toLowerCase()}:${txt(b).slice(0, 25)}`,
          overlapArea: Math.round(area),
        })
        if (overlaps.length > 8) break
      }
    }
    if (overlaps.length > 8) break
  }

  const text = document.body.innerText
  const buttons = [...new Set(interactives.filter((e) => e.tagName === 'BUTTON' || e.getAttribute('role') === 'button').map(txt).filter(Boolean))]

  // 4) 假数据 / 泄漏信号（在可见文本里）
  const leak = {
    englishEnum: [...new Set((text.match(/\b(?:pending|queued|running|failed|completed|draft|partial_failed|awaiting_[a-z_]+|in_progress|succeeded|cancelled)[a-z_]*\b/g) ?? []))].slice(0, 20),
    snakeCase: [...new Set((text.match(/\b[a-z][a-z0-9]*_[a-z0-9_]{2,}\b/g) ?? []))].slice(0, 20),
    rawJson: (text.match(/[{[]\s*"[a-zA-Z_]+"\s*:/g) ?? []).length,
    markdownStars: (text.match(/\*\*/g) ?? []).length,
    todoWords: [...new Set((text.match(/TODO|FIXME|待实现|尚未实现|unimplemented|mock|占位/g) ?? []))].slice(0, 10),
    literalNull: [...new Set((text.match(/\b(?:undefined|NaN|null)\b/g) ?? []))].slice(0, 10),
    hardcodedZero: (text.match(/\b0\s*(?:个|条|项|%)\b/g) ?? []).slice(0, 10),
    taskCodes: [...new Set((text.match(/T\d{2}\b/g) ?? []))].slice(0, 10),
    englishSentences: [...new Set((text.match(/[A-Za-z][A-Za-z ,'-]{25,}/g) ?? []).filter((s) => !/https?:|@|\.com|SFT|GRPO|JSONL|CSV|Alpaca|ShareGPT|Atelier|Dify|Markdown/.test(s)))].slice(0, 6),
  }

  // 5) 表单控件是否有可见标签（易用性）
  const unlabeled = []
  for (const el of document.querySelectorAll('input:not([type="hidden"]), textarea, select')) {
    if (!visible(el)) continue
    const id = el.getAttribute('id')
    const hasLabel = (id && document.querySelector(`label[for="${CSS.escape(id)}"]`))
      || el.closest('label')
      || el.getAttribute('aria-label')
      || el.getAttribute('aria-labelledby')
      || el.getAttribute('placeholder')
    if (!hasLabel) {
      unlabeled.push({ tag: el.tagName.toLowerCase(), type: el.getAttribute('type') || '', name: el.getAttribute('name') || '' })
    }
  }

  return {
    title: document.title,
    url: location.pathname + location.search,
    h1: [...document.querySelectorAll('h1,h2')].filter(visible).map(txt).slice(0, 8),
    bodyText: text,
    bodyLen: text.length,
    docScrollHeight: document.documentElement.scrollHeight,
    viewportHeight: window.innerHeight,
    viewportWidth: window.innerWidth,
    pagesTall: Math.round((document.documentElement.scrollHeight / window.innerHeight) * 10) / 10,
    buttonCount: buttons.length,
    buttons,
    links: [...new Set([...document.querySelectorAll('a[href]')].filter(visible).map((a) => a.getAttribute('href')))].slice(0, 60),
    tables: document.querySelectorAll('table').length,
    rows: document.querySelectorAll('tbody tr').length,
    inputs: [...document.querySelectorAll('input,textarea,select')].filter(visible).length,
    clipped,
    overflowing,
    bodyOverflowX,
    overlaps,
    leak,
    unlabeled,
  }
}

async function visit(key, routePath, opts = {}) {
  events = []
  let status = 'ok'
  try {
    await page.goto(`${BASE}${routePath}`, { waitUntil: 'networkidle', timeout: 30000 })
  } catch (e) {
    status = `nav-failed: ${String(e).slice(0, 120)}`
  }
  await page.waitForTimeout(opts.settle ?? 1800)
  const shotFile = await shot(key)
  let info = null
  try {
    info = await page.evaluate(PROBE)
  } catch (e) {
    status = `probe-failed: ${String(e).slice(0, 120)}`
  }
  const dedup = []
  const seen = new Set()
  for (const e of events) {
    const k = JSON.stringify(e)
    if (!seen.has(k)) { seen.add(k); dedup.push(e) }
  }
  const rec = { key, routePath, finalUrl: page.url().replace(BASE, ''), status, shot: shotFile, ...(info ?? {}), events: dedup }
  delete rec.bodyText
  routes.push(rec)
  console.log(`[${status === 'ok' ? ' OK ' : 'WARN'}] ${key.padEnd(30)} h=${info?.docScrollHeight ?? '?'} (${info?.pagesTall ?? '?'}屏) btns=${info?.buttonCount ?? '?'} clip=${info?.clipped?.length ?? '?'} ovl=${info?.overlaps?.length ?? '?'} err=${dedup.length}`)
  return info
}

// =========================================================================
// 1. 全路由走查
// =========================================================================
console.log('\n=== 1. 全路由走查 ===')
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
  ['help', '/help'],
  ['catalog', '/catalog'],
  ['notfound', '/this-route-does-not-exist'],
  ['p-overview', `/p/${PROJECT_ID}/overview`],
  ['p-blueprint', `/p/${PROJECT_ID}/blueprint`],
  ['p-coverage', `/p/${PROJECT_ID}/coverage`],
  ['p-standard', `/p/${PROJECT_ID}/standard`],
  ['p-runs', `/p/${PROJECT_ID}/runs`],
  ['p-pilot', `/p/${PROJECT_ID}/pilot`],
  ['p-runnew', `/p/${PROJECT_ID}/runs/new`],
  ['p-compare', `/p/${PROJECT_ID}/compare`],
  ['p-data', `/p/${PROJECT_ID}/data`],
  ['p-review', `/p/${PROJECT_ID}/review`],
  ['p-quality', `/p/${PROJECT_ID}/quality`],
  ['p-qualitynew', `/p/${PROJECT_ID}/quality/new`],
  ['p-rules', `/p/${PROJECT_ID}/rules`],
  ['p-releases', `/p/${PROJECT_ID}/releases`],
  ['p-releasenew', `/p/${PROJECT_ID}/releases/new`],
]

const pageFacts = {}
for (const [key, p] of ROUTES) {
  pageFacts[key] = await visit(key, p)
}

// 聚合判定：溢出裁剪 / 重叠 / 泄漏
for (const [key, info] of Object.entries(pageFacts)) {
  if (!info) continue
  if (info.clipped?.length > 0) {
    const worst = info.clipped.slice(0, 5)
    addFinding({
      id: `CLIP-${key}`,
      category: '视觉/布局',
      severity: info.clipped.length >= 3 ? 'high' : 'medium',
      title: `${key} 页面存在 ${info.clipped.length} 处文本被控件边缘裁剪`,
      page: key,
      route: ROUTES.find((r) => r[0] === key)?.[1],
      evidence: worst,
      extra: `最严重示例：「${worst[0].text}」可视宽 ${worst[0].clientWidth}px / 内容宽 ${worst[0].scrollWidth}px`,
    })
  }
  if (info.bodyOverflowX > 4) {
    addFinding({
      id: `OVERFLOW-${key}`,
      category: '视觉/布局',
      severity: 'high',
      title: `${key} 页面出现横向溢出 ${info.bodyOverflowX}px（出现左右滚动条）`,
      page: key,
      route: ROUTES.find((r) => r[0] === key)?.[1],
      evidence: info.overflowing,
    })
  }
  if (info.overlaps?.length > 0) {
    addFinding({
      id: `OVERLAP-${key}`,
      category: '视觉/布局',
      severity: 'high',
      title: `${key} 页面存在 ${info.overlaps.length} 组可交互控件互相遮挡`,
      page: key,
      route: ROUTES.find((r) => r[0] === key)?.[1],
      evidence: info.overlaps,
    })
  }
  if (info.leak?.markdownStars > 0) {
    addFinding({
      id: `MD-${key}`,
      category: '视觉',
      severity: 'medium',
      title: `${key} 页面漏出 Markdown 星号 ${info.leak.markdownStars} 处`,
      page: key,
      route: ROUTES.find((r) => r[0] === key)?.[1],
      evidence: (info.bodyText?.match(/.{0,40}\*\*.{0,40}/g) ?? []).slice(0, 5),
    })
  }
  if (info.leak?.rawJson > 0) {
    addFinding({
      id: `JSON-${key}`,
      category: '视觉/易用性',
      severity: 'medium',
      title: `${key} 页面默认视图直接渲染原始 JSON（${info.leak.rawJson} 处）`,
      page: key,
      route: ROUTES.find((r) => r[0] === key)?.[1],
      evidence: info.leak.rawJson,
    })
  }
  if (info.leak?.englishEnum?.length > 0 || info.leak?.snakeCase?.length > 0) {
    addFinding({
      id: `ENUM-${key}`,
      category: '视觉',
      severity: 'medium',
      title: `${key} 页面中文界面漏出内部英文枚举/内部键`,
      page: key,
      route: ROUTES.find((r) => r[0] === key)?.[1],
      evidence: { englishEnum: info.leak.englishEnum, snakeCase: info.leak.snakeCase },
    })
  }
  if (info.leak?.taskCodes?.length > 0) {
    addFinding({
      id: `TASKCODE-${key}`,
      category: '假功能/内部信息',
      severity: 'medium',
      title: `${key} 页面向用户展示内部任务号 ${info.leak.taskCodes.join('/')}`,
      page: key,
      route: ROUTES.find((r) => r[0] === key)?.[1],
      evidence: info.leak.taskCodes,
    })
  }
  if (info.leak?.literalNull?.length > 0) {
    addFinding({
      id: `NULL-${key}`,
      category: '假数据',
      severity: 'high',
      title: `${key} 页面出现 undefined/NaN/null 字面量`,
      page: key,
      route: ROUTES.find((r) => r[0] === key)?.[1],
      evidence: info.leak.literalNull,
    })
  }
  if (info.unlabeled?.length > 0) {
    addFinding({
      id: `LABEL-${key}`,
      category: '易用性',
      severity: 'low',
      title: `${key} 页面有 ${info.unlabeled.length} 个输入控件无可见标签且无 aria-label/placeholder`,
      page: key,
      route: ROUTES.find((r) => r[0] === key)?.[1],
      evidence: info.unlabeled.slice(0, 6),
    })
  }
  for (const e of info.events ?? []) {
    if (e.kind === 'pageerror') {
      addFinding({ id: `JSERR-${key}`, category: '功能', severity: 'high', title: `${key} 页面抛出未捕获 JS 异常`, page: key, route: ROUTES.find((r) => r[0] === key)?.[1], evidence: e.text })
    }
    if (e.kind === 'http' && e.status >= 500) {
      addFinding({ id: `HTTP5XX-${key}`, category: '功能', severity: 'high', title: `${key} 页面请求 5xx：${e.url}`, page: key, route: ROUTES.find((r) => r[0] === key)?.[1], evidence: e })
    }
  }
}

writeFileSync(path.join(OUT, 'routes.json'), JSON.stringify(routes, null, 2))
console.log(`\n路由走查完成：${routes.length} 条，findings=${findings.length}`)
await browser.close()
writeFileSync(path.join(OUT, 'findings-stage1.json'), JSON.stringify(findings, null, 2))
