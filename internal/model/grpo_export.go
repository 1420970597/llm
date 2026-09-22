package model

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
)

// 本文件定义 GRPO 导出文件（JSONL）的 typed 形状与结构校验（Issue #160 T25）。
//
// 契约：docs/plans/atelier-implementation.md §2.2（GRPO payload 字段）、
// T25 的原文要求：「逐行解码字段 question/judge_prompt/levels/level_rubrics，
// 校验结构和档位一一对应」。
//
// 为什么需要一份**导出专用**的 typed 形状，而不是复用样本 payload：
//
//	样本 payload 里的键是 camelCase（`judgePrompt`/`levelRubrics`），
//	而导出契约（§2.2 与 T25）要求的是 snake_case 的
//	`judge_prompt`/`level_rubrics`。两者是同一种数据的两种外部表示。
//	把导出直接写成 payload 会让「契约要求 judge_prompt 而文件里是
//	judgePrompt」这种偏差只在用户训练脚本里暴露。
//
// 另一条更重要的理由：**结构必须能被机器校验**。T25 明确禁止把 `levels`
// 与 `level_rubrics` 退化成逗号字符串，而「禁止」只有在校验处拒绝才成立。
// 本文件的 VerifyGRPOExportLine 就是那个拒绝点。

// GRPORubricExport 是 `level_rubrics` 数组元素在导出文件里的形状。
//
// 用结构体而不是 map：结构体的字段顺序是确定的，因此「同一份内容两次
// 编码得到相同字节」不需要依赖编码器的额外保证（T21 的 hash 复算依赖这一点）。
type GRPORubricExport struct {
	Level      string `json:"level"`
	Criteria   string `json:"criteria"`
	AcceptCase string `json:"accept_case"`
	RejectCase string `json:"reject_case"`
}

// GRPOExportLine 是 GRPO JSONL 的一行（导出契约的 typed 形状）。
type GRPOExportLine struct {
	Question     string             `json:"question"`
	JudgePrompt  string             `json:"judge_prompt"`
	Levels       []string           `json:"levels"`
	LevelRubrics []GRPORubricExport `json:"level_rubrics"`
	// FrameworkRef 可空：它是 provenance，不是判分依据。
	FrameworkRef string `json:"framework_ref,omitempty"`
}

// ToGRPORubricExports 把样本里的判据转成导出形状。
func ToGRPORubricExports(rubrics []GrpoLevelRubric) []GRPORubricExport {
	exports := make([]GRPORubricExport, 0, len(rubrics))
	for _, rubric := range rubrics {
		exports = append(exports, GRPORubricExport{
			Level:      rubric.Level,
			Criteria:   rubric.Criteria,
			AcceptCase: rubric.AcceptCase,
			RejectCase: rubric.RejectCase,
		})
	}
	return exports
}

// GRPORequiredExportFields 是 GRPO JSONL 每一行必须存在的顶层字段（T25 原文）。
func GRPORequiredExportFields() []string {
	return []string{"question", "judge_prompt", "levels", "level_rubrics"}
}

// VerifyGRPOExportLine 解码一行 GRPO JSONL 并校验结构。
//
// 拒绝的条件（每一条都对应一个真实的退化形态）：
//
//  1. 行不是 JSON 对象 → 编码器出了问题；
//  2. `levels` / `level_rubrics` 不是数组 → 正是「strings.Join 成逗号字符串」
//     的形态（把逗号字符串喂给 []string 会解码失败）；
//  3. 档位不足两档、有空档位或重复档位 → 判分标准无法成立（§2.2）；
//  4. `level_rubrics` 与 `levels` 数量不一致或档位名不匹配 → 判据与档位
//     没有一一对应（§2.2 明确要求对应）；
//  5. 某一档没有判据文本 → 该档无法判分；
//  6. `question` / `judge_prompt` 为空 → 文件不能被训练使用。
//
// 返回的错误带**行号**由调用方补充：这里只描述这一行哪里不对。
func VerifyGRPOExportLine(line []byte) error {
	trimmed := bytes.TrimSpace(line)
	if len(trimmed) == 0 {
		return fmt.Errorf("空行：GRPO JSONL 每一行都必须是一个对象")
	}
	// 先检查顶层必须是对象：数组/字符串也能被 json.Unmarshal 进结构体的
	// 相应情况很少，但显式判断能让错误信息更准确。
	if trimmed[0] != '{' {
		return fmt.Errorf("不是 JSON 对象（首个非空白字符是 %q）", trimmed[0])
	}

	var record GRPOExportLine
	decoder := json.NewDecoder(bytes.NewReader(trimmed))
	if err := decoder.Decode(&record); err != nil {
		// 这条错误信息刻意不包含原始行内容：行里是用户数据，
		// 而错误会被写进发布构建日志（T25 的日志不含正文）。
		return fmt.Errorf("字段结构不合法：%v", err)
	}

	if strings.TrimSpace(record.Question) == "" {
		return fmt.Errorf("缺少 question")
	}
	if strings.TrimSpace(record.JudgePrompt) == "" {
		return fmt.Errorf("缺少 judge_prompt")
	}
	if len(record.Levels) < 2 {
		return fmt.Errorf("levels 至少需要两档，实际 %d 档", len(record.Levels))
	}
	seen := make(map[string]bool, len(record.Levels))
	for index, level := range record.Levels {
		name := strings.TrimSpace(level)
		if name == "" {
			return fmt.Errorf("levels[%d] 为空", index)
		}
		if seen[name] {
			return fmt.Errorf("levels[%d] 档位 %q 重复", index, name)
		}
		seen[name] = true
	}
	if len(record.LevelRubrics) != len(record.Levels) {
		return fmt.Errorf("level_rubrics 有 %d 条，而 levels 有 %d 档（必须一一对应）",
			len(record.LevelRubrics), len(record.Levels))
	}
	rubricByName := make(map[string]GRPORubricExport, len(record.LevelRubrics))
	for index, rubric := range record.LevelRubrics {
		name := strings.TrimSpace(rubric.Level)
		if name == "" {
			return fmt.Errorf("level_rubrics[%d].level 为空", index)
		}
		if !seen[name] {
			return fmt.Errorf("level_rubrics[%d] 的档位 %q 不在 levels 里", index, name)
		}
		if strings.TrimSpace(rubric.Criteria) == "" {
			return fmt.Errorf("level_rubrics[%d]（档位 %q）没有判据文本", index, name)
		}
		rubricByName[name] = rubric
	}
	for _, level := range record.Levels {
		if _, found := rubricByName[strings.TrimSpace(level)]; !found {
			return fmt.Errorf("档位 %q 缺少对应判据", level)
		}
	}
	return nil
}

// VerifyGRPOExportLines 逐行校验一份 GRPO JSONL 内容，返回**有效行数**。
//
// 返回行数的理由：调用方要用它跟 manifest 的条目数对账（T25 验收项
// 「发布清单与实际行数/hash 一致」）。一份文件「每行都合法」但行数与
// 清单不符，仍然是错的发布（少了内容或多了重复）。
//
// 错误带行号：一份十万行的文件里「哪一行坏了」是用户唯一能行动的线索。
func VerifyGRPOExportLines(content []byte) (int, error) {
	lines := bytes.Split(content, []byte("\n"))
	checked := 0
	for index, line := range lines {
		if len(bytes.TrimSpace(line)) == 0 {
			// 尾随换行会产生一个空片段；文件中间的空行由 VerifyGRPOExportLine
			// 明确拒绝（else 分支）。这里跳过纯尾随空段。
			if index == len(lines)-1 {
				continue
			}
			return checked, fmt.Errorf("第 %d 行是空行：GRPO JSONL 不允许空行", index+1)
		}
		if err := VerifyGRPOExportLine(line); err != nil {
			return checked, fmt.Errorf("第 %d 行：%w", index+1, err)
		}
		checked++
	}
	if checked == 0 {
		return 0, fmt.Errorf("文件没有任何有效行")
	}
	return checked, nil
}
