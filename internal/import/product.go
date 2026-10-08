package importer

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/1420970597/llm/internal/model"
)

type ImportFailure struct {
	SourceID int64  `json:"sourceId"`
	Reason   string `json:"reason"`
}
type MappedSample struct {
	SourceID    int64
	SampleKey   string
	ContentHash string
	Payload     map[string]any
}
type ProductPreview struct {
	SourceItems    int             `json:"sourceItems"`
	ValidItems     int             `json:"validItems"`
	DuplicateItems int             `json:"duplicateItems"`
	FailedItems    int             `json:"failedItems"`
	Failures       []ImportFailure `json:"failures"`
}

func MapProductRows(format string, content []byte) ([]MappedSample, ProductPreview, error) {
	preview := ProductPreview{Failures: []ImportFailure{}}
	if format != "alpaca" && format != "sharegpt" && format != "jsonl" {
		return nil, preview, fmt.Errorf("format 必须是 alpaca、sharegpt 或 jsonl")
	}
	if len(content) > MaxProductUploadBytes {
		return nil, preview, fmt.Errorf("产物文件不能超过 20 MB")
	}
	if !utf8.Valid(content) {
		return nil, preview, fmt.Errorf("产物不是有效的 UTF-8 文本，请转换编码后重试")
	}
	content = bytes.TrimSpace(bytes.TrimPrefix(content, []byte{0xef, 0xbb, 0xbf}))
	rows := []json.RawMessage{}
	ids := []int64{}
	if len(content) > 0 && content[0] == '[' {
		if err := json.Unmarshal(content, &rows); err != nil {
			return nil, preview, fmt.Errorf("JSON 数组格式不正确：%w", err)
		}
		for i := range rows {
			ids = append(ids, int64(i+1))
		}
	} else {
		scanner := bufio.NewScanner(bytes.NewReader(content))
		scanner.Buffer(make([]byte, 65536), MaxProductUploadBytes)
		line := int64(0)
		for scanner.Scan() {
			line++
			if len(bytes.TrimSpace(scanner.Bytes())) == 0 {
				continue
			}
			rows = append(rows, append(json.RawMessage(nil), scanner.Bytes()...))
			ids = append(ids, line)
		}
		if err := scanner.Err(); err != nil {
			return nil, preview, fmt.Errorf("无法读取 JSONL：%w", err)
		}
	}
	if len(rows) > MaxProductRows {
		return nil, preview, fmt.Errorf("单次最多导入 5000 条，请拆分文件")
	}
	seen := map[string]bool{}
	mapped := []MappedSample{}
	for i, raw := range rows {
		preview.SourceItems++
		payload, err := mapProductRow(format, raw)
		if err != nil {
			preview.FailedItems++
			if len(preview.Failures) < MaxRecordedFailures {
				preview.Failures = append(preview.Failures, ImportFailure{SourceID: ids[i], Reason: err.Error()})
			}
			continue
		}
		hash, err := model.ContentHash(payload)
		if err != nil {
			return nil, preview, err
		}
		if seen[hash] {
			preview.DuplicateItems++
		} else {
			preview.ValidItems++
		}
		seen[hash] = true
		mapped = append(mapped, MappedSample{SourceID: ids[i], SampleKey: "product-source_product-" + hash, ContentHash: hash, Payload: payload})
	}
	return mapped, preview, nil
}

func mapProductRow(format string, raw json.RawMessage) (map[string]any, error) {
	var row map[string]json.RawMessage
	if err := json.Unmarshal(raw, &row); err != nil || row == nil {
		return nil, fmt.Errorf("这一行必须是 JSON 对象，请检查括号和引号")
	}
	if format == "jsonl" {
		if _, ok := row["instruction"]; ok {
			format = "alpaca"
		} else if _, ok := row["conversations"]; ok {
			format = "sharegpt"
		}
	}
	read := func(key string, required bool) (string, error) {
		data, ok := row[key]
		if !ok {
			if required {
				return "", fmt.Errorf("缺少 %s 字段，请补齐后重试", key)
			}
			return "", nil
		}
		var value string
		if err := json.Unmarshal(data, &value); err != nil {
			return "", fmt.Errorf("%s 必须是字符串", key)
		}
		if strings.IndexByte(value, 0) >= 0 {
			return "", fmt.Errorf("%s 含有 NUL 二进制字符", key)
		}
		if required && strings.TrimSpace(value) == "" {
			return "", fmt.Errorf("%s 不能为空", key)
		}
		return strings.TrimSpace(value), nil
	}
	var question, answer, reasoning string
	payload := map[string]any{"source": "external_import"}
	switch format {
	case "alpaca":
		var err error
		question, err = read("instruction", true)
		if err != nil {
			return nil, err
		}
		input, err := read("input", false)
		if err != nil {
			return nil, err
		}
		if input != "" {
			question += "\n\n" + input
		}
		answer, err = read("output", true)
		if err != nil {
			return nil, err
		}
		reasoning, err = read("reasoning", false)
		if err != nil {
			return nil, err
		}
	case "sharegpt":
		var messages []struct {
			From  string `json:"from"`
			Value string `json:"value"`
		}
		if err := json.Unmarshal(row["conversations"], &messages); err != nil || len(messages) < 2 {
			return nil, fmt.Errorf("conversations 需要包含 human/gpt 消息数组")
		}
		expected := "human"
		turns := 0
		for i, m := range messages {
			if strings.IndexByte(m.Value, 0) >= 0 {
				return nil, fmt.Errorf("conversations[%d].value 含有 NUL 二进制字符", i)
			}
			if strings.TrimSpace(m.Value) == "" {
				return nil, fmt.Errorf("conversations[%d].value 不能为空", i)
			}
			if i == 0 && m.From == "system" {
				continue
			}
			if m.From != expected {
				return nil, fmt.Errorf("conversations[%d].from 应为 %s；仅支持 system/human/gpt", i, expected)
			}
			if m.From == "human" {
				question = m.Value
				expected = "gpt"
			} else {
				answer = m.Value
				expected = "human"
				turns++
			}
		}
		if turns == 0 || expected != "human" {
			return nil, fmt.Errorf("对话必须以 gpt 的完整回答结束")
		}
		// Preserve every turn/system message; primary fields describe the last turn.
		payload["conversations"] = messages
	case "jsonl":
		var err error
		question, err = read("question", true)
		if err != nil {
			return nil, err
		}
		answer, err = read("answer", true)
		if err != nil {
			return nil, err
		}
		reasoning, err = read("reasoning", false)
		if err != nil {
			return nil, err
		}
	}
	if reasoning == "" && strings.HasPrefix(answer, "<think>") {
		if end := strings.Index(answer, "</think>"); end >= 0 {
			reasoning = strings.TrimSpace(answer[len("<think>"):end])
			answer = strings.TrimSpace(answer[end+len("</think>"):])
		}
	}
	if strings.TrimSpace(answer) == "" {
		return nil, fmt.Errorf("答案正文不能为空")
	}
	payload["question"] = question
	payload["answer"] = answer
	payload["reasoning"] = reasoning
	return payload, nil
}
