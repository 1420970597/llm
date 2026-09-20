package main

import (
	"testing"
)

// TestArtifactObjectKeyBucketIsParsed 锁定「下载按工件自带 bucket」的解析规则。
//
// 背景（issue #136）：工件表把位置记在 object_key 里，形如 `s3://<bucket>/<path>`
// —— bucket 是工件自身携带的信息。而下载此前用 `ResolveStorageProfile` 解析出的
// 「当前默认存储配置」的 bucket 去读，于是管理员切换过默认存储之后，
// 此前用旧 bucket 导出的工件就永久 500：
//
//	当前默认 bucket: llm-factory-local
//	artifact#16 objectKey=s3://llm-factory-dev/...   -> 500 The specified key does not exist
//	（而 llm-minio-1 里 /data/llm-factory-dev/... 对象确实存在）
//
// 父代理用**决定性用例**验证过修复：工件 20/21 在 llm-factory-local，
// 而默认 bucket 是 llm-factory-dev（该 bucket 下根本没有这些对象），
// 修复后两者都能 200 下载。
//
// 本测试只覆盖解析规则（纯函数），避免依赖真实 MinIO。
func TestArtifactObjectKeyBucketIsParsed(t *testing.T) {
	cases := []struct {
		objectKey  string
		wantBucket string
		wantKey    string
	}{
		{
			"s3://llm-factory-dev/datasets/50/exports/dataset-alpaca.jsonl",
			"llm-factory-dev",
			"datasets/50/exports/dataset-alpaca.jsonl",
		},
		{
			"s3://llm-factory-local/datasets/149/exports/dataset-sharegpt.jsonl",
			"llm-factory-local",
			"datasets/149/exports/dataset-sharegpt.jsonl",
		},
		{
			// 不含 bucket 的历史形态：bucket 应为空（调用方回退到 profile 的 bucket）。
			"datasets/5/exports/dataset.jsonl",
			"",
			"datasets/5/exports/dataset.jsonl",
		},
	}

	for _, tc := range cases {
		bucket, key := splitArtifactObjectKey(tc.objectKey)
		if bucket != tc.wantBucket {
			t.Errorf("objectKey=%q 的 bucket 应为 %q，实际 %q（用错 bucket 会导致历史工件永久 500）",
				tc.objectKey, tc.wantBucket, bucket)
		}
		if key != tc.wantKey {
			t.Errorf("objectKey=%q 的 key 应为 %q，实际 %q", tc.objectKey, tc.wantKey, key)
		}
	}
}
