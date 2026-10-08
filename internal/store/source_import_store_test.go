package store

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	importer "github.com/1420970597/llm/internal/import"
	"github.com/1420970597/llm/internal/model"
)

func queuedSource(t *testing.T, f jobFixture, kind, sourceKey string, body []byte) (*LegacyImportStore, QueueSourceImportInput, LegacyImport, model.Job) {
	t.Helper()
	s := NewLegacyImportStore(f.pool)
	input := QueueSourceImportInput{ProjectID: f.projectID, SourceKind: kind, SourceKey: sourceKey, Content: body, Options: SourceImportOptions{FileName: "guide.md", Kind: "markdown", Chunking: model.DefaultSourceChunking(), ActorID: f.userID, ChangeReason: "测试素材导入"}}
	if kind == SourceKindProduct {
		input.Options.Kind = ""
		input.Options.Format = "alpaca"
		input.Options.TargetKind = "sft"
	}
	row, _, err := s.QueueSourceImport(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	if row.JobID == nil {
		t.Fatal("durable job missing")
	}
	job, claimed, err := f.jobs.ClaimJobByID(context.Background(), *row.JobID, "source-test", time.Minute)
	if err != nil || !claimed {
		t.Fatalf("claim %v %v", claimed, err)
	}
	return s, input, row, job
}

func TestSourceDocumentIngestReplayAndFrozenChunkIDs(t *testing.T) {
	f := newJobFixture(t)
	ctx := context.Background()
	s, input, row, job := queuedSource(t, f, SourceKindDocument, "", []byte("# 冷链\n连续记录与异常报告。\n## 验证\n保留断档依据。"))
	if len(job.Payload) > 100 {
		t.Fatal("job payload contains uploaded body")
	}
	done, err := s.ProcessSourceImport(ctx, job, row.ID)
	if err != nil {
		t.Fatal(err)
	}
	if done.Status != "completed" || done.Counts.ImportedVersions != 2 || done.Counts.SourceItems != 2 {
		t.Fatalf("wrong report %+v", done)
	}
	doc, err := NewDocumentStore(f.pool).GetDocument(ctx, f.projectID, model.KindSource, "")
	if err != nil {
		t.Fatal(err)
	}
	var payload model.SourcePayload
	if err := json.Unmarshal(doc.Current.Payload, &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.Documents[0].ChunkIDs) != 2 || payload.Documents[0].ChunkCount != 2 {
		t.Fatalf("frozen source lacks IDs %+v", payload)
	}
	var staged bool
	if err := f.pool.QueryRow(ctx, `SELECT source_content IS NOT NULL FROM legacy_imports WHERE id=$1`, row.ID).Scan(&staged); err != nil || staged {
		t.Fatalf("staging not cleared %v", err)
	}
	before := doc.CurrentVersion
	again, replay, err := s.QueueSourceImport(ctx, input)
	if err != nil || !replay || again.ID != row.ID {
		t.Fatalf("not replay %+v %v", again, err)
	}
	doc, _ = NewDocumentStore(f.pool).GetDocument(ctx, f.projectID, model.KindSource, "")
	if doc.CurrentVersion != before {
		t.Fatal("replay appended a source version")
	}
	input.Content = []byte("changed")
	if _, _, err := s.QueueSourceImport(ctx, input); !errors.Is(err, ErrSourceKeyConflict) && !errors.Is(err, ErrRevisionConflict) {
		t.Fatalf("changed implicit identity needs revision: %v", err)
	}
}

func TestProductIngestPreciseFailuresDedupAndTenantScope(t *testing.T) {
	f := newJobFixture(t)
	ctx := context.Background()
	body := []byte("{\"instruction\":\"检查什么？\",\"output\":\"原始记录\"}\n{\"instruction\":\"缺字段\"}\n{\"instruction\":\"检查什么？\",\"output\":\"原始记录\"}")
	s, input, row, job := queuedSource(t, f, SourceKindProduct, "easy-dataset/coldchain-v1", body)
	done, err := s.ProcessSourceImport(ctx, job, row.ID)
	if err != nil {
		t.Fatal(err)
	}
	if done.Counts.ImportedVersions != 1 || done.Counts.SkippedExisting != 1 || done.Counts.FailedItems != 1 || done.Counts.SourceItems != 3 {
		t.Fatalf("wrong counts %+v", done.Counts)
	}
	var failures []importer.ImportFailure
	json.Unmarshal(done.Failures, &failures)
	if len(failures) != 1 || failures[0].SourceID != 2 {
		t.Fatalf("wrong row failures %s", done.Failures)
	}
	if _, replay, err := s.QueueSourceImport(ctx, input); err != nil || !replay {
		t.Fatalf("not replay %v", err)
	}
	input.SourceKey = "easy-dataset/new-file"
	second, _, err := s.QueueSourceImport(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	next, _, err := f.jobs.ClaimJobByID(ctx, *second.JobID, "source-test", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	done, err = s.ProcessSourceImport(ctx, next, second.ID)
	if err != nil || done.Counts.ImportedVersions != 0 || done.Counts.SkippedExisting != 2 {
		t.Fatalf("content not deduped %+v %v", done, err)
	}
	input.SourceKey = "easy-dataset/coldchain-v1"
	input.Content = []byte(`{"instruction":"new","output":"new"}`)
	if _, _, err := s.QueueSourceImport(ctx, input); !errors.Is(err, ErrSourceKeyConflict) {
		t.Fatalf("same key changed content must conflict %v", err)
	}
	other, err := NewProjectStore(f.pool).CreateProject(ctx, seedAuthzWorkspace(t, f.pool, "source-scope"), f.userID, validProjectInput("另一项目"))
	if err != nil {
		t.Fatal(err)
	}
	input.ProjectID = other.ID
	if _, _, err := s.QueueSourceImport(ctx, input); err != nil {
		t.Fatalf("same source key in another project must work %v", err)
	}
}

func TestSourceImportFencesStaleSideEffectsAndRejectsEncoding(t *testing.T) {
	f := newJobFixture(t)
	ctx := context.Background()
	s, _, row, job := queuedSource(t, f, SourceKindDocument, "stale", []byte("# guide\ncontent"))
	if _, err := f.pool.Exec(ctx, `UPDATE jobs SET fencing_token=fencing_token+1 WHERE id=$1`, job.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ProcessSourceImport(ctx, job, row.ID); !errors.Is(err, ErrJobLeaseLost) {
		t.Fatalf("stale write accepted %v", err)
	}
	var count int
	f.pool.QueryRow(ctx, `SELECT COUNT(*) FROM source_chunks WHERE project_id=$1`, f.projectID).Scan(&count)
	if count != 0 {
		t.Fatal("stale worker inserted chunks")
	}
	if _, err := f.pool.Exec(ctx, `UPDATE jobs SET fencing_token=$2,lease_until=NOW()-INTERVAL '1 second' WHERE id=$1`, job.ID, job.FencingToken); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ProcessSourceImport(ctx, job, row.ID); !errors.Is(err, ErrJobLeaseLost) {
		t.Fatalf("expired lease wrote side effects: %v", err)
	}
	doc, _ := NewDocumentStore(f.pool).GetDocument(ctx, f.projectID, model.KindSource, "")
	input := QueueSourceImportInput{ProjectID: f.projectID, SourceKind: SourceKindDocument, SourceKey: "bad-encoding", Content: []byte{0xff}, Options: SourceImportOptions{FileName: "bad.txt", Kind: "txt", ActorID: f.userID, ChangeReason: "边界测试", ExpectedRevision: doc.RowVersion, Chunking: model.DefaultSourceChunking()}}
	bad, _, err := s.QueueSourceImport(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	badJob, _, err := f.jobs.ClaimJobByID(ctx, *bad.JobID, "source-test", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ProcessSourceImport(ctx, badJob, bad.ID); err == nil {
		t.Fatal("invalid encoding accepted")
	}
	loaded, _ := s.GetSourceImport(ctx, f.projectID, bad.ID)
	if loaded.Status != "failed" || loaded.Counts.FailedItems != 1 {
		t.Fatalf("missing actionable error %+v", loaded)
	}
}
