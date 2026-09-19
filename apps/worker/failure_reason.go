package main

import (
	"context"
	"errors"
	"fmt"
	"log"

	"github.com/1420970597/llm/internal/store"
)

// failureReason 把内部错误翻译成**给用户看的中文原因**（issue #83）。
//
// 背景：此前失败路径只调用 datasets.UpdateStatus(ctx, id, "<stage>_failed")，
// 失败原因只进 worker 日志。前端只能拿到状态字符串，于是界面只能显示
// 「系统同步中」「请排查失败原因」——用户既不知原因也不知去哪修。
//
// 翻译规则（刻意保守）：
//   - 能明确归因的（存储配置缺失）给出**可操作**提示：说明原因 + 去哪配；
//   - 其余情况给一句通用但**不谎报**的说明，并保留错误摘要（截断），
//     让用户/运维能在界面上直接看到第一手信息，而不用去翻容器日志。
//
// 不把 error.Error() 原样透出的原因：里面可能是英文技术细节
// （例如 `no rows in result set`），对用户没有指导意义，
// 但完全不透出又会让问题无法自助定位，因此采用「中文结论 + 截断的技术摘要」组合。
func failureReason(err error) string {
	if err == nil {
		return ""
	}

	switch {
	case errors.Is(err, store.ErrNoStorageProfile):
		return "缺少结果存储配置：系统里还没有任何可用的结果存储。" +
			"请到「系统设置 → 结果存储」新增一条并设为可用/默认，或在部署配置中设置 S3_BUCKET 等变量后重启服务，然后重新发起本阶段。"
	case errors.Is(err, store.ErrStorageProfileNotFound):
		return "本任务绑定的结果存储配置不存在或已停用。" +
			"请到「系统设置 → 结果存储」确认配置仍然可用，或为本任务重新选择存储后重试。"
	}

	const maxDetailRunes = 300
	detail := []rune(err.Error())
	if len(detail) > maxDetailRunes {
		detail = append(detail[:maxDetailRunes], '…')
	}
	return fmt.Sprintf("本阶段执行失败：%s", string(detail))
}

// markStageFailed 统一处理失败路径：写失败状态 + 写用户可见原因 + 记日志。
//
// 抽成一个函数而不是在每个 case 里重复，是为了保证**所有**失败路径都把原因落库 ——
// 漏掉任何一条都会重现 issue #83「界面只说不清原因」的症状。
func markStageFailed(ctx context.Context, datasets *store.DatasetStore, datasetID int64, status string, err error) {
	reason := failureReason(err)
	if updateErr := datasets.MarkFailed(ctx, datasetID, status, reason); updateErr != nil {
		// 写原因失败不能掩盖原始失败：两条都记下来。
		log.Printf("worker.mark_failed.dataset_error dataset=%d status=%s reason_err=%v original_err=%v",
			datasetID, status, updateErr, err)
		return
	}
	log.Printf("worker.mark_failed dataset=%d status=%s reason=%q err=%v", datasetID, status, reason, err)
}
