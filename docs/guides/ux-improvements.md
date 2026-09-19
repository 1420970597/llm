# UX 改造实践说明（分支 `ux/page-flow-improvements`）

> 本文档记录基于 issue #84（操作步骤不符合人类习惯）与 #90（页面设计说明书）的改造实践，
> 并附**实际运行截屏**作为证据。
>
> **本分支尚未合并到 `main`**。合并前需经评审。

## 0. 一句话结论

改造前，完成一次数据流水线需要 **19 步操作、5 次「返回任务详情 → 点阶段卡片」的往返**，
且失败时界面不告知原因。改造后：

| 变化 | 改造前 | 改造后 |
|---|---|---|
| 阶段间前进 | **无「下一步」**，必须绕回任务详情 | 阶段页直接给出「**下一步：{下一阶段}**」 |
| 等待期反馈 | 静态文案「系统同步中，请稍后刷新」 | **百分比进度条 + 各阶段明细 + 队列深度 + ETA** |
| 新建任务 | 一次平铺 6 个字段（含 3 项管理员配置） | **4 个内置模板一键预填**，渐进披露 |
| 阶段路由来源 | 三处各写一份、互相漂移（#61 成因） | `stageFlow.ts` **单一事实来源** |
| 未知状态 | 直接显示 `directions_completed`（#98） | 中文映射 + 正确进度与去向 |

---

## 1. 改造内容

### 1.1 新增文件（子代理并行产出 + 父代理补齐）

| 文件 | 作用 | 对应 issue |
|---|---|---|
| `apps/web-user/src/views/flow/stageFlow.ts` | 流水线 5 阶段的**单一事实来源** | #90 |
| `apps/web-user/src/views/flow/StageNextStep.tsx` | 阶段页「下一步」导航条 | #84 |
| `apps/web-user/src/views/flow/StageProgressDetail.tsx` | 等待/失败期的真实进度展示 | #84 / #83 |
| `apps/web-user/src/views/flow/TaskTemplatePicker.tsx` | 任务模板卡片选择 | #84 |

### 1.2 `App.tsx` 接入点（父代理串行完成）

| 接入 | 说明 |
|---|---|
| 阶段页加「下一步」 | `renderRecordPage` 新增 `stageKey` prop，据 `stageFlow` 渲染；`renderDomains` 同样接入 |
| 任务详情改进度 | 用 `StageProgressDetail` 替换原有静态文案，消费已有的 `pipeline/progress` 数据 |
| 主按钮路由派生 | 改用 `currentStageFor()`，保留 `statusToActionRoute` 兜底 |
| 新建任务加模板 | 新增 `TASK_TEMPLATES` 常量与 `selectedTemplateId` state |
| `canGenerate*` 修正 | 原定义未含 `*_failed`，直接接线会禁用重试按钮；改为 `isStageDone(上一阶段) \|\| isStageFailed(本阶段)` |
| 移除既有死变量 | `reviewArtifactCount` / `otherArtifactCount` / `workbenchStats`（算了却从未渲染） |

### 1.3 修复 #98（改造过程中发现）

`directions_completed` / `directions_partial_failed` 由 worker 写入，但前端 6 个状态映射函数全无分支，
导致界面显示原始英文状态串、进度归零。已在 `stageFlow.ts` 与 `App.tsx` 补齐。

---

## 2. 全部页面截屏

> 截屏环境：docker compose 全栈，`admin@company.com`，Chromium 1440×900。
> 截图目录：`docs/assets/ux-after/`

### 2.1 入口与总览

#### 登录页

![登录页](assets/ux-after/01-login.png)

#### 工作台

![工作台](assets/ux-after/02-home.png)

### 2.2 任务创建（渐进披露改造点）

#### 新建任务 —— **新增任务模板卡片**

![新建任务](assets/ux-after/03-planning.png)

**改造点**：页面顶部新增「从模板开始」区块，4 个内置模板（行业研究问答 / 客服对话 SFT /
代码指令集 / 长链推理 GRPO）各显示名称、描述、标签与「主题 · 目标条数」摘要。
点击任一模板即预填任务主题与目标规模。

对应 issue #84 的调研结论：Dify 用 5 种 Built-in Pipeline、Argilla 用 5 种任务模板
降低上手成本，本系统此前需从空白填起且一次暴露 6 个字段。

#### 我的任务

![我的任务](assets/ux-after/04-tasks.png)

#### 任务详情（枢纽页）—— **进度展示改造点**

![任务详情](assets/ux-after/20-task-detail.png)

**改造点**：原先只有一行静态文案「系统同步中，请稍后刷新」。
现在替换为 `StageProgressDetail` 组件：

- **总体进度**百分比进度条
- ETA 文案（未知时显示「预计时间待确认」）
- 队列深度（「队列中还有 N 个任务」/「当前无排队任务」）
- **各阶段明细**：每阶段的状态标签（待开始/排队中/进行中/已完成/失败，不同颜色）、条数、摘要

该组件同时支持 `failureReason` 置顶展示（为 #83 预留，后端字段尚未落库）。

### 2.3 流水线 5 个阶段页（「下一步」改造点）

#### 阶段 1 主题结构

![阶段1 主题结构](assets/ux-after/15-stage-domains.png)

#### 阶段 2 问题生成

![阶段2 问题生成](assets/ux-after/16-stage-questions.png)

**改造点**：PageHeader 下方新增「**下一步：答案内容**」按钮与说明文案
（未完成时按钮禁用并提示「完成本阶段（问题生成）后可进入「答案内容」」）。

#### 阶段 3 答案内容

![阶段3 答案内容](assets/ux-after/17-stage-reasoning.png)

#### 阶段 4 质量评分

![阶段4 质量评分](assets/ux-after/18-stage-rewards.png)

#### 阶段 5 导出交付

![阶段5 导出交付](assets/ux-after/19-stage-exports.png)

最后一个阶段不再显示「下一步」，改为「**已是最后一步** → 前往数据资产」。

### 2.4 结果与治理

#### 数据资产

![数据资产](assets/ux-after/05-results.png)

#### 质量评估（多模型互评）

![质量评估](assets/ux-after/06-evaluation.png)

#### 数据清洗

![数据清洗](assets/ux-after/07-cleaning.png)

#### 账户与帮助

![账户与帮助](assets/ux-after/08-help.png)

### 2.5 管理员配置

#### 运营监控

![运营监控](assets/ux-after/09-operations.png)

#### AI 服务

![AI 服务](assets/ux-after/10-admin-providers.png)

#### 结果存储

![结果存储](assets/ux-after/11-admin-storage.png)

#### 生成规则

![生成规则](assets/ux-after/12-admin-strategies.png)

#### 生成指令

![生成指令](assets/ux-after/13-admin-prompts.png)

#### 操作记录

![操作记录](assets/ux-after/14-admin-audit.png)

### 2.6 质量评估的 4 个页签

#### 维度管理

![维度管理](assets/ux-after/21-eval-dimensions.png)

#### 新建评估

![新建评估](assets/ux-after/22-eval-create.png)

#### 运行列表

![运行列表](assets/ux-after/23-eval-runs.png)

#### 结果与报告

![结果与报告](assets/ux-after/24-eval-report.png)

---

## 3. 改造前后对照片

### 3.1 阶段页：从「无前进」到「下一步」

```text
改造前：PageHeader actions = [返回当前任务] [返回我的任务] [刷新结果] [开始生成题目]
        ↑ 没有前进入口，用户只能点「返回当前任务」绕回详情页

改造后：PageHeader actions = [返回当前任务] [返回我的任务] [刷新结果] [开始生成题目]
        ─────────────────────────────────────────────
        [下一步：答案内容 →]  本阶段已完成，可直接进入「答案内容」。
```text

### 3.2 任务详情：从静态文案到真实进度

```text
改造前：
  标签行：状态 / 进度 15% / ETA: 确认方向结构后显示
  进度条（仅一根）
  静态文案：结构未确认，尚未开始生成。

改造后：
  总体进度 55%  ────────────────────────
  ⏱ 约 3 分钟    队列中还有 2 个任务
  各阶段明细
    领域整理    [已完成] 30 条
    问题生成    [已完成] 100 条
    推理生成    [进行中] 37 条
    奖励评估    [待开始] 0 条
    导出交付    [待开始] 0 条
  • 问题生成：已生成 100 条问题
  • 推理生成：已生成 37 条推理
  ...
```text

### 3.3 新建任务：从 6 字段平铺到模板预填

```text
改造前：任务名称(可选) / 任务主题 / 目标样本数 / 生成策略 / AI 服务 / 存储配置
        ↑ 后三项是管理员配置，与业务必填项同屏；页面却写着「只填任务主题和目标规模即可」

改造后：┌─────────────────────────────────────────┐
        │ 从模板开始                              │
        │ 选一个模板即可预置任务主题与目标规模；   │
        │ 也可以不选，直接在下方手动填写。         │
        ├────────────┬────────────┬───────────────┤
        │ 行业研究问答│ 客服对话SFT│ 代码指令集    │
        │ SFT 问答   │ SFT 对话   │ SFT 代码      │
        │ 主题:行业研究│ 主题:客服..│ 主题:代码指令 │
        │ 目标 50 条  │ 目标 30 条 │ 目标 30 条    │
        └────────────┴────────────┴───────────────┘
        任务主题和目标规模是必填项；其余按需展开。
```text

---

## 4. 验证

| 项 | 结果 |
|---|---|
| `tsc --noEmit` | 通过（无输出） |
| `npm run build -w apps/web-user` | `✓ built in 37.58s` |
| 真实浏览器逐页截图 | 24 张，全部渲染正常 |
| 模板预填 | 实测点「行业研究问答」→ 主题自动填「行业研究」、规模填 50 |
| 「下一步」按钮 | 实测阶段页显示「下一步：答案内容」 |
| 「从模板开始」区块 | 实测 DOM 渲染出 4 个模板及描述、标签、主题与目标条数 |
| 下载校验单元测试 | `npm test -w apps/web-user` → 6/6 通过（新增） |

### 4.0 关于下载校验加固的说明

`downloadArtifact` 原为 `if (!datasetId || !id) return`，仅判真值。若后端返回的
`datasetId`/`id` 因数据异常而非数字，会拼出 `/api/v1/datasets/NaN/...` 的无效请求。

现把规则抽成纯函数 `views/flow/downloadTarget.ts`（严格正整数 + 同源白名单），
并配套**真实可运行的单元测试** `tests/downloadTarget.test.ts`：

```text
$ npm test -w apps/web-user
# tests 6
# pass 6
# fail 0
```

测试覆盖：合法输入、`NaN`/`undefined`/`null`/对象/数组/布尔、`0`/负数/小数/`Infinity`，
以及 `https://`、`//host`、`javascript:`、`data:` 等跨域与危险协议。

> **测试发现了一个真实缺陷**：最初实现用 `Number(v)` 后判 `Number.isInteger`，
> 而 `Number(true) === 1`，导致布尔值 `true` 会通过校验。已改为类型与字面双重检查
> （只接受 number 与纯十进制数字串）。这是先写断言再修正实现的收益。
>
> **未做端到端实测（如实声明）**：交付物下载按钮需先有导出的 artifact 才会出现，
> 本次演示数据集尚未走到导出阶段，因此 UI 路径未在浏览器中走通。
> 当前证据为：单元测试（强）+ 生产构建通过（中），**不含**浏览器端到端（缺）。

### 4.1 已知限制（如实记录）

1. **未新增后端接口**：遵守冻结契约 §1.1「本轮不新增任何 HTTP 路由」，模板仅为前端常量，
   未落库；`failureReason` 因后端无该字段而未展示（属 #83 范围）。
2. **`canGenerate*` 的判据是"可尝试"而非"必然成功"**：例如问题生成还要求 level=2 方向存在
   （见 #65），该前置条件后端在 `routes_questions_v2.go:117` 校验，前端暂未同步。
3. **未做响应式截图**：所有截屏为 1440×900 桌面视口。
4. **未合并**：本分支等待评审，未执行 `git push`，未创建 PR。

---

## 5. 并行开发的实际情况（如实记录）

本次按用户授权尝试了**子代理并行开发**，结果如下：

| 子任务 | 计划产出 | 实际结果 |
|---|---|---|
| A | `StageNextStep.tsx` | ✅ 子代理产出，父代理微调一处冗余三元表达式 |
| B | `StageProgressDetail.tsx` | ❌ **lane 基础设施故障**（runner 进程消失），由父代理补齐 |
| C | `TaskTemplatePicker.tsx` | ✅ 子代理产出 |
| — | `App.tsx` 接入 | 父代理独占串行完成（该文件 3757 行，三者并发改必然冲突） |

**故障详情**：3 个子代理的 runner 进程先后在约 1m50s 时消失，
`process-terminal.json` 记录 `reason: "writer-close-unverified"`，属进程级故障而非任务逻辑失败。
经查宿主内存 1.8G 且当时低于 200MB 可用，疑为资源压力所致。故障后已清理残留进程并转由父代理完成剩余工作。

---

## 6. 关联 issue

| Issue | 关系 |
|---|---|
| #84 | 本改造的**主要依据**（操作步骤不符合人类习惯） |
| #90 | 本改造的**结构依据**（页面设计说明书） |
| #98 | **改造过程中发现并修复**（前端不认识 `directions_completed`） |
| #83 | `StageProgressDetail` 已支持 `failureReason`，等后端字段 |
| #65 | 方向生成仍缺 UI 入口，本改造未涉及 |
