package store_test

import (
	"database/sql"
	"errors"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/redlinelabs-dev/dyno/server/internal/store"
)

// Builder's own cases, beside the spec cases in schedules_test.go.

func TestCx_NewestResultMeansHighestID(t *testing.T) {
	defer schNoPanic(t)
	s := schOpen(t)
	sc := schAddSchedule(t, s, "a")
	// Stored in this order, but the first is dated later than the second.
	first := schAdd(t, s, schRun(sc.ID, sc.FarEnd, 5, 8, nil))
	second := schAdd(t, s, schRun(sc.ID, sc.FarEnd, 1, 9, nil))

	if last, err := s.LastResult(sc.ID); err != nil || last.ID != second.ID {
		t.Errorf("LastResult = result %d, err %v; want %d, the highest id, not %d, the latest time",
			last.ID, err, second.ID, first.ID)
	}
	if good, err := s.LastSuccess(sc.ID); err != nil || good.ID != second.ID {
		t.Errorf("LastSuccess = result %d, err %v; want %d", good.ID, err, second.ID)
	}
	if idle, err := s.RecentIdle(sc.ID, sc.FarEnd, 2); err != nil || !slices.Equal(idle, []float64{9, 8}) {
		t.Errorf("RecentIdle = %v, err %v; want [9 8], the highest id first", idle, err)
	}
}

func TestCx_NoScheduleHasAnIDBelowOne(t *testing.T) {
	defer schNoPanic(t)
	s := schOpen(t)
	schAdd(t, s, schClientResult(0)) // a client result's schedule_id is 0
	for _, id := range []int64{0, -1} {
		if got, err := s.LastResult(id); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("LastResult(%d) = %+v, err %v; want ErrNotFound", id, got, err)
		}
		if got, err := s.LastSuccess(id); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("LastSuccess(%d) = %+v, err %v; want ErrNotFound", id, got, err)
		}
		if got, err := s.RecentIdle(id, "", 5); err != nil || len(got) != 0 {
			t.Errorf("RecentIdle(%d) = %v, err %v; want none", id, got, err)
		}
	}
}

func TestCx_RecentIdleOfNoneOrFewerThanNoneIsEmpty(t *testing.T) {
	defer schNoPanic(t)
	s := schOpen(t)
	sc := schAddSchedule(t, s, "a")
	schAdd(t, s, schRun(sc.ID, sc.FarEnd, 0, 8, nil))
	schAdd(t, s, schRun(sc.ID, sc.FarEnd, 1, 9, nil))
	for _, n := range []int{0, -1} { // SQLite reads a negative LIMIT as no limit
		if got, err := s.RecentIdle(sc.ID, sc.FarEnd, n); err != nil || len(got) != 0 {
			t.Errorf("RecentIdle(n = %d) = %v, err %v; want none", n, got, err)
		}
	}
}

func TestCx_UpdatedAtNeverGoesBack(t *testing.T) {
	defer schNoPanic(t)
	dir := t.TempDir()
	s := schOpenIn(t, dir)
	sc := schAddSchedule(t, s, "a")

	// As if the clock had been set back since the schedule was saved.
	ahead := time.Now().Add(time.Hour).UTC().Truncate(time.Millisecond)
	db, err := sql.Open("sqlite", "file:"+filepath.Join(dir, "dyno.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`UPDATE schedules SET updated_at = ? WHERE id = ?`, ahead.UnixMilli(), sc.ID); err != nil {
		t.Fatal(err)
	}

	got, err := s.UpdateSchedule(sc)
	if err != nil {
		t.Fatal(err)
	}
	if !got.UpdatedAt.Equal(ahead) {
		t.Errorf("updated_at = %s, want it kept at %s, not moved back to now", got.UpdatedAt, ahead)
	}
}
