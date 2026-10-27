package core

import (
	"context"
	"encoding/json"
	"sync/atomic"
)

// ResultCache stores one completion for an identical message list.
// infra.PrefixCache implements it. A hit must not call the model.
type ResultCache interface {
	Get(prefix []byte) (string, bool)
	Set(prefix []byte, result string)
}

// CachedLLM returns a stored completion when the message list matches.
// The inner client is counted only on a miss. This is application-level
// reuse of a previous result, not a provider prompt cache.
type CachedLLM struct {
	Inner LLMClient
	Cache ResultCache
	calls atomic.Int64
}

// NewCachedLLM wraps inner. A nil cache calls inner every time.
func NewCachedLLM(inner LLMClient, cache ResultCache) *CachedLLM {
	return &CachedLLM{Inner: inner, Cache: cache}
}

// Calls is how many times Inner.Complete ran.
func (c *CachedLLM) Calls() int64 { return c.calls.Load() }

// Complete returns the cached message or calls Inner.
func (c *CachedLLM) Complete(ctx context.Context, msgs []Message) (Message, error) {
	key := messageKey(msgs)
	if c.Cache != nil {
		if raw, ok := c.Cache.Get(key); ok {
			var msg Message
			if err := json.Unmarshal([]byte(raw), &msg); err == nil {
				return msg, nil
			}
		}
	}
	c.calls.Add(1)
	msg, err := c.Inner.Complete(ctx, msgs)
	if err != nil || c.Cache == nil {
		return msg, err
	}
	if raw, err := json.Marshal(msg); err == nil {
		c.Cache.Set(key, string(raw))
	}
	return msg, nil
}

func messageKey(msgs []Message) []byte {
	raw, _ := json.Marshal(msgs)
	return raw
}
