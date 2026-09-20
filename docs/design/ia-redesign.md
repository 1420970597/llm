# 控制台信息架构与交互重设计方案

> 类型：**设计提案**（不改生产代码）。对应 issue #84 / #90。
> 状态：待评审。原型可交互：`docs/design/ia-prototype.html`（用浏览器打开，改 URL hash 切换页面）。
> 证据底稿：`docs/research/ia-research-notes.md`（一手来源与抓取方式）。

---

## 0. 本文的证据分级

| 标记 | 含义 |
|---|---|
| **[一手理论]** | 直接抓取原文并引用段落（NN/g） |
| **[一手产品]** | 直接读取**产品源码**（非文档描述），如 Langfuse |
| **[本仓库实测]** | 用真实 Chromium 在本地运行的系统上采集 |

> 与上一轮的区别：`docs/guides/ux-improvements.md` 的外部结论是**转述 issue #84 的二手引用**，
> 未独立复核。本文全部为一手抓取。

---

## 1. 调研

### 1.1 NN/g《Wizards》——流程类界面的 8 条准则 [一手理论]

来源：<https://www.nngroup.com/articles/wizards/>

> "**Use wizards for novice users or infrequent processes** … if you expect that some of
> your users will perform it **repeatedly**, consider offering them another **faster
> alternative** for inputting their data."

> "Wizards can require a **higher interaction cost (more clicks)** … the tedium of clicking
> through each of the steps can overwhelm the advantage of splitting the process."

**对本项目的判定**：数据工厂的 5 阶段流水线是**重复性**业务（用户会反复创建任务）。
因此**不应做成纯向导**，必须是「**概览 + 可中断 + 可直达单步**」的组合。
这否定了「加个『下一步』就够了」的朴素思路 —— 也说明上一轮我只加按钮是远远不够的。

其余直接可用的准则：

| # | 准则（原文要点） | 本项目现状 |
|---|---|---|
| 2 | 显示步骤列表并**高亮当前步** | 侧边栏无步骤感，只在任务详情有 5 张卡片 |
| 3 | **强制顺序**，不得跳过前置步骤 | ❌ 5 个阶段页可从菜单任意直达 |
| 4 | 显式 next/prev 按钮，标签要有信息量 | ❌ 实测 18 个按钮中向前导航 = 0 |
| 5 | **可中途退出并保存，可恢复** | ⚠️ 数据在服务端持久化，但 UI 无「继续」主操作 |
| 8 | 复用上次选择作为默认值 | ❌ 每次从空白填 |

### 1.2 NN/g《Progressive Disclosure》——披露层级上限 [一手理论]

来源：<https://www.nngroup.com/articles/progressive-disclosure/>

> "**Progressive Disclosure**：1. Initially show users **only a few** of the most important
> options. 2. Offer a **larger set** of specialized options upon request."

> 与 **Staged Disclosure** 的区别：前者「most users get what they need on the initial
> display」；后者「**Yes** — users access subsequent displays」（必然要走完）。

> "In practice, designs that go beyond **2 disclosure levels** typically have **low
> usability** because users often get lost when moving between the levels."

**对本项目的两条硬约束**：

1. 5 个阶段属于 **staged disclosure**（用户必然走完），**不该出现在「按需展开」的侧边栏里**。
2. 侧边栏 = 一级，页面内折叠 = 二级，**不得再引入第三级**。

### 1.3 NN/g《IA vs Navigation》——顺序不能颠倒 [一手理论]

来源：<https://www.nngroup.com/articles/ia-vs-navigation/>

> "The IA is **not** part of the on-screen user interface — rather, **IA informs UI**."

> "**Define the IA Before Designing Navigation** … it is inefficient and even **dangerous**
> to do so."

**对上一轮的定性**：我做的是「往现有导航里加按钮」，**没有先定义 IA**。顺序错了。

### 1.4 Langfuse 真实产品源码 [一手产品]

来源：GitHub `langfuse/langfuse` → `web/src/components/layouts/routes.tsx`

```ts
export enum RouteSection {
  Main = "main",
  Secondary = "secondary",
}
export enum RouteGroup {
  Observability = "Observability",
  PromptManagement = "Prompt Management",
  Evaluation = "Evaluation",
}
export type Route = {
  title: string; href: string; icon?: LucideIcon;
  items?: Array<Route>;                  // ← 支持嵌套
  section?: RouteSection;                // ← 主区/次区物理分离
  group?: RouteGroup;                    // ← 区内分组（能力域）
  projectRbacScopes?: ProjectScope[];    // ← 权限过滤
  show?: (p) => boolean;                 // ← 条件显示
};
```

**可借鉴的三点**：

1. **一级导航是「能力域」**（Observability / Evaluation），不是功能清单。
2. **`section` 把业务区与设置区物理分开** —— 这正是本项目「6 个管理项与业务项并列」的解法。
3. **`items` 真嵌套** —— 阶段不是平级菜单项。

### 1.5 本仓库实测（现状）[本仓库实测]

![当前侧边栏](assets/ia-design/current-sidebar.png)

| 指标 | 实测值 |
|---|---|
| 侧边栏宽度 | 240 px |
| 普通用户可点条目 | **7**（业务项） |
| 管理员可点条目 | **7** 业务项 + 「系统设置」内折叠的 **6** 个管理项 |
| 出现在侧边栏的**流程步骤** | **0** |

> **一处自我更正（重要）**：本方案初稿写着「普通用户 9 项 / 管理员 13 项」「5 个流程步骤被挂成平级菜单项」。
> 用真实浏览器逐角色精确测量后发现**两个数字都是错的**：`navParent` 只用于「高亮哪个菜单」，
> **并不生成菜单项**，5 个阶段页从未出现在侧边栏里；而 `App.tsx` 写分组时本就带了 `adminOnly`
> 过滤与折叠的「系统设置」分组，管理员那 6 项并非与业务项平铺。上表已更正为实测值。
> 结论：真正的缺口不是「菜单太多」，而是**阶段页缺少任务上下文**（见下方错误 A）。

---

## 2. 问题诊断

### 2.1 三个结构性错误

#### 错误 A：流程步骤没有「任务上下文」（不是「被当成平级功能」）

```text
主题结构 → 问题生成 → 答案内容 → 质量评估 → 导出交付
```

这是一条**线性流程**（staged disclosure）。它没有出现在侧边栏里（这点本身是对的），
但阶段页**不显示自己在这条链上的位置**：侧边栏始终高亮「我的任务 / 数据资产」，
于是两个不同阶段的页面看起来完全一样，用户看不出「我在第几步 / 哪几步已完成 / 下一步去哪」。

#### 错误 B：菜单同时承担「去哪」和「做什么」两种语义

`数据资产`（去哪）与 `导出交付`（流程某一步）并列 → 语义混杂。

#### 错误 C：同一概念两个名字**

| 代码位置 | 标签 | 路由 | 语义 |
|---|---|---|---|
| `userPages[4]`（`App.tsx:110`） | 质量评估 | `/console/evaluation` | **多模型互评** |
| `resultWorkbenchPages[2]`（`App.tsx:168`） | 质量评估 | `/console/rewards` | **流水线第 4 步评分** |

用户看到两个同名菜单，点进去是不同东西。

### 2.2 未完成的嵌套意图

`App.tsx:116-118` 注释原文：

> 阶段路由不是侧边栏项，用户是从任务详情页的阶段卡片进入的，因此每个阶段必须额外声明
> 它在侧边栏里的归属父项（`navParent`）……

→ 作者**已意识到阶段不该是平级项**，但只实现了「高亮归属」，**没实现「二级菜单」**。

---

## 3. 新信息架构

### 3.1 五条设计原则

1. 一级导航 **≤ 5 项**，按**用户心智模型**而非功能清单划分。
2. **流程步骤不做一级导航**，改为「任务上下文二级导航」。
3. **同一概念只有一个名字、一个位置**。
4. **管理员能力与业务能力物理分离**（不是靠 `adminOnly` 混在同级）。
5. 任何页面都能回答「**我在哪 / 这是哪个任务 / 下一步去哪**」。

### 3.2 三个一级区块（从 9→3）

![全局导航](assets/ia-design/01-全局导航-任务总览.png)

### 3.3 任务上下文二级导航（关键设计）

进入某个任务后，侧边栏**切换为任务上下文**，而非继续显示全局菜单：

![任务上下文-进行中](assets/ia-design/03-任务上下文-进行中.png)

要点：5 个阶段**成为当前任务的二级导航**；每步带**状态图标 + 计数**；
底部固定「下一步」；**未完成的步骤灰显不可点**（NN/g 准则 3）。

### 3.4 迁移映射

| 旧路由 | 旧标签 | 新位置 | 新路由 |
|---|---|---|---|
| `/console/home` | 工作台 | ① 任务 | 不变 |
| `/console/tasks` | 我的任务 | ① 任务 | 不变 |
| `/console/planning` | 新建任务 | ① 任务 | 不变，改向导 |
| `/console/results` | 数据资产 | ② 数据资产 | `/console/assets` |
| `/console/exports` | 导出交付 | ② 数据资产 | `/console/assets/exports` |
| `/console/evaluation` | 质量评估 | ③ 质量与治理 | `/console/quality/eval` **改名「评估任务」** |
| `/console/cleaning` | 数据清洗 | ③ 质量与治理 | `/console/quality/clean` |
| `/console/rewards` | 质量评估（阶段4） | **任务上下文** | `/tasks/:id/rewards` |
| `/console/domains` | 主题结构 | **任务上下文** | `/tasks/:id/domains` |
| `/console/questions` | 问题生成 | **任务上下文** | `/tasks/:id/questions` |
| `/console/reasoning` | 答案内容 | **任务上下文** | `/tasks/:id/reasoning` |
| `/console/help` | 账户与帮助 | 底部 | 不变 |
| `/console/operations` | 运营监控 | ⚙ 系统管理 | `/console/admin/operations` |
| `/console/admin/*` | 其余 5 项 | ⚙ 系统管理 | 不变 |

**关键变化**：4 个阶段路由从全局菜单**移入任务上下文**，并携带 `taskId` ——
顺带修掉 #123「刷新丢失任务上下文」。

**兼容**：旧路由保留为重定向（有 `activeDatasetId` → 跳 `/tasks/:id/xxx`；
否则跳 `/console/tasks` 并提示「请先选择一个任务」）。

---

## 4. 原型图（逐页 · 逐元素 · 点击后效果）

> 以下每页给出：截图 + **每个可点元素的落点表**。
> 原型文件：`docs/design/ia-prototype.html`

### 4.1 全局导航区

![全局导航](assets/ia-design/01-全局导航-任务总览.png)

| 元素 | 点击后 | 备注 |
|---|---|---|
| 顶部任务切换器 | 展开任务下拉，选中即切换全局上下文 | 新增，解决「当前是哪个任务」 |
| 任务 › 总览 | `/console/home` | 一级区块「任务」 |
| 任务 › 全部任务 | `/console/tasks`（badge 显示进行中数量） | |
| 任务 › ＋新建任务 | `/console/planning` | 动作项，用 `＋` 前缀区分 |
| 数据资产 › 数据集 | `/console/assets` | |
| 数据资产 › 导出记录 | `/console/assets/exports` | 原「导出交付」，移出流程 |
| 质量与治理 › 评估任务 | `/console/quality/eval` | **改名，消除同名冲突** |
| 质量与治理 › 清洗记录 | `/console/quality/clean` | |
| 质量与治理 › 评分规则 | `/console/quality/judges` | |
| ⚙ 系统管理 | 展开管理区（原 6 项） | 仅管理员可见，与业务区**物理分离** |
| ? 帮助 | `/console/help` | |

### 4.2 新建任务（渐进披露）

![新建任务](assets/ia-design/02-新建任务-模板与渐进披露.png)

| 元素 | 点击后 | 备注 |
|---|---|---|
| 模板卡片（3 张） | 选中态高亮 + **预填**主题与目标条数 | 复用已实现能力 |
| 任务主题 | 输入框 | 必填 |
| 目标样本数 | 输入框 | 必填，带说明 |
| ▸ 生成参数（可选） | 展开 n / m / x | 默认收起；留空=后端默认，**不丢能力** |
| ▸ 高级设置（仅管理员） | 展开策略 / AI 服务 / 存储 | 默认收起 |
| 创建任务 | 提交 → 跳任务详情 | |
| 取消 | 返回 `/console/tasks` | |

**必填从 6–9 项降到 2 项**，页面文案与界面一致。

### 4.3 任务上下文（三态）

![进行中](assets/ia-design/03-任务上下文-进行中.png)

| 元素 | 点击后 |
|---|---|
| ← 全部任务 | `/console/tasks` |
| 任务卡（名称 / 计数 / 进度条） | `/tasks/:id`（进度总览） |
| ◫ 进度总览 | `/tasks/:id` |
| ✓ 主题结构（已完成） | `/tasks/:id/domains` **可点** |
| ● 问题生成（当前） | `/tasks/:id/questions` **可点** |
| ○ 答案内容（锁） | **不可点**，hover 提示「完成『问题生成』后可进入」 |
| ○ 质量评分 / 导出交付（锁） | 同上 |
| 底部按钮（进行中） | 灰显「等待本阶段完成」，**不可点** |

![步骤完成](assets/ia-design/04-任务上下文-步骤完成.png)

| 元素 | 点击后 |
|---|---|
| ✓ 问题生成（已完成） | 可点，可回看 |
| ○ 答案内容 | **变为可点** |
| 底部按钮 | 变为「**下一步：答案内容 →**」→ `/tasks/:id/reasoning` |

![锁定态](assets/ia-design/05-任务上下文-锁定态.png)

锁定态页面**直接说明原因**（「上一步『问题生成』尚未完成」），而不是留白或报错。

### 4.4 数据资产与质量治理

![数据资产](assets/ia-design/06-数据资产.png)

| 元素 | 点击后 |
|---|---|
| 下载 JSONL | 同源 `/api/v1/datasets/:id/artifacts/:aid`（复用已加固的 `downloadTarget.ts`） |

![导出记录](assets/ia-design/07-导出记录.png)

失败行**显示原因 + 恢复路径**（「去配置」→ 存储页），而不是只标红。

![质量治理](assets/ia-design/08-质量治理.png)

---

## 5. 实施计划

按「先 IA 后导航」的顺序，分 4 批，每批可独立验收、可独立回滚。

| 批次 | 内容 | 风险 | 依赖 |
|---|---|---|---|
| **B1** | 纯路由/sidebar 改造：三区块 + 任务上下文 + 旧路由重定向 | 中（触及 `App.tsx`） | 无 |
| **B2** | 新建任务向导改造（模板 + 折叠 + 文案一致） | 低 | 无 |
| **B3** | 阶段路由迁移到 `/tasks/:id/*` + 锁态与「下一步」 | 中 | B1 |
| **B4** | 管理员区收进 ⚙ 系统管理；术语统一（评估任务 / 评分） | 低 | B1 |

**不新增后端路由、不新增迁移** —— 本设计是前端 IA 重排。

---

## 6. 验收标准

| # | 标准 | 验证方式 |
|---|---|---|
| 1 | 普通用户一级导航 ≤ 4 项 | 实测 DOM 计数 |
| 2 | 5 个阶段**不出现在**全局侧边栏 | 实测 DOM 无 `/console/domains` 等链接 |
| 3 | 进入任务后侧边栏显示 5 步 + 当前高亮 | 实测 + 截图 |
| 4 | 未完成步骤不可点 | 实测 `disabled` 或 `aria-disabled` |
| 5 | 「下一步：{名称}」在步骤完成后可用 | 实测两态 |
| 6 | 旧路由不 404，重定向正确 | 逐条访问 13 个旧路由 |
| 7 | 「质量评估」同名冲突消除 | 全文搜索无二义 |
| 8 | `tsc --noEmit` + `vite build` 通过 | CI 闸门 |

---

## 7. 待决问题（需要你拍板）

1. **是否需要「跨任务的全局流水线视图」？** 当前设计假设用户**一次专注一个任务**。
   若运营需要同时盯多个任务的阶段进度，需要额外的全局看板（本方案未含）。
2. **阶段路由是否真要带 taskId？** 带 → 修掉 #123，但所有旧链接需重定向；
   不带 → 改动小，但上下文仍靠内存。
3. **管理员区是否折叠为单一入口？** 折叠更干净，但管理员多一次点击。
4. **是否保留「数据清洗」在质量治理下？** 也可论证它属于「数据资产」。
