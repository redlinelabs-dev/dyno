package store_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"slices"
	"testing"
	"time"

	"github.com/redlinelabs-dev/dyno/server/internal/result"
	"github.com/redlinelabs-dev/dyno/server/internal/schedule"
	"github.com/redlinelabs-dev/dyno/server/internal/settings"
	"github.com/redlinelabs-dev/dyno/server/internal/store"
)

// histT0 is where the seed data starts.
var histT0 = time.Date(2026, 11, 10, 0, 0, 0, 0, time.UTC)

// histHour is h hours after histT0.
func histHour(h int) time.Time {
	return histT0.Add(time.Duration(h) * time.Hour)
}

func histF(v float64) *float64 {
	return &v
}

func histI(v int) *int {
	return &v
}

func histOpen(t *testing.T, dir string) *store.Store {
	t.Helper()
	st, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

func histAdd(t *testing.T, st *store.Store, r result.Result) result.Result {
	t.Helper()
	got, err := st.Add(r)
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	return got
}

// histRes is a client result stored at the given time. It carries detail, so
// a Query that returned it would show.
func histRes(at time.Time, source, device string) result.Result {
	r := result.Result{At: at, Source: source}
	r.Client = "Chrome on Linux"
	r.Device = device
	r.DownloadMbps = 940
	r.UploadMbps = 400
	r.IdleMs = 8
	r.LoadedDownMs = 20
	r.LoadedUpMs = 30
	r.JitterMs = 0.5
	r.Streams = 5
	r.ClientLimited = true
	r.Detail = []byte(`{"timeline":[1,2,3]}`)
	r.ServerBytesDown = 1 << 30
	r.ServerBytesUp = 1 << 29
	r.Derive()
	return r
}

// histSched is a scheduled result for one schedule. A failed run has no
// figures and carries its failure.
func histSched(at time.Time, scheduleID int64, failed bool) result.Result {
	r := histRes(at, "schedule", "")
	r.Client = "dyno/0.1.0"
	r.ScheduleID = scheduleID
	r.FarEnd = "http://tailmeadow:3966"
	if failed {
		r.DownloadMbps, r.UploadMbps = 0, 0
		r.IdleMs, r.LoadedDownMs, r.LoadedUpMs, r.JitterMs = 0, 0, 0, 0
		r.Streams, r.ClientLimited = 0, false
		r.BufferbloatMs, r.Grade, r.RPM = 0, "", 0
		r.ServerBytesDown, r.ServerBytesUp = 0, 0
		r.Failure = &result.Failure{Code: "unreachable", Phase: "latency", Message: "No connection to the far end."}
	}
	return r
}

type histRow struct {
	source string
	device string
	sched  int64
	failed bool
}

// histSeedTen stores ten results an hour apart from histT0, with ids 1 to 10:
// client results 1 (phone), 2 (laptop), 3 (phone) and 9 (no device), then
// scheduled ones: 4, 5 and 8 for schedule 1, 6 and 7 for schedule 2 and 10 for
// schedule 3. Results 5, 7 and 10 are failed runs.
func histSeedTen(t *testing.T, st *store.Store) {
	t.Helper()
	rows := []histRow{
		{"client", "phone", 0, false},
		{"client", "laptop", 0, false},
		{"client", "phone", 0, false},
		{"schedule", "", 1, false},
		{"schedule", "", 1, true},
		{"schedule", "", 2, false},
		{"schedule", "", 2, true},
		{"schedule", "", 1, false},
		{"client", "", 0, false},
		{"schedule", "", 3, true},
	}
	for i, row := range rows {
		r := histRes(histHour(i), row.source, row.device)
		if row.source == "schedule" {
			r = histSched(histHour(i), row.sched, row.failed)
		}
		want := int64(i + 1)
		if got := histAdd(t, st, r); got.ID != want {
			t.Fatalf("seed %d got id %d, want %d", i+1, got.ID, want)
		}
	}
}

// histSeedMany stores n client results a second apart, with ids 1 to n.
func histSeedMany(t *testing.T, st *store.Store, n int) {
	t.Helper()
	for i := range n {
		at := histT0.Add(time.Duration(i) * time.Second)
		histAdd(t, st, histRes(at, "client", ""))
	}
}

func histQuery(t *testing.T, st *store.Store, f store.Filter) []result.Result {
	t.Helper()
	var got []result.Result
	err := st.Query(f, func(r result.Result) error {
		got = append(got, r)
		return nil
	})
	if err != nil {
		t.Fatalf("Query(%+v): %v", f, err)
	}
	return got
}

func histIDs(rs []result.Result) []int64 {
	ids := make([]int64, 0, len(rs))
	for _, r := range rs {
		ids = append(ids, r.ID)
	}
	return ids
}

// histRange lists the ids from first to last, counting up or down.
func histRange(first, last int64) []int64 {
	step := int64(1)
	if last < first {
		step = -1
	}
	end := last + step
	var ids []int64
	for id := first; id != end; id += step {
		ids = append(ids, id)
	}
	return ids
}

func histHead(ids []int64) []int64 {
	if len(ids) > 12 {
		return ids[:12]
	}
	return ids
}

func histCheckIDs(t *testing.T, what string, got, want []int64) {
	t.Helper()
	if slices.Equal(got, want) {
		return
	}
	if len(got) <= 20 && len(want) <= 20 {
		t.Errorf("%s: ids %v, want %v", what, got, want)
		return
	}
	t.Errorf("%s: %d ids, want %d; first %v, want first %v", what, len(got), len(want), histHead(got), histHead(want))
}

// histAddWithin adds a result and reads it back from another goroutine. A
// Query that still held the database would make this time out, where doing it
// on the caller's goroutine would deadlock the test.
func histAddWithin(st *store.Store, d time.Duration) (int64, error) {
	type outcome struct {
		id  int64
		err error
	}
	done := make(chan outcome, 1)
	go func() {
		r, err := st.Add(histRes(time.Now().UTC(), "client", "late"))
		if err == nil {
			_, err = st.Get(r.ID)
		}
		done <- outcome{r.ID, err}
	}()
	select {
	case o := <-done:
		return o.id, o.err
	case <-time.After(d):
		return 0, fmt.Errorf("Add and Get took over %v", d)
	}
}

func TestH3_SettingsOfANewStoreAreTheZeroValue(t *testing.T) {
	st := histOpen(t, t.TempDir())
	got, err := st.Settings()
	if err != nil {
		t.Fatal(err)
	}
	if got != (settings.Settings{}) {
		t.Fatalf("new store: %+v, want the zero value", got)
	}
}

func TestH3_PatchSettingsAppliesAndReturnsEverySetting(t *testing.T) {
	st := histOpen(t, t.TempDir())
	for _, step := range []struct {
		name  string
		patch settings.Patch
		want  settings.Settings
	}{
		{"all three", settings.Patch{PlanDownMbps: histF(940.5), PlanUpMbps: histF(40.25), KeepDays: histI(90)}, settings.Settings{PlanDownMbps: 940.5, PlanUpMbps: 40.25, KeepDays: 90}},
		{"keep_days alone", settings.Patch{KeepDays: histI(7)}, settings.Settings{PlanDownMbps: 940.5, PlanUpMbps: 40.25, KeepDays: 7}},
		{"plan_up_mbps alone", settings.Patch{PlanUpMbps: histF(100)}, settings.Settings{PlanDownMbps: 940.5, PlanUpMbps: 100, KeepDays: 7}},
		{"plan_down_mbps alone", settings.Patch{PlanDownMbps: histF(1000)}, settings.Settings{PlanDownMbps: 1000, PlanUpMbps: 100, KeepDays: 7}},
		{"an empty patch", settings.Patch{}, settings.Settings{PlanDownMbps: 1000, PlanUpMbps: 100, KeepDays: 7}},
		{"zeros that are sent", settings.Patch{PlanUpMbps: histF(0), KeepDays: histI(0)}, settings.Settings{PlanDownMbps: 1000}},
		{"the largest values", settings.Patch{PlanDownMbps: histF(100000), PlanUpMbps: histF(100000), KeepDays: histI(36500)}, settings.Settings{PlanDownMbps: 100000, PlanUpMbps: 100000, KeepDays: 36500}},
	} {
		got, err := st.PatchSettings(step.patch)
		if err != nil {
			t.Fatalf("%s: %v", step.name, err)
		}
		if got != step.want {
			t.Errorf("%s: PatchSettings returned %+v, want %+v", step.name, got, step.want)
		}
		read, err := st.Settings()
		if err != nil {
			t.Fatalf("%s: Settings: %v", step.name, err)
		}
		if read != step.want {
			t.Errorf("%s: Settings() = %+v, want %+v", step.name, read, step.want)
		}
	}
}

func TestH3_InvalidPatchChangesNothing(t *testing.T) {
	st := histOpen(t, t.TempDir())
	want, err := st.PatchSettings(settings.Patch{PlanDownMbps: histF(500), PlanUpMbps: histF(50), KeepDays: histI(30)})
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		name  string
		patch settings.Patch
	}{
		{"keep_days below range", settings.Patch{KeepDays: histI(-1)}},
		{"keep_days above range", settings.Patch{KeepDays: histI(36501)}},
		{"plan_down_mbps below range", settings.Patch{PlanDownMbps: histF(-1)}},
		{"plan_up_mbps above range", settings.Patch{PlanUpMbps: histF(100000.1)}},
		{"plan_down_mbps not a number", settings.Patch{PlanDownMbps: histF(math.NaN())}},
		{"valid fields beside an invalid one", settings.Patch{PlanDownMbps: histF(999), PlanUpMbps: histF(99), KeepDays: histI(-1)}},
		{"an invalid field first", settings.Patch{PlanDownMbps: histF(-5), KeepDays: histI(7)}},
	} {
		if _, err := st.PatchSettings(c.patch); err == nil {
			t.Errorf("%s: PatchSettings accepted it", c.name)
		}
		got, err := st.Settings()
		if err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Errorf("%s: settings are now %+v, want %+v", c.name, got, want)
		}
	}
}

func TestH3_SettingsSurviveReopeningTheStore(t *testing.T) {
	dir := t.TempDir()
	first := histOpen(t, dir)
	want := settings.Settings{PlanDownMbps: 940.5, PlanUpMbps: 40.25, KeepDays: 90}
	if _, err := first.PatchSettings(settings.Patch{PlanDownMbps: histF(940.5), PlanUpMbps: histF(40.25), KeepDays: histI(90)}); err != nil {
		t.Fatal(err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	second := histOpen(t, dir)
	got, err := second.Settings()
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Errorf("after reopening: %+v, want %+v", got, want)
	}
}

func TestH4_QuerySelectsAsTheFilterSays(t *testing.T) {
	st := histOpen(t, t.TempDir())
	histSeedTen(t, st)
	for _, c := range []struct {
		name string
		f    store.Filter
		want []int64
	}{
		{"zero filter: everything, newest first", store.Filter{}, histRange(10, 1)},
		{"oldest first", store.Filter{Oldest: true}, histRange(1, 10)},
		{"device phone", store.Filter{Device: "phone"}, []int64{3, 1}},
		{"device laptop", store.Filter{Device: "laptop"}, []int64{2}},
		{"a device nobody has", store.Filter{Device: "nobody"}, []int64{}},
		{"source client", store.Filter{Source: "client"}, []int64{9, 3, 2, 1}},
		{"source schedule", store.Filter{Source: "schedule"}, []int64{10, 8, 7, 6, 5, 4}},
		{"schedule 1", store.Filter{Schedule: 1}, []int64{8, 5, 4}},
		{"schedule 2", store.Filter{Schedule: 2}, []int64{7, 6}},
		{"schedule 3", store.Filter{Schedule: 3}, []int64{10}},
		{"a schedule with no results", store.Filter{Schedule: 99}, []int64{}},
		{"outcome failed", store.Filter{Outcome: "failed"}, []int64{10, 7, 5}},
		{"outcome succeeded", store.Filter{Outcome: "succeeded"}, []int64{9, 8, 6, 4, 3, 2, 1}},
		{"from, inclusive", store.Filter{From: histHour(3)}, histRange(10, 4)},
		{"to, exclusive", store.Filter{To: histHour(3)}, histRange(3, 1)},
		{"from and to", store.Filter{From: histHour(3), To: histHour(6)}, []int64{6, 5, 4}},
		{"from after everything", store.Filter{From: histHour(100)}, []int64{}},
		{"to before everything", store.Filter{To: histHour(0)}, []int64{}},
		{"before", store.Filter{Before: 5}, []int64{4, 3, 2, 1}},
		{"before the first id", store.Filter{Before: 1}, []int64{}},
		{"before beyond the last id", store.Filter{Before: 11}, histRange(10, 1)},
		{"before, oldest first", store.Filter{Before: 5, Oldest: true}, histRange(1, 4)},
		{"limit", store.Filter{Limit: 3}, []int64{10, 9, 8}},
		{"limit, oldest first", store.Filter{Limit: 3, Oldest: true}, []int64{1, 2, 3}},
		{"limit 0 is no limit", store.Filter{Limit: 0}, histRange(10, 1)},
		{"limit above the count", store.Filter{Limit: 100}, histRange(10, 1)},
		{"before and limit", store.Filter{Before: 8, Limit: 2}, []int64{7, 6}},
		{"before, limit and oldest first", store.Filter{Before: 8, Limit: 2, Oldest: true}, []int64{1, 2}},
		{"source, schedule and outcome", store.Filter{Source: "schedule", Schedule: 1, Outcome: "failed"}, []int64{5}},
		{"source, schedule and from", store.Filter{Source: "schedule", Schedule: 1, From: histHour(4)}, []int64{8, 5}},
		{"schedule and a one-hour range", store.Filter{Schedule: 2, From: histHour(5), To: histHour(6)}, []int64{6}},
		{"source, device and before", store.Filter{Source: "client", Device: "phone", Before: 3}, []int64{1}},
		{"outcome and from", store.Filter{Outcome: "failed", From: histHour(5)}, []int64{10, 7}},
		{"device and to", store.Filter{Device: "phone", To: histHour(2)}, []int64{1}},
		{"schedule, outcome and before", store.Filter{Schedule: 1, Outcome: "succeeded", Before: 8}, []int64{4}},
		{"outcome, oldest first and limit", store.Filter{Outcome: "failed", Oldest: true, Limit: 2}, []int64{5, 7}},
		{"source, outcome, range and limit", store.Filter{Source: "schedule", Outcome: "succeeded", From: histHour(3), To: histHour(8), Limit: 2}, []int64{8, 6}},
		{"the same, oldest first", store.Filter{Source: "schedule", Outcome: "succeeded", From: histHour(3), To: histHour(8), Limit: 2, Oldest: true}, []int64{4, 6}},
		{"clients can't have failed", store.Filter{Source: "client", Outcome: "failed"}, []int64{}},
		{"a device is not a schedule", store.Filter{Device: "phone", Source: "schedule"}, []int64{}},
	} {
		t.Run(c.name, func(t *testing.T) {
			got := histQuery(t, st, c.f)
			histCheckIDs(t, "Query", histIDs(got), c.want)
			for _, r := range got {
				if len(r.Detail) != 0 {
					t.Errorf("result %d carries detail %s", r.ID, r.Detail)
				}
			}
		})
	}
}

func TestH4_FromIsInclusiveAndToExclusiveToTheMillisecond(t *testing.T) {
	st := histOpen(t, t.TempDir())
	x := histHour(12)
	ms := time.Millisecond
	// Ids 1, 2 and 3 are stored at x - 1 ms, x and x + 1 ms.
	for _, at := range []time.Time{x.Add(-ms), x, x.Add(ms)} {
		histAdd(t, st, histRes(at, "client", ""))
	}
	for _, c := range []struct {
		name string
		f    store.Filter
		want []int64
	}{
		{"from x", store.Filter{From: x}, []int64{3, 2}},
		{"from x + 1 ms", store.Filter{From: x.Add(ms)}, []int64{3}},
		{"from x - 1 ms", store.Filter{From: x.Add(-ms)}, []int64{3, 2, 1}},
		{"to x", store.Filter{To: x}, []int64{1}},
		{"to x + 1 ms", store.Filter{To: x.Add(ms)}, []int64{2, 1}},
		{"to x - 1 ms", store.Filter{To: x.Add(-ms)}, []int64{}},
		{"from x to x + 1 ms", store.Filter{From: x, To: x.Add(ms)}, []int64{2}},
	} {
		histCheckIDs(t, c.name, histIDs(histQuery(t, st, c.f)), c.want)
	}
}

// histPagedResult is the i-th of a seed that is a mix: every third result is a
// scheduled run, of schedule 1, 2 or 3 in turn and failed one time in four,
// and the rest are clients on a phone or a laptop. A second apart.
func histPagedResult(i int) result.Result {
	at := histT0.Add(time.Duration(i) * time.Second)
	if i%3 == 0 {
		run := i / 3
		return histSched(at, int64(run%3+1), run%4 == 0)
	}
	device := "laptop"
	if i%2 == 0 {
		device = "phone"
	}
	return histRes(at, "client", device)
}

// histMatches is what a Filter says about a result, in Go.
func histMatches(r result.Result, f store.Filter) bool {
	if f.Device != "" && r.Device != f.Device {
		return false
	}
	if f.Source != "" && r.Source != f.Source {
		return false
	}
	if f.Schedule != 0 && r.ScheduleID != f.Schedule {
		return false
	}
	if f.Outcome == "failed" && r.Failure == nil {
		return false
	}
	if f.Outcome == "succeeded" && r.Failure != nil {
		return false
	}
	if !f.From.IsZero() && r.At.Before(f.From) {
		return false
	}
	if !f.To.IsZero() && !r.At.Before(f.To) {
		return false
	}
	return f.Before == 0 || r.ID < f.Before
}

// Filters and limits keep holding when the results run over several pages.
func TestH4_FiltersAndLimitHoldAcrossPages(t *testing.T) {
	st := histOpen(t, t.TempDir())
	const n = 1300
	stored := make([]result.Result, 0, n)
	for i := range n {
		stored = append(stored, histAdd(t, st, histPagedResult(i)))
	}
	at := func(i int) time.Time {
		return histT0.Add(time.Duration(i) * time.Second)
	}
	filters := []store.Filter{
		{},
		{Limit: 499},
		{Limit: 500},
		{Limit: 501},
		{Limit: 1000},
		{Limit: 1001},
		{Limit: 1299},
		{Limit: 5000},
		{Source: "client"},
		{Source: "client", Limit: 700},
		{Source: "schedule"},
		{Schedule: 2},
		{Schedule: 3, Limit: 100},
		{Outcome: "failed"},
		{Outcome: "succeeded", Limit: 900},
		{Device: "phone"},
		{Device: "laptop", Limit: 600},
		{From: at(250), To: at(1100)},
		{From: at(250), To: at(1100), Source: "client", Device: "phone"},
		{Before: 900},
		{Before: 1200, Limit: 600},
		{Source: "schedule", Outcome: "succeeded", From: at(100), To: at(1200), Before: 1100},
		{Source: "client", Device: "laptop", Before: 1250, From: at(300), Limit: 250},
	}
	for _, f := range filters {
		for _, oldest := range []bool{false, true} {
			f.Oldest = oldest
			var want []int64
			for _, r := range stored {
				if histMatches(r, f) {
					want = append(want, r.ID)
				}
			}
			if !oldest {
				slices.Reverse(want)
			}
			if f.Limit > 0 && len(want) > f.Limit {
				want = want[:f.Limit]
			}
			if len(want) == 0 {
				t.Fatalf("filter %+v selects nothing in the seed: the test would prove nothing", f)
			}
			histCheckIDs(t, fmt.Sprintf("%+v", f), histIDs(histQuery(t, st, f)), want)
		}
	}
}

func TestH4_QueryRowsAreTheStoredResults(t *testing.T) {
	st := histOpen(t, t.TempDir())
	histSeedTen(t, st)
	want, err := st.List(100, 0, "")
	if err != nil {
		t.Fatal(err)
	}
	got := histQuery(t, st, store.Filter{})
	if len(got) != len(want) {
		t.Fatalf("Query returned %d results, List %d", len(got), len(want))
	}
	for i := range want {
		wb, _ := json.Marshal(want[i])
		gb, _ := json.Marshal(got[i])
		if string(wb) != string(gb) {
			t.Errorf("row %d:\n Query %s\n  List %s", i, gb, wb)
		}
	}
	failed := map[int64]bool{5: true, 7: true, 10: true}
	for _, r := range got {
		switch {
		case failed[r.ID] && (r.Failure == nil || r.Failure.Code != "unreachable" || r.Failure.Phase != "latency" || r.Failure.Message != "No connection to the far end."):
			t.Errorf("result %d: failure %+v, want the one stored", r.ID, r.Failure)
		case !failed[r.ID] && r.Failure != nil:
			t.Errorf("result %d: failure %+v, want none", r.ID, r.Failure)
		}
	}
}

func TestH4_QueryOnAnEmptyStore(t *testing.T) {
	st := histOpen(t, t.TempDir())
	calls := 0
	err := st.Query(store.Filter{}, func(result.Result) error {
		calls++
		return nil
	})
	if err != nil || calls != 0 {
		t.Fatalf("Query on an empty store: err %v, %d calls", err, calls)
	}
}

func TestH5_QueryDoesNotHoldTheDatabaseWhileEachRuns(t *testing.T) {
	st := histOpen(t, t.TempDir())
	histSeedMany(t, st, 1200)

	// each parks itself on its 1st and 501st calls and hands the test the
	// channel that lets it go.
	parked := make(chan chan struct{}, 2)
	quit := make(chan struct{})
	defer close(quit)
	done := make(chan error, 1)
	go func() {
		calls := 0
		done <- st.Query(store.Filter{}, func(result.Result) error {
			calls++
			if calls == 1 || calls == 501 {
				release := make(chan struct{})
				parked <- release
				select {
				case <-release:
				case <-quit:
				}
			}
			return nil
		})
	}()

	for _, call := range []int{1, 501} {
		select {
		case release := <-parked:
			// The case says within 1 s; the bound here is looser because a loaded
			// -race run can pass 1 s with no defect. A Query holding the connection
			// blocks Add until release, so it still fails.
			if _, err := histAddWithin(st, 10*time.Second); err != nil {
				t.Errorf("while each was parked on call %d: %v", call, err)
			}
			close(release)
		case err := <-done:
			t.Fatalf("Query returned (%v) before each's call %d", err, call)
		case <-time.After(30 * time.Second):
			t.Fatalf("each's call %d never came", call)
		}
	}
	if err := <-done; err != nil {
		t.Fatalf("Query: %v", err)
	}
}

func TestH6_QuerySeesTheResultsThatExistedWhenItStarted(t *testing.T) {
	for _, oldest := range []bool{false, true} {
		name := "newest first"
		if oldest {
			name = "oldest first"
		}
		t.Run(name, func(t *testing.T) {
			st := histOpen(t, t.TempDir())
			const n = 1100
			histSeedMany(t, st, n)
			want := histRange(1, n)
			if !oldest {
				slices.Reverse(want)
			}
			var got []int64
			err := st.Query(store.Filter{Oldest: oldest}, func(r result.Result) error {
				got = append(got, r.ID)
				// Once on the first page and once on the second.
				if len(got) == 1 || len(got) == 600 {
					if _, err := histAddWithin(st, 10*time.Second); err != nil {
						t.Errorf("adding a result during call %d: %v", len(got), err)
					}
				}
				return nil
			})
			if err != nil {
				t.Fatalf("Query: %v", err)
			}
			histCheckIDs(t, "ids Query returned", got, want)
		})
	}
}

func TestH7_QueryStopsAtTheFirstErrorEachReturns(t *testing.T) {
	st := histOpen(t, t.TempDir())
	histSeedMany(t, st, 700)
	boom := errors.New("boom")
	for _, oldest := range []bool{false, true} {
		for _, stopAt := range []int{1, 3, 500, 501, 600, 700} {
			calls := 0
			err := st.Query(store.Filter{Oldest: oldest}, func(result.Result) error {
				calls++
				if calls == stopAt {
					return boom
				}
				return nil
			})
			if !errors.Is(err, boom) {
				t.Errorf("oldest=%v, error on call %d: Query returned %v", oldest, stopAt, err)
			}
			if calls != stopAt {
				t.Errorf("oldest=%v, error on call %d: each ran %d times", oldest, stopAt, calls)
			}
		}
	}
	// A Query that stopped early lets the database go: this adds a 701st result.
	if _, err := histAddWithin(st, 10*time.Second); err != nil {
		t.Errorf("after Query stopped early: %v", err)
	}
	// And one that nothing stops returns nil, after every result.
	calls := 0
	err := st.Query(store.Filter{}, func(result.Result) error {
		calls++
		return nil
	})
	if err != nil || calls != 701 {
		t.Errorf("a full Query: err %v after %d calls, want nil after 701", err, calls)
	}
}

func TestH8_PruneDeletesWhatWasStoredBeforeTAndLeavesSchedulesAlone(t *testing.T) {
	st := histOpen(t, t.TempDir())
	a, err := st.AddSchedule(schedule.Schedule{Name: "tailmeadow", FarEnd: "http://tailmeadow:3966", Kind: schedule.Light, EveryMinutes: 60})
	if err != nil {
		t.Fatal(err)
	}
	b, err := st.AddSchedule(schedule.Schedule{Name: "mirror", FarEnd: "http://mirror:3966", Kind: schedule.Full, EveryMinutes: 180})
	if err != nil {
		t.Fatal(err)
	}

	cut := histHour(2400)
	month := 30 * 24 * time.Hour
	rs := []result.Result{
		histRes(cut.Add(-month), "client", ""),
		histSched(cut.Add(-time.Hour), a.ID, true),
		histRes(cut.Add(-time.Millisecond), "client", ""),
		histRes(cut, "client", ""),
		histSched(cut.Add(time.Millisecond), b.ID, false),
		histRes(cut.Add(3*time.Hour), "client", ""),
	}
	ids := make([]int64, len(rs))
	for i, r := range rs {
		ids[i] = histAdd(t, st, r).ID
	}

	n, err := st.Prune(cut)
	if err != nil {
		t.Fatalf("Prune: %v", err)
	}
	if n != 3 {
		t.Errorf("Prune(cut) deleted %d results, want 3", n)
	}
	left, err := st.List(100, 0, "")
	if err != nil {
		t.Fatal(err)
	}
	histCheckIDs(t, "results left after Prune(cut)", histIDs(left), []int64{ids[5], ids[4], ids[3]})
	for _, id := range ids[:3] {
		if _, err := st.Get(id); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("Get(%d) after it was pruned: %v, want ErrNotFound", id, err)
		}
	}
	histCheckSchedules(t, st, a, b)

	if n, err := st.Prune(cut); err != nil || n != 0 {
		t.Errorf("a second Prune(cut): deleted %d, err %v, want 0 and nil", n, err)
	}

	// Pruning everything still leaves the schedules.
	n, err = st.Prune(cut.Add(24 * time.Hour))
	if err != nil || n != 3 {
		t.Errorf("Prune a day later: deleted %d, err %v, want 3 and nil", n, err)
	}
	if left, _ := st.List(100, 0, ""); len(left) != 0 {
		t.Errorf("%d results left after pruning them all", len(left))
	}
	histCheckSchedules(t, st, a, b)
}

func histCheckSchedules(t *testing.T, st *store.Store, a, b schedule.Schedule) {
	t.Helper()
	got, err := st.Schedules()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].ID != a.ID || got[0].Name != "tailmeadow" || got[1].ID != b.ID || got[1].Name != "mirror" {
		t.Errorf("schedules after Prune: %+v, want the two that were added", got)
	}
}
