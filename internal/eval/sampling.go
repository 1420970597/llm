package eval

import (
	"fmt"
	"math"
	"math/rand"
	"sort"
)

// 本文件由 L9 lane 独占：评估运行的抽样策略。
//
// 契约：docs/plans/eval-and-cleaning-plan.md 第 3.9 节，
// sampling_mode 取值 full / ratio / count（见第 2 节 eval_runs 表定义）。
//
// 全部是纯函数：不查 DB、不发网络请求，因此可以脱离环境单测。

// 抽样模式常量。与 eval_runs.sampling_mode 的取值一一对应。
const (
	SamplingFull  = "full"
	SamplingRatio = "ratio"
	SamplingCount = "count"
)

// SampleSpec 描述一次抽样所需的全部输入。
//
// Seed 必须是稳定的（调用方传 runID）：同一 (Seed, QuestionIDs, 模式, 参数)
// 每次必须抽到同一批，否则评估运行不可复现、报告与明细对不上。
type SampleSpec struct {
	Mode        string
	Ratio       float64
	Size        int
	Seed        int64
	QuestionIDs []int64
}

// SampleQuestionIDs 按抽样模式选出要评估的问题 ID。
//
// 返回的切片保持 QuestionIDs 的原始相对顺序，不随机打乱。
// 理由：抽样解决的是「评哪些」，不是「按什么顺序评」。顺序稳定让报告里的
// itemIndex 可复现，也让两次运行的结果可以直接逐条对比。
func SampleQuestionIDs(spec SampleSpec) ([]int64, error) {
	total := len(spec.QuestionIDs)
	if total == 0 {
		// 没有数据可评不是错误：worker 会据此把运行标记为 0 条并给出说明。
		return []int64{}, nil
	}

	var want int
	switch spec.Mode {
	case SamplingFull, "":
		// 空模式按全量处理：创建运行时的默认值就是 full。
		want = total

	case SamplingRatio:
		if spec.Ratio <= 0 {
			// 刻意不把 <=0 静默当成全量：用户把比例填成 0 或负数时，
			// 若悄悄评了全部数据，用户会以为「只评了一小部分」而信任一个
			// 覆盖面完全不同的结论。宁可报错让他改。
			return nil, fmt.Errorf("sample ratio must be > 0, got %v", spec.Ratio)
		}
		if spec.Ratio >= 1 {
			want = total
			break
		}
		// 四舍五入后至少取 1 条：ratio>0 却抽到 0 条同样是静默失败，
		// 会让运行看起来「已完成」却没有任何分数。
		want = int(math.Round(float64(total) * spec.Ratio))
		if want < 1 {
			want = 1
		}

	case SamplingCount:
		if spec.Size <= 0 {
			return nil, fmt.Errorf("sample size must be > 0, got %d", spec.Size)
		}
		want = spec.Size

	default:
		return nil, fmt.Errorf("unknown sampling mode %q", spec.Mode)
	}

	if want >= total {
		out := make([]int64, total)
		copy(out, spec.QuestionIDs)
		return out, nil
	}

	// 用 Seed 派生的私有随机源，不碰全局 rand：
	// 全局源会被同进程其他调用影响，破坏可复现性。
	// rand.New 在 Go 1.20+ 无需显式 Seed；这里用 Seed 构造以绑定 runID。
	source := rand.New(rand.NewSource(spec.Seed))
	picked := source.Perm(total)[:want]

	// Perm 给出的下标是乱序的，排序后按原顺序取，保证输出顺序稳定。
	sort.Ints(picked)

	out := make([]int64, 0, want)
	for _, index := range picked {
		out = append(out, spec.QuestionIDs[index])
	}
	return out, nil
}
