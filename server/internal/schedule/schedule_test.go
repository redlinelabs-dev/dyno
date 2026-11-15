package schedule_test

import (
	"reflect"
	"runtime/debug"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/redlinelabs-dev/dyno/server/internal/schedule"
)

// schNoPanic turns a panic in a test, such as a stub that isn't built yet, into
// a failure of that test alone instead of an abort of the whole package.
func schNoPanic(t *testing.T) {
	if r := recover(); r != nil {
		t.Fatalf("panic: %v\n%s", r, debug.Stack())
	}
}

// schNormalizeOK holds the addresses NormalizeFarEnd accepts (C1), and what it
// makes of them. C3 reuses it.
var schNormalizeOK = []struct{ in, want string }{
	{"tailmeadow", "http://tailmeadow:3966"},
	{" tailmeadow:3966 ", "http://tailmeadow:3966"},
	{"TailMeadow:8080", "http://tailmeadow:8080"},
	{"http://tailmeadow", "http://tailmeadow"}, // a scheme means no port is added
	{"https://dyno.example/", "https://dyno.example"},
	{"HTTPS://Dyno.Example:8443", "https://dyno.example:8443"},
	{"100.101.102.103", "http://100.101.102.103:3966"},
	{"[fd7a::1]", "http://[fd7a::1]:3966"},
	{"[fd7a::1]:4000", "http://[fd7a::1]:4000"},
	{"http://[FD7A::1]", "http://[fd7a::1]"},
}

// schNormalizeBad holds the addresses NormalizeFarEnd rejects (C2). C5 uses
// them as far ends Validate rejects.
var schNormalizeBad = []string{
	"", "   ", "ftp://h", "http://", "//h", "javascript:alert(1)", "http://user:pw@h",
	"http://h/path", "h/path", "http://h/?q=1", "http://h?q=1", "http://h#f",
	"fd7a::1", // IPv6 without brackets
	"h:0", "h:70000", "h:abc",
}

func TestC1_NormalizeFarEndAcceptsAndNormalizes(t *testing.T) {
	defer schNoPanic(t)
	for _, c := range schNormalizeOK {
		got, err := schedule.NormalizeFarEnd(c.in)
		if err != nil {
			t.Errorf("NormalizeFarEnd(%q): unexpected error %v", c.in, err)
			continue
		}
		if got != c.want {
			t.Errorf("NormalizeFarEnd(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestC2_NormalizeFarEndRejects(t *testing.T) {
	defer schNoPanic(t)
	for _, in := range schNormalizeBad {
		if got, err := schedule.NormalizeFarEnd(in); err == nil {
			t.Errorf("NormalizeFarEnd(%q) = %q, want an error", in, got)
		}
	}
}

func TestC3_NormalizeFarEndIsIdempotent(t *testing.T) {
	defer schNoPanic(t)
	for _, c := range schNormalizeOK {
		once, err := schedule.NormalizeFarEnd(c.in)
		if err != nil {
			t.Errorf("NormalizeFarEnd(%q): unexpected error %v", c.in, err)
			continue
		}
		twice, err := schedule.NormalizeFarEnd(once)
		if err != nil || twice != once {
			t.Errorf("NormalizeFarEnd(%q) = %q, again = %q (err %v), want it unchanged", c.in, once, twice, err)
		}
	}
}

func TestC4_ValidateFillsDefaults(t *testing.T) {
	defer schNoPanic(t)
	for _, c := range []struct {
		label    string
		in       schedule.Schedule
		wantFar  string
		wantName string
	}{
		{"hostname", schedule.Schedule{FarEnd: "tailmeadow"}, "http://tailmeadow:3966", "tailmeadow"},
		{"IPv6 host loses its brackets", schedule.Schedule{FarEnd: "[fd7a::1]"}, "http://[fd7a::1]:3966", "fd7a::1"},
		{"IPv4 host", schedule.Schedule{FarEnd: "100.101.102.103"}, "http://100.101.102.103:3966", "100.101.102.103"},
		{"host loses its port", schedule.Schedule{FarEnd: "https://dyno.example:8443"}, "https://dyno.example:8443", "dyno.example"},
		{"far end is normalized", schedule.Schedule{FarEnd: " TailMeadow:8080 "}, "http://tailmeadow:8080", "tailmeadow"},
		{"a name of spaces is empty", schedule.Schedule{FarEnd: "tailmeadow", Name: "   "}, "http://tailmeadow:3966", "tailmeadow"},
		{"name is trimmed", schedule.Schedule{FarEnd: "tailmeadow", Name: "  Gran's  "}, "http://tailmeadow:3966", "Gran's"},
	} {
		s := c.in
		if err := s.Validate(); err != nil {
			t.Errorf("%s: Validate: %v", c.label, err)
			continue
		}
		if s.FarEnd != c.wantFar || s.Name != c.wantName || s.Kind != schedule.Light ||
			s.EveryMinutes != schedule.DefaultEvery || s.OffsetMinutes != 0 || s.Paused {
			t.Errorf("%s: Validate gave %+v, want far end %q, name %q, light, every %d, offset 0, not paused",
				c.label, s, c.wantFar, c.wantName, schedule.DefaultEvery)
		}
	}
}

func TestC4_ValidateKeepsWhatItIsGiven(t *testing.T) {
	defer schNoPanic(t)
	// Zero offset and not paused stay as they are, and so does everything set.
	for _, in := range []schedule.Schedule{
		{Name: "Gran's", FarEnd: "http://tailmeadow:3966", Kind: schedule.Light, EveryMinutes: 15, OffsetMinutes: 0, Paused: false},
		{Name: "Office", FarEnd: "https://dyno.example", Kind: schedule.Full, EveryMinutes: 60, OffsetMinutes: 30, Paused: true},
	} {
		s := in
		if err := s.Validate(); err != nil {
			t.Errorf("Validate(%+v): %v", in, err)
			continue
		}
		if s != in {
			t.Errorf("Validate changed a valid schedule:\n in %+v\ngot %+v", in, s)
		}
	}
}

func TestC4_ValidateIgnoresIDAndTimes(t *testing.T) {
	defer schNoPanic(t)
	made := time.Date(2026, 11, 13, 17, 2, 11, 408_000_000, time.UTC)
	s := schedule.Schedule{ID: 7, FarEnd: "tailmeadow", CreatedAt: made, UpdatedAt: made}
	if err := s.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if s.ID != 7 || !s.CreatedAt.Equal(made) || !s.UpdatedAt.Equal(made) {
		t.Errorf("Validate touched the id or times: %+v", s)
	}
	fresh := schedule.Schedule{FarEnd: "tailmeadow"} // a schedule not stored yet has no id and no times
	if err := fresh.Validate(); err != nil {
		t.Errorf("Validate of a new schedule: %v", err)
	}
}

func TestC5_ValidateRejectsAndNamesTheField(t *testing.T) {
	defer schNoPanic(t)
	type badCase struct {
		label, field string
		change       func(*schedule.Schedule)
	}
	every := func(n int) func(*schedule.Schedule) {
		return func(s *schedule.Schedule) { s.EveryMinutes = n }
	}
	timing := func(every, offset int) func(*schedule.Schedule) {
		return func(s *schedule.Schedule) { s.EveryMinutes, s.OffsetMinutes = every, offset }
	}
	named := func(name string) func(*schedule.Schedule) {
		return func(s *schedule.Schedule) { s.Name = name }
	}

	cases := []badCase{
		{"kind medium", "kind", func(s *schedule.Schedule) { s.Kind = "medium" }},
		{"offset -1", "offset_minutes", timing(60, -1)},
		{"offset equal to every, 60", "offset_minutes", timing(60, 60)},
		{"offset equal to every, 1440", "offset_minutes", timing(1440, 1440)},
		{"offset past every", "offset_minutes", timing(60, 61)},
		{"name of 65 bytes", "name", named(strings.Repeat("a", 65))},
		{"name of 33 two-byte runes", "name", named(strings.Repeat("é", 33))},
		{"name with a newline", "name", named("Gran\nny")},
		{"name with a NUL", "name", named("Gran\x00ny")},
		{"name with U+0085", "name", named("Gran\u0085ny")},
		{"name that is not UTF-8", "name", named("Gran\xffny")},
	}
	for _, n := range []int{10, 45, 2880, -15} {
		cases = append(cases, badCase{"every_minutes " + strconv.Itoa(n), "every_minutes", every(n)})
	}
	for _, bad := range schNormalizeBad {
		cases = append(cases, badCase{"far end " + strconv.Quote(bad), "far_end",
			func(s *schedule.Schedule) { s.FarEnd = bad }})
	}

	for _, c := range cases {
		s := schedule.Schedule{Name: "Gran's", FarEnd: "tailmeadow", Kind: schedule.Light, EveryMinutes: 60}
		c.change(&s)
		err := s.Validate()
		if err == nil {
			t.Errorf("%s: Validate accepted %+v", c.label, s)
			continue
		}
		// The message begins with the field it is about, so an offset error,
		// which also says every_minutes, can't stand in for an every_minutes one.
		if !strings.HasPrefix(err.Error(), c.field) {
			t.Errorf("%s: error %q doesn't begin with the field %q", c.label, err, c.field)
		}
	}
}

func TestC5_ValidateAcceptsTheEdgesOfEachField(t *testing.T) {
	defer schNoPanic(t)
	for _, c := range []struct{ label, name string }{
		{"64 bytes", strings.Repeat("a", 64)},
		{"32 two-byte runes", strings.Repeat("é", 32)},
		{"64 bytes after trimming", "  " + strings.Repeat("a", 64) + "  "},
		{"multi-byte text", "Gran's Wi-Fi \u2603"},
	} {
		s := schedule.Schedule{Name: c.name, FarEnd: "tailmeadow", Kind: schedule.Full, EveryMinutes: 60}
		if err := s.Validate(); err != nil {
			t.Errorf("name of %s: %v", c.label, err)
		} else if s.Name != strings.TrimSpace(c.name) {
			t.Errorf("name of %s: Validate gave %q, want %q", c.label, s.Name, strings.TrimSpace(c.name))
		}
	}
	for _, kind := range []string{schedule.Light, schedule.Full} {
		s := schedule.Schedule{FarEnd: "tailmeadow", Kind: kind}
		if err := s.Validate(); err != nil {
			t.Errorf("kind %q: %v", kind, err)
		}
	}
	for _, every := range schedule.Intervals {
		for _, offset := range []int{0, every - 1} {
			s := schedule.Schedule{FarEnd: "tailmeadow", EveryMinutes: every, OffsetMinutes: offset}
			if err := s.Validate(); err != nil {
				t.Errorf("every %d, offset %d: %v", every, offset, err)
			}
		}
	}
}

func TestC6_ApplyCopiesOnlyNonNilFields(t *testing.T) {
	defer schNoPanic(t)
	base := schedule.Schedule{
		ID: 9, Name: "Gran's", FarEnd: "http://tailmeadow:3966", Kind: schedule.Light,
		EveryMinutes: 60, OffsetMinutes: 30, Paused: true,
		CreatedAt: time.Date(2026, 11, 13, 17, 2, 11, 408_000_000, time.UTC),
		UpdatedAt: time.Date(2026, 11, 13, 18, 0, 0, 0, time.UTC),
	}

	if got := base.Apply(schedule.Patch{}); !reflect.DeepEqual(got, base) {
		t.Errorf("a zero Patch changed the schedule:\n in %+v\ngot %+v", base, got)
	}

	str := func(v string) *string { return &v }
	num := func(v int) *int { return &v }
	flag := func(v bool) *bool { return &v }
	for _, c := range []struct {
		label string
		patch schedule.Patch
		want  func(*schedule.Schedule)
	}{
		{"name", schedule.Patch{Name: str("Office")}, func(s *schedule.Schedule) { s.Name = "Office" }},
		{"empty name", schedule.Patch{Name: str("")}, func(s *schedule.Schedule) { s.Name = "" }},
		{"far end", schedule.Patch{FarEnd: str("https://dyno.example")}, func(s *schedule.Schedule) { s.FarEnd = "https://dyno.example" }},
		{"kind", schedule.Patch{Kind: str(schedule.Full)}, func(s *schedule.Schedule) { s.Kind = schedule.Full }},
		{"every", schedule.Patch{EveryMinutes: num(120)}, func(s *schedule.Schedule) { s.EveryMinutes = 120 }},
		{"offset", schedule.Patch{OffsetMinutes: num(5)}, func(s *schedule.Schedule) { s.OffsetMinutes = 5 }},
		{"offset set to 0", schedule.Patch{OffsetMinutes: num(0)}, func(s *schedule.Schedule) { s.OffsetMinutes = 0 }},
		{"paused set to false", schedule.Patch{Paused: flag(false)}, func(s *schedule.Schedule) { s.Paused = false }},
		{"unvalidated value", schedule.Patch{EveryMinutes: num(45)}, func(s *schedule.Schedule) { s.EveryMinutes = 45 }},
	} {
		want := base
		c.want(&want)
		if got := base.Apply(c.patch); !reflect.DeepEqual(got, want) {
			t.Errorf("patching %s:\n got %+v\nwant %+v", c.label, got, want)
		}
	}

	// Every field at once, and the receiver is left alone.
	all := schedule.Patch{Name: str("Office"), FarEnd: str("https://dyno.example"), Kind: str(schedule.Full),
		EveryMinutes: num(15), OffsetMinutes: num(14), Paused: flag(false)}
	want := base
	want.Name, want.FarEnd, want.Kind, want.EveryMinutes, want.OffsetMinutes, want.Paused =
		"Office", "https://dyno.example", schedule.Full, 15, 14, false
	if got := base.Apply(all); !reflect.DeepEqual(got, want) {
		t.Errorf("patching every field:\n got %+v\nwant %+v", got, want)
	}
	if base.Name != "Gran's" || base.EveryMinutes != 60 || !base.Paused {
		t.Errorf("Apply changed its receiver: %+v", base)
	}
}

// schT is a UTC time in November 2026.
func schT(day, hour, minute, sec, nsec int) time.Time {
	return time.Date(2026, 11, day, hour, minute, sec, nsec, time.UTC)
}

func TestC7_NextIsTheFirstGridTimeAfter(t *testing.T) {
	defer schNoPanic(t)
	for _, c := range []struct {
		label         string
		every, offset int
		in, want      time.Time
	}{
		{"every 180, just before 18:00", 180, 0, schT(13, 17, 59, 59, 0), schT(13, 18, 0, 0, 0)},
		{"every 180, on 18:00", 180, 0, schT(13, 18, 0, 0, 0), schT(13, 21, 0, 0, 0)},
		{"every 180, into the next day", 180, 0, schT(13, 23, 30, 0, 0), schT(14, 0, 0, 0, 0)},
		{"every 180, a second before midnight", 180, 0, schT(13, 23, 59, 59, 0), schT(14, 0, 0, 0, 0)},
		{"every 180, across a year", 180, 0, time.Date(2026, 12, 31, 23, 59, 0, 0, time.UTC), time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)},
		{"daily at 03:00, before it", 1440, 180, schT(13, 2, 0, 0, 0), schT(13, 3, 0, 0, 0)},
		{"daily at 03:00, on it", 1440, 180, schT(13, 3, 0, 0, 0), schT(14, 3, 0, 0, 0)},
		{"daily at midnight, on it", 1440, 0, schT(13, 0, 0, 0, 0), schT(14, 0, 0, 0, 0)},
		{"daily at midnight, a nanosecond before the next", 1440, 0, schT(13, 23, 59, 59, 999_999_999), schT(14, 0, 0, 0, 0)},
		{"every 15 offset 7, from 10:00", 15, 7, schT(13, 10, 0, 0, 0), schT(13, 10, 7, 0, 0)},
		{"every 15 offset 7, a nanosecond past 10:07", 15, 7, schT(13, 10, 7, 0, 1), schT(13, 10, 22, 0, 0)},
		{"every 15 offset 7, after the day's last time", 15, 7, schT(13, 23, 55, 0, 0), schT(14, 0, 7, 0, 0)},
		{"every 20, at 23:50", 20, 0, schT(13, 23, 50, 0, 0), schT(14, 0, 0, 0, 0)},
		{"every 720 offset 30, on 12:30", 720, 30, schT(13, 12, 30, 0, 0), schT(14, 0, 30, 0, 0)},
	} {
		for _, paused := range []bool{false, true} { // Next ignores Paused
			s := schedule.Schedule{EveryMinutes: c.every, OffsetMinutes: c.offset, Paused: paused}
			got := s.Next(c.in)
			if !got.Equal(c.want) {
				t.Errorf("%s (paused %v): Next(%s) = %s, want %s", c.label, paused,
					c.in.Format(time.RFC3339Nano), got.Format(time.RFC3339Nano), c.want.Format(time.RFC3339Nano))
			}
			if got.Location() != time.UTC {
				t.Errorf("%s (paused %v): Next is in %s, want UTC", c.label, paused, got.Location())
			}
		}
	}
}

func TestC7_NextUsesUTCGridWhateverZoneItIsAsked(t *testing.T) {
	defer schNoPanic(t)
	ist := time.FixedZone("IST", 5*3600+30*60)
	west := time.FixedZone("PST", -8*3600)
	s := schedule.Schedule{EveryMinutes: 180, OffsetMinutes: 0}
	for _, c := range []struct {
		label string
		in    time.Time
		want  time.Time
	}{
		// 2026-11-13 18:40 UTC. A grid kept in local time would answer 21:30 UTC.
		{"UTC+05:30", time.Date(2026, 11, 14, 0, 10, 0, 0, ist), schT(13, 21, 0, 0, 0)},
		// 2026-11-13 23:59:59 UTC, which is already the 14th in India.
		{"UTC+05:30, a second before UTC midnight", time.Date(2026, 11, 14, 5, 29, 59, 0, ist), schT(14, 0, 0, 0, 0)},
		// 2026-11-14 00:00 UTC, which is still the 13th in the west.
		{"UTC-08:00, on UTC midnight", time.Date(2026, 11, 13, 16, 0, 0, 0, west), schT(14, 3, 0, 0, 0)},
	} {
		got := s.Next(c.in)
		if !got.Equal(c.want) {
			t.Errorf("%s: Next(%s) = %s, want %s", c.label, c.in, got, c.want)
		}
		if got.Location() != time.UTC {
			t.Errorf("%s: Next is in %s, want UTC", c.label, got.Location())
		}
	}
}

func TestC8_IntervalsAreSortedAndDivideADay(t *testing.T) {
	defer schNoPanic(t)
	iv := schedule.Intervals
	if len(iv) == 0 || iv[0] != 15 || iv[len(iv)-1] != 1440 {
		t.Fatalf("Intervals = %v, want it to start at 15 and end at 1440", iv)
	}
	for i, n := range iv {
		if n <= 0 || 1440%n != 0 {
			t.Errorf("Intervals[%d] = %d does not divide 1440", i, n)
		}
		if i > 0 && n <= iv[i-1] {
			t.Errorf("Intervals is not sorted: %d after %d", n, iv[i-1])
		}
	}
	// docs/api.md lists the menu.
	if want := []int{15, 20, 30, 60, 120, 180, 240, 360, 480, 720, 1440}; !slices.Equal(iv, want) {
		t.Errorf("Intervals = %v, want %v", iv, want)
	}
	if !slices.Contains(iv, schedule.DefaultEvery) {
		t.Errorf("DefaultEvery %d is not one of Intervals", schedule.DefaultEvery)
	}
}
