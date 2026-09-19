# 第二轮之后：后续功能计划

> 输入来源：
> 1. `docs/plans/round2-acceptance-report.md`（本轮验收报告）
> 2. `docs/plans/round2-lane-board.md` §10「已合并但仍有未尽事项的 lane」
> 3. 各 lane 独立评审者提出的 P2 finding
> 4. `docs/guides/web-user-ux-review.md`（R17 的真实对比与改进建议）
> 5. `docs/plans/round2-prompt-template-contamination.md`（已定位未根治的风险）

---

## 0. 优先级定义

| 级别 | 含义 |
| --- | --- |
| **P0** | 会造成数据错误或用户无法完成核心任务，且当前**可复现** |
| **P1** | 明确降低可用性/可维护性，或让缺陷**无法被及时发现**（质量网缺口） |
| **P2** | 体验改进、文档完善、代码整洁 |

---

## 1. P0：建议立即开工

### 1.1 消除「外部写入者静默接管全局配置」的风险

**问题**（本轮已定位、已缓解、**未根治**）：

`prompt_templates` 与 `model_providers` 都是**全局单值语义**且**无归属概念**：
- 任何写入者写一条 `is_active=true` 的模板就接管了全局提示词
  （`GetActivePromptByStage` 取「最近更新的活跃模板」）；
- 本轮实测：自动审查 harness 留下的 7 条「请生成测试问题。」模板
  **让模型返回了与任务完全无关的通用问答清单**；
- 同源问题：harness 留下的空 provider（无 model/无 key）出现在评估裁判候选里，
  用户选了之后运行才失败（已由 PR #128 修了「候选与执行侧口径一致」，
  但**没有修「为什么这些空 provider 会存在并被当作候选」**）。

**为什么是 P0**：它是**跨运行污染**，污染的是**行为**而不是数据行数。
不会让任何测试失败于自身，只会让**别人的**运行产出错的数据，且**静默**。
本轮能发现纯属侥幸（端到端验收恰好校验了产出的语义）。

**建议方案**（三选一或组合，需要设计取舍）：

| 方案 | 做法 | 取舍 |
| --- | --- | --- |
| A. 作用域/归属 | 给模板与 provider 加 `scope`（global / test / dataset）与 `owner`，查询时按 scope 过滤 | 隔离会降低模板复用性；需要迁移 + 回填 |
| B. 测试写入自动过期 | 测试写入的配置带 TTL 或可识别前缀，启动时清理 | 依赖外部 harness 配合（本仓库单方面改不了全部） |
| C. 显式启用语义 | 全局生效必须**显式** `is_default=true`，且同一 stage 只允许一条 | 改动小；但「谁把上一条取消」仍需约定 |

**验收标准**：
1. 一条测试写入的配置**不可能**改变生产行为；
2. 有一个测试证明「注入脏配置后，生产链路行为不变」。

---

## 2. P1：质量网补强（本轮的元教训）

本轮的多个缺陷**共同根因是「质量网漏了」，而不是代码写错**：

| 已补的机制 | 拦住了什么 |
| --- | --- |
| CI 执行 UI 守卫（PR #124） | 8 个 `test/l15_*.mjs` 此前**没有任何 CI job 执行** |
| tsc 死代码门禁（PR #99） | 死代码是合法 TypeScript，CI 看不见（实测积累 7 个） |
| 状态值覆盖测试（PR #114） | 「后端新增状态、前端不认识」 |
| 用户文案黑名单（PR #122） | 内部术语泄漏到用户界面 |
| 下游状态白名单（PR #116） | `!= "failed"` 在新增取值时静默放行 |

### 2.1 仍有缺口：前端渲染腿只拿到 Spin 占位

**问题**（R10 评审 Finding 3）：`App.tsx` 在 `sessionLoading` 为真时只渲染 Spin 占位，
而 `renderToStaticMarkup` **不执行 `useEffect`**，因此部分 UI 守卫的「渲染腿」
实际渲染的是占位符 —— 它只能证明「组件不抛异常」，**证明不了「能力面板真的渲染出来」**。

R1 的 `test/l15_stage_routes.mjs` 已经用 esbuild `onLoad` 插件预置会话状态解决了这个问题
（父代理复核过该做法），但其余脚本未统一。

**建议**：把「会话预置」抽成共享工具（建议路径 test/lib/seedSession.mjs），
让全部 UI 守卫的渲染腿都拿到**真实页面**而不是占位符。

**验收标准**：删掉某个能力面板的渲染代码，对应守卫必须失败。

### 2.2 仍有缺口：变异用例的恒真式写法

**问题**（R10 评审 Finding 3）：形如
`record('变异：… -> 断言失败', !mutated.includes(label))` 的断言是**恒真式**：
若源码本来就没有该 label，`replaceAll` 是 no-op，`!false = true` 直接 PASS。

wave5 的任务书已明确禁止该写法并要求「谓词函数 + 喂入变异源码」模式，
但**早期 lane（R1/R10）里仍存在恒真式用例**。

**建议**：写一个元测试扫描 `test/l15_*.mjs`，检出「变异断言的右侧只依赖被变异源码」的模式。

### 2.3 建议：把「真实 LLM 端到端」纳入定期验证

`test/integration/r3_real_provider_test.go` 带 `//go:build integration`，
**不进入** `go test ./...`，因此「真实 provider 不会误伤」这条结论**无 CI 自动化保障**。

**建议**：加一个 nightly / 手动触发的 workflow 跑 `-tags=integration` 与
`test_acceptance_7requirements.py`，并把结果写进仓库（例如 badge 或 artifact）。

---

## 3. P1：功能完整性

### 3.1 阶段页缺「下一步」按钮（R17 评审 §1，标为必须修）

**问题**：一次流水线需要用户**人工逐一点 5 次启动**，且阶段页**没有「下一步」**，
完成一次流水线需 5 次往返。`功能说明.txt` 明确要求「系统设计必须符合人机交互习惯」。

**建议**：阶段页在「本阶段已完成」时提供「下一步」主按钮（跳到下一阶段页并保留任务上下文
—— 后者已由 PR #123 的 `?taskId=` 机制支持）。

**验收标准**：真实浏览器走一遍，从 draft 到 export 的点击次数从 5 次往返降到 ≤2 次。

### 3.2 `question_generator_v2` 与 legacy 的语义差异未收敛

**问题**：`generateQuestionsV2`（支持 `questionsPerDirection` + `difficultyMix`）
与 legacy `generateQuestions` 语义不同，UI 目前仍走 legacy（R13 的记录里说明过这个判断）。
`test/l15_capability_entries.mjs` 的孤儿方法检测里 `generateQuestionsV2` 仍是孤儿。

**建议**：明确「v2 是唯一入口」，把 legacy 路径标记为 deprecated 或删除；
若保留两条，必须写明何时用哪条。

### 3.3 `App.tsx` 仍有 9 个孤儿 API 方法（PR #92）

已从 21 降到 9，剩余 9 个各有正当理由（如 `setEvalRunJudges` 属评估模块内部、
`saveExportMapping` 属管理后台配置）。建议逐条决策「接线 / 删除 / 保留并注明理由」，
并在 CI 里断言「孤儿方法数不增加」（防止回退）。

---

## 4. P2：体验与整洁

### 4.1 评估运行列表的标签编号错标（R10 评审 Finding 6）

`R1 评估裁判配置` 的标签里 `R1` 是错的 —— 评估属需求 6 / lane L7–L10，
而 `R1` 在 `docs/plans/eval-and-cleaning-plan.md` 里指「关键词 → n 领域 → m 方向」。

**建议**：改为「评估裁判配置（需求 6 / 多 LLM 互评）」。

### 4.2 `--with-api` 的清理不在 `finally`（R10 评审 Finding 2）

`test/l15_capability_entries.mjs` 的探针数据集删除在循环之后、`try` 之内，
异常会被外层 `catch` 吞掉 → 可能向共享库遗留数据。

**建议**：把清理提到 `finally`（其余脚本已用 `finally`，唯独数据库清理没有）。

### 4.3 测试残留未使用 import（R10 评审 Finding 7）

`test/l15_capability_entries.mjs` 有 `import { tmpdir } from 'node:os'` 但未使用。
`.mjs` 不受 tsc 管辖，因此 `noUnusedLocals` 覆盖不到。

**建议**：给 `.mjs` 加一个轻量 lint（或用 `node --check` + 自定义扫描）。

### 4.4 前端页面结构文档的持续同步（R17 交付）

`docs/guides/web-user-page-map.md` 的现状来自真实浏览器采集，
但**页面改动后不会自动更新**。建议把采集脚本纳入 CI（或定期跑），
并在 PR 模板里提示「改了页面结构请重跑采集」。

---

## 5. 本轮已知但不建议现在做的事（附理由）

| 项 | 不建议的理由 |
| --- | --- |
| 给 `reasoning` 阶段改成「读 provider 的 `timeout_seconds`」 | 本阶段调用链（`ResolveProvider` → `ProviderConfig`）不携带该字段，为此扩展 6 个调用点签名属大改；当前 300s 与 provider 配置同值，行为一致 |
| 把 `clear_demo_data.sh` 加限制 | 它是有意为之的运维工具（README 有用途说明）；限制它超出缺陷治理范围。改为在文档与 lane 冷启动包里明确「lane 禁止执行」 |
| 为「共享库数据损失事故」做数据恢复 | 不承载交付价值（本地开发卷），且回滚会破坏已验证的迁移状态；已如实登记为风险 |

---

## 6. 建议的开工顺序

```text
第 1 步（P0）  1.1 全局配置的作用域/归属 —— 消除静默污染
第 2 步（P1）  2.1 会话预置共享工具 + 2.2 变异恒真式元测试
第 3 步（P1）  3.1 阶段页「下一步」按钮（人机交互硬要求）
第 4 步（P1）  2.3 nightly 真实 LLM 端到端
第 5 步（P1）  3.2 / 3.3 语义收敛与孤儿方法决策
第 6 步（P2）  4.1–4.4 体验与整洁
```

**每一条都应当配套**：
1. 一个能在 CI 跑的守卫（无容器可跑，exit code 决定成败）；
2. 一条变异自证（改坏它，守卫必须失败）——**且不得写成恒真式**；
3. 端到端证据（真实 LLM / 真实浏览器），若缺输入则如实记「输入缺失」，不伪造。
