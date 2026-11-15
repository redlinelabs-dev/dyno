package retention_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/redlinelabs-dev/dyno/server/internal/result"
	"github.com/redlinelabs-dev/dyno/server/internal/retention"
	"github.com/redlinelabs-dev/dyno/server/internal/settings"
	"github.com/redlinelabs-dev/dyno/server/internal/store"
)

const histDay = 24 * time.Hour

func histOpen(t *testing.T, dir string) *store.Store {
	t.Helper()
	st, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

// histRes is a client result stored at the given time.
func histRes(at time.Time) result.Result {
	r := result.Result{At: at, Source: "client"}
	r.Client = "Chrome on Linux"
	r.DownloadMbps = 940
	r.UploadMbps = 400
	r.IdleMs = 8
	r.Streams = 5
	return r
}

// histFailedRun is a scheduled run that failed, stored at the given time.
func histFailedRun(at time.Time) result.Result {
	r := histRes(at)
	r.Source = "schedule"
	r.ScheduleID = 3
	r.Client = "dyno/0.1.0"
	r.DownloadMbps, r.UploadMbps, r.IdleMs, r.Streams = 0, 0, 0, 0
	r.Failure = &result.Failure{Code: "timeout", Phase: "download", Message: "The far end stopped answering."}
	return r
}

func histAdd(t *testing.T, st *store.Store, r result.Result) int64 {
	t.Helper()
	got, err := st.Add(r)
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	return got.ID
}

// histAgo is the time d before now.
func histAgo(now time.Time, d time.Duration) time.Time {
	return now.Add(-d)
}

// histAddAges stores one client result for each age, that long before now,
// and returns their ids.
func histAddAges(t *testing.T, st *store.Store, now time.Time, ages ...time.Duration) []int64 {
	t.Helper()
	var ids []int64
	for _, age := range ages {
		ids = append(ids, histAdd(t, st, histRes(histAgo(now, age))))
	}
	return ids
}

func histKeep(t *testing.T, st *store.Store, days int) {
	t.Helper()
	if _, err := st.PatchSettings(settings.Patch{KeepDays: &days}); err != nil {
		t.Fatal(err)
	}
}

// histCheckLeft wants exactly the results with the ids in want to be stored.
func histCheckLeft(t *testing.T, st *store.Store, what string, want []int64) {
	t.Helper()
	list, err := st.List(1000, 0, "")
	if err != nil {
		t.Fatal(err)
	}
	got := make([]int64, 0, len(list))
	for _, r := range list {
		got = append(got, r.ID)
	}
	slices.Sort(got)
	want = slices.Clone(want)
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Errorf("%s: results left %v, want %v", what, got, want)
	}
}

// histExpect wants the results in gone to be gone and those in kept to be
// stored.
func histExpect(t *testing.T, st *store.Store, what string, gone, kept []int64) {
	t.Helper()
	for _, id := range gone {
		if _, err := st.Get(id); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("%s: result %d should be gone, Get returned %v", what, id, err)
		}
	}
	for _, id := range kept {
		if _, err := st.Get(id); err != nil {
			t.Errorf("%s: result %d should be kept, Get returned %v", what, id, err)
		}
	}
}

// histLog is a log destination that Run's goroutine can write while the test
// reads it.
type histLog struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (l *histLog) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.Write(p)
}

// events parses the JSON lines logged so far.
func (l *histLog) events(t *testing.T) []map[string]any {
	t.Helper()
	l.mu.Lock()
	text := l.buf.String()
	l.mu.Unlock()
	var out []map[string]any
	for _, line := range strings.Split(text, "\n") {
		if line == "" {
			continue
		}
		var ev map[string]any
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			t.Fatalf("log line is not JSON: %q", line)
		}
		out = append(out, ev)
	}
	return out
}

// histCheckFlat wants every value of a log event to be a primitive (ADR-0020).
func histCheckFlat(t *testing.T, ev map[string]any) {
	t.Helper()
	for k, v := range ev {
		switch v.(type) {
		case map[string]any, []any:
			t.Errorf("%s is nested: %v (ADR-0020: primitives only)", k, v)
		}
	}
}

// histCheckPrune checks a results.prune event for a prune that deleted the
// given number of results; ADR-0030 names the count's key, deleted_results.
func histCheckPrune(t *testing.T, ev map[string]any, deleted int) {
	t.Helper()
	if ev["msg"] != "results.prune" {
		t.Errorf("msg = %v, want results.prune", ev["msg"])
	}
	if ev["level"] != "INFO" {
		t.Errorf("level = %v, want INFO for a prune that went as planned", ev["level"])
	}
	if ev["result"] != "succeeded" {
		t.Errorf("result = %v, want succeeded", ev["result"])
	}
	histCheckFlat(t, ev)
	if n, ok := ev["deleted_results"].(float64); !ok || n != float64(deleted) {
		t.Errorf("deleted_results = %v in %v, want %d", ev["deleted_results"], ev, deleted)
	}
	// ADR-0030 gives every logged prune keep_days and duration_ms. The callers
	// differ in keep_days, so only its being a positive number is checked here.
	if n, ok := ev["keep_days"].(float64); !ok || n <= 0 {
		t.Errorf("keep_days = %v in %v, want a positive number (ADR-0030)", ev["keep_days"], ev)
	}
	if d, ok := ev["duration_ms"].(float64); !ok || d < 0 {
		t.Errorf("duration_ms = %v in %v, want a number of milliseconds, 0 or more (ADR-0030)", ev["duration_ms"], ev)
	}
}

// histCheckFailedPrune checks a results.prune event for a prune that failed
// with the given error.code, at ERROR: the database refused it (ADR-0030).
func histCheckFailedPrune(t *testing.T, ev map[string]any, code string) {
	t.Helper()
	if ev["msg"] != "results.prune" {
		t.Errorf("msg = %v, want results.prune", ev["msg"])
	}
	if ev["level"] != "ERROR" {
		t.Errorf("level = %v, want ERROR for a prune the database refused (ADR-0030)", ev["level"])
	}
	if ev["result"] != "failed" {
		t.Errorf("result = %v, want failed", ev["result"])
	}
	if ev["error.code"] != code {
		t.Errorf("error.code = %v, want %s", ev["error.code"], code)
	}
	for _, key := range []string{"error.code", "error.message"} {
		if s, ok := ev[key].(string); !ok || s == "" {
			t.Errorf("%s = %v, want a non-empty string (ADR-0020)", key, ev[key])
		}
	}
	histCheckFlat(t, ev)
}

func TestH9_PruneWithKeepDaysZeroDeletesNothing(t *testing.T) {
	st := histOpen(t, t.TempDir())
	now := time.Date(2026, 11, 13, 12, 0, 0, 0, time.UTC)
	ids := histAddAges(t, st, now, 3650*histDay, 400*histDay, 8*histDay, histDay, 0)
	failed := histAdd(t, st, histFailedRun(histAgo(now, 90*histDay)))
	ids = append(ids, failed)

	for _, at := range []time.Time{now, now.Add(1000 * histDay)} {
		n, err := retention.Prune(st, at)
		if err != nil {
			t.Fatalf("Prune(st, %v): %v", at, err)
		}
		if n != 0 {
			t.Errorf("Prune(st, %v) with keep_days 0 deleted %d results", at, n)
		}
	}
	histCheckLeft(t, st, "with keep_days 0", ids)
}

func TestH9_PruneDeletesWhatIsOlderThanKeepDays(t *testing.T) {
	st := histOpen(t, t.TempDir())
	now := time.Date(2026, 11, 13, 12, 0, 0, 0, time.UTC)
	week := 7 * histDay
	// goneAt is the step at which Prune deletes a result: 1 with keep_days 7,
	// 2 once keep_days is 1, and 0 for one that stays.
	rows := []struct {
		name   string
		age    time.Duration
		failed bool
		goneAt int
	}{
		{"a year old", 365 * histDay, false, 1},
		{"a failed run 30 days old", 30 * histDay, true, 1},
		{"8 days old", 8 * histDay, false, 1},
		{"7 days and a millisecond old", week + time.Millisecond, false, 1},
		{"exactly 7 days old", week, false, 2},
		{"6 days old", 6 * histDay, false, 2},
		{"25 hours old", 25 * time.Hour, false, 2},
		{"exactly 1 day old", histDay, false, 0},
		{"an hour old", time.Hour, false, 0},
		{"new", 0, false, 0},
	}
	histKeep(t, st, 7)
	ids := make([]int64, len(rows))
	for i, row := range rows {
		res := histRes(histAgo(now, row.age))
		if row.failed {
			res = histFailedRun(histAgo(now, row.age))
		}
		ids[i] = histAdd(t, st, res)
	}
	// stay lists the results that are still stored once step has run.
	stay := func(step int) []int64 {
		var out []int64
		for i, row := range rows {
			if row.goneAt == 0 || row.goneAt > step {
				out = append(out, ids[i])
			}
		}
		return out
	}
	deleted := func(step int) int64 {
		n := int64(0)
		for _, row := range rows {
			if row.goneAt == step {
				n++
			}
		}
		return n
	}

	n, err := retention.Prune(st, now)
	if err != nil {
		t.Fatalf("Prune with keep_days 7: %v", err)
	}
	if n != deleted(1) {
		t.Errorf("Prune with keep_days 7 deleted %d results, want %d", n, deleted(1))
	}
	histCheckLeft(t, st, "with keep_days 7", stay(1))

	// keep_days is read at each call: lowering it takes effect at the next one.
	histKeep(t, st, 1)
	n, err = retention.Prune(st, now)
	if err != nil {
		t.Fatalf("Prune with keep_days 1: %v", err)
	}
	if n != deleted(2) {
		t.Errorf("Prune with keep_days 1 deleted %d results, want %d", n, deleted(2))
	}
	histCheckLeft(t, st, "with keep_days 1", stay(2))

	n, err = retention.Prune(st, now)
	if err != nil || n != 0 {
		t.Errorf("a second Prune with keep_days 1: deleted %d, err %v, want 0 and nil", n, err)
	}
	histCheckLeft(t, st, "after the second Prune", stay(2))
}

func TestH10_RunPrunesAtOnceThenEveryHour(t *testing.T) {
	if retention.Every != time.Hour {
		t.Errorf("Every = %v, want one hour", retention.Every)
	}
	dir := t.TempDir()
	synctest.Test(t, func(t *testing.T) {
		st, err := store.Open(dir)
		if err != nil {
			t.Fatal(err)
		}
		defer st.Close()
		histKeep(t, st, 7)

		// old results are too old now, soon ones are 30 and 45 minutes short
		// of it, and fresh ones stay.
		t0 := time.Now()
		week := 7 * histDay
		old := histAddAges(t, st, t0, 8*histDay, 9*histDay, 10*histDay)
		soon := histAddAges(t, st, t0, week-30*time.Minute, week-45*time.Minute)
		fresh := histAddAges(t, st, t0, 0, time.Hour, 2*time.Hour, 3*time.Hour)

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		logs := &histLog{}
		done := make(chan struct{})
		go func() {
			defer close(done)
			retention.Run(ctx, st, slog.New(slog.NewJSONHandler(logs, nil)))
		}()

		synctest.Wait()
		histExpect(t, st, "at once", old, slices.Concat(soon, fresh))
		evs := logs.events(t)
		if len(evs) != 1 {
			t.Fatalf("the first prune logged %d lines, want 1: %v", len(evs), evs)
		}
		histCheckPrune(t, evs[0], 3)

		time.Sleep(retention.Every + time.Minute)
		synctest.Wait()
		histExpect(t, st, "an hour on", slices.Concat(old, soon), fresh)
		evs = logs.events(t)
		if len(evs) != 2 {
			t.Fatalf("after an hour there are %d log lines, want 2: %v", len(evs), evs)
		}
		histCheckPrune(t, evs[1], 2)

		time.Sleep(retention.Every)
		synctest.Wait()
		histExpect(t, st, "two hours on", slices.Concat(old, soon), fresh)
		if evs = logs.events(t); len(evs) != 2 {
			t.Errorf("a prune that deleted nothing logged: %v", evs[2:])
		}

		cancel()
		synctest.Wait()
		select {
		case <-done:
		default:
			t.Fatal("Run did not return once ctx was done")
		}

		// Nothing prunes once Run has returned.
		late := histAddAges(t, st, time.Now(), 30*histDay)
		time.Sleep(3 * retention.Every)
		synctest.Wait()
		histExpect(t, st, "after Run returned", nil, late)
		if evs = logs.events(t); len(evs) != 2 {
			t.Errorf("log lines after Run returned: %v", evs[2:])
		}
	})
}

func TestH10_RunLogsNothingWhenItDeletesNothing(t *testing.T) {
	for _, c := range []struct {
		name string
		keep int
		ages []time.Duration
	}{
		{"keep_days 0 keeps even the oldest", 0, []time.Duration{3650 * histDay, 400 * histDay, histDay}},
		{"keep_days 7 and nothing older", 7, []time.Duration{6 * histDay, time.Hour, 0}},
	} {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			synctest.Test(t, func(t *testing.T) {
				st, err := store.Open(dir)
				if err != nil {
					t.Fatal(err)
				}
				defer st.Close()
				histKeep(t, st, c.keep)
				ids := histAddAges(t, st, time.Now(), c.ages...)

				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				logs := &histLog{}
				done := make(chan struct{})
				go func() {
					defer close(done)
					retention.Run(ctx, st, slog.New(slog.NewJSONHandler(logs, nil)))
				}()

				// The first prune and three hourly ones.
				for range 4 {
					synctest.Wait()
					histExpect(t, st, "nothing was old", nil, ids)
					if evs := logs.events(t); len(evs) != 0 {
						t.Fatalf("Run logged when it deleted nothing: %v", evs)
					}
					time.Sleep(retention.Every + time.Minute)
				}
				cancel()
				synctest.Wait()
				select {
				case <-done:
				default:
					t.Error("Run did not return once ctx was done")
				}
			})
		})
	}
}

func TestH10_RunLogsAPruneThatFails(t *testing.T) {
	dir := t.TempDir()
	synctest.Test(t, func(t *testing.T) {
		st, err := store.Open(dir)
		if err != nil {
			t.Fatal(err)
		}
		// A closed store refuses every read and write.
		if err := st.Close(); err != nil {
			t.Fatal(err)
		}

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		logs := &histLog{}
		done := make(chan struct{})
		go func() {
			defer close(done)
			retention.Run(ctx, st, slog.New(slog.NewJSONHandler(logs, nil)))
		}()

		synctest.Wait()
		evs := logs.events(t)
		if len(evs) != 1 {
			t.Fatalf("the first prune logged %d lines, want 1: %v", len(evs), evs)
		}
		histCheckFailedPrune(t, evs[0], "store_read_failed")

		// A failure doesn't stop Run: it tries again an hour later.
		time.Sleep(retention.Every + time.Minute)
		synctest.Wait()
		if evs = logs.events(t); len(evs) != 2 {
			t.Fatalf("after an hour there are %d log lines, want 2: %v", len(evs), evs)
		}
		histCheckFailedPrune(t, evs[1], "store_read_failed")

		cancel()
		synctest.Wait()
		select {
		case <-done:
		default:
			t.Error("Run did not return once ctx was done")
		}
	})
}
