/**
 * 迁移冒烟契约守卫（issue #217 B1）。
 *
 * 运行：
 *   node test/l15_migration_smoke.mjs
 *
 * ---------------------------------------------------------------------------
 * 这条守卫防的是什么
 * ---------------------------------------------------------------------------
 * `AGENTS.md` §1 把 `make db-migrate-smoke` 描述为「启动临时 Postgres 验证
 * SQL 迁移脚本」（复数），而它此前**只应用 `0001_phase1_foundation.sql`**：
 * 实测走完该 target 后库里只有 3 张表，`samples` / `sample_versions` /
 * `batches` / `review_projections` / `legacy_imports` 全部不存在，
 * 0022–0039 一条都没被验证。也就是说这条「迁移验证」命令**永远绿灯**，
 * 却对绝大多数迁移零覆盖 —— 与「无证据当成已验证」是同一类失败。
 *
 * 这只守卫默认不启动任何容器（CI 可跑），做三层：
 *   第 1 层 源码级：Makefile target 必须委托给 scripts/db-migrate-smoke.sh，
 *           且脚本必须**遍历全部迁移**（glob/循环）而不是硬编码单一文件；
 *           脚本还必须断言核心表存在（否则「遍历」被删也没人发现）。
 *   第 2 层 迁移清单：断言仓库里迁移文件 > 1，且脚本要应用的是全量
 *           （否则「全部应用」在一份迁移的仓库里是空话）。
 *   第 3 层 变异自证：把脚本改回「只跑 0001」，断言第 1 层**真的**会失败。
 *
 * 用最小 shell 解析而不是引入 YAML/Make 解析库：这些文件是纯文本契约，
 * 过度工程反而让守卫更脆。
 */

import { readFileSync } from 'node:fs'
import { readdirSync } from 'node:fs'
import path from 'node:path'
import { fileURLToPath } from 'node:url'

const REPO_ROOT = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..')
const read = (relative) => readFileSync(path.join(REPO_ROOT, relative), 'utf8')

const MAKEFILE = read('Makefile')
const SMOKE_SCRIPT = read('scripts/db-migrate-smoke.sh')
const AGENTS = read('AGENTS.md')

const failures = []
function record(name, ok, detail) {
  console.log(`[${ok ? 'PASS' : 'FAIL'}] ${name}: ${detail}`)
  if (!ok) failures.push(name)
}

/** 抽出 `db-migrate-smoke:` 这条 target 的 recipe 文本（到下一个 target 为止）。 */
function makefileTarget(makefile, target) {
  const lines = makefile.split('\n')
  const start = lines.findIndex((line) => line === `${target}:` || line.startsWith(`${target}:`))
  if (start < 0) return null
  const body = []
  for (let i = start + 1; i < lines.length; i++) {
    const line = lines[i]
    // 空行或下一行不是 tab 缩进 => target 结束。
    if (line === '' || !line.startsWith('\t')) break
    body.push(line)
  }
  return body.join('\n')
}

/**
 * 谓词：迁移冒烟契约是否被满足。
 *
 * 抽成函数是为了让「变异体」也能喂进来 —— 只有证明断言**会响**，
 * 它才是守卫，而不是一段注释。
 */
function problemsWithMigrationSmoke(makefile, script) {
  const problems = []
  const target = makefileTarget(makefile, 'db-migrate-smoke')
  if (target === null) {
    problems.push('Makefile 缺少 db-migrate-smoke target')
    return problems
  }
  // 1) target 必须委托给脚本，不得内联「只跑一个迁移」的 psql。
  if (!/scripts\/db-migrate-smoke\.sh/.test(target)) {
    problems.push('db-migrate-smoke 未委托给 scripts/db-migrate-smoke.sh（可能退回内联单文件）')
  }
  if (/psql[^\n]*<[^\n]*sql\/migrations\/0001/.test(target)) {
    problems.push('db-migrate-smoke 内联了「只应用 0001」的 psql（#217 B1 的原始缺陷形态）')
  }
  // 2) 脚本必须遍历**全部**迁移。
  //    必须看 MIGRATIONS 赋值本身：文案里也出现过 `sql/migrations/*.sql`，
  //    只查「文本里有没有 glob」会被注释/提示语骗过（变异自证抓到了这一点）。
  if (!/MIGRATIONS=\(sql\/migrations\/\*\.sql\)/.test(script)) {
    problems.push('脚本的 MIGRATIONS 未按 sql/migrations/*.sql 取全量（无法覆盖新增迁移）')
  }
  if (/for file in sql\/migrations\/0001/.test(script)) {
    problems.push('脚本只遍历 0001（这正是要修掉的静默空转）')
  }
  if (!/psql[^\n]*-v ON_ERROR_STOP=1/.test(script)) {
    problems.push('应用迁移未用 ON_ERROR_STOP=1（坏迁移会被静默吞掉）')
  }
  // 3) 脚本必须断言核心表存在，否则「遍历」被删也没人发现。
  //    必须断言清单**非空**（`REQUIRED_TABLES=()` 也要被抓住）。
  if (!/REQUIRED_TABLES=\([a-z_]+/.test(script) || !/to_regclass\(/.test(script)) {
    problems.push('脚本没有断言核心表存在（无法发现「只跑 0001」的静默回归）')
  }
  // 4) 文档描述与实现必须一致：AGENTS.md 说的就是「验证 SQL 迁移脚本」。
  if (!/make db-migrate-smoke/.test(AGENTS)) {
    problems.push('AGENTS.md 不再记录 make db-migrate-smoke（契约与文档脱钩）')
  }
  return problems
}

const MIGRATIONS = readdirSync(path.join(REPO_ROOT, 'sql', 'migrations')).filter((name) => name.endsWith('.sql'))

record(
  '迁移冒烟契约（委托脚本 + 全量遍历 + 核心表断言 + 文档一致）',
  problemsWithMigrationSmoke(MAKEFILE, SMOKE_SCRIPT).length === 0,
  problemsWithMigrationSmoke(MAKEFILE, SMOKE_SCRIPT).join('；') || `结构断言通过（迁移文件 ${MIGRATIONS.length} 份）`,
)

record(
  '仓库迁移多于一份（否则「全部应用」无意义）',
  MIGRATIONS.length > 1,
  `sql/migrations 下 ${MIGRATIONS.length} 份迁移`,
)

// ---------------------------------------------------------------------------
// 变异自证：断言必须能捕获「退回只跑 0001」
// ---------------------------------------------------------------------------

// 变异 1：脚本退回只遍历 0001。
{
  const mutated = SMOKE_SCRIPT.replace(
    'MIGRATIONS=(sql/migrations/*.sql)',
    'MIGRATIONS=(sql/migrations/0001_phase1_foundation.sql)',
  )
  const problems = problemsWithMigrationSmoke(MAKEFILE, mutated)
  record(
    '变异：脚本退回只遍历 0001 -> 断言必须报错',
    problems.length > 0,
    problems.length > 0 ? `捕获到 ${problems.length} 个问题` : '断言空转（改坏了却仍然通过）',
  )
}

// 变异 2：删掉核心表断言（静默空转重新变得不可见）。
{
  const mutated = SMOKE_SCRIPT.replace(/REQUIRED_TABLES=\([^)]*\)/, 'REQUIRED_TABLES=()')
  const problems = problemsWithMigrationSmoke(MAKEFILE, mutated)
  record(
    '变异：删掉核心表断言 -> 断言必须报错',
    problems.length > 0,
    problems.length > 0 ? `捕获到 ${problems.length} 个问题` : '断言空转（改坏了却仍然通过）',
  )
}

// 变异 3：Makefile 退回内联只跑 0001。
{
  const mutated = MAKEFILE.replace(
    'db-migrate-smoke:\n\tbash scripts/db-migrate-smoke.sh',
    'db-migrate-smoke:\n\tdocker exec -i llm-postgres-migrate-smoke psql -U llm_factory -d llm_factory < sql/migrations/0001_phase1_foundation.sql',
  )
  const problems = problemsWithMigrationSmoke(mutated, SMOKE_SCRIPT)
  record(
    '变异：Makefile 退回内联只跑 0001 -> 断言必须报错',
    problems.length > 0,
    problems.length > 0 ? `捕获到 ${problems.length} 个问题` : '断言空转（改坏了却仍然通过）',
  )
}

if (failures.length > 0) {
  console.error(`\n迁移冒烟契约守卫失败：${failures.length} 项`)
  process.exitCode = 1
} else {
  console.log('\n迁移冒烟契约守卫通过（1 条结构断言 + 1 条清单断言 + 3 条变异自证）。')
}
