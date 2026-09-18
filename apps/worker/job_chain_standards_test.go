package main

import (
	"testing"

	"github.com/1420970597/llm/internal/model"
)

// 回归：入队时写入的 domainIds 若与当前候选全部失配（数据重排后 cursor 过期），
// 任务不能静默 no-op —— 必须回退为全量处理，否则调用方拿到 completed 却没有任何产出。
func TestPickDomainsStaleSelectionFallsBackToAll(t *testing.T) {
	domains := []model.Domain{
		{ID: 2069, Name: "侦察预警体系", Level: 2},
		{ID: 2070, Name: "指挥控制体系", Level: 2},
	}
	// 入队时钉住的是后来变成 level=1 的父领域 ID，与当前 level=2 候选无交集。
	stale := []int64{2067, 2068}

	if got := pickDomains(domains, stale); len(got) != 0 {
		t.Fatalf("pickDomains = %d 条，期望 0（用于触发回退）", len(got))
	}

	// 与 handler 相同的回退语义：失配即全量。
	selected := stale
	targets := pickDomains(domains, selected)
	if len(targets) == 0 {
		targets = domains
	}
	if len(targets) != len(domains) {
		t.Fatalf("回退后 targets = %d 条，期望 %d 条", len(targets), len(domains))
	}

	// 回退后写入 cursor 的必须是本次真正处理的方向，而不是过期 ID。
	effective := domainIDsOf(targets)
	if len(effective) != 2 || effective[0] != 2069 || effective[1] != 2070 {
		t.Fatalf("effective = %v，期望 [2069 2070]", effective)
	}
}

// 未过期时选择集仍必须生效，回退不能把「指定部分方向生成」这个能力吃掉。
func TestPickDomainsHonoursLiveSelection(t *testing.T) {
	domains := []model.Domain{
		{ID: 1, Name: "a", Level: 2},
		{ID: 2, Name: "b", Level: 2},
		{ID: 3, Name: "c", Level: 2},
	}
	got := pickDomains(domains, []int64{2})
	if len(got) != 1 || got[0].ID != 2 {
		t.Fatalf("pickDomains = %+v，期望只保留 ID=2", got)
	}
	// 空选择 = 全选。
	if got := pickDomains(domains, nil); len(got) != 3 {
		t.Fatalf("空选择应全选，实际 %d 条", len(got))
	}
}

// doneDomainIDs 支持断点续跑：已完成的方向不应被重复处理。
func TestDoneDomainIDsRoundTrip(t *testing.T) {
	cursor := map[string]any{"doneDomainIds": []any{float64(11), float64(12)}}
	done := doneDomainIDs(cursor)
	if len(done) != 2 {
		t.Fatalf("done = %d 个，期望 2", len(done))
	}
	if _, ok := done[11]; !ok {
		t.Fatal("ID 11 应记为已完成")
	}
	if ids := domainIDList(done); len(ids) != 2 || ids[0] != 11 || ids[1] != 12 {
		t.Fatalf("domainIDList = %v，期望升序 [11 12]", ids)
	}
}
