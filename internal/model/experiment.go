package model

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// 本文件定义冻结质量实验的对象与判据（Issue #160 T14）。
//
// 契约：docs/plans/atelier-implementation.md §2.3（质量分母）、§2.6（缺分语义）、
// §4.1（Experiment / Decision 的 revision 语义）。
//
// 三组常量与判据集中在这里，而不是散在 store 与 handler 里：
// 它们同时被「创建实验」「执行评估」「报告聚合」「界面能力位」四处使用，
// 任何一处写错都会让「这批数据可以发布」这个结论出错。

// 实验状态（§2.6：与批次状态**独立**）。
const (
	ExperimentStatusQueued        = "queued"
	ExperimentStatusRunning       = "running"
	ExperimentStatusPartialFailed = "partial_failed"
	ExperimentStatusCompleted     = "completed"
	ExperimentStatusFailed        = "failed"
)

// 实验用途。两者的**分母含义不同**，因此必须区分：
//   - quality：分母是冻结的样本范围（§2.3）；
//   - comparison：分母是**配对完成数**（T18），两侧缺一边就不成对。
const (
	ExperimentPurposeQuality    = "quality"
	ExperimentPurposeComparison = "comparison"
)

// 实验项状态。
//
// 关键：`missing`（缺分）、`error`（裁判出错）、`not_applicable`（该维度不适用）
// 是**三种不同的**事实，且**都不等于 0 分**（§2.6）。
// 把它们压成一个「0 分」会让「没评」显示成「评得很差」——
// 那是会让用户错误地淘汰好数据的方向。
const (
	ExperimentItemPending       = "pending"
	ExperimentItemScored        = "scored"
	ExperimentItemMissing       = "missing"
	ExperimentItemError         = "error"
	ExperimentItemNotApplicable = "not_applicable"
)

// 评分格状态（每裁判 × 每维度一格）。
const (
	ScoreStateScored        = "scored"
	ScoreStateMissing       = "missing"
	ScoreStateError         = "error"
	ScoreStateNotApplicable = "not_applicable"
)

// 缺分策略。
const (
	// MissingScoreExclude 把缺分从该维度的分母里排除（默认）。
	// 必须在报告里显示实际覆盖，否则「平均分」会掩盖大量缺分。
	MissingScoreExclude = "exclude"
	// MissingScoreFailExperiment 缺分即实验失败。
	// 适用于「评分必须完整才有意义」的场景（例如同基准比较）。
	MissingScoreFailExperiment = "fail_experiment"
)

// JudgeSpec 是一名裁判的冻结快照。
type JudgeSpec struct {
	ConnectionID int64  `json:"connectionId"`
	Label        string `json:"label"`
	// EndpointFingerprint 是**独立性判定**的依据：同一个真实来源
	// （同 endpoint）即使配了两条 provider 记录，也不算两个独立裁判。
	EndpointFingerprint string `json:"endpointFingerprint"`
	// ModelName 参与指纹判断的辅助信息（同 endpoint 不同模型算不算同源
	// 是一个判断，这里冻结当时的结论）。
	ModelName string `json:"modelName"`
}

// GeneratorSource 是一个生成来源的冻结快照（**由样本来源推导**）。
type GeneratorSource struct {
	ConnectionID        int64  `json:"connectionId"`
	EndpointFingerprint string `json:"endpointFingerprint"`
	// SampleVersionIDs 记录哪些样本版本来自这个来源，使
	// 「多生成来源时逐条判断独立性」可以给出具体覆盖（T14 验收项）。
	SampleVersionIDs []int64 `json:"sampleVersionIds"`
}

// RubricDimension 是量表里的一个维度（冻结快照）。
type RubricDimension struct {
	Key    string  `json:"key"`
	Label  string  `json:"label"`
	Weight float64 `json:"weight"`
	// Min/Max 是归一化范围：聚合时把原始分映射到 0–1 再按权重平均。
	// 把范围冻结进量表（而不是每次从当前维度表读）是「报告可复算」的前提。
	Min float64 `json:"min"`
	Max float64 `json:"max"`
}

// RubricSpec 是量表快照。
type RubricSpec struct {
	Dimensions []RubricDimension `json:"dimensions"`
}

// Validate 校验量表：维度非空、权重和必须为 1、范围合法。
//
// 权重和必须为 1 是 §5 的明确要求（`Weights` 总和必须为 1）。
// 不校验会让「归一化总分」失去意义：权重和 0.6 的总分会被当成满分比例。
func (rubric RubricSpec) Validate() error {
	if len(rubric.Dimensions) == 0 {
		return FieldErrors{{Field: "rubric.dimensions", Message: "量表至少需要一个维度"}}
	}
	var errs FieldErrors
	total := 0.0
	seen := map[string]bool{}
	for index, dimension := range rubric.Dimensions {
		field := fmt.Sprintf("rubric.dimensions[%d]", index)
		if strings.TrimSpace(dimension.Key) == "" {
			errs = append(errs, FieldError{Field: field + ".key", Message: "维度键必填"})
			continue
		}
		if seen[dimension.Key] {
			errs = append(errs, FieldError{Field: field + ".key", Message: "维度键重复"})
		}
		seen[dimension.Key] = true
		if dimension.Weight < 0 {
			errs = append(errs, FieldError{Field: field + ".weight", Message: "权重不能为负"})
		}
		if dimension.Max <= dimension.Min {
			errs = append(errs, FieldError{
				Field: field + ".max", Message: "归一化上界必须大于下界",
			})
		}
		total += dimension.Weight
	}
	// 允许 0.001 的浮点误差：权重由用户在界面上以小数输入，
	// 0.1+0.2+0.7 在浮点下不等于 1.0，把它判成错误会让合法配置无法保存。
	if len(errs) == 0 && (total < 0.999 || total > 1.001) {
		errs = append(errs, FieldError{
			Field:   "rubric.dimensions",
			Message: fmt.Sprintf("权重总和必须为 1，当前为 %.3f", total),
		})
	}
	if len(errs) > 0 {
		return errs
	}
	return nil
}

// Experiment 是一个冻结的实验。
type Experiment struct {
	ID         int64  `json:"id"`
	ProjectID  int64  `json:"projectId"`
	BatchID    *int64 `json:"batchId,omitempty"`
	TargetKind string `json:"targetKind"`
	Status     string `json:"status"`
	Purpose    string `json:"purpose"`

	SamplingSeed int64 `json:"samplingSeed"`

	Judges           []JudgeSpec       `json:"judges"`
	Rubric           RubricSpec        `json:"rubric"`
	GeneratorSources []GeneratorSource `json:"generatorSources"`

	MissingScorePolicy string `json:"missingScorePolicy"`

	// 分母是固定的 inspectedCount（= experiment_items 行数）；
	// 其余三个是分子/侧面计数。
	InspectedCount int `json:"inspectedCount"`
	ScoredCount    int `json:"scoredCount"`
	MissingCount   int `json:"missingCount"`
	ErrorCount     int `json:"errorCount"`

	CreatedBy  *int64     `json:"createdBy,omitempty"`
	StartedAt  *time.Time `json:"startedAt,omitempty"`
	FinishedAt *time.Time `json:"finishedAt,omitempty"`
	CreatedAt  time.Time  `json:"createdAt"`
	UpdatedAt  time.Time  `json:"updatedAt"`
}

// ExperimentItem 是冻结范围内的一个待评对象（一个样本版本）。
type ExperimentItem struct {
	ID              int64  `json:"id"`
	ExperimentID    int64  `json:"experimentId"`
	ProjectID       int64  `json:"projectId"`
	SampleID        int64  `json:"sampleId"`
	SampleVersionID int64  `json:"sampleVersionId"`
	ContentHash     string `json:"contentHash"`
	// GeneratorSource/Fingerprint **由样本来源推导**并冻结。
	GeneratorSource      string `json:"generatorSource"`
	GeneratorFingerprint string `json:"generatorFingerprint"`
	Status               string `json:"status"`
	Attempts             int    `json:"attempts"`
	ErrorClass           string `json:"errorClass"`
	ErrorMessage         string `json:"errorMessage"`
}

// ExperimentScore 是一格评分（裁判 × 维度）。
type ExperimentScore struct {
	ID                int64  `json:"id"`
	ExperimentID      int64  `json:"experimentId"`
	ExperimentItemID  int64  `json:"experimentItemId"`
	JudgeConnectionID int64  `json:"judgeConnectionId"`
	JudgeIndex        int    `json:"judgeIndex"`
	IsIndependent     bool   `json:"isIndependent"`
	Dimension         string `json:"dimension"`
	// RawScore 为 nil 表示缺分（不是 0）。
	RawScore        *float64 `json:"rawScore"`
	NormalizedScore *float64 `json:"normalizedScore"`
	ScoreState      string   `json:"scoreState"`
	Rationale       string   `json:"rationale"`
	ErrorClass      string   `json:"errorClass"`
	SupersededBy    *int64   `json:"supersededBy,omitempty"`
}

// ---------------------------------------------------------------------------
// 独立性判定
// ---------------------------------------------------------------------------

// CheckJudgeIndependence 判定「至少一名独立裁判」是否成立。
//
// 判据（T14 验收项原文：「至少一名独立裁判，同真实来源的别名连接不得自评」）：
//   - 裁判的 endpoint 指纹与**该实验范围内全部生成来源**的指纹都不同 → 独立；
//   - 若实验含多个生成来源，则逐条给出「哪些来源由谁评」的覆盖情况，
//     因为「对 A 来源独立、对 B 来源不独立」的报告必须说清楚覆盖。
//
// 返回 (至少一名独立裁判成立, 逐来源的独立裁判清单)。
// 第二个返回值是 T14 验收项「若实验含多生成来源，逐条判断独立性并明确
// 有效评分覆盖」的落地点：界面据此显示「来源 X 只有 1 名独立裁判」。
func CheckJudgeIndependence(judges []JudgeSpec, sources []GeneratorSource) (bool, map[string][]int64) {
	// sourceFingerprint → 独立的裁判连接 ID
	coverage := map[string][]int64{}
	for _, source := range sources {
		independent := []int64{}
		for _, judge := range judges {
			if IsIndependentJudge(judge, source) {
				independent = append(independent, judge.ConnectionID)
			}
		}
		sort.Slice(independent, func(i, j int) bool { return independent[i] < independent[j] })
		coverage[source.EndpointFingerprint] = independent
	}

	// 「至少一名独立裁判」的判据：**存在**一个来源至少有一名独立裁判。
	// 注意这可能不足以支撑发布：若实验含多个来源，T20 的门槛会要求
	// 每个被纳入的来源都有独立裁判。这里只回答「能不能跑」，
	// 而「能不能发布」是 T20 的问题 —— 把两者混在一起会让创建实验
	// 因为一个尚未参与评估的来源而被拒。
	hasAny := false
	for _, independent := range coverage {
		if len(independent) > 0 {
			hasAny = true
			break
		}
	}
	return hasAny, coverage
}

// IsIndependentJudge 判断一名裁判对一个生成来源是否独立。
//
// 判据是 **endpoint 指纹不同**，而不是连接 ID 不同：
// 同一个人完全可以用两条 provider 记录指向同一个真实来源
// （例如「主账号」与「备用账号」都指向 api.example.com），
// 而那种情况下的「两个裁判」其实是自评。
//
// 指纹为空时的处置：生成来源没有指纹（旧数据/未声明）时**保守地判为不独立**。
// 反过来的取舍（判为独立）会让「独立性」在配置不完整时静默失效，
// 而失效的方向是「本该拦住的自评被放行」——那正是这条检查要防的。
func IsIndependentJudge(judge JudgeSpec, source GeneratorSource) bool {
	if judge.EndpointFingerprint == "" || source.EndpointFingerprint == "" {
		// 用连接 ID 做最后一道区分：至少不能是同一条连接。
		return judge.ConnectionID != source.ConnectionID && judge.EndpointFingerprint != source.EndpointFingerprint
	}
	return !strings.EqualFold(judge.EndpointFingerprint, source.EndpointFingerprint)
}

// ---------------------------------------------------------------------------
// 报告统计（§2.3、§3.1）
// ---------------------------------------------------------------------------

// AcceptanceRateOf 计算接纳率与展示文案（§2.3）。
//
// 零分母 → nil + 「无结论」。这一条是本文件最容易被「优化」掉、
// 而后果最严重的规则：显示 100% 会让一个没做任何检查的实验看起来可以发布。
//
// 放在 model 而不是 studio：它同时被实验统计与项目统计使用，
// 两处各写一遍必然有一天对不上（而两个页面显示不同接纳率是致命的）。
func AcceptanceRateOf(accepted, inspected int) (*float64, string) {
	if inspected <= 0 {
		return nil, "无结论"
	}
	rate := float64(accepted) / float64(inspected)
	return &rate, fmt.Sprintf("%.1f%%", rate*100)
}

// ExperimentStats 是实验报告的分列统计。
//
// 与 SampleStats 分开：样本统计面向「项目里有多少内容」，
// 实验统计面向「这一次检查覆盖了多少、结论的置信边界在哪」。
// 两者的分母不同（项目范围 vs 冻结范围），混用一个类型必然出错。
type ExperimentStats struct {
	// Inspected 是**冻结的分母**（实验创建时的样本版本数）。
	Inspected int `json:"inspected"`
	Scored    int `json:"scored"`
	Missing   int `json:"missing"`
	Error     int `json:"error"`
	// NotApplicable 是「该维度不适用」的项数。
	NotApplicable int `json:"notApplicable"`
	Pending       int `json:"pending"`

	// Coverage 是本实验的完成覆盖（scored / inspected），用于标注置信边界。
	Coverage float64 `json:"coverage"`
	// CoverageDisplay 是面向用户的覆盖文案（例如「87.5%（抽样结论，非全量）」）。
	CoverageDisplay string `json:"coverageDisplay"`

	// AcceptanceRate 的分子分母**必须**来自同一范围：
	// 接纳数 / 纳入检查数。分母为 0 时为 nil（无结论），**不是** 100%。
	AcceptanceRate        *float64 `json:"acceptanceRate"`
	AcceptanceRateDisplay string   `json:"acceptanceRateDisplay"`
}

// BuildExperimentStats 计算分列统计。
//
// 四条规则（每条都对应一个验收项或明确禁令）：
//
//  1. **分母固定**：inspected 由实验创建时冻结，不随后续筛选/隔离变化；
//  2. **缺分与错误不缩小分母**：它们与 scored 并列计数（§2.3
//     「隔离不缩小分母」的同类原则：任何排除都不该让分母变小）；
//  3. **零分母 → 无结论**：显示「无结论」而不是 100%；
//  4. **抽样必须标注范围与覆盖**：coverage 与它的文案是报告的一部分。
func BuildExperimentStats(inspected, scored, missing, errors, notApplicable, pending, accepted int) ExperimentStats {
	stats := ExperimentStats{
		Inspected: inspected, Scored: scored, Missing: missing, Error: errors,
		NotApplicable: notApplicable, Pending: pending,
	}
	if inspected > 0 {
		stats.Coverage = float64(scored) / float64(inspected)
	} else {
		stats.Coverage = 0
	}
	stats.CoverageDisplay = fmt.Sprintf("%.1f%%（分母为实验冻结的 %d 个样本版本）", stats.Coverage*100, inspected)
	if inspected == 0 {
		stats.CoverageDisplay = "无结论（实验范围为空）"
	}
	rate, display := AcceptanceRateOf(accepted, inspected)
	stats.AcceptanceRate = rate
	stats.AcceptanceRateDisplay = display
	return stats
}

// NormalizedScore 把一个原始分按量表范围映射到 0–1。
//
// 返回 ok=false 表示**无法归一化**（范围非法）。调用方必须据此把该格标成
// missing 而不是 0：把无法归一化的分当成 0 会把它算成最差分。
func NormalizedScore(raw float64, dimension RubricDimension) (float64, bool) {
	span := dimension.Max - dimension.Min
	if span <= 0 {
		return 0, false
	}
	normalized := (raw - dimension.Min) / span
	// 越界不静默夹紧：夹紧会把「量表配错了」这个事实藏起来。
	// 而是如实返回（可能 >1 或 <0），由报告的异常检查暴露。
	return normalized, true
}

// ExperimentCapabilities 派生实验能力位（契约 §4）。
//
// `canRerun` 的语义是「重跑会创建**新**实验」：修改配置或完整重评必须
// 新建实验（T14 验收项），因此这里只提供「复制为新实验」而不是「就地改」。
func ExperimentCapabilities(role, status string) Capabilities {
	canReview := role == ProjectRoleOwner || role == ProjectRoleReviewer
	return Capabilities{
		CanEdit:     false,
		CanRun:      role == ProjectRoleOwner && status != ExperimentStatusRunning,
		CanReview:   canReview,
		CanPublish:  false,
		CanDownload: role == ProjectRoleOwner || role == ProjectRoleReviewer || role == ProjectRoleViewer,
	}
}

// ExperimentRunnable 判断某个目标类型在当前阶段能否运行实验。
//
// GRPO 在 T24 接入前必须**明确不可运行**（T14 验收项原文）：
// 让一个 GRPO 实验跑起来但用 SFT 的量表与打分逻辑，会产出
// 「看起来正常、其实语义错误」的质量结论 —— 那比直接拒绝更危险。
func ExperimentRunnable(targetKind, phase string) (bool, string) {
	if targetKind == TargetKindGRPO {
		return false, "GRPO 质量适配器由 T24 交付，在此之前 GRPO 项目不能创建质量实验"
	}
	return true, ""
}
