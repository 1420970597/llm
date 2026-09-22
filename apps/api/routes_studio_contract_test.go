package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/1420970597/llm/internal/model"
	"github.com/1420970597/llm/internal/store"
	"github.com/1420970597/llm/internal/studio"
)

// 本文件是 Issue #160 T08 的**契约测试**。
//
// 契约 §6 原文：
//
//	「TS 类型与本节 schema **一致**；由 T08 的契约测试断言。」
//
// 为什么值得写一个反射 + 文本解析的测试，而不是靠人工 review：
// 「后端加了字段、前端类型没跟上」不会让任何一侧编译失败，也不会让任何
// 既有用例变红 —— 它只在某个页面读不到数据时以 `undefined` 的形式出现，
// 而那时排查要跨 Go/TS 两个仓库区域。这个测试把那种漂移变成 CI 失败。
//
// 三组断言：
//  1. 每个响应实体的 Go JSON 字段名集合 == `studio.ts` 里对应 interface 的字段集合；
//  2. 错误响应确实是契约 §1.2 的**嵌套**形状（顶层只有 error）；
//  3. T08 注册的每个 Atelier 路由都可达（不是 404），且未知子资源是 404。

// studioTypeScriptPath 是前端类型文件。契约 §6 指定了它的位置。
const studioTypeScriptPath = "web-user/src/lib/api/studio.ts"

// contractTypes 是「Go 类型 → TS interface 名」的映射。
//
// 显式列出而不是自动猜名字：映射本身就是文档（哪一类响应由哪个类型定义），
// 而自动猜名字会在重命名时静默跳过某个类型 —— 那正是这个测试要防的漂移。
func contractTypes() []struct {
	name string
	ts   string
	typ  any
} {
	return []struct {
		name string
		ts   string
		typ  any
	}{
		{"studio.Envelope", "Envelope", studio.Envelope{}},
		{"studio.ErrorBody", "ApiErrorBody", studio.ErrorBody{}},
		{"studio.Blocker", "ApiBlocker", studio.Blocker{}},
		{"studio.Page", "Page", studio.Page[studio.BatchSummary]{}},
		{"studio.ProjectOverview", "ProjectOverview", studio.ProjectOverview{}},
		{"studio.OverviewVersions", "OverviewVersions", studio.OverviewVersions{}},
		{"studio.VersionSummary", "VersionSummary", studio.VersionSummary{}},
		{"studio.OverviewBatches", "OverviewBatches", studio.OverviewBatches{}},
		{"studio.NextAction", "NextAction", studio.NextAction{}},
		{"studio.BatchSummary", "BatchSummary", studio.BatchSummary{}},
		{"studio.BatchBudgetView", "BatchBudgetView", studio.BatchBudgetView{}},
		{"studio.BatchCapabilities", "BatchCapabilities", studio.BatchCapabilities{}},
		{"studio.SampleCapabilities", "SampleCapabilities", studio.SampleCapabilities{}},
		{"studio.DocumentCapabilities", "DocumentCapabilities", studio.DocumentCapabilities{}},
		{"studio.SampleStats", "SampleStats", studio.SampleStats{}},
		{"model.BudgetSnapshot", "BudgetSnapshot", model.BudgetSnapshot{}},
		{"model.Capabilities", "ProjectCapabilities", model.Capabilities{}},
		{"getBatchDetail", "BatchDetail", getBatchDetail{}},
		{"getBatchDetail.batch", "BatchSummary", getBatchDetail{}.Batch},
		{"batchFailureView", "BatchFailure", batchFailureView{}},
		{"sampleSummary", "SampleSummary", sampleSummary{}},
		{"sampleVersionView", "SampleVersionView", sampleVersionView{}},
		{"sampleVersionView.source", "SampleVersionSource", sampleVersionView{}.Source},
	}
}

// TestStudioTypeScriptMatchesGoSchema 断言 TS 类型与 Go JSON schema 逐字段一致。
func TestStudioTypeScriptMatchesGoSchema(t *testing.T) {
	source := readStudioTypeScript(t)

	for _, entry := range contractTypes() {
		t.Run(entry.name, func(t *testing.T) {
			goFields := jsonFieldNames(t, entry.typ)
			tsFields := tsInterfaceFields(t, source, entry.ts)
			if len(tsFields) == 0 {
				t.Fatalf("studio.ts 里找不到 interface/type %q（契约 §6 要求类型与该 schema 一致）", entry.ts)
			}
			if missing := difference(goFields, tsFields); len(missing) > 0 {
				t.Fatalf("%s 在 studio.ts 的 %s 里缺字段：%v\n（前端类型落后于后端 schema）",
					entry.name, entry.ts, missing)
			}
			if extra := difference(tsFields, goFields); len(extra) > 0 {
				t.Fatalf("studio.ts 的 %s 声明了后端 %s 不存在的字段：%v\n（前端读一个永远不会出现的字段会静默得到 undefined）",
					entry.ts, entry.name, extra)
			}
		})
	}
}

// TestStudioErrorEnvelopeIsNested 断言错误响应是契约 §1.2 的嵌套形状。
//
// T02 初版把错误体写成了扁平（顶层直接是 code/message），而 §1.2 要求
// `{"error": {...}}`。这个断言把「哪天又被改回扁平」变成测试失败。
func TestStudioErrorEnvelopeIsNested(t *testing.T) {
	fields := jsonFieldNames(t, apiErrorResponse{})
	if len(fields) != 1 || fields[0] != "error" {
		t.Fatalf("错误响应顶层必须只有 error 一个键（§1.2），实际 %v", fields)
	}

	// 错误体自身的字段必须齐全：少任何一个都会让前端无法区分
	// 「冲突」与「校验失败」，于是只能按状态码猜。
	bodyFields := jsonFieldNames(t, studio.ErrorBody{})
	for _, required := range []string{"code", "message", "requestId", "retryable"} {
		if !slices.Contains(bodyFields, required) {
			t.Fatalf("错误体缺少契约 §1.2 的字段 %q，实际 %v", required, bodyFields)
		}
	}
}

// TestStudioRoutesAreRegistered 断言 Atelier 路由都挂上了。
//
// 用「真实请求不是 404」而不是查注册表：`http.ServeMux` 不暴露 pattern，
// 而契约 §6 恰恰要求「未知子资源返回 404」。行为断言是唯一能守住它的方式。
//
// 未登录时 401 才算「路由存在」：404 说明路由没挂上（界面表现为
// 「打开页面就提示资源不存在」），而 500 说明 handler 在鉴权前就崩了。
func TestStudioRoutesAreRegistered(t *testing.T) {
	app, handler := sharedRoutedApplication(t)

	paths := []struct {
		method string
		path   string
	}{
		{http.MethodGet, "/api/v1/projects/p_1/overview"},
		{http.MethodGet, "/api/v1/projects/p_1/batches"},
		{http.MethodPost, "/api/v1/projects/p_1/batches"},
		{http.MethodGet, "/api/v1/projects/p_1/batches/b_1"},
		{http.MethodGet, "/api/v1/projects/p_1/batches/b_1/items"},
		{http.MethodGet, "/api/v1/projects/p_1/batches/b_1/events"},
		{http.MethodGet, "/api/v1/projects/p_1/batches/b_1/failures"},
		{http.MethodPost, "/api/v1/projects/p_1/batches/b_1/pause"},
		{http.MethodPost, "/api/v1/projects/p_1/batches/b_1/resume"},
		{http.MethodPost, "/api/v1/projects/p_1/batches/b_1/retry-failed"},
		{http.MethodGet, "/api/v1/projects/p_1/samples"},
		{http.MethodGet, "/api/v1/projects/p_1/samples/s_1"},
		{http.MethodGet, "/api/v1/projects/p_1/samples/s_1/history"},
		{http.MethodGet, "/api/v1/projects/p_1/samples/s_1/versions/1"},
	}
	for _, target := range paths {
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, httptest.NewRequest(target.method, target.path, nil))
		switch recorder.Code {
		case http.StatusNotFound:
			t.Fatalf("路由 %s %s 未注册（返回 404）", target.method, target.path)
		case http.StatusUnauthorized:
			// 期望：未登录先被中间件拦下，不泄漏资源是否存在。
		default:
			t.Fatalf("未登录访问 %s %s 必须返回 401，实际 %d", target.method, target.path, recorder.Code)
		}
	}

	// 未知子资源必须是 404（契约 §6）。带会话才观察得到：
	// 匿名请求先返回 401 是**有意**的顺序，不泄露资源存在性。
	cookie := mustSessionCookie(t, app)
	unknown := []struct {
		method string
		path   string
	}{
		{http.MethodGet, "/api/v1/projects/p_1/batches/b_1/unknown-subresource"},
		{http.MethodGet, "/api/v1/projects/p_1/samples/s_1/unknown-subresource"},
		{http.MethodGet, "/api/v1/projects/p_1/unknown-subresource"},
		{http.MethodPost, "/api/v1/projects/p_1/batches/b_1/unknown-action"},
	}
	for _, target := range unknown {
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(target.method, target.path, nil)
		request.AddCookie(cookie)
		handler.ServeHTTP(recorder, request)
		if recorder.Code != http.StatusNotFound {
			t.Fatalf("已登录用户访问未知子资源 %s 必须 404，实际 %d", target.path, recorder.Code)
		}
	}
}

// TestStudioPaginatedResponseShape 断言分页响应形状与契约 §1.5 一致。
func TestStudioPaginatedResponseShape(t *testing.T) {
	page := studio.NewPage([]studio.BatchSummary{
		{BatchID: 1, CreatedAt: "2026-09-21T10:00:00Z"},
		{BatchID: 2, CreatedAt: "2026-09-21T09:00:00Z"},
		{BatchID: 3, CreatedAt: "2026-09-21T08:00:00Z"},
	}, 2, "createdAt:desc", func(item studio.BatchSummary) studio.Cursor {
		return studio.Cursor{ID: item.BatchID}
	})

	if len(page.Items) != 2 {
		t.Fatalf("limit=2 时必须恰好返回 2 条（多取的那条不返回），实际 %d", len(page.Items))
	}
	if page.NextCursor == "" {
		t.Fatal("还有下一页时必须给出 nextCursor")
	}
	if page.SortKey != "createdAt:desc" {
		t.Fatalf("sortKey 必须是游标比较所用的排序键，实际 %q", page.SortKey)
	}

	// 游标必须能往返：翻页的下一页位置由它决定，解析失败会让翻页从头开始。
	cursor, err := studio.DecodeCursor(page.NextCursor)
	if err != nil {
		t.Fatalf("游标必须能解析：%v", err)
	}
	if cursor.ID != 2 {
		t.Fatalf("游标必须指向最后一条的 id（2），实际 %d", cursor.ID)
	}

	// 正好取满时不得给出 nextCursor（否则前端会多发一次空请求）。
	exact := studio.NewPage([]studio.BatchSummary{{BatchID: 1}, {BatchID: 2}}, 2, "createdAt:desc",
		func(item studio.BatchSummary) studio.Cursor { return studio.Cursor{ID: item.BatchID} })
	if exact.NextCursor != "" {
		t.Fatalf("恰好取满时必须没有下一页，实际 %q", exact.NextCursor)
	}

	// 坏游标必须报校验错误，而不是静默从头开始（那会让列表无限循环拉第一页）。
	if _, err := studio.DecodeCursor("not-a-cursor"); err == nil {
		t.Fatal("坏游标必须报错")
	}
}

// TestStudioListQueryRejectsUnsupportedFilters 断言 samples 端点的未接入筛选
// 是**显式拒绝**而不是静默忽略。
//
// 静默忽略会让用户以为自己看到的是筛过的结果 —— 而「列表看起来正常」
// 让这种错误完全不可见。契约 §3 列出了 status/risk，但判据来自 T16/T17 的表。
func TestStudioListQueryRejectsUnsupportedFilters(t *testing.T) {
	app := &application{}
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/api/v1/projects/p_1/samples?status=accepted", nil)
	app.listSamples(recorder, request)
	// 未登录先返回 401；这里断言的是「不返回 200 一个看起来很正常的空列表」。
	if recorder.Code == http.StatusOK {
		t.Fatal("未登录的请求不可能成功")
	}
}

// TestStudioBudgetExhaustedMapsTo429 断言预算失败映射为契约 §1.2 的 429。
func TestStudioBudgetExhaustedMapsTo429(t *testing.T) {
	app := &application{}
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/v1/projects/p_1/batches", nil)
	app.writeStudioError(recorder, request, store.ErrBudgetExhausted)

	if recorder.Code != http.StatusTooManyRequests {
		t.Fatalf("预算耗尽必须返回 429，实际 %d", recorder.Code)
	}
	var body apiErrorResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("错误响应必须是 JSON: %v", err)
	}
	if body.Error.Code != studio.CodeBudget {
		t.Fatalf("错误码必须是 %q，实际 %q", studio.CodeBudget, body.Error.Code)
	}
	if !body.Error.Retryable {
		t.Fatal("预算耗尽必须 retryable=true：提高上限后同一个命令就能成功")
	}
	if body.Error.RequestID == "" {
		t.Fatal("错误必须带 requestId（§1.2）")
	}
}

// TestStudioErrorsAreChineseAndLeakFree 断言新端点的错误文案是中文且不含内部细节。
//
// 与 write_error_test.go 的既有约定一致（issue #102/#109）：用户看到的是
// 可操作的中文，原始 SQL / 密钥 / 英文内部文案不上界面。
func TestStudioErrorsAreChineseAndLeakFree(t *testing.T) {
	app := &application{}
	cases := []struct {
		name string
		err  error
	}{
		{"未找到", studio.NewError(studio.CodeNotFound, "未找到该项目，请返回项目列表确认它是否已被删除")},
		{"版本冲突", studio.NewError(studio.CodeRevisionStale, "版本已变化，请重新加载后再保存")},
		{"无权限", studio.NewError(studio.CodeForbidden, "你的角色不能执行该操作，请联系项目负责人")},
		{"预算", studio.NewError(studio.CodeBudget, "预算额度已用完")},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodGet, "/api/v1/projects/p_1", nil)
			app.writeStudioError(recorder, request, testCase.err)

			var body apiErrorResponse
			if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
				t.Fatalf("错误响应必须是 JSON: %v", err)
			}
			if !hasChinese(body.Error.Message) {
				t.Fatalf("用户可见文案必须是中文，实际 %q", body.Error.Message)
			}
			for _, leaked := range []string{"SELECT", "INSERT", "pgx", "sql", "password", "api_key", "secret"} {
				if strings.Contains(strings.ToLower(body.Error.Message), strings.ToLower(leaked)) {
					t.Fatalf("错误文案不得包含内部细节 %q：%q", leaked, body.Error.Message)
				}
			}
			if body.Error.RequestID == "" {
				t.Fatal("错误必须带 requestId")
			}
		})
	}
}

// ---------------------------------------------------------------------------
// 辅助
// ---------------------------------------------------------------------------

var (
	routedOnce  sync.Once
	routedApp   *application
	routedMux   http.Handler
	routedPanic any
)

// sharedRoutedApplication 返回「已注册全部路由」的应用与 handler。
//
// 为什么必须共享（而不是每个测试各注册一次）：`applyRouteRegistrars` 会把
// `RegisterDatasetRouter` 注册的段写进**包级** map，重复应用会 panic ——
// 那是防止两个 lane 静默覆盖彼此路由的保护。多个测试都需要一个注册完整的
// mux，因此收敛到一个 sync.Once；每个测试只新建自己的请求。
func sharedRoutedApplication(t *testing.T) (*application, http.Handler) {
	t.Helper()
	routedOnce.Do(func() {
		app := &application{}
		mux := http.NewServeMux()
		func() {
			defer func() { routedPanic = recover() }()
			applyRouteRegistrars(mux, app)
		}()
		routedApp = app
		routedMux = app.middleware(mux)
	})
	if routedPanic != nil {
		t.Fatalf("注册路由时 panic（重复注册或与其他 lane 的前缀冲突）: %v", routedPanic)
	}
	return routedApp, routedMux
}

// readStudioTypeScript 读取前端类型文件。
func readStudioTypeScript(t *testing.T) string {
	t.Helper()
	// 测试的工作目录是 apps/api，因此先回到仓库根。
	candidates := []string{
		filepath.Join("..", studioTypeScriptPath),
		studioTypeScriptPath,
	}
	for _, candidate := range candidates {
		raw, err := os.ReadFile(candidate)
		if err == nil {
			return string(raw)
		}
	}
	t.Fatalf("找不到前端类型文件 %s（契约 §6 指定了它的位置）", studioTypeScriptPath)
	return ""
}

// jsonFieldNames 用反射读出结构体的 JSON 字段名。
//
// 忽略 `json:"-"`；保留 `omitempty` 的字段（它仍会出现，只是可能缺省）。
func jsonFieldNames(t *testing.T, value any) []string {
	t.Helper()
	typ := reflect.TypeOf(value)
	for typ.Kind() == reflect.Pointer {
		typ = typ.Elem()
	}
	if typ.Kind() != reflect.Struct {
		t.Fatalf("契约测试只支持结构体，实际 %s", typ.Kind())
	}
	names := make([]string, 0, typ.NumField())
	for index := 0; index < typ.NumField(); index++ {
		field := typ.Field(index)
		if !field.IsExported() {
			continue
		}
		// 匿名字段（内嵌结构）要摊平：`struct{ studio.ProjectOverview; Stats ... }`
		// 在 JSON 里是提升的，前端类型也是平的。
		tag := field.Tag.Get("json")
		name := strings.Split(tag, ",")[0]
		if name == "-" {
			continue
		}
		if field.Anonymous {
			names = append(names, jsonFieldNames(t, reflect.New(field.Type).Elem().Interface())...)
			continue
		}
		if name == "" {
			name = field.Name
		}
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// tsInterfaceFields 解析 TS 文件里某个 interface/type 的顶层字段名。
//
// 只支持扁平声明（`field: type` / `field?: type`），因为这正是本契约的用法：
// 嵌套对象引用另一个具名类型，从而让「字段集合」这件事可以被逐类型比对。
// 解析器刻意保持简单 —— 一个能理解全部 TS 语法的解析器无法在 Go 测试里维护。
func tsInterfaceFields(t *testing.T, source, name string) []string {
	t.Helper()
	header := regexp.MustCompile(`export (?:interface|type) ` + regexp.QuoteMeta(name) + `\b[^{]*\{`)
	locator := header.FindStringIndex(source)
	if locator == nil {
		return nil
	}
	body := source[locator[1]:]
	depth := 1
	end := -1
	for index, char := range body {
		switch char {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				end = index
			}
		}
		if end >= 0 {
			break
		}
	}
	if end < 0 {
		t.Fatalf("TS 类型 %s 的花括号不平衡", name)
	}
	body = body[:end]

	fields := []string{}
	// 逐行取顶层字段：嵌套对象在自己的行上（`  nested: {`），
	// 因此用「行首标识符 + 冒号」判定即可，而不必真正解析嵌套。
	// 嵌套块的行以缩进更多开头，靠 `depth` 跟踪跳过。
	lineDepth := 0
	for _, line := range strings.Split(body, "\n") {
		trimmed := strings.TrimSpace(line)
		if lineDepth > 0 {
			lineDepth += strings.Count(trimmed, "{") - strings.Count(trimmed, "}")
			continue
		}
		if trimmed == "" || strings.HasPrefix(trimmed, "//") || strings.HasPrefix(trimmed, "/*") ||
			strings.HasPrefix(trimmed, "*") {
			continue
		}
		lineDepth += strings.Count(trimmed, "{") - strings.Count(trimmed, "}")
		match := regexp.MustCompile(`^([A-Za-z_][A-Za-z0-9_]*)\??\s*:`).FindStringSubmatch(trimmed)
		if match == nil {
			continue
		}
		fields = append(fields, match[1])
	}
	sort.Strings(fields)
	return fields
}

// difference 返回 a 中不在 b 里的元素。
func difference(a, b []string) []string {
	set := make(map[string]struct{}, len(b))
	for _, item := range b {
		set[item] = struct{}{}
	}
	result := []string{}
	for _, item := range a {
		if _, exists := set[item]; !exists {
			result = append(result, item)
		}
	}
	return result
}
