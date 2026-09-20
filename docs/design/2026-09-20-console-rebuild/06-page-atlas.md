# 全页面原型图册

本图册由页面字典和 Chromium 实际截图生成。截图使用 1440px 宽，长页面保留完整高度。每张图对应单文件原型中的实际页面，不是文字占位。

[返回方案总览](README.md) · [交互规格](03-interaction-spec.md) · [下载可点击原型](prototype.html)

## 页面索引

| ID | 页面 | 原型地址 | 主要职责 |
|---|---|---|---|
| G01 | [登录](#g01-登录) | `/login` | 邮箱登录，回到登录前访问的对象 |
| G02 | [工作台](#g02-工作台) | `/home` | 待办、近期任务和最近交付，直接进入处理对象 |
| G03 | [通知中心](#g03-通知中心) | `/notifications` | 任务完成、异常和待处理提醒 |
| G04 | [账户](#g04-账户) | `/account` | 当前身份、偏好与退出登录 |
| G05 | [帮助中心](#g05-帮助中心) | `/help` | 按业务问题查找操作指引 |
| G06 | [创建第一份训练数据](#g06-创建第一份训练数据) | `/help/first-dataset` | 从目标到交付的完整教程 |
| D01 | [数据集](#d01-数据集) | `/datasets` | 跨任务检索内容资产 |
| D02 | [仓储调度 · SFT](#d02-仓储调度-sft) | `/datasets/24` | 内容、来源、质量与交付概览 |
| D03 | [样本浏览](#d03-样本浏览) | `/datasets/24/samples` | 按状态、方向和质量筛选样本 |
| D04 | [样本 #1024](#d04-样本-1024) | `/datasets/24/samples/1024` | 问题、推理、答案及评估证据 |
| D05 | [导出记录](#d05-导出记录) | `/datasets/24/exports` | 查看同一数据集的每次交付 |
| D06 | [创建导出](#d06-创建导出) | `/datasets/24/exports/new` | 范围、格式、字段预览及完整性检查 |
| D07 | [导出 #81](#d07-导出-81) | `/datasets/24/exports/81` | 可追溯的导出配置、状态和下载 |
| T01 | [生产任务](#t01-生产任务) | `/tasks` | 按状态筛选执行任务并进入详情 |
| T02 | [创建生产任务](#t02-创建生产任务) | `/tasks/new` | 目标、结构规模、生成方案三步向导 |
| T03 | [仓储调度 · 生产任务](#t03-仓储调度-生产任务) | `/tasks/24` | 进度、当前待办和最近运行 |
| T04 | [领域与方向](#t04-领域与方向) | `/tasks/24/structure` | 生成、编辑、校验并确认结构 |
| T05 | [思维标准](#t05-思维标准) | `/tasks/24/standards` | 按方向管理长链思维步骤 |
| T06 | [订单分配 · 思维标准](#t06-订单分配-思维标准) | `/tasks/24/standards/12` | 逐步编辑、保存版本并比较历史 |
| T07 | [问题集](#t07-问题集) | `/tasks/24/questions` | 生成、难度分布及问题预览 |
| T08 | [训练产物](#t08-训练产物) | `/tasks/24/output` | 根据训练类型查看 SFT 或 GRPO 产物 |
| T09 | [运行记录](#t09-运行记录) | `/tasks/24/runs` | 每个阶段的执行历史、错误及恢复 |
| T10 | [运行 #104](#t10-运行-104) | `/tasks/24/runs/104` | 失败影响、已完成范围和续跑入口 |
| T11 | [自动评分（历史流程）](#t11-自动评分历史流程) | `/tasks/24/scoring` | 承接既有 rewards 记录，与多模型评估分开 |
| E01 | [质量评估](#e01-质量评估) | `/evaluations` | 默认进入评估运行列表 |
| E02 | [新建评估](#e02-新建评估) | `/evaluations/new` | 数据范围、维度和独立裁判配置 |
| E03 | [仓储调度 · 双模型评估](#e03-仓储调度-双模型评估) | `/evaluations/36` | 运行状态、配置和当前结论 |
| E04 | [逐条评分](#e04-逐条评分) | `/evaluations/36/scores` | 按样本、裁判、维度定位分歧 |
| E05 | [样本 #1024 · 评分证据](#e05-样本-1024-评分证据) | `/evaluations/36/items/1024` | 多裁判理由并排对照 |
| E06 | [评估报告](#e06-评估报告) | `/evaluations/36/report` | 分布、覆盖范围及改进建议 |
| C01 | [数据清洗](#c01-数据清洗) | `/cleaning` | 清洗运行列表与报告入口 |
| C02 | [新建清洗](#c02-新建清洗) | `/cleaning/new` | 阶段、规则、预览命中与执行 |
| C03 | [仓储调度 · 清洗 #18](#c03-仓储调度-清洗-18) | `/cleaning/18` | 扫描、命中、隔离及复查统计 |
| C04 | [命中明细](#c04-命中明细) | `/cleaning/18/findings` | 筛选规则和处置状态，进入单条复查 |
| C05 | [命中 #7 · 人工复查](#c05-命中-7-人工复查) | `/cleaning/18/findings/7` | 上下文高亮及保留/隔离决策 |
| R01 | [资源库](#r01-资源库) | `/resources` | 生成模板、指令、维度、规则、关键词与字段映射 |
| R02 | [生成模板](#r02-生成模板) | `/resources/templates` | 复用目标类型、规模和生成偏好 |
| R03 | [标准 SFT 模板](#r03-标准-sft-模板) | `/resources/templates/1` | 查看参数、用于新任务或编辑 |
| R04 | [评估维度](#r04-评估维度) | `/resources/dimensions` | 内置 58 项与自定义维度分别检索 |
| R05 | [逻辑一致性](#r05-逻辑一致性) | `/resources/dimensions/1` | 评分细则、分值与权重 |
| R06 | [清洗规则](#r06-清洗规则) | `/resources/rules` | 规则执行范围、动作和启用状态 |
| R07 | [拒答文本复查](#r07-拒答文本复查) | `/resources/rules/1` | 命中条件、阶段和处置配置 |
| R08 | [关键词库](#r08-关键词库) | `/resources/keywords` | 匹配模式、类别及启用状态 |
| R09 | [关键词 · 对不起](#r09-关键词-对不起) | `/resources/keywords/1` | 查看和编辑单个匹配项 |
| R10 | [批量导入关键词](#r10-批量导入关键词) | `/resources/keywords/import` | 逐行解析、去重预览后导入 |
| R11 | [生成指令](#r11-生成指令) | `/resources/prompts` | 按生成阶段管理提示词 |
| R12 | [仓储问题生成指令](#r12-仓储问题生成指令) | `/resources/prompts/1` | 系统指令、用户指令及版本 |
| R13 | [导出字段映射](#r13-导出字段映射) | `/resources/mappings` | 按训练类型与格式管理映射 |
| R14 | [SFT · Alpaca 映射](#r14-sft-alpaca-映射) | `/resources/mappings/1` | 源字段、目标字段和导出预览 |
| A01 | [系统管理](#a01-系统管理) | `/admin` | 服务就绪、队列和配置问题 |
| A02 | [模型服务](#a02-模型服务) | `/admin/providers` | 连接配置与可用性 |
| A03 | [模型服务 · Atlas](#a03-模型服务-atlas) | `/admin/providers/1` | 连接、测试、模型列表及密钥管理 |
| A04 | [结果存储](#a04-结果存储) | `/admin/storage` | 可用存储与默认落点 |
| A05 | [主存储](#a05-主存储) | `/admin/storage/1` | 连接配置和历史产物关联 |
| A06 | [操作记录](#a06-操作记录) | `/admin/audit` | 按对象、时间和动作检索 |
| A07 | [操作记录 #91](#a07-操作记录-91) | `/admin/audit/91` | 变更前后对比与对象跳转 |
| D08 | [仓储调度 · GRPO](#d08-仓储调度-grpo) | `/datasets/25` | GRPO 资产、教师评判标准与交付 |
| D09 | [GRPO 样本列表](#d09-grpo-样本列表) | `/datasets/25/samples` | 问题与教师提示词的完整性 |
| D10 | [GRPO 样本 #2024](#d10-grpo-样本-2024) | `/datasets/25/samples/2024` | 教师提示词、奖励档位与框架引用 |
| D11 | [创建 GRPO 导出](#d11-创建-grpo-导出) | `/datasets/25/exports/new` | GRPO 字段预览和类型完整性 |
| D12 | [GRPO 导出 #82](#d12-grpo-导出-82) | `/datasets/25/exports/82` | 教师评判训练文件下载 |
| T12 | [思维标准 · v2 快照](#t12-思维标准-v2-快照) | `/tasks/24/standards/12/versions/2` | 只读历史版本，与当前编辑页分开 |
| E07 | [评估维度 · 运行快照](#e07-评估维度-运行快照) | `/evaluations/36/dimensions/logic-v1` | 本次评分使用的量表、权重和细则 |
| C06 | [GRPO 问题清洗 #19](#c06-grpo-问题清洗-19) | `/cleaning/19` | 只扫描 GRPO 问题，教师提示词适配待补 |

## 全局

<a id="g01"></a>

### G01 · 登录

地址：`/login`。邮箱登录，回到登录前访问的对象。

![G01 登录](assets/screens/G01.png)

<a id="g02"></a>

### G02 · 工作台

地址：`/home`。待办、近期任务和最近交付，直接进入处理对象。

![G02 工作台](assets/screens/G02.png)

<a id="g03"></a>

### G03 · 通知中心

地址：`/notifications`。任务完成、异常和待处理提醒。

![G03 通知中心](assets/screens/G03.png)

<a id="g04"></a>

### G04 · 账户

地址：`/account`。当前身份、偏好与退出登录。

![G04 账户](assets/screens/G04.png)

<a id="g05"></a>

### G05 · 帮助中心

地址：`/help`。按业务问题查找操作指引。

![G05 帮助中心](assets/screens/G05.png)

<a id="g06"></a>

### G06 · 创建第一份训练数据

地址：`/help/first-dataset`。从目标到交付的完整教程。

![G06 创建第一份训练数据](assets/screens/G06.png)


## 数据集

<a id="d01"></a>

### D01 · 数据集

地址：`/datasets`。跨任务检索内容资产。

![D01 数据集](assets/screens/D01.png)

<a id="d02"></a>

### D02 · 仓储调度 · SFT

地址：`/datasets/24`。内容、来源、质量与交付概览。

![D02 仓储调度 · SFT](assets/screens/D02.png)

<a id="d03"></a>

### D03 · 样本浏览

地址：`/datasets/24/samples`。按状态、方向和质量筛选样本。

![D03 样本浏览](assets/screens/D03.png)

<a id="d04"></a>

### D04 · 样本 #1024

地址：`/datasets/24/samples/1024`。问题、推理、答案及评估证据。

![D04 样本 #1024](assets/screens/D04.png)

<a id="d05"></a>

### D05 · 导出记录

地址：`/datasets/24/exports`。查看同一数据集的每次交付。

![D05 导出记录](assets/screens/D05.png)

<a id="d06"></a>

### D06 · 创建导出

地址：`/datasets/24/exports/new`。范围、格式、字段预览及完整性检查。

![D06 创建导出](assets/screens/D06.png)

<a id="d07"></a>

### D07 · 导出 #81

地址：`/datasets/24/exports/81`。可追溯的导出配置、状态和下载。

![D07 导出 #81](assets/screens/D07.png)

<a id="d08"></a>

### D08 · 仓储调度 · GRPO

地址：`/datasets/25`。GRPO 资产、教师评判标准与交付。

![D08 仓储调度 · GRPO](assets/screens/D08.png)

<a id="d09"></a>

### D09 · GRPO 样本列表

地址：`/datasets/25/samples`。问题与教师提示词的完整性。

![D09 GRPO 样本列表](assets/screens/D09.png)

<a id="d10"></a>

### D10 · GRPO 样本 #2024

地址：`/datasets/25/samples/2024`。教师提示词、奖励档位与框架引用。

![D10 GRPO 样本 #2024](assets/screens/D10.png)

<a id="d11"></a>

### D11 · 创建 GRPO 导出

地址：`/datasets/25/exports/new`。GRPO 字段预览和类型完整性。

![D11 创建 GRPO 导出](assets/screens/D11.png)

<a id="d12"></a>

### D12 · GRPO 导出 #82

地址：`/datasets/25/exports/82`。教师评判训练文件下载。

![D12 GRPO 导出 #82](assets/screens/D12.png)


## 生产任务

<a id="t01"></a>

### T01 · 生产任务

地址：`/tasks`。按状态筛选执行任务并进入详情。

![T01 生产任务](assets/screens/T01.png)

<a id="t02"></a>

### T02 · 创建生产任务

地址：`/tasks/new`。目标、结构规模、生成方案三步向导。

![T02 创建生产任务](assets/screens/T02.png)

<a id="t03"></a>

### T03 · 仓储调度 · 生产任务

地址：`/tasks/24`。进度、当前待办和最近运行。

![T03 仓储调度 · 生产任务](assets/screens/T03.png)

<a id="t04"></a>

### T04 · 领域与方向

地址：`/tasks/24/structure`。生成、编辑、校验并确认结构。

![T04 领域与方向](assets/screens/T04.png)

<a id="t05"></a>

### T05 · 思维标准

地址：`/tasks/24/standards`。按方向管理长链思维步骤。

![T05 思维标准](assets/screens/T05.png)

<a id="t06"></a>

### T06 · 订单分配 · 思维标准

地址：`/tasks/24/standards/12`。逐步编辑、保存版本并比较历史。

![T06 订单分配 · 思维标准](assets/screens/T06.png)

<a id="t07"></a>

### T07 · 问题集

地址：`/tasks/24/questions`。生成、难度分布及问题预览。

![T07 问题集](assets/screens/T07.png)

<a id="t08"></a>

### T08 · 训练产物

地址：`/tasks/24/output`。根据训练类型查看 SFT 或 GRPO 产物。

![T08 训练产物](assets/screens/T08.png)

<a id="t09"></a>

### T09 · 运行记录

地址：`/tasks/24/runs`。每个阶段的执行历史、错误及恢复。

![T09 运行记录](assets/screens/T09.png)

<a id="t10"></a>

### T10 · 运行 #104

地址：`/tasks/24/runs/104`。失败影响、已完成范围和续跑入口。

![T10 运行 #104](assets/screens/T10.png)

<a id="t11"></a>

### T11 · 自动评分（历史流程）

地址：`/tasks/24/scoring`。承接既有 rewards 记录，与多模型评估分开。

![T11 自动评分（历史流程）](assets/screens/T11.png)

<a id="t12"></a>

### T12 · 思维标准 · v2 快照

地址：`/tasks/24/standards/12/versions/2`。只读历史版本，与当前编辑页分开。

![T12 思维标准 · v2 快照](assets/screens/T12.png)


## 质量评估

<a id="e01"></a>

### E01 · 质量评估

地址：`/evaluations`。默认进入评估运行列表。

![E01 质量评估](assets/screens/E01.png)

<a id="e02"></a>

### E02 · 新建评估

地址：`/evaluations/new`。数据范围、维度和独立裁判配置。

![E02 新建评估](assets/screens/E02.png)

<a id="e03"></a>

### E03 · 仓储调度 · 双模型评估

地址：`/evaluations/36`。运行状态、配置和当前结论。

![E03 仓储调度 · 双模型评估](assets/screens/E03.png)

<a id="e04"></a>

### E04 · 逐条评分

地址：`/evaluations/36/scores`。按样本、裁判、维度定位分歧。

![E04 逐条评分](assets/screens/E04.png)

<a id="e05"></a>

### E05 · 样本 #1024 · 评分证据

地址：`/evaluations/36/items/1024`。多裁判理由并排对照。

![E05 样本 #1024 · 评分证据](assets/screens/E05.png)

<a id="e06"></a>

### E06 · 评估报告

地址：`/evaluations/36/report`。分布、覆盖范围及改进建议。

![E06 评估报告](assets/screens/E06.png)

<a id="e07"></a>

### E07 · 评估维度 · 运行快照

地址：`/evaluations/36/dimensions/logic-v1`。本次评分使用的量表、权重和细则。

![E07 评估维度 · 运行快照](assets/screens/E07.png)


## 数据清洗

<a id="c01"></a>

### C01 · 数据清洗

地址：`/cleaning`。清洗运行列表与报告入口。

![C01 数据清洗](assets/screens/C01.png)

<a id="c02"></a>

### C02 · 新建清洗

地址：`/cleaning/new`。阶段、规则、预览命中与执行。

![C02 新建清洗](assets/screens/C02.png)

<a id="c03"></a>

### C03 · 仓储调度 · 清洗 #18

地址：`/cleaning/18`。扫描、命中、隔离及复查统计。

![C03 仓储调度 · 清洗 #18](assets/screens/C03.png)

<a id="c04"></a>

### C04 · 命中明细

地址：`/cleaning/18/findings`。筛选规则和处置状态，进入单条复查。

![C04 命中明细](assets/screens/C04.png)

<a id="c05"></a>

### C05 · 命中 #7 · 人工复查

地址：`/cleaning/18/findings/7`。上下文高亮及保留/隔离决策。

![C05 命中 #7 · 人工复查](assets/screens/C05.png)

<a id="c06"></a>

### C06 · GRPO 问题清洗 #19

地址：`/cleaning/19`。只扫描 GRPO 问题，教师提示词适配待补。

![C06 GRPO 问题清洗 #19](assets/screens/C06.png)


## 资源库

<a id="r01"></a>

### R01 · 资源库

地址：`/resources`。生成模板、指令、维度、规则、关键词与字段映射。

![R01 资源库](assets/screens/R01.png)

<a id="r02"></a>

### R02 · 生成模板

地址：`/resources/templates`。复用目标类型、规模和生成偏好。

![R02 生成模板](assets/screens/R02.png)

<a id="r03"></a>

### R03 · 标准 SFT 模板

地址：`/resources/templates/1`。查看参数、用于新任务或编辑。

![R03 标准 SFT 模板](assets/screens/R03.png)

<a id="r04"></a>

### R04 · 评估维度

地址：`/resources/dimensions`。内置 58 项与自定义维度分别检索。

![R04 评估维度](assets/screens/R04.png)

<a id="r05"></a>

### R05 · 逻辑一致性

地址：`/resources/dimensions/1`。评分细则、分值与权重。

![R05 逻辑一致性](assets/screens/R05.png)

<a id="r06"></a>

### R06 · 清洗规则

地址：`/resources/rules`。规则执行范围、动作和启用状态。

![R06 清洗规则](assets/screens/R06.png)

<a id="r07"></a>

### R07 · 拒答文本复查

地址：`/resources/rules/1`。命中条件、阶段和处置配置。

![R07 拒答文本复查](assets/screens/R07.png)

<a id="r08"></a>

### R08 · 关键词库

地址：`/resources/keywords`。匹配模式、类别及启用状态。

![R08 关键词库](assets/screens/R08.png)

<a id="r09"></a>

### R09 · 关键词 · 对不起

地址：`/resources/keywords/1`。查看和编辑单个匹配项。

![R09 关键词 · 对不起](assets/screens/R09.png)

<a id="r10"></a>

### R10 · 批量导入关键词

地址：`/resources/keywords/import`。逐行解析、去重预览后导入。

![R10 批量导入关键词](assets/screens/R10.png)

<a id="r11"></a>

### R11 · 生成指令

地址：`/resources/prompts`。按生成阶段管理提示词。

![R11 生成指令](assets/screens/R11.png)

<a id="r12"></a>

### R12 · 仓储问题生成指令

地址：`/resources/prompts/1`。系统指令、用户指令及版本。

![R12 仓储问题生成指令](assets/screens/R12.png)

<a id="r13"></a>

### R13 · 导出字段映射

地址：`/resources/mappings`。按训练类型与格式管理映射。

![R13 导出字段映射](assets/screens/R13.png)

<a id="r14"></a>

### R14 · SFT · Alpaca 映射

地址：`/resources/mappings/1`。源字段、目标字段和导出预览。

![R14 SFT · Alpaca 映射](assets/screens/R14.png)


## 系统管理

<a id="a01"></a>

### A01 · 系统管理

地址：`/admin`。服务就绪、队列和配置问题。

![A01 系统管理](assets/screens/A01.png)

<a id="a02"></a>

### A02 · 模型服务

地址：`/admin/providers`。连接配置与可用性。

![A02 模型服务](assets/screens/A02.png)

<a id="a03"></a>

### A03 · 模型服务 · Atlas

地址：`/admin/providers/1`。连接、测试、模型列表及密钥管理。

![A03 模型服务 · Atlas](assets/screens/A03.png)

<a id="a04"></a>

### A04 · 结果存储

地址：`/admin/storage`。可用存储与默认落点。

![A04 结果存储](assets/screens/A04.png)

<a id="a05"></a>

### A05 · 主存储

地址：`/admin/storage/1`。连接配置和历史产物关联。

![A05 主存储](assets/screens/A05.png)

<a id="a06"></a>

### A06 · 操作记录

地址：`/admin/audit`。按对象、时间和动作检索。

![A06 操作记录](assets/screens/A06.png)

<a id="a07"></a>

### A07 · 操作记录 #91

地址：`/admin/audit/91`。变更前后对比与对象跳转。

![A07 操作记录 #91](assets/screens/A07.png)

## 全局状态与响应式

### 空数据

![空数据](assets/screens/state-empty.png)

### 加载中

![加载中](assets/screens/state-loading.png)

### 请求失败

![请求失败](assets/screens/state-error.png)

### 断网

![断网](assets/screens/state-offline.png)

### 无权限

![无权限](assets/screens/state-denied.png)

### 404

![404](assets/screens/state-missing.png)

### 1024px 布局

![1024px 布局](assets/screens/responsive-1024.png)

### 390px 布局

![390px 布局](assets/screens/responsive-390.png)
