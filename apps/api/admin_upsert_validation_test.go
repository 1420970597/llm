package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// issue #63：4 个 admin 配置 upsert 端点原先零校验 —— 空 body 直接落库并返回 200。
//
// 本文件覆盖「必须被拒」的全部形状，并且**真实调用 HTTP handler**（不是孤立地测
// 一个校验函数）：每个用例都构造真实 *http.Request，走 upsertProvider /
// upsertStorageProfile / upsertStrategy / upsertPrompt 的完整入口。
//
// 为什么这些用例不需要数据库：
// 校验是 upsert 的第一条语句，早于加密与任何 s.db 访问。因此 adminUpsertCase 用
// `&application{}`（store 为 nil）构造 handler 也安全 —— 拒绝路径在触达 store 字段
// 之前就返回了。若将来有人把校验挪到写库之后，这些用例会立刻 panic 而不是静默通过。
//
// 「合法 body -> 200」需要真实落库，放在同包的 admin_upsert_persist_test.go，
// 未设置 LLM_TEST_POSTGRES_DSN 时跳过。

// adminUpsertCase 是「应被拒绝」的单表用例。
type adminUpsertCase struct {
	name string
	// path 是端点路径，用于生成可读的用例名。
	path string
	// handler 是被测的真实 handler。
	handler func(app *application, w http.ResponseWriter, r *http.Request)
	// body 是请求体。
	body string
	// wantJSONKey 必须出现在 error 文案里，证明错误指向了具体字段
	// （issue 验收标准要求「返回 400 + 明确字段名」）。
	wantJSONKey string
}

// provider 端点：name / baseUrl / model 全空是 issue #63 记录的第一条实测证据
// （落库行 `2||||t`）。
var providerValidationCases = []adminUpsertCase{
	{
		name:        "空 body",
		path:        "/api/v1/admin/providers",
		handler:     (*application).upsertProvider,
		body:        `{}`,
		wantJSONKey: "name",
	},
	{
		name:        "只缺服务名称",
		path:        "/api/v1/admin/providers",
		handler:     (*application).upsertProvider,
		body:        `{"baseUrl":"http://example.invalid/v1","model":"m","maxConcurrency":4,"timeoutSeconds":120}`,
		wantJSONKey: "name",
	},
	{
		name:        "只缺基础 URL",
		path:        "/api/v1/admin/providers",
		handler:     (*application).upsertProvider,
		body:        `{"name":"n","model":"m","maxConcurrency":4,"timeoutSeconds":120}`,
		wantJSONKey: "baseUrl",
	},
	{
		name:        "基础 URL 不是完整地址",
		path:        "/api/v1/admin/providers",
		handler:     (*application).upsertProvider,
		body:        `{"name":"n","baseUrl":"example.invalid/v1","model":"m","maxConcurrency":4,"timeoutSeconds":120}`,
		wantJSONKey: "baseUrl",
	},
	{
		name:        "只缺模型名称",
		path:        "/api/v1/admin/providers",
		handler:     (*application).upsertProvider,
		body:        `{"name":"n","baseUrl":"http://example.invalid/v1","maxConcurrency":4,"timeoutSeconds":120}`,
		wantJSONKey: "model",
	},
	{
		name:        "最大并发数为 0",
		path:        "/api/v1/admin/providers",
		handler:     (*application).upsertProvider,
		body:        `{"name":"n","baseUrl":"http://example.invalid/v1","model":"m","maxConcurrency":0,"timeoutSeconds":120}`,
		wantJSONKey: "maxConcurrency",
	},
	{
		name:        "超时秒数为 0",
		path:        "/api/v1/admin/providers",
		handler:     (*application).upsertProvider,
		body:        `{"name":"n","baseUrl":"http://example.invalid/v1","model":"m","maxConcurrency":4,"timeoutSeconds":0}`,
		wantJSONKey: "timeoutSeconds",
	},
}

// storage 端点：issue #63 观察到前端预填了合理默认值，因此这条端点「空表单」不会出现。
// 但服务端仍必须挡住缺 name/provider/endpoint/bucket 与非法 endpoint —— 否则任何脚本
// 或其他调用方仍能写出脏行。
var storageValidationCases = []adminUpsertCase{
	{
		name:        "空 body",
		path:        "/api/v1/admin/storage-profiles",
		handler:     (*application).upsertStorageProfile,
		body:        `{}`,
		wantJSONKey: "name",
	},
	{
		name:        "只缺提供方",
		path:        "/api/v1/admin/storage-profiles",
		handler:     (*application).upsertStorageProfile,
		body:        `{"name":"n","endpoint":"http://minio:9000","bucket":"b"}`,
		wantJSONKey: "provider",
	},
	{
		name:        "只缺端点",
		path:        "/api/v1/admin/storage-profiles",
		handler:     (*application).upsertStorageProfile,
		body:        `{"name":"n","provider":"minio","bucket":"b"}`,
		wantJSONKey: "endpoint",
	},
	{
		name:        "端点不是完整地址",
		path:        "/api/v1/admin/storage-profiles",
		handler:     (*application).upsertStorageProfile,
		body:        `{"name":"n","provider":"minio","endpoint":"minio:9000","bucket":"b"}`,
		wantJSONKey: "endpoint",
	},
	{
		name:        "只缺存储桶",
		path:        "/api/v1/admin/storage-profiles",
		handler:     (*application).upsertStorageProfile,
		body:        `{"name":"n","provider":"minio","endpoint":"http://minio:9000"}`,
		wantJSONKey: "bucket",
	},
}

// strategy 端点：issue #63 最危险的一条 —— 空表单写出
// `{"id":1,"name":"","domainCount":1000,"isDefault":true}`，把不可用策略设成了默认策略。
var strategyValidationCases = []adminUpsertCase{
	{
		name:        "空 body",
		path:        "/api/v1/admin/generation-strategies",
		handler:     (*application).upsertStrategy,
		body:        `{}`,
		wantJSONKey: "name",
	},
	{
		// 这是 issue #63 原文的实测 payload（name 为空 + domainCount:1000 + isDefault:true）。
		name:        "issue 实测的脏 payload（空名称 + domainCount 1000 + 默认策略）",
		path:        "/api/v1/admin/generation-strategies",
		handler:     (*application).upsertStrategy,
		body:        `{"name":"","domainCount":1000,"questionsPerDomain":10,"answerVariants":1,"rewardVariants":1,"planningMode":"balanced","isDefault":true}`,
		wantJSONKey: "name",
	},
	{
		name:        "领域数为 0",
		path:        "/api/v1/admin/generation-strategies",
		handler:     (*application).upsertStrategy,
		body:        `{"name":"n","domainCount":0,"questionsPerDomain":10,"answerVariants":1,"rewardVariants":1}`,
		wantJSONKey: "domainCount",
	},
	{
		name:        "领域数超过上限",
		path:        "/api/v1/admin/generation-strategies",
		handler:     (*application).upsertStrategy,
		body:        `{"name":"n","domainCount":1001,"questionsPerDomain":10,"answerVariants":1,"rewardVariants":1}`,
		wantJSONKey: "domainCount",
	},
	{
		name:        "每领域问题数为 0",
		path:        "/api/v1/admin/generation-strategies",
		handler:     (*application).upsertStrategy,
		body:        `{"name":"n","domainCount":10,"questionsPerDomain":0,"answerVariants":1,"rewardVariants":1}`,
		wantJSONKey: "questionsPerDomain",
	},
	{
		name:        "答案变体数为 0",
		path:        "/api/v1/admin/generation-strategies",
		handler:     (*application).upsertStrategy,
		body:        `{"name":"n","domainCount":10,"questionsPerDomain":10,"answerVariants":0,"rewardVariants":1}`,
		wantJSONKey: "answerVariants",
	},
	{
		name:        "奖励变体数为 0",
		path:        "/api/v1/admin/generation-strategies",
		handler:     (*application).upsertStrategy,
		body:        `{"name":"n","domainCount":10,"questionsPerDomain":10,"answerVariants":1,"rewardVariants":0}`,
		wantJSONKey: "rewardVariants",
	},
}

// prompt 端点：issue #63 记录空模板以 isActive:true 落库，会让生成阶段拿到空 prompt。
var promptValidationCases = []adminUpsertCase{
	{
		name:        "空 body",
		path:        "/api/v1/admin/prompts",
		handler:     (*application).upsertPrompt,
		body:        `{}`,
		wantJSONKey: "name",
	},
	{
		name:        "只缺阶段",
		path:        "/api/v1/admin/prompts",
		handler:     (*application).upsertPrompt,
		body:        `{"name":"n","systemPrompt":"系统指令"}`,
		wantJSONKey: "stage",
	},
	{
		// stage 写错会让模板永远不会被 GetActivePromptByStage 读到，
		// 用户以为已启用、实际生成阶段仍走内置提示词。
		name:        "阶段不在允许集合内",
		path:        "/api/v1/admin/prompts",
		handler:     (*application).upsertPrompt,
		body:        `{"name":"n","stage":"not-a-real-stage","systemPrompt":"系统指令"}`,
		wantJSONKey: "stage",
	},
	{
		// issue #63：系统指令与用户指令都是空字符串、模板却是启用状态。
		name:        "系统指令与用户指令都为空",
		path:        "/api/v1/admin/prompts",
		handler:     (*application).upsertPrompt,
		body:        `{"name":"n","stage":"question-generation","version":"v1","systemPrompt":"","userPrompt":"","isActive":true}`,
		wantJSONKey: "systemPrompt/userPrompt",
	},
}

// 4 个端点 x {空 body, 缺必填字段, 非法取值} 全表。
func allAdminUpsertRejectCases() []adminUpsertCase {
	cases := make([]adminUpsertCase, 0, 24)
	cases = append(cases, providerValidationCases...)
	cases = append(cases, storageValidationCases...)
	cases = append(cases, strategyValidationCases...)
	cases = append(cases, promptValidationCases...)
	return cases
}

// 被拒绝的请求必须是 400 + 指向具体字段的中文提示，绝不能是 200（issue #63 的原始行为）。
func TestAdminUpsertRejectsInvalidInput(t *testing.T) {
	for _, tc := range allAdminUpsertRejectCases() {
		t.Run(tc.path+"/"+tc.name, func(t *testing.T) {
			app := &application{}
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, tc.path, strings.NewReader(tc.body))

			tc.handler(app, rec, req)

			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want %d (body=%q)", rec.Code, http.StatusBadRequest, rec.Body.String())
			}
			body := decodeErrorBody(t, rec)
			if body == "" {
				t.Fatal("error 文案为空，必须带上具体字段名")
			}
			if !strings.Contains(body, tc.wantJSONKey) {
				t.Errorf("error = %q, want it to name the offending field %q", body, tc.wantJSONKey)
			}
			// 400 必须原样透出中文业务提示（writeError 约定），不能是英文兜底文案。
			if body == "internal server error" {
				t.Error("body leaked the English fallback text")
			}
		})
	}
}

// PUT 与 POST 走同一个 handler（main.go 注册了两条路由），因此校验对更新路径同样生效。
// 这条用例显式锁住「不带 id 的 PUT 不会退回旧的无校验行为」。
func TestAdminUpsertRejectsInvalidInputOnPut(t *testing.T) {
	app := &application{}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPut, "/api/v1/admin/generation-strategies", strings.NewReader(`{}`))

	app.upsertStrategy(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
	if body := decodeErrorBody(t, rec); !strings.Contains(body, "name") {
		t.Errorf("error = %q, want it to name field name", body)
	}
}

// 校验必须发生在任何写库动作之前。若有人把校验挪到 store 调用之后，
// 这条用例会因为 nil store 被解引用而 panic（而不是静默通过）。
func TestAdminUpsertValidatesBeforeTouchingStore(t *testing.T) {
	app := &application{}
	if app.store != nil {
		t.Fatal("前置条件不成立：本用例要求 store 为 nil 才能证明校验早于写库")
	}

	for _, tc := range allAdminUpsertRejectCases() {
		t.Run(tc.path+"/"+tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, tc.path, strings.NewReader(tc.body))
			tc.handler(app, rec, req)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want %d", rec.Code, http.StatusBadRequest)
			}
		})
	}
}

// 非法 JSON 仍是 400（原有行为不变），但文案来自解码错误而不是字段校验。
func TestAdminUpsertStillRejectsMalformedJSON(t *testing.T) {
	app := &application{}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/providers", strings.NewReader(`{`))

	app.upsertProvider(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
}

// 响应形状必须保持 {"error": "..."}（前端拦截器按 error.response.data.error 取文案）。
func TestAdminUpsertErrorShapeUnchanged(t *testing.T) {
	app := &application{}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/providers", strings.NewReader(`{}`))

	app.upsertProvider(rec, req)

	var payload map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("response body is not a JSON object: %v (body=%q)", err, rec.Body.String())
	}
	if len(payload) != 1 || payload["error"] == "" {
		t.Fatalf("payload = %v, want exactly one non-empty key \"error\"", payload)
	}
}
