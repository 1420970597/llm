package legacy

import (
	"context"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/1420970597/llm/internal/model"
	"github.com/1420970597/llm/internal/store"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// 本文件验证 Issue #160 T30 的只读盘点与映射规划（真实 Postgres）。
//
// 它在 CI 的 integration job 里带 DSN 运行，因此是**真执行**而不是被 Skip
//（scripts/check-integration-tests.sh 会断言它出现 `--- PASS`）。

type inventoryFixture struct {
	pool        *pgxpool.Pool
	userID      int64
	workspaceID int64
	// datasetIDs 按用途分组，避免测试里靠位置理解数据。
	noOwnerDataset     int64
	mappableDataset    int64
	snapshotDataset    int64
	conflictDataset    int64
	richDataset        int64
	beforeCounts       map[string]int64
	inventoryTablesRaw []string
}

func newInventoryFixture(t *testing.T) *inventoryFixture {
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
	fixture := &inventoryFixture{pool: pool, inventoryTablesRaw: inventoryTables}

	if err := pool.QueryRow(ctx, `
    INSERT INTO workspaces (name, slug) VALUES ($1, $2) RETURNING id`,
		"盘点测试工作区 "+suffix, "inventory-test-"+suffix).Scan(&fixture.workspaceID); err != nil {
		t.Fatalf("seed workspace: %v", err)
	}
	if err := pool.QueryRow(ctx, `
    INSERT INTO users (email, hashed_password, role) VALUES ($1, 'x', 'user') RETURNING id`,
		"inventory-"+suffix+"@example.test").Scan(&fixture.userID); err != nil {
		t.Fatalf("seed user: %v", err)
	}

	// dataset 1：没有 owner（旧库的常见形态）。
	if err := pool.QueryRow(ctx, `
    INSERT INTO datasets (name, root_keyword, status) VALUES ($1, '军事', 'draft') RETURNING id`,
		"t30-无主-"+suffix).Scan(&fixture.noOwnerDataset); err != nil {
		t.Fatalf("seed no-owner dataset: %v", err)
	}

	// dataset 2：有主 + 生成运行 + 领域/问题 → 可映射为 legacy-origin 项目。
	if err := pool.QueryRow(ctx, `
    INSERT INTO datasets (name, root_keyword, status, created_by) VALUES ($1, '医疗', 'completed', $2)
    RETURNING id`, "t30-有证据-"+suffix, fixture.userID).Scan(&fixture.mappableDataset); err != nil {
		t.Fatalf("seed mappable dataset: %v", err)
	}
	seedDomainAndQuestion(t, pool, fixture.mappableDataset, "mappable-"+suffix)
	if _, err := pool.Exec(ctx, `
    INSERT INTO generation_runs (dataset_id, stage, status) VALUES ($1, 'domains', 'completed')`,
		fixture.mappableDataset); err != nil {
		t.Fatalf("seed generation run: %v", err)
	}

	// dataset 3：有主 + 只有问题（没有生成运行）→ 导入快照批次。
	if err := pool.QueryRow(ctx, `
    INSERT INTO datasets (name, root_keyword, status, created_by) VALUES ($1, '法律', 'completed', $2)
    RETURNING id`, "t30-仅内容-"+suffix, fixture.userID).Scan(&fixture.snapshotDataset); err != nil {
		t.Fatalf("seed snapshot dataset: %v", err)
	}
	seedDomainAndQuestion(t, pool, fixture.snapshotDataset, "snapshot-"+suffix)

	// dataset 4：与已有项目同名 → 冲突。
	if err := pool.QueryRow(ctx, `
    INSERT INTO datasets (name, root_keyword, status, created_by) VALUES ($1, '金融', 'completed', $2)
    RETURNING id`, "t30-冲突-"+suffix, fixture.userID).Scan(&fixture.conflictDataset); err != nil {
		t.Fatalf("seed conflict dataset: %v", err)
	}
	projects := store.NewProjectStore(pool)
	projectInput := model.CreateProjectInput{Name: "t30-冲突-" + suffix, Goal: "占位", TargetKind: model.TargetKindSFT}
	projectInput.Normalize()
	if _, err := projects.CreateProject(ctx, fixture.workspaceID, fixture.userID, projectInput); err != nil {
		t.Fatalf("seed conflicting project: %v", err)
	}

	// dataset 5：评测/奖励/推理/工件齐全，且工件缺 object_key。
	if err := pool.QueryRow(ctx, `
    INSERT INTO datasets (name, root_keyword, status, created_by) VALUES ($1, '制造', 'completed', $2)
    RETURNING id`, "t30-历史资产-"+suffix, fixture.userID).Scan(&fixture.richDataset); err != nil {
		t.Fatalf("seed rich dataset: %v", err)
	}
	domainID, questionID := seedDomainAndQuestion(t, pool, fixture.richDataset, "rich-"+suffix)
	if _, err := pool.Exec(ctx, `
    INSERT INTO eval_runs (dataset_id, name, status) VALUES ($1, $2, 'completed')`,
		fixture.richDataset, "旧评估-"+suffix); err != nil {
		t.Fatalf("seed eval run: %v", err)
	}
	if _, err := pool.Exec(ctx, `
    INSERT INTO reasoning_records (dataset_id, question_id, answer_summary, object_key)
    VALUES ($1, $2, '旧摘要', 'legacy/reasoning.jsonl')`, fixture.richDataset, questionID); err != nil {
		t.Fatalf("seed reasoning record: %v", err)
	}
	if _, err := pool.Exec(ctx, `
    INSERT INTO reward_records (dataset_id, question_id, score, object_key)
    VALUES ($1, $2, 1.0, 'legacy/reward.jsonl')`, fixture.richDataset, questionID); err != nil {
		t.Fatalf("seed reward record: %v", err)
	}
	if _, err := pool.Exec(ctx, `
    INSERT INTO artifacts (dataset_id, artifact_type, object_key, content_type)
    VALUES ($1, 'jsonl', 'legacy/export.jsonl', 'application/x-ndjson')`,
		fixture.richDataset); err != nil {
		t.Fatalf("seed artifact: %v", err)
	}
	if _, err := pool.Exec(ctx, `
    INSERT INTO artifacts (dataset_id, artifact_type, object_key, content_type)
    VALUES ($1, 'jsonl', '', 'application/x-ndjson')`, fixture.richDataset); err != nil {
		t.Fatalf("seed artifact without object key: %v", err)
	}
	_ = domainID

	fixture.beforeCounts = tableCounts(t, pool, fixture.inventoryTablesRaw)
	t.Cleanup(func() {
		cleanup := context.Background()
		// 按依赖顺序清理：项目 → 数据集（其余子表由 ON DELETE CASCADE 带走）。
		_, _ = pool.Exec(cleanup, `DELETE FROM projects WHERE name = $1`, "t30-冲突-"+suffix)
		_, _ = pool.Exec(cleanup, `DELETE FROM datasets WHERE name LIKE $1`, "t30-%"+suffix)
		_, _ = pool.Exec(cleanup, `DELETE FROM users WHERE email = $1`, "inventory-"+suffix+"@example.test")
		_, _ = pool.Exec(cleanup, `DELETE FROM workspaces WHERE slug = $1`, "inventory-test-"+suffix)
	})
	return fixture
}

func seedDomainAndQuestion(t *testing.T, pool *pgxpool.Pool, datasetID int64, key string) (int64, int64) {
	t.Helper()
	ctx := context.Background()
	var domainID int64
	if err := pool.QueryRow(ctx, `
    INSERT INTO domains (dataset_id, name, canonical_name) VALUES ($1, $2, $2) RETURNING id`,
		datasetID, "domain-"+key).Scan(&domainID); err != nil {
		t.Fatalf("seed domain: %v", err)
	}
	var questionID int64
	if err := pool.QueryRow(ctx, `
    INSERT INTO questions (dataset_id, domain_id, content, canonical_hash)
    VALUES ($1, $2, $3, $4) RETURNING id`,
		datasetID, domainID, "问题 "+key, "hash-"+key).Scan(&questionID); err != nil {
		t.Fatalf("seed question: %v", err)
	}
	return domainID, questionID
}

func tableCounts(t *testing.T, pool *pgxpool.Pool, tables []string) map[string]int64 {
	t.Helper()
	counts := map[string]int64{}
	ctx := context.Background()
	for _, name := range tables {
		var count int64
		if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM `+quoteIdent(name)).Scan(&count); err != nil {
			t.Fatalf("count %s: %v", name, err)
		}
		counts[name] = count
	}
	return counts
}

// TestInventoryPlansLegacyDatasets 覆盖 T30 的核心验收项：
// 每类决策都被产出，且带可行动的示例 ID。
func TestInventoryPlansLegacyDatasets(t *testing.T) {
	fixture := newInventoryFixture(t)
	report, err := Inventory(context.Background(), fixture.pool, Options{})
	if err != nil {
		t.Fatalf("Inventory: %v", err)
	}
	if !report.DryRun || !report.ReadOnly {
		t.Fatalf("必须声明 dry-run 与只读事务：%+v", report)
	}

	wanted := []string{
		DecisionNeedsOwner,
		DecisionMappableProject,
		DecisionImportSnapshot,
		DecisionConflict,
		DecisionNeedsReeval,
		DecisionReasoningSeparateSource,
		DecisionGRPONotTeacherMaterial,
		DecisionArtifactUnverified,
		DecisionMissingReference,
	}
	for _, kind := range wanted {
		if report.Totals[kind] == 0 {
			t.Errorf("缺少决策 %s（说明该来源没有被盘点或没有被标记）", kind)
		}
	}

	// 示例 ID：可映射的 dataset 必须给出 generation_run 的示例 ID，
	// 否则管理员无法从报告跳到具体对象。
	foundSample := false
	for _, plan := range report.Datasets {
		if plan.DatasetID != fixture.mappableDataset {
			continue
		}
		for _, decision := range plan.Decisions {
			if decision.Kind == DecisionMappableProject && len(decision.SampleIDs) > 0 {
				foundSample = true
			}
		}
	}
	if !foundSample {
		t.Error("可映射决策必须带示例 ID（否则报告无法定位到具体运行）")
	}

	// 无主 dataset 必须是 blocker，而不是「自动归给某个人」。
	for _, plan := range report.Datasets {
		if plan.DatasetID != fixture.noOwnerDataset {
			continue
		}
		hasBlocker := false
		for _, decision := range plan.Decisions {
			if decision.Kind == DecisionNeedsOwner && decision.Severity == SeverityBlocker {
				hasBlocker = true
			}
		}
		if !hasBlocker {
			t.Error("没有 owner 的 dataset 必须产生 blocker 级 needs_owner 决策")
		}
	}
}

// TestInventoryIsReadOnly 覆盖「dry-run 不写业务数据」。
//
// 两条证据一起用：
//
//  1. **机制**：与 Inventory 相同选项的只读事务里，一次写入必须被 Postgres 拒绝
//     （`cannot execute ... in a read-only transaction`）。这比「代码里没有 INSERT」
//     强：即使将来有人在盘点里加了一条 UPDATE，它也会被数据库拦住。
//  2. **本测试自己的夹具行数不变**（按唯一名称前缀过滤）。
//     刻意**不**统计全表行数：`go test ./...` 会并行跑不同 package 的测试，
//     别的 package 建/删 workspaces 会让全表计数漂移，从而把「盘点只读」
//     误报为失败（实测踩过：workspaces 3 -> 2）。
func TestInventoryIsReadOnly(t *testing.T) {
	fixture := newInventoryFixture(t)
	ctx := context.Background()

	probe, err := fixture.pool.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
	if err != nil {
		t.Fatalf("begin read-only tx: %v", err)
	}
	defer func() { _ = probe.Rollback(ctx) }()
	if _, err := probe.Exec(ctx, `CREATE TEMP TABLE t30_readonly_probe (id int)`); err == nil {
		t.Fatal("只读事务里的写操作必须被拒绝（否则 dry-run 的保证不存在）")
	} else if !strings.Contains(strings.ToLower(err.Error()), "read-only") {
		t.Fatalf("写失败的原因应是只读事务，实际 %v", err)
	}

	before := ownFixtureRows(t, fixture)
	if _, err := Inventory(ctx, fixture.pool, Options{}); err != nil {
		t.Fatalf("Inventory: %v", err)
	}
	after := ownFixtureRows(t, fixture)
	if before != after {
		t.Fatalf("盘点改变了本夹具的行数：%d -> %d（dry-run 不得写业务数据）", before, after)
	}
}

// ownFixtureRows 统计属于本测试的 dataset/user/workspace 行数。
//
// 按**夹具自己的 ID** 计数而不是按名称前缀：名称拼接容易与其它测试撞车，
// 而 ID 是本测试刚插入的主键，不会被并发影响。
func ownFixtureRows(t *testing.T, fixture *inventoryFixture) int64 {
	t.Helper()
	ctx := context.Background()
	var total, count int64
	datasetIDs := []int64{fixture.noOwnerDataset, fixture.mappableDataset,
		fixture.snapshotDataset, fixture.conflictDataset, fixture.richDataset}
	if err := fixture.pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM datasets WHERE id = ANY($1::bigint[])`, datasetIDs).Scan(&count); err != nil {
		t.Fatalf("count own datasets: %v", err)
	}
	total += count
	if err := fixture.pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM users WHERE id = $1`, fixture.userID).Scan(&count); err != nil {
		t.Fatalf("count own user: %v", err)
	}
	total += count
	if err := fixture.pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM workspaces WHERE id = $1`, fixture.workspaceID).Scan(&count); err != nil {
		t.Fatalf("count own workspace: %v", err)
	}
	return total + count
}

// TestInventoryReportsStaleDatabase 覆盖「迁移不完整」的库。
//
// 实测背景：部署中的开发库停在 0021（缺 releases/sample_versions/batches 等表）。
// 那时直接 `COUNT(*)` 会以「relation does not exist」失败，让一次可以部分完成的
// 盘点变成一个也拿不到的错误；而静默跳过又会让报告看起来完整。
// 本测试冻结正确行为：**跳过缺失的表，并在报告里以 Note 显式写明**。
func TestInventoryReportsStaleDatabase(t *testing.T) {
	dsn := os.Getenv("LLM_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("输入缺失: LLM_TEST_POSTGRES_DSN 未设置，跳过真实 Postgres 集成测试")
	}
	ctx := context.Background()
	admin, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer admin.Close()

	dbName := "llm_legacy_stale_" + strings.ReplaceAll(t.Name(), "/", "_")
	conn, err := admin.Acquire(ctx)
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	if _, err := conn.Exec(ctx, `DROP DATABASE IF EXISTS `+quoteIdent(dbName)); err != nil {
		conn.Release()
		t.Fatalf("drop: %v", err)
	}
	if _, err := conn.Exec(ctx, `CREATE DATABASE `+quoteIdent(dbName)); err != nil {
		conn.Release()
		t.Fatalf("create: %v", err)
	}
	conn.Release()

	parsed, err := url.Parse(dsn)
	if err != nil {
		t.Fatalf("parse dsn: %v", err)
	}
	parsed.Path = "/" + dbName
	pool, err := pgxpool.New(ctx, parsed.String())
	if err != nil {
		t.Fatalf("connect scratch: %v", err)
	}
	defer pool.Close()
	t.Cleanup(func() {
		cleanup, err := admin.Acquire(context.Background())
		if err != nil {
			return
		}
		defer cleanup.Release()
		_, _ = cleanup.Exec(context.Background(), `DROP DATABASE IF EXISTS `+quoteIdent(dbName))
	})

	// 只建 datasets 一张表（模拟停在最早迁移上的库）。
	if _, err := pool.Exec(ctx, `
    CREATE TABLE datasets (
      id BIGSERIAL PRIMARY KEY, name TEXT NOT NULL, root_keyword TEXT NOT NULL,
      target_size BIGINT NOT NULL DEFAULT 0, status TEXT NOT NULL DEFAULT 'draft',
      created_by BIGINT, created_at TIMESTAMPTZ NOT NULL DEFAULT NOW())`); err != nil {
		t.Fatalf("create stale datasets: %v", err)
	}
	if _, err := pool.Exec(ctx, `
    INSERT INTO datasets (name, root_keyword, status) VALUES ('陈旧库-无主', '测试', 'draft')`); err != nil {
		t.Fatalf("seed stale dataset: %v", err)
	}

	report, err := Inventory(ctx, pool, Options{})
	if err != nil {
		t.Fatalf("迁移不完整的库不应让盘点失败：%v", err)
	}
	joined := strings.Join(report.Notes, "\n")
	if !strings.Contains(joined, "数据库迁移不完整") {
		t.Fatalf("必须显式报告迁移不完整，实际 Notes=%v", report.Notes)
	}
	if !strings.Contains(joined, "releases") {
		t.Fatalf("必须点名缺失的表（否则管理员不知道缺什么），实际 Notes=%v", report.Notes)
	}
	// 无主 dataset 仍然**不会被静默忽略**：报告必须说明为什么没有决策。
	if !strings.Contains(joined, "不产出") {
		t.Fatalf("必须说明为何不产出映射决策，实际 Notes=%v", report.Notes)
	}
	// 已存在的表仍要被盘点（部分盘点比什么都没有有用）。
	foundDatasetsStat := false
	for _, stat := range report.Tables {
		if stat.Name == "datasets" {
			foundDatasetsStat = true
		}
	}
	if !foundDatasetsStat {
		t.Fatal("已存在的表必须出现在盘点结果里（部分盘点也有用）")
	}
}

// 为什么断言源码而不是行为：
//   - 「不调用模型」无法用行为证明（模型调用可能只在某条分支上）；
//     断言 import 列表是唯一能证明「不可能调用」的方式。
//   - 「不删源数据」同理：一条将来才被触发的 DELETE 是当前行为测试抓不到的。
func TestInventorySourceHasNoWriteOrModelCalls(t *testing.T) {
	source, err := os.ReadFile(filepath.Join("inventory.go"))
	if err != nil {
		t.Fatalf("read source: %v", err)
	}
	// 只看**代码**：注释里会出现「本包不依赖 internal/llm」「没有 DELETE/TRUNCATE」
	// 这类说明文字，把注释算进来会让断言变成文字游戏（而且会惩罚好注释）。
	code := stripGoComments(string(source))
	for _, forbidden := range []string{
		"internal/llm", "internal/eval", "internal/studio", "internal/exporter",
	} {
		if strings.Contains(code, forbidden) {
			t.Errorf("盘点工具不得依赖 %s：dry-run 禁止调用模型与写出制品", forbidden)
		}
	}
	// SQL 关键字按大写词边界检查。
	upper := strings.ToUpper(code)
	for _, keyword := range []string{"INSERT INTO", "UPDATE ", "DELETE FROM", "TRUNCATE", "DROP TABLE", "ALTER TABLE"} {
		if strings.Contains(upper, keyword) {
			t.Errorf("盘点工具源码里出现写语句 %q：源数据不得被修改（T30 验收项）", keyword)
		}
	}
}

// stripGoComments 去掉行注释与块注释（保留字符串字面量里的内容）。
//
// 手写而不是用 go/parser：这里只需要「不误报」，而 parser 会把
// 注释作为 AST 节点给出，反而更简单；但引入 go/parser 会让本测试
// 对源码格式有额外要求（例如未格式化的文件解析失败）。用最小实现。
func stripGoComments(source string) string {
	var builder strings.Builder
	lines := strings.Split(source, "\n")
	inBlock := false
	for _, line := range lines {
		if inBlock {
			if index := strings.Index(line, "*/"); index >= 0 {
				inBlock = false
				line = line[index+2:]
			} else {
				continue
			}
		}
		if index := strings.Index(line, "/*"); index >= 0 && !strings.Contains(line, "*/") {
			inBlock = true
			line = line[:index]
		}
		if index := strings.Index(line, "//"); index >= 0 {
			line = line[:index]
		}
		builder.WriteString(line)
		builder.WriteByte('\n')
	}
	return builder.String()
}
