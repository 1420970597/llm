/**
 * Task-detail stage links must preserve the selected legacy task.
 *
 * The explicit legacy detail page is rendered from `/console/tasks/:taskId/legacy`;
 * a bare stage pathname loses that ID on refresh and shows an unrelated workspace.
 * Keep this guard source-level and dependency-free so it runs with the other
 * l15 UI checks even when no API container is available.
 */

import { readFileSync } from 'node:fs'
import path from 'node:path'
import { fileURLToPath } from 'node:url'

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..')
const source = readFileSync(path.join(root, 'apps', 'web-user', 'src', 'App.tsx'), 'utf8')

const failures = []
function record(name, ok, detail) {
  console.log(`[${ok ? 'PASS' : 'FAIL'}] ${name}: ${detail}`)
  if (!ok) failures.push(name)
}

const marker = '<Title heading={5} className="!mb-0">质量与交付</Title>'
const start = source.indexOf(marker)
const end = source.indexOf('\n        </Card>', start)
const block = start >= 0 && end > start ? source.slice(start, end) : ''

record(
  '任务详情质量与交付面板可定位',
  block.length > 0,
  '找到质量与交付面板，避免断言落到其他页面的同名按钮',
)

for (const route of ['/console/questions', '/console/reasoning', '/console/rewards', '/console/exports']) {
  const expected = `stageRouteWithTask('${route}', activeDataset?.id)`
  record(
    `详情链接保留 taskId：${route}`,
    block.includes(expected),
    block.includes(expected) ? expected : `缺少 ${expected}`,
  )
}

record(
  '详情面板没有裸阶段跳转',
  !/navigate\(['"]\/console\/(questions|reasoning|rewards|exports)['"]\)/.test(block),
  '所有阶段按钮都经过 stageRouteWithTask',
)

record(
  '旧版阶段操作显式进入兼容子路由',
  source.includes("const legacyRoute = `${route.replace(/\\/$/, '')}/legacy`") &&
    source.includes('`${legacyRoute}${legacyRoute.includes(\'?\') ? \'&\' : \'?\'}taskId=${datasetId}`') &&
    source.includes('path="/console/questions/legacy" element={renderQuestionStage()}'),
  '默认阶段路由保留给 T31 映射/历史桥接；具体旧操作写入兼容路径和旧 dataset ID',
)

// Mutation self-check: replacing one helper call with a bare path must fail.
const mutated = block.replace(
  "stageRouteWithTask('/console/questions', activeDataset?.id)",
  "'/console/questions'",
)
record(
  '变异自证：丢 taskId 会被捕获',
  /navigate\(['"]\/console\/questions['"]\)/.test(mutated),
  '守卫源码包含上下文契约',
)

if (failures.length > 0) {
  console.error(`\n详情页上下文守卫失败：${failures.length} 项`)
  process.exitCode = 1
} else {
  console.log('\n详情页上下文守卫通过。')
}
