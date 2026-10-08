package model_test

import (
	"encoding/json"
	"testing"

	"github.com/1420970597/llm/internal/exporter"
	"github.com/1420970597/llm/internal/model"
)

func TestDefaultGRPODocumentsExportStructuredJSON(t *testing.T) {
	_, _, _, mapping, blueprint := model.DefaultProjectDocuments(model.TargetKindGRPO, "退货", "培训")
	if err := model.ValidateMappingPayload(mapping); err != nil {
		t.Fatal(err)
	}
	if blueprint.Nodes.Generation.SchemaVersion != model.SampleSchemaGRPO {
		t.Fatal("GRPO default must use the GRPO sample contract")
	}
	fields := make(map[string]any, len(mapping.Fields))
	for _, field := range mapping.Fields {
		fields[field.TargetField] = field.SourceField
	}
	encoder, found := exporter.Get(mapping.Format)
	if !found {
		t.Fatal("default format is not registered")
	}
	bytes, err := encoder.Encode([]exporter.Record{{
		Question: "冷链异常如何处理？", JudgePrompt: "按照证据完整性评分。", RewardLevels: []string{"不合格", "合格"},
		LevelRubrics: []model.GRPORubricExport{
			{Level: "不合格", Criteria: "遗漏温度记录", RejectCase: "直接承诺退款"},
			{Level: "合格", Criteria: "记录温度并交质量组审核", AcceptCase: "上传温度记录"},
		},
	}}, model.ExportMapping{Format: mapping.Format, FieldMap: fields})
	if err != nil {
		t.Fatal(err)
	}
	if count, err := model.VerifyGRPOExportLines(bytes); err != nil || count != 1 {
		t.Fatalf("default GRPO mapping must produce a valid typed row: count=%d err=%v", count, err)
	}
	var row model.GRPOExportLine
	if err := json.Unmarshal(bytes, &row); err != nil {
		t.Fatal(err)
	}
	if row.Levels[1] != "合格" || row.LevelRubrics[1].AcceptCase != "上传温度记录" {
		t.Fatalf("default mapping lost rubric content: %+v", row)
	}
}

func TestDefaultDocumentsUnknownTargetFallsBackToSFT(t *testing.T) {
	for _, target := range []string{model.TargetKindSFT, "unknown"} {
		t.Run(target, func(t *testing.T) {
			coverage, _, _, mapping, blueprint := model.DefaultProjectDocuments(target, "", " ")
			if blueprint.Nodes.Generation.SchemaVersion != model.SampleSchemaSFT || coverage.Domains[0].Name != "待定义主题" {
				t.Fatal("SFT and unknown target with blank topic must use editable SFT defaults")
			}
			if err := model.ValidateMappingPayload(mapping); err != nil {
				t.Fatal(err)
			}
			fields := make(map[string]any, len(mapping.Fields))
			for _, field := range mapping.Fields {
				fields[field.TargetField] = field.SourceField
			}
			encoder, found := exporter.Get(mapping.Format)
			if !found {
				t.Fatal("default format is not registered")
			}
			bytes, err := encoder.Encode([]exporter.Record{{Question: "题目", ChainOfThought: "推理", Answer: "答案"}}, model.ExportMapping{Format: mapping.Format, FieldMap: fields})
			if err != nil {
				t.Fatal(err)
			}
			var row map[string]string
			if err := json.Unmarshal(bytes, &row); err != nil {
				t.Fatal(err)
			}
			if len(row) != 3 || row["question"] != "题目" || row["reasoning"] != "推理" || row["answer"] != "答案" {
				t.Fatalf("SFT defaults lost existing text mapping: %+v", row)
			}
		})
	}
}
