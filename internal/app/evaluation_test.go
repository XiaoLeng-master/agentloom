package app

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/XiaoLeng-master/agentloom/internal/rag"
)

func TestRuntimeTraceEvaluationBatchIsLinkedAndReadable(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "guide.md")
	if err := os.WriteFile(source, []byte("agentloom planner executor verifier"), 0600); err != nil {
		t.Fatal(err)
	}
	dataset := filepath.Join(dir, "cases.json")
	a := New([]rag.Document{{Source: source, Text: "agentloom planner executor verifier"}}).WithExperimentHistory(dir).WithCorpusPath(dataset)
	chat := httptest.NewRequest(http.MethodPost, "/v1/chat", strings.NewReader(`{"message":"planner executor"}`))
	chat.Header.Set("Authorization", "Bearer local-demo")
	chatResponse := httptest.NewRecorder()
	a.Handler().ServeHTTP(chatResponse, chat)
	if chatResponse.Code != http.StatusOK {
		t.Fatalf("chat status=%d body=%s", chatResponse.Code, chatResponse.Body.String())
	}
	var reply struct {
		TraceID string `json:"trace_id"`
		Reply   string `json:"reply"`
	}
	if err := json.Unmarshal(chatResponse.Body.Bytes(), &reply); err != nil || reply.TraceID == "" {
		t.Fatalf("reply=%+v err=%v", reply, err)
	}
	cases, err := json.Marshal([]map[string]string{{"id": "runtime-1", "question": "planner executor", "groundTruth": reply.Reply, "sourceDoc": source}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dataset, cases, 0600); err != nil {
		t.Fatal(err)
	}

	requestBody := `{"trace_ids":["` + reply.TraceID + `"]}`
	evaluate := httptest.NewRequest(http.MethodPost, "/v1/evaluations", strings.NewReader(requestBody))
	evaluate.Header.Set("Authorization", "Bearer local-demo")
	evaluationResponse := httptest.NewRecorder()
	a.Handler().ServeHTTP(evaluationResponse, evaluate)
	if evaluationResponse.Code != http.StatusCreated {
		t.Fatalf("evaluate status=%d body=%s", evaluationResponse.Code, evaluationResponse.Body.String())
	}
	var batch struct {
		ID       string   `json:"id"`
		Mode     string   `json:"mode"`
		TraceIDs []string `json:"trace_ids"`
		Passed   int      `json:"passed"`
		Reports  []struct {
			AnswerQuality *struct {
				Mode   string `json:"mode"`
				CaseID string `json:"case_id"`
			} `json:"answer_quality"`
		} `json:"reports"`
	}
	if err := json.Unmarshal(evaluationResponse.Body.Bytes(), &batch); err != nil || batch.ID == "" || batch.Mode != "runtime_evidence_rules" || batch.Passed != 1 || len(batch.TraceIDs) != 1 || batch.TraceIDs[0] != reply.TraceID || len(batch.Reports) != 1 || batch.Reports[0].AnswerQuality == nil || batch.Reports[0].AnswerQuality.Mode != "deterministic_actual_answer" || batch.Reports[0].AnswerQuality.CaseID != "runtime-1" {
		t.Fatalf("batch=%+v err=%v body=%s", batch, err, evaluationResponse.Body.String())
	}
	get := httptest.NewRequest(http.MethodGet, "/v1/evaluations/"+batch.ID, nil)
	get.Header.Set("Authorization", "Bearer local-demo")
	readback := httptest.NewRecorder()
	a.Handler().ServeHTTP(readback, get)
	if readback.Code != http.StatusOK || !strings.Contains(readback.Body.String(), reply.TraceID) {
		t.Fatalf("readback status=%d body=%s", readback.Code, readback.Body.String())
	}
	data, err := os.ReadFile(filepath.Join(dir, "trace-batches", batch.ID+".json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "planner executor") || strings.Contains(string(data), "agentloom planner executor verifier") {
		t.Fatalf("batch evidence persisted prompt or answer: %s", data)
	}
}

func TestRuntimeTraceEvaluationRequiresAdminAndKnownUniqueTraces(t *testing.T) {
	a := New(nil).WithExperimentHistory(t.TempDir()).WithAccessPolicy(AccessPolicy{PublicMode: true, PublicToken: "public", AdminToken: "admin"})
	for _, tc := range []struct {
		name  string
		body  string
		token string
		want  int
	}{
		{name: "admin required", body: `{"trace_ids":["missing"]}`, token: "public", want: http.StatusForbidden},
		{name: "trace required", body: `{"trace_ids":["missing"]}`, token: "admin", want: http.StatusNotFound},
		{name: "duplicates rejected", body: `{"trace_ids":["same","same"]}`, token: "admin", want: http.StatusBadRequest},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/v1/evaluations", strings.NewReader(tc.body))
			if tc.token != "" {
				req.Header.Set("Authorization", "Bearer "+tc.token)
			}
			rec := httptest.NewRecorder()
			a.Handler().ServeHTTP(rec, req)
			if rec.Code != tc.want {
				t.Fatalf("status=%d want=%d body=%s", rec.Code, tc.want, rec.Body.String())
			}
		})
	}
	list := httptest.NewRequest(http.MethodGet, "/v1/evaluations", nil)
	list.Header.Set("Authorization", "Bearer public")
	listResponse := httptest.NewRecorder()
	a.Handler().ServeHTTP(listResponse, list)
	if listResponse.Code != http.StatusForbidden {
		t.Fatalf("public token enumerated trace batches: status=%d", listResponse.Code)
	}
}
