package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/1420970597/llm/internal/exporter"
	"github.com/1420970597/llm/internal/model"
	"github.com/1420970597/llm/internal/store"
)

// 本文件由 L6 lane 独占。通过路由注册表接入，不修改 main.go / routes.go / exports.go。

// exportApp 缓存 application 供数据集子路由使用。
//
// RegisterDatasetRouter 的 router 签名是 (w, r, id, rest)，拿不到 app，
// 而它必须在 init() 里调用（此时 app 还不存在）。因此先用 RegisterRoutes
// 捕获 app，再由 routeExport 读取。applyRouteRegistrars 在 main() 里、
// ListenAndServe 之前执行，所以请求到达时必然已就绪。
var exportApp *application

func init() {
	// 接管 /api/v1/datasets/{id}/export/... 子路径。
	//
	// ⚠️ 注意：main.go 的 routeDatasetGet / routeDatasetActions 会**优先**调用
	// tryDatasetRouter，因此注册本段后 legacy 分支里的 /export、/export/download
	// 不再会被命中。为了不回退既有功能，下面 routeExport 完整复刻了
	// apps/api/exports.go 中 listArtifacts / downloadArtifact / enqueueExport 的行为。
	RegisterDatasetRouter("export", routeExport)

	RegisterRoutes(func(mux *http.ServeMux, app *application) {
		exportApp = app
		mux.HandleFunc("GET /api/v1/admin/export-mappings", app.listExportMappings)
		mux.HandleFunc("POST /api/v1/admin/export-mappings", app.upsertExportMapping)
		mux.HandleFunc("PUT /api/v1/admin/export-mappings", app.upsertExportMapping)
	})
}

// routeExport 分发数据集导出子路径。
//
//	rest == ""                → GET 列出现有导出产物；POST 入队导出
//	rest == "/download"       → GET 下载指定产物
//	rest == "/formats"        → GET 支持的格式与字段映射清单
func routeExport(w http.ResponseWriter, r *http.Request, id int64, rest string) {
	app := exportApp
	if app == nil {
		http.Error(w, "application not ready", http.StatusInternalServerError)
		return
	}
	switch {
	case rest == "/formats" && r.Method == http.MethodGet:
		app.exportFormats(w, r, id)
	case rest == "/download" && r.Method == http.MethodGet:
		// 复用 legacy 实现，行为与既有下载完全一致。
		app.downloadArtifact(w, r)
	case rest == "" && r.Method == http.MethodGet:
		app.listArtifacts(w, r)
	case rest == "" && r.Method == http.MethodPost:
		app.enqueueMultiFormatExport(w, r, id)
	default:
		http.NotFound(w, r)
	}
}

// exportFormats 返回支持的格式与可用字段映射。
//
// 内置映射在首次访问时幂等补种，保证全新环境打开界面就能看到可用映射，
// 不需要先手动调 seed 接口。
func (app *application) exportFormats(w http.ResponseWriter, r *http.Request, id int64) {
	// 数据集不存在时明确返回 404，而不是给出格式清单让调用方误以为可用。
	if _, err := app.datasets.GetDataset(r.Context(), id); err != nil {
		app.writeError(w, http.StatusNotFound, err)
		return
	}

	mappingStore := store.NewExportMappingStore(app.db())
	mappings, err := mappingStore.EnsureSeeded(r.Context())
	if err != nil {
		app.writeError(w, http.StatusInternalServerError, err)
		return
	}

	app.writeJSON(w, http.StatusOK, model.ExportFormatList{
		Formats:  exporter.Formats(),
		Mappings: mappings,
	})
}

// enqueueMultiFormatExport 校验请求并入队一次导出。
//
// 未指定 format（包括旧调用方直接 POST 空 body 的情况）时，整体委派给
// legacy 的 app.enqueueExport：它带着奖励完整性校验、且 worker 侧会回退到
// legacy 的 handleExportGeneration，因此旧调用方的行为与字段集完全不变。
// 不能把空 format 当成 "jsonl"：那会让旧调用方的导出字段被字段映射改写。
func (app *application) enqueueMultiFormatExport(w http.ResponseWriter, r *http.Request, id int64) {
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		app.writeError(w, http.StatusBadRequest, err)
		return
	}
	trimmed := bytes.TrimSpace(raw)

	var input model.ExportRequest
	if len(trimmed) == 0 || json.Unmarshal(trimmed, &input) != nil || input.Format == "" {
		// 还原 body（enqueueExport 虽不读，但保持请求对象完整，避免后续中间件读到空流）。
		r.Body = io.NopCloser(bytes.NewReader(trimmed))
		app.enqueueExport(w, r)
		return
	}

	if _, ok := exporter.Get(input.Format); !ok {
		app.writeError(w, http.StatusBadRequest,
			fmt.Errorf("不支持的导出格式 %q，可用格式：%s", input.Format, strings.Join(exporter.Formats(), ", ")))
		return
	}

	if _, err := app.datasets.GetDataset(r.Context(), id); err != nil {
		app.writeError(w, http.StatusNotFound, err)
		return
	}

	// 指定了映射时先校验存在，避免任务入队后才发现映射 ID 无效。
	mappingStore := store.NewExportMappingStore(app.db())
	if input.MappingID > 0 {
		if _, err := mappingStore.Get(r.Context(), input.MappingID); err != nil {
			app.writeError(w, http.StatusBadRequest, fmt.Errorf("字段映射 %d 不存在", input.MappingID))
			return
		}
	}

	// 先写请求再入队：反序的话 worker 可能在请求落盘前就出队并取不到格式，
	// 静默退化成 legacy 导出。
	request := exporter.Request{
		Format:    input.Format,
		MappingID: input.MappingID,
		Filters:   input.Filters,
	}
	if request.Filters == nil {
		request.Filters = map[string]any{}
	}
	payload, err := json.Marshal(request)
	if err != nil {
		app.writeError(w, http.StatusInternalServerError, err)
		return
	}
	if err := app.redis.Set(r.Context(), exporter.RequestKey(id), payload, exporter.RequestTTL).Err(); err != nil {
		app.writeError(w, http.StatusInternalServerError, err)
		return
	}

	enqueued, err := app.enqueueJob(r.Context(), "export.generate", id, "export_queued")
	if err != nil {
		app.writeError(w, http.StatusInternalServerError, err)
		return
	}
	if enqueued {
		app.audit(r.Context(), "enqueue", "dataset_export", datasetIDString(id),
			"export.generate format="+input.Format)
	}

	app.writeJSON(w, http.StatusAccepted, model.StageEnqueueResult{
		DatasetID:  id,
		Stage:      "export",
		State:      "queued",
		Message:    queuedMessage(enqueued, "导出任务已入队", "导出任务已在队列中"),
		AcceptedAt: time.Now().Format(time.RFC3339),
	})
}

// listExportMappings 返回全部导出字段映射。
func (app *application) listExportMappings(w http.ResponseWriter, r *http.Request) {
	mappingStore := store.NewExportMappingStore(app.db())
	items, err := mappingStore.EnsureSeeded(r.Context())
	if err != nil {
		app.writeError(w, http.StatusInternalServerError, err)
		return
	}
	app.writeJSON(w, http.StatusOK, items)
}

// upsertExportMapping 新增或更新一份导出字段映射。
func (app *application) upsertExportMapping(w http.ResponseWriter, r *http.Request) {
	var input model.ExportMapping
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		app.writeError(w, http.StatusBadRequest, err)
		return
	}

	mappingStore := store.NewExportMappingStore(app.db())
	item, err := mappingStore.Upsert(r.Context(), input)
	if err != nil {
		app.writeError(w, http.StatusBadRequest, err)
		return
	}
	app.audit(r.Context(), "upsert", "export_mapping", datasetIDString(item.ID), item.Name)
	app.writeJSON(w, http.StatusOK, item)
}
