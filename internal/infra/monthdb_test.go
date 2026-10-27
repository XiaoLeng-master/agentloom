package infra

import (
	"context"
	"path/filepath"
	"testing"
)

func TestMonthDBIdempotentAndSplitByMonth(t *testing.T) {
	db, err := OpenMonthDB(filepath.Join(t.TempDir(), "traces.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	oct := []byte(`{"id":"t-oct","created_at":"2026-10-02T00:00:00Z","query":"q"}`)
	nov := []byte(`{"id":"t-nov","created_at":"2026-11-02T00:00:00Z","query":"q"}`)
	if err := db.SavePayload(ctx, oct); err != nil {
		t.Fatal(err)
	}
	if err := db.SavePayload(ctx, oct); err != nil {
		t.Fatal(err)
	}
	if err := db.SavePayload(ctx, nov); err != nil {
		t.Fatal(err)
	}
	if n, err := db.Count(ctx, "2026-10"); err != nil || n != 1 {
		t.Fatalf("october=%d err=%v", n, err)
	}
	if n, err := db.Count(ctx, "2026-11"); err != nil || n != 1 {
		t.Fatalf("november=%d err=%v", n, err)
	}
	if got := db.Sharder().Count(); got != 2 {
		t.Fatalf("sharder count=%d, duplicate must not be routed again", got)
	}
	months := db.Sharder().Months()
	if len(months) != 2 || months[0] != "2026-10" || months[1] != "2026-11" {
		t.Fatalf("months=%v", months)
	}
	if err := db.SavePayload(ctx, []byte("not-json")); err == nil {
		t.Fatal("invalid payload should fail")
	}
	if _, err := monthTable("2026/10"); err == nil {
		t.Fatal("bad month key accepted")
	}
}
