package store

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// 本文件验证 issue #9：同一 (dataset_id, stage) 的活跃运行记录必须唯一。
//
// 关键不变量落在数据库层（部分唯一索引 uniq_generation_runs_active +
// StartRun 的单语句 ON CONFLICT upsert），纯函数单测无法证明，因此这里连真实
// Postgres。未设置 LLM_TEST_POSTGRES_DSN 时跳过，避免在无数据库环境里伪装通过。
//
// 本地运行（在 compose 网络内）：
//
//	docker run --rm --network llm_default \
//	  -v $PWD:/w -w /w \
//	  -e LLM_TEST_POSTGRES_DSN="postgres://llm_factory:llm_factory_dev@llm-postgres-1:5432/llm_factory?sslmode=disable" \
//	  -v llm-gomodcache:/go/pkg/mod -v llm-gocache:/root/.cache/go-build \
//	  golang:1.24-alpine sh -c "go test ./internal/store/ -run TestGenerationRun -v"

func newGenerationRunTestPool(t *testing.T) *pgxpool.Pool {
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
	return pool
}

// seedGenerationRunDataset 建一个空的测试数据集，返回其 id。
// 唯一名字前缀 l15-r4-test-<pid>-，清理时按 id 精确删除（级联清 generation_runs）。
func seedGenerationRunDataset(t *testing.T, pool *pgxpool.Pool) int64 {
	t.Helper()
	var id int64
	name := fmt.Sprintf("l15-r4-test-%d-%s", os.Getpid(), t.Name())
	err := pool.QueryRow(context.Background(), `
    INSERT INTO datasets (name, root_keyword, status)
    VALUES ($1, '并发测试关键词', 'draft')
    RETURNING id`, name).Scan(&id)
	if err != nil {
		t.Fatalf("seed dataset: %v", err)
	}
	t.Cleanup(func() {
		// 精确条件删除；datasets 的 ON DELETE CASCADE 会带走 generation_runs。
		_, _ = pool.Exec(context.Background(), `DELETE FROM datasets WHERE id = $1`, id)
	})
	return id
}

// activeRunCount 统计某 (dataset_id, stage) 的活跃记录数。
func activeRunCount(t *testing.T, pool *pgxpool.Pool, datasetID int64, stage string) int {
	t.Helper()
	var count int
	err := pool.QueryRow(context.Background(), `
    SELECT COUNT(*) FROM generation_runs
    WHERE dataset_id = $1 AND stage = $2 AND status IN ('pending', 'running')`,
		datasetID, stage).Scan(&count)
	if err != nil {
		t.Fatalf("count active runs: %v", err)
	}
	return count
}

// TestGenerationRunStartRunConcurrentCreatesSingleActiveRecord 是本 lane 的核心断言：
// 16 个 goroutine 同时 StartRun，最终活跃记录恰好 1 条，且所有调用返回同一个 run id。
//
// 修复前（SELECT → INSERT 无互斥）这条测试必然失败：并发的调用方都查到
// pgx.ErrNoRows，各自插入一条 running，孤儿永久无法被 ActiveRun 读到/被 FinishRun 结束。
func TestGenerationRunStartRunConcurrentCreatesSingleActiveRecord(t *testing.T) {
	pool := newGenerationRunTestPool(t)
	ctx := context.Background()
	datasetID := seedGenerationRunDataset(t, pool)
	const stage = "l15-r4-concurrent"

	const goroutines = 16
	store := NewGenerationRunStore(pool)

	type startResult struct {
		index int
		runID int64
		err   error
	}
	results := make(chan startResult, goroutines)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			<-start // 尽量让所有 goroutine 同时进入
			run, err := store.StartRun(ctx, datasetID, stage, 5)
			results <- startResult{index: idx, runID: run.ID, err: err}
		}(i)
	}
	close(start)
	wg.Wait()
	close(results)

	// 结果经 channel 汇总后再断言，避免在 goroutine 内写共享切片。
	ids := make([]int64, goroutines)
	seen := make([]bool, goroutines)
	for result := range results {
		if result.err != nil {
			t.Fatalf("goroutine %d StartRun 失败: %v", result.index, result.err)
		}
		ids[result.index] = result.runID
		seen[result.index] = true
	}
	for i, ok := range seen {
		if !ok {
			t.Fatalf("goroutine %d 未返回结果", i)
		}
	}
	for i, id := range ids {
		if id != ids[0] {
			t.Fatalf("并发 StartRun 返回了不同的 run id：goroutine 0 -> %d，goroutine %d -> %d",
				ids[0], i, id)
		}
	}

	if got := activeRunCount(t, pool, datasetID, stage); got != 1 {
		t.Fatalf("活跃记录数必须为 1，实际 %d（存在并发产生的孤儿，issue #9 未修复）", got)
	}

	// ActiveRun 必须能读到那条唯一活跃记录，且就是 StartRun 返回的那条。
	active, err := store.ActiveRun(ctx, datasetID, stage)
	if err != nil {
		t.Fatalf("ActiveRun: %v", err)
	}
	if active.ID != ids[0] {
		t.Fatalf("ActiveRun 读到 id=%d，StartRun 返回 id=%d，两者必须一致", active.ID, ids[0])
	}
	// 复用次数 = 并发调用数：首次插入 attempts=1，其余 goroutine 各 +1。
	if active.Attempts != goroutines {
		t.Fatalf("attempts 必须等于并发调用次数 %d（说明每次复用都命中同一条记录），实际 %d",
			goroutines, active.Attempts)
	}
}

// TestGenerationRunStartRunIsAtomicAgainstConcurrentInsert 是「StartRun 必须是单语句
// 原子 upsert」的**确定性**守卫，不依赖 goroutine 调度时序。
//
// 手法：用一条未提交的事务占住唯一索引，精确复现旧实现的并发窗口。
//
//	A: BEGIN; INSERT 一条活跃记录;      ← 未提交（SELECT 看不到它，但索引项已存在）
//	B: StartRun(...)                    ← 旧实现此时 SELECT 仍然看到「无记录」
//	A: COMMIT;                          ← B 解除阻塞
//
// 旧实现（SELECT→INSERT）在 A 提交后必然报 23505 duplicate key；
// 新实现（INSERT ... ON CONFLICT DO UPDATE）在同一时刻把冲突解析为复用，正常返回。
//
// 为什么不用纯 goroutine 并发：实测 16 goroutine 的并发测试打在旧实现上约五次才命中一次，
// 那样的守卫会让 CI 时好时坏。这里用未提交事务做屏障，把「两步之间的窗口」变成必然事件。
func TestGenerationRunStartRunIsAtomicAgainstConcurrentInsert(t *testing.T) {
	pool := newGenerationRunTestPool(t)
	ctx := context.Background()
	datasetID := seedGenerationRunDataset(t, pool)
	const stage = "l15-r4-atomic"

	// 连接 A：开事务并插入活跃记录，先不提交。
	blocker, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatalf("acquire blocker conn: %v", err)
	}
	defer blocker.Release()
	tx, err := blocker.Begin(ctx)
	if err != nil {
		t.Fatalf("begin blocker tx: %v", err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	var blockerID int64
	if err := tx.QueryRow(ctx, `
    INSERT INTO generation_runs (dataset_id, stage, status, total_units)
    VALUES ($1, $2, 'running', 5)
    RETURNING id`, datasetID, stage).Scan(&blockerID); err != nil {
		t.Fatalf("blocker insert: %v", err)
	}

	// 连接 B：StartRun。旧实现在这里会先 SELECT（看不到 A 的未提交行），
	// 然后 INSERT 并在索引上阻塞。
	store := NewGenerationRunStore(pool)
	type outcome struct {
		runID int64
		err   error
	}
	done := make(chan outcome, 1)
	go func() {
		run, err := store.StartRun(ctx, datasetID, stage, 5)
		done <- outcome{runID: run.ID, err: err}
	}()

	// 给 B 足够时间走到「已经 SELECT 过、正阻塞在 INSERT」的位置，再让 A 提交。
	select {
	case result := <-done:
		t.Fatalf("StartRun 不应在 A 提交前完成（说明障碍未生效）：id=%d err=%v", result.runID, result.err)
	case <-time.After(400 * time.Millisecond):
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit blocker tx: %v", err)
	}

	var result outcome
	select {
	case result = <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("StartRun 在 A 提交后仍未返回（可能死锁）")
	}
	if result.err != nil {
		t.Fatalf("StartRun 必须把并发冲突解析为复用而不是报错（issue #9：read-then-insert 会在此报23505）: %v", result.err)
	}
	if result.runID != blockerID {
		t.Fatalf("StartRun 必须复用 A 那条活跃记录：期望 id=%d 实际 id=%d", blockerID, result.runID)
	}
	if got := activeRunCount(t, pool, datasetID, stage); got != 1 {
		t.Fatalf("活跃记录数必须为 1，实际 %d", got)
	}
}

// TestGenerationRunDuplicateActiveInsertRejected 是 issue #9 的**确定性**回归守卫。
//
// 为什么不能只靠上面的并发测试：并发测试的语义断言（最终 1 条活跃记录）依赖
// 「两个 goroutine 恰好都在对方 INSERT 前完成 SELECT」这一时序。实测在 16 goroutine
// 的负载下，旧实现（SELECT→INSERT 无互斥）**仍然可能恰好不撞上而通过**——那是没被
// 触发的竞态，不是被守住的缺陷。因此这里直接复现旧实现的**两步序列**并断言数据库拒绝：
//
//  1. SELECT 活跃记录（旧实现的第一步：并发时两个请求都会拿到「无记录」）；
//  2. INSERT 一条活跃记录（成功）；
//  3. 再 INSERT 一条同 (dataset_id, stage) 的活跃记录 —— 必须被唯一索引拒绝。
//
// 第 3 步在修复前会成功（线上实测可直接插出两条 running），修复后必然报 SQLSTATE 23505。
// 这条断言不依赖调度时序，因此是稳定的守卫。
func TestGenerationRunDuplicateActiveInsertRejected(t *testing.T) {
	pool := newGenerationRunTestPool(t)
	ctx := context.Background()
	datasetID := seedGenerationRunDataset(t, pool)
	const stage = "l15-r4-duplicate"

	store := NewGenerationRunStore(pool)
	// 第一步：旧实现的并发窗口中，两个请求都会看到「没有活跃记录」。
	if _, err := store.ActiveRun(ctx, datasetID, stage); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("前置条件不成立：空阶段上 ActiveRun 应当返回 pgx.ErrNoRows，实际 err=%v", err)
	}

	// 第二步：请求 A 插入活跃记录。
	first, err := store.StartRun(ctx, datasetID, stage, 5)
	if err != nil {
		t.Fatalf("请求 A 的 StartRun 应当成功: %v", err)
	}

	// 第三步：请求 B（仍然拿的是第一步那个过期的「无记录」结论）直接 INSERT，
	// 模拟旧实现绕过唯一的第二条语句。
	_, err = pool.Exec(ctx, `
    INSERT INTO generation_runs (dataset_id, stage, status, total_units)
    VALUES ($1, $2, 'running', 5)`, datasetID, stage)
	if err == nil {
		t.Fatalf("重复活跃记录被接受：dataset=%d stage=%s 已存在活跃记录 id=%d，"+
			"第二条 running 仍插入成功 —— 部分唯一索引缺失（issue #9 未修复）",
			datasetID, stage, first.ID)
	}
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		t.Fatalf("期望 Postgres 错误（唯一约束冲突），实际: %v", err)
	}
	if pgErr.Code != "23505" {
		t.Fatalf("期望 SQLSTATE 23505（unique_violation），实际 %s: %s", pgErr.Code, pgErr.Message)
	}
	if pgErr.ConstraintName != "uniq_generation_runs_active" {
		t.Fatalf("期望冲突来自 uniq_generation_runs_active，实际 %s", pgErr.ConstraintName)
	}

	// 反证：旧的两步序列换成新实现的单语句 upsert 后，第二条语句不再报错，
	// 而是复用到同一条记录。
	second, err := store.StartRun(ctx, datasetID, stage, 5)
	if err != nil {
		t.Fatalf("修复后的 StartRun 必须复用而不是报错: %v", err)
	}
	if second.ID != first.ID {
		t.Fatalf("复用必须命中同一条记录：first=%d second=%d", first.ID, second.ID)
	}
	if got := activeRunCount(t, pool, datasetID, stage); got != 1 {
		t.Fatalf("活跃记录数必须仍为 1，实际 %d", got)
	}
}

// TestGenerationRunStartRunPreservesCursorOnReuse 守住「复用不覆盖断点进度」不变量：
// 复用已有活跃记录时，cursor / total_units / done_units 必须保留，否则续跑退化为从头重跑
// （saveJobCursor 的先 StartRun 后 SaveCursor 顺序依赖这一点）。
func TestGenerationRunStartRunPreservesCursorOnReuse(t *testing.T) {
	pool := newGenerationRunTestPool(t)
	ctx := context.Background()
	datasetID := seedGenerationRunDataset(t, pool)
	const stage = "l15-r4-cursor"

	store := NewGenerationRunStore(pool)
	first, err := store.StartRun(ctx, datasetID, stage, 10)
	if err != nil {
		t.Fatalf("首次 StartRun: %v", err)
	}
	if err := store.SaveCursor(ctx, first.ID, map[string]any{"completedDomainIds": []int64{1, 2}}, 2, 10); err != nil {
		t.Fatalf("SaveCursor: %v", err)
	}

	second, err := store.StartRun(ctx, datasetID, stage, 999)
	if err != nil {
		t.Fatalf("复用 StartRun: %v", err)
	}
	if second.ID != first.ID {
		t.Fatalf("复用必须返回同一条记录：first=%d second=%d", first.ID, second.ID)
	}
	if second.TotalUnits != 10 || second.DoneUnits != 2 {
		t.Fatalf("复用不得覆盖进度：total_units 期望 10 实际 %d，done_units 期望 2 实际 %d",
			second.TotalUnits, second.DoneUnits)
	}
	if got := len(second.Cursor["completedDomainIds"].([]any)); got != 2 {
		t.Fatalf("复用不得覆盖 cursor：completedDomainIds 期望 2 项，实际 %d 项", got)
	}
	if second.Attempts != 2 {
		t.Fatalf("复用必须累加 attempts：期望 2 实际 %d", second.Attempts)
	}
}

// TestGenerationRunFinishRunAllowsNewActiveRun 证明部分唯一索引只约束活跃态：
// 终态记录不参与唯一性，同一 (dataset_id, stage) 结束后可以再开一条新记录，
// 且旧记录仍留在列表里（断点续跑的历史证据不能被覆盖）。
func TestGenerationRunFinishRunAllowsNewActiveRun(t *testing.T) {
	pool := newGenerationRunTestPool(t)
	ctx := context.Background()
	datasetID := seedGenerationRunDataset(t, pool)
	const stage = "l15-r4-finish"

	store := NewGenerationRunStore(pool)
	first, err := store.StartRun(ctx, datasetID, stage, 3)
	if err != nil {
		t.Fatalf("首次 StartRun: %v", err)
	}
	if err := store.FinishRun(ctx, first.ID, "completed", ""); err != nil {
		t.Fatalf("FinishRun: %v", err)
	}

	second, err := store.StartRun(ctx, datasetID, stage, 3)
	if err != nil {
		t.Fatalf("终态后再次 StartRun 必须成功（部分索引不该拦住历史记录）: %v", err)
	}
	if second.ID == first.ID {
		t.Fatal("终态记录不能被复用为新的活跃记录，必须是新插入的一条")
	}
	if got := activeRunCount(t, pool, datasetID, stage); got != 1 {
		t.Fatalf("活跃记录数必须仍为 1，实际 %d", got)
	}

	runs, err := store.ListRuns(ctx, datasetID)
	if err != nil {
		t.Fatalf("ListRuns: %v", err)
	}
	if len(runs) != 2 {
		t.Fatalf("同一阶段的历史运行记录必须保留：期望 2 条，实际 %d 条", len(runs))
	}
}

// TestGenerationRunActiveUniqueIndexExists 直接对数据库断言索引存在且是部分唯一索引。
//
// 这条断言的意义：迁移 0020 只在本文件与 test/l15_worker_concurrency.py 里被依赖，
// 若索引被误删或误建成普通索引，上面几条并发测试可能因为「恰好没撞上」而偶发通过；
// 这里显式检查定义，失败信息明确指向迁移缺失。
func TestGenerationRunActiveUniqueIndexExists(t *testing.T) {
	pool := newGenerationRunTestPool(t)
	var indexDef string
	err := pool.QueryRow(context.Background(), `
    SELECT indexdef FROM pg_indexes
    WHERE tablename = 'generation_runs' AND indexname = 'uniq_generation_runs_active'`).Scan(&indexDef)
	if err != nil {
		t.Fatalf("部分唯一索引 uniq_generation_runs_active 不存在（迁移 0020 未应用）: %v", err)
	}
	for _, want := range []string{"UNIQUE", "(dataset_id, stage)", "WHERE"} {
		if !containsFold(indexDef, want) {
			t.Fatalf("索引定义缺少 %q，实际定义：%s", want, indexDef)
		}
	}
	if !containsFold(indexDef, "pending") || !containsFold(indexDef, "running") {
		t.Fatalf("索引的活跃态谓词必须同时覆盖 pending 与 running，实际定义：%s", indexDef)
	}
}

func containsFold(haystack, needle string) bool {
	return len(needle) == 0 || indexFold(haystack, needle) >= 0
}

func indexFold(haystack, needle string) int {
	h := []rune(lowerASCII(haystack))
	n := []rune(lowerASCII(needle))
	if len(n) == 0 {
		return 0
	}
	for i := 0; i+len(n) <= len(h); i++ {
		match := true
		for j := range n {
			if h[i+j] != n[j] {
				match = false
				break
			}
		}
		if match {
			return i
		}
	}
	return -1
}

func lowerASCII(value string) string {
	out := []rune(value)
	for i, r := range out {
		if r >= 'A' && r <= 'Z' {
			out[i] = r + ('a' - 'A')
		}
	}
	return string(out)
}
