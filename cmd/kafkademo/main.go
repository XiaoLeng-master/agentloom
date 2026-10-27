// Command kafkademo publishes a burst of traces to the optional Kafka topic
// and prints the counters agentd exposes. It does not invent measurements:
// every number in the JSON is read back from this run.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/XiaoLeng-master/agentloom/internal/infra"
)

func main() {
	url := flag.String("url", "http://127.0.0.1:8080", "agentd base URL")
	n := flag.Int("n", 200, "trace records to publish")
	replay := flag.Bool("replay", false, "republish the dead-letter topic and exit")
	flag.Parse()

	brokers := infra.SplitBrokers(os.Getenv("AGENTLOOM_KAFKA_BROKERS"))
	if len(brokers) == 0 {
		fmt.Fprintln(os.Stderr, "AGENTLOOM_KAFKA_BROKERS is required")
		os.Exit(2)
	}
	topic := os.Getenv("AGENTLOOM_KAFKA_TOPIC")
	if topic == "" {
		topic = "agentloom.traces"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	if *replay {
		cfg := infra.KafkaConfig{Brokers: brokers, Topic: topic, DLQTopic: os.Getenv("AGENTLOOM_KAFKA_DLQ_TOPIC")}
		count, err := infra.ReplayDLQ(ctx, cfg, 0)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		fmt.Printf("{\"replayed\":%d}\n", count)
		return
	}

	if err := waitReady(ctx, *url); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	started := time.Now()
	var peak peakStats
	for i := 0; i < *n; i++ {
		month := "2026-10-04T00:00:00Z"
		if i%20 == 0 {
			month = "2026-11-04T00:00:00Z"
		}
		body := fmt.Sprintf(`{"id":"demo-%d","created_at":"%s","query":"burst","answer":"ok"}`, i, month)
		if err := postTrace(ctx, *url, body); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		if st, err := fetchStats(ctx, *url); err == nil {
			peak.observe(st)
		}
	}
	final, err := waitDrained(ctx, *url, int64(*n), &peak)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	beforeDLQ := final.DLQ
	if err := postTrace(ctx, *url, "not-json"); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	final, err = waitDLQ(ctx, *url, beforeDLQ, &peak)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	wall := time.Since(started).Milliseconds()
	report := map[string]any{
		"published_by_demo":    *n,
		"poison_payloads":      1,
		"max_buffered":         peak.buffered,
		"max_reader_lag":       peak.readerLag,
		"max_backlog_estimate": peak.backlog,
		"final":                final,
		"wall_ms":              wall,
		"brokers":              brokers,
		"topic":                topic,
	}
	if wall > 0 {
		report["consumed_per_sec"] = float64(final.Consumed) / (float64(wall) / 1000)
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(report); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func waitReady(ctx context.Context, base string) error {
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/health", nil)
		if err != nil {
			return err
		}
		res, err := http.DefaultClient.Do(req)
		if err == nil {
			res.Body.Close()
			if res.StatusCode == 200 {
				st, err := fetchStats(ctx, base)
				if err == nil && st.Enabled {
					return nil
				}
			}
		}
		time.Sleep(200 * time.Millisecond)
	}
	return fmt.Errorf("agentd at %s did not report an enabled kafka pipeline", base)
}

type peakStats struct {
	buffered  int
	readerLag int64
	backlog   int64
}

func (p *peakStats) observe(st infra.PipelineStats) {
	if st.Buffered > p.buffered {
		p.buffered = st.Buffered
	}
	if st.ReaderLag > p.readerLag {
		p.readerLag = st.ReaderLag
	}
	if st.BacklogEstimate > p.backlog {
		p.backlog = st.BacklogEstimate
	}
}

func postTrace(ctx context.Context, base, body string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/v1/trace-pipeline", strings.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusAccepted {
		buf := new(bytes.Buffer)
		_, _ = buf.ReadFrom(res.Body)
		return fmt.Errorf("enqueue %d: %s", res.StatusCode, buf.String())
	}
	return nil
}

func waitDrained(ctx context.Context, base string, want int64, peak *peakStats) (infra.PipelineStats, error) {
	deadline := time.Now().Add(45 * time.Second)
	var last infra.PipelineStats
	for time.Now().Before(deadline) {
		st, err := fetchStats(ctx, base)
		if err != nil {
			return st, err
		}
		last = st
		peak.observe(st)
		if st.Consumed >= want && st.BacklogEstimate == 0 && st.Buffered == 0 {
			return st, nil
		}
		time.Sleep(50 * time.Millisecond)
	}
	return last, fmt.Errorf("timed out waiting for %d consumed messages: %+v", want, last)
}

func waitDLQ(ctx context.Context, base string, before int64, peak *peakStats) (infra.PipelineStats, error) {
	deadline := time.Now().Add(20 * time.Second)
	var last infra.PipelineStats
	for time.Now().Before(deadline) {
		st, err := fetchStats(ctx, base)
		if err != nil {
			return st, err
		}
		last = st
		peak.observe(st)
		if st.DLQ > before {
			return st, nil
		}
		time.Sleep(50 * time.Millisecond)
	}
	return last, fmt.Errorf("timed out waiting for dead-letter count to pass %d: %+v", before, last)
}

func fetchStats(ctx context.Context, base string) (infra.PipelineStats, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/v1/trace-pipeline", nil)
	if err != nil {
		return infra.PipelineStats{}, err
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return infra.PipelineStats{}, err
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		buf := new(bytes.Buffer)
		_, _ = buf.ReadFrom(res.Body)
		return infra.PipelineStats{}, fmt.Errorf("trace pipeline %d: %s", res.StatusCode, buf.String())
	}
	var st infra.PipelineStats
	if err := json.NewDecoder(res.Body).Decode(&st); err != nil {
		return infra.PipelineStats{}, err
	}
	return st, nil
}
