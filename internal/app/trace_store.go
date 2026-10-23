package app

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/redis/go-redis/v9"
)

var ErrTraceNotFound = errors.New("trace not found")

type TraceStore interface {
	Save(context.Context, Trace) error
	Get(context.Context, string) (Trace, error)
}

// RedisTraceStore persists demo traces across process restarts.
type RedisTraceStore struct {
	client *redis.Client
	ttl    time.Duration
}

func NewRedisTraceStore(addr string, ttl time.Duration) *RedisTraceStore {
	return &RedisTraceStore{client: redis.NewClient(&redis.Options{Addr: addr}), ttl: ttl}
}

func (s *RedisTraceStore) Ping(ctx context.Context) error { return s.client.Ping(ctx).Err() }
func (s *RedisTraceStore) Close() error                   { return s.client.Close() }

func (s *RedisTraceStore) Save(ctx context.Context, trace Trace) error {
	data, err := json.Marshal(trace)
	if err != nil {
		return err
	}
	return s.client.Set(ctx, "agentloom:trace:"+trace.ID, data, s.ttl).Err()
}

func (s *RedisTraceStore) Get(ctx context.Context, id string) (Trace, error) {
	var trace Trace
	data, err := s.client.Get(ctx, "agentloom:trace:"+id).Bytes()
	if errors.Is(err, redis.Nil) {
		return trace, ErrTraceNotFound
	}
	if err != nil {
		return trace, err
	}
	err = json.Unmarshal(data, &trace)
	return trace, err
}
