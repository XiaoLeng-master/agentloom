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
	a := New([]rag.Document{{Source: "guide", Text: "agentloom planner executor verifier guide"}}).WithConfigManager(mgr).WithCorpusPath(dataset).WithExperimentHistory(filepath.Join(dir, "history"))
	bench := httptest.NewRequest(http.MethodGet, "/v1/bench", nil)
	bench.Header.Set("Authorization", "Bearer local-demo")
	preview := httptest.NewRecorder()
	a.Handler().ServeHTTP(preview, bench)
	body := preview.Body.String()
	if preview.Code != 200 || strings.Contains(body, "not_evaluated") || strings.Contains(body, "答案质量尚未完成") {
		t.Fatalf("bench %d: %s", preview.Code, body)
	}
	for _, want := range []string{
		`"source_hit":"来源命中"`,
		`"lexical_proxy":"确定性词法代理已评"`,
		`"real_answer":"真实回答未评"`,
		`"quality_mode":"deterministic_proxy"`,
		`"before_quality":`,
		`"strategy":"fixed"`,
		`"strategy":"recursive"`,
		`"strategy":"semantic"`,
		`"strategy":"parent-child"`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("bench missing %s: %s", want, body)
		}
	}
	active, err := mgr.Acquire("kb-agent")
	if err != nil {
		t.Fatal(err)
	}
	if active.Config().Version != 1 || active.Config().ChunkStrategy != "fixed" {
		t.Fatalf("bench mutated config: %+v", active.Config())
	}
	active.Release()
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
	var experiment struct {
		ID            string `json:"id"`
		AnswerQuality string `json:"answer_quality"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &experiment); err != nil || experiment.ID == "" || experiment.AnswerQuality != "确定性词法代理已评" {
		t.Fatalf("experiment=%+v err=%v", experiment, err)
	}
	get := httptest.NewRequest(http.MethodGet, "/v1/evolutions/"+experiment.ID, nil)
	get.Header.Set("Authorization", "Bearer local-demo")
	readback := httptest.NewRecorder()
	a.Handler().ServeHTTP(readback, get)
	if readback.Code != 200 || !strings.Contains(readback.Body.String(), experiment.ID) {
		t.Fatalf("readback %d: %s", readback.Code, readback.Body.String())
	}
	after := chat()
	if before.ConfigVersion != 1 || before.ChunkStrategy != "fixed" || after.ConfigVersion != 2 || after.ChunkStrategy != "recursive" {
		t.Fatalf("before=%+v after=%+v", before, after)
	}
}
