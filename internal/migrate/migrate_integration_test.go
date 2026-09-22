package migrate

import (
	"context"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

// 本文件是 T32 的**迁移门禁**（真实 Postgres）。
//
// 为什么必须存在：仓库里原有的迁移检查只有 `docker compose exec postgres psql -c '\dt'`
// —— 它只证明「server 起来了、有表」，不证明：
//
//   * `schema_migrations` 与 sql/migrations 目录**一一对应**（少记一条会在下次
//     启动时重复执行，而那种错误只在生产上以「迁移重复执行」的形式出现）；
//   * 重复执行不产生新行（幂等）；
//   * 关键约束/索引真的建起来了（T14 的分母保护、T16 的至少一名 owner 触发器、
//     T05 的跨项目复合外键）；
//   * **从旧夹具升级**：在只应用过 0021 的库上跑完剩余迁移，旧数据仍在。
//     把「从旧版本升级」与「空库安装」混为一谈，是迁移事故最常见的来源。
//
// 未设置 LLM_TEST_POSTGRES_DSN 时跳过（与仓库既有约定一致），
// 但 CI 的 integration job 一定会设置它，并由 check-integration-tests.sh 断言本测试
// **没有**被跳过 —— 「go test 成功但集成测试全部 Skip」不满足 T32。

func TestRunAppliesAllMigrationsAndIsIdempotent(t *testing.T) {
	adminDSN := os.Getenv("LLM_TEST_POSTGRES_DSN")
	if adminDSN == "" {
		t.Skip("输入缺失: LLM_TEST_POSTGRES_DSN 未设置，跳过真实 Postgres 集成测试")
	}
	ctx := context.Background()
	migrationsDir := filepath.Join("..", "..", "sql", "migrations")

	// 用**独立数据库**：脚本已经把迁移应用到共享库了，在共享库上数
	// schema_migrations 行数会把「脚本应用的」算成「runner 应用的」。
	dbName := "llm_migrate_check_" + strings.ReplaceAll(t.Name(), "/", "_")
	admin, err := pgxpool.New(ctx, adminDSN)
	if err != nil {
		t.Fatalf("connect admin: %v", err)
	}
	defer admin.Close()

	// DROP/CREATE DATABASE 不能在事务里，且必须用**同一连接**执行。
	conn, err := admin.Acquire(ctx)
	if err != nil {
		t.Fatalf("acquire admin conn: %v", err)
	}
	if _, err := conn.Exec(ctx, `DROP DATABASE IF EXISTS `+quoteIdent(dbName)); err != nil {
		conn.Release()
		t.Fatalf("drop test db: %v", err)
	}
	if _, err := conn.Exec(ctx, `CREATE DATABASE `+quoteIdent(dbName)); err != nil {
		conn.Release()
		t.Fatalf("create test db: %v", err)
	}
	conn.Release()
	t.Cleanup(func() {
		cleanup, err := admin.Acquire(context.Background())
		if err != nil {
			return
		}
		defer cleanup.Release()
		_, _ = cleanup.Exec(context.Background(), `DROP DATABASE IF EXISTS `+quoteIdent(dbName))
	})

	testDSN, err := databaseURL(adminDSN, dbName)
	if err != nil {
		t.Fatalf("build test dsn: %v", err)
	}
	pool, err := pgxpool.New(ctx, testDSN)
	if err != nil {
		t.Fatalf("connect test db: %v", err)
	}
	defer pool.Close()

	// ---------------------------------------------------------------------
	// 1. 从 0021 夹具升级：只应用前 0021 个迁移，写入旧数据，再跑完剩余迁移。
	// ---------------------------------------------------------------------
	preDir := t.TempDir()
	for _, name := range migrationFiles(t, migrationsDir) {
		if name > "0021" {
			break
		}
		copyFile(t, filepath.Join(migrationsDir, name), filepath.Join(preDir, name))
	}
	if err := Run(ctx, pool, preDir); err != nil {
		t.Fatalf("应用 0021 及以前的迁移失败：%v", err)
	}
	var fixtureID int64
	if err := pool.QueryRow(ctx, `
    INSERT INTO datasets (name, root_keyword, status) VALUES ('t32-fixture', '军事', 'completed')
    RETURNING id`).Scan(&fixtureID); err != nil {
		t.Fatalf("写入 0021 形状的夹具数据失败：%v", err)
	}

	// ---------------------------------------------------------------------
	// 2. 跑完全部迁移。
	// ---------------------------------------------------------------------
	if err := Run(ctx, pool, migrationsDir); err != nil {
		t.Fatalf("应用全部迁移失败：%v", err)
	}
	files := migrationFiles(t, migrationsDir)
	var recorded int
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM schema_migrations`).Scan(&recorded); err != nil {
		t.Fatalf("读取 schema_migrations 失败：%v", err)
	}
	if recorded != len(files) {
		t.Fatalf("schema_migrations 有 %d 行，而目录有 %d 个迁移文件：两者必须一一对应",
			recorded, len(files))
	}
	// 旧数据必须仍在（迁移是加法，不是重建）。
	var stillThere bool
	if err := pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM datasets WHERE id = $1)`, fixtureID).Scan(&stillThere); err != nil {
		t.Fatalf("检查夹具数据失败：%v", err)
	}
	if !stillThere {
		t.Fatal("从旧夹具升级后旧数据消失：迁移必须只做加法")
	}

	// ---------------------------------------------------------------------
	// 3. 关键约束/索引必须存在（它们承载的是产品规则，不是实现细节）。
	// ---------------------------------------------------------------------
	for _, probe := range []struct {
		what string
		sql  string
	}{
		{"实验评分当前有效的部分唯一索引", `SELECT EXISTS(SELECT 1 FROM pg_indexes WHERE indexname = 'uniq_experiment_score_current')`},
		{"实验项对样本版本的 RESTRICT 外键", `SELECT EXISTS(
         SELECT 1 FROM pg_constraint c
         JOIN pg_class t ON t.oid = c.conrelid
         WHERE c.conname = 'experiment_items_sample_version_id_fkey' AND t.relname = 'experiment_items')`},
		{"至少一名 owner 的触发器", `SELECT EXISTS(SELECT 1 FROM pg_trigger WHERE tgname = 'trg_project_members_keep_owner')`},
		{"批次与蓝图版本的跨项目复合外键", `SELECT EXISTS(SELECT 1 FROM pg_constraint WHERE conname = 'batches_blueprint_same_project')`},
		{"样本版本与批次单元的跨项目复合外键", `SELECT EXISTS(SELECT 1 FROM pg_constraint WHERE conname = 'sample_versions_batch_item_fk')`},
		{"发布候选表", `SELECT EXISTS(SELECT 1 FROM information_schema.tables WHERE table_name = 'release_candidates')`},
		{"GRPO 实验目标配置列（T24）", `SELECT EXISTS(SELECT 1 FROM information_schema.columns
         WHERE table_name = 'experiments' AND column_name = 'target_config')`},
	} {
		var exists bool
		if err := pool.QueryRow(ctx, probe.sql).Scan(&exists); err != nil {
			t.Fatalf("检查 %s 失败：%v", probe.what, err)
		}
		if !exists {
			t.Fatalf("迁移后缺少 %s：它承载一条产品规则，不能只靠「代码里记得」", probe.what)
		}
	}

	// ---------------------------------------------------------------------
	// 4. 幂等：再跑一次不产生新行。
	// ---------------------------------------------------------------------
	if err := Run(ctx, pool, migrationsDir); err != nil {
		t.Fatalf("重复执行迁移必须成功（幂等）：%v", err)
	}
	var recordedAgain int
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM schema_migrations`).Scan(&recordedAgain); err != nil {
		t.Fatalf("读取 schema_migrations 失败：%v", err)
	}
	if recordedAgain != recorded {
		t.Fatalf("重复执行迁移增加了记录：%d -> %d", recorded, recordedAgain)
	}
}

// migrationFiles 返回目录里的迁移文件名（已排序）。
func migrationFiles(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("读取迁移目录失败：%v", err)
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") {
			continue
		}
		names = append(names, entry.Name())
	}
	sort.Strings(names)
	return names
}

func copyFile(t *testing.T, from, to string) {
	t.Helper()
	content, err := os.ReadFile(from)
	if err != nil {
		t.Fatalf("读取 %s 失败：%v", from, err)
	}
	if err := os.WriteFile(to, content, 0o600); err != nil {
		t.Fatalf("写入 %s 失败：%v", to, err)
	}
}

// databaseURL 把 DSN 里的数据库名替换掉（保持其它参数不变）。
func databaseURL(dsn, dbName string) (string, error) {
	parsed, err := url.Parse(dsn)
	if err != nil {
		return "", err
	}
	parsed.Path = "/" + dbName
	return parsed.String(), nil
}

// quoteIdent 给标识符加双引号（数据库名来自测试自身，这里只是防手误）。
func quoteIdent(name string) string {
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}
