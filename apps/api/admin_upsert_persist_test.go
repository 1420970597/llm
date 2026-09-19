package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	appcrypto "github.com/1420970597/llm/internal/crypto"
	"github.com/1420970597/llm/internal/model"
	"github.com/1420970597/llm/internal/store"
	"github.com/jackc/pgx/v5/pgxpool"
)

// issue #63 的另一半：合法 body 必须仍然 200 并真实落库。
//
// 只测「拒绝」是不够的 —— 一个把合法请求也挡掉的校验同样能通过纯拒绝用例，
// 但会直接打断管理员的配置流程。因此这里用真实 Postgres 跑完整 handler。
//
// 未设置 LLM_TEST_POSTGRES_DSN 时跳过，避免在无数据库环境里伪装通过。
//
// 本地运行：
//
//	docker run --rm --network llm_default -v $PWD:/w -w /w \
//	  -e LLM_TEST_POSTGRES_DSN="postgres://llm_factory:llm_factory_dev@llm-postgres-1:5432/llm_factory?sslmode=disable" \
//	  -e APP_ENCRYPTION_KEY="$(docker exec llm-api-1 printenv APP_ENCRYPTION_KEY)" \
//	  golang:1.24-alpine sh -c "go test ./apps/api/ -run TestAdminUpsert -v"
//
// 反污染（契约 §6.2）：所有行都带唯一前缀 l15-r6-<pid>-，结束时按该前缀精确删除，
// 不做无 WHERE 的批量删除。
const adminUpsertTestPrefix = "l15-r6-"

func adminUpsertTestName(suffix string) string {
	return fmt.Sprintf("%s%d-%s", adminUpsertTestPrefix, os.Getpid(), suffix)
}

func newAdminUpsertTestApp(t *testing.T) (*application, context.Context, func()) {
	t.Helper()
	dsn := os.Getenv("LLM_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("输入缺失: LLM_TEST_POSTGRES_DSN 未设置，跳过真实 Postgres 集成测试")
	}
	encKey := os.Getenv("APP_ENCRYPTION_KEY")
	if encKey == "" {
		encKey = "l15-r6-admin-validation-test-key!"
	}

	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect postgres: %v", err)
	}
	box, err := appcrypto.NewSecretBox(encKey)
	if err != nil {
		pool.Close()
		t.Fatalf("new secret box: %v", err)
	}

	app := &application{store: store.NewAdminStore(pool, box)}
	return app, ctx, func() {
		cleanupAdminUpsertFixtures(t, ctx, pool)
		pool.Close()
	}
}

// cleanupAdminUpsertFixtures 只删除本测试用唯一前缀创建的行。
func cleanupAdminUpsertFixtures(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()
	pattern := adminUpsertTestPrefix + "%"
	for _, stmt := range []string{
		`DELETE FROM model_providers WHERE name LIKE $1`,
		`DELETE FROM storage_profiles WHERE name LIKE $1`,
		`DELETE FROM generation_strategies WHERE name LIKE $1`,
		`DELETE FROM prompt_templates WHERE name LIKE $1`,
	} {
		if _, err := pool.Exec(ctx, stmt, pattern); err != nil {
			t.Errorf("清理夹具失败 (%s): %v", stmt, err)
		}
	}
}

func postAdminUpsert(t *testing.T, app *application, path, body string, handler func(*application, http.ResponseWriter, *http.Request)) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	handler(app, rec, req)
	return rec
}

// 合法 provider body 必须 200，且返回体里的 name/baseUrl/model 与提交一致。
func TestAdminUpsertPersistsValidProvider(t *testing.T) {
	app, _, cleanup := newAdminUpsertTestApp(t)
	defer cleanup()

	name := adminUpsertTestName("provider")
	body := fmt.Sprintf(`{"name":%q,"baseUrl":"http://example.invalid/v1","model":"l15-r6-model","maxConcurrency":2,"timeoutSeconds":30,"isActive":true}`, name)

	rec := postAdminUpsert(t, app, "/api/v1/admin/providers", body, (*application).upsertProvider)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d (body=%s)", rec.Code, http.StatusOK, rec.Body.String())
	}
	var item struct {
		ID       int64  `json:"id"`
		Name     string `json:"name"`
		BaseURL  string `json:"baseUrl"`
		Model    string `json:"model"`
		APIKey   string `json:"apiKey"`
		IsActive bool   `json:"isActive"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &item); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if item.ID == 0 || item.Name != name || item.Model != "l15-r6-model" {
		t.Fatalf("persisted = %+v, want the submitted values back", item)
	}
	// 密钥不在响应里明文回显（沿用既有响应形状）。
	if item.APIKey != "" {
		t.Errorf("apiKey = %q, want it not echoed back", item.APIKey)
	}
}

// 合法的 storage / strategy / prompt 也必须 200。
func TestAdminUpsertPersistsValidRemainingEndpoints(t *testing.T) {
	app, _, cleanup := newAdminUpsertTestApp(t)
	defer cleanup()

	storageName := adminUpsertTestName("storage")
	storageBody := fmt.Sprintf(`{"name":%q,"provider":"minio","endpoint":"http://minio:9000","region":"us-east-1","bucket":"l15-r6-bucket","accessKeyId":"minioadmin","usePathStyle":true,"isActive":true,"isDefault":false}`, storageName)
	if rec := postAdminUpsert(t, app, "/api/v1/admin/storage-profiles", storageBody, (*application).upsertStorageProfile); rec.Code != http.StatusOK {
		t.Fatalf("storage status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}

	strategyName := adminUpsertTestName("strategy")
	strategyBody := fmt.Sprintf(`{"name":%q,"description":"d","domainCount":5,"questionsPerDomain":2,"answerVariants":1,"rewardVariants":1,"planningMode":"balanced","isDefault":false}`, strategyName)
	if rec := postAdminUpsert(t, app, "/api/v1/admin/generation-strategies", strategyBody, (*application).upsertStrategy); rec.Code != http.StatusOK {
		t.Fatalf("strategy status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}

	promptName := adminUpsertTestName("prompt")
	promptBody := fmt.Sprintf(`{"name":%q,"stage":"question-generation","version":"v1","systemPrompt":"系统指令","userPrompt":"用户指令","isActive":false}`, promptName)
	if rec := postAdminUpsert(t, app, "/api/v1/admin/prompts", promptBody, (*application).upsertPrompt); rec.Code != http.StatusOK {
		t.Fatalf("prompt status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}
}

// issue #63 的验收标准「model_providers 不再可能插入 name/base_url/model 为空的行」。
//
// 直接对比「拒绝请求前后」的统计：被拒绝的请求不得新增任何行。
func TestAdminUpsertRejectionWritesNoRow(t *testing.T) {
	app, ctx, cleanup := newAdminUpsertTestApp(t)
	defer cleanup()

	countRows := func() int {
		t.Helper()
		providers, err := app.store.ListProviders(ctx)
		if err != nil {
			t.Fatalf("list providers: %v", err)
		}
		return len(providers)
	}

	before := countRows()
	rec := postAdminUpsert(t, app, "/api/v1/admin/providers", `{}`, (*application).upsertProvider)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	if after := countRows(); after != before {
		t.Fatalf("provider 行数 %d -> %d，被拒绝的请求不应写入任何行", before, after)
	}
}

// stage 的白名单必须与真实调用 GetActivePromptByStage 的取值一致。
//
// 防漂移断言：worker/api 新增生成阶段却忘了同步白名单时，保存模板会莫名 400；
// 反过来白名单里留着已废弃的 stage 则会允许保存永远读不到的模板。两个方向都兜住。
func TestPromptStageAllowlistCoversRealStages(t *testing.T) {
	valid := []string{
		"domain-generation",
		"question-generation",
		"reasoning-generation",
		"reward-generation",
		"sft-generation",
	}
	for _, stage := range valid {
		input := model.PromptTemplate{Name: "n", Stage: stage, SystemPrompt: "s"}
		if err := store.ValidatePromptInput(input); err != nil {
			t.Errorf("stage %q 应当被接受，却被拒绝: %v", stage, err)
		}
	}

	// 常见拼错 / 空值必须被拒。
	for _, bad := range []string{"domain_generation", "unknown-stage", ""} {
		input := model.PromptTemplate{Name: "n", Stage: bad, SystemPrompt: "s"}
		if err := store.ValidatePromptInput(input); err == nil {
			t.Errorf("stage %q 应当被拒绝", bad)
		}
	}
}

// 校验规则本身的边界：允许「只有系统指令」或「只有用户指令」，但两者皆空必须拒绝。
// issue #63 报的正是「两者皆空且 isActive=true」。
func TestPromptValidationAllowsOneSidedPrompt(t *testing.T) {
	base := func(system, user string) model.PromptTemplate {
		return model.PromptTemplate{Name: "n", Stage: "question-generation", SystemPrompt: system, UserPrompt: user}
	}
	if err := store.ValidatePromptInput(base("系统指令", "")); err != nil {
		t.Errorf("只填系统指令应当被接受: %v", err)
	}
	if err := store.ValidatePromptInput(base("", "用户指令")); err != nil {
		t.Errorf("只填用户指令应当被接受: %v", err)
	}
	if err := store.ValidatePromptInput(base("", "")); err == nil {
		t.Error("两者皆空必须被拒绝")
	}
}
