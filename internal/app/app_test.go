package app

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/XiaoLeng-master/agentloom/internal/config"
	"github.com/XiaoLeng-master/agentloom/internal/core"
	"github.com/XiaoLeng-master/agentloom/internal/rag"
)

func TestDeepSeekExecutorAnswersAfterToolResult(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		var request struct {
			ToolChoice string `json:"tool_choice"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
		}
		if calls == 1 {
			w.Write([]byte(`{"choices":[{"message":{"content":null,"tool_calls":[{"id":"c1","function":{"name":"search_docs","arguments":"{\"query\":\"planner\"}"}}]}}]}`))
			return
		}
		if request.ToolChoice != "none" {
			t.Errorf("second tool_choice = %q", request.ToolChoice)
		}
		w.Write([]byte(`{"choices":[{"message":{"content":"answer from search"}}]}`))
	}))
	defer server.Close()
	model := core.DeepSeekClient{APIKey: "test", Endpoint: server.URL, Tools: []map[string]any{{"type": "function", "function": map[string]any{"name": "search_docs"}}}}
	a := NewWithModels([]rag.Document{{Source: "guide", Text: "planner executor verifier"}}, map[string]core.LLMClient{"executor": model})
	req := httptest.NewRequest(http.MethodPost, "/v1/chat", strings.NewReader(`{"message":"planner"}`))
	req.Header.Set("Authorization", "Bearer local-demo")
	rec := httptest.NewRecorder()
	a.Handler().ServeHTTP(rec, req)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "answer from search") || calls != 2 {
		t.Fatalf("status=%d body=%s calls=%d", rec.Code, rec.Body.String(), calls)
	}
}

func TestChatTraceFlow(t *testing.T) {
	handler := New([]rag.Document{{Source: "guide", Text: "agentloom planner executor verifier search guide"}}).Handler()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat", strings.NewReader(`{"message":"planner executor"}`))
	req.Header.Set("Authorization", "Bearer local-demo")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("chat: %d %s", rec.Code, rec.Body.String())
	}
	var reply struct {
		Reply        string `json:"reply"`
		TraceID      string `json:"trace_id"`
		Verification string `json:"verification"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &reply); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(reply.Reply, "guide") || reply.TraceID == "" || reply.Verification != "passed" {
		t.Fatalf("reply: %+v", reply)
	}
	traceReq := httptest.NewRequest(http.MethodGet, "/v1/traces/"+reply.TraceID, nil)
	traceReq.Header.Set("Authorization", "Bearer local-demo")
	traceRec := httptest.NewRecorder()
	handler.ServeHTTP(traceRec, traceReq)
	if traceRec.Code != 200 {
		t.Fatalf("trace: %d %s", traceRec.Code, traceRec.Body.String())
	}
	var trace Trace
	if err := json.Unmarshal(traceRec.Body.Bytes(), &trace); err != nil {
		t.Fatal(err)
	}
	if trace.ID != reply.TraceID || len(trace.Contracts) != 3 || len(trace.Retrievals) == 0 || trace.Retrievals[0].Source != "guide" {
		t.Fatalf("trace: %+v", trace)
	}
}

func TestPublicModeAllowsChatButProtectsEvolution(t *testing.T) {
	handler := New(nil).Handler()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat", strings.NewReader(`{"message":"hello"}`))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("chat status=%d body=%s", rec.Code, rec.Body.String())
	}
	req = httptest.NewRequest(http.MethodPost, "/v1/evolve", nil)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != 403 {
		t.Fatalf("evolve status=%d", rec.Code)
	}
}

func TestRuntimeConfigShowsCanarySplit(t *testing.T) {
	mgr := config.NewManager()
	mgr.Register("kb-agent", config.Config{Name: "kb-agent", SystemPrompt: "etcd-stable", ChunkStrategy: "recursive", PromptMode: "echo", TopK: 5})
	if _, err := mgr.StartCanary("kb-agent", config.Config{Name: "kb-agent", SystemPrompt: "etcd-canary", ChunkStrategy: "fixed", PromptMode: "echo", TopK: 3}, 10); err != nil {
		t.Fatal(err)
	}
	handler := New(nil).WithConfigManager(mgr).Handler()
	canaryTenant, ok := config.TenantForBucket(0)
	if !ok {
		t.Fatal("missing canary tenant")
	}
	stableTenant, ok := config.TenantForBucket(99)
	if !ok {
		t.Fatal("missing stable tenant")
	}
	read := func(tenant string) map[string]any {
		req := httptest.NewRequest(http.MethodGet, "/v1/config", nil)
		req.Header.Set("X-Tenant", tenant)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != 200 {
			t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
		}
		var body map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		return body
	}
	inside := read(canaryTenant)
	outside := read(stableTenant)
	if inside["system_prompt"] != "etcd-canary" || inside["version"] != float64(2) || inside["in_canary"] != true || inside["canary_percent"] != float64(10) {
		t.Fatalf("canary view=%v", inside)
	}
	if outside["system_prompt"] != "etcd-stable" || outside["version"] != float64(1) || outside["in_canary"] != false {
		t.Fatalf("stable view=%v", outside)
	}
}

func TestConfiguredPublicTokenIsRequired(t *testing.T) {
	handler := New(nil).WithAccessPolicy(AccessPolicy{PublicMode: true, PublicToken: "public", AdminToken: "admin"}).Handler()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat", strings.NewReader(`{"message":"hello"}`))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != 403 {
		t.Fatalf("missing token status=%d", rec.Code)
	}
	req = httptest.NewRequest(http.MethodPost, "/v1/chat", strings.NewReader(`{"message":"hello"}`))
	req.Header.Set("Authorization", "Bearer public")
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("public token status=%d body=%s", rec.Code, rec.Body.String())
	}
}
