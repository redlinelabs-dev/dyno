// Package schedule defines a scheduled test's settings and its timetable
// (ADR-0028, ADR-0029). It does no I/O: the store keeps schedules, and
// package scheduler runs them.
package schedule

import (
	"errors"
	"fmt"
	"net/netip"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

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
// path, query, fragment or user info. An IPv6 address must be in brackets,
// and a host name is ASCII: other characters go in as punycode. It is
// idempotent: a base URL it returned comes back unchanged.
//
//	"tailmeadow"              -> "http://tailmeadow:3966"
//	"TailMeadow:8080"         -> "http://tailmeadow:8080"
//	"https://dyno.example/"   -> "https://dyno.example"
//	"[fd7a::1]"               -> "http://[fd7a::1]:3966"
func NormalizeFarEnd(in string) (string, error) {
	f, err := parseFarEnd(in)
	if err != nil {
		return "", err
	}
	return f.String(), nil
}

// farEnd is a far end's address in parts.
type farEnd struct {
	scheme string // "http" or "https"
	host   string // lowercase, and without the brackets of an IPv6 address
	port   string // "" when there is none
}

func (f farEnd) String() string {
	host := f.host
	if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	if f.port != "" {
		host += ":" + f.port
	}
	return f.scheme + "://" + host
}

// parseFarEnd is NormalizeFarEnd's work, with the parts kept for Validate.
// The text it returns as errors names the field and never repeats the input,
// which is the client's and can be long.
func parseFarEnd(in string) (farEnd, error) {
	s := strings.TrimSpace(in)
	if s == "" {
		return farEnd{}, errors.New("far_end is empty")
	}
	f := farEnd{scheme: "http"}
	explicitScheme := false
	// url.Parse reads "host:3966" as the scheme "host", so look for "://" itself.
	if i := strings.Index(s, "://"); i >= 0 {
		f.scheme = strings.ToLower(s[:i])
		if f.scheme != "http" && f.scheme != "https" {
			return farEnd{}, errors.New("far_end must be an http or https address")
		}
		s, explicitScheme = s[i+3:], true
	}
	s = strings.TrimSuffix(s, "/")
	switch {
	case s == "":
		return farEnd{}, errors.New("far_end has no host")
	case strings.ContainsAny(s, "/?#"):
		return farEnd{}, errors.New("far_end must not have a path, query or fragment")
	case strings.Contains(s, "@"):
		return farEnd{}, errors.New("far_end must not have a user name or password")
	}

	var port string
	var hasPort bool
	if strings.HasPrefix(s, "[") {
		end := strings.IndexByte(s, ']')
		if end < 0 {
			return farEnd{}, errors.New("far_end has a [ without a ]")
		}
		addr, err := netip.ParseAddr(s[1:end])
		if err != nil || !addr.Is6() || addr.Zone() != "" {
			return farEnd{}, errors.New("far_end must hold an IPv6 address in [brackets]")
		}
		f.host = addr.String()
		if rest := s[end+1:]; rest != "" {
			if port, hasPort = strings.CutPrefix(rest, ":"); !hasPort {
				return farEnd{}, errors.New("far_end has text after the ]")
			}
		}
	} else {
		if strings.Count(s, ":") > 1 {
			return farEnd{}, errors.New("far_end must put an IPv6 address in [brackets]")
		}
		var host string
		host, port, hasPort = strings.Cut(s, ":")
		if !validHost(host) {
			return farEnd{}, errors.New(`far_end's host name can only have letters a to z, digits, "-", "_" and ".", ` +
				"in parts of up to 63 characters; write any other character as punycode")
		}
		f.host = strings.ToLower(host)
	}

	switch {
	case hasPort:
		p, ok := parsePort(port)
		if !ok {
			return farEnd{}, errors.New("far_end has a port that isn't a number from 1 to 65535")
		}
		f.port = p
	case !explicitScheme:
		f.port = DefaultPort
	}
	return f, nil
}

// validHost reports whether h can be a host name: dot-separated labels of
// 1 to 63 ASCII letters, digits, "-" and "_", none starting or ending with
// "-", in at most 253 bytes. Names with other characters go in as punycode.
func validHost(h string) bool {
	if h == "" || len(h) > 253 {
		return false
	}
	for label := range strings.SplitSeq(h, ".") {
		if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for i := 0; i < len(label); i++ {
			c := label[i]
			if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
				return false
			}
		}
	}
	return true
}

// parsePort returns s as a port number from 1 to 65535 without leading
// zeros. s is digits only: no sign, no spaces.
func parsePort(s string) (string, bool) {
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return "", false
		}
	}
	n, err := strconv.Atoi(s)
	if err != nil || n < 1 || n > 65535 {
		return "", false
	}
	return strconv.Itoa(n), true
}

// Validate checks s and fills in what it leaves empty, ignoring ID and the
// times. FarEnd is normalized. Name is trimmed of surrounding spaces and,
// when that leaves it empty, set to the far end's host without port or
// brackets; it must then be at most MaxNameLen bytes of valid UTF-8 with no
// control characters. An empty Kind means Light, and an EveryMinutes of 0
// means DefaultEvery. The error names the field by its JSON name.
func (s *Schedule) Validate() error {
	f, err := parseFarEnd(s.FarEnd)
	if err != nil {
		return err
	}
	name := strings.TrimSpace(s.Name)
	if name == "" {
		// A host can be 253 bytes and a name only 64: cut the default, don't refuse it.
		name = cutUTF8(f.host, MaxNameLen)
	}
	switch {
	case len(name) > MaxNameLen:
		return fmt.Errorf("name must be at most %d bytes", MaxNameLen)
	case !utf8.ValidString(name):
		return errors.New("name must be valid UTF-8")
	case strings.ContainsFunc(name, unicode.IsControl):
		return errors.New("name must not have control characters")
	}
	kind := s.Kind
	if kind == "" {
		kind = Light
	}
	if kind != Light && kind != Full {
		return fmt.Errorf("kind must be %q or %q", Light, Full)
	}
	every := s.EveryMinutes
	if every == 0 {
		every = DefaultEvery
	}
	if !slices.Contains(Intervals, every) {
		return fmt.Errorf("every_minutes must be one of %s", intervalList())
	}
	if s.OffsetMinutes < 0 || s.OffsetMinutes >= every {
		return fmt.Errorf("offset_minutes must be from 0 to %d, less than every_minutes", every-1)
	}
	s.FarEnd, s.Name, s.Kind, s.EveryMinutes = f.String(), name, kind, every
	return nil
}

// intervalList is the menu of Intervals as text, for an error.
func intervalList() string {
	parts := make([]string, len(Intervals))
	for i, n := range Intervals {
		parts[i] = strconv.Itoa(n)
	}
	return strings.Join(parts, ", ")
}

// cutUTF8 returns s cut to at most n bytes, without splitting a character.
func cutUTF8(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

// Apply returns s with p's non-nil fields copied in. It doesn't validate.
func (s Schedule) Apply(p Patch) Schedule {
	if p.Name != nil {
		s.Name = *p.Name
	}
	if p.FarEnd != nil {
		s.FarEnd = *p.FarEnd
	}
	if p.Kind != nil {
		s.Kind = *p.Kind
	}
	if p.EveryMinutes != nil {
		s.EveryMinutes = *p.EveryMinutes
	}
	if p.OffsetMinutes != nil {
		s.OffsetMinutes = *p.OffsetMinutes
	}
	if p.Paused != nil {
		s.Paused = *p.Paused
	}
	return s
}

// Next returns the first time on s's grid strictly after t: midnight UTC of
// some day, plus OffsetMinutes, plus a whole number of EveryMinutes. The
// result is in UTC. It ignores Paused. A schedule that fails Validate, such as
// a row edited by hand, gets the default interval or a zero offset instead of
// a panic, so the answer is always after t.
func (s Schedule) Next(t time.Time) time.Time {
	every := s.EveryMinutes
	if every <= 0 || every > 1440 {
		every = DefaultEvery
	}
	offset := s.OffsetMinutes
	if offset < 0 || offset >= every {
		offset = 0
	}
	t = t.UTC()
	day := time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
	since := t.Sub(day)
	start, step := time.Duration(offset)*time.Minute, time.Duration(every)*time.Minute
	if since < start {
		return day.Add(start)
	}
	return day.Add(start + (since-start)/step*step + step)
}
