package main

import "testing"

func TestJudgeOutputLimitDefaultsAndValidates(t *testing.T) {
	for _, value := range []int{0, 512, 4096, 32768} {
		got, err := judgeOutputLimit(value)
		if err != nil || got <= 0 {
			t.Fatalf("valid limit %d got=%d err=%v", value, got, err)
		}
	}
	for _, value := range []int{-1, 511, 32769} {
		if _, err := judgeOutputLimit(value); err == nil {
			t.Fatalf("invalid limit %d accepted", value)
		}
	}
}
