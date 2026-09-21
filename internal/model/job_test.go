package model

import (
	"strings"
	"testing"
)

// 本文件验证 Issue #160 T06 中**纯逻辑**的作业契约部分。
//
// 涉及数据库的部分（租约、fencing、outbox）在 internal/store/job_store_test.go
// 用真实 Postgres 验证；这里只锁定「不连库也能测、但错了会很贵」的判定：
// 幂等键组成、消息路由判据、退避曲线、重试分类。

// TestJobIdempotencyKeyBindsAllFourSegments 覆盖 T06 验收项
// 「幂等键绑定 actor/project/command 和请求摘要」。
//
// 每一段都必须真正参与组成：任何一段被漏掉，都会产生一个可复现的串键场景
// （两个用户互相拿到对方的批次、同键不同请求被当成重放）。因此这里
// 逐个改动单段并断言键**必定**变化。
func TestJobIdempotencyKeyBindsAllFourSegments(t *testing.T) {
	base, err := JobIdempotencyKey(7, 11, "batch.create.pilot", "sha256:aaa")
	if err != nil {
		t.Fatalf("JobIdempotencyKey: %v", err)
	}

	mutations := map[string]string{
		"actor":   mustKey(t, 8, 11, "batch.create.pilot", "sha256:aaa"),
		"project": mustKey(t, 7, 12, "batch.create.pilot", "sha256:aaa"),
		"command": mustKey(t, 7, 11, "batch.create.scale", "sha256:aaa"),
		"digest":  mustKey(t, 7, 11, "batch.create.pilot", "sha256:bbb"),
	}
	for segment, mutated := range mutations {
		if mutated == base {
			t.Fatalf("改动 %s 后幂等键必须变化（否则该段没有参与组成）：%s", segment, base)
		}
	}

	// 稳定性：同输入必须得到同键，否则「同键同请求回放原结果」不可能成立。
	again := mustKey(t, 7, 11, "batch.create.pilot", "sha256:aaa")
	if again != base {
		t.Fatalf("同输入必须得到同键：%s vs %s", base, again)
	}
}

// TestJobIdempotencyKeyRejectsIncompleteInputs 覆盖「宁可报错也不静默不去重」。
//
// 静默降级最危险的形态是：幂等键为空 → 作业表的部分唯一索引不生效
// → 用户双击直接产生两个批次，而每个批次都会真实调用模型花钱。
// 因此参数不完整必须是错误。
func TestJobIdempotencyKeyRejectsIncompleteInputs(t *testing.T) {
	cases := []struct {
		name            string
		actor, project  int64
		command, digest string
	}{
		{"缺 actor", 0, 11, "cmd", "digest"},
		{"缺 project", 7, 0, "cmd", "digest"},
		{"命令名为空", 7, 11, "  ", "digest"},
		{"缺请求摘要", 7, 11, "cmd", "\t"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if key, err := JobIdempotencyKey(testCase.actor, testCase.project, testCase.command, testCase.digest); err == nil {
				t.Fatalf("必须报错，实际得到键 %q", key)
			}
		})
	}
}

func mustKey(t *testing.T, actorID, projectID int64, command, digest string) string {
	t.Helper()
	key, err := JobIdempotencyKey(actorID, projectID, command, digest)
	if err != nil {
		t.Fatalf("JobIdempotencyKey: %v", err)
	}
	return key
}

// TestProbeJobEnvelopeJobIDIsLenientAboutVersion 锁定路由判据的宽松性。
//
// 路由必须比解析宽松，否则「未来版本的作业消息」会在旧消费者里被当垃圾丢弃。
func TestProbeJobEnvelopeJobIDIsLenientAboutVersion(t *testing.T) {
	cases := []struct {
		raw    string
		wantID int64
		wantOK bool
	}{
		{`{"schemaVersion":1,"jobId":3}`, 3, true},
		{`{"schemaVersion":99,"jobId":4}`, 4, true},
		{`{"jobId":5}`, 5, true},
		{`{"jobId":-1}`, -1, true},
		{`{"type":"sft.generate","datasetId":2}`, 0, true},
		{`not json`, 0, false},
	}
	for _, testCase := range cases {
		id, ok := ProbeJobEnvelopeJobID([]byte(testCase.raw))
		if ok != testCase.wantOK || id != testCase.wantID {
			t.Fatalf("ProbeJobEnvelopeJobID(%s) = (%d,%v)，期望 (%d,%v)",
				testCase.raw, id, ok, testCase.wantID, testCase.wantOK)
		}
	}
}

// TestJobBackoffIsBoundedAndMonotonic 覆盖退避的两个硬要求。
//
//   - 单调不减：第 3 次重试不能比第 2 次更早（限流场景下更密集的重试只会再被限流）；
//   - 有上界：否则「第 5 次重试」会等到几小时后，而用户以为点了恢复没反应。
//
// 同时锁定 attempt<=0 的容错（调用方可能传 0），不能返回 0 或负数
// —— 0 退避等价于忙等。
func TestJobBackoffIsBoundedAndMonotonic(t *testing.T) {
	previous := JobBackoff(1)
	if previous <= 0 {
		t.Fatalf("第一次退避必须为正：%v", previous)
	}
	for attempt := 2; attempt <= 12; attempt++ {
		current := JobBackoff(attempt)
		if current < previous {
			t.Fatalf("退避必须单调不减：attempt=%d 得到 %v，上一次 %v", attempt, current, previous)
		}
		if current > JobBackoffMax {
			t.Fatalf("退避必须有上界 %v，attempt=%d 得到 %v", JobBackoffMax, attempt, current)
		}
		previous = current
	}
	if got := JobBackoff(50); got != JobBackoffMax {
		t.Fatalf("足够多次失败后退避必须是上界，实际 %v", got)
	}
	if got := JobBackoff(0); got <= 0 {
		t.Fatalf("attempt=0 必须被容错为正退避，实际 %v", got)
	}
	if got := JobBackoff(-3); got <= 0 {
		t.Fatalf("attempt<0 必须被容错为正退避，实际 %v", got)
	}
}

// TestIsRetryableJobErrorIsConservative 锁定「自动再花钱跑一次」的判据。
//
// 分类错误的代价不对称：把配置错误当可重试 → 无限重试并持续产生费用；
// 把限流当不可重试 → 用户多点一次恢复。因此只对有明确外部原因的类别返回 true。
func TestIsRetryableJobErrorIsConservative(t *testing.T) {
	retryable := []string{ErrorClassRateLimited, ErrorClassTimeout, ErrorClassProvider, ErrorClassInternal}
	for _, class := range retryable {
		if !IsRetryableJobError(class) {
			t.Fatalf("%s 应当可重试", class)
		}
	}
	notRetryable := []string{
		ErrorClassSchema, ErrorClassInvalidJSON, ErrorClassEmptyOutput,
		ErrorClassTruncated, ErrorClassConfig, "", "unknown_class",
	}
	for _, class := range notRetryable {
		if IsRetryableJobError(class) {
			t.Fatalf("%s 不应自动重试（同样的输入会得到同样的结果，或需要人工修配置）", class)
		}
	}
}

// TestStudioJobKindNamespace 锁定新旧作业类型的命名空间分隔。
//
// 「旧消费者不得误吞新消息」在类型层面也成立：旧类型带点但不带 studio. 前缀。
// 若新类型不加前缀，dispatcher 的「有没有处理器」判断会与旧注册表混淆。
func TestStudioJobKindNamespace(t *testing.T) {
	if !IsStudioJobKind(JobKindBatchGenerate) || !IsStudioJobKind(JobKindReleaseBuild) {
		t.Fatal("studio.* 必须被识别为 Studio 作业")
	}
	for _, legacy := range []string{"sft.generate", "export.generate", "eval.run", "studio.", ""} {
		if IsStudioJobKind(legacy) {
			t.Fatalf("%q 不得被识别为 Studio 作业", legacy)
		}
	}
	if !strings.HasPrefix(JobKindBatchGenerate, "studio.") {
		t.Fatal("Studio 作业类型必须以 studio. 前缀开头")
	}
}

// TestIsLegacyJobPayloadDistinguishesFormats 锁定「旧 vs 新」的判定：
// 有 jobId 一律算新格式，两者皆无才算无法识别。
func TestIsLegacyJobPayloadDistinguishesFormats(t *testing.T) {
	if !IsLegacyJobPayload([]byte(`{"type":"sft.generate","datasetId":1}`)) {
		t.Fatal("带 type/datasetId 的消息必须判为旧格式")
	}
	if IsLegacyJobPayload([]byte(`{"schemaVersion":1,"jobId":2,"kind":"studio.batch.generate"}`)) {
		t.Fatal("带 jobId 的消息不得判为旧格式")
	}
	if IsLegacyJobPayload([]byte(`{"jobId":3,"type":"sft.generate","datasetId":1}`)) {
		t.Fatal("同时带 jobId 与 type 的消息按新格式处理（jobId 优先）")
	}
	if IsLegacyJobPayload([]byte(`not json`)) {
		t.Fatal("无法解析的内容不得判为旧格式")
	}
}
