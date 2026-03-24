package main

import (
	"encoding/json"
	"net/http"

	"github.com/1420970597/llm/internal/llm"
	"github.com/1420970597/llm/internal/model"
)

func (app *application) providerModels(w http.ResponseWriter, r *http.Request) {
	provider, err := app.resolveProviderDraft(r)
	if err != nil {
		app.writeError(w, http.StatusBadRequest, err)
		return
	}

	models, _, err := llm.FetchProviderModels(r.Context(), provider)
	if err != nil {
		app.writeError(w, http.StatusBadRequest, err)
		return
	}
	app.writeJSON(w, http.StatusOK, model.ProviderModelsResponse{Models: models})
}

func (app *application) providerConnectivityTest(w http.ResponseWriter, r *http.Request) {
	provider, err := app.resolveProviderDraft(r)
	if err != nil {
		app.writeError(w, http.StatusBadRequest, err)
		return
	}

	result, err := llm.TestProviderConnectivity(r.Context(), provider)
	if err != nil {
		app.writeError(w, http.StatusBadRequest, err)
		return
	}
	app.writeJSON(w, http.StatusOK, result)
}

func (app *application) resolveProviderDraft(r *http.Request) (model.ModelProvider, error) {
	var input model.ModelProvider
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		return model.ModelProvider{}, err
	}

	if input.ID == 0 {
		return input, nil
	}

	persisted, err := app.store.GetProviderWithSecret(r.Context(), input.ID)
	if err != nil {
		return model.ModelProvider{}, err
	}

	if input.Name != "" {
		persisted.Name = input.Name
	}
	if input.BaseURL != "" {
		persisted.BaseURL = input.BaseURL
	}
	if input.Model != "" {
		persisted.Model = input.Model
	}
	if input.ProviderType != "" {
		persisted.ProviderType = input.ProviderType
	}
	if input.ReasoningEffort != "" || persisted.ReasoningEffort != "" {
		persisted.ReasoningEffort = input.ReasoningEffort
	}
	if input.MaxConcurrency != 0 {
		persisted.MaxConcurrency = input.MaxConcurrency
	}
	if input.TimeoutSeconds != 0 {
		persisted.TimeoutSeconds = input.TimeoutSeconds
	}
	if input.APIKey != "" {
		persisted.APIKey = input.APIKey
	}
	persisted.IsActive = input.IsActive || persisted.IsActive
	return persisted, nil
}
