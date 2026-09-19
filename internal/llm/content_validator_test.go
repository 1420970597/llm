package llm

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/1420970597/llm/internal/model"
)

// 本文件锁定 issue #7 的核心行为：模型返回了**合法 JSON**，但字段内容是占位符时，
// 记录必须被判为 invalid，而不是 generated。
//
// 所有用例都真实调用被测的生成函数（GenerateReasoning / GenerateRewards /
// GenerateQuestions），从假的 OpenAI 兼容服务取回响应，走完
// 「HTTP → 解码 → JSON 解析 → 内容校验 → 状态判定」整条真实路径。
// 校验逻辑不在测试里重写一遍。

// validReasoningText 是一段长度与信息量都合格的典型长链思考文本。
// 生产库中合法推理记录的量级为千字符（实测 2004），这里取相近量级。
const validReasoningText = "首先确认巡逻区域的地理与水文条件，包括航道宽度、水深与季风影响。" +
	"其次评估编队可用兵力与续航，按任务优先级把护卫舰分配到外围警戒位，补给舰居中。" +
	"随后判断对方可能的接触方式，若为水面目标则保持电磁静默并抢占上风位；若为水下目标则改走浅水区。" +
	"最后为每种接触方式预留撤离航向与备选集合点，确保任一环节失效都有一条可行的替代方案。"

// validAnswerText 是合格的答案摘要。
const validAnswerText = "按地理与水文条件确定巡逻航线，按任务优先级分配编队阵位，针对水面与水下两类接触分别预设应对与撤离方案。"

// validRationaleText 是合格的评分理由。
const validRationaleText = "答案覆盖了地理条件、兵力分配与威胁应对三个必要环节，并给出了可执行的撤离方案，论证链条完整且与标准步骤逐条对应。"

// payloadProvider 起一个假的 OpenAI 兼容服务，返回给定的 content。
//
// 与 question_generator_v2_integration_test.go 的 fakeProvider 区别：
// 这里额外断言请求确实到达过（调用计数），避免「函数没发请求就返回」的假通过。
func payloadProvider(t *testing.T, content string, calls *int) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/chat/completions") {
			http.NotFound(w, r)
			return
		}
		if calls != nil {
			*calls++
		}
		payload := map[string]any{
			"choices": []any{map[string]any{
				"message":       map[string]any{"role": "assistant", "content": content},
				"finish_reason": "stop",
			}},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(payload)
	}))
	t.Cleanup(server.Close)
	return server
}

func payloadProviderConfig(server *httptest.Server) ProviderConfig {
	return ProviderConfig{
		BaseURL:      server.URL,
		Model:        "test-model",
		ProviderType: "openai-compatible",
		APIKey:       "test-key",
	}
}

func testDataset() model.Dataset {
	return model.Dataset{ID: 1, RootKeyword: "海上巡逻"}
}

func testQuestion() model.Question {
	return model.Question{ID: 7, Content: "在某某位置有某某单位巡逻，遇到突发情况，请做出规划。"}
}

// TestGenerateReasoningRejectsPlaceholderContent 是 issue #7 的回归用例。
//
// 表中每一条都是**合法 JSON**，此前会被判为 generated。修复后必须判为 invalid。
func TestGenerateReasoningRejectsPlaceholderContent(t *testing.T) {
	cases := []struct {
		name    string
		content string
	}{
		{"模型返回三个点（issue #7 实测形状）", `{"answer":"...","reasoning":"..."}`},
		{"两字段皆空串", `{"answer":"","reasoning":""}`},
		{"显式占位 N/A", `{"answer":"N/A","reasoning":"N/A"}`},
		{"中文占位", `{"answer":"待补充","reasoning":"暂无"}`},
		{"占位点无内容字符", `{"answer":"。。。","reasoning":"---"}`},
		{"answer 正常但 reasoning 是占位", `{"answer":"` + validAnswerText + `","reasoning":"..."}`},
		{"reasoning 正常但 answer 是占位", `{"answer":"...","reasoning":"` + validReasoningText + `"}`},
		{"reasoning 长度不足", `{"answer":"` + validAnswerText + `","reasoning":"模型没有给出长链思考"}`},
		{
			"reasoning 与 answer 雷同",
			`{"answer":"` + validAnswerText + `","reasoning":"` + validAnswerText + `"}`,
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			calls := 0
			server := payloadProvider(t, testCase.content, &calls)

			records, _, err := GenerateReasoning(context.Background(), payloadProviderConfig(server),
				testDataset(), []model.Question{testQuestion()}, nil)

			if calls == 0 {
				t.Fatal("生成函数没有发出请求，测试无效")
			}
			if err != nil {
				t.Fatalf("GenerateReasoning 不应返回整批错误（单条不合格只标状态）: %v", err)
			}
			if len(records) != 1 {
				t.Fatalf("应产出 1 条记录，得到 %d", len(records))
			}
			if records[0].Status != ContentStatusInvalid {
				t.Fatalf("占位内容必须判为 %q，实际 %q", ContentStatusInvalid, records[0].Status)
			}
			if records[0].Status == "generated" {
				t.Fatal("占位内容绝不能判为 generated")
			}
		})
	}
}

// TestGenerateReasoningAcceptsValidContent 保证修复没有把正常长文本也拦掉。
func TestGenerateReasoningAcceptsValidContent(t *testing.T) {
	content := `{"answer":"` + validAnswerText + `","reasoning":"` + validReasoningText + `"}`
	calls := 0
	server := payloadProvider(t, content, &calls)

	records, payloads, err := GenerateReasoning(context.Background(), payloadProviderConfig(server),
		testDataset(), []model.Question{testQuestion()}, nil)
	if err != nil {
		t.Fatalf("合法内容不应报错: %v", err)
	}
	if calls == 0 {
		t.Fatal("生成函数没有发出请求，测试无效")
	}
	if len(records) != 1 {
		t.Fatalf("应产出 1 条记录，得到 %d", len(records))
	}
	if records[0].Status != "generated" {
		t.Fatalf("合法长文本必须判为 generated，实际 %q", records[0].Status)
	}
	if records[0].Reasoning != validReasoningText {
		t.Fatalf("reasoning 应原样保留，实际 %q", records[0].Reasoning)
	}
	if payloads[7].Reasoning != validReasoningText {
		t.Fatal("payload 应保留模型原始输出")
	}
}

// TestGenerateReasoningMarksTransportFailureAsFailed 锁定 failed 语义不变：
// 网络/协议层失败仍然是 failed，不能与「模型摆烂」的 invalid 混淆。
func TestGenerateReasoningMarksTransportFailureAsFailed(t *testing.T) {
	cases := []struct {
		name    string
		handler http.HandlerFunc
	}{
		{"服务端 500", func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, `{"error":"boom"}`, http.StatusInternalServerError)
		}},
		{"返回非 JSON 纯文本", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/plain")
			_, _ = w.Write([]byte("抱歉，我无法完成这个请求。"))
		}},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			server := httptest.NewServer(testCase.handler)
			t.Cleanup(server.Close)

			records, _, err := GenerateReasoning(context.Background(), payloadProviderConfig(server),
				testDataset(), []model.Question{testQuestion()}, nil)
			if err != nil {
				t.Fatalf("单条失败不应返回整批错误: %v", err)
			}
			if len(records) != 1 {
				t.Fatalf("应产出 1 条记录，得到 %d", len(records))
			}
			if records[0].Status != "failed" {
				t.Fatalf("传输/解析失败必须判为 failed，实际 %q", records[0].Status)
			}
		})
	}
}

// TestGenerateReasoningReturnsInvalidContentError 锁定可直接用 errors.Is 判断，
// 使「invalid」与「failed」在代码层面可分。
func TestGenerateReasoningReturnsInvalidContentError(t *testing.T) {
	server := payloadProvider(t, `{"answer":"...","reasoning":"..."}`, nil)

	_, err := generateReasoningForQuestion(context.Background(), payloadProviderConfig(server),
		testDataset(), testQuestion(), nil)
	if err == nil {
		t.Fatal("占位内容必须返回错误")
	}
	if !errors.Is(err, ErrInvalidContent) {
		t.Fatalf("错误必须可被 errors.Is(ErrInvalidContent) 识别，实际 %v", err)
	}
	var invalidError invalidContentError
	if !errors.As(err, &invalidError) {
		t.Fatalf("错误必须携带字段名与原因，实际 %T", err)
	}
	if invalidError.Field() == "" || invalidError.Reason() == "" {
		t.Fatalf("字段名与原因都不应为空: %+v", invalidError)
	}
}

// TestGenerateRewardsRejectsPlaceholderContent 对 reward 路径给出同等判定。
func TestGenerateRewardsRejectsPlaceholderContent(t *testing.T) {
	cases := []struct {
		name    string
		content string
	}{
		{"理由为三个点", `{"score":0.5,"rationale":"..."}`},
		{"理由为空", `{"score":0.5,"rationale":""}`},
		{"理由为 N/A", `{"score":0.5,"rationale":"N/A"}`},
		{"理由长度不足", `{"score":0.5,"rationale":"还不错"}`},
		{"理由为中文占位", `{"score":0.5,"rationale":"待补充"}`},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			calls := 0
			server := payloadProvider(t, testCase.content, &calls)

			records, _, err := GenerateRewards(context.Background(), payloadProviderConfig(server),
				testDataset(), []model.Question{testQuestion()}, nil)
			if err != nil {
				t.Fatalf("单条不合格不应返回整批错误: %v", err)
			}
			if calls == 0 {
				t.Fatal("生成函数没有发出请求，测试无效")
			}
			if len(records) != 1 {
				t.Fatalf("应产出 1 条记录，得到 %d", len(records))
			}
			if records[0].Status != ContentStatusInvalid {
				t.Fatalf("占位理由必须判为 %q，实际 %q", ContentStatusInvalid, records[0].Status)
			}
		})
	}
}

// TestGenerateRewardsAcceptsValidContent 与 TestGenerateRewardsMarksFailureAsFailed
// 分别锁定 reward 路径的正例与 failed 语义。
func TestGenerateRewardsAcceptsValidContent(t *testing.T) {
	server := payloadProvider(t, `{"score":0.82,"rationale":"`+validRationaleText+`"}`, nil)

	records, _, err := GenerateRewards(context.Background(), payloadProviderConfig(server),
		testDataset(), []model.Question{testQuestion()}, nil)
	if err != nil {
		t.Fatalf("合法理由不应报错: %v", err)
	}
	if len(records) != 1 || records[0].Status != "generated" {
		t.Fatalf("合法理由应判为 generated，实际 %+v", records)
	}
	if records[0].Score != 0.82 {
		t.Fatalf("分数应保留，实际 %v", records[0].Score)
	}
}

func TestGenerateRewardsMarksFailureAsFailed(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":"boom"}`, http.StatusInternalServerError)
	}))
	t.Cleanup(server.Close)

	records, _, err := GenerateRewards(context.Background(), payloadProviderConfig(server),
		testDataset(), []model.Question{testQuestion()}, nil)
	if err != nil {
		t.Fatalf("单条失败不应返回整批错误: %v", err)
	}
	if len(records) != 1 || records[0].Status != "failed" {
		t.Fatalf("传输失败必须判为 failed，实际 %+v", records)
	}
}

// TestGenerateQuestionsDropsPlaceholderContent 锁定问题路径的行为：
// 占位问题被丢弃，不进训练集。
//
// 为什么是丢弃而不是标 invalid：questions.status 由 store 层硬编码为
// 'generated'（pipeline_store.go / question_store_v2.go），没有 invalid 通道。
func TestGenerateQuestionsDropsPlaceholderContent(t *testing.T) {
	mixed := `["...","在某某位置有某某单位巡逻，遇到突发情况，请做出规划。","N/A","待补充"]`
	server := payloadProvider(t, mixed, nil)

	dataset := testDataset()
	dataset.Estimate.QuestionsPerDomain = 4
	domains := []model.Domain{{ID: 10, DatasetID: 1, Name: "海上巡逻"}}

	questions, err := GenerateQuestions(context.Background(), payloadProviderConfig(server), dataset, domains, nil)
	if err != nil {
		t.Fatalf("部分不合格不应返回整批错误: %v", err)
	}
	if len(questions) != 1 {
		t.Fatalf("应只保留 1 条有效问题，实际 %d 条: %+v", len(questions), questions)
	}
	if questions[0].Content != "在某某位置有某某单位巡逻，遇到突发情况，请做出规划。" {
		t.Fatalf("保留的应是有效那条，实际 %q", questions[0].Content)
	}
	for _, question := range questions {
		if question.Content == "..." || question.Content == "N/A" || question.Content == "待补充" {
			t.Fatalf("占位问题不得入库: %q", question.Content)
		}
	}
}

// TestGenerateQuestionsRejectsAllPlaceholderContent 覆盖「整批都是占位」：
// 必须报错（可被 errors.Is 识别为 invalid），不能返回空切片让数据集空推进。
func TestGenerateQuestionsRejectsAllPlaceholderContent(t *testing.T) {
	server := payloadProvider(t, `["...","N/A"]`, nil)

	dataset := testDataset()
	dataset.Estimate.QuestionsPerDomain = 2
	domains := []model.Domain{{ID: 10, DatasetID: 1, Name: "海上巡逻"}}

	_, err := GenerateQuestions(context.Background(), payloadProviderConfig(server), dataset, domains, nil)
	if err == nil {
		t.Fatal("整批占位必须报错，不能静默返回空切片")
	}
	if !errors.Is(err, ErrInvalidContent) {
		t.Fatalf("错误必须可被 errors.Is(ErrInvalidContent) 识别，实际 %v", err)
	}
}

// TestAssessReasoningContentTable 直接锁定内容判定的边界。
//
// 前一部分的用例都走完整生成路径（慢且需要 HTTP）；这里补边界，包括
// 长度阈值附近的取值，确保阈值本身被测试而非只被使用。
func TestAssessReasoningContentTable(t *testing.T) {
	answer := validAnswerText
	reasoning := validReasoningText

	cases := []struct {
		name      string
		answer    string
		reasoning string
		valid     bool
	}{
		{"正常内容", answer, reasoning, true},
		{"answer 为空", "", reasoning, false},
		{"reasoning 为空", answer, "", false},
		{"两者都是空白字符", "   ", "\n\t ", false},
		{"两者都是三个点", "...", "...", false},
		{"两者都是省略号", "。。。", "。。。", false},
		{"answer 是 N/A", "N/A", reasoning, false},
		{"reasoning 是 TODO", answer, "TODO", false},
		{"reasoning 恰好达到下限", answer, strings.Repeat("思", minReasoningRunes), true},
		{"reasoning 比下限少一个字", answer, strings.Repeat("思", minReasoningRunes-1), false},
		{"answer 恰好达到下限", strings.Repeat("答", minAnswerRunes), reasoning, true},
		{"answer 比下限少一个字", strings.Repeat("答", minAnswerRunes-1), reasoning, false},
		{"重复内容（长度合格但自我复制）", answer, answer, false},
		{"重复内容仅标点不同", answer, answer + "。", false},
		{"有效字符太少", strings.Repeat("!", 200), reasoning, false},
		{"与 answer 不同但都很短", "短答案", "短思考", false},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			assessment := AssessReasoningContent(testCase.answer, testCase.reasoning)
			if assessment.Valid != testCase.valid {
				t.Fatalf("Valid 应为 %v，实际 %v（原因：%s）", testCase.valid, assessment.Valid, assessment.Reason)
			}
			if testCase.valid && assessment.Reason != "" {
				t.Fatalf("合格时不应带原因，实际 %q", assessment.Reason)
			}
			if !testCase.valid {
				if assessment.Reason == "" {
					t.Fatal("不合格必须给出原因")
				}
				if assessment.Field == "" {
					t.Fatal("不合格必须给出字段名")
				}
			}
		})
	}
}

// TestAssessQuestionContentTable 锁定问题文本的判定边界。
func TestAssessQuestionContentTable(t *testing.T) {
	cases := []struct {
		name    string
		content string
		valid   bool
	}{
		{"正常中文问题", "在某某位置有某某单位巡逻，遇到突发情况，请做出规划。", true},
		{"空串", "", false},
		{"空白", "    ", false},
		{"三个点", "...", false},
		{"N/A", "N/A", false},
		{"待定", "待定", false},
		{"比下限少一个字", strings.Repeat("问", minQuestionRunes-1), false},
		{"恰好达到下限", strings.Repeat("问", minQuestionRunes), true},
		{"只有标点符号", "？？？？？？？？？？？？？？", false},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			assessment := AssessQuestionContent(testCase.content)
			if assessment.Valid != testCase.valid {
				t.Fatalf("Valid 应为 %v，实际 %v（原因：%s）", testCase.valid, assessment.Valid, assessment.Reason)
			}
		})
	}
}

// TestAssessRewardContentRejectsPlaceholders 锁定评分理由的占位判定。
func TestAssessRewardContentRejectsPlaceholders(t *testing.T) {
	for _, placeholder := range []string{"", "...", "N/A", "TODO", "待补充", "无", "不清楚"} {
		if assessment := AssessRewardContent(placeholder); assessment.Valid {
			t.Fatalf("占位理由 %q 必须不合格", placeholder)
		}
	}
	if assessment := AssessRewardContent(validRationaleText); !assessment.Valid {
		t.Fatalf("合法理由必须合格，实际原因：%s", assessment.Reason)
	}
}

// TestReasoningStatusInvalidAliasMatchesContract 锁定契约 §1.3 指定的符号存在，
// 且与内部使用的取值是同一个拼写 —— 避免两处漂移。
func TestReasoningStatusInvalidAliasMatchesContract(t *testing.T) {
	if ReasoningStatusInvalid != "invalid" {
		t.Fatalf("契约要求取值为 \"invalid\"，实际 %q", ReasoningStatusInvalid)
	}
	if ReasoningStatusInvalid != ContentStatusInvalid {
		t.Fatalf("两个名字必须指向同一取值：%q vs %q", ReasoningStatusInvalid, ContentStatusInvalid)
	}
}

// TestStatusColumnsAllowInvalidWithoutNewMigration 证明新增 invalid 取值不需要新迁移。
//
// 契约 §1.3 要求 lane 自行核实这一点并在测试里证明。这里用源码级断言锁定：
// reasoning_records.status 与 reward_records.status 均为 TEXT、无 CHECK 约束，
// 因此任何字符串取值都能写入。
//
// 之所以不写成「连数据库查 pg_constraint」：单元测试不应依赖外部 Postgres。
// 活体验证（psql 查 pg_constraint）作为独立证据写在 PR 描述里。
func TestStatusColumnsAllowInvalidWithoutNewMigration(t *testing.T) {
	for _, testCase := range []struct {
		migration string
		table     string
	}{
		{"0005_reasoning.sql", "reasoning_records"},
		{"0006_rewards.sql", "reward_records"},
	} {
		t.Run(testCase.table, func(t *testing.T) {
			sqlBytes, err := os.ReadFile(filepath.Join("..", "..", "sql", "migrations", testCase.migration))
			if err != nil {
				t.Fatalf("读取迁移 %s 失败: %v", testCase.migration, err)
			}
			sqlText := string(sqlBytes)

			// 定位到 status 列定义，确认类型是 TEXT（不是枚举或带 CHECK 的类型）。
			statusLine := "status TEXT NOT NULL DEFAULT 'generated'"
			if !strings.Contains(sqlText, statusLine) {
				t.Fatalf("%s 的 status 列必须是 %q，以保证可写入 invalid",
					testCase.migration, statusLine)
			}
			if strings.Contains(strings.ToUpper(sqlText), "CHECK") {
				t.Fatalf("%s 出现 CHECK 约束：新增 invalid 取值将需要新迁移", testCase.migration)
			}
		})
	}
}
