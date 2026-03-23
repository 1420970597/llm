# LLM Data Factory

Phase 1 foundation for an enterprise LLM long-chain-of-thought dataset factory.

## Stack
- React + TypeScript + Vite (`apps/web-user`, `apps/web-admin`)
- Go services (`apps/api`, `apps/worker`)
- PostgreSQL + Redis + MinIO via Docker Compose
- Docker multi-stage builds for all services

## Phase 1 deliverables
- Monorepo structure and baseline docs
- User and admin web shells
- API and worker service scaffolds
- Docker Compose local stack with conflict-safe host ports
- CI skeleton for web builds, Go tests, and Compose validation

## Quick start
1. Copy environment defaults if needed:
   - `cp .env.example .env`
2. Install web dependencies:
   - `npm install`
3. Build the frontends:
   - `npm run build`
4. Validate Docker Compose:
   - `npm run compose:config`
5. (Optional) smoke-test Go services in Docker:
   - `docker build -f deployments/docker/api.Dockerfile -t llm-api-phase1 .`
   - `docker build -f deployments/docker/worker.Dockerfile -t llm-worker-phase1 .`
6. Start the stack:
   - `npm run compose:up`

## Default host ports
- User portal: `http://localhost:3210`
- Admin portal: `http://localhost:3211`
- API health: `http://localhost:38080/healthz`
- PostgreSQL: `localhost:15432`
- Redis: `localhost:16379`
- MinIO API: `http://localhost:19000`
- MinIO Console: `http://localhost:19001`

These host ports are intentionally non-default so existing local services do not need to be stopped.

## Repository layout
- `apps/web-user` — user-facing dataset factory portal shell
- `apps/web-admin` — admin control plane shell
- `apps/api` — HTTP API service scaffold
- `apps/worker` — background worker scaffold
- `internal/config` — shared Go configuration helpers
- `deployments/docker` — Dockerfiles and nginx configs
- `deployments/compose` — local Compose topology
- `docs/architecture` — phase-level architecture notes
- `sql/migrations` — initial schema baseline
