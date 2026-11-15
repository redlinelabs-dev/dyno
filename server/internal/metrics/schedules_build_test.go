package metrics_test

// The schedule metrics' own guards, beside the spec tests of schedules_test.go,
// on its helpers. None of these is a spec case.

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/redlinelabs-dev/dyno/server/internal/metrics"
)

// A name that isn't UTF-8 can only come from a row edited by hand, and one such
// label can make a scraper refuse the whole exposition.
func TestRx_ANameThatIsntUTF8IsRepaired(t *testing.T) {
	defer rnGuard(t)
	m := metrics.New("dev")
	m.SetSchedules([]metrics.ScheduleSeries{{ID: 1, Name: "bad\xffname"}})
	m.ScheduleRan(1, rnSuccess(rnAt))
	ex := rnRead(t, m)
	if !utf8.ValidString(ex.Text) {
		t.Errorf("the exposition isn't valid UTF-8:\n%s", rnScheduleLines(ex.Text))
	}
	rnWantSeries(t, ex, 1, rnSeries{Name: "bad�name", Succeeded: 1, Last: rnGaugesOf(rnSuccess(rnAt))})
}

// Restore is for the newest success. A failure given to it by mistake sets no
// gauge, and leaves what an earlier success set.
func TestRx_RestoreIgnoresAFailure(t *testing.T) {
	defer rnGuard(t)
	m := metrics.New("dev")
	m.SetSchedules([]metrics.ScheduleSeries{{ID: 1, Name: "home"}})
	m.ScheduleRestore(1, rnFailure(rnAt))
	ex := rnRead(t, m)
	rnWantSeries(t, ex, 1, rnSeries{Name: "home"})
	for _, family := range rnGaugeFamilies {
		if strings.Contains(ex.Text, family) {
			t.Errorf("%s appears after a failure was restored", family)
		}
	}

	good := rnSuccess(rnAt)
	m.ScheduleRestore(1, good)
	m.ScheduleRestore(1, rnFailure(rnAt.Add(1)))
	rnWantSeries(t, rnRead(t, m), 1, rnSeries{Name: "home", Last: rnGaugesOf(good)})
}

// Series come out in schedule id order, however they were set, so that two
// scrapes of the same state read the same.
func TestRx_SeriesComeOutInIDOrder(t *testing.T) {
	defer rnGuard(t)
	m := metrics.New("dev")
	m.SetSchedules([]metrics.ScheduleSeries{{ID: 3, Name: "c"}, {ID: 1, Name: "a"}, {ID: 2, Name: "b"}})
	for _, id := range []int64{3, 1, 2} {
		m.ScheduleRan(id, rnSuccess(rnAt))
	}
	text := string(m.Append(nil))
	for _, family := range []string{"dyno_schedule_runs_total", "dyno_schedule_idle_latency_seconds"} {
		first := strings.Index(text, family+`{schedule="1"`)
		second := strings.Index(text, family+`{schedule="2"`)
		third := strings.Index(text, family+`{schedule="3"`)
		if first < 0 || second < first || third < second {
			t.Errorf("%s: schedules 1, 2 and 3 are at %d, %d and %d, want them in that order", family, first, second, third)
		}
	}
	// The process metrics change between scrapes; the schedule series must not.
	if rnScheduleLines(text) != rnScheduleLines(string(m.Append(nil))) {
		t.Error("two scrapes of the same schedule state differ")
	}
}
