package main

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/1420970597/llm/internal/llm"
	"github.com/1420970597/llm/internal/model"
)

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
	persistedDomains, _ := app.datasets.ListDomains(operationCtx, id)
	persistedEdges, _ := app.datasets.ListDomainEdges(operationCtx, id)
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
