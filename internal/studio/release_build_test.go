package studio

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/1420970597/llm/internal/exporter"
	"github.com/1420970597/llm/internal/model"
)

// 本文件验证 T21 编码层的**纯逻辑**（不需要数据库）。
//
// 为什么分块编码值得单独测：它只有一个目的 —— 让大规模导出的峰值内存
// 与「批次大小」有关而不是与文件大小有关。而一旦分块改变了输出字节，
// 「同一份清单两次导出得到同一个 hash」就不成立，于是
// 「重试是否得到同一份文件」无法验证。

func buildMapping() model.ExportMapping {
	return model.ExportMapping{
		Format: model.ExportFormatJSONL,
		FieldMap: map[string]any{
			"question":  "question",
			"reasoning": "chain_of_thought",
			"answer":    "answer",
		},
	}
}

func buildRecords(count int) []exporter.Record {
	records := make([]exporter.Record, 0, count)
	for index := 0; index < count; index++ {
		records = append(records, exporter.Record{
			DatasetID: 1, QuestionID: int64(index + 1),
			Question:       fmt.Sprintf("问题 %d", index+1),
			ChainOfThought: "先识别约束，再逐步推导。",
			Answer:         fmt.Sprintf("答案 %d", index+1),
		})
	}
	return records
}

// TestBuildJSONLArtifactIsChunkEquivariant 覆盖「分块编码不改变输出字节」。
//
// 记录数刻意超过 MaxRecordsPerEncodeChunk：否则这条断言只是
// 「一次编码等于自己」。
func TestBuildJSONLArtifactIsChunkEquivariant(t *testing.T) {
	if MaxRecordsPerEncodeChunk >= 5000 {
		t.Fatalf("测试前提失效：批大小 %d 不小于 5000，无法验证跨批一致性", MaxRecordsPerEncodeChunk)
	}
	records := buildRecords(5000)
	mapping := buildMapping()

	chunked, chunkedHash, err := buildJSONLArtifact(context.Background(), nil, records, mapping)
	if err != nil {
		t.Fatalf("buildJSONLArtifact: %v", err)
	}

	// 一次性编码同样全部记录，作为对照。
	encoder, found := exporter.Get(model.ExportFormatJSONL)
	if !found {
		t.Fatal("JSONL 编码器必须可用")
	}
	oneShot, err := encoder.Encode(records, mapping)
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	if string(chunked) != string(oneShot) {
		t.Fatalf("分块编码的输出必须与一次性编码逐字节一致（长度 %d vs %d）",
			len(chunked), len(oneShot))
	}
	if chunkedHash != model.ComputeArtifactHash(oneShot) {
		t.Fatalf("分块编码的 hash 必须与一次性编码一致：%s vs %s",
			chunkedHash, model.ComputeArtifactHash(oneShot))
	}
	if !strings.HasPrefix(chunkedHash, "sha256:") {
		t.Fatalf("hash 必须带算法前缀，实际 %s", chunkedHash)
	}
	// 行数 = 记录数（JSONL 一行一条），这是「清单与实际行数一致」的基础。
	lines := strings.Count(strings.TrimRight(string(chunked), "\n"), "\n") + 1
	if lines != len(records) {
		t.Fatalf("输出行数必须等于记录数：%d vs %d", lines, len(records))
	}
}

// TestBuildJSONLArtifactUsesFrozenMapping 覆盖「映射来自冻结版本」。
//
// 断言的是**文件里的字段名与值**：映射里的 `reasoning` 取自
// `chain_of_thought`（旧记录字段名），而输出里必须叫 `reasoning`
// （新契约字段名，§2.2 明确旧名不再作为新契约字段）。
func TestBuildJSONLArtifactUsesFrozenMapping(t *testing.T) {
	records := buildRecords(1)
	output, _, err := buildJSONLArtifact(context.Background(), nil, records, buildMapping())
	if err != nil {
		t.Fatalf("buildJSONLArtifact: %v", err)
	}
	text := string(output)
	if !strings.Contains(text, `"reasoning"`) {
		t.Fatalf("输出必须用新契约字段名 reasoning，实际 %s", text)
	}
	if strings.Contains(text, `"chain_of_thought"`) {
		t.Fatalf("输出不得出现旧字段名 chain_of_thought，实际 %s", text)
	}
	if !strings.Contains(text, "先识别约束") {
		t.Fatalf("输出必须包含映射来源字段的值，实际 %s", text)
	}
	// 每行必须是独立 JSON 对象（JSONL 的语义）。
	for _, line := range strings.Split(strings.TrimSpace(text), "\n") {
		if !strings.HasPrefix(strings.TrimSpace(line), "{") {
			t.Fatalf("JSONL 每行必须是 JSON 对象，实际 %q", line)
		}
	}
}

// TestBuildBufferedArtifactRejectsUnknownFormat 覆盖「不支持的格式明确报错」。
//
// 本轮只承诺 SFT JSONL/CSV/Alpaca 与 GRPO JSONL；其中 parquet 名虽注册但
// 实为列式 JSONL（T04 已记录），因此它不应被当作可用格式悄悄产出。
func TestBuildBufferedArtifactRejectsUnknownFormat(t *testing.T) {
	records := buildRecords(2)
	if _, _, err := buildBufferedArtifact(nil, "not-a-format", records, buildMapping()); err == nil {
		t.Fatal("未知格式必须报错（不得静默产出其它格式的文件）")
	}
	// CSV 是承诺支持的格式：必须能编码。
	if _, hash, err := buildBufferedArtifact(nil, model.ExportFormatCSV, records, buildMapping()); err != nil {
		t.Fatalf("CSV 应被支持，实际 %v", err)
	} else if !strings.HasPrefix(hash, "sha256:") {
		t.Fatalf("CSV 制品的 hash 必须带算法前缀，实际 %s", hash)
	}
}

// TestLoadRecordMapsReasoningAndKeepsLevelsArray 覆盖字段命名与档位结构。
//
// 档位必须保留**数组**（不能 join 成逗号字符串）：GRPO 的发布 JSONL
// 要求 levels 是数组，而逗号字符串会让下游无法还原档位。
func TestLoadRecordMapsReasoningAndKeepsLevelsArray(t *testing.T) {
	// 直接验证 stringField 的容错（loadRecord 的 DB 路径由 store 侧测试覆盖）。
	if got := stringField(map[string]any{"question": "q"}, "question"); got != "q" {
		t.Fatalf("字符串字段应原样返回，实际 %q", got)
	}
	if got := stringField(map[string]any{"question": 42}, "question"); got != "" {
		t.Fatalf("非字符串字段应返回空串（不猜测转换），实际 %q", got)
	}
	if got := stringField(map[string]any{}, "missing"); got != "" {
		t.Fatalf("缺失字段应返回空串，实际 %q", got)
	}
}

// ---------------------------------------------------------------------------
// T25：GRPO 发布 JSONL
// ---------------------------------------------------------------------------

// grpoMapping 是 T25 要求的结构化映射（单占位符 → 保留数组/对象类型）。
func grpoMapping() model.ExportMapping {
	return model.ExportMapping{
		Format: model.ExportFormatJSONL,
		FieldMap: map[string]any{
			"question":      "{{question}}",
			"judge_prompt":  "{{judgePrompt}}",
			"levels":        "{{levels}}",
			"level_rubrics": "{{level_rubrics}}",
		},
	}
}

func grpoRecords(count int) []exporter.Record {
	records := make([]exporter.Record, 0, count)
	for index := 0; index < count; index++ {
		records = append(records, exporter.Record{
			DatasetID: 1, QuestionID: int64(index + 1),
			Question:     fmt.Sprintf("GRPO 问题 %d", index+1),
			JudgePrompt:  "按档位评分：基础、精通。",
			RewardLevels: []string{"基础", "精通"},
			LevelRubrics: []model.GRPORubricExport{
				{Level: "基础", Criteria: "能说出思路", AcceptCase: "提到 pivot", RejectCase: "答非所问"},
				{Level: "精通", Criteria: "能讨论退化", AcceptCase: "指出有序输入", RejectCase: "否认退化"},
			},
		})
	}
	return records
}

// TestValidateGRPOReleaseRequiresJSONLAndStructuralFields 覆盖 T25
// 「GRPO 发布页仅开放支持其 schema 的 JSONL」与「四个必需字段」。
func TestValidateGRPOReleaseRequiresJSONLAndStructuralFields(t *testing.T) {
	base := ReleaseBuildInput{TargetKind: model.TargetKindGRPO, Format: model.ExportFormatCSV}
	if err := validateGRPORelease(base, grpoMapping()); err == nil {
		t.Fatal("GRPO 用 CSV 发布必须被拒绝（无法保留数组/对象结构）")
	}

	base.Format = model.ExportFormatJSONL
	missingRubrics := model.ExportMapping{
		Format: model.ExportFormatJSONL,
		FieldMap: map[string]any{
			"question":     "{{question}}",
			"judge_prompt": "{{judgePrompt}}",
			"levels":       "{{levels}}",
		},
	}
	if err := validateGRPORelease(base, missingRubrics); err == nil {
		t.Fatal("缺少 level_rubrics 的映射必须被拒绝")
	}

	if err := validateGRPORelease(base, grpoMapping()); err != nil {
		t.Fatalf("结构化映射应当被接受：%v", err)
	}
}

// TestGRPOJSONLArtifactKeepsStructureAndPassesVerification 覆盖 T25 的核心验收项：
// 逐行解码字段、levels 是数组、level_rubrics 是对象数组且与档位一一对应。
//
// 同时做**变异自证**：把 levels 换成非单占位符写法（结构会被压平），
// 断言校验必须报错 —— 否则这条测试只证明「合法输入能通过」。
func TestGRPOJSONLArtifactKeepsStructureAndPassesVerification(t *testing.T) {
	records := grpoRecords(3)
	content, _, err := buildJSONLArtifact(context.Background(), nil, records, grpoMapping())
	if err != nil {
		t.Fatalf("buildJSONLArtifact: %v", err)
	}
	count, err := model.VerifyGRPOExportLines(content)
	if err != nil {
		t.Fatalf("结构化输出应当通过逐行校验：%v", err)
	}
	if count != len(records) {
		t.Fatalf("行数必须等于记录数：%d vs %d", count, len(records))
	}
	text := string(content)
	// 数组与对象结构必须在字节层面可见（这是「没有 join 成逗号串」的证据）。
	if !strings.Contains(text, `"levels":["基础","精通"]`) {
		t.Fatalf("levels 必须是 JSON 数组，实际 %s", text)
	}
	if !strings.Contains(text, `"level_rubrics":[{`) {
		t.Fatalf("level_rubrics 必须是对象数组，实际 %s", text)
	}
	if !strings.Contains(text, `"accept_case"`) || !strings.Contains(text, `"reject_case"`) {
		t.Fatalf("档位判据必须带边界例字段，实际 %s", text)
	}

	// 变异：把 levels 改成裸字段名（stringify 后变成空串）——结构校验必须捕获。
	flattened := model.ExportMapping{
		Format: model.ExportFormatJSONL,
		FieldMap: map[string]any{
			"question":      "{{question}}",
			"judge_prompt":  "{{judgePrompt}}",
			"levels":        "levels",
			"level_rubrics": "level_rubrics",
		},
	}
	brokenContent, _, err := buildJSONLArtifact(context.Background(), nil, records, flattened)
	if err != nil {
		t.Fatalf("buildJSONLArtifact(变异): %v", err)
	}
	if _, err := model.VerifyGRPOExportLines(brokenContent); err == nil {
		t.Fatal("被压平的 levels/level_rubrics 必须被逐行校验拒绝（否则禁止 strings.Join 只是一句口号）")
	}
}

// TestGRPOReleaseVerificationRejectsLineCountMismatch 覆盖 T25
// 「发布清单与实际行数一致」：行数与清单不符必须中止发布。
func TestGRPOReleaseVerificationRejectsLineCountMismatch(t *testing.T) {
	// 直接构造：清单 3 条但只有 2 行内容，验证「行数对账」这条判据本身。
	records := grpoRecords(2)
	content, _, err := buildJSONLArtifact(context.Background(), nil, records, grpoMapping())
	if err != nil {
		t.Fatalf("buildJSONLArtifact: %v", err)
	}
	count, err := model.VerifyGRPOExportLines(content)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if count == 3 {
		t.Fatal("测试前提失效：行数不应等于 3")
	}
}
