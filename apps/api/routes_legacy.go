package main

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/1420970597/llm/internal/store"
)

// 本文件是旧路由兼容与旧写入口冻结（Issue #160 T31）。
//
// 契约：#160 T31 的原文要求：
//
//   * 「旧 `/console/tasks/:id` 经映射跳转；无法确定对象的阶段入口保留只读
//     历史列表，不跳错项目」；
//   * 「冻结后旧 API 仅历史读，写返回迁移说明/新项目链接，不暗中修改新项目」。
//
// 两个机制，互相独立：
//
//  1. **映射查询**（`GET /api/v1/legacy/datasets/{id}/project`）：按
//     `projects.legacy_dataset_id` 反查。查不到时返回 404 + 「尚无映射」，
//     而不是猜一个项目 —— 猜错会把用户带到一个内容完全无关的项目里，
//     而那种错误看起来像「数据丢了」。
//  2. **旧写入口冻结**（`LEGACY_WRITES_FROZEN=true`）：所有旧 dataset 中心的
//     写请求在**中间件层**被拒绝，返回 409 + 迁移说明。
//     放在中间件而不是逐个 handler：旧端点数以十计，逐个加检查必然漏一个，
//     而漏掉的那个会在迁移期间继续写旧库（于是新旧两套数据同时被写）。
//
// 与 `STUDIO_ENABLED` 的分工：那个开关管新 Studio 命令，这个开关管旧入口。
// 迁移期间通常是「旧入口冻结 + 新入口开放」。

// legacyWritePrefixes 是旧 dataset 中心入口的写路径前缀。
//
// 刻意**不包含** `/api/v1/admin/`（治理面，迁移期间运维仍要改 provider/存储）
// 与 `/api/v1/projects`、`/api/v1/studio`（新主线，由 STUDIO_ENABLED 管）。
var legacyWritePrefixes = []string{
	"/api/v1/datasets",
	"/api/v1/domains",
	"/api/v1/questions",
	"/api/v1/chain-standards",
	"/api/v1/reasoning",
	"/api/v1/rewards",
	"/api/v1/grpo",
	"/api/v1/sft",
	"/api/v1/export",
	"/api/v1/eval",
	"/api/v1/cleaning",
}

// isLegacyWriteRequest 判断一个请求是否是「旧入口的写操作」。
func isLegacyWriteRequest(method, path string) bool {
	switch method {
	case http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
	default:
		return false
	}
	for _, prefix := range legacyWritePrefixes {
		if path == prefix || strings.HasPrefix(path, prefix+"/") {
			return true
		}
	}
	return false
}

// msgLegacyWriteFrozen 是冻结时返回给用户的说明。
//
// 它的三个要素缺一不可：① 说明发生了什么（不是故障，是迁移）；② 指向新入口；
// ③ 给出「我原来的数据对应哪个项目」的查询方式。只说「已禁用」会让用户
// 以为系统坏了并去找运维，而那是本可以避免的人工成本。
const msgLegacyWriteFrozen = "旧入口已冻结（数据迁移进行中）：新的写操作请在「数据项目」工作区里进行。" +
	"旧资产与项目的对应关系可用 GET /api/v1/legacy/datasets/{datasetId}/project 查询；" +
	"已发布的内容仍可下载。"

func init() {
	RegisterRoutes(registerLegacyRoutes)
}

func registerLegacyRoutes(mux *http.ServeMux, app *application) {
	mux.HandleFunc("GET /api/v1/legacy/datasets/{datasetId}/project", app.getLegacyDatasetProject)
}

// legacyProjectMapping 是旧 dataset → 新项目的映射响应。
type legacyProjectMapping struct {
	DatasetID int64 `json:"datasetId"`
	// ProjectID 为 0 表示尚无映射（前端据此显示只读历史说明而不是跳转）。
	ProjectID int64 `json:"projectId"`
	// PagePath 是前端可直接跳转的路径（前端不自行拼 URL）。
	PagePath string `json:"pagePath,omitempty"`
	// MigrationStatus 让用户知道「还要不要等」。
	MigrationStatus string `json:"migrationStatus"`
	Message         string `json:"message"`
}

// getLegacyDatasetProject 返回旧 dataset 对应的新项目。
func (app *application) getLegacyDatasetProject(w http.ResponseWriter, r *http.Request) {
	if _, ok := requestUser(r); !ok {
		app.writeAPIError(w, r, http.StatusUnauthorized, codeUnauthorized, msgAuthRequired, nil)
		return
	}
	datasetID, err := strconv.ParseInt(r.PathValue("datasetId"), 10, 64)
	if err != nil || datasetID <= 0 {
		app.writeAPIError(w, r, http.StatusNotFound, codeNotFound, "未找到该历史任务", nil)
		return
	}

	imports := store.NewLegacyImportStore(app.studio.Pool)
	projectID, err := imports.FindProjectByLegacyDataset(r.Context(), datasetID)
	if err != nil {
		app.writeAPIError(w, r, http.StatusInternalServerError, "INTERNAL_ERROR", "查询映射失败，请稍后重试", nil)
		return
	}
	if projectID == 0 {
		// 「尚无映射」不是 404 错误页：前端需要据此渲染只读历史列表
		//（T31：「无法确定对象的阶段入口保留只读历史列表，不跳错项目」）。
		app.writeJSON(w, http.StatusOK, legacyProjectMapping{
			DatasetID:       datasetID,
			MigrationStatus: "not_mapped",
			Message: "该历史任务尚未映射到新项目或仍在迁移中。它只能作为只读历史查看，" +
				"不能在旧入口继续写入；迁移完成后这里会给出对应的项目地址。",
		})
		return
	}

	app.writeJSON(w, http.StatusOK, legacyProjectMapping{
		DatasetID:       datasetID,
		ProjectID:       projectID,
		PagePath:        "/p/" + strconv.FormatInt(projectID, 10) + "/overview",
		MigrationStatus: "mapped",
		Message:         "该历史任务已映射到新项目；请在项目工作区继续操作。",
	})
}

// legacyWritesFrozenError 是中间件用的错误值（保持与旧错误形状一致）。
func legacyWritesFrozenError() error {
	return errors.New(msgLegacyWriteFrozen)
}
