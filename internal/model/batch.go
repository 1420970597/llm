package model

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// 本文件定义 Atelier 批次、单元与样本版本对象（Issue #160 T05）。
//
// 契约：docs/plans/atelier-implementation.md §4.1、§4.2、§2.6；
// docs/plans/atelier-api-contract.md §2.3、§2.4、§3.1。
//
// 核心设计（#160 §1 的根因修复）：
//   现有 sft_records/grpo_prompts 以 (dataset_id, question_id) upsert，
//   重跑会覆盖历史。新路径把**身份**（Sample）与**内容**（SampleVersion）分开：
//   身份稳定、内容只追加。因此「同题不同批次的输出」与「原样本重生成」
//   都能并存且可追溯。

// 批次用途。
const (
	BatchPurposePilot = "pilot"
	BatchPurposeScale = "scale"
)

// 批次状态（§2.6）。partial_failed 与 failed 区分：
// 前者可幂等恢复失败项，后者是致命错误（继续重试没有意义）。
const (
	BatchStatusQueued         = "queued"
	BatchStatusRunning        = "running"
	BatchStatusPauseRequested = "pause_requested"
	BatchStatusPaused         = "paused"
	BatchStatusPartialFailed  = "partial_failed"
	BatchStatusCompleted      = "completed"
	BatchStatusFailed         = "failed"
)

// 批次控制状态。与 status 分离：合并成一个字段就无法表达
// 「已请求暂停，但在途请求还在跑」这一真实状态（#159 反复强调
// 「暂停不撤回在途请求」）。
const (
	BatchControlRun            = "run"
	BatchControlPauseRequested = "pause_requested"
	BatchControlPaused         = "paused"
)

// 批次阶段状态。
const (
	StepStatusPending       = "pending"
	StepStatusRunning       = "running"
	StepStatusPaused        = "paused"
	StepStatusPartialFailed = "partial_failed"
	StepStatusCompleted     = "completed"
	StepStatusFailed        = "failed"
	StepStatusSkipped       = "skipped"
)

// 单元状态。
const (
	ItemStatusPending   = "pending"
	ItemStatusRunning   = "running"
	ItemStatusSucceeded = "succeeded"
	ItemStatusFailed    = "failed"
	ItemStatusSkipped   = "skipped"
)

// 错误类别。界面按它给出**可操作**建议，而不是只展示一段英文。
//
// 这些取值来自真实失败形态（T12 的验收项要求可控 provider 覆盖
// 成功/空输出/截断/无效 JSON/429/超时/部分失败）。
const (
	ErrorClassProvider    = "provider_error"
	ErrorClassRateLimited = "rate_limited"
	ErrorClassTimeout     = "timeout"
	ErrorClassEmptyOutput = "empty_output"
	ErrorClassTruncated   = "truncated"
	ErrorClassInvalidJSON = "invalid_json"
	ErrorClassSchema      = "schema_violation"
	ErrorClassConfig      = "config_error"
	ErrorClassInternal    = "internal_error"
)

// ErrorClassAction 给出错误类别的**可操作**建议。
//
// 契约与 #159 都要求「失败有可操作原因」，而不是让用户去猜。
// 因此这里返回中文建议：用户能据此判断「重试」「改配置」还是「找管理员」。
func ErrorClassAction(errorClass string) string {
	switch errorClass {
	case ErrorClassRateLimited:
		return "供应商限流，稍后恢复失败项即可；降低并发可以减少触发"
	case ErrorClassTimeout:
		return "请求超时，费用可能已产生（记为未知）；可直接恢复失败项重试"
	case ErrorClassTruncated:
		return "输出被截断，请提高输出上限或简化提示词后新建批次"
	case ErrorClassEmptyOutput:
		return "模型返回空内容，请检查提示词或更换模型后新建批次"
	case ErrorClassInvalidJSON:
		return "模型返回的不是合法 JSON，请检查输出 schema 或降低温度后新建批次"
	case ErrorClassSchema:
		return "内容结构不符合要求，请检查输出 schema 与标准步骤"
	case ErrorClassConfig:
		return "生成配置有问题，请打开项目设计页的“生成”节点选择模型连接并保存新蓝图后，再新建批次"
	case ErrorClassProvider:
		return "供应商返回错误，恢复失败项可重试；持续失败请联系管理员检查连接"
	case ErrorClassInternal:
		return "系统内部错误，恢复失败项可重试；持续失败请联系管理员"
	default:
		return "恢复失败项可重试；若持续失败请联系管理员"
	}
}

// BatchGenerationConfig 是批次内联的生成配置快照。
//
// 为什么要内联而不是只引用蓝图版本：「这一批用了什么并发与输出上限」
// 属于**运行事实**。蓝图后续可以改，而批次必须能解释自己当时是怎么跑的。
//
// ModelConnectionID 是**非秘密标识**：契约 §2.4 明确凭证单独取，
// 不把明文密钥冻结进快照。
type BatchGenerationConfig struct {
	ModelConnectionID int64    `json:"modelConnectionId"`
	ModelVersion      string   `json:"modelVersion"`
	Concurrency       int      `json:"concurrency"`
	MaxTokens         int      `json:"maxTokens"`
	Temperature       *float64 `json:"temperature,omitempty"`
	SchemaVersion     string   `json:"schemaVersion"`
}

// BatchSnapshot 是批次的输入快照（版本行 ID + 内容 hash + 展示用版本号）。
//
// 用**版本行 ID** 而不是版本号：版本号只在 (document, logicalId) 内唯一，
// 而快照需要跨 logicalId 的全局身份。
type BatchSnapshot struct {
	BlueprintVersionID       int64  `json:"blueprintVersionId"`
	BlueprintContentHash     string `json:"blueprintContentHash"`
	CoverageVersionID        int64  `json:"coverageVersionId"`
	CoverageContentHash      string `json:"coverageContentHash"`
	StandardVersionID        int64  `json:"standardVersionId"`
	StandardContentHash      string `json:"standardContentHash"`
	QualityPolicyVersionID   int64  `json:"qualityPolicyVersionId"`
	QualityPolicyContentHash string `json:"qualityPolicyContentHash"`
	MappingVersionID         int64  `json:"mappingVersionId"`
	MappingContentHash       string `json:"mappingContentHash"`
}

// Batch 是一次试制或扩量。
type Batch struct {
	ID            int64  `json:"id"`
	ProjectID     int64  `json:"projectId"`
	Purpose       string `json:"purpose"`
	Status        string `json:"status"`
	ControlState  string `json:"controlState"`
	TargetKind    string `json:"targetKind"`
	SchemaVersion string `json:"schemaVersion"`

	Snapshot         BatchSnapshot         `json:"snapshot"`
	GenerationConfig BatchGenerationConfig `json:"generationConfig"`

	PlannedUnits   int `json:"plannedUnits"`
	CompletedUnits int `json:"completedUnits"`
	FailedUnits    int `json:"failedUnits"`
	// InFlightUnits 是「已提交给供应商、尚未确认」的数量。
	// 暂停时界面必须显示它：停止新请求不等于立刻停费（§2.4）。
	InFlightUnits int `json:"inFlightUnits"`

	Budget BudgetPolicy `json:"budget"`
	// 批次级预算计数器（Issue #160 T07）。
	//
	// 与 BudgetPolicy 分开：BudgetPolicy 是「上限」（配置），
	// 这三个是「已经占用多少」（事实）。放在同一层是因为
	// 「本批还剩多少」必须能一次读出来，而不需要 JOIN usage_ledger 聚合 ——
	// 10 万单元的批次下每次预留都聚合一遍是 O(n²)。
	//
	// 三者的语义与 §2.4 的四态对应，详见 budget_reservations 的表注释。
	BudgetReservedMinor  int64 `json:"budgetReservedMinor"`
	BudgetSettledMinor   int64 `json:"budgetSettledMinor"`
	BudgetUncertainMinor int64 `json:"budgetUncertainMinor"`

	CoverageSlice json.RawMessage `json:"coverageSlice,omitempty"`
	FencingToken  int64           `json:"fencingToken"`
	LeaseOwner    string          `json:"leaseOwner,omitempty"`
	LeaseUntil    *time.Time      `json:"leaseUntil,omitempty"`

	CreatedBy  *int64     `json:"createdBy,omitempty"`
	StartedAt  *time.Time `json:"startedAt,omitempty"`
	FinishedAt *time.Time `json:"finishedAt,omitempty"`
	CreatedAt  time.Time  `json:"createdAt"`
	UpdatedAt  time.Time  `json:"updatedAt"`
}

// BatchShortfall 返回「计划量 − 完成量」的缺口。
//
// issue #190：这个数字必须由服务端给出，而不是让前端拿两个字段相减 ——
// 界面上少一处算术，就少一处口径漂移。
func (batch Batch) Shortfall() int {
	shortfall := batch.PlannedUnits - batch.CompletedUnits
	if shortfall < 0 {
		return 0
	}
	return shortfall
}

// ShortfallNote 给出缺口的中文原因（无缺口时为空串）。
//
// 只有**已定稿**的批次才允许宣称缺口：把还在跑的批次标成「缺口」会让用户
// 以为已经跑完了。
func (batch Batch) ShortfallNote() string {
	shortfall := batch.Shortfall()
	if shortfall == 0 {
		return ""
	}
	terminal := batch.Status == BatchStatusCompleted || batch.Status == BatchStatusFailed ||
		batch.Status == BatchStatusPartialFailed
	if !terminal {
		return ""
	}
	return fmt.Sprintf("计划 %d，实际产出 %d，缺口 %d：覆盖率不足或无素材接地，请补充方向配额/素材后重跑",
		batch.PlannedUnits, batch.CompletedUnits, shortfall)
}

// BatchStep 是一个阶段的进度。
type BatchStep struct {
	ID           int64      `json:"id"`
	BatchID      int64      `json:"batchId"`
	Phase        string     `json:"phase"`
	UnitLabel    string     `json:"unitLabel"`
	Status       string     `json:"status"`
	TotalUnits   int        `json:"totalUnits"`
	DoneUnits    int        `json:"doneUnits"`
	FailedUnits  int        `json:"failedUnits"`
	ErrorSummary string     `json:"errorSummary"`
	StartedAt    *time.Time `json:"startedAt,omitempty"`
	FinishedAt   *time.Time `json:"finishedAt,omitempty"`
	CreatedAt    time.Time  `json:"createdAt"`
	UpdatedAt    time.Time  `json:"updatedAt"`
}

// Sample 是样本**身份**（稳定、不含内容）。
type Sample struct {
	ID            int64     `json:"id"`
	ProjectID     int64     `json:"projectId"`
	SampleKey     string    `json:"sampleKey"`
	TargetKind    string    `json:"targetKind"`
	Title         string    `json:"title"`
	OriginBatchID *int64    `json:"originBatchId,omitempty"`
	LatestVersion int       `json:"latestVersion"`
	CreatedAt     time.Time `json:"createdAt"`
	UpdatedAt     time.Time `json:"updatedAt"`
}

// SampleVersion 是**不可变内容版本**。
//
// 「不可变」是契约而不是约定：没有 UPDATE 路径，判断/规则/发布都不得改写它
// （§4.1 与 T05 验收项「内容/来源不随旧表更新」）。
type SampleVersion struct {
	ID            int64           `json:"id"`
	SampleID      int64           `json:"sampleId"`
	ProjectID     int64           `json:"projectId"`
	Version       int             `json:"version"`
	TargetKind    string          `json:"targetKind"`
	SchemaVersion string          `json:"schemaVersion"`
	Payload       json.RawMessage `json:"payload"`
	ContentHash   string          `json:"contentHash"`

	BatchID         *int64          `json:"batchId,omitempty"`
	BatchItemID     *int64          `json:"batchItemId,omitempty"`
	Attempt         int             `json:"attempt"`
	GeneratorConfig json.RawMessage `json:"generatorConfig,omitempty"`

	StandardVersionID    *int64 `json:"standardVersionId,omitempty"`
	StandardContentHash  string `json:"standardContentHash"`
	BlueprintVersionID   *int64 `json:"blueprintVersionId,omitempty"`
	BlueprintContentHash string `json:"blueprintContentHash"`

	CreatedBy *int64    `json:"createdBy,omitempty"`
	CreatedAt time.Time `json:"createdAt"`
}

// BatchItem 是单元级执行事实。
type BatchItem struct {
	ID              int64      `json:"id"`
	BatchID         int64      `json:"batchId"`
	ProjectID       int64      `json:"projectId"`
	ItemKey         string     `json:"itemKey"`
	SampleID        *int64     `json:"sampleId,omitempty"`
	Status          string     `json:"status"`
	Attempt         int        `json:"attempt"`
	ErrorClass      string     `json:"errorClass"`
	ErrorMessage    string     `json:"errorMessage"`
	Retryable       bool       `json:"retryable"`
	SampleVersionID *int64     `json:"sampleVersionId,omitempty"`
	StartedAt       *time.Time `json:"startedAt,omitempty"`
	FinishedAt      *time.Time `json:"finishedAt,omitempty"`
	CreatedAt       time.Time  `json:"createdAt"`
	UpdatedAt       time.Time  `json:"updatedAt"`
}

// BatchEvent 是事件时间线的一项。
type BatchEvent struct {
	ID        int64           `json:"id"`
	BatchID   int64           `json:"batchId"`
	ProjectID int64           `json:"projectId"`
	EventType string          `json:"eventType"`
	Sequence  int             `json:"sequence"`
	ActorID   *int64          `json:"actorId,omitempty"`
	Detail    json.RawMessage `json:"detail,omitempty"`
	CreatedAt time.Time       `json:"createdAt"`
}

// 事件类型。载荷只含对象 ID 与版本，不把大段样本内容塞进消息（契约 §5）。
const (
	BatchEventQueued         = "BatchQueued"
	BatchEventStarted        = "BatchStarted"
	BatchEventPaused         = "BatchPaused"
	BatchEventResumed        = "BatchResumed"
	BatchEventStepCompleted  = "BatchStepCompleted"
	BatchEventPartialFailed  = "BatchPartialFailed"
	BatchEventCompleted      = "BatchCompleted"
	BatchEventFailed         = "BatchFailed"
	BatchEventRetryRequested = "BatchRetryFailedRequested"
)

// 规模上限。契约 §2.3：pilot 1–100，scale 1–100000。
const (
	MinPilotUnits = 1
	MaxPilotUnits = 100
	MinScaleUnits = 1
	MaxScaleUnits = 100000
)

// CreateBatchInput 是创建批次的请求体（契约 §2.3）。
type CreateBatchInput struct {
	Purpose string `json:"purpose"`
	// 版本引用。T05 允许只给 blueprint（覆盖/标准从蓝图节点推导），
	// 也允许显式给出以支持「同蓝图不同切片」的扩量。
	BlueprintVersionID     int64 `json:"blueprintVersionId"`
	CoverageVersionID      int64 `json:"coverageVersionId"`
	StandardVersionID      int64 `json:"standardVersionId"`
	QualityPolicyVersionID int64 `json:"qualityPolicyVersionId"`
	MappingVersionID       int64 `json:"mappingVersionId"`
	// UnitCount：pilot 1–100；scale 1–100000。
	UnitCount int `json:"unitCount"`
	Budget    struct {
		Currency   string `json:"currency"`
		LimitMinor *int64 `json:"limitMinor"`
	} `json:"budget"`
	// CoverageSlice 可选：只跑某个切片（T13 的扩量范围）。
	CoverageSlice json.RawMessage `json:"coverageSlice,omitempty"`
	// PlannedItemKeys 是服务端算出的计划单元键。
	// 它不由客户端提供 —— 但那属于 T12/T13 的覆盖分配逻辑，
	// 因此这里留空表示「由 worker 在执行时按快照分配」。
	PlannedItemKeys []string `json:"-"`
}

// Normalize 填默认值（只处理「未提供」，不掩盖「提供了非法值」）。
func (input *CreateBatchInput) Normalize() {
	input.Purpose = strings.ToLower(strings.TrimSpace(input.Purpose))
	if input.Purpose == "" {
		input.Purpose = BatchPurposePilot
	}
	if input.Budget.Currency == "" {
		input.Budget.Currency = "CNY"
	}
}

// UnitLimitForPurpose 返回该用途的单元数上限。
func UnitLimitForPurpose(purpose string) int {
	if purpose == BatchPurposeScale {
		return MaxScaleUnits
	}
	return MaxPilotUnits
}

// Validate 校验创建批次的请求。
//
// 契约 §2.3 的约束：purpose 必须是 pilot/scale，unitCount 在范围内。
// 「至少引用了哪个版本」的约束刻意不在这里：不同 purpose 的最小引用集不同
// （试制可能只给蓝图，扩量必须给全部），而那属于 T13 的执行前检查。
// 这里只拒绝**明显不可能成立**的取值，避免把阶段依赖做成死锁。
func (input CreateBatchInput) Validate() error {
	var errs FieldErrors

	if input.Purpose != BatchPurposePilot && input.Purpose != BatchPurposeScale {
		errs = append(errs, FieldError{Field: "purpose", Message: "只能是 pilot 或 scale"})
	}
	limit := UnitLimitForPurpose(input.Purpose)
	minUnits := MinPilotUnits
	if input.Purpose == BatchPurposeScale {
		minUnits = MinScaleUnits
	}
	if input.UnitCount < minUnits || input.UnitCount > limit {
		errs = append(errs, FieldError{
			Field:   "unitCount",
			Message: fmt.Sprintf("必须在 %d–%d 之间", minUnits, limit),
		})
	}
	if limit := input.Budget.LimitMinor; limit != nil && *limit != 0 && *limit < 100 {
		errs = append(errs, FieldError{Field: "budget.limitMinor", Message: "至少为 100（1 元，单位为分）"})
	}
	if input.Budget.Currency != "" && input.Budget.Currency != "CNY" {
		errs = append(errs, FieldError{Field: "budget.currency", Message: "当前只支持 CNY"})
	}
	if input.UnitCount > 0 && input.UnitCount != 0 && len(input.CoverageSlice) > 0 && !json.Valid(input.CoverageSlice) {
		errs = append(errs, FieldError{Field: "coverageSlice", Message: "格式不正确"})
	}

	if len(errs) > 0 {
		return errs
	}
	return nil
}

// BudgetLimitValue 返回落库的预算上限（未提供时 0 = 未设上限）。
func (input CreateBatchInput) BudgetLimitValue() int64 {
	if input.Budget.LimitMinor != nil {
		return *input.Budget.LimitMinor
	}
	return 0
}

// BatchCapabilities 按批次状态与角色派生能力位（契约 §4）。
//
// 每条都由服务端在命令里重新校验；这里是**呈现建议**，
// 前端据它显示/隐藏按钮，但禁用按钮不构成安全边界。
func BatchCapabilities(role, status, controlState string) Capabilities {
	// 终态不可再暂停/恢复：那会让「已完成」的批次回到运行态，
	// 从而重跑已经成功的单元（浪费预算且可能与已发布内容不一致）。
	terminal := status == BatchStatusCompleted || status == BatchStatusFailed

	canControl := role == ProjectRoleOwner && !terminal
	return Capabilities{
		CanEdit:     false,
		CanRun:      canControl,
		CanReview:   false,
		CanPublish:  false,
		CanDownload: true,
	}
}

// BatchProgress 是按单位分别统计的进度（§2.6 与 issue #141 的教训）。
//
// 为什么必须分列而不是一个百分比：用「题数」冒充「方向数」会让用户
// 看到 100% 而实际只跑完方向。界面按 unitLabel 分别展示。
type BatchProgress struct {
	UnitLabel      string      `json:"unitLabel"`
	PlannedUnits   int         `json:"plannedUnits"`
	CompletedUnits int         `json:"completedUnits"`
	FailedUnits    int         `json:"failedUnits"`
	InFlightUnits  int         `json:"inFlightUnits"`
	Steps          []BatchStep `json:"steps"`
	// CompletionPercent 只在该批次**只有一个阶段**时才有意义，
	// 因此它是 *int：多阶段时返回 null，让界面显示各阶段分列，
	// 而不是编造一个「总体百分比」。
	CompletionPercent *int `json:"completionPercent"`
}

// ComputeBatchProgress 组装进度读模型。
//
// 关键取舍：多阶段时 **CompletionPercent 为 nil**（前端显示「各阶段进度」），
// 而不是取平均或取最大。平均值会让「方向跑完、题目开始」显示成 55%，
// 而这不是任何一个真实单位的完成度 —— 那正是 #141 修掉的那类错误。
func ComputeBatchProgress(batch Batch, steps []BatchStep) BatchProgress {
	progress := BatchProgress{
		UnitLabel:      "单元",
		PlannedUnits:   batch.PlannedUnits,
		CompletedUnits: batch.CompletedUnits,
		FailedUnits:    batch.FailedUnits,
		InFlightUnits:  batch.InFlightUnits,
		Steps:          steps,
	}
	if len(steps) == 1 && steps[0].TotalUnits > 0 {
		percent := steps[0].DoneUnits * 100 / steps[0].TotalUnits
		progress.UnitLabel = steps[0].UnitLabel
		progress.CompletionPercent = &percent
	}
	return progress
}
