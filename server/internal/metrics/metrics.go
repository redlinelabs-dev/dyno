// Package metrics keeps the server's Prometheus metrics and writes them in
// the text exposition format 0.0.4. It is written by hand, without the
// Prometheus client library, to keep the footprint small (ADR-0006, ADR-0017).
//
// Everything the measurement hot path touches is an atomic, so counting costs
// no allocation and no lock. Stored results are rare, so their counters and
// histograms share one mutex, which keeps a scrape consistent.
package metrics

import (
	"math"
	"net/http"
	"os"
	"runtime"
	"runtime/metrics"
	"sort"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/redlinelabs-dev/dyno/server/internal/result"
)

// ContentType is the media type of the text exposition format 0.0.4.
const ContentType = "text/plain; version=0.0.4; charset=utf-8"

// Direction is a measurement stream's direction.
type Direction uint8

const (
	Download Direction = iota
	Upload
)

var directionNames = [...]string{Download: "download", Upload: "upload"}

// Route is the low-cardinality route label of dyno_requests_total. The set
// is fixed: a raw path never becomes a label value.
type Route uint8

const (
	RouteOther Route = iota // no route matched (404 or 405 from the mux)
	RouteDL
	RouteUL
	RoutePing
	RoutePlan
	RouteResults
	RouteUI
	numRoutes
)

var routeNames = [numRoutes]string{"other", "dl", "ul", "ping", "plan", "results", "ui"}

// RouteOf maps a ServeMux pattern (http.Request.Pattern) to its route label.
func RouteOf(pattern string) Route {
	switch pattern {
	case "GET /dl":
		return RouteDL
	case "POST /ul":
		return RouteUL
	case "GET /ping":
		return RoutePing
	case "GET /api/v1/plan":
		return RoutePlan
	case "POST /api/v1/results", "GET /api/v1/results", "GET /api/v1/results/{id}":
		return RouteResults
	case "GET /":
		return RouteUI
	}
	return RouteOther
}

// Status codes outside this range are not counted; net/http allows none
// below 100, and dyno sends none above 599.
const (
	minCode = 100
	maxCode = 599
)

// Grades in exposition order; "none" is a result without loaded latency.
var grades = [...]string{"A+", "A", "B", "C", "D", "F", "none"}

var (
	// Bytes per second, as Prometheus base units ask: 1 Mbit/s to 10 Gbit/s.
	speedBuckets   = []float64{125e3, 625e3, 1.25e6, 3.125e6, 6.25e6, 1.25e7, 3.125e7, 6.25e7, 1.25e8, 3.125e8, 6.25e8, 1.25e9}
	latencyBuckets = []float64{.005, .01, .02, .05, .1, .2, .5, 1, 2}
)

// Metrics holds every metric dyno exports. The zero value is not usable; use
// New. Methods on a nil *Metrics do nothing, so code that counts needs no
// check of its own.
type Metrics struct {
	version string
	start   time.Time

	sent     atomic.Uint64
	received atomic.Uint64
	streams  [len(directionNames)]atomic.Int64
	requests [numRoutes][maxCode - minCode + 1]atomic.Uint64

	mu       sync.Mutex
	byGrade  [len(grades)]uint64
	download histogram
	upload   histogram
	idle     histogram
	loaded   histogram
}

// New returns empty metrics for a server of the given version.
func New(version string) *Metrics {
	return &Metrics{
		version:  version,
		start:    time.Now(),
		download: newHistogram(speedBuckets),
		upload:   newHistogram(speedBuckets),
		idle:     newHistogram(latencyBuckets),
		loaded:   newHistogram(latencyBuckets),
	}
}

// AddSent counts n bytes written by /dl.
func (m *Metrics) AddSent(n int) {
	if m != nil && n > 0 {
		m.sent.Add(uint64(n))
	}
}

// AddReceived counts n bytes read by /ul.
func (m *Metrics) AddReceived(n int) {
	if m != nil && n > 0 {
		m.received.Add(uint64(n))
	}
}

// StreamOpened and StreamClosed bracket one /dl or /ul request.
func (m *Metrics) StreamOpened(d Direction) {
	if m != nil {
		m.streams[d].Add(1)
	}
}

func (m *Metrics) StreamClosed(d Direction) {
	if m != nil {
		m.streams[d].Add(-1)
	}
}

// CountRequest counts one finished request.
func (m *Metrics) CountRequest(route Route, code int) {
	if m == nil || route >= numRoutes || code < minCode || code > maxCode {
		return
	}
	m.requests[route][code-minCode].Add(1)
}

// ObserveResult counts one stored result. Results hold Mbit/s and ms; the
// metrics use base units, bits per second and seconds. Loaded latency is the
// worse of the two directions and is skipped when it wasn't measured.
func (m *Metrics) ObserveResult(r result.Result) {
	if m == nil {
		return
	}
	g := len(grades) - 1
	for i, name := range grades {
		if name == r.Grade {
			g = i
			break
		}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.byGrade[g]++
	m.download.observe(r.DownloadMbps * 1e6 / 8)
	m.upload.observe(r.UploadMbps * 1e6 / 8)
	m.idle.observe(r.IdleMs / 1e3)
	if loaded := max(r.LoadedDownMs, r.LoadedUpMs); loaded > 0 {
		m.loaded.observe(loaded / 1e3)
	}
}

// Handler serves GET /metrics and answers 404 to everything else.
func (m *Metrics) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/metrics" {
			http.NotFound(w, r)
			return
		}
		b := m.Append(make([]byte, 0, 8<<10))
		h := w.Header()
		h.Set("Content-Type", ContentType)
		h.Set("Cache-Control", "no-store")
		h.Set("Content-Length", strconv.Itoa(len(b)))
		w.Write(b)
	})
}

// Append appends every metric in the text exposition format to b.
func (m *Metrics) Append(b []byte) []byte {
	b = header(b, "dyno_build_info", "gauge", "The running dyno version; always 1.")
	b = append(b, `dyno_build_info{version="`...)
	b = appendLabelValue(b, m.version)
	b = append(b, "\"} 1\n"...)

	m.mu.Lock()
	b = header(b, "dyno_results_total", "counter", "Results stored since the server started, by grade.")
	for i, g := range grades {
		b = append(b, `dyno_results_total{grade="`...)
		b = appendLabelValue(b, g)
		b = append(b, `"} `...)
		b = strconv.AppendUint(b, m.byGrade[i], 10)
		b = append(b, '\n')
	}
	b = m.download.append(b, "dyno_result_download_bytes_per_second", "Download speed of stored results.")
	b = m.upload.append(b, "dyno_result_upload_bytes_per_second", "Upload speed of stored results.")
	b = m.idle.append(b, "dyno_result_idle_latency_seconds", "Idle latency of stored results.")
	b = m.loaded.append(b, "dyno_result_loaded_latency_seconds", "Loaded latency of stored results, the worse of download and upload.")
	m.mu.Unlock()

	b = header(b, "dyno_bytes_sent_total", "counter", "Bytes written by /dl.")
	b = sampleUint(b, "dyno_bytes_sent_total", m.sent.Load())
	b = header(b, "dyno_bytes_received_total", "counter", "Bytes read by /ul.")
	b = sampleUint(b, "dyno_bytes_received_total", m.received.Load())

	b = header(b, "dyno_streams_active", "gauge", "In-flight /dl and /ul requests.")
	for d, name := range directionNames {
		b = append(b, `dyno_streams_active{direction="`...)
		b = append(b, name...)
		b = append(b, `"} `...)
		b = strconv.AppendInt(b, m.streams[d].Load(), 10)
		b = append(b, '\n')
	}

	// A (route, code) pair appears once seen; the family once any pair has.
	declared := false
	for route := range m.requests {
		for i := range m.requests[route] {
			n := m.requests[route][i].Load()
			if n == 0 {
				continue
			}
			if !declared {
				b = header(b, "dyno_requests_total", "counter", "Finished HTTP requests on the main listener, by route and status code.")
				declared = true
			}
			b = append(b, `dyno_requests_total{route="`...)
			b = append(b, routeNames[route]...)
			b = append(b, `",code="`...)
			b = strconv.AppendInt(b, int64(i+minCode), 10)
			b = append(b, `"} `...)
			b = strconv.AppendUint(b, n, 10)
			b = append(b, '\n')
		}
	}

	if rss, ok := residentMemory(); ok {
		b = header(b, "process_resident_memory_bytes", "gauge", "Resident memory size in bytes.")
		b = sampleUint(b, "process_resident_memory_bytes", rss)
	}
	b = header(b, "process_start_time_seconds", "gauge", "Start time of the process since the Unix epoch in seconds.")
	b = append(b, "process_start_time_seconds "...)
	b = appendFloat(b, float64(m.start.UnixMilli())/1e3)
	b = append(b, '\n')
	b = header(b, "go_goroutines", "gauge", "Number of goroutines that currently exist.")
	b = sampleUint(b, "go_goroutines", uint64(runtime.NumGoroutine()))

	// Go's own memory accounting, which the soak's leak check reads (ADR-0006).
	// Names as Prometheus's Go client exposes the same runtime/metrics.
	mem := [...]metrics.Sample{{Name: "/gc/heap/live:bytes"}, {Name: "/memory/classes/total:bytes"}, {Name: "/memory/classes/heap/released:bytes"}}
	metrics.Read(mem[:])
	for i, g := range [...]struct{ name, help string }{
		{"go_gc_heap_live_bytes", "Heap memory occupied by live objects as of the last garbage collection."},
		{"go_memory_classes_total_bytes", "All memory mapped by the Go runtime."},
		{"go_memory_classes_heap_released_bytes", "Heap memory the Go runtime has returned to the OS."},
	} {
		b = header(b, g.name, "gauge", g.help)
		b = sampleUint(b, g.name, mem[i].Value.Uint64())
	}
	return b
}

// residentMemory reads the resident set size from /proc/self/statm (Linux).
// Elsewhere it reports false and the metric is left out.
func residentMemory() (uint64, bool) {
	data, err := os.ReadFile("/proc/self/statm")
	if err != nil {
		return 0, false
	}
	// statm is "size resident shared text lib data dt", in pages.
	var field, pages uint64
	digits := false
	for _, c := range data {
		switch {
		case c >= '0' && c <= '9':
			if field == 1 {
				pages = pages*10 + uint64(c-'0')
				digits = true
			}
		case c == ' ':
			field++
		}
		if field > 1 {
			break
		}
	}
	if !digits {
		return 0, false
	}
	return pages * uint64(os.Getpagesize()), true
}

// histogram is a Prometheus histogram over fixed upper bounds. counts[i]
// holds observations in bucket i alone; append makes them cumulative.
type histogram struct {
	bounds []float64
	counts []uint64 // len(bounds)+1; the last is above every bound
	sum    float64
	count  uint64
}

func newHistogram(bounds []float64) histogram {
	return histogram{bounds: bounds, counts: make([]uint64, len(bounds)+1)}
}

func (h *histogram) observe(v float64) {
	if math.IsNaN(v) {
		return
	}
	h.counts[sort.SearchFloat64s(h.bounds, v)]++ // first bound >= v: le semantics
	h.sum += v
	h.count++
}

func (h *histogram) append(b []byte, name, help string) []byte {
	b = header(b, name, "histogram", help)
	var cum uint64
	for i, c := range h.counts {
		cum += c
		b = append(b, name...)
		b = append(b, `_bucket{le="`...)
		if i < len(h.bounds) {
			b = appendFloat(b, h.bounds[i])
		} else {
			b = append(b, "+Inf"...)
		}
		b = append(b, `"} `...)
		b = strconv.AppendUint(b, cum, 10)
		b = append(b, '\n')
	}
	b = append(b, name...)
	b = append(b, "_sum "...)
	b = appendFloat(b, h.sum)
	b = append(b, '\n')
	b = append(b, name...)
	b = append(b, "_count "...)
	b = strconv.AppendUint(b, h.count, 10)
	return append(b, '\n')
}

// header writes the HELP and TYPE lines. Help texts are constants here, with
// no backslash or newline to escape.
func header(b []byte, name, typ, help string) []byte {
	b = append(b, "# HELP "...)
	b = append(b, name...)
	b = append(b, ' ')
	b = append(b, help...)
	b = append(b, "\n# TYPE "...)
	b = append(b, name...)
	b = append(b, ' ')
	b = append(b, typ...)
	return append(b, '\n')
}

func sampleUint(b []byte, name string, v uint64) []byte {
	b = append(b, name...)
	b = append(b, ' ')
	b = strconv.AppendUint(b, v, 10)
	return append(b, '\n')
}

// appendFloat formats v the way the Prometheus client libraries do.
func appendFloat(b []byte, v float64) []byte {
	switch {
	case math.IsInf(v, 1):
		return append(b, "+Inf"...)
	case math.IsInf(v, -1):
		return append(b, "-Inf"...)
	case math.IsNaN(v):
		return append(b, "NaN"...)
	}
	return strconv.AppendFloat(b, v, 'g', -1, 64)
}

// appendLabelValue escapes a label value: backslash, double quote and line
// feed, as the text format requires.
func appendLabelValue(b []byte, s string) []byte {
	for i := 0; i < len(s); i++ {
		switch c := s[i]; c {
		case '\\':
			b = append(b, `\\`...)
		case '"':
			b = append(b, `\"`...)
		case '\n':
			b = append(b, `\n`...)
		default:
			b = append(b, c)
		}
	}
	return b
}
