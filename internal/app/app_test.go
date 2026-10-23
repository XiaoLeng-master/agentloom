package app

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/XiaoLeng-master/agentloom/internal/rag"
)

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
	if trace.ID != reply.TraceID || len(trace.Contracts) != 3 {
		t.Fatalf("trace: %+v", trace)
	}
}

func TestChatRequiresDemoToken(t *testing.T) {
	handler := New(nil).Handler()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat", strings.NewReader(`{"message":"hello"}`))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != 403 {
		t.Fatalf("status=%d", rec.Code)
	}
}
