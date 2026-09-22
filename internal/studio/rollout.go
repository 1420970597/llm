package studio

import (
	"sort"
	"strconv"
	"strings"

	"github.com/1420970597/llm/internal/store"
)

// 本文件实现 Atelier 的**特性开关与项目级灰度**（Issue #160 T33）。
//
// 契约：#160 T33 的原文要求「配置 `studio_enabled` 与项目级 rollout」，
// 以及验收项「回退开关停止新 Studio 命令，已提交作业由兼容 worker 完成或暂停，
// 已发布文件保持可下载」。
//
// 三条设计取舍：
//
//  1. **零值 = 全部启用**。`Rollout{}` 不阻止任何请求。反过来的设计
//    （零值 = 全禁用）会让「忘了往 Service 里传 rollout」在生产上以
//    功能整体不可用的形式爆炸，而那种故障最难归因：一切测试都过，
//    线上什么都做不了。
//
//  2. **只拦写入，不拦读取与下载**。回退时用户必须还能把已经拿到的
//     数据取走。一个「关掉后连自己已发布的文件都下载不了」的开关，
//     会让运维在需要回退时不敢用 —— 那就等于没有开关。
//
//  3. **拒绝用 503 + 明确文案，而不是 404**。这不是「资源不存在」，
//     而是「服务被运维暂停」。用 404 会让用户以为数据丢了并开始
//     重复创建，而 503 带着 `retryable` 语义，客户端知道等一会儿再试。
//
// 放在 `internal/studio` 而不是 `internal/config`：Service 要用它做判定，
// 而 Service 不应该依赖配置加载（配置是进程启动期的事，判定是请求期的事）。

// Rollout 描述 Atelier 新能力的可用范围。
type Rollout struct {
	// DisabledGlobally 为 true 时，所有项目的新 Studio 命令都被拒绝。
	DisabledGlobally bool
	// DisabledProjects 是项目级回退名单：projectID → 原因（可为空串）。
	// 用原因而不是 bool，是为了让「为什么这个项目被挡」在状态接口里可读；
	// 而空串仍然表示「被挡但没写原因」，不会被误读为「没被挡」。
	DisabledProjects map[int64]string
}

// BlockedFor 判断某个项目的新 Studio 命令是否被挡住。
//
// 返回 (是否挡住, 原因)。原因会写进 503 的文案与状态接口。
func (rollout Rollout) BlockedFor(projectID int64) (bool, string) {
	if rollout.DisabledGlobally {
		return true, "Atelier 已被运维开关（STUDIO_ENABLED=false）暂停：当前不接受新的设计/运行/判断/发布命令，" +
			"读取与已发布文件下载不受影响"
	}
	if rollout.DisabledProjects != nil {
		if reason, found := rollout.DisabledProjects[projectID]; found {
			message := "该项目被运维回退名单（STUDIO_DISABLED_PROJECT_IDS）暂停：当前不接受新的 Studio 命令"
			if strings.TrimSpace(reason) != "" {
				message += "（原因：" + reason + "）"
			}
			return true, message
		}
	}
	return false, ""
}

// BlocksAction 判断某个动作是否属于「新 Studio 命令」。
//
// 判定放在服务层而不是 handler：一个 handler 忘了调用开关检查，
// 就会在回退后仍能写入 —— 而那种缺口只在真正回退时才发现。
func (rollout Rollout) BlocksAction(action store.AuthzAction) bool {
	switch action {
	case store.AuthzRead, store.AuthzDownload, store.AuthzManageMembers, store.AuthzManageWorkspace:
		// 读取、下载、成员/工作区管理不受影响：
		// 前两者是「把已有数据拿走」，后两者是「恢复现场」的手段。
		return false
	case store.AuthzDesign, store.AuthzRun, store.AuthzReview, store.AuthzPublish:
		return true
	default:
		// 未知动作按**写入**处理：把未知当成读取会让一个将来新增的写动作
		// 默认绕过回退开关，而那种缺口的方向恰好是最危险的。
		return true
	}
}

// RolloutState 是给运维看的开关状态（`GET /api/v1/studio/rollout`）。
type RolloutState struct {
	Enabled          bool              `json:"enabled"`
	DisabledProjects []RolloutProject  `json:"disabledProjects"`
	Notes            []string          `json:"notes"`
	Extra            map[string]string `json:"extra,omitempty"`
}

// RolloutProject 是回退名单里的一项。
type RolloutProject struct {
	ProjectID int64  `json:"projectId"`
	Reason    string `json:"reason"`
}

// State 把开关状态整理成可读模型（项目 ID 排序，保证输出稳定）。
func (rollout Rollout) State() RolloutState {
	state := RolloutState{Enabled: !rollout.DisabledGlobally, DisabledProjects: []RolloutProject{}}
	ids := make([]int64, 0, len(rollout.DisabledProjects))
	for id := range rollout.DisabledProjects {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	for _, id := range ids {
		state.DisabledProjects = append(state.DisabledProjects, RolloutProject{ProjectID: id, Reason: rollout.DisabledProjects[id]})
	}
	if rollout.DisabledGlobally {
		state.Notes = append(state.Notes,
			"总开关关闭：新 Studio 命令被拒绝，读取与已发布文件下载仍可用；"+
				"已提交的作业由 worker 侧开关决定继续完成还是暂停")
	}
	if len(state.DisabledProjects) > 0 {
		state.Notes = append(state.Notes,
			"项目级回退名单生效：名单内项目的新命令被拒绝，其它项目不受影响")
	}
	if state.Enabled && len(state.DisabledProjects) == 0 {
		state.Notes = append(state.Notes, "全部启用")
	}
	return state
}

// ParseRollout 从配置原语构造 Rollout。
//
// reasons 允许为空：`STUDIO_DISABLED_PROJECT_IDS=12,13` 这种只有 ID 的写法
// 会产生「被挡但没写原因」的条目，状态接口会如实显示空原因。
func ParseRollout(enabled bool, disabledProjectIDs []int64, reasons map[int64]string) Rollout {
	rollout := Rollout{DisabledGlobally: !enabled}
	if len(disabledProjectIDs) > 0 {
		rollout.DisabledProjects = make(map[int64]string, len(disabledProjectIDs))
		for _, id := range disabledProjectIDs {
			if id <= 0 {
				continue
			}
			rollout.DisabledProjects[id] = reasons[id]
		}
	}
	return rollout
}

// ParseProjectIDList 解析「id:原因,id:原因」或纯 ID 列表。
//
// 支持原因是为了让回退有据可查（`12:release 积压,13:成本失控`）：
// 只记 ID 的回退名单在一周后没人记得为什么被关。
func ParseProjectIDList(raw string) ([]int64, map[int64]string) {
	ids := []int64{}
	reasons := map[int64]string{}
	for _, part := range strings.Split(raw, ",") {
		trimmed := strings.TrimSpace(part)
		if trimmed == "" {
			continue
		}
		idText := trimmed
		reason := ""
		if index := strings.Index(trimmed, ":"); index >= 0 {
			idText = strings.TrimSpace(trimmed[:index])
			reason = strings.TrimSpace(trimmed[index+1:])
		}
		parsed, err := strconv.ParseInt(idText, 10, 64)
		if err != nil || parsed <= 0 {
			continue
		}
		ids = append(ids, parsed)
		if reason != "" {
			reasons[parsed] = reason
		}
	}
	return ids, reasons
}
