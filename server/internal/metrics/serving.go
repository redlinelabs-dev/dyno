package metrics

import "time"

// ServingGrace is how long Serving stays true after the last /dl or /ul
// stream closed: long enough to cover the gap between a test's download and
// its upload.
const ServingGrace = 5 * time.Second

// Serving reports whether this server is serving a test: a /dl or /ul stream
// is open, or the last one closed less than ServingGrace ago. A scheduled run
// waits on it before it starts (ADR-0029). It is false on a nil *Metrics.
//
// It reads the open-stream gauges and one timestamp, both written once per
// stream open and close, so the per-byte path never touches it.
func (m *Metrics) Serving() bool {
	if m == nil {
		return false
	}
	for i := range m.streams {
		if m.streams[i].Load() > 0 {
			return true
		}
	}
	last := m.lastClose.Load()
	return last != 0 && time.Now().UnixNano()-last < int64(ServingGrace)
}
