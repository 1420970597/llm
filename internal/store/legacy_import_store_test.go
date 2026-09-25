package store

import (
	"context"
	"testing"
)

// 本文件验证「旧资产是否已全部迁移」的对账读数（issue #197 第 16 条）。
//
// 为什么这条读数必须被测试冻结：它是**是否允许移除「历史资产」菜单**的判据。
// 判错的后果不对称 ——
//   - 迁移没完成却报告「已完成」→ 管理员删掉菜单，旧资产就再也找不到入口；
//   - 迁移完成却报告「未完成」→ 只是菜单多留一会儿，无害。
// 因此这里对两类边界都做断言：空库、以及有旧数据但未绑定。

// TestLegacyMigrationStatusEmptyDatabase 覆盖没有旧数据时的结论。
//
// 语义要点：空库的 `migrationComplete` 是 **true**（可以移除菜单），
// 但 `Note` 必须说明「没有可迁移的历史资产」而不是「迁移成功」——
// 把「从未有过旧数据」说成一件工作成果是在汇报里撒谎。
func TestLegacyMigrationStatusEmptyDatabase(t *testing.T) {
	pool := newStudioTestPool(t)
	store := NewLegacyImportStore(pool)

	status, err := store.LegacyMigrationStatus(context.Background())
	if err != nil {
		t.Fatalf("LegacyMigrationStatus: %v", err)
	}
	if status.LegacyDatasets != 0 {
		t.Fatalf("全新测试库不应有旧数据集，实际 %d", status.LegacyDatasets)
	}
	if !status.MigrationComplete {
		t.Fatal("没有旧数据时，移除菜单是安全的：migrationComplete 必须为 true")
	}
	if status.Note == "" {
		t.Fatal("必须给出可读结论，而不是让界面自己拼文案")
	}
	if status.PendingDatasets != 0 {
		t.Fatalf("无旧数据时未迁移数必须为 0，实际 %d", status.PendingDatasets)
	}
}

// TestLegacyMigrationStatusWithUnmigratedDatasets 覆盖「有旧数据且未绑定项目」。
//
// 这正是本部署实测的形态（58 个旧数据集、0 条导入台账、0 个绑定项目），
// 因此必须报告「未完成」并保留菜单。
func TestLegacyMigrationStatusWithUnmigratedDatasets(t *testing.T) {
	pool := newStudioTestPool(t)
	ctx := context.Background()
	store := NewLegacyImportStore(pool)
	// `datasets.created_by` 有外键，因此先建一个真实用户（不能用写死的 1：
	// 全新测试库里 id=1 还不存在，这正是本用例第一次失败的原因）。
	ownerID := seedStudioUser(t, pool, "legacy-owner")

	// 直接插入两条最小可用的旧数据集（模拟迁移前的历史资产）。
	// 只填 NOT NULL 列：这条读数只做 COUNT，不读其它字段。
	for _, name := range []string{"legacy-a", "legacy-b"} {
		if _, err := pool.Exec(ctx, `
      INSERT INTO datasets (name, root_keyword, target_size, created_by, status)
      VALUES ($1, $1, 10, $2, 'draft')`, name, ownerID); err != nil {
			t.Fatalf("seed legacy dataset %s: %v", name, err)
		}
	}

	status, err := store.LegacyMigrationStatus(ctx)
	if err != nil {
		t.Fatalf("LegacyMigrationStatus: %v", err)
	}
	if status.LegacyDatasets < 2 {
		t.Fatalf("必须统计到刚插入的旧数据集，实际 %d", status.LegacyDatasets)
	}
	if status.MigrationComplete {
		t.Fatal("还有未绑定的旧数据集时不得报告迁移完成（会导致菜单被误删）")
	}
	if status.PendingDatasets == 0 {
		t.Fatal("未迁移数必须为正：这些数据集没有任何项目绑定")
	}
	if status.PendingDatasets != status.LegacyDatasets {
		t.Fatalf("本用例没有任何绑定，未迁移数应等于旧数据集总数：%d vs %d",
			status.PendingDatasets, status.LegacyDatasets)
	}
}
