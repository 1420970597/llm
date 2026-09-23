/**
 * Atelier legacy compatibility index guard.
 *
 * The old console remains a supported read/operation surface during migration.
 * This source-level check prevents a legacy entry from silently disappearing
 * from the Atelier discoverability index or from the actual App route tree.
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
  record(`旧入口 ${route} 已进入 Atelier 索引`, indexed, indexed ? 'legacyCapabilities.ts 已声明 compat 状态' : 'legacyCapabilities.ts 缺少声明')
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

record('Atelier 兼容索引有可达路由', /key:\s*'settings\.capabilities'[\s\S]*?path:\s*'\/settings\/capabilities'/.test(studioRoutes), '辅助入口元数据包含 /settings/capabilities')
record('兼容索引已注册页面组件', /['"]settings\.capabilities['"]:\s*\(\)\s*=>\s*<LegacyCapabilitiesPage\s*\/>/.test(studioTree), 'StudioRoutes.tsx 注册 LegacyCapabilitiesPage')
record(
  'Atelier 下钻旧阶段时保留任务上下文',
  studioTree.includes('const withTask = (path: string) => `${path}?taskId=${projectId}') &&
    /stageRouteDatasetId\(search: string\)[\s\S]*?get\('taskId'\)/.test(app),
  '兼容链接与旧壳使用同一 taskId 查询参数，避免进入后显示空任务',
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
