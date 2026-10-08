# Issue #206 — 批次详情「事件时间线」漏出内部英文事件键（复核轮）

## 结论

**已修复，本轮复核确认可关单。**

修复由 `74c9153`（PR #220，2026-09-28 16:05）落地，早于本轮认领前已进入 `origin/main`；
issue 当时未被关闭，因此本轮的工作是**同条件复跑取证 + 复核守卫**，而不是再次修改代码。

## 本轮复核证据（真实栈 127.0.0.1:3210 + 真实 Chromium 1600×1000）

- 复现/验证脚本：`docs/audit/issue-206/verify-timeline.mjs`
- 修复前（issue 原始取证）：`docs/audit/issue-206/01-before.png`
  （来源 `docs/audit/usertest-20260928/screenshots/71-b1-detail.png`，
  原始报告 §「批次详情：事件时间线显示 BatchRetryFailedRequested / BatchResumed 等英文键」）
- 修复后：`docs/audit/issue-206/02-after.png`

实测读数（`after.json`）：

| 判定项 | 修复前 | 修复后 |
| --- | --- | --- |
| 可见英文事件键（`/^Batch[A-Z][A-Za-z]*$/`） | 8 行全部命中 | **0** |
| 可见内置错误码（`config_error` 等） | `config_error` 可见 | **0** |
| 事件文案示例 | `BatchRetryFailedRequested` | `批次请求重试` |
| 原始 code 去处 | 直接显示 | 保留在 `title`（排查用） |
| `detail` 可诊断字段 | 不显示 | `单元 #12 · 错误类型 配置错误 · 不可重试` |

## 修复机制（供 reviewer 核对）

服务端从**动态列表同一张表** `internal/store/activity_store.go#batchEventLabels`
经 `DescribeBatchEvent` 下发 `eventTypeLabel`（`apps/api/routes_studio_batches.go:541`），
前端 `RunPages.tsx` 消费该字段并把原始 code 移入 `title`。这样同一批事件在
「动态」与「批次详情」共享一份文案来源，不会再出现两处各自漂移。

## 守卫

`test/l15_issue197_remediation.mjs` 的 `problemsWithBatchTimelineLabels`：
断言 `RunPages` 不再裸渲染 `{event.eventType}`、必须消费 `eventTypeLabel`、
且 `model.BatchEvent` 带 `json:"eventTypeLabel"`。已附 2 条变异自证
（把时间线退回原样渲染 / 删掉服务端下发字段，断言都必须报错）。
本轮复跑 `node test/l15_issue197_remediation.mjs`：`[PASS] #206 ...` 与两条变异均通过。
