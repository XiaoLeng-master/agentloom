package infra

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

// MonthDB stores trace payloads in one SQLite table per month. The table name
// comes from MonthKey. trace_id is the primary key, so a redelivered trace
// does not create a second row.
type MonthDB struct {
	db      *sql.DB
	sharder *MonthlySharder
}

// OpenMonthDB creates the file and an in-memory MonthlySharder that records
// only the first insert of each trace.
func OpenMonthDB(path string) (*MonthDB, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)")
	if err != nil {
		return nil, err
	}
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, err
	}
	db.SetMaxOpenConns(1)
	return &MonthDB{db: db, sharder: NewMonthlySharder()}, nil
}

// Sharder returns the in-memory month index kept beside the tables.
func (d *MonthDB) Sharder() *MonthlySharder { return d.sharder }

// Close closes the database.
func (d *MonthDB) Close() error { return d.db.Close() }

// SavePayload inserts one trace. A duplicate trace_id is a success and does
// not append another copy to the sharder.
func (d *MonthDB) SavePayload(ctx context.Context, payload []byte) error {
	var head struct {
		ID        string    `json:"id"`
		CreatedAt time.Time `json:"created_at"`
	}
	if err := json.Unmarshal(payload, &head); err != nil {
		return fmt.Errorf("trace payload: %w", err)
	}
	if head.ID == "" {
		return errors.New("trace id missing")
	}
	if head.CreatedAt.IsZero() {
		head.CreatedAt = time.Now().UTC()
	}
	table, err := monthTable(MonthKey(head.CreatedAt))
	if err != nil {
		return err
	}
	if _, err := d.db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS `+table+` (
		trace_id TEXT PRIMARY KEY,
		created_at TEXT NOT NULL,
		payload BLOB NOT NULL
	)`); err != nil {
		return err
	}
	res, err := d.db.ExecContext(ctx, `INSERT OR IGNORE INTO `+table+` (trace_id, created_at, payload) VALUES (?, ?, ?)`,
		head.ID, head.CreatedAt.UTC().Format(time.RFC3339Nano), payload)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 1 {
		d.sharder.Route(head.CreatedAt, append([]byte(nil), payload...))
	}
	return nil
}

// Count returns the number of rows in one month table. A missing table is zero.
func (d *MonthDB) Count(ctx context.Context, month string) (int, error) {
	table, err := monthTable(month)
	if err != nil {
		return 0, err
	}
	var name string
	err = d.db.QueryRowContext(ctx, `SELECT name FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&name)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	var n int
	if err := d.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM `+table).Scan(&n); err != nil {
		return 0, err
	}
	return n, nil
}

func monthTable(month string) (string, error) {
	if len(month) != 7 || month[4] != '-' || !digits(month[:4]) || !digits(month[5:]) {
		return "", fmt.Errorf("invalid month key %q", month)
	}
	return "traces_" + strings.ReplaceAll(month, "-", "_"), nil
}

func digits(s string) bool {
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return s != ""
}
