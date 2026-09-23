/**
 * Atelier entrypoint guard (Issue #160 / #164 follow-up).
 *
 * The Studio routes were already mounted, but the authentication boundary still
 * sent users to the legacy `/console/tasks` page after login. That made the
 * redesign appear absent on the standard 3210 deployment. Keep the assertion
 * close to the source so a future auth refactor cannot silently restore the old
 * product entrypoint.
 */

import { readFileSync } from 'node:fs'
import path from 'node:path'
import { fileURLToPath } from 'node:url'

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..')
const source = readFileSync(path.join(root, 'apps', 'web-user', 'src', 'App.tsx'), 'utf8')
const helper = readFileSync(path.join(root, 'apps', 'web-user', 'src', 'lib', 'authRedirect.ts'), 'utf8')

const failures = []
function record(name, ok, detail) {
  console.log(`[${ok ? 'PASS' : 'FAIL'}] ${name}: ${detail}`)
  if (!ok) failures.push(name)
}

const loginRedirect = source.match(
  /if \(user && location\.pathname === '\/login'\) \{([\s\S]*?)\n    \}/,
)?.[1] ?? ''
const loginSubmit = source.match(
  /const handleLogin = async \([^)]*\)[\s\S]*?\n    \} catch \(error\)/,
)?.[0] ?? ''
const authenticatedLoginRoute = source.match(
  /path="\/login"[\s\S]*?user \? \(([\s\S]*?)\) : \(/,
)?.[1] ?? ''

record(
  '认证守卫使用安全的 Atelier 登录目标',
  /navigate\(loginRedirect, \{ replace: true \}\)/.test(loginRedirect) && /resolveLoginRedirect/.test(source),
  '登录页已有会话时必须使用经过校验的 next，缺省目标为 /today',
)
record(
  '登录提交进入 Atelier 目标页',
  /navigate\(loginRedirect, \{ replace: true \}\)/.test(loginSubmit),
  '登录成功后恢复安全的站内 next，缺省目标由 helper 提供',
)
record(
  '登录路由已登录分支进入 Atelier',
  /<Navigate to=\{loginRedirect\} replace \/>/.test(authenticatedLoginRoute),
  '直接访问 /login 时不能回到旧任务列表',
)
record(
  '登录目标 helper 拒绝外部跳转',
  /startsWith\('\/\/'\)/.test(helper) && /includes\('\\\\'\)/.test(helper) && /pathname !== '\/login'/.test(helper),
  'next 只允许站内绝对路径，且不能回到登录页形成循环',
)

// Mutation self-check: if the redirect regresses to the legacy route, the
// predicate above must fail rather than pass vacuously.
const mutated = source.replaceAll('navigate(loginRedirect, { replace: true })', "navigate('/console/tasks', { replace: true })")
const mutatedBlock = mutated.match(
  /if \(user && location\.pathname === '\/login'\) \{([\s\S]*?)\n    \}/,
)?.[1] ?? ''
record(
  '变异自证：旧认证入口会被捕获',
  !/navigate\(loginRedirect, \{ replace: true \}\)/.test(mutatedBlock),
  '将认证守卫改回 /console/tasks 后断言失败',
)

if (failures.length > 0) {
  console.error(`\n${failures.length} 个 Atelier 入口守卫失败`)
  process.exitCode = 1
}
