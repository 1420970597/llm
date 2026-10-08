package model

import (
	"fmt"
	"strings"
	"time"
)

const (
	SourceDocument              = "document"
	SourceAI                    = "ai"
	SourceManual                = "manual"
	SourceNone                  = "none"
	JobKindSourceDocumentIngest = "studio.source.document.ingest"
	JobKindSourceProductIngest  = "studio.source.product.ingest"
)

type SourceChunking struct {
	Algorithm       string `json:"algorithm"`
	Separator       string `json:"separator"`
	MaxLength       int    `json:"maxLength"`
	MinLength       int    `json:"minLength"`
	KeepHeadingPath bool   `json:"keepHeadingPath"`
}

func DefaultSourceChunking() SourceChunking {
	return SourceChunking{Algorithm: "recursive", Separator: "\n\n", MaxLength: 2000, MinLength: 200, KeepHeadingPath: true}
}

type SourceDocumentEntry struct {
	StableID    string  `json:"stableId"`
	FileName    string  `json:"fileName"`
	Kind        string  `json:"kind"`
	ContentHash string  `json:"contentHash"`
	ChunkCount  int     `json:"chunkCount"`
	ChunkIDs    []int64 `json:"chunkIds,omitempty"`
	ParsedAt    string  `json:"parsedAt,omitempty"`
}

type SourcePayload struct {
	SchemaVersion string                `json:"schemaVersion"`
	Documents     []SourceDocumentEntry `json:"documents"`
	Chunking      SourceChunking        `json:"chunking"`
}

type SourceChunk struct {
	ID                     int64     `json:"id"`
	ProjectID              int64     `json:"projectId"`
	SourceDocumentStableID string    `json:"sourceDocumentStableId"`
	HeadingPath            string    `json:"headingPath"`
	Ordinal                int       `json:"ordinal"`
	Content                string    `json:"content"`
	ContentHash            string    `json:"contentHash"`
	CreatedAt              time.Time `json:"createdAt"`
}

func ValidateSourcePayload(payload SourcePayload) error {
	var errs FieldErrors
	if payload.SchemaVersion != "source.v1" {
		errs = append(errs, FieldError{Field: "schemaVersion", Message: "必须为 source.v1"})
	}
	if len(payload.Documents) > 500 {
		errs = append(errs, FieldError{Field: "documents", Message: "最多保存 500 个素材来源"})
	}
	seen := map[string]bool{}
	for i, doc := range payload.Documents {
		prefix := fmt.Sprintf("documents[%d]", i)
		if strings.TrimSpace(doc.StableID) == "" || seen[doc.StableID] {
			errs = append(errs, FieldError{Field: prefix + ".stableId", Message: "稳定 ID 必填且不能重复"})
		}
		seen[doc.StableID] = true
		if strings.TrimSpace(doc.ContentHash) == "" {
			errs = append(errs, FieldError{Field: prefix + ".contentHash", Message: "内容摘要必填"})
		}
		if doc.Kind != "markdown" && doc.Kind != "txt" {
			errs = append(errs, FieldError{Field: prefix + ".kind", Message: "支持 Markdown/TXT；PDF/DOCX 尚未接入"})
		}
		if doc.ChunkCount < 0 {
			errs = append(errs, FieldError{Field: prefix + ".chunkCount", Message: "块数不能为负数"})
		}
		if doc.ChunkCount != len(doc.ChunkIDs) {
			errs = append(errs, FieldError{Field: prefix + ".chunkIds", Message: "块数与素材引用不一致"})
		}
		seenChunks := map[int64]bool{}
		for _, id := range doc.ChunkIDs {
			if id <= 0 || seenChunks[id] {
				errs = append(errs, FieldError{Field: prefix + ".chunkIds", Message: "素材块 ID 必须为正整数且不能重复"})
				break
			}
			seenChunks[id] = true
		}
	}
	if payload.Chunking.Algorithm != "recursive" && payload.Chunking.Algorithm != "text" {
		errs = append(errs, FieldError{Field: "chunking.algorithm", Message: "支持 recursive 或 text"})
	}
	if payload.Chunking.MinLength < 1 || payload.Chunking.MaxLength < payload.Chunking.MinLength || payload.Chunking.MaxLength > 100000 {
		errs = append(errs, FieldError{Field: "chunking.maxLength", Message: "必须满足 1 ≤ 最小长度 ≤ 最大长度 ≤ 100000"})
	}
	if len(payload.Chunking.Separator) > 100 {
		errs = append(errs, FieldError{Field: "chunking.separator", Message: "分隔符不能超过 100 字节"})
	}
	if len(errs) > 0 {
		return errs
	}
	return nil
}
