package llm

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestGenerateQuestionsV2DropsPlaceholderContent 锁定 issue #7 的活路径半边。
//
// 背景：question_generator.go（legacy）里有占位判定，但注册表优先使得 legacy
// 分支在生产链路上**不可达**（apps/worker/job_questions_v2.go 注册了
// "questions.generate"）。活路径 question_generator_v2.go 此前只跳过空串，
// 于是模型返回 "..." / "N/A" 时仍会落库为问题。
//
// 本测试喂入「占位 + 正常」混合内容，断言占位被丢弃、正常内容保留。
// 没有它，把判定改回 `if content == ""` 不会让任何测试失败（父代理实测确认过），
// 即修复本身会失去保护。
func TestGenerateQuestionsV2DropsPlaceholderContent(t *testing.T) {
	const normal = "在东海海域执行巡逻任务时遭遇不明目标靠近编队，请给出处置方案。"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []any{map[string]any{
				"message": map[string]any{
					"role": "assistant",
					// 3 个占位 + 1 个正常：占位形态覆盖空串、三点、N/A
					"content": `[{"content":"","difficulty":"easy"},{"content":"...","difficulty":"easy"},{"content":"N/A","difficulty":"easy"},{"content":"` + normal + `","difficulty":"easy"}]`,
				},
			}},
		})
	}))
	defer server.Close()

	input := fakeInput(server, 1, map[string]float64{DifficultyEasy: 1})
	input.Directions = []DirectionContext{{DomainID: 10, DomainName: "编队护航"}}

	questions, err := GenerateQuestionsV2(context.Background(), fakeProviderConfig(server), input)
	if err != nil {
		t.Fatalf("generate failed: %v", err)
	}

	for _, question := range questions {
		if assessment := AssessQuestionContent(question.Content); !assessment.Valid {
			t.Errorf("占位内容被写入结果集：%q（判定原因：%s）", question.Content, assessment.Reason)
		}
	}
	if len(questions) == 0 {
		t.Fatalf("正常内容也被误删：应至少保留 1 条（%q）", normal)
	}
}
