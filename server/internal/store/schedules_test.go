package store_test

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"path/filepath"
	"reflect"
	"runtime/debug"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/redlinelabs-dev/dyno/server/internal/result"
	"github.com/redlinelabs-dev/dyno/server/internal/schedule"
	"github.com/redlinelabs-dev/dyno/server/internal/store"
)

// schNoPanic turns a panic in a test, such as a stub that isn't built yet, into
// a failure of that test alone instead of an abort of the whole package.
func schNoPanic(t *testing.T) {
	if r := recover(); r != nil {
		t.Fatalf("panic: %v\n%s", r, debug.Stack())
	}
}

// schOpen opens a store in a new directory and closes it when the test ends.
func schOpen(t *testing.T) *store.Store {
	t.Helper()
	return schOpenIn(t, t.TempDir())
}

func schOpenIn(t *testing.T, dir string) *store.Store {
	t.Helper()
	s, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

// schNew is a schedule that isn't stored yet: no id and no times.
func schNew(host string) schedule.Schedule {
	return schedule.Schedule{
		Name: host, FarEnd: "http://" + host + ":3966", Kind: schedule.Light,
		EveryMinutes: 60, OffsetMinutes: 15,
	}
}

func schAddSchedule(t *testing.T, s *store.Store, host string) schedule.Schedule {
	t.Helper()
	got, err := s.AddSchedule(schNew(host))
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func schAdd(t *testing.T, s *store.Store, r result.Result) result.Result {
	t.Helper()
	got, err := s.Add(r)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

// schSame reports a difference between two schedules, comparing times as
// instants.
func schSame(t *testing.T, what string, got, want schedule.Schedule) {
	t.Helper()
	if got.ID != want.ID || got.Name != want.Name || got.FarEnd != want.FarEnd || got.Kind != want.Kind ||
		got.EveryMinutes != want.EveryMinutes || got.OffsetMinutes != want.OffsetMinutes || got.Paused != want.Paused ||
		!got.CreatedAt.Equal(want.CreatedAt) || !got.UpdatedAt.Equal(want.UpdatedAt) {
		t.Errorf("%s:\n got %+v\nwant %+v", what, got, want)
	}
}

// schCheckStamp fails the test unless ts is in UTC with no part of a
// millisecond.
func schCheckStamp(t *testing.T, what string, ts time.Time) {
	t.Helper()
	if ts.Location() != time.UTC {
		t.Errorf("%s is in %s, want UTC", what, ts.Location())
	}
	if ts.Nanosecond()%1_000_000 != 0 {
		t.Errorf("%s = %s, want a whole millisecond", what, ts.Format(time.RFC3339Nano))
	}
}

// schBase is the time of the first result a test stores; schAt(i) is i
// minutes later.
var schBase = time.Date(2026, 11, 13, 12, 0, 0, 0, time.UTC)

func schAt(i int) time.Time { return schBase.Add(time.Duration(i) * time.Minute) }

// schRun is the result of a scheduled test for schedule id against farEnd, i
// minutes after schBase: a success with idle latency idle, or a failure with
// no measurements when fail is set. A success carries detail.
func schRun(id int64, farEnd string, i int, idle float64, fail *result.Failure) result.Result {
	r := result.Result{At: schAt(i), Source: "schedule", ScheduleID: id, FarEnd: farEnd, Failure: fail}
	r.Client = "dyno/v0.1.0"
	if fail == nil {
		r.DownloadMbps, r.UploadMbps = 900+float64(i), 90+float64(i)
		r.IdleMs, r.LoadedDownMs, r.LoadedUpMs, r.JitterMs, r.Streams = idle, idle+4, idle+6, 0.5, 5
		r.Detail = []byte(`{"idle":[[0.05,8.1]]}`)
		r.Derive()
	}
	return r
}

// schClientResult is a result a browser posted.
func schClientResult(i int) result.Result {
	r := result.Result{At: schAt(i), Source: "client"}
	r.Client, r.Device, r.TestID = "Chrome on Linux", "kitchen-ipad", "3f9c2a7e01b44d6a9e5c8b12"
	r.DownloadMbps, r.UploadMbps, r.IdleMs, r.LoadedDownMs, r.LoadedUpMs, r.Streams = 938.2, 412.6, 8.4, 21, 39.5, 5
	r.Derive()
	return r
}

var schTimeout = &result.Failure{Code: "timeout", Phase: "download", Message: "The far end stopped answering during the download."}

// schWithoutDetail is r as a list or a "last result" call returns it.
func schWithoutDetail(r result.Result) result.Result {
	r.Detail = nil
	return r
}

// schResultEqual compares two results, the time as an instant.
func schResultEqual(got, want result.Result) bool {
	if !got.At.Equal(want.At) {
		return false
	}
	got.At = want.At
	return reflect.DeepEqual(got, want)
}

func TestC9_AddScheduleReturnsWhatItStored(t *testing.T) {
	defer schNoPanic(t)
	s := schOpen(t)
	inputs := []schedule.Schedule{
		schNew("tailmeadow"),
		{Name: "Gran's", FarEnd: "https://dyno.example:8443", Kind: schedule.Full, EveryMinutes: 15, OffsetMinutes: 14, Paused: true},
		{Name: "Night", FarEnd: "http://[fd7a::1]:3966", Kind: schedule.Light, EveryMinutes: 1440, OffsetMinutes: 180},
	}
	var added []schedule.Schedule
	for _, in := range inputs {
		before := time.Now().Truncate(time.Millisecond)
		got, err := s.AddSchedule(in)
		after := time.Now()
		if err != nil {
			t.Fatalf("AddSchedule(%+v): %v", in, err)
		}
		if got.ID <= 0 || (len(added) > 0 && got.ID <= added[len(added)-1].ID) {
			t.Errorf("id = %d after %d schedules, want a positive id above the earlier ones", got.ID, len(added))
		}
		want := in
		want.ID, want.CreatedAt, want.UpdatedAt = got.ID, got.CreatedAt, got.UpdatedAt
		schSame(t, "AddSchedule's reply", got, want)
		if !got.CreatedAt.Equal(got.UpdatedAt) {
			t.Errorf("created_at %s and updated_at %s differ on a new schedule", got.CreatedAt, got.UpdatedAt)
		}
		schCheckStamp(t, "created_at", got.CreatedAt)
		schCheckStamp(t, "updated_at", got.UpdatedAt)
		if got.CreatedAt.Before(before) || got.CreatedAt.After(after) {
			t.Errorf("created_at %s is outside the call's window %s to %s",
				got.CreatedAt.Format(time.RFC3339Nano), before.Format(time.RFC3339Nano), after.Format(time.RFC3339Nano))
		}

		back, err := s.Schedule(got.ID)
		if err != nil {
			t.Fatalf("Schedule(%d): %v", got.ID, err)
		}
		schSame(t, "Schedule(id)", back, got)
		schCheckStamp(t, "stored created_at", back.CreatedAt)
		schCheckStamp(t, "stored updated_at", back.UpdatedAt)
		added = append(added, got)
	}

	list, err := s.Schedules()
	if err != nil || len(list) != len(added) {
		t.Fatalf("Schedules() has %d schedules, err %v; want %d", len(list), err, len(added))
	}
	for i := range added {
		schSame(t, fmt.Sprintf("Schedules()[%d]", i), list[i], added[i])
	}
	if _, err := s.Schedule(added[len(added)-1].ID + 1); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("Schedule of an unknown id: err %v, want ErrNotFound", err)
	}
}

func TestC9_SchedulesOfAnEmptyStore(t *testing.T) {
	defer schNoPanic(t)
	s := schOpen(t)
	if list, err := s.Schedules(); err != nil || len(list) != 0 {
		t.Fatalf("Schedules() = %v, err %v; want none", list, err)
	}
}

func TestC10_AddScheduleStopsAtTheLimit(t *testing.T) {
	defer schNoPanic(t)
	s := schOpen(t)
	for i := range schedule.MaxSchedules {
		if _, err := s.AddSchedule(schNew(fmt.Sprintf("host%d", i))); err != nil {
			t.Fatalf("schedule %d: %v", i+1, err)
		}
	}
	extra, err := s.AddSchedule(schNew("one-too-many"))
	if !errors.Is(err, store.ErrFull) {
		t.Fatalf("schedule %d: err %v (%+v), want ErrFull", schedule.MaxSchedules+1, err, extra)
	}
	list, err := s.Schedules()
	if err != nil || len(list) != schedule.MaxSchedules {
		t.Fatalf("after the refused add Schedules() has %d, err %v; want %d", len(list), err, schedule.MaxSchedules)
	}
	for _, sc := range list {
		if sc.Name == "one-too-many" {
			t.Error("the refused schedule was stored")
		}
	}

	// A delete makes room for exactly one more.
	if err := s.DeleteSchedule(list[0].ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddSchedule(schNew("fits-now")); err != nil {
		t.Errorf("add after a delete: %v", err)
	}
	if _, err := s.AddSchedule(schNew("full-again")); !errors.Is(err, store.ErrFull) {
		t.Errorf("add past the limit again: err %v, want ErrFull", err)
	}
}

func TestC10_ConcurrentAddsStopAtTheLimit(t *testing.T) {
	defer schNoPanic(t)
	const workers = 32
	for round := range 3 {
		s := schOpen(t)
		start := make(chan struct{})
		var wg sync.WaitGroup
		var mu sync.Mutex
		var added, full int
		var other []error
		for i := range workers {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				err := func() (err error) {
					defer func() { // a stub that panics fails the test, it doesn't abort the package
						if r := recover(); r != nil {
							err = fmt.Errorf("panic: %v", r)
						}
					}()
					_, err = s.AddSchedule(schNew(fmt.Sprintf("host%d", i)))
					return err
				}()
				mu.Lock()
				defer mu.Unlock()
				switch {
				case err == nil:
					added++
				case errors.Is(err, store.ErrFull):
					full++
				default:
					other = append(other, err)
				}
			}()
		}
		close(start)
		wg.Wait()
		if len(other) > 0 {
			t.Fatalf("round %d: unexpected errors: %v", round, other)
		}
		if added != schedule.MaxSchedules || full != workers-schedule.MaxSchedules {
			t.Errorf("round %d: %d adds succeeded and %d got ErrFull, want %d and %d",
				round, added, full, schedule.MaxSchedules, workers-schedule.MaxSchedules)
		}
		if list, err := s.Schedules(); err != nil || len(list) != schedule.MaxSchedules {
			t.Errorf("round %d: Schedules() has %d, err %v; want %d", round, len(list), err, schedule.MaxSchedules)
		}
	}
}

func TestC11_IdsAreNeverReused(t *testing.T) {
	defer schNoPanic(t)
	dir := t.TempDir()
	s := schOpenIn(t, dir)
	var ids []int64
	for _, host := range []string{"a", "b", "c"} {
		ids = append(ids, schAddSchedule(t, s, host).ID)
	}
	if err := s.DeleteSchedule(ids[2]); err != nil {
		t.Fatal(err)
	}
	fourth := schAddSchedule(t, s, "d")
	if fourth.ID != 4 {
		t.Errorf("id after deleting the newest of three = %d, want 4", fourth.ID)
	}

	// Delete them all: the next id is still above every one handed out.
	list, err := s.Schedules()
	if err != nil || len(list) != 3 {
		t.Fatalf("Schedules() = %v, err %v", list, err)
	}
	for _, sc := range list {
		if err := s.DeleteSchedule(sc.ID); err != nil {
			t.Fatal(err)
		}
	}
	fifth := schAddSchedule(t, s, "e")
	if fifth.ID <= fourth.ID {
		t.Errorf("id after deleting every schedule = %d, want above %d", fifth.ID, fourth.ID)
	}

	// And across a restart.
	if err := s.DeleteSchedule(fifth.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	again := schAddSchedule(t, schOpenIn(t, dir), "f")
	if again.ID <= fifth.ID {
		t.Errorf("id after a restart = %d, want above %d", again.ID, fifth.ID)
	}
}

func TestC12_UpdateScheduleSavesEveryField(t *testing.T) {
	defer schNoPanic(t)
	s := schOpen(t)
	orig := schAddSchedule(t, s, "tailmeadow")
	bystander := schAddSchedule(t, s, "bystander")
	time.Sleep(5 * time.Millisecond) // so a new updated_at can't equal the old one

	changed := orig
	changed.Name, changed.FarEnd, changed.Kind = "Gran's", "https://dyno.example:8443", schedule.Full
	changed.EveryMinutes, changed.OffsetMinutes, changed.Paused = 240, 100, true
	before := time.Now().Truncate(time.Millisecond)
	got, err := s.UpdateSchedule(changed)
	after := time.Now()
	if err != nil {
		t.Fatal(err)
	}
	want := changed
	want.UpdatedAt = got.UpdatedAt
	schSame(t, "UpdateSchedule's reply", got, want)
	if !got.CreatedAt.Equal(orig.CreatedAt) {
		t.Errorf("created_at = %s, want it kept at %s", got.CreatedAt, orig.CreatedAt)
	}
	if got.UpdatedAt.Before(orig.UpdatedAt) || got.UpdatedAt.Before(before) || got.UpdatedAt.After(after) {
		t.Errorf("updated_at = %s, want now (between %s and %s) and not before %s",
			got.UpdatedAt.Format(time.RFC3339Nano), before.Format(time.RFC3339Nano),
			after.Format(time.RFC3339Nano), orig.UpdatedAt.Format(time.RFC3339Nano))
	}
	schCheckStamp(t, "updated_at", got.UpdatedAt)

	back, err := s.Schedule(orig.ID)
	if err != nil {
		t.Fatal(err)
	}
	schSame(t, "Schedule after the update", back, got)
	other, err := s.Schedule(bystander.ID)
	if err != nil {
		t.Fatal(err)
	}
	schSame(t, "another schedule after the update", other, bystander)

	// A caller's stale created_at doesn't overwrite the stored one.
	stale := got
	stale.Name, stale.CreatedAt = "Stale", time.Time{}
	if _, err := s.UpdateSchedule(stale); err != nil {
		t.Fatal(err)
	}
	if back, err := s.Schedule(orig.ID); err != nil || back.Name != "Stale" || !back.CreatedAt.Equal(orig.CreatedAt) {
		t.Errorf("after an update with a zero created_at: %+v, err %v; want created_at %s kept", back, err, orig.CreatedAt)
	}
}

func TestC12_UpdateUnknownScheduleIsNotFound(t *testing.T) {
	defer schNoPanic(t)
	s := schOpen(t)
	ghost := schNew("ghost")
	ghost.ID = 999
	if _, err := s.UpdateSchedule(ghost); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("update of an unknown id: err %v, want ErrNotFound", err)
	}

	// A deleted schedule is unknown too, and an update doesn't bring it back.
	gone := schAddSchedule(t, s, "gone")
	if err := s.DeleteSchedule(gone.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.UpdateSchedule(gone); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("update of a deleted schedule: err %v, want ErrNotFound", err)
	}
	if list, err := s.Schedules(); err != nil || len(list) != 0 {
		t.Errorf("Schedules() = %v, err %v; want none", list, err)
	}
}

func TestC13_DeleteScheduleKeepsItsResults(t *testing.T) {
	defer schNoPanic(t)
	s := schOpen(t)
	doomed := schAddSchedule(t, s, "doomed")
	kept := schAddSchedule(t, s, "kept")
	ok := schAdd(t, s, schRun(doomed.ID, doomed.FarEnd, 0, 8, nil))
	failed := schAdd(t, s, schRun(doomed.ID, doomed.FarEnd, 1, 0, schTimeout))
	client := schAdd(t, s, schClientResult(2))

	if err := s.DeleteSchedule(doomed.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Schedule(doomed.ID); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("Schedule after its delete: err %v, want ErrNotFound", err)
	}
	if list, err := s.Schedules(); err != nil || len(list) != 1 || list[0].ID != kept.ID {
		t.Errorf("Schedules() after the delete = %+v, err %v; want only the other schedule", list, err)
	}
	for _, want := range []result.Result{ok, failed, client} {
		got, err := s.Get(want.ID)
		if err != nil || !schResultEqual(got, want) {
			t.Errorf("Get(%d) after the schedule's delete:\n got %+v, err %v\nwant %+v", want.ID, got, err, want)
		}
	}
	if list, err := s.List(10, 0, ""); err != nil || len(list) != 3 {
		t.Errorf("List after the delete has %d results, err %v; want 3", len(list), err)
	}

	if err := s.DeleteSchedule(doomed.ID); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("second delete: err %v, want ErrNotFound", err)
	}
	if err := s.DeleteSchedule(999); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("delete of an unknown id: err %v, want ErrNotFound", err)
	}
}

func TestC14_LastResultAndLastSuccess(t *testing.T) {
	defer schNoPanic(t)
	s := schOpen(t)
	a := schAddSchedule(t, s, "a")
	b := schAddSchedule(t, s, "b")
	idle := schAddSchedule(t, s, "idle")       // never ran
	failing := schAddSchedule(t, s, "failing") // only ever failed

	schAdd(t, s, schRun(a.ID, a.FarEnd, 0, 8, nil))
	a2 := schAdd(t, s, schRun(a.ID, a.FarEnd, 1, 9, nil))
	b1 := schAdd(t, s, schRun(b.ID, b.FarEnd, 2, 10, nil))
	schAdd(t, s, schClientResult(3))
	a3 := schAdd(t, s, schRun(a.ID, a.FarEnd, 4, 0, schTimeout))
	b2 := schAdd(t, s, schRun(b.ID, b.FarEnd, 5, 0, schTimeout))
	f1 := schAdd(t, s, schRun(failing.ID, failing.FarEnd, 6, 0, schTimeout))
	schAdd(t, s, schClientResult(7)) // the newest result of all, and nobody's

	for _, c := range []struct {
		label string
		id    int64
		last  *result.Result // nil: ErrNotFound
		good  *result.Result
	}{
		{"a", a.ID, &a3, &a2},
		{"b", b.ID, &b2, &b1},
		{"a schedule that only failed", failing.ID, &f1, nil},
		{"a schedule that never ran", idle.ID, nil, nil},
		{"an unknown schedule", 999, nil, nil},
	} {
		got, err := s.LastResult(c.id)
		switch {
		case c.last == nil:
			if !errors.Is(err, store.ErrNotFound) {
				t.Errorf("LastResult(%s): %+v, err %v; want ErrNotFound", c.label, got, err)
			}
		case err != nil || !schResultEqual(got, schWithoutDetail(*c.last)):
			t.Errorf("LastResult(%s):\n got %+v, err %v\nwant %+v", c.label, got, err, schWithoutDetail(*c.last))
		}

		got, err = s.LastSuccess(c.id)
		switch {
		case c.good == nil:
			if !errors.Is(err, store.ErrNotFound) {
				t.Errorf("LastSuccess(%s): %+v, err %v; want ErrNotFound", c.label, got, err)
			}
		case err != nil || !schResultEqual(got, schWithoutDetail(*c.good)):
			t.Errorf("LastSuccess(%s):\n got %+v, err %v\nwant %+v", c.label, got, err, schWithoutDetail(*c.good))
		}
	}

	// The failure comes back filled in.
	if got, err := s.LastResult(a.ID); err != nil || got.Failure == nil || *got.Failure != *schTimeout {
		t.Errorf("LastResult(a).Failure = %+v, err %v; want %+v", got.Failure, err, schTimeout)
	}
	if got, err := s.LastSuccess(a.ID); err != nil || got.Failure != nil {
		t.Errorf("LastSuccess(a).Failure = %+v, err %v; want none", got.Failure, err)
	}
}

func TestC15_RecentIdle(t *testing.T) {
	defer schNoPanic(t)
	const farA, farB = "http://tailmeadow:3966", "http://other:3966"
	s := schOpen(t)
	a := schAddSchedule(t, s, "a")
	b := schAddSchedule(t, s, "b")
	n := 0
	add := func(id int64, farEnd string, idle float64, fail *result.Failure) {
		r := schRun(id, farEnd, n, idle, fail)
		if fail != nil {
			r.IdleMs = 777 // a decoy: only the failure marks this row out
		}
		n++
		schAdd(t, s, r)
	}
	add(a.ID, farA, 10, nil)
	add(a.ID, farB, 99, nil)
	add(a.ID, farA, 11, nil)
	add(b.ID, farA, 55, nil)
	add(a.ID, farA, 0, schTimeout)
	add(a.ID, farA, 12, nil)
	add(a.ID, farA, 13, nil)
	add(a.ID, farA, 0, schTimeout)
	add(a.ID, farA, 14, nil)

	for _, c := range []struct {
		label  string
		id     int64
		farEnd string
		n      int
		want   []float64
	}{
		{"newest three", a.ID, farA, 3, []float64{14, 13, 12}},
		{"newest four", a.ID, farA, 4, []float64{14, 13, 12, 11}},
		{"exactly all of them", a.ID, farA, 5, []float64{14, 13, 12, 11, 10}},
		{"fewer than asked for", a.ID, farA, 100, []float64{14, 13, 12, 11, 10}},
		{"another far end", a.ID, farB, 10, []float64{99}},
		{"another schedule", b.ID, farA, 10, []float64{55}},
		{"a far end it never ran against", a.ID, "http://nobody:3966", 10, nil},
		{"an unknown schedule", 999, farA, 10, nil},
	} {
		got, err := s.RecentIdle(c.id, c.farEnd, c.n)
		if err != nil || !slices.Equal(got, c.want) { // an empty result may be nil or empty
			t.Errorf("RecentIdle(%s) = %v, err %v; want %v", c.label, got, err, c.want)
		}
	}
}

func TestC16_ScheduledFieldsRoundTrip(t *testing.T) {
	defer schNoPanic(t)
	s := schOpen(t)
	sc := schAddSchedule(t, s, "tailmeadow")

	flagged := schRun(sc.ID, sc.FarEnd, 0, 40, nil)
	flagged.PathChanged = true
	plain := schRun(sc.ID, sc.FarEnd, 1, 8, nil)
	timedOut := schRun(sc.ID, sc.FarEnd, 2, 0, schTimeout)
	noPlan := schRun(sc.ID, sc.FarEnd, 3, 0, &result.Failure{Code: "unreachable", Phase: "", Message: "No connection to the far end."})
	everything := schRun(sc.ID, sc.FarEnd, 4, 0, &result.Failure{Code: "internal", Phase: "latency", Message: "The run failed inside dyno."})
	everything.PathChanged = true
	client := schClientResult(5)

	type fixture struct {
		label  string
		in     result.Result
		has    []string // which of the four optional keys its JSON has
		failed bool
	}
	fixtures := []*fixture{
		{label: "flagged success", in: flagged, has: []string{"schedule_id", "far_end", "path_changed"}},
		{label: "plain success", in: plain, has: []string{"schedule_id", "far_end"}},
		{label: "timed-out run", in: timedOut, has: []string{"schedule_id", "far_end", "failure"}, failed: true},
		{label: "run that never read the plan", in: noPlan, has: []string{"schedule_id", "far_end", "failure"}, failed: true},
		{label: "result with all four", in: everything, has: []string{"schedule_id", "far_end", "path_changed", "failure"}, failed: true},
		{label: "client result", in: client},
	}
	for _, f := range fixtures {
		f.in = schAdd(t, s, f.in)
	}

	page, err := s.List(100, 0, "")
	if err != nil || len(page) != len(fixtures) {
		t.Fatalf("List has %d results, err %v; want %d", len(page), err, len(fixtures))
	}
	listed := map[int64]result.Result{}
	for _, r := range page {
		listed[r.ID] = r
	}

	for _, f := range fixtures {
		got, err := s.Get(f.in.ID)
		if err != nil || !schResultEqual(got, f.in) {
			t.Errorf("%s: Get:\n got %+v, err %v\nwant %+v", f.label, got, err, f.in)
		}
		if l, ok := listed[f.in.ID]; !ok || !schResultEqual(l, schWithoutDetail(f.in)) {
			t.Errorf("%s: List:\n got %+v\nwant %+v", f.label, l, schWithoutDetail(f.in))
		}

		raw, err := json.Marshal(got)
		if err != nil {
			t.Fatal(err)
		}
		var obj map[string]json.RawMessage
		if err := json.Unmarshal(raw, &obj); err != nil {
			t.Fatal(err)
		}
		for _, key := range []string{"schedule_id", "far_end", "path_changed", "failure"} {
			if _, has := obj[key]; has != slices.Contains(f.has, key) {
				t.Errorf("%s: JSON has %q = %v, want %v: %s", f.label, key, has, !has, raw)
			}
		}
		if f.failed {
			var fail map[string]string
			if err := json.Unmarshal(obj["failure"], &fail); err != nil {
				t.Fatalf("%s: failure is not an object of strings: %s", f.label, obj["failure"])
			}
			if keys := slices.Sorted(maps.Keys(fail)); !slices.Equal(keys, []string{"code", "message", "phase"}) {
				t.Errorf("%s: failure has keys %v, want code, message and phase", f.label, keys)
			}
			if want := f.in.Failure; fail["code"] != want.Code || fail["phase"] != want.Phase || fail["message"] != want.Message {
				t.Errorf("%s: failure = %v, want %+v", f.label, fail, *want)
			}
		}
	}

	// A client result carries none of the scheduled fields.
	got, err := s.Get(fixtures[len(fixtures)-1].in.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.ScheduleID != 0 || got.FarEnd != "" || got.PathChanged || got.Failure != nil {
		t.Errorf("client result came back with scheduled fields: %+v", got)
	}
}

// What the first release's schema was: migrations 1 and 2 of store.go.
const (
	schMigration1 = `CREATE TABLE results (
		id                INTEGER PRIMARY KEY,
		at                INTEGER NOT NULL,
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
		detail            TEXT
	);
	CREATE INDEX results_at ON results(at);`
	schMigration2 = `ALTER TABLE results ADD COLUMN device TEXT NOT NULL DEFAULT '';
	CREATE INDEX results_device ON results(device, id);`
)

func TestC17_OpensTheDatabaseOfThePreviousRelease(t *testing.T) {
	defer schNoPanic(t)
	dir := t.TempDir()
	db, err := sql.Open("sqlite", "file:"+filepath.Join(dir, "dyno.db"))
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{schMigration1, schMigration2, `PRAGMA user_version = 2`,
		`INSERT INTO results (at, source, client, device, test_id, download_mbps, upload_mbps, idle_ms,
			loaded_down_ms, loaded_up_ms, jitter_ms, streams, client_limited, bufferbloat_ms, grade, rpm,
			server_bytes_down, server_bytes_up, detail)
			VALUES (1790000000123, 'client', 'Edge on Windows', 'kitchen-ipad', 'abc123', 880.5, 400, 8.1,
			19, 41.5, 0.8, 5, 1, 33.4, 'B', 1446, 1000, 2000, '{"rate":[[1,2,0]]}')`} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	db.Close()

	s := schOpenIn(t, dir)
	old, err := s.Get(1)
	if err != nil {
		t.Fatalf("Get of the old row: %v", err)
	}
	want := result.Result{ID: 1, At: time.UnixMilli(1790000000123).UTC(), Source: "client",
		BufferbloatMs: 33.4, Grade: "B", RPM: 1446, ServerBytesDown: 1000, ServerBytesUp: 2000}
	want.TestID, want.Client, want.Device = "abc123", "Edge on Windows", "kitchen-ipad"
	want.DownloadMbps, want.UploadMbps, want.IdleMs = 880.5, 400, 8.1
	want.LoadedDownMs, want.LoadedUpMs, want.JitterMs, want.Streams, want.ClientLimited = 19, 41.5, 0.8, 5, true
	want.Detail = []byte(`{"rate":[[1,2,0]]}`)
	if !schResultEqual(old, want) {
		t.Errorf("the old row after migrating:\n got %+v\nwant %+v", old, want)
	}
	if old.ScheduleID != 0 || old.FarEnd != "" || old.PathChanged || old.Failure != nil {
		t.Errorf("the old row has scheduled fields: %+v", old)
	}
	if raw, err := json.Marshal(old); err != nil {
		t.Fatal(err)
	} else {
		var obj map[string]json.RawMessage
		json.Unmarshal(raw, &obj)
		for _, key := range []string{"schedule_id", "far_end", "path_changed", "failure"} {
			if _, has := obj[key]; has {
				t.Errorf("the old row's JSON has %q: %s", key, raw)
			}
		}
	}
	if list, err := s.List(10, 0, ""); err != nil || len(list) != 1 || !schResultEqual(list[0], schWithoutDetail(want)) {
		t.Errorf("List after migrating = %+v, err %v", list, err)
	}

	// The new tables work on the migrated file, and the old row stays.
	sc := schAddSchedule(t, s, "tailmeadow")
	scheduled := schAdd(t, s, schRun(sc.ID, sc.FarEnd, 0, 8, nil))
	if last, err := s.LastResult(sc.ID); err != nil || last.ID != scheduled.ID {
		t.Errorf("LastResult on the migrated file = %+v, err %v", last, err)
	}
	if list, err := s.List(10, 0, ""); err != nil || len(list) != 2 {
		t.Errorf("List has %d results, err %v; want 2", len(list), err)
	}

	// Opening it a second time changes nothing.
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s = schOpenIn(t, dir)
	if list, err := s.Schedules(); err != nil || len(list) != 1 || list[0].ID != sc.ID {
		t.Errorf("Schedules() after a second open = %+v, err %v", list, err)
	}
	if got, err := s.Get(1); err != nil || !schResultEqual(got, want) {
		t.Errorf("the old row after a second open:\n got %+v, err %v", got, err)
	}
}
