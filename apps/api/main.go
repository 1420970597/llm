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
	//
	// 注意：引导与 HTTP upsertProvider 共用 store.ValidateProviderInput（校验只该有一份），
	// 但配置不完整时**不能** log.Fatalf —— 那会让「少填一个字段」升级为「容器起不来」。
	// 详见 apps/api/provider_bootstrap.go 的说明与对应单测。
	bootstrapInput, outcome, validationErr := resolveBootstrapProvider(cfg)
	switch outcome {
	case bootstrapReady:
		providerID, created, err := app.store.EnsureProvider(ctx, bootstrapInput)
		if err != nil {
			log.Fatalf("bootstrap provider failed: %v", err)
		}
		if created {
			log.Printf("bootstrap provider ensured: id=%d model=%s", providerID, cfg.BootstrapProviderModel)
		}
	case bootstrapSkippedIncomplete:
		log.Printf("WARNING: 跳过默认 provider 引导，APP_BOOTSTRAP_PROVIDER_* 配置不完整: %v（服务继续启动，可在管理后台手动添加）", validationErr)
	}

	// 从环境变量幂等引导默认结果存储配置（issue #83）。
	//
	// 为什么需要：全新部署下 storage_profiles 表为空，而答案/评分/导出三个阶段都要写对象存储。
	// 没有引导时，用户能建出一个**注定在答案阶段失败**的任务，
	// 而且失败原因只以 `no rows in result set` 出现在 worker 日志里。
	//
	// 与 provider 引导同样：配置不完整就跳过并告警，**不阻断启动**
	//（详见 apps/api/storage_bootstrap.go 的说明与对应单测）。
	storageInput, storageOutcome, storageValidationErr := resolveBootstrapStorage(cfg)
	switch storageOutcome {
	case storageBootstrapReady:
		storageID, created, err := app.store.EnsureStorageProfile(ctx, storageInput)
		if err != nil {
			log.Fatalf("bootstrap storage profile failed: %v", err)
		}
		if created {
			log.Printf("bootstrap storage profile ensured: id=%d bucket=%s", storageID, cfg.BootstrapStorageBucket)
		}
	case storageBootstrapSkippedIncomplete:
		log.Printf("WARNING: 跳过默认结果存储引导，存储配置不完整: %v（服务继续启动；这会导致答案/评分/导出阶段失败，请在管理后台配置结果存储）", storageValidationErr)
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
		app.writeError(w, upsertErrorStatus(err), err)
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
		app.writeError(w, upsertErrorStatus(err), err)
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
		app.writeError(w, upsertErrorStatus(err), err)
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
		app.writeError(w, upsertErrorStatus(err), err)
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
//
// 第三条约定的由来（issue #102）：修复前上面两条只在 status >= 500 时生效，
// 而 handler 常把 store 层错误直接以 **4xx** 上报（例如
// `app.writeError(w, http.StatusNotFound, err)`，err 实际是 pgx.ErrNoRows）。
// 那时 msg = err.Error() 会把驱动原文 `no rows in result set` 原样回给客户端。
// 因此「不外泄内部实现」不能挂在状态码上，必须挂在**错误内容**上：
//
//   - 驱动层的「记录不存在」无论被上报成什么状态码，一律按 404 + 中文；
//   - 4xx 的文案若是内部实现细节（SQL/驱动/表列名），替换为按状态码区分的
//     安全中文，而不是原样透出；
//   - 但 handler 已经用 userFacingError 给出的**具体**文案（例如「未找到该任务」
//     与「未找到该评估运行」的区别）优先于上面的通用文案 —— 通用文案是兜底，
//     不是替代品：丢弃具体文案会让用户不知道到底是哪类资源不存在。
//
// 为什么不逐个修 6 个调用点：`writeError` 是本包全部 ~150 个 handler 的唯一错误出口，
// 逐个改会漏，且将来新写的 handler 会再次踩同一个坑。这里集中拦住才是根因修复。
func (app *application) writeError(w http.ResponseWriter, status int, err error) {
	// 优先级 1：handler 明确给出的用户可见文案（userFacingError）。
	// 它已经保证不含内部细节，只需要修正可能的错误状态码。
	var facing userFacingError
	if errors.As(err, &facing) {
		if errors.Is(err, pgx.ErrNoRows) {
			status = http.StatusNotFound
		} else if status == 0 {
			status = http.StatusBadRequest
		}
		app.writeJSON(w, status, map[string]string{"error": facing.Error()})
		return
	}

	// 优先级 2：驱动层「记录不存在」。它是 404 而不是 5xx，
	// 而且它的 Error() 就是英文驱动原文，绝不能进响应体。
	if errors.Is(err, pgx.ErrNoRows) {
		app.writeJSON(w, http.StatusNotFound, map[string]string{"error": msgResourceNotFound})
		return
	}

	// 优先级 3：其余情况（5xx 隐藏细节；4xx 若是内部细节则换安全文案）。
	msg := err.Error()
	if status >= 500 {
		log.Printf("internal error: %v", err)
		msg = "服务暂时不可用，请稍后重试"
	} else if looksLikeInternalDetail(msg) {
		// 4xx 但文案是内部实现细节：按状态码回中文，并留下日志便于定位。
		// 不把原文回给客户端，也不静默丢掉 —— 日志里仍可查到第一手信息。
		log.Printf("client error with internal detail (status=%d): %v", status, err)
		msg = clientErrorMessageFor(status)
	}
	app.writeJSON(w, status, map[string]string{"error": msg})
}

const msgResourceNotFound = "请求的资源不存在"

// internalDetailMarkers 是「这段文案属于内部实现」的识别标记。
//
// 刻意只认驱动/SQL 层面的特征串，不做「含英文就替换」的宽泛判断 ——
// 那会误伤合法的业务英文（例如用户填的模型名、导出格式名），
// 而本仓库的产品文案本来就应当是中文（见 issue #102 的期望）。
var internalDetailMarkers = []string{
	"no rows in result set", // pgx.ErrNoRows 的 Error()
	"SQLSTATE",              // Postgres 错误码前缀
	"pq: ",                  // lib/pq 风格错误前缀
	"pgx",
	"sql: ",          // database/sql 风格
	"syntax error",   // SQL 语法错误
	"duplicate key",  // 唯一约束冲突
	"violates ",      // constraint violation
	"relation \"",    // 表名泄漏
	"column \"",      // 列名泄漏
	"does not exist", // 未映射的库层错误
	"connection refused",
	"i/o timeout",

	// encoding/json 的错误文案会原样拼出这些英文片段。
	// 用户提交畸形 body 时（可被轻易触发，例如 `{bad json`）
	// 它们会经 writeError(w, 400, err) 直接渲染到界面上。
	// `invalid character` 来自 Decoder 的 SyntaxError，
	// `cannot unmarshal` / `unexpected end of JSON` 来自 Unmarshal/UnmarshalTypeError，
	// 是不同代码路径，都要覆盖。
	"looking for beginning of",
	"unexpected end of JSON",
	"cannot unmarshal",
	"invalid character",
	"json: ",
}

// looksLikeInternalDetail 判断一段错误文案是否含内部实现细节。
//
// 大小写不敏感：驱动文案的大小写并不稳定（`no rows in result set` 是小写，
// 但 SQLSTATE 是大写）。
func looksLikeInternalDetail(msg string) bool {
	lower := strings.ToLower(msg)
	for _, marker := range internalDetailMarkers {
		if strings.Contains(lower, strings.ToLower(marker)) {
			return true
		}
	}
	return false
}

// clientErrorMessageFor 给出按状态码区分的安全中文文案。
//
// 为什么不用一句通用兜底：404 与 400 对用户意味着完全不同的下一步动作。
// 404 要说「资源不存在」（用户应回去确认任务/记录是否还在），
// 400 要说「请求参数有误」（用户应检查自己填的内容）。
func clientErrorMessageFor(status int) string {
	switch status {
	case http.StatusNotFound:
		return msgResourceNotFound
	case http.StatusConflict:
		return "当前状态不允许执行该操作，请刷新后重试"
	case http.StatusForbidden:
		return "没有执行该操作的权限，请联系管理员"
	case http.StatusUnauthorized:
		return "登录状态已失效，请重新登录"
	case http.StatusMethodNotAllowed:
		return "该操作不被支持，请刷新页面后重试"
	default:
		return "请求格式有误，请检查填写的内容后重试"
	}
}

func (app *application) writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

// upsertErrorStatus 把 4 个 admin 配置 upsert 的 store 错误映射为 HTTP 状态码。
//
// store.ValidationError 表示用户填错了字段，属于客户端问题（issue #63）：
// 它必须变成 400 + 具体字段名，不能被当成 500。其余错误仍走 500，
// 由 writeError 统一隐藏内部细节。
func upsertErrorStatus(err error) int {
	var validationErr *store.ValidationError
	if errors.As(err, &validationErr) {
		return http.StatusBadRequest
	}
	return http.StatusInternalServerError
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
