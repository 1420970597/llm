package llm

import (
	"context"
	"encoding/json"

	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/1420970597/llm/internal/model"
)

// fakeProvider 起一个假的 OpenAI 兼容服务，返回固定的 chat completion。
//
// 用途：确定性地验证「生成 → 难度分配 → 落库字段」整条真实代码路径，
// 不依赖真实 LLM 的随机性与配额。
func fakeProvider(t *testing.T, content string) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/chat/completions") {
			http.NotFound(w, r)
			return
		}
		payload := map[string]any{
			"choices": []any{map[string]any{
				"message": map[string]any{
					"role":    "assistant",
					"content": content,
				},
				"finish_reason": "stop",
			}},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(payload)
	}))
	t.Cleanup(server.Close)
	return server
}

func fakeInput(server *httptest.Server, perDirection int, mix map[string]float64) QuestionGenInput {
	return QuestionGenInput{
		DatasetID:             1,
		RootKeyword:           "海上巡逻",
		QuestionsPerDirection: perDirection,
		DifficultyMix:         mix,
		Directions: []DirectionContext{
			{DomainID: 10, DomainName: "编队护航"},
		},
	}
}

func fakeProviderConfig(server *httptest.Server) ProviderConfig {
	return ProviderConfig{
		BaseURL:      server.URL,
		Model:        "test-model",
		ProviderType: "openai-compatible",
		APIKey:       "test-key",
	}
}

// 关键行为：落库的 difficulty 必须等于用户配比算出的计划，
// 而不是照抄模型自报的标签。
//
// 这条用例锁定的真实缺陷：模型经常不遵守提示词里的难度要求
// （实测请求 hard 配额为 0，模型仍把一半问题标成 hard）。
func TestGenerateQuestionsV2UsesPlannedDifficultyNotModelLabels(t *testing.T) {
	// 模型故意把两条都标成 hard，与计划（easy + medium）冲突。
	server := fakeProvider(t, `[
      {"content":"问题一：近海巡逻遭遇可疑目标","difficulty":"hard"},
      {"content":"问题二：编队护航中的通信中断","difficulty":"hard"}
    ]`)

	// 请求 easy 0.5 / medium 0.5 / hard 0 —— hard 配额为 0。
	input := fakeInput(server, 2, map[string]float64{
		DifficultyEasy: 0.5, DifficultyMedium: 0.5, DifficultyHard: 0,
	})

	questions, err := GenerateQuestionsV2(context.Background(), fakeProviderConfig(server), input)
	if err != nil {
		t.Fatalf("generate failed: %v", err)
	}
	if len(questions) != 2 {
		t.Fatalf("generated %d questions, want 2", len(questions))
	}

	got := []string{questions[0].Difficulty, questions[1].Difficulty}
	want := []string{DifficultyEasy, DifficultyMedium}
	for index := range want {
		if got[index] != want[index] {
			t.Errorf("question %d difficulty = %s, want %s (plan must win over model label)",
				index, got[index], want[index])
		}
	}
	for _, question := range questions {
		if question.Difficulty == DifficultyHard {
			t.Errorf("hard quota was 0 but a question was labelled hard: %+v", question)
		}
	}
}

// difficultyScore 必须与 difficulty 一致。
func TestGenerateQuestionsV2KeepsScoreConsistentWithDifficulty(t *testing.T) {
	server := fakeProvider(t, `[
      {"content":"问题甲：例行巡逻时发现不明小艇靠近，请给出处置方案。","difficulty":"medium"},
      {"content":"问题乙：突发拦截任务中通信中断，请给出处置方案。","difficulty":"medium"},
      {"content":"问题丙：同时出现多批可疑目标，请给出处置优先级。","difficulty":"medium"}
    ]`)
	input := fakeInput(server, 3, map[string]float64{
		DifficultyEasy: 0.3, DifficultyMedium: 0.5, DifficultyHard: 0.2,
	})

	questions, err := GenerateQuestionsV2(context.Background(), fakeProviderConfig(server), input)
	if err != nil {
		t.Fatalf("generate failed: %v", err)
	}
	if len(questions) != 3 {
		t.Fatalf("generated %d questions, want 3", len(questions))
	}

	counts := map[string]int{}
	for _, question := range questions {
		counts[question.Difficulty]++
		if want := DifficultyScoreOf(question.Difficulty); question.DifficultyScore != want {
			t.Errorf("difficulty=%s has score=%d, want %d",
				question.Difficulty, question.DifficultyScore, want)
		}
	}
	// 契约样例：3 个问题按 0.3/0.5/0.2 分配 → easy=1, medium=1, hard=1
	// （最大余数法：0.9→0, 1.5→1, 0.6→0，余数 0.9/0.6/0.5 补两个）。
	if counts[DifficultyEasy] != 1 || counts[DifficultyMedium] != 1 || counts[DifficultyHard] != 1 {
		t.Errorf("difficulty counts = %v, want easy=1 medium=1 hard=1", counts)
	}
}

// 落库字段必须完整：DedupeKey / CanonicalHash / Source / CleaningStatus。
func TestGenerateQuestionsV2PopulatesPersistedFields(t *testing.T) {
	server := fakeProvider(t, `["问题一：近海巡逻遭遇可疑目标"]`)
	input := fakeInput(server, 1, nil)

	questions, err := GenerateQuestionsV2(context.Background(), fakeProviderConfig(server), input)
	if err != nil {
		t.Fatalf("generate failed: %v", err)
	}
	if len(questions) != 1 {
		t.Fatalf("generated %d questions, want 1", len(questions))
	}

	question := questions[0]
	if question.DatasetID != input.DatasetID {
		t.Errorf("dataset id = %d, want %d", question.DatasetID, input.DatasetID)
	}
	if question.DirectionDomainID != 10 {
		t.Errorf("direction domain id = %d, want 10", question.DirectionDomainID)
	}
	if question.DomainID != 10 {
		t.Errorf("domain id = %d, want 10 (direction id)", question.DomainID)
	}
	if question.DedupeKey != DedupeKey(question.Content) {
		t.Errorf("dedupe key mismatch: %s", question.DedupeKey)
	}
	if question.CanonicalHash == "" {
		t.Error("canonical hash must not be empty")
	}
	if question.Source != "ai" {
		t.Errorf("source = %q, want ai", question.Source)
	}
	if question.CleaningStatus != "clean" {
		t.Errorf("cleaning status = %q, want clean", question.CleaningStatus)
	}
	if question.DomainName != "编队护航" {
		t.Errorf("domain name = %q, want 编队护航", question.DomainName)
	}
}

// 批内近重复必须被去重，且不因去重而丢弃其余问题。
func TestGenerateQuestionsV2DeduplicatesWithinBatch(t *testing.T) {
	// 每轮都返回同一批内容（含一对近重复），验证：
	// 1. 近重复只写入一条
	// 2. 去重后无法凑足 target 时，保留已产出的问题而不是报错
	server := fakeProvider(t, `[
      {"content":"在东海海域遇到不明目标，请做出规划。","difficulty":"easy"},
      {"content":"在东海海域遇到不明目标 请做出规划!","difficulty":"medium"},
      {"content":"在南海海域遇到不明目标，请做出规划。","difficulty":"hard"}
    ]`)
	input := fakeInput(server, 3, nil)

	questions, err := GenerateQuestionsV2(context.Background(), fakeProviderConfig(server), input)
	if err != nil {
		t.Fatalf("generate failed: %v", err)
	}

	seen := map[string]struct{}{}
	for _, question := range questions {
		if _, exists := seen[question.DedupeKey]; exists {
			t.Errorf("duplicate dedupe key survived: %s", question.DedupeKey)
		}
		seen[question.DedupeKey] = struct{}{}
	}
	// 三条中两条是近重复，只剩两条唯一内容。
	if len(questions) != 2 {
		t.Fatalf("generated %d questions, want 2 unique (third was a near-duplicate)", len(questions))
	}
}

// 去重后能通过后续轮次补齐到 target。
func TestGenerateQuestionsV2FillsShortfallAfterDedupe(t *testing.T) {
	callCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		// 首轮：2 条内容，其中 1 条是另一条的近重复 → 只能采纳 1 条
		// 次轮：补足剩余的 2 条全新内容
		var content string
		if callCount == 1 {
			content = `[
              {"content":"首轮问题甲，请做出规划。","difficulty":"easy"},
              {"content":"首轮问题甲 请做出规划!","difficulty":"medium"}
            ]`
		} else {
			content = `[
              {"content":"次轮问题乙，请做出规划。","difficulty":"medium"},
              {"content":"次轮问题丙，请做出规划。","difficulty":"hard"}
            ]`
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []any{map[string]any{
				"message": map[string]any{"role": "assistant", "content": content},
			}},
		})
	}))
	defer server.Close()

	input := fakeInput(server, 3, nil)
	questions, err := GenerateQuestionsV2(context.Background(), fakeProviderConfig(server), input)
	if err != nil {
		t.Fatalf("generate failed: %v", err)
	}
	if len(questions) != 3 {
		t.Fatalf("generated %d questions, want 3 (shortfall filled by later round)", len(questions))
	}
	if callCount < 2 {
		t.Errorf("provider called %d times, want >=2", callCount)
	}
}

// 单轮产出不足时应继续请求补齐，而不是直接返回少量结果。
func TestGenerateQuestionsV2RetriesUntilTargetReached(t *testing.T) {
	callCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		// 第一轮只给 1 条，第二轮补足剩余 2 条。
		var content string
		if callCount == 1 {
			content = `[{"content":"问题一：首轮产出，发现可疑目标靠近编队，请给出处置方案。","difficulty":"easy"}]`
		} else {
			content = `[
              {"content":"问题二：次轮产出甲，可疑目标改变航向，请给出处置方案。","difficulty":"medium"},
              {"content":"问题三：次轮产出乙，目标进入警戒区，请给出处置方案。","difficulty":"hard"}
            ]`
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []any{map[string]any{
				"message": map[string]any{"role": "assistant", "content": content},
			}},
		})
	}))
	defer server.Close()

	input := fakeInput(server, 3, nil)
	questions, err := GenerateQuestionsV2(context.Background(), fakeProviderConfig(server), input)
	if err != nil {
		t.Fatalf("generate failed: %v", err)
	}
	if len(questions) != 3 {
		t.Fatalf("generated %d questions, want 3", len(questions))
	}
	if callCount < 2 {
		t.Errorf("provider called %d times, want >=2 (must retry when short)", callCount)
	}
}

// provider 返回错误时，生成必须失败而不是静默返回空结果。
func TestGenerateQuestionsV2PropagatesProviderFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":"boom"}`))
	}))
	defer server.Close()

	input := fakeInput(server, 2, nil)
	if _, err := GenerateQuestionsV2(context.Background(), fakeProviderConfig(server), input); err == nil {
		t.Fatal("expected error when provider fails")
	}
}

// 生成的提示词必须包含用户配比算出的配额（而不是模型自由发挥）。
func TestGenerateQuestionsV2SendsDifficultyQuotaToProvider(t *testing.T) {
	var firstPrompt string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Messages []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"messages"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		for _, message := range body.Messages {
			if message.Role == "user" && firstPrompt == "" {
				// 只记录首次请求：首轮的配额才是完整的用户配比，
				// 后续轮次只包含尚未满足的剩余配额。
				firstPrompt = message.Content
			}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []any{map[string]any{
				"message": map[string]any{"role": "assistant", "content": `["问题一：近海巡逻遭遇可疑目标，请给出处置方案","问题二：编队护航中通信中断，请给出处置方案","问题三：多目标同时出现，请给出处置优先级","问题四：突发拦截任务，请给出处置流程"]`},
			}},
		})
	}))
	defer server.Close()

	input := fakeInput(server, 4, map[string]float64{
		DifficultyEasy: 0.25, DifficultyMedium: 0.5, DifficultyHard: 0.25,
	})
	if _, err := GenerateQuestionsV2(context.Background(), fakeProviderConfig(server), input); err != nil {
		t.Fatalf("generate failed: %v", err)
	}

	for _, want := range []string{"简单 1 个", "中等 2 个", "困难 1 个"} {
		if !strings.Contains(firstPrompt, want) {
			t.Errorf("prompt missing quota %q\n--- prompt ---\n%s", want, firstPrompt)
		}
	}
}

// 长链思维标准步骤必须进入提示词，使问题能触发完整思考链。
func TestGenerateQuestionsV2SendsChainFrameworkToProvider(t *testing.T) {
	var capturedPrompt string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Messages []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"messages"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		for _, message := range body.Messages {
			if message.Role == "user" {
				capturedPrompt = message.Content
			}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []any{map[string]any{
				"message": map[string]any{"role": "assistant", "content": `["问题一：近海巡逻遭遇可疑目标，请给出处置方案"]`},
			}},
		})
	}))
	defer server.Close()

	input := fakeInput(server, 1, nil)
	input.Directions[0].ChainSteps = []model.ChainStep{
		{Index: 1, Title: "确认海域态势", Description: "收集海况", Checkpoint: "态势是否清晰"},
		{Index: 2, Title: "评估可用兵力"},
	}
	if _, err := GenerateQuestionsV2(context.Background(), fakeProviderConfig(server), input); err != nil {
		t.Fatalf("generate failed: %v", err)
	}

	for _, want := range []string{"确认海域态势", "态势是否清晰", "评估可用兵力", "长链思维标准步骤"} {
		if !strings.Contains(capturedPrompt, want) {
			t.Errorf("prompt missing chain framework %q\n--- prompt ---\n%s", want, capturedPrompt)
		}
	}
}

// 用户模板中的占位符必须被替换，且长链步骤可用 {{chainSteps}} 注入。
func TestGenerateQuestionsV2RendersUserTemplate(t *testing.T) {
	var firstPrompt string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Messages []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"messages"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		for _, message := range body.Messages {
			if message.Role == "user" && firstPrompt == "" {
				// 只记录首次请求：后续轮次的 count 会因补齐而变小。
				firstPrompt = message.Content
			}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []any{map[string]any{
				"message": map[string]any{"role": "assistant", "content": `["问题一：近海巡逻遭遇可疑目标，请给出处置方案","问题二：编队护航中通信中断，请给出处置方案"]`},
			}},
		})
	}))
	defer server.Close()

	input := fakeInput(server, 2, nil)
	input.UserPrompt = "主题={{rootKeyword}} 方向={{domainName}} 数量={{count}} 框架={{chainSteps}}"
	input.Directions[0].ChainSteps = []model.ChainStep{{Index: 1, Title: "步骤甲"}}

	if _, err := GenerateQuestionsV2(context.Background(), fakeProviderConfig(server), input); err != nil {
		t.Fatalf("generate failed: %v", err)
	}

	for _, want := range []string{"主题=海上巡逻", "方向=编队护航", "数量=2", "步骤甲"} {
		if !strings.Contains(firstPrompt, want) {
			t.Errorf("template not rendered: missing %q\n--- prompt ---\n%s", want, firstPrompt)
		}
	}
	if strings.Contains(firstPrompt, "{{") {
		t.Errorf("unreplaced placeholder remains:\n%s", firstPrompt)
	}
}

// 多方向时每个方向各自按配比分配。
func TestGenerateQuestionsV2AllocatesPerDirection(t *testing.T) {
	server := fakeProvider(t, `[
      {"content":"问题甲：编队护航中遭遇可疑目标，请给出处置方案。","difficulty":"easy"},
      {"content":"问题乙：护航海域出现不明目标，请给出处置方案。","difficulty":"medium"}
    ]`)
	input := fakeInput(server, 2, map[string]float64{
		DifficultyEasy: 0.5, DifficultyMedium: 0.5, DifficultyHard: 0,
	})
	// 两个方向必须给出不同内容，否则会被去重掉。
	input.Directions = []DirectionContext{
		{DomainID: 10, DomainName: "编队护航"},
		{DomainID: 11, DomainName: "护航反潜"},
	}

	questions, err := GenerateQuestionsV2(context.Background(), fakeProviderConfig(server), input)
	if err != nil {
		t.Fatalf("generate failed: %v", err)
	}
	if len(questions) != 4 {
		t.Fatalf("generated %d questions, want 4 (2 directions × 2)", len(questions))
	}

	perDirection := map[int64]map[string]int{}
	for _, question := range questions {
		if perDirection[question.DirectionDomainID] == nil {
			perDirection[question.DirectionDomainID] = map[string]int{}
		}
		perDirection[question.DirectionDomainID][question.Difficulty]++
	}
	for _, domainID := range []int64{10, 11} {
		counts := perDirection[domainID]
		if counts[DifficultyEasy] != 1 || counts[DifficultyMedium] != 1 {
			t.Errorf("direction %d counts = %v, want easy=1 medium=1", domainID, counts)
		}
	}
}

// 超时必须足够长：该模型单次响应可达 120 秒。
func TestQuestionGenTimeoutAllowsSlowReasoningModels(t *testing.T) {
	if questionGenTimeout < 120*time.Second {
		t.Fatalf("questionGenTimeout = %s, want >= 120s for reasoning models", questionGenTimeout)
	}
}
