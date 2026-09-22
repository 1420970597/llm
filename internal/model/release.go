package model

import (
	"fmt"
	"sort"
	"strings"
)

// 本文件定义发布候选的门槛判定（Issue #160 T20）。
//
// 契约：docs/plans/atelier-implementation.md §2.5（发布名与 ID）、§4.2（发布状态机）、
// docs/plans/atelier-api-contract.md §2.8（候选与 blocker）、§2.9（冻结）。
//
// 本文件的核心主张：**门槛必须能在冻结之前算清楚，并且每条阻塞都指向具体对象**。
// 一句「还不能发布」会让用户自己在几万条内容里找原因，而那实际上等于没有门槛。
//
// 门槛的六类判据（对应 T20 验收项逐条）：
//  1. 必需字段（版本名/映射/格式/用途）；
//  2. 范围非空（空范围的分母为 0，却会产出「0 条通过」的假结论）；
//  3. 每个内容版本都有**有效接纳**且确认了**当前**证据版本；
//  4. 没有待审阅、没有未协调的冲突；
//  5. 隔离项必须带排除原因（静默排除会让数据卡失真）；
//  6. 接纳率达标（分母是**冻结范围**，不是「当前筛选」）。

// 发布状态（§4.2）。
const (
	ReleaseStatusCandidate   = "candidate"
	ReleaseStatusBlocked     = "blocked"
	ReleaseStatusBuilding    = "building"
	ReleaseStatusPublished   = "published"
	ReleaseStatusBuildFailed = "build_failed"
)

// ReleaseStatusTerminal 判断状态是否不可再变。
//
// published 之后内容/映射/数据卡/hash 只读：后续风险通过**独立警告**表达，
// 不重写历史文件（§2.4）。因此它必须被当作终态而不是「可以再改一次」。
func ReleaseStatusTerminal(status string) bool {
	return status == ReleaseStatusPublished
}

// 阻塞代码。前端按 code 分支（不解析中文文案）。
const (
	BlockerMissingField        = "MISSING_FIELD"
	BlockerEmptyRange          = "EMPTY_RANGE"
	BlockerPendingReview       = "PENDING_REVIEW"
	BlockerReviewConflict      = "REVIEW_CONFLICT"
	BlockerEvidenceIncomplete  = "EVIDENCE_INCOMPLETE"
	BlockerQuarantined         = "QUARANTINED"
	BlockerExclusionNoReason   = "EXCLUSION_WITHOUT_REASON"
	BlockerQualityTargetMissed = "QUALITY_TARGET_NOT_MET"
)

// ReleaseBlocker 是一条阻塞项（**领域层**形态）。
//
// 刻意不带 Link（那是契约 §1.2 的响应结构，属于 studio 层）：
// 领域层只回答「哪一条内容因为什么被挡住」，
// 而「点它跳到哪个 URL」由 studio 层拼 —— 否则领域规则要依赖 API 路径形态。
type ReleaseBlocker struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	// SampleVersionID 指向具体内容版本（nil 表示这是范围级问题）。
	SampleVersionID *int64 `json:"sampleVersionId,omitempty"`
	// Field 用于「必需字段缺失」这类字段级阻塞。
	Field string `json:"field,omitempty"`
}

// ReleaseItemFacts 是一个候选清单项的门槛相关事实。
type ReleaseItemFacts struct {
	SampleID        int64
	SampleVersionID int64

	// 有效处置（来自 review_projections）。
	EffectiveAction string
	// 冻结时记录的两个 revision。
	AggregateReviewRevision int64
	EvidenceRevision        int64
	// 当前投影上的两个 revision（用于检测「确认与冻结之间有人改过」）。
	CurrentAggregateReviewRevision int64
	CurrentEvidenceRevision        int64
	// Excluded 表示这一项被排除在最终范围之外。
	Excluded       bool
	ExcludedReason string
}

// ReleaseGateInput 是门槛判定的全部输入。
type ReleaseGateInput struct {
	// 候选的必需字段。
	ReleaseName      string
	MappingVersionID int64
	Format           string
	IntendedUse      string

	// 范围（已冻结的内容版本）。
	Items []ReleaseItemFacts

	// 项目质量目标（0 表示未设目标，此时不做达标判定）。
	AcceptanceRateTarget float64
	// 已接纳数（在**冻结范围**内的已接纳项，含被排除但曾接纳的项）。
	AcceptedCount int
}

// ReleaseGateResult 是门槛判定结果。
type ReleaseGateResult struct {
	Passed   bool             `json:"passed"`
	Blockers []ReleaseBlocker `json:"blockers"`
	// Inspected 是分母：冻结范围的大小（不随排除缩小，§2.3）。
	Inspected int `json:"inspected"`
	// AcceptanceRate 为 nil 表示无结论（分母为 0）。
	AcceptanceRate        *float64 `json:"acceptanceRate"`
	AcceptanceRateDisplay string   `json:"acceptanceRateDisplay"`
}

// EvaluateReleaseGate 判定候选是否可以通过门槛。
//
// 顺序刻意是「字段 → 范围 → 逐项 → 汇总质量」：
// 字段缺失时逐项检查没有意义（用户先要补字段），而把两类问题混在一起
// 会让 blocker 列表很长、第一条却不是最该修的。
//
// 返回**全部** blocker 而不是第一个：用户一次修完比来回提交四次快得多
// （与后端 `Validate()` 一次报全部同一约定）。
func EvaluateReleaseGate(input ReleaseGateInput) ReleaseGateResult {
	blockers := []ReleaseBlocker{}

	// ---- 1. 必需字段 ----
	if strings.TrimSpace(input.ReleaseName) == "" {
		blockers = append(blockers, ReleaseBlocker{
			Code: BlockerMissingField, Field: "releaseName",
			Message: "发布版本名必填（它会成为项目内唯一的可读版本名）",
		})
	}
	if input.MappingVersionID <= 0 {
		blockers = append(blockers, ReleaseBlocker{
			Code: BlockerMissingField, Field: "mappingVersionId",
			Message: "必须选择映射版本：没有它无法确定输出字段",
		})
	}
	if strings.TrimSpace(input.Format) == "" {
		blockers = append(blockers, ReleaseBlocker{
			Code: BlockerMissingField, Field: "format",
			Message: "必须选择输出格式",
		})
	}
	if strings.TrimSpace(input.IntendedUse) == "" {
		blockers = append(blockers, ReleaseBlocker{
			Code: BlockerMissingField, Field: "intendedUse",
			Message: "必须填写用途：数据卡要能说清这份数据用来做什么，缺失会导致下游误用",
		})
	}

	// ---- 2. 范围非空 ----
	// 空范围会产出「0 条通过」这种看起来成功的结果，而它的分母为 0
	//（接纳率「无结论」）。因此必须显式阻塞，而不是发布一个空文件。
	included := make([]ReleaseItemFacts, 0, len(input.Items))
	for _, item := range input.Items {
		if !item.Excluded {
			included = append(included, item)
		}
	}
	if len(included) == 0 {
		blockers = append(blockers, ReleaseBlocker{
			Code:    BlockerEmptyRange,
			Message: "发布范围为空：没有任何内容版本可发布（空范围不能产出有效版本）",
		})
	}

	// ---- 3~5. 逐项检查 ----
	accepted := 0
	for _, item := range input.Items {
		versionID := item.SampleVersionID

		// 被排除的项必须有原因（静默排除会让数据卡说不清覆盖损失）。
		if item.Excluded {
			if strings.TrimSpace(item.ExcludedReason) == "" {
				blockers = append(blockers, ReleaseBlocker{
					Code: BlockerExclusionNoReason, SampleVersionID: &versionID,
					Message: fmt.Sprintf("内容版本 %d 被排除但没写原因：数据卡必须能解释覆盖损失", item.SampleVersionID),
				})
			}
			if item.EffectiveAction == EffectiveAccepted {
				// 被排除但曾接纳：计入分子，但分母仍是冻结范围（§2.3）。
				accepted++
			}
			continue
		}

		// 「确认与冻结之间有人改过判断/证据」——这是并发场景的核心检测。
		if item.CurrentAggregateReviewRevision != item.AggregateReviewRevision ||
			item.CurrentEvidenceRevision != item.EvidenceRevision {
			blockers = append(blockers, ReleaseBlocker{
				Code: BlockerEvidenceIncomplete, SampleVersionID: &versionID,
				Message: fmt.Sprintf(
					"内容版本 %d 的判断或证据在你确认之后发生了变化，请重新确认范围", item.SampleVersionID),
			})
			continue
		}

		switch item.EffectiveAction {
		case EffectiveAccepted:
			accepted++
		case EffectiveQuarantined:
			blockers = append(blockers, ReleaseBlocker{
				Code: BlockerQuarantined, SampleVersionID: &versionID,
				Message: fmt.Sprintf("内容版本 %d 已被隔离，不能进入发布范围", item.SampleVersionID),
			})
		case EffectiveConflict:
			blockers = append(blockers, ReleaseBlocker{
				Code: BlockerReviewConflict, SampleVersionID: &versionID,
				Message: fmt.Sprintf("内容版本 %d 存在相反的人工判断，需要项目负责人协调", item.SampleVersionID),
			})
		default:
			// pending：待审阅也是阻塞（§2.3「待审阅不算接纳」）。
			blockers = append(blockers, ReleaseBlocker{
				Code: BlockerPendingReview, SampleVersionID: &versionID,
				Message: fmt.Sprintf("内容版本 %d 还没有有效接纳（待审阅不算接纳）", item.SampleVersionID),
			})
		}
	}

	// ---- 6. 质量目标 ----
	// 分母是**冻结范围**（inspected），不是「最终纳入的条数」——
	// 用后者会让「排除几条差的」变成提高接纳率的手段（§2.3 明确禁止）。
	inspected := len(included) + countExcluded(input.Items)
	rate, display := AcceptanceRateOf(accepted, inspected)
	result := ReleaseGateResult{
		Inspected: inspected, AcceptanceRate: rate, AcceptanceRateDisplay: display,
	}
	if input.AcceptanceRateTarget > 0 {
		if rate == nil {
			blockers = append(blockers, ReleaseBlocker{
				Code: BlockerQualityTargetMissed,
				Message: fmt.Sprintf("项目设定了 %.0f%% 的接纳率目标，但当前分母为 0（无结论），无法确认达标",
					input.AcceptanceRateTarget*100),
			})
		} else if *rate < input.AcceptanceRateTarget {
			blockers = append(blockers, ReleaseBlocker{
				Code: BlockerQualityTargetMissed,
				Message: fmt.Sprintf("接纳率 %.1f%% 低于项目目标 %.1f%%（分母为冻结的 %d 个内容版本）",
					*rate*100, input.AcceptanceRateTarget*100, inspected),
			})
		}
	}

	// 排序：让界面与测试的「第一条」稳定（按内容版本号，范围级问题排最后）。
	sort.SliceStable(blockers, func(i, j int) bool {
		left, right := blockers[i], blockers[j]
		switch {
		case left.SampleVersionID == nil && right.SampleVersionID != nil:
			return false
		case left.SampleVersionID != nil && right.SampleVersionID == nil:
			return true
		case left.SampleVersionID != nil && right.SampleVersionID != nil:
			return *left.SampleVersionID < *right.SampleVersionID
		default:
			return left.Code < right.Code
		}
	})

	result.Blockers = blockers
	result.Passed = len(blockers) == 0
	return result
}

// countExcluded 统计被排除的项数（它们仍计入分母）。
//
// 显式写成函数是为了让「分母 = 纳入 + 排除」这件事在代码里可见：
// 如果哪天有人把分母改成只算纳入的，这个函数会变成唯一的改动点，
// 而它的注释会提醒为什么要保留排除项。
func countExcluded(items []ReleaseItemFacts) int {
	count := 0
	for _, item := range items {
		if item.Excluded {
			count++
		}
	}
	return count
}

// NormalizeReleaseNameKey 规范化版本名用于唯一约束。
//
// 去空白 + 转小写：不让 `V1.2` 与 `v1.2` 同时存在 ——
// 用户会以为它们是同一版，而下载列表里出现两个「v1.2」是明显的混乱。
func NormalizeReleaseNameKey(name string) string {
	return strings.ToLower(strings.TrimSpace(name))
}

// ValidateReleaseName 校验版本名形态。
//
// 禁止 `latest`：下载路径禁止 latest 回退（§2.5），而一个叫 latest 的
// 版本名会让「文件名里的 latest」变成对用户的误导 —— 它会看起来像
// 「总是最新」，实际是一个固定版本。
func ValidateReleaseName(name string) error {
	trimmed := strings.TrimSpace(name)
	var errs FieldErrors
	if trimmed == "" {
		errs = append(errs, FieldError{Field: "releaseName", Message: "发布版本名必填"})
	} else if len([]rune(trimmed)) > 64 {
		errs = append(errs, FieldError{Field: "releaseName", Message: "版本名不能超过 64 个字符"})
	} else if strings.EqualFold(trimmed, "latest") {
		errs = append(errs, FieldError{Field: "releaseName",
			Message: "版本名不能叫 latest：下载路径禁止 latest 回退，用它会误导用户以为文件总是最新"})
	} else if strings.ContainsAny(trimmed, "/\\:*?\"<>|") {
		errs = append(errs, FieldError{Field: "releaseName",
			Message: "版本名不能包含路径分隔符或通配符（它会出现在下载文件名里）"})
	}
	if len(errs) > 0 {
		return errs
	}
	return nil
}

// ReleaseCapabilitiesView is the release-specific capability shape from
// contract §4.  It deliberately is not the project capability shape: a
// release has no edit/run/member controls, but does expose the published-only
// "create next" action.
type ReleaseCapabilitiesView struct {
	// CanEdit is retained for compatibility with the existing model-level
	// capability contract; published releases always return false.
	CanEdit       bool `json:"-"`
	CanPublish    bool `json:"canPublish"`
	CanDownload   bool `json:"canDownload"`
	CanCreateNext bool `json:"canCreateNext"`
}

// ReleaseCapabilities 派生发布能力位（契约 §4）。
//
// published 是**终态**：只有下载与「创建下一版」可用（§2.4 发布后只读）。
func ReleaseCapabilities(role, status string) ReleaseCapabilitiesView {
	isOwner := role == ProjectRoleOwner
	canReview := isOwner || role == ProjectRoleReviewer
	switch status {
	case ReleaseStatusPublished:
		return ReleaseCapabilitiesView{
			CanDownload:   canReview || role == ProjectRoleViewer,
			CanCreateNext: isOwner,
		}
	case ReleaseStatusBuilding:
		// building 期间不可重复发布（重复命令返回同一个 release，见 §2.9）。
		return ReleaseCapabilitiesView{CanDownload: canReview}
	default:
		return ReleaseCapabilitiesView{CanEdit: isOwner, CanPublish: isOwner, CanDownload: canReview}
	}
}
