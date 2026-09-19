package llm

import (
	"errors"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

// 本文件实现「模型返回了合法 JSON，但内容是不是真的可用」的判定。
//
// 背景（issue #7）：模型在长文本生成被截断或「偷懒」时，会返回合法 JSON 但内容是
// 占位符，例如 {"answer":"...","reasoning":"..."}。此前代码只在 JSON 解析失败时
// 走失败分支，解析成功即置 status="generated"，于是 "..." 这类占位内容被当作成品
// 入库，并顺着状态机进入评估、清洗与导出，最终污染训练集。
//
// 判定分两层，缺一不可：
//  1. 结构层（已有）：JSON 能否解析 —— 由 unmarshalStructuredContent 负责。
//  2. 内容层（本文件）：字段是否为空 / 是否为占位符 / 是否过短 / 是否为上游输入的复制。
//
// 关键概念区分：
//   - failed  ：网络、超时、非 JSON 等「模型没答上来」。
//   - invalid ：模型答上来了，但内容是占位/无效的「模型摆烂」。
//     二者对下游同等看待（都不可用），但分开计数，便于观察是哪一类问题在增多。

// ContentStatusInvalid 是「模型返回了合法 JSON，但内容无效」的记录状态值。
//
// reasoning_records.status 与 reward_records.status 均为 TEXT 且无 CHECK 约束
// （见 0005_reasoning.sql / 0006_rewards.sql，已由 content_validator_test.go 断言），
// 因此新增该取值不需要新迁移。
const ContentStatusInvalid = "invalid"

// ReasoningStatusInvalid 是 ContentStatusInvalid 的别名。
//
// 之所以存在两个名字：冻结契约 docs/plans/issue-remediation-plan.md §1.3 指定了
// ReasoningStatusInvalid 这个名字，而同一取值也用于 reward_records。别名保证
// 全仓库只有一种取值拼写，契约符号同时存在。
const ReasoningStatusInvalid = ContentStatusInvalid

// ErrInvalidContent 表示模型返回了可解析但内容无效（占位 / 过短 / 自我复制）的结果。
//
// 调用方用 errors.Is 区分它与真实失败：
//   - errors.Is(err, ErrInvalidContent) == true  -> 记录状态 invalid
//   - 其他非 nil error                            -> 记录状态 failed
var ErrInvalidContent = errors.New("model returned placeholder content")

// invalidContentError 携带具体不合格原因，便于日志与测试断言。
type invalidContentError struct {
	field  string
	reason string
}

func (e invalidContentError) Error() string {
	return fmt.Sprintf("%s: %s", ErrInvalidContent.Error(), e.reason)
}

func (e invalidContentError) Unwrap() error { return ErrInvalidContent }

// Field 返回不合格字段名（answer / reasoning / rationale / content）。
func (e invalidContentError) Field() string { return e.field }

// Reason 返回不合格原因的中文说明。
func (e invalidContentError) Reason() string { return e.reason }

// 长度下限（按 rune 计，对中文按字计）。
//
// 标定依据：2026-09-19 对真实 provider（deepseek-v4.1-flash @ 152.53.126.151:8885）
// 直接抓取的输出样本，以及 issue #7 的故障样本。
//
//	字段        故障/占位样本     真实有效样本（rune）        取下限
//	reasoning   3（"..."）        83, 92, 93, 110, 112, 116, 126, 151, 200   40
//	answer     3–21              919, 1025, 1255, 1851, 2170             40
//	rationale   见下               23（拒绝作答）                          16
//	content     未观测             25, 28, 29                             12
//
// 取值原则：下限必须在「占位样本」与「最小真实有效样本」之间，且离后者留出
// 约 2 倍余量，以容忍模型正常波动。若下限取到真实样本附近（初版曾取 reasoning=80，
// 而真实样本最小为 83），会把合法数据误判为 invalid，比漏报更危险。
//
// rationale 单独说明：真实抓到的 23 rune 样本是「未提供需要评估的回答，无法评分」这类
// **拒绝作答**，不是占位符。拒绝作答的识别属于数据清洗的关键词匹配能力
// （功能说明.txt），不归本文件管；因此 rationale 下限只取到能拦住占位符的程度（16），
// 不试图在本层判定拒绝语。
const (
	// minReasoningRunes 是长链思考（reasoning）的长度下限。
	minReasoningRunes = 40
	// minAnswerRunes 是答案摘要（answer_summary）的长度下限。
	minAnswerRunes = 40
	// minRationaleRunes 是评分理由（rationale）的长度下限。
	minRationaleRunes = 16
	// minQuestionRunes 是问题（content）的长度下限。
	minQuestionRunes = 12
	// minContentRunes 是「有效字符」下限：字母、数字与汉字的总数。
	// 用于拦掉 "..."、"---"、"？？？" 这类没有承载任何信息的字符串。
	minContentRunes = 8
)

// placeholderTokens 是归一化后需要整体判定为占位符的取值。
//
// 归一化规则见 normalizeForPlaceholder：只保留字母、数字与汉字，其余删除并转小写。
// 因此 "..."/"。。。" 会归一化为空串，". . ." 同样如此。
//
// 注意：这里刻意只做「整个字段就是占位符」的判定，不做子串匹配。
// 子串级别的拒绝语（"对不起"、"我不能"）属于数据清洗能力
// （功能说明.txt 的关键词匹配）的职责，两处不重复实现。
var placeholderTokens = map[string]struct{}{
	"":            {},
	"na":          {},
	"n":           {},
	"none":        {},
	"null":        {},
	"nil":         {},
	"nan":         {},
	"undefined":   {},
	"todo":        {},
	"tbd":         {},
	"tba":         {},
	"pending":     {},
	"placeholder": {},
	"unknown":     {},
	"待定":          {},
	"待补充":         {},
	"暂无":          {},
	"略":           {},
	"省略":          {},
	"无":           {},
	"无法回答":        {},
	"不知道":         {},
	"不清楚":         {},
}

// normalizeForPlaceholder 把文本归一化到「只剩信息字符」的形式（小写）。
func normalizeForPlaceholder(input string) string {
	lowered := strings.ToLower(input)
	var builder strings.Builder
	builder.Grow(len(lowered))
	for _, char := range lowered {
		if unicode.IsLetter(char) || unicode.IsDigit(char) {
			builder.WriteRune(char)
		}
	}
	return builder.String()
}

// countContentRunes 统计承载信息的字符数（字母、数字、汉字）。
func countContentRunes(input string) int {
	count := 0
	for _, char := range input {
		if unicode.IsLetter(char) || unicode.IsDigit(char) {
			count++
		}
	}
	return count
}

// LongTextAssessment 是长文本字段（reasoning / answer / rationale）的判定结果。
type LongTextAssessment struct {
	// Valid 为 true 表示内容可进入训练集。
	Valid bool
	// Field 是不合格的字段名（answer / reasoning / rationale / content）；Valid 为 true 时为空。
	Field string
	// Reason 是不合格原因的中文说明；Valid 为 true 时为空。
	Reason string
}

// assessLongText 对单个长文本字段做内容层判定。
//
// field 是字段名，minRunes 是该字段的长度下限。
func assessLongText(field, value string, minRunes int) LongTextAssessment {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return LongTextAssessment{Field: field, Reason: field + " 为空"}
	}
	if _, isPlaceholder := placeholderTokens[normalizeForPlaceholder(trimmed)]; isPlaceholder {
		return LongTextAssessment{Field: field, Reason: field + " 是占位符"}
	}
	if contentRunes := countContentRunes(trimmed); contentRunes < minContentRunes {
		return LongTextAssessment{Field: field, Reason: fmt.Sprintf("%s 有效字符过少（%d < %d）", field, contentRunes, minContentRunes)}
	}
	if length := utf8.RuneCountInString(trimmed); length < minRunes {
		return LongTextAssessment{Field: field, Reason: fmt.Sprintf("%s 长度不足（%d < %d）", field, length, minRunes)}
	}
	return LongTextAssessment{Valid: true}
}

// AssessQuestionContent 判定问题文本是否可用。
//
// 不合法的问题不应入队：questions.status 由 store 层硬编码为 'generated'
// （pipeline_store.go / question_store_v2.go），没有 invalid 通道，
// 因此对问题采取「直接丢弃」而不是「落库标状态」。
func AssessQuestionContent(content string) LongTextAssessment {
	return assessLongText("content", content, minQuestionRunes)
}

// AssessReasoningContent 判定一条推理记录（答案摘要 + 长链思考）是否可用。
//
// answer 与 reasoning 任一不合格即整条不合格。reasoning 是长链思考数据的主体，
// 必须存在且成规模；answer_summary 是面向人的摘要，同样不得是占位符。
//
// 另外拒绝 reasoning 与 answer 雷同：那说明模型只写了一遍内容然后复制到两个字段，
// 没有真正产出「答案 + 思考过程」这对数据。
func AssessReasoningContent(answer, reasoning string) LongTextAssessment {
	answerAssessment := assessLongText("answer", answer, minAnswerRunes)
	if !answerAssessment.Valid {
		return answerAssessment
	}
	reasoningAssessment := assessLongText("reasoning", reasoning, minReasoningRunes)
	if !reasoningAssessment.Valid {
		return reasoningAssessment
	}
	if normalizeForPlaceholder(answer) == normalizeForPlaceholder(reasoning) {
		return LongTextAssessment{Field: "reasoning", Reason: "reasoning 与 answer 雷同"}
	}
	return LongTextAssessment{Valid: true}
}

// AssessRewardContent 判定一条评分记录的理由（rationale）是否可用。
//
// reward_records 只落 score，rationale 进对象存储；但 rationale 是评分的唯一依据，
// 占位理由意味着这条评分没有实际判断过程，故同样按 invalid 处理。
func AssessRewardContent(rationale string) LongTextAssessment {
	return assessLongText("rationale", rationale, minRationaleRunes)
}

// newInvalidContentError 由判定结果构造可被 errors.Is(ErrInvalidContent) 识别的错误。
func newInvalidContentError(assessment LongTextAssessment) error {
	return invalidContentError{field: assessment.Field, reason: assessment.Reason}
}
