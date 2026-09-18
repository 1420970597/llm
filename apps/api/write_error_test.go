package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
)

// writeError 是全部 ~150 个 handler 共用的错误出口。这里覆盖它的两条约定。

// store 层用 pgx.ErrNoRows 表示「记录不存在」，handler 往往一律按 500 上报。
// writeError 必须把它降级为 404，否则用户访问不存在的任务会看到 500
// （issue #66：GET /api/v1/datasets/99999 -> 500）。
func TestWriteErrorDowngradesErrNoRowsToNotFound(t *testing.T) {
	app := &application{}
	rec := httptest.NewRecorder()

	app.writeError(rec, http.StatusInternalServerError, pgx.ErrNoRows)

	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusNotFound)
	}
	body := decodeErrorBody(t, rec)
	if body == "internal server error" {
		t.Error("body leaked the English fallback text that issue #66 reported")
	}
}

// 包装过的 ErrNoRows（store 层常 fmt.Errorf("%w") 再返回）也必须能识别。
func TestWriteErrorDowngradesWrappedErrNoRows(t *testing.T) {
	app := &application{}
	rec := httptest.NewRecorder()

	app.writeError(rec, http.StatusInternalServerError, fmt.Errorf("加载数据集失败: %w", pgx.ErrNoRows))

	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want %d (wrapped ErrNoRows must also downgrade)", rec.Code, http.StatusNotFound)
	}
}

// 真正的服务端故障仍是 500，但不允许把内部错误原文（可能含 SQL、连接串）
// 或英文兜底文案返回给客户端 —— 前端会把 body 里的 error 直接渲染给中文用户。
func TestWriteErrorHidesInternalDetailAndStaysChinese(t *testing.T) {
	app := &application{}
	rec := httptest.NewRecorder()

	app.writeError(rec, http.StatusInternalServerError,
		errors.New("pq: relation \"secret_table\" does not exist"))

	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusInternalServerError)
	}
	body := decodeErrorBody(t, rec)
	if strings.Contains(body, "secret_table") {
		t.Errorf("body leaked internal detail: %q", body)
	}
	if body == "internal server error" {
		t.Error("body must not be the English fallback (issue #66)")
	}
	if !strings.ContainsAny(body, "服务暂时不可用请稍后重试") {
		t.Errorf("body should be a Chinese user-facing message, got %q", body)
	}
}

// 4xx 的原文必须原样透出：这些是给用户看的业务提示，不能被改写。
func TestWriteErrorPassesThroughClientErrors(t *testing.T) {
	app := &application{}
	rec := httptest.NewRecorder()

	app.writeError(rec, http.StatusBadRequest, errors.New("难度配比不能为空"))

	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
	if got := decodeErrorBody(t, rec); got != "难度配比不能为空" {
		t.Errorf("body = %q, want the original client-facing message", got)
	}
}

func decodeErrorBody(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var payload struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("response body is not JSON: %v (body=%q)", err, rec.Body.String())
	}
	return payload.Error
}
