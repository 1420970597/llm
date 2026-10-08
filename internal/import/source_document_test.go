package importer

import (
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/1420970597/llm/internal/model"
)

func TestChunkDocumentDeterministicHeadingAndUnicode(t *testing.T) {
	options := model.SourceChunking{Algorithm: "recursive", Separator: "\n\n", MinLength: 5, MaxLength: 35, KeepHeadingPath: true}
	body := []byte("\ufeff# 冷链\r\n记录要求。\r\n## 验证\r\n温度连续性。\r\n### 异常\r\n" + strings.Repeat("长", 101))
	first, err := ChunkDocument(body, "markdown", options)
	if err != nil {
		t.Fatal(err)
	}
	second, err := ChunkDocument(body, "markdown", options)
	if err != nil || !reflect.DeepEqual(first, second) {
		t.Fatalf("non-deterministic %v", err)
	}
	if len(first) < 5 {
		t.Fatalf("expected heading-aware/hard splits, got %d", len(first))
	}
	hasPath := false
	total := 0
	for _, c := range first {
		if utf8.RuneCountInString(c.Content) > 35 {
			t.Fatal("oversize chunk")
		}
		hasPath = hasPath || c.HeadingPath == "冷链 › 验证 › 异常"
		total += strings.Count(c.Content, "长")
	}
	if !hasPath || total != 101 {
		t.Fatalf("missing heading/content %v %d", hasPath, total)
	}
}

func TestChunkDocumentEmptyEncodingAndUnsupported(t *testing.T) {
	chunks, err := ChunkDocument([]byte(" \n\t"), "txt", model.DefaultSourceChunking())
	if err != nil || len(chunks) != 0 {
		t.Fatal("empty must produce 0 chunks")
	}
	if _, err := ChunkDocument([]byte{0xff}, "txt", model.DefaultSourceChunking()); err == nil {
		t.Fatal("bad UTF-8 accepted")
	}
	if _, err := ChunkDocument([]byte("binary\x00text"), "txt", model.DefaultSourceChunking()); err == nil {
		t.Fatal("NUL accepted for PostgreSQL text")
	}
	if _, err := ChunkDocument([]byte("pdf"), "pdf", model.DefaultSourceChunking()); err == nil {
		t.Fatal("fake PDF accepted")
	}
}
