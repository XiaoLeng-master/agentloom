package core

import "context"

// Tool is the unified contract for anything an Agent can call. Built-in tools
// and MCP adapters (D3) both implement this interface, so the Agent loop is
// agnostic to where a tool lives.
type Tool interface {
	// Name is the unique tool identifier the model uses in a ToolCall.
	Name() string
	// Schema describes the tool's parameters as a JSON schema; injected into the
	// model prompt so it knows how to call the tool.
	Schema() map[string]any
	// Call executes the tool. Errors are returned to the Agent loop, which feeds
	// them back to the model as an Observation rather than crashing the turn.
	Call(ctx context.Context, args map[string]any) (string, error)
}
