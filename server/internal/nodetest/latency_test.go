package nodetest_test

import (
	"math"
	"net/http"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/redlinelabs-dev/dyno/server/internal/nodetest"
	"github.com/redlinelabs-dev/dyno/server/internal/stats"
)

func near(t *testing.T, what string, got, want, tol float64) {
	t.Helper()
	if math.IsNaN(got) || math.Abs(got-want) > tol {
		t.Errorf("%s = %v, want %v ± %v", what, got, want, tol)
	}
}

// noOverlap checks the requests to path went one at a time: each started no
// more than slack before the one ahead of it ended. A request that ran to its
// end is answered after the handler returns, so the next can't start before
// that and a few ms of slack is enough; a probe the client drops at the end
// of a direction ends only when the far end notices, so give it ~100 ms.
func noOverlap(t *testing.T, rs []rec, path string, slack time.Duration) {
	t.Helper()
	ps := on(rs, path)
	for i := 1; i < len(ps); i++ {
		if prev := ps[i-1]; !prev.End.IsZero() && ps[i].Start.Add(slack).Before(prev.End) {
			t.Errorf("%s request %d started %v before request %d ended: two in flight at once", path, i, prev.End.Sub(ps[i].Start), i-1)
		}
	}
}

// idleSleeps is what /ping sleeps in N10, by ping number: the plan's one
// ping first, then the five samples.
var idleSleeps = []time.Duration{
	5 * time.Millisecond,
	20 * time.Millisecond, 30 * time.Millisecond, 25 * time.Millisecond, 40 * time.Millisecond, 22 * time.Millisecond,
}

func TestN10_IdleLatencyIsTheMedianOfPingsSentOneAtATime(t *testing.T) {
	p := shortPlan() // 5 idle samples
	p.Download.DurationMs, p.Upload.DurationMs = 500, 500
	fe := newFarEnd(t, p)
	fe.PingHook = func(w http.ResponseWriter, r *http.Request, n int) bool {
		if n < len(idleSleeps) {
			fe.sleep(r.Context(), idleSleeps[n])
		}
		return false
	}
	fe.start()
	res := fe.runFull(t)
	mustPass(t, res)
	fe.waitIdle(t)
	rs := fe.recs()

	// The median of 20, 30, 25, 40, 22 is 25 and their jitter (10+5+15+18)/4
	// is 12; the plan's first ping isn't a sample.
	sub := res.Out.Submission
	// IdleMs is 24.5 to 39 (case N10, amended twice): the sleeps' median, 25,
	// plus request overhead, which under -race can pass 10 ms. The 39 still
	// catches the slowest ping taken (40 plus overhead). The 24.5 does not
	// catch the plan's ping counted: its median is 23.5 plus overhead, which
	// lands inside the range. The two checks below do: IdleMs is the median of
	// the five samples in detail.idle, and each sample took at least its sleep.
	// JitterMs is within 6 of 12.
	near(t, "IdleMs", sub.IdleMs, 31.75, 7.25)
	near(t, "JitterMs", sub.JitterMs, 12, 6)
	idle := parseDetail(t, res.Out).Idle
	ms := make([]float64, 0, len(idle))
	for i, s := range idle {
		ms = append(ms, s[1])
		// idleSleeps[0] is the plan's ping, so sample i is ping i+1. A sample
		// is rounded to 0.1 ms and a timer never fires early, so no slack.
		if i+1 < len(idleSleeps) {
			if floor := float64(idleSleeps[i+1]) / float64(time.Millisecond); s[1] < floor {
				t.Errorf("detail.idle[%d] = %v ms, want at least %v: the sample isn't ping %d", i, s[1], floor, i+1)
			}
		}
	}
	// Each sample and IdleMs are rounded to 0.1 ms, so they can differ by 0.1.
	if len(ms) > 0 {
		near(t, "IdleMs against the median of detail.idle", sub.IdleMs, stats.Median(ms), 0.11)
	}
	// Every idle ping completes, so none can start before the last one ended.
	noOverlap(t, rs, "/ping", 5*time.Millisecond)
	if n := pingsBefore(rs, "/ping", latencyEnd(res, rs, "/dl")); n != 6 {
		t.Errorf("%d pings before the first /dl, want 6 (the plan's one, then 5 samples)", n)
	}
	if d := parseDetail(t, res.Out); len(d.Idle) != 5 {
		t.Errorf("detail.idle has %d samples, want 5", len(d.Idle))
	}
}

func TestN11_TheTimeToOpenAConnectionIsNotLatency(t *testing.T) {
	p := shortPlan()
	p.Download.Streams, p.Upload.Streams = 1, 1
	p.Download.DurationMs, p.Upload.DurationMs = 500, 500
	fe := newFarEnd(t, p)
	fe.AcceptDelay = 100 * time.Millisecond
	fe.start()
	res := fe.runFull(t)
	mustPass(t, res)

	if got := res.Out.Submission.IdleMs; got >= 20 {
		t.Errorf("IdleMs = %v with /ping answering at once, want under 20: the 100 ms to open a connection was counted", got)
	}
	for i, s := range parseDetail(t, res.Out).Idle {
		if s[1] >= 60 {
			t.Errorf("idle sample %d = %v ms, want under 60 (counting the 100 ms to open a connection would give over 100)", i, s[1])
		}
	}
}

func TestN12_WarmUpPingsRunBeforeTheIdleSamples(t *testing.T) {
	b := theBaseline(t) // Warm is 300 ms
	if len(b.res.Prog) == 0 || b.res.Prog[0].Phase != nodetest.PhaseWarm {
		t.Fatalf("first Progress update = %+v, want phase %q", b.res.Prog, nodetest.PhaseWarm)
	}
	end := latencyEnd(b.res, b.recs, "/x/dl")
	pings := on(b.recs, "/x/ping")
	if len(pings) == 0 {
		t.Fatal("no /x/ping requests")
	}
	n := pingsBefore(b.recs, "/x/ping", end)
	if want := b.plan.Latency.IdleSamples + 1; n <= want {
		t.Errorf("%d pings before the first /dl, want more than %d", n, want)
	}
	var last time.Time
	for _, p := range pings {
		if p.Start.Before(end) {
			last = p.Start
		}
	}
	// The samples are the last pings, so the warm-up came first. The
	// warm-up's clock starts after the plan request, so it runs 300 ms from
	// just before the first ping; the slack is for that ping's trip to the
	// far end.
	if span := last.Sub(pings[0].Start); span < 295*time.Millisecond {
		t.Errorf("pings before the first /dl spanned %v, want at least 300 ms of warm-up and then the samples", span)
	}
}

func TestN12_WithoutWarmUpExactlyOnePingPrecedesTheSamples(t *testing.T) {
	fe := newFarEnd(t, shortPlan()).start() // 5 idle samples; options() has Warm -1
	res := fe.runUntil(t, fe.options(), nodetest.PhaseDownload, 100*time.Millisecond)
	for _, pa := range res.Prog {
		if pa.Phase == nodetest.PhaseWarm {
			t.Fatalf("Progress reported %q with Warm negative", pa.Phase)
		}
	}
	if len(res.Prog) == 0 || res.Prog[0].Phase != nodetest.PhaseLatency {
		t.Errorf("first Progress update = %+v, want phase %q", res.Prog, nodetest.PhaseLatency)
	}
	rs := fe.recs()
	if n := pingsBefore(rs, "/ping", latencyEnd(res, rs, "/dl")); n != 6 {
		t.Errorf("%d pings before the first /dl, want exactly 6 (1 + idle_samples)", n)
	}
}

// An idle or warm-up ping over PingTimeout fails the run in its phase. (A
// loaded probe doesn't: see the next test.)
func TestN13_APingOverPingTimeoutFailsTheRunInItsPhase(t *testing.T) {
	for _, c := range []struct {
		name  string
		warm  time.Duration
		hangs func(fe *farEnd, n int) bool
		phase nodetest.Phase
	}{
		{"latency", -1, func(fe *farEnd, n int) bool { return n == 2 }, nodetest.PhaseLatency},
		{"warm", 400 * time.Millisecond, func(fe *farEnd, n int) bool { return n == 3 }, nodetest.PhaseWarm},
	} {
		t.Run(c.name, func(t *testing.T) {
			p := shortPlan()
			fe := newFarEnd(t, p)
			fe.DLDelay = 100 * time.Millisecond
			fe.PingHook = func(w http.ResponseWriter, r *http.Request, n int) bool {
				if c.hangs(fe, n) {
					fe.block(r)
					return true
				}
				return false
			}
			fe.start()
			o := fe.options()
			o.Warm, o.PingTimeout = c.warm, 300*time.Millisecond
			res := run(testCtx(t, 30*time.Second), o, nil)
			wantFail(t, res, nodetest.CodeTimeout, c.phase)
			if res.Took > 2*time.Second {
				t.Errorf("took %v to give up on a ping with PingTimeout 300 ms", res.Took)
			}
		})
	}
}

// A loaded probe that times out ends that direction's probing, not the run
// (ADR-0032). The download lasts 3 s with a probe every 100 ms, each answered
// in 20 ms until hangAt after the first /dl, then the next one hangs (that one
// probe only). The
// probes that finished after warm-up before the hang give LoadedDownMs about
// 20, so a deterministic non-zero figure; the upload's own probing carries on.
func TestN13_ALoadedProbeThatHangsEndsThatDirectionsProbingNotTheRun(t *testing.T) {
	for _, c := range []struct {
		name    string
		hangAt  time.Duration // after the first /dl
		timeout time.Duration // PingTimeout
	}{
		// The hung probe times out at 1.3 s, well inside the download.
		{"times out inside the direction", time.Second, 300 * time.Millisecond},
		// The hung probe would time out at about 7.5 s, long after the
		// direction ends at 3 s: the run must not wait for it.
		{"still hung when the direction ends", 2500 * time.Millisecond, 5 * time.Second},
	} {
		t.Run(c.name, func(t *testing.T) {
			p := shortPlan()
			p.Download.DurationMs = 3000
			fe := newFarEnd(t, p)
			fe.DLDelay = 100 * time.Millisecond
			var hung atomic.Bool
			fe.PingHook = func(w http.ResponseWriter, r *http.Request, n int) bool {
				if first := fe.firstStart("/dl"); !first.IsZero() {
					// Hold one probe only, the first at or after hangAt, so the
					// upload's probing (which starts a moment before 3 s on the
					// far end's clock, with the abandoned /dl still in flight)
					// is never touched.
					if since := time.Since(first); since >= c.hangAt && since < 3*time.Second && fe.inflightNow("/dl") > 0 && hung.CompareAndSwap(false, true) {
						fe.block(r)
						return true
					}
				}
				fe.sleep(r.Context(), 20*time.Millisecond)
				return false
			}
			fe.start()
			o := fe.options()
			o.PingTimeout = c.timeout
			res := run(testCtx(t, 30*time.Second), o, nil)
			mustPass(t, res)
			if !hung.Load() {
				t.Fatal("no probe was held, so this test proves nothing")
			}
			// 3 s of download, 0.6 s of upload and a moment either side; a run that
			// waited for the hung probe would take 7.5 s in the second case.
			if res.Took > 5*time.Second {
				t.Errorf("Run took %v, want about 3.8 s: it waited for a hung probe past the direction's end", res.Took)
			}
			sub := res.Out.Submission
			if sub.DownloadMbps <= 0 {
				t.Errorf("DownloadMbps = %v, want above 0: the direction carried on", sub.DownloadMbps)
			}
			near(t, "LoadedDownMs", sub.LoadedDownMs, 20, 15)
			if sub.LoadedUpMs <= 0 || sub.LoadedUpMs > 100 {
				t.Errorf("LoadedUpMs = %v, want about 20: the hang ended the download's probing only", sub.LoadedUpMs)
			}
		})
	}
}

func TestN13_APingThatAnswersAnErrorStatusIsHTTP(t *testing.T) {
	for _, status := range []int{404, 500, 502, 503} {
		t.Run(strconv.Itoa(status), func(t *testing.T) {
			fe := newFarEnd(t, shortPlan())
			fe.PingHook = replyWith(status, "text/html", []byte("<html><h1>Bad Gateway</h1></html>"))
			fe.start()
			res := fe.runFull(t)
			wantFail(t, res, nodetest.CodeHTTP, nodetest.PhaseLatency)
			if n := len(on(fe.recs(), "/dl")); n != 0 {
				t.Errorf("%d /dl requests after a failed ping", n)
			}
		})
	}
}

func TestN14_ProbesGoOnTheirOwnConnection(t *testing.T) {
	b := theBaseline(t)
	probe, load := map[string]bool{}, map[string]bool{}
	for _, r := range b.recs {
		switch r.Path {
		case "/x/ping":
			probe[r.Remote] = true
		case "/x/dl", "/x/ul":
			load[r.Remote] = true
		}
	}
	if len(probe) == 0 || len(load) == 0 {
		t.Fatalf("connections seen: %d carrying /ping, %d carrying /dl or /ul", len(probe), len(load))
	}
	for addr := range probe {
		if load[addr] {
			t.Errorf("connection %s carried both /ping and a transfer", addr)
		}
	}
}

func TestN15_LoadedLatencyIsTheMedianOfTheProbesDuringEachDirection(t *testing.T) {
	p := shortPlan()
	p.Latency.LoadedIntervalMs = 100
	p.Download = transferDoc{Streams: 2, DurationMs: 1000, WarmupMs: 200, StartBytes: 64 << 10, MaxBytes: 4 << 20, TargetRequestMs: 100}
	p.Upload = p.Download
	fe := newFarEnd(t, p)
	fe.DLDelay, fe.ULHold = 80*time.Millisecond, 80*time.Millisecond // a transfer is always in flight
	fe.PingHook = func(w http.ResponseWriter, r *http.Request, n int) bool {
		d := 5 * time.Millisecond
		switch {
		case fe.inflightNow("/dl") > 0:
			d = 60 * time.Millisecond
		case fe.inflightNow("/ul") > 0:
			d = 25 * time.Millisecond
		}
		fe.sleep(r.Context(), d)
		return false
	}
	fe.start()
	res := fe.runFull(t)
	mustPass(t, res)
	fe.waitIdle(t)
	rs := fe.recs()

	sub := res.Out.Submission
	near(t, "LoadedDownMs", sub.LoadedDownMs, 60, 15)
	near(t, "LoadedUpMs", sub.LoadedUpMs, 25, 15)
	if sub.IdleMs >= 15 {
		t.Errorf("IdleMs = %v, want under 15 (/ping sleeps 5 ms with nothing in flight)", sub.IdleMs)
	}
	noOverlap(t, rs, "/ping", 100*time.Millisecond) // probes go one at a time; the last of a direction is dropped

	// A probe every loaded_interval_ms: none sooner, and a steady run of them.
	for _, c := range []struct {
		dir        string
		from, till nodetest.Phase
	}{{"download", nodetest.PhaseDownload, nodetest.PhaseUpload}, {"upload", nodetest.PhaseUpload, nodetest.PhaseDone}} {
		from, till := res.firstAt(c.from), res.firstAt(c.till)
		var starts []time.Time
		for _, r := range on(rs, "/ping") {
			if !r.Start.Before(from) && r.Start.Before(till) {
				starts = append(starts, r.Start)
			}
		}
		if len(starts) < 4 {
			t.Errorf("%s: %d probes in a 1 s direction, want at least 4", c.dir, len(starts))
		}
		for i := 1; i < len(starts); i++ {
			if gap := starts[i].Sub(starts[i-1]); gap < 90*time.Millisecond || gap > 310*time.Millisecond {
				t.Errorf("%s: probe %d came %v after the one before, want about the 100 ms interval", c.dir, i, gap)
			}
		}
	}
}

// The probes before the warm-up ends are fast and the ones after it slow, and
// there are more of the fast ones: a median over every probe would read about
// 2 ms, one over those that finish after the warm-up reads about 100.
func TestN15_OnlyProbesThatFinishAfterWarmUpCount(t *testing.T) {
	p := shortPlan()
	p.Latency.LoadedIntervalMs = 50
	p.Download = transferDoc{Streams: 2, DurationMs: 1000, WarmupMs: 400, StartBytes: 64 << 10, MaxBytes: 4 << 20, TargetRequestMs: 100}
	p.Upload.DurationMs = 500
	fe := newFarEnd(t, p)
	fe.DLDelay = 80 * time.Millisecond
	fe.PingHook = func(w http.ResponseWriter, r *http.Request, n int) bool {
		d := 2 * time.Millisecond
		if first := fe.firstStart("/dl"); !first.IsZero() && time.Since(first) >= 400*time.Millisecond && fe.inflightNow("/dl") > 0 {
			d = 100 * time.Millisecond
		}
		fe.sleep(r.Context(), d)
		return false
	}
	fe.start()
	res := fe.runFull(t)
	mustPass(t, res)
	near(t, "LoadedDownMs", res.Out.Submission.LoadedDownMs, 100, 15)
}
