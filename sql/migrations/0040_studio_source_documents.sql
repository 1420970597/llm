-- #217: source is a sixth immutable document kind. Reuse the import ledger;
-- durable bounded upload bytes live in PostgreSQL TOAST, never Redis/jobs.
ALTER TABLE versioned_documents DROP CONSTRAINT IF EXISTS versioned_documents_kind_check;
ALTER TABLE versioned_documents ADD CONSTRAINT versioned_documents_kind_check
  CHECK (kind IN ('blueprint','coverage','standard','quality_policy','mapping','source'));
ALTER TABLE document_versions DROP CONSTRAINT IF EXISTS document_versions_kind_check;
ALTER TABLE document_versions ADD CONSTRAINT document_versions_kind_check
  CHECK (kind IN ('blueprint','coverage','standard','quality_policy','mapping','source'));
ALTER TABLE legacy_imports DROP CONSTRAINT IF EXISTS legacy_imports_source_kind_check;
ALTER TABLE legacy_imports ADD CONSTRAINT legacy_imports_source_kind_check
  CHECK (source_kind IN ('dataset','source_document','source_product'));
ALTER TABLE legacy_imports ADD COLUMN IF NOT EXISTS source_content BYTEA;
ALTER TABLE legacy_imports ADD COLUMN IF NOT EXISTS source_options JSONB NOT NULL DEFAULT '{}'::jsonb;
ALTER TABLE legacy_imports ADD COLUMN IF NOT EXISTS job_id BIGINT REFERENCES jobs(id) ON DELETE SET NULL;
ALTER TABLE legacy_imports ADD CONSTRAINT legacy_imports_content_size_check
  CHECK (source_content IS NULL OR octet_length(source_content) <= 209715200);

CREATE TABLE IF NOT EXISTS source_chunks (
  id BIGSERIAL PRIMARY KEY,
  project_id BIGINT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
  source_document_stable_id TEXT NOT NULL,
  heading_path TEXT NOT NULL DEFAULT '',
  ordinal INTEGER NOT NULL CHECK (ordinal >= 1),
  content TEXT NOT NULL,
  content_hash TEXT NOT NULL,
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  UNIQUE (project_id, content_hash)
);
CREATE INDEX IF NOT EXISTS idx_source_chunks_project_document
  ON source_chunks (project_id,source_document_stable_id,ordinal);
-- Content deduplication can reuse a chunk across several documents/strategies.
CREATE TABLE IF NOT EXISTS source_document_chunks (
  project_id BIGINT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
  source_document_stable_id TEXT NOT NULL,
  chunk_id BIGINT NOT NULL REFERENCES source_chunks(id) ON DELETE CASCADE,
  ordinal INTEGER NOT NULL CHECK (ordinal >= 1),
  PRIMARY KEY (project_id,source_document_stable_id,chunk_id)
);
ALTER TABLE sample_versions ADD COLUMN IF NOT EXISTS source_chunk_ids JSONB NOT NULL DEFAULT '[]'::jsonb
  CHECK (jsonb_typeof(source_chunk_ids) = 'array');
COMMENT ON COLUMN legacy_imports.source_content IS 'Bounded durable upload staging; cleared after successful ingestion. No content in job/Redis payload.';
COMMENT ON TABLE source_chunks IS 'Native Markdown/TXT chunks retained until explicit project deletion; provenance IDs survive as missing_chunk in immutable samples.';

-- The legacy ledger survives project deletion for audit, but failed uploads
-- cannot be resumed without their project and must not retain staged bytes.
CREATE OR REPLACE FUNCTION clear_source_upload_on_project_delete() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
  UPDATE legacy_imports SET source_content=NULL
  WHERE target_project_id=OLD.id AND source_kind IN ('source_document','source_product');
  RETURN OLD;
END;
$$;
CREATE TRIGGER trg_projects_clear_source_upload BEFORE DELETE ON projects
  FOR EACH ROW EXECUTE FUNCTION clear_source_upload_on_project_delete();
