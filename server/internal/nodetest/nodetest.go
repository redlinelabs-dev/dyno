// Package nodetest runs a speed test from this dyno against another one, the
// far end, the way docs/api.md "Running a test" tells any client to. Scheduled
// tests use it (ADR-0019), so their results compare with a browser's: the
// same plan, the same rules (ADR-0003) and the same arithmetic (package stats).
package nodetest

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/hex"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/redlinelabs-dev/dyno/server/internal/result"
)

// Kind is how much of the far end's plan a run follows.
type Kind string

const (
	Full  Kind = "full"  // the far end's plan as it is
	Light Kind = "light" // at most LightDuration per direction and MaxBytes each way, for frequent runs
)

// Defaults.
const (
	LightDuration      = 5 * time.Second  // a light run's longest direction
	LightWarmup        = time.Second      // a light run's warm-up per direction
	LightMaxBytes      = 100 << 20        // bytes a light run moves each way, unless Options.MaxBytes says otherwise
	DefaultWarm        = 5 * time.Second  // pings before idle latency, so a tailnet path settles (ADR-0019)
	DefaultPingTimeout = 10 * time.Second // the longest one latency probe may take
	MaxStreams         = 5                // load streams per direction, whatever the plan asks (ADR-0003)
)

// Options says what to test against and how.
type Options struct {
	FarEnd      string         // absolute http or https URL of the far end, such as "http://tailmeadow:3966"
	Kind        Kind           // "" means Full
	MaxBytes    int64          // Light only: bytes per direction; 0 means LightMaxBytes
	Warm        time.Duration  // 0 means DefaultWarm; negative means no warm-up beyond the plan's one ping
	PingTimeout time.Duration  // 0 means DefaultPingTimeout
	TLS         *tls.Config    // for https far ends; nil means the system's roots
	Progress    func(Progress) // optional; never called concurrently with itself

	tune func(*net.Dialer) // tests only: changes how connections are dialed
}

// Phase is a stage of a run.
type Phase string

const (
	PhaseWarm     Phase = "warm"
	PhaseLatency  Phase = "latency"
	PhaseDownload Phase = "download"
	PhaseUpload   Phase = "upload"
	PhaseDone     Phase = "done"
)

// Progress is one live update from a running test.
type Progress struct {
	Phase Phase
	T     float64 // seconds since Run started
	Mbps  float64 // live rate of the direction running now; 0 outside transfers
	Ms    float64 // the latency sample this update reports; 0 if none
}

// Outcome is a finished test.
type Outcome struct {
	Submission    result.Submission // ready to store; TestID, Client and Device are empty
	FarEndName    string            // the far end plan's server.name
	FarEndVersion string            // the far end plan's server.version
}

// Error codes: why a run failed, in words that say which end to look at.
const (
	CodeBadFarEnd   = "bad_far_end"   // FarEnd isn't an absolute http or https URL; nothing was sent
	CodeUnreachable = "unreachable"   // no connection: name lookup failed, refused, or no route
	CodeTimeout     = "timeout"       // connected, but a reply didn't come in time, or ctx's deadline passed
	CodeNotDyno     = "not_dyno"      // something answered that isn't dyno: a plan or reply that doesn't parse, or a redirect
	CodeHTTP        = "http_status"   // the far end answered a test request with an error status
	CodeStream      = "stream_failed" // a transfer broke off part way
	CodeCanceled    = "canceled"      // ctx was canceled
)

// Error is why a run failed. Run returns it as a *Error. Its message
// (errors.go) is one plain sentence naming the phase.
type Error struct {
	Code  string // one of the Code constants
	Phase Phase  // the stage that failed; "" before the plan was read
	Err   error  // the underlying error
}

func (e *Error) Unwrap() error { return e.Err }

// Run tests against o.FarEnd and returns the result, or a *Error saying why
// it failed. A failed run returns no result. Run returns promptly once ctx is
// done, with every request it started finished or abandoned.
//
// The steps are docs/api.md's: read the plan and clamp it; ping to warm the
// path and sample idle latency; run download, then upload, each with probes
// alongside; work the figures out with package stats. Progress is called only
// on the goroutine that called Run.
func Run(ctx context.Context, o Options) (Outcome, error) {
	base, err := parseFarEnd(o.FarEnd)
	if err != nil {
		return Outcome{}, err
	}
	r := newRunner(ctx, o, base)
	defer r.close()

	plan, err := fetchPlan(ctx, r.probe, base)
	if err != nil {
		return Outcome{}, err
	}
	if r.light {
		plan.lighten()
	}
	r.plan = plan
	r.detail = newDetail(plan)

	if err := r.latency(); err != nil {
		return Outcome{}, err
	}
	if err := r.makeBlock(); err != nil {
		return Outcome{}, err
	}

	lag := startLagMeter(int((plan.down.duration+plan.up.duration)/lagTick) + 16)
	defer lag.finish()
	down, err := r.direction(0)
	if err != nil {
		return Outcome{}, err
	}
	up, err := r.direction(1)
	if err != nil {
		return Outcome{}, err
	}
	lagMs := lag.finish()
	r.report(Progress{Phase: PhaseDone})
	return r.outcome(down, up, lagMs), nil
}

// FarEndInfo is what a far end's plan says about itself.
type FarEndInfo struct {
	Name    string // the plan's server.name
	Version string // the plan's server.version
}

// Check reads o.FarEnd's plan and reports what answered, without running a
// test: one GET of the plan and nothing else. It uses only FarEnd and TLS
// from o, follows no redirects, and fails the way Run's first step does
// (CodeBadFarEnd, CodeUnreachable, CodeTimeout, CodeNotDyno, CodeCanceled),
// with Phase "". Schedules call it before they save a far end (ADR-0028).
func Check(ctx context.Context, o Options) (FarEndInfo, error) {
	base, err := parseFarEnd(o.FarEnd)
	if err != nil {
		return FarEndInfo{}, err
	}
	hc := newClient(o.TLS, 1, 0, o.tune)
	defer hc.CloseIdleConnections()
	plan, err := fetchPlan(ctx, hc, base)
	if err != nil {
		return FarEndInfo{}, err
	}
	return FarEndInfo{Name: plan.name, Version: plan.version}, nil
}

const (
	// dialTimeout is how long a connection may take to open. It is under
	// planTimeout and DefaultPingTimeout, so a far end that never answers the
	// connection fails as the dial, "unreachable" (docs/api.md: no connection),
	// and not as whichever 10 s timer happened to fire first.
	dialTimeout = 5 * time.Second
	userAgent   = "dyno"
)

// blockSize is the size of the random block upload bodies repeat. It is
// larger than any compression window a proxy on the path is likely to use,
// as the server's own download payload is (package measure).
const blockSize = 4 << 20

// runner is one Run: its clients, its plan, and what it has measured. Its
// fields are for the goroutine that called Run, except those the streams
// read: base, host, plan, block, salt and seq.
type runner struct {
	ctx  context.Context
	opts Options
	base *url.URL
	host string
	t0   time.Time

	probe *http.Client
	load  *http.Client

	plan        *farPlan
	light       bool
	maxBytes    int64
	warm        time.Duration
	pingTimeout time.Duration

	salt  string
	seq   atomic.Uint64
	block []byte

	idle   []float64
	detail runDetail
}

// newRunner makes the clients and the settings a run needs. The probe client
// carries the plan and the latency probes; the load client carries every
// stream. Their connections are apart, so a probe never queues behind a
// transfer (ADR-0003).
func newRunner(ctx context.Context, o Options, base *url.URL) *runner {
	r := &runner{ctx: ctx, opts: o, base: base, host: base.Host, t0: time.Now()}
	r.probe = newClient(o.TLS, 1, 0, o.tune)
	r.load = newClient(o.TLS, MaxStreams, MaxStreams, o.tune)
	r.light = o.Kind == Light
	r.maxBytes = o.MaxBytes
	if r.maxBytes <= 0 {
		r.maxBytes = LightMaxBytes
	}
	if o.Warm == 0 {
		r.warm = DefaultWarm
	} else if o.Warm > 0 {
		r.warm = o.Warm
	}
	r.pingTimeout = o.PingTimeout
	if r.pingTimeout <= 0 {
		r.pingTimeout = DefaultPingTimeout
	}
	var salt [6]byte
	rand.Read(salt[:])
	r.salt = hex.EncodeToString(salt[:])
	return r
}

// close closes the connections the run opened. Requests still in flight were
// canceled by their contexts, which closes their connections.
func (r *runner) close() {
	r.probe.CloseIdleConnections()
	r.load.CloseIdleConnections()
}

// since is the seconds since Run started.
func (r *runner) since() float64 { return time.Since(r.t0).Seconds() }

// report sends Progress an update, stamped with the time. It is called only
// on the goroutine that called Run, which is what keeps Progress from being
// called twice at once.
func (r *runner) report(p Progress) {
	if r.opts.Progress == nil {
		return
	}
	p.T = r.since()
	r.opts.Progress(p)
}

// bust is a cache-busting value no other request of this run, or of another
// run, carries.
func (r *runner) bust() string {
	return r.salt + strconv.FormatUint(r.seq.Add(1), 36)
}

// makeBlock makes the random block every upload body is cut from, once.
func (r *runner) makeBlock() error {
	b := make([]byte, blockSize)
	if _, err := rand.Read(b); err != nil {
		return fmt.Errorf("nodetest: making the upload payload: %w", err)
	}
	r.block = b
	return nil
}

// newClient makes an HTTP/1.1 client that follows no redirects and asks for
// no compression (ADR-0003). idle is how many idle connections it keeps and
// limit, when above 0, how many connections it opens at most. tune, when not
// nil, may change the dialer; tests use it.
func newClient(tlsConf *tls.Config, idle, limit int, tune func(*net.Dialer)) *http.Client {
	tr := &http.Transport{}
	dialer := &net.Dialer{Timeout: dialTimeout, KeepAlive: 15 * time.Second}
	if tune != nil {
		tune(dialer)
	}
	tr.DialContext = dialer.DialContext
	if tlsConf != nil {
		tr.TLSClientConfig = tlsConf.Clone()
	}
	tr.TLSHandshakeTimeout = 10 * time.Second
	tr.ForceAttemptHTTP2 = false
	tr.TLSNextProto = map[string]func(string, *tls.Conn) http.RoundTripper{}
	tr.DisableCompression = true
	tr.MaxIdleConns = idle
	tr.MaxIdleConnsPerHost = idle
	tr.MaxConnsPerHost = limit
	tr.IdleConnTimeout = 30 * time.Second
	tr.MaxResponseHeaderBytes = 64 << 10
	hc := &http.Client{}
	hc.Transport = tr
	hc.CheckRedirect = noRedirect
	return hc
}

// noRedirect hands back a redirect's own reply instead of following it, so a
// far end can't send the run to another address.
func noRedirect(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
