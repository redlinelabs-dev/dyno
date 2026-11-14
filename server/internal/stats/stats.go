// Package stats is the measurement arithmetic every dyno engine shares. It is
// web/src/stats.ts in Go, function for function, so a scheduled test run by
// the server and a browser's test are worked out the same way (ADR-0003).
package stats

import (
	"math"
	"slices"
)

// maxBuckets caps how many buckets Add will make, so a stray timestamp can't
// ask for gigabytes.
const maxBuckets = 1 << 16

// maxSeries caps the samples UploadSeries makes, for the same reason.
const maxSeries = 1 << 16

// Mean of a; NaN when a is empty.
func Mean(a []float64) float64 {
	if len(a) == 0 {
		return math.NaN()
	}
	sum := 0.0
	for _, v := range a {
		sum += v
	}
	return sum / float64(len(a))
}

// sorted is a sorted copy of a, so Median and Quantile leave their input as
// it was.
func sorted(a []float64) []float64 {
	s := slices.Clone(a)
	slices.Sort(s)
	return s
}

// Median of a; NaN when a is empty. a is not reordered.
func Median(a []float64) float64 {
	if len(a) == 0 {
		return math.NaN()
	}
	s := sorted(a)
	m := len(s) / 2
	if len(s)%2 == 1 {
		return s[m]
	}
	return (s[m-1] + s[m]) / 2
}

// Jitter as Cloudflare defines it: the mean absolute difference of consecutive
// samples. 0 for fewer than two samples.
func Jitter(samples []float64) float64 {
	if len(samples) < 2 {
		return 0
	}
	sum := 0.0
	for i := 1; i < len(samples); i++ {
		sum += math.Abs(samples[i] - samples[i-1])
	}
	return sum / float64(len(samples)-1)
}

// Mbps is megabits per second for bytes moved in seconds; 0 when seconds <= 0.
func Mbps(bytes, seconds float64) float64 {
	if seconds > 0 {
		return bytes * 8 / seconds / 1e6
	}
	return 0
}

// Quantile is the value at quantile q (0..1) by nearest rank; NaN when a is
// empty. a is not reordered.
func Quantile(a []float64, q float64) float64 {
	if len(a) == 0 || math.IsNaN(q) {
		return math.NaN()
	}
	s := sorted(a)
	i := math.Min(float64(len(s)-1), math.Max(0, math.Ceil(q*float64(len(s)))-1))
	return s[int(i)]
}

// Buckets counts bytes per fixed-width time bucket, so the live rate and the
// steady-state rate both come from the same counts.
type Buckets struct {
	Width  float64   // seconds per bucket
	Counts []float64 // bytes per bucket; bucket i covers [i*Width, (i+1)*Width)
}

// NewBuckets returns empty buckets of width seconds.
func NewBuckets(width float64) *Buckets { return &Buckets{Width: width} }

// Add counts bytes arriving at t seconds. A time before 0 counts in the first
// bucket; a time that isn't a number, or that is past the last bucket Add will
// make, counts nowhere.
func (b *Buckets) Add(t, bytes float64) {
	if !(b.Width > 0) || math.IsNaN(t) {
		return
	}
	i := math.Max(0, math.Floor(t/b.Width))
	if i >= maxBuckets {
		return
	}
	n := int(i)
	for len(b.Counts) <= n {
		b.Counts = append(b.Counts, 0)
	}
	b.Counts[n] += bytes
}

// Rate is Mbit/s over [from, to) seconds, whole buckets only. Buckets nothing
// arrived in count as zero, trailing ones included. 0 when no whole bucket fits.
func (b *Buckets) Rate(from, to float64) float64 {
	if !(b.Width > 0) || math.IsNaN(from) || math.IsNaN(to) {
		return 0
	}
	lo := math.Max(0, math.Ceil(from/b.Width-1e-9))
	hi := math.Floor(to/b.Width + 1e-9)
	if !(hi > lo) {
		return 0
	}
	have := float64(len(b.Counts))
	sum := 0.0
	for _, c := range b.Counts[int(min(lo, have)):int(min(hi, have))] {
		sum += c
	}
	return Mbps(sum, (hi-lo)*b.Width)
}

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
func UploadBytes(streams [][]UploadReq, from, to float64) float64 {
	sum := 0.0
	for _, reqs := range streams {
		for _, r := range reqs {
			overlap := min(r.End, to) - max(r.Start, from)
			if overlap <= 0 {
				continue
			}
			if r.End > r.Start {
				sum += r.Bytes * overlap / (r.End - r.Start)
			} else {
				sum += r.Bytes
			}
		}
	}
	return sum
}

// UploadKnown is how far into the direction the server's count is complete:
// the earliest of the streams' last replies. ok is false until a request has
// finished.
func UploadKnown(streams [][]UploadReq) (known float64, ok bool) {
	for _, reqs := range streams {
		if len(reqs) == 0 {
			continue
		}
		if end := reqs[len(reqs)-1].End; !ok || end < known {
			known, ok = end, true
		}
	}
	return known, ok
}

// UploadRate is upload speed from the server's counts (ADR-0003), worked out
// as stats.ts's uploadRate is: every stream's bytes between the end of warmup
// and UploadKnown over that one window, or, when spreading doesn't hold,
// every byte over the span from the first request to the last reply. ok is
// false when no request finished.
func UploadRate(streams [][]UploadReq, warmup float64) (mbps float64, ok bool) {
	known, ok := UploadKnown(streams)
	if !ok {
		return 0, false
	}
	if spreads(streams, warmup, known) {
		return Mbps(UploadBytes(streams, warmup, known), known-warmup), true
	}
	return confirmed(streams, lastEnd(streams))
}

// UploadSeries is the upload chart, a [seconds, Mbit/s] sample every step,
// worked out as stats.ts's uploadSeries is. Empty when no request finished.
func UploadSeries(streams [][]UploadReq, warmup, step, window float64) [][2]float64 {
	known, ok := UploadKnown(streams)
	if !ok || !(step > 0) {
		return nil
	}
	spread := spreads(streams, warmup, known)
	last := known
	if !spread {
		last = lastEnd(streams)
	}
	var out [][2]float64
	for i := 1; i <= maxSeries && float64(i)*step <= last+1e-9; i++ {
		t := float64(i) * step
		from := max(0, t-window)
		var v float64
		if spread {
			v = Mbps(UploadBytes(streams, from, t), t-from)
		} else {
			v, _ = confirmed(streams, t)
		}
		out = append(out, [2]float64{t, v})
	}
	return out
}

// spreads reports whether spreading each request's bytes over its time holds
// for the window [warmup, known]: the window is at least a second and no
// request in it lasted more than half of it.
func spreads(streams [][]UploadReq, warmup, known float64) bool {
	window := known - warmup
	longest := 0.0
	for _, reqs := range streams {
		for _, r := range reqs {
			if r.End > warmup && r.Start < known {
				longest = max(longest, r.End-r.Start)
			}
		}
	}
	return window >= 1 && longest <= window/2
}

// lastEnd is when the last reply of any stream arrived.
func lastEnd(streams [][]UploadReq) float64 {
	last := math.Inf(-1)
	for _, reqs := range streams {
		for _, r := range reqs {
			last = max(last, r.End)
		}
	}
	return last
}

// confirmed is the bytes of the requests answered by t, over the time since
// the first one opened, in Mbit/s. ok is false before that time.
func confirmed(streams [][]UploadReq, t float64) (mbps float64, ok bool) {
	first := math.Inf(1)
	for _, reqs := range streams {
		for _, r := range reqs {
			first = min(first, r.Start)
		}
	}
	if !(t > first) {
		return 0, false
	}
	sum := 0.0
	for _, reqs := range streams {
		for _, r := range reqs {
			if r.End <= t {
				sum += r.Bytes
			}
		}
	}
	return Mbps(sum, t-first), true
}
