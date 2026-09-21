package main

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"runtime/debug"
	"strings"
	"time"

	"github.com/1420970597/llm/internal/model"
)

type contextKey string

const (
	sessionCookieName contextKey = "llm_session"
	userContextKey    contextKey = "session_user"
)

type sessionPayload struct {
	User      model.User `json:"user"`
	ExpiresAt int64      `json:"expiresAt"`
}

func (app *application) login(w http.ResponseWriter, r *http.Request) {
	var input model.LoginRequest
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		app.writeError(w, http.StatusBadRequest, err)
		return
	}

	user, err := app.auth.Authenticate(r.Context(), input.Email, input.Password)
	if err != nil {
		app.writeError(w, http.StatusUnauthorized, err)
		return
	}

	cookie, err := app.createSessionCookie(user)
	if err != nil {
		app.writeError(w, http.StatusInternalServerError, err)
		return
	}

	http.SetCookie(w, cookie)
	app.writeJSON(w, http.StatusOK, model.AuthResponse{User: user})
}

func (app *application) me(w http.ResponseWriter, r *http.Request) {
	user, ok := app.currentUser(r)
	if !ok {
		app.writeError(w, http.StatusUnauthorized, errors.New(msgAuthRequired))
		return
	}
	app.writeJSON(w, http.StatusOK, model.AuthResponse{User: user})
}

func (app *application) logout(w http.ResponseWriter, _ *http.Request) {
	http.SetCookie(w, app.clearSessionCookie())
	app.writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (app *application) middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		defer func() {
			if recovered := recover(); recovered != nil {
				log.Printf("panic method=%s path=%s err=%v stack=%s", r.Method, r.URL.Path, recovered, string(debug.Stack()))
				app.writeError(w, http.StatusInternalServerError, errors.New("服务暂时不可用，请稍后重试"))
			}
			log.Printf("method=%s path=%s duration=%s", r.Method, r.URL.Path, time.Since(start))
		}()

		w.Header().Set("Access-Control-Allow-Origin", app.cfg.AllowedOrigin)
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
		w.Header().Set("Access-Control-Allow-Methods", "GET,POST,PUT,OPTIONS")
		w.Header().Set("Access-Control-Allow-Credentials", "true")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}

		r = app.withSessionUser(r)

		if app.routeRequiresAdmin(r.URL.Path) {
			user, ok := app.currentUser(r)
			if !ok {
				app.writeError(w, http.StatusUnauthorized, errors.New(msgAuthRequired))
				return
			}
			if user.Role != "admin" {
				app.writeError(w, http.StatusForbidden, errors.New(msgAdminRequired))
				return
			}
		} else if app.routeRequiresAuth(r.URL.Path) {
			if _, ok := app.currentUser(r); !ok {
				app.writeError(w, http.StatusUnauthorized, errors.New(msgAuthRequired))
				return
			}
		}

		next.ServeHTTP(w, r)
	})
}

func (app *application) routeRequiresAuth(path string) bool {
	switch {
	case strings.HasPrefix(path, "/api/v1/auth/"):
		return false
	case path == "/healthz", path == "/readyz":
		return false
	case path == "/api/v1/platform/overview":
		return false
	default:
		return true
	}
}

func (app *application) routeRequiresAdmin(path string) bool {
	return strings.HasPrefix(path, "/api/v1/admin/")
}

func (app *application) withSessionUser(r *http.Request) *http.Request {
	cookie, err := r.Cookie(string(sessionCookieName))
	if err != nil || cookie.Value == "" {
		return r
	}

	raw, err := app.box.Decrypt(cookie.Value)
	if err != nil {
		return r
	}

	var payload sessionPayload
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		return r
	}
	if payload.ExpiresAt <= time.Now().Unix() {
		return r
	}

	// 从服务端当前状态刷新身份（Issue #160 T03）。
	//
	// 为什么不能只信 cookie：cookie 是 24 小时的**角色副本**。
	// 管理员在库里被降级为 user 后，旧 cookie 里的 `role="admin"`
	// 仍然能让 /api/v1/admin/* 通过检查 —— 那就是一个最长 24 小时的
	// 权限提升窗口，而它恰好是 T03 要求关闭的东西。
	//
	// 失败时**取不到就不认这个会话**（fail closed）：数据库不可用时
	// 「继续信任旧角色」等于「数据库故障时权限全部放开」。
	current, err := app.loadSessionUser(r.Context(), payload.User.ID)
	if err != nil {
		return r
	}
	return r.WithContext(context.WithValue(r.Context(), userContextKey, current))
}

// sessionUserLoader 接口化「按 ID 取当前用户」这一步。
//
// 为什么要接口而不是直接调 app.auth：这条路径的**两个分支**（刷新成功 → 用库里的
// 角色；刷新失败 → 丢弃会话）都必须在测试里跑到，而它们正好是 T03 的核心断言。
// 直接调 store 会让测试必须连库才能验证「撤权立即生效」，而那种测试在没设 DSN 时
// 静默 Skip —— 那么 CI 上就没人守着这个安全属性了。
func (app *application) loadSessionUser(ctx context.Context, userID int64) (model.User, error) {
	if app.sessionUsers != nil {
		return app.sessionUsers(ctx, userID)
	}
	return app.currentSessionUser(ctx, userID)
}

// currentSessionUser 按会话里的用户 ID 重新读取服务端当前用户。
//
// 返回错误时调用方会丢弃会话：账号被删、库不可达都属于「不能确认身份」的情形。
func (app *application) currentSessionUser(ctx context.Context, userID int64) (model.User, error) {
	if userID <= 0 || app.auth == nil {
		return model.User{}, errors.New("会话身份不可确认")
	}
	return app.auth.GetUserByID(ctx, userID)
}

func (app *application) currentUser(r *http.Request) (model.User, bool) {
	user, ok := r.Context().Value(userContextKey).(model.User)
	return user, ok
}

func (app *application) createSessionCookie(user model.User) (*http.Cookie, error) {
	payload := sessionPayload{
		User:      user,
		ExpiresAt: time.Now().Add(24 * time.Hour).Unix(),
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	sealed, err := app.box.Encrypt(string(raw))
	if err != nil {
		return nil, err
	}
	return &http.Cookie{
		Name:     string(sessionCookieName),
		Value:    sealed,
		Path:     "/",
		HttpOnly: true,
		Secure:   app.cfg.Environment == "production",
		SameSite: http.SameSiteLaxMode,
		Expires:  time.Now().Add(24 * time.Hour),
		MaxAge:   60 * 60 * 24,
	}, nil
}

func (app *application) clearSessionCookie() *http.Cookie {
	return &http.Cookie{
		Name:     string(sessionCookieName),
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Expires:  time.Unix(0, 0),
		MaxAge:   -1,
	}
}
