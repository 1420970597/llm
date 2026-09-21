package model

import (
	"reflect"
	"sort"
	"strings"
	"testing"
)

// 本文件是 Issue #160 T11 的节点元数据**反漂移测试**。
//
// 契约 §5 的节点表有两份「事实」：typed payload 结构体（服务端校验用）
// 与节点元数据（界面渲染检查器用）。两份事实必然漂移，除非有东西把
// 它们钉在一起 —— 那就是本文件。
//
// 两个方向的漂移后果不同，但都很难发现：
//   - 结构体加了字段、元数据没跟上 → 前端看不到它，用户无法配置，
//     而「能配的都配了」看起来一切正常；
//   - 元数据写了不存在的字段 → 前端渲染一个永远读不到值的输入框，
//     用户填了但保存后丢失。

// TestBlueprintNodeSpecsCoverPayload 双向断言元数据与 payload 结构体一致。
func TestBlueprintNodeSpecsCoverPayload(t *testing.T) {
	payload := BlueprintPayload{}
	payloadType := reflect.TypeOf(payload.Nodes)

	covered := map[string]string{}
	for _, spec := range BlueprintNodeSpecs() {
		t.Run(spec.Key, func(t *testing.T) {
			field, found := structFieldByJSONTag(payloadType, spec.PayloadField)
			if !found {
				t.Fatalf("元数据里的节点 %q 声明的 payloadField %q 在 BlueprintPayload.Nodes 里不存在",
					spec.Key, spec.PayloadField)
			}
			if previous, exists := covered[spec.PayloadField]; exists {
				t.Fatalf("payloadField %q 被节点 %q 与 %q 同时声明（两处写入同一份配置会互相覆盖）",
					spec.PayloadField, previous, spec.Key)
			}
			covered[spec.PayloadField] = spec.Key
			if spec.PayloadField == "" {
				t.Fatal("节点必须声明 payloadField：key 是 snake_case 的 URL 键，两者不能混用")
			}
			want := jsonFieldNamesOf(field.Type)
			got := spec.NodeFieldNames()
			if !reflect.DeepEqual(want, got) {
				t.Fatalf("节点 %s 的字段与 payload 结构体不一致：\n元数据：%v\n结构体：%v", spec.Key, got, want)
			}
			if len(spec.Fields) == 0 {
				t.Fatalf("节点 %s 没有任何字段，界面会渲染一个空检查器", spec.Key)
			}
			if spec.Key == spec.PayloadField {
				// 允许相等，但必须是有意的：这里只在两者都出现「下划线/驼峰混用」
				// 时提示，因为那是最容易写错的一类。
				if strings.Contains(spec.Key, "_") {
					t.Logf("节点 %s 的键与 payload 字段同名且含下划线，请确认这是有意的", spec.Key)
				}
			}
			// 字段本身必须能渲染：有标签，且数值类有范围或明确说明。
			for _, item := range spec.Fields {
				if item.Label == "" {
					t.Fatalf("节点 %s 的字段 %q 缺少中文标签", spec.Key, item.Name)
				}
				if item.Kind == "" {
					t.Fatalf("节点 %s 的字段 %q 缺少控件类型", spec.Key, item.Name)
				}
				if item.Kind == FieldKindEnum && len(item.Options) == 0 {
					t.Fatalf("节点 %s 的枚举字段 %q 没有可选项，界面无法渲染", spec.Key, item.Name)
				}
			}
		})
	}
}

// TestBlueprintNodeKeysMatchSpecs 断言键集合与顺序与 T04 冻结的常量一致。
//
// 顺序也有意义：前端按它渲染画布，而「视觉顺序与 Tab 顺序一致」
// 是蓝图页的可访问性要求（与 BlueprintNodeKeys 的注释一致）。
func TestBlueprintNodeKeysMatchSpecs(t *testing.T) {
	want := BlueprintNodeKeys()
	got := make([]string, 0, len(want))
	for _, spec := range BlueprintNodeSpecs() {
		got = append(got, spec.Key)
	}
	if !reflect.DeepEqual(want, got) {
		t.Fatalf("节点键与顺序必须与 BlueprintNodeKeys() 一致：\n常量：%v\n元数据：%v", want, got)
	}

	// 反向：Nodes 结构体里的每个字段都必须被某个节点声明覆盖，
	// 否则那个节点在界面上根本不存在（用户无法配置它）。
	payloadType := reflect.TypeOf(BlueprintPayload{}.Nodes)
	declared := map[string]bool{}
	for _, spec := range BlueprintNodeSpecs() {
		declared[spec.PayloadField] = true
	}
	for index := 0; index < payloadType.NumField(); index++ {
		name := jsonTagName(payloadType.Field(index))
		if !declared[name] {
			t.Fatalf("payload 节点字段 %q 没有任何节点元数据覆盖：它在界面上不可配置", name)
		}
	}
}

// jsonTagName 读出一个字段的 JSON 名。
func jsonTagName(field reflect.StructField) string {
	tag := field.Tag.Get("json")
	if comma := indexOfByte(tag, ','); comma >= 0 {
		return tag[:comma]
	}
	return tag
}

// TestBlueprintNodeSpecByKeyRejectsUnknownNodes 覆盖 §5「禁止任意脚本节点」。
//
// 不存在的节点必须能被区分出来（返回 false），这样 API 可以返回 404
// 而不是渲染一个空表单 —— 后者会让用户以为「这个节点就是没有配置项」。
func TestBlueprintNodeSpecByKeyRejectsUnknownNodes(t *testing.T) {
	if _, found := BlueprintNodeSpecByKey("script"); found {
		t.Fatal("任意脚本节点必须不被承认（§5 明确禁止）")
	}
	if _, found := BlueprintNodeSpecByKey(""); found {
		t.Fatal("空节点键必须不被承认")
	}
	if _, found := BlueprintNodeSpecByKey("humanReview"); found {
		t.Fatal("节点键必须用冻结的常量形式（human_review），不接受另一种拼写")
	}
	spec, found := BlueprintNodeSpecByKey(" human_review ")
	if !found || spec.Key != BlueprintNodeHumanReview {
		t.Fatalf("带空白的合法键应当被接受，实际 found=%v key=%q", found, spec.Key)
	}
}

// TestCheckNodeRequirementsOnlyBlocksAtExecution 覆盖 §5 与 T11 的关键区分：
// **保存允许不完整，执行不允许**。
func TestCheckNodeRequirementsOnlyBlocksAtExecution(t *testing.T) {
	// 生成节点的必填项：连接、schema、并发、输出上限。
	missing := CheckNodeRequirements(BlueprintNodeGeneration, map[string]any{})
	if len(missing) != 4 {
		t.Fatalf("空配置应当报出 4 项必填缺失，实际 %d：%+v", len(missing), missing)
	}
	for _, item := range missing {
		if item.Label == "" || item.Reason == "" {
			t.Fatalf("缺失项必须带可展示的标签与原因（界面据此定位字段）：%+v", item)
		}
	}

	// 齐全时不得报缺失。
	complete := map[string]any{
		"modelConnectionId": int64(3),
		"schemaVersion":     "sft.sample.v1",
		"concurrency":       8,
		"maxTokens":         4096,
	}
	if got := CheckNodeRequirements(BlueprintNodeGeneration, complete); len(got) != 0 {
		t.Fatalf("必填齐全时不应报缺失，实际 %+v", got)
	}

	// `samplingSeed=0` 是**合法取值**（默认 seed），不得被当成「未设置」。
	// 这一条正是「用零值判空」的经典陷阱：把 0 当空会让用户无法显式使用
	// 默认 seed，而错误信息会说「必须设置抽样 seed」。
	seedZero := CheckNodeRequirements(BlueprintNodeEvaluation, map[string]any{
		"judgeConnectionIds": []int64{5},
		"rubricVersionId":    int64(2),
		"samplingSeed":       0,
	})
	for _, item := range seedZero {
		if item.Field == "samplingSeed" {
			t.Fatal("samplingSeed=0 是合法默认值，不得被判为缺失")
		}
	}

	// 空数组算缺失（必需证据集定义为空等于没有门槛）。
	emptyEvidence := CheckNodeRequirements(BlueprintNodeHumanReview, map[string]any{
		"assignment":       "risk-based",
		"requiredEvidence": []string{},
	})
	foundEvidence := false
	for _, item := range emptyEvidence {
		if item.Field == "requiredEvidence" {
			foundEvidence = true
		}
	}
	if !foundEvidence {
		t.Fatal("空的必需证据集必须被判为缺失：它会让发布门槛形同不存在")
	}

	// 不存在的节点必须被显式报出，而不是静默返回空清单。
	unknown := CheckNodeRequirements("script", map[string]any{})
	if len(unknown) != 1 || unknown[0].Field != "" {
		t.Fatalf("不存在的节点必须报错，实际 %+v", unknown)
	}
}

// TestGenerationNodeFieldsMatchFrozenLimits 覆盖 §5 的并发 1–32 与 T07 的
// 「必须显式设置输出上限」。
func TestGenerationNodeFieldsMatchFrozenLimits(t *testing.T) {
	spec, found := BlueprintNodeSpecByKey(BlueprintNodeGeneration)
	if !found {
		t.Fatal("生成节点必须存在")
	}
	byName := map[string]NodeFieldSpec{}
	for _, field := range spec.Fields {
		byName[field.Name] = field
	}

	concurrency := byName["concurrency"]
	if concurrency.Min != float64(MinGenerationConcurrency) || concurrency.Max != float64(MaxGenerationConcurrency) {
		t.Fatalf("并发范围必须与冻结常量一致：元数据 %v–%v，常量 %d–%d",
			concurrency.Min, concurrency.Max, MinGenerationConcurrency, MaxGenerationConcurrency)
	}
	if !byName["maxTokens"].Required {
		t.Fatal("输出上限必须是执行前必填：没有它无法界定单次调用风险，也就无法可靠预留预算（T07）")
	}
	// 温度不得是必填：推理型模型会拒绝该参数（能力声明为不支持时要能留空）。
	if byName["temperature"].Required {
		t.Fatal("温度不得必填：推理型模型不支持它，必填会让这类项目无法执行")
	}
	// 交付格式必须排除 parquet（T04 已记录：它实为列式 JSONL）。
	delivery, _ := BlueprintNodeSpecByKey(BlueprintNodeDelivery)
	for _, field := range delivery.Fields {
		if field.Name != "format" {
			continue
		}
		for _, option := range field.Options {
			if option == "parquet" {
				t.Fatal("交付格式不得包含 parquet（internal/exporter 的 parquet 实为列式 JSONL）")
			}
		}
	}
}

// jsonFieldNamesOf 读出结构体的 JSON 字段名（升序）。
func jsonFieldNamesOf(typ reflect.Type) []string {
	names := make([]string, 0, typ.NumField())
	for index := 0; index < typ.NumField(); index++ {
		field := typ.Field(index)
		if !field.IsExported() {
			continue
		}
		tag := field.Tag.Get("json")
		name := tag
		if comma := indexOfByte(tag, ','); comma >= 0 {
			name = tag[:comma]
		}
		if name == "" || name == "-" {
			name = field.Name
		}
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// structFieldByJSONTag 按 JSON tag 找字段。
func structFieldByJSONTag(typ reflect.Type, tag string) (reflect.StructField, bool) {
	for index := 0; index < typ.NumField(); index++ {
		field := typ.Field(index)
		name := field.Tag.Get("json")
		if comma := indexOfByte(name, ','); comma >= 0 {
			name = name[:comma]
		}
		if name == tag {
			return field, true
		}
	}
	return reflect.StructField{}, false
}

func indexOfByte(text string, target byte) int {
	for index := 0; index < len(text); index++ {
		if text[index] == target {
			return index
		}
	}
	return -1
}
