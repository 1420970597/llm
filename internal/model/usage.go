package model

import (
	"fmt"
	"math"
	"strings"
	"time"
)

// 本文件定义 Atelier 的用量与预算对象（Issue #160 T07）。
//
// 契约：docs/plans/atelier-implementation.md §2.4（币种/精度/费用四态）、
// §4.1（Usage / Budget）、§5（模型参数默认取能力声明）。
//
// 两个不可让步的约定，整个包的实现都围绕它们：
//
//  1. **金额一律整数最小货币单位（分）**，类型是 int64，没有任何浮点路径。
//     §2.4 原文「禁止用浮点表示金额」。浮点的代价不是精度误差本身，
//     而是「同一个已发布批次在不同时间显示不同成本」，那会让对账无法进行。
//
//  2. **未知不是 0**。`actual_minor` 在 DB 里可空，在 Go 里是 *int64；
//     nil 表示「可能已收费但金额未知」。写成 0 会让预算统计静默偏低，
//     而偏低是最危险的失真方向 —— 超支看起来没超。

// 费用四态（§2.4）。
const (
	// AmountStateReserved 表示「已预留、尚未结算」。
	AmountStateReserved = "reserved"
	// AmountStateEstimated 表示「有价格与用量，但金额是上界估计」。
	AmountStateEstimated = "estimated"
	// AmountStateActual 表示「有精确的实际金额」。
	AmountStateActual = "actual"
	// AmountStateUnknown 表示「可能已产生费用但金额未知」。
	AmountStateUnknown = "unknown"
)

// 用量来源。三者的可信度不同，界面与对账必须能区分。
const (
	// UsageSourceProvider 表示 token 数来自供应商响应。
	UsageSourceProvider = "provider"
	// UsageSourceEstimated 表示 token 数是本地估算。
	UsageSourceEstimated = "estimated"
	// UsageSourceUnknown 表示无法得知用量。
	UsageSourceUnknown = "unknown"
)

// 结算状态。settled 覆盖 actual/estimated/unknown 三种金额态：
// 它只表示「这次调用不会再产生新的用量」，不表示金额精确。
const (
	UsageStateReserved = "reserved"
	UsageStateSettled  = "settled"
	UsageStateReleased = "released"
)

// 与供应商账单核对的状态（T01 在 §2.4 留给本任务定义的口径）。
//
// 为什么必须有一个「无法核对」的终态：把无法核对硬标成 confirmed
// 会让对账报告失去意义；而如果只允许 confirmed/mismatch，
// 运维就会被迫在两者之间选一个，于是「没有明细可比」被伪装成「已对平」。
const (
	// ReconciliationNotAttempted 表示尚未核对。
	ReconciliationNotAttempted = "not_attempted"
	// ReconciliationPending 表示已提交核对请求，等待账单。
	ReconciliationPending = "pending"
	// ReconciliationConfirmed 表示与账单一致。
	ReconciliationConfirmed = "confirmed"
	// ReconciliationMismatch 表示与账单不一致，需要人工介入。
	ReconciliationMismatch = "mismatch"
	// ReconciliationImpossible 表示无法核对（供应商不提供明细，
	// 例如超时后连请求 ID 都没有）。
	ReconciliationImpossible = "impossible"
)

// tokensPerMillion 是价格单位的分母：单价以「分/百万 token」表示。
//
// 为什么单价也用整数：如果用「分/token」的浮点单价，那么 0.00002 元/token
// 这种常见价格就必然带小数，等于把浮点从金额挪到单价上，问题没有消失。
const tokensPerMillion = 1_000_000

// maxTokensForCost 是参与成本计算时的 token 数上界（10 亿）。
//
// 存在的理由不是「业务上不可能更多」，而是防止乘法溢出把成本算成负数 ——
// 一个负数成本会直接破坏预算判定（`已用 + 新增 <= 上限` 恒真）。
// 超过上界时宁可报告「无法计算」，也不要给出一个错得离谱的数字。
const maxTokensForCost = 1_000_000_000

// TokenUsage 是一次调用的 token 用量。
//
// 两个字段都是指针：nil 表示「没能拿到这个数字」，与 0 是不同的事实。
// 供应商只回传 output_tokens 时，把 input 记成 0 会让单价计算把
// 输入成本算成免费 —— 那是系统性低估。
type TokenUsage struct {
	InputTokens  *int64
	OutputTokens *int64
	Source       string
}

// HasAnyToken 判断是否至少知道一个方向的用量。
func (u TokenUsage) HasAnyToken() bool {
	return u.InputTokens != nil || u.OutputTokens != nil
}

// TotalTokens 返回已知 token 总数；两个方向都未知时返回 (0,false)。
func (u TokenUsage) TotalTokens() (int64, bool) {
	if !u.HasAnyToken() {
		return 0, false
	}
	var total int64
	if u.InputTokens != nil {
		total += *u.InputTokens
	}
	if u.OutputTokens != nil {
		total += *u.OutputTokens
	}
	return total, true
}

// PriceVersion 是一版价格（§2.4「价格版本与价格表单独版本化」）。
//
// 刻意不叫 Price：名字里带 Version 是为了让「这个价格属于哪一版」
// 在每次使用时都要显式回答，而不是让调用方用一个隐式的「当前价格」。
type PriceVersion struct {
	ID               int64  `json:"id"`
	PriceVersion     string `json:"priceVersion"`
	ConnectionID     int64  `json:"connectionId"`
	EndpointFP       string `json:"endpointFingerprint"`
	ModelName        string `json:"modelName"`
	Currency         string `json:"currency"`
	InputPerMillion  int64  `json:"inputPriceMinorPerMillion"`
	OutputPerMillion int64  `json:"outputPriceMinorPerMillion"`
	// IsFree 显式声明「这个接入点不收费」。
	//
	// 为什么必须与「两个单价都是 0」区分开：0/0 有两种含义 ——
	// 「忘了填价格」与「确实免费」。合并成一种的后果是：
	// 忘了填会被当成免费（预算永远不涨、超支不报警），
	// 而确实免费无法表达。分开之后，0/0 且非 IsFree = 未配置 = 费用未知。
	IsFree        bool      `json:"isFree"`
	EffectiveFrom time.Time `json:"effectiveFrom"`
	Note          string    `json:"note"`
}

// IsZero 判断这一版价格是否「未配置」（**不是**「免费」）。
//
// 判据是「两个单价都是 0 且未显式声明免费」。把「忘了填价格」当成免费
// 会让预算永远不涨 —— 那正是最危险的失真方向。确实免费的接入点
// 用 IsFree 显式声明，那时费用是精确的 0（而不是未知）。
func (p PriceVersion) IsZero() bool {
	return !p.IsFree && p.InputPerMillion == 0 && p.OutputPerMillion == 0
}

// UsageCharge 结算一次调用的结果。
type UsageCharge struct {
	// AmountMinor 是结算金额；nil 表示未知（**不是** 0）。
	AmountMinor *int64 `json:"amountMinor"`
	// AmountState 是四态之一。
	AmountState string `json:"amountState"`
	// Usage 是参与计算的 token 用量。
	Usage TokenUsage `json:"-"`
	// PriceVersion 是参与计算的价格版本名（空表示没有可用价格）。
	PriceVersion string `json:"priceVersion"`
	// Note 解释金额是怎么来的（面向运维与对账，不面向终端用户）。
	Note string `json:"note"`
}

// ComputeCharge 按用量与价格计算一次调用的费用。
//
// 三条语义（都可被测试直接断言）：
//
//  1. **两个方向分别向上取整**。分是最小单位，而 1 token 的成本通常不足 1 分；
//     四舍五入会让小额调用几乎全部免费，而向上取整只会略微高估 ——
//     高估是可接受的（用户少花一点额度），低估不可接受（超支不报警）。
//  2. **缺少任一必要输入就返回 unknown**，绝不返回 0。缺用量或没价格时，
//     唯一诚实的结果是「不知道」。
//  3. **溢出/越界时同样返回 unknown**。宁可承认算不出来，也不要给出一个
//     错误数字（负数成本会直接破坏预算判定）。
func ComputeCharge(usage TokenUsage, price PriceVersion, priceVersionName string) UsageCharge {
	if price.IsZero() {
		return UsageCharge{
			AmountMinor:  nil,
			AmountState:  AmountStateUnknown,
			Usage:        usage,
			PriceVersion: "",
			Note:         "该连接与模型尚未配置价格版本，费用无法计算（不是 0）",
		}
	}
	// 显式声明免费的接入点：0 是**精确**的，不是未知。
	// 放在用量检查之前：免费接入点不需要 token 数也能确定费用。
	if price.IsFree {
		free := int64(0)
		return UsageCharge{
			AmountMinor:  &free,
			AmountState:  AmountStateActual,
			Usage:        usage,
			PriceVersion: priceVersionName,
			Note:         "该接入点已声明为免费（价格版本显式标记 is_free）",
		}
	}

	if !usage.HasAnyToken() {
		return UsageCharge{
			AmountMinor:  nil,
			AmountState:  AmountStateUnknown,
			Usage:        usage,
			PriceVersion: priceVersionName,
			Note:         "供应商未回传 token 用量，费用无法计算（不是 0）",
		}
	}

	// 依赖方向未知与已知为 0 的区别：只用已知的方向计价，
	// 未知方向不参与（不能当作 0 计成免费，但也不能编造一个数量）。
	var total int64
	if usage.InputTokens != nil {
		part, ok := perMillionCost(*usage.InputTokens, price.InputPerMillion)
		if !ok {
			return UsageCharge{AmountMinor: nil, AmountState: AmountStateUnknown, Usage: usage,
				PriceVersion: priceVersionName, Note: "输入 token 数超出可计算范围，费用无法计算（不是 0）"}
		}
		total += part
	}
	if usage.OutputTokens != nil {
		part, ok := perMillionCost(*usage.OutputTokens, price.OutputPerMillion)
		if !ok {
			return UsageCharge{AmountMinor: nil, AmountState: AmountStateUnknown, Usage: usage,
				PriceVersion: priceVersionName, Note: "输出 token 数超出可计算范围，费用无法计算（不是 0）"}
		}
		total += part
	}

	source := usage.Source
	if source == "" {
		source = UsageSourceUnknown
	}
	// token 数来自本地估算时，金额只能是估计值，不能升级成 actual。
	amountState := AmountStateActual
	note := "按供应商回传的 token 数与价格版本计算"
	if source != UsageSourceProvider {
		amountState = AmountStateEstimated
		note = "按本地估算的 token 数与价格版本计算，是上界估计而非精确值"
	}
	amount := total
	return UsageCharge{
		AmountMinor:  &amount,
		AmountState:  amountState,
		Usage:        usage,
		PriceVersion: priceVersionName,
		Note:         note,
	}
}

// perMillionCost 计算 tokens 个 token 在「每百万 token price 分」下的成本（向上取整）。
//
// 返回 ok=false 表示参数越界或会溢出 —— 调用方必须据此给出 unknown，
// 而不是把溢出后的（可能是负的）结果当成费用。
func perMillionCost(tokens, priceMinorPerMillion int64) (int64, bool) {
	if tokens <= 0 || priceMinorPerMillion <= 0 {
		return 0, true
	}
	if tokens > maxTokensForCost {
		return 0, false
	}
	// 先判溢出再相乘：price 与 tokens 都是正数，乘积可能超过 int64。
	if priceMinorPerMillion > math.MaxInt64/tokens {
		return 0, false
	}
	product := tokens * priceMinorPerMillion
	if product > math.MaxInt64-(tokensPerMillion-1) {
		return 0, false
	}
	return (product + tokensPerMillion - 1) / tokensPerMillion, true
}

// ReservationQuote 是提交外部请求前应预留的金额。
type ReservationQuote struct {
	// AmountMinor 是预留金额；Ok=false 时该值无意义。
	AmountMinor int64
	// Ok=false 表示**无法可靠预留**。§2.4 明确要求这种情况阻止新的自动执行
	// 并要求补齐价格/上限，而不是「先跑起来再说」。
	Ok bool
	// Reason 面向运维解释为什么无法预留。
	Reason string
}

// QuoteReservation 计算一次调用的预留金额。
//
// 预留的语义是「为这次请求可能产生的最大费用占住额度」：
//
//	预留 = 输入上界估算 × 输入单价 + 输出上限 × 输出单价（都向上取整）
//
// 为什么用输出**上限**而不是期望值：预留必须是不低于实际花费的上界，
// 否则并发预留会集体低估，于是「不超卖」在数学上就不成立。
// 代价是额度被暂时占得偏多，这部分在结算时归还。
//
// 没有价格时 Ok=false：无法预留就必须阻止自动执行（§2.4）。
// 没有上限输出时同样 Ok=false —— 一个不知道上限的请求无法界定风险。
func QuoteReservation(estimatedInputTokens, maxOutputTokens int64, price PriceVersion) ReservationQuote {
	if price.IsZero() {
		return ReservationQuote{Ok: false, Reason: "该连接与模型没有可用价格版本，无法预留预算；补齐价格或设置预算策略后再执行"}
	}
	if price.IsFree {
		// 免费接入点无需预留，也无需输出上限 —— 但它仍是**显式**的声明，
		// 而不是「没有价格所以放行」。
		return ReservationQuote{AmountMinor: 0, Ok: true}
	}
	if maxOutputTokens <= 0 {
		return ReservationQuote{Ok: false, Reason: "生成配置没有输出上限（maxTokens），无法界定单次调用风险；补齐上限后再执行"}
	}
	if estimatedInputTokens < 0 {
		estimatedInputTokens = 0
	}
	inputPart, ok := perMillionCost(estimatedInputTokens, price.InputPerMillion)
	if !ok {
		return ReservationQuote{Ok: false, Reason: "输入上界超出可计算范围，无法预留预算"}
	}
	outputPart, ok := perMillionCost(maxOutputTokens, price.OutputPerMillion)
	if !ok {
		return ReservationQuote{Ok: false, Reason: "输出上限超出可计算范围，无法预留预算"}
	}
	if inputPart > math.MaxInt64-outputPart {
		return ReservationQuote{Ok: false, Reason: "预留金额溢出，无法预留预算"}
	}
	return ReservationQuote{AmountMinor: inputPart + outputPart, Ok: true}
}

// EffectiveLimitMinor 取「项目上限」与「批次上限」中更严格的那个。
//
// 0 表示未设上限，因此不能被当成「最严格」。两者都未设时返回 0。
// 抽成函数而不是各处写 if：这个规则一旦在两处写得不一样，
// 「批次显示还有额度但项目已经拦住」这类矛盾就会长期存在且难以复现。
func EffectiveLimitMinor(projectLimit, batchLimit int64) int64 {
	switch {
	case projectLimit <= 0 && batchLimit <= 0:
		return 0
	case projectLimit <= 0:
		return batchLimit
	case batchLimit <= 0:
		return projectLimit
	case projectLimit < batchLimit:
		return projectLimit
	default:
		return batchLimit
	}
}

// BudgetSnapshot 是一个预算台账的当前状态（§2.4 四态的聚合视图）。
type BudgetSnapshot struct {
	ProjectID      int64  `json:"projectId"`
	Currency       string `json:"currency"`
	LimitMinor     int64  `json:"limitMinor"`
	ReservedMinor  int64  `json:"reservedMinor"`
	SettledMinor   int64  `json:"settledMinor"`
	UncertainMinor int64  `json:"uncertainMinor"`
	Version        int64  `json:"version"`
}

// SpentMinor 是「已经花掉或可能已经花掉」的总额。
//
// 刻意把 uncertain 计入：不确定的费用不能从「已用」里排除，
// 否则用户会看到「还剩很多额度」却已经被供应商扣款。
func (s BudgetSnapshot) SpentMinor() int64 {
	return s.SettledMinor + s.UncertainMinor
}

// CommittedMinor 是「已占用」的总额：在途预留 + 已花。判定上限时用它。
func (s BudgetSnapshot) CommittedMinor() int64 {
	return s.ReservedMinor + s.SettledMinor + s.UncertainMinor
}

// RemainingMinor 返回剩余可预留额度；未设上限时返回 (0,false)，0 不是「没有额度」。
func (s BudgetSnapshot) RemainingMinor() (int64, bool) {
	if s.LimitMinor <= 0 {
		return 0, false
	}
	remaining := s.LimitMinor - s.CommittedMinor()
	if remaining < 0 {
		remaining = 0
	}
	return remaining, true
}

// Exhausted 判断预留是否已经耗尽上限。
func (s BudgetSnapshot) Exhausted() bool {
	if s.LimitMinor <= 0 {
		return false
	}
	return s.CommittedMinor() >= s.LimitMinor
}

// UsageLedger 是一条用量流水。
type UsageLedger struct {
	ID        int64  `json:"id"`
	ProjectID int64  `json:"projectId"`
	BatchID   *int64 `json:"batchId,omitempty"`
	JobID     *int64 `json:"jobId,omitempty"`
	Attempt   int    `json:"attempt"`
	Purpose   string `json:"purpose"`

	IdempotencyKey string `json:"idempotencyKey"`
	RequestID      string `json:"requestId"`

	ConnectionID        *int64 `json:"connectionId,omitempty"`
	EndpointFingerprint string `json:"endpointFingerprint"`
	ModelName           string `json:"modelName"`
	ResponseModelID     string `json:"responseModelId"`
	ConfigFingerprint   string `json:"configFingerprint"`

	Currency       string `json:"currency"`
	PriceVersionID *int64 `json:"priceVersionId,omitempty"`
	PriceVersion   string `json:"priceVersion"`

	InputTokens  *int64 `json:"inputTokens,omitempty"`
	OutputTokens *int64 `json:"outputTokens,omitempty"`
	UsageSource  string `json:"usageSource"`

	AmountState    string `json:"amountState"`
	ReservedMinor  int64  `json:"reservedMinor"`
	EstimatedMinor int64  `json:"estimatedMinor"`
	ActualMinor    *int64 `json:"actualMinor"`

	State              string `json:"state"`
	Reconciliation     string `json:"reconciliation"`
	ReconciliationNote string `json:"reconciliationNote"`
	ErrorClass         string `json:"errorClass"`

	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// ChargeMinor 返回这条账目对预算的占用金额（结算后）。
//
// 规则与 internal/store 的计数器更新一致，抽成方法是为了让
// 「界面显示的成本」和「预算扣减的成本」永远不会用两套逻辑：
//   - actual → 实际金额；
//   - estimated → 估计金额；
//   - unknown → 预留金额（我们愿意为这次请求付出的上界，不是 0）。
func (u UsageLedger) ChargeMinor() int64 {
	switch u.AmountState {
	case AmountStateActual, AmountStateEstimated:
		if u.ActualMinor != nil {
			return *u.ActualMinor
		}
		if u.AmountState == AmountStateEstimated {
			return u.EstimatedMinor
		}
		return u.ReservedMinor
	case AmountStateUnknown:
		return u.ReservedMinor
	default:
		return u.ReservedMinor
	}
}

// ModelCapabilities 是一个连接+模型的能力声明（§5）。
//
// MaxOutputTokens/MaxContextTokens 为 0 表示**未声明**（未知），
// 不是「不支持」—— 两者在界面上的处置不同（未知提示补齐声明，不支持提示换模型）。
type ModelCapabilities struct {
	ConnectionID             int64  `json:"connectionId"`
	ModelName                string `json:"modelName"`
	EndpointFP               string `json:"endpointFingerprint"`
	SupportsTemperature      bool   `json:"supportsTemperature"`
	SupportsReasoningEffort  bool   `json:"supportsReasoningEffort"`
	SupportsStructuredOutput bool   `json:"supportsStructuredOutput"`
	SupportsJSONMode         bool   `json:"supportsJsonMode"`
	MaxOutputTokens          int    `json:"maxOutputTokens"`
	MaxContextTokens         int    `json:"maxContextTokens"`
	DeclaredBy               string `json:"declaredBy"`
	// Source 说明这份能力声明来自哪里：`declaration`（数据库里管理员声明）
	// 或 `builtin-default`（按供应商类型/模型名的保守回退）。
	// 界面必须能区分：前者可信，后者只是「不至于直接失败」的猜测。
	Source string `json:"source"`
}

// Capability source 取值。
const (
	CapabilitySourceDeclaration = "declaration"
	CapabilitySourceBuiltin     = "builtin-default"
)

// ModelRequestSpec 是一次外部请求中与「能力」相关的参数。
type ModelRequestSpec struct {
	EndpointURL     string   `json:"endpointUrl"`
	ModelName       string   `json:"modelName"`
	Temperature     *float64 `json:"temperature,omitempty"`
	MaxOutputTokens int      `json:"maxOutputTokens"`
	ReasoningEffort string   `json:"reasoningEffort"`
	// StructuredOutput 表示请求要求严格结构化输出（response_format=json_schema）。
	StructuredOutput bool `json:"structuredOutput"`
	// JSONMode 表示请求只要求 JSON 模式（response_format=json_object）。
	JSONMode bool `json:"jsonMode"`
	// SchemaVersion 是请求期望的输出 schema 版本（例如 sft.sample.v1）。
	// 它不直接影响供应商行为，但属于「配置指纹」的一部分：同一连接同一模型下
	// 换了 schema 就是不同的产出规格，成本对比必须能区分。
	SchemaVersion string `json:"schemaVersion"`
}

// ValidateAgainstCapabilities 在**提交前**校验请求与能力声明是否一致。
//
// 为什么必须在提交前校验（而不是等供应商返回 400）：一次被供应商拒绝的请求
// 仍然可能产生费用（部分供应商对失败请求计费），而且错误信息是英文的
// 供应商错误，用户无法据此行动。能力校验把「一定会失败」的请求拦在花钱之前。
//
// 返回的是 FieldErrors（面向用户的中文字段错误），与项目/文档校验同一形态，
// 使 API 层能用同一套 422 响应渲染。
func (c ModelCapabilities) ValidateAgainstCapabilities(spec ModelRequestSpec) FieldErrors {
	var errs FieldErrors

	if strings.TrimSpace(spec.ModelName) == "" {
		errs = append(errs, FieldError{Field: "model.name", Message: "模型名必填"})
	} else if c.ModelName != "" && !strings.EqualFold(strings.TrimSpace(spec.ModelName), strings.TrimSpace(c.ModelName)) {
		// 模型名不一致说明调用方拿错了能力声明 —— 用错声明校验比不校验更危险，
		// 因为它会给出「校验通过」的假保证。
		errs = append(errs, FieldError{
			Field:   "model.name",
			Message: fmt.Sprintf("能力声明的模型是 %s，与请求的 %s 不一致", c.ModelName, spec.ModelName),
		})
	}

	if spec.Temperature != nil && !c.SupportsTemperature {
		errs = append(errs, FieldError{
			Field:   "generation.temperature",
			Message: "该模型不支持 temperature（推理型模型会拒绝它），请移除该参数或改为按能力声明的默认值",
		})
	}
	if spec.ReasoningEffort != "" && !c.SupportsReasoningEffort {
		errs = append(errs, FieldError{
			Field:   "generation.reasoningEffort",
			Message: "该模型不支持 reasoning_effort，请移除该参数或更换模型",
		})
	}
	if spec.MaxOutputTokens < 0 {
		errs = append(errs, FieldError{Field: "generation.maxTokens", Message: "输出上限不能为负数"})
	} else if spec.MaxOutputTokens > 0 && c.MaxOutputTokens > 0 && spec.MaxOutputTokens > c.MaxOutputTokens {
		errs = append(errs, FieldError{
			Field:   "generation.maxTokens",
			Message: fmt.Sprintf("输出上限 %d 超过该模型的声明上限 %d", spec.MaxOutputTokens, c.MaxOutputTokens),
		})
	}
	if spec.StructuredOutput && !c.SupportsStructuredOutput {
		errs = append(errs, FieldError{
			Field:   "generation.schemaVersion",
			Message: "该模型不支持严格结构化输出（json_schema）；请改用提示词约束 JSON，或更换模型",
		})
	}
	if spec.JSONMode && !c.SupportsJSONMode && !c.SupportsStructuredOutput {
		errs = append(errs, FieldError{
			Field:   "generation.schemaVersion",
			Message: "该模型不支持 JSON 模式；请改用提示词约束 JSON，或更换模型",
		})
	}

	return errs
}

// ValidateMaxTokensWithinCapability 只校验输出上限，供「预览节点配置」这类
// 不需要完整校验的场景使用（T11 的节点检查器在保存前给出即时反馈）。
func (c ModelCapabilities) ValidateMaxTokensWithinCapability(maxOutputTokens int) FieldErrors {
	return c.ValidateAgainstCapabilities(ModelRequestSpec{
		ModelName:       c.ModelName,
		MaxOutputTokens: maxOutputTokens,
	})
}

// EndpointFingerprint 生成接入点的指纹。
//
// 保留 scheme/host/port + 路径，去掉查询串与末尾斜杠：
//   - 查询串常带 api-version 之类的参数，它属于**配置指纹**而不是接入点身份，
//     混在一起会让「同一个接入点换了 api-version」看起来像换了供应商；
//   - 去重末尾斜杠让 `https://x/v1` 与 `https://x/v1/` 不会被当成两个接入点。
//
// 不包含任何凭证：指纹会进账目与快照（§2.4「不把明文密钥冻结进快照」）。
func EndpointFingerprint(baseURL string) string {
	trimmed := strings.TrimSpace(baseURL)
	if trimmed == "" {
		return ""
	}
	// 去掉 scheme，只留 host + path：scheme 变化（http/https）不影响计费口径，
	// 而保留它只会让同一个接入点在账上出现两个身份。
	withoutScheme := trimmed
	if index := strings.Index(withoutScheme, "://"); index >= 0 {
		withoutScheme = withoutScheme[index+3:]
	}
	if index := strings.IndexAny(withoutScheme, "?#"); index >= 0 {
		withoutScheme = withoutScheme[:index]
	}
	withoutScheme = strings.TrimRight(withoutScheme, "/")
	return strings.ToLower(withoutScheme)
}

// FormatMinor 把整数分值格式化为可读金额，例如 1234 -> "12.34 元"。
//
// 负数按「欠费」语义保留符号。币种非 CNY 时用代码前缀而不是猜测符号：
// 猜测符号会让一个 JPY 金额看起来像人民币。
func FormatMinor(minor int64, currency string) string {
	sign := ""
	value := minor
	if value < 0 {
		sign = "-"
		value = -value
	}
	whole := value / 100
	fraction := value % 100
	text := fmt.Sprintf("%s%d.%02d", sign, whole, fraction)
	if currency == "" || currency == "CNY" {
		return text + " 元"
	}
	return text + " " + currency
}
