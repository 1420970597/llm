package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/1420970597/llm/internal/model"
)

// TestQualityPreviewRouteIsRegistered guards the page-facing contract.  A
// missing registration is otherwise indistinguishable from a broken preview
// implementation in the browser (both show a 404).
func TestQualityPreviewRouteIsRegistered(t *testing.T) {
	mux := http.NewServeMux()
	registerQualityRoutes(mux, &application{})

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost,
		"/api/v1/projects/p_42/rule-previews", nil)
	mux.ServeHTTP(recorder, request)
	if recorder.Code == http.StatusNotFound {
		t.Fatalf("rule preview route must be registered; got 404")
	}
	// Authentication is checked before touching the store.  A bare request
	// therefore must be rejected with 401, not panic or report a false 200.
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated preview request should be 401, got %d", recorder.Code)
	}
}

func TestReleaseSampleVersionLinkUsesSampleIdentityAndLocalVersion(t *testing.T) {
	link := releaseSampleVersionLink(42, model.SampleVersion{
		SampleID:  9001,
		Version:   7,
		CreatedAt: time.Now(),
	})
	want := "/p/42/data/s_9001?version=7"
	if link != want {
		t.Fatalf("release blocker link = %q, want %q", link, want)
	}
}
