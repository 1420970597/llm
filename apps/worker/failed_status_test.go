package main

import (
	"testing"
)

// TestFailedStatusForJobIsRecognizedByProgressAPI 锁定 issue #140 的修复。
//
// 背景：worker 的注册表路径此前写 `job.Type + "_failed"`，而注册表里的 job 类型
// 都带点号（export.generate / sft.generate / eval.run ...），于是写出
// `export.generate_failed` 这类状态串；而进度接口
// （internal/store/dataset_store.go 的 rankByStatus / failedStageByStatus /
// completionByStatus）只认识**下划线形态**。
//
// 后果（实测 dataset 161，status=export.generate_failed）：
//
//	completion=0  currentStage=domains
//	domains in_progress(40)  questions pending ... export pending   ← 无任何 failed
//
// 用户看到「进行中 0%」，既不知道失败了，也不知道卡在哪一步。
//
// 本测试断言：每个注册表 job 类型映射出的失败状态，都必须是
// **进度接口认识的下划线形态**（不含点号）。这是「状态串口径一致」的最小充分条件。
func TestFailedStatusForJobIsRecognizedByProgressAPI(t *testing.T) {
	// 与 apps/worker 里 RegisterJobHandler 注册的 job 类型保持一致。
	registered := []string{
		"directions.generate",
		"questions.generate",
		"chain-standards.generate",
		"grpo.generate",
		"sft.generate",
		"export.generate",
		"eval.run",
		"cleaning.run",
	}

	for _, jobType := range registered {
		got := failedStatusForJob(jobType)
		if got == "" {
			t.Errorf("job 类型 %q 没有映射到失败状态", jobType)
			continue
		}
		// 点号是问题的根源：进度接口的映射表全是下划线形态。
		for _, ch := range got {
			if ch == '.' {
				t.Errorf("job 类型 %q 映射出 %q —— 含点号的状态串进度接口不认识，"+
					"会让失败任务显示成「进行中 0%%、无阶段失败」（issue #140）", jobType, got)
				break
			}
		}
		if got == jobType+"_failed" {
			t.Errorf("job 类型 %q 仍在使用 `job.Type + \"_failed\"` 的旧行为（得到 %q）", jobType, got)
		}
	}

	// 未知类型必须仍可观测（回退到旧形态），不能静默丢状态。
	if got := failedStatusForJob("some.unknown.job"); got != "some.unknown.job_failed" {
		t.Errorf("未知 job 类型应回退到 `<type>_failed` 以便排查，实际 %q", got)
	}
}

// TestFailedStatusNamesAreDistinct 断言不同阶段不会映射到同一个状态串。
//
// 若两个阶段映射到同一状态，进度接口的 failedStageByStatus 无法区分失败发生在哪一步，
// 用户看到的失败位置会是错的。
func TestFailedStatusNamesAreDistinct(t *testing.T) {
	registered := []string{
		"directions.generate",
		"questions.generate",
		"chain-standards.generate",
		"grpo.generate",
		"sft.generate",
		"export.generate",
		"eval.run",
		"cleaning.run",
	}
	seen := map[string]string{}
	for _, jobType := range registered {
		status := failedStatusForJob(jobType)
		if prev, dup := seen[status]; dup {
			t.Errorf("job 类型 %q 与 %q 映射到同一个失败状态 %q —— 无法区分失败发生在哪一步",
				jobType, prev, status)
		}
		seen[status] = jobType
	}
}
