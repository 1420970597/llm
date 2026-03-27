# Phase 4 Question Generation Pipeline

## Delivered
- Redis-backed async queue for question-generation jobs.
- API endpoint to enqueue question generation per dataset.
- Worker process that consumes queue jobs, generates questions, deduplicates by canonical hash, and persists them in PostgreSQL.
- User-facing question-generation controls and question preview list.

## Verification evidence
- `npm run build`
- Dockerized `go test ./...`
- `docker build -f deployments/docker/api.Dockerfile -t llm-api-phase4 .`
- `docker build -f deployments/docker/worker.Dockerfile -t llm-worker-phase4 .`
- End-to-end Docker real-provider flow with PostgreSQL + Redis + API + worker, producing persisted question records for the active dataset.

## Remaining follow-up for later phases
- Long reasoning/answer generation.
- Reward dataset generation.
- Artifact storage and export packaging.
