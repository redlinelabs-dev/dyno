package store

import (
	"errors"

	"github.com/redlinelabs-dev/dyno/server/internal/result"
	"github.com/redlinelabs-dev/dyno/server/internal/schedule"
)

// ErrFull is returned by AddSchedule when schedule.MaxSchedules exist.
var ErrFull = errors.New("the schedule limit is reached")

// AddSchedule stores a new schedule and returns it with its id and times
// set (now, to the millisecond, in UTC). The id is never one a deleted
// schedule had. When schedule.MaxSchedules exist it stores nothing and
// returns ErrFull, also when calls race. It doesn't validate sc.
func (s *Store) AddSchedule(sc schedule.Schedule) (schedule.Schedule, error) {
	panic("not implemented")
}

// UpdateSchedule saves sc over the schedule with sc.ID and returns it, with
// CreatedAt kept and UpdatedAt set to now. ErrNotFound if there's none.
func (s *Store) UpdateSchedule(sc schedule.Schedule) (schedule.Schedule, error) {
	panic("not implemented")
}

// DeleteSchedule deletes a schedule. Its results stay. ErrNotFound if
// there's none.
func (s *Store) DeleteSchedule(id int64) error { panic("not implemented") }

// Schedule returns one schedule. ErrNotFound if there's none.
func (s *Store) Schedule(id int64) (schedule.Schedule, error) { panic("not implemented") }

// Schedules returns every schedule, oldest first.
func (s *Store) Schedules() ([]schedule.Schedule, error) { panic("not implemented") }

// LastResult returns a schedule's newest result, failed or not, without
// detail. ErrNotFound if it has none.
func (s *Store) LastResult(scheduleID int64) (result.Result, error) { panic("not implemented") }

// LastSuccess returns a schedule's newest result without a failure, without
// detail. ErrNotFound if it has none.
func (s *Store) LastSuccess(scheduleID int64) (result.Result, error) { panic("not implemented") }

// RecentIdle returns the idle latencies of a schedule's newest n results
// without a failure whose far end is farEnd, newest first.
func (s *Store) RecentIdle(scheduleID int64, farEnd string, n int) ([]float64, error) {
	panic("not implemented")
}
