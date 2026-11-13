// Package schedule defines a scheduled test's settings and its timetable
// (ADR-0028, ADR-0029). It does no I/O: the store keeps schedules, and
// package scheduler runs them.
package schedule

import "time"

// Kinds of test a schedule runs. They match nodetest's.
const (
	Light = "light" // at most 5 s per direction and 100 MiB each way; the default
	Full  = "full"  // the far end's plan as it is
)

// Limits and defaults (ADR-0029).
const (
	MaxSchedules = 16     // schedules that can exist at once
	MaxNameLen   = 64     // bytes in a name
	DefaultEvery = 180    // minutes between runs when a schedule doesn't say
	DefaultPort  = "3966" // a far end's port when neither a scheme nor a port is given
)

// Intervals are the minutes a schedule may run every, shortest first. Each
// divides a day, so a schedule's times are the same every day.
var Intervals = []int{15, 20, 30, 60, 120, 180, 240, 360, 480, 720, 1440}

// Schedule is one scheduled test's settings.
type Schedule struct {
	ID            int64     `json:"id"`
	Name          string    `json:"name"`
	FarEnd        string    `json:"far_end"`        // base URL, as NormalizeFarEnd returns it
	Kind          string    `json:"kind"`           // Light or Full
	EveryMinutes  int       `json:"every_minutes"`  // one of Intervals
	OffsetMinutes int       `json:"offset_minutes"` // 0 to EveryMinutes-1
	Paused        bool      `json:"paused"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}

// Patch is a change to some of a schedule's fields; nil fields stay as they
// are.
type Patch struct {
	Name          *string `json:"name"`
	FarEnd        *string `json:"far_end"`
	Kind          *string `json:"kind"`
	EveryMinutes  *int    `json:"every_minutes"`
	OffsetMinutes *int    `json:"offset_minutes"`
	Paused        *bool   `json:"paused"`
}

// Status is what the scheduler is doing with a schedule right now.
type Status struct {
	Running bool // a run of it is going
	Queued  bool // a run of it is waiting for another schedule's run
}

// NormalizeFarEnd turns an address as a person types it into a far end's
// base URL. Surrounding spaces are trimmed. No scheme means http, and then
// no port means DefaultPort; with a scheme, that scheme's own port applies
// and none is added. Scheme and host are lowercased, and a lone trailing "/"
// is dropped. Only http and https are allowed, with a host and without a
// path, query, fragment or user info. An IPv6 address must be in brackets.
// It is idempotent: a base URL it returned comes back unchanged.
//
//	"tailmeadow"              -> "http://tailmeadow:3966"
//	"TailMeadow:8080"         -> "http://tailmeadow:8080"
//	"https://dyno.example/"   -> "https://dyno.example"
//	"[fd7a::1]"               -> "http://[fd7a::1]:3966"
func NormalizeFarEnd(in string) (string, error) { panic("not implemented") }

// Validate checks s and fills in what it leaves empty, ignoring ID and the
// times. FarEnd is normalized. Name is trimmed of surrounding spaces and,
// when that leaves it empty, set to the far end's host without port or
// brackets; it must then be at most MaxNameLen bytes of valid UTF-8 with no
// control characters. An empty Kind means Light, and an EveryMinutes of 0
// means DefaultEvery. The error names the field by its JSON name.
func (s *Schedule) Validate() error { panic("not implemented") }

// Apply returns s with p's non-nil fields copied in. It doesn't validate.
func (s Schedule) Apply(p Patch) Schedule { panic("not implemented") }

// Next returns the first time on s's grid strictly after t: midnight UTC of
// some day, plus OffsetMinutes, plus a whole number of EveryMinutes. The
// result is in UTC. It ignores Paused.
func (s Schedule) Next(t time.Time) time.Time { panic("not implemented") }
