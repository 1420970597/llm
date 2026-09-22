// Package exporter 实现数据集的多格式导出。
//
// 设计目的：把「从数据库取数」与「编码成某种格式」彻底分开。各格式实现只依赖
// 统一中间结构 Record，因此新增格式只需新增一个文件并在 init() 里注册，
// 不需要触碰导出任务、路由或存储层。
package exporter

import (
	"sort"
	"sync"

	"github.com/1420970597/llm/internal/model"
)

// Record 是所有导出格式共用的统一中间结构。
//
// 字段命名与冻结契约 docs/plans/eval-and-cleaning-plan.md 第 3.6 节的
// 字段映射来源保持一致，用户可以在 field_map 里直接引用这些名字。
type Record struct {
	DatasetID      int64
	DatasetName    string
	QuestionID     int64
	Question       string
	ChainOfThought string
	Answer         string
	JudgePrompt    string
	Difficulty     string
	DomainName     string
	RewardScore    float64
	HasReward      bool
	RewardLevels   []string
	// LevelRubrics 是 GRPO 的档位判据（T25）。
	//
	// 用 typed 数组而不是 map/字符串：导出契约要求 `level_rubrics` 是
	// 对象数组且与 `levels` 一一对应，而 typed 结构体的字段顺序确定，
	// 因此「同一份内容两次编码得到相同字节」（T21 的 hash 复算依赖它）。
	LevelRubrics []model.GRPORubricExport
	// FrameworkRef 是 GRPO 判据的框架来源（provenance），可空。
	FrameworkRef string
}

// Exporter 一种导出格式。
type Exporter interface {
	// Format 返回格式标识（同时用于 API 的 formats 列表与任务入参）。
	Format() string
	// Ext 返回导出文件扩展名（含点）。
	Ext() string
	// ContentType 返回写入对象存储时使用的 MIME 类型。
	ContentType() string
	// Encode 把统一记录编码为该格式的字节内容。
	Encode(records []Record, mapping model.ExportMapping) ([]byte, error)
}

// canonicalFormats 冻结契约里 formats 字段的顺序。
// Formats() 按此顺序返回已注册的格式，保证接口响应与契约逐字一致。
var canonicalFormats = []string{"jsonl", "csv", "parquet", "alpaca", "sharegpt"}

var (
	registryMu sync.RWMutex
	registry   = map[string]Exporter{}
)

// Register 注册一种导出格式。由各格式实现在 init() 中调用。
func Register(exp Exporter) {
	if exp == nil || exp.Format() == "" {
		return
	}
	registryMu.Lock()
	defer registryMu.Unlock()
	if _, exists := registry[exp.Format()]; exists {
		panic("duplicate exporter format: " + exp.Format())
	}
	registry[exp.Format()] = exp
}

// Get 按格式标识查找导出器。
func Get(format string) (Exporter, bool) {
	registryMu.RLock()
	defer registryMu.RUnlock()
	exp, ok := registry[format]
	return exp, ok
}

// Formats 返回已注册的格式标识。
//
// 先按契约顺序输出，再把契约之外新增的格式按字典序追加，
// 这样后续扩展格式不会打乱既有接口响应的前五项。
func Formats() []string {
	registryMu.RLock()
	seen := make(map[string]bool, len(registry))
	for name := range registry {
		seen[name] = true
	}
	registryMu.RUnlock()

	out := make([]string, 0, len(seen))
	for _, name := range canonicalFormats {
		if seen[name] {
			out = append(out, name)
			delete(seen, name)
		}
	}
	rest := make([]string, 0, len(seen))
	for name := range seen {
		rest = append(rest, name)
	}
	sort.Strings(rest)
	return append(out, rest...)
}

// Field 一个已解析的目标字段。
type Field struct {
	Key   string
	Value string
	// Raw 非 nil 时优先用于 JSON 编码，以便保留数值等原始类型。
	Raw any
}

// FieldSpec 一条字段映射规则：把 Source 表达式解析后写到 Key 上。
type FieldSpec struct {
	Key    string
	Source string
}

// Resolve 按映射把一条 Record 解析成有序字段列表。
//
// 映射为空时使用 defaults（各格式的内置字段）。映射非空时完全以映射为准，
// 并按目标字段名字典序输出，保证 CSV 表头与 JSON 键序在多次导出间稳定可复现。
func Resolve(record Record, mapping model.ExportMapping, defaults []FieldSpec) ([]Field, error) {
	specs := defaults
	if len(mapping.FieldMap) > 0 {
		keys := make([]string, 0, len(mapping.FieldMap))
		for key := range mapping.FieldMap {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		specs = make([]FieldSpec, 0, len(keys))
		for _, key := range keys {
			expr, ok := mapping.FieldMap[key].(string)
			if !ok {
				return nil, &MappingError{Field: key, Reason: "取值表达式必须是字符串"}
			}
			specs = append(specs, FieldSpec{Key: key, Source: expr})
		}
	}

	out := make([]Field, 0, len(specs))
	for _, spec := range specs {
		value, raw, err := resolveAny(record, spec.Source)
		if err != nil {
			return nil, &MappingError{Field: spec.Key, Reason: err.Error()}
		}
		out = append(out, Field{Key: spec.Key, Value: value, Raw: raw})
	}
	return out, nil
}

// MappingError 字段映射配置错误。带上字段名，便于用户在管理界面直接定位。
type MappingError struct {
	Field  string
	Reason string
}

func (e *MappingError) Error() string {
	return "字段映射 " + e.Field + " 无效: " + e.Reason
}
