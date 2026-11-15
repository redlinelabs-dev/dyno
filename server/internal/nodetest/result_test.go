package nodetest_test

import (
	"bytes"
	"math"
	"testing"
	"time"

	"github.com/redlinelabs-dev/dyno/server/internal/nodetest"
)

// Long: a light run caps each direction at LightDuration however long the
// plan says, with LightWarmup as the warm-up. It takes two caps' worth.
func TestN30_LightCapsEachDirectionAtLightDurationWithLightWarmup(t *testing.T) {
	if testing.Short() {
		t.Skip("takes 11 s")
	}
	p := shortPlan()
	p.Download = transferDoc{Streams: 2, DurationMs: 20000, WarmupMs: 8000, StartBytes: 256 << 10, MaxBytes: 4 << 20, TargetRequestMs: 500}
	p.Upload = p.Download
	fe := newFarEnd(t, p)
	fe.DLMbps, fe.ULMbps = 16, 16 // 2 MB/s: five seconds move 10 MB, far under the default byte cap
	fe.start()
	o := fe.options()
	o.Kind = nodetest.Light
	res := run(testCtx(t, 60*time.Second), o, nil)
	mustPass(t, res)

	phases := map[string]time.Duration{
		"download": res.firstAt(nodetest.PhaseUpload).Sub(res.firstAt(nodetest.PhaseDownload)),
		"upload":   res.firstAt(nodetest.PhaseDone).Sub(res.firstAt(nodetest.PhaseUpload)),
	}
	for name, got := range phases {
		if got < nodetest.LightDuration-100*time.Millisecond || got > nodetest.LightDuration+500*time.Millisecond {
			t.Errorf("the %s phase lasted %v, want %v (+0.5 s)", name, got, nodetest.LightDuration)
		}
	}
	spans := parseDetail(t, res.Out).Spans
	if len(spans) != 2 {
		t.Fatalf("spans = %v, want two", spans)
	}
	for _, sp := range spans {
		near(t, "span length", sp[3]-sp[1], nodetest.LightDuration.Seconds(), 0.25)
		near(t, "span warm-up", sp[2]-sp[1], nodetest.LightWarmup.Seconds(), 0.25)
	}
}

func TestN31_LightMovesAtMostMaxBytesEachWayAndEndsEarly(t *testing.T) {
	const limit = 8 << 20
	p := shortPlan()
	p.Download = transferDoc{Streams: 3, DurationMs: 20000, WarmupMs: 8000, StartBytes: 1 << 20, MaxBytes: 64 << 20, TargetRequestMs: 1000}
	p.Upload = p.Download
	fe := newFarEnd(t, p)
	fe.DLMbps, fe.ULMbps = 0, 0 // fast: 8 MiB is a moment
	fe.start()
	o := fe.options()
	o.Kind, o.MaxBytes = nodetest.Light, limit
	res := run(testCtx(t, 60*time.Second), o, nil)
	mustPass(t, res)
	fe.waitIdle(t)
	rs := fe.recs()

	var asked, sent, bodies, read int64
	for _, r := range on(rs, "/dl") {
		asked += r.size()
		sent += r.Bytes
	}
	for _, r := range on(rs, "/ul") {
		bodies += r.Length
		read += r.Bytes
	}
	if asked == 0 || bodies == 0 {
		t.Fatalf("/dl asked for %d bytes and /ul sent %d: nothing moved", asked, bodies)
	}
	if asked > limit {
		t.Errorf("the /dl requests asked for %d bytes together, want at most MaxBytes %d", asked, limit)
	}
	if sent > limit {
		t.Errorf("the far end sent %d bytes, want at most MaxBytes %d", sent, limit)
	}
	if bodies > limit {
		t.Errorf("the /ul bodies add up to %d bytes, want at most MaxBytes %d", bodies, limit)
	}
	if read > limit {
		t.Errorf("the far end read %d bytes, want at most MaxBytes %d", read, limit)
	}
	sub := res.Out.Submission
	if sub.DownloadMbps <= 0 || sub.UploadMbps <= 0 {
		t.Errorf("DownloadMbps %v, UploadMbps %v, want both above 0 when the cap ended the directions", sub.DownloadMbps, sub.UploadMbps)
	}
	// Worked out over the time each direction ran, not the 5 s cap: 8 MiB over
	// 5 s is 13 Mbit/s, while loopback with no pacing moves it in well under
	// 0.7 s even under -race, which is over 100 Mbit/s.
	if sub.DownloadMbps < 100 || sub.UploadMbps < 100 {
		t.Errorf("DownloadMbps %v, UploadMbps %v, want both over 100: the speed is over the time the direction ran, not the 5 s cap (13 Mbit/s)", sub.DownloadMbps, sub.UploadMbps)
	}
	if res.Took > 5*time.Second {
		t.Errorf("Run took %v: the directions should end when the cap is reached, not at the 5 s cap", res.Took)
	}
}

func TestN32_FullFollowsThePlanAndIgnoresMaxBytes(t *testing.T) {
	p := shortPlan()
	p.Download = transferDoc{Streams: 2, DurationMs: 600, WarmupMs: 150, StartBytes: 2 << 20, MaxBytes: 8 << 20, TargetRequestMs: 100}
	p.Upload = p.Download
	fe := newFarEnd(t, p)
	fe.DLMbps, fe.ULMbps = 200, 200
	fe.start()
	o := fe.options()
	o.Kind, o.MaxBytes = nodetest.Full, 1<<20
	res := run(testCtx(t, 60*time.Second), o, nil)
	mustPass(t, res)
	fe.waitIdle(t)
	rs := fe.recs()

	var sent, read int64
	for _, r := range on(rs, "/dl") {
		sent += r.Bytes
	}
	for _, r := range on(rs, "/ul") {
		read += r.Bytes
	}
	if sent < 4<<20 || read < 4<<20 {
		t.Errorf("the far end sent %d and read %d bytes, want each well over 4 MiB: MaxBytes 1 MiB is for Light runs", sent, read)
	}
	for name, got := range map[string]time.Duration{
		"download": res.firstAt(nodetest.PhaseUpload).Sub(res.firstAt(nodetest.PhaseDownload)),
		"upload":   res.firstAt(nodetest.PhaseDone).Sub(res.firstAt(nodetest.PhaseUpload)),
	} {
		if got < 500*time.Millisecond || got > 1000*time.Millisecond {
			t.Errorf("the %s phase lasted %v, want the plan's 600 ms (+0.4 s, -0.1 s)", name, got)
		}
	}
}

// roundedTo reports whether v has no more than digits decimals.
func roundedTo(v float64, digits int) bool {
	f := math.Pow10(digits)
	return math.Abs(v*f-math.Round(v*f)) < 1e-6
}

func TestN33_TheSubmissionIsValidAndRounded(t *testing.T) {
	b := theBaseline(t)
	sub := b.res.Out.Submission
	if err := sub.Validate(); err != nil {
		t.Errorf("Submission.Validate: %v", err)
	}
	if sub.TestID != "" || sub.Client != "" || sub.Device != "" {
		t.Errorf("TestID %q, Client %q, Device %q, want all empty", sub.TestID, sub.Client, sub.Device)
	}
	if sub.Streams != b.plan.Download.Streams {
		t.Errorf("Streams = %d, want the download stream count %d", sub.Streams, b.plan.Download.Streams)
	}
	for name, v := range map[string]float64{
		"DownloadMbps": sub.DownloadMbps, "UploadMbps": sub.UploadMbps, "IdleMs": sub.IdleMs,
		"LoadedDownMs": sub.LoadedDownMs, "LoadedUpMs": sub.LoadedUpMs,
	} {
		if v <= 0 {
			t.Errorf("%s = %v, want above 0 after a full run", name, v)
		}
		if !roundedTo(v, 1) {
			t.Errorf("%s = %v, want it rounded to 0.1", name, v)
		}
	}
	if !roundedTo(sub.JitterMs, 2) {
		t.Errorf("JitterMs = %v, want it rounded to 0.01", sub.JitterMs)
	}
}

func TestN34_DetailHasTheBrowsersShape(t *testing.T) {
	b := theBaseline(t)
	if raw := bytes.TrimSpace(b.res.Out.Submission.Detail); len(raw) == 0 || raw[0] != '{' {
		t.Fatalf("Detail = %q, want a JSON object", raw)
	}
	d := parseDetail(t, b.res.Out)
	limit := b.res.Took.Seconds() + 0.3 // seconds from the start of the run
	if len(d.Spans) != 2 {
		t.Fatalf("spans = %v, want [[0, start, counted, end], [1, start, counted, end]]", d.Spans)
	}

	if len(d.Idle) != b.plan.Latency.IdleSamples {
		t.Errorf("idle has %d samples, want %d", len(d.Idle), b.plan.Latency.IdleSamples)
	}
	prev := 0.0
	for i, s := range d.Idle {
		if len(s) != 2 || s[0] < prev || s[0] > limit || s[1] <= 0 {
			t.Errorf("idle[%d] = %v, want [t, ms] with t rising from 0 to %.1f and ms above 0", i, s, limit)
			continue
		}
		prev = s[0]
	}

	for i, sp := range d.Spans {
		if len(sp) != 4 || sp[0] != float64(i) || sp[1] > sp[2] || sp[2] > sp[3] || sp[3] > limit {
			t.Errorf("spans[%d] = %v, want [%d, start, counted, end] with start <= counted <= end", i, sp, i)
		}
	}
	if len(d.Idle) > 0 && d.Spans[0][1] < d.Idle[len(d.Idle)-1][0]-0.11 { // spans are rounded to 0.1 s
		t.Errorf("the download span starts at %v, before the last idle sample at %v", d.Spans[0][1], d.Idle[len(d.Idle)-1][0])
	}
	if d.Spans[1][1] < d.Spans[0][3]-0.11 {
		t.Errorf("the upload span starts at %v, before the download span ends at %v", d.Spans[1][1], d.Spans[0][3])
	}

	// rate and probes: [t, value, dir], with both directions present and each
	// sample inside its direction's span.
	for name, series := range map[string][][]float64{"rate": d.Rate, "probes": d.Probes} {
		dirs := map[float64]bool{}
		for i, s := range series {
			if len(s) != 3 || (s[2] != 0 && s[2] != 1) || s[1] < 0 {
				t.Errorf("%s[%d] = %v, want [t, value, dir] with dir 0 or 1", name, i, s)
				continue
			}
			dirs[s[2]] = true
			if sp := d.Spans[int(s[2])]; s[0] < sp[1]-0.15 || s[0] > sp[3]+0.5 {
				t.Errorf("%s[%d] = %v, outside its direction's span %v", name, i, s, sp)
			}
		}
		if !dirs[0] || !dirs[1] {
			t.Errorf("%s has samples for directions %v, want both 0 (download) and 1 (upload)", name, dirs)
		}
	}

	if d.TimerLagMs == nil || *d.TimerLagMs < 0 {
		t.Errorf("timer_lag_ms = %v, want a number of milliseconds", d.TimerLagMs)
	}
	if d.UploadFromClient {
		t.Error(`detail says "upload_from_client" though upload replies arrived`)
	}
}

func TestN35_TheOutcomeNamesTheFarEnd(t *testing.T) {
	out := theBaseline(t).res.Out
	if out.FarEndName != "fakedyno" || out.FarEndVersion != "v9.9.9" {
		t.Errorf("FarEndName %q, FarEndVersion %q, want the plan's server.name and server.version", out.FarEndName, out.FarEndVersion)
	}
}

func TestN36_ProgressReportsPhasesInOrderFromOneGoroutine(t *testing.T) {
	b := theBaseline(t) // Warm is 300 ms, so the warm phase is there too
	res := b.res
	want := []nodetest.Phase{nodetest.PhaseWarm, nodetest.PhaseLatency, nodetest.PhaseDownload, nodetest.PhaseUpload, nodetest.PhaseDone}
	if got := res.phases(); len(got) != len(want) {
		t.Errorf("phases reported = %v, want %v", got, want)
	} else {
		for i := range want {
			if got[i] != want[i] {
				t.Errorf("phases reported = %v, want %v", got, want)
				break
			}
		}
	}
	if res.Overlap {
		t.Error("Progress was called while an earlier call was still running")
	}
	rate := map[nodetest.Phase]bool{}
	prev := 0.0
	for i, pa := range res.Prog {
		if pa.T < prev {
			t.Errorf("update %d (%s) has T %v, after T %v", i, pa.Phase, pa.T, prev)
		}
		prev = pa.T
		if pa.Mbps > 0 {
			rate[pa.Phase] = true
		}
	}
	for _, ph := range []nodetest.Phase{nodetest.PhaseDownload, nodetest.PhaseUpload} {
		if !rate[ph] {
			t.Errorf("no update in the %s phase had Mbps above 0", ph)
		}
	}
	if n := len(res.Prog); n > 0 && res.Prog[n-1].Phase != nodetest.PhaseDone {
		t.Errorf("the last update is phase %q, want %q", res.Prog[n-1].Phase, nodetest.PhaseDone)
	}
}
