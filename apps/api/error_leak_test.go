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

// 本文件锁定 issue #102：**任何**状态码下都不得把内部实现细节回给客户端。
//
// 为什么单独成文件（而不是加进 write_error_test.go）：
// write_error_test.go 覆盖的是 5xx 路径与 4xx 原样透出这两条既有约定；
// 本文件覆盖的是**第三条约定的由来** —— handler 用 4xx 状态上报 store 层错误时，
// 驱动原文（`no rows in result set`）曾经原封不动进了响应体。
// 两者是不同年代的约定，分开更利于将来判断哪一条被破坏。

func decodeErr(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var payload struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("响应体不是 JSON: %v (body=%q)", err, rec.Body.String())
	}
	return payload.Error
}

// issue #102 的原始形态：handler 把 pgx.ErrNoRows 以 **404** 上报，
// 修复前 msg = err.Error() = "no rows in result set" 会原样回给客户端。
func TestWriteErrorNeverLeaksDriverTextRegardlessOfStatus(t *testing.T) {
	app := &application{}

	cases := []struct {
		name   string
		status int
		err    error
	}{
		{"404 直接透传 pgx.ErrNoRows", http.StatusNotFound, pgx.ErrNoRows},
		{"404 包装过的 ErrNoRows", http.StatusNotFound, fmt.Errorf("加载数据集失败: %w", pgx.ErrNoRows)},
		{"400 传驱动原文", http.StatusBadRequest, errors.New("no rows in result set")},
		{"409 传驱动原文", http.StatusConflict, errors.New("no rows in result set")},
		{"404 传 SQL 语法错误", http.StatusNotFound, errors.New("pq: syntax error at or near \"SELCT\"")},
		{"400 传约束冲突", http.StatusBadRequest, errors.New("duplicate key value violates unique constraint \"x_key\"")},
		{"404 传表名泄漏", http.StatusNotFound, errors.New("relation \"secret_table\" does not exist")},
		{"500 传驱动原文", http.StatusInternalServerError, errors.New("no rows in result set")},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			app.writeError(rec, tc.status, tc.err)
			body := decodeErr(t, rec)

			// 核心断言：响应体不得含任何内部实现细节。
			for _, banned := range []string{
				"no rows in result set", "SQLSTATE", "pq:", "pgx", "sql:",
				"syntax error", "duplicate key", "violates ",
				"relation \"", "column \"", "does not exist",
			} {
				if strings.Contains(body, banned) {
					t.Errorf("状态码 %d 的响应体泄漏了内部细节 %q：%q", tc.status, banned, body)
				}
			}
			// 文案必须是中文（产品面向中文用户；英文会被前端原样渲染）。
			if !hasChinese(body) {
				t.Errorf("状态码 %d 的响应体不是中文文案：%q", tc.status, body)
			}
		})
	}
}

// 驱动层「记录不存在」必须稳定映射为 404，**与调用方传的状态码无关**。
//
// 这条保证很重要：handler 若误传 500，用户会看到「服务异常」而不是「找不到」，
// 从而误以为系统故障。修复前 404 只对 5xx 生效，现在提到最前面。
func TestWriteErrorMapsErrNoRowsToNotFoundAtAnyStatus(t *testing.T) {
	app := &application{}
	for _, status := range []int{
		http.StatusInternalServerError,
		http.StatusBadRequest,
		http.StatusConflict,
		http.StatusNotFound,
	} {
		rec := httptest.NewRecorder()
		app.writeError(rec, status, fmt.Errorf("包装: %w", pgx.ErrNoRows))
		if rec.Code != http.StatusNotFound {
			t.Errorf("传入状态 %d 时 writeError 返回 %d，期望 404", status, rec.Code)
		}
		if got := decodeErr(t, rec); !hasChinese(got) {
			t.Errorf("传入状态 %d 时文案非中文：%q", status, got)
		}
	}
}

// 4xx 的**业务**文案必须原样透出（既有约定不能被本次修复破坏）。
//
// 这是防矫枉过正的关键：如果改成「4xx 一律替换成通用文案」，
// 用户就再也看不到「指定的 AI 服务不存在」这类真正有用的提示。
func TestWriteErrorStillPassesThroughChineseBusinessMessages(t *testing.T) {
	app := &application{}
	business := []string{
		"指定的 AI 服务不存在",
		"未找到该子资源",
		"难度配比不能为空",
		"打分档次至少需要两档（例如 -1 与 1），请先在任务设置里补齐",
		"该任务还没有题目，请先在「问题生成」页生成题目",
	}
	for _, msg := range business {
		rec := httptest.NewRecorder()
		app.writeError(rec, http.StatusBadRequest, errors.New(msg))
		if got := decodeErr(t, rec); got != msg {
			t.Errorf("业务文案被改写：期望 %q，实际 %q", msg, got)
		}
	}
}

// 中文业务文案里**合法地**出现英文标识符时不得被误判为内部细节。
//
// 例：导出格式名（alpaca/csv）与字段名（providerId）都是用户要看到的，
// 若用「含英文就替换」的宽泛规则，这些提示会被吞掉。
func TestWriteErrorDoesNotFlagLegitimateEnglishIdentifiers(t *testing.T) {
	app := &application{}
	business := []string{
		"不支持的导出格式 \"xml\"，可用格式：jsonl, alpaca, sharegpt, csv, parquet",
		"缺少必要的查询参数 artifactId，请从导出页面重新发起下载",
		"datasetId 不是有效的整数",
	}
	for _, msg := range business {
		rec := httptest.NewRecorder()
		app.writeError(rec, http.StatusBadRequest, errors.New(msg))
		if got := decodeErr(t, rec); got != msg {
			t.Errorf("合法英文标识符被误伤：期望 %q，实际 %q", msg, got)
		}
	}
}

// looksLikeInternalDetail 的独立表驱动（含大小写与边界）。
func TestLooksLikeInternalDetail(t *testing.T) {
	cases := []struct {
		msg  string
		want bool
	}{
		{"no rows in result set", true},
		{"NO ROWS IN RESULT SET", true},
		{"ERROR: SQLSTATE 23505", true},
		{"pq: relation \"users\" does not exist", true},
		{"syntax error at or near", true},
		{"duplicate key value violates unique constraint", true},
		{"connection refused", true},
		{"i/o timeout", true},
		// 正常业务文案不能被误判
		{"指定的 AI 服务不存在", false},
		{"该任务还没有题目，请先在「问题生成」页生成题目", false},
		{"未找到该子资源", false},
		{"difficulty mix ratio must be positive", false},
	}
	for _, tc := range cases {
		if got := looksLikeInternalDetail(tc.msg); got != tc.want {
			t.Errorf("looksLikeInternalDetail(%q) = %v, 期望 %v", tc.msg, got, tc.want)
		}
	}
}

// userFacingError 必须同时做到两件事：Error() 只给用户看中文，
// 而 errors.Is/As 仍能定位底层错误。
//
// 这条是本次修复的关键机制：直接用 fmt.Errorf("中文: %w", err) 会把
// 被包装错误的 Error() 文本也拼进去，若它是 pgx.ErrNoRows，结果仍是
// 「中文: no rows in result set」，照旧泄漏。
func TestUserFacingErrorKeepsIdentityButHidesCause(t *testing.T) {
	wrapped := newUserFacingError(msgDatasetNotFound, pgx.ErrNoRows)

	if got := wrapped.Error(); got != msgDatasetNotFound {
		t.Errorf("Error() = %q，期望只含中文文案 %q", got, msgDatasetNotFound)
	}
	if strings.Contains(wrapped.Error(), "no rows in result set") {
		t.Error("Error() 泄漏了被包装错误的原文")
	}
	if !errors.Is(wrapped, pgx.ErrNoRows) {
		t.Error("errors.Is 应能识别底层 pgx.ErrNoRows（writeError 靠它映射 404）")
	}
}

// 端到端：userFacingError 包住 ErrNoRows 后交给 writeError，
// 结果必须是 404 + 中文，而不是 404 + 驱动原文。
func TestUserFacingErrorThroughWriteError(t *testing.T) {
	app := &application{}
	rec := httptest.NewRecorder()
	app.writeError(rec, http.StatusNotFound, newUserFacingError(msgDatasetNotFound, pgx.ErrNoRows))

	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, 期望 404", rec.Code)
	}
	if got := decodeErr(t, rec); got != msgDatasetNotFound {
		t.Errorf("body = %q, 期望 %q", got, msgDatasetNotFound)
	}
}

// 所有用户可见文案常量都必须是中文，且不得含内部术语。
//
// 这条是 #109 的守卫：文案里出现「去重键」「TTL」「游标」「cursor」
// 这类实现概念时，用户无法据此行动。
func TestUserFacingMessagesAreChineseAndFreeOfInternalJargon(t *testing.T) {
	messages := map[string]string{
		"msgDatasetNotFound":     msgDatasetNotFound,
		"msgEvalRunNotFound":     msgEvalRunNotFound,
		"msgCleaningRunNotFound": msgCleaningRunNotFound,
		"msgArtifactNotFound":    msgArtifactNotFound,
		"msgAuthRequired":        msgAuthRequired,
		"msgAdminRequired":       msgAdminRequired,
		"msgProviderUnavailable": msgProviderUnavailable,
		"msgNoDomains":           msgNoDomains,
		"msgNoDirections":        msgNoDirections,
		"msgNoQuestions":         msgNoQuestions,
		"msgNoReasoning":         msgNoReasoning,
		"msgNoRewardRecords":     msgNoRewardRecords,
		"msgRewardsIncomplete":   msgRewardsIncomplete,
		"msgReasoningIncomplete": msgReasoningIncomplete,
		"msgNoChainStepTargets":  msgNoChainStepTargets,
		"msgStepsEmpty":          msgStepsEmpty,
		"msgRewardLevelsTooFew":  msgRewardLevelsTooFew,
		"msgRunNotResumable":     msgRunNotResumable,
		"msgNoResumableRun":      msgNoResumableRun,
		"msgDuplicateEnqueue":    msgDuplicateEnqueue,
		"msgEnqueueFailed":       msgEnqueueFailed,
	}

	// 内部术语黑名单（issue #109 里用户实际看到的那批）。
	jargon := []string{
		"去重键", "TTL", "游标", "cursor", "dedup", "dimension_keys",
		"level=2", "generation_run", "SetNX", "redis", "Redis",
	}

	for name, msg := range messages {
		if msg == "" {
			t.Errorf("%s 不得为空", name)
			continue
		}
		if !hasChinese(msg) {
			t.Errorf("%s 必须是中文文案，实际 %q", name, msg)
		}
		for _, term := range jargon {
			if strings.Contains(msg, term) {
				t.Errorf("%s 含内部术语 %q：%q", name, term, msg)
			}
		}
		if looksLikeInternalDetail(msg) {
			t.Errorf("%s 被判定为内部实现细节：%q", name, msg)
		}
	}
}

// hasChinese 判断字符串是否含中日韩统一表意文字。
func hasChinese(s string) bool {
	for _, r := range s {
		if r >= 0x4E00 && r <= 0x9FFF {
			return true
		}
	}
	return false
}
