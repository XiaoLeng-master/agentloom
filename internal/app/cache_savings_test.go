package app

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestMeasureCacheSavingsFewerCalls(t *testing.T) {
	got := MeasureCacheSavings()
	if got.EmbedWithCache >= got.EmbedWithoutCache || got.EmbedWithCache == 0 {
		t.Fatalf("embed with=%d without=%d", got.EmbedWithCache, got.EmbedWithoutCache)
	}
	if got.LLMWithCache >= got.LLMWithoutCache || got.LLMWithCache != 1 || got.LLMWithoutCache != got.Requests {
		t.Fatalf("llm with=%d without=%d requests=%d", got.LLMWithCache, got.LLMWithoutCache, got.Requests)
	}
}

func TestMetricsReportsBothLevels(t *testing.T) {
	handler := New(nil).Handler()
	req := httptest.NewRequest(http.MethodGet, "/v1/metrics", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("status %d %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Embed []struct {
			Level   string `json:"level"`
			Enabled bool   `json:"enabled"`
		} `json:"embed"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Embed) != 2 || body.Embed[0].Level != "l1" || !body.Embed[0].Enabled || body.Embed[1].Level != "l2" || body.Embed[1].Enabled {
		t.Fatalf("embed levels=%+v", body.Embed)
	}
	dir := t.TempDir()
	source := filepath.Join(dir, "guide.txt")
	if err := os.WriteFile(source, []byte("planner executor verifier"), 0600); err != nil {
		t.Fatal(err)
	}
	dataset := filepath.Join(dir, "cases.json")
	raw, _ := json.Marshal([]map[string]string{{"id": "c1", "question": "planner", "sourceDoc": source}})
	if err := os.WriteFile(dataset, raw, 0600); err != nil {
		t.Fatal(err)
	}
	benchHandler := New(nil).WithCorpusPath(dataset).Handler()
	bench := httptest.NewRequest(http.MethodGet, "/v1/bench", nil)
	benchRec := httptest.NewRecorder()
	benchHandler.ServeHTTP(benchRec, bench)
	if benchRec.Code != 200 {
		t.Fatalf("bench %d %s", benchRec.Code, benchRec.Body.String())
	}
	var benchBody map[string]any
	if err := json.Unmarshal(benchRec.Body.Bytes(), &benchBody); err != nil {
		t.Fatal(err)
	}
	if _, ok := benchBody["cache"]; !ok {
		t.Fatalf("bench missing cache: %s", benchRec.Body.String())
	}
}
