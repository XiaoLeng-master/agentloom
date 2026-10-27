//go:build integration

package config

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"
)

// TestEtcdWatchReloadsAndRollsBack talks to a real etcd.
// It runs only when AGENTLOOM_ETCD_ENDPOINTS is set, for example by `make etcd-integration`.
func TestEtcdWatchReloadsAndRollsBack(t *testing.T) {
	raw := strings.TrimSpace(os.Getenv("AGENTLOOM_ETCD_ENDPOINTS"))
	if raw == "" {
		t.Skip("AGENTLOOM_ETCD_ENDPOINTS is empty; skip live etcd")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	prefix := "/agentloom-it/" + t.Name() + "/"
	src, err := DialEtcd(ctx, EtcdOptions{Endpoints: strings.Split(raw, ","), Prefix: prefix, DialTimeout: 3 * time.Second, DrainTimeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer src.Close()
	m := NewManager()
	m.Register("kb-agent", Config{Name: "kb-agent", SystemPrompt: "memory", ChunkStrategy: "fixed"})
	if err := src.Sync(ctx, m); err != nil {
		t.Fatal(err)
	}
	watchCtx, watchCancel := context.WithCancel(ctx)
	defer watchCancel()
	watchErr := make(chan error, 1)
	go func() { watchErr <- src.Watch(watchCtx, m) }()

	_, err = src.client.Put(ctx, prefix+"kb-agent/config", `{"system_prompt":"etcd-stable","chunk_strategy":"recursive","prompt_mode":"echo","top_k":5}`)
	if err != nil {
		t.Fatal(err)
	}
	waitConfig(t, m, "etcd-stable", 2)
	_, err = src.client.Put(ctx, prefix+"kb-agent/canary", `{"percent":10,"system_prompt":"etcd-canary","chunk_strategy":"fixed"}`)
	if err != nil {
		t.Fatal(err)
	}
	tenant, ok := TenantForBucket(0)
	if !ok {
		t.Fatal("missing canary tenant")
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		handle, err := m.AcquireTenant("kb-agent", tenant)
		if err != nil {
			t.Fatal(err)
		}
		prompt := handle.Config().SystemPrompt
		version := handle.Config().Version
		handle.Release()
		if prompt == "etcd-canary" && version == 3 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("canary not visible: prompt=%s version=%d", prompt, version)
		}
		time.Sleep(20 * time.Millisecond)
	}
	if _, err := src.client.Delete(ctx, prefix+"kb-agent/canary"); err != nil {
		t.Fatal(err)
	}
	deadline = time.Now().Add(5 * time.Second)
	for {
		if _, ok := m.CanaryPercent("kb-agent"); !ok {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("canary delete did not roll back")
		}
		time.Sleep(20 * time.Millisecond)
	}
	watchCancel()
	select {
	case err := <-watchErr:
		if err != nil && ctx.Err() == nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
	}
}

func waitConfig(t *testing.T, m *Manager, prompt string, version int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		handle, err := m.Acquire("kb-agent")
		if err != nil {
			t.Fatal(err)
		}
		gotPrompt := handle.Config().SystemPrompt
		gotVersion := handle.Config().Version
		handle.Release()
		if gotPrompt == prompt && gotVersion == version {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("config prompt=%s version=%d", gotPrompt, gotVersion)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
