import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { useNavigate } from 'react-router-dom'
import { Banner, Button, Card, Input, InputNumber, Radio, RadioGroup, Select, Typography } from '@douyinfe/semi-ui'
import { ArrowLeft, ArrowRight, CheckCircle2, Info } from 'lucide-react'
import { client } from '../../lib/api'
import { newIdempotencyKey } from '../../lib/api/studio'
import {
  MAX_PILOT_SIZE,
  MIN_BUDGET_LIMIT_MINOR,
  MIN_PILOT_SIZE,
  STEP_PATHS,
  clearDraft,
  emptyDraft,
  groupServerErrors,
  loadDraft,
  plannedQuestions,
  saveDraft,
  toCreateProjectRequest,
  validateStep,
  type TargetKind,
  type WizardDraft,
  type WizardFieldError,
  type WizardStep,
} from '../wizard'
import { projectHref } from '../StudioLayout'

/**
 * 三步项目向导（Issue #160 T10）：`/new` → `/new/coverage` → `/new/quality`。
 *
 * 三条来自 T10 验收项的性质，都体现在这个组件里：
 *
 *  1. **「上一步保留草稿」**：草稿在每次输入时就写入 localStorage（按用户 ID 分键），
 *     因此刷新、前进/后退、关掉标签再回来都能恢复。用「提交时才存」会在
 *     用户按刷新时丢掉整份填写。
 *  2. **「创建重复点击只建一次」**：提交带 `Idempotency-Key`，且该键在
 *     本次页面生命周期内**保持不变** —— 每次点击换一个键就不再是「同键同请求」，
 *     于是会真的建出第二个项目。
 *  3. **「无配置时可设计」**：向导**不**查询也不要求模型连接或存储配置
 *     （契约 §2.1「无模型调用」）。最终提交只建草稿。
 */

export type NewProjectWizardProps = {
  step: WizardStep
  userId: number
}

type StepCopy = {
  title: string
  description: string
}

const STEP_COPY: Record<WizardStep, StepCopy> = {
  basic: {
    title: '第一步：这是什么项目',
    description:
      '只填意图与目标类型。这一步不会调用模型，也不要求已经配置好连接 —— 设计与运行是分开的。',
  },
  coverage: {
    title: '第二步：目标量与覆盖',
    description:
      'n × m × x 是计划量，不是已产出。试制数量是小批验证用的，不会影响后面的扩量范围。',
  },
  quality: {
    title: '第三步：质量门槛与预算',
    description:
      '接纳率是「接纳数 / 纳入检查的样本版本数」。预算以分为单位，留空表示不设上限。',
  },
}

export function NewProjectWizard({ step, userId }: NewProjectWizardProps) {
  const navigate = useNavigate()
  const { Title, Text } = Typography

  const [draft, setDraft] = useState<WizardDraft>(() => emptyDraft())
  const [fieldErrors, setFieldErrors] = useState<WizardFieldError[]>([])
  const [serverError, setServerError] = useState<string | null>(null)
  const [submitting, setSubmitting] = useState(false)
  const [restored, setRestored] = useState(false)

  // 幂等键在**整个向导会话**内保持稳定：用户反复点击「创建项目」时，
  // 服务端据此判定「同键同请求」并回放原结果，而不是建出第二个项目。
  const idempotencyKey = useRef(newIdempotencyKey())
  // 已经创建成功的项目 ID：避免第二次点击时重复提交（也在服务端幂等之外
  // 再挡一层，因为用户看到的是「点两次」而不是「两个请求」）。
  const createdProjectId = useRef<number | null>(null)

  useEffect(() => {
    const storage = browserStorage()
    const loaded = loadDraft(storage, userId)
    setDraft(loaded)
    setRestored(loaded.name !== '' || loaded.goal !== '')
  }, [userId])

  const update = useCallback(
    <K extends keyof WizardDraft>(key: K, value: WizardDraft[K]) => {
      setDraft((previous) => {
        const next = { ...previous, [key]: value }
        // 每次输入即保存：刷新丢草稿是 T10 明确要避免的形态。
        saveDraft(browserStorage(), userId, next)
        return next
      })
      // 该字段的错误随输入清除，避免用户改好了还看着红字。
      setFieldErrors((previous) => previous.filter((error) => error.field !== key))
    },
    [userId],
  )

  const errorsByField = useMemo(() => {
    const map = new Map<string, string>()
    for (const error of fieldErrors) map.set(error.field, error.message)
    return map
  }, [fieldErrors])

  const goToStep = useCallback(
    (target: WizardStep) => {
      const errors = validateStep(draft, step)
      if (errors.length > 0) {
        // 校验失败时**停在本步**并把错误落到字段上（T10 验收项
        // 「必填/整数/范围错误聚焦字段」）。
        setFieldErrors(errors)
        focusFirstInvalid(errors[0].field)
        return
      }
      setFieldErrors([])
      navigate(STEP_PATHS[target])
    },
    [draft, navigate, step],
  )

  const submit = useCallback(async () => {
    if (createdProjectId.current !== null) {
      // 已经建好了：直接进入，不再发请求。这是「重复点击只建一次」的
      // 最后一道防线（前两道是幂等键与服务端唯一约束）。
      navigate(projectHref('project.overview', createdProjectId.current))
      return
    }
    const errors = validateStep(draft, step)
    if (errors.length > 0) {
      setFieldErrors(errors)
      focusFirstInvalid(errors[0].field)
      return
    }

    setSubmitting(true)
    setServerError(null)
    try {
      const response = await client.post<{ data: { id: number } }>(
        '/v1/projects',
        toCreateProjectRequest(draft),
        { headers: { 'Idempotency-Key': idempotencyKey.current } },
      )
      const projectId = response.data?.data?.id
      if (!projectId) {
        // 服务端返回了 201 但没有项目 ID：这是契约破坏。显式报错而不是
        // 猜一个跳转目标 —— 跳到错误的项目比报错更糟。
        throw new Error('创建成功但没有返回项目 ID，请联系管理员并提供 requestId')
      }
      createdProjectId.current = projectId
      // 创建成功后清掉草稿：否则下次进向导会看到上一次的内容，
      // 用户很容易以为自己没建成功而再建一次。
      clearDraft(browserStorage(), userId)
      navigate(projectHref('project.overview', projectId))
    } catch (error) {
      const apiError = error as {
        statusCode?: number
        message?: string
        response?: { data?: { error?: { fieldErrors?: WizardFieldError[]; message?: string } } }
      }
      const serverFieldErrors = apiError.response?.data?.error?.fieldErrors
      if (serverFieldErrors && serverFieldErrors.length > 0) {
        // 服务端是最终判定者。把它的字段错误归到步骤上，并跳到
        // **最早出问题的那一步** —— 否则用户会看到「本页没有这个字段」的错误。
        const grouped = groupServerErrors(serverFieldErrors)
        setFieldErrors(serverFieldErrors)
        setServerError(apiError.response?.data?.error?.message ?? '提交未通过校验')
        if (grouped.firstStep && grouped.firstStep !== step) {
          navigate(STEP_PATHS[grouped.firstStep])
        }
      } else {
        setServerError(apiError.message ?? '创建项目失败，请稍后重试')
      }
    } finally {
      setSubmitting(false)
    }
  }, [draft, navigate, step, userId])

  const stepIndex = ['basic', 'coverage', 'quality'].indexOf(step) + 1

  return (
    <div className="console-page wizard-page" data-studio-wizard-step={step}>
      <div className="console-page__header">
        <div>
          <Text type="tertiary" size="small">
            第 {stepIndex} / 3 步
          </Text>
          <Title heading={4} className="!mb-1">
            {STEP_COPY[step].title}
          </Title>
          <Text type="tertiary">{STEP_COPY[step].description}</Text>
        </div>
      </div>

      {restored && step === 'basic' ? (
        <Banner
          type="info"
          closeIcon={null}
          description="已恢复上次未提交的草稿。草稿按当前账号保存，切换账号不会看到别人的内容。"
        />
      ) : null}

      {serverError ? (
        <Banner type="danger" closeIcon={null} description={serverError} />
      ) : null}

      <Card className="console-card" bodyStyle={{ padding: 20 }}>
        {step === 'basic' ? (
          <BasicStep draft={draft} errors={errorsByField} update={update} />
        ) : null}
        {step === 'coverage' ? (
          <CoverageStep draft={draft} errors={errorsByField} update={update} />
        ) : null}
        {step === 'quality' ? (
          <QualityStep draft={draft} errors={errorsByField} update={update} />
        ) : null}
      </Card>

      <div className="wizard-actions">
        {step !== 'basic' ? (
          <Button
            icon={<ArrowLeft size={14} />}
            onClick={() => {
              const order: WizardStep[] = ['basic', 'coverage', 'quality']
              navigate(STEP_PATHS[order[order.indexOf(step) - 1]])
            }}
          >
            上一步
          </Button>
        ) : (
          <span />
        )}
        {step !== 'quality' ? (
          <Button
            theme="solid"
            type="primary"
            icon={<ArrowRight size={14} />}
            onClick={() => goToStep(step === 'basic' ? 'coverage' : 'quality')}
          >
            下一步
          </Button>
        ) : (
          <Button
            theme="solid"
            type="primary"
            icon={<CheckCircle2 size={14} />}
            loading={submitting}
            onClick={() => void submit()}
          >
            创建项目草稿
          </Button>
        )}
      </div>
    </div>
  )
}

type StepProps = {
  draft: WizardDraft
  errors: Map<string, string>
  update: <K extends keyof WizardDraft>(key: K, value: WizardDraft[K]) => void
}

function BasicStep({ draft, errors, update }: StepProps) {
  const { Text } = Typography
  return (
    <div className="wizard-fields">
      <Field label="项目名称" required error={errors.get('name')} fieldId="wizard-name">
        <Input
          id="wizard-name"
          value={draft.name}
          onChange={(value) => update('name', value)}
          placeholder="例如：冷链问答数据"
          maxLength={200}
        />
      </Field>
      <Field label="目标" error={errors.get('goal')} fieldId="wizard-goal">
        <Input
          id="wizard-goal"
          value={draft.goal}
          onChange={(value) => update('goal', value)}
          placeholder="例如：交付可用于 SFT 的冷链领域问答"
        />
      </Field>
      <Field label="训练类型" error={errors.get('targetKind')} fieldId="wizard-target-kind">
        <RadioGroup
          id="wizard-target-kind"
          type="button"
          value={draft.targetKind}
          onChange={(event) => update('targetKind', event.target.value as TargetKind)}
        >
          <Radio value="sft">SFT（监督微调）</Radio>
          <Radio value="grpo">GRPO（判据与档位）</Radio>
        </RadioGroup>
        <Text type="tertiary" size="small" className="block mt-1">
          训练类型在项目运行后不可直接切换：蓝图节点、样本结构与发布格式都由它派生，
          要换类型必须复制为新项目。
        </Text>
      </Field>
    </div>
  )
}

function CoverageStep({ draft, errors, update }: StepProps) {
  const { Text } = Typography
  const planned = plannedQuestions(draft)
  return (
    <div className="wizard-fields">
      <div className="wizard-triple">
        <Field label="领域数（n）" required error={errors.get('coverage.domains')} fieldId="wizard-domains">
          <InputNumber
            id="wizard-domains"
            value={draft.domains === '' ? undefined : Number(draft.domains)}
            min={1}
            onChange={(value) => update('domains', value === undefined ? '' : String(value))}
          />
        </Field>
        <Field
          label="每领域方向数（m）"
          required
          error={errors.get('coverage.directionsPerDomain')}
          fieldId="wizard-directions"
        >
          <InputNumber
            id="wizard-directions"
            value={draft.directionsPerDomain === '' ? undefined : Number(draft.directionsPerDomain)}
            min={1}
            onChange={(value) => update('directionsPerDomain', value === undefined ? '' : String(value))}
          />
        </Field>
        <Field
          label="每方向问题数（x）"
          required
          error={errors.get('coverage.questionsPerDirection')}
          fieldId="wizard-questions"
        >
          <InputNumber
            id="wizard-questions"
            value={draft.questionsPerDirection === '' ? undefined : Number(draft.questionsPerDirection)}
            min={1}
            onChange={(value) => update('questionsPerDirection', value === undefined ? '' : String(value))}
          />
        </Field>
      </div>

      <div className="wizard-hint">
        <Info size={14} aria-hidden />
        <Text type="tertiary" size="small">
          计划问题数：{planned}（= n × m × x，<strong>计划量</strong>，不是当前已产出）
        </Text>
      </div>

      <Field label="试制数量" required error={errors.get('pilotSize')} fieldId="wizard-pilot">
        <InputNumber
          id="wizard-pilot"
          value={draft.pilotSize === '' ? undefined : Number(draft.pilotSize)}
          min={MIN_PILOT_SIZE}
          max={MAX_PILOT_SIZE}
          onChange={(value) => update('pilotSize', value === undefined ? '' : String(value))}
        />
        <Text type="tertiary" size="small" className="block mt-1">
          取值范围 {MIN_PILOT_SIZE}–{MAX_PILOT_SIZE}。试制不会覆盖生产内容。
        </Text>
      </Field>
    </div>
  )
}

function QualityStep({ draft, errors, update }: StepProps) {
  const { Text } = Typography
  return (
    <div className="wizard-fields">
      <Field
        label="接纳率目标"
        error={errors.get('quality.acceptanceRateTarget')}
        fieldId="wizard-acceptance"
      >
        <Input
          id="wizard-acceptance"
          value={draft.acceptanceRateTarget}
          onChange={(value) => update('acceptanceRateTarget', value)}
          placeholder="0.85"
        />
        <Text type="tertiary" size="small" className="block mt-1">
          0–1 之间。接纳率 = 接纳数 / 纳入检查的样本版本数；没有纳入检查时显示「无结论」。
        </Text>
      </Field>

      <Field label="预算币种" error={errors.get('budget.currency')} fieldId="wizard-currency">
        <Select
          id="wizard-currency"
          value={draft.budgetCurrency}
          onChange={(value) => update('budgetCurrency', String(value))}
          optionList={[{ value: 'CNY', label: 'CNY（人民币）' }]}
        />
      </Field>

      <Field label="预算上限（分）" error={errors.get('budget.limitMinor')} fieldId="wizard-limit">
        <Input
          id="wizard-limit"
          value={draft.budgetLimitMinor}
          onChange={(value) => update('budgetLimitMinor', value)}
          placeholder="留空或填 0 表示不设上限"
        />
        <Text type="tertiary" size="small" className="block mt-1">
          以「分」为单位的整数。非 0 时至少为 {MIN_BUDGET_LIMIT_MINOR}（1 元）。
        </Text>
      </Field>

      <Field label="额度用尽时" error={errors.get('budget.onExhausted')} fieldId="wizard-on-exhausted">
        <RadioGroup
          id="wizard-on-exhausted"
          type="button"
          value={draft.budgetOnExhausted}
          onChange={(event) =>
            update('budgetOnExhausted', event.target.value === 'stop' ? 'stop' : 'pause')
          }
        >
          <Radio value="pause">暂停（保留在途，阻止新提交）</Radio>
          <Radio value="stop">停止（不再自动执行）</Radio>
        </RadioGroup>
      </Field>
    </div>
  )
}

/**
 * 字段包装：标签 + 错误 + **可聚焦的字段 ID**。
 *
 * `fieldId` 不是装饰：校验失败时要能把焦点移到首个无效字段
 *（T10 验收项「错误聚焦字段」）。靠 class 选择器聚焦在嵌套组件下
 * 很脆弱（Semi 的 InputNumber 外层是 div），因此用显式 id。
 */
function Field({
  label,
  required,
  error,
  fieldId,
  children,
}: {
  label: string
  required?: boolean
  error?: string
  fieldId: string
  children: React.ReactNode
}) {
  return (
    <div className="wizard-field" data-field={fieldId} data-invalid={error ? 'true' : undefined}>
      <label className="wizard-field__label" htmlFor={fieldId}>
        {label}
        {required ? <span className="wizard-field__required"> *</span> : null}
      </label>
      {children}
      {error ? (
        <div className="wizard-field__error" role="alert">
          {error}
        </div>
      ) : null}
    </div>
  )
}

/** 把焦点移到首个无效字段（并滚动到可见位置）。 */
function focusFirstInvalid(field: string): void {
  if (typeof document === 'undefined') return
  const container = document.querySelector(`[data-field="${field}"]`)
  if (!container) return
  const focusable = container.querySelector<HTMLElement>('input, textarea, select, [tabindex]')
  if (focusable) {
    focusable.focus()
    focusable.scrollIntoView({ block: 'center' })
  }
}

/** 浏览器 localStorage 的包装；服务端渲染或隐私模式下返回内存实现。 */
function browserStorage(): {
  getItem: (key: string) => string | null
  setItem: (key: string, value: string) => void
  removeItem: (key: string) => void
} {
  try {
    if (typeof window !== 'undefined' && window.localStorage) return window.localStorage
  } catch {
    // 隐私模式下访问 localStorage 会抛异常；回退到内存实现，
    // 使「新建项目」这个入口仍然可用（只是刷新后草稿不恢复）。
  }
  const memory = new Map<string, string>()
  return {
    getItem: (key) => memory.get(key) ?? null,
    setItem: (key, value) => void memory.set(key, value),
    removeItem: (key) => void memory.delete(key),
  }
}
