package core

import (
	"context"
	"errors"
	"testing"
)

// mockLLM returns scripted responses in order. Once the script is exhausted it
// keeps returning the last response, so a looping model can be modeled cheaply.
type mockLLM struct {
	resps []Message
	calls int
}

func (m *mockLLM) Complete(ctx context.Context, _ []Message) (Message, error) {
	if len(m.resps) == 0 {
		return Message{Role: RoleAssistant, Content: ""}, nil
	}
	r := m.resps[min(m.calls, len(m.resps)-1)]
	m.calls++
	return r, nil
}

// echoTool returns args["msg"] verbatim, or "no msg" when absent/wrong-typed.
type echoTool struct{}

func (echoTool) Name() string { return "echo" }
func (echoTool) Schema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"msg": map[string]any{"type": "string"},
		},
	}
}
func (echoTool) Call(_ context.Context, args map[string]any) (string, error) {
	if v, ok := args["msg"]; ok {
		if s, ok := v.(string); ok {
			return s, nil
		}
	}
	return "no msg", nil
}

func TestAgent_ReActOneToolCall(t *testing.T) {
	llm := &mockLLM{resps: []Message{
		{Role: RoleAssistant, ToolCalls: []ToolCall{{ID: "c1", Name: "echo", Args: map[string]any{"msg": "hi"}}}},
		{Role: RoleAssistant, Content: "echo said: hi"},
	}}
	a := NewAgent("t", "you are helpful", llm, []Tool{echoTool{}}, 0)
	got, err := a.Run(context.Background(), "say hi via echo")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got != "echo said: hi" {
		t.Fatalf("got %q, want %q", got, "echo said: hi")
	}
	if llm.calls != 2 {
		t.Fatalf("llm called %d times, want 2 (think + final)", llm.calls)
	}
}

func TestAgent_MaxIterations(t *testing.T) {
	// model always asks for a tool and never terminates
	loopLLM := &mockLLM{resps: []Message{
		{Role: RoleAssistant, ToolCalls: []ToolCall{{ID: "c1", Name: "echo", Args: map[string]any{"msg": "loop"}}}},
	}}
	a := NewAgent("t", "", loopLLM, []Tool{echoTool{}}, 3)
	_, err := a.Run(context.Background(), "loop")
	if !errors.Is(err, ErrMaxIterations) {
		t.Fatalf("want ErrMaxIterations, got %v", err)
	}
}

func TestAgent_UnknownToolFedBack(t *testing.T) {
	// model calls a nonexistent tool first, then recovers with a final answer
	llm := &mockLLM{resps: []Message{
		{Role: RoleAssistant, ToolCalls: []ToolCall{{ID: "c1", Name: "ghost", Args: nil}}},
		{Role: RoleAssistant, Content: "recovered"},
	}}
	a := NewAgent("t", "", llm, []Tool{echoTool{}}, 0)
	got, err := a.Run(context.Background(), "x")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got != "recovered" {
		t.Fatalf("got %q, want %q", got, "recovered")
	}
}

func TestAgent_ContextCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // pre-cancel so the loop must bail before touching the LLM
	llm := &mockLLM{resps: []Message{{Role: RoleAssistant, Content: "x"}}}
	a := NewAgent("t", "", llm, nil, 0)
	_, err := a.Run(ctx, "x")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("want context.Canceled, got %v", err)
	}
}

func TestRegistry_CreateAndUnknown(t *testing.T) {
	r := NewRegistry()
	r.Register("echo-agent", func(cfg AgentConfig) (*Agent, error) {
		return NewAgent(
			cfg.Name, cfg.SystemPrompt,
			&mockLLM{resps: []Message{{Role: RoleAssistant, Content: "ok"}}},
			cfg.Tools, cfg.MaxIter,
		), nil
	})
	a, err := r.Create("echo-agent", AgentConfig{Name: "e"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	got, err := a.Run(context.Background(), "hi")
	if err != nil || got != "ok" {
		t.Fatalf("got %q err %v", got, err)
	}
	if _, err := r.Create("missing", AgentConfig{}); err == nil {
		t.Fatalf("want error for unknown agent name")
	}
}
