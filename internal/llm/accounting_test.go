package llm

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

type accountingProbe struct {
	deny     bool
	reserved int
	settled  []ResponseMetadata
	meta     RequestMetadata
}

func (probe *accountingProbe) ReserveCall(_ context.Context, meta RequestMetadata) (CallSettlement, error) {
	probe.reserved++
	probe.meta = meta
	if probe.deny {
		return nil, errors.New("configuration: budget exhausted")
	}
	return func(ctx context.Context, response ResponseMetadata, _ error) error {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		probe.settled = append(probe.settled, response)
		return nil
	}, nil
}

func TestCallAccountingReservesBeforeHTTPAndSettlesReceipt(t *testing.T) {
	calls := 0
	probe := &accountingProbe{deny: true}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		if probe.reserved == 0 {
			t.Error("HTTP preceded reservation")
		}
		_, _ = w.Write([]byte(`{"id":"req1","model":"m1","usage":{"prompt_tokens":12,"completion_tokens":8},"choices":[{"message":{"content":"ok"}}]}`))
	}))
	defer server.Close()
	provider := ProviderConfig{BaseURL: server.URL, Model: "m1", MaxTokens: 1024, Accounting: probe}
	if _, err := requestChatCompletion(context.Background(), provider, map[string]any{"messages": []any{}}, time.Second); err == nil || calls != 0 {
		t.Fatalf("failed reservation called provider: calls=%d err=%v", calls, err)
	}
	probe.deny = false
	if _, err := requestChatCompletion(context.Background(), provider, map[string]any{"messages": []any{}}, time.Second); err != nil {
		t.Fatal(err)
	}
	if calls != 1 || len(probe.settled) != 1 || probe.settled[0].RequestID != "req1" || probe.settled[0].Usage.InputTokens == nil || probe.meta.MaxOutputTokens != 1024 || probe.meta.ConfigFingerprint == "" {
		t.Fatalf("incomplete accounting: calls=%d probe=%+v", calls, probe)
	}
}

func TestCallAccountingSettlesEveryChargedRetry(t *testing.T) {
	probe := &accountingProbe{}
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		if calls == 1 {
			_, _ = w.Write([]byte(`{"id":"charged-empty","usage":{"prompt_tokens":10,"completion_tokens":2},"choices":[]}`))
			return
		}
		_, _ = w.Write([]byte(`{"id":"retry-ok","choices":[{"message":{"content":"ok"}}]}`))
	}))
	defer server.Close()
	_, err := requestChatCompletion(context.Background(), ProviderConfig{BaseURL: server.URL, Accounting: probe}, map[string]any{}, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if calls != 2 || probe.reserved != 2 || len(probe.settled) != 2 || probe.settled[0].RequestID != "charged-empty" || probe.settled[1].Usage.HasAnyToken() {
		t.Fatalf("retry must preserve paid empty response and unknown usage: %+v", probe)
	}
}

func TestCallAccountingSettlesTruncatedResponseWithoutRepeatingFrozenLimit(t *testing.T) {
	probe := &accountingProbe{}
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"id\":\"charged-length\",\"model\":\"m1\",\"choices\":[{\"delta\":{\"reasoning_details\":[{\"text\":\"格式示例：{\\\"answer\\\":\\\"...\\\"}\"}]},\"finish_reason\":\"length\"}]}\n\ndata: {\"id\":\"charged-length\",\"choices\":[],\"usage\":{\"prompt_tokens\":760,\"completion_tokens\":4096}}\n\ndata: [DONE]\n"))
	}))
	defer server.Close()
	_, err := requestChatCompletion(context.Background(), ProviderConfig{BaseURL: server.URL, Model: "m1", MaxTokens: 4096, Accounting: probe}, map[string]any{}, time.Second)
	if err != errCompletionTruncated {
		t.Fatalf("truncation err=%v", err)
	}
	if calls != 1 || probe.reserved != 1 || len(probe.settled) != 1 {
		t.Fatalf("same frozen limit was retried or receipt was lost: calls=%d probe=%+v", calls, probe)
	}
	settled := probe.settled[0]
	if settled.RequestID != "charged-length" || settled.Usage.OutputTokens == nil || *settled.Usage.OutputTokens != 4096 || settled.Usage.InputTokens == nil || *settled.Usage.InputTokens != 760 {
		t.Fatalf("paid truncated usage was not retained: %+v", settled)
	}
}
