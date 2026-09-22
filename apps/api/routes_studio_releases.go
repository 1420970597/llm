package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/1420970597/llm/internal/model"
	"github.com/1420970597/llm/internal/storage"
	"github.com/1420970597/llm/internal/store"
	"github.com/1420970597/llm/internal/studio"
	"github.com/jackc/pgx/v5"
)

// 本文件实现发布准备、阻塞数据卡、固定下载与交付库（Issue #160 T22）。
//
// 契约：docs/plans/atelier-api-contract.md §2.8（候选与 blocker）、§2.9（冻结发布）、
// §2.10（下载与下一版）、§3（L01–L03/B03）。
//
// 三条来自 T22 验收项的关键决定：
//
//  1. **URL 与下载只用稳定 releaseId**，版本名只出现在展示与文件名里
//     （§2.5）。因此页面不能靠「版本名」定位对象。
//  2. **文件名含类型与版本名，但不叫 latest**：`latest` 会让人以为文件总是
//     最新，而它其实是一个固定版本（且下载路径禁止 latest 回退）。
//  3. **viewer 没有发布入口且 API 拒绝**：服务端按 AuthzPublish 重新判定，
//     不依赖界面隐藏按钮（契约 §4「前端禁用按钮不构成安全边界」）。

func init() {
	RegisterRoutes(registerReleaseRoutes)
}

func registerReleaseRoutes(mux *http.ServeMux, app *application) {
	base := projectPrefix + "/{projectId}/releases"
	mux.HandleFunc("POST "+base, app.createReleaseCandidate)
	mux.HandleFunc("GET "+base, app.listReleases)
	mux.HandleFunc("GET "+base+"/{releaseId}", app.getReleaseCard)
	mux.HandleFunc("POST "+base+"/{releaseId}/publish", app.publishRelease)
	mux.HandleFunc("POST "+base+"/{releaseId}/next-candidate", app.createNextCandidate)
	mux.HandleFunc("GET "+base+"/{releaseId}/artifacts/{artifactId}/download", app.downloadReleaseArtifact)
	mux.HandleFunc("GET "+base+"/{releaseId}/next-candidate", app.releaseNextCandidateLinks)

	// 交付库：**独立公共对象**（契约 §3 的 B03），只含已发布且可访问的版本。
	mux.HandleFunc("GET /api/v1/deliveries", app.listDeliveries)
}

// releaseCandidateRequest 是创建/修订候选的请求体（契约 §2.8）。
type releaseCandidateRequest struct {
	ReleaseName      string         `json:"releaseName"`
	SampleVersionIDs []int64        `json:"sampleVersionIds"`
	MappingVersionID int64          `json:"mappingVersionId"`
	Format           string         `json:"format"`
	IntendedUse      string         `json:"intendedUse"`
	Limitations      []string       `json:"limitations"`
	Provenance       map[string]any `json:"provenance"`
}

// createReleaseCandidate 创建发布候选（同事务分配 candidateId + releaseId + 版本名）。
func (app *application) createReleaseCandidate(w http.ResponseWriter, r *http.Request) {
	user, projectID, role, ok := app.authorizeRelease(w, r, store.AuthzPublish)
	if !ok {
		return
	}
	var request releaseCandidateRequest
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		app.writeStudioError(w, r, studio.NewValidationError("请求格式有误，请检查填写的内容后重试", nil))
		return
	}
	release, err := app.studio.Releases.CreateReleaseCandidate(r.Context(), store.CreateReleaseCandidateInput{
		ProjectID: projectID, ReleaseName: request.ReleaseName,
		MappingVersionID: request.MappingVersionID, Format: request.Format,
		IntendedUse: request.IntendedUse, Limitations: request.Limitations,
		Provenance: request.Provenance, SampleVersionIDs: request.SampleVersionIDs,
		CreatedBy: &user.ID,
	})
	if err != nil {
		app.writeReleaseError(w, r, err)
		return
	}
	app.writeReleaseEnvelope(w, r.Context(), http.StatusCreated, projectID, release, role)
}

// getReleaseCard 返回发布 + 数据卡 + 制品（L03）。
//
// 数据卡（qualitySnapshot/coverageSummary）与 blocker 快照一起返回：
// 「为什么被挡住」与「这一版的质量范围是什么」是同一页要回答的两个问题。
func (app *application) getReleaseCard(w http.ResponseWriter, r *http.Request) {
	user, projectID, role, ok := app.authorizeRelease(w, r, store.AuthzRead)
	if !ok {
		return
	}
	releaseID, ok := app.parseReleaseID(w, r)
	if !ok {
		return
	}
	release, err := app.studio.Releases.GetRelease(r.Context(), projectID, releaseID)
	if err != nil {
		app.writeReleaseError(w, r, err)
		return
	}
	artifacts, err := app.studio.ReleaseArtifacts.ListArtifacts(r.Context(), releaseID, release.CandidateRevision)
	if err != nil {
		app.writeStudioError(w, r, err)
		return
	}
	manifest, manifestHash, manifestErr := app.studio.ReleaseArtifacts.GetManifest(
		r.Context(), releaseID, release.CandidateRevision)
	if manifestErr != nil && !errors.Is(manifestErr, store.ErrArtifactNotFound) {
		app.writeStudioError(w, r, manifestErr)
		return
	}
	_ = user

	envelope := studio.NewEnvelope(
		strconv.FormatInt(release.ID, 10), release.Status, release.CandidateRevision,
		release.UpdatedAt, model.ReleaseCapabilities(role, release.Status),
		releaseLinks(projectID, release.ID), nil)
	envelope.Data = struct {
		Release      store.Release           `json:"release"`
		Artifacts    []store.ReleaseArtifact `json:"artifacts"`
		Manifest     model.ReleaseManifest   `json:"manifest"`
		ManifestHash string                  `json:"manifestHash"`
		// Blockers 是**快照**（发布门槛检查时的结果），每条带可跳转对象。
		Blockers []studio.Blocker `json:"blockers"`
	}{
		Release: release, Artifacts: artifacts, Manifest: manifest, ManifestHash: manifestHash,
		Blockers: app.releaseBlockers(r.Context(), projectID, release),
	}
	app.writeStudioEnvelope(w, http.StatusOK, envelope)
}

// releaseBlockers 把门槛快照转成带链接的 blocker（契约 §2.8）。
func (app *application) releaseBlockers(ctx context.Context, projectID int64, release store.Release) []studio.Blocker {
	blockers := []studio.Blocker{}
	for _, blocker := range release.Blockers {
		item := studio.Blocker{Code: blocker.Code, Message: blocker.Message}
		if blocker.SampleVersionID != nil {
			// blocker 只保存 sample_versions.id；SPA 页面需要样本身份与
			// 样本内版本号才能打开 `/p/:id/data/s_x?version=n`。
			// 先按行 ID 解析，禁止把版本行 ID 当成 sampleId 拼进 URL。
			version, err := app.studio.Batches.GetSampleVersionByID(ctx, projectID, *blocker.SampleVersionID)
			if err == nil {
				item.Link = fmt.Sprintf("/p/%d/data/%s?version=%d", projectID,
					studio.SampleResourceID(version.SampleID), version.Version)
			}
		}
		blockers = append(blockers, item)
	}
	return blockers
}

// listReleases 列出项目的发布（L01）。
func (app *application) listReleases(w http.ResponseWriter, r *http.Request) {
	_, projectID, _, ok := app.authorizeRelease(w, r, store.AuthzRead)
	if !ok {
		return
	}
	items, err := app.studio.Releases.ListReleases(r.Context(), projectID, 50)
	if err != nil {
		app.writeStudioError(w, r, err)
		return
	}
	app.writeJSON(w, http.StatusOK, map[string]any{
		"items": items, "nextCursor": "", "sortKey": "createdAt:desc",
	})
}

// publishRelease 冻结并发布（契约 §2.9，202 + building）。
//
// 幂等：相同命令返回同一个 release（重复点击不会产生第二个发布）。
func (app *application) publishRelease(w http.ResponseWriter, r *http.Request) {
	user, projectID, role, ok := app.authorizeRelease(w, r, store.AuthzPublish)
	if !ok {
		return
	}
	releaseID, ok := app.parseReleaseID(w, r)
	if !ok {
		return
	}
	// 先冻结（写不可变清单 + outbox + building），再由发布作业产出制品并发布。
	frozen, err := app.studio.Releases.FreezeRelease(r.Context(), projectID, releaseID, user.ID)
	if err != nil {
		app.writeReleaseError(w, r, err)
		return
	}
	app.writeReleaseEnvelope(w, r.Context(), http.StatusAccepted, projectID, frozen, role)
}

// createNextCandidate 从已发布版本创建下一版候选（契约 §2.10）。
//
// **不改原版**：新候选是独立的 releaseId + 版本名，原版的文件与数据卡保持只读。
func (app *application) createNextCandidate(w http.ResponseWriter, r *http.Request) {
	user, projectID, role, ok := app.authorizeRelease(w, r, store.AuthzPublish)
	if !ok {
		return
	}
	releaseID, ok := app.parseReleaseID(w, r)
	if !ok {
		return
	}
	previous, err := app.studio.Releases.GetRelease(r.Context(), projectID, releaseID)
	if err != nil {
		app.writeReleaseError(w, r, err)
		return
	}
	if previous.Status != model.ReleaseStatusPublished {
		app.writeStudioError(w, r, studio.NewValidationError(
			"只有已发布的版本才能创建下一版",
			[]model.FieldError{{Field: "status", Message: "当前状态不能创建下一版"}}))
		return
	}

	// 复制清单与配置，但**新版本名**（调用方给或自动 +1）。
	var request struct {
		ReleaseName string `json:"releaseName"`
	}
	_ = json.NewDecoder(r.Body).Decode(&request)
	name := strings.TrimSpace(request.ReleaseName)
	if name == "" {
		name = nextReleaseName(previous.ReleaseName)
	}

	items, err := app.studio.Releases.ListReleaseItems(r.Context(), releaseID, previous.CandidateRevision, 200_000)
	if err != nil {
		app.writeStudioError(w, r, err)
		return
	}
	versionIDs := make([]int64, 0, len(items))
	for _, item := range items {
		if strings.TrimSpace(item.ExcludedReason) != "" {
			continue
		}
		versionIDs = append(versionIDs, item.SampleVersionID)
	}
	mappingVersionID := int64(0)
	if previous.MappingVersionID != nil {
		mappingVersionID = *previous.MappingVersionID
	}
	next, err := app.studio.Releases.CreateReleaseCandidate(r.Context(), store.CreateReleaseCandidateInput{
		ProjectID: projectID, ReleaseName: name, MappingVersionID: mappingVersionID,
		Format: previous.Format, IntendedUse: previous.IntendedUse,
		Limitations: previous.Limitations, SampleVersionIDs: versionIDs,
		CreatedBy: &user.ID,
	})
	if err != nil {
		app.writeReleaseError(w, r, err)
		return
	}
	app.writeReleaseEnvelope(w, r.Context(), http.StatusCreated, projectID, next, role)
}

// releaseNextCandidateLinks 给出「创建下一版」的入口信息（页面链接，不自行拼 URL）。
func (app *application) releaseNextCandidateLinks(w http.ResponseWriter, r *http.Request) {
	_, projectID, _, ok := app.authorizeRelease(w, r, store.AuthzRead)
	if !ok {
		return
	}
	releaseID, ok := app.parseReleaseID(w, r)
	if !ok {
		return
	}
	release, err := app.studio.Releases.GetRelease(r.Context(), projectID, releaseID)
	if err != nil {
		app.writeReleaseError(w, r, err)
		return
	}
	app.writeJSON(w, http.StatusOK, map[string]any{
		"releaseId": release.ID,
		// 页面链接由服务端给出（前端不自行拼 URL，契约 §1.1）。
		"page":          "/p/" + strconv.FormatInt(projectID, 10) + "/releases/new",
		"suggestedName": nextReleaseName(release.ReleaseName),
	})
}

// downloadReleaseArtifact 下载不可变制品（契约 §2.10）。
//
// 三条要求：
//   - **按 releaseId + artifactId 定位**，不用版本名，也不允许 latest 回退；
//   - 读的是**制品记录里固化的 endpoint/bucket**（凭证从当前配置取，
//     因为凭证会轮换，而文件不会移动）；
//   - 文件名含类型与版本名，但**不叫 latest**。
func (app *application) downloadReleaseArtifact(w http.ResponseWriter, r *http.Request) {
	_, projectID, _, ok := app.authorizeRelease(w, r, store.AuthzRead)
	if !ok {
		return
	}
	releaseID, ok := app.parseReleaseID(w, r)
	if !ok {
		return
	}
	artifactID, err := strconv.ParseInt(r.PathValue("artifactId"), 10, 64)
	if err != nil || artifactID <= 0 {
		app.writeStudioError(w, r, studio.NewError(studio.CodeNotFound, "未找到该交付文件"))
		return
	}

	release, err := app.studio.Releases.GetRelease(r.Context(), projectID, releaseID)
	if err != nil {
		app.writeReleaseError(w, r, err)
		return
	}
	artifact, err := app.studio.ReleaseArtifacts.GetArtifact(r.Context(), artifactID)
	if err != nil {
		app.writeReleaseError(w, r, err)
		return
	}
	// 制品必须属于这个 release：路径里的 releaseId 不能被绕过
	//（否则可以用任意 releaseId 下载另一个发布的文件）。
	if artifact.ReleaseID != releaseID {
		app.writeStudioError(w, r, studio.NewError(studio.CodeNotFound, "未找到该交付文件"))
		return
	}
	// 只有**已校验**的制品可下载：未校验意味着「对象可能没写成功」。
	if artifact.State != model.ArtifactStateVerified {
		app.writeStudioError(w, r, studio.NewError(studio.CodeConflict,
			"该文件尚未校验完成，暂时不能下载（请稍后刷新）"))
		return
	}

	// 凭证从当前配置取（会轮换），而 endpoint/bucket 用制品固化的值
	//（切换默认存储不该让旧文件找不到）。
	_, region, _, accessKeyID, secretKey, usePathStyle, err := app.datasets.ResolveStorageProfile(r.Context(), 0)
	if err != nil {
		app.writeStudioError(w, r, studio.NewError(studio.CodeUnavailable,
			"存储配置不可用，暂时无法下载交付文件"))
		return
	}
	objectStore, err := storage.New(storage.Profile{
		Endpoint: firstNonEmptyString(artifact.StorageEndpoint, ""),
		Region:   region, Bucket: firstNonEmptyString(artifact.StorageBucket, ""),
		AccessKeyID: accessKeyID, SecretKey: secretKey, UsePathStyle: usePathStyle,
	})
	if err != nil {
		app.writeStudioError(w, r, studio.NewError(studio.CodeUnavailable,
			"存储连接失败，暂时无法下载交付文件"))
		return
	}

	content, err := objectStore.ReadBytes(r.Context(), artifact.ObjectKey)
	if err != nil {
		// 文件丢失/损坏必须**明确报错**，不回退导出最新（T21/T22 的共同要求）。
		app.writeStudioError(w, r, studio.NewError(studio.CodeNotFound,
			"交付文件在对象存储中不存在或不可读；请联系管理员按同一内容 hash 受控重建"))
		return
	}
	// 下载前校验字节：hash 不符说明文件被改过或损坏。
	if got := model.ComputeArtifactHash(content); !strings.EqualFold(got, artifact.ArtifactHash) {
		app.writeStudioError(w, r, studio.NewError(studio.CodeConflict,
			"交付文件内容与发布记录不符（hash 校验失败），已拒绝下载；请联系管理员核查"))
		return
	}

	// 文件名含类型与版本名，**不含 latest**。
	filename := fmt.Sprintf("%s-%s.%s", sanitizeFilename(release.ReleaseName),
		sanitizeFilename(release.Format), extensionFor(release.Format))
	w.Header().Set("Content-Type", artifact.ContentType)
	w.Header().Set("Content-Length", strconv.FormatInt(int64(len(content)), 10))
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", filename))
	// 让调用方能核对字节（与发布记录一致）。
	w.Header().Set("X-Artifact-Hash", artifact.ArtifactHash)
	if _, err := io.Copy(w, strings.NewReader(string(content))); err != nil {
		// 响应已开始写出，无法再改状态码；只记录。
		app.logInternal(r, "artifact download interrupted", err)
	}
}

// listDeliveries 是交付库（契约 §3 的 B03）。
//
// **只含已发布且用户可访问的版本**：候选不是交付物，不能出现在这里
// （否则用户会下载到一个尚未确认的文件）。
func (app *application) listDeliveries(w http.ResponseWriter, r *http.Request) {
	user, ok := requestUser(r)
	if !ok {
		app.writeStudioError(w, r, studio.NewError(studio.CodeUnauthorized, msgAuthRequired))
		return
	}
	projectIDs, err := app.projects.ProjectIDsForUser(r.Context(), user.ID)
	if err != nil {
		app.writeStudioError(w, r, err)
		return
	}
	query, err := studio.ParseListQuery(r.URL.Query(), 20)
	if err != nil {
		app.writeStudioError(w, r, err)
		return
	}

	items := []map[string]any{}
	for _, projectID := range projectIDs {
		releases, err := app.studio.Releases.ListReleases(r.Context(), projectID, 50)
		if err != nil {
			app.writeStudioError(w, r, err)
			return
		}
		for _, release := range releases {
			if release.Status != model.ReleaseStatusPublished {
				continue // 候选不是交付物
			}
			// 搜索按用途/版本名（服务端口径，不是前端过滤）。
			if search := strings.TrimSpace(query.Search); search != "" {
				haystack := strings.ToLower(release.ReleaseName + " " + release.IntendedUse)
				if !strings.Contains(haystack, strings.ToLower(search)) {
					continue
				}
			}
			items = append(items, map[string]any{
				"releaseId": release.ID, "projectId": release.ProjectID,
				"releaseName": release.ReleaseName, "status": release.Status,
				"intendedUse": release.IntendedUse, "format": release.Format,
				"targetKind": release.TargetKind, "publishedAt": release.PublishedAt,
				"page": "/p/" + strconv.FormatInt(release.ProjectID, 10) +
					"/releases/" + strconv.FormatInt(release.ID, 10),
			})
		}
	}
	app.writeJSON(w, http.StatusOK, map[string]any{
		"items": items, "nextCursor": "", "sortKey": "publishedAt:desc",
	})
}

// ---------------------------------------------------------------------------
// 辅助
// ---------------------------------------------------------------------------

// authorizeRelease 做发布侧的统一授权（读/发布两种动作）。
func (app *application) authorizeRelease(w http.ResponseWriter, r *http.Request, action store.AuthzAction) (model.User, int64, string, bool) {
	user, ok := requestUser(r)
	if !ok {
		app.writeStudioError(w, r, studio.NewError(studio.CodeUnauthorized, msgAuthRequired))
		return model.User{}, 0, "", false
	}
	projectID, err := parseProjectID(r.PathValue("projectId"))
	if err != nil {
		app.writeStudioError(w, r, studio.NewError(studio.CodeNotFound, msgProjectNotFound))
		return model.User{}, 0, "", false
	}
	decision, err := app.studio.Authorize(r.Context(), projectID, user.ID, action)
	if err != nil {
		// viewer 直接调发布 API 会走到这里 → 403/404。
		// **不依赖界面隐藏按钮**（契约 §4：前端禁用按钮不构成安全边界）。
		app.writeStudioError(w, r, err)
		return model.User{}, 0, "", false
	}
	return user, projectID, decision.Role, true
}

// parseReleaseID 解析路径里的稳定 releaseId。
func (app *application) parseReleaseID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	releaseID, err := strconv.ParseInt(r.PathValue("releaseId"), 10, 64)
	if err != nil || releaseID <= 0 {
		app.writeStudioError(w, r, studio.NewError(studio.CodeNotFound, "未找到该发布版本"))
		return 0, false
	}
	return releaseID, true
}

// writeReleaseEnvelope 写一个发布响应。
func (app *application) writeReleaseEnvelope(w http.ResponseWriter, ctx context.Context, status int, projectID int64, release store.Release, role string) {
	envelope := studio.NewEnvelope(
		strconv.FormatInt(release.ID, 10), release.Status, release.CandidateRevision,
		release.UpdatedAt, model.ReleaseCapabilities(role, release.Status),
		releaseLinks(projectID, release.ID), nil)
	envelope.Data = struct {
		Release  store.Release    `json:"release"`
		Blockers []studio.Blocker `json:"blockers"`
		Warnings []string         `json:"warnings"`
	}{
		Release: release, Blockers: app.releaseBlockers(ctx, projectID, release),
		Warnings: releaseWarnings(release),
	}
	app.writeStudioEnvelope(w, status, envelope)
}

// releaseWarnings 给出非阻塞提示。
//
// 「已发布只读」必须显式说明：用户改完项目配置回来会以为文件也跟着变了，
// 而事实是旧文件必须保持一致（§2.4）。
func releaseWarnings(release store.Release) []string {
	warnings := []string{}
	if release.Status == model.ReleaseStatusPublished {
		warnings = append(warnings,
			"该版本已发布：内容、映射、数据卡与 hash 只读；之后修改项目配置或隔离样本都不会改变这些文件")
	}
	if release.Status == model.ReleaseStatusBuildFailed {
		warnings = append(warnings,
			"上次构建设失败：直接再次发布即可幂等续接（不会更换发布身份，也不会重复产出文件）")
	}
	if release.Status == model.ReleaseStatusBuilding {
		warnings = append(warnings, "正在构建制品：完成后才会出现在交付库中")
	}
	return warnings
}

// releaseLinks 是发布对象可跳转的链接。
func releaseLinks(projectID, releaseID int64) studio.Links {
	base := projectPrefix + "/" + strconv.FormatInt(projectID, 10) +
		"/releases/" + strconv.FormatInt(releaseID, 10)
	return studio.Links{
		"self":           base,
		"project":        projectPrefix + "/" + strconv.FormatInt(projectID, 10),
		"page":           "/p/" + strconv.FormatInt(projectID, 10) + "/releases/" + strconv.FormatInt(releaseID, 10),
		"publish":        base + "/publish",
		"next-candidate": base + "/next-candidate",
	}
}

// writeReleaseError 把发布侧错误翻译成契约错误码。
func (app *application) writeReleaseError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, store.ErrReleaseNameTaken):
		app.writeAPIError(w, r, http.StatusConflict, studio.CodeConflict, err.Error(), nil)
	case errors.Is(err, store.ErrReleaseGateBlocked):
		app.writeAPIError(w, r, http.StatusConflict, studio.CodeConflict,
			"发布门槛未通过：请按数据卡里的阻塞项修正后重新确认", nil)
	case errors.Is(err, store.ErrReleaseRevisionStale):
		app.writeAPIError(w, r, http.StatusConflict, studio.CodeRevisionStale, err.Error(), nil)
	case errors.Is(err, store.ErrReleasePublished):
		app.writeAPIError(w, r, http.StatusConflict, studio.CodeConflict, err.Error(), nil)
	case errors.Is(err, store.ErrReleaseNotFound), errors.Is(err, store.ErrArtifactNotFound):
		app.writeAPIError(w, r, http.StatusNotFound, studio.CodeNotFound,
			"未找到该发布版本或交付文件", nil)
	case errors.Is(err, pgx.ErrNoRows):
		app.writeAPIError(w, r, http.StatusNotFound, studio.CodeNotFound, msgProjectNotFound, nil)
	default:
		app.writeStudioError(w, r, err)
	}
}

// nextReleaseName 由当前版本名推导下一个（v1.2 → v1.3）。
//
// 推导失败时返回一个带时间戳的名字而不是空串：空名字会让创建下一版
// 直接以「版本名必填」失败，而用户只是想点一下「下一版」。
func nextReleaseName(current string) string {
	trimmed := strings.TrimSpace(current)
	index := strings.LastIndex(trimmed, ".")
	if index > 0 && index < len(trimmed)-1 {
		suffix := trimmed[index+1:]
		if number, err := strconv.Atoi(suffix); err == nil {
			return trimmed[:index+1] + strconv.Itoa(number+1)
		}
	}
	return trimmed + "-next"
}

// sanitizeFilename 去掉文件名里不安全的字符。
func sanitizeFilename(value string) string {
	replacer := strings.NewReplacer("/", "_", "\\", "_", ":", "_", "*", "_",
		"?", "_", "\"", "_", "<", "_", ">", "_", "|", "_", " ", "_")
	sanitized := strings.Trim(replacer.Replace(strings.TrimSpace(value)), "_")
	if sanitized == "" {
		return "release"
	}
	return sanitized
}

// extensionFor 返回格式对应的扩展名。
func extensionFor(format string) string {
	switch strings.ToLower(format) {
	case "csv":
		return "csv"
	case "alpaca", "jsonl", "json":
		return "jsonl"
	default:
		return "dat"
	}
}

func firstNonEmptyString(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
