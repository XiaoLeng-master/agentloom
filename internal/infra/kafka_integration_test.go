//go:build integration

package infra

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestKafkaIdempotentMonthlyAndDLQ(t *testing.T) {
	if _, err := exec.LookPath("docker"); err != nil && os.Getenv("AGENTLOOM_KAFKA_BROKERS") == "" {
		t.Skip("docker not available and AGENTLOOM_KAFKA_BROKERS unset")
	}
	brokers := SplitBrokers(os.Getenv("AGENTLOOM_KAFKA_BROKERS"))
	if len(brokers) == 0 {
		t.Skip("AGENTLOOM_KAFKA_BROKERS is unset; not running against a Kafka broker")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	db, err := OpenMonthDB(filepath.Join(t.TempDir(), "traces.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	topic := "agentloom.it." + time.Now().UTC().Format("20060102150405")
	q, err := NewKafkaQueue(KafkaConfig{
		Brokers:  brokers,
		Topic:    topic,
		DLQTopic: topic + ".dlq",
		GroupID:  topic,
		Attempts: 2,
		DB:       db,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer q.Close()
	runCtx, stop := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		_ = q.Run(runCtx)
		close(done)
	}()
	payload := []byte(`{"id":"same","created_at":"2026-10-03T00:00:00Z","query":"q"}`)
	other := []byte(`{"id":"other","created_at":"2026-12-03T00:00:00Z","query":"q"}`)
	for _, body := range [][]byte{payload, payload, other, []byte("not-json")} {
		if err := Publish(ctx, brokers, topic, []byte(traceKey(body)), body); err != nil {
			t.Fatal(err)
		}
	}
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		st := q.Stats()
		if st.Consumed >= 4 && st.DLQ >= 1 && st.Stored == 2 {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	st := q.Stats()
	if st.DLQ < 1 || st.Stored != 2 {
		t.Fatalf("stats=%+v", st)
	}
	if n, err := db.Count(ctx, "2026-10"); err != nil || n != 1 {
		t.Fatalf("october=%d err=%v", n, err)
	}
	if n, err := db.Count(ctx, "2026-12"); err != nil || n != 1 {
		t.Fatalf("december=%d err=%v", n, err)
	}
	stop()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("consumer did not stop")
	}
}
