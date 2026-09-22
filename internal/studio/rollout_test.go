package studio

import (
	"strings"
	"testing"

	"github.com/1420970597/llm/internal/store"
)

// 本文件验证 Issue #160 T33 的特性开关判据（纯函数）。
//
// 三条最容易写反、而写反后后果最重的语义：
//
//  1. **零值 = 全部启用**：忘了传 rollout 不能等于「全禁用」；
//  2. **只拦写入**：读取与下载必须始终可用，否则运维不敢用回退开关；
//  3. **未知动作按写入处理**：将来新增的写动作不能默认绕过回退开关。

// TestRolloutZeroValueEnablesEverything 覆盖「零值 = 全部启用」。
func TestRolloutZeroValueEnablesEverything(t *testing.T) {
	var rollout Rollout
	if blocked, reason := rollout.BlockedFor(42); blocked {
		t.Fatalf("零值不得阻止任何项目，实际被挡：%s", reason)
	}
	if rollout.BlocksAction(store.AuthzPublish) != true {
		t.Fatal("BlocksAction 只回答「这个动作是否属于写入」，与开关状态无关")
	}
	state := rollout.State()
	if !state.Enabled || len(state.DisabledProjects) != 0 {
		t.Fatalf("零值状态应为全部启用：%+v", state)
	}
}

// TestRolloutBlocksWritesButNotReads 覆盖回退语义。
func TestRolloutBlocksWritesButNotReads(t *testing.T) {
	rollout := Rollout{DisabledGlobally: true}
	for _, action := range []store.AuthzAction{store.AuthzDesign, store.AuthzRun, store.AuthzReview, store.AuthzPublish} {
		if !rollout.BlocksAction(action) {
			t.Errorf("回退时必须拦下写入动作 %s", action)
		}
	}
	for _, action := range []store.AuthzAction{store.AuthzRead, store.AuthzDownload,
		store.AuthzManageMembers, store.AuthzManageWorkspace} {
		if rollout.BlocksAction(action) {
			t.Errorf("回退时 %s 必须仍然可用（用户要能把已有数据拿走）", action)
		}
	}
	// 未知动作按写入处理：把未知当成读取会让将来新增的写动作默认绕过开关。
	if !rollout.BlocksAction(store.AuthzAction("future_action")) {
		t.Fatal("未知动作必须按写入处理")
	}
}

// TestRolloutProjectLevelScoping 覆盖项目级回退名单。
func TestRolloutProjectLevelScoping(t *testing.T) {
	rollout := ParseRollout(true, []int64{7, 9}, map[int64]string{7: "发布积压"})
	if blocked, _ := rollout.BlockedFor(7); !blocked {
		t.Fatal("名单内项目必须被挡")
	}
	if blocked, _ := rollout.BlockedFor(8); blocked {
		t.Fatal("名单外项目不得被挡")
	}
	_, reason := rollout.BlockedFor(7)
	if reason == "" || !strings.Contains(reason, "发布积压") {
		t.Fatalf("拒绝原因必须带运维写的理由，实际 %q", reason)
	}
	// 只有 ID 没有原因时也要被挡（空原因不等于没被挡）。
	blocked, idOnlyReason := rollout.BlockedFor(9)
	if !blocked || idOnlyReason == "" {
		t.Fatalf("无原因的项目也要被挡并给出可读文案，实际 blocked=%v reason=%q", blocked, idOnlyReason)
	}
	if !strings.Contains(idOnlyReason, "STUDIO_DISABLED_PROJECT_IDS") {
		t.Fatalf("拒绝文案必须点明是哪个开关导致的，实际 %q", idOnlyReason)
	}
}

// TestParseProjectIDListSupportsReasons 覆盖配置解析的两种写法。
func TestParseProjectIDListSupportsReasons(t *testing.T) {
	ids, reasons := ParseProjectIDList(" 12:发布积压 , 13 ,bad, 14:成本失控 ")
	if len(ids) != 3 {
		t.Fatalf("应解析出 3 个 ID（bad 被丢弃），实际 %v", ids)
	}
	if reasons[12] != "发布积压" || reasons[13] != "" || reasons[14] != "成本失控" {
		t.Fatalf("原因解析不符合预期：%v", reasons)
	}
	// 非法项被丢弃而不是变成 0（0 会在 BlockedFor 里永远不命中，
	// 从而让一条写错的配置静默失效）。
	for _, id := range ids {
		if id <= 0 {
			t.Fatalf("不得产出非正 ID：%v", ids)
		}
	}
}

// TestRolloutStateIsStableAndReadable 覆盖状态输出（运维要看的东西）。
func TestRolloutStateIsStableAndReadable(t *testing.T) {
	rollout := ParseRollout(false, []int64{30, 10, 20}, nil)
	state := rollout.State()
	if state.Enabled {
		t.Fatal("总开关关闭时 Enabled 必须为 false")
	}
	want := []int64{10, 20, 30}
	if len(state.DisabledProjects) != len(want) {
		t.Fatalf("回退名单数量不符：%+v", state.DisabledProjects)
	}
	for index, id := range want {
		if state.DisabledProjects[index].ProjectID != id {
			t.Fatalf("回退名单必须按 ID 升序（输出稳定）：%+v", state.DisabledProjects)
		}
	}
	if len(state.Notes) == 0 {
		t.Fatal("状态必须带说明（否则运维看不懂这些字段意味着什么）")
	}
}
