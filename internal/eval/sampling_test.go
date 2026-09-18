package eval

import (
	"strings"
	"testing"
)

func ids(values ...int64) []int64 { return values }

func TestSampleQuestionIDsFullReturnsAll(t *testing.T) {
	input := ids(10, 20, 30, 40, 50)

	for _, mode := range []string{SamplingFull, ""} {
		got, err := SampleQuestionIDs(SampleSpec{Mode: mode, Seed: 7, QuestionIDs: input})
		if err != nil {
			t.Fatalf("mode %q: unexpected error: %v", mode, err)
		}
		if len(got) != len(input) {
			t.Fatalf("mode %q: expected %d items, got %d", mode, len(input), len(got))
		}
		for index := range input {
			if got[index] != input[index] {
				t.Fatalf("mode %q: order changed at %d: want %d got %d", mode, index, input[index], got[index])
			}
		}
	}
}

func TestSampleQuestionIDsCountExact(t *testing.T) {
	input := ids(1, 2, 3, 4, 5, 6, 7, 8, 9, 10)

	got, err := SampleQuestionIDs(SampleSpec{Mode: SamplingCount, Size: 3, Seed: 42, QuestionIDs: input})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("expected exactly 3 items, got %d: %v", len(got), got)
	}

	// 抽出的必须是输入的真子集，且互不重复。
	seen := map[int64]bool{}
	for _, id := range got {
		if seen[id] {
			t.Fatalf("duplicate id %d in sample: %v", id, got)
		}
		seen[id] = true
	}
	for _, id := range got {
		found := false
		for _, candidate := range input {
			if candidate == id {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("sampled id %d is not in the input set", id)
		}
	}
}

func TestSampleQuestionIDsCountAtLeastTotalReturnsAll(t *testing.T) {
	input := ids(1, 2, 3)

	for _, size := range []int{3, 4, 100} {
		got, err := SampleQuestionIDs(SampleSpec{Mode: SamplingCount, Size: size, Seed: 1, QuestionIDs: input})
		if err != nil {
			t.Fatalf("size %d: unexpected error: %v", size, err)
		}
		if len(got) != 3 {
			t.Fatalf("size %d: expected all 3 items, got %d", size, len(got))
		}
	}
}

func TestSampleQuestionIDsRatioRoundsAndFloorsToOne(t *testing.T) {
	input := ids(1, 2, 3, 4, 5, 6, 7, 8, 9, 10)

	cases := []struct {
		ratio float64
		want  int
	}{
		{0.5, 5},
		{0.25, 3}, // 2.5 四舍五入到 3
		{0.15, 2}, // 1.5 四舍五入到 2
		{0.01, 1}, // 0.1 四舍五入到 0，被下限抬到 1
		{1.0, 10}, // 等于 1 即全量
		{1.5, 10}, // 超过 1 也按全量
	}
	for _, tc := range cases {
		got, err := SampleQuestionIDs(SampleSpec{Mode: SamplingRatio, Ratio: tc.ratio, Seed: 3, QuestionIDs: input})
		if err != nil {
			t.Fatalf("ratio %v: unexpected error: %v", tc.ratio, err)
		}
		if len(got) != tc.want {
			t.Errorf("ratio %v: expected %d items, got %d: %v", tc.ratio, tc.want, len(got), got)
		}
	}
}

// ratio <= 0 刻意报错而非静默全量：静默全量会让用户以为自己只评了一小部分，
// 从而信任一个覆盖面完全不同的结论。本测试锁定这个决定。
func TestSampleQuestionIDsRejectsNonPositiveRatio(t *testing.T) {
	input := ids(1, 2, 3)

	for _, ratio := range []float64{0, -0.5, -1} {
		_, err := SampleQuestionIDs(SampleSpec{Mode: SamplingRatio, Ratio: ratio, Seed: 1, QuestionIDs: input})
		if err == nil {
			t.Fatalf("ratio %v: expected an error, got nil (silently treating it as full would mislead the user)", ratio)
		}
		if !strings.Contains(err.Error(), "ratio") {
			t.Errorf("ratio %v: error should mention ratio, got %q", ratio, err.Error())
		}
	}
}

func TestSampleQuestionIDsRejectsNonPositiveSize(t *testing.T) {
	input := ids(1, 2, 3)

	for _, size := range []int{0, -1} {
		_, err := SampleQuestionIDs(SampleSpec{Mode: SamplingCount, Size: size, Seed: 1, QuestionIDs: input})
		if err == nil {
			t.Fatalf("size %d: expected an error, got nil", size)
		}
	}
}

func TestSampleQuestionIDsRejectsUnknownMode(t *testing.T) {
	_, err := SampleQuestionIDs(SampleSpec{Mode: "random-ish", Seed: 1, QuestionIDs: ids(1, 2)})
	if err == nil {
		t.Fatal("expected an error for an unknown sampling mode")
	}
}

func TestSampleQuestionIDsEmptyInputIsNotAnError(t *testing.T) {
	got, err := SampleQuestionIDs(SampleSpec{Mode: SamplingRatio, Ratio: 0.5, Seed: 1, QuestionIDs: nil})
	if err != nil {
		t.Fatalf("empty input should not be an error, got: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("expected empty result, got %v", got)
	}
}

// 确定性是硬要求：同一 runID 必须每次抽到同一批，
// 否则评估运行不可复现，报告与明细会对不上。
func TestSampleQuestionIDsIsDeterministicForSameSeed(t *testing.T) {
	input := ids(1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17, 18, 19, 20)
	spec := SampleSpec{Mode: SamplingCount, Size: 7, Seed: 1234, QuestionIDs: input}

	first, err := SampleQuestionIDs(spec)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// 反复抽样 20 次，每次都必须是同一批、同一顺序。
	for attempt := 0; attempt < 20; attempt++ {
		again, err := SampleQuestionIDs(spec)
		if err != nil {
			t.Fatalf("attempt %d: unexpected error: %v", attempt, err)
		}
		if len(again) != len(first) {
			t.Fatalf("attempt %d: length changed: %d vs %d", attempt, len(again), len(first))
		}
		for index := range first {
			if again[index] != first[index] {
				t.Fatalf("attempt %d: sample changed at %d: want %v got %v", attempt, index, first, again)
			}
		}
	}
}

// 不同 runID 应当抽到不同批次（至少不能永远相同），
// 否则「按比例抽样」就退化成了「永远取前 N 条」。
func TestSampleQuestionIDsVariesBySeed(t *testing.T) {
	input := make([]int64, 0, 100)
	for index := 1; index <= 100; index++ {
		input = append(input, int64(index))
	}

	seen := map[string]bool{}
	for seed := int64(1); seed <= 5; seed++ {
		got, err := SampleQuestionIDs(SampleSpec{Mode: SamplingCount, Size: 10, Seed: seed, QuestionIDs: input})
		if err != nil {
			t.Fatalf("seed %d: unexpected error: %v", seed, err)
		}
		key := ""
		for _, id := range got {
			key += string(rune(id)) + ","
		}
		seen[key] = true
	}
	if len(seen) < 2 {
		t.Fatalf("expected different seeds to yield different samples, got %d distinct sample(s)", len(seen))
	}
}

// 抽样结果必须是输入的严格子集，且保持原始相对顺序。
func TestSampleQuestionIDsKeepsOriginalRelativeOrder(t *testing.T) {
	input := ids(5, 10, 15, 20, 25, 30, 35, 40)

	got, err := SampleQuestionIDs(SampleSpec{Mode: SamplingCount, Size: 5, Seed: 99, QuestionIDs: input})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	for index := 1; index < len(got); index++ {
		if got[index] <= got[index-1] {
			t.Fatalf("sample is not in ascending original order: %v", got)
		}
	}
}
