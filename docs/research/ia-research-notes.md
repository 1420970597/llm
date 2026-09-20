# IA 调研笔记（一手来源）

> 本文件是 `docs/design/ia-redesign.md` 的证据底稿。
> **每条结论都标注来源与获取方式**，区分「一手理论」「一手产品源码」「本仓库实测」。

---

## 0. 证据分级

| 标记 | 含义 |
|---|---|
| **[一手理论]** | 直接抓取原文并引用段落 |
| **[一手产品]** | 直接读取该产品的**源码**（非文档描述） |
| **[本仓库实测]** | 用 Playwright 在本地运行的系统上采集 |

> 本轮的改进：上一版文档（`docs/guides/ux-improvements.md`）的同类结论是
> **转述 issue #84 的二手引用**，未独立复核。本文件全部为一手抓取。

---

## 1. NN/g《Wizards: Definition and Design Recommendations》

来源：<https://www.nngroup.com/articles/wizards/>（2026-09-19 抓取）

### 1.1 定义 [一手理论]

> "A **wizard** is a step-by-step process that allows users to input information in a
> prescribed order and in which subsequent steps may depend on information entered in
> previous ones."

### 1.2 适用场景 [一手理论]

> "**Use wizards for novice users or infrequent processes (e.g., configuration or setup).**
> ... if you expect that some of your users will perform it repeatedly, consider offering
> them another faster alternative for inputting their data."

> "Wizards can require a higher **interaction cost** (more clicks) than other input
> patterns. Especially if wizards need to be invoked repeatedly, the tedium of clicking
> through each of the steps can overwhelm the advantage..."

**对本项目的判定**：数据工厂的 5 阶段流水线是**重复性**业务（用户会反复创建任务），
因此**不应做成纯向导**，而应是「**概览 + 可中断 + 可直达单步**」的组合。
这修正了「加个下一步就够了」的朴素想法。

### 1.3 八条设计准则（原文编号）[一手理论]

1. Use wizards for novice users or infrequent processes
2. **Communicate a clear mental model of the process** by displaying a list or a diagram
   of the steps involved and highlighting the current step
3. **Enforce a clear sequential order of the steps.** Do not allow users to pick step
   before completing the steps preceding it
4. **Include buttons for navigating to the next and previous steps and label the steps
   descriptively.** "Generic labels such as *Next* and *Previous* have weak information
   scent"
5. **Allow users to exit the wizard midway and save state. Allow them to resume**
6. **Wizard steps should be self-sufficient** and not require information available
   elsewhere in the app
7. Help and explanations should appear **next to** the wizard and should not cover it
8. **Consider reusing the user's selections from previous use as the defaults**

**对本项目的映射**（这是方案的直接依据）：

| 准则 | 现状 | 方案 |
|---|---|---|
| 2 显示步骤+高亮当前 | 任务详情有 5 张卡片；但侧边栏无步骤感 | 任务上下文二级导航显示 5 步 + 当前高亮 |
| 3 强制顺序 | ❌ 5 个阶段页可任意从菜单直达 | 未完成的步骤灰显、不可点 |
| 4 显式 next + 描述性标签 | ❌ 实测 18 个按钮中向前导航 = 0 | 「下一步：{下一阶段}」 |
| 5 可中断恢复 | ⚠️ 任务持久化在服务端，但 UI 无「继续」入口 | 任务列表给「继续」主操作 |
| 8 复用上次选择 | ❌ 每次从空白填 | 任务模板（已实现）|

---

## 2. NN/g《Progressive Disclosure》

来源：<https://www.nngroup.com/articles/progressive-disclosure/>（2026-09-19 抓取）

### 2.1 核心 [一手理论]

> "1. Initially, show users **only a few** of the most important options.
>  2. Offer a **larger set** of specialized options upon request."

### 2.2 与 staged disclosure 的区别（关键区分）[一手理论]

> | | Progressive Disclosure | Staged Disclosure |
> |---|---|---|
> | Initial display | **Core** features | Features users access **first** in the task sequence |
> | Subsequent display(s) | **Secondary** features | Features users access **later** in the task |
> | Do users access subsequent displays? | **Usually not** | **Yes** |

> "**Wizards** are the classic example of staged disclosure."

### 2.3 层级上限（重要约束）[一手理论]

> "In practice, designs that go beyond **2 disclosure levels** typically have **low
> usability** because users often get lost when moving between the levels."

**对本项目的映射**：
- 侧边栏 = 一级；页面内分组（如「高级设置」）= 二级。**不应再引入第三级折叠**。
- 5 个阶段属于 **staged disclosure**（用户必然要走完），不是 progressive disclosure。
  **因此它们不该出现在「按需展开」的侧边栏里**，而应作为任务的固定步骤栏。

---

## 3. NN/g《The Difference Between Information Architecture (IA) and Navigation》

来源：<https://www.nngroup.com/articles/ia-vs-navigation/>（2026-09-19 抓取）

### 3.1 核心区分 [一手理论]

> "A website's ... information architecture has two main components:
> - identification and definition of **site content and functionality**
> - the underlying organization, structure and nomenclature that define the
>   **relationships** between a site's content/functionality"

> "The IA is not part of the on-screen user interface (UI) — rather, **IA informs UI**.
> The IA is documented in spreadsheets and diagrams, not in wireframes..."

### 3.2 决定顺序 [一手理论]

> "**Define the IA Before Designing Navigation** ... it is inefficient and even dangerous
> to do so [ignore IA]. Navigation that does not adequately accommodate the full scope of
> content and functionality of a site can be very costly."

**对本项目的映射**：上一轮我做的是「往现有导航里加按钮」，**没有先定义 IA**。
本方案的顺序修正为：先定 IA（`ia-redesign.md` §3）→ 再设计导航 → 再画原型。

---

## 4. Langfuse 真实产品源码（一手产品证据）

来源：GitHub `langfuse/langfuse`，`web/src/components/layouts/routes.tsx`
（通过 `gh api repos/.../contents/...` 直接读取源码，非文档描述）

### 4.1 其 IA 骨架 [一手产品]

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
  title: string;
  href: string;
  icon?: LucideIcon;
  items?: Array<Route>;          // ← 可嵌套
  section?: RouteSection;        // ← 顶/底分区
  group?: RouteGroup;            // ← 区内分组
  projectRbacScopes?: ProjectScope[];      // ← 权限过滤
  organizationRbacScope?: OrganizationScope;
  entitlements?: Entitlement[];
  productModule?: ProductModule;
  show?: (p) => boolean;         // ← 条件显示
};
```

### 4.2 提取到的真实导航条目 [一手产品]

从源码 `routes.tsx` 提取的 `title → href`：

| title | href | group |
|---|---|---|
| Home | `/` | — |
| Dashboards | `.../dashboards` | — |
| Tracing | `.../tracing` | Observability |
| Sessions | `.../sessions` | Observability |
| Users | `.../users` | Observability |
| Prompts | `.../prompts` | Prompt Management |
| Playground | `.../playground` | Prompt Management |
| Scores | `.../scores` | Evaluation |
| Evaluators | `.../evaluators` | Evaluation |
| Human Annotation | `.../annotation` | Evaluation |
| Datasets | `.../datasets` | Evaluation |
| Experiments | `.../experiments` | Evaluation |
| Settings | `.../settings` | — (Secondary) |

### 4.3 可借鉴的三点 [一手产品]

1. **`RouteGroup` 枚举**：一级导航不是功能列表，而是**能力域**（Observability /
   Prompt Management / Evaluation）。本项目对应的是「任务 / 数据资产 / 质量治理」。
2. **`section: Main | Secondary`**：主区（业务）与次区（设置）**物理分开**，
   不是靠 `adminOnly` 混在同一列表里 —— 这正是本项目「6 个管理项与业务项并列」的解法。
3. **`items?: Array<Route>`**：真产品**支持嵌套**，且 `projectRbacScopes` 做权限过滤。

---

## 5. 本仓库实测（Playwright）

### 5.1 侧边栏实测 [本仓库实测]

脚本：Playwright 真实 Chromium，1440×900，`admin@company.com`，
访问 `/console/home` 后读取侧边栏 DOM。

| 指标 | 实测值 |
|---|---|
| 侧边栏宽度 | 240 px |
| 普通用户可见一级项 | **9**（业务导航 7 + 系统设置）|
| 管理员可见一级项 | **13** |
| 截图 | `/tmp/ia-research/current-sidebar.png` |

### 5.2 19 个路由的信息架构性质分类 [本仓库实测]

从 `App.tsx` 的 `userPages` / `taskWorkbenchPages` / `resultWorkbenchPages` /
`adminPages` 常量提取：

| 性质 | 数量 | 路由 |
|---|---|---|
| 系统管理 | 6 | operations, admin/providers, admin/storage, admin/strategies, admin/prompts, admin/audit |
| **流程步骤** | **5** | domains, questions, reasoning, rewards, exports |
| 总览 | 2 | home, tasks |
| 资产工具 | 2 | evaluation, cleaning |
| 资产/动作/工具 | 各 1 | results / planning / help |

### 5.3 名称冲突 [本仓库实测，代码级]

| 常量位置 | 标签 | 路由 | 语义 |
|---|---|---|---|
| `userPages[4]` (`App.tsx:110`) | 质量评估 | `/console/evaluation` | 多模型互评 |
| `resultWorkbenchPages[2]` (`App.tsx:168`) | 质量评估 | `/console/rewards` | 流水线第 4 步 |

### 5.4 未完成的嵌套意图 [本仓库实测，代码级]

`App.tsx:116-118` 注释原文：

> 阶段工作台页面。阶段路由不是侧边栏项，用户是从任务详情页的阶段卡片进入的，
> 因此每个阶段必须额外声明它在侧边栏里的归属父项（navParent），否则处于该阶段时
> 侧边栏会高亮到不相关的默认项。

→ 作者已意识到阶段**不该是平级菜单项**，但只实现了「高亮归属」，未实现「二级菜单」。
