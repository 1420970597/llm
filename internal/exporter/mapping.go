package exporter

import (
	"strconv"
	"strings"

	"github.com/1420970597/llm/internal/model"
)

// 本文件实现字段映射的取值表达式解析。
//
// 支持的写法（全部大小写不敏感、允许下划线与点号互换，便于前端直接填表）：
//
//	question            直接引用字段
//	{{ question }}      带花括号的模板占位（整体就是一个占位符时保留原始类型）
//	{{chainOfThought}}\n\n答案：{{answer}}   多占位拼接成字符串
//	const:foo           常量字符串
//
// 可用字段见 recordFields。引用了未知字段会返回 MappingError 而不是静默留空，
// 避免用户配错映射后导出一批空列却无人察觉。

// recordFields 把 Record 暴露为可被映射表达式引用的字段表。
func recordFields(record Record) map[string]any {
	fields := map[string]any{
		"dataset_id":       record.DatasetID,
		"dataset_name":     record.DatasetName,
		"question_id":      record.QuestionID,
		"question":         record.Question,
		"chain_of_thought": record.ChainOfThought,
		"answer":           record.Answer,
		"judge_prompt":     record.JudgePrompt,
		"difficulty":       record.Difficulty,
		"domain_name":      record.DomainName,
		"reward_levels":    strings.Join(record.RewardLevels, ","),
	}
	if record.HasReward {
		fields["reward_score"] = record.RewardScore
	} else {
		// 没有打分记录时给出空字符串，而不是 0——0 是合法分值，会被误读成「得了 0 分」。
		fields["reward_score"] = ""
	}
	return fields
}

// normalizeKey 统一字段名写法。
//
// 前端配置映射时可能写成 camelCase（chainOfThought）、点号（chain.of.thought）
// 或短横线，这里统一折叠成下划线小写形式，让这些写法都能命中同一个字段。
func normalizeKey(raw string) string {
	key := strings.TrimSpace(raw)
	var builder strings.Builder
	for index, char := range key {
		switch {
		case char == '.' || char == '-' || char == ' ':
			builder.WriteByte('_')
		case char >= 'A' && char <= 'Z':
			// 只在「小写/数字 后面紧跟大写」时插入下划线，即真正的 camelCase 边界。
			// 否则 SCREAMING_SNAKE（CHAIN_OF_THOUGHT）会被拆成单个字母。
			if index > 0 {
				previous := rune(key[index-1])
				if previous >= 'a' && previous <= 'z' || previous >= '0' && previous <= '9' {
					builder.WriteByte('_')
				}
			}
			builder.WriteRune(char + ('a' - 'A'))
		default:
			builder.WriteRune(char)
		}
	}
	normalized := builder.String()
	for strings.Contains(normalized, "__") {
		normalized = strings.ReplaceAll(normalized, "__", "_")
	}
	return strings.Trim(normalized, "_")
}

// lookupField 解析单个字段名。第二返回值表示该名字是否是已知字段。
func lookupField(record Record, name string) (any, bool) {
	fields := recordFields(record)
	value, ok := fields[normalizeKey(name)]
	return value, ok
}

// resolveAny 解析一个取值表达式。
// 返回的 raw 非 nil 时表示该表达式是单个占位符且取到了原始类型，
// JSON 编码应直接使用 raw 以保留数值类型；raw 为 nil 时使用字符串 value。
func resolveAny(record Record, expr string) (string, any, error) {
	trimmed := strings.TrimSpace(expr)

	if strings.HasPrefix(trimmed, "const:") {
		constant := strings.TrimPrefix(trimmed, "const:")
		return constant, constant, nil
	}

	if strings.HasPrefix(trimmed, "{{") && strings.HasSuffix(trimmed, "}}") &&
		strings.Count(trimmed, "{{") == 1 {
		name := strings.TrimSuffix(strings.TrimPrefix(trimmed, "{{"), "}}")
		value, ok := lookupField(record, name)
		if !ok {
			return "", nil, &MappingError{Field: name, Reason: "未知字段"}
		}
		return stringify(value), value, nil
	}

	if !strings.Contains(trimmed, "{{") {
		// 裸字段名，例如 "answer"。
		value, ok := lookupField(record, trimmed)
		if !ok {
			return "", nil, &MappingError{Field: trimmed, Reason: "未知字段"}
		}
		return stringify(value), nil, nil
	}

	// 模板拼接：逐个替换占位符，结果一律是字符串。
	var builder strings.Builder
	rest := trimmed
	for {
		open := strings.Index(rest, "{{")
		if open < 0 {
			builder.WriteString(rest)
			break
		}
		closeIdx := strings.Index(rest[open:], "}}")
		if closeIdx < 0 {
			builder.WriteString(rest)
			break
		}
		builder.WriteString(rest[:open])
		name := rest[open+2 : open+closeIdx]
		value, ok := lookupField(record, name)
		if !ok {
			return "", nil, &MappingError{Field: name, Reason: "未知字段"}
		}
		builder.WriteString(stringify(value))
		rest = rest[open+closeIdx+2:]
	}
	return builder.String(), nil, nil
}

// stringify 把字段值转成字符串。字符串原样返回，不额外加引号。
func stringify(value any) string {
	switch typed := value.(type) {
	case nil:
		return ""
	case string:
		return typed
	case float64:
		return strconv.FormatFloat(typed, 'f', -1, 64)
	case int64:
		return strconv.FormatInt(typed, 10)
	case int:
		return strconv.Itoa(typed)
	default:
		return ""
	}
}

// BuiltinSpecs 内置映射的字段定义，供 store 层做幂等 seed。
// 顺序即 JSONL / CSV 的列顺序。
func BuiltinSpecs() []model.ExportMapping {
	return []model.ExportMapping{
		{
			Name:       "sft-alpaca",
			Format:     "alpaca",
			TargetKind: "sft",
			IsBuiltin:  true,
			IsDefault:  true,
			FieldMap: map[string]any{
				"instruction": "{{question}}",
				"input":       "const:",
				"output":      "{{chainOfThought}}\n\n答案：{{answer}}",
			},
		},
		{
			Name:       "sft-sharegpt",
			Format:     "sharegpt",
			TargetKind: "sft",
			IsBuiltin:  true,
			FieldMap: map[string]any{
				"human": "{{question}}",
				"gpt":   "{{chainOfThought}}\n\n答案：{{answer}}",
			},
		},
		{
			Name:       "grpo-jsonl",
			Format:     "jsonl",
			TargetKind: "grpo",
			IsBuiltin:  true,
			FieldMap: map[string]any{
				"question":      "{{question}}",
				"judge_prompt":  "{{judgePrompt}}",
				"reward_levels": "{{rewardLevels}}",
				"domain_name":   "{{domainName}}",
				"difficulty":    "{{difficulty}}",
			},
		},
	}
}

// FieldNames 返回映射里配置的目标字段名，按字典序。
func FieldNames(mapping model.ExportMapping) []string {
	names := make([]string, 0, len(mapping.FieldMap))
	for key := range mapping.FieldMap {
		names = append(names, key)
	}
	return names
}
