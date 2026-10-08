package main

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/1420970597/llm/internal/model"
	"github.com/1420970597/llm/internal/store"
)

func init() {
	RegisterStudioJobHandler(model.JobKindSourceDocumentIngest, handleSourceIngest)
	RegisterStudioJobHandler(model.JobKindSourceProductIngest, handleSourceIngest)
}

func handleSourceIngest(ctx context.Context, env *StudioJobEnv, job model.Job) (any, error) {
	var input struct {
		ImportID int64 `json:"importId"`
	}
	if err := json.Unmarshal(job.Payload, &input); err != nil || input.ImportID <= 0 || job.ProjectID == nil {
		return nil, fmt.Errorf("config_error: 导入任务缺少 importId/projectId")
	}
	return store.NewLegacyImportStore(env.Pool).ProcessSourceImport(ctx, job, input.ImportID)
}
