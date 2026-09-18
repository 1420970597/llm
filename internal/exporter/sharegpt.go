package exporter

import (
	"bytes"
	"encoding/json"

	"github.com/1420970597/llm/internal/model"
)

func init() {
	Register(sharegptExporter{})
}

// sharegptExporter ShareGPT 多轮对话格式。
//
// 输出结构固定为 ShareGPT 生态的既有约定（conversations / from / value），
// 不做映射改写；用户通过映射配置的是 human 与 gpt 两个轮次的内容来源。
type sharegptExporter struct{}

func (sharegptExporter) Format() string      { return "sharegpt" }
func (sharegptExporter) Ext() string         { return ".jsonl" }
func (sharegptExporter) ContentType() string { return "application/x-ndjson" }

func defaultShareGPTSpecs() []FieldSpec {
	return []FieldSpec{
		{Key: "human", Source: "{{question}}"},
		{Key: "gpt", Source: "{{chainOfThought}}\n\n答案：{{answer}}"},
	}
}

type shareGPTTurn struct {
	From  string `json:"from"`
	Value string `json:"value"`
}

type shareGPTConversation struct {
	Conversations []shareGPTTurn `json:"conversations"`
}

func (sharegptExporter) Encode(records []Record, mapping model.ExportMapping) ([]byte, error) {
	var buffer bytes.Buffer
	for _, record := range records {
		fields, err := Resolve(record, mapping, defaultShareGPTSpecs())
		if err != nil {
			return nil, err
		}

		// 按固定语义挑选 human 轮与 gpt 轮，不依赖用户配置的字段顺序。
		byKey := make(map[string]string, len(fields))
		for _, field := range fields {
			byKey[normalizeKey(field.Key)] = field.Value
		}
		human, hasHuman := byKey["human"]
		if !hasHuman {
			return nil, &MappingError{Field: "human", Reason: "sharegpt 映射必须包含 human 轮"}
		}
		assistant, hasAssistant := byKey["gpt"]
		if !hasAssistant {
			return nil, &MappingError{Field: "gpt", Reason: "sharegpt 映射必须包含 gpt 轮"}
		}

		payload := shareGPTConversation{Conversations: []shareGPTTurn{
			{From: "human", Value: human},
			{From: "gpt", Value: assistant},
		}}
		line, err := json.Marshal(payload)
		if err != nil {
			return nil, err
		}
		buffer.Write(line)
		buffer.WriteByte('\n')
	}
	return buffer.Bytes(), nil
}
