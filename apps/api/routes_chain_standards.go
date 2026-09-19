package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/1420970597/llm/internal/model"
	"github.com/1420970597/llm/internal/store"
)

// 本文件实现 L2：方向 → 长链思维标准步骤（可编辑、版本化）。
//
// 路由注册说明（重要）：
// `RegisterDatasetRouter` 的 router 签名是 (w, r, id, rest)，**拿不到 app**，
// 无法构造 store；且 main.go 只为 /api/v1/datasets/ 注册了 GET/POST 两个 catchall，
// PUT 请求根本不会进入数据集子路由。因此本 lane 统一在 RegisterRoutes（可拿到 app）
// 里注册显式 pattern，由 net/http 的 ServeMux 做最长匹配：
//   - GET  /api/v1/datasets/{id}/chain-standards                → 列表
//   - POST /api/v1/datasets/{id}/chain-standards/generate       → 入队生成
//   - PUT  /api/v1/datasets/{id}/chain-standards/{domainId}     → 编辑（版本自增）
//   - GET  /api/v1/datasets/{id}/chain-standards/{domainId}/versions → 版本历史
// 显式 pattern 优先级高于 main.go 的 "GET /api/v1/datasets/" catchall，
// 未匹配的路径仍会落回 legacy 分支，不影响既有功能。

const chainStandardJobType = "chain-standards.generate"

func init() {
	RegisterRoutes(func(mux *http.ServeMux, app *application) {
		mux.HandleFunc("GET /api/v1/datasets/{id}/chain-standards", app.listChainStandards)
		mux.HandleFunc("POST /api/v1/datasets/{id}/chain-standards/generate", app.enqueueChainStandardGeneration)
		mux.HandleFunc("PUT /api/v1/datasets/{id}/chain-standards/{domainId}", app.updateChainStandard)
		mux.HandleFunc("GET /api/v1/datasets/{id}/chain-standards/{domainId}/versions", app.listChainStandardVersions)
	})
}

func (app *application) chainStandards() *store.ChainStandardStore {
	return store.NewChainStandardStore(app.db())
}

// listChainStandards 返回数据集下全部方向的标准步骤。
func (app *application) listChainStandards(w http.ResponseWriter, r *http.Request) {
	id, err := pathInt64(r, "id")
	if err != nil {
		app.writeError(w, http.StatusBadRequest, err)
		return
	}

	items, err := app.chainStandards().GetByDataset(r.Context(), id)
	if err != nil {
		app.writeError(w, http.StatusInternalServerError, err)
		return
	}
	app.writeJSON(w, http.StatusOK, items)
}

// enqueueChainStandardGeneration 把标准步骤生成任务入队。
//
// 入队载荷只有 {type, datasetId}（enqueueJob 是冻结的共享实现），因此
// 请求体里的 domainIds 会落到 generation_runs.cursor，由 worker 读取，
// 这样既不重复实现入队逻辑，也能让「指定部分方向生成」真实生效。
func (app *application) enqueueChainStandardGeneration(w http.ResponseWriter, r *http.Request) {
	id, err := pathInt64(r, "id")
	if err != nil {
		app.writeError(w, http.StatusBadRequest, err)
		return
	}

	var input model.ChainStandardGenerateRequest
	if r.Body != nil {
		// 允许空请求体：语义为「全部方向」。
		if decodeErr := json.NewDecoder(r.Body).Decode(&input); decodeErr != nil && decodeErr.Error() != "EOF" {
			app.writeError(w, http.StatusBadRequest, decodeErr)
			return
		}
	}

	dataset, err := app.datasets.GetDataset(r.Context(), id)
	if err != nil {
		app.writeError(w, http.StatusInternalServerError, err)
		return
	}
	if dataset.ProviderID <= 0 {
		app.writeError(w, http.StatusConflict, errors.New(msgProviderUnavailable))
		return
	}

	domains, err := app.datasets.ListDomains(r.Context(), id)
	if err != nil {
		app.writeError(w, http.StatusInternalServerError, err)
		return
	}
	targets := filterDomainsByIDs(domains, input.DomainIDs)
	if len(targets) == 0 {
		app.writeError(w, http.StatusConflict, errors.New(msgNoChainStepTargets))
		return
	}

	if err := app.saveJobCursor(r.Context(), id, chainStandardJobType, len(targets), map[string]any{
		"domainIds": domainIDs(targets),
	}); err != nil {
		app.writeError(w, http.StatusInternalServerError, err)
		return
	}

	enqueued, err := app.enqueueJob(r.Context(), chainStandardJobType, id, "chain_standards_queued")
	if err != nil {
		app.writeError(w, http.StatusInternalServerError, err)
		return
	}
	if enqueued {
		app.audit(r.Context(), "enqueue", "chain_standard_generation", datasetIDString(id), chainStandardJobType)
	}
	app.writeJSON(w, http.StatusAccepted, enqueueResult(id, "chain-standards", enqueued,
		"长链思维标准步骤生成任务已入队", "长链思维标准步骤生成任务已在队列中"))
}

// updateChainStandard 保存用户编辑，current_version 自增并留下 user 版本。
func (app *application) updateChainStandard(w http.ResponseWriter, r *http.Request) {
	id, err := pathInt64(r, "id")
	if err != nil {
		app.writeError(w, http.StatusBadRequest, err)
		return
	}
	domainID, err := pathInt64(r, "domainId")
	if err != nil {
		app.writeError(w, http.StatusBadRequest, err)
		return
	}

	var input model.ChainStandardUpdateRequest
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		app.writeError(w, http.StatusBadRequest, err)
		return
	}
	if len(input.Steps) == 0 {
		app.writeError(w, http.StatusBadRequest, errors.New(msgStepsEmpty))
		return
	}

	createdBy := int64(0)
	if user, ok := requestUser(r); ok {
		createdBy = user.ID
	}

	updated, err := app.chainStandards().UpdateStepsWithVersion(r.Context(), id, domainID, input.Steps, input.ChangeNote, createdBy)
	if err != nil {
		if strings.Contains(err.Error(), "not found") {
			app.writeError(w, http.StatusNotFound, newUserFacingError(msgDatasetNotFound, err))
			return
		}
		app.writeError(w, http.StatusInternalServerError, err)
		return
	}
	app.audit(r.Context(), "update", "chain_standard", datasetIDString(id), fmt.Sprintf("domain=%d version=%d", domainID, updated.CurrentVersion))
	app.writeJSON(w, http.StatusOK, updated)
}

// listChainStandardVersions 返回某方向的全部历史版本，按版本号倒序。
func (app *application) listChainStandardVersions(w http.ResponseWriter, r *http.Request) {
	id, err := pathInt64(r, "id")
	if err != nil {
		app.writeError(w, http.StatusBadRequest, err)
		return
	}
	domainID, err := pathInt64(r, "domainId")
	if err != nil {
		app.writeError(w, http.StatusBadRequest, err)
		return
	}

	items, err := app.chainStandards().ListVersions(r.Context(), id, domainID)
	if err != nil {
		app.writeError(w, http.StatusInternalServerError, err)
		return
	}
	app.writeJSON(w, http.StatusOK, items)
}

// saveJobCursor 在入队前把本次任务的参数写入 generation_runs.cursor。
// 复用 GenerationRunStore 的 StartRun + SaveCursor，不另建队列表。
func (app *application) saveJobCursor(ctx context.Context, datasetID int64, stage string, totalUnits int, cursor map[string]any) error {
	run, err := app.generationRuns.StartRun(ctx, datasetID, stage, totalUnits)
	if err != nil {
		return err
	}
	return app.generationRuns.SaveCursor(ctx, run.ID, cursor, 0, totalUnits)
}

// filterDomainsByIDs 按 ID 过滤方向；ids 为空表示全选。
func filterDomainsByIDs(domains []model.Domain, ids []int64) []model.Domain {
	if len(ids) == 0 {
		return domains
	}
	wanted := make(map[int64]struct{}, len(ids))
	for _, id := range ids {
		wanted[id] = struct{}{}
	}
	filtered := make([]model.Domain, 0, len(ids))
	for _, domain := range domains {
		if _, ok := wanted[domain.ID]; ok {
			filtered = append(filtered, domain)
		}
	}
	return filtered
}

// domainIDs 抽取方向 ID 列表。
func domainIDs(domains []model.Domain) []int64 {
	ids := make([]int64, 0, len(domains))
	for _, domain := range domains {
		ids = append(ids, domain.ID)
	}
	return ids
}

// pathInt64 读取 ServeMux 的路径参数并转为 int64。
func pathInt64(r *http.Request, name string) (int64, error) {
	raw := r.PathValue(name)
	parsed, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || parsed <= 0 {
		return 0, fmt.Errorf("invalid %s in path: %q", name, raw)
	}
	return parsed, nil
}
