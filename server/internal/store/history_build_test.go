package store_test

import (
	"database/sql"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/redlinelabs-dev/dyno/server/internal/result"
	"github.com/redlinelabs-dev/dyno/server/internal/settings"
	"github.com/redlinelabs-dev/dyno/server/internal/store"
)

// hxOpen opens a store in dir and closes it when the test ends.
func hxOpen(t *testing.T, dir string) *store.Store {
	t.Helper()
	st, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

func hxInt(v int) *int { return &v }

// hxAt stores a client result at the given time and returns its id.
func hxAt(t *testing.T, st *store.Store, at time.Time) int64 {
	t.Helper()
	r := result.Result{At: at, Source: "client"}
	r.Client = "Chrome on Linux"
	r.DownloadMbps = 100
	got, err := st.Add(r)
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	return got.ID
}

// hxIDs returns the ids a Query delivers, in order.
func hxIDs(t *testing.T, st *store.Store, f store.Filter) []int64 {
	t.Helper()
	ids := []int64{}
	err := st.Query(f, func(r result.Result) error {
		ids = append(ids, r.ID)
		return nil
	})
	if err != nil {
		t.Fatalf("Query(%+v): %v", f, err)
	}
	return ids
}

// Results are stored to the millisecond, so a bound with a fraction of one
// rounds up: "at or after" starts at the first whole millisecond not before
// it, and "before" ends there.
func TestHx_QueryBoundsRoundUpToTheMillisecond(t *testing.T) {
	st := hxOpen(t, t.TempDir())
	x := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	ms, us := time.Millisecond, time.Microsecond
	// Ids 1, 2 and 3 are stored at x - 1 ms, x and x + 1 ms.
	for _, at := range []time.Time{x.Add(-ms), x, x.Add(ms)} {
		hxAt(t, st, at)
	}
	for _, c := range []struct {
		name string
		f    store.Filter
		want []int64
	}{
		{"from half a millisecond before x", store.Filter{From: x.Add(-500 * us)}, []int64{3, 2}},
		{"from a microsecond after x", store.Filter{From: x.Add(us)}, []int64{3}},
		{"to half a millisecond after x", store.Filter{To: x.Add(500 * us)}, []int64{2, 1}},
		{"to a microsecond after x", store.Filter{To: x.Add(us)}, []int64{2, 1}},
		{"to a microsecond before x", store.Filter{To: x.Add(-us)}, []int64{1}},
		{"a range inside one millisecond", store.Filter{From: x.Add(us), To: x.Add(500 * us)}, []int64{}},
	} {
		if got := hxIDs(t, st, c.f); !slices.Equal(got, c.want) {
			t.Errorf("%s: ids %v, want %v", c.name, got, c.want)
		}
	}
}

// A range of time narrows the ids a page reads, but the ids stay a bound: when
// the clock stepped back and ids no longer follow time, nothing in the range
// may be lost and nothing outside it sent.
func TestHx_QueryRangeHoldsWhenIdsAndTimesRunOutOfStep(t *testing.T) {
	st := hxOpen(t, t.TempDir())
	hour := func(n int) time.Time { return time.Date(2026, 11, 10, n, 0, 0, 0, time.UTC) }
	// Ids 1 to 5 are stored at the hours 5, 1, 3, 2 and 4.
	for _, n := range []int{5, 1, 3, 2, 4} {
		hxAt(t, st, hour(n))
	}
	for _, c := range []struct {
		name string
		f    store.Filter
		want []int64
	}{
		{"from hour 4", store.Filter{From: hour(4)}, []int64{5, 1}},
		{"to hour 3", store.Filter{To: hour(3)}, []int64{4, 2}},
		{"hours 2 to 5", store.Filter{From: hour(2), To: hour(5)}, []int64{5, 4, 3}},
		{"hours 2 to 5, oldest first", store.Filter{From: hour(2), To: hour(5), Oldest: true}, []int64{3, 4, 5}},
		{"from hour 3, before id 5", store.Filter{From: hour(3), Before: 5}, []int64{3, 1}},
	} {
		if got := hxIDs(t, st, c.f); !slices.Equal(got, c.want) {
			t.Errorf("%s: ids %v, want %v", c.name, got, c.want)
		}
	}
}

func TestHx_QueryRefusesWhatItCannotRead(t *testing.T) {
	st := hxOpen(t, t.TempDir())
	hxAt(t, st, time.Now().UTC())
	for _, f := range []store.Filter{{Limit: -1}, {Outcome: "ok"}, {Outcome: "Failed"}} {
		calls := 0
		err := st.Query(f, func(result.Result) error {
			calls++
			return nil
		})
		if err == nil || calls != 0 {
			t.Errorf("Query(%+v): err %v after %d calls, want an error and no calls", f, err, calls)
		}
	}
}

// Prune deletes in statements of a page's worth: the count has to add up where
// one ends and the next begins, and when the last one is full.
func TestHx_PruneCountsAcrossStatements(t *testing.T) {
	cut := time.Date(2026, 11, 10, 0, 0, 0, 0, time.UTC)
	for _, old := range []int{1, store.PageSize - 1, store.PageSize, store.PageSize + 1, 2 * store.PageSize, 2*store.PageSize + 123} {
		st := hxOpen(t, t.TempDir())
		for i := range old {
			hxAt(t, st, cut.Add(-time.Duration(i+1)*time.Second))
		}
		var keep []int64
		for i := range 3 {
			keep = append(keep, hxAt(t, st, cut.Add(time.Duration(i)*time.Second)))
		}
		n, err := st.Prune(cut)
		if err != nil || n != int64(old) {
			t.Errorf("%d old results: Prune deleted %d, err %v", old, n, err)
		}
		if left := hxIDs(t, st, store.Filter{Oldest: true}); !slices.Equal(left, keep) {
			t.Errorf("%d old results: left %v, want %v", old, left, keep)
		}
	}
}

// A key this version doesn't know is skipped, and a value out of range comes
// back as stored: retention clamps it where it matters.
func TestHx_SettingsSkipUnknownKeysAndKeepStoredValues(t *testing.T) {
	dir := t.TempDir()
	first := hxOpen(t, dir)
	if _, err := first.PatchSettings(settings.Patch{KeepDays: hxInt(7)}); err != nil {
		t.Fatal(err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}

	raw, err := sql.Open("sqlite", filepath.Join(dir, "dyno.db"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = raw.Exec(`INSERT INTO settings (key, value) VALUES ('from_a_newer_version', '{"nested": [1, 2]}')`)
	if err == nil {
		_, err = raw.Exec(`INSERT OR REPLACE INTO settings (key, value) VALUES ('plan_down_mbps', '250000')`)
	}
	raw.Close()
	if err != nil {
		t.Fatal(err)
	}

	st := hxOpen(t, dir)
	got, err := st.Settings()
	if err != nil {
		t.Fatalf("Settings: %v", err)
	}
	if want := (settings.Settings{PlanDownMbps: 250000, KeepDays: 7}); got != want {
		t.Errorf("Settings() = %+v, want %+v", got, want)
	}
	// A patch writes only the fields it sends, so the stored value stays.
	got, err = st.PatchSettings(settings.Patch{KeepDays: hxInt(30)})
	if err != nil {
		t.Fatalf("PatchSettings: %v", err)
	}
	if want := (settings.Settings{PlanDownMbps: 250000, KeepDays: 30}); got != want {
		t.Errorf("PatchSettings returned %+v, want %+v", got, want)
	}
}
