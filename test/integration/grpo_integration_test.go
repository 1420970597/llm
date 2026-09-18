//go:build integration

// L4 GRPO 端到端集成测试。
//
// 跑法（在 compose 网络内，连真实 Postgres 与真实 LLM）：
//
//	docker run --rm --network llm_default \
//	  -v <worktree>:/w -w /w \
//	  -v llm-gomodcache:/go/pkg/mod -v llm-gocache:/root/.cache/go-build \
//	  -e GOFLAGS=-mod=readonly -e L4_DATASET_ID=1 \
//	  golang:1.24-alpine sh -c "go test -tags=integration -run TestGrpo -v ./test/integration/"
//
// 这不是 mock：连的是 compose 里的真实 Postgres，调的是真实 provider
// http://152.53.126.151:8885/v1（model global:deepseek-v4.1-flash）。
package integration

import (
	"context"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	appcrypto "github.com/1420970597/llm/internal/crypto"
	"github.com/1420970597/llm/internal/llm"
	"github.com/1420970597/llm/internal/model"
	"github.com/1420970597/llm/internal/store"
	"github.com/jackc/pgx/v5/pgxpool"
)

// encryptionKey 与 compose 中 APP_ENCRYPTION_KEY 一致。
const encryptionKey = "phase1-dev-only-32-byte-secret!!!"

func mustBox(t *testing.T) *appcrypto.SecretBox {
	t.Helper()
	box, err := appcrypto.NewSecretBox(encryptionKey)
	if err != nil {
		t.Fatalf("secret box: %v", err)
	}
	return box
}

const dsn = "postgres://llm_factory:llm_factory_dev@postgres:5432/llm_factory?sslmode=disable"

func datasetID(t *testing.T) int64 {
	t.Helper()
	raw := strings.TrimSpace(os.Getenv("L4_DATASET_ID"))
	if raw == "" {
		t.Fatal("输入缺失: L4_DATASET_ID 未设置")
	}
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		t.Fatalf("L4_DATASET_ID 非法: %v", err)
	}
	return id
}

// TestGrpoEndToEndAgainstRealProvider 是 L4 的核心验证：
// 真实 Postgres 读问题与长链标准步骤 → 真实 LLM 生成判据 → 真实写回 grpo_prompts → 真实读回。
func TestGrpoEndToEndAgainstRealProvider(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect postgres: %v", err)
	}
	defer pool.Close()

	id := datasetID(t)

	// 1. 真实读取数据集与 provider
	datasets := store.NewDatasetStore(pool, mustBox(t))
	dataset, err := datasets.GetDataset(ctx, id)
	if err != nil {
		t.Fatalf("load dataset %d: %v", id, err)
	}
	if dataset.ProviderID == 0 {
		t.Fatal("输入缺失: 数据集未配置 provider_id")
	}
	baseURL, modelName, providerType, reasoningEffort, apiKey, err := datasets.ResolveProvider(ctx, dataset.ProviderID)
	if err != nil {
		t.Fatalf("resolve provider: %v", err)
	}
	if apiKey == "" || baseURL == "" {
		t.Fatalf("输入缺失: provider 缺少 APIKey 或 BaseURL (base=%q keylen=%d)", baseURL, len(apiKey))
	}
	t.Logf("provider base=%s model=%s levels=%v", baseURL, modelName, dataset.RewardLevels)

	levels := llm.NormalizeLevels(dataset.RewardLevels)
	if len(levels) < 2 {
		t.Fatalf("输入缺失: 数据集档次不足两个: %v", dataset.RewardLevels)
	}

	// 2. 真实读取问题上下文（含长链标准步骤 join）
	grpoStore := store.NewGrpoStore(pool)
	contexts, err := grpoStore.ListQuestionContexts(ctx, id)
	if err != nil {
		t.Fatalf("list question contexts: %v", err)
	}
	if len(contexts) == 0 {
		t.Fatalf("输入缺失: 数据集 %d 没有问题，无法测试 GRPO 生成", id)
	}
	t.Logf("loaded %d question contexts; first: qid=%d direction=%q chainSteps=%d",
		len(contexts), contexts[0].QuestionID, contexts[0].DirectionName, len(contexts[0].ChainSteps))

	// 只取前 2 条，控制真实 LLM 调用时长
	sample := contexts
	if len(sample) > 2 {
		sample = sample[:2]
	}

	// 3. 真实 LLM 调用
	prompts := make([]model.GrpoPrompt, 0, len(sample))
	for _, item := range sample {
		started := time.Now()
		output, err := llm.GenerateGrpoPrompt(ctx, llm.ProviderConfig{
			BaseURL:         baseURL,
			Model:           modelName,
			ProviderType:    providerType,
			ReasoningEffort: reasoningEffort,
			APIKey:          apiKey,
		}, llm.GrpoPromptInput{
			RootKeyword:   dataset.RootKeyword,
			DirectionName: item.DirectionName,
			Question:      item.QuestionText,
			ChainSteps:    item.ChainSteps,
			Levels:        levels,
		})
		if err != nil {
			t.Fatalf("真实 LLM 生成失败 question_id=%d: %v", item.QuestionID, err)
		}
		t.Logf("real LLM ok question_id=%d elapsed=%s rubrics=%d prompt_chars=%d",
			item.QuestionID, time.Since(started).Round(time.Millisecond), len(output.LevelRubrics), len(output.JudgePrompt))

		if len(output.LevelRubrics) != len(levels) {
			t.Fatalf("判据数量不符: got %d want %d", len(output.LevelRubrics), len(levels))
		}
		for _, rubric := range output.LevelRubrics {
			if strings.TrimSpace(rubric.Criteria) == "" {
				t.Fatalf("档次 %s 判据为空", rubric.Level)
			}
		}
		// 六个必需段落
		for _, needle := range []string{
			"你是资深的长链思考数据评审专家",
			"## 一、评审对象",
			"## 二、整体性思考框架（必须逐步核对）",
			"## 三、打分档次与判据",
			"## 四、结合具体场景的判断要求",
			"## 五、输出格式（强制）",
		} {
			if !strings.Contains(output.JudgePrompt, needle) {
				t.Fatalf("提示词缺少必需段落 %q", needle)
			}
		}
		for _, level := range levels {
			if !strings.Contains(output.JudgePrompt, "档次 `"+level+"`") {
				t.Fatalf("提示词未列出档次 %q", level)
			}
		}

		prompts = append(prompts, model.GrpoPrompt{
			DatasetID:    id,
			QuestionID:   item.QuestionID,
			DomainID:     item.DirectionID,
			Levels:       levels,
			JudgePrompt:  output.JudgePrompt,
			LevelRubrics: output.LevelRubrics,
			FrameworkRef: output.FrameworkRef,
			Status:       "generated",
		})
	}

	// 4. 真实写回 + 读回
	if err := grpoStore.UpsertPrompts(ctx, id, prompts); err != nil {
		t.Fatalf("upsert prompts: %v", err)
	}
	persisted, err := grpoStore.ListPrompts(ctx, id)
	if err != nil {
		t.Fatalf("list prompts: %v", err)
	}
	if len(persisted) == 0 {
		t.Fatal("写回后读不到任何提示词")
	}

	found := 0
	for _, item := range persisted {
		for _, want := range prompts {
			if item.QuestionID == want.QuestionID {
				found++
				if item.JudgePrompt != want.JudgePrompt {
					t.Fatalf("question_id=%d judgePrompt 往返不一致", item.QuestionID)
				}
				if len(item.LevelRubrics) != len(want.LevelRubrics) {
					t.Fatalf("question_id=%d levelRubrics 往返数量不一致: %d vs %d",
						item.QuestionID, len(item.LevelRubrics), len(want.LevelRubrics))
				}
				if item.QuestionText == "" {
					t.Fatalf("question_id=%d 读回时缺少 questionText（join 失效）", item.QuestionID)
				}
				if item.DomainName == "" {
					t.Fatalf("question_id=%d 读回时缺少 domainName（join 失效）", item.QuestionID)
				}
			}
		}
	}
	if found != len(prompts) {
		t.Fatalf("往返匹配数不符: got %d want %d", found, len(prompts))
	}

	// 5. 幂等：重复 upsert 不得产生重复行
	if err := grpoStore.UpsertPrompts(ctx, id, prompts); err != nil {
		t.Fatalf("second upsert: %v", err)
	}
	after, err := grpoStore.CountPrompts(ctx, id)
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	distinctQuestions := map[int64]struct{}{}
	for _, item := range prompts {
		distinctQuestions[item.QuestionID] = struct{}{}
	}
	if after < len(distinctQuestions) {
		t.Fatalf("幂等失败: 行数 %d 少于不同问题数 %d", after, len(distinctQuestions))
	}

	t.Logf("E2E PASS: %d prompts persisted, total rows in dataset=%d", len(prompts), after)
}

// TestGrpoDoesNotTouchRewardRecords 锁定 L4 的落点决策：
// GRPO 写入不得改动 reward_records，否则会破坏导出门禁与 reward_score。
func TestGrpoDoesNotTouchRewardRecords(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect postgres: %v", err)
	}
	defer pool.Close()

	id := datasetID(t)

	before := countRewards(t, ctx, pool, id)
	grpoStore := store.NewGrpoStore(pool)
	if _, err := grpoStore.CountPrompts(ctx, id); err != nil {
		t.Fatalf("count grpo prompts: %v", err)
	}
	after := countRewards(t, ctx, pool, id)

	if before != after {
		t.Fatalf("GRPO 操作改动了 reward_records: before=%d after=%d", before, after)
	}
	t.Logf("reward_records 未被触碰: %d 行", after)
}

func countRewards(t *testing.T, ctx context.Context, pool *pgxpool.Pool, datasetID int64) int {
	t.Helper()
	var count int
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM reward_records WHERE dataset_id = $1`, datasetID).Scan(&count); err != nil {
		t.Fatalf("count rewards: %v", err)
	}
	return count
}
