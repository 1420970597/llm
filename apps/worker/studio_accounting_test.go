package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/1420970597/llm/internal/llm"
	"github.com/1420970597/llm/internal/model"
	"github.com/1420970597/llm/internal/store"
	"github.com/1420970597/llm/internal/studio"
	"github.com/jackc/pgx/v5/pgxpool"
)

func accountingFixture(t *testing.T) (*studioCallAccounting, *pgxpool.Pool) {
	t.Helper()
	dsn := os.Getenv("LLM_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("LLM_TEST_POSTGRES_DSN is required for real ledger tests")
	}
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	suffix := fmt.Sprintf("account-%d-%d", os.Getpid(), time.Now().UnixNano())
	var workspaceID, userID, connectionID, batchID int64
	if err := pool.QueryRow(ctx, `INSERT INTO workspaces(name,slug) VALUES($1,$1) RETURNING id`, suffix).Scan(&workspaceID); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO users(email,hashed_password,role) VALUES($1,'test','admin') RETURNING id`, suffix+"@example.test").Scan(&userID); err != nil {
		t.Fatal(err)
	}
	projects := store.NewProjectStore(pool)
	input := model.CreateProjectInput{Name: "请求账目验证", Goal: "验证真实数据库预算", TargetKind: "sft"}
	input.Normalize()
	project, err := projects.CreateProject(ctx, workspaceID, userID, input)
	if err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO model_providers(name,base_url,model) VALUES($1,'http://accounting.test/v1','test-model') RETURNING id`, suffix).Scan(&connectionID); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO batches(project_id,purpose,target_kind,schema_version,planned_units) VALUES($1,'pilot','sft','batch.v1',1) RETURNING id`, project.ID).Scan(&batchID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM projects WHERE id=$1`, project.ID)
		_, _ = pool.Exec(context.Background(), `DELETE FROM model_price_versions WHERE provider_connection_id=$1`, connectionID)
		_, _ = pool.Exec(context.Background(), `DELETE FROM model_providers WHERE id=$1`, connectionID)
		_, _ = pool.Exec(context.Background(), `DELETE FROM workspaces WHERE id=$1`, workspaceID)
		_, _ = pool.Exec(context.Background(), `DELETE FROM users WHERE id=$1`, userID)
		pool.Close()
	})
	return &studioCallAccounting{usage: store.NewUsageStore(pool), projects: projects, batches: store.NewBatchStore(pool),
		provider:  llm.ProviderConfig{BaseURL: "http://accounting.test/v1", Model: "test-model"},
		projectID: project.ID, batchID: &batchID, connectionID: connectionID,
		idempotencyKey: fmt.Sprintf("batch:%d:item:domain/direction#1:attempt:1", batchID), attempt: 1, purpose: "batch_generate"}, pool
}

func setAccountingPrice(t *testing.T, accounting *studioCallAccounting, price int64) {
	t.Helper()
	_, err := accounting.usage.UpsertPriceVersion(context.Background(), store.PriceVersionInput{ConnectionID: accounting.connectionID,
		ModelName: accounting.provider.Model, EndpointFP: model.EndpointFingerprint(accounting.provider.BaseURL), PriceVersion: "test-v1", InputPerMillion: price, OutputPerMillion: price})
	if err != nil {
		t.Fatal(err)
	}
}

func TestStudioAccountingWritesEachReceiptAndUnknownCost(t *testing.T) {
	accounting, pool := accountingFixture(t)
	setAccountingPrice(t, accounting, 1000)
	ctx := context.Background()
	meta := llm.RequestMetadata{InputTokensUpperBound: 2000, MaxOutputTokens: 1024, ConfigFingerprint: "sha256:test"}
	settle, err := accounting.ReserveCall(ctx, meta)
	if err != nil {
		t.Fatal(err)
	}
	in, out := int64(100), int64(200)
	if err := settle(ctx, llm.ResponseMetadata{RequestID: "question-receipt", ModelID: "actual-model", Usage: model.TokenUsage{InputTokens: &in, OutputTokens: &out, Source: model.UsageSourceProvider}}, nil); err != nil {
		t.Fatal(err)
	}
	settle, err = accounting.ReserveCall(ctx, meta)
	if err != nil {
		t.Fatal(err)
	}
	if err := settle(ctx, llm.ResponseMetadata{}, context.DeadlineExceeded); err != nil {
		t.Fatal(err)
	}
	var receipts, unknown int
	var requestID, actualModel string
	if err := pool.QueryRow(ctx, `SELECT COUNT(*),COUNT(*) FILTER(WHERE amount_state='unknown' AND actual_minor IS NULL) FROM usage_ledger WHERE batch_id=$1`, *accounting.batchID).Scan(&receipts, &unknown); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT request_id,response_model_id FROM usage_ledger WHERE batch_id=$1 AND amount_state='actual'`, *accounting.batchID).Scan(&requestID, &actualModel); err != nil {
		t.Fatal(err)
	}
	budget, err := accounting.usage.ProjectBudget(ctx, accounting.projectID, "CNY")
	if err != nil {
		t.Fatal(err)
	}
	if receipts != 2 || unknown != 1 || requestID != "question-receipt" || actualModel != "actual-model" || budget.ReservedMinor != 0 || budget.SettledMinor != 2 || budget.UncertainMinor != 4 {
		t.Fatalf("ledger/budget mismatch: receipts=%d unknown=%d id=%q model=%q budget=%+v", receipts, unknown, requestID, actualModel, budget)
	}
}

func TestStudioAccountingRejectsMissingPriceAndBudgetOverrun(t *testing.T) {
	accounting, pool := accountingFixture(t)
	ctx := context.Background()
	meta := llm.RequestMetadata{InputTokensUpperBound: 2000, MaxOutputTokens: 1024}
	if _, err := accounting.ReserveCall(ctx, meta); err == nil {
		t.Fatal("missing price allowed external call")
	}
	setAccountingPrice(t, accounting, 1_000_000)
	if _, err := pool.Exec(ctx, `UPDATE projects SET budget_limit_minor=100 WHERE id=$1`, accounting.projectID); err != nil {
		t.Fatal(err)
	}
	if _, err := accounting.ReserveCall(ctx, meta); !errors.Is(err, studio.ErrBatchPaused) {
		t.Fatalf("budget overrun must pause external calls: %v", err)
	}
	batch, err := accounting.batches.GetBatch(ctx, *accounting.batchID)
	if err != nil || batch.ControlState != model.BatchControlPauseRequested {
		t.Fatalf("budget pause did not persist: batch=%+v err=%v", batch, err)
	}
	var count int
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM usage_ledger WHERE batch_id=$1`, *accounting.batchID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatal("rejected calls left ledger entries")
	}
}

func TestStudioAccountingPauseBlocksNextStepAndHTTPRetry(t *testing.T) {
	for _, retry := range []bool{false, true} {
		name := "next-step"
		if retry {
			name = "http-retry"
		}
		t.Run(name, func(t *testing.T) {
			accounting, pool := accountingFixture(t)
			project, err := accounting.projects.GetProject(context.Background(), accounting.projectID)
			if err != nil {
				t.Fatal(err)
			}
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				calls++
				if calls == 1 {
					if _, err := accounting.batches.PauseBatch(context.Background(), accounting.projectID, *accounting.batchID, 0); err != nil {
						t.Errorf("pause in-flight request: %v", err)
						http.Error(w, "pause failed", http.StatusInternalServerError)
						return
					}
				}
				if retry && calls == 1 {
					_, _ = w.Write([]byte(`{"id":"paid-question","model":"test-model","usage":{"prompt_tokens":100,"completion_tokens":200},"choices":[]}`))
					return
				}
				_, _ = w.Write([]byte(`{"id":"paid-question","model":"test-model","usage":{"prompt_tokens":100,"completion_tokens":200},"choices":[{"message":{"content":"valid question"}}]}`))
			}))
			defer server.Close()
			accounting.provider.BaseURL = server.URL
			setAccountingPrice(t, accounting, 1000)
			provider := llm.WithUsageReporting(llm.ProviderConfig{BaseURL: server.URL, APIKey: "test", Model: "test-model", MaxTokens: 1024, Accounting: accounting})
			ctx := context.Background()
			_, err = llm.CompleteJSON(ctx, provider, "system", "question", time.Second)
			if !retry {
				if err != nil {
					t.Fatalf("in-flight response must complete: %v", err)
				}
				_, err = llm.CompleteJSON(ctx, provider, "system", "answer", time.Second)
			}
			if !errors.Is(err, studio.ErrBatchPaused) || calls != 1 {
				t.Fatalf("pause must block subsequent HTTP: calls=%d err=%v", calls, err)
			}
			var count, settled int
			if err := pool.QueryRow(ctx, `SELECT COUNT(*),COUNT(*) FILTER(WHERE state='settled' AND request_id='paid-question') FROM usage_ledger WHERE batch_id=$1`, *accounting.batchID).Scan(&count, &settled); err != nil {
				t.Fatal(err)
			}
			budget, err := accounting.usage.ProjectBudget(ctx, accounting.projectID, "CNY")
			if err != nil || count != 1 || settled != 1 || budget.ReservedMinor != 0 || budget.SettledMinor != 2 {
				t.Fatalf("in-flight receipt must settle without a new reservation: count=%d settled=%d budget=%+v err=%v", count, settled, budget, err)
			}
			if _, err := accounting.batches.ResumeBatch(ctx, accounting.projectID, *accounting.batchID, project.OwnerID); err != nil {
				t.Fatal(err)
			}
			accounting.attempt = 2
			accounting.idempotencyKey = fmt.Sprintf("batch:%d:item:domain/direction#1:attempt:2", *accounting.batchID)
			if _, err := llm.CompleteJSON(ctx, provider, "system", "answer", time.Second); err != nil || calls != 2 {
				t.Fatalf("explicit resume must allow the new attempt: calls=%d err=%v", calls, err)
			}
		})
	}
}
