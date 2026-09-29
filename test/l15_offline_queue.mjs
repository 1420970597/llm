/**
 * 断网待同步队列守卫（Issue #160 T29）。
 *
 * 运行：node test/l15_offline_queue.mjs
 *
 * ---------------------------------------------------------------------------
 * 这个守卫防的是什么
 * ---------------------------------------------------------------------------
 * T29 的验收项里有四条是**语义性**的，靠点一遍页面无法长期保证：
 *
 *  1. **离线不返回成功**：入队必须是「待同步」，不能是「已保存」——
 *     返回成功会让用户以为数据已经进库，而它只在本机。
 *  2. **收费/权限操作不得入队**：启动运行、发布、成员/连接变更必须联网重新确认。
 *     入队允许清单（allowlist）而不是禁止清单：新增写操作时默认不可入队。
 *  3. **队列绑定账号**：换账号后不得误提交上一个账号的内容（隐私 + 正确性）。
 *  4. **敏感正文有 TTL 且退出即清**：最多 15 分钟，且离线可见期不超过原会话。
 *
 * 第 5 条由**变异自证**保证断言非空转：把允许清单改回「什么都允许」，
 * 第 2 条断言必须失败。
 *
 * 用 esbuild 打包**生产模块** src/lib/pendingQueue.ts 并真实调用：
 * 这个模块不依赖 React 与浏览器专用 API（localStorage 通过注入的 globalThis 提供），
 * 因此可以直接执行 —— 与 test/l15_studio_shell.mjs 第 2 层同一路线。
 */

import { mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import { createRequire } from 'node:module'
import { tmpdir } from 'node:os'
import path from 'node:path'
import { fileURLToPath } from 'node:url'

const REPO_ROOT = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..')
const WEB_ROOT = path.join(REPO_ROOT, 'apps', 'web-user')
const QUEUE_SOURCE = path.join(WEB_ROOT, 'src', 'lib', 'pendingQueue.ts')

const webRequire = createRequire(path.join(WEB_ROOT, 'package.json'))
const esbuild = webRequire('esbuild')

const failures = []
function record(name, ok, detail) {
  console.log(`[${ok ? 'PASS' : 'FAIL'}] ${name}: ${detail}`)
  if (!ok) failures.push(name)
}
function recordSkip(name, detail) {
  console.log(`[SKIP] ${name}: ${detail}`)
}

/** 最小 localStorage 垫片（带 length/key，配额/禁用可用 flag 模拟）。 */
const STORAGE_SHIM = `
(function installStorageShim() {
  if (globalThis.window && globalThis.window.__l15Storage) return
  const map = new Map()
  let failWrites = false
  const storage = {
    get length() { return map.size },
    key(index) { return Array.from(map.keys())[index] ?? null },
    getItem(key) { return map.has(key) ? map.get(key) : null },
    setItem(key, value) {
      if (failWrites) { const error = new Error('QuotaExceededError'); error.name = 'QuotaExceededError'; throw error }
      map.set(key, String(value))
    },
    removeItem(key) { map.delete(key) },
    clear() { map.clear() },
  }
  globalThis.window = globalThis.window || {}
  globalThis.window.localStorage = storage
  globalThis.window.__l15Storage = storage
  globalThis.__l15FailWrites = (value) => { failWrites = value }
  globalThis.__l15DumpStorage = () => Object.fromEntries(map.entries())
})();
`

async function buildQueueModule(workDir, sourceOverride) {
  const outfile = path.join(workDir, `pending-${sourceOverride ? 'mutated' : 'base'}.cjs`)
  const plugin = sourceOverride
    ? {
        name: 'override-queue',
        setup(build) {
          build.onLoad({ filter: /lib[\\/]pendingQueue\.ts$/ }, () => ({
            contents: sourceOverride,
            loader: 'ts',
            resolveDir: path.dirname(QUEUE_SOURCE),
          }))
        },
      }
    : undefined
  await esbuild.build({
    stdin: {
      contents: `export * from ${JSON.stringify(QUEUE_SOURCE)}`,
      resolveDir: WEB_ROOT,
      loader: 'ts',
    },
    bundle: true,
    format: 'cjs',
    platform: 'node',
    absWorkingDir: WEB_ROOT,
    outfile,
    nodePaths: [path.join(WEB_ROOT, 'node_modules'), path.join(REPO_ROOT, 'node_modules')],
    mainFields: ['module', 'main'],
    logLevel: 'error',
    banner: { js: STORAGE_SHIM },
    plugins: plugin ? [plugin] : [],
  })
  return webRequire(outfile)
}

const workDir = mkdtempSync(path.join(tmpdir(), 'l15-offline-queue-'))
try {
  const queue = await buildQueueModule(workDir)

  // ---- 1. 离线入队是「待同步」，不是「已保存」 ----
  const actor = 42
  const workspace = 7
  const accepted = queue.enqueue({
    kind: 'review_decision_draft',
    actorId: actor,
    workspaceId: workspace,
    objectRef: 'sample_version:11',
    revision: 3,
    payload: { body: '第二步跳得太快', action: 'accepted' },
  })
  record('离线入队返回待同步（不是已保存）', accepted.status === 'pending',
    `status=${accepted.status}`)
  record('待同步条数可见', queue.pendingCount(actor, workspace) === 1,
    `pendingCount=${queue.pendingCount(actor, workspace)}`)

  // ---- 2. 收费/权限操作不得入队（含变异自证） ----
  const forbidden = [
    ['batch_create', '启动收费运行'],
    ['release_publish', '发布'],
    ['member_change', '成员/连接变更'],
  ]
  let forbiddenOK = true
  for (const [kind, label] of forbidden) {
    const result = queue.enqueue({
      kind, actorId: actor, workspaceId: workspace,
      objectRef: 'x:1', revision: 1, payload: { body: 'x' },
    })
    if (result.status !== 'rejected') {
      forbiddenOK = false
      record(`离线入队拒绝${label}`, false, `实际 status=${result.status}`)
    }
  }
  if (forbiddenOK) {
    record('离线入队拒绝收费/权限类操作', true, '3 类操作全部被拒绝（允许清单语义）')
  }

  // ---- 3. 队列绑定账号：换账号不误提交 ----
  const otherActor = 43
  record('换账号读不到别人的队列', queue.pendingCount(otherActor, workspace) === 0,
    `otherPending=${queue.pendingCount(otherActor, workspace)}`)
  let submittedForOther = 0
  const mismatch = await queue.flush({
    currentActorId: otherActor, workspaceId: workspace,
    submit: async () => { submittedForOther += 1; return 'submitted' },
  })
  record('换账号冲刷不会提交原账号内容', submittedForOther === 0 && mismatch.submitted.length === 0,
    `submitted=${submittedForOther}`)

  // ---- 4. TTL（≤ 15 分钟）与过期清理 ----
  record('TTL 不超过 15 分钟', queue.OFFLINE_CONTENT_TTL_MS <= 15 * 60 * 1000,
    `ttl=${queue.OFFLINE_CONTENT_TTL_MS}ms`)
  const later = Date.now() + queue.OFFLINE_CONTENT_TTL_MS + 1000
  const expiredCount = queue.readQueue(actor, workspace, later).length
  record('过期条目被自动清理', expiredCount === 0, `laterPending=${expiredCount}`)
  // 上一步的「按未来时间读取」会真的把过期条目清掉（这是预期行为），
  // 因此下面两段各重新入队一次 —— 否则它们测的是空队列，而不是冲突/撤权语义。
  queue.enqueue({
    kind: 'review_decision_draft', actorId: actor, workspaceId: workspace,
    objectRef: 'sample_version:11', revision: 3, payload: { body: '重新入队', action: 'accepted' },
  })

  // ---- 5. 冲突保留草稿；权限失败清理并停止同步 ----
  const conflict = await queue.flush({
    currentActorId: actor, workspaceId: workspace,
    submit: async () => 'conflict',
  })
  record('revision 冲突时保留草稿（不静默覆盖）',
    conflict.retained.length === 1 && conflict.submitted.length === 0 && queue.pendingCount(actor, workspace) === 1,
    `retained=${conflict.retained.length}`)

  queue.enqueue({
    kind: 'review_decision_draft', actorId: actor, workspaceId: workspace,
    objectRef: 'sample_version:12', revision: 4, payload: { body: '撤权用例', action: 'accepted' },
  })
  const denied = await queue.flush({
    currentActorId: actor, workspaceId: workspace,
    submit: async () => 'denied',
  })
  record('权限失败清理队列并停止同步',
    denied.cleared === true && queue.pendingCount(actor, workspace) === 0,
    `cleared=${denied.cleared}`)

  // ---- 6. 存储配额失败显式报错（不静默丢草稿） ----
  queue.enqueue({ kind: 'comment_draft', actorId: actor, workspaceId: workspace, objectRef: 'batch:1', revision: 1, payload: { body: 'ok' } })
  globalThis.__l15FailWrites(true)
  const quota = queue.enqueue({
    kind: 'comment_draft', actorId: actor, workspaceId: workspace,
    objectRef: 'batch:2', revision: 1, payload: { body: 'quota' },
  })
  globalThis.__l15FailWrites(false)
  record('存储配额失败显式报错', quota.status === 'rejected' && /满|失败/.test(quota.reason ?? ''),
    `status=${quota.status} reason=${quota.reason ?? ''}`)

  // ---- 7. 退出账号清理；离线内容可被工作区禁用 ----
  queue.clearForActor(actor)
  record('退出账号清理该账号队列', queue.pendingCount(actor, workspace) === 0,
    `afterClear=${queue.pendingCount(actor, workspace)}`)
  // ---- 6. actor 分键必须有写有读（#160 T29 实测补强） ----
  //
  // 缺陷形态（实测发现）：`currentActorID()` 读 `studio.session.userId`，
  // 但**全仓库没有一处写这个键** —— 于是 actorId 永远是 0，后果有两个且都是静默的：
  //   1. 离线草稿一律以「未登录」被拒绝（功能整条不可用，但界面只显示一句
  //      看似合理的错误，所以自动化以外的走查极易放行）；
  //   2. 退出账号时 `clearForActor(0)` 是空操作，本机敏感正文不清理，
  //      而这正是 T29 原文要禁止的。
  //
  // 这条断言是**源码级**的：它不依赖任何运行时路径，因此不会因为
  // 「本地没登录」而跳过 —— 而跳过正是这个缺陷能潜伏下来的原因。
  const queueSourceText = readFileSync(QUEUE_SOURCE, 'utf8')
  const appSourceText = readFileSync(path.join(WEB_ROOT, 'src', 'App.tsx'), 'utf8')
  const actorKeyMatch = queueSourceText.match(/const SESSION_ACTOR_KEY = '([^']+)'/) 
  const actorKey = actorKeyMatch?.[1] ?? ''
  record('会话 actor 键名有定义', actorKey.length > 0, `SESSION_ACTOR_KEY="${actorKey}"`)
  record('存在写入会话 actor 的导出函数',
    /export function setCurrentActorID\(/.test(queueSourceText),
    'setCurrentActorID 已导出（读取方不再是单腿）')
  // 写入方必须真的被 UI 调用，否则模块写了但没人用，缺陷依旧。
  const actorWriteCalls = (appSourceText.match(/setCurrentActorID\(/g) ?? []).length
  record('会话建立/失效处调用 setCurrentActorID', actorWriteCalls >= 2,
    `App.tsx 有 ${actorWriteCalls} 处调用（需 ≥2：登录+恢复、退出+失效）`)
  // actor 为 0 必须在入队处被拒（否则会静默把草稿寄存到「0 号账号」）。
  record('actor 为 0 时拒绝入队（不静默写错寄）',
    /未登录：离线草稿必须绑定当前账号/.test(queueSourceText),
    '未登录分支存在且文案明确')

  record('工作区可禁用离线内容',
    queue.offlineContentAllowed(true) === false && queue.offlineContentAllowed(false) === true,
    'disabled=true → 不允许；disabled=false → 允许')

  // ---- 8. 变异自证：把允许清单改成「什么都允许」，第 2 条断言必须失败 ----
  const original = readFileSync(QUEUE_SOURCE, 'utf8')
  const mutated = original.replace(
    /export const queueableKinds: readonly PendingKind\[\] = \[[^\]]*\]/,
    "export const queueableKinds: readonly PendingKind[] = ['review_decision_draft', 'comment_draft', 'batch_create', 'release_publish', 'member_change'] as readonly PendingKind[]",
  )
  if (mutated === original) {
    record('变异自证（允许清单被放宽必须被捕获）', false,
      '未能定位 queueableKinds —— 源码结构已变化，请同步本守卫')
  } else {
    writeFileSync(path.join(workDir, 'mutated-check.ts'), mutated)
    const mutatedQueue = await buildQueueModule(workDir, mutated)
    const mutatedResult = mutatedQueue.enqueue({
      kind: 'batch_create', actorId: actor, workspaceId: workspace,
      objectRef: 'x:1', revision: 1, payload: { body: 'x' },
    })
    record('变异自证（允许清单被放宽必须被捕获）', mutatedResult.status === 'pending',
      `放宽后 batch_create 变成 ${mutatedResult.status}（第 2 条断言会失败）`)
  }

  // 变异自证：把「写入方」删掉， actor 分键断言必须报错。
  //
  // 用**同一个谓词**跑变异体，而不是只跑真实文件：只跑真实文件证明不了
  // 断言真的在检查这件事（可能只是恒真）。
  const actorWiringProblems = (queueText, appText) => {
    const problems = []
    if (!/const SESSION_ACTOR_KEY = '[^']+'/.test(queueText)) problems.push('缺少键名定义')
    if (!/export function setCurrentActorID\(/.test(queueText)) problems.push('缺少写入函数')
    if ((appText.match(/setCurrentActorID\(/g) ?? []).length < 2) problems.push('UI 未在两个会话转换点调用')
    if (!/未登录：离线草稿必须绑定当前账号/.test(queueText)) problems.push('actor=0 未被拒')
    return problems
  }
  record('actor 分键谓词对真实源码无问题',
    actorWiringProblems(queueSourceText, appSourceText).length === 0,
    '谓词基线通过')
  const mutatedQueueText = queueSourceText.replace(/export function setCurrentActorID\(/, 'function setCurrentActorID(')
  const mutatedAppText = appSourceText.replaceAll('setCurrentActorID(', 'noopActorId(')
  record('变异自证（拆掉 actor 写入链路必须被捕获）',
    actorWiringProblems(mutatedQueueText, mutatedAppText).length >= 2,
    `捕获 ${actorWiringProblems(mutatedQueueText, mutatedAppText).length} 个问题（缺少写入函数 + UI 未调用）`)

  // ---- 9. 真实浏览器断网/窄屏/键盘（可选的第二层，`--with-browser`） ----
  //
  // 为什么保留「需要真实栈」的分支而不是把它移进默认路径：CI 运行镜像里没有
  // chromium 也没有起容器，把需要栈的断言塞进默认路径会让 CI 红掉，
  // 而「让守卫因环境而红」的后果是别人直接把它从 CI 里摘掉（本仓库已发生过）。
  // 因此默认路径只跑不需要容器的源码级断言，实测走可选的采集器。
  const wantsBrowser = process.argv.includes('--with-browser')
  if (!wantsBrowser) {
    recordSkip('真实浏览器断网/窄屏（390/768/1440）断言',
      '未启用 --with-browser：默认路径无容器；采集器为 test/audit/t29_measure.mjs')
  } else {
    // 采集器自己会断言；这里只负责把它的退出码并进本守卫。
    const { spawnSync } = await import('node:child_process')
    const measured = spawnSync(process.execPath,
      [path.join(REPO_ROOT, 'test', 'audit', 't29_measure.mjs')],
      { cwd: REPO_ROOT, stdio: 'inherit' })
    record('真实浏览器 T29 实测（断网/窄屏/键盘）', measured.status === 0,
      `t29_measure.mjs 退出码=${measured.status}（产物 docs/audit/issue-160-t29/findings.json）`)
  }
} finally {
  rmSync(workDir, { recursive: true, force: true })
}

if (failures.length > 0) {
  console.error(`\nOFFLINE QUEUE GUARD FAILED: ${failures.length} 项失败`)
  process.exit(1)
}
console.log('\nOFFLINE QUEUE GUARD OK')
