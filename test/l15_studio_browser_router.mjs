/**
 * T32 浏览器路由 E2E 守卫（Issue #160 T32）。
 *
 * 运行：
 *   node test/l15_studio_browser_router.mjs
 *
 * ---------------------------------------------------------------------------
 * 为什么是「真实路由树 + BrowserRouter + SSR」而不是 Playwright
 * ---------------------------------------------------------------------------
 * 计划里 T32 写的是「Playwright BrowserRouter E2E」。本机与 CI 的
 * `ui` 步骤都没有 chromium/playwright，而本仓库的既有约定是
 * 「不为 UI 守卫新增依赖」（docs/plans/atelier-implementation.md 与
 * test/l15_stage_routes.mjs 的文件头都记录了这条约束），否则会影响
 * 其它 lane 共享的 node_modules。
 *
 * 因此这里做**能真实执行的那一半**：
 *
 *   * 用 esbuild 打包**生产源码** src/studio/StudioRoutes.tsx；
 *   * 装一个最小的 `window.history`/`window.location` 垫片，让
 *     react-router 的 **BrowserRouter**（不是 MemoryRouter）真正走
 *     浏览器路径解析；
 *   * 用 react-dom/server 真实执行路由与页面组件函数体；
 *   * 断言「深链接 → 导航 → 返回 → 刷新」这条序列解析稳定，
 *     且项目 ID 只来自 URL（不是任何全局状态）。
 *
 * BrowserRouter 与 MemoryRouter 的差别正是**路径来源**：MemoryRouter 用
 * 内存数组，BrowserRouter 用 `window.location`。用垫片驱动 BrowserRouter
 * 才能测到「刷新/粘贴深链接」这一类场景；用 MemoryRouter 测不到。
 *
 * 局限（如实写出，不用它代替真实浏览器）：SSR 不执行 useEffect，
 * 因此看不到「数据加载完成后」的界面；真实浏览器里的滚动、焦点、
 * 触屏与 390px 布局仍需要 T29 的可选路径（--with-browser）或人工验收。
 *
 * 运行产物：test/artifacts/browser-router/report.json（CI 作为 artifact 上传，
 * 但**断言结果**才是门禁；artifact 只是取证）。
 */

import { mkdirSync, mkdtempSync, readFileSync, writeFileSync, rmSync } from 'node:fs'
import { createRequire } from 'node:module'
import { tmpdir } from 'node:os'
import path from 'node:path'
import { fileURLToPath } from 'node:url'

const REPO_ROOT = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..')
const WEB_ROOT = path.join(REPO_ROOT, 'apps', 'web-user')
const STUDIO_ROUTES_SOURCE = path.join(WEB_ROOT, 'src', 'studio', 'StudioRoutes.tsx')
const ROUTES_SOURCE = path.join(WEB_ROOT, 'src', 'studio', 'routes.ts')
const REPORT_DIR = path.join(REPO_ROOT, 'test', 'artifacts', 'browser-router')

const webRequire = createRequire(path.join(WEB_ROOT, 'package.json'))
const esbuild = webRequire('esbuild')

const WITH_BROWSER = process.argv.includes('--with-browser')

const failures = []
const results = []
function record(name, ok, detail) {
  console.log(`[${ok ? 'PASS' : 'FAIL'}] ${name}: ${detail}`)
  results.push({ name, ok, detail })
  if (!ok) failures.push(name)
}
function recordSkip(name, detail) {
  console.log(`[SKIP] ${name}: ${detail}`)
  results.push({ name, ok: true, skipped: true, detail })
}

/**
 * window 垫片：BrowserRouter 需要的最小 DOM 面。
 *
 * 只实现 BrowserRouter 真正读写的部分（history.state/replaceState/
 * pushState/listener、location.pathname/search/hash）。多实现一点会让
 * 「垫片行为」与真实浏览器产生偏差，而那种偏差不会报错，只会让断言失真。
 */
const WINDOW_SHIM = `
(function installWindowShim() {
  if (globalThis.window && globalThis.window.__l15Shim) return
  const listeners = []
  const history = {
    state: null,
    length: 1,
    _url: '/',
    pushState(state, _title, url) { this.state = state; this._url = String(url) },
    replaceState(state, _title, url) { this.state = state; if (url) this._url = String(url) },
    go() {}, back() {}, forward() {},
  }
  const location = {
    pathname: '/', search: '', hash: '', href: 'http://localhost/',
    origin: 'http://localhost', protocol: 'http:', host: 'localhost', hostname: 'localhost', port: '',
  }
  globalThis.window = {
    __l15Shim: true,
    history,
    location,
    addEventListener(type, handler) { listeners.push([type, handler]) },
    removeEventListener() {},
    document: { title: '' },
    navigator: { userAgent: 'l15-browser-router-shim' },
  }
  // react-router 的 getUrlBasedHistory 默认取 document.defaultView 作为 window；
  // 不设它时会拿到 undefined，报错位置在 router 内部（很难从堆栈看出是垫片缺字段）。
  globalThis.window.document.defaultView = globalThis.window
  globalThis.document = globalThis.window.document
  globalThis.__l15SetPath = function setPath(pathname) {
    const parsed = new URL('http://localhost' + pathname)
    location.pathname = parsed.pathname
    location.search = parsed.search
    location.hash = parsed.hash
    location.href = parsed.toString()
    history._url = pathname
  }
})();
`

async function buildBrowserRenderer(workDir) {
  const entry = [
    "import { createElement } from 'react'",
    "import { renderToStaticMarkup } from 'react-dom/server'",
    "import { BrowserRouter, Routes } from 'react-router-dom'",
    `import { studioRouteTree } from ${JSON.stringify(STUDIO_ROUTES_SOURCE)}`,
    'export function renderAt(pathname) {',
    '  globalThis.__l15SetPath(pathname)',
    '  try {',
    '    const html = renderToStaticMarkup(',
    '      createElement(BrowserRouter, null,',
    '        createElement(Routes, null,',
    '          studioRouteTree({ user: { id: 1, email: "admin@example.com", role: "admin" }, onLogout() {} }),',
    '        ),',
    '      ),',
    '    )',
    '    return { html, error: null }',
    '  } catch (error) {',
    // React 18 的 renderToStaticMarkup 不会走类组件的 getDerivedStateFromError
    //（SSR 的已知限制），因此非法项目 ID 会把错误抛到这里。返回错误信息，
    // 由测试断言它是**可读文案**（真实浏览器里同一段文案由错误边界渲染）。
    '    return { html: "", error: error instanceof Error ? error.message : String(error) }',
    '  }',
    '}',
    '',
  ].join('\n')

  const build = await esbuild.build({
    stdin: { contents: entry, resolveDir: WEB_ROOT, loader: 'tsx' },
    bundle: true,
    format: 'cjs',
    platform: 'node',
    jsx: 'automatic',
    absWorkingDir: WEB_ROOT,
    write: false,
    outfile: 'browser-router.cjs',
    nodePaths: [path.join(WEB_ROOT, 'node_modules'), path.join(REPO_ROOT, 'node_modules')],
    mainFields: ['module', 'main'],
    loader: { '.css': 'empty' },
    logLevel: 'error',
    banner: { js: WINDOW_SHIM },
    define: { 'import.meta.env': JSON.stringify({ PROD: true, MODE: 'production' }) },
  })

  const Module = webRequire('node:module')
  const mod = new Module.Module('l15-studio-browser-router')
  mod.paths = Module.Module._nodeModulePaths(REPO_ROOT)
  mod._compile(build.outputFiles[0].text, path.join(workDir, 'browser-router.cjs'))
  return mod.exports.renderAt
}

/** 用 esbuild 打包生产路由模块（纯逻辑，无 React）。 */
async function buildRouteModule(workDir, tag, sourceOverride) {
  // 每次构建用**不同的 outfile**：Node 的 require 按解析后路径缓存，
  // 复用同一个文件名会让变异版本直接拿到原始模块（本守卫真的踩过这一次 ——
  // 表现为「变异自证静默通过」）。
  const outfile = path.join(workDir, `routes-${tag}.cjs`)
  const plugin = sourceOverride
    ? {
        name: 'override-routes',
        setup(build) {
          build.onLoad({ filter: /studio[\\/]routes\.ts$/ }, () => ({
            contents: sourceOverride,
            loader: 'ts',
            resolveDir: path.dirname(ROUTES_SOURCE),
          }))
        },
      }
    : undefined
  await esbuild.build({
    stdin: {
      contents: `export * from ${JSON.stringify(ROUTES_SOURCE)}`,
      resolveDir: WEB_ROOT,
      loader: 'ts',
    },
    bundle: true,
    format: 'cjs',
    platform: 'node',
    absWorkingDir: WEB_ROOT,
    outfile,
    nodePaths: [path.join(WEB_ROOT, 'node_modules'), path.join(REPO_ROOT, 'node_modules')],
    mainFields: ['module', 'main'],
    logLevel: 'error',
    define: { 'import.meta.env': JSON.stringify({ PROD: true, MODE: 'production' }) },
    plugins: plugin ? [plugin] : [],
  })
  return webRequire(outfile)
}

/** 导航序列：深链接 → 应用内跳转 → 返回 → 刷新。 */
const NAVIGATION_STEPS = [
  { path: '/p/7/overview', key: 'project.overview' },
  { path: '/p/7/data/s_3?status=pending', key: 'project.sample' },
  { path: '/p/7/overview', key: 'project.overview' },
  { path: '/p/7/overview', key: 'project.overview' },
  { path: '/p/7/blueprint?node=generation&version=4', key: 'project.blueprint' },
  { path: '/p/7/runs/b_12/failures', key: 'project.runFailures' },
]

function runNavigationSequence(routes, steps) {
  const observed = []
  for (const step of steps) {
    const matched = routes.matchRoute(step.path)
    observed.push({
      path: step.path,
      key: matched?.key ?? null,
      navParent: matched?.navParent ?? null,
      activeNavKey: matched ? routes.activeNavKey(step.path) : null,
      moduleStatus: matched?.moduleStatus ?? null,
    })
  }
  return observed
}

const workDir = mkdtempSync(path.join(tmpdir(), 'l15-browser-router-'))
try {
  // -------------------------------------------------------------------------
  // 1. 真实路由模块的导航序列（deep link → navigate → back → refresh）
  // -------------------------------------------------------------------------
  const routes = await buildRouteModule(workDir, 'base')
  const observed = runNavigationSequence(routes, NAVIGATION_STEPS)
  let sequenceOk = true
  for (let index = 0; index < NAVIGATION_STEPS.length; index += 1) {
    const expected = NAVIGATION_STEPS[index]
    const actual = observed[index]
    if (actual.key !== expected.key) {
      sequenceOk = false
      record(`导航序列第 ${index + 1} 步`, false,
        `${expected.path} 应解析为 ${expected.key}，实际 ${String(actual.key)}`)
    }
  }
  if (sequenceOk) {
    record('深链接 → 导航 → 返回 → 刷新 解析稳定', true,
      `${observed.length} 步全部命中同一份路由元数据`)
  }
  // 子页必须归属父标签（issue #61 的形态）：否则侧边栏高亮会掉回默认项。
  const failuresStep = observed[observed.length - 1]
  record('子页高亮归属父标签', failuresStep.activeNavKey === 'project.runs',
    `runs/b_12/failures 的 activeNavKey = ${String(failuresStep.activeNavKey)}`)
  record('/catalog 在生产构建不挂载', routes.isCatalogRouteMounted() === false,
    `isCatalogRouteMounted() = ${String(routes.isCatalogRouteMounted())}`)

  // -------------------------------------------------------------------------
  // 2. BrowserRouter 真实渲染：项目 ID 只来自 URL
  // -------------------------------------------------------------------------
  let renderAt = null
  try {
    renderAt = await buildBrowserRenderer(workDir)
  } catch (error) {
    record('BrowserRouter 渲染入口可打包', false, `esbuild/打包失败：${error.message}`)
  }

  if (renderAt) {
    const first = renderAt('/p/7/overview').html
    record('深链接渲染项目壳', first.includes('data-studio-project-id="7"'),
      '输出包含 data-studio-project-id="7"（项目 ID 来自 URL，而非全局状态）')

    // 第二个项目：同一进程内再次渲染，不得复用上一次的 ID（状态泄漏）。
    const second = renderAt('/p/9/data').html
    const leaked = second.includes('data-studio-project-id="7"')
    record('切换项目不泄漏上一项目 ID',
      second.includes('data-studio-project-id="9"') && !leaked,
      `第二次渲染包含 id=9：${second.includes('data-studio-project-id="9"')}，含 id=7（泄漏）：${leaked}`)

    // 未实现的模块必须显示**诚实的能力状态**，而不是空白页。
    const planned = routes.allStudioRoutes.find((route) => route.moduleStatus === 'planned')
    if (planned) {
      const plannedPath = planned.path
        .replace(':projectId', '7')
        .replace(':sampleId', 's_1')
        .replace(':releaseId', 'r_1')
        .replace(':experimentId', 'e_1')
        .replace(':batchId', 'b_1')
        .replace('/*', '')
      const html = renderAt(plannedPath).html
      const honest = html.includes('data-capability-notice') || html.includes('尚未')
      record(`未交付模块显示能力状态（${plannedPath}）`, honest,
        honest ? '输出包含能力状态提示' : `未找到能力状态提示；输出片段：${html.slice(0, 120)}`)
    } else {
      recordSkip('未交付模块显示能力状态', '当前所有路由都是 available')
    }

    // 非法项目 ID：壳层抛错，必须有**可读文案**（不能白屏、不能是原始堆栈）。
    const invalid = renderAt('/p/not-a-number/overview')
    const readable = invalid.error !== null && invalid.error.includes('项目地址不正确')
    record('非法项目 ID 有可读错误文案', readable,
      readable ? `错误文案：${invalid.error}` : `未得到可读文案，error=${String(invalid.error)}`)

    recordSkip('真实浏览器断言（滚动/焦点/390px）',
      WITH_BROWSER ? '需要 chromium；当前环境未提供，保留为 T29 的可选路径' : '未启用 --with-browser')
  }

  // -------------------------------------------------------------------------
  // 3. 变异自证：把 navParent 改坏，第 1 层必须捕获
  // -------------------------------------------------------------------------
  const original = readFileSync(ROUTES_SOURCE, 'utf8')
  // 用 replaceAll 而不是 replace：`navParent: 'project.runs'` 在 routes.ts 里
  // 出现多次（pilot/compare/runNew/runDetail/runFailures），只改第一处不会
  // 影响被测的 runFailures —— 那样变异自证会「静默通过」，而它恰好是
  // 证明断言非空转的那一条。
  const mutated = original.replaceAll(
    "navParent: 'project.runs'",
    "navParent: 'project.quality'",
  )
  if (mutated === original) {
    record('变异自证（navParent 写错）', false,
      '未能定位 navParent: project.runs —— routes.ts 结构已变化，请同步本守卫')
  } else {
    const mutatedModule = await buildRouteModule(workDir, 'mutated', mutated)
    const mutatedObserved = runNavigationSequence(mutatedModule, [NAVIGATION_STEPS[NAVIGATION_STEPS.length - 1]])
    const caught = mutatedObserved[0].activeNavKey !== 'project.runs'
    record('变异自证（navParent 写错必须被捕获）', caught,
      `改坏后 activeNavKey = ${String(mutatedObserved[0].activeNavKey)}`)
  }
} finally {
  rmSync(workDir, { recursive: true, force: true })
}

// ---------------------------------------------------------------------------
// 产物：断言结果 + 本次观察到的导航序列（CI artifact 取证）
// ---------------------------------------------------------------------------
mkdirSync(REPORT_DIR, { recursive: true })
const report = {
  generatedAt: new Date().toISOString(),
  withBrowser: WITH_BROWSER,
  note: '断言结果才是门禁；本文件仅用于 CI artifact 取证。',
  failures,
  results,
}
writeFileSync(path.join(REPORT_DIR, 'report.json'), JSON.stringify(report, null, 2))
console.log(`\n产物：${path.relative(REPO_ROOT, path.join(REPORT_DIR, 'report.json'))}`)

if (failures.length > 0) {
  console.error(`\nBROWSER ROUTER GUARD FAILED: ${failures.length} 项失败`)
  process.exit(1)
}
console.log('\nBROWSER ROUTER GUARD OK')
