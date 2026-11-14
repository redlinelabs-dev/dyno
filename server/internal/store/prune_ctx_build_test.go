package store_test

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/redlinelabs-dev/dyno/server/internal/store"
)

// pxBudget is a context that reports no error for the first left calls to Err
// and context.Canceled after. PruneContext asks once before each batch, so
// left is how many batches may begin; that makes "cancelled between batches"
// exact where a timer would not be.
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

// pxFill stores n results before cut and three after, and returns the ids of
// the three.
func pxFill(t *testing.T, st *store.Store, cut time.Time, n int) []int64 {
	t.Helper()
	for i := range n {
		hxAt(t, st, cut.Add(-time.Duration(i+1)*time.Second))
	}
	var keep []int64
	for i := range 3 {
		keep = append(keep, hxAt(t, st, cut.Add(time.Duration(i)*time.Second)))
	}
	return keep
}

// A context that ended before the prune began deletes nothing and says why.
func TestPx_PruneContextCancelledBeforeDeletesNothing(t *testing.T) {
	cut := time.Date(2026, 11, 10, 0, 0, 0, 0, time.UTC)
	st := hxOpen(t, t.TempDir())
	keep := pxFill(t, st, cut, 10)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	n, err := st.PruneContext(ctx, cut)
	if !errors.Is(err, context.Canceled) || n != 0 {
		t.Fatalf("PruneContext with a cancelled context: deleted %d, err %v, want 0 and context.Canceled", n, err)
	}
	if left := hxIDs(t, st, store.Filter{Oldest: true}); len(left) != 10+len(keep) {
		t.Errorf("%d results left, want all %d", len(left), 10+len(keep))
	}
}

// A context that ends between batches stops the prune there: it deletes the
// batches begun, returns the count so far and the context's error, and what
// it deleted stays deleted. A later Prune finishes the backlog.
func TestPx_PruneContextCancelledBetweenBatchesStopsThere(t *testing.T) {
	cut := time.Date(2026, 11, 10, 0, 0, 0, 0, time.UTC)
	old := 3*store.PageSize + 200
	st := hxOpen(t, t.TempDir())
	keep := pxFill(t, st, cut, old)

	n, err := st.PruneContext(&pxBudget{Context: context.Background(), left: 2}, cut)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if want := int64(2 * store.PageSize); n != want {
		t.Fatalf("deleted %d, want the two batches that began, %d", n, want)
	}
	if left := hxIDs(t, st, store.Filter{Oldest: true}); len(left) != old+len(keep)-int(n) {
		t.Errorf("%d results left, want %d: what is gone must match the count returned", len(left), old+len(keep)-int(n))
	}

	n2, err := st.Prune(cut)
	if err != nil || n2 != int64(old)-n {
		t.Errorf("Prune afterwards deleted %d, err %v, want %d and nil", n2, err, int64(old)-n)
	}
	if left := hxIDs(t, st, store.Filter{Oldest: true}); !slices.Equal(left, keep) {
		t.Errorf("left %v, want %v", left, keep)
	}
}

// A context that ends during the last batch changes nothing: the prune was
// done, and it reports success.
func TestPx_PruneContextFinishesWhenTheLastBatchIsShort(t *testing.T) {
	cut := time.Date(2026, 11, 10, 0, 0, 0, 0, time.UTC)
	st := hxOpen(t, t.TempDir())
	keep := pxFill(t, st, cut, 7)
	n, err := st.PruneContext(&pxBudget{Context: context.Background(), left: 1}, cut)
	if err != nil || n != 7 {
		t.Fatalf("deleted %d, err %v, want 7 and nil", n, err)
	}
	if left := hxIDs(t, st, store.Filter{Oldest: true}); !slices.Equal(left, keep) {
		t.Errorf("left %v, want %v", left, keep)
	}
}
