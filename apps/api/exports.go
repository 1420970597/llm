package main

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/1420970597/llm/internal/model"
	"github.com/1420970597/llm/internal/storage"
)

// validateExportPrerequisites 校验「这个数据集现在有没有东西可导出」。
//
// **这是两条导出分支（legacy 与 multi-format）共用的唯一判定**。
//
// 为什么要抽出来（issue #137）：此前两条分支各写各的校验，且口径不同 ——
// multi-format 分支**完全不做上游校验**，于是：
//
//	POST /datasets/164/export {}                          -> 409（legacy 拦下）
//	POST /datasets/164/export {"format":"jsonl",...}      -> 202（绕过成功）
//
// 结果是：对没有任何可导出内容的数据集也能入队，状态被写成 export_queued，
// worker 随后才失败并留下 export.generate_failed 终态 —— 用户看到「导出失败」
// 而不是「你还没有可导出的内容」。实测全库积累了一批这样的失败数据集。
//
// 判定依据是「**该数据集实际有什么内容**」，而不是「有没有质量评估」——
// 后者对 SFT 分支是错的（功能说明.txt 把导出分成两条互斥分支：
// GRPO 需要教师评判提示词、SFT 需要思维链与答案；SFT 数据集本就不产生
// reward_records，见迁移 0018 把 sft_records 作为一等公民）。
// 因此：
//   - 有可用的 sft_records（状态 generated）-> SFT 分支，内容齐备；
//   - 否则必须走 GRPO 分支 -> 要求 reward 覆盖全部题目。
//
// 返回 (状态码, 错误)；错误为 nil 表示校验通过。
func (app *application) validateExportPrerequisites(ctx context.Context, datasetID int64) (int, error) {
	questions, err := app.pipeline.ListQuestions(ctx, datasetID)
	if err != nil {
		return http.StatusInternalServerError, err
	}

	// SFT 分支：思维链或答案可用即可导出。
	// 只认 generated（契约 §1.3：invalid/failed 不可进入导出）。
	sftRecords, err := app.sftStore().ListRecords(ctx, datasetID)
	if err != nil {
		return http.StatusInternalServerError, err
	}
	for _, record := range sftRecords {
		if !model.RecordStatusUsableForDownstream(record.Status) {
			continue
		}
		if strings.TrimSpace(record.ChainOfThought) != "" || strings.TrimSpace(record.Answer) != "" {
			return 0, nil
		}
	}

	// 没有可用的 SFT 内容 -> 只能走 GRPO 分支，要求评分完整。
	if len(questions) == 0 {
		return http.StatusConflict, errors.New(msgNoQuestions)
	}
	rewards, err := app.rewards.List(ctx, datasetID)
	if err != nil {
		return http.StatusInternalServerError, err
	}
	if len(rewards) == 0 {
		return http.StatusConflict, errors.New(msgNoRewardRecords)
	}
	if len(rewards) < len(questions) {
		return http.StatusConflict, errors.New(msgRewardsIncomplete)
	}
	return 0, nil
}

func (app *application) enqueueExport(w http.ResponseWriter, r *http.Request) {
	id, err := datasetIDFromPath(r.URL.Path)
	if err != nil {
		app.writeError(w, http.StatusBadRequest, err)
		return
	}

	if status, err := app.validateExportPrerequisites(r.Context(), id); err != nil {
		app.writeError(w, status, err)
		return
	}

	enqueued, err := app.enqueueDatasetJob(r.Context(), "export.generate", id, "export_queued")
	if err != nil {
		app.writeError(w, http.StatusInternalServerError, err)
		return
	}
	if enqueued {
		_ = app.store.WriteAuditLog(r.Context(), "user", "enqueue", "dataset_export", datasetIDString(id), "export.generate")
	}
	app.writeJSON(w, http.StatusAccepted, model.StageEnqueueResult{
		DatasetID:  id,
		Stage:      "export",
		State:      "queued",
		Message:    queuedMessage(enqueued, "导出任务已入队", "导出任务已在队列中"),
		AcceptedAt: time.Now().Format(time.RFC3339),
	})
}

func (app *application) listArtifacts(w http.ResponseWriter, r *http.Request) {
	id, err := datasetIDFromPath(r.URL.Path)
	if err != nil {
		app.writeError(w, http.StatusBadRequest, err)
		return
	}
	items, err := app.artifacts.List(r.Context(), id)
	if err != nil {
		app.writeError(w, http.StatusInternalServerError, err)
		return
	}
	app.writeJSON(w, http.StatusOK, items)
}

// splitArtifactObjectKey 把工件的 object_key 拆成 (bucket, key)。
//
// object_key 有两种形态：
//   - `s3://<bucket>/<path>`（当前形态）-> bucket 与 path 分别取出；
//   - `<path>`（历史形态，不含 bucket）-> bucket 返回空串，
//     调用方据此回退到存储配置里的 bucket。
//
// 为什么 bucket 必须从 objectKey 里取（issue #136）：
// 工件的 bucket 是**落盘时那个**存储配置决定的，而 ResolveStorageProfile 给的是
// 「**当前默认**存储配置」。管理员切换过默认存储后，用后者去读旧工件会 500
// （key does not exist），尽管对象在旧 bucket 里好好躺着。
func splitArtifactObjectKey(objectKey string) (bucket string, key string) {
	key = objectKey
	parsed, err := url.Parse(objectKey)
	if err != nil || parsed.Scheme != "s3" {
		return "", key
	}
	return parsed.Host, strings.TrimPrefix(parsed.Path, "/")
}

func (app *application) downloadArtifact(w http.ResponseWriter, r *http.Request) {
	id, err := datasetIDFromPath(r.URL.Path)
	if err != nil {
		app.writeError(w, http.StatusBadRequest, err)
		return
	}
	artifactID := r.URL.Query().Get("artifactId")
	if artifactID == "" {
		app.writeError(w, http.StatusBadRequest, errors.New("缺少必要的查询参数 artifactId，请从导出页面重新发起下载"))
		return
	}
	items, err := app.artifacts.List(r.Context(), id)
	if err != nil {
		app.writeError(w, http.StatusInternalServerError, err)
		return
	}
	dataset, err := app.datasets.GetDataset(r.Context(), id)
	if err != nil {
		app.writeError(w, http.StatusInternalServerError, err)
		return
	}
	endpoint, region, bucket, accessKeyID, secretKey, usePathStyle, err := app.datasets.ResolveStorageProfile(r.Context(), dataset.StorageProfileID)
	if err != nil {
		app.writeError(w, http.StatusInternalServerError, err)
		return
	}
	objectStore, err := storage.New(storage.Profile{
		Endpoint:     endpoint,
		Region:       region,
		Bucket:       bucket,
		AccessKeyID:  accessKeyID,
		SecretKey:    secretKey,
		UsePathStyle: usePathStyle,
	})
	if err != nil {
		app.writeError(w, http.StatusInternalServerError, err)
		return
	}
	for _, item := range items {
		if artifactID == strconv.FormatInt(item.ID, 10) {
			fileName := item.ObjectKey[strings.LastIndex(item.ObjectKey, "/")+1:]
			if fileName == "" {
				fileName = "dataset-export.jsonl"
			}
			// 对象位置以**工件自身携带的 objectKey** 为准（issue #136）。
			//
			// objectKey 形如 `s3://<bucket>/<path>`：bucket 是工件落盘时用的那个。
			// 而上面 ResolveStorageProfile 拿到的是「**当前默认**存储配置」——
			// 管理员切换过默认存储之后，用它去读旧工件会 500（key does not exist），
			// 尽管对象在旧 bucket 里好好躺着。
			//
			// 因此：bucket 从 objectKey 里取；只有当 objectKey 不含 bucket
			// （历史数据或非 s3 形态）时才回退到 profile 的 bucket。
			bucket, objectKey := splitArtifactObjectKey(item.ObjectKey)
			payload, err := objectStore.ReadBytesFromBucket(r.Context(), bucket, objectKey)
			if err != nil {
				app.writeError(w, http.StatusInternalServerError, err)
				return
			}
			safeFileName := strings.NewReplacer("\"", "", "\r", "", "\n", "").Replace(fileName)
			w.Header().Set("Content-Type", item.ContentType)
			w.Header().Set("Content-Disposition", "attachment; filename=\""+safeFileName+"\"")
			_, _ = w.Write(payload)
			return
		}
	}
	http.NotFound(w, r)
}

func (app *application) runtimeStatus(w http.ResponseWriter, r *http.Request) {
	status, err := app.artifacts.Runtime(r.Context())
	if err != nil {
		app.writeError(w, http.StatusInternalServerError, err)
		return
	}
	app.writeJSON(w, http.StatusOK, status)
}
