/**
 * 项目优先导航与待处理行动台守卫。
 * Docker: docker run --rm -v "$PWD:/w" -w /w node:22-alpine node test/l15_product_navigation.mjs
 *
 * 真实执行生产链接构造器并 SSR 渲染导航/加载态；不请求 API，不修改数据。
 * 浏览器焦点和 Dropdown 弹层仍由集成浏览器验证负责，不能由 SSR 替代。
 */
import assert from 'node:assert/strict'
import { mkdirSync, readFileSync, writeFileSync } from 'node:fs'
import { createRequire } from 'node:module'
import path from 'node:path'
import { fileURLToPath } from 'node:url'

const repoRoot = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..')
const webRoot = path.join(repoRoot, 'apps/web-user')
const require = createRequire(path.join(webRoot, 'package.json'))
const { build } = require('esbuild')
const Module = require('node:module')
const layoutPath = path.join(webRoot, 'src/studio/StudioLayout.tsx')
const todayPath = path.join(webRoot, 'src/studio/pages/TodayPages.tsx')
const layoutSource = readFileSync(layoutPath, 'utf8')
const todaySource = readFileSync(todayPath, 'utf8')
const results = []
function check(name, callback) {
  try {
    callback()
    results.push({ name, ok: true })
    console.log(`[PASS] ${name}`)
  } catch (error) {
    results.push({ name, ok: false })
    console.error(`[FAIL] ${name}: ${error.message}`)
  }
}

const compiled = await build({
  stdin: {
    contents: `
      import { createElement } from 'react'
      import { renderToStaticMarkup } from 'react-dom/server'
      import { MemoryRouter } from 'react-router-dom'
      import { StudioLayout } from ${JSON.stringify(layoutPath)}
      import { TodayPage } from ${JSON.stringify(todayPath)}
      export { overviewProjectHref, todoHref } from ${JSON.stringify(todayPath)}
      export function renderLayout(pathname = '/today') {
        return renderToStaticMarkup(createElement(MemoryRouter, { initialEntries: [pathname] },
          createElement(StudioLayout, { userEmail: 'viewer@example.com', isAdmin: false, onLogout() {} })))
      }
      export function renderToday() {
        return renderToStaticMarkup(createElement(MemoryRouter, null, createElement(TodayPage)))
      }
    `,
    resolveDir: webRoot,
    loader: 'tsx',
  },
  bundle: true,
  format: 'cjs',
  platform: 'node',
  jsx: 'automatic',
  write: false,
  absWorkingDir: webRoot,
  nodePaths: [path.join(webRoot, 'node_modules'), path.join(repoRoot, 'node_modules')],
  mainFields: ['module', 'main'],
  loader: { '.css': 'empty' },
  define: { 'import.meta.env': JSON.stringify({ PROD: true, MODE: 'production' }) },
  logLevel: 'error',
})
const mod = new Module.Module('product-navigation')
mod.paths = Module.Module._nodeModulePaths(webRoot)
mod._compile(compiled.outputFiles[0].text, path.join(webRoot, 'product-navigation.cjs'))
const { overviewProjectHref, todoHref, renderLayout, renderToday } = mod.exports
const scopedOverview = (projectCount, scopedProjectIds) => ({ projectCount, scopedProjectIds })
const todo = (kind, projectId, links = {}) => ({ kind, projectId, links, count: 2, summary: '待处理', updatedAt: '2026-10-09T08:00:00Z' })

check('单项目审阅直达正确项目', () => {
  assert.equal(overviewProjectHref(scopedOverview(1, [7]), 'project.review'), '/p/7/review')
})
check('多项目审阅先选项目但保留目标任务', () => {
  const destination = new URL(overviewProjectHref(scopedOverview(2, [7, 9]), 'project.review'), 'https://test.invalid')
  assert.equal(destination.pathname, '/projects')
  assert.equal(destination.searchParams.get('next'), 'project.review')
})
check('项目范围不一致时不猜测项目', () => {
  for (const overview of [scopedOverview(0, []), scopedOverview(1, []), scopedOverview(1, [7, 9]), scopedOverview(2, [7])]) {
    const destination = new URL(overviewProjectHref(overview, 'project.runs'), 'https://test.invalid')
    assert.equal(destination.pathname, '/projects')
    assert.equal(destination.searchParams.get('next'), 'project.runs')
  }
})
check('生产、数据、发布聚合入口分别保留各自任务', () => {
  for (const routeKey of ['project.runs', 'project.data', 'project.releases']) {
    const destination = new URL(overviewProjectHref(scopedOverview(3, [1, 2, 3]), routeKey), 'https://test.invalid')
    assert.equal(destination.searchParams.get('next'), routeKey)
  }
})
check('待办优先保留服务端对象深链', () => {
  assert.equal(todoHref(todo('release_blocked', 7, { page: '/p/7/releases/l_2' })), '/p/7/releases/l_2')
})
check('缺少深链的已知待办仍可继续处理', () => {
  assert.equal(todoHref(todo('pending_review', 7)), '/p/7/review')
  assert.equal(todoHref(todo('pilot_comparable', 7)), '/p/7/compare')
  assert.equal(todoHref(todo('failed_recovery', 7)), '/p/7/runs')
  assert.equal(todoHref(todo('release_blocked', 7)), '/p/7/releases')
  assert.equal(todoHref(todo('pending_review', 0)), '/projects?next=project.review')
  assert.equal(todoHref(todo('unread_activity', 0)), '/activity')
  assert.equal(todoHref(todo('unknown_kind', 0)), undefined)
})

// React Router 的 SSR useLayoutEffect 提示不是页面错误，只在这段渲染中抑制它。
const originalError = console.error
let layoutHTML
let projectHTML
let todayHTML
try {
  console.error = (...args) => {
    if (String(args[0]).includes('useLayoutEffect does nothing on the server')) return
    originalError(...args)
  }
  layoutHTML = renderLayout()
  projectHTML = renderLayout('/p/7/blueprint')
  todayHTML = renderToday()
} finally {
  console.error = originalError
}
check('全局导航以项目为先，只展示四个业务入口', () => {
  const primaryNavigation = layoutHTML.match(/<div class="sidebar-nav-section studio-primary-navigation">([\s\S]*?)<\/div>/)?.[1] ?? ''
  const labels = [...primaryNavigation.matchAll(/class="sidebar-nav-item__label">([^<]+)</g)].map((match) => match[1])
  assert.deepEqual(labels, ['数据项目', '待处理', '方案库', '交付库'])
  assert(!primaryNavigation.includes('评估工作台'))
  assert(!primaryNavigation.includes('连接设置'))
})
check('项目深链保持全局数据项目导航选中', () => {
  assert.match(projectHTML, /aria-current="page"[^>]*class="sidebar-nav-item sidebar-nav-item--active"[^>]*href="\/projects"|class="sidebar-nav-item sidebar-nav-item--active"[^>]*href="\/projects"/)
})
check('工具菜单有可访问名称和真实辅助路由接线', () => {
  assert.match(layoutHTML, /aria-label="工具与设置"/)
  // Semi Dropdown 会把 trigger 的 menu 改写为 true；两者均表示菜单弹层。
  assert.match(layoutHTML, /aria-haspopup="(?:menu|true)"/)
  assert.match(layoutSource, /toolRoutes\.map/)
  assert.match(layoutSource, /navigate\(route\.path\)/)
  assert.match(layoutSource, /<Dropdown\.Menu data-studio-tools-menu="true">/)
  assert.match(layoutSource, /active=\{activeKey === route\.key\}/)
})
check('桌面导航不存在收起逻辑或侧栏宽度状态', () => {
  assert(!layoutSource.includes('collapsed'))
  assert(!layoutHTML.includes('收起导航'))
  assert(!layoutHTML.includes('展开导航'))
})
check('移动抽屉保留焦点进入、Escape返回和遮罩关闭', () => {
  assert.match(layoutSource, /mobileNavFocusTimer/)
  assert.match(layoutSource, /#studio-main-navigation a\[href\]/)
  assert.match(layoutSource, /mobileMenuRef\.current\?\.focus\(\)/)
  assert.match(layoutSource, /if \(event\.key !== 'Escape'\) return/)
  assert.match(layoutSource, /className="atelier-mobile-backdrop"/)
})
check('工具菜单支持方向键打开、Escape关闭并恢复焦点', () => {
  assert.match(layoutSource, /event\.key !== 'ArrowDown'/)
  assert.match(layoutSource, /\[data-studio-tools-menu\] \[role="menuitem"\]/)
  assert.match(layoutSource, /toolsTriggerRef\.current\?\.focus\(\)/)
  assert.match(layoutSource, /event\.stopPropagation\(\)/)
})
check('角色展示、退出清理和命令搜索没有丢失', () => {
  assert.match(layoutHTML, /普通用户/)
  assert.match(layoutHTML, /aria-label="退出登录"/)
  assert.match(layoutSource, /clearForActor\(actorId\)/)
  assert.equal((layoutSource.match(/<CommandSearch\s*\/>/g) ?? []).length, 1)
})
check('待处理首屏是行动队列，不渲染营销和重复信息', () => {
  assert.match(todayHTML, /<h1>待处理<\/h1>/)
  assert.match(todayHTML, /待办队列/)
  assert.match(todayHTML, /正在汇总待办/)
  for (const removed of ['atelier-today-hero', 'atelier-calendar-panel', 'atelier-workstyle-panel', 'atelier-continue-panel', '把下一份训练数据']) {
    assert(!todaySource.includes(removed), removed)
  }
})
check('动态默认折叠，标记已读仍只调用动态接口', () => {
  assert.match(todaySource, /<details className="atelier-activity-disclosure/)
  assert.match(todaySource, /data-today-mark-read="true"/)
  assert.match(todaySource, /await activityApi\.markRead\(\)/)
  assert.match(todaySource, /todos\.filter\(\(todo\) => todo\.kind !== 'unread_activity'\)/)
})
check('事实磁贴保留服务端计数，交付与阻塞保持两个目标', () => {
  for (const field of ['projectCount', 'runningBatches', 'batchesWithShortfall', 'pendingReview', 'producedLast7Days', 'publishedReleases', 'blockedReleases']) {
    assert(todaySource.includes(`overview.${field}`), field)
  }
  assert(todaySource.includes("href={studioPath('deliveries')}"))
  assert(todaySource.includes("href={overviewProjectHref(overview, 'project.releases')}"))
})

if (process.argv.includes('--with-browser')) {
  const playwrightModule = process.env.PLAYWRIGHT_MODULE ?? 'playwright'
  const playwright = await import(playwrightModule)
  const browser = await (playwright.default ?? playwright).chromium.launch({ headless: true, executablePath: process.env.PLAYWRIGHT_EXECUTABLE, args: ['--no-sandbox'] })
  const baseURL = process.env.PRODUCT_UI_BASE_URL ?? 'http://127.0.0.1:13211'
  const page = await browser.newPage({ viewport: { width: 1440, height: 960 } })
  const pageErrors = []
  page.on('pageerror', (error) => pageErrors.push(error.message))
  let readRequests = 0
  let hasUnread = true
  let todayFailure = false
  const project = {
    id: 'p_7', status: 'review', revision: 1, updatedAt: '2026-10-09T08:00:00Z',
    capabilities: { canEdit: true, canRun: true, canPublish: true },
    data: { id: 7, name: '导航测试项目', goal: '测试主线入口', targetKind: 'sft', status: 'review', domainCount: 1, directionsPerDomain: 1, questionsPerDirection: 2, pilotSize: 2 },
  }
  const workspace = {
    projectCount: 2, scopedProjectIds: [7, 9], runningBatches: 1,
    batchesWithShortfall: 1, totalPlannedUnits: 20, totalCompletedUnits: 12,
    pendingReview: 8, producedLast7Days: 12, publishedReleases: 3, blockedReleases: 1,
  }
  const pending = todo('pending_review', 7, { page: '/p/7/review' })
  const blocked = todo('release_blocked', 7, { page: '/p/7/releases' })
  const unread = todo('unread_activity', 0, { page: '/activity' })
  // Vite 源文件 /src/lib/api/studio.ts 不是 HTTP API，不能被 mock 捕获。
  await page.route('**/api/v1/**', async (route) => {
    const url = new URL(route.request().url())
    const pathname = url.pathname.replace(/^\/api/, '')
    let payload = {}
    let status = 200
    if (pathname === '/v1/auth/me') payload = { user: { id: 1, email: 'viewer@example.com', role: 'user' } }
    else if (pathname === '/v1/datasets') payload = []
    else if (pathname === '/v1/legacy/migration-status') payload = { scope: 'visible_legacy_assets', migrationComplete: false, legacyDatasets: 2, pendingDatasets: 1 }
    else if (pathname === '/v1/today') {
      if (todayFailure) { status = 503; payload = { message: '测试待办加载失败' } }
      else payload = { todos: [pending, blocked, ...(hasUnread ? [unread] : [])], notes: ['动态已读不会改变待办。'], overview: workspace }
    } else if (pathname === '/v1/activity/read') { readRequests += 1; hasUnread = false; payload = { watermark: {}, notes: [] } }
    else if (pathname === '/v1/projects') payload = { items: [project], nextCursor: '', sortKey: 'updated_at' }
    else if (/^\/v1\/projects\/(?:p_)?7$/.test(pathname)) payload = project
    else if (pathname.endsWith('/overview')) payload = { ...project, data: { ...project.data, versions: {}, batches: { total: 1, pilot: 1, scale: 0, running: 0, paused: 0, failed: 0, completed: 1 }, budget: { settledMinor: 0, uncertainMinor: 0, reservedMinor: 0 }, nextAction: { kind: 'review', message: '完成审阅', href: '/p/7/review' }, stats: { plannedQuestions: 2, generated: 2, pendingReview: 2, accepted: 0, acceptanceRateDisplay: '暂无接纳结论' } } }
    else if (pathname.endsWith('/samples')) payload = { items: [], nextCursor: '' }
    else if (pathname === '/v1/activity') payload = { items: [], nextCursor: '', notes: [] }
    else if (pathname === '/v1/settings/providers' || pathname === '/v1/settings/storages') payload = { items: [] }
    return route.fulfill({ status, contentType: 'application/json', body: JSON.stringify(payload) })
  })
  async function browserCheck(name, callback) {
    try { await callback(); results.push({ name, ok: true }); console.log(`[PASS] ${name}`) }
    catch (error) { results.push({ name, ok: false }); console.error(`[FAIL] ${name}: ${error.message}`) }
  }
  const waitFor = (callback) => page.waitForFunction(callback)
  try {
    await page.goto(`${baseURL}/today`, { waitUntil: 'domcontentloaded' })
    await page.locator('[data-today-todos]').waitFor({ timeout: 10000 }).catch(async (error) => {
      throw new Error(`${error.message}; URL=${page.url()}; pageErrors=${JSON.stringify(pageErrors)}; body=${(await page.locator('body').innerText()).slice(0, 800)}`)
    })
    await browserCheck('浏览器：桌面四个主入口完整可见，不被工具区覆盖或横向裁切', async () => {
      assert.ok(await page.evaluate(() => document.documentElement.scrollWidth - innerWidth) <= 1, 'desktop page must not overflow horizontally')
      const navigation = page.locator('.studio-primary-navigation')
      const bounds = await navigation.boundingBox()
      assert.ok(bounds, 'primary navigation must be visible')
      for (const name of ['数据项目', '待处理', '方案库', '交付库']) {
        const link = navigation.getByRole('link', { name, exact: true })
        const rect = await link.boundingBox()
        assert.ok(rect && rect.x >= bounds.x - 1 && rect.x + rect.width <= bounds.x + bounds.width + 1, `${name}: clipped primary entry; link=${JSON.stringify(rect)}, navigation=${JSON.stringify(bounds)}`)
        assert.equal(await link.evaluate((anchor) => {
          const r = anchor.getBoundingClientRect()
          return document.elementFromPoint(r.x + r.width / 2, r.y + r.height / 2)?.closest('a') === anchor
        }), true, `${name}: another header region covers the main entry`)
      }
    })
    await browserCheck('浏览器：汇总待审阅跳转后选项目进入审阅，不回概览', async () => {
      assert.equal(await page.locator('[data-overview-tile="pending"]').getAttribute('href'), '/projects?next=project.review')
      await page.locator('[data-overview-tile="pending"]').click()
      await page.waitForURL('**/projects?next=project.review')
      await page.getByRole('button', { name: /打开项目 导航测试项目/ }).click()
      await page.waitForURL(/\/p\/(?:p_)?7\/review$/)
      await page.locator('[data-studio-page="review-queue"]').waitFor()
    })
    await page.goto(`${baseURL}/today`, { waitUntil: 'domcontentloaded' })
    await page.locator('[data-today-todos]').waitFor()
    await browserCheck('浏览器：动态折叠与全部已读不会清空业务待办', async () => {
      const disclosure = page.locator('[data-today-unread]')
      assert.equal(await disclosure.getAttribute('open'), null)
      await disclosure.locator('summary').click()
      await page.locator('[data-today-mark-read]').click()
      await waitFor(() => !document.querySelector('[data-today-mark-read]'))
      assert.equal(readRequests, 1)
      assert.equal(await page.locator('[data-todo-kind="pending_review"]').count(), 1)
      assert.equal(await page.locator('[data-todo-kind="release_blocked"]').count(), 1)
    })
    await browserCheck('浏览器：工具菜单支持键盘打开和Escape焦点返回', async () => {
      await page.getByRole('button', { name: '工具与设置' }).focus()
      await page.keyboard.press('ArrowDown')
      await page.locator('[data-studio-tools-menu]').waitFor()
      await waitFor(() => document.activeElement?.getAttribute('role') === 'menuitem')
      for (const name of ['评估工作台', '清洗工作台', '历史资产', '连接设置', '成员与角色', '帮助']) {
        assert.equal(await page.getByRole('menuitem', { name, exact: true }).count(), 1)
      }
      await page.keyboard.press('Escape')
      await waitFor(() => document.activeElement?.getAttribute('aria-label') === '工具与设置')
      assert.equal(await page.getByRole('button', { name: '工具与设置' }).getAttribute('aria-expanded'), 'false')
    })
    await browserCheck('浏览器：工具菜单动态入口可实际导航', async () => {
      await page.getByRole('button', { name: '工具与设置' }).click()
      await page.getByRole('menuitem', { name: '动态', exact: true }).click()
      await page.waitForURL('**/activity')
      await page.locator('[data-studio-page="activity"]').waitFor()
    })
    await page.setViewportSize({ width: 390, height: 844 })
    await page.goto(`${baseURL}/today`, { waitUntil: 'domcontentloaded' })
    await page.locator('[data-today-todos]').waitFor()
    await browserCheck('浏览器：390px待处理布局无页面横向溢出', async () => {
      assert.ok(await page.evaluate(() => document.documentElement.scrollWidth - innerWidth) <= 1)
    })
    await browserCheck('浏览器：移动抽屉进入焦点、Escape关闭并恢复菜单焦点', async () => {
      const drawer = page.locator('#studio-main-navigation')
      assert.equal(await drawer.getAttribute('data-mobile-open'), 'false')
      assert.equal(await drawer.evaluate((element) => getComputedStyle(element).visibility), 'hidden')
      await page.getByRole('button', { name: '打开主导航', exact: true }).focus()
      for (let step = 0; step < 12; step++) {
        await page.keyboard.press('Tab')
        assert.equal(await drawer.evaluate((element) => element.contains(document.activeElement)), false, 'closed drawer must not receive Tab focus')
      }
      await page.getByRole('button', { name: '打开主导航', exact: true }).click()
      await waitFor(() => document.querySelector('#studio-main-navigation')?.contains(document.activeElement))
      assert.equal(await drawer.getAttribute('data-mobile-open'), 'true')
      await page.keyboard.press('Escape')
      await waitFor(() => document.activeElement?.getAttribute('aria-label') === '打开主导航')
      assert.equal(await drawer.getAttribute('data-mobile-open'), 'false')
      await page.getByRole('button', { name: '打开主导航', exact: true }).click()
      await page.locator('.atelier-mobile-backdrop').click({ position: { x: 380, y: 700 } })
      assert.equal(await drawer.getAttribute('data-mobile-open'), 'false')
    })
    await browserCheck('浏览器：待办刷新失败提供错误与重试，不伪装为空待办', async () => {
      todayFailure = true
      await page.getByRole('button', { name: '刷新待办', exact: true }).click()
      await page.locator('[data-today-error]').waitFor()
      assert.equal(await page.locator('[data-today-empty]').count(), 0)
      todayFailure = false
      await page.locator('[data-today-error]').getByRole('button', { name: '重试' }).click()
      await waitFor(() => !document.querySelector('[data-today-error]'))
    })
    const artifactDir = path.join(repoRoot, 'output/playwright/product-navigation')
    mkdirSync(artifactDir, { recursive: true })
    await page.screenshot({ path: path.join(artifactDir, 'today-mobile.png'), fullPage: true })
    await page.setViewportSize({ width: 1440, height: 960 })
    await page.evaluate(() => new Promise((resolve) => requestAnimationFrame(() => requestAnimationFrame(resolve))))
    const responsiveLayout = await page.evaluate(() => {
      const describe = (selector) => {
        const element = document.querySelector(selector)
        if (!element) return null
        const style = getComputedStyle(element), rect = element.getBoundingClientRect()
        return { selector, rect: { x: rect.x, y: rect.y, width: rect.width, height: rect.height }, width: style.width, flex: style.flex,
          gridTemplateColumns: style.gridTemplateColumns, gridTemplateAreas: style.gridTemplateAreas, display: style.display, position: style.position }
      }
      return ['.atelier-shell', '.app-layout__sidebar', '.studio-primary-navigation', '.sidebar-nav-section', '.sidebar-footer'].map(describe)
    })
    console.log(`Responsive layout: ${JSON.stringify(responsiveLayout)}`)
    writeFileSync(path.join(artifactDir, 'responsive-layout.json'), JSON.stringify(responsiveLayout, null, 2) + '\n')
    await page.waitForFunction(() => {
      const sidebar = document.querySelector('.app-layout__sidebar')
      return sidebar && Math.abs(sidebar.getBoundingClientRect().width - innerWidth) <= 1
    })
    await browserCheck('浏览器：移动转桌面后四个主入口仍完整可见', async () => {
      const layout = await page.locator('.studio-primary-navigation a').evaluateAll((anchors) => anchors.map((anchor) => {
        const rect = anchor.getBoundingClientRect()
        const sidebar = anchor.closest('.app-layout__sidebar').getBoundingClientRect()
        return { label: anchor.textContent, x: rect.x, width: rect.width, sidebarWidth: sidebar.width,
          visible: rect.x >= sidebar.x - 1 && rect.right <= sidebar.right + 1 && document.elementFromPoint(rect.x + rect.width / 2, rect.y + rect.height / 2)?.closest('a') === anchor }
      }))
      assert.equal(layout.length, 4)
      assert.ok(layout.every((entry) => entry.visible), `responsive navigation clipped: ${JSON.stringify(layout)}`)
    })
    await page.screenshot({ path: path.join(artifactDir, 'today-desktop.png'), fullPage: true })
    await browserCheck('浏览器：退出调用既有退出流程并回登录页', async () => {
      await page.getByRole('button', { name: '退出登录', exact: true }).click()
      await page.waitForURL('**/login')
    })
    check('浏览器：交互过程没有页面运行时错误', () => assert.deepEqual(pageErrors, []))
  } finally {
    await browser.close()
  }
}

console.log(`\n${results.filter((result) => result.ok).length}/${results.length} checks passed`)
if (results.some((result) => !result.ok)) process.exitCode = 1
