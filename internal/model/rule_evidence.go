package model

import (
	"fmt"
	"regexp"
	"strings"
)

// 本文件定义版本化规则与**不可变命中证据**（Issue #160 T15）。
//
// 契约：docs/plans/atelier-implementation.md §4.1（Decision 与证据分离）、
// §5（rules 节点）；docs/plans/atelier-api-contract.md §2.6（规则预览）。
//
// 三条不可让步的性质：
//
//  1. **预览是纯读**：预览只扫描选定内容版本并返回命中位置，
//     不写处置、不改内容、不入收费模型队列（§2.6）。
//     因此预览的实现在 store 里只有 SELECT。
//  2. **命中证据不可变**：证据冻结规则表达式、字段、严重度与建议动作。
//     规则后续被改/删不影响历史命中。
//  3. **自动结论不写人工处置**：规则只产出 evidence；
//     人工 Decision 由 T16 的表承载。两者不共表，结构上不可能互相冒充。

// 匹配方式、严重度与建议动作的取值**复用 T04 冻结的常量**
//（studio_docs.go 的 RuleMatch*/RuleSeverity*/RuleAction*）：
// 另立一套会让「质量策略版本」与「规则证据」对同一个概念用不同的字符串，
// 而那种不一致只在运行时以「评估时规则被跳过」的形式出现。
//
// 明确记录两点与 T15 相关的约束：
//   * 匹配方式是**闭合集合**（契约 §5 禁止任意脚本节点），因此不接受自由字符串；
//   * 建议动作里**没有「自动隔离」**——契约 §5 与 T15 要求「默认入审阅」
//     「规则命中不替人工处置」，所以这些取值只是**建议**，不是自动执行。

// 服务端资源上限。由**服务端**控制而不是信任客户端（T15 验收项原文：
// 「Go regexp/输入长度/计算上限由服务端控制，本地校验仅辅助」）。
const (
	// MaxRuleExpressionLength 限制表达式长度：超长表达式本身就是攻击面
	// （灾难性回溯的常见形态），而正常规则远不会这么长。
	MaxRuleExpressionLength = 500
	// MaxRuleContentLength 限制参与匹配的内容长度。
	// 与「样本正文可达数万字节」的现实对齐：超过则拒绝并提示分批，
	// 而不是让一次请求把服务端 CPU 占满。
	MaxRuleContentLength = 2_000_000
	// MaxRuleMatchesPerContent 限制单条内容的命中数上限。
	// 一个过宽的表达式（例如 `.*`）可以在一条内容上产出几十万次命中，
	// 而它们既不增加信息量、又会撑爆响应体。
	MaxRuleMatchesPerContent = 5000
)

// ValidateRuleSpec 校验一条质量策略规则（服务端）。
//
// 关键：**正则表达式必须在这里被真正编译**。只在界面校验是不够的 ——
// 客户端可以绕过界面直接调 API，而非法 regex 一旦落库，之后每次扫描都会
// 在运行期失败（那时错误信息与「哪条规则写坏了」已经隔了几层）。
// 因此校验即编译：编译不过就拒绝保存。
//
// 报错文案只说「不是合法正则」，不透出 Go 的编译错误原文：
// 它含内部语法细节，而用户需要知道的是「这条表达式写错了」。
func ValidateRuleSpec(rule QualityRule) error {
	field := "rules." + strings.TrimSpace(rule.ID)
	if strings.TrimSpace(rule.ID) == "" {
		return FieldErrors{{Field: "rules[].id", Message: "规则 ID 必填（它是历史命中证据的稳定引用）"}}
	}
	if strings.TrimSpace(rule.Expression) == "" {
		return FieldErrors{{Field: field + ".expression", Message: "规则表达式必填"}}
	}
	if len([]rune(rule.Expression)) > MaxRuleExpressionLength {
		return FieldErrors{{Field: field + ".expression", Message: fmt.Sprintf(
			"表达式不能超过 %d 个字符（超长表达式容易触发灾难性回溯）", MaxRuleExpressionLength)}}
	}

	switch rule.MatchType {
	case RuleMatchRegex:
		if _, err := regexp.Compile(rule.Expression); err != nil {
			return FieldErrors{{Field: field + ".expression",
				Message: "不是合法的正则表达式，请修正后保存（非法规则不会入库）"}}
		}
	case RuleMatchContains, RuleMatchStructure, RuleMatchFieldCheck:
		// 字面包含、结构检查与字段检查不需要编译：
		// 后两者的表达式是少量内置形式（例如 len>0），由执行侧解释。
	default:
		return FieldErrors{{Field: field + ".matchType",
			Message: "匹配类型只能是 contains、regex、field_check 或 structure"}}
	}

	if strings.TrimSpace(rule.Field) == "" {
		return FieldErrors{{Field: field + ".field", Message: "必须指定规则作用的字段"}}
	}
	switch rule.Severity {
	case RuleSeverityError, RuleSeverityWarning, RuleSeverityInfo:
	default:
		return FieldErrors{{Field: field + ".severity", Message: "严重度只能是 error、warning 或 info"}}
	}
	switch rule.SuggestedAction {
	case RuleActionReview, RuleActionSuggestQuarantine:
	default:
		return FieldErrors{{Field: field + ".suggestedAction",
			Message: "建议动作只能是 review 或 suggest_quarantine（它只是建议，不会自动执行）"}}
	}
	return nil
}

// RulePreviewHit 是一条预览命中。
//
// 位置以**字符偏移**给出（与界面高亮一致）：字节偏移会让中文内容的
// 高亮位置错位，而那是用户能直接看到的错误。
type RulePreviewHit struct {
	RuleID          string `json:"ruleId"`
	RuleName        string `json:"ruleName"`
	Field           string `json:"field"`
	Severity        string `json:"severity"`
	SuggestedAction string `json:"suggestedAction"`
	MatchStart      int    `json:"matchStart"`
	MatchEnd        int    `json:"matchEnd"`
	Snippet         string `json:"snippet"`
}

// RulePreviewResult 是预览结果。
//
// 显式带 `SideEffects: false`：契约 §2.6 要求「前后内容/处置/队列计数无变化」，
// 而把这个事实写进响应让前端与验收脚本都能**断言**它，
// 而不是靠「相信预览没写库」。
type RulePreviewResult struct {
	PolicyVersionID int64            `json:"policyVersionId"`
	ScannedCount    int              `json:"scannedCount"`
	Hits            []RulePreviewHit `json:"hits"`
	// SideEffects 恒为 false（预览是纯读）。
	SideEffects bool `json:"sideEffects"`
	// Truncated 表示命中数达到上限而被截断：必须显式告知，
	// 否则用户会以为「就这么多命中」。
	Truncated bool `json:"truncated"`
}

// RuleEvidence 是一条不可变的命中证据（落库形态）。
type RuleEvidence struct {
	ID              int64  `json:"id"`
	EvaluationID    int64  `json:"evaluationId"`
	ProjectID       int64  `json:"projectId"`
	SampleID        int64  `json:"sampleId"`
	SampleVersionID int64  `json:"sampleVersionId"`
	ContentHash     string `json:"contentHash"`

	RuleID          string `json:"ruleId"`
	RuleName        string `json:"ruleName"`
	RuleExpression  string `json:"ruleExpression"`
	RuleMatchType   string `json:"ruleMatchType"`
	Severity        string `json:"severity"`
	SuggestedAction string `json:"suggestedAction"`

	FieldName  string `json:"fieldName"`
	MatchStart int    `json:"matchStart"`
	MatchEnd   int    `json:"matchEnd"`
	Snippet    string `json:"snippet"`
}

// RuleEvidenceFromHit 把预览命中冻结成证据。
//
// 显式带上**规则快照**（表达式/严重度/建议动作）：这些字段在规则被改后会
// 与「当前规则」不同，而证据必须保留当时的值 —— 否则「为什么当时拦下了它」
// 在规则调整后无法回答。
func RuleEvidenceFromHit(rule QualityRule, hit RulePreviewHit, evaluationID, projectID, sampleID, sampleVersionID int64, contentHash string) RuleEvidence {
	return RuleEvidence{
		EvaluationID: evaluationID, ProjectID: projectID,
		SampleID: sampleID, SampleVersionID: sampleVersionID, ContentHash: contentHash,
		RuleID: rule.ID, RuleName: rule.Name, RuleExpression: rule.Expression,
		RuleMatchType: rule.MatchType, Severity: rule.Severity, SuggestedAction: rule.SuggestedAction,
		FieldName: hit.Field, MatchStart: hit.MatchStart, MatchEnd: hit.MatchEnd, Snippet: hit.Snippet,
	}
}
