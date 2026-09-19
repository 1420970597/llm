package llm

import (
	"context"
	"strconv"
	"strings"
	"testing"
)

// difficultyLevels 是断言分布时使用的固定档位顺序。
var difficultyLevels = []string{DifficultyEasy, DifficultyMedium, DifficultyHard}

// countDifficulty 统计一组档位中每档出现次数，恒含三档（缺档为 0）。
func countDifficulty(levels []string) map[string]int {
	counts := map[string]int{DifficultyEasy: 0, DifficultyMedium: 0, DifficultyHard: 0}
	for _, level := range levels {
		counts[level]++
	}
	return counts
}

// sweepAssignee 按 index=0..total-1 调用分配函数，收集实际分布。
func sweepAssignee(assign func(index, total int) string, total int) map[string]int {
	levels := make([]string, 0, total)
	for index := 0; index < total; index++ {
		levels = append(levels, assign(index, total))
	}
	return countDifficulty(levels)
}

// assertEveryLevelPresent 断言三档都至少出现一次。
func assertEveryLevelPresent(t *testing.T, label string, counts map[string]int) {
	t.Helper()
	for _, level := range difficultyLevels {
		if counts[level] < 1 {
			t.Errorf("%s: 档位 %s 缺失，分布=%v", label, level, counts)
		}
	}
}

// assertSumEquals 断言配额之和恰好等于 total。
func assertSumEquals(t *testing.T, label string, allocation map[string]int, total int) {
	t.Helper()
	sum := 0
	for _, level := range difficultyLevels {
		if allocation[level] < 0 {
			t.Errorf("%s: 档位 %s 出现负配额 %d（allocation=%v）", label, level, allocation[level], allocation)
		}
		sum += allocation[level]
	}
	if sum != total {
		t.Errorf("%s: 配额之和 = %d, want %d（allocation=%v）", label, sum, total, allocation)
	}
}

// defaultMixForTest 是契约与前端默认使用的配比。
var defaultMixForTest = map[string]float64{
	DifficultyEasy:   0.3,
	DifficultyMedium: 0.5,
	DifficultyHard:   0.2,
}

// ── issue #10：DifficultyAssigner 在小 total 下丢档 ───────────────────────────
//
// 历史缺陷：bucket := (index*10)/total 是整数除法，total=2 时 bucket 只取 {0,5}，
// 困难档恒为 0；total=3 时同样为 0。生产 dataset 6 已复现（每组 2 条问题只有
// easy + medium）。以下用例锁定修复后的分布：total >= 3 三档齐全，total=2 时
// 也必须保留困难档。

// total=2 必须保留困难档。这是生产已复现的形状（dataset 6 每个方向 2 个问题）。
func TestDifficultyAssignerKeepsHardAtTotalTwo(t *testing.T) {
	counts := sweepAssignee(DifficultyAssigner, 2)
	if counts[DifficultyHard] < 1 {
		t.Fatalf("total=2 时困难档必须至少 1 条，实际分布=%v", counts)
	}
	// 槽位只有 2 个而档位有 3 个，规则是「按难度降序保留」：困难 + 中等。
	if counts[DifficultyEasy] != 0 {
		t.Errorf("total=2 时槽位不足，按难度降序应保留困难+中等，实际分布=%v", counts)
	}
	if counts[DifficultyMedium] != 1 || counts[DifficultyHard] != 1 {
		t.Errorf("total=2 分布 = %v, want medium=1 hard=1", counts)
	}
}

// 生产回归：dataset 6 的每个方向 2 条问题，不允许再出现「全组无困难档」。
func TestDifficultyAssignerProductionShapeDatasetSix(t *testing.T) {
	// 每个 direction_domain_id 各自独立分配，故对每个分组分别断言。
	for direction := 0; direction < 3; direction++ {
		counts := sweepAssignee(DifficultyAssigner, 2)
		if counts[DifficultyHard] == 0 {
			t.Fatalf("direction 分组 %d 的 2 条问题里困难档为 0，分布=%v（生产 dataset 6 缺陷复现）", direction, counts)
		}
	}
}

// total >= 3 时三档齐全，每档至少 1 条。
func TestDifficultyAssignerKeepsEveryLevelForSmallTotals(t *testing.T) {
	for total := 3; total <= 20; total++ {
		counts := sweepAssignee(DifficultyAssigner, total)
		assertEveryLevelPresent(t, "DifficultyAssigner total="+itoa(total), counts)
	}
}

// total=1 时槽位只有一个，规则是把唯一槽位留给最难的档位，而不是最容易的。
//
// 历史行为是返回 easy（bucket=0 → easy），导致「只生成 1 个问题」时永远拿不到
// 困难样本；修复后按难度降序保留，唯一槽位给困难。
func TestDifficultyAssignerSingleSlotPrefersHardest(t *testing.T) {
	if got := DifficultyAssigner(0, 1); got != DifficultyHard {
		t.Fatalf("total=1 时唯一槽位应按难度降序保留困难档，got %s", got)
	}
	counts := sweepAssignee(DifficultyAssigner, 1)
	if counts[DifficultyHard] != 1 {
		t.Errorf("total=1 分布 = %v, want hard=1", counts)
	}
}

// 默认实现的 3:4:3 配比必须体现在大 total 的分布上（不是只体现在注释里）。
func TestDifficultyAssignerFollowsThreeFourThreeMix(t *testing.T) {
	const total = 10
	counts := sweepAssignee(DifficultyAssigner, total)
	if counts[DifficultyEasy] != 3 || counts[DifficultyMedium] != 4 || counts[DifficultyHard] != 3 {
		t.Fatalf("total=10 分布 = %v, want easy=3 medium=4 hard=3（3:4:3）", counts)
	}
}

// total <= 0 时必须返回中等档而不是 panic。
func TestDifficultyAssignerHandlesNonPositiveTotal(t *testing.T) {
	for _, total := range []int{0, -1, -100} {
		if got := DifficultyAssigner(0, total); got != DifficultyMedium {
			t.Errorf("DifficultyAssigner(0, %d) = %s, want %s", total, got, DifficultyMedium)
		}
	}
}

// 档位字符串与数值分必须一一对应，且顺序单调。
func TestAssignDifficultyKeepsScoreConsistent(t *testing.T) {
	scoreByLevel := map[string]int{}
	for index := 0; index < 30; index++ {
		level, score := AssignDifficulty(index, 30)
		if want := DifficultyScoreOf(level); score != want {
			t.Fatalf("index=%d level=%s score=%d, want %d", index, level, score, want)
		}
		scoreByLevel[level] = score
	}
	if scoreByLevel[DifficultyEasy] >= scoreByLevel[DifficultyMedium] {
		t.Errorf("easy 分数 %d 应小于 medium 分数 %d", scoreByLevel[DifficultyEasy], scoreByLevel[DifficultyMedium])
	}
	if scoreByLevel[DifficultyMedium] >= scoreByLevel[DifficultyHard] {
		t.Errorf("medium 分数 %d 应小于 hard 分数 %d", scoreByLevel[DifficultyMedium], scoreByLevel[DifficultyHard])
	}
}

// 未知档位字符串必须归为中等档的分数，不能产生 0 分。
func TestDifficultyScoreOfUnknownLevelFallsBackToMedium(t *testing.T) {
	for _, level := range []string{"", "unknown", "EASY", "极度困难"} {
		if got := DifficultyScoreOf(level); got != DifficultyScoreOf(DifficultyMedium) {
			t.Errorf("DifficultyScoreOf(%q) = %d, want %d", level, got, DifficultyScoreOf(DifficultyMedium))
		}
	}
}

// ── issue #11：DifficultyFromMix 在小 total 下丢档 ───────────────────────────

// total=2 且配比含困难档时，困难档必须至少 1 条。
//
// 历史缺陷：位置区间法下 position 只取 {0, 0.5}，困难档区间 [0.8, 1.0) 一个采样点
// 都没有，故 hard 恒为 0。
func TestDifficultyFromMixKeepsHardAtTotalTwo(t *testing.T) {
	mixes := []map[string]float64{
		defaultMixForTest,
		{DifficultyEasy: 0.5, DifficultyMedium: 0.3, DifficultyHard: 0.2},
		{DifficultyEasy: 0.1, DifficultyMedium: 0.1, DifficultyHard: 0.8},
		// 困难档权重很小，但用户仍然要求它出现。
		{DifficultyEasy: 0.8, DifficultyMedium: 0.1, DifficultyHard: 0.1},
		// 只有困难与中等两个档位参与，槽位恰好够。
		{DifficultyMedium: 0.5, DifficultyHard: 0.5},
	}
	for _, mix := range mixes {
		counts := sweepAssignee(assignForMix(mix), 2)
		if counts[DifficultyHard] < 1 {
			t.Errorf("mix=%v total=2 时困难档必须至少 1 条，实际分布=%v", mix, counts)
		}
	}
}

// total=3 且配比含困难档时三档齐全（issue #11 报告的核心场景）。
func TestDifficultyFromMixKeepsEveryLevelAtTotalThree(t *testing.T) {
	counts := sweepAssignee(assignForMix(defaultMixForTest), 3)
	assertEveryLevelPresent(t, "DifficultyFromMix total=3", counts)
}

// total=4 且配比含困难档时三档齐全（issue #11 的第二处复现）。
func TestDifficultyFromMixKeepsEveryLevelAtTotalFour(t *testing.T) {
	counts := sweepAssignee(assignForMix(defaultMixForTest), 4)
	assertEveryLevelPresent(t, "DifficultyFromMix total=4", counts)
}

// 凡是 total >= 参与分配的档位数，每一档都至少出现一次。
func TestDifficultyFromMixKeepsEveryPositiveWeightLevel(t *testing.T) {
	mixes := []struct {
		name string
		mix  map[string]float64
	}{
		{"契约默认 0.3/0.5/0.2", defaultMixForTest},
		{"三等分", map[string]float64{DifficultyEasy: 1, DifficultyMedium: 1, DifficultyHard: 1}},
		{"两档 0.7/0.3", map[string]float64{DifficultyEasy: 0.7, DifficultyMedium: 0.3}},
		{"极端 0.05/0.05/0.9", map[string]float64{DifficultyEasy: 0.05, DifficultyMedium: 0.05, DifficultyHard: 0.9}},
		{"微差 0.34/0.33/0.33", map[string]float64{DifficultyEasy: 0.34, DifficultyMedium: 0.33, DifficultyHard: 0.33}},
		{"仅困难", map[string]float64{DifficultyHard: 1}},
		{"仅简单", map[string]float64{DifficultyEasy: 1}},
		{"困难+中等", map[string]float64{DifficultyMedium: 0.5, DifficultyHard: 0.5}},
	}
	for _, item := range mixes {
		participants := 0
		for _, level := range difficultyLevels {
			if item.mix[level] > 0 {
				participants++
			}
		}
		for total := participants; total <= 20; total++ {
			counts := sweepAssignee(assignForMix(item.mix), total)
			for _, level := range difficultyLevels {
				if item.mix[level] > 0 && counts[level] < 1 {
					t.Errorf("mix=%s total=%d 档位 %s 缺失，分布=%v", item.name, total, level, counts)
				}
				if item.mix[level] == 0 && counts[level] != 0 {
					t.Errorf("mix=%s total=%d 档位 %s 权重为 0 却出现 %d 条，分布=%v",
						item.name, total, level, counts[level], counts)
				}
			}
		}
	}
}

// 槽位不足（total < 参与档位数）时按难度降序保留：total=2/3档 → 困难+中等。
func TestDifficultyFromMixPrefersHarderLevelsWhenSlotsAreScarce(t *testing.T) {
	counts := sweepAssignee(assignForMix(defaultMixForTest), 2)
	if counts[DifficultyHard] != 1 || counts[DifficultyMedium] != 1 || counts[DifficultyEasy] != 0 {
		t.Fatalf("total=2 分布 = %v, want hard=1 medium=1 easy=0（难度降序保留）", counts)
	}
	// 只有两档参与时，total=2 恰好覆盖两个档位。
	twoLevel := map[string]float64{DifficultyMedium: 0.5, DifficultyHard: 0.5}
	counts = sweepAssignee(assignForMix(twoLevel), 2)
	if counts[DifficultyHard] != 1 || counts[DifficultyMedium] != 1 {
		t.Fatalf("两档 total=2 分布 = %v, want medium=1 hard=1", counts)
	}
}

// 大 total 下分布必须贴近用户配比（误差不超过 1 条）。
func TestDifficultyFromMixTracksRequestedMix(t *testing.T) {
	const total = 40
	counts := sweepAssignee(assignForMix(defaultMixForTest), total)
	want := map[string]int{DifficultyEasy: 12, DifficultyMedium: 20, DifficultyHard: 8}
	for _, level := range difficultyLevels {
		diff := counts[level] - want[level]
		if diff < -1 || diff > 1 {
			t.Errorf("total=%d 档位 %s = %d, want 约 %d（±1），分布=%v", total, level, counts[level], want[level], counts)
		}
	}
}

// mix 为空 / nil 时回退到 DifficultyAssigner，而不是丢档或 panic。
func TestDifficultyFromMixFallsBackWhenMixMissing(t *testing.T) {
	cases := []map[string]float64{
		nil,
		{},
		{DifficultyEasy: 0, DifficultyMedium: 0, DifficultyHard: 0},
		{"unknown_level": 1},
	}
	for _, mix := range cases {
		for index := 0; index < 5; index++ {
			got := DifficultyFromMix(mix, index, 5)
			want := DifficultyAssigner(index, 5)
			if got != want {
				t.Errorf("mix=%v index=%d DifficultyFromMix = %s, want 回退到 DifficultyAssigner 的 %s", mix, index, got, want)
			}
		}
	}
}

// 配比里有非法项但仍有合法档位时，不整体回退：丢弃非法项，按剩余档位分配。
//
// 这条边界很容易被误判成「mix 无效 → 回退默认」，从而让某个档位意外复活。
func TestDifficultyFromMixDropsInvalidWeightsKeepsValidOnes(t *testing.T) {
	// 负权重被丢弃，只剩 medium 参与分配，因此 5 条全部落在 medium。
	mix := map[string]float64{DifficultyEasy: -1, DifficultyMedium: 2}
	counts := sweepAssignee(assignForMix(mix), 5)
	if counts[DifficultyMedium] != 5 || counts[DifficultyEasy] != 0 || counts[DifficultyHard] != 0 {
		t.Fatalf("mix=%v 分布 = %v, want medium=5（负权重应被丢弃而非触发整体回退）", mix, counts)
	}
	// 未知档位被丢弃，剩余合法档位照常分配。
	mix = map[string]float64{DifficultyHard: 1, "unknown_level": 5}
	counts = sweepAssignee(assignForMix(mix), 4)
	if counts[DifficultyHard] != 4 {
		t.Fatalf("mix=%v 分布 = %v, want hard=4（未知档位应被丢弃）", mix, counts)
	}
}

// total <= 0 时必须回退而不是 panic 或越界。
func TestDifficultyFromMixHandlesNonPositiveTotal(t *testing.T) {
	for _, total := range []int{0, -1, -50} {
		if got := DifficultyFromMix(defaultMixForTest, 0, total); got != DifficultyMedium {
			t.Errorf("total=%d 应回退到 DifficultyAssigner（medium），got %s", total, got)
		}
	}
}

// index 越界时必须收敛到首尾档位，不得 panic。
func TestDifficultyFromMixClampsOutOfRangeIndex(t *testing.T) {
	if got := DifficultyFromMix(defaultMixForTest, -1, 5); got != DifficultyEasy {
		t.Errorf("index=-1 应收敛到首个档位 easy，got %s", got)
	}
	if got := DifficultyFromMix(defaultMixForTest, 99, 5); got != DifficultyHard {
		t.Errorf("index=99 应收敛到末个档位 hard，got %s", got)
	}
}

// 同一配比、同一 index 必须给出可重现的结果（无 map 遍历顺序依赖）。
func TestDifficultyFromMixIsDeterministic(t *testing.T) {
	for index := 0; index < 10; index++ {
		first := DifficultyFromMix(defaultMixForTest, index, 10)
		for round := 0; round < 50; round++ {
			if got := DifficultyFromMix(defaultMixForTest, index, 10); got != first {
				t.Fatalf("index=%d 第 %d 次调用得到 %s，首次为 %s（结果不可重现）", index, round, got, first)
			}
		}
	}
}

// ── 跨入口一致性：三个入口共用同一套配额算法 ─────────────────────────────────

// DifficultyFromMix 与 v2 生成路径的分配计划必须逐条一致。
//
// 两个入口若各写一份算术，会出现「同一个配比在两条路径下分布不同」的隐性缺陷；
// 这条用例把「共用同一份配额算法」变成可断言的契约。
func TestDifficultyFromMixMatchesGenerationPlan(t *testing.T) {
	mixes := []map[string]float64{
		defaultMixForTest,
		{DifficultyEasy: 1, DifficultyMedium: 1, DifficultyHard: 1},
		{DifficultyEasy: 0.7, DifficultyMedium: 0.3},
		{DifficultyEasy: 0.05, DifficultyMedium: 0.05, DifficultyHard: 0.9},
		{DifficultyEasy: 0.5, DifficultyMedium: 0.5, DifficultyHard: 0},
	}
	for _, mix := range mixes {
		for total := 1; total <= 25; total++ {
			allocation := AllocateDifficultyMix(total, mix)
			plan := difficultyPlan(allocation, total)
			for index := 0; index < total; index++ {
				got := DifficultyFromMix(mix, index, total)
				if want := plan[index]; got != want {
					t.Fatalf("mix=%v total=%d index=%d: DifficultyFromMix=%s 但生成计划为 %s（allocation=%v）",
						mix, total, index, got, want, allocation)
				}
			}
		}
	}
}

// 权重为 0 的档位在两条路径下都必须保持 0 条。
//
// 用户显式传 hard:0 表示「不要困难题」，补齐保底名额时不得把它强行加回来。
func TestZeroWeightLevelStaysEmptyInBothPaths(t *testing.T) {
	mix := map[string]float64{DifficultyEasy: 0.5, DifficultyMedium: 0.5, DifficultyHard: 0}
	for total := 1; total <= 20; total++ {
		allocation := AllocateDifficultyMix(total, mix)
		if allocation[DifficultyHard] != 0 {
			t.Errorf("total=%d 时 hard 权重为 0，却分配了 %d 条（allocation=%v）", total, allocation[DifficultyHard], allocation)
		}
		counts := sweepAssignee(assignForMix(mix), total)
		if counts[DifficultyHard] != 0 {
			t.Errorf("total=%d 时 DifficultyFromMix 产出 %d 条 hard，want 0", total, counts[DifficultyHard])
		}
	}
}

// 配比只声明一个档位时，total 必须全部落在该档位。
func TestSingleLevelMixUsesAllSlots(t *testing.T) {
	for _, level := range difficultyLevels {
		mix := map[string]float64{level: 1}
		for total := 1; total <= 10; total++ {
			allocation := AllocateDifficultyMix(total, mix)
			if allocation[level] != total {
				t.Errorf("mix={%s:1} total=%d allocation=%v, want %s=%d", level, total, allocation, level, total)
			}
			counts := sweepAssignee(assignForMix(mix), total)
			if counts[level] != total {
				t.Errorf("mix={%s:1} total=%d 分布=%v, want %s=%d", level, total, counts, level, total)
			}
		}
	}
}

// assignForMix 把配比闭包成单一的 index→level 函数，便于复用 sweepAssignee。
func assignForMix(mix map[string]float64) func(index, total int) string {
	return func(index, total int) string {
		return DifficultyFromMix(mix, index, total)
	}
}

// itoa 把整数渲染为十进制文本，供断言消息使用。
func itoa(value int) string {
	return strconv.Itoa(value)
}

// 配额算法必须保证各档之和恒等于 total，覆盖多种配比与 1..40 的规模。
func TestAllocateDifficultyQuotaAlwaysSumsToTotal(t *testing.T) {
	mixes := []map[string]float64{
		defaultMixForTest,
		{DifficultyEasy: 1, DifficultyMedium: 1, DifficultyHard: 1},
		{DifficultyEasy: 0.7, DifficultyMedium: 0.3},
		{DifficultyEasy: 0.05, DifficultyMedium: 0.05, DifficultyHard: 0.9},
		{DifficultyEasy: 0.34, DifficultyMedium: 0.33, DifficultyHard: 0.33},
		{DifficultyEasy: 0.5, DifficultyMedium: 0.5, DifficultyHard: 0},
		{DifficultyHard: 1},
		{DifficultyEasy: 1},
	}
	for _, mix := range mixes {
		for total := 1; total <= 40; total++ {
			allocation := AllocateDifficultyMix(total, mix)
			assertSumEquals(t, "mix="+mixLabel(mix)+" total="+itoa(total), allocation, total)
		}
	}
}

// 配额算法在 total 小于参与档位数时按难度降序保留，且总数仍然守恒。
func TestAllocateDifficultyQuotaScarceSlotsPreferHarderLevels(t *testing.T) {
	allocation := AllocateDifficultyMix(2, defaultMixForTest)
	if allocation[DifficultyHard] != 1 || allocation[DifficultyMedium] != 1 || allocation[DifficultyEasy] != 0 {
		t.Fatalf("total=2 allocation = %v, want hard=1 medium=1 easy=0", allocation)
	}
	allocation = AllocateDifficultyMix(1, defaultMixForTest)
	if allocation[DifficultyHard] != 1 {
		t.Fatalf("total=1 allocation = %v, want hard=1", allocation)
	}
	// 只有两档参与时，total=1 保留更难的那一档。
	allocation = AllocateDifficultyMix(1, map[string]float64{DifficultyEasy: 0.8, DifficultyHard: 0.2})
	if allocation[DifficultyHard] != 1 || allocation[DifficultyEasy] != 0 {
		t.Fatalf("两档 total=1 allocation = %v, want hard=1 easy=0", allocation)
	}
}

// 余数分配在等价小数部分之间必须确定，结果可重现。
func TestAllocateDifficultyQuotaIsDeterministic(t *testing.T) {
	mix := map[string]float64{DifficultyEasy: 0.34, DifficultyMedium: 0.33, DifficultyHard: 0.33}
	first := AllocateDifficultyMix(9, mix)
	for round := 0; round < 50; round++ {
		got := AllocateDifficultyMix(9, mix)
		for _, level := range difficultyLevels {
			if got[level] != first[level] {
				t.Fatalf("第 %d 次分配 %v 与首次 %v 不一致（结果不可重现）", round, got, first)
			}
		}
	}
}

// total <= 0 时配额全为 0，不 panic、不出现负数。
func TestAllocateDifficultyQuotaHandlesNonPositiveTotal(t *testing.T) {
	for _, total := range []int{0, -1, -100} {
		allocation := AllocateDifficultyMix(total, defaultMixForTest)
		assertSumEquals(t, "total="+itoa(total), allocation, 0)
	}
}

// ── 端到端：真实生成路径的落库难度必须覆盖困难档 ─────────────────────────────

// 每个方向生成 2 个问题时，真实生成路径落库的难度必须包含困难档。
//
// 这条用例走完整的 GenerateQuestionsV2 → generateForDirection → 落库字段构造，
// 用 fakeProvider（同包既有测试基础设施）提供确定性响应，锁定 issue #10 的生产形状：
// dataset 6 每个方向 2 条问题，历史结果是「easy + medium」，困难档为 0。
func TestGenerateQuestionsV2ProducesHardAtTwoPerDirection(t *testing.T) {
	server := fakeProvider(t, `[
      {"content":"问题一：近海巡逻遭遇可疑目标","difficulty":"easy"},
      {"content":"问题二：编队护航中的通信中断","difficulty":"medium"}
    ]`)
	// 用户未指定配比，走 defaultDifficultyMix（0.3/0.5/0.2）。
	input := fakeInput(server, 2, nil)

	questions, err := GenerateQuestionsV2(context.Background(), fakeProviderConfig(server), input)
	if err != nil {
		t.Fatalf("generate failed: %v", err)
	}
	if len(questions) != 2 {
		t.Fatalf("generated %d questions, want 2", len(questions))
	}

	levels := make([]string, 0, len(questions))
	for _, question := range questions {
		levels = append(levels, question.Difficulty)
		if want := DifficultyScoreOf(question.Difficulty); question.DifficultyScore != want {
			t.Errorf("difficulty=%s 的 difficultyScore=%d, want %d", question.Difficulty, question.DifficultyScore, want)
		}
	}
	counts := countDifficulty(levels)
	if counts[DifficultyHard] < 1 {
		t.Fatalf("每方向 2 条问题时困难档必须至少 1 条，实际落库分布=%v（levels=%v）", counts, levels)
	}
}

// 用户显式指定含困难档的配比且每方向 3 条时，落库分布必须三档齐全。
func TestGenerateQuestionsV2ProducesEveryLevelAtThreePerDirection(t *testing.T) {
	server := fakeProvider(t, `[
      {"content":"问题甲：例行巡逻时发现不明小艇靠近，请给出处置方案。","difficulty":"medium"},
      {"content":"问题乙：突发拦截任务中通信中断，请给出处置方案。","difficulty":"medium"},
      {"content":"问题丙：同时出现多批可疑目标，请给出处置优先级。","difficulty":"medium"}
    ]`)
	input := fakeInput(server, 3, defaultMixForTest)

	questions, err := GenerateQuestionsV2(context.Background(), fakeProviderConfig(server), input)
	if err != nil {
		t.Fatalf("generate failed: %v", err)
	}
	levels := make([]string, 0, len(questions))
	for _, question := range questions {
		levels = append(levels, question.Difficulty)
	}
	assertEveryLevelPresent(t, "GenerateQuestionsV2 每方向 3 条", countDifficulty(levels))
}

// mixLabel 为断言消息生成稳定的配比标识（避免 map 打印顺序不定）。
func mixLabel(mix map[string]float64) string {
	parts := make([]string, 0, len(difficultyLevels))
	for _, level := range difficultyLevels {
		if weight, ok := mix[level]; ok {
			parts = append(parts, level+"="+strconv.FormatFloat(weight, 'g', -1, 64))
		}
	}
	return "{" + strings.Join(parts, ",") + "}"
}
