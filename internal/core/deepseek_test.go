package core

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestDeepSeekEmptyModelDefaultsToFlash(t *testing.T) {
	var got string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Model string `json:"model"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		got = body.Model
		w.Write([]byte(`{"choices":[{"message":{"content":"ok"}}]}`))
	}))
	defer server.Close()
	client := DeepSeekClient{APIKey: "test-key", Endpoint: server.URL}
	msg, err := client.Complete(context.Background(), []Message{{Role: RoleUser, Content: "question"}})
	if err != nil || msg.Content != "ok" {
		t.Fatalf("msg=%+v err=%v", msg, err)
	}
	if got != DefaultModel || DefaultModel != "deepseek-flash" {
		t.Fatalf("empty model sent %q, want deepseek-flash", got)
	}
}

func TestDeepSeekToolRoundTrip(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.Header.Get("Authorization") != "Bearer test-key" {
			t.Error("missing API key")
		}
		var body struct {
			Messages   []map[string]any  `json:"messages"`
			ToolChoice string            `json:"tool_choice"`
			Model      string            `json:"model"`
			MaxTokens  int               `json:"max_tokens"`
			Thinking   map[string]string `json:"thinking"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if body.Model != "deepseek-flash" || body.MaxTokens != 1200 || body.Thinking["type"] != "disabled" {
			t.Errorf("model options: %+v", body)
		}
		if requests == 2 {
			if body.ToolChoice != "none" {
				t.Errorf("tool_choice = %q, want none", body.ToolChoice)
			}
			last := body.Messages[len(body.Messages)-1]
			if last["role"] != "tool" || last["tool_call_id"] != "call-1" {
				t.Errorf("tool message: %+v", last)
			}
			w.Write([]byte(`{"choices":[{"message":{"content":"done"}}]}`))
			return
		}
		w.Write([]byte(`{"choices":[{"message":{"content":null,"tool_calls":[{"id":"call-1","function":{"name":"search_docs","arguments":"{\"query\":\"agentloom\"}"}}]}}]}`))
	}))
	defer server.Close()
	client := DeepSeekClient{APIKey: "test-key", Model: "deepseek-flash", Endpoint: server.URL, MaxTokens: 1200, NoThinking: true, Tools: []map[string]any{{"type": "function", "function": map[string]any{"name": "search_docs"}}}}
	first, err := client.Complete(context.Background(), []Message{{Role: RoleUser, Content: "question"}})
	if err != nil || len(first.ToolCalls) != 1 || first.ToolCalls[0].Name != "search_docs" {
		t.Fatalf("first=%+v err=%v", first, err)
	}
	second, err := client.Complete(context.Background(), []Message{{Role: RoleUser, Content: "question"}, first, {Role: RoleTool, Content: "result", ToolCallID: "call-1"}})
	if err != nil || second.Content != "done" {
		t.Fatalf("second=%+v err=%v", second, err)
	}
}

func TestDeepSeekOnCallReportsUsageAndErrors(t *testing.T) {
	status := http.StatusOK
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if status != http.StatusOK {
			w.WriteHeader(status)
			w.Write([]byte(`{"error":{"message":"Model Not Exist"}}`))
			return
		}
		w.Write([]byte(`{"model":"deepseek-flash","usage":{"prompt_tokens":11,"completion_tokens":5,"total_tokens":16},"choices":[{"message":{"content":"ok"}}]}`))
	}))
	defer server.Close()
	var got []CallStats
	client := DeepSeekClient{APIKey: "test-key", Endpoint: server.URL, OnCall: func(s CallStats) { got = append(got, s) }}
	if _, err := client.Complete(context.Background(), []Message{{Role: RoleUser, Content: "q"}}); err != nil {
		t.Fatal(err)
	}
	status = http.StatusBadRequest
	_, err := client.Complete(context.Background(), []Message{{Role: RoleUser, Content: "q"}})
	if err == nil || !strings.Contains(err.Error(), "Model Not Exist") {
		t.Fatalf("error should carry API message, got %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("calls reported = %d", len(got))
	}
	if got[0].Status != 200 || got[0].TotalTokens != 16 || got[0].PromptTokens != 11 || got[0].CompletionTokens != 5 || got[0].ResponseModel != "deepseek-flash" || got[0].Err != "" {
		t.Fatalf("success stats: %+v", got[0])
	}
	if got[1].Status != 400 || got[1].Err == "" {
		t.Fatalf("error stats: %+v", got[1])
	}
	for _, s := range got {
		if strings.Contains(s.Err, "test-key") {
			t.Fatal("stats leaked API key")
		}
	}
}
