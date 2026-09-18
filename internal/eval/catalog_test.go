package eval

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/1420970597/llm/internal/model"
)

func TestBuiltinDimensionsMeetsMinimumCount(t *testing.T) {
	builtin := BuiltinDimensions()
	if len(builtin) < 56 {
		t.Fatalf("内置维度必须 >= 56 个（需求要求不少于 50，留余量），当前 %d 个", len(builtin))
	}
	if err := Validate(builtin); err != nil {
		t.Fatalf("内置维度目录自身校验失败：%v", err)
	}
}

func TestBuiltinDimensionKeysAreUnique(t *testing.T) {
	seen := map[string]int{}
	for _, dim := range BuiltinDimensions() {
		seen[dim.Key]++
	}
	for key, count := range seen {
		if count != 1 {
			t.Fatalf("维度 key %q 出现 %d 次，必须全局唯一", key, count)
		}
	}
	if len(seen) != len(BuiltinDimensions()) {
		t.Fatalf("去重后 key 数 %d 与维度总数 %d 不一致", len(seen), len(BuiltinDimensions()))
	}
}

func TestBuiltinDimensionRubricsAreActionable(t *testing.T) {
	for _, dim := range BuiltinDimensions() {
		rubric := strings.TrimSpace(dim.Rubric)
		if rubric == "" {
			t.Fatalf("维度 %s 的 rubric 为空", dim.Key)
		}
		if length := len([]rune(rubric)); length <= 40 {
			t.Fatalf("维度 %s 的 rubric 只有 %d 个字符，过于简略，必须给出可操作的分档判据", dim.Key, length)
		}
		// 分档判据必须真的分档，而不是一句泛泛描述。
		if !strings.Contains(rubric, "1分") || !strings.Contains(rubric, "5分") {
			t.Fatalf("维度 %s 的 rubric 未给出 1 分与 5 分的具体表现", dim.Key)
		}
		if strings.TrimSpace(dim.Description) == "" {
			t.Fatalf("维度 %s 缺少 description", dim.Key)
		}
	}
}

func TestBuiltinDimensionCategoriesCoverRequiredGroups(t *testing.T) {
	required := []string{
		CategoryLongChain, CategoryFaithfulness, CategoryInstruction,
		CategoryDomainFit, CategoryAnswerQuality, CategoryRobustness, CategoryEfficiency,
	}
	available := map[string]bool{}
	for _, category := range Categories() {
		available[category] = true
	}
	for _, category := range required {
		if !available[category] {
			t.Fatalf("缺少必需的维度分类 %q", category)
		}
	}

	counts := map[string]int{}
	for _, dim := range BuiltinDimensions() {
		counts[dim.Category]++
	}
	for _, category := range required {
		if counts[category] < 5 {
			t.Fatalf("分类 %q 只有 %d 个维度，每类至少需要 5 个", category, counts[category])
		}
	}

	// 每个维度的 category 都必须出现在 Categories() 的返回值里，保证前端分组不漏项。
	for _, dim := range BuiltinDimensions() {
		if !available[dim.Category] {
			t.Fatalf("维度 %s 的分类 %q 未出现在 Categories() 中", dim.Key, dim.Category)
		}
	}
}

func TestCategoriesIsSortedAndDeduplicated(t *testing.T) {
	categories := Categories()
	if len(categories) != len(BuiltinDimensions()) {
		// 分类数必然少于维度数，这里只断言不重复。
	}
	seen := map[string]struct{}{}
	for index, category := range categories {
		if _, exists := seen[category]; exists {
			t.Fatalf("分类 %q 重复出现", category)
		}
		seen[category] = struct{}{}
		if index > 0 && categories[index-1] > category {
			t.Fatalf("分类未按字典序排列：%q 出现在 %q 之后", category, categories[index-1])
		}
	}
}

func TestValidateRejectsDuplicateKey(t *testing.T) {
	bad := []Dimension{
		{Key: "dup", Name: "A", Category: CategoryLongChain, Rubric: strings.Repeat("判据", 30), ScaleMin: 1, ScaleMax: 5, Weight: 1},
		{Key: "dup", Name: "B", Category: CategoryLongChain, Rubric: strings.Repeat("判据", 30), ScaleMin: 1, ScaleMax: 5, Weight: 1},
	}
	for len(bad) < 50 {
		bad = append(bad, Dimension{
			Key: "filler", Name: "F", Category: CategoryLongChain,
			Rubric: strings.Repeat("判据", 30), ScaleMin: 1, ScaleMax: 5, Weight: 1,
		})
	}
	if err := Validate(bad); err == nil {
		t.Fatal("Validate 必须拒绝重复的 key")
	}
}

func TestValidateRejectsShortRubric(t *testing.T) {
	dimensions := make([]Dimension, 0, 50)
	for index := 0; index < 49; index++ {
		dimensions = append(dimensions, Dimension{
			Key: "ok", Name: "N", Category: CategoryLongChain,
			Rubric: strings.Repeat("判据", 30), ScaleMin: 1, ScaleMax: 5, Weight: 1,
		})
	}
	dimensions[0].Key = "short"
	dimensions[0].Rubric = "太短了"
	// 制造 50 个唯一 key
	for index := range dimensions {
		dimensions[index].Key = dimensions[index].Key + string(rune('a'+index%26)) + string(rune('a'+index/26))
	}
	if err := Validate(dimensions); err == nil {
		t.Fatal("Validate 必须拒绝过于简略的 rubric")
	}
}

type fakeDimensionSink struct {
	existing map[string]model.EvalDimension
	inserted int
	failWith error
}

func (f *fakeDimensionSink) UpsertBuiltin(_ context.Context, dimensions []model.EvalDimension) (int, error) {
	if f.failWith != nil {
		return 0, f.failWith
	}
	if f.existing == nil {
		f.existing = map[string]model.EvalDimension{}
	}
	added := 0
	for _, dim := range dimensions {
		if _, exists := f.existing[dim.Key]; exists {
			continue
		}
		f.existing[dim.Key] = dim
		added++
	}
	f.inserted = added
	return added, nil
}

func (f *fakeDimensionSink) Count(_ context.Context) (int, error) {
	if f.failWith != nil {
		return 0, f.failWith
	}
	return len(f.existing), nil
}

func TestSeedIsIdempotent(t *testing.T) {
	sink := &fakeDimensionSink{}

	inserted, total, err := Seed(context.Background(), sink)
	if err != nil {
		t.Fatalf("首次 Seed 失败：%v", err)
	}
	if inserted != len(BuiltinDimensions()) {
		t.Fatalf("首次 Seed 应写入全部 %d 个维度，实际 %d 个", len(BuiltinDimensions()), inserted)
	}
	if total != len(BuiltinDimensions()) {
		t.Fatalf("首次 Seed 后总数应为 %d，实际 %d", len(BuiltinDimensions()), total)
	}

	// 再次 Seed 不得重复写入。
	insertedAgain, totalAgain, err := Seed(context.Background(), sink)
	if err != nil {
		t.Fatalf("二次 Seed 失败：%v", err)
	}
	if insertedAgain != 0 {
		t.Fatalf("二次 Seed 必须幂等（新增 0 条），实际新增 %d 条", insertedAgain)
	}
	if totalAgain != len(BuiltinDimensions()) {
		t.Fatalf("二次 Seed 后总数仍应为 %d，实际 %d", len(BuiltinDimensions()), totalAgain)
	}
}

func TestSeedPropagatesStoreError(t *testing.T) {
	sink := &fakeDimensionSink{failWith: errors.New("db down")}
	if _, _, err := Seed(context.Background(), sink); err == nil {
		t.Fatal("Seed 必须向上传播 store 错误，而不是吞掉")
	}
}

func TestSeedRejectsNilSink(t *testing.T) {
	if _, _, err := Seed(context.Background(), nil); err == nil {
		t.Fatal("Seed 必须拒绝 nil sink")
	}
}

func TestToModelMarksBuiltinAndActive(t *testing.T) {
	for _, dim := range BuiltinDimensions() {
		item := dim.ToModel()
		if !item.IsBuiltin {
			t.Fatalf("维度 %s 转换后必须标记 is_builtin=true", dim.Key)
		}
		if !item.IsActive {
			t.Fatalf("维度 %s 转换后必须标记 is_active=true", dim.Key)
		}
		if item.Key != dim.Key || item.Category != dim.Category {
			t.Fatalf("维度 %s 转换后 key/category 不一致", dim.Key)
		}
		if item.ScaleMin != dim.ScaleMin || item.ScaleMax != dim.ScaleMax {
			t.Fatalf("维度 %s 转换后分值区间不一致", dim.Key)
		}
	}
}
