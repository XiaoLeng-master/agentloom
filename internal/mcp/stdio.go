package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"sync"
)

// StdioTransport speaks JSON-RPC 2.0 over a subprocess's stdin/stdout (the
// MCP stdio transport). MCP stdio is synchronous per message, so concurrent
// Callers are serialized by a mutex. Reading line-delimited JSON avoids the
// classic stdio framing pitfall (half-read messages on a shared pipe).
type StdioTransport struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout *bufio.Reader
	mu     sync.Mutex
	nextID int
}

// NewStdioTransport spawns serverCmd and connects to its stdio pipes.
func NewStdioTransport(serverCmd string, args ...string) (*StdioTransport, error) {
	c := exec.Command(serverCmd, args...)
	in, err := c.StdinPipe()
	if err != nil {
		return nil, err
	}
	out, err := c.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := c.Start(); err != nil {
		return nil, fmt.Errorf("mcp stdio: start %s: %w", serverCmd, err)
	}
	return &StdioTransport{cmd: c, stdin: in, stdout: bufio.NewReader(out)}, nil
}

func (t *StdioTransport) roundTrip(_ context.Context, method string, params any) (any, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.nextID++
	req := jsonRPCRequest{JSONRPC: "2.0", ID: t.nextID, Method: method, Params: params}
	b, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	if _, err := t.stdin.Write(append(b, '\n')); err != nil {
		return nil, fmt.Errorf("mcp stdio: write: %w", err)
	}
	line, err := t.stdout.ReadBytes('\n')
	if err != nil {
		return nil, fmt.Errorf("mcp stdio: read: %w", err)
	}
	var resp jsonRPCResponse
	if err := json.Unmarshal(line, &resp); err != nil {
		return nil, fmt.Errorf("mcp stdio: decode: %w", err)
	}
	if resp.Error != nil {
		return nil, fmt.Errorf("mcp stdio: rpc %d: %s", resp.Error.Code, resp.Error.Message)
	}
	return resp.Result, nil
}

// List discovers tools via tools/list.
func (t *StdioTransport) List(ctx context.Context) ([]ToolSchema, error) {
	r, err := t.roundTrip(ctx, "tools/list", nil)
	if err != nil {
		return nil, err
	}
	return decodeTools(r)
}

// Call invokes a tool via tools/call.
func (t *StdioTransport) Call(ctx context.Context, name string, args map[string]any) (any, error) {
	return t.roundTrip(ctx, "tools/call", map[string]any{"name": name, "arguments": args})
}

// Close terminates the subprocess.
func (t *StdioTransport) Close() error {
	_ = t.stdin.Close()
	if t.cmd.Process != nil {
		_ = t.cmd.Process.Kill()
	}
	return t.cmd.Wait()
}

func decodeTools(r any) ([]ToolSchema, error) {
	raw, err := json.Marshal(r)
	if err != nil {
		return nil, err
	}
	var fr struct {
		Tools []ToolSchema `json:"tools"`
	}
	if err := json.Unmarshal(raw, &fr); err != nil {
		return nil, err
	}
	return fr.Tools, nil
}
