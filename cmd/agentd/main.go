// Command agentd is the HTTP entry point for the agentloom runtime. It is a
// minimal runnable surface so `make run` brings the platform up; as the full
// Agent loop, MCP tools, and eval harness land, they wire in here behind the
// same HTTP surface. Today it exposes health + a list of registered agents,
// seeded from the config.Manager so the hot-reload path is live from boot.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"sync"
	"syscall"
	"time"

	"github.com/XiaoLeng-master/agentloom/internal/app"
	"github.com/XiaoLeng-master/agentloom/internal/config"
	"github.com/XiaoLeng-master/agentloom/internal/core"
	"github.com/XiaoLeng-master/agentloom/internal/rag"
	"github.com/XiaoLeng-master/agentloom/web"
)

func publicDocs(root string) ([]rag.Document, error) {
	paths := []string{"README.md", "docs/01-architecture.md", "docs/02-design-decisions.md"}
	docs := make([]rag.Document, 0, len(paths))
	for _, path := range paths {
		body, err := os.ReadFile(filepath.Join(root, path))
		if err != nil {
			return nil, err
		}
		docs = append(docs, rag.Document{Source: path, Text: string(body)})
	}
	return docs, nil
}

// callBudget caps real model calls in one agentd process. Limit <= 0 means no cap.
// It logs one line per attempt without request headers or the API key.
type callBudget struct {
	mu    sync.Mutex
	limit int
	used  int
}

var errCallBudget = errors.New("deepseek call budget exhausted")

func (b *callBudget) take() (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.limit > 0 && b.used >= b.limit {
		return b.used, errCallBudget
	}
	b.used++
	return b.used, nil
}

type budgetedLLM struct {
	inner  core.LLMClient
	budget *callBudget
}

func (m budgetedLLM) Complete(ctx context.Context, msgs []core.Message) (core.Message, error) {
	if _, err := m.budget.take(); err != nil {
		log.Printf("deepseek call refused: %v (limit %d)", err, m.budget.limit)
		return core.Message{}, err
	}
	return m.inner.Complete(ctx, msgs)
}

func logCall(budget *callBudget) func(core.CallStats) {
	return func(s core.CallStats) {
		budget.mu.Lock()
		n := budget.used
		budget.mu.Unlock()
		log.Printf("deepseek call n=%d model=%s response_model=%s status=%d latency_ms=%d prompt_tokens=%d completion_tokens=%d total_tokens=%d err=%q",
			n, s.Model, s.ResponseModel, s.Status, s.Latency.Milliseconds(), s.PromptTokens, s.CompletionTokens, s.TotalTokens, s.Err)
	}
}

func main() {
	addr := flag.String("addr", ":8080", "listen address")
	flag.Parse()

	// Seed one agent so the hot-reload manager is exercised from boot.
	mgr := config.NewManager()
	mgr.Register("kb-agent", config.Config{
		Name:          "kb-agent",
		SystemPrompt:  "You are a knowledge-base assistant.",
		Model:         "qwen2.5",
		MaxIter:       10,
		ChunkStrategy: "fixed",
	})

	mux := http.NewServeMux()
	publicMode := os.Getenv("AGENTLOOM_PUBLIC_MODE") == "1"
	publicToken := os.Getenv("AGENTLOOM_PUBLIC_TOKEN")
	adminToken := os.Getenv("AGENTLOOM_ADMIN_TOKEN")
	if publicMode && adminToken == "" {
		log.Fatal("AGENTLOOM_ADMIN_TOKEN is required in public mode")
	}
	var models map[string]core.LLMClient
	if key := os.Getenv("DEEPSEEK_API_KEY"); key != "" {
		models = map[string]core.LLMClient{}
		model := os.Getenv("DEEPSEEK_MODEL")
		if model == "" {
			model = core.DefaultModel
		}
		limit, _ := strconv.Atoi(os.Getenv("AGENTLOOM_DEEPSEEK_MAX_CALLS"))
		budget := &callBudget{limit: limit}
		for _, role := range []string{"executor"} {
			client := core.DeepSeekClient{APIKey: key, Model: model, MaxTokens: 1200, NoThinking: true, OnCall: logCall(budget)}
			if role == "executor" {
				client.Tools = []map[string]any{{"type": "function", "function": map[string]any{"name": "search_docs", "description": "Search indexed documentation", "parameters": map[string]any{"type": "object", "properties": map[string]any{"query": map[string]any{"type": "string"}}, "required": []string{"query"}}}}}
			}
			models[role] = budgetedLLM{inner: client, budget: budget}
		}
	}
	docs, err := publicDocs(".")
	if err != nil {
		log.Fatalf("load public docs: %v", err)
	}
	local := app.NewWithModels(docs, models)
	local.WithConfigManager(mgr).WithExperimentHistory(".agentloom/evolutions")
	if publicMode {
		local.WithAccessPolicy(app.AccessPolicy{PublicMode: true, PublicToken: publicToken, AdminToken: adminToken}).WithMaxConcurrentChats(4)
	}
	if redisAddr := os.Getenv("AGENTLOOM_REDIS_ADDR"); redisAddr != "" {
		store := app.NewRedisTraceStore(redisAddr, 24*time.Hour)
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		err := store.Ping(ctx)
		cancel()
		if err != nil {
			log.Fatalf("redis trace store unavailable: %v", err)
		}
		defer store.Close()
		local.WithTraceStore(store)
	}
	mux.Handle("/v1/", local.Handler())
	defer local.Close()
	mux.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{
			"status": "ok",
			"agents": mgr.Active(),
		})
	})
	mux.Handle("/", web.Handler())

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
