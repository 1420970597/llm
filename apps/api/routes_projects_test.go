package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	appcrypto "github.com/1420970597/llm/internal/crypto"
	"github.com/1420970597/llm/internal/model"
)

// 本文件覆盖 Issue #160 T02/T08 的**无数据库**契约断言：
// 路由注册、请求体校验、错误码形状与幂等摘要。
//
// 需要真实 Postgres 的端到端断言（分页、并发、事务回滚）在
// internal/store/project_store_test.go；本文件只跑内存，因此在任何 CI 上都会执行
// —— 这正是它存在的意义：契约回归不能因为「没设 DSN」而被 Skip 掉。

// TestSessionRoleIsRefreshedFromServer 是 T03 的核心安全断言：
// **登录时写入 cookie 的角色不得被长期信任**。
//
// 场景：管理员登录（cookie 里 role=admin），随后在库里被降级为 user。
// 若中间件直接用 cookie 里的副本，旧 cookie 仍能通过 /api/v1/admin/* 检查 ——
// 那就是一个最长 24 小时的权限提升窗口。
//
// 断言两条分支：
//  1. 服务端当前角色是 user → 即使 cookie 写着 admin，管理员接口也必须 403；
//  2. 服务端读不到身份（账号被删 / 库不可用）→ 会话整个失效（401），
//     而不是「继续信任旧角色」（fail closed；数据库故障时不能把权限全放开）。
func TestSessionRoleIsRefreshedFromServer(t *testing.T) {
	app := &application{}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/admin/dashboard", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	handler := app.middleware(mux)

	box, err := appcrypto.NewSecretBox("phase1-dev-only-32-byte-secret!!!")
	if err != nil {
		t.Fatalf("secret box: %v", err)
	}
	app.box = box

	// cookie 里写的是 admin（登录时的副本）。
	cookie, err := app.createSessionCookie(model.User{ID: 7, Email: "admin@example.test", Role: "admin"})
	if err != nil {
		t.Fatalf("create session cookie: %v", err)
	}

	// 分支 1：服务端当前角色已降级为 user。
	app.sessionUsers = func(context.Context, int64) (model.User, error) {
		return model.User{ID: 7, Email: "admin@example.test", Role: "user"}, nil
	}
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/api/v1/admin/dashboard", nil)
	request.AddCookie(cookie)
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("服务端已降级时旧 cookie 必须失效（403），实际 %d —— 说明仍在使用 cookie 里的角色副本", recorder.Code)
	}

	// 分支 2：服务端读不到身份。
	app.sessionUsers = func(context.Context, int64) (model.User, error) {
		return model.User{}, errors.New("no rows in result set")
	}
	recorder = httptest.NewRecorder()
	request = httptest.NewRequest(http.MethodGet, "/api/v1/admin/dashboard", nil)
	request.AddCookie(cookie)
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("无法确认服务端身份时必须丢弃会话（401，fail closed），实际 %d", recorder.Code)
	}

	// 反证：服务端当前角色确实是 admin 时必须放行，否则上面的断言可能只是
	// 「所有请求都被拒」的假阳性。
	app.sessionUsers = func(context.Context, int64) (model.User, error) {
		return model.User{ID: 7, Email: "admin@example.test", Role: "admin"}, nil
	}
	recorder = httptest.NewRecorder()
	request = httptest.NewRequest(http.MethodGet, "/api/v1/admin/dashboard", nil)
	request.AddCookie(cookie)
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("服务端角色仍为 admin 时必须放行，实际 %d", recorder.Code)
	}
}

// TestProjectRoutesRegistered 断言项目 API 真正可达，且注册时不 panic。
//
// 为什么用「发一个真实请求并断言不是 404」而不是直接在 mux 上查注册表：
// `http.ServeMux` 不暴露已注册的 pattern，而契约 §6 要求「未知子资源 404」。
// 因此**行为断言**（已注册 → 401/200；未注册 → 404）才是唯一能真正守住契约的方式。
//
// 注意：`registerProjectRoutes` 已由本包的 `init()` 注册到全局注册表，
// 测试只调用 `applyRouteRegistrars`。同时调用两者会重复注册同一 pattern，
// Go 1.22+ ServeMux 会 panic —— 那正是本测试要排除的失败形态。
func TestProjectRoutesRegistered(t *testing.T) {
	// 共享一次注册结果：applyRouteRegistrars 会写包级 map，重复应用会 panic
	//（那是防两个 lane 静默覆盖彼此路由的保护，见 sharedRoutedApplication）。
	app, handler := sharedRoutedApplication(t)

	// 已注册的路由必须可达：未登录时由中间件（或 handler）返回 401，而不是 404。
	// 404 会说明路由根本没挂上，而这种情况在界面上表现为「打开页面就提示资源不存在」。
	for _, path := range []string{projectPrefix, projectPrefix + "/p_1", projectPrefix + "/p_1/members"} {
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, path, nil))
		if recorder.Code == http.StatusNotFound {
			t.Fatalf("路由 %s 未注册（返回 404）", path)
		}
		if recorder.Code != http.StatusUnauthorized {
			t.Fatalf("未登录访问 %s 必须返回 401，实际 %d", path, recorder.Code)
		}
	}

	// 未注册的子资源必须是 404，不能静默回落到某个读模型（既有 bug 的形态，issue #8）。
	//
	// 这里必须**带会话**才看得到 404：未登录时中间件先返回 401，这是有意的顺序
	// —— 对匿名请求不泄露「这个资源存不存在」。因此匿名请求只能断言 401，
	// 而「未知子资源 404」是已认证用户才观察得到的契约。
	cookie := mustSessionCookie(t, app)
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, projectPrefix+"/p_1/unknown-subresource", nil)
	request.AddCookie(cookie)
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("未知子资源对已登录用户必须返回 404，实际 %d", recorder.Code)
	}
}

// mustSessionCookie 造一个有效会话 cookie。
//
// 用途：让「已认证视角」的契约（未知子资源 404）可测。匿名视角的断言只是 401，
// 而 401 对「路由是否真的挂上」没有区分力 —— 两者必须都测。
//
// 同时注入 sessionUsers 假实现：T03 之后 cookie 里的用户不再被直接信任，
// 中间件会重新读一次服务端身份（那正是「撤权立即生效」的实现方式），
// 因此不注入的话这里会真的去查库并因为取不到而丢弃会话。
func mustSessionCookie(t *testing.T, app *application) *http.Cookie {
	t.Helper()
	box, err := appcrypto.NewSecretBox("phase1-dev-only-32-byte-secret!!!")
	if err != nil {
		t.Fatalf("secret box: %v", err)
	}
	app.box = box
	app.sessionUsers = func(context.Context, int64) (model.User, error) {
		return model.User{ID: 1, Email: "reader@example.test", Role: "user"}, nil
	}
	cookie, err := app.createSessionCookie(model.User{ID: 1, Email: "reader@example.test", Role: "user"})
	if err != nil {
		t.Fatalf("create session cookie: %v", err)
	}
	return cookie
}

// TestCreateProjectValidationRejectsAllInvalidFieldsAtOnce 断言校验一次报**全部**问题。
//
// 为什么这条重要：向导表单一次提交多个字段，只报第一个会让用户来回提交三、四次
// （W03–W05 验收项要求聚焦首个无效字段，而不是只暴露一个字段）。
func TestCreateProjectValidationRejectsAllInvalidFieldsAtOnce(t *testing.T) {
	input := model.CreateProjectInput{
		Name:       "",
		TargetKind: "sft",
		PilotSize:  101,
	}
	// 显式填 0：必须被当成非法值报 422，而不是被归一化成 1（契约 §2.1 domains ≥ 1）。
	zero := 0
	input.Coverage.Domains = &zero
	input.Coverage.DirectionsPerDomain = &zero
	input.Coverage.QuestionsPerDirection = &zero
	input.Budget.Currency = "USD"
	input.Budget.OnExhausted = "explode"
	target := 1.5
	input.Quality.AcceptanceRateTarget = &target

	input.Normalize()
	err := input.Validate()
	if err == nil {
		t.Fatal("非法输入必须校验失败")
	}
	fieldErrors, ok := model.HasFieldErrors(err)
	if !ok {
		t.Fatalf("必须是字段级错误（handler 据此返回 422 + fieldErrors），实际: %v", err)
	}

	fields := map[string]bool{}
	for _, item := range fieldErrors {
		fields[item.Field] = true
	}
	for _, want := range []string{
		"name", "pilotSize", "coverage.domains", "coverage.directionsPerDomain",
		"coverage.questionsPerDirection", "budget.currency", "budget.onExhausted",
		"quality.acceptanceRateTarget",
	} {
		if !fields[want] {
			t.Fatalf("字段 %q 的错误必须一次全部报出，实际只报了 %v", want, fieldErrors)
		}
	}
}

// TestCreateProjectNormalizeDoesNotMaskInvalidValues 断言默认值只处理「未提供」。
//
// 一个具体的陷阱：显式传 acceptanceRateTarget=2.0 若被「夹到 1.0」，
// 用户会以为自己填的目标生效了，而实际项目目标与输入不符。
func TestCreateProjectNormalizeDoesNotMaskInvalidValues(t *testing.T) {
	invalid := 2.0
	input := model.CreateProjectInput{Name: "x", TargetKind: "sft", PilotSize: 1}
	input.Quality.AcceptanceRateTarget = &invalid
	input.Normalize()
	if err := input.Validate(); err == nil {
		t.Fatal("acceptanceRateTarget=2.0 必须报错，不能被 Normalize 悄悄夹到 1.0")
	}

	zero := int64(0)
	noLimit := model.CreateProjectInput{Name: "y", TargetKind: "sft", PilotSize: 1}
	noLimit.Budget.LimitMinor = &zero
	noLimit.Normalize()
	if err := noLimit.Validate(); err != nil {
		t.Fatalf("limitMinor=0 表示「未设上限」，不该报错: %v", err)
	}

	tooSmall := int64(50)
	small := model.CreateProjectInput{Name: "z", TargetKind: "sft", PilotSize: 1}
	small.Budget.LimitMinor = &tooSmall
	small.Normalize()
	if err := small.Validate(); err == nil {
		t.Fatal("limitMinor=50（0.5 元）必须报错：预算下限是 1 元")
	}
}

// TestCreateProjectDefaultsMatchContract 断言契约 §2.1 的默认值。
func TestCreateProjectDefaultsMatchContract(t *testing.T) {
	input := model.CreateProjectInput{}
	input.Normalize()

	if input.TargetKind != model.TargetKindSFT {
		t.Fatalf("targetKind 默认必须是 sft，实际 %q", input.TargetKind)
	}
	domains, directionsPerDomain, questionsPerDirection := input.CoverageValues()
	if input.PilotSize != 1 || domains != 1 || directionsPerDomain != 1 || questionsPerDirection != 1 {
		t.Fatalf("n/m/x 与 pilotSize 未提供时应为 1，实际 %+v", input)
	}
	if input.Budget.Currency != "CNY" {
		t.Fatalf("币种默认必须是 CNY（§2.4 不隐式假设），实际 %q", input.Budget.Currency)
	}
	if input.Budget.OnExhausted != model.BudgetOnExhaustedPause {
		t.Fatalf("预算耗尽默认必须是 pause，实际 %q", input.Budget.OnExhausted)
	}
	if got := input.AcceptanceTargetValue(); got != 0.85 {
		t.Fatalf("接纳率目标默认必须是 0.85，实际 %v", got)
	}
}

// TestDigestCreateProjectIgnoresIrrelevantShape 断言幂等摘要只看语义字段。
//
// 反例说明为什么需要它：若摘要直接哈希原始 body，那么前端「先发一次带
// workspaceId 的请求、再发一次不带」会被判定为「同键不同请求」并返回 409，
// 而用户做的事其实是同一件。
func TestDigestCreateProjectIgnoresIrrelevantShape(t *testing.T) {
	first := model.CreateProjectInput{Name: "冷链", Goal: "g", TargetKind: "sft", PilotSize: 3}
	second := model.CreateProjectInput{Name: "冷链", Goal: "g", TargetKind: "sft", PilotSize: 3}
	// SourceRecipeVersionId 不参与创建摘要：它不改变「创建一个新项目」这件事的语义，
	// 且显式 null 与省略必须等价。
	recipeID := int64(7)
	first.SourceRecipeVersionID = &recipeID

	if digestCreateProject(first) != digestCreateProject(second) {
		t.Fatal("语义相同的请求必须得到同一摘要（否则重试会被误判为「不同请求」并返回 409）")
	}

	different := second
	different.PilotSize = 4
	if digestCreateProject(first) == digestCreateProject(different) {
		t.Fatal("语义不同的请求必须得到不同摘要（否则「同键不同请求」不会被拦下）")
	}
}

// TestParseProjectIDAcceptsBothForms 断言 `p_12` 与 `12` 都能解析。
//
// 同时接受两种形态是有意的：URL 用 `p_12` 以示与其他对象 ID 不同，
// 而手工调试/既有脚本常直接写数字。非法输入必须报错而不是被当成 0 号项目。
func TestParseProjectIDAcceptsBothForms(t *testing.T) {
	for _, raw := range []string{"p_12", "12"} {
		id, err := parseProjectID(raw)
		if err != nil || id != 12 {
			t.Fatalf("parseProjectID(%q) 应当得到 12，实际 id=%d err=%v", raw, id, err)
		}
	}
	for _, raw := range []string{"", "p_", "0", "-1", "abc", "p_abc"} {
		if _, err := parseProjectID(raw); err == nil {
			t.Fatalf("非法项目 ID %q 必须报错（静默当成 0 号项目会写到别的对象）", raw)
		}
	}
}

// TestProjectEnvelopeCarriesContractFields 断言契约 §1.1 的稳定外壳字段齐全。
func TestProjectEnvelopeCarriesContractFields(t *testing.T) {
	app := &application{}
	project := model.Project{
		ID: 7, WorkspaceID: 1, Name: "冷链问答", TargetKind: "sft",
		Status: model.ProjectStatusDraft, RowVersion: 3,
	}
	envelope := app.projectEnvelope(project, model.ProjectRoleOwner)

	if envelope.ID != "p_7" {
		t.Fatalf("稳定 ID 必须是 p_7，实际 %q", envelope.ID)
	}
	if envelope.Revision != 3 || envelope.Status != "draft" {
		t.Fatalf("envelope 必须带 revision/status，实际 %+v", envelope)
	}
	if !envelope.Capabilities.CanEdit || !envelope.Capabilities.CanPublish {
		t.Fatalf("owner 在 draft 状态必须能设计与发布，实际 %+v", envelope.Capabilities)
	}
	if envelope.Links["overview"] == "" || envelope.Links["self"] == "" {
		t.Fatalf("envelope 必须给出可跳转链接（前端不自行拼 URL），实际 %+v", envelope.Links)
	}
	if envelope.Warnings == nil {
		t.Fatal("warnings 必须是 [] 而不是 null：前端按数组渲染")
	}
	// 稳定 ID 与展示名分离（§2.5）：项目侧同样如此，ID 不是名字。
	if strings.Contains(envelope.Links["self"], project.Name) {
		t.Fatal("链接必须用稳定 ID 而不是名称")
	}
}

// TestProjectCapabilitiesByRole 断言契约 §1.6 的角色能力矩阵。
func TestProjectCapabilitiesByRole(t *testing.T) {
	owner := model.ProjectCapabilities(model.ProjectRoleOwner, model.ProjectStatusDraft)
	reviewer := model.ProjectCapabilities(model.ProjectRoleReviewer, model.ProjectStatusDraft)
	viewer := model.ProjectCapabilities(model.ProjectRoleViewer, model.ProjectStatusDraft)

	if !owner.CanEdit || !owner.CanRun || !owner.CanReview || !owner.CanPublish || !owner.CanManageMembers {
		t.Fatalf("owner 必须能设计/运行/判断/发布/管成员，实际 %+v", owner)
	}
	if reviewer.CanEdit || reviewer.CanRun || reviewer.CanPublish {
		t.Fatalf("reviewer 不得设计/运行/发布，实际 %+v", reviewer)
	}
	if !reviewer.CanReview || !reviewer.CanDownload {
		t.Fatalf("reviewer 必须能判断与下载，实际 %+v", reviewer)
	}
	if viewer.CanEdit || viewer.CanRun || viewer.CanReview || viewer.CanPublish || viewer.CanManageMembers {
		t.Fatalf("viewer 必须是只读，实际 %+v", viewer)
	}
	if !viewer.CanDownload {
		t.Fatalf("viewer 必须能下载可访问的发布，实际 %+v", viewer)
	}

	// workspace admin 不在项目角色集合里：不应有任何项目内容能力。
	admin := model.ProjectCapabilities("admin", model.ProjectStatusDraft)
	if admin.CanEdit || admin.CanReview || admin.CanPublish {
		t.Fatalf("workspace admin 不默认拥有项目内容能力（§1.6），实际 %+v", admin)
	}

	// 归档只阻止新运行与发布，读/下载保持可用。
	archived := model.ProjectCapabilities(model.ProjectRoleOwner, model.ProjectStatusArchived)
	if archived.CanEdit || archived.CanRun || archived.CanPublish {
		t.Fatalf("归档项目必须禁止写与运行，实际 %+v", archived)
	}
	if !archived.CanDownload {
		t.Fatalf("归档项目必须仍可下载历史发布，实际 %+v", archived)
	}
}

// TestWriteAPIErrorShape 断言契约 §1.2 的错误响应字段。
//
// 形状是**嵌套**的（`{"error": {...}}`）：T02 初版写成了扁平，
// 而 §1.2 与 §6（「TS 类型与本节 schema 一致」）都要求嵌套 ——
// 扁平形状下前端拦截器只能把 `data.error` 当字符串，
// 新契约的错误码/字段错误/blockers 全部拿不到。T08 对齐了它。
func TestWriteAPIErrorShape(t *testing.T) {
	app := &application{}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/projects", nil)
	recorder := httptest.NewRecorder()

	app.writeAPIError(recorder, req, http.StatusUnprocessableEntity, codeValidation,
		"参数有误", []model.FieldError{{Field: "pilotSize", Message: "必须在 1–100 之间"}})

	if recorder.Code != http.StatusUnprocessableEntity {
		t.Fatalf("状态码必须是 422，实际 %d", recorder.Code)
	}
	var body apiErrorResponse
	if err := json.NewDecoder(bytes.NewReader(recorder.Body.Bytes())).Decode(&body); err != nil {
		t.Fatalf("错误响应必须是 JSON: %v", err)
	}
	if body.Error.Code != codeValidation {
		t.Fatalf("错误码必须是 %q，实际 %q", codeValidation, body.Error.Code)
	}
	if body.Error.RequestID == "" {
		t.Fatal("错误响应必须带 requestId（§1.2）：用户报错截图要能对上日志")
	}
	if len(body.Error.FieldErrors) != 1 || body.Error.FieldErrors[0].Field != "pilotSize" {
		t.Fatalf("fieldErrors 必须透出，实际 %+v", body.Error.FieldErrors)
	}
	if body.Error.Retryable {
		t.Fatal("422 是不可重试错误，retryable 必须为 false")
	}

	// 嵌套形状必须真的嵌套：顶层不得同时出现 code/message。
	var topLevel map[string]json.RawMessage
	if err := json.Unmarshal(recorder.Body.Bytes(), &topLevel); err != nil {
		t.Fatalf("错误响应必须是 JSON: %v", err)
	}
	if _, exists := topLevel["code"]; exists {
		t.Fatal("错误体必须嵌在 error 下（§1.2），不得把 code 提到顶层")
	}
	if _, exists := topLevel["error"]; !exists {
		t.Fatal("错误响应必须在 error 键下携带错误体")
	}

	// 5xx 才可重试。
	recorder = httptest.NewRecorder()
	app.writeAPIError(recorder, req, http.StatusServiceUnavailable, codeUnavailable, "依赖不可用", nil)
	_ = json.NewDecoder(bytes.NewReader(recorder.Body.Bytes())).Decode(&body)
	if !body.Error.Retryable {
		t.Fatal("503 必须标记 retryable=true")
	}
	if body.Error.Code != codeUnavailable {
		t.Fatalf("503 的错误码必须是 %q，实际 %q", codeUnavailable, body.Error.Code)
	}
}

// TestRequestIDUsesHeaderWhenPresent 断言网关/前端能自己种 requestId。
func TestRequestIDUsesHeaderWhenPresent(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	generated := requestID(req)
	if !strings.HasPrefix(generated, "req_") {
		t.Fatalf("没有请求头时必须现算一个 req_ 前缀的 ID，实际 %q", generated)
	}
	if again := requestID(req); again == generated {
		t.Fatal("现算的 requestId 必须每次不同（同毫秒两次请求必须可区分）")
	}

	req.Header.Set("X-Request-Id", "trace-abc")
	if got := requestID(req); got != "trace-abc" {
		t.Fatalf("必须复用请求头里的 ID，实际 %q", got)
	}

	// 超长请求头不被信任：它会被写进日志与响应，必须限长。
	req.Header.Set("X-Request-Id", strings.Repeat("x", 500))
	if got := requestID(req); got == strings.Repeat("x", 500) {
		t.Fatal("超长请求头不得原样采纳（会被写入日志与响应体）")
	}
}
