package schedule_test

import (
	"strings"
	"testing"
	"time"

	"github.com/redlinelabs-dev/dyno/server/internal/schedule"
)

// Builder's own cases, beside the spec cases in schedule_test.go.

func TestCx_NormalizeFarEndMoreAddressesItAccepts(t *testing.T) {
	defer schNoPanic(t)
	for _, c := range []struct{ in, want string }{
		{"tailmeadow/", "http://tailmeadow:3966"},
		{"http://tailmeadow:3966/", "http://tailmeadow:3966"},
		{"[fd7a:0:0:0:0:0:0:1]", "http://[fd7a::1]:3966"}, // one address, one spelling
		{"[::ffff:1.2.3.4]", "http://[::ffff:1.2.3.4]:3966"},
		{"my_host.local", "http://my_host.local:3966"},
		{"Dyno.Home.Arpa:00443", "http://dyno.home.arpa:443"},
		{"https://dyno.example:443", "https://dyno.example:443"}, // a port typed stays
	} {
		got, err := schedule.NormalizeFarEnd(c.in)
		if err != nil || got != c.want {
			t.Errorf("NormalizeFarEnd(%q) = %q, err %v; want %q", c.in, got, err, c.want)
		}
	}
}

func TestCx_NormalizeFarEndMoreAddressesItRefuses(t *testing.T) {
	defer schNoPanic(t)
	for _, in := range []string{
		"h:", "http://h//", "[::1", "[::1]x", "[::1]:", "[1.2.3.4]", "[fe80::1%25eth0]", "[]",
		"a..b", "-a", "a-", ".a", "a.", strings.Repeat("a", 64), strings.Repeat("a.", 130) + "a",
		"münchen.example", "h\x00", "h h", "http://@h", "h:+80", "h:-1", "h: 80", "h:99999999999999999999",
		"http://h:80:80", "https:/h", "http:h", "http:///", "/",
	} {
		if got, err := schedule.NormalizeFarEnd(in); err == nil {
			t.Errorf("NormalizeFarEnd(%q) = %q, want an error", in, got)
		}
	}
}

func TestCx_NormalizeFarEndErrorsNameTheFieldAndDontEchoTheInput(t *testing.T) {
	defer schNoPanic(t)
	_, err := schedule.NormalizeFarEnd("ftp://" + strings.Repeat("x", 3000))
	if err == nil {
		t.Fatal("want an error")
	}
	if !strings.Contains(err.Error(), "far_end") || len(err.Error()) > 200 || strings.Contains(err.Error(), "xxx") {
		t.Errorf("error %q should name far_end and be short, without the input", err)
	}
}

func TestCx_ValidateCutsALongDefaultName(t *testing.T) {
	defer schNoPanic(t)
	label := strings.Repeat("a", 63)
	s := schedule.Schedule{FarEnd: label + "." + label + "." + label} // a host of 191 bytes
	if err := s.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if len(s.Name) != schedule.MaxNameLen || !strings.HasPrefix(s.FarEnd, "http://"+s.Name) {
		t.Errorf("name = %q, want the host cut to %d bytes", s.Name, schedule.MaxNameLen)
	}
}

func TestCx_ValidateLeavesTheScheduleAloneWhenItFails(t *testing.T) {
	defer schNoPanic(t)
	in := schedule.Schedule{Name: "  Gran's  ", FarEnd: " TailMeadow ", EveryMinutes: 45}
	s := in
	if err := s.Validate(); err == nil {
		t.Fatal("want an error for every_minutes 45")
	}
	if s != in {
		t.Errorf("a failed Validate changed the schedule:\n in %+v\ngot %+v", in, s)
	}
}

func TestCx_NextIsAfterTAndOnTheGrid(t *testing.T) {
	defer schNoPanic(t)
	times := []time.Time{
		schT(13, 0, 0, 0, 0), schT(13, 0, 0, 0, 1), schT(13, 7, 7, 7, 7),
		schT(13, 23, 59, 59, 999_999_999), schT(14, 0, 0, 0, 0),
		time.Date(2027, 2, 28, 23, 59, 59, 0, time.UTC),
	}
	for _, every := range schedule.Intervals {
		step := time.Duration(every) * time.Minute
		for _, offset := range []int{0, 1, every - 1} {
			s := schedule.Schedule{EveryMinutes: every, OffsetMinutes: offset}
			for _, in := range times {
				got := s.Next(in)
				if !got.After(in) || got.Sub(in) > step {
					t.Errorf("every %d offset %d: Next(%s) = %s, want a time after it and at most %v later",
						every, offset, in.Format(time.RFC3339Nano), got.Format(time.RFC3339Nano), step)
					continue
				}
				midnight := time.Date(got.Year(), got.Month(), got.Day(), 0, 0, 0, 0, time.UTC)
				if (got.Sub(midnight)-time.Duration(offset)*time.Minute)%step != 0 {
					t.Errorf("every %d offset %d: Next(%s) = %s is off the grid",
						every, offset, in.Format(time.RFC3339Nano), got.Format(time.RFC3339Nano))
				}
			}
		}
	}
}

func TestCx_NextOfAScheduleThatFailsValidateIsStillAfterT(t *testing.T) {
	defer schNoPanic(t)
	in := schT(13, 10, 0, 0, 0)
	for _, s := range []schedule.Schedule{
		{},
		{EveryMinutes: -5},
		{EveryMinutes: 1 << 30},
		{EveryMinutes: 60, OffsetMinutes: 60},
		{EveryMinutes: 60, OffsetMinutes: -3},
	} {
		got := s.Next(in)
		if !got.After(in) || got.Sub(in) > 24*time.Hour {
			t.Errorf("Next of %+v = %s, want a time after %s and within a day", s, got, in)
		}
	}
}
