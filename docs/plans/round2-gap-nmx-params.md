# 需求缺口：n/m/x 在 UI 上不可控（父代理现场核实）

状态：**已核实，第二波候选**（来源：verify-requirement-reach lane 的线索 + 父代理独立复核）

## 需求条文

`功能说明.txt` 步骤 1、3 逐字要求：

> 1、用户输入指定关键词，工具调用llm模型生成关键词下属的n个领域……再根据n个领域生成m个领域下属方向……
> 形成n*m项条目，**其中n和m是用户可控的**。

> 3、根据1、2步骤的成果，调用llm开始生成每个方向的具体问题x个……**其中x是用户可控的数量**。

## 现场核实（可复现）

### 后端**已经**支持三者的用户可控

```bash
cd /root/llm
# m 用户可控（注释就写明了）
grep -n "UpdateDirectionCount" internal/store/dataset_store.go
#   internal/store/dataset_store.go:132:// UpdateDirectionCount 设置每个领域下生成的方向数量（m 用户可控）。
# 入队接口接受 directionCount（n 由领域生成控制，m 由此参数控制）
grep -n "DirectionCount" apps/api/routes_directions.go
#   :94  directionCount := input.DirectionCount
#   :95  if directionCount <= 0 { directionCount = dataset.DirectionCount }
# 前端 API 客户端也已支持传参
sed -n '612,632p' apps/web-user/src/lib/api.ts
#   generateDirections: (id, directionCount?) => POST .../directions/generate { directionCount }
#   generateQuestionsV2: (id, questionsPerDirection, difficultyMix?) => POST .../questions/generate
```text

数据集模型也有字段：`DirectionCount` / `QuestionsPerDirect`（`internal/model/dataset.go:24`、`internal/store/dataset_store.go:80-81`）。

### 前端 UI **没有**把这三个参数暴露给用户

```bash
# 1) m/x 完全没有输入框：策略表单只有 领域数 / 每领域问题数 / 答案变体数 / 奖励变体数
sed -n '3400,3422p' apps/web-user/src/App.tsx
#   3406  领域数          -> strategyDraft.domainCount
#   3410  每领域问题数     -> strategyDraft.questionsPerDomain
#   （没有「每领域方向数」这一项）
# 2) 创建任务表单把生成参数整块隐藏了
sed -n '863p' apps/web-user/src/App.tsx
#   const showAdvancedPlanning = false          <-- 硬编码 false
# 3) 生成动作调用时**不传**参数，退化为后端默认值
sed -n '1436,1442p' apps/web-user/src/App.tsx
#   const nextGraph = await consoleApi.generateDomains(activeDatasetId)   <-- 未传 domainCount
sed -n '1516,1521p' apps/web-user/src/App.tsx
#   const result = await consoleApi.generateQuestions(activeDatasetId)    <-- 走 legacy，未传 questionsPerDirection
```text

### 实际生效的值来自哪里

```bash
sed -n '80,82p' internal/store/dataset_store.go
#   if input.DirectionCount <= 0 { input.DirectionCount = 3 }     <-- m 退化为 3
sed -n '96,100p' apps/api/routes_directions.go
#   if directionCount <= 0 { directionCount = dataset.DirectionCount }
#   if directionCount <= 0 { directionCount = 3 }
```text

## 缺口判定

| 项 | 需求 | 后端 | 前端 UI | 判定 |
|---|---|---|---|---|
| **n**（领域数） | 用户可控 | 支持（`domainCount` / 估算） | 仅在「生成策略」表单，且创建任务表单默认隐藏该区（`showAdvancedPlanning=false`）；非管理员不可见 | **部分可达** |
| **m**（每领域方向数） | 用户可控 | **完整支持**（`UpdateDirectionCount`、`directionCount` 入参） | **完全没有输入项**；生成时也不传参 → 恒为 3 | **不可达** |
| **x**（每方向问题数） | 用户可控 | **完整支持**（`questionsPerDirection` 入参） | 策略表单有「每领域问题数」，但生成动作走 legacy 接口不传参 | **部分可达** |

**结论**：`功能说明.txt` 明确要求 n/m/x 用户可控，而后端能力齐备、UI 接线缺失。
这属于**产品需求缺口**，而不是后端缺陷 —— 与 #65（后端 6 项能力缺 UI 入口）是同一类问题，
但 #65 的 lane 范围只覆盖 L2–L6/R1，**不包含** n/m/x 这三个生成参数。

## 处置（父代理决定）

1. **记为第二波候选**（优先级：高）。理由：
   - 它是 `功能说明.txt` 的**逐字要求**，不是解释出来的；
   - 后端已就绪，改动面小（`App.tsx` 的生成参数区 + 两个调用点传参），风险低；
   - 但它在冻结契约 `docs/plans/issue-remediation-plan.md` 的 lane 划分里**没有对应 lane**，
     因此按契约 §2「父代理可自主决定 lane 拆分」与 §8「需要变更先更新契约文件并在 PR 中说明理由」，
     由父代理开一条**独立的新 lane**，并在 PR 里写明为何超出原 12 条 lane。
2. **不擅自改冻结契约的既有 12 条 lane 定义**，而是新增一条 R13 并登记到 `round2-lane-board.md`。
3. 该 lane 必须自带 UI 可达性测试（沿用 `test/l14_ui_smoke.mjs` 的 esbuild + React 渲染路线，
   **不引入 chromium/playwright**），并断言「改参数 → 请求体里真的带上该参数」。
