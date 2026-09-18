package exporter

import (
	"bytes"

	"github.com/1420970597/llm/internal/model"
)

func init() {
	Register(alpacaExporter{})
}

// alpacaExporter 标准 Alpaca SFT 格式。
//
// 默认语义（对应内置映射 sft-alpaca）：
//
//	instruction = 问题
//	input       = 空（长链思考任务的输入已经完整包含在 instruction 里）
//	output      = 思维链 + 答案
//
// 配置了映射时以映射为准，字段名可由用户自定义。
type alpacaExporter struct{}

func (alpacaExporter) Format() string      { return "alpaca" }
func (alpacaExporter) Ext() string         { return ".jsonl" }
func (alpacaExporter) ContentType() string { return "application/x-ndjson" }

func defaultAlpacaSpecs() []FieldSpec {
	return []FieldSpec{
		{Key: "instruction", Source: "{{question}}"},
		{Key: "input", Source: "const:"},
		{Key: "output", Source: "{{chainOfThought}}\n\n答案：{{answer}}"},
	}
}

func (alpacaExporter) Encode(records []Record, mapping model.ExportMapping) ([]byte, error) {
	var buffer bytes.Buffer
	for _, record := range records {
		fields, err := Resolve(record, mapping, defaultAlpacaSpecs())
		if err != nil {
			return nil, err
		}
		line, err := encodeOrderedObject(fields)
		if err != nil {
			return nil, err
		}
		buffer.Write(line)
		buffer.WriteByte('\n')
	}
	return buffer.Bytes(), nil
}
