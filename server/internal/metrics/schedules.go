package metrics

import "github.com/redlinelabs-dev/dyno/server/internal/result"

// ScheduleSeries names one schedule's series: the schedule label is its id,
// the name label its name.
type ScheduleSeries struct {
	ID   int64
	Name string
}

// SetSchedules sets the schedules that have series, and drops the series
// of any schedule not in list. A schedule's runs_total pair starts at 0.
func (m *Metrics) SetSchedules(list []ScheduleSeries) { panic("not implemented") }

// ScheduleRan counts a finished run of schedule id: failed when r.Failure is
// set. A successful r also sets the schedule's last-success gauges from r.
// A schedule not set with SetSchedules is ignored.
func (m *Metrics) ScheduleRan(id int64, r result.Result) { panic("not implemented") }

// ScheduleRestore sets schedule id's last-success gauges from r, its newest
// successful result, without counting a run. The scheduler calls it at
// start. A schedule not set with SetSchedules is ignored.
func (m *Metrics) ScheduleRestore(id int64, r result.Result) { panic("not implemented") }
