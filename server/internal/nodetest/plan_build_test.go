package nodetest

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"net/url"
	"strings"
	"testing"
	"time"
)

// An address with a password in it is refused, and the message must not
// repeat the password: it is stored and shown.
func TestNx_ParseFarEndRefusesUserInfoWithoutEchoingIt(t *testing.T) {
	for _, s := range []string{"http://admin:hunter2@tailmeadow:3966", "user:hunter2@tailmeadow:3966", "hunter2@tailmeadow"} {
		_, err := parseFarEnd(s)
		var ne *Error
		if !errors.As(err, &ne) || ne.Code != CodeBadFarEnd {
			t.Errorf("parseFarEnd(%q) = %v, want a bad_far_end *Error", s, err)
			continue
		}
		if strings.Contains(err.Error(), "hunter2") {
			t.Errorf("parseFarEnd(%q) says %q, which repeats the password", s, err.Error())
		}
	}
}

func TestNx_ParseFarEndKeepsAPathAndDropsQueryAndFragment(t *testing.T) {
	u, err := parseFarEnd("https://Tailmeadow.example:8443/dyno/?x=1#top")
	if err != nil {
		t.Fatal(err)
	}
	if got, want := u.String(), "https://Tailmeadow.example:8443/dyno/"; got != want {
		t.Errorf("parseFarEnd = %q, want %q", got, want)
	}
}

// A far end can't send the run to another machine by naming one in its plan.
func TestNx_EndpointsStayOnTheFarEnd(t *testing.T) {
	base, err := url.Parse("http://tailmeadow:3966")
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		ref  string
		want string // "" when the endpoint must be refused
	}{
		{"/dl", "http://tailmeadow:3966/dl"},
		{"dl", "http://tailmeadow:3966/dl"},
		{"/x/dl?bytes=1#f", "http://tailmeadow:3966/x/dl"},
		{"http://tailmeadow:3966/dl", "http://tailmeadow:3966/dl"},
		{"http://elsewhere:3966/dl", ""},
		{"http://tailmeadow:3967/dl", ""},
		{"https://tailmeadow:3966/dl", ""},
		{"//elsewhere/dl", ""},
		{"http://user@tailmeadow:3966/dl", ""},
		{"mailto:someone@example.com", ""},
	} {
		got, ok := resolveEndpoint(base, c.ref)
		switch {
		case c.want == "" && ok:
			t.Errorf("resolveEndpoint(%q) = %v, want it refused", c.ref, got)
		case c.want != "" && !ok:
			t.Errorf("resolveEndpoint(%q) refused, want %q", c.ref, c.want)
		case ok && got.String() != c.want:
			t.Errorf("resolveEndpoint(%q) = %q, want %q", c.ref, got, c.want)
		}
	}
}

func TestNx_ClampDirection(t *testing.T) {
	const gib = 1 << 30

	high := clampDirection(wireTransfer{Streams: 64, DurationMs: 600_000, WarmupMs: 600_000, StartBytes: 1e12, MaxBytes: 1e13, TargetRequestMs: 1e9}, gib)
	wantHigh := dirPlan{streams: MaxStreams, duration: 30 * time.Second, warmup: 12 * time.Second, startBytes: gib, maxBytes: gib, target: 10 * time.Second}
	if high != wantHigh {
		t.Errorf("clamped down: %+v, want %+v", high, wantHigh)
	}

	low := clampDirection(wireTransfer{}, gib)
	wantLow := dirPlan{streams: 1, duration: 500 * time.Millisecond, startBytes: 1 << 10, maxBytes: 64 << 10, target: 100 * time.Millisecond}
	if low != wantLow {
		t.Errorf("clamped up: %+v, want %+v", low, wantLow)
	}

	fraction := clampDirection(wireTransfer{Streams: 2.9, DurationMs: 1234.7, WarmupMs: 100.2, StartBytes: 5000.5, MaxBytes: 70000.9, TargetRequestMs: 250.5}, gib)
	wantFraction := dirPlan{streams: 2, duration: 1234 * time.Millisecond, warmup: 100 * time.Millisecond, startBytes: 5000, maxBytes: 70000, target: 250 * time.Millisecond}
	if fraction != wantFraction {
		t.Errorf("fractions: %+v, want %+v", fraction, wantFraction)
	}
}

func TestNx_LightenCapsEachDirectionAndSetsItsWarmUp(t *testing.T) {
	p := &farPlan{}
	p.down.duration, p.down.warmup = 20*time.Second, 8*time.Second
	p.up.duration, p.up.warmup = 3*time.Second, 100*time.Millisecond
	p.lighten()
	if p.down.duration != LightDuration || p.down.warmup != LightWarmup {
		t.Errorf("down is %v with %v of warm-up, want %v with %v", p.down.duration, p.down.warmup, LightDuration, LightWarmup)
	}
	if p.up.duration != 3*time.Second || p.up.warmup != LightWarmup {
		t.Errorf("up is %v with %v of warm-up, want 3s with %v", p.up.duration, p.up.warmup, LightWarmup)
	}
}

func TestNx_BudgetNeverGivesOutMoreThanItHolds(t *testing.T) {
	b := newBudget(10)
	for _, c := range []struct{ want, got int64 }{{4, 4}, {100, 6}, {1, 0}} {
		if got := b.take(c.want); got != c.got {
			t.Errorf("take(%d) = %d, want %d", c.want, got, c.got)
		}
	}
	var none *budget
	if got := none.take(123); got != 123 {
		t.Errorf("a nil budget took %d of 123, want all of it", got)
	}
}

func TestNx_PayloadSendsExactlyTheBytesLeftAndRepeatsItsBlock(t *testing.T) {
	var counted int
	p := &payload{block: []byte{1, 2, 3, 4, 5}, left: 12, count: func(n int) { counted += n }}
	got, err := io.ReadAll(p)
	if err != nil {
		t.Fatal(err)
	}
	if want := []byte{1, 2, 3, 4, 5, 1, 2, 3, 4, 5, 1, 2}; !bytes.Equal(got, want) {
		t.Errorf("body = %v, want %v", got, want)
	}
	if counted != 12 {
		t.Errorf("counted %d bytes, want 12", counted)
	}
}

func TestNx_AppendCappedNeverGrowsASlice(t *testing.T) {
	s := make([]int, 0, 2)
	for i := 1; i <= 3; i++ {
		s = appendCapped(s, i)
	}
	if len(s) != 2 || cap(s) != 2 || s[0] != 1 || s[1] != 2 {
		t.Errorf("s = %v with cap %d, want [1 2] with cap 2", s, cap(s))
	}
}

func TestNx_ErrorMessagesNameWhatWasSeen(t *testing.T) {
	root := errors.New("boom")
	cases := []struct {
		err  *Error
		want string
	}{
		{newError(CodeUnreachable, "", "tailmeadow:3966", "connection refused", nil), "couldn't reach tailmeadow:3966: connection refused"},
		{newError(CodeStream, PhaseDownload, "tailmeadow:3966", "unexpected EOF", nil), "download: the far end closed the connection part way: unexpected EOF"},
		{newError(CodeHTTP, PhaseUpload, "tailmeadow:3966", statusText(502), nil), "upload: the far end answered with an error: 502 Bad Gateway"},
		{newError(CodeCanceled, PhaseWarm, "tailmeadow:3966", "", nil), "warm-up: the test was canceled"},
	}
	for _, c := range cases {
		if got := c.err.Error(); got != c.want {
			t.Errorf("Error() = %q, want %q", got, c.want)
		}
	}
	if e := newError(CodeStream, PhaseUpload, "h:1", "why", root); !errors.Is(e, root) {
		t.Error("errors.Is can't reach the error underneath a run's *Error")
	}
	// What a far end chose to say is cut to a line of printable text.
	msg := newError(CodeNotDyno, "", "h:1", "line one\nline two "+strings.Repeat("x", 500), nil).Error()
	if strings.ContainsAny(msg, "\r\n") || len(msg) > 300 {
		t.Errorf("message is %d bytes and has a line break: %q", len(msg), msg)
	}
}

func TestNx_ClassifyReadsErrorsByWhatTheyMean(t *testing.T) {
	live := context.Background()
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	expired, stop := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer stop()
	refused := &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("connection refused")}
	for _, c := range []struct {
		name  string
		ctx   context.Context
		phase Phase
		err   error
		code  string
	}{
		{"refused", live, "", refused, CodeUnreachable},
		{"no such host", live, "", &net.DNSError{Err: "no such host", Name: "x.invalid"}, CodeUnreachable},
		{"cut off mid-download", live, PhaseDownload, io.ErrUnexpectedEOF, CodeStream},
		{"cut off mid-upload", live, PhaseUpload, io.ErrUnexpectedEOF, CodeStream},
		{"cut off between pings", live, PhaseLatency, io.EOF, CodeUnreachable},
		{"a request's own limit", live, PhaseLatency, context.DeadlineExceeded, CodeTimeout},
		{"canceled", canceled, PhaseUpload, errors.New("whatever"), CodeCanceled},
		{"out of time", expired, PhaseDownload, errors.New("whatever"), CodeTimeout},
	} {
		t.Run(c.name, func(t *testing.T) {
			got := classify(c.ctx, "h:1", c.phase, c.err)
			if got.Code != c.code || got.Phase != c.phase {
				t.Errorf("classify = code %q in phase %q (%v), want code %q in phase %q", got.Code, got.Phase, got, c.code, c.phase)
			}
		})
	}
}

func TestNx_StatusErrorsSeparateRedirectsFromErrors(t *testing.T) {
	if e := statusError(PhaseDownload, "h:1", 301); e.Code != CodeNotDyno || e.Phase != PhaseDownload {
		t.Errorf("a 301 is code %q in phase %q, want not_dyno in download", e.Code, e.Phase)
	}
	e := statusError(PhaseUpload, "h:1", 502)
	if e.Code != CodeHTTP || !strings.Contains(e.Error(), "502 Bad Gateway") {
		t.Errorf("a 502 is code %q and reads %q, want http_status naming 502 Bad Gateway", e.Code, e.Error())
	}
}
