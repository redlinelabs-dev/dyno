package nodetest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// serveBanner answers every connection with an SSH banner.
func serveBanner(t *testing.T) string {
	t.Helper()
	return serveRaw(t, "SSH-2.0-OpenSSH_9.6\r\n")
}

// serveRaw listens on a loopback port and answers every connection with reply
// as it is, after reading the request so closing doesn't reset the connection
// and eat the reply. It returns the address.
func serveRaw(t *testing.T, reply string) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			conn.SetDeadline(time.Now().Add(5 * time.Second))
			buf := make([]byte, 4096)
			conn.Read(buf)
			conn.Write([]byte(reply))
			conn.Close()
		}
	}()
	t.Cleanup(func() {
		ln.Close()
		wg.Wait()
	})
	return ln.Addr().String()
}

// An SSH server on the far end's port answered, so it isn't "no connection":
// it is something that isn't a dyno, and what it said isn't repeated.
func TestNx_ANonHTTPReplyIsNotDyno(t *testing.T) {
	addr := serveBanner(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	o := Options{FarEnd: "http://" + addr, Warm: -1}

	_, err := Check(ctx, o)
	var ne *Error
	if !errors.As(err, &ne) || ne.Code != CodeNotDyno || ne.Phase != "" {
		t.Errorf("Check = %v, want a not_dyno *Error with no phase", err)
	} else if strings.Contains(err.Error(), "OpenSSH") {
		t.Errorf("Check says %q, which repeats what the far end sent", err.Error())
	}

	_, err = Run(ctx, o)
	if !errors.As(err, &ne) || ne.Code != CodeNotDyno || ne.Phase != "" {
		t.Errorf("Run = %v, want a not_dyno *Error with no phase", err)
	}
}

// A reply that isn't HTTP is not_dyno in whatever phase it came, while a
// connection that closed before any reply is still unreachable.
func TestNx_ClassifyNonHTTPRepliesInAnyPhase(t *testing.T) {
	live := context.Background()
	malformed := fmt.Errorf("Get %q: %w", "http://h:1/x", fmt.Errorf("net/http: HTTP/1.x transport connection broken: %w", errors.New(`malformed HTTP response "SSH-2.0-OpenSSH_9.6"`)))
	for _, p := range []Phase{"", PhaseWarm, PhaseLatency, PhaseDownload, PhaseUpload} {
		t.Run(string(p), func(t *testing.T) {
			got := classify(live, "h:1", p, malformed)
			if got.Code != CodeNotDyno || got.Phase != p {
				t.Errorf("classify = code %q in phase %q, want not_dyno in %q", got.Code, got.Phase, p)
			}
			if strings.Contains(got.Error(), "OpenSSH") {
				t.Errorf("message %q repeats what the far end sent", got.Error())
			}
		})
	}
	if got := classify(live, "h:1", PhaseLatency, errors.New("EOF")); got.Code == CodeNotDyno {
		t.Errorf("an error that doesn't say malformed HTTP was read as not_dyno")
	}
}

// timeoutErr is a dial that gave up, as a net.Error that says so.
type timeoutErr struct{}

func (timeoutErr) Error() string   { return "i/o timeout" }
func (timeoutErr) Timeout() bool   { return true }
func (timeoutErr) Temporary() bool { return true }

// docs/api.md: unreachable is "no connection" and timeout is "connected, but a
// reply didn't come in time". A dial that ran out of time never connected.
func TestNx_ADialThatTimedOutIsUnreachable(t *testing.T) {
	err := &net.OpError{Op: "dial", Net: "tcp", Err: timeoutErr{}}
	for _, p := range []Phase{"", PhaseWarm, PhaseDownload} {
		if got := classify(context.Background(), "h:1", p, err); got.Code != CodeUnreachable {
			t.Errorf("phase %q: a dial that timed out is %q, want unreachable", p, got.Code)
		}
	}
}

// The dial's limit has to be under every other 10 s timer on the way to a
// far end's first answer, or which one fires first, and so which code a black
// hole gets, is a race.
func TestNx_TheDialTimeoutBeatsThePlanAndPingTimeouts(t *testing.T) {
	dial, plan, ping := dialTimeout, planTimeout, DefaultPingTimeout
	if dial >= plan || dial >= ping {
		t.Errorf("dialTimeout %v, planTimeout %v, DefaultPingTimeout %v: the dial must be the shortest", dial, plan, ping)
	}
}

// An address whose connection never completes fails as the dial, unreachable,
// when the dial's limit passes, with no real unroutable address: the name's
// lookup is the thing that hangs, under the same Dialer.Timeout.
func TestNx_ADialThatNeverCompletesIsUnreachable(t *testing.T) {
	tune := func(d *net.Dialer) {
		d.Timeout = 150 * time.Millisecond
		d.Resolver = &net.Resolver{
			PreferGo: true,
			Dial: func(ctx context.Context, network, addr string) (net.Conn, error) {
				<-ctx.Done()
				return nil, ctx.Err()
			},
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	o := Options{FarEnd: "http://blackhole.dyno-nodetest.invalid:3966", Warm: -1, tune: tune}

	for name, call := range map[string]func() error{
		"Check": func() error { _, err := Check(ctx, o); return err },
		"Run":   func() error { _, err := Run(ctx, o); return err },
	} {
		t.Run(name, func(t *testing.T) {
			began := time.Now()
			err := call()
			var ne *Error
			if !errors.As(err, &ne) || ne.Code != CodeUnreachable || ne.Phase != "" {
				t.Fatalf("%s = %v, want an unreachable *Error with no phase", name, err)
			}
			if took := time.Since(began); took > 5*time.Second {
				t.Errorf("took %v to give up on a dial limited to 150 ms", took)
			}
		})
	}
}

// probeFarEnd is a far end whose transfers are healthy and whose /ping
// answers its first answers requests and then fails: with a 500, or by closing
// the connection. The plan's idle latency takes 6 pings.
func probeFarEnd(t *testing.T, answers int64, failure string) string {
	t.Helper()
	var pings atomic.Int64
	zeros := make([]byte, 64<<10)
	tr := wireTransfer{Streams: 1, DurationMs: 600, WarmupMs: 150, StartBytes: 64 << 10, MaxBytes: 1 << 20, TargetRequestMs: 100}
	plan := wirePlan{
		TestID:    "3f9c2a7e01b44d6a9e5c8b12",
		Server:    wireServer{Name: "fakedyno", Version: "v9.9.9"},
		Endpoints: wireEndpoints{Download: "/dl", Upload: "/ul", Ping: "/ping"},
		Latency:   wireLatency{IdleSamples: 5, LoadedIntervalMs: 50},
		Download:  tr,
		Upload:    tr,
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/plan", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(plan)
	})
	mux.HandleFunc("/dl", func(w http.ResponseWriter, r *http.Request) {
		size, _ := strconv.ParseInt(r.URL.Query().Get("bytes"), 10, 64)
		w.Header().Set("Content-Length", strconv.FormatInt(size, 10))
		for size > 0 {
			n := min(size, int64(len(zeros)))
			if _, err := w.Write(zeros[:n]); err != nil {
				return
			}
			size -= n
		}
	})
	mux.HandleFunc("/ul", func(w http.ResponseWriter, r *http.Request) {
		n, _ := io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"bytes":%d}`, n)
	})
	mux.HandleFunc("/ping", func(w http.ResponseWriter, r *http.Request) {
		if pings.Add(1) <= 6+answers {
			time.Sleep(2 * time.Millisecond)
			return
		}
		if failure == "closes" {
			conn, _, err := w.(http.Hijacker).Hijack()
			if err == nil {
				conn.Close()
			}
			return
		}
		http.Error(w, "no", http.StatusInternalServerError)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv.URL
}

// ADR-0032: a loaded probe that fails ends the probing for its direction and
// nothing else. The run passes with its speeds; the loaded latency is the
// median of the probes that finished after warm-up, and 0 with none.
func TestNx_AFailedLoadedProbeEndsTheProbingNotTheRun(t *testing.T) {
	for _, c := range []struct {
		name, failure string
		answers       int64
		wantDown      bool // some probes finished after the 150 ms warm-up
	}{
		{"500 on every loaded probe", "500", 0, false},
		{"closed on every loaded probe", "closes", 0, false},
		{"500 after eight answers", "500", 8, true},
		{"closed after eight answers", "closes", 8, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			url := probeFarEnd(t, c.answers, c.failure)
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			out, err := Run(ctx, Options{FarEnd: url, Warm: -1})
			if err != nil {
				t.Fatalf("Run = %v, want a result: a lost probe must not fail the test", err)
			}
			sub := out.Submission
			if sub.DownloadMbps <= 0 || sub.UploadMbps <= 0 {
				t.Errorf("speeds %v down, %v up, want both measured", sub.DownloadMbps, sub.UploadMbps)
			}
			if c.wantDown && sub.LoadedDownMs <= 0 {
				t.Errorf("LoadedDownMs = %v, want the median of the probes that finished after warm-up", sub.LoadedDownMs)
			}
			if !c.wantDown && sub.LoadedDownMs != 0 {
				t.Errorf("LoadedDownMs = %v with every probe lost before warm-up ended, want 0", sub.LoadedDownMs)
			}
			if sub.LoadedUpMs != 0 {
				t.Errorf("LoadedUpMs = %v with every upload probe lost, want 0", sub.LoadedUpMs)
			}
		})
	}
}

// An answer that starts like HTTP but breaks the protocol further in is still
// an answer: not_dyno, never "no connection", and the bytes the far end chose
// stay out of the message, which is stored and shown.
func TestNx_AMalformedHTTPReplyIsNotDynoAndNotRepeated(t *testing.T) {
	const mark = "ZZMARK"
	for _, c := range []struct{ name, reply string }{
		{"a header line with a control byte", "HTTP/1.1 302 Found\r\nLocation: http://evil.example/\x1b" + mark + strings.Repeat("Z", 300) + "\r\n\r\n"},
		{"a header line with no colon", "HTTP/1.1 200 OK\r\n" + mark + "\r\n\r\n"},
		{"a header name with a space", "HTTP/1.1 200 OK\r\nBad Name" + mark + ": x\r\n\r\n"},
		{"a header block starting with a space", "HTTP/1.1 200 OK\r\n " + mark + ": x\r\n\r\n"},
		{"a status line with a short code", "HTTP/1.1 20 " + mark + "\r\n\r\n"},
		{"a status line with a code that isn't a number", "HTTP/1.1 2x0 " + mark + "\r\n\r\n"},
		{"a version that isn't HTTP/1.x", "HTTP/9.9 200 OK " + mark + "\r\n\r\n"},
		{"a Content-Length that isn't a number", "HTTP/1.1 200 OK\r\nContent-Length: " + mark + "\r\n\r\n"},
		{"two Content-Lengths that differ", "HTTP/1.1 200 OK\r\nContent-Length: 5\r\nContent-Length: 6" + "\r\n\r\n"},
		{"a transfer encoding it can't read", "HTTP/1.1 200 OK\r\nTransfer-Encoding: " + mark + "\r\n\r\n"},
		{"headers past the limit", "HTTP/1.1 200 OK\r\nX-" + mark + ": " + strings.Repeat("a", 80<<10) + "\r\n\r\n"},
	} {
		t.Run(c.name, func(t *testing.T) {
			addr := serveRaw(t, c.reply)
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			o := Options{FarEnd: "http://" + addr, Warm: -1}
			check := func() error { _, err := Check(ctx, o); return err }
			run := func() error { _, err := Run(ctx, o); return err }
			for name, call := range map[string]func() error{"Check": check, "Run": run} {
				err := call()
				var ne *Error
				if !errors.As(err, &ne) || ne.Code != CodeNotDyno || ne.Phase != "" {
					t.Errorf("%s = %v, want a not_dyno *Error with no phase", name, err)
					continue
				}
				if msg := err.Error(); strings.Contains(msg, mark) || strings.Contains(msg, "evil.example") || strings.Contains(msg, "\x1b") {
					t.Errorf("%s says %q, which repeats what the far end sent", name, msg)
				}
			}
		})
	}
}

// brokenPlanJSON is a plan that is whole and valid, with a marker in the one
// field a far end chooses freely, so a message that repeats it shows.
func brokenPlanJSON(mark string) string {
	tr := wireTransfer{Streams: 1, DurationMs: 600, WarmupMs: 150, StartBytes: 64 << 10, MaxBytes: 1 << 20, TargetRequestMs: 100}
	b, err := json.Marshal(wirePlan{
		TestID:    "3f9c2a7e01b44d6a9e5c8b12",
		Server:    wireServer{Name: mark, Version: "v9.9.9"},
		Endpoints: wireEndpoints{Download: "/dl", Upload: "/ul", Ping: "/ping"},
		Latency:   wireLatency{IdleSamples: 5, LoadedIntervalMs: 50},
		Download:  tr,
		Upload:    tr,
	})
	if err != nil {
		panic(err)
	}
	return string(b)
}

// A plan reply whose head came (a 2xx) and whose body then broke off is an
// answer that isn't a dyno's plan: not_dyno, in a message of ours. docs/api.md:
// unreachable means no connection, and here something answered.
func TestNx_APlanBodyThatBreaksOffIsNotDynoNotUnreachable(t *testing.T) {
	const mark = "ZZMARK"
	plan := brokenPlanJSON(mark)
	chunked := "HTTP/1.1 200 OK\r\nContent-Type: application/json\r\nTransfer-Encoding: chunked\r\n\r\n"
	whole := fmt.Sprintf("%x\r\n%s\r\n", len(plan), plan)
	for _, c := range []struct{ name, reply string }{
		{"chunked, cut short after the plan's chunk", chunked + whole},
		{"chunked, cut short inside the plan's chunk", chunked + fmt.Sprintf("%x\r\n%s", len(plan), plan[:len(plan)/2])},
		{"chunked, a bad chunk size after the plan", chunked + whole + mark + "\r\n"},
		{"chunked, a bad chunk size before the plan", chunked + mark + "\r\n" + whole},
		{"a Content-Length longer than the body, then a close",
			"HTTP/1.1 200 OK\r\nContent-Type: application/json\r\nContent-Length: " + strconv.Itoa(len(plan)+500) + "\r\n\r\n" + plan},
	} {
		t.Run(c.name, func(t *testing.T) {
			addr := serveRaw(t, c.reply)
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			o := Options{FarEnd: "http://" + addr, Warm: -1}
			check := func() error { _, err := Check(ctx, o); return err }
			run := func() error { _, err := Run(ctx, o); return err }
			for name, call := range map[string]func() error{"Check": check, "Run": run} {
				err := call()
				var ne *Error
				if !errors.As(err, &ne) || ne.Code != CodeNotDyno || ne.Phase != "" {
					t.Errorf("%s = %v, want a not_dyno *Error with no phase", name, err)
					continue
				}
				msg := err.Error()
				if strings.Contains(msg, mark) || strings.Contains(msg, "couldn't reach") || strings.Contains(msg, "chunk") || strings.Contains(msg, "EOF") {
					t.Errorf("%s says %q, which isn't a message of our own about a reply that broke off", name, msg)
				}
			}
		})
	}
}

// serveThenHold answers every connection with head and then says nothing more
// until the test ends, so a body that was promised never comes.
func serveThenHold(t *testing.T, head string) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			wg.Add(1)
			go func() {
				defer wg.Done()
				defer conn.Close()
				buf := make([]byte, 4096)
				conn.Read(buf)
				conn.Write([]byte(head))
				<-done
			}()
		}
	}()
	t.Cleanup(func() {
		close(done)
		ln.Close()
		wg.Wait()
	})
	return ln.Addr().String()
}

// Silence after the plan's head is still a timeout, not a broken-off reply:
// the time ran out, and classify's order (canceled, then timeout) is kept.
// The deadline here is the caller's 1 s, well under planTimeout, which is a
// constant and would make this test wait 10 s.
func TestNx_APlanBodyThatNeverComesStillTimesOut(t *testing.T) {
	addr := serveThenHold(t, "HTTP/1.1 200 OK\r\nContent-Type: application/json\r\nContent-Length: 600\r\n\r\n{")
	o := Options{FarEnd: "http://" + addr, Warm: -1}
	for name, call := range map[string]func(context.Context) error{
		"Check": func(ctx context.Context) error { _, err := Check(ctx, o); return err },
		"Run":   func(ctx context.Context) error { _, err := Run(ctx, o); return err },
	} {
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			began := time.Now()
			err := call(ctx)
			var ne *Error
			if !errors.As(err, &ne) || ne.Code != CodeTimeout || ne.Phase != "" {
				t.Fatalf("%s = %v, want a timeout *Error with no phase", name, err)
			}
			if took := time.Since(began); took < 900*time.Millisecond || took > 3*time.Second {
				t.Errorf("returned after %v, want about the 1 s deadline", took)
			}
		})
	}
}

// A canceled ctx while the plan's body is awaited is still canceled.
func TestNx_APlanBodyCanceledMidReadStaysCanceled(t *testing.T) {
	addr := serveThenHold(t, "HTTP/1.1 200 OK\r\nContent-Type: application/json\r\nContent-Length: 600\r\n\r\n{")
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(300*time.Millisecond, cancel)
	defer cancel()
	_, err := Check(ctx, Options{FarEnd: "http://" + addr, Warm: -1})
	var ne *Error
	if !errors.As(err, &ne) || ne.Code != CodeCanceled || ne.Phase != "" {
		t.Fatalf("Check = %v, want a canceled *Error with no phase", err)
	}
}
