-- 0037: Atelier 不可变制品、manifest 与 hash（Issue #160 T21）
--
-- 契约来源：docs/plans/atelier-implementation.md §2.5（发布名与 ID）、§4.1（Release 对象）、
-- §4.2（发布状态机）；docs/plans/atelier-api-contract.md §2.9（冻结并发布）、§2.10（下载）。
--
-- 编号说明：§7.1 的编号表到 0034 截止，0035/0036 已被 T17/T18 占用，本迁移取 0037
--（同样不重编号已冻结的 0031–0034）。
--
-- 本迁移要解决的问题（#160 T21 的原文判断）：
--   旧导出把「上传对象」与「登记记录」当成一次操作，于是：
--     * 上传成功但 DB 失败 → 对象成了孤儿，而界面显示失败（用户重试会再传一份）；
--     * 上传超时但对象其实已存在 → 重试会覆盖它，而「已发布文件不可变」被破坏；
--     * 没有任何 hash 记录 → 无法回答「这个文件是不是当初那一份」。
--
-- 三条不可让步的性质：
--
--  1. **对象存储与 DB 不是单一事务**：顺序固定为
--     「编码算 hash → 写对象（同 hash 路径，幂等）→ 校验 size/hash/存在
--      → DB 事务登记制品与 manifest → published」。
--     因此「上传成功但 DB 失败」可以幂等续接，而**未确认的对象永不导致 published**。
--
--  2. **hash 分层且互不包含**：内容 hash（清单）→ artifact hash（文件字节）
--     → manifest hash（元数据）。manifest_hash 的输入**不包含它自己**，
--     否则它会变成自指而无法验证。
--
--  3. **存储身份固化**：制品记录它写入时的 storage profile 身份、
--     endpoint 与 bucket。此后切换默认存储或轮换凭证都**不修改文件**，
--     而下载仍能定位到当初那份对象（T21 验收项）。

-- ---------------------------------------------------------------------------
-- release_manifests：不可变的发布清单（元数据）
-- ---------------------------------------------------------------------------
--
-- 为什么与制品分开：一个发布可以有多个制品（jsonl/csv/alpaca 各一份），
-- 而 manifest 描述「这一版发布了什么」，是**一次**的。两者粒度不同，
-- 合在一起会让「多格式发布」不得不多行重复同一份 manifest。

CREATE TABLE IF NOT EXISTS release_manifests (
  id BIGSERIAL PRIMARY KEY,
  release_id BIGINT NOT NULL REFERENCES releases(id) ON DELETE CASCADE,
  revision BIGINT NOT NULL CHECK (revision >= 1),

  -- 规范化序列化后的 manifest（字段排序固定）。
  manifest JSONB NOT NULL,
  -- manifest_hash 的输入**不含它自己**（见 model.CanonicalManifestBytes）。
  manifest_hash TEXT NOT NULL,
  -- hash_scope 说明这次 hash 覆盖了什么范围，使「两个 hash 可比」有依据。
  hash_scope TEXT NOT NULL DEFAULT 'release_items+config',
  -- 清单项数与内容 hash（用于与制品对账）。
  item_count INTEGER NOT NULL DEFAULT 0 CHECK (item_count >= 0),
  items_content_hash TEXT NOT NULL DEFAULT '',

  encoder_version TEXT NOT NULL DEFAULT '',
  mapping_version_id BIGINT REFERENCES document_versions(id) ON DELETE RESTRICT,

  created_by BIGINT REFERENCES users(id),
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),

  UNIQUE (release_id, revision)
);

CREATE INDEX IF NOT EXISTS idx_release_manifests_release
  ON release_manifests (release_id, revision DESC);

-- ---------------------------------------------------------------------------
-- release_artifacts：不可变制品
-- ---------------------------------------------------------------------------
--
-- state 的三态与 §4.2 的发布状态机配套：
--   registered=已确认存在但尚未发布（可幂等续接）
--   verified  =对象存在且 size/hash 校验通过（发布的前置条件）
--   failed    =已知失败（错误可解释）
--
-- 刻意**没有**「deleting」态：已发布对象不得被普通写路径覆盖或删除
--（存储策略另行记录保留/删除流程）。删除是运维动作，不在这里表达。

CREATE TABLE IF NOT EXISTS release_artifacts (
  id BIGSERIAL PRIMARY KEY,
  release_id BIGINT NOT NULL REFERENCES releases(id) ON DELETE CASCADE,
  revision BIGINT NOT NULL CHECK (revision >= 1),

  -- 制品身份。
  artifact_type TEXT NOT NULL DEFAULT 'export'
    CHECK (artifact_type IN ('export', 'manifest', 'report')),
  format TEXT NOT NULL DEFAULT 'jsonl',

  -- 对象位置与**存储身份固化**（不跟随「当前默认存储」漂移）。
  object_key TEXT NOT NULL,
  storage_profile_id BIGINT REFERENCES storage_profiles(id) ON DELETE RESTRICT,
  storage_endpoint TEXT NOT NULL DEFAULT '',
  storage_bucket TEXT NOT NULL DEFAULT '',
  -- 对象版本（若存储支持）：使「同 key 被覆盖」也能定位到当初那份字节。
  object_version TEXT NOT NULL DEFAULT '',

  -- 字节事实。
  size_bytes BIGINT NOT NULL DEFAULT 0 CHECK (size_bytes >= 0),
  content_type TEXT NOT NULL DEFAULT 'application/jsonl',
  -- artifact_hash 是**文件字节**的 hash（与清单 hash、manifest hash 分开）。
  artifact_hash TEXT NOT NULL,
  -- items_content_hash 是参与编码的**清单内容**的 hash：
  -- 它使「文件内容是否符合清单」可独立校验（哈希分层的目的）。
  items_content_hash TEXT NOT NULL DEFAULT '',

  encoder_version TEXT NOT NULL DEFAULT '',
  mapping_version_id BIGINT REFERENCES document_versions(id) ON DELETE RESTRICT,

  state TEXT NOT NULL DEFAULT 'registered'
    CHECK (state IN ('registered', 'verified', 'failed')),
  error_class TEXT NOT NULL DEFAULT '',
  error_message TEXT NOT NULL DEFAULT '',
  uploaded_by BIGINT REFERENCES users(id),
  verified_at TIMESTAMPTZ,
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),

  -- 同一发布的同一修订下，同一格式只能有一份**当前**制品：
  -- 重复消息与重试都命中这一行，从而「只有一份有效发布」（T21 验收项）。
  UNIQUE (release_id, revision, artifact_type, format)
);

CREATE INDEX IF NOT EXISTS idx_release_artifacts_release
  ON release_artifacts (release_id, revision, id);

-- 「按 hash 找既有制品」是幂等续接的关键查询：
-- 上传后 DB 写入失败时，重试会先按 hash 找已存在的对象，而不是重传。
CREATE INDEX IF NOT EXISTS idx_release_artifacts_hash
  ON release_artifacts (release_id, artifact_hash);
