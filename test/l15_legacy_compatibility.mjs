/**
 * Atelier legacy compatibility index guard.
 *
 * The old console remains a supported deep-link surface during migration.
 * The Atelier product shell must not expose a compatibility index as a menu or
 * product page; legacy links are validated separately against App's bridge.
 * It intentionally has no API/browser dependency and is safe to run in CI.
 */

import { readFileSync } from 'node:fs'
import path from 'node:path'
import { fileURLToPath } from 'node:url'

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..')
const app = readFileSync(path.join(root, 'apps', 'web-user', 'src', 'App.tsx'), 'utf8')
const routes = readFileSync(path.join(root, 'apps', 'web-user', 'src', 'studio', 'legacyCapabilities.ts'), 'utf8')
const studioRoutes = readFileSync(path.join(root, 'apps', 'web-user', 'src', 'studio', 'routes.ts'), 'utf8')
const studioTree = readFileSync(path.join(root, 'apps', 'web-user', 'src', 'studio', 'StudioRoutes.tsx'), 'utf8')
const cleaningView = readFileSync(path.join(root, 'apps', 'web-user', 'src', 'views', 'CleaningView.tsx'), 'utf8')
const toolPages = readFileSync(path.join(root, 'apps', 'web-user', 'src', 'studio', 'pages', 'LegacyToolPages.tsx'), 'utf8')
const projectsPage = readFileSync(path.join(root, 'apps', 'web-user', 'src', 'studio', 'pages', 'ProjectsPages.tsx'), 'utf8')
const adminPage = readFileSync(path.join(root, 'apps', 'web-user', 'src', 'studio', 'pages', 'AdminWorkspacePage.tsx'), 'utf8')
const bridge = readFileSync(path.join(root, 'apps', 'web-user', 'src', 'studio', 'LegacyRouteBridge.tsx'), 'utf8')
const historyPage = readFileSync(path.join(root, 'apps', 'web-user', 'src', 'studio', 'pages', 'LegacyHistoryPage.tsx'), 'utf8')

const failures = []
function record(name, ok, detail) {
  console.log(`[${ok ? 'PASS' : 'FAIL'}] ${name}: ${detail}`)
  if (!ok) failures.push(name)
}

const expected = [
  '/console/home',
  '/console/overview',
  '/console/planning',
  '/console/tasks',
  '/console/tasks/:taskId',
  '/console/results',
  '/console/evaluation',
  '/console/cleaning',
  '/console/help',
  '/console/domains',
  '/console/questions',
  '/console/reasoning',
  '/console/rewards',
  '/console/exports',
  '/console/operations',
  '/console/admin/providers',
  '/console/admin/storage',
  '/console/admin/strategies',
  '/console/admin/prompts',
  '/console/admin/audit',
]

for (const route of expected) {
  const indexed = new RegExp(`route:\\s*'${route.replaceAll('/', '\\/')}'[\\s\\S]*?status:\\s*'compat'`).test(routes)
  const mounted = route.includes(':taskId')
    ? /path=["']\/console\/tasks\/:taskId["']/.test(app)
    : new RegExp(`path=["']${route.replaceAll('/', '\\/')}["']`).test(app)
  record(`旧入口 ${route} 保留深链元数据`, indexed, indexed ? 'legacyCapabilities.ts 已声明 compat 状态' : 'legacyCapabilities.ts 缺少声明')
  record(`旧入口 ${route} 仍由 App 挂载`, mounted, mounted ? 'App.tsx 保留兼容路由' : 'App.tsx 未找到兼容路由')
}

const adminEntries = expected.filter((route) => route.includes('/admin/') || route.endsWith('/operations'))
for (const route of adminEntries) {
  const entryStart = routes.indexOf(`route: '${route}'`)
  const entry = entryStart >= 0 ? routes.slice(entryStart, routes.indexOf('\n', entryStart)) : ''
  record(`管理员入口 ${route} 标记 adminOnly`, /adminOnly:\s*true/.test(entry), '管理员入口不得向普通用户伪装成公共能力')
}

// 评估与清洗不是只保留旧壳链接：它们已经有 Atelier 原生辅助工作台，
// 但旧深链仍必须继续挂载。两条断言分别锁定「新入口可达」和「真实页面已注册」。
for (const [legacyRoute, nativePath, nativeKey] of [
  ['/console/evaluation', '/tools/evaluation', 'tools.evaluation'],
  ['/console/cleaning', '/tools/cleaning', 'tools.cleaning'],
]) {
  const legacyEntry = routes.slice(routes.indexOf(`route: '${legacyRoute}'`), routes.indexOf('\n', routes.indexOf(`route: '${legacyRoute}'`)))
  record(`旧入口 ${legacyRoute} 指向 Atelier 原生工作台`, legacyEntry.includes(`nativeHref: '${nativePath}'`), `兼容索引保留 ${nativePath}`)
  const routeDeclared = studioRoutes.includes(`key: '${nativeKey}'`) && studioRoutes.includes(`path: '${nativePath}'`)
  const pageRegistered = studioTree.includes(`'${nativeKey}': () =>`)
  record(`Atelier 工作台 ${nativePath} 已声明并注册`, routeDeclared && pageRegistered, `${nativeKey} 元数据与页面注册表均存在`)
}

record(
  '正式菜单移除兼容功能',
  !/key:\s*'settings\.capabilities'/.test(studioRoutes) && !/兼容功能/.test(studioRoutes),
  '辅助入口不再暴露迁移说明索引',
)
record(
  '兼容功能旧书签重定向到原生设置',
  studioTree.includes('<Route path="/settings/capabilities" element={<Navigate to="/settings/connections" replace />} />') &&
    !studioTree.includes("'settings.capabilities': () => <LegacyCapabilitiesPage />"),
  '保留可达性但不再渲染兼容产品页',
)
record(
  'Atelier 下钻旧阶段时保留任务上下文',
  studioTree.includes('const withProject = (next: string) => `/projects?next=') &&
    projectsPage.includes('const requestedProjectId = parseProjectResourceId(searchParams.get(\'projectId\'))') &&
    projectsPage.includes('studioApi.getProject(requestedProjectId)') &&
    projectsPage.includes('const target = projectHref(projectTarget, response.id)') &&
    projectsPage.includes('context.delete(\'next\')') &&
    projectsPage.includes('context.delete(\'projectId\')') &&
    projectsPage.includes('navigate(query ? `${target}?${query}` : target)'),
  '兼容链接按项目 ID 直查，不依赖列表请求；目标跳转保留非路由控制参数，避免丢失旧数据集上下文',
)
record(
  '清洗原生工作台不会把流程按钮送回旧壳',
  cleaningView.includes('export type CleaningNavigation') &&
    cleaningView.includes('mapCleaningFlowRoute(route, navigation)') &&
    toolPages.includes("planning: '/new'") &&
    toolPages.includes("results: '/deliveries'") &&
    toolPages.includes('navigation={navigation}'),
  '旧清洗组件保留兼容默认值，Atelier 工作台注入 /new、/projects、/deliveries 等新路径',
)
record(
  '旧阶段入口选择项目后进入对应 Atelier 工作区',
  routes.includes("nativeHref: '/projects?next=project.blueprint'") &&
    routes.includes("nativeHref: '/projects?next=project.data'") &&
    routes.includes("nativeHref: '/projects?next=project.quality'") &&
    projectsPage.includes('useSearchParams') &&
    projectsPage.includes('projectTarget') &&
    projectsPage.includes('navigate(projectHref(projectTarget'),
  '设计、数据和质量阶段不会只落到项目首页',
)
record(
  '任务详情通过 T31 映射桥接，而非把 datasetId 当 projectId',
  app.includes('<Route path="/console/tasks/:taskId" element={<LegacyTaskBridgeRoute />} />') &&
    bridge.includes("`/v1/legacy/datasets/${datasetId}/project`") &&
    bridge.includes('fillRoutePathByKey(target, { projectId })') &&
    bridge.includes('mapping.datasetId !== datasetId') &&
    bridge.includes("nativePathForMapping(mapping, 'project.overview')") &&
    bridge.includes("mapping.migrationStatus !== 'mapped'") &&
    bridge.includes('legacyHistoryPath(datasetId)'),
  '请求映射后仅使用响应 projectId 生成项目地址；无映射/错误回退到同 id 的只读历史页',
)
record(
  '旧阶段带 taskId 时进入对应历史页签，不把旧记录伪装成 Atelier 新数据',
  app.includes('target="project.blueprint"') &&
    app.includes('target="project.data"') &&
    app.includes('target="project.quality"') &&
    app.includes('target="project.releases"') &&
    bridge.includes('legacyHistoryPathForTarget(datasetId, target)') &&
    bridge.includes("'project.blueprint': 'structure'") &&
    bridge.includes("'project.data': 'samples'") &&
    bridge.includes("'project.releases': 'artifacts'") &&
    bridge.includes("<Navigate to=\"/legacy/history\" replace />") &&
    bridge.includes('`${legacyHistoryPath(datasetId)}?tab=${HISTORY_TAB_BY_TARGET[target]}`') &&
    app.includes("const legacyView = location.pathname.endsWith('/legacy')"),
  '方向、数据、质量、导出分别打开结构/样本/制品只读页签；缺上下文回到历史索引',
)
record(
  '旧任务与阶段只读历史路径实际挂载',
  studioRoutes.includes("path: '/legacy/history'") &&
    studioRoutes.includes("path: '/legacy/history/:datasetId'") &&
    studioTree.includes("'legacy.history': () => <LegacyHistoryRoute />") &&
    studioTree.includes("'legacy.history.detail': () => <LegacyHistoryRoute />") &&
    studioTree.includes('onDatasetChange={(nextId) => navigate(`/legacy/history/${nextId}?tab=overview`, { replace: true })') &&
    studioTree.includes('{auxiliaryRoutes.map((route) =>') &&
    historyPage.includes('client.get<LegacyProjectMapping>') &&
    !bridge.includes('client.post(') && !bridge.includes('client.put('),
  'Studio 路由承载历史页，历史页读取映射与真实旧资产，桥接层不发写请求',
)
record(
  '普通用户旧任务详情不请求管理员专属导出映射',
  app.includes("isAdmin ? settle(consoleApi.listExportMappings(), 'export-mappings') : Promise.resolve(null)") &&
    app.includes('字段映射由管理员治理工作区管理'),
  '基于实际浏览器 403 修正为角色化读取，不把权限拒绝记成静默能力失败',
)
record(
  '旧详情仍可显式打开兼容视图',
    app.includes('path="/console/tasks/:taskId/legacy" element={renderTaskDetail()}') &&
    app.includes('navigate(`/console/tasks/${dataset.id}/legacy`)') &&
    app.includes('>旧版操作</Button>') &&
    app.includes("const legacyRoute = `${route.replace(/\\/$/, '')}/legacy`") &&
    app.includes('path="/console/domains/legacy" element={renderDomains()}') &&
    app.includes('path="/console/exports/legacy" element={renderExportStage()}'),
  '原旧页面作为显式兼容子路由保留，默认深链则执行 T31 桥接',
)
record(
  '历史资产页只承载迁移对象浏览',
  !historyPage.includes('Modal.confirm({') &&
    !historyPage.includes('打开兼容操作') &&
    historyPage.includes('onDatasetChange?.(next)') &&
    historyPage.includes('client.get<LegacyProjectMapping>'),
  '旧版写操作不再从产品页暴露，历史对象通过真实 GET 与项目映射查看',
)
record(
  '管理员旧入口可直达治理标签',
  routes.includes("#admin-governance/providers") &&
    routes.includes("#admin-governance/storage") &&
    routes.includes("#admin-governance/strategies") &&
    routes.includes("#admin-governance/prompts") &&
    routes.includes("#admin-governance/audit") &&
    adminPage.includes('adminTabFromHash') &&
    adminPage.includes('hashchange'),
  'provider/storage/strategy/prompt/audit 深链接不再默认落到运行监控',
)

// Mutation self-check: removing one indexed declaration must be observable.
const mutated = routes.replace("route: '/console/exports'", "route: '/console/removed'")
record('变异自证：索引删除一个旧入口会失败', !new RegExp("route:\\s*'/console/exports'").test(mutated), '删除索引项后不再匹配原入口')

if (failures.length > 0) {
  console.error(`\n旧功能兼容索引守卫失败：${failures.length} 项`)
  process.exitCode = 1
} else {
  console.log(`\n旧功能兼容索引守卫通过：${expected.length} 个入口均已覆盖`)
}
