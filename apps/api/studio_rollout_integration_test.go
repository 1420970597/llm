package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"

	appcrypto "github.com/1420970597/llm/internal/crypto"
	"github.com/1420970597/llm/internal/model"
	"github.com/1420970597/llm/internal/storage"
	"github.com/1420970597/llm/internal/store"
	"github.com/1420970597/llm/internal/studio"
)

// TestStudioRollbackStopsLegacyProjectWritesAndKeepsReadsAvailable proves the
// rollback gate at the shared project authorization boundary.  Source import
// is intentionally used here because it is a legacy project route that used to
// call AuthzStore directly and could therefore bypass the Studio rollout gate.
// The fixture uses real Postgres and the source handler's normal HTTP path; no
// fake authorization or in-memory ledger is involved.
func TestStudioRollbackStopsLegacyProjectWritesAndKeepsReadsAvailable(t *testing.T) {
	fixture, projectID := sourceIntegrationProject(t)
	fixture.app.studio.Rollout = studio.Rollout{
		DisabledGlobally: true,
	}

	request := sourceMultipartRequest(t, projectID, fixture.actor, "rollback.md", "# queued\nshould not be written", 0)
	recorder := httptest.NewRecorder()
	fixture.app.uploadSourceDocument(recorder, request)
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("rollback must reject a new source command with 503, got %d: %s", recorder.Code, recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), "STUDIO_ENABLED") {
		t.Fatalf("rollback response must explain the operator switch: %s", recorder.Body.String())
	}

	var imports int
	if err := fixture.pool.QueryRow(context.Background(),
		`SELECT COUNT(*) FROM legacy_imports WHERE target_project_id = $1`, projectID).Scan(&imports); err != nil {
		t.Fatalf("count source ledger rows: %v", err)
	}
	if imports != 0 {
		t.Fatalf("rejected source command must not write a ledger row, got %d", imports)
	}
	// Project-scoped rollback must use the same command boundary, and a
	// nonmember must still receive a hidden 404 rather than rollout details.
	fixture.app.studio.Rollout = studio.Rollout{DisabledProjects: map[int64]string{projectID: "controlled acceptance rollback"}}
	recorder = httptest.NewRecorder()
	fixture.app.uploadSourceDocument(recorder, sourceMultipartRequest(t, projectID, fixture.actor, "rollback.md", "source", 0))
	if recorder.Code != http.StatusServiceUnavailable || !strings.Contains(recorder.Body.String(), "STUDIO_DISABLED_PROJECT_IDS") {
		t.Fatalf("per-project gate must reject source write: %d %s", recorder.Code, recorder.Body.String())
	}
	recorder = httptest.NewRecorder()
	fixture.app.uploadSourceDocument(recorder, sourceMultipartRequest(t, projectID, fixture.secondOwner, "rollback.md", "source", 0))
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("nonmember must not observe rollback status: %d %s", recorder.Code, recorder.Body.String())
	}
	fixture.app.studio.Rollout = studio.Rollout{DisabledGlobally: true}

	// Existing project reads remain available while writes are paused.
	read := httptest.NewRequest(http.MethodGet, "/api/v1/projects/p_1", nil)
	read.SetPathValue("projectId", "p_"+strconv.FormatInt(projectID, 10))
	read = read.WithContext(context.WithValue(read.Context(), userContextKey,
		model.User{ID: fixture.actor, Role: "user"}))
	recorder = httptest.NewRecorder()
	fixture.app.getProject(recorder, read)
	if recorder.Code != http.StatusOK {
		t.Fatalf("rollback must preserve project reads, got %d: %s", recorder.Code, recorder.Body.String())
	}
}

// TestStudioFaultRollbackKeepsPublishedDownloadAndRejectsCorruption checks the
// real object-storage HTTP path after rollback. SQL only seeds the published
// fixture; the object bytes, locator, metadata, download and hash validation use
// the production storage and artifact APIs.
func TestStudioFaultRollbackKeepsPublishedDownloadAndRejectsCorruption(t *testing.T) {
	endpoint := os.Getenv("LLM_TEST_S3_ENDPOINT")
	if endpoint == "" {
		t.Skip("real MinIO missing; run scripts/go-test-studio-failures.sh")
	}
	f, projectID := sourceIntegrationProject(t)
	ctx := context.Background()
	box, err := appcrypto.NewSecretBox("phase1-dev-only-32-byte-secret!!!")
	if err != nil {
		t.Fatal(err)
	}
	profile, err := store.NewAdminStore(f.pool, box).UpsertStorageProfile(ctx, model.StorageProfile{
		Name: "rollback-download", Provider: "minio", Endpoint: endpoint,
		Region: "us-east-1", Bucket: "studio-fault-download",
		AccessKeyID: os.Getenv("LLM_TEST_S3_ACCESS_KEY"), SecretAccessKey: os.Getenv("LLM_TEST_S3_SECRET_KEY"),
		UsePathStyle: true, IsActive: true, IsDefault: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = f.pool.Exec(ctx, `DELETE FROM storage_profiles WHERE id=$1`, profile.ID) })
	f.app.datasets = store.NewDatasetStore(f.pool, box)
	f.app.studio.Rollout = studio.Rollout{DisabledGlobally: true}
	var releaseID int64
	if err := f.pool.QueryRow(ctx, `INSERT INTO releases(project_id,release_name,release_name_key,status,intended_use,published_at) VALUES($1,'download-v1','download-v1','published','fixture',NOW()) RETURNING id`, projectID).Scan(&releaseID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(ctx, `INSERT INTO release_candidates(release_id,project_id,revision,format,frozen_at) VALUES($1,$2,1,'jsonl',NOW())`, releaseID, projectID); err != nil {
		t.Fatal(err)
	}
	content := []byte("{\"question\":\"frozen question\"}\n")
	hash := model.ComputeArtifactHash(content)
	key := model.ArtifactObjectKey(releaseID, 1, "jsonl", hash)
	objects, err := storage.New(storage.Profile{Endpoint: endpoint, Region: "us-east-1", Bucket: profile.Bucket, AccessKeyID: os.Getenv("LLM_TEST_S3_ACCESS_KEY"), SecretKey: os.Getenv("LLM_TEST_S3_SECRET_KEY"), UsePathStyle: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := objects.PutBytes(ctx, key, content, "application/jsonl"); err != nil {
		t.Fatal(err)
	}
	registered, err := f.app.studio.ReleaseArtifacts.RegisterArtifact(ctx, store.ArtifactUpload{
		ReleaseID: releaseID, Revision: 1, ArtifactType: "export", Format: "jsonl", ObjectKey: key,
		StorageEndpoint: endpoint, StorageBucket: profile.Bucket, SizeBytes: int64(len(content)), ContentType: "application/jsonl",
		ArtifactHash: hash, ItemsContentHash: hash, EncoderVersion: "exporter-v1", UploadedBy: &f.actor,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := f.app.studio.ReleaseArtifacts.MarkArtifactVerified(ctx, registered.Artifact.ID); err != nil {
		t.Fatal(err)
	}
	request := func(actor int64) *http.Request {
		r := sourceAuthRequest(httptest.NewRequest(http.MethodGet, "/api/v1/projects/1/releases/1/artifacts/1/download", nil), projectID, actor)
		r.SetPathValue("releaseId", fmt.Sprint(releaseID))
		r.SetPathValue("artifactId", fmt.Sprint(registered.Artifact.ID))
		return r
	}
	w := httptest.NewRecorder()
	f.app.downloadReleaseArtifact(w, request(f.actor))
	if w.Code != http.StatusOK || w.Header().Get("X-Artifact-Hash") != hash || model.ComputeArtifactHash(w.Body.Bytes()) != hash {
		t.Fatalf("rollback must preserve fixed published download and hash: %d %s", w.Code, w.Body.String())
	}
	w = httptest.NewRecorder()
	f.app.downloadReleaseArtifact(w, request(f.secondOwner))
	if w.Code != http.StatusNotFound {
		t.Fatalf("nonmember download must be hidden: %d %s", w.Code, w.Body.String())
	}
	if _, err := objects.PutBytes(ctx, key, []byte("corrupted bytes"), "application/jsonl"); err != nil {
		t.Fatal(err)
	}
	w = httptest.NewRecorder()
	f.app.downloadReleaseArtifact(w, request(f.actor))
	if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "hash") {
		t.Fatalf("corrupted published object must be rejected: %d %s", w.Code, w.Body.String())
	}
	if _, err := f.pool.Exec(ctx, `UPDATE release_artifacts SET object_key='missing-object' WHERE id=$1`, registered.Artifact.ID); err != nil {
		t.Fatal(err)
	}
	w = httptest.NewRecorder()
	f.app.downloadReleaseArtifact(w, request(f.actor))
	if w.Code != http.StatusNotFound {
		t.Fatalf("missing object must not regenerate latest: %d %s", w.Code, w.Body.String())
	}
}
