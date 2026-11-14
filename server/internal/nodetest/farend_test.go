package nodetest_test

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/redlinelabs-dev/dyno/server/internal/nodetest"
)

// The fake far end every nodetest test runs against: a stand-in for a dyno
// that serves a plan you control, records every request it gets, and
// misbehaves on request. Its /dl, /ul and /ping do what docs/api.md says
// unless a knob changes them.

const testID = "3f9c2a7e01b44d6a9e5c8b12"

// transferDoc and planDoc are docs/api.md's plan JSON.
type transferDoc struct {
	Streams         int   `json:"streams"`
	DurationMs      int   `json:"duration_ms"`
	WarmupMs        int   `json:"warmup_ms"`
	StartBytes      int64 `json:"start_bytes"`
	MaxBytes        int64 `json:"max_bytes"`
	TargetRequestMs int   `json:"target_request_ms"`
}

type planDoc struct {
	TestID string `json:"test_id"`
	Server struct {
		Name    string `json:"name"`
		Version string `json:"version"`
	} `json:"server"`
	Endpoints struct {
		Download string `json:"download"`
		Upload   string `json:"upload"`
		Ping     string `json:"ping"`
		Results  string `json:"results"`
	} `json:"endpoints"`
	Latency struct {
		IdleSamples      int `json:"idle_samples"`
		LoadedIntervalMs int `json:"loaded_interval_ms"`
	} `json:"latency"`
	Download transferDoc `json:"download"`
	Upload   transferDoc `json:"upload"`
}

// shortPlan is a plan a test can run in about a second: two streams, 600 ms
// a direction, small requests that double fast.
func shortPlan() planDoc {
	var p planDoc
	p.TestID = testID
	p.Server.Name, p.Server.Version = "fakedyno", "v9.9.9"
	p.Endpoints.Download, p.Endpoints.Upload, p.Endpoints.Ping, p.Endpoints.Results = "/dl", "/ul", "/ping", "/api/v1/results"
	p.Latency.IdleSamples, p.Latency.LoadedIntervalMs = 5, 100
	p.Download = transferDoc{Streams: 2, DurationMs: 600, WarmupMs: 150, StartBytes: 64 << 10, MaxBytes: 4 << 20, TargetRequestMs: 100}
	p.Upload = p.Download
	return p
}

// rec is one request the far end saw.
type rec struct {
	N       int // requests to this path before it
	Seq     int // requests to any path before it
	Method  string
	Path    string
	Query   url.Values
	Header  http.Header
	Proto   string
	Remote  string // the client's address
	Start   time.Time
	End     time.Time // zero while the handler runs
	Bytes   int64     // /dl: bytes written; /ul: bytes read
	Length  int64     // /ul: Content-Length, -1 when there was none
	Chunked bool      // /ul: no Content-Length, sent chunked
	Head    []byte    // /ul: the first Options.KeepBody bytes of the body
	Aborted bool      // the client went away before the handler finished
}

// size is the bytes= a /dl asked for.
func (r rec) size() int64 {
	n, _ := strconv.ParseInt(r.Query.Get("bytes"), 10, 64)
	return n
}

const (
	chunk       = 32 << 10 // bytes per write or read
	keptUploads = 2        // uploads whose bodies KeepBody applies to
)

// noise is the incompressible payload every /dl cuts from.
var noise = func() []byte {
	b := make([]byte, 1<<20)
	rand.Read(b)
	return b
}()

// farEnd is a fake dyno. Make one with newFarEnd, set the knobs, then start it.
type farEnd struct {
	t   *testing.T
	URL string
	srv *httptest.Server

	Plan planDoc // served at /api/v1/plan

	DLMbps, ULMbps float64                // rate shared by all /dl (or /ul) streams, Mbit/s; 0 is no limit
	DLDelay        time.Duration          // each /dl waits this long before it answers
	ULHold         time.Duration          // a /ul's reply comes this long after the request opened, if not due sooner
	ULCount        func(read int64) int64 // the byte count a /ul reply claims; nil tells the truth
	KeepBody       int                    // bytes of each of the first uploads' bodies to keep
	AcceptDelay    time.Duration          // each new connection waits this long before the server takes it
	TLS            bool                   // serve https, offering HTTP/2 as well as HTTP/1.1

	// A hook runs once the request is recorded and returns true if it answered.
	// n counts that path's requests from 0.
	PlanHook, DLHook, ULHook, PingHook func(w http.ResponseWriter, r *http.Request, n int) bool

	stop     chan struct{}
	stopOnce sync.Once
	dlLink   pacer
	ulLink   pacer

	mu       sync.Mutex
	log      []*rec
	inflight map[string]int
	peak     map[string]int
	seen     map[string]int
	open     int
}

// newFarEnd returns a far end serving plan, with /dl and /ul at 200 Mbit/s so
// a test never moves gigabytes. Set a rate to 0 for no limit.
func newFarEnd(t *testing.T, plan planDoc) *farEnd {
	t.Helper()
	return &farEnd{
		t: t, Plan: plan, DLMbps: 200, ULMbps: 200,
		stop:     make(chan struct{}),
		inflight: map[string]int{}, peak: map[string]int{}, seen: map[string]int{},
	}
}

// start begins serving, with the knobs as they are now. The far end closes
// when the test ends.
func (fe *farEnd) start() *farEnd {
	fe.t.Helper()
	fe.dlLink.bps = fe.DLMbps * 1e6 / 8
	fe.ulLink.bps = fe.ULMbps * 1e6 / 8
	srv := httptest.NewUnstartedServer(fe)
	srv.Config.ConnState = fe.connState
	if fe.AcceptDelay > 0 {
		srv.Listener = &slowListener{Listener: srv.Listener, delay: fe.AcceptDelay}
	}
	if fe.TLS {
		srv.EnableHTTP2 = true
		srv.TLS = &tls.Config{NextProtos: []string{"h2", "http/1.1"}}
		srv.StartTLS()
	} else {
		srv.Start()
	}
	fe.srv, fe.URL = srv, srv.URL
	fe.t.Cleanup(fe.close)
	return fe
}

func (fe *farEnd) close() {
	fe.stopOnce.Do(func() { close(fe.stop) }) // lets every blocked handler go
	fe.srv.CloseClientConnections()           // and a client that leaked a connection can't hold Close
	fe.srv.Close()
}

// options is what a test hands nodetest.Run: this far end, no warm-up beyond
// the plan's one ping, and a ping timeout short enough to fail fast.
func (fe *farEnd) options() nodetest.Options {
	o := nodetest.Options{FarEnd: fe.URL, Warm: -1, PingTimeout: 3 * time.Second}
	if fe.TLS {
		pool := x509.NewCertPool()
		pool.AddCert(fe.srv.Certificate())
		o.TLS = &tls.Config{RootCAs: pool}
	}
	return o
}

func (fe *farEnd) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	rc, done := fe.begin(r)
	defer done()
	ep := fe.Plan.Endpoints
	switch r.URL.Path {
	case "/api/v1/plan":
		fe.servePlan(w, r, rc)
	case ep.Download:
		fe.serveDL(w, r, rc)
	case ep.Upload:
		fe.serveUL(w, r, rc)
	case ep.Ping:
		fe.servePing(w, r, rc)
	default:
		http.NotFound(w, r)
	}
}

func (fe *farEnd) servePlan(w http.ResponseWriter, r *http.Request, rc *rec) {
	if r.Method != http.MethodGet {
		jsonError(w, http.StatusMethodNotAllowed, "GET only")
		return
	}
	if fe.PlanHook != nil && fe.PlanHook(w, r, rc.N) {
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(fe.Plan)
}

func (fe *farEnd) serveDL(w http.ResponseWriter, r *http.Request, rc *rec) {
	if r.Method != http.MethodGet {
		jsonError(w, http.StatusMethodNotAllowed, "GET only")
		return
	}
	n, err := strconv.ParseInt(r.URL.Query().Get("bytes"), 10, 64)
	if err != nil || n < 0 {
		jsonError(w, http.StatusBadRequest, "bytes must be an integer")
		return
	}
	if fe.DLHook != nil && fe.DLHook(w, r, rc.N) {
		return
	}
	if !fe.sleep(r.Context(), fe.DLDelay) {
		fe.update(rc, func(rc *rec) { rc.Aborted = true })
		return
	}
	h := w.Header()
	h.Set("Content-Type", "application/octet-stream")
	h.Set("Content-Length", strconv.FormatInt(n, 10))
	w.WriteHeader(http.StatusOK)
	var sent int64
	defer func() { fe.update(rc, func(rc *rec) { rc.Bytes = sent }) }()
	for off := 0; sent < n; {
		c := int(min(int64(chunk), n-sent))
		if !fe.dlLink.wait(r.Context(), fe.stop, c) {
			fe.update(rc, func(rc *rec) { rc.Aborted = true })
			return
		}
		if _, err := w.Write(noise[off : off+c]); err != nil {
			fe.update(rc, func(rc *rec) { rc.Aborted = true })
			return
		}
		sent += int64(c)
		off = (off + c) % len(noise)
	}
}

func (fe *farEnd) serveUL(w http.ResponseWriter, r *http.Request, rc *rec) {
	if r.Method != http.MethodPost {
		jsonError(w, http.StatusMethodNotAllowed, "POST only")
		return
	}
	if fe.ULHook != nil && fe.ULHook(w, r, rc.N) {
		return
	}
	keep := 0
	if rc.N < keptUploads {
		keep = fe.KeepBody
	}
	buf := make([]byte, chunk)
	var read int64
	var head []byte
	abort := func() {
		fe.update(rc, func(rc *rec) { rc.Bytes, rc.Head, rc.Aborted = read, head, true })
	}
	for {
		n, err := r.Body.Read(buf)
		if n > 0 {
			read += int64(n)
			if room := keep - len(head); room > 0 {
				head = append(head, buf[:min(n, room)]...)
			}
			if !fe.ulLink.wait(r.Context(), fe.stop, n) { // the far end reads at the link's speed
				abort()
				return
			}
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			abort()
			return
		}
	}
	fe.update(rc, func(rc *rec) { rc.Bytes, rc.Head = read, head })
	if wait := fe.ULHold - time.Since(rc.Start); wait > 0 && !fe.sleep(r.Context(), wait) {
		fe.update(rc, func(rc *rec) { rc.Aborted = true })
		return
	}
	count := read
	if fe.ULCount != nil {
		count = fe.ULCount(read)
	}
	w.Header().Set("Content-Type", "application/json")
	fmt.Fprintf(w, `{"bytes":%d,"elapsed_us":%d}`+"\n", count, time.Since(rc.Start).Microseconds())
}

func (fe *farEnd) servePing(w http.ResponseWriter, r *http.Request, rc *rec) {
	if r.Method != http.MethodGet {
		jsonError(w, http.StatusMethodNotAllowed, "GET only")
		return
	}
	if fe.PingHook != nil && fe.PingHook(w, r, rc.N) {
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// begin records r and counts it in flight until done is called.
func (fe *farEnd) begin(r *http.Request) (rc *rec, done func()) {
	rc = &rec{
		Method: r.Method, Path: r.URL.Path, Query: r.URL.Query(), Header: r.Header.Clone(),
		Proto: r.Proto, Remote: r.RemoteAddr, Start: time.Now(),
		Length: r.ContentLength, Chunked: len(r.TransferEncoding) > 0,
	}
	fe.mu.Lock()
	rc.Seq, rc.N = len(fe.log), fe.seen[r.URL.Path]
	fe.seen[r.URL.Path]++
	fe.log = append(fe.log, rc)
	fe.inflight[r.URL.Path]++
	fe.peak[r.URL.Path] = max(fe.peak[r.URL.Path], fe.inflight[r.URL.Path])
	fe.mu.Unlock()
	return rc, func() {
		fe.mu.Lock()
		rc.End = time.Now()
		fe.inflight[rc.Path]--
		fe.mu.Unlock()
	}
}

func (fe *farEnd) update(rc *rec, f func(*rec)) {
	fe.mu.Lock()
	defer fe.mu.Unlock()
	f(rc)
}

func (fe *farEnd) connState(_ net.Conn, s http.ConnState) {
	fe.mu.Lock()
	defer fe.mu.Unlock()
	switch s {
	case http.StateNew:
		fe.open++
	case http.StateClosed, http.StateHijacked:
		fe.open--
	}
}

// sleep waits d, and reports false if the request or the far end ended first.
func (fe *farEnd) sleep(ctx context.Context, d time.Duration) bool {
	if d <= 0 {
		return true
	}
	tm := time.NewTimer(d)
	defer tm.Stop()
	select {
	case <-tm.C:
		return true
	case <-ctx.Done():
		return false
	case <-fe.stop:
		return false
	}
}

// block holds a request until its client goes away or the far end closes.
func (fe *farEnd) block(r *http.Request) {
	select {
	case <-r.Context().Done():
	case <-fe.stop:
	}
}

// waitFor polls cond until it holds, or d has passed.
func waitFor(d time.Duration, cond func() bool) bool {
	for deadline := time.Now().Add(d); ; time.Sleep(5 * time.Millisecond) {
		if cond() {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
	}
}

// waitIdle waits for the requests still in flight to end: the ones a client
// abandons end a moment after it drops them.
func (fe *farEnd) waitIdle(t *testing.T) {
	t.Helper()
	busy := func() int {
		fe.mu.Lock()
		defer fe.mu.Unlock()
		n := 0
		for _, c := range fe.inflight {
			n += c
		}
		return n
	}
	if !waitFor(2*time.Second, func() bool { return busy() == 0 }) {
		t.Errorf("%d requests still in flight 2 s after the run", busy())
	}
}

// recs is every request seen so far, in arrival order.
func (fe *farEnd) recs() []rec {
	fe.mu.Lock()
	defer fe.mu.Unlock()
	out := make([]rec, len(fe.log))
	for i, rc := range fe.log {
		out[i] = *rc
	}
	return out
}

// reset forgets what was recorded.
func (fe *farEnd) reset() {
	fe.mu.Lock()
	defer fe.mu.Unlock()
	fe.log = nil
	fe.seen = map[string]int{}
	fe.peak = map[string]int{}
}

func (fe *farEnd) peakInflight(path string) int {
	fe.mu.Lock()
	defer fe.mu.Unlock()
	return fe.peak[path]
}

func (fe *farEnd) inflightNow(path string) int {
	fe.mu.Lock()
	defer fe.mu.Unlock()
	return fe.inflight[path]
}

// firstStart is when the first request to path arrived; the zero time if none has.
func (fe *farEnd) firstStart(path string) time.Time {
	fe.mu.Lock()
	defer fe.mu.Unlock()
	for _, rc := range fe.log {
		if rc.Path == path {
			return rc.Start
		}
	}
	return time.Time{}
}

func (fe *farEnd) openConns() int {
	fe.mu.Lock()
	defer fe.mu.Unlock()
	return fe.open
}

// on is the requests to path, in arrival order.
func on(rs []rec, path string) []rec {
	var out []rec
	for _, r := range rs {
		if r.Path == path {
			out = append(out, r)
		}
	}
	return out
}

// drained is a hook that reads the request body before it answers, so the
// client has finished sending when the answer comes.
func drained(h func(http.ResponseWriter, *http.Request, int) bool) func(http.ResponseWriter, *http.Request, int) bool {
	return func(w http.ResponseWriter, r *http.Request, n int) bool {
		io.Copy(io.Discard, r.Body)
		return h(w, r, n)
	}
}

func jsonError(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	fmt.Fprintf(w, `{"error":%q}`+"\n", msg)
}

// breakAfter answers a /dl with its headers and the first part of its body,
// then drops the connection: a download that breaks off mid-body.
func breakAfter(w http.ResponseWriter, size int64, part int) {
	conn, buf, err := http.NewResponseController(w).Hijack()
	if err != nil {
		return
	}
	defer conn.Close()
	fmt.Fprintf(buf, "HTTP/1.1 200 OK\r\nContent-Type: application/octet-stream\r\nContent-Length: %d\r\n\r\n", size)
	buf.Write(noise[:min(part, len(noise))])
	buf.Flush()
}

// pacer is a link of fixed speed shared by every request that uses it: each
// chunk takes the time it needs after the one before, so the total is the
// rate however many streams draw on it. A link nobody uses doesn't save up
// credit.
type pacer struct {
	bps  float64 // bytes per second; 0 is no limit
	mu   sync.Mutex
	next time.Time
}

// wait returns when n more bytes have had their turn on the link, or false if
// the request or the far end ended first.
func (p *pacer) wait(ctx context.Context, stop <-chan struct{}, n int) bool {
	if p.bps <= 0 {
		select {
		case <-ctx.Done():
			return false
		case <-stop:
			return false
		default:
			return true
		}
	}
	p.mu.Lock()
	if now := time.Now(); p.next.Before(now) {
		p.next = now
	}
	p.next = p.next.Add(time.Duration(float64(n) / p.bps * float64(time.Second)))
	due := p.next
	p.mu.Unlock()
	tm := time.NewTimer(time.Until(due))
	defer tm.Stop()
	select {
	case <-tm.C:
		return true
	case <-ctx.Done():
		return false
	case <-stop:
		return false
	}
}

// slowListener hands the server each new connection only after a delay, so a
// client that opens one pays for it before its first reply.
type slowListener struct {
	net.Listener
	delay time.Duration
}

func (l *slowListener) Accept() (net.Conn, error) {
	c, err := l.Listener.Accept()
	if err == nil {
		time.Sleep(l.delay)
	}
	return c, err
}

// progressAt is a Progress update and when it arrived.
type progressAt struct {
	nodetest.Progress
	At time.Time
}

// result is what one nodetest.Run call did.
type result struct {
	Out     nodetest.Outcome
	Err     error
	Start   time.Time
	Took    time.Duration
	Prog    []progressAt
	Overlap bool // Progress was entered while another call was running
}

// run calls nodetest.Run and records its Progress updates. hook, if set, runs
// inside each update, on the goroutine Run called Progress from. The update
// list has no lock on purpose: with -race, Progress calls that aren't ordered
// show up as a data race.
func run(ctx context.Context, o nodetest.Options, hook func(progressAt)) *result {
	res := &result{Start: time.Now()}
	var inCall atomic.Int32
	var overlap atomic.Bool
	o.Progress = func(p nodetest.Progress) {
		if inCall.Add(1) != 1 {
			overlap.Store(true)
		}
		defer inCall.Add(-1)
		pa := progressAt{Progress: p, At: time.Now()}
		res.Prog = append(res.Prog, pa)
		if hook != nil {
			hook(pa)
		}
	}
	res.Out, res.Err = nodetest.Run(ctx, o)
	res.Took = time.Since(res.Start)
	res.Overlap = overlap.Load()
	return res
}

// firstAt is when Progress first reported phase; the zero time if it never did.
func (r *result) firstAt(p nodetest.Phase) time.Time {
	for _, pa := range r.Prog {
		if pa.Phase == p {
			return pa.At
		}
	}
	return time.Time{}
}

// phases is the phases Progress reported, in order, repeats collapsed.
func (r *result) phases() []nodetest.Phase {
	var out []nodetest.Phase
	for _, pa := range r.Prog {
		if len(out) == 0 || out[len(out)-1] != pa.Phase {
			out = append(out, pa.Phase)
		}
	}
	return out
}

// testCtx is a context that ends with the test, or after d.
func testCtx(t *testing.T, d time.Duration) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), d)
	t.Cleanup(cancel)
	return ctx
}

// runFull runs fe's options to the end.
func (fe *farEnd) runFull(t *testing.T) *result {
	t.Helper()
	return run(testCtx(t, 60*time.Second), fe.options(), nil)
}

// runUntil runs fe's options and cancels the run at the first Progress update
// in phase, after pause. The run's own result is a cancellation, so it's the
// far end's records that count.
func (fe *farEnd) runUntil(t *testing.T, o nodetest.Options, phase nodetest.Phase, pause time.Duration) *result {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	t.Cleanup(cancel)
	var armed atomic.Bool
	return run(ctx, o, func(pa progressAt) {
		if pa.Phase == phase && armed.CompareAndSwap(false, true) {
			if pause <= 0 {
				cancel()
			} else {
				time.AfterFunc(pause, cancel)
			}
		}
	})
}

// wantFail checks that res failed with code in phase and returned no result.
func wantFail(t *testing.T, res *result, code string, phase nodetest.Phase) *nodetest.Error {
	t.Helper()
	var ne *nodetest.Error
	if !errors.As(res.Err, &ne) {
		t.Fatalf("Run returned err %v (%T), want a *nodetest.Error with code %q", res.Err, res.Err, code)
	}
	if ne.Code != code || ne.Phase != phase {
		t.Errorf("Run failed with code %q in phase %q, want code %q in phase %q (err: %v)", ne.Code, ne.Phase, code, phase, ne.Err)
	}
	if !reflect.DeepEqual(res.Out, nodetest.Outcome{}) {
		t.Errorf("a failed Run returned a result: %+v", res.Out)
	}
	return ne
}

// message is err.Error(), or "" if that panics: a method that isn't written
// yet fails this test and leaves the rest of the run to go on.
func message(t *testing.T, err error) (msg string) {
	t.Helper()
	defer func() {
		if r := recover(); r != nil {
			t.Errorf("Error() panicked: %v", r)
		}
	}()
	return err.Error()
}

// mustPass fails the test if the run failed.
func mustPass(t *testing.T, res *result) {
	t.Helper()
	if res.Err != nil {
		t.Fatalf("Run failed: %v", res.Err)
	}
}

// detail is the browser's detail object (docs/api.md, ADR-0012).
type detail struct {
	Idle             [][]float64 `json:"idle"`
	Rate             [][]float64 `json:"rate"`
	Probes           [][]float64 `json:"probes"`
	Spans            [][]float64 `json:"spans"`
	TimerLagMs       *float64    `json:"timer_lag_ms"`
	UploadFromClient bool        `json:"upload_from_client"`
}

func parseDetail(t *testing.T, out nodetest.Outcome) detail {
	t.Helper()
	var d detail
	if err := json.Unmarshal(out.Submission.Detail, &d); err != nil {
		t.Fatalf("Submission.Detail is not a JSON object in the browser's shape: %v\n%s", err, out.Submission.Detail)
	}
	return d
}
