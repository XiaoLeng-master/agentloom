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
			Messages []map[string]any `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if requests == 2 {
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
	client := DeepSeekClient{APIKey: "test-key", Endpoint: server.URL}
	first, err := client.Complete(context.Background(), []Message{{Role: RoleUser, Content: "question"}})
	if err != nil || len(first.ToolCalls) != 1 || first.ToolCalls[0].Name != "search_docs" {
		t.Fatalf("first=%+v err=%v", first, err)
	}
	second, err := client.Complete(context.Background(), []Message{{Role: RoleUser, Content: "question"}, first, {Role: RoleTool, Content: "result", ToolCallID: "call-1"}})
	if err != nil || second.Content != "done" {
		t.Fatalf("second=%+v err=%v", second, err)
	}
}
