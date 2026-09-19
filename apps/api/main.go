package main

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/1420970597/llm/internal/config"
	appcrypto "github.com/1420970597/llm/internal/crypto"
	"github.com/1420970597/llm/internal/migrate"
	"github.com/1420970597/llm/internal/model"
	"github.com/1420970597/llm/internal/store"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
)

type application struct {
	cfg            config.APIConfig
	box            *appcrypto.SecretBox
	auth           *store.AuthStore
	store          *store.AdminStore
	datasets       *store.DatasetStore
	pipeline       *store.PipelineStore
	reasoning      *store.ReasoningStore
	rewards        *store.RewardStore
	artifacts      *store.ArtifactStore
	generationRuns *store.GenerationRunStore
	redis          *redis.Client
}

func main() {
	cfg := config.LoadAPIConfig()
	if cfg.EncryptionKey == "phase1-dev-only-32-byte-secret!!!" {
		log.Printf("WARNING: using default encryption key — set APP_ENCRYPTION_KEY in production")
	}
	ctx := context.Background()

	pool, err := pgxpool.New(ctx, cfg.PostgresDSN)
	if err != nil {
		log.Fatalf("postgres connect failed: %v", err)
	}
	defer pool.Close()

	if err := migrate.Run(ctx, pool, cfg.MigrationPath); err != nil {
		log.Fatalf("migration failed: %v", err)
	}

	box, err := appcrypto.NewSecretBox(cfg.EncryptionKey)
	if err != nil {
		log.Fatalf("secret box init failed: %v", err)
	}

	redisClient := redis.NewClient(&redis.Options{
		Addr: cfg.RedisHost + ":" + cfg.RedisPort,
	})

	app := &application{
		cfg:            cfg,
		box:            box,
		auth:           store.NewAuthStore(pool),
		store:          store.NewAdminStore(pool, box),
		datasets:       store.NewDatasetStore(pool, box),
		pipeline:       store.NewPipelineStore(pool),
		reasoning:      store.NewReasoningStore(pool),
		rewards:        store.NewRewardStore(pool),
		artifacts:      store.NewArtifactStore(pool, redisClient, cfg.QueueName),
		generationRuns: store.NewGenerationRunStore(pool),
		redis:          redisClient,
	}

	if err := app.reasoning.EnsureSchemaReady(ctx); err != nil {
		log.Fatalf("reasoning schema readiness failed: %v", err)
	}

	if err := app.auth.EnsureBootstrapUser(ctx, cfg.DefaultAdminEmail, cfg.DefaultAdminPassword, "admin"); err != nil {
		log.Fatalf("bootstrap admin user failed: %v", err)
	}
	if err := app.auth.EnsureBootstrapUser(ctx, cfg.DefaultUserEmail, cfg.DefaultUserPassword, "user"); err != nil {
		log.Fatalf("bootstrap user failed: %v", err)
	}

	// 从环境变量幂等引导默认 LLM provider（密钥加密落库，不写入日志）。
	if cfg.BootstrapProviderBaseURL != "" && cfg.BootstrapProviderAPIKey != "" {
		providerID, created, err := app.store.EnsureProvider(ctx, model.ModelProvider{
			Name:           cfg.BootstrapProviderName,
			BaseURL:        cfg.BootstrapProviderBaseURL,
			Model:          cfg.BootstrapProviderModel,
			ProviderType:   cfg.BootstrapProviderType,
			MaxConcurrency: cfg.BootstrapProviderMaxConcurrency,
			TimeoutSeconds: cfg.BootstrapProviderTimeoutSeconds,
			IsActive:       true,
			APIKey:         cfg.BootstrapProviderAPIKey,
		})
		if err != nil {
			log.Fatalf("bootstrap provider failed: %v", err)
		}
		if created {
			log.Printf("bootstrap provider ensured: id=%d model=%s", providerID, cfg.BootstrapProviderModel)
		}
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", app.health)
	mux.HandleFunc("GET /readyz", app.ready)
	mux.HandleFunc("POST /api/v1/auth/login", app.login)
	mux.HandleFunc("GET /api/v1/auth/me", app.me)
	mux.HandleFunc("POST /api/v1/auth/logout", app.logout)
	mux.HandleFunc("GET /api/v1/platform/overview", app.overview)
	mux.HandleFunc("GET /api/v1/platform/runtime", app.runtimeStatus)
	mux.HandleFunc("GET /api/v1/admin/dashboard", app.dashboard)
	mux.HandleFunc("GET /api/v1/admin/providers", app.listProviders)
	mux.HandleFunc("POST /api/v1/admin/providers", app.upsertProvider)
	mux.HandleFunc("PUT /api/v1/admin/providers", app.upsertProvider)
	mux.HandleFunc("POST /api/v1/admin/providers/models", app.providerModels)
	mux.HandleFunc("POST /api/v1/admin/providers/test", app.providerConnectivityTest)
	mux.HandleFunc("GET /api/v1/admin/storage-profiles", app.listStorageProfiles)
	mux.HandleFunc("POST /api/v1/admin/storage-profiles", app.upsertStorageProfile)
	mux.HandleFunc("PUT /api/v1/admin/storage-profiles", app.upsertStorageProfile)
	mux.HandleFunc("GET /api/v1/admin/generation-strategies", app.listStrategies)
	mux.HandleFunc("POST /api/v1/admin/generation-strategies", app.upsertStrategy)
	mux.HandleFunc("PUT /api/v1/admin/generation-strategies", app.upsertStrategy)
	mux.HandleFunc("GET /api/v1/admin/prompts", app.listPrompts)
	mux.HandleFunc("POST /api/v1/admin/prompts", app.upsertPrompt)
	mux.HandleFunc("PUT /api/v1/admin/prompts", app.upsertPrompt)
	mux.HandleFunc("GET /api/v1/admin/audit-logs", app.listAuditLogs)
	mux.HandleFunc("POST /api/v1/datasets/plans/estimate", app.estimatePlan)
	mux.HandleFunc("GET /api/v1/datasets", app.listDatasets)
	mux.HandleFunc("POST /api/v1/datasets", app.createDataset)
	mux.HandleFunc("GET /api/v1/datasets/", app.routeDatasetGet)
	mux.HandleFunc("POST /api/v1/datasets/", app.routeDatasetActions)

	// 应用各 lane 通过 init() 注册的新前缀路由（见 apps/api/routes.go）。
	applyRouteRegistrars(mux, app)

	server := &http.Server{
		Addr:              ":" + cfg.Port,
		Handler:           app.middleware(mux),
		ReadHeaderTimeout: 5 * time.Second,
	}

	log.Printf("api listening on :%s", cfg.Port)
	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatalf("api server failed: %v", err)
	}
}

func (app *application) health(w http.ResponseWriter, _ *http.Request) {
	app.writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "service": "api"})
}

func (app *application) ready(w http.ResponseWriter, _ *http.Request) {
	app.writeJSON(w, http.StatusOK, map[string]any{"status": "ready", "service": "api", "environment": app.cfg.Environment})
}

func (app *application) overview(w http.ResponseWriter, _ *http.Request) {
	app.writeJSON(w, http.StatusOK, map[string]any{
		"name":        "llm-data-factory",
		"environment": app.cfg.Environment,
		"storage":     app.cfg.S3Endpoint,
		"database":    app.cfg.PostgresHost,
		"cache":       app.cfg.RedisHost,
	})
}

func (app *application) dashboard(w http.ResponseWriter, r *http.Request) {
	dashboard, err := app.store.Dashboard(r.Context())
	if err != nil {
		app.writeError(w, http.StatusInternalServerError, err)
		return
	}
	app.writeJSON(w, http.StatusOK, dashboard)
}

func (app *application) listProviders(w http.ResponseWriter, r *http.Request) {
	items, err := app.store.ListProviders(r.Context())
	if err != nil {
		app.writeError(w, http.StatusInternalServerError, err)
		return
	}
	app.writeJSON(w, http.StatusOK, items)
}

func (app *application) upsertProvider(w http.ResponseWriter, r *http.Request) {
	var input model.ModelProvider
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		app.writeError(w, http.StatusBadRequest, err)
		return
	}
	item, err := app.store.UpsertProvider(r.Context(), input)
	if err != nil {
		app.writeError(w, http.StatusInternalServerError, err)
		return
	}
	_ = app.store.WriteAuditLog(r.Context(), "admin", "upsert", "model_provider", strconv.FormatInt(item.ID, 10), item.Name)
	app.writeJSON(w, http.StatusOK, item)
}

func (app *application) listStorageProfiles(w http.ResponseWriter, r *http.Request) {
	items, err := app.store.ListStorageProfiles(r.Context())
	if err != nil {
		app.writeError(w, http.StatusInternalServerError, err)
		return
	}
	app.writeJSON(w, http.StatusOK, items)
}

func (app *application) upsertStorageProfile(w http.ResponseWriter, r *http.Request) {
	var input model.StorageProfile
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		app.writeError(w, http.StatusBadRequest, err)
		return
	}
	item, err := app.store.UpsertStorageProfile(r.Context(), input)
	if err != nil {
		app.writeError(w, http.StatusInternalServerError, err)
		return
	}
	_ = app.store.WriteAuditLog(r.Context(), "admin", "upsert", "storage_profile", strconv.FormatInt(item.ID, 10), item.Name)
	app.writeJSON(w, http.StatusOK, item)
}

func (app *application) listStrategies(w http.ResponseWriter, r *http.Request) {
	items, err := app.store.ListStrategies(r.Context())
	if err != nil {
		app.writeError(w, http.StatusInternalServerError, err)
		return
	}
	app.writeJSON(w, http.StatusOK, items)
}

func (app *application) upsertStrategy(w http.ResponseWriter, r *http.Request) {
	var input model.GenerationStrategy
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		app.writeError(w, http.StatusBadRequest, err)
		return
	}
	item, err := app.store.UpsertStrategy(r.Context(), input)
	if err != nil {
		app.writeError(w, http.StatusInternalServerError, err)
		return
	}
	_ = app.store.WriteAuditLog(r.Context(), "admin", "upsert", "generation_strategy", strconv.FormatInt(item.ID, 10), item.Name)
	app.writeJSON(w, http.StatusOK, item)
}

func (app *application) listPrompts(w http.ResponseWriter, r *http.Request) {
	items, err := app.store.ListPrompts(r.Context())
	if err != nil {
		app.writeError(w, http.StatusInternalServerError, err)
		return
	}
	app.writeJSON(w, http.StatusOK, items)
}

func (app *application) upsertPrompt(w http.ResponseWriter, r *http.Request) {
	var input model.PromptTemplate
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		app.writeError(w, http.StatusBadRequest, err)
		return
	}
	item, err := app.store.UpsertPrompt(r.Context(), input)
	if err != nil {
		app.writeError(w, http.StatusInternalServerError, err)
		return
	}
	_ = app.store.WriteAuditLog(r.Context(), "admin", "upsert", "prompt_template", strconv.FormatInt(item.ID, 10), item.Name)
	app.writeJSON(w, http.StatusOK, item)
}

func (app *application) listAuditLogs(w http.ResponseWriter, r *http.Request) {
	items, err := app.store.ListAuditLogs(r.Context(), 100)
	if err != nil {
		app.writeError(w, http.StatusInternalServerError, err)
		return
	}
	app.writeJSON(w, http.StatusOK, items)
}

// writeError 统一写错误响应。
//
// 关键行为：调用方常把 store 层错误一律当作 500 上报，但 store 层在「记录不存在」
// 时返回的是 pgx.ErrNoRows —— 那是客户端问题（404），不是服务端故障。这里集中识别
// 并降级，否则每个 handler 都得自己判断（历史上 apps/api/datasets.go 的 getDataset
// 就是这么把 404 变成 500 的）。
//
// 另一个约定：5xx 不把内部错误原文回给客户端（可能含 SQL、连接串等），但也不能回
// 英文兜底文案 —— 前端会把它直接渲染给中文用户，所以用中文。
func (app *application) writeError(w http.ResponseWriter, status int, err error) {
	msg := err.Error()
	if status >= 500 {
		if errors.Is(err, pgx.ErrNoRows) {
			status = http.StatusNotFound
			msg = "请求的资源不存在"
		} else {
			log.Printf("internal error: %v", err)
			msg = "服务暂时不可用，请稍后重试"
		}
	}
	app.writeJSON(w, status, map[string]string{"error": msg})
}

func (app *application) writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

func (app *application) routeDatasetActions(w http.ResponseWriter, r *http.Request) {
	// 优先走注册表（lane 新增的数据集子路由），命中则不再进入 legacy 分支。
	if tryDatasetRouter(w, r) {
		return
	}
	switch {
	case strings.HasSuffix(r.URL.Path, "/domains/generate"):
		app.generateDomains(w, r)
	case strings.HasSuffix(r.URL.Path, "/domains/graph"):
		app.updateGraph(w, r)
	case strings.HasSuffix(r.URL.Path, "/domains/confirm"):
		app.confirmDomains(w, r)
	case strings.HasSuffix(r.URL.Path, "/questions/generate"):
		app.enqueueQuestionGeneration(w, r)
	case strings.HasSuffix(r.URL.Path, "/reasoning/generate"):
		app.enqueueReasoningGeneration(w, r)
	case strings.HasSuffix(r.URL.Path, "/rewards/generate"):
		app.enqueueRewardGeneration(w, r)
	case strings.HasSuffix(r.URL.Path, "/export"):
		app.enqueueExport(w, r)
	default:
		// 不认识的子路径一律 404。落这里说明既没注册到注册表，也不在 legacy 清单里 ——
		// 返 200 会让漏注册的端点静默成功（issue #8）。
		app.writeDatasetSubResourceNotFound(w)
	}
}

func (app *application) routeDatasetGet(w http.ResponseWriter, r *http.Request) {
	// 优先走注册表（lane 新增的数据集子路由），命中则不再进入 legacy 分支。
	if tryDatasetRouter(w, r) {
		return
	}
	switch {
	case strings.HasSuffix(r.URL.Path, "/questions"):
		app.listQuestions(w, r)
	case strings.HasSuffix(r.URL.Path, "/reasoning"):
		app.listReasoning(w, r)
	case strings.HasSuffix(r.URL.Path, "/rewards"):
		app.listRewards(w, r)
	case strings.HasSuffix(r.URL.Path, "/export/download"):
		app.downloadArtifact(w, r)
	case strings.HasSuffix(r.URL.Path, "/export"):
		app.listArtifacts(w, r)
	case strings.HasSuffix(r.URL.Path, "/pipeline/progress"):
		app.pipelineProgress(w, r)
	case strings.HasSuffix(r.URL.Path, "/domains"):
		// /domains 此前不在 switch 里，靠 default 落到 getDataset 才返回数据集图。
		// 换成 404 兜底后必须显式保留，且**保持响应形状不变**（测试与前端都依赖
		// 它返回带 domains 字段的对象），否则就是把 issue #8 的修复变成回归。
		app.getDataset(w, r)
	default:
		// 只有**精确指向数据集本身**（/api/v1/datasets/{id}[/]）才返回数据集图。
		// 其余子路径一律 404，否则任何漏注册的端点都会静默拿到 200 + 数据集图。
		suffix, ok := datasetSubResourceSuffix(r.URL.Path)
		if !ok {
			http.NotFound(w, r)
			return
		}
		if suffix != "" {
			app.writeDatasetSubResourceNotFound(w)
			return
		}
		app.getDataset(w, r)
	}
}
