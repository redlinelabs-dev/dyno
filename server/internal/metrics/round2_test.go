package metrics_test

// Round 2 of the schedule metrics, case R19b from docs/api.md "Metrics".
// dyno_schedule_last_success_timestamp_seconds keeps the milliseconds of the
// result's At, whether ScheduleRan or ScheduleRestore set it. The timestamp
// test in schedules_test.go allows a whole second of error, which hides a value
// cut to whole seconds.

import (
	"fmt"
	"math"
	"testing"
	"time"

	"github.com/redlinelabs-dev/dyno/server/internal/metrics"
)

// r2Timestamp returns schedule 1's last-success timestamp as the exposition
// shows it.
func r2Timestamp(t *testing.T, m *metrics.Metrics) float64 {
	t.Helper()
	for _, s := range rnRead(t, m).Samples {
		if s.Name == "dyno_schedule_last_success_timestamp_seconds" && s.Labels["schedule"] == "1" {
			return s.Value
		}
	}
	t.Fatal("no last-success timestamp for schedule 1")
	return 0
}

func TestR19b_TimestampKeepsTheMillisecondsOfAt(t *testing.T) {
	for _, ms := range []int{408, 1, 999} {
		at := time.Date(2026, 11, 13, 17, 2, 11, ms*int(time.Millisecond), time.UTC)
		want := float64(at.UnixMilli()) / 1e3
		for _, how := range []string{"ScheduleRan", "ScheduleRestore"} {
			t.Run(fmt.Sprintf("%s at 17:02:11.%03d", how, ms), func(t *testing.T) {
				defer rnGuard(t)
				m := metrics.New("dev")
				m.SetSchedules([]metrics.ScheduleSeries{{ID: 1, Name: "home"}})
				if how == "ScheduleRan" {
					m.ScheduleRan(1, rnSuccess(at))
				} else {
					m.ScheduleRestore(1, rnSuccess(at))
				}
				if got := r2Timestamp(t, m); math.Abs(got-want) > 0.0005 {
					t.Errorf("timestamp %.6f, want %.3f, the result's At to the millisecond, and it is %.6f s out", got, want, got-want)
				}
			})
		}
	}
}
