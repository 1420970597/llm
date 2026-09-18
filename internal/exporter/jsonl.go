package exporter

import (
	"bytes"
	"encoding/json"

	"github.com/1420970597/llm/internal/model"
)

func init() {
	Register(jsonlExporter{})
}

// jsonlExporter 每行一个 JSON 对象，字段按映射展开。
type jsonlExporter struct{}

func (jsonlExporter) Format() string      { return "jsonl" }
func (jsonlExporter) Ext() string         { return ".jsonl" }
func (jsonlExporter) ContentType() string { return "application/x-ndjson" }

// defaultJSONLSpecs 未配置映射时的默认字段，顺序即输出列顺序。
func defaultJSONLSpecs() []FieldSpec {
	return []FieldSpec{
		{Key: "question", Source: "{{question}}"},
		{Key: "chain_of_thought", Source: "{{chainOfThought}}"},
		{Key: "answer", Source: "{{answer}}"},
		{Key: "judge_prompt", Source: "{{judgePrompt}}"},
		{Key: "difficulty", Source: "{{difficulty}}"},
		{Key: "domain_name", Source: "{{domainName}}"},
		{Key: "reward_score", Source: "{{rewardScore}}"},
		{Key: "dataset_name", Source: "{{datasetName}}"},
	}
}

func (jsonlExporter) Encode(records []Record, mapping model.ExportMapping) ([]byte, error) {
	var buffer bytes.Buffer
	for _, record := range records {
		fields, err := Resolve(record, mapping, defaultJSONLSpecs())
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

// encodeOrderedObject 按给定字段顺序编码一个 JSON 对象。
//
// 为什么不直接用 map + json.Marshal：Go 的 map 序列化按字典序输出键，
// 会让用户配置的字段顺序失效，且多次导出结果不稳定。
func encodeOrderedObject(fields []Field) ([]byte, error) {
	var buffer bytes.Buffer
	buffer.WriteByte('{')
	for index, field := range fields {
		if index > 0 {
			buffer.WriteByte(',')
		}
		key, err := json.Marshal(field.Key)
		if err != nil {
			return nil, err
		}
		buffer.Write(key)
		buffer.WriteByte(':')

		if field.Raw != nil {
			raw, err := json.Marshal(field.Raw)
			if err != nil {
				return nil, err
			}
			buffer.Write(raw)
			continue
		}
		value, err := json.Marshal(field.Value)
		if err != nil {
			return nil, err
		}
		buffer.Write(value)
	}
	buffer.WriteByte('}')
	return buffer.Bytes(), nil
}
