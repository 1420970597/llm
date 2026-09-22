package store

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/1420970597/llm/internal/model"
	"github.com/jackc/pgx/v5/pgxpool"
)

// 本文件验证 Issue #160 T17 的选择快照与审阅筛选。
//
// 必须连真实 Postgres：核心断言是「范围内不被静默缩小」「过期即不可用」
// 「跨项目读不到」—— 都是数据库约束与作用域语义。

type selectionFixture struct {
	pool       *pgxpool.Pool
	projectID  int64
	userID     int64
	versions   []model.SampleVersion
	selections *SelectionStore
	batches    *BatchStore
	reviews    *ReviewStore
}

// newSelectionFixture 建项目 + 3 个样本版本。
func newSelectionFixture(t *testing.T) selectionFixture {
	t.Helper()
	base := newBatchFixture(t)
	ctx := context.Background()
	batches := NewBatchStore(base.pool)

	versions := []model.SampleVersion{}
	for index := 1; index <= 3; index++ {
		key := "selection-item-" + strings.ReplaceAll(t.Name(), "/", "_") + "-" + fmt.Sprint(index)
		sample, err := batches.EnsureSample(ctx, base.projectID, key, model.TargetKindSFT, key, nil)
		if err != nil {
			t.Fatalf("EnsureSample: %v", err)
		}
		_, version, err := batches.AppendSampleVersion(ctx, AppendSampleVersionInput{
			ProjectID: base.projectID, SampleKey: sample.SampleKey,
			TargetKind: model.TargetKindSFT, Title: key,
			Payload: map[string]any{"question": "q" + fmt.Sprint(index), "reasoning": "r", "answer": "a"},
		})
		if err != nil {
			t.Fatalf("AppendSampleVersion: %v", err)
		}
		versions = append(versions, version)
	}

	return selectionFixture{
		pool: base.pool, projectID: base.projectID, userID: base.editorID,
		versions: versions, selections: NewSelectionStore(base.pool),
		batches: batches, reviews: NewReviewStore(base.pool),
	}
}

func (fixture selectionFixture) versionIDs() []int64 {
	ids := make([]int64, 0, len(fixture.versions))
	for _, version := range fixture.versions {
		ids = append(ids, version.ID)
	}
	return ids
}

// TestSelectionSnapshotFreezesExactRange 覆盖「范围被准确冻结」。
func TestSelectionSnapshotFreezesExactRange(t *testing.T) {
	fixture := newSelectionFixture(t)
	ctx := context.Background()

	snapshot, err := fixture.selections.Create(ctx, CreateSelectionSnapshotInput{
		ProjectID: fixture.projectID, Purpose: "release", CreatedBy: &fixture.userID,
		SampleVersionIDs: fixture.versionIDs(), Filter: []byte(`{"source":"explicit"}`),
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if snapshot.ItemCount != 3 {
		t.Fatalf("应冻结 3 条，实际 %d", snapshot.ItemCount)
	}
	if snapshot.ExpiresAt == nil {
		t.Fatal("快照必须有有效期（否则「上周选的」能直接用于发布）")
	}

	loaded, items, err := fixture.selections.Get(ctx, fixture.projectID, snapshot.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if len(items) != 3 || loaded.ItemCount != 3 {
		t.Fatalf("读回应恰好 3 条，实际 %d（记录 %d）", len(items), loaded.ItemCount)
	}
	// 明细必须与传入集合**完全一致**（顺序排序后比较）。
	for index, versionID := range fixture.versionIDs() {
		if items[index] != versionID {
			t.Fatalf("第 %d 条应为 %d，实际 %d", index+1, versionID, items[index])
		}
	}
}

// TestSelectionSnapshotRejectsIncompleteRange 覆盖「范围不得静默缩小」。
func TestSelectionSnapshotRejectsIncompleteRange(t *testing.T) {
	fixture := newSelectionFixture(t)
	ctx := context.Background()

	// 含一个不存在的版本 → 拒绝（否则用户以为选了 4 条、实际只有 3 条）。
	_, err := fixture.selections.Create(ctx, CreateSelectionSnapshotInput{
		ProjectID: fixture.projectID, Purpose: "release", CreatedBy: &fixture.userID,
		SampleVersionIDs: append(fixture.versionIDs(), 1<<62),
	})
	if !IsStoreValidationError(err) {
		t.Fatalf("含不存在版本必须被拒绝，实际 %v", err)
	}
	// 空范围 → 拒绝。
	if _, err := fixture.selections.Create(ctx, CreateSelectionSnapshotInput{
		ProjectID: fixture.projectID, Purpose: "release", CreatedBy: &fixture.userID,
	}); !IsStoreValidationError(err) {
		t.Fatalf("空范围必须被拒绝，实际 %v", err)
	}
	// 跨项目 → 拒绝。
	otherProject := fixture.projectID + 1_000_000
	if _, err := fixture.selections.Create(ctx, CreateSelectionSnapshotInput{
		ProjectID: otherProject, Purpose: "release", CreatedBy: &fixture.userID,
		SampleVersionIDs: fixture.versionIDs(),
	}); !IsStoreValidationError(err) {
		t.Fatalf("跨项目必须被拒绝，实际 %v", err)
	}
	// 用途非法 → 拒绝。
	if _, err := fixture.selections.Create(ctx, CreateSelectionSnapshotInput{
		ProjectID: fixture.projectID, Purpose: "whatever", CreatedBy: &fixture.userID,
		SampleVersionIDs: fixture.versionIDs(),
	}); !IsStoreValidationError(err) {
		t.Fatalf("非法用途必须被拒绝，实际 %v", err)
	}
}

// TestSelectionSnapshotIsProjectScopedAndExpires 覆盖「重新鉴权」与「过期即不可用」。
//
// T17 验收项：「少量 selection 参数也需重新鉴权」——
// 快照 ID 可以出现在 URL 里，因此它必须是一次真实的授权检查点。
func TestSelectionSnapshotIsProjectScopedAndExpires(t *testing.T) {
	fixture := newSelectionFixture(t)
	ctx := context.Background()

	snapshot, err := fixture.selections.Create(ctx, CreateSelectionSnapshotInput{
		ProjectID: fixture.projectID, Purpose: "release", CreatedBy: &fixture.userID,
		SampleVersionIDs: fixture.versionIDs(),
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	// 另一个项目（模拟越权）读不到它。
	if _, _, err := fixture.selections.Get(ctx, fixture.projectID+1_000_000, snapshot.ID); !errors.Is(err, ErrSelectionSnapshotNotFound) {
		t.Fatalf("跨项目读取必须返回 NotFound（不得确认它存在），实际 %v", err)
	}
	// 不存在的 ID。
	if _, _, err := fixture.selections.Get(ctx, fixture.projectID, 1<<62); !errors.Is(err, ErrSelectionSnapshotNotFound) {
		t.Fatalf("不存在的快照必须返回 NotFound，实际 %v", err)
	}

	// 过期：直接把 expires_at 推到过去。
	if _, err := fixture.pool.Exec(ctx, `
    UPDATE sample_selection_snapshots SET expires_at = NOW() - INTERVAL '1 minute' WHERE id = $1`,
		snapshot.ID); err != nil {
		t.Fatalf("expire snapshot: %v", err)
	}
	if _, _, err := fixture.selections.Get(ctx, fixture.projectID, snapshot.ID); !errors.Is(err, ErrSelectionSnapshotNotFound) {
		t.Fatalf("过期快照必须不可用（否则会基于陈旧范围发布），实际 %v", err)
	}
}

// TestListSampleVersionIDsByFilterResolvesServerSide 覆盖
// 「前端只传条件、ID 由服务端解析」与上限。
func TestListSampleVersionIDsByFilterResolvesServerSide(t *testing.T) {
	fixture := newSelectionFixture(t)
	ctx := context.Background()

	// 全部（未判断 → pending）。
	all, err := fixture.batches.ListSampleVersionIDsByFilter(ctx, SampleVersionFilter{
		ProjectID: fixture.projectID,
	}, 100)
	if err != nil {
		t.Fatalf("ListSampleVersionIDsByFilter: %v", err)
	}
	if len(all) != 3 {
		t.Fatalf("应解析出 3 条，实际 %d", len(all))
	}

	// 按审阅状态：把一个版本接纳后，pending 应剩 2 条、accepted 1 条。
	projection, err := fixture.reviews.GetProjection(ctx, fixture.projectID, fixture.versions[0].ID)
	if err != nil {
		t.Fatalf("GetProjection: %v", err)
	}
	if _, err := fixture.reviews.SubmitDecision(ctx, fixture.projectID, fixture.userID,
		model.SubmitDecisionInput{
			SampleVersionID: fixture.versions[0].ID, EvidenceRevision: projection.EvidenceRevision,
			ReviewerRevision: 1, Action: model.DecisionAccept, Reason: "推理链完整",
		}); err != nil {
		t.Fatalf("SubmitDecision: %v", err)
	}

	pending, err := fixture.batches.ListSampleVersionIDsByFilter(ctx, SampleVersionFilter{
		ProjectID: fixture.projectID, ReviewStatus: model.EffectivePending,
	}, 100)
	if err != nil {
		t.Fatalf("pending 筛选: %v", err)
	}
	if len(pending) != 2 {
		t.Fatalf("pending 应剩 2 条，实际 %d（已接纳的不该出现在待审阅范围里）", len(pending))
	}
	accepted, err := fixture.batches.ListSampleVersionIDsByFilter(ctx, SampleVersionFilter{
		ProjectID: fixture.projectID, ReviewStatus: model.EffectiveAccepted,
	}, 100)
	if err != nil {
		t.Fatalf("accepted 筛选: %v", err)
	}
	if len(accepted) != 1 || accepted[0] != fixture.versions[0].ID {
		t.Fatalf("accepted 应恰好是刚被接纳的那一条，实际 %v", accepted)
	}

	// 上限命中必须报错而不是截断（截断会让用户以为范围就是这些）。
	if _, err := fixture.batches.ListSampleVersionIDsByFilter(ctx, SampleVersionFilter{
		ProjectID: fixture.projectID,
	}, 2); !IsStoreValidationError(err) {
		t.Fatalf("超过上限必须报错，实际 %v", err)
	}

	// 跨项目解析出 0 条（作用域生效）。
	other, err := fixture.batches.ListSampleVersionIDsByFilter(ctx, SampleVersionFilter{
		ProjectID: fixture.projectID + 1_000_000,
	}, 100)
	if err != nil {
		t.Fatalf("跨项目筛选: %v", err)
	}
	if len(other) != 0 {
		t.Fatalf("跨项目不得解析出任何版本，实际 %d", len(other))
	}
}

// TestListSamplesIncludesReviewProjection 覆盖「列表带审阅投影」。
//
// 列表必须一次带回有效处置：逐条查会变成 N+1 次请求，而审阅队列
// 正是「一次看一屏」的场景。
func TestListSamplesIncludesReviewProjection(t *testing.T) {
	fixture := newSelectionFixture(t)
	ctx := context.Background()

	// 未判断 → 默认 pending（LEFT JOIN 没有投影行时按 pending 处理）。
	items, err := fixture.batches.ListSamples(ctx, SampleListQuery{ProjectID: fixture.projectID, Limit: 10})
	if err != nil {
		t.Fatalf("ListSamples: %v", err)
	}
	if len(items) != 3 {
		t.Fatalf("应有 3 条，实际 %d", len(items))
	}
	for _, item := range items {
		if item.ReviewStatus != model.EffectivePending {
			t.Fatalf("未判断的内容必须显示为 pending，实际 %q（否则会从待审阅队列里消失）", item.ReviewStatus)
		}
	}

	// 接纳一条后，只有它变成 accepted。
	projection, _ := fixture.reviews.GetProjection(ctx, fixture.projectID, fixture.versions[0].ID)
	if _, err := fixture.reviews.SubmitDecision(ctx, fixture.projectID, fixture.userID,
		model.SubmitDecisionInput{
			SampleVersionID: fixture.versions[0].ID, EvidenceRevision: projection.EvidenceRevision,
			ReviewerRevision: 1, Action: model.DecisionAccept, Reason: "理由",
		}); err != nil {
		t.Fatalf("SubmitDecision: %v", err)
	}

	unreviewed, err := fixture.batches.ListSamples(ctx, SampleListQuery{
		ProjectID: fixture.projectID, UnreviewedOnly: true, Limit: 10,
	})
	if err != nil {
		t.Fatalf("ListSamples(unreviewed): %v", err)
	}
	if len(unreviewed) != 2 {
		t.Fatalf("只看未审阅时应剩 2 条，实际 %d", len(unreviewed))
	}
	for _, item := range unreviewed {
		if item.ReviewStatus == model.EffectiveAccepted {
			t.Fatal("已接纳的内容不得出现在未审阅列表里")
		}
	}

	accepted, err := fixture.batches.ListSamples(ctx, SampleListQuery{
		ProjectID: fixture.projectID, ReviewStatus: model.EffectiveAccepted, Limit: 10,
	})
	if err != nil {
		t.Fatalf("ListSamples(accepted): %v", err)
	}
	if len(accepted) != 1 || accepted[0].ID != fixture.versions[0].SampleID {
		t.Fatalf("按 accepted 筛选应恰好 1 条，实际 %+v", accepted)
	}
	if accepted[0].AggregateReviewRevision == 0 {
		t.Fatal("列表必须带聚合序号（发布候选冻结时做竞争检测）")
	}
}

var _ = os.Getpid
