package scheduler_test

// Guards found by a clean-context review, beside the spec tests. None of these
// is a spec case.

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/redlinelabs-dev/dyno/server/internal/nodetest"
	"github.com/redlinelabs-dev/dyno/server/internal/result"
	"github.com/redlinelabs-dev/dyno/server/internal/schedule"
	"github.com/redlinelabs-dev/dyno/server/internal/scheduler"
)

// Status called before Run, on a Scheduler nothing has set up, must not read
// the entries while Run's setup writes them. The race detector is what fails
// it, so the coordinator's -race run is what counts; each round is a fresh
// Scheduler, released together, so that Status often goes first.
func TestRx_StatusOnAFreshSchedulerDoesNotRaceRunsSetup(t *testing.T) {
	noRun := func(context.Context, nodetest.Options) (nodetest.Outcome, error) {
		return nodetest.Outcome{}, nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Run does its setup and returns, so it needs no store
	for range 300 {
		s := &scheduler.Scheduler{Test: noRun}
		start := make(chan struct{})
		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			<-start
			s.Run(ctx)
		}()
		go func() {
			defer wg.Done()
			<-start
			if got := s.Status(1); got != (schedule.Status{}) {
				t.Errorf("Status(1) = %+v on a Scheduler with no schedules, want the zero Status", got)
			}
		}()
		close(start)
		wg.Wait()
	}
}

// When Run's first read of the schedules fails and a later one, made by
// Changed, succeeds, the gauges are restored from the store by that later read,
// not left blank until a restart.
func TestRx_GaugesAreRestoredByTheFirstLoadThatSucceeds(t *testing.T) {
	rnBubble(t, func(t *testing.T) {
		dir := t.TempDir()
		e := r2NewEnvIn(t, dir, rnWithMetrics)
		fault := r2OpenFault(t, dir)
		a := e.Add(rnSched("a", 1440)) // nothing is due in this test
		good := e.Seed(result.Result{
			At: rnT0.Add(-time.Hour), Source: "schedule", ScheduleID: a.ID, FarEnd: a.FarEnd,
			Submission: result.Submission{Client: "seed", DownloadMbps: 300, UploadMbps: 30, IdleMs: 12, LoadedDownMs: 25, LoadedUpMs: 45, Streams: 5},
		})
		fault.Refuse("schedules")
		e.Start() // Run's first read fails
		if len(r2Loads(e)) == 0 {
			t.Fatal("no schedule.load event, so the first read of the schedules didn't fail")
		}
		fault.Allow("schedules")
		e.Changed() // succeeds before Run's retry is due
		rnWantSeries(t, e.Expo(), a.ID, rnSeries{Name: "a", Last: rnGaugesOf(good)})

		// Once is enough: a later read doesn't set the gauges again.
		e.Seed(result.Result{
			At: rnT0.Add(-time.Minute), Source: "schedule", ScheduleID: a.ID, FarEnd: a.FarEnd,
			Submission: result.Submission{Client: "seed", DownloadMbps: 999, UploadMbps: 99, IdleMs: 5, LoadedDownMs: 9, LoadedUpMs: 9, Streams: 5},
		})
		e.Changed()
		rnWantSeries(t, e.Expo(), a.ID, rnSeries{Name: "a", Last: rnGaugesOf(good)})
	})
}
