# LLM长链思考训练数据工厂 TODO / 架构设计

## 0. 项目定位

### 0.1 目标
构建一个**企业级、面向用户可操作、可扩展、可审计、可异步大规模生成**的“LLM 长链思考训练数据工厂”，支持：

1. 用户输入目标数据集规模与领域关键词。
2. 系统调用 **OpenAI 协议兼容**的大模型接口（支持自定义 `base_url`、`api_key`、`model`，默认支持 `gpt-5.4`）生成不少于 1000 个领域方向。
3. 以知识图谱方式展示领域结构，支持用户人工编辑与大模型二次修正。
4. 基于确认后的领域集合，异步批量生成问题。
5. 基于问题集合，异步生成长思维链/详细推理过程与答案，形成监督训练数据。
6. 在问答对完成后，异步生成奖励评估/偏好优化/RL 用数据。
7. 全过程具备去重、质量控制、成本控制、存储归档、导出与审计能力。

### 0.2 项目边界

#### In Scope
- 用户端与管理员端双门户。
- 多模型配置、多存储配置、多策略模板。
- 大规模异步任务编排、失败重试、断点续跑。
- 领域图谱编辑、问题生成、答案/推理生成、奖励数据生成。
- 数据去重、质量评分、版本管理、导出。
- Docker 化开发、测试、部署。
- 默认使用 PostgreSQL + Redis + S3（兼容 MinIO/阿里云 OSS）。

#### Out of Scope（首期不做或弱化）
- 在线模型训练平台本身。
- 完整的自动化标注员市场。
- 超复杂多租户计费结算中心（首期只保留计量基础能力）。
- 全自动知识图谱推理引擎（首期做图谱展示、编辑、分层组织与版本管理）。

### 0.3 核心架构原则
1. **先规划再生成**：先做数据集计划、预算、配额、去重策略，再执行大规模生成。
2. **全流程异步化**：所有高耗时任务进入任务流水线，前端只做状态驱动。
3. **幂等优先**：每个生成节点都必须可重试、可回放、可跳过已完成步骤。
4. **数据与任务解耦**：任务状态、原始模型响应、标准化样本、导出产物分层存储。
5. **去重前置 + 去重后置**：既防止生成前任务重复，也防止生成后内容重复。
6. **配置驱动**：管理员控制模型、存储、Prompt 模板、策略模板、配额规则。
7. **企业级稳定性**：限流、重试、熔断、队列隔离、审计、告警、可观测性必须内建。

---

## 1. 关键业务澄清与落地约束

### 1.1 关于“m 的 n 次方个问题”
该表述如果按数学笛卡尔爆炸严格执行，会导致：
- 成本不可控
- 生成时长不可控
- 高相似问题比例显著提升
- 质量审核无法闭环

**工程落地建议：**
将“m 的 n 次方”改造为：
- `领域数 m`：由目标规模 + 策略模板自动计算
- `每领域问题数 q`：按领域权重、层级深度、稀疏约束分配
- `总问题量 target_questions = sum(domain_quota)`
- `总训练样本量 target_samples = target_questions * answer_variants * reward_variants`

即：**用“配额分配 + 分层展开 + 去重收敛”替代数学指数爆炸**。

### 1.2 关于“长思维链”
建议系统层面将输出拆成 3 层：
1. `question`：问题正文
2. `answer`：最终答案
3. `reasoning_artifact`：推理痕迹/详细解题过程/步骤性解释

这样便于：
- 分开存储
- 分级导出
- 按训练场景控制可见性
- 后续替换不同生成策略

---

## 2. 总体系统架构

## 2.1 系统角色

### 用户端
- 创建数据集任务
- 输入目标规模、关键词、策略模板
- 查看领域图谱
- 人工修改/合并/删除/补充领域
- 发起问题生成/答案生成/奖励数据生成
- 查看进度、质量、导出结果

### 管理员端
- 配置模型供应商 / OpenAI 兼容接口
- 配置对象存储、数据库、Redis、OSS/S3
- 配置策略模板（领域拆分策略、问题生成策略、奖励策略）
- 管理数据集版本、导出规则、审计日志
- 配置系统限流、预算、告警、质量阈值

## 2.2 服务分层

### 前端层（React）
1. **用户门户（User Portal）**
2. **管理后台（Admin Portal）**
3. **共享组件库（Shared UI Kit）**
4. **图谱可视化模块（Knowledge Graph UI）**

### 接入层
1. **API Gateway / BFF**
2. **Auth / Session / RBAC 中间层**
3. **限流与审计中间层**

### 后端业务层（Go）
1. **Dataset Service**：数据集生命周期管理
2. **Planning Service**：配额规划、领域拆分方案计算
3. **Graph Service**：领域节点/边、图谱版本管理
4. **Generation Service**：统一生成任务入口
5. **Provider Service**：OpenAI 协议兼容模型调用封装
6. **Dedup Service**：文本归一化、签名、相似度、聚类去重
7. **Quality Service**：质量规则、采样复检、得分
8. **Reward Service**：奖励评估样本生成
9. **Storage Service**：S3/OSS 上传、下载、清单管理
10. **Export Service**：导出 JSONL / Parquet / manifest
11. **Admin Config Service**：系统配置、模板、预算、阈值
12. **Audit Service**：操作审计、模型调用审计、数据版本审计

### 异步执行层
1. **Job Orchestrator**：任务编排、状态机、阶段推进
2. **Domain Worker**：领域生成工作器
3. **Question Worker**：问题生成工作器
4. **Answer/Reasoning Worker**：答案与推理生成工作器
5. **Reward Worker**：奖励数据生成工作器
6. **Dedup Worker**：去重与聚类工作器
7. **Export Worker**：导出打包工作器
8. **Scheduler / Retry / DLQ Worker**：重试与死信任务处理

### 数据层
1. **PostgreSQL**：元数据、任务状态、配置、审计、索引
2. **Redis**：队列、缓存、限流、分布式锁、幂等键
3. **S3 / OSS**：原始响应、推理文本、大文件、导出产物
4. **可选 MinIO**：本地/测试环境 S3 兼容对象存储

---

## 3. 模块拆分设计

## 3.1 前端模块设计（React）

### 3.1.1 用户端模块
1. **登录与身份模块**
   - 登录/注销
   - Token 刷新
   - 权限控制

2. **数据集创建向导**
   - 输入目标数据集大小
   - 输入关键词
   - 选择策略模板
   - 预算预估与成本提示
   - 提交创建

3. **领域规划与图谱编辑页面**
   - 展示 1000+ 领域节点
   - 按层级/聚类折叠
   - 合并、拆分、删除、重命名
   - 调用模型“补充节点/重组结构/清洗重复”
   - 图谱版本回滚

4. **生成进度中心**
   - 领域阶段
   - 问题阶段
   - 答案/推理阶段
   - 奖励阶段
   - 失败任务重试
   - 进度条与吞吐统计

5. **样本浏览器**
   - 查看领域 -> 问题 -> 答案 -> 推理 -> 奖励标签
   - 去重标记、质量得分、异常原因
   - 抽样审核

6. **导出中心**
   - JSONL / CSV / Parquet / 压缩包
   - 筛选导出
   - 导出历史

### 3.1.2 管理端模块
1. **模型配置中心**
   - Provider 名称
   - Base URL
   - API Key（加密存储）
   - 模型列表
   - 超时、并发、重试、限速

2. **存储配置中心**
   - S3 / MinIO / 阿里云 OSS
   - Bucket / Endpoint / Region
   - 凭证与连通性测试

3. **策略模板中心**
   - 领域扩展模板
   - 问题生成模板
   - 答案/推理模板
   - 奖励评估模板
   - 去重阈值模板

4. **任务治理中心**
   - 队列状态
   - Worker 健康度
   - 重试队列
   - 死信队列
   - 并发控制

5. **数据集管理中心**
   - 数据集版本
   - 数据集状态
   - 导出归档
   - 删除/冻结/恢复

6. **审计与告警中心**
   - 用户操作日志
   - 模型调用日志
   - 存储访问日志
   - 失败率告警
   - 成本异常告警

## 3.2 后端模块设计（Go）

### 3.2.1 API Gateway / BFF
职责：
- 为用户端/管理端提供统一 REST/GraphQL API（建议首期 REST）
- 鉴权、请求校验、错误码标准化
- 聚合多服务数据返回前端

### 3.2.2 Dataset Service
职责：
- 创建/更新/冻结数据集
- 管理数据集版本
- 管理生成配置快照

### 3.2.3 Planning Service
职责：
- 根据 `目标样本量 + 关键词 + 策略模板` 自动计算：
  - 领域数量
  - 层级结构
  - 每领域问题配额
  - 每阶段预算上限
- 输出执行计划 `dataset_plan`

### 3.2.4 Graph Service
职责：
- 存储图谱节点、边、层级、聚类信息
- 支持人工与 AI 双向编辑
- 支持图谱版本比较与回滚

### 3.2.5 Provider Service
职责：
- 统一封装 OpenAI 协议兼容接口
- 支持不同 `base_url/api_key/model`
- 统一重试、超时、限流、熔断、日志脱敏
- 为上层提供稳定的 `Generate()` / `BatchGenerate()` 接口

### 3.2.6 Generation Service
职责：
- 接收生成请求并拆分为任务
- 管理状态机
- 跟踪任务血缘关系（领域 -> 问题 -> 答案 -> 奖励）

### 3.2.7 Dedup Service
职责：
- 文本标准化
- 精确去重（hash）
- 近似去重（SimHash / MinHash / embedding 相似度）
- 聚类合并
- 生成重复告警与替换任务

### 3.2.8 Quality Service
职责：
- 检查问题质量、答案完整度、推理长度、格式合规性
- 检测空样本、脏样本、异常重复、低信息密度样本
- 输出质量分数与拦截原因

### 3.2.9 Reward Service
职责：
- 生成偏好比较、打分解释、维度评价、rule-based reward 元数据
- 支持单样本 reward 与成对偏好 reward

### 3.2.10 Export Service
职责：
- 生成导出清单
- 打包 JSONL / Parquet / manifest
- 上传到 S3/OSS
- 生成可下载链接

---

## 4. 异步数据流水线设计

## 4.1 主流程

```text
创建数据集
  -> 生成执行计划
  -> 生成领域列表
  -> 领域标准化/去重/聚类
  -> 图谱展示与人工确认
  -> 生成问题
  -> 问题标准化/去重/质量过滤
  -> 生成答案+推理过程
  -> 样本标准化/去重/质量过滤
  -> 生成奖励评估数据
  -> 数据集聚合与版本冻结
  -> 导出与归档
```

## 4.2 任务阶段细化

### 阶段 A：数据集创建与规划
输入：
- 关键词
- 目标样本规模
- 策略模板

输出：
- `dataset`
- `dataset_plan`
- 预算、配额、并发策略

### 阶段 B：领域生成
1. 根据模板调用模型生成 >=1000 个领域方向
2. 对领域名称进行标准化
3. 基于相似度做第一轮去重
4. 构建图谱节点与层级关系
5. 提供前端人工确认

### 阶段 C：图谱确认与修正
1. 用户手工改动领域图谱
2. 用户可请求模型二次修正
3. 图谱版本冻结，生成 `approved_graph_version`

### 阶段 D：问题生成
1. 根据领域配额拆分问题生成任务
2. 每个领域批量生成问题
3. 标准化、相似度去重、质量评分
4. 对被过滤的样本自动补量

### 阶段 E：答案与推理生成
1. 对通过的问题生成答案与推理过程
2. 结构化解析输出
3. 质量检查（长度、完整性、格式）
4. 去重与异常回补

### 阶段 F：奖励数据生成
1. 以问答对为输入
2. 生成 reward label / 评分维度 / 偏好对 / critique
3. 与原始样本建立血缘关系
4. 做独立质量检查

### 阶段 G：冻结与导出
1. 生成数据集版本快照
2. 生成统计信息
3. 导出 manifest + 文件分片
4. 上传对象存储

## 4.3 编排策略

### 推荐实现
- **API 服务只负责入队与查询，不做重任务执行**
- 使用 **Redis Streams / Asynq** 作为任务队列
- PostgreSQL 存储任务状态机与执行历史
- Worker 无状态化，可水平扩容

### 队列拆分建议
- `queue_domain_generate`
- `queue_graph_refine`
- `queue_question_generate`
- `queue_answer_generate`
- `queue_reward_generate`
- `queue_dedup`
- `queue_export`
- `queue_retry`
- `queue_dlq`

### 任务状态机
- `pending`
- `queued`
- `running`
- `partially_succeeded`
- `succeeded`
- `failed`
- `retry_waiting`
- `cancelled`
- `dead_lettered`

### 幂等策略
每个任务必须带：
- `idempotency_key`
- `dataset_id`
- `stage`
- `input_hash`
- `attempt`

相同输入重复提交时直接复用已有结果或进入跳过逻辑。

---

## 5. 去重与数据质量方案

## 5.1 去重分层

### 第一层：结构去重
- 相同关键词 + 相同策略模板 + 相同执行参数 -> 阻止重复创建计划
- 相同领域名标准化后唯一约束

### 第二层：文本精确去重
- 领域名 hash
- 问题正文 hash
- 答案正文 hash
- 奖励样本 hash

### 第三层：近似语义去重
- 标准化文本
- SimHash / MinHash 比较
- embedding 向量余弦相似度
- 聚类后保留代表样本，淘汰高相似项

### 第四层：图谱级去重
- 兄弟节点高重合
- 跨层级重复概念
- 别名归并

## 5.2 去重策略建议
1. `exact_threshold = 1.0`
2. `near_dup_threshold = 0.92 ~ 0.97`（按阶段可配置）
3. 质量低且相似度高的优先删除
4. 保留“信息密度高、结构更完整、表述更规范”的样本

## 5.3 质量控制规则
- 问题长度阈值
- 推理长度阈值
- 答案非空检查
- 输出 JSON 结构合法性检查
- 违禁词/模板残留检查
- 同质化问题密度检查
- 单领域样本上限与分布均衡检查

---

## 6. 核心数据模型（建议）

以下为逻辑模型，物理表名可按 snake_case 落地。

## 6.1 用户与权限

### `users`
- `id`
- `email`
- `password_hash` / `sso_subject`
- `name`
- `status`
- `created_at`
- `updated_at`

### `roles`
- `id`
- `code` (`admin`, `operator`, `user`, `auditor`)
- `name`

### `user_roles`
- `user_id`
- `role_id`

## 6.2 系统配置

### `model_providers`
- `id`
- `name`
- `provider_type` (`openai_compatible`)
- `base_url`
- `api_key_encrypted`
- `default_model`
- `timeout_seconds`
- `max_retries`
- `qps_limit`
- `concurrency_limit`
- `is_active`

### `storage_configs`
- `id`
- `name`
- `storage_type` (`s3`, `minio`, `oss`)
- `endpoint`
- `bucket`
- `region`
- `access_key_encrypted`
- `secret_key_encrypted`
- `is_default`

### `strategy_templates`
- `id`
- `name`
- `template_type` (`domain`, `question`, `answer`, `reward`, `dedup`, `plan`)
- `content`
- `config_json`
- `version`
- `is_active`

## 6.3 数据集与计划

### `datasets`
- `id`
- `name`
- `keyword`
- `target_size`
- `status`
- `current_version`
- `created_by`
- `created_at`
- `updated_at`

### `dataset_plans`
- `id`
- `dataset_id`
- `plan_version`
- `target_domains`
- `target_questions`
- `target_answers`
- `target_rewards`
- `budget_limit`
- `plan_config_json`
- `status`
- `created_at`

### `dataset_versions`
- `id`
- `dataset_id`
- `version`
- `graph_version_id`
- `manifest_path`
- `sample_count`
- `status`
- `frozen_at`

## 6.4 图谱模型

### `domain_nodes`
- `id`
- `dataset_id`
- `graph_version_id`
- `name`
- `normalized_name`
- `description`
- `level`
- `parent_id`
- `source_type` (`ai_generated`, `user_created`, `ai_refined`)
- `status`
- `dedup_signature`
- `quality_score`

### `domain_edges`
- `id`
- `dataset_id`
- `graph_version_id`
- `from_node_id`
- `to_node_id`
- `edge_type` (`parent_of`, `related_to`, `alias_of`)
- `weight`

### `graph_versions`
- `id`
- `dataset_id`
- `version`
- `status` (`draft`, `approved`, `archived`)
- `created_by`
- `created_at`

## 6.5 生成任务

### `generation_tasks`
- `id`
- `dataset_id`
- `stage`
- `entity_type`
- `entity_id`
- `parent_task_id`
- `idempotency_key`
- `status`
- `priority`
- `attempt`
- `provider_id`
- `model_name`
- `input_payload_json`
- `output_payload_json`
- `error_message`
- `started_at`
- `finished_at`

### `task_events`
- `id`
- `task_id`
- `event_type`
- `payload_json`
- `created_at`

## 6.6 训练样本

### `questions`
- `id`
- `dataset_id`
- `domain_node_id`
- `question_text`
- `normalized_text`
- `language`
- `difficulty`
- `quality_score`
- `dedup_status`
- `source_task_id`

### `answers`
- `id`
- `question_id`
- `answer_text`
- `answer_format`
- `quality_score`
- `source_task_id`

### `reasoning_artifacts`
- `id`
- `question_id`
- `answer_id`
- `reasoning_text`
- `reasoning_format`
- `token_count`
- `storage_path`
- `quality_score`
- `source_task_id`

### `reward_samples`
- `id`
- `dataset_id`
- `question_id`
- `answer_id`
- `reward_type` (`score`, `pairwise`, `critique`, `rubric`)
- `reward_payload_json`
- `quality_score`
- `source_task_id`

## 6.7 去重与质量

### `dedup_records`
- `id`
- `dataset_id`
- `entity_type`
- `entity_id`
- `exact_hash`
- `simhash`
- `embedding_ref`
- `cluster_id`
- `duplicate_of`
- `similarity_score`
- `decision`

### `quality_reviews`
- `id`
- `dataset_id`
- `entity_type`
- `entity_id`
- `rule_code`
- `score`
- `decision`
- `detail_json`
- `created_at`

## 6.8 文件与审计

### `artifacts`
- `id`
- `dataset_id`
- `artifact_type`
- `storage_path`
- `content_type`
- `size_bytes`
- `checksum`
- `created_at`

### `audit_logs`
- `id`
- `operator_id`
- `action`
- `resource_type`
- `resource_id`
- `before_json`
- `after_json`
- `ip`
- `created_at`

---

## 7. 存储设计

## 7.1 PostgreSQL 存什么
- 用户、角色、权限
- 数据集元数据
- 图谱节点/边与版本
- 任务状态与执行历史
- 质量记录、去重决策、审计日志
- 导出清单、索引信息

## 7.2 Redis 存什么
- 任务队列
- Worker 心跳
- 分布式锁
- 幂等 Key
- 热点缓存
- 限流令牌桶
- 短期任务结果缓存

## 7.3 S3/OSS 存什么
- 原始模型请求/响应归档
- 超长推理文本
- 批量 JSONL / Parquet 文件
- 导出压缩包
- 图谱快照
- 大型质量报告

## 7.4 存储原则
- **数据库只存索引与结构化元数据**
- **大文本与大文件只存对象存储**
- `storage_path` + `checksum` 作为回溯入口

---

## 8. Docker 化部署拓扑

## 8.1 开发 / 测试环境（docker-compose）

### 服务清单
1. `frontend-user`
2. `frontend-admin`
3. `backend-api`
4. `worker-domain`
5. `worker-question`
6. `worker-answer`
7. `worker-reward`
8. `worker-dedup`
9. `scheduler`
10. `postgres`
11. `redis`
12. `minio`
13. `nginx`（反向代理）
14. `otel-collector`（可选）
15. `prometheus` / `grafana`（可选）

## 8.2 生产环境建议拓扑

```text
[ Nginx / API Gateway ]
        |
  ---------------------
  |                   |
[User React]     [Admin React]
        |
   [Go API/BFF]
        |
  -------------------------------
  |        |        |           |
[Dataset] [Graph] [Config] [Audit API]
        |
   [Redis Queue / Scheduler]
        |
 -------------------------------------------------
 |        |          |          |                 |
[Domain] [Question] [Answer] [Reward] [Dedup Workers]
        |
  -------------------------
  |                       |
[PostgreSQL]       [S3/OSS/MinIO]
```

## 8.3 Docker 设计建议
- 前端使用多阶段构建：`node build -> nginx serve`
- 后端使用多阶段构建：`golang build -> distroless/alpine runtime`
- Worker 与 API 共用基础镜像，不同启动命令区分角色
- 所有配置通过环境变量 + 配置文件注入
- 敏感信息通过 `.env`（开发）与 Secret（生产）注入

## 8.4 建议目录结构（后续实现）

```text
/root/llm
├── apps/
│   ├── user-web/
│   ├── admin-web/
│   └── api/
├── workers/
│   ├── domain-worker/
│   ├── question-worker/
│   ├── answer-worker/
│   ├── reward-worker/
│   └── dedup-worker/
├── internal/
│   ├── auth/
│   ├── dataset/
│   ├── planning/
│   ├── graph/
│   ├── provider/
│   ├── storage/
│   ├── queue/
│   ├── quality/
│   ├── dedup/
│   └── audit/
├── deployments/
│   ├── docker/
│   ├── compose/
│   └── nginx/
├── sql/
├── docs/
├── scripts/
└── todo.md
```

---

## 9. 稳定性设计要求

## 9.1 可用性
- API 与 Worker 分离部署
- Worker 可水平扩缩容
- 队列按阶段隔离，避免单阶段阻塞全局
- 数据集任务支持暂停/恢复/取消
- 每阶段结果支持 checkpoint

## 9.2 容错
- 指数退避重试
- 熔断器保护模型供应商
- 超时控制（请求级、任务级、阶段级）
- 死信队列
- 后台人工干预重放

## 9.3 幂等与断点续跑
- 每个阶段保存完成标记
- 已生成领域/问题/答案不重复生成
- 可按 `dataset_id + stage + shard` 重新运行

## 9.4 性能
- 批量任务分片
- 并发受 provider 配置与预算控制
- 大对象异步上传 S3
- 热门查询缓存到 Redis
- 图谱分页/聚类加载，避免 1000+ 节点一次性全量渲染

## 9.5 成本控制
- 每次任务记录 token 消耗、请求次数、失败重试成本
- 预算超限自动熔断或转人工确认
- 高重复率阶段自动降速并触发策略重算

---

## 10. 安全性设计要求

## 10.1 身份与权限
- 用户/管理员 RBAC
- 管理端敏感操作二次确认
- 审计员只读访问

## 10.2 密钥安全
- API Key、存储密钥必须加密存储
- 前端永不接触真实模型密钥
- 使用服务端代理调用模型接口

## 10.3 数据安全
- 对象存储使用分桶/前缀隔离
- 导出文件使用短期签名链接
- 审计日志不可篡改（至少追加写）

## 10.4 输入输出安全
- Prompt 模板变量校验
- 文件下载权限控制
- 输出 JSON Schema 校验
- 防止用户利用关键词诱导系统破坏配置/泄露密钥

## 10.5 网络安全
- 统一 HTTPS
- 内部服务走私网网络
- 数据库与 Redis 不对公网暴露
- API Gateway 做基本 WAF/限流

---

## 11. 可观测性要求

## 11.1 日志
- 结构化日志（JSON）
- `trace_id`, `dataset_id`, `task_id`, `stage`, `provider` 必须贯穿
- 敏感字段脱敏

## 11.2 指标
- API QPS / 延迟 / 错误率
- 各阶段任务吞吐
- 队列积压长度
- 模型调用成功率 / 超时率 / 重试率
- 去重率 / 质量拦截率
- 单数据集成本 / 全局成本

## 11.3 链路追踪
- API -> Queue -> Worker -> Provider -> Storage 全链路 trace
- 使用 OpenTelemetry 标准埋点

## 11.4 告警
- 队列堆积超阈值
- 模型失败率突增
- Redis/Postgres 健康异常
- S3/OSS 上传失败率上升
- 单任务成本异常

---

## 12. 前后端交互设计建议

## 12.1 用户主流程 API
1. `POST /api/datasets`
2. `GET /api/datasets/:id`
3. `POST /api/datasets/:id/plan`
4. `POST /api/datasets/:id/domains/generate`
5. `GET /api/datasets/:id/graph`
6. `POST /api/datasets/:id/graph/refine`
7. `POST /api/datasets/:id/graph/approve`
8. `POST /api/datasets/:id/questions/generate`
9. `POST /api/datasets/:id/answers/generate`
10. `POST /api/datasets/:id/rewards/generate`
11. `GET /api/datasets/:id/progress`
12. `GET /api/datasets/:id/samples`
13. `POST /api/datasets/:id/export`

## 12.2 管理端 API
1. `POST /api/admin/providers`
2. `POST /api/admin/storage-configs`
3. `POST /api/admin/strategy-templates`
4. `GET /api/admin/queues`
5. `POST /api/admin/tasks/:id/retry`
6. `GET /api/admin/audit-logs`
7. `GET /api/admin/system-metrics`

---

## 13. 实施路线图 / 分阶段 TODO

## Phase 0：项目初始化（必须先做）
- [ ] 初始化仓库结构（apps / internal / workers / deployments / docs）
- [ ] 确定 monorepo 方案
- [ ] 初始化 Go 后端骨架
- [ ] 初始化 React 用户端与管理端骨架
- [ ] 编写 Dockerfile / docker-compose 基础版本
- [ ] 接入 PostgreSQL / Redis / MinIO 本地开发环境
- [ ] 设计基础配置加载机制
- [ ] 建立代码规范、日志规范、错误码规范

**交付物**：项目可启动空骨架 + 健康检查接口 + 基础 compose。

## Phase 1：身份、配置与基础设施
- [ ] 用户/角色/RBAC
- [ ] 模型配置管理
- [ ] 存储配置管理
- [ ] 策略模板管理
- [ ] 审计日志基础能力
- [ ] API 网关与统一鉴权中间件

**交付物**：管理员可登录并完成模型与存储配置。

## Phase 2：数据集规划能力
- [ ] 数据集创建接口
- [ ] 数据集计划生成器
- [ ] 目标规模 -> 领域数/问题数配额算法
- [ ] 预算估算与配额展示
- [ ] 数据集状态机

**交付物**：用户可创建数据集并看到可执行计划。

## Phase 3：领域生成与知识图谱
- [ ] Domain Worker
- [ ] OpenAI 兼容 Provider 封装
- [ ] 领域标准化与首轮去重
- [ ] 图谱节点/边存储
- [ ] 用户端图谱渲染页面
- [ ] 图谱人工编辑与 AI 二次修正
- [ ] 图谱版本审批与冻结

**交付物**：可生成并编辑 1000+ 领域图谱。

## Phase 4：问题生成流水线
- [ ] Question Worker
- [ ] 分领域配额拆分
- [ ] 问题生成 Prompt 模板接入
- [ ] 问题标准化与去重
- [ ] 补量机制
- [ ] 进度中心页面

**交付物**：稳定生成问题集，支持失败重试与补量。

## Phase 5：答案/推理生成流水线
- [ ] Answer Worker
- [ ] 推理文本结构化存储
- [ ] 长文本对象存储落盘
- [ ] 答案/推理质量规则
- [ ] 样本浏览器页面

**交付物**：可产出训练监督样本（问题 + 答案 + 推理）。

## Phase 6：奖励数据流水线
- [ ] Reward Worker
- [ ] 支持评分型、偏好型、rubric 型 reward
- [ ] 奖励样本与问答血缘绑定
- [ ] 奖励质量审核

**交付物**：可产出强化学习/奖励建模数据。

## Phase 7：导出与版本管理
- [ ] 数据集冻结
- [ ] JSONL/Parquet 导出
- [ ] manifest 生成
- [ ] S3/OSS 上传
- [ ] 导出历史与下载

**交付物**：可用于训练的标准化数据包。

## Phase 8：稳定性与治理增强
- [ ] 队列治理页
- [ ] 死信队列重放
- [ ] 熔断/限流/预算熔断
- [ ] 指标与告警
- [ ] OpenTelemetry 全链路追踪
- [ ] 成本分析看板

**交付物**：达到企业级可运维标准。

## Phase 9：高级优化（第二阶段）
- [ ] 更强近似去重（向量化）
- [ ] 自动领域均衡与长尾补齐
- [ ] 多 Provider 负载均衡
- [ ] 灰度策略模板
- [ ] 多租户能力
- [ ] Kubernetes 生产部署模板

---

## 14. 首批必须落地的技术决策

### 决策 1：队列选型
**建议首期：Redis + Go Worker**
- 优点：部署简单、开发快、符合现有约束
- 风险：超大规模复杂工作流时可视化与编排能力有限
- 预案：后期切换/兼容 Temporal 或 Kafka 编排层

### 决策 2：图谱渲染选型
**建议：React + Cytoscape.js（优先）**
- 适合大节点图谱展示
- 支持布局、聚类与交互
- 若更强调流程式编辑，可局部结合 React Flow

### 决策 3：大文本存储
**建议：推理长文本存对象存储，数据库只存索引**
- 避免 PostgreSQL 膨胀
- 提升查询与备份效率

### 决策 4：去重实现
**建议：精确哈希 + 近似签名 + 可选 embedding 三层组合**
- 首期先实现 hash + simhash
- 二期再引入向量相似度增强

---

## 15. 风险清单

1. **问题规模设计不合理导致成本爆炸**
   - 对策：预算上限 + 配额计划器 + 分阶段确认

2. **领域/问题高度重复导致数据质量差**
   - 对策：多层去重 + 补量 + 长尾均衡策略

3. **单一模型供应商不稳定**
   - 对策：Provider 抽象 + 熔断 + 多模型备用

4. **推理长文本导致数据库膨胀**
   - 对策：对象存储分层

5. **图谱节点过多导致前端卡顿**
   - 对策：聚类、分层加载、虚拟化渲染

6. **异步任务失败后无法恢复**
   - 对策：任务状态机 + 幂等键 + checkpoint + DLQ

---

## 16. 最小可用版本（MVP）建议

### MVP 目标
在最短时间内验证：
- 用户是否能从关键词生成领域图谱
- 是否能稳定生成问题 + 答案 + 推理 + reward
- 是否能控制重复率与失败率

### MVP 范围
- 单租户
- 单管理员
- 单模型 provider
- 单对象存储配置
- JSONL 导出
- 基础去重（hash + simhash）
- 基础图谱编辑

### MVP 验收标准
- 单次任务可稳定生成 >= 1000 领域
- 可基于确认领域生成可配置规模的问题集
- 问题、答案、推理、reward 四类数据链路完整
- 支持失败重试、任务进度查看、导出下载
- 重复率、失败率、成本可观测

---

## 17. 结论

该项目不应被实现为“单接口串行调用模型”的简单脚本系统，而应实现为：

- **前端双门户 + Go 后端服务 + Redis 异步编排 + PostgreSQL 元数据中心 + S3/OSS 对象存储**
- **带图谱确认、阶段冻结、去重治理、质量控制、导出归档的企业级数据工厂**

首期建议优先落地：
1. 基础仓库与 Docker 化基础设施
2. 管理员配置中心
3. 数据集计划与领域图谱流程
4. 问题/答案/奖励三段式异步流水线
5. 去重、导出、观测能力

