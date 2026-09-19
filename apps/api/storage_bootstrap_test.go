package main

import (
	"strings"
	"testing"

	"github.com/1420970597/llm/internal/config"
	"github.com/1420970597/llm/internal/model"
)

// baseStorageConfig 返回一份「配置完整」的引导配置，供各用例按需改字段。
func baseStorageConfig() config.APIConfig {
	return config.APIConfig{
		BootstrapStorageEnabled:      true,
		BootstrapStorageName:         "默认结果存储",
		BootstrapStorageProvider:     "minio",
		BootstrapStorageEndpoint:     "http://minio:9000",
		BootstrapStorageRegion:       "us-east-1",
		BootstrapStorageBucket:       "llm-factory-dev",
		BootstrapStorageAccessKeyID:  "minioadmin",
		BootstrapStorageSecretKey:    "minioadmin",
		BootstrapStorageUsePathStyle: true,
	}
}

// TestResolveBootstrapStorageReady 确认完整配置产出「可引导」的输入，
// 且关键字段被正确映射（含 IsActive/IsDefault）。
//
// IsDefault 必须为 true：storageProfileId 为 0 的任务靠 `ORDER BY is_default DESC`
// 才能解析到它，否则 bootstrap 出配置也救不了「默认存储」这条路径。
func TestResolveBootstrapStorageReady(t *testing.T) {
	input, outcome, err := resolveBootstrapStorage(baseStorageConfig())
	if outcome != storageBootstrapReady {
		t.Fatalf("outcome = %v, 期望 storageBootstrapReady（err=%v）", outcome, err)
	}
	if err != nil {
		t.Fatalf("配置完整时不应返回 error，得到 %v", err)
	}
	if input.Bucket != "llm-factory-dev" || input.Endpoint != "http://minio:9000" {
		t.Errorf("bucket/endpoint 映射错误: bucket=%q endpoint=%q", input.Bucket, input.Endpoint)
	}
	if !input.IsActive {
		t.Error("IsActive 必须为 true，否则 ResolveStorageProfile 的 WHERE is_active = TRUE 查不到它")
	}
	if !input.IsDefault {
		t.Error("IsDefault 必须为 true，否则 storageProfileId=0 的任务解析不到默认存储")
	}
}

// TestResolveBootstrapStorageDisabled 确认显式关闭时不做任何引导。
func TestResolveBootstrapStorageDisabled(t *testing.T) {
	cfg := baseStorageConfig()
	cfg.BootstrapStorageEnabled = false
	_, outcome, err := resolveBootstrapStorage(cfg)
	if outcome != storageBootstrapSkippedDisabled {
		t.Fatalf("outcome = %v, 期望 storageBootstrapSkippedDisabled", outcome)
	}
	if err != nil {
		t.Fatalf("显式关闭不应返回 error，得到 %v", err)
	}
}

// TestResolveBootstrapStorageIncompleteSkipsWithoutFatal 是本修复最关键的一条：
// 配置不完整时必须「跳过引导 + 返回可读原因」，而**不是**中断启动。
//
// 为什么单测这个：main() 里的 log.Fatalf 在测试中不可执行，所以把
// 「不 fatal」这条约束编码成 outcome 的取值来断言。若将来有人把 skip 改成 fatal，
// 这条测试会失败而不是让某个部署环境静默起不来。
func TestResolveBootstrapStorageIncompleteSkipsWithoutFatal(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*config.APIConfig)
	}{
		{"缺 bucket", func(c *config.APIConfig) { c.BootstrapStorageBucket = "" }},
		{"缺 endpoint", func(c *config.APIConfig) { c.BootstrapStorageEndpoint = "" }},
		{"缺 name", func(c *config.APIConfig) { c.BootstrapStorageName = "   " }},
		{"endpoint 不是合法 URL", func(c *config.APIConfig) { c.BootstrapStorageEndpoint = "minio:9000" }},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			cfg := baseStorageConfig()
			testCase.mutate(&cfg)

			input, outcome, err := resolveBootstrapStorage(cfg)
			if outcome != storageBootstrapSkippedIncomplete {
				t.Fatalf("outcome = %v, 期望 storageBootstrapSkippedIncomplete（不完整时不能被认为是 ready）", outcome)
			}
			if err == nil {
				t.Fatal("不完整时必须返回可读原因，供日志告警")
			}
			if input != (model.StorageProfile{}) {
				t.Errorf("跳过引导时不应产出输入，得到 %+v", input)
			}
		})
	}
}

// TestResolveBootstrapStorageUsesSharedValidator 确认引导复用了 store 层的校验
// （而不是自己再写一套规则）。
//
// 判据：把 endpoint 换成非法 URL 时，错误信息应是 store 校验器给出的中文提示，
// 而不是任何本文件自造的文案。
func TestResolveBootstrapStorageUsesSharedValidator(t *testing.T) {
	cfg := baseStorageConfig()
	cfg.BootstrapStorageEndpoint = "not-a-url"
	_, _, err := resolveBootstrapStorage(cfg)
	if err == nil {
		t.Fatal("非法 endpoint 应被拒绝")
	}
	if !strings.Contains(err.Error(), "端点") {
		t.Errorf("错误应来自共享校验器的中文字段提示（含「端点」），得到 %q", err.Error())
	}
}
