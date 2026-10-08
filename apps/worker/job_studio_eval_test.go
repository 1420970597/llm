package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	appcrypto "github.com/1420970597/llm/internal/crypto"
	"github.com/1420970597/llm/internal/model"
	"github.com/1420970597/llm/internal/store"
	"github.com/1420970597/llm/internal/studio"
	"github.com/jackc/pgx/v5/pgxpool"
)

func judgeAccountingFixture(t *testing.T) (*studioConnectionJudge, studio.JudgeRequest, *studioCallAccounting, *atomic.Int32, *pgxpool.Pool) {
	t.Helper()
	accounting, pool := accountingFixture(t)
	calls := &atomic.Int32{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		var body struct {
			MaxTokens     int      `json:"max_tokens"`
			Temperature   *float64 `json:"temperature"`
			StreamOptions struct {
				IncludeUsage bool `json:"include_usage"`
			} `json:"stream_options"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if body.MaxTokens != 1024 || body.Temperature == nil || *body.Temperature != 0.25 || !body.StreamOptions.IncludeUsage {
			t.Errorf("frozen judge config and streaming usage were not sent: %+v", body)
		}
		_, _ = w.Write([]byte(`{"id":"judge-receipt","model":"actual-judge-model","usage":{"prompt_tokens":10,"completion_tokens":5},"choices":[{"message":{"content":"{\"quality\":{\"score\":0.9,\"rationale\":\"可复核\"}}"}}]}`))
	}))
	t.Cleanup(server.Close)
	box, err := appcrypto.NewSecretBox("worker-judge-fixture-key-at-least-32-bytes")
	if err != nil {
		t.Fatal(err)
	}
	encrypted, err := box.Encrypt("test-only-key")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(context.Background(), `UPDATE model_providers SET base_url=$2, encrypted_api_key=$3 WHERE id=$1`, accounting.connectionID, server.URL, encrypted); err != nil {
		t.Fatal(err)
	}
	accounting.provider.BaseURL = server.URL
	temperature := 0.25
	request := studio.JudgeRequest{ExperimentID: 7, ItemID: 11, TargetKind: model.TargetKindSFT,
		Payload:          json.RawMessage(`{"question":"温度异常应如何处置？","reasoning":"先隔离并排查。","answer":"隔离并记录。"}`),
		Rubric:           model.RubricSpec{Dimensions: []model.RubricDimension{{Key: "quality", Label: "质量", Weight: 1, Min: 0, Max: 1}}},
		JudgedDimensions: []string{"quality"},
		Judge: model.JudgeSpec{ConnectionID: accounting.connectionID, ModelName: accounting.provider.Model,
			EndpointFingerprint: model.EndpointFingerprint(server.URL), Config: model.JudgeConfig{MaxTokens: 1024, Temperature: &temperature}}}
	judge := &studioConnectionJudge{datasets: store.NewDatasetStore(pool, box), usage: accounting.usage, projects: accounting.projects,
		projectID: accounting.projectID, experimentID: request.ExperimentID, jobID: 41, attempt: 1}
	return judge, request, accounting, calls, pool
}

func TestStudioJudgeAccountsReceiptsAndJobAttemptReplay(t *testing.T) {
	judge, request, accounting, calls, pool := judgeAccountingFixture(t)
	setAccountingPrice(t, accounting, 1000)
	ctx := context.Background()
	verdicts, err := judge.JudgeItem(ctx, request)
	if err != nil || len(verdicts) != 1 || verdicts[0].RawScore == nil || *verdicts[0].RawScore != 0.9 {
		t.Fatalf("judge verdict: %+v, %v", verdicts, err)
	}
	if _, err := judge.JudgeItem(ctx, request); err == nil || calls.Load() != 1 {
		t.Fatalf("same job/item/judge attempt must not repeat a paid request: calls=%d, err=%v", calls.Load(), err)
	}
	judge.attempt = 2
	if _, err := judge.JudgeItem(ctx, request); err != nil || calls.Load() != 2 {
		t.Fatalf("a new job attempt must have its own receipt: calls=%d, err=%v", calls.Load(), err)
	}
	var rows, attempts, withIdentity, withoutBatch int
	if err := pool.QueryRow(ctx, `SELECT COUNT(*),COUNT(DISTINCT attempt),COUNT(*) FILTER(WHERE request_id='judge-receipt' AND response_model_id='actual-judge-model' AND purpose='experiment_judge' AND job_id=41 AND config_fingerprint <> ''),COUNT(*) FILTER(WHERE batch_id IS NULL) FROM usage_ledger WHERE project_id=$1`, accounting.projectID).Scan(&rows, &attempts, &withIdentity, &withoutBatch); err != nil {
		t.Fatal(err)
	}
	if rows != 2 || attempts != 2 || withIdentity != 2 || withoutBatch != 2 {
		t.Fatalf("incomplete project judge ledger: rows=%d attempts=%d identity=%d withoutBatch=%d", rows, attempts, withIdentity, withoutBatch)
	}
	budget, err := accounting.usage.ProjectBudget(ctx, accounting.projectID, "CNY")
	if err != nil || budget.ReservedMinor != 0 || budget.SettledMinor != 4 || budget.UncertainMinor != 0 {
		t.Fatalf("judge cost must enter project budget: %+v, %v", budget, err)
	}
}

func TestStudioJudgeRejectsMissingPriceOutputBoundAndConnectionDrift(t *testing.T) {
	for _, test := range []struct {
		name string
		edit func(*testing.T, *studio.JudgeRequest, *studioCallAccounting)
	}{
		{"missing-price", func(_ *testing.T, _ *studio.JudgeRequest, _ *studioCallAccounting) {}},
		{"missing-output-bound", func(t *testing.T, request *studio.JudgeRequest, accounting *studioCallAccounting) {
			setAccountingPrice(t, accounting, 1000)
			request.Judge.Config.MaxTokens = 0
		}},
		{"model-drift", func(t *testing.T, request *studio.JudgeRequest, accounting *studioCallAccounting) {
			setAccountingPrice(t, accounting, 1000)
			request.Judge.ModelName = "old-frozen-model"
		}},
		{"endpoint-drift", func(t *testing.T, request *studio.JudgeRequest, accounting *studioCallAccounting) {
			setAccountingPrice(t, accounting, 1000)
			request.Judge.EndpointFingerprint = model.EndpointFingerprint("https://old-endpoint.test/v1")
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			judge, request, accounting, calls, _ := judgeAccountingFixture(t)
			test.edit(t, &request, accounting)
			if _, err := judge.JudgeItem(context.Background(), request); err == nil || calls.Load() != 0 {
				t.Fatalf("invalid frozen price/config must block HTTP: calls=%d, err=%v", calls.Load(), err)
			}
		})
	}
}

func TestStudioJudgeBudgetExhaustionStopsHTTPAndPersistsErrorEvidence(t *testing.T) {
	judge, request, accounting, calls, pool := judgeAccountingFixture(t)
	setAccountingPrice(t, accounting, 1_000_000)
	ctx := context.Background()
	if _, err := pool.Exec(ctx, `UPDATE projects SET budget_limit_minor=100 WHERE id=$1`, accounting.projectID); err != nil {
		t.Fatal(err)
	}
	versions := []int64{}
	for i := 0; i < 2; i++ {
		_, version, err := accounting.batches.AppendSampleVersion(ctx, store.AppendSampleVersionInput{ProjectID: accounting.projectID,
			SampleKey: fmt.Sprintf("judge-budget-%d", i), TargetKind: model.TargetKindSFT,
			Payload: map[string]any{"question": "温度异常应如何处置？", "reasoning": "先隔离。", "answer": "隔离并记录。"}})
		if err != nil {
			t.Fatal(err)
		}
		versions = append(versions, version.ID)
	}
	experiments := store.NewExperimentStore(pool)
	experiment, err := experiments.CreateExperiment(ctx, store.CreateExperimentInput{ProjectID: accounting.projectID, TargetKind: model.TargetKindSFT,
		SampleVersionIDs: versions, Judges: []model.JudgeSpec{request.Judge}, Rubric: request.Rubric})
	if err != nil {
		t.Fatal(err)
	}
	judge.experimentID = experiment.ID
	runner := studio.ExperimentRunner{Experiments: experiments, Batches: accounting.batches, Judge: judge}
	result, err := runner.RunExperiment(ctx, experiment.ID)
	if err != nil || result.Error != 2 || calls.Load() != 0 {
		t.Fatalf("budget exhaustion must leave errors across frozen scope with zero HTTP: %+v, calls=%d, err=%v", result, calls.Load(), err)
	}
	var errorsRecorded, ledgerRows int
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM experiment_scores WHERE experiment_id=$1 AND score_state='error'`, experiment.ID).Scan(&errorsRecorded); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM usage_ledger WHERE project_id=$1`, accounting.projectID).Scan(&ledgerRows); err != nil {
		t.Fatal(err)
	}
	if errorsRecorded != 2 || ledgerRows != 0 {
		t.Fatalf("error evidence or rejected ledger mismatch: errors=%d ledger=%d", errorsRecorded, ledgerRows)
	}
	if _, err := pool.Exec(ctx, `UPDATE projects SET budget_limit_minor=0 WHERE id=$1`, accounting.projectID); err != nil {
		t.Fatal(err)
	}
	request.ExperimentID = experiment.ID
	if _, err := judge.JudgeItem(ctx, request); !errors.Is(err, store.ErrBudgetExhausted) || calls.Load() != 0 {
		t.Fatalf("same exhausted attempt must remain stopped after an external budget edit: calls=%d, err=%v", calls.Load(), err)
	}
}
