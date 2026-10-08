package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	importer "github.com/1420970597/llm/internal/import"
	"github.com/1420970597/llm/internal/model"
	"github.com/1420970597/llm/internal/store"
)

func sourceIntegrationProject(t *testing.T) (*workspaceAuthzIntegrationFixture, int64) {
	t.Helper()
	f := newWorkspaceAuthzIntegrationFixture(t)
	f.app.store = store.NewAdminStore(f.pool, nil)
	one := 1
	input := model.CreateProjectInput{Name: "素材 API 测试", Goal: "来源接地", TargetKind: "sft", PilotSize: 1}
	input.Coverage.Domains, input.Coverage.DirectionsPerDomain, input.Coverage.QuestionsPerDirection = &one, &one, &one
	p, err := f.app.projects.CreateProject(context.Background(), f.workspaceA, f.actor, input)
	if err != nil {
		t.Fatal(err)
	}
	return f, p.ID
}

func sourceAuthRequest(request *http.Request, projectID, actor int64) *http.Request {
	request.SetPathValue("projectId", fmt.Sprint(projectID))
	return request.WithContext(context.WithValue(request.Context(), userContextKey, model.User{ID: actor, Role: "user"}))
}

func sourceMultipartRequest(t *testing.T, projectID, actor int64, fileName, content string, revision int64) *http.Request {
	t.Helper()
	var body bytes.Buffer
	form := multipart.NewWriter(&body)
	file, err := form.CreateFormFile("file", fileName)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.Write([]byte(content)); err != nil {
		t.Fatal(err)
	}
	if err := form.WriteField("changeReason", ""); err != nil {
		t.Fatal(err)
	}
	if err := form.WriteField("expectedRevision", fmt.Sprint(revision)); err != nil {
		t.Fatal(err)
	}
	if err := form.Close(); err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(http.MethodPost, "/api/v1/projects/1/source-imports", &body)
	r.Header.Set("Content-Type", form.FormDataContentType())
	return sourceAuthRequest(r, projectID, actor)
}

func TestSourceUploadHTTPBoundariesAndDurableIngestion(t *testing.T) {
	f, projectID := sourceIntegrationProject(t)
	ctx := context.Background()
	if err := f.app.authz.UpsertProjectMember(ctx, projectID, f.actor, f.other, model.ProjectRoleViewer, "只读用户", "source-test"); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name         string
		actor        int64
		filename     string
		declaredSize int64
		want         int
	}{
		{"viewer forbidden", f.other, "guide.md", 0, 403},
		{"nonmember hidden", f.secondOwner, "guide.md", 0, 404},
		{"unsupported PDF", f.actor, "guide.pdf", 0, 415},
		{"unsupported DOCX", f.actor, "guide.docx", 0, 415},
		{"oversize", f.actor, "guide.md", importer.MaxSourceUploadBytes + (2 << 20), 413},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := sourceMultipartRequest(t, projectID, tc.actor, tc.filename, "# guide\n公开原文", 0)
			if tc.declaredSize > 0 {
				r.ContentLength = tc.declaredSize
			}
			w := httptest.NewRecorder()
			f.app.uploadSourceDocument(w, r)
			if w.Code != tc.want {
				t.Fatalf("want %d got %d: %s", tc.want, w.Code, w.Body.String())
			}
		})
	}
	w := httptest.NewRecorder()
	f.app.uploadSourceDocument(w, sourceMultipartRequest(t, projectID, f.actor, "guide.md", "# guide\n公开原文", 0))
	if w.Code != http.StatusAccepted {
		t.Fatalf("upload %d: %s", w.Code, w.Body.String())
	}
	var result struct {
		ImportID int64 `json:"importId"`
		JobID    int64 `json:"jobId"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil || result.ImportID == 0 || result.JobID == 0 {
		t.Fatalf("missing durable IDs %s %v", w.Body.String(), err)
	}
	pendingSource, err := f.app.documents.GetDocument(ctx, projectID, model.KindSource, "")
	if err != nil || pendingSource.Current == nil || pendingSource.Current.ChangeReason != "上传素材 guide.md" {
		t.Fatalf("missing automatic reason: %+v %v", pendingSource.Current, err)
	}
	jobs := store.NewJobStore(f.pool)
	job, claimed, err := jobs.ClaimJobByID(ctx, result.JobID, "api-source-test", time.Minute)
	if err != nil || !claimed {
		t.Fatalf("claim %v %v", claimed, err)
	}
	row, err := store.NewLegacyImportStore(f.pool).ProcessSourceImport(ctx, job, result.ImportID)
	if err != nil || row.Status != "completed" {
		t.Fatalf("ingestion %+v %v", row, err)
	}
	overview, err := f.app.studio.LoadProjectOverview(ctx, projectID)
	if err != nil || overview.Versions.Source == nil || overview.Versions.Source.Version != 2 {
		t.Fatalf("overview missing source snapshot: %+v %v", overview.Versions.Source, err)
	}
	w = httptest.NewRecorder()
	f.app.uploadSourceDocument(w, sourceMultipartRequest(t, projectID, f.actor, "new.md", "new content", 0))
	if w.Code != http.StatusConflict {
		t.Fatalf("stale revision should409 got%d %s", w.Code, w.Body.String())
	}
	r := sourceAuthRequest(httptest.NewRequest(http.MethodGet, "/api/v1/projects/1/source-chunks", nil), projectID, f.other)
	w = httptest.NewRecorder()
	f.app.listSourceChunks(w, r)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "公开原文") {
		t.Fatalf("read chunks %d %s", w.Code, w.Body.String())
	}
	r = sourceAuthRequest(httptest.NewRequest(http.MethodGet, "/api/v1/projects/1/source-imports/1", nil), projectID, f.actor)
	r.SetPathValue("importId", fmt.Sprint(result.ImportID))
	w = httptest.NewRecorder()
	f.app.getSourceImport(w, r)
	if w.Code != http.StatusOK || strings.Contains(w.Body.String(), "sourceContent") {
		t.Fatalf("ledger leaks raw bytes %d %s", w.Code, w.Body.String())
	}
	r.SetPathValue("importId", "999999999999")
	w = httptest.NewRecorder()
	f.app.getSourceImport(w, r)
	if w.Code != http.StatusNotFound {
		t.Fatalf("missing ledger got%d", w.Code)
	}
}

func TestSourceProductsPreviewAndImportHTTP(t *testing.T) {
	f, projectID := sourceIntegrationProject(t)
	input := sourceProductRequest{Format: "alpaca", SourceKey: "easy-dataset/public-v1", Content: "{\"instruction\":\"问题\",\"output\":\"答案\"}\n{\"instruction\":\"缺答案\"}", TargetKind: "sft"}
	body, _ := json.Marshal(input)
	request := func(raw []byte) *http.Request {
		return sourceAuthRequest(httptest.NewRequest(http.MethodPost, "/api/v1/projects/1/source-import-products", bytes.NewReader(raw)), projectID, f.actor)
	}
	w := httptest.NewRecorder()
	f.app.previewSourceProducts(w, request(body))
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"failedItems":1`) {
		t.Fatalf("preview %d %s", w.Code, w.Body.String())
	}
	var count int
	if err := f.pool.QueryRow(context.Background(), `SELECT COUNT(*) FROM sample_versions WHERE project_id=$1`, projectID).Scan(&count); err != nil || count != 0 {
		t.Fatalf("preview wrote sample %d %v", count, err)
	}
	w = httptest.NewRecorder()
	f.app.importSourceProducts(w, request(body))
	if w.Code != http.StatusAccepted {
		t.Fatalf("import %d %s", w.Code, w.Body.String())
	}
	w = httptest.NewRecorder()
	f.app.importSourceProducts(w, request(body))
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"replay":true`) {
		t.Fatalf("replay %d %s", w.Code, w.Body.String())
	}
	input.Content = "{\"instruction\":\"changed\",\"output\":\"changed\"}"
	changed, _ := json.Marshal(input)
	w = httptest.NewRecorder()
	f.app.importSourceProducts(w, request(changed))
	if w.Code != http.StatusConflict {
		t.Fatalf("changed key should409 got%d %s", w.Code, w.Body.String())
	}
	w = httptest.NewRecorder()
	f.app.previewSourceProducts(w, request(append(body, []byte(" {}")...)))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("trailing JSON got%d", w.Code)
	}
}
