/**
 * 断网待同步队列（Issue #160 T29）。
 *
 * 契约：#160 T29 的原文要求——
 *
 *   - 保留最近读取内容与明确的「待同步」意图，但**离线写入不显示成功**；
 *   - 草稿/判断意图可持久化为绑定 actor/workspace/object/revision 的本地队列；
 *     重连先鉴权，再展示缓存/重读版本/差异，用户确认后提交；
 *   - **启动收费运行、发布、成员/连接变更不得后台自动重放**，需在线重新确认；
 *     幂等键防网络不确定导致的重复提交；
 *   - 退出账号或获知撤权时清理敏感缓存；离线客户端无法即时获知远端撤权，
 *     不能承诺远程擦除已读取内容；
 *   - 首版敏感正文默认仅当前会话缓存，离线可见期**最多 15 分钟**且不超过原会话有效期，
 *     工作区可禁用离线内容；恢复联网必须先鉴权，权限失败即清理并停止同步；
 *   - 明确浏览器存储配额/失效提示。
 *
 * 五条与「不骗用户」直接相关的实现约定：
 *
 *  1. **只有非收费意图可入队**。`queueableKinds` 是**允许清单**（不是禁止清单）：
 *     新增一种写操作时它默认不可入队，必须显式加进来 —— 反过来（禁止清单）
 *     会让将来新增的收费操作默认可以后台重放，而那是会真实花钱的。
 *  2. **条目绑定 actor**。队列里存 `actorId`，冲刷时必须与当前登录用户一致，
 *     否则拒绝并清理：换账号后误提交上一个账号的判断是隐私与正确性的双重事故。
 *  3. **离线不返回成功语义**。入队返回 `pending`，界面上写「待同步（未提交）」。
 *     返回「已保存」会让用户以为数据已经进库，而它只在本机。
 *  4. **敏感正文有 TTL**（≤ 15 分钟）且退出账号即清。离线无法得知远端撤权，
 *     因此文档与界面都**不承诺**远程擦除已读取内容。
 *  5. **配额与失效显式报错**。存储写失败必须让用户看到（否则他会以为草稿保存了）。
 */

/** 队列条目的种类：只有**非收费、可安全重放**的意图允许入队。 */
export type PendingKind = 'review_decision_draft' | 'comment_draft'

/**
 * 允许入队的种类（allowlist）。
 *
 * 刻意**不包含**：创建批次（启动收费运行）、发布、成员/连接变更 ——
 * 这些操作必须在线重新确认（T29 验收项）。
 */
export const queueableKinds: readonly PendingKind[] = ['review_decision_draft', 'comment_draft']

/** 离线敏感正文的最大保留时长（T29 原文：最多 15 分钟）。 */
export const OFFLINE_CONTENT_TTL_MS = 15 * 60 * 1000

const STORAGE_PREFIX = 'studio.pending.v1'

export type PendingEntry = {
  id: string
  kind: PendingKind
  actorId: number
  workspaceId: number
  /** 对象引用：例如 `sample_version:123`。 */
  objectRef: string
  /** 提交时的 revision（用于冲突检测：revision 变了就不能静默覆盖）。 */
  revision: number
  /** 幂等键：网络不确定时重复提交得到同一个结果。 */
  idempotencyKey: string
  /** 意图正文（判断理由/评论内容）。 */
  payload: { body: string; action?: string }
  createdAt: number
  expiresAt: number
}

export type EnqueueResult =
  | { status: 'pending'; entry: PendingEntry }
  | { status: 'rejected'; reason: string }

export type FlushOutcome = {
  submitted: string[]
  retained: PendingEntry[]
  cleared: boolean
  message?: string
}

/**
 * 我们用到的 localStorage 子集（同时也是垫片测试的注入点）。
 *
 * 刻意**不叫** `Storage`：那会遮蔽 DOM 的全局类型，使 `Pick<Storage, ...>`
 * 变成自引用（TypeScript 报 2456），而错误信息完全指不到真正的原因。
 */
type StorageLike = {
  getItem(key: string): string | null
  setItem(key: string, value: string): void
  removeItem(key: string): void
  readonly length: number
  key(index: number): string | null
}

function storage(): StorageLike | null {
  try {
    if (typeof window === 'undefined' || !window.localStorage) return null
    return window.localStorage
  } catch {
    // 隐私模式/被策略禁用时访问 localStorage 会抛异常：返回 null 让调用方
    // 走「不可用」分支（明确告知用户），而不是让页面崩在读取队列上。
    return null
  }
}

function storageKey(actorId: number, workspaceId: number): string {
  // 键里带 actorId：**必须**如此。固定键会让下一个登录的用户读到上一个人的
  // 草稿，并可能替他提交（与 T10 的向导草稿同一形态的隐私问题）。
  return `${STORAGE_PREFIX}.u${actorId}.w${workspaceId}`
}

/** 生成幂等键（不依赖 crypto.randomUUID 的可用性）。 */
export function newPendingIdempotencyKey(): string {
  const random = Math.random().toString(36).slice(2, 10)
  return `pending-${Date.now().toString(36)}-${random}`
}

/**
 * 入队一个待同步意图。
 *
 * 返回 `pending`（而不是「已保存/已提交」）：离线时唯一诚实的说法是
 * 「它在本机，等联网再提交」。
 */
export function enqueue(input: {
  kind: string
  actorId: number
  workspaceId: number
  objectRef: string
  revision: number
  payload: { body: string; action?: string }
  now?: number
}): EnqueueResult {
  if (!queueableKinds.includes(input.kind as PendingKind)) {
    return {
      status: 'rejected',
      reason:
        '这类操作不能离线排队：启动运行、发布、成员/连接变更必须联网重新确认（它们会花钱或改变权限）',
    }
  }
  if (input.actorId <= 0) {
    return { status: 'rejected', reason: '未登录：离线草稿必须绑定当前账号' }
  }
  const store = storage()
  if (!store) {
    return { status: 'rejected', reason: '浏览器本地存储不可用（隐私模式或被策略禁用），无法保存离线草稿' }
  }
  const now = input.now ?? Date.now()
  const entry: PendingEntry = {
    id: newPendingIdempotencyKey(),
    kind: input.kind as PendingKind,
    actorId: input.actorId,
    workspaceId: input.workspaceId,
    objectRef: input.objectRef,
    revision: input.revision,
    idempotencyKey: newPendingIdempotencyKey(),
    payload: input.payload,
    createdAt: now,
    expiresAt: now + OFFLINE_CONTENT_TTL_MS,
  }
  const existing = readQueue(input.actorId, input.workspaceId, now)
  const next = [...existing.filter((item) => item.objectRef !== entry.objectRef), entry]
  try {
    store.setItem(storageKey(input.actorId, input.workspaceId), JSON.stringify(next))
  } catch {
    // 配额不足/存储被禁用：必须让用户看到，否则他会以为草稿保住了。
    return { status: 'rejected', reason: '本地存储写入失败（可能已满）：草稿没有保存，请先导出手头内容' }
  }
  return { status: 'pending', entry }
}

/** 读取队列（自动丢弃过期条目：离线可见期不超过 TTL）。 */
export function readQueue(actorId: number, workspaceId: number, now = Date.now()): PendingEntry[] {
  const store = storage()
  if (!store) return []
  const raw = store.getItem(storageKey(actorId, workspaceId))
  if (!raw) return []
  let parsed: unknown
  try {
    parsed = JSON.parse(raw)
  } catch {
    // 坏数据（手工改过/半个写入）直接丢弃：留着会让队列永远刷不动。
    store.removeItem(storageKey(actorId, workspaceId))
    return []
  }
  if (!Array.isArray(parsed)) return []
  const alive = (parsed as PendingEntry[]).filter(
    (entry) => entry && typeof entry.expiresAt === 'number' && entry.expiresAt > now && entry.actorId === actorId,
  )
  if (alive.length !== (parsed as PendingEntry[]).length) {
    try {
      store.setItem(storageKey(actorId, workspaceId), JSON.stringify(alive))
    } catch {
      // 清理失败不影响「返回未过期条目」这个正确结果。
    }
  }
  return alive
}

/** 清理某个账号的队列（退出账号 / 获知撤权时调用）。 */
export function clearForActor(actorId: number, workspaceId?: number): void {
  const store = storage()
  if (!store) return
  if (typeof workspaceId === 'number') {
    store.removeItem(storageKey(actorId, workspaceId))
    return
  }
  // 不知道工作区时按前缀清理（退出账号时走这条）：删掉该账号的所有工作区队列。
  for (const key of listKeys(store)) {
    if (key.startsWith(`${STORAGE_PREFIX}.u${actorId}.`)) store.removeItem(key)
  }
}

/** 清空所有账号的队列（仅用于测试与「清理本机数据」）。 */
export function clearAll(): void {
  const store = storage()
  if (!store) return
  for (const key of listKeys(store)) {
    if (key.startsWith(STORAGE_PREFIX)) store.removeItem(key)
  }
}

function listKeys(store: StorageLike): string[] {
  const keys: string[] = []
  for (let index = 0; index < store.length; index += 1) {
    const key = store.key(index)
    if (key) keys.push(key)
  }
  return keys
}

/**
 * 冲刷队列（重连后由界面显式调用）。
 *
 * 四条规则：
 *   - **先鉴权**：`currentActorId` 必须与条目一致，否则拒绝并清理（换账号误提交）；
 *   - 提交前把最新 revision 交给调用方比较，**revision 变了就保留草稿**（不静默覆盖）；
 *   - 提交失败（网络/权限）时保留条目，用户确认后再试；
 *   - 权限失败（403/404）时清理该账号队列并停止同步（撤权语义）。
 */
export async function flush(input: {
  currentActorId: number
  workspaceId: number
  /** 提交一条：返回 'submitted' 成功、'conflict' revision 冲突、'denied' 权限失败、'error' 其它失败。 */
  submit: (entry: PendingEntry) => Promise<'submitted' | 'conflict' | 'denied' | 'error'>
  now?: number
}): Promise<FlushOutcome> {
  const now = input.now ?? Date.now()
  const queue = readQueue(input.currentActorId, input.workspaceId, now)
  if (input.currentActorId <= 0) {
    return { submitted: [], retained: queue, cleared: false, message: '未登录：请先登录再同步' }
  }
  const mismatched = queue.filter((entry) => entry.actorId !== input.currentActorId)
  if (mismatched.length > 0) {
    // 理论上 readQueue 已按 actor 过滤；这里再判一次是防御性的：
    // 漏掉这一步的后果是替别人提交，方向不可接受。
    return { submitted: [], retained: [], cleared: true, message: '队列属于其它账号，已清理' }
  }

  const submitted: string[] = []
  const retained: PendingEntry[] = []
  let denied = false
  for (const entry of queue) {
    const outcome = await input.submit(entry)
    switch (outcome) {
      case 'submitted':
        submitted.push(entry.id)
        break
      case 'denied':
        denied = true
        break
      case 'conflict':
      case 'error':
      default:
        retained.push(entry)
        break
    }
  }

  if (denied) {
    clearForActor(input.currentActorId, input.workspaceId)
    return {
      submitted,
      retained: [],
      cleared: true,
      message: '权限已变化：本地待同步内容已清理，请刷新页面后重新登录',
    }
  }

  const store = storage()
  if (store) {
    try {
      store.setItem(storageKey(input.currentActorId, input.workspaceId), JSON.stringify(retained))
    } catch {
      // 写回失败不改变「已提交的部分」这一事实。
    }
  }
  return { submitted, retained, cleared: false }
}

/** 待同步条数（界面用于显示「待同步（未提交）」）。 */
export function pendingCount(actorId: number, workspaceId: number, now = Date.now()): number {
  return readQueue(actorId, workspaceId, now).length
}

/** 离线内容是否被工作区禁用（首版：由注入的开关决定）。 */
export function offlineContentAllowed(workspaceDisabled: boolean, sessionValidUntil?: number, now = Date.now()): boolean {
  if (workspaceDisabled) return false
  if (typeof sessionValidUntil === 'number' && now >= sessionValidUntil) return false
  return true
}

/**
 * 会话用户 ID 在 localStorage 里的键。
 *
 * 为什么由本模块**拥有**这个键名：队列的 actor 绑定必须与「当前登录是谁」用
 * 同一个来源。两处各自写一个键名迟早会漂移，而漂移的表现是
 * 「队列按 A 存、按 B 读」—— 于是草稿看起来丢了（或更糟：错寄）。
 *
 * 只用于**本地分键**，不参与任何权限判定（权限一律由服务端判）。
 */
const SESSION_ACTOR_KEY = 'studio.session.userId'

/** 读取当前会话用户 ID（读不到返回 0，表示「未登录/未知」）。 */
export function currentActorID(): number {
  const store = storage()
  if (!store) return 0
  const raw = store.getItem(SESSION_ACTOR_KEY)
  const parsed = Number.parseInt(raw ?? '', 10)
  return Number.isFinite(parsed) && parsed > 0 ? parsed : 0
}
