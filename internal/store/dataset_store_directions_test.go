package store

import (
	"reflect"
	"testing"

	"github.com/1420970597/llm/internal/model"
)

// 断点续跑的核心是「已完成的领域不重跑」。这里锁定 PendingDomainIDs 的行为，
// 它是 resume 路径上唯一决定「跳过哪些领域」的地方。

func TestPendingDomainIDsSkipsCompletedDomains(t *testing.T) {
	domains := []model.Domain{
		{ID: 11, Name: "海军"},
		{ID: 12, Name: "陆军"},
		{ID: 13, Name: "空军"},
	}
	// 上一轮已完成 11 与 13，续跑时应只处理 12。
	pending := PendingDomainIDs(domains, []int64{11, 13})

	if !reflect.DeepEqual(pending, []int64{12}) {
		t.Fatalf("续跑应只处理未完成领域 [12]，实际得到 %v", pending)
	}
}

func TestPendingDomainIDsKeepsInputOrder(t *testing.T) {
	domains := []model.Domain{{ID: 3}, {ID: 1}, {ID: 2}}
	pending := PendingDomainIDs(domains, nil)

	if !reflect.DeepEqual(pending, []int64{3, 1, 2}) {
		t.Fatalf("应保持 domains 输入顺序 [3 1 2]，实际得到 %v", pending)
	}
}

func TestPendingDomainIDsAllDoneReturnsEmpty(t *testing.T) {
	domains := []model.Domain{{ID: 1}, {ID: 2}}
	pending := PendingDomainIDs(domains, []int64{1, 2})

	if len(pending) != 0 {
		t.Fatalf("全部已完成时应返回空切片，实际得到 %v", pending)
	}
}

func TestDirectionCursorRoundTrip(t *testing.T) {
	original := DirectionCursor{
		CompletedDomainIDs: []int64{7, 9},
		FailedDomainIDs:    []int64{8},
		DirectionCount:     4,
		ProducedDirections: 8,
	}

	encoded := EncodeDirectionCursor(original)
	decoded, err := DecodeDirectionCursor(encoded)
	if err != nil {
		t.Fatalf("游标解码失败: %v", err)
	}

	if !reflect.DeepEqual(decoded.CompletedDomainIDs, original.CompletedDomainIDs) {
		t.Fatalf("已完成领域不一致: %v", decoded.CompletedDomainIDs)
	}
	if !reflect.DeepEqual(decoded.FailedDomainIDs, original.FailedDomainIDs) {
		t.Fatalf("失败领域不一致: %v", decoded.FailedDomainIDs)
	}
	if decoded.DirectionCount != original.DirectionCount {
		t.Fatalf("方向数不一致: %d", decoded.DirectionCount)
	}
	if decoded.ProducedDirections != original.ProducedDirections {
		t.Fatalf("已产出方向数不一致: %d", decoded.ProducedDirections)
	}
}

// cursor 从 JSONB 读回时数字会变成 float64，必须仍能正确还原。
func TestDecodeDirectionCursorHandlesJSONBNumericCoercion(t *testing.T) {
	fromJSONB := map[string]any{
		"completedDomainIds": []any{float64(11), float64(12)},
		"failedDomainIds":    []any{},
		"directionCount":     float64(3),
		"producedDirections": float64(6),
	}

	decoded, err := DecodeDirectionCursor(fromJSONB)
	if err != nil {
		t.Fatalf("游标解码失败: %v", err)
	}
	if !reflect.DeepEqual(decoded.CompletedDomainIDs, []int64{11, 12}) {
		t.Fatalf("JSONB 数字应还原为 []int64{11,12}，实际 %#v", decoded.CompletedDomainIDs)
	}
	if decoded.DirectionCount != 3 || decoded.ProducedDirections != 6 {
		t.Fatalf("数值字段还原错误: %+v", decoded)
	}
}

func TestDecodeDirectionCursorEmptyIsZeroValue(t *testing.T) {
	decoded, err := DecodeDirectionCursor(nil)
	if err != nil {
		t.Fatalf("空游标不应报错: %v", err)
	}
	if len(decoded.CompletedDomainIDs) != 0 || decoded.DirectionCount != 0 {
		t.Fatalf("空游标应为零值，实际 %+v", decoded)
	}
}

func TestEncodeDirectionCursorNormalizesNilSlices(t *testing.T) {
	encoded := EncodeDirectionCursor(DirectionCursor{})

	completed, ok := encoded["completedDomainIds"].([]int64)
	if !ok {
		t.Fatalf("completedDomainIds 应为 []int64，实际 %#v", encoded["completedDomainIds"])
	}
	if len(completed) != 0 {
		t.Fatalf("nil 切片应归一化为空切片，实际 %v", completed)
	}
}

func TestCanonicalDomainNameNormalizesCaseAndSpace(t *testing.T) {
	cases := map[string]string{
		"  海上  巡逻 ":      "海上 巡逻",
		"Anti_Submarine": "anti submarine",
		"海上打击":           "海上打击",
	}
	for input, want := range cases {
		if got := CanonicalDomainName(input); got != want {
			t.Fatalf("CanonicalDomainName(%q) = %q，期望 %q", input, got, want)
		}
	}
}

// 不变量：已有断点进度的运行绝不能被空游标覆盖，否则续跑退化为从头重跑。
func TestShouldInitCursorKeepsExistingProgress(t *testing.T) {
	if ShouldInitCursor(DirectionCursor{CompletedDomainIDs: []int64{1, 2, 3}}) {
		t.Fatal("已有 3 个已完成领域时不得重新初始化游标，否则断点进度丢失")
	}
}

func TestShouldInitCursorTrueForFreshRun(t *testing.T) {
	if !ShouldInitCursor(DirectionCursor{}) {
		t.Fatal("从未跑过的运行应初始化游标以写入本次 m 值")
	}
}

func TestIsResumableStage(t *testing.T) {
	if !IsResumableStage(DirectionStage) {
		t.Fatalf("%q 应可续跑", DirectionStage)
	}
	if IsResumableStage("unknown-stage") {
		t.Fatal("未知阶段不应被当作可续跑")
	}
	if got := JobTypeForStage(DirectionStage); got != "directions.generate" {
		t.Fatalf("阶段到 job 类型映射错误: %q", got)
	}
}
