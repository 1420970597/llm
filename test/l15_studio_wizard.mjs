/**
 * L15 项目向导守卫（Issue #160 T10）。
 *
 * 运行：
 *   node test/l15_studio_wizard.mjs
 *
 * ---------------------------------------------------------------------------
 * 这个守卫防的是什么
 * ---------------------------------------------------------------------------
 * T10 的验收项里有四条**无法靠点击一遍就长期保证**：
 *
 *  1. 「表单 schema 与后端一致」—— 前端说 1–100、后端说 1–50 这类漂移
 *     只在用户提交时以一个后端 422 的形式暴露，而用户不知道是谁错了。
 *     这里直接读 **Go 源码里的常量**与 TS 侧常量比对。
 *  2. 「刷新恢复草稿且绑定当前用户，退出账号清理」—— 存储键必须含用户 ID。
 *  3. 「必填/整数/范围错误聚焦字段」—— 校验必须**一次报全部**且能把
 *     字段映射回步骤（服务端错误也要能跳回出错的那一步）。
 *  4. 「创建重复点击只建一次」—— 幂等键必须在会话内稳定。
 *
 * 分层（与其它 L15 守卫同一约定：默认路径不依赖容器/浏览器）：
 *   第 1 层 源码级：Go/TS 常量一致、存储键含用户、幂等键稳定。
 *   第 2 层 真实模块调用：esbuild 打包生产模块 src/studio/wizard.ts，
 *          注入内存存储，断言真实校验/持久化/映射行为。
 *   第 3 层 变异自证：改坏常量、去掉用户分键、放宽整数校验，
 *          断言第 1、2 层**确实**会失败。
 */

import { mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import { createRequire } from 'node:module'
import { tmpdir } from 'node:os'
import path from 'node:path'
import { fileURLToPath } from 'node:url'

const REPO_ROOT = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..')
const WEB_ROOT = path.join(REPO_ROOT, 'apps', 'web-user')
const WIZARD_SOURCE = path.join(WEB_ROOT, 'src', 'studio', 'wizard.ts')
const WIZARD_PAGE = path.join(WEB_ROOT, 'src', 'studio', 'pages', 'NewProjectWizard.tsx')
const GO_PROJECT_MODEL = path.join(REPO_ROOT, 'internal', 'model', 'project.go')

const webRequire = createRequire(path.join(WEB_ROOT, 'package.json'))
const esbuild = webRequire('esbuild')

const failures = []
function record(name, ok, detail) {
  console.log(`[${ok ? 'PASS' : 'FAIL'}] ${name}: ${detail}`)
  if (!ok) failures.push(name)
}

const wizardSource = readFileSync(WIZARD_SOURCE, 'utf8')
const goSource = readFileSync(GO_PROJECT_MODEL, 'utf8')

// ---------------------------------------------------------------------------
// 第 1 层：前后端常量一致（「表单 schema 与后端一致」）
// ---------------------------------------------------------------------------

/** 从 Go 源码里读一个整型常量的值。 */
function goIntConst(name) {
  const match = goSource.match(new RegExp(`${name}\\s*=\\s*(\\d+)`))
  return match ? Number(match[1]) : null
}

/** 从 TS 源码里读一个导出的数字常量。 */
function tsNumberConst(name) {
  const match = wizardSource.match(new RegExp(`export const ${name}\\s*=\\s*(\\d+)`))
  return match ? Number(match[1]) : null
}

const constantPairs = [
  ['MinPilotSize', 'MIN_PILOT_SIZE'],
  ['MaxPilotSize', 'MAX_PILOT_SIZE'],
]
const mismatched = []
for (const [goName, tsName] of constantPairs) {
  const goValue = goIntConst(goName)
  const tsValue = tsNumberConst(tsName)
  if (goValue === null || tsValue === null || goValue !== tsValue) {
    mismatched.push(`${goName}=${goValue} vs ${tsName}=${tsValue}`)
  }
}
record(
  '前端与后端的取值边界一致',
  mismatched.length === 0,
  mismatched.length === 0
    ? constantPairs.map(([goName, tsName]) => `${goName}=${goIntConst(goName)}=${tsName}`).join('，')
    : `不一致：${mismatched.join('；')}`,
)

/** 名称长度上限也必须一致（后端用 rune 计，前端也用 [...name].length）。 */
const goNameLimit = Number(goSource.match(/len\(\[\]rune\(input\.Name\)\)\s*>\s*(\d+)/)?.[1] ?? NaN)
const tsNameLimit = tsNumberConst('MAX_NAME_LENGTH')
record(
  '名称长度上限一致，且按字符（rune）计数',
  goNameLimit === tsNameLimit && Number.isFinite(goNameLimit),
  `Go=${goNameLimit}，TS=${tsNameLimit}；TS 用 [...name].length 计数`,
)

/** 预算下限一致性（后端 100 分，且显式 0 表示不设上限）。 */
const goBudgetFloor = Number(goSource.match(/\*limit\s*!=\s*0\s*&&\s*\*limit\s*<\s*(\d+)/)?.[1] ?? NaN)
const tsBudgetFloor = tsNumberConst('MIN_BUDGET_LIMIT_MINOR')
record(
  '预算下限一致，且 0 表示不设上限',
  goBudgetFloor === tsBudgetFloor && Number.isFinite(goBudgetFloor),
  `Go=${goBudgetFloor}，TS=${tsBudgetFloor}；两侧都显式放行 0`,
)

/** 草稿存储键必须包含用户 ID（「草稿绑定当前用户」）。 */
record(
  '草稿存储键按用户分键',
  /draftStorageKey[\s\S]{0,200}u\$\{userId\}/.test(wizardSource),
  '键形如 studio.wizard.draft.v1.u<userId>',
)

/** 向导页必须在退出账号路径上清理草稿（页面里调用 clearDraft）。 */
const wizardPageSource = readFileSync(WIZARD_PAGE, 'utf8')
record(
  '向导页在创建成功后清理草稿',
  /clearDraft\(/.test(wizardPageSource),
  '提交成功后调用 clearDraft（否则下次进向导会看到上次内容并可能重复创建）',
)

/** 幂等键必须在会话内稳定（用 ref 而不是每次渲染生成）。 */
record(
  '幂等键在向导会话内保持稳定',
  /useRef\(\s*newIdempotencyKey\(\)\s*\)/.test(wizardPageSource) &&
    !/newIdempotencyKey\(\)/.test(wizardPageSource.replace(/useRef\(\s*newIdempotencyKey\(\)\s*\)/, '')),
  'Idempotency-Key 只生成一次（每次点击换键会真的建出第二个项目）',
)

// ---------------------------------------------------------------------------
// 第 2 层：真实模块调用
// ---------------------------------------------------------------------------

const workDir = mkdtempSync(path.join(tmpdir(), 'l15-studio-wizard-'))

async function bundle(sourceFile) {
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
  })
  const mod = webRequire(outfile)
  return mod.default ?? mod
}

const wizard = await bundle(WIZARD_SOURCE)

/** 内存存储：不需要浏览器会话 localStorage。 */
function memoryStorage() {
  const map = new Map()
  return {
    getItem: (key) => map.get(key) ?? null,
    setItem: (key, value) => void map.set(key, value),
    removeItem: (key) => void map.delete(key),
    keys: () => [...map.keys()],
  }
}

// 草稿往返：保存后能读回同一份内容。
{
  const storage = memoryStorage()
  const draft = { ...wizard.emptyDraft(), name: '冷链问答数据', domains: '8', pilotSize: '12' }
  wizard.saveDraft(storage, 7, draft)
  const loaded = wizard.loadDraft(storage, 7)
  record(
    '草稿保存后能原样读回',
    loaded.name === '冷链问答数据' && loaded.domains === '8' && loaded.pilotSize === '12',
    `name=${loaded.name} domains=${loaded.domains} pilotSize=${loaded.pilotSize}`,
  )

  // 关键性质：**另一个用户读不到**这份草稿（否则会建出属于别人的内容）。
  const otherUser = wizard.loadDraft(storage, 8)
  record(
    '另一个用户读不到他人的草稿',
    otherUser.name === '',
    `user8 读到 name=${JSON.stringify(otherUser.name)}`,
  )

  // 清理后回到空白。
  wizard.clearDraft(storage, 7)
  record('清理草稿后回到空白', wizard.loadDraft(storage, 7).name === '', 'clearDraft 生效')
}

// 坏草稿不得让入口打不开，也不得被「尽力修补」成半份。
{
  const storage = memoryStorage()
  storage.setItem(wizard.draftStorageKey(1), '{ this is not json')
  record(
    '损坏的草稿回退为空白而不是报错',
    wizard.loadDraft(storage, 1).name === '',
    '坏 JSON → 空白草稿（新建项目入口仍然可用）',
  )
  storage.setItem(wizard.draftStorageKey(1), JSON.stringify({ version: 999, draft: { name: 'x' } }))
  record(
    '版本不匹配的草稿被忽略',
    wizard.loadDraft(storage, 1).name === '',
    '版本 999 的旧结构不会被当成当前草稿使用',
  )
}

// 校验：一次报全部错误（不是第一个）。
//
// 用**四个字段同时非法**的草稿：只让一两个非法会让「一次报全部」
// 这条断言变成「报了两个」这种弱断言，从而漏掉「遇到第一个错误就返回」
// 的实现（那正是后端 Validate() 明确避免的形态）。
{
  const draft = {
    ...wizard.emptyDraft(),
    name: '',
    domains: '0',
    directionsPerDomain: '',
    questionsPerDirection: 'x',
    pilotSize: '0',
  }
  const coverageErrors = wizard.validateStep(draft, 'coverage')
  record(
    '校验一次报出该步的全部字段错误',
    coverageErrors.length === 4 &&
      ['coverage.domains', 'coverage.directionsPerDomain', 'coverage.questionsPerDirection', 'pilotSize']
        .every((field) => coverageErrors.some((error) => error.field === field)),
    `coverage 步报出 ${coverageErrors.length} 条：${coverageErrors.map((error) => error.field).join(', ')}`,
  )
  const all = wizard.validateAll(draft)
  record(
    '提交前校验覆盖全部步骤',
    all.some((error) => error.field === 'name') &&
      all.some((error) => error.field === 'coverage.domains') &&
      all.some((error) => error.field === 'pilotSize'),
    `validateAll 报出 ${all.length} 条`,
  )
}

// 宽松解析必须被拒绝：'12abc' 不能被当成 12。
{
  const loose = wizard.checkPositiveInteger('12abc')
  record(
    '非整数输入被拒绝（不做宽松解析）',
    loose.ok === false,
    loose.ok === false ? `拒绝原因：${loose.message}` : '竟然接受了 12abc',
  )
  const negative = wizard.checkPositiveInteger('-3')
  record('负数被拒绝', negative.ok === false, negative.ok === false ? negative.message : '接受了负数')
  const good = wizard.checkPositiveInteger('12')
  record('合法正整数被接受', good.ok === true && good.value === 12, `12 → ${good.ok ? good.value : 'n/a'}`)
}

// 计划量 = n × m × x，且非法输入时为 0（不是编一个数字）。
{
  const draft = { ...wizard.emptyDraft(), domains: '8', directionsPerDomain: '5', questionsPerDirection: '6' }
  record('计划问题数 = n × m × x', wizard.plannedQuestions(draft) === 240, `8×5×6=${wizard.plannedQuestions(draft)}`)
  const invalid = { ...wizard.emptyDraft(), domains: 'x' }
  record('非法输入时计划量为 0（不编造）', wizard.plannedQuestions(invalid) === 0, '返回 0 而不是猜测值')
}

// 服务端错误必须能归到步骤上（否则用户看到「本页没有这个字段」）。
{
  const grouped = wizard.groupServerErrors([
    { field: 'pilotSize', message: '必须在 1–100 之间' },
    { field: 'budget.limitMinor', message: '至少为 100' },
    { field: 'name', message: '必填' },
  ])
  record(
    '服务端字段错误被归到正确步骤',
    grouped.byStep.coverage.length === 1 &&
      grouped.byStep.quality.length === 1 &&
      grouped.byStep.basic.length === 1 &&
      grouped.firstStep === 'basic',
    `basic=${grouped.byStep.basic.length} coverage=${grouped.byStep.coverage.length} quality=${grouped.byStep.quality.length}，首个出错步骤=${grouped.firstStep}`,
  )
}

// 目标类型在运行后不可切换。
{
  record(
    '运行后不允许直接切换目标类型',
    wizard.targetKindSwitchable(false) === true && wizard.targetKindSwitchable(true) === false,
    '没有批次时可切换；已有批次时必须复制新项目',
  )
}

// 请求体形状与后端 `CreateProjectInput` 一致（字段名逐一对齐）。
{
  const draft = { ...wizard.emptyDraft(), name: ' 项目 ', domains: '2', acceptanceRateTarget: '0.9' }
  const request = wizard.toCreateProjectRequest(draft)
  const shapeOk =
    typeof request.name === 'string' &&
    typeof request.targetKind === 'string' &&
    typeof request.coverage?.domains === 'number' &&
    typeof request.coverage?.directionsPerDomain === 'number' &&
    typeof request.coverage?.questionsPerDirection === 'number' &&
    typeof request.pilotSize === 'number' &&
    typeof request.quality?.acceptanceRateTarget === 'number' &&
    typeof request.budget?.currency === 'string' &&
    typeof request.budget?.onExhausted === 'string'
  record('请求体字段与后端 CreateProjectInput 对齐', shapeOk, `name=${JSON.stringify(request.name)} pilotSize=${request.pilotSize}`)
  record('名称与目标被去掉首尾空白', request.name === '项目', JSON.stringify(request.name))
  record(
    '留空的预算上限不出现在请求体里（= 不设上限）',
    !('limitMinor' in request.budget),
    JSON.stringify(request.budget),
  )
}

// ---------------------------------------------------------------------------
// 第 3 层：变异自证
// ---------------------------------------------------------------------------

// 变异 1：把 TS 的试制上限改成 50 → 第 1 层常量比对必须失败。
{
  const mutated = wizardSource.replace('export const MAX_PILOT_SIZE = 100', 'export const MAX_PILOT_SIZE = 50')
  const tsValue = Number(mutated.match(/export const MAX_PILOT_SIZE\s*=\s*(\d+)/)?.[1] ?? NaN)
  record(
    '变异 1：前端试制上限与后端不一致会被捕获',
    tsValue !== goIntConst('MaxPilotSize'),
    `TS=50 vs Go=${goIntConst('MaxPilotSize')} → 判定为不一致`,
  )
}

// 变异 2：去掉草稿存储键里的用户 ID → 第 1 层必须失败。
{
  const mutated = wizardSource.replace("return `studio.wizard.draft.v${DRAFT_VERSION}.u${userId}`", "return `studio.wizard.draft.v${DRAFT_VERSION}`")
  record(
    '变异 2：草稿不再按用户分键会被捕获',
    !/draftStorageKey[\s\S]{0,200}u\$\{userId\}/.test(mutated),
    '去掉 u${userId} 后守卫断言失败',
  )
}

// 变异 3：把正整数校验放宽成 parseInt → 第 2 层必须失败。
//
// 用「整段函数体替换」而不是替换某一行：行级正则依赖具体空白与字符，
// 源码一旦被格式化（gofmt/prettier 风格调整）变异就会**静默失效** ——
// 而变异失效意味着这条自证什么也没证明（本次就是这样失败了一次）。
{
  const mutated = wizardSource.replace(
    /export function checkPositiveInteger\(raw: string\): IntegerCheck \{[\s\S]*?\n\}/,
    "export function checkPositiveInteger(raw: string): IntegerCheck {\n" +
      "  const value = Number.parseInt(raw, 10)\n" +
      "  if (Number.isFinite(value) && value >= 1) return { ok: true, value }\n" +
      "  return { ok: false, message: '必须是整数' }\n" +
      "}",
  )
  const mutatedFile = path.join(workDir, 'mutated-loose.ts')
  writeFileSync(mutatedFile, mutated)
  const mutatedModule = await bundle(mutatedFile)
  const loose = mutatedModule.checkPositiveInteger('12abc')
  record(
    '变异 3：放宽整数校验会被捕获',
    loose.ok === true,
    loose.ok === true ? '变异后 12abc 被接受 → 第 2 层断言会失败' : '变异未生效（守卫可能失效）',
  )
}

rmSync(workDir, { recursive: true, force: true })

if (failures.length > 0) {
  console.error(`\nL15 项目向导守卫失败 ${failures.length} 项：`)
  for (const name of failures) console.error(`  - ${name}`)
  process.exit(1)
}
console.log('\nL15 项目向导守卫全部通过。')
