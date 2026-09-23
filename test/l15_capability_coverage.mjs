/**
 * Atelier legacy capability coverage guard.
 *
 * Keeps the migration directory honest: each old surface must be classified as
 * native, historical read-only, legacy tool, or an explicit un-migrated gap.
 */

import { readFileSync } from 'node:fs'
import path from 'node:path'
import { fileURLToPath } from 'node:url'

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..')
const pagePath = path.join(root, 'apps', 'web-user', 'src', 'studio', 'pages', 'LegacyCapabilityWorkbench.tsx')
const stylePath = path.join(root, 'apps', 'web-user', 'src', 'styles.css')
const historyPath = path.join(root, 'apps', 'web-user', 'src', 'studio', 'pages', 'LegacyHistoryPage.tsx')
const helpPath = path.join(root, 'apps', 'web-user', 'src', 'studio', 'pages', 'SettingsPages.tsx')
const adminPath = path.join(root, 'apps', 'web-user', 'src', 'studio', 'pages', 'AdminWorkspacePage.tsx')
const projectsPath = path.join(root, 'apps', 'web-user', 'src', 'studio', 'pages', 'ProjectsPages.tsx')
const page = readFileSync(pagePath, 'utf8')
const styles = readFileSync(stylePath, 'utf8')
const history = readFileSync(historyPath, 'utf8')
const help = readFileSync(helpPath, 'utf8')
const admin = readFileSync(adminPath, 'utf8')
const projects = readFileSync(projectsPath, 'utf8')

const failures = []
function record(name, ok, detail) {
  console.log(`[${ok ? 'PASS' : 'FAIL'}] ${name}: ${detail}`)
  if (!ok) failures.push(name)
}

record(
  '覆盖矩阵显式区分四种迁移状态',
  page.includes("type CoverageStatus = 'native' | 'legacy-readonly' | 'legacy-tool' | 'not-migrated'") &&
    page.includes("statusLabel: 'Atelier 原生'") && page.includes("statusLabel: '旧数据只读'") &&
    page.includes("statusLabel: '旧工具兼容'") && page.includes("statusLabel: '未等价迁移'"),
  'Atelier 原生、历史只读、旧工具兼容与未迁移缺口有不同标识',
)

for (const capability of [
  '旧主题图谱生成、重命名、逐项审核与确认',
  '方向生成、恢复与按域编辑',
  '旧长链标准逐域编辑',
  '旧问题难度统计',
  '旧 reasoning 与 SFT 记录',
  '旧 reward 评分记录',
  '旧 GRPO prompt 与生成记录',
  '旧 dataset 导出制品与下载',
  '旧多模型评估、维度与裁判报告',
  '旧关键词配置、清洗扫描与命中报告',
]) {
  record(`覆盖矩阵列出旧能力：${capability}`, page.includes(capability), '矩阵必须解释旧能力状态，不能只展示新入口')
}

record(
  '矩阵说明 T31 实际导入边界',
  page.includes('T31 只把旧问题和最新 SFT reasoning/answer') &&
    page.includes('独立 reasoning 记录和其余 SFT 版本不转成样本版本') &&
    page.includes('无内容的问题会跳过') && page.includes('旧非 SFT 记录'),
  '未把旧记录笼统宣称为已迁移',
)

record(
  '旧写冻结状态不会被 UI 假设为始终开启',
  page.includes('LEGACY_WRITES_FROZEN 配置控制'),
  '页面明确以服务端冻结配置为准',
)

record(
  '旧 GRPO 教师提示词生成有显式兼容入口',
  page.includes("legacyOperation?: 'structure' | 'task' | 'questions' | 'reasoning' | 'rewards' | 'grpo' | 'exports'") &&
    page.includes("legacyOperation: 'grpo'") && page.includes("case 'grpo': return `/console/tasks/${legacyId}/legacy`") &&
    page.includes("row.legacyOperation === 'grpo' ? '旧 GRPO 操作'"),
  '旧提示词写操作从 GRPO 迁移说明显式进入兼容工作台并受服务端冻结策略控制',
)

record(
  '历史资产深链使用旧数据集 ID',
  page.includes('`/legacy/history/${legacyId}`') && page.includes('result.legacyDatasetId'),
  '不把 projectId 误当 datasetId，也不把历史详情落到其他数据集',
)

record(
  '未等价迁移的旧操作可显式到达且保留数据集上下文',
  page.includes("legacyOperation?: 'structure' | 'task' | 'questions' | 'reasoning' | 'rewards' | 'grpo' | 'exports'") &&
    page.includes('const legacyOperationPath =') &&
    page.includes('`/console/tasks/${legacyId}/legacy`') &&
    page.includes('`/console/questions/legacy${query}`') &&
    page.includes("if (!legacyId) return '/console/tasks'") &&
    page.includes('LEGACY_WRITES_FROZEN 配置控制') &&
    page.includes('Modal.confirm({') && page.includes("okText: '打开兼容操作'") &&
    history.includes('openLegacyOperations') && history.includes('Modal.confirm({') && history.includes('onDatasetChange?.(next)'),
  '矩阵区分只读历史与显式兼容工作台；离开只读页面时二次确认，缺 dataset 映射时要求用户选择任务',
)

record(
  '帮助页含恢复清单和高风险操作提示',
  help.includes('data-help-recovery="true"') && help.includes('data-help-risk="true"') &&
    help.includes('服务端 LEGACY_WRITES_FROZEN'),
  'Atelier 帮助保留故障恢复与高风险操作信息',
)
record(
  '管理员运行监控包含最近旧任务与审计记录',
  admin.includes('最近任务活动') && admin.includes('最近操作') &&
    admin.includes('consoleApi.listDatasets()') && admin.includes("navigate(`/console/tasks/${dataset.id}`)"),
  '保留旧运营页的近期活动与任务下钻，不只展示累计统计',
)
record(
  '指定项目深链不依赖当前列表页',
    projects.includes('studioApi.getProject(requestedProjectId)') &&
    projects.includes('const target = projectHref(projectTarget, response.id)') &&
    projects.includes('context.delete(\'next\')') && projects.includes('context.delete(\'projectId\')') &&
    projects.includes('navigate(query ? `${target}?${query}` : target)') &&
    projects.includes('autoOpenedTarget.current === requestedContext') &&
    projects.includes('if (autoOpenedTarget.current === requestedContext) autoOpenedTarget.current = \'\'') &&
    projects.includes('data-project-target-error="true"') && projects.includes('无法打开指定项目'),
  '项目深链按 ID 直查，跳转保留旧数据集 query，并在 StrictMode effect cleanup 后可重试',
)

record(
  '团队页只展示当前角色可执行的成员操作',
  help.includes('data-team-workspace-readonly="true"') &&
    help.includes('canManageMembers !== true') &&
    help.includes('data-team-project-upsert="true"') &&
    help.includes('projectMemberReason.trim()') &&
    help.includes('removeProjectMember(projectID, userID, projectMemberReason.trim())'),
  'workspace admin 與 project owner 能力分開，變更與移除要求審計原因',
)

record(
  '兼容索引没有重新启用旧生成写 API',
  !/consoleApi\.(generateDomains|confirmDomains|generateDirections|resumeStage|generateChainStandards|updateChainStandard|generateQuestions|generateQuestionsV2|generateReasoning|generateRewards|generateGrpo|generateSft|generateExport|exportDataset)\s*\(/.test(page),
  '矩阵只展示状态和安全目标，不调用旧写 API',
)

record(
  '矩阵是可访问且窄屏可读的表格布局',
  page.includes('role="table"') && styles.includes('.legacy-capability-matrix__row') &&
    styles.includes('@media (max-width: 960px) {\n  .legacy-capability-workbench'),
  '表头/单元有语义角色，窄屏布局切换为可扫描的字段分组',
)

if (failures.length > 0) {
  console.error(`\n能力覆盖守卫失败：${failures.length} 项`)
  process.exitCode = 1
} else {
  console.log('\n能力覆盖矩阵守卫通过')
}
