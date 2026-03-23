COMPOSE := docker compose -f deployments/compose/docker-compose.yml

.PHONY: install build web-build compose-config compose-up compose-down compose-logs docker-prune go-test-docker db-migrate-smoke

install:
	npm install

build: web-build

web-build:
	npm run build

compose-config:
	$(COMPOSE) config

compose-up:
	$(COMPOSE) up -d --build

compose-down:
	$(COMPOSE) down

compose-logs:
	$(COMPOSE) logs -f

docker-prune:
	docker builder prune -af

go-test-docker:
	docker run --rm -v $(PWD):/workspace -w /workspace golang:1.24-alpine sh -lc "go test ./..."

db-migrate-smoke:
	docker rm -f llm-postgres-migrate-smoke >/dev/null 2>&1 || true
	docker run -d --rm --name llm-postgres-migrate-smoke -e POSTGRES_DB=llm_factory -e POSTGRES_USER=llm_factory -e POSTGRES_PASSWORD=llm_factory_dev -p 15433:5432 postgres:17-alpine
	sleep 6
	docker exec -i llm-postgres-migrate-smoke psql -U llm_factory -d llm_factory < sql/migrations/0001_phase1_foundation.sql
	docker exec llm-postgres-migrate-smoke psql -U llm_factory -d llm_factory -c "\dt"
	docker rm -f llm-postgres-migrate-smoke >/dev/null
