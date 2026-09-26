/**
 * Visible-copy guard for Markdown markers (issue #171 → #192).
 *
 * React renders plain JSX text literally. A pair of `**` in a string is not
 * emphasis, so user-facing copy must use semantic elements such as `<strong>`
 * instead of leaking Markdown source into the interface.
 *
 * ---------------------------------------------------------------------------
 * 为什么从「6 个文件的清单」改成「扫描整棵源码树」（issue #192）
 * ---------------------------------------------------------------------------
 * 第一轮（#171 → PR #182）修掉了 4 处 `**`，并把守卫写成**手工维护的文件清单**。
 * 结果第二轮审计（#192）在同一个部署版本上发现 **5 个页面 6 处** —— 清单之外的
 * 页面照样漏。手工清单有一个不可修的结构性缺陷：**漏一个文件就是漏一处缺陷，
 * 而且不会有任何信号**。
 *
 * 因此这里改为：
 *   1. 递归扫描 `apps/web-user/src` 下全部 `.ts` / `.tsx`；
 *   2. 用一个真正的**词法扫描器**去掉注释（块注释里写 `**强调**` 是正常文档，
 *      不应误报），只检查**剩余代码**里的 `**`；
 *   3. 一旦出现即失败并打印 `文件:行号:内容`，因此可直接当作 CI 门禁。
 *
 * 扫描器必须自己写而不是用正则：`/* … *​/`、`//`、字符串、模板字符串与正则
 * 字面量里的内容语义完全不同，正则无法区分「注释里的 **」与「文案里的 **」。
 * 这正是一次误报就会让守卫被关掉的典型场合。
 */

import { readFileSync, readdirSync, statSync } from 'node:fs'
import path from 'node:path'
import { fileURLToPath } from 'node:url'

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..')
const SRC_ROOT = path.join(root, 'apps', 'web-user', 'src')

const failures = []
function record(name, ok, detail) {
  console.log(`[${ok ? 'PASS' : 'FAIL'}] ${name}: ${detail}`)
  if (!ok) failures.push(name)
}

// ---------------------------------------------------------------------------
// 词法扫描：返回「去掉注释后的代码」并保留行号
// ---------------------------------------------------------------------------

/**
 * stripComments 去掉整行注释、块注释与行尾注释，保留其它一切字符
 *（含字符串与模板字符串的内容），并用空格替换被删除的字符以保持行号。
 *
 * 为什么是「行级 + 块级」两步而不是一个通用 JS 词法器：写完通用词法器就要处理
 * 正则字面量、除法与模板插值，而一个错位的解释会把后面所有行都当成字符串，
 * 造成**假通过**（守卫报绿，缺陷还在）。这里用保守得多的规则：只去掉
 * 「确实看得出是注释」的三种形态，其余一律当作代码检查 —— 误报会被人看见
 * 并修掉，漏报不会。
 *
 * 行尾注释的 `//` 要求前面是空白，因此 `https://…` 不会被视为注释开端。
 */
function stripComments(source) {
  const withoutBlocks = source.replace(/\/\*[\s\S]*?\*\//g, (block) =>
    block.replace(/[^\n]/g, ' '),
  )
  return withoutBlocks
    .split('\n')
    .map((line) => {
      if (line.trimStart().startsWith('//')) {
        return ' '.repeat(line.length)
      }
      const comment = line.match(/^(.*?\S)\s+\/\/.*$/)
      if (comment) {
        return comment[1] + ' '.repeat(line.length - comment[1].length)
      }
      return line
    })
    .join('\n')
}

/** 递归收集源码文件。 */
function collectSources(directory) {
  const found = []
  for (const entry of readdirSync(directory).sort()) {
    const full = path.join(directory, entry)
    const stats = statSync(full)
    if (stats.isDirectory()) {
      found.push(...collectSources(full))
      continue
    }
    if (entry.endsWith('.ts') || entry.endsWith('.tsx')) {
      found.push(full)
    }
  }
  return found
}

const files = collectSources(SRC_ROOT)
record(
  '扫描范围覆盖全部前端源码（而不是手工文件清单）',
  files.length >= 40,
  `已扫描 ${files.length} 个 .ts/.tsx 文件`,
)

const offenders = []
for (const file of files) {
  const relative = path.relative(root, file)
  const code = stripComments(readFileSync(file, 'utf8'))
  code.split('\n').forEach((line, index) => {
    if (line.includes('**')) {
      offenders.push(`${relative}:${index + 1}: ${line.trim().slice(0, 160)}`)
    }
  })
}

record(
  '可见文案不包含裸 Markdown 强调标记',
  offenders.length === 0,
  offenders.length === 0
    ? '全部源码在去注释后不含 ** 字面量'
    : `仍有 ${offenders.length} 处：\n  ${offenders.join('\n  ')}`,
)

// ---------------------------------------------------------------------------
// 语义化强调必须仍然存在（防止「删掉文案」被当成通过）
// ---------------------------------------------------------------------------

const sources = files.map((file) => readFileSync(file, 'utf8')).join('\n')
const semanticEmphasis = [
  '<strong>相同输入下</strong>',
  '<strong>冻结</strong>',
  '<strong>服务端</strong>',
  '<strong>计划量</strong>',
]
record(
  '强调文案使用语义化元素',
  semanticEmphasis.every((fragment) => sources.includes(fragment)),
  '强调词使用 strong，而不是 Markdown 源码',
)

if (failures.length > 0) {
  console.error(`\nMarkdown 可见文案守卫失败：${failures.length} 项`)
  process.exitCode = 1
} else {
  console.log('\nMarkdown 可见文案守卫通过')
}
