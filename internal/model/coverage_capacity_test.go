package model

import "testing"

// 本文件验证 issue #190 的权威口径：**覆盖矩阵的可产出量**。
//
// 为什么这条口径必须在 model 层：它同时被「保存/启动前的容量校验」与
// 「运行期的单元分配」使用。两份实现会让「界面说能产 12、实际产 1」
// 重新变成可能 —— 那正是 #190 的根因。

func capacityTestCoverage() CoveragePayload {
	return CoveragePayload{
		SchemaVersion: SchemaVersionFor(KindCoverage),
		Domains: []CoverageDomain{
			{
				StableID: "cold-chain", Name: "冷链",
				Directions: []CoverageDirection{
					{StableID: "temperature", Name: "温控", Quota: 3},
					{StableID: "transport", Name: "运输", Quota: 2},
				},
			},
			{
				StableID: "pharma", Name: "医药",
				Directions: []CoverageDirection{
					{StableID: "storage", Name: "存储", Quota: 4},
				},
			},
		},
	}
}

// TestCoverageCapacityMatchesAllocation 覆盖正常路径：
// 容量必须等于实际能分配出的单元数，且分配是确定性的。
func TestCoverageCapacityMatchesAllocation(t *testing.T) {
	coverage := capacityTestCoverage()

	if got := CoverageCapacity(coverage); got != 9 {
		t.Fatalf("容量必须是 3+2+4=9，实际 %d", got)
	}
	// 用远大于容量的 limit 分配，得到的单元数必须正好是容量。
	units := AllocateCoverageUnits(coverage, 1000)
	if len(units) != CoverageCapacity(coverage) {
		t.Fatalf("分配 %d 个单元，而容量是 %d：两者必须相等", len(units), CoverageCapacity(coverage))
	}

	// 确定性：同输入两次分配完全相同（恢复失败项依赖它）。
	again := AllocateCoverageUnits(coverage, 1000)
	for index := range units {
		if units[index] != again[index] {
			t.Fatalf("第 %d 个单元不确定：%+v vs %+v", index+1, units[index], again[index])
		}
	}

	// 顺序：先领域、后方向、再方向内序号。
	want := []string{
		"cold-chain/temperature#1", "cold-chain/temperature#2", "cold-chain/temperature#3",
		"cold-chain/transport#1", "cold-chain/transport#2",
		"pharma/storage#1", "pharma/storage#2", "pharma/storage#3", "pharma/storage#4",
	}
	if len(units) != len(want) {
		t.Fatalf("单元数应为 %d，实际 %d", len(want), len(units))
	}
	for index, expected := range want {
		got := units[index].DomainStableID + "/" + units[index].DirectionStableID + "#" +
			itoa(units[index].Ordinal)
		if got != expected {
			t.Fatalf("第 %d 个单元应为 %s，实际 %s", index+1, expected, got)
		}
	}
}

// TestCoverageCapacityHandlesEdgeCases 覆盖边界路径：
// 配额为 0/负数视为 1、limit ≤ 0 不分配、空覆盖容量为 0。
func TestCoverageCapacityHandlesEdgeCases(t *testing.T) {
	zeroQuota := CoveragePayload{Domains: []CoverageDomain{{
		StableID: "d", Name: "领域",
		Directions: []CoverageDirection{
			// quota ≤ 0 在界面上是「有名字的方向」，因此必须算作 1 个可产出单元，
			// 否则容量会比实际分配少，校验会误拒合法的计划量。
			{StableID: "zero", Name: "零配额", Quota: 0},
			{StableID: "negative", Name: "负配额", Quota: -5},
		},
	}}}
	if got := CoverageCapacity(zeroQuota); got != 2 {
		t.Fatalf("quota ≤ 0 必须视为 1，容量应为 2，实际 %d", got)
	}

	if got := CoverageCapacity(CoveragePayload{}); got != 0 {
		t.Fatalf("空覆盖计划的容量必须为 0，实际 %d", got)
	}
	if got := AllocateCoverageUnits(zeroQuota, 0); len(got) != 0 {
		t.Fatalf("limit 为 0 时不得分配单元，实际 %d", len(got))
	}
	if got := AllocateCoverageUnits(zeroQuota, -1); len(got) != 0 {
		t.Fatalf("limit 为负数时不得分配单元，实际 %d", len(got))
	}

	// 截断：limit 小于容量时按顺序取前 limit 个。
	if got := AllocateCoverageUnits(capacityTestCoverage(), 4); len(got) != 4 {
		t.Fatalf("limit=4 时必须恰好分配 4 个，实际 %d", len(got))
	}
}

func itoa(value int) string {
	if value == 0 {
		return "0"
	}
	digits := make([]byte, 0, 12)
	for value > 0 {
		digits = append([]byte{byte('0' + value%10)}, digits...)
		value /= 10
	}
	return string(digits)
}
