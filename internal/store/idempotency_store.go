package store

import (
	"context"
	"errors"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// 本文件实现命令幂等记录（Issue #160 T02，契约 §1.3）。
//
// 为什么不用 Redis 的 SETNX + TTL（现有 enqueueJob 的做法）：
// 短 TTL 只能防「同一秒内的重复点击」，而幂等要回答的是更硬的问题：
//   * 用户在超时后手动重试；
//   * 前端在断网恢复后重放请求；
//   * worker/网关的重复投递。
// 这些都可能发生在 TTL 之后。用持久表 + 请求摘要，才能稳定区分
// 「同键同请求」（回放原结果）与「同键不同请求」（409）。

type IdempotencyStore struct {
	db *pgxpool.Pool
}

func NewIdempotencyStore(db *pgxpool.Pool) *IdempotencyStore {
	return &IdempotencyStore{db: db}
}

// IdempotencyRecord 是一条已完成的命令记录。
type IdempotencyRecord struct {
	Scope          string
	ActorID        int64
	Key            string
	RequestDigest  string
	ResourceID     int64
	ResponseStatus int
}

// Lookup 查询是否已有该 (scope, actor, key) 的记录。
//
// 第二个返回值为 false 表示「没有记录」，此时应正常执行命令。
func (s *IdempotencyStore) Lookup(ctx context.Context, scope string, actorID int64, key string) (IdempotencyRecord, bool, error) {
	key = strings.TrimSpace(key)
	if key == "" || actorID <= 0 {
		return IdempotencyRecord{}, false, nil
	}

	var record IdempotencyRecord
	err := s.db.QueryRow(ctx, `
    SELECT scope, actor_id, idempotency_key, request_digest, COALESCE(resource_id, 0), response_status
    FROM idempotency_records
    WHERE scope = $1 AND actor_id = $2 AND idempotency_key = $3`,
		scope, actorID, key,
	).Scan(&record.Scope, &record.ActorID, &record.Key, &record.RequestDigest,
		&record.ResourceID, &record.ResponseStatus)
	if errors.Is(err, pgx.ErrNoRows) {
		return IdempotencyRecord{}, false, nil
	}
	if err != nil {
		return IdempotencyRecord{}, false, err
	}
	return record, true, nil
}

// Save 记录一次**已成功执行**的命令。
//
// 只在命令成功之后写入：先写记录再执行会让「执行失败」也占住幂等键，
// 于是用户重试永远拿到一个不存在的资源。
//
// 并发下两个请求可能同时通过 Lookup 并各自执行：唯一键会让第二个 Save 报 23505。
// 这是**有意的保守取舍** —— 业务层（项目名）的唯一约束才是最终防线，
// 而幂等表只保证「有序重放」这一常见场景；要彻底串行化需要用
// INSERT ... ON CONFLICT 抢占的「先占位后执行」模式，那会把失败态也写进表。
// 调用方必须容忍 Save 的冲突错误（T02 的 handler 会记录并继续）。
func (s *IdempotencyStore) Save(ctx context.Context, scope string, actorID int64, key, digest string, resourceID int64, status int) error {
	key = strings.TrimSpace(key)
	if key == "" || actorID <= 0 {
		return nil
	}

	_, err := s.db.Exec(ctx, `
    INSERT INTO idempotency_records
      (scope, actor_id, idempotency_key, request_digest, resource_id, response_status)
    VALUES ($1, $2, $3, $4, $5, $6)
    ON CONFLICT (scope, actor_id, idempotency_key) DO NOTHING`,
		scope, actorID, key, digest, resourceID, status)
	return err
}

// Delete 删除一条幂等记录。仅用于测试与保留策略（T21），不参与正常命令路径。
func (s *IdempotencyStore) Delete(ctx context.Context, scope string, actorID int64, key string) error {
	_, err := s.db.Exec(ctx, `
    DELETE FROM idempotency_records WHERE scope = $1 AND actor_id = $2 AND idempotency_key = $3`,
		scope, actorID, strings.TrimSpace(key))
	return err
}
