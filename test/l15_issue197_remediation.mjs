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
// #206：批次事件模型（eventTypeLabel 字段的落点）。
const MODEL_BATCH = read('internal/model/batch.go')
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
const APP_TSX = read('apps/web-user/src/App.tsx')
const ADMIN_PAGE = read('apps/web-user/src/studio/pages/AdminWorkspacePage.tsx')
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

/**
 * #191 第四条渲染路径：审计表的「**资源**」列。
 *
 * 为什么单列一个谓词：#191 上一轮修的是「动作」列（`audit_logs.action`），
 * 而「资源」列读的是**另一个取值域** `audit_logs.resource_type`
 * （`sample_version` / `workspace_member` / `blueprint_version` …），
 * 当时完全没有映射，于是在真实 Chromium 里仍然显示英文内部键。
 * 把两者分开断言，是为了让「只修一半」这种形态在结构上不再可能。
 *
 * 三个渲染载体（都在旧控制台）：
 *   1. `App.tsx` 的 `auditColumns`（`/console/admin/audit` 主表）；
 *   2. `AdminWorkspacePage.tsx` 治理面板的「最近操作」（前 6 条）；
 *   3. `AdminWorkspacePage.tsx` 治理面板的「操作记录」全表。
 */
function problemsWithAuditResourceLabels(enumSrc, appSrc, adminSrc) {
  const problems = []
  if (!/export function describeAuditResource\(/.test(enumSrc)) {
    problems.push('enumLabels 缺少 describeAuditResource（资源列没有中文映射）')
  }
  if (!/AUDIT_RESOURCE_LABELS/.test(enumSrc)) {
    problems.push('enumLabels 缺少 AUDIT_RESOURCE_LABELS 资源文案表')
  }
  // 兜底不得回传原始 resourceType（与动作列同一条契约）。
  const fn = enumSrc.match(/export function describeAuditResource\([^)]*\)[^{]*\{([\s\S]*?)\n\}/)
  if (!fn) {
    problems.push('describeAuditResource 结构无法解析，守卫失效')
  } else if (/\breturn\s+raw\b/.test(fn[1])) {
    problems.push('describeAuditResource 把原始 resourceType 回传给用户')
  }
  // 载体 1：App.tsx 的 auditColumns 必须接线。
  if (!/describeAuditResource\(value\)/.test(appSrc)) {
    problems.push('App.tsx 的「资源」列没有调用 describeAuditResource（#191 实测泄漏点）')
  }
  // 载体 2 + 3：治理面板两张表都必须接线（同一缺陷的两个额外载体）。
  const wired = (adminSrc.match(/describeAuditResource\(/g) ?? []).length
  if (wired < 2) {
    problems.push(`AdminWorkspacePage 只有 ${wired} 处审计表接到 describeAuditResource（应为「最近操作」与「操作记录」两张）`)
  }
  // 可见文案不得直接插值原始 code；只允许出现在 title 上（便于排查）。
  const visibleRawLeak = /(?<!title=\{`[^`]{0,40})\$\{item\.resourceType\} #\$\{item\.resourceId\}/
  if (visibleRawLeak.test(adminSrc.replace(/<span title=\{[^}]*\}>/g, '<span>'))) {
    problems.push('AdminWorkspacePage 把原始 resourceType 当可见文案渲染（未放进 title）')
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

/**
 * #211：审阅状态（`review_projections.effective_action`）不得裸渲染。
 *
 * 缺陷形态：新建质量实验的「检查范围」直接渲染 `sample.reviewStatus`，
 * 中文界面里漏出 `pending` / `accepted`。同一类形态在本仓已有先例：
 * ReviewPages 旧实现用 `LABEL[status] ?? status` 兜底，未知值同样漏出枚举。
 *
 * 两条断言：
 *   1. `QualityPages.tsx` 的审阅状态列必须走 `describeReviewStatus`；
 *   2. 任何 `?? <xxx>.reviewStatus` / `?? effective` 式的**原始值回退**都不得存在
 *      （回退即漏出，这是同一缺陷的另一种写法）。
 */
function problemsWithReviewStatusLabels(qualitySrc, reviewSrc, enumSrc) {
  const problems = []
  if (!/export function describeReviewStatus\(/.test(enumSrc)) {
    problems.push('enumLabels 缺少 describeReviewStatus（审阅状态没有单一来源）')
  }
  if (!/export function reviewStatusColor\(/.test(enumSrc)) {
    problems.push('enumLabels 缺少 reviewStatusColor（颜色与文案会各自演化）')
  }
  // 检查范围表格必须接线（#211 的实测泄漏点）。
  if (!/describeReviewStatus\(sample\.reviewStatus\)/.test(qualitySrc)) {
    problems.push('QualityPages 的审阅状态列没有调用 describeReviewStatus（#211 实测泄漏点）')
  }
  // 审阅队列页也必须用同一份映射：它原先有一个本地表 + `?? status` 兜底，
  // 两者共存会让同一状态在「队列」与「检查范围」出现两种译法。
  if (!/describeReviewStatus\(/.test(reviewSrc)) {
    problems.push('ReviewPages 的审阅状态没有走 describeReviewStatus（会出现第二张映射表）')
  }
  if (!/reviewStatusColor\(/.test(reviewSrc)) {
    problems.push('ReviewPages 的标签颜色没有走 reviewStatusColor')
  }
  // 原始值回退形态：`?? sample.reviewStatus` / `?? item.reviewStatus` / `?? effective`。
  for (const [name, src] of [['QualityPages', qualitySrc], ['ReviewPages', reviewSrc]]) {
    // 注释可能**解释**缺陷来源（「旧实现用 `?? reviewStatus`」），
    // 把注释当缺陷是误报，而误报会让人直接关掉守卫 —— 因此先剥注释。
    const code = stripComments(src)
    if (/\?\?\s*(sample|item|detail\.sample)?\.?reviewStatus\b/.test(code)) {
      problems.push(`${name} 仍用 ?? reviewStatus 兜底（未知值会把内部枚举漏给用户）`)
    }
    if (/\?\?\s*effective\b/.test(code)) {
      problems.push(`${name} 仍用 ?? effective 兜底`)
    }
  }
  // #211 的第二个要求：必须说明未审阅内容能否纳入评测。
  if (!/unreviewedScopeNotice/.test(qualitySrc)) {
    problems.push('QualityPages 未说明「未审阅内容能否纳入评测」（#211 第 2 项要求）')
  }
  return problems
}

/**
 * #206：批次详情「事件时间线」不得裸渲染内部事件键。
 *
 * 缺陷形态：`RunPages.tsx` 直接输出 `{event.eventType}`（BatchPartialFailed
 * 等），与「动态」列表的中文文案分叉。
 *
 * 断言：
 *   1. 不得出现 `{event.eventType}` 原样渲染（#206 建议的守卫）；
 *   2. 必须消费服务端下发的 `eventTypeLabel`（而不是前端再抄一张表）；
 *   3. 必须把 detail 里的可诊断字段显示出来（单元号/错误类/缺口）。
 */
function problemsWithBatchTimelineLabels(runSrc, modelSrc, apiSrc, storeSrc) {
  const problems = []
  // 先剥注释：修复说明里会引用 `{event.eventType}` 这个旧形态，
  // 不剥会让守卫对**自己的文档**报错（误报 → 有人直接关掉守卫）。
  const runCode = stripComments(runSrc)
  // 排除 `title={event.eventType}`：把原始 code 保留在 title 上是**刻意**的
  // （与本仓其它页面的惯例一致，便于排查），不是泄漏。
  // 不加负向断言的话，合法用法会把守卫弄成永久红灯。
  const rawRender = runCode.replace(/title=\{\s*event\.eventType\s*\}/g, '')
  if (/\{\s*event\.eventType\s*\}/.test(rawRender)) {
    problems.push('RunPages 仍原样渲染 {event.eventType}（#206 的缺陷形态）')
  }
  if (!/event\.eventTypeLabel/.test(runCode)) {
    problems.push('RunPages 时间线没消费服务端下发的 eventTypeLabel')
  }
  // 时间线兜底不得回退成原始 code（用 title 保留才是对的）。
  if (/event\.eventTypeLabel\s*\|\|\s*event\.eventType\b/.test(runCode)) {
    problems.push('时间线把原始 eventType 当作文案兜底（泄漏再现）')
  }
  if (!/describeBatchEventDetail\(/.test(runCode)) {
    problems.push('时间线没有显示 detail 里的可诊断字段（12 行「部分失败」无法区分）')
  }
  // 服务端必须真的下发文案，且与动态列表用**同一张表**（不得新建第二张）。
  //
  // 断言 JSON 标签而不是字段名：字段名在同一段注释里被反复引用（解释为什么要
  // 下发），只查名字时「删字段但留注释」会让断言空转 —— 变异自证把它拓出来了。
  if (!/json:"eventTypeLabel"/.test(modelSrc)) {
    problems.push('model.BatchEvent 缺少 eventTypeLabel 字段（前端拿不到中文文案）')
  }
  if (!/DescribeBatchEvent/.test(apiSrc) || !/DescribeBatchEvent/.test(storeSrc)) {
    problems.push('API 未从 store 的 batchEventLabels 下发事件文案（会诱发第二张表）')
  }
  return problems
}

// ---------------------------------------------------------------------------
// #214 第 2 轮新增的子项守卫（#200 / #205 / #207 / #210 / #213）
//
// 这五条的共同形态是「同一份事实在两个地方各自实现」：总览与队列各数一次、
// 磁贴与当前页共用一个 href、文档承诺与实现分家、审计记录与「查看」目标脱钩、
// 服务端字段错误与页面提示脱钩。因此断言的都是**接线**，而不只是「函数存在」。
// ---------------------------------------------------------------------------

/** #200：总览的待判断计数必须与审阅队列共用同一条谓词。 */
function problemsWithOverviewQueueParity(activitySrc, sampleQuerySrc) {
  const problems = []
  // 队列侧提供共享常量（唯一 SQL 来源）。
  if (!/const latestReviewProjectionJoin\s*=/.test(sampleQuerySrc)) {
    problems.push('sample_query 缺少共享的投影连接常量（总览会各写一份）')
  }
  if (!/const effectiveReviewStatusSQL\s*=\s*`COALESCE\(rp\.effective_action, 'pending'\)`/.test(sampleQuerySrc)) {
    problems.push('sample_query 缺少「无投影视为 pending」的唯一表达式')
  }
  // 总览侧必须**复用**它，而不是直接数 review_projections 的行。
  if (!/latestReviewProjectionJoin/.test(activitySrc) || !/effectiveReviewStatusSQL/.test(activitySrc)) {
    problems.push('总览没有复用队列口径（零判断项目上两者必然分叉）')
  }
  if (/SELECT COUNT\(\*\) FROM review_projections\b/.test(activitySrc)) {
    problems.push('总览仍在直接数 review_projections 的行（#200 的缺陷形态）')
  }
  return problems
}

/** #205：总览磁贴的 href 不得指向当前页自身，且「被挡住 N」必须有独立出口。 */
function problemsWithOverviewTileTargets(todaySrc, apiTypes) {
  const problems = []
  const code = stripComments(todaySrc)
  // `studioPath('today')` 出现在磁贴的 href 上就是「点了原地不动」。
  // 允许它出现在其它地方（例如渲染 `/today` 页自身的组件），因此只看磁贴行。
  const tileLines = code.split('\n').filter((line) => line.includes('data-overview-tile='))
  for (const line of tileLines) {
    if (/href=\{studioPath\('today'\)\}/.test(line)) {
      problems.push(`磁贴指向当前页自身：${line.trim().slice(0, 80)}`)
    }
  }
  if (!/overviewProjectHref\(/.test(code)) {
    problems.push('磁贴没有走项目内页跳转函数（会退回硬编码路径）')
  }
  // 「被挡住 N」必须有自己的目标：交付库按定义只显示已发布版本。
  if (!/data-overview-blocked-link=/.test(code)) {
    problems.push('「被挡住的候选」缺少独立出口（#205 第二条）')
  }
  // 深链需要一个项目 ID；它必须来自服务端（前端不得猜）。
  if (!/scopedProjectIds/.test(apiTypes)) {
    problems.push('WorkspaceOverview 未下发 scopedProjectIds（磁贴只能回项目列表）')
  }
  return problems
}

/** #207：帮助页承诺的审阅 J/K 快捷键必须在三栏审阅页真的实现。 */
function problemsWithReviewShortcuts(reviewSrc, helpSrc) {
  const problems = []
  const reviewCode = stripComments(reviewSrc)
  const helpCode = stripComments(helpSrc)
  const promisesJK = /'J \/ K/.test(helpCode) || /J \/ K/.test(helpCode)
  if (!promisesJK) {
    problems.push('帮助页已不再承诺 J/K（若刻意删除，请同步更新本断言与 issue #207）')
  }
  if (!/addEventListener\('keydown'/.test(reviewCode)) {
    problems.push('三栏审阅页没有 keydown 监听（帮助页承诺了不存在的功能）')
  }
  // 必须复用已有的相对导航（否则按钮能用、键盘不能用，且提示文案会分叉）。
  // 允许经 ref 间接调用（`goRelativeRef.current(...)`）—— 那是为了避免把
  // 频繁变化的 callback 放进 keydown 监听的依赖里（每次导航都重装监听器）。
  if (!/useRef\(goRelative\)/.test(reviewCode) || !/goRelative\w*\.current\(/.test(reviewCode)) {
    problems.push('键盘导航没有复用 goRelative（会与「上一条/下一条」按钮行为分叉）')
  }
  // 焦点在输入控件里时不得抢键：审阅页有判断理由输入框，抢键会吞掉用户正在写的字母。
  if (!/INPUT'\s*\|\|\s*tag === 'TEXTAREA'/.test(reviewCode)) {
    problems.push('键盘导航没有排除输入控件（在理由输入框里敲 j 会跳走）')
  }
  if (!/altKey \|\| event\.ctrlKey \|\| event\.metaKey/.test(reviewCode)) {
    problems.push('键盘导航没有排除修饰键（会与浏览器/系统快捷键冲突）')
  }
  return problems
}

/** #210：审计类动态的链接必须由服务端指向对象，而不是项目概览。 */
function problemsWithAuditActivityLinks(activitySrc) {
  const problems = []
  if (!/func auditActivityLink\(/.test(activitySrc)) {
    problems.push('缺少 auditActivityLink（审计类链接没有权威实现）')
  }
  if (!/a\.resource_type/.test(activitySrc)) {
    problems.push('动态查询没有带出 resource_type（服务端无法知道该跳到哪个对象）')
  }
  if (!/item\.Source == model\.ActivitySourceAudit/.test(activitySrc)) {
    problems.push('审计类没有走专门的链接推导（会退回概览）')
  }
  // 审计分支不得直接把概览当默认值 —— 除「本项目内但未登记的资源」那一处外，
  // 函数体里出现「无对象标识时退回概览」即为缺陷再现。
  const body = activitySrc.match(/func auditActivityLink\([\s\S]*?\n\}/)
  if (!body) {
    problems.push('无法读取 auditActivityLink 函数体（本断言会空转）')
    return problems
  }
  if (!/return model\.Links\{"page": "\/activity"\}/.test(body[0])) {
    problems.push('无对象标识时没有诚实的退路（会指到一个并不持有该对象的页）')
  }
  return problems
}

/** #213：映射复选框必须有行内可访问名；发布表单必须按字段展示错误。 */
function problemsWithAccessibleMappingAndFieldErrors(editorSrc, releaseSrc) {
  const problems = []
  const editorCode = stripComments(editorSrc)
  const releaseCode = stripComments(releaseSrc)
  // 复选框必须有可访问名，且名字要**含本行字段标识**，不能只是同一句「必填」。
  if (!/aria-label=\{`把\$\{accessible\}设为必填`\}/.test(editorCode)) {
    problems.push('映射行的「必填」复选框缺少行内可访问名（读屏只会念三个「必填」）')
  }
  // 发布表单：字段级提示必须真的渲染在页面上（而不是只存在于 state 里）。
  if (!/fieldErrors\.intendedUse/.test(releaseCode)) {
    problems.push('「用途」没有字段级错误渲染（多字段错误仍只显示最后一条）')
  }
  if (!/applyServerFieldErrors\(/.test(releaseCode)) {
    problems.push('没有消费服务端 fieldErrors（结构化错误被压成一句话）')
  }
  if (!/aria-describedby=/.test(releaseCode)) {
    problems.push('字段错误没有与输入框用 aria-describedby 关联（读屏拿不到）')
  }
  return problems
}

// ---------------------------------------------------------------------------
// 判定
// ---------------------------------------------------------------------------

const HELP_PAGE = read('apps/web-user/src/studio/pages/SettingsPages.tsx')
const TODAY_PAGE = read('apps/web-user/src/studio/pages/TodayPages.tsx')
const DOCUMENT_EDITORS = read('apps/web-user/src/studio/DocumentEditors.tsx')
const RELEASE_PAGE = read('apps/web-user/src/studio/pages/ReleasePages.tsx')
const STUDIO_API_TYPES = read('apps/web-user/src/lib/api/studio.ts')

const checks = [
  ['#190 批次容量校验（服务端事实 + 字段级拒绝）',
    problemsWithBatchCapacityValidation(MODEL_DOCS, BATCH_STORE)],
  ['#190 诚实的批次终态（completed 需真的产出完）',
    problemsWithHonestBatchStatus(BATCH_STORE, BATCH_RUNNER)],
  ['#191 枚举/事件键不再漏出内部英文',
    problemsWithEnumLabelLeaks(ACTIVITY_STORE, ENUM_LABELS, CLEANING_META)],
  ['#191 审计表「资源」列不再漏出内部英文键',
    problemsWithAuditResourceLabels(ENUM_LABELS, APP_TSX, ADMIN_PAGE)],
  ['#211 审阅状态不再裸渲染 + 说明未审阅内容口径',
    problemsWithReviewStatusLabels(read('apps/web-user/src/studio/pages/QualityPages.tsx'),
      REVIEW_PAGE, ENUM_LABELS)],
  ['#206 批次事件时间线不再漏出内部事件键',
    problemsWithBatchTimelineLabels(RUN_PAGE, MODEL_BATCH, read('apps/api/routes_studio_batches.go'),
      ACTIVITY_STORE)],
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
  ['#200 总览待判断与审阅队列同一口径',
    problemsWithOverviewQueueParity(ACTIVITY_STORE, read('internal/store/sample_query.go'))],
  ['#205 总览磁贴不指向当前页 + 被挡住有出口',
    problemsWithOverviewTileTargets(TODAY_PAGE, STUDIO_API_TYPES)],
  ['#207 帮助页承诺的审阅 J/K 快捷键已实现',
    problemsWithReviewShortcuts(REVIEW_PAGE, HELP_PAGE)],
  ['#210 审计类动态链接指向操作对象',
    problemsWithAuditActivityLinks(ACTIVITY_STORE)],
  ['#213 交付映射复选框可访问名 + 发布表单字段级错误',
    problemsWithAccessibleMappingAndFieldErrors(DOCUMENT_EDITORS, RELEASE_PAGE)],
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
  // 变异必须同时打断**映射**与**接线**：只删映射或只删接线，两者都要报问题。
  ['#191 让资源列回传原始 resourceType', problemsWithAuditResourceLabels(
    ENUM_LABELS.replace(/export function describeAuditResource\([^)]*\)[^{]*\{[\s\S]*?\n\}/,
      'export function describeAuditResource(raw: string) { return raw }'), APP_TSX, ADMIN_PAGE)],
  ['#191 摘掉治理面板的资源列接线', problemsWithAuditResourceLabels(
    ENUM_LABELS, APP_TSX, ADMIN_PAGE.replaceAll('describeAuditResource(item.resourceType)', 'item.resourceType').replaceAll('describeAuditResource(value)', 'value'))],
  ['#211 让审阅状态退回裸渲染', problemsWithReviewStatusLabels(
    read('apps/web-user/src/studio/pages/QualityPages.tsx').replace(
      /describeReviewStatus\(sample\.reviewStatus\)/, 'sample.reviewStatus'), REVIEW_PAGE, ENUM_LABELS)],
  ['#211 让颜色/文案映射退化', problemsWithReviewStatusLabels(
    read('apps/web-user/src/studio/pages/QualityPages.tsx'),
    REVIEW_PAGE.replace(/describeReviewStatus\(/g, 'noop('), ENUM_LABELS)],
  ['#206 让时间线退回原样渲染 eventType', problemsWithBatchTimelineLabels(
    RUN_PAGE.replace(/event\.eventTypeLabel \|\| '批次事件'/, 'event.eventType'),
    MODEL_BATCH, read('apps/api/routes_studio_batches.go'), ACTIVITY_STORE)],
  ['#206 删掉服务端下发的文案字段', problemsWithBatchTimelineLabels(
    RUN_PAGE, MODEL_BATCH.replace('`json:"eventTypeLabel"`', '`json:"removed"`'),
    read('apps/api/routes_studio_batches.go'), ACTIVITY_STORE)],
  ['#194 两个入口共用默认筛选', problemsWithDataReviewSplit(
    REVIEW_PAGE.replace(/queueMode \? 'pending' : ''/, "'pending'"), ROUTES)],
  // 必须替换**全部**出现：连接表与存储表各有 `data-label="名称"`，
  // 只替换第一处会让断言仍然通过（第一版变异就是这么空转的）。
  ['#194 删掉窄屏字段名', problemsWithMobileTableLabels(
    SETTINGS_PAGE.replaceAll('data-label="名称"', ''), read('apps/web-user/src/styles.css'))],
  // 用 split/join 做**无条件**替换：`\$` 在 JS 字符串与正则里都要二次转义，
  // 上一版正则静默不匹配 -> 变异体等于原文 -> 断言空转（守卫自己抓到了这一点）。
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
  ['#200 让总览退回「直接数投影行」', problemsWithOverviewQueueParity(
    ACTIVITY_STORE.replace('FROM samples s`+latestReviewProjectionJoin+`', 'FROM review_projections' )
      .replace("SELECT COUNT(*) FROM review_projections\n    WHERE s.project_id", 'SELECT COUNT(*) FROM review_projections\n    WHERE project_id'),
    read('internal/store/sample_query.go'))],
  ['#205 让磁贴退回指向当前页', problemsWithOverviewTileTargets(
    TODAY_PAGE.replace("href={overviewProjectHref(overview, 'project.runs')}", "href={studioPath('today')}"),
    STUDIO_API_TYPES)],
  ['#205 删掉「被挡住」的独立出口', problemsWithOverviewTileTargets(
    TODAY_PAGE.replaceAll('data-overview-blocked-link=', 'data-unused='), STUDIO_API_TYPES)],
  ['#207 摘掉 J/K 键盘监听', problemsWithReviewShortcuts(
    REVIEW_PAGE.replace(/document\.addEventListener\('keydown', onKeyDown\)/, 'void onKeyDown'),
    HELP_PAGE)],
  ['#207 让键盘导航在输入框里抢键', problemsWithReviewShortcuts(
    REVIEW_PAGE.replace(/tag === 'INPUT' \|\| tag === 'TEXTAREA'/, 'false'), HELP_PAGE)],
  ['#210 让审计链接退回概览', problemsWithAuditActivityLinks(
    ACTIVITY_STORE.replace(/func auditActivityLink\([\s\S]*?\n\}/, 'func auditActivityLink(projectID int64, resourceType string, objectID, sampleObjectID int64, documentVersion int64) model.Links { return model.Links{"page": projectPageLink(projectID, "overview")} }'))],
  ['#213 摘掉复选框的行内可访问名', problemsWithAccessibleMappingAndFieldErrors(
    DOCUMENT_EDITORS.replace(/aria-label=\{`把\$\{accessible\}设为必填`\}/, ''), RELEASE_PAGE)],
  ['#213 让发布错误退回单行总体提示', problemsWithAccessibleMappingAndFieldErrors(
    DOCUMENT_EDITORS, RELEASE_PAGE.replaceAll('fieldErrors.intendedUse', 'errorLines.intendedUse'))],
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
