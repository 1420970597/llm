package store

import (
	"context"
	"testing"

	importer "github.com/1420970597/llm/internal/import"
	"github.com/1420970597/llm/internal/model"
)

func TestSourceChunkPaginationUsesFrozenVersionMembership(t *testing.T) {
	f := newJobFixture(t)
	ctx := context.Background()
	chunks := NewSourceChunkStore(f.pool)
	docs := NewDocumentStore(f.pool)
	parsed, _ := importer.ChunkDocument([]byte("original material"), "txt", model.DefaultSourceChunking())
	rows, _, err := chunks.UpsertSourceChunks(ctx, f.projectID, "original", parsed)
	if err != nil {
		t.Fatal(err)
	}
	payload := model.SourcePayload{SchemaVersion: "source.v1", Chunking: model.DefaultSourceChunking(), Documents: []model.SourceDocumentEntry{{StableID: "original", FileName: "old.txt", Kind: "txt", ContentHash: "original-hash", ChunkCount: 1, ChunkIDs: []int64{rows[0].ID}}}}
	head, old, err := docs.SaveVersion(ctx, f.projectID, model.KindSource, f.userID, SaveDocumentVersionInput{Payload: payload})
	if err != nil {
		t.Fatal(err)
	}
	parsed, _ = importer.ChunkDocument([]byte("later upload"), "txt", model.DefaultSourceChunking())
	later, _, err := chunks.UpsertSourceChunks(ctx, f.projectID, "later", parsed)
	if err != nil {
		t.Fatal(err)
	}
	payload.Documents = append(payload.Documents, model.SourceDocumentEntry{StableID: "later", FileName: "new.txt", Kind: "txt", ContentHash: "later-hash", ChunkCount: 1, ChunkIDs: []int64{later[0].ID}})
	_, current, err := docs.SaveVersion(ctx, f.projectID, model.KindSource, f.userID, SaveDocumentVersionInput{ExpectedRevision: head.RowVersion, Payload: payload})
	if err != nil {
		t.Fatal(err)
	}
	items, total, err := chunks.ListSourceChunksAtVersion(ctx, f.projectID, old.ID, "", "", 1, 0)
	if err != nil || total != 1 || len(items) != 1 || items[0].ID != rows[0].ID {
		t.Fatalf("historical preview leaked later upload: %+v %d %v", items, total, err)
	}
	items, total, err = chunks.ListSourceChunksAtVersion(ctx, f.projectID, current.ID, "later", "later", 1, 0)
	if err != nil || total != 1 || len(items) != 1 || items[0].ID != later[0].ID {
		t.Fatalf("filter/count before pagination: %+v %d %v", items, total, err)
	}
	items, total, err = chunks.ListSourceChunksAtVersion(ctx, f.projectID, old.ID, "later", "", 50, 0)
	if err != nil || total != 0 || len(items) != 0 {
		t.Fatalf("old version includes new document: %+v %d %v", items, total, err)
	}
	if _, _, err := chunks.ListSourceChunksAtVersion(ctx, f.projectID+99999, old.ID, "", "", 50, 0); err == nil {
		t.Fatal("cross-project source version exposed chunks")
	}
}
