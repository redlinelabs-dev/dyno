// Package retention deletes results older than the keep_days setting
// (ADR-0030).
package retention

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/redlinelabs-dev/dyno/server/internal/settings"
	"github.com/redlinelabs-dev/dyno/server/internal/store"
)

// Every is how often Run deletes old results.
const Every = time.Hour

// Prune deletes the results stored more than keep_days days (of 24 hours)
// before now, and returns how many it deleted. keep_days 0 deletes nothing.
func Prune(st *store.Store, now time.Time) (int64, error) {
	s, err := st.Settings()
	if err != nil {
		return 0, fmt.Errorf("reading keep_days: %w", err)
	}
	return deleteOlder(context.Background(), st, s.KeepDays, now)
}

// deleteOlder is Prune once keep_days is known, and stops between batches when
// ctx is done.
func deleteOlder(ctx context.Context, st *store.Store, keepDays int, now time.Time) (int64, error) {
	if keepDays <= 0 {
		return 0, nil
	}
	// A stored value past the limit would overflow the duration below and put
	// the cut in the future, which would delete everything.
	days := min(keepDays, settings.MaxKeepDays)
	cut := now.Add(-time.Duration(days) * 24 * time.Hour)
	return st.PruneContext(ctx, cut)
}

// Run prunes at once and then every Every until ctx is done, and returns
// once it has stopped. Each prune is a Sweep, so each one that deletes rows
// or fails logs a results.prune event (ADR-0020); nil log means
// slog.Default().
func Run(ctx context.Context, st *store.Store, log *slog.Logger) {
	if log == nil {
		log = slog.Default()
	}
	tick := time.NewTicker(Every)
	defer tick.Stop()
	for ctx.Err() == nil {
		Sweep(ctx, st, log)
		select {
		case <-ctx.Done():
		case <-tick.C:
		}
	}
}

// Sweep prunes once, as Prune does, and logs what it did as a results.prune
// event: nothing when it deleted nothing. A failure is an ERROR, since the
// database refused a read or a write and the operator has to act, with
// error.code store_read_failed when keep_days couldn't be read and
// store_delete_failed when the delete failed, which also carries
// deleted_results, the rows deleted before it failed; the next prune tries
// again. A prune stopped by ctx between batches is not a failure: it logs
// result canceled at INFO, with deleted_results so far and no error keys, or
// nothing when it had deleted nothing. Run sweeps every Every, and a change of
// keep_days sweeps at once. nil log means slog.Default().
func Sweep(ctx context.Context, st *store.Store, log *slog.Logger) {
	if log == nil {
		log = slog.Default()
	}
	start := time.Now()
	s, err := st.Settings()
	if err != nil {
		pruneFailed(ctx, log, start, "store_read_failed", fmt.Errorf("reading keep_days: %w", err))
		return
	}
	deleted, err := deleteOlder(ctx, st, s.KeepDays, start)
	switch {
	case err != nil && ctx.Err() != nil && errors.Is(err, ctx.Err()):
		// Stopped on purpose, by shutdown or a hung-up request: not a database
		// failure, and what went stays deleted.
		if deleted > 0 {
			log.LogAttrs(ctx, slog.LevelInfo, "results.prune",
				slog.String("result", "canceled"),
				slog.Int64("deleted_results", deleted),
				slog.Int("keep_days", s.KeepDays),
				slog.Float64("duration_ms", sinceMs(start)))
		}
	case err != nil:
		pruneFailed(ctx, log, start, "store_delete_failed", err, slog.Int64("deleted_results", deleted))
	case deleted > 0:
		log.LogAttrs(ctx, slog.LevelInfo, "results.prune",
			slog.String("result", "succeeded"),
			slog.Int64("deleted_results", deleted),
			slog.Int("keep_days", s.KeepDays),
			slog.Float64("duration_ms", sinceMs(start)))
	}
}

// pruneFailed logs a prune the database refused, with what every failed event
// carries and any extra attrs. A failed delete adds deleted_results, the rows
// that went before it failed, since batches already committed stay deleted.
func pruneFailed(ctx context.Context, log *slog.Logger, start time.Time, code string, err error, extra ...slog.Attr) {
	log.LogAttrs(ctx, slog.LevelError, "results.prune", append([]slog.Attr{
		slog.String("result", "failed"),
		slog.Float64("duration_ms", sinceMs(start)),
		slog.String("error.code", code),
		slog.String("error.message", err.Error()),
	}, extra...)...)
}

// sinceMs is the time since start in milliseconds, to the microsecond.
func sinceMs(start time.Time) float64 {
	return float64(time.Since(start).Microseconds()) / 1e3
}
