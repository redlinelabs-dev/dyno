package retention_test

import (
	"context"
	"database/sql"
	"log"
	"log/slog"
	"maps"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/redlinelabs-dev/dyno/server/internal/retention"

	_ "modernc.org/sqlite"
)

// Builder's own cases for Sweep, the one prune that the hourly job and a PATCH
// of keep_days share (ADR-0030), beside the spec cases in events_test.go.

// wlCheckKeys wants the event to hold exactly the keys in names.
func wlCheckKeys(t *testing.T, label string, ev map[string]any, names ...string) {
	t.Helper()
	if got, want := slices.Sorted(maps.Keys(ev)), slices.Sorted(slices.Values(names)); !slices.Equal(got, want) {
		t.Errorf("%s: the event holds the keys %v, want exactly %v", label, got, want)
	}
}

// Sweep logs one results.prune when it deleted something, with the keys
// ADR-0030 names, and nothing when it deleted nothing.
func TestHx_SweepLogsOnePruneWhenItDeletesAndNothingWhenItDoesNot(t *testing.T) {
	st := histOpen(t, t.TempDir())
	histKeep(t, st, 7)
	now := time.Now()
	old := histAddAges(t, st, now, 8*histDay, 20*histDay)
	fresh := histAddAges(t, st, now, time.Hour)
	logs := &histLog{}
	lg := slog.New(slog.NewJSONHandler(logs, nil))

	retention.Sweep(context.Background(), st, lg)
	histExpect(t, st, "after the sweep", old, fresh)
	evs := logs.events(t)
	if len(evs) != 1 {
		t.Fatalf("a sweep that deleted two results logged %d lines, want 1: %v", len(evs), evs)
	}
	histCheckPrune(t, evs[0], 2)
	wlCheckKeys(t, "the sweep that deleted", evs[0], "time", "level", "msg", "result", "deleted_results", "keep_days", "duration_ms")
	if evs[0]["deleted_results"] != 2.0 || evs[0]["keep_days"] != 7.0 {
		t.Errorf("deleted_results = %v and keep_days = %v, want 2 and 7", evs[0]["deleted_results"], evs[0]["keep_days"])
	}

	retention.Sweep(context.Background(), st, lg)
	if evs = logs.events(t); len(evs) != 1 {
		t.Errorf("after a sweep that deleted nothing the log holds %v, want only the first", evs)
	}

	histKeep(t, st, 0)
	histAddAges(t, st, now, 400*histDay)
	retention.Sweep(context.Background(), st, lg)
	if evs = logs.events(t); len(evs) != 1 {
		t.Errorf("after a sweep with keep_days 0 the log holds %v, want only the first", evs)
	}
}

// A failed prune carries what every failed event carries and no keep_days. One
// that couldn't read keep_days has no count either; one whose delete failed
// reports the rows deleted before it did, here none.
func TestHx_AFailedSweepCarriesOnlyWhatAFailureCarries(t *testing.T) {
	failed := []string{"time", "level", "msg", "result", "duration_ms", "error.code", "error.message"}
	withCount := append(slices.Clone(failed), "deleted_results")

	// A closed store can't give keep_days.
	closed := histOpen(t, t.TempDir())
	histKeep(t, closed, 7)
	if err := closed.Close(); err != nil {
		t.Fatal(err)
	}
	logs := &histLog{}
	retention.Sweep(context.Background(), closed, slog.New(slog.NewJSONHandler(logs, nil)))
	evs := logs.events(t)
	if len(evs) != 1 {
		t.Fatalf("a sweep that couldn't read keep_days logged %d lines, want 1: %v", len(evs), evs)
	}
	evCheckPruneFailed(t, "the read fails", evs[0], "store_read_failed", "keep_days")
	wlCheckKeys(t, "the read fails", evs[0], failed...)

	// A store that reads keep_days and refuses the delete.
	dir := t.TempDir()
	evMakeTables(t, dir)
	evRefuse(t, dir, "results", "DELETE")
	refused := histOpen(t, dir)
	histKeep(t, refused, 7)
	old := histAddAges(t, refused, time.Now(), 30*histDay)
	logs = &histLog{}
	retention.Sweep(context.Background(), refused, slog.New(slog.NewJSONHandler(logs, nil)))
	evs = logs.events(t)
	if len(evs) != 1 {
		t.Fatalf("a sweep whose delete failed logged %d lines, want 1: %v", len(evs), evs)
	}
	evCheckPruneFailed(t, "the delete fails", evs[0], "store_delete_failed", "refused by the test")
	wlCheckKeys(t, "the delete fails", evs[0], withCount...)
	if evs[0]["deleted_results"] != 0.0 {
		t.Errorf("the delete fails: deleted_results = %v, want 0", evs[0]["deleted_results"])
	}
	histExpect(t, refused, "after a refused delete", nil, old)
}

// A delete that fails after some batches went reports how many rows they took:
// those stay deleted. The test store refuses the delete of every row with an id
// above 550, so of 600 old rows the first batch of 500 goes and the second
// fails.
func TestHx_AFailedDeleteCarriesTheRowsDeletedBeforeIt(t *testing.T) {
	dir := t.TempDir()
	evMakeTables(t, dir)
	db, err := sql.Open("sqlite", filepath.Join(dir, "dyno.db"))
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec("CREATE TRIGGER wl_refuse_late BEFORE DELETE ON results WHEN OLD.id > 550 BEGIN SELECT RAISE(ABORT, 'refused by the test'); END"); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	st := histOpen(t, dir)
	histKeep(t, st, 7)
	now := time.Now()
	ages := make([]time.Duration, 600) // id 1 is the oldest, so every scan order meets the ids in order
	for i := range ages {
		ages[i] = 30*histDay + time.Duration(len(ages)-i)*time.Minute
	}
	ids := histAddAges(t, st, now, ages...)

	logs := &histLog{}
	retention.Sweep(context.Background(), st, slog.New(slog.NewJSONHandler(logs, nil)))
	evs := logs.events(t)
	if len(evs) != 1 {
		t.Fatalf("a sweep whose second batch failed logged %d lines, want 1: %v", len(evs), evs)
	}
	evCheckPruneFailed(t, "the second batch fails", evs[0], "store_delete_failed", "refused by the test")
	wlCheckKeys(t, "the second batch fails", evs[0], "time", "level", "msg", "result", "duration_ms", "error.code", "error.message", "deleted_results")
	if evs[0]["deleted_results"] != 500.0 {
		t.Errorf("deleted_results = %v, want 500, the rows the first batch deleted", evs[0]["deleted_results"])
	}
	histExpect(t, st, "after the failed second batch", ids[:500], ids[500:])
}

// A nil log means slog.Default(), for a sweep as for Run.
func TestHx_SweepWithANilLogUsesTheDefaultLogger(t *testing.T) {
	logs := &histLog{}
	was, writer, flags := slog.Default(), log.Writer(), log.Flags()
	slog.SetDefault(slog.New(slog.NewJSONHandler(logs, nil)))
	t.Cleanup(func() {
		slog.SetDefault(was)
		log.SetOutput(writer)
		log.SetFlags(flags)
	})

	st := histOpen(t, t.TempDir())
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	retention.Sweep(context.Background(), st, nil)
	evs := logs.events(t)
	if len(evs) != 1 {
		t.Fatalf("a sweep with a nil log logged %d lines to the default logger, want 1: %v", len(evs), evs)
	}
	histCheckFailedPrune(t, evs[0], "store_read_failed")
}
