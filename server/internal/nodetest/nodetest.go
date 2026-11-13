// Package nodetest runs a speed test from this dyno against another one, the
// far end, the way docs/api.md "Running a test" tells any client to. Scheduled
// tests use it (ADR-0019), so their results compare with a browser's: the
// same plan, the same rules (ADR-0003) and the same arithmetic (package stats).
package nodetest

import (
	"context"
	"crypto/tls"
	"errors"
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

// Error is why a run failed. Run returns it as a *Error.
type Error struct {
	Code  string // one of the Code constants
	Phase Phase  // the stage that failed; "" before the plan was read
	Err   error  // the underlying error
}

func (e *Error) Error() string { panic("not implemented") }
func (e *Error) Unwrap() error { return e.Err }

// Run tests against o.FarEnd and returns the result, or a *Error saying why
// it failed. A failed run returns no result. Run returns promptly once ctx is
// done, with every request it started finished or abandoned.
func Run(ctx context.Context, o Options) (Outcome, error) {
	return Outcome{}, errors.New("not implemented")
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
	return FarEndInfo{}, errors.New("not implemented")
}
