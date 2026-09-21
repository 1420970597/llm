/**
 * 三步项目向导的草稿状态（Issue #160 T10）。
 *
 * 契约来源：`docs/plans/atelier-api-contract.md` §2.1；
 * `internal/model/project.go` 的 `CreateProjectInput` 校验。
 *
 * 为什么把草稿逻辑单独成一个**无 React 依赖**的模块：
 *
 *  1. 它是 T10 验收项里最容易被忽略的两条的实现处 ——
 *     「刷新恢复草稿且绑定当前用户，退出账号清理」与
 *     「必填/整数/范围错误聚焦字段」。这两条都与渲染无关，
 *     放在组件里就只能靠浏览器点一遍来验。
 *  2. 前端校验与后端校验**必须一致**。把字段范围写在这里一次，
 *     并由 `test/l15_studio_wizard.mjs` 与 Go 侧的常量断言一致，
 *     可以避免「前端说 1–100、后端说 1–50」这类只在提交时才暴露的不一致。
 *
 * 与后端的职责划分：前端校验只是为了**让用户不必往返一次**；
 * 真正的判定在服务端（`model.CreateProjectInput.Validate`）。
 * 因此这里绝不做「前端通过就假定成功」的事。
 */

/** 与后端 `model.TargetKindSFT` / `TargetKindGRPO` 一致。 */
export const TARGET_KINDS = ['sft', 'grpo'] as const
export type TargetKind = (typeof TARGET_KINDS)[number]

/** 与后端 `model.MinPilotSize` / `MaxPilotSize` 一致。 */
export const MIN_PILOT_SIZE = 1
export const MAX_PILOT_SIZE = 100

/** 与后端一致：预算下限 100 分（1 元）；显式 0 表示未设上限。 */
export const MIN_BUDGET_LIMIT_MINOR = 100

/** 与后端一致：名称上限 200 个字符（按 rune 计）。 */
export const MAX_NAME_LENGTH = 200

/** 向导步骤。顺序即用户旅程，`/new` → `/new/coverage` → `/new/quality`。 */
export const WIZARD_STEPS = ['basic', 'coverage', 'quality'] as const
export type WizardStep = (typeof WIZARD_STEPS)[number]

/** 每一步对应的路由路径（前端不自行拼 URL，见 studio/routes.ts 的同一原则）。 */
export const STEP_PATHS: Record<WizardStep, string> = {
  basic: '/new',
  coverage: '/new/coverage',
  quality: '/new/quality',
}

export type WizardDraft = {
  name: string
  goal: string
  targetKind: TargetKind
  /** 三个数字存成字符串：表单里「空」与「0」必须能区分（后端用指针同理）。 */
  domains: string
  directionsPerDomain: string
  questionsPerDirection: string
  pilotSize: string
  acceptanceRateTarget: string
  budgetCurrency: string
  /** 单位是分；空字符串表示未设上限。 */
  budgetLimitMinor: string
  budgetOnExhausted: 'pause' | 'stop'
}

/** 一份空白草稿（默认值与后端 `Normalize()` 的默认值一致）。 */
export function emptyDraft(): WizardDraft {
  return {
    name: '',
    goal: '',
    targetKind: 'sft',
    domains: '1',
    directionsPerDomain: '1',
    questionsPerDirection: '1',
    pilotSize: '1',
    acceptanceRateTarget: '0.85',
    budgetCurrency: 'CNY',
    budgetLimitMinor: '',
    budgetOnExhausted: 'pause',
  }
}

/** 字段级错误，形状与契约 §1.2 的 `fieldErrors` 一致（同一渲染路径）。 */
export type WizardFieldError = { field: string; message: string }

/**
 * 校验某一步。返回全部错误（不是第一个）：
 * 向导一次提交多个字段，只报第一个会让用户来回提交三、四次
 * （与后端 `Validate()` 一次报全部的约定一致）。
 */
export function validateStep(draft: WizardDraft, step: WizardStep): WizardFieldError[] {
  const errors: WizardFieldError[] = []

  if (step === 'basic') {
    const name = draft.name.trim()
    if (name === '') {
      errors.push({ field: 'name', message: '必填' })
    } else if ([...name].length > MAX_NAME_LENGTH) {
      errors.push({ field: 'name', message: `不能超过 ${MAX_NAME_LENGTH} 个字符` })
    }
    if (!TARGET_KINDS.includes(draft.targetKind)) {
      errors.push({ field: 'targetKind', message: '只能是 sft 或 grpo' })
    }
  }

  if (step === 'coverage') {
    for (const [field, label] of [
      ['domains', '领域数'],
      ['directionsPerDomain', '每领域方向数'],
      ['questionsPerDirection', '每方向问题数'],
    ] as const) {
      const check = checkPositiveInteger(draft[field])
      if (!check.ok) {
        errors.push({ field: `coverage.${field}`, message: `${label}${check.message}` })
      }
    }
    const pilot = checkPositiveInteger(draft.pilotSize)
    if (!pilot.ok) {
      errors.push({ field: 'pilotSize', message: `试制数量${pilot.message}` })
    } else if (pilot.value < MIN_PILOT_SIZE || pilot.value > MAX_PILOT_SIZE) {
      errors.push({
        field: 'pilotSize',
        message: `必须在 ${MIN_PILOT_SIZE}–${MAX_PILOT_SIZE} 之间`,
      })
    }
  }

  if (step === 'quality') {
    if (draft.acceptanceRateTarget.trim() !== '') {
      const target = Number(draft.acceptanceRateTarget)
      if (!Number.isFinite(target) || target < 0 || target > 1) {
        errors.push({ field: 'quality.acceptanceRateTarget', message: '必须在 0–1 之间' })
      }
    }
    if (draft.budgetCurrency !== 'CNY') {
      errors.push({ field: 'budget.currency', message: '当前只支持 CNY' })
    }
    if (draft.budgetLimitMinor.trim() !== '') {
      const limit = Number(draft.budgetLimitMinor)
      if (!Number.isInteger(limit) || limit < 0) {
        errors.push({ field: 'budget.limitMinor', message: '必须是不小于 0 的整数（单位：分）' })
      } else if (limit !== 0 && limit < MIN_BUDGET_LIMIT_MINOR) {
        // 与后端一致：显式 0 表示「不设上限」，因此 0 不被下限拦住。
        errors.push({
          field: 'budget.limitMinor',
          message: `至少为 ${MIN_BUDGET_LIMIT_MINOR}（1 元，单位为分）；填 0 表示不设上限`,
        })
      }
    }
    if (draft.budgetOnExhausted !== 'pause' && draft.budgetOnExhausted !== 'stop') {
      errors.push({ field: 'budget.onExhausted', message: '只能是 pause 或 stop' })
    }
  }

  return errors
}

/** 校验全部步骤（提交前用）。 */
export function validateAll(draft: WizardDraft): WizardFieldError[] {
  return WIZARD_STEPS.flatMap((step) => validateStep(draft, step))
}

/** 判断一个字段是否落在某一步，用于把服务端错误**聚焦到字段**。 */
export function stepForField(field: string): WizardStep {
  if (field.startsWith('coverage.') || field === 'pilotSize') return 'coverage'
  if (
    field.startsWith('quality.') ||
    field.startsWith('budget.') ||
    field === 'budget.limitMinor'
  ) {
    return 'quality'
  }
  return 'basic'
}

/**
 * 把服务端的 fieldErrors 归到步骤上，并返回应该跳转到的第一步。
 *
 * 为什么需要它：服务端是最终判定者，它可能返回前端校验没覆盖的字段错误
 * （例如后端新增了校验）。如果只是把错误显示在当前页，用户会看到
 * 「本页没有这个字段」的错误 —— 而不知道去哪改。
 */
export function groupServerErrors(
  fieldErrors: WizardFieldError[],
): { byStep: Record<WizardStep, WizardFieldError[]>; firstStep: WizardStep | null } {
  const byStep: Record<WizardStep, WizardFieldError[]> = { basic: [], coverage: [], quality: [] }
  for (const error of fieldErrors) {
    byStep[stepForField(error.field)].push(error)
  }
  const firstStep = WIZARD_STEPS.find((step) => byStep[step].length > 0) ?? null
  return { byStep, firstStep }
}

type IntegerCheck = { ok: true; value: number } | { ok: false; message: string }

/**
 * 正整数校验。
 *
 * 刻意**不使用** `Number.parseInt('12abc') === 12` 这种宽松解析：
 * 它会静默接受用户输错的「12a」，而用户看到的是「已填写 12」，
 * 提交后却得到与预期不同的数量。宁可报「必须是整数」。
 */
export function checkPositiveInteger(raw: string): IntegerCheck {
  const text = raw.trim()
  if (text === '') return { ok: false, message: '必填' }
  if (!/^\d+$/.test(text)) return { ok: false, message: '必须是整数' }
  const value = Number(text)
  if (!Number.isSafeInteger(value)) return { ok: false, message: '数值过大' }
  if (value < 1) return { ok: false, message: '必须大于等于 1' }
  return { ok: true, value }
}

/**
 * 计划问题数 = n × m × x（契约 §2.1）。
 *
 * 界面必须把它标成**计划量**：旧实现用 answerVariants/rewardVariants 偷乘，
 * 于是「完成度」在一件都没做的时候就已显示 20%（§2.1 明确禁止）。
 */
export function plannedQuestions(draft: WizardDraft): number {
  const domains = checkPositiveInteger(draft.domains)
  const directions = checkPositiveInteger(draft.directionsPerDomain)
  const questions = checkPositiveInteger(draft.questionsPerDirection)
  if (!domains.ok || !directions.ok || !questions.ok) return 0
  return domains.value * directions.value * questions.value
}

/** 转成后端 `CreateProjectInput` 的请求体。 */
export type CreateProjectRequest = {
  name: string
  goal: string
  targetKind: TargetKind
  coverage: { domains: number; directionsPerDomain: number; questionsPerDirection: number }
  pilotSize: number
  quality: { acceptanceRateTarget?: number }
  budget: { currency: string; limitMinor?: number; onExhausted: 'pause' | 'stop' }
}

export function toCreateProjectRequest(draft: WizardDraft): CreateProjectRequest {
  const domains = checkPositiveInteger(draft.domains)
  const directions = checkPositiveInteger(draft.directionsPerDomain)
  const questions = checkPositiveInteger(draft.questionsPerDirection)
  const pilot = checkPositiveInteger(draft.pilotSize)

  const request: CreateProjectRequest = {
    name: draft.name.trim(),
    goal: draft.goal.trim(),
    targetKind: draft.targetKind,
    coverage: {
      domains: domains.ok ? domains.value : 1,
      directionsPerDomain: directions.ok ? directions.value : 1,
      questionsPerDirection: questions.ok ? questions.value : 1,
    },
    pilotSize: pilot.ok ? pilot.value : 1,
    quality: {},
    budget: {
      currency: draft.budgetCurrency,
      onExhausted: draft.budgetOnExhausted,
    },
  }

  const target = draft.acceptanceRateTarget.trim()
  if (target !== '') {
    request.quality.acceptanceRateTarget = Number(target)
  }

  const limit = draft.budgetLimitMinor.trim()
  if (limit !== '') {
    request.budget.limitMinor = Number(limit)
  }

  return request
}

// ---------------------------------------------------------------------------
// 草稿持久化
// ---------------------------------------------------------------------------

/** 草稿的存储版本。结构变化时递增，避免读到旧结构的半份草稿。 */
export const DRAFT_VERSION = 1

/**
 * 草稿的存储键。
 *
 * **必须包含用户 ID**：T10 验收项要求「刷新恢复草稿且绑定当前用户，
 * 退出账号清理」。只用固定键会让下一个登录的用户看到上一个人的项目名称
 * 与目标 —— 那既是隐私问题，也会让「新建项目」直接建出属于别人的内容。
 */
export function draftStorageKey(userId: number | string): string {
  return `studio.wizard.draft.v${DRAFT_VERSION}.u${userId}`
}

/** 存储接口。抽出来是为了让测试注入内存实现（不需要浏览器）。 */
export type DraftStorage = {
  getItem: (key: string) => string | null
  setItem: (key: string, value: string) => void
  removeItem: (key: string) => void
}

/** 序列化草稿。 */
export function serializeDraft(draft: WizardDraft): string {
  return JSON.stringify({ version: DRAFT_VERSION, draft })
}

/**
 * 反序列化草稿。
 *
 * 结构不合法时返回 null（调用方回退到空白草稿）而不是抛错：
 * 一份坏草稿不应该让「新建项目」这个入口直接打不开。
 * 但也**不**做「尽力修补」——半个草稿会静默带上错误的数值。
 */
export function deserializeDraft(raw: string | null): WizardDraft | null {
  if (!raw) return null
  try {
    const parsed = JSON.parse(raw) as { version?: number; draft?: Partial<WizardDraft> }
    if (parsed.version !== DRAFT_VERSION || !parsed.draft) return null
    const base = emptyDraft()
    const merged: WizardDraft = { ...base }
    for (const key of Object.keys(base) as (keyof WizardDraft)[]) {
      const value = parsed.draft[key]
      if (typeof value === 'string') {
        // 只接受字符串字段：类型不符时保留默认值，不强行转换。
        ;(merged as Record<string, unknown>)[key] = value
      }
    }
    if (!TARGET_KINDS.includes(merged.targetKind)) merged.targetKind = 'sft'
    if (merged.budgetOnExhausted !== 'pause' && merged.budgetOnExhausted !== 'stop') {
      merged.budgetOnExhausted = 'pause'
    }
    return merged
  } catch {
    return null
  }
}

/** 读取草稿（不存在或损坏时返回空白草稿）。 */
export function loadDraft(storage: DraftStorage, userId: number | string): WizardDraft {
  return deserializeDraft(storage.getItem(draftStorageKey(userId))) ?? emptyDraft()
}

/** 保存草稿。 */
export function saveDraft(storage: DraftStorage, userId: number | string, draft: WizardDraft): void {
  storage.setItem(draftStorageKey(userId), serializeDraft(draft))
}

/**
 * 清理草稿。
 *
 * 在「退出账号」与「创建成功」两处都要调用：
 *   * 退出账号：T10 验收项「退出账号清理」；
 *   * 创建成功：否则下次进向导会看到上一次的内容，用户很容易以为自己
 *     还没创建成功而再建一次（幂等键能防重复创建，但会让人困惑）。
 */
export function clearDraft(storage: DraftStorage, userId: number | string): void {
  storage.removeItem(draftStorageKey(userId))
}

/**
 * 是否允许直接切换目标类型（SFT ↔ GRPO）。
 *
 * 契约与后端的一致性要求：`target_kind` 是**不可变**的（蓝图节点、样本 schema、
 * 质量量表与发布编码都按它派生）。因此运行过之后不允许直接切换，
 * 必须复制为新项目（T10 验收项原文）。
 *
 * 这里把它做成一个函数而不是内联判断，是为了让界面与测试用同一条规则。
 */
export function targetKindSwitchable(hasAnyBatch: boolean): boolean {
  return !hasAnyBatch
}
