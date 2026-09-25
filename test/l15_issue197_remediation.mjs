/**
 * Issue #190–#195 / #197 回归守卫（源码级，CI 可执行，无需容器）。
 *
 * 运行：
 *   node test/l15_issue197_remediation.mjs      # 源码级断言 + 变异自证（CI 默认路径）
 *
 * ---------------------------------------------------------------------------
 * 为什么需要这些断言（而不是「改完就算」）
 * ---------------------------------------------------------------------------
 * 本轮修的很多缺陷是**同一个形态反复出现**，只修当前那一处必然复发：
 *
 *   #190 计划量与可产出量从不比较          → 必须断言「容量校验存在且被调用」
 *   #191 展示层回退为原始内部 code          → 必须断言「所有 label 函数不回传原串」
 *   #192 Markdown 星号（上一轮守卫写成手工清单）→ 已由 l15_markdown_ui.mjs 改成全树扫描
 *   #194 两个入口渲染同一页面               → 必须断言「两页的默认筛选/文案/CTA 不同」
 *   #197-15 邮件邀请（无出站邮件时永远不可用）→ 必须断言「直接建号路径存在且带强度校验」
 *   #197-16 硬编码「尚未迁移」              → 必须断言「结论来自服务端读模型」
 *
 * 每条断言都通过**变异自证**：把断言抽成谓词函数（`problemsWithX(src)`），
 * 再把刻意改坏的源码喂进去，断言返回的问题列表**非空**。这样「断言真的会响」
 * 是被证明的，而不是被声称的（与 `l15_app_ux.mjs` 同一约定）。
 */

import { readFileSync } from 'node:fs'
import path from 'node:path'
import { fileURLToPath } from 'node:url'

const REPO_ROOT = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..')
const read = (relative) => readFileSync(path.join(REPO_ROOT, relative), 'utf8')

const MODEL_DOCS = read('internal/model/studio_docs.go')
const BATCH_STORE = read('internal/store/batch_store.go')
const BATCH_RUNNER = read('internal/studio/batch_runner.go')
const ACTIVITY_STORE = read('internal/store/activity_store.go')
const AUTH_STORE = read('internal/store/auth_store.go')
const MEMBER_STORE = read('internal/store/workspace_member_store.go')
const LEGACY_STORE = read('internal/store/legacy_import_store.go')
const LEGACY_PAGE = read('apps/web-user/src/studio/pages/LegacyHistoryPage.tsx')
const REVIEW_PAGE = read('apps/web-user/src/studio/pages/ReviewPages.tsx')
const SETTINGS_PAGE = read('apps/web-user/src/studio/pages/SettingsPages.tsx')
const RUN_PAGE = read('apps/web-user/src/studio/pages/RunPages.tsx')
const ROUTES = read('apps/web-user/src/studio/routes.ts')
const ENUM_LABELS = read('apps/web-user/src/lib/enumLabels.ts')
const CLEANING_META = read('apps/web-user/src/views/cleaning/cleaningMeta.ts')
const CD_WORKFLOW = read('.github/workflows/cd.yml')
const MARKDOWN_GUARD = read('test/l15_markdown_ui.mjs')

const failures = []
const results = []

/**
 * stripComments 去掉块注释、`//` 整行与行尾注释。
 *
 * 为什么守卫必须去注释：这些断言检查的是「代码有没有回退到原始值」，
 * 而注释里写「旧实现是 `?? status`」是解释缺陷来源的正当文档。
 * 把注释也当缺陷会产生误报，而**误报的守卫会被直接关掉**（比没有守卫更糟）。
 */
function stripComments(text) {
  // 先去掉块注释（JSDoc 里会**引用**旧实现的写法，例如「旧实现是 `?? status`」，
  // 那是解释缺陷来源的正当文档，不是缺陷本身）。
  const withoutBlocks = text.replace(/\/\*[\s\S]*?\*\//g, '')
  return withoutBlocks
    .split('\n')
    .map((line) => {
      if (line.trimStart().startsWith('//')) return ''
      const index = line.indexOf('//')
      return index >= 0 ? line.slice(0, index) : line
    })
    .join('\n')
}

function record(name, ok, detail) {
  results.push({ name, ok, detail })
  console.log(`[${ok ? 'PASS' : 'FAIL'}] ${name}: ${detail}`)
  if (!ok) failures.push(name)
}

// ---------------------------------------------------------------------------
// 谓词函数（变异用例把「改坏的源码」喂进这些函数，断言返回的列表非空）
// ---------------------------------------------------------------------------

/** #190：容量口径必须是**服务端**事实，并且入口校验真的调用它。 */
function problemsWithBatchCapacityValidation(modelSrc, storeSrc) {
  const problems = []
  if (!/func CoverageCapacity\(/.test(modelSrc)) {
    problems.push('model 层缺少 CoverageCapacity（容量口径没有权威实现）')
  }
  if (!/func AllocateCoverageUnits\(/.test(modelSrc)) {
    problems.push('model 层缺少 AllocateCoverageUnits（分配与容量会各自演化）')
  }
  if (!/CoverageCapacity\(coverage\)/.test(storeSrc)) {
    problems.push('批次入口没有调用 CoverageCapacity，计划量超过可产出量时不会被拒绝')
  }
  // 错误必须落到**字段级**（unitCount），否则前端无法定位到那个输入框。
  if (!/"unitCount"/.test(storeSrc)) {
    problems.push('容量拒绝没有给出 unitCount 字段错误（前端无法定位到计划量输入框）')
  }
  return problems
}

/** #190：`completed < planned` 一律不得聚合为 completed。 */
function problemsWithHonestBatchStatus(storeSrc, runnerSrc) {
  const problems = []
  // 状态推导里必须存在「完成数达到计划数」这个条件，而不是只看 total。
  if (!/completed\s*>=\s*planned/.test(storeSrc)) {
    problems.push('状态聚合没有比较 completed 与 planned，缺口会被当成已完成')
  }
  if (!/completed\+failed == total && completed < planned/.test(storeSrc)) {
    problems.push('缺少「全部定稿但产出少于计划」的分支')
  }
  // 终态事件必须能区分「完成」与「有缺口」。
  if (!/BatchEventPartialFailed/.test(runnerSrc)) {
    problems.push('runner 终态只发 BatchCompleted，缺口不会出现在事件时间线上')
  }
  if (!/shortfall/i.test(runnerSrc)) {
    problems.push('runner 终态事件没有带上缺口数字')
  }
  return problems
}

/** #191：展示层映射不得把原始内部 code 回传给用户。 */
function problemsWithEnumLabelLeaks(activitySrc, enumSrc, cleaningSrc) {
  const problems = []
  // 后端：未登记动作必须有派生的中文兜底，而不是 `操作 %s` 原样回传。
  const fallbackBody = activitySrc.match(/func auditActionFallback\([^)]*\)[^{]*\{([\s\S]*?)\n\}/)
  if (!fallbackBody) {
    problems.push('activity_store 缺少未登记动作的中文兜底（会回传原始 action code）')
  } else {
    const body = fallbackBody[1]
    // 兜底函数必须**推导**中文（用到资源/动词表），而不是把入参原样返回。
    if (/return\s+action\s*$|return\s+action\b(?!\s*\+)/m.test(body) && !/auditResourceLabels|verb/.test(body)) {
      problems.push('auditActionFallback 直接把原始 action code 返回给用户')
    }
    if (!/配置变更/.test(body)) {
      problems.push('auditActionFallback 缺少最后的中性兜底文案')
    }
  }
  if (/操作 %s/.test(activitySrc)) {
    problems.push('activity_store 仍然把原始 action code 拼进用户可见文案（`操作 %s`）')
  }
  if (!/blueprint_version_created/.test(activitySrc)) {
    problems.push('审计动作表缺少 blueprint_version_created（#191 实测命中的键）')
  }
  // 前端：describe* 函数不得以 `?? raw` / `return raw` 兜底。
  if (!/export function describeAuditAction\(/.test(enumSrc)) {
    problems.push('enumLabels 缺少 describeAuditAction')
  }
  if (/\?\?\s*raw\b|return\s+raw\s*$/m.test(enumSrc)) {
    problems.push('enumLabels 仍存在把原始值回传的兜底')
  }
  // 清洗工作台以前用 `?? status`，会把 directions_completed 直接显示出来。
  //
  // 只检查**代码**：注释里写「旧实现是 `?? status`」是解释缺陷来源的正当文档，
  // 把注释也当缺陷会让守卫产生误报 —— 而误报会让人直接关掉守卫。
  if (/\?\?\s*status\b/.test(stripComments(cleaningSrc))) {
    problems.push('cleaningMeta 仍用 `?? status` 兜底（会漏出 directions_completed）')
  }
  return problems
}

/** #194：数据与审阅必须是**语义不同**的两个页面。 */
function problemsWithDataReviewSplit(pageSrc, routesSrc) {
  const problems = []
  if (!/queueMode \? 'pending' : ''/.test(pageSrc)) {
    problems.push('两个入口的默认筛选相同（都进同一张表，观感上仍是同一页）')
  }
  if (!/const description = queueMode/.test(pageSrc)) {
    problems.push('两个入口共用同一段说明文案')
  }
  if (!/queueMode \? '审阅队列' : '数据'/.test(pageSrc)) {
    problems.push('两个入口的标题没有区分')
  }
  // 行操作也必须不同名：数据=查看内容，审阅=审阅。
  if (!/queueMode && sample\.capabilities\?\.canReview \? '审阅' : '查看内容'/.test(pageSrc)) {
    problems.push('两个入口的行操作同名同形（用户无法预期点进去做什么）')
  }
  if (!/label: '审阅',/.test(routesSrc)) {
    problems.push('路由元数据里「审阅」标签缺失（菜单仍叫「审阅队列」）')
  }
  return problems
}

/** #194：窄屏表格必须有字段名，否则表头与数据行错位混合。 */
function problemsWithMobileTableLabels(pageSrc, cssSrc) {
  const problems = []
  if (!/data-label="名称"/.test(pageSrc)) {
    problems.push('连接表缺少 data-label，窄屏卡片布局没有字段名')
  }
  if (!/content: attr\(data-label\)/.test(cssSrc)) {
    problems.push('CSS 没有渲染 data-label（窄屏下字段名不会出现）')
  }
  if (!/comparison-row--head \{ display: none/.test(cssSrc)) {
    problems.push('窄屏没有隐藏表头（表头会与单列卡片的数据错位混合）')
  }
  // 注释块会夹在 `{` 与属性之间，因此窗口要给够（实测 ~400 字符）。
  if (!/blueprint-related-config \{[\s\S]{0,800}?flex-wrap: wrap/.test(cssSrc)) {
    problems.push('蓝图右栏没有 flex-wrap（长版本名会压住按钮）')
  }
  return problems
}

/** #195：部署必须显式注入版本，并在部署后自证。 */
function problemsWithDeployVersionAttestation(cdSrc) {
  const problems = []
  // 用**子串匹配**而不是正则：文件里的字面量是 YAML 转义后的
  // `export GIT_SHA=\$(git rev-parse HEAD)`，而 `\$` 在正则里同时涉及
  // 转义与行尾锚点，写错会静默不匹配（本守卫第一版就踩了这个坑）。
  if (!cdSrc.includes('export GIT_SHA=\\$(git rev-parse HEAD)')) {
    problems.push('CD 没有在目标机器上取 HEAD（用 CI 侧的值会得到自信的错误版本）')
  }
  if (!/docker compose up -d --build/.test(cdSrc)) {
    problems.push('CD 缺少 compose 构建步骤')
  }
  if (!/version\.json/.test(cdSrc)) {
    problems.push('CD 没有读回线上 version.json（无法证明部署真的生效）')
  }
  if (!/unknown/.test(cdSrc)) {
    problems.push('CD 没有把 version=unknown 当作失败（#195 的核心症状会被放过）')
  }
  return problems
}

/** #197-15：直接建号路径必须存在，且密码/角色都有校验。 */
function problemsWithDirectUserCreation(authSrc, memberSrc, pageSrc) {
  const problems = []
  if (!/func ValidateInitialPassword\(/.test(authSrc)) {
    problems.push('缺少初始密码强度校验')
  }
  if (!/len\(\[\]rune\(password\)\) < 8/.test(authSrc)) {
    problems.push('密码强度规则缺失或不是「至少 8 位」')
  }
  if (!/EqualFold\(strings\.TrimSpace\(password\), normalizingEmail\(email\)\)/.test(authSrc)) {
    problems.push('没有拦住「密码等于邮箱」这种敷衍输入')
  }
  if (!/func \(s \*WorkspaceMemberStore\) CreateWorkspaceMemberWithAccount\(/.test(memberSrc)) {
    problems.push('store 层缺少「建号 + 加成员」的复合命令')
  }
  // 建号与加成员必须在同一事务里，否则会留下「有账号没工作区」的半成品。
  if (!/tx\.QueryRow\(ctx, `\s*INSERT INTO users/.test(memberSrc)) {
    problems.push('建号没有走事务（可能留下账号与成员关系不一致的半成品）')
  }
  if (!/createWorkspaceMemberDirect/.test(pageSrc)) {
    problems.push('前端没有调用直接建号接口（表单填了也发不出去）')
  }
  return problems
}

/** #197-16：迁移结论必须来自服务端读模型，不能在页面里硬编码。 */
function problemsWithMigrationStatusHonesty(storeSrc, pageSrc) {
  const problems = []
  if (!/func \(s \*LegacyImportStore\) LegacyMigrationStatus\(/.test(storeSrc)) {
    problems.push('store 层缺少迁移对账读模型（结论会变成硬编码）')
  }
  if (!/legacy_dataset_id IS NOT NULL/.test(storeSrc)) {
    problems.push('对账没有统计「绑定到项目的旧数据集」（无法判断是否迁移完）')
  }
  if (!/migrationComplete/.test(storeSrc)) {
    problems.push('读模型没有给出可判定的结论字段')
  }
  if (!/migrationStatus\?\.note|migrationStatus\.note/.test(pageSrc)) {
    problems.push('页面没有渲染服务端结论')
  }
  if (/尚未迁移<\/strong>|迁移状态：<strong>尚未迁移/.test(pageSrc)) {
    problems.push('页面硬编码了迁移结论（迁移真的完成后仍会显示未迁移）')
  }
  return problems
}

/** #197-13：数据集分析必须由服务端算，且空数据集不得给出 0 分结论。 */
function problemsWithDatasetAnalysis(storeSrc, pageSrc, analysisSrc) {
  const problems = []
  if (!/func \(s \*BatchStore\) ListSampleVersionFacts\(/.test(storeSrc)) {
    problems.push('store 缺少样本版本分析事实读取（前端只能拉全量自己算）')
  }
  if (!/func AnalyzeDataset\(/.test(analysisSrc)) {
    problems.push('studio 缺少服务端分析实现')
  }
  // 分位数必须真的算，而不是给一个固定值。
  if (!/nearestRank\(0\.9\)/.test(analysisSrc)) {
    problems.push('缺少 P90 计算（长度分布只剩最短/最长）')
  }
  if (!/sampleCount === 0/.test(pageSrc)) {
    problems.push('前端没有区分「空数据集」与「0 分结论」')
  }
  return problems
}

/** #192：Markdown 守卫必须是全树扫描，而不是手工文件清单。 */
function problemsWithMarkdownGuardCoverage(guardSrc) {
  const problems = []
  if (!/collectSources\(/.test(guardSrc)) {
    problems.push('守卫不是递归扫描（手工清单会漏页，这正是 #192 的成因）')
  }
  if (!/stripComments\(/.test(guardSrc)) {
    problems.push('守卫没有剥离注释（块注释里的 Markdown 会产生误报，误报会让人关掉守卫）')
  }
  // 手工清单的形态：一个硬编码的 files 数组。
  if (/const files = \[\s*'apps\/web-user/.test(guardSrc)) {
    problems.push('守卫仍然是手工文件清单（#192 的原始缺陷形态）')
  }
  return problems
}

// ---------------------------------------------------------------------------
// 判定
// ---------------------------------------------------------------------------

const checks = [
  ['#190 批次容量校验（服务端事实 + 字段级拒绝）',
    problemsWithBatchCapacityValidation(MODEL_DOCS, BATCH_STORE)],
  ['#190 诚实的批次终态（completed 需真的产出完）',
    problemsWithHonestBatchStatus(BATCH_STORE, BATCH_RUNNER)],
  ['#191 枚举/事件键不再漏出内部英文',
    problemsWithEnumLabelLeaks(ACTIVITY_STORE, ENUM_LABELS, CLEANING_META)],
  ['#194 数据与审阅是两个语义不同的页面',
    problemsWithDataReviewSplit(REVIEW_PAGE, ROUTES)],
  ['#194 窄屏表格卡片化 + 右栏不重叠',
    problemsWithMobileTableLabels(SETTINGS_PAGE, read('apps/web-user/src/styles.css'))],
  ['#195 部署版本注入与自证',
    problemsWithDeployVersionAttestation(CD_WORKFLOW)],
  ['#197-15 用户名 + 初始密码直接建号',
    problemsWithDirectUserCreation(AUTH_STORE, MEMBER_STORE, SETTINGS_PAGE)],
  ['#197-16 迁移结论来自服务端读模型',
    problemsWithMigrationStatusHonesty(LEGACY_STORE, LEGACY_PAGE)],
  ['#197-13 数据集分析由服务端计算且区分空集',
    problemsWithDatasetAnalysis(BATCH_STORE, RUN_PAGE, read('internal/studio/dataset_analysis.go'))],
  ['#192 Markdown 守卫覆盖全树而非手工清单',
    problemsWithMarkdownGuardCoverage(MARKDOWN_GUARD)],
]

for (const [name, problems] of checks) {
  record(name, problems.length === 0, problems.length === 0 ? '结构断言通过' : problems.join('；'))
}

// ---------------------------------------------------------------------------
// 变异自证：把「改坏的源码」喂进谓词，断言它**真的会报问题**
// ---------------------------------------------------------------------------

const mutations = [
  ['#190 删掉入口容量校验', problemsWithBatchCapacityValidation(MODEL_DOCS,
    BATCH_STORE.replace(/CoverageCapacity\(coverage\)/, 'len(coverage.Domains)'))],
  ['#190 把状态比较退回只看 total', problemsWithHonestBatchStatus(
    BATCH_STORE.replace(/completed\+failed == total && completed < planned/, 'false'), BATCH_RUNNER)],
  ['#191 让兜底回传原始 action code', problemsWithEnumLabelLeaks(ACTIVITY_STORE.replace(
    /func auditActionFallback\(action string\) string \{[\s\S]*?\n\}/, 'func auditActionFallback(action string) string { return action }'),
    ENUM_LABELS, CLEANING_META)],
  ['#194 两个入口共用默认筛选', problemsWithDataReviewSplit(
    REVIEW_PAGE.replace(/queueMode \? 'pending' : ''/, "'pending'"), ROUTES)],
  // 必须替换**全部**出现：连接表与存储表各有 `data-label="名称"`，
  // 只替换第一处会让断言仍然通过（第一版变异就是这么空转的）。
  ['#194 删掉窄屏字段名', problemsWithMobileTableLabels(
    SETTINGS_PAGE.replaceAll('data-label="名称"', ''), read('apps/web-user/src/styles.css'))],
  // 用 split/join 做**无条件**替换：`\$` 在 JS 字符串与正则里都要二次转义，
  // 上一版正则静默不匹配 → 变异体等于原文 → 断言空转（守卫自己抓到了这一点）。
  ['#195 不再注入版本', problemsWithDeployVersionAttestation(
    CD_WORKFLOW.split('export GIT_SHA=\\$(git rev-parse HEAD)').join('export GIT_SHA=unknown'))],
  ['#197-15 放宽密码强度到「非空即可」', problemsWithDirectUserCreation(
    AUTH_STORE.replace(/len\(\[\]rune\(password\)\) < 8/, 'len(password) == 0'), MEMBER_STORE, SETTINGS_PAGE)],
  ['#197-16 在页面上硬编码结论', problemsWithMigrationStatusHonesty(LEGACY_STORE,
    LEGACY_PAGE.replace(/migrationStatus\.note/g, '尚未迁移</strong>'))],
  ['#197-13 删掉 P90 计算', problemsWithDatasetAnalysis(BATCH_STORE, RUN_PAGE,
    read('internal/studio/dataset_analysis.go').replace(/nearestRank\(0\.9\)/, '0'))],
  ['#192 退回手工文件清单', problemsWithMarkdownGuardCoverage(
    MARKDOWN_GUARD
      .replace(/function collectSources\(/, 'function unusedCollectSources(')
      .replace('const files = collectSources(SRC_ROOT)', "const files = ['apps/web-user/src/App.tsx']"),
  )],
]

for (const [name, problems] of mutations) {
  record(
    `变异：${name} -> 断言必须报错`,
    problems.length > 0,
    problems.length > 0 ? `捕获到 ${problems.length} 个问题` : '断言空转（改坏了却仍然通过）',
  )
}

if (failures.length > 0) {
  console.error(`\nIssue #197 回归守卫失败：${failures.length} 项`)
  process.exitCode = 1
} else {
  console.log(`\nIssue #197 回归守卫通过（${checks.length} 条结构断言 + ${mutations.length} 条变异自证）`)
}
