package store

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/1420970597/llm/internal/model"
)

func TestSampleListParamsNormalizesLimitsAndFilters(t *testing.T) {
	cursor := time.Date(2026, 10, 1, 10, 0, 0, 0, time.UTC)
	params := sampleListParams(SampleListQuery{ProjectID: 7, BatchID: 9,
		TargetKind: " sft ", Search: " question ", ReviewStatus: " pending ",
		Cursor: cursor, CursorID: 11, UnreviewedOnly: true, Limit: 25})
	if len(params) != 9 || params[0] != int64(7) || params[2] != "sft" || params[3] != "question" || params[6] != "pending" || params[8] != 25 {
		t.Fatalf("read and EXPLAIN must use the same normalized parameters: %#v", params)
	}
	if value, ok := params[4].(*time.Time); !ok || value == nil || !value.Equal(cursor) {
		t.Fatalf("cursor must preserve the timestamp and ID full order: %#v", params)
	}
	for _, test := range []struct{ input, want int }{{0, 20}, {-1, 20}, {101, 100}} {
		got := sampleListParams(SampleListQuery{Limit: test.input})
		if got[8] != test.want || got[4].(*time.Time) != nil {
			t.Fatalf("limit %d must normalize to %d with no cursor, got %#v", test.input, test.want, got)
		}
	}
}

func TestSampleKeysetPagesHandleEqualTimestampsAndCancellation(t *testing.T) {
	fixture := newSelectionFixture(t)
	ctx := context.Background()
	if _, err := fixture.pool.Exec(ctx, `UPDATE samples SET created_at = '2026-10-01T00:00:00Z' WHERE project_id = $1`, fixture.projectID); err != nil {
		t.Fatal(err)
	}
	first, err := fixture.batches.ListSamples(ctx, SampleListQuery{ProjectID: fixture.projectID, Limit: 2})
	if err != nil || len(first) != 2 || first[0].ID <= first[1].ID {
		t.Fatalf("first page must use ID as a stable timestamp tie breaker: %+v, %v", first, err)
	}
	last := first[len(first)-1]
	next, err := fixture.batches.ListSamples(ctx, SampleListQuery{ProjectID: fixture.projectID, Cursor: last.CreatedAt, CursorID: last.ID, Limit: 2})
	if err != nil || len(next) != 1 || next[0].ID >= last.ID {
		t.Fatalf("next page must not repeat or omit equal-time identities: %+v, %v", next, err)
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := fixture.batches.ListSamples(cancelled, SampleListQuery{ProjectID: fixture.projectID}); err == nil {
		t.Fatal("cancelled queries must return an error, not a successful empty page")
	}
	if count, err := fixture.batches.CountSamplesByProject(ctx, fixture.projectID); err != nil || count != 3 {
		t.Fatalf("cancellation must not affect persisted samples: %d, %v", count, err)
	}
}

func TestSampleReviewQueryUsesOnlyCurrentVersion(t *testing.T) {
	fixture := newSelectionFixture(t)
	ctx := context.Background()
	old := fixture.versions[0]
	var sampleKey string
	if err := fixture.pool.QueryRow(ctx, `SELECT sample_key FROM samples WHERE id = $1`, old.SampleID).Scan(&sampleKey); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.pool.Exec(ctx, `INSERT INTO review_projections (sample_version_id, project_id, effective_action) VALUES ($1, $2, 'accepted')`, old.ID, fixture.projectID); err != nil {
		t.Fatal(err)
	}
	_, latest, err := fixture.batches.AppendSampleVersion(ctx, AppendSampleVersionInput{
		ProjectID: fixture.projectID, SampleKey: sampleKey,
		TargetKind: model.TargetKindSFT, Title: "当前未判断版本",
		Payload: map[string]any{"question": "新版问题", "reasoning": "新版推理", "answer": "新版答案"},
	})
	if err != nil {
		t.Fatal(err)
	}
	items, err := fixture.batches.ListSamples(ctx, SampleListQuery{ProjectID: fixture.projectID, UnreviewedOnly: true, Limit: 100})
	if err != nil || len(items) != 3 {
		t.Fatalf("old accepted projection must not hide a newer pending version: %+v, %v", items, err)
	}
	for _, item := range items {
		if item.ID == old.SampleID && (item.LatestVersionID != latest.ID || item.ReviewStatus != model.EffectivePending) {
			t.Fatalf("current version identity/projection mismatch: %+v", item)
		}
	}
	if count, err := fixture.batches.CountPendingReviewSamplesByProject(ctx, fixture.projectID); err != nil || count != 3 {
		t.Fatalf("pending count must follow current versions: %d, %v", count, err)
	}
}

func TestSampleReviewQueryPreservesVersionlessRowsAndEmptyScopes(t *testing.T) {
	fixture := newActivityFixture(t)
	ctx := context.Background()
	if items, err := fixture.batches.ListSamples(ctx, SampleListQuery{ProjectID: fixture.projectB}); err != nil || len(items) != 0 {
		t.Fatalf("empty project must return an empty page: %+v, %v", items, err)
	}
	sample, err := fixture.batches.EnsureSample(ctx, fixture.projectA, "versionless", model.TargetKindSFT, "尚未产出版本", nil)
	if err != nil {
		t.Fatal(err)
	}
	items, err := fixture.batches.ListSamples(ctx, SampleListQuery{ProjectID: fixture.projectA, ReviewStatus: model.EffectivePending})
	if err != nil || len(items) != 1 || items[0].ID != sample.ID || items[0].LatestVersionID != 0 || items[0].ReviewStatus != model.EffectivePending {
		t.Fatalf("left joins must retain a sample without a current version as pending: %+v, %v", items, err)
	}
	if count, err := fixture.batches.CountPendingReviewSamplesByProject(ctx, fixture.projectA); err != nil || count != 1 {
		t.Fatalf("pending count must retain the same versionless sample: %d, %v", count, err)
	}
	if items, err := fixture.batches.ListSamples(ctx, SampleListQuery{ProjectID: fixture.projectB}); err != nil || len(items) != 0 {
		t.Fatalf("an empty scope must not leak the other project's row: %+v, %v", items, err)
	}
	if count, err := fixture.batches.CountPendingReviewSamplesByProject(ctx, fixture.projectB); err != nil || count != 0 {
		t.Fatalf("empty project must retain a zero pending count: %d, %v", count, err)
	}
}

func TestProjectWorkflowCountsCurrentVersionsAndDistinctEvidence(t *testing.T) {
	fixture := newSelectionFixture(t)
	ctx := context.Background()
	t.Cleanup(func() {
		_, _ = fixture.pool.Exec(ctx, `DELETE FROM experiments WHERE project_id = $1`, fixture.projectID)
		_, _ = fixture.pool.Exec(ctx, `DELETE FROM releases WHERE project_id = $1`, fixture.projectID)
	})
	for index, action := range []string{"accepted", "quarantined", "conflict"} {
		if _, err := fixture.pool.Exec(ctx, `INSERT INTO review_projections (sample_version_id, project_id, effective_action) VALUES ($1, $2, $3)`, fixture.versions[index].ID, fixture.projectID, action); err != nil {
			t.Fatal(err)
		}
	}
	// Two experiments inspect the same current versions. A repeated score must
	// not turn two inspected sample versions into four overview records.
	for repeat := 0; repeat < 2; repeat++ {
		var experimentID int64
		if err := fixture.pool.QueryRow(ctx, `INSERT INTO experiments (project_id, target_kind) VALUES ($1, 'sft') RETURNING id`, fixture.projectID).Scan(&experimentID); err != nil {
			t.Fatal(err)
		}
		for index, status := range []string{"scored", "missing"} {
			version := fixture.versions[index]
			if _, err := fixture.pool.Exec(ctx, `INSERT INTO experiment_items (experiment_id, project_id, sample_id, sample_version_id, status) VALUES ($1, $2, $3, $4, $5)`, experimentID, fixture.projectID, version.SampleID, version.ID, status); err != nil {
				t.Fatal(err)
			}
		}
	}
	var releaseID int64
	if err := fixture.pool.QueryRow(ctx, `INSERT INTO releases (project_id, release_name, release_name_key, status) VALUES ($1, 'v1', 'v1', 'published') RETURNING id`, fixture.projectID).Scan(&releaseID); err != nil {
		t.Fatal(err)
	}
	old := fixture.versions[0]
	if _, err := fixture.pool.Exec(ctx, `INSERT INTO release_items (release_id, revision, sample_id, sample_version_id, effective_action) VALUES ($1, 1, $2, $3, 'accepted')`, releaseID, old.SampleID, old.ID); err != nil {
		t.Fatal(err)
	}
	counts, err := fixture.batches.ProjectWorkflowCounts(ctx, fixture.projectID)
	if err != nil || counts.Generated != 3 || counts.CurrentVersions != 3 || counts.Accepted != 1 || counts.Quarantined != 1 || counts.Conflicts != 1 || counts.Pending != 0 || counts.Inspected != 2 || counts.Scored != 1 || counts.AcceptedInspected != 1 || counts.PublishedAccepted != 1 || counts.Published != 1 {
		t.Fatalf("current distinct workflow facts: %+v, %v", counts, err)
	}
	var sampleKey string
	if err := fixture.pool.QueryRow(ctx, `SELECT sample_key FROM samples WHERE id = $1`, old.SampleID).Scan(&sampleKey); err != nil {
		t.Fatal(err)
	}
	if _, _, err := fixture.batches.AppendSampleVersion(ctx, AppendSampleVersionInput{ProjectID: fixture.projectID, SampleKey: sampleKey, TargetKind: model.TargetKindSFT, Title: "新版待审", Payload: map[string]any{"question": "q2", "reasoning": "r2", "answer": "a2"}}); err != nil {
		t.Fatal(err)
	}
	counts, err = fixture.batches.ProjectWorkflowCounts(ctx, fixture.projectID)
	if err != nil || counts.Generated != 4 || counts.CurrentVersions != 3 || counts.Accepted != 0 || counts.Pending != 1 || counts.Inspected != 1 || counts.Scored != 0 || counts.AcceptedInspected != 0 || counts.PublishedAccepted != 0 || counts.Published != 1 {
		t.Fatalf("historical accepted/scored/published facts must not mark a new version done: %+v, %v", counts, err)
	}
}

func TestProjectWorkflowCountsEmptyScopeBatchPaginationAndErrors(t *testing.T) {
	fixture := newActivityFixture(t)
	ctx := context.Background()
	t.Cleanup(func() {
		_, _ = fixture.pool.Exec(ctx, `DELETE FROM experiments WHERE project_id = ANY($1::bigint[])`, []int64{fixture.projectA, fixture.projectB})
		_, _ = fixture.pool.Exec(ctx, `DELETE FROM releases WHERE project_id = ANY($1::bigint[])`, []int64{fixture.projectA, fixture.projectB})
	})
	empty, err := fixture.batches.ProjectWorkflowCounts(ctx, fixture.projectB)
	if err != nil || empty != (ProjectWorkflowCounts{}) {
		t.Fatalf("empty scope must return genuine zero facts: %+v, %v", empty, err)
	}
	fixture.seedPendingReview(t, fixture.projectA)
	if _, err := fixture.batches.EnsureSample(ctx, fixture.projectA, "versionless-overview", model.TargetKindSFT, "尚未产出", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.pool.Exec(ctx, `INSERT INTO batches (project_id, purpose, status, target_kind, schema_version)
		SELECT $1, 'pilot', CASE WHEN n <= 100 THEN 'completed' ELSE 'paused' END, 'sft', 'sft.sample.v1' FROM generate_series(1, 101) n`, fixture.projectA); err != nil {
		t.Fatal(err)
	}
	fixture.seedFailedBatch(t, fixture.projectA)
	if _, err := fixture.pool.Exec(ctx, `INSERT INTO releases (project_id, release_name, release_name_key, status) VALUES ($1, 'blocked', 'blocked', 'blocked')`, fixture.projectA); err != nil {
		t.Fatal(err)
	}
	counts, err := fixture.batches.ProjectWorkflowCounts(ctx, fixture.projectA)
	if err != nil || counts.CurrentVersions != 1 || counts.Generated != 1 || counts.Pending != 2 || counts.BatchTotal != 102 || counts.Completed != 100 || counts.Paused != 1 || counts.Failed != 1 || counts.ReleasePending != 1 {
		t.Fatalf("counts must not be truncated to a 100-row list or invent a versionless output: %+v, %v", counts, err)
	}
	empty, err = fixture.batches.ProjectWorkflowCounts(ctx, fixture.projectB)
	if err != nil || empty != (ProjectWorkflowCounts{}) {
		t.Fatalf("another project's samples, batches and releases must not leak: %+v, %v", empty, err)
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := fixture.batches.ProjectWorkflowCounts(cancelled, fixture.projectA); err == nil {
		t.Fatal("failed statistics query must return an error, not zeros")
	}
}

func TestProjectWorkflowCountsDoesNotCreditOtherProjectEvidence(t *testing.T) {
	fixture := newActivityFixture(t)
	ctx := context.Background()
	t.Cleanup(func() {
		_, _ = fixture.pool.Exec(ctx, `DELETE FROM experiments WHERE project_id = $1`, fixture.projectB)
		_, _ = fixture.pool.Exec(ctx, `DELETE FROM releases WHERE project_id = $1`, fixture.projectB)
	})
	versionID := fixture.seedPendingReview(t, fixture.projectA)
	var sampleID, experimentID, releaseID int64
	if err := fixture.pool.QueryRow(ctx, `SELECT sample_id FROM sample_versions WHERE id = $1`, versionID).Scan(&sampleID); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.pool.Exec(ctx, `INSERT INTO review_projections (sample_version_id, project_id, effective_action) VALUES ($1, $2, 'accepted')`, versionID, fixture.projectA); err != nil {
		t.Fatal(err)
	}
	// Deliberately malformed cross-project fixture references are possible with
	// legacy single-column foreign keys. A read must still enforce its scope.
	if err := fixture.pool.QueryRow(ctx, `INSERT INTO experiments (project_id, target_kind) VALUES ($1, 'sft') RETURNING id`, fixture.projectB).Scan(&experimentID); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.pool.Exec(ctx, `INSERT INTO experiment_items (experiment_id, project_id, sample_id, sample_version_id, status) VALUES ($1, $2, $3, $4, 'scored')`, experimentID, fixture.projectB, sampleID, versionID); err != nil {
		t.Fatal(err)
	}
	if err := fixture.pool.QueryRow(ctx, `INSERT INTO releases (project_id, release_name, release_name_key, status) VALUES ($1, 'foreign', 'foreign', 'published') RETURNING id`, fixture.projectB).Scan(&releaseID); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.pool.Exec(ctx, `INSERT INTO release_items (release_id, revision, sample_id, sample_version_id, effective_action) VALUES ($1, 1, $2, $3, 'accepted')`, releaseID, sampleID, versionID); err != nil {
		t.Fatal(err)
	}
	counts, err := fixture.batches.ProjectWorkflowCounts(ctx, fixture.projectA)
	if err != nil || counts.Accepted != 1 || counts.Inspected != 0 || counts.Scored != 0 || counts.PublishedAccepted != 0 || counts.Published != 0 {
		t.Fatalf("another project's frozen references cannot credit inspection or publication: %+v, %v", counts, err)
	}
	other, err := fixture.batches.ProjectWorkflowCounts(ctx, fixture.projectB)
	if err != nil || other.Generated != 0 || other.CurrentVersions != 0 || other.Accepted != 0 || other.Inspected != 0 || other.Scored != 0 || other.PublishedAccepted != 0 {
		t.Fatalf("foreign references cannot create current samples in an empty project: %+v, %v", other, err)
	}
}

type samplePerformanceMetric struct {
	Name        string  `json:"name"`
	Concurrency int     `json:"concurrency"`
	Runs        int     `json:"runs"`
	P50MS       float64 `json:"p50Ms"`
	P95MS       float64 `json:"p95Ms"`
	MaxMS       float64 `json:"maxMs"`
	FirstMS     float64 `json:"firstMs"`
}

type samplePerformanceReport struct {
	RecordedAt      string                     `json:"recordedAt"`
	GoVersion       string                     `json:"goVersion"`
	PostgresVersion string                     `json:"postgresVersion"`
	RuntimeCPUs     int                        `json:"runtimeCpus"`
	GOMAXPROCS      int                        `json:"gomaxprocs"`
	Samples         int                        `json:"samples"`
	Versions        int                        `json:"versions"`
	Pending         int                        `json:"pending"`
	SeedMS          float64                    `json:"seedMs"`
	Metrics         []samplePerformanceMetric  `json:"metrics"`
	Plans           map[string]json.RawMessage `json:"plans"`
	Indexes         []string                   `json:"indexes"`
}

// TestSampleQueryPerformance100K is an opt-in real Postgres measurement. It
// seeds a dedicated fixture; no simulated HTTP timings or provider calls are
// substituted for database work. Use the temporary-Postgres runner to execute.
func TestSampleQueryPerformance100K(t *testing.T) {
	if os.Getenv("LLM_RUN_SAMPLE_PERFORMANCE") != "1" {
		t.Skip("opt-in 100K measurement: set LLM_RUN_SAMPLE_PERFORMANCE=1 on a temporary database")
	}
	fixture := newActivityFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	const sampleCount = 100000
	seedStart := time.Now()
	// Equal timestamps deliberately exercise the keyset ID tie breaker; update
	// timestamps run in the opposite direction to creation timestamps so the
	// older updated_at index cannot accidentally stand in for the read order.
	_, err := fixture.pool.Exec(ctx, `
INSERT INTO samples (project_id, sample_key, target_kind, title, latest_version, created_at, updated_at)
SELECT $1, 'perf-' || n, 'sft', '十万样本性能基准 ' || n, 1,
       '2026-10-01T00:00:00Z'::timestamptz + (n / 10) * interval '1 millisecond',
       '2026-10-02T00:00:00Z'::timestamptz - n * interval '1 millisecond'
FROM generate_series(1, $2::int) n`, fixture.projectA, sampleCount)
	if err != nil {
		t.Fatalf("seed samples: %v", err)
	}
	_, err = fixture.pool.Exec(ctx, `
INSERT INTO sample_versions (sample_id, project_id, version, target_kind, schema_version, payload, content_hash)
SELECT s.id, s.project_id, 1, 'sft', 'sft.sample.v1',
       jsonb_build_object('question', s.title, 'reasoning', repeat('可复核推理。', 40), 'answer', '明确答案'),
       md5(s.sample_key)
FROM samples s WHERE s.project_id = $1`, fixture.projectA)
	if err != nil {
		t.Fatalf("seed versions: %v", err)
	}
	// 80% accepted, 10% explicit pending, 10% absent projections. Both kinds
	// of pending must remain visible and included in the authoritative count.
	_, err = fixture.pool.Exec(ctx, `
INSERT INTO review_projections (sample_version_id, project_id, effective_action)
SELECT v.id, v.project_id, CASE WHEN s.id % 10 < 8 THEN 'accepted' ELSE 'pending' END
FROM sample_versions v JOIN samples s ON s.id = v.sample_id
WHERE v.project_id = $1 AND s.id % 10 <> 9`, fixture.projectA)
	if err != nil {
		t.Fatalf("seed review projections: %v", err)
	}
	if _, err := fixture.pool.Exec(ctx, `ANALYZE samples; ANALYZE sample_versions; ANALYZE review_projections`); err != nil {
		t.Fatal(err)
	}
	report := samplePerformanceReport{RecordedAt: time.Now().UTC().Format(time.RFC3339),
		GoVersion: runtime.Version(), RuntimeCPUs: runtime.NumCPU(), GOMAXPROCS: runtime.GOMAXPROCS(0), Samples: sampleCount,
		Versions: sampleCount, Pending: 20000, SeedMS: float64(time.Since(seedStart).Microseconds()) / 1000,
		Plans: map[string]json.RawMessage{}}
	if err := fixture.pool.QueryRow(ctx, `SHOW server_version`).Scan(&report.PostgresVersion); err != nil {
		t.Fatal(err)
	}
	indexRows, err := fixture.pool.Query(ctx, `SELECT indexdef FROM pg_indexes WHERE tablename = 'samples' ORDER BY indexname`)
	if err != nil {
		t.Fatal(err)
	}
	for indexRows.Next() {
		var definition string
		if err := indexRows.Scan(&definition); err != nil {
			t.Fatal(err)
		}
		report.Indexes = append(report.Indexes, definition)
	}
	indexRows.Close()
	if err := indexRows.Err(); err != nil {
		t.Fatal(err)
	}
	if count, err := fixture.batches.CountSamplesByProject(ctx, fixture.projectA); err != nil || count != sampleCount {
		t.Fatalf("sample count %d: %v", count, err)
	}
	if count, err := fixture.batches.CountSampleVersionsByProject(ctx, fixture.projectA); err != nil || count != sampleCount {
		t.Fatalf("version count %d: %v", count, err)
	}
	if count, err := fixture.batches.CountPendingReviewSamplesByProject(ctx, fixture.projectA); err != nil || count != report.Pending {
		t.Fatalf("pending count must include absent projections: %d, %v", count, err)
	}
	queries := []struct {
		name  string
		query SampleListQuery
	}{
		{"first-page", SampleListQuery{ProjectID: fixture.projectA, Limit: 100}},
		{"next-page", SampleListQuery{ProjectID: fixture.projectA, Limit: 100}},
		{"middle-page", SampleListQuery{ProjectID: fixture.projectA, Limit: 100}},
		{"last-page", SampleListQuery{ProjectID: fixture.projectA, Limit: 100}},
		{"unreviewed-first-page", SampleListQuery{ProjectID: fixture.projectA, Limit: 100, UnreviewedOnly: true}},
	}
	// Fixture cursor acquisition is outside the timed production calls. The
	// production path remains keyset pagination and never uses this OFFSET.
	for index, offset := range []int{100, 50000, 99900} {
		query := &queries[index+1].query
		if err := fixture.pool.QueryRow(ctx, `SELECT created_at, id FROM samples WHERE project_id=$1 ORDER BY created_at DESC, id DESC OFFSET $2 LIMIT 1`, fixture.projectA, offset-1).Scan(&query.Cursor, &query.CursorID); err != nil {
			t.Fatal(err)
		}
	}
	for _, test := range queries {
		query := test.query
		run := func() error {
			items, err := fixture.batches.ListSamples(ctx, query)
			if err != nil {
				return err
			}
			if len(items) != 100 {
				return fmt.Errorf("%s returned %d items", test.name, len(items))
			}
			for index, item := range items {
				if item.ProjectID != fixture.projectA || item.LatestVersionID <= 0 {
					return fmt.Errorf("invalid identity/version for %s", test.name)
				}
				if query.UnreviewedOnly && item.ReviewStatus == model.EffectiveAccepted {
					return fmt.Errorf("accepted item escaped filter")
				}
				if index > 0 && !(item.CreatedAt.Before(items[index-1].CreatedAt) || (item.CreatedAt.Equal(items[index-1].CreatedAt) && item.ID < items[index-1].ID)) {
					return fmt.Errorf("unordered page")
				}
			}
			return nil
		}
		report.Metrics = append(report.Metrics, measureSampleReads(t, test.name, 1, 30, run))
		var plan []byte
		if err := fixture.pool.QueryRow(ctx, `EXPLAIN (ANALYZE, BUFFERS, FORMAT JSON) `+listSamplesSQL, sampleListParams(query)...).Scan(&plan); err != nil {
			t.Fatal(err)
		}
		report.Plans[test.name] = plan
	}
	countRun := func() error {
		count, err := fixture.batches.CountPendingReviewSamplesByProject(ctx, fixture.projectA)
		if err == nil && count != report.Pending {
			return fmt.Errorf("pending count drift: %d", count)
		}
		return err
	}
	report.Metrics = append(report.Metrics, measureSampleReads(t, "pending-count", 1, 15, countRun))
	var countPlan []byte
	if err := fixture.pool.QueryRow(ctx, `EXPLAIN (ANALYZE, BUFFERS, FORMAT JSON) `+countPendingReviewSamplesSQL, fixture.projectA, model.EffectivePending).Scan(&countPlan); err != nil {
		t.Fatal(err)
	}
	report.Plans["pending-count"] = countPlan
	firstRun := func() error { _, err := fixture.batches.ListSamples(ctx, queries[0].query); return err }
	report.Metrics = append(report.Metrics, measureSampleReads(t, "first-page-concurrent", 4, 20, firstRun))
	if items, err := fixture.batches.ListSamples(ctx, SampleListQuery{ProjectID: fixture.projectB, Limit: 100}); err != nil || len(items) != 0 {
		t.Fatalf("empty other project must not leak sample data: %d, %v", len(items), err)
	}
	if count, err := fixture.batches.CountPendingReviewSamplesByProject(ctx, fixture.projectB); err != nil || count != 0 {
		t.Fatalf("empty other project count: %d, %v", count, err)
	}
	output := os.Getenv("LLM_SAMPLE_PERFORMANCE_REPORT")
	if output == "" {
		output = "/tmp/sample-performance-100k.json"
	}
	if err := os.MkdirAll(filepath.Dir(output), 0755); err != nil {
		t.Fatal(err)
	}
	encoded, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(output, append(encoded, '\n'), 0644); err != nil {
		t.Fatal(err)
	}
	t.Logf("100K performance report: %s; seed %.2f ms", output, report.SeedMS)
}

func measureSampleReads(t *testing.T, name string, concurrency, runs int, operation func() error) samplePerformanceMetric {
	t.Helper()
	start := time.Now()
	if err := operation(); err != nil {
		t.Fatalf("%s initial read: %v", name, err)
	}
	first := float64(time.Since(start).Microseconds()) / 1000
	durations := make(chan float64, concurrency*runs)
	errors := make(chan error, concurrency*runs)
	var workers sync.WaitGroup
	for worker := 0; worker < concurrency; worker++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for run := 0; run < runs; run++ {
				start := time.Now()
				if err := operation(); err != nil {
					errors <- err
				}
				durations <- float64(time.Since(start).Microseconds()) / 1000
			}
		}()
	}
	workers.Wait()
	close(durations)
	close(errors)
	for err := range errors {
		t.Fatalf("%s read failed: %v", name, err)
	}
	values := make([]float64, 0, concurrency*runs)
	for value := range durations {
		values = append(values, value)
	}
	sort.Float64s(values)
	metric := samplePerformanceMetric{Name: name, Concurrency: concurrency, Runs: len(values), FirstMS: first,
		P50MS: values[int(math.Ceil(float64(len(values))*0.5))-1],
		P95MS: values[int(math.Ceil(float64(len(values))*0.95))-1], MaxMS: values[len(values)-1]}
	t.Logf("%s n=%d concurrency=%d first=%.2fms p50=%.2fms p95=%.2fms max=%.2fms", name, metric.Runs, concurrency, first, metric.P50MS, metric.P95MS, metric.MaxMS)
	return metric
}
