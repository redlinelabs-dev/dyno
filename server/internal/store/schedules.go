package store

import (
	"database/sql"
	"errors"
	"time"

	"github.com/redlinelabs-dev/dyno/server/internal/result"
	"github.com/redlinelabs-dev/dyno/server/internal/schedule"
)

// ErrFull is returned by AddSchedule when schedule.MaxSchedules exist.
var ErrFull = errors.New("the schedule limit is reached")

const scheduleColumns = `id, name, far_end, kind, every_minutes, offset_minutes, paused, created_at, updated_at`

// AddSchedule stores a new schedule and returns it with its id and times
// set (now, to the millisecond, in UTC). The id is never one a deleted
// schedule had. When schedule.MaxSchedules exist it stores nothing and
// returns ErrFull, also when calls race. It doesn't validate sc.
func (s *Store) AddSchedule(sc schedule.Schedule) (schedule.Schedule, error) {
	now := time.Now().UTC().Truncate(time.Millisecond)
	// The count and the insert are one statement, so SQLite takes its write
	// lock before it counts: two adds can't both see room for the last place,
	// whatever the connection pool does.
	res, err := s.db.Exec(`INSERT INTO schedules
			(name, far_end, kind, every_minutes, offset_minutes, paused, created_at, updated_at)
		SELECT ?, ?, ?, ?, ?, ?, ?, ?
		WHERE (SELECT COUNT(*) FROM schedules) < ?`,
		sc.Name, sc.FarEnd, sc.Kind, sc.EveryMinutes, sc.OffsetMinutes, sc.Paused,
		now.UnixMilli(), now.UnixMilli(), schedule.MaxSchedules)
	if err != nil {
		return schedule.Schedule{}, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return schedule.Schedule{}, err
	}
	if n == 0 {
		return schedule.Schedule{}, ErrFull
	}
	id, err := res.LastInsertId()
	if err != nil {
		return schedule.Schedule{}, err
	}
	sc.ID, sc.CreatedAt, sc.UpdatedAt = id, now, now
	return sc, nil
}

// UpdateSchedule saves sc over the schedule with sc.ID and returns it, with
// CreatedAt kept and UpdatedAt set to now. ErrNotFound if there's none.
func (s *Store) UpdateSchedule(sc schedule.Schedule) (schedule.Schedule, error) {
	now := time.Now().UTC().Truncate(time.Millisecond)
	// max() keeps updated_at from going back if the clock was set back.
	res, err := s.db.Exec(`UPDATE schedules SET name = ?, far_end = ?, kind = ?, every_minutes = ?,
			offset_minutes = ?, paused = ?, updated_at = max(updated_at, ?)
		WHERE id = ?`,
		sc.Name, sc.FarEnd, sc.Kind, sc.EveryMinutes, sc.OffsetMinutes, sc.Paused, now.UnixMilli(), sc.ID)
	if err != nil {
		return schedule.Schedule{}, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return schedule.Schedule{}, err
	}
	if n == 0 {
		return schedule.Schedule{}, ErrNotFound
	}
	return s.Schedule(sc.ID)
}

// DeleteSchedule deletes a schedule. Its results stay. ErrNotFound if
// there's none.
func (s *Store) DeleteSchedule(id int64) error {
	res, err := s.db.Exec(`DELETE FROM schedules WHERE id = ?`, id)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// Schedule returns one schedule. ErrNotFound if there's none.
func (s *Store) Schedule(id int64) (schedule.Schedule, error) {
	sc, err := scanSchedule(s.db.QueryRow(`SELECT `+scheduleColumns+` FROM schedules WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return schedule.Schedule{}, ErrNotFound
	}
	return sc, err
}

// Schedules returns every schedule, oldest first.
func (s *Store) Schedules() ([]schedule.Schedule, error) {
	rows, err := s.db.Query(`SELECT ` + scheduleColumns + ` FROM schedules ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []schedule.Schedule{}
	for rows.Next() {
		sc, err := scanSchedule(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, sc)
	}
	return out, rows.Err()
}

func scanSchedule(rs rowScanner) (schedule.Schedule, error) {
	var sc schedule.Schedule
	err := rs.Scan(&sc.ID, &sc.Name, &sc.FarEnd, &sc.Kind, &sc.EveryMinutes, &sc.OffsetMinutes, &sc.Paused,
		(*unixMillis)(&sc.CreatedAt), (*unixMillis)(&sc.UpdatedAt))
	return sc, err
}

// LastResult returns a schedule's newest result, failed or not, without
// detail. ErrNotFound if it has none.
func (s *Store) LastResult(scheduleID int64) (result.Result, error) {
	return s.newestOf(scheduleID, ``)
}

// LastSuccess returns a schedule's newest result without a failure, without
// detail. ErrNotFound if it has none.
func (s *Store) LastSuccess(scheduleID int64) (result.Result, error) {
	return s.newestOf(scheduleID, ` AND failure_code = ''`)
}

// newestOf returns the highest-id result of schedule scheduleID that also
// meets cond, an SQL condition starting with AND. Newest means highest id:
// ids only grow, and two results can share a millisecond.
func (s *Store) newestOf(scheduleID int64, cond string) (result.Result, error) {
	if scheduleID <= 0 { // 0 is every client result's schedule_id
		return result.Result{}, ErrNotFound
	}
	r, err := scanResult(s.db.QueryRow(
		`SELECT `+columns+` FROM results WHERE schedule_id = ?`+cond+` ORDER BY id DESC LIMIT 1`, scheduleID))
	if errors.Is(err, sql.ErrNoRows) {
		return result.Result{}, ErrNotFound
	}
	return r, err
}

// RecentIdle returns the idle latencies of a schedule's newest n results
// without a failure whose far end is farEnd, newest first.
func (s *Store) RecentIdle(scheduleID int64, farEnd string, n int) ([]float64, error) {
	out := []float64{}
	if scheduleID <= 0 || n <= 0 { // a negative LIMIT in SQLite means no limit
		return out, nil
	}
	rows, err := s.db.Query(`SELECT idle_ms FROM results
		WHERE schedule_id = ? AND far_end = ? AND failure_code = ''
		ORDER BY id DESC LIMIT ?`, scheduleID, farEnd, n)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var idle float64
		if err := rows.Scan(&idle); err != nil {
			return nil, err
		}
		out = append(out, idle)
	}
	return out, rows.Err()
}
