package main

import (
	"context"
	"sync"

	"github.com/1420970597/llm/internal/store"
	"github.com/redis/go-redis/v9"
)

// 本文件是 worker 的「任务处理注册表」，属于冻结契约的一部分
// （见 docs/plans/eval-and-cleaning-plan.md 第 1 节）。
//
// 设计目的：让各 lane 只通过 init() 注册自己的 job handler，无需修改 main.go，
// 从而保证多个 lane 并行开发时不会产生对同一文件的写冲突。

// jobContext 汇聚 worker 运行所需的全部依赖，供各 lane handler 复用。
type jobContext struct {
	queue          string
	redis          *redis.Client
	datasets       *store.DatasetStore
	pipeline       *store.PipelineStore
	prompts        *store.AdminStore
	reasoning      *store.ReasoningStore
	rewards        *store.RewardStore
	artifacts      *store.ArtifactStore
	generationRuns *store.GenerationRunStore
	extra          map[string]any
}

// jobHandler 处理一个已注册的 job 类型。
type jobHandler func(ctx context.Context, jc *jobContext, job jobPayload) error

var (
	jobMu       sync.RWMutex
	jobHandlers = map[string]jobHandler{}
)

// RegisterJobHandler 注册一个 job handler。lane 在 init() 中调用。
// 重复注册同一 jobType 会 panic，避免两个 lane 静默覆盖彼此的实现。
func RegisterJobHandler(jobType string, handler jobHandler) {
	if jobType == "" || handler == nil {
		return
	}
	jobMu.Lock()
	defer jobMu.Unlock()
	if _, exists := jobHandlers[jobType]; exists {
		panic("duplicate job handler: " + jobType)
	}
	jobHandlers[jobType] = handler
}

// lookupJobHandler 查找已注册的 handler。
func lookupJobHandler(jobType string) (jobHandler, bool) {
	jobMu.RLock()
	defer jobMu.RUnlock()
	handler, ok := jobHandlers[jobType]
	return handler, ok
}

// SetExtra 让 lane 把共享依赖（如评估 store）挂到 jobContext 上，避免互相改签名。
func (jc *jobContext) SetExtra(key string, value any) {
	if jc.extra == nil {
		jc.extra = map[string]any{}
	}
	jc.extra[key] = value
}

// Extra 读取 lane 挂载的共享依赖。
func (jc *jobContext) Extra(key string) (any, bool) {
	value, ok := jc.extra[key]
	return value, ok
}
