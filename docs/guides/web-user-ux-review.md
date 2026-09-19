# Web 控制台交互设计评审（操作流程与人类习惯的差距）

> 类型：**交互设计 / 信息架构评审**（设计建议，非功能缺陷）
> 对应 issue：[#84](https://github.com/1420970597/llm/issues/84)
> 配套事实来源：[`web-user-page-map.md`](./web-user-page-map.md)（页面现状，真实浏览器采集）
> 基线：`main` @ `080df69`；采集脚本见页面说明书 §0

---

## 0. 本文的证据分级（重要）

为避免把「调研结论」与「本仓库实测」混为一谈，本文用三种标记：

| 标记 | 含义 |
| --- | --- |
| **[实测]** | 本 lane 用真实浏览器在本仓库上采集/验证，附脚本与产物 |
| **[复算]** | 从本仓库源码按同源逻辑复算（给出文件:行），并经真机交叉验证 |
| **[引自 #84]** | 来自 issue #84 body 的调研（含其引用的外部 URL）。**本 lane 未独立复核外部文档**，仅采信其引用并注明 |

> 本 lane 环境没有网络访问工具，因此**不新增任何未核实的外部引用**。
> 所有外部对比均沿用 #84 已给出的 URL，并保持其原始措辞。

---

## 1. 结论速览

| # | 问题 | 严重度 | 证据 | 业界共识（引自 #84） |
| --- | --- | --- | --- | --- |
| 1 | 5 个阶段各需人工点一次「启动」 | 🔴 高 | **[实测]** 每阶段页恰好 1 个主操作按钮 | 一次提交后自动流转 |
| 2 | 阶段页**没有「下一步」** | 🔴 高 | **[实测]** 18 个控件中向前导航 = 0 | 向导应提供最短路径 |
| 3 | 完成一次流水线需 5 次往返 | 🔴 高 | **[实测+复算]** §2.3 | 同类项目约 3–5 步 |
| 4 | 新建任务页字段过多（管理员 9 个 / 普通用户 6 个） | 🟠 中 | **[实测]** §4.1 | 渐进披露 |
| 5 | **失败态显示「系统同步中，请稍后刷新。」** | 🔴 高 | **[实测+复算]** §3.1 | 失败必须给原因 + 恢复路径 |
| 6 | 三处导航对同一阶段指向不同页面 | 🟠 中 | **[实测]** §4.3 | — |

**一句话**：本系统把「一条数据流水线」实现成了「5 个需要人工逐一点火的独立任务」。
按 `功能说明.txt`「系统设计必须符合人机交互习惯」的要求，其中 **#2 与 #5 是必须修的**，
且两者改动面都很小（详见 §5）。

> **与 #84 原文的一处修正**：原文 §关键证据 2 把「等待期间是静态文案」列为普遍问题。
> 本 lane 实测发现**排队态其实有合理文案**（见 §3.2），静态兜底只发生在
> **失败态与未知状态**。因此本文把该条收敛为更精确的 §3.1/§3.2，严重度不变（失败态更严重）。

---

## 2. 主流程实测

### 2.1 阶段页确认「无下一步」[实测]

采集器（`test/l15_page_structure_capture.mjs`）真实渲染 5 个阶段页，
枚举所有**可见**的 `button / [role=button] / a[href]`（已排除侧边栏）：

| 阶段页 | 实测可见按钮 | 向前导航 |
| --- | --- | --- |
| `/console/domains` | 返回当前任务 / 返回我的任务 / 刷新结构 / 生成方向结构 | **0** |
| `/console/questions` | 返回我的任务 / 刷新结果 / 开始生成题目 | **0** |
| `/console/reasoning` | 返回我的任务 / 刷新结果 / 开始生成答案 | **0** |
| `/console/rewards` | 返回我的任务 / 刷新结果 / 开始质量评估 | **0** |
| `/console/exports` | 返回我的任务 / 刷新结果 / 开始导出结果 | **0** |

共 **18 个可见按钮，向前的导航数量为 0**。

对照：数据清洗页实测有 **4 个**阶段间导航按钮（「前往新建任务」「前往生成数据」
「前往质量评估」「前往导出交付」）。**同一个产品里已有阶段间导航的实现范式**，
说明这不是技术问题，而是 5 个阶段页漏做了。

### 2.2 每阶段一次人工点火 [实测+复算]

**[实测]** 每个阶段页恰好只有 1 个主操作按钮（上表最后一列）。

**[复算]** `apps/web-user/src/App.tsx:240-250` 的状态文案把「等待人工操作」写成了常态：

```text
draft:               '等待你确认主题结构'
domains_confirmed:   '等待你启动问题生成'
questions_generated: '等待你启动答案生成'
reasoning_generated: '等待你启动质量评分'
rewards_generated:   '等待你启动导出'
```

**5 个阶段全部是「等待你启动」。**

### 2.3 操作步数与往返 [实测+复算]

按页面说明书 §5.1 的实测结构（阶段页无向前导航 + 详情页是唯一枢纽）推导：

```text
 1. 登录
 2. 侧边栏「新建任务」→ /console/planning
 3. 填写任务主题 + 目标样本数
 4. 点「创建任务」→ 自动进入 /console/tasks/{id}
 5. 点「第 1 步」卡片 → /console/domains
 6. 点「生成方向结构」
 7. ★ 返回当前任务（往返 1）
 8. 点「第 2 步」卡片 → /console/questions
 9. 点「开始生成题目」
10. ★ 返回当前任务（往返 2）
11. 点「第 3 步」卡片 → /console/reasoning
12. 点「开始生成答案」
13. ★ 返回当前任务（往返 3）
14. 点「第 4 步」卡片 → /console/rewards
15. 点「开始质量评估」
16. ★ 返回当前任务（往返 4）
17. 点「第 5 步」卡片 → /console/exports
18. 点「开始导出结果」
19. 下载结果
```

**19 步、5 次「返回详情 → 找卡片 → 点生成」往返。**
（往返次数 = 阶段数 − 1 = 4 次回到详情 + 1 次回列表取结果 = 5，与 #84 原文一致。）

### 2.4 枢纽单点依赖 [实测]

**[实测]** 5 张阶段卡片实测落点（`test/artifacts/page-structure/hub-and-form.json`）：

```text
第 1 步：主题结构  -> /console/domains
第 2 步：问题生成  -> /console/questions
第 3 步：答案内容  -> /console/reasoning
第 4 步：质量评估  -> /console/rewards
第 5 步：导出交付  -> /console/exports
```

且这 5 个阶段页**不在侧边栏**（实测侧边栏 7 个业务项里没有它们）。
因此**任务详情页是唯一入口** —— 该页不可达则整条流水线瘫痪，这正是 #61 的破坏力来源（已修复）。

**补充观察 [实测]**：本轮 #65/R10 又在任务详情页新增了「进阶能力」面板
（7 项能力 × 执行/刷新 = 14 个按钮）。枢纽页职责继续膨胀，单点依赖风险同步上升。

---

## 3. 状态可见性（本文最严重的发现）

### 3.1 🔴 失败态显示「系统同步中，请稍后刷新。」[实测+复算]

**[实测]** 共享库中存在真实失败数据集（`datasets.id=47`，`status='chain-standards.generate_failed'`）。
真实浏览器打开 `/console/tasks/47`，页面首个摘要区块的**实际可见文本**：

```text
chain-standards.generate_failed | 进度 20% | ETA: 刷新后更新 ETA | 更新 09/19 07:13 | 系统同步中，请稍后刷新。
```

同时实测确认：

- 页面出现 `系统同步中，请稍后刷新。` → **true**
- 页面出现 `生成失败 / 已失败 / 失败原因` 字样 → **false**
- 页面**直接泄漏原始状态码** `chain-standards.generate_failed` 给用户

**[复算]** `apps/web-user/src/App.tsx:264-284` 的 `waitingReasonLabel` 是个 `switch`，
只覆盖 6 个「正常」状态 + 后缀 `_queued`；**其余一切落进 `default:`**。
逐状态复算结果：

| status | 等待原因文案 |
| --- | --- |
| `draft` | 结构未确认，尚未开始生成。 |
| `domains_confirmed` | 结构已确认，等待你启动问题生成。 |
| `*_queued` | 该阶段已入队，正在等待执行资源分配。 |
| **`questions_failed`** | **系统同步中，请稍后刷新。** |
| **`reasoning_failed`** | **系统同步中，请稍后刷新。** |
| **`rewards_failed`** | **系统同步中，请稍后刷新。** |
| **`export_failed`** | **系统同步中，请稍后刷新。** |
| **`reasoning_partial` / `*_partial_failed`** | **系统同步中，请稍后刷新。** |
| **`chain-standards.generate_failed`** | **系统同步中，请稍后刷新。** |
| **`directions_completed`** | **系统同步中，请稍后刷新。** |

**即：所有失败态都显示为「系统同步中」** —— 用户会以为系统在正常工作，
实际上流水线已经停了。这比「不显示原因」（#83）更严重：它**给出了错误的乐观信号**。

#### 更深一层：失败原因其实**已经落库**，只是没显示 [实测]

```bash
docker exec llm-postgres-1 psql -U llm_factory -d llm_factory -tAc \
  "select stage,status,error_summary from generation_runs where dataset_id=47 order by id desc limit 4"
```

```text
eval.run              | completed      |
directions            | running        |
chain-standards.generate | partial_failed | 3/4 个方向生成失败
directions            | completed      |
```

`generation_runs.error_summary` **已经存了可读的中文原因**（「3/4 个方向生成失败」）。
但 `PipelineStageStatus` 类型（`lib/api.ts:532-538`）只有
`{key,label,state,count,summary}`，**没有承载 error_summary 的字段**，
所以前端根本拿不到它。

#### 附：未知状态的连带影响 [复算+实测]

`internal/store/dataset_store.go:366-395` 用三个映射把 `datasets.status` 翻译成阶段状态：
`rankByStatus`（进度排名）、`queuedStageByStatus`、`failedStageByStatus`。
`chain-standards.generate_failed`、`directions_completed`、`grpo_queued` 等状态
**在三个映射里都不存在**，于是：

```text
status                           rank             failedStage
questions_failed                 rank=2           questions
chain-standards.generate_failed  rank=undefined   (无)
directions_completed             rank=undefined   (无)
grpo_queued                      rank=undefined   (无)
```

Go 中 map 未命中的零值是 `0`，因此这些数据集的 `statusRank = 0`，
**所有阶段都被算成 pending/in-progress**。
这与 API 实测输出一致（`/datasets/47/pipeline/progress` 返回
`domains.state="in_progress"`、`questions/reasoning/rewards.state="pending"`，
而该数据集其实在 chain-standards 阶段就失败了）。

### 3.2 ✅ 修正：#84 对「等待期间静态文案」的表述需收敛 [实测]

#84 原文把「等待期间是静态文案」列为普遍问题。本 lane 实测发现**不成立**：

```text
# datasets.id=87, status='grpo_queued' → /console/tasks/87 首个区块实测文本：
grpo_queued | 进度 0% | ETA: 预计处理中 | 更新 09/19 07:48 | 该阶段已入队，正在等待执行资源分配。
```

**排队态有明确文案，且给出了 ETA 与进度百分比。**
前端也确实在消费 `completionPercent`（`App.tsx:2199`、`:2651` 实测引用）。

因此准确的结论是：
- **排队/正常态：可见性好**（有进度、有 ETA、有原因）；
- **失败/未知态：退化为静态「系统同步中」**（§3.1）。

建议按此收敛，把整改力量集中在失败态（收益最大）。

---

## 4. 其它设计问题

### 4.1 🟠 新建任务页字段过多 [实测]

用**管理员**与**普通用户**两个账号分别采集 `/console/planning` 的可见字段标签：

| 身份 | 实测可见字段 | 数量 |
| --- | --- | --- |
| `user@company.com`（role=user） | 任务名称（可选）/ 任务主题 / 目标样本数（条）/ 领域数 n / 每领域方向数 m / 每方向问题数 x | **6** |
| `admin@company.com`（role=admin） | 上一行 6 个 + 生成策略 / AI 服务 / 存储配置 | **9** |

**[实测]** 页面自身说明写着「只填任务主题和目标规模即可」，而界面同时呈现 6–9 个字段 ——
**文案与界面背离**。

> **与 #84 原文的差异**：原文记录 6 个字段（含 3 个管理员配置）。
> 本轮 #85（n/m/x 用户可控，`功能说明.txt` 步骤 1/3 的硬要求）又加了 3 个，
> 因此管理员视图现为 **9 个**。原文的「6 个」已过时，但**问题方向未变、且更严重**。

**注意**：n/m/x 不能简单删掉 —— 它们是产品需求明确要求的用户可控项。
应做**分组/折叠**而不是移除（§5.4）。

### 4.2 🟠 管理员配置混入普通创建流程 [实测]

「生成策略 / AI 服务 / 存储配置」与业务必填项同屏。
**实测后果**已在 issue #83 记录：存储未配置会导致答案阶段必然失败，
而用户在创建任务这一步完全看不出这个前置条件。

### 4.3 🟠 三处导航对同一阶段指向不同页面 [实测]

| 阶段 | 侧边栏 | 任务详情卡片 | 数据清洗流程条 |
| --- | --- | --- | --- |
| 生成数据 | （无） | `/console/domains` … `/console/rewards` | `/console/tasks` |
| 质量评估 | `/console/evaluation` | **`/console/rewards`** | **`/console/evaluation`** |
| 导出交付 | （在「数据资产」内） | **`/console/exports`** | **`/console/results`** |

实测来源：清洗页可见按钮「前往质量评估」「前往导出交付」的目标由
`apps/web-user/src/views/cleaning/CleaningFlowSteps.tsx:22-26` 的 `route` 字段给出
（`/console/evaluation`、`/console/results`）；任务详情卡片的落点见 §2.4 实测。

另：`/console/rewards` 与 `/console/evaluation` 的**入口文案都叫「质量评估」**，
但实测页面标题分别是「质量评分结果中心」与「质量评估」（见页面说明书 §6.1）。

### 4.4 🟡 运营监控入口默认收起 [实测]

「系统设置」分组默认收起（实测默认状态只能看到组标题），管理员需先展开才见 6 个子项。

> 这条同时解释了一个测试缺陷：本轮 `test/l15_browser_human_e2e.mjs` 早期版本
> 未先展开就去点「运营监控」，导致 `scrollIntoViewIfNeeded` 超时误报。
> 已修复为该脚本显式先展开分组。

---

## 5. 改进建议（按优先级，含改动面）

### 5.1 🔴 P0 — 失败态必须显示真实原因与恢复路径

**问题**：§3.1。失败显示为「系统同步中」，且原始状态码直接泄漏给用户。

**为什么这是 P0**：它是**错误信号**（用户以为在跑，实际已停），
比 #83 的「不显示原因」更危险；而 `error_summary` **已经落库**，成本极低。

**建议**（三步，可独立落地）：

1. **后端透出原因**：给 `PipelineStageStatus`（`internal/model/dataset.go:61`）加一个
   可选字段（例如 `errorSummary string` / `errorCode string`），
   在 `internal/store/dataset_store.go` 组装阶段状态时，从 `generation_runs.error_summary`
   取该阶段的最近一次失败原因填入。**这是纯增量字段，不破坏既有响应形状。**
2. **前端停止兜底成「同步中」**：`waitingReasonLabel` 增加失败态分支
   （`*_failed` / `*_partial` / 未知状态），文案明确说「该阶段失败」并把 `errorSummary` 展示出来。
3. **不再泄漏原始状态码**：当前实测把 `chain-standards.generate_failed` 直接渲染给用户。
   建议映射为中文（如「方向标准生成失败」），原始码放进「详情/技术信息」折叠区。

**改动面**：后端 1 个 struct + 1 处组装（小）；前端 1 个函数 + 1 处渲染（小）。
**不需要新迁移**（`error_summary` 字段已存在）。

### 5.2 🔴 P0 — 阶段页补「下一步」

**问题**：§2.1 实测 0 个向前导航，导致 §2.3 的 5 次往返。

**建议**：在阶段页 PageHeader 增加一个向前按钮：

```text
[返回当前任务] [刷新] [下一步：{下一阶段名} →]
```

`statusToActionRoute()`（`App.tsx:345`）已含完整映射，可直接复用。
**同仓库已有范式**：`views/cleaning/CleaningFlowSteps.tsx` 用
`{key,label,detail,route}[]` 数组驱动阶段跳转，可抽成共享组件避免再次漂移
（与 #79 用「单一来源派生」修 `stageRouteNavMap` 是同一手法）。

**改动面**：前端 1 个共享步骤组件 + 5 个阶段页各接一次（小–中）。
**无需后端改动**。

### 5.3 🟠 P1 — 新建任务页渐进披露

**问题**：§4.1（管理员 9 个字段 / 普通用户 6 个）。

**建议**：
1. 默认只显示 **任务名称（可选）/ 任务主题 / 目标样本数**；
2. **n / m / x** 收进「生成参数（可选）」折叠区 —— 它们是需求要求的用户可控项，
   但 R13 已把 `0` 定义为「未设置、走后端默认」，因此折叠**不丢能力**；
3. **生成策略 / AI 服务 / 存储配置**收进「高级设置」，仅管理员可见、默认折叠；
4. 页面说明文案与界面保持一致（不再写「只填主题和规模即可」）；
5. **存储未配置时前置提示**（与 #83 联动），不要把失败推到答案阶段。

**改动面**：前端单页布局调整（中）。代码里已有 `showAdvancedPlanning` 开关可复用。

**参考 [引自 #84]**：Label Studio 创建弹窗「Project Name 是唯一必填项」
（<https://labelstud.io/guide/setup_project.html>）；
NN/g「渐进披露」（<https://www.nngroup.com/articles/progressive-disclosure/>）。

### 5.4 🟠 P1 — 消除「三处导航指向不同」（§4.3）

**建议**：以**任务详情页的 5 个阶段页**为唯一权威，让
侧边栏、清洗流程条、数据资产页的跳转全部指向同一批路由。
手法建议复用「单一来源派生」（把阶段路由表作为唯一常量，其余引用它）。

**同时消除同名冲突**：

| 现状 | 建议 |
| --- | --- |
| 侧边栏「质量评估」= `/console/evaluation` | 改名 **「多模型互评」**（与其 caption 一致） |
| 阶段 4 卡片「质量评估」= `/console/rewards` | 保留「质量评分」 |

**改动面**：前端文案 + 路由常量集中（中）。

### 5.5 🟡 P2 — 术语统一

`功能说明.txt` 的原文用词是 n 个**「领域」**与 m 个**「方向」**；
UI 里另有「一级方向 / 主题结构 / 方向结构」等混用。
建议以需求原文为准建立术语表并在 UI 统一。

**改动面**：文案（小），但需全站一致性检查。

---

## 6. 本 lane 未做的改进及理由

| 项 | 未做原因 |
| --- | --- |
| 不实现 §5.1–§5.5 的代码改动 | 本 lane 定位为**文档与设计**（issue #90/#84 均要求「梳理 / 建议」而非立即改码）。且 §5.2 与 §5.4 都触及 5 个阶段页与路由常量，属结构性改动，应交由独立 lane 并在有回归守卫的前提下进行。 |
| 不新增外部项目对比 | 本 lane 无网络访问工具，无法独立核实外部文档行为。全部外部对比沿用 #84 已给的 URL 并明确标注 **[引自 #84]**，不新增未核实内容。 |
| 不改 `App.tsx` | 避免与并行 lane（R15/R16）及后续修复产生文件冲突；本文的精确「文件:行」定位已足够支撑后续 lane 实施。 |

---

## 7. 建议的落地顺序（供父代理决定是否开第四波）

| 顺序 | 项 | 理由 |
| --- | --- | --- |
| 1 | §5.1 失败态显示原因 | P0；`error_summary` 已落库，成本最低、收益最高（消除错误信号） |
| 2 | §5.2 阶段页「下一步」 | P0；消除 5 次往返；同仓库已有范式 |
| 3 | §5.3 渐进披露 | P1；与 #83 联动 |
| 4 | §5.4 导航目标统一 + 改名 | P1；可合并入第 2 项的同一次结构性改动 |
| 5 | §5.5 术语统一 | P2；文案层 |

---

## 8. 参考来源

**本仓库实测产物**（本 lane 生成，可复跑）：

| 产物 | 说明 |
| --- | --- |
| `test/l15_page_structure_capture.mjs` | 18 个路由的真实 DOM 采集（标题/控件/空状态） |
| `test/l15_hub_and_form_capture.mjs` | 枢纽页阶段卡片实测落点 + 新建任务表单字段 |
| `test/artifacts/page-structure/page-structure.json` | 采集产物（已 gitignore） |
| `test/artifacts/page-structure/hub-and-form.json` | 采集产物（已 gitignore） |

**本仓库源码定位**（本文每条结论都给了文件:行）：

| 位置 | 用途 |
| --- | --- |
| `apps/web-user/src/App.tsx:240-284` | 状态文案与等待原因（§3.1） |
| `apps/web-user/src/App.tsx:345` | `statusToActionRoute` 阶段映射（§5.2） |
| `apps/web-user/src/views/cleaning/CleaningFlowSteps.tsx:22-26` | 清洗页阶段跳转定义（§4.3、§5.2 范式） |
| `internal/store/dataset_store.go:366-412` | 阶段状态/进度排名映射（§3.1 附） |
| `internal/model/dataset.go:61-67` | `PipelineStageStatus`（§5.1 第 1 步） |
| `apps/web-user/src/lib/api.ts:532-538` | 前端同类型（§5.1 第 1 步） |

**外部参考（均为 [引自 #84]，本 lane 未独立复核）**：

| 来源 | URL |
| --- | --- |
| Label Studio — Set up your labeling project | <https://labelstud.io/guide/setup_project.html> |
| Dify — Create Knowledge Pipeline | <https://docs.dify.ai/en/cloud/use-dify/knowledge/knowledge-pipeline/create-knowledge-pipeline> |
| Dify — Upload Files | <https://docs.dify.ai/en/cloud/use-dify/knowledge/knowledge-pipeline/upload-files> |
| Argilla — Create and update a dataset | <https://docs.v1.argilla.io/en/latest/practical_guides/create_update_dataset/create_dataset.html> |
| NN/g — Progressive Disclosure | <https://www.nngroup.com/articles/progressive-disclosure/> |
| NN/g — Wizards: Definition and Design Recommendations | <https://www.nngroup.com/articles/wizards/> |
| NN/g — Progress Indicators | <https://www.nngroup.com/articles/progress-indicators/> |

## 9. 关联 issue

| Issue | 关系 |
| --- | --- |
| [#90](https://github.com/1420970597/llm/issues/90) | 页面设计说明书（本文的结构层基础） |
| [#83](https://github.com/1420970597/llm/issues/83) | 答案生成失败不可见 —— 本文 §5.1 是其前端侧对应项 |
| [#64](https://github.com/1420970597/llm/issues/64) | 静默失败（已修复并合并）；同属「可见系统状态」缺失 |
| [#61](https://github.com/1420970597/llm/issues/61) | 枢纽页断裂即全瘫（已修复并合并）—— 本文 §2.4 记录其设计成因 |
| [#65](https://github.com/1420970597/llm/issues/65) | 缺 UI 入口 + 命名误导（已修复并合并）—— 本文 §5.5 术语项 |
| [#85](https://github.com/1420970597/llm/pull/85) | n/m/x 用户可控 —— 使 §4.1 字段数从 6 增至 9 |
| [#88](https://github.com/1420970597/llm/issues/88) | 部署镜像落后 —— 页面说明书 §0 解释了为何必须自己构建再采集 |
