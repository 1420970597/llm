package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/1420970597/llm/internal/model"
	"github.com/1420970597/llm/internal/store"
)

func TestModelPriceValidation(t *testing.T) {
	for _, input := range []modelPriceRequest{{}, {PriceVersion: "v1", InputPerMillion: -1}, {PriceVersion: "v1"}} {
		if len(input.validate()) == 0 {
			t.Fatalf("accepted invalid price %+v", input)
		}
	}
	for _, input := range []modelPriceRequest{{PriceVersion: "v1", InputPerMillion: 1000}, {PriceVersion: "free", IsFree: true}} {
		if len(input.validate()) != 0 {
			t.Fatalf("rejected valid price %+v", input)
		}
	}
}

func TestModelPriceHTTPAuthorizationAndRoundTrip(t *testing.T) {
	f := newWorkspaceAuthzIntegrationFixture(t)
	f.app.store = store.NewAdminStore(f.pool, nil)
	var providerID int64
	if err := f.pool.QueryRow(context.Background(), `INSERT INTO model_providers(name,base_url,model) VALUES('价格HTTP测试','http://price.test/v1','price-model') RETURNING id`).Scan(&providerID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = f.pool.Exec(context.Background(), `DELETE FROM model_price_versions WHERE provider_connection_id=$1`, providerID)
		_, _ = f.pool.Exec(context.Background(), `DELETE FROM model_providers WHERE id=$1`, providerID)
	})
	request := func(role string, body string) *http.Request {
		r := httptest.NewRequest(http.MethodPut, "/api/v1/settings/model-prices/1", bytes.NewBufferString(body))
		r.SetPathValue("providerId", strconv.FormatInt(providerID, 10))
		return r.WithContext(context.WithValue(r.Context(), userContextKey, model.User{ID: f.actor, Role: role}))
	}
	w := httptest.NewRecorder()
	f.app.putModelPrice(w, request("user", `{}`))
	if w.Code != 403 {
		t.Fatalf("ordinary user altered pricing: %d", w.Code)
	}
	w = httptest.NewRecorder()
	f.app.putModelPrice(w, request("admin", `{"priceVersion":"v1","inputPriceMinorPerMillion":-1}`))
	if w.Code != 422 {
		t.Fatalf("negative price %d %s", w.Code, w.Body.String())
	}
	w = httptest.NewRecorder()
	f.app.putModelPrice(w, request("admin", `{"priceVersion":"v1","inputPriceMinorPerMillion":1000,"outputPriceMinorPerMillion":2000,"isEstimated":true}`))
	if w.Code != 200 {
		t.Fatalf("save price %d %s", w.Code, w.Body.String())
	}
	w = httptest.NewRecorder()
	f.app.getModelPrice(w, request("admin", ``))
	var response struct {
		Price model.PriceVersion `json:"price"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if w.Code != 200 || response.Price.InputPerMillion != 1000 || !response.Price.IsEstimated || response.Price.ModelName != "price-model" || response.Price.EndpointFP != "price.test/v1" {
		t.Fatalf("price identity/estimate lost: %s", w.Body.String())
	}
}
