#!/usr/bin/env sh
# Execute fault tests serially by package: store fixtures intentionally clear
# their outbox records, so package concurrency would interfere with the worker
# fault acceptance. Required PASS lines prevent missing infrastructure skips.
set -eu
for name in LLM_TEST_POSTGRES_DSN LLM_TEST_REDIS_ADDR LLM_TEST_S3_ENDPOINT LLM_TEST_FAULT_CONTROL_DIR; do
  eval "value=\${$name:-}"
  if [ -z "$value" ]; then echo "::error::$name missing" >&2; exit 1; fi
done
output_file=$(mktemp)
trap 'rm -f "$output_file"' EXIT
for package in ./apps/worker ./apps/api ./internal/store; do
  if ! go test "$package" -count=1 -v -run '^(TestStudioFault|TestStudioRollbackStopsLegacyProjectWritesAndKeepsReadsAvailable|TestMarkBuildFailedIsIdempotentAndDoesNotOverwritePublished|TestFreezeReleaseIsIdempotentAndStartsBuilding)' >>"$output_file" 2>&1; then
    cat "$output_file"
    exit 1
  fi
done
cat "$output_file"
required='TestStudioFaultRedisInterruptionPreservesOutboxAndRecovers
TestStudioFaultLeaseRedeliveryRejectsStaleWorker
TestStudioFaultLostNotificationIsRearmed
TestStudioFaultIncompatibleEnvelopeIsPreservedAndValidReplaySucceeds
TestStudioFaultRollbackPausesQueueAndResumeCompletes
TestStudioFaultUnknownWorkerKindPreservesPendingUntilCompatible
TestStudioFaultObjectStoreFailureRetriesSameReleaseAndHash
TestStudioFaultRollbackKeepsPublishedDownloadAndRejectsCorruption
TestStudioRollbackStopsLegacyProjectWritesAndKeepsReadsAvailable
TestMarkBuildFailedIsIdempotentAndDoesNotOverwritePublished
TestFreezeReleaseIsIdempotentAndStartsBuilding'
for name in $required; do
  if ! grep -qE -- "--- PASS: $name " "$output_file"; then
    echo "::error::required fault test missing or skipped: $name" >&2
    exit 1
  fi
done
echo '[studio-failures] PASS all 11 required real-infrastructure tests'
