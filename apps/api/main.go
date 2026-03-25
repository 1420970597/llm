package main

import (
	"context"
	"encoding/json"
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
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
)

type application struct {
	cfg       config.APIConfig
	box       *appcrypto.SecretBox
	auth      *store.AuthStore
	store     *store.AdminStore
	datasets  *store.DatasetStore
	pipeline  *store.PipelineStore
	reasoning *store.ReasoningStore
	rewards   *store.RewardStore
	artifacts *store.ArtifactStore
	redis     *redis.Client
}

func main() {
	cfg := config.LoadAPIConfig()
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
		cfg:       cfg,
		box:       box,
		auth:      store.NewAuthStore(pool),
		store:     store.NewAdminStore(pool, box),
		datasets:  store.NewDatasetStore(pool, box),
		pipeline:  store.NewPipelineStore(pool),
		reasoning: store.NewReasoningStore(pool),
		rewards:   store.NewRewardStore(pool),
		artifacts: store.NewArtifactStore(pool, redisClient, cfg.QueueName),
		redis:     redisClient,
	}

	if err := app.auth.EnsureBootstrapUser(ctx, cfg.DefaultAdminEmail, cfg.DefaultAdminPassword, "admin"); err != nil {
		log.Fatalf("bootstrap admin user failed: %v", err)
	}
	if err := app.auth.EnsureBootstrapUser(ctx, cfg.DefaultUserEmail, cfg.DefaultUserPassword, "user"); err != nil {
		log.Fatalf("bootstrap user failed: %v", err)
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

func (app *application) writeError(w http.ResponseWriter, status int, err error) {
	app.writeJSON(w, status, map[string]string{"error": err.Error()})
}

func (app *application) writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

func (app *application) routeDatasetActions(w http.ResponseWriter, r *http.Request) {
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
		http.NotFound(w, r)
	}
}

func (app *application) routeDatasetGet(w http.ResponseWriter, r *http.Request) {
	switch {
	case strings.HasSuffix(r.URL.Path, "/questions"):
		app.listQuestions(w, r)
	case strings.HasSuffix(r.URL.Path, "/reasoning"):
		app.listReasoning(w, r)
	case strings.HasSuffix(r.URL.Path, "/rewards"):
		app.listRewards(w, r)
	case strings.HasSuffix(r.URL.Path, "/export"):
		app.listArtifacts(w, r)
	case strings.HasSuffix(r.URL.Path, "/pipeline/progress"):
		app.pipelineProgress(w, r)
	default:
		app.getDataset(w, r)
	}
}
