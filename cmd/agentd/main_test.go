package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/XiaoLeng-master/agentloom/internal/app"
	"github.com/XiaoLeng-master/agentloom/internal/config"
	"github.com/XiaoLeng-master/agentloom/internal/core"
)

func TestPublicDocsBecomeRetrievalEvidence(t *testing.T) {
	docs, err := publicDocs("../..")
	if err != nil {
		t.Fatal(err)
	}
	if len(docs) != 3 {
		t.Fatalf("loaded %d public docs", len(docs))
	}
	mgr := config.NewManager()
	mgr.Register("kb-agent", config.Config{Name: "kb-agent", SystemPrompt: "test", ChunkStrategy: "recursive"})
	handler := app.New(docs).WithConfigManager(mgr).Handler()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat", strings.NewReader(`{"message":"Planner Executor Verifier"}`))
	req.Header.Set("Authorization", "Bearer local-demo")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, req)
	if response.Code != http.StatusOK {
		t.Fatalf("status %d: %s", response.Code, response.Body.String())
	}
	var reply struct {
		TraceID string `json:"trace_id"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &reply); err != nil {
		t.Fatal(err)
	}
	get := httptest.NewRequest(http.MethodGet, "/v1/traces/"+reply.TraceID, nil)
	get.Header.Set("Authorization", "Bearer local-demo")
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, get)
	var trace app.Trace
	if err := json.Unmarshal(response.Body.Bytes(), &trace); err != nil {
		t.Fatal(err)
	}
	if len(trace.Retrievals) == 0 || trace.Retrievals[0].Source == "agentloom-demo" || trace.ChunkStrategy != "recursive" {
		t.Fatalf("trace evidence: %+v", trace)
	}
	foundRoles := false
	for _, item := range trace.Retrievals {
		if item.Source == "docs/01-architecture.md" && strings.Contains(item.Text, "Planner 池") && strings.Contains(item.Text, "Executor 池") && strings.Contains(item.Text, "Verifier 池") {
			foundRoles = true
		}
	}
	if !foundRoles {
		t.Fatalf("role responsibilities missing from evidence: %+v", trace.Retrievals)
	}
}

type countingLLM struct{ calls *int }

func (c countingLLM) Complete(context.Context, []core.Message) (core.Message, error) {
	*c.calls++
	return core.Message{Role: core.RoleAssistant, Content: "ok"}, nil
}

func TestCallBudgetRefusesBeyondLimit(t *testing.T) {
	calls := 0
	llm := budgetedLLM{inner: countingLLM{&calls}, budget: &callBudget{limit: 2}}
	for i := 0; i < 2; i++ {
		if _, err := llm.Complete(context.Background(), nil); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := llm.Complete(context.Background(), nil); !errors.Is(err, errCallBudget) {
		t.Fatalf("third call err = %v", err)
	}
	if calls != 2 {
		t.Fatalf("inner calls = %d, want 2", calls)
	}
}
