package exporter

import (
	"bytes"
	"encoding/json"
	"strconv"

	"github.com/1420970597/llm/internal/model"
)

func init() {
	Register(parquetExporter{})
}

// parquetExporter 列式导出。
//
// ⚠️ 尚未使用真正的 Parquet 文件格式，原因与现状：
//
//	Go 写 Parquet 必须引入第三方库（github.com/parquet-go/parquet-go）。
//	本 lane 的任务书明确要求「go.mod 若没有 parquet 依赖就不要擅自添加，
//	改为实现 Parquet 兼容的列式 JSONL 并在报告里申请批准」。
//	当前 go.mod 无该依赖，因此这里实现的是**列式 JSONL**：
//	每行是一个列对象 {"column":..., "type":..., "values":[...]}，
//	数据按列组织（与 Parquet 的列存语义一致，便于按列读取），
//	但文件本身不是 Parquet 格式，不能直接被 pyarrow/pandas 读取。
//
// 待父代理批准引入依赖后，只需替换本文件的 Encode 实现，
// 其余调用方（路由、worker、格式注册表）无需改动。
type parquetExporter struct{}

func (parquetExporter) Format() string { return "parquet" }
func (parquetExporter) Ext() string    { return ".jsonl" }

// ContentType 使用自定义类型，明确区分于真正的 application/vnd.apache.parquet，
// 避免下游把列式 JSONL 误当成 Parquet 文件解析。
func (parquetExporter) ContentType() string { return "application/x-parquet-jsonl" }

// IsRealParquet 供调用方与测试断言当前实现是否为真 Parquet。
const IsRealParquet = false

func defaultParquetSpecs() []FieldSpec {
	return []FieldSpec{
		{Key: "question", Source: "{{question}}"},
		{Key: "chain_of_thought", Source: "{{chainOfThought}}"},
		{Key: "answer", Source: "{{answer}}"},
		{Key: "difficulty", Source: "{{difficulty}}"},
		{Key: "domain_name", Source: "{{domainName}}"},
		{Key: "reward_score", Source: "{{rewardScore}}"},
	}
}

// parquetColumn 列式输出的单个列。
type parquetColumn struct {
	Column string `json:"column"`
	Type   string `json:"type"`
	Values []any  `json:"values"`
}

func (parquetExporter) Encode(records []Record, mapping model.ExportMapping) ([]byte, error) {
	// 先按行解析，再转置成列。列顺序由第一条记录解析出的字段顺序决定，
	// 保证同一份映射下多次导出的列顺序稳定。
	var columnNames []string
	rows := make([][]Field, 0, len(records))

	// 记录为空时也要产出一份列定义，否则下游无法得知 schema。
	if len(records) == 0 {
		fields, err := Resolve(Record{}, mapping, defaultParquetSpecs())
		if err != nil {
			return nil, err
		}
		for _, field := range fields {
			columnNames = append(columnNames, field.Key)
		}
		return encodeColumns(columnNames, rows)
	}

	for _, record := range records {
		fields, err := Resolve(record, mapping, defaultParquetSpecs())
		if err != nil {
			return nil, err
		}
		if columnNames == nil {
			for _, field := range fields {
				columnNames = append(columnNames, field.Key)
			}
		}
		rows = append(rows, fields)
	}
	return encodeColumns(columnNames, rows)
}

// encodeColumns 把按行解析的字段转置为列并序列化成列式 JSONL。
func encodeColumns(columnNames []string, rows [][]Field) ([]byte, error) {
	// 收集每列的值。Raw 非 nil 时保留原始类型（数值列不会退化成字符串）。
	rawColumns := make([][]any, len(columnNames))
	for index := range columnNames {
		rawColumns[index] = make([]any, 0, len(rows))
	}
	for _, row := range rows {
		for index := range columnNames {
			if index >= len(row) {
				rawColumns[index] = append(rawColumns[index], "")
				continue
			}
			field := row[index]
			if field.Raw != nil {
				rawColumns[index] = append(rawColumns[index], field.Raw)
				continue
			}
			rawColumns[index] = append(rawColumns[index], field.Value)
		}
	}

	var buffer bytes.Buffer
	for index, name := range columnNames {
		column := parquetColumn{
			Column: name,
			Type:   inferColumnType(rawColumns[index]),
			Values: rawColumns[index],
		}
		line, err := json.Marshal(column)
		if err != nil {
			return nil, err
		}
		buffer.Write(line)
		buffer.WriteByte('\n')
	}
	return buffer.Bytes(), nil
}

// inferColumnType 推断列类型。空列与混合列一律按字符串处理，
// 避免下游按数值列读取时遇到空值报错。
func inferColumnType(values []any) string {
	if len(values) == 0 {
		return "string"
	}
	for _, value := range values {
		switch typed := value.(type) {
		case float64:
			if _, err := strconv.ParseFloat(strconv.FormatFloat(typed, 'f', -1, 64), 64); err != nil {
				return "string"
			}
		case int64, int:
		default:
			return "string"
		}
	}
	return "number"
}
