package main

import (
	"os"
	"strings"
	"testing"
)

// TestDirectionDoneUnitsMatchesTotalUnits 锁定 issue #141 的修复。
//
// 背景：`total_units` 在入队时算的是 `len(domains) * directionCount`（**方向数**，
// 见 apps/api/routes_directions.go 的 StartRun），而 worker 回写游标时传的却是
// `len(cursor.CompletedDomainIDs)`（**领域数**）。两个数不同单位，于是进度永远
// 显示成 10/30 这类「完成度 33%」的假象 —— 而该阶段其实已经全部完成。
//
// 实测（generation_runs.stage='directions'）：
//
//	 id  | status    | total_units | done_units | level1 | level2
//	219  | completed |          30 |         10 |     10 |     30   ← done 用了领域数
//	247  | completed |         100 |        100 |    100 |    100   ← 恰好相等，掩盖了缺陷
//
// 为什么必须有这条测试：247/248 两条因为「每个领域只产 1 个方向」，
// 领域数恰好等于方向数，缺陷完全没有暴露。只靠集成测试会被这类数据骗过去。
func TestDirectionDoneUnitsMatchesTotalUnits(t *testing.T) {
	cases := []struct {
		name       string
		produced   int // 已产出的**方向**数（与 total_units 同单位）
		totalUnits int // len(domains) * directionCount
		want       int
	}{
		// 核心回归：3 个领域 × 10 个方向 = 30。全部产出后必须是 30，不是 3。
		// 历史实现传领域数 3，于是进度停在 10%（3/30）。
		{"3 领域×10 方向全部产出", 30, 30, 30},
		// 只完成 1 个领域（产出 10 个方向）：进度应为 10/30，而不是 1/30。
		{"完成 1 个领域（10 个方向）", 10, 30, 10},
		// 单个领域多产（模型给了 4 个而不是 3 个）：不得超过 total_units，
		// 否则进度条会超过 100%。
		{"模型多产时封顶", 40, 30, 30},
		// total_units 缺失（0）时不做封顶，原样返回，避免把进度清零。
		{"total_units 未知时不封顶", 12, 0, 12},
		// 负值兜底（防御性；produced 由游标解码而来）。
		{"负值归零", -5, 30, 0},
		// 边界：恰好相等。
		{"恰好相等", 100, 100, 100},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := directionDoneUnits(tc.produced, tc.totalUnits); got != tc.want {
				t.Errorf("directionDoneUnits(%d, %d) = %d，期望 %d",
					tc.produced, tc.totalUnits, got, tc.want)
			}
		})
	}
}

// TestDirectionDoneUnitsCallSiteUsesDirectionCount 守住真正的回归点：**调用点**。
//
// 上面那个纯函数无法自己发现「调用者传错了单位」—— 传 10 就返回 10，
// 这是正确行为。issue #141 的缺陷在**调用点**：
//
//	// 错误（历史实现）：传领域数
//	SaveCursor(ctx, run.ID, cursor, len(cursor.CompletedDomainIDs), run.TotalUnits)
//	// 正确（修复后）：传方向数
//	doneUnits := directionDoneUnits(produced, run.TotalUnits)
//
// 因此这里直接对源码做结构性断言：SaveCursor 的 done_units 实参
// 必须是 directionDoneUnits(...) 的结果，而**不能**是 len(...DomainIDs)。
// 这类断言不依赖容器，且能精确锁住「单位用错」这个缺陷本身。
func TestDirectionDoneUnitsCallSiteUsesDirectionCount(t *testing.T) {
	src, err := os.ReadFile("job_directions.go")
	if err != nil {
		t.Fatalf("读取 job_directions.go: %v", err)
	}
	text := string(src)

	if !strings.Contains(text, "doneUnits := directionDoneUnits(produced, run.TotalUnits)") {
		t.Error("SaveCursor 的 done_units 必须来自 directionDoneUnits(produced, run.TotalUnits)：" +
			"produced 是已产出的**方向**数，与 total_units 同单位（issue #141）")
	}

	// 历史缺陷形态：直接把领域数当 done_units 传给 SaveCursor。
	if strings.Contains(text, "SaveCursor(ctx, run.ID, store.EncodeDirectionCursor(cursor), len(cursor.CompletedDomainIDs)") {
		t.Error("SaveCursor 的 done_units 又变回了 len(cursor.CompletedDomainIDs)（领域数）—— " +
			"这与 total_units（方向数）不同单位，会让进度停在 33%（issue #141 回归）")
	}

	// 兜底：SaveCursor 那一行不能直接出现 CompletedDomainIDs 的长度。
	for i, line := range strings.Split(text, "\n") {
		if strings.Contains(line, "SaveCursor(") && strings.Contains(line, "CompletedDomainIDs") {
			t.Errorf("job_directions.go:%d 的 SaveCursor 实参里出现 CompletedDomainIDs（领域数）—— "+
				"done_units 必须是方向数：%s", i+1, strings.TrimSpace(line))
		}
	}
}
