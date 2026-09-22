-- 0026: Atelier 可靠作业派发：jobs / outbox / 租约 / fencing（Issue #160 T06）
--
-- 契约来源：docs/plans/atelier-implementation.md §4.1、#160 §1 的 worker 行；
-- docs/plans/atelier-api-contract.md §1.3、§5。
--
-- 为什么需要这一层（#160 §1 对现有实现的原文判断）：
--   「不是带确认和租约的可靠任务系统。新增事务 outbox、DB 作业状态、
--     租约/心跳/过期回收与幂等提交。」
--
-- 现有实现的三个具体缺陷（都是实测过的，不是理论问题）：
--   1. `enqueueJob` 用 Redis LPush + `dedup:` SETNX(10min)，**没有 ack**：
--      BRPOP 是破坏性读取，worker 崩溃即丢任务，而 datasets.status 仍停在
--      `*_queued`（issue #139/#143 实测最长停留 23 小时）。
--   2. 恢复逻辑靠**扫业务表状态**猜「哪些任务丢了」（recoverStalledJobs）。
--      那只能覆盖已知的 *_queued 状态，且无法回答「该重投几次」「谁在做」。
--   3. 唯一性约束被当成执行租约：`uniq_generation_runs_active` 只保证
--      「同 dataset+stage 同时只有一个活跃记录」，它不阻止**两个 worker
--      同时处理同一条消息**（消息可以被重复投递）。
--
-- 本迁移建立的是「Postgres 是权威状态，Redis 只是通知」的模型：
--   * jobs 表是任务的事实来源（状态、尝试、租约、fencing）；
--   * outbox 表保证「业务事务提交 → 一定会被派发」，不依赖调用方记得入队；
--   * Redis 消息只携带 `{schemaVersion, jobId}`（**不携带业务载荷**），
--     因此丢消息可以从 DB 重投，而不会因为载荷与 DB 状态不一致而做错事。

-- ---------------------------------------------------------------------------
-- jobs：权威作业状态
-- ---------------------------------------------------------------------------
--
-- job_kind 与 payload 分离：kind 是**可编程的**分发依据，payload 是数据。
-- 不把 kind 塞进 payload，是为了能按 kind 建索引与统计（可观测性 T33）。
--
-- 为什么 payload 里**不放密钥**（§2.4）：payload 会进日志与 outbox，
-- 而凭证必须每次从加密配置解析。

CREATE TABLE IF NOT EXISTS jobs (
  id BIGSERIAL PRIMARY KEY,
  project_id BIGINT REFERENCES projects(id) ON DELETE CASCADE,
  -- 批次是大多数作业的作用域（生成/评估/发布）。它可空，
  -- 因为将来可能有项目级作业（例如迁移核对）。
  batch_id BIGINT REFERENCES batches(id) ON DELETE CASCADE,
  job_kind TEXT NOT NULL,
  payload JSONB NOT NULL DEFAULT '{}'::jsonb,

  status TEXT NOT NULL DEFAULT 'pending'
    CHECK (status IN ('pending', 'leased', 'running', 'succeeded', 'failed', 'cancelled', 'dead')),

  -- 尝试与重试：attempt 是**已开始**的次数，与 max_attempts 一起决定是否还能重试。
  -- 单独一张 job_attempts 记录每次尝试的细节（见下），jobs 上只留计数。
  attempt INTEGER NOT NULL DEFAULT 0 CHECK (attempt >= 0),
  max_attempts INTEGER NOT NULL DEFAULT 3 CHECK (max_attempts >= 1),

  -- 退避：next_run_at 让「重试」不必立即重试（限流场景立即重试只会再被限流）。
  next_run_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),

  -- 租约与 fencing（#160 T06 的原文要求「DB CAS 抢占租约、心跳、
  -- fencing token 防过期 worker 迟到提交」）。
  --
  -- fencing_token 是关键：租约过期后旧 worker 可能仍在跑，它回来提交结果时
  -- 必须能被告知「你已经不是租约持有者」。做法是每次抢占**递增** token，
  -- 提交时带自己的 token，不匹配即拒绝。仅靠 lease_until 判断是不够的 ——
  -- 那会允许「持有者以为自己还有租约」的窗口。
  lease_owner TEXT NOT NULL DEFAULT '',
  lease_until TIMESTAMPTZ,
  fencing_token BIGINT NOT NULL DEFAULT 0,

  -- 错误分类与可读信息。error_class 与 batch_items 用同一套取值，
  -- 使界面能用同一份文案（model.ErrorClassAction）。
  error_class TEXT NOT NULL DEFAULT '',
  error_message TEXT NOT NULL DEFAULT '',
  retryable BOOLEAN NOT NULL DEFAULT TRUE,

  -- 幂等：同一 (job_kind, idempotency_key) 只应产生一个作业。
  -- 唯一索引建在下面（部分索引，只在 key 非空时生效）。
  idempotency_key TEXT NOT NULL DEFAULT '',

  created_by BIGINT REFERENCES users(id),
  started_at TIMESTAMPTZ,
  finished_at TIMESTAMPTZ,
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- 幂等唯一：同 actor + kind + key 只建一个作业。
-- 用部分索引是因为 idempotency_key 允许为空（内部作业不需要幂等键）。
CREATE UNIQUE INDEX IF NOT EXISTS uniq_jobs_idempotency
  ON jobs (job_kind, idempotency_key)
  WHERE idempotency_key <> '';

-- 抢占查询的主索引：按「可运行 + 到期时间」取下一个。
-- 部分索引只覆盖 pending（租约过期的 reclaimed 会回到 pending），
-- 因此这个索引很小且命中率高。
CREATE INDEX IF NOT EXISTS idx_jobs_claimable
  ON jobs (next_run_at, id)
  WHERE status = 'pending';

-- 「这个批次还有多少作业在跑」是批次详情的常用查询（R02 的在途数量）。
CREATE INDEX IF NOT EXISTS idx_jobs_batch_status
  ON jobs (batch_id, status)
  WHERE batch_id IS NOT NULL;

-- 过期租约回收：按 lease_until 扫描（部分索引只覆盖 leased/running）。
CREATE INDEX IF NOT EXISTS idx_jobs_lease_expiry
  ON jobs (lease_until)
  WHERE status IN ('leased', 'running');

CREATE INDEX IF NOT EXISTS idx_jobs_project_created
  ON jobs (project_id, created_at DESC, id DESC)
  WHERE project_id IS NOT NULL;

-- ---------------------------------------------------------------------------
-- job_attempts：每次尝试的审计与错误
-- ---------------------------------------------------------------------------
--
-- 为什么不把错误只写在 jobs 上：重试会覆盖上一次的错误，于是「第一次是限流、
-- 第二次是超时」这种关键信息会丢失，而它正是判断「该降并发还是该改提示词」的依据。
-- 与 batch_items 保留 attempt 的思路一致：历史事实只追加。

CREATE TABLE IF NOT EXISTS job_attempts (
  id BIGSERIAL PRIMARY KEY,
  job_id BIGINT NOT NULL REFERENCES jobs(id) ON DELETE CASCADE,
  attempt INTEGER NOT NULL CHECK (attempt >= 1),
  -- 每次尝试的租约持有者与 token：用于回答「这次尝试是谁做的、
  -- 它后来是否被判定为过期」（fencing 的取证依据）。
  lease_owner TEXT NOT NULL DEFAULT '',
  fencing_token BIGINT NOT NULL DEFAULT 0,
  outcome TEXT NOT NULL DEFAULT 'running'
    CHECK (outcome IN ('running', 'succeeded', 'failed', 'lease_expired', 'fenced')),
  error_class TEXT NOT NULL DEFAULT '',
  error_message TEXT NOT NULL DEFAULT '',
  started_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  finished_at TIMESTAMPTZ,
  UNIQUE (job_id, attempt)
);

CREATE INDEX IF NOT EXISTS idx_job_attempts_job ON job_attempts (job_id, attempt DESC);

-- ---------------------------------------------------------------------------
-- outbox：事务性派发保证
-- ---------------------------------------------------------------------------
--
-- 这是「API 落库后派发前崩溃」这一场景的正解（T06 验收项明确列出）：
-- 业务事务**同时**写 jobs + outbox，于是「作业存在」与「需要派发」同生共死。
-- 派发进程（dispatcher）从 outbox 取未派发事件投递 Redis，投递成功后标记。
--
-- Redis 丢失时从 DB 恢复：因为权威状态在 jobs 表，dispatcher 只需重新扫
-- 「pending 且未成功派发」的作业 —— 不依赖 Redis 里曾有什么。
--
-- 为什么消息体只放 id：见文件头。放载荷会让「重投」可能用旧载荷覆盖新状态。

CREATE TABLE IF NOT EXISTS outbox (
  id BIGSERIAL PRIMARY KEY,
  -- event_id 是**幂等键**：同一次业务动作重复写入 outbox 时必须命中同一条，
  -- 否则重试的业务事务会产生两条派发（进而两个 worker 抢同一作业）。
  event_id TEXT NOT NULL,
  topic TEXT NOT NULL,
  -- payload 只含对象 ID 与版本（契约 §5「不把大段样本内容塞进消息总线」）。
  payload JSONB NOT NULL DEFAULT '{}'::jsonb,

  status TEXT NOT NULL DEFAULT 'pending'
    CHECK (status IN ('pending', 'dispatched', 'failed')),
  attempts INTEGER NOT NULL DEFAULT 0 CHECK (attempts >= 0),
  -- next_attempt_at 让派发失败也能退避（Redis 短暂不可用时不要忙等）。
  next_attempt_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  last_error TEXT NOT NULL DEFAULT '',

  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  dispatched_at TIMESTAMPTZ,
  UNIQUE (event_id)
);

-- dispatcher 的主索引：按「待派发 + 到期」取批次。
CREATE INDEX IF NOT EXISTS idx_outbox_dispatchable
  ON outbox (next_attempt_at, id)
  WHERE status = 'pending';

-- ---------------------------------------------------------------------------
-- jobs ↔ outbox 的关系
-- ---------------------------------------------------------------------------
--
-- 刻意**不**给 outbox 加 job_id 外键：outbox 是通用的「需要派发什么」日志，
-- 将来会有非作业事件（例如发布通知）。作业派发事件的 payload 里带 jobId，
-- 由 dispatcher 解析；这样 outbox 的语义保持单一（派发意图），
-- 不与 jobs 的生命周期耦合（作业删了不代表派发意图不该被记录）。
