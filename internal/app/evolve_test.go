package app

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/XiaoLeng-master/agentloom/internal/config"
	"github.com/XiaoLeng-master/agentloom/internal/rag"
)

func TestExperimentChangesSubsequentTraceVersion(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "guide.txt")
	if err := os.WriteFile(source, []byte("agentloom planner executor verifier guide"), 0600); err != nil {
		t.Fatal(err)
	}
	dataset := filepath.Join(dir, "cases.json")
	cases, _ := json.Marshal([]map[string]string{{"id": "case-1", "question": "planner", "sourceDoc": source}})
	if err := os.WriteFile(dataset, cases, 0600); err != nil {
		t.Fatal(err)
	}
	mgr := config.NewManager()
	mgr.Register("kb-agent", config.Config{Name: "kb-agent", SystemPrompt: "demo", ChunkStrategy: "fixed"})
	a := New([]rag.Document{{Source: "guide", Text: "agentloom planner executor verifier guide"}}).WithConfigManager(mgr).WithCorpusPath(dataset)
	chat := func() Trace {
		req := httptest.NewRequest(http.MethodPost, "/v1/chat", strings.NewReader(`{"message":"planner"}`))
		req.Header.Set("Authorization", "Bearer local-demo")
		rec := httptest.NewRecorder()
		a.Handler().ServeHTTP(rec, req)
		if rec.Code != 200 {
			t.Fatalf("chat %d: %s", rec.Code, rec.Body.String())
		}
		var reply struct {
			TraceID string `json:"trace_id"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &reply); err != nil {
			t.Fatal(err)
		}
		a.mu.RLock()
		trace := a.traces[reply.TraceID]
		a.mu.RUnlock()
		return trace
	}
	before := chat()
	req := httptest.NewRequest(http.MethodPost, "/v1/evolve", nil)
	req.Header.Set("Authorization", "Bearer local-demo")
	rec := httptest.NewRecorder()
	a.Handler().ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("evolve %d: %s", rec.Code, rec.Body.String())
	}
	after := chat()
	if before.ConfigVersion != 1 || before.ChunkStrategy != "fixed" || after.ConfigVersion != 2 || after.ChunkStrategy != "recursive" {
		t.Fatalf("before=%+v after=%+v", before, after)
	}
}
