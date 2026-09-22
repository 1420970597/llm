package studio

import (
	"context"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

// 本文件验证 Issue #160 T33 的运维快照（真实 Postgres）。
//
// 重点不是「字段存在」，而是三条容易写错、而写错后会让值班的人误判的语义：
//
//  1. **未知成本不被算成 0**：amount_state='unknown' 单独计数（超时/断连
//     的默认形态就是未知，把它当 0 会显示「成本正常」）；
//  2. **队列年龄与 outbox 年龄分开**：它们是两个循环是否停滞的证据；
//  3. **Notes 给出可行动的解读**（数字没有解读等于没有信息）。

func newHealthFixture(t *testing.T) (*pgxpool.Pool, int64) {
	t.Helper()
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

	suffix := strings.ReplaceAll(t.Name(), "/", "_")
	var workspaceID, userID int64
	if err := pool.QueryRow(ctx, `
    INSERT INTO workspaces (name, slug) VALUES ($1, $2) RETURNING id`,
		"健康测试工作区 "+suffix, "health-test-"+suffix).Scan(&workspaceID); err != nil {
		t.Fatalf("seed workspace: %v", err)
	}
	if err := pool.QueryRow(ctx, `
    INSERT INTO users (email, hashed_password, role) VALUES ($1, 'x', 'user') RETURNING id`,
		"health-"+suffix+"@example.test").Scan(&userID); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	var projectID int64
	if err := pool.QueryRow(ctx, `
    INSERT INTO projects (workspace_id, name, goal, target_kind, status, owner_id,
                          domain_count, directions_per_domain, questions_per_direction, pilot_size,
                          budget_currency, budget_on_exhausted)
    VALUES ($1, $2, '健康度夹具', 'sft', 'draft', $3, 1, 1, 1, 2, 'CNY', 'pause')
    RETURNING id`, workspaceID, "health-project-"+suffix, userID).Scan(&projectID); err != nil {
		t.Fatalf("seed project: %v", err)
	}
	t.Cleanup(func() {
		cleanup := context.Background()
		// 只删本夹具的行（按项目/工作区限定），不做无 WHERE 的删除。
		for _, statement := range []string{
			`DELETE FROM job_attempts WHERE job_id IN (SELECT id FROM jobs WHERE project_id = $1)`,
			`DELETE FROM jobs WHERE project_id = $1`,
			`DELETE FROM outbox WHERE payload->>'projectId' = $2`,
			`DELETE FROM usage_ledger WHERE project_id = $1`,
			`DELETE FROM budget_reservations WHERE project_id = $1`,
			`DELETE FROM experiments WHERE project_id = $1`,
			`DELETE FROM releases WHERE project_id = $1`,
		} {
			if strings.Contains(statement, "$2") {
				_, _ = pool.Exec(cleanup, statement, projectID, stringID(projectID))
				continue
			}
			_, _ = pool.Exec(cleanup, statement, projectID)
		}
		_, _ = pool.Exec(cleanup, `DELETE FROM projects WHERE id = $1`, projectID)
		_, _ = pool.Exec(cleanup, `DELETE FROM users WHERE id = $1`, userID)
		_, _ = pool.Exec(cleanup, `DELETE FROM workspaces WHERE id = $1`, workspaceID)
	})
	return pool, projectID
}

func stringID(value int64) string {
	return strconv.FormatInt(value, 10)
}

// TestLoadStudioHealthCountsFactsAndKeepsUnknownCostSeparate 覆盖核心计数。
func TestLoadStudioHealthCountsFactsAndKeepsUnknownCostSeparate(t *testing.T) {
	pool, projectID := newHealthFixture(t)
	ctx := context.Background()

	// 一条等待很久的未派发 outbox 事件 + 一个排队很久的作业。
	if _, err := pool.Exec(ctx, `
    INSERT INTO outbox (event_id, topic, payload, status, created_at)
    VALUES ($1, 'studio.batch.generate', '{}'::jsonb, 'pending', NOW() - INTERVAL '30 minutes')`,
		"health-outbox-"+stringID(projectID)); err != nil {
		t.Fatalf("seed outbox: %v", err)
	}
	var jobID int64
	if err := pool.QueryRow(ctx, `
    INSERT INTO jobs (project_id, job_kind, payload, status, created_at)
    VALUES ($1, 'studio.batch.generate', '{}'::jsonb, 'pending', NOW() - INTERVAL '2 hours')
    RETURNING id`, projectID).Scan(&jobID); err != nil {
		t.Fatalf("seed job: %v", err)
	}
	// 一次租约过期尝试（worker 崩溃的证据）。
	if _, err := pool.Exec(ctx, `
    INSERT INTO job_attempts (job_id, attempt, outcome, started_at)
    VALUES ($1, 1, 'lease_expired', NOW() - INTERVAL '1 hour')`, jobID); err != nil {
		t.Fatalf("seed attempt: %v", err)
	}
	// 预算台账 + 一条未知金额的用量（必须单独计数，不得算成 0）。
	if _, err := pool.Exec(ctx, `
    INSERT INTO budget_reservations (project_id, currency, limit_minor, reserved_minor, settled_minor, uncertain_minor)
    VALUES ($1, 'CNY', 100000, 5000, 1200, 800)`, projectID); err != nil {
		t.Fatalf("seed budget: %v", err)
	}
	if _, err := pool.Exec(ctx, `
    INSERT INTO usage_ledger (project_id, idempotency_key, currency, amount_state, state, reconciliation)
    VALUES ($1, $2, 'CNY', 'unknown', 'settled', 'impossible')`,
		projectID, "health-usage-"+stringID(projectID)); err != nil {
		t.Fatalf("seed usage: %v", err)
	}
	if _, err := pool.Exec(ctx, `
    INSERT INTO experiments (project_id, target_kind, status, missing_count)
    VALUES ($1, 'sft', 'partial_failed', 2)`, projectID); err != nil {
		t.Fatalf("seed experiment: %v", err)
	}
	if _, err := pool.Exec(ctx, `
    INSERT INTO releases (project_id, release_name, release_name_key, status)
    VALUES ($1, $2, $3, 'build_failed')`,
		projectID, "health-release-"+stringID(projectID), "health-release-"+stringID(projectID)); err != nil {
		t.Fatalf("seed release: %v", err)
	}

	health, err := LoadStudioHealth(ctx, pool, Rollout{})
	if err != nil {
		t.Fatalf("LoadStudioHealth: %v", err)
	}

	// 计数是**全局**的（别的测试可能同时插入），因此断言 >= 而不是 ==。
	if health.Outbox.Pending < 1 {
		t.Errorf("未派发 outbox 至少 1 条，实际 %d", health.Outbox.Pending)
	}
	if health.Outbox.OldestPendingAgeSeconds < 25*60 {
		t.Errorf("outbox 年龄应 >= 1500 秒，实际 %d", health.Outbox.OldestPendingAgeSeconds)
	}
	if health.Jobs.Pending < 1 || health.Jobs.OldestPendingAgeSeconds < 60*60 {
		t.Errorf("排队作业与年龄不符：pending=%d age=%d", health.Jobs.Pending, health.Jobs.OldestPendingAgeSeconds)
	}
	if health.Leases.Expired24h < 1 {
		t.Errorf("租约回收至少 1 次，实际 %d", health.Leases.Expired24h)
	}
	if health.Usage.UnknownAmount24h < 1 {
		t.Errorf("未知金额必须单独计数（不得算成 0），实际 %d", health.Usage.UnknownAmount24h)
	}
	if health.Usage.ReconciliationImpossible < 1 {
		t.Errorf("无法对账的记录必须可见，实际 %d", health.Usage.ReconciliationImpossible)
	}
	if health.Experiments.WithMissingScores < 1 || health.Experiments.PartialFailed < 1 {
		t.Errorf("缺分实验必须可见：%+v", health.Experiments)
	}
	if health.Releases.BuildFailed < 1 {
		t.Errorf("build_failed 发布必须可见：%+v", health.Releases)
	}

	// 预算按币种聚合，且四态分开。
	foundCurrency := false
	for _, entry := range health.Budget.Currencies {
		if entry.Currency == "CNY" {
			foundCurrency = true
			if entry.UncertainMinor < 800 {
				t.Errorf("uncertain 必须单独统计（它是「未知占用的额度」），实际 %d", entry.UncertainMinor)
			}
		}
	}
	if !foundCurrency {
		t.Fatalf("预算台账必须按币种给出：%+v", health.Budget.Currencies)
	}

	// Notes 必须包含可行动的解读。
	joined := strings.Join(health.Notes, "\n")
	for _, want := range []string{"派发循环", "租约", "未知不等于 0", "缺分"} {
		if !strings.Contains(joined, want) {
			t.Errorf("Notes 缺少对 %q 的解读，实际：%s", want, joined)
		}
	}
}

// TestLogContextFieldsOmitZeroValues 覆盖日志关联字段的形状。
//
// 断言字段**名**：名字拼错会让日志不再可关联，而那种偏差不会报错。
func TestLogContextFieldsOmitZeroValues(t *testing.T) {
	fields := LogContext{RequestID: "req-1", ProjectID: 7, BatchID: 9}.Fields()
	for _, key := range []string{"requestId", "projectId", "batchId"} {
		if _, found := fields[key]; !found {
			t.Errorf("缺少关联字段 %q，实际 %v", key, fields)
		}
	}
	for _, key := range []string{"experimentId", "releaseId"} {
		if _, found := fields[key]; found {
			t.Errorf("零值字段 %q 不应出现（会淹没真正有值的字段）", key)
		}
	}
	if len(LogContext{}.Fields()) != 0 {
		t.Error("空 LogContext 不产生任何字段")
	}
}
