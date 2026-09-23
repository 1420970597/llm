/**
 * Team settings permission and project-list contract guard.
 *
 * Exercises the production parsing/role helpers with esbuild, then verifies the
 * page gates member mutations on current workspace membership and server
 * project capabilities. No API, database, or browser is required.
 */
import assert from 'node:assert/strict'
import { transform } from 'esbuild'
import { readFileSync } from 'node:fs'
import path from 'node:path'
import { fileURLToPath } from 'node:url'

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..')
const settingsPath = path.join(root, 'apps/web-user/src/studio/pages/SettingsPages.tsx')
const settingsSource = readFileSync(settingsPath, 'utf8')

function helperSource(name) {
  const start = settingsSource.indexOf(`export function ${name}`)
  assert.notEqual(start, -1, `生产代码必须导出 ${name}`)
  const end = settingsSource.indexOf('\n}\n', start)
  assert.notEqual(end, -1, `${name} 函数必须闭合`)
  return settingsSource.slice(start, end + 2)
}

const helperCode = await transform(
  `${helperSource('parseManageableProjectOptions').replace('export function', 'function')}\n${helperSource('isWorkspaceAdminMember').replace('export function', 'function')}\nexport { parseManageableProjectOptions, isWorkspaceAdminMember }`,
  { loader: 'ts', format: 'esm' },
)
const helpers = await import(`data:text/javascript;base64,${Buffer.from(helperCode.code).toString('base64')}`)
const { isWorkspaceAdminMember, parseManageableProjectOptions } = helpers

assert.deepEqual(
  parseManageableProjectOptions([
    {
      id: 'p_12',
      capabilities: { canManageMembers: true },
      data: { id: 12, name: '项目 A' },
    },
    {
      id: 'p_13',
      capabilities: { canManageMembers: false },
      data: { id: 13, name: '只读项目' },
    },
    { id: 'p_14', capabilities: { canManageMembers: true }, data: { id: '14', name: '错误类型 ID' } },
    { id: 'p_15', data: { id: 15, name: '缺少能力位' } },
  ]),
  [{ id: 12, name: '项目 A', canManageMembers: true }],
  '项目下拉必须从 envelope.data.id/name 解析，并只纳入服务端声明可管理成员的项目',
)

assert.equal(
  isWorkspaceAdminMember(7, [{ userId: 7, role: 'admin' }]),
  true,
  '工作区管理能力来自当前用户在 workspace_members 中的角色',
)
assert.equal(
  isWorkspaceAdminMember(7, [{ userId: 7, role: 'member', userRole: 'admin' }]),
  false,
  'users.role=admin 不能提升为工作区管理员',
)
assert.equal(isWorkspaceAdminMember(null, [{ userId: 7, role: 'admin' }]), false)

const teamStart = settingsSource.indexOf('export function TeamPage()')
const teamEnd = settingsSource.indexOf('// 帮助（S04）', teamStart)
assert.ok(teamStart >= 0 && teamEnd > teamStart, 'TeamPage must remain a separately inspectable page')
const teamPage = settingsSource.slice(teamStart, teamEnd)

assert.match(teamPage, /type ProjectListItem = TypedEnvelope<ProjectIdentity, ProjectCapabilities>/)
assert.match(teamPage, /settingsApi\.projects\(\{ limit: 50/)
assert.match(teamPage, /parseManageableProjectOptions\(collected\)/)
assert.match(teamPage, /while \(cursor && !seenCursors\.has\(cursor\) && seenCursors\.add\(cursor\)\)/)
assert.match(teamPage, /settingsApi\.workspaceMembers\(\)[\s\S]*authApi\.me\(\)/)
assert.match(teamPage, /isWorkspaceAdminMember\(userId, workspaceMembers\)/)
assert.match(teamPage, /canManageWorkspace \? \(/)
assert.match(teamPage, /data-team-workspace-readonly="true"/)
assert.match(teamPage, /project\?\.canManageMembers !== true/)
assert.match(teamPage, /selectedProject && projects\.some\(\(project\) => String\(project\.id\) === selectedProject && project\.canManageMembers\)/)
assert.match(teamPage, /settingsApi\.upsertProjectMember\(projectID/)
assert.match(teamPage, /email: targetEmail/)
assert.match(teamPage, /settingsApi\.removeProjectMember\(projectID, userID, projectMemberReason\.trim\(\)\)/)
assert.match(teamPage, /role: projectMemberRole as 'owner' \| 'reviewer' \| 'viewer'/)
assert.match(teamPage, /reason: projectMemberReason\.trim\(\)/)
assert.doesNotMatch(teamPage, /currentUserResult\.value\.user\.role\s*===\s*['"]admin['"]/, 'global users.role must never grant workspace membership-management rights')

const apiSource = readFileSync(path.join(root, 'apps/web-user/src/lib/api/studio.ts'), 'utf8')
assert.match(apiSource, /export type ProjectMemberUpsertInput = \{[\s\S]*reason: string[\s\S]*\}\s*& \(\{ userId: number; email\?: never \} \| \{ email: string; userId\?: never \}\)/)
assert.match(apiSource, /removeProjectMember: \(projectId: number, userId: number, reason: string\)/)
assert.match(apiSource, /delete\(`\$\{projectPath\(projectId\)\}\/members\/\$\{userId\}`, \{ params: \{ reason \} \}\)/)

console.log('[PASS] 项目 envelope 解析、分页、工作区角色隔离与项目成员能力位守卫通过')
