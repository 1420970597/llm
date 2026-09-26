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
//
// 为什么全部断言都基于**增量**而不是绝对值：本包的测试共用同一个临时 Postgres
// （`newStudioTestPool` 把 DSN 指向同一个库），因此「库里有 0 条旧数据」这种
// 假设在整套测试里不成立 —— 实测过：单独跑通过、全量跑失败（另两个用例先插了数据）。
// 断言增量才能同时满足「单跑」与「全量跑」。
func TestLegacyMigrationStatusIsSelfConsistent(t *testing.T) {
	pool := newStudioTestPool(t)
	status, err := NewLegacyImportStore(pool).LegacyMigrationStatus(context.Background())
	if err != nil {
		t.Fatalf("LegacyMigrationStatus: %v", err)
	}

	// 不变式 1：结论必须与读数一致。这是界面拿来决定「能不能删菜单」的字段，
	// 不允许出现 `migrationComplete=true` 但 `pendingDatasets>0` 的自相矛盾。
	if status.MigrationComplete != (status.PendingDatasets == 0) {
		t.Fatalf("结论与读数矛盾：migrationComplete=%v pending=%d",
			status.MigrationComplete, status.PendingDatasets)
	}
	// 不变式 2：未迁移数不可能超过旧数据集总数。
	if status.PendingDatasets > status.LegacyDatasets {
		t.Fatalf("未迁移数(%d) 不可能超过旧数据集总数(%d)",
			status.PendingDatasets, status.LegacyDatasets)
	}
	// 不变式 3：无旧数据时未迁移数必须为 0（而不是一个负数或脏值）。
	if status.LegacyDatasets == 0 && status.PendingDatasets != 0 {
		t.Fatalf("没有旧数据集时未迁移数必须为 0，实际 %d", status.PendingDatasets)
	}
	// 不变式 4：必须给出可读结论文案（前端只渲染，不自己拼）。
	if status.Note == "" {
		t.Fatal("必须给出可读结论，而不是让界面自己拼文案")
	}
}

// TestLegacyMigrationStatusCountsUnmigratedDatasets 覆盖「有旧数据且未绑定项目」。
//
// 这正是本部署实测的形态（58 个旧数据集、0 条导入台账、0 个绑定项目），
// 因此必须报告「未完成」并保留菜单。
func TestLegacyMigrationStatusCountsUnmigratedDatasets(t *testing.T) {
	pool := newStudioTestPool(t)
	ctx := context.Background()
	store := NewLegacyImportStore(pool)

	before, err := store.LegacyMigrationStatus(ctx)
	if err != nil {
		t.Fatalf("baseline LegacyMigrationStatus: %v", err)
	}

	// `datasets.created_by` 有外键，因此先建一个真实用户（不能用写死的 1：
	// 全新测试库里 id=1 还不存在）。
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

	after, err := store.LegacyMigrationStatus(ctx)
	if err != nil {
		t.Fatalf("LegacyMigrationStatus: %v", err)
	}
	if after.LegacyDatasets != before.LegacyDatasets+2 {
		t.Fatalf("旧数据集总数必须增加 2：%d -> %d", before.LegacyDatasets, after.LegacyDatasets)
	}
	// 新插入的两条没有任何项目绑定，因此必然计入「未迁移」。
	if after.PendingDatasets != before.PendingDatasets+2 {
		t.Fatalf("未迁移数必须增加 2（两条新数据都没有绑定项目）：%d -> %d",
			before.PendingDatasets, after.PendingDatasets)
	}
	if after.MigrationComplete {
		t.Fatal("还有未绑定的旧数据集时不得报告迁移完成（会导致菜单被误删）")
	}
	if after.Note == "" {
		t.Fatal("未完成时也必须给出可读结论")
	}
}
