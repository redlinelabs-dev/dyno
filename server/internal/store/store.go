// Package store keeps results in SQLite: one file in WAL mode on a local
// volume (ADR-0002). The driver is modernc.org/sqlite, pure Go, so the binary
// stays static (ADR-0006).
package store

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"

	"github.com/redlinelabs-dev/dyno/server/internal/result"
)

// ErrNotFound is returned for a result or schedule id that doesn't exist.
var ErrNotFound = errors.New("not found")

type Store struct {
	db *sql.DB
}

// Open opens or creates dyno.db in dir and brings its schema up to date.
func Open(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	// A 1 MiB page cache keeps memory flat; the working set is a few rows.
	dsn := "file:" + filepath.Join(dir, "dyno.db") +
		"?_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)&_pragma=busy_timeout(5000)" +
		"&_pragma=cache_size(-1024)&_pragma=temp_store(MEMORY)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	// One connection: writes are rare and tiny, and every extra connection
	// carries its own page cache.
	db.SetMaxOpenConns(1)
	if err := migrate(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrating %s: %w", dir, err)
	}
	return &Store{db: db}, nil
}

func (s *Store) Close() error { return s.db.Close() }

// migrations[i] takes the schema from user_version i to i+1. Append only.
var migrations = []string{
	`CREATE TABLE results (
		id                INTEGER PRIMARY KEY,
		at                INTEGER NOT NULL, -- unix milliseconds, UTC
		source            TEXT NOT NULL,
		client            TEXT NOT NULL,
		test_id           TEXT NOT NULL,
		download_mbps     REAL NOT NULL,
		upload_mbps       REAL NOT NULL,
		idle_ms           REAL NOT NULL,
		loaded_down_ms    REAL NOT NULL,
		loaded_up_ms      REAL NOT NULL,
		jitter_ms         REAL NOT NULL,
		streams           INTEGER NOT NULL,
		client_limited    INTEGER NOT NULL,
		bufferbloat_ms    REAL NOT NULL,
		grade             TEXT NOT NULL,
		rpm               INTEGER NOT NULL,
		server_bytes_down INTEGER NOT NULL,
		server_bytes_up   INTEGER NOT NULL,
		detail            TEXT -- JSON samples for the charts
	);
	CREATE INDEX results_at ON results(at);`,
	// 2: the device a result came from, for History's "This device" (ADR-0022).
	// Results from before it have none.
	`ALTER TABLE results ADD COLUMN device TEXT NOT NULL DEFAULT '';
	CREATE INDEX results_device ON results(device, id);`,
	// 3: scheduled tests (ADR-0028, ADR-0029). AUTOINCREMENT, so a deleted
	// schedule's id, which its results keep, never names a new schedule.
	`CREATE TABLE schedules (
		id             INTEGER PRIMARY KEY AUTOINCREMENT,
		name           TEXT NOT NULL,
		far_end        TEXT NOT NULL,
		kind           TEXT NOT NULL,
		every_minutes  INTEGER NOT NULL,
		offset_minutes INTEGER NOT NULL,
		paused         INTEGER NOT NULL,
		created_at     INTEGER NOT NULL, -- unix milliseconds, UTC
		updated_at     INTEGER NOT NULL
	);
	ALTER TABLE results ADD COLUMN schedule_id INTEGER NOT NULL DEFAULT 0;
	ALTER TABLE results ADD COLUMN far_end TEXT NOT NULL DEFAULT '';
	ALTER TABLE results ADD COLUMN path_changed INTEGER NOT NULL DEFAULT 0;
	ALTER TABLE results ADD COLUMN failure_code TEXT NOT NULL DEFAULT '';
	ALTER TABLE results ADD COLUMN failure_phase TEXT NOT NULL DEFAULT '';
	ALTER TABLE results ADD COLUMN failure_message TEXT NOT NULL DEFAULT '';
	CREATE INDEX results_schedule ON results(schedule_id, id);`,
	// 4: the server's settings (ADR-0030), one row each, the value as JSON.
	`CREATE TABLE settings (key TEXT PRIMARY KEY, value TEXT NOT NULL);`,
}

func migrate(db *sql.DB) error {
	var v int
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&v); err != nil {
		return err
	}
	for ; v < len(migrations); v++ {
		tx, err := db.Begin()
		if err != nil {
			return err
		}
		if _, err := tx.Exec(migrations[v]); err != nil {
			tx.Rollback()
			return err
		}
		if _, err := tx.Exec(fmt.Sprintf(`PRAGMA user_version = %d`, v+1)); err != nil {
			tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	return nil
}

// columns are in scanResult order; insertColumns is the same minus id.
const insertColumns = `at, source, client, device, test_id, download_mbps, upload_mbps, idle_ms,
	loaded_down_ms, loaded_up_ms, jitter_ms, streams, client_limited, bufferbloat_ms, grade, rpm,
	server_bytes_down, server_bytes_up, schedule_id, far_end, path_changed,
	failure_code, failure_phase, failure_message`

const columns = `id, ` + insertColumns

// Add stores r and returns it with its id.
func (s *Store) Add(r result.Result) (result.Result, error) {
	var detail any
	if len(r.Detail) > 0 {
		detail = string(r.Detail)
	}
	var fail result.Failure
	if r.Failure != nil {
		fail = *r.Failure
	}
	res, err := s.db.Exec(`INSERT INTO results (`+insertColumns+`, detail)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		r.At.UnixMilli(), r.Source, r.Client, r.Device, r.TestID, r.DownloadMbps, r.UploadMbps, r.IdleMs,
		r.LoadedDownMs, r.LoadedUpMs, r.JitterMs, r.Streams, r.ClientLimited, r.BufferbloatMs, r.Grade, r.RPM,
		r.ServerBytesDown, r.ServerBytesUp, r.ScheduleID, r.FarEnd, r.PathChanged,
		fail.Code, fail.Phase, fail.Message, detail)
	if err != nil {
		return r, err
	}
	r.ID, err = res.LastInsertId()
	return r, err
}

// List returns up to limit results older than id before (0 for the newest),
// newest first, without detail. A device other than "" keeps only its results.
func (s *Store) List(limit int, before int64, device string) ([]result.Result, error) {
	if before <= 0 {
		before = 1<<63 - 1
	}
	q, args := `SELECT `+columns+` FROM results WHERE id < ?`, []any{before}
	if device != "" {
		q, args = q+` AND device = ?`, append(args, device)
	}
	rows, err := s.db.Query(q+` ORDER BY id DESC LIMIT ?`, append(args, limit)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []result.Result{}
	for rows.Next() {
		r, err := scanResult(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// Get returns one result with its detail.
func (s *Store) Get(id int64) (result.Result, error) {
	var detail sql.NullString
	r, err := scanResult(s.db.QueryRow(`SELECT `+columns+`, detail FROM results WHERE id = ?`, id), &detail)
	if errors.Is(err, sql.ErrNoRows) {
		return r, ErrNotFound
	}
	if detail.Valid {
		r.Detail = []byte(detail.String)
	}
	return r, err
}

// rowScanner is a *sql.Row or *sql.Rows.
type rowScanner interface{ Scan(dest ...any) error }

// scanResult reads a row that selected columns, then extra, if any.
func scanResult(rs rowScanner, extra ...any) (result.Result, error) {
	var r result.Result
	var fail result.Failure
	err := rs.Scan(append([]any{&r.ID, (*unixMillis)(&r.At), &r.Source, &r.Client, &r.Device, &r.TestID,
		&r.DownloadMbps, &r.UploadMbps, &r.IdleMs, &r.LoadedDownMs, &r.LoadedUpMs, &r.JitterMs, &r.Streams,
		&r.ClientLimited, &r.BufferbloatMs, &r.Grade, &r.RPM, &r.ServerBytesDown, &r.ServerBytesUp,
		&r.ScheduleID, &r.FarEnd, &r.PathChanged, &fail.Code, &fail.Phase, &fail.Message}, extra...)...)
	if err == nil && fail.Code != "" {
		r.Failure = &fail
	}
	return r, err
}

// unixMillis scans an INTEGER column of unix milliseconds into a time.Time.
type unixMillis time.Time

func (t *unixMillis) Scan(v any) error {
	ms, ok := v.(int64)
	if !ok {
		return fmt.Errorf("at: want int64, got %T", v)
	}
	*t = unixMillis(time.UnixMilli(ms).UTC())
	return nil
}
