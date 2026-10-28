package core

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

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

func TestDeepSeekDefaultModel(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Model string `json:"model"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body.Model != "deepseek-flash" {
			t.Errorf("default model = %q", body.Model)
		}
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"ok"}}]}`))
	}))
	defer server.Close()
	_, err := (DeepSeekClient{APIKey: "test-key", Endpoint: server.URL}).Complete(context.Background(), []Message{{Role: RoleUser, Content: "hello"}})
	if err != nil {
		t.Fatal(err)
	}
}
