package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/1420970597/llm/internal/eval"
	"github.com/1420970597/llm/internal/model"
	"github.com/1420970597/llm/internal/store"
)

// 本文件由 L7 lane 独占。通过路由注册表接入，不修改 main.go。
//
// 冻结契约（docs/plans/eval-and-cleaning-plan.md 第 3.7 节）：
//
//	GET /api/v1/admin/eval/judges      -> EvalJudgeOption[]
//	PUT /api/v1/eval/runs/{runId}/judges  body {"providerIds":[2,3]} -> EvalRunJudge[]

// evalJudgesApp 保存 main() 在 applyRouteRegistrars 时注入的 application。
//
// 路径参数版的路由（/api/v1/eval/runs/{runId}/judges）拿不到 application，
// 而 RegisterRoutes 必须在 init() 里调用（此时 app 还不存在），因此先捕获。
// applyRouteRegistrars 在 ListenAndServe 之前执行，请求到达时必然已就绪。
var evalJudgesApp *application

func init() {
	RegisterRoutes(func(mux *http.ServeMux, app *application) {
		evalJudgesApp = app
		mux.HandleFunc("GET /api/v1/admin/eval/judges", app.listEvalJudgeOptions)
		mux.HandleFunc("PUT /api/v1/eval/runs/{runId}/judges", app.setEvalRunJudges)
	})
}

func (app *application) evalJudgeStore() *store.EvalJudgeStore {
	return store.NewEvalJudgeStore(app.db())
}

// listEvalJudgeOptions 返回可选的裁判模型列表。
//
// 走 /api/v1/admin/ 前缀，middleware 已强制管理员权限，此处不再重复校验。
func (app *application) listEvalJudgeOptions(w http.ResponseWriter, r *http.Request) {
	options, err := app.evalJudgeStore().ListProviderOptions(r.Context())
	if err != nil {
		app.writeError(w, http.StatusInternalServerError, err)
		return
	}
	app.writeJSON(w, http.StatusOK, options)
}

// setEvalRunJudges 设定某次评估运行的裁判，并剔除生成者自身。
//
// 语义：用户给出想用的 providerIds，但生成该数据集的模型必须被剔除——
// 若用户选中了它，仍写入记录并标 excluded=true + 原因，使「为何没参与」可追溯，
// 而不是静默丢弃让用户以为配置生效了。
func (app *application) setEvalRunJudges(w http.ResponseWriter, r *http.Request) {
	runID, err := strconv.ParseInt(strings.TrimSpace(r.PathValue("runId")), 10, 64)
	if err != nil || runID <= 0 {
		app.writeError(w, http.StatusBadRequest, fmt.Errorf("invalid eval run id"))
		return
	}

	var input struct {
		ProviderIDs []int64 `json:"providerIds"`
	}
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		app.writeError(w, http.StatusBadRequest, err)
		return
	}
	if len(input.ProviderIDs) == 0 {
		app.writeError(w, http.StatusBadRequest, fmt.Errorf("providerIds must not be empty"))
		return
	}

	judgeStore := app.evalJudgeStore()
	generatorProviderID, err := judgeStore.GeneratorProviderID(r.Context(), runID)
	if err != nil {
		app.writeError(w, http.StatusNotFound, fmt.Errorf("eval run %d not found: %w", runID, err))
		return
	}

	providers, err := app.store.ListProviders(r.Context())
	if err != nil {
		app.writeError(w, http.StatusInternalServerError, err)
		return
	}

	// 先对全部候选做剔除判定，再按用户选择过滤——
	// 这样用户选中的生成者模型也能拿到准确的剔除原因。
	_, records, err := eval.ResolveJudges(r.Context(), providers, generatorProviderID)
	if err != nil {
		app.writeError(w, http.StatusInternalServerError, err)
		return
	}

	wanted := make(map[int64]struct{}, len(input.ProviderIDs))
	for _, id := range input.ProviderIDs {
		wanted[id] = struct{}{}
	}

	selected := make([]model.EvalRunJudge, 0, len(input.ProviderIDs))
	found := make(map[int64]struct{}, len(input.ProviderIDs))
	for _, record := range records {
		if _, ok := wanted[record.ProviderID]; !ok {
			continue
		}
		selected = append(selected, record)
		found[record.ProviderID] = struct{}{}
	}

	missing := make([]string, 0)
	for _, id := range input.ProviderIDs {
		if _, ok := found[id]; !ok {
			missing = append(missing, strconv.FormatInt(id, 10))
		}
	}
	if len(missing) > 0 {
		app.writeError(w, http.StatusBadRequest, fmt.Errorf("unknown provider ids: %s", strings.Join(missing, ",")))
		return
	}

	saved, err := judgeStore.UpsertRunJudges(r.Context(), runID, selected)
	if err != nil {
		app.writeError(w, http.StatusInternalServerError, err)
		return
	}

	usable := 0
	for _, judge := range saved {
		if !judge.Excluded {
			usable++
		}
	}
	app.audit(r.Context(), "update", "eval_run_judges", strconv.FormatInt(runID, 10),
		fmt.Sprintf("selected=%d usable=%d generator_provider_id=%d", len(saved), usable, generatorProviderID))

	app.writeJSON(w, http.StatusOK, saved)
}
