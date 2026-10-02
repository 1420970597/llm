# Issue #201 复核轮：批次「已完成」却只产出 1/4 且无恢复入口（第 1 轮）

> 代码基线 `origin/main` @ `a889b43`（含修复 PR #228）；修复前对照 `f615353`。
> 验证环境 真实容器栈 `127.0.0.1:3210` + 真实 Postgres + 真实 Chromium 1600×1000。
> 采集脚本：`test/audit/issue-201/repro.mjs`（可重跑，`--phase before|after`，默认拒绝覆盖已提交证据）。

## 结论

**已修复，本轮无代码改动。** 缺陷在 `main` 上已由 PR #228（提交 `1381667`）修复，
但 issue 从未关闭。本轮做的是**可控单变量复核**：把 `b_2` 的库内状态手工置回缺陷形态，
再等一个完整维护轮次，观察当前部署的代码会不会把它纠正回来。

> 为什么不能只「读一下现在的状态」就判定已修复：`b_2` 在 2026-09-30 就被那一轮的
> 维护循环纠正过。直接读到的 `partial_failed` 可能只是**历史遗留的正确状态**，
> 与当前部署的代码是否正确无关。因此本脚本每轮都**重新置回缺陷**再观察。

## 复现（修复前，`f615353`）

```text
[issue-201] phase=before deployed=f615353 armed=completed|4|1 units=1
  status=completed shortfall=3 可点按钮=["搜索","A","异常恢复"]
  归因覆盖率=true 本次新增纠正事件=0（历史 7 条）ok=false
  缺口卡="产出缺口：计划 4，实际产出 1，缺口 3
          计划 4，实际产出 1，缺口 3：覆盖率不足或无素材接地，请补充方向配额/素材后重跑"
```

![修复前：b_2 状态「已完成」，缺口 3，没有任何补齐入口](https://raw.githubusercontent.com/1420970597/llm/PLACEHOLDER_SHA/docs/audit/issue-201/before-b2-detail.png)

实测读数：`b_2` 计划 4 / 完成 1 / `batch_items` 只有 1 行，状态 **`completed`**；
等满一个维护轮次（45s）后状态**没有任何变化**，时间线也没有新增纠正事件 ——
即「历史终态路径」确实不会自我纠正。缺口卡还把它归因于「覆盖率不足」，
而真实原因是 3 个计划单元**从未被创建**（#208 的误导形态）。**缺陷成立。**

## 根因（修复前）

| 位置 | 形态 |
| --- | --- |
| `internal/store/batch_store.go:1574` | `case status == BatchStatusCompleted \|\| status == BatchStatusFailed:` 直接空处理 —— 终态**复核缺口**的分支不存在 |
| `internal/store/batch_store.go` | 没有 `ListDivergentBatchIDs`：即使聚合修好了，**也没有任何路径**会把已跑完的历史批次再算一次 |
| `apps/worker/studio_jobs.go` | 维护循环里没有 `reconcileDivergentBatches`：状态与事实不符的批次不会被扫描重算 |
| `internal/studio/service.go` | `BatchCapabilitiesFor('completed')` 返回全 false，于是界面上连一个可点的按钮都没有 |

## 修复（已在 `main`，本轮未改动代码）

| 位置 | 修复 |
| --- | --- |
| `RefreshBatchCounts` | 状态聚合重写为**从事实推导的不动点**：只有「无活作业且无在途单元」时才判终态，且 `completed` 也必须复核缺口 |
| 同上 | 状态被纠正时写一条带 `correctionOf` 的 `BatchPartialFailed` 事件（时间线必须能解释） |
| 同上 | 新增 `ListDivergentBatchIDs`，把「状态与事实不符」变成可扫描事实 |
| `apps/worker/studio_jobs.go` | 维护循环每 30 秒重算不一致批次（**可达性**：没有它，修复只对未来的批次生效） |
| `internal/model/batch.go` | `ShortfallNote` 从失败事实推导原因（不再写死「覆盖率不足」） |
| `apps/web-user/src/studio/pages/RunPages.tsx` | 缺口卡显示服务端原因 + 新增「补齐缺口（继续本批次）」按钮（#201 的出口） |

## 验证（修复后，`a889b43`）

```text
[issue-201] phase=after deployed=a889b43 armed=completed|4|1 units=1
  status=partial_failed shortfall=3 可点按钮=["搜索","A","异常恢复","补齐缺口（继续本批次）"]
  归因覆盖率=false 本次新增纠正事件=1 ok=true
```

![修复后：b_2 收敛为「部分完成」，出现「补齐缺口（继续本批次）」入口](https://raw.githubusercontent.com/1420970597/llm/PLACEHOLDER_SHA/docs/audit/issue-201/after-b2-detail.png)

| 判定（机器事实） | 修复前（`f615353`） | 修复后（`a889b43`） |
| --- | --- | --- |
| 置回缺陷后的状态 | `completed` | **`partial_failed`** |
| 维护轮次内新增纠正事件 | **0** | **1**（带 `correctionOf: completed`） |
| 可点操作按钮 | 仅「异常恢复」 | 「异常恢复」+ **「补齐缺口（继续本批次）」** |
| 缺口卡归因「覆盖率不足」 | **true**（误导） | **false**（改为「有 3 个计划单元没有产出（未创建或被跳过）」） |
| 缺口读数 `shortfallUnits` | 3 | 3（事实不变） |
| `pageerror` | 0 | 0 |

前后是**同栈、同账号、同视口、同路由、同数据、同脚本**：`before` 采于 `f615353` 构建的栈，
`after` 采于 `a889b43` 构建的栈。两次都先把 `b_2` 手工置回 `completed` + `completed_units=1`
（`batch_items` 本就只有 1 行），因此差异**只来自部署的代码**。

## 逐条核对 issue 的三个建议方向

| # | 建议 | 状态 | 证据 |
| --- | --- | --- | --- |
| 1 | `RefreshBatchCounts` 的终态分支必须仍然复核缺口（降级为 `partial_failed` 并写 `finished_at` + 缺口事件） | **已落地** | 状态 `completed`→`partial_failed`；维护轮次内新增带 `correctionOf` 的事件 |
| 2 | `BatchCapabilitiesFor` 对 `completed + 有缺口` 给出可操作项 | **已落地（等价方案）** | 收敛为 `partial_failed` 后出现「补齐缺口（继续本批次）」。capability 位与状态一致（`canResume: true`），比「给 completed 开一个特例」更少一处口径分叉 |
| 3 | 补一条真实 Postgres 测试：手工置为 `completed` 但 `completed_units < planned_units`，断言结果不是 `completed` | **已落地** | `internal/store/batch_store_test.go` 的 `TestRefreshBatchCountsCorrectsStaleCompletedWithShortfall`（含顺便验证「无缺口的历史 completed 不得被改写」） |

## 未收口 / 残留风险

| 项 | 状态 |
| --- | --- |
| #201 本体（终态收敛 + 恢复入口 + 回归测试） | **全部收口** |
| 关联但**独立**的子项 | #202（僵尸 running）与 #208（缺口文案误导）已在同一 PR #228 收口，且各自的 issue 已按自身证据关闭 —— 不在本 issue 范围内 |

- 无残留风险：本轮**未改动任何产品代码**，只新增取证脚本与证据。
- 复核手段的边界：本脚本依赖一个**完整的维护轮次**（30s 间隔，脚本等 45s）。
  若维护循环被关停或间隔被放大，本脚本会在 after 相位报 `ok=false` —— 那是**如实**的失败。

## 复现/验证命令

```bash
node test/audit/issue-201/repro.mjs --phase before   # 需部署 f615353（修复前）
node test/audit/issue-201/repro.mjs --phase after    # 需部署含修复的版本
```
