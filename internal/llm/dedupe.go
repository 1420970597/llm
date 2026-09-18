package llm

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"unicode"
)

// dedupeKeyBytes 是去重键使用的 sha256 前缀长度。
// 16 字节（128 位）在单数据集量级（万级）下碰撞概率可忽略，且键更短便于索引。
const dedupeKeyBytes = 16

// NormalizeForDedupe 把问题文本归一化到「语义不变、写法无关」的形式。
//
// 归一化步骤（顺序有影响）：
//  1. 转小写 —— 消除大小写差异
//  2. 全角转半角 —— 消除「ＡＢ」与「AB」、「，」与「,」的差异
//  3. 删除空白 —— 消除换行、缩进、多余空格的差异
//  4. 删除标点与符号 —— 消除「请做出规划。」与「请做出规划!」的差异
//
// 只保留字母、数字与汉字，因此同一问题的不同书写变体会得到相同结果。
func NormalizeForDedupe(content string) string {
	lowered := strings.ToLower(content)
	var builder strings.Builder
	builder.Grow(len(lowered))
	for _, char := range lowered {
		half := toHalfWidth(char)
		if unicode.IsSpace(half) || unicode.IsPunct(half) || unicode.IsSymbol(half) {
			continue
		}
		builder.WriteRune(half)
	}
	return builder.String()
}

// toHalfWidth 把全角 ASCII（U+FF01..U+FF5E）与全角空格（U+3000）转成半角。
// 其余字符原样返回（汉字、假名等不在该区间内）。
func toHalfWidth(char rune) rune {
	switch {
	case char == '\u3000':
		return ' '
	case char >= '\uFF01' && char <= '\uFF5E':
		return char - 0xFEE0
	default:
		return char
	}
}

// DedupeKey 返回问题内容的内容级去重键。
//
// 与 store.CanonicalHash 的区别：CanonicalHash 只做小写与空白折叠，用于
// questions 表的 UNIQUE(dataset_id, canonical_hash) 约束；DedupeKey 额外
// 消除标点与全角差异，能识别「同一问题的不同标点写法」这类近重复。
// 两者互补：DB 约束兜底精确重复，DedupeKey 在应用层拦截近重复。
func DedupeKey(content string) string {
	normalized := NormalizeForDedupe(content)
	digest := sha256.Sum256([]byte(normalized))
	return hex.EncodeToString(digest[:dedupeKeyBytes])
}
