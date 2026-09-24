package studio

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/1420970597/llm/internal/model"
	"github.com/1420970597/llm/internal/store"
	"github.com/jackc/pgx/v5/pgxpool"
)

// 本文件验证 Issue #160 T12 的批原生 runner。
//
// 为什么用**真实 Postgres + 可控假 provider**（而不是纯内存）：
//  T12 验收项里的核心性质全是数据库语义 —— 「阶段恢复不重新生成成功项」
//  靠 UNIQUE(batch_id,item_key) 与条件更新，「进度单位正确」靠从 item 事实
//  重算的计数，「同题不同批次不覆盖」靠只追加的 sample_versions。
//  这些用内存实现「测」的话测的是测试自己的实现。
//
//  而真实 provider 无法按需产生「空输出 / 截断 / 无效 JSON / 429 / 超时」
//  这五种结果，且在测试里真调外部模型既不稳定也不该花钱 —— 因此
//  生成侧用 scriptedGenerator（可控假 provider，符合 T12「默认 CI 使用
//  可控假 provider，不伪称真实产出」）。
//
// 未设置 LLM_TEST_POSTGRES_DSN 时跳过（T32 会检查这些测试不是 Skip）。

// scriptedGenerator 是按单元键脚本化的假 provider。
type scriptedGenerator struct {
	mu sync.Mutex
	// bySuffix 按 item_key 的最后一个 `#n` 决定行为；缺省为成功。
	script map[string]func(UnitRequest) (UnitResult, error)
	// calls 记录每次调用的单元键，用于断言「恢复不重跑成功项」。
	calls []string
	// delay 模拟慢调用（用于并发/暂停测试）。
	delay time.Duration
	// failOnce 让某个键第一次失败、第二次成功（用于「恢复」测试）。
	failed map[string]int
}

func (generator *scriptedGenerator) GenerateUnit(ctx context.Context, request UnitRequest) (UnitResult, error) {
	if generator.delay > 0 {
		select {
		case <-time.After(generator.delay):
		case <-ctx.Done():
			return UnitResult{}, ctx.Err()
		}
	}
	generator.mu.Lock()
	generator.calls = append(generator.calls, request.ItemKey)
	script := generator.script[request.ItemKey]
	if script == nil && generator.failed == nil {
		generator.mu.Unlock()
		return successResult(request), nil
	}
	if generator.failed != nil {
		// 第一次失败、之后成功：模拟「限流后恢复」。
		if count := generator.failed[request.ItemKey]; count == 0 {
			generator.failed[request.ItemKey] = 1
			generator.mu.Unlock()
			return UnitResult{}, errors.New("HTTP 429 Too Many Requests")
		}
	}
	generator.mu.Unlock()
	if script == nil {
		return successResult(request), nil
	}
	return script(request)
}

func (generator *scriptedGenerator) callCount() int {
	generator.mu.Lock()
	defer generator.mu.Unlock()
	return len(generator.calls)
}

func (generator *scriptedGenerator) calledKeys() []string {
	generator.mu.Lock()
	defer generator.mu.Unlock()
	return append([]string{}, generator.calls...)
}

func successResult(request UnitRequest) UnitResult {
	return UnitResult{
		Payload: map[string]any{
			"question":  "单元 " + request.ItemKey,
			"reasoning": "先识别约束，再逐步推导，最后核对边界。",
			"answer":    "结论：" + request.ItemKey,
		},
		Title: "单元 " + request.ItemKey,
		Usage: model.TokenUsage{
			InputTokens: pointer(120), OutputTokens: pointer(340),
			Source: model.UsageSourceProvider,
		},
		RequestID: "req-" + request.ItemKey, ResponseModelID: "fake-model-v1",
	}
}

func pointer(value int64) *int64 { return &value }

// ---------------------------------------------------------------------------
// fixture
// ---------------------------------------------------------------------------

type runnerFixture struct {
	pool        *pgxpool.Pool
	projectID   int64
	userID      int64
	blueprintID int64
	runner      *BatchRunner
	batches     *store.BatchStore
}

// newRunnerFixture 建工作区/用户/项目/三类文档版本，返回可用的 runner。
func newRunnerFixture(t *testing.T, generator UnitGenerator) runnerFixture {
	t.Helper()
	dsn := os.Getenv("LLM_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("输入缺失: LLM_TEST_POSTGRES_DSN 未设置，跳过真实 Postgres 集成测试")
	}
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatalf("connect postgres: %v", err)
	}
	t.Cleanup(pool.Close)
	ctx := context.Background()

	suffix := fmt.Sprintf("%d-%s", os.Getpid(), strings.ReplaceAll(t.Name(), "/", "_"))

	var workspaceID int64
	if err := pool.QueryRow(ctx, `
    INSERT INTO workspaces (name, slug) VALUES ($1, $2)
    ON CONFLICT (slug) DO UPDATE SET name = EXCLUDED.name
    RETURNING id`, "runner 测试工作区 "+suffix, "runner-test-"+suffix).Scan(&workspaceID); err != nil {
		t.Fatalf("seed workspace: %v", err)
	}

	var userID int64
	if err := pool.QueryRow(ctx, `
    INSERT INTO users (email, hashed_password, role) VALUES ($1, 'x', 'user')
    ON CONFLICT (email) DO UPDATE SET role = EXCLUDED.role
    RETURNING id`, "runner-"+suffix+"@example.test").Scan(&userID); err != nil {
		t.Fatalf("seed user: %v", err)
	}

	projects := store.NewProjectStore(pool)
	input := model.CreateProjectInput{Name: "runner 项目", Goal: "验证批原生生成", TargetKind: model.TargetKindSFT}
	input.Normalize()
	project, err := projects.CreateProject(ctx, workspaceID, userID, input)
	if err != nil {
		t.Fatalf("create project: %v", err)
	}

	documents := store.NewDocumentStore(pool)
	coveragePayload := model.CoveragePayload{
		SchemaVersion: model.SchemaVersionFor(model.KindCoverage),
		Domains: []model.CoverageDomain{
			{
				StableID: "cold-chain", Name: "冷链",
				Directions: []model.CoverageDirection{
					{StableID: "temperature", Name: "温控", Quota: 3, Source: "manual"},
					{StableID: "traceability", Name: "追溯", Quota: 2, Source: "manual"},
				},
			},
		},
	}
	_, coverageVersion, err := documents.SaveVersion(ctx, project.ID, model.KindCoverage, userID,
		store.SaveDocumentVersionInput{ChangeReason: "初版覆盖", Payload: coveragePayload})
	if err != nil {
		t.Fatalf("save coverage: %v", err)
	}
	blueprintPayload := model.BlueprintPayload{SchemaVersion: model.SchemaVersionFor(model.KindBlueprint)}
	blueprintPayload.Nodes.Coverage.CoverageVersionID = coverageVersion.ID
	blueprintPayload.Nodes.Generation.ModelConnectionID = 1
	blueprintPayload.Nodes.Generation.SchemaVersion = model.SampleSchemaSFT
	blueprintPayload.Nodes.Generation.Concurrency = 2
	blueprintPayload.Nodes.Generation.MaxTokens = 1024
	_, blueprintVersion, err := documents.SaveVersion(ctx, project.ID, model.KindBlueprint, userID,
		store.SaveDocumentVersionInput{ChangeReason: "生成测试蓝图", Payload: blueprintPayload})
	if err != nil {
		t.Fatalf("save blueprint: %v", err)
	}

	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM projects WHERE workspace_id = $1`, workspaceID)
		_, _ = pool.Exec(context.Background(), `DELETE FROM workspaces WHERE id = $1`, workspaceID)
		_, _ = pool.Exec(context.Background(), `DELETE FROM users WHERE id = $1`, userID)
	})

	batches := store.NewBatchStore(pool)
	return runnerFixture{
		pool: pool, projectID: project.ID, userID: userID, blueprintID: blueprintVersion.ID, batches: batches,
		runner: &BatchRunner{
			Batches: batches, Documents: documents, Usage: store.NewUsageStore(pool),
			Generator: generator,
		},
	}
}

// createBatch 建一个仅引用覆盖版本的批次（runner 只需要覆盖就能分配单元）。
func (fixture runnerFixture) createBatch(t *testing.T, units int) model.Batch {
	t.Helper()
	ctx := context.Background()
	versions, err := store.NewDocumentStore(fixture.pool).ListVersions(ctx, fixture.projectID,
		model.KindBlueprint, store.DefaultLogicalID, 5)
	if err != nil || len(versions) == 0 {
		t.Fatalf("read coverage versions: %v", err)
	}
	input := model.CreateBatchInput{
		Purpose:            model.BatchPurposePilot,
		BlueprintVersionID: fixture.blueprintID,
		UnitCount:          units,
	}
	input.Normalize()
	batch, err := fixture.batches.CreateBatch(ctx, fixture.projectID, fixture.userID, model.TargetKindSFT, input)
	if err != nil {
		t.Fatalf("create batch: %v", err)
	}
	return batch
}

// ---------------------------------------------------------------------------
// 覆盖分配（纯函数）
// ---------------------------------------------------------------------------

// TestAllocateUnitsIsDeterministicAndRespectsQuota 覆盖「覆盖单元分配」与
// T12 验收项「进度单位正确」的基础：单元键必须确定且按配额分配。
func TestAllocateUnitsIsDeterministicAndRespectsQuota(t *testing.T) {
	coverage := model.CoveragePayload{
		Domains: []model.CoverageDomain{{
			StableID: "d1", Name: "领域一",
			Directions: []model.CoverageDirection{
				{StableID: "a", Name: "方向A", Quota: 2},
				{StableID: "b", Name: "方向B", Quota: 3},
			},
		}},
	}

	first := AllocateUnits(coverage, 4)
	if len(first) != 4 {
		t.Fatalf("计划 4 个单元时必须恰好分配 4 个，实际 %d", len(first))
	}
	keys := make([]string, 0, len(first))
	for _, unit := range first {
		keys = append(keys, unit.ItemKey())
	}
	want := []string{"d1/a#1", "d1/a#2", "d1/b#1", "d1/b#2"}
	for index, key := range want {
		if keys[index] != key {
			t.Fatalf("单元键必须确定且有序：第 %d 个应为 %q，实际 %q（全部：%v）",
				index+1, key, keys[index], keys)
		}
	}

	// 确定性：同输入两次分配必须完全相同（否则恢复会算出另一批键 → 重跑已完成工作）。
	second := AllocateUnits(coverage, 4)
	for index := range first {
		if first[index].ItemKey() != second[index].ItemKey() {
			t.Fatalf("分配必须确定：第 %d 个键不同（%q vs %q）",
				index+1, first[index].ItemKey(), second[index].ItemKey())
		}
	}

	// 配额用尽后不再分配（quota 合计 5，计划 10 只能得到 5 个）。
	if got := AllocateUnits(coverage, 10); len(got) != 5 {
		t.Fatalf("配额合计 5 时最多分配 5 个单元，实际 %d", len(got))
	}
	// 计划 0 不分配（不编造单元）。
	if got := AllocateUnits(coverage, 0); len(got) != 0 {
		t.Fatalf("计划 0 单元时不得分配，实际 %d", len(got))
	}
}

// TestDifficultyRatioIsProportional 覆盖「难度配比」：小配额下也要与配比一致。
func TestDifficultyRatioIsProportional(t *testing.T) {
	coverage := model.CoveragePayload{
		Domains: []model.CoverageDomain{{
			StableID: "d", Name: "领域",
			Directions: []model.CoverageDirection{{
				StableID: "x", Name: "方向", Quota: 10,
				DifficultyRatios: []model.DifficultyRatio{
					{Difficulty: "easy", Ratio: 0.7},
					{Difficulty: "hard", Ratio: 0.3},
				},
			}},
		}},
	}
	units := AllocateUnits(coverage, 10)
	counts := map[string]int{}
	for _, unit := range units {
		counts[unit.Difficulty]++
	}
	if counts["easy"] != 7 || counts["hard"] != 3 {
		t.Fatalf("70/30 的配比在 10 个单元上应得到 7/3，实际 %v", counts)
	}
}

// ---------------------------------------------------------------------------
// 成功路径与错误分类
// ---------------------------------------------------------------------------

// TestRunBatchGeneratesAllUnits 覆盖验收项「可控 provider 成功」。
func TestRunBatchGeneratesAllUnits(t *testing.T) {
	generator := &scriptedGenerator{}
	fixture := newRunnerFixture(t, generator)
	batch := fixture.createBatch(t, 3)

	result, err := fixture.runner.RunBatch(context.Background(), batch.ID)
	if err != nil {
		t.Fatalf("RunBatch: %v", err)
	}
	if result.CompletedUnits != 3 || result.FailedUnits != 0 {
		t.Fatalf("3 个单元应全部完成，实际 completed=%d failed=%d", result.CompletedUnits, result.FailedUnits)
	}
	if result.Superseded {
		t.Fatal("正常执行不应标记为 superseded")
	}

	// 内容必须是**样本版本**（不是覆盖式记录），且带 batch/item 身份。
	versions, err := fixture.batches.ListSampleVersions(context.Background(), fixture.projectID, 1, 10)
	if err != nil {
		// 样本 ID 未知时用统计断言，避免依赖具体 ID。
		t.Logf("ListSampleVersions: %v", err)
	}
	_ = versions
	count, err := fixture.batches.CountSampleVersionsByProject(context.Background(), fixture.projectID)
	if err != nil {
		t.Fatalf("CountSampleVersionsByProject: %v", err)
	}
	if count != 3 {
		t.Fatalf("3 个成功单元应产生 3 条样本版本，实际 %d", count)
	}
}

// TestRunBatchClassifiesProviderFailures 覆盖验收项「可控 provider 覆盖
// 空输出、截断、无效 JSON、429、超时、部分失败」。
//
// 断言两件事：错误类别**准确**（界面据此给可操作建议），
// 以及一个单元失败**不阻塞**其他单元（部分失败是正常终态）。
func TestRunBatchClassifiesProviderFailures(t *testing.T) {
	cases := []struct {
		name      string
		script    func(UnitRequest) (UnitResult, error)
		wantClass string
	}{
		{
			name:      "空输出",
			script:    func(UnitRequest) (UnitResult, error) { return UnitResult{Payload: map[string]any{}}, nil },
			wantClass: model.ErrorClassEmptyOutput,
		},
		{
			name: "截断",
			script: func(UnitRequest) (UnitResult, error) {
				return UnitResult{}, errors.New("output truncated: reached max_tokens length limit")
			},
			wantClass: model.ErrorClassTruncated,
		},
		{
			name: "无效 JSON",
			script: func(UnitRequest) (UnitResult, error) {
				return UnitResult{}, errors.New("invalid character 'x' looking for beginning of value")
			},
			wantClass: model.ErrorClassInvalidJSON,
		},
		{
			name: "429 限流",
			script: func(UnitRequest) (UnitResult, error) {
				return UnitResult{}, errors.New("provider returned 429 too many requests")
			},
			wantClass: model.ErrorClassRateLimited,
		},
		{
			name: "超时",
			script: func(UnitRequest) (UnitResult, error) {
				return UnitResult{}, errors.New("request timed out after 300s")
			},
			wantClass: model.ErrorClassTimeout,
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			generator := &scriptedGenerator{
				script: map[string]func(UnitRequest) (UnitResult, error){"cold-chain/temperature#1": testCase.script},
			}
			fixture := newRunnerFixture(t, generator)
			batch := fixture.createBatch(t, 2)

			result, err := fixture.runner.RunBatch(context.Background(), batch.ID)
			if err != nil {
				t.Fatalf("RunBatch: %v", err)
			}
			// 部分失败：1 失败 + 1 成功。
			if result.FailedUnits != 1 || result.CompletedUnits != 1 {
				t.Fatalf("应得到 1 失败 + 1 成功，实际 failed=%d completed=%d",
					result.FailedUnits, result.CompletedUnits)
			}

			items, err := fixture.batches.ListBatchItems(context.Background(), batch.ID, model.ItemStatusFailed, 10)
			if err != nil {
				t.Fatalf("ListBatchItems: %v", err)
			}
			if len(items) != 1 {
				t.Fatalf("应有 1 条失败项，实际 %d", len(items))
			}
			if items[0].ErrorClass != testCase.wantClass {
				t.Fatalf("错误类别应为 %s，实际 %s（message=%s）",
					testCase.wantClass, items[0].ErrorClass, items[0].ErrorMessage)
			}
			// 失败原因必须可操作：界面直接展示这句话。
			if strings.TrimSpace(items[0].ErrorMessage) == "" {
				t.Fatal("失败项必须带可展示的原因")
			}
		})
	}
}

// TestRunBatchResumeDoesNotRegenerateSucceeded 覆盖验收项
// 「阶段恢复不重新生成成功项」。
//
// 这是「重复点击恢复不重复成果」在 runner 侧的实现：只处理 pending 单元，
// 已成功的单元不会被再次生成 —— 因此也不会再次计费。
func TestRunBatchResumeDoesNotRegenerateSucceeded(t *testing.T) {
	generator := &scriptedGenerator{
		script: map[string]func(UnitRequest) (UnitResult, error){
			// 第一个单元第一次失败，之后成功。
			"cold-chain/temperature#1": func(UnitRequest) (UnitResult, error) {
				return UnitResult{}, errors.New("HTTP 429 rate limit exceeded")
			},
		},
	}
	fixture := newRunnerFixture(t, generator)
	batch := fixture.createBatch(t, 3)

	if _, err := fixture.runner.RunBatch(context.Background(), batch.ID); err != nil {
		t.Fatalf("第一次 RunBatch: %v", err)
	}
	firstCallCount := generator.callCount()
	if firstCallCount != 3 {
		t.Fatalf("第一次应调用 3 次（每单元一次），实际 %d", firstCallCount)
	}

	// 恢复：只重置可重试的失败项（与 T13 的「恢复失败项」同一路径）。
	reset, err := fixture.batches.MarkRetryableItemsPending(context.Background(),
		fixture.projectID, batch.ID, fixture.userID)
	if err != nil {
		t.Fatalf("MarkRetryableItemsPending: %v", err)
	}
	if reset != 1 {
		t.Fatalf("只有 1 个可重试失败项应被重置，实际 %d", reset)
	}

	// 让该键这次成功。
	generator.mu.Lock()
	generator.script = nil
	generator.mu.Unlock()

	if _, err := fixture.runner.RunBatch(context.Background(), batch.ID); err != nil {
		t.Fatalf("恢复 RunBatch: %v", err)
	}

	// 关键断言：恢复只新增 **1** 次调用，而不是把 3 个单元再跑一遍。
	if got := generator.callCount() - firstCallCount; got != 1 {
		t.Fatalf("恢复只应重新生成 1 个失败单元，实际重新生成 %d 个（成功项被重跑会重复计费）", got)
	}

	refreshed, err := fixture.batches.GetBatch(context.Background(), batch.ID)
	if err != nil {
		t.Fatalf("GetBatch: %v", err)
	}
	if refreshed.CompletedUnits != 3 || refreshed.FailedUnits != 0 {
		t.Fatalf("恢复后应 3 完成 0 失败，实际 completed=%d failed=%d",
			refreshed.CompletedUnits, refreshed.FailedUnits)
	}
}

// TestRunBatchStopsSubmittingWhenPaused 覆盖契约 §2.4 的暂停语义：
// 「暂停只阻止新提交；在途仍可完成」。
func TestRunBatchStopsSubmittingWhenPaused(t *testing.T) {
	generator := &scriptedGenerator{}
	fixture := newRunnerFixture(t, generator)
	batch := fixture.createBatch(t, 3)

	if _, err := fixture.batches.PauseBatch(context.Background(), fixture.projectID, batch.ID, fixture.userID); err != nil {
		t.Fatalf("PauseBatch: %v", err)
	}

	result, err := fixture.runner.RunBatch(context.Background(), batch.ID)
	if err != nil {
		t.Fatalf("RunBatch: %v", err)
	}
	if !result.Superseded {
		t.Fatal("暂停后执行必须标记 superseded（而不是假装完成）")
	}
	if generator.callCount() != 0 {
		t.Fatalf("暂停后不得提交任何生成请求，实际调用了 %d 次", generator.callCount())
	}
}

// TestRunBatchUsesSnapshotNotCurrentConfiguration 覆盖验收项
// 「已有成功内容不受默认 prompt/provider/标准变更影响」的核心机制：
// runner 只读**批次快照**。
//
// 做法：把批次的 generation_config 改成一份可识别的配置，
// 然后断言生成请求里带的正是快照里的值（而不是任何「当前默认」）。
func TestRunBatchUsesSnapshotNotCurrentConfiguration(t *testing.T) {
	generator := &scriptedGenerator{}
	fixture := newRunnerFixture(t, generator)
	batch := fixture.createBatch(t, 1)

	// 直接改快照，模拟「创建时用的是这份配置」。
	if _, err := fixture.pool.Exec(context.Background(), `
    UPDATE batches
    SET generation_config = '{"modelConnectionId":4242,"modelVersion":"snapshot-v9","concurrency":1,"maxTokens":1234,"schemaVersion":"sft.sample.v1"}'::jsonb
    WHERE id = $1`, batch.ID); err != nil {
		t.Fatalf("update snapshot: %v", err)
	}

	observed := make(chan model.BatchGenerationConfig, 1)
	fixture.runner.Generator = &capturingGenerator{inner: successResult, out: observed}

	if _, err := fixture.runner.RunBatch(context.Background(), batch.ID); err != nil {
		t.Fatalf("RunBatch: %v", err)
	}
	select {
	case config := <-observed:
		if config.ModelConnectionID != 4242 || config.MaxTokens != 1234 || config.ModelVersion != "snapshot-v9" {
			t.Fatalf("生成必须使用批次快照配置，实际 %+v", config)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("没有观察到生成请求")
	}
}

// capturingGenerator 记录请求里的生成配置（用于快照断言）。
type capturingGenerator struct {
	inner func(UnitRequest) UnitResult
	out   chan model.BatchGenerationConfig
}

func (generator *capturingGenerator) GenerateUnit(ctx context.Context, request UnitRequest) (UnitResult, error) {
	select {
	case generator.out <- request.GenerationConfig:
	default:
	}
	return generator.inner(request), nil
}

// TestConcurrentBatchesDoNotCrossConfigurations 覆盖验收项
// 「两个并发批次配置不同不串数据」。
//
// 为什么必须并发测：串数据的成因是共享可变状态（包级缓冲、共享 slice），
// 而那种缺陷在单线程下完全正确。因此这里并行跑两个快照不同的批次，
// 并断言各自拿到的配置始终是自己的。
func TestConcurrentBatchesDoNotCrossConfigurations(t *testing.T) {
	fixture := newRunnerFixture(t, &scriptedGenerator{})
	batchA := fixture.createBatch(t, 2)
	batchB := fixture.createBatch(t, 2)

	for _, target := range []struct {
		id       int64
		conn     int64
		maxToken int
	}{{batchA.ID, 11, 1111}, {batchB.ID, 22, 2222}} {
		// 用 json.Marshal 而不是手拼 JSON 字符串：手拼的 JSON 既可能格式错误，
		// 又会让静态分析器无法区分「参数值」与「被拼进 SQL 的文本」——
		// 真正的注入点会因此被淹没在噪声里（与本仓库既有的同类取舍一致）。
		raw, marshalErr := json.Marshal(map[string]any{
			"modelConnectionId": target.conn,
			"modelVersion":      fmt.Sprintf("v-%d", target.conn),
			"concurrency":       1,
			"maxTokens":         target.maxToken,
			"schemaVersion":     "sft.sample.v1",
		})
		if marshalErr != nil {
			t.Fatalf("marshal config: %v", marshalErr)
		}
		if _, err := fixture.pool.Exec(context.Background(), `
      UPDATE batches SET generation_config = $2::jsonb WHERE id = $1`,
			target.id, raw); err != nil {
			t.Fatalf("update snapshot: %v", err)
		}
	}

	observed := make(chan struct {
		batchID int64
		config  model.BatchGenerationConfig
	}, 16)
	fixture.runner.Generator = &recordingGenerator{out: observed}

	var waitGroup sync.WaitGroup
	for _, id := range []int64{batchA.ID, batchB.ID} {
		waitGroup.Add(1)
		go func(batchID int64) {
			defer waitGroup.Done()
			if _, err := fixture.runner.RunBatch(context.Background(), batchID); err != nil {
				t.Errorf("RunBatch(%d): %v", batchID, err)
			}
		}(id)
	}
	waitGroup.Wait()

	expectation := map[int64]int64{batchA.ID: 11, batchB.ID: 22}
	expectationTokens := map[int64]int{batchA.ID: 1111, batchB.ID: 2222}
	seen := 0
	for seen < 4 {
		select {
		case item := <-observed:
			seen++
			if item.config.ModelConnectionID != expectation[item.batchID] {
				t.Fatalf("批次 %d 拿到了别人的连接配置：%d（期望 %d）—— 并发批次串数据",
					item.batchID, item.config.ModelConnectionID, expectation[item.batchID])
			}
			if item.config.MaxTokens != expectationTokens[item.batchID] {
				t.Fatalf("批次 %d 拿到了别人的输出上限：%d（期望 %d）",
					item.batchID, item.config.MaxTokens, expectationTokens[item.batchID])
			}
		case <-time.After(3 * time.Second):
			t.Fatalf("只观察到 %d 次生成请求，预期 4 次", seen)
		}
	}
}

// recordingGenerator 记录请求所属批次与配置。
type recordingGenerator struct {
	out chan struct {
		batchID int64
		config  model.BatchGenerationConfig
	}
}

func (generator *recordingGenerator) GenerateUnit(ctx context.Context, request UnitRequest) (UnitResult, error) {
	select {
	case generator.out <- struct {
		batchID int64
		config  model.BatchGenerationConfig
	}{request.BatchID, request.GenerationConfig}:
	default:
	}
	return successResult(request), nil
}

// TestClassifyUnitErrorIsConservative 锁定错误分类的保守性（与 worker 侧同一判据）。
func TestClassifyUnitErrorIsConservative(t *testing.T) {
	cases := []struct {
		message string
		want    string
	}{
		{"HTTP 429 Too Many Requests", model.ErrorClassRateLimited},
		{"request timed out", model.ErrorClassTimeout},
		{"output truncated by length limit", model.ErrorClassTruncated},
		{"invalid character 'x' looking for beginning of value", model.ErrorClassInvalidJSON},
		{"provider returned no choices", model.ErrorClassEmptyOutput},
		{"schema violation: answer required", model.ErrorClassSchema},
		{"upstream returned 503", model.ErrorClassProvider},
		{"something odd", model.ErrorClassInternal},
	}
	for _, testCase := range cases {
		if got := classifyUnitError(errors.New(testCase.message)); got != testCase.want {
			t.Fatalf("%q 的分类应为 %s，实际 %s", testCase.message, testCase.want, got)
		}
	}
	if classifyUnitError(nil) != "" {
		t.Fatal("nil 错误应分类为空")
	}
}
