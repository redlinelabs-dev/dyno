package scheduler_test

// Round 2 of the scheduler's cases: R2b, R8b, R10b, R10c, R11b, R16b, R17b,
// R24 and R25. A mutation run found guards that no test pinned down. Like
// scheduler_test.go, every test runs in a testing/synctest bubble whose clock
// starts at 2000-01-01 00:00:00 UTC, so "00:15" is rnT0 plus 15 minutes, and
// the Runner is the fake in fake_test.go unless a test sets its own.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/redlinelabs-dev/dyno/server/internal/nodetest"
	"github.com/redlinelabs-dev/dyno/server/internal/result"
	"github.com/redlinelabs-dev/dyno/server/internal/schedule"
	"github.com/redlinelabs-dev/dyno/server/internal/scheduler"
	"github.com/redlinelabs-dev/dyno/server/internal/store"
	"github.com/redlinelabs-dev/dyno/server/internal/testrun"

	_ "modernc.org/sqlite"
)

// ---- helpers ----

// r2NewEnvIn is rnNewEnv with the store in dir, a directory the test keeps, so
// that the test can open the database itself.
func r2NewEnvIn(t *testing.T, dir string, opts ...func(*rnEnv)) *rnEnv {
	t.Helper()
	st, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	e := &rnEnv{t: t, Store: st, Fake: rnNewFake(), Logs: &rnLogs{}}
	e.Sched = &scheduler.Scheduler{
		Store:   st,
		Test:    e.Fake.Run,
		Version: rnVersion,
		Log:     slog.New(slog.NewJSONHandler(e.Logs, nil)),
	}
	for _, o := range opts {
		o(e)
	}
	t.Cleanup(e.Stop)
	return e
}

// r2Fault is a second connection to the store's database. It makes reads of a
// table fail by renaming the table away, and back.
type r2Fault struct {
	t  *testing.T
	db *sql.DB
}

// r2OpenFault opens the connection. Call it inside the bubble, since
// database/sql starts a goroutine and the cleanup ends it before the bubble
// does.
func r2OpenFault(t *testing.T, dir string) *r2Fault {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(dir, "dyno.db"))
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Errorf("closing the second connection: %v", err)
		}
	})
	return &r2Fault{t: t, db: db}
}

func (f *r2Fault) exec(query string) {
	f.t.Helper()
	if _, err := f.db.Exec(query); err != nil {
		f.t.Fatalf("%s: %v", query, err)
	}
}

// Refuse makes every read of table fail.
func (f *r2Fault) Refuse(table string) { f.exec("ALTER TABLE " + table + " RENAME TO r2_away") }

// Allow undoes Refuse.
func (f *r2Fault) Allow(table string) { f.exec("ALTER TABLE r2_away RENAME TO " + table) }

// r2Loads returns the schedule.load events logged so far.
func r2Loads(e *rnEnv) []map[string]any {
	e.t.Helper()
	return e.Logs.Events(e.t, "schedule.load")
}

// ---- R2b, R8b: where a run stands in line ----

func TestR2b_AScheduleLearnedOfBeforeRunStartsWaitsForItsNextGridTime(t *testing.T) {
	rnBubble(t, func(t *testing.T) {
		e := rnNewEnv(t)
		a := e.Add(rnSched("a", 15))
		e.Changed() // at 00:00, before Run starts
		e.Advance(rnMin(10))
		b := rnSched("b", 15)
		b.OffsetMinutes = 12 // 00:12, 00:27, 00:42
		b = e.Add(b)
		e.Changed()
		e.Advance(rnMin(10)) // now 00:20, and dyno was down at 00:15
		e.Start()
		if calls := e.Fake.Calls(""); len(calls) != 0 {
			t.Fatalf("a run began at %v as Run started, but a time that passed while dyno was down isn't made up", calls[0].Start.Sub(rnT0))
		}
		e.GoTo(rnMin(31))
		e.WantStarts(a.FarEnd, rnMin(30))
		e.WantStarts(b.FarEnd, rnMin(27))
	})
}

func TestR8b_RunNowOnAScheduleWaitingBehindARunKeepsItsPlaceInLine(t *testing.T) {
	rnBubble(t, func(t *testing.T) {
		e := rnNewEnv(t)
		x := e.Add(rnSched("x", 15)) // runs 00:15 to 00:19
		e.Fake.On(x.FarEnd, rnOK(4*time.Minute))
		a := rnSched("a", 15)
		a.OffsetMinutes = 1 // 00:01, 00:16, 00:31
		a = e.Add(a)
		e.Fake.On(a.FarEnd, rnOK(30*time.Second))
		b := rnSched("b", 15)
		b.OffsetMinutes = 2 // 00:02, 00:17, 00:32
		b = e.Add(b)
		e.Fake.On(b.FarEnd, rnOK(30*time.Second))
		e.Advance(rnMin(3)) // dyno was down at 00:01 and 00:02, and missed times aren't made up
		e.Start()
		// x is running, and a and b came due behind it at 00:16 and 00:17.
		e.GoTo(rnMin(18))
		if got := e.Status(x.ID); got != (schedule.Status{Running: true}) {
			t.Errorf("Status(x) = %+v at 00:18, want it running", got)
		}
		for _, s := range []schedule.Schedule{a, b} {
			if got := e.Status(s.ID); got != (schedule.Status{Queued: true}) {
				t.Errorf("Status(%s) = %+v at 00:18, want it queued behind x", s.Name, got)
			}
		}
		rnRunNow(e, a.ID) // a is due and waiting already
		e.GoTo(rnMin(29))
		var order []string
		for _, c := range e.Fake.Calls("") {
			order = append(order, c.Opts.FarEnd)
		}
		if want := []string{x.FarEnd, a.FarEnd, b.FarEnd}; !slices.Equal(order, want) {
			t.Errorf("the runs went in the order %v, want %v, so a keeps its place before b and each runs once", order, want)
		}
		e.WantStarts(a.FarEnd, rnMin(19))
		e.WantStarts(b.FarEnd, rnMin(19)+30*time.Second)
	})
}

// ---- R10b, R10c: each run's wait for Busy is its own ----

func TestR10b_ARunThatStopsWaitingForBusyDoesntUseUpTheNextRunsWait(t *testing.T) {
	for _, how := range []string{"deleted", "paused"} {
		t.Run(how, func(t *testing.T) {
			rnBubble(t, func(t *testing.T) {
				e := rnNewEnv(t, rnWithBusy(func() bool { return true }))
				// a is due at 00:15, and b at 00:30.
				a := e.Add(rnSched("a", 15))
				b := e.Add(rnSched("b", 30))
				e.Start()
				e.GoTo(rnMin(16)) // a has waited for Busy since 00:15
				if got := e.Status(a.ID); got != (schedule.Status{Queued: true}) {
					t.Errorf("Status(a) = %+v at 00:16, want it queued while it waits for Busy", got)
				}
				if how == "deleted" {
					e.Delete(a.ID)
				} else {
					a.Paused = true
					a = e.Update(a)
				}
				e.Changed()
				e.GoTo(rnMin(30) + scheduler.BusyWait + 2*scheduler.BusyPoll)
				e.WantStarts(a.FarEnd)
				calls := e.Fake.Calls(b.FarEnd)
				if len(calls) != 1 {
					t.Fatalf("%d runs of b, want 1", len(calls))
				}
				if wait := calls[0].Start.Sub(rnT0) - rnMin(30); wait < scheduler.BusyWait || wait > scheduler.BusyWait+scheduler.BusyPoll {
					t.Errorf("b began %v after its time, want %v, since a's wait for Busy mustn't count towards b's", wait, scheduler.BusyWait)
				}
			})
		})
	}
}

func TestR10c_EachRunWaitsForBusyOnItsOwn(t *testing.T) {
	rnBubble(t, func(t *testing.T) {
		e := rnNewEnv(t, rnWithBusy(func() bool { return true }))
		a := e.Add(rnSched("a", 15))
		e.Fake.On(a.FarEnd, rnOK(30*time.Second))
		b := e.Add(rnSched("b", 15))
		e.Fake.On(b.FarEnd, rnOK(30*time.Second))
		e.Start()
		e.GoTo(rnMin(21))
		ac, bc := e.Fake.Calls(a.FarEnd), e.Fake.Calls(b.FarEnd)
		if len(ac) != 1 || len(bc) != 1 || !ac[0].Finished {
			t.Fatalf("runs of a %+v and of b %+v, want one each, a finished by 00:21", ac, bc)
		}
		if wait := ac[0].Start.Sub(rnT0) - rnMin(15); wait < scheduler.BusyWait || wait > scheduler.BusyWait+scheduler.BusyPoll {
			t.Errorf("a began %v after its time, want %v", wait, scheduler.BusyWait)
		}
		if wait := bc[0].Start.Sub(ac[0].End); wait < scheduler.BusyWait || wait > scheduler.BusyWait+scheduler.BusyPoll {
			t.Errorf("b began %v after a's run ended, want %v, since every run waits for Busy on its own", wait, scheduler.BusyWait)
		}
	})
}

// ---- R11b: a scheduled result has no browser test behind it ----

// r2WantSuccess fails t unless r, as read by how, is a success with the given
// download. A run that fails validation is stored as a failure, which has no
// test id or device either.
func r2WantSuccess(t *testing.T, how string, r result.Result, downMbps float64) {
	t.Helper()
	if r.Failure != nil || r.DownloadMbps != downMbps {
		t.Errorf("the result from %s has failure %+v and download %v, want no failure and %v", how, r.Failure, r.DownloadMbps, downMbps)
	}
}

func TestR11b_AScheduledResultHasNoTestIDOrDeviceWhateverTheRunnerSays(t *testing.T) {
	rnBubble(t, func(t *testing.T) {
		e := rnNewEnv(t)
		a := e.Add(rnSched("a", 60))
		// The download the fake's Outcome carries, and a test id the server would issue.
		down := rnOutcome(rnIdle).Submission.DownloadMbps
		testID := testrun.NewID()
		// A Runner that fills in what only a browser's test has.
		e.Sched.Test = func(ctx context.Context, o nodetest.Options) (nodetest.Outcome, error) {
			out, err := e.Fake.Run(ctx, o)
			out.Submission.TestID = testID
			out.Submission.Device = "kitchen-ipad"
			out.Submission.Client = "Mozilla/5.0 (X11; Linux x86_64; rv:131.0) Gecko/20100101 Firefox/131.0"
			return out, err
		}
		e.Start()
		e.GoTo(rnMin(61))
		rs := e.ResultsOf(a.ID)
		if len(rs) != 1 {
			t.Fatalf("%d results stored for one run, want 1", len(rs))
		}
		rnWantScheduled(t, rs[0], a)
		r2WantSuccess(t, "List", rs[0], down)
		full, err := e.Store.Get(rs[0].ID)
		if err != nil {
			t.Fatal(err)
		}
		rnWantScheduled(t, full, a)
		r2WantSuccess(t, "Get", full, down)
		if got, err := e.Store.List(100, 0, "kitchen-ipad"); err != nil || len(got) != 0 {
			t.Errorf("the device's history holds %d results, error %v, want none since a scheduled run isn't a device's test", len(got), err)
		}
	})
}

// ---- R16b: what an internal failure may say ----

// r2Source is how an odd text reaches the scheduler: as a panic's string, as a
// panic's error, or as an error the Runner returns.
type r2Source int

const (
	r2PanicString r2Source = iota
	r2PanicError
	r2ReturnError
)

// script is what the fake Runner does to bring text to the scheduler.
func (s r2Source) script(text string) rnScript {
	switch s {
	case r2PanicString:
		return rnPanics(0, text)
	case r2PanicError:
		return rnPanics(0, errors.New(text))
	}
	return rnFail(0, errors.New(text))
}

// r2Row is one odd text, and how it reaches the scheduler.
type r2Row struct {
	Name string
	Text string
	Src  r2Source
}

// r2Start is the printable start of every odd text. It is 121 bytes, and 240
// minus 121 is 119, which 2, 3 and 4 don't divide. So a cut at byte 240 lands
// inside a multi-byte rune of 2, 3 or 4 bytes that follows the start.
var r2Start = "disk quota exceeded: " + strings.Repeat("q", 100)

// r2Text returns start followed by chunk, repeated until the text is at least
// 10 KiB.
func r2Text(start, chunk string) string {
	var b strings.Builder
	b.WriteString(start)
	for b.Len() < 10<<10 {
		b.WriteString(chunk)
	}
	return b.String()
}

// r2BadMessage says what is wrong with an internal failure's message, or
// returns "". It must be at most 240 bytes of valid UTF-8 without control
// characters, and hold start, the printable start of its text.
func r2BadMessage(msg, start string) string {
	switch {
	case len(msg) > 240:
		return fmt.Sprintf("is %d bytes, want at most 240", len(msg))
	case !utf8.ValidString(msg):
		return fmt.Sprintf("isn't valid UTF-8: %q", msg)
	case strings.IndexFunc(msg, unicode.IsControl) >= 0:
		return fmt.Sprintf("holds a control character: %q", msg)
	case !strings.Contains(msg, start):
		return fmt.Sprintf("doesn't hold the text's printable start %q: %q", start, msg)
	}
	return ""
}

// r2Recurse calls itself n more times, then panics with v.
func r2Recurse(n int, v any) int {
	if n == 0 {
		panic(v)
	}
	return r2Recurse(n-1, v) + 1
}

func TestR16b_AnInternalMessageIsAtMost240BytesOfCleanUTF8(t *testing.T) {
	rnBubble(t, func(t *testing.T) {
		if len(r2Start) != 121 {
			t.Fatalf("the test's own start is %d bytes, want 121", len(r2Start))
		}
		// The case's text: newlines, NULs, escapes and bytes that aren't UTF-8,
		// all within the 240 bytes that the message keeps.
		listed := r2Text(r2Start, "\nsecond line\x00after a nul\x1b[31mred\x1b[0m \xff\xfe broken \xc3( cut \xe4\xb8 lone \x80 tail ")
		// Control characters the case doesn't name: DEL, NEL and another C1
		// control, tab, CR, VT, FF and BS.
		other := r2Text(r2Start, "\x7f\u0085middle\u009f\t\r\v\f\b text ")
		rows := []r2Row{
			{"the case's text, as a panic with a string", listed, r2PanicString},
			{"the case's text, as a panic with an error", listed, r2PanicError},
			{"the case's text, as a returned error", listed, r2ReturnError},
			{"2-byte runes, as a panic with a string", r2Text(r2Start, "é"), r2PanicString},
			{"3-byte runes, as a returned error", r2Text(r2Start, "世"), r2ReturnError},
			{"4-byte runes, as a panic with a string", r2Text(r2Start, "😀"), r2PanicString},
			{"other control characters, as a panic with a string", other, r2PanicString},
			{"other control characters, as a returned error", other, r2ReturnError},
		}
		e := rnNewEnv(t)
		ss := make([]schedule.Schedule, len(rows))
		for i, row := range rows {
			if len(row.Text) < 10<<10 {
				t.Fatalf("%s: the text is %d bytes, want at least 10 KiB", row.Name, len(row.Text))
			}
			ss[i] = e.Add(rnSched(fmt.Sprintf("odd%d", i), 15))
			e.Fake.On(ss[i].FarEnd, row.Src.script(row.Text))
		}
		e.Start()
		e.GoTo(rnMin(16))
		for i, row := range rows {
			rs := e.ResultsOf(ss[i].ID)
			if len(rs) != 1 {
				t.Errorf("%s: %d results, want 1", row.Name, len(rs))
				continue
			}
			f := rs[0].Failure
			if f == nil || f.Code != scheduler.CodeInternal {
				t.Errorf("%s: failure %+v, want code %q", row.Name, f, scheduler.CodeInternal)
				continue
			}
			if why := r2BadMessage(f.Message, r2Start); why != "" {
				t.Errorf("%s: the message %s", row.Name, why)
			}
		}
	})
}

func TestR16b_AStackIsCutTo4KiBHoweverDeepThePanic(t *testing.T) {
	rnBubble(t, func(t *testing.T) {
		e := rnNewEnv(t)
		a := e.Add(rnSched("a", 15))
		e.Sched.Test = func(context.Context, nodetest.Options) (nodetest.Outcome, error) {
			r2Recurse(300, "panic at the bottom of 300 calls")
			return nodetest.Outcome{}, nil
		}
		e.Start()
		e.GoTo(rnMin(16))
		events := e.Logs.Events(t, "schedule.run")
		if len(events) != 1 {
			t.Fatalf("%d schedule.run events, want 1: %v", len(events), events)
		}
		ev := events[0]
		if ev["result"] != "failed" || ev["error.code"] != scheduler.CodeInternal {
			t.Errorf("event %v, want result failed and error.code %q", ev, scheduler.CodeInternal)
		}
		stack, _ := ev["error.stack"].(string)
		if stack == "" {
			t.Fatalf("event %v has no error.stack, want one for a run that panicked", ev)
		}
		if len(stack) > 4096 {
			t.Errorf("error.stack is %d bytes, want at most 4096", len(stack))
		}
		rs := e.ResultsOf(a.ID)
		if len(rs) != 1 || rs[0].Failure == nil || rs[0].Failure.Code != scheduler.CodeInternal ||
			!strings.Contains(rs[0].Failure.Message, "panic at the bottom of 300 calls") {
			t.Errorf("results %+v, want one internal failure holding the panic's text", rs)
		}
	})
}

// ---- R17b: a run that comes due as Run stops ----

func TestR17b_ARunDueAsRunsCtxEndsDoesntStart(t *testing.T) {
	rnBubble(t, func(t *testing.T) {
		var e *rnEnv
		var asked atomic.Int32
		e = rnNewEnv(t, rnWithBusy(func() bool {
			if asked.Add(1) == 1 {
				e.Cancel() // Run's ctx ends as a's run comes due
			}
			return false // and this server isn't serving a test
		}))
		e.Add(rnSched("a", 15))
		e.Start()
		e.GoTo(rnMin(16))
		if asked.Load() == 0 {
			t.Fatal("Busy was never asked, so a's run never came due")
		}
		if !e.RunReturned() {
			t.Error("Run didn't return after its ctx ended")
		}
		if calls := e.Fake.Calls(""); len(calls) != 0 {
			t.Errorf("the Runner was called %d times, first at %v, but a run that comes due as Run's ctx ends mustn't start", len(calls), calls[0].Start.Sub(rnT0))
		}
		if rs := e.Results(); len(rs) != 0 {
			t.Errorf("%d results stored for a run that never began: %+v", len(rs), rs)
		}
		if events := e.Logs.Events(t, "schedule.run"); len(events) != 0 {
			t.Errorf("%d schedule.run events for a run that never began: %v", len(events), events)
		}
	})
}

// ---- R24, R25: reads that fail ----

// In R24 the store refuses reads of the schedules from 00:01:10 to 00:01:15.
// A change it couldn't read must be applied by 00:01:41, which is 30 s after
// the failed read plus a second of slack.
const r2ReadsFail = time.Minute + 10*time.Second
const r2ReadsHeal = time.Minute + 15*time.Second
const r2AppliedBy = time.Minute + 41*time.Second

func TestR24_AChangeThatCouldntBeReadIsAppliedWithin30sNoOtherScheduleArmed(t *testing.T) {
	rnBubble(t, func(t *testing.T) {
		dir := t.TempDir()
		e := r2NewEnvIn(t, dir, rnWithMetrics)
		fault := r2OpenFault(t, dir)
		e.Start() // an empty store
		e.GoTo(r2ReadsFail)
		a := e.Add(rnSched("a", 15))
		fault.Refuse("schedules")
		e.Changed() // the scheduler can't read a
		if len(r2Loads(e)) == 0 {
			t.Fatal("no schedule.load event, so the read of the schedules didn't fail")
		}
		e.GoTo(r2ReadsHeal)
		fault.Allow("schedules")
		e.GoTo(r2AppliedBy) // with no Changed or RunNow since
		rnWantSeries(t, e.Expo(), a.ID, rnSeries{Name: "a"})
		e.GoTo(rnMin(16))
		e.WantStarts(a.FarEnd, rnMin(15))
	})
}

func TestR24_AChangeThatCouldntBeReadIsAppliedWithin30sWithAnotherScheduleArmed(t *testing.T) {
	rnBubble(t, func(t *testing.T) {
		dir := t.TempDir()
		e := r2NewEnvIn(t, dir, rnWithMetrics)
		fault := r2OpenFault(t, dir)
		z := e.Add(rnSched("z", 15)) // armed since 00:00
		e.Start()
		rnWantSeries(t, e.Expo(), z.ID, rnSeries{Name: "z"})
		e.GoTo(r2ReadsFail)
		a := e.Add(rnSched("a", 15))
		fault.Refuse("schedules")
		e.Changed() // the scheduler can't read a
		if len(r2Loads(e)) == 0 {
			t.Fatal("no schedule.load event, so the read of the schedules didn't fail")
		}
		e.GoTo(r2ReadsHeal)
		fault.Allow("schedules")
		e.GoTo(r2AppliedBy) // with no Changed or RunNow since
		ss := e.Expo()
		rnWantSeries(t, ss, a.ID, rnSeries{Name: "a"})
		rnWantSeries(t, ss, z.ID, rnSeries{Name: "z"})
		e.GoTo(rnMin(16))
		e.WantStarts(z.FarEnd, rnMin(15))
		e.WantStarts(a.FarEnd, rnMin(15))
	})
}

func TestR24_ReadsThatKeepFailingAreRetriedAboutEvery30sNotInATightLoop(t *testing.T) {
	rnBubble(t, func(t *testing.T) {
		dir := t.TempDir()
		e := r2NewEnvIn(t, dir, rnWithMetrics)
		fault := r2OpenFault(t, dir)
		z := e.Add(rnSched("z", 15)) // armed since 00:00
		e.Start()
		e.GoTo(r2ReadsFail)
		before := len(r2Loads(e))
		a := e.Add(rnSched("a", 15))
		fault.Refuse("schedules")
		e.Changed()
		e.GoTo(r2ReadsFail + 5*time.Minute) // reads are refused until 00:06:10
		logged := len(r2Loads(e)) - before
		if logged < 1 {
			t.Fatal("no schedule.load event, so the read of the schedules didn't fail")
		}
		if logged > 12 {
			t.Errorf("%d schedule.load events in the 5 minutes that reads were refused, want at most 12, since a failed read is retried about every 30 s", logged)
		}
		fault.Allow("schedules")
		// 30 s after the last refused read, and a second.
		e.GoTo(r2ReadsFail + 5*time.Minute + 31*time.Second)
		rnWantSeries(t, e.Expo(), a.ID, rnSeries{Name: "a"})
		e.GoTo(rnMin(16))
		e.WantStarts(z.FarEnd, rnMin(15))
		e.WantStarts(a.FarEnd, rnMin(15))
	})
}

func TestR25_ALastSuccessThatCantBeReadIsLoggedAndTheSchedulesStillRun(t *testing.T) {
	rnBubble(t, func(t *testing.T) {
		dir := t.TempDir()
		e := r2NewEnvIn(t, dir, rnWithMetrics)
		fault := r2OpenFault(t, dir)
		a := e.Add(rnSched("a", 15))
		seed := e.Seed(result.Result{
			At: rnT0.Add(-time.Hour), Source: "schedule", ScheduleID: a.ID, FarEnd: a.FarEnd,
			Submission: result.Submission{Client: "seed", DownloadMbps: 300, UploadMbps: 30, IdleMs: 12, LoadedDownMs: 25, LoadedUpMs: 45, Streams: 5},
		})
		fault.Refuse("results") // Run reads a's last success as it starts
		e.Start()
		// Run has settled, so reads of the results can work again.
		fault.Allow("results")
		loads := r2Loads(e)
		if len(loads) == 0 {
			t.Fatal("no schedule.load event for the last success that couldn't be read")
		}
		for _, ev := range loads {
			msg, _ := ev["error.message"].(string)
			if ev["level"] != "ERROR" || ev["result"] != "failed" || ev["error.code"] != "store_read_failed" || msg == "" {
				t.Errorf("schedule.load event %v, want level ERROR, result failed, error.code store_read_failed and an error.message", ev)
			}
		}
		// Counting starts at 0, and there are no gauges to restore.
		rnWantSeries(t, e.Expo(), a.ID, rnSeries{Name: "a"})
		e.GoTo(rnMin(16))
		e.WantStarts(a.FarEnd, rnMin(15))
		rs := e.ResultsOf(a.ID)
		if len(rs) != 2 || rs[0].ID != seed.ID || rs[1].Client != "dyno/"+rnVersion || rs[1].Failure != nil {
			t.Fatalf("a's results %+v, want the seeded success and then the run's", rs)
		}
		rnWantSeries(t, e.Expo(), a.ID, rnSeries{Name: "a", Succeeded: 1, Last: rnGaugesOf(rs[1])})
	})
}
