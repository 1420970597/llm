# UX 改造并行任务契约（父代理维护）

## 背景

基于 issue #84（操作步骤不符合人类习惯）与 #90（页面设计说明书）的调研结论，
在分支 `ux/page-flow-improvements` 上实践改造。

## 硬性约束（违反即回滚）

1. **`apps/web-user/src/App.tsx` 由父代理独占**，子代理**禁止修改**。
   理由：该文件 3757 行，所有页面渲染都在其中，并行修改必然冲突。
2. 子代理**只能创建/修改自己名下的新文件**（见下表），不得触碰他人文件。
3. 不得修改 `lib/api.ts`（父代理独占，避免与 App.tsx 的接口同时变动）。
4. 不新增 npm 依赖（`apps/web-user/package.json` 冻结）。
5. 组件必须是**纯展示组件**：props 进、回调出，不直接调用 API、不读路由。
   这样父代理接入时无需理解内部实现。
6. 样式用 Tailwind class + 已有 CSS 变量（参考 `apps/web-user/src/styles.css`），
   不要引入新的样式体系。
7. 中文文案，与现有页面语气一致。
8. 每个组件文件必须能被 `tsc --noEmit` 通过（严格模式）。

## 并行任务划分

| 任务 | 产出文件 | 说明 |
|---|---|---|
| **A** | `apps/web-user/src/views/flow/StageNextStep.tsx` | 阶段间「下一步」导航条 |
| **B** | `apps/web-user/src/views/flow/StageProgressDetail.tsx` | 等待态的真实进度展示 |
| **C** | `apps/web-user/src/views/flow/TaskTemplatePicker.tsx` | 任务模板选择（渐进披露） |

三个文件互不重叠，可并行开发。

## 接口约定（父代理接入时依赖，必须严格遵守）

### A. StageNextStep

```tsx
export type StageNextStepProps = {
  /** 下一阶段名称，如「问题生成」 */
  nextLabel: string
  /** 下一阶段路由，如 '/console/questions' */
  nextRoute: string
  /** 当前阶段是否已完成，未完成时禁用「下一步」 */
  currentStageDone: boolean
  /** 可选：点击「下一步」时的额外副作用（父代理用于刷新数据） */
  onNavigate?: (route: string) => void
  /** 可选：当前阶段未完成时的提示文案 */
  blockedHint?: string
}
export function StageNextStep(props: StageNextStepProps): JSX.Element
```

### B. StageProgressDetail

```tsx
export type StageProgressDetailProps = {
  /** 总进度 0-100 */
  completionPercent: number
  /** 阶段列表（来自 /pipeline/progress 的 stages 字段） */
  stages: Array<{
    key: string
    label: string
    state: 'pending' | 'queued' | 'in_progress' | 'completed' | 'failed'
    count: number
    summary: string
  }>
  /** 队列深度 */
  queueDepth: number
  /** ETA 文案，如「约 3 分钟」；未知时传 '—' */
  eta: string
  /** 失败原因；有值时优先展示（对应 issue #83） */
  failureReason?: string
}
export function StageProgressDetail(props: StageProgressDetailProps): JSX.Element
```

### C. TaskTemplatePicker

```tsx
export type TaskTemplate = {
  id: string
  name: string
  description: string
  /** 预置的任务主题 */
  rootKeyword: string
  /** 预置的目标样本数 */
  targetSize: number
  /** 标签，如 ['SFT', '问答'] */
  tags: string[]
}
export type TaskTemplatePickerProps = {
  templates: TaskTemplate[]
  /** 选中模板时回调，父代理据此填充表单 */
  onSelect: (template: TaskTemplate) => void
  /** 当前选中的模板 id */
  selectedId?: string
}
export function TaskTemplatePicker(props: TaskTemplatePickerProps): JSX.Element
```

## 验收要求

- 组件可独立 `import`，不依赖 App.tsx 中的任何内部函数
- 使用 Semi UI（`@douyinfe/semi-ui`）组件保持一致视觉
- 每个组件导出**类型**与**函数组件**
- 附一段注释说明它解决 #84/#90 中的哪个具体问题
