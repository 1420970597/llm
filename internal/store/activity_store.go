package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/1420970597/llm/internal/model"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// 本文件实现「今日工作」「动态」与「评论」（Issue #160 T27）。
//
// 契约：sql/migrations/0033_studio_activity_comments.sql 的文件头（表结构取舍）、
// #160 T27 的原文要求。
//
// 四条实现约定：
//
//  1. **权限过滤在 SQL 里**（`project_id = ANY(可读项目)`）。把不可读项目的
//     待办先读进内存再在 Go 里过滤，会让「撤权项目不出现在今日工作」这条
//     验收项依赖于「记得过滤」——而漏掉过滤的表现是用户看到不属于自己的待办。
//  2. **待办全部由事实派生**，没有一张「待办表」。因此不存在「待办状态与
//     真实状态不一致」的问题（那种不一致在待办表里几乎必然出现）。
//  3. **评论不是 Decision**：本文件不触碰 review_decisions / review_projections /
//     releases，因此「评论不能解除发布门槛」是结构性的。
//  4. **提及在写入时校验成员关系、在读取时再按当前关系过滤**：撤权之后，
//     旧评论里的提及不再对被撤权者可见（T27 验收项「提及不会泄露正文」）。

// ErrCommentNotFound 表示评论不存在（或不属于该项目）。
var ErrCommentNotFound = errors.New("未找到该评论")

// ErrCommentNotAuthor 表示只有作者本人可以更正自己的评论。
var ErrCommentNotAuthor = errors.New("只有评论作者可以更正自己的评论")

// ActivityStore 提供待办、动态、阅读水位与评论的读写。
type ActivityStore struct {
	db       *pgxpool.Pool
	projects *ProjectStore
}

// NewActivityStore 构造活动 store。
func NewActivityStore(db *pgxpool.Pool) *ActivityStore {
	return &ActivityStore{db: db, projects: NewProjectStore(db)}
}

// LoadTodos 汇总当前用户在工作区的待办。
//
// 返回的每条都带具体对象链接（T27：条目必须可行动）。
func (s *ActivityStore) LoadTodos(ctx context.Context, userID, workspaceID int64, sampleLimit int) ([]model.TodoItem, error) {
	if sampleLimit <= 0 || sampleLimit > 20 {
		sampleLimit = 3
	}
	projectIDs, err := s.projects.ProjectIDsForUser(ctx, userID)
	if err != nil {
		return nil, err
	}
	// 只保留属于该工作区的项目：用户可能在多个工作区里都是成员，
	// 而「今日工作」是**工作区作用域**的视图。
	scoped, err := s.filterWorkspaceProjects(ctx, workspaceID, projectIDs)
	if err != nil {
		return nil, err
	}
	todos := []model.TodoItem{}
	if len(scoped) == 0 {
		return todos, nil
	}

	type grouped struct {
		projectID int64
		count     int64
		updatedAt time.Time
	}
	load := func(sql string) ([]grouped, error) {
		rows, err := s.db.Query(ctx, sql, scoped)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		items := []grouped{}
		for rows.Next() {
			var item grouped
			if err := rows.Scan(&item.projectID, &item.count, &item.updatedAt); err != nil {
				return nil, err
			}
			items = append(items, item)
		}
		return items, rows.Err()
	}

	type spec struct {
		kind    string
		sql     string
		summary func(count int64) string
		link    string
	}
	specs := []spec{
		{
			kind: model.TodoPendingReview,
			sql: `SELECT project_id, COUNT(*), MAX(updated_at) FROM review_projections
            WHERE project_id = ANY($1::bigint[]) AND effective_action = '` + model.EffectivePending + `'
            GROUP BY project_id ORDER BY 2 DESC`,
			summary: func(count int64) string { return fmt.Sprintf("%d 条内容等待你判断", count) },
			link:    "review",
		},
		{
			kind: model.TodoPilotComparable,
			sql: `SELECT b.project_id, COUNT(*), MAX(b.updated_at) FROM batches b
            WHERE b.project_id = ANY($1::bigint[]) AND b.purpose = 'pilot' AND b.status = 'completed'
              AND NOT EXISTS (SELECT 1 FROM comparison_adoptions a WHERE a.project_id = b.project_id)
            GROUP BY b.project_id HAVING COUNT(*) >= 2 ORDER BY 2 DESC`,
			summary: func(count int64) string {
				return fmt.Sprintf("%d 个试制批次可以对比（比较后采纳方案）", count)
			},
			link: "compare",
		},
		{
			kind: model.TodoFailedRecovery,
			sql: `SELECT project_id, COUNT(*), MAX(updated_at) FROM batches
            WHERE project_id = ANY($1::bigint[]) AND status IN ('partial_failed', 'failed')
            GROUP BY project_id ORDER BY 2 DESC`,
			summary: func(count int64) string { return fmt.Sprintf("%d 个批次有失败项待恢复", count) },
			link:    "runs",
		},
		{
			kind: model.TodoReleaseBlocked,
			sql: `SELECT project_id, COUNT(*), MAX(updated_at) FROM releases
            WHERE project_id = ANY($1::bigint[]) AND status = '` + model.ReleaseStatusBlocked + `'
            GROUP BY project_id ORDER BY 2 DESC`,
			summary: func(count int64) string { return fmt.Sprintf("%d 个发布候选被门槛挡住", count) },
			link:    "releases",
		},
	}
	for _, item := range specs {
		groups, err := load(item.sql)
		if err != nil {
			return nil, err
		}
		for _, group := range groups {
			todos = append(todos, model.TodoItem{
				Kind: item.kind, ProjectID: group.projectID, Count: group.count,
				Summary: item.summary(group.count), UpdatedAt: group.updatedAt,
				Links: model.Links{"page": projectPageLink(group.projectID, item.link)},
			})
		}
	}

	// 未读动态：它是**唯一**与「已读水位」相关的待办，且只表达「有更新」。
	watermark, err := s.GetReadWatermark(ctx, userID, workspaceID)
	if err != nil {
		return nil, err
	}
	unread, err := s.countUnreadActivity(ctx, scoped, watermark)
	if err != nil {
		return nil, err
	}
	if unread > 0 {
		todos = append(todos, model.TodoItem{
			Kind: model.TodoUnreadActivity, ProjectID: 0, Count: unread,
			Summary: fmt.Sprintf("%d 条动态尚未查看（未读不等于已处理）", unread),
			Links:   model.Links{"page": "/activity"},
		})
	}

	// 排序：数量多的优先，便于用户先处理大头。
	// 用插入排序保持稳定（同数量时按现有顺序），避免每次刷新顺序跳动。
	for index := 1; index < len(todos); index++ {
		current := todos[index]
		position := index - 1
		for position >= 0 && todos[position].Count < current.Count {
			todos[position+1] = todos[position]
			position--
		}
		todos[position+1] = current
	}
	return todos, nil
}

// filterWorkspaceProjects 只保留属于该工作区的项目。
func (s *ActivityStore) filterWorkspaceProjects(ctx context.Context, workspaceID int64, projectIDs []int64) ([]int64, error) {
	if len(projectIDs) == 0 {
		return nil, nil
	}
	rows, err := s.db.Query(ctx, `
    SELECT id FROM projects WHERE workspace_id = $1 AND id = ANY($2::bigint[])`,
		workspaceID, projectIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	scoped := []int64{}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		scoped = append(scoped, id)
	}
	return scoped, rows.Err()
}

// LoadActivity 读取动态（keyset 分页，只含可读项目）。
func (s *ActivityStore) LoadActivity(ctx context.Context, userID, workspaceID int64,
	cursor model.ActivityCursor, limit int) ([]model.ActivityItem, string, error) {
	if limit <= 0 || limit > 100 {
		limit = 30
	}
	projectIDs, err := s.projects.ProjectIDsForUser(ctx, userID)
	if err != nil {
		return nil, "", err
	}
	scoped, err := s.filterWorkspaceProjects(ctx, workspaceID, projectIDs)
	if err != nil {
		return nil, "", err
	}
	watermark, err := s.GetReadWatermark(ctx, userID, workspaceID)
	if err != nil {
		return nil, "", err
	}
	if len(scoped) == 0 {
		return []model.ActivityItem{}, "", nil
	}

	// 合并两个来源。批次的逐单元 BatchPartialFailed 先按批次聚合，避免一个
	// 批次的 N 个失败单元在动态和未读数里被放大成 N 条通知。
	// `source_rank` 参与排序与游标比较（见 model.ActivityCursor）。
	rows, err := s.db.Query(ctx, `
	WITH batch_failures AS (
	  SELECT '`+model.ActivitySourceBatch+`'::text AS source,
	         MIN(e.id) AS event_id, e.project_id, e.event_type AS kind,
	         (array_agg(e.actor_id ORDER BY e.created_at DESC))[1] AS actor_id,
	         e.batch_id AS object_id,
	         (array_agg(e.detail::text ORDER BY e.created_at DESC))[1] AS detail,
	         MAX(e.created_at) AS created_at, 0 AS source_rank,
	         COUNT(*)::bigint AS aggregate_count,
	         b.planned_units::bigint AS aggregate_total,
	         ('batch-failure:' || e.batch_id::text) AS group_key
	  FROM batch_events e
	  JOIN batches b ON b.id = e.batch_id
	  WHERE e.project_id = ANY($1::bigint[]) AND e.event_type = '`+model.BatchEventPartialFailed+`'
	  GROUP BY e.project_id, e.batch_id, e.event_type, b.planned_units
	), batch_events_regular AS (
	  SELECT '`+model.ActivitySourceBatch+`'::text AS source, e.id AS event_id, e.project_id,
	         e.event_type AS kind, e.actor_id AS actor_id, e.batch_id AS object_id,
	         e.detail::text AS detail, e.created_at, 0 AS source_rank,
	         0::bigint AS aggregate_count, 0::bigint AS aggregate_total, ''::text AS group_key
	  FROM batch_events e
	  WHERE e.project_id = ANY($1::bigint[]) AND e.event_type <> '`+model.BatchEventPartialFailed+`'
	), merged AS (
	  SELECT source, event_id, project_id, kind, actor_id, object_id, detail, created_at,
	         source_rank, aggregate_count, aggregate_total, group_key FROM batch_failures
	  UNION ALL
	  SELECT source, event_id, project_id, kind, actor_id, object_id, detail, created_at,
	         source_rank, aggregate_count, aggregate_total, group_key FROM batch_events_regular
	  UNION ALL
	  SELECT '`+model.ActivitySourceAudit+`'::text, a.id, a.project_id,
	         a.action, a.actor_user_id, COALESCE(NULLIF(a.resource_id, '')::bigint, 0),
	         COALESCE(NULLIF(a.reason, ''), a.detail), a.created_at, 1,
	         0::bigint, 0::bigint, ''::text
	  FROM audit_logs a WHERE a.project_id = ANY($1::bigint[])
	)
	SELECT source, event_id, project_id, kind, actor_id, object_id, detail, created_at,
	       aggregate_count, aggregate_total, group_key
	FROM merged
    WHERE ($2::bigint IS NULL OR (created_at, source_rank, event_id) < (to_timestamp($2::bigint / 1000000.0), $3::int, $4::bigint))
    ORDER BY created_at DESC, source_rank ASC, event_id DESC
    LIMIT $5`,
		scoped, nullableCursorTime(cursor), model.ActivitySourceRank(cursor.Source), cursor.ID, limit+1)
	if err != nil {
		return nil, "", err
	}
	defer rows.Close()

	items := []model.ActivityItem{}
	for rows.Next() {
		var item model.ActivityItem
		var objectID int64
		var actorID *int64
		var detail string
		var aggregateCount int64
		var aggregateTotal int64
		var groupKey string
		if err := rows.Scan(&item.Source, &item.EventID, &item.ProjectID, &item.Kind,
			&actorID, &objectID, &detail, &item.CreatedAt, &aggregateCount, &aggregateTotal, &groupKey); err != nil {
			return nil, "", err
		}
		item.ActorID = actorID
		item.Detail = detail
		item.GroupKey = groupKey
		item.AggregateCount = aggregateCount
		item.AggregateTotal = aggregateTotal
		item.Summary = activitySummary(item.Source, item.Kind, objectID, aggregateCount, aggregateTotal)
		item.Links = activityLinks(item.Source, item.ProjectID, objectID)
		item.Unread = model.ActivityUnread(item, watermark)
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, "", err
	}

	nextCursor := ""
	if len(items) > limit {
		items = items[:limit]
		nextCursor = model.EncodeActivityCursor(model.ActivityCursorOf(items[len(items)-1]))
	}
	return items, nextCursor, nil
}

// GetReadWatermark 读取个人水位（没有记录时返回零值，表示「全部未读」）。
func (s *ActivityStore) GetReadWatermark(ctx context.Context, userID, workspaceID int64) (model.ReadWatermark, error) {
	var watermark model.ReadWatermark
	err := s.db.QueryRow(ctx, `
    SELECT user_id, workspace_id, last_seen_at, last_seen_event_id, updated_at
    FROM activity_reads WHERE user_id = $1 AND workspace_id = $2`,
		userID, workspaceID).Scan(&watermark.UserID, &watermark.WorkspaceID,
		&watermark.LastSeenAt, &watermark.LastSeenEventID, &watermark.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return model.ReadWatermark{UserID: userID, WorkspaceID: workspaceID}, nil
	}
	if err != nil {
		return model.ReadWatermark{}, err
	}
	return watermark, nil
}

// MarkAllRead 更新个人水位（「全部已读」）。
//
// 只写当前用户的水位行：它不改变任何业务状态，也不影响别人的未读。
// 这正是不用「事件 × 用户的已读标记」的原因（迁移文件头有详细说明）。
func (s *ActivityStore) MarkAllRead(ctx context.Context, userID, workspaceID int64, at time.Time, eventID int64) (model.ReadWatermark, error) {
	var watermark model.ReadWatermark
	err := s.db.QueryRow(ctx, `
    INSERT INTO activity_reads (user_id, workspace_id, last_seen_at, last_seen_event_id)
    VALUES ($1, $2, $3, $4)
    ON CONFLICT (user_id, workspace_id) DO UPDATE
    SET last_seen_at = GREATEST(activity_reads.last_seen_at, EXCLUDED.last_seen_at),
        last_seen_event_id = CASE
          WHEN EXCLUDED.last_seen_at > activity_reads.last_seen_at THEN EXCLUDED.last_seen_event_id
          WHEN EXCLUDED.last_seen_at = activity_reads.last_seen_at
            THEN GREATEST(activity_reads.last_seen_event_id, EXCLUDED.last_seen_event_id)
          ELSE activity_reads.last_seen_event_id END,
        updated_at = NOW()
    RETURNING user_id, workspace_id, last_seen_at, last_seen_event_id, updated_at`,
		userID, workspaceID, at, eventID).Scan(&watermark.UserID, &watermark.WorkspaceID,
		&watermark.LastSeenAt, &watermark.LastSeenEventID, &watermark.UpdatedAt)
	if err != nil {
		return model.ReadWatermark{}, err
	}
	return watermark, nil
}

// countUnreadActivity 统计未读动态数（可读项目范围内）。批次逐单元失败
// 先按批次折叠，保证一个批次的 N 个失败不会制造 N 条未读动态。
func (s *ActivityStore) countUnreadActivity(ctx context.Context, projectIDs []int64, watermark model.ReadWatermark) (int64, error) {
	if len(projectIDs) == 0 {
		return 0, nil
	}
	var count int64
	err := s.db.QueryRow(ctx, `
	WITH batch_failures AS (
	  SELECT project_id, batch_id, MAX(created_at) AS created_at
	  FROM batch_events
	  WHERE project_id = ANY($1::bigint[]) AND event_type = '`+model.BatchEventPartialFailed+`'
	  GROUP BY project_id, batch_id
	), merged AS (
	  SELECT project_id, created_at
	  FROM batch_events
	  WHERE project_id = ANY($1::bigint[]) AND event_type <> '`+model.BatchEventPartialFailed+`'
	  UNION ALL
	  SELECT project_id, created_at FROM batch_failures
	  UNION ALL
	  SELECT project_id, created_at FROM audit_logs WHERE project_id = ANY($1::bigint[])
	)
    SELECT COUNT(*) FROM merged
    WHERE $2::timestamptz IS NULL OR created_at > $2::timestamptz`,
		projectIDs, nullableWatermark(watermark)).Scan(&count)
	return count, err
}

// ---------------------------------------------------------------------------
// 评论
// ---------------------------------------------------------------------------

// CreateCommentInput 是新增或更正评论的输入。
type CreateCommentInput struct {
	ProjectID  int64
	AnchorKind string
	AnchorID   int64
	Body       string
	Mentions   []int64
	AuthorID   int64
}

// CreateComment 新增一条评论（同一作者再次提交同一锚点时是**更正**）。
//
// 更正语义：插入新修订行并把旧行标为 superseded_by，旧行仍可读。
// 「同一作者 + 同一锚点只有一条当前有效评论」由部分唯一索引保证。
func (s *ActivityStore) CreateComment(ctx context.Context, input CreateCommentInput) (model.Comment, error) {
	if err := model.ValidateCommentInput(input.AnchorKind, input.AnchorID, input.Body, input.Mentions); err != nil {
		return model.Comment{}, err
	}
	if input.AuthorID <= 0 {
		return model.Comment{}, &apiStoreError{Message: "评论需要作者"}
	}
	// 提及必须是项目成员：否则提及会通知到无权访问的人（T27 验收项）。
	validMentions, err := s.FilterMentionableUsers(ctx, input.ProjectID, input.Mentions)
	if err != nil {
		return model.Comment{}, err
	}
	if len(validMentions) != len(input.Mentions) {
		return model.Comment{}, model.FieldErrors{{Field: "mentions",
			Message: "提及的人里有非本项目成员：只能提及有权访问该项目的人"}}
	}
	mentionsJSON, err := json.Marshal(validMentions)
	if err != nil {
		return model.Comment{}, err
	}
	// 锚点必须属于本项目：跨项目锚点会让评论出现在别人的项目里。
	if err := s.ensureAnchorInProject(ctx, input.ProjectID, input.AnchorKind, input.AnchorID); err != nil {
		return model.Comment{}, err
	}

	tx, err := s.db.Begin(ctx)
	if err != nil {
		return model.Comment{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// 锁住该作者在该锚点上的当前评论，避免并发更正产生两条「当前」。
	var previousID *int64
	var previousRevision int
	err = tx.QueryRow(ctx, `
    SELECT id, revision FROM comments
    WHERE project_id = $1 AND anchor_kind = $2 AND anchor_id = $3 AND author_id = $4
      AND superseded_by IS NULL
    FOR UPDATE`, input.ProjectID, input.AnchorKind, input.AnchorID, input.AuthorID).
		Scan(&previousID, &previousRevision)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return model.Comment{}, err
	}

	revision := previousRevision + 1

	// 顺序很关键（与 experiment_scores 的更正同一形态，那里由测试发现过该缺陷）：
	// `uniq_comment_current` 是**部分**唯一索引（只约束 superseded_by IS NULL 的行）。
	// 若先 INSERT 新行再标记旧行，两条行在那一刻都是 NULL，索引立刻报 23505
	// → 更正永远失败。因此先取下一个序列值，在插入前就把旧行标成被它取代，
	// 再用**显式 id** 插入。整个过程在同一事务与行锁下，不存在竞争。
	newID := int64(0)
	if previousID != nil {
		if err := tx.QueryRow(ctx, `
      SELECT nextval(pg_get_serial_sequence('comments', 'id'))`).Scan(&newID); err != nil {
			return model.Comment{}, err
		}
		if _, err := tx.Exec(ctx, `
      UPDATE comments SET superseded_by = $2 WHERE id = $1`, *previousID, newID); err != nil {
			return model.Comment{}, err
		}
	}

	var comment model.Comment
	var rawMentions []byte
	err = tx.QueryRow(ctx, `
    INSERT INTO comments (id, project_id, anchor_kind, anchor_id, body, mentions, revision, supersedes_id, author_id)
    VALUES (COALESCE(NULLIF($1::bigint, 0), nextval(pg_get_serial_sequence('comments', 'id'))),
            $2, $3, $4, $5, $6, $7, $8, $9)
    RETURNING id, project_id, anchor_kind, anchor_id, body, mentions, revision,
              supersedes_id, superseded_by, author_id, created_at`,
		newID, input.ProjectID, input.AnchorKind, input.AnchorID, strings.TrimSpace(input.Body),
		mentionsJSON, revision, previousID, input.AuthorID,
	).Scan(&comment.ID, &comment.ProjectID, &comment.AnchorKind, &comment.AnchorID,
		&comment.Body, &rawMentions, &comment.Revision, &comment.SupersedesID,
		&comment.SupersededBy, &comment.AuthorID, &comment.CreatedAt)
	if err != nil {
		return model.Comment{}, err
	}
	comment.Mentions = decodeMentionIDs(rawMentions)
	comment.Current = true

	// 审计与评论同事务（T03 的约定）。**不写 review_decisions**：
	// 评论不是 Decision，不能解除发布门槛。
	if err := writeStudioAuditTx(ctx, tx, StudioAudit{
		ActorID: input.AuthorID, Action: "comment_created", Resource: "comment",
		ResourceID: fmt.Sprint(comment.ID), ProjectID: input.ProjectID,
		Reason: fmt.Sprintf("anchor=%s:%d revision=%d mentions=%d",
			input.AnchorKind, input.AnchorID, comment.Revision, len(comment.Mentions)),
	}); err != nil {
		return model.Comment{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return model.Comment{}, err
	}
	return comment, nil
}

// ListComments 列出锚点上的评论（当前有效在前，历史修订在后）。
func (s *ActivityStore) ListComments(ctx context.Context, projectID int64, anchorKind string, anchorID int64, limit int) ([]model.Comment, error) {
	if limit <= 0 || limit > 200 {
		limit = 100
	}
	rows, err := s.db.Query(ctx, `
    SELECT id, project_id, anchor_kind, anchor_id, body, mentions, revision,
           supersedes_id, superseded_by, author_id, created_at
    FROM comments
    WHERE project_id = $1 AND anchor_kind = $2 AND anchor_id = $3
    ORDER BY (superseded_by IS NULL) DESC, created_at DESC
    LIMIT $4`, projectID, anchorKind, anchorID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	comments := []model.Comment{}
	for rows.Next() {
		var comment model.Comment
		var rawMentions []byte
		if err := rows.Scan(&comment.ID, &comment.ProjectID, &comment.AnchorKind, &comment.AnchorID,
			&comment.Body, &rawMentions, &comment.Revision, &comment.SupersedesID,
			&comment.SupersededBy, &comment.AuthorID, &comment.CreatedAt); err != nil {
			return nil, err
		}
		comment.Mentions = decodeMentionIDs(rawMentions)
		comment.Current = comment.SupersededBy == nil
		comments = append(comments, comment)
	}
	return comments, rows.Err()
}

// CommentByID 读取一条评论。
func (s *ActivityStore) CommentByID(ctx context.Context, projectID, commentID int64) (model.Comment, error) {
	var comment model.Comment
	var rawMentions []byte
	err := s.db.QueryRow(ctx, `
    SELECT id, project_id, anchor_kind, anchor_id, body, mentions, revision,
           supersedes_id, superseded_by, author_id, created_at
    FROM comments WHERE id = $1 AND project_id = $2`, commentID, projectID).
		Scan(&comment.ID, &comment.ProjectID, &comment.AnchorKind, &comment.AnchorID,
			&comment.Body, &rawMentions, &comment.Revision, &comment.SupersedesID,
			&comment.SupersededBy, &comment.AuthorID, &comment.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return model.Comment{}, ErrCommentNotFound
	}
	if err != nil {
		return model.Comment{}, err
	}
	comment.Mentions = decodeMentionIDs(rawMentions)
	comment.Current = comment.SupersededBy == nil
	return comment, nil
}

// FilterMentionableUsers 返回 userIDs 里**确实是该项目成员**的那些。
//
// 用「返回交集」而不是「返回布尔」：调用方需要知道哪些被丢掉了才能给出
// 可操作的字段错误（「你提到了 3 个人，其中 1 个不是本项目成员」）。
func (s *ActivityStore) FilterMentionableUsers(ctx context.Context, projectID int64, userIDs []int64) ([]int64, error) {
	if projectID <= 0 || len(userIDs) == 0 {
		return []int64{}, nil
	}
	rows, err := s.db.Query(ctx, `
    SELECT user_id FROM project_members
    WHERE project_id = $1 AND user_id = ANY($2::bigint[])
    ORDER BY user_id`, projectID, userIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	valid := []int64{}
	for rows.Next() {
		var userID int64
		if err := rows.Scan(&userID); err != nil {
			return nil, err
		}
		valid = append(valid, userID)
	}
	return valid, rows.Err()
}

// ensureAnchorInProject 校验锚点属于该项目。
func (s *ActivityStore) ensureAnchorInProject(ctx context.Context, projectID int64, anchorKind string, anchorID int64) error {
	var exists bool
	var err error
	switch anchorKind {
	case model.CommentAnchorSampleVersion:
		err = s.db.QueryRow(ctx, `
      SELECT EXISTS(SELECT 1 FROM sample_versions WHERE id = $1 AND project_id = $2)`,
			anchorID, projectID).Scan(&exists)
	case model.CommentAnchorBatch:
		err = s.db.QueryRow(ctx, `
      SELECT EXISTS(SELECT 1 FROM batches WHERE id = $1 AND project_id = $2)`,
			anchorID, projectID).Scan(&exists)
	default:
		return model.FieldErrors{{Field: "anchorKind", Message: "不支持的评论锚点类型"}}
	}
	if err != nil {
		return err
	}
	if !exists {
		return model.FieldErrors{{Field: "anchorId",
			Message: "评论锚点不存在或不属于该项目"}}
	}
	return nil
}

// ---------------------------------------------------------------------------
// 搜索
// ---------------------------------------------------------------------------

// SearchHit 是命令搜索的一条结果。
type SearchHit struct {
	Kind      string `json:"kind"`
	ProjectID int64  `json:"projectId,omitempty"`
	ObjectID  int64  `json:"objectId,omitempty"`
	Label     string `json:"label"`
	Caption   string `json:"caption,omitempty"`
	PagePath  string `json:"pagePath"`
}

// Search 在**可访问范围**内搜索项目与对象。
//
// 三条与验收项直接相关：
//   - 只搜可读项目（撤权项目不出现在搜索里）；
//   - 页面（路由元数据）的搜索由前端做（它才知道导航结构），这里只搜数据；
//   - 结果带 `pagePath`，前端不自行拼 URL。
func (s *ActivityStore) Search(ctx context.Context, userID, workspaceID int64, keyword string, limit int) ([]SearchHit, error) {
	keyword = strings.TrimSpace(keyword)
	if keyword == "" {
		return []SearchHit{}, nil
	}
	if limit <= 0 || limit > 50 {
		limit = 20
	}
	projectIDs, err := s.projects.ProjectIDsForUser(ctx, userID)
	if err != nil {
		return nil, err
	}
	scoped, err := s.filterWorkspaceProjects(ctx, workspaceID, projectIDs)
	if err != nil {
		return nil, err
	}
	if len(scoped) == 0 {
		return []SearchHit{}, nil
	}
	pattern := "%" + keyword + "%"

	hits := []SearchHit{}
	// 项目
	rows, err := s.db.Query(ctx, `
    SELECT id, name, goal FROM projects
    WHERE workspace_id = $1 AND id = ANY($2::bigint[])
      AND (name ILIKE $3 OR goal ILIKE $3)
    ORDER BY updated_at DESC LIMIT $4`, workspaceID, scoped, pattern, limit)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var hit SearchHit
		var goal string
		if err := rows.Scan(&hit.ProjectID, &hit.Label, &goal); err != nil {
			rows.Close()
			return nil, err
		}
		hit.Kind, hit.Caption = "project", goal
		hit.PagePath = projectPageLink(hit.ProjectID, "overview")
		hits = append(hits, hit)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// 样本（按标题/键）
	rows, err = s.db.Query(ctx, `
    SELECT s.project_id, s.id, s.title, s.sample_key FROM samples s
    WHERE s.project_id = ANY($1::bigint[])
      AND (s.title ILIKE $2 OR s.sample_key ILIKE $2)
    ORDER BY s.updated_at DESC LIMIT $3`, scoped, pattern, limit)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var hit SearchHit
		var key string
		if err := rows.Scan(&hit.ProjectID, &hit.ObjectID, &hit.Label, &key); err != nil {
			rows.Close()
			return nil, err
		}
		hit.Kind, hit.Caption = "sample", key
		hit.PagePath = projectPageLink(hit.ProjectID, "data") + "/" + fmt.Sprint(hit.ObjectID)
		hits = append(hits, hit)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// 批次（按名称/用途，无名称时用 ID）
	rows, err = s.db.Query(ctx, `
    SELECT b.project_id, b.id, b.purpose, b.status FROM batches b
    WHERE b.project_id = ANY($1::bigint[])
      AND ($2::text = '' OR b.status ILIKE $2 OR b.purpose ILIKE $2)
    ORDER BY b.created_at DESC LIMIT $3`, scoped, pattern, limit)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var hit SearchHit
		var purpose, status string
		if err := rows.Scan(&hit.ProjectID, &hit.ObjectID, &purpose, &status); err != nil {
			rows.Close()
			return nil, err
		}
		hit.Kind = "batch"
		hit.Label = fmt.Sprintf("批次 %d（%s）", hit.ObjectID, purpose)
		hit.Caption = status
		hit.PagePath = projectPageLink(hit.ProjectID, "runs") + "/" + fmt.Sprint(hit.ObjectID)
		hits = append(hits, hit)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	if len(hits) > limit {
		hits = hits[:limit]
	}
	return hits, nil
}

// ---------------------------------------------------------------------------
// 内部辅助
// ---------------------------------------------------------------------------

// projectPageLink 拼项目内页面的前端路径（前端不自行拼 URL）。
func projectPageLink(projectID int64, tab string) string {
	base := "/p/" + fmt.Sprint(projectID) + "/"
	switch tab {
	case "overview", "blueprint", "coverage", "standard", "pilot", "compare", "runs", "data", "quality", "review", "rules", "releases":
		return base + tab
	default:
		return base + "overview"
	}
}

// nullableCursorTime 把游标时间转成可空的微秒值（首页传 NULL）。
func nullableCursorTime(cursor model.ActivityCursor) *int64 {
	if cursor.Time == 0 && cursor.Source == "" && cursor.ID == 0 {
		return nil
	}
	value := cursor.Time
	return &value
}

// nullableWatermark 把水位转成可空时间（没有记录时表示「全部未读」）。
func nullableWatermark(watermark model.ReadWatermark) *time.Time {
	if watermark.LastSeenAt.IsZero() {
		return nil
	}
	value := watermark.LastSeenAt
	return &value
}

// activitySummary 把事件翻译成面向用户的一句话。
//
// 未知事件**显示原始 kind**而不是一个猜的标签：猜错会让用户以为系统做了
// 一件它没做的事，而显示代码至少可以被搜索到。
func activitySummary(source, kind string, objectID, aggregateCount, aggregateTotal int64) string {
	if source == model.ActivitySourceBatch {
		if kind == model.BatchEventPartialFailed && aggregateCount > 0 && objectID > 0 {
			if aggregateTotal > 0 {
				return fmt.Sprintf("批次 %d 部分失败（%d/%d 个单元失败）", objectID, aggregateCount, aggregateTotal)
			}
			return fmt.Sprintf("批次 %d 部分失败（%d 个单元失败）", objectID, aggregateCount)
		}
		if label, found := batchEventLabels[kind]; found {
			if objectID > 0 {
				return fmt.Sprintf("%s（批次 %d）", label, objectID)
			}
			return label
		}
		return fmt.Sprintf("批次事件 %s", kind)
	}
	if label, found := auditActionLabels[kind]; found {
		return label
	}
	return fmt.Sprintf("操作 %s", kind)
}

// activityLinks 给出可跳转的链接。
func activityLinks(source string, projectID, objectID int64) model.Links {
	if projectID <= 0 {
		return model.Links{"page": "/activity"}
	}
	if source == model.ActivitySourceBatch && objectID > 0 {
		return model.Links{"page": projectPageLink(projectID, "runs") + "/" + fmt.Sprint(objectID)}
	}
	return model.Links{"page": projectPageLink(projectID, "overview")}
}

// batchEventLabels 是批次事件的展示文案（覆盖 model.BatchEvent* 的全部取值）。
var batchEventLabels = map[string]string{
	model.BatchEventQueued:         "批次已排队",
	model.BatchEventStarted:        "批次开始执行",
	model.BatchEventPaused:         "批次已暂停",
	model.BatchEventResumed:        "批次已恢复",
	model.BatchEventStepCompleted:  "批次阶段完成",
	model.BatchEventPartialFailed:  "批次部分失败",
	model.BatchEventCompleted:      "批次完成",
	model.BatchEventFailed:         "批次失败",
	model.BatchEventRetryRequested: "批次请求重试",
}

// auditActionLabels 是常见的项目级审计动作文案。
//
// 未列出的动作显示原始 code（见 activitySummary 的说明）。
var auditActionLabels = map[string]string{
	"batch_create":                "创建批次",
	"batch_pause":                 "暂停批次",
	"batch_resume":                "恢复批次",
	"batch_retry_failed":          "重试失败项",
	"experiment_create":           "创建质量实验",
	"release_candidate_created":   "创建发布候选",
	"release_published":           "发布版本",
	"review_decision":             "提交人工判断",
	"recipe_create":               "创建方案",
	"project_created_from_recipe": "用方案创建项目",
	"comment_created":             "发表评论",
}

// decodeMentionIDs 解析 mentions JSONB。
func decodeMentionIDs(raw []byte) []int64 {
	ids := []int64{}
	if len(raw) == 0 {
		return ids
	}
	if err := json.Unmarshal(raw, &ids); err != nil {
		return []int64{}
	}
	return ids
}

// MarkAllReadNow 用**数据库时钟**推进个人水位（「全部已读」）。
//
// 为什么不接受调用方传入的时间：事件时间来自数据库 NOW()，
// 用应用进程的时钟会在两者有偏移时把「刚刚产生的事件」标成已读
// （用户看不到自己刚触发的那条）。水位也只写当前用户。
//
// eventID 传 0：同一微秒内产生的事件会**保守地显示为未读**——
// 这个方向的误差是无害的（未读不等于业务已处理），反过来才会丢提醒。
func (s *ActivityStore) MarkAllReadNow(ctx context.Context, userID, workspaceID int64) (model.ReadWatermark, error) {
	var watermark model.ReadWatermark
	err := s.db.QueryRow(ctx, `
    INSERT INTO activity_reads (user_id, workspace_id, last_seen_at, last_seen_event_id)
    VALUES ($1, $2, NOW(), 0)
    ON CONFLICT (user_id, workspace_id) DO UPDATE
    SET last_seen_at = GREATEST(activity_reads.last_seen_at, NOW()),
        updated_at = NOW()
    RETURNING user_id, workspace_id, last_seen_at, last_seen_event_id, updated_at`,
		userID, workspaceID).Scan(&watermark.UserID, &watermark.WorkspaceID,
		&watermark.LastSeenAt, &watermark.LastSeenEventID, &watermark.UpdatedAt)
	if err != nil {
		return model.ReadWatermark{}, err
	}
	return watermark, nil
}
