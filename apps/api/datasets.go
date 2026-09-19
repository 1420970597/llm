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

	"github.com/1420970597/llm/internal/llm"
	"github.com/1420970597/llm/internal/model"
)

// errProviderNotFound 表示请求里的 providerId 没有对应的模型服务。
// 用哨兵错误而不是直接写 400，是为了让调用方能把「用户输入错」与「查库失败」分开映射。
var errProviderNotFound = errors.New("指定的 AI 服务不存在")

// validateDatasetProvider 校验请求里的 providerId 能不能被引用，
// 并返回应该回给客户端的 HTTP 状态码。校验通过时返回 (0, nil)。
//
// providerId 为 0 表示「暂不绑定模型服务」，是既有的合法语义（列默认值就是 0），
// 必须放行：这类数据集仍然能建出来，到具体生成阶段才由该阶段自己报错。
//
// 但非 0 的 providerId 必须真实存在。否则数据集能建成功、却要到几分钟甚至
// 几十分钟后的生成阶段才失败（issue #58：providerId=999999 创建返回 201，
// 数据集已落库），用户拿到的是一个注定跑不通的任务。
func (app *application) validateDatasetProvider(ctx context.Context, providerID int64) (int, error) {
	return resolveDatasetProvider(ctx, providerID, app.store.ListProviders)
}

// resolveDatasetProvider 是校验规则的完整实现，只依赖一个「取 provider 列表」的
// 函数，不直接持有连接池。
//
// 为什么要抽成这个形状：错误 -> 状态码的映射（400 + 契约文案）与错误分类
// （用户的错 vs 查库失败）都是本修复的可观察契约，而 apps/api 里既有的测试
// 都不连库（write_error_test.go / routes_cleaning_rule_ids_test.go）。
// 把 list 作为参数后，全部分支（含查库失败必须报 500 而不是 400）
// 都能用表驱动测试锁死，且映射只实现一次，不会跟测试静默漂移。
//
// 不额外引入按 id 查 provider 的方法：provider 是管理员手工维护的少量配置，
// 全量列表的行数与调用频率都可忽略，这样改动就局限在 apps/api 内。
func resolveDatasetProvider(
	ctx context.Context,
	providerID int64,
	list func(context.Context) ([]model.ModelProvider, error),
) (int, error) {
	if providerID == 0 {
		return 0, nil
	}
	providers, err := list(ctx)
	if err != nil {
		// 查库失败不是用户的错：报 400 会让用户以为是自己填错了 id。
		return http.StatusInternalServerError, err
	}
	for _, provider := range providers {
		if provider.ID == providerID {
			return 0, nil
		}
	}
	return http.StatusBadRequest, errProviderNotFound
}

// errNoStorageProfileForDataset 是创建数据集时「没有任何可用结果存储」的用户可见提示。
//
// 与 store.ErrNoStorageProfile 分开：store 层的错误面向 worker 日志与内部调用，
// 这里面向用户表单，必须直接告诉他**去哪个页面**修。
var errNoStorageProfileForDataset = errors.New("尚未配置结果存储，请先到「系统设置 → 结果存储」创建一条可用配置")

// errStorageProfileNotFoundForDataset 是指定了 storageProfileId 但它不存在/已停用时
// 的用户可见提示。
var errStorageProfileNotFoundForDataset = errors.New("指定的结果存储配置不存在或已停用，请重新选择")

// validateDatasetStorage 校验请求里的 storageProfileId 能不能被引用，
// 并返回应该回给客户端的 HTTP 状态码。校验通过时返回 (0, nil)。
//
// storageProfileId 为 0 表示「用默认存储」，这是既有的合法语义（列默认值就是 0，
// 且 ResolveStorageProfile 会在 id=0 时挑 is_default 优先的那条）。
// 但**库里必须至少有一条可用配置**，否则该数据集注定在答案阶段失败（issue #83）——
// 这正是本校验要拦住的情况。
//
// 形状与 resolveDatasetProvider 一致：把「取列表」作为参数注入，理由相同 ——
// 错误 -> 状态码的映射（400 + 契约文案）与错误分类（用户的错 vs 查库失败）
// 都是本修复的可观察契约，而 apps/api 里既有测试都不连库，
// 把 list 作为参数后全部分支都能用表驱动测试锁死。
func resolveDatasetStorage(
	ctx context.Context,
	storageProfileID int64,
	list func(context.Context) ([]model.StorageProfile, error),
) (int, error) {
	profiles, err := list(ctx)
	if err != nil {
		// 查库失败不是用户的错：报 400 会让用户以为是自己填错了。
		return http.StatusInternalServerError, err
	}

	if storageProfileID == 0 {
		// 用默认存储：只要存在至少一条 active 的就行。
		for _, profile := range profiles {
			if profile.IsActive {
				return 0, nil
			}
		}
		return http.StatusBadRequest, errNoStorageProfileForDataset
	}

	for _, profile := range profiles {
		if profile.ID != storageProfileID {
			continue
		}
		if !profile.IsActive {
			return http.StatusBadRequest, errStorageProfileNotFoundForDataset
		}
		return 0, nil
	}
	return http.StatusBadRequest, errStorageProfileNotFoundForDataset
}

// validateDatasetStorage 把 resolveDatasetStorage 接到真实 store 上。
func (app *application) validateDatasetStorage(ctx context.Context, storageProfileID int64) (int, error) {
	return resolveDatasetStorage(ctx, storageProfileID, app.store.ListStorageProfiles)
}

func (app *application) estimatePlan(w http.ResponseWriter, r *http.Request) {
	var input model.GeneratePlanRequest
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		app.writeError(w, http.StatusBadRequest, err)
		return
	}
	estimate, err := app.datasets.Estimate(r.Context(), input.RootKeyword, input.TargetSize, input.StrategyID)
	if err != nil {
		app.writeError(w, http.StatusBadRequest, err)
		return
	}
	app.writeJSON(w, http.StatusOK, estimate)
}

func (app *application) createDataset(w http.ResponseWriter, r *http.Request) {
	var input model.Dataset
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		app.writeError(w, http.StatusBadRequest, err)
		return
	}
	// 校验放在 INSERT 之前：providerId 无效时不能留下半成品数据集。
	if status, err := app.validateDatasetProvider(r.Context(), input.ProviderID); err != nil {
		app.writeError(w, status, err)
		return
	}
	// 存储配置同理（issue #83）：答案/评分/导出三个阶段都要写对象存储。
	// 不在这里拦住的话，用户能建出一个**注定在答案阶段失败**的任务，
	// 而且失败原因只会以内部错误的形式出现在 worker 日志里。
	if status, err := app.validateDatasetStorage(r.Context(), input.StorageProfileID); err != nil {
		app.writeError(w, status, err)
		return
	}
	if input.Name == "" {
		input.Name = input.RootKeyword + " dataset"
	}
	if input.Status == "" {
		input.Status = "draft"
	}
	item, err := app.datasets.CreateDataset(r.Context(), input)
	if err != nil {
		app.writeError(w, http.StatusInternalServerError, err)
		return
	}
	_ = app.store.WriteAuditLog(r.Context(), "user", "create", "dataset", strconv.FormatInt(item.ID, 10), item.Name)
	app.writeJSON(w, http.StatusCreated, item)
}

func (app *application) listDatasets(w http.ResponseWriter, r *http.Request) {
	items, err := app.datasets.ListDatasets(r.Context())
	if err != nil {
		app.writeError(w, http.StatusInternalServerError, err)
		return
	}
	app.writeJSON(w, http.StatusOK, items)
}

func (app *application) getDataset(w http.ResponseWriter, r *http.Request) {
	id, err := datasetIDFromPath(r.URL.Path)
	if err != nil {
		app.writeError(w, http.StatusBadRequest, err)
		return
	}

	dataset, err := app.datasets.GetDataset(r.Context(), id)
	if err != nil {
		app.writeError(w, http.StatusInternalServerError, err)
		return
	}
	domains, err := app.datasets.ListDomains(r.Context(), id)
	if err != nil {
		app.writeError(w, http.StatusInternalServerError, err)
		return
	}
	edges, err := app.datasets.ListDomainEdges(r.Context(), id)
	if err != nil {
		app.writeError(w, http.StatusInternalServerError, err)
		return
	}
	app.writeJSON(w, http.StatusOK, model.DatasetGraph{Dataset: dataset, Domains: domains, Edges: edges})
}

func (app *application) generateDomains(w http.ResponseWriter, r *http.Request) {
	id, err := datasetIDFromPath(r.URL.Path)
	if err != nil {
		app.writeError(w, http.StatusBadRequest, err)
		return
	}
	log.Printf("domains.generate.start dataset_id=%d path=%s", id, r.URL.Path)

	operationCtx, cancel := context.WithTimeout(context.Background(), 9*time.Minute)
	defer cancel()

	dataset, err := app.datasets.GetDataset(operationCtx, id)
	if err != nil {
		log.Printf("domains.generate.dataset_error dataset_id=%d err=%v", id, err)
		app.writeError(w, http.StatusInternalServerError, err)
		return
	}

	baseURL, modelName, providerType, reasoningEffort, apiKey, err := app.datasets.ResolveProvider(operationCtx, dataset.ProviderID)
	if err != nil {
		log.Printf("domains.generate.provider_resolve_error dataset_id=%d provider_id=%d err=%v", id, dataset.ProviderID, err)
		app.writeError(w, http.StatusInternalServerError, err)
		return
	}
	log.Printf("domains.generate.provider dataset_id=%d provider_id=%d provider_type=%s model=%s base_url=%s reasoning_effort=%s", id, dataset.ProviderID, providerType, modelName, baseURL, reasoningEffort)

	promptTemplate, promptErr := app.store.GetActivePromptByStage(operationCtx, "domain-generation")
	var promptConfig *model.PromptTemplate
	if promptErr == nil {
		promptConfig = &promptTemplate
	}

	domains, edges, err := llm.GenerateDomains(operationCtx, llm.ProviderConfig{
		BaseURL:         baseURL,
		Model:           modelName,
		ProviderType:    providerType,
		ReasoningEffort: reasoningEffort,
		APIKey:          apiKey,
	}, dataset, promptConfig)
	if err != nil {
		log.Printf("domains.generate.llm_error dataset_id=%d provider_id=%d err=%v", id, dataset.ProviderID, err)
		app.writeError(w, http.StatusInternalServerError, err)
		return
	}
	log.Printf("domains.generate.llm_success dataset_id=%d domains=%d edges=%d", id, len(domains), len(edges))

	if err := app.datasets.ReplaceDomains(operationCtx, id, domains, edges); err != nil {
		log.Printf("domains.generate.persist_error dataset_id=%d err=%v", id, err)
		app.writeError(w, http.StatusInternalServerError, err)
		return
	}

	graph, err := app.datasets.GetDataset(operationCtx, id)
	if err != nil {
		log.Printf("domains.generate.reload_error dataset_id=%d err=%v", id, err)
		app.writeError(w, http.StatusInternalServerError, err)
		return
	}
	persistedDomains, err := app.datasets.ListDomains(operationCtx, id)
	if err != nil {
		log.Printf("domains.generate.list_domains_error dataset_id=%d err=%v", id, err)
		app.writeError(w, http.StatusInternalServerError, err)
		return
	}
	persistedEdges, err := app.datasets.ListDomainEdges(operationCtx, id)
	if err != nil {
		log.Printf("domains.generate.list_edges_error dataset_id=%d err=%v", id, err)
		app.writeError(w, http.StatusInternalServerError, err)
		return
	}
	log.Printf("domains.generate.done dataset_id=%d domains=%d edges=%d", id, len(persistedDomains), len(persistedEdges))
	app.writeJSON(w, http.StatusOK, model.DatasetGraph{Dataset: graph, Domains: persistedDomains, Edges: persistedEdges})
}

func (app *application) updateGraph(w http.ResponseWriter, r *http.Request) {
	id, err := datasetIDFromPath(r.URL.Path)
	if err != nil {
		app.writeError(w, http.StatusBadRequest, err)
		return
	}

	var payload struct {
		Domains []model.Domain `json:"domains"`
	}
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		app.writeError(w, http.StatusBadRequest, err)
		return
	}
	for index := range payload.Domains {
		payload.Domains[index].Canonical = canonicalName(payload.Domains[index].Name)
	}
	if err := app.datasets.UpdateGraph(r.Context(), id, payload.Domains); err != nil {
		app.writeError(w, http.StatusInternalServerError, err)
		return
	}
	app.writeJSON(w, http.StatusOK, map[string]any{"updated": len(payload.Domains)})
}

func (app *application) confirmDomains(w http.ResponseWriter, r *http.Request) {
	id, err := datasetIDFromPath(r.URL.Path)
	if err != nil {
		app.writeError(w, http.StatusBadRequest, err)
		return
	}
	if err := app.datasets.ConfirmDomains(r.Context(), id); err != nil {
		app.writeError(w, http.StatusInternalServerError, err)
		return
	}
	_ = app.store.WriteAuditLog(r.Context(), "user", "confirm", "dataset_domains", strconv.FormatInt(id, 10), "domains confirmed")
	app.writeJSON(w, http.StatusOK, map[string]string{"status": "domains_confirmed"})
}

func (app *application) pipelineProgress(w http.ResponseWriter, r *http.Request) {
	id, err := datasetIDFromPath(r.URL.Path)
	if err != nil {
		app.writeError(w, http.StatusBadRequest, err)
		return
	}
	progress, err := app.datasets.PipelineProgress(r.Context(), id)
	if err != nil {
		app.writeError(w, http.StatusInternalServerError, err)
		return
	}
	app.writeJSON(w, http.StatusOK, progress)
}

func datasetIDFromPath(path string) (int64, error) {
	parts := strings.Split(strings.Trim(path, "/"), "/")
	for index, part := range parts {
		if part == "datasets" && index+1 < len(parts) {
			return strconv.ParseInt(parts[index+1], 10, 64)
		}
	}
	return 0, strconv.ErrSyntax
}

func canonicalName(value string) string {
	lowered := strings.ToLower(strings.TrimSpace(value))
	return strings.Join(strings.Fields(lowered), " ")
}
