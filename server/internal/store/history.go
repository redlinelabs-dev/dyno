package store

import (
	"time"

	"github.com/redlinelabs-dev/dyno/server/internal/result"
)

// PageSize is the most rows Query reads before it lets the database go.
const PageSize = 500

// Filter selects results. Its zero value selects every result, newest first.
type Filter struct {
	Limit    int       // at most this many; 0 means no limit
	Before   int64     // only ids below this; 0 means no bound
	Device   string    // only this device's; "" means any
	Source   string    // "client" or "schedule"; "" means any
	Schedule int64     // only this schedule's; 0 means any
	Outcome  string    // "succeeded" (no failure) or "failed"; "" means any
	From, To time.Time // only at >= From and at < To; a zero time is no bound
	Oldest   bool      // oldest first (ascending id) instead of newest first
}

// Query calls each with every result f selects, in order, without detail.
// It reads at most PageSize rows at a time and holds no database connection
// while each runs, so a slow caller doesn't hold up other reads and writes.
// It stops at the first error each returns, and returns that error.
func (s *Store) Query(f Filter, each func(result.Result) error) error { panic("not implemented") }

// Prune deletes the results stored before t and returns how many it
// deleted.
func (s *Store) Prune(before time.Time) (int64, error) { panic("not implemented") }
