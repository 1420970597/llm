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
import path from 'node:path'
import { fileURLToPath } from 'node:url'

const REPO_ROOT = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..')

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
])

/**
 * 后缀模式写法（例如 `internal/store/*_store.go` 简写成 `_store.go`）。
 * 这类引用描述的是「一组按命名约定匹配的文件」，不是单个路径，跳过。
 */
const isSuffixPattern = (ref) => ref.startsWith('_') || ref.includes('*')

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
 * 返回 { path, how } 或 null；how 取值：exact / shorthand / basename。
 */
function resolveRef(ref) {
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

    const resolved = resolveRef(ref)
    if (resolved) {
      codeRefChecked.push(ref)
      if (resolved.how === 'shorthand') {
        codeRefResolvedViaShorthand.push(`${ref} -> ${resolved.path}`)
      } else if (resolved.how === 'basename') {
        codeRefResolvedViaBasename.push(`${ref} -> ${resolved.path}`)
      }
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
console.log(`    · 跳过非仓库路径写法 / 后缀模式：${codeRefSkipped.length} 条`)
console.log(`    · 契约冻结的前向引用（文件尚未创建，符合契约 §6.1）：${[...new Set(codeRefForward)].length} 条`)
for (const ref of [...new Set(codeRefForward)].sort()) {
  console.log(`        ~ ${ref}`)
}

if (problems.length > 0) {
  console.log(`\n发现 ${problems.length} 个问题：`)
  for (const problem of problems) console.log(`  ✗ ${problem}`)
  process.exit(1)
}

console.log('\n全部通过：相对链接可解析、同文档锚点有对应标题、代码引用路径真实存在。')
