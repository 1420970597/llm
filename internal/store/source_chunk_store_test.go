package store

import (
	"context"
	"testing"

	importer "github.com/1420970597/llm/internal/import"
	"github.com/1420970597/llm/internal/model"
)

func TestSourceChunksDeduplicateAndSampleProvenanceRoundTrip(t *testing.T) {
	f := newJobFixture(t)
	ctx := context.Background()
	s := NewSourceChunkStore(f.pool)
	chunks, err := importer.ChunkDocument([]byte("material"), "txt", model.DefaultSourceChunking())
	if err != nil {
		t.Fatal(err)
	}
	rows, added, err := s.UpsertSourceChunks(ctx, f.projectID, "doc", chunks)
	if err != nil || added != 1 {
		t.Fatal(err)
	}
	_, added, err = s.UpsertSourceChunks(ctx, f.projectID, "duplicate", chunks)
	if err != nil || added != 0 {
		t.Fatalf("duplicate inserted %d %v", added, err)
	}
	filtered, total, err := s.ListSourceChunks(ctx, f.projectID, "duplicate", "", 50, 0)
	if err != nil || total != 1 || len(filtered) != 1 || filtered[0].ID != rows[0].ID {
		t.Fatalf("deduplicated document lost membership: %+v %d %v", filtered, total, err)
	}
	ids := []int64{rows[0].ID}
	batches := NewBatchStore(f.pool)
	sample, version, err := batches.AppendSampleVersion(ctx, AppendSampleVersionInput{ProjectID: f.projectID, SampleKey: "source-round-trip", TargetKind: "sft", Payload: map[string]string{"question": "q", "reasoning": "r", "answer": "a"}, SourceChunkIDs: ids})
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := batches.GetSampleVersionByID(ctx, f.projectID, version.ID)
	if err != nil || len(loaded.SourceChunkIDs) != 1 || loaded.SourceChunkIDs[0] != ids[0] {
		t.Fatalf("read byID lost IDs %+v %v", loaded, err)
	}
	loaded, err = batches.GetSampleVersion(ctx, f.projectID, sample.ID, 1)
	if err != nil || len(loaded.SourceChunkIDs) != 1 {
		t.Fatal("read version lost IDs")
	}
	history, err := batches.ListSampleVersions(ctx, f.projectID, sample.ID, 10)
	if err != nil || len(history[0].SourceChunkIDs) != 1 {
		t.Fatal("history lost IDs")
	}
	if _, err := s.GetChunks(ctx, f.projectID+99999, ids); err == nil {
		t.Fatal("cross project data leak")
	}
	found, err := s.FindChunks(ctx, f.projectID, []int64{ids[0], ids[0] + 99999})
	if err != nil || len(found) != 1 {
		t.Fatal("permissive lookup loses live provenance")
	}
	if _, err := s.GetChunks(ctx, f.projectID, []int64{ids[0] + 99999}); err == nil {
		t.Fatal("strict lookup accepted missing")
	}
	coverage := coveragePayload()
	coverage.Domains[0].Directions[0].Source = model.SourceDocument
	coverage.Domains[0].Directions[0].SourceChunkIDs = []int64{ids[0] + 99999}
	if _, _, err := NewDocumentStore(f.pool).SaveVersion(ctx, f.projectID, model.KindCoverage, f.userID, SaveDocumentVersionInput{Payload: coverage}); err == nil {
		t.Fatal("coverage saved cross-project/missing chunk")
	}
}
