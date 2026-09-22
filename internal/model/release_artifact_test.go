package model

import (
	"strings"
	"testing"
)

// 本文件验证 Issue #160 T21 的制品与 manifest hash 规则。
//
// 这些规则全部**无法靠人工检查保证**：hash 输入里多一个字段、
// 少一次排序，都会让「同内容两份文件」hash 不同，而那种错误
// 只在「重试是否得到同一份文件」这种场景下暴露。

func manifestFixture() ReleaseManifest {
	return ReleaseManifest{
		ReleaseID: 7, Revision: 1, ReleaseName: "v1.0", TargetKind: TargetKindSFT,
		Format: ExportFormatJSONL, MappingVersionID: 3, EncoderVersion: "exporter-v1",
		IntendedUse: "SFT 训练",
		Limitations: []string{"仅覆盖冷链领域", "仅覆盖冷链领域", "  "},
		Items: []ManifestItem{
			{SampleID: 2, SampleVersionID: 20, ContentHash: "h2"},
			{SampleID: 1, SampleVersionID: 10, ContentHash: "h1"},
		},
	}
}

// TestCanonicalManifestIsOrderIndependent 覆盖「排序/编码确定」。
//
// 清单顺序取决于数据库返回顺序，而没有 ORDER BY 时 Postgres 可自由返回 ——
// 不排序会让同样内容的两份 manifest 产出不同 hash，
// 于是「重试是否得到同一份文件」无法验证。
func TestCanonicalManifestIsOrderIndependent(t *testing.T) {
	first := manifestFixture()
	second := manifestFixture()
	// 反转条目顺序（模拟数据库返回顺序不同）。
	second.Items[0], second.Items[1] = second.Items[1], second.Items[0]

	firstHash, err := ComputeManifestHash(first)
	if err != nil {
		t.Fatalf("ComputeManifestHash: %v", err)
	}
	secondHash, err := ComputeManifestHash(second)
	if err != nil {
		t.Fatalf("ComputeManifestHash: %v", err)
	}
	if firstHash != secondHash {
		t.Fatalf("条目顺序不得影响 manifest hash：%s vs %s", firstHash, secondHash)
	}

	// 规范化字节里条目必须按 (sampleId, sampleVersionId) 升序。
	raw, err := CanonicalManifestBytes(first)
	if err != nil {
		t.Fatalf("CanonicalManifestBytes: %v", err)
	}
	text := string(raw)
	if strings.Index(text, `"sampleId":1`) > strings.Index(text, `"sampleId":2`) {
		t.Fatalf("规范化后条目必须按 sampleId 升序，实际 %s", text)
	}
}

// TestCanonicalManifestNormalizesLimitationsAndCount 覆盖
// 「集合语义字段不参与顺序、计数从条目派生」。
func TestCanonicalManifestNormalizesLimitationsAndCount(t *testing.T) {
	manifest := manifestFixture()
	raw, err := CanonicalManifestBytes(manifest)
	if err != nil {
		t.Fatalf("CanonicalManifestBytes: %v", err)
	}
	// 重复与空白项被去掉：限制清单是集合语义，重复会让数据卡出现两条一样的限制。
	if strings.Count(string(raw), "仅覆盖冷链领域") != 1 {
		t.Fatalf("限制必须去重，实际 %s", raw)
	}
	// ItemCount 从 Items 派生：手填一个不一致的数字会让「内容相同但计数不同」
	// 的 manifest 产出不同 hash。
	manifest.ItemCount = 999
	rawAgain, err := CanonicalManifestBytes(manifest)
	if err != nil {
		t.Fatalf("CanonicalManifestBytes: %v", err)
	}
	if string(raw) != string(rawAgain) {
		t.Fatal("ItemCount 必须从 Items 派生（手填值不得进入 hash 输入）")
	}
}

// TestManifestHashDoesNotIncludeItself 覆盖「manifest_hash 不把自己包含进 hash 输入」。
//
// 自指会让验证变成循环：你无法在不知道 hash 的情况下计算 hash。
func TestManifestHashDoesNotIncludeItself(t *testing.T) {
	manifest := manifestFixture()
	hash, err := ComputeManifestHash(manifest)
	if err != nil {
		t.Fatalf("ComputeManifestHash: %v", err)
	}
	raw, err := CanonicalManifestBytes(manifest)
	if err != nil {
		t.Fatalf("CanonicalManifestBytes: %v", err)
	}
	if strings.Contains(string(raw), "manifest_hash") || strings.Contains(string(raw), "manifestHash") {
		t.Fatalf("hash 输入不得包含 manifest hash 字段（自指），实际 %s", raw)
	}
	// 稳定性：同输入两次得到同 hash。
	again, err := ComputeManifestHash(manifest)
	if err != nil {
		t.Fatalf("ComputeManifestHash: %v", err)
	}
	if hash != again {
		t.Fatalf("同输入必须得到同 hash：%s vs %s", hash, again)
	}
	if !strings.HasPrefix(hash, "sha256:") {
		t.Fatalf("hash 必须带算法前缀（使「换了算法」可见），实际 %s", hash)
	}
}

// TestComputeItemsContentHashIsOrderIndependentAndScoped 覆盖清单 hash 的范围。
//
// 它只覆盖**内容身份**：格式变了不该让它变化（格式由 artifact_hash 与
// manifest 表达），而内容或来源变了必须变化。
func TestComputeItemsContentHashIsOrderIndependentAndScoped(t *testing.T) {
	items := []ManifestItem{
		{SampleID: 1, SampleVersionID: 10, ContentHash: "h1", StandardContentHash: "s1"},
		{SampleID: 2, SampleVersionID: 20, ContentHash: "h2", StandardContentHash: "s2"},
	}
	base := ComputeItemsContentHash(items)

	reversed := []ManifestItem{items[1], items[0]}
	if ComputeItemsContentHash(reversed) != base {
		t.Fatal("清单顺序不得影响内容 hash")
	}

	// 内容变化必须体现。
	changed := []ManifestItem{items[0], items[1]}
	changed[1].ContentHash = "h2-new"
	if ComputeItemsContentHash(changed) == base {
		t.Fatal("内容 hash 变化必须体现在清单 hash 里")
	}
	// 来源 hash 变化同样必须体现（否则「换了标准」不可见）。
	changedSource := []ManifestItem{items[0], items[1]}
	changedSource[0].StandardContentHash = "s1-new"
	if ComputeItemsContentHash(changedSource) == base {
		t.Fatal("来源 hash 变化必须体现在清单 hash 里")
	}
}

// TestComputeArtifactHashIsChunkEquivariant 覆盖「流式/分块计算」。
//
// 大规模导出不可能把整个文件放进内存，因此 hash 必须按块累加，
// 而分块方式**不得**影响结果 —— 否则「同一文件两次上传」hash 会不同。
func TestComputeArtifactHashIsChunkEquivariant(t *testing.T) {
	whole := []byte(`{"question":"q"}` + "\n" + `{"question":"q2"}` + "\n")
	oneShot := ComputeArtifactHash(whole)
	chunked := ComputeArtifactHash([]byte(`{"question":"q"}`), []byte("\n"), []byte(`{"question":"q2"}`), []byte("\n"))
	if oneShot != chunked {
		t.Fatalf("分块方式不得影响 artifact hash：%s vs %s", oneShot, chunked)
	}
	if ComputeArtifactHash([]byte("a")) == ComputeArtifactHash([]byte("b")) {
		t.Fatal("不同字节必须有不同 hash")
	}
}

// TestCanPublishReleaseRequiresVerifiedArtifacts 覆盖验收项
// 「上传超时、存储不足、DB 失败均不显示成功」。
func TestCanPublishReleaseRequiresVerifiedArtifacts(t *testing.T) {
	verified := ArtifactFact{Format: "jsonl", State: ArtifactStateVerified, ItemsHashMatchesManifest: true}

	// 状态必须是 building / build_failed。
	if ok, _ := CanPublishRelease(ReleaseStatusCandidate, "sha256:x", []ArtifactFact{verified}); ok {
		t.Fatal("candidate 状态不得直接发布")
	}
	// 缺 manifest。
	if ok, reason := CanPublishRelease(ReleaseStatusBuilding, "", []ArtifactFact{verified}); ok {
		t.Fatalf("缺 manifest 不得发布，实际 reason=%q", reason)
	}
	// 没有任何已校验制品（上传超时的典型形态）。
	if ok, reason := CanPublishRelease(ReleaseStatusBuilding, "sha256:m", []ArtifactFact{
		{Format: "jsonl", State: ArtifactStateRegistered},
	}); ok {
		t.Fatalf("只有 registered 制品时不得发布，实际 reason=%q", reason)
	}
	if ok, reason := CanPublishRelease(ReleaseStatusBuilding, "sha256:m", nil); ok {
		t.Fatalf("没有制品时不得发布，实际 reason=%q", reason)
	}
	// 制品内容清单与 manifest 不一致：最危险的错配。
	if ok, reason := CanPublishRelease(ReleaseStatusBuilding, "sha256:m", []ArtifactFact{
		{Format: "jsonl", State: ArtifactStateVerified, ItemsContentHash: "sha256:other", ItemsHashMatchesManifest: false},
	}); ok {
		t.Fatalf("清单与文件不一致时不得发布，实际 reason=%q", reason)
	}
	// 正例。
	if ok, reason := CanPublishRelease(ReleaseStatusBuilding, "sha256:m", []ArtifactFact{verified}); !ok {
		t.Fatalf("全部满足时必须可发布，实际 reason=%q", reason)
	}
	// build_failed 可幂等续接（§2.9）。
	if ok, _ := CanPublishRelease(ReleaseStatusBuildFailed, "sha256:m", []ArtifactFact{verified}); !ok {
		t.Fatal("build_failed 必须能幂等续接")
	}
}

// TestValidateArtifactUpload 覆盖「对象必须真的写成功且字节一致」。
func TestValidateArtifactUpload(t *testing.T) {
	hash := ComputeArtifactHash([]byte("content"))
	if err := ValidateArtifactUpload(int64(len("content")), hash, int64(len("content")), hash, true); err != nil {
		t.Fatalf("一致的上传不应报错：%v", err)
	}
	// 对象不存在：上传超时往往**看起来**成功但对象其实没写进去。
	if err := ValidateArtifactUpload(7, hash, 0, "", false); err == nil {
		t.Fatal("对象不存在必须报错（上传超时不得显示成功）")
	}
	// 大小不符：传输被截断。
	if err := ValidateArtifactUpload(100, hash, 40, hash, true); err == nil {
		t.Fatal("大小不符必须报错")
	}
	// hash 不符：对象被并发覆盖。
	if err := ValidateArtifactUpload(7, hash, 7, ComputeArtifactHash([]byte("other")), true); err == nil {
		t.Fatal("hash 不符必须报错（不得静默接受被覆盖的对象）")
	}
}

// TestArtifactObjectKeyIsDeterministicAndNotLatest 覆盖路径规则。
func TestArtifactObjectKeyIsDeterministicAndNotLatest(t *testing.T) {
	hash := ComputeArtifactHash([]byte("content"))
	key := ArtifactObjectKey(12, 3, "jsonl", hash)
	if key != ArtifactObjectKey(12, 3, "jsonl", hash) {
		t.Fatal("同一输入必须得到同一 key（幂等重传写同一位置，不产生第二份）")
	}
	if strings.Contains(strings.ToLower(key), "latest") {
		t.Fatalf("路径不得含 latest（下载路径禁止 latest 回退）：%s", key)
	}
	if !strings.Contains(key, "releases/12/r3/") {
		t.Fatalf("路径必须含 releaseId 与 revision：%s", key)
	}
	// 内容不同 → key 不同（不会覆盖旧文件）。
	other := ArtifactObjectKey(12, 3, "jsonl", ComputeArtifactHash([]byte("other")))
	if key == other {
		t.Fatal("不同内容必须写到不同 key（覆盖等于破坏「已发布文件不可变」）")
	}
	// 格式不同 → key 不同。
	if key == ArtifactObjectKey(12, 3, "csv", hash) {
		t.Fatal("不同格式必须写到不同 key")
	}
}

// TestArtifactStateForAndCapabilities 覆盖状态与「发布后只读」。
func TestArtifactStateForAndCapabilities(t *testing.T) {
	if ArtifactStateFor(true, false) != ArtifactStateVerified {
		t.Fatal("校验通过应为 verified")
	}
	if ArtifactStateFor(false, true) != ArtifactStateFailed {
		t.Fatal("失败应为 failed")
	}
	if ArtifactStateFor(false, false) != ArtifactStateRegistered {
		t.Fatal("未校验应为 registered（不得当成可发布）")
	}
	// 只有「已发布 + 已校验」才可下载。
	if !ReleaseArtifactCapabilities(ProjectRoleViewer, ArtifactStateVerified, ReleaseStatusPublished).CanDownload {
		t.Fatal("已发布且已校验的制品应可下载")
	}
	if ReleaseArtifactCapabilities(ProjectRoleOwner, ArtifactStateRegistered, ReleaseStatusBuilding).CanDownload {
		t.Fatal("未发布/未校验的制品不得提供下载")
	}
}
