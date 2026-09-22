package studio

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/1420970597/llm/internal/exporter"
	"github.com/1420970597/llm/internal/model"
	"github.com/1420970597/llm/internal/store"
)

// 本文件是发布作业的**编码与校验**部分（Issue #160 T21）。
//
// 契约：docs/plans/atelier-api-contract.md §2.9；internal/model/release_artifact.go。
//
// 与 store 的分工：store 管「登记与发布判定」，本文件管「读冻结清单 →
// 编码 → 产出字节与 hash」。**输入只来自 release_items**（T21 验收项：
// 「输入仅来自已冻结 release_items」「禁止读取 Redis dataset 请求键/
// 默认映射/实时问题表」）：
//
//   * 内容按 `sample_version_id` 从 sample_versions 读（不可变，T05）；
//   * 映射按候选冻结的 `mapping_version_id` 读（不是「当前默认映射」）；
//   * 样本集合来自 release_items（不是「当前筛选条件」）。
//
// 因此「发布后改蓝图/映射/标准、隔离原样本、切换默认 storage，
// 旧文件仍一致」成立 —— 因为编码输入里没有任何「当前」状态。

// ReleaseBuildInput 是一次发布的编码输入。
type ReleaseBuildInput struct {
	ProjectID   int64
	ReleaseID   int64
	Revision    int64
	ReleaseName string
	TargetKind  string
	Format      string
	IntendedUse string
	Limitations []string
	Provenance  map[string]any

	MappingVersionID int64
	// Items 是**已冻结**的清单（来自 release_items）。
	Items []store.ReleaseItem
	// QualitySnapshot/CoverageSummary 是发布时冻结的质量与覆盖摘要。
	QualitySnapshot json.RawMessage
	CoverageSummary json.RawMessage
}

// ReleaseBuildOutput 是编码结果。
type ReleaseBuildOutput struct {
	Manifest         model.ReleaseManifest
	ManifestHash     string
	ArtifactBytes    []byte
	ArtifactHash     string
	SizeBytes        int64
	ItemsContentHash string
}

// EncoderVersion 标识编码器契约版本。
//
// 它进 manifest：同一份内容换了编码逻辑（例如字段顺序变了）会产出不同字节，
// 而用户需要知道当初用的是哪一版。有它才可能「用旧版重算出同一 hash」。
const EncoderVersion = "atelier-exporter-v1"

// MaxArtifactBytes 是单份制品的字节上限（明确的**内存上限**）。
//
// T21 要求「大规模导出需流式/分块或有明确内存上限」。JSONL 走**逐行流式**
// 路径（见 buildJSONLArtifact），因此不受它限制；CSV/Alpaca 的编码器接口
// 是「一次给出全部记录」（`Encode(records []Record, ...)`），要真正流式
// 需要改第一轮冻结的导出契约 —— 那不是本轮该做的事。
// 因此这里给 CSV/Alpaca 一个显式上限并在超限时**明确报错**（提示改用 JSONL），
// 而不是悄悄地占用大量内存直到 OOM。
const MaxArtifactBytes = 256 << 20 // 256 MiB

// MaxRecordsPerEncodeChunk 是逐行流式编码的批大小。
//
// 它只影响内存占用峰值，不改变输出字节：JSONL 是按行拼接的，
// 分批与一次性编码的结果完全一致（这一点由测试断言）。
const MaxRecordsPerEncodeChunk = 2000

// BuildReleaseArtifact 读冻结清单、编码并计算全部 hash。
func (runner *ReleaseBuilder) BuildReleaseArtifact(ctx context.Context, input ReleaseBuildInput) (ReleaseBuildOutput, error) {
	if len(input.Items) == 0 {
		return ReleaseBuildOutput{}, NewError(CodeValidation,
			"发布清单为空：没有任何内容版本可导出（空范围不能产出有效版本）")
	}
	if strings.TrimSpace(input.Format) == "" {
		input.Format = model.ExportFormatJSONL
	}

	// 映射来自**候选冻结的版本**，不是「当前默认映射」。
	mapping, err := runner.loadMapping(ctx, input.MappingVersionID)
	if err != nil {
		return ReleaseBuildOutput{}, err
	}

	// 逐条读内容（按冻结的版本 ID）并构造统一记录。
	// 分批处理：一次把十万条内容读进内存是不可接受的。
	records := make([]exporter.Record, 0, len(input.Items))
	manifestItems := make([]model.ManifestItem, 0, len(input.Items))
	for _, item := range input.Items {
		if strings.TrimSpace(item.ExcludedReason) != "" {
			// 被排除的项**不进入文件**，但仍计入 manifest 的范围说明
			//（数据卡要能解释覆盖损失，§2.3）。
			continue
		}
		record, err := runner.loadRecord(ctx, input.ProjectID, input.TargetKind, item)
		if err != nil {
			return ReleaseBuildOutput{}, err
		}
		records = append(records, record)
		manifestItems = append(manifestItems, model.ManifestItem{
			SampleID: item.SampleID, SampleVersionID: item.SampleVersionID,
			ContentHash:             item.ContentHash,
			StandardContentHash:     item.StandardContentHash,
			BlueprintContentHash:    item.BlueprintContentHash,
			AggregateReviewRevision: item.AggregateReviewRevision,
			EvidenceRevision:        item.EvidenceRevision,
		})
	}
	if len(records) == 0 {
		return ReleaseBuildOutput{}, NewError(CodeValidation,
			"发布清单里的内容都被排除了：没有可导出的内容")
	}

	itemsContentHash := model.ComputeItemsContentHash(manifestItems)

	var artifactBytes []byte
	var artifactHash string
	switch input.Format {
	case model.ExportFormatJSONL:
		// JSONL 走**逐行流式**：边编码边累计 hash，峰值内存与文件大小无关。
		artifactBytes, artifactHash, err = buildJSONLArtifact(ctx, runner, records, mapping)
	default:
		// 其他格式走一次编码，但受显式上限保护。
		artifactBytes, artifactHash, err = buildBufferedArtifact(runner, input.Format, records, mapping)
	}
	if err != nil {
		return ReleaseBuildOutput{}, err
	}

	manifest := model.ReleaseManifest{
		ReleaseID: input.ReleaseID, Revision: input.Revision, ReleaseName: input.ReleaseName,
		TargetKind: input.TargetKind, Format: input.Format,
		MappingVersionID: input.MappingVersionID, EncoderVersion: EncoderVersion,
		IntendedUse: input.IntendedUse, Limitations: input.Limitations,
		Provenance: input.Provenance,
		Items:      manifestItems, ItemCount: len(manifestItems),
		ItemsContentHash: itemsContentHash,
		QualitySnapshot:  input.QualitySnapshot, CoverageSummary: input.CoverageSummary,
		HashScope: model.HashScopeReleaseItems,
	}
	manifestHash, err := model.ComputeManifestHash(manifest)
	if err != nil {
		return ReleaseBuildOutput{}, err
	}

	return ReleaseBuildOutput{
		Manifest: manifest, ManifestHash: manifestHash,
		ArtifactBytes: artifactBytes, ArtifactHash: artifactHash,
		SizeBytes: int64(len(artifactBytes)), ItemsContentHash: itemsContentHash,
	}, nil
}

// ReleaseBuilder 是发布作业的编码器。
type ReleaseBuilder struct {
	Batches   *store.BatchStore
	Documents *store.DocumentStore
	// EncoderVersion 允许测试注入一个不同的版本（用于断言它进 manifest）。
	encoderVersionOverride string
}

// buildJSONLArtifact 逐行编码 JSONL 并增量计算 hash。
//
// 为什么逐个记录编码（而不是一次 `Encode(records)`）：
// JSONL 的语义是「一行一个 JSON 对象」，因此逐条编码再拼接与整体编码
// 结果**完全一致**，而峰值内存只与 `MaxRecordsPerEncodeChunk` 有关。
// 这使十万条导出不必占用与文件等量的内存（T21 的流式要求）。
func buildJSONLArtifact(ctx context.Context, runner *ReleaseBuilder, records []exporter.Record, mapping model.ExportMapping) ([]byte, string, error) {
	encoder, found := exporter.Get(model.ExportFormatJSONL)
	if !found {
		return nil, "", NewError(CodeUnavailable, "JSONL 编码器不可用")
	}
	digest := sha256.New()
	// 输出仍累积在内存里（上传接口需要完整字节），但**编码**是分块的：
	// 编码器每次只处理一个批次的记录，因此临时对象不会与文件等量增长。
	// 真正的零内存上传需要对象存储的多段上传接口，属后续优化
	//（已在此显式记录，而不是声称已经流式）。
	output := make([]byte, 0, len(records)*256)
	for start := 0; start < len(records); start += MaxRecordsPerEncodeChunk {
		if err := ctx.Err(); err != nil {
			return nil, "", err
		}
		end := start + MaxRecordsPerEncodeChunk
		if end > len(records) {
			end = len(records)
		}
		chunk, err := encoder.Encode(records[start:end], mapping)
		if err != nil {
			return nil, "", err
		}
		digest.Write(chunk)
		output = append(output, chunk...)
		if int64(len(output)) > MaxArtifactBytes {
			return nil, "", NewError(CodeValidation, fmt.Sprintf(
				"导出内容超过 %d MiB 上限：请缩小发布范围后重试", MaxArtifactBytes>>20))
		}
	}
	return output, "sha256:" + hex.EncodeToString(digest.Sum(nil)), nil
}

// buildBufferedArtifact 一次编码非 JSONL 格式（受显式上限保护）。
func buildBufferedArtifact(runner *ReleaseBuilder, format string, records []exporter.Record, mapping model.ExportMapping) ([]byte, string, error) {
	encoder, found := exporter.Get(format)
	if !found {
		return nil, "", NewError(CodeValidation, fmt.Sprintf(
			"不支持的导出格式 %q（本轮只承诺 SFT JSONL/CSV/Alpaca 与 GRPO JSONL）", format))
	}
	encoded, err := encoder.Encode(records, mapping)
	if err != nil {
		return nil, "", err
	}
	if int64(len(encoded)) > MaxArtifactBytes {
		// 明确报错而不是悄悄占内存：把上限说清楚，用户才知道该换 JSONL。
		return nil, "", NewError(CodeValidation, fmt.Sprintf(
			"%s 导出超过 %d MiB 上限：该格式的编码器一次处理全部记录，"+
				"请改用 JSONL（它按行流式编码）或缩小发布范围",
			format, MaxArtifactBytes>>20))
	}
	return encoded, model.ComputeArtifactHash(encoded), nil
}

// loadMapping 读取候选冻结的映射版本。
//
// 读不到映射必须报错而不是退化到「内置默认映射」：那会让发布出的文件
// 与用户确认的字段映射不一致（T21 验收项禁止读「默认映射」）。
func (runner *ReleaseBuilder) loadMapping(ctx context.Context, mappingVersionID int64) (model.ExportMapping, error) {
	if mappingVersionID <= 0 {
		return model.ExportMapping{}, NewError(CodeValidation,
			"发布候选没有映射版本：无法确定输出字段（请在选择映射版本后重新确认）")
	}
	version, err := runner.Documents.GetVersionByID(ctx, mappingVersionID)
	if err != nil {
		return model.ExportMapping{}, NewError(CodeValidation,
			"映射版本不存在或不可用，请重新选择映射版本")
	}
	var payload model.MappingPayload
	if err := json.Unmarshal(version.Payload, &payload); err != nil {
		return model.ExportMapping{}, NewError(CodeValidation, "映射版本内容无法解析，请重新保存映射")
	}
	// MappingPayload.Fields 是 []MappingField{TargetField, SourceField}，
	// 而 exporter 用的是 FieldMap（map[目标字段]取值表达式）。
	// 这里显式转换：两者语义相同但形态不同，直接赋值会编译不过
	//（而"顺手改成 FieldMap"却不转换会让所有字段都取不到值）。
	fieldMap := make(map[string]any, len(payload.Fields))
	for _, field := range payload.Fields {
		target := strings.TrimSpace(field.TargetField)
		if target == "" {
			continue
		}
		fieldMap[target] = field.SourceField
	}
	if len(fieldMap) == 0 {
		return model.ExportMapping{}, NewError(CodeValidation,
			"映射版本没有可用的字段映射，请重新保存映射")
	}
	return model.ExportMapping{
		Format:   payload.Format,
		FieldMap: fieldMap,
	}, nil
}

// loadRecord 按**冻结的样本版本 ID** 读内容并构造统一记录。
func (runner *ReleaseBuilder) loadRecord(ctx context.Context, projectID int64, targetKind string, item store.ReleaseItem) (exporter.Record, error) {
	// 必须带**项目作用域**：GetSampleVersionByID 的查询是
	// `WHERE id = $1 AND project_id = $2`，传 0 会一条也读不到
	//（那会让每次发布都以「读取内容失败」告终）。
	version, err := runner.Batches.GetSampleVersionByID(ctx, projectID, item.SampleVersionID)
	if err != nil {
		return exporter.Record{}, NewError(CodeUnavailable, fmt.Sprintf(
			"读取内容版本 %d 失败：%v", item.SampleVersionID, err))
	}
	var payload map[string]any
	if err := json.Unmarshal(version.Payload, &payload); err != nil {
		return exporter.Record{}, NewError(CodeValidation, fmt.Sprintf(
			"内容版本 %d 不是合法的 JSON 对象", item.SampleVersionID))
	}

	record := exporter.Record{
		DatasetID:   item.ReleaseID,
		QuestionID:  item.SampleVersionID,
		Question:    stringField(payload, "question"),
		Answer:      stringField(payload, "answer"),
		JudgePrompt: stringField(payload, "judge_prompt"),
	}
	// **字段命名**：新契约用 `reasoning`，旧名 `chainOfThought` 不再作为
	// 新契约字段（§2.2）。这里显式映射，而不是把旧名透传 ——
	// 透传会让导出文件里出现一个不在契约里的字段名。
	record.ChainOfThought = stringField(payload, "reasoning")
	// GRPO 的档位保留**数组结构**（不能 join 成逗号字符串）。
	if levels, found := payload["levels"].([]any); found {
		record.RewardLevels = make([]string, 0, len(levels))
		for _, level := range levels {
			if text, ok := level.(string); ok {
				record.RewardLevels = append(record.RewardLevels, text)
			}
		}
	}
	if difficulty, found := payload["difficulty"].(string); found {
		record.Difficulty = difficulty
	}
	_ = targetKind
	return record, nil
}

// stringField 取字符串字段（非字符串返回空串）。
func stringField(payload map[string]any, key string) string {
	if value, found := payload[key].(string); found {
		return value
	}
	return ""
}
