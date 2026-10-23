package mcp

import (
	"context"
	"testing"
)

type mockTransport struct {
	tools []ToolSchema
}

func (m *mockTransport) List(ctx context.Context) ([]ToolSchema, error) {
	return m.tools, nil
}
func (m *mockTransport) Call(ctx context.Context, name string, args map[string]any) (any, error) {
	return map[string]any{"tool": name, "args": args}, nil
}
func (m *mockTransport) Close() error { return nil }

func TestMCPTool_BindAndCall(t *testing.T) {
	mt := &mockTransport{tools: []ToolSchema{
		{Name: "echo", InputSchema: map[string]any{"type": "object"}},
	}}
	tool, err := Bind(context.Background(), mt, "echo")
	if err != nil {
		t.Fatalf("Bind: %v", err)
	}
	if tool.Name() != "echo" {
		t.Fatalf("name = %q, want echo", tool.Name())
	}
	got, err := tool.Call(context.Background(), map[string]any{"msg": "hi"})
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	if got == "" {
		t.Fatal("Call returned empty observation")
	}
}

func TestMCPTool_BindUnknownTool(t *testing.T) {
	mt := &mockTransport{tools: []ToolSchema{{Name: "a"}}}
	if _, err := Bind(context.Background(), mt, "ghost"); err == nil {
		t.Fatal("Bind unknown tool should error")
	}
}

func TestAdvertiseAll(t *testing.T) {
	mt := &mockTransport{tools: []ToolSchema{{Name: "a"}, {Name: "b"}}}
	tools, err := AdvertiseAll(context.Background(), mt)
	if err != nil {
		t.Fatalf("AdvertiseAll: %v", err)
	}
	if len(tools) != 2 {
		t.Fatalf("want 2 tools, got %d", len(tools))
	}
}

func TestMCPToolRejectsInvalidArgumentsBeforeTransport(t *testing.T) {
	mt := &countingTransport{mockTransport: mockTransport{tools: []ToolSchema{{Name: "lookup", InputSchema: map[string]any{"type": "object", "required": []any{"query"}, "properties": map[string]any{"query": map[string]any{"type": "string"}}, "additionalProperties": false}}}}}
	tool, err := Bind(context.Background(), mt, "lookup")
	if err != nil {
		t.Fatal(err)
	}
	for _, args := range []map[string]any{{}, {"query": 7}, {"query": "ok", "extra": true}} {
		if _, err := tool.Call(context.Background(), args); err == nil {
			t.Fatalf("accepted %v", args)
		}
	}
	if mt.calls != 0 {
		t.Fatalf("transport called %d times", mt.calls)
	}
	if _, err := tool.Call(context.Background(), map[string]any{"query": "ok"}); err != nil {
		t.Fatal(err)
	}
	if mt.calls != 1 {
		t.Fatalf("transport called %d times", mt.calls)
	}
}

type countingTransport struct {
	mockTransport
	calls int
}

func (m *countingTransport) Call(ctx context.Context, name string, args map[string]any) (any, error) {
	m.calls++
	return m.mockTransport.Call(ctx, name, args)
}
