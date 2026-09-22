package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/1420970597/llm/internal/model"
	"github.com/1420970597/llm/internal/storage"
	"github.com/1420970597/llm/internal/store"
	"github.com/1420970597/llm/internal/studio"
)

// 本文件把发布作业接到 Studio 作业系统上（Issue #160 T21）。
//
// 契约：docs/plans/atelier-api-contract.md §2.9；internal/studio/release_build.go。
//
// 执行顺序**不可调换**（T21 的核心要求）：
//
//	读冻结清单 → 编码算 hash → 写对象（同 hash 路径，幂等）
//	→ **读回校验** size/hash/存在 → 登记制品与 manifest → 判定可否 published
//
// 其中「读回校验」是必需的：上传接口返回成功不等于对象真的写成功了
//（超时、存储不足、代理截断都会让 PUT 看起来成功）。而 DB 只在制品
// **确认之后**才可能发布 —— 因此「未确认的对象导致 published」在结构上不可能。

func init() {
	RegisterStudioJobHandler(model.JobKindReleaseBuild, handleReleaseBuild)
}

// handleReleaseBuild 执行一次发布作业。
func handleReleaseBuild(ctx context.Context, env *StudioJobEnv, job model.Job) (any, error) {
	releaseID := int64(0)
	if job.Payload != nil {
		var payload struct {
			ReleaseID int64 `json:"releaseId"`
			Revision  int64 `json:"revision"`
		}
		if err := json.Unmarshal(job.Payload, &payload); err == nil {
			releaseID = payload.ReleaseID
		}
	}
	if releaseID <= 0 {
		return nil, fmt.Errorf("发布作业缺少 releaseId（作业创建路径异常）")
	}

	releases := env.Releases()
	artifacts := env.ReleaseArtifacts()
	datasets := env.datasetStore()
	if datasets == nil {
		return nil, fmt.Errorf("worker 未注入 provider 解析依赖，无法执行发布")
	}

	release, err := releases.GetRelease(ctx, 0, releaseID)
	if err != nil {
		// GetRelease 需要项目作用域，而作业里只有 releaseId：
		// 用 0 查不到时从 releases 表按 ID 反查项目（发布作业是系统级动作，
		// 不经过用户授权路径）。
		release, err = releases.GetReleaseByID(ctx, releaseID)
		if err != nil {
			return nil, err
		}
	}
	// 幂等：已发布直接返回（相同命令返回同一个 release，§2.9）。
	if release.Status == model.ReleaseStatusPublished {
		return map[string]any{"releaseId": releaseID, "status": "published", "replayed": true}, nil
	}

	revision := release.CandidateRevision
	items, err := releases.ListReleaseItems(ctx, releaseID, revision, 200_000)
	if err != nil {
		return nil, err
	}
	if len(items) == 0 {
		return nil, fmt.Errorf("发布清单为空：不能产出有效版本（请重新确认候选范围）")
	}

	// 映射版本：取候选冻结的那一个（不是「当前默认映射」）。
	mappingVersionID := int64(0)
	if release.MappingVersionID != nil {
		mappingVersionID = *release.MappingVersionID
	}

	builder := &studio.ReleaseBuilder{Batches: env.Batches(), Documents: env.Documents()}
	built, err := builder.BuildReleaseArtifact(ctx, studio.ReleaseBuildInput{
		ProjectID: release.ProjectID, ReleaseID: release.ID, Revision: revision,
		ReleaseName: release.ReleaseName, TargetKind: release.TargetKind,
		Format: release.Format, IntendedUse: release.IntendedUse,
		Limitations:      release.Limitations,
		Provenance:       decodeJSONMap(release.Provenance),
		MappingVersionID: mappingVersionID,
		Items:            items,
		QualitySnapshot:  release.QualitySnapshot,
		CoverageSummary:  release.CoverageSummary,
	})
	if err != nil {
		return nil, err
	}

	// 存储身份在**写入时**固化，之后切换默认存储不影响这份文件。
	endpoint, region, bucket, accessKeyID, secretKey, usePathStyle, err :=
		datasets.ResolveStorageProfile(ctx, 0)
	if err != nil {
		return nil, fmt.Errorf("解析存储配置失败：%w", err)
	}
	objectStore, err := storage.New(storage.Profile{
		Endpoint: endpoint, Region: region, Bucket: bucket,
		AccessKeyID: accessKeyID, SecretKey: secretKey, UsePathStyle: usePathStyle,
	})
	if err != nil {
		return nil, err
	}

	objectKey := model.ArtifactObjectKey(release.ID, revision, release.Format, built.ArtifactHash)

	// **先查既有制品**：上传成功但 DB 失败时，重试会命中这里而不是重传
	//（重传会覆盖对象，而「已发布文件不可变」不允许）。
	existing, found, err := artifacts.FindArtifactByHash(ctx, release.ID, built.ArtifactHash)
	if err != nil {
		return nil, err
	}
	if found && existing.State == model.ArtifactStateVerified {
		// 已有确认过的同内容制品：直接进入发布判定。
		published, reason, err := artifacts.PublishReleaseIfReady(ctx, release.ID, revision)
		if err != nil {
			return nil, err
		}
		return map[string]any{
			"releaseId": releaseID, "artifactHash": built.ArtifactHash,
			"published": published, "reason": reason, "replayed": true,
		}, nil
	}

	if _, err := objectStore.PutBytes(ctx, objectKey, built.ArtifactBytes, contentTypeFor(release.Format)); err != nil {
		return nil, fmt.Errorf("写入对象失败：%w", err)
	}

	// **读回校验**：上传返回成功不等于对象可用。
	readBack, err := objectStore.ReadBytes(ctx, objectKey)
	if err != nil {
		return nil, fmt.Errorf("读回对象失败（上传可能未真正写入）：%w", err)
	}
	if err := model.ValidateArtifactUpload(
		built.SizeBytes, built.ArtifactHash, int64(len(readBack)),
		model.ComputeArtifactHash(readBack), true,
	); err != nil {
		return nil, err
	}

	registered, err := artifacts.RegisterArtifact(ctx, store.ArtifactUpload{
		ReleaseID: release.ID, Revision: revision,
		ArtifactType: "export", Format: release.Format,
		ObjectKey:       objectKey,
		StorageEndpoint: endpoint, StorageBucket: bucket,
		SizeBytes: built.SizeBytes, ContentType: contentTypeFor(release.Format),
		ArtifactHash: built.ArtifactHash, ItemsContentHash: built.ItemsContentHash,
		EncoderVersion:   studio.EncoderVersion,
		MappingVersionID: nullableInt64(mappingVersionID),
		UploadedBy:       job.CreatedBy,
	})
	if err != nil {
		return nil, err
	}
	if _, _, err := artifacts.RegisterManifest(ctx, built.Manifest, job.CreatedBy); err != nil {
		return nil, err
	}
	if err := artifacts.MarkArtifactVerified(ctx, registered.Artifact.ID); err != nil {
		return nil, err
	}

	// 最后一步：只有制品确认后才可能发布（唯一入口）。
	published, reason, err := artifacts.PublishReleaseIfReady(ctx, release.ID, revision)
	if err != nil {
		return nil, err
	}
	if !published {
		// 未发布**不是错误**：可能还有其它格式的制品未完成。
		// 把它作为结果返回，让 dispatcher/运维能解释「为什么还在 building」。
		return map[string]any{
			"releaseId": releaseID, "artifactHash": built.ArtifactHash,
			"published": false, "reason": reason,
		}, nil
	}
	return map[string]any{
		"releaseId": releaseID, "artifactHash": built.ArtifactHash,
		"manifestHash": built.ManifestHash, "sizeBytes": built.SizeBytes,
		"published": true,
	}, nil
}

// contentTypeFor 返回格式对应的 MIME 类型。
func contentTypeFor(format string) string {
	switch strings.ToLower(format) {
	case "csv":
		return "text/csv"
	case "jsonl", "json":
		return "application/jsonl"
	default:
		return "application/octet-stream"
	}
}

// decodeJSONMap 把 JSON 对象解码成 map（失败返回空 map 而不是报错：
// provenance 缺失不该阻止发布，它只是数据卡的一个字段）。
func decodeJSONMap(raw json.RawMessage) map[string]any {
	if len(raw) == 0 {
		return map[string]any{}
	}
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return map[string]any{}
	}
	return decoded
}

func nullableInt64(value int64) *int64 {
	if value == 0 {
		return nil
	}
	pointer := value
	return &pointer
}

// ---------------------------------------------------------------------------
// StudioJobEnv 的发布依赖
// ---------------------------------------------------------------------------

func (env *StudioJobEnv) Releases() *store.ReleaseStore {
	if value, ok := env.Extra("releaseStore"); ok {
		if releases, ok := value.(*store.ReleaseStore); ok {
			return releases
		}
	}
	releases := store.NewReleaseStore(env.Pool)
	env.SetExtra("releaseStore", releases)
	return releases
}

func (env *StudioJobEnv) ReleaseArtifacts() *store.ReleaseArtifactStore {
	if value, ok := env.Extra("releaseArtifactStore"); ok {
		if artifacts, ok := value.(*store.ReleaseArtifactStore); ok {
			return artifacts
		}
	}
	artifacts := store.NewReleaseArtifactStore(env.Pool)
	env.SetExtra("releaseArtifactStore", artifacts)
	return artifacts
}
