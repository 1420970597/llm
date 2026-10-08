package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestSourceRoutesRequireAuthentication(t *testing.T) {
	app, handler := sharedRoutedApplication(t)
	for _, entry := range []struct{ method, path string }{{"POST", "source-imports"}, {"GET", "source-imports"}, {"GET", "source-imports/1"}, {"POST", "source-import-products"}, {"POST", "source-import-products/preview"}, {"GET", "source-chunks"}, {"GET", "source-chunks/1"}, {"GET", "source-versions"}, {"POST", "source-versions"}, {"GET", "source-versions/1"}} {
		t.Run(entry.method+entry.path, func(t *testing.T) {
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, httptest.NewRequest(entry.method, "/api/v1/projects/p_1/"+entry.path, nil))
			if w.Code != http.StatusUnauthorized {
				t.Fatalf("want401 got%d body%s", w.Code, w.Body.String())
			}
		})
	}
	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/api/v1/projects/p_1/source-chunks/1/unknown", nil)
	r.AddCookie(mustSessionCookie(t, app))
	handler.ServeHTTP(w, r)
	if w.Code != http.StatusNotFound {
		t.Fatalf("unknown resource should404 got%d", w.Code)
	}
}

func TestSourceFileTypesRejectUnsupportedAndAcceptMarkdown(t *testing.T) {
	for _, name := range []string{"guide.md", "说明.TXT", "guide.markdown"} {
		if _, ok := sourceFileKind(name); !ok {
			t.Fatal(name)
		}
	}
	for _, name := range []string{"guide.pdf", "evil.zip", "file.docx", "file"} {
		if _, ok := sourceFileKind(name); ok {
			t.Fatalf("unsupported %s accepted", name)
		}
	}
}
