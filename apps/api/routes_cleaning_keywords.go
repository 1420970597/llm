package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"

	"github.com/1420970597/llm/internal/cleaning"
	"github.com/1420970597/llm/internal/model"
	"github.com/1420970597/llm/internal/store"
	"github.com/jackc/pgx/v5"
)

// 本文件实现 L11 的清洗关键词库 HTTP 层（契约见 docs/plans/eval-and-cleaning-plan.md 第 3.11 节）。
// 只通过 RegisterRoutes 注册新前缀 /api/v1/cleaning/，不修改 main.go。
func init() {
	RegisterRoutes(func(mux *http.ServeMux, app *application) {
		mux.HandleFunc("GET /api/v1/cleaning/keywords", app.listCleaningKeywords)
		mux.HandleFunc("POST /api/v1/cleaning/keywords", app.upsertCleaningKeyword)
		mux.HandleFunc("PUT /api/v1/cleaning/keywords", app.upsertCleaningKeyword)
		mux.HandleFunc("DELETE /api/v1/cleaning/keywords/{id}", app.deleteCleaningKeyword)
		mux.HandleFunc("POST /api/v1/cleaning/keywords/import", app.importCleaningKeywords)
		mux.HandleFunc("POST /api/v1/cleaning/keywords/seed", app.seedCleaningKeywords)

		mux.HandleFunc("GET /api/v1/cleaning/rules", app.listCleaningRules)
		mux.HandleFunc("POST /api/v1/cleaning/rules", app.upsertCleaningRule)
		mux.HandleFunc("PUT /api/v1/cleaning/rules", app.upsertCleaningRule)
	})
}

func (app *application) cleaningKeywords() *store.CleaningKeywordStore {
	return store.NewCleaningKeywordStore(app.db())
}

func (app *application) listCleaningKeywords(w http.ResponseWriter, r *http.Request) {
	// 首次读取时自动补种内置词库（issue: 全新部署下内置词缺失导致清洗静默失效）。
	//
	// 为什么放在**读**路径而不是只在启动时做：与 export_mappings 的既有做法保持一致
	//（internal/store/export_mapping_store.go 的 EnsureSeeded 也挂在 GET 上），
	// 且这样「清空过关键词库」的环境也能自愈，不必重启服务。
	//
	// 幂等且不覆盖用户改动：EnsureSeeded 只在**内置词条数为 0** 时补种，
	// 而 SeedBuiltin 本身不覆盖 is_active（用户停用过的内置词不会被重新启用）。
	//
	// 补种失败不阻断列表读取：目录为空时至少要让用户看到「确实是空的」，
	// 而不是收到 500；失败原因记日志便于排查。
	if err := app.cleaningKeywords().EnsureSeeded(r.Context()); err != nil {
		log.Printf("cleaning.keywords.seed_failed err=%v", err)
	}

	// active 省略时不限启用状态；显式传 true/false 才精确过滤。
	var active *bool
	if raw := strings.TrimSpace(r.URL.Query().Get("active")); raw != "" {
		parsed, err := strconv.ParseBool(raw)
		if err != nil {
			app.writeError(w, http.StatusBadRequest, fmt.Errorf("active 必须是 true 或 false"))
			return
		}
		active = &parsed
	}

	items, err := app.cleaningKeywords().List(r.Context(), r.URL.Query().Get("category"), active)
	if err != nil {
		app.writeError(w, http.StatusInternalServerError, err)
		return
	}
	app.writeJSON(w, http.StatusOK, items)
}

func (app *application) upsertCleaningKeyword(w http.ResponseWriter, r *http.Request) {
	var input model.CleaningKeyword
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		app.writeError(w, http.StatusBadRequest, err)
		return
	}

	item, err := app.cleaningKeywords().Upsert(r.Context(), input)
	if err != nil {
		switch {
		case errors.Is(err, pgx.ErrNoRows):
			app.writeError(w, http.StatusNotFound, fmt.Errorf("关键词不存在"))
		case errors.Is(err, cleaning.ErrBuiltinKeywordIdentityImmutable),
			errors.Is(err, cleaning.ErrKeywordIdentityImmutable):
			// 409 而非 200：身份字段不可就地修改，必须让调用方知道这次没生效，
			// 否则前端会弹「已保存」而值根本没变。
			app.writeError(w, http.StatusConflict, err)
		default:
			app.writeError(w, http.StatusBadRequest, err)
		}
		return
	}
	app.writeJSON(w, http.StatusOK, item)
}

func (app *application) deleteCleaningKeyword(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		app.writeError(w, http.StatusBadRequest, fmt.Errorf("关键词 id 非法"))
		return
	}

	if err := app.cleaningKeywords().Delete(r.Context(), id); err != nil {
		switch {
		case errors.Is(err, cleaning.ErrBuiltinKeywordImmutable):
			app.writeError(w, http.StatusConflict, err)
		case errors.Is(err, pgx.ErrNoRows):
			app.writeError(w, http.StatusNotFound, fmt.Errorf("关键词 %d 不存在", id))
		default:
			app.writeError(w, http.StatusInternalServerError, err)
		}
		return
	}
	app.writeJSON(w, http.StatusOK, map[string]bool{"deleted": true})
}

func (app *application) importCleaningKeywords(w http.ResponseWriter, r *http.Request) {
	var input model.CleaningKeywordImportRequest
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		app.writeError(w, http.StatusBadRequest, err)
		return
	}
	if len(input.Patterns) == 0 {
		app.writeError(w, http.StatusBadRequest, fmt.Errorf("patterns 不能为空"))
		return
	}

	inserted, skipped, err := app.cleaningKeywords().Import(r.Context(), input.Patterns, input.Category, input.Severity)
	if err != nil {
		app.writeError(w, http.StatusInternalServerError, err)
		return
	}
	app.writeJSON(w, http.StatusOK, map[string]int{"inserted": inserted, "skipped": skipped})
}

func (app *application) seedCleaningKeywords(w http.ResponseWriter, r *http.Request) {
	keywords := app.cleaningKeywords()
	inserted, err := keywords.SeedBuiltin(r.Context())
	if err != nil {
		app.writeError(w, http.StatusInternalServerError, err)
		return
	}
	total, err := keywords.CountBuiltin(r.Context())
	if err != nil {
		app.writeError(w, http.StatusInternalServerError, err)
		return
	}
	app.writeJSON(w, http.StatusOK, map[string]int{"inserted": inserted, "total": total})
}

func (app *application) listCleaningRules(w http.ResponseWriter, r *http.Request) {
	items, err := app.cleaningKeywords().ListRules(r.Context())
	if err != nil {
		app.writeError(w, http.StatusInternalServerError, err)
		return
	}
	app.writeJSON(w, http.StatusOK, items)
}

func (app *application) upsertCleaningRule(w http.ResponseWriter, r *http.Request) {
	var input model.CleaningRule
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		app.writeError(w, http.StatusBadRequest, err)
		return
	}

	item, err := app.cleaningKeywords().UpsertRule(r.Context(), input)
	if err != nil {
		app.writeError(w, http.StatusBadRequest, err)
		return
	}
	app.writeJSON(w, http.StatusOK, item)
}
