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
	"time"

	appcrypto "github.com/1420970597/llm/internal/crypto"
	"github.com/1420970597/llm/internal/store"
	"github.com/jackc/pgx/v5/pgxpool"
)

// createDataset 的 providerId 校验是「先校验、后 INSERT」，这条断言的证据必须
// 来自真实 Postgres：只有真库才能证明「拒绝时确实没有落库」以及
// 「合法 providerId 仍然 201」。
//
// 未设置 LLM_TEST_POSTGRES_DSN 时跳过并明确报告「输入缺失」，
// 与 internal/store/chain_standard_store_test.go 的既有约定一致，绝不伪装通过。
//
// 本地运行：
//
//	docker run --rm --network llm_default \
//	  -v $PWD:/w -w /w \
//	  -e LLM_TEST_POSTGRES_DSN="postgres://llm_factory:llm_factory_dev@llm-postgres-1:5432/llm_factory?sslmode=disable" \
//	  -v llm-gomodcache:/go/pkg/mod -v llm-gocache:/root/.cache/go-build \
//	  golang:1.24-alpine sh -c "go test ./apps/api/ -run TestCreateDatasetProviderGateIntegration -v"

const testProviderName = "l15-r7-fixture-provider"

func TestCreateDatasetProviderGateIntegration(t *testing.T) {
	dsn := os.Getenv("LLM_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("输入缺失: LLM_TEST_POSTGRES_DSN 未设置，跳过真实 Postgres 集成测试")
	}

	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect postgres: %v", err)
	}
	defer pool.Close()

	box, err := appcrypto.NewSecretBox("phase1-dev-only-32-byte-secret!!!")
	if err != nil {
		t.Fatalf("secret box: %v", err)
	}

	// 造一个本测试专用的 provider，避免依赖库里恰好存在 id=1。
	// 用唯一名做 WHERE 条件清理，禁止无 WHERE 的删除（契约 §6.2）。
	var providerID int64
	err = pool.QueryRow(ctx, `
	  INSERT INTO model_providers (name, base_url, model, provider_type, is_active)
	  VALUES ($1, 'http://127.0.0.1:9/v1', 'fixture-model', 'openai-compatible', TRUE)
	  RETURNING id`, testProviderName).Scan(&providerID)
	if err != nil {
		t.Fatalf("seed provider: %v", err)
	}

	datasetName := fmt.Sprintf("l15-r7-%d-%d", os.Getpid(), time.Now().UnixNano())
	defer func() {
		_, _ = pool.Exec(ctx, `DELETE FROM datasets WHERE name = $1`, datasetName)
		_, _ = pool.Exec(ctx, `DELETE FROM model_providers WHERE name = $1`, testProviderName)
	}()

	app := &application{
		store:    store.NewAdminStore(pool, box),
		datasets: store.NewDatasetStore(pool, box),
	}

	post := func(t *testing.T, body string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(http.MethodPost, "/api/v1/datasets", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		app.createDataset(rec, req)
		return rec
	}

	// 用例 1：不存在的 providerId -> 400，且必须没有落库。
	rec := post(t, fmt.Sprintf(`{"name":%q,"rootKeyword":"军事","targetSize":3,"providerId":999999}`, datasetName))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("unknown providerId: status = %d, want %d (body=%s)", rec.Code, http.StatusBadRequest, rec.Body.String())
	}
	if got := decodeErrorBody(t, rec); got != "指定的 AI 服务不存在" {
		t.Fatalf("error = %q, want 指定的 AI 服务不存在", got)
	}
	var leaked int
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM datasets WHERE name = $1`, datasetName).Scan(&leaked); err != nil {
		t.Fatalf("count leaked rows: %v", err)
	}
	if leaked != 0 {
		t.Fatalf("被拒绝的请求仍然落库了 %d 行（校验必须在 INSERT 之前）", leaked)
	}

	// 用例 2：真实存在的 providerId -> 201，并回调清库。
	rec = post(t, fmt.Sprintf(`{"name":%q,"rootKeyword":"军事","targetSize":3,"providerId":%d}`, datasetName, providerID))
	if rec.Code != http.StatusCreated {
		t.Fatalf("existing providerId: status = %d, want %d (body=%s)", rec.Code, http.StatusCreated, rec.Body.String())
	}
	var created struct {
		ID         int64 `json:"id"`
		ProviderID int64 `json:"providerId"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode created dataset: %v", err)
	}
	if created.ProviderID != providerID {
		t.Fatalf("providerId = %d, want %d", created.ProviderID, providerID)
	}
	if created.ID == 0 {
		t.Fatal("created dataset has no id")
	}

	// 用例 3：providerId=0 的既有语义必须保持（仍可 201）。
	zeroName := datasetName + "-zero"
	defer func() {
		_, _ = pool.Exec(ctx, `DELETE FROM datasets WHERE name = $1`, zeroName)
	}()
	rec = post(t, fmt.Sprintf(`{"name":%q,"rootKeyword":"军事","targetSize":3,"providerId":0}`, zeroName))
	if rec.Code != http.StatusCreated {
		t.Fatalf("providerId=0 must keep the legacy semantics: status = %d, want %d (body=%s)",
			rec.Code, http.StatusCreated, rec.Body.String())
	}
}
