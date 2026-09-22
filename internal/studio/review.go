package studio

import (
	"fmt"

	"github.com/1420970597/llm/internal/model"
)

// 本文件是人工判断的**契约层**（Issue #160 T16）。
//
// 为什么阻塞项放在这里而不是 internal/model：
// `Blocker` 是**契约 §1.2/§2.8 的响应结构**（带可跳转 Link），属于表示层；
// 而 model 是纯领域规则（不引用 HTTP 路径）。放回 model 会形成
// model ← studio ← model 的循环，也会让「领域规则」依赖「API 路径形态」。
// 判定逻辑（谁冲突、谁有效）仍在 model，这里只负责把它翻译成阻塞项。

// SampleVersionRef 是阻塞项链接所需的最小引用。
type SampleVersionRef struct {
	ProjectID int64 `json:"projectId"`
	SampleID  int64 `json:"sampleId"`
	Version   int   `json:"version"`
}

// Link 返回可直达该内容版本的 API 路径。
//
// 路径在这里生成而不是由调用方拼：T20 的 blocker 必须能点到**具体对象**
// （契约 §2.8 要求每条 blocker 带可跳转对象），而让每个调用点自己拼 URL
// 必然出现拼错 —— 那正是「blocker 点不到东西」的成因。
func (ref SampleVersionRef) Link() string {
	return fmt.Sprintf("/api/v1/projects/%d/samples/s_%d/versions/%d",
		ref.ProjectID, ref.SampleID, ref.Version)
}

// ReviewBlockers 给出「为什么这一版内容还不能进入发布」的阻塞项。
//
// 每条 blocker 都带具体的下一步，而不是一句「还有待审阅」——
// 后者会让用户自己去列表里找是哪几条（T20 的候选门槛直接复用这些 blocker）。
func ReviewBlockers(version SampleVersionRef, projection model.ReviewProjection) []Blocker {
	blockers := []Blocker{}
	switch projection.EffectiveAction {
	case model.EffectivePending:
		message := "尚未判断"
		switch projection.PendingReason {
		case model.PendingReasonEvidenceChanged:
			message = "必需证据集已变化，需要重新判断"
		case model.PendingReasonNewVersion:
			message = "内容产生了新版本，需要对新版本重新判断"
		case model.PendingReasonNoDecision:
			message = "尚未有人判断这一版内容"
		}
		blockers = append(blockers, Blocker{
			Code: "PENDING_REVIEW", Message: message, Link: version.Link(),
		})
	case model.EffectiveConflict:
		blockers = append(blockers, Blocker{
			Code:    "REVIEW_CONFLICT",
			Message: "存在相反的人工判断，需要项目负责人追加协调决定后才能发布",
			Link:    version.Link(),
		})
	case model.EffectiveQuarantined:
		blockers = append(blockers, Blocker{
			Code:    "QUARANTINED",
			Message: "该内容版本已被隔离，不进入发布范围",
			Link:    version.Link(),
		})
	}
	return blockers
}
