package llm

import "testing"

// 同一问题的不同书写变体必须得到相同去重键。
func TestDedupeKeyCollapsesPunctuationWhitespaceAndWidth(t *testing.T) {
	variants := []string{
		"在东海海域有巡逻编队遇到不明目标，请做出规划。",
		"在东海海域有巡逻编队遇到不明目标，请做出规划",
		"在东海海域有巡逻编队遇到不明目标, 请做出规划!",
		"在东海海域有巡逻编队遇到不明目标，请做出规划！",
		"在东海海域有巡逻编队遇到不明目标\n请做出规划。",
		"  在东海海域有巡逻编队遇到不明目标，请做出规划。  ",
		"在东海海域有巡逻编队遇到不明目标，请做出规划。",
	}
	want := DedupeKey(variants[0])
	if want == "" {
		t.Fatal("dedupe key must not be empty")
	}
	for _, variant := range variants[1:] {
		if got := DedupeKey(variant); got != want {
			t.Errorf("variant %q produced %s, want %s (same as %q)", variant, got, want, variants[0])
		}
	}
}

// 全角与半角字符必须被归一化到同一键。
func TestDedupeKeyTreatsFullWidthAsHalfWidth(t *testing.T) {
	if DedupeKey("AB12") != DedupeKey("ＡＢ１２") {
		t.Fatalf("full-width variant must collapse: %s vs %s", DedupeKey("AB12"), DedupeKey("ＡＢ１２"))
	}
}

// 不同问题必须得到不同键，避免过度归一化把语义不同的内容判为重复。
func TestDedupeKeyDistinguishesDifferentQuestions(t *testing.T) {
	first := DedupeKey("在东海海域有巡逻编队遇到不明目标，请做出规划。")
	second := DedupeKey("在南海海域有巡逻编队遇到不明目标，请做出规划。")
	if first == second {
		t.Fatalf("different questions collided: %s", first)
	}
}

// 键格式固定：sha256 前 16 字节的 hex，共 32 个字符。
func TestDedupeKeyFormat(t *testing.T) {
	key := DedupeKey("任意内容")
	if len(key) != dedupeKeyBytes*2 {
		t.Fatalf("dedupe key length = %d, want %d", len(key), dedupeKeyBytes*2)
	}
	for _, char := range key {
		if !((char >= '0' && char <= '9') || (char >= 'a' && char <= 'f')) {
			t.Fatalf("dedupe key %q contains non-hex character %q", key, char)
		}
	}
}

// 纯标点差异的内容归一化后为空串，仍然要产生稳定的非空键（不能 panic 或返回空）。
func TestDedupeKeyOnEmptyNormalization(t *testing.T) {
	key := DedupeKey("。！？  ")
	if key == "" {
		t.Fatal("dedupe key for punctuation-only content must not be empty")
	}
	if key != DedupeKey("!!!") {
		t.Fatalf("all-punctuation contents must share one key: %s vs %s", key, DedupeKey("!!!"))
	}
}

func TestNormalizeForDedupeKeepsLettersDigitsAndHan(t *testing.T) {
	got := NormalizeForDedupe("AB 12，东海！")
	want := "ab12东海"
	if got != want {
		t.Fatalf("NormalizeForDedupe = %q, want %q", got, want)
	}
}
