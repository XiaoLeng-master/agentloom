package infra

import (
	"context"
	"time"
)

// Deliver calls handle until it succeeds or attempts are exhausted. The last
// failure is handed to dlq. A nil dlq leaves that error with the caller so the
// message can be retried later. delay may be nil.
func Deliver(ctx context.Context, event []byte, attempts int, delay func(int) time.Duration, handle func(context.Context, []byte) error, dlq func(context.Context, []byte) error) (toDLQ bool, err error) {
	if attempts < 1 {
		attempts = 1
	}
	var last error
	for attempt := 1; attempt <= attempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return false, err
		}
		last = handle(ctx, event)
		if last == nil {
			return false, nil
		}
		if attempt == attempts || delay == nil {
			continue
		}
		timer := time.NewTimer(delay(attempt))
		select {
		case <-ctx.Done():
			timer.Stop()
			return false, ctx.Err()
		case <-timer.C:
		}
	}
	if dlq == nil {
		return false, last
	}
	if err := dlq(ctx, event); err != nil {
		return false, err
	}
	return true, nil
}
