# Phase 1 Foundation Notes

## Objectives
- Establish a monorepo with user/admin frontends, API/worker services, and a Docker-based local stack.
- Avoid host-service disruption by using non-default external ports.
- Create a refined visual baseline for future user-facing workflows.

## UI direction
- Apple-style visual system with soft gradients, layered surfaces, restrained shadows, and premium icons.
- Subtle entrance motion only; no decorative emoji.

## Backend direction
- Keep the Phase 1 Go scaffold minimal but production-oriented.
- Separate API and worker entrypoints while sharing configuration helpers.
- Prepare for later S3/PostgreSQL/Redis integration through environment-based config.

## Local infrastructure
- PostgreSQL, Redis, and MinIO run in Docker Compose.
- API and worker images are built from Go source in multi-stage Dockerfiles.
- Frontends are built with Vite and served by nginx containers.

## CI note
- A GitHub Actions workflow skeleton was prepared during Phase 1 validation but was not kept in the pushed commit because the available GitHub credential cannot update workflow files without the `workflow` scope.
- Until that scope is available, validation remains documented and reproducible via local `npm`, `docker build`, `docker compose config`, and Docker-based smoke checks.
