# 素材问题生成与交付追溯

对应 Discussion #165 与 Issue #217。接入边界是公开导出格式及原生素材采集；SFT 和 GRPO 共用一个问题生成函数。

| 来源 | 生成行为 | 样本标记 | 追溯 |
| --- | --- | --- | --- |
| document | 校验冻结版本里的块 ID，读取块并调用模型生成问题 | document | source_chunk_ids、manifest ID/内容摘要/章节 |
| ai | 用户显式选择后按主题与难度生成问题 | keyword_only | 不计为素材接地 |
| manual / none / 未指定 | 拒绝自动生成，给出不可重试配置错误 | 无新样本 | 保留失败原因 |
| external_import | 导入 Alpaca、ShareGPT 或 JSONL 成品 | external_import | 导入台账，不宣称素材接地 |

```mermaid
sequenceDiagram
    participant UI as 前端
    participant API as API
    participant Store as Store
    participant Worker as Worker
    participant LLM as LLM
    UI->>API: 采集素材、选择块、保存蓝图
    API->>Store: 追加素材/覆盖/蓝图版本
    UI->>API: 创建批次
    API->>Store: 冻结版本和生成参数
    Store-->>Worker: 作业与不可变快照
    Worker->>Store: 检查块属于项目及冻结素材版本
    alt 缺失或未明确来源
        Worker->>Store: config_error，retryable=false
    else 有素材或明确 AI 来源
        Worker->>LLM: 真实问题生成（素材仅作为数据）
        LLM-->>Worker: 问题、请求回执、用量
        Worker->>LLM: SFT 推理/答案或 GRPO 判据
        LLM-->>Worker: typed 内容、用量
        Worker->>Store: 追加版本与素材 ID
    end
    UI->>API: 导出冻结的发布候选
    API->>Worker: 发布作业
    Worker->>Store: 读取样本版本、查询素材引用
    Worker-->>UI: 制品与 manifest 接地摘要
```

```mermaid
stateDiagram-v2
    [*] --> Planned
    Planned --> Validating
    Validating --> ConfigError: 缺素材、跨项目或未冻结块
    Validating --> GeneratingQuestion: 输入有效
    GeneratingQuestion --> GeneratingContent: 问题有效
    GeneratingQuestion --> Failed: 供应商或输出错误
    GeneratingContent --> Committed: typed 内容有效
    GeneratingContent --> Failed: 供应商或输出错误
    Failed --> Planned: 可重试且显式恢复
    ConfigError --> [*]
    Committed --> [*]
```

素材查询不会回退到当前默认版本。SFT 与 GRPO 的两次调用均使用批次冻结的最大 token 数和温度；回执用量聚合，任何未报告的输入或输出项继续保持未知。

预算在每个 HTTP 尝试之前事务预留，每个响应、空响应、解码失败和重试分别结算；成功的问题生成不会因为后续内容失败而从账目消失。超时使用独立的有界上下文记录未知费用并占用当时的预留，不补零。缺少价格、价格与当前接入点不一致、预留失败均不发出模型请求。管理员可在连接页保存价格版本；保守报价以 `isEstimated` 明确标记，即使 token 来自真实供应商，金额仍为 estimated，不能冒充实际账单。免费连接须显式声明。

实验裁判复用同一记账入口，冻结模型、接入点、输出上限及可选温度；旧实验缺少输出上限时拒绝付费执行，需创建新的实验。项目级实验使用项目预算，绑定批次的实验同时使用批次预算；同作业尝试重放已结算请求不会重复付费，新尝试保留独立回执。裁判预算耗尽后，同一次作业尝试的其他裁判停止发出请求，并保留逐条错误证据。

批次因预算暂停时，正在等待调用的单元恢复为 pending，已成功单元保持成功；恢复后从待处理单元继续，单元尝试号保留递增历史。手动暂停同样在每次请求预留时检查，已在途的响应仍结算，暂停后的答案调用和 HTTP 重试不再发出。

```mermaid
sequenceDiagram
    participant Generator as 生成器
    participant Budget as 用量与预算 Store
    participant Provider as 模型接入点
    Generator->>Budget: 读取当前连接价格、冻结请求上界
    Generator->>Budget: 项目与批次预算事务预留
    alt 无法可靠预留
        Budget-->>Generator: 拒绝，未发出请求
    else 预留成功
        Generator->>Provider: 单次 HTTP 尝试
        Provider-->>Generator: 回执或错误
        Generator->>Budget: 实际 / 估计 / 未知结算
        Note over Generator,Budget: 每次重试独立记账；未知费用不归零
    end
```

发布清单为每条样本保存来源和素材引用。素材不存在或正文被擦除时，保留 `missing_chunk` 记录；文件可以继续导出，接地摘要明确计入缺失。历史内容 hash 的计算字段保持原契约；manifest hash 包含新增追溯信息。

输入素材用于提供事实依据，关联块不等于自动证明模型答案正确；既有规则、实验和人工审阅继续决定是否接纳及发布。
