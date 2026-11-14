package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/redlinelabs-dev/dyno/server/internal/result"
)

// PageSize is the most rows Query reads before it lets the database go.
const PageSize = 500

// pruneBatch is the most rows one of Prune's statements deletes. Each runs as
// its own transaction, so the connection is free between them and the WAL
// stays small however much there is to delete.
const pruneBatch = PageSize

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
// It sees the results stored when it started: one added meanwhile is left
// out. It stops at the first error each returns, and returns that error.
func (s *Store) Query(f Filter, each func(result.Result) error) error {
	if f.Limit < 0 {
		return fmt.Errorf("store: negative limit %d", f.Limit)
	}
	conds, args, err := f.conditions()
	if err != nil {
		return err
	}

	// Every page reads the ids above lo, up to and including hi. hi starts at
	// the newest id now, so a result added while Query runs is never reached.
	var newest sql.NullInt64
	if err := s.db.QueryRow(`SELECT MAX(id) FROM results`).Scan(&newest); err != nil {
		return err
	}
	lo, hi := int64(0), newest.Int64
	if f.Before > 0 {
		hi = min(hi, f.Before-1)
	}
	if !f.From.IsZero() || !f.To.IsZero() {
		// A range of time becomes a range of ids, so a page never reads the
		// results outside it. The ids stay a bound, not a filter: the time
		// conditions still apply, which keeps this right if ids and times
		// ever run out of step.
		first, last, found, err := s.idsBetween(f.From, f.To)
		if err != nil {
			return err
		}
		if !found {
			return nil
		}
		lo, hi = max(lo, first-1), min(hi, last)
	}
	if hi <= lo {
		return nil
	}

	order := "DESC"
	if f.Oldest {
		order = "ASC"
	}
	query := `SELECT ` + columns + ` FROM results WHERE id > ? AND id <= ?` + conds +
		` ORDER BY id ` + order + ` LIMIT ?`
	params := make([]any, 0, len(args)+3)
	params = append(params, lo, hi)
	params = append(params, args...)
	params = append(params, 0)

	var page []result.Result
	left := f.Limit
	for {
		n := PageSize
		if f.Limit > 0 {
			n = min(n, left)
		}
		params[0], params[1], params[len(params)-1] = lo, hi, n
		page, err = s.readPage(page[:0], query, params)
		if err != nil {
			return err
		}
		for i := range page {
			if err := each(page[i]); err != nil {
				return err
			}
		}
		if len(page) < n {
			return nil
		}
		if f.Limit > 0 {
			left -= n
			if left == 0 {
				return nil
			}
		}
		// Keyset paging: the next page starts after the last id, never at an
		// offset, so it costs the same however deep it is.
		if f.Oldest {
			lo = page[len(page)-1].ID
		} else {
			hi = page[len(page)-1].ID - 1
		}
		if hi <= lo {
			return nil
		}
	}
}

// readPage appends one page of rows to dst. The rows are closed before it
// returns, so its caller never holds the connection while it uses them.
func (s *Store) readPage(dst []result.Result, query string, params []any) ([]result.Result, error) {
	rows, err := s.db.Query(query, params...)
	if err != nil {
		return dst, err
	}
	defer rows.Close()
	for rows.Next() {
		r, err := scanResult(rows)
		if err != nil {
			return dst, err
		}
		dst = append(dst, r)
	}
	return dst, rows.Err()
}

// idsBetween returns the lowest and highest id of the results stored from
// from up to but not including to, where a zero time is no bound. found is
// false when there are none. It reads the results_at index and no row.
func (s *Store) idsBetween(from, to time.Time) (first, last int64, found bool, err error) {
	var conds []string
	var args []any
	if !from.IsZero() {
		conds = append(conds, "at >= ?")
		args = append(args, ceilMillis(from))
	}
	if !to.IsZero() {
		conds = append(conds, "at < ?")
		args = append(args, ceilMillis(to))
	}
	var lo, hi sql.NullInt64
	err = s.db.QueryRow(`SELECT MIN(id), MAX(id) FROM results WHERE `+strings.Join(conds, " AND "), args...).Scan(&lo, &hi)
	return lo.Int64, hi.Int64, lo.Valid, err
}

// conditions returns the WHERE terms, each starting with AND, and their
// arguments, for what f selects apart from its id bounds.
func (f Filter) conditions() (string, []any, error) {
	var b strings.Builder
	var args []any
	if f.Device != "" {
		b.WriteString(` AND device = ?`)
		args = append(args, f.Device)
	}
	if f.Source != "" {
		b.WriteString(` AND source = ?`)
		args = append(args, f.Source)
	}
	if f.Schedule != 0 {
		b.WriteString(` AND schedule_id = ?`)
		args = append(args, f.Schedule)
	}
	switch f.Outcome {
	case "":
	case "succeeded":
		b.WriteString(` AND failure_code = ''`)
	case "failed":
		b.WriteString(` AND failure_code <> ''`)
	default:
		return "", nil, fmt.Errorf("store: unknown outcome %q", f.Outcome)
	}
	// The unary + keeps SQLite from reading the results_at index for these
	// terms, which would sort every result in the range again for each page.
	// The id bounds already narrow the scan to the range.
	if !f.From.IsZero() {
		b.WriteString(` AND +at >= ?`)
		args = append(args, ceilMillis(f.From))
	}
	if !f.To.IsZero() {
		b.WriteString(` AND +at < ?`)
		args = append(args, ceilMillis(f.To))
	}
	return b.String(), args, nil
}

// ceilMillis is t in unix milliseconds, rounded up. Results are stored to the
// millisecond, so "at or after t" starts at the first whole millisecond that
// isn't before t, and "before t" ends there.
func ceilMillis(t time.Time) int64 {
	ms := t.UnixMilli()
	if t.Nanosecond()%int(time.Millisecond) != 0 {
		ms++
	}
	return ms
}

// Prune deletes the results stored before the given time and returns how many
// it deleted. It deletes pruneBatch rows at a time, so a large backlog never
// holds the one connection for long. On an error it returns the count so far.
func (s *Store) Prune(before time.Time) (int64, error) {
	return s.PruneContext(context.Background(), before)
}

// PruneContext is Prune that stops between batches once ctx is done, so a
// large backlog can't hold a shutdown. It returns ctx's error and the count so
// far; the batches already deleted stay deleted. A batch begun is finished.
func (s *Store) PruneContext(ctx context.Context, before time.Time) (int64, error) {
	cut := ceilMillis(before)
	var total int64
	for {
		if err := ctx.Err(); err != nil {
			return total, err
		}
		res, err := s.db.Exec(`DELETE FROM results WHERE id IN (SELECT id FROM results WHERE at < ? LIMIT ?)`, cut, pruneBatch)
		if err != nil {
			return total, err
		}
		n, err := res.RowsAffected()
		if err != nil {
			return total, err
		}
		total += n
		if n < pruneBatch {
			return total, nil
		}
	}
}
