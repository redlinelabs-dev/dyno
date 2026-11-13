// Package retention deletes results older than the keep_days setting
// (ADR-0030).
package retention

import (
	"context"
	"log/slog"
	"time"

	"github.com/redlinelabs-dev/dyno/server/internal/store"
)

// Every is how often Run deletes old results.
const Every = time.Hour

// Prune deletes the results stored more than keep_days days (of 24 hours)
// before now, and returns how many it deleted. keep_days 0 deletes nothing.
func Prune(st *store.Store, now time.Time) (int64, error) { panic("not implemented") }

// Run prunes at once and then every Every until ctx is done, and returns
// once it has stopped. Each prune that deletes rows or fails logs a
// results.prune event (ADR-0020); nil log means slog.Default().
func Run(ctx context.Context, st *store.Store, log *slog.Logger) { panic("not implemented") }
