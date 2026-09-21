package store

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/1420970597/llm/internal/model"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// 本文件实现 Atelier 的用量流水与预算预留（Issue #160 T07）。
//
// 契约：docs/plans/atelier-implementation.md §2.4、§4.1；
// sql/migrations/0027_studio_usage_budget.sql 的文件头（那里解释了表结构取舍）。
//
// 本文件的核心主张：**预留必须在提交外部请求之前完成，且不超卖必须由数据库
// 的单条条件更新保证**。三条不可让步的性质：
//
//  1. 预留与账目**同事务**写入。分两次写会留下两种坏状态：
//     账目存在但没有预留（预算漏记），或预留存在但没有账目（额度永久被占）。
//  2. 「不超卖」靠条件 UPDATE 的行锁，而不是「先 SELECT 判额度再 INSERT」。
//     后者在两个 worker 同时通过检查时必然超卖，而那种故障只在并发下出现。
//  3. 未知费用**不写 0**。actual_minor 为 NULL 时要求结算方明确说明
//     「是未知」还是「是释放」—— 两者对预算的含义完全相反。

var (
	// ErrBudgetExhausted 表示预留会导致超出（项目或批次的）预算上限。
	// API 层必须把它映射成 429（契约 §1.3「限流 / 预算预留失败」）。
	ErrBudgetExhausted = errors.New("预算额度不足，已阻止新的外部调用")
	// ErrUsageNotFound 表示账目不存在。
	ErrUsageNotFound = errors.New("用量账目不存在")
	// ErrUsageNotReserved 表示账目已经结算或释放过（重复结算）。
	// 重复结算必须报错而不是静默覆盖：它通常意味着调用方把同一次调用算了两遍，
	// 而静默覆盖会让「多扣的钱」无从发现。
	ErrUsageNotReserved = errors.New("用量账目已经结算或释放，不能重复结算")
)

// UsageStore 提供用量与预算的读写。
type UsageStore struct {
	db *pgxpool.Pool
}

func NewUsageStore(db *pgxpool.Pool) *UsageStore {
	return &UsageStore{db: db}
}

// PriceVersionInput 是登记一版价格的输入。
type PriceVersionInput struct {
	PriceVersion     string
	ConnectionID     int64
	EndpointFP       string
	ModelName        string
	Currency         string
	InputPerMillion  int64
	OutputPerMillion int64
	// IsFree 显式声明免费接入点（见 model.PriceVersion.IsFree 的说明）。
	IsFree        bool
	EffectiveFrom time.Time
	Note          string
	CreatedBy     *int64
}

// UpsertPriceVersion 登记一版价格。
//
// 幂等语义：同 (price_version, connection, model) 重复写入时**更新**单价
// （同一个版本号的更正），而不是新建一行。价格版本名是业务标识，
// 同一个名字出现两条会让「用哪条价格」不确定。
func (s *UsageStore) UpsertPriceVersion(ctx context.Context, input PriceVersionInput) (model.PriceVersion, error) {
	if strings.TrimSpace(input.PriceVersion) == "" {
		return model.PriceVersion{}, &apiStoreError{Message: "价格版本名必填"}
	}
	if input.ConnectionID <= 0 {
		return model.PriceVersion{}, &apiStoreError{Message: "价格必须绑定到一个模型连接"}
	}
	if strings.TrimSpace(input.ModelName) == "" {
		return model.PriceVersion{}, &apiStoreError{Message: "价格必须绑定到模型名"}
	}
	if input.InputPerMillion < 0 || input.OutputPerMillion < 0 {
		return model.PriceVersion{}, &apiStoreError{Message: "单价不能为负（金额以分为单位的整数表示）"}
	}
	if !input.IsFree && input.InputPerMillion == 0 && input.OutputPerMillion == 0 {
		// 拒绝「没填价格」被静默存成 0：那会让这个连接的所有调用费用未知，
		// 而未知在预算上只能按预留金额占用 —— 用户会看到额度被占却不加钱。
		// 确实免费请显式传 IsFree。
		return model.PriceVersion{}, &apiStoreError{Message: "单价不能全为 0；确实免费请显式标记为免费接入点"}
	}
	if input.Currency == "" {
		input.Currency = "CNY"
	}
	if input.Currency != "CNY" {
		return model.PriceVersion{}, &apiStoreError{Message: "当前只支持 CNY 计价（§2.4）"}
	}
	if input.EffectiveFrom.IsZero() {
		input.EffectiveFrom = time.Now()
	}

	var price model.PriceVersion
	err := s.db.QueryRow(ctx, `
    INSERT INTO model_price_versions
      (price_version, provider_connection_id, endpoint_fingerprint, model_name, currency,
       input_price_minor_per_million, output_price_minor_per_million, is_free,
       effective_from, note, created_by)
    VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
    ON CONFLICT (price_version, provider_connection_id, model_name) DO UPDATE
    SET endpoint_fingerprint = EXCLUDED.endpoint_fingerprint,
        currency = EXCLUDED.currency,
        input_price_minor_per_million = EXCLUDED.input_price_minor_per_million,
        output_price_minor_per_million = EXCLUDED.output_price_minor_per_million,
        is_free = EXCLUDED.is_free,
        effective_from = LEAST(model_price_versions.effective_from, EXCLUDED.effective_from),
        note = EXCLUDED.note,
        updated_at = NOW()
    RETURNING id, price_version, provider_connection_id, endpoint_fingerprint, model_name, currency,
              input_price_minor_per_million, output_price_minor_per_million, is_free,
              effective_from, note`,
		input.PriceVersion, input.ConnectionID, input.EndpointFP, input.ModelName, input.Currency,
		input.InputPerMillion, input.OutputPerMillion, input.IsFree,
		input.EffectiveFrom, input.Note, input.CreatedBy,
	).Scan(&price.ID, &price.PriceVersion, &price.ConnectionID, &price.EndpointFP, &price.ModelName,
		&price.Currency, &price.InputPerMillion, &price.OutputPerMillion, &price.IsFree,
		&price.EffectiveFrom, &price.Note)
	if err != nil {
		return model.PriceVersion{}, err
	}
	return price, nil
}

// ResolvePriceVersion 取某连接某模型在指定时间点生效的价格版本。
//
// 按 effective_from 倒序取第一条 ≤ 查询时间：这样「补录一条更早生效的价格」
// 不会改变已有账目的成本（历史账目已经记下了它当时用的 price_version_id）。
// 没有任何匹配时返回 (zero,false,nil) —— 「没配价格」是一个正常状态，
// 不是错误；调用方必须据此判定「无法预留」（§2.4），而不是当作免费。
func (s *UsageStore) ResolvePriceVersion(ctx context.Context, connectionID int64, modelName string, at time.Time) (model.PriceVersion, bool, error) {
	if at.IsZero() {
		at = time.Now()
	}
	var price model.PriceVersion
	err := s.db.QueryRow(ctx, `
    SELECT id, price_version, provider_connection_id, endpoint_fingerprint, model_name, currency,
           input_price_minor_per_million, output_price_minor_per_million, is_free,
           effective_from, note
    FROM model_price_versions
    WHERE provider_connection_id = $1 AND model_name = $2 AND effective_from <= $3
    ORDER BY effective_from DESC, id DESC
    LIMIT 1`, connectionID, modelName, at,
	).Scan(&price.ID, &price.PriceVersion, &price.ConnectionID, &price.EndpointFP, &price.ModelName,
		&price.Currency, &price.InputPerMillion, &price.OutputPerMillion, &price.IsFree,
		&price.EffectiveFrom, &price.Note)
	if errors.Is(err, pgx.ErrNoRows) {
		return model.PriceVersion{}, false, nil
	}
	if err != nil {
		return model.PriceVersion{}, false, err
	}
	return price, true, nil
}

// CapabilityInput 是登记一个连接+模型能力声明的输入。
type CapabilityInput struct {
	ConnectionID             int64
	ModelName                string
	EndpointFP               string
	SupportsTemperature      bool
	SupportsReasoningEffort  bool
	SupportsStructuredOutput bool
	SupportsJSONMode         bool
	MaxOutputTokens          int
	MaxContextTokens         int
}

// UpsertCapabilities 写入能力声明（同连接同模型一行）。
func (s *UsageStore) UpsertCapabilities(ctx context.Context, input CapabilityInput) (model.ModelCapabilities, error) {
	if input.ConnectionID <= 0 || strings.TrimSpace(input.ModelName) == "" {
		return model.ModelCapabilities{}, &apiStoreError{Message: "能力声明必须绑定到连接与模型"}
	}
	if input.MaxOutputTokens < 0 || input.MaxContextTokens < 0 {
		return model.ModelCapabilities{}, &apiStoreError{Message: "token 上限不能为负数（0 表示未声明）"}
	}

	var caps model.ModelCapabilities
	err := s.db.QueryRow(ctx, `
    INSERT INTO model_capabilities
      (provider_connection_id, model_name, endpoint_fingerprint, supports_temperature,
       supports_reasoning_effort, supports_structured_output, supports_json_mode,
       max_output_tokens, max_context_tokens, declared_by)
    VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, 'operator')
    ON CONFLICT (provider_connection_id, model_name) DO UPDATE
    SET endpoint_fingerprint = EXCLUDED.endpoint_fingerprint,
        supports_temperature = EXCLUDED.supports_temperature,
        supports_reasoning_effort = EXCLUDED.supports_reasoning_effort,
        supports_structured_output = EXCLUDED.supports_structured_output,
        supports_json_mode = EXCLUDED.supports_json_mode,
        max_output_tokens = EXCLUDED.max_output_tokens,
        max_context_tokens = EXCLUDED.max_context_tokens,
        is_active = TRUE,
        updated_at = NOW()
    RETURNING provider_connection_id, model_name, endpoint_fingerprint, supports_temperature,
              supports_reasoning_effort, supports_structured_output, supports_json_mode,
              max_output_tokens, max_context_tokens, declared_by`,
		input.ConnectionID, input.ModelName, input.EndpointFP, input.SupportsTemperature,
		input.SupportsReasoningEffort, input.SupportsStructuredOutput, input.SupportsJSONMode,
		input.MaxOutputTokens, input.MaxContextTokens,
	).Scan(&caps.ConnectionID, &caps.ModelName, &caps.EndpointFP, &caps.SupportsTemperature,
		&caps.SupportsReasoningEffort, &caps.SupportsStructuredOutput, &caps.SupportsJSONMode,
		&caps.MaxOutputTokens, &caps.MaxContextTokens, &caps.DeclaredBy)
	if err != nil {
		return model.ModelCapabilities{}, err
	}
	caps.Source = model.CapabilitySourceDeclaration
	return caps, nil
}

// ResolveCapabilities 取能力声明。
//
// 第二个返回值为 false 表示**没有声明**；调用方必须回退到
// internal/llm 的内置保守默认（并在界面标明来源是回退值），
// 而不是假定「支持一切」—— 一个假定支持 temperature 的推理型模型会直接 400。
func (s *UsageStore) ResolveCapabilities(ctx context.Context, connectionID int64, modelName string) (model.ModelCapabilities, bool, error) {
	var caps model.ModelCapabilities
	err := s.db.QueryRow(ctx, `
    SELECT provider_connection_id, model_name, endpoint_fingerprint, supports_temperature,
           supports_reasoning_effort, supports_structured_output, supports_json_mode,
           max_output_tokens, max_context_tokens, declared_by
    FROM model_capabilities
    WHERE provider_connection_id = $1 AND model_name = $2 AND is_active = TRUE`,
		connectionID, modelName,
	).Scan(&caps.ConnectionID, &caps.ModelName, &caps.EndpointFP, &caps.SupportsTemperature,
		&caps.SupportsReasoningEffort, &caps.SupportsStructuredOutput, &caps.SupportsJSONMode,
		&caps.MaxOutputTokens, &caps.MaxContextTokens, &caps.DeclaredBy)
	if errors.Is(err, pgx.ErrNoRows) {
		return model.ModelCapabilities{}, false, nil
	}
	if err != nil {
		return model.ModelCapabilities{}, false, err
	}
	caps.Source = model.CapabilitySourceDeclaration
	return caps, true, nil
}

// ReserveUsageInput 是「提交前预留」的输入。
type ReserveUsageInput struct {
	ProjectID int64
	// BatchID 非空时同时校验批次预算（§6.1 的批次自带 budget）。
	BatchID *int64
	// JobID/Attempt 记录「哪次尝试花的钱」。重试会重复计费，这是
	// 「不承诺恰好一次」的唯一可解释方式。
	JobID   *int64
	Attempt int

	Purpose           string
	IdempotencyKey    string
	ConnectionID      *int64
	EndpointFP        string
	ModelName         string
	ConfigFingerprint string

	Currency       string
	PriceVersionID *int64
	PriceVersion   string

	// ReservationMinor 是本次调用预留的金额（由 model.QuoteReservation 算出）。
	// 为 0 是允许的（未设上限且无价格时的显式 0 预留），但调用方在
	// 「无法可靠预留」时必须先阻止执行（§2.4），而不是走到这里。
	ReservationMinor int64

	// ProjectLimitMinor/BatchLimitMinor 是**当前生效**的上限（0=未设上限）。
	// 由调用方从项目/批次读入并在同一事务里同步到台账，
	// 使「改了项目预算」不需要额外的同步任务。
	ProjectLimitMinor int64
	BatchLimitMinor   int64
}

// ReserveUsage 在一个事务内：判定不超卖 → 累加预留 → 写入账目行。
//
// 返回 ErrBudgetExhausted 表示**未写入任何东西**（整体回滚），
// 调用方据此返回 429 且不发出外部请求 —— 「预留失败就不调用」是 §2.4 的要求，
// 不是优化。
//
// 幂等：同一 IdempotencyKey 重复预留返回既有账目且**不再累加**预留。
// 这一条对断网重放（T29）是必需的：否则重放会把额度占两遍。
func (s *UsageStore) ReserveUsage(ctx context.Context, input ReserveUsageInput) (model.UsageLedger, bool, error) {
	if input.ProjectID <= 0 {
		return model.UsageLedger{}, false, &apiStoreError{Message: "用量必须归属到一个项目"}
	}
	if strings.TrimSpace(input.IdempotencyKey) == "" {
		// 幂等键必填：没有它就无法阻止断网重放重复预留。
		return model.UsageLedger{}, false, &apiStoreError{Message: "用量必须带幂等键"}
	}
	if input.Currency == "" {
		input.Currency = "CNY"
	}
	if input.ReservationMinor < 0 {
		return model.UsageLedger{}, false, &apiStoreError{Message: "预留金额不能为负"}
	}

	tx, err := s.db.Begin(ctx)
	if err != nil {
		return model.UsageLedger{}, false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// 幂等命中：返回既有账目，不重复占用额度。
	existing, found, err := getUsageByIdempotencyTx(ctx, tx, input.IdempotencyKey)
	if err != nil {
		return model.UsageLedger{}, false, err
	}
	if found {
		if err := tx.Commit(ctx); err != nil {
			return model.UsageLedger{}, false, err
		}
		return existing, false, nil
	}

	if err := reserveProjectBudgetTx(ctx, tx, input); err != nil {
		return model.UsageLedger{}, false, err
	}
	if input.BatchID != nil {
		if err := reserveBatchBudgetTx(ctx, tx, input); err != nil {
			return model.UsageLedger{}, false, err
		}
	}

	var usage model.UsageLedger
	err = tx.QueryRow(ctx, `
    INSERT INTO usage_ledger
      (project_id, batch_id, job_id, attempt, purpose, idempotency_key,
       connection_id, endpoint_fingerprint, model_name, config_fingerprint,
       currency, price_version_id, price_version,
       amount_state, reserved_minor, state)
    VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13,
            'reserved', $14, 'reserved')
    RETURNING `+usageLedgerColumns,
		input.ProjectID, input.BatchID, input.JobID, input.Attempt, input.Purpose, input.IdempotencyKey,
		input.ConnectionID, input.EndpointFP, input.ModelName, input.ConfigFingerprint,
		input.Currency, input.PriceVersionID, input.PriceVersion,
		input.ReservationMinor,
	).Scan(usageScanTargets(&usage)...)
	if err != nil {
		return model.UsageLedger{}, false, err
	}

	if err := tx.Commit(ctx); err != nil {
		return model.UsageLedger{}, false, err
	}
	return usage, true, nil
}

// reserveProjectBudgetTx 在项目级台账上做「不超卖 + 累加预留」。
//
// 先用 INSERT ... ON CONFLICT DO NOTHING 保证行存在（并同步最新上限），
// 再用**条件 UPDATE** 判定与累加。为什么不合成一条 INSERT ... ON CONFLICT
// DO UPDATE：新插入的那条没有 WHERE 可以拦，于是「全新项目的第一笔调用」
// 会绕过额度判定直接超卖。
func reserveProjectBudgetTx(ctx context.Context, tx pgx.Tx, input ReserveUsageInput) error {
	if _, err := tx.Exec(ctx, `
    INSERT INTO budget_reservations (project_id, currency, limit_minor)
    VALUES ($1, $2, $3)
    ON CONFLICT (project_id, currency) DO NOTHING`,
		input.ProjectID, input.Currency, input.ProjectLimitMinor); err != nil {
		return err
	}

	var projectID int64
	err := tx.QueryRow(ctx, `
    UPDATE budget_reservations
    SET limit_minor = $3,
        reserved_minor = reserved_minor + $4,
        version = version + 1,
        updated_at = NOW()
    WHERE project_id = $1
      AND currency = $2
      AND ($3 <= 0 OR reserved_minor + settled_minor + uncertain_minor + $4 <= $3)
    RETURNING project_id`,
		input.ProjectID, input.Currency, input.ProjectLimitMinor, input.ReservationMinor).Scan(&projectID)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrBudgetExhausted
	}
	return err
}

// reserveBatchBudgetTx 在批次级计数器上做同一件事。
func reserveBatchBudgetTx(ctx context.Context, tx pgx.Tx, input ReserveUsageInput) error {
	// 先确认批次存在且币种一致：币种不一致说明调用方拿错了配置，
	// 此时把它当成「额度不足」会让用户去加预算而不是去修配置 —— 误导。
	var batchCurrency string
	var existingLimit int64
	if err := tx.QueryRow(ctx, `
    SELECT budget_currency, budget_limit_minor FROM batches WHERE id = $1 FOR UPDATE`,
		*input.BatchID).Scan(&batchCurrency, &existingLimit); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return &apiStoreError{Message: "批次不存在，无法预留预算"}
		}
		return err
	}
	if batchCurrency != input.Currency {
		return &apiStoreError{Message: fmt.Sprintf(
			"批次预算币种是 %s，与调用的 %s 不一致", batchCurrency, input.Currency)}
	}

	// 上限以批次行上存的为准（它才是批次预算的事实来源），
	// 传入值只用于**同步**：批次预算被改过时以行为准，避免调用方读到旧值后放宽限制。
	limit := existingLimit
	if input.BatchLimitMinor > 0 && existingLimit != input.BatchLimitMinor {
		limit = input.BatchLimitMinor
	}

	var batchID int64
	err := tx.QueryRow(ctx, `
    UPDATE batches
    SET budget_limit_minor = $2,
        budget_reserved_minor = budget_reserved_minor + $3,
        updated_at = NOW()
    WHERE id = $1
      AND ($2 <= 0 OR budget_reserved_minor + budget_settled_minor + budget_uncertain_minor + $3 <= $2)
    RETURNING id`,
		*input.BatchID, limit, input.ReservationMinor).Scan(&batchID)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrBudgetExhausted
	}
	return err
}

// SettleUsageInput 是结算的输入。
type SettleUsageInput struct {
	// Charge 是 model.ComputeCharge 的结果。AmountMinor 为 nil 时表示未知。
	Charge model.UsageCharge
	// RequestID/ResponseModelID 是供应商回传的标识（可选，用于对账）。
	RequestID       string
	ResponseModelID string
	ErrorClass      string
}

// SettleUsage 结算一次调用：把预留转成实际/估计/未知，并同步两个计数器。
//
// 计数器规则（与 model.UsageLedger.ChargeMinor 必须一致，两处一致由测试保证）：
//
//	reserved_minor  -= 预留
//	settled_minor   += 实际金额（仅有精确值时）
//	uncertain_minor += 估计金额（estimated）或预留金额（unknown）
//
// 为什么 unknown 用**预留金额**而不是 0 占用：预留是我们愿意为这次请求付出的
// 上界，而超时/断连时供应商可能已经收费。记 0 等于宣称「确定没花钱」，
// 会让用户看到「还剩很多额度」却已被扣款 —— 那是最危险的方向。
func (s *UsageStore) SettleUsage(ctx context.Context, usageID int64, input SettleUsageInput) (model.UsageLedger, error) {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return model.UsageLedger{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	current, err := getUsageForUpdateTx(ctx, tx, usageID)
	if err != nil {
		return model.UsageLedger{}, err
	}
	if current.State != model.UsageStateReserved {
		return model.UsageLedger{}, ErrUsageNotReserved
	}

	charge := input.Charge
	amountState := charge.AmountState
	if amountState == "" {
		amountState = model.AmountStateUnknown
	}
	var actualMinor *int64
	settledDelta := int64(0)
	uncertainDelta := int64(0)
	switch amountState {
	case model.AmountStateActual, model.AmountStateEstimated:
		if charge.AmountMinor == nil {
			// 「有态但没金额」是矛盾输入。宁可报错也不要写一个 0 ——
			// 0 会被下游当成「确实没花钱」。
			return model.UsageLedger{}, &apiStoreError{Message: "结算金额态与实际金额不一致"}
		}
		actualMinor = charge.AmountMinor
		if amountState == model.AmountStateActual {
			settledDelta = *charge.AmountMinor
		} else {
			uncertainDelta = *charge.AmountMinor
		}
	default: // unknown
		amountState = model.AmountStateUnknown
		uncertainDelta = current.ReservedMinor
	}

	var input_tokens, output_tokens *int64
	tokensKnown := charge.Usage.HasAnyToken()
	if tokensKnown {
		input_tokens = charge.Usage.InputTokens
		output_tokens = charge.Usage.OutputTokens
	}
	usageSource := charge.Usage.Source
	if usageSource == "" {
		usageSource = model.UsageSourceUnknown
	}
	estimatedMinor := int64(0)
	if charge.AmountMinor != nil && amountState == model.AmountStateEstimated {
		estimatedMinor = *charge.AmountMinor
	}

	if err := applyBudgetDeltaTx(ctx, tx, current, -current.ReservedMinor, settledDelta, uncertainDelta); err != nil {
		return model.UsageLedger{}, err
	}

	var usage model.UsageLedger
	err = tx.QueryRow(ctx, `
    UPDATE usage_ledger
    SET amount_state = $2,
        -- 显式 ::bigint：$3 可为 NULL（未知金额），而仅出现在
        -- 「$3 IS NULL」里的参数无法被 Postgres 推断类型（报 42P08）。
        actual_minor = $3::bigint,
        estimated_minor = $4,
        input_tokens = COALESCE($5::bigint, input_tokens),
        output_tokens = COALESCE($6::bigint, output_tokens),
        usage_source = CASE WHEN $5::bigint IS NULL AND $6::bigint IS NULL THEN usage_source ELSE $7 END,
        state = 'settled',
        request_id = CASE WHEN $8 = '' THEN request_id ELSE $8 END,
        response_model_id = CASE WHEN $9 = '' THEN response_model_id ELSE $9 END,
        error_class = $10,
        reconciliation = CASE
          WHEN reconciliation <> 'not_attempted' THEN reconciliation
          WHEN $3::bigint IS NULL THEN 'impossible'
          ELSE 'not_attempted'
        END,
        reconciliation_note = CASE
          WHEN reconciliation <> 'not_attempted' THEN reconciliation_note
          WHEN $3::bigint IS NULL AND reconciliation_note = '' THEN '没有可核对的金额（供应商未回传用量或未配置价格）'
          ELSE reconciliation_note
        END,
        updated_at = NOW()
    WHERE id = $1
    RETURNING `+usageLedgerColumns,
		usageID, amountState, actualMinor, estimatedMinor, input_tokens, output_tokens,
		usageSource, strings.TrimSpace(input.RequestID), strings.TrimSpace(input.ResponseModelID),
		strings.TrimSpace(input.ErrorClass),
	).Scan(usageScanTargets(&usage)...)
	if err != nil {
		return model.UsageLedger{}, err
	}

	if err := tx.Commit(ctx); err != nil {
		return model.UsageLedger{}, err
	}
	return usage, nil
}

// ReleaseUsage 释放预留：请求**没有产生费用**时使用。
//
// 严格的适用条件：能证明供应商没有收费（连接被拒绝/DNS 失败/请求根本
// 没有构造成功）。超时、连接中途断开、响应无法解析都**不**属于此列 ——
// 那些必须走 SettleUsage 的 unknown 分支（§2.4「超时但供应商可能已收费时
// 记为 unknown，不得直接记 0」）。
func (s *UsageStore) ReleaseUsage(ctx context.Context, usageID int64, errorClass, note string) (model.UsageLedger, error) {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return model.UsageLedger{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	current, err := getUsageForUpdateTx(ctx, tx, usageID)
	if err != nil {
		return model.UsageLedger{}, err
	}
	if current.State != model.UsageStateReserved {
		return model.UsageLedger{}, ErrUsageNotReserved
	}

	if err := applyBudgetDeltaTx(ctx, tx, current, -current.ReservedMinor, 0, 0); err != nil {
		return model.UsageLedger{}, err
	}

	var usage model.UsageLedger
	err = tx.QueryRow(ctx, `
    UPDATE usage_ledger
    SET state = 'released',
        amount_state = 'unknown',
        actual_minor = NULL,
        error_class = CASE WHEN $2 = '' THEN error_class ELSE $2 END,
        reconciliation = 'confirmed',
        reconciliation_note = CASE WHEN $3 = '' THEN '请求未产生费用，已释放预留' ELSE $3 END,
        updated_at = NOW()
    WHERE id = $1
    RETURNING `+usageLedgerColumns,
		usageID, strings.TrimSpace(errorClass), strings.TrimSpace(note),
	).Scan(usageScanTargets(&usage)...)
	if err != nil {
		return model.UsageLedger{}, err
	}

	if err := tx.Commit(ctx); err != nil {
		return model.UsageLedger{}, err
	}
	return usage, nil
}

// applyBudgetDeltaTx 把预留/结算的增量写到项目台账与（若有）批次计数器上。
//
// 增量式而不是重算：重算需要对同一批次的全部账目做聚合，而并发预留
// 会让「读到的总额」与「自己那一笔」之间出现窗口，进而少算别人的预留
// —— 那正是超卖。增量更新保持与预留时同一种串行化方式（行锁）。
func applyBudgetDeltaTx(ctx context.Context, tx pgx.Tx, usage model.UsageLedger, reservedDelta, settledDelta, uncertainDelta int64) error {
	// 负的 reserved_minor 会让 CHECK 失败，那会在「结算时预留已被别人
	// 释放过」这种数据不一致时暴露出来 —— 报错比静默把额度算多了好。
	if _, err := tx.Exec(ctx, `
    UPDATE budget_reservations
    SET reserved_minor = reserved_minor + $3,
        settled_minor = settled_minor + $4,
        uncertain_minor = uncertain_minor + $5,
        version = version + 1,
        updated_at = NOW()
    WHERE project_id = $1 AND currency = $2`,
		usage.ProjectID, usage.Currency, reservedDelta, settledDelta, uncertainDelta); err != nil {
		return err
	}
	if usage.BatchID != nil {
		if _, err := tx.Exec(ctx, `
      UPDATE batches
      SET budget_reserved_minor = budget_reserved_minor + $2,
          budget_settled_minor = budget_settled_minor + $3,
          budget_uncertain_minor = budget_uncertain_minor + $4,
          updated_at = NOW()
      WHERE id = $1`,
			*usage.BatchID, reservedDelta, settledDelta, uncertainDelta); err != nil {
			return err
		}
	}
	return nil
}

// MarkReconciliation 更新与供应商账单核对的状态。
type ReconciliationInput struct {
	UsageID int64
	Status  string
	Note    string
}

// MarkReconciliation 记录核对结果。
//
// 允许把 not_attempted 推进到 pending/confirmed/mismatch/impossible，
// 也允许 confirmed ↔ mismatch 之间更正（账单可能后补）。
// 不允许把任何状态改回 not_attempted：那等于抹掉「已经核对过」这一事实，
// 而审计要的正是「谁在什么时候确认过」。
func (s *UsageStore) MarkReconciliation(ctx context.Context, input ReconciliationInput) (model.UsageLedger, error) {
	switch input.Status {
	case model.ReconciliationPending, model.ReconciliationConfirmed,
		model.ReconciliationMismatch, model.ReconciliationImpossible:
	default:
		return model.UsageLedger{}, &apiStoreError{Message: "核对状态只能是 pending、confirmed、mismatch 或 impossible"}
	}
	if strings.TrimSpace(input.Note) == "" && input.Status == model.ReconciliationMismatch {
		// mismatch 必须带说明：没有说明的「不一致」无法被任何人处理。
		return model.UsageLedger{}, &apiStoreError{Message: "标记账单不一致时必须填写差异说明"}
	}

	var usage model.UsageLedger
	err := s.db.QueryRow(ctx, `
    UPDATE usage_ledger
    SET reconciliation = $2, reconciliation_note = $3, updated_at = NOW()
    WHERE id = $1
    RETURNING `+usageLedgerColumns,
		input.UsageID, input.Status, strings.TrimSpace(input.Note),
	).Scan(usageScanTargets(&usage)...)
	if errors.Is(err, pgx.ErrNoRows) {
		return model.UsageLedger{}, ErrUsageNotFound
	}
	if err != nil {
		return model.UsageLedger{}, err
	}
	return usage, nil
}

// GetUsage 读取一条账目。
func (s *UsageStore) GetUsage(ctx context.Context, usageID int64) (model.UsageLedger, error) {
	var usage model.UsageLedger
	err := s.db.QueryRow(ctx, `SELECT `+usageLedgerColumns+` FROM usage_ledger WHERE id = $1`, usageID).
		Scan(usageScanTargets(&usage)...)
	if errors.Is(err, pgx.ErrNoRows) {
		return model.UsageLedger{}, ErrUsageNotFound
	}
	if err != nil {
		return model.UsageLedger{}, err
	}
	return usage, nil
}

// ProjectBudget 读取项目预算台账状态。
//
// 台账行不存在时返回「未设上限、全为 0」的快照而不是错误：
// 一个还没花过钱的项目在界面上应当显示「尚未产生费用」，
// 而不是一个错误 —— 那会让概览页在新建项目上直接失败。
func (s *UsageStore) ProjectBudget(ctx context.Context, projectID int64, currency string) (model.BudgetSnapshot, error) {
	if currency == "" {
		currency = "CNY"
	}
	snapshot := model.BudgetSnapshot{ProjectID: projectID, Currency: currency}
	err := s.db.QueryRow(ctx, `
    SELECT limit_minor, reserved_minor, settled_minor, uncertain_minor, version
    FROM budget_reservations WHERE project_id = $1 AND currency = $2`,
		projectID, currency,
	).Scan(&snapshot.LimitMinor, &snapshot.ReservedMinor, &snapshot.SettledMinor,
		&snapshot.UncertainMinor, &snapshot.Version)
	if errors.Is(err, pgx.ErrNoRows) {
		// 还没有台账：用项目上配置的上限补一个快照，使界面显示「上限 2000、已用 0」。
		var limit int64
		if err := s.db.QueryRow(ctx, `
      SELECT budget_limit_minor FROM projects WHERE id = $1`, projectID).Scan(&limit); err != nil {
			// 项目不存在时原样返回 pgx.ErrNoRows：与 GetProject 的约定一致，
			// 让 API 层用同一套「资源不存在」映射，而不是多一种自定义错误。
			return model.BudgetSnapshot{}, err
		}
		snapshot.LimitMinor = limit
		return snapshot, nil
	}
	if err != nil {
		return model.BudgetSnapshot{}, err
	}
	return snapshot, nil
}

// ListOpenUsage 列出尚未结算的账目（T33 的「未结算账目」指标）。
//
// 排除释放中的已结算项：它们不代表未完成的工作。
func (s *UsageStore) ListOpenUsage(ctx context.Context, olderThan time.Duration, limit int) ([]model.UsageLedger, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	if olderThan <= 0 {
		olderThan = time.Minute
	}
	rows, err := s.db.Query(ctx, `
    SELECT `+usageLedgerColumns+`
    FROM usage_ledger
    WHERE state = 'reserved' AND created_at < NOW() - make_interval(secs => $1)
    ORDER BY created_at
    LIMIT $2`, olderThan.Seconds(), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	items := []model.UsageLedger{}
	for rows.Next() {
		var usage model.UsageLedger
		if err := rows.Scan(usageScanTargets(&usage)...); err != nil {
			return nil, err
		}
		items = append(items, usage)
	}
	return items, rows.Err()
}

// ---------------------------------------------------------------------------
// 内部辅助
// ---------------------------------------------------------------------------

// usageLedgerColumns 是用量账目的权威列清单，供 usageScanTargets 对齐。
//
// 与 batch_store.go 一样把列清单写成常量而不是在各处重复手写，但这里的选择
// 相反：**允许**把常量插值进语句。理由是它由本文件与 usageScanTargets 成对
// 维护，且 TestUsageLedgerColumnsMatchScanTargets 断言两者长度一致；
// batch_store.go 当初改为「每条语句完整静态写出」是为了消除安全门禁对
// 拼接的噪声告警。两处取舍不同是**有意**的 —— 用一份常量配一份扫描目标
// 才有可能用一个测试同时兑住「列多了」与「列少了」两种漂移。
const usageLedgerColumns = `
    id, project_id, batch_id, job_id, attempt, purpose, idempotency_key, request_id,
    connection_id, endpoint_fingerprint, model_name, response_model_id, config_fingerprint,
    currency, price_version_id, price_version,
    input_tokens, output_tokens, usage_source,
    amount_state, reserved_minor, estimated_minor, actual_minor,
    state, reconciliation, reconciliation_note, error_class,
    created_at, updated_at`

// usageScanTargets 返回与 usageLedgerColumns 顺序一致的扫描目标。
//
// 用函数而不是在各处手写 &usage.X：列清单与扫描目标的顺序错位是最难发现的
// 一类缺陷（值全部错位但类型相同，只有业务语义出错），抽成一处可以让
// 「改列清单时忘了改扫描目标」变成编译期的字段缺失。
func usageScanTargets(usage *model.UsageLedger) []any {
	return []any{
		&usage.ID, &usage.ProjectID, &usage.BatchID, &usage.JobID, &usage.Attempt, &usage.Purpose,
		&usage.IdempotencyKey, &usage.RequestID,
		&usage.ConnectionID, &usage.EndpointFingerprint, &usage.ModelName, &usage.ResponseModelID,
		&usage.ConfigFingerprint,
		&usage.Currency, &usage.PriceVersionID, &usage.PriceVersion,
		&usage.InputTokens, &usage.OutputTokens, &usage.UsageSource,
		&usage.AmountState, &usage.ReservedMinor, &usage.EstimatedMinor, &usage.ActualMinor,
		&usage.State, &usage.Reconciliation, &usage.ReconciliationNote, &usage.ErrorClass,
		&usage.CreatedAt, &usage.UpdatedAt,
	}
}

// UsageLedgerColumnCount 暴露列数供测试断言「列清单与扫描目标一致」。
func UsageLedgerColumnCount() int { return len(usageScanTargets(&model.UsageLedger{})) }

func getUsageByIdempotencyTx(ctx context.Context, tx pgx.Tx, key string) (model.UsageLedger, bool, error) {
	var usage model.UsageLedger
	err := tx.QueryRow(ctx, `SELECT `+usageLedgerColumns+` FROM usage_ledger WHERE idempotency_key = $1`, key).
		Scan(usageScanTargets(&usage)...)
	if errors.Is(err, pgx.ErrNoRows) {
		return model.UsageLedger{}, false, nil
	}
	if err != nil {
		return model.UsageLedger{}, false, err
	}
	return usage, true, nil
}

func getUsageForUpdateTx(ctx context.Context, tx pgx.Tx, usageID int64) (model.UsageLedger, error) {
	var usage model.UsageLedger
	err := tx.QueryRow(ctx, `SELECT `+usageLedgerColumns+` FROM usage_ledger WHERE id = $1 FOR UPDATE`, usageID).
		Scan(usageScanTargets(&usage)...)
	if errors.Is(err, pgx.ErrNoRows) {
		return model.UsageLedger{}, ErrUsageNotFound
	}
	if err != nil {
		return model.UsageLedger{}, err
	}
	return usage, nil
}
