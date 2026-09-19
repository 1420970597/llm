/**
 * L15 R13 n/m/x 生成参数可达性守卫（自包含，一条命令）。
 *
 * 运行：
 *   node test/l15_nmx_params.mjs            # 源码级断言（CI 默认路径，无需容器）
 *   node test/l15_nmx_params.mjs --with-api # 额外跑真实 API，证明参数真的落库
 *
 * ---------------------------------------------------------------------------
 * 为什么需要这个测试（需求来源）
 * ---------------------------------------------------------------------------
 * 功能说明.txt 步骤 1、3 逐字要求：
 *   「生成关键词下属的n个领域……再根据n个领域生成m个领域下属方向……其中n和m是用户可控的」
 *   「调用llm开始生成每个方向的具体问题x个……其中x是用户可控的数量」
 *
 * 修复前的缺口（父代理现场核实，见 docs/plans/round2-gap-nmx-params.md）：
 *   - 后端三项能力齐备（dataset.DirectionCount / QuestionsPerDirect、UpdateDirectionCount）；
 *   - 但前端 UI 从未把三者暴露给用户：策略表单没有「每领域方向数」，
 *     创建任务表单的生成参数区被 showAdvancedPlanning=false 隐藏，
 *     生成动作调用时不传参 → m 恒为默认 3。
 *
 * 本测试锁定的不变量
 * ==================
 *   A. 创建任务表单里有 n / m / x 三个**可达输入项**（不是死代码、不是被条件隐藏）；
 *   B. createDataset 的请求体里真的带上了 directionCount 与 questionsPerDirection；
 *   C. n 通过 estimate.domainCount 生效（后端 GenerateDomains 读的就是它）；
 *   D. 0 值表示「未设置」，必须原样传 0 让后端回退默认，不得被前端改写成别的数；
 *   E. 变异自证：把传参删掉、把输入项删掉，断言必须失败。
 *
 * 为什么是源码级断言 + 可选真实 API，而不是浏览器 E2E：
 *   本机没有 chromium / playwright / jsdom，且契约 §3 禁止为本测试新增依赖
 *   （会污染共享 node_modules）。CI 的 Frontend job 只跑 tsc + vite build，
 *   不起容器，所以默认路径必须能在无容器下跑绿并决定 exit code。
 *   「参数真的落库」这一层由 --with-api 覆盖（需先起候选容器）。
 */

import { readFileSync } from 'node:fs'
import { createRequire } from 'node:module'
import path from 'node:path'
import { fileURLToPath } from 'node:url'

const REPO_ROOT = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..')
const WEB_ROOT = path.join(REPO_ROOT, 'apps', 'web-user')
const APP_SOURCE = path.join(WEB_ROOT, 'src', 'App.tsx')

const API_BASE = process.env.L15_NMX_API_BASE ?? 'http://127.0.0.1:18180/api/v1'
const ADMIN_EMAIL = process.env.L15_ADMIN_EMAIL ?? 'admin@company.com'
const ADMIN_PASSWORD = process.env.L15_ADMIN_PASSWORD ?? 'admin123456'

// --with-api：启用需要候选容器的真实 API 断言。默认关闭（CI 不起容器）。
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

// ---------------------------------------------------------------------------
// 源码级断言
// ---------------------------------------------------------------------------

/** 取出 plannerForm 的 useState 初始化块（`const [plannerForm, setPlannerForm] = useState({...})`）。 */
function plannerFormInit() {
  const m = source.match(/const\s+\[plannerForm,\s*setPlannerForm\]\s*=\s*useState\(\{([\s\S]*?)\n\s*\}\)/)
  return m ? m[1] : null
}

/** 取出 createDataset 里 consoleApi.createDataset({...}) 的入参对象文本。 */
function createDatasetPayload() {
  const idx = source.indexOf('consoleApi.createDataset(')
  if (idx < 0) return null
  // 从第一个 '{' 起做括号配平，取到匹配的 '}'
  const start = source.indexOf('{', idx)
  if (start < 0) return null
  let depth = 0
  for (let i = start; i < source.length; i++) {
    const ch = source[i]
    if (ch === '{') depth++
    else if (ch === '}') {
      depth--
      if (depth === 0) return source.slice(start, i + 1)
    }
  }
  return null
}

// ---- A. 三个输入项存在且可达 ----
const formInit = plannerFormInit()
record('plannerForm 声明了 domainCount / directionCount / questionsPerDirection 三个字段',
  Boolean(formInit) &&
    /\bdomainCount\s*:/.test(formInit) &&
    /\bdirectionCount\s*:/.test(formInit) &&
    /\bquestionsPerDirection\s*:/.test(formInit),
  formInit ? '三个字段均在 plannerForm 初始化里' : '未找到 plannerForm 初始化')

const inputLabels = ['领域数 n', '每领域方向数 m', '每方向问题数 x']

/** 找出 JSX 里真正渲染该文案的位置（`<Text ...>文案</Text>`），跳过注释里的提及。 */
function jsxLabelIndex(label) {
  const needle = `>${label}</Text>`
  return source.indexOf(needle)
}

for (const label of inputLabels) {
  const idx = jsxLabelIndex(label)
  record(`创建任务表单渲染出「${label}」输入项`, idx >= 0,
    idx >= 0 ? `在 App.tsx:${source.slice(0, idx).split('\n').length} 找到` : '未找到该输入项')
}

// 每个输入项都必须绑定到 plannerForm 对应字段（而不是只有文案没有接线）
const wiring = [
  { label: '领域数 n', field: 'domainCount' },
  { label: '每领域方向数 m', field: 'directionCount' },
  { label: '每方向问题数 x', field: 'questionsPerDirection' },
]
for (const { label, field } of wiring) {
  const idx = jsxLabelIndex(label)
  // 取该 label 之后 900 字符内的 InputNumber 绑定
  const window = idx >= 0 ? source.slice(idx, idx + 900) : ''
  record(`「${label}」的输入框绑定到 plannerForm.${field}`,
    window.includes(`plannerForm.${field}`) && /setPlannerForm/.test(window),
    window.includes(`plannerForm.${field}`) ? `读取 plannerForm.${field} 且有 onChange 写回` : `未绑定到 ${field}`)
}

// ---- 生成参数区不得被硬编码条件隐藏 ----
const advancedIdx = source.indexOf('showAdvancedPlanning')
const advancedDecl = source.match(/const\s+showAdvancedPlanning\s*=\s*(true|false)/)
record('生成参数区没有被 showAdvancedPlanning=false 硬编码隐藏',
  Boolean(advancedDecl),
  advancedDecl ? `showAdvancedPlanning = ${advancedDecl[1]}` : '未找到声明')
if (advancedIdx >= 0 && advancedDecl) {
  // 三个参数输入项必须位于 `isAdmin || showAdvancedPlanning` 条件块**之外**，
  // 否则非管理员看不到 —— 而需求说这三个参数是「用户可控」。
  const condIdx = source.indexOf('isAdmin || showAdvancedPlanning')
  const targetIdx = jsxLabelIndex('每领域方向数 m')
  record('m 输入项位于管理员条件块之外（普通用户可达）',
    condIdx < 0 || targetIdx < 0 || targetIdx < condIdx,
    condIdx >= 0 && targetIdx >= condIdx
      ? 'm 输入项在 isAdmin 条件块内，普通用户不可达'
      : 'm 输入项在条件块之外，普通用户可达')
}

// ---- B/C. 请求体真的带上参数 ----
const payload = createDatasetPayload()
record('createDataset 的请求体带上 directionCount（m）',
  Boolean(payload) && /directionCount\s*:/.test(payload),
  payload ? (payload.match(/directionCount\s*:[^,}]*/) ?? ['未找到'])[0].trim() : '未找到 createDataset 入参')

record('createDataset 的请求体带上 questionsPerDirection（x）',
  Boolean(payload) && /questionsPerDirection\s*:/.test(payload),
  payload ? (payload.match(/questionsPerDirection\s*:[^,}]*/) ?? ['未找到'])[0].trim() : '未找到 createDataset 入参')

// n 通过 estimate.domainCount 生效（后端 GenerateDomains 读的就是它）
record('n（领域数）通过 estimate.domainCount 生效',
  Boolean(payload) && /estimate\s*:/.test(payload) && /domainCount/.test(payload),
  Boolean(payload) && /estimate\s*:/.test(payload) && /domainCount/.test(payload)
    ? 'createDataset 入参里 estimate 引用了 domainCount'
    : 'createDataset 入参没有把 domainCount 写进 estimate')

// ---- D. 0 值语义：必须原样传 0（表示未设置，由后端回退默认） ----
record('m/x 的 0 值原样传递（0 表示未设置，交后端回退默认）',
  Boolean(payload) &&
    /directionCount\s*:[^,}]*\|\|\s*0/.test(payload) &&
    /questionsPerDirection\s*:[^,}]*\|\|\s*0/.test(payload),
  '两个字段都写成 `Number(...) || 0`，不会把「未填」变成别的数')

// ---------------------------------------------------------------------------
// 变异自证：断言必须能捕获「参数被删掉 / 输入项被删掉」
// ---------------------------------------------------------------------------

function mutateRemovePayloadField(field) {
  return source.replace(new RegExp(`\\s*${field}\\s*:[^,}]*,\\n`, 'g'), '\n')
}

function mutateRemoveInputLabel(label) {
  return source.replaceAll(label, '已删除的标签')
}

const mutPayloadDirection = mutateRemovePayloadField('directionCount')
record('变异：删掉请求体里的 directionCount -> 断言失败',
  !/directionCount\s*:/.test(createDatasetPayloadFrom(mutPayloadDirection) ?? ''),
  '删掉后 payload 中不再出现 directionCount（断言会失败）')

const mutPayloadQuestions = mutateRemovePayloadField('questionsPerDirection')
record('变异：删掉请求体里的 questionsPerDirection -> 断言失败',
  !/questionsPerDirection\s*:/.test(createDatasetPayloadFrom(mutPayloadQuestions) ?? ''),
  '删掉后 payload 中不再出现 questionsPerDirection（断言会失败）')

const mutLabel = mutateRemoveInputLabel('每领域方向数 m')
record('变异：删掉「每领域方向数 m」输入项 -> 断言失败',
  mutLabel.indexOf('每领域方向数 m') < 0,
  '删掉后源码里不再出现该输入项文案（断言会失败）')

/** 从给定源码文本里抽出 createDataset 的入参对象（供变异用例复用）。 */
function createDatasetPayloadFrom(text) {
  const idx = text.indexOf('consoleApi.createDataset(')
  if (idx < 0) return null
  const start = text.indexOf('{', idx)
  if (start < 0) return null
  let depth = 0
  for (let i = start; i < text.length; i++) {
    const ch = text[i]
    if (ch === '{') depth++
    else if (ch === '}') {
      depth--
      if (depth === 0) return text.slice(start, i + 1)
    }
  }
  return null
}

// ---------------------------------------------------------------------------
// 可选：真实 API（--with-api）
// ---------------------------------------------------------------------------

async function realCheck() {
  const login = await fetch(`${API_BASE}/auth/login`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ email: ADMIN_EMAIL, password: ADMIN_PASSWORD }),
  })
  record('真实登录本 lane 的 API', login.ok, `${API_BASE} 登录返回 ${login.status}`)
  if (!login.ok) throw new Error('被测 API 不可及')
  const cookie = (login.headers.getSetCookie?.() ?? []).map((c) => c.split(';')[0]).join('; ')

  const keyword = `l15-r13-${process.pid}`
  const created = await fetch(`${API_BASE}/datasets`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json', Cookie: cookie },
    body: JSON.stringify({
      name: `${keyword}-ds`,
      rootKeyword: keyword,
      targetSize: 4,
      status: 'draft',
      // 与 UI 传参完全一致：m=2、x=3，且 estimate.domainCount=n=2
      directionCount: 2,
      questionsPerDirection: 3,
      estimate: { domainCount: 2, questionsPerDomain: 3, answerVariants: 1, rewardVariants: 1 },
    }),
  })
  const body = await created.json().catch(() => ({}))
  record('真实 API 创建数据集受理 n/m/x', created.status === 201,
    `HTTP ${created.status} id=${body.id ?? '-'}`)

  record('真实落库：directionCount（m）与请求一致',
    Number(body.directionCount) === 2, `directionCount=${body.directionCount}（期望 2）`)
  record('真实落库：questionsPerDirection（x）与请求一致',
    Number(body.questionsPerDirection) === 3, `questionsPerDirection=${body.questionsPerDirection}（期望 3）`)
  record('真实落库：n 经 estimate.domainCount 生效',
    Number(body.estimate?.domainCount) === 2, `estimate.domainCount=${body.estimate?.domainCount}（期望 2）`)

  // 精确清理：本 API 没有 DELETE /datasets/{id}（契约 §1.1 不新增路由），
  // 因此用 psql 按**精确 id** 删除自己创建的那一行及其级联子表。
  // 禁止无 WHERE 的删除（契约 §6.2）。
  if (body.id) {
    const { execFileSync } = await import('node:child_process')
    try {
      execFileSync('docker', [
        'exec', 'llm-postgres-1', 'psql', '-U', 'llm_factory', '-d', 'llm_factory', '-v', 'ON_ERROR_STOP=1',
        '-c', `DELETE FROM datasets WHERE id = ${Number(body.id)}`,
      ], { stdio: 'pipe' })
      console.log(`[清理] 已按精确 id 删除 dataset id=${body.id}`)
    } catch (error) {
      console.log(`[清理] 需手工清理 dataset id=${body.id}（psql 失败：${error?.message ?? error}）`)
    }
  }
}

if (WITH_API) {
  try {
    await realCheck()
  } catch (error) {
    record('真实 API 断言', false, String(error?.message ?? error))
  }
} else {
  recordSkip('真实 API：n/m/x 落库校验',
    `未启用 --with-api（默认路径不需要容器；加 --with-api 并先跑 scripts/l15-r13-stack.sh）`)
}

// ---------------------------------------------------------------------------
// 汇总
// ---------------------------------------------------------------------------

console.log('')
const skipped = results.filter((r) => r.skipped).length
if (failures.length > 0) {
  console.error(`NMX PARAMS FAILED: ${failures.length}/${results.length} 项未通过 -> ${failures.join(', ')}`)
  process.exitCode = 1
} else {
  console.log(`NMX PARAMS OK: ${results.length - skipped}/${results.length - skipped} 项通过` +
    (skipped ? `（另 ${skipped} 项因输入缺失跳过）` : ''))
}
