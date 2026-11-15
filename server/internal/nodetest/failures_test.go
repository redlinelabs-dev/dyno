package nodetest_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"runtime"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/redlinelabs-dev/dyno/server/internal/nodetest"
)

func TestN25_ADownloadThatBreaksOffMidBodyFailsTheRun(t *testing.T) {
	p := shortPlan()
	p.Download.StartBytes = 1 << 20
	fe := newFarEnd(t, p)
	fe.DLHook = func(w http.ResponseWriter, r *http.Request, n int) bool {
		if n != 0 {
			return false
		}
		breakAfter(w, 1<<20, 256<<10) // promises 1 MiB, hijacks the connection and closes it after 256 KiB
		return true
	}
	fe.start()
	res := fe.runFull(t)
	ne := wantFail(t, res, nodetest.CodeStream, nodetest.PhaseDownload)
	if msg := message(t, ne); !strings.Contains(msg, "download") {
		t.Errorf("Error() = %q, want it to name the download", msg)
	}
	if n := len(on(fe.recs(), "/ul")); n != 0 {
		t.Errorf("%d /ul requests after a failed download", n)
	}
}

// upPlan is shortPlan with the shortest download a plan may ask for, for the
// tests that fail in the upload and so wait out the download first.
func upPlan() planDoc {
	p := shortPlan()
	p.Download.DurationMs = 500
	return p
}

func TestN26_AnUploadThatAnswersWithAnErrorStatusFailsTheRun(t *testing.T) {
	for _, status := range []int{500, 502} {
		t.Run(strconv.Itoa(status), func(t *testing.T) {
			fe := newFarEnd(t, upPlan())
			fe.ULHook = drained(func(w http.ResponseWriter, r *http.Request, n int) bool {
				jsonError(w, status, "no")
				return true
			})
			fe.start()
			wantFail(t, fe.runFull(t), nodetest.CodeHTTP, nodetest.PhaseUpload)
		})
	}
}

func TestN26_AnUploadReplyThatIsNotBytesIsNotDyno(t *testing.T) {
	for _, c := range []struct{ name, contentType, body string }{
		{"HTML", "text/html", "<html><body>OK</body></html>"},
		{"bytes of the wrong type", "application/json", `{"bytes":"lots"}`},
		{"an empty reply", "", ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			fe := newFarEnd(t, upPlan())
			fe.ULHook = drained(replyWith(200, c.contentType, []byte(c.body)))
			fe.start()
			wantFail(t, fe.runFull(t), nodetest.CodeNotDyno, nodetest.PhaseUpload)
		})
	}
}

// The first request in a direction to start 300 ms after its first one
// fails; the others carry on. The run must stop at once, and start nothing
// new, though the direction has 4 s to go.
func TestN27_OneFailingStreamEndsTheDirectionAtOnce(t *testing.T) {
	for _, c := range []struct {
		name  string
		path  string
		code  string
		phase nodetest.Phase
	}{
		{"download", "/dl", nodetest.CodeStream, nodetest.PhaseDownload},
		{"upload", "/ul", nodetest.CodeHTTP, nodetest.PhaseUpload},
	} {
		t.Run(c.name, func(t *testing.T) {
			p := shortPlan()
			p.Download.DurationMs = 500
			long := transferDoc{Streams: 3, DurationMs: 4000, WarmupMs: 300, StartBytes: 64 << 10, MaxBytes: 1 << 20, TargetRequestMs: 100}
			if c.path == "/dl" {
				p.Download = long
			} else {
				p.Upload = long
			}
			fe := newFarEnd(t, p)
			fe.DLDelay, fe.ULHold = 40*time.Millisecond, 40*time.Millisecond

			var first, failed atomic.Int64
			hook := func(w http.ResponseWriter, r *http.Request, n int) bool {
				now := time.Now().UnixNano()
				first.CompareAndSwap(0, now)
				if now-first.Load() < int64(300*time.Millisecond) || !failed.CompareAndSwap(0, now) {
					return false
				}
				if c.path == "/dl" {
					size, _ := strconv.ParseInt(r.URL.Query().Get("bytes"), 10, 64)
					breakAfter(w, size, int(size/2))
				} else {
					io.Copy(io.Discard, r.Body)
					jsonError(w, http.StatusInternalServerError, "no")
				}
				return true
			}
			if c.path == "/dl" {
				fe.DLHook = hook
			} else {
				fe.ULHook = hook
			}
			fe.start()
			res := fe.runFull(t)

			wantFail(t, res, c.code, c.phase)
			if res.Took > 1500*time.Millisecond {
				t.Errorf("Run took %v, want it to stop soon after the failure, not after the direction's 4 s", res.Took)
			}
			at := failed.Load()
			if at == 0 {
				t.Fatal("no request ever failed, so the test shows nothing")
			}
			for _, r := range on(fe.recs(), c.path) {
				if late := r.Start.Sub(time.Unix(0, at)); late > 100*time.Millisecond {
					t.Errorf("a %s started %v after the failure", c.path, late)
				}
			}
		})
	}
}

func TestN28_CancelingStopsTheRunItsRequestsAndItsConnections(t *testing.T) {
	p := shortPlan()
	p.Download = transferDoc{Streams: 3, DurationMs: 5000, WarmupMs: 500, StartBytes: 256 << 10, MaxBytes: 4 << 20, TargetRequestMs: 1000}
	fe := newFarEnd(t, p)
	fe.DLMbps = 40
	fe.start()
	before := runtime.NumGoroutine()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var armed atomic.Bool
	var canceledAt atomic.Int64
	res := run(ctx, fe.options(), func(pa progressAt) {
		if pa.Phase == nodetest.PhaseDownload && armed.CompareAndSwap(false, true) {
			time.AfterFunc(300*time.Millisecond, func() { // mid-download
				canceledAt.Store(time.Now().UnixNano())
				cancel()
			})
		}
	})

	var ne *nodetest.Error
	if !errors.As(res.Err, &ne) || ne.Code != nodetest.CodeCanceled {
		t.Fatalf("Run returned %v, want a *nodetest.Error with code %q", res.Err, nodetest.CodeCanceled)
	}
	if !reflect.DeepEqual(res.Out, nodetest.Outcome{}) {
		t.Errorf("a canceled Run returned a result: %+v", res.Out)
	}
	at := canceledAt.Load()
	if at == 0 {
		t.Fatal("Run ended before it was canceled")
	}
	if late := res.Start.Add(res.Took).Sub(time.Unix(0, at)); late > time.Second {
		t.Errorf("Run returned %v after ctx was canceled, want within 1 s", late)
	}

	// A request the client wrote just before the cancel can reach the far end
	// a moment after Run returns, so count once those have settled: a client
	// that keeps sending still adds requests over the next 500 ms.
	time.Sleep(100 * time.Millisecond)
	requests := len(fe.recs())
	time.Sleep(500 * time.Millisecond)
	if got := len(fe.recs()); got != requests {
		t.Errorf("the far end got %d more requests after Run returned", got-requests)
	}
	if !waitFor(time.Second, func() bool { return fe.openConns() == 0 }) {
		t.Errorf("%d connections still open 1 s after Run returned", fe.openConns())
	}
	if !waitFor(2*time.Second, func() bool { return runtime.NumGoroutine() <= before+2 }) {
		buf := make([]byte, 1<<16)
		t.Errorf("%d goroutines 2 s after Run returned, %d before it started:\n%s", runtime.NumGoroutine(), before, buf[:runtime.Stack(buf, true)])
	}
}

func TestN29_ErrorsAreStructuredAndSayWhatFailed(t *testing.T) {
	root := errors.New("boom")
	cause := &nodetest.Error{Code: nodetest.CodeStream, Phase: nodetest.PhaseDownload, Err: root}

	wrapped := fmt.Errorf("monitor: %w", cause)
	var got *nodetest.Error
	if !errors.As(wrapped, &got) || got != cause {
		t.Errorf("errors.As found %v, want the *Error it wrapped", got)
	}
	if !errors.Is(wrapped, root) {
		t.Error("errors.Is can't reach the underlying error through Unwrap")
	}
	if errors.Unwrap(cause) != root {
		t.Errorf("Unwrap = %v, want the underlying error", errors.Unwrap(cause))
	}

	// Error() names the phase and says what the code means in plain words, so
	// each code reads differently and none is the bare identifier.
	reads := map[string]string{}
	for _, code := range []string{
		nodetest.CodeBadFarEnd, nodetest.CodeUnreachable, nodetest.CodeTimeout, nodetest.CodeNotDyno,
		nodetest.CodeHTTP, nodetest.CodeStream, nodetest.CodeCanceled,
	} {
		msg := message(t, &nodetest.Error{Code: code, Phase: nodetest.PhaseDownload, Err: root})
		if !strings.Contains(msg, "download") {
			t.Errorf("code %q: Error() = %q, want it to name the download phase", code, msg)
		}
		if strings.Contains(code, "_") && strings.Contains(msg, code) {
			t.Errorf("code %q: Error() = %q, want plain words, not the code", code, msg)
		}
		if other, dup := reads[msg]; dup {
			t.Errorf("codes %q and %q both read %q", other, code, msg)
		}
		reads[msg] = code
	}
	for _, ph := range []nodetest.Phase{nodetest.PhaseWarm, nodetest.PhaseLatency, nodetest.PhaseDownload, nodetest.PhaseUpload} {
		if msg := message(t, &nodetest.Error{Code: nodetest.CodeTimeout, Phase: ph, Err: root}); !strings.Contains(msg, string(ph)) {
			t.Errorf("phase %q: Error() = %q, want it to name the phase", ph, msg)
		}
	}
	// No phase (the plan wasn't read) and no underlying error are both fine.
	for _, e := range []*nodetest.Error{
		{Code: nodetest.CodeUnreachable, Err: root},
		{Code: nodetest.CodeBadFarEnd},
	} {
		if msg := message(t, e); strings.TrimSpace(msg) == "" || strings.HasPrefix(msg, ":") {
			t.Errorf("Error() of %+v = %q, want a sentence", *e, msg)
		}
	}
}
