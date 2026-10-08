package store

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/1420970597/llm/internal/model"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type SourceChunkStore struct{ db *pgxpool.Pool }

func NewSourceChunkStore(db *pgxpool.Pool) *SourceChunkStore { return &SourceChunkStore{db: db} }

const sourceChunkColumns = `id,project_id,source_document_stable_id,heading_path,ordinal,content,content_hash,created_at`

func sourceChunkIDsJSON(ids []int64) json.RawMessage {
	if ids == nil {
		ids = []int64{}
	}
	raw, _ := json.Marshal(ids)
	return raw
}

func scanSourceChunk(row pgx.Row) (model.SourceChunk, error) {
	var c model.SourceChunk
	err := row.Scan(&c.ID, &c.ProjectID, &c.SourceDocumentStableID, &c.HeadingPath, &c.Ordinal, &c.Content, &c.ContentHash, &c.CreatedAt)
	return c, err
}

func upsertSourceChunkTx(ctx context.Context, tx pgx.Tx, projectID int64, stableID string, c model.SourceChunk) (model.SourceChunk, bool, error) {
	row, err := scanSourceChunk(tx.QueryRow(ctx, `INSERT INTO source_chunks(project_id,source_document_stable_id,heading_path,ordinal,content,content_hash) VALUES($1,$2,$3,$4,$5,$6) ON CONFLICT(project_id,content_hash) DO NOTHING RETURNING `+sourceChunkColumns, projectID, stableID, c.HeadingPath, c.Ordinal, c.Content, c.ContentHash))
	created := err != pgx.ErrNoRows
	if err == pgx.ErrNoRows {
		row, err = scanSourceChunk(tx.QueryRow(ctx, `SELECT `+sourceChunkColumns+` FROM source_chunks WHERE project_id=$1 AND content_hash=$2`, projectID, c.ContentHash))
	}
	if err != nil {
		return row, false, err
	}
	_, err = tx.Exec(ctx, `INSERT INTO source_document_chunks(project_id,source_document_stable_id,chunk_id,ordinal) VALUES($1,$2,$3,$4) ON CONFLICT DO NOTHING`, projectID, stableID, row.ID, c.Ordinal)
	return row, created, err
}

func (s *SourceChunkStore) UpsertSourceChunks(ctx context.Context, projectID int64, stableID string, chunks []model.SourceChunk) ([]model.SourceChunk, int, error) {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return nil, 0, err
	}
	defer tx.Rollback(ctx)
	rows := []model.SourceChunk{}
	added := 0
	for _, c := range chunks {
		row, created, err := upsertSourceChunkTx(ctx, tx, projectID, stableID, c)
		if err != nil {
			return nil, 0, err
		}
		if created {
			added++
		}
		rows = append(rows, row)
	}
	return rows, added, tx.Commit(ctx)
}

func (s *SourceChunkStore) GetSourceChunk(ctx context.Context, projectID, id int64) (model.SourceChunk, error) {
	return scanSourceChunk(s.db.QueryRow(ctx, `SELECT `+sourceChunkColumns+` FROM source_chunks WHERE project_id=$1 AND id=$2`, projectID, id))
}

func (s *SourceChunkStore) GetChunks(ctx context.Context, projectID int64, ids []int64) ([]model.SourceChunk, error) {
	result, err := s.FindChunks(ctx, projectID, ids)
	if err != nil {
		return nil, err
	}
	if len(result) != len(ids) {
		return nil, model.FieldErrors{{Field: "sourceChunkIds", Message: "素材块不存在或属于其他项目"}}
	}
	return result, nil
}

// FindChunks omits missing content while retaining deterministic requested order,
// so exports can preserve missing_chunk provenance after explicit erasure.
func (s *SourceChunkStore) FindChunks(ctx context.Context, projectID int64, ids []int64) ([]model.SourceChunk, error) {
	if len(ids) == 0 {
		return []model.SourceChunk{}, nil
	}
	if len(ids) > 500 {
		return nil, model.FieldErrors{{Field: "sourceChunkIds", Message: "单个方向最多关联 500 个素材块"}}
	}
	rows, err := s.db.Query(ctx, `SELECT `+sourceChunkColumns+` FROM source_chunks WHERE project_id=$1 AND id=ANY($2)`, projectID, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	byID := map[int64]model.SourceChunk{}
	for rows.Next() {
		c, err := scanSourceChunk(rows)
		if err != nil {
			return nil, err
		}
		byID[c.ID] = c
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	result := make([]model.SourceChunk, 0, len(ids))
	for _, id := range ids {
		c, ok := byID[id]
		if !ok {
			continue
		}
		result = append(result, c)
	}
	return result, nil
}

func validateChunkIDsTx(ctx context.Context, tx pgx.Tx, projectID int64, ids []int64) error {
	if len(ids) == 0 {
		return nil
	}
	if len(ids) > 500 {
		return model.FieldErrors{{Field: "sourceChunkIds", Message: "最多关联 500 个素材块"}}
	}
	seen := map[int64]bool{}
	for _, id := range ids {
		if id <= 0 || seen[id] {
			return model.FieldErrors{{Field: "sourceChunkIds", Message: "素材块 ID 必须为正整数且不能重复"}}
		}
		seen[id] = true
	}
	var count int
	if err := tx.QueryRow(ctx, `SELECT COUNT(*) FROM source_chunks WHERE project_id=$1 AND id=ANY($2)`, projectID, ids).Scan(&count); err != nil {
		return err
	}
	if count != len(ids) {
		return model.FieldErrors{{Field: "sourceChunkIds", Message: "素材块不存在或属于其他项目"}}
	}
	return nil
}

func (s *SourceChunkStore) ChunkIDsExistInProject(ctx context.Context, projectID int64, ids []int64) error {
	_, err := s.GetChunks(ctx, projectID, ids)
	return err
}

func (s *SourceChunkStore) ListSourceChunks(ctx context.Context, projectID int64, stableID, q string, limit, offset int) ([]model.SourceChunk, int, error) {
	return s.listSourceChunks(ctx, projectID, stableID, q, limit, offset, nil)
}

// ListSourceChunksAtVersion filters before counting/pagination and uses only
// frozen IDs, so viewing an old source version never includes a later upload.
func (s *SourceChunkStore) ListSourceChunksAtVersion(ctx context.Context, projectID, versionID int64, stableID, q string, limit, offset int) ([]model.SourceChunk, int, error) {
	version, err := NewDocumentStore(s.db).GetVersionByID(ctx, versionID)
	if err != nil {
		return nil, 0, err
	}
	if version.ProjectID != projectID || version.Kind != model.KindSource {
		return nil, 0, pgx.ErrNoRows
	}
	var payload model.SourcePayload
	if err := json.Unmarshal(version.Payload, &payload); err != nil {
		return nil, 0, err
	}
	ids := []int64{}
	for _, doc := range payload.Documents {
		if stableID == "" || doc.StableID == stableID {
			ids = append(ids, doc.ChunkIDs...)
		}
	}
	return s.listSourceChunks(ctx, projectID, "", q, limit, offset, ids)
}

func (s *SourceChunkStore) listSourceChunks(ctx context.Context, projectID int64, stableID, q string, limit, offset int, frozenIDs []int64) ([]model.SourceChunk, int, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	if offset < 0 {
		offset = 0
	}
	q = strings.TrimSpace(q)
	where := ` FROM source_chunks WHERE project_id=$1 AND ($2='' OR EXISTS(SELECT 1 FROM source_document_chunks dc WHERE dc.project_id=$1 AND dc.source_document_stable_id=$2 AND dc.chunk_id=source_chunks.id)) AND ($3='' OR strpos(content,$3)>0 OR strpos(heading_path,$3)>0) AND ($4::bigint[] IS NULL OR id=ANY($4))`
	var total int
	if err := s.db.QueryRow(ctx, `SELECT COUNT(*)`+where, projectID, stableID, q, frozenIDs).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := s.db.Query(ctx, `SELECT `+sourceChunkColumns+where+` ORDER BY source_document_stable_id,ordinal,id LIMIT $5 OFFSET $6`, projectID, stableID, q, frozenIDs, limit, offset)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	items := []model.SourceChunk{}
	for rows.Next() {
		c, err := scanSourceChunk(rows)
		if err != nil {
			return nil, 0, err
		}
		items = append(items, c)
	}
	return items, total, rows.Err()
}
