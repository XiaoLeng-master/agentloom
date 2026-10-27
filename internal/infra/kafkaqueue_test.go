package infra

import "testing"

func TestSplitBrokersAndDefaults(t *testing.T) {
	if got := SplitBrokers(""); len(got) != 0 {
		t.Fatalf("empty=%v", got)
	}
	got := SplitBrokers(" localhost:9092, ,127.0.0.1:9092 ")
	if len(got) != 2 || got[0] != "localhost:9092" || got[1] != "127.0.0.1:9092" {
		t.Fatalf("brokers=%v", got)
	}
	cfg := ApplyKafkaDefaults(KafkaConfig{})
	if cfg.Topic != defaultTraceTopic || cfg.DLQTopic != defaultDLQTopic || cfg.GroupID != defaultGroup || cfg.Attempts != 3 || cfg.PushBuffer != 1024 {
		t.Fatalf("defaults=%+v", cfg)
	}
}

func TestTraceKey(t *testing.T) {
	if got := traceKey([]byte(`{"id":"abc"}`)); got != "abc" {
		t.Fatalf("key=%s", got)
	}
	if got := traceKey([]byte("not-json")); got != "unknown" {
		t.Fatalf("key=%s", got)
	}
}
