CREATE TABLE IF NOT EXISTS model_providers (
  id BIGSERIAL PRIMARY KEY,
  name TEXT NOT NULL,
  base_url TEXT NOT NULL,
  model TEXT NOT NULL,
  provider_type TEXT NOT NULL DEFAULT 'openai-compatible',
  max_concurrency INTEGER NOT NULL DEFAULT 4,
  timeout_seconds INTEGER NOT NULL DEFAULT 120,
  is_active BOOLEAN NOT NULL DEFAULT TRUE,
  encrypted_api_key TEXT,
  api_key_masked TEXT NOT NULL DEFAULT '',
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS storage_profiles (
  id BIGSERIAL PRIMARY KEY,
  name TEXT NOT NULL,
  provider TEXT NOT NULL,
  endpoint TEXT NOT NULL,
  region TEXT NOT NULL DEFAULT '',
  bucket TEXT NOT NULL,
  access_key_id TEXT NOT NULL,
  encrypted_secret_key TEXT,
  secret_key_masked TEXT NOT NULL DEFAULT '',
  use_path_style BOOLEAN NOT NULL DEFAULT TRUE,
  is_default BOOLEAN NOT NULL DEFAULT FALSE,
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS generation_strategies (
  id BIGSERIAL PRIMARY KEY,
  name TEXT NOT NULL,
  description TEXT NOT NULL DEFAULT '',
  domain_count INTEGER NOT NULL DEFAULT 100,
  questions_per_domain INTEGER NOT NULL DEFAULT 10,
  answer_variants INTEGER NOT NULL DEFAULT 1,
  reward_variants INTEGER NOT NULL DEFAULT 1,
  planning_mode TEXT NOT NULL DEFAULT 'balanced',
  is_default BOOLEAN NOT NULL DEFAULT FALSE,
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS prompt_templates (
  id BIGSERIAL PRIMARY KEY,
  name TEXT NOT NULL,
  stage TEXT NOT NULL,
  version TEXT NOT NULL DEFAULT 'v1',
  system_prompt TEXT NOT NULL,
  user_prompt TEXT NOT NULL,
  is_active BOOLEAN NOT NULL DEFAULT TRUE,
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS audit_logs (
  id BIGSERIAL PRIMARY KEY,
  actor TEXT NOT NULL,
  action TEXT NOT NULL,
  resource_type TEXT NOT NULL,
  resource_id TEXT NOT NULL,
  detail TEXT NOT NULL DEFAULT '',
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
