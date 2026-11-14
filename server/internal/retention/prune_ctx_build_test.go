package retention_test

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/redlinelabs-dev/dyno/server/internal/result"
	"github.com/redlinelabs-dev/dyno/server/internal/retention"
	"github.com/redlinelabs-dev/dyno/server/internal/store"
)

// pxBudget is a context that reports no error for the first left calls to Err
// and context.Canceled after. The prune asks once before each batch, so left
// is how many batches may begin.
type pxBudget struct {
	context.Context
	left int
}

func (c *pxBudget) Err() error {
	if c.left > 0 {
		c.left--
		return nil
	}
	return context.Canceled
}

// pxCount is how many results are stored.
func pxCount(t *testing.T, st *store.Store) int {
	t.Helper()
	n := 0
	if err := st.Query(store.Filter{}, func(result.Result) error { n++; return nil }); err != nil {
		t.Fatalf("Query: %v", err)
	}
	return n
}

// pxBacklog stores old results 8 days old and three 1 hour old, with
// keep_days 7.
func pxBacklog(t *testing.T, st *store.Store, old int) {
	t.Helper()
	histKeep(t, st, 7)
	now := time.Now()
	for i := range old {
		histAdd(t, st, histRes(histAgo(now, 8*histDay+time.Duration(i)*time.Second)))
	}
	histAddAges(t, st, now, time.Hour, time.Hour, time.Hour)
}

// A sweep whose context ends in the middle of a backlog logs one results.prune
// with result canceled and the rows it got through, at INFO and with no error
// keys, and what is gone matches that count. A shutdown that stops it is not a
// database failure.
func TestPx_SweepCancelledMidBacklogLogsCanceledWithTheCount(t *testing.T) {
	const batch = store.PageSize
	old := 2*batch + 100
	st := histOpen(t, t.TempDir())
	pxBacklog(t, st, old)
	logs := &histLog{}
	lg := slog.New(slog.NewJSONHandler(logs, nil))

	retention.Sweep(&pxBudget{Context: context.Background(), left: 1}, st, lg)

	evs := logs.events(t)
	if len(evs) != 1 {
		t.Fatalf("a cancelled sweep logged %d lines, want 1: %v", len(evs), evs)
	}
	ev := evs[0]
	wlCheckKeys(t, "the canceled sweep", ev, "time", "level", "msg", "result", "deleted_results", "keep_days", "duration_ms")
	if ev["msg"] != "results.prune" || ev["level"] != "INFO" || ev["result"] != "canceled" {
		t.Errorf("event %v, want an INFO results.prune with result canceled", ev)
	}
	if ev["deleted_results"] != float64(batch) || ev["keep_days"] != 7.0 {
		t.Errorf("deleted_results = %v and keep_days = %v, want %d and 7", ev["deleted_results"], ev["keep_days"], batch)
	}
	if got, want := pxCount(t, st), old+3-batch; got != want {
		t.Errorf("%d results left, want %d: what is gone must match deleted_results", got, want)
	}

	// The next sweep finishes the backlog and logs it as a success.
	retention.Sweep(context.Background(), st, lg)
	if got := pxCount(t, st); got != 3 {
		t.Errorf("%d results left after the next sweep, want the 3 fresh ones", got)
	}
}

// A sweep whose context ended before it deleted anything logs nothing and
// deletes nothing.
func TestPx_SweepCancelledBeforeAnyBatchLogsNothing(t *testing.T) {
	st := histOpen(t, t.TempDir())
	pxBacklog(t, st, 20)
	logs := &histLog{}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	retention.Sweep(ctx, st, slog.New(slog.NewJSONHandler(logs, nil)))

	if evs := logs.events(t); len(evs) != 0 {
		t.Errorf("a sweep cancelled before it began logged %v, want nothing", evs)
	}
	if got := pxCount(t, st); got != 23 {
		t.Errorf("%d results left, want all 23", got)
	}
}
