// Package cleaning 实现数据清洗的领域逻辑：拒答关键词库匹配与规则判定。
//
// 需求来源：数据在生成过程中（问题、思维链、答案等步骤）可能因模型自身的
// 道德或安全限制而拒答，需要用关键词匹配把这些样本识别出来。
//
// 命名契约（docs/plans/eval-and-cleaning-plan.md 第 3.11 节 + 父代理裁决）：
// 本包由 L11 与 L12 两条 lane 共同写入，两边标识符不得重叠。
//
//	L11（本文件）：Match / BuiltinKeywords / MatchKeywords / Snippet / EvaluateRules
//	L12（scanner.go）：ScannerMatch / Matcher / ScanTarget / ScanResult / RuleSpec / Scan
//
// 因此匹配函数命名为 MatchKeywords 而非 Match —— Go 包级不允许类型与函数同名。
package cleaning

import (
	"errors"
	"regexp"
	"sort"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"

	"github.com/1420970597/llm/internal/model"
)

// ErrBuiltinKeywordImmutable 表示试图删除内置关键词。
//
// 领域规则：内置关键词库是全系统清洗的基线，只允许停用（IsActive=false），
// 不允许删除；否则用户一次误删就会让清洗静默失效且无法追溯。
var ErrBuiltinKeywordImmutable = errors.New("内置关键词不可删除，请改为停用（isActive=false）")

// Match 单条关键词命中。
type Match struct {
	KeywordID   int64  `json:"keywordId"`
	Pattern     string `json:"pattern"`
	Category    string `json:"category"`
	MatchedText string `json:"matchedText"`
	Snippet     string `json:"snippet"`
	Severity    string `json:"severity"`
}

// 关键词分类。
const (
	CategoryRefusal            = "refusal"
	CategorySafety             = "safety"
	CategoryUncertaintyEvasion = "uncertainty_evasion"
	CategoryEnglishRefusal     = "english_refusal"
	CategoryPlaceholder        = "placeholder"
)

// 严重度。
const (
	SeverityBlock = "block"
	SeverityWarn  = "warn"
)

// 匹配模式。
const (
	ModeContains = "contains"
	ModePrefix   = "prefix"
	ModeRegex    = "regex"
)

func builtin(pattern, category, mode, severity, note string) model.CleaningKeyword {
	return model.CleaningKeyword{
		Pattern:   pattern,
		Category:  category,
		MatchMode: mode,
		Severity:  severity,
		IsBuiltin: true,
		IsActive:  true,
		Note:      note,
	}
}

// BuiltinKeywords 返回内置拒答/安全关键词库（41 条，中英文覆盖）。
//
// 每次调用返回新的切片，调用方可安全修改返回值而不影响其他调用者。
func BuiltinKeywords() []model.CleaningKeyword {
	return []model.CleaningKeyword{
		// ---- refusal：中文直接拒答 ----
		builtin("对不起", CategoryRefusal, ModeContains, SeverityBlock, "典型中文拒答开场"),
		builtin("我不能", CategoryRefusal, ModeContains, SeverityBlock, ""),
		builtin("抱歉，我无法", CategoryRefusal, ModeContains, SeverityBlock, ""),
		builtin("我无法提供", CategoryRefusal, ModeContains, SeverityBlock, ""),
		builtin("我不能帮助", CategoryRefusal, ModeContains, SeverityBlock, ""),
		builtin("恕难从命", CategoryRefusal, ModeContains, SeverityBlock, ""),
		builtin("我拒绝", CategoryRefusal, ModeContains, SeverityBlock, ""),
		builtin("无法回答", CategoryRefusal, ModeContains, SeverityBlock, ""),
		builtin("不适合回答", CategoryRefusal, ModeContains, SeverityBlock, ""),
		builtin("不便透露", CategoryRefusal, ModeContains, SeverityBlock, ""),
		builtin("我不能讨论", CategoryRefusal, ModeContains, SeverityBlock, ""),
		// 这两条几乎只出现在句首，用 prefix 避免误伤正文里的「作为人工智能技术…」。
		builtin("作为一个AI", CategoryRefusal, ModePrefix, SeverityBlock, "自我身份声明式拒答，仅句首匹配"),
		builtin("作为人工智能", CategoryRefusal, ModePrefix, SeverityBlock, "自我身份声明式拒答，仅句首匹配"),

		// ---- safety：安全/合规限制 ----
		builtin("违反", CategorySafety, ModeContains, SeverityBlock, ""),
		builtin("不符合规定", CategorySafety, ModeContains, SeverityBlock, ""),
		builtin("涉及敏感", CategorySafety, ModeContains, SeverityBlock, ""),
		builtin("不安全", CategorySafety, ModeContains, SeverityBlock, ""),
		builtin("违法违规", CategorySafety, ModeContains, SeverityBlock, ""),
		builtin("道德准则", CategorySafety, ModeContains, SeverityBlock, ""),
		builtin("使用政策", CategorySafety, ModeContains, SeverityBlock, ""),
		builtin("内容政策", CategorySafety, ModeContains, SeverityBlock, ""),

		// ---- uncertainty_evasion：含糊推脱 ----
		builtin("请咨询专业人士", CategoryUncertaintyEvasion, ModeContains, SeverityWarn, ""),
		builtin("建议咨询", CategoryUncertaintyEvasion, ModeContains, SeverityWarn, ""),
		builtin("我建议你自行", CategoryUncertaintyEvasion, ModeContains, SeverityWarn, ""),
		builtin("无法保证准确性", CategoryUncertaintyEvasion, ModeContains, SeverityWarn, ""),
		builtin("可能不准确", CategoryUncertaintyEvasion, ModeContains, SeverityWarn, ""),

		// ---- english_refusal：英文拒答 ----
		builtin("I cannot", CategoryEnglishRefusal, ModeContains, SeverityBlock, ""),
		builtin("I can't", CategoryEnglishRefusal, ModeContains, SeverityBlock, ""),
		builtin("I'm sorry", CategoryEnglishRefusal, ModeContains, SeverityBlock, ""),
		builtin("I am unable", CategoryEnglishRefusal, ModeContains, SeverityBlock, ""),
		builtin("As an AI", CategoryEnglishRefusal, ModePrefix, SeverityBlock, "仅句首匹配"),
		builtin("I must decline", CategoryEnglishRefusal, ModeContains, SeverityBlock, ""),
		builtin("I'm not able to", CategoryEnglishRefusal, ModeContains, SeverityBlock, ""),
		builtin("I won't", CategoryEnglishRefusal, ModeContains, SeverityBlock, ""),
		builtin("against my guidelines", CategoryEnglishRefusal, ModeContains, SeverityBlock, ""),
		builtin("violates policy", CategoryEnglishRefusal, ModeContains, SeverityBlock, ""),

		// ---- placeholder：占位/空输出 ----
		builtin("TODO", CategoryPlaceholder, ModeContains, SeverityWarn, ""),
		builtin("待补充", CategoryPlaceholder, ModeContains, SeverityWarn, ""),
		builtin("此处省略", CategoryPlaceholder, ModeContains, SeverityWarn, ""),
		builtin("N/A", CategoryPlaceholder, ModeContains, SeverityWarn, ""),
		// 中文行文普遍用 U+2026「…」而非三个 ASCII 点号，两种都要覆盖。
		// 误伤风险高，因此把严重度压到 warn。
		builtin(`(\.{3,}|…{2,})`, CategoryPlaceholder, ModeRegex, SeverityWarn, "连续省略号，误伤风险高，仅告警"),
	}
}

// normalize 把内容归一化为「全角转半角 + 逐 rune 小写」。
//
// 关键不变量：**逐 rune 一对一映射，rune 数量与顺序不变**。
// 因此归一化串上的 rune 下标可以直接用于原串取片段（Snippet 依赖这一点）。
// 这里刻意用 unicode.ToLower 逐 rune 处理，而不是 strings.ToLower：
// 后者对个别字符（如 'İ' U+0130）会一变二，破坏下标对应关系。
func normalize(s string) []rune {
	out := make([]rune, 0, len(s))
	for _, r := range s {
		switch {
		case r == '\u3000': // 全角空格
			r = ' '
		case r >= '\uff01' && r <= '\uff5e': // 全角 ASCII
			r = r - 0xfee0
		}
		out = append(out, unicode.ToLower(r))
	}
	return out
}

// Snippet 取 content 中 rune 区间 [start, end) 前后各 radius 个 rune 的上下文。
//
// 必须按 rune 处理：中文按 byte 切会切出半个字，产生非法 UTF-8。
func Snippet(content string, start, end int, radius int) string {
	runes := []rune(content)
	n := len(runes)
	if start < 0 {
		start = 0
	}
	if end > n {
		end = n
	}
	if start > end {
		start = end
	}
	if radius < 0 {
		radius = 0
	}
	from := start - radius
	if from < 0 {
		from = 0
	}
	to := end + radius
	if to > n {
		to = n
	}
	return string(runes[from:to])
}

// regexCache 缓存已编译的正则。清洗会对整库关键词逐条匹配，
// 同一批 pattern 会在每次扫描里重复出现，编译开销不该重复付。
var regexCache sync.Map // pattern(string) -> *regexp.Regexp

func compilePattern(pattern string) (*regexp.Regexp, error) {
	if cached, ok := regexCache.Load(pattern); ok {
		if re, ok := cached.(*regexp.Regexp); ok {
			return re, nil
		}
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		return nil, err
	}
	regexCache.Store(pattern, re)
	return re, nil
}

// MatchKeywords 在 content 上匹配关键词库，返回全部命中。
//
// 行为：
//   - 按 MatchMode 匹配：contains（默认，未知值也按 contains）/ prefix / regex
//   - 同一关键词多次命中只返回一条，Snippet 取首次命中的上下文
//   - 跳过 IsActive=false 或 Pattern 为空的关键词
//   - regex 匹配针对归一化后（已小写）的文本，因此正则里的字母请写小写
//
// 返回值恒为非 nil 切片，便于调用方直接 range / 序列化成 []。
func MatchKeywords(content string, keywords []model.CleaningKeyword) []Match {
	matches := make([]Match, 0, 4)
	if content == "" || len(keywords) == 0 {
		return matches
	}

	origRunes := []rune(content)
	runes := normalize(content)
	hay := string(runes) // 已小写，contains/prefix/regex 都在它上面找
	// normalize 是一对一映射，因此归一化串的 rune 下标可直接用于 origRunes。

	for _, kw := range keywords {
		if !kw.IsActive || kw.Pattern == "" {
			continue
		}

		mode := kw.MatchMode
		if mode == "" {
			mode = ModeContains
		}

		var start, end int
		switch mode {
		case ModeRegex:
			re, err := compilePattern(kw.Pattern)
			if err != nil {
				// 非法正则视为该关键词不可用，不阻断整轮扫描。
				continue
			}
			loc := re.FindStringIndex(hay)
			if loc == nil {
				continue
			}
			start = utf8.RuneCountInString(hay[:loc[0]])
			end = utf8.RuneCountInString(hay[:loc[1]])

		case ModePrefix:
			needle := string(normalize(kw.Pattern))
			if needle == "" || !strings.HasPrefix(hay, needle) {
				continue
			}
			start = 0
			end = utf8.RuneCountInString(needle)

		default: // ModeContains
			needle := string(normalize(kw.Pattern))
			if needle == "" {
				continue
			}
			idx := strings.Index(hay, needle)
			if idx < 0 {
				continue
			}
			start = utf8.RuneCountInString(hay[:idx])
			end = start + utf8.RuneCountInString(needle)
		}

		matches = append(matches, Match{
			KeywordID: kw.ID,
			Pattern:   kw.Pattern,
			Category:  kw.Category,
			// MatchedText 取原文片段，保留大小写；命中位置来自归一化文本。
			MatchedText: string(origRunes[start:end]),
			Snippet:     Snippet(content, start, end, 30),
			Severity:    kw.Severity,
		})
	}
	return matches
}

// EvaluateRules 按规则优先级判定最终动作，返回动作与命中数。
//
// 判定顺序：过滤掉未启用、以及 stage_scope 不含 stage 的规则（scope 为空视为全阶段生效），
// 按 priority 升序（小者优先）取第一条满足 hits >= min_hits 的规则，返回它的 Action。
//
// 无任何规则命中时返回 ("keep", hits) —— "keep" 表示保留样本，
// 供调用方区分「没命中规则」与「命中规则但动作为 flag」。
func EvaluateRules(matches []Match, rules []model.CleaningRule, stage string) (string, int) {
	hits := len(matches)

	ordered := make([]model.CleaningRule, 0, len(rules))
	for _, rule := range rules {
		if !rule.IsActive || !ruleAppliesToStage(rule, stage) {
			continue
		}
		ordered = append(ordered, rule)
	}
	sort.SliceStable(ordered, func(i, j int) bool {
		return ordered[i].Priority < ordered[j].Priority
	})

	for _, rule := range ordered {
		minHits := rule.MinHits
		if minHits < 1 {
			minHits = 1
		}
		if hits >= minHits {
			action := rule.Action
			if action == "" {
				action = "flag"
			}
			return action, hits
		}
	}
	return "keep", hits
}

func ruleAppliesToStage(rule model.CleaningRule, stage string) bool {
	if len(rule.StageScope) == 0 {
		return true
	}
	for _, scope := range rule.StageScope {
		if scope == stage {
			return true
		}
	}
	return false
}
