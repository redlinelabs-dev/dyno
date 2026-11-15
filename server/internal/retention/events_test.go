package retention_test

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/redlinelabs-dev/dyno/server/internal/retention"
	"github.com/redlinelabs-dev/dyno/server/internal/store"
	_ "modernc.org/sqlite"
)

// These tests are the hourly half of H23: what Run logs when the database
// refuses its prune (ADR-0030). The half after a PATCH of keep_days is in
// package api.

// evMakeTables makes the store's tables in dir, which a trigger needs, and
// closes the store again.
func evMakeTables(t *testing.T, dir string) {
	t.Helper()
	st, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
}

// evRefuse makes the store's file in dir abort every statement of the given
// kinds ("INSERT", "UPDATE" or "DELETE") on a table, through triggers made on
// a second connection that is closed again. Reads and every other write still
// work.
func evRefuse(t *testing.T, dir, table string, kinds ...string) {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(dir, "dyno.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if _, err := db.Exec("PRAGMA busy_timeout = 5000"); err != nil {
		t.Fatal(err)
	}
	for _, kind := range kinds {
		ddl := fmt.Sprintf("CREATE TRIGGER ev_refuse_%s_%s BEFORE %s ON %s BEGIN SELECT RAISE(ABORT, 'refused by the test'); END",
			strings.ToLower(kind), table, kind, table)
		if _, err := db.Exec(ddl); err != nil {
			t.Fatalf("%s: %v", ddl, err)
		}
	}
}

// evStart runs Run over st, in a synctest bubble, and returns where it logs
// and a func that stops it and wants it to have returned.
func evStart(t *testing.T, st *store.Store) (*histLog, func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	logs := &histLog{}
	done := make(chan struct{})
	go func() {
		defer close(done)
		retention.Run(ctx, st, slog.New(slog.NewJSONHandler(logs, nil)))
	}()
	return logs, func() {
		cancel()
		synctest.Wait()
		select {
		case <-done:
		default:
			t.Error("Run did not return once ctx was done")
		}
	}
}

// evCheckPruneFailed wants a results.prune event for a prune the database
// refused: at ERROR, failed, with the given error.code and the error's text,
// which holds text.
func evCheckPruneFailed(t *testing.T, label string, ev map[string]any, code, text string) {
	t.Helper()
	if ev["msg"] != "results.prune" {
		t.Errorf("%s: msg = %v, want results.prune", label, ev["msg"])
	}
	if ev["level"] != "ERROR" {
		t.Errorf("%s: level = %v, want ERROR: the database refused it", label, ev["level"])
	}
	if ev["result"] != "failed" {
		t.Errorf("%s: result = %v, want failed", label, ev["result"])
	}
	if ev["error.code"] != code {
		t.Errorf("%s: error.code = %v, want %s", label, ev["error.code"], code)
	}
	if got, _ := ev["error.message"].(string); got == "" || !strings.Contains(got, text) {
		t.Errorf("%s: error.message = %q, want the error's text, holding %q", label, got, text)
	}
	histCheckFlat(t, ev)
}

func TestH23_HourlyPruneThatCantReadKeepDaysLogsStoreReadFailed(t *testing.T) {
	dir := t.TempDir()
	synctest.Test(t, func(t *testing.T) {
		st, err := store.Open(dir)
		if err != nil {
			t.Fatal(err)
		}
		// A closed store refuses every read, keep_days included.
		if err := st.Close(); err != nil {
			t.Fatal(err)
		}
		logs, stop := evStart(t, st)
		defer stop()

		synctest.Wait()
		evs := logs.events(t)
		if len(evs) != 1 {
			t.Fatalf("the first prune logged %d lines, want 1: %v", len(evs), evs)
		}
		evCheckPruneFailed(t, "the first prune", evs[0], "store_read_failed", "")

		// A failure doesn't stop it: an hour later it reads keep_days again.
		time.Sleep(retention.Every + time.Minute)
		synctest.Wait()
		evs = logs.events(t)
		if len(evs) != 2 {
			t.Fatalf("after an hour there are %d log lines, want 2: %v", len(evs), evs)
		}
		evCheckPruneFailed(t, "the hourly prune", evs[1], "store_read_failed", "")
	})
}

func TestH23_HourlyPruneWhoseDeleteFailsLogsStoreDeleteFailed(t *testing.T) {
	dir := t.TempDir()
	// A trigger needs the tables, so they are made first.
	evMakeTables(t, dir)
	evRefuse(t, dir, "results", "DELETE")
	synctest.Test(t, func(t *testing.T) {
		st, err := store.Open(dir)
		if err != nil {
			t.Fatal(err)
		}
		defer st.Close()
		histKeep(t, st, 7)
		t0 := time.Now()
		old := histAddAges(t, st, t0, 8*histDay, 30*histDay)
		fresh := histAddAges(t, st, t0, time.Hour, 0)
		logs, stop := evStart(t, st)
		defer stop()

		synctest.Wait()
		evs := logs.events(t)
		if len(evs) != 1 {
			t.Fatalf("the first prune logged %d lines, want 1: %v", len(evs), evs)
		}
		evCheckPruneFailed(t, "the first prune", evs[0], "store_delete_failed", "refused by the test")
		histExpect(t, st, "after a refused delete", nil, slices.Concat(old, fresh))

		// A failure doesn't stop it: an hour later it tries again.
		time.Sleep(retention.Every + time.Minute)
		synctest.Wait()
		evs = logs.events(t)
		if len(evs) != 2 {
			t.Fatalf("after an hour there are %d log lines, want 2: %v", len(evs), evs)
		}
		evCheckPruneFailed(t, "the hourly prune", evs[1], "store_delete_failed", "refused by the test")
		histExpect(t, st, "after the second refused delete", nil, slices.Concat(old, fresh))
	})
}

// The prune that deleted something logs the keys ADR-0030 names, as the one a
// PATCH of keep_days does.
func TestH23_HourlyPruneThatDeletesCarriesDeletedResultsAndKeepDays(t *testing.T) {
	dir := t.TempDir()
	synctest.Test(t, func(t *testing.T) {
		st, err := store.Open(dir)
		if err != nil {
			t.Fatal(err)
		}
		defer st.Close()
		histKeep(t, st, 7)
		t0 := time.Now()
		histAddAges(t, st, t0, 8*histDay, 9*histDay, 10*histDay)
		histAddAges(t, st, t0, histDay)
		logs, stop := evStart(t, st)
		defer stop()

		synctest.Wait()
		evs := logs.events(t)
		if len(evs) != 1 {
			t.Fatalf("the first prune logged %d lines, want 1: %v", len(evs), evs)
		}
		ev := evs[0]
		histCheckPrune(t, ev, 3)
		if ev["deleted_results"] != 3.0 || ev["keep_days"] != 7.0 {
			t.Errorf("deleted_results = %v and keep_days = %v, want 3 and 7", ev["deleted_results"], ev["keep_days"])
		}
		if d, ok := ev["duration_ms"].(float64); !ok || d < 0 {
			t.Errorf("duration_ms = %v, want a number of milliseconds", ev["duration_ms"])
		}
	})
}
