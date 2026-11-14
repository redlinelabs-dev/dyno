package nodetest_test

import (
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/redlinelabs-dev/dyno/server/internal/nodetest"
)

// Round 2: two cases a mutation run found untested. With the guard each one
// covers taken out, every earlier test stayed green.

// r2PlanCap is the most a plan reply may hold (N4).
const r2PlanCap = 64 << 10

// r2Pads are runs of whitespace for after a valid plan's closing brace, each
// taking the body past the cap. They are still valid JSON, so the cap is all
// that can refuse them. Whitespace before the plan would prove nothing: a
// reader that stops at the cap loses the plan itself, and what is left fails
// to parse with or without a size check.
func r2Pads(planLen int) []struct{ name, pad string } {
	return []struct{ name, pad string }{
		{"spaces, one byte past the cap", strings.Repeat(" ", r2PlanCap+1-planLen)},
		{"newlines, far past the cap", strings.Repeat("\n", 4*r2PlanCap)},
		{"CRLF line ends", strings.Repeat("\r\n", 36<<10)},
	}
}

// r2FarEndReplying is a far end whose plan reply is body, as JSON.
func r2FarEndReplying(t *testing.T, body []byte) *farEnd {
	t.Helper()
	fe := newFarEnd(t, shortPlan())
	fe.PlanHook = replyWith(200, "application/json", body)
	return fe.start()
}

// r2OnlyThePlan checks the far end saw the plan request and nothing after it:
// no /dl, /ul or /ping.
func r2OnlyThePlan(t *testing.T, rs []rec) {
	t.Helper()
	if len(rs) == 0 {
		t.Error("the far end saw no request, so the plan was never read")
	}
	for _, r := range rs {
		if r.Path != "/api/v1/plan" {
			t.Errorf("after a plan the cap refuses, %s %s went out", r.Method, r.Path)
		}
	}
}

func TestN4b_APlanPaddedPastTheCapWithTrailingWhitespaceIsNotDyno(t *testing.T) {
	base := string(planJSON(t, func(map[string]any) {}))
	for _, c := range r2Pads(len(base)) {
		body := append([]byte(base), c.pad...)
		t.Run("Run, "+c.name, func(t *testing.T) {
			fe := r2FarEndReplying(t, body)
			wantFail(t, fe.runFull(t), nodetest.CodeNotDyno, "")
			r2OnlyThePlan(t, fe.recs())
		})
		t.Run("Check, "+c.name, func(t *testing.T) {
			fe := r2FarEndReplying(t, body)
			_, err := nodetest.Check(testCtx(t, 10*time.Second), fe.options())
			wantCheckFail(t, err, nodetest.CodeNotDyno)
			r2OnlyThePlan(t, fe.recs())
		})
	}

	// The same padding under the cap is still a plan, so the refusals above
	// are about size and nothing else.
	t.Run("under the cap, the same padding is fine", func(t *testing.T) {
		fe := r2FarEndReplying(t, append([]byte(base), strings.Repeat("\n", 48<<10)...))
		info, err := nodetest.Check(testCtx(t, 10*time.Second), fe.options())
		if err != nil {
			t.Fatalf("Check: %v", err)
		}
		if want := (nodetest.FarEndInfo{Name: "fakedyno", Version: "v9.9.9"}); info != want {
			t.Errorf("Check = %+v, want the plan's server %+v", info, want)
		}
		res := fe.runUntil(t, fe.options(), nodetest.PhaseDownload, 0)
		if res.firstAt(nodetest.PhaseDownload).IsZero() {
			t.Errorf("Run never reached the download (err %v)", res.Err)
		}
	})
}

// docs/api.md: unreachable is "no connection", in any phase; stream_failed is
// a transfer that broke off part way. A far end that stops listening once the
// download starts refuses the download's new connections, which is the first
// kind and not the second.
func TestN3b_ARefusedConnectionInTheDownloadIsUnreachableNotAStreamFailure(t *testing.T) {
	p := shortPlan()
	p.Download.Streams = nodetest.MaxStreams // the plan's connection may serve one; the rest must dial
	fe := newFarEnd(t, p)
	fe.DLHook = func(w http.ResponseWriter, r *http.Request, n int) bool {
		w.Header().Set("Connection", "close") // no stream keeps a connection past one request
		return false
	}
	fe.start()

	// Progress is called on Run's own goroutine, so the listener is gone
	// before Run goes on to open the download's streams. Connections already
	// open (the probes') stay up; only new ones are refused.
	var stopped atomic.Bool
	res := run(testCtx(t, 30*time.Second), fe.options(), func(pa progressAt) {
		if pa.Phase == nodetest.PhaseDownload && stopped.CompareAndSwap(false, true) {
			fe.srv.Listener.Close()
		}
	})
	if !stopped.Load() {
		t.Fatalf("Run never reported the %s phase (err %v), so the listener stayed open", nodetest.PhaseDownload, res.Err)
	}
	wantFail(t, res, nodetest.CodeUnreachable, nodetest.PhaseDownload)
	if n := len(on(fe.recs(), "/ul")); n != 0 {
		t.Errorf("%d /ul requests after a failed download", n)
	}
}
