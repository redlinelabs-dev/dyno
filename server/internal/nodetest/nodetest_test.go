package nodetest_test

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/redlinelabs-dev/dyno/server/internal/nodetest"
)

// The baseline is one full run against a far end that uses everything at
// once: three download streams, two upload streams, endpoints moved under
// /x/, a warm-up. The cases that only look at what an ordinary run does
// share its recording instead of each running a test of their own.
type baseline struct {
	plan  planDoc
	res   *result
	recs  []rec
	peaks map[string]int
}

var (
	baseOnce sync.Once
	baseData *baseline
)

func baselinePlan() planDoc {
	p := shortPlan()
	p.Endpoints.Download, p.Endpoints.Upload, p.Endpoints.Ping = "/x/dl", "/x/ul", "/x/ping"
	p.Latency.IdleSamples, p.Latency.LoadedIntervalMs = 5, 50
	p.Download = transferDoc{Streams: 3, DurationMs: 600, WarmupMs: 150, StartBytes: 64 << 10, MaxBytes: 4 << 20, TargetRequestMs: 100}
	p.Upload = transferDoc{Streams: 2, DurationMs: 600, WarmupMs: 150, StartBytes: 1 << 20, MaxBytes: 4 << 20, TargetRequestMs: 100}
	return p
}

// theBaseline runs the baseline once, whichever test asks first.
func theBaseline(t *testing.T) *baseline {
	t.Helper()
	baseOnce.Do(func() {
		plan := baselinePlan()
		fe := newFarEnd(t, plan)
		fe.DLMbps, fe.ULMbps = 400, 400
		fe.DLDelay, fe.ULHold = 40*time.Millisecond, 60*time.Millisecond // requests that overlap
		fe.KeepBody = 1 << 20
		fe.PingHook = func(w http.ResponseWriter, r *http.Request, n int) bool {
			fe.sleep(r.Context(), 3*time.Millisecond) // a believable round trip
			return false
		}
		fe.start()
		o := fe.options()
		o.Warm = 300 * time.Millisecond
		// The pause widens the window in which two Progress calls would overlap (N36).
		res := run(testCtx(t, 60*time.Second), o, func(progressAt) { time.Sleep(200 * time.Microsecond) })
		fe.waitIdle(t)
		baseData = &baseline{plan: plan, res: res, recs: fe.recs(), peaks: map[string]int{}}
		for _, p := range []string{plan.Endpoints.Download, plan.Endpoints.Upload, plan.Endpoints.Ping} {
			baseData.peaks[p] = fe.peakInflight(p)
		}
	})
	if baseData.res.Err != nil {
		t.Fatalf("the baseline run failed: %v", baseData.res.Err)
	}
	return baseData
}

// pingsBefore counts the requests to path that started before when.
func pingsBefore(rs []rec, path string, when time.Time) int {
	n := 0
	for _, r := range on(rs, path) {
		if r.Start.Before(when) {
			n++
		}
	}
	return n
}

// latencyEnd is when the latency phase was over: the first /dl or the first
// download Progress update, whichever came first. A loaded probe may leave
// a hair before the first /dl, so the count of latency pings stops here.
func latencyEnd(res *result, rs []rec, dl string) time.Time {
	end := res.firstAt(nodetest.PhaseDownload)
	if first := on(rs, dl); len(first) > 0 && (end.IsZero() || first[0].Start.Before(end)) {
		end = first[0].Start
	}
	return end
}

// noCompression is N8: nothing asks for a compressed reply.
func noCompression(t *testing.T, rs []rec) {
	t.Helper()
	for _, r := range rs {
		if ae := strings.TrimSpace(r.Header.Get("Accept-Encoding")); ae != "" && !strings.EqualFold(ae, "identity") {
			t.Errorf("%s %s sent Accept-Encoding: %s", r.Method, r.Path, ae)
		}
	}
}

func TestN1_PlanFirstThenTheEndpointsItNames(t *testing.T) {
	b := theBaseline(t)
	if len(b.recs) == 0 || b.recs[0].Method != "GET" || b.recs[0].Path != "/api/v1/plan" {
		t.Fatalf("the first request was %+v, want GET /api/v1/plan", b.recs)
	}
	named := map[string]bool{"/api/v1/plan": true, "/x/dl": true, "/x/ul": true, "/x/ping": true}
	seen := map[string]int{}
	for _, r := range b.recs {
		seen[r.Path]++
		if !named[r.Path] {
			t.Errorf("request to %s, which the plan didn't name", r.Path)
		}
	}
	if seen["/api/v1/plan"] != 1 {
		t.Errorf("the plan was fetched %d times, want once", seen["/api/v1/plan"])
	}
	for _, p := range []string{"/x/dl", "/x/ul", "/x/ping"} {
		if seen[p] == 0 {
			t.Errorf("nothing was sent to %s", p)
		}
	}
}

func TestN1_FarEndWithATrailingSlashStillReachesThePlan(t *testing.T) {
	fe := newFarEnd(t, shortPlan()).start()
	o := fe.options()
	o.FarEnd += "/"
	fe.runUntil(t, o, nodetest.PhaseDownload, 100*time.Millisecond)
	rs := fe.recs()
	if len(rs) == 0 || rs[0].Path != "/api/v1/plan" {
		t.Fatalf("the first request was %+v, want GET /api/v1/plan", rs)
	}
	for _, r := range rs {
		if strings.HasPrefix(r.Path, "//") {
			t.Errorf("request to %q: the far end's trailing slash was glued on", r.Path)
		}
	}
	if len(on(rs, "/dl")) == 0 || len(on(rs, "/ping")) == 0 {
		t.Errorf("paths seen: %d /ping, %d /dl; want both", len(on(rs, "/ping")), len(on(rs, "/dl")))
	}
}

// badFarEnds are FarEnd values that aren't an absolute http or https URL.
// Those that can name fe do, so a request that leaks out shows up there.
func badFarEnds(fe *farEnd) []struct{ name, farEnd string } {
	host := strings.TrimPrefix(fe.URL, "http://")
	return []struct{ name, farEnd string }{
		{"host and port, no scheme", host},
		{"name and port, no scheme", "tailmeadow:3966"},
		{"ftp", "ftp://" + host + "/"},
		{"file", "file:///etc/hosts"},
		{"scheme-relative", "//" + host},
		{"empty", ""},
		{"blank", "   "},
		{"http without a host", "http://"},
		{"https without a host", "https://"},
	}
}

func TestN2_ABadFarEndIsRefusedAndNothingIsSent(t *testing.T) {
	fe := newFarEnd(t, shortPlan()).start()
	for _, bad := range badFarEnds(fe) {
		t.Run(bad.name, func(t *testing.T) {
			res := run(testCtx(t, 10*time.Second), nodetest.Options{FarEnd: bad.farEnd, Warm: -1}, nil)
			wantFail(t, res, nodetest.CodeBadFarEnd, "")
			if res.Took > time.Second {
				t.Errorf("took %v to refuse %q", res.Took, bad.farEnd)
			}
		})
	}
	if n := len(fe.recs()); n != 0 {
		t.Errorf("bad far ends sent %d requests to the far end they named", n)
	}
}

// closedAddr is a loopback address nothing listens on.
func closedAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()
	return addr
}

// needFastLookupFailure skips the test where a name under .invalid doesn't
// fail to resolve at once (no resolver to ask, or one that takes its time).
func needFastLookupFailure(t *testing.T, host string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
	defer cancel()
	began := time.Now()
	_, err := net.DefaultResolver.LookupHost(ctx, host)
	if err == nil || time.Since(began) > time.Second {
		t.Skipf("this machine's resolver doesn't fail %s within a second (err %v)", host, err)
	}
}

func TestN3_NothingListeningIsUnreachable(t *testing.T) {
	res := run(testCtx(t, 20*time.Second), nodetest.Options{FarEnd: "http://" + closedAddr(t), Warm: -1}, nil)
	wantFail(t, res, nodetest.CodeUnreachable, "")
	if res.Took > 2*time.Second {
		t.Errorf("took %v to find nothing listening, want under 2 s", res.Took)
	}
}

func TestN3_AnUnresolvableHostIsUnreachable(t *testing.T) {
	const host = "dyno-nodetest-n3.invalid"
	needFastLookupFailure(t, host)
	res := run(testCtx(t, 20*time.Second), nodetest.Options{FarEnd: "http://" + host + ":3966", Warm: -1}, nil)
	wantFail(t, res, nodetest.CodeUnreachable, "")
}

// replyWith is a hook that answers with a fixed status and body.
func replyWith(status int, contentType string, body []byte) func(http.ResponseWriter, *http.Request, int) bool {
	return func(w http.ResponseWriter, r *http.Request, n int) bool {
		if contentType != "" {
			w.Header().Set("Content-Type", contentType)
		}
		w.WriteHeader(status)
		w.Write(body)
		return true
	}
}

// planJSON is shortPlan as JSON, after edit has changed the object.
func planJSON(t *testing.T, edit func(m map[string]any)) []byte {
	t.Helper()
	b, err := json.Marshal(shortPlan())
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	edit(m)
	out, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// notDynoPlans are replies to the plan request that dyno wouldn't send.
func notDynoPlans(t *testing.T) []struct {
	name string
	hook func(http.ResponseWriter, *http.Request, int) bool
} {
	t.Helper()
	return []struct {
		name string
		hook func(http.ResponseWriter, *http.Request, int) bool
	}{
		{"HTML", replyWith(200, "text/html; charset=utf-8", []byte("<!doctype html><html><title>Welcome to nginx!</title></html>"))},
		{"JSON without test_id", replyWith(200, "application/json", planJSON(t, func(m map[string]any) { delete(m, "test_id") }))},
		{"JSON without endpoints", replyWith(200, "application/json", planJSON(t, func(m map[string]any) { delete(m, "endpoints") }))},
		{"a body over 64 KiB", replyWith(200, "application/json", planJSON(t, func(m map[string]any) { m["pad"] = strings.Repeat("x", 70<<10) }))},
		{"an empty reply", replyWith(200, "", nil)},
		{"a JSON list", replyWith(200, "application/json", []byte("[1,2,3]"))},
	}
}

func TestN4_AReplyThatIsNotAPlanIsNotDyno(t *testing.T) {
	for _, c := range notDynoPlans(t) {
		t.Run(c.name, func(t *testing.T) {
			fe := newFarEnd(t, shortPlan())
			fe.PlanHook = c.hook
			fe.start()
			res := fe.runFull(t)
			wantFail(t, res, nodetest.CodeNotDyno, "")
			for _, r := range fe.recs() {
				if r.Path != "/api/v1/plan" {
					t.Errorf("after a plan that was %s, a request went to %s", c.name, r.Path)
				}
			}
		})
	}
}

// redirectTarget is a second server that counts what reaches it.
func redirectTarget(t *testing.T) (target string, hits func() int) {
	t.Helper()
	var n atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n.Add(1)
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(srv.Close)
	return srv.URL, func() int { return int(n.Load()) }
}

// redirectTo is a hook that answers with a redirect to somewhere else.
func redirectTo(target string, code int) func(http.ResponseWriter, *http.Request, int) bool {
	return func(w http.ResponseWriter, r *http.Request, n int) bool {
		w.Header().Set("Location", target+"/landed")
		w.WriteHeader(code)
		return true
	}
}

func TestN5_ARedirectIsNeverFollowed(t *testing.T) {
	for _, c := range []struct {
		name  string
		code  int
		phase nodetest.Phase
		set   func(fe *farEnd, h func(http.ResponseWriter, *http.Request, int) bool)
	}{
		{"plan", 302, "", func(fe *farEnd, h func(http.ResponseWriter, *http.Request, int) bool) { fe.PlanHook = h }},
		{"ping", 302, nodetest.PhaseLatency, func(fe *farEnd, h func(http.ResponseWriter, *http.Request, int) bool) { fe.PingHook = h }},
		{"dl", 301, nodetest.PhaseDownload, func(fe *farEnd, h func(http.ResponseWriter, *http.Request, int) bool) { fe.DLHook = h }},
		{"ul", 307, nodetest.PhaseUpload, func(fe *farEnd, h func(http.ResponseWriter, *http.Request, int) bool) { fe.ULHook = drained(h) }},
	} {
		t.Run(c.name, func(t *testing.T) {
			target, hits := redirectTarget(t)
			fe := newFarEnd(t, shortPlan())
			c.set(fe, redirectTo(target, c.code))
			fe.start()
			res := fe.runFull(t)
			wantFail(t, res, nodetest.CodeNotDyno, c.phase)
			if n := hits(); n != 0 {
				t.Errorf("the redirect's target got %d requests", n)
			}
			if n := len(on(fe.recs(), "/landed")); n != 0 {
				t.Errorf("/landed got %d requests", n)
			}
		})
	}
}

func TestN6_APlanThatNeverComesTimesOutAtTheDeadline(t *testing.T) {
	fe := newFarEnd(t, shortPlan())
	fe.PlanHook = func(w http.ResponseWriter, r *http.Request, n int) bool { fe.block(r); return true }
	fe.start()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	res := run(ctx, fe.options(), nil)
	wantFail(t, res, nodetest.CodeTimeout, "")
	if res.Took < 900*time.Millisecond || res.Took > 2*time.Second {
		t.Errorf("returned after %v, want about the 1 s deadline and within 2 s", res.Took)
	}
}

func TestN7_EveryRequestIsHTTP11EvenFromAnHTTP2FarEnd(t *testing.T) {
	fe := newFarEnd(t, shortPlan())
	fe.TLS = true
	fe.start()
	// Control: this far end does speak HTTP/2 to a client that asks for it.
	resp, err := fe.srv.Client().Get(fe.URL + "/ping")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.ProtoMajor != 2 {
		t.Fatalf("the far end answered %s to a client that offers HTTP/2, so this test proves nothing", resp.Proto)
	}
	fe.reset()

	res := fe.runFull(t)
	mustPass(t, res)
	rs := fe.recs()
	for _, p := range []string{"/api/v1/plan", "/ping", "/dl", "/ul"} {
		if len(on(rs, p)) == 0 {
			t.Errorf("no request reached %s", p)
		}
	}
	for _, r := range rs {
		if r.Proto != "HTTP/1.1" {
			t.Errorf("%s %s arrived as %s, want HTTP/1.1", r.Method, r.Path, r.Proto)
		}
	}
	noCompression(t, rs)
}

func TestN8_NoRequestAsksForCompression(t *testing.T) {
	noCompression(t, theBaseline(t).recs)
}

// Plan values past their limits (N9). The ceilings first: the far end asks
// for 64 streams, a billion samples and terabyte requests.
func TestN9_PlanValuesAreClampedDownToTheirCeilings(t *testing.T) {
	p := shortPlan()
	p.Latency.IdleSamples = 100000
	p.Download = transferDoc{Streams: 64, DurationMs: 600, WarmupMs: 150, StartBytes: 1 << 40, MaxBytes: 1 << 41, TargetRequestMs: 100}
	p.Upload = transferDoc{Streams: 64, DurationMs: 600, WarmupMs: 150, StartBytes: 64 << 10, MaxBytes: 1 << 20, TargetRequestMs: 100}
	fe := newFarEnd(t, p)
	fe.DLMbps = 100
	fe.DLDelay, fe.ULHold = 60*time.Millisecond, 60*time.Millisecond // so every stream's request overlaps the others'
	fe.start()
	res := fe.runFull(t)
	mustPass(t, res)
	rs := fe.recs()

	if got := fe.peakInflight("/dl"); got != nodetest.MaxStreams {
		t.Errorf("most /dl in flight at once = %d, want %d (plan asked for 64)", got, nodetest.MaxStreams)
	}
	if got := fe.peakInflight("/ul"); got != nodetest.MaxStreams {
		t.Errorf("most /ul in flight at once = %d, want %d (plan asked for 64)", got, nodetest.MaxStreams)
	}
	if got := res.Out.Submission.Streams; got != nodetest.MaxStreams {
		t.Errorf("Submission.Streams = %d, want %d", got, nodetest.MaxStreams)
	}
	var biggest int64
	for _, r := range on(rs, "/dl") {
		biggest = max(biggest, r.size())
	}
	if biggest != 1<<30 {
		t.Errorf("the biggest /dl asked for %d bytes, want 1 GiB (the plan's %d cut to the ceiling)", biggest, int64(1)<<40)
	}
	d := parseDetail(t, res.Out)
	if len(d.Idle) != 100 {
		t.Errorf("%d idle samples, want 100 (the plan asked for 100000)", len(d.Idle))
	}
	if n := pingsBefore(rs, "/ping", latencyEnd(res, rs, "/dl")); n != 101 {
		t.Errorf("%d pings before the first /dl, want 101 (one, then 100 samples)", n)
	}
}

// And the floors: zero streams and samples, a 10 ms direction, a 1-byte
// start, a 1 ms target and a 1 ms probe interval.
func TestN9_PlanValuesAreClampedUpToTheirFloors(t *testing.T) {
	p := shortPlan()
	p.Latency.IdleSamples, p.Latency.LoadedIntervalMs = 0, 1
	p.Download = transferDoc{Streams: 0, DurationMs: 10, WarmupMs: 0, StartBytes: 1, MaxBytes: 1 << 20, TargetRequestMs: 1}
	p.Upload = transferDoc{Streams: -3, DurationMs: 1000, WarmupMs: 100000, StartBytes: 1, MaxBytes: 1 << 20, TargetRequestMs: 100}
	fe := newFarEnd(t, p)
	fe.DLDelay, fe.ULHold = 40*time.Millisecond, 40*time.Millisecond
	fe.start()
	res := fe.runFull(t)
	mustPass(t, res)
	rs := fe.recs()

	if got := fe.peakInflight("/dl"); got != 1 {
		t.Errorf("most /dl in flight at once = %d, want 1 (plan asked for 0 streams)", got)
	}
	if got := fe.peakInflight("/ul"); got != 1 {
		t.Errorf("most /ul in flight at once = %d, want 1 (plan asked for -3 streams)", got)
	}
	d := parseDetail(t, res.Out)
	if len(d.Idle) != 1 {
		t.Errorf("%d idle samples, want 1 (the plan asked for 0)", len(d.Idle))
	}
	if len(d.Spans) != 2 {
		t.Fatalf("spans = %v, want two", d.Spans)
	}
	if got := d.Spans[0][3] - d.Spans[0][1]; got < 0.4 || got > 0.7 {
		t.Errorf("download lasted %v s in its span, want 0.5 (the plan asked for 10 ms)", got)
	}
	if got := d.Spans[1][3] - d.Spans[1][1]; got < 0.85 || got > 1.15 {
		t.Errorf("upload lasted %v s in its span, want 1", got)
	}
	if got := d.Spans[1][2] - d.Spans[1][1]; got < 0.25 || got > 0.55 {
		t.Errorf("upload warm-up lasted %v s, want 0.4 (40%% of its 1 s; the plan asked for 100 s)", got)
	}
	if first := on(rs, "/dl"); len(first) == 0 || first[0].size() != 1024 {
		t.Errorf("first /dl = %+v, want bytes=1024 (start_bytes 1 raised to 1 KiB)", first)
	}
	if first := on(rs, "/ul"); len(first) == 0 || first[0].Length != 1024 {
		t.Errorf("first /ul = %+v, want a 1024-byte body", first)
	}
	var biggest int64
	for _, r := range on(rs, "/dl") {
		biggest = max(biggest, r.size())
	}
	if biggest < 2048 {
		t.Errorf("/dl never grew past %d bytes: a 40 ms request counted as over a 1 ms target, which should be raised to 100 ms", biggest)
	}
	probes := 0
	for _, r := range on(rs, "/ping") {
		if !r.Start.Before(res.firstAt(nodetest.PhaseDownload)) && r.Start.Before(res.firstAt(nodetest.PhaseUpload)) {
			probes++
		}
	}
	if probes > 14 {
		t.Errorf("%d probes in a 0.5 s direction, want about 10 or fewer (the plan's 1 ms interval raised to 50 ms)", probes)
	}
}

// The upload ceiling: a plan asking for terabyte uploads gets 256 MiB at most.
// The first body's Content-Length shows it as the request opens, so the run
// needn't send it; the direction ends at 500 ms and drops the request.
//
// There is no waitIdle: the client's socket buffers still hold megabytes the
// far end's /ul handler drains at the paced 8 Mbit/s, so it runs on for
// seconds after the run, and the test doesn't need it to finish (Length is
// recorded when the request arrives). The far end's cleanup ends it: close
// shuts the stop channel, which makes the handler's paced read give up, then
// closes the connections and the server, which returns once every handler has.
func TestN9_UploadMaxBytesIsCutTo256MiB(t *testing.T) {
	p := shortPlan()
	p.Upload = transferDoc{Streams: 1, DurationMs: 500, WarmupMs: 100, StartBytes: 1 << 40, MaxBytes: 1 << 41, TargetRequestMs: 100}
	fe := newFarEnd(t, p)
	fe.ULMbps = 8 // the far end reads slowly, so the run moves little
	fe.start()
	res := fe.runFull(t)
	mustPass(t, res)

	var biggest int64
	ul := on(fe.recs(), "/ul")
	for _, r := range ul {
		biggest = max(biggest, r.Length)
	}
	if len(ul) == 0 || biggest != 256<<20 {
		t.Errorf("the biggest /ul body is %d bytes over %d requests, want 256 MiB (%d): the plan's 1<<40 cut to the ceiling", biggest, len(ul), int64(256<<20))
	}
}

// Long: loaded_interval_ms is cut to 5000 and target_request_ms to 10000.
// One download stream at 1 MB/s asks for 12 MiB, a request of 12.6 s: over
// the 10 s ceiling, so the next request holds its size, though the plan's
// target is 100 s. The plan's probe interval is 100 s, so a 15 s direction
// with probes at about 0, 5 and 10 s shows the 5 s ceiling.
func TestN9_LoadedIntervalAndTargetAreCutToTheirCeilings(t *testing.T) {
	if testing.Short() {
		t.Skip("takes 16 s")
	}
	p := shortPlan()
	p.Latency.LoadedIntervalMs = 100_000
	p.Download = transferDoc{Streams: 1, DurationMs: 15_000, WarmupMs: 500, StartBytes: 12 << 20, MaxBytes: 64 << 20, TargetRequestMs: 100_000}
	fe := newFarEnd(t, p)
	fe.DLMbps = 8
	fe.start()
	res := fe.runUntil(t, fe.options(), nodetest.PhaseUpload, 0)
	rs := fe.recs()

	sizes := dlSizes(rs)
	if len(sizes) < 2 {
		t.Fatalf("%d /dl requests %v, want at least 2 in 15 s", len(sizes), sizes)
	}
	if sizes[0] != 12<<20 || sizes[1] != 12<<20 {
		t.Errorf("first two /dl asked for %d and %d bytes, want both 12 MiB: the first took 12.6 s, over the 10 s ceiling on the target, so the size holds", sizes[0], sizes[1])
	}
	from, till := res.firstAt(nodetest.PhaseDownload), res.firstAt(nodetest.PhaseUpload)
	if till.IsZero() {
		t.Fatalf("the download never ended (err %v)", res.Err)
	}
	probes := 0
	for _, r := range on(rs, "/ping") {
		if !r.Start.Before(from) && r.Start.Before(till) {
			probes++
		}
	}
	if probes < 2 || probes > 4 {
		t.Errorf("%d probes in a 15 s direction, want 2 to 4 (an interval of 5 s, not the plan's 100 s)", probes)
	}
}

// Long: a plan asking for 600 s a direction is cut to 30 s. Only the
// download is watched, so this takes 30 s and not a minute.
func TestN9_ADurationOver30SecondsIsCutTo30Seconds(t *testing.T) {
	if testing.Short() {
		t.Skip("takes 30 s")
	}
	p := shortPlan()
	p.Download = transferDoc{Streams: 2, DurationMs: 600_000, WarmupMs: 1500, StartBytes: 256 << 10, MaxBytes: 4 << 20, TargetRequestMs: 1000}
	fe := newFarEnd(t, p)
	fe.DLMbps = 40
	fe.start()
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	res := run(ctx, fe.options(), func(pa progressAt) {
		if pa.Phase == nodetest.PhaseUpload {
			cancel()
		}
	})
	up := res.firstAt(nodetest.PhaseUpload)
	if up.IsZero() {
		t.Fatalf("the download was still running after %v (err %v); a 600 s plan should be cut to 30 s", res.Took, res.Err)
	}
	if got := up.Sub(res.firstAt(nodetest.PhaseDownload)); got < 29500*time.Millisecond || got > 31*time.Second {
		t.Errorf("the download lasted %v, want 30 s and within 31 s", got)
	}
}
