// Package mcp adapts Model Context Protocol servers (stdio or HTTP) into the
// core.Tool interface, so the Agent loop is agnostic to whether a tool is
// built-in or remote-MCP-backed. The transport layer also normalizes errors
// so tool failures become Observations for the model to self-correct, rather
// than crashing the ReAct loop.
package mcp

import (
	"context"
	"fmt"
)

func validateResponse(resp jsonRPCResponse, id int) error {
	if resp.JSONRPC != "2.0" || resp.ID != id {
		return fmt.Errorf("mcp: mismatched JSON-RPC response: version %q, id %d (want %d)", resp.JSONRPC, resp.ID, id)
	}
	return nil
}

// ToolSchema describes a tool advertised by an MCP server.
type ToolSchema struct {
	Name        string
	Description string
	InputSchema map[string]any
	// ReadOnly must be explicitly set by the connector owner before retry policy is considered.
	ReadOnly bool
}

// Transport is the MCP transport contract. stdio and HTTP both implement it.
// A production deployment may add SSE; the Agent only depends on this face.
type Transport interface {
	List(ctx context.Context) ([]ToolSchema, error)
	Call(ctx context.Context, name string, args map[string]any) (any, error)
	Close() error
}

// jsonRPCRequest/Response are the wire format shared by both transports.
type jsonRPCRequest struct {
	JSONRPC string `json:"jsonrpc"`
	ID      int    `json:"id"`
	Method  string `json:"method"`
	Params  any    `json:"params,omitempty"`
}

type jsonRPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type jsonRPCResponse struct {
	JSONRPC string        `json:"jsonrpc"`
	ID      int           `json:"id"`
	Result  any           `json:"result,omitempty"`
	Error   *jsonRPCError `json:"error,omitempty"`
}
