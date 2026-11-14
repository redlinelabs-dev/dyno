// Package scheduler runs scheduled tests (ADR-0028, ADR-0029): one at a
// time, each on its schedule's grid, storing every finished run, failed or
// not.
package scheduler

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/redlinelabs-dev/dyno/server/internal/metrics"
	"github.com/redlinelabs-dev/dyno/server/internal/nodetest"
	"github.com/redlinelabs-dev/dyno/server/internal/schedule"
	"github.com/redlinelabs-dev/dyno/server/internal/store"
)

// Runner runs one test: nodetest.Run in production.
type Runner func(ctx context.Context, o nodetest.Options) (nodetest.Outcome, error)

// Limits on a run (ADR-0029).
const (
	RunTimeout = 5 * time.Minute // a run still going after this is stopped and stored as timeout
	BusyWait   = 2 * time.Minute // the longest a run waits for this server's own tests to end
	BusyPoll   = 5 * time.Second // how often it looks while it waits
)

// CodeInternal is the failure code of a run that failed inside dyno: Test
// panicked, or returned an error that isn't a *nodetest.Error.
const CodeInternal = "internal"

// How the loop paces itself.
const (
	// maxSleep is the longest one sleep lasts. A timer counts time the host was
	// awake, so after a suspend, or a clock set forward, the loop would
	// otherwise oversleep by as much as the schedule's interval.
	maxSleep = time.Minute

	// retryEvery is how often the loop reads the schedules again after a read
	// failed.
	retryEvery = 30 * time.Second
)

// Scheduler runs the schedules in Store. Set its fields, then call Run.
// Changed, RunNow and Status may be called from any goroutine at any time,
// before Run starts too.
type Scheduler struct {
	Store   *store.Store
	Test    Runner           // required
	Version string           // this dyno's version; results get client "dyno/<Version>"
	Busy    func() bool      // whether this server is serving a test right now; nil means never
	Metrics *metrics.Metrics // optional
	Log     *slog.Logger     // nil means slog.Default()

	once sync.Once

	// wake tells Run that the schedules changed. It holds at most one signal.
	wake chan struct{}

	// loadMu keeps two reads of the store from being applied out of order.
	loadMu sync.Mutex

	// restored is set once the gauges have been restored from the store. It is
	// read and written under loadMu only.
	restored bool

	// mu guards the fields below, which Run, Changed, RunNow and Status share.
	// It is never held across a run, a store call or a log.
	mu      sync.Mutex
	entries map[int64]*entry
	queue   []*entry
	stale   bool
	active  bool

	// failedAt is when the last read of the schedules failed; it matters
	// while stale is set.
	failedAt time.Time
}

// setup makes what the zero Scheduler lacks. Every entry point calls it
// through once.
func (s *Scheduler) setup() {
	s.wake = make(chan struct{}, 1)
	s.entries = make(map[int64]*entry)
}

// Run runs schedules until ctx is done. Then it stops any run in progress,
// storing nothing for it, and returns once that run has stopped. A Run that
// starts while another is going returns at once.
//
// It loops: queue what has come due, wait while this server is serving a
// test, run the head of the queue, and sleep until something is due or the
// schedules change. It calls the Runner itself and starts no goroutine, so
// one run goes at a time and nothing outlives Run.
func (s *Scheduler) Run(ctx context.Context) {
	s.once.Do(s.setup)
	if !s.claim() {
		return
	}
	defer s.release()

	s.rearm(time.Now())
	first := true              // the first turn reads the schedules whatever failed before
	var waitingSince time.Time // when the run at the head began waiting for Busy; zero when none is
	for ctx.Err() == nil {
		if first || s.retryDue(time.Now()) {
			s.load(true)
		}
		first = false
		now := time.Now()
		queued, wait := s.plan(now)
		if queued == 0 {
			waitingSince = time.Time{}
			s.sleep(ctx, wait)
			continue
		}
		if s.busy() {
			if waitingSince.IsZero() {
				waitingSince = now
			}
			if left := BusyWait - now.Sub(waitingSince); left > 0 {
				s.sleep(ctx, min(BusyPoll, left))
				continue
			}
		}
		waitingSince = time.Time{}
		s.execute(ctx)
	}
}

// claim marks a Run as going. It reports false when one is going already.
func (s *Scheduler) claim() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.active {
		return false
	}
	s.active = true
	return true
}

func (s *Scheduler) release() {
	s.mu.Lock()
	s.active = false
	s.mu.Unlock()
}

// retryDue reports whether the last read failed and retryEvery has passed
// since. A turn that a failed read woke early doesn't read again at once: the
// database is likely still down, and each try logs an ERROR.
func (s *Scheduler) retryDue(now time.Time) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.stale && !now.Before(s.failedAt.Add(retryEvery))
}

// plan queues what has come due. It reports how many runs are waiting and,
// for when none is, how long to sleep.
func (s *Scheduler) plan(now time.Time) (queued int, wait time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.enqueueDue(now)
	return len(s.queue), s.untilNext(now)
}

// busy reports whether this server is serving a test.
func (s *Scheduler) busy() bool { return s.Busy != nil && s.Busy() }

// sleep waits for d, for a change of the schedules, or for ctx to end. A
// negative d waits for the last two only.
func (s *Scheduler) sleep(ctx context.Context, d time.Duration) {
	var timeout <-chan time.Time
	if d >= 0 {
		t := time.NewTimer(d)
		defer t.Stop()
		timeout = t.C
	}
	select {
	case <-ctx.Done():
	case <-s.wake:
	case <-timeout:
	}
}

// signal wakes Run, if it is asleep.
func (s *Scheduler) signal() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

// Changed tells the scheduler that schedules were added, edited or
// deleted. It reads the schedules again before it returns, and never waits
// for a run. A deleted schedule's run in progress is stopped, and nothing
// is stored for it.
func (s *Scheduler) Changed() {
	s.once.Do(s.setup)
	s.load(false)
}

// RunNow queues a run of schedule id, paused or not. A schedule that is
// queued or running already isn't queued again. It returns
// store.ErrNotFound when there's no such schedule.
func (s *Scheduler) RunNow(id int64) error {
	s.once.Do(s.setup)
	if _, err := s.Store.Schedule(id); err != nil {
		return err
	}
	if !s.known(id) {
		// Changed hasn't told us of it yet. Reading every schedule, as Changed
		// does, also keeps the entries to what the store holds.
		if _, err := s.load(false); err != nil {
			return err
		}
	}
	s.mu.Lock()
	e := s.entries[id]
	if e == nil { // deleted since the check above
		s.mu.Unlock()
		return store.ErrNotFound
	}
	now := time.Now()
	if !e.running && !e.waiting(now) {
		e.next, e.due = time.Time{}, now
		s.push(e)
	}
	s.mu.Unlock()
	s.signal()
	return nil
}

func (s *Scheduler) known(id int64) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.entries[id] != nil
}

// Status reports whether schedule id is running or queued. A run waiting for
// this server's own tests to end is queued, and so is one asked for before
// Run started.
func (s *Scheduler) Status(id int64) schedule.Status {
	s.once.Do(s.setup)
	s.mu.Lock()
	defer s.mu.Unlock()
	e := s.entries[id]
	if e == nil {
		return schedule.Status{}
	}
	return schedule.Status{Running: e.running, Queued: e.waiting(time.Now())}
}

// load reads the schedules from the store and makes the scheduler's state
// match them. A read that fails is logged and leaves the state as it was; Run
// reads again within retryEvery. fromRun is true for Run's own reads: a
// failure elsewhere wakes Run, which may be asleep with no timer, so that it
// sets its retry timer, but Run's own failed retry mustn't wake it again.
func (s *Scheduler) load(fromRun bool) ([]schedule.Schedule, error) {
	s.loadMu.Lock()
	defer s.loadMu.Unlock()
	list, err := s.Store.Schedules()
	s.mu.Lock()
	s.stale = err != nil
	if err == nil {
		s.apply(list, time.Now())
	} else {
		s.failedAt = time.Now()
	}
	s.mu.Unlock()
	if err != nil {
		s.logLoad(err)
		if !fromRun {
			s.signal()
		}
		return nil, err
	}
	s.Metrics.SetSchedules(seriesOf(list))
	if !s.restored {
		s.restored = true
		s.restore(list)
	}
	s.signal()
	return list, nil
}

// seriesOf names list's metric series.
func seriesOf(list []schedule.Schedule) []metrics.ScheduleSeries {
	out := make([]metrics.ScheduleSeries, len(list))
	for i, sc := range list {
		out[i] = metrics.ScheduleSeries{ID: sc.ID, Name: sc.Name}
	}
	return out
}

// restore sets the last-success gauges of the schedules in list from the
// store, so that a restart doesn't blank them (docs/api.md, "Metrics"). load
// calls it once, on the first read of the schedules that succeeds, whatever
// started that read.
func (s *Scheduler) restore(list []schedule.Schedule) {
	if s.Metrics == nil {
		return
	}
	for _, sc := range list {
		r, err := s.Store.LastSuccess(sc.ID)
		switch {
		case err == nil:
			s.Metrics.ScheduleRestore(sc.ID, r)
		case !errors.Is(err, store.ErrNotFound):
			s.logLoad(err)
		}
	}
}

// logLoad logs a read of the store that failed. The schedules in use may be
// out of date until a read succeeds, and the database refusing reads is what
// the operator has to act on (ADR-0020).
func (s *Scheduler) logLoad(err error) {
	s.logger().LogAttrs(context.Background(), slog.LevelError, "schedule.load",
		slog.String("result", "failed"),
		slog.String("error.code", codeStoreRead),
		slog.String("error.message", err.Error()))
}

func (s *Scheduler) logger() *slog.Logger {
	if s.Log != nil {
		return s.Log
	}
	return slog.Default()
}
