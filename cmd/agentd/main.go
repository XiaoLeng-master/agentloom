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
	"path/filepath"
	"strings"
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

func main() {
	addr := flag.String("addr", ":8080", "listen address")
	flag.Parse()
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	mgr, etcdSource, err := openConfig(ctx, os.Getenv)
	if err != nil {
		log.Fatal(err)
	}
	if etcdSource != nil {
		defer etcdSource.Close()
		go func() {
			if err := etcdSource.Watch(ctx, mgr); err != nil && ctx.Err() == nil {
				log.Fatalf("etcd watch: %v", err)
			}
		}()
	}

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
			model = "deepseek-flash"
		}
		for _, role := range []string{"executor"} {
			client := core.DeepSeekClient{APIKey: key, Model: model, MaxTokens: 1200, NoThinking: true}
			if role == "executor" {
				client.Tools = []map[string]any{{"type": "function", "function": map[string]any{"name": "search_docs", "description": "Search indexed documentation", "parameters": map[string]any{"type": "object", "properties": map[string]any{"query": map[string]any{"type": "string"}}, "required": []string{"query"}}}}}
			}
			models[role] = client
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

	<-ctx.Done()
	log.Println("shutting down...")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = srv.Shutdown(ctx)
}

func openConfig(ctx context.Context, getenv func(string) string) (*config.Manager, *config.EtcdSource, error) {
	mgr := config.NewManager()
	mgr.Register("kb-agent", config.Config{
		Name:          "kb-agent",
		SystemPrompt:  "You are a knowledge-base assistant.",
		Model:         "qwen2.5",
		MaxIter:       10,
		ChunkStrategy: "fixed",
	})
	opts, enabled, err := config.OptionsFromEnv(getenv)
	if err != nil {
		return nil, nil, err
	}
	if !enabled {
		log.Print("config source: memory")
		return mgr, nil, nil
	}
	src, err := config.DialEtcd(ctx, opts)
	if err != nil {
		return nil, nil, err
	}
	src.OnApply = func(applied config.Applied) {
		log.Printf("etcd applied kind=%s agent=%s version=%d percent=%d draining=%d drain_left=%d", applied.Kind, applied.Name, applied.Version, applied.Percent, applied.Draining, applied.DrainLeft)
	}
	src.OnError = func(err error) {
		log.Printf("etcd apply: %v", err)
	}
	if err := src.Sync(ctx, mgr); err != nil {
		_ = src.Close()
		return nil, nil, err
	}
	prefix := opts.Prefix
	if strings.TrimSpace(prefix) == "" {
		prefix = "/agentloom/agents/"
	}
	log.Printf("config source: etcd endpoints=%s prefix=%s", strings.Join(opts.Endpoints, ","), prefix)
	return mgr, src, nil
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}
