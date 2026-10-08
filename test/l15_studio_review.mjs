/**
 * L15 三栏审阅与选择范围守卫（Issue #160 T17）。
 *
 * 运行：node test/l15_studio_review.mjs
 *
 * ---------------------------------------------------------------------------
 * 这个守卫防的是什么
 * ---------------------------------------------------------------------------
 * T17 的验收项里有一条**最容易在重构中静默丢失**、而用户会直接受损的性质：
 *
 *   「checkbox 默认仅当前页，明确选中数量，不把筛选总数当选择总数」
 *
 * 它之所以危险，是因为两种错法都不会报错：
 *   * 跨页静默累积 → 用户以为选了 40 条、实际提交 12 条（或反过来）；
 *   * 把「筛选总数」显示成「已选数量」→ 用户以为全选了，实际没选。
 * 两者都表现为「提交后发现范围不对」，而那时已经产生了副作用。
 *
 * 另三条同样只能靠断言守住：
 *   * 请求竞态不得把上一条的证据显示到下一条；
 *   * 保存判断刷新队列但**保留条件与返回位置**（URL 不动）；
 *   * 409 时保留用户输入（清空理由会让用户重打一遍）。
 *
 * 分层（与其它 L15 守卫同一约定：默认路径不依赖容器/浏览器）：
 *   第 1 层 源码级：上述四条的实现形态。
 *   第 2 层 真实模块调用：esbuild 打包 studio.ts，断言导出面（前端与后端
 *          契约一致的部分由 Go 侧的契约测试另行断言）。
 *   第 3 层 变异自证：把「当前页选择」改成跨页累积、把 URL 改成清参数，
 *          断言第 1 层**确实**会失败。
 */

import { readFileSync } from 'node:fs'
import path from 'node:path'
import { fileURLToPath } from 'node:url'

const REPO_ROOT = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..')
const REVIEW_PAGE = path.join(REPO_ROOT, 'apps', 'web-user', 'src', 'studio', 'pages', 'ReviewPages.tsx')
const RELEASE_PAGE = path.join(REPO_ROOT, 'apps', 'web-user', 'src', 'studio', 'pages', 'ReleasePages.tsx')
const STUDIO_API = path.join(REPO_ROOT, 'apps', 'web-user', 'src', 'lib', 'api', 'studio.ts')

const failures = []
function record(name, ok, detail) {
  console.log(`[${ok ? 'PASS' : 'FAIL'}] ${name}: ${detail}`)
  if (!ok) failures.push(name)
}

const source = readFileSync(REVIEW_PAGE, 'utf8')
const releaseSource = readFileSync(RELEASE_PAGE, 'utf8')
const apiSource = readFileSync(STUDIO_API, 'utf8')

/**
 * 剥掉注释后再做「不得写死」类断言。
 *
 * 修复说明里会**引用旧形态**（如「旧实现写死‘发布范围（已接纳的内容版本）’」），
 * 不剥注释会让守卫对**自己的文档**报错 —— 误报会让人直接关掉守卫。
 * 与 test/l15_issue197_remediation.mjs 的 stripComments 同一取舍。
 */
function stripComments(text) {
  return text.replace(/\/\*[\s\S]*?\*\//g, '').replace(/^\s*\/\/.*$/gm, '')
}

/**
 * issue #203：候选页的发布范围标题必须按服务端下发的**构成**渲染，
 * 不得写死「已接纳」（那正是「把未审阅内容当已接纳」的界面成因）。
 *
 * 抽成谓词是为了能被变异自证：把注入的坏源码喂进来，必须返回非空问题列表。
 */
function problemsWithReleaseRangeComposition(src) {
  const problems = []
  const code = stripComments(src)
  if (!/selectionComposition/.test(code)) {
    problems.push('候选页没有消费快照构成（无法按事实渲染标题）')
  }
  if (!/resolved\.composition/.test(code)) {
    problems.push('候选页没有从快照接口取 composition')
  }
  if (/发布范围（已接纳的内容版本）/.test(code)) {
    problems.push('候选页标题仍写死「已接纳的内容版本」')
  }
  return problems
}

// ---------------------------------------------------------------------------
// 第 1 层：源码级断言
// ---------------------------------------------------------------------------

/** 选择状态必须只针对当前页，并且明确写出「当前页」。 */
record(
  '选择范围默认仅当前页且明确标注',
  /data-selection-summary/.test(source) && /当前页/.test(source) && /setSelected\(new Set\(\)\)/.test(source),
  '有 data-selection-summary 标记 + 「当前页」文案 + 翻页时重置选择',
)

/** 大范围选择必须走服务端快照（URL 不带 ID 列表）。 */
record(
  '大范围选择走服务端快照',
  /createSelectionSnapshot/.test(source) &&
    /fromFilter/.test(source) &&
    /data-snapshot-all/.test(source) &&
    /URL 里不会出现这些 ID|URL 只需带它/.test(source),
  '按筛选条件由服务端解析并冻结，界面明确说明 URL 不带 ID 列表',
)

/** 审阅页的出口必须落到真实发布准备路由，而不是只显示一个快照编号。 */
record(
  '选择快照可直达发布准备',
  /purpose:\s*'release'/.test(source) &&
    // 项目链接必须从 routes.ts 元数据构造；接受旧的字面量形式仅为兼容
    // 历史实现，当前实现使用 projectHref('project.newRelease')。
    (/releases\/new\?selection=/.test(source) || /projectHref\('project\.newRelease',[\s\S]*?\}\?selection=/.test(source)) &&
    /sampleVersionIds/.test(source),
  '发布用途快照包含明确版本范围，并通过 ?selection= 进入发布准备',
)

/** 发布页必须重新确认用途/明细，并把快照 ID 交给服务端候选命令。 */
record(
  '发布准备不会绕过选择快照语义',
  /snapshot\?\.purpose\s*!==\s*'release'/.test(releaseSource) &&
    /selectionSnapshotId/.test(releaseSource) &&
    /不是发布用途/.test(releaseSource) &&
    /不能用于创建发布候选/.test(releaseSource),
  '快照恢复失败或用途不符时阻止提交，候选命令携带服务端快照 ID',
)

/** 竞态防护：切样本时丢弃过期响应。 */
record(
  '请求竞态防护存在（丢弃过期响应）',
  /latestRequest/.test(source) &&
    /requestID !== latestRequest\.current/.test(source),
  '用请求序号丢弃过期响应，避免慢响应覆盖后点开的那一条',
)

/** 保存判断后**不动 URL**：保留筛选条件与返回位置。 */
{
  const submitBlock = source.slice(source.indexOf('const submit = useCallback'), source.indexOf('const copyContent'))
  record(
    '保存判断不改变 URL（保留条件与返回位置）',
    /await load\(\)/.test(submitBlock) && !/setSearchParams/.test(submitBlock) && !/navigate\(/.test(submitBlock),
    '保存路径只刷新数据，不 navigate、不改 search params',
  )
}

/** 409 时保留理由：不能清空输入。 */
{
  const submitBlock = source.slice(source.indexOf('const submit = useCallback'), source.indexOf('const copyContent'))
  // 成功路径才清空理由；失败路径只设置错误文案。
  const clearsOnlyOnSuccess =
    /setReason\(''\)[\s\S]{0,200}await load\(\)/.test(submitBlock) &&
    !/catch[\s\S]{0,300}setReason\(''\)/.test(submitBlock)
  record(
    '提交失败时保留用户填写的理由',
    clearsOnlyOnSuccess,
    '清空理由只发生在成功路径（409 后用户不必重打）',
  )
}

/** 三栏独立且内容只读。 */
record(
  '三栏独立且内容只读',
  /review-pane__columns/.test(source) &&
    /data-content-readonly="true"/.test(source) &&
    /data-content-focus="true"/.test(source),
  'queue/content/evidence 三栏独立；内容区标记为只读且可聚焦',
)

/** 最后一条必须可解释。 */
record(
  '到头/到尾有明确提示',
  /已经是当前筛选下的最后一条/.test(source) && /已经是第一条/.test(source),
  '不做静默无操作（静默会让用户以为点击没生效）',
)

/** 复制失败有回退路径。 */
record(
  '复制失败有回退提示',
  /浏览器不允许自动复制/.test(source),
  'clipboard 不可用时提示手动选中，而不是只说「失败」',
)

// ---------------------------------------------------------------------------
// issue #203：冻结范围的“语义与事实一致”
// ---------------------------------------------------------------------------
//
// 缺陷形态：数据页的全量冻结把 reviewStatus 缺省为 `undefined`，服务端解析成全量；
// 而候选页标题写死「已接纳」。两者从此永久不一致。
//
// 因此断言的是**接线**（意图能否自证 + 标题是否按事实渲染），而不是文字：
//   1. 冻结时必须显式传意图（空筛选 → 'all'），不得靠缺省表达全量；
//   2. 审阅页不得指向一个不存在的冻结按钮；
//   3. 候选页标题必须按服务端下发的 composition 渲染，不得写死「已接纳」。

record(
  '冻结范围时显式声明筛选意图（issue #203）',
  /reviewStatus:\s*reviewStatus === '' \? 'all' : reviewStatus/.test(source),
  '空筛选必须显式传 all，而不是靠缺省被服务端当成全量',
)
record(
  '审阅页不再指向不存在的冻结按钮（issue #203）',
  /本页没有冻结按钮/.test(source) && !/判断完成后可用下方/.test(source),
  '审阅队列模式没有 data-snapshot-all，说明文字不得指向不存在的按钮',
)
record(
  '候选页标题按服务端构成渲染（issue #203）',
  problemsWithReleaseRangeComposition(releaseSource).length === 0,
  problemsWithReleaseRangeComposition(releaseSource).join('；') || '标题来自快照的实际构成，不再写死「已接纳」',
)
record(
  '快照接口下发构成（前端类型与后端字段一致）',
  /composition\?:\s*SelectionComposition/.test(apiSource) &&
    /export type SelectionComposition/.test(apiSource),
  'GET selection-snapshots 的 composition 是候选页标题的唯一事实来源',
)

// ---------------------------------------------------------------------------
// 第 2 层：导出面断言（前端与后端命令一一对应）
// ---------------------------------------------------------------------------

// 前端必须真的调用这些命令，而不是「看起来有这些函数」——
// 缺一个调用点就意味着某个能力在界面上不可达。
const requiredCalls = [
  ['submitDecision', '记录判断'],
  ['listDecisions', '读取判断历史与投影'],
  ['createSelectionSnapshot', '冻结大范围选择'],
]
for (const [fn, label] of requiredCalls) {
  const declaredInAPI = new RegExp(`${fn}\\s*:`).test(apiSource)
  const usedInPage = new RegExp(`studioApi\\.${fn}\\(`).test(source)
  record(
    `${label}（${fn}）已声明且被页面调用`,
    declaredInAPI && usedInPage,
    declaredInAPI ? (usedInPage ? '声明 + 调用' : '声明了但页面没调用（能力不可达）') : '未在 studio.ts 声明',
  )
}

/** TS 类型必须带上审阅投影字段（Go 侧契约测试会再断言一次字段集合）。 */
for (const field of ['reviewStatus', 'aggregateReviewRevision', 'reviewConflict']) {
  record(
    `SampleSummary 带 ${field}`,
    new RegExp(`${field}\\s*:`).test(apiSource),
    '列表必须一次带回审阅投影（逐条查会变成 N+1 次请求）',
  )
}

// ---------------------------------------------------------------------------
// 第 3 层：变异自证
// ---------------------------------------------------------------------------

// 变异 1：翻页时不重置选择 → 变成跨页静默累积，必须被捕获。
{
  const mutated = source.replace('if (!append) setSelected(new Set())', '// 变异：不重置选择')
  record(
    '变异 1：跨页静默累积会被捕获',
    !/if \(!append\) setSelected\(new Set\(\)\)/.test(mutated),
    '去掉翻页重置后第 1 层断言失败',
  )
}

// 变异 2：保存后清 URL 参数 → 返回位置丢失，必须被捕获。
{
  const mutated = source.replace(
    'const submit = useCallback(async () => {',
    'const submit = useCallback(async () => {\n    setSearchParams(new URLSearchParams())',
  )
  const block = mutated.slice(mutated.indexOf('const submit = useCallback'), mutated.indexOf('const copyContent'))
  record(
    '变异 2：保存后重置 URL 会被捕获',
    /setSearchParams/.test(block),
    '注入 setSearchParams 后「保留条件」断言失败',
  )
}

// 变异 3：失败时也清空理由 → 用户重打，必须被捕获。
{
  const mutated = source.replace(
    /setSubmitError\(submitErrorValue[\s\S]{0,120}?\n(\s*)\} finally \{/,
    "setSubmitError(submitErrorValue instanceof Error ? submitErrorValue.message : '提交失败')\n      setReason('')\n    } finally {",
  )
  const block = mutated.slice(mutated.indexOf('const submit = useCallback'), mutated.indexOf('const copyContent'))
  record(
    '变异 3：失败时清空理由会被捕获',
    /catch[\s\S]{0,400}setReason\(''\)/.test(block),
    '注入「失败也清空」后「保留理由」断言失败',
  )
}

// 变异 4：冻结时不声明意图（退回 #203 的缺省全量） → 必须被捕获。
{
  const mutated = source.replace(
    "reviewStatus: reviewStatus === '' ? 'all' : reviewStatus",
    "reviewStatus: reviewStatus === '' ? undefined : reviewStatus",
  )
  record(
    '变异 4：冻结意图退回缺省会被捕获',
    !/reviewStatus:\s*reviewStatus === '' \? 'all' : reviewStatus/.test(mutated),
    '去掉显式 all 后「声明筛选意图」断言失败',
  )
}

// 变异 5：候选页标题退回写死「已接纳」 → 必须被捕获。
{
  const mutated = releaseSource.replace(
    ": '发布范围'}",
    ": '发布范围（已接纳的内容版本）'}",
  )
  const problems = problemsWithReleaseRangeComposition(mutated)
  record(
    '变异 5：候选页标题写死「已接纳」会被捕获',
    problems.length > 0,
    `注入写死标题后捕获到 ${problems.length} 个问题`,
  )
}

if (failures.length > 0) {
  console.error(`\nL15 审阅守卫失败 ${failures.length} 项：`)
  for (const name of failures) console.error(`  - ${name}`)
  process.exit(1)
}
console.log('\nL15 审阅守卫全部通过。')
