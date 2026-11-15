package nodetest_test

import (
	"net/http"
	"testing"
	"time"

	"github.com/redlinelabs-dev/dyno/server/internal/nodetest"
)

// A plan that sends the test to another machine is refused before a request
// goes there.
func TestNx_APlanThatNamesAnotherHostIsNotDyno(t *testing.T) {
	p := shortPlan()
	p.Endpoints.Download = "http://dyno-nodetest-elsewhere.invalid/dl"
	fe := newFarEnd(t, p).start()
	res := fe.runFull(t)
	wantFail(t, res, nodetest.CodeNotDyno, "")
	if n := len(fe.recs()); n != 1 {
		t.Errorf("%d requests reached the far end, want just the plan", n)
	}
}

// Numbers a far end gets wrong are clamped, not refused: fractions, negative
// counts and values past any integer.
func TestNx_CheckAcceptsPlansWithFractionsAndHugeNumbers(t *testing.T) {
	fe := newFarEnd(t, shortPlan())
	fe.PlanHook = replyWith(200, "application/json", planJSON(t, func(m map[string]any) {
		dl := m["download"].(map[string]any)
		dl["streams"], dl["duration_ms"], dl["max_bytes"] = 2.5, 1e300, 1e30
		m["latency"].(map[string]any)["idle_samples"] = -4
	}))
	fe.start()
	info, err := nodetest.Check(testCtx(t, 10*time.Second), fe.options())
	if err != nil {
		t.Fatalf("Check refused a plan with fractions and huge numbers: %v", err)
	}
	if info.Name != "fakedyno" {
		t.Errorf("Check = %+v, want the plan's server", info)
	}
}

// A body that isn't the length asked for isn't a dyno's, however well formed
// the reply is.
func TestNx_ADownloadOfTheWrongLengthIsNotDyno(t *testing.T) {
	fe := newFarEnd(t, shortPlan())
	fe.DLHook = replyWith(200, "application/octet-stream", []byte("not what you asked for"))
	fe.start()
	wantFail(t, fe.runFull(t), nodetest.CodeNotDyno, nodetest.PhaseDownload)
}

// A download that never sends a byte is a timeout, not a run that measured
// 0 Mbit/s.
func TestNx_ADirectionThatMovedNothingIsATimeout(t *testing.T) {
	p := shortPlan()
	p.Download.DurationMs = 500
	fe := newFarEnd(t, p)
	fe.DLHook = func(w http.ResponseWriter, r *http.Request, n int) bool { fe.block(r); return true }
	fe.start()
	res := fe.runFull(t)
	wantFail(t, res, nodetest.CodeTimeout, nodetest.PhaseDownload)
	if res.Took > 3*time.Second {
		t.Errorf("took %v, want about the direction's 0.5 s", res.Took)
	}
}

// A far end can't make an upload read faster than it was written by claiming
// it received more than it was sent.
func TestNx_AnUploadReplyCanNotClaimMoreThanWasSent(t *testing.T) {
	p := shortPlan()
	p.Download.DurationMs, p.Upload.DurationMs = 500, 500
	fe := newFarEnd(t, p)
	fe.ULCount = func(read int64) int64 { return read * 1000 }
	fe.start()
	res := fe.runFull(t)
	mustPass(t, res)
	if got := res.Out.Submission.UploadMbps; got <= 0 || got > 5000 {
		t.Errorf("UploadMbps = %v from a far end claiming 1000 times what it read, want the real speed (200 Mbit/s link)", got)
	}
}
