# Atelier 项目 API 契约（T01）

> 本文件是 Issue [#160](https://github.com/1420970597/llm/issues/160) 任务 **T01** 的
> API schema 附件，配套 [`docs/plans/atelier-implementation.md`](atelier-implementation.md)。
>
> 命名空间：`/api/v1/projects/*`（项目命令与读模型）、`/api/v1/studio/*`（工作区聚合）、
> `/api/v1/recipes/*`、`/api/v1/deliveries/*`（独立公共对象）。
> API 仍在现有 Go 进程内，通过 `RegisterRoutes` 注册，不改 `main.go` 的路由表。
>
> 本文件定义契约，不声称已实现。实现任务见每节的「任务」列。

---

## 1. 通用约定

### 1.1 成功响应信封

所有对象响应返回稳定字段：

```jsonc
{
  "id": "…",              // 稳定身份。URL/下载/审计只用它
  "status": "…",          // 对象状态，见 atelier-implementation.md §4.2
  "revision": 7,          // 乐观锁版本号
  "updatedAt": "2026-09-21T10:00:00Z",
  "capabilities": {       // 服务端判定的能力，仅辅助 UI，不是安全边界
    "canEdit": true,
    "canRun": true,
    "canReview": true,
    "canPublish": false,
    "canDownload": true
  },
  "links": {              // 可跳转对象，前端不自行拼 URL
    "self": "/api/v1/projects/p_01/blueprint-versions/3",
    "project": "/api/v1/projects/p_01"
  },
  "warnings": []          // 非阻塞提示（如「候选清单已变化，请重新确认」）
}
```

### 1.2 错误响应

```jsonc
{
  "error": {
    "code": "REVISION_CONFLICT",
    "message": "版本已变化，请重新加载后再保存",   // 用户可见中文
    "fieldErrors": [{ "field": "concurrency", "message": "必须在 1–32 之间" }],
    "blockers": [{ "code": "PENDING_REVIEW", "message": "还有 3 条待审阅", "link": "…" }],
    "requestId": "req_…",
    "retryable": false
  }
}
```

| 状态码 | 语义 |
|---|---|
| 401 | 未登录 / 会话失效 |
| 403 | 已登录但无权限（**不**用 404 掩盖已存在的公共对象） |
| 404 | 不存在，或**资源隐藏型**（无权得知其存在） |
| 409 | 状态或版本冲突（`expectedRevision` 不匹配、非法状态转换、幂等键复用但请求不同） |
| 422 | 语义校验失败（配比总和不等于 1、并发越界、量表错误、无独立裁判、空范围） |
| 429 | 限流 / 预算预留失败 |
| 503 | 依赖暂不可用 |

- 沿用现有错误脱敏（`apps/api/user_error.go`），**禁止**给前端原始 SQL 或密钥。
- 日志带 `requestId`，但**不含**密钥或样本正文。

### 1.3 幂等

写命令接受 `Idempotency-Key` 头：

- 同键 + 同请求摘要 → 返回**原结果**（不重复执行）。
- 同键 + 不同请求摘要 → 409。
- 幂等记录持久化在 `idempotency_records`，**不依赖短 TTL**。
- 幂等键绑定 `actor` + `project` + `command` + 请求摘要。

### 1.4 乐观锁

版本化资源保存时携带 `expectedRevision`（body 或 `If-Match`）。不匹配返回 409，
并**保留用户草稿**（前端不得丢弃输入）。

### 1.5 分页

列表使用稳定游标，不使用 offset：

```jsonc
{
  "items": [],
  "nextCursor": "eyJ…",   // 为空表示到底
  "sortKey": "updatedAt:desc"
}
```

排序键必须**稳定且唯一**（末位追加 `id`），保证翻页无重复、无遗漏。

### 1.6 权限（服务端判定）

| 角色 | 设计 | 运行 | 判断 | 发布 | 读授权内容 | 下载已发布 |
|---|---|---|---|---|---|---|
| owner | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ |
| reviewer | ✗ | ✗ | ✓ | ✗ | ✓ | ✓ |
| viewer | ✗ | ✗ | ✗ | ✗ | ✓ | ✓（仅允许下载的发布） |
| workspace admin | 管理成员/连接；**不默认拥有**所有项目内容读权 | | | | 需显式成员授权 | |

会话用户角色**不能**一直信任登录时 cookie 副本；成员/角色变更从服务端当前状态判定。

---

## 2. 项目命令

`P = /api/v1/projects/{projectId}`

### 2.1 创建项目

```http
POST /api/v1/projects
Idempotency-Key: <uuid>
```

```jsonc
{
  "name": "冷链问答数据",
  "goal": "交付可用于 SFT 的冷链领域问答",
  "targetKind": "sft",              // "sft" | "grpo"
  "coverage": { "domains": 8, "directionsPerDomain": 5, "questionsPerDirection": 6 },
  "pilotSize": 12,
  "quality": { "acceptanceRateTarget": 0.85 },
  "budget": { "currency": "CNY", "limitMinor": 8000, "onExhausted": "pause" },
  "sourceRecipeVersionId": null     // 可选，T26
}
```

- `201` + 新 project ID。
- **无模型调用**；不因缺 provider/storage 失败。
- 校验：`domains ≥ 1`、`directionsPerDomain ≥ 1`、`questionsPerDirection ≥ 1`、
  `pilotSize ∈ [1,100]`、`limitMinor ≥ 100`（≥1 元）、`acceptanceRateTarget ∈ [0,1]`。
- 字段错误返回 422 + `fieldErrors`。

### 2.2 版本化文档

五类同构：`blueprint-versions`、`coverage-versions`、`standard-versions`、
`quality-policy-versions`、`mapping-versions`。

```http
POST P/blueprint-versions
```

```jsonc
{
  "expectedRevision": 3,        // 当前采用版本的 revision；不匹配 409
  "logicalId": "main",          // 同一项目内同一逻辑文档的稳定 ID
  "changeReason": "把并发从 8 提到 12",
  "payload": {
    "schemaVersion": "blueprint.v1",
    "nodes": {
      "coverage":  { "coverageVersionId": "cov_7" },
      "standard":  { "standardVersionId": "std_4" },
      "generation": {
        "modelConnectionId": "conn_2",     // 非秘密标识，不含明文密钥
        "schemaVersion": "sft.sample.v1",
        "concurrency": 12,                 // 1–32
        "maxTokens": 4096
      },
      "evaluation": { "judgeConnectionIds": ["conn_5"], "rubricVersionId": "rub_2", "samplingSeed": 42 },
      "rules":      { "qualityPolicyVersionId": "qp_3" },
      "humanReview": { "assignment": "risk-based", "requiredEvidence": ["rules", "judge"] },
      "delivery":   { "mappingVersionId": "map_1", "format": "jsonl" }
    }
  }
}
```

- `201` + 新版本；旧版本保持只读，可读/比较/复制。
- 节点依赖由服务端校验（如 `generation.modelConnectionId` 必须存在且非停用）。
- **禁止任意脚本节点**。
- 非法并发、配比总和、schema 不合法 → 422 + `fieldErrors`。

### 2.3 启动批次

```http
POST P/batches
Idempotency-Key: <uuid>
```

```jsonc
{
  "purpose": "pilot",                 // "pilot" | "scale"
  "blueprintVersionId": "bp_9",
  "coverageVersionId": "cov_7",
  "standardVersionId": "std_4",
  "unitCount": 12,                    // pilot 1–100；scale 1–100000
  "budget": { "currency": "CNY", "limitMinor": 2000 },
  "coverageSlice": null               // 可选：只跑某切片
}
```

- `202` + `batchId`/`jobId` + `Location: P/batches/{batchId}`。
- 批次保存**完整输入快照**（含各版本 ID 与内容 hash）。
- 改模型/标准必须新建批次，不得改已有批次。

### 2.4 批次控制

```http
POST P/batches/{batchId}/pause        # 202
POST P/batches/{batchId}/resume       # 202
POST P/batches/{batchId}/retry-failed # 202
```

- 返回控制状态 + **明确的在途数量**。
- `pause` 只阻止新提交；在途请求仍可完成并计费。
- `retry-failed` 沿用同一快照，**只提交失败/未完成项**，成功内容保留。
- 非法状态转换 → 409。

### 2.5 质量实验

```http
POST P/experiments
```

```jsonc
{
  "sampleVersionIds": ["sv_1", "sv_2"],   // 创建时固定，之后不受指针变化影响
  "samplingSeed": 42,
  "rubricVersionId": "rub_2",
  "judgeConnectionIds": ["conn_5"],
  "qualityPolicyVersionId": "qp_3"
}
```

- 固定 selection/config 后 `202`。
- 无独立裁判 / 空范围 / 错误量表 → 422。
- 生成者身份由样本来源推导；不接受客户端伪造 generator ID 绕过独立性检查。
- 同真实来源的**别名连接**不得自评。

### 2.6 规则预览

```http
POST P/rule-previews
```

```jsonc
{
  "qualityPolicyVersionId": "qp_3",
  "ruleId": "rule_2",
  "sampleVersionIds": ["sv_1", "sv_2"]
}
```

- `200` + 命中位置（字段、偏移、片段）。
- **不写处置、不入收费模型队列**，前后内容/处置/队列计数无变化。
- 非法 regex → 422，不落库。

### 2.7 记录判断

```http
POST P/samples/{sampleId}/versions/{versionId}/decisions
```

```jsonc
{
  "evidenceRevision": 5,     // 当前必需证据版本；不匹配 409
  "reviewerRevision": 2,     // 该审阅者个人并发检查
  "action": "accepted",      // "accepted" | "quarantined"
  "reason": "规则命中为误报，推理链完整",
  "supersedes": null         // 更正时填上一条 decision ID
}
```

- `201` + decision；重复提交幂等。
- 版本变更 → 409 并**保留用户草稿**。
- 理由必填。
- 判断**只追加**，不改原始内容。

### 2.8 发布候选

```http
POST P/release-candidates
```

```jsonc
{
  "releaseName": "v1.2",                       // 项目内唯一
  "sampleVersionIds": ["sv_1", "sv_2"],        // 具体范围，不是可变的 SQL 筛选条件
  "mappingVersionId": "map_1",
  "format": "jsonl",
  "intendedUse": "SFT 训练",
  "limitations": ["仅覆盖冷链领域"],
  "provenance": { "license": "internal", "retentionDays": 365 }
}
```

- 同一事务内分配 `candidateId` + `releaseId` 并预留 `releaseName`。
- 返回 `candidateId`、**稳定 `releaseId`**、`revision`。
- 每条 `blocker` 带可跳转对象：

```jsonc
{
  "blockers": [
    { "code": "PENDING_REVIEW", "message": "sv_3 待审阅", "link": "/api/v1/projects/p_01/samples/s_3/versions/1" },
    { "code": "EVIDENCE_INCOMPLETE", "message": "缺少独立裁判证据", "link": "…" }
  ]
}
```

```http
PATCH P/release-candidates/{candidateId}
```

- 修订沿用同一 `candidateId` 与 `releaseId`，`candidate_revision` 递增。

### 2.9 冻结并发布

```http
POST P/release-candidates/{candidateId}/publish
Idempotency-Key: <uuid>
```

- `202` + `releaseId` + `status: "building"`。
- **相同命令返回同一个 release**（不换身份）。
- 仅文件校验成功（size/hash/对象存在）后才 `published`；失败为 `build_failed`，可幂等续接。
- 冻结事务锁定候选与相关 `quality`/`evidence`/`aggregate_review` revision，
  确认当前有效接纳引用当前必需 `evidence_revision`。

### 2.10 下载与下一版

```http
GET  P/releases/{releaseId}/artifacts/{artifactId}/download
POST P/releases/{releaseId}/next-candidate
```

- 下载不可变文件；**禁止** `latest` 回退。
- 下载走统一会话/错误处理；401/403/过期凭据有可理解提示。
- 文件名含类型与版本名，不以 `latest` 命名。
- `next-candidate` 复制候选但**不改原版**，返回独立候选。

---

## 3. 读模型

读页面视图可由多个对象组成，但**写操作必须指向一个明确命令**。

| 端点 | 用途 | 关键要求 | 任务 |
|---|---|---|---|
| `GET /api/v1/projects?q=&cursor=` | 项目列表 | 服务端搜索/分页；只过滤项目 | T10 |
| `GET P/overview` | 项目概览 | 真实版本/批次/待决定；主动作定位当前阻塞 | T10、T27 |
| `GET P/batches?purpose=&status=&cursor=` | 批次列表 | 不按最大 ID 猜「当前运行」 | T13 |
| `GET P/batches/{batchId}` | 批次详情 | 配置快照、事件、失败项、在途数 | T13 |
| `GET P/samples?status=&batch=&risk=&q=&cursor=` | 样本列表 | 服务端分页；选择范围明确 | T17 |
| `GET P/samples/{sampleId}/versions/{versionId}` | 样本版本 | 内容只读 + 版本来源 | T17 |
| `GET P/experiments/{experimentId}` | 实验报告 | 固定范围、分母、缺分、分歧、证据链接 | T19 |
| `GET P/releases?cursor=` | 发布列表 | 候选与已发布区分 | T22 |
| `GET P/releases/{releaseId}` | 发布数据卡 | 原范围指标、排除数量、覆盖损失 | T22 |
| `GET /api/v1/deliveries?q=&cursor=` | 交付库 | **仅**已发布且用户可访问 | T22 |
| `GET /api/v1/today` | 今日工作聚合 | 每条带具体对象链接与权限 | T27 |
| `GET /api/v1/activity?cursor=` | 动态 | 持久化事件、分页、未读位置 | T27 |
| `GET /api/v1/search?q=` | 命令搜索 | 只含有权访问的页面/项目/对象 | T27 |

### 3.1 统计字段契约

样本列表与实验报告返回**分列统计**，不得相互冒充：

```jsonc
{
  "plannedQuestions": 240,        // n × m × x，计划量
  "generated": 231,               // 实际生成数
  "structureValid": 228,          // 通过结构校验数
  "inspected": 200,               // 纳入检查数（实验冻结的分母）
  "scored": 198,                  // 已打分数
  "accepted": 170,                // 已接纳数
  "quarantined": 18,              // 已隔离数
  "pendingReview": 12,            // 待审阅数（不算接纳，不缩小分母）
  "acceptanceRate": 0.85,         // accepted / inspected；inspected=0 时为 null
  "acceptanceRateDisplay": "85.0%" // inspected=0 时为 "无结论"
}
```

- `acceptanceRate` 为 `null` 时前端显示**无结论**，**不是** 100%。
- 抽样结论必须带 `scope` 与 `coverage`。

---

## 4. 能力响应

`capabilities` 由服务端按「当前用户 × 对象 × 对象状态」计算，仅辅助 UI 呈现：

| 对象 | 能力键 |
|---|---|
| Project | `canEdit`、`canRun`、`canReview`、`canPublish`、`canManageMembers` |
| Batch | `canPause`、`canResume`、`canRetryFailed` |
| Experiment | `canRerun`（重跑创建**新**实验） |
| Release | `canPublish`、`canDownload`、`canCreateNext` |
| Connection | `canTest`、`canEdit`、`canDisable` |

**前端禁用按钮不构成安全边界**；每个写 API 自行重新校验。

---

## 5. 事件契约

事件载荷只含对象 ID 与版本，**不把大段样本内容塞进消息总线**。

| 事件 | 载荷 | 任务 |
|---|---|---|
| `ProjectCreated` | `projectId`、`targetKind` | T02 |
| `BlueprintVersionCreated` | `projectId`、`logicalId`、`version` | T04 |
| `BatchQueued` | `projectId`、`batchId`、`purpose` | T06 |
| `BatchItemCompleted` | `batchId`、`itemId`、`sampleVersionId` | T05、T12 |
| `BatchItemFailed` | `batchId`、`itemId`、`errorClass`、`retryable` | T06 |
| `BatchPaused` / `BatchResumed` | `batchId`、`inFlight` | T13 |
| `ExperimentQueued` | `projectId`、`experimentId` | T14 |
| `EvidenceReady` | `experimentId`、`evidenceRevision` | T14、T15 |
| `DecisionRecorded` | `sampleId`、`versionId`、`decisionId`、`aggregateReviewRevision` | T16 |
| `CandidateCreated` | `projectId`、`candidateId`、`releaseId` | T20 |
| `ReleasePublished` | `projectId`、`releaseId`、`artifactIds`、`manifestHash` | T21 |

消费者必须**允许重复和乱序**；运行状态由事件投影得到。

---

## 6. TypeScript 类型对应

前端类型定义落在 `apps/web-user/src/lib/api/studio.ts`（T08），复用现有
`lib/api.ts` 的会话与错误处理。契约要求：

- TS 类型与本节 schema **一致**；由 T08 的契约测试断言。
- 未知子资源返回 404，非法状态返回 409，分页无重复遗漏。
- 各模块后续 API 以同一契约**增量**交付，不新增漂移的映射表。

---

## 7. 与旧契约的关系

| 旧接口 | 处置 |
|---|---|
| `/api/v1/datasets/*` | 过渡期保留为**兼容读模型**；新运行不走它。T31 后旧写入口返回迁移说明 |
| `/api/v1/tasks/*`（`todo.md` §12.2 设想） | **从未实现**，不再是目标接口 |
| `/api/v1/eval/*`、`/api/v1/cleaning/*` | 计算逻辑复用（T14/T15），但新实验/规则版本走项目命令 |
| `/api/v1/admin/*` | 保留治理能力；T28 提供非秘密 connection-options 读接口给普通用户 |

---

## 8. T24–T33 的**增量**交付（不改变 §1–§7）

§1–§7 是 T01 冻结的契约。后续任务以**同一信封/错误/分页/幂等约定**增量交付端点，
不新增漂移的映射表。本节只登记「多了哪些端点、有哪些新字段」，schema 细节见各端点的
实现与测试。

### 8.1 T24 质量实验的 GRPO 适配

| 端点 | 变化 |
|---|---|
| `POST P/experiments` | 请求新增 `targetConfig`（GRPO 专属：教师提示词版本 / 基准回答版本 / 边界参考集）。量表按 `target_kind` 强制匹配：GRPO 省略 `rubric` 时用内置量表（档位覆盖 / 边界稳定性 / 评分解释一致性） |
| `GET P/experiments/{id}` | 报告新增 `targetKind` 维度语义；GRPO 的 `level_coverage` 由**确定性判据**给出（`judge_connection_id = 0` 的行），不按裁判数复制 |

GRPO 实验的 target_config 字段：

| 字段 | 必填 | 说明 |
|---|---|---|
| `teacherPromptVersion` | 否 | 教师提示词版本标识（结果关联用） |
| `baselineAnswerVersion` | 否 | 基准回答版本标识；为空时 `boundary_stability` 记缺分 |
| `boundaryReference.items[]` | 否 | `{level, input, expected: accept\|reject, note}`；冻结参考集 |
| `boundaryReferenceHash` | 读 | 由服务端复算；客户端传入不一致即 422 |

### 8.2 T25 GRPO 发布 JSONL

GRPO 发布的每一行**必须**满足下列 schema（服务端逐行解码校验，不通过即中止发布）：

| 字段 | 类型 | 约束 |
|---|---|---|
| `question` | string | 非空 |
| `judge_prompt` | string | 非空 |
| `levels` | string[] | ≥ 2 档、非空、不重复；**必须是数组**（逗号字符串会被拒绝） |
| `level_rubrics` | object[] | 与 `levels` 一一对应；每项 `{level, criteria, accept_case, reject_case}`，`criteria` 非空 |
| `framework_ref` | string | 可空（provenance） |

GRPO 项目只能发布 `jsonl`；候选创建期与构建期各校验一次（502/409 归属见 §1.2）。

### 8.3 T31 旧路由映射与旧写入口冻结

| 端点 | 语义 |
|---|---|
| `GET /api/v1/legacy/datasets/{datasetId}/project` | 200 + `{datasetId, projectId, pagePath, migrationStatus, message}`；`migrationStatus ∈ {mapped, not_mapped}`。**未映射不是 404**：它是「尚未迁移」，前端据此渲染只读历史列表 |

`LEGACY_WRITES_FROZEN=true` 时，`/api/v1/datasets` 等旧入口的写方法返回
**409** + 迁移说明（读与下载不受影响）。`/api/v1/admin/*` 与 `/api/v1/projects` 不受此开关影响。

### 8.4 T33 运维开关与诊断

| 端点 | 权限 | 语义 |
|---|---|---|
| `GET /api/v1/studio/rollout` | 管理员 | `{enabled, disabledProjects[], notes[]}` |
| `GET /api/v1/studio/health` | 管理员 | 运维快照（outbox/jobs/leases/budget/usage/experiments/releases + `notes[]` 解读） |

`STUDIO_ENABLED=false` 时，项目级**写入**动作（design/run/review/publish）返回
**503** + `DEPENDENCY_UNAVAILABLE`；读取、下载、成员管理与项目创建之外的读路径不受影响。
未知动作按写入处理。
