package store

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/1420970597/llm/internal/model"
)

// TestGetSampleVersionByIDToleratesMissingGeneratorConfig 隔离出 T14 执行侧
// 「读内容失败」的根因。
//
// 背景：实验执行侧按冻结的 sample_version_id 读内容，而在真实数据里
// `generator_config` 是**可空**的（人工导入/迁移来的版本没有它）。
// 若把 NULL 扫进 json.RawMessage，读取会直接报错，而调用方会把每一项
// 都标成 error —— 真实原因只是「这一版没有生成配置」。
func TestGetSampleVersionByIDToleratesMissingGeneratorConfig(t *testing.T) {
	fixture := newBatchFixture(t)
	ctx := context.Background()
	batches := NewBatchStore(fixture.pool)

	// 两个版本：一个**不带** generator_config（人工导入的形态），一个带。
	withoutConfig, err := batches.EnsureSample(ctx, fixture.projectID, "no-config-"+t.Name(),
		model.TargetKindSFT, "无生成配置", nil)
	if err != nil {
		t.Fatalf("EnsureSample: %v", err)
	}
	_, versionWithout, err := batches.AppendSampleVersion(ctx, AppendSampleVersionInput{
		ProjectID: fixture.projectID, SampleKey: withoutConfig.SampleKey,
		TargetKind: model.TargetKindSFT, Title: "无生成配置",
		Payload: map[string]any{"question": "q", "reasoning": "r", "answer": "a"},
	})
	if err != nil {
		t.Fatalf("AppendSampleVersion: %v", err)
	}

	loaded, err := batches.GetSampleVersionByID(ctx, fixture.projectID, versionWithout.ID)
	if err != nil {
		t.Fatalf("缺少 generator_config 的版本必须能读出（NULL 不得导致读取失败）：%v", err)
	}
	if loaded.Version != versionWithout.Version {
		t.Fatalf("读回的版本号应为 %d，实际 %d", versionWithout.Version, loaded.Version)
	}
	if len(loaded.Payload) == 0 {
		t.Fatal("内容必须被读出")
	}
	// 没有生成配置时应是空对象而不是报错后的零值。
	if len(loaded.GeneratorConfig) != 0 && !json.Valid(loaded.GeneratorConfig) {
		t.Fatalf("生成配置必须是合法 JSON 或空，实际 %s", loaded.GeneratorConfig)
	}

	// 带生成配置的版本同样可读。
	var versionWithID int64
	withConfig, err := batches.EnsureSample(ctx, fixture.projectID, "with-config-"+t.Name(),
		model.TargetKindSFT, "有生成配置", nil)
	if err != nil {
		t.Fatalf("EnsureSample: %v", err)
	}
	_, versionWith, err := batches.AppendSampleVersion(ctx, AppendSampleVersionInput{
		ProjectID: fixture.projectID, SampleKey: withConfig.SampleKey,
		TargetKind: model.TargetKindSFT, Title: "有生成配置",
		Payload:         map[string]any{"question": "q", "reasoning": "r", "answer": "a"},
		GeneratorConfig: json.RawMessage(`{"modelConnectionId":7}`),
	})
	if err != nil {
		t.Fatalf("AppendSampleVersion: %v", err)
	}
	versionWithID = versionWith.ID
	loadedWithConfig, err := batches.GetSampleVersionByID(ctx, fixture.projectID, versionWithID)
	if err != nil {
		t.Fatalf("带生成配置的版本必须可读：%v", err)
	}
	if len(loadedWithConfig.GeneratorConfig) == 0 {
		t.Fatal("生成配置必须被读出")
	}

	// 跨项目读取必须读不到（防止用别的项目的版本 ID 取内容）。
	// 用不存在的项目 ID 而不是再建一个 fixture：同一测试里建两个 fixture 会
	// 撞项目名的唯一约束（那会掩盖真正要断言的东西）。
	if _, err := batches.GetSampleVersionByID(ctx, fixture.projectID+1_000_000, versionWithID); err == nil {
		t.Fatal("跨项目按 ID 读取必须失败（否则是一个越权读路径）")
	}
}
