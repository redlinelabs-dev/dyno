package scheduler_test

// Cases R1 to R22 of the scheduler (ADR-0028, ADR-0029, docs/api.md
// "Schedules"). Every test runs in a testing/synctest bubble, whose clock
// starts at 2000-01-01 00:00:00 UTC, so "00:15" below is rnT0 plus 15 minutes.
// The Runner is the fake in fake_test.go.

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/redlinelabs-dev/dyno/server/internal/nodetest"
	"github.com/redlinelabs-dev/dyno/server/internal/result"
	"github.com/redlinelabs-dev/dyno/server/internal/schedule"
	"github.com/redlinelabs-dev/dyno/server/internal/scheduler"
	"github.com/redlinelabs-dev/dyno/server/internal/store"
)

// ---- helpers ----

// rnRunNow calls RunNow and fails t unless it returns nil.
func rnRunNow(e *rnEnv, id int64) {
	e.t.Helper()
	if err := e.RunNow(id); err != nil {
		e.t.Errorf("RunNow(%d): %v", id, err)
	}
}

// rnNear reports whether got is within rnSlack of want.
func rnNear(got, want time.Duration) bool {
	d := got - want
	return d >= -rnSlack && d <= rnSlack
}

// rnFirstCall returns the Runner's first call for farEnd, and ends the test if
// there was none.
func rnFirstCall(e *rnEnv, farEnd string) rnCall {
	e.t.Helper()
	calls := e.Fake.Calls(farEnd)
	if len(calls) == 0 {
		e.t.Fatalf("the Runner was never called for %s", farEnd)
	}
	return calls[0]
}

// rnOffsets returns where each call began, as offsets from rnT0.
func rnOffsets(calls []rnCall) []time.Duration {
	var out []time.Duration
	for _, c := range calls {
		out = append(out, c.Start.Sub(rnT0))
	}
	return out
}

// rnNoOverlap describes the first call that began before the one before it
// ended, or returns "".
func rnNoOverlap(calls []rnCall) string {
	for i := 1; i < len(calls); i++ {
		prev, c := calls[i-1], calls[i]
		if !prev.Finished || c.Start.Before(prev.End) {
			return fmt.Sprintf("call %d began at %v while call %d was still going", c.Seq, c.Start.Sub(rnT0), prev.Seq)
		}
	}
	return ""
}

// rnNoMeasurements describes what's wrong if r, the result of a failed run,
// holds a speed, a latency or a grade, or returns "".
func rnNoMeasurements(r result.Result) string {
	s := r.Submission
	if s.DownloadMbps != 0 || s.UploadMbps != 0 || s.IdleMs != 0 || s.LoadedDownMs != 0 || s.LoadedUpMs != 0 || s.JitterMs != 0 {
		return fmt.Sprintf("a failed run has speeds or latencies: down %v, up %v, idle %v, loaded %v and %v, jitter %v",
			s.DownloadMbps, s.UploadMbps, s.IdleMs, s.LoadedDownMs, s.LoadedUpMs, s.JitterMs)
	}
	if r.Grade != "" || r.BufferbloatMs != 0 || r.RPM != 0 {
		return fmt.Sprintf("a failed run has a grade %q, bufferbloat %v ms and %d rpm", r.Grade, r.BufferbloatMs, r.RPM)
	}
	return ""
}

// rnWantScheduled checks the fields that every stored result of schedule s
// has, failed or not (R11).
func rnWantScheduled(t *testing.T, r result.Result, s schedule.Schedule) {
	t.Helper()
	if r.Source != "schedule" {
		t.Errorf("source %q, want schedule", r.Source)
	}
	if r.Client != "dyno/"+rnVersion {
		t.Errorf("client %q, want %q", r.Client, "dyno/"+rnVersion)
	}
	if r.Device != "" || r.TestID != "" {
		t.Errorf("device %q and test_id %q, want both empty", r.Device, r.TestID)
	}
	if r.ScheduleID != s.ID || r.FarEnd != s.FarEnd {
		t.Errorf("schedule_id %d and far_end %q, want %d and %q", r.ScheduleID, r.FarEnd, s.ID, s.FarEnd)
	}
}

// ---- R1, R2: when runs happen ----

func TestR1_RunsAtEachGridTime(t *testing.T) {
	rnBubble(t, func(t *testing.T) {
		e := rnNewEnv(t)
		a := e.Add(rnSched("a", 15))
		e.Start()
		e.Advance(time.Hour + 30*time.Second)
		e.WantStarts(a.FarEnd, rnMin(15), rnMin(30), rnMin(45), rnMin(60))
		if n := len(e.ResultsOf(a.ID)); n != 4 {
			t.Errorf("%d results stored for 4 runs", n)
		}
	})
}

func TestR1_OffsetsShiftTheGrid(t *testing.T) {
	rnBubble(t, func(t *testing.T) {
		e := rnNewEnv(t)
		b := rnSched("b", 30)
		b.OffsetMinutes = 7
		b = e.Add(b)
		c := rnSched("c", 60)
		c.OffsetMinutes = 45
		c = e.Add(c)
		e.Start()
		e.Advance(2*time.Hour + 30*time.Second)
		e.WantStarts(b.FarEnd, rnMin(7), rnMin(37), rnMin(67), rnMin(97))
		e.WantStarts(c.FarEnd, rnMin(45), rnMin(105))
	})
}

func TestR2_MissedRunsAreNotMadeUp(t *testing.T) {
	rnBubble(t, func(t *testing.T) {
		e := rnNewEnv(t)
		a := e.Add(rnSched("a", 15))
		e.Advance(rnMin(20)) // dyno was down at 00:15
		e.Start()
		e.GoTo(rnMin(46))
		e.WantStarts(a.FarEnd, rnMin(30), rnMin(45))
	})
}

func TestR2_AfterALongStopThereIsNoBurst(t *testing.T) {
	rnBubble(t, func(t *testing.T) {
		e := rnNewEnv(t)
		a := e.Add(rnSched("a", 15))
		e.Advance(5*time.Hour + rnMin(50))
		e.Start()
		e.Advance(rnMin(30))
		e.WantStarts(a.FarEnd, 6*time.Hour, 6*time.Hour+rnMin(15))
	})
}

// ---- R3, R4: one run at a time ----

func TestR3_OneRunAtATimeLowerIDFirst(t *testing.T) {
	rnBubble(t, func(t *testing.T) {
		e := rnNewEnv(t)
		var ss []schedule.Schedule
		for _, name := range []string{"c", "b", "a"} { // the ids rise as the names fall
			s := e.Add(rnSched(name, 15))
			e.Fake.On(s.FarEnd, rnOK(30*time.Second))
			ss = append(ss, s)
		}
		e.Start()
		e.GoTo(rnMin(32))
		for i, s := range ss {
			turn := time.Duration(i) * 30 * time.Second
			e.WantStarts(s.FarEnd, rnMin(15)+turn, rnMin(30)+turn)
		}
		if n := e.Fake.MaxInFlight(); n != 1 {
			t.Errorf("%d runs at once, want 1", n)
		}
		if msg := rnNoOverlap(e.Fake.Calls("")); msg != "" {
			t.Error(msg)
		}
	})
}

func TestR4_NoPileUpWhileRunning(t *testing.T) {
	rnBubble(t, func(t *testing.T) {
		e := rnNewEnv(t)
		a := e.Add(rnSched("a", 15))
		e.Fake.On(a.FarEnd, rnOK(rnMin(40)))
		e.Start()
		e.GoTo(3 * time.Hour)
		// 00:30 and 00:45 pass during the first run, which ends at 00:55. They
		// aren't made up: the next run is at the first time after the end.
		e.WantStarts(a.FarEnd, rnMin(15), rnMin(60), rnMin(105), rnMin(150))
	})
}

func TestR4_NoPileUpWhileQueued(t *testing.T) {
	rnBubble(t, func(t *testing.T) {
		e := rnNewEnv(t)
		a := e.Add(rnSched("a", 15))
		e.Fake.On(a.FarEnd, rnOK(rnMin(40)))
		b := e.Add(rnSched("b", 15))
		e.Fake.On(b.FarEnd, rnOK(time.Minute))
		e.Start()
		e.GoTo(3 * time.Hour)
		// b waits behind a from 00:15. The grid times that pass meanwhile don't
		// queue it again: it runs once after each of a's runs.
		e.WantStarts(a.FarEnd, rnMin(15), rnMin(60), rnMin(105), rnMin(150))
		e.WantStarts(b.FarEnd, rnMin(55), rnMin(100), rnMin(145))
		if msg := rnNoOverlap(e.Fake.Calls("")); msg != "" {
			t.Error(msg)
		}
	})
}

func TestR4_SixteenSchedulesNeverOverlapAndNoneRunsTwiceInARow(t *testing.T) {
	rnBubble(t, func(t *testing.T) {
		e := rnNewEnv(t)
		var ss []schedule.Schedule
		for i := 1; i <= 16; i++ {
			s := e.Add(rnSched(fmt.Sprintf("s%02d", i), 15))
			e.Fake.On(s.FarEnd, rnOK(4*time.Minute+30*time.Second))
			ss = append(ss, s)
		}
		e.Start()
		e.GoTo(3 * time.Hour)
		calls := e.Fake.Calls("")
		if n := e.Fake.MaxInFlight(); n != 1 {
			t.Errorf("%d runs at once, want 1", n)
		}
		if msg := rnNoOverlap(calls); msg != "" {
			t.Error(msg)
		}
		// All 16 are due at 00:15 and take turns, lowest id first.
		for i := 0; i < len(ss) && i < len(calls); i++ {
			if got := calls[i].Opts.FarEnd; got != ss[i].FarEnd {
				t.Errorf("call %d was for %s, want %s", i+1, got, ss[i].FarEnd)
			}
		}
		// The queue never empties, so each run begins as the one before it
		// ends, and no schedule goes twice in a row.
		for i := 1; i < len(calls); i++ {
			if calls[i].Opts.FarEnd == calls[i-1].Opts.FarEnd {
				t.Errorf("%s ran twice in a row, calls %d and %d", calls[i].Opts.FarEnd, i, i+1)
			}
			if gap := calls[i].Start.Sub(calls[i-1].End); gap > rnSlack {
				t.Errorf("call %d began %v after call %d ended, with the queue full", i+1, gap, i)
			}
		}
		// The first run begins at 00:15 and one follows every 4m30s: the 37th
		// begins at 02:57, and 36 runs have ended by 03:00.
		if len(calls) != 37 {
			t.Errorf("%d runs began by 03:00, want 37", len(calls))
		}
		if n := len(e.Results()); n != 36 {
			t.Errorf("%d results stored, want 36", n)
		}
	})
}

// ---- R5: Changed applies at once ----

func TestR5_AddedScheduleRunsAtItsNextGridTime(t *testing.T) {
	rnBubble(t, func(t *testing.T) {
		e := rnNewEnv(t)
		a := e.Add(rnSched("a", 60))
		e.Start()
		e.GoTo(rnMin(20))
		b := e.Add(rnSched("b", 15))
		e.Changed()
		e.GoTo(rnMin(61))
		e.WantStarts(b.FarEnd, rnMin(30), rnMin(45), rnMin(60))
		e.WantStarts(a.FarEnd, rnMin(60))
	})
}

func TestR5_ScheduleAddedWhileNothingWasScheduled(t *testing.T) {
	rnBubble(t, func(t *testing.T) {
		e := rnNewEnv(t)
		e.Start()
		e.GoTo(rnMin(20))
		if e.RunReturned() {
			t.Fatal("Run returned with no schedules; it should wait for some")
		}
		a := e.Add(rnSched("a", 15))
		e.Changed()
		e.GoTo(rnMin(46))
		e.WantStarts(a.FarEnd, rnMin(30), rnMin(45))
	})
}

func TestR5_ChangedTimingReplacesTheOldNextTime(t *testing.T) {
	rnBubble(t, func(t *testing.T) {
		e := rnNewEnv(t)
		a := e.Add(rnSched("a", 60)) // next at 01:00
		b := e.Add(rnSched("b", 15)) // next at 00:15
		e.Start()
		e.GoTo(rnMin(10))
		a.EveryMinutes, a.OffsetMinutes = 15, 5 // 00:20, 00:35, 00:50, 01:05
		a = e.Update(a)
		b.EveryMinutes = 60 // 01:00
		b = e.Update(b)
		e.Changed()
		e.GoTo(rnMin(62))
		e.WantStarts(a.FarEnd, rnMin(20), rnMin(35), rnMin(50))
		e.WantStarts(b.FarEnd, rnMin(60))
	})
}

func TestR5_PausedScheduleDoesNotRunAndResumesOnTheGrid(t *testing.T) {
	rnBubble(t, func(t *testing.T) {
		e := rnNewEnv(t)
		x := rnSched("x", 60)
		x.OffsetMinutes = 15 // 00:15 and 01:15
		x = e.Add(x)
		e.Fake.On(x.FarEnd, rnOK(10*time.Minute))
		a := e.Add(rnSched("a", 15))
		e.Fake.On(a.FarEnd, rnOK(time.Minute))
		e.Start()
		e.GoTo(rnMin(20)) // x has been running since 00:15, and a waits behind it
		a.Paused = true
		a = e.Update(a)
		e.Changed()
		e.GoTo(rnMin(40))
		// a's queued run was dropped, and its time at 00:30 passed unused.
		e.WantStarts(x.FarEnd, rnMin(15))
		e.WantStarts(a.FarEnd)
		a.Paused = false
		a = e.Update(a)
		e.Changed()
		e.GoTo(rnMin(50))
		e.WantStarts(a.FarEnd, rnMin(45)) // not at 00:40, when it was resumed
	})
}

// ---- R6, R7: deleting ----

func TestR6_DeletingARunningScheduleCancelsItsRunAndStoresNothing(t *testing.T) {
	rnBubble(t, func(t *testing.T) {
		e := rnNewEnv(t, rnWithMetrics)
		a := e.Add(rnSched("a", 15))
		e.Fake.On(a.FarEnd, rnOK(5*time.Minute))
		b := e.Add(rnSched("b", 15))
		e.Fake.On(b.FarEnd, rnOK(time.Minute))
		e.Start()
		e.GoTo(rnMin(17)) // a has been running since 00:15, and b waits
		e.Delete(a.ID)
		e.Changed()
		e.Settle()
		calls := e.Fake.Calls(a.FarEnd)
		if len(calls) != 1 {
			t.Fatalf("%d runs of a, want 1", len(calls))
		}
		// The canceled run counts as neither outcome: a's series are gone with the
		// schedule, and nothing the cancel did brought one back.
		for _, s := range e.Expo() {
			if s.Name == "dyno_schedule_runs_total" && s.Labels["schedule"] == strconv.FormatInt(a.ID, 10) {
				t.Errorf("the deleted schedule still has %s%v = %v after its run was canceled", s.Name, s.Labels, s.Value)
			}
		}
		c := calls[0]
		if !c.CtxEnded || !errors.Is(c.CtxErr, context.Canceled) {
			t.Errorf("the Runner's ctx wasn't canceled by the delete: ended %v, err %v", c.CtxEnded, c.CtxErr)
		}
		if !rnNear(c.CtxEndedAt.Sub(rnT0), rnMin(17)) {
			t.Errorf("the ctx ended at %v, want 00:17", c.CtxEndedAt.Sub(rnT0))
		}
		e.GoTo(rnMin(32))
		if rs := e.ResultsOf(a.ID); len(rs) != 0 {
			t.Errorf("%d results stored for the deleted schedule's run, want none: %+v", len(rs), rs)
		}
		if n := len(e.Fake.Calls(a.FarEnd)); n != 1 {
			t.Errorf("a was run %d times, want once: it is gone", n)
		}
		// b gets the turn the delete freed, and keeps its own times.
		e.WantStarts(b.FarEnd, rnMin(17), rnMin(30))
		if n := len(e.ResultsOf(b.ID)); n != 2 {
			t.Errorf("%d results for b, want 2", n)
		}
	})
}

// A Runner that finishes the run anyway, and returns a success after its
// schedule was deleted, still leaves nothing stored; and the next schedule has
// to wait for it, since one run goes at a time.
func TestR6_FinishedRunOfADeletedScheduleIsNotStored(t *testing.T) {
	rnBubble(t, func(t *testing.T) {
		e := rnNewEnv(t)
		a := e.Add(rnSched("a", 15))
		e.Fake.On(a.FarEnd, rnScript{Take: 4 * time.Minute, Ignore: true, Out: rnOutcome(rnIdle)}) // ends at 00:19
		b := e.Add(rnSched("b", 15))
		e.Fake.On(b.FarEnd, rnOK(time.Minute))
		e.Start()
		e.GoTo(rnMin(17))
		e.Delete(a.ID)
		e.Changed()
		e.GoTo(rnMin(21))
		c := rnFirstCall(e, a.FarEnd)
		if !c.CtxEnded || !errors.Is(c.CtxErr, context.Canceled) {
			t.Errorf("the Runner's ctx wasn't canceled by the delete: ended %v, err %v", c.CtxEnded, c.CtxErr)
		}
		if rs := e.ResultsOf(a.ID); len(rs) != 0 {
			t.Errorf("%d results stored for the deleted schedule's run, want none: %+v", len(rs), rs)
		}
		e.WantStarts(b.FarEnd, rnMin(19))
		if n := e.Fake.MaxInFlight(); n != 1 {
			t.Errorf("%d runs at once, want 1", n)
		}
	})
}

func TestR7_DeletingAQueuedScheduleDropsItsRun(t *testing.T) {
	rnBubble(t, func(t *testing.T) {
		e := rnNewEnv(t)
		x := e.Add(rnSched("x", 15))
		e.Fake.On(x.FarEnd, rnOK(10*time.Minute))
		q := e.Add(rnSched("q", 15))
		e.Start()
		e.GoTo(rnMin(18)) // x has been running since 00:15, and q waits
		e.Delete(q.ID)
		e.Changed()
		e.GoTo(rnMin(31))
		e.WantStarts(q.FarEnd) // never
		e.WantStarts(x.FarEnd, rnMin(15), rnMin(30))
		if rnFirstCall(e, x.FarEnd).CtxEnded {
			t.Error("deleting q stopped x's run")
		}
		if got := e.Status(q.ID); got != (schedule.Status{}) {
			t.Errorf("Status of the deleted schedule is %+v, want the zero Status", got)
		}
		if n := len(e.ResultsOf(q.ID)); n != 0 {
			t.Errorf("%d results for q", n)
		}
	})
}

// ---- R8: RunNow ----

func TestR8_RunNowQueuesAtOnceAlsoWhenPaused(t *testing.T) {
	rnBubble(t, func(t *testing.T) {
		e := rnNewEnv(t)
		a := rnSched("a", 1440)
		a.OffsetMinutes, a.Paused = 600, true // its time would be 10:00, if it weren't paused
		a = e.Add(a)
		e.Fake.On(a.FarEnd, rnOK(10*time.Second))
		b := rnSched("b", 1440)
		b.OffsetMinutes = 600
		b = e.Add(b)
		e.Start()
		e.GoTo(rnMin(7))
		rnRunNow(e, a.ID)
		rnRunNow(e, b.ID)
		e.GoTo(rnMin(8))
		e.WantStarts(a.FarEnd, rnMin(7))
		// b waits for a's 10 s run, then goes.
		starts := rnOffsets(e.Fake.Calls(b.FarEnd))
		if len(starts) != 1 || starts[0] < rnMin(7) || starts[0] > rnMin(7)+10*time.Second+rnSlack {
			t.Errorf("b's runs began at %v, want one at 00:07:10", starts)
		}
		if n := len(e.ResultsOf(a.ID)); n != 1 {
			t.Errorf("%d results for the paused schedule's run, want 1", n)
		}
		// a stays paused: 10:00 passes without a run of it, and b has its own.
		e.GoTo(10*time.Hour + rnMin(5))
		e.WantStarts(a.FarEnd, rnMin(7))
		if n := len(e.Fake.Calls(b.FarEnd)); n != 2 {
			t.Errorf("b ran %d times, want 2: RunNow's and the one at 10:00", n)
		}
	})
}

func TestR8_RunNowOfAnUnknownIDIsNotFound(t *testing.T) {
	rnBubble(t, func(t *testing.T) {
		e := rnNewEnv(t)
		a := rnSched("a", 1440)
		a.OffsetMinutes = 600
		a = e.Add(a)
		e.Start()
		e.GoTo(rnMin(5))
		for _, id := range []int64{a.ID + 100, 0, -1} {
			if err := e.RunNow(id); !errors.Is(err, store.ErrNotFound) {
				t.Errorf("RunNow(%d) = %v, want store.ErrNotFound", id, err)
			}
		}
		e.Delete(a.ID)
		e.Changed()
		if err := e.RunNow(a.ID); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("RunNow of a deleted schedule = %v, want store.ErrNotFound", err)
		}
		e.GoTo(rnMin(10))
		if n := len(e.Fake.Calls("")); n != 0 {
			t.Errorf("the Runner was called %d times, want 0", n)
		}
	})
}

func TestR8_RunNowDoesntQueueARunQueuedOrRunningAlready(t *testing.T) {
	rnBubble(t, func(t *testing.T) {
		e := rnNewEnv(t)
		x := rnSched("x", 1440)
		x.OffsetMinutes = 600
		x = e.Add(x)
		e.Fake.On(x.FarEnd, rnOK(10*time.Minute))
		a := rnSched("a", 1440)
		a.OffsetMinutes = 600
		a = e.Add(a)
		e.Fake.On(a.FarEnd, rnOK(time.Minute))
		e.Start()
		e.GoTo(rnMin(5))
		rnRunNow(e, x.ID) // x runs from 00:05 to 00:15
		e.GoTo(rnMin(6))
		rnRunNow(e, a.ID) // a waits behind it
		e.GoTo(rnMin(7))
		for range 2 {
			rnRunNow(e, x.ID) // already running
			rnRunNow(e, a.ID) // already queued
		}
		e.GoTo(rnMin(20))
		e.WantStarts(x.FarEnd, rnMin(5))
		e.WantStarts(a.FarEnd, rnMin(15))
		// Once a run has ended, the schedule can be asked for again.
		e.GoTo(rnMin(30))
		rnRunNow(e, x.ID)
		e.GoTo(rnMin(31))
		e.WantStarts(x.FarEnd, rnMin(5), rnMin(30))
	})
}

func TestR8_RunNowBeforeRunStartsRunsOnceRunStarts(t *testing.T) {
	rnBubble(t, func(t *testing.T) {
		e := rnNewEnv(t)
		a := rnSched("a", 1440)
		a.Paused = true
		a = e.Add(a)
		// None of these may block or panic before Run has started.
		e.Changed()
		_ = e.Status(a.ID)
		rnRunNow(e, a.ID)
		rnRunNow(e, a.ID)
		e.Settle()
		if n := len(e.Fake.Calls("")); n != 0 {
			t.Fatalf("the Runner was called %d times before Run started", n)
		}
		e.Advance(rnMin(3))
		e.Start()
		e.Advance(rnMin(10))
		e.WantStarts(a.FarEnd, rnMin(3)) // once, as Run starts
	})
}

// ---- R9: Status ----

func TestR9_StatusIsRunningQueuedOrNeither(t *testing.T) {
	rnBubble(t, func(t *testing.T) {
		e := rnNewEnv(t)
		x := e.Add(rnSched("x", 30))
		e.Fake.On(x.FarEnd, rnOK(10*time.Minute))
		a := e.Add(rnSched("a", 30))
		e.Fake.On(a.FarEnd, rnOK(5*time.Minute))
		e.Start()
		none, running, queued := schedule.Status{}, schedule.Status{Running: true}, schedule.Status{Queued: true}
		check := func(at time.Duration, wantX, wantA schedule.Status) {
			t.Helper()
			e.GoTo(at)
			if got := e.Status(x.ID); got != wantX {
				t.Errorf("at %v: Status(x) = %+v, want %+v", at, got, wantX)
			}
			if got := e.Status(a.ID); got != wantA {
				t.Errorf("at %v: Status(a) = %+v, want %+v", at, got, wantA)
			}
		}
		// Nothing is due yet.
		check(rnMin(29), none, none)
		// Both are due at 00:30, and x has the lower id.
		check(rnMin(31), running, queued)
		// x ended at 00:40, and a ends at 00:45.
		check(rnMin(41), none, running)
		// Both wait for 01:00.
		check(rnMin(46), none, none)
		for _, id := range []int64{x.ID + a.ID + 100, 0, -1} {
			if got := e.Status(id); got != none {
				t.Errorf("Status(%d) = %+v, want the zero Status", id, got)
			}
		}
	})
}

// ---- R10: waiting for this server's own tests ----

func TestR10_BusyThatEndsStartsTheRunWithinOnePoll(t *testing.T) {
	rnBubble(t, func(t *testing.T) {
		var busy atomic.Bool
		busy.Store(true)
		e := rnNewEnv(t, rnWithBusy(busy.Load))
		a := e.Add(rnSched("a", 15))
		e.Start()
		e.GoTo(rnMin(15) + 30*time.Second) // due at 00:15, and still serving a test 30 s later
		if calls := e.Fake.Calls(""); len(calls) != 0 {
			t.Fatalf("a run began at %v while this dyno was busy", calls[0].Start.Sub(rnT0))
		}
		busy.Store(false)
		freed := time.Now()
		e.Advance(2 * scheduler.BusyPoll)
		calls := e.Fake.Calls(a.FarEnd)
		if len(calls) != 1 {
			t.Fatalf("%d runs after Busy ended, want 1", len(calls))
		}
		if wait := calls[0].Start.Sub(freed); wait < 0 || wait > scheduler.BusyPoll {
			t.Errorf("the run began %v after Busy ended, want within %v", wait, scheduler.BusyPoll)
		}
	})
}

func TestR10_BusyThroughoutRunsAtBusyWaitAnyway(t *testing.T) {
	rnBubble(t, func(t *testing.T) {
		e := rnNewEnv(t, rnWithBusy(func() bool { return true }))
		a := e.Add(rnSched("a", 15))
		e.Start()
		due := rnMin(15)
		e.GoTo(due + scheduler.BusyWait - 5*time.Second)
		if calls := e.Fake.Calls(""); len(calls) != 0 {
			t.Fatalf("a run began at %v, before BusyWait had passed", calls[0].Start.Sub(rnT0))
		}
		e.GoTo(due + scheduler.BusyWait + 2*scheduler.BusyPoll)
		calls := e.Fake.Calls(a.FarEnd)
		if len(calls) != 1 {
			t.Fatalf("%d runs, want 1: it should run anyway", len(calls))
		}
		if wait := calls[0].Start.Sub(rnT0) - due; wait < scheduler.BusyWait || wait > scheduler.BusyWait+scheduler.BusyPoll {
			t.Errorf("the run began %v after its time, want %v", wait, scheduler.BusyWait)
		}
	})
}

func TestR10_NilOrIdleBusyNeverWaits(t *testing.T) {
	for name, busy := range map[string]func() bool{"nil": nil, "false": func() bool { return false }} {
		t.Run(name, func(t *testing.T) {
			rnBubble(t, func(t *testing.T) {
				e := rnNewEnv(t, rnWithBusy(busy))
				a := e.Add(rnSched("a", 15))
				e.Start()
				e.Advance(rnMin(16))
				e.WantStarts(a.FarEnd, rnMin(15))
			})
		})
	}
}

// ---- R11 to R16: what a run stores ----

func TestR11_SuccessfulRunStoresOneResult(t *testing.T) {
	rnBubble(t, func(t *testing.T) {
		e := rnNewEnv(t)
		a := e.Add(rnSched("tailmeadow", 60))
		out := rnOutcome(8.4)
		out.Submission.ClientLimited = true
		e.Fake.On(a.FarEnd, rnScript{Take: 90 * time.Second, Out: out})
		e.Start()
		e.GoTo(rnMin(63))
		rs := e.ResultsOf(a.ID)
		if len(rs) != 1 || len(e.Results()) != 1 {
			t.Fatalf("%d results stored for one run (%d for its schedule), want 1", len(e.Results()), len(rs))
		}
		r := rs[0]
		rnWantScheduled(t, r, a)
		got, want := r.Submission, out.Submission
		if got.DownloadMbps != want.DownloadMbps || got.UploadMbps != want.UploadMbps || got.IdleMs != want.IdleMs ||
			got.LoadedDownMs != want.LoadedDownMs || got.LoadedUpMs != want.LoadedUpMs || got.JitterMs != want.JitterMs ||
			got.Streams != want.Streams || got.ClientLimited != want.ClientLimited {
			t.Errorf("measurements %+v, want the Outcome's %+v", got, want)
		}
		derived := result.Result{Submission: want}
		derived.Derive()
		if r.Grade == "" || r.Grade != derived.Grade || r.BufferbloatMs != derived.BufferbloatMs || r.RPM != derived.RPM {
			t.Errorf("grade %q, bufferbloat %v, rpm %d; want %q, %v, %d as Derive works them out",
				r.Grade, r.BufferbloatMs, r.RPM, derived.Grade, derived.BufferbloatMs, derived.RPM)
		}
		full, err := e.Store.Get(r.ID)
		if err != nil {
			t.Fatal(err)
		}
		if string(full.Detail) != string(want.Detail) {
			t.Errorf("detail %s, want the Outcome's %s", full.Detail, want.Detail)
		}
		if r.Failure != nil || r.PathChanged {
			t.Errorf("failure %+v, path_changed %v, want neither", r.Failure, r.PathChanged)
		}
		c := rnFirstCall(e, a.FarEnd)
		if r.At.UnixMilli() < c.Start.UnixMilli() || r.At.UnixMilli() > c.End.UnixMilli() {
			t.Errorf("at %v, want between the run's start %v and end %v", r.At, c.Start, c.End)
		}
	})
}

func TestR12_RunnerGetsFarEndKindAndATimeout(t *testing.T) {
	rnBubble(t, func(t *testing.T) {
		e := rnNewEnv(t)
		l := e.Add(rnSched("light", 60))
		f := rnSched("full", 60)
		f.Kind = schedule.Full
		f = e.Add(f)
		e.Start()
		e.GoTo(rnMin(61))
		for _, c := range []struct {
			s    schedule.Schedule
			kind nodetest.Kind
		}{{l, nodetest.Light}, {f, nodetest.Full}} {
			calls := e.Fake.Calls(c.s.FarEnd)
			if len(calls) != 1 {
				t.Errorf("%s: %d calls, want 1", c.s.Name, len(calls))
				continue
			}
			call := calls[0]
			o := call.Opts
			if o.FarEnd != c.s.FarEnd || o.Kind != c.kind {
				t.Errorf("%s: Options far end %q, kind %q; want %q and %q", c.s.Name, o.FarEnd, o.Kind, c.s.FarEnd, c.kind)
			}
			if o.MaxBytes != 0 || o.Warm != 0 || o.PingTimeout != 0 || o.TLS != nil {
				t.Errorf("%s: Options set more than the far end and the kind: %+v", c.s.Name, o)
			}
			if !call.HasDeadline {
				t.Errorf("%s: the Runner's ctx has no deadline", c.s.Name)
				continue
			}
			if d := call.Deadline.Sub(call.Start); d < scheduler.RunTimeout-rnSlack || d > scheduler.RunTimeout+rnSlack {
				t.Errorf("%s: the ctx's deadline is %v after the run began, want %v", c.s.Name, d, scheduler.RunTimeout)
			}
		}
	})
}

func TestR13_FailedRunStoresAFailureFromTheError(t *testing.T) {
	rnBubble(t, func(t *testing.T) {
		e := rnNewEnv(t)
		fails := []struct {
			code  string
			phase nodetest.Phase
		}{
			{nodetest.CodeUnreachable, ""},
			{nodetest.CodeTimeout, nodetest.PhaseLatency},
			{nodetest.CodeNotDyno, ""},
			{nodetest.CodeHTTP, nodetest.PhaseDownload},
			{nodetest.CodeStream, nodetest.PhaseUpload},
			{nodetest.CodeBadFarEnd, ""},
		}
		ss := make([]schedule.Schedule, len(fails))
		for i, f := range fails {
			ss[i] = e.Add(rnSched("far"+strconv.Itoa(i), 60))
			e.Fake.On(ss[i].FarEnd, rnFail(5*time.Second, rnErr(f.code, f.phase)))
		}
		e.Start()
		e.GoTo(rnMin(61))
		for i, f := range fails {
			s := ss[i]
			rs := e.ResultsOf(s.ID)
			if len(rs) != 1 {
				t.Errorf("%s: %d results, want 1", f.code, len(rs))
				continue
			}
			r := rs[0]
			rnWantScheduled(t, r, s)
			if msg := rnNoMeasurements(r); msg != "" {
				t.Errorf("%s: %s", f.code, msg)
			}
			if r.PathChanged {
				t.Errorf("%s: path_changed is set on a failed run", f.code)
			}
			if r.Failure == nil {
				t.Errorf("%s: no failure stored", f.code)
				continue
			}
			// The scheduler has stored the result, so Error() may be read now.
			returned := rnFirstCall(e, s.FarEnd).Returned
			nerr, ok := returned.(*nodetest.Error)
			if !ok {
				t.Fatalf("%s: the fake returned %T", f.code, returned)
			}
			if r.Failure.Code != f.code || r.Failure.Phase != string(f.phase) || r.Failure.Message != nerr.Error() {
				t.Errorf("%s: failure %+v, want code %q, phase %q, message %q", f.code, *r.Failure, f.code, f.phase, nerr.Error())
			}
			if r.Failure.Message == "" {
				t.Errorf("%s: the message is empty", f.code)
			}
		}
	})
}

func TestR14_RunStillGoingAtRunTimeoutIsStoppedAndStoredAsTimeout(t *testing.T) {
	rnBubble(t, func(t *testing.T) {
		e := rnNewEnv(t)
		a := e.Add(rnSched("a", 30))
		e.Fake.On(a.FarEnd, rnScript{Block: true})
		e.Start()
		e.GoTo(rnMin(30) + scheduler.RunTimeout - 10*time.Second)
		if n := len(e.ResultsOf(a.ID)); n != 0 {
			t.Errorf("%d results stored before the run's time was up", n)
		}
		e.GoTo(rnMin(36))
		calls := e.Fake.Calls(a.FarEnd)
		if len(calls) != 1 {
			t.Fatalf("%d runs, want 1", len(calls))
		}
		c := calls[0]
		if !c.CtxEnded || !errors.Is(c.CtxErr, context.DeadlineExceeded) {
			t.Errorf("the ctx ended %v with %v, want its deadline to pass", c.CtxEnded, c.CtxErr)
		}
		if d := c.CtxEndedAt.Sub(c.Start); d < scheduler.RunTimeout-rnSlack || d > scheduler.RunTimeout+rnSlack {
			t.Errorf("the run was stopped %v after it began, want %v", d, scheduler.RunTimeout)
		}
		rs := e.ResultsOf(a.ID)
		if len(rs) != 1 {
			t.Fatalf("%d results, want the one timeout", len(rs))
		}
		rnWantScheduled(t, rs[0], a)
		nerr, ok := c.Returned.(*nodetest.Error)
		if !ok {
			t.Fatalf("the fake returned %T", c.Returned)
		}
		if f := rs[0].Failure; f == nil || f.Code != nodetest.CodeTimeout || f.Phase != string(nerr.Phase) || f.Message != nerr.Error() {
			t.Errorf("failure %+v, want the timeout error's code, phase and text", f)
		}
		if msg := rnNoMeasurements(rs[0]); msg != "" {
			t.Error(msg)
		}
	})
}

func TestR15_CanceledErrorStoresNothing(t *testing.T) {
	rnBubble(t, func(t *testing.T) {
		e := rnNewEnv(t, rnWithMetrics)
		a := e.Add(rnSched("a", 15))
		e.Fake.On(a.FarEnd, rnFail(10*time.Second, rnErr(nodetest.CodeCanceled, nodetest.PhaseDownload)))
		b := e.Add(rnSched("b", 15))
		e.Start()
		e.GoTo(rnMin(31))
		e.WantStarts(a.FarEnd, rnMin(15), rnMin(30)) // the schedule goes on
		if rs := e.ResultsOf(a.ID); len(rs) != 0 {
			t.Errorf("%d results stored for canceled runs, want none: %+v", len(rs), rs)
		}
		// A canceled run counts as neither outcome, though it ran twice.
		rnWantSeries(t, e.Expo(), a.ID, rnSeries{Name: "a"})
		if n := len(e.ResultsOf(b.ID)); n != 2 {
			t.Errorf("%d results for b, want 2: the scheduler is still working", n)
		}
	})
}

func TestR16_PanicOrForeignErrorIsStoredAsInternalAndTheScheduleGoesOn(t *testing.T) {
	rnBubble(t, func(t *testing.T) {
		e := rnNewEnv(t)
		bad := []struct {
			script rnScript
			text   string // what the failure's message must hold
		}{
			{rnPanics(0, "kaboom 42"), "kaboom 42"},
			{rnPanics(0, errors.New("exploded 43")), "exploded 43"},
			{rnFail(0, errors.New("disk on fire 44")), "disk on fire 44"},
		}
		ss := make([]schedule.Schedule, len(bad))
		for i, b := range bad {
			ss[i] = e.Add(rnSched("bad"+strconv.Itoa(i), 15))
			e.Fake.On(ss[i].FarEnd, b.script)
		}
		ok := e.Add(rnSched("ok", 15))
		e.Start()
		e.GoTo(rnMin(31))
		if e.RunReturned() {
			t.Fatal("Run returned")
		}
		for i, b := range bad {
			s := ss[i]
			e.WantStarts(s.FarEnd, rnMin(15), rnMin(30)) // the next run still happens on time
			rs := e.ResultsOf(s.ID)
			if len(rs) != 2 {
				t.Errorf("%q: %d results, want 2", b.text, len(rs))
				continue
			}
			for _, r := range rs {
				rnWantScheduled(t, r, s)
				if msg := rnNoMeasurements(r); msg != "" {
					t.Errorf("%q: %s", b.text, msg)
				}
				if r.Failure == nil || r.Failure.Code != scheduler.CodeInternal || !strings.Contains(r.Failure.Message, b.text) {
					t.Errorf("%q: failure %+v, want code %q with the text in its message", b.text, r.Failure, scheduler.CodeInternal)
				}
			}
		}
		if rs := e.ResultsOf(ok.ID); len(rs) != 2 || rs[0].Failure != nil || rs[1].Failure != nil {
			t.Errorf("the schedule after the failing ones has results %+v, want 2 successes", rs)
		}
	})
}

// ---- R17: stopping ----

func TestR17_CancelingRunStopsTheRunAndWaitsForIt(t *testing.T) {
	rnBubble(t, func(t *testing.T) {
		e := rnNewEnv(t)
		a := e.Add(rnSched("a", 15))
		e.Fake.On(a.FarEnd, rnScript{Take: 10 * time.Minute, StopTake: 2 * time.Second, Out: rnOutcome(rnIdle)})
		e.Start()
		e.GoTo(rnMin(16)) // the run began at 00:15
		e.Cancel()
		e.Settle()
		c := rnFirstCall(e, a.FarEnd)
		if !c.CtxEnded || !errors.Is(c.CtxErr, context.Canceled) {
			t.Errorf("the Runner's ctx wasn't canceled: ended %v, err %v", c.CtxEnded, c.CtxErr)
		}
		if e.RunReturned() {
			t.Error("Run returned while the Runner was still winding down")
		}
		e.Advance(3 * time.Second)
		if !e.RunReturned() {
			t.Fatal("Run didn't return after the Runner did")
		}
		c = rnFirstCall(e, a.FarEnd)
		if !c.Finished || e.returnedAt.Before(c.End) {
			t.Errorf("Run returned at %v, before the Runner ended at %v", e.returnedAt.Sub(rnT0), c.End.Sub(rnT0))
		}
		if rs := e.Results(); len(rs) != 0 {
			t.Errorf("%d results stored for a run that was stopped: %+v", len(rs), rs)
		}
		// The test's cleanup ends the bubble: any goroutine left over fails it.
	})
}

// A Runner that finishes the run anyway still leaves nothing stored, and Run
// waits for it.
func TestR17_FinishedRunAfterCancelIsNotStored(t *testing.T) {
	rnBubble(t, func(t *testing.T) {
		e := rnNewEnv(t)
		a := e.Add(rnSched("a", 15))
		e.Fake.On(a.FarEnd, rnScript{Take: 10 * time.Minute, Ignore: true, Out: rnOutcome(rnIdle)})
		e.Start()
		e.GoTo(rnMin(16)) // the run began at 00:15 and ends at 00:25
		e.Cancel()
		e.GoTo(rnMin(24))
		if e.RunReturned() {
			t.Error("Run returned before the Runner did")
		}
		e.GoTo(rnMin(26))
		if !e.RunReturned() {
			t.Fatal("Run didn't return after the Runner did")
		}
		if e.returnedAt.Sub(rnT0) < rnMin(25) {
			t.Errorf("Run returned at %v, before the Runner ended at 00:25", e.returnedAt.Sub(rnT0))
		}
		if rs := e.Results(); len(rs) != 0 {
			t.Errorf("%d results stored for a run that was stopped: %+v", len(rs), rs)
		}
	})
}

func TestR17_CancelingRunReturnsWhenNothingIsRunning(t *testing.T) {
	rnBubble(t, func(t *testing.T) {
		e := rnNewEnv(t)
		e.Add(rnSched("a", 15))
		e.Start()
		e.GoTo(rnMin(5))
		e.Cancel()
		e.Settle()
		if !e.RunReturned() {
			t.Error("Run didn't return when its ctx ended between runs")
		}
		e.Advance(time.Hour)
		if n := len(e.Fake.Calls("")); n != 0 {
			t.Errorf("the Runner was called %d times", n)
		}
	})
}

func TestR17_CancelingRunEndsAWaitForBusy(t *testing.T) {
	rnBubble(t, func(t *testing.T) {
		e := rnNewEnv(t, rnWithBusy(func() bool { return true }))
		e.Add(rnSched("a", 15))
		e.Start()
		e.GoTo(rnMin(15) + 30*time.Second) // due at 00:15, and waiting for Busy since
		e.Cancel()
		e.Advance(scheduler.BusyPoll + time.Second) // it may take a poll to notice
		if !e.RunReturned() {
			t.Fatal("Run didn't return while a run was waiting for Busy")
		}
		e.Advance(10 * time.Minute)
		if n := len(e.Fake.Calls("")); n != 0 {
			t.Errorf("the Runner was called %d times after the ctx ended", n)
		}
		if n := len(e.Results()); n != 0 {
			t.Errorf("%d results stored", n)
		}
	})
}

// ---- R18: PathChanged ----

// rnSeed is one stored result a schedule has before its run.
type rnSeed struct {
	Idle  float64
	Fail  bool   // a failed run: no measurements, and a failure
	Far   string // against a far end other than the schedule's
	Other bool   // a result of another schedule, against the same far end
}

func rnIdles(idle ...float64) []rnSeed {
	var out []rnSeed
	for _, v := range idle {
		out = append(out, rnSeed{Idle: v})
	}
	return out
}

func rnFails(n int) []rnSeed {
	return slices.Repeat([]rnSeed{{Fail: true}}, n)
}

func rnOtherFar(n int, idle float64) []rnSeed {
	return slices.Repeat([]rnSeed{{Idle: idle, Far: rnFar("old")}}, n)
}

func rnOtherSched(n int, idle float64) []rnSeed {
	return slices.Repeat([]rnSeed{{Idle: idle, Other: true}}, n)
}

func rnSeedResult(s schedule.Schedule, seed rnSeed, at time.Time) result.Result {
	r := result.Result{
		At: at, Source: "schedule", ScheduleID: s.ID, FarEnd: s.FarEnd,
		Submission: result.Submission{
			Client: "seed", DownloadMbps: 900, UploadMbps: 400, IdleMs: seed.Idle,
			LoadedDownMs: 20, LoadedUpMs: 30, JitterMs: 0.5, Streams: 5,
		},
	}
	if seed.Far != "" {
		r.FarEnd = seed.Far
	}
	if seed.Other {
		r.ScheduleID = s.ID + 1000
	}
	if seed.Fail {
		r.Submission = result.Submission{Client: "seed"}
		r.Failure = &result.Failure{Code: nodetest.CodeTimeout, Phase: "download", Message: "a seeded failure"}
	}
	r.Derive()
	return r
}

func TestR18_PathChangedComparesIdleLatencyWithTheBaseline(t *testing.T) {
	rnBubble(t, func(t *testing.T) {
		e := rnNewEnv(t)
		cases := []struct {
			name    string
			seeds   []rnSeed // oldest first
			idle    float64  // the run's idle latency, ms
			flagged bool
		}{
			{"far above the median of 18, 19, 20, 18, 22", rnIdles(18, 19, 20, 18, 22), 50, true},
			{"under twice the median", rnIdles(18, 19, 20, 18, 22), 35, false},
			{"exactly twice isn't more than twice", rnIdles(18, 19, 20, 18, 22), 38, false},
			{"just over twice", rnIdles(18, 19, 20, 18, 22), 38.5, true},
			{"more than twice but under 10 ms above", rnIdles(4, 4, 4, 4, 4), 12, false},
			{"exactly 10 ms above is enough", rnIdles(4, 4, 4, 4, 4), 14, true},
			{"two earlier successes are no baseline", rnIdles(20, 20), 500, false},
			{"three are", rnIdles(20, 20, 20), 50, true},
			{"failures don't pull the baseline down", append(rnIdles(20, 20, 20), rnFails(4)...), 30, false},
			{"failures don't count towards the three", append(rnIdles(20, 20), rnFails(3)...), 500, false},
			{"another far end's results are left out", append(rnIdles(20, 20, 20), rnOtherFar(5, 100)...), 50, true},
			{"a far end with no history has no baseline", rnOtherFar(5, 20), 500, false},
			{"another schedule's results are left out", append(rnIdles(20, 20, 20), rnOtherSched(5, 100)...), 50, true},
			{"the run's own result is left out", rnIdles(10, 10, 40), 45, true},
			{"only the newest ten count", append(rnIdles(slices.Repeat([]float64{100}, 10)...), rnIdles(slices.Repeat([]float64{20}, 10)...)...), 50, true},
		}
		ss := make([]schedule.Schedule, len(cases))
		for i, c := range cases {
			ss[i] = e.Add(rnSched(fmt.Sprintf("p%02d", i), 15))
			e.Fake.On(ss[i].FarEnd, rnScript{Out: rnOutcome(c.idle)})
			for j, seed := range c.seeds {
				e.Seed(rnSeedResult(ss[i], seed, rnT0.Add(-24*time.Hour+time.Duration(j)*time.Minute)))
			}
		}
		e.Start()
		e.GoTo(rnMin(16))
		for i, c := range cases {
			rs := e.ResultsOf(ss[i].ID)
			if len(rs) == 0 {
				t.Errorf("%s: no results", c.name)
				continue
			}
			r := rs[len(rs)-1] // the run's own, stored last
			if r.Client != "dyno/"+rnVersion || r.IdleMs != c.idle || r.Failure != nil {
				t.Errorf("%s: the newest result isn't the run's: %+v", c.name, r)
				continue
			}
			if r.PathChanged != c.flagged {
				t.Errorf("%s: path_changed = %v for idle %v ms, want %v", c.name, r.PathChanged, c.idle, c.flagged)
			}
		}
	})
}

// ---- R19: metrics ----

func TestR19_RunStartsCountersAtZeroAndRestoresTheLastSuccess(t *testing.T) {
	rnBubble(t, func(t *testing.T) {
		e := rnNewEnv(t, rnWithMetrics)
		a := e.Add(rnSched("home", 15))
		b := e.Add(rnSched("away", 15))
		at := time.Date(1999, 12, 31, 20, 0, 0, 0, time.UTC)
		good := e.Seed(result.Result{
			At: at, Source: "schedule", ScheduleID: a.ID, FarEnd: a.FarEnd,
			Submission: result.Submission{Client: "seed", DownloadMbps: 300, UploadMbps: 30, IdleMs: 12, LoadedDownMs: 25, LoadedUpMs: 45, Streams: 5},
		})
		e.Seed(result.Result{ // newer, but a failure: the gauges show the success
			At: at.Add(time.Hour), Source: "schedule", ScheduleID: a.ID, FarEnd: a.FarEnd,
			Submission: result.Submission{Client: "seed"},
			Failure:    &result.Failure{Code: nodetest.CodeUnreachable, Message: "a newer failure"},
		})
		e.Start()
		ss := e.Expo()
		rnWantSeries(t, ss, a.ID, rnSeries{Name: "home", Last: rnGaugesOf(good)})
		rnWantSeries(t, ss, b.ID, rnSeries{Name: "away"})
	})
}

func TestR19_FinishedRunsCountAndSetTheGauges(t *testing.T) {
	rnBubble(t, func(t *testing.T) {
		e := rnNewEnv(t, rnWithMetrics)
		a := e.Add(rnSched("home", 15))
		b := e.Add(rnSched("away", 15))
		e.Fake.OnCall(a.FarEnd, func(n int) rnScript {
			if n == 1 {
				return rnOK(0)
			}
			return rnFail(0, rnErr(nodetest.CodeTimeout, nodetest.PhaseDownload))
		})
		e.Fake.On(b.FarEnd, rnFail(0, rnErr(nodetest.CodeUnreachable, "")))
		e.Start()
		e.GoTo(rnMin(16))
		rs := e.ResultsOf(a.ID)
		if len(rs) != 1 || rs[0].Failure != nil {
			t.Fatalf("a's results after its first run: %+v", rs)
		}
		last := rnGaugesOf(rs[0])
		ss := e.Expo()
		rnWantSeries(t, ss, a.ID, rnSeries{Name: "home", Succeeded: 1, Last: last})
		rnWantSeries(t, ss, b.ID, rnSeries{Name: "away", Failed: 1})
		// Failures count as failed and leave the gauges as the last success set them.
		e.GoTo(rnMin(31))
		ss = e.Expo()
		rnWantSeries(t, ss, a.ID, rnSeries{Name: "home", Succeeded: 1, Failed: 1, Last: last})
		rnWantSeries(t, ss, b.ID, rnSeries{Name: "away", Failed: 2})
	})
}

func TestR19_ChangedAddsDropsAndRenamesSeries(t *testing.T) {
	rnBubble(t, func(t *testing.T) {
		e := rnNewEnv(t, rnWithMetrics)
		a := e.Add(rnSched("a", 15))
		b := e.Add(rnSched("b", 15))
		e.Start()
		e.GoTo(rnMin(16)) // both succeeded once
		last := func(s schedule.Schedule) *rnGauges {
			t.Helper()
			rs := e.ResultsOf(s.ID)
			if len(rs) == 0 {
				t.Fatalf("no results for %s", s.Name)
			}
			return rnGaugesOf(rs[len(rs)-1])
		}
		// A new schedule's pair starts at 0.
		c := e.Add(rnSched("c", 1440))
		e.Changed()
		ss := e.Expo()
		rnWantSeries(t, ss, c.ID, rnSeries{Name: "c"})
		// A deleted schedule's series go away, and the others' stay.
		e.Delete(b.ID)
		e.Changed()
		ss = e.Expo()
		rnWantNoSeries(t, ss, b.ID)
		rnWantSeries(t, ss, a.ID, rnSeries{Name: "a", Succeeded: 1, Last: last(a)})
		// A renamed schedule shows its new name, in the format's escapes.
		a.Name = `Gran's "den" \ home`
		a = e.Update(a)
		e.Changed()
		text := string(e.Metrics.Append(nil))
		if !strings.Contains(text, `name="Gran's \"den\" \\ home"`) {
			t.Errorf("the name isn't escaped as the format asks:\n%s", rnScheduleLines(text))
		}
		ss = rnParseExpo(t, text)
		rnWantName(t, ss, a.ID, a.Name) // and the old name has no series left
		rnWantNoSeries(t, ss, b.ID)
	})
}

// rnScheduleLines returns the lines of text that hold schedule series.
func rnScheduleLines(text string) string {
	var out []string
	for _, line := range strings.Split(text, "\n") {
		if strings.HasPrefix(line, "dyno_schedule_") {
			out = append(out, line)
		}
	}
	return strings.Join(out, "\n")
}

// ---- R20: logs ----

func TestR20_EachFinishedRunLogsOneScheduleRunEvent(t *testing.T) {
	rnBubble(t, func(t *testing.T) {
		e := rnNewEnv(t)
		type run struct {
			script  rnScript
			kind    string // the schedule's; light when empty
			outcome string // the event's result
			level   string
			code    string // the failure's error.code
			text    string // what an internal failure's error.message must hold
		}
		far := func(code string, phase nodetest.Phase) run {
			return run{script: rnFail(5*time.Second, rnErr(code, phase)), outcome: "failed", level: "WARN", code: code}
		}
		runs := []run{
			{script: rnOK(90 * time.Second), kind: schedule.Full, outcome: "succeeded", level: "INFO"},
			far(nodetest.CodeUnreachable, ""),
			far(nodetest.CodeTimeout, nodetest.PhaseLatency),
			far(nodetest.CodeNotDyno, ""),
			far(nodetest.CodeHTTP, nodetest.PhaseDownload),
			far(nodetest.CodeStream, nodetest.PhaseUpload),
			far(nodetest.CodeBadFarEnd, ""),
			{script: rnPanics(5*time.Second, "kaboom 42"), outcome: "failed", level: "ERROR", code: scheduler.CodeInternal, text: "kaboom 42"},
			{script: rnFail(5*time.Second, errors.New("disk on fire 44")), kind: schedule.Full, outcome: "failed", level: "ERROR", code: scheduler.CodeInternal, text: "disk on fire 44"},
		}
		ids := make([]int64, len(runs))
		kinds := make([]string, len(runs))
		for i, r := range runs {
			s := rnSched("r"+strconv.Itoa(i), 60)
			if r.kind != "" {
				s.Kind = r.kind
			}
			s = e.Add(s)
			e.Fake.On(s.FarEnd, r.script)
			ids[i], kinds[i] = s.ID, s.Kind
		}
		e.Start()
		e.GoTo(rnMin(70))
		events := e.Logs.Events(t, "schedule.run")
		if len(events) != len(runs) {
			t.Fatalf("%d schedule.run events, want %d: one per finished run", len(events), len(runs))
		}
		byID := map[int64]map[string]any{}
		for _, ev := range events {
			id, _ := rnNumber(ev["schedule_id"])
			byID[int64(id)] = ev
		}
		for i, r := range runs {
			ev := byID[ids[i]]
			if ev == nil {
				t.Errorf("run %d (%s): no event with its schedule_id %d", i, r.code, ids[i])
				continue
			}
			who := fmt.Sprintf("run %d (%q)", i, r.code)
			if ev["result"] != r.outcome || ev["level"] != r.level || ev["kind"] != kinds[i] {
				t.Errorf("%s: result %v, level %v, kind %v; want %q, %q, %q", who, ev["result"], ev["level"], ev["kind"], r.outcome, r.level, kinds[i])
			}
			if d, ok := rnNumber(ev["duration_ms"]); !ok || math.Abs(d-float64(r.script.Take.Milliseconds())) > 1000 {
				t.Errorf("%s: duration_ms %v, want about %d", who, ev["duration_ms"], r.script.Take.Milliseconds())
			}
			code, hasCode := ev["error.code"]
			msg, hasMsg := ev["error.message"]
			if r.code == "" {
				if hasCode || hasMsg {
					t.Errorf("%s: a success carries error keys: %v, %v", who, code, msg)
				}
				continue
			}
			text, _ := msg.(string)
			if code != r.code || text == "" || !strings.Contains(text, r.text) {
				t.Errorf("%s: error.code %v and error.message %q, want %q and a message holding %q", who, code, text, r.code, r.text)
			}
		}
	})
}

func TestR20_CanceledRunsLogCanceled(t *testing.T) {
	rnBubble(t, func(t *testing.T) {
		e := rnNewEnv(t)
		a := e.Add(rnSched("a", 15)) // its Runner returns a canceled error on its own
		e.Fake.On(a.FarEnd, rnFail(10*time.Second, rnErr(nodetest.CodeCanceled, nodetest.PhaseDownload)))
		b := e.Add(rnSched("b", 15)) // is deleted during its run
		e.Fake.On(b.FarEnd, rnOK(10*time.Minute))
		e.Start()
		e.GoTo(rnMin(20))
		e.Delete(b.ID)
		e.Changed()
		e.GoTo(rnMin(21))
		events := e.Logs.Events(t, "schedule.run")
		if len(events) != 2 {
			t.Fatalf("%d schedule.run events, want 2: %v", len(events), events)
		}
		for _, ev := range events {
			id, _ := rnNumber(ev["schedule_id"])
			if int64(id) != a.ID && int64(id) != b.ID {
				t.Errorf("an event for schedule %v", ev["schedule_id"])
			}
			if _, ok := rnNumber(ev["duration_ms"]); !ok || ev["result"] != "canceled" || ev["kind"] != schedule.Light {
				t.Errorf("event %v, want result canceled, kind light and a duration_ms", ev)
			}
		}
	})
}

func TestR20_CancelingRunLogsTheStoppedRunAsCanceled(t *testing.T) {
	rnBubble(t, func(t *testing.T) {
		e := rnNewEnv(t)
		a := e.Add(rnSched("a", 15))
		e.Fake.On(a.FarEnd, rnOK(10*time.Minute))
		e.Start()
		e.GoTo(rnMin(16))
		e.Cancel()
		e.Stop() // Run has returned when this does, so the log can be read
		events := e.Logs.Events(t, "schedule.run")
		if len(events) != 1 {
			t.Fatalf("%d schedule.run events, want 1: %v", len(events), events)
		}
		ev := events[0]
		if id, _ := rnNumber(ev["schedule_id"]); int64(id) != a.ID {
			t.Errorf("schedule_id %v, want %d", ev["schedule_id"], a.ID)
		}
		if _, ok := rnNumber(ev["duration_ms"]); !ok || ev["result"] != "canceled" || ev["kind"] != schedule.Light {
			t.Errorf("event %v, want result canceled, kind light and a duration_ms", ev)
		}
	})
}

func TestR20_NilLogUsesTheDefaultLogger(t *testing.T) {
	var buf rnLogs
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(old) })
	rnBubble(t, func(t *testing.T) {
		e := rnNewEnv(t, func(e *rnEnv) { e.Sched.Log = nil })
		a := e.Add(rnSched("a", 15))
		e.Start()
		e.GoTo(rnMin(16))
		events := buf.Events(t, "schedule.run")
		if len(events) != 1 {
			t.Fatalf("%d schedule.run events in the default logger, want 1", len(events))
		}
		if id, _ := rnNumber(events[0]["schedule_id"]); int64(id) != a.ID || events[0]["result"] != "succeeded" {
			t.Errorf("event %v", events[0])
		}
	})
}

// ---- R21: a run keeps what it started with ----

func TestR21_RunKeepsTheFarEndItStartedWith(t *testing.T) {
	rnBubble(t, func(t *testing.T) {
		e := rnNewEnv(t)
		a := e.Add(rnSched("old", 30))
		e.Fake.On(rnFar("old"), rnOK(5*time.Minute))
		e.Fake.On(rnFar("new"), rnOK(time.Minute))
		e.Start()
		e.GoTo(rnMin(32)) // the run from 00:30 is going
		a.FarEnd = rnFar("new")
		a = e.Update(a)
		e.Changed()
		e.GoTo(rnMin(36))
		old := e.Fake.Calls(rnFar("old"))
		if len(old) != 1 || old[0].CtxEnded {
			t.Errorf("the run against the old far end: %d calls, ctx ended %v; editing must not stop it", len(old), len(old) == 1 && old[0].CtxEnded)
		}
		rs := e.ResultsOf(a.ID)
		if len(rs) != 1 || rs[0].Failure != nil || rs[0].FarEnd != rnFar("old") {
			t.Fatalf("results after the run %+v, want one success against %s", rs, rnFar("old"))
		}
		e.GoTo(rnMin(62))
		e.WantStarts(rnFar("new"), rnMin(60))
		rs = e.ResultsOf(a.ID)
		if len(rs) != 2 || rs[1].FarEnd != rnFar("new") {
			t.Errorf("results %+v, want the next run's against %s", rs, rnFar("new"))
		}
	})
}

func TestR21_ScheduleCanBePausedDuringItsRunAndStillStoresIt(t *testing.T) {
	rnBubble(t, func(t *testing.T) {
		e := rnNewEnv(t)
		a := e.Add(rnSched("a", 30))
		e.Fake.On(a.FarEnd, rnOK(5*time.Minute))
		e.Start()
		e.GoTo(rnMin(32))
		a.Paused = true
		a = e.Update(a)
		e.Changed()
		e.GoTo(rnMin(36))
		if c := e.Fake.Calls(a.FarEnd); len(c) != 1 || c[0].CtxEnded {
			t.Errorf("pausing the schedule stopped its run: %+v", c)
		}
		rs := e.ResultsOf(a.ID)
		if len(rs) != 1 || rs[0].Failure != nil {
			t.Errorf("results %+v, want the paused run's success", rs)
		}
		e.GoTo(2 * time.Hour)
		e.WantStarts(a.FarEnd, rnMin(30)) // and no more, while paused
	})
}

// ---- R22: concurrent use ----

func TestR22_ConcurrentCallsDuringRunsNeitherRaceNorBlock(t *testing.T) {
	rnBubble(t, func(t *testing.T) {
		e := rnNewEnv(t)
		a := e.Add(rnSched("a", 15))
		e.Fake.On(a.FarEnd, rnOK(10*time.Minute))
		b := rnSched("b", 1440)
		b.OffsetMinutes, b.Paused = 600, true
		b = e.Add(b)
		e.Fake.On(b.FarEnd, rnOK(time.Minute))
		e.Start()
		e.GoTo(rnMin(16)) // a's run, which began at 00:15, goes on until 00:25

		const workers, rounds = 6, 40
		var wg sync.WaitGroup
		for w := range workers {
			wg.Add(1)
			go func() {
				defer wg.Done()
				defer func() {
					if p := recover(); p != nil {
						t.Errorf("worker %d: panic: %v", w, p)
					}
				}()
				mine := b
				for i := range rounds {
					began := time.Now()
					e.Sched.Changed()
					if !e.Sched.Status(a.ID).Running {
						t.Errorf("worker %d: Status(a) isn't Running during a's run", w)
					}
					_ = e.Sched.RunNow(a.ID)
					_ = e.Sched.RunNow(b.ID)
					if w == 0 { // edits race with the scheduler's reads
						mine.Name = fmt.Sprintf("b%d", i)
						if _, err := e.Store.UpdateSchedule(mine); err != nil {
							t.Errorf("UpdateSchedule: %v", err)
						}
					}
					if d := time.Since(began); d > rnSlack {
						t.Errorf("worker %d: its calls took %v, as if they waited for the run", w, d)
					}
					time.Sleep(10 * time.Millisecond)
				}
			}()
		}
		wg.Wait()
		if n := len(e.Fake.Calls(a.FarEnd)); n != 1 {
			t.Errorf("a was run %d times, want once: RunNow doesn't queue a running schedule", n)
		}
		e.GoTo(rnMin(29))
		e.WantStarts(a.FarEnd, rnMin(15))
		e.WantStarts(b.FarEnd, rnMin(25)) // asked for 240 times, run once, when a's run ended
	})
}

func TestR22_CallsAfterRunReturnedNeitherBlockNorPanic(t *testing.T) {
	rnBubble(t, func(t *testing.T) {
		e := rnNewEnv(t)
		a := e.Add(rnSched("a", 15))
		e.Start()
		e.GoTo(rnMin(5))
		e.Cancel()
		e.Settle()
		if !e.RunReturned() {
			t.Fatal("Run didn't return")
		}
		var wg sync.WaitGroup
		var finished atomic.Int32
		for range 4 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				defer func() {
					if p := recover(); p != nil {
						t.Errorf("panic after Run returned: %v", p)
					}
				}()
				e.Sched.Changed()
				_ = e.Sched.RunNow(a.ID)
				_ = e.Sched.Status(a.ID)
				finished.Add(1)
			}()
		}
		e.Settle()
		if n := finished.Load(); n != 4 {
			t.Fatalf("%d of 4 goroutines came back from Changed, RunNow and Status", n)
		}
		wg.Wait()
	})
}
