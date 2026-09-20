package store

import (
	"context"
	"fmt"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

// TestMarkQueuedIfNotTerminalDoesNotOverwriteTerminalState 锁定 issue #142 的修复。
//
// 背景：`startEvalRun` 此前**无条件**把运行写成 queued（"先把运行标为 queued 再入队"）。
// 若 worker 正在并发完成这条运行（写入 completed / partial_failed），
// API 随后又把状态写成 queued，最终落库的就是 queued —— 而队列里已经没有任务
//（已被消费），于是运行**永久停在 queued**，同时 scored_items 是满的。
//
// 实测（eval_run 22）：
//
//	worker 日志:  02:38:46 eval.run.done run=22 dataset=149 items=8 scored=8 status=completed
//	数据库:       status=queued  scored_items=8/8  updated_at=03:24:35
//
// 界面上「已入队」与「100% 已打分 8/8」同时出现，报告也已可读 ——
// 用户以为还要等，其实早就完成了。
//
// 本测试连真实 Postgres，断言 MarkQueuedIfNotTerminal：
//  1. 对终态（completed / partial_failed / failed）**不覆盖**；
//  2. 对非终态（draft/queued/running）正常置为 queued。
func TestMarkQueuedIfNotTerminalDoesNotOverwriteTerminalState(t *testing.T) {
	dsn := os.Getenv("LLM_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("输入缺失: LLM_TEST_POSTGRES_DSN 未设置，跳过真实 Postgres 集成测试")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect postgres: %v", err)
	}
	t.Cleanup(pool.Close)

	// 造一个独立数据集承载测试运行（唯一前缀，结束按 id 精确删除）。
	name := fmt.Sprintf("l15-eval-markqueued-%d", os.Getpid())
	var datasetID int64
	if err := pool.QueryRow(ctx, `
    INSERT INTO datasets (name, root_keyword, status)
    VALUES ($1, '评估状态测试', 'draft') RETURNING id`, name).Scan(&datasetID); err != nil {
		t.Fatalf("seed dataset: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM datasets WHERE id = $1`, datasetID)
	})

	runs := NewEvalRunStore(pool)
	create := func() int64 {
		var id int64
		if err := pool.QueryRow(ctx, `
      INSERT INTO eval_runs (dataset_id, name, status, sampling_mode, sample_ratio, sample_size, dimension_keys)
      VALUES ($1, $2, 'draft', 'full', 0, 0, '{}') RETURNING id`,
			datasetID, name).Scan(&id); err != nil {
			t.Fatalf("seed eval_run: %v", err)
		}
		return id
	}

	// 1) 终态不得被覆盖。
	for _, terminal := range []string{"completed", "partial_failed", "failed"} {
		runID := create()
		if _, err := pool.Exec(ctx, `UPDATE eval_runs SET status = $2 WHERE id = $1`, runID, terminal); err != nil {
			t.Fatalf("set terminal %s: %v", terminal, err)
		}
		if err := runs.MarkQueuedIfNotTerminal(ctx, runID, 8, 8); err != nil {
			t.Fatalf("MarkQueuedIfNotTerminal: %v", err)
		}
		var got string
		if err := pool.QueryRow(ctx, `SELECT status FROM eval_runs WHERE id = $1`, runID).Scan(&got); err != nil {
			t.Fatalf("read status: %v", err)
		}
		if got != terminal {
			t.Errorf("终态 %q 被 MarkQueuedIfNotTerminal 覆盖成 %q —— 这正是 issue #142："+
				"已完成的评估被打回 queued 且永久卡住", terminal, got)
		}
	}

	// 2) 非终态应正常置为 queued（这是该方法存在的理由）。
	runID := create()
	if err := runs.MarkQueuedIfNotTerminal(ctx, runID, 8, 0); err != nil {
		t.Fatalf("MarkQueuedIfNotTerminal (draft): %v", err)
	}
	var got string
	if err := pool.QueryRow(ctx, `SELECT status FROM eval_runs WHERE id = $1`, runID).Scan(&got); err != nil {
		t.Fatalf("read status: %v", err)
	}
	if got != "queued" {
		t.Errorf("draft 运行应被置为 queued（worker 靠它反查待跑运行），实际 %q", got)
	}
}
