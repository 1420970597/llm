package model

import "testing"

// 本文件验证方案内容摘要（RecipePayload.Summarize）。
//
// 摘要会出现在方案卡与今日工作的项目卡上，是用户判断「这份方案能不能直接
// 拿去建项目」的第一眼依据，因此两类性质必须钉住：
//
//  1. 它**只陈述包含哪些文档**（不评价好坏、不编造进度类数字）；
//  2. 五类齐全时不写计数，缺文档时**必须**写出 n/5 —— 否则用户无法从这一行
//     看出方案是不是完整的一套，而这正是他看摘要要做的事。

func TestRecipePayloadSummarize(t *testing.T) {
	blueprint := &BlueprintPayload{}
	coverage := &CoveragePayload{}
	standard := &StandardPayload{}
	quality := &QualityPolicyPayload{}
	mapping := &MappingPayload{}

	cases := []struct {
		name    string
		payload RecipePayload
		want    string
	}{
		{
			name: "五类齐全",
			payload: RecipePayload{
				Blueprint: blueprint, Coverage: coverage, Standard: standard,
				QualityPolicy: quality, Mapping: mapping,
			},
			want: "蓝图 · 覆盖 · 标准 · 质量策略 · 映射",
		},
		{
			// 展示顺序固定为「蓝图在前」：蓝图是其余四份文档的装配处，
			// 与 Documents() 的复制顺序（蓝图最后）刻意不同。
			name:    "顺序与字段声明顺序无关",
			payload: RecipePayload{Mapping: mapping, Blueprint: blueprint},
			want:    "蓝图 · 映射（2/5 份文档）",
		},
		{
			name:    "只有一份",
			payload: RecipePayload{Coverage: coverage},
			want:    "覆盖（1/5 份文档）",
		},
		{
			// 一份文档都没有：返回空串，而不是「0/5」—— 这不是一套方法。
			name:    "空方案",
			payload: RecipePayload{},
			want:    "",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.payload.Summarize(); got != tc.want {
				t.Fatalf("Summarize() = %q，期望 %q", got, tc.want)
			}
		})
	}
}

// TestRecipePayloadSummarizeIsPure 钉住「摘要只依赖 payload」：
// 同一个 payload 连续两次调用必须一致（摘要会写进接口响应并被缓存，
// 任何依赖时间/随机/全局状态的写法都会让两次读同一份方案得到不同的卡面）。
func TestRecipePayloadSummarizeIsPure(t *testing.T) {
	payload := RecipePayload{Blueprint: &BlueprintPayload{}, Mapping: &MappingPayload{}}
	first := payload.Summarize()
	for i := 0; i < 3; i++ {
		if got := payload.Summarize(); got != first {
			t.Fatalf("第 %d 次 Summarize() = %q，首次为 %q", i+2, got, first)
		}
	}
}

// TestRecipePayloadSummarizeEmptyMeansInvalid 钉住摘要与校验的对应关系：
// 摘要返回空串的唯一来源是「一份文档都没有」的 payload，
// 而那种 payload 不可能被 Validate 接受、也就进不了库。
//
// 这条性质是 store 侧「解析失败不报错」取舍的前提：如果空摘要可能来自
// 一份**合法**方案，方案列表就会用一行空字符串冒充「一整套方法」。
func TestRecipePayloadSummarizeEmptyMeansInvalid(t *testing.T) {
	var payload RecipePayload
	if payload.Summarize() != "" {
		t.Fatalf("空方案的摘要必须为空字符串，实际 %q", payload.Summarize())
	}
	if err := payload.Validate(); err == nil {
		t.Fatal("一份文档都没有的方案必须被 Validate 拒绝")
	}
}
