package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/1420970597/llm/internal/model"
	"github.com/1420970597/llm/internal/store"
)

func TestDocumentVersionHTTPPaginationAndInvalidCursor(t *testing.T) {
	f, projectID := sourceIntegrationProject(t)
	var revision int64
	for index := 0; index < 3; index++ {
		doc, _, err := f.app.documents.SaveVersion(context.Background(), projectID, model.KindStandard, f.actor,
			store.SaveDocumentVersionInput{ExpectedRevision: revision, Payload: model.StandardPayload{
				SchemaVersion: "standard.v1", Steps: []model.StandardStep{{ID: "step", Title: "推理", Detail: "逐步分析", Checkpoint: "依据明确", Order: index}},
			}})
		if err != nil {
			t.Fatal(err)
		}
		revision = doc.RowVersion
	}
	read := func(query string, actor int64) *httptest.ResponseRecorder {
		t.Helper()
		request := sourceAuthRequest(httptest.NewRequest(http.MethodGet, "/api/v1/projects/1/standard-versions?"+query, nil), projectID, actor)
		response := httptest.NewRecorder()
		f.app.listDocumentVersions(model.KindStandard)(response, request)
		return response
	}
	var page struct {
		Items      []store.DocumentVersion `json:"items"`
		NextCursor string                  `json:"nextCursor"`
		SortKey    string                  `json:"sortKey"`
	}
	first := read("limit=2", f.actor)
	if first.Code != 200 || json.Unmarshal(first.Body.Bytes(), &page) != nil || len(page.Items) != 2 || page.Items[0].Version != 3 || page.NextCursor != "2" || page.SortKey != "version:desc" {
		t.Fatalf("第一页必须返回真实游标: %d %s", first.Code, first.Body.String())
	}
	second := read("limit=2&cursor="+page.NextCursor, f.actor)
	if second.Code != 200 || json.Unmarshal(second.Body.Bytes(), &page) != nil || len(page.Items) != 1 || page.Items[0].Version != 1 || page.NextCursor != "" {
		t.Fatalf("第二页不得重复且正确到底: %d %s", second.Code, second.Body.String())
	}
	for _, cursor := range []string{"nope", "0", "-1", "999999999999999999999999"} {
		if response := read("cursor="+cursor, f.actor); response.Code != 400 {
			t.Fatalf("非法游标 %s: %d %s", cursor, response.Code, response.Body.String())
		}
	}
	if response := read("limit=2", f.secondOwner); response.Code != 404 {
		t.Fatalf("分页不能暴露其他项目: %d %s", response.Code, response.Body.String())
	}
}
