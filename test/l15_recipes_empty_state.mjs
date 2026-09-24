/**
 * Recipe library empty-state guard for issue #172.
 *
 * An empty state must describe the capability that is actually available and
 * link to a real destination. It must never promise a nonexistent “save as
 * recipe” button.
 */

import { readFileSync } from 'node:fs'
import path from 'node:path'
import { fileURLToPath } from 'node:url'

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..')
const page = readFileSync(path.join(root, 'apps', 'web-user', 'src', 'studio', 'pages', 'RecipesPages.tsx'), 'utf8')

const failures = []
function record(name, ok, detail) {
  console.log(`[${ok ? 'PASS' : 'FAIL'}] ${name}: ${detail}`)
  if (!ok) failures.push(name)
}

record(
  '空状态不承诺不存在的保存为方案入口',
  !page.includes('保存为方案') && !page.includes('设计页 → 保存为方案'),
  '空状态不再指向不存在的按钮',
)
record(
  '空状态诚实说明另存为方案尚未开放',
  page.includes('从既有项目另存为方案尚未开放'),
  '未交付能力明确标注边界，不伪造可用状态',
)
record(
  '空状态提供真实的数据项目入口',
  page.includes('data-recipes-empty-cta="true"') && page.includes("onClick={() => navigate('/projects')}"),
  'CTA 直接进入已注册的数据项目列表',
)

if (failures.length > 0) {
  console.error(`\n方案库空状态守卫失败：${failures.length} 项`)
  process.exitCode = 1
} else {
  console.log('\n方案库空状态守卫通过')
}
