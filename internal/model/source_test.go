package model

import "testing"

func TestSourcePayloadValidation(t *testing.T) {
	valid := SourcePayload{SchemaVersion: "source.v1", Chunking: DefaultSourceChunking(), Documents: []SourceDocumentEntry{{StableID: "doc", FileName: "guide.md", Kind: "markdown", ContentHash: "digest"}}}
	if err := ValidateSourcePayload(valid); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name   string
		change func(*SourcePayload)
		field  string
	}{
		{"schema", func(p *SourcePayload) { p.SchemaVersion = "source.v2" }, "schemaVersion"},
		{"duplicate", func(p *SourcePayload) { p.Documents = append(p.Documents, p.Documents[0]) }, "documents[1].stableId"},
		{"hash", func(p *SourcePayload) { p.Documents[0].ContentHash = "" }, "documents[0].contentHash"},
		{"algorithm", func(p *SourcePayload) { p.Chunking.Algorithm = "token" }, "chunking.algorithm"},
		{"length", func(p *SourcePayload) { p.Chunking.MinLength = 2100 }, "chunking.maxLength"},
		{"unsupported", func(p *SourcePayload) { p.Documents[0].Kind = "pdf" }, "documents[0].kind"},
		{"count mismatch", func(p *SourcePayload) { p.Documents[0].ChunkCount = 1 }, "documents[0].chunkIds"},
		{"duplicate chunks", func(p *SourcePayload) { p.Documents[0].ChunkCount = 2; p.Documents[0].ChunkIDs = []int64{1, 1} }, "documents[0].chunkIds"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := valid
			p.Documents = append([]SourceDocumentEntry(nil), valid.Documents...)
			c.change(&p)
			err := ValidateSourcePayload(p)
			fields, ok := HasFieldErrors(err)
			if !ok {
				t.Fatalf("expected field error: %v", err)
			}
			found := false
			for _, f := range fields {
				found = found || f.Field == c.field
			}
			if !found {
				t.Fatalf("missing %s: %v", c.field, fields)
			}
		})
	}
}

func TestCoverageSourceValidation(t *testing.T) {
	p := CoveragePayload{SchemaVersion: "coverage.v1", Domains: []CoverageDomain{{StableID: "d", Name: "领域", Directions: []CoverageDirection{{StableID: "s", Name: "方向", Quota: 1, Source: SourceDocument, SourceChunkIDs: []int64{3}}}}}}
	if err := ValidateCoveragePayload(p); err != nil {
		t.Fatal(err)
	}
	p.Domains[0].Directions[0].SourceChunkIDs = nil
	err := ValidateCoveragePayload(p)
	fields, ok := HasFieldErrors(err)
	if !ok || fields[0].Field != "domains[0].directions[0].sourceChunkIds" {
		t.Fatalf("wrong failure %v", err)
	}
	p.Domains[0].Directions[0].Source = ""
	if err := ValidateCoveragePayload(p); err != nil {
		t.Fatalf("legacy empty source should save as a gap: %v", err)
	}
	p.Domains[0].Directions[0].Source = "invented"
	if err := ValidateCoveragePayload(p); err == nil {
		t.Fatal("invalid source accepted")
	}
}
