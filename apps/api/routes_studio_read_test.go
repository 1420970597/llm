package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/1420970597/llm/internal/model"
	"github.com/1420970597/llm/internal/store"
	"github.com/1420970597/llm/internal/studio"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestProjectOverviewHTTPStatsAndRoleAwareWorkflow(t *testing.T) {
	f, projectID := sourceIntegrationProject(t)
	ctx := context.Background()
	if err := f.app.authz.UpsertProjectMember(ctx, projectID, f.actor, f.other, model.ProjectRoleViewer, "只读", "overview-test"); err != nil {
		t.Fatal(err)
	}
	_, version, err := f.app.studio.Batches.AppendSampleVersion(ctx, store.AppendSampleVersionInput{ProjectID: projectID, SampleKey: "overview-import", TargetKind: model.TargetKindSFT, Title: "待审", Payload: map[string]any{"question": "q", "reasoning": "r", "answer": "a"}})
	if err != nil {
		t.Fatal(err)
	}
	request := func(actor int64) *httptest.ResponseRecorder {
		t.Helper()
		w := httptest.NewRecorder()
		f.app.projectOverview(w, sourceAuthRequest(httptest.NewRequest(http.MethodGet, "/api/v1/projects/1/overview", nil), projectID, actor))
		return w
	}
	for _, tc := range []struct {
		actor  int64
		suffix string
	}{{f.actor, "/review"}, {f.other, "/data"}} {
		w := request(tc.actor)
		var envelope struct {
			Data         studio.ProjectOverview `json:"data"`
			Capabilities model.Capabilities     `json:"capabilities"`
		}
		if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &envelope) != nil {
			t.Fatalf("overview contract must remain data.stats: %d %s", w.Code, w.Body.String())
		}
		if envelope.Data.Stats.Generated != 1 || envelope.Data.Stats.StructureValid != 1 || envelope.Data.Stats.PendingReview != 1 || envelope.Data.Stats.AcceptanceRate != nil || envelope.Data.NextAction.Kind != "review" || envelope.Data.NextAction.Href != fmt.Sprintf("/p/p_%d%s", projectID, tc.suffix) {
			t.Fatalf("overview must expose real current-version facts and authorized next action: %+v", envelope)
		}
		if tc.actor == f.other && (envelope.Capabilities.CanReview || envelope.Capabilities.CanRun || envelope.Capabilities.CanPublish) {
			t.Fatalf("viewer must not receive write capabilities: %+v", envelope.Capabilities)
		}
	}
	if w := request(f.secondOwner); w.Code != http.StatusNotFound {
		t.Fatalf("unrelated workspace owner must not read project overview: %d %s", w.Code, w.Body.String())
	}
	if _, err := f.pool.Exec(ctx, `INSERT INTO review_projections (sample_version_id, project_id, effective_action) VALUES ($1, $2, 'accepted')`, version.ID, projectID); err != nil {
		t.Fatal(err)
	}
	w := request(f.actor)
	var accepted struct {
		Data studio.ProjectOverview `json:"data"`
	}
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &accepted) != nil || accepted.Data.Stats.Accepted != 1 || accepted.Data.Stats.PendingReview != 0 || accepted.Data.NextAction.Kind != "release" {
		t.Fatalf("accepted stats must not remain stub zeros: %d %s", w.Code, w.Body.String())
	}
	if _, _, err := f.app.studio.Batches.AppendSampleVersion(ctx, store.AppendSampleVersionInput{ProjectID: projectID, SampleKey: "overview-import", TargetKind: model.TargetKindSFT, Title: "新版待审", Payload: map[string]any{"question": "q2", "reasoning": "r2", "answer": "a2"}}); err != nil {
		t.Fatal(err)
	}
	w = request(f.actor)
	var latest struct {
		Data studio.ProjectOverview `json:"data"`
	}
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &latest) != nil || latest.Data.Stats.Generated != 2 || latest.Data.Stats.Accepted != 0 || latest.Data.Stats.PendingReview != 1 || latest.Data.NextAction.Kind != "review" {
		t.Fatalf("new content version must reset historical acceptance: %d %s", w.Code, w.Body.String())
	}
	if err := f.app.authz.UpsertProjectMember(ctx, projectID, f.actor, f.other, model.ProjectRoleReviewer, "审阅", "overview-test"); err != nil {
		t.Fatal(err)
	}
	w = request(f.other)
	var reviewer struct {
		Data studio.ProjectOverview `json:"data"`
	}
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &reviewer) != nil || reviewer.Data.NextAction.Href != fmt.Sprintf("/p/p_%d/review", projectID) {
		t.Fatalf("fresh role upgrade must permit the review route: %d %s", w.Code, w.Body.String())
	}
	if _, err := f.pool.Exec(ctx, `UPDATE projects SET status = 'archived' WHERE id = $1`, projectID); err != nil {
		t.Fatal(err)
	}
	w = request(f.actor)
	var archived struct {
		Data         studio.ProjectOverview `json:"data"`
		Capabilities model.Capabilities     `json:"capabilities"`
	}
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &archived) != nil || archived.Capabilities.CanReview || archived.Data.NextAction.Href != fmt.Sprintf("/p/p_%d/data", projectID) {
		t.Fatalf("archived owner must receive read-only next actions: %d %s", w.Code, w.Body.String())
	}
	// Authorization/project reads still succeed; only statistics fail. The HTTP
	// boundary must return 500, never a plausible successful all-zero overview.
	unavailable, err := pgxpool.New(ctx, f.pool.Config().ConnString())
	if err != nil {
		t.Fatal(err)
	}
	unavailable.Close()
	f.app.studio.Batches = store.NewBatchStore(unavailable)
	if w := request(f.actor); w.Code != http.StatusInternalServerError {
		t.Fatalf("failed statistics reader must fail the whole overview: %d %s", w.Code, w.Body.String())
	}
}
