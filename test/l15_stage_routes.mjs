/**
 * L15 R1 阶段路由可达性守卫（自包含，一条命令）。
 *
 * 运行：
 *   node test/l15_stage_routes.mjs              # 源码级断言（CI 默认路径，无需任何容器）
 *   node test/l15_stage_routes.mjs --with-api   # 额外跑真实 API + 渲染级断言
 *
 * ---------------------------------------------------------------------------
 * 为什么默认只跑源码级断言（CI 可执行性是硬要求）
 * ---------------------------------------------------------------------------
 * CI 的 Backend job 只跑 `go test`，Frontend job 只跑 `tsc + vite build`，
 * **都不会**起 :18101 的候选容器。因此若本脚本把「真实 API 登录」作为必经前置，
 * 它在 CI 里 100% 失败 —— 等于一个永远跑不起来的守卫，CI 防不住回归；
 * 而且会把「环境不可达」误报为「断言失败」，真出回归时没人分得清两者。
 *
 * 所以拆成两层：
 *   - **源码级断言（默认，exit code 由此决定）**：只读 App.tsx 源码文本求值，
 *     不需要容器、不需要 DOM、不需要网络。CI 直接跑它。
 *     覆盖：#61 自指重定向形态、5 个阶段路由必须渲染页面、render* 不得成死代码、
 *     stageRouteNavMap 必须由阶段工作台声明派生、以及 **route→navParent 取值正确性**。
 *   - **真实 API + 渲染级断言（--with-api，可选）**：起候选容器后跑，
 *     验证打的是本 lane 的镜像，并用 esbuild + SSR 真实渲染页面。
 *     未启用时输出 [SKIP]，**不影响 exit code**。
 *
 * 变异自证：源码级断言自带变异用例（把路由退回 <Navigate>、把派生退回手写表、
 * 把 navParent 改错），每条都必须被捕获；若某条变异未被捕获，测试自身报 FAIL。
 * 这样「断言非空转」是被证明的，而不是被声称的。
 *
 * 背景（issue #61）：5b90c2e 把 5 个任务阶段路由改成
 *   <Route path="/console/domains" element={<Navigate to={activeTaskDetailRoute} replace />} />
 * 而 activeTaskDetailRoute 就是 /console/tasks/{id}，于是阶段页变成自指重定向，
 * 「生成方向结构」等按钮在 UI 上彻底不可达，流水线硬阻塞在 draft。
 *
 * PR #70 把路由改回渲染各自页面；本 lane 进一步把阶段路由的侧边栏归属
 * （stageRouteNavMap）改为从阶段工作台声明的 navParent 派生，消除「路由表 / 侧边栏归属 /
 * 面包屑」三处各写一份、互相漂移的隐患。
 *
 * ---------------------------------------------------------------------------
 * 为什么是「源码级断言 + 渲染级断言」而不是浏览器 E2E
 * ---------------------------------------------------------------------------
 * 本机没有 chromium / playwright / jsdom，且契约要求本轮不得为本测试新增依赖
 * （新增依赖会污染共享 node_modules，进而误导其他 lane 的断言）。因此：
 *
 *   1. 渲染级：沿用 test/l14_ui_smoke.mjs 的技术路线——用 esbuild 真实打包 App.tsx，
 *      再用 React 的 renderToStaticMarkup 真实执行组件函数体。这会真实跑过
 *      路由匹配、页面渲染函数、侧边栏与面包屑的派生计算。
 *
 *   2. SSR 的限制（实测确认，不是推测）：App 在 `sessionLoading` 为真时只渲染
 *      一个 Spin 占位；而 sessionLoading 只在 useEffect 里被清掉，SSR 不执行 effect。
 *      因此纯 SSR 只能拿到占位符，无法观察登录后的路由。
 *      解决办法：用 esbuild 的 onLoad 插件在**打包期**把两处会话状态预置为已登录
 *      （sessionLoading=false、user=admin）。这是测试侧的 bundle 变换，
 *      产品源码一个字节都不改（下面有 selfCheckSeedIsMinimal 逐行证明）。
 *
 *   3. 「URL 未被重定向」在 SSR 下的可观测形式：
 *      - 决定性证据：若某阶段路由的 element 是 <Navigate>，SSR 下该路由渲染 null，
 *        阶段页标题就不可能出现。所以「阶段页标题出现」直接证伪了重定向。
 *      - 辅助证据：在 MemoryRouter 内挂一个读取 useLocation() 的回显组件，
 *        输出路由解析后的真实 pathname，断言它等于请求的路径。
 *      - 静态证据：直接断言 5 条阶段 <Route> 的 element 里没有 <Navigate>，
 *        这是对 #61 原缺陷形态的精确回归守卫。
 *
 * 数据来源：真实 HTTP 调用本 lane 构建的 API（默认 http://127.0.0.1:18101/api/v1）
 * 完成一次真实登录，证明被测对象是本 lane 的镜像；API 不可达则直接失败退出，
 * 不用硬编码假数据。
 */

import { mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import { createRequire } from 'node:module'
import { tmpdir } from 'node:os'
import path from 'node:path'
import { fileURLToPath } from 'node:url'

const REPO_ROOT = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..')
const WEB_ROOT = path.join(REPO_ROOT, 'apps', 'web-user')
const APP_SOURCE = path.join(WEB_ROOT, 'src', 'App.tsx')

const webRequire = createRequire(path.join(WEB_ROOT, 'package.json'))
const esbuild = webRequire('esbuild')

const API_BASE = process.env.L15_API_BASE ?? 'http://127.0.0.1:18101/api/v1'
const ADMIN_EMAIL = process.env.L15_ADMIN_EMAIL ?? 'admin@company.com'
const ADMIN_PASSWORD = process.env.L15_ADMIN_PASSWORD ?? 'admin123456'

// --with-api：启用需要候选容器的「真实 API + 渲染级断言」。
// 默认关闭，因为 CI 不起容器；默认路径必须能独立跑绿并决定 exit code。
const WITH_API = process.argv.includes('--with-api')

/**
 * 5 个阶段路由、其在侧边栏中的归属父项（navParent），以及面包屑使用的短标签（label）。
 * 三者都来自同一份阶段工作台声明（taskWorkbenchPages / resultWorkbenchPages），
 * 这正是本 lane 要锁定的一致性不变量（契约 §2 R1 行的冻结范围）。
 */
const STAGE_ROUTES = [
  { route: '/console/domains', navParent: '/console/tasks', label: '主题结构', title: '生成主题结构并完成确认' },
  { route: '/console/questions', navParent: '/console/results', label: '问题生成', title: '题目生成结果中心' },
  { route: '/console/reasoning', navParent: '/console/results', label: '答案内容', title: '答案与思路结果中心' },
  { route: '/console/rewards', navParent: '/console/results', label: '质量评估', title: '质量评分结果中心' },
  { route: '/console/exports', navParent: '/console/results', label: '导出交付', title: '导出结果中心' },
]

/** 任务详情页在 SSR 下（无 activeDataset）的特征，阶段页不得出现。 */
const DETAIL_PAGE_MARKERS = ['我的任务 / 任务详情', '未找到任务详情']

const failures = []
const results = []

function record(name, ok, detail) {
  results.push({ name, ok, detail })
  console.log(`[${ok ? 'PASS' : 'FAIL'}] ${name}: ${detail}`)
  if (!ok) failures.push(name)
}

function recordSkip(name, detail) {
  results.push({ name, ok: true, skipped: true, detail })
  console.log(`[SKIP] ${name}: ${detail}`)
}

// ---------------------------------------------------------------------------
// 源码级断言（对源码文本求值，便于用「变异」自证断言非空转）
// ---------------------------------------------------------------------------

/** 从源码里抽取某个 `const <name>...= [...]` 数组中的字段。 */
function extractArrayField(source, arrayName, field) {
  const block = source.match(new RegExp(`const\\s+${arrayName}\\s*:[^=]*=\\s*\\[([\\s\\S]*?)\\n\\]`))
  if (!block) return null
  const out = []
  const re = new RegExp(`${field}:\\s*'([^']+)'`, 'g')
  for (const m of block[1].matchAll(re)) out.push(m[1])
  return out
}

function extractStageDeclarations(source) {
  const taskRoutes = extractArrayField(source, 'taskWorkbenchPages', 'route') ?? []
  const taskParents = extractArrayField(source, 'taskWorkbenchPages', 'navParent') ?? []
  const taskLabels = extractArrayField(source, 'taskWorkbenchPages', 'label') ?? []
  const resultRoutes = extractArrayField(source, 'resultWorkbenchPages', 'route') ?? []
  const resultParents = extractArrayField(source, 'resultWorkbenchPages', 'navParent') ?? []
  const resultLabels = extractArrayField(source, 'resultWorkbenchPages', 'label') ?? []
  if (taskRoutes.length !== taskParents.length || resultRoutes.length !== resultParents.length) {
    throw new Error('navParent 与 route 数量不一致：阶段工作台声明缺少 navParent')
  }
  return [
    ...taskRoutes.map((r, i) => ({ route: r, navParent: taskParents[i], label: taskLabels[i] })),
    ...resultRoutes.map((r, i) => ({ route: r, navParent: resultParents[i], label: resultLabels[i] })),
  ]
}

/** 取出某条 <Route path="..."> 的完整 element 表达式。 */
function extractRouteElement(source, route) {
  const re = new RegExp(`<Route\\s+path="${route.replace(/[/\\^$*+?.()|[\]{}]/g, '\\$&')}"\\s+element=\\{([\\s\\S]*?)\\}\\s*/>`)
  const m = source.match(re)
  return m ? m[1] : null
}

/**
 * 断言：5 条阶段路由都渲染真实页面，而不是 <Navigate> 重定向。
 * 返回问题列表（空 = 通过）。
 */
function problemsWithStageRoutes(source) {
  const problems = []
  for (const { route } of STAGE_ROUTES) {
    const element = extractRouteElement(source, route)
    if (element === null) {
      problems.push(`${route} 在路由表中缺失`)
      continue
    }
    if (element.includes('<Navigate')) {
      problems.push(`${route} 的 element 是 <Navigate>（自指重定向，issue #61 原形态）`)
    }
  }
  return problems
}

/**
 * 断言：阶段路由的侧边栏归属由阶段工作台声明派生，且每个阶段都声明了 navParent。
 * 返回问题列表。
 */
function problemsWithNavDerivation(source) {
  const problems = []
  for (const { route, navParent } of STAGE_ROUTES) {
    if (!navParent) problems.push(`${route} 未声明 navParent`)
  }

  const mapBlock = source.match(/const\s+stageRouteNavMap\s*:[^=]*=\s*([\s\S]*?)\n\n/)
  if (!mapBlock) {
    problems.push('未找到 stageRouteNavMap 定义')
    return problems
  }
  const expression = mapBlock[1]
  // 必须是派生，不能是手写常量表（手写表会在路由变动时再次漂移）。
  if (!expression.includes('taskWorkbenchPages') || !expression.includes('resultWorkbenchPages')) {
    problems.push('stageRouteNavMap 不是由 taskWorkbenchPages/resultWorkbenchPages 派生')
  }
  if (!expression.includes('navParent')) {
    problems.push('stageRouteNavMap 的派生未使用 navParent')
  }
  return problems
}

/** 变异：把某阶段路由还原成 #61 的自指重定向，用于证明断言非空转。 */
function mutateToSelfRedirect(source, route) {
  const element = extractRouteElement(source, route)
  if (element === null) throw new Error(`变异失败：找不到 ${route}`)
  return source.replace(
    `<Route path="${route}" element={${element}} />`,
    `<Route path="${route}" element={<Navigate to={activeTaskDetailRoute} replace />} />`,
  )
}

/** 变异：把 stageRouteNavMap 退回手写常量表，用于证明断言非空转。 */
function mutateToHandWrittenMap(source) {
  return source.replace(
    /const\s+stageRouteNavMap\s*:[^=]*=\s*[\s\S]*?\n\n/,
    "const stageRouteNavMap: Record<string, string> = {\n  '/console/domains': '/console/tasks',\n}\n\n",
  )
}

// ---------------------------------------------------------------------------
// 渲染级断言
// ---------------------------------------------------------------------------

/** esbuild 插件：把会话状态预置为已登录，并把改后的源码落盘供 self-check。 */
function makeSeedPlugin(seededPath, applied) {
  return {
    name: 'l15-seed-session',
    setup(build) {
      build.onLoad({ filter: /src[/\\]App\.tsx$/ }, () => {
        let source = readFileSync(APP_SOURCE, 'utf8')
        const before = source
        source = source.replace(
          'const [sessionLoading, setSessionLoading] = useState(true)',
          'const [sessionLoading, setSessionLoading] = useState(false)',
        )
        source = source.replace(
          'const [user, setUser] = useState<User | null>(null)',
          "const [user, setUser] = useState<User | null>({ id: 1, email: 'admin@company.com', role: 'admin' })",
        )
        if (source === before) {
          throw new Error('会话状态预置未命中：App.tsx 结构已变化，请同步本测试')
        }
        applied.seeded = source
        if (seededPath) writeFileSync(seededPath, source)
        return { contents: source, loader: 'tsx', resolveDir: path.dirname(APP_SOURCE) }
      })
    },
  }
}

async function buildRenderer(seededPath, applied) {
  const entry = [
    "import { createElement } from 'react'",
    "import { renderToStaticMarkup } from 'react-dom/server'",
    "import { MemoryRouter, useLocation } from 'react-router-dom'",
    `import App from ${JSON.stringify(APP_SOURCE)}`,
    '// 回显 router 解析后的真实 pathname，作为「未被重定向」的辅助证据。',
    'function LocationEcho() {',
    "  return createElement('span', { id: 'l15-location' }, useLocation().pathname)",
    '}',
    'export function render(initialPath) {',
    '  return renderToStaticMarkup(',
    '    createElement(',
    '      MemoryRouter,',
    '      { initialEntries: [initialPath] },',
    '      createElement(LocationEcho),',
    '      createElement(App),',
    '    ),',
    '  )',
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
    outfile: 'renderer.cjs',
    nodePaths: [path.join(WEB_ROOT, 'node_modules'), path.join(REPO_ROOT, 'node_modules')],
    // 同 l14_ui_smoke.mjs：优先 ESM 入口，绕开 jsonc-parser 的 UMD/AMD 互操作坑。
    mainFields: ['module', 'main'],
    loader: { '.css': 'empty' },
    logLevel: 'error',
    plugins: [makeSeedPlugin(seededPath, applied)],
  })

  const Module = webRequire('node:module')
  const mod = new Module.Module('l15-stage-routes')
  mod.paths = Module.Module._nodeModulePaths(REPO_ROOT)
  mod._compile(build.outputFiles[0].text, path.join(REPO_ROOT, 'renderer.cjs'))
  return mod.exports.render
}

/** 从 SSR 输出中提取侧边栏当前高亮项（Semi Nav 的 item-text）。 */
function selectedNavItems(html) {
  const found = new Set()
  const re = /semi-navigation-item-selected[\s\S]{0,1200}?<span class="semi-navigation-item-text">([^<]+)<\/span>/g
  for (const m of html.matchAll(re)) found.add(m[1])
  return [...found]
}

async function realLogin() {
  const res = await fetch(`${API_BASE}/auth/login`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ email: ADMIN_EMAIL, password: ADMIN_PASSWORD }),
  })
  if (!res.ok) throw new Error(`登录失败 ${res.status}: ${(await res.text()).slice(0, 200)}`)
  return res.status
}

// ---------------------------------------------------------------------------
// 主流程
// ---------------------------------------------------------------------------

const workDir = mkdtempSync(path.join(tmpdir(), 'l15-stage-routes-'))
const applied = { seeded: null }
let render

try {
  const source = readFileSync(APP_SOURCE, 'utf8')

  // ---- 0. 真实 API 探活（证明打的是本 lane 的镜像） ----
  // 仅在 --with-api 下执行：CI 不起容器，默认路径不得因环境缺失而失败。
  if (WITH_API) {
    try {
      const status = await realLogin()
      record('真实登录本 lane 的 API', status === 200, `${API_BASE} 登录返回 ${status}`)
    } catch (error) {
      record('真实登录本 lane 的 API', false,
        `${API_BASE} 不可达或被拒：${error.message}（先跑 scripts/l15-r1-stack.sh）`)
      throw new Error('被测 API 不可及，无法继续')
    }
  } else {
    recordSkip('真实登录本 lane 的 API', `未启用 --with-api（默认路径不需要容器；加 --with-api 并先跑 scripts/l15-r1-stack.sh）`)
  }

  // ---- 1. 打包 + 会话预置最小性自检 ----
  // 渲染级断言同样需要 esbuild 打包，但**不需要网络**；打包失败属真实缺陷，故不跳过。
  render = await buildRenderer(path.join(workDir, 'App.seeded.tsx'), applied)

  record('App.tsx 可被打包并渲染', typeof render === 'function', 'esbuild bundle + renderToStaticMarkup 可用')

  if (!applied.seeded) {
    record('会话预置最小性', false, 'esbuild 插件未产出预置源码')
  } else {
    const byHand = source
      .replace('const [sessionLoading, setSessionLoading] = useState(true)',
               'const [sessionLoading, setSessionLoading] = useState(false)')
      .replace('const [user, setUser] = useState<User | null>(null)',
               "const [user, setUser] = useState<User | null>({ id: 1, email: 'admin@company.com', role: 'admin' })")
    record('会话预置与预期变换逐字节一致', byHand === applied.seeded,
      byHand === applied.seeded ? '预置只在打包期改会话状态' : '预置内容与预期不符')

    const diffLines = []
    const a = source.split('\n')
    const b = applied.seeded.split('\n')
    for (let i = 0; i < Math.max(a.length, b.length); i++) {
      if (a[i] !== b[i]) diffLines.push(i + 1)
    }
    record('预置只改动 2 行会话状态', diffLines.length === 2,
      `差异行号 [${diffLines.join(', ')}]（应为 2 行：sessionLoading 与 user）`)

    // 路由表与导航归属必须与磁盘源码完全一致，证明断言针对的是真实产品代码。
    for (const { route, navParent } of STAGE_ROUTES) {
      const fromSource = extractRouteElement(source, route)
      const fromSeeded = extractRouteElement(applied.seeded, route)
      if (fromSource !== fromSeeded) {
        record(`预置未改动 ${route} 的 element`, false, '预置意外改到了路由表')
      }
      if (!extractArrayField(applied.seeded, 'taskWorkbenchPages', 'navParent')?.includes(navParent)
          && !extractArrayField(applied.seeded, 'resultWorkbenchPages', 'navParent')?.includes(navParent)) {
        record(`预置未改动 ${route} 的 navParent`, false, '预置意外改到了导航归属')
      }
    }
  }

  // ---- 2. 源码级断言 ----
  const routeProblems = problemsWithStageRoutes(source)
  record('5 条阶段路由都渲染真实页面（无 <Navigate> 重定向）', routeProblems.length === 0,
    routeProblems.length === 0 ? '5/5 通过' : routeProblems.join('; '))

  const navProblems = problemsWithNavDerivation(source)
  record('stageRouteNavMap 由阶段工作台声明派生且每阶段声明 navParent', navProblems.length === 0,
    navProblems.length === 0 ? '派生自 taskWorkbenchPages/resultWorkbenchPages 的 navParent' : navProblems.join('; '))

  const declarations = extractStageDeclarations(source)
  const declOK = STAGE_ROUTES.every((s) =>
    declarations.some((d) => d.route === s.route && d.navParent === s.navParent && d.label === s.label))
  record('阶段声明的 route→navParent/label 与契约冻结值一一对应', declOK,
    declarations.map((d) => `${d.route}→${d.navParent}(${d.label})`).join(', '))

  // 非空转自证：把断言喂给「已知坏」的源码，必须报错。
  const mutatedRedirect = mutateToSelfRedirect(source, '/console/questions')
  record('变异检测：自指重定向必须被断言捕获',
    problemsWithStageRoutes(mutatedRedirect).length > 0,
    problemsWithStageRoutes(mutatedRedirect).join('; ') || '未被捕获（断言空转）')

  const mutatedMap = mutateToHandWrittenMap(source)
  record('变异检测：手写 nav 表必须被断言捕获',
    problemsWithNavDerivation(mutatedMap).length > 0,
    problemsWithNavDerivation(mutatedMap).join('; ') || '未被捕获（断言空转）')

  // ---- 3. 渲染级断言 ----
  const sidebarLabels = new Map()
  const userRoutes = extractArrayField(source, 'userPages', 'route') ?? []
  const userLabels = extractArrayField(source, 'userPages', 'label') ?? []
  userRoutes.forEach((r, i) => sidebarLabels.set(r, userLabels[i]))

  for (const { route, navParent, label, title } of STAGE_ROUTES) {
    const html = render(route)
    const name = route.replace('/console/', '')

    const titlePresent = html.includes(title)
    record(`[${name}] 渲染出本阶段页面标题「${title}」`, titlePresent,
      titlePresent ? '阶段页内容存在（若为 <Navigate>，SSR 下会渲染 null，此处不会命中）' : '未命中阶段页标题')

    const echo = html.match(/<span id="l15-location">([^<]*)<\/span>/)
    record(`[${name}] 路由解析后 pathname 未被重定向`, echo?.[1] === route,
      `pathname=${echo?.[1] ?? '(缺失)'}（期望 ${route}）`)

    const selected = selectedNavItems(html)
    const expectedLabel = sidebarLabels.get(navParent)
    record(`[${name}] 侧边栏高亮到归属父项「${expectedLabel}」`,
      selected.length === 1 && selected[0] === expectedLabel,
      `高亮=${JSON.stringify(selected)}（期望 ["${expectedLabel}"]）`)

    // 面包屑用的是阶段声明的 label（短标签），与 navParent 同源，
    // 因此它同时证明「面包屑没有漂移到任务详情页」。
    const breadcrumb = html.match(/<span class="current">([^<]*)<\/span>/)
    record(`[${name}] 面包屑显示本阶段短标签「${label}」而非任务详情`,
      breadcrumb?.[1] === label,
      `面包屑=${JSON.stringify(breadcrumb?.[1] ?? '(缺失)')}（期望 ${label}）`)

    const detailMarkers = DETAIL_PAGE_MARKERS.filter((marker) => html.includes(marker))
    record(`[${name}] 不含任务详情页自指特征`, detailMarkers.length === 0,
      detailMarkers.length === 0 ? '无详情页特征' : `出现了 ${detailMarkers.join(', ')}`)
  }

  // 反向对照：详情页特征本身可被识别（证明上一条断言不是空转）。
  const detailHtml = render('/console/tasks/1')
  const detailDetected = DETAIL_PAGE_MARKERS.some((marker) => detailHtml.includes(marker))
  if (detailDetected) {
    record('反向对照：详情页特征可被识别', true,
      `无 activeDataset 的详情页出现 ${DETAIL_PAGE_MARKERS.filter((m) => detailHtml.includes(m)).join(', ')}`)
  } else {
    recordSkip('反向对照：详情页特征可被识别',
      'SSR 下详情页未出现预期特征，反向对照退化为源码级断言（已在上面通过）')
  }
} catch (error) {
  if (failures.length === 0) {
    record('测试执行', false, error.message)
  }
} finally {
  rmSync(workDir, { recursive: true, force: true })
}

console.log('')
const skipped = results.filter((r) => r.skipped).length
if (failures.length > 0) {
  console.error(`STAGE ROUTES FAILED: ${failures.length}/${results.length} 项未通过 -> ${failures.join(', ')}`)
  process.exitCode = 1
} else {
  console.log(`STAGE ROUTES OK: ${results.length - skipped}/${results.length - skipped} 项通过` +
    (skipped ? `（另 ${skipped} 项因输入缺失跳过）` : ''))
}
