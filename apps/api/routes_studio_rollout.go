package main

import (
	"net/http"

	"github.com/1420970597/llm/internal/config"
	"github.com/1420970597/llm/internal/studio"
)

// 本文件暴露 Atelier 的**运维状态**（Issue #160 T33）。
//
// 两个端点，都是只读、都要求管理员：
//
//	GET /api/v1/studio/rollout  当前特性开关与项目级回退名单
//	GET /api/v1/studio/health   运维诊断快照（队列年龄/租约回收/预算/用量/发布）
//
// 为什么要求管理员而不是「任何登录用户」：快照里含预算金额与项目级回退原因，
// 而回退原因往往是运维手写的现场描述（可能提到客户或事故）。这类信息
// 与项目内容同级敏感，交给成员自助查看会把治理信息泄露成公共信息。
//
// 为什么不是 `/api/v1/admin/*`：它属于 Studio 而不是旧控制台治理面，
// 放在 studio 命名空间下可以让「回退时看哪里」与「回退开关本身」在同一处。

func init() {
	RegisterRoutes(registerStudioRolloutRoutes)
}

func registerStudioRolloutRoutes(mux *http.ServeMux, app *application) {
	mux.HandleFunc("GET /api/v1/studio/rollout", app.getStudioRollout)
	mux.HandleFunc("GET /api/v1/studio/health", app.getStudioHealth)
}

// studioRolloutFromConfig 把进程配置翻译成 studio.Rollout（T33）。
//
// 支持 `STUDIO_DISABLED_PROJECT_IDS=12:发布积压,13:成本失控` 这种带原因的写法；
// 纯 ID 列表仍然可用（原因为空）。解析不出 ID 时回退到配置里已经解析好的列表，
// 避免两处解析口径不一致导致「配置看起来生效了其实没有」。
func studioRolloutFromConfig(cfg config.APIConfig) studio.Rollout {
	ids, reasons := studio.ParseProjectIDList(cfg.StudioDisabledProjectsRaw)
	if len(ids) == 0 {
		ids = cfg.StudioDisabledProjectIDs
	}
	return studio.ParseRollout(cfg.StudioEnabled, ids, reasons)
}

// getStudioRollout 返回当前开关状态（运维用）。
func (app *application) getStudioRollout(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(r) {
		app.writeStudioError(w, r, studio.NewError(studio.CodeForbidden, "需要管理员权限才能查看运维开关状态"))
		return
	}
	app.writeJSON(w, http.StatusOK, app.studio.Rollout.State())
}

// getStudioHealth 返回运维诊断快照。
//
// 刻意**不**缓存：运维在事故中刷新页面时必须拿到当下的数字，
// 而一个 30 秒的缓存会让「刚恢复了没有」变成猜谜。
func (app *application) getStudioHealth(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(r) {
		app.writeStudioError(w, r, studio.NewError(studio.CodeForbidden, "需要管理员权限才能查看运维快照"))
		return
	}
	health, err := studio.LoadStudioHealth(r.Context(), app.studio.Pool, app.studio.Rollout)
	if err != nil {
		app.writeStudioError(w, r, err)
		return
	}
	app.writeJSON(w, http.StatusOK, health)
}
