package nodetest

import (
	"encoding/json"
	"math"
	"sync"
	"time"

	"github.com/redlinelabs-dev/dyno/server/internal/result"
	"github.com/redlinelabs-dev/dyno/server/internal/stats"
)

// lagTick is how often the timer-lag meter's timer fires.
const lagTick = 50 * time.Millisecond

// lagLimited is the median lag in milliseconds that marks this machine, not
// the network, as the bottleneck (ADR-0003 rule 7).
const lagLimited = 20.0

// runDetail is Submission.Detail: the browser's detail object (docs/api.md,
// ADR-0012). Times are seconds from the start of the run. Rate and probes
// carry a direction last, 0 for download and 1 for upload; spans are
// [dir, start, counted, end].
type runDetail struct {
	Idle             [][2]float64 `json:"idle"`
	Rate             [][3]float64 `json:"rate"`
	Probes           [][3]float64 `json:"probes"`
	Spans            [][4]float64 `json:"spans"`
	UploadFromClient bool         `json:"upload_from_client,omitempty"`
	TimerLagMs       float64      `json:"timer_lag_ms"`
}

// newDetail makes the detail for a run of plan, with room for every sample
// the plan's durations allow and no more: a run's samples are capped by
// appendCapped, whatever the far end does.
func newDetail(p *farPlan) runDetail {
	both := p.down.duration + p.up.duration
	var d runDetail
	d.Idle = make([][2]float64, 0, p.idleSamples)
	d.Rate = make([][3]float64, 0, int(both/rateEvery)+32)
	d.Probes = make([][3]float64, 0, int(both/p.loadedEvery)+32)
	d.Spans = make([][4]float64, 0, 2)
	return d
}

// appendCapped appends v to s unless s is full. The slices it is used on were
// made with room for what a run can produce, so a full one means a bug or a
// clock gone wrong, and growing it would only hide that.
func appendCapped[T any](s []T, v T) []T {
	if len(s) == cap(s) {
		return s
	}
	return append(s, v)
}

// outcome is the finished run as docs/api.md's result: speeds and latencies
// rounded to 0.1 ms or Mbit/s, jitter to 0.01 ms, and the detail the chart
// reads.
func (r *runner) outcome(down, up legResult, lagMs float64) Outcome {
	r.detail.TimerLagMs = round1(lagMs)
	r.detail.UploadFromClient = up.fromClient

	var sub result.Submission
	sub.DownloadMbps = round1(down.mbps)
	sub.UploadMbps = round1(up.mbps)
	sub.IdleMs = round1(stats.Median(r.idle))
	sub.LoadedDownMs = round1(down.loadedMs)
	sub.LoadedUpMs = round1(up.loadedMs)
	sub.JitterMs = round2(stats.Jitter(r.idle))
	sub.Streams = r.plan.down.streams
	sub.ClientLimited = lagMs > lagLimited
	if raw, err := json.Marshal(r.detail); err == nil {
		sub.Detail = raw
	}
	return Outcome{Submission: sub, FarEndName: r.plan.name, FarEndVersion: r.plan.version}
}

// round1 rounds v to one decimal; round2 to two. A value that isn't a finite
// number becomes 0, so the detail always encodes.
func round1(v float64) float64 { return roundTo(v, 10) }
func round2(v float64) float64 { return roundTo(v, 100) }

func roundTo(v, scale float64) float64 {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return 0
	}
	return math.Round(v*scale) / scale
}

// lagMeter measures how late a 50 ms timer fires while the transfers run.
// When this process can't keep up, the machine's CPU, not the network, is
// likely what limited the result (ADR-0003 rule 7).
type lagMeter struct {
	stop chan struct{}
	done chan struct{}
	once sync.Once
	lags []float64
}

// startLagMeter starts the meter. It keeps at most capacity samples, and
// finish ends it.
func startLagMeter(capacity int) *lagMeter {
	m := &lagMeter{stop: make(chan struct{}), done: make(chan struct{})}
	m.lags = make([]float64, 0, capacity)
	go m.run()
	return m
}

func (m *lagMeter) run() {
	defer close(m.done)
	tick := time.NewTicker(lagTick)
	defer tick.Stop()
	last := time.Now()
	for {
		select {
		case <-m.stop:
			return
		case <-tick.C:
			now := time.Now()
			m.lags = appendCapped(m.lags, max(0, float64(now.Sub(last)-lagTick)/float64(time.Millisecond)))
			last = now
		}
	}
}

// finish stops the meter, waits for it, and returns the median lateness in
// milliseconds, 0 if it took no sample. It can be called more than once.
func (m *lagMeter) finish() float64 {
	m.once.Do(func() { close(m.stop) })
	<-m.done
	if len(m.lags) == 0 {
		return 0
	}
	return stats.Quantile(m.lags, 0.5)
}
