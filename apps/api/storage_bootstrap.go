package main

import (
	"github.com/1420970597/llm/internal/config"
	"github.com/1420970597/llm/internal/model"
	"github.com/1420970597/llm/internal/store"
)

// 启动期默认结果存储引导的决策（issue #83）。
//
// 背景：全新部署下 storage_profiles 表为空，而答案/评分/导出三个阶段都要写对象存储。
// 此前没有对应的 bootstrap（provider 有 EnsureProvider，storage 什么都没有），
// 也没有种子迁移，于是全新部署的用户能建出一个**注定在答案阶段失败**的任务，
// 而失败原因只以 `no rows in result set` 出现在 worker 日志里，界面上只看到
// 「答案生成失败 / 系统同步中」——用户既不知原因也不知怎么修。
//
// 这里把「决策」从 main() 拆出来，与 apps/api/provider_bootstrap.go 完全同构，
// 理由也一样：
//
//   - 引导走的是与 HTTP upsertStorageProfile **同一个** store 入口
//     （EnsureStorageProfile → UpsertStorageProfile → store.ValidateStorageProfileInput），
//     校验只该有一份；
//   - 但失败语义必须不同。启动期没有可交互的「用户」，若沿用 log.Fatalf，
//     一个漏填的 S3_BUCKET 就会把「配置少写一个字段」升级成「容器起不来」。
//
// 因此配置不完整 → 跳过引导 + 告警，服务照常启动。
// 拆出返回值（而不是在 main() 里 if/else）是为了让这条约束能被单测直接证明：
// main() 里的 log.Fatalf 在测试中不可执行，而 outcome 可以断言。
type storageBootstrapOutcome int

const (
	// storageBootstrapSkippedDisabled：显式关闭了引导（APP_BOOTSTRAP_STORAGE_ENABLED=false）。
	storageBootstrapSkippedDisabled storageBootstrapOutcome = iota
	// storageBootstrapSkippedIncomplete：配置不完整（例如缺 bucket）。
	// 告警后跳过引导，**不阻断启动**。
	storageBootstrapSkippedIncomplete
	// storageBootstrapReady：配置完整，调用方应执行幂等引导写库。
	storageBootstrapReady
)

// storageBootstrapInput 是引导用的输入与「是否真的完整」的判定结果。
//
// enabled 为 false 时不返回 input；否则 input 已通过 store 层的完整校验。
func resolveBootstrapStorage(cfg config.APIConfig) (model.StorageProfile, storageBootstrapOutcome, error) {
	if !cfg.BootstrapStorageEnabled {
		return model.StorageProfile{}, storageBootstrapSkippedDisabled, nil
	}

	input := model.StorageProfile{
		Name:            cfg.BootstrapStorageName,
		Provider:        cfg.BootstrapStorageProvider,
		Endpoint:        cfg.BootstrapStorageEndpoint,
		Region:          cfg.BootstrapStorageRegion,
		Bucket:          cfg.BootstrapStorageBucket,
		AccessKeyID:     cfg.BootstrapStorageAccessKeyID,
		SecretAccessKey: cfg.BootstrapStorageSecretKey,
		UsePathStyle:    cfg.BootstrapStorageUsePathStyle,
		IsActive:        true,
		// 默认存储：storageProfileId 为 0 时 ResolveStorageProfile 会挑 is_default 优先的那条，
		// 把它标为默认让「未绑定存储的任务」也能解析到它。
		IsDefault: true,
	}

	// 复用与 HTTP 入口完全相同的校验函数，避免「启动期宽松、HTTP 严格」两套规则。
	// 校验失败不返回 fatal 错误，而是降级为「跳过引导」。
	if err := store.ValidateStorageProfileInput(input); err != nil {
		return model.StorageProfile{}, storageBootstrapSkippedIncomplete, err
	}
	return input, storageBootstrapReady, nil
}
