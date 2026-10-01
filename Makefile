# 统一走仓库根入口，确保 .env 插值与 env_file 一致（见根 docker-compose.yml 注释）
COMPOSE := docker compose
GIT_SHA ?= $(shell git rev-parse HEAD 2>/dev/null || printf unknown)
BUILD_TIME ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)

.PHONY: install build web-build compose-config compose-up compose-down compose-logs docker-prune go-test-docker db-migrate-smoke legacy-inventory legacy-import

install:
	npm install

build: web-build

web-build:
	npm run build

compose-config:
	$(COMPOSE) config

compose-up:
	GIT_SHA="$(GIT_SHA)" BUILD_TIME="$(BUILD_TIME)" $(COMPOSE) up -d --build

compose-down:
	$(COMPOSE) down

compose-logs:
	$(COMPOSE) logs -f

docker-prune:
	docker builder prune -af

go-test-docker:
	docker run --rm -v $(PWD):/workspace -w /workspace golang:1.24-alpine sh -lc "go test ./..."

# 迁移冒烟：起临时 Postgres 应用**全部**迁移（AGENTS.md §1 描述的就是「验证 SQL 迁移脚本」）。
#
# 为什么不再内联只跑 0001（issue #217 B1）：旧 target 对 0022–0039 零覆盖，
# 却永远绿灯。逐文件应用 + 核心表断言在 scripts/db-migrate-smoke.sh 里，
# 且可单独运行（CI/人工同一入口）。
db-migrate-smoke:
	bash scripts/db-migrate-smoke.sh

# 旧数据迁移始终先盘点，再按 dataset 显式导入。DATASET_ID / ACTOR_ID
# 必须由操作者提供，避免把授权归属不明的数据自动带入项目工作区。
legacy-inventory:
	$(COMPOSE) exec api studio-migrate -dsn "postgres://$$POSTGRES_USER:$$POSTGRES_PASSWORD@$$POSTGRES_HOST:$$POSTGRES_PORT/$$POSTGRES_DB?sslmode=disable" -report /app/artifacts/studio-migrate/report.json

legacy-import:
	@command test -n "$(DATASET_ID)" && command test -n "$(ACTOR_ID)" && command test -n "$(OWNER_ID)" || (echo "用法：make legacy-import DATASET_ID=<id> ACTOR_ID=<admin-id> OWNER_ID=<owner-id> [WORKSPACE_ID=<id>]"; exit 2)
	$(COMPOSE) exec api studio-migrate -dsn "postgres://$$POSTGRES_USER:$$POSTGRES_PASSWORD@$$POSTGRES_HOST:$$POSTGRES_PORT/$$POSTGRES_DB?sslmode=disable" -import-dataset "$(DATASET_ID)" -actor "$(ACTOR_ID)" -owner "$(OWNER_ID)" -workspace "$(or $(WORKSPACE_ID),1)" -apply -resume
