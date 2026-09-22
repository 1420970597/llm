package model

import (
	"sort"
	"strings"
	"time"
)

// 本文件定义追加式人工判断、分派与冲突协调（Issue #160 T16）。
//
// 契约：docs/plans/atelier-implementation.md §4.1（Decision 对象）、
// §4.3（三个 revision 的作用域）；docs/plans/atelier-api-contract.md §2.7。
//
// 四条不可让步的性质：
//
//  1. **只追加**：判断没有更新路径。更正通过 `supersedes` 指向被取代的那条，
//     旧行永远保留 —— 否则「原本判了什么、谁判的、为什么」会被永久抹掉，
//     而申诉与复核恰恰只需要这些。
//  2. **判断不改内容**：判断只引用 `sample_version_id`，从不写样本正文
//     （§4.1「原始内容永不覆盖」）。
//  3. **个人 revision 与聚合 revision 是两件事**：
//     前者管「我自己有没有被并发覆盖」，后者管「这一版的有效处置变没变」。
//     合并成一个会让「同一人并发更正」与「两人意见相反」无法区分。
//  4. **证据版本参与有效性**：判断记录它确认的 evidence_revision；
//     证据集递增后旧判断不再构成有效接纳（回到待判断）。

// 判断动作。只有两种终态处置：接纳与隔离。
//
// 没有「不适用」：如果一条内容无法判断，那属于**缺证据**（evidence 问题），
// 而不是一种处置 —— 把「判不了」做成第三种动作会让接纳率的分母失去意义
// （它可以被用来把难判的内容排除出分母）。
const (
	DecisionAccept     = "accepted"
	DecisionQuarantine = "quarantined"
)

// 有效处置（投影）。
//
// `conflict` 是一个**独立状态**而不是「最后一条胜出」：两人意见相反时
// 系统不应该替他们选一个，而应当把冲突暴露出来，由项目 owner 追加协调决定
// 才能解除发布阻塞（T16 验收项）。
const (
	EffectivePending     = "pending"
	EffectiveAccepted    = "accepted"
	EffectiveQuarantined = "quarantined"
	EffectiveConflict    = "conflict"
)

// 待判断的原因。界面据此告诉用户「下一步做什么」，而不是只显示一个待定状态。
const (
	PendingReasonNoDecision      = "no_decision"
	PendingReasonEvidenceChanged = "evidence_changed"
	PendingReasonConflict        = "conflict"
	PendingReasonNewVersion      = "new_version"
)

// 分派状态。
const (
	AssignmentOpen      = "open"
	AssignmentDone      = "done"
	AssignmentCancelled = "cancelled"
)

// ReviewDecision 是一条判断（追加式）。
type ReviewDecision struct {
	ID               int64  `json:"id"`
	ProjectID        int64  `json:"projectId"`
	SampleID         int64  `json:"sampleId"`
	SampleVersionID  int64  `json:"sampleVersionId"`
	ContentHash      string `json:"contentHash"`
	EvidenceRevision int64  `json:"evidenceRevision"`

	ReviewerID       int64  `json:"reviewerId"`
	ReviewerRevision int64  `json:"reviewerRevision"`
	Action           string `json:"action"`
	Reason           string `json:"reason"`

	Supersedes   *int64    `json:"supersedes,omitempty"`
	ResolutionOf *int64    `json:"resolutionOf,omitempty"`
	CreatedAt    time.Time `json:"createdAt"`
}

// ReviewProjection 是「有效处置」的投影。
//
// 它是**可重建的**：事实只有 review_decisions 一张表，投影损坏可以重放修复。
// 这一点很重要 —— 它意味着投影更新逻辑即使有 bug 也不会丢判断历史。
type ReviewProjection struct {
	SampleVersionID int64  `json:"sampleVersionId"`
	ProjectID       int64  `json:"projectId"`
	ContentHash     string `json:"contentHash"`

	EvidenceRevision        int64     `json:"evidenceRevision"`
	AggregateReviewRevision int64     `json:"aggregateReviewRevision"`
	EffectiveAction         string    `json:"effectiveAction"`
	Conflict                bool      `json:"conflict"`
	DecisionCount           int       `json:"decisionCount"`
	PendingReason           string    `json:"pendingReason"`
	UpdatedAt               time.Time `json:"updatedAt"`
}

// ReviewAssignment 是一条分派待办。
type ReviewAssignment struct {
	ID              int64      `json:"id"`
	ProjectID       int64      `json:"projectId"`
	SampleID        int64      `json:"sampleId"`
	SampleVersionID int64      `json:"sampleVersionId"`
	RiskKey         string     `json:"riskKey"`
	AssigneeID      *int64     `json:"assigneeId,omitempty"`
	AssignedBy      *int64     `json:"assignedBy,omitempty"`
	Status          string     `json:"status"`
	Note            string     `json:"note"`
	CreatedAt       time.Time  `json:"createdAt"`
	ResolvedAt      *time.Time `json:"resolvedAt,omitempty"`
}

// SubmitDecisionInput 是提交一条判断的请求（契约 §2.7）。
type SubmitDecisionInput struct {
	SampleVersionID int64
	// EvidenceRevision 是**提交者确认的**当前必需证据版本。
	// 与库中不一致时 409（旧证据的迟到提交），因此调用方必须先读再提交。
	EvidenceRevision int64
	// ReviewerRevision 是**该审阅者的**个人并发序号。
	// 调用方应当带上「我基于哪一次判断编辑」的序号；它防的是
	// 「同一个人开了两个标签页，后保存的静默覆盖先保存的」。
	ReviewerRevision int64
	Action           string
	Reason           string
	// Supersedes 非空表示这是对某条判断的更正。
	Supersedes *int64
}

// ValidateSubmitDecision 校验提交内容（服务端）。
//
// 理由必填：没有理由的判断无法复核，也无法在冲突时被协调 ——
// 而「协调」正是本项目把冲突做成显式状态的前提。
func ValidateSubmitDecision(input SubmitDecisionInput) error {
	var errs FieldErrors
	if input.SampleVersionID <= 0 {
		errs = append(errs, FieldError{Field: "sampleVersionId", Message: "必须指定要判断的内容版本"})
	}
	if strings.TrimSpace(input.Reason) == "" {
		errs = append(errs, FieldError{Field: "reason", Message: "理由必填：没有理由的判断无法被复核"})
	}
	switch input.Action {
	case DecisionAccept, DecisionQuarantine:
	default:
		errs = append(errs, FieldError{Field: "action",
			Message: "处置只能是 accepted 或 quarantined"})
	}
	if input.ReviewerRevision < 1 {
		// 从 1 开始（不是 0）：0 与「未提供」无法区分，而把「未提供」当成
		// 合法序号会让并发检查静默失效 —— 那正是这个字段要防的事。
		errs = append(errs, FieldError{Field: "reviewerRevision",
			Message: "必须提供你自己的判断序号（从 1 开始），用于检查并发更正"})
	}
	if len(errs) > 0 {
		return errs
	}
	return nil
}

// DecisionFacts 是一次投影计算所需的全部事实。
type DecisionFacts struct {
	// Decisions 是**当前有效**的判断（已排除被 supersedes 指向的那些）。
	Decisions []ReviewDecision
	// EvidenceRevision 是当前生效的必需证据集版本。
	EvidenceRevision int64
	// ContentHash 是当前内容版本的内容 hash。
	ContentHash string
	// ProjectedContentHash 是投影上次计算时对应的内容 hash。
	// 与 ContentHash 不同说明内容产生了新版本 → 需要重新判断。
	ProjectedContentHash string
	// ProjectedEvidenceRevision 是投影上次计算时的证据版本。
	ProjectedEvidenceRevision int64
}

// ComputeProjection 由判断事实推导「有效处置」。
//
// 这是本文件最核心的纯函数，也是 T16 三条验收项的落点：
//
//	(a) 「两人相反判断保留两条、标 conflict」—— 相反动作并存时直接冲突，
//	    不按时间取最后一条（那会静默丢掉一个人的意见）。
//	(b) 「证据集变化后旧接纳回到待判断」—— 判断确认的证据版本与当前不一致时，
//	    即使动作相同也不算有效接纳。
//	(c) 「新内容版本需要新判断」—— 内容 hash 变化同样回到待判断。
//
// 返回值里的 revision 变化交给调用方递增（本函数不持有状态）。
func ComputeProjection(facts DecisionFacts) (effectiveAction string, pendingReason string, conflict bool) {
	// (c) 内容新版本：旧判断针对的是另一份内容，必须先重新判断。
	//     放在最前，因为「内容变了」比「证据变了」更根本。
	if facts.ProjectedContentHash != "" && facts.ContentHash != "" &&
		facts.ProjectedContentHash != facts.ContentHash {
		return EffectivePending, PendingReasonNewVersion, false
	}

	if len(facts.Decisions) == 0 {
		return EffectivePending, PendingReasonNoDecision, false
	}

	// **(a0) 协调决定优先**（这是一次真实缺陷的修复，由测试发现）：
	// owner 追加协调决定后，投影必须取它的结论并清除冲突 ——
	// 否则「相反意见仍然有效」会让冲突永远无法解除，而 T16 验收项要求
	// 「项目 owner 追加协调决定后才能解除发布阻塞」。
	//
	// 注意仍然要求它确认**当前证据版本**：证据集变化后旧的协调同样失效
	//（与普通判断一致，否则「新风险沿用旧结论」会从协调这条路绕回来）。
	// 协调决定取最新一条（ID 最大），因为 owner 可能修正自己的裁定。
	var latestResolution *ReviewDecision
	for index := range facts.Decisions {
		decision := facts.Decisions[index]
		if decision.ResolutionOf == nil {
			continue
		}
		if decision.EvidenceRevision != facts.EvidenceRevision {
			continue
		}
		if latestResolution == nil || decision.ID > latestResolution.ID {
			candidate := decision
			latestResolution = &candidate
		}
	}
	if latestResolution != nil {
		switch latestResolution.Action {
		case DecisionQuarantine:
			return EffectiveQuarantined, "", false
		default:
			return EffectiveAccepted, "", false
		}
	}

	// 只看**确认了当前证据版本**的判断。旧证据的判断不构成有效接纳 ——
	// 这正是「新风险不能沿用旧接纳发布」的实现方式。
	current := make([]ReviewDecision, 0, len(facts.Decisions))
	for _, decision := range facts.Decisions {
		if decision.EvidenceRevision == facts.EvidenceRevision {
			current = append(current, decision)
		}
	}
	if len(current) == 0 {
		return EffectivePending, PendingReasonEvidenceChanged, false
	}

	// (a) 相反动作并存 → 冲突。**不做多数表决**：
	//     2 人接纳 / 1 人隔离时按多数放行，会让少数派的理由被静默忽略，
	//     而「少数派为什么说它有问题」往往才是真正需要看的。
	hasAccept, hasQuarantine := false, false
	for _, decision := range current {
		switch decision.Action {
		case DecisionAccept:
			hasAccept = true
		case DecisionQuarantine:
			hasQuarantine = true
		}
	}
	if hasAccept && hasQuarantine {
		return EffectiveConflict, PendingReasonConflict, true
	}
	if hasQuarantine {
		return EffectiveQuarantined, "", false
	}
	return EffectiveAccepted, "", false
}

// EffectiveDecisions 返回参与投影计算的判断（排除被取代的）。
//
// 排除依据是 supersedes 链：一条判断被更正的判断指向即失效。
// 这使「纠错后旧证据、操作者可追溯」成立 —— 旧行仍在表里，只是不参与投影。
func EffectiveDecisions(all []ReviewDecision) []ReviewDecision {
	superseded := map[int64]bool{}
	for _, decision := range all {
		if decision.Supersedes != nil {
			superseded[*decision.Supersedes] = true
		}
	}
	effective := make([]ReviewDecision, 0, len(all))
	for _, decision := range all {
		if superseded[decision.ID] {
			continue
		}
		effective = append(effective, decision)
	}
	sort.SliceStable(effective, func(i, j int) bool { return effective[i].ID < effective[j].ID })
	return effective
}

// NextReviewerRevision 计算某审阅者的下一个个人序号。
//
// 调用方在**提交前**用它校验「我拿的序号是否还是最新的」：
// 客户端带旧序号 → 409 并保留输入（T16 验收项「过期返回 409 并保留输入」）。
func NextReviewerRevision(existing []ReviewDecision, reviewerID int64) int64 {
	maxRevision := int64(0)
	for _, decision := range existing {
		if decision.ReviewerID != reviewerID {
			continue
		}
		if decision.ReviewerRevision > maxRevision {
			maxRevision = decision.ReviewerRevision
		}
	}
	return maxRevision + 1
}

// ReviewProjectionCapabilities 派生能力位（契约 §4）。
//
// `CanResolveConflict` 只给 owner：冲突协调是**决定最终结论**的动作，
// reviewer 只能表达意见（两条相反的意见正是冲突的来源）。
func ReviewProjectionCapabilities(role, effectiveAction string) Capabilities {
	isOwner := role == ProjectRoleOwner
	canReview := isOwner || role == ProjectRoleReviewer
	return Capabilities{
		CanEdit:   isOwner,
		CanRun:    false,
		CanReview: canReview,
		// 只有 **accepted** 才可发布（这是一次真实缺陷的修复，由测试发现）：
		// 原先写成 `!= conflict`，于是**已隔离**的内容对一个 owner 也显示
		// 「可发布」—— 而隔离正是「这条不能进发布范围」的结论。
		// 待判断同理不可发布。这个位置只表达能力，真正的阻塞由 T20 的门槛计算。
		CanPublish:  isOwner && effectiveAction == EffectiveAccepted,
		CanDownload: canReview || role == ProjectRoleViewer,
	}
}
