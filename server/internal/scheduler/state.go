package scheduler

import (
	"cmp"
	"context"
	"slices"
	"time"

	"github.com/redlinelabs-dev/dyno/server/internal/schedule"
)

// entry is what the scheduler knows about one schedule. Its fields are
// guarded by Scheduler.mu.
type entry struct {
	// sc is the schedule as the store last held it.
	sc schedule.Schedule

	// next is the first time on sc's grid after its last run ended. It is zero
	// while the schedule is paused, queued or running: grid times that pass
	// then are dropped, never made up.
	next time.Time

	// due is when the schedule joined the queue, which orders the queue.
	due time.Time

	queued  bool
	running bool

	// deleted is set when the schedule is deleted while its run is going.
	deleted bool

	// cancel stops the run in progress.
	cancel context.CancelFunc
}

// interval is the time between e's runs, as schedule.Schedule.Next reads it.
func (e *entry) interval() time.Duration {
	every := e.sc.EveryMinutes
	if every <= 0 || every > 1440 {
		every = schedule.DefaultEvery
	}
	return time.Duration(every) * time.Minute
}

// armed reports whether e is waiting for its next grid time: it is not paused,
// queued or running, and has a time.
func (e *entry) armed() bool {
	return !e.sc.Paused && !e.queued && !e.running && !e.next.IsZero()
}

// waiting reports whether a run of e is due and hasn't started: it is queued,
// or its time has come and the loop isn't round to queueing it yet.
func (e *entry) waiting(now time.Time) bool {
	return e.queued || (e.armed() && !e.next.After(now))
}

// apply makes the entries match list, the schedules the store holds now.
// A schedule that is new, retimed or resumed gets its next time from now. One
// that is paused leaves the queue, unless it was paused already: then a run
// someone asked for stays. A schedule that is gone loses its entry, and its run
// in progress, if any, is stopped.
func (s *Scheduler) apply(list []schedule.Schedule, now time.Time) {
	listed := make(map[int64]bool, len(list))
	for _, sc := range list {
		listed[sc.ID] = true
		e := s.entries[sc.ID]
		if e == nil {
			e = &entry{sc: sc}
			s.entries[sc.ID] = e
			if !sc.Paused {
				e.next = sc.Next(now)
			}
			continue
		}
		old := e.sc
		e.sc = sc
		switch {
		case sc.Paused:
			e.next = time.Time{}
			if !old.Paused {
				s.dequeue(e)
			}
		case e.queued || e.running:
			// Its next time is set when the run ends.
		case old.Paused || old.EveryMinutes != sc.EveryMinutes || old.OffsetMinutes != sc.OffsetMinutes || e.next.IsZero():
			e.next = sc.Next(now)
		}
	}
	for id, e := range s.entries {
		if listed[id] {
			continue
		}
		delete(s.entries, id)
		s.dequeue(e)
		if e.running {
			e.deleted = true
			e.cancel()
		}
	}
}

// rearm gives every schedule that is waiting for its time the first one after
// now. Run calls it when it starts, so that a schedule whose time passed while
// nothing was running waits for the next, as ADR-0029 says.
func (s *Scheduler) rearm(now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, e := range s.entries {
		if !e.sc.Paused && !e.queued && !e.running {
			e.next = e.sc.Next(now)
		}
	}
}

// enqueueDue queues every schedule whose time has come, each with its grid
// time as the time it became due. The grid times after that one are dropped
// until its run has ended.
func (s *Scheduler) enqueueDue(now time.Time) {
	for _, e := range s.entries {
		if !e.armed() {
			continue
		}
		// A time further off than one interval means the clock was set back.
		if e.next.Sub(now) > e.interval() {
			e.next = e.sc.Next(now)
		}
		if e.next.After(now) {
			continue
		}
		e.due, e.next = e.next, time.Time{}
		s.push(e)
	}
}

// untilNext is how long the loop may sleep: until the earliest next time, but
// at most maxSleep, and after a failed read at most retryEvery. It is
// negative when nothing is scheduled and nothing needs retrying, and the loop
// then waits to be told.
func (s *Scheduler) untilNext(now time.Time) time.Duration {
	wait := time.Duration(-1)
	for _, e := range s.entries {
		if !e.armed() {
			continue
		}
		if d := e.next.Sub(now); wait < 0 || d < wait {
			wait = d
		}
	}
	if wait > maxSleep {
		wait = maxSleep
	}
	if s.stale {
		// Until the retry is due, which is never past retryEvery from now.
		if left := max(s.failedAt.Add(retryEvery).Sub(now), 0); wait < 0 || wait > left {
			wait = left
		}
	}
	return wait
}

// push puts e in the queue, ordered by when it became due and then by id. A
// schedule already queued stays where it is.
func (s *Scheduler) push(e *entry) {
	if e.queued {
		return
	}
	e.queued = true
	i, _ := slices.BinarySearchFunc(s.queue, e, func(q, target *entry) int {
		if c := q.due.Compare(target.due); c != 0 {
			return c
		}
		return cmp.Compare(q.sc.ID, target.sc.ID)
	})
	s.queue = slices.Insert(s.queue, i, e)
}

// pop takes the head of the queue, or returns nil when it is empty.
func (s *Scheduler) pop() *entry {
	if len(s.queue) == 0 {
		return nil
	}
	e := s.queue[0]
	s.queue = slices.Delete(s.queue, 0, 1)
	e.queued = false
	return e
}

// dequeue takes e out of the queue, if it is in it.
func (s *Scheduler) dequeue(e *entry) {
	if !e.queued {
		return
	}
	e.queued = false
	s.queue = slices.DeleteFunc(s.queue, func(q *entry) bool { return q == e })
}
