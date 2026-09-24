package model

import "strings"

// DefaultProjectDocuments returns a complete, editable starting configuration
// for a new project. References are filled by the document bootstrap service
// after the leaf documents have been saved; connection IDs remain zero until a
// real administrator connection is available.
func DefaultProjectDocuments(targetKind, name, goal string) (CoveragePayload, StandardPayload, QualityPolicyPayload, MappingPayload, BlueprintPayload) {
	if targetKind != TargetKindGRPO {
		targetKind = TargetKindSFT
	}
	topic := strings.TrimSpace(goal)
	if topic == "" {
		topic = strings.TrimSpace(name)
	}
	if topic == "" {
		topic = "待定义主题"
	}

	coverage := CoveragePayload{
		SchemaVersion: SchemaVersionFor(KindCoverage),
		Domains: []CoverageDomain{{
			StableID: "domain-1", Name: topic,
			Directions: []CoverageDirection{{
				StableID: "direction-1", Name: "核心方向", Quota: 1,
				DifficultyRatios: []DifficultyRatio{{Difficulty: "medium", Ratio: 100}},
				Source:           "project-default",
			}},
		}},
	}
	standard := StandardPayload{
		SchemaVersion: SchemaVersionFor(KindStandard),
		Steps: []StandardStep{
			{ID: "goal", Title: "确认任务目标", Detail: "明确问题要回答什么以及输出边界。", Checkpoint: "目标与题目内容一致，且没有超出项目范围。", Order: 1},
			{ID: "constraints", Title: "提取关键约束", Detail: "列出事实、限制条件和必须覆盖的风险。", Checkpoint: "关键约束均可在答案中找到对应依据。", Order: 2},
			{ID: "solution", Title: "构建候选方案", Detail: "按步骤形成可执行、可复核的结论。", Checkpoint: "方案包含操作步骤与必要的例外处理。", Order: 3},
			{ID: "verify", Title: "验证结论", Detail: "检查完整性、冲突和不确定性。", Checkpoint: "结论有明确验证结果，未知项没有被伪装成事实。", Order: 4},
		},
	}
	quality := QualityPolicyPayload{
		SchemaVersion: SchemaVersionFor(KindQualityPolicy),
		Rules: []QualityRule{{
			ID: "structure-required", Name: "结构完整性", MatchType: RuleMatchStructure,
			Field: "answer", Severity: RuleSeverityWarning, SuggestedAction: RuleActionReview,
		}},
	}
	fields := []MappingField{{TargetField: "question", SourceField: "question", Required: true}}
	if targetKind == TargetKindGRPO {
		fields = append(fields,
			MappingField{TargetField: "judge_prompt", SourceField: "judgePrompt", Required: true},
			MappingField{TargetField: "levels", SourceField: "levels", Required: true},
			MappingField{TargetField: "level_rubrics", SourceField: "levelRubrics", Required: true},
			MappingField{TargetField: "framework_ref", SourceField: "frameworkRef", Required: false},
		)
	} else {
		fields = append(fields,
			MappingField{TargetField: "reasoning", SourceField: "reasoning", Required: true},
			MappingField{TargetField: "answer", SourceField: "answer", Required: true},
		)
	}
	mapping := MappingPayload{SchemaVersion: SchemaVersionFor(KindMapping), Format: ExportFormatJSONL, Fields: fields}
	blueprint := BlueprintPayload{SchemaVersion: SchemaVersionFor(KindBlueprint)}
	blueprint.Nodes.Generation.SchemaVersion = SampleSchemaForTarget(targetKind)
	blueprint.Nodes.Generation.Concurrency = 4
	blueprint.Nodes.Generation.MaxTokens = 1024
	blueprint.Nodes.Generation.FailurePolicy = "retry_then_skip"
	blueprint.Nodes.Evaluation.SamplingSeed = 42
	blueprint.Nodes.Evaluation.Weights = map[string]float64{"accuracy": 1}
	blueprint.Nodes.Evaluation.MissingScorePolicy = "exclude"
	blueprint.Nodes.HumanReview.Assignment = "risk-based"
	blueprint.Nodes.HumanReview.RequiredEvidence = []string{"quality_report", "rule_preview"}
	blueprint.Nodes.HumanReview.RiskScope = "全量"
	rate := 0.2
	blueprint.Nodes.HumanReview.SampleRate = &rate
	blueprint.Nodes.Delivery.Format = ExportFormatJSONL
	blueprint.Nodes.Delivery.IntendedUse = "模型训练与离线评测"
	blueprint.Nodes.Delivery.Limitations = []string{"初始配置需由项目成员复核后再执行"}
	return coverage, standard, quality, mapping, blueprint
}
