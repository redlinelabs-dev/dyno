package scheduler

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"runtime/debug"
	"strings"
	"time"
	"unicode"

	"github.com/redlinelabs-dev/dyno/server/internal/nodetest"
	"github.com/redlinelabs-dev/dyno/server/internal/result"
	"github.com/redlinelabs-dev/dyno/server/internal/schedule"
	"github.com/redlinelabs-dev/dyno/server/internal/stats"
)

// How a run ended, as schedule.run logs its result.
const (
	statusSucceeded = "succeeded"
	statusFailed    = "failed"
	statusCanceled  = "canceled"
)

// The error.code of a read or a write of the store that failed (ADR-0020).
const (
	codeStoreRead  = "store_read_failed"
	codeStoreWrite = "store_write_failed"
)

// maxMessage caps the text of an internal failure, which can quote a panic,
// and maxStack the stack trace a panic adds to its log event.
const (
	maxMessage = 240
	maxStack   = 4 << 10
)

// The path-change rule of ADR-0028. The baseline is the median idle latency
// of a schedule's newest baselineRuns good results against the same far end,
// and needs baselineMin of them. A run is flagged when its idle latency is
// more than pathFactor times the baseline and at least pathMinMs above it.
const (
	baselineRuns = 10
	baselineMin  = 3
	pathFactor   = 2
	pathMinMs    = 10
)

// verdict is how a run ended.
type verdict struct {
	// status is statusSucceeded, statusFailed or statusCanceled.
	status string

	// failure is why a failed run failed. The result stores it and the log
	// reports it.
	failure *result.Failure

	// stack is where the Runner panicked, for the log.
	stack []byte
}

// failed is the verdict of a run that failed with a code, a phase and a
// message.
func failed(code, phase, message string) verdict {
	return verdict{status: statusFailed, failure: &result.Failure{Code: code, Phase: phase, Message: message}}
}

// internal is the verdict of a run that failed inside dyno. The log says why.
func internal(message string, stack []byte) verdict {
	v := failed(CodeInternal, "", tidy(message, maxMessage))
	v.stack = stack
	return v
}

// panicError is a panic in the Runner, recovered.
type panicError struct {
	value any
	stack []byte
}

func (p *panicError) Error() string { return fmt.Sprint(p.value) }

// call runs the Runner. A panic in it comes back as a *panicError, so a bug in
// a test never stops the schedules.
func (s *Scheduler) call(ctx context.Context, o nodetest.Options) (out nodetest.Outcome, err error) {
	defer func() {
		if p := recover(); p != nil {
			out, err = nodetest.Outcome{}, &panicError{value: p, stack: debug.Stack()}
		}
	}()
	return s.Test(ctx, o)
}

// execute runs the schedule at the head of the queue, stores its result and
// logs the run. A run stopped on purpose stores nothing.
func (s *Scheduler) execute(ctx context.Context) {
	if ctx.Err() != nil {
		return
	}
	runCtx, cancel := context.WithTimeout(ctx, RunTimeout)
	defer cancel()

	s.mu.Lock()
	s.enqueueDue(time.Now())
	e := s.pop()
	if e == nil {
		s.mu.Unlock()
		return
	}
	e.running, e.cancel = true, cancel
	sc := e.sc // the run keeps what it started with, whatever is edited meanwhile
	s.mu.Unlock()

	kind := nodetest.Light
	if sc.Kind == schedule.Full {
		kind = nodetest.Full
	}
	start := time.Now()
	out, err := s.call(runCtx, nodetest.Options{FarEnd: sc.FarEnd, Kind: kind})
	end := time.Now()

	sub := out.Submission
	sub.TestID, sub.Device, sub.Client = "", "", "dyno/"+s.Version
	v := s.judge(ctx, runCtx, e, &sub, err, end)
	if v.status != statusCanceled {
		saved, serr := s.Store.Add(s.build(sc, sub, v.failure, end))
		if serr != nil {
			v = verdict{status: statusFailed, failure: &result.Failure{Code: codeStoreWrite, Message: serr.Error()}}
		} else {
			s.Metrics.ScheduleRan(sc.ID, saved)
		}
	}
	s.finish(e, end)
	s.logRun(context.WithoutCancel(ctx), sc.ID, kind, end.Sub(start), v)
}

// judge decides how a run ended, from what the Runner returned (sub, its
// figures, and err) and when (end). It checks the figures of a success.
//
// A run stopped on purpose, by a delete or by shutdown, stores nothing, and
// neither does one the Runner reports as canceled. A panic, an error that
// isn't a *nodetest.Error, a failure with no code and figures that can't be
// stored are internal failures: a bug in dyno, not a far end. A run that hit
// its deadline is a timeout whatever it returned (docs/api.md: "the run
// passed 5 minutes").
//
// Reading the Runner's error is the Runner's code running here, outside call's
// recover: a typed-nil *nodetest.Error, or an error whose Error or Unwrap
// panics, would stop the process. Such a panic is an internal failure too.
func (s *Scheduler) judge(ctx, runCtx context.Context, e *entry, sub *result.Submission, err error, end time.Time) (v verdict) {
	defer func() {
		if p := recover(); p != nil {
			v = internal("the test's error couldn't be read: "+fmt.Sprint(p), debug.Stack())
		}
	}()
	s.mu.Lock()
	gone := e.deleted
	s.mu.Unlock()
	if gone || ctx.Err() != nil {
		return verdict{status: statusCanceled}
	}
	if pe, ok := errors.AsType[*panicError](err); ok {
		return internal("the test crashed inside dyno: "+pe.Error(), pe.stack)
	}
	nerr, _ := errors.AsType[*nodetest.Error](err)
	if hitDeadline(runCtx, err, end) {
		if nerr != nil && nerr.Code == nodetest.CodeTimeout {
			return failed(nerr.Code, string(nerr.Phase), nerr.Error())
		}
		te := &nodetest.Error{Code: nodetest.CodeTimeout, Err: fmt.Errorf("the test ran longer than %v", RunTimeout)}
		if nerr != nil {
			te.Phase = nerr.Phase
		}
		return failed(te.Code, string(te.Phase), te.Error())
	}
	switch {
	case err == nil:
		if verr := sub.Validate(); verr != nil {
			return internal("the test returned figures that can't be stored: "+verr.Error(), nil)
		}
		return verdict{status: statusSucceeded}
	case nerr == nil:
		return internal("the test failed inside dyno: "+err.Error(), nil)
	case nerr.Code == nodetest.CodeCanceled:
		return verdict{status: statusCanceled}
	case nerr.Code == "":
		return internal("the test failed without a reason: "+nerr.Error(), nil)
	}
	return failed(nerr.Code, string(nerr.Phase), nerr.Error())
}

// hitDeadline reports whether a run that returned err at end ran into its
// deadline. An error at or after the deadline did: the context ending is what
// stopped it, whatever the Runner called the error. A success did only if it
// came after the deadline; one that finished at the very instant used its 5
// minutes and no more.
func hitDeadline(runCtx context.Context, err error, end time.Time) bool {
	deadline, ok := runCtx.Deadline()
	switch {
	case !ok:
		return false
	case err == nil:
		return end.After(deadline)
	}
	return !end.Before(deadline)
}

// build makes the result to store: the figures of a success, with what is
// derived from them and the path-change flag, or the failure of a run that
// failed, which has no figures.
func (s *Scheduler) build(sc schedule.Schedule, sub result.Submission, failure *result.Failure, at time.Time) result.Result {
	res := result.Result{At: at.UTC().Truncate(time.Millisecond), Source: "schedule", ScheduleID: sc.ID, FarEnd: sc.FarEnd}
	if failure != nil {
		res.Client = sub.Client
		res.Failure = failure
		return res
	}
	res.Submission = sub
	res.Derive()
	res.PathChanged = s.pathChanged(sc, sub.IdleMs)
	return res
}

// pathChanged applies ADR-0028's rule to a run of sc with idle latency idleMs.
// It reads the baseline before the run's own result is stored, so the result
// is never part of it. A baseline it can't read means no flag.
func (s *Scheduler) pathChanged(sc schedule.Schedule, idleMs float64) bool {
	recent, err := s.Store.RecentIdle(sc.ID, sc.FarEnd, baselineRuns)
	if err != nil || len(recent) < baselineMin {
		return false
	}
	base := stats.Median(recent)
	return idleMs > pathFactor*base && idleMs-base >= pathMinMs
}

// finish ends e's run. The schedule's next time is the first on its grid after
// end, unless it is paused or gone.
func (s *Scheduler) finish(e *entry, end time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e.running, e.cancel = false, nil
	if !e.deleted && !e.sc.Paused {
		e.next = e.sc.Next(end)
	}
}

// logRun logs the schedule.run event of a finished run: one per run (ADR-0020).
// A failure adds its error.code and error.message, at WARN when a far end or the
// network is to blame and at ERROR when dyno is. A run stopped on purpose is
// INFO, and has neither, since nobody has to act on it.
func (s *Scheduler) logRun(ctx context.Context, id int64, kind nodetest.Kind, took time.Duration, v verdict) {
	attrs := make([]slog.Attr, 0, 7)
	attrs = append(attrs,
		slog.String("result", v.status),
		slog.Int64("schedule_id", id),
		slog.String("kind", string(kind)),
		slog.Float64("duration_ms", float64(took.Microseconds())/1e3),
	)
	level := slog.LevelInfo
	if f := v.failure; f != nil {
		level = slog.LevelWarn
		if f.Code == CodeInternal || f.Code == codeStoreWrite {
			level = slog.LevelError
		}
		attrs = append(attrs, slog.String("error.code", f.Code), slog.String("error.message", f.Message))
		if len(v.stack) > 0 {
			attrs = append(attrs, slog.String("error.stack", string(v.stack[:min(len(v.stack), maxStack)])))
		}
	}
	s.logger().LogAttrs(ctx, level, "schedule.run", attrs...)
}

// tidy makes s fit to store and show: valid text without control characters,
// at most limit bytes.
func tidy(s string, limit int) string {
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, strings.ToValidUTF8(s, ""))
	s = strings.TrimSpace(s)
	if len(s) > limit {
		s = strings.ToValidUTF8(s[:limit], "")
	}
	return s
}
