package studio

import (
	"context"
	"testing"

	"github.com/1420970597/llm/internal/model"
	"github.com/1420970597/llm/internal/store"
)

func TestProjectOverviewReflectsSourceVersionWithoutFabricatingEmptyVersion(t *testing.T) {
	pool, projectID := newHealthFixture(t)
	ctx := context.Background()
	service := NewWithRollout(pool, Rollout{})
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM audit_logs WHERE project_id=$1`, projectID)
	})
	overview, err := service.LoadProjectOverview(ctx, projectID)
	if err != nil || overview.Versions.Source != nil {
		t.Fatalf("missing source must be null: %+v %v", overview.Versions.Source, err)
	}
	var actorID int64
	if err := pool.QueryRow(ctx, `SELECT owner_id FROM projects WHERE id=$1`, projectID).Scan(&actorID); err != nil {
		t.Fatal(err)
	}
	_, version, err := store.NewDocumentStore(pool).SaveVersion(ctx, projectID, model.KindSource, actorID, store.SaveDocumentVersionInput{Payload: model.SourcePayload{SchemaVersion: "source.v1", Documents: []model.SourceDocumentEntry{}, Chunking: model.DefaultSourceChunking()}})
	if err != nil {
		t.Fatal(err)
	}
	overview, err = service.LoadProjectOverview(ctx, projectID)
	if err != nil || overview.Versions.Source == nil || overview.Versions.Source.VersionID != version.ID || overview.Versions.Source.ContentHash != version.ContentHash {
		t.Fatalf("real source version missing: %+v %v", overview.Versions.Source, err)
	}
}
