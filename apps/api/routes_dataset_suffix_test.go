package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// 数据集子路由注册表的行为测试（issue #8）。
//
// 冻结契约 docs/plans/eval-and-cleaning-plan.md 第 1.1 节声明了
// RegisterDatasetGet / RegisterDatasetAction / datasetActionFunc 三个符号。
// 这三个符号原先不存在，导致 L3 的 questions/difficulty-stats 与 L6 的
// export/formats 这类**含两个路径段**的端点无法注册（单段接口只能表达单段，
// 而 questions/export 又都在禁止注册清单里）。
//
// 本文件断言按 suffix 注册/查找确实可用。

// TestDatasetSuffixRegistryMatchesMultiSegmentSuffix 是本 lane 的核心断言：
// 含斜杠的 suffix 必须能注册并被命中。
func TestDatasetSuffixRegistryMatchesMultiSegmentSuffix(t *testing.T) {
	suffix := "l15-r5-test/multi-segment"
	// 唯一后缀，避免与任何真实注册冲突（重复注册会 panic，这是设计行为）。
	registerDatasetSuffix(datasetGets, "get", suffix, func(w http.ResponseWriter, r *http.Request, id int64) {
		w.WriteHeader(http.StatusTeapot)
	})

	fn, found := lookupDatasetSuffix(datasetGets, suffix)
	if !found || fn == nil {
		t.Fatalf("多段 suffix %q 未注册成功；#8 的多段端点仍无法表达", suffix)
	}
}

// TestDatasetSuffixRegistryPrefersLongestMatch 断言最长匹配：
// 同时注册 "export" 与 "export/formats" 时，更具体的那个获胜。
func TestDatasetSuffixRegistryPrefersLongestMatch(t *testing.T) {
	base := "l15-r5-test/export"
	specific := base + "/formats"

	registerDatasetSuffix(datasetGets, "get", base, func(w http.ResponseWriter, r *http.Request, id int64) {
		w.Header().Set("X-Matched", "base")
	})
	registerDatasetSuffix(datasetGets, "get", specific, func(w http.ResponseWriter, r *http.Request, id int64) {
		w.Header().Set("X-Matched", "specific")
	})

	rec := httptest.NewRecorder()
	fn, found := lookupDatasetSuffix(datasetGets, specific)
	if !found {
		t.Fatalf("suffix %q 未命中", specific)
	}
	fn(rec, httptest.NewRequest(http.MethodGet, "/", nil), 1)
	if got := rec.Header().Get("X-Matched"); got != "specific" {
		t.Errorf("命中 %q，期望 specific（更长/更具体的 suffix 必须获胜）", got)
	}
}

// TestDatasetSuffixRegistryFallsBackToShorterPrefix 断言未精确命中时逐级回退到更短的前缀，
// 这样注册一段 "cleaning" 就能覆盖 "cleaning/run"、"cleaning/runs" 等子路径。
func TestDatasetSuffixRegistryFallsBackToShorterPrefix(t *testing.T) {
	prefix := "l15-r5-test/fallback"
	registerDatasetSuffix(datasetGets, "get", prefix, func(w http.ResponseWriter, r *http.Request, id int64) {
		w.Header().Set("X-Matched", "prefix")
	})

	rec := httptest.NewRecorder()
	fn, found := lookupDatasetSuffix(datasetGets, prefix+"/deep/child")
	if !found {
		t.Fatalf("未回退到前缀 %q", prefix)
	}
	fn(rec, httptest.NewRequest(http.MethodGet, "/", nil), 1)
	if got := rec.Header().Get("X-Matched"); got != "prefix" {
		t.Errorf("命中 %q，期望 prefix", got)
	}
}

// TestDatasetSuffixRegistryNormalizesSlashes 断言注册与匹配使用同一拼写规则，
// 否则 "/questions/difficulty-stats/" 这类写法会静默注册到查不到的表项。
func TestDatasetSuffixRegistryNormalizesSlashes(t *testing.T) {
	registerDatasetSuffix(datasetGets, "get", "/l15-r5-test/slashes/", func(http.ResponseWriter, *http.Request, int64) {})

	if _, found := lookupDatasetSuffix(datasetGets, "l15-r5-test/slashes"); !found {
		t.Error("带前后斜杠注册的 suffix 无法用规范拼写查到")
	}
	if got := normalizeDatasetSuffix("/a/b/"); got != "a/b" {
		t.Errorf("normalizeDatasetSuffix = %q, want %q", got, "a/b")
	}
}

// TestDatasetSuffixRegistrySeparatesGetAndAction 断言 GET 与 POST 表互相隔离，
// 否则给 reward-levels 注册 PUT/GET 语义时会出现跨方法串台。
func TestDatasetSuffixRegistrySeparatesGetAndAction(t *testing.T) {
	suffix := "l15-r5-test/method-scoped"
	registerDatasetSuffix(datasetGets, "get", suffix, func(http.ResponseWriter, *http.Request, int64) {})

	if _, found := lookupDatasetSuffix(datasetActions, suffix); found {
		t.Error("只在 datasetGets 注册的 suffix 不应出现在 datasetActions 里")
	}
}

// TestDatasetSubResourceSuffixDistinguishesResourceFromSubResource 是本 lane
// 404 兜底的判据：只有精确指向数据集本身时才允许返回数据集图。
func TestDatasetSubResourceSuffixDistinguishesResourceFromSubResource(t *testing.T) {
	cases := []struct {
		path   string
		suffix string
		ok     bool
	}{
		// 精确指向数据集本身 -> suffix 为空，允许返回数据集图。
		{"/api/v1/datasets/12", "", true},
		{"/api/v1/datasets/12/", "", true},
		// 子资源 -> suffix 非空，必须 404（除非注册表或 legacy 认领）。
		{"/api/v1/datasets/12/whatever", "whatever", true},
		{"/api/v1/datasets/12/questions/difficulty-stats", "questions/difficulty-stats", true},
		// 路径结构不合法 -> ok=false。
		{"/api/v1/datasets/", "", false},
		{"/api/v1/datasets/not-a-number/whatever", "", false},
		{"/api/v1/datasets/0/x", "", false},
		{"/api/v1/other/12/x", "", false},
	}

	for _, tc := range cases {
		t.Run(tc.path, func(t *testing.T) {
			suffix, ok := datasetSubResourceSuffix(tc.path)
			if ok != tc.ok || suffix != tc.suffix {
				t.Errorf("datasetSubResourceSuffix(%q) = (%q, %v), want (%q, %v)",
					tc.path, suffix, ok, tc.suffix, tc.ok)
			}
		})
	}
}

// TestTryDatasetRouterRejectsUnregisteredSubResource 断言注册表未命中时返回 false，
// 让调用方有机会去 legacy 分支或走 404 兜底 —— 而不是被错误地当成命中。
func TestTryDatasetRouterRejectsUnregisteredSubResource(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/api/v1/datasets/1/l15-r5-test-never-registered", nil)
	rec := httptest.NewRecorder()

	if tryDatasetRouter(rec, req) {
		t.Error("未注册的子路径被报告为命中；调用方会跳过 404 兜底，重新引入 #8 的静默 200")
	}
}

// TestTryDatasetRouterMatchesRegisteredSuffix 断言注册后的 suffix 真的被路由到。
func TestTryDatasetRouterMatchesRegisteredSuffix(t *testing.T) {
	registerDatasetSuffix(datasetGets, "get", "l15-r5-test-routed/inner", func(w http.ResponseWriter, r *http.Request, id int64) {
		if id != 42 {
			t.Errorf("handler 收到 id=%d, want 42", id)
		}
		w.WriteHeader(http.StatusAccepted)
	})

	req := httptest.NewRequest(http.MethodGet, "/api/v1/datasets/42/l15-r5-test-routed/inner", nil)
	rec := httptest.NewRecorder()

	if !tryDatasetRouter(rec, req) {
		t.Fatal("已注册的 suffix 未被路由到")
	}
	if rec.Code != http.StatusAccepted {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusAccepted)
	}
}

// TestDatasetSubResourceNotFoundMessageIsChinese 断言 404 文案是契约冻结的中文值。
//
// writeError 的既有约定（PR #68）是 4xx 透出中文业务提示；这里用的是
// writeDatasetSubResourceNotFound 的专用出口，文案必须与契约 §1.1 一致。
func TestDatasetSubResourceNotFoundMessageIsChinese(t *testing.T) {
	if datasetSubResourceNotFoundMessage != "未找到该子资源" {
		t.Errorf("datasetSubResourceNotFoundMessage = %q, want %q",
			datasetSubResourceNotFoundMessage, "未找到该子资源")
	}
	if strings.ContainsAny(datasetSubResourceNotFoundMessage, "abcdefghijklmnopqrstuvwxyz") {
		t.Error("404 文案不得混入英文；前端会把它直接渲染给中文用户")
	}

	app := &application{}
	rec := httptest.NewRecorder()
	app.writeDatasetSubResourceNotFound(rec)

	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusNotFound)
	}
	if !strings.Contains(rec.Body.String(), datasetSubResourceNotFoundMessage) {
		t.Errorf("响应体未包含契约文案: %s", rec.Body.String())
	}
}
