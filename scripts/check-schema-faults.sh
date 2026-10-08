#!/usr/bin/env sh
# Controlled T33 version mismatch probes. Called only for the disposable DB
# created by go-test-postgres.sh; never use against an existing environment.
set -eu
test_dsn="$1"
compat_output=$(mktemp)
trap 'rm -f "$compat_output"' EXIT
psql -v ON_ERROR_STOP=1 "$test_dsn" -c 'CREATE DATABASE studio_schema_fresh' >/dev/null
fresh_dsn="${test_dsn%/*}/studio_schema_fresh?sslmode=disable"
sh scripts/check-schema-compat.sh "$fresh_dsn"

# Every migration has already really been applied by go-test-postgres.sh.
# Record those filenames before exercising the deploy compatibility guard.
psql -v ON_ERROR_STOP=1 "$test_dsn" -c 'CREATE TABLE IF NOT EXISTS schema_migrations(filename TEXT PRIMARY KEY, applied_at TIMESTAMPTZ NOT NULL DEFAULT NOW())' >/dev/null
for file in sql/migrations/*.sql; do
  filename=$(basename "$file")
  psql -v ON_ERROR_STOP=1 "$test_dsn" -c "INSERT INTO schema_migrations(filename) VALUES('$filename') ON CONFLICT DO NOTHING" >/dev/null
done
sh scripts/check-schema-compat.sh "$test_dsn"
psql -v ON_ERROR_STOP=1 "$test_dsn" -c "INSERT INTO schema_migrations(filename) VALUES('9999_controlled_future_schema.sql')" >/dev/null
if sh scripts/check-schema-compat.sh "$test_dsn" >"$compat_output" 2>&1; then
  cat "$compat_output"
  echo '::error::future migration was accepted by an incompatible binary' >&2
  exit 1
fi
# The incompatible result is expected test evidence, not a failing CI
# annotation. Preserve its reason while keeping the job's error channel clean.
sed 's/::error::/[expected incompatibility] /' "$compat_output"
psql -v ON_ERROR_STOP=1 "$test_dsn" -c "DELETE FROM schema_migrations WHERE filename='9999_controlled_future_schema.sql'" >/dev/null
sh scripts/check-schema-compat.sh "$test_dsn"
echo '[studio-failures] PASS schema fresh/current/future rejected/recovered'
