package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/XiaoLeng-master/agentloom/internal/app"
	"github.com/XiaoLeng-master/agentloom/internal/config"
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

func TestLocalAdminTokenOverride(t *testing.T) {
	handler := app.New(nil).WithAccessPolicy(app.AccessPolicy{PublicMode: true, AdminToken: "custom-admin"}).Handler()
	for _, token := range []string{"local-demo", "custom-admin"} {
		req := httptest.NewRequest(http.MethodPost, "/v1/evolve", nil)
		req.Header.Set("Authorization", "Bearer "+token)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, req)
		if token == "local-demo" && response.Code != http.StatusForbidden {
			t.Fatalf("default token remained valid: status %d", response.Code)
		}
		if token == "custom-admin" && response.Code == http.StatusForbidden {
			t.Fatalf("configured token rejected: status %d", response.Code)
		}
	}
}
