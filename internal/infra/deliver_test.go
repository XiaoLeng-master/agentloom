package infra

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestDeliverRetriesThenDLQ(t *testing.T) {
	calls := 0
	dlq := 0
	toDLQ, err := Deliver(context.Background(), []byte("bad"), 3, func(int) time.Duration { return 0 }, func(context.Context, []byte) error {
		calls++
		return errors.New("store down")
	}, func(context.Context, []byte) error {
		dlq++
		return nil
	})
	if err != nil || !toDLQ || calls != 3 || dlq != 1 {
		t.Fatalf("toDLQ=%v err=%v calls=%d dlq=%d", toDLQ, err, calls, dlq)
	}
}

func TestDeliverStopsOnSuccess(t *testing.T) {
	calls := 0
	toDLQ, err := Deliver(context.Background(), []byte("ok"), 3, nil, func(context.Context, []byte) error {
		calls++
		if calls < 2 {
			return errors.New("temporary")
		}
		return nil
	}, func(context.Context, []byte) error {
		t.Fatal("dlq")
		return nil
	})
	if err != nil || toDLQ || calls != 2 {
		t.Fatalf("toDLQ=%v err=%v calls=%d", toDLQ, err, calls)
	}
}

func TestDeliverKeepsErrorWhenDLQFails(t *testing.T) {
	_, err := Deliver(context.Background(), []byte("bad"), 1, nil, func(context.Context, []byte) error {
		return errors.New("store down")
	}, func(context.Context, []byte) error {
		return errors.New("dlq down")
	})
	if err == nil || err.Error() != "dlq down" {
		t.Fatalf("err=%v", err)
	}
}
