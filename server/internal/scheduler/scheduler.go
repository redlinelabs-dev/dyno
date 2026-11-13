// Package scheduler runs scheduled tests (ADR-0028, ADR-0029): one at a
// time, each on its schedule's grid, storing every finished run, failed or
// not.
package scheduler

import (
	"context"
	"log/slog"
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
}

// Run runs schedules until ctx is done. Then it stops any run in progress,
// storing nothing for it, and returns once that run has stopped.
func (s *Scheduler) Run(ctx context.Context) { panic("not implemented") }

// Changed tells the scheduler that schedules were added, edited or
// deleted. It reads the schedules again before it returns, and never waits
// for a run. A deleted schedule's run in progress is stopped, and nothing
// is stored for it.
func (s *Scheduler) Changed() { panic("not implemented") }

// RunNow queues a run of schedule id, paused or not. A schedule that is
// queued or running already isn't queued again. It returns
// store.ErrNotFound when there's no such schedule.
func (s *Scheduler) RunNow(id int64) error { panic("not implemented") }

// Status reports whether schedule id is running or queued.
func (s *Scheduler) Status(id int64) schedule.Status { panic("not implemented") }
