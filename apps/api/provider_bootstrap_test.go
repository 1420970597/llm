package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/1420970597/llm/internal/config"
	"github.com/1420970597/llm/internal/model"
	"github.com/1420970597/llm/internal/store"
)

// issue #63 的连带风险：新增的必填校验会让启动期 bootstrap 也走同一条 store 入口
// （EnsureProvider → UpsertProvider → store.ValidateProviderInput）。
//
// 如果启动期沿用「校验失败即 log.Fatalf」，那么一个漏填 APP_BOOTSTRAP_PROVIDER_MODEL
// 的部署会从「少写一个字段」直接变成「api 容器起不来」。本文件锁死：
//   - 未配置引导     → 静默跳过（既有行为不变）
//   - 配置不完整     → 告警 + 跳过引导，**不阻断启动**
//   - 配置完整       → 正常引导
//   - HTTP 入口对同一份配置仍然严格 400（两条路径的失败语义必须不同）

// 完整可用的 bootstrap 配置。
func completeBootstrapConfig() config.APIConfig {
	return config.APIConfig{
		BootstrapProviderName:           "l15-r6-bootstrap",
		BootstrapProviderBaseURL:        "http://example.invalid/v1",
		BootstrapProviderModel:          "l15-r6-model",
		BootstrapProviderAPIKey:         "sk-l15-r6-not-a-real-key",
		BootstrapProviderType:           "openai-compatible",
		BootstrapProviderTimeoutSeconds: 120,
		BootstrapProviderMaxConcurrency: 4,
	}
}

// 未配置引导（BASE_URL 为空）时静默跳过，不返回错误 —— 这是既有语义，不能被本轮改动破坏。
func TestBootstrapSkippedWhenUnconfigured(t *testing.T) {
	cases := []struct {
		name string
		mut  func(*config.APIConfig)
	}{
		{"BASE_URL 为空", func(c *config.APIConfig) { c.BootstrapProviderBaseURL = "" }},
		{"API_KEY 为空", func(c *config.APIConfig) { c.BootstrapProviderAPIKey = "" }},
		{"两者都为空", func(c *config.APIConfig) {
			c.BootstrapProviderBaseURL = ""
			c.BootstrapProviderAPIKey = ""
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := completeBootstrapConfig()
			tc.mut(&cfg)

			_, outcome, err := resolveBootstrapProvider(cfg)
			if outcome != bootstrapSkippedUnconfigured {
				t.Fatalf("outcome = %v, want bootstrapSkippedUnconfigured", outcome)
			}
			if err != nil {
				t.Fatalf("未配置引导不应返回错误（否则调用方会误判为异常）: %v", err)
			}
		})
	}
}

// 配置不完整必须降级为「告警 + 跳过」，绝不能返回会让 main() 走 log.Fatalf 的路径。
//
// 这条是本次修复的核心回归守卫：它断言 outcome 明确落在 skipped 分支，
// 而不是 ready 分支（ready 分支才允许调用 EnsureProvider）。
func TestBootstrapIncompleteConfigSkipsInsteadOfFailing(t *testing.T) {
	cases := []struct {
		name        string
		mut         func(*config.APIConfig)
		wantJSONKey string
	}{
		{
			// 真实场景：config.LoadAPIConfig 的 APP_BOOTSTRAP_PROVIDER_MODEL 默认值是空串，
			// 因此「只填了 BASE_URL 与 API_KEY」的部署会命中这里。
			name:        "缺 MODEL（config 默认即为空串）",
			mut:         func(c *config.APIConfig) { c.BootstrapProviderModel = "" },
			wantJSONKey: "model",
		},
		{
			name:        "缺 NAME",
			mut:         func(c *config.APIConfig) { c.BootstrapProviderName = "" },
			wantJSONKey: "name",
		},
		{
			name:        "BASE_URL 不是完整地址",
			mut:         func(c *config.APIConfig) { c.BootstrapProviderBaseURL = "example.invalid/v1" },
			wantJSONKey: "baseUrl",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := completeBootstrapConfig()
			tc.mut(&cfg)

			_, outcome, err := resolveBootstrapProvider(cfg)

			if outcome != bootstrapSkippedIncomplete {
				t.Fatalf("outcome = %v, want bootstrapSkippedIncomplete（不能是 ready，否则会拿无效配置去写库）", outcome)
			}
			if err == nil {
				t.Fatal("配置不完整必须返回错误，供调用方打印告警")
			}
			// 告警文案必须能指回具体字段，否则运维只看到「配置不完整」无从下手。
			if !strings.Contains(err.Error(), tc.wantJSONKey) {
				t.Errorf("error = %q, want it to name field %q", err.Error(), tc.wantJSONKey)
			}
			// 必须是 store 层的校验错误类型，证明两条路径共用同一套规则。
			var validationErr *store.ValidationError
			if !errors.As(err, &validationErr) {
				t.Errorf("error = %T (%v), want *store.ValidationError", err, err)
			}
		})
	}
}

// 配置完整时返回 ready 与可直接落库的输入，字段映射正确。
func TestBootstrapReadyWithCompleteConfig(t *testing.T) {
	cfg := completeBootstrapConfig()

	input, outcome, err := resolveBootstrapProvider(cfg)

	if outcome != bootstrapReady {
		t.Fatalf("outcome = %v, want bootstrapReady (err=%v)", outcome, err)
	}
	if err != nil {
		t.Fatalf("配置完整不应返回错误: %v", err)
	}
	if input.Name != cfg.BootstrapProviderName ||
		input.BaseURL != cfg.BootstrapProviderBaseURL ||
		input.Model != cfg.BootstrapProviderModel ||
		input.APIKey != cfg.BootstrapProviderAPIKey ||
		input.ProviderType != cfg.BootstrapProviderType ||
		input.MaxConcurrency != cfg.BootstrapProviderMaxConcurrency ||
		input.TimeoutSeconds != cfg.BootstrapProviderTimeoutSeconds {
		t.Fatalf("input = %+v, want every field mapped from config", input)
	}
	if !input.IsActive {
		t.Error("引导的 provider 必须是启用状态，否则任务选中后仍无法调用")
	}
}

// 关键区分：同一份「配置不完整」的输入，启动期跳过，而 HTTP 入口必须 400。
//
// 这两条断言放在同一个用例里，是为了防止将来有人「统一」两条路径的行为：
// 要么让启动期变回 fatal（部署崩），要么让 HTTP 变宽松（脏数据回来）。
func TestBootstrapAndHTTPDifferOnIncompleteConfig(t *testing.T) {
	cfg := completeBootstrapConfig()
	cfg.BootstrapProviderModel = "" // 只有 model 缺失

	// 启动期：降级为跳过，不阻断。
	if _, outcome, err := resolveBootstrapProvider(cfg); outcome != bootstrapSkippedIncomplete || err == nil {
		t.Fatalf("启动期 outcome = %v err = %v, want skipped + non-nil error", outcome, err)
	}

	// HTTP 入口：同一份配置必须被拒为 400，并指明 model 字段。
	app := &application{}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/providers",
		strings.NewReader(`{"name":"n","baseUrl":"http://example.invalid/v1","maxConcurrency":4,"timeoutSeconds":120}`))
	app.upsertProvider(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("HTTP status = %d, want 400", rec.Code)
	}
	var payload struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("response body is not JSON: %v (body=%q)", err, rec.Body.String())
	}
	if !strings.Contains(payload.Error, "model") {
		t.Errorf("HTTP error = %q, want it to name field model", payload.Error)
	}
}

// 校验规则本身必须接受一份完全合法的 provider 输入。
//
// 这条不依赖数据库：它证明「合法 body 不被拒」，与需要真实 Postgres 才能验证的
// 「合法 body 落库并返回 200」互补（后者见 admin_upsert_persist_test.go）。
func TestValidateProviderInputAcceptsValidInput(t *testing.T) {
	valid := model.ModelProvider{
		Name:           "n",
		BaseURL:        "https://api.example.invalid/v1",
		Model:          "m",
		MaxConcurrency: 1,
		TimeoutSeconds: 1,
	}
	if err := store.ValidateProviderInput(valid); err != nil {
		t.Fatalf("合法输入被拒绝: %v", err)
	}

	// https 与 http 都必须接受（本地 MinIO / 内网网关常为 http）。
	for _, scheme := range []string{"http", "https"} {
		input := valid
		input.BaseURL = scheme + "://host.invalid:8080/v1"
		if err := store.ValidateProviderInput(input); err != nil {
			t.Errorf("scheme %s 应被接受: %v", scheme, err)
		}
	}
}

// 4 个端点各自的「合法输入」都必须越过校验层（不依赖数据库）。
//
// 与拒绝用例对称：只测拒绝会漏掉「合法请求被误挡」这类回归，
// 而那会直接打断管理员的配置流程。
func TestValidateInputsAcceptValidBodies(t *testing.T) {
	if err := store.ValidateStorageProfileInput(model.StorageProfile{
		Name: "n", Provider: "minio", Endpoint: "http://minio:9000", Bucket: "b",
	}); err != nil {
		t.Errorf("合法 storage 被拒绝: %v", err)
	}

	// domainCount 的上边界 1000 必须被接受（dataset_store 的既有上限）。
	if err := store.ValidateStrategyInput(model.GenerationStrategy{
		Name: "n", DomainCount: 1000, QuestionsPerDomain: 1, AnswerVariants: 1, RewardVariants: 1,
	}); err != nil {
		t.Errorf("domainCount=1000（上限内）应被接受: %v", err)
	}

	for _, stage := range []string{
		"domain-generation", "question-generation", "reasoning-generation",
		"reward-generation", "sft-generation",
	} {
		if err := store.ValidatePromptInput(model.PromptTemplate{
			Name: "n", Stage: stage, SystemPrompt: "s",
		}); err != nil {
			t.Errorf("合法 stage %q 被拒绝: %v", stage, err)
		}
	}
}
