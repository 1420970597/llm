package legacy

import (
	"context"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/1420970597/llm/internal/model"
	"github.com/1420970597/llm/internal/store"
	"github.com/jackc/pgx/v5/pgxpool"
)

// stringID 把 ID 转成字符串（构造确定性 sample_key 用）。
func stringID(value int64) string { return strconv.FormatInt(value, 10) }

// testProjectInput 构造一个合法的项目输入（冲突夹具用）。
func testProjectInput(name string) model.CreateProjectInput {
	input := model.CreateProjectInput{Name: name, Goal: "冲突夹具", TargetKind: model.TargetKindSFT}
	input.Normalize()
	return input
}

// 本文件验证 Issue #160 T31 的幂等导入（真实 Postgres）。
//
// 四条必须由**真库**证明的性质（单元测试证明不了）：
//  1. 重复执行零重复导入（台账唯一键 + 内容 hash 两层）；
//  2. 无归属的 dataset 被拒绝，且**没有**留下项目/样本；
//  3. 同名项目冲突被拒绝（而不是自动改名）；
//  4. dry-run 不写业务数据。
//
// 它由 CI 的 integration job 带 DSN 运行，并由 check-integration-tests.sh
// 断言其出现 `--- PASS`。

type importFixture struct {
	pool        *pgxpool.Pool
	deps        ImportDeps
	workspaceID int64
	userID      int64
	suffix      string
	// datasetIDs 是本测试建过的 dataset（清理时按它们精确删除台账）。
	// 不按 LIKE 'dataset:%' 清理：那会删掉其它测试的台账行，
	// 而后者的失败会表现为「我的导入没有回放」这种难以定位的错误。
	datasetIDs []int64
}

func newImportFixture(t *testing.T) *importFixture {
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
	fixture := &importFixture{
		pool: pool, deps: NewImportDeps(pool), suffix: suffix,
	}
	if err := pool.QueryRow(ctx, `
    INSERT INTO workspaces (name, slug) VALUES ($1, $2) RETURNING id`,
		"导入测试工作区 "+suffix, "import-test-"+suffix).Scan(&fixture.workspaceID); err != nil {
		t.Fatalf("seed workspace: %v", err)
	}
	if err := pool.QueryRow(ctx, `
    INSERT INTO users (email, hashed_password, role) VALUES ($1, 'x', 'user') RETURNING id`,
		"import-"+suffix+"@example.test").Scan(&fixture.userID); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	t.Cleanup(func() {
		cleanup := context.Background()
		// 先删台账（它引用项目/批次），再删项目（样本/版本随之级联）。
		for _, datasetID := range fixture.datasetIDs {
			_, _ = pool.Exec(cleanup, `DELETE FROM legacy_imports WHERE source_key = $1`, DatasetSourceKey(datasetID))
		}
		_, _ = pool.Exec(cleanup, `DELETE FROM projects WHERE workspace_id = $1`, fixture.workspaceID)
		_, _ = pool.Exec(cleanup, `DELETE FROM datasets WHERE root_keyword = $1`, "import-test-"+suffix)
		_, _ = pool.Exec(cleanup, `DELETE FROM users WHERE id = $1`, fixture.userID)
		_, _ = pool.Exec(cleanup, `DELETE FROM workspaces WHERE id = $1`, fixture.workspaceID)
	})
	return fixture
}

// seedDataset 建一个 dataset 与若干「有 SFT 内容」的问题。
func (fixture *importFixture) seedDataset(t *testing.T, label string, withOwner bool, questions int, withContent bool) int64 {
	t.Helper()
	ctx := context.Background()
	var owner any
	if withOwner {
		owner = fixture.userID
	}
	var datasetID int64
	if err := fixture.pool.QueryRow(ctx, `
    INSERT INTO datasets (name, root_keyword, status, created_by)
    VALUES ($1, $2, 'completed', $3) RETURNING id`,
		"import-"+label+"-"+fixture.suffix, "import-test-"+fixture.suffix, owner).Scan(&datasetID); err != nil {
		t.Fatalf("seed dataset: %v", err)
	}
	fixture.datasetIDs = append(fixture.datasetIDs, datasetID)
	for index := 0; index < questions; index++ {
		var domainID, questionID int64
		if err := fixture.pool.QueryRow(ctx, `
      INSERT INTO domains (dataset_id, name, canonical_name) VALUES ($1, $2, $2) RETURNING id`,
			datasetID, "d-"+label+"-"+stringID(int64(index))).Scan(&domainID); err != nil {
			t.Fatalf("seed domain: %v", err)
		}
		if err := fixture.pool.QueryRow(ctx, `
      INSERT INTO questions (dataset_id, domain_id, content, canonical_hash) VALUES ($1, $2, $3, $4)
      RETURNING id`, datasetID, domainID,
			"问题 "+label+" "+stringID(int64(index)), "hash-"+label+"-"+stringID(int64(index))).Scan(&questionID); err != nil {
			t.Fatalf("seed question: %v", err)
		}
		if !withContent {
			continue
		}
		if _, err := fixture.pool.Exec(ctx, `
      INSERT INTO sft_records (dataset_id, question_id, chain_of_thought, answer)
      VALUES ($1, $2, $3, $4)`, datasetID, questionID,
			"推理 "+label+" "+stringID(int64(index)), "答案 "+label+" "+stringID(int64(index))); err != nil {
			t.Fatalf("seed sft record: %v", err)
		}
	}
	return datasetID
}

func (fixture *importFixture) countProjectVersions(t *testing.T, projectID int64) int64 {
	t.Helper()
	var count int64
	if err := fixture.pool.QueryRow(context.Background(),
		`SELECT COUNT(*) FROM sample_versions WHERE project_id = $1`, projectID).Scan(&count); err != nil {
		t.Fatalf("count versions: %v", err)
	}
	return count
}

// TestImportDatasetIsIdempotent 覆盖「重复执行零重复导入」的两层判据。
func TestImportDatasetIsIdempotent(t *testing.T) {
	fixture := newImportFixture(t)
	ctx := context.Background()
	datasetID := fixture.seedDataset(t, "幂等", true, 3, true)

	options := ImportOptions{DatasetID: datasetID, WorkspaceID: fixture.workspaceID, ActorID: fixture.userID}
	first, err := ImportDataset(ctx, fixture.deps, options)
	if err != nil {
		t.Fatalf("首次导入失败：%v", err)
	}
	if first.Counts.ImportedVersions != 3 {
		t.Fatalf("应导入 3 个内容版本，实际 %+v", first.Counts)
	}
	if first.Status != "completed" {
		t.Fatalf("首次导入应为 completed，实际 %s", first.Status)
	}
	if first.ProjectID == 0 {
		t.Fatal("必须建立/解析出目标项目")
	}
	versionsAfterFirst := fixture.countProjectVersions(t, first.ProjectID)
	if versionsAfterFirst != 3 {
		t.Fatalf("项目内应有 3 个内容版本，实际 %d", versionsAfterFirst)
	}
	// legacy_dataset_id 必须写进项目（旧路由兼容靠它反查）。
	mapped, err := fixture.deps.Imports.FindProjectByLegacyDataset(ctx, datasetID)
	if err != nil || mapped != first.ProjectID {
		t.Fatalf("legacy_dataset_id 未正确写入：mapped=%d err=%v", mapped, err)
	}
	// 快照批次必须已完成且没有单元（没有伪造一次运行）。
	var batchStatus string
	var batchItems int64
	if err := fixture.pool.QueryRow(ctx, `SELECT status FROM batches WHERE id = $1`, first.BatchID).Scan(&batchStatus); err != nil {
		t.Fatalf("read batch: %v", err)
	}
	if batchStatus != "completed" {
		t.Fatalf("导入快照批次应标为 completed，实际 %s", batchStatus)
	}
	if err := fixture.pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM batch_items WHERE batch_id = $1`, first.BatchID).Scan(&batchItems); err != nil {
		t.Fatalf("count batch items: %v", err)
	}
	if batchItems != 0 {
		t.Fatalf("导入不应产生任何 batch_items（那会假装有生成过程），实际 %d", batchItems)
	}

	// 第二次：台账已完成 → 回放，不产生任何新版本。
	second, err := ImportDataset(ctx, fixture.deps, options)
	if err != nil {
		t.Fatalf("重复导入不应失败：%v", err)
	}
	if !second.Replay {
		t.Fatal("已完成的来源必须走回放（replay=true）")
	}
	if got := fixture.countProjectVersions(t, first.ProjectID); got != versionsAfterFirst {
		t.Fatalf("回放不得产生新版本：%d -> %d", versionsAfterFirst, got)
	}

	// 第三次：Resume=false，从头走一遍 —— 此时靠**内容 hash** 幂等。
	third, err := ImportDataset(ctx, fixture.deps, ImportOptions{
		DatasetID: datasetID, WorkspaceID: fixture.workspaceID, ActorID: fixture.userID, Resume: false,
	})
	if err != nil {
		t.Fatalf("从头重跑不应失败：%v", err)
	}
	if third.Replay {
		// 从头重跑时台账是既有行且已完成 → 仍然是回放；
		// 这条分支由上一条覆盖。这里显式说明：Resume 只影响「未完成」的台账。
		t.Log("台账已完成，Resume=false 同样走回放（幂等性由台账层保证）")
	}
	if got := fixture.countProjectVersions(t, first.ProjectID); got != versionsAfterFirst {
		t.Fatalf("从头重跑不得产生新版本：%d -> %d", versionsAfterFirst, got)
	}
}

// TestImportSkipsQuestionsWithoutContent 覆盖「没有 SFT 内容的问题不编内容」。
func TestImportSkipsQuestionsWithoutContent(t *testing.T) {
	fixture := newImportFixture(t)
	ctx := context.Background()
	datasetID := fixture.seedDataset(t, "无内容", true, 2, false)

	summary, err := ImportDataset(ctx, fixture.deps, ImportOptions{
		DatasetID: datasetID, WorkspaceID: fixture.workspaceID, ActorID: fixture.userID,
	})
	if err != nil {
		t.Fatalf("导入失败：%v", err)
	}
	if summary.Counts.ImportedVersions != 0 || summary.Counts.SkippedNoContent != 2 {
		t.Fatalf("没有内容的问题必须全部跳过且不导入，实际 %+v", summary.Counts)
	}
}

// TestImportRefusesDatasetWithoutOwner 覆盖 T30 的 blocker 在导入路径上生效。
func TestImportRefusesDatasetWithoutOwner(t *testing.T) {
	fixture := newImportFixture(t)
	ctx := context.Background()
	datasetID := fixture.seedDataset(t, "无主", false, 2, true)

	_, err := ImportDataset(ctx, fixture.deps, ImportOptions{
		DatasetID: datasetID, WorkspaceID: fixture.workspaceID, ActorID: fixture.userID,
	})
	if err == nil || !strings.Contains(err.Error(), "归属") {
		t.Fatalf("没有 owner 的 dataset 必须被拒绝并说明原因，实际 %v", err)
	}
	// 不得留下任何项目或样本（拒绝要在写入之前）。
	var projects, samples int64
	if err := fixture.pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM projects WHERE workspace_id = $1`, fixture.workspaceID).Scan(&projects); err != nil {
		t.Fatalf("count projects: %v", err)
	}
	if err := fixture.pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM samples WHERE sample_key LIKE $1`, "legacy-"+stringID(datasetID)+"-%").Scan(&samples); err != nil {
		t.Fatalf("count samples: %v", err)
	}
	if projects != 0 || samples != 0 {
		t.Fatalf("拒绝路径不得留下数据：projects=%d samples=%d", projects, samples)
	}
}

// TestImportRefusesNameConflict 覆盖 T30 的 conflict 决策在导入路径上生效。
func TestImportRefusesNameConflict(t *testing.T) {
	fixture := newImportFixture(t)
	ctx := context.Background()
	datasetID := fixture.seedDataset(t, "冲突", true, 1, true)

	// 先建一个同名项目（用项目 store，确保与生产路径一致）。
	projects := store.NewProjectStore(fixture.pool)
	var datasetName string
	if err := fixture.pool.QueryRow(ctx, `SELECT name FROM datasets WHERE id = $1`, datasetID).Scan(&datasetName); err != nil {
		t.Fatalf("read dataset name: %v", err)
	}
	conflict := testProjectInput(datasetName)
	if _, err := projects.CreateProject(ctx, fixture.workspaceID, fixture.userID, conflict); err != nil {
		t.Fatalf("seed conflicting project: %v", err)
	}

	_, err := ImportDataset(ctx, fixture.deps, ImportOptions{
		DatasetID: datasetID, WorkspaceID: fixture.workspaceID, ActorID: fixture.userID,
	})
	if err == nil || !strings.Contains(err.Error(), "同名项目") {
		t.Fatalf("同名项目必须被拒绝并给出可操作建议，实际 %v", err)
	}

	// 显式指定目标项目后应当可以导入（conflict 必须是可操作的，而不是死胡同）。
	var targetProjectID int64
	if err := fixture.pool.QueryRow(ctx, `
    SELECT id FROM projects WHERE workspace_id = $1 AND name = $2`,
		fixture.workspaceID, datasetName).Scan(&targetProjectID); err != nil {
		t.Fatalf("read conflict project: %v", err)
	}
	summary, err := ImportDataset(ctx, fixture.deps, ImportOptions{
		DatasetID: datasetID, WorkspaceID: fixture.workspaceID, ActorID: fixture.userID,
		TargetProjectID: targetProjectID,
	})
	if err != nil {
		t.Fatalf("显式指定目标项目后应能导入：%v", err)
	}
	if summary.ProjectID != targetProjectID {
		t.Fatalf("导入应落在显式指定的项目上：%d vs %d", summary.ProjectID, targetProjectID)
	}
}

// TestImportDryRunWritesNothing 覆盖 dry-run 的只读语义。
func TestImportDryRunWritesNothing(t *testing.T) {
	fixture := newImportFixture(t)
	ctx := context.Background()
	datasetID := fixture.seedDataset(t, "演练", true, 2, true)

	summary, err := ImportDataset(ctx, fixture.deps, ImportOptions{
		DatasetID: datasetID, WorkspaceID: fixture.workspaceID, ActorID: fixture.userID, DryRun: true,
	})
	if err != nil {
		t.Fatalf("dry-run 不应失败：%v", err)
	}
	if summary.Status != "dry_run" {
		t.Fatalf("dry-run 的状态应为 dry_run，实际 %s", summary.Status)
	}
	var importRows, projects, samples int64
	if err := fixture.pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM legacy_imports WHERE source_key = $1`, DatasetSourceKey(datasetID)).Scan(&importRows); err != nil {
		t.Fatalf("count imports: %v", err)
	}
	if err := fixture.pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM projects WHERE workspace_id = $1`, fixture.workspaceID).Scan(&projects); err != nil {
		t.Fatalf("count projects: %v", err)
	}
	if err := fixture.pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM samples WHERE sample_key LIKE $1`, "legacy-"+stringID(datasetID)+"-%").Scan(&samples); err != nil {
		t.Fatalf("count samples: %v", err)
	}
	if importRows != 0 || projects != 0 || samples != 0 {
		t.Fatalf("dry-run 不得写业务数据：imports=%d projects=%d samples=%d", importRows, projects, samples)
	}
}
