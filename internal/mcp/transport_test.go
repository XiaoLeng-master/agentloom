package mcp

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestHTTPTransportResponseValidation(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
		ok     bool
	}{
		{"valid", 200, `{"jsonrpc":"2.0","id":1,"result":{"tools":[]}}`, true},
		{"server-error", 500, `{"jsonrpc":"2.0","id":1,"result":{}}`, false},
		{"wrong-id", 200, `{"jsonrpc":"2.0","id":2,"result":{}}`, false},
		{"wrong-version", 200, `{"jsonrpc":"1.0","id":1,"result":{}}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(tc.status); fmt.Fprint(w, tc.body) }))
			defer server.Close()
			_, err := NewHTTPTransport(server.URL).List(context.Background())
			if (err == nil) != tc.ok {
				t.Fatalf("err=%v want success=%v", err, tc.ok)
			}
		})
	}
}

func TestHTTPTransportContextDeadline(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { time.Sleep(100 * time.Millisecond) }))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	_, err := NewHTTPTransport(server.URL).List(ctx)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err=%v", err)
	}
}

func TestStdioTransportCancellation(t *testing.T) {
	tr, err := NewStdioTransport("sleep", "10")
	if err != nil {
		t.Fatal(err)
	}
	defer tr.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err = tr.List(ctx)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err=%v", err)
	}
	if time.Since(start) > time.Second {
		t.Fatal("cancellation blocked")
	}
}

func TestReadOnlyClassificationDefaultsToWrite(t *testing.T) {
	transport := &mockTransport{tools: []ToolSchema{{Name: "change"}, {Name: "inspect", ReadOnly: true}}}
	change, err := Bind(context.Background(), transport, "change")
	if err != nil {
		t.Fatal(err)
	}
	inspect, err := Bind(context.Background(), transport, "inspect")
	if err != nil {
		t.Fatal(err)
	}
	if change.ReadOnly() || !inspect.ReadOnly() {
		t.Fatal("incorrect effect classification")
	}
}
