package scheduler_test

// The scheduler's own guards, beside the spec tests of scheduler_test.go, on
// the harness of fake_test.go. None of these is a spec case.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/redlinelabs-dev/dyno/server/internal/nodetest"
	"github.com/redlinelabs-dev/dyno/server/internal/schedule"
	"github.com/redlinelabs-dev/dyno/server/internal/scheduler"
	"github.com/redlinelabs-dev/dyno/server/internal/store"
)

// A run stopped on purpose, or one its Runner calls canceled, logs at INFO and
// has no error keys: nobody has to act on it (ADR-0020).
func TestRx_CanceledRunsLogAtInfoWithoutErrorKeys(t *testing.T) {
	rnBubble(t, func(t *testing.T) {
		e := rnNewEnv(t)
		a := e.Add(rnSched("a", 15)) // its Runner reports canceled on its own
		e.Fake.On(a.FarEnd, rnFail(10*time.Second, rnErr(nodetest.CodeCanceled, nodetest.PhaseDownload)))
		b := e.Add(rnSched("b", 15)) // is deleted during its run
		e.Fake.On(b.FarEnd, rnOK(10*time.Minute))
		c := e.Add(rnSched("c", 15)) // is running when Run's ctx ends
		e.Fake.On(c.FarEnd, rnOK(10*time.Minute))
		e.Start()
		e.GoTo(rnMin(18))
		e.Delete(b.ID)
		e.Changed()
		e.GoTo(rnMin(19)) // c's run began as b's was stopped
		e.Cancel()
		e.Stop()
		events := e.Logs.Events(t, "schedule.run")
		if len(events) != 3 {
			t.Fatalf("%d schedule.run events, want 3: %v", len(events), events)
		}
		for _, ev := range events {
			if ev["result"] != "canceled" || ev["level"] != "INFO" {
				t.Errorf("event %v, want result canceled at INFO", ev)
			}
			for _, key := range []string{"error.code", "error.message", "error.stack"} {
				if v, ok := ev[key]; ok {
					t.Errorf("a canceled run has %s %v", key, v)
				}
			}
		}
	})
}

// A run waiting for Busy, and one asked for before Run started, are queued.
func TestRx_StatusIsQueuedBeforeRunStartsAndWhileWaitingForBusy(t *testing.T) {
	rnBubble(t, func(t *testing.T) {
		var busy atomic.Bool
		busy.Store(true)
		e := rnNewEnv(t, rnWithBusy(busy.Load))
		s := rnSched("a", 1440)
		s.OffsetMinutes, s.Paused = 600, true
		a := e.Add(s)
		e.Fake.On(a.FarEnd, rnOK(30*time.Second))
		queued, running, none := schedule.Status{Queued: true}, schedule.Status{Running: true}, schedule.Status{}

		rnRunNow(e, a.ID) // before Run starts
		if got := e.Status(a.ID); got != queued {
			t.Errorf("before Run started: Status = %+v, want %+v", got, queued)
		}
		e.Start()
		e.GoTo(rnMin(1)) // Busy has held the run back since Start
		if n := len(e.Fake.Calls("")); n != 0 {
			t.Fatalf("the Runner was called %d times while Busy was true", n)
		}
		if got := e.Status(a.ID); got != queued {
			t.Errorf("waiting for Busy: Status = %+v, want %+v", got, queued)
		}
		busy.Store(false)
		e.Advance(2 * scheduler.BusyPoll)
		if got := e.Status(a.ID); got != running {
			t.Errorf("after Busy ended: Status = %+v, want %+v", got, running)
		}
		e.Advance(time.Minute)
		if got := e.Status(a.ID); got != none {
			t.Errorf("after the run: Status = %+v, want %+v", got, none)
		}
	})
}

// A schedule whose time comes during another's run is queued from then, not
// from the moment the first run ends.
func TestRx_StatusIsQueuedForAScheduleDueBehindALongRun(t *testing.T) {
	rnBubble(t, func(t *testing.T) {
		e := rnNewEnv(t)
		x := e.Add(rnSched("x", 15)) // due at 00:15, and runs for 4 minutes
		e.Fake.On(x.FarEnd, rnOK(4*time.Minute))
		s := rnSched("y", 60)
		s.OffsetMinutes = 16 // due at 00:16, behind x's run, and next at 01:16
		y := e.Add(s)
		e.Fake.On(y.FarEnd, rnOK(time.Minute))
		e.Start()
		e.GoTo(rnMin(17))
		if got := e.Status(x.ID); got != (schedule.Status{Running: true}) {
			t.Errorf("Status(x) = %+v, want running", got)
		}
		if got := e.Status(y.ID); got != (schedule.Status{Queued: true}) {
			t.Errorf("Status(y) = %+v, want queued", got)
		}
		e.GoTo(rnMin(21))
		e.WantStarts(y.FarEnd, rnMin(19)) // as x's run ended
	})
}

// A run asked for on a paused schedule stays queued when another schedule is
// edited: only pausing a schedule drops its queued run.
func TestRx_ChangeElsewhereKeepsARunAskedForOfAPausedSchedule(t *testing.T) {
	rnBubble(t, func(t *testing.T) {
		e := rnNewEnv(t)
		x := e.Add(rnSched("x", 15))
		e.Fake.On(x.FarEnd, rnOK(4*time.Minute))
		s := rnSched("p", 1440)
		s.OffsetMinutes, s.Paused = 600, true
		p := e.Add(s)
		e.Fake.On(p.FarEnd, rnOK(time.Minute))
		e.Start()
		e.GoTo(rnMin(16)) // x has been running since 00:15
		rnRunNow(e, p.ID) // p waits behind it
		e.GoTo(rnMin(17))
		x.Name = "x, renamed"
		e.Update(x)
		e.Changed()
		if got := e.Status(p.ID); got != (schedule.Status{Queued: true}) {
			t.Errorf("Status(p) = %+v after an edit of x, want queued", got)
		}
		e.GoTo(rnMin(21))
		e.WantStarts(p.FarEnd, rnMin(19)) // as x's run ended
	})
}

// A Runner that returns figures no result can hold is a bug, and it must not
// reach the database: a NaN there would break every read of the results.
func TestRx_FiguresThatCantBeStoredAreAnInternalFailure(t *testing.T) {
	rnBubble(t, func(t *testing.T) {
		e := rnNewEnv(t)
		nan := rnOutcome(rnIdle)
		nan.Submission.IdleMs = math.NaN()
		empty := e.Add(rnSched("empty", 15)) // returns no error and no measurement
		e.Fake.On(empty.FarEnd, rnScript{Take: time.Second})
		notNumber := e.Add(rnSched("nan", 15))
		e.Fake.On(notNumber.FarEnd, rnScript{Take: time.Second, Out: nan})
		e.Start()
		e.GoTo(rnMin(31))
		for _, s := range []schedule.Schedule{empty, notNumber} {
			rs := e.ResultsOf(s.ID)
			if len(rs) != 2 { // the next run was on time
				t.Errorf("%s: %d results, want 2", s.Name, len(rs))
				continue
			}
			for _, r := range rs {
				rnWantScheduled(t, r, s)
				if msg := rnNoMeasurements(r); msg != "" {
					t.Errorf("%s: %s", s.Name, msg)
				}
				if r.Failure == nil || r.Failure.Code != scheduler.CodeInternal {
					t.Errorf("%s: failure %+v, want code %q", s.Name, r.Failure, scheduler.CodeInternal)
				}
			}
		}
		for _, ev := range e.Logs.Events(t, "schedule.run") {
			if ev["level"] != "ERROR" || ev["result"] != "failed" {
				t.Errorf("event %v, want a failure at ERROR", ev)
			}
		}
	})
}

// A detail that isn't JSON would make the stored result impossible to marshal,
// so it is an internal failure like any other figures that can't be stored
// (R16c).
func TestRx_ADetailThatIsNotJSONIsAnInternalFailure(t *testing.T) {
	rnBubble(t, func(t *testing.T) {
		e := rnNewEnv(t)
		bad := rnOutcome(rnIdle)
		bad.Submission.Detail = json.RawMessage("{not json")
		a := e.Add(rnSched("a", 60))
		e.Fake.On(a.FarEnd, rnScript{Take: time.Second, Out: bad})
		e.Start()
		e.GoTo(rnMin(61))
		rs := e.ResultsOf(a.ID)
		if len(rs) != 1 {
			t.Fatalf("%d results, want 1", len(rs))
		}
		// List never returns Detail, so read the row back with Get.
		r, err := e.Store.Get(rs[0].ID)
		if err != nil {
			t.Fatal(err)
		}
		if r.Failure == nil || r.Failure.Code != scheduler.CodeInternal {
			t.Errorf("failure %+v, want code %q", r.Failure, scheduler.CodeInternal)
		}
		if msg := rnNoMeasurements(r); msg != "" {
			t.Error(msg)
		}
		if len(r.Detail) != 0 {
			t.Errorf("detail %q stored, want none", r.Detail)
		}
		if _, err := json.Marshal(r); err != nil {
			t.Errorf("the stored result doesn't marshal: %v", err)
		}
	})
}

// A detail with bytes that aren't UTF-8 inside a string passes json.Valid but
// would be served as invalid JSON, so it is an internal failure too (R16c,
// B30).
func TestRx_ADetailThatIsNotUTF8IsAnInternalFailure(t *testing.T) {
	rnBubble(t, func(t *testing.T) {
		e := rnNewEnv(t)
		bad := rnOutcome(rnIdle)
		bad.Submission.Detail = json.RawMessage("{\"x\":\"a\xff\xfeb\"}")
		a := e.Add(rnSched("a", 60))
		e.Fake.On(a.FarEnd, rnScript{Take: time.Second, Out: bad})
		e.Start()
		e.GoTo(rnMin(61))
		rs := e.ResultsOf(a.ID)
		if len(rs) != 1 {
			t.Fatalf("%d results, want 1", len(rs))
		}
		// List never returns Detail, so read the row back with Get.
		r, err := e.Store.Get(rs[0].ID)
		if err != nil {
			t.Fatal(err)
		}
		if r.Failure == nil || r.Failure.Code != scheduler.CodeInternal {
			t.Errorf("failure %+v, want code %q", r.Failure, scheduler.CodeInternal)
		}
		if msg := rnNoMeasurements(r); msg != "" {
			t.Error(msg)
		}
		if len(r.Detail) != 0 {
			t.Errorf("detail %q stored, want none", r.Detail)
		}
		if _, err := json.Marshal(r); err != nil {
			t.Errorf("the stored result doesn't marshal: %v", err)
		}
	})
}

// rxBrokenError is an error whose Error or Unwrap panics when the scheduler
// reads it.
type rxBrokenError struct{ onError, onUnwrap bool }

func (b rxBrokenError) Error() string {
	if b.onError {
		panic("Error exploded")
	}
	return "broken"
}

func (b rxBrokenError) Unwrap() error {
	if b.onUnwrap {
		panic("Unwrap exploded")
	}
	return nil
}

// An error the Runner returns can itself panic when the scheduler reads it: a
// typed-nil *nodetest.Error, or an Error or Unwrap that panics. That runs in
// Run's goroutine, outside the Runner's recover, so it must be an internal
// failure and not the end of the process (R16d, B26).
func TestRx_AnErrorThatPanicsWhenReadIsAnInternalFailure(t *testing.T) {
	rnBubble(t, func(t *testing.T) {
		e := rnNewEnv(t)
		var typedNil *nodetest.Error
		rows := []struct {
			name string
			err  error
		}{
			{"typed-nil *nodetest.Error", typedNil},
			{"Error panics", rxBrokenError{onError: true}},
			{"Unwrap panics", rxBrokenError{onUnwrap: true}},
		}
		ss := make([]schedule.Schedule, len(rows))
		for i, row := range rows {
			ss[i] = e.Add(rnSched(fmt.Sprintf("broken%d", i), 15))
			e.Fake.On(ss[i].FarEnd, rnFail(0, row.err))
		}
		ok := e.Add(rnSched("ok", 15))
		e.Start()
		e.GoTo(rnMin(16))
		if e.RunReturned() {
			t.Fatal("Run returned")
		}
		for i, row := range rows {
			rs := e.ResultsOf(ss[i].ID)
			if len(rs) != 1 {
				t.Errorf("%s: %d results, want 1", row.name, len(rs))
				continue
			}
			r := rs[0]
			rnWantScheduled(t, r, ss[i])
			if msg := rnNoMeasurements(r); msg != "" {
				t.Errorf("%s: %s", row.name, msg)
			}
			if r.Failure == nil || r.Failure.Code != scheduler.CodeInternal {
				t.Errorf("%s: failure %+v, want code %q", row.name, r.Failure, scheduler.CodeInternal)
				continue
			}
			if why := r2BadMessage(r.Failure.Message, ""); why != "" {
				t.Errorf("%s: the message %s", row.name, why)
			}
		}
		if n := len(e.ResultsOf(ok.ID)); n != 1 {
			t.Errorf("ok: %d results, want 1: the scheduler stopped working", n)
		}
		events := e.Logs.Events(t, "schedule.run")
		if len(events) != len(rows)+1 {
			t.Errorf("%d schedule.run events, want %d", len(events), len(rows)+1)
		}
		e.GoTo(rnMin(31)) // the next round: every schedule still runs
		for i, row := range rows {
			if n := len(e.ResultsOf(ss[i].ID)); n != 2 {
				t.Errorf("%s: %d results after the second round, want 2", row.name, n)
			}
		}
		if n := len(e.ResultsOf(ok.ID)); n != 2 {
			t.Errorf("ok: %d results after the second round, want 2", n)
		}
	})
}

// The store reads a result with no failure code as a success, so a failure
// without one must never reach it as it is.
func TestRx_AFailureWithoutACodeIsInternalAndNotASuccess(t *testing.T) {
	rnBubble(t, func(t *testing.T) {
		e := rnNewEnv(t)
		a := e.Add(rnSched("a", 60))
		e.Fake.On(a.FarEnd, rnFail(time.Second, &nodetest.Error{Phase: nodetest.PhaseLatency, Err: errors.New("no code")}))
		e.Start()
		e.GoTo(rnMin(61))
		rs := e.ResultsOf(a.ID)
		if len(rs) != 1 || rs[0].Failure == nil || rs[0].Failure.Code != scheduler.CodeInternal {
			t.Fatalf("results %+v, want one failure with code %q", rs, scheduler.CodeInternal)
		}
		if _, err := e.Store.LastSuccess(a.ID); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("LastSuccess = %v, want ErrNotFound: the failure was stored as a success", err)
		}
	})
}

// A kind that is neither light nor full, which only a row edited by hand can
// hold, runs the cheaper test.
func TestRx_AKindThatIsNeitherLightNorFullRunsLight(t *testing.T) {
	rnBubble(t, func(t *testing.T) {
		e := rnNewEnv(t)
		var ss []schedule.Schedule
		for i, kind := range []string{"", "bogus"} {
			s := rnSched(fmt.Sprintf("kind%d", i), 60)
			s.Kind = kind
			ss = append(ss, e.Add(s))
		}
		e.Start()
		e.GoTo(rnMin(61))
		for _, s := range ss {
			calls := e.Fake.Calls(s.FarEnd)
			if len(calls) != 1 || calls[0].Opts.Kind != nodetest.Light {
				t.Errorf("kind %q: calls %+v, want one run of kind %q", s.Kind, calls, nodetest.Light)
			}
		}
		events := e.Logs.Events(t, "schedule.run")
		if len(events) != len(ss) {
			t.Fatalf("%d schedule.run events, want %d", len(events), len(ss))
		}
		for _, ev := range events {
			if ev["kind"] != schedule.Light {
				t.Errorf("event %v, want kind light", ev)
			}
		}
	})
}

// A run that hits its deadline is a timeout, whatever it returns: an error at
// the deadline or after it, or a success after it. A success at the very
// instant of the deadline used its 5 minutes and no more, and is kept.
func TestRx_ARunThatHitsItsDeadlineIsATimeoutWhateverItReturned(t *testing.T) {
	rnBubble(t, func(t *testing.T) {
		e := rnNewEnv(t)
		late := scheduler.RunTimeout + time.Minute // the Runner doesn't notice that its ctx ended
		cases := []struct {
			name   string
			script rnScript
			code   string
			phase  string
		}{
			{"another far-end code", rnScript{Take: late, Ignore: true, Err: rnErr(nodetest.CodeUnreachable, nodetest.PhaseDownload)}, nodetest.CodeTimeout, "download"},
			{"a plain error", rnScript{Take: late, Ignore: true, Err: errors.New("plain")}, nodetest.CodeTimeout, ""},
			{"canceled", rnScript{Take: late, Ignore: true, Err: rnErr(nodetest.CodeCanceled, nodetest.PhaseUpload)}, nodetest.CodeTimeout, "upload"},
			{"an error at the deadline", rnScript{Take: scheduler.RunTimeout, Ignore: true, Err: errors.New("plain")}, nodetest.CodeTimeout, ""},
			{"a success after it", rnScript{Take: late, Ignore: true, Out: rnOutcome(rnIdle)}, nodetest.CodeTimeout, ""},
			{"a success at it", rnScript{Take: scheduler.RunTimeout, Ignore: true, Out: rnOutcome(rnIdle)}, "", ""},
		}
		ss := make([]schedule.Schedule, len(cases))
		for i, c := range cases {
			s := rnSched(fmt.Sprintf("late%d", i), 1440)
			s.OffsetMinutes = 60 // once a day, at 01:00
			ss[i] = e.Add(s)
			e.Fake.On(ss[i].FarEnd, c.script)
		}
		e.Start()
		e.GoTo(2 * time.Hour)
		for i, c := range cases {
			rs := e.ResultsOf(ss[i].ID)
			if len(rs) != 1 {
				t.Errorf("%s: %d results, want 1", c.name, len(rs))
				continue
			}
			f := rs[0].Failure
			switch {
			case c.code == "" && f != nil:
				t.Errorf("%s: stored as the failure %+v, want a success", c.name, *f)
			case c.code != "" && (f == nil || f.Code != c.code || f.Phase != c.phase || f.Message == ""):
				t.Errorf("%s: failure %+v, want code %q, phase %q and a message", c.name, f, c.code, c.phase)
			}
		}
	})
}

// A panic is the one failure whose log event carries a stack, since "the test
// panicked" alone says nothing about where.
func TestRx_OnlyAPanicLogsAStack(t *testing.T) {
	rnBubble(t, func(t *testing.T) {
		e := rnNewEnv(t)
		boom := e.Add(rnSched("boom", 60))
		e.Fake.On(boom.FarEnd, rnPanics(time.Second, "kaboom"))
		down := e.Add(rnSched("down", 60))
		e.Fake.On(down.FarEnd, rnFail(time.Second, rnErr(nodetest.CodeUnreachable, "")))
		e.Start()
		e.GoTo(rnMin(62))
		events := e.Logs.Events(t, "schedule.run")
		if len(events) != 2 {
			t.Fatalf("%d schedule.run events, want 2", len(events))
		}
		for _, ev := range events {
			id, _ := rnNumber(ev["schedule_id"])
			stack, has := ev["error.stack"].(string)
			switch int64(id) {
			case boom.ID:
				if !has || !strings.Contains(stack, "goroutine") {
					t.Errorf("the panic's event has stack %q, want a goroutine trace", stack)
				}
			case down.ID:
				if has {
					t.Errorf("a far-end failure carries a stack: %q", stack)
				}
			}
		}
	})
}

// A second Run, while one is going, must not start a second loop: two would
// run two tests at once.
func TestRx_ASecondRunReturnsAtOnce(t *testing.T) {
	rnBubble(t, func(t *testing.T) {
		e := rnNewEnv(t)
		a := e.Add(rnSched("a", 15))
		e.Start()
		second := make(chan struct{})
		go func() {
			defer close(second)
			e.Sched.Run(context.Background())
		}()
		e.Settle()
		select {
		case <-second:
		default:
			t.Fatal("a second Run didn't return at once")
		}
		e.Advance(rnMin(16))
		e.WantStarts(a.FarEnd, rnMin(15))
	})
}

// A read of the schedules that fails is logged, since the schedules in use
// may be out of date, and it neither blocks nor panics.
func TestRx_AFailedReadOfTheSchedulesIsLogged(t *testing.T) {
	rnBubble(t, func(t *testing.T) {
		e := rnNewEnv(t)
		a := e.Add(rnSched("a", 15))
		e.Start()
		e.GoTo(rnMin(1))
		if err := e.Store.Close(); err != nil { // the database goes away
			t.Fatal(err)
		}
		e.Changed()
		if err := e.RunNow(a.ID); err == nil || errors.Is(err, store.ErrNotFound) {
			t.Errorf("RunNow = %v, want the read error", err)
		}
		events := e.Logs.Events(t, "schedule.load")
		if len(events) == 0 {
			t.Fatal("no schedule.load event for a read that failed")
		}
		for _, ev := range events {
			if ev["level"] != "ERROR" || ev["result"] != "failed" || ev["error.code"] != "store_read_failed" {
				t.Errorf("event %v, want a store_read_failed failure at ERROR", ev)
			}
		}
	})
}

// A run whose result can't be written is logged at ERROR and isn't counted:
// the metrics read the same as the database.
func TestRx_ARunThatCantBeStoredIsLoggedAndNotCounted(t *testing.T) {
	rnBubble(t, func(t *testing.T) {
		e := rnNewEnv(t, rnWithMetrics)
		a := e.Add(rnSched("a", 15))
		e.Fake.On(a.FarEnd, rnOK(10*time.Second))
		e.Start()
		e.GoTo(rnMin(15) + 5*time.Second) // the run is going
		if err := e.Store.Close(); err != nil {
			t.Fatal(err)
		}
		e.GoTo(rnMin(16))
		events := e.Logs.Events(t, "schedule.run")
		if len(events) != 1 {
			t.Fatalf("%d schedule.run events, want 1: %v", len(events), events)
		}
		ev := events[0]
		if ev["result"] != "failed" || ev["level"] != "ERROR" || ev["error.code"] != "store_write_failed" {
			t.Errorf("event %v, want a store_write_failed failure at ERROR", ev)
		}
		rnWantSeries(t, e.Expo(), a.ID, rnSeries{Name: "a"})
	})
}
