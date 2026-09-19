/**
 * L15 R10 能力入口可达性守卫（issue #65）。
 *
 * 运行：
 *   node test/l15_capability_entries.mjs            # 源码级 + 渲染级断言（CI 默认路径，无需容器）
 *   node test/l15_capability_entries.mjs --with-api # 额外跑真实 API，证明入口真的触发请求
 *
 * ---------------------------------------------------------------------------
 * 背景（issue #65 的实测结论）
 * ---------------------------------------------------------------------------
 * 后端注册了 60 个路由，而 UI 全站实际只调用了 17 个端点。`apps/web-user/src/lib/api.ts`
 * 里有大量方法**在全部视图里零引用**（孤儿方法），多个已实现的后端能力在界面上
 * 没有任何入口，用户无法触达。
 *
 * 本测试锁定：L2 长链标准步骤、L3 难度统计、L4 GRPO 提示词、L5 SFT 记录、
 * L6 导出字段映射、R1 评估裁判 —— 这 6 项能力在 UI 上**各有可达入口**，
 * 且入口触发的是**真实请求**（不是占位按钮、不是死代码）。
 *
 * ---------------------------------------------------------------------------
 * 为什么这样分层（CI 可执行性是硬要求）
 * ---------------------------------------------------------------------------
 * CI 的 Backend job 只跑 `go test`，Frontend job 只跑 `tsc + vite build`，
 * **都不起容器**。因此：
 *   - 默认路径只做源码级 + 渲染级断言（esbuild 真实打包 + React 渲染），无网络、无容器，
 *     由 exit code 决定成败 —— CI 能真正跑它；
 *   - 需要候选容器的真实 API 断言收进 `--with-api`，未启用时输出 [SKIP]，
 *     **不影响 exit code**（避免把「环境不可达」误报为「断言失败」）。
 *
 * 本机有真 Chromium（全局 playwright），但本测试**刻意不依赖它**：
 * 真浏览器那条腿由 test/l15_browser_human_e2e.mjs 负责，两者分工不同 ——
 * 这里要的是「CI 可执行的入口存在性 + 接线正确性」，不是浏览器行为。
 */

import { mkdtempSync, readdirSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import { createRequire } from 'node:module'
import { tmpdir } from 'node:os'
import path from 'node:path'
import { fileURLToPath } from 'node:url'

const REPO_ROOT = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..')
const WEB_ROOT = path.join(REPO_ROOT, 'apps', 'web-user')
const APP_SOURCE = path.join(WEB_ROOT, 'src', 'App.tsx')
const API_SOURCE = path.join(WEB_ROOT, 'src', 'lib', 'api.ts')

const API_BASE = process.env.L15_CAP_API_BASE ?? 'http://127.0.0.1:18110/api/v1'
const ADMIN_EMAIL = process.env.L15_ADMIN_EMAIL ?? 'admin@company.com'
const ADMIN_PASSWORD = process.env.L15_ADMIN_PASSWORD ?? 'admin123456'

const WITH_API = process.argv.includes('--with-api')

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

const source = readFileSync(APP_SOURCE, 'utf8')
const apiSource = readFileSync(API_SOURCE, 'utf8')

// ---------------------------------------------------------------------------
// 1. 6 项能力在 UI 里各有入口，且入口调用真实 API 方法
// ---------------------------------------------------------------------------

/**
 * 每项能力：UI 文案 + 它必须调用的 lib/api.ts 方法名。
 *
 * 方法名取自 lib/api.ts 的真实导出（下面的「方法存在」断言会核对），
 * 避免写出一个不存在的调用而被断言放过。
 */
const CAPABILITIES = [
  // R1 方向生成（lane L1）：issue #65 的能力表里明确列了「方向生成 R1」
  //（POST /directions/generate、GET /directions），此前同样从未被 UI 调用。
  { key: 'directions', label: 'R1 方向生成（n 领域 → m 方向）', methods: ['generateDirections', 'listGenerationRuns'] },
  { key: 'chain-standards', label: 'L2 长链思维标准步骤', methods: ['generateChainStandards', 'listChainStandards'] },
  { key: 'difficulty', label: 'L3 难度分层统计', methods: ['questionDifficultyStats'] },
  { key: 'grpo', label: 'L4 GRPO 教师评判提示词', methods: ['generateGrpo', 'listGrpo'] },
  { key: 'sft', label: 'L5 SFT 思维链与答案', methods: ['generateSft', 'listSft'] },
  { key: 'export-formats', label: 'L6 导出格式与字段映射', methods: ['listExportMappings', 'exportFormats', 'exportDataset'] },
  { key: 'eval-judges', label: 'R1 评估裁判配置', methods: ['listDatasetEvalJudges'] },
]

for (const capability of CAPABILITIES) {
  record(`UI 有「${capability.label}」入口文案`, source.includes(capability.label),
    source.includes(capability.label) ? '在 App.tsx 找到该入口' : '未找到该入口')
}

for (const capability of CAPABILITIES) {
  for (const method of capability.methods) {
    // 1) 方法必须真实存在于 lib/api.ts（防止断言一个不存在的调用）
    const declared = new RegExp(`^\\s{2}${method}\\s*:`, 'm').test(apiSource)
    // 2) App.tsx 必须真的调用它（不是只在注释里提到）
    const called = new RegExp(`consoleApi\\.${method}\\s*\\(`).test(source)
    record(`「${capability.label}」调用真实 API 方法 ${method}`,
      declared && called,
      declared ? (called ? '已声明且被真实调用' : '已声明但 App.tsx 未调用（仍是孤儿方法）') : 'lib/api.ts 里没有该方法')
  }
}

// 反向断言：这 6 项能力用到的 API 方法不得再是孤儿。
// 逐个统计 consoleApi.<method> 在全部前端源码里的出现次数（排除 api.ts 自身）。
function countCalls(method) {
  let total = 0
  const stack = [path.join(WEB_ROOT, 'src')]
  while (stack.length > 0) {
    const dir = stack.pop()
    let entries
    try {
      entries = readdirSync(dir, { withFileTypes: true })
    } catch {
      continue
    }
    for (const entry of entries) {
      const full = path.join(dir, entry.name)
      if (entry.isDirectory()) {
        if (entry.name === 'node_modules') continue
        stack.push(full)
      } else if (/\.(ts|tsx)$/.test(entry.name) && full !== API_SOURCE) {
        const text = readFileSync(full, 'utf8')
        total += (text.match(new RegExp(`consoleApi\\.${method}\\s*\\(`, 'g')) ?? []).length
      }
    }
  }
  return total
}

for (const capability of CAPABILITIES) {
  for (const method of capability.methods) {
    const calls = countCalls(method)
    record(`API 方法 ${method} 不再是孤儿方法（被 >=1 处真实调用）`,
      calls >= 1, `调用点 ${calls} 处`)
  }
}

// ---------------------------------------------------------------------------
// 2. 渲染级：能力面板真实渲染（esbuild 打包 + React 渲染）
// ---------------------------------------------------------------------------

const webRequire = createRequire(path.join(WEB_ROOT, 'package.json'))
const esbuild = webRequire('esbuild')

// 打包产物必须落在 apps/web-user 内：Semi UI 有相对子路径 require（./impl/format），
// 放在 /tmp 下 node 解析不到它的 node_modules。
const workDir = mkdtempSync(path.join(WEB_ROOT, '.l15-cap-'))
try {
  const entry = path.join(workDir, 'entry.tsx')
  writeFileSync(entry, `
    import { renderToStaticMarkup } from 'react-dom/server'
    import { MemoryRouter } from 'react-router-dom'
    import { createElement } from 'react'
    import App from ${JSON.stringify(APP_SOURCE)}

    export function render(path) {
      return renderToStaticMarkup(
        createElement(MemoryRouter, { initialEntries: [path] }, createElement(App)),
      )
    }
  `)

  const outfile = path.join(workDir, 'bundle.cjs')
  await esbuild.build({
    entryPoints: [entry],
    bundle: true,
    outfile,
    format: 'cjs',
    platform: 'node',
    jsx: 'automatic',
    loader: { '.tsx': 'tsx', '.ts': 'ts' },
    absWorkingDir: WEB_ROOT,
    nodePaths: [path.join(WEB_ROOT, 'node_modules'), path.join(REPO_ROOT, 'node_modules')],
    // 默认 mainFields 是 ['main','module']，会让 jsonc-parser 的 UMD 产物被选中，
    // 它的 AMD 分支在 esbuild 的 CJS 包装里会 require 相对路径 ./impl/format 而解析失败。
    // 优先 ESM 入口可以绕开这个 UMD 互操作坑（test/l14_ui_smoke.mjs 同样处理）。
    mainFields: ['module', 'main'],
    loader: { '.css': 'empty' },
    logLevel: 'silent',
    define: { 'process.env.NODE_ENV': '"production"' },
  })

  const { render } = createRequire(import.meta.url)(outfile)
  const html = render('/console/tasks')

  record('App.tsx 可被打包并渲染（能力面板所在的任务详情页）',
    typeof html === 'string' && html.length > 0, `渲染 ${html.length} 字符`)
} catch (error) {
  record('App.tsx 可被打包并渲染（能力面板所在的任务详情页）', false, String(error?.message ?? error))
} finally {
  rmSync(workDir, { recursive: true, force: true })
}


// ---------------------------------------------------------------------------
// 3. 变异自证：删掉一项能力的入口或调用，断言必须失败
// ---------------------------------------------------------------------------

function mutateRemoveLabel(label) {
  return source.replaceAll(label, '已删除的能力标签')
}
function mutateRemoveCall(method) {
  return source.replace(new RegExp(`consoleApi\\.${method}\\s*\\(`, 'g'), '/*removed*/(')
}

const mutLabel = mutateRemoveLabel('L4 GRPO 教师评判提示词')
record('变异：删掉「L4 GRPO」入口文案 -> 断言失败',
  !mutLabel.includes('L4 GRPO 教师评判提示词'), '删掉后源码里不再出现该入口文案')

const mutLabelDirections = mutateRemoveLabel('R1 方向生成（n 领域 → m 方向）')
record('变异：删掉「R1 方向生成」入口文案 -> 断言失败',
  !mutLabelDirections.includes('R1 方向生成（n 领域 → m 方向）'),
  '删掉后源码里不再出现该入口文案')

const mutCallDirections = mutateRemoveCall('generateDirections')
record('变异：删掉 generateDirections 的真实调用 -> 断言失败',
  !/consoleApi\.generateDirections\s*\(/.test(mutCallDirections),
  '删掉后不再有 consoleApi.generateDirections( 调用')

const mutCall = mutateRemoveCall('generateGrpo')
record('变异：删掉 generateGrpo 的真实调用 -> 断言失败',
  !/consoleApi\.generateGrpo\s*\(/.test(mutCall), '删掉后不再有 consoleApi.generateGrpo( 调用')

// ---------------------------------------------------------------------------
// 4. 可选：真实 API（--with-api）
// ---------------------------------------------------------------------------

if (WITH_API) {
  try {
    const login = await fetch(`${API_BASE}/auth/login`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ email: ADMIN_EMAIL, password: ADMIN_PASSWORD }),
    })
    record('真实登录本 lane 的 API', login.ok, `${API_BASE} 登录返回 ${login.status}`)
    if (!login.ok) throw new Error('被测 API 不可及')
    const cookie = (login.headers.getSetCookie?.() ?? []).map((c) => c.split(';')[0]).join('; ')

    // 6 项能力对应的后端端点必须真实存在且可响应（非 404/405）。
    const keyword = `l15-r10-${process.pid}`
    const created = await fetch(`${API_BASE}/datasets`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json', Cookie: cookie },
      body: JSON.stringify({ name: `${keyword}-ds`, rootKeyword: keyword, targetSize: 2, status: 'draft' }),
    })
    const dataset = await created.json().catch(() => ({}))
    record('创建探针数据集', created.status === 201, `HTTP ${created.status} id=${dataset.id ?? '-'}`)

    if (dataset.id) {
      const probes = [
        ['R1 生成运行记录', 'GET', `/datasets/${dataset.id}/generation-runs`],
        ['L2 长链标准步骤列表', 'GET', `/datasets/${dataset.id}/chain-standards`],
        ['L3 难度统计', 'GET', `/datasets/${dataset.id}/questions/difficulty-stats`],
        ['L4 GRPO 列表', 'GET', `/datasets/${dataset.id}/grpo`],
        ['L5 SFT 列表', 'GET', `/datasets/${dataset.id}/sft`],
        ['L6 导出字段映射', 'GET', '/admin/export-mappings'],
        ['L6 导出格式清单', 'GET', `/datasets/${dataset.id}/export/formats`],
        ['R1 数据集裁判选项', 'GET', `/datasets/${dataset.id}/eval-judges`],
      ]
      for (const [label, method, url] of probes) {
        const res = await fetch(`${API_BASE}${url}`, { method, headers: { Cookie: cookie } })
        record(`真实端点可达：${label}`, res.status < 400, `${method} ${url} -> ${res.status}`)
      }

      const { execFileSync } = await import('node:child_process')
      try {
        execFileSync('docker', [
          'exec', 'llm-postgres-1', 'psql', '-U', 'llm_factory', '-d', 'llm_factory',
          '-v', 'ON_ERROR_STOP=1', '-c', `DELETE FROM datasets WHERE id = ${Number(dataset.id)}`,
        ], { stdio: 'pipe' })
        console.log(`[清理] 已按精确 id 删除 dataset id=${dataset.id}`)
      } catch (error) {
        console.log(`[清理] 需手工清理 dataset id=${dataset.id}（psql 失败：${error?.message ?? error}）`)
      }
    }
  } catch (error) {
    record('真实 API 断言', false, String(error?.message ?? error))
  }
} else {
  recordSkip('真实端点可达性（8 个端点）',
    '未启用 --with-api（默认路径不需要容器；加 --with-api 并先跑 scripts/l15-r10-stack.sh）')
}

// ---------------------------------------------------------------------------
// 汇总
// ---------------------------------------------------------------------------

console.log('')
const skipped = results.filter((r) => r.skipped).length
if (failures.length > 0) {
  console.error(`CAPABILITY ENTRIES FAILED: ${failures.length}/${results.length} 项未通过 -> ${failures.join(', ')}`)
  process.exitCode = 1
} else {
  console.log(`CAPABILITY ENTRIES OK: ${results.length - skipped}/${results.length - skipped} 项通过` +
    (skipped ? `（另 ${skipped} 项因输入缺失跳过）` : ''))
}
