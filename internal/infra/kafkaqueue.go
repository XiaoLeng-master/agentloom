package infra

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/segmentio/kafka-go"
)

const (
	defaultTraceTopic = "agentloom.traces"
	defaultDLQTopic   = "agentloom.traces.dlq"
	defaultGroup      = "agentloom-traces"
)

// KafkaConfig selects the optional broker path. An empty Brokers list means
// the process should keep using ToyQueue.
type KafkaConfig struct {
	Brokers    []string
	Topic      string
	DLQTopic   string
	GroupID    string
	Attempts   int
	PushBuffer int
	DB         *MonthDB
}

// PipelineStats is a point-in-time view of one process. ReaderLag comes from
// the Kafka client. BacklogEstimate is published minus messages this process
// has finished (including those sent to the dead-letter topic).
type PipelineStats struct {
	Backend         string   `json:"backend"`
	Enabled         bool     `json:"enabled"`
	Published       int64    `json:"published"`
	Dropped         int64    `json:"dropped"`
	Consumed        int64    `json:"consumed"`
	DLQ             int64    `json:"dlq"`
	Buffered        int      `json:"buffered"`
	ReaderLag       int64    `json:"reader_lag"`
	BacklogEstimate int64    `json:"backlog_estimate"`
	Months          []string `json:"months,omitempty"`
	Stored          int      `json:"stored"`
}

// KafkaQueue publishes traces to a topic and consumes them on a background
// call to Run. Push does not wait for the consumer. A full local buffer is
// counted as a drop, same as ToyQueue.
type KafkaQueue struct {
	cfg       KafkaConfig
	local     chan kafka.Message
	mu        sync.RWMutex
	closed    atomic.Bool
	writer    *kafka.Writer
	dlq       *kafka.Writer
	reader    *kafka.Reader
	pubWG     sync.WaitGroup
	closeOnce sync.Once

	published   atomic.Int64
	dropped     atomic.Int64
	consumed    atomic.Int64
	dlqCount    atomic.Int64
	handleDelay time.Duration
}

// SetHandleDelay waits before each stored write so a local burst can observe
// consumer lag. Zero keeps the handler immediate.
func (q *KafkaQueue) SetHandleDelay(d time.Duration) { q.handleDelay = d }

// ApplyKafkaDefaults fills empty topic, group, attempt, and buffer fields.
func ApplyKafkaDefaults(cfg KafkaConfig) KafkaConfig {
	if cfg.Topic == "" {
		cfg.Topic = defaultTraceTopic
	}
	if cfg.DLQTopic == "" {
		cfg.DLQTopic = defaultDLQTopic
	}
	if cfg.GroupID == "" {
		cfg.GroupID = defaultGroup
	}
	if cfg.Attempts < 1 {
		cfg.Attempts = 3
	}
	if cfg.PushBuffer < 1 {
		cfg.PushBuffer = 1024
	}
	return cfg
}

// NewKafkaQueue ensures the topics exist and starts the publisher goroutine.
func NewKafkaQueue(cfg KafkaConfig) (*KafkaQueue, error) {
	if len(cfg.Brokers) == 0 {
		return nil, errors.New("kafka brokers required")
	}
	cfg = ApplyKafkaDefaults(cfg)
	if cfg.DB == nil {
		return nil, errors.New("month database required")
	}
	if err := ensureTopics(cfg.Brokers, cfg.Topic, cfg.DLQTopic); err != nil {
		return nil, err
	}
	q := &KafkaQueue{
		cfg:   cfg,
		local: make(chan kafka.Message, cfg.PushBuffer),
		writer: &kafka.Writer{
			Addr:                   kafka.TCP(cfg.Brokers...),
			Topic:                  cfg.Topic,
			Balancer:               &kafka.LeastBytes{},
			RequiredAcks:           kafka.RequireOne,
			AllowAutoTopicCreation: true,
			BatchTimeout:           5 * time.Millisecond,
		},
		dlq: &kafka.Writer{
			Addr:                   kafka.TCP(cfg.Brokers...),
			Topic:                  cfg.DLQTopic,
			Balancer:               &kafka.LeastBytes{},
			RequiredAcks:           kafka.RequireOne,
			AllowAutoTopicCreation: true,
			BatchTimeout:           5 * time.Millisecond,
		},
		reader: kafka.NewReader(kafka.ReaderConfig{
			Brokers:        cfg.Brokers,
			GroupID:        cfg.GroupID,
			Topic:          cfg.Topic,
			MinBytes:       1,
			MaxBytes:       1 << 20,
			MaxWait:        200 * time.Millisecond,
			StartOffset:    kafka.FirstOffset,
			CommitInterval: 0,
		}),
	}
	q.pubWG.Add(1)
	go q.publish()
	return q, nil
}

// Push enqueues one event without waiting for a consumer. The buffer is copied.
func (q *KafkaQueue) Push(event []byte) error {
	msg := kafka.Message{Key: []byte(traceKey(event)), Value: append([]byte(nil), event...)}
	q.mu.RLock()
	defer q.mu.RUnlock()
	if q.closed.Load() {
		return ErrQueueClosed
	}
	select {
	case q.local <- msg:
		return nil
	default:
		q.dropped.Add(1)
		return ErrQueueFull
	}
}

// Run reads the topic until ctx ends. A successful handler or a successful
// dead-letter publish commits the offset. A failed dead-letter publish leaves
// the offset uncommitted.
func (q *KafkaQueue) Run(ctx context.Context) error {
	for {
		msg, err := q.reader.FetchMessage(ctx)
		if err != nil {
			return err
		}
		toDLQ, err := Deliver(ctx, msg.Value, q.cfg.Attempts, func(attempt int) time.Duration {
			return time.Duration(attempt) * 20 * time.Millisecond
		}, q.save, func(ctx context.Context, event []byte) error {
			wctx, cancel := context.WithTimeout(ctx, 2*time.Second)
			defer cancel()
			return q.dlq.WriteMessages(wctx, kafka.Message{Key: []byte(traceKey(event)), Value: event})
		})
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			timer := time.NewTimer(100 * time.Millisecond)
			select {
			case <-ctx.Done():
				timer.Stop()
				return ctx.Err()
			case <-timer.C:
			}
			continue
		}
		if err := q.reader.CommitMessages(ctx, msg); err != nil {
			return err
		}
		q.consumed.Add(1)
		if toDLQ {
			q.dlqCount.Add(1)
		}
	}
}

// Stats returns counters for this process. It does not describe other consumers.
func (q *KafkaQueue) Stats() PipelineStats {
	published := q.published.Load()
	consumed := q.consumed.Load()
	lag := int64(0)
	if q.reader != nil {
		lag = q.reader.Stats().Lag
	}
	backlog := published - consumed
	if backlog < 0 {
		backlog = 0
	}
	st := PipelineStats{
		Backend:         "kafka",
		Enabled:         true,
		Published:       published,
		Dropped:         q.dropped.Load(),
		Consumed:        consumed,
		DLQ:             q.dlqCount.Load(),
		Buffered:        len(q.local),
		ReaderLag:       lag,
		BacklogEstimate: backlog,
		Stored:          q.cfg.DB.Sharder().Count(),
		Months:          q.cfg.DB.Sharder().Months(),
	}
	return st
}

// Close stops the publisher and the client. Run should already be cancelled.
func (q *KafkaQueue) Close() error {
	q.closeOnce.Do(func() {
		q.mu.Lock()
		q.closed.Store(true)
		close(q.local)
		q.mu.Unlock()
		q.pubWG.Wait()
		_ = q.writer.Close()
		_ = q.dlq.Close()
		_ = q.reader.Close()
	})
	return nil
}

func (q *KafkaQueue) save(ctx context.Context, event []byte) error {
	if q.handleDelay > 0 {
		timer := time.NewTimer(q.handleDelay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
	return q.cfg.DB.SavePayload(ctx, event)
}

func (q *KafkaQueue) publish() {
	defer q.pubWG.Done()
	for msg := range q.local {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		err := q.writer.WriteMessages(ctx, msg)
		cancel()
		if err != nil {
			q.dropped.Add(1)
			continue
		}
		q.published.Add(1)
	}
}

// Publish writes one record and waits for the broker ack. The demo uses it
// to put a burst, and a poison payload, onto the same topic agentd consumes.
func Publish(ctx context.Context, brokers []string, topic string, key, value []byte) error {
	w := &kafka.Writer{
		Addr:                   kafka.TCP(brokers...),
		Topic:                  topic,
		RequiredAcks:           kafka.RequireOne,
		AllowAutoTopicCreation: true,
		BatchTimeout:           5 * time.Millisecond,
	}
	defer w.Close()
	return w.WriteMessages(ctx, kafka.Message{Key: key, Value: value})
}

// ReplayDLQ copies up to limit records from the dead-letter topic back onto
// the main topic. limit <= 0 reads until the topic stays idle for two seconds.
// Redelivery is safe for rows that already have a trace_id.
func ReplayDLQ(ctx context.Context, cfg KafkaConfig, limit int) (int, error) {
	cfg = ApplyKafkaDefaults(cfg)
	reader := kafka.NewReader(kafka.ReaderConfig{
		Brokers:     cfg.Brokers,
		Topic:       cfg.DLQTopic,
		Partition:   0,
		MaxBytes:    1 << 20,
		MaxWait:     500 * time.Millisecond,
		StartOffset: kafka.FirstOffset,
	})
	defer reader.Close()
	writer := &kafka.Writer{
		Addr:                   kafka.TCP(cfg.Brokers...),
		Topic:                  cfg.Topic,
		RequiredAcks:           kafka.RequireOne,
		AllowAutoTopicCreation: true,
	}
	defer writer.Close()
	n := 0
	for limit <= 0 || n < limit {
		readCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		msg, err := reader.ReadMessage(readCtx)
		cancel()
		if err != nil {
			if n > 0 && errors.Is(err, context.DeadlineExceeded) {
				return n, nil
			}
			if errors.Is(err, context.DeadlineExceeded) {
				return 0, nil
			}
			return n, err
		}
		if err := writer.WriteMessages(ctx, kafka.Message{Key: msg.Key, Value: msg.Value}); err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}

func ensureTopics(brokers []string, topics ...string) error {
	conn, err := kafka.Dial("tcp", brokers[0])
	if err != nil {
		return err
	}
	defer conn.Close()
	controller, err := conn.Controller()
	if err != nil {
		return err
	}
	ctrl, err := kafka.Dial("tcp", net.JoinHostPort(controller.Host, strconv.Itoa(controller.Port)))
	if err != nil {
		return err
	}
	defer ctrl.Close()
	var specs []kafka.TopicConfig
	for _, topic := range topics {
		specs = append(specs, kafka.TopicConfig{Topic: topic, NumPartitions: 1, ReplicationFactor: 1})
	}
	if err := ctrl.CreateTopics(specs...); err != nil && !strings.Contains(strings.ToLower(err.Error()), "already exists") {
		return err
	}
	return nil
}

func traceKey(event []byte) string {
	var head struct {
		ID string `json:"id"`
	}
	if json.Unmarshal(event, &head) == nil && head.ID != "" {
		return head.ID
	}
	return "unknown"
}

// SplitBrokers parses AGENTLOOM_KAFKA_BROKERS. An empty result means Kafka is off.
func SplitBrokers(raw string) []string {
	var out []string
	for _, part := range strings.Split(raw, ",") {
		part = strings.TrimSpace(part)
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}
