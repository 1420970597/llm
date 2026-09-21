#!/usr/bin/env node
/**
 * 文档链接与代码引用检查（R12 lane 的验证工具）。
 *
 * 运行：
 *   node scripts/check-docs.mjs
 *
 * 检查三件事，全部针对本轮文档（README.md + docs/**）：
 *
 *   1. 相对链接可解析：`[文字](路径)` 与 `<img src="路径">` 里的相对路径
 *      必须真实存在于仓库中（按 markdown 文件所在目录解析）。
 *      外链（http/https/mailto）与纯锚点跳过。
 *   2. 同文档锚点可解析：`](#anchor)` 必须能对应到本文件某个标题。
 *      锚点按 GitHub 的 slug 规则计算（小写、去标点、空格转连字符）。
 *   3. 代码引用真实存在：正文里 `反引号` 包起来的、形如路径的引用
 *      （含 `/` 且带已知扩展名）必须在仓库中存在。
 *      这一条用于防止文档描述的文件被改名/删除后文档静默漂移。
 *
 * 为什么不用现成的 markdown 链接检查器：本机没有装任何 markdown 工具链，
 * 且需要第 3 条「代码引用」检查（通用工具不做）。这里只用 Node 内置模块。
 *
 * 退出码：发现任何问题返回 1，全部通过返回 0。
 */

import { existsSync, readFileSync, readdirSync, statSync } from 'node:fs'
import { execFileSync } from 'node:child_process'
import path from 'node:path'
import { fileURLToPath } from 'node:url'

const REPO_ROOT = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..')

/**
 * 仓库的「主 checkout」目录名（如 `llm`）。
 *
 * 为什么不用 `path.basename(REPO_ROOT)`：本脚本可能在 git worktree 里运行
 * （父代理为每条 lane 建了 worktree），此时 `REPO_ROOT` 的 basename 是 worktree
 * 目录名（如 `r12`），不等于作者写文档时用的仓库目录名。
 *
 * 文档里写绝对路径（`/root/llm/docker-compose.yml`）时，前缀必然是**主 checkout**
 * 的绝对路径。git 的 common dir 指向主 checkout 的 `.git`，其父目录正是该路径。
 */
function canonicalRepoPath() {
  try {
    const commonDir = execFileSync('git', ['rev-parse', '--git-common-dir'], {
      cwd: REPO_ROOT, encoding: 'utf8', stdio: ['ignore', 'pipe', 'ignore'],
    }).trim()
    return path.resolve(REPO_ROOT, path.dirname(path.resolve(REPO_ROOT, commonDir)))
  } catch {
    // 非 git 环境（或 git 不可用）时退回当前目录：宁可少报也不要报错。
    return REPO_ROOT
  }
}

const CANONICAL_REPO_PATH = canonicalRepoPath()

/** 收集需要检查的 markdown 文件：README.md + docs/ 下全部 .md。 */
function collectMarkdownFiles() {
  const files = []
  const readme = path.join(REPO_ROOT, 'README.md')
  if (existsSync(readme)) files.push(readme)

  const walk = (dir) => {
    for (const entry of readdirSync(dir)) {
      const full = path.join(dir, entry)
      if (statSync(full).isDirectory()) walk(full)
      else if (entry.endsWith('.md')) files.push(full)
    }
  }
  const docsDir = path.join(REPO_ROOT, 'docs')
  if (existsSync(docsDir)) walk(docsDir)
  return files
}

/** GitHub 风格的标题锚点。 */
function slugify(heading) {
  return heading
    .trim()
    .toLowerCase()
    // 去掉行内代码标记
    .replace(/`/g, '')
    // GitHub 会移除的标点集合（含中文全角括号、顿号、句号等常见 CJK 标点）
    .replace(/[!"#$%&'()*+,./:;<=>?@[\]^`{|}~·—－–]/g, '')
    .replace(/[（）【】《》、，。：；？！“”‘’]/g, '')
    .trim()
    .replace(/\s+/g, '-')
}

/** 提取文档里的一级到六级标题。 */
function extractHeadings(markdown) {
  const headings = []
  for (const line of markdown.split('\n')) {
    const match = /^(#{1,6})\s+(.*)$/.exec(line)
    if (match) headings.push(match[2])
  }
  return headings
}

const problems = []
const relativeChecked = []
const anchorChecked = []
const codeRefChecked = []
const codeRefSkipped = []
const codeRefForward = []
const codeRefResolvedViaShorthand = []
const codeRefResolvedViaBasename = []
const codeRefResolvedViaNumbered = []
const codeRefResolvedViaAbsoluteTail = []
const codeRefOutOfRepo = []
const codeRefGenerated = []

function checkRelativeTarget(fromFile, rawTarget) {
  // 去掉可能的行内锚点与标题后缀
  const target = rawTarget.split('#')[0].trim()
  if (!target) return
  // 外链、邮件、纯锚点跳过
  if (/^(https?:|mailto:|tel:|data:)/i.test(target)) return
  const resolved = path.resolve(path.dirname(fromFile), target)
  const rel = path.relative(REPO_ROOT, resolved)
  relativeChecked.push(rel)
  if (!existsSync(resolved)) {
    problems.push(
      `${path.relative(REPO_ROOT, fromFile)}: 相对链接指向不存在的路径 -> ${target}`,
    )
  }
}

/**
 * 形如 docs/a/b.md、apps/api/main.go、sql/migrations/0016_x.sql 的代码引用。
 */
const CODE_REF_PATTERN = /`([A-Za-z0-9_./-]+\.(?:md|go|ts|tsx|sql|json|svg|mjs|js|yml|yaml|sh|py|css))`/g

/**
 * 相对路径的解析基准。
 *
 * 仓库里的文档习惯写简写（`lib/api.ts`、`src/views/EvaluationView.tsx`），
 * 而不是完整路径 `apps/web-user/src/lib/api.ts`。既有文档
 * （docs/architecture/phase-8-eval-and-cleaning.md）就是这么写的，
 * 因此检查器按多基准依次尝试，命中任一即视为可解析，同时汇报实际命中的完整路径。
 *
 * 顺序有意义：先试更具体的基准，避免同名文件误命中。
 */
const RESOLVE_ROOTS = [
  '', // 仓库根（完整路径写法的基准）
  'apps/web-user',
  'apps/web-user/src',
  'apps/api',
  'apps/worker',
  'internal',
  'docs',
  'sql/migrations',
  'test',
]

/**
 * 契约冻结但尚未落地的前向引用（显式白名单）。
 *
 * 这些名字由冻结契约 docs/plans/issue-remediation-plan.md 规定：
 *   - 迁移 0020 的文件名（§1.2，由 R4 创建）；
 *   - test/l15_*.py|mjs 七个测试文件名（§6.1，由 R1/R2/R4/R5/R8/R10/R11 各自创建）；
 *   - docs/plans/round2-lane-board.md（父代理维护的看板，在本 lane 基线之前单独开 PR）。
 *
 * 契约 §6.1 明确要求「文件由各自 lane 创建，父代理不预建空文件」，
 * 因此这些文件在本 lane 的基线上**不存在是预期状态**。
 *
 * 处理方式刻意不是静默跳过：命中的白名单条目会在摘要里单独列出，
 * 方便审查者确认「未解析的引用全都是契约前向引用，没有意外的漂移」。
 * 一旦这些文件被创建，它们会自动转为正常校验（白名单只在解析失败时生效）。
 */
const CONTRACT_FORWARD_REFS = new Set([
  'sql/migrations/0020_generation_runs_active_unique.sql',
  'docs/plans/round2-lane-board.md',
  'test/l15_stage_routes.mjs',
  'test/l15_worker_batch_status.py',
  'test/l15_worker_concurrency.py',
  'test/l15_route_contract.py',
  'test/l15_silent_guards.mjs',
  'test/l15_capability_entries.mjs',
  'test/l15_issue_triage.py',
  // R7 自选的测试文件名（契约 §2 只要求「Go 测试 + test/」，未冻结具体名字）。
  // 已核对 origin/lane/r7-dataset-provider-exists 上真实存在（+222 行）。
  'test/l15_dataset_provider.py',
  // 多 LLM 互评需求验证 lane 的测试文件名（同样未在契约 §6.1 冻结）。
  // 已核对 origin/lane/eval-multi-judge 上真实存在，PR #81 尚在评审中。
  'test/l15_eval_multi_judge.py',

  // ---- Atelier 主线（Issue #160，T01）----
  // docs/plans/atelier-implementation.md §1 与 §6.3、docs/plans/atelier-api-contract.md §6
  // 冻结了「落点」文件名，并要求「文件由各任务创建，T01 不预建空文件」。
  // 同理命中的条目会在摘要里单独列出；文件一旦创建即自动转为正常校验。
  'apps/api/routes_projects.go',
  'apps/api/routes_recipes.go',
  'apps/api/routes_studio_activity.go',
  'apps/worker/studio_jobs.go',
  'apps/worker/job_studio_eval.go',
  'apps/web-user/src/lib/api/studio.ts',
  'internal/model/project.go',
  'internal/store/project_store.go',
  'internal/store/job_store.go',
  'internal/eval/grpo_adapter.go',
  // 迁移编号分配表（§7.1），由 T02–T30 各自创建。
  'sql/migrations/0022_studio_workspaces_projects.sql',
  'sql/migrations/0023_studio_authz_audit.sql',
  'sql/migrations/0024_studio_versioned_docs.sql',
  'sql/migrations/0025_studio_batches_samples.sql',
  'sql/migrations/0026_studio_jobs_outbox.sql',
  'sql/migrations/0027_studio_usage_budget.sql',
  'sql/migrations/0028_studio_experiments.sql',
  'sql/migrations/0029_studio_rules_evidence.sql',
  'sql/migrations/0030_studio_review.sql',
  'sql/migrations/0031_studio_releases.sql',
  'sql/migrations/0032_studio_recipes.sql',
  'sql/migrations/0033_studio_activity_comments.sql',
  'sql/migrations/0034_studio_legacy_imports.sql',
])

/**
 * 后缀模式写法（例如 `internal/store/*_store.go` 简写成 `_store.go`）。
 * 这类引用描述的是「一组按命名约定匹配的文件」，不是单个路径，跳过。
 */
const isSuffixPattern = (ref) => ref.startsWith('_') || ref.includes('*')

/**
 * 形似路径但不是「本仓库真实文件引用」的写法，显式跳过。
 *
 * 与 CONTRACT_FORWARD_REFS 的区别：白名单是「契约冻结、尚未创建」的**前向引用**
 * （会被单独列出让人审查）；这里是**从来就不打算指向仓库内具体文件**的写法，
 * 列入后静默跳过。写入这里必须有理由，不能拿来掩盖真实的路径漂移。
 */
const CODE_REF_IGNORE = [
  // 用户/字典里的示例文件名，不是仓库文件（例如 *.env 说明、导出示例）
  /^\/etc\//,
  // 语义化版本或纯数字文件名
  /^v\d+\.\d+/,
  // 形如 `phase-1-foundation.md ~ phase-8-eval-and-cleaning.md` 的区间写法会先被
  // CODE_REF_PATTERN 切成两段，这里按「文档标题里的省略写法」跳过。
  /^\.\.\./,
]

/**
 * 仓库外的编排产物（**有意不入库**），无法用仓库相对路径校验。
 *
 * 父代理的编排脚本放在 /root/pi-waves/ 而不是仓库里（它是一次性的编排参数，
 * 不是产品交付物）。docs/plans/round2-lane-board.md 引用它时用的是裸文件名。
 * 列入此处会在摘要里单独列出，便于审查者确认「未校验的仓库外引用只有这些」。
 */
const OUT_OF_REPO_REFS = new Set([
  'wave1.js', // /root/pi-waves/wave1.js（父代理的 wave1 orchestration script）
])

/**
 * **生成物**（有意不入库）的仓库相对路径 → 生成它的脚本。
 *
 * 为什么单独处理而不是加进 CODE_REF_IGNORE 静默跳过：
 * 静默跳过会让「引用了一个根本不存在的采集产物」永远不被发现。
 * 这里的做法是：产物本身不必存在（因为它由脚本生成、不入库），
 * 但**生成它的脚本必须存在** —— 这样引用仍然可校验，
 * 而且若将来脚本被删/改名，检查会立即失败。
 *
 * 提交这类产物到仓库是刻意避免的：它们是浏览器采集的快照
 * （体积大、内容随页面实现变动、diff 噪声高），入库没有收益。
 */
const GENERATED_REFS = new Map([
  ['test/artifacts/page-structure/page-structure.json', 'test/l15_page_structure_capture.mjs'],
  ['test/artifacts/page-structure/hub-and-form.json', 'test/l15_hub_and_form_capture.mjs'],
])

/**
 * 仓库内全部文件的 basename 索引（只建一次）。
 *
 * 既有文档大量用裸文件名引用源码（`scanner.go`、`exporter.go`、`api.ts`），
 * 这是仓库既有的写作惯例（见 docs/architecture/phase-8-eval-and-cleaning.md），
 * 因此裸名能唯一（或至少能）对应到真实文件就算可解析；
 * 同名多份时也通过（说明引用不算错），但记录候选数供审查者判断。
 */
const basenameIndex = new Map()
function buildBasenameIndex() {
  const skipDirs = new Set(['node_modules', '.git', 'dist', '.pi'])
  const walk = (dir) => {
    for (const entry of readdirSync(dir, { withFileTypes: true })) {
      if (skipDirs.has(entry.name)) continue
      const full = path.join(dir, entry.name)
      if (entry.isDirectory()) {
        walk(full)
        continue
      }
      if (!basenameIndex.has(entry.name)) basenameIndex.set(entry.name, [])
      basenameIndex.get(entry.name).push(path.relative(REPO_ROOT, full))
    }
  }
  walk(REPO_ROOT)
}

/**
 * 按多基准解析一个相对路径。
 * 返回 { path, how } 或 null；how 取值：exact / shorthand / basename / numbered。
 */
/** 判断一个绝对路径是否落在当前仓库内。 */
function isInsideRepo(ref) {
  const rel = path.relative(REPO_ROOT, ref)
  return rel !== '' && !rel.startsWith('..') && !path.isAbsolute(rel)
}

function resolveRef(ref) {
  // 绝对路径：先判断是否落在仓库内。
  //
  // 文档里会写运行环境中的绝对路径（例如 `docker inspect` 输出的
  // `/root/llm/docker-compose.yml`），也可能写仓库外的编排产物
  // （`/root/pi-waves/wave1.js`）。前者能用仓库相对路径校验，后者不能
  // （CI 里根本没有 /root/pi-waves）。
  if (path.isAbsolute(ref)) {
    if (isInsideRepo(ref)) {
      return existsSync(ref) ? { path: path.relative(REPO_ROOT, ref), how: 'exact' } : null
    }
    // 仓库根的绝对路径写法：文档引用运行环境时会把仓库根写成绝对形式
    //（例如 board 里 `docker inspect` 的输出 `/root/llm/docker-compose.yml`）。
    // 逐级剥掉前缀，把尾巴当仓库相对路径再解析 —— 这样仍能捕获
    // 「被绝对路径引用的仓库文件被改名/删除」的真实漂移，而不是一律放行。
    // 注意：这里命中说明「仓库里存在同名尾巴」，不能因为 /root/llm 不等于
    // 当前 worktree 就判定失败 —— 但也不能跳过校验（删掉文件就应报错）。
    let tail = ref.replace(/^\/+/, '')
    while (tail.includes('/')) {
      tail = tail.slice(tail.indexOf('/') + 1)
      const resolved = resolveRef(tail)
      if (resolved) return { path: resolved.path, how: 'absolute-tail' }
    }
    // 确实在仓库外且在仓库里找不到同名尾巴（如 /root/pi-waves/wave1.js）：
    // 不谎报可解析，交上层归类为「仓库外引用」。
    return null
  }

  for (const root of RESOLVE_ROOTS) {
    const candidate = root ? path.join(REPO_ROOT, root, ref) : path.join(REPO_ROOT, ref)
    if (existsSync(candidate)) {
      return { path: path.relative(REPO_ROOT, candidate), how: root ? 'shorthand' : 'exact' }
    }
  }
  // 裸文件名 / 后缀段：按 basename 索引回查
  const bare = path.basename(ref)
  if (basenameIndex.has(bare)) {
    const matches = basenameIndex.get(bare)
    // 带目录前缀的写法要真能对上目录尾段，避免 `a/b.go` 误中 `c/b.go`
    const refDir = path.dirname(ref)
    if (refDir === '.') return { path: matches[0], how: 'basename' }
    const dirMatches = matches.filter((m) => path.dirname(m).endsWith(refDir))
    if (dirMatches.length > 0) return { path: dirMatches[0], how: 'basename' }
  }

  // 编号前缀写法的解析。
  //
  // 仓库的迁移文件命名是 `<NNNN>_<name>.sql`，而文档（包括已冻结的
  // docs/plans/eval-and-cleaning-plan.md 的编号归属表）习惯把编号与文件名分列引用：
  // 表格里写 `0011` 与 `eval_core.sql` 两列，指向同一个文件 0011_eval_core.sql。
  // 这是既有写作惯例，不是路径漂移，因此按「带编号前缀的 basename 唯一匹配」解析。
  // 只在唯一命中时接受：多份同名变体（0001_x.sql 与 0002_x.sql）会被判为歧义而不解析，
  // 避免用一个看似合理的猜测掩盖真实的引用错误。
  const numberedCandidates = []
  for (const [name, paths] of basenameIndex) {
    if (/^\d{4}_/.test(name) && name.endsWith('_' + bare)) {
      numberedCandidates.push(...paths)
    }
  }
  if (numberedCandidates.length === 1) {
    return { path: numberedCandidates[0], how: 'numbered' }
  }

  return null
}

function checkCodeRefs(fromFile, markdown) {
  for (const match of markdown.matchAll(CODE_REF_PATTERN)) {
    const ref = match[1]
    if (CODE_REF_IGNORE.some((pattern) => pattern.test(ref))) {
      codeRefSkipped.push(ref)
      continue
    }
    if (isSuffixPattern(ref)) {
      codeRefSkipped.push(ref)
      continue
    }

    // 仓库外的编排产物（有意不入库）：单独列出供审查，不算失败也不算通过。
    if (OUT_OF_REPO_REFS.has(ref)) {
      codeRefOutOfRepo.push(ref)
      continue
    }

    // 生成物（有意不入库）：产物不要求存在，但生成它的脚本必须存在。
    const generator = GENERATED_REFS.get(ref)
    if (generator) {
      if (existsSync(path.join(REPO_ROOT, generator))) {
        codeRefGenerated.push(`${ref} <- ${generator}`)
      } else {
        problems.push(
          `${fromFile}: 生成物引用 \`${ref}\` 声称由 \`${generator}\` 生成，但该脚本不存在`,
        )
      }
      continue
    }

    const resolved = resolveRef(ref)
    if (resolved) {
      codeRefChecked.push(ref)
      if (resolved.how === 'shorthand') {
        codeRefResolvedViaShorthand.push(`${ref} -> ${resolved.path}`)
      } else if (resolved.how === 'basename') {
        codeRefResolvedViaBasename.push(`${ref} -> ${resolved.path}`)
      } else if (resolved.how === 'numbered') {
        codeRefResolvedViaNumbered.push(`${ref} -> ${resolved.path}`)
      } else if (resolved.how === 'absolute-tail') {
        codeRefResolvedViaAbsoluteTail.push(`${ref} -> ${resolved.path}`)
      }
      continue
    }

    // 未解析的绝对路径：区分「指向仓库根、但文件不存在」与「确实在仓库外」。
    //
    // 关键区分依据：主 checkout 的绝对路径（CANONICAL_REPO_PATH，如 `/root/llm`）。
    //   - 以它开头  → 作者本意是指向本仓库，解析失败就是**真实漂移**
    //     （文件被改名/删除），必须报错；
    //   - 不以它开头 → 确实在仓库外（如 `/root/pi-waves/...`），
    //     归为「仓库外引用」列出供审查，不阻断。
    // 这样 CI 里的 `/root/llm/...` 仍然有效，同时不会把任意绝对路径放行。
    if (path.isAbsolute(ref)) {
      if (ref === CANONICAL_REPO_PATH || ref.startsWith(CANONICAL_REPO_PATH + '/')) {
        problems.push(
          `${path.relative(REPO_ROOT, fromFile)}: 代码引用路径不存在 -> \`${ref}\`（指向本仓库的绝对路径，文件已改名或删除？）`,
        )
        continue
      }
      codeRefOutOfRepo.push(ref)
      continue
    }

    // 未解析：只在它是契约冻结的前向引用时放过，并在摘要里单独列出。
    if (CONTRACT_FORWARD_REFS.has(ref)) {
      codeRefForward.push(ref)
      continue
    }

    problems.push(
      `${path.relative(REPO_ROOT, fromFile)}: 代码引用路径不存在 -> \`${ref}\``,
    )
  }
}

const files = collectMarkdownFiles()
buildBasenameIndex()

for (const file of files) {
  const markdown = readFileSync(file, 'utf8')
  const slugSet = new Set(extractHeadings(markdown).map(slugify))

  // 1. 相对链接：[文字](目标)
  for (const match of markdown.matchAll(/\]\(([^)\s]+)\)/g)) {
    checkRelativeTarget(file, match[1])
  }
  // 1b. 图片：<img src="目标">
  for (const match of markdown.matchAll(/<img[^>]*\ssrc="([^"]+)"/g)) {
    checkRelativeTarget(file, match[1])
  }

  // 2. 同文档锚点：](#anchor)
  for (const match of markdown.matchAll(/\]\(#([^)]+)\)/g)) {
    const anchor = match[1]
    anchorChecked.push(`${path.relative(REPO_ROOT, file)}#${anchor}`)
    if (!slugSet.has(anchor)) {
      problems.push(
        `${path.relative(REPO_ROOT, file)}: 同文档锚点无对应标题 -> #${anchor}`,
      )
    }
  }

  // 3. 反引号里的代码路径引用
  checkCodeRefs(file, markdown)
}

console.log(`检查文件数：${files.length}`)
console.log(`  相对链接：${relativeChecked.length} 条`)
console.log(`  同文档锚点：${anchorChecked.length} 条`)
console.log(`  代码路径引用：${codeRefChecked.length} 条`)
console.log(`    · 其中简写路径按基准解析命中：${new Set(codeRefResolvedViaShorthand).size} 条`)
console.log(`    · 其中裸文件名按 basename 回查命中：${new Set(codeRefResolvedViaBasename).size} 条`)
console.log(`    · 其中编号前缀写法命中：${new Set(codeRefResolvedViaNumbered).size} 条`)
console.log(`    · 其中绝对路径尾巴命中：${new Set(codeRefResolvedViaAbsoluteTail).size} 条`)
console.log(`    · 跳过非仓库路径写法 / 后缀模式：${codeRefSkipped.length} 条`)
console.log(`    · 契约冻结的前向引用（文件尚未创建，符合契约 §6.1）：${[...new Set(codeRefForward)].length} 条`)
for (const ref of [...new Set(codeRefForward)].sort()) {
  console.log(`        ~ ${ref}`)
}
console.log(`    · 仓库外引用（有意不入库的运行环境路径，不参与校验）：${[...new Set(codeRefOutOfRepo)].length} 条`)
if (codeRefGenerated.length > 0) {
  console.log(`    · 生成物引用（不入库，但已校验生成它的脚本存在）：${[...new Set(codeRefGenerated)].length} 条`)
  for (const item of [...new Set(codeRefGenerated)]) {
    console.log(`        ~ ${item}`)
  }
}
for (const ref of [...new Set(codeRefOutOfRepo)].sort()) {
  console.log(`        ~ ${ref}`)
}

if (problems.length > 0) {
  console.log(`\n发现 ${problems.length} 个问题：`)
  for (const problem of problems) console.log(`  ✗ ${problem}`)
  process.exit(1)
}

console.log('\n全部通过：相对链接可解析、同文档锚点有对应标题、代码引用路径真实存在。')
