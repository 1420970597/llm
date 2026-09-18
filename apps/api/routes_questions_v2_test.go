package main

import (
	"testing"

	"github.com/1420970597/llm/internal/model"
	"github.com/1420970597/llm/internal/store"
)

// init() 必须注册 questions 段且不 panic。
// 注册表对重复段会 panic，这条测试同时防止本 lane 与其他 lane 撞段。
func TestQuestionsRouterRegistered(t *testing.T) {
	router, ok := lookupDatasetRouter("questions")
	if !ok {
		t.Fatal("questions dataset router is not registered")
	}
	if router == nil {
		t.Fatal("questions dataset router is nil")
	}
}

// 请求体显式指定 x 时优先级最高。
func TestResolveQuestionsPerDirectionPrefersRequest(t *testing.T) {
	dataset := model.Dataset{
		QuestionsPerDirect: 7,
		Estimate:           model.PlanEstimate{QuestionsPerDomain: 9},
	}
	if got := resolveQuestionsPerDirection(3, dataset); got != 3 {
		t.Fatalf("got %d, want 3 (request value wins)", got)
	}
}

// 请求体未指定时回退到数据集自身的 questions_per_direction。
func TestResolveQuestionsPerDirectionFallsBackToDataset(t *testing.T) {
	dataset := model.Dataset{
		QuestionsPerDirect: 7,
		Estimate:           model.PlanEstimate{QuestionsPerDomain: 9},
	}
	if got := resolveQuestionsPerDirection(0, dataset); got != 7 {
		t.Fatalf("got %d, want 7 (dataset value)", got)
	}
}

// 数据集也未配置时回退到策略推算值（legacy 语义）。
func TestResolveQuestionsPerDirectionFallsBackToEstimate(t *testing.T) {
	dataset := model.Dataset{
		QuestionsPerDirect: 0,
		Estimate:           model.PlanEstimate{QuestionsPerDomain: 9},
	}
	if got := resolveQuestionsPerDirection(0, dataset); got != 9 {
		t.Fatalf("got %d, want 9 (estimate value)", got)
	}
}

// 全部落空时返回兜底值，且必须为正数。
func TestResolveQuestionsPerDirectionUsesDefault(t *testing.T) {
	got := resolveQuestionsPerDirection(0, model.Dataset{})
	if got != store.DefaultQuestionsPerDirection {
		t.Fatalf("got %d, want %d", got, store.DefaultQuestionsPerDirection)
	}
	if got <= 0 {
		t.Fatalf("resolved count must be positive, got %d", got)
	}
}

// 负数请求值视为未指定，走回退链。
func TestResolveQuestionsPerDirectionIgnoresNegativeRequest(t *testing.T) {
	dataset := model.Dataset{QuestionsPerDirect: 4}
	if got := resolveQuestionsPerDirection(-3, dataset); got != 4 {
		t.Fatalf("got %d, want 4 (negative request must be ignored)", got)
	}
}

// 配比清洗：保留三档、归一化大小写与空白、丢弃非法档位与非正值。
func TestNormalizeDifficultyMixCleansInput(t *testing.T) {
	got := normalizeDifficultyMix(map[string]float64{
		"EASY":    0.3,
		" Medium": 0.5,
		"hard":    0.2,
		"bogus":   1.0,
		"easy2":   0.5,
	})
	want := map[string]float64{"easy": 0.3, "medium": 0.5, "hard": 0.2}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for level, weight := range want {
		if got[level] != weight {
			t.Errorf("mix[%s] = %v, want %v (full: %v)", level, got[level], weight, got)
		}
	}
}

// 非正值与非法的档位必须被剔除；全部非法时返回 nil 表示未指定。
func TestNormalizeDifficultyMixRejectsInvalid(t *testing.T) {
	if got := normalizeDifficultyMix(map[string]float64{"easy": 0, "medium": -1}); got != nil {
		t.Fatalf("all-invalid mix should return nil, got %v", got)
	}
	if got := normalizeDifficultyMix(map[string]float64{"unknown": 1}); got != nil {
		t.Fatalf("unknown-only mix should return nil, got %v", got)
	}
}

// 空配比返回 nil，交给生成器使用默认配比。
func TestNormalizeDifficultyMixHandlesEmpty(t *testing.T) {
	if got := normalizeDifficultyMix(nil); got != nil {
		t.Fatalf("nil mix should return nil, got %v", got)
	}
	if got := normalizeDifficultyMix(map[string]float64{}); got != nil {
		t.Fatalf("empty mix should return nil, got %v", got)
	}
}
