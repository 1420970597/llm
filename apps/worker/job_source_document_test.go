package main

import (
	"context"
	"testing"

	"github.com/1420970597/llm/internal/model"
)

func TestSourceWorkerRegistrationAndInvalidInput(t *testing.T) {
	for _, kind := range []string{model.JobKindSourceDocumentIngest, model.JobKindSourceProductIngest} {
		handler, ok := lookupStudioJobHandler(kind)
		if !ok || !model.IsStudioJobKind(kind) {
			t.Fatal("source job is not registered")
		}
		_, err := handler(context.Background(), &StudioJobEnv{}, model.Job{Kind: kind, Payload: []byte(`{"importId":0}`)})
		if err == nil || classifyStudioJobError(err) != model.ErrorClassConfig {
			t.Fatalf("invalid job must not retry: %v", err)
		}
	}
}
