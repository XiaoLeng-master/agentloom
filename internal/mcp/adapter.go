package mcp

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/XiaoLeng-master/agentloom/internal/core"
)

// MCPTool adapts a tool exposed by an MCP Transport into the core.Tool
// interface. The Agent loop calls Name/Schema/Call and never learns whether
// the tool is built-in or remote-MCP-backed. Tool errors are surfaced as
// plain strings so the ReAct loop can feed them back as Observations.
type MCPTool struct {
	name      string
	schema    map[string]any
	readOnly  bool
	transport Transport
}

// Bind looks up name in t.List and returns an MCPTool bound to it.
func Bind(ctx context.Context, t Transport, name string) (*MCPTool, error) {
	tools, err := t.List(ctx)
	if err != nil {
		return nil, err
	}
	for _, td := range tools {
		if td.Name == name {
			return &MCPTool{name: td.Name, schema: td.InputSchema, readOnly: td.ReadOnly, transport: t}, nil
		}
	}
	return nil, fmt.Errorf("mcp: tool %q not advertised", name)
}

func (m *MCPTool) Name() string           { return m.name }
func (m *MCPTool) Schema() map[string]any { return m.schema }

// ReadOnly reports the connector owner's effect classification; unknown defaults to false.
func (m *MCPTool) ReadOnly() bool { return m.readOnly }
func (m *MCPTool) Call(ctx context.Context, args map[string]any) (string, error) {
	if err := validateArguments(m.schema, args); err != nil {
		return "", fmt.Errorf("mcp tool %s: %w", m.name, err)
	}
	r, err := m.transport.Call(ctx, m.name, args)
	if err != nil {
		return "", err
	}
	b, err := json.Marshal(r)
	if err != nil {
		return fmt.Sprintf("%v", r), nil
	}
	return string(b), nil
}

// compile-time assertion: MCPTool satisfies core.Tool.
var _ core.Tool = (*MCPTool)(nil)

// AdvertiseAll returns core.Tool adapters for every tool the Transport lists.
// An Agent factory uses it to expose a whole MCP server as a toolset.
func AdvertiseAll(ctx context.Context, t Transport) ([]core.Tool, error) {
	tools, err := t.List(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]core.Tool, 0, len(tools))
	for _, td := range tools {
		td := td
		out = append(out, &MCPTool{name: td.Name, schema: td.InputSchema, readOnly: td.ReadOnly, transport: t})
	}
	return out, nil
}
