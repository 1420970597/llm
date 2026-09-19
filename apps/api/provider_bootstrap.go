package main

import (
	"github.com/1420970597/llm/internal/config"
	"github.com/1420970597/llm/internal/model"
	"github.com/1420970597/llm/internal/store"
)

// 启动期默认 provider 引导的决策。
//
// 为什么要把「决策」从 main() 里拆出来（issue #63 的连带风险）：
// 引导走的是与 HTTP upsertProvider **同一个** store 入口
// （EnsureProvider → UpsertProvider → store.ValidateProviderInput）。
// 两者共用校验是对的（校验只该有一份），但失败语义必须不同：
//
//   - HTTP 请求：校验失败 → 400 + 字段名，用户能立刻改表单；
//   - 启动期：没有可交互的「用户」。此时若沿用 log.Fatalf，一个漏填的
//     APP_BOOTSTRAP_PROVIDER_MODEL 就会把「配置少写一个字段」升级成
//     「容器起不来 / 反复重启」，即把局部配置问题放大成整服务不可用。
//
// 所以启动期把「配置不完整」判定为**跳过引导并告警**，服务照常启动。
// 拆分返回值而不是在 main() 里 if/else，是为了让这条约束能被单元测试直接证明：
// main() 中的 log.Fatalf 在测试里不可执行，而这里的 outcome 可以断言。
type bootstrapOutcome int

const (
	// bootstrapSkippedUnconfigured：未配置引导（BASE_URL 或 API_KEY 缺失）。
	// 与既有行为一致，静默跳过。
	bootstrapSkippedUnconfigured bootstrapOutcome = iota
	// bootstrapSkippedIncomplete：配置不完整（例如缺 model），
	// 告警后跳过引导，**不阻断启动**。
	bootstrapSkippedIncomplete
	// bootstrapReady：配置完整，调用方应执行引导写库。
	bootstrapReady
)

// resolveBootstrapProvider 决定是否需要引导默认 provider，以及用哪个输入。
//
// 返回的 error 仅在 outcome == bootstrapSkippedIncomplete 时非 nil，
// 内容是 store.ValidateProviderInput 给出的中文字段提示，用于日志告警。
func resolveBootstrapProvider(cfg config.APIConfig) (model.ModelProvider, bootstrapOutcome, error) {
	if cfg.BootstrapProviderBaseURL == "" || cfg.BootstrapProviderAPIKey == "" {
		return model.ModelProvider{}, bootstrapSkippedUnconfigured, nil
	}

	input := model.ModelProvider{
		Name:           cfg.BootstrapProviderName,
		BaseURL:        cfg.BootstrapProviderBaseURL,
		Model:          cfg.BootstrapProviderModel,
		ProviderType:   cfg.BootstrapProviderType,
		MaxConcurrency: cfg.BootstrapProviderMaxConcurrency,
		TimeoutSeconds: cfg.BootstrapProviderTimeoutSeconds,
		IsActive:       true,
		APIKey:         cfg.BootstrapProviderAPIKey,
	}

	// 复用与 HTTP 入口完全相同的校验函数，避免「启动期宽松、HTTP 严格」两套规则。
	// 校验失败不返回 fatal 错误，而是降级为「跳过引导」。
	if err := store.ValidateProviderInput(input); err != nil {
		return model.ModelProvider{}, bootstrapSkippedIncomplete, err
	}
	return input, bootstrapReady, nil
}
