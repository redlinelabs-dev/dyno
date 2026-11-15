package nodetest

import (
	"context"
	"encoding/json"
	"io"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/redlinelabs-dev/dyno/server/internal/stats"
)

// bucketWidth is the seconds per byte-count bucket.
const bucketWidth = 0.1

// rateEvery is how often a running direction reports its live rate: one
// bucket.
const rateEvery = 100 * time.Millisecond

// liveWindow is the seconds the live download rate averages.
const liveWindow = 0.5

// uploadWindow is the same for upload, whose bytes are spread over requests of
// about a second.
const uploadWindow = 1.0

// readBuf is the size of each download stream's one read buffer.
const readBuf = 64 << 10

// minUploadBytes is the smallest upload request after a stream's first.
const minUploadBytes = 64 << 10

// maxReplyBytes is the most of an upload's reply that is read.
const maxReplyBytes = 4 << 10

// maxUploadReqs caps the finished requests one upload stream records, so a
// far end that asks for tiny requests can't grow a run's memory. A stream that
// reaches it stops sending.
const maxUploadReqs = 4096

// legResult is what one direction measured.
type legResult struct {
	mbps       float64
	loadedMs   float64
	fromClient bool
}

// probeSample is one loaded-latency probe: its latency, and when it finished
// counted from the start of the run and from the start of its direction.
type probeSample struct {
	ms float64
	t  float64
	at float64
}

// leg is one direction of a run: its streams, its probes, and the counts they
// share. The streams and the probe touch only the guarded fields; the rest
// belongs to the goroutine running the direction.
type leg struct {
	r      *runner
	dir    int
	phase  Phase
	spec   dirPlan
	budget *budget
	start  time.Time
	warm   float64
	dur    float64

	// buckets holds bytes received as they arrive, for download, and bytes
	// handed to the socket, for upload. uploads holds each stream's finished
	// requests with the byte count the far end reported.
	mu      sync.Mutex
	buckets *stats.Buckets
	uploads [][]stats.UploadReq

	samples    []probeSample
	clientRate [][3]float64
	live       float64
}

// direction runs one direction of the test, download for dir 0 and upload for
// dir 1: the plan's streams, the probes alongside, and the figures worked
// out when it ends (docs/api.md "Running a test", steps 3 to 5).
func (r *runner) direction(dir int) (legResult, error) {
	l := &leg{r: r, dir: dir, phase: PhaseDownload, spec: r.plan.down}
	if dir == 1 {
		l.phase, l.spec = PhaseUpload, r.plan.up
	}
	return l.run()
}

// run runs the direction for its duration. It ends sooner when a light run's
// byte cap is spent, and fails at once when a stream does, because
// the rest would be a result from fewer streams than it claims. A probe that
// fails only stops the probing (ADR-0032). Requests
// still in flight when it ends are abandoned, which is not a failure.
func (l *leg) run() (legResult, error) {
	r, spec := l.r, l.spec

	// A direction starts on connections of its own.
	r.load.CloseIdleConnections()
	r.report(Progress{Phase: l.phase})

	l.start = time.Now()
	startT := l.start.Sub(r.t0).Seconds()
	l.warm, l.dur = spec.warmup.Seconds(), spec.duration.Seconds()
	l.buckets = stats.NewBuckets(bucketWidth)
	l.samples = make([]probeSample, 0, int(spec.duration/r.plan.loadedEvery)+4)
	if l.dir == 1 {
		l.uploads = make([][]stats.UploadReq, spec.streams)
		l.clientRate = make([][3]float64, 0, int(spec.duration/rateEvery)+4)
	}
	if r.light {
		l.budget = newBudget(r.maxBytes)
	}
	spanAt := len(r.detail.Spans)
	r.detail.Spans = append(r.detail.Spans, [4]float64{float64(l.dir), round1(startT), round1(startT + l.warm), round1(startT + l.dur)})

	dctx, cancel := context.WithCancel(r.ctx)
	var wg sync.WaitGroup
	defer func() {
		cancel()
		wg.Wait()
	}()
	ends := make(chan error, spec.streams)
	samples := make(chan probeSample, 8)
	for i := range spec.streams {
		wg.Go(func() { ends <- l.stream(dctx, i) })
	}
	wg.Go(func() { l.probe(dctx, samples) })

	timer := time.NewTimer(spec.duration)
	defer timer.Stop()
	tick := time.NewTicker(rateEvery)
	defer tick.Stop()

	var failure error
	finished := 0
	early := false
loop:
	for {
		select {
		case <-r.ctx.Done():
			failure = r.ctx.Err()
			break loop
		case <-timer.C:
			break loop
		case err := <-ends:
			if err != nil {
				failure = err
				break loop
			}
			finished++
			if finished == spec.streams {
				early = true
				break loop
			}
		case s := <-samples:
			l.onProbe(s, true)
		case <-tick.C:
			l.onTick()
		}
	}
	ended := time.Since(l.start).Seconds()
	cancel()
	wg.Wait()

	// Whatever the streams and the probe finished before they stopped counts.
drain:
	for {
		select {
		case s := <-samples:
			l.onProbe(s, false)
		case err := <-ends:
			if err != nil && failure == nil {
				failure = err
			}
		default:
			break drain
		}
	}
	if failure == nil {
		failure = r.ctx.Err()
	}
	if failure != nil {
		return legResult{}, classify(r.ctx, r.host, l.phase, failure)
	}

	counted := l.counted()
	if early {
		// The byte cap ended the direction: it ran for less time than planned,
		// so its figures are worked out over the time it ran, with the same
		// 40% limit on warm-up the browser puts on a short test.
		l.warm = min(l.warm, 0.4*ended)
		l.dur = ended
		r.detail.Spans[spanAt] = [4]float64{float64(l.dir), round1(startT), round1(startT + l.warm), round1(startT + l.dur)}
	}

	var res legResult
	if l.dir == 0 {
		if counted == 0 {
			return legResult{}, newError(CodeTimeout, l.phase, r.host, "no data arrived", nil)
		}
		res.mbps = l.downloadRate(early, counted)
	} else if rate, ok := stats.UploadRate(l.uploads, l.warm); ok {
		// Upload comes from the far end's counts, never from what was written.
		res.mbps = rate
		for _, s := range stats.UploadSeries(l.uploads, l.warm, bucketWidth, uploadWindow) {
			r.detail.Rate = appendCapped(r.detail.Rate, [3]float64{round1(startT + s[0]), round1(s[1]), 1})
		}
	} else {
		// Not one request was answered: the client's own count stands, and the
		// detail says so.
		if counted == 0 {
			return legResult{}, newError(CodeTimeout, l.phase, r.host, "no data was sent", nil)
		}
		res.fromClient = true
		res.mbps = l.rate(l.warm, l.dur)
		for _, s := range l.clientRate {
			r.detail.Rate = appendCapped(r.detail.Rate, s)
		}
	}
	res.loadedMs = l.loadedMedian()
	return res, nil
}

// at is the seconds since the direction began.
func (l *leg) at() float64 { return time.Since(l.start).Seconds() }

// count adds n bytes, arriving or sent now, to the buckets.
func (l *leg) count(n int) {
	t := l.at()
	l.mu.Lock()
	l.buckets.Add(t, float64(n))
	l.mu.Unlock()
}

// counted is every byte the buckets hold.
func (l *leg) counted() float64 {
	l.mu.Lock()
	defer l.mu.Unlock()
	sum := 0.0
	for _, c := range l.buckets.Counts {
		sum += c
	}
	return sum
}

// rate is the buckets' rate over [from, to), in whole buckets.
func (l *leg) rate(from, to float64) float64 {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buckets.Rate(from, to)
}

// downloadRate is the download speed: the rate over the whole buckets from the
// end of warm-up to the end of the direction, empty ones included. When a
// byte cap ended the direction before a whole bucket was left to count, it is
// every byte over the time the direction ran.
func (l *leg) downloadRate(early bool, counted float64) float64 {
	if early {
		whole := math.Floor(l.dur/bucketWidth+1e-9) - math.Ceil(l.warm/bucketWidth-1e-9)
		if whole < 1 {
			return stats.Mbps(counted, l.dur)
		}
	}
	return l.rate(l.warm, l.dur)
}

// onTick reports the live rate, every rateEvery. The live download rate is the
// last half second of bytes received. The live upload rate is the last second
// the far end has confirmed, which trails real time by about a request: the
// client's own count runs megabytes ahead of the network.
func (l *leg) onTick() {
	r := l.r
	t := l.at()
	l.mu.Lock()
	rate := l.buckets.Rate(max(0, t-liveWindow), t)
	var confirmed float64
	if l.dir == 1 {
		if known, ok := stats.UploadKnown(l.uploads); ok {
			confirmed = stats.Mbps(stats.UploadBytes(l.uploads, known-uploadWindow, known), uploadWindow)
		}
	}
	l.mu.Unlock()

	now := r.since()
	if l.dir == 0 {
		l.live = rate
		r.detail.Rate = appendCapped(r.detail.Rate, [3]float64{round1(now), round1(rate), 0})
	} else {
		l.live = confirmed
		l.clientRate = appendCapped(l.clientRate, [3]float64{round1(now), round1(rate), 1})
	}
	r.report(Progress{Phase: l.phase, Mbps: l.live})
}

// onProbe takes in a loaded-latency probe. live is false for a probe found
// after the direction ended, which isn't reported.
func (l *leg) onProbe(s probeSample, live bool) {
	r := l.r
	l.samples = appendCapped(l.samples, s)
	r.detail.Probes = appendCapped(r.detail.Probes, [3]float64{round1(s.t), round1(s.ms), float64(l.dir)})
	if live {
		r.report(Progress{Phase: l.phase, Mbps: l.live, Ms: s.ms})
	}
}

// loadedMedian is the direction's loaded latency: the median of the probes
// that finished after warm-up, 0 if none did. It is worked out when the
// direction is over, so a direction a byte cap cut short uses the shorter
// warm-up its speed does.
func (l *leg) loadedMedian() float64 {
	ms := make([]float64, 0, len(l.samples))
	for _, s := range l.samples {
		if s.at >= l.warm {
			ms = append(ms, s.ms)
		}
	}
	if len(ms) == 0 {
		return 0
	}
	return stats.Median(ms)
}

// probe pings every loaded_interval_ms, one at a time, on the probe client's
// own connection. It ends with ctx. A probe that fails any other way, or runs
// past PingTimeout, ends the probing and nothing else: the direction carries
// on, and its loaded latency is the median of the probes that finished after
// warm-up, 0 with none (ADR-0032). The failure goes nowhere, so a probe lost
// on a saturated link can't throw away a valid speed.
func (l *leg) probe(ctx context.Context, samples chan<- probeSample) {
	r := l.r
	for {
		ms, err := r.ping(ctx)
		if err != nil {
			return
		}
		s := probeSample{ms: ms, t: r.since(), at: l.at()}
		select {
		case samples <- s:
		case <-ctx.Done():
			return
		}
		if !sleep(ctx, r.plan.loadedEvery) {
			return
		}
	}
}

// stream is one load stream: requests back to back until ctx ends, the byte
// cap is spent or a request fails. It returns nil when ctx ended it, which is
// not a failure.
func (l *leg) stream(ctx context.Context, i int) error {
	if l.dir == 0 {
		return l.download(ctx)
	}
	return l.upload(ctx, i)
}

// download is back-to-back downloads on one connection, as the browser does:
// the first asks for start_bytes, and each next request doubles the size while
// the last one finished within target_request_ms, never above max_bytes.
func (l *leg) download(ctx context.Context) error {
	buf := make([]byte, readBuf)
	size := l.spec.startBytes
	for ctx.Err() == nil {
		ask := l.budget.take(size)
		if ask <= 0 {
			return nil
		}
		began := time.Now()
		if err := l.fetch(ctx, buf, ask); err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		if time.Since(began) < l.spec.target {
			size = min(size*2, l.spec.maxBytes)
		}
	}
	return nil
}

// fetch downloads size bytes, counting them as they arrive. A reply that
// isn't exactly size bytes isn't a dyno's.
func (l *leg) fetch(ctx context.Context, buf []byte, size int64) error {
	r := l.r
	u := *r.plan.download
	u.RawQuery = "bytes=" + strconv.FormatInt(size, 10) + "&t=" + url.QueryEscape(r.plan.testID) + "&r=" + r.bust()
	req, err := newRequest(ctx, http.MethodGet, &u, nil)
	if err != nil {
		return err
	}
	resp, err := r.load.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return statusError(PhaseDownload, r.host, resp.StatusCode)
	}
	if resp.ContentLength >= 0 && resp.ContentLength != size {
		return r.wrongLength()
	}
	var got int64
	for {
		n, err := resp.Body.Read(buf)
		if n > 0 {
			got += int64(n)
			l.count(n)
		}
		if got > size {
			return r.wrongLength()
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
	}
	if got != size {
		return r.wrongLength()
	}
	return nil
}

// wrongLength is the error for a download that isn't the size asked for.
func (r *runner) wrongLength() *Error {
	return newError(CodeNotDyno, PhaseDownload, r.host, "a download came back the wrong length", nil)
}

// upload is back-to-back uploads, as the browser does: the first body is
// start_bytes, and each next one is sized to take target_request_ms at the
// rate the far end's last reply says, from 64 KiB to max_bytes. Doubling, as
// downloads do, would overshoot, and the far end's count only arrives with
// each reply.
func (l *leg) upload(ctx context.Context, i int) error {
	size := l.spec.startBytes
	for n := 0; n < maxUploadReqs && ctx.Err() == nil; n++ {
		body := l.budget.take(size)
		if body <= 0 {
			return nil
		}
		start := l.at()
		began := time.Now()
		got, err := l.post(ctx, body)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		l.mu.Lock()
		l.uploads[i] = append(l.uploads[i], stats.UploadReq{Start: start, End: l.at(), Bytes: got})
		l.mu.Unlock()

		took := max(1.0, float64(time.Since(began).Microseconds())/1000) // ms
		next := got / took * float64(l.spec.target.Milliseconds())
		size = int64(math.Round(min(float64(l.spec.maxBytes), max(minUploadBytes, next))))
	}
	return nil
}

// post uploads size bytes of the run's random block and returns the bytes the
// far end says it received. A reply that doesn't say is not a dyno's.
func (l *leg) post(ctx context.Context, size int64) (float64, error) {
	r := l.r
	u := *r.plan.upload
	u.RawQuery = "t=" + url.QueryEscape(r.plan.testID) + "&r=" + r.bust()
	req, err := newRequest(ctx, http.MethodPost, &u, &payload{block: r.block, left: size, count: l.count})
	if err != nil {
		return 0, err
	}
	req.ContentLength = size
	req.Header.Set("Content-Type", "application/octet-stream")
	resp, err := r.load.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return 0, statusError(PhaseUpload, r.host, resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxReplyBytes))
	if err != nil {
		return 0, err
	}
	var reply struct {
		Bytes *float64 `json:"bytes"`
	}
	if err := json.Unmarshal(data, &reply); err != nil || reply.Bytes == nil || *reply.Bytes < 0 {
		return 0, newError(CodeNotDyno, PhaseUpload, r.host, "an upload reply that doesn't say how many bytes arrived", nil)
	}
	return min(*reply.Bytes, float64(size)), nil
}

// payload is an upload body: left bytes cut from the run's random block,
// which repeats. It counts the bytes as the transport takes them.
type payload struct {
	block []byte
	off   int
	left  int64
	count func(int)
}

func (p *payload) Read(b []byte) (int, error) {
	if p.left <= 0 {
		return 0, io.EOF
	}
	n := copy(b, p.block[p.off:])
	if int64(n) > p.left {
		n = int(p.left)
	}
	p.off += n
	if p.off == len(p.block) {
		p.off = 0
	}
	p.left -= int64(n)
	if n > 0 {
		p.count(n)
	}
	return n, nil
}

// budget is the bytes a light run may still move in one direction, shared by
// the direction's streams.
type budget struct{ left atomic.Int64 }

func newBudget(n int64) *budget {
	b := &budget{}
	b.left.Store(n)
	return b
}

// take reserves up to want bytes and returns how many it got, 0 once the
// budget is spent. A nil budget has no limit.
func (b *budget) take(want int64) int64 {
	if b == nil {
		return want
	}
	for {
		left := b.left.Load()
		if left <= 0 {
			return 0
		}
		n := min(left, want)
		if b.left.CompareAndSwap(left, left-n) {
			return n
		}
	}
}
