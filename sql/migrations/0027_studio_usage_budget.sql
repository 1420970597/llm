-- 0027: Atelier 模型能力、真实用量与预算预留（Issue #160 T07）
--
-- 契约来源：docs/plans/atelier-implementation.md §2.4（币种/精度/费用四态）、
-- §5（模型参数默认取能力声明）、§4.1（Usage / Budget 对象）；
-- docs/plans/atelier-api-contract.md §1.3（错误码 429）。
--
-- 本迁移要解决的问题（#160 T07 的原文判断）：
--   现有 generation_runs 只记录「跑过没跑过」，没有可结算的用量事实：
--     * 费用无处存放（既没有币种也没有精度约定，容易被写成浮点）；
--     * 无法回答「这一批已经花了多少、还剩多少」；
--     * 超时/无 usage 的请求无法表达「可能已收费但金额未知」，
--       实践中被记成 0 —— 于是预算永远看起来没超。
--
-- 四条硬约束（每一条都有对应的 CHECK 或 NULL 语义）：
--
-- 1. **金额一律整数最小货币单位（分）**：所有 *_minor 列都是 BIGINT，
--    没有任何 NUMERIC/DOUBLE。§2.4 原文「禁止用浮点表示金额」。
--    价格是「每百万 token 的分」，同样是整数 —— 这避免了单价本身带小数。
--
-- 2. **未知费用不是 0**：usage_ledger.actual_minor 是**可空**列，
--    NULL 表示「可能已收费但金额未知」。写 0 会让预算统计静默偏低，
--    而那是最危险的失真方向（超支看起来没超）。
--
-- 3. **预留与结算分离**：budget_reservations 保存**当前**的
--    reserved / settled / uncertain 三个计数器，抢占式条件更新
--    （见 internal/store/usage_store.go）在单条语句里完成「不超卖」判定，
--    不依赖表扫描聚合。逐请求的历史事实在 usage_ledger 里只追加。
--
-- 4. **凭证不进本迁移**：connection_id 只引用 model_providers，
--    明文密钥仍然只在 model_providers.encrypted_api_key 且经 crypto 解密。

-- ---------------------------------------------------------------------------
-- model_price_versions：价格版本（§2.4「价格版本与价格表单独版本化」）
-- ---------------------------------------------------------------------------
--
-- 为什么价格必须**版本化**而不是就地改：
-- 一次结算发生在上个月的价格下，而「现在的价格」可能已经变了。
-- 如果只有「当前价格」，历史用量的成本会随价格调整而漂移 ——
-- 于是同一个已发布的批次在不同时间显示不同成本，且无法复算对账。
--
-- 为什么按 (connection, model_name) 索引而不是只按模型名：
-- 同一个模型名在不同供应商/不同接入点的计费可能不同，而连接是计费口径的
-- 真实来源。endpoint_fingerprint 记录接入点（见 §2.4「连接 endpoint/config 指纹」），
-- 使「换了接入点」这件事在账上可见。

CREATE TABLE IF NOT EXISTS model_price_versions (
  id BIGSERIAL PRIMARY KEY,
  -- 人可读的版本标识，例如 `2026-09-01-cny`。与行 ID 分开：
  -- 对账时需要用**业务可读**的名字引用，而 ID 会随环境变化。
  price_version TEXT NOT NULL,
  provider_connection_id BIGINT NOT NULL REFERENCES model_providers(id) ON DELETE RESTRICT,
  endpoint_fingerprint TEXT NOT NULL DEFAULT '',
  model_name TEXT NOT NULL,
  currency TEXT NOT NULL DEFAULT 'CNY',
  -- 单价：每百万 token 的分。整数，避免单价本身带小数。
  input_price_minor_per_million BIGINT NOT NULL DEFAULT 0 CHECK (input_price_minor_per_million >= 0),
  output_price_minor_per_million BIGINT NOT NULL DEFAULT 0 CHECK (output_price_minor_per_million >= 0),
  -- 显式声明「这个接入点不收费」。
  --
  -- 为什么需要它：两个单价都为 0 有两种截然不同的含义 ——
  -- 「忘了填价格」与「确实是免费的」。如果把它们合并成一种，
  -- 那么「忘了填」会被当成免费（预算永远不涨，超支不报警），
  -- 而「确实免费」又无法表达。用一列把两种事实分开，
  -- 于是 0/0 且不是免费 = 未配置 = 费用未知（不是 0）。
  is_free BOOLEAN NOT NULL DEFAULT FALSE,
  -- 价格何时开始生效。回填历史账目时按 usage 的时间选版本。
  effective_from TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  note TEXT NOT NULL DEFAULT '',
  created_by BIGINT REFERENCES users(id),
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  -- 同版本名 + 同连接 + 同模型只能有一条：重复会让「用哪条价格」不确定。
  UNIQUE (price_version, provider_connection_id, model_name)
);

-- 取「某连接某模型在某个时间点的有效价格」的主索引。
CREATE INDEX IF NOT EXISTS idx_model_price_lookup
  ON model_price_versions (provider_connection_id, model_name, effective_from DESC);

-- ---------------------------------------------------------------------------
-- model_capabilities：能力声明（§5「模型参数默认取能力声明」）
-- ---------------------------------------------------------------------------
--
-- 为什么需要一张表（而不是在代码里写死判断）：
-- 「这个模型支不支持 temperature / 结构化输出 / 最大输出多少」是**供应商事实**，
-- 不同接入点的同名模型也可能不同，而且会随供应商升级变化。
-- 写死在代码里意味着每次变化都要发版；而 T28 的连接设置页需要一个
-- 让管理员声明与修正的地方。
--
-- 代码里的内置默认（internal/llm/capabilities.go）只作为**没有声明时**的
-- 保守回退，本表存在行时以本表为准。

CREATE TABLE IF NOT EXISTS model_capabilities (
  id BIGSERIAL PRIMARY KEY,
  provider_connection_id BIGINT NOT NULL REFERENCES model_providers(id) ON DELETE CASCADE,
  model_name TEXT NOT NULL,
  endpoint_fingerprint TEXT NOT NULL DEFAULT '',
  -- 是否接受 temperature。推理型模型普遍拒绝它（传了会 400），
  -- 因此「一律允许 temperature」会在真实供应商上直接失败。
  supports_temperature BOOLEAN NOT NULL DEFAULT TRUE,
  supports_reasoning_effort BOOLEAN NOT NULL DEFAULT FALSE,
  -- 是否支持 response_format=json_schema（严格结构化输出）。
  -- 不支持时只能用 prompt 约束 JSON，两者的失败模式完全不同，
  -- 因此必须能在提交前区分（否则用户看到的是「模型返回非法 JSON」）。
  supports_structured_output BOOLEAN NOT NULL DEFAULT FALSE,
  supports_json_mode BOOLEAN NOT NULL DEFAULT FALSE,
  -- 0 = 未声明（视为未知，而不是「不支持」）。
  max_output_tokens INTEGER NOT NULL DEFAULT 0 CHECK (max_output_tokens >= 0),
  max_context_tokens INTEGER NOT NULL DEFAULT 0 CHECK (max_context_tokens >= 0),
  declared_by TEXT NOT NULL DEFAULT 'operator',
  is_active BOOLEAN NOT NULL DEFAULT TRUE,
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  UNIQUE (provider_connection_id, model_name)
);

-- ---------------------------------------------------------------------------
-- budget_reservations：预算预留台账（§4.1 Usage / Budget）
-- ---------------------------------------------------------------------------
--
-- 粒度是 (project, currency) 一行，不是「一次请求一行」。理由：
--   * 「不超卖」的判定必须在一个可串行化的点上完成。一行一项目币种时，
--     一次条件 UPDATE 就是完整的判定（行锁天然串行化），
--     而「按请求行做 SUM 聚合」在 10 万单元批次下是 O(n²)，
--     并且需要额外的 FOR UPDATE 才能串行化 —— 两者都要付代价，但前者不需要扫描。
--   * 逐请求的事实并不丢：它在 usage_ledger 里，且与本表在同事务内更新。
--
-- uncertain_minor 是关键的一列：超时/无 usage 的请求金额未知但**可能已产生**，
-- 因此它既不能进 settled_minor（没有精确值），也不能被释放
-- （释放等于宣称「没花钱」）。单列存放使「不确定占用」可见且不阻塞后续预算判断。
CREATE TABLE IF NOT EXISTS budget_reservations (
  id BIGSERIAL PRIMARY KEY,
  project_id BIGINT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
  currency TEXT NOT NULL DEFAULT 'CNY',
  -- 0 = 未设上限（与 projects.budget_limit_minor 的 0 语义一致）。
  -- 未设上限时不做拦截，但仍累积用量，界面才能显示「已用多少」。
  limit_minor BIGINT NOT NULL DEFAULT 0 CHECK (limit_minor >= 0),
  -- 在途预留：已提交给外部供应商、尚未结算的金额。
  reserved_minor BIGINT NOT NULL DEFAULT 0 CHECK (reserved_minor >= 0),
  -- 已结算的实际费用。
  settled_minor BIGINT NOT NULL DEFAULT 0 CHECK (settled_minor >= 0),
  -- 未知费用：可能已产生但金额不确定。**不得**并入 settled_minor 冒充精确值，
  -- 也**不得**在结算时清零 —— 清了就等于宣称「确定没花钱」。
  uncertain_minor BIGINT NOT NULL DEFAULT 0 CHECK (uncertain_minor >= 0),
  -- 计数器版本：每次变更 +1。供对账与「这个数字是什么时候的」取证。
  version BIGINT NOT NULL DEFAULT 0,
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  UNIQUE (project_id, currency)
);

-- ---------------------------------------------------------------------------
-- usage_ledger：用量流水（逐请求/attempt 的事实）
-- ---------------------------------------------------------------------------
--
-- 为什么「身份」字段这么多：一次外部调用要能回答下面这些问题，
-- 而每一个都对应一个真实的排障场景：
--   * 是**哪个作业的哪一次 attempt** 花的钱（job_id/attempt）—— 重试会重复计费，
--     这是「不承诺恰好一次」的唯一可解释方式；
--   * 用的是**哪个连接、哪个模型、什么配置指纹**（connection_id/model_name/
--     config_fingerprint）—— 否则「撤销连接后是不是偷偷换了模型」无法取证；
--   * 供应商**实际**用了哪个模型（response_model_id）—— 别名/路由会与请求不同；
--   * 用的是**哪一版价格**（price_version_id/price_version）—— 否则历史成本会漂移；
--   * 传输层幂等键（idempotency_key）—— 断网重放不能重复计费两次。

CREATE TABLE IF NOT EXISTS usage_ledger (
  id BIGSERIAL PRIMARY KEY,
  project_id BIGINT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
  batch_id BIGINT REFERENCES batches(id) ON DELETE SET NULL,
  -- 刻意**不**给 job_id 加外键：jobs 会随批次/项目归档被清理，
  -- 而账目必须留存（财务事实不能因为业务对象被删就消失）。
  job_id BIGINT,
  attempt INTEGER NOT NULL DEFAULT 0 CHECK (attempt >= 0),
  -- purpose 说明这次调用在做什么（generation/judge/embedding/...），
  -- 使「成本花在哪一类调用上」可以在不读业务表的情况下回答。
  purpose TEXT NOT NULL DEFAULT '',

  -- 传输层幂等：同一逻辑请求重复提交（断网重放/重试）只应计费一次。
  idempotency_key TEXT NOT NULL,
  -- 供应商侧请求/响应标识（若有），用于与账单核对。
  request_id TEXT NOT NULL DEFAULT '',

  -- 连接与模型身份（**非秘密**：§2.4 明确不把明文密钥冻结进任何快照）。
  connection_id BIGINT REFERENCES model_providers(id) ON DELETE RESTRICT,
  endpoint_fingerprint TEXT NOT NULL DEFAULT '',
  model_name TEXT NOT NULL DEFAULT '',
  -- 供应商响应里回显的模型标识。与请求不同即为「被路由/别名替换」，
  -- 这是核对账单时的第一手证据。
  response_model_id TEXT NOT NULL DEFAULT '',
  -- 请求参数的指纹（温度/输出上限/schema 等）。同一连接同一模型下
  -- 配置不同会产生不同成本，因此必须能区分。
  config_fingerprint TEXT NOT NULL DEFAULT '',

  currency TEXT NOT NULL DEFAULT 'CNY',
  price_version_id BIGINT REFERENCES model_price_versions(id) ON DELETE RESTRICT,
  price_version TEXT NOT NULL DEFAULT '',

  -- token 用量可空：没有 usage 的响应（流式截断/供应商不回传）
  -- 不能用 0 冒充「没消耗」。
  input_tokens BIGINT CHECK (input_tokens IS NULL OR input_tokens >= 0),
  output_tokens BIGINT CHECK (output_tokens IS NULL OR output_tokens >= 0),
  -- 用量来源：provider=供应商回传；estimated=本地估算；unknown=无法得知。
  -- 三者的可信度不同，界面与对账必须能区分（§2.4）。
  usage_source TEXT NOT NULL DEFAULT 'unknown'
    CHECK (usage_source IN ('provider', 'estimated', 'unknown')),

  -- 金额四态（§2.4）。
  amount_state TEXT NOT NULL DEFAULT 'reserved'
    CHECK (amount_state IN ('estimated', 'actual', 'unknown', 'reserved')),
  -- 预留金额：提交前按「预估 token 上限 × 价格」预留，用于不超卖。
  reserved_minor BIGINT NOT NULL DEFAULT 0 CHECK (reserved_minor >= 0),
  -- 估计金额（有价格但无真实 usage 时的上界估计）。
  estimated_minor BIGINT NOT NULL DEFAULT 0 CHECK (estimated_minor >= 0),
  -- 实际金额。**可空且 NULL ≠ 0**：NULL 表示「可能已收费但金额未知」。
  actual_minor BIGINT CHECK (actual_minor IS NULL OR actual_minor >= 0),
  -- 结算状态。
  state TEXT NOT NULL DEFAULT 'reserved'
    CHECK (state IN ('reserved', 'settled', 'released')),

  -- 与供应商账单核对的状态（T01 留给 T07 定义的口径，见 §2.4）。
  --   not_attempted=尚未核对；pending=已提交核对请求待回；confirmed=账单一致；
  --   mismatch=账单不一致（需要人工介入）；impossible=无法核对
  --   （供应商不提供明细，例如超时后连请求 ID 都没有）。
  -- 「impossible」是一个**正当且必须存在**的终态：把无法核对硬标成 confirmed
  -- 会让对账报告失去意义。
  reconciliation TEXT NOT NULL DEFAULT 'not_attempted'
    CHECK (reconciliation IN ('not_attempted', 'pending', 'confirmed', 'mismatch', 'impossible')),
  reconciliation_note TEXT NOT NULL DEFAULT '',

  error_class TEXT NOT NULL DEFAULT '',
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),

  -- 幂等：同一次逻辑请求只产生一条账目。用唯一约束而不是「应用层先查后写」，
  -- 因为并发重放会同时通过应用层检查。
  UNIQUE (idempotency_key)
);

-- 项目成本报表的主索引（按时间倒序取最近的花费）。
CREATE INDEX IF NOT EXISTS idx_usage_ledger_project_created
  ON usage_ledger (project_id, created_at DESC, id DESC);

-- 批次成本与「在途预留」统计。
CREATE INDEX IF NOT EXISTS idx_usage_ledger_batch
  ON usage_ledger (batch_id, state)
  WHERE batch_id IS NOT NULL;

-- 未结算/未核对账目的巡检（T33 的可观测性指标）。
CREATE INDEX IF NOT EXISTS idx_usage_ledger_open
  ON usage_ledger (state, created_at)
  WHERE state = 'reserved';

CREATE INDEX IF NOT EXISTS idx_usage_ledger_reconciliation
  ON usage_ledger (reconciliation, created_at)
  WHERE reconciliation <> 'not_attempted';

-- ---------------------------------------------------------------------------
-- 批次级预算计数器
-- ---------------------------------------------------------------------------
--
-- 为什么要在 batches 上加三列而不是只留项目级台账：
-- §6.1 的批次创建命令接受 `budget: {currency, limitMinor}`，也就是**批次可以
-- 有自己的预算**（「这一批最多花 2000 分」）。如果只按项目聚合，那么
-- 「本批已花 800，上限 2000」就无法判定 —— 项目台账里混着别的批次的支出。
--
-- 用「行内计数器 + 条件更新」而不是「SUM(usage_ledger WHERE batch_id=...)」，
-- 原因同项目台账：10 万单元的批次下，每次预留都做一次聚合是 O(n²)，
-- 而且仍然需要一个额外的串行化点才能防超卖。
--
-- ADD COLUMN IF NOT EXISTS 保证迁移可重复执行：第二次执行时整条语句被跳过，
-- 因此 CHECK 约束不会被重复创建。
ALTER TABLE batches
  ADD COLUMN IF NOT EXISTS budget_reserved_minor BIGINT NOT NULL DEFAULT 0 CHECK (budget_reserved_minor >= 0),
  ADD COLUMN IF NOT EXISTS budget_settled_minor BIGINT NOT NULL DEFAULT 0 CHECK (budget_settled_minor >= 0),
  ADD COLUMN IF NOT EXISTS budget_uncertain_minor BIGINT NOT NULL DEFAULT 0 CHECK (budget_uncertain_minor >= 0);
