/**
 * L15 Studio 壳守卫（Issue #160 T09）。
 *
 * 运行：
 *   node test/l15_studio_shell.mjs
 *
 * ---------------------------------------------------------------------------
 * 这个守卫防的是什么
 * ---------------------------------------------------------------------------
 * T09 的验收项里有四条是**结构性**的，靠读代码或点一遍页面都无法长期保证：
 *
 *  1. 「只保留一份 route metadata 派生导航/面包屑/权限，不新增漂移的路由映射表」
 *     —— 旧控制台曾经把路由表、侧边栏归属、面包屑各写一份，issue #61 就是
 *     三者漂移的结果。这里断言只有 routes.ts 拥有项目路由路径。
 *
 *  2. 「未实现模块只显示诚实能力状态，不显示演示分数」
 *     —— 断言每个 planned 路由都带负责的任务号，且 available 路由都有页面组件
 *     （少一个就会渲染空白页，而空白页无法区分「没有数据」与「功能没做」）。
 *
 *  3. 「生产不带 /catalog、重置演示、身份切换控件」
 *     —— 断言 /catalog 的挂载由 import.meta.env.PROD 决定，而不是写在菜单位置。
 *
 *  4. 「不依赖 activeDatasetId」（issue #64 的同类问题在新壳里的形态）
 *     —— 断言新壳源码里不出现 activeDatasetId。
 *
 * ---------------------------------------------------------------------------
 * 分层（与 l15_silent_guards.mjs 同一约定：默认路径必须能在 CI 跑）
 * ---------------------------------------------------------------------------
 *   第 1 层 源码级：路由元数据的结构与归属（不需要打包）。
 *   第 2 层 真实模块调用：用 esbuild 打包 **生产模块** src/studio/routes.ts，
 *          注入真实 pathname，断言 matchRoute / activeBreadcrumbs / activeNavKey
 *          的**行为**（这个文件不依赖 React，因此可以直接执行）。
 *   第 3 层 变异自证：把元数据改坏，断言第 1、2 层**确实**会失败。
 *
 * 数据来源：不连数据库、不起容器、不写任何数据。
 */

import { mkdtempSync, readFileSync, readdirSync, rmSync, writeFileSync } from 'node:fs'
import { createRequire } from 'node:module'
import { tmpdir } from 'node:os'
import path from 'node:path'
import { fileURLToPath } from 'node:url'

const REPO_ROOT = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..')
const WEB_ROOT = path.join(REPO_ROOT, 'apps', 'web-user')
const STUDIO_ROOT = path.join(WEB_ROOT, 'src', 'studio')
const APP_SOURCE_PATH = path.join(WEB_ROOT, 'src', 'App.tsx')
const ROUTE_STATUS_SOURCE_PATH = path.join(STUDIO_ROOT, 'pages', 'RouteStatusPage.tsx')
const ROUTES_SOURCE = path.join(STUDIO_ROOT, 'routes.ts')
const STUDIO_ROUTES_SOURCE = path.join(STUDIO_ROOT, 'StudioRoutes.tsx')
const STUDIO_LAYOUT_SOURCE = path.join(STUDIO_ROOT, 'StudioLayout.tsx')
const DOCUMENT_EDITORS_SOURCE = path.join(STUDIO_ROOT, 'DocumentEditors.tsx')
const BLUEPRINT_PAGE_SOURCE = path.join(STUDIO_ROOT, 'pages', 'BlueprintPages.tsx')
const QUALITY_PAGE_SOURCE = path.join(STUDIO_ROOT, 'pages', 'QualityPages.tsx')
const RELEASE_PAGE_SOURCE = path.join(STUDIO_ROOT, 'pages', 'ReleasePages.tsx')
const RUN_PAGE_SOURCE = path.join(STUDIO_ROOT, 'pages', 'RunPages.tsx')
const NGINX_CONF = path.join(REPO_ROOT, 'deployments', 'docker', 'nginx', 'web-user.conf')

const webRequire = createRequire(path.join(WEB_ROOT, 'package.json'))
const esbuild = webRequire('esbuild')

const failures = []
function record(name, ok, detail) {
  console.log(`[${ok ? 'PASS' : 'FAIL'}] ${name}: ${detail}`)
  if (!ok) failures.push(name)
}

// ---------------------------------------------------------------------------
// 第 1 层：源码级断言（路由元数据是唯一来源）
// ---------------------------------------------------------------------------

/**
 * stripComments 去掉行注释与块注释。
 *
 * 为什么必须先剥离：注释里出现 `activeDatasetId` 或 `/p/:projectId/...`
 * 往往是**引用**（说明这条验收项是什么、这个路径会落到哪个分支），
 * 而把它们当违规会让守卫逼着作者删掉有价值的解释文字 ——
 * 那种「为了过守卫而降低文档质量」的结果比漏报更糟。
 */
function stripComments(text) {
  return text.replace(/\/\*[\s\S]*?\*\//g, '').replace(/(^|[^:])\/\/[^\n]*/g, '$1')
}

const routesSourceRaw = readFileSync(ROUTES_SOURCE, 'utf8')
const appSourceRaw = readFileSync(APP_SOURCE_PATH, 'utf8')
const routeStatusSource = readFileSync(ROUTE_STATUS_SOURCE_PATH, 'utf8')
const routesSource = routesSourceRaw
// StudioRoutes.tsx 的源码也要在这里读取：下面「available 与注册表一致」的
// 断言用到它，而它原本声明在文件后半段 —— 在其之前引用会抛
// ReferenceError（守卫自己当场报出过这个错误）。
const studioRoutesSource = readFileSync(STUDIO_ROUTES_SOURCE, 'utf8')
const studioLayoutSource = readFileSync(STUDIO_LAYOUT_SOURCE, 'utf8')
const documentEditorsSource = readFileSync(DOCUMENT_EDITORS_SOURCE, 'utf8')
const blueprintPageSource = readFileSync(BLUEPRINT_PAGE_SOURCE, 'utf8')
const qualityPageSource = readFileSync(QUALITY_PAGE_SOURCE, 'utf8')
const releasePageSource = readFileSync(RELEASE_PAGE_SOURCE, 'utf8')
const runPageSource = readFileSync(RUN_PAGE_SOURCE, 'utf8')

record(
  '蓝图引用配置必须有真实编辑闭环',
  documentEditorsSource.includes('useVersionedDocument') &&
    documentEditorsSource.includes('CoveragePayloadEditor') &&
    documentEditorsSource.includes('StandardPayloadEditor') &&
    documentEditorsSource.includes('QualityPolicyPayloadEditor') &&
    documentEditorsSource.includes('MappingPayloadEditor') &&
    blueprintPageSource.includes('DocumentSaveBar') &&
    qualityPageSource.includes('QualityPolicyPayloadEditor') &&
    releasePageSource.includes('MappingPayloadEditor'),
  '覆盖、标准、规则和映射均使用版本化编辑器与保存栏',
)
record(
  '生产规划使用版本候选而不是手填 ID',
  runPageSource.includes('versionOptions') &&
    runPageSource.includes('<Select') &&
    runPageSource.includes('id="plan-blueprint"') &&
    !runPageSource.includes('<Input\n              id="plan-blueprint"'),
  '试制/扩量从服务端版本目录选择蓝图、覆盖、标准、质量和映射版本',
)

// 链接构造器与默认入口只能通过路由元数据解析：项目路由不在 menuRoutes
// 中，直接查菜单会把 projectHref('project.overview') 静默降级到错误入口。
record(
  '项目链接使用完整路由元数据而非菜单子集',
  studioLayoutSource.includes('fillRoutePathByKey(key, { projectId, ...extra })') &&
    !studioLayoutSource.includes("menuRoutes().find((item) => item.key === key)"),
  'projectHref 通过 fillRoutePathByKey 解析项目路由',
)
record(
  '面包屑路径使用路由元数据',
  (() => {
    const body = routesSourceRaw.slice(routesSourceRaw.indexOf('export function breadcrumbsFor'))
    return body.includes("fillRoutePathByKey('projects', {})") &&
      body.includes("fillRoutePathByKey('project.overview', params)") &&
      !body.includes("path: '/projects'") &&
      !body.includes("fillRoutePath('/p/:projectId/overview', params)")
  })(),
  'projects 与 project.overview 均按 key 解析',
)
record(
  'Atelier 默认入口与错误边界使用路由元数据',
  studioRoutesSource.includes("fillRoutePathByKey('today', {})") &&
    studioRoutesSource.includes("fillRoutePathByKey('projects', {})") &&
    studioRoutesSource.includes('<Route index element={<Navigate to={studioDefaultPath()} replace />} />'),
  '默认入口为 today，根路径在认证壳内跳转，错误边界回到 projects',
)
record(
  '未知路由显示明确状态而不是静默跳转',
  appSourceRaw.includes("import { RouteStatusPage } from './studio/pages/RouteStatusPage'") &&
    appSourceRaw.includes('<Route path="*" element={<RouteStatusPage />} />') &&
    appSourceRaw.includes('<RouteStatusPage />'),
  '旧控制台与 Atelier 外层 fallback 都渲染 RouteStatusPage',
)
record(
  '404 页面保留原始路径并提供恢复入口',
  routeStatusSource.includes('location.pathname') &&
    routeStatusSource.includes('返回今日工作') &&
    routeStatusSource.includes('搜索数据项目'),
  '页面展示 pathname/search/hash，并提供今日工作与项目搜索入口',
)
record(
  '生产 /catalog 明确说明未挂载',
  routeStatusSource.includes('目录评审未在生产环境挂载') &&
    routeStatusSource.includes("data-route-status={isCatalog ? 'catalog-unmounted' : 'not-found'}"),
  '生产 catalog 不再伪装成首页',
)

/**
 * 项目路由路径只允许出现在 routes.ts。
 *
 * 为什么这条最值得守：`/p/:projectId/...` 一旦在别的文件里再写一遍，
 * 改路由时必然漏改一处，而漏改的形态是「某个链接 404」——
 * 它只在用户点到那个链接时出现，任何类型检查都发现不了。
 */
const STUDIO_FILES = listFiles(STUDIO_ROOT)
const leakyFiles = []
for (const file of STUDIO_FILES) {
  if (file === ROUTES_SOURCE) continue
  const text = stripComments(readFileSync(file, 'utf8'))
  // 允许 `/p/:projectId` 作为**元数据引用**（fillRoutePath 的参数），
  // 但禁止再次声明项目路由的完整模式（例如 `/p/:projectId/blueprint`）。
  const pattern = /['"`]\/p\/:projectId\/[a-z0-9-]+/g
  const hits = text.match(pattern)
  if (hits) leakyFiles.push(`${path.relative(REPO_ROOT, file)}: ${hits.join(', ')}`)
}
record(
  '项目路由路径只在 routes.ts 声明',
  leakyFiles.length === 0,
  leakyFiles.length === 0 ? `检查了 ${STUDIO_FILES.length} 个文件` : `发现重复声明：${leakyFiles.join(' | ')}`,
)

/**
 * parseRouteBlocks 把导出数组拆成「每个路由一块」。
 *
 * 逐块解析而不是用一条跨块的正则：跨块正则会用**另一个**块的
 * moduleStatus/task 来满足当前块的匹配（贪婪跨度），于是「某个路由漏了
 * task 号」这种缺陷会被相邻路由的 task 掩盖过去 —— 而那正是本守卫要防的。
 */
function parseRouteBlocks(text, exportName) {
  const declaration = text.indexOf(`export const ${exportName}`)
  if (declaration < 0) return []
  const equals = text.indexOf('= [', declaration)
  if (equals < 0) return []
  // 必须把 body 限制在**本数组**的方括号内：从声明处切到文件末尾
  // 会把后续数组的条目也算进来（曾经数出 61/64 这种明显不可能的数字，
  // 而那种幻觉会让「某个数组整个为空」这类缺陷被掩盖）。
  const arrayStart = text.indexOf('[', equals)
  let depth = 0
  let arrayEnd = -1
  for (let index = arrayStart; index < text.length; index += 1) {
    if (text[index] === '[') depth += 1
    else if (text[index] === ']') {
      depth -= 1
      if (depth === 0) {
        arrayEnd = index
        break
      }
    }
  }
  if (arrayEnd < 0) return []
  const body = text.slice(arrayStart, arrayEnd)
  const keyMatches = [...body.matchAll(/key: '([^']+)'/g)]
  const blocks = []
  for (let index = 0; index < keyMatches.length; index += 1) {
    const start = keyMatches[index].index
    const end = index + 1 < keyMatches.length ? keyMatches[index + 1].index : body.length
    const chunk = body.slice(start, end)
    const status = chunk.match(/moduleStatus: '([^']+)'/)?.[1] ?? ''
    const task = chunk.match(/task: '([^']+)'/)?.[1] ?? ''
    blocks.push({ key: keyMatches[index][1], status, task })
  }
  return blocks
}

const routeBlocks = [
  ...parseRouteBlocks(routesSource, 'wizardRoutes'),
  ...parseRouteBlocks(routesSource, 'globalRoutes'),
  ...parseRouteBlocks(routesSource, 'globalDetailRoutes'),
  ...parseRouteBlocks(routesSource, 'projectRoutes'),
  ...parseRouteBlocks(routesSource, 'projectDetailRoutes'),
  ...parseRouteBlocks(routesSource, 'auxiliaryRoutes'),
  ...parseRouteBlocks(routesSource, 'catalogRoutes'),
]
const plannedBlocks = routeBlocks.filter((block) => block.status === 'planned')
const availableBlocks = routeBlocks.filter((block) => block.status === 'available')
record(
  '每个 planned 模块都标注负责的任务号',
  // 不设「至少 N 个 planned」这类阈值：随任务落地 planned 会自然减少，
  // 而这条断言的不变量是「每个 planned 都有任务号」。
  // 阈值会让守卫在进度推进后无意义地失败（已发生过一次）。
  plannedBlocks.every((block) => /T\d+/.test(block.task)) && plannedBlocks.length > 0,
  `planned=${plannedBlocks.length}；缺任务号：${plannedBlocks.filter((block) => !/T\d+/.test(block.task)).map((block) => block.key).join(', ') || '无'}`,
)
/**
 * available 模块必须**恰好**等于注册了页面组件的那些键。
 *
 * 用「与注册表比对」而不是写死数量：写死数量会让每加一个页面都要改守卫，
 * 而守卫的职责是「available 却没有页面」这类缺陷 —— 那才是「未实现能力
 * 伪装可用」的入口。这一条比数量断言强得多，且不需要随任务更新。
 */
record(
  '每个 available 模块都标注来源任务',
  availableBlocks.length > 0 && availableBlocks.every((block) => /T\d+/.test(block.task)),
  `available=${availableBlocks.length}（${availableBlocks.map((block) => `${block.key}→${block.task}`).join(', ')}）`,
)
record(
  '每个路由都有明确的实现状态',
  // 基础契约包含 34 条；评估/清洗两个 Atelier 辅助工作台、兼容索引
  // 与历史资产索引/详情是额外入口。保留精确计数，避免整组路由被误删时「>=」仍然通过，
  // 同时把每个产品级入口的加入明确写进守卫，而不是让它变成隐式漂移。
  routeBlocks.length === 38 && routeBlocks.every((block) => block.status === 'planned' || block.status === 'available'),
  `共 ${routeBlocks.length} 条路由（34 基础 + 2 评估/清洗工作台 + 2 历史资产）；状态缺失：${routeBlocks.filter((block) => !block.status).map((block) => block.key).join(', ') || '无'}`,
)

/**
 * 四个全局入口与六个项目标签必须齐全（契约 §3.1 的层级定义）。
 *
 * 少一个入口或标签意味着产品目标被悄悄缩减，而那不会让任何测试变红 ——
 * 只会让用户在界面里找不到功能。
 */
const globalCount = countBlocks(routesSource, 'globalRoutes')
const projectCount = countBlocks(routesSource, 'projectRoutes')
record('全局四入口齐全', globalCount === 4, `globalRoutes=${globalCount}`)
record('项目六工作区齐全', projectCount === 6, `projectRoutes=${projectCount}`)

/**
 * 只允许一份映射表：禁止在 StudioRoutes.tsx 里出现 `path: '/...'` 形式的路由字面量。
 *
 * 路由元素必须由元数据 map 生成；手写 `<Route path="/p/...">` 会让
 * 「元数据改了但 JSX 没改」重新变成可能。
 */
const hardcodedRoutePaths = [...studioRoutesSource.matchAll(/<Route[^>]*path=["'](\/[^"']*)["']/g)]
  .map((match) => match[1])
  // `/p/:projectId`（项目壳）与 `/login`（认证）是壳层结构本身，不是功能路由。
  .filter((value) => value !== '/p/:projectId')
record(
  '路由元素由元数据生成，无手写功能路径',
  hardcodedRoutePaths.length === 0,
  hardcodedRoutePaths.length === 0 ? 'StudioRoutes.tsx 无手写功能路由' : `手写路径：${hardcodedRoutePaths.join(', ')}`,
)

/** 新壳不得依赖 activeDatasetId（issue #64 的同一类问题）。 */
const activeDatasetHits = STUDIO_FILES.filter((file) =>
  /activeDatasetId/.test(stripComments(readFileSync(file, 'utf8'))),
)
record(
  '新壳不依赖 activeDatasetId',
  activeDatasetHits.length === 0,
  activeDatasetHits.length === 0 ? 'studio/ 下无该标识符' : `出现在：${activeDatasetHits.map((f) => path.basename(f)).join(', ')}`,
)

/** 新壳不得有「未选中任务就静默 return」的守卫形态。 */
const silentGuardFiles = STUDIO_FILES.filter((file) =>
  /if\s*\(\s*!\s*[A-Za-z_$][\w$]*\s*\)\s*\{\s*return\s*\}/.test(stripComments(readFileSync(file, 'utf8'))),
)
record(
  '新壳无静默 return 守卫',
  silentGuardFiles.length === 0,
  silentGuardFiles.length === 0 ? 'studio/ 下无空 return 守卫' : `出现在：${silentGuardFiles.map((f) => path.basename(f)).join(', ')}`,
)

/**
 * /catalog 的挂载必须由生产标记决定（T09 验收项「生产不带 /catalog」），
 * 且必须是**防御式读取**。
 *
 * 为什么把「防御式」也写进断言：`import.meta.env` 只在 Vite 构建里存在，
 * 而仓库的 UI 守卫（本脚本第 2 层、test/l15_stage_routes.mjs）用 esbuild
 * 渲染真实组件树 —— esbuild 不注入它，直接访问 `.PROD` 会抛
 * `Cannot read properties of undefined` 并把整个渲染腿打挂。
 * 这个回归在本次改动里真的发生过，因此它值得被断言，而不是靠注释提醒。
 */
const catalogMountBlock = routesSource.match(/export function isCatalogRouteMounted[\s\S]*?\n\}/)?.[0] ?? ''
record(
  '/catalog 挂载由生产标记决定且防御式读取',
  /import\.meta/.test(catalogMountBlock) &&
    /PROD/.test(catalogMountBlock) &&
    /if\s*\(\s*!env/.test(catalogMountBlock),
  catalogMountBlock
    ? '读取 import.meta.env 前判断宿主存在（与 buildInfo.ts 的 #112 修复同一形态）'
    : '未找到 isCatalogRouteMounted 实现',
)

/**
 * Nginx 必须把未知路径回退到 index.html，否则「粘贴深链接」在真实部署里是 404。
 *
 * 这条断言的是**部署配置**而不是前端代码：前端用 BrowserRouter，
 * 任何直接访问 `/p/12/overview` 的请求都先落到 nginx。
 */
const nginx = readFileSync(NGINX_CONF, 'utf8')
record(
  'nginx 对未知路径回退 index.html（深链接可用）',
  /try_files\s+\$uri\s+\$uri\/\s+\/index\.html/.test(nginx),
  'location / 使用 try_files $uri $uri/ /index.html',
)

// ---------------------------------------------------------------------------
// 第 2 层：真实模块调用（打包生产模块并断言行为）
// ---------------------------------------------------------------------------

const workDir = mkdtempSync(path.join(tmpdir(), 'l15-studio-shell-'))

/**
 * bundleRoutes 打包 routes.ts 并返回它的导出。
 *
 * 用 esbuild 打包**生产源码**而不是在测试里重写一份等价逻辑：
 * 重写的那份只是测试自己的实现，产品代码改了它不会失败。
 * `import.meta.env.PROD` 由 esbuild define 注入，与 vite 的语义一致。
 */
async function bundleRoutes(prod) {
  const entry = path.join(workDir, `entry-${prod ? 'prod' : 'dev'}.js`)
  const outfile = path.join(workDir, `routes-${prod ? 'prod' : 'dev'}.cjs`)
  writeFileSync(entry, `export * from ${JSON.stringify(ROUTES_SOURCE)}\n`)
  await esbuild.build({
    entryPoints: [entry],
    outfile,
    bundle: true,
    format: 'cjs',
    platform: 'node',
    logLevel: 'silent',
    // 定义 `import.meta.env` 本身而不是 `import.meta.env.PROD`：
    // 生产代码对它是**防御式**访问（先取宿主再读 PROD），
    // 只定义后者时 esbuild 无法替代表达式链，测试会拿到 undefined 环境，
    // 于是「生产不挂载」的断言测的就不是真实行为。
    define: { 'import.meta.env': JSON.stringify({ PROD: prod }) },
  })
  const mod = webRequire(outfile)
  return mod.default ?? mod
}

const devRoutes = await bundleRoutes(false)
const prodRoutes = await bundleRoutes(true)

const availableKeys = availableBlocks.map((block) => block.key).sort()
const registryBlock =
  studioRoutesSource.match(/const AVAILABLE_PAGES[^{]*\{([\s\S]*?)\n\}/)?.[1] ?? ''
const knownRouteKeys = new Set(prodRoutes.allStudioRoutes.map((route) => route.key))
// 键可能是带引号的（含点号的 'project.overview'）或不带引号的（projects）——
// 只匹配带引号的那种会漏掉后者，于是断言在「注册表写的是普通键」时误报。
const registeredKeys = [...registryBlock.matchAll(/(?:'([A-Za-z0-9_.]+)'|\b([A-Za-z_][A-Za-z0-9_]*))\s*:/g)]
  .map((match) => match[1] ?? match[2])
  // 用真实路由键集合过滤：注册表里可能有非路由的辅助键（例如包装组件），
  // 而它们不属于「模块是否有页面」这件事。
  .filter((key) => knownRouteKeys.has(key))
  .sort()
record(
  'available 模块与已注册页面组件一一对应',
  JSON.stringify(availableKeys) === JSON.stringify(registeredKeys),
  `available=[${availableKeys.join(', ')}] 注册=[${registeredKeys.join(', ')}]`,
)
record(
  '开发构建挂载 /catalog',
  devRoutes.isCatalogRouteMounted() === true,
  `isCatalogRouteMounted()=${devRoutes.isCatalogRouteMounted()}`,
)
record(
  '生产构建不挂载 /catalog',
  prodRoutes.isCatalogRouteMounted() === false,
  `isCatalogRouteMounted()=${prodRoutes.isCatalogRouteMounted()}`,
)

/** matchRoute 必须能识别深链接（刷新/新标签打开时页面靠它取到标题与归属）。 */
const deepLinks = devRoutes.allStudioRoutes.filter((route) => route.kind === 'project')
const unmatched = deepLinks
  .map((route) => route.path.replace(':projectId', '42'))
  .filter((pathname) => {
    const matched = prodRoutes.matchRoute(pathname)
    return !matched || matched.path.replace(':projectId', '42') !== pathname
  })
record(
  '所有项目路由都能按 pathname 匹配回元数据',
  unmatched.length === 0,
  unmatched.length === 0 ? `检查了 ${deepLinks.length} 条项目路由` : `未匹配：${unmatched.join(', ')}`,
)

const historyDetailRoute = prodRoutes.matchRoute('/legacy/history/42')
record(
  '历史资产详情不伪装成辅助菜单项',
  historyDetailRoute?.navParent === 'legacy.history' &&
    !prodRoutes.menuRoutes().some((route) => route.key === 'legacy.history.detail') &&
    prodRoutes.menuRoutes().some((route) => route.key === 'legacy.history'),
  '详情深链仍匹配并高亮历史资产，但不会在侧栏展示占位参数 URL',
)

/**
 * 子页高亮到所属标签（issue #61 的修复形态）。
 *
 * `/p/42/runs/new`（扩量规划，navParent=project.runs）必须让侧边栏高亮
 * 「生产」而不是掉回默认项，否则用户无法从界面判断自己在哪。
 */
const childCases = [
  ['/p/42/runs/new', 'project.runs'],
  ['/p/42/pilot', 'project.runs'],
  ['/p/42/compare', 'project.runs'],
  ['/p/42/coverage', 'project.blueprint'],
  ['/p/42/standard', 'project.blueprint'],
  ['/p/42/review', 'project.data'],
  ['/p/42/rules', 'project.quality'],
  ['/p/42/releases/new', 'project.releases'],
]
const wrongHighlight = childCases.filter(([pathname, expected]) => prodRoutes.activeNavKey(pathname) !== expected)
record(
  '子页高亮到所属标签（导航归属唯一来源）',
  wrongHighlight.length === 0,
  wrongHighlight.length === 0
    ? `检查了 ${childCases.length} 个子页`
    : wrongHighlight.map(([pathname, expected]) => `${pathname}→${prodRoutes.activeNavKey(pathname)}（期望 ${expected}）`).join('，'),
)

/** 面包屑必须包含项目与所属标签，且由元数据派生。 */
const crumbs = prodRoutes.breadcrumbsFor('/p/42/runs/new', { projectId: 42 })
const crumbLabels = crumbs.map((item) => item.label)
record(
  '面包屑包含层级（数据项目 → 项目 → 所属标签 → 当前页）',
  crumbLabels[0] === '数据项目' && crumbLabels.includes('生产') && crumbLabels[crumbLabels.length - 1] === '扩量规划',
  crumbLabels.join(' / '),
)

const detailCrumbs = prodRoutes.breadcrumbsFor('/recipes/7', { recipeId: 7 })
const wizardCrumbs = prodRoutes.breadcrumbsFor('/new/coverage', {})
record(
  '入口详情与向导步骤面包屑保留父级路径',
  detailCrumbs[0]?.label === '方案库' &&
    detailCrumbs[0]?.path === '/recipes' &&
    wizardCrumbs[0]?.label === '新建项目' &&
    wizardCrumbs[0]?.path === '/new',
  `详情=${detailCrumbs.map((item) => `${item.label}:${item.path ?? '-'}`).join(' / ')}；向导=${wizardCrumbs
    .map((item) => `${item.label}:${item.path ?? '-'}`)
    .join(' / ')}`,
)

/** 每个可跳转路径都能从元数据生成（前端不自行拼 URL）。 */
const filledPath = prodRoutes.fillRoutePath('/p/:projectId/data', { projectId: 7 })
record('路由路径可由元数据填充', filledPath === '/p/7/data', filledPath)

// ---------------------------------------------------------------------------
// 第 3 层：变异自证
// ---------------------------------------------------------------------------

// 变异 1：把一个子页的 navParent 去掉 → 第 2 层的高亮断言必须失败。
{
  const mutated = routesSource.replace(
    /(key: 'project\.runNew',[\s\S]*?)navParent: 'project\.runs',/,
    '$1',
  )
  const ok = await mutatedDetectionFails(mutated, '/p/42/runs/new', 'project.runs')
  record('变异 1：去掉 navParent 会被捕获', ok, ok ? '变异被第 2 层捕获' : '变异未被捕获（守卫失效）')
}

// 变异 2：把 /catalog 的挂载条件改成恒 true → 第 2 层的生产断言必须失败。
{
  const mutated = routesSource.replace(
    /export function isCatalogRouteMounted\(\): boolean \{[\s\S]*?\n\}/,
    'export function isCatalogRouteMounted(): boolean {\n  return true\n}',
  )
  const ok = await mutatedCatalogMountedInProd(mutated)
  record('变异 2：catalog 恒挂载会被捕获', ok, ok ? '变异被第 2 层捕获' : '变异未被捕获（守卫失效）')
}

// 变异 3：在 StudioRoutes.tsx 里手写一条功能路由 → 第 1 层必须失败。
{
  const mutated = studioRoutesSource.replace(
    '<Route path="/p/:projectId" element={<ProjectLayout />}>',
    '<Route path="/p/:projectId/blueprint" element={<ProjectLayout />}>\n        <Route path="/p/:projectId" element={<ProjectLayout />}>',
  )
  const hits = [...mutated.matchAll(/<Route[^>]*path=["'](\/[^"']*)["']/g)]
    .map((match) => match[1])
    .filter((value) => value !== '/p/:projectId')
  record('变异 3：手写功能路由会被捕获', hits.length > 0, hits.length > 0 ? `捕获到 ${hits.join(', ')}` : '未捕获（守卫失效）')
}

async function mutatedDetectionFails(mutatedSource, pathname, expected) {
  const mutatedFile = path.join(workDir, `mutated-nav-${Date.now()}.ts`)
  writeFileSync(mutatedFile, mutatedSource)
  const mod = await bundleFrom(mutatedFile)
  return mod.activeNavKey(pathname) !== expected
}

async function mutatedCatalogMountedInProd(mutatedSource) {
  const mutatedFile = path.join(workDir, `mutated-catalog-${Date.now()}.ts`)
  writeFileSync(mutatedFile, mutatedSource)
  const mod = await bundleFrom(mutatedFile, true)
  return mod.isCatalogRouteMounted() !== false
}

async function bundleFrom(sourceFile, prod = false) {
  const entry = path.join(workDir, `entry-${path.basename(sourceFile)}.js`)
  const outfile = path.join(workDir, `out-${path.basename(sourceFile)}.cjs`)
  writeFileSync(entry, `export * from ${JSON.stringify(sourceFile)}\n`)
  await esbuild.build({
    entryPoints: [entry],
    outfile,
    bundle: true,
    format: 'cjs',
    platform: 'node',
    logLevel: 'silent',
    define: { 'import.meta.env': JSON.stringify({ PROD: prod }) },
  })
  const mod = webRequire(outfile)
  return mod.default ?? mod
}

// ---------------------------------------------------------------------------
// 汇总
// ---------------------------------------------------------------------------

rmSync(workDir, { recursive: true, force: true })

/** 数一数某个导出数组里有几个 `key: '...'` 条目。 */
function countBlocks(text, exportName) {
  const start = text.indexOf(`export const ${exportName}`)
  if (start < 0) return -1
  // 必须从 `= [` 开始找：类型标注 `StudioRouteMeta[]` 里的 `[` 会让
  // 深度跟踪在类型处就闭合，于是每个数组都被数成 0 项（曾经如此）。
  const arrayStart = text.indexOf('= [', start)
  if (arrayStart < 0) return -1
  let depth = 0
  for (let index = arrayStart; index < text.length; index++) {
    if (text[index] === '[') depth += 1
    else if (text[index] === ']') {
      depth -= 1
      if (depth === 0) {
        const body = text.slice(arrayStart, index)
        return [...body.matchAll(/key: '/g)].length
      }
    }
  }
  return -1
}

function listFiles(directory) {
  const result = []
  for (const entry of readdirSync(directory, { withFileTypes: true })) {
    const full = path.join(directory, entry.name)
    if (entry.isDirectory()) result.push(...listFiles(full))
    else if (/\.(ts|tsx)$/.test(entry.name)) result.push(full)
  }
  return result
}

if (failures.length > 0) {
  console.error(`\nL15 Studio 壳守卫失败 ${failures.length} 项：`)
  for (const name of failures) console.error(`  - ${name}`)
  process.exit(1)
}
console.log('\nL15 Studio 壳守卫全部通过。')
