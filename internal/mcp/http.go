package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"
)

// HTTPTransport speaks JSON-RPC 2.0 over HTTP POST. Suitable for remote MCP
// servers; supports concurrent calls (each gets its own request/response).
type HTTPTransport struct {
	endpoint string
	client   *http.Client
	mu       sync.Mutex
	nextID   int
}

// NewHTTPTransport connects to an MCP HTTP endpoint.
func NewHTTPTransport(endpoint string) *HTTPTransport {
	return &HTTPTransport{endpoint: endpoint, client: &http.Client{Timeout: 30 * time.Second}}
}

func (t *HTTPTransport) call(ctx context.Context, method string, params any) (any, error) {
	t.mu.Lock()
	t.nextID++
	id := t.nextID
	t.mu.Unlock()
	req := jsonRPCRequest{JSONRPC: "2.0", ID: id, Method: method, Params: params}
	b, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("mcp http: encode: %w", err)
	}
	hr, err := http.NewRequestWithContext(ctx, http.MethodPost, t.endpoint, bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	hr.Header.Set("Content-Type", "application/json")
	resp, err := t.client.Do(hr)
	if err != nil {
		return nil, fmt.Errorf("mcp http: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("mcp http: status %d", resp.StatusCode)
	}
	var jr jsonRPCResponse
	if err := json.NewDecoder(resp.Body).Decode(&jr); err != nil {
		return nil, err
	}
	if err := validateResponse(jr, id); err != nil {
		return nil, err
	}
	if jr.Error != nil {
		return nil, fmt.Errorf("mcp http: rpc %d: %s", jr.Error.Code, jr.Error.Message)
	}
	return jr.Result, nil
}

// List discovers tools via tools/list.
func (t *HTTPTransport) List(ctx context.Context) ([]ToolSchema, error) {
	r, err := t.call(ctx, "tools/list", nil)
	if err != nil {
		return nil, err
	}
	return decodeTools(r)
}

// Call invokes a tool via tools/call.
func (t *HTTPTransport) Call(ctx context.Context, name string, args map[string]any) (any, error) {
	return t.call(ctx, "tools/call", map[string]any{"name": name, "arguments": args})
}

// Close is a no-op for HTTP.
func (t *HTTPTransport) Close() error { return nil }
