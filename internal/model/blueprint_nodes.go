package model

import (
	"fmt"
	"sort"
	"strings"
)

// 本文件定义蓝图节点的**元数据**（Issue #160 T11）。
//
// 契约：docs/plans/atelier-implementation.md §5（蓝图节点配置规范）。
//
// 为什么需要一份显式的节点元数据，而不是让前端按节点写七个检查器：
//
//  1. **节点依赖必须由服务端校验**（§5 原文）。前端知道字段名还不够，
//     它需要知道「这个字段的约束是什么、缺了算不算错、由哪个任务交付」。
//     这些信息散在前端 = 服务端与前端各有一份「什么算合法」。
//  2. §5 要求「未接入的 GRPO 节点先只读显示能力状态，T23–T25 开通」。
//     「哪个节点还没接入、由谁开通」是**数据**，不是 JSX 里的 if。
//  3. §5 明确「禁止任意脚本节点」：节点集合是**封闭**的常量集合，
//     因此它可以被穷举、被测试、被 UI 完整覆盖。
//
// 反漂移的机制（与 TS 契约测试同一思路）：本文件的字段表与
// `BlueprintPayload` 的 Go 结构体字段由 `TestBlueprintNodeSpecsCoverPayload`
// 双向断言 —— 结构体加了字段而元数据没跟上（前端会看不到它），
// 或元数据写了不存在的字段（前端会渲染一个永远读不到值的输入框），
// 两者都会让测试失败。

// 节点可用性。与 studio 包的模块状态同义：`planned` 的节点只读展示，
// 不提供保存路径（否则用户会以为配置生效了）。
const (
	NodeAvailabilityAvailable = "available"
	NodeAvailabilityPlanned   = "planned"
)

// NodeFieldKind 决定界面用哪种控件，以及服务端如何解释约束。
type NodeFieldKind string

const (
	// FieldKindInt 整数（可带 Min/Max）。
	FieldKindInt NodeFieldKind = "int"
	// FieldKindFloat 浮点（可带 Min/Max，例如温度、权重）。
	FieldKindFloat NodeFieldKind = "float"
	// FieldKindRatio 0–1 的比例（抽检比例）。
	FieldKindRatio NodeFieldKind = "ratio"
	// FieldKindString 文本。
	FieldKindString NodeFieldKind = "string"
	// FieldKindEnum 从 Options 里选一个。
	FieldKindEnum NodeFieldKind = "enum"
	// FieldKindID 引用某个版本/连接的行 ID。
	FieldKindID NodeFieldKind = "id"
	// FieldKindIDList 引用多个行 ID（裁判连接）。
	FieldKindIDList NodeFieldKind = "idList"
	// FieldKindStringList 字符串数组（必需证据集、用途限制）。
	FieldKindStringList NodeFieldKind = "stringList"
	// FieldKindRatioMap 维度权重映射，总和必须为 1。
	FieldKindRatioMap NodeFieldKind = "ratioMap"
	// FieldKindJSON 结构化输出的字段约束（自由 JSON schema）。
	FieldKindJSON NodeFieldKind = "json"
)

// NodeFieldSpec 是一个节点字段的元数据。
type NodeFieldSpec struct {
	// Name 是 payload 里的 JSON 字段名（前端据此读写同一份结构）。
	Name string `json:"name"`
	// Label 是中文标签。
	Label string        `json:"label"`
	Kind  NodeFieldKind `json:"kind"`
	// Required 表示「执行前必须填」，不是「保存版本时必须填」。
	//
	// 这个区分是关键的：§5 允许保存只填了一部分的蓝图草稿
	//（用户可以先把结构定下来再逐项补内容），因此保存校验与执行前校验
	// 必须是两套。字段这一个值只表达后者。
	Required bool `json:"required"`
	// Min/Max 只对数值类字段有意义（0 表示不限制）。
	Min float64 `json:"min,omitempty"`
	Max float64 `json:"max,omitempty"`
	// Options 是枚举取值。
	Options []string `json:"options,omitempty"`
	// Help 是面向用户的说明（为什么这个字段重要、默认值是什么）。
	Help string `json:"help,omitempty"`
}

// BlueprintNodeSpec 是一个蓝图节点的元数据。
type BlueprintNodeSpec struct {
	// Key 是节点键，与 §5 的节点表一致，也与 URL 的 `?node=` 取值一致。
	//
	// 注意 Key 与 PayloadField **不是**同一个字符串：节点键是 snake_case
	//（`human_review`，用于 URL 与校验分派），而 payload 里的字段名是 camelCase
	//（`humanReview`，属于已冻结的文档 schema）。把两者混为一个值会让
	// 「?node=human_review 读不到配置」这种缺陷出现在界面上而看不出原因 ——
	// 因此这里显式区分，并由测试断言两者都能在各自的集合里找到。
	Key string `json:"key"`
	// PayloadField 是该节点在 `BlueprintPayload.Nodes` 里的 JSON 字段名。
	// 前端据它在同一份 payload 上读写，服务端据它做字段齐备性断言。
	PayloadField string `json:"payloadField"`
	Label        string `json:"label"`
	Caption      string `json:"caption"`
	// Availability 是 available 或 planned。
	Availability string `json:"availability"`
	// Task 是交付/维护该节点的任务号（planned 时界面必须显示）。
	Task string `json:"task"`
	// RequiresGeneration 表示该节点会真实调用模型（执行前检查要拦住缺连接）。
	RequiresGeneration bool `json:"requiresGeneration"`
	// Fields 是该节点的 typed 配置字段。
	Fields []NodeFieldSpec `json:"fields"`
}

// BlueprintNodeSpecs 返回全部节点元数据（顺序即界面顺序，与数据流一致）。
//
// 节点**键**直接复用 T04 冻结的常量（BlueprintNodeCoverage 等），不写字符串
// 字面量：`studio_docs.go` 的 typed validator 按这些键分派，两处各写一遍
// 一个字母的差异（例如 humanReview vs human_review）就会让「保存成功但
// 校验从未生效」这种最难发现的缺陷成为可能。
//
// 顺序刻意是「范围 → 标准 → 生成 → 评估 → 规则 → 人工 → 交付」：
// 它同时是用户理解成本最低的阅读顺序，也是数据流的依赖顺序。
func BlueprintNodeSpecs() []BlueprintNodeSpec {
	return []BlueprintNodeSpec{
		{
			Key:          BlueprintNodeCoverage,
			PayloadField: "coverage",
			Label:        "覆盖范围",
			Caption:      "引用覆盖版本：稳定领域/方向 ID、配额、难度配比与来源",
			Availability: NodeAvailabilityAvailable,
			Task:         "T04、T11",
			Fields: []NodeFieldSpec{
				{
					Name: "coverageVersionId", Label: "覆盖版本", Kind: FieldKindID, Required: true,
					Help: "引用版本行 ID。删除草稿方向不会破坏已引用的版本 —— 引用的不是数组下标。",
				},
			},
		},
		{
			Key:          BlueprintNodeStandard,
			PayloadField: "standard",
			Label:        "思维标准",
			Caption:      "引用标准版本：可排序步骤与每步检查点",
			Availability: NodeAvailabilityAvailable,
			Task:         "T04、T11",
			Fields: []NodeFieldSpec{
				{
					Name: "standardVersionId", Label: "标准版本", Kind: FieldKindID, Required: true,
					Help: "标准步骤的顺序会被执行侧沿用，因此排序必须显式保存为新版本。",
				},
				{
					// 草稿期内联步骤：允许先把步骤写下来再保存为标准版本。
					// 一旦 standardVersionId 非零，服务端以被引用版本为准
					//（见 BlueprintStandardNode 的注释），因此它**不是**执行前必填 ——
					// 必填会让「引用已有标准」这种正常用法被拦下。
					Name: "steps", Label: "内联步骤（草稿）", Kind: FieldKindJSON, Required: false,
					Help: "保存为标准版本前可在此直接编辑步骤；已引用标准版本时以被引用版本为准。",
				},
			},
		},
		{
			Key:          BlueprintNodeGeneration,
			PayloadField: "generation",
			Label:        "生成",
			Caption:      "模型连接（非秘密标识）、输出 schema、并发与输出上限",
			// 生成节点在 T12 接通执行侧；配置本身在 T11 就可保存与校验。
			Availability:       NodeAvailabilityAvailable,
			Task:               "T11、T12",
			RequiresGeneration: true,
			Fields: []NodeFieldSpec{
				{
					Name: "modelConnectionId", Label: "模型连接", Kind: FieldKindID, Required: true,
					Help: "只存连接 ID，不存明文密钥 —— 快照与账目都不允许出现凭证。",
				},
				{Name: "modelVersion", Label: "模型版本", Kind: FieldKindString, Required: false},
				{
					Name: "schemaVersion", Label: "样本 schema", Kind: FieldKindEnum, Required: true,
					Options: []string{"sft.sample.v1", "grpo.sample.v1"},
					Help:    "SFT 用 reasoning，GRPO 用 judge_prompt/levels/level_rubrics；两者结构不同，不能混用。",
				},
				{
					Name: "concurrency", Label: "并发", Kind: FieldKindInt, Required: true, Min: 1, Max: 32,
					Help: "1–32。更高的并发会被预算预留与供应商限流拦住，而不是在这里放开。",
				},
				{
					Name: "maxTokens", Label: "输出上限", Kind: FieldKindInt, Required: true,
					Help: "必须显式设置：没有输出上限时无法界定单次调用风险，因而无法可靠预留预算（T07）。",
				},
				{
					Name: "temperature", Label: "温度", Kind: FieldKindFloat, Required: false, Min: 0, Max: 2,
					Help: "推理型模型（gpt-5/o 系列等）会拒绝该参数；能力声明为不支持时请留空。",
				},
				{
					Name: "failurePolicy", Label: "失败策略", Kind: FieldKindEnum, Required: false,
					Options: []string{"retry_then_skip", "stop_batch"},
				},
				{
					Name: "jsonSchema", Label: "输出字段约束", Kind: FieldKindJSON, Required: false,
					Help: "留空表示用样本 schema 的默认约束。",
				},
			},
		},
		{
			Key:          BlueprintNodeEvaluation,
			PayloadField: "evaluation",
			Label:        "独立评估",
			Caption:      "裁判连接、量表权重、抽样 seed；至少一名独立裁判",
			Availability: NodeAvailabilityAvailable,
			Task:         "T11、T14",
			Fields: []NodeFieldSpec{
				{
					Name: "judgeConnectionIds", Label: "裁判连接", Kind: FieldKindIDList, Required: true,
					Help: "至少一名，且不得与生成者同源（同真实来源的别名连接不算独立）。",
				},
				{Name: "rubricVersionId", Label: "量表版本", Kind: FieldKindID, Required: true},
				{
					Name: "samplingSeed", Label: "抽样 seed", Kind: FieldKindInt, Required: false,
					Help: "固定 seed 让抽样可复现；实验创建后改 seed 不会改变既有实验。",
				},
				{
					Name: "weights", Label: "维度权重", Kind: FieldKindRatioMap, Required: false,
					Help: "总和必须为 1；缺分与真实 0 分必须区分（缺分不计入归一化分母的分子）。",
				},
				{
					Name: "missingScorePolicy", Label: "缺分策略", Kind: FieldKindEnum, Required: false,
					Options: []string{"exclude", "fail_experiment"},
				},
			},
		},
		{
			Key:          BlueprintNodeRules,
			PayloadField: "rules",
			Label:        "规则检查",
			Caption:      "引用质量策略版本：规则表达式、字段、严重度与建议动作",
			Availability: NodeAvailabilityAvailable,
			Task:         "T11、T15",
			Fields: []NodeFieldSpec{
				{Name: "qualityPolicyVersionId", Label: "质量策略版本", Kind: FieldKindID, Required: true},
			},
		},
		{
			Key:          BlueprintNodeHumanReview,
			PayloadField: "humanReview",
			Label:        "人工检查点",
			Caption:      "分派策略、必需证据集与风险范围",
			Availability: NodeAvailabilityAvailable,
			Task:         "T11、T16",
			Fields: []NodeFieldSpec{
				{
					Name: "assignment", Label: "分派策略", Kind: FieldKindEnum, Required: true,
					Options: []string{"risk-based", "all", "sampled"},
				},
				{
					Name: "requiredEvidence", Label: "必需证据集", Kind: FieldKindStringList, Required: true,
					Help: "它定义了 evidence_revision：补齐证据或发现新风险都会让它递增，使旧接纳回到待判断。",
				},
				{Name: "riskScope", Label: "风险范围", Kind: FieldKindString, Required: false},
				{
					Name: "sampleRate", Label: "抽检比例", Kind: FieldKindRatio, Required: false, Min: 0, Max: 1,
				},
			},
		},
		{
			Key:          BlueprintNodeDelivery,
			PayloadField: "delivery",
			Label:        "版本交付",
			Caption:      "引用映射版本、输出格式与用途/限制",
			Availability: NodeAvailabilityAvailable,
			Task:         "T11、T20",
			Fields: []NodeFieldSpec{
				{Name: "mappingVersionId", Label: "映射版本", Kind: FieldKindID, Required: true},
				{
					Name: "format", Label: "输出格式", Kind: FieldKindEnum, Required: true,
					// 有意**不含** parquet：internal/exporter 的 parquet 实为列式 JSONL
					//（IsRealParquet=false），把它列进允许集等于一个假承诺（T04 已记录）。
					Options: KnownExportFormats(),
				},
				{
					Name: "intendedUse", Label: "用途", Kind: FieldKindString, Required: true,
					Help: "数据卡必须写清用途；缺失会让下游误用。",
				},
				{Name: "limitations", Label: "限制", Kind: FieldKindStringList, Required: false},
			},
		},
	}
}

// BlueprintNodeSpecByKey 按节点键查元数据。
//
// 第二个返回值为 false 表示**节点不存在**（而不是「存在但没配置」）。
// 这两件事在界面上的处置完全不同：前者应返回 404，后者应显示空表单。
func BlueprintNodeSpecByKey(key string) (BlueprintNodeSpec, bool) {
	normalized := strings.TrimSpace(key)
	for _, spec := range BlueprintNodeSpecs() {
		if spec.Key == normalized {
			return spec, true
		}
	}
	return BlueprintNodeSpec{}, false
}

// NodeFieldNames 返回某个节点的字段名集合（升序）。
func (spec BlueprintNodeSpec) NodeFieldNames() []string {
	names := make([]string, 0, len(spec.Fields))
	for _, field := range spec.Fields {
		names = append(names, field.Name)
	}
	sort.Strings(names)
	return names
}

// MissingRequiredFieldsAtExecution 返回「执行前必须补齐」但当前为空的字段。
//
// 与保存版本校验分开（见 NodeFieldSpec.Required 的说明）：
// 保存允许不完整（用户要能先把结构定下来），执行不允许。
// 返回的是**字段名 + 中文说明**，供界面逐项链到对应输入框。
type NodeRequirement struct {
	Node   string `json:"node"`
	Field  string `json:"field"`
	Label  string `json:"label"`
	Reason string `json:"reason"`
}

// CheckNodeRequirements 检查「执行前」的节点配置是否齐全。
//
// values 是该节点当前 payload 的字段值（键为 JSON 字段名）。
// 这里刻意**只**做「有没有值」的判断，类型与范围的判定交给 typed validator
// （ValidateBlueprintPayload 及其同类）—— 两处都做范围判断必然漂移。
func CheckNodeRequirements(nodeKey string, values map[string]any) []NodeRequirement {
	spec, found := BlueprintNodeSpecByKey(nodeKey)
	if !found {
		return []NodeRequirement{{
			Node: nodeKey, Field: "", Label: "节点",
			Reason: "不存在的节点键（节点集合是封闭常量集合，不支持任意脚本节点）",
		}}
	}
	requirements := []NodeRequirement{}
	for _, field := range spec.Fields {
		if !field.Required {
			continue
		}
		if hasNodeValue(values[field.Name]) {
			continue
		}
		requirements = append(requirements, NodeRequirement{
			Node: spec.Key, Field: field.Name, Label: field.Label,
			Reason: fmt.Sprintf("执行前必须设置%s", field.Label),
		})
	}
	return requirements
}

// hasNodeValue 判断一个字段值是否「有内容」。
//
// 0 与空字符串在数值字段里是不同的：`samplingSeed=0` 是一个**合法取值**
// （默认 seed），因此这里不能把所有零值都当成「空」。真正需要拦住的是
// 「未设置」的形态：nil、缺失、空字符串、空数组、空 map。
// 数值型必填字段由 typed validator 判范围（例如 concurrency 必须 ≥1）。
func hasNodeValue(value any) bool {
	switch typed := value.(type) {
	case nil:
		return false
	case string:
		return strings.TrimSpace(typed) != ""
	case []any:
		return len(typed) > 0
	case []string:
		return len(typed) > 0
	case []int64:
		return len(typed) > 0
	case map[string]any:
		return len(typed) > 0
	case map[string]float64:
		return len(typed) > 0
	default:
		return true
	}
}
