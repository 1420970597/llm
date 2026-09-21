# Atelier 画面图册与路径索引

原型总共 39 个主要画面（含独立试制详情、扩量规划和 B19 详情），另有动态状态、样本切换、筛选参数和角色状态。路径均可直接复制到 `prototype.html#` 后使用。

## 代表画面

| 画面 | 说明 |
|---|---|
| ![今日工作](assets/screens/W01.png) | 工作区首页只聚合需要决定的事项 |
| ![生产蓝图](assets/screens/P02.png) | 有限语义节点 + 右侧步骤检查器 |
| ![三栏审阅](assets/screens/D02.png) | 队列、内容、证据同屏，可进入专注模式 |
| ![发布数据卡](assets/screens/L03.png) | 质量门槛、冻结清单和版本数据卡 |

屏幕编号 `P03` 指设计画面，业务批次 `P03` 指试制对象；两者属于不同编号空间。批次详情的屏幕编号是 `R04`。

## 全站主路径

| ID | URL | 画面 | 点击重点 |
|---|---|---|---|
| [W01](assets/screens/W01.png) | `/today` | 今日工作 | 待决定、试制比较、继续项目、交付日历 |
| [W02](assets/screens/W02.png) | `/projects` | 数据项目 | 搜索、项目卡、从方案开始 |
| [W03](assets/screens/W03.png) | `/new` | 定义交付目标 | 名称、目标、SFT/GRPO、下一步 |
| [W04](assets/screens/W04.png) | `/new/coverage` | 规划覆盖与规模 | n×m×x、试制样本、实时规模 |
| [W05](assets/screens/W05.png) | `/new/quality` | 设定质量门槛 | 接纳率、预算、预算策略、创建 |
| [P01](assets/screens/P01.png) | `/p/aurora/overview` | 项目概览 | 旅程进度、当前版本、下一个决定 |
| [P02](assets/screens/P02.png) | `/p/aurora/blueprint` | 生产蓝图 | 节点选择、检查器、保存新方案 |
| [P03](assets/screens/P03.png) | `/p/aurora/coverage` | 覆盖矩阵 | 缺口、下一批切片计划 |
| [P04](assets/screens/P04.png) | `/p/aurora/standard` | 思维标准 | 多行检查步骤、变更说明、新版本 |
| [P05](assets/screens/P05.png) | `/p/aurora/pilot` | 小批试制 | 数量、覆盖方式、版本、启动 P03 |
| [P06](assets/screens/P06.png) | `/p/aurora/compare` | 试制对比 | 同一基准 A/B、采用方案确认 |
| [R01](assets/screens/R01.png) | `/p/aurora/runs` | 生产批次 | B18、P03、A/B 的独立身份 |
| [R02](assets/screens/R02.png) | `/p/aurora/runs/b18` | 批次 B18 | 阶段、暂停、费用、快照 |
| [R03](assets/screens/R03.png) | `/p/aurora/runs/b18/failures` | 异常与恢复 | 错误、幂等恢复、成功内容保留 |
| [R04](assets/screens/R04.png) | `/p/aurora/runs/p03` | 试制批次 P03 | 独立事件、快照、扩量决策 |
| [R05](assets/screens/R05.png) | `/p/aurora/runs/new` | 规划扩量 | 方案、范围、目标、预算 |
| [R06](assets/screens/R06.png) | `/p/aurora/runs/b19` | 扩量批次 B19 | 独立计划和排队状态 |
| [D01](assets/screens/D01.png) | `/p/aurora/data` | 样本工作区 | 搜索、状态、checkbox、发布选择 |
| [D02](assets/screens/D02.png) | `/p/aurora/data/s104` | 样本审阅台 | 三栏、证据、判断、专注模式 |
| [D03](assets/screens/D03.png) | `/p/aurora/data/s104/history` | 样本版本与来源 | 时间线、批次、标准、判断 |
| [Q01](assets/screens/Q01.png) | `/p/aurora/quality` | 质量实验室 | 风险切片、实验、规则、队列 |
| [Q02](assets/screens/Q02.png) | `/p/aurora/quality/new` | 新建质量实验 | 抽样、裁判、规则、排队 |
| [Q03](assets/screens/Q03.png) | `/p/aurora/quality/e9` | 质量实验 E9 | 排队→完成、维度、分歧证据 |
| [Q04](assets/screens/Q04.png) | `/p/aurora/review` | 审阅队列 | 默认待审阅、显式筛选其它状态 |
| [Q05](assets/screens/Q05.png) | `/p/aurora/rules` | 清洗策略 | 正则校验、预览、策略版本 |
| [L01](assets/screens/L01.png) | `/p/aurora/releases` | 发布版本 | 候选/已发布、数据卡 |
| [L02](assets/screens/L02.png) | `/p/aurora/releases/new` | 准备发布 | 范围、格式、用途限制 |
| [L03](assets/screens/L03.png) | `/p/aurora/releases/v1.2` | 发布候选 v1.2 | 门槛阻塞、冻结、下载 |
| [G01](assets/screens/G01.png) | `/p/boreal/blueprint` | GRPO 生产蓝图 | 教师评判材料、奖励档位 |
| [G02](assets/screens/G02.png) | `/p/boreal/data/g201` | GRPO 样本审阅 | 教师提示词、档位、规则 |
| [G03](assets/screens/G03.png) | `/p/boreal/releases/new` | GRPO 发布配置 | JSONL 专属字段 |
| [B01](assets/screens/B01.png) | `/recipes` | 方案库 | 方案卡和整套方法 |
| [B02](assets/screens/B02.png) | `/recipes/reasoning` | 约束推理方案 | 适用范围、组成、复制项目 |
| [B03](assets/screens/B03.png) | `/deliveries` | 交付库 | 跨项目已发布版本 |
| [S01](assets/screens/S01.png) | `/activity` | 动态与待办 | 事件→对象、全部已读 |
| [S02](assets/screens/S02.png) | `/settings/connections` | 模型与存储连接 | 测试/保存分离、密钥不回显 |
| [S03](assets/screens/S03.png) | `/settings/team` | 成员与角色 | 能力和权限说明 |
| [S04](assets/screens/S04.png) | `/help` | 使用指南 | 旅程、快捷键、原型说明 |
| [S05](assets/screens/S05.png) | `/catalog` | 全站原型目录 | 全路径评审入口 |

## 状态、参数和身份变体

- `/p/aurora/data?status=待审阅&q=冷链`：筛选条件写入地址，浏览器返回恢复筛选。
- `/p/aurora/releases/new?selection=s104,s105`：发布准备锁定已选样本。
- `/p/aurora/blueprint?node=evaluation`：右侧检查器随节点改变。
- `/p/boreal/data/g201`：同一审阅台改为 GRPO 内容标签和字段。
- 底部场景选择：空数据、加载、失败、断网、无权限、不存在。
- 底部身份选择：项目负责人可保存/暂停/发布；只读访客不能进入设置或写入。
- 视口 1440×1024 展示桌面密度，390×844 展示移动端队列、画布和表单堆叠。

原型目录本身是评审工具，不应成为正式产品的第七个业务菜单。

## 补充图面

<details><summary>GRPO 专属质量报告</summary>

![GRPO 专属质量报告](assets/screens/grpo-report.png)

</details>

<details><summary>空数据</summary>

![空数据](assets/screens/state-empty.png)

</details>

<details><summary>加载</summary>

![加载](assets/screens/state-loading.png)

</details>

<details><summary>失败</summary>

![失败](assets/screens/state-error.png)

</details>

<details><summary>断网</summary>

![断网](assets/screens/state-offline.png)

</details>

<details><summary>无权限</summary>

![无权限](assets/screens/state-denied.png)

</details>

<details><summary>不存在</summary>

![不存在](assets/screens/state-missing.png)

</details>

<details><summary>390px 蓝图</summary>

![390px 蓝图](assets/screens/mobile-blueprint.png)

</details>

<details><summary>390px 审阅</summary>

![390px 审阅](assets/screens/mobile-review.png)

</details>


截图隐藏了底部原型评审工具条与临时 toast，使内容无遮挡；交互 HTML 中仍保留这些评审工具。
