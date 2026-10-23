// Package app connects the existing runtime components for a local, deterministic demo.
package app

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/XiaoLeng-master/agentloom/internal/core"
	"github.com/XiaoLeng-master/agentloom/internal/gateway"
	"github.com/XiaoLeng-master/agentloom/internal/orchestrator"
	"github.com/XiaoLeng-master/agentloom/internal/rag"
)

type Trace struct {
	ID        string                  `json:"id"`
	Query     string                  `json:"query"`
	Answer    string                  `json:"answer"`
	Contracts []orchestrator.Contract `json:"contracts"`
	CreatedAt time.Time               `json:"created_at"`
}
type App struct {
	gate    *gateway.Gateway
	runtime *orchestrator.Orchestrator
	mu      sync.RWMutex
	traces  map[string]Trace
}

func New(docs []rag.Document) *App {
	retriever := &rag.Retriever{Splitter: rag.RecursiveSplitter{Size: 400, Overlap: 40}, Embedder: rag.HashEmbedder{DimV: 128}, Store: &rag.MemoryStore{}}
	retriever.Index(docs)
	runtime := orchestrator.NewOrchestrator(1)
	runtime.AddAgent(orchestrator.RolePlanner, core.NewAgent("planner", "local demo", demoLLM{role: "planner"}, nil, 1))
	runtime.AddAgent(orchestrator.RoleExecutor, core.NewAgent("executor", "local demo", demoLLM{role: "executor"}, []core.Tool{searchTool{retriever}}, 2))
	runtime.AddAgent(orchestrator.RoleVerifier, core.NewAgent("verifier", "local demo", demoLLM{role: "verifier"}, nil, 1))
	return &App{gate: gateway.New(localAuth{}, gateway.InjectionFilter{}, gateway.NewRateLimitFilter(60, time.Minute)), runtime: runtime, traces: map[string]Trace{}}
}

func (a *App) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/chat", a.chat)
	mux.HandleFunc("GET /v1/traces/{id}", a.trace)
	mux.HandleFunc("GET /health/live", func(w http.ResponseWriter, r *http.Request) { write(w, 200, map[string]string{"status": "ok"}) })
	return mux
}

func (a *App) chat(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Message string `json:"message"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&input); err != nil || strings.TrimSpace(input.Message) == "" {
		write(w, 400, map[string]string{"error": "message required"})
		return
	}
	token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	ok, filter, reason := a.gate.Allow(r.Context(), gateway.Request{Tenant: "local", Token: token, Prompt: input.Message, AgentName: "demo"})
	if !ok {
		write(w, 403, map[string]string{"error": filter + ": " + reason})
		return
	}
	answer, contracts, err := a.runtime.Run(r.Context(), input.Message)
	if err != nil {
		write(w, 502, map[string]string{"error": err.Error()})
		return
	}
	var bytes [16]byte
	if _, err := rand.Read(bytes[:]); err != nil {
		write(w, 500, map[string]string{"error": "trace id unavailable"})
		return
	}
	id := hex.EncodeToString(bytes[:])
	trace := Trace{ID: id, Query: input.Message, Answer: answer, Contracts: contracts, CreatedAt: time.Now().UTC()}
	a.mu.Lock()
	a.traces[id] = trace
	a.mu.Unlock()
	write(w, 200, map[string]any{"reply": answer, "trace_id": id, "verification": "passed", "mode": "local-demo"})
}

func (a *App) trace(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Authorization") != "Bearer local-demo" {
		write(w, 403, map[string]string{"error": "invalid token"})
		return
	}
	a.mu.RLock()
	trace, ok := a.traces[r.PathValue("id")]
	a.mu.RUnlock()
	if !ok {
		write(w, 404, map[string]string{"error": "trace not found"})
		return
	}
	write(w, 200, trace)
}

func write(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

type searchTool struct{ retriever *rag.Retriever }

type localAuth struct{}

func (localAuth) Name() string { return "local-auth" }
func (localAuth) Allow(_ context.Context, r gateway.Request) (bool, string) {
	if r.Token == "local-demo" {
		return true, ""
	}
	return false, "invalid demo token"
}

func (s searchTool) Name() string { return "search_docs" }
func (s searchTool) Schema() map[string]any {
	return map[string]any{"type": "object", "required": []string{"query"}}
}
func (s searchTool) Call(ctx context.Context, args map[string]any) (string, error) {
	query, ok := args["query"].(string)
	if !ok {
		return "", fmt.Errorf("query must be string")
	}
	results := s.retriever.Retrieve(query, 3)
	if len(results) == 0 {
		return "no indexed documents", nil
	}
	return fmt.Sprintf("%s (source: %s)", results[0].Text, results[0].Source), nil
}

type demoLLM struct{ role string }

func (d demoLLM) Complete(_ context.Context, msgs []core.Message) (core.Message, error) {
	user := ""
	for _, msg := range msgs {
		if msg.Role == core.RoleUser {
			user = msg.Content
			break
		}
	}
	switch d.role {
	case "planner":
		b, _ := json.Marshal([]orchestrator.SubTask{{ID: "t1", Input: user}})
		return core.Message{Role: core.RoleAssistant, Content: string(b)}, nil
	case "executor":
		for _, msg := range msgs {
			if msg.Role == core.RoleTool {
				return core.Message{Role: core.RoleAssistant, Content: msg.Content}, nil
			}
		}
		return core.Message{Role: core.RoleAssistant, ToolCalls: []core.ToolCall{{ID: "search-1", Name: "search_docs", Args: map[string]any{"query": user}}}}, nil
	case "verifier":
		var result orchestrator.ExecResult
		if err := json.Unmarshal([]byte(user), &result); err != nil {
			return core.Message{}, err
		}
		b, _ := json.Marshal(orchestrator.Verdict{TaskID: result.TaskID, Pass: result.Output != ""})
		return core.Message{Role: core.RoleAssistant, Content: string(b)}, nil
	}
	return core.Message{}, fmt.Errorf("unknown demo role")
}
