package nodetest

import (
	"context"
	"io"
	"net/http"
	"net/http/httptrace"
	"net/url"
	"sync/atomic"
	"time"
)

// warmEvery is the gap between warm-up pings: often enough to keep a path
// busy while it settles, rare enough to cost the far end nothing.
const warmEvery = 50 * time.Millisecond

// maxPingBody is the most of a probe's reply that is read. A dyno's is empty.
const maxPingBody = 64 << 10

// latency runs the warm-up, if there is one, then docs/api.md's idle latency:
// one ping that opens the probe connection and isn't a sample, then the
// plan's idle_samples pings one after another.
func (r *runner) latency() error {
	if r.warm > 0 {
		r.report(Progress{Phase: PhaseWarm})
		end := time.Now().Add(r.warm)
		for time.Now().Before(end) {
			ms, err := r.ping(r.ctx)
			if err != nil {
				return classify(r.ctx, r.host, PhaseWarm, err)
			}
			r.report(Progress{Phase: PhaseWarm, Ms: ms})
			if !sleep(r.ctx, min(warmEvery, time.Until(end))) {
				return classify(r.ctx, r.host, PhaseWarm, r.ctx.Err())
			}
		}
	}
	r.report(Progress{Phase: PhaseLatency})
	for i := 0; i <= r.plan.idleSamples; i++ {
		ms, err := r.ping(r.ctx)
		if err != nil {
			return classify(r.ctx, r.host, PhaseLatency, err)
		}
		if i == 0 {
			continue
		}
		r.idle = append(r.idle, ms)
		r.detail.Idle = appendCapped(r.detail.Idle, [2]float64{round1(r.since()), round1(ms)})
		r.report(Progress{Phase: PhaseLatency, Ms: ms})
	}
	return nil
}

// ping is one latency probe on the probe client's connection, in
// milliseconds: from the request being written to the first byte of the
// reply, so opening a connection is never counted. A probe that takes longer
// than the run's PingTimeout is a CodeTimeout error with no phase yet. The
// error is the raw one when ctx ended it.
func (r *runner) ping(ctx context.Context) (float64, error) {
	pctx, cancel := context.WithTimeout(ctx, r.pingTimeout)
	defer cancel()

	// The hooks run on the transport's goroutines, not this one.
	var wrote, first atomic.Int64
	began := time.Now()
	trace := &httptrace.ClientTrace{}
	trace.WroteRequest = func(httptrace.WroteRequestInfo) { wrote.Store(int64(time.Since(began)) + 1) }
	trace.GotFirstResponseByte = func() { first.Store(int64(time.Since(began)) + 1) }

	u := *r.plan.ping
	u.RawQuery = "r=" + r.bust()
	req, err := newRequest(httptrace.WithClientTrace(pctx, trace), http.MethodGet, &u, nil)
	if err != nil {
		return 0, err
	}
	resp, err := r.probe.Do(req)
	if err != nil {
		return 0, r.pingFailed(ctx, pctx, err)
	}
	defer resp.Body.Close()
	wall := time.Since(began)
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return 0, statusError("", r.host, resp.StatusCode)
	}
	if _, err := io.Copy(io.Discard, io.LimitReader(resp.Body, maxPingBody)); err != nil {
		return 0, r.pingFailed(ctx, pctx, err)
	}

	took := wall // when the hooks didn't both run, wall time from the request
	if w, f := wrote.Load(), first.Load(); w > 0 && f > w {
		took = time.Duration(f - w)
	}
	return float64(took) / float64(time.Millisecond), nil
}

// pingFailed is err as ping returns it: a CodeTimeout error when the probe's
// own limit ended it, the raw error otherwise.
func (r *runner) pingFailed(ctx, pctx context.Context, err error) error {
	if ctx.Err() == nil && pctx.Err() != nil {
		return newError(CodeTimeout, "", r.host, "a ping took longer than "+r.pingTimeout.String(), err)
	}
	return err
}

// newRequest makes a request that asks for no compression and says who is
// asking.
func newRequest(ctx context.Context, method string, u *url.URL, body io.Reader) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, method, u.String(), body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept-Encoding", "identity")
	req.Header.Set("User-Agent", userAgent)
	return req, nil
}

// sleep waits d and reports false if ctx ended first.
func sleep(ctx context.Context, d time.Duration) bool {
	if d <= 0 {
		return ctx.Err() == nil
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return true
	case <-ctx.Done():
		return false
	}
}
