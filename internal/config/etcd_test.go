package config

import (
	"testing"
	"time"
)

func TestOptionsFromEnvFallsBackWhenUnset(t *testing.T) {
	opts, ok, err := OptionsFromEnv(func(string) string { return "" })
	if err != nil || ok || len(opts.Endpoints) != 0 {
		t.Fatalf("opts=%+v ok=%v err=%v", opts, ok, err)
	}
	opts, ok, err = OptionsFromEnv(func(key string) string {
		switch key {
		case "AGENTLOOM_ETCD_ENDPOINTS":
			return "127.0.0.1:2379, 127.0.0.1:2389"
		case "AGENTLOOM_ETCD_PREFIX":
			return "/demo"
		default:
			return ""
		}
	})
	if err != nil || !ok || len(opts.Endpoints) != 2 || opts.Endpoints[0] != "127.0.0.1:2379" || opts.Prefix != "/demo" {
		t.Fatalf("opts=%+v ok=%v err=%v", opts, ok, err)
	}
	if _, _, err := OptionsFromEnv(func(key string) string {
		if key == "AGENTLOOM_ETCD_ENDPOINTS" {
			return ","
		}
		return ""
	}); err == nil {
		t.Fatal("comma-only endpoints were accepted")
	}
}

func TestEtcdDocumentsDriveReloadCanaryAndRollback(t *testing.T) {
	m := NewManager()
	m.Register("kb-agent", Config{Name: "kb-agent", SystemPrompt: "memory", Model: "qwen2.5", MaxIter: 10, ChunkStrategy: "fixed"})
	prefix := defaultEtcdPrefix
	held, err := m.Acquire("kb-agent")
	if err != nil {
		t.Fatal(err)
	}
	defer held.Release()
	stable := []byte(`{"system_prompt":"etcd-stable","model":"qwen2.5","max_iter":10,"chunk_strategy":"recursive","prompt_mode":"echo","top_k":5}`)
	applied, err := ApplyEtcdKV(m, prefix, prefix+"kb-agent/config", stable, false, 20*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	if applied.Kind != "config" || applied.Version != 2 || applied.Draining != 1 || applied.DrainLeft == 0 {
		t.Fatalf("stable apply=%+v", applied)
	}
	if held.Config().Version != 1 || held.Config().SystemPrompt != "memory" || held.Config().ChunkStrategy != "fixed" {
		t.Fatalf("in-flight changed: %+v", held.Config())
	}
	next, err := m.Acquire("kb-agent")
	if err != nil {
		t.Fatal(err)
	}
	if next.Config().Version != 2 || next.Config().SystemPrompt != "etcd-stable" || next.Config().ChunkStrategy != "recursive" || next.Config().TopK != 5 || next.Config().PromptMode != "echo" {
		t.Fatalf("reloaded=%+v", next.Config())
	}
	next.Release()
	held.Release()
	if left := m.Drain("kb-agent", time.Second); left != 0 {
		t.Fatalf("drain left=%d", left)
	}

	canaryDoc := []byte(`{"percent":10,"system_prompt":"etcd-canary","model":"qwen2.5","max_iter":10,"chunk_strategy":"fixed","prompt_mode":"echo","top_k":3}`)
	applied, err = ApplyEtcdKV(m, prefix, prefix+"kb-agent/canary", canaryDoc, false, time.Second)
	if err != nil || applied.Kind != "canary" || applied.Percent != 10 || applied.Version != 3 {
		t.Fatalf("canary=%+v err=%v", applied, err)
	}
	canaryTenant, ok := TenantForBucket(0)
	if !ok {
		t.Fatal("missing canary tenant")
	}
	stableTenant, ok := TenantForBucket(99)
	if !ok {
		t.Fatal("missing stable tenant")
	}
	inside, err := m.AcquireTenant("kb-agent", canaryTenant)
	if err != nil {
		t.Fatal(err)
	}
	if inside.Config().Version != 3 || inside.Config().SystemPrompt != "etcd-canary" || inside.Config().ChunkStrategy != "fixed" {
		t.Fatalf("canary tenant=%+v", inside.Config())
	}
	outside, err := m.AcquireTenant("kb-agent", stableTenant)
	if err != nil {
		t.Fatal(err)
	}
	if outside.Config().Version != 2 || outside.Config().SystemPrompt != "etcd-stable" {
		t.Fatalf("stable tenant entered canary: %+v", outside.Config())
	}
	outside.Release()
	plain, err := m.Acquire("kb-agent")
	if err != nil {
		t.Fatal(err)
	}
	if plain.Config().Version != 2 {
		t.Fatalf("plain acquire left stable: %+v", plain.Config())
	}
	plain.Release()

	wider := []byte(`{"percent":50,"system_prompt":"etcd-canary","model":"qwen2.5","max_iter":10,"chunk_strategy":"fixed","prompt_mode":"echo","top_k":3}`)
	applied, err = ApplyEtcdKV(m, prefix, prefix+"kb-agent/canary", wider, false, time.Second)
	if err != nil || applied.Percent != 50 || applied.Version != 3 {
		t.Fatalf("widen=%+v err=%v", applied, err)
	}
	midTenant, ok := TenantForBucket(40)
	if !ok {
		t.Fatal("missing mid tenant")
	}
	if canaryTenant != "tenant-79" || stableTenant != "tenant-74" || midTenant != "tenant-8" {
		t.Fatalf("demo tenants changed: canary=%s stable=%s mid=%s", canaryTenant, stableTenant, midTenant)
	}
	mid, err := m.AcquireTenant("kb-agent", midTenant)
	if err != nil {
		t.Fatal(err)
	}
	if mid.Config().Version != 3 {
		t.Fatalf("widened tenant stayed stable: %+v", mid.Config())
	}
	mid.Release()
	still, err := m.AcquireTenant("kb-agent", canaryTenant)
	if err != nil {
		t.Fatal(err)
	}
	if still.Config().Version != 3 || still.Config().SystemPrompt != "etcd-canary" {
		t.Fatalf("widening dropped the original tenant: %+v", still.Config())
	}
	still.Release()

	applied, err = ApplyEtcdKV(m, prefix, prefix+"kb-agent/canary", nil, true, 20*time.Millisecond)
	if err != nil || applied.Kind != "rollback" || applied.DrainLeft == 0 {
		t.Fatalf("rollback=%+v err=%v", applied, err)
	}
	if inside.Config().Version != 3 || inside.Config().SystemPrompt != "etcd-canary" {
		t.Fatalf("in-flight canary changed: %+v", inside.Config())
	}
	if _, ok := m.CanaryPercent("kb-agent"); ok {
		t.Fatal("canary remained after delete")
	}
	after, err := m.AcquireTenant("kb-agent", canaryTenant)
	if err != nil {
		t.Fatal(err)
	}
	if after.Config().Version != 2 || after.Config().SystemPrompt != "etcd-stable" {
		t.Fatalf("new acquire after rollback=%+v", after.Config())
	}
	after.Release()
	inside.Release()

	full := []byte(`{"percent":100,"system_prompt":"etcd-canary","model":"qwen2.5","max_iter":10,"chunk_strategy":"fixed","prompt_mode":"echo","top_k":3}`)
	applied, err = ApplyEtcdKV(m, prefix, prefix+"kb-agent/canary", full, false, time.Second)
	if err != nil || applied.Kind != "promote" || applied.Percent != 100 || applied.Version != 3 {
		t.Fatalf("promote=%+v err=%v", applied, err)
	}
	if _, ok := m.CanaryPercent("kb-agent"); ok {
		t.Fatal("canary slot remained after 100 percent")
	}
	active, err := m.Acquire("kb-agent")
	if err != nil {
		t.Fatal(err)
	}
	defer active.Release()
	if active.Config().Version != 3 || active.Config().SystemPrompt != "etcd-canary" {
		t.Fatalf("promoted active=%+v", active.Config())
	}
}

func TestEtcdRollbackDocumentAndBadValue(t *testing.T) {
	m := NewManager()
	m.Register("kb-agent", Config{Name: "kb-agent", SystemPrompt: "memory"})
	prefix := defaultEtcdPrefix
	if _, err := ApplyEtcdKV(m, prefix, prefix+"kb-agent/config", []byte(`{"system_prompt":"etcd-stable"}`), false, time.Second); err != nil {
		t.Fatal(err)
	}
	if _, err := ApplyEtcdKV(m, prefix, prefix+"kb-agent/canary", []byte(`{"percent":10,"system_prompt":"etcd-canary"}`), false, time.Second); err != nil {
		t.Fatal(err)
	}
	applied, err := ApplyEtcdKV(m, prefix, prefix+"kb-agent/canary", []byte(`{"rollback":true}`), false, time.Second)
	if err != nil || applied.Kind != "rollback" {
		t.Fatalf("rollback doc=%+v err=%v", applied, err)
	}
	if _, ok := m.CanaryPercent("kb-agent"); ok {
		t.Fatal("rollback document left the canary active")
	}
	before, err := m.Acquire("kb-agent")
	if err != nil {
		t.Fatal(err)
	}
	version := before.Config().Version
	before.Release()
	if _, err := ApplyEtcdKV(m, prefix, prefix+"kb-agent/config", []byte("{"), false, time.Second); err == nil {
		t.Fatal("bad json was accepted")
	}
	if _, err := ApplyEtcdKV(m, prefix, prefix+"kb-agent/config", nil, true, time.Second); err != nil {
		t.Fatal(err)
	}
	after, err := m.Acquire("kb-agent")
	if err != nil {
		t.Fatal(err)
	}
	defer after.Release()
	if after.Config().Version != version || after.Config().SystemPrompt != "etcd-stable" {
		t.Fatalf("stable changed after bad or deleted config: %+v", after.Config())
	}
	if _, err := ApplyEtcdKV(m, prefix, prefix+"other", []byte(`{"system_prompt":"x"}`), false, time.Second); err != nil {
		t.Fatal(err)
	}
}
