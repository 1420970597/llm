package exporter

import (
	"bytes"
	"encoding/csv"

	"github.com/1420970597/llm/internal/model"
)

func init() {
	Register(csvExporter{})
}

// csvExporter 标准 CSV：首行表头，值内的逗号/引号/换行由 encoding/csv 正确转义。
type csvExporter struct{}

func (csvExporter) Format() string      { return "csv" }
func (csvExporter) Ext() string         { return ".csv" }
func (csvExporter) ContentType() string { return "text/csv" }

// defaultCSVSpecs 未配置映射时的默认列，顺序即表头顺序。
func defaultCSVSpecs() []FieldSpec {
	return []FieldSpec{
		{Key: "question", Source: "{{question}}"},
		{Key: "chain_of_thought", Source: "{{chainOfThought}}"},
		{Key: "answer", Source: "{{answer}}"},
		{Key: "difficulty", Source: "{{difficulty}}"},
		{Key: "domain_name", Source: "{{domainName}}"},
		{Key: "reward_score", Source: "{{rewardScore}}"},
	}
}

func (csvExporter) Encode(records []Record, mapping model.ExportMapping) ([]byte, error) {
	var buffer bytes.Buffer
	writer := csv.NewWriter(&buffer)

	// 列顺序：配置了映射时以映射解析出的第一行为准，保证表头与数据行一致。
	var header []string
	for index, record := range records {
		fields, err := Resolve(record, mapping, defaultCSVSpecs())
		if err != nil {
			return nil, err
		}
		if index == 0 {
			header = make([]string, 0, len(fields))
			for _, field := range fields {
				header = append(header, field.Key)
			}
			if err := writer.Write(header); err != nil {
				return nil, err
			}
		}
		row := make([]string, 0, len(fields))
		for _, field := range fields {
			row = append(row, field.Value)
		}
		if err := writer.Write(row); err != nil {
			return nil, err
		}
	}

	// 没有记录时也要输出表头，否则下游按列名读取会直接失败。
	if len(records) == 0 {
		fields, err := Resolve(Record{}, mapping, defaultCSVSpecs())
		if err != nil {
			return nil, err
		}
		header = make([]string, 0, len(fields))
		for _, field := range fields {
			header = append(header, field.Key)
		}
		if err := writer.Write(header); err != nil {
			return nil, err
		}
	}

	writer.Flush()
	if err := writer.Error(); err != nil {
		return nil, err
	}
	return buffer.Bytes(), nil
}
