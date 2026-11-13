// Package stats is the measurement arithmetic every dyno engine shares. It is
// web/src/stats.ts in Go, function for function, so a scheduled test run by
// the server and a browser's test are worked out the same way (ADR-0003).
package stats

// Mean of a; NaN when a is empty.
func Mean(a []float64) float64 { panic("not implemented") }

// Median of a; NaN when a is empty. a is not reordered.
func Median(a []float64) float64 { panic("not implemented") }

// Jitter as Cloudflare defines it: the mean absolute difference of consecutive
// samples. 0 for fewer than two samples.
func Jitter(samples []float64) float64 { panic("not implemented") }

// Mbps is megabits per second for bytes moved in seconds; 0 when seconds <= 0.
func Mbps(bytes, seconds float64) float64 { panic("not implemented") }

// Quantile is the value at quantile q (0..1) by nearest rank; NaN when a is
// empty. a is not reordered.
func Quantile(a []float64, q float64) float64 { panic("not implemented") }

// Buckets counts bytes per fixed-width time bucket, so the live rate and the
// steady-state rate both come from the same counts.
type Buckets struct {
	Width  float64   // seconds per bucket
	Counts []float64 // bytes per bucket; bucket i covers [i*Width, (i+1)*Width)
}

// NewBuckets returns empty buckets of width seconds.
func NewBuckets(width float64) *Buckets { panic("not implemented") }

// Add counts bytes arriving at t seconds.
func (b *Buckets) Add(t, bytes float64) { panic("not implemented") }

// Rate is Mbit/s over [from, to) seconds, whole buckets only. Buckets nothing
// arrived in count as zero, trailing ones included. 0 when no whole bucket fits.
func (b *Buckets) Rate(from, to float64) float64 { panic("not implemented") }

// UploadReq is one finished upload request: the server's byte count, timed
// on the client's clock.
type UploadReq struct {
	Start float64 // seconds since the direction began, when the request was opened
	End   float64 // when the server's reply arrived, so every byte had been received
	Bytes float64 // bytes the server says it received
}

// UploadBytes is the bytes the server received in [from, to), taking each
// request's bytes as spread evenly over its time on the wire. A request that
// straddles a window edge counts in part.
func UploadBytes(streams [][]UploadReq, from, to float64) float64 { panic("not implemented") }

// UploadKnown is how far into the direction the server's count is complete:
// the earliest of the streams' last replies. ok is false until a request has
// finished.
func UploadKnown(streams [][]UploadReq) (known float64, ok bool) { panic("not implemented") }

// UploadRate is upload speed from the server's counts (ADR-0003), worked out
// as stats.ts's uploadRate is: every stream's bytes between the end of warmup
// and UploadKnown over that one window, or, when spreading doesn't hold,
// every byte over the span from the first request to the last reply. ok is
// false when no request finished.
func UploadRate(streams [][]UploadReq, warmup float64) (mbps float64, ok bool) {
	panic("not implemented")
}

// UploadSeries is the upload chart, a [seconds, Mbit/s] sample every step,
// worked out as stats.ts's uploadSeries is. Empty when no request finished.
func UploadSeries(streams [][]UploadReq, warmup, step, window float64) [][2]float64 {
	panic("not implemented")
}
