package main

import (
  "encoding/json"
  "log"
  "net/http"
  "time"

  "github.com/1420970597/llm/internal/config"
)

func main() {
  cfg := config.LoadAPIConfig()

  mux := http.NewServeMux()
  mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
    writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "service": "api"})
  })
  mux.HandleFunc("/readyz", func(w http.ResponseWriter, _ *http.Request) {
    writeJSON(w, http.StatusOK, map[string]any{"status": "ready", "service": "api", "environment": cfg.Environment})
  })
  mux.HandleFunc("/api/v1/platform/overview", func(w http.ResponseWriter, _ *http.Request) {
    writeJSON(w, http.StatusOK, map[string]any{
      "name":        "llm-data-factory",
      "environment": cfg.Environment,
      "storage":     cfg.S3Endpoint,
      "database":    cfg.PostgresHost,
      "cache":       cfg.RedisHost,
    })
  })

  server := &http.Server{
    Addr:              ":" + cfg.Port,
    Handler:           loggingMiddleware(mux),
    ReadHeaderTimeout: 5 * time.Second,
  }

  log.Printf("api listening on :%s", cfg.Port)
  if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
    log.Fatalf("api server failed: %v", err)
  }
}

func loggingMiddleware(next http.Handler) http.Handler {
  return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
    start := time.Now()
    next.ServeHTTP(w, r)
    log.Printf("method=%s path=%s duration=%s", r.Method, r.URL.Path, time.Since(start))
  })
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
  w.Header().Set("Content-Type", "application/json")
  w.WriteHeader(status)
  _ = json.NewEncoder(w).Encode(payload)
}
