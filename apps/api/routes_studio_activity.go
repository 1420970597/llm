package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/1420970597/llm/internal/model"
	"github.com/1420970597/llm/internal/store"
	"github.com/1420970597/llm/internal/studio"
)

// 本文件实现「今日工作」「动态」「命令搜索」与「评论」（Issue #160 T27）。
//
// 契约：docs/plans/atelier-api-contract.md §1（信封/错误/幂等）、
// sql/migrations/0033_studio_activity_comments.sql、#160 T27 的原文要求。
//
// 权限：今日工作/动态/搜索是**工作区作用域**（只含可读项目，过滤在 store 的
// SQL 里）；评论是**项目作用域**（读取需 AuthzRead，发表/更正需 AuthzReview ——
// viewer 只读，因此不能发表评论）。
//
// 评论不是 Decision：本文件的写入路径只调用 ActivityStore 的评论方法，
// 它不触碰 review_decisions / review_projections / releases，因此
// 「评论不能解除发布门槛」是结构性的（不是靠约定）。

func init() {
	RegisterRoutes(registerStudioActivityRoutes)
}

func registerStudioActivityRoutes(mux *http.ServeMux, app *application) {
	mux.HandleFunc("GET /api/v1/today", app.getTodayWork)
	mux.HandleFunc("GET /api/v1/activity", app.listActivity)
	mux.HandleFunc("POST /api/v1/activity/read", app.markActivityRead)
	mux.HandleFunc("GET /api/v1/search", app.searchStudio)

	mux.HandleFunc("GET /api/v1/projects/{projectId}/comments", app.listComments)
	mux.HandleFunc("POST /api/v1/projects/{projectId}/comments", app.createComment)
	mux.HandleFunc("PATCH /api/v1/projects/{projectId}/comments/{commentId}", app.updateComment)
	mux.HandleFunc("GET /api/v1/projects/{projectId}/mention-candidates", app.listMentionCandidates)
}

// ---------------------------------------------------------------------------
// 今日工作与动态
// ---------------------------------------------------------------------------

// getTodayWork 返回待办聚合（每条带具体对象链接）。
func (app *application) getTodayWork(w http.ResponseWriter, r *http.Request) {
	user, workspaceID, ok := app.resolveActivityWorkspace(w, r)
	if !ok {
		return
	}
	activity := store.NewActivityStore(app.studio.Pool)
	todos, err := activity.LoadTodos(r.Context(), user.ID, workspaceID, 3)
	if err != nil {
		app.writeStudioError(w, r, err)
		return
	}
	watermark, err := activity.GetReadWatermark(r.Context(), user.ID, workspaceID)
	if err != nil {
		app.writeStudioError(w, r, err)
		return
	}
	// issue #197 第 10 条：今日工作以前只有一个「7 条未读动态」的数字，
	// 甲方原话是「展示的数据不对，应当是一个工作台总览的效果」。
	// 这里的每个数字都来自真实对象，且都能点进对应列表（口径与列表页一致）。
	overview, err := activity.LoadWorkspaceOverview(r.Context(), user.ID, workspaceID)
	if err != nil {
		app.writeStudioError(w, r, err)
		return
	}
	app.writeJSON(w, http.StatusOK, map[string]any{
		"todos": todos,
		// 水位的语义是「我看到哪了」，不是「哪些事处理完了」。
		"watermark": watermark,
		"overview":  overview,
		"notes": []string{
			"未读只影响红点：待办由事实派生（待判断/失败恢复/候选阻塞），不会因为点了「全部已读」而消失",
			"总览里的每个数字都是计数，不是推算出来的比率；点进去看到的是同一份事实。",
		},
	})
}

// listActivity 返回动态（带游标增量轮询）。
func (app *application) listActivity(w http.ResponseWriter, r *http.Request) {
	user, workspaceID, ok := app.resolveActivityWorkspace(w, r)
	if !ok {
		return
	}
	cursor, err := model.DecodeActivityCursor(r.URL.Query().Get("cursor"))
	if err != nil {
		app.writeStudioError(w, r, err)
		return
	}
	limit, _ := strconv.Atoi(strings.TrimSpace(r.URL.Query().Get("limit")))
	activity := store.NewActivityStore(app.studio.Pool)
	items, nextCursor, err := activity.LoadActivity(r.Context(), user.ID, workspaceID, cursor, limit)
	if err != nil {
		app.writeStudioError(w, r, err)
		return
	}
	app.writeJSON(w, http.StatusOK, map[string]any{
		"items": items, "nextCursor": nextCursor, "sortKey": "createdAt:desc,source:asc,id:desc",
	})
}

// markActivityRead 推进**当前用户**的阅读水位。
func (app *application) markActivityRead(w http.ResponseWriter, r *http.Request) {
	user, workspaceID, ok := app.resolveActivityWorkspace(w, r)
	if !ok {
		return
	}
	activity := store.NewActivityStore(app.studio.Pool)
	watermark, err := activity.MarkAllReadNow(r.Context(), user.ID, workspaceID)
	if err != nil {
		app.writeStudioError(w, r, err)
		return
	}
	app.writeJSON(w, http.StatusOK, map[string]any{
		"watermark": watermark,
		"notes": []string{
			"「全部已读」只更新你个人的阅读水位：不改变任何业务状态，也不影响其他成员的未读",
		},
	})
}

// searchStudio 在可访问范围内搜索项目与对象。
func (app *application) searchStudio(w http.ResponseWriter, r *http.Request) {
	user, workspaceID, ok := app.resolveActivityWorkspace(w, r)
	if !ok {
		return
	}
	keyword := strings.TrimSpace(r.URL.Query().Get("q"))
	limit, _ := strconv.Atoi(strings.TrimSpace(r.URL.Query().Get("limit")))
	activity := store.NewActivityStore(app.studio.Pool)
	hits, err := activity.Search(r.Context(), user.ID, workspaceID, keyword, limit)
	if err != nil {
		app.writeStudioError(w, r, err)
		return
	}
	app.writeJSON(w, http.StatusOK, map[string]any{
		"items": hits, "nextCursor": "", "sortKey": "relevance",
		"notes": []string{"只搜索你有权访问的项目与对象；页面导航的匹配由前端完成"},
	})
}

// resolveActivityWorkspace 解析工作区并校验成员身份。
func (app *application) resolveActivityWorkspace(w http.ResponseWriter, r *http.Request) (model.User, int64, bool) {
	user, ok := requestUser(r)
	if !ok {
		app.writeStudioError(w, r, studio.NewError(studio.CodeUnauthorized, msgAuthRequired))
		return model.User{}, 0, false
	}
	requested, _ := strconv.ParseInt(strings.TrimSpace(r.URL.Query().Get("workspaceId")), 10, 64)
	workspaceID := requested
	if workspaceID <= 0 {
		workspace, err := app.projects.DefaultWorkspace(r.Context())
		if err != nil {
			app.writeAPIEntityError(w, r, err)
			return model.User{}, 0, false
		}
		workspaceID = workspace.ID
	}
	decision, err := app.authz.AuthorizeWorkspace(r.Context(), workspaceID, user.ID, store.AuthzRead)
	if err != nil {
		app.writeAPIEntityError(w, r, err)
		return model.User{}, 0, false
	}
	if !decision.IsMember {
		app.writeStudioError(w, r, studio.NewError(studio.CodeNotFound, "未找到该工作区"))
		return model.User{}, 0, false
	}
	return user, workspaceID, true
}

// ---------------------------------------------------------------------------
// 评论
// ---------------------------------------------------------------------------

type commentRequest struct {
	AnchorKind string  `json:"anchorKind"`
	AnchorID   int64   `json:"anchorId"`
	Body       string  `json:"body"`
	Mentions   []int64 `json:"mentions"`
}

// listComments 列出锚点上的评论（当前有效 + 历史修订）。
func (app *application) listComments(w http.ResponseWriter, r *http.Request) {
	user, projectID, ok := app.authorizeComment(w, r, store.AuthzRead)
	if !ok {
		return
	}
	anchorKind := strings.TrimSpace(r.URL.Query().Get("anchorKind"))
	anchorID, _ := strconv.ParseInt(strings.TrimSpace(r.URL.Query().Get("anchorId")), 10, 64)
	if err := model.ValidateCommentInput(anchorKind, anchorID, "占位", nil); err != nil {
		// 只借用锚点部分校验：正文在读取时无关。
		if fieldErrors, ok := model.HasFieldErrors(err); ok {
			filtered := model.FieldErrors{}
			for _, item := range fieldErrors {
				if !strings.HasPrefix(item.Field, "body") {
					filtered = append(filtered, item)
				}
			}
			if len(filtered) > 0 {
				app.writeStudioError(w, r, studio.NewValidationError("评论锚点参数不正确", filtered))
				return
			}
		}
	}
	activity := store.NewActivityStore(app.studio.Pool)
	comments, err := activity.ListComments(r.Context(), projectID, anchorKind, anchorID, 100)
	if err != nil {
		app.writeStudioError(w, r, err)
		return
	}
	// 提及按**当前**成员关系过滤：撤权后旧评论里的提及不再对被撤权者可见。
	//
	// 用**一次查询**取全部被提及者的有效集合，再在内存里求交集：
	// 逐条评论各查一次会让「一页 100 条评论」变成 100 次往返，
	// 而这个端点在审阅页是高频调用的。
	mentionSet := map[int64]bool{}
	allMentions := []int64{}
	for _, comment := range comments {
		for _, userID := range comment.Mentions {
			if !mentionSet[userID] {
				mentionSet[userID] = true
				allMentions = append(allMentions, userID)
			}
		}
	}
	validMentions, err := activity.FilterMentionableUsers(r.Context(), projectID, allMentions)
	if err != nil {
		app.writeStudioError(w, r, err)
		return
	}
	validSet := make(map[int64]bool, len(validMentions))
	for _, userID := range validMentions {
		validSet[userID] = true
	}
	visible := make([]model.Comment, 0, len(comments))
	for _, comment := range comments {
		filtered := make([]int64, 0, len(comment.Mentions))
		for _, userID := range comment.Mentions {
			if validSet[userID] {
				filtered = append(filtered, userID)
			}
		}
		comment.Mentions = filtered
		visible = append(visible, comment)
	}
	app.writeJSON(w, http.StatusOK, map[string]any{
		"items": visible, "nextCursor": "", "sortKey": "current:desc,createdAt:desc",
		"notes":    []string{"评论不是判断：它不能解除发布门槛，也不能替代人工 Decision"},
		"viewerId": user.ID,
	})
}

// createComment 发表评论（同一作者再次提交同一锚点时是更正）。
func (app *application) createComment(w http.ResponseWriter, r *http.Request) {
	user, projectID, ok := app.authorizeComment(w, r, store.AuthzReview)
	if !ok {
		return
	}
	var request commentRequest
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		app.writeStudioError(w, r, studio.NewValidationError("请求格式有误，请检查填写的内容后重试", nil))
		return
	}
	activity := store.NewActivityStore(app.studio.Pool)
	comment, err := activity.CreateComment(r.Context(), store.CreateCommentInput{
		ProjectID: projectID, AnchorKind: request.AnchorKind, AnchorID: request.AnchorID,
		Body: request.Body, Mentions: request.Mentions, AuthorID: user.ID,
	})
	if err != nil {
		app.writeCommentError(w, r, err)
		return
	}
	app.writeJSON(w, http.StatusCreated, comment)
}

// updateComment 更正自己的评论（保留旧修订）。
func (app *application) updateComment(w http.ResponseWriter, r *http.Request) {
	user, projectID, ok := app.authorizeComment(w, r, store.AuthzReview)
	if !ok {
		return
	}
	commentID, err := strconv.ParseInt(strings.TrimSpace(r.PathValue("commentId")), 10, 64)
	if err != nil || commentID <= 0 {
		app.writeStudioError(w, r, studio.NewError(studio.CodeNotFound, "未找到该评论"))
		return
	}
	activity := store.NewActivityStore(app.studio.Pool)
	existing, err := activity.CommentByID(r.Context(), projectID, commentID)
	if err != nil {
		app.writeCommentError(w, r, err)
		return
	}
	if existing.AuthorID != user.ID {
		app.writeStudioError(w, r, studio.NewError(studio.CodeForbidden,
			"只有评论作者可以更正自己的评论（评论是证据，不能让任何人改别人的话）"))
		return
	}
	var request commentRequest
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		app.writeStudioError(w, r, studio.NewValidationError("请求格式有误，请检查填写的内容后重试", nil))
		return
	}
	// 更正复用「同一作者 + 同一锚点」的追加语义：锚点不可改（改了就是另一条评论）。
	updated, err := activity.CreateComment(r.Context(), store.CreateCommentInput{
		ProjectID: projectID, AnchorKind: existing.AnchorKind, AnchorID: existing.AnchorID,
		Body: request.Body, Mentions: request.Mentions, AuthorID: user.ID,
	})
	if err != nil {
		app.writeCommentError(w, r, err)
		return
	}
	app.writeJSON(w, http.StatusOK, updated)
}

// listMentionCandidates 返回可被提及的项目成员（避免前端自己拼用户列表）。
func (app *application) listMentionCandidates(w http.ResponseWriter, r *http.Request) {
	_, projectID, ok := app.authorizeComment(w, r, store.AuthzRead)
	if !ok {
		return
	}
	members, err := app.projects.ListProjectMembers(r.Context(), projectID)
	if err != nil {
		app.writeStudioError(w, r, err)
		return
	}
	app.writeJSON(w, http.StatusOK, map[string]any{"items": members, "nextCursor": "", "sortKey": "userId:asc"})
}

// authorizeComment 做项目级授权（读取 AuthzRead / 发表 AuthzReview）。
func (app *application) authorizeComment(w http.ResponseWriter, r *http.Request, action store.AuthzAction) (model.User, int64, bool) {
	user, ok := requestUser(r)
	if !ok {
		app.writeStudioError(w, r, studio.NewError(studio.CodeUnauthorized, msgAuthRequired))
		return model.User{}, 0, false
	}
	projectID, err := parseProjectID(r.PathValue("projectId"))
	if err != nil {
		app.writeStudioError(w, r, studio.NewError(studio.CodeNotFound, msgProjectNotFound))
		return model.User{}, 0, false
	}
	if _, err := app.studio.Authorize(r.Context(), projectID, user.ID, action); err != nil {
		app.writeStudioError(w, r, err)
		return model.User{}, 0, false
	}
	return user, projectID, true
}

// writeCommentError 把评论的哨兵错误翻译成契约错误。
func (app *application) writeCommentError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, store.ErrCommentNotFound):
		app.writeStudioError(w, r, studio.NewError(studio.CodeNotFound, "未找到该评论"))
		return
	case errors.Is(err, store.ErrCommentNotAuthor):
		app.writeStudioError(w, r, studio.NewError(studio.CodeForbidden, err.Error()))
		return
	}
	app.writeAPIEntityError(w, r, err)
}
