package scheduler_test

// The harness for the scheduler's tests: a fake Runner that records every call
// and does what a script says, an environment that opens a store and runs the
// scheduler inside a testing/synctest bubble, a capture for its logs, and a
// reader for the exposition of its metrics.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"os"
	"runtime/debug"
	"runtime/pprof"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/redlinelabs-dev/dyno/server/internal/metrics"
	"github.com/redlinelabs-dev/dyno/server/internal/nodetest"
	"github.com/redlinelabs-dev/dyno/server/internal/result"
	"github.com/redlinelabs-dev/dyno/server/internal/schedule"
	"github.com/redlinelabs-dev/dyno/server/internal/scheduler"
	"github.com/redlinelabs-dev/dyno/server/internal/store"
)

const (
	rnVersion  = "v0.7.3"         // the Version the scheduler under test is given
	rnIdle     = 8.4              // idle latency, in ms, of the fake's default outcome
	rnSlack    = time.Second      // how far from its time an event may land
	rnWatchdog = 60 * time.Second // real time one test may take before it counts as stuck
)

// rnT0 is where every synctest bubble's clock starts.
var rnT0 = time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)

// rnMin returns n minutes.
func rnMin(n int) time.Duration { return time.Duration(n) * time.Minute }

// rnAdvance moves the bubble's clock on by d and returns once everything that
// came due has run and blocked again.
func rnAdvance(d time.Duration) {
	time.Sleep(d)
	synctest.Wait()
}

// rnGuard, deferred, turns a panic into a failure of t, so that one call that
// panics doesn't end the package's other tests.
func rnGuard(t *testing.T) {
	if p := recover(); p != nil {
		t.Fatalf("panic: %v\n%s", p, debug.Stack())
	}
}

// rnBubble runs f in a synctest bubble. A call stuck on something the bubble's
// clock can't move, such as a mutex, would hang until the package's timeout.
// The watchdog, which runs outside the bubble on the real clock, dumps every
// goroutine and ends the run sooner.
func rnBubble(t *testing.T, f func(t *testing.T)) {
	t.Helper()
	wd := time.AfterFunc(rnWatchdog, func() {
		pprof.Lookup("goroutine").WriteTo(os.Stderr, 2)
		panic(fmt.Sprintf("%s: stuck for %v of real time", t.Name(), rnWatchdog))
	})
	defer wd.Stop()
	synctest.Test(t, func(t *testing.T) {
		defer rnGuard(t)
		f(t)
	})
}

// ---- the fake Runner ----

// rnScript says what the fake Runner does for one call.
type rnScript struct {
	Take     time.Duration    // how long the run takes, unless it's canceled or Run stops first; RunTimeout passing doesn't cut it short (Block is the run the deadline stops)
	StopTake time.Duration    // how long it takes to wind down once its ctx has ended
	Block    bool             // run until the ctx ends, ignoring Take
	Ignore   bool             // run for all of Take whatever happens to the ctx, then finish as if nothing had
	Panic    any              // panic with this after Take, when not nil
	Err      error            // return this after Take instead of Out, when not nil
	Out      nodetest.Outcome // what a run that succeeds returns
}

// rnOK is a run that succeeds after take.
func rnOK(take time.Duration) rnScript { return rnScript{Take: take, Out: rnOutcome(rnIdle)} }

// rnFail is a run that returns err after take.
func rnFail(take time.Duration, err error) rnScript { return rnScript{Take: take, Err: err} }

// rnPanics is a run that panics with v after take.
func rnPanics(take time.Duration, v any) rnScript { return rnScript{Take: take, Panic: v} }

// rnErr is what nodetest.Run fails with for code, at phase.
func rnErr(code string, phase nodetest.Phase) *nodetest.Error {
	return &nodetest.Error{Code: code, Phase: phase, Err: errors.New("what the far end did instead")}
}

// rnOutcome is a finished test with idle latency idleMs.
func rnOutcome(idleMs float64) nodetest.Outcome {
	return nodetest.Outcome{
		Submission: result.Submission{
			DownloadMbps: 940.5, UploadMbps: 410.25, IdleMs: idleMs,
			LoadedDownMs: 21, LoadedUpMs: 39.5, JitterMs: 0.82, Streams: 5,
			Detail: json.RawMessage(`{"idle":[[0.05,8.1]],"rate":[[1.2,870.4,0]]}`),
		},
		FarEndName: "dyno", FarEndVersion: "v0.1.0",
	}
}

// rnCtxError is what nodetest.Run returns once its ctx has ended.
func rnCtxError(ctx context.Context) error {
	code := nodetest.CodeCanceled
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		code = nodetest.CodeTimeout
	}
	return &nodetest.Error{Code: code, Phase: nodetest.PhaseDownload, Err: ctx.Err()}
}

// rnCall is one call of the fake Runner.
type rnCall struct {
	Seq         int       // 1 for the first call of all
	Start       time.Time // the bubble's time when the call began
	End         time.Time // and when it ended; zero until it has
	Finished    bool      // the call has returned or panicked
	Panicked    bool
	Opts        nodetest.Options
	Deadline    time.Time // the ctx's deadline
	HasDeadline bool
	CtxEnded    bool // the ctx ended while the call was going
	CtxEndedAt  time.Time
	CtxErr      error // the ctx's error then
	Returned    error // what the call returned, when it returned an error
}

// rnFake is a scheduler.Runner that records its calls. Each far end has a
// script; one without gets rnOK(0).
type rnFake struct {
	mu          sync.Mutex
	scripts     map[string]func(n int) rnScript
	perFar      map[string]int
	calls       []*rnCall
	inFlight    int
	maxInFlight int
	stop        <-chan struct{} // Run's own ctx, so a run past its deadline still ends when Run stops
}

func rnNewFake() *rnFake {
	return &rnFake{scripts: map[string]func(int) rnScript{}, perFar: map[string]int{}}
}

// On sets what every call for farEnd does.
func (f *rnFake) On(farEnd string, s rnScript) {
	f.OnCall(farEnd, func(int) rnScript { return s })
}

// OnCall sets what the n-th call for farEnd does, counting from 1.
func (f *rnFake) OnCall(farEnd string, script func(n int) rnScript) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.scripts[farEnd] = script
}

// Run is the scheduler.Runner.
func (f *rnFake) Run(ctx context.Context, o nodetest.Options) (nodetest.Outcome, error) {
	f.mu.Lock()
	f.perFar[o.FarEnd]++
	n := f.perFar[o.FarEnd]
	c := &rnCall{Seq: len(f.calls) + 1, Start: time.Now(), Opts: o}
	c.Deadline, c.HasDeadline = ctx.Deadline()
	f.calls = append(f.calls, c)
	f.inFlight++
	f.maxInFlight = max(f.maxInFlight, f.inFlight)
	script := f.scripts[o.FarEnd]
	f.mu.Unlock()

	s := rnScript{Out: rnOutcome(rnIdle)}
	if script != nil {
		s = script(n)
	}
	var returned error
	defer func() {
		f.mu.Lock()
		defer f.mu.Unlock()
		c.End, c.Finished, c.Returned = time.Now(), true, returned
		f.inFlight--
	}()

	ended := false
	switch {
	case s.Block:
		<-ctx.Done()
		ended = true
	case s.Ignore:
		time.Sleep(s.Take)
		if err := ctx.Err(); err != nil { // noticed only now
			f.mu.Lock()
			c.CtxEnded, c.CtxEndedAt, c.CtxErr = true, time.Now(), err
			f.mu.Unlock()
		}
	case s.Take > 0:
		tm := time.NewTimer(s.Take)
		defer tm.Stop()
		select {
		case <-tm.C:
		case <-ctx.Done():
			// A run longer than RunTimeout overruns its deadline, as R4's do: only
			// a cancel, or Run stopping, ends it early.
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				select {
				case <-tm.C:
				case <-f.stop:
					ended = true
				}
			} else {
				ended = true
			}
		}
	}
	if ended {
		f.mu.Lock()
		c.CtxEnded, c.CtxEndedAt, c.CtxErr = true, time.Now(), ctx.Err()
		f.mu.Unlock()
		if s.StopTake > 0 {
			time.Sleep(s.StopTake)
		}
		returned = rnCtxError(ctx)
		return nodetest.Outcome{}, returned
	}
	if s.Panic != nil {
		f.mu.Lock()
		c.Panicked = true
		f.mu.Unlock()
		panic(s.Panic)
	}
	if s.Err != nil {
		returned = s.Err
		return nodetest.Outcome{}, s.Err
	}
	return s.Out, nil
}

// Calls returns copies of the calls for farEnd, oldest first. "" means all of
// them.
func (f *rnFake) Calls(farEnd string) []rnCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []rnCall
	for _, c := range f.calls {
		if farEnd == "" || c.Opts.FarEnd == farEnd {
			out = append(out, *c)
		}
	}
	return out
}

// MaxInFlight is the most calls that were going at once.
func (f *rnFake) MaxInFlight() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.maxInFlight
}

// ---- logs ----

// rnLogs collects what a slog handler writes.
type rnLogs struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (l *rnLogs) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.Write(p)
}

// Events returns the logged lines whose msg is msg, as JSON objects, oldest
// first.
func (l *rnLogs) Events(t *testing.T, msg string) []map[string]any {
	t.Helper()
	l.mu.Lock()
	text := l.buf.String()
	l.mu.Unlock()
	var out []map[string]any
	for _, line := range strings.Split(text, "\n") {
		if line == "" {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("a log line isn't JSON: %q: %v", line, err)
		}
		if m["msg"] == msg {
			out = append(out, m)
		}
	}
	return out
}

// rnNumber reads a JSON number, or a string holding one.
func rnNumber(v any) (float64, bool) {
	switch v := v.(type) {
	case float64:
		return v, true
	case string:
		f, err := strconv.ParseFloat(v, 64)
		return f, err == nil
	}
	return 0, false
}

// ---- the environment ----

// rnEnv is a store, a fake Runner and a scheduler under test, in a bubble.
type rnEnv struct {
	t       *testing.T
	Store   *store.Store
	Fake    *rnFake
	Sched   *scheduler.Scheduler
	Metrics *metrics.Metrics // nil unless the test asked for it
	Logs    *rnLogs

	cancel     context.CancelFunc
	done       chan struct{} // closed once Run has returned
	returnedAt time.Time     // when it did
	started    bool
	stopped    bool
}

// rnWithMetrics gives the scheduler metrics.
func rnWithMetrics(e *rnEnv) {
	e.Metrics = metrics.New(rnVersion)
	e.Sched.Metrics = e.Metrics
}

// rnWithBusy gives the scheduler a Busy func.
func rnWithBusy(busy func() bool) func(*rnEnv) {
	return func(e *rnEnv) { e.Sched.Busy = busy }
}

// rnNewEnv opens a store in a new directory, and makes a scheduler that logs
// into e.Logs and runs the fake Runner. Call it inside a bubble. Nothing runs
// until Start. The test's cleanup stops everything.
func rnNewEnv(t *testing.T, opts ...func(*rnEnv)) *rnEnv {
	t.Helper()
	st, err := store.Open(t.TempDir())
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

// Start runs the scheduler in a goroutine and lets it settle.
func (e *rnEnv) Start() {
	e.t.Helper()
	if e.started {
		e.t.Fatal("Start called twice")
	}
	e.started = true
	ctx, cancel := context.WithCancel(context.Background())
	e.cancel = cancel
	e.Fake.stop = ctx.Done()
	e.done = make(chan struct{})
	go func() {
		defer close(e.done)
		defer func() {
			if p := recover(); p != nil {
				e.t.Errorf("Run panicked: %v\n%s", p, debug.Stack())
			}
		}()
		e.Sched.Run(ctx)
		e.returnedAt = time.Now()
	}()
	synctest.Wait()
}

// Cancel ends Run's ctx.
func (e *rnEnv) Cancel() {
	if e.cancel != nil {
		e.cancel()
	}
}

// RunReturned reports whether Run has returned.
func (e *rnEnv) RunReturned() bool {
	select {
	case <-e.done:
		return true
	default:
		return false
	}
}

// Stop ends Run, waits for it to return and closes the store. It is safe to
// call more than once.
func (e *rnEnv) Stop() {
	if e.stopped {
		return
	}
	e.stopped = true
	if e.started {
		e.cancel()
		select {
		case <-e.done:
		case <-time.After(10 * time.Minute):
			e.t.Errorf("Run didn't return within 10 minutes of its ctx ending")
		}
	}
	if err := e.Store.Close(); err != nil {
		e.t.Errorf("closing the store: %v", err)
	}
}

// Settle waits until every goroutine in the bubble is blocked.
func (e *rnEnv) Settle() { synctest.Wait() }

// Advance moves the clock on by d and lets everything due run.
func (e *rnEnv) Advance(d time.Duration) { rnAdvance(d) }

// Now is the bubble's clock as an offset from rnT0.
func (e *rnEnv) Now() time.Duration { return time.Since(rnT0) }

// GoTo moves the clock on to rnT0 plus off and lets everything due run.
func (e *rnEnv) GoTo(off time.Duration) {
	e.t.Helper()
	d := off - e.Now()
	if d < 0 {
		e.t.Fatalf("GoTo(%v): the clock is already at %v", off, e.Now())
	}
	rnAdvance(d)
}

// Add stores a schedule.
func (e *rnEnv) Add(s schedule.Schedule) schedule.Schedule {
	e.t.Helper()
	got, err := e.Store.AddSchedule(s)
	if err != nil {
		e.t.Fatalf("AddSchedule: %v", err)
	}
	return got
}

// Update saves a schedule over the one with its id.
func (e *rnEnv) Update(s schedule.Schedule) schedule.Schedule {
	e.t.Helper()
	got, err := e.Store.UpdateSchedule(s)
	if err != nil {
		e.t.Fatalf("UpdateSchedule: %v", err)
	}
	return got
}

// Delete deletes a schedule from the store. It doesn't tell the scheduler.
func (e *rnEnv) Delete(id int64) {
	e.t.Helper()
	if err := e.Store.DeleteSchedule(id); err != nil {
		e.t.Fatalf("DeleteSchedule: %v", err)
	}
}

// Seed stores a result.
func (e *rnEnv) Seed(r result.Result) result.Result {
	e.t.Helper()
	got, err := e.Store.Add(r)
	if err != nil {
		e.t.Fatalf("Add: %v", err)
	}
	return got
}

// Changed, RunNow and Status call the scheduler.
func (e *rnEnv) Changed() {
	e.t.Helper()
	e.Sched.Changed()
}

func (e *rnEnv) RunNow(id int64) error {
	e.t.Helper()
	return e.Sched.RunNow(id)
}

func (e *rnEnv) Status(id int64) schedule.Status {
	e.t.Helper()
	return e.Sched.Status(id)
}

// Results returns every stored result, oldest first, without detail.
func (e *rnEnv) Results() []result.Result {
	e.t.Helper()
	rs, err := e.Store.List(100000, 0, "")
	if err != nil {
		e.t.Fatalf("List: %v", err)
	}
	slices.Reverse(rs)
	return rs
}

// ResultsOf returns the results with scheduleID, oldest first, without detail.
func (e *rnEnv) ResultsOf(scheduleID int64) []result.Result {
	e.t.Helper()
	var out []result.Result
	for _, r := range e.Results() {
		if r.ScheduleID == scheduleID {
			out = append(out, r)
		}
	}
	return out
}

// WantStarts checks that the Runner was called for farEnd at exactly these
// offsets from rnT0, give or take rnSlack, and at no other time.
func (e *rnEnv) WantStarts(farEnd string, want ...time.Duration) {
	e.t.Helper()
	var got []time.Duration
	for _, c := range e.Fake.Calls(farEnd) {
		got = append(got, c.Start.Sub(rnT0))
	}
	if len(got) != len(want) {
		e.t.Errorf("%s: the Runner was called at %v, want %v", farEnd, got, want)
		return
	}
	for i := range got {
		if d := got[i] - want[i]; d < -rnSlack || d > rnSlack {
			e.t.Errorf("%s: the Runner was called at %v, want %v", farEnd, got, want)
			return
		}
	}
}

// Expo reads the metrics' exposition.
func (e *rnEnv) Expo() []rnSample {
	e.t.Helper()
	return rnParseExpo(e.t, string(e.Metrics.Append(nil)))
}

// rnFar is the far end that rnSched(name, ...) points at.
func rnFar(name string) string { return "http://" + name + ":3966" }

// rnSched is a light schedule named name, against rnFar(name), that runs every
// `every` minutes from midnight. Change its fields before storing it.
func rnSched(name string, every int) schedule.Schedule {
	return schedule.Schedule{Name: name, FarEnd: rnFar(name), Kind: schedule.Light, EveryMinutes: every}
}

// ---- reading the exposition ----

// rnSample is one line of an exposition.
type rnSample struct {
	Name   string
	Labels map[string]string
	Value  float64
}

// rnParseExpo reads every sample of a text exposition, undoing the label
// escapes. It fails t on a line it can't read, and on a series that appears
// twice.
func rnParseExpo(t *testing.T, text string) []rnSample {
	t.Helper()
	if !strings.HasSuffix(text, "\n") {
		t.Fatalf("the exposition doesn't end in a newline")
	}
	var out []rnSample
	seen := map[string]bool{}
	for i, line := range strings.Split(strings.TrimSuffix(text, "\n"), "\n") {
		if strings.HasPrefix(line, "#") {
			continue
		}
		s, err := rnParseSample(line)
		if err != nil {
			t.Fatalf("exposition line %d: %v", i+1, err)
		}
		keys := make([]string, 0, len(s.Labels))
		for k := range s.Labels {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		id := s.Name
		for _, k := range keys {
			id += "\x00" + k + "=" + s.Labels[k]
		}
		if seen[id] {
			t.Fatalf("the series %s%v appears twice", s.Name, s.Labels)
		}
		seen[id] = true
		out = append(out, s)
	}
	return out
}

func rnParseSample(line string) (rnSample, error) {
	s := rnSample{Labels: map[string]string{}}
	i := strings.IndexAny(line, "{ ")
	if i <= 0 {
		return s, fmt.Errorf("no metric name or value in %q", line)
	}
	s.Name = line[:i]
	rest := line[i:]
	if rest[0] == '{' {
		rest = rest[1:]
		for {
			eq := strings.IndexByte(rest, '=')
			if eq <= 0 || len(rest) < eq+2 || rest[eq+1] != '"' {
				return s, fmt.Errorf("bad label in %q", line)
			}
			key := rest[:eq]
			rest = rest[eq+2:]
			var val strings.Builder
			for {
				if rest == "" {
					return s, fmt.Errorf("unterminated label value in %q", line)
				}
				c := rest[0]
				rest = rest[1:]
				if c == '"' {
					break
				}
				if c == '\\' {
					if rest == "" {
						return s, fmt.Errorf("dangling escape in %q", line)
					}
					switch rest[0] {
					case '\\':
						val.WriteByte('\\')
					case '"':
						val.WriteByte('"')
					case 'n':
						val.WriteByte('\n')
					default:
						return s, fmt.Errorf("bad escape \\%c in %q", rest[0], line)
					}
					rest = rest[1:]
					continue
				}
				val.WriteByte(c)
			}
			if _, dup := s.Labels[key]; dup {
				return s, fmt.Errorf("label %s twice in %q", key, line)
			}
			s.Labels[key] = val.String()
			if strings.HasPrefix(rest, ",") {
				rest = rest[1:]
				continue
			}
			if !strings.HasPrefix(rest, "}") {
				return s, fmt.Errorf("label set not closed in %q", line)
			}
			rest = rest[1:]
			break
		}
	}
	if !strings.HasPrefix(rest, " ") || strings.Contains(rest[1:], " ") {
		return s, fmt.Errorf("want exactly one value and no timestamp in %q", line)
	}
	v, err := strconv.ParseFloat(rest[1:], 64)
	if err != nil {
		return s, fmt.Errorf("value in %q: %v", line, err)
	}
	s.Value = v
	return s, nil
}

// rnGauges are one schedule's last-success gauges, in the units the exposition
// uses: seconds, bytes per second and seconds.
type rnGauges struct {
	At, Down, Up, Idle float64
	Loaded             float64 // 0 means the series is absent
}

// rnGaugesOf is what the gauges should read for r, a schedule's newest
// successful result: Mbit/s become bytes per second (x 125000), ms become
// seconds, and the loaded latency is the worse direction.
func rnGaugesOf(r result.Result) *rnGauges {
	return &rnGauges{
		At:     float64(r.At.Unix()),
		Down:   r.DownloadMbps * 125000,
		Up:     r.UploadMbps * 125000,
		Idle:   r.IdleMs / 1000,
		Loaded: max(r.LoadedDownMs, r.LoadedUpMs) / 1000,
	}
}

// rnSeries is what the exposition should hold for one schedule.
type rnSeries struct {
	Name      string // the name label
	Succeeded float64
	Failed    float64
	Last      *rnGauges // nil means no last-success series at all
}

// rnWantSeries checks that ss holds exactly the series in want for schedule
// id, with exactly their labels, and no others.
func rnWantSeries(t *testing.T, ss []rnSample, id int64, want rnSeries) {
	t.Helper()
	sid := strconv.FormatInt(id, 10)
	got := map[string]float64{}
	for _, s := range ss {
		if !strings.HasPrefix(s.Name, "dyno_schedule_") || s.Labels["schedule"] != sid {
			continue
		}
		key, labels := s.Name, 2
		if s.Name == "dyno_schedule_runs_total" {
			key, labels = key+"/"+s.Labels["result"], 3
		}
		if len(s.Labels) != labels {
			t.Errorf("schedule %d: %s has labels %v, want %d of them", id, s.Name, s.Labels, labels)
		}
		if s.Labels["name"] != want.Name {
			t.Errorf("schedule %d: %s has name %q, want %q", id, key, s.Labels["name"], want.Name)
		}
		got[key] = s.Value
	}
	expect := map[string]float64{
		"dyno_schedule_runs_total/succeeded": want.Succeeded,
		"dyno_schedule_runs_total/failed":    want.Failed,
	}
	if g := want.Last; g != nil {
		expect["dyno_schedule_last_success_timestamp_seconds"] = g.At
		expect["dyno_schedule_download_bytes_per_second"] = g.Down
		expect["dyno_schedule_upload_bytes_per_second"] = g.Up
		expect["dyno_schedule_idle_latency_seconds"] = g.Idle
		if g.Loaded != 0 {
			expect["dyno_schedule_loaded_latency_seconds"] = g.Loaded
		}
	}
	for key, w := range expect {
		v, ok := got[key]
		if !ok {
			t.Errorf("schedule %d: no series %s", id, key)
			continue
		}
		tol := 1e-9 * math.Max(1, math.Abs(w))
		if key == "dyno_schedule_last_success_timestamp_seconds" {
			tol = 1
		}
		if math.Abs(v-w) > tol {
			t.Errorf("schedule %d: %s = %v, want %v", id, key, v, w)
		}
	}
	for key := range got {
		if _, ok := expect[key]; !ok {
			t.Errorf("schedule %d: unexpected series %s", id, key)
		}
	}
}

// rnWantNoSeries checks that ss holds no schedule series for id.
func rnWantNoSeries(t *testing.T, ss []rnSample, id int64) {
	t.Helper()
	sid := strconv.FormatInt(id, 10)
	for _, s := range ss {
		if strings.HasPrefix(s.Name, "dyno_schedule_") && s.Labels["schedule"] == sid {
			t.Errorf("schedule %d: the series %s%v should be gone", id, s.Name, s.Labels)
		}
	}
}

// rnWantName checks that every schedule series of id carries name, and that
// there is at least one.
func rnWantName(t *testing.T, ss []rnSample, id int64, name string) {
	t.Helper()
	sid := strconv.FormatInt(id, 10)
	n := 0
	for _, s := range ss {
		if !strings.HasPrefix(s.Name, "dyno_schedule_") || s.Labels["schedule"] != sid {
			continue
		}
		n++
		if s.Labels["name"] != name {
			t.Errorf("schedule %d: %s has name %q, want %q", id, s.Name, s.Labels["name"], name)
		}
	}
	if n == 0 {
		t.Errorf("schedule %d has no series", id)
	}
}
