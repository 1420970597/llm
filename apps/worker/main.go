package main

import (
  "encoding/json"
  "log"
  "net/http"
  "time"

  "github.com/1420970597/llm/internal/config"
)

func main() {
  cfg := config.LoadWorkerConfig()

  go func() {
    ticker := time.NewTicker(15 * time.Second)
    defer ticker.Stop()
    for range ticker.C {
      log.Printf("worker heartbeat queue=%s redis=%s:%s", cfg.QueueName, cfg.RedisHost, cfg.RedisPort)
    }
  }()

  mux := http.NewServeMux()
  mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
    w.Header().Set("Content-Type", "application/json")
    _ = json.NewEncoder(w).Encode(map[string]string{"status": "ok", "service": "worker"})
  })

  server := &http.Server{
    Addr:              ":" + cfg.Port,
    Handler:           mux,
    ReadHeaderTimeout: 5 * time.Second,
  }

  log.Printf("worker listening on :%s", cfg.Port)
  if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
    log.Fatalf("worker server failed: %v", err)
  }
}
