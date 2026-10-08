// Package importer owns deterministic source parsing and public dataset formats.
package importer

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/1420970597/llm/internal/model"
)

const MaxSourceUploadBytes = 200 << 20
const MaxProductUploadBytes = 20 << 20
const MaxProductRows = 5000
const MaxRecordedFailures = 200

func ContentDigest(content []byte) string {
	sum := sha256.Sum256(content)
	return hex.EncodeToString(sum[:])
}

// ChunkDocument counts Unicode characters, retains every source rune, and splits
// long paragraphs even when the document has no separator. Same bytes/settings
// always produce the same chunk sequence and hashes.
func ChunkDocument(content []byte, kind string, options model.SourceChunking) ([]model.SourceChunk, error) {
	if kind != "markdown" && kind != "txt" {
		return nil, fmt.Errorf("不支持该素材类型；请上传 Markdown/TXT")
	}
	if !utf8.Valid(content) {
		return nil, fmt.Errorf("素材不是有效的 UTF-8 文本，请转换编码后重新上传")
	}
	if strings.IndexByte(string(content), 0) >= 0 {
		return nil, fmt.Errorf("素材含有 NUL 二进制字符，请上传纯文本文件")
	}
	if err := model.ValidateSourcePayload(model.SourcePayload{SchemaVersion: "source.v1", Chunking: options}); err != nil {
		return nil, err
	}
	text := strings.TrimPrefix(string(content), "\ufeff")
	text = strings.ReplaceAll(strings.ReplaceAll(text, "\r\n", "\n"), "\r", "\n")
	if strings.TrimSpace(text) == "" {
		return []model.SourceChunk{}, nil
	}
	parts := []struct{ text, path string }{}
	heading := []string{}
	var section strings.Builder
	path := ""
	flush := func() {
		if section.Len() > 0 {
			parts = append(parts, struct{ text, path string }{section.String(), path})
			section.Reset()
		}
	}
	for _, line := range strings.SplitAfter(text, "\n") {
		trimmed := strings.TrimSpace(line)
		level := 0
		if kind == "markdown" {
			for level < len(trimmed) && level < 6 && trimmed[level] == '#' {
				level++
			}
		}
		if level > 0 && level < len(trimmed) && trimmed[level] == ' ' {
			flush()
			if level <= len(heading) {
				heading = heading[:level-1]
			}
			for len(heading) < level-1 {
				heading = append(heading, "")
			}
			heading = append(heading, strings.TrimSpace(trimmed[level:]))
			path = strings.Join(heading, " › ")
		}
		section.WriteString(line)
	}
	flush()
	chunks := []model.SourceChunk{}
	for _, part := range parts {
		runes := []rune(part.text)
		for len(runes) > 0 {
			end := len(runes)
			if end > options.MaxLength {
				end = options.MaxLength
			}
			if options.Algorithm == "recursive" && end < len(runes) {
				prefix := string(runes[:end])
				separator := options.Separator
				for _, sep := range []string{separator, "\n", " "} {
					if sep == "" {
						continue
					}
					if p := strings.LastIndex(prefix, sep); p >= 0 {
						candidate := utf8.RuneCountInString(prefix[:p+len(sep)])
						if candidate >= options.MinLength {
							end = candidate
							break
						}
					}
				}
			}
			value := string(runes[:end])
			runes = runes[end:]
			if strings.TrimSpace(value) == "" {
				continue
			}
			chunk := model.SourceChunk{Ordinal: len(chunks) + 1, Content: value, ContentHash: ContentDigest([]byte(value))}
			if options.KeepHeadingPath {
				chunk.HeadingPath = part.path
			}
			chunks = append(chunks, chunk)
		}
	}
	return chunks, nil
}
