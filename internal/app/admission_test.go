package app

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/XiaoLeng-master/agentloom/internal/core"
	"github.com/XiaoLeng-master/agentloom/internal/orchestrator"
	"github.com/XiaoLeng-master/agentloom/internal/quota"
	"github.com/XiaoLeng-master/agentloom/internal/rag"
)

type holdPlanner struct {
	inflight *atomic.Int32
	release  <-chan struct{}
}

func (h *holdPlanner) Complete(ctx context.Context, msgs []core.Message) (core.Message, error) {
	h.inflight.Add(1)
	defer h.inflight.Add(-1)
	select {
	case <-h.release:
	case <-ctx.Done():
		return core.Message{}, ctx.Err()
	}
	user := ""
	for _, msg := range msgs {
		if msg.Role == core.RoleUser {
			user = msg.Content
			break
		}
	}
	body, _ := json.Marshal([]orchestrator.SubTask{{ID: "t1", Input: user}})
	return core.Message{Role: core.RoleAssistant, Content: string(body)}, nil
}

func chatRequest(ctx context.Context, message, remote string) *http.Request {
	req := httptest.NewRequest(http.MethodPost, "/v1/chat", strings.NewReader(`{"message":"`+message+`"}`))
	if ctx != nil {
		req = req.WithContext(ctx)
	}
	req.RemoteAddr = remote
	return req
}

func waitInflight(t *testing.T, n *atomic.Int32, want int32) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if n.Load() >= want {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("planner inflight=%d, want at least %d", n.Load(), want)
}

func TestChatAdmissionQueuesAndRejects(t *testing.T) {
	release := make(chan struct{})
	var inflight atomic.Int32
	application := NewWithModels([]rag.Document{{Source: "guide", Text: "planner executor verifier"}}, map[string]core.LLMClient{
		"planner": &holdPlanner{inflight: &inflight, release: release},
	}).WithChatQuota(1, 1)
	handler := application.Handler()

	first := make(chan int, 1)
	go func() {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, chatRequest(context.Background(), "hold", "203.0.113.1:1000"))
		first <- rec.Code
	}()
	waitInflight(t, &inflight, 1)

	queued := make(chan int, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, chatRequest(ctx, "queued", "203.0.113.1:1001"))
		queued <- rec.Code
	}()
	time.Sleep(40 * time.Millisecond)
	select {
	case code := <-queued:
		t.Fatalf("queued chat returned %d before a slot was free", code)
	default:
	}
	if got := inflight.Load(); got != 1 {
		t.Fatalf("planner inflight=%d, the second request should still be waiting on the global slot", got)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	rejected := httptest.NewRecorder()
	handler.ServeHTTP(rejected, chatRequest(ctx, "late", "203.0.113.1:1002"))
	if rejected.Code != http.StatusTooManyRequests {
		t.Fatalf("status=%d body=%s", rejected.Code, rejected.Body.String())
	}
	if !strings.Contains(rejected.Body.String(), "concurrency") {
		t.Fatalf("body=%s", rejected.Body.String())
	}
	if application.ChatAdmissionStats().Rejected < 1 {
		t.Fatalf("rejected=%d", application.ChatAdmissionStats().Rejected)
	}
	if !quotaRejected(application.ChatAdmissionStats()) {
		t.Fatalf("stats=%+v", application.ChatAdmissionStats())
	}

	close(release)
	if code := <-first; code != http.StatusOK {
		t.Fatalf("holder status=%d", code)
	}
	if code := <-queued; code != http.StatusOK {
		t.Fatalf("queued status=%d", code)
	}
}

func quotaRejected(stats quota.Stats) bool { return stats.Rejected >= 1 }

func TestChatAdmissionPerSource(t *testing.T) {
	release := make(chan struct{})
	var inflight atomic.Int32
	application := NewWithModels(nil, map[string]core.LLMClient{
		"planner": &holdPlanner{inflight: &inflight, release: release},
	}).WithChatQuota(2, 1)
	handler := application.Handler()

	start := func(remote string) <-chan int {
		done := make(chan int, 1)
		go func() {
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, chatRequest(context.Background(), "hold", remote))
			done <- rec.Code
		}()
		return done
	}
	first := start("203.0.113.10:1")
	second := start("203.0.113.20:1")
	waitInflight(t, &inflight, 2)
	time.Sleep(30 * time.Millisecond)
	if got := inflight.Load(); got != 2 {
		t.Fatalf("two sources should each hold one slot, inflight=%d", got)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, chatRequest(ctx, "again", "203.0.113.10:2"))
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("same source should wait and then time out, status=%d body=%s", rec.Code, rec.Body.String())
	}
	if inflight.Load() != 2 {
		t.Fatalf("timed-out source entered the planner, inflight=%d", inflight.Load())
	}
	close(release)
	if code := <-first; code != http.StatusOK {
		t.Fatalf("source A status=%d", code)
	}
	if code := <-second; code != http.StatusOK {
		t.Fatalf("source B status=%d", code)
	}
}

func TestPublicTokenDenialDoesNotTakeChatSlot(t *testing.T) {
	release := make(chan struct{})
	var inflight atomic.Int32
	application := NewWithModels(nil, map[string]core.LLMClient{
		"planner": &holdPlanner{inflight: &inflight, release: release},
	}).WithAccessPolicy(AccessPolicy{PublicMode: true, PublicToken: "public", AdminToken: "admin"}).WithChatQuota(1, 1)
	handler := application.Handler()

	denied := httptest.NewRecorder()
	req := chatRequest(context.Background(), "nope", "203.0.113.30:1")
	handler.ServeHTTP(denied, req)
	if denied.Code != http.StatusForbidden {
		t.Fatalf("status=%d", denied.Code)
	}

	done := make(chan int, 1)
	go func() {
		rec := httptest.NewRecorder()
		ok := chatRequest(context.Background(), "yes", "203.0.113.30:2")
		ok.Header.Set("Authorization", "Bearer public")
		handler.ServeHTTP(rec, ok)
		done <- rec.Code
	}()
	waitInflight(t, &inflight, 1)
	close(release)
	if code := <-done; code != http.StatusOK {
		t.Fatalf("authorized chat status=%d", code)
	}
}
