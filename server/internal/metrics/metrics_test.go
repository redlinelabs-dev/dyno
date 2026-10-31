package metrics

import (
	"fmt"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/redlinelabs-dev/dyno/server/internal/result"
)

// family is one parsed metric family.
type family struct {
	typ     string
	help    bool
	samples []sample
}

type sample struct {
	name   string // with any _bucket, _sum or _count suffix
	labels map[string]string
	value  float64
}

// parseExposition is a strict reader of the text format 0.0.4, as much of it
// as dyno writes: every family has HELP then TYPE before its samples, no
// family appears twice, sample names belong to the family just declared,
// label values are quoted with only \\, \" and \n escapes, and values parse
// as Go floats. Histograms are checked by checkHistogram.
func parseExposition(text string) (map[string]*family, error) {
	if !strings.HasSuffix(text, "\n") {
		return nil, fmt.Errorf("output doesn't end in a newline")
	}
	fams := map[string]*family{}
	var cur string
	for n, line := range strings.Split(strings.TrimSuffix(text, "\n"), "\n") {
		n++
		switch {
		case line == "":
			return nil, fmt.Errorf("line %d: empty", n)
		case strings.HasPrefix(line, "# HELP "):
			name, help, ok := strings.Cut(line[len("# HELP "):], " ")
			if !ok || help == "" || !validName(name) {
				return nil, fmt.Errorf("line %d: bad HELP %q", n, line)
			}
			if _, dup := fams[name]; dup {
				return nil, fmt.Errorf("line %d: family %s declared twice", n, name)
			}
			fams[name] = &family{help: true}
			cur = name
		case strings.HasPrefix(line, "# TYPE "):
			name, typ, _ := strings.Cut(line[len("# TYPE "):], " ")
			f := fams[name]
			if name != cur || f == nil || f.typ != "" || len(f.samples) > 0 {
				return nil, fmt.Errorf("line %d: TYPE %s not straight after its HELP", n, name)
			}
			switch typ {
			case "counter", "gauge", "histogram":
			default:
				return nil, fmt.Errorf("line %d: type %q", n, typ)
			}
			f.typ = typ
		case strings.HasPrefix(line, "#"):
			return nil, fmt.Errorf("line %d: stray comment %q", n, line)
		default:
			s, err := parseSample(line)
			if err != nil {
				return nil, fmt.Errorf("line %d: %v", n, err)
			}
			f := fams[cur]
			if f == nil || f.typ == "" {
				return nil, fmt.Errorf("line %d: sample %s before HELP and TYPE", n, s.name)
			}
			want := []string{cur}
			if f.typ == "histogram" {
				want = []string{cur + "_bucket", cur + "_sum", cur + "_count"}
			}
			if !contains(want, s.name) {
				return nil, fmt.Errorf("line %d: sample %s inside family %s", n, s.name, cur)
			}
			f.samples = append(f.samples, s)
		}
	}
	for name, f := range fams {
		if len(f.samples) == 0 {
			return nil, fmt.Errorf("family %s has no samples", name)
		}
		if f.typ == "histogram" {
			if err := checkHistogram(name, f.samples); err != nil {
				return nil, err
			}
		}
	}
	return fams, nil
}

func parseSample(line string) (sample, error) {
	s := sample{labels: map[string]string{}}
	i := strings.IndexAny(line, "{ ")
	if i < 0 {
		return s, fmt.Errorf("no value in %q", line)
	}
	s.name = line[:i]
	if !validName(s.name) {
		return s, fmt.Errorf("bad metric name %q", s.name)
	}
	rest := line[i:]
	if rest[0] == '{' {
		rest = rest[1:]
		for {
			eq := strings.IndexByte(rest, '=')
			if eq < 0 || !validName(rest[:eq]) || len(rest) < eq+2 || rest[eq+1] != '"' {
				return s, fmt.Errorf("bad label in %q", line)
			}
			key := rest[:eq]
			rest = rest[eq+2:]
			var val strings.Builder
			for {
				if rest == "" {
					return s, fmt.Errorf("unterminated label value in %q", line)
				}
				c := rest[0]
				rest = rest[1:]
				if c == '"' {
					break
				}
				if c == '\n' {
					return s, fmt.Errorf("raw newline in a label value")
				}
				if c == '\\' {
					if rest == "" {
						return s, fmt.Errorf("dangling escape in %q", line)
					}
					switch rest[0] {
					case '\\':
						val.WriteByte('\\')
					case '"':
						val.WriteByte('"')
					case 'n':
						val.WriteByte('\n')
					default:
						return s, fmt.Errorf("bad escape \\%c in %q", rest[0], line)
					}
					rest = rest[1:]
					continue
				}
				val.WriteByte(c)
			}
			if _, dup := s.labels[key]; dup {
				return s, fmt.Errorf("label %s twice in %q", key, line)
			}
			s.labels[key] = val.String()
			if strings.HasPrefix(rest, ",") {
				rest = rest[1:]
				continue
			}
			if !strings.HasPrefix(rest, "}") {
				return s, fmt.Errorf("label set not closed in %q", line)
			}
			rest = rest[1:]
			break
		}
	}
	if !strings.HasPrefix(rest, " ") || strings.Contains(rest[1:], " ") {
		return s, fmt.Errorf("want exactly one value, no timestamp, in %q", line)
	}
	v, err := parseValue(rest[1:])
	if err != nil {
		return s, fmt.Errorf("value in %q: %v", line, err)
	}
	s.value = v
	return s, nil
}

func parseValue(s string) (float64, error) {
	switch s {
	case "+Inf":
		return math.Inf(1), nil
	case "-Inf":
		return math.Inf(-1), nil
	case "NaN":
		return math.NaN(), nil
	}
	return strconv.ParseFloat(s, 64)
}

// checkHistogram: buckets carry le and only le, bounds rise, counts are
// cumulative, the last bucket is +Inf and equals _count, and _sum and _count
// come once each, after the buckets.
func checkHistogram(name string, ss []sample) error {
	var buckets []sample
	var sum, count *sample
	for i := range ss {
		s := &ss[i]
		switch s.name {
		case name + "_bucket":
			if sum != nil || count != nil {
				return fmt.Errorf("%s: bucket after _sum or _count", name)
			}
			if len(s.labels) != 1 || s.labels["le"] == "" {
				return fmt.Errorf("%s: bucket labels %v", name, s.labels)
			}
			buckets = append(buckets, *s)
		case name + "_sum":
			if sum != nil {
				return fmt.Errorf("%s: two _sum", name)
			}
			sum = s
		case name + "_count":
			if count != nil {
				return fmt.Errorf("%s: two _count", name)
			}
			count = s
		}
	}
	if len(buckets) == 0 || sum == nil || count == nil {
		return fmt.Errorf("%s: want buckets, _sum and _count", name)
	}
	prevLe, prevN := math.Inf(-1), -1.0
	for _, b := range buckets {
		le, err := parseValue(b.labels["le"])
		if err != nil || le <= prevLe {
			return fmt.Errorf("%s: le %q not rising", name, b.labels["le"])
		}
		if b.value < prevN {
			return fmt.Errorf("%s: bucket le=%s count %v below the previous %v", name, b.labels["le"], b.value, prevN)
		}
		prevLe, prevN = le, b.value
	}
	if last := buckets[len(buckets)-1]; last.labels["le"] != "+Inf" || last.value != count.value {
		return fmt.Errorf("%s: last bucket %v = %v, _count %v", name, last.labels, last.value, count.value)
	}
	return nil
}

func validName(s string) bool {
	if s == "" {
		return false
	}
	for i, c := range s {
		ok := c == '_' || c == ':' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || i > 0 && c >= '0' && c <= '9'
		if !ok {
			return false
		}
	}
	return true
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

func find(t *testing.T, f *family, name string, labels map[string]string) float64 {
	t.Helper()
	for _, s := range f.samples {
		if s.name != name || len(s.labels) != len(labels) {
			continue
		}
		match := true
		for k, v := range labels {
			match = match && s.labels[k] == v
		}
		if match {
			return s.value
		}
	}
	t.Fatalf("no sample %s%v", name, labels)
	return 0
}

func TestExpositionIsValid(t *testing.T) {
	m := New("v1.2 \"quoted\" back\\slash\nnewline")
	m.ObserveResult(result.Result{Submission: result.Submission{DownloadMbps: 938.2, UploadMbps: 41, IdleMs: 8, LoadedDownMs: 19, LoadedUpMs: 41.5}, Grade: "B"})
	m.ObserveResult(result.Result{Submission: result.Submission{DownloadMbps: 20000, UploadMbps: 0.5, IdleMs: 3000}}) // above and below every bound
	m.AddSent(1 << 20)
	m.AddReceived(5)
	m.StreamOpened(Upload)
	m.CountRequest(RouteDL, 200)
	m.CountRequest(RouteUI, 404)
	m.CountRequest(RouteUI, 404)
	m.CountRequest(RouteOther, 99) // out of range: dropped

	text := string(m.Append(nil))
	fams, err := parseExposition(text)
	if err != nil {
		t.Fatalf("%v\n%s", err, text)
	}
	for name, typ := range map[string]string{
		"dyno_build_info": "gauge", "dyno_results_total": "counter",
		"dyno_result_download_bytes_per_second": "histogram", "dyno_result_upload_bytes_per_second": "histogram",
		"dyno_result_idle_latency_seconds": "histogram", "dyno_result_loaded_latency_seconds": "histogram",
		"dyno_bytes_sent_total": "counter", "dyno_bytes_received_total": "counter",
		"dyno_streams_active": "gauge", "dyno_requests_total": "counter",
		"process_resident_memory_bytes": "gauge", "process_start_time_seconds": "gauge", "go_goroutines": "gauge",
	} {
		if f := fams[name]; f == nil || f.typ != typ {
			t.Errorf("%s: %+v, want type %s", name, f, typ)
		}
	}
	if len(fams) != 13 {
		t.Errorf("%d families, want 13", len(fams))
	}

	if v := find(t, fams["dyno_build_info"], "dyno_build_info", map[string]string{"version": m.version}); v != 1 {
		t.Errorf("build info %v", v)
	}
	if !strings.Contains(text, `version="v1.2 \"quoted\" back\\slash\nnewline"`) {
		t.Errorf("version label not escaped:\n%s", text)
	}
	for g, want := range map[string]float64{"B": 1, "none": 1, "A+": 0, "F": 0} {
		if v := find(t, fams["dyno_results_total"], "dyno_results_total", map[string]string{"grade": g}); v != want {
			t.Errorf("results grade %s = %v, want %v", g, v, want)
		}
	}
	dl := fams["dyno_result_download_bytes_per_second"]
	for le, want := range map[string]float64{"125000": 0, "1.25e+08": 1, "1.25e+09": 1, "+Inf": 2} {
		if v := find(t, dl, "dyno_result_download_bytes_per_second_bucket", map[string]string{"le": le}); v != want {
			t.Errorf("download le=%s: %v, want %v", le, v, want)
		}
	}
	if v := find(t, dl, "dyno_result_download_bytes_per_second_sum", nil); math.Abs(v-(938.2e6+20000e6)/8) > 1 {
		t.Errorf("download sum %v", v)
	}
	loaded := fams["dyno_result_loaded_latency_seconds"]
	if v := find(t, loaded, "dyno_result_loaded_latency_seconds_count", nil); v != 1 {
		t.Errorf("loaded latency counted %v results, want 1 (the other had none)", v)
	}
	if v := find(t, loaded, "dyno_result_loaded_latency_seconds_bucket", map[string]string{"le": "0.05"}); v != 1 {
		t.Errorf("41.5 ms not in le=0.05: %v", v)
	}
	if v := find(t, fams["dyno_result_idle_latency_seconds"], "dyno_result_idle_latency_seconds_bucket", map[string]string{"le": "2"}); v != 1 {
		t.Errorf("3 s idle latency counted at le=2: %v", v)
	}
	if v := find(t, fams["dyno_bytes_sent_total"], "dyno_bytes_sent_total", nil); v != 1<<20 {
		t.Errorf("sent %v", v)
	}
	if v := find(t, fams["dyno_streams_active"], "dyno_streams_active", map[string]string{"direction": "upload"}); v != 1 {
		t.Errorf("upload streams %v", v)
	}
	req := fams["dyno_requests_total"]
	if len(req.samples) != 2 {
		t.Errorf("requests: %+v", req.samples)
	}
	if v := find(t, req, "dyno_requests_total", map[string]string{"route": "ui", "code": "404"}); v != 2 {
		t.Errorf("ui 404s %v", v)
	}
	start := find(t, fams["process_start_time_seconds"], "process_start_time_seconds", nil)
	if d := float64(time.Now().Unix()) - start; d < -1 || d > 60 {
		t.Errorf("start time %v is %v s from now", start, d)
	}
	if v := find(t, fams["process_resident_memory_bytes"], "process_resident_memory_bytes", nil); v < 1<<20 {
		t.Errorf("rss %v", v)
	}
}

// The checker itself must reject what Prometheus would.
func TestParserRejectsBadInput(t *testing.T) {
	for _, bad := range []string{
		"x 1\n",
		"# HELP x h\nx 1\n",
		"# TYPE x counter\n# HELP x h\nx 1\n",
		"# HELP x h\n# TYPE x counter\nx{a=\"b\\q\"} 1\n",
		"# HELP x h\n# TYPE x counter\nx{a=\"b} 1\n",
		"# HELP x h\n# TYPE x counter\ny 1\n",
		"# HELP x h\n# TYPE x histogram\nx_bucket{le=\"1\"} 2\nx_bucket{le=\"+Inf\"} 1\nx_sum 1\nx_count 1\n",
		"# HELP x h\n# TYPE x histogram\nx_bucket{le=\"1\"} 1\nx_sum 1\nx_count 1\n",
		"# HELP x h\n# TYPE x histogram\nx_bucket{le=\"+Inf\"} 2\nx_sum 1\nx_count 1\n",
		"# HELP x h\n# TYPE x gauge\nx 1",
	} {
		if _, err := parseExposition(bad); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
}

func TestEmptyMetricsAreValid(t *testing.T) {
	if _, err := parseExposition(string(New("dev").Append(nil))); err != nil {
		t.Fatal(err)
	}
	var m *Metrics // nil: counting is a no-op, not a crash
	m.AddSent(1)
	m.StreamOpened(Download)
	m.CountRequest(RouteDL, 200)
	m.ObserveResult(result.Result{})
}

func TestHandlerServesOnlyGetMetrics(t *testing.T) {
	h := New("dev").Handler()
	for _, c := range []struct {
		method, path string
		status       int
	}{
		{"GET", "/metrics", 200},
		{"POST", "/metrics", 404},
		{"HEAD", "/metrics", 404},
		{"GET", "/", 404},
		{"GET", "/metrics/", 404},
		{"GET", "/dl", 404},
	} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(c.method, c.path, nil))
		if w.Code != c.status {
			t.Errorf("%s %s: %d, want %d", c.method, c.path, w.Code, c.status)
		}
		if c.status == 200 && w.Header().Get("Content-Type") != ContentType {
			t.Errorf("Content-Type %q", w.Header().Get("Content-Type"))
		}
	}
}

func TestRouteOfIsLowCardinality(t *testing.T) {
	for pattern, want := range map[string]string{
		"GET /dl": "dl", "POST /ul": "ul", "GET /ping": "ping", "GET /api/v1/plan": "plan",
		"POST /api/v1/results": "results", "GET /api/v1/results/{id}": "results", "GET /": "ui",
		"": "other", "GET /api/v1/results/17": "other",
	} {
		if got := routeNames[RouteOf(pattern)]; got != want {
			t.Errorf("RouteOf(%q) = %s, want %s", pattern, got, want)
		}
	}
}

// The wrapper must keep http.ResponseController working (per-write
// deadlines and flushing) and record the code the client saw.
func TestInstrumentKeepsResponseControllerAndCountsCodes(t *testing.T) {
	m := New("dev")
	mux := http.NewServeMux()
	mux.HandleFunc("GET /dl", func(w http.ResponseWriter, r *http.Request) {
		rc := http.NewResponseController(w)
		if err := rc.SetWriteDeadline(time.Now().Add(time.Second)); err != nil {
			t.Errorf("SetWriteDeadline through the wrapper: %v", err)
		}
		w.Write([]byte("abc"))
		if err := rc.Flush(); err != nil {
			t.Errorf("Flush through the wrapper: %v", err)
		}
	})
	mux.HandleFunc("GET /ping", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) })
	mux.HandleFunc("GET /", func(w http.ResponseWriter, r *http.Request) {}) // writes nothing: 200
	srv := httptest.NewServer(m.Instrument(mux))
	defer srv.Close()
	for _, path := range []string{"/dl", "/ping", "/x", "/x"} {
		resp, err := http.Get(srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
	}
	srv.Close() // waits for handlers, so the counts are final
	for _, c := range []struct {
		route Route
		code  int
		want  uint64
	}{{RouteDL, 200, 1}, {RoutePing, 204, 1}, {RouteUI, 200, 2}} {
		if got := m.requests[c.route][c.code-minCode].Load(); got != c.want {
			t.Errorf("%s %d: %d, want %d", routeNames[c.route], c.code, got, c.want)
		}
	}
}

// Counting on the hot path must not allocate.
func TestHotPathCountersDontAllocate(t *testing.T) {
	m := New("dev")
	if n := testing.AllocsPerRun(1000, func() {
		m.StreamOpened(Download)
		m.AddSent(256 << 10)
		m.AddReceived(64 << 10)
		m.CountRequest(RouteDL, 200)
		m.StreamClosed(Download)
	}); n != 0 {
		t.Fatalf("%v allocations per request", n)
	}
}
