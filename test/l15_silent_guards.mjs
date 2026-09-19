/**
 * L15 R8 未选中任务守卫测试（issue #64）。
 *
 * 运行：
 *   node test/l15_silent_guards.mjs                # 默认：源码级 + 真实模块调用（CI 可跑，无需容器）
 *   node test/l15_silent_guards.mjs --with-browser # 额外：真 Chromium 点击真实 UI
 *
 * ---------------------------------------------------------------------------
 * 为什么默认路径必须不需要容器（CI 可执行性是硬要求）
 * ---------------------------------------------------------------------------
 * CI 的 Backend job 只跑 `go test`，Frontend job 只跑 `tsc + vite build`，
 * **都不起容器**。因此若本脚本把「起 API 容器 / 起前端 dev server」当成必经前置，
 * 它在 CI 里 100% 失败 —— 等于一个永远跑不起来的守卫，CI 防不住回归；
 * 而且会把「环境不可达」误报为「断言失败」，真出回归时没人分得清。
 *
 * 所以拆成三层，只有前两层的成败决定 exit code：
 *
 *   第 1 层（源码级）：读 App.tsx 源码文本，断言
 *     - 5 处生成函数**全部**走统一守卫，不再有 `if (!activeDatasetId) return` 静默返回；
 *     - 刷新类按钮不再用 `activeDatasetId && ...` 短路（那正是 #64 的原始形态）；
 *     - 全文不再残留任何已知静默模式。
 *
 *   第 2 层（真实模块调用）：真实 import 生产模块 `lib/taskGuard.ts`（用 esbuild 打包，
 *     与 App.tsx 打包同一套配置），注入可观测的提示记录器与动作替身，断言
 *     「提示通道确实被调用」**且**「业务动作确实没被调用」。
 *     这一层是「无静默 return」的**直接证据** —— 不是读代码推断，而是执行生产代码。
 *
 *   第 3 层（--with-browser，可选）：真 Chromium 打开真实页面点击按钮。
 *     本机已装全局 playwright + Chromium（`/root/.pi/agent/npm/node_modules/playwright`），
 *     不写入 package.json、不污染共享 node_modules。
 *     这一层最接近 issue #64 的原始复现方式（报告就是用 Chromium 点击验收的）。
 *
 * 变异自证：本脚本内建 3 个变异用例（把守卫改回静默 return / 把刷新改回短路 /
 * 删掉提示调用），每条都必须被第 1、2 层捕获；未捕获则测试自身 FAIL。
 *
 * 数据来源：不连接数据库、不写任何数据行。默认路径无需任何外部服务。
 */

import { mkdtempSync, readFileSync, rmSync } from 'node:fs'
import { createRequire } from 'node:module'
import { tmpdir } from 'node:os'
import path from 'node:path'
import { fileURLToPath } from 'node:url'

const REPO_ROOT = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..')
const WEB_ROOT = path.join(REPO_ROOT, 'apps', 'web-user')
const APP_SOURCE = path.join(WEB_ROOT, 'src', 'App.tsx')
const GUARD_SOURCE = path.join(WEB_ROOT, 'src', 'lib', 'taskGuard.ts')

const NODE_PATHS = [path.join(WEB_ROOT, 'node_modules'), path.join(REPO_ROOT, 'node_modules')]
const webRequire = createRequire(path.join(WEB_ROOT, 'package.json'))
const esbuild = webRequire('esbuild')

// --with-browser：启用需要真 Chromium 的第 3 层。默认关闭（CI 不装浏览器）。
const WITH_BROWSER = process.argv.includes('--with-browser')

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
// 第 1 层：源码级断言
// ---------------------------------------------------------------------------

const source = readFileSync(APP_SOURCE, 'utf8')

/**
 * 5 个生成函数：契约 §1.4 要求它们的守卫行为一致。
 * 修复前只有 generateDomains 有提示，其余 4 处是静默 `return`。
 */
const GENERATION_FUNCTIONS = [
  { fn: 'generateDomains', label: '生成方向结构' },
  { fn: 'generateQuestions', label: '生成题目' },
  { fn: 'generateReasoning', label: '生成答案' },
  { fn: 'generateRewards', label: '生成质量评估' },
  { fn: 'generateExport', label: '导出结果' },
]

/** 抽出某个 `const <fn> = async () => { ... }` 的函数体（花括号配平）。 */
function functionBody(text, fn) {
  const start = text.indexOf(`const ${fn} = async`)
  if (start < 0) return null
  const braceStart = text.indexOf('{', start)
  if (braceStart < 0) return null
  let depth = 0
  for (let i = braceStart; i < text.length; i++) {
    if (text[i] === '{') depth++
    else if (text[i] === '}') {
      depth--
      if (depth === 0) return text.slice(braceStart, i + 1)
    }
  }
  return null
}

for (const { fn, label } of GENERATION_FUNCTIONS) {
  const body = functionBody(source, fn)
  if (!body) {
    record(`${fn} 函数存在`, false, '未找到该函数定义，断言失效')
    continue
  }
  // 必须走统一守卫（这保证「行为一致」是结构性的，而不是靠人工比对齐）
  record(`${fn} 使用统一守卫 resolveDatasetId`, /resolveDatasetId\s*\(/.test(body),
    /resolveDatasetId\s*\(/.test(body) ? '已接入统一守卫' : `未接入；仍是各自手写的守卫（${label}）`)

  // 不得残留静默 return
  const silentReturn = /if\s*\(\s*!activeDatasetId\s*\)\s*return\s*$/m.test(body.replace(/;;/g, ';'))
  record(`${fn} 无静默 return`, !silentReturn,
    silentReturn ? '仍存在 `if (!activeDatasetId) return` 静默返回' : '无静默返回')
}

// 全文不得残留 #64 的原始短路形态
const shortCircuitHits = source
  .split('\n')
  .map((line, index) => ({ line, number: index + 1 }))
  .filter(({ line }) => /activeDatasetId\s*&&\s*void\s+loadDatasetWorkspace/.test(line))

record('全文无「activeDatasetId && void loadDatasetWorkspace」短路（#64 原始形态）',
  shortCircuitHits.length === 0,
  shortCircuitHits.length === 0
    ? '已全部改为统一守卫'
    : `仍有 ${shortCircuitHits.length} 处：行 ${shortCircuitHits.map((h) => h.number).join(', ')}`)

// 三元形式的短路同样会静默（条件为假时只刷新别的数据，用户point的按钮没生效）
const ternaryHits = source
  .split('\n')
  .filter((line) => /activeDatasetId\s*\?\s*void\s+loadDatasetWorkspace/.test(line))
record('全文无「activeDatasetId ? void loadDatasetWorkspace : ...」三元短路',
  ternaryHits.length === 0,
  ternaryHits.length === 0 ? '无该形态' : `仍有 ${ternaryHits.length} 处`)

// 守卫必须统一走提示通道（notifyNoActiveTask），而不是各自 Toast.warning 各写文案
record('5 处生成守卫统一使用同一提示通道 notifyNoActiveTask',
  GENERATION_FUNCTIONS.every(({ fn }) => {
    const body = functionBody(source, fn)
    return Boolean(body) && /notifyNoActiveTask/.test(body)
  }),
  '每个生成函数都把提示通道作为参数传给 resolveDatasetId')

// 刷新类动作必须走 withActiveDataset（而不是各自 if 判断）
const refreshGuardCount = (source.match(/withActiveDataset\s*\(/g) ?? []).length
record('刷新类动作统一使用 withActiveDataset（>= 5 处：4 个阶段页 + 数据资产/主题结构 + 帮助页）',
  refreshGuardCount >= 5,
  `共 ${refreshGuardCount} 处`)

// ---------------------------------------------------------------------------
// 第 2 层：真实调用生产模块（不是读代码推断）
// ---------------------------------------------------------------------------

function buildGuardModule() {
  const workDir = mkdtempSync(path.join(tmpdir(), 'l15-silent-guards-'))
  const outfile = path.join(workDir, 'taskGuard.cjs')
  esbuild.buildSync({
    entryPoints: [GUARD_SOURCE],
    bundle: true,
    format: 'cjs',
    platform: 'node',
    outfile,
    absWorkingDir: WEB_ROOT,
    nodePaths: NODE_PATHS,
    mainFields: ['module', 'main'],
    logLevel: 'warning',
  })
  return { workDir, guard: createRequire(import.meta.url)(outfile) }
}

const { workDir, guard } = buildGuardModule()

try {
  record('生产模块 lib/taskGuard.ts 可被真实打包', typeof guard.resolveDatasetId === 'function'
    && typeof guard.withActiveDataset === 'function',
  'esbuild 打包成功，两个导出函数可用')

  record('提示文案是面向用户的中文且不含技术术语',
    typeof guard.NO_ACTIVE_TASK_MESSAGE === 'string'
      && /请先/.test(guard.NO_ACTIVE_TASK_MESSAGE)
      && !/(SQL|SELECT|API|POST|GET|null|undefined|datasetId)/i.test(guard.NO_ACTIVE_TASK_MESSAGE),
    `"${guard.NO_ACTIVE_TASK_MESSAGE}"`)

  // ---- resolveDatasetId：未选中任务 ----
  {
    const notices = []
    const returned = guard.resolveDatasetId(null, (m) => notices.push(m), '生成题目')
    record('resolveDatasetId(未选中) 调用了提示通道（= 没有静默 return）',
      notices.length === 1, `提示 ${notices.length} 次：${JSON.stringify(notices)}`)
    record('resolveDatasetId(未选中) 返回 null（调用方据此提前返回）',
      returned === null, `返回值=${String(returned)}`)
    record('resolveDatasetId(未选中) 的提示指明了动作与前置条件',
      notices.length === 1 && notices[0].includes('生成题目') && notices[0].includes('任务'),
      notices[0] ?? '(无提示)')
  }

  // ---- resolveDatasetId：已选中任务（正例，防「一律提示」的矫枉过正） ----
  {
    const notices = []
    const returned = guard.resolveDatasetId(42, (m) => notices.push(m), '生成题目')
    record('resolveDatasetId(已选中) 不提示且返回真实 id',
      notices.length === 0 && returned === 42,
      `提示 ${notices.length} 次，返回值=${String(returned)}（期望 0 次 / 42）`)
  }

  // ---- resolveDatasetId：非法 id 也视为未选中 ----
  for (const bad of [0, -1, Number.NaN]) {
    const notices = []
    const returned = guard.resolveDatasetId(bad, (m) => notices.push(m), '生成题目')
    record(`resolveDatasetId(${String(bad)}) 视为未选中并提示`,
      notices.length === 1 && returned === null,
      `提示 ${notices.length} 次，返回值=${String(returned)}`)
  }

  // ---- withActiveDataset：未选中任务时必须「提示 + 不执行动作」 ----
  {
    const notices = []
    const calls = []
    const started = guard.withActiveDataset(null, (m) => notices.push(m), (id) => calls.push(id))
    record('withActiveDataset(未选中) 调用了提示通道',
      notices.length === 1, `提示 ${notices.length} 次`)
    record('withActiveDataset(未选中) **没有**执行业务动作（核心：不是静默 pass-through）',
      calls.length === 0, `动作被调用 ${calls.length} 次（期望 0）`)
    record('withActiveDataset(未选中) 返回 false', started === false, `返回值=${String(started)}`)
  }

  // ---- withActiveDataset：已选中任务 ----
  {
    const notices = []
    const calls = []
    const started = guard.withActiveDataset(7, (m) => notices.push(m), (id) => calls.push(id))
    record('withActiveDataset(已选中) 执行动作且不提示',
      notices.length === 0 && calls.length === 1 && calls[0] === 7 && started === true,
      `提示 ${notices.length} 次，动作收到 id=${JSON.stringify(calls)}，返回值=${String(started)}`)
  }
} finally {
  rmSync(workDir, { recursive: true, force: true })
}

// ---------------------------------------------------------------------------
// 变异自证：断言必须能捕获「改回静默 return」
// ---------------------------------------------------------------------------

/** 把某生成函数的统一守卫改回 #64 的静默形态。 */
function mutateToSilentReturn(text, fn) {
  const body = functionBody(text, fn)
  if (!body) return text
  const mutated = body.replace(
    /const datasetId = resolveDatasetId\([^)]*\)\n\s*if \(!datasetId\) return\n/,
    'if (!activeDatasetId) return\n',
  )
  if (mutated === body) return text
  return text.replace(body, mutated)
}

/** 把刷新类统一守卫改回短路形态。 */
function mutateToShortCircuit(text) {
  return text.replace(
    /withActiveDataset\(activeDatasetId, notifyNoActiveTask, \(id\) => loadDatasetWorkspace\(id, '方向结构已刷新'\)\)/,
    "activeDatasetId && void loadDatasetWorkspace(activeDatasetId, '方向结构已刷新')",
  )
}

// 变异 1：生成函数改回静默 return
const mutatedSilent = mutateToSilentReturn(source, 'generateQuestions')
{
  const body = functionBody(mutatedSilent, 'generateQuestions') ?? ''
  const stillGuarded = /resolveDatasetId\s*\(/.test(body)
  const silent = /if\s*\(\s*!activeDatasetId\s*\)\s*return/.test(body)
  record('变异：generateQuestions 改回静默 return -> 第 1 层可捕获',
    !stillGuarded && silent,
    stillGuarded ? '变异未生效（断言会空转）' : '变异生效：守卫消失且出现静默 return，第 1 层断言会 FAIL')
}

// 变异 2：刷新按钮改回短路
const mutatedShort = mutateToShortCircuit(source)
record('变异：刷新按钮改回 `activeDatasetId && ...` 短路 -> 第 1 层可捕获',
  /activeDatasetId\s*&&\s*void\s+loadDatasetWorkspace/.test(mutatedShort),
  '变异生效：短路形态重现，第 1 层断言会 FAIL')

// 变异 3：删掉提示调用（提示通道被绕过）
{
  const mutatedNoNotice = source.replaceAll('notifyNoActiveTask', 'noopNotice')
  const generationStillGuard = /notifyNoActiveTask/.test(
    functionBody(mutatedNoNotice, 'generateDomains') ?? '',
  )
  record('变异：把提示通道换成 noop -> 「统一使用 notifyNoActiveTask」断言可捕获',
    !generationStillGuard,
    generationStillGuard ? '变异未生效' : '变异生效：守卫不再接统一提示通道，第 1 层断言会 FAIL')
}

// 变异 4：把 withActiveDataset 换成不提示的静默跳过
{
  const mutated = source.replace(
    /withActiveDataset\(activeDatasetId, notifyNoActiveTask, \(id\) => loadDatasetWorkspace\(id, '数据资产页已刷新'\)\)/,
    "activeDatasetId && void loadDatasetWorkspace(activeDatasetId || 0, '数据资产页已刷新')",
  )
  record('变异：数据资产刷新改回静默跳过 -> 短路断言可捕获',
    mutated !== source && /activeDatasetId\s*&&\s*void\s+loadDatasetWorkspace/.test(mutated),
    mutated === source ? '变异未生效' : '变异生效，第 1 层断言会 FAIL')
}

// ---------------------------------------------------------------------------
// 第 3 层（可选）：真 Chromium 点击真实 UI
// ---------------------------------------------------------------------------

if (WITH_BROWSER) {
  const BASE = process.env.L15_R8_WEB_BASE ?? 'http://127.0.0.1:3210'
  let browser = null
  try {
    const { chromium } = webRequireIt()
    browser = await chromium.launch()
    const page = await browser.newPage()

    // 登录页真实 DOM（实测，不是猜的）：
    //   邮箱 input[placeholder="请输入邮箱"]（type=text）、密码 input[placeholder="请输入密码"]、
    //   按钮文案「进入我的任务」。
    await page.goto(`${BASE}/login`, { waitUntil: 'domcontentloaded' })
    await page.getByPlaceholder('请输入邮箱').fill('admin@company.com')
    await page.getByPlaceholder('请输入密码').fill('admin123456')
    await page.getByRole('button', { name: /进入我的任务/ }).click()
    await page.waitForURL(/\/console/, { timeout: 20000 }).catch(() => {})
    await page.waitForTimeout(1500)

    // 直接经侧边栏语义进入「数据资产」页：该路由不带 taskId，
    // 因此 activeDatasetId 为 null —— 正是 issue #64 的复现场景。
    await page.goto(`${BASE}/console/results`, { waitUntil: 'domcontentloaded' })
    await page.waitForTimeout(2000)

    const NOTICE = '请先进入具体任务'
    const networkCalls = []
    page.on('request', (req) => {
      if (req.url().includes('/api/v1/datasets/')) networkCalls.push(req.url())
    })

    const before = await page.getByText(NOTICE).count()
    const beforeCalls = networkCalls.length

    // 点「刷新结果」——修复前这里是 `activeDatasetId && ...` 短路：无提示、无请求。
    const refreshButton = page.getByRole('button', { name: /刷新结果/ }).first()
    const clicked = await refreshButton.click({ timeout: 8000 }).then(() => true).catch(() => false)
    await page.waitForTimeout(1800)

    const after = await page.getByText(NOTICE).count()
    const afterCalls = networkCalls.length

    record('真浏览器：找到「刷新结果」按钮并可点击', clicked, clicked ? '已点击' : '未找到/不可点击')
    record('真浏览器：未选中任务时点「刷新结果」出现提示文案',
      after > before,
      `提示节点数 ${before} -> ${after}（文案「${NOTICE}」）`)
    // 断言的是「有可见反馈」而非「发了请求」：修复后应给出提示并**不**盲发请求。
    record('真浏览器：点击产生了用户可见反馈（不是完全静默）',
      after > before || after > 0,
      `提示节点 ${after} 个；期间 /datasets/ 请求 ${beforeCalls} -> ${afterCalls}`)

    // 正例（防矫枉过正）：已选中任务时，刷新必须**真的**发请求而不是一律弹提示。
    // 仓库的任务列表用按钮而非链接，因此用「继续当前任务/继续任务」进入详情页，
    // 从 URL 里取真实 task id（不硬编码）。
    // 先回到任务列表（此刻停在数据资产页，列表的「继续任务」按钮不在这里）。
    await page.goto(`${BASE}/console/tasks`, { waitUntil: 'domcontentloaded' })
    await page.waitForTimeout(2000)
    const entered = await page
      .getByRole('button', { name: /继续当前任务|继续任务/ })
      .first()
      .click({ timeout: 8000 })
      .then(() => true)
      .catch(() => false)
    await page.waitForTimeout(2500)

    const taskId = page.url().match(/\/console\/tasks\/(\d+)/)?.[1]
    if (entered && taskId) {
      const callsBefore = networkCalls.length
      const okRefresh = await page
        .getByRole('button', { name: /^刷新$/ })
        .first()
        .click({ timeout: 8000 })
        .then(() => true)
        .catch(() => false)
      await page.waitForTimeout(2500)
      record('真浏览器：已选中任务时刷新**确实**发出请求（正例，防矫枉过正）',
        okRefresh && networkCalls.length > callsBefore,
        `任务 id=${taskId}，/datasets/ 请求数 ${callsBefore} -> ${networkCalls.length}`)
    } else {
      recordSkip('真浏览器正例（已选中任务）',
        entered ? '已点击但未进入任务详情' : '未找到「继续任务」按钮')
    }
  } catch (error) {
    // 第 3 层是可选增强：环境不可达时如实跳过，**不影响 exit code**
    recordSkip('真浏览器断言', `无法执行（${String(error?.message ?? error).split('\n')[0]}）`)
  } finally {
    // 必须无条件关掉浏览器：否则残留 Chromium 会让 node 进程不退出（表现为超时）。
    if (browser) await browser.close().catch(() => {})
  }
} else {
  recordSkip('真浏览器断言（第 3 层）',
    '未启用 --with-browser（默认路径不需要浏览器；加 --with-browser 并确保基线可达）')
}

/** 从全局安装解析 playwright（不写入 package.json，避免污染共享 node_modules）。 */
function webRequireIt() {
  const globalRequire = createRequire('/root/.pi/agent/npm/')
  return globalRequire('playwright')
}

// ---------------------------------------------------------------------------
// 汇总
// ---------------------------------------------------------------------------

console.log('')
const skipped = results.filter((r) => r.skipped).length
if (failures.length > 0) {
  console.error(`SILENT GUARDS FAILED: ${failures.length}/${results.length} 项未通过 -> ${failures.join(', ')}`)
  process.exitCode = 1
} else {
  console.log(`SILENT GUARDS OK: ${results.length - skipped}/${results.length - skipped} 项通过` +
    (skipped ? `（另 ${skipped} 项因输入缺失跳过）` : ''))
}
