// Package app connects the existing runtime components for a local, deterministic demo.
package app

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/XiaoLeng-master/agentloom/internal/config"
	"github.com/XiaoLeng-master/agentloom/internal/core"
	"github.com/XiaoLeng-master/agentloom/internal/evolution"
	"github.com/XiaoLeng-master/agentloom/internal/gateway"
	"github.com/XiaoLeng-master/agentloom/internal/infra"
	"github.com/XiaoLeng-master/agentloom/internal/orchestrator"
	"github.com/XiaoLeng-master/agentloom/internal/rag"
)

type Trace struct {
	ID            string                  `json:"id"`
	Query         string                  `json:"query"`
	Answer        string                  `json:"answer"`
	Contracts     []orchestrator.Contract `json:"contracts"`
	CreatedAt     time.Time               `json:"created_at"`
	ConfigVersion int                     `json:"config_version,omitempty"`
	ChunkStrategy string                  `json:"chunk_strategy,omitempty"`
	Retrievals    []RetrievalEvidence     `json:"retrievals,omitempty"`
}

// RetrievalEvidence records the passages actually returned to the executor.
type RetrievalEvidence struct {
	Query  string  `json:"query"`
	Source string  `json:"source"`
	Text   string  `json:"text"`
	Score  float32 `json:"score"`
}

type retrievalLog struct {
	mu      sync.Mutex
	entries []RetrievalEvidence
}
type retrievalKey struct{}
type App struct {
	gate       *gateway.Gateway
	runtime    *orchestrator.Orchestrator
	mu         sync.RWMutex
	traces     map[string]Trace
	store      TraceStore
	queue      *infra.ToyQueue
	worker     chan struct{}
	failed     atomic.Int64
	modelMode  bool
	manager    *config.Manager
	corpusPath string
	history    *evolution.History
}

// WithConfigManager makes each request acquire one immutable configuration version.
func (a *App) WithConfigManager(manager *config.Manager) *App { a.manager = manager; return a }

// WithCorpusPath locates the public evaluation set for an offline experiment.
func (a *App) WithCorpusPath(path string) *App { a.corpusPath = path; return a }

// WithExperimentHistory enables readback of local experiment evidence.
func (a *App) WithExperimentHistory(dir string) *App {
	a.history = &evolution.History{Dir: dir}
	return a
}

// WithTraceStore enables persistent traces; New remains dependency-free for the local demo.
func (a *App) WithTraceStore(store TraceStore) *App {
	a.store = store
	a.queue = infra.NewToyQueue(256)
	a.worker = make(chan struct{})
	go func() {
		defer close(a.worker)
		_ = a.queue.Consume(context.Background(), 16, func(batch [][]byte) {
			for _, event := range batch {
				var trace Trace
				if err := json.Unmarshal(event, &trace); err != nil {
					a.failed.Add(1)
					continue
				}
				ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
				err := store.Save(ctx, trace)
				cancel()
				if err != nil {
					a.failed.Add(1)
				}
			}
		})
	}()
	return a
}

// Close drains accepted trace events before the backing store is closed.
func (a *App) Close() {
	if a.queue != nil {
		a.queue.Close()
		<-a.worker
	}
}

func New(docs []rag.Document) *App {
	return NewWithModels(docs, nil)
}

// NewWithModels uses supplied role models, retaining the deterministic demo when nil.
func NewWithModels(docs []rag.Document, models map[string]core.LLMClient) *App {
	retrievers := map[string]*rag.Retriever{}
	for _, splitter := range []rag.Splitter{rag.FixedSplitter{Size: 40}, rag.RecursiveSplitter{Size: 60, Overlap: 10}} {
		r := &rag.Retriever{Splitter: splitter, Embedder: rag.HashEmbedder{DimV: 512}, Store: &rag.MemoryStore{}}
		r.Index(docs)
		retrievers[splitter.Name()] = r
	}
	selectModel := func(role string) core.LLMClient {
		if models != nil && models[role] != nil {
			return models[role]
		}
		return demoLLM{role: role}
	}
	runtime := orchestrator.NewOrchestrator(1)
	runtime.AddAgent(orchestrator.RolePlanner, core.NewAgent("planner", "Return a JSON array of subtasks with id and input fields. No markdown.", selectModel("planner"), nil, 1))
	runtime.AddAgent(orchestrator.RoleExecutor, core.NewAgent("executor", "Use search_docs once for relevant evidence. Answer the full user question concisely, cite source paths, and distinguish target architecture from implemented behavior. If evidence is insufficient, say so.", selectModel("executor"), []core.Tool{searchTool{retrievers}}, 2))
	runtime.AddAgent(orchestrator.RoleVerifier, core.NewAgent("verifier", "Return JSON with task_id and pass fields. Check the execution result. No markdown.", demoLLM{role: "verifier"}, nil, 1))
	return &App{gate: gateway.New(localAuth{}, gateway.InjectionFilter{}, gateway.NewRateLimitFilter(60, time.Minute)), runtime: runtime, traces: map[string]Trace{}, modelMode: models != nil}
}

func (a *App) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/chat", a.chat)
	mux.HandleFunc("GET /v1/traces/{id}", a.trace)
	mux.HandleFunc("POST /v1/evolve", a.evolve)
	mux.HandleFunc("GET /v1/evolutions", a.evolutions)
	mux.HandleFunc("GET /v1/evolutions/{id}", a.experiment)
	mux.HandleFunc("GET /health/live", func(w http.ResponseWriter, r *http.Request) { write(w, 200, map[string]string{"status": "ok"}) })
	return mux
}

func (a *App) evolve(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Authorization") != "Bearer local-demo" {
		write(w, 403, map[string]string{"error": "invalid token"})
		return
	}
	if a.manager == nil {
		write(w, 503, map[string]string{"error": "configuration unavailable"})
		return
	}
	h, err := a.manager.Acquire("kb-agent")
	if err != nil {
		write(w, 503, map[string]string{"error": "configuration unavailable"})
		return
	}
	cfg := h.Config()
	h.Release()
	if cfg.ChunkStrategy != "fixed" {
		write(w, 409, map[string]string{"error": "active strategy must be fixed for this experiment"})
		return
	}
	path := a.corpusPath
	if path == "" {
		path = "datasets/rag_eval_98.json"
	}
	cases, docs, hash, err := evolution.LoadPublicCorpus(path)
	if err != nil {
		write(w, 503, map[string]string{"error": "public corpus unavailable"})
		return
	}
	prediction := evolution.Prediction{ExpectedHitRateDeltaMin: 0.10, ExpectedHitRateDeltaMax: 0.25}
	before, after, decision, err := evolution.ImproveRetrieval(a.manager, "kb-agent", cfg, cases, docs, hash,
		rag.FixedSplitter{Size: 40}, rag.RecursiveSplitter{Size: 60, Overlap: 10}, prediction)
	result, recordErr := evolution.NewExperiment(before, after, prediction, decision)
	if recordErr != nil {
		write(w, 500, map[string]string{"error": "experiment id unavailable"})
		return
	}
	if a.history != nil {
		if saveErr := a.history.Save(result); saveErr != nil {
			write(w, 503, map[string]any{"error": "experiment evidence not saved", "result": result})
			return
		}
	}
	if err != nil {
		write(w, 409, result)
		return
	}
	write(w, 200, result)
}

func (a *App) evolutions(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Authorization") != "Bearer local-demo" {
		write(w, 403, map[string]string{"error": "invalid token"})
		return
	}
	if a.history == nil {
		write(w, 503, map[string]string{"error": "experiment history unavailable"})
		return
	}
	results, err := a.history.List()
	if err != nil {
		write(w, 503, map[string]string{"error": "experiment history unavailable"})
		return
	}
	write(w, 200, results)
}

func (a *App) experiment(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Authorization") != "Bearer local-demo" {
		write(w, 403, map[string]string{"error": "invalid token"})
		return
	}
	if a.history == nil {
		write(w, 503, map[string]string{"error": "experiment history unavailable"})
		return
	}
	result, err := a.history.Get(r.PathValue("id"))
	if errors.Is(err, evolution.ErrExperimentNotFound) {
		write(w, 404, map[string]string{"error": "experiment not found"})
		return
	}
	if err != nil {
		write(w, 503, map[string]string{"error": "experiment history unavailable"})
		return
	}
	write(w, 200, result)
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
	ctx := r.Context()
	retrievals := &retrievalLog{}
	ctx = context.WithValue(ctx, retrievalKey{}, retrievals)
	var cfg config.Config
	if a.manager != nil {
		handle, err := a.manager.Acquire("kb-agent")
		if err != nil {
			write(w, 503, map[string]string{"error": "agent configuration unavailable"})
			return
		}
		defer handle.Release()
		cfg = handle.Config()
		ctx = context.WithValue(ctx, strategyKey{}, cfg.ChunkStrategy)
	}
	answer, contracts, err := a.runtime.Run(ctx, input.Message)
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
	retrievals.mu.Lock()
	evidence := append([]RetrievalEvidence(nil), retrievals.entries...)
	retrievals.mu.Unlock()
	trace := Trace{ID: id, Query: input.Message, Answer: answer, Contracts: contracts, CreatedAt: time.Now().UTC(), ConfigVersion: cfg.Version, ChunkStrategy: cfg.ChunkStrategy, Retrievals: evidence}
	a.mu.Lock()
	a.traces[id] = trace
	a.mu.Unlock()
	if a.queue != nil {
		data, _ := json.Marshal(trace)
		_ = a.queue.Push(data)
	}
	mode := "local-demo"
	if a.modelMode {
		mode = "deepseek"
	}
	write(w, 200, map[string]any{"reply": answer, "trace_id": id, "verification": "passed", "mode": mode})
}

func (a *App) trace(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Authorization") != "Bearer local-demo" {
		write(w, 403, map[string]string{"error": "invalid token"})
		return
	}
	var trace Trace
	var err error
	a.mu.RLock()
	var ok bool
	trace, ok = a.traces[r.PathValue("id")]
	a.mu.RUnlock()
	if !ok && a.store != nil {
		trace, err = a.store.Get(r.Context(), r.PathValue("id"))
	} else if !ok {
		err = ErrTraceNotFound
	}
	if errors.Is(err, ErrTraceNotFound) {
		write(w, 404, map[string]string{"error": "trace not found"})
		return
	}
	if err != nil {
		write(w, 503, map[string]string{"error": "trace storage unavailable"})
		return
	}
	write(w, 200, trace)
}

func write(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

type strategyKey struct{}
type searchTool struct{ retrievers map[string]*rag.Retriever }

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
	strategy, _ := ctx.Value(strategyKey{}).(string)
	if strategy == "" {
		strategy = "recursive"
	}
	retriever := s.retrievers[strategy]
	if retriever == nil {
		return "", fmt.Errorf("unknown chunk strategy %q", strategy)
	}
	results := retriever.Retrieve(query, 3)
	if len(results) == 0 {
		return "no indexed documents", nil
	}
	var parts []string
	for _, result := range results {
		passage := []rune(result.Text)
		if len(passage) > 1200 {
			passage = passage[:1200]
		}
		text := string(passage)
		parts = append(parts, fmt.Sprintf("Source: %s\nContent: %s", result.Source, text))
		if log, ok := ctx.Value(retrievalKey{}).(*retrievalLog); ok {
			log.mu.Lock()
			log.entries = append(log.entries, RetrievalEvidence{Query: query, Source: result.Source, Text: text, Score: result.Score})
			log.mu.Unlock()
		}
	}
	return strings.Join(parts, "\n\n"), nil
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
