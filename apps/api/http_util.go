package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/1420970597/llm/internal/model"
)

// 本文件是「HTTP 公共工具」，属于冻结契约的一部分。
// lane 必须复用这里的能力，不得各自重复实现鉴权、JSON 编解码与错误响应。

// requestUser 从请求上下文取出已认证用户（由 middleware 注入）。
func requestUser(r *http.Request) (model.User, bool) {
	user, ok := r.Context().Value(userContextKey).(model.User)
	return user, ok
}

// requireAdmin 判断当前请求是否为管理员。
func requireAdmin(r *http.Request) bool {
	user, ok := requestUser(r)
	return ok && user.Role == "admin"
}

// 审计日志写入，忽略失败（与现有实现保持一致）。
func (app *application) audit(ctx context.Context, action, resourceType, resourceID, detail string) {
	_ = app.store.WriteAuditLog(ctx, "user", action, resourceType, resourceID, detail)
}

// enqueueJob 统一入队入口：带 Redis 去重键，供所有 lane 复用。
// queuedStatus 为入队后写回 datasets.status 的状态值；为空则不写回。
func (app *application) enqueueJob(ctx context.Context, jobType string, datasetID int64, queuedStatus string) (bool, error) {
	dedupKey := fmt.Sprintf("dedup:%s:%d", jobType, datasetID)
	set, err := app.redis.SetNX(ctx, dedupKey, "1", 10*time.Minute).Result()
	if err != nil {
		return false, err
	}
	if !set {
		return false, nil
	}

	payload, err := json.Marshal(map[string]any{"type": jobType, "datasetId": datasetID})
	if err != nil {
		_ = app.redis.Del(ctx, dedupKey)
		return false, err
	}
	if err := app.redis.LPush(ctx, app.cfg.QueueName, payload).Err(); err != nil {
		_ = app.redis.Del(ctx, dedupKey)
		return false, err
	}
	if queuedStatus != "" {
		if err := app.datasets.UpdateStatus(ctx, datasetID, queuedStatus); err != nil {
			_ = app.redis.Del(ctx, dedupKey)
			return false, err
		}
	}
	return true, nil
}

// enqueueResult 构造统一的入队响应。
func enqueueResult(datasetID int64, stage string, enqueued bool, created, existing string) model.StageEnqueueResult {
	message := existing
	if enqueued {
		message = created
	}
	return model.StageEnqueueResult{
		DatasetID:  datasetID,
		Stage:      stage,
		State:      "queued",
		Message:    message,
		AcceptedAt: time.Now().Format(time.RFC3339),
	}
}
