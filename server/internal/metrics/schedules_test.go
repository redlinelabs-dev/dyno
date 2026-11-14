package metrics_test

// The schedule metrics (docs/api.md "Metrics", ADR-0029): cases R19 and R23,
// driven straight through SetSchedules, ScheduleRan and ScheduleRestore, and
// read back from the exposition Append writes.

import (
	"fmt"
	"math"
	"runtime/debug"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/redlinelabs-dev/dyno/server/internal/metrics"
	"github.com/redlinelabs-dev/dyno/server/internal/result"
)

// The families the schedule gauges are in; the counter is runs_total.
var rnGaugeFamilies = []string{
	"dyno_schedule_last_success_timestamp_seconds",
	"dyno_schedule_download_bytes_per_second",
	"dyno_schedule_upload_bytes_per_second",
	"dyno_schedule_idle_latency_seconds",
	"dyno_schedule_loaded_latency_seconds",
}

// rnGuard, deferred, turns a panic into a failure of t, so that one call that
// panics doesn't end the package's other tests.
func rnGuard(t *testing.T) {
	if p := recover(); p != nil {
		t.Fatalf("panic: %v\n%s", p, debug.Stack())
	}
}

// ---- results to count ----

var rnAt = time.Date(2026, 11, 13, 17, 2, 11, 0, time.UTC)

// rnSuccess is a successful scheduled result: 940.5 Mbit/s down, 410.25 up,
// 8.4 ms idle, and 21 and 39.5 ms loaded.
func rnSuccess(at time.Time) result.Result {
	return result.Result{
		At: at, Source: "schedule", FarEnd: "http://a:3966",
		Submission: result.Submission{
			DownloadMbps: 940.5, UploadMbps: 410.25, IdleMs: 8.4,
			LoadedDownMs: 21, LoadedUpMs: 39.5, JitterMs: 0.82, Streams: 5,
		},
	}
}

// rnFailure is a failed run: no measurements, and a failure.
func rnFailure(at time.Time) result.Result {
	return result.Result{
		At: at, Source: "schedule", FarEnd: "http://a:3966",
		Failure: &result.Failure{Code: "unreachable", Message: "no route to the far end"},
	}
}

// rnGaugesOf is what the gauges should read for a successful r, worked out
// the way docs/api.md says: Mbit/s become bytes per second (x 125000), ms
// become seconds, and the loaded latency is the worse direction.
func rnGaugesOf(r result.Result) *rnGauges {
	return &rnGauges{
		At:     float64(r.At.Unix()),
		Down:   r.DownloadMbps * 125000,
		Up:     r.UploadMbps * 125000,
		Idle:   r.IdleMs / 1000,
		Loaded: max(r.LoadedDownMs, r.LoadedUpMs) / 1000,
	}
}

// ---- reading the exposition ----

type rnSample struct {
	Name   string
	Labels map[string]string
	Value  float64
}

type rnExpo struct {
	Text    string
	Samples []rnSample
	Types   map[string]string // family name to its # TYPE
}

// rnRead writes the exposition and reads it strictly: every line is a HELP
// line, a TYPE line or a sample, every sample follows the TYPE line of its
// family, and no series appears twice.
func rnRead(t *testing.T, m *metrics.Metrics) rnExpo {
	t.Helper()
	text := string(m.Append(nil))
	if !strings.HasSuffix(text, "\n") {
		t.Fatalf("the exposition doesn't end in a newline")
	}
	ex := rnExpo{Text: text, Types: map[string]string{}}
	seen := map[string]bool{}
	for i, line := range strings.Split(strings.TrimSuffix(text, "\n"), "\n") {
		switch {
		case strings.HasPrefix(line, "# HELP "):
		case strings.HasPrefix(line, "# TYPE "):
			f := strings.Fields(line)
			if len(f) != 4 {
				t.Fatalf("exposition line %d: bad TYPE line %q", i+1, line)
			}
			if _, dup := ex.Types[f[2]]; dup {
				t.Fatalf("exposition line %d: family %s declared twice", i+1, f[2])
			}
			ex.Types[f[2]] = f[3]
		case strings.HasPrefix(line, "#"):
			t.Fatalf("exposition line %d: stray comment %q", i+1, line)
		default:
			s, err := rnParseSample(line)
			if err != nil {
				t.Fatalf("exposition line %d: %v", i+1, err)
			}
			family := s.Name
			if _, ok := ex.Types[family]; !ok { // a histogram's _bucket, _sum or _count
				for _, suffix := range []string{"_bucket", "_sum", "_count"} {
					if base, found := strings.CutSuffix(s.Name, suffix); found {
						family = base
					}
				}
			}
			if _, ok := ex.Types[family]; !ok {
				t.Fatalf("exposition line %d: the sample %s has no TYPE line before it", i+1, s.Name)
			}
			keys := make([]string, 0, len(s.Labels))
			for k := range s.Labels {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			id := s.Name
			for _, k := range keys {
				id += "\x00" + k + "=" + s.Labels[k]
			}
			if seen[id] {
				t.Fatalf("exposition line %d: the series %s%v appears twice", i+1, s.Name, s.Labels)
			}
			seen[id] = true
			ex.Samples = append(ex.Samples, s)
		}
	}
	return ex
}

func rnParseSample(line string) (rnSample, error) {
	s := rnSample{Labels: map[string]string{}}
	i := strings.IndexAny(line, "{ ")
	if i <= 0 {
		return s, fmt.Errorf("no metric name or value in %q", line)
	}
	s.Name = line[:i]
	rest := line[i:]
	if rest[0] == '{' {
		rest = rest[1:]
		for {
			eq := strings.IndexByte(rest, '=')
			if eq <= 0 || len(rest) < eq+2 || rest[eq+1] != '"' {
				return s, fmt.Errorf("bad label in %q", line)
			}
			key := rest[:eq]
			rest = rest[eq+2:]
			var val strings.Builder
			for {
				if rest == "" {
					return s, fmt.Errorf("unterminated label value in %q", line)
				}
				c := rest[0]
				rest = rest[1:]
				if c == '"' {
					break
				}
				if c == '\\' {
					if rest == "" {
						return s, fmt.Errorf("dangling escape in %q", line)
					}
					switch rest[0] {
					case '\\':
						val.WriteByte('\\')
					case '"':
						val.WriteByte('"')
					case 'n':
						val.WriteByte('\n')
					default:
						return s, fmt.Errorf("bad escape \\%c in %q", rest[0], line)
					}
					rest = rest[1:]
					continue
				}
				val.WriteByte(c)
			}
			if _, dup := s.Labels[key]; dup {
				return s, fmt.Errorf("label %s twice in %q", key, line)
			}
			s.Labels[key] = val.String()
			if strings.HasPrefix(rest, ",") {
				rest = rest[1:]
				continue
			}
			if !strings.HasPrefix(rest, "}") {
				return s, fmt.Errorf("label set not closed in %q", line)
			}
			rest = rest[1:]
			break
		}
	}
	if !strings.HasPrefix(rest, " ") || strings.Contains(rest[1:], " ") {
		return s, fmt.Errorf("want exactly one value and no timestamp in %q", line)
	}
	v, err := strconv.ParseFloat(rest[1:], 64)
	if err != nil {
		return s, fmt.Errorf("value in %q: %v", line, err)
	}
	s.Value = v
	return s, nil
}

// rnGauges are one schedule's last-success gauges, in the units the exposition
// uses: seconds, bytes per second and seconds.
type rnGauges struct {
	At, Down, Up, Idle float64
	Loaded             float64 // 0 means the series is absent
}

// rnSeries is what the exposition should hold for one schedule.
type rnSeries struct {
	Name      string // the name label
	Succeeded float64
	Failed    float64
	Last      *rnGauges // nil means no last-success series at all
}

// rnWantSeries checks that ex holds exactly the series in want for schedule
// id, with exactly their labels, and no others.
func rnWantSeries(t *testing.T, ex rnExpo, id int64, want rnSeries) {
	t.Helper()
	sid := strconv.FormatInt(id, 10)
	got := map[string]float64{}
	for _, s := range ex.Samples {
		if !strings.HasPrefix(s.Name, "dyno_schedule_") || s.Labels["schedule"] != sid {
			continue
		}
		key, labels := s.Name, 2
		if s.Name == "dyno_schedule_runs_total" {
			key, labels = key+"/"+s.Labels["result"], 3
		}
		if len(s.Labels) != labels {
			t.Errorf("schedule %d: %s has labels %v, want %d of them", id, s.Name, s.Labels, labels)
		}
		if s.Labels["name"] != want.Name {
			t.Errorf("schedule %d: %s has name %q, want %q", id, key, s.Labels["name"], want.Name)
		}
		got[key] = s.Value
	}
	expect := map[string]float64{
		"dyno_schedule_runs_total/succeeded": want.Succeeded,
		"dyno_schedule_runs_total/failed":    want.Failed,
	}
	if g := want.Last; g != nil {
		expect["dyno_schedule_last_success_timestamp_seconds"] = g.At
		expect["dyno_schedule_download_bytes_per_second"] = g.Down
		expect["dyno_schedule_upload_bytes_per_second"] = g.Up
		expect["dyno_schedule_idle_latency_seconds"] = g.Idle
		if g.Loaded != 0 {
			expect["dyno_schedule_loaded_latency_seconds"] = g.Loaded
		}
	}
	for key, w := range expect {
		v, ok := got[key]
		if !ok {
			t.Errorf("schedule %d: no series %s", id, key)
			continue
		}
		tol := 1e-9 * math.Max(1, math.Abs(w))
		if key == "dyno_schedule_last_success_timestamp_seconds" {
			tol = 1
		}
		if math.Abs(v-w) > tol {
			t.Errorf("schedule %d: %s = %v, want %v", id, key, v, w)
		}
	}
	for key := range got {
		if _, ok := expect[key]; !ok {
			t.Errorf("schedule %d: unexpected series %s", id, key)
		}
	}
}

// rnWantNoSeries checks that ex holds no schedule series for id.
func rnWantNoSeries(t *testing.T, ex rnExpo, id int64) {
	t.Helper()
	sid := strconv.FormatInt(id, 10)
	for _, s := range ex.Samples {
		if strings.HasPrefix(s.Name, "dyno_schedule_") && s.Labels["schedule"] == sid {
			t.Errorf("schedule %d: the series %s%v should be absent", id, s.Name, s.Labels)
		}
	}
}

// rnWantName checks that every schedule series of id carries name, and that
// there's at least one.
func rnWantName(t *testing.T, ex rnExpo, id int64, name string) {
	t.Helper()
	sid := strconv.FormatInt(id, 10)
	n := 0
	for _, s := range ex.Samples {
		if !strings.HasPrefix(s.Name, "dyno_schedule_") || s.Labels["schedule"] != sid {
			continue
		}
		n++
		if s.Labels["name"] != name {
			t.Errorf("schedule %d: %s has name %q, want %q", id, s.Name, s.Labels["name"], name)
		}
	}
	if n == 0 {
		t.Errorf("schedule %d has no series", id)
	}
}

// rnWantType checks a family's TYPE line.
func rnWantType(t *testing.T, ex rnExpo, family, typ string) {
	t.Helper()
	if got := ex.Types[family]; got != typ {
		t.Errorf("%s has TYPE %q, want %q", family, got, typ)
	}
}

// rnNoScheduleText checks that the exposition says nothing about schedules.
func rnNoScheduleText(t *testing.T, ex rnExpo) {
	t.Helper()
	if strings.Contains(ex.Text, "dyno_schedule_") {
		t.Errorf("the exposition holds schedule metrics, want none:\n%s", rnScheduleLines(ex.Text))
	}
}

func rnScheduleLines(text string) string {
	var out []string
	for _, line := range strings.Split(text, "\n") {
		if strings.Contains(line, "dyno_schedule_") {
			out = append(out, line)
		}
	}
	return strings.Join(out, "\n")
}

// ---- R19 ----

func TestR19_SetSchedulesStartsEveryRunsPairAtZero(t *testing.T) {
	defer rnGuard(t)
	m := metrics.New("dev")
	rnNoScheduleText(t, rnRead(t, m)) // none set, none shown

	m.SetSchedules([]metrics.ScheduleSeries{{ID: 1, Name: "home"}, {ID: 7, Name: "office"}})
	ex := rnRead(t, m)
	rnWantSeries(t, ex, 1, rnSeries{Name: "home"})
	rnWantSeries(t, ex, 7, rnSeries{Name: "office"})
	rnWantType(t, ex, "dyno_schedule_runs_total", "counter")
	// Neither has had a success, so the gauge families have no series. A family
	// with no series is left out altogether.
	for _, family := range rnGaugeFamilies {
		if strings.Contains(ex.Text, family) {
			t.Errorf("%s appears before any schedule has a success", family)
		}
	}
}

func TestR19_SuccessCountsAndSetsEveryGauge(t *testing.T) {
	defer rnGuard(t)
	m := metrics.New("dev")
	m.SetSchedules([]metrics.ScheduleSeries{{ID: 1, Name: "home"}, {ID: 2, Name: "office"}})

	m.ScheduleRan(1, rnSuccess(rnAt))
	ex := rnRead(t, m)
	// 940.5 Mbit/s is 117,562,500 bytes per second, and 410.25 is 51,281,250;
	// 8.4 ms is 0.0084 s; the worse loaded latency is 39.5 ms, 0.0395 s.
	rnWantSeries(t, ex, 1, rnSeries{Name: "home", Succeeded: 1,
		Last: &rnGauges{At: float64(rnAt.Unix()), Down: 117562500, Up: 51281250, Idle: 0.0084, Loaded: 0.0395}})
	rnWantSeries(t, ex, 2, rnSeries{Name: "office"})
	rnWantType(t, ex, "dyno_schedule_runs_total", "counter")
	for _, family := range rnGaugeFamilies {
		rnWantType(t, ex, family, "gauge")
	}

	// A second success counts again and replaces the gauges.
	later := rnSuccess(rnAt.Add(3 * time.Hour))
	later.DownloadMbps, later.UploadMbps, later.IdleMs, later.LoadedDownMs, later.LoadedUpMs = 100, 20, 12, 30, 25
	m.ScheduleRan(1, later)
	ex = rnRead(t, m)
	rnWantSeries(t, ex, 1, rnSeries{Name: "home", Succeeded: 2,
		Last: &rnGauges{At: float64(later.At.Unix()), Down: 12500000, Up: 2500000, Idle: 0.012, Loaded: 0.03}})
	rnWantSeries(t, ex, 2, rnSeries{Name: "office"})
}

func TestR19_TimestampIsTheResultsAtInUnixSeconds(t *testing.T) {
	defer rnGuard(t)
	m := metrics.New("dev")
	m.SetSchedules([]metrics.ScheduleSeries{{ID: 1, Name: "home"}})
	r := rnSuccess(time.Date(2026, 11, 13, 17, 2, 11, 408_000_000, time.UTC))
	m.ScheduleRan(1, r)
	ex := rnRead(t, m)
	for _, s := range ex.Samples {
		if s.Name == "dyno_schedule_last_success_timestamp_seconds" {
			if d := s.Value - float64(r.At.UnixMilli())/1e3; math.Abs(d) > 1 {
				t.Errorf("timestamp %v, want %d: seconds since the epoch, not ms or ns", s.Value, r.At.Unix())
			}
			return
		}
	}
	t.Error("no last-success timestamp")
}

func TestR19_FailureCountsAndLeavesTheGauges(t *testing.T) {
	defer rnGuard(t)
	m := metrics.New("dev")
	m.SetSchedules([]metrics.ScheduleSeries{{ID: 1, Name: "home"}})

	// A failure before any success counts, and makes no gauge.
	m.ScheduleRan(1, rnFailure(rnAt))
	ex := rnRead(t, m)
	rnWantSeries(t, ex, 1, rnSeries{Name: "home", Failed: 1})
	for _, family := range rnGaugeFamilies {
		if strings.Contains(ex.Text, family) {
			t.Errorf("%s appears after a failure, with no success", family)
		}
	}

	// After a success, further failures count and the gauges stay as they were.
	success := rnSuccess(rnAt.Add(time.Hour))
	m.ScheduleRan(1, success)
	m.ScheduleRan(1, rnFailure(rnAt.Add(2*time.Hour)))
	m.ScheduleRan(1, rnFailure(rnAt.Add(3*time.Hour)))
	ex = rnRead(t, m)
	rnWantSeries(t, ex, 1, rnSeries{Name: "home", Succeeded: 1, Failed: 3, Last: rnGaugesOf(success)})
}

func TestR19_LoadedLatencyIsTheWorseDirectionAndAbsentWhenNotMeasured(t *testing.T) {
	defer rnGuard(t)
	m := metrics.New("dev")
	m.SetSchedules([]metrics.ScheduleSeries{{ID: 1, Name: "home"}})
	for i, c := range []struct {
		down, up float64 // loaded latency in ms
		want     float64 // the gauge in seconds; 0 means absent
	}{
		{50, 20, 0.05},
		{20, 50, 0.05},
		{0, 35, 0.035},
		{35, 0, 0.035},
		{0, 0, 0},
		{12, 12, 0.012},
	} {
		r := rnSuccess(rnAt.Add(time.Duration(i) * time.Hour))
		r.LoadedDownMs, r.LoadedUpMs = c.down, c.up
		m.ScheduleRan(1, r)
		ex := rnRead(t, m)
		// The other gauges are there every time, and the loaded one follows the
		// newest success: with none measured it goes, and doesn't linger.
		rnWantSeries(t, ex, 1, rnSeries{Name: "home", Succeeded: float64(i + 1),
			Last: &rnGauges{At: float64(r.At.Unix()), Down: 117562500, Up: 51281250, Idle: 0.0084, Loaded: c.want}})
	}
}

func TestR19_RestoreSetsGaugesWithoutCounting(t *testing.T) {
	defer rnGuard(t)
	m := metrics.New("dev")
	m.SetSchedules([]metrics.ScheduleSeries{{ID: 1, Name: "home"}, {ID: 2, Name: "office"}})
	r := rnSuccess(rnAt)
	m.ScheduleRestore(1, r)
	ex := rnRead(t, m)
	rnWantSeries(t, ex, 1, rnSeries{Name: "home", Last: rnGaugesOf(r)}) // both counters still 0
	rnWantSeries(t, ex, 2, rnSeries{Name: "office"})

	// Runs go on counting from there, and a failure leaves the restored gauges.
	m.ScheduleRan(1, rnFailure(rnAt.Add(time.Hour)))
	ex = rnRead(t, m)
	rnWantSeries(t, ex, 1, rnSeries{Name: "home", Failed: 1, Last: rnGaugesOf(r)})
}

func TestR19_SetSchedulesDropsRemovedSeriesAndKeepsTheRest(t *testing.T) {
	defer rnGuard(t)
	m := metrics.New("dev")
	m.SetSchedules([]metrics.ScheduleSeries{{ID: 1, Name: "a"}, {ID: 2, Name: "b"}})
	good := rnSuccess(rnAt)
	m.ScheduleRan(1, good)
	m.ScheduleRan(2, good)
	m.ScheduleRan(2, rnFailure(rnAt.Add(time.Hour)))

	// b is gone, with all its series; a keeps its counts and gauges.
	m.SetSchedules([]metrics.ScheduleSeries{{ID: 1, Name: "a"}})
	ex := rnRead(t, m)
	rnWantNoSeries(t, ex, 2)
	rnWantSeries(t, ex, 1, rnSeries{Name: "a", Succeeded: 1, Last: rnGaugesOf(good)})
	m.ScheduleRan(2, good) // and a run of it that finishes now is ignored
	rnWantNoSeries(t, rnRead(t, m), 2)

	// A new schedule starts at 0. Setting the same list again changes nothing.
	for range 2 {
		m.SetSchedules([]metrics.ScheduleSeries{{ID: 1, Name: "a"}, {ID: 3, Name: "c"}})
		ex = rnRead(t, m)
		rnWantSeries(t, ex, 1, rnSeries{Name: "a", Succeeded: 1, Last: rnGaugesOf(good)})
		rnWantSeries(t, ex, 3, rnSeries{Name: "c"})
	}

	// A renamed schedule shows its new name, and the old one is gone.
	m.SetSchedules([]metrics.ScheduleSeries{{ID: 1, Name: "renamed"}, {ID: 3, Name: "c"}})
	ex = rnRead(t, m)
	rnWantName(t, ex, 1, "renamed")
	rnWantName(t, ex, 3, "c")
	for _, s := range ex.Samples {
		if s.Labels["name"] == "a" {
			t.Errorf("the old name is still there: %s%v", s.Name, s.Labels)
		}
	}

	// With none left, nothing about schedules is left.
	m.SetSchedules(nil)
	rnNoScheduleText(t, rnRead(t, m))
}

func TestR19_LabelValuesAreEscaped(t *testing.T) {
	defer rnGuard(t)
	m := metrics.New("dev")
	quoted, lines := `Gran's "den" \ home`, "two\nlines"
	m.SetSchedules([]metrics.ScheduleSeries{{ID: 1, Name: quoted}, {ID: 2, Name: lines}})
	good := rnSuccess(rnAt)
	m.ScheduleRan(1, good)
	text := string(m.Append(nil))
	for _, want := range []string{`name="Gran's \"den\" \\ home"`, `name="two\nlines"`} {
		if !strings.Contains(text, want) {
			t.Errorf("no label %s in:\n%s", want, rnScheduleLines(text))
		}
	}
	// Read back, the values come out as they went in, and no line broke in two.
	ex := rnRead(t, m)
	rnWantSeries(t, ex, 1, rnSeries{Name: quoted, Succeeded: 1, Last: rnGaugesOf(good)})
	rnWantSeries(t, ex, 2, rnSeries{Name: lines})
}

func TestR19_ScrapesWhileRunsAreCounted(t *testing.T) {
	defer rnGuard(t)
	m := metrics.New("dev")
	m.SetSchedules([]metrics.ScheduleSeries{{ID: 1, Name: "home"}})
	good := rnSuccess(rnAt)
	const workers, each = 4, 100
	var wg sync.WaitGroup
	for w := range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() {
				if p := recover(); p != nil {
					t.Errorf("worker %d: panic: %v", w, p)
				}
			}()
			for i := range each {
				if (w+i)%2 == 0 {
					m.ScheduleRan(1, good)
				} else {
					m.ScheduleRan(1, rnFailure(rnAt))
				}
				if i%10 == 0 {
					m.Append(nil)
				}
			}
		}()
	}
	wg.Wait()
	// Each worker made 50 successes and 50 failures.
	rnWantSeries(t, rnRead(t, m), 1, rnSeries{Name: "home", Succeeded: 200, Failed: 200, Last: rnGaugesOf(good)})
}

// ---- R23 ----

func TestR23_NilMetricsDoNothing(t *testing.T) {
	defer rnGuard(t)
	var m *metrics.Metrics
	m.SetSchedules([]metrics.ScheduleSeries{{ID: 1, Name: "home"}})
	m.ScheduleRan(1, rnSuccess(rnAt))
	m.ScheduleRan(1, rnFailure(rnAt))
	m.ScheduleRestore(1, rnSuccess(rnAt))
	m.SetSchedules(nil)
}

func TestR23_UnknownSchedulesAreIgnored(t *testing.T) {
	defer rnGuard(t)
	m := metrics.New("dev")
	// No schedule has been set: nothing is counted or shown.
	m.ScheduleRan(5, rnSuccess(rnAt))
	m.ScheduleRan(5, rnFailure(rnAt))
	m.ScheduleRestore(5, rnSuccess(rnAt))
	rnNoScheduleText(t, rnRead(t, m))

	// Schedule 1 is set; calls for 2 are ignored, and 1's own are not.
	m.SetSchedules([]metrics.ScheduleSeries{{ID: 1, Name: "home"}})
	m.ScheduleRan(2, rnSuccess(rnAt))
	m.ScheduleRan(2, rnFailure(rnAt))
	m.ScheduleRestore(2, rnSuccess(rnAt))
	ex := rnRead(t, m)
	rnWantSeries(t, ex, 1, rnSeries{Name: "home"})
	rnWantNoSeries(t, ex, 2)
	m.ScheduleRan(1, rnFailure(rnAt))
	rnWantSeries(t, rnRead(t, m), 1, rnSeries{Name: "home", Failed: 1})

	// Once it is dropped, calls for it are ignored too, and nothing comes back.
	m.SetSchedules(nil)
	m.ScheduleRan(1, rnSuccess(rnAt))
	m.ScheduleRestore(1, rnSuccess(rnAt))
	rnNoScheduleText(t, rnRead(t, m))
}
