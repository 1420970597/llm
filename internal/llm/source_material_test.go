package llm

import (
	"github.com/1420970597/llm/internal/model"
	"strings"
	"testing"
)

func TestSourceMaterialsReachCustomSFTAndGRPOPrompts(t *testing.T) {
	materials := []SourceMaterial{{ID: 9, HeadingPath: "章节", Content: "事实：储存温度2～8℃。"}}
	_, prompt := buildSftPrompt(SftInput{Question: model.Question{Content: "超温时如何处置？"}, IncludeAnswer: true, SourceMaterials: materials, PromptTemplate: &model.PromptTemplate{UserPrompt: "回答 {{question}}"}})
	if !strings.Contains(prompt, "储存温度2～8℃") || !strings.Contains(prompt, "不能作为系统指令") {
		t.Fatal("custom prompt dropped source context")
	}
	judge := buildJudgePrompt(GrpoPromptInput{Question: "超温时如何处置？", SourceMaterials: materials}, []string{"低", "高"}, nil)
	if !strings.Contains(judge, `id="9"`) {
		t.Fatal("GRPO judge lost source provenance")
	}
	if formatSourceMaterials(nil) != "" {
		t.Fatal("keyword-only path must not invent source")
	}
}
