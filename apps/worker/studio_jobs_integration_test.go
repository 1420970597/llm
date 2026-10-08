package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	appcrypto "github.com/1420970597/llm/internal/crypto"
	"github.com/1420970597/llm/internal/model"
	"github.com/1420970597/llm/internal/storage"
	"github.com/1420970597/llm/internal/store"
	"github.com/1420970597/llm/internal/studio"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
)

// These tests run the production dispatcher, consumer, maintenance loop and
// release builder against disposable Postgres, Redis and MinIO services.
// scripts/go-test-studio-failures.sh supplies those services and rejects skips.
// The source ingestion handler is used for queue tests so recovery proves a
// real durable business write, rather than a test-only success handler.
type studioFaultFixture struct {
	pool              *pgxpool.Pool
	rt                *studioRuntime
	projectID, userID int64
}

func newStudioFaultFixture(t *testing.T) studioFaultFixture {
	t.Helper()
	dsn, addr := os.Getenv("LLM_TEST_POSTGRES_DSN"), os.Getenv("LLM_TEST_REDIS_ADDR")
	if dsn == "" || addr == "" {
		t.Skip("real Postgres/Redis missing; run scripts/go-test-studio-failures.sh")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	suffix := fmt.Sprintf("%d-%d", os.Getpid(), time.Now().UnixNano())
	var workspaceID, userID int64
	if err := pool.QueryRow(ctx, `INSERT INTO workspaces(name,slug) VALUES($1,$2) RETURNING id`, "fault acceptance", "fault-"+suffix).Scan(&workspaceID); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO users(email,hashed_password,role) VALUES($1,'x','user') RETURNING id`, "fault-"+suffix+"@example.test").Scan(&userID); err != nil {
		t.Fatal(err)
	}
	input := model.CreateProjectInput{Name: "fault acceptance", TargetKind: model.TargetKindSFT}
	input.Normalize()
	project, err := store.NewProjectStore(pool).CreateProject(ctx, workspaceID, userID, input)
	if err != nil {
		t.Fatal(err)
	}
	client := redis.NewClient(&redis.Options{Addr: addr, DialTimeout: 200 * time.Millisecond, ReadTimeout: 300 * time.Millisecond, WriteTimeout: 300 * time.Millisecond, MaxRetries: -1})
	t.Cleanup(func() { _ = client.Close() })
	queue := "studio-fault-" + suffix
	rt := &studioRuntime{env: &StudioJobEnv{Pool: pool, Jobs: store.NewJobStore(pool), Redis: client, Queue: queue, Owner: "fault-worker-" + suffix}, legacyQueue: queue + "-legacy", lease: time.Minute, concurrency: 1, enabled: true}
	t.Cleanup(func() {
		_, _ = client.Del(ctx, queue, queue+"-rejected", queue+"-legacy").Result()
		_, _ = pool.Exec(ctx, `DELETE FROM outbox WHERE payload->>'projectId'=$1 OR payload->>'jobId' IN (SELECT id::text FROM jobs WHERE project_id=$2)`, fmt.Sprint(project.ID), project.ID)
		_, _ = pool.Exec(ctx, `DELETE FROM audit_logs WHERE project_id=$1`, project.ID)
		_, _ = pool.Exec(ctx, `DELETE FROM release_items WHERE release_id IN (SELECT id FROM releases WHERE project_id=$1)`, project.ID)
		_, _ = pool.Exec(ctx, `DELETE FROM releases WHERE project_id=$1`, project.ID)
		_, _ = pool.Exec(ctx, `DELETE FROM projects WHERE id=$1`, project.ID)
		_, _ = pool.Exec(ctx, `DELETE FROM workspace_members WHERE workspace_id=$1`, workspaceID)
		_, _ = pool.Exec(ctx, `DELETE FROM workspaces WHERE id=$1`, workspaceID)
		_, _ = pool.Exec(ctx, `DELETE FROM users WHERE id=$1`, userID)
	})
	return studioFaultFixture{pool: pool, rt: rt, projectID: project.ID, userID: userID}
}

func (f studioFaultFixture) sourceJob(t *testing.T) model.Job {
	t.Helper()
	row, _, err := store.NewLegacyImportStore(f.pool).QueueSourceImport(context.Background(), store.QueueSourceImportInput{
		ProjectID: f.projectID, SourceKind: store.SourceKindDocument, SourceKey: "failure-fixture", Content: []byte("# Source\nA durable source paragraph for recovery."),
		Options: store.SourceImportOptions{FileName: "source.md", Kind: "markdown", ActorID: f.userID, Chunking: model.DefaultSourceChunking()},
	})
	if err != nil || row.JobID == nil {
		t.Fatalf("queue source import: %+v %v", row, err)
	}
	job, err := f.rt.env.Jobs.GetJob(context.Background(), *row.JobID)
	if err != nil {
		t.Fatal(err)
	}
	return job
}

func waitStudioFault(t *testing.T, label string, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("timeout waiting for %s", label)
}

func (f studioFaultFixture) consume(t *testing.T) func() {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); f.rt.consumeLoop(ctx) }()
	return func() {
		cancel()
		select {
		case <-done:
		case <-time.After(8 * time.Second):
			t.Fatal("consumer failed to stop")
		}
	}
}

func (f studioFaultFixture) assertIngested(t *testing.T, jobID int64) {
	t.Helper()
	waitStudioFault(t, "source job success", func() bool {
		j, err := f.rt.env.Jobs.GetJob(context.Background(), jobID)
		return err == nil && j.Status == model.JobStatusSucceeded
	})
	var count int
	if err := f.pool.QueryRow(context.Background(), `SELECT COUNT(*) FROM source_chunks WHERE project_id=$1`, f.projectID).Scan(&count); err != nil || count != 1 {
		t.Fatalf("recovery must write exactly one real source chunk, got %d: %v", count, err)
	}
}

func TestStudioFaultRedisInterruptionPreservesOutboxAndRecovers(t *testing.T) {
	f := newStudioFaultFixture(t)
	control := os.Getenv("LLM_TEST_FAULT_CONTROL_DIR")
	if control == "" {
		t.Skip("container stop/start controller missing")
	}
	job := f.sourceJob(t)
	if err := os.WriteFile(filepath.Join(control, "redis-stop"), []byte("stop"), 0600); err != nil {
		t.Fatal(err)
	}
	waitStudioFault(t, "actual Redis container interruption", func() bool { return f.rt.env.Redis.Ping(context.Background()).Err() != nil })
	f.rt.dispatchOnce(context.Background())
	var status, lastError string
	if err := f.pool.QueryRow(context.Background(), `SELECT status,last_error FROM outbox WHERE payload->>'jobId'=$1`, fmt.Sprint(job.ID)).Scan(&status, &lastError); err != nil {
		t.Fatal(err)
	}
	if status != model.OutboxStatusPending || lastError == "" {
		t.Fatalf("Redis interruption must preserve pending outbox plus error, got %s %q", status, lastError)
	}
	if err := os.WriteFile(filepath.Join(control, "redis-start"), []byte("start"), 0600); err != nil {
		t.Fatal(err)
	}
	waitStudioFault(t, "Redis restart", func() bool { return f.rt.env.Redis.Ping(context.Background()).Err() == nil })
	// Advance only the disposable outbox clock so the recovery assertion does
	// not wait for the production 30-second retry backoff.
	if _, err := f.pool.Exec(context.Background(), `UPDATE outbox SET next_attempt_at=NOW() WHERE payload->>'jobId'=$1`, fmt.Sprint(job.ID)); err != nil {
		t.Fatal(err)
	}
	f.rt.dispatchOnce(context.Background())
	stop := f.consume(t)
	defer stop()
	f.assertIngested(t, job.ID)
}

func TestStudioFaultLeaseRedeliveryRejectsStaleWorker(t *testing.T) {
	f := newStudioFaultFixture(t)
	ctx := context.Background()
	job := f.sourceJob(t)
	old, claimed, err := f.rt.env.Jobs.ClaimJobByID(ctx, job.ID, "crashed-worker", time.Minute)
	if err != nil || !claimed {
		t.Fatalf("initial claim: %v %v", claimed, err)
	}
	if _, err := f.pool.Exec(ctx, `UPDATE jobs SET lease_until=NOW()-INTERVAL '1 second' WHERE id=$1`, job.ID); err != nil {
		t.Fatal(err)
	}
	f.rt.maintainOnce(ctx)
	current, err := f.rt.env.Jobs.GetJob(ctx, job.ID)
	if err != nil || current.Status != model.JobStatusPending {
		t.Fatalf("expired lease must requeue: %+v %v", current, err)
	}
	if _, err := f.rt.env.Jobs.CompleteJob(ctx, old.ID, old.LeaseOwner, old.FencingToken, map[string]any{"stale": true}); !errors.Is(err, store.ErrJobLeaseLost) {
		t.Fatalf("crashed worker must be fenced before redelivery: %v", err)
	}
	f.rt.dispatchOnce(ctx)
	stop := f.consume(t)
	defer stop()
	f.assertIngested(t, job.ID)
	if _, err := f.rt.env.Jobs.CompleteJob(ctx, old.ID, old.LeaseOwner, old.FencingToken, map[string]any{"stale": true}); err != nil && !errors.Is(err, store.ErrJobLeaseLost) {
		t.Fatalf("late completion returned unexpected error: %v", err)
	}
	// The completed job is idempotent, but its payload must remain the real
	// import result and must never be overwritten by the crashed worker.
	completed, err := f.rt.env.Jobs.GetJob(ctx, job.ID)
	if err != nil || strings.Contains(string(completed.Payload), "stale") {
		t.Fatalf("stale worker overwrote recovered result: %s %v", completed.Payload, err)
	}
}

func TestStudioFaultLostNotificationIsRearmed(t *testing.T) {
	f := newStudioFaultFixture(t)
	ctx := context.Background()
	job := f.sourceJob(t)
	f.rt.dispatchOnce(ctx)
	if err := f.rt.env.Redis.Del(ctx, f.rt.env.Queue).Err(); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(ctx, `UPDATE outbox SET dispatched_at=NOW()-INTERVAL '2 minutes' WHERE payload->>'jobId'=$1`, fmt.Sprint(job.ID)); err != nil {
		t.Fatal(err)
	}
	f.rt.maintainOnce(ctx)
	f.rt.dispatchOnce(ctx)
	stop := f.consume(t)
	defer stop()
	f.assertIngested(t, job.ID)
}

func TestStudioFaultIncompatibleEnvelopeIsPreservedAndValidReplaySucceeds(t *testing.T) {
	f := newStudioFaultFixture(t)
	ctx := context.Background()
	job := f.sourceJob(t)
	raw := fmt.Sprintf(`{"schemaVersion":999,"jobId":%d,"kind":%q}`, job.ID, job.Kind)
	if err := f.rt.env.Redis.LPush(ctx, f.rt.env.Queue, raw).Err(); err != nil {
		t.Fatal(err)
	}
	stop := f.consume(t)
	defer stop()
	waitStudioFault(t, "rejected envelope", func() bool { return f.rt.env.Redis.LLen(ctx, f.rt.env.Queue+"-rejected").Val() == 1 })
	rejected, err := f.rt.env.Redis.LIndex(ctx, f.rt.env.Queue+"-rejected", 0).Result()
	if err != nil || rejected != raw {
		t.Fatalf("dead letter must preserve exact raw bytes: %q %v", rejected, err)
	}
	current, err := f.rt.env.Jobs.GetJob(ctx, job.ID)
	if err != nil || current.Status != model.JobStatusPending || current.Attempt != 0 {
		t.Fatalf("bad envelope must not claim job: %+v %v", current, err)
	}
	f.rt.dispatchOnce(ctx)
	f.assertIngested(t, job.ID)
}

func TestStudioFaultRollbackPausesQueueAndResumeCompletes(t *testing.T) {
	f := newStudioFaultFixture(t)
	ctx := context.Background()
	job := f.sourceJob(t)
	f.rt.dispatchOnce(ctx)
	f.rt.enabled = false
	stop := f.consume(t)
	time.Sleep(200 * time.Millisecond)
	if count := f.rt.env.Redis.LLen(ctx, f.rt.env.Queue).Val(); count != 1 {
		t.Fatalf("rollback must leave message in Redis, got %d", count)
	}
	paused, err := f.rt.env.Jobs.GetJob(ctx, job.ID)
	if err != nil || paused.Status != model.JobStatusPending || paused.Attempt != 0 {
		t.Fatalf("paused worker must not claim: %+v %v", paused, err)
	}
	stop()
	f.rt.enabled = true
	stop = f.consume(t)
	defer stop()
	f.assertIngested(t, job.ID)
}

func TestStudioFaultUnknownWorkerKindPreservesPendingUntilCompatible(t *testing.T) {
	f := newStudioFaultFixture(t)
	ctx := context.Background()
	job := f.sourceJob(t)
	if _, err := f.pool.Exec(ctx, `UPDATE jobs SET job_kind='studio.future.source_ingest' WHERE id=$1`, job.ID); err != nil {
		t.Fatal(err)
	}
	f.rt.dispatchOnce(ctx)
	current, err := f.rt.env.Jobs.GetJob(ctx, job.ID)
	if err != nil || current.Status != model.JobStatusPending || current.Attempt != 0 {
		t.Fatalf("old worker must preserve an unknown durable job: %+v %v", current, err)
	}
	if count := f.rt.env.Redis.LLen(ctx, f.rt.env.Queue).Val(); count != 0 {
		t.Fatalf("unknown handler must not enqueue unusable notification, got %d", count)
	}
	var lastError string
	if err := f.pool.QueryRow(ctx, `SELECT last_error FROM outbox WHERE payload->>'jobId'=$1`, fmt.Sprint(job.ID)).Scan(&lastError); err != nil || !strings.Contains(lastError, "未注册") {
		t.Fatalf("version mismatch must be explained in outbox: %q %v", lastError, err)
	}
	// Restore the compatible production kind rather than registering a fake
	// future handler: the same durable job then produces real source chunks.
	if _, err := f.pool.Exec(ctx, `UPDATE jobs SET job_kind=$2 WHERE id=$1`, job.ID, job.Kind); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(ctx, `UPDATE outbox SET next_attempt_at=NOW() WHERE payload->>'jobId'=$1`, fmt.Sprint(job.ID)); err != nil {
		t.Fatal(err)
	}
	f.rt.dispatchOnce(ctx)
	stop := f.consume(t)
	defer stop()
	f.assertIngested(t, job.ID)
}

func TestStudioFaultObjectStoreFailureRetriesSameReleaseAndHash(t *testing.T) {
	f := newStudioFaultFixture(t)
	ctx := context.Background()
	endpoint := os.Getenv("LLM_TEST_S3_ENDPOINT")
	if endpoint == "" {
		t.Skip("real MinIO missing")
	}
	box, err := appcrypto.NewSecretBox("phase1-dev-only-32-byte-secret!!!")
	if err != nil {
		t.Fatal(err)
	}
	admin := store.NewAdminStore(f.pool, box)
	profile := model.StorageProfile{Name: "fault-storage", Provider: "minio", Endpoint: endpoint, Region: "us-east-1", Bucket: "studio-fault-acceptance", AccessKeyID: os.Getenv("LLM_TEST_S3_ACCESS_KEY"), SecretAccessKey: "wrong-secret-for-fault", UsePathStyle: true, IsActive: true, IsDefault: true}
	profile, err = admin.UpsertStorageProfile(ctx, profile)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = f.pool.Exec(ctx, `DELETE FROM storage_profiles WHERE id=$1`, profile.ID) })
	f.rt.env.SetExtra("datasetStore", store.NewDatasetStore(f.pool, box))
	batch := store.NewBatchStore(f.pool)
	sample, err := batch.EnsureSample(ctx, f.projectID, "release-fault", model.TargetKindSFT, "release fault", nil)
	if err != nil {
		t.Fatal(err)
	}
	_, version, err := batch.AppendSampleVersion(ctx, store.AppendSampleVersionInput{ProjectID: f.projectID, SampleKey: sample.SampleKey, TargetKind: model.TargetKindSFT, Title: "release fault", Payload: map[string]any{"question": "question", "reasoning": "reasoning", "answer": "answer"}})
	if err != nil {
		t.Fatal(err)
	}
	reviews := store.NewReviewStore(f.pool)
	projection, err := reviews.GetProjection(ctx, f.projectID, version.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reviews.SubmitDecision(ctx, f.projectID, f.userID, model.SubmitDecisionInput{SampleVersionID: version.ID, EvidenceRevision: projection.EvidenceRevision, ReviewerRevision: 1, Action: model.DecisionAccept, Reason: "acceptance fixture"}); err != nil {
		t.Fatal(err)
	}
	docs := store.NewDocumentStore(f.pool)
	_, mapping, err := docs.SaveVersion(ctx, f.projectID, model.KindMapping, f.userID, store.SaveDocumentVersionInput{ChangeReason: "acceptance mapping", Payload: model.MappingPayload{SchemaVersion: model.SchemaVersionFor(model.KindMapping), Format: model.ExportFormatJSONL, Fields: []model.MappingField{{TargetField: "question", SourceField: "question", Required: true}, {TargetField: "reasoning", SourceField: "chain_of_thought", Required: true}, {TargetField: "answer", SourceField: "answer", Required: true}}}})
	if err != nil {
		t.Fatal(err)
	}
	releases := store.NewReleaseStore(f.pool)
	release, err := releases.CreateReleaseCandidate(ctx, store.CreateReleaseCandidateInput{ProjectID: f.projectID, ReleaseName: "fault-v1", MappingVersionID: mapping.ID, Format: "jsonl", IntendedUse: "acceptance fixture", SampleVersionIDs: []int64{version.ID}, CreatedBy: &f.userID})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := releases.FreezeRelease(ctx, f.projectID, release.ID, f.userID); err != nil {
		t.Fatal(err)
	}
	var jobID int64
	if err := f.pool.QueryRow(ctx, `SELECT id FROM jobs WHERE project_id=$1 AND job_kind=$2`, f.projectID, model.JobKindReleaseBuild).Scan(&jobID); err != nil {
		t.Fatalf("freeze must enqueue release build: %v", err)
	}
	claimed, ok, err := f.rt.env.Jobs.ClaimJobByID(ctx, jobID, f.rt.env.Owner, time.Minute)
	if err != nil || !ok {
		t.Fatalf("claim: %v %v", ok, err)
	}
	_, err = handleReleaseBuild(ctx, f.rt.env, claimed)
	if err == nil || !strings.Contains(err.Error(), "写入对象失败") {
		t.Fatalf("bad MinIO secret must fail actual upload, got %v", err)
	}
	failed, err := releases.GetRelease(ctx, f.projectID, release.ID)
	if err != nil || failed.Status != model.ReleaseStatusBuildFailed {
		t.Fatalf("failed upload must be explicit build_failed: %+v %v", failed, err)
	}
	health, err := studio.LoadStudioHealth(ctx, f.pool, studio.Rollout{})
	if err != nil || health.Releases.BuildFailed != 1 {
		t.Fatalf("storage failure must appear in rollout health: %+v %v", health.Releases, err)
	}
	artifacts := store.NewReleaseArtifactStore(f.pool)
	rows, err := artifacts.ListArtifacts(ctx, release.ID, release.CandidateRevision)
	if err != nil || len(rows) != 0 {
		t.Fatalf("failed upload must register zero valid artifacts: %+v %v", rows, err)
	}
	// The failed attempt consumes its original job identity. Manual publication
	// after the cause is repaired reuses that job and the same frozen release.
	if _, err := f.rt.env.Jobs.FailJob(ctx, jobID, claimed.LeaseOwner, claimed.FencingToken, model.ErrorClassConfig, "controlled invalid object credentials"); err != nil {
		t.Fatal(err)
	}
	profile.SecretAccessKey = os.Getenv("LLM_TEST_S3_SECRET_KEY")
	profile, err = admin.UpsertStorageProfile(ctx, profile)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := releases.FreezeRelease(ctx, f.projectID, release.ID, f.userID); err != nil {
		t.Fatal(err)
	}
	current, ok, err := f.rt.env.Jobs.ClaimJobByID(ctx, jobID, f.rt.env.Owner, time.Minute)
	if err != nil || !ok {
		t.Fatalf("same job retry: %v %v", ok, err)
	}
	f.rt.execute(ctx, current)
	published, err := releases.GetRelease(ctx, f.projectID, release.ID)
	if err != nil || published.Status != model.ReleaseStatusPublished || published.ID != release.ID {
		t.Fatalf("repair must publish the same identity: %+v %v", published, err)
	}
	health, err = studio.LoadStudioHealth(ctx, f.pool, studio.Rollout{})
	if err != nil || health.Releases.BuildFailed != 0 {
		t.Fatalf("recovered publication must clear the health failure: %+v %v", health.Releases, err)
	}
	rows, err = artifacts.ListArtifacts(ctx, release.ID, release.CandidateRevision)
	if err != nil || len(rows) != 1 || rows[0].State != model.ArtifactStateVerified {
		t.Fatalf("one verified artifact required: %+v %v", rows, err)
	}
	objectStore, err := storage.New(storage.Profile{Endpoint: endpoint, Region: "us-east-1", Bucket: profile.Bucket, AccessKeyID: os.Getenv("LLM_TEST_S3_ACCESS_KEY"), SecretKey: os.Getenv("LLM_TEST_S3_SECRET_KEY"), UsePathStyle: true})
	if err != nil {
		t.Fatal(err)
	}
	bytes, err := objectStore.ReadBytes(ctx, rows[0].ObjectKey)
	if err != nil || model.ComputeArtifactHash(bytes) != rows[0].ArtifactHash {
		t.Fatalf("actual downloaded bytes must match frozen hash: %v", err)
	}
	if _, err := handleReleaseBuild(ctx, f.rt.env, current); err != nil {
		t.Fatalf("published replay: %v", err)
	}
	replayed, err := artifacts.ListArtifacts(ctx, release.ID, release.CandidateRevision)
	if err != nil || len(replayed) != 1 || replayed[0].ArtifactHash != rows[0].ArtifactHash {
		t.Fatalf("replay must preserve one artifact and hash: %+v %v", replayed, err)
	}
}
