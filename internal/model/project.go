package model

import (
	"fmt"
	"strings"
	"time"
)

// 本文件定义 Atelier 主线的项目与工作区对象（Issue #160 T02）。
//
// 契约：docs/plans/atelier-implementation.md §4.1、docs/plans/atelier-api-contract.md §2.1。
//
// 与 dataset 的关系：`datasets` 同时承担目标、运行配置、运行状态与导出源四个角色，
// 于是「同一主题跑两次不同方案」只能覆盖旧数据。Project 只表达**用户目标与预算**，
// 运行由 Batch、内容由 SampleVersion、交付由 Release 承担。两者并存而非改名。

// 项目 target_kind：决定样本 payload schema 与可用节点（§2.2）。
const (
	TargetKindSFT  = "sft"
	TargetKindGRPO = "grpo"
)

// 项目状态机（§4.2）。archived 只阻止新运行，不删批次或发布。
const (
	ProjectStatusDraft        = "draft"
	ProjectStatusDesigned     = "designed"
	ProjectStatusPilotRunning = "pilot_running"
	ProjectStatusPilotReady   = "pilot_ready"
	ProjectStatusScaling      = "scaling"
	ProjectStatusReview       = "review"
	ProjectStatusCandidate    = "candidate"
	ProjectStatusPublished    = "published"
	ProjectStatusArchived     = "archived"
)

// 项目角色（契约 §1.6）。workspace admin 不在这个集合里：
// 它管成员与连接，**不默认拥有**项目内容读权。
const (
	ProjectRoleOwner    = "owner"
	ProjectRoleReviewer = "reviewer"
	ProjectRoleViewer   = "viewer"
)

// 工作区角色。与项目角色分表：作用域不同，合并容易写出越权。
const (
	WorkspaceRoleAdmin  = "admin"
	WorkspaceRoleMember = "member"
)

// 预算策略。金额一律整数最小货币单位（分），禁止浮点（§2.4）。
const (
	BudgetOnExhaustedPause = "pause"
	BudgetOnExhaustedStop  = "stop"
)

// 项目目标量上限。pilotSize 的上限来自契约 §2.1；扩量上限（1–100000）属于 T13，
// 这里只约束创建时的试制量。
const (
	MinPilotSize = 1
	MaxPilotSize = 100
)

// FieldError 是单个字段的校验失败，直接面向用户（中文）。
type FieldError struct {
	Field   string `json:"field"`
	Message string `json:"message"`
}

// FieldErrors 是「多个字段同时不合法」的校验结果。
//
// 为什么不是遇到第一个错误就返回：向导表单一次提交多个字段，
// 只报第一个会让用户来回提交三、四次（W03–W05 的验收项要求「聚焦首个无效字段」
// 而不是「只暴露一个字段」）。
type FieldErrors []FieldError

func (e FieldErrors) Error() string {
	if len(e) == 0 {
		return "请求参数有误"
	}
	parts := make([]string, 0, len(e))
	for _, item := range e {
		parts = append(parts, item.Field+item.Message)
	}
	return strings.Join(parts, "；")
}

// HasFieldErrors 判断错误是否携带字段级校验结果。
func HasFieldErrors(err error) (FieldErrors, bool) {
	fieldErrors, ok := err.(FieldErrors)
	return fieldErrors, ok
}

// BudgetPolicy 是项目与批次的预算上限。
//
// 币种显式存入而不是隐式假设：一个项目可能来自使用其它币种的工作区，
// 隐式默认会让「未知费用」这类边界问题退化成一个不该有的浮点汇率换算。
type BudgetPolicy struct {
	Currency   string `json:"currency"`
	LimitMinor int64  `json:"limitMinor"`
	// OnExhausted 是预算耗尽后的行为："pause"（暂停新请求）或 "stop"。
	OnExhausted string `json:"onExhausted"`
}

// Workspace 是成员与连接的治理作用域。
type Workspace struct {
	ID        int64     `json:"id"`
	Name      string    `json:"name"`
	Slug      string    `json:"slug"`
	CreatedBy *int64    `json:"createdBy,omitempty"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// WorkspaceMember 是工作区成员（治理角色）。
type WorkspaceMember struct {
	WorkspaceID int64     `json:"workspaceId"`
	UserID      int64     `json:"userId"`
	Email       string    `json:"email"`
	Role        string    `json:"role"`
	CreatedAt   time.Time `json:"createdAt"`
}

// ProjectMember 是项目成员（内容角色）。
type ProjectMember struct {
	ProjectID int64     `json:"projectId"`
	UserID    int64     `json:"userId"`
	Email     string    `json:"email"`
	Role      string    `json:"role"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// Project 是用户目标与预算的作用域。
type Project struct {
	ID                    int64        `json:"id"`
	WorkspaceID           int64        `json:"workspaceId"`
	Name                  string       `json:"name"`
	Goal                  string       `json:"goal"`
	TargetKind            string       `json:"targetKind"`
	Status                string       `json:"status"`
	OwnerID               int64        `json:"ownerId"`
	DomainCount           int          `json:"domainCount"`
	DirectionsPerDomain   int          `json:"directionsPerDomain"`
	QuestionsPerDirection int          `json:"questionsPerDirection"`
	PilotSize             int          `json:"pilotSize"`
	AcceptanceRateTarget  float64      `json:"acceptanceRateTarget"`
	Budget                BudgetPolicy `json:"budget"`
	RowVersion            int64        `json:"rowVersion"`
	// LegacyDatasetID 指向迁移来源（T30/T31）。此阶段只保留可追溯字段，
	// 不批量导入旧数据，也不把旧 dataset 当成项目。
	LegacyDatasetID *int64    `json:"legacyDatasetId,omitempty"`
	CreatedAt       time.Time `json:"createdAt"`
	UpdatedAt       time.Time `json:"updatedAt"`
}

// CoverageValues 返回归一化后的 n/m/x。
//
// 为什么要访问器而不是直接读字段：字段是指针（区分「未提供」与「显式 0」），
// 而调用方（store 插入、方案复制、测试）需要的是确定值。把解引用集中在一处，
// 避免每个调用点各写一次 nil 判断而漏掉一个。
func (input CreateProjectInput) CoverageValues() (domains, directionsPerDomain, questionsPerDirection int) {
	if input.Coverage.Domains != nil {
		domains = *input.Coverage.Domains
	}
	if input.Coverage.DirectionsPerDomain != nil {
		directionsPerDomain = *input.Coverage.DirectionsPerDomain
	}
	if input.Coverage.QuestionsPerDirection != nil {
		questionsPerDirection = *input.Coverage.QuestionsPerDirection
	}
	return domains, directionsPerDomain, questionsPerDirection
}

// PlannedQuestions 是 n × m × x，即**计划**问题数。
//
// 它不等于「当前已产出」：实际生成、结构校验通过、纳入检查、已打分、已接纳、
// 已隔离分别统计（§2.1）。禁止用旧 PlanEstimate.AnswerVariants 偷乘目标量。
func (p Project) PlannedQuestions() int {
	return p.DomainCount * p.DirectionsPerDomain * p.QuestionsPerDirection
}

// IsArchived 表示项目只读：可以读历史，但不能发起新运行。
func (p Project) IsArchived() bool { return p.Status == ProjectStatusArchived }

// CreateProjectInput 是创建项目草稿的请求体（契约 §2.1）。
//
// 注意这里**没有** providerId / storageProfileId：创建草稿不得因为缺少模型或存储
// 而失败，也不得调用模型。运行能力在执行前才检查（#160 §1、T10 验收项）。
type CreateProjectInput struct {
	Name        string `json:"name"`
	Goal        string `json:"goal"`
	TargetKind  string `json:"targetKind"`
	WorkspaceID int64  `json:"workspaceId"`

	Coverage struct {
		// 用指针而不是 int：JSON 里「未提供」与「显式填 0」是两件事。
		// 若用 int，0 只能有一个含义，而两种选择都错：
		//   * 当成「未提供」→ 用户显式填 0 时静默变成 1，界面显示的目标与输入不符；
		//   * 当成非法值 → 只想省略该字段的客户端（例如方案复制）必须重复一遍默认值。
		// 契约 §2.1 要求 `domains ≥ 1` 报 422 + fieldErrors，因此必须能区分二者。
		Domains               *int `json:"domains"`
		DirectionsPerDomain   *int `json:"directionsPerDomain"`
		QuestionsPerDirection *int `json:"questionsPerDirection"`
	} `json:"coverage"`

	PilotSize int `json:"pilotSize"`

	Quality struct {
		AcceptanceRateTarget *float64 `json:"acceptanceRateTarget"`
	} `json:"quality"`

	Budget struct {
		Currency    string `json:"currency"`
		LimitMinor  *int64 `json:"limitMinor"`
		OnExhausted string `json:"onExhausted"`
	} `json:"budget"`

	// SourceRecipeVersionID 由 T26 消费；此处只校验 ID 形态，不解析方案内容。
	SourceRecipeVersionID *int64 `json:"sourceRecipeVersionId"`
}

// Normalize 填默认值。默认值只处理「未提供」，不掩盖「提供了非法值」：
// 例如 acceptanceRateTarget 显式传 2.0 必须报错，而不是被夹到 1.0。
func (input *CreateProjectInput) Normalize() {
	input.Name = strings.TrimSpace(input.Name)
	input.Goal = strings.TrimSpace(input.Goal)
	input.TargetKind = strings.ToLower(strings.TrimSpace(input.TargetKind))
	if input.TargetKind == "" {
		input.TargetKind = TargetKindSFT
	}
	if input.Coverage.Domains == nil {
		one := 1
		input.Coverage.Domains = &one
	}
	if input.Coverage.DirectionsPerDomain == nil {
		one := 1
		input.Coverage.DirectionsPerDomain = &one
	}
	if input.Coverage.QuestionsPerDirection == nil {
		one := 1
		input.Coverage.QuestionsPerDirection = &one
	}
	if input.PilotSize == 0 {
		input.PilotSize = 1
	}
	if input.Budget.Currency == "" {
		input.Budget.Currency = "CNY"
	}
	if input.Budget.OnExhausted == "" {
		input.Budget.OnExhausted = BudgetOnExhaustedPause
	}
}

// Validate 返回全部字段级错误；无错误时返回 nil。
//
// 为什么校验放在 model 而不是 handler：创建项目还有非 HTTP 调用方
// （T26 方案复制、T30 迁移 dry-run 的落地路径），handler 层校验管不到它们。
func (input CreateProjectInput) Validate() error {
	var errs FieldErrors

	if input.Name == "" {
		errs = append(errs, FieldError{Field: "name", Message: "必填"})
	} else if len([]rune(input.Name)) > 200 {
		errs = append(errs, FieldError{Field: "name", Message: "不能超过 200 个字符"})
	}
	if input.TargetKind != TargetKindSFT && input.TargetKind != TargetKindGRPO {
		errs = append(errs, FieldError{Field: "targetKind", Message: "只能是 sft 或 grpo"})
	}
	if input.Coverage.Domains != nil && *input.Coverage.Domains < 1 {
		errs = append(errs, FieldError{Field: "coverage.domains", Message: "必须大于等于 1"})
	}
	if input.Coverage.DirectionsPerDomain != nil && *input.Coverage.DirectionsPerDomain < 1 {
		errs = append(errs, FieldError{Field: "coverage.directionsPerDomain", Message: "必须大于等于 1"})
	}
	if input.Coverage.QuestionsPerDirection != nil && *input.Coverage.QuestionsPerDirection < 1 {
		errs = append(errs, FieldError{Field: "coverage.questionsPerDirection", Message: "必须大于等于 1"})
	}
	if input.PilotSize < MinPilotSize || input.PilotSize > MaxPilotSize {
		errs = append(errs, FieldError{
			Field:   "pilotSize",
			Message: fmt.Sprintf("必须在 %d–%d 之间", MinPilotSize, MaxPilotSize),
		})
	}
	if target := input.Quality.AcceptanceRateTarget; target != nil {
		if *target < 0 || *target > 1 {
			errs = append(errs, FieldError{Field: "quality.acceptanceRateTarget", Message: "必须在 0–1 之间"})
		}
	}
	// 预算下限 100 分 = 1 元（契约 §2.1）。显式传 0 视为「未设置」，
	// 而不是「零预算」—— 零预算会让所有执行立即 429，那不是用户的本意。
	if limit := input.Budget.LimitMinor; limit != nil && *limit != 0 && *limit < 100 {
		errs = append(errs, FieldError{Field: "budget.limitMinor", Message: "至少为 100（1 元，单位为分）"})
	}
	if input.Budget.OnExhausted != BudgetOnExhaustedPause && input.Budget.OnExhausted != BudgetOnExhaustedStop {
		errs = append(errs, FieldError{Field: "budget.onExhausted", Message: "只能是 pause 或 stop"})
	}
	if input.Budget.Currency != "CNY" {
		errs = append(errs, FieldError{Field: "budget.currency", Message: "当前只支持 CNY"})
	}

	if len(errs) > 0 {
		return errs
	}
	return nil
}

// AcceptanceTargetValue 返回最终落库的接纳率目标（未提供时用默认值）。
func (input CreateProjectInput) AcceptanceTargetValue() float64 {
	if input.Quality.AcceptanceRateTarget != nil {
		return *input.Quality.AcceptanceRateTarget
	}
	return 0.85
}

// BudgetLimitValue 返回最终落库的预算上限（未提供时 0 = 未设上限）。
func (input CreateProjectInput) BudgetLimitValue() int64 {
	if input.Budget.LimitMinor != nil {
		return *input.Budget.LimitMinor
	}
	return 0
}

// Capabilities 是服务端按「当前用户 × 对象 × 对象状态」算出的能力位（契约 §4）。
//
// 它只辅助 UI 呈现，**不是安全边界**：每个写 API 自行重新校验（#160 §5）。
type Capabilities struct {
	CanEdit          bool `json:"canEdit"`
	CanRun           bool `json:"canRun"`
	CanReview        bool `json:"canReview"`
	CanPublish       bool `json:"canPublish"`
	CanDownload      bool `json:"canDownload"`
	CanManageMembers bool `json:"canManageMembers"`
}

// ProjectCapabilities 按项目角色与项目状态派生能力位。
//
// archived 项目的所有写能力为 false —— 归档只阻止新运行，读与下载保持可用，
// 所以 canDownload/canReview 不因归档关闭。
func ProjectCapabilities(role, status string) Capabilities {
	archived := status == ProjectStatusArchived
	switch role {
	case ProjectRoleOwner:
		return Capabilities{
			CanEdit:          !archived,
			CanRun:           !archived,
			CanReview:        !archived,
			CanPublish:       !archived,
			CanDownload:      true,
			CanManageMembers: !archived,
		}
	case ProjectRoleReviewer:
		return Capabilities{CanReview: !archived, CanDownload: true}
	case ProjectRoleViewer:
		return Capabilities{CanDownload: true}
	default:
		return Capabilities{}
	}
}
