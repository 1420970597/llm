/**
 * Visible-copy guard for Markdown markers.
 *
 * React renders plain JSX text literally. A pair of `**` in a string is not
 * emphasis, so user-facing copy must use semantic elements such as `<strong>`
 * instead of leaking Markdown source into the interface.
 */

import { readFileSync } from 'node:fs'
import path from 'node:path'
import { fileURLToPath } from 'node:url'

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..')
const files = [
  'apps/web-user/src/studio/pages/ComparePage.tsx',
  'apps/web-user/src/studio/pages/NewProjectWizard.tsx',
  'apps/web-user/src/studio/pages/QualityPages.tsx',
  'apps/web-user/src/studio/pages/RecipesPages.tsx',
  'apps/web-user/src/studio/pages/ReviewPages.tsx',
  'apps/web-user/src/studio/pages/RunPages.tsx',
].map((file) => path.join(root, file))
const source = files.map((file) => readFileSync(file, 'utf8')).join('\n')

const failures = []
function record(name, ok, detail) {
  console.log(`[${ok ? 'PASS' : 'FAIL'}] ${name}: ${detail}`)
  if (!ok) failures.push(name)
}

const forbiddenVisibleCopy = [
  '只恢复**可重试**',
  '比较的是**相同输入下**',
  '实验创建时**冻结**',
  '按审阅状态与关键词在**服务端**',
  '待同步，**未提交**',
  '，**计划量**，',
  '只有**已发布**',
  '复制的是**选定版本的内容快照**',
]
record(
  '可见文案不包含裸 Markdown 强调标记',
  forbiddenVisibleCopy.every((fragment) => !source.includes(fragment)),
  '所有已知用户文案均不再把 ** 当作普通文本输出',
)

const semanticEmphasis = [
  '<strong>相同输入下</strong>',
  '<strong>冻结</strong>',
  '<strong>服务端</strong>',
  '<strong>计划量</strong>',
  '<strong>已发布</strong>',
  '<strong>选定版本的内容快照</strong>',
]
record(
  '强调文案使用语义化元素',
  semanticEmphasis.every((fragment) => source.includes(fragment)),
  '强调词使用 strong，而不是 Markdown 源码',
)
record(
  '离线草稿提示保留提交状态语义',
  source.includes('已保存为本机草稿（待同步，未提交）：') && !source.includes('待同步，**未提交**'),
  '草稿提示仍明确说明未提交，不依赖 Markdown',
)

if (failures.length > 0) {
  console.error(`\nMarkdown 可见文案守卫失败：${failures.length} 项`)
  process.exitCode = 1
} else {
  console.log('\nMarkdown 可见文案守卫通过')
}
