package app

import (
	"encoding/json"
	"fmt"
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
	expected := filepath.Join(dir, "expected.txt")
	if err := os.WriteFile(expected, []byte("alpha beta gamma delta"), 0600); err != nil {
		t.Fatal(err)
	}
	rows := []map[string]string{{
		"id": "case-miss", "type": "retrieval", "question": "zebra", "groundTruth": "zebra", "sourceDoc": expected,
	}}
	for i := 0; i < 4; i++ {
		decoy := filepath.Join(dir, fmt.Sprintf("decoy-%d.txt", i))
		if err := os.WriteFile(decoy, []byte("zebra zebra zebra zebra"), 0600); err != nil {
			t.Fatal(err)
		}
		rows = append(rows, map[string]string{
			"id": fmt.Sprintf("case-decoy-%d", i), "type": "qa", "question": "zebra", "groundTruth": "missing-fact", "sourceDoc": decoy,
		})
	}
	dataset := filepath.Join(dir, "cases.json")
	cases, _ := json.Marshal(rows)
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
	if preview.Code != 200 || !strings.Contains(preview.Body.String(), `"answer_quality":"not_evaluated"`) {
		t.Fatalf("bench %d: %s", preview.Code, preview.Body.String())
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
		Tasks         []struct {
			Layer  string `json:"layer"`
			Cause  string `json:"cause"`
			Config struct {
				ChunkStrategy string `json:"chunk_strategy"`
				TopK          int    `json:"top_k"`
				PromptMode    string `json:"prompt_mode"`
				ToolMode      string `json:"tool_mode"`
			} `json:"config"`
			Prediction struct {
				Metric string  `json:"metric"`
				Min    float64 `json:"ExpectedHitRateDeltaMin"`
				Max    float64 `json:"ExpectedHitRateDeltaMax"`
			} `json:"prediction"`
		} `json:"tasks"`
		Promoted *struct {
			ChunkStrategy string `json:"chunk_strategy"`
			PromptMode    string `json:"prompt_mode"`
			TopK          int    `json:"top_k"`
			Version       int    `json:"version"`
		} `json:"promoted"`
		CauseCounts []struct {
			Layer string `json:"layer"`
			Cause string `json:"cause"`
			Count int    `json:"count"`
		} `json:"cause_counts"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &experiment); err != nil || experiment.ID == "" {
		t.Fatalf("experiment=%+v err=%v body=%s", experiment, err, rec.Body.String())
	}
	if !strings.HasPrefix(experiment.AnswerQuality, "deterministic_proxy:") {
		t.Fatalf("answer quality=%s", experiment.AnswerQuality)
	}
	if len(experiment.Tasks) < 2 {
		t.Fatalf("tasks=%s", rec.Body.String())
	}
	seen := map[string]bool{}
	for _, task := range experiment.Tasks {
		if task.Layer == "" || task.Cause == "" || task.Config.ChunkStrategy == "" || task.Config.TopK == 0 || task.Prediction.Metric == "" {
			t.Fatalf("incomplete task=%+v", task)
		}
		if task.Prediction.Min == 0.10 && task.Prediction.Max == 0.25 {
			t.Fatalf("hardcoded prediction on %+v", task)
		}
		seen[task.Config.ChunkStrategy+"/"+task.Prediction.Metric] = true
	}
	if len(seen) < 2 {
		t.Fatalf("tasks did not vary: %+v", experiment.Tasks)
	}
	if len(experiment.CauseCounts) == 0 || experiment.CauseCounts[0].Count <= 0 {
		t.Fatalf("cause counts=%+v", experiment.CauseCounts)
	}
	get := httptest.NewRequest(http.MethodGet, "/v1/evolutions/"+experiment.ID, nil)
	get.Header.Set("Authorization", "Bearer local-demo")
	readback := httptest.NewRecorder()
	a.Handler().ServeHTTP(readback, get)
	if readback.Code != 200 || !strings.Contains(readback.Body.String(), experiment.ID) || !strings.Contains(readback.Body.String(), `"tasks"`) {
		t.Fatalf("readback %d: %s", readback.Code, readback.Body.String())
	}
	after := chat()
	if before.ConfigVersion != 1 || before.ChunkStrategy != "fixed" {
		t.Fatalf("before=%+v", before)
	}
	if experiment.Promoted == nil {
		if after.ConfigVersion != 1 || after.ChunkStrategy != "fixed" {
			t.Fatalf("gate did not promote but config changed: %+v", after)
		}
		return
	}
	if after.ConfigVersion != experiment.Promoted.Version || after.ChunkStrategy != experiment.Promoted.ChunkStrategy || after.PromptMode != experiment.Promoted.PromptMode || after.TopK != experiment.Promoted.TopK {
		t.Fatalf("promoted=%+v after=%+v", experiment.Promoted, after)
	}
}

func TestConciseConfigChangesChatAnswer(t *testing.T) {
	docs := []rag.Document{{Source: "doc.txt", Text: strings.Repeat("needle token is the ground fact. ", 12)}}
	mgr := config.NewManager()
	mgr.Register("kb-agent", config.Config{Name: "kb-agent", SystemPrompt: "demo", ChunkStrategy: "fixed", PromptMode: "echo", TopK: 3})
	a := New(docs).WithConfigManager(mgr)
	ask := func() string {
		req := httptest.NewRequest(http.MethodPost, "/v1/chat", strings.NewReader(`{"message":"needle token"}`))
		req.Header.Set("Authorization", "Bearer local-demo")
		rec := httptest.NewRecorder()
		a.Handler().ServeHTTP(rec, req)
		if rec.Code != 200 {
			t.Fatalf("chat %d: %s", rec.Code, rec.Body.String())
		}
		var reply struct {
			Reply string `json:"reply"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &reply); err != nil {
			t.Fatal(err)
		}
		return reply.Reply
	}
	echo := ask()
	mgr.Reload("kb-agent", config.Config{Name: "kb-agent", SystemPrompt: "demo", ChunkStrategy: "fixed", PromptMode: "concise", TopK: 3})
	concise := ask()
	if echo == concise || !strings.Contains(concise, "Source:") {
		t.Fatalf("echo=%s\nconcise=%s", echo, concise)
	}
}
