package studio

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/1420970597/llm/internal/model"
	"github.com/1420970597/llm/internal/store"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestProjectOverviewNextActionFollowsFactsAndCapabilities(t *testing.T) {
	configured := OverviewVersions{Blueprint: &VersionSummary{VersionID: 1}, Coverage: &VersionSummary{VersionID: 2}, Standard: &VersionSummary{VersionID: 3}}
	for _, tc := range []struct {
		name, role, status string
		overview           ProjectOverview
		counts             store.ProjectWorkflowCounts
		kind, suffix       string
	}{
		{name: "new project configures", role: "owner", kind: "design", suffix: "/blueprint"},
		{name: "configured project starts pilot", role: "owner", overview: ProjectOverview{Versions: configured}, kind: "run", suffix: "/pilot"},
		{name: "completed pilot can scale", role: "owner", overview: ProjectOverview{Versions: configured, Batches: OverviewBatches{Pilot: 1, Completed: 1}}, kind: "run", suffix: "/runs/new"},
		{name: "import directly reviews without design or pilot", role: "owner", overview: ProjectOverview{Stats: SampleStats{PendingReview: 2}}, kind: "review", suffix: "/review"},
		{name: "conflict opens visible conflict queue", role: "owner", overview: ProjectOverview{Stats: SampleStats{PendingReview: 1}}, counts: store.ProjectWorkflowCounts{Conflicts: 1}, kind: "review", suffix: "/review?status=conflict"},
		{name: "reviewer reviews even while production runs", role: "reviewer", overview: ProjectOverview{Stats: SampleStats{PendingReview: 2}, Batches: OverviewBatches{Running: 1}}, kind: "review", suffix: "/review"},
		{name: "viewer observes samples", role: "viewer", overview: ProjectOverview{Stats: SampleStats{PendingReview: 2}}, kind: "review", suffix: "/data"},
		{name: "archived owner cannot review", role: "owner", status: "archived", overview: ProjectOverview{Stats: SampleStats{PendingReview: 2}}, kind: "review", suffix: "/data"},
		{name: "unknown role fails closed", role: "unexpected", overview: ProjectOverview{Stats: SampleStats{PendingReview: 2}}, kind: "review", suffix: "/data"},
		{name: "running batch has monitor exit", role: "owner", overview: ProjectOverview{Batches: OverviewBatches{Running: 1}}, kind: "run", suffix: "/runs"},
		{name: "paused batch has recovery exit", role: "owner", overview: ProjectOverview{Batches: OverviewBatches{Paused: 1}}, kind: "run", suffix: "/runs"},
		{name: "failed batch has retry exit", role: "owner", overview: ProjectOverview{Batches: OverviewBatches{Failed: 1}}, kind: "run", suffix: "/runs"},
		{name: "imported accepted content can release", role: "owner", overview: ProjectOverview{Stats: SampleStats{Accepted: 1}}, kind: "release", suffix: "/releases"},
		{name: "reviewer can only observe release", role: "reviewer", overview: ProjectOverview{Stats: SampleStats{Accepted: 1}}, kind: "release", suffix: "/releases"},
		{name: "pending candidate is not done", role: "owner", overview: ProjectOverview{Stats: SampleStats{Accepted: 1}}, counts: store.ProjectWorkflowCounts{ReleasePending: 1, Published: 1, PublishedAccepted: 1}, kind: "release", suffix: "/releases"},
		{name: "unpublished accepted version not done", role: "owner", overview: ProjectOverview{Stats: SampleStats{Accepted: 2}}, counts: store.ProjectWorkflowCounts{Published: 1, PublishedAccepted: 1}, kind: "release", suffix: "/releases"},
		{name: "all accepted current versions published", role: "owner", overview: ProjectOverview{Stats: SampleStats{Accepted: 2}}, counts: store.ProjectWorkflowCounts{Published: 1, PublishedAccepted: 2}, kind: "done", suffix: "/releases"},
		{name: "quarantine remains visible", role: "reviewer", overview: ProjectOverview{Stats: SampleStats{Quarantined: 1}}, kind: "review", suffix: "/review?status=quarantined"},
		{name: "viewer cannot start pilot", role: "viewer", overview: ProjectOverview{Versions: configured}, kind: "run", suffix: "/runs"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			project := model.Project{ID: 19, Status: tc.status}
			caps := model.ProjectCapabilities(tc.role, tc.status)
			action := nextActionFor(project, tc.overview, tc.counts, caps)
			if action.Kind != tc.kind || action.Href != "/p/p_19"+tc.suffix || action.Message == "" {
				t.Fatalf("want %s %s, got %+v", tc.kind, tc.suffix, action)
			}
			if !caps.CanRun && (strings.Contains(action.Message, "开始") || strings.Contains(action.Message, "恢复") || strings.Contains(action.Message, "重试")) {
				t.Fatalf("read-only production capability must not advertise writes: %+v", action)
			}
			if !caps.CanPublish && action.Message == "发布已接纳数据" {
				t.Fatalf("nonpublisher must not receive a publish instruction: %+v", action)
			}
		})
	}
}

func TestLoadProjectOverviewImportedContentAndCurrentVersionStats(t *testing.T) {
	pool, projectID := newHealthFixture(t)
	ctx := context.Background()
	service := New(pool)
	empty, err := service.LoadProjectOverview(ctx, projectID, model.ProjectRoleOwner)
	if err != nil || empty.Stats.Generated != 0 || empty.Stats.AcceptanceRate != nil || empty.Stats.AcceptanceRateDisplay != "无结论" || empty.NextAction.Kind != "design" {
		t.Fatalf("empty project should show zero facts without a invented rate: %+v, %v", empty, err)
	}
	_, version, err := service.Batches.AppendSampleVersion(ctx, store.AppendSampleVersionInput{ProjectID: projectID, SampleKey: "external", TargetKind: model.TargetKindSFT, Title: "导入样本", Payload: map[string]any{"question": "q", "reasoning": "r", "answer": "a"}})
	if err != nil {
		t.Fatal(err)
	}
	imported, err := service.LoadProjectOverview(ctx, projectID, model.ProjectRoleOwner)
	if err != nil || imported.Stats.PendingReview != 1 || imported.NextAction.Kind != "review" || imported.Batches.Pilot != 0 {
		t.Fatalf("imported sample must go straight to review: %+v, %v", imported, err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO review_projections (sample_version_id, project_id, effective_action) VALUES ($1, $2, 'accepted')`, version.ID, projectID); err != nil {
		t.Fatal(err)
	}
	accepted, err := service.LoadProjectOverview(ctx, projectID, model.ProjectRoleOwner)
	if err != nil || accepted.Stats.Accepted != 1 || accepted.Stats.Inspected != 0 || accepted.Stats.AcceptanceRate != nil || accepted.NextAction.Kind != "release" {
		t.Fatalf("accepted imported content is real even without a quality denominator: %+v, %v", accepted, err)
	}
	var releaseID int64
	if err := pool.QueryRow(ctx, `INSERT INTO releases (project_id, release_name, release_name_key, status) VALUES ($1, 'v1', 'v1', 'published') RETURNING id`, projectID).Scan(&releaseID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO release_items (release_id, revision, sample_id, sample_version_id, effective_action) VALUES ($1, 1, $2, $3, 'accepted')`, releaseID, version.SampleID, version.ID); err != nil {
		t.Fatal(err)
	}
	published, err := service.LoadProjectOverview(ctx, projectID, model.ProjectRoleOwner)
	if err != nil || published.NextAction.Kind != "done" {
		t.Fatalf("real published current content is done: %+v, %v", published, err)
	}
	if _, _, err := service.Batches.AppendSampleVersion(ctx, store.AppendSampleVersionInput{ProjectID: projectID, SampleKey: "external", TargetKind: model.TargetKindSFT, Title: "新版待审", Payload: map[string]any{"question": "q2", "reasoning": "r2", "answer": "a2"}}); err != nil {
		t.Fatal(err)
	}
	latest, err := service.LoadProjectOverview(ctx, projectID, model.ProjectRoleOwner)
	if err != nil || latest.Stats.Generated != 2 || latest.Stats.StructureValid != 1 || latest.Stats.Accepted != 0 || latest.Stats.PendingReview != 1 || latest.NextAction.Kind != "review" {
		t.Fatalf("historical acceptance/publication must not mark the new version done: %+v, %v", latest, err)
	}
	readonly, err := service.LoadProjectOverview(ctx, projectID)
	if err != nil || readonly.NextAction.Href != fmt.Sprintf("/p/p_%d/data", projectID) {
		t.Fatalf("unspecified role must default to read-only actions: %+v, %v", readonly.NextAction, err)
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := service.LoadProjectOverview(cancelled, projectID, model.ProjectRoleOwner); err == nil {
		t.Fatal("failed overview read must not produce a successful zero snapshot")
	}
	// Keep the project/document readers healthy and fail only the statistics
	// reader. This catches a regression back to swallowing individual counts.
	unavailable, err := pgxpool.New(ctx, pool.Config().ConnString())
	if err != nil {
		t.Fatal(err)
	}
	unavailable.Close()
	service.Batches = store.NewBatchStore(unavailable)
	if _, err := service.LoadProjectOverview(ctx, projectID, model.ProjectRoleOwner); err == nil {
		t.Fatal("statistics failure after successful project reads must propagate")
	}
}

func TestLoadProjectOverviewAcceptanceRateUsesInspectedCurrentVersions(t *testing.T) {
	pool, projectID := newHealthFixture(t)
	ctx := context.Background()
	service := New(pool)
	var inspected model.SampleVersion
	for index := 0; index < 3; index++ {
		_, version, err := service.Batches.AppendSampleVersion(ctx, store.AppendSampleVersionInput{ProjectID: projectID, SampleKey: fmt.Sprintf("rate-%d", index), TargetKind: model.TargetKindSFT, Title: "已接纳", Payload: map[string]any{"question": "q", "reasoning": "r", "answer": "a"}})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, `INSERT INTO review_projections (sample_version_id, project_id, effective_action) VALUES ($1, $2, 'accepted')`, version.ID, projectID); err != nil {
			t.Fatal(err)
		}
		inspected = version
	}
	var experimentID int64
	if err := pool.QueryRow(ctx, `INSERT INTO experiments (project_id, target_kind) VALUES ($1, 'sft') RETURNING id`, projectID).Scan(&experimentID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO experiment_items (experiment_id, project_id, sample_id, sample_version_id, status) VALUES ($1, $2, $3, $4, 'scored')`, experimentID, projectID, inspected.SampleID, inspected.ID); err != nil {
		t.Fatal(err)
	}
	overview, err := service.LoadProjectOverview(ctx, projectID, model.ProjectRoleOwner)
	if err != nil || overview.Stats.Accepted != 3 || overview.Stats.Inspected != 1 || overview.Stats.Scored != 1 || overview.Stats.AcceptanceRate == nil || *overview.Stats.AcceptanceRate != 1 {
		t.Fatalf("uninspected accepted content must not turn 1/1 inspected acceptance into 300%%: %+v, %v", overview.Stats, err)
	}
}
