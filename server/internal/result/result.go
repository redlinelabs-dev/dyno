// Package result defines a stored test result and the figures the server
// derives from it. Deriving them here, not in each client, keeps the web UI
// and a native app from disagreeing about a grade.
package result

import (
	"encoding/json"
	"errors"
	"math"
	"strings"
	"time"

	"github.com/redlinelabs-dev/dyno/server/internal/testrun"
)

// Submission is what a client posts after running the plan.
type Submission struct {
	TestID        string          `json:"test_id"`
	Client        string          `json:"client"`
	Device        string          `json:"device"` // the client's own opaque id for itself, so History can show one device's tests
	DownloadMbps  float64         `json:"download_mbps"`
	UploadMbps    float64         `json:"upload_mbps"`
	IdleMs        float64         `json:"idle_ms"`
	LoadedDownMs  float64         `json:"loaded_down_ms"`
	LoadedUpMs    float64         `json:"loaded_up_ms"`
	JitterMs      float64         `json:"jitter_ms"`
	Streams       int             `json:"streams"`
	ClientLimited bool            `json:"client_limited"`
	Detail        json.RawMessage `json:"detail,omitempty"`
}

// Result is a stored test. Detail (raw samples for the charts) is only
// returned when one result is fetched by id.
type Result struct {
	ID     int64     `json:"id"`
	At     time.Time `json:"at"`
	Source string    `json:"source"`
	Submission
	BufferbloatMs   float64 `json:"bufferbloat_ms"`
	Grade           string  `json:"grade"`
	RPM             int     `json:"rpm"`
	ServerBytesDown int64   `json:"server_bytes_down"`
	ServerBytesUp   int64   `json:"server_bytes_up"`
}

const maxClientLen = 80

// maxRPM caps round trips per minute: 0.06 ms of loaded latency. A smaller
// latency would overflow the int.
const maxRPM = 1_000_000

// MaxDeviceLen caps a device id. Ids are letters, digits, "-" and "_", which
// keeps them safe in a query string without escaping.
const MaxDeviceLen = 64

// ValidDevice reports whether id can be a device id. Empty means no device.
func ValidDevice(id string) bool {
	if len(id) > MaxDeviceLen {
		return false
	}
	for _, c := range []byte(id) {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
			return false
		}
	}
	return true
}

// Validate rejects submissions no honest client sends.
func (s *Submission) Validate() error {
	for _, v := range []float64{s.DownloadMbps, s.UploadMbps, s.IdleMs, s.LoadedDownMs, s.LoadedUpMs, s.JitterMs} {
		if math.IsNaN(v) || math.IsInf(v, 0) || v < 0 || v > 1e6 {
			return errors.New("measurements must be finite, non-negative numbers")
		}
	}
	if s.Streams < 0 || s.Streams > 64 {
		return errors.New("streams out of range")
	}
	if s.TestID != "" && !testrun.ValidID(s.TestID) {
		return errors.New("test_id must be the plan's 24 hex characters, or empty")
	}
	if s.DownloadMbps == 0 && s.UploadMbps == 0 && s.IdleMs == 0 && s.LoadedDownMs == 0 && s.LoadedUpMs == 0 {
		return errors.New("a result needs at least one measurement")
	}
	if !ValidDevice(s.Device) {
		return errors.New("device must be up to 64 letters, digits, - or _")
	}
	if len(s.Client) > maxClientLen {
		s.Client = strings.ToValidUTF8(s.Client[:maxClientLen], "")
	}
	return nil
}

// Derive fills in the figures computed from the submission.
func (r *Result) Derive() {
	busy := max(r.LoadedDownMs, r.LoadedUpMs)
	if busy == 0 {
		return // no loaded latency measured, so nothing to grade
	}
	r.BufferbloatMs = round1(max(0, busy-r.IdleMs))
	r.Grade = Grade(r.BufferbloatMs)
	r.RPM = int(math.Min(math.Round(60_000/busy), maxRPM))
}

// Grade grades the latency added under load on Cloudflare's published
// loaded-latency thresholds of 10, 20, 50, 100 and 500 ms (ADR-0003, ADR-0010).
func Grade(addedMs float64) string {
	switch {
	case addedMs < 10:
		return "A+"
	case addedMs < 20:
		return "A"
	case addedMs < 50:
		return "B"
	case addedMs < 100:
		return "C"
	case addedMs < 500:
		return "D"
	}
	return "F"
}

func round1(v float64) float64 { return math.Round(v*10) / 10 }
