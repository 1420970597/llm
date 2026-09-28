# 2026-09-28 真实浏览器全量系统使用测试报告

> **代码基线**：`origin/main` @ `daebc32923e9c37b869b5ac55f470cffc3686a81`（本地 HEAD 与远程一致，无需 rebase）
> **部署自证**：`http://127.0.0.1:3210/version.json` = `daebc32923e9c37b869b5ac55f470cffc3686a81`（与源码一致 ✅）
> **测试方式**：真实 Chromium（Playwright，headless）+ 真实容器栈（api/worker/postgres/redis/minio）+ 真实 LLM
> **脚本**：`test/audit/usertest-20260928/run.mjs`（全路由走查 + 结构判定）
> **采集时间**：2026-09-28

---

## 0. 环境与数据基线

| 项 | 值 |
| --- | --- |
| 前端 | `http://127.0.0.1:3210`（`llm-web-user-1`） |
| 后端 | `llm-api-1` / `llm-worker-1` / `llm-postgres-1` / `llm-redis-1` / `llm-minio-1` |
| 账号 | `admin@company.com`（工作区管理员） |
| 视口 | 桌面 `1600×1000`；移动 `390×844` |
| 数据 | 项目 1 / 批次 5（含本次真实发起的 b_4）/ 旧数据集 58 / 样本 4 / 样本版本 9 |

本次**真实发起过**一次 4 单元试制批次（`b_4`），走完 `queued → running → completed 4/4`，
证明生成链路本身是通的（不是假功能）。但它同时把「今日工作读数」与「批次状态机」两处缺陷暴露出来。

---

## 1. 测试范围

### 1.1 全路由走查（31 条）

```
today, projects, recipes, deliveries, new(/new,/new/coverage,/new/quality),
activity, tools/evaluation, tools/cleaning, legacy/history,
settings/connections, settings/team, help, catalog, 未知路由(404),
p/1/{overview,blueprint,coverage,standard,runs,pilot,runs/new,compare,
     data,review,quality,quality/new,rules,releases,releases/new}
```

自动化判定维度：横向溢出、文本裁剪、控件重叠、Markdown/JSON/英文枚举泄漏、
`undefined/NaN/null` 字面量、无标签输入、JS 异常、5xx。

**结果**：无横向溢出、无文本裁剪（`scrollWidth > clientWidth` 且 `overflow:hidden`）、
无 JS 异常、无 5xx、无 `undefined/NaN` 泄漏。**布局与健壮性基线良好。**

### 1.2 业务流程深挖（真实操作）

覆盖：新建项目向导 → 蓝图 7 节点配置与保存 → 覆盖矩阵 m×n×z → 思维标准 →
真实发起试制批次（含真实 LLM 生成）→ 数据/审阅三栏判断 → 质量实验创建 →
清洗扫描发起 → 冻结范围 → 发布候选 → 交付 → 团队建号 → 全局搜索 → 移动端。

---

## 2. 确认的缺陷（按严重度）

### 🔴 D1. 今日工作「待人工判断」恒为 0，与审阅队列真实待办矛盾

| 证据 | 事实 |
| --- | --- |
| `GET /api/v1/today` → `overview.pendingReview` | **0** |
| `GET /api/v1/projects/1/samples?status=pending` | **3 条** |
| 审阅队列页 `/p/1/review` | 「待判断 **3** 条」 |
| 今日工作磁贴 | 「待人工判断 **0** · 进入项目「审阅」处理」 |
| SQL：`review_projections` 有效 pending | **0** |
| SQL：`samples` LEFT JOIN 投影（队列口径）为 pending | **3** |

**根因**：两处口径不同 —— `activity_store.go` 只 count `review_projections` 里
已存在的行；而审阅队列用 `COALESCE(rp.effective_action,'pending')`，把
**从未被判断过**（没有投影行）的内容也算作待判断。于是「一次都没判断过」的项目
在总览里显示 0 待办，用户据此认为没有活要干，而审阅队列里躺着 3 条。

**业务影响**：总览是「今天该做什么」的唯一入口。它把最该做的判断显示成 0，
与页面自己写的「总览里的每个数字都是计数，点进去看到的是同一份事实」直接矛盾。

**复现**：`curl -b cookie /api/v1/today` 与 `/api/v1/projects/1/samples?status=pending` 对比。

---

### 🔴 D2. 批次「已完成」却只有 1/4 产出，且无任何恢复入口（#190 修复不完整）

| 批次 | 计划 | 完成 | 失败 | 状态 | 缺口 | 可操作按钮 |
| --- | --- | --- | --- | --- | --- | --- |
| b_4 | 4 | 4 | 0 | completed | 0 | — |
| **b_2** | 4 | **1** | **0** | **completed** | **3** | **无** |
| b_3 | 4 | 4 | 0 | completed | 0 | — |
| b_1 | 12 | 0 | 12 | running | 12 | 暂停 |

`b_2` 是核心缺陷：**计划 4、产出 1、失败 0、剩余 3 条单元根本不存在**。
数据库里 `batch_items` 只有 1 行（`succeeded`），另外 3 条从未被写入。

- UI 顶部显示「部分完成 1/4」（`honestStatusLabel` 生效），**但状态字段是 `completed`**；
- `BatchCapabilitiesFor('completed')` → 全部 `false`，因此**界面上没有任何按钮**可以
  补齐这 3 条；
- `MarkRetryableItemsPending` 也无从下手：`retryable` 的失败项为 0；
- 时间线只有 `BatchQueued` + `BatchCompleted` 两条，**没有任何解释缺口的记录**。

**根因**：`RefreshBatchCounts` 的 `status == BatchStatusCompleted` 分支**提前返回**，
不再按「完成量 < 计划量」降级为 `partial_failed`：

```go
switch {
case status == model.BatchStatusCompleted || status == model.BatchStatusFailed:
    // 终态不被计数刷新改写（历史事实）。   ← b_2 永远卡在 completed
```

`#190` 修复了「计划 12 完成 1 时置 completed」的**推导路径**，但没有修掉
「状态已经是 completed 时不再复核」这条**历史路径**。于是 b_2 这类脏数据
永远不会被自我纠正，用户也没有出口。

---

### 🔴 D3. 批次「运行中」+ 12 条不可重试失败，形成永久僵尸批次

`b_1`：`planned=12, completed=0, failed=12, inFlight=0`，状态 `running`。

- 12 条单元全部 `config_error / retryable=false`（批次快照缺模型连接）；
- **不存在对应的 job**（`jobs` 表只有 1/2/3/4，没有 b_1 的后续作业）；
- `capabilities = {canPause: true, canResume: false, canRetryFailed: false}`；
- 点「暂停」后状态变 `pause_requested`，再点「继续」→ `BatchResumed` 事件写入，
  但**不产生任何作业、状态不回落到终态**，20 秒后仍是 `running`；
- 结果：批次永远显示「运行中」，而它既不会跑完，也不会给出任何恢复/终止入口。

**业务影响**：`今日工作 → 进行中批次` 长期显示 1，用户会一直等待一个永远不会推进的批次。

---

### 🟠 D4. 「按当前筛选冻结并准备发布」冻结的范围与按钮文案不符（把待审内容当已接纳）

| 入口 | 页面语义 | 冻结结果 |
| --- | --- | --- |
| `/p/1/data`（数据） | 默认「全部」 | **9 条**（含 3 条 pending） |
| 预览文案 | 「发布范围（**已接纳**的内容版本）」 | — |

产出流程：

1. 在「数据」页点「按当前筛选冻结并准备发布（服务端解析）」；
2. 发送 `POST /selection-snapshots {"purpose":"release","fromFilter":{}}`（**无 reviewStatus**）；
3. 服务端 `fromFilter.reviewStatus` 为空 → `ListSampleVersionIDsByFilter` 不做审阅状态过滤
   → 冻结 **9 条**（含 3 条 `pending`）；
4. 跳到「准备发布」页，提示「已从服务端选择范围恢复 9 个内容版本（快照 5）」，
   随后候选页的标题却写「发布范围（**已接纳**的内容版本）」。

**业务影响**：用户以为自己冻结的是「已接纳」范围，实际把未审阅内容也纳入候选。
语义与事实不一致比数值错误更危险 —— 后续门槛虽然会拦住，但用户已建立了错误的心智模型。

---

### 🟠 D5. 蓝图画布节点被右栏遮挡，点击命中右栏（三种不同反应）

`/p/1/blueprint` 实测几何：

```
画布      : x=225  width=790   scrollWidth=1886  (overflow-x: auto)
检查器右栏 : x=1062 width=370
```

| 节点 | x | 右边界 | 与右栏重叠 | 点击命中的元素 |
| --- | --- | --- | --- | --- |
| 覆盖范围 | 238 | 474 | 0 | node ✅ |
| 思维标准 | 508 | 744 | 0 | node ✅ |
| 生成 | 778 | 1014 | 0 | node ✅ |
| **独立评估** | 1048 | 1284 | **236px** | **inspector** ❌ |
| **规则检查** | 1318 | 1554 | **139px** | **inspector** ❌ |
| 人工检查点 | 1588 | 1824 | 0 | null（在视口外） |
| 版本交付 | 1858 | 2094 | 0 | null（在视口外） |

用**真实鼠标点击节点中心**（非 `force`）时，点「独立评估」和「规则检查」落在
右栏 `blueprint-inspector` 上，`?node=` 参数不变化，右栏仍停在「生成」。
视觉上节点看起来完整可见（画布 `overflow-x:auto` 不裁剪），但**不可点**。

更严重的是三种反应不一致：点「独立评估」→ 面板变「这个节点会做什么」（评估节点），
点「规则检查」→ 面板仍是「生成」，点「人工检查点」→ 面板空白。用户无法建立操作预期。

**业务影响**：`#197` 第 11 条要求的「点击某节点在右侧展示该流程信息」在 7 个节点里
只有前 3 个真正可达。

---

### 🟠 D6. 「产出缺口」磁贴指向 `/today` 自身，点击无任何反馈

| 磁贴 | href | 点击后 |
| --- | --- | --- |
| 数据项目 | `/projects` | ✅ 跳转 |
| **进行中批次** | **`/today`** | ❌ 原地不动 |
| **产出缺口** | **`/today`** | ❌ 原地不动 |
| 待人工判断 | `/activity` | ⚠️ 跳到动态（应去审阅） |
| **近 7 天产出** | **`/today`** | ❌ 原地不动 |
| 交付 | `/deliveries` | ✅ 跳转 |

6 个磁贴有 3 个指向当前页，2 个语义错误（「待人工判断」→ `/activity` 而不是
`/p/{id}/review`）。页面同时声明「总览里的每个数字都是计数…**点进去看到的是同一份事实**」，
但一半的磁贴点不进去。

---

### 🟡 D7. 失败/暂停批次没有「恢复入口」的闭环说明

`b_1` 详情页显示「计划 12，实际产出 0，缺口 12：覆盖率不足或无素材接地，
请补充方向配额/素材后重跑」，但：

- 真实原因是 **批次快照缺模型连接**（`config_error`），不是「覆盖率不足」；
- `ShortfallNote()` 对所有缺口都套同一句固定文案，与失败详情页的
  `suggestedAction`（「打开生成设置…再新建批次」）**互相矛盾**；
- 用户按缺口文案去改覆盖矩阵是无效操作。

---

### 🟡 D8. 帮助页承诺的 J/K 快捷键完全没有实现

`/help`「快捷键」区写着：

> · 审阅队列：**J / K** 在当前页内切换样本（保持筛选条件）

全仓库 `keydown` 监听只有 `StudioLayout.tsx` 的 `closeOnEscape`。
在 `/p/1/data/s_5` 按 J、K，URL 与内容均无变化。这是**文档承诺了不存在的功能**。

---

### 🟡 D9. 批次事件时间线直接显示英文内部事件键

`/p/1/runs/b_1`「事件时间线」逐条渲染原始 `eventType`：

```
BatchRetryFailedRequested / BatchResumed / BatchPaused / BatchCompleted / BatchPartialFailed
```

`RunPages.tsx:717` 是 `{event.eventType}` 原样输出。这与 issue #191
（内部英文事件键泄漏）是同一类缺陷的**未覆盖残留**：动态列表已修好（「批次已恢复」
「暂停批次」），但批次详情时间线没有走同一套展示层映射。

---

### 🟡 D10. 「数据」页冻结按钮文案与真实行为不一致（与 D4 同源，单列）

`/p/1/data` 主按钮写「按当前筛选冻结并准备发布（服务端解析）」，用户理解为
「导出/发布当前筛选」。但它是**唯一**入口，且不校验 reviewStatus；而
`/p/1/review` 队列模式（`queueMode`）**根本没有这个按钮**
（`data-snapshot-all` 计数 = 0）。于是：

- 「审阅」页无法冻结范围（说明文字却写「可用下方「按筛选条件冻结」把同一范围交给发布流程」）;
- 「数据」页能冻结，但冻结的是全量而非已接纳。

---

### 🟡 D11. 连接设置首屏被 6 条空记录占据

`/settings/connections` 模型连接表（11 条）中有 **6 条完全空白**记录
（name/baseUrl/model 全空），其中 **2 条 `isActive=true`**：

| id | name | baseUrl | model | isActive |
| --- | --- | --- | --- | --- |
| 13,14,17,18 | *(空)* | *(空)* | *(空)* | false |
| **15,19** | *(空)* | *(空)* | *(空)* | **true** |
| 16,20 | 自动服务-* | `not-a-url` | *(空)* | **true** |

这些空记录**同样出现在蓝图「生成 → 模型服务」下拉里**（`options` 含 6 个空项），
用户在 11 个选项里要辨认哪些可用。这是历史测试遗留数据未被清理，
但产品没有「无效连接」标记或过滤，用户无法区分「暂未配置」与「配置坏了」。

另外「密钥标识」列显示 `****ae67`（`MaskSecret` 只保留末 4 位），
但 **accessKey `minioadmin` 在存储表里是明文的**（`accessKeyId` 经 API 原样返回）。

---

### 🟡 D12. 「检查范围」用的是与审阅队列一致的口径，但 UI 不说明

`/p/1/quality/new` 的「检查范围」列出 4 条内容版本，审阅状态列显示
`accepted / pending`（英文枚举，属 #191 同类）。用户不知道该不该勾未审阅的内容；
页面文案只说「已选 N 个内容版本」，没有说明「未审阅内容是否可纳入评测」。

（本次因「全部裁判都与生成来源同源」被 422 拒绝，未能完成实验创建 ——
这是**正确的业务拦截**，属于诚实能力边界，不计为缺陷。）

---

### 🔵 D13. 活动行「查看」对审计类事件指向无信息的 `/p/1/overview`

`/activity` 中 `data-activity-item=audit` 的行（19 条）「查看」链接全部指向
`/p/1/overview`，而那条记录讲的是「恢复批次 / 暂停批次 / 创建发布候选」等
具体操作。`batch` 类的行正确指向 `/p/1/runs/{id}`。审计类没有落到对象详情。

---

### 🔵 D14. 「交付」磁贴的「被挡住 1」点进去看不到被挡住的候选

磁贴显示「已发布 0 · **被挡住 1**」，指向 `/deliveries`；而交付库按定义
**只显示已发布版本**，页面显示「还没有可交付的已发布版本」。
被挡住的候选在 `/p/1/releases`，磁贴没有给出通往它的路径。

---

### 🔵 D15. 全局搜索不覆盖批次与样本 ID，且无结果时文案有歧义

| 关键词 | 结果 |
| --- | --- |
| `方向一` | 2 条 sample ✅ |
| `冷链` | 1 条 project ✅ |
| `b_3` | **0 条** |
| `s_5` | **0 条** |

`/api/v1/search` 支持 project/sample，不支持批次资源 ID。用户手上有 `b_3`
这种从界面上抄下来的标识却搜不到。空结果文案写「没有匹配的页面或对象（只搜索你有权访问的内容）」，
把「不存在」与「无权限」混成一句。

---

## 3. 未复现为缺陷的项（诚实记录）

| 项 | 结论 |
| --- | --- |
| 全路由横向溢出 / 文本裁剪 | ❌ 未发现（移动端 390px 亦无） |
| JS 异常 / 5xx | ❌ 未发现（登录后控制台干净） |
| Markdown 星号泄漏 | ✅ 已修复（#192 生效，全树 0 处） |
| 动态列表英文枚举 | ✅ 已修复（#191 生效，全部中文） |
| 连接设置新增/编辑跳旧页 | ✅ 已修复（页内弹窗，含「测试连通性」） |
| 列表项元素级编辑按钮 | ✅ 已修复（每行「编辑 / 启用 / 停用」） |
| 团队新建用户（用户名+密码） | ✅ 真实可用（建号后新账号可登录） |
| 覆盖矩阵 m×n×z 结构树 | ✅ 已实现（含公式与可产出量） |
| 思考步骤自然语言模板 | ✅ 已实现（分步表单，JSON 为次视图） |
| 数据预览 | ✅ 已实现（分字段人话视图 + 原始 JSON 切换） |
| 生产页数据集自动分析 | ✅ 已实现（打开即算：P50/P90、难度占比、缺口） |
| 批次计划量超容量被拒绝 | ✅ 正确（422 + 可操作文案） |
| 质量实验同源裁判被拒绝 | ✅ 正确（422 + 明确原因） |
| 发布门槛拦未审阅内容 | ✅ 正确（`PENDING_REVIEW` + `QUALITY_TARGET_NOT_MET`） |
| 未知路由 404 页 | ✅ 已实现（含路径回显） |
| 移动端布局 | ✅ 无崩坏（卡片式表格 + `data-label`） |

---

## 4. 复现脚本

```bash
cd /root/llm
node test/audit/usertest-20260928/run.mjs    # 全路由走查 + 结构判定
```

产物：`test/artifacts/usertest-20260928/{routes.json,findings-stage1.json,*.png}`
