package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/1420970597/llm/internal/eval"
	"github.com/1420970597/llm/internal/model"
	"github.com/1420970597/llm/internal/store"
	"github.com/jackc/pgx/v5"
)

// 本文件实现 L8：评估维度（内置 ≥50 个长链思考维度 + 用户自定义）。
//
// 路由注册说明：/api/v1/eval/ 是本 lane 新占用的顶层前缀，与 main.go 内置路由
// 及 legacy 数据集子路由均不冲突，因此直接在 RegisterRoutes 里注册显式 pattern。
//
//	GET    /api/v1/eval/dimensions            列表（?category=&builtin=）
//	POST   /api/v1/eval/dimensions            新增/更新（按 key upsert）
//	PUT    /api/v1/eval/dimensions            按 ID 更新
//	DELETE /api/v1/eval/dimensions/{id}       删除（内置维度拒绝）
//	POST   /api/v1/eval/dimensions/seed       幂等写入内置维度目录
//	GET    /api/v1/eval/dimensions/categories 分类列表
func init() {
	RegisterRoutes(func(mux *http.ServeMux, app *application) {
		mux.HandleFunc("GET /api/v1/eval/dimensions", app.listEvalDimensions)
		mux.HandleFunc("POST /api/v1/eval/dimensions", app.upsertEvalDimension)
		mux.HandleFunc("PUT /api/v1/eval/dimensions", app.updateEvalDimension)
		mux.HandleFunc("DELETE /api/v1/eval/dimensions/{id}", app.deleteEvalDimension)
		mux.HandleFunc("POST /api/v1/eval/dimensions/seed", app.seedEvalDimensions)
		mux.HandleFunc("GET /api/v1/eval/dimensions/categories", app.listEvalDimensionCategories)
	})
}

func (app *application) evalDimensions() *store.EvalDimensionStore {
	return store.NewEvalDimensionStore(app.db())
}

// listEvalDimensions 返回维度列表。?category= 按分类过滤，?builtin=true|false 按来源过滤。
func (app *application) listEvalDimensions(w http.ResponseWriter, r *http.Request) {
	category := strings.TrimSpace(r.URL.Query().Get("category"))

	builtin, err := parseOptionalBool(r.URL.Query().Get("builtin"))
	if err != nil {
		app.writeError(w, http.StatusBadRequest, err)
		return
	}

	items, err := app.evalDimensions().List(r.Context(), category, builtin)
	if err != nil {
		app.writeError(w, http.StatusInternalServerError, err)
		return
	}
	app.writeJSON(w, http.StatusOK, items)
}

// parseOptionalBool 解析可选的布尔查询参数；空串返回 nil（表示不限制）。
func parseOptionalBool(raw string) (*bool, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return nil, nil
	}
	parsed, err := strconv.ParseBool(trimmed)
	if err != nil {
		return nil, errors.New("builtin 参数只能是 true 或 false")
	}
	return &parsed, nil
}

// upsertEvalDimension 新增或按 key 更新维度（用户自定义维度的主入口）。
func (app *application) upsertEvalDimension(w http.ResponseWriter, r *http.Request) {
	input, err := decodeEvalDimension(r)
	if err != nil {
		app.writeError(w, http.StatusBadRequest, err)
		return
	}
	input.ID = 0 // POST 语义：按 key upsert，不接受客户端指定主键。

	item, err := app.evalDimensions().Upsert(r.Context(), input)
	if err != nil {
		app.writeError(w, http.StatusBadRequest, err)
		return
	}
	app.audit(r.Context(), "upsert", "eval_dimension", strconv.FormatInt(item.ID, 10), item.Key)
	app.writeJSON(w, http.StatusOK, item)
}

// updateEvalDimension 按 ID 更新维度。缺少 id 时返回 400。
func (app *application) updateEvalDimension(w http.ResponseWriter, r *http.Request) {
	input, err := decodeEvalDimension(r)
	if err != nil {
		app.writeError(w, http.StatusBadRequest, err)
		return
	}
	if input.ID <= 0 {
		app.writeError(w, http.StatusBadRequest, errors.New("更新维度时必须提供 id"))
		return
	}

	item, err := app.evalDimensions().Upsert(r.Context(), input)
	if err != nil {
		app.writeError(w, http.StatusBadRequest, err)
		return
	}
	app.audit(r.Context(), "upsert", "eval_dimension", strconv.FormatInt(item.ID, 10), item.Key)
	app.writeJSON(w, http.StatusOK, item)
}

// deleteEvalDimension 删除用户自定义维度；内置维度返回 409。
//
// 内置维度是评估口径的基准，删掉会让历史评估结果失去维度定义，因此只允许停用。
func (app *application) deleteEvalDimension(w http.ResponseWriter, r *http.Request) {
	id, err := pathInt64(r, "id")
	if err != nil {
		app.writeError(w, http.StatusBadRequest, err)
		return
	}

	err = app.evalDimensions().Delete(r.Context(), id)
	switch {
	case err == nil:
		app.audit(r.Context(), "delete", "eval_dimension", strconv.FormatInt(id, 10), "")
		app.writeJSON(w, http.StatusOK, map[string]bool{"deleted": true})
	case errors.Is(err, store.ErrBuiltinDimensionProtected):
		app.writeError(w, http.StatusConflict, err)
	case errors.Is(err, pgx.ErrNoRows):
		app.writeError(w, http.StatusNotFound, errors.New("评估维度不存在"))
	default:
		app.writeError(w, http.StatusInternalServerError, err)
	}
}

// seedEvalDimensions 幂等写入内置维度目录，重复调用不会产生重复记录。
func (app *application) seedEvalDimensions(w http.ResponseWriter, r *http.Request) {
	inserted, total, err := eval.Seed(r.Context(), app.evalDimensions())
	if err != nil {
		app.writeError(w, http.StatusInternalServerError, err)
		return
	}
	app.audit(r.Context(), "seed", "eval_dimension", "", strconv.Itoa(inserted))
	app.writeJSON(w, http.StatusOK, map[string]int{"inserted": inserted, "total": total})
}

// listEvalDimensionCategories 返回库中实际存在的分类，供前端分组渲染。
func (app *application) listEvalDimensionCategories(w http.ResponseWriter, r *http.Request) {
	categories, err := app.evalDimensions().Categories(r.Context())
	if err != nil {
		app.writeError(w, http.StatusInternalServerError, err)
		return
	}
	app.writeJSON(w, http.StatusOK, map[string][]string{"categories": categories})
}

// decodeEvalDimension 解析请求体。零值字段由 store 层补默认值。
func decodeEvalDimension(r *http.Request) (model.EvalDimension, error) {
	var input model.EvalDimension
	if r.Body == nil {
		return input, errors.New("请求体不能为空")
	}
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		return input, err
	}
	return input, nil
}
