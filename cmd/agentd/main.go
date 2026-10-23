// Command agentd is the HTTP entry point for the agentloom runtime. It is a
// minimal runnable surface so `make run` brings the platform up; as the full
// Agent loop, MCP tools, and eval harness land, they wire in here behind the
// same HTTP surface. Today it exposes health + a list of registered agents,
// seeded from the config.Manager so the hot-reload path is live from boot.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/XiaoLeng-master/agentloom/internal/app"
	"github.com/XiaoLeng-master/agentloom/internal/config"
	"github.com/XiaoLeng-master/agentloom/internal/rag"
)

func main() {
	addr := flag.String("addr", ":8080", "listen address")
	flag.Parse()

	// Seed one agent so the hot-reload manager is exercised from boot.
	mgr := config.NewManager()
	mgr.Register("kb-agent", config.Config{
		Name:         "kb-agent",
		SystemPrompt: "You are a knowledge-base assistant.",
		Model:        "qwen2.5",
		MaxIter:      10,
	})

	mux := http.NewServeMux()
	local := app.New([]rag.Document{{Source: "agentloom-demo", Text: "agentloom uses a planner, executor and verifier. The executor can retrieve indexed documentation. This is a local deterministic demonstration."}})
	mux.Handle("/v1/", local.Handler())
	mux.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{
			"status": "ok",
			"agents": mgr.Active(),
		})
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{
			"service": "agentloom",
			"repo":    "github.com/XiaoLeng-master/agentloom",
			"agents":  mgr.Active(),
		})
	})

	srv := &http.Server{Addr: *addr, Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() {
		log.Printf("agentloom listening on %s (agents: %v)", *addr, mgr.Active())
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("listen: %v", err)
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	<-stop
	log.Println("shutting down...")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = srv.Shutdown(ctx)
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}
