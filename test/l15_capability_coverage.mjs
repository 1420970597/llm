/**
 * Project workspace migration-surface guard.
 *
 * Issue #170 moves migration details out of the six project workspaces. This
 * guard prevents the internal capability matrix from being reintroduced as a
 * page import, JSX fragment, or stylesheet block.
 */

import { existsSync, readFileSync } from 'node:fs'
import path from 'node:path'
import { fileURLToPath } from 'node:url'

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..')
const stylePath = path.join(root, 'apps', 'web-user', 'src', 'styles.css')
const historyPath = path.join(root, 'apps', 'web-user', 'src', 'studio', 'pages', 'LegacyHistoryPage.tsx')
const workspaceFiles = [
  'ProjectsPages.tsx',
  'QualityPages.tsx',
  'ReleasePages.tsx',
  'ReviewPages.tsx',
  'RunPages.tsx',
  'BlueprintPages.tsx',
].map((name) => path.join(root, 'apps', 'web-user', 'src', 'studio', 'pages', name))

const styles = readFileSync(stylePath, 'utf8')
const history = readFileSync(historyPath, 'utf8')
const workspaceSource = workspaceFiles.map((file) => readFileSync(file, 'utf8')).join('\n')
const workbenchPath = path.join(root, 'apps', 'web-user', 'src', 'studio', 'pages', 'LegacyCapabilityWorkbench.tsx')

const failures = []
function record(name, ok, detail) {
  console.log(`[${ok ? 'PASS' : 'FAIL'}] ${name}: ${detail}`)
  if (!ok) failures.push(name)
}

record(
  '迁移矩阵组件已移除',
  !existsSync(workbenchPath),
  '不再保留可被误接入的死页面',
)
record(
  '六个项目工作区不引用迁移矩阵',
  !workspaceSource.includes('LegacyCapabilityWorkbench') && !workspaceSource.includes('能力覆盖与迁移边界'),
  '首屏只呈现当前工作区的核心操作',
)
record(
  '迁移矩阵样式已移除',
  !styles.includes('legacy-capability-workbench') && !styles.includes('legacy-capability-matrix'),
  '避免死 DOM 样式继续暗示矩阵属于产品流程',
)
record(
  '历史兼容信息仍从历史资产入口提供',
  history.includes('openLegacyOperations') && history.includes('LEGACY_WRITES_FROZEN 配置'),
  '旧数据操作保留在显式历史入口并受服务端策略控制',
)
record(
  '项目工作区没有重新启用旧生成写 API',
  !/consoleApi\.(generateDomains|confirmDomains|generateDirections|resumeStage|generateChainStandards|updateChainStandard|generateQuestions|generateQuestionsV2|generateReasoning|generateRewards|generateGrpo|generateSft|generateExport|exportDataset)\s*\(/.test(workspaceSource),
  '项目工作区不调用旧写 API',
)

if (failures.length > 0) {
  console.error(`\n项目工作区迁移矩阵守卫失败：${failures.length} 项`)
  process.exitCode = 1
} else {
  console.log('\n项目工作区迁移矩阵守卫通过')
}
