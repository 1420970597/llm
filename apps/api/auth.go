package main

import (
	"context"
	"encoding/json"
	"fmt"
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
		app.writeError(w, http.StatusUnauthorized, fmt.Errorf("authentication required"))
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
				app.writeError(w, http.StatusInternalServerError, fmt.Errorf("internal server error"))
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
				app.writeError(w, http.StatusUnauthorized, fmt.Errorf("authentication required"))
				return
			}
			if user.Role != "admin" {
				app.writeError(w, http.StatusForbidden, fmt.Errorf("admin privileges required"))
				return
			}
		} else if app.routeRequiresAuth(r.URL.Path) {
			if _, ok := app.currentUser(r); !ok {
				app.writeError(w, http.StatusUnauthorized, fmt.Errorf("authentication required"))
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

	return r.WithContext(context.WithValue(r.Context(), userContextKey, payload.User))
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
