/**
 * L14 前端清洗页渲染冒烟测试（自包含，一条命令）。
 *
 * 运行：
 *   node test/l14_ui_smoke.mjs
 *
 * 为什么不是浏览器 E2E：本机没有 chromium / playwright / jsdom。
 * 这里用 esbuild 把 CleaningView 及其全部子组件真实打包（React 一并打进同一个
 * 实例，避免 hooks context 跨副本失效），再用 React 的 renderToStaticMarkup
 * 真实执行组件函数体。useEffect 在服务端渲染时不执行，因此不会发网络请求，
 * 但 JSX 分支、派生计算、文案都真实跑过。验证内容：
 *   1. CleaningView 与全部子组件能被真实打包并渲染，不抛异常；
 *   2. 空状态（datasets = []）与有数据状态都产出预期中文文案；
 *   3. 页面确实渲染出流程步骤条 + 关键词库 / 规则 / 发起清洗 / 报告四个区块。
 *
 * 数据来源：datasets 通过真实 HTTP 调用本 lane 构建的 API（默认
 * http://127.0.0.1:18093）取得，不是硬编码假数据。API 不可达则直接失败退出。
 */

import { mkdtempSync, rmSync, writeFileSync } from 'node:fs'
import { createRequire } from 'node:module'
import { tmpdir } from 'node:os'
import path from 'node:path'
import { fileURLToPath } from 'node:url'

const REPO_ROOT = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..')
const WEB_ROOT = path.join(REPO_ROOT, 'apps', 'web-user')
const NODE_PATHS = [path.join(WEB_ROOT, 'node_modules'), path.join(REPO_ROOT, 'node_modules')]

const webRequire = createRequire(path.join(WEB_ROOT, 'package.json'))
const esbuild = webRequire('esbuild')

const API_BASE = process.env.L14_API_BASE ?? 'http://127.0.0.1:18093/api/v1'

const failures = []
const results = []

function record(name, ok, detail) {
  results.push({ name, ok, detail })
  console.log(`[${ok ? 'PASS' : 'FAIL'}] ${name}: ${detail}`)
  if (!ok) {
    failures.push(name)
  }
}

/** 生成一个把 MemoryRouter + CleaningView 绑在一起的临时入口，保证 React 单实例。 */
function buildRenderer() {
  const workDir = mkdtempSync(path.join(tmpdir(), 'l14-smoke-'))
  const entry = path.join(workDir, 'entry.tsx')
  writeFileSync(
    entry,
    [
      "import { createElement } from 'react'",
      "import { renderToStaticMarkup } from 'react-dom/server'",
      "import { MemoryRouter } from 'react-router-dom'",
      `import { CleaningView } from ${JSON.stringify(path.join(WEB_ROOT, 'src/views/CleaningView.tsx'))}`,
      'export function render(props) {',
      '  return renderToStaticMarkup(createElement(MemoryRouter, null, createElement(CleaningView, props)))',
      '}',
      '',
    ].join('\n'),
  )

  const outfile = path.join(workDir, 'renderer.cjs')
  esbuild.buildSync({
    entryPoints: [entry],
    bundle: true,
    format: 'cjs',
    platform: 'node',
    jsx: 'automatic',
    outfile,
    absWorkingDir: WEB_ROOT,
    nodePaths: NODE_PATHS,
    // 默认 mainFields 是 ['main','module']，会让 jsonc-parser 的 UMD 产物被选中，
    // 它的 AMD 分支在 esbuild 的 CJS 包装里会 require 相对路径 ./impl/format 而解析失败。
    // 优先 ESM 入口可以绕开这个 UMD 互操作坑。
    mainFields: ['module', 'main'],
    loader: { '.css': 'empty' },
    logLevel: 'warning',
  })
  return { workDir, render: createRequire(import.meta.url)(outfile).render }
}

async function login() {
  const res = await fetch(`${API_BASE}/auth/login`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ email: 'admin@company.com', password: 'admin123456' }),
  })
  if (!res.ok) {
    throw new Error(`登录失败 ${res.status}: ${(await res.text()).slice(0, 200)}`)
  }
  const raw = res.headers.getSetCookie?.()[0] ?? res.headers.get('set-cookie') ?? ''
  return raw.split(';')[0]
}

const { workDir, render } = buildRenderer()
try {
  const cookie = await login()
  const datasetsRes = await fetch(`${API_BASE}/datasets`, { headers: { Cookie: cookie } })
  if (!datasetsRes.ok) {
    throw new Error(`读取任务列表失败 ${datasetsRes.status}`)
  }
  const datasets = await datasetsRes.json()
  record('读取真实任务列表', Array.isArray(datasets) && datasets.length > 0, `${datasets.length} 个任务`)

  // --- 空状态 ---
  const emptyHtml = render({ datasets: [] })
  record('空状态渲染成功', emptyHtml.length > 0, `${emptyHtml.length} 字节`)
  record(
    '空状态文案面向普通用户（无接口名/SQL/表名）',
    emptyHtml.includes('你还没有任何任务') && !/(SQL|SELECT|cleaning_runs|POST \/)/i.test(emptyHtml),
    emptyHtml.includes('你还没有任何任务') ? '含引导文案且无技术术语' : '缺少引导文案',
  )

  // --- 有数据状态（真实任务列表） ---
  const html = render({ datasets })
  record('有数据状态渲染成功', html.length > 0, `${html.length} 字节`)
  record('流程步骤条渲染', html.includes('这一步在任务流程里的位置') && html.includes('数据清洗'), '含流程位置说明')
  record('关键词库区块渲染', html.includes('关键词库') && html.includes('批量导入'), '含关键词库与批量导入')
  record('清洗规则区块渲染', html.includes('清洗规则') && html.includes('最少命中数'), '含规则说明')
  record('发起清洗区块渲染', html.includes('发起一次清洗') && html.includes('拦截阶段'), '含发起清洗入口')
  record(
    '三个拦截阶段独立可勾选',
    html.includes('问题') && html.includes('思维链') && html.includes('答案') && html.includes('本次启用哪些规则'),
    '问题/思维链/答案三个阶段均渲染',
  )
  record('清洗记录区块渲染', html.includes('清洗记录'), '含运行列表')
  record('引导到数据资产导出', html.includes('去数据资产导出') && html.includes('清洗之后去哪里'), '含下一步引导')

  console.log('')
  if (failures.length > 0) {
    console.error(`SMOKE FAILED: ${failures.length}/${results.length} 项未通过 -> ${failures.join(', ')}`)
    process.exitCode = 1
  } else {
    console.log(`SMOKE OK: ${results.length}/${results.length} 项通过`)
  }
} finally {
  rmSync(workDir, { recursive: true, force: true })
}
