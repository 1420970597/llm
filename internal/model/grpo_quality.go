package model

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// 本文件定义 GRPO 的质量输入、量表与判据（Issue #160 T24）。
//
// 契约：docs/plans/atelier-implementation.md §2.2（GRPO payload 字段）、
// §2.3（分母与覆盖）、§2.6（缺分语义）；#160 T24 的原文要求。
//
// 为什么需要一份**专属**于 GRPO 的量表，而不是直接复用 SFT 的三个维度：
//
//	SFT 样本是 question + reasoning + answer，评估的是「推理过程好不好」；
//	GRPO 样本是 question + judge_prompt + levels + level_rubrics，
//	评估的是「判分标准好不好」—— 两者连被评对象都不同。
//
// 用 SFT 量表去评 GRPO 样本，最好的情况是每个维度都因缺字段而记缺分
//（看起来「跑了但没结论」），最坏的情况是模型对空 reasoning/answer 打分，
// 产出「看起来正常、其实语义错误」的质量结论 —— 后者正是 T24 要防的。
// 因此这里另立量表，并显式声明它只对 grpo 样本有效。

// GRPO 质量维度键（§2.2 与 T24 原文的「档位覆盖 / 边界稳定性 / 评分解释一致性」）。
const (
	// GRPODimLevelCoverage 档位覆盖：每个声明档位是否真的有可判分的依据。
	//
	// 它是**确定性**维度（不调用模型）：由 BuildLevelCoverage 直接计算。
	// 放在量表里而不是当成一个隐式前置检查，是为了让它进入报告的同一条
	// 分母与覆盖统计 —— 隐藏的前置检查会在「全都被挡住了」时显示成「无数据」。
	GRPODimLevelCoverage = "level_coverage"
	// GRPODimBoundaryStability 边界稳定性：档位之间的分界是否可稳定复现。
	// 需要**冻结的边界参考集**才可判定；没有参考集时记缺分（T24：
	// 「缺参考样例显示缺证据，不生成假统计」）。
	GRPODimBoundaryStability = "boundary_stability"
	// GRPODimExplanationConsistency 评分解释一致性：
	// judge_prompt 与各档判据是否给出彼此一致、无矛盾的判分解释。
	GRPODimExplanationConsistency = "explanation_consistency"
)

// GRPOQualityDimensions 返回 GRPO 的内置量表维度。
//
// 权重与范围的取值理由：
//   - level_coverage 权重最高（0.4）：一个档位完全没有可判分依据时，
//     其余两个维度讨论的「边界」与「解释」都不成立；
//   - 范围：level_coverage 是比例（0–1，确定性计算）；
//     另两个维度由裁判按 1–5 分档评分（与既有 eval 量表的表达一致）。
//     量表**逐维度**记录 min/max，归一化因此不需要假设统一量纲 ——
//     这正是 RubricDimension 带范围的原因。
func GRPOQualityDimensions() []RubricDimension {
	return []RubricDimension{
		{Key: GRPODimLevelCoverage, Label: "档位覆盖", Weight: 0.4, Min: 0, Max: 1},
		{Key: GRPODimBoundaryStability, Label: "边界稳定性", Weight: 0.3, Min: 1, Max: 5},
		{Key: GRPODimExplanationConsistency, Label: "评分解释一致性", Weight: 0.3, Min: 1, Max: 5},
	}
}

// BuiltinGRPORubric 返回可直接保存的 GRPO 量表快照。
func BuiltinGRPORubric() RubricSpec {
	return RubricSpec{Dimensions: GRPOQualityDimensions()}
}

// GRPOJudgedDimensionKeys 返回需要**模型裁判**的维度键（确定性维度之外的部分）。
func GRPOJudgedDimensionKeys() []string {
	return []string{GRPODimBoundaryStability, GRPODimExplanationConsistency}
}

// LocalDimensionKeys 返回某目标类型下**不需要模型**的确定性维度键。
//
// 由 runner 用来把确定性判据与模型裁判分开记录：
//   - 确定性维度只记一行（judge_connection_id = 0），不按裁判数复制。
//     复制 N 份会让报告显示「N 名裁判完全一致」，而那个一致是假的
//     （它根本不是裁判判断，是同一段代码算出来的）。
//   - 模型裁判只负责其余维度，避免同一格既有确定性分又有模型分。
func LocalDimensionKeys(targetKind string) []string {
	if targetKind == TargetKindGRPO {
		return []string{GRPODimLevelCoverage}
	}
	return nil
}

// IsLocalDimension 判断某维度是否属于确定性维度。
func IsLocalDimension(targetKind, dimension string) bool {
	for _, key := range LocalDimensionKeys(targetKind) {
		if key == dimension {
			return true
		}
	}
	return false
}

// GRPOSample 是 grpo.sample.v1 的 typed 形状（§2.2）。
//
// 保留 levels（数组）与 level_rubrics（对象数组）的结构：T25 的 JSONL 导出
// 逐行解码它们，而 strings.Join 成逗号字符串会让档位边界无法还原。
type GRPOSample struct {
	Question     string            `json:"question"`
	JudgePrompt  string            `json:"judgePrompt"`
	Levels       []string          `json:"levels"`
	LevelRubrics []GrpoLevelRubric `json:"levelRubrics"`
	FrameworkRef string            `json:"frameworkRef"`
}

// ParseGRPOSamplePayload 解析样本版本 payload 并校验（§2.2）。
//
// 不接受「字段名兼容读」（例如同时认 chainOfThought）：GRPO 分支的
// 内容边界由 T23 冻结，静默接受别名会让一份写错的 payload 通过校验，
// 而错误只在导出/训练时才暴露。
func ParseGRPOSamplePayload(raw json.RawMessage) (GRPOSample, error) {
	var sample GRPOSample
	if len(raw) == 0 {
		return GRPOSample{}, fmt.Errorf("样本内容为空")
	}
	if err := json.Unmarshal(raw, &sample); err != nil {
		return GRPOSample{}, fmt.Errorf("GRPO 样本内容不是合法 JSON：%w", err)
	}
	if err := ValidateGRPOSamplePayload(sample.Levels, sample.LevelRubrics); err != nil {
		return GRPOSample{}, err
	}
	if strings.TrimSpace(sample.Question) == "" {
		return GRPOSample{}, fmt.Errorf("GRPO 样本缺少问题内容")
	}
	return sample, nil
}

// LevelCoverage 是档位覆盖的确定性判定结果。
//
// Score 是「有可判分依据的档位」占比（0–1）。Evaluable=false 表示
// **无法判定**（没有档位），调用方必须记缺分而不是记 0 —— 0 会被聚合
// 当成「档位全都不可判分」，而事实是「没有档位可判」。
type LevelCoverage struct {
	Evaluable bool
	Score     float64
	// CoveredLevels 是按输入顺序有完整依据的档位。
	CoveredLevels []string
	// MissingLevels 是缺少依据的档位（每档一条可读原因）。
	MissingLevels []string
	Reasons       []string
}

// BuildLevelCoverage 计算档位覆盖（**纯函数，不调用模型**）。
//
// 判定一个档位「有可判分依据」的三个条件（缺一不可）：
//
//  1. 该档位在 level_rubrics 里有条目（ValidateGRPOSamplePayload 已要求，
//     这里再查一次是因为**旧数据**可能不满足新校验）；
//  2. 判据文本非空：只有档位名而没有判据，裁判无从下分；
//  3. 至少有一个边界例（接受或拒绝）：只写「什么样算符合」无法约束
//     档位边界，而档位边界正是 GRPO 判据最容易含糊的地方。
//
// 另外检查 judge_prompt 是否提到了每一个档位：判分标准写在判据里、
// 但裁判提示词里没提某个档位时，模型实际上不会去区分它。
// 这是一条**文本级**检查（不调模型），因此它可能漏判「用同义词提到」，
// 但漏判的方向是「显示覆盖不足」而不是「显示覆盖充足」，可以接受。
func BuildLevelCoverage(sample GRPOSample) LevelCoverage {
	levels := sample.Levels
	if len(levels) == 0 {
		return LevelCoverage{Evaluable: false, Reasons: []string{"样本没有声明任何档位"}}
	}

	rubricByLevel := map[string]GrpoLevelRubric{}
	for _, rubric := range sample.LevelRubrics {
		key := strings.TrimSpace(rubric.Level)
		if key == "" {
			continue
		}
		// 同一档位多条判据时保留第一条：重复本身是校验失败，
		// 这里只需要一个确定的判定基准（不静默合并文本）。
		if _, exists := rubricByLevel[key]; !exists {
			rubricByLevel[key] = rubric
		}
	}

	prompt := strings.ToLower(sample.JudgePrompt)
	result := LevelCoverage{Evaluable: true}
	for _, level := range levels {
		trimmed := strings.TrimSpace(level)
		if trimmed == "" {
			result.MissingLevels = append(result.MissingLevels, level)
			result.Reasons = append(result.Reasons, "存在空档位名")
			continue
		}
		rubric, found := rubricByLevel[trimmed]
		if !found {
			result.MissingLevels = append(result.MissingLevels, trimmed)
			result.Reasons = append(result.Reasons, fmt.Sprintf("档位 %q 没有对应判据", trimmed))
			continue
		}
		if strings.TrimSpace(rubric.Criteria) == "" {
			result.MissingLevels = append(result.MissingLevels, trimmed)
			result.Reasons = append(result.Reasons, fmt.Sprintf("档位 %q 的判据文本为空", trimmed))
			continue
		}
		if strings.TrimSpace(rubric.AcceptCase) == "" && strings.TrimSpace(rubric.RejectCase) == "" {
			result.MissingLevels = append(result.MissingLevels, trimmed)
			result.Reasons = append(result.Reasons, fmt.Sprintf("档位 %q 没有边界例（接受/拒绝至少给一个）", trimmed))
			continue
		}
		if prompt != "" && !strings.Contains(prompt, strings.ToLower(trimmed)) {
			result.MissingLevels = append(result.MissingLevels, trimmed)
			result.Reasons = append(result.Reasons, fmt.Sprintf(
				"档位 %q 未出现在裁判提示词里（判据写了但提示词没提，模型不会去区分它）", trimmed))
			continue
		}
		result.CoveredLevels = append(result.CoveredLevels, trimmed)
	}

	result.Score = float64(len(result.CoveredLevels)) / float64(len(levels))
	return result
}

// ---------------------------------------------------------------------------
// 边界参考集（T24「有需要时用冻结的标注/边界回答集进行判定实验」）
// ---------------------------------------------------------------------------

// BoundaryReferenceItem 是边界参考集里的一条。
type BoundaryReferenceItem struct {
	Level string `json:"level"`
	// Input 是被判定的内容（可以是问题、回答或判分场景）。
	Input string `json:"input"`
	// Expected 是该内容在冻结标注下的期望处置：accept / reject。
	Expected string `json:"expected"`
	Note     string `json:"note"`
}

// 边界参考项期望值（闭合集合）。
const (
	BoundaryExpectedAccept = "accept"
	BoundaryExpectedReject = "reject"
)

// BoundaryReferenceSet 是冻结的边界参考集。
//
// 「冻结」的含义：它随实验一起快照（内容 hash 与来源写入 target_config），
// 此后修改外部标注文件不会改变已经跑过的实验 —— 与样本版本同一条原则。
type BoundaryReferenceSet struct {
	// ID/Source 记录来源，使「这批边界样例从哪来、谁标的」可追溯。
	ID     string `json:"id"`
	Source string `json:"source"`
	// Sampled 表示这是抽样而非全量（抽样结论必须标示范围）。
	Sampled bool                    `json:"sampled"`
	Items   []BoundaryReferenceItem `json:"items"`
}

// Validate 校验边界参考集：每条必须有合法期望值与非空内容。
func (set BoundaryReferenceSet) Validate() error {
	var errs FieldErrors
	for index, item := range set.Items {
		field := fmt.Sprintf("boundaryReference.items[%d]", index)
		if strings.TrimSpace(item.Input) == "" {
			errs = append(errs, FieldError{Field: field + ".input", Message: "边界样例内容必填"})
		}
		switch strings.TrimSpace(item.Expected) {
		case BoundaryExpectedAccept, BoundaryExpectedReject:
		default:
			errs = append(errs, FieldError{
				Field:   field + ".expected",
				Message: "期望值必须是 accept 或 reject",
			})
		}
	}
	if len(errs) > 0 {
		return errs
	}
	return nil
}

// Hash 返回参考集的规范化内容 hash（用于冻结与复算）。
//
// 排序后再 hash：JSON 数组顺序在客户端可能不同，而「同一份参考集」
// 必须得到同一个 hash，否则重复实验的 target_config 会看起来不同。
func (set BoundaryReferenceSet) Hash() (string, error) {
	ordered := make([]BoundaryReferenceItem, len(set.Items))
	copy(ordered, set.Items)
	sort.SliceStable(ordered, func(i, j int) bool {
		if ordered[i].Level != ordered[j].Level {
			return ordered[i].Level < ordered[j].Level
		}
		if ordered[i].Input != ordered[j].Input {
			return ordered[i].Input < ordered[j].Input
		}
		return ordered[i].Expected < ordered[j].Expected
	})
	canonical := struct {
		ID      string                  `json:"id"`
		Source  string                  `json:"source"`
		Sampled bool                    `json:"sampled"`
		Items   []BoundaryReferenceItem `json:"items"`
	}{ID: set.ID, Source: set.Source, Sampled: set.Sampled, Items: ordered}
	return ContentHash(canonical)
}

// ---------------------------------------------------------------------------
// 实验的 typed target_config（迁移 0038）
// ---------------------------------------------------------------------------

// GRPOTargetConfig 是 GRPO 实验冻结的专属配置（T24）。
//
// 为什么必须冻结：T24 要求「结果关联教师提示词内容版本与基准回答版本」。
// 若只存 ID 引用，那么教师模板或基准回答更新一次，历史 GRPO 报告的解释
// 就会指向另一份内容 —— 与 T14「创建即冻结」同一条原则。
type GRPOTargetConfig struct {
	// TeacherPromptVersion 是教师提示词（judge_prompt 模板）的版本标识。
	TeacherPromptVersion string `json:"teacherPromptVersion"`
	// BaselineAnswerVersion 是基准回答集的版本标识（可空：无基准时留空，
	// 但那时 boundary_stability 会因缺参考集而记缺分，而不是记满分）。
	BaselineAnswerVersion string `json:"baselineAnswerVersion"`
	// BoundaryReference 是冻结的边界参考集本体。
	BoundaryReference BoundaryReferenceSet `json:"boundaryReference"`
	// BoundaryReferenceHash 是参考集的内容 hash（复算校验用）。
	BoundaryReferenceHash string `json:"boundaryReferenceHash"`
}

// Validate 校验 target_config。
func (config GRPOTargetConfig) Validate() error {
	return config.BoundaryReference.Validate()
}

// HasBoundaryReference 表示存在可判定的边界参考集。
//
// 判据是「至少一条样例」而不是「字段非空」：一个声称有参考集却没有任何
// 样例的配置，会让 boundary_stability 拿到一个没有依据的分数。
func (config GRPOTargetConfig) HasBoundaryReference() bool {
	return len(config.BoundaryReference.Items) > 0
}

// Prepare 计算并写入参考集 hash（创建实验时由服务端调用）。
func (config *GRPOTargetConfig) Prepare() error {
	if err := config.Validate(); err != nil {
		return err
	}
	hash, err := config.BoundaryReference.Hash()
	if err != nil {
		return err
	}
	config.BoundaryReferenceHash = hash
	return nil
}

// ParseGRPOTargetConfig 解析 target_config JSON；空值返回零值配置。
func ParseGRPOTargetConfig(raw json.RawMessage) (GRPOTargetConfig, error) {
	var config GRPOTargetConfig
	if len(raw) == 0 || string(raw) == "null" {
		return config, nil
	}
	if err := json.Unmarshal(raw, &config); err != nil {
		return config, fmt.Errorf("GRPO 实验配置不是合法 JSON：%w", err)
	}
	return config, nil
}
