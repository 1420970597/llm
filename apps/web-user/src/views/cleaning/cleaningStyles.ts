import { useEffect } from 'react'

/**
 * 清洗模块的局部样式（L14 独占）。
 *
 * 为什么是 TS 常量而不是 .css 文件：本 lane 的文件归属只包含
 * `views/cleaning/*.tsx` 与 `views/cleaning/*.ts`，而全局 `index.css` / `styles.css`
 * 不属于本 lane，不得修改。因此把样式作为字符串随组件注入一次，
 * 所有选择器都以 `.cleaning-` 前缀命名空间隔离，不会影响其他页面。
 */
export const CLEANING_STYLES = `
.cleaning-page-head {
  display: flex;
  align-items: flex-end;
  justify-content: space-between;
  gap: 16px;
  flex-wrap: wrap;
}

.cleaning-flow {
  display: grid;
  grid-template-columns: repeat(5, minmax(0, 1fr));
  gap: 12px;
  margin-top: 16px;
}

.cleaning-flow-step {
  display: grid;
  gap: 6px;
  padding: 14px 16px;
  border-radius: 16px;
  border: 1px solid rgba(var(--semi-grey-2), 0.12);
  background: color-mix(in srgb, var(--semi-color-bg-1) 90%, white 10%);
}

.cleaning-flow-step.is-current {
  border-color: var(--semi-color-primary);
  box-shadow: 0 10px 30px rgba(0, 100, 250, 0.12);
}

.cleaning-flow-step.is-done {
  border-color: rgba(var(--semi-green-5), 0.35);
}

.cleaning-flow-step-head {
  display: flex;
  align-items: center;
  gap: 8px;
}

.cleaning-flow-index {
  display: inline-flex;
  align-items: center;
  gap: 4px;
  font-size: 12px;
  font-weight: 700;
  color: var(--semi-color-primary);
}

.cleaning-flow-detail {
  font-size: 12px;
  color: var(--semi-color-text-2);
}

.cleaning-dataset-bar {
  display: flex;
  align-items: flex-start;
  justify-content: space-between;
  gap: 20px;
  flex-wrap: wrap;
}

.cleaning-dataset-summary {
  min-width: 320px;
  flex: 1;
}

.cleaning-panel-head {
  display: flex;
  align-items: flex-start;
  justify-content: space-between;
  gap: 16px;
  flex-wrap: wrap;
}

.cleaning-run-grid {
  display: grid;
  grid-template-columns: minmax(0, 1fr) minmax(0, 1.4fr);
  gap: 16px;
}

.cleaning-import-grid {
  display: grid;
  grid-template-columns: minmax(0, 2fr) minmax(0, 1fr);
  gap: 16px;
}

.cleaning-stage-picker {
  display: grid;
  gap: 8px;
}

.cleaning-stage-option {
  display: grid;
  gap: 4px;
  padding: 10px 12px;
  border-radius: 12px;
  border: 1px solid rgba(var(--semi-grey-2), 0.12);
  cursor: pointer;
}

.cleaning-stage-option.is-checked {
  border-color: var(--semi-color-primary);
  background: rgba(var(--semi-blue-0), 0.08);
}

.cleaning-stage-label {
  font-weight: 600;
}

.cleaning-stage-desc {
  font-size: 12px;
  color: var(--semi-color-text-2);
  padding-left: 24px;
}

.cleaning-rule-picker {
  display: grid;
  gap: 8px;
}

.cleaning-rule-option {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
  padding: 12px 14px;
  border-radius: 14px;
  border: 1px solid rgba(var(--semi-grey-2), 0.12);
  background: color-mix(in srgb, var(--semi-color-bg-1) 90%, white 10%);
}

.cleaning-rule-option-main {
  display: grid;
  gap: 4px;
}

.cleaning-bar-list {
  display: grid;
  gap: 12px;
}

.cleaning-bar-row {
  display: grid;
  gap: 6px;
}

.cleaning-bar-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
}

.cleaning-bar-track {
  height: 8px;
  border-radius: 999px;
  background: rgba(var(--semi-grey-2), 0.16);
  overflow: hidden;
}

.cleaning-bar-fill {
  height: 100%;
  border-radius: 999px;
  background: linear-gradient(90deg, rgba(var(--semi-blue-5), 0.9), rgba(var(--semi-cyan-5), 0.9));
}

.cleaning-bar-sample {
  font-size: 12px;
  color: var(--semi-color-text-2);
  word-break: break-all;
}

.cleaning-severity-grid {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(120px, 1fr));
  gap: 12px;
}

.cleaning-severity-card {
  padding: 14px 16px;
  border-radius: 16px;
  border: 1px solid rgba(var(--semi-grey-2), 0.12);
  background: color-mix(in srgb, var(--semi-color-bg-1) 90%, white 10%);
}

.cleaning-severity-card.is-block {
  border-color: rgba(var(--semi-red-5), 0.35);
}

.cleaning-severity-card.is-warn {
  border-color: rgba(var(--semi-orange-5), 0.35);
}

.cleaning-severity-value {
  font-size: 28px;
  font-weight: 700;
  letter-spacing: -0.04em;
}

.cleaning-snippet {
  font-size: 12px;
  color: var(--semi-color-text-2);
  word-break: break-all;
}

@media (max-width: 1100px) {
  .cleaning-flow {
    grid-template-columns: repeat(2, minmax(0, 1fr));
  }

  .cleaning-run-grid,
  .cleaning-import-grid {
    grid-template-columns: 1fr;
  }
}
`

/**
 * 把清洗模块样式注入文档一次。
 *
 * 用 DOM 注入而不是 import './cleaningStyles.css'，是为了让本 lane 的文件归属
 * 保持在 `views/cleaning/*.ts(x)` 之内（新增全局 css 文件会触碰不属于本 lane 的构建产物清单）。
 * id 固定，重复挂载不会产生重复节点。
 */
export function useCleaningStyles(): void {
  useEffect(() => {
    const styleId = 'cleaning-l14-styles'
    if (document.getElementById(styleId)) {
      return
    }
    const element = document.createElement('style')
    element.id = styleId
    element.textContent = CLEANING_STYLES
    document.head.appendChild(element)
  }, [])
}
