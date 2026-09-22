package model

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// 本文件定义不可变制品与 manifest 的 hash 规则（Issue #160 T21）。
//
// 契约：docs/plans/atelier-implementation.md §2.5/§4.1；
// docs/plans/atelier-api-contract.md §2.9/§2.10。
//
// 本文件的核心主张：**hash 分层且互不包含**。
//
//	content hash（每条内容）→ items_content_hash（清单）→ artifact_hash（文件字节）
//	  → manifest_hash（元数据，且输入不含它自己）
//
// 为什么必须分层：把它们合成一个 hash 会让「文件损坏」与「元数据被改」
// 无法区分，而两者的处置完全不同（前者要受控重建，后者要审计）。
//
// 为什么 manifest_hash 不能包含自己：自指会让验证变成循环 ——
// 你无法在不知道 hash 的情况下计算 hash。

// 制品状态。
const (
	ArtifactStateRegistered = "registered"
	ArtifactStateVerified   = "verified"
	ArtifactStateFailed     = "failed"
)

// HashScopeReleaseItems 是 manifest hash 的默认覆盖范围。
//
// 显式记录范围（而不是文档里说一句）：两个 hash 可比的前提是范围相同，
// 而范围会随版本演进（例如将来把「裁判配置」也纳入）。没有 scope 字段时，
// 「hash 不一样」无法判断是内容变了还是范围变了。
const HashScopeReleaseItems = "release_items+config"

// ManifestItem 是 manifest 里的一条内容引用。
//
// 只有**引用**（ID + hash + 来源 hash），不含样本正文：
// manifest 会进下载与审计，把正文塞进去会让它变成一份数据副本。
type ManifestItem struct {
	SampleID        int64  `json:"sampleId"`
	SampleVersionID int64  `json:"sampleVersionId"`
	ContentHash     string `json:"contentHash"`
	// 来源 hash：样本版本自带的引用（**不是**当前项目采用版本）。
	StandardContentHash     string `json:"standardContentHash,omitempty"`
	BlueprintContentHash    string `json:"blueprintContentHash,omitempty"`
	AggregateReviewRevision int64  `json:"aggregateReviewRevision,omitempty"`
	EvidenceRevision        int64  `json:"evidenceRevision,omitempty"`
}

// ReleaseManifest 是发布清单（元数据）。
type ReleaseManifest struct {
	ReleaseID   int64  `json:"releaseId"`
	Revision    int64  `json:"revision"`
	ReleaseName string `json:"releaseName"`
	TargetKind  string `json:"targetKind"`

	// 输入规格：格式、映射版本与编码器版本。
	//
	// 三者都不可省：「同样的内容导出成不同格式」与「同一格式换了映射」
	// 都会产出不同字节，而用户需要知道当初用的是什么。
	Format           string `json:"format"`
	MappingVersionID int64  `json:"mappingVersionId"`
	EncoderVersion   string `json:"encoderVersion"`

	// 用途/限制/来源：数据卡的核心字段。
	IntendedUse string         `json:"intendedUse"`
	Limitations []string       `json:"limitations"`
	Provenance  map[string]any `json:"provenance"`

	// 内容清单与数量。
	Items     []ManifestItem `json:"items"`
	ItemCount int            `json:"itemCount"`
	// ItemsContentHash 是清单内容的 hash（与 artifact_hash 分开）。
	ItemsContentHash string `json:"itemsContentHash"`
	// 质量与覆盖摘要（原范围指标与覆盖损失）。
	QualitySnapshot json.RawMessage `json:"qualitySnapshot,omitempty"`
	CoverageSummary json.RawMessage `json:"coverageSummary,omitempty"`

	HashScope string `json:"hashScope"`
}

// CanonicalManifestBytes 生成 manifest 的**规范化**序列化。
//
// 规范化是 hash 可比的前提，因此这里做三件事：
//
//  1. **Items 按 (sampleId, sampleVersionId) 排序**：清单顺序取决于数据库
//     返回顺序，而那是**未定义**的（没有 ORDER BY 时 Postgres 可自由返回）。
//     不排序会让「同样内容的两份 manifest」hash 不同，于是「重试是否
//     得到同一份文件」无法验证 —— 这是一次真实缺陷的常见形态。
//  2. **Limitations 去重并排序**：限制清单是集合语义，顺序不该影响 hash；
//     重复项会让数据卡出现两条一样的限制。
//  3. **JSON 序列化不带缩进**：缩进是展示层的事，不该进 hash 输入。
//
// **manifest_hash 不包含在这里**：输入里没有 hash 字段，因此不可能自指。
func CanonicalManifestBytes(manifest ReleaseManifest) ([]byte, error) {
	normalized := manifest
	normalized.Items = append([]ManifestItem(nil), manifest.Items...)
	sort.SliceStable(normalized.Items, func(i, j int) bool {
		if normalized.Items[i].SampleID != normalized.Items[j].SampleID {
			return normalized.Items[i].SampleID < normalized.Items[j].SampleID
		}
		return normalized.Items[i].SampleVersionID < normalized.Items[j].SampleVersionID
	})
	normalized.Limitations = normalizedLimitations(manifest.Limitations)
	if normalized.Provenance == nil {
		normalized.Provenance = map[string]any{}
	}
	if normalized.HashScope == "" {
		normalized.HashScope = HashScopeReleaseItems
	}
	// ItemCount 从 Items 派生，避免「数量与实际条数不一致」被写进 hash
	//（那会让两份内容相同但计数字段不同的 manifest 产出不同 hash）。
	normalized.ItemCount = len(normalized.Items)
	return json.Marshal(normalized)
}

// normalizedLimitations 去重 + 排序 + 去空白。
func normalizedLimitations(limitations []string) []string {
	seen := map[string]bool{}
	result := make([]string, 0, len(limitations))
	for _, limitation := range limitations {
		trimmed := strings.TrimSpace(limitation)
		if trimmed == "" || seen[trimmed] {
			continue
		}
		seen[trimmed] = true
		result = append(result, trimmed)
	}
	sort.Strings(result)
	return result
}

// ComputeItemsContentHash 计算清单内容的 hash。
//
// 只覆盖**内容身份**（每条的内容 hash 与来源 hash），不含数量与格式：
// 它回答的是「这份文件里的内容与清单是否一致」，
// 而「格式变了」不该让它变化（格式由 artifact_hash 与 manifest 表达）。
func ComputeItemsContentHash(items []ManifestItem) string {
	identities := make([]string, 0, len(items))
	for _, item := range items {
		identities = append(identities, fmt.Sprintf("%d:%d:%s:%s:%s",
			item.SampleID, item.SampleVersionID, item.ContentHash,
			item.StandardContentHash, item.BlueprintContentHash))
	}
	// 排序使「同样的内容集合」无论传入顺序都得到同一 hash。
	sort.Strings(identities)
	digest := sha256.New()
	for _, identity := range identities {
		digest.Write([]byte(identity))
		digest.Write([]byte{'\n'})
	}
	return "sha256:" + hex.EncodeToString(digest.Sum(nil))
}

// ComputeManifestHash 计算 manifest hash。
func ComputeManifestHash(manifest ReleaseManifest) (string, error) {
	raw, err := CanonicalManifestBytes(manifest)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(raw)
	return "sha256:" + hex.EncodeToString(digest[:]), nil
}

// ComputeArtifactHash 计算制品（文件字节）的 hash。
//
// 逐块计算（调用方按块喂入）：大规模导出不可能把整个文件放进内存，
// 而「先全量读入再 hash」正是那样一种实现（T21 验收项要求流式/分块
// 或有明确内存上限）。
func ComputeArtifactHash(chunks ...[]byte) string {
	digest := sha256.New()
	for _, chunk := range chunks {
		digest.Write(chunk)
	}
	return "sha256:" + hex.EncodeToString(digest.Sum(nil))
}

// ArtifactStateFor 判断制品是否可用于发布。
//
// 只有 `verified` 才算确认：`registered` 表示「DB 有记录但对象尚未校验」，
// 把它当成可发布会让「上传超时但其实没写成功」被当成成功
// （T21 验收项：上传超时不得显示成功）。
func ArtifactStateFor(verified bool, failed bool) string {
	switch {
	case failed:
		return ArtifactStateFailed
	case verified:
		return ArtifactStateVerified
	default:
		return ArtifactStateRegistered
	}
}

// CanPublishRelease 判断一次发布是否具备全部前置条件。
//
// 返回阻塞原因（空表示可以发布）。四条判据：
//
//  1. 发布状态必须是 building（candidate/blocked 不能直接发布）；
//  2. 至少有一个 `verified` 制品（没有任何确认对象的「发布」是假发布）；
//  3. manifest 已写入（没有 manifest 无法回答「发布了什么」）；
//  4. 每个 verified 制品的 items_content_hash 与 manifest 一致
//     （防止「清单是 A、文件是 B」这种最危险的错配）。
func CanPublishRelease(status string, manifestHash string, artifacts []ArtifactFact) (bool, string) {
	if status != ReleaseStatusBuilding && status != ReleaseStatusBuildFailed {
		return false, fmt.Sprintf("发布状态为 %s，只有 building 或 build_failed 才能进入 published", status)
	}
	if strings.TrimSpace(manifestHash) == "" {
		return false, "缺少 manifest：无法回答「这一版发布了什么」，不能标记为已发布"
	}
	verified := 0
	for _, artifact := range artifacts {
		if artifact.State != ArtifactStateVerified {
			continue
		}
		verified++
		if artifact.ItemsContentHash != "" && !artifact.ItemsHashMatchesManifest {
			return false, fmt.Sprintf(
				"制品 %s 的内容清单与 manifest 不一致：不能把「清单是 A、文件是 B」的版本标为已发布",
				artifact.Format)
		}
	}
	if verified == 0 {
		return false, "没有任何已校验的制品：只有对象存在且 size/hash 校验通过后才能发布"
	}
	return true, ""
}

// ArtifactFact 是发布判定所需的制品事实。
type ArtifactFact struct {
	Format                   string
	State                    string
	ItemsContentHash         string
	ItemsHashMatchesManifest bool
}

// ValidateArtifactUpload 校验一次上传的要确认的字节事实。
//
// 三个校验都不可省（T21 验收项「上传超时、存储不足、DB 失败均不显示成功」）：
//   - size 与本地实际写入字节不符 → 传输被截断；
//   - 重新读回对象的 hash 与本地 hash 不符 → 对象被并发覆盖或存储有问题；
//   - 对象不存在 → 上传其实失败了（超时的常见真相）。
func ValidateArtifactUpload(expectedSize int64, expectedHash string, actualSize int64, actualHash string, exists bool) error {
	if !exists {
		return fmt.Errorf("对象不存在：上传可能因超时而未真正写入，不能标记为已发布")
	}
	if actualSize != expectedSize {
		return fmt.Errorf("对象大小不符（期望 %d 字节、实际 %d 字节）：传输被截断",
			expectedSize, actualSize)
	}
	if !strings.EqualFold(expectedHash, actualHash) {
		return fmt.Errorf("对象内容 hash 不符（期望 %s、实际 %s）：对象可能被并发覆盖",
			expectedHash, actualHash)
	}
	return nil
}

// ArtifactObjectKey 生成制品的对象路径。
//
// 路径里含 **releaseId + revision + 内容 hash**，因此：
//   - 同一版本的同一内容重复上传会写到**同一个 key**（幂等，不产生第二份）；
//   - 内容不同（换了映射/格式）自然写到不同 key，不会覆盖旧文件；
//   - 「已发布文件不可变」由「路径含 hash」保证 —— 覆盖等于写入另一个内容，
//     而那会落在另一个 key 上。
//
// 不含 `latest`：下载路径禁止 latest 回退（§2.5），路径也不给它留位置。
func ArtifactObjectKey(releaseID, revision int64, format, artifactHash string) string {
	// 去掉 `sha256:` 前缀并取前 16 位：完整 hash 进路径会让 key 过长，
	// 而 16 个十六进制字符（64 位）在本场景下足以避免碰撞（同一 release
	// 的制品数量是「格式数」量级，而不是百万级）。
	short := strings.TrimPrefix(artifactHash, "sha256:")
	if len(short) > 16 {
		short = short[:16]
	}
	return fmt.Sprintf("releases/%d/r%d/export-%s-%s.jsonl", releaseID, revision, format, short)
}

// ReleaseArtifactCapabilities 派生制品能力位（契约 §4）。
//
// published 之后只有下载：已发布文件不可变，因此没有「重新上传」能力。
func ReleaseArtifactCapabilities(role, artifactState, releaseStatus string) Capabilities {
	canDownload := role == ProjectRoleOwner || role == ProjectRoleReviewer || role == ProjectRoleViewer
	if releaseStatus != ReleaseStatusPublished || artifactState != ArtifactStateVerified {
		return Capabilities{}
	}
	return Capabilities{CanDownload: canDownload}
}
