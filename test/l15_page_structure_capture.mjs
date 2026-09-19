/**
 * R17 页面结构采集器（真实浏览器，产出 #90 页面设计说明书的**唯一事实来源**）。
 *
 * 运行：
 *   node test/l15_page_structure_capture.mjs                       # 默认 http://127.0.0.1:18195
 *   BASE_URL=http://127.0.0.1:3210 node test/l15_page_structure_capture.mjs
 *
 * ---------------------------------------------------------------------------
 * 为什么必须用真实浏览器
 * ---------------------------------------------------------------------------
 * #90 要求「逐页列出：页面 → 入口 → 主要操作 → 操作后去哪 → 空状态与失败状态文案」。
 * 这些内容**无法从源码可靠推断**：
 *   - 按钮是否真的可见，取决于 isAdmin / 是否有 activeDataset / 折叠状态；
 *   - 跳转目标由 onClick 里的 navigate(...) 决定，散落在各 render 函数里；
 *   - 空状态文案取决于运行时数据，同一个页面在有/无任务时不一样。
 * 因此本脚本真实渲染每个路由，采集**实际可见**的按钮与标题，落盘为 JSON 供文档引用。
 *
 * 不引入 npm 依赖：playwright 从全局路径 require（本机已装 Chromium）。
 * 缺 playwright/Chromium 时**明确失败并给出安装命令**，不静默跳过（避免假绿）。
 *
 * 产物：test/artifacts/page-structure.json（+ 截图）。该目录已 gitignore。
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

const results = []
const failures = []

function record(name, ok, detail) {
  results.push({ name, ok, detail })
  console.log(`[${ok ? 'PASS' : 'FAIL'}] ${name}: ${detail}`)
  if (!ok) failures.push(name)
}

// 19 个路由：9 用户可见 + 5 阶段页 + 5 管理员配置（#90 的页面总览表）
const ROUTES = [
  { path: '/console/home', kind: 'user', label: '工作台' },
  { path: '/console/planning', kind: 'user', label: '新建任务' },
  { path: '/console/tasks', kind: 'user', label: '我的任务' },
  { path: '/console/results', kind: 'user', label: '数据资产' },
  { path: '/console/evaluation', kind: 'user', label: '质量评估（多模型互评）' },
  { path: '/console/cleaning', kind: 'user', label: '数据清洗' },
  { path: '/console/help', kind: 'user', label: '账户与帮助' },
  { path: '/console/operations', kind: 'admin', label: '运营监控' },
  { path: '/console/domains', kind: 'stage', label: '阶段1 主题结构' },
  { path: '/console/questions', kind: 'stage', label: '阶段2 问题生成' },
  { path: '/console/reasoning', kind: 'stage', label: '阶段3 答案内容' },
  { path: '/console/rewards', kind: 'stage', label: '阶段4 质量评分' },
  { path: '/console/exports', kind: 'stage', label: '阶段5 导出交付' },
  { path: '/console/admin/providers', kind: 'admin', label: 'AI 服务' },
  { path: '/console/admin/storage', kind: 'admin', label: '结果存储' },
  { path: '/console/admin/strategies', kind: 'admin', label: '生成规则' },
  { path: '/console/admin/prompts', kind: 'admin', label: '生成指令' },
  { path: '/console/admin/audit', kind: 'admin', label: '操作记录' },
]

/**
 * 采集一个页面的真实可见结构。
 *
 * 「可见操作控件」的判定：取所有 button / [role=button] / 侧边栏外的 a，
 * 过滤掉不可见与空文案的，再按文案去重。
 */
async function capturePage(page, route) {
  await page.goto(`${BASE_URL}${route.path}`, { waitUntil: 'load' })
  // 等 React 挂载 + bootstrap 请求完成（有任务时详情页会多一次工作区拉取）
  await page.waitForTimeout(2500)

  const info = await page.evaluate(() => {
    const visible = (el) => {
      const rect = el.getBoundingClientRect()
      if (rect.width === 0 && rect.height === 0) return false
      const style = window.getComputedStyle(el)
      return style.visibility !== 'hidden' && style.display !== 'none' && style.opacity !== '0'
    }
    const textOf = (el) => (el.innerText || el.textContent || '').replace(/\s+/g, ' ').trim()

    // 侧边栏之外的操作控件（侧边栏导航单独采集，否则每页都会混入全站导航）
    const sidebar = document.querySelector('.app-layout__sidebar')
    const inSidebar = (el) => sidebar && sidebar.contains(el)

    const controls = []
    for (const el of document.querySelectorAll('button, [role="button"], a[href]')) {
      if (inSidebar(el)) continue
      if (!visible(el)) continue
      const text = textOf(el)
      if (!text) continue
      const tag = el.tagName.toLowerCase()
      const href = el.getAttribute('href') || ''
      const disabled = el.disabled === true || el.getAttribute('aria-disabled') === 'true'
      controls.push({ text, tag, href, disabled })
    }
    // 按 文案+tag 去重
    const seen = new Set()
    const uniqueControls = []
    for (const c of controls) {
      const key = `${c.tag}|${c.text}|${c.href}`
      if (seen.has(key)) continue
      seen.add(key)
      uniqueControls.push(c)
    }

    const titleEl = document.querySelector('.console-page-title')
    const badgeEl = document.querySelector('.console-page-badge, [class*="badge"]')

    // 空状态文案：Semi UI 的 Empty 组件
    const emptyEls = Array.from(document.querySelectorAll('[class*="semi-empty"]'))
      .filter(visible)
      .map(textOf)
      .filter(Boolean)
      .slice(0, 4)

    // 表格/卡片容器里可见的「数据存在」证据：行数或记录条数文案
    const hasData = document.querySelectorAll('.semi-table-row').length

    return {
      finalPath: window.location.pathname,
      title: titleEl ? textOf(titleEl) : '',
      badge: badgeEl ? textOf(badgeEl).slice(0, 80) : '',
      controls: uniqueControls,
      emptyTexts: emptyEls,
      tableRows: hasData,
      bodyHead: textOf(document.body).slice(0, 400),
    }
  })

  return { route, ...info }
}

async function login(page) {
  await page.goto(`${BASE_URL}/login`, { waitUntil: 'load' })
  await page.locator('input').first().fill(ADMIN_EMAIL)
  await page.locator('input[type=password]').fill(ADMIN_PASSWORD)
  await page.locator('button[type=submit], .semi-button-primary').first().click()
  await page.waitForURL(/\/console\//, { timeout: 20000 })
  await page.waitForTimeout(1500)
}

async function main() {
  const require = createRequire(path.join(REPO_ROOT, 'package.json'))
  let chromium
  try {
    ;({ chromium } = require('/root/.pi/agent/npm/node_modules/playwright'))
  } catch {
    try {
      ;({ chromium } = require('playwright'))
    } catch {
      console.error('缺少 playwright。安装：')
      console.error('  cd /root/.pi/agent/npm/node_modules/playwright && node cli.js install --with-deps chromium')
      process.exit(2)
    }
  }

  // CI 可执行性是硬要求：CI 的 Backend/Frontend job 都不起容器，
  // 因此这里先探活，探不到就**明确跳过**（exit 0 + 打印复现命令），
  // 而不是把「环境不可达」报成「采集失败」。
  // 采集器的定位是「产出文档事实来源」，不是一个回归守卫，因此跳过不影响 CI 正确性。
  const reachable = await fetch(`${BASE_URL}/login`, { method: 'GET' })
    .then((r) => r.ok || r.status === 200)
    .catch(() => false)
  if (!reachable) {
    console.log(`[SKIP] 采集需要真实前端服务，${BASE_URL} 当前不可达。`)
    console.log('       本地复现（3 步）：')
    console.log('         npm run build -w apps/web-user')
    console.log('         docker run -d --name l15-r17-web --network llm_default -p 18195:80 \\')
    console.log('           -v "$PWD/apps/web-user/dist:/usr/share/nginx/html:ro" \\')
    console.log('           -v "$PWD/deployments/docker/nginx/web-user.conf:/etc/nginx/conf.d/default.conf:ro" nginx:alpine')
    console.log('         node test/l15_page_structure_capture.mjs')
    process.exit(0)
  }

  mkdirSync(ARTIFACT_DIR, { recursive: true })
  const browser = await chromium.launch({ headless: true })
  const context = await browser.newContext({ viewport: { width: 1440, height: 960 }, locale: 'zh-CN' })
  const page = await context.newPage()

  // 侧边栏结构（两套导航体系之一）
  let sidebar = null
  const captured = []
  try {
    await login(page)
    record('登录成功', true, `${BASE_URL} 已进入控制台`)

    sidebar = await page.evaluate(() => {
      const root = document.querySelector('.app-layout__sidebar')
      if (!root) return null
      const items = []
      for (const el of root.querySelectorAll('.semi-navigation-item, .semi-navigation-sub-title')) {
        const text = (el.innerText || '').replace(/\s+/g, ' ').trim()
        if (text) items.push(text)
      }
      return { rawText: (root.innerText || '').replace(/\n+/g, '\n').trim(), items }
    })
    record('采集到侧边栏结构', Boolean(sidebar && sidebar.items.length > 0),
      sidebar ? `${sidebar.items.length} 个导航项/分组标题` : '未找到 .app-layout__sidebar')

    for (const route of ROUTES) {
      const info = await capturePage(page, route)
      captured.push(info)
      const redirected = info.finalPath !== route.path
      record(`采集 ${route.path}`, true,
        `标题「${info.title}」控件 ${info.controls.length} 个` + (redirected ? ` ⚠️ 实际落在 ${info.finalPath}` : ''))
      if (redirected) {
        // 重定向是重要事实（例如未选中任务时的兜底），记录但不判失败
        console.log(`      ↳ 重定向：${route.path} → ${info.finalPath}`)
      }
    }
  } catch (error) {
    record('采集过程未抛异常', false, String(error?.message ?? error))
  } finally {
    const report = {
      baseUrl: BASE_URL,
      capturedAt: new Date().toISOString(),
      browser: browser.version(),
      sidebar,
      pages: captured,
    }
    const out = path.join(ARTIFACT_DIR, 'page-structure.json')
    writeFileSync(out, JSON.stringify(report, null, 2))
    console.log(`\n采集结果：${out}`)
    await browser.close()
  }

  console.log('')
  if (failures.length > 0) {
    console.error(`PAGE CAPTURE FAILED: ${failures.length}/${results.length} 项未通过 -> ${failures.join(', ')}`)
    process.exitCode = 1
  } else {
    console.log(`PAGE CAPTURE OK: ${results.length}/${results.length} 项采集完成`)
  }
}

await main()
