package store

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/1420970597/llm/internal/model"
)

// 本文件验证 Issue #160 T21 的制品登记与发布判定。
//
// 必须连真实 Postgres：核心断言是「重复消息只有一份有效发布」
// 与「未确认的对象永不导致 published」——都是唯一约束与事务语义。

type artifactFixture struct {
	releaseFixture
	artifacts *ReleaseArtifactStore
	release   Release
}

// newArtifactFixture 建一个**通过门槛**的发布候选。
func newArtifactFixture(t *testing.T) artifactFixture {
	t.Helper()
	base := newReleaseFixture(t)
	ctx := context.Background()
	release, err := base.releases.CreateReleaseCandidate(ctx,
		base.candidateInput("v1.0", base.versionIDs))
	if err != nil {
		t.Fatalf("CreateReleaseCandidate: %v", err)
	}
	if _, err := base.releases.FreezeRelease(ctx, base.projectID, release.ID, base.userID); err != nil {
		t.Fatalf("FreezeRelease: %v", err)
	}
	frozen, err := base.releases.GetRelease(ctx, base.projectID, release.ID)
	if err != nil {
		t.Fatalf("GetRelease: %v", err)
	}
	return artifactFixture{
		releaseFixture: base, artifacts: NewReleaseArtifactStore(base.pool), release: frozen,
	}
}

// manifestFor 用该发布的真实清单构造 manifest。
func (fixture artifactFixture) manifestFor(t *testing.T) model.ReleaseManifest {
	t.Helper()
	items, err := fixture.releases.ListReleaseItems(context.Background(), fixture.release.ID,
		fixture.release.CandidateRevision, 100)
	if err != nil {
		t.Fatalf("ListReleaseItems: %v", err)
	}
	manifestItems := make([]model.ManifestItem, 0, len(items))
	for _, item := range items {
		manifestItems = append(manifestItems, model.ManifestItem{
			SampleID: item.SampleID, SampleVersionID: item.SampleVersionID,
			ContentHash:          item.ContentHash,
			StandardContentHash:  item.StandardContentHash,
			BlueprintContentHash: item.BlueprintContentHash,
		})
	}
	manifest := model.ReleaseManifest{
		ReleaseID: fixture.release.ID, Revision: fixture.release.CandidateRevision,
		ReleaseName: fixture.release.ReleaseName, TargetKind: model.TargetKindSFT,
		Format: "jsonl", MappingVersionID: 0, EncoderVersion: "exporter-v1",
		IntendedUse: "SFT 训练", Limitations: []string{"仅覆盖冷链领域"},
		Items: manifestItems, HashScope: model.HashScopeReleaseItems,
	}
	manifest.ItemsContentHash = model.ComputeItemsContentHash(manifestItems)
	// ItemCount 由规范化派生（与 CanonicalManifestBytes 的规则一致）。
	if raw, err := model.CanonicalManifestBytes(manifest); err == nil {
		_ = raw
	}
	return manifest
}

// uploadFor 构造一次上传登记。
func (fixture artifactFixture) uploadFor(manifest model.ReleaseManifest, payload string) ArtifactUpload {
	hash := model.ComputeArtifactHash([]byte(payload))
	return ArtifactUpload{
		ReleaseID: fixture.release.ID, Revision: fixture.release.CandidateRevision,
		ArtifactType: "export", Format: "jsonl",
		ObjectKey: model.ArtifactObjectKey(fixture.release.ID, fixture.release.CandidateRevision, "jsonl", hash),
		// 存储身份固化：即使之后切换默认存储，这份记录仍指向当时的 endpoint/bucket。
		StorageEndpoint: "minio.internal:9000", StorageBucket: "llm-factory-dev",
		SizeBytes: int64(len(payload)), ContentType: "application/jsonl",
		ArtifactHash: hash, ItemsContentHash: manifest.ItemsContentHash,
		EncoderVersion: "exporter-v1", UploadedBy: &fixture.userID,
	}
}

// TestRegisterManifestIsIdempotent 覆盖「重放得到同一份 manifest」。
func TestRegisterManifestIsIdempotent(t *testing.T) {
	fixture := newArtifactFixture(t)
	ctx := context.Background()
	manifest := fixture.manifestFor(t)

	firstID, firstHash, err := fixture.artifacts.RegisterManifest(ctx, manifest, &fixture.userID)
	if err != nil {
		t.Fatalf("RegisterManifest: %v", err)
	}
	if firstHash == "" || !strings.HasPrefix(firstHash, "sha256:") {
		t.Fatalf("manifest hash 必须带算法前缀，实际 %q", firstHash)
	}
	// 重放：同一修订返回既有记录与**同一 hash**。
	secondID, secondHash, err := fixture.artifacts.RegisterManifest(ctx, manifest, &fixture.userID)
	if err != nil {
		t.Fatalf("重复 RegisterManifest: %v", err)
	}
	if secondID != firstID {
		t.Fatalf("重复登记必须返回同一记录：%d vs %d", firstID, secondID)
	}
	if secondHash != firstHash {
		t.Fatalf("重复登记必须得到同一 hash：%s vs %s", firstHash, secondHash)
	}

	// 清单顺序变化后 hash 仍应相同（规范化生效）。
	reordered := manifest
	reordered.Items = append([]model.ManifestItem(nil), manifest.Items...)
	for i, j := 0, len(reordered.Items)-1; i < j; i, j = i+1, j-1 {
		reordered.Items[i], reordered.Items[j] = reordered.Items[j], reordered.Items[i]
	}
	reorderedHash, err := model.ComputeManifestHash(reordered)
	if err != nil {
		t.Fatalf("ComputeManifestHash: %v", err)
	}
	if reorderedHash != firstHash {
		t.Fatalf("清单顺序不得影响 manifest hash：%s vs %s", reorderedHash, firstHash)
	}
}

// TestRegisterArtifactReplaysSameHashAndRejectsDifferentHash 覆盖验收项
// 「重试/重复消息只有一份有效发布」。
func TestRegisterArtifactReplaysSameHashAndRejectsDifferentHash(t *testing.T) {
	fixture := newArtifactFixture(t)
	ctx := context.Background()
	manifest := fixture.manifestFor(t)
	if _, _, err := fixture.artifacts.RegisterManifest(ctx, manifest, &fixture.userID); err != nil {
		t.Fatalf("RegisterManifest: %v", err)
	}

	upload := fixture.uploadFor(manifest, `{"question":"q"}`+"\n")
	first, err := fixture.artifacts.RegisterArtifact(ctx, upload)
	if err != nil {
		t.Fatalf("RegisterArtifact: %v", err)
	}
	if first.Replayed {
		t.Fatal("首次登记不应标记为重放")
	}
	if first.Artifact.State != model.ArtifactStateRegistered {
		t.Fatalf("新登记应为 registered（未校验不得当可发布），实际 %s", first.Artifact.State)
	}
	// 存储身份必须固化下来。
	if first.Artifact.StorageEndpoint == "" || first.Artifact.StorageBucket == "" {
		t.Fatal("制品必须固化存储身份（endpoint/bucket），否则切换默认存储无法定位对象")
	}

	// 同 hash 重放：返回既有行，不新增。
	replay, err := fixture.artifacts.RegisterArtifact(ctx, upload)
	if err != nil {
		t.Fatalf("同 hash 重放: %v", err)
	}
	if !replay.Replayed || replay.Artifact.ID != first.Artifact.ID {
		t.Fatalf("同 hash 必须回放既有制品，实际 replayed=%v id=%d", replay.Replayed, replay.Artifact.ID)
	}
	var count int
	if err := fixture.pool.QueryRow(ctx, `
    SELECT COUNT(*) FROM release_artifacts WHERE release_id = $1`, fixture.release.ID).Scan(&count); err != nil {
		t.Fatalf("count artifacts: %v", err)
	}
	if count != 1 {
		t.Fatalf("重复登记不得产生第二份制品，实际 %d 份", count)
	}

	// 确认之后：不同 hash 的登记必须被拒绝（已发布文件不可变）。
	if err := fixture.artifacts.MarkArtifactVerified(ctx, first.Artifact.ID); err != nil {
		t.Fatalf("MarkArtifactVerified: %v", err)
	}
	different := fixture.uploadFor(manifest, `{"question":"changed"}`+"\n")
	if _, err := fixture.artifacts.RegisterArtifact(ctx, different); err == nil {
		t.Fatal("已确认制品不得被不同内容覆盖")
	}
}

// TestRegisterArtifactAllowsReplacingUnverifiedAttempt 覆盖「重试路径」。
//
// 未确认（registered/failed）的制品允许被新的尝试替换 —— 否则一次失败的
// 上传会让这个格式永远无法再试。
func TestRegisterArtifactAllowsReplacingUnverifiedAttempt(t *testing.T) {
	fixture := newArtifactFixture(t)
	ctx := context.Background()
	manifest := fixture.manifestFor(t)
	if _, _, err := fixture.artifacts.RegisterManifest(ctx, manifest, &fixture.userID); err != nil {
		t.Fatalf("RegisterManifest: %v", err)
	}

	first, err := fixture.artifacts.RegisterArtifact(ctx, fixture.uploadFor(manifest, "attempt-1"))
	if err != nil {
		t.Fatalf("RegisterArtifact: %v", err)
	}
	if err := fixture.artifacts.MarkArtifactFailed(ctx, first.Artifact.ID, model.ErrorClassTimeout, "上传超时"); err != nil {
		t.Fatalf("MarkArtifactFailed: %v", err)
	}

	second, err := fixture.artifacts.RegisterArtifact(ctx, fixture.uploadFor(manifest, "attempt-2"))
	if err != nil {
		t.Fatalf("失败后重试登记: %v", err)
	}
	if second.Replayed {
		t.Fatal("新内容的重试不应标记为重放")
	}
	if second.Artifact.ArtifactHash == first.Artifact.ArtifactHash {
		t.Fatal("重试的内容 hash 应与失败尝试不同")
	}
	if second.Artifact.State != model.ArtifactStateRegistered {
		t.Fatalf("重试后应回到 registered，实际 %s", second.Artifact.State)
	}

	// 按 hash 找既有制品：幂等续接的入口。
	found, ok, err := fixture.artifacts.FindArtifactByHash(ctx, fixture.release.ID, second.Artifact.ArtifactHash)
	if err != nil || !ok {
		t.Fatalf("FindArtifactByHash: ok=%v err=%v", ok, err)
	}
	if found.ID != second.Artifact.ID {
		t.Fatalf("按 hash 必须找到刚登记的制品：%d vs %d", found.ID, second.Artifact.ID)
	}
}

// TestPublishReleaseIfReadyRequiresVerifiedArtifact 覆盖验收项
// 「上传超时、存储不足、DB 失败均不显示成功」。
//
// 未校验的制品**永不**导致 published —— 这是本文件最重要的一条。
func TestPublishReleaseIfReadyRequiresVerifiedArtifact(t *testing.T) {
	fixture := newArtifactFixture(t)
	ctx := context.Background()
	manifest := fixture.manifestFor(t)

	// 1) 没有 manifest：不得发布。
	published, reason, err := fixture.artifacts.PublishReleaseIfReady(ctx, fixture.release.ID,
		fixture.release.CandidateRevision)
	if err != nil {
		t.Fatalf("PublishReleaseIfReady: %v", err)
	}
	if published {
		t.Fatal("没有 manifest 不得标记为已发布")
	}
	if !strings.Contains(reason, "manifest") {
		t.Fatalf("原因必须说明缺 manifest，实际 %q", reason)
	}

	// 2) 有 manifest 但制品未校验：不得发布。
	if _, _, err := fixture.artifacts.RegisterManifest(ctx, manifest, &fixture.userID); err != nil {
		t.Fatalf("RegisterManifest: %v", err)
	}
	registered, err := fixture.artifacts.RegisterArtifact(ctx, fixture.uploadFor(manifest, "content"))
	if err != nil {
		t.Fatalf("RegisterArtifact: %v", err)
	}
	published, reason, err = fixture.artifacts.PublishReleaseIfReady(ctx, fixture.release.ID,
		fixture.release.CandidateRevision)
	if err != nil {
		t.Fatalf("PublishReleaseIfReady: %v", err)
	}
	if published {
		t.Fatal("未校验的制品不得导致 published（上传超时不得显示成功）")
	}
	if !strings.Contains(reason, "已校验") {
		t.Fatalf("原因必须说明缺已校验制品，实际 %q", reason)
	}

	// 3) 校验后：发布成功。
	if err := fixture.artifacts.MarkArtifactVerified(ctx, registered.Artifact.ID); err != nil {
		t.Fatalf("MarkArtifactVerified: %v", err)
	}
	published, reason, err = fixture.artifacts.PublishReleaseIfReady(ctx, fixture.release.ID,
		fixture.release.CandidateRevision)
	if err != nil {
		t.Fatalf("PublishReleaseIfReady: %v", err)
	}
	if !published {
		t.Fatalf("全部满足时必须发布，实际 reason=%q", reason)
	}
	reloaded, err := fixture.releases.GetRelease(ctx, fixture.projectID, fixture.release.ID)
	if err != nil {
		t.Fatalf("GetRelease: %v", err)
	}
	if reloaded.Status != model.ReleaseStatusPublished {
		t.Fatalf("状态应为 published，实际 %s", reloaded.Status)
	}
	if reloaded.PublishedAt == nil {
		t.Fatal("published 必须记录发布时间")
	}

	// 4) 幂等：再次调用返回已发布而不是报错（相同命令返回同一个 release）。
	published, _, err = fixture.artifacts.PublishReleaseIfReady(ctx, fixture.release.ID,
		fixture.release.CandidateRevision)
	if err != nil || !published {
		t.Fatalf("重复发布必须幂等成功，实际 published=%v err=%v", published, err)
	}
}

// TestArtifactStateTransitions 覆盖状态机与失败信息保留。
func TestArtifactStateTransitions(t *testing.T) {
	fixture := newArtifactFixture(t)
	ctx := context.Background()
	manifest := fixture.manifestFor(t)
	if _, _, err := fixture.artifacts.RegisterManifest(ctx, manifest, &fixture.userID); err != nil {
		t.Fatalf("RegisterManifest: %v", err)
	}
	registered, err := fixture.artifacts.RegisterArtifact(ctx, fixture.uploadFor(manifest, "x"))
	if err != nil {
		t.Fatalf("RegisterArtifact: %v", err)
	}

	if err := fixture.artifacts.MarkArtifactFailed(ctx, registered.Artifact.ID,
		model.ErrorClassProvider, "存储返回 503"); err != nil {
		t.Fatalf("MarkArtifactFailed: %v", err)
	}
	items, err := fixture.artifacts.ListArtifacts(ctx, fixture.release.ID, fixture.release.CandidateRevision)
	if err != nil {
		t.Fatalf("ListArtifacts: %v", err)
	}
	if len(items) != 1 || items[0].State != model.ArtifactStateFailed {
		t.Fatalf("应为 failed，实际 %+v", items)
	}
	if items[0].ErrorClass != model.ErrorClassProvider || items[0].ErrorMessage == "" {
		t.Fatal("失败必须保留错误类别与可读原因（供重试与排障）")
	}

	// 不存在的制品。
	if err := fixture.artifacts.MarkArtifactVerified(ctx, 1<<62); !errors.Is(err, ErrArtifactNotFound) {
		t.Fatalf("不存在的制品必须返回 ErrArtifactNotFound，实际 %v", err)
	}
	if _, err := fixture.artifacts.GetArtifact(ctx, 1<<62); !errors.Is(err, ErrArtifactNotFound) {
		t.Fatalf("GetArtifact 不存在时必须返回 ErrArtifactNotFound，实际 %v", err)
	}
}

// TestManifestAndArtifactHashesAreSeparate 覆盖「hash 分层且互不包含」。
func TestManifestAndArtifactHashesAreSeparate(t *testing.T) {
	fixture := newArtifactFixture(t)
	ctx := context.Background()
	manifest := fixture.manifestFor(t)
	_, manifestHash, err := fixture.artifacts.RegisterManifest(ctx, manifest, &fixture.userID)
	if err != nil {
		t.Fatalf("RegisterManifest: %v", err)
	}
	upload := fixture.uploadFor(manifest, "payload")
	registered, err := fixture.artifacts.RegisterArtifact(ctx, upload)
	if err != nil {
		t.Fatalf("RegisterArtifact: %v", err)
	}

	if registered.Artifact.ArtifactHash == manifestHash {
		t.Fatal("artifact hash（文件字节）与 manifest hash（元数据）必须是两个不同的值")
	}
	if registered.Artifact.ItemsContentHash != manifest.ItemsContentHash {
		t.Fatalf("制品的清单 hash 必须与 manifest 一致：%s vs %s",
			registered.Artifact.ItemsContentHash, manifest.ItemsContentHash)
	}
	// 读回 manifest：hash 必须可复算（发布后能验证文件对应哪份清单）。
	loaded, loadedHash, err := fixture.artifacts.GetManifest(ctx, fixture.release.ID,
		fixture.release.CandidateRevision)
	if err != nil {
		t.Fatalf("GetManifest: %v", err)
	}
	if loadedHash != manifestHash {
		t.Fatalf("读回的 manifest hash 必须一致：%s vs %s", loadedHash, manifestHash)
	}
	recomputed, err := model.ComputeManifestHash(loaded)
	if err != nil {
		t.Fatalf("ComputeManifestHash: %v", err)
	}
	if recomputed != loadedHash {
		t.Fatalf("manifest hash 必须可复算：%s vs %s", recomputed, loadedHash)
	}
}
