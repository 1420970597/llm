package store

import (
	"context"
	"fmt"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

// TestPipelineProgressDoesNotClaimCompletedWithoutEvidence 锁定 R11 lane 独立评审者
// 发现、父代理用 dataset 50 活体复现的那个缺陷。
//
// 缺陷形态：PipelineProgress 的 stageState 只按 status 的 rank 判定 completed，
// **完全不看该阶段是否有实际记录**。于是走 SFT 分支的数据集（迁移 0018 把
// sft_records 作为一等公民，SFT 与 GRPO 两条分支互斥，因此 SFT 分支不产生
// reasoning_records / reward_records）在终态时会呈现：
//
//	reasoning  completed  count=0   已生成 0 条推理
//	rewards    completed  count=0   已生成 0 条评分
//
// 「已完成 · 0 条」自相矛盾：用户无法分辨这是「真的做完了但没数据」还是
// 「状态推进错了」，会误以为流水线漏跑了两个阶段。
//
// 修复后：rank 达标但零产出 → "skipped"（本数据集不需要这个阶段），
// 与 "completed" 明确区分。
//
// 为什么必须连真实 Postgres：PipelineProgress 的输入全部来自 SQL COUNT 与
// datasets 行，纯函数单测无法构造这个形状。未设置 LLM_TEST_POSTGRES_DSN 时跳过
// （避免在无数据库环境里伪装通过）。
func TestPipelineProgressDoesNotClaimCompletedWithoutEvidence(t *testing.T) {
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

	// 造一个「状态已到终态、但 reasoning/rewards 零记录」的数据集 ——
	// 即 SFT 分支的正常终态形状。
	name := fmt.Sprintf("l15-progress-test-%d", os.Getpid())
	var datasetID int64
	if err := pool.QueryRow(ctx, `
    INSERT INTO datasets (name, root_keyword, status, target_kind)
    VALUES ($1, '进度一致性测试', 'export_generated', 'sft')
    RETURNING id`, name).Scan(&datasetID); err != nil {
		t.Fatalf("seed dataset: %v", err)
	}
	t.Cleanup(func() {
		// 精确条件删除；子表靠 ON DELETE CASCADE 清理。
		_, _ = pool.Exec(context.Background(), `DELETE FROM datasets WHERE id = $1`, datasetID)
	})

	// 至少要有领域/问题/工件，才能让 domains/questions/export 阶段有产出，
	// 从而把断言聚焦在「零产出的 reasoning/rewards 不得显示为 completed」。
	if _, err := pool.Exec(ctx, `
    INSERT INTO domains (dataset_id, name, canonical_name, level)
    VALUES ($1, '测试领域', '测试领域', 1)`, datasetID); err != nil {
		t.Fatalf("seed domain: %v", err)
	}

	store := NewDatasetStore(pool, nil)
	progress, err := store.PipelineProgress(ctx, datasetID)
	if err != nil {
		t.Fatalf("PipelineProgress: %v", err)
	}

	byKey := map[string]string{}
	counts := map[string]int{}
	for _, stage := range progress.Stages {
		byKey[stage.Key] = stage.State
		counts[stage.Key] = stage.Count
	}

	// 核心断言：零产出的阶段不得声称 completed。
	for _, key := range []string{"reasoning", "rewards"} {
		if counts[key] != 0 {
			t.Fatalf("前置条件不成立：%s 阶段本应零产出，实际 count=%d", key, counts[key])
		}
		if byKey[key] == "completed" {
			t.Errorf("阶段 %s 零产出却报告 completed —— 界面会显示「已完成 · 0 条」这种自相矛盾的文案（R11 评审者发现的缺陷）", key)
		}
		if byKey[key] != "skipped" {
			t.Errorf("阶段 %s 零产出时应为 skipped（本数据集不需要该阶段），实际 %q", key, byKey[key])
		}
	}

	// 有产出的阶段仍应正常报告 completed（防矫枉过正：不能把「跳过」当成万能答案）。
	if counts["domains"] > 0 && byKey["domains"] != "completed" {
		t.Errorf("domains 有 %d 条产出，应报告 completed，实际 %q", counts["domains"], byKey["domains"])
	}
}
