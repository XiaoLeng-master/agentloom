package app

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestTracePipelineDefaultsToMemory(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/v1/trace-pipeline", nil)
	rec := httptest.NewRecorder()
	New(nil).Handler().ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("status %d", rec.Code)
	}
	var body struct {
		Backend string `json:"backend"`
		Enabled bool   `json:"enabled"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Enabled || body.Backend != "memory" {
		t.Fatalf("body=%+v", body)
	}
	post := httptest.NewRequest(http.MethodPost, "/v1/trace-pipeline", strings.NewReader(`{"id":"x"}`))
	rec = httptest.NewRecorder()
	New(nil).Handler().ServeHTTP(rec, post)
	if rec.Code != 409 {
		t.Fatalf("enqueue status %d", rec.Code)
	}
}
