package metrics

import (
	"slices"
	"strconv"
	"strings"
	"sync"

	"github.com/redlinelabs-dev/dyno/server/internal/result"
)

// ScheduleSeries names one schedule's series: the schedule label is its id,
// the name label its name.
type ScheduleSeries struct {
	ID   int64
	Name string
}

// SetSchedules sets the schedules that have series, and drops the series
// of any schedule not in list. A schedule's runs_total pair starts at 0.
func (m *Metrics) SetSchedules(list []ScheduleSeries) {
	if m != nil {
		m.sched.set(list)
	}
}

// ScheduleRan counts a finished run of schedule id: failed when r.Failure is
// set. A successful r also sets the schedule's last-success gauges from r.
// A schedule not set with SetSchedules is ignored.
func (m *Metrics) ScheduleRan(id int64, r result.Result) {
	if m != nil {
		m.sched.ran(id, r)
	}
}

// ScheduleRestore sets schedule id's last-success gauges from r, its newest
// successful result, without counting a run. The scheduler calls it at
// start. A schedule not set with SetSchedules is ignored.
func (m *Metrics) ScheduleRestore(id int64, r result.Result) {
	if m != nil {
		m.sched.restore(id, r)
	}
}

// schedules holds the per-schedule series. Its zero value holds none and is
// ready to use. It has its own lock, apart from Metrics.mu: a scrape of the
// schedules never waits for a result being counted, and the reverse.
type schedules struct {
	mu     sync.Mutex
	series map[int64]*scheduleSeries
}

// scheduleSeries is one schedule's counters and last-success gauges.
type scheduleSeries struct {
	name      string
	succeeded uint64
	failed    uint64
	last      lastSuccess
}

// lastSuccess is what a schedule's newest successful result sets the gauges
// to, in Prometheus base units.
type lastSuccess struct {
	// ok is false until the schedule has a success.
	ok bool

	// at is when the result was stored, in Unix seconds.
	at float64

	// down and up are bytes per second.
	down, up float64

	// idle and loaded are seconds. loaded is 0 when it wasn't measured, and
	// then has no series.
	idle, loaded float64
}

// lastOf is the gauges r sets. Results hold Mbit/s and ms; the metrics use
// base units, as ObserveResult does.
func lastOf(r result.Result) lastSuccess {
	return lastSuccess{
		ok:     true,
		at:     float64(r.At.UnixMilli()) / 1e3,
		down:   r.DownloadMbps * 1e6 / 8,
		up:     r.UploadMbps * 1e6 / 8,
		idle:   r.IdleMs / 1e3,
		loaded: max(r.LoadedDownMs, r.LoadedUpMs) / 1e3,
	}
}

// set keeps the series of the schedules in list, with their new names, adds
// the ones it hasn't seen, and drops the rest. A name that isn't valid UTF-8,
// which only a row edited by hand can hold, is repaired: the text format is
// UTF-8, and one bad label can fail the whole scrape.
func (s *schedules) set(list []ScheduleSeries) {
	s.mu.Lock()
	defer s.mu.Unlock()
	next := make(map[int64]*scheduleSeries, len(list))
	for _, l := range list {
		sr := s.series[l.ID]
		if sr == nil {
			sr = new(scheduleSeries)
		}
		sr.name = strings.ToValidUTF8(l.Name, "�")
		next[l.ID] = sr
	}
	s.series = next
}

func (s *schedules) ran(id int64, r result.Result) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sr := s.series[id]
	switch {
	case sr == nil:
	case r.Failure != nil:
		sr.failed++
	default:
		sr.succeeded++
		sr.last = lastOf(r)
	}
}

func (s *schedules) restore(id int64, r result.Result) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if sr := s.series[id]; sr != nil && r.Failure == nil {
		sr.last = lastOf(r)
	}
}

// append appends the schedule metric families to b; a family with no series
// is left out.
func (s *schedules) append(b []byte) []byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.series) == 0 {
		return b
	}
	ids := make([]int64, 0, len(s.series))
	for id := range s.series {
		ids = append(ids, id)
	}
	slices.Sort(ids)

	b = header(b, "dyno_schedule_runs_total", "counter", "Scheduled runs finished since the server started, by schedule and result.")
	for _, id := range ids {
		sr := s.series[id]
		b = appendRuns(b, id, sr.name, "succeeded", sr.succeeded)
		b = appendRuns(b, id, sr.name, "failed", sr.failed)
	}
	b = s.appendGauge(b, ids, "dyno_schedule_last_success_timestamp_seconds",
		"When the schedule's newest successful result was stored, in Unix seconds.",
		func(l *lastSuccess) (float64, bool) { return l.at, l.ok })
	b = s.appendGauge(b, ids, "dyno_schedule_download_bytes_per_second",
		"Download speed of the schedule's newest successful result.",
		func(l *lastSuccess) (float64, bool) { return l.down, l.ok })
	b = s.appendGauge(b, ids, "dyno_schedule_upload_bytes_per_second",
		"Upload speed of the schedule's newest successful result.",
		func(l *lastSuccess) (float64, bool) { return l.up, l.ok })
	b = s.appendGauge(b, ids, "dyno_schedule_idle_latency_seconds",
		"Idle latency of the schedule's newest successful result.",
		func(l *lastSuccess) (float64, bool) { return l.idle, l.ok })
	b = s.appendGauge(b, ids, "dyno_schedule_loaded_latency_seconds",
		"The worse of the two loaded latencies of the schedule's newest successful result. Left out when it wasn't measured.",
		func(l *lastSuccess) (float64, bool) { return l.loaded, l.ok && l.loaded > 0 })
	return b
}

// appendGauge appends one gauge family: a series for each schedule in ids that
// get has a value for, and nothing when none has. The caller holds s.mu.
func (s *schedules) appendGauge(b []byte, ids []int64, name, help string, get func(*lastSuccess) (float64, bool)) []byte {
	declared := false
	for _, id := range ids {
		sr := s.series[id]
		v, ok := get(&sr.last)
		if !ok {
			continue
		}
		if !declared {
			b = header(b, name, "gauge", help)
			declared = true
		}
		b = appendScheduleLabels(b, name, id, sr.name)
		b = append(b, "} "...)
		b = appendFloat(b, v)
		b = append(b, '\n')
	}
	return b
}

// appendRuns appends one dyno_schedule_runs_total sample.
func appendRuns(b []byte, id int64, name, outcome string, n uint64) []byte {
	b = appendScheduleLabels(b, "dyno_schedule_runs_total", id, name)
	b = append(b, `,result="`...)
	b = append(b, outcome...)
	b = append(b, `"} `...)
	b = strconv.AppendUint(b, n, 10)
	return append(b, '\n')
}

// appendScheduleLabels appends a sample's name and the schedule and name
// labels, up to the closing quote of the last one.
func appendScheduleLabels(b []byte, family string, id int64, name string) []byte {
	b = append(b, family...)
	b = append(b, `{schedule="`...)
	b = strconv.AppendInt(b, id, 10)
	b = append(b, `",name="`...)
	b = appendLabelValue(b, name)
	return append(b, '"')
}
