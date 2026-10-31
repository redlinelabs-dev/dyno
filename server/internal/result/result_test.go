package result

import (
	"math"
	"strings"
	"testing"
)

func TestGradeThresholds(t *testing.T) {
	for _, c := range []struct {
		ms   float64
		want string
	}{{0, "A+"}, {9.9, "A+"}, {10, "A"}, {19.9, "A"}, {20, "B"}, {49, "B"}, {50, "C"}, {99, "C"}, {100, "D"}, {499, "D"}, {500, "F"}} {
		if g := Grade(c.ms); g != c.want {
			t.Errorf("Grade(%v) = %s, want %s", c.ms, g, c.want)
		}
	}
}

func TestDeriveUsesWorseDirection(t *testing.T) {
	r := Result{Submission: Submission{IdleMs: 8, LoadedDownMs: 20, LoadedUpMs: 68}}
	r.Derive()
	if r.BufferbloatMs != 60 || r.Grade != "C" || r.RPM != 882 {
		t.Fatalf("got %+v", r)
	}
}

func TestDeriveWithoutLoadedLatency(t *testing.T) {
	r := Result{Submission: Submission{IdleMs: 8}}
	r.Derive()
	if r.Grade != "" || r.RPM != 0 {
		t.Fatalf("graded a test with no loaded latency: %+v", r)
	}
}

func TestValidate(t *testing.T) {
	for _, bad := range []Submission{{DownloadMbps: -1}, {IdleMs: math.NaN()}, {UploadMbps: math.Inf(1)}, {Streams: 99}, {Device: "a b"}, {Device: strings.Repeat("x", 65)}} {
		if bad.Validate() == nil {
			t.Errorf("accepted %+v", bad)
		}
	}
	long := Submission{Client: string(make([]byte, 500))}
	if err := long.Validate(); err != nil || len(long.Client) != maxClientLen {
		t.Errorf("client label not trimmed: len %d, err %v", len(long.Client), err)
	}
}
