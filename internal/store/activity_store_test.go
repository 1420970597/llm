package store

import (
	"context"
	"errors"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/1420970597/llm/internal/model"
	"github.com/jackc/pgx/v5/pgxpool"
)

// 本文件验证 Issue #160 T27 的待办聚合、动态分页、阅读水位、评论与搜索（真实 Postgres）。
//
// 五条验收项：
//  1. 待办与搜索**只含可读项目**，撤权后立即消失；
//  2. 动态分页**无重复无遗漏**（多来源合并流的游标正确性）；
//  3. 未读只影响红点：标记全部已读**不改变**任何业务状态（待办数量不变）；
//  4. 评论是修订链：更正保留旧行；提及非成员被拒绝；
//  5. 评论**不是** Decision：发表评论不会改变 review_projections 的有效处置。

type activityFixture struct {
	pool        *pgxpool.Pool
	activity    *ActivityStore
	projects    *ProjectStore
	batches     *BatchStore
	reviews     *ReviewStore
	workspaceID int64
	ownerID     int64
	outsiderID  int64
	projectA    int64
	projectB    int64
	suffix      string
}

func newActivityFixture(t *testing.T) *activityFixture {
	t.Helper()
	dsn := os.Getenv("LLM_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("输入缺失: LLM_TEST_POSTGRES_DSN 未设置，跳过真实 Postgres 集成测试")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect postgres: %v", err)
	}
	t.Cleanup(pool.Close)

	suffix := strings.ReplaceAll(t.Name(), "/", "_")
	fixture := &activityFixture{
		pool: pool, activity: NewActivityStore(pool), projects: NewProjectStore(pool),
		batches: NewBatchStore(pool), reviews: NewReviewStore(pool), suffix: suffix,
	}
	if err := pool.QueryRow(ctx, `
    INSERT INTO workspaces (name, slug) VALUES ($1, $2) RETURNING id`,
		"活动测试工作区 "+suffix, "activity-test-"+suffix).Scan(&fixture.workspaceID); err != nil {
		t.Fatalf("seed workspace: %v", err)
	}
	fixture.ownerID = fixture.seedUser(t, "owner")
	fixture.outsiderID = fixture.seedUser(t, "outsider")
	if _, err := pool.Exec(ctx, `
    INSERT INTO workspace_members (workspace_id, user_id, role, created_by) VALUES ($1, $2, 'member', $2)`,
		fixture.workspaceID, fixture.ownerID); err != nil {
		t.Fatalf("seed workspace member: %v", err)
	}
	fixture.projectA = fixture.seedProject(t, "A", fixture.ownerID)
	fixture.projectB = fixture.seedProject(t, "B", fixture.ownerID)

	t.Cleanup(func() {
		cleanup := context.Background()
		_, _ = pool.Exec(cleanup, `DELETE FROM comments WHERE project_id = ANY($1::bigint[])`,
			[]int64{fixture.projectA, fixture.projectB})
		_, _ = pool.Exec(cleanup, `DELETE FROM activity_reads WHERE workspace_id = $1`, fixture.workspaceID)
		_, _ = pool.Exec(cleanup, `DELETE FROM projects WHERE workspace_id = $1`, fixture.workspaceID)
		_, _ = pool.Exec(cleanup, `DELETE FROM users WHERE email LIKE $1`, "activity-"+suffix+"-%")
		_, _ = pool.Exec(cleanup, `DELETE FROM workspaces WHERE id = $1`, fixture.workspaceID)
	})
	return fixture
}

func (fixture *activityFixture) seedUser(t *testing.T, label string) int64 {
	t.Helper()
	var userID int64
	if err := fixture.pool.QueryRow(context.Background(), `
    INSERT INTO users (email, hashed_password, role) VALUES ($1, 'x', 'user') RETURNING id`,
		"activity-"+fixture.suffix+"-"+label+"@example.test").Scan(&userID); err != nil {
		t.Fatalf("seed user %s: %v", label, err)
	}
	return userID
}

func (fixture *activityFixture) seedProject(t *testing.T, label string, ownerID int64) int64 {
	t.Helper()
	project, err := fixture.projects.CreateProject(context.Background(), fixture.workspaceID, ownerID,
		model.CreateProjectInput{Name: "活动项目 " + label + " " + fixture.suffix, Goal: "g", TargetKind: model.TargetKindSFT})
	if err != nil {
		t.Fatalf("create project %s: %v", label, err)
	}
	return project.ID
}

// seedFailedBatch 造一个 partial_failed 批次（失败恢复待办）。
func (fixture *activityFixture) seedFailedBatch(t *testing.T, projectID int64) int64 {
	t.Helper()
	var batchID int64
	if err := fixture.pool.QueryRow(context.Background(), `
    INSERT INTO batches (project_id, purpose, status, control_state, target_kind, schema_version,
                         generation_config, planned_units, completed_units, failed_units,
                         budget_currency, created_by)
    VALUES ($1, 'pilot', 'partial_failed', 'run', 'sft', 'sft.sample.v1', '{}'::jsonb, 3, 1, 1, 'CNY', $2)
    RETURNING id`, projectID, fixture.ownerID).Scan(&batchID); err != nil {
		t.Fatalf("seed failed batch: %v", err)
	}
	return batchID
}

// seedPendingReview 造一条待判断的内容版本。
func (fixture *activityFixture) seedPendingReview(t *testing.T, projectID int64) int64 {
	t.Helper()
	ctx := context.Background()
	key := "activity-sample-" + fixture.suffix + "-" + strings.ReplaceAll(t.Name(), "/", "_")
	sample, err := fixture.batches.EnsureSample(ctx, projectID, key, model.TargetKindSFT, key, nil)
	if err != nil {
		t.Fatalf("EnsureSample: %v", err)
	}
	_, version, err := fixture.batches.AppendSampleVersion(ctx, AppendSampleVersionInput{
		ProjectID: projectID, SampleKey: sample.SampleKey, TargetKind: model.TargetKindSFT,
		Title: key, Payload: map[string]any{"question": "q", "reasoning": "r", "answer": "a"},
	})
	if err != nil {
		t.Fatalf("AppendSampleVersion: %v", err)
	}
	// 待判断由投影派生：没有 decision 时投影就是 pending（review_store 的 EnsureProjection）。
	if _, err := fixture.reviews.GetProjection(ctx, projectID, version.ID); err != nil {
		t.Fatalf("GetProjection: %v", err)
	}
	return version.ID
}

// TestTodosArePermissionFiltered 覆盖「只含可读项目」与撤权即时生效。
func TestTodosArePermissionFiltered(t *testing.T) {
	fixture := newActivityFixture(t)
	ctx := context.Background()
	fixture.seedFailedBatch(t, fixture.projectA)
	fixture.seedFailedBatch(t, fixture.projectA)
	fixture.seedFailedBatch(t, fixture.projectB)

	todos, err := fixture.activity.LoadTodos(ctx, fixture.ownerID, fixture.workspaceID, 3)
	if err != nil {
		t.Fatalf("LoadTodos: %v", err)
	}
	byKind := map[string]int64{}
	for _, todo := range todos {
		byKind[todo.Kind] += todo.Count
	}
	if byKind[model.TodoFailedRecovery] != 3 {
		t.Fatalf("owner 应看到 3 个失败批次，实际 %d（%+v）", byKind[model.TodoFailedRecovery], todos)
	}
	for _, todo := range todos {
		if todo.Links["page"] == "" {
			t.Fatalf("每条待办必须带可跳转链接：%+v", todo)
		}
	}

	// 非成员看不到任何待办（他不在任何项目里）。
	outsiderTodos, err := fixture.activity.LoadTodos(ctx, fixture.outsiderID, fixture.workspaceID, 3)
	if err != nil {
		t.Fatalf("LoadTodos(outsider): %v", err)
	}
	if len(outsiderTodos) != 0 {
		t.Fatalf("非成员不得看到任何项目的待办，实际 %+v", outsiderTodos)
	}

	// 撤权即时生效：用一个普通成员（reviewer）验证 —— owner 受
	// 「项目至少一名 owner」保护，不能用来测撤权。
	memberID := fixture.seedUser(t, "member")
	if _, err := fixture.pool.Exec(ctx, `
    INSERT INTO project_members (project_id, user_id, role, created_by) VALUES ($1, $2, 'reviewer', $2)`,
		fixture.projectB, memberID); err != nil {
		t.Fatalf("add member: %v", err)
	}
	before, err := fixture.activity.LoadTodos(ctx, memberID, fixture.workspaceID, 3)
	if err != nil {
		t.Fatalf("LoadTodos(member): %v", err)
	}
	if len(before) == 0 {
		t.Fatal("成员应看到自己项目的待办")
	}
	if _, err := fixture.pool.Exec(ctx, `
    DELETE FROM project_members WHERE project_id = $1 AND user_id = $2`, fixture.projectB, memberID); err != nil {
		t.Fatalf("remove member: %v", err)
	}
	after, err := fixture.activity.LoadTodos(ctx, memberID, fixture.workspaceID, 3)
	if err != nil {
		t.Fatalf("LoadTodos(after revoke): %v", err)
	}
	if len(after) != 0 {
		t.Fatalf("撤权后不得再看到该项目待办，实际 %+v", after)
	}
}

// TestActivityPaginationAndWatermark 覆盖多来源合并流的分页正确性与未读语义。
func TestActivityPaginationAndWatermark(t *testing.T) {
	fixture := newActivityFixture(t)
	ctx := context.Background()
	batchID := fixture.seedFailedBatch(t, fixture.projectA)
	// 造 5 条批次事件 + 2 条项目审计（两个来源都有）。
	for index := 0; index < 5; index++ {
		if _, err := fixture.pool.Exec(ctx, `
      INSERT INTO batch_events (batch_id, project_id, event_type, sequence, actor_id, created_at)
      VALUES ($1, $2, $3, $4, $5, NOW() - make_interval(secs => $6))`,
			batchID, fixture.projectA, model.BatchEventStarted, index+1, fixture.ownerID, index); err != nil {
			t.Fatalf("seed batch event: %v", err)
		}
	}
	for index := 0; index < 2; index++ {
		if _, err := fixture.pool.Exec(ctx, `
      INSERT INTO audit_logs (actor, action, resource_type, resource_id, project_id, actor_user_id, created_at)
      VALUES ('user', 'batch_create', 'batch', $1, $2, $3, NOW() - make_interval(secs => $4))`,
			"1", fixture.projectA, fixture.ownerID, index); err != nil {
			t.Fatalf("seed audit: %v", err)
		}
	}

	// 逐页拉取（limit=2），断言无重复、无遗漏、总数为 7。
	seen := map[string]bool{}
	cursor := model.ActivityCursor{}
	pages := 0
	for {
		items, next, err := fixture.activity.LoadActivity(ctx, fixture.ownerID, fixture.workspaceID, cursor, 2)
		if err != nil {
			t.Fatalf("LoadActivity: %v", err)
		}
		for _, item := range items {
			key := item.Source + "#" + itoa64ForTest(item.EventID)
			if seen[key] {
				t.Fatalf("分页出现重复条目 %s（游标排序键不一致）", key)
			}
			seen[key] = true
			if !item.Unread {
				t.Fatalf("首次读取时所有动态都应未读：%+v", item)
			}
			if item.Links["page"] == "" {
				t.Fatalf("动态必须带跳转链接：%+v", item)
			}
		}
		pages++
		if next == "" {
			break
		}
		if pages > 10 {
			t.Fatal("分页未收敛（可能游标没有前进）")
		}
		decoded, err := model.DecodeActivityCursor(next)
		if err != nil {
			t.Fatalf("DecodeActivityCursor: %v", err)
		}
		cursor = decoded
	}
	if len(seen) != 7 {
		t.Fatalf("两个来源共 7 条动态必须全部被翻到，实际 %d 条", len(seen))
	}

	// 未读只影响红点：标记全部已读后，待办里**业务类**待办数量不变。
	todosBefore, err := fixture.activity.LoadTodos(ctx, fixture.ownerID, fixture.workspaceID, 3)
	if err != nil {
		t.Fatalf("LoadTodos: %v", err)
	}
	businessBefore := int64(0)
	for _, todo := range todosBefore {
		if todo.Kind != model.TodoUnreadActivity {
			businessBefore += todo.Count
		}
	}
	if _, err := fixture.activity.MarkAllReadNow(ctx, fixture.ownerID, fixture.workspaceID); err != nil {
		t.Fatalf("MarkAllReadNow: %v", err)
	}
	items, _, err := fixture.activity.LoadActivity(ctx, fixture.ownerID, fixture.workspaceID, model.ActivityCursor{}, 10)
	if err != nil {
		t.Fatalf("LoadActivity(after read): %v", err)
	}
	for _, item := range items {
		if item.Unread {
			t.Fatalf("标记全部已读后不应有未读条目：%+v", item)
		}
	}
	todosAfter, err := fixture.activity.LoadTodos(ctx, fixture.ownerID, fixture.workspaceID, 3)
	if err != nil {
		t.Fatalf("LoadTodos(after read): %v", err)
	}
	businessAfter := int64(0)
	hasUnreadTodo := false
	for _, todo := range todosAfter {
		if todo.Kind == model.TodoUnreadActivity {
			hasUnreadTodo = true
			continue
		}
		businessAfter += todo.Count
	}
	if businessAfter != businessBefore {
		t.Fatalf("「全部已读」不得改变业务待办：%d -> %d", businessBefore, businessAfter)
	}
	if hasUnreadTodo {
		t.Fatal("标记全部已读后不应再有「未读动态」待办")
	}
}

// TestCommentsAreRevisionChainAndNotDecisions 覆盖评论的修订链、提及校验与
// 「评论不是 Decision」。
func TestCommentsAreRevisionChainAndNotDecisions(t *testing.T) {
	fixture := newActivityFixture(t)
	ctx := context.Background()
	versionID := fixture.seedPendingReview(t, fixture.projectA)

	// 发表评论。
	first, err := fixture.activity.CreateComment(ctx, CreateCommentInput{
		ProjectID: fixture.projectA, AnchorKind: model.CommentAnchorSampleVersion, AnchorID: versionID,
		Body: "这条推理的第二步跳得太快", AuthorID: fixture.ownerID,
	})
	if err != nil {
		t.Fatalf("CreateComment: %v", err)
	}
	if first.Revision != 1 || !first.Current {
		t.Fatalf("首条评论应为 revision=1 且 current，实际 %+v", first)
	}

	// 提及非成员被拒绝（T27：提及只通知有权访问的成员）。
	if _, err := fixture.activity.CreateComment(ctx, CreateCommentInput{
		ProjectID: fixture.projectA, AnchorKind: model.CommentAnchorSampleVersion, AnchorID: versionID,
		Body: "@outsider 看一下", Mentions: []int64{fixture.outsiderID}, AuthorID: fixture.ownerID,
	}); err == nil {
		t.Fatal("提及非项目成员必须被拒绝")
	}

	// 更正：产生 revision=2，旧行仍在（superseded_by 指向新行）。
	second, err := fixture.activity.CreateComment(ctx, CreateCommentInput{
		ProjectID: fixture.projectA, AnchorKind: model.CommentAnchorSampleVersion, AnchorID: versionID,
		Body: "更正：第二步缺少约束说明", AuthorID: fixture.ownerID,
	})
	if err != nil {
		t.Fatalf("CreateComment(update): %v", err)
	}
	if second.Revision != 2 || second.SupersedesID == nil || *second.SupersedesID != first.ID {
		t.Fatalf("更正应产生 revision=2 且指向旧行，实际 %+v", second)
	}
	comments, err := fixture.activity.ListComments(ctx, fixture.projectA,
		model.CommentAnchorSampleVersion, versionID, 10)
	if err != nil {
		t.Fatalf("ListComments: %v", err)
	}
	if len(comments) != 2 {
		t.Fatalf("历史修订必须保留（共 2 条），实际 %d", len(comments))
	}
	if comments[0].ID != second.ID || !comments[0].Current {
		t.Fatalf("当前有效评论应排在最前，实际 %+v", comments[0])
	}
	if comments[1].SupersededBy == nil || *comments[1].SupersededBy != second.ID {
		t.Fatalf("旧行必须被标记 superseded_by，实际 %+v", comments[1])
	}

	// 评论**不是** Decision：projection 仍是 pending（评论不能解除发布门槛）。
	projection, err := fixture.reviews.GetProjection(ctx, fixture.projectA, versionID)
	if err != nil {
		t.Fatalf("GetProjection: %v", err)
	}
	if projection.EffectiveAction != model.EffectivePending {
		t.Fatalf("发表评论不得改变有效处置（评论不是判断），实际 %s", projection.EffectiveAction)
	}

	// 锚点跨项目被拒绝。
	if _, err := fixture.activity.CreateComment(ctx, CreateCommentInput{
		ProjectID: fixture.projectB, AnchorKind: model.CommentAnchorSampleVersion, AnchorID: versionID,
		Body: "跨项目锚点", AuthorID: fixture.ownerID,
	}); err == nil {
		t.Fatal("锚点不属于该项目时必须被拒绝")
	}
}

// TestSearchOnlyAccessibleProjects 覆盖搜索的权限过滤。
func TestSearchOnlyAccessibleProjects(t *testing.T) {
	fixture := newActivityFixture(t)
	ctx := context.Background()
	keyword := "冷链-" + fixture.suffix
	if _, err := fixture.pool.Exec(ctx, `
    UPDATE projects SET name = $1 WHERE id = $2`, keyword, fixture.projectA); err != nil {
		t.Fatalf("rename project: %v", err)
	}

	hits, err := fixture.activity.Search(ctx, fixture.ownerID, fixture.workspaceID, "冷链-", 20)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(hits) == 0 {
		t.Fatal("成员应能搜到自己项目的对象")
	}
	for _, hit := range hits {
		if hit.PagePath == "" {
			t.Fatalf("搜索结果必须带可跳转路径：%+v", hit)
		}
	}

	outsiderHits, err := fixture.activity.Search(ctx, fixture.outsiderID, fixture.workspaceID, "冷链-", 20)
	if err != nil {
		t.Fatalf("Search(outsider): %v", err)
	}
	if len(outsiderHits) != 0 {
		t.Fatalf("非成员不得搜到任何项目，实际 %+v", outsiderHits)
	}

	// 空关键字返回空（而不是全量）：列表页与搜索页的语义不同。
	empty, err := fixture.activity.Search(ctx, fixture.ownerID, fixture.workspaceID, "   ", 20)
	if err != nil {
		t.Fatalf("Search(empty): %v", err)
	}
	if len(empty) != 0 {
		t.Fatalf("空关键字必须返回空结果，实际 %d 条", len(empty))
	}
}

// TestCommentNotFoundSentinel 覆盖哨兵错误（API 用它映射 404）。
func TestCommentNotFoundSentinel(t *testing.T) {
	fixture := newActivityFixture(t)
	if _, err := fixture.activity.CommentByID(context.Background(), fixture.projectA, 1<<40); !errors.Is(err, ErrCommentNotFound) {
		t.Fatalf("不存在的评论必须返回 ErrCommentNotFound，实际 %v", err)
	}
}

func itoa64ForTest(value int64) string {
	return strconvFormatInt(value)
}

// strconvFormatInt 是本测试文件用的薄封装（避免为一行引入整个 strconv 的用法差异）。
func strconvFormatInt(value int64) string {
	return strconv.FormatInt(value, 10)
}
