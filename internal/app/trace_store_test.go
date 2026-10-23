package app

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestRedisTraceStore(t *testing.T) {
	addr := os.Getenv("AGENTLOOM_TEST_REDIS_ADDR")
	if addr == "" {
		t.Skip("set AGENTLOOM_TEST_REDIS_ADDR for Redis integration")
	}
	store := NewRedisTraceStore(addr, time.Minute)
	defer store.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := store.Ping(ctx); err != nil {
		t.Fatal(err)
	}
	id := "integration-" + time.Now().UTC().Format("20060102150405.000000000")
	if _, err := store.Get(ctx, id); !errors.Is(err, ErrTraceNotFound) {
		t.Fatalf("missing trace error = %v", err)
	}
	want := Trace{ID: id, Query: "test", Answer: "ok", CreatedAt: time.Now().UTC().Truncate(time.Microsecond)}
	if err := store.Save(ctx, want); err != nil {
		t.Fatal(err)
	}
	got, err := store.Get(ctx, id)
	if err != nil || got.ID != want.ID || got.Query != want.Query || got.Answer != want.Answer {
		t.Fatalf("Get() = %+v, %v", got, err)
	}
}

func TestRedisTraceSurvivesAppRecreation(t *testing.T) {
	addr := os.Getenv("AGENTLOOM_TEST_REDIS_ADDR")
	if addr == "" {
		t.Skip("set AGENTLOOM_TEST_REDIS_ADDR for Redis integration")
	}
	store := NewRedisTraceStore(addr, time.Minute)
	defer store.Close()
	first := New(nil).WithTraceStore(store)
	req := httptest.NewRequest(http.MethodPost, "/v1/chat", strings.NewReader(`{"message":"hello"}`))
	req.Header.Set("Authorization", "Bearer local-demo")
	response := httptest.NewRecorder()
	first.Handler().ServeHTTP(response, req)
	if response.Code != http.StatusOK {
		t.Fatalf("chat status %d: %s", response.Code, response.Body.String())
	}
	var result struct {
		TraceID string `json:"trace_id"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil || result.TraceID == "" {
		t.Fatalf("invalid chat response: %s, %v", response.Body.String(), err)
	}
	first.Close()
	second := New(nil).WithTraceStore(store)
	defer second.Close()
	get := httptest.NewRequest(http.MethodGet, "/v1/traces/"+result.TraceID, nil)
	get.Header.Set("Authorization", "Bearer local-demo")
	response = httptest.NewRecorder()
	second.Handler().ServeHTTP(response, get)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"query":"hello"`) {
		t.Fatalf("trace status %d: %s", response.Code, response.Body.String())
	}
}

type blockedTraceStore struct {
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

func (s *blockedTraceStore) Save(_ context.Context, _ Trace) error {
	s.once.Do(func() { close(s.started) })
	<-s.release
	return nil
}
func (s *blockedTraceStore) Get(context.Context, string) (Trace, error) {
	return Trace{}, ErrTraceNotFound
}

func TestSlowTraceStoreDoesNotBlockChat(t *testing.T) {
	store := &blockedTraceStore{started: make(chan struct{}), release: make(chan struct{})}
	a := New(nil).WithTraceStore(store)
	req := httptest.NewRequest(http.MethodPost, "/v1/chat", strings.NewReader(`{"message":"hello"}`))
	req.Header.Set("Authorization", "Bearer local-demo")
	response := httptest.NewRecorder()
	done := make(chan struct{})
	go func() { a.Handler().ServeHTTP(response, req); close(done) }()
	select {
	case <-done:
		if response.Code != http.StatusOK {
			t.Fatalf("chat status %d", response.Code)
		}
	case <-time.After(time.Second):
		close(store.release)
		t.Fatal("chat waited for trace storage")
	}
	select {
	case <-store.started:
	case <-time.After(time.Second):
		t.Fatal("trace worker did not start")
	}
	close(store.release)
	a.Close()
}
