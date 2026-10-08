package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	importer "github.com/1420970597/llm/internal/import"
	"github.com/1420970597/llm/internal/model"
	"github.com/jackc/pgx/v5"
)

const SourceKindDocument = "source_document"
const SourceKindProduct = "source_product"

var ErrSourceKeyConflict = errors.New("来源键已用于不同的文件或切分策略，请使用新的来源键")

type SourceImportOptions struct {
	FileName         string               `json:"fileName,omitempty"`
	Kind             string               `json:"kind,omitempty"`
	Format           string               `json:"format,omitempty"`
	TargetKind       string               `json:"targetKind,omitempty"`
	ChangeReason     string               `json:"changeReason"`
	ExpectedRevision int64                `json:"expectedRevision"`
	Chunking         model.SourceChunking `json:"chunking"`
	ActorID          int64                `json:"actorId"`
	StableID         string               `json:"stableId,omitempty"`
}

type QueueSourceImportInput struct {
	ProjectID             int64
	SourceKind, SourceKey string
	Content               []byte
	Options               SourceImportOptions
}

func (s *LegacyImportStore) QueueSourceImport(ctx context.Context, input QueueSourceImportInput) (LegacyImport, bool, error) {
	if input.SourceKind != SourceKindDocument && input.SourceKind != SourceKindProduct {
		return LegacyImport{}, false, model.FieldErrors{{Field: "sourceKind", Message: "不支持目录或私有接口导入"}}
	}
	if input.Options.ActorID <= 0 || strings.TrimSpace(input.Options.ChangeReason) == "" {
		return LegacyImport{}, false, model.FieldErrors{{Field: "changeReason", Message: "变更理由必填"}}
	}
	if len(input.Content) > importer.MaxSourceUploadBytes {
		return LegacyImport{}, false, model.FieldErrors{{Field: "file", Message: "素材不能超过 200 MB"}}
	}
	if input.SourceKind == SourceKindDocument {
		if err := model.ValidateSourcePayload(model.SourcePayload{SchemaVersion: "source.v1", Chunking: input.Options.Chunking}); err != nil {
			return LegacyImport{}, false, err
		}
		if input.Options.Kind != "markdown" && input.Options.Kind != "txt" {
			return LegacyImport{}, false, model.FieldErrors{{Field: "file", Message: "仅支持 Markdown/TXT"}}
		}
	} else {
		if input.Options.TargetKind != model.TargetKindSFT {
			return LegacyImport{}, false, model.FieldErrors{{Field: "targetKind", Message: "公开问答格式只支持导入 SFT 项目；GRPO 需要独立判据"}}
		}
		if _, _, err := importer.MapProductRows(input.Options.Format, input.Content); err != nil {
			return LegacyImport{}, false, model.FieldErrors{{Field: "content", Message: err.Error()}}
		}
	}
	hash := importer.ContentDigest(input.Content)
	if strings.TrimSpace(input.SourceKey) == "" {
		strategy, _ := json.Marshal(input.Options.Chunking)
		input.SourceKey = "source-document:" + hash + ":" + importer.ContentDigest(strategy)[:16]
	}
	if len(input.SourceKey) > 200 {
		return LegacyImport{}, false, model.FieldErrors{{Field: "sourceKey", Message: "来源键最多 200 字节"}}
	}
	// Ledger uniqueness is global; make tenant/project scope explicit in its key.
	key := "project:" + strconv.FormatInt(input.ProjectID, 10) + ":" + input.SourceKey
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return LegacyImport{}, false, err
	}
	defer tx.Rollback(ctx)
	row, _, err := beginImport(ctx, tx, input.SourceKind, key)
	if err != nil {
		return row, false, err
	}
	row, err = scanLegacyImport(tx.QueryRow(ctx, `SELECT `+legacyImportColumns+` FROM legacy_imports WHERE id=$1 FOR UPDATE`, row.ID))
	if err != nil {
		return row, false, err
	}
	if row.ContentHash != "" {
		var original SourceImportOptions
		var raw []byte
		if err := tx.QueryRow(ctx, `SELECT source_options FROM legacy_imports WHERE id=$1`, row.ID).Scan(&raw); err != nil {
			return row, false, err
		}
		if err := json.Unmarshal(raw, &original); err != nil {
			return row, false, err
		}
		if row.ContentHash != hash || original.Kind != input.Options.Kind || original.Format != input.Options.Format || original.Chunking != input.Options.Chunking {
			return row, false, ErrSourceKeyConflict
		}
		return row, true, tx.Commit(ctx)
	}
	var targetKind string
	if err := tx.QueryRow(ctx, `SELECT target_kind FROM projects WHERE id=$1`, input.ProjectID).Scan(&targetKind); err != nil {
		return row, false, err
	}
	if input.SourceKind == SourceKindProduct && targetKind != input.Options.TargetKind {
		return row, false, model.FieldErrors{{Field: "targetKind", Message: "导入类型与项目不一致"}}
	}
	input.Options.StableID = "document-" + hash + "-" + importer.ContentDigest([]byte(input.SourceKey))[:16]
	if input.SourceKind == SourceKindDocument {
		payload, revision, err := currentSourcePayloadTx(ctx, tx, input.ProjectID)
		if err != nil {
			return row, false, err
		}
		if revision != input.Options.ExpectedRevision {
			return row, false, ErrRevisionConflict
		}
		payload.Chunking = input.Options.Chunking
		found := false
		for _, d := range payload.Documents {
			if d.StableID == input.Options.StableID {
				found = true
			}
		}
		if !found {
			payload.Documents = append(payload.Documents, model.SourceDocumentEntry{StableID: input.Options.StableID, FileName: input.Options.FileName, Kind: input.Options.Kind, ContentHash: hash})
		}
		if _, _, err := saveVersionTx(ctx, tx, input.ProjectID, model.KindSource, input.Options.ActorID, SaveDocumentVersionInput{ExpectedRevision: revision, Payload: payload, ChangeReason: input.Options.ChangeReason}); err != nil {
			return row, false, err
		}
	}
	before := map[string]any{"actorId": input.Options.ActorID, "contentHash": hash, "sourceKey": input.SourceKey}
	var existing int
	if err := tx.QueryRow(ctx, `SELECT COUNT(*) FROM sample_versions WHERE project_id=$1`, input.ProjectID).Scan(&existing); err != nil {
		return row, false, err
	}
	before["sampleVersions"] = existing
	beforeJSON, _ := json.Marshal(before)
	optionsJSON, _ := json.Marshal(input.Options)
	jobKind := model.JobKindSourceDocumentIngest
	if input.SourceKind == SourceKindProduct {
		jobKind = model.JobKindSourceProductIngest
	}
	job, _, err := EnqueueJobTx(ctx, tx, EnqueueJobInput{ProjectID: &input.ProjectID, Kind: jobKind, Payload: map[string]int64{"importId": row.ID}, IdempotencyKey: fmt.Sprintf("source-import:%d", row.ID), CreatedBy: &input.Options.ActorID})
	if err != nil {
		return row, false, err
	}
	_, err = tx.Exec(ctx, `UPDATE legacy_imports SET target_project_id=$2,status='pending',content_hash=$3,source_content=$4,source_options=$5,before_snapshot=$6,job_id=$7 WHERE id=$1`, row.ID, input.ProjectID, hash, input.Content, optionsJSON, beforeJSON, job.ID)
	if err != nil {
		return row, false, err
	}
	if err := writeStudioAuditTx(ctx, tx, StudioAudit{ActorID: input.Options.ActorID, Action: "project_source_import_queued", Resource: "project", ResourceID: strconv.FormatInt(input.ProjectID, 10), ProjectID: input.ProjectID, Reason: input.Options.ChangeReason, Detail: input.SourceKind + ": " + input.SourceKey}); err != nil {
		return row, false, err
	}
	row, err = scanLegacyImport(tx.QueryRow(ctx, `SELECT `+legacyImportColumns+` FROM legacy_imports WHERE id=$1`, row.ID))
	if err != nil {
		return row, false, err
	}
	return row, false, tx.Commit(ctx)
}

func currentSourcePayloadTx(ctx context.Context, tx pgx.Tx, projectID int64) (model.SourcePayload, int64, error) {
	payload := model.SourcePayload{SchemaVersion: "source.v1", Documents: []model.SourceDocumentEntry{}, Chunking: model.DefaultSourceChunking()}
	var raw []byte
	var revision int64
	err := tx.QueryRow(ctx, `SELECT v.payload,d.row_version FROM versioned_documents d JOIN document_versions v ON v.document_id=d.id AND v.version=d.current_version WHERE d.project_id=$1 AND d.kind='source' AND d.logical_id='main' FOR UPDATE OF d`, projectID).Scan(&raw, &revision)
	if errors.Is(err, pgx.ErrNoRows) {
		return payload, 0, nil
	}
	if err != nil {
		return payload, 0, err
	}
	err = json.Unmarshal(raw, &payload)
	return payload, revision, err
}

func (s *LegacyImportStore) GetSourceImport(ctx context.Context, projectID, id int64) (LegacyImport, error) {
	return scanLegacyImport(s.db.QueryRow(ctx, `SELECT `+legacyImportColumns+` FROM legacy_imports WHERE id=$1 AND target_project_id=$2 AND source_kind IN ('source_document','source_product')`, id, projectID))
}
func (s *LegacyImportStore) ListSourceImports(ctx context.Context, projectID int64, limit, offset int) ([]LegacyImport, int, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	if offset < 0 {
		offset = 0
	}
	var total int
	if err := s.db.QueryRow(ctx, `SELECT COUNT(*) FROM legacy_imports WHERE target_project_id=$1 AND source_kind IN ('source_document','source_product')`, projectID).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := s.db.Query(ctx, `SELECT `+legacyImportColumns+` FROM legacy_imports WHERE target_project_id=$1 AND source_kind IN ('source_document','source_product') ORDER BY id DESC LIMIT $2 OFFSET $3`, projectID, limit, offset)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	items := []LegacyImport{}
	for rows.Next() {
		row, err := scanLegacyImport(rows)
		if err != nil {
			return nil, 0, err
		}
		items = append(items, row)
	}
	return items, total, rows.Err()
}

// lockSourceImportTx fences every local side effect, not only CompleteJob.
func lockSourceImportTx(ctx context.Context, tx pgx.Tx, job model.Job, importID int64) (LegacyImport, error) {
	var valid bool
	if err := tx.QueryRow(ctx, `SELECT lease_owner=$2 AND fencing_token=$3 AND lease_until>NOW() AND status IN ('leased','running') FROM jobs WHERE id=$1 FOR UPDATE`, job.ID, job.LeaseOwner, job.FencingToken).Scan(&valid); err != nil {
		return LegacyImport{}, err
	}
	if !valid {
		return LegacyImport{}, ErrJobLeaseLost
	}
	row, err := scanLegacyImport(tx.QueryRow(ctx, `SELECT `+legacyImportColumns+` FROM legacy_imports WHERE id=$1 AND job_id=$2 FOR UPDATE`, importID, job.ID))
	if err != nil {
		return row, err
	}
	if job.ProjectID == nil || row.TargetProjectID == nil || *job.ProjectID != *row.TargetProjectID {
		return row, ErrJobLeaseLost
	}
	return row, nil
}

func (s *LegacyImportStore) ProcessSourceImport(ctx context.Context, job model.Job, importID int64) (LegacyImport, error) {
	if job.ProjectID == nil {
		return LegacyImport{}, fmt.Errorf("config_error: 缺少项目上下文")
	}
	var options SourceImportOptions
	var raw, content []byte
	if err := s.db.QueryRow(ctx, `SELECT source_options,source_content FROM legacy_imports WHERE id=$1 AND job_id=$2`, importID, job.ID).Scan(&raw, &content); err != nil {
		return LegacyImport{}, err
	}
	if err := json.Unmarshal(raw, &options); err != nil {
		return LegacyImport{}, fmt.Errorf("config_error: 素材参数无法读取")
	}
	row, err := s.GetSourceImport(ctx, *job.ProjectID, importID)
	if err != nil {
		return row, err
	}
	if row.Status == "completed" {
		return row, nil
	}
	chunks := []model.SourceChunk{}
	samples := []importer.MappedSample{}
	preview := importer.ProductPreview{Failures: []importer.ImportFailure{}}
	if row.SourceKind == SourceKindDocument {
		chunks, err = importer.ChunkDocument(content, options.Kind, options.Chunking)
	} else {
		samples, preview, err = importer.MapProductRows(options.Format, content)
	}
	if err != nil {
		if saveErr := s.failSourceImport(ctx, job, importID, err.Error()); saveErr != nil {
			return row, saveErr
		}
		return row, fmt.Errorf("config_error: %w", err)
	}
	if row.SourceKind == SourceKindDocument {
		for i, c := range chunks {
			if int64(i+1) <= row.Cursor {
				continue
			}
			tx, err := s.db.Begin(ctx)
			if err != nil {
				return row, err
			}
			current, err := lockSourceImportTx(ctx, tx, job, importID)
			if err != nil {
				tx.Rollback(ctx)
				return row, err
			}
			_, created, err := upsertSourceChunkTx(ctx, tx, *job.ProjectID, options.StableID, c)
			if err != nil {
				tx.Rollback(ctx)
				return row, err
			}
			if created {
				current.Counts.ImportedVersions++
			} else {
				current.Counts.SkippedExisting++
			}
			err = saveSourceProgressTx(ctx, tx, importID, int64(i+1), current.Counts)
			if err != nil {
				tx.Rollback(ctx)
				return row, err
			}
			if err := tx.Commit(ctx); err != nil {
				return row, err
			}
			row = current
			row.Cursor = int64(i + 1)
		}
	} else {
		for _, sample := range samples {
			if sample.SourceID <= row.Cursor {
				continue
			}
			tx, err := s.db.Begin(ctx)
			if err != nil {
				return row, err
			}
			current, err := lockSourceImportTx(ctx, tx, job, importID)
			if err != nil {
				tx.Rollback(ctx)
				return row, err
			}
			// Serialise content identities across concurrent files before dedupe/append.
			if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, fmt.Sprintf("%d:%s", *job.ProjectID, sample.SampleKey)); err != nil {
				tx.Rollback(ctx)
				return row, err
			}
			var exists bool
			err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM sample_versions sv JOIN samples s ON s.id=sv.sample_id WHERE s.project_id=$1 AND s.sample_key=$2 AND sv.content_hash=$3)`, *job.ProjectID, sample.SampleKey, sample.ContentHash).Scan(&exists)
			if err != nil {
				tx.Rollback(ctx)
				return row, err
			}
			if exists {
				current.Counts.SkippedExisting++
			} else {
				_, err = appendSampleVersionTx(ctx, tx, AppendSampleVersionInput{ProjectID: *job.ProjectID, SampleKey: sample.SampleKey, TargetKind: options.TargetKind, Title: sample.Payload["question"].(string), Payload: sample.Payload, CreatedBy: &options.ActorID})
				if err != nil {
					tx.Rollback(ctx)
					return row, err
				}
				current.Counts.ImportedVersions++
			}
			if err := saveSourceProgressTx(ctx, tx, importID, sample.SourceID, current.Counts); err != nil {
				tx.Rollback(ctx)
				return row, err
			}
			if err := tx.Commit(ctx); err != nil {
				return row, err
			}
			row = current
			row.Cursor = sample.SourceID
		}
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return row, err
	}
	defer tx.Rollback(ctx)
	row, err = lockSourceImportTx(ctx, tx, job, importID)
	if err != nil {
		return row, err
	}
	after := map[string]any{"actorId": options.ActorID, "contentHash": row.ContentHash}
	if row.SourceKind == SourceKindDocument {
		payload, revision, err := currentSourcePayloadTx(ctx, tx, *job.ProjectID)
		if err != nil {
			return row, err
		}
		ids := []int64{}
		seenIDs := map[int64]bool{}
		for _, c := range chunks {
			var id int64
			if err := tx.QueryRow(ctx, `SELECT id FROM source_chunks WHERE project_id=$1 AND content_hash=$2`, *job.ProjectID, c.ContentHash).Scan(&id); err != nil {
				return row, err
			}
			if !seenIDs[id] {
				ids = append(ids, id)
				seenIDs[id] = true
			}
		}
		for i := range payload.Documents {
			if payload.Documents[i].StableID == options.StableID {
				payload.Documents[i].ChunkCount = len(ids)
				payload.Documents[i].ChunkIDs = ids
				payload.Documents[i].ParsedAt = time.Now().UTC().Format(time.RFC3339)
			}
		}
		_, version, err := saveVersionTx(ctx, tx, *job.ProjectID, model.KindSource, options.ActorID, SaveDocumentVersionInput{ExpectedRevision: revision, Payload: payload, ChangeReason: "素材解析完成：" + options.FileName})
		if err != nil {
			return row, err
		}
		after["sourceVersionId"] = version.ID
		after["chunkIds"] = ids
		after["chunks"] = len(chunks)
		row.Counts.SourceItems = len(chunks)
		if len(chunks) == 0 {
			row.Counts.SourceItems = 1
			row.Counts.SkippedNoContent = 1
		}
	} else {
		row.Counts.SourceItems = preview.SourceItems
		row.Counts.FailedItems = preview.FailedItems
	}
	var total int
	if err := tx.QueryRow(ctx, `SELECT COUNT(*) FROM sample_versions WHERE project_id=$1`, *job.ProjectID).Scan(&total); err != nil {
		return row, err
	}
	after["sampleVersions"] = total
	afterJSON, _ := json.Marshal(after)
	failures, _ := json.Marshal(preview.Failures)
	if err := saveSourceProgressTx(ctx, tx, importID, row.Cursor, row.Counts); err != nil {
		return row, err
	}
	_, err = tx.Exec(ctx, `UPDATE legacy_imports SET status='completed',after_snapshot=$2,failures=$3,finished_at=NOW(),source_content=NULL,error_message='' WHERE id=$1`, importID, afterJSON, failures)
	if err != nil {
		return row, err
	}
	if err := tx.Commit(ctx); err != nil {
		return row, err
	}
	return s.GetSourceImport(ctx, *job.ProjectID, importID)
}

func saveSourceProgressTx(ctx context.Context, tx pgx.Tx, id, cursor int64, c LegacyImportCounts) error {
	_, err := tx.Exec(ctx, `UPDATE legacy_imports SET status='running',cursor=$2,source_items=$3,imported_versions=$4,skipped_existing=$5,skipped_no_content=$6,failed_items=$7,updated_at=NOW() WHERE id=$1`, id, cursor, c.SourceItems, c.ImportedVersions, c.SkippedExisting, c.SkippedNoContent, c.FailedItems)
	return err
}
func (s *LegacyImportStore) failSourceImport(ctx context.Context, job model.Job, id int64, message string) error {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err := lockSourceImportTx(ctx, tx, job, id); err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `UPDATE legacy_imports SET status='failed',failed_items=1,error_message=$2,failures=$3,updated_at=NOW() WHERE id=$1`, id, message, json.RawMessage(`[ {"sourceId":1,"reason":`+strconv.Quote(message)+`} ]`))
	if err != nil {
		return err
	}
	return tx.Commit(ctx)
}
