package nodetest

import (
	"context"
	"encoding/json"
	"io"
	"math"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// The plan is read from the far end, so it is checked and clamped before any
// of it is used: a far end can't make a run unbounded or send it elsewhere.
const (
	planPath      = "/api/v1/plan"
	maxPlanBytes  = 64 << 10
	planTimeout   = 10 * time.Second
	maxTestIDLen  = 128
	maxNameLen    = 128
	minDuration   = 500 * time.Millisecond
	maxDuration   = 30 * time.Second
	maxIdle       = 100
	minLoadedGap  = 50 * time.Millisecond
	maxLoadedGap  = 5 * time.Second
	minTarget     = 100 * time.Millisecond
	maxTarget     = 10 * time.Second
	maxDownBytes  = 1 << 30
	maxUpBytes    = 256 << 20
	minStartBytes = 1 << 10
	minMaxBytes   = 64 << 10
)

// dirPlan is one direction of a plan once clamped.
type dirPlan struct {
	streams    int
	duration   time.Duration
	warmup     time.Duration
	startBytes int64
	maxBytes   int64
	target     time.Duration
}

// farPlan is the far end's plan once checked and clamped.
type farPlan struct {
	testID      string
	name        string
	version     string
	download    *url.URL
	upload      *url.URL
	ping        *url.URL
	idleSamples int
	loadedEvery time.Duration
	down        dirPlan
	up          dirPlan
}

// lighten cuts the plan down to a light run: each direction at most
// LightDuration long, with LightWarmup of warm-up.
func (p *farPlan) lighten() {
	for _, d := range []*dirPlan{&p.down, &p.up} {
		d.duration = min(d.duration, LightDuration)
		d.warmup = min(LightWarmup, d.duration*2/5)
	}
}

// The plan as the far end sends it. Numbers are read as floats so a far end
// that sends a fraction or a huge value is clamped, not refused.
type wirePlan struct {
	TestID    string        `json:"test_id"`
	Server    wireServer    `json:"server"`
	Endpoints wireEndpoints `json:"endpoints"`
	Latency   wireLatency   `json:"latency"`
	Download  wireTransfer  `json:"download"`
	Upload    wireTransfer  `json:"upload"`
}

type wireServer struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

type wireEndpoints struct {
	Download string `json:"download"`
	Upload   string `json:"upload"`
	Ping     string `json:"ping"`
}

type wireLatency struct {
	IdleSamples      float64 `json:"idle_samples"`
	LoadedIntervalMs float64 `json:"loaded_interval_ms"`
}

type wireTransfer struct {
	Streams         float64 `json:"streams"`
	DurationMs      float64 `json:"duration_ms"`
	WarmupMs        float64 `json:"warmup_ms"`
	StartBytes      float64 `json:"start_bytes"`
	MaxBytes        float64 `json:"max_bytes"`
	TargetRequestMs float64 `json:"target_request_ms"`
}

// parseFarEnd reads o.FarEnd: an absolute http or https URL with a host and
// no user info. Its query and fragment are dropped. Nothing is sent.
func parseFarEnd(s string) (*url.URL, error) {
	u, err := url.Parse(s)
	switch {
	case err != nil:
		return nil, newError(CodeBadFarEnd, "", "", "it isn't a valid URL", nil)
	case u.Scheme != "http" && u.Scheme != "https":
		return nil, newError(CodeBadFarEnd, "", "", "it doesn't start with http:// or https://", nil)
	case u.Opaque != "" || u.Hostname() == "":
		return nil, newError(CodeBadFarEnd, "", "", "it names no host", nil)
	case u.User != nil:
		return nil, newError(CodeBadFarEnd, "", "", "it has a user name or password in it", nil)
	}
	u.RawQuery, u.ForceQuery, u.Fragment, u.RawFragment = "", false, "", ""
	return u, nil
}

// notPlan is the error for a reply that isn't a dyno's plan.
func notPlan(host, why string) *Error {
	return newError(CodeNotDyno, "", host, why, nil)
}

// fetchPlan makes the run's first request, GET <base>/api/v1/plan, and checks
// what comes back. It follows no redirect (hc doesn't) and gives up after
// planTimeout. Every failure has no phase. A request that gets no connection
// is unreachable; a reply that is no dyno plan, including one whose body
// breaks off after a 2xx head, is not_dyno; a canceled ctx is canceled and
// running out of time is timeout.
func fetchPlan(ctx context.Context, hc *http.Client, base *url.URL) (*farPlan, error) {
	host := base.Host
	if err := ctx.Err(); err != nil {
		return nil, classify(ctx, host, "", err)
	}
	rctx, cancel := context.WithTimeout(ctx, planTimeout)
	defer cancel()
	u := *base
	u.Path = strings.TrimRight(base.Path, "/") + planPath
	u.RawPath = ""
	req, err := newRequest(rctx, http.MethodGet, &u, nil)
	if err != nil {
		return nil, classify(ctx, host, "", err)
	}
	resp, err := hc.Do(req)
	if err != nil {
		return nil, classify(ctx, host, "", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		if resp.StatusCode >= 300 && resp.StatusCode < 400 {
			return nil, notPlan(host, "it redirected to another address")
		}
		return nil, notPlan(host, "it answered "+statusText(resp.StatusCode)+" to the plan request")
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxPlanBytes+1))
	if err != nil {
		// The head came, so something answered. A canceled ctx and a timeout
		// keep their codes; any other break in the body (cut short, bad
		// chunk framing, a Content-Length longer than the body) is a reply
		// that isn't a plan, never "no connection". The message is ours.
		if ne := classify(ctx, host, "", err); ne.Code != CodeUnreachable {
			return nil, ne
		}
		return nil, notPlan(host, "its plan reply broke off")
	}
	if len(data) > maxPlanBytes {
		return nil, notPlan(host, "its plan is over 64 KiB")
	}
	var wp wirePlan
	if err := json.Unmarshal(data, &wp); err != nil {
		return nil, notPlan(host, "its plan isn't valid JSON")
	}
	return buildPlan(&wp, base)
}

// buildPlan checks a decoded plan and clamps its numbers.
func buildPlan(wp *wirePlan, base *url.URL) (*farPlan, error) {
	host := base.Host
	if wp.TestID == "" {
		return nil, notPlan(host, "its plan has no test_id")
	}
	if len(wp.TestID) > maxTestIDLen {
		return nil, notPlan(host, "its test_id is too long")
	}
	ep := wp.Endpoints
	if ep.Download == "" || ep.Upload == "" || ep.Ping == "" {
		return nil, notPlan(host, "its plan names no download, upload or ping address")
	}
	p := &farPlan{testID: wp.TestID, name: tidy(wp.Server.Name, maxNameLen), version: tidy(wp.Server.Version, maxNameLen)}
	var ok1, ok2, ok3 bool
	p.download, ok1 = resolveEndpoint(base, ep.Download)
	p.upload, ok2 = resolveEndpoint(base, ep.Upload)
	p.ping, ok3 = resolveEndpoint(base, ep.Ping)
	if !ok1 || !ok2 || !ok3 {
		return nil, notPlan(host, "its plan points the test at another address")
	}
	p.idleSamples = int(clamp(wp.Latency.IdleSamples, 1, maxIdle))
	p.loadedEvery = millis(wp.Latency.LoadedIntervalMs, minLoadedGap, maxLoadedGap)
	p.down = clampDirection(wp.Download, maxDownBytes)
	p.up = clampDirection(wp.Upload, maxUpBytes)
	return p, nil
}

// resolveEndpoint resolves a plan's endpoint against the far end the way a
// browser resolves a link. ok is false when it leads anywhere but the same
// scheme and host, so a far end can't send the run to another machine.
func resolveEndpoint(base *url.URL, ref string) (u *url.URL, ok bool) {
	r, err := url.Parse(ref)
	if err != nil {
		return nil, false
	}
	u = base.ResolveReference(r)
	if u.Scheme != base.Scheme || !strings.EqualFold(u.Host, base.Host) || u.User != nil || u.Opaque != "" {
		return nil, false
	}
	u.RawQuery, u.ForceQuery, u.Fragment, u.RawFragment = "", false, "", ""
	return u, true
}

// clampDirection clamps one direction of a plan. maxCap is the most a single
// request may be.
func clampDirection(w wireTransfer, maxCap float64) dirPlan {
	var d dirPlan
	d.streams = int(clamp(w.Streams, 1, MaxStreams))
	d.duration = millis(w.DurationMs, minDuration, maxDuration)
	d.warmup = min(millis(w.WarmupMs, 0, maxDuration), d.duration*2/5)
	maxBytes := clamp(w.MaxBytes, minMaxBytes, maxCap)
	d.maxBytes = int64(maxBytes)
	d.startBytes = int64(clamp(w.StartBytes, minStartBytes, maxBytes))
	d.target = millis(w.TargetRequestMs, minTarget, maxTarget)
	return d
}

// clamp limits v to [lo, hi]; NaN goes to lo.
func clamp(v, lo, hi float64) float64 {
	switch {
	case math.IsNaN(v) || v < lo:
		return lo
	case v > hi:
		return hi
	}
	return v
}

// millis is a plan's milliseconds as a Duration clamped to [lo, hi].
func millis(ms float64, lo, hi time.Duration) time.Duration {
	return time.Duration(clamp(ms, float64(lo/time.Millisecond), float64(hi/time.Millisecond))) * time.Millisecond
}
