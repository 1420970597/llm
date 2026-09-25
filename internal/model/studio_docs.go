package model

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// 本文件定义 Atelier 五类版本化文档的 typed payload（Issue #160 T04）。
//
// 为什么单独一个文件而不是塞进 internal/model/pipeline_v2.go：
// 后者是**第一轮冻结契约**（L1–L15）的落点，本轮的落点由
// docs/plans/atelier-implementation.md §6.3 指定为 internal/model/pipeline_v2.go，
// 但该文件已承载 ChainStandard/GrpoPrompt/SftRecord/ExportMapping 等在用类型。
// 把它们混在一起会让「哪一轮的契约」变得不可辨认，也会让本轮的改动
// 与旧类型的 diff 无法分开审阅。这里保留 §6.3 指定的**语义落点**
//（同一 model 包的 typed 文档 schema），但物理上分文件，
// 并在 atelier-implementation.md §1.1 记录该基线差异。
//
// 契约：docs/plans/atelier-implementation.md §5（蓝图节点配置规范）、
// docs/plans/atelier-api-contract.md §2.2。
//
// 设计要点：
//  1. 五类文档共享同一套版本与乐观锁语义（表结构见迁移 0024），
//     但 payload 各自 typed —— 契约 §5 明确「不能把所有节点做成同一表单」。
//  2. **禁止任意脚本节点**：这里的节点集合是闭合的，没有「自定义」入口。
//  3. 校验在服务端（本文件）而不是只在前端：前端的即时校验是体验，
//     服务端校验才是约束（P03/P04 的验收项明确要求「服务端也拒绝」）。
//  4. ContentHash 对**规范化 JSON** 计算：跨版本与跨环境必须得到同一 hash，
//     否则批次快照的「内容 hash 一致性」检查会误报。

// DocumentKind 是五类版本化文档的类型。
type DocumentKind string

const (
	KindBlueprint     DocumentKind = "blueprint"
	KindCoverage      DocumentKind = "coverage"
	KindStandard      DocumentKind = "standard"
	KindQualityPolicy DocumentKind = "quality_policy"
	KindMapping       DocumentKind = "mapping"
)

// AllDocumentKinds 是全部合法类型，顺序与蓝图节点顺序一致。
func AllDocumentKinds() []DocumentKind {
	return []DocumentKind{KindCoverage, KindStandard, KindBlueprint, KindQualityPolicy, KindMapping}
}

// IsValidDocumentKind 判断类型是否合法。
func IsValidDocumentKind(kind DocumentKind) bool {
	for _, candidate := range AllDocumentKinds() {
		if candidate == kind {
			return true
		}
	}
	return false
}

// schemaVersionFor 返回某类文档当前的 schema 版本号。
//
// 与类型绑定（不是自由文本）：契约 §2.2 要求 payload 的 schema_version
// 与对象类型一致，否则「同一份 payload 被两类文档都接受」会掩盖类型错误。
func schemaVersionFor(kind DocumentKind) string {
	switch kind {
	case KindBlueprint:
		return "blueprint.v1"
	case KindCoverage:
		return "coverage.v1"
	case KindStandard:
		return "standard.v1"
	case KindQualityPolicy:
		return "quality_policy.v1"
	case KindMapping:
		return "mapping.v1"
	default:
		return ""
	}
}

// SchemaVersionFor 是 schemaVersionFor 的导出形式（handler 需要回显它）。
func SchemaVersionFor(kind DocumentKind) string { return schemaVersionFor(kind) }

// ---------------------------------------------------------------------------
// 蓝图（blueprint.v1）
// ---------------------------------------------------------------------------

// 蓝图节点 key。集合闭合：没有「任意脚本节点」（契约 §5 明确禁止）。
const (
	BlueprintNodeCoverage    = "coverage"
	BlueprintNodeStandard    = "standard"
	BlueprintNodeGeneration  = "generation"
	BlueprintNodeEvaluation  = "evaluation"
	BlueprintNodeRules       = "rules"
	BlueprintNodeHumanReview = "human_review"
	BlueprintNodeDelivery    = "delivery"
)

// BlueprintNodeKeys 是全部节点 key，顺序即画布顺序（§5 的表格顺序）。
//
// 顺序有意义：前端按它渲染左侧画布，而「视觉顺序与 Tab 顺序一致」
// 是 D02/蓝图页的可访问性要求。
func BlueprintNodeKeys() []string {
	return []string{
		BlueprintNodeCoverage,
		BlueprintNodeStandard,
		BlueprintNodeGeneration,
		BlueprintNodeEvaluation,
		BlueprintNodeRules,
		BlueprintNodeHumanReview,
		BlueprintNodeDelivery,
	}
}

// 生成并发上限。契约 §5 与 §2.2 都是 1–32。
const (
	MinGenerationConcurrency = 1
	MaxGenerationConcurrency = 32
)

// GenerationMinTokens 是输出上限的下界：0 表示「用连接默认」，非 0 时必须有意义。
const GenerationMinTokens = 64

// BlueprintCoverageNode 引用覆盖版本。
type BlueprintCoverageNode struct {
	CoverageVersionID int64 `json:"coverageVersionId"`
}

// BlueprintStandardNode 引用思维标准版本。
type BlueprintStandardNode struct {
	StandardVersionID int64 `json:"standardVersionId"`
	// Steps 是**草稿期**的内联步骤（保存为新标准版本前允许内联编辑）。
	// 一旦 standardVersionId 非零，服务端以被引用版本为准；
	// 两者都为空是允许的（草稿允许尚未配标准），但保存批次前会被拦下（T12）。
	Steps []StandardStep `json:"steps,omitempty"`
}

// BlueprintGenerationNode 是生成节点。
//
// modelConnectionId 是**非秘密标识**：契约 §2.2 与 §2.4 明确
// 「版本 payload 不含密钥」「凭证单独取，不能冻结明文密钥」。
type BlueprintGenerationNode struct {
	ModelConnectionID int64  `json:"modelConnectionId"`
	ModelVersion      string `json:"modelVersion"`
	SchemaVersion     string `json:"schemaVersion"`
	Concurrency       int    `json:"concurrency"`
	MaxTokens         int    `json:"maxTokens"`
	// Temperature 用指针：契约 §5 要求「模型参数默认取能力声明，不一律允许
	// temperature」。指针让它能表达「未设置」，而 0 是一个**合法且常见**的取值，
	// 用 float64 + 零值判断会把「用户设了 0」误判为「用户没设」。
	Temperature   *float64 `json:"temperature"`
	FailurePolicy string   `json:"failurePolicy"`
	// JSONSchema 是结构化输出的字段约束（可为空 = 用 schemaVersion 的默认约束）。
	JSONSchema map[string]any `json:"jsonSchema,omitempty"`
}

// BlueprintEvaluationNode 是独立评估节点。
//
// 契约 §5 要求「至少一名独立裁判」。独立性检查需要知道「生成者是谁」，
// 因此这里显式要求 judgeConnectionIds 非空；「同一真实来源的别名连接不得自评」
// 的完整判定在 T14（需要连接的 endpoint 指纹，属于连接读模型）。
type BlueprintEvaluationNode struct {
	JudgeConnectionIDs []int64 `json:"judgeConnectionIds"`
	RubricVersionID    int64   `json:"rubricVersionId"`
	SamplingSeed       int64   `json:"samplingSeed"`
	// Weights 是维度权重，总和必须为 1（§5「权重和=1」）。
	Weights map[string]float64 `json:"weights,omitempty"`
	// MissingScorePolicy 是缺分策略（§2.6：缺分与真实 0 分区分）。
	MissingScorePolicy string `json:"missingScorePolicy"`
}

// BlueprintRulesNode 引用质量策略版本。
type BlueprintRulesNode struct {
	QualityPolicyVersionID int64 `json:"qualityPolicyVersionId"`
}

// BlueprintHumanReviewNode 是人工检查点。
type BlueprintHumanReviewNode struct {
	Assignment string `json:"assignment"`
	// RequiredEvidence 是必需证据集（§4.3 的 evidence_revision 由它定义）。
	RequiredEvidence []string `json:"requiredEvidence"`
	RiskScope        string   `json:"riskScope"`
	// SampleRate 是抽检比例，0–1。
	SampleRate *float64 `json:"sampleRate"`
}

// BlueprintDeliveryNode 是版本交付节点。
type BlueprintDeliveryNode struct {
	MappingVersionID int64    `json:"mappingVersionId"`
	Format           string   `json:"format"`
	IntendedUse      string   `json:"intendedUse"`
	Limitations      []string `json:"limitations,omitempty"`
}

// BlueprintPayload 是 blueprint.v1 的完整 payload。
//
// 用值类型（不是指针）表示「节点存在但内容为空」是合法的草稿状态：
// 用户可以在只填了目标的情况下保存蓝图 v1，再逐步补节点。
// 「执行前必须配齐」是 T12/T13 的前置检查，不是保存版本的约束。
type BlueprintPayload struct {
	SchemaVersion string `json:"schemaVersion"`
	Nodes         struct {
		Coverage    BlueprintCoverageNode    `json:"coverage"`
		Standard    BlueprintStandardNode    `json:"standard"`
		Generation  BlueprintGenerationNode  `json:"generation"`
		Evaluation  BlueprintEvaluationNode  `json:"evaluation"`
		Rules       BlueprintRulesNode       `json:"rules"`
		HumanReview BlueprintHumanReviewNode `json:"humanReview"`
		Delivery    BlueprintDeliveryNode    `json:"delivery"`
	} `json:"nodes"`
}

// ---------------------------------------------------------------------------
// 覆盖（coverage.v1）
// ---------------------------------------------------------------------------

// DifficultyRatio 是难度配比的一项。
type DifficultyRatio struct {
	Difficulty string  `json:"difficulty"`
	Ratio      float64 `json:"ratio"`
}

// CoverageDirection 是覆盖计划中的一个方向。
//
// StableID 是契约 §4.1 要求的「稳定领域/方向 ID」：
// 它不是数据库自增 ID，而是用户在版本里持续沿用的标识（如 slug），
// 这样「删除草稿方向不破坏被引用的版本」（T04 验收项）—— 版本引用的是稳定 ID，
// 不是行号或数组下标。
type CoverageDirection struct {
	StableID         string            `json:"stableId"`
	Name             string            `json:"name"`
	Quota            int               `json:"quota"`
	DifficultyRatios []DifficultyRatio `json:"difficultyRatios,omitempty"`
	Source           string            `json:"source"`
}

// CoverageDomain 是覆盖计划中的一个领域。
type CoverageDomain struct {
	StableID   string              `json:"stableId"`
	Name       string              `json:"name"`
	Directions []CoverageDirection `json:"directions"`
}

// CoveragePayload 是 coverage.v1 的完整 payload。
type CoveragePayload struct {
	SchemaVersion string           `json:"schemaVersion"`
	Domains       []CoverageDomain `json:"domains"`
}

// CoverageUnit 是覆盖版本展开出的一个**确定性**单元。
//
// 它属于 model 包而不是 studio 包（issue #190 的根因修复）：
// 「一个覆盖方案最多能产出多少单元」必须在**保存/启动前**就能算出并拒绝，
// 而不是等 runner 跑到最后才发现只产出了计划量的一小部分。
// 计算放在这里，studio 的单元分配与批次的容量校验共用同一份实现 ——
// 两份实现会让「界面说能产 12、实际产 1」重新变成可能。
type CoverageUnit struct {
	DomainStableID    string
	DirectionStableID string
	DomainName        string
	DirectionName     string
	// Ordinal 是该方向内的第几个单元（从 1 开始）。
	Ordinal int
	// Quota 是该方向的配额（用于进度分列展示，不用于编造总体百分比）。
	Quota int
}

// CoverageCapacity 返回该覆盖版本最多能产出的单元数。
//
// 这是 issue #190 的权威口径：`plannedUnits` 与它比较，而不是与用户填的数字比较。
// quota ≤ 0 视为 1，与 AllocateCoverageUnits 保持一致（否则「有名字但永远不产出」
// 的方向在容量里消失，而界面上它还在）。
func CoverageCapacity(coverage CoveragePayload) int {
	total := 0
	for _, domain := range coverage.Domains {
		for _, direction := range domain.Directions {
			quota := direction.Quota
			if quota <= 0 {
				quota = 1
			}
			total += quota
		}
	}
	return total
}

// AllocateCoverageUnits 按覆盖配额展开单元，最多 limit 个。
//
// 顺序：先按领域顺序，再按方向顺序，再按方向内 ordinal。刻意是**纯函数**：
// 批次的 item_key 必须稳定，否则「恢复失败项」会算出另一批键并重跑已完成的工作。
func AllocateCoverageUnits(coverage CoveragePayload, limit int) []CoverageUnit {
	if limit <= 0 {
		return nil
	}
	units := make([]CoverageUnit, 0, limit)
	for _, domain := range coverage.Domains {
		for _, direction := range domain.Directions {
			quota := direction.Quota
			if quota <= 0 {
				quota = 1
			}
			for ordinal := 1; ordinal <= quota; ordinal++ {
				if len(units) >= limit {
					return units
				}
				units = append(units, CoverageUnit{
					DomainStableID:    domain.StableID,
					DirectionStableID: direction.StableID,
					DomainName:        domain.Name,
					DirectionName:     direction.Name,
					Ordinal:           ordinal,
					Quota:             quota,
				})
			}
		}
	}
	return units
}

// ---------------------------------------------------------------------------
// 思维标准（standard.v1）
// ---------------------------------------------------------------------------

// StandardStep 是一个检查步骤。
//
// Checkpoint 是契约 §5 要求的「每步有检查点」：没有检查点的步骤无法被验证，
// 而「只有认真思考这种不可验证的提示」正是 #159 要消除的形态。
type StandardStep struct {
	ID         string `json:"id"`
	Title      string `json:"title"`
	Detail     string `json:"detail"`
	Checkpoint string `json:"checkpoint"`
	Order      int    `json:"order"`
}

// StandardPayload 是 standard.v1 的完整 payload。
type StandardPayload struct {
	SchemaVersion string         `json:"schemaVersion"`
	Steps         []StandardStep `json:"steps"`
}

// ---------------------------------------------------------------------------
// 质量策略（quality_policy.v1）
// ---------------------------------------------------------------------------

// 规则匹配方式。集合闭合：不接受任意脚本（契约 §5 明确「禁止任意脚本节点」）。
const (
	RuleMatchContains   = "contains"
	RuleMatchRegex      = "regex"
	RuleMatchFieldCheck = "field_check"
	RuleMatchStructure  = "structure"
)

// 规则严重度。
const (
	RuleSeverityInfo    = "info"
	RuleSeverityWarning = "warning"
	RuleSeverityError   = "error"
)

// 规则命中后的建议动作。
//
// 注意这里没有「自动隔离」：契约 §5 与 T15 要求「默认入审阅」「规则命中不替人工处置」。
// 允许的值只能是「建议」，不是「自动执行」。
const (
	RuleActionReview            = "review"
	RuleActionSuggestQuarantine = "suggest_quarantine"
)

// QualityRule 是一条版本化规则。
type QualityRule struct {
	ID              string `json:"id"`
	Name            string `json:"name"`
	MatchType       string `json:"matchType"`
	Expression      string `json:"expression"`
	Field           string `json:"field"`
	Severity        string `json:"severity"`
	SuggestedAction string `json:"suggestedAction"`
	// Keywords 是关键词内容快照（T15：规则版本包含关键词内容快照，
	// 使历史命中保留当时的表达式与关键词，而不是跟随关键词表漂移）。
	Keywords []string `json:"keywords,omitempty"`
}

// QualityPolicyPayload 是 quality_policy.v1 的完整 payload。
type QualityPolicyPayload struct {
	SchemaVersion string        `json:"schemaVersion"`
	Rules         []QualityRule `json:"rules"`
}

// ---------------------------------------------------------------------------
// 字段映射（mapping.v1）
// ---------------------------------------------------------------------------

// MappingField 是一条字段映射。
type MappingField struct {
	TargetField string `json:"targetField"`
	SourceField string `json:"sourceField"`
	Required    bool   `json:"required"`
}

// MappingPayload 是 mapping.v1 的完整 payload。
type MappingPayload struct {
	SchemaVersion string         `json:"schemaVersion"`
	Format        string         `json:"format"`
	Fields        []MappingField `json:"fields"`
}

// ---------------------------------------------------------------------------
// 校验
// ---------------------------------------------------------------------------

// maxRegexLength 限制正则表达式的长度。
//
// 契约 T15 要求「Go regexp/输入长度/计算上限由服务端控制，本地校验仅辅助」。
// 长度限制是第一道：超长表达式配合回溯会让正则匹配变成 DoS 入口。
const maxRegexLength = 500

// ValidateBlueprintPayload 校验蓝图 payload。
//
// 校验范围是契约 §2.2 明列的三类「非法返回 422 + fieldErrors」：
// 并发越界、配比总和不等于 1、schema 不合法。节点依赖（引用的连接必须存在）
// 需要连接读模型，属于 T07/T11 的执行前检查，不在这里假装已检查。
func ValidateBlueprintPayload(payload BlueprintPayload) error {
	var errs FieldErrors

	if payload.SchemaVersion != schemaVersionFor(KindBlueprint) {
		errs = append(errs, FieldError{
			Field:   "schemaVersion",
			Message: fmt.Sprintf("必须为 %s", schemaVersionFor(KindBlueprint)),
		})
	}

	generation := payload.Nodes.Generation
	if generation.Concurrency < MinGenerationConcurrency || generation.Concurrency > MaxGenerationConcurrency {
		errs = append(errs, FieldError{
			Field: "nodes.generation.concurrency",
			Message: fmt.Sprintf("必须在 %d–%d 之间",
				MinGenerationConcurrency, MaxGenerationConcurrency),
		})
	}
	if generation.MaxTokens < 0 || (generation.MaxTokens > 0 && generation.MaxTokens < GenerationMinTokens) {
		errs = append(errs, FieldError{
			Field:   "nodes.generation.maxTokens",
			Message: fmt.Sprintf("必须为 0（用连接默认）或不小于 %d", GenerationMinTokens),
		})
	}
	if generation.Temperature != nil && (*generation.Temperature < 0 || *generation.Temperature > 2) {
		errs = append(errs, FieldError{
			Field:   "nodes.generation.temperature",
			Message: "必须在 0–2 之间",
		})
	}
	if generation.SchemaVersion != "" && generation.SchemaVersion != SampleSchemaSFT &&
		generation.SchemaVersion != SampleSchemaGRPO {
		errs = append(errs, FieldError{
			Field: "nodes.generation.schemaVersion",
			Message: fmt.Sprintf("只能是 %s 或 %s（由项目目标类型决定）",
				SampleSchemaSFT, SampleSchemaGRPO),
		})
	}
	// 模型连接是非秘密标识，但不允许为负。
	if generation.ModelConnectionID < 0 {
		errs = append(errs, FieldError{Field: "nodes.generation.modelConnectionId", Message: "取值不合法"})
	}

	// 评估节点：契约要求至少一名独立裁判；权重和必须为 1。
	evaluation := payload.Nodes.Evaluation
	seenJudge := map[int64]bool{}
	duplicateJudge := false
	for _, id := range evaluation.JudgeConnectionIDs {
		if id <= 0 {
			errs = append(errs, FieldError{
				Field: "nodes.evaluation.judgeConnectionIds", Message: "包含无效的连接标识"})
			break
		}
		if seenJudge[id] {
			duplicateJudge = true
		}
		seenJudge[id] = true
	}
	if duplicateJudge {
		// 重复的裁判会让权重与分歧统计重复计数，属于数据错误而不是冗余。
		errs = append(errs, FieldError{
			Field: "nodes.evaluation.judgeConnectionIds", Message: "不能包含重复的裁判连接"})
	}
	if len(evaluation.Weights) > 0 {
		sum := 0.0
		for _, weight := range evaluation.Weights {
			if weight < 0 {
				errs = append(errs, FieldError{
					Field: "nodes.evaluation.weights", Message: "权重不能为负数"})
				break
			}
			sum += weight
		}
		// 浮点比较用容差：用户在前端输入 0.1+0.2+0.7 时二进制表示不是精确的 1.0，
		// 用 == 1 会拒绝一个用户认为正确的输入。
		if sum < 0.999 || sum > 1.001 {
			errs = append(errs, FieldError{
				Field:   "nodes.evaluation.weights",
				Message: fmt.Sprintf("权重之和必须为 1（当前 %.3f）", sum),
			})
		}
	}
	switch evaluation.MissingScorePolicy {
	case "", "exclude", "zero", "not_applicable":
	default:
		errs = append(errs, FieldError{
			Field:   "nodes.evaluation.missingScorePolicy",
			Message: "只能是 exclude、zero 或 not_applicable",
		})
	}

	// 人工检查点：抽检比例 0–1。
	if rate := payload.Nodes.HumanReview.SampleRate; rate != nil && (*rate < 0 || *rate > 1) {
		errs = append(errs, FieldError{
			Field: "nodes.humanReview.sampleRate", Message: "必须在 0–1 之间"})
	}

	// 交付节点：格式必须在映射支持范围内（真正的「是否支持该类型」检查在 T20/T21，
	// 这里拒绝明显不可能的取值，避免存下一个永远发不出的格式）。
	delivery := payload.Nodes.Delivery
	if delivery.Format != "" && !IsKnownExportFormat(delivery.Format) {
		errs = append(errs, FieldError{
			Field:   "nodes.delivery.format",
			Message: fmt.Sprintf("只能是 %s", strings.Join(KnownExportFormats(), "、")),
		})
	}

	if len(errs) > 0 {
		return errs
	}
	return nil
}

// ValidateCoveragePayload 校验覆盖 payload（契约 §5 与 T04 验收项）：
// 稳定 ID 非空且不重复、配额为正、难度配比总和为 100%。
func ValidateCoveragePayload(payload CoveragePayload) error {
	var errs FieldErrors

	if payload.SchemaVersion != schemaVersionFor(KindCoverage) {
		errs = append(errs, FieldError{
			Field:   "schemaVersion",
			Message: fmt.Sprintf("必须为 %s", schemaVersionFor(KindCoverage)),
		})
	}

	seenDomain := map[string]bool{}
	for index, domain := range payload.Domains {
		field := fmt.Sprintf("domains[%d]", index)
		stableID := strings.TrimSpace(domain.StableID)
		if stableID == "" {
			errs = append(errs, FieldError{Field: field + ".stableId", Message: "必填（稳定 ID 供版本引用）"})
		} else if seenDomain[stableID] {
			errs = append(errs, FieldError{Field: field + ".stableId", Message: "不能与其它领域重复"})
		}
		seenDomain[stableID] = true
		if strings.TrimSpace(domain.Name) == "" {
			errs = append(errs, FieldError{Field: field + ".name", Message: "必填"})
		}

		seenDirection := map[string]bool{}
		for directionIndex, direction := range domain.Directions {
			directionField := fmt.Sprintf("%s.directions[%d]", field, directionIndex)
			directionID := strings.TrimSpace(direction.StableID)
			if directionID == "" {
				errs = append(errs, FieldError{Field: directionField + ".stableId", Message: "必填"})
			} else if seenDirection[directionID] {
				errs = append(errs, FieldError{Field: directionField + ".stableId", Message: "不能与同一领域内其它方向重复"})
			}
			seenDirection[directionID] = true
			if direction.Quota < 1 {
				errs = append(errs, FieldError{Field: directionField + ".quota", Message: "必须大于等于 1"})
			}
			if err := validateDifficultyRatios(directionField, direction.DifficultyRatios); err != nil {
				errs = append(errs, err.(FieldErrors)...)
			}
		}
	}

	if len(errs) > 0 {
		return errs
	}
	return nil
}

// validateDifficultyRatios 校验一组难度配比：名称非空、比例为正、总和为 100%。
//
// 契约 §5 原文是「配比和必须为 100%」。用百分比而不是 0–1 的小数：
// 用户在界面上填的是「简单 30%、中等 50%、困难 20%」，而 0.3+0.5+0.2
// 这类小数在浮点下经常不等于 1.0 —— 要求 100 并留 0.01 容差更贴近输入方式。
func validateDifficultyRatios(parentField string, ratios []DifficultyRatio) error {
	if len(ratios) == 0 {
		// 空配比合法：表示「沿用默认难度分布」，不是错误。
		return nil
	}
	var errs FieldErrors
	sum := 0.0
	seen := map[string]bool{}
	for index, ratio := range ratios {
		field := fmt.Sprintf("%s.difficultyRatios[%d]", parentField, index)
		name := strings.TrimSpace(ratio.Difficulty)
		if name == "" {
			errs = append(errs, FieldError{Field: field + ".difficulty", Message: "必填"})
		} else if seen[strings.ToLower(name)] {
			errs = append(errs, FieldError{Field: field + ".difficulty", Message: "不能重复"})
		}
		seen[strings.ToLower(name)] = true
		if ratio.Ratio < 0 {
			errs = append(errs, FieldError{Field: field + ".ratio", Message: "不能为负数"})
		}
		sum += ratio.Ratio
	}
	if sum < 99.99 || sum > 100.01 {
		errs = append(errs, FieldError{
			Field:   parentField + ".difficultyRatios",
			Message: fmt.Sprintf("配比之和必须为 100（当前 %.2f）", sum),
		})
	}
	if len(errs) > 0 {
		return errs
	}
	return nil
}

// ValidateStandardPayload 校验标准 payload（契约 §5「步骤可排序，每步有检查点」）。
func ValidateStandardPayload(payload StandardPayload) error {
	var errs FieldErrors

	if payload.SchemaVersion != schemaVersionFor(KindStandard) {
		errs = append(errs, FieldError{
			Field:   "schemaVersion",
			Message: fmt.Sprintf("必须为 %s", schemaVersionFor(KindStandard)),
		})
	}
	if len(payload.Steps) == 0 {
		errs = append(errs, FieldError{Field: "steps", Message: "至少需要一步（标准步骤不能为空）"})
	}

	seenID := map[string]bool{}
	seenOrder := map[int]bool{}
	for index, step := range payload.Steps {
		field := fmt.Sprintf("steps[%d]", index)
		id := strings.TrimSpace(step.ID)
		if id == "" {
			errs = append(errs, FieldError{Field: field + ".id", Message: "必填"})
		} else if seenID[id] {
			errs = append(errs, FieldError{Field: field + ".id", Message: "不能重复"})
		}
		seenID[id] = true

		if strings.TrimSpace(step.Title) == "" {
			errs = append(errs, FieldError{Field: field + ".title", Message: "必填"})
		}
		checkpoint := strings.TrimSpace(step.Checkpoint)
		if checkpoint == "" {
			errs = append(errs, FieldError{
				Field: field + ".checkpoint", Message: "必填（没有检查点的步骤无法被验证）"})
		}
		if seenOrder[step.Order] {
			errs = append(errs, FieldError{Field: field + ".order", Message: "排序值不能重复"})
		}
		seenOrder[step.Order] = true
	}

	if len(errs) > 0 {
		return errs
	}
	return nil
}

// ValidateQualityPolicyPayload 校验质量策略 payload（T15：表达式、字段、严重度、建议动作）。
func ValidateQualityPolicyPayload(payload QualityPolicyPayload) error {
	var errs FieldErrors

	if payload.SchemaVersion != schemaVersionFor(KindQualityPolicy) {
		errs = append(errs, FieldError{
			Field:   "schemaVersion",
			Message: fmt.Sprintf("必须为 %s", schemaVersionFor(KindQualityPolicy)),
		})
	}

	seenID := map[string]bool{}
	for index, rule := range payload.Rules {
		field := fmt.Sprintf("rules[%d]", index)
		id := strings.TrimSpace(rule.ID)
		if id == "" {
			errs = append(errs, FieldError{Field: field + ".id", Message: "必填"})
		} else if seenID[id] {
			errs = append(errs, FieldError{Field: field + ".id", Message: "不能重复"})
		}
		seenID[id] = true

		if strings.TrimSpace(rule.Name) == "" {
			errs = append(errs, FieldError{Field: field + ".name", Message: "必填"})
		}
		switch rule.MatchType {
		case RuleMatchContains, RuleMatchRegex, RuleMatchFieldCheck, RuleMatchStructure:
		default:
			errs = append(errs, FieldError{
				Field: field + ".matchType",
				Message: fmt.Sprintf("只能是 %s、%s、%s 或 %s",
					RuleMatchContains, RuleMatchRegex, RuleMatchFieldCheck, RuleMatchStructure),
			})
		}

		// 正则**必须在这里编译一次**：非法 regex 不能落库（T15 验收项）。
		// 只在前端校验不够 —— 直接调 API 的调用方会绕过它。
		if rule.MatchType == RuleMatchRegex {
			expression := rule.Expression
			if strings.TrimSpace(expression) == "" {
				errs = append(errs, FieldError{Field: field + ".expression", Message: "正则规则必须填写表达式"})
			} else if len(expression) > maxRegexLength {
				errs = append(errs, FieldError{
					Field:   field + ".expression",
					Message: fmt.Sprintf("表达式长度不能超过 %d 个字符", maxRegexLength),
				})
			} else if _, err := regexp.Compile(expression); err != nil {
				// 回显编译错误会暴露 Go regexp 的英文消息，但它对用户修正表达式
				// 直接有用（指出哪个位置出问题）。这里只回显不含内部路径的部分。
				errs = append(errs, FieldError{
					Field:   field + ".expression",
					Message: "不是合法的正则表达式，请检查括号与转义：" + sanitizeRegexError(err),
				})
			}
		} else if rule.Expression == "" && rule.MatchType == RuleMatchContains && len(rule.Keywords) == 0 {
			errs = append(errs, FieldError{
				Field:   field + ".keywords",
				Message: "包含匹配需要至少一个关键词或表达式",
			})
		}

		if strings.TrimSpace(rule.Field) == "" {
			errs = append(errs, FieldError{Field: field + ".field", Message: "必填（要检查哪个字段）"})
		}
		switch rule.Severity {
		case RuleSeverityInfo, RuleSeverityWarning, RuleSeverityError:
		default:
			errs = append(errs, FieldError{
				Field: field + ".severity",
				Message: fmt.Sprintf("只能是 %s、%s 或 %s",
					RuleSeverityInfo, RuleSeverityWarning, RuleSeverityError),
			})
		}
		// 只允许「建议」类动作：规则命中不能自动替人工处置（契约 §5、T15）。
		switch rule.SuggestedAction {
		case RuleActionReview, RuleActionSuggestQuarantine:
		default:
			errs = append(errs, FieldError{
				Field: field + ".suggestedAction",
				Message: fmt.Sprintf("只能是 %s 或 %s（规则命中不能自动替人工处置）",
					RuleActionReview, RuleActionSuggestQuarantine),
			})
		}
	}

	if len(errs) > 0 {
		return errs
	}
	return nil
}

// sanitizeRegexError 提取 regexp 错误里对用户有用的部分。
//
// 为什么需要它：`regexp.Compile` 的错误是 `error parsing regexp: missing closing ):
// `(a` 这种形式 —— 含英文技术细节但不含仓库路径或密钥。这里只做长度收敛，
// 避免超长输入（500 字符的表达式）产生同样长的错误消息回给用户。
func sanitizeRegexError(err error) string {
	message := err.Error()
	message = strings.TrimPrefix(message, "error parsing regexp: ")
	if len([]rune(message)) > 200 {
		message = string([]rune(message)[:200])
	}
	return message
}

// ValidateMappingPayload 校验映射 payload。
func ValidateMappingPayload(payload MappingPayload) error {
	var errs FieldErrors

	if payload.SchemaVersion != schemaVersionFor(KindMapping) {
		errs = append(errs, FieldError{
			Field:   "schemaVersion",
			Message: fmt.Sprintf("必须为 %s", schemaVersionFor(KindMapping)),
		})
	}
	if payload.Format != "" && !IsKnownExportFormat(payload.Format) {
		errs = append(errs, FieldError{
			Field:   "format",
			Message: fmt.Sprintf("只能是 %s", strings.Join(KnownExportFormats(), "、")),
		})
	}

	seenTarget := map[string]bool{}
	for index, field := range payload.Fields {
		fieldPath := fmt.Sprintf("fields[%d]", index)
		target := strings.TrimSpace(field.TargetField)
		if target == "" {
			errs = append(errs, FieldError{Field: fieldPath + ".targetField", Message: "必填"})
		} else if seenTarget[target] {
			// 同一目标字段被映射两次会产生「后者覆盖前者」的静默行为，
			// 而用户看到的是「我配了两条，只有一条生效」。
			errs = append(errs, FieldError{Field: fieldPath + ".targetField", Message: "不能重复映射同一目标字段"})
		}
		seenTarget[target] = true
		if strings.TrimSpace(field.SourceField) == "" {
			errs = append(errs, FieldError{Field: fieldPath + ".sourceField", Message: "必填"})
		}
	}

	if len(errs) > 0 {
		return errs
	}
	return nil
}

// ---------------------------------------------------------------------------
// 规范化与内容 hash
// ---------------------------------------------------------------------------

// CanonicalJSON 把任意 payload 序列化成**确定**的字节序列。
//
// 为什么不能直接用 json.Marshal：Go 的 json.Marshal 对 map 按 key 排序
// （这一点是确定的），但对**结构体字段顺序**按定义顺序输出 —— 那也没问题。
// 真正的问题是 JSONB 往返：从库里读回来再序列化时，数字会变成 float64，
// 于是 `1` 变成 `1`（float64 的整数仍输出为 1）但 `1.0` 会变成 `1`。
// 因此 hash 必须在**写入时**对原始字节计算一次并存储，
// 读取时使用存储的 hash，而不是重新计算（那会随编解码细节漂移）。
//
// 本函数用于写入路径：它保证同一份逻辑内容产生同一字节序列。
// 实现上通过 `json.Marshal` + 二次解码为 `any` 再 Marshal 来获得
// 「键有序、无多余空白」的表示。
func CanonicalJSON(payload any) ([]byte, error) {
	raw, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	// 二次规范化：把 map 键排序、消除数字表示差异。
	var generic any
	if err := json.Unmarshal(raw, &generic); err != nil {
		return nil, err
	}
	return marshalCanonical(generic)
}

// marshalCanonical 递归输出确定性 JSON。
//
// 手写而不是依赖 `json.Marshal` 的 map 排序：`json.Marshal` 会**转义 HTML 字符**
// （`<` → `\u003c`），而转义规则可能随 Go 版本调整；手写能让 hash 的定义
// 完全落在本仓库里。这一点对「批次快照的 hash 一致性检查」是必要的 ——
// hash 算法不能被上游库的默认值悄悄改变。
func marshalCanonical(value any) ([]byte, error) {
	var builder strings.Builder
	if err := writeCanonical(&builder, value); err != nil {
		return nil, err
	}
	return []byte(builder.String()), nil
}

func writeCanonical(builder *strings.Builder, value any) error {
	switch typed := value.(type) {
	case nil:
		builder.WriteString("null")
	case bool:
		if typed {
			builder.WriteString("true")
		} else {
			builder.WriteString("false")
		}
	case float64:
		// 整数形式的浮点输出成整数：`1.0` 与 `1` 必须得到同一 hash，
		// 否则用户在界面上把 1 改成 1.0 会被判定为「内容变了」。
		if typed == float64(int64(typed)) {
			builder.WriteString(fmt.Sprintf("%d", int64(typed)))
		} else {
			builder.WriteString(strings.TrimRight(strings.TrimRight(
				fmt.Sprintf("%.10f", typed), "0"), "."))
		}
	case string:
		encoded, err := json.Marshal(typed)
		if err != nil {
			return err
		}
		// json.Marshal 会转义 <、>、& 为 \u003c 等；这里替换回字面量，
		// 让 hash 不受 HTML 转义策略影响。
		encoded = []byte(strings.NewReplacer(
			`\u003c`, "<", `\u003e`, ">", `\u0026`, "&",
		).Replace(string(encoded)))
		builder.Write(encoded)
	case []any:
		builder.WriteByte('[')
		for index, item := range typed {
			if index > 0 {
				builder.WriteByte(',')
			}
			if err := writeCanonical(builder, item); err != nil {
				return err
			}
		}
		builder.WriteByte(']')
	case map[string]any:
		keys := make([]string, 0, len(typed))
		for key := range typed {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		builder.WriteByte('{')
		for index, key := range keys {
			if index > 0 {
				builder.WriteByte(',')
			}
			encodedKey, err := json.Marshal(key)
			if err != nil {
				return err
			}
			builder.Write(encodedKey)
			builder.WriteByte(':')
			if err := writeCanonical(builder, typed[key]); err != nil {
				return err
			}
		}
		builder.WriteByte('}')
	default:
		// 其余类型（json.Number、[]byte 等）走一次 Marshal + Unmarshal 收敛。
		raw, err := json.Marshal(typed)
		if err != nil {
			return err
		}
		var generic any
		if err := json.Unmarshal(raw, &generic); err != nil {
			return err
		}
		return writeCanonical(builder, generic)
	}
	return nil
}

// ContentHash 计算 payload 的内容 hash（规范化 JSON 的 SHA-256 十六进制）。
//
// 为什么用 SHA-256 而不是短 hash：它的用途之一是让批次快照能发现
// 「引用的内容不再一致」。碰撞意味着一次静默的内容替换，而这正是要防的。
func ContentHash(payload any) (string, error) {
	canonical, err := CanonicalJSON(payload)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(canonical)
	return hex.EncodeToString(sum[:]), nil
}

// ---------------------------------------------------------------------------
// 导出格式
// ---------------------------------------------------------------------------

// 本轮承诺的导出格式（#160 §1：SFT JSONL/CSV/Alpaca 与 GRPO JSONL）。
//
// 与 internal/exporter 的 canonicalFormats（jsonl/csv/parquet/alpaca/sharegpt）
// 的关系：这里是**新文档允许出现的集合**，是那份清单的子集，理由逐条如下：
//
//   - `sharegpt` 保留兼容（旧数据集仍在用），但不出现在新的默认映射里；
//   - `parquet` **有意排除**：internal/exporter/parquet.go 的 IsRealParquet=false，
//     它写出来的是列式 JSONL。允许它出现在新蓝图/映射里会让用户以为
//     拿到的是真 Parquet，而 #160 §1 明确「本轮只承诺 SFT JSONL/CSV/Alpaca
//     与 GRPO JSONL；真 Parquet 非本轮必需」。等它变成真 Parquet 再放进来。
//
// 这两个常量名带 Studio 前缀，避免与 internal/exporter 里同名的格式常量
// 在将来被误当作同一件事。
const (
	ExportFormatJSONL    = "jsonl"
	ExportFormatCSV      = "csv"
	ExportFormatAlpaca   = "alpaca"
	ExportFormatShareGPT = "sharegpt"
)

// KnownExportFormats 返回新文档允许的格式，按展示顺序。
func KnownExportFormats() []string {
	return []string{ExportFormatJSONL, ExportFormatCSV, ExportFormatAlpaca, ExportFormatShareGPT}
}

// IsKnownExportFormat 判断格式是否允许出现在新的蓝图/映射文档里。
func IsKnownExportFormat(format string) bool {
	for _, candidate := range KnownExportFormats() {
		if candidate == format {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// 样本版本 payload schema 与字段（§2.2）
// ---------------------------------------------------------------------------

// 样本版本 payload 的 schema 版本。随项目 target_kind 固定（§2.2）。
const (
	SampleSchemaSFT  = "sft.sample.v1"
	SampleSchemaGRPO = "grpo.sample.v1"
)

// SampleSchemaForTarget 返回某目标类型的样本 schema 版本。
func SampleSchemaForTarget(targetKind string) string {
	if targetKind == TargetKindGRPO {
		return SampleSchemaGRPO
	}
	return SampleSchemaSFT
}

// RequiredSampleFields 返回某目标类型的必填字段（§2.2 的表格）。
//
// SFT 新路径统一用 `reasoning`，不再用旧名 `chainOfThought`；
// GRPO 必须保留 `levels`（数组）与 `level_rubrics`（对象数组）。
func RequiredSampleFields(targetKind string) []string {
	if targetKind == TargetKindGRPO {
		return []string{"question", "judge_prompt", "levels", "level_rubrics"}
	}
	return []string{"question", "reasoning", "answer"}
}

// ValidateGRPOSamplePayload 校验 GRPO 样本 payload（§2.2）：
// 至少两档、档位不重复、每档有判据与边界例、与 levels 一一对应。
//
// 复用既有的 GrpoLevelRubric（internal/model/pipeline_v2.go）而不是新定义一个
// 同概念类型：那份类型已经承载了「判据文本（Criteria）+ 边界例（AcceptCase /
// RejectCase）」三件事，正是 §2.2 要求的「每档至少包含判据文本与边界例」。
// 另起一个类型会让同一份 GRPO 判据在两条代码路径上有两种形状，
// 而 §2.2 明确禁止把 level_rubrics 退化成逗号字符串 —— 两种形状正是退化的温床。
func ValidateGRPOSamplePayload(levels []string, rubrics []GrpoLevelRubric) error {
	var errs FieldErrors

	if len(levels) < 2 {
		errs = append(errs, FieldError{Field: "levels", Message: "至少需要两档"})
	}
	seen := map[string]bool{}
	for index, level := range levels {
		field := fmt.Sprintf("levels[%d]", index)
		if strings.TrimSpace(level) == "" {
			errs = append(errs, FieldError{Field: field, Message: "不能为空"})
			continue
		}
		if seen[level] {
			errs = append(errs, FieldError{Field: field, Message: fmt.Sprintf("档位 %q 重复", level)})
		}
		seen[level] = true
	}

	seenRubric := map[string]bool{}
	for index, rubric := range rubrics {
		field := fmt.Sprintf("levelRubrics[%d]", index)
		if strings.TrimSpace(rubric.Level) == "" {
			errs = append(errs, FieldError{Field: field + ".level", Message: "必填"})
			continue
		}
		if seenRubric[rubric.Level] {
			errs = append(errs, FieldError{Field: field + ".level", Message: "同一档位不能有两条判据"})
		}
		seenRubric[rubric.Level] = true
		if strings.TrimSpace(rubric.Criteria) == "" {
			errs = append(errs, FieldError{Field: field + ".criteria", Message: "至少需要一条判据"})
		}
		// 边界例至少给一个方向：只写「什么样算符合」而不写「什么样算不符合」
		// 无法约束判分，而档位之间的边界正是 GRPO 判据最容易含糊的地方。
		if strings.TrimSpace(rubric.AcceptCase) == "" && strings.TrimSpace(rubric.RejectCase) == "" {
			errs = append(errs, FieldError{
				Field:   field + ".acceptCase",
				Message: "至少要给出一个边界例（接受或拒绝）",
			})
		}
		if !seen[rubric.Level] {
			errs = append(errs, FieldError{
				Field:   field + ".level",
				Message: fmt.Sprintf("档位 %q 不在 levels 中（判据必须与档位一一对应）", rubric.Level),
			})
		}
	}
	// 反向：每一档都必须有判据。
	for index, level := range levels {
		if strings.TrimSpace(level) != "" && !seenRubric[level] {
			errs = append(errs, FieldError{
				Field:   fmt.Sprintf("levelRubrics[%d]", index),
				Message: fmt.Sprintf("档位 %q 缺少判据", level),
			})
		}
	}

	if len(errs) > 0 {
		return errs
	}
	return nil
}
