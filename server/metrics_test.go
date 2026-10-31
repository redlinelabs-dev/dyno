package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"testing"
)

// startRun runs the server as main does, on loopback ports, and returns the
// bound addresses. Its log is JSON in logs, safe to read once stop returns.
func startRun(t *testing.T, cfg config) (addr, metricsAddr net.Addr, logs *bytes.Buffer, stop func() error) {
	t.Helper()
	logs = &bytes.Buffer{}
	log := slog.New(slog.NewJSONHandler(logs, nil))
	ctx, cancel := context.WithCancel(context.Background())
	up := make(chan [2]net.Addr, 1)
	done := make(chan error, 1)
	go func() {
		done <- run(ctx, cfg, log, func(a, m net.Addr) { up <- [2]net.Addr{a, m} })
	}()
	select {
	case a := <-up:
		stop = sync.OnceValue(func() error { cancel(); return <-done })
		t.Cleanup(func() { stop() })
		return a[0], a[1], logs, stop
	case err := <-done:
		cancel()
		t.Fatalf("run: %v\n%s", err, logs)
	}
	return nil, nil, nil, nil
}

func TestMetricsOffByDefault(t *testing.T) {
	cfg := configFromEnv(func(string) string { return "" })
	if cfg.metricsListen != "" || cfg.listen != defaultListen {
		t.Fatalf("default config %+v", cfg)
	}
	cfg.listen, cfg.data = "127.0.0.1:0", t.TempDir()
	addr, metricsAddr, logs, stop := startRun(t, cfg)
	if metricsAddr != nil {
		t.Fatalf("metrics listener on %v with DYNO_METRICS_LISTEN unset", metricsAddr)
	}
	// Never on the main port.
	resp, err := http.Get("http://" + addr.String() + "/metrics")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 404 || bytes.Contains(body, []byte("dyno_")) {
		t.Fatalf("main port /metrics: %d %.80q", resp.StatusCode, body)
	}
	if err := stop(); err != nil {
		t.Fatal(err)
	}

	events := decodeLines(t, logs)
	if len(events) != 2 || events[0]["msg"] != "server.start" || events[1]["msg"] != "server.stop" {
		t.Fatalf("events: %s", logs)
	}
	start := events[0]
	for k, want := range map[string]any{"level": "INFO", "result": "succeeded", "version": version,
		"listen_address": addr.String(), "metrics_listen_address": "", "data_dir": cfg.data} {
		if start[k] != want {
			t.Errorf("server.start %s = %v, want %v", k, start[k], want)
		}
	}
	if events[1]["result"] != "succeeded" {
		t.Errorf("server.stop: %v", events[1])
	}
}

// The operator asked for metrics, so an address that can't be bound is fatal.
func TestMetricsListenFailureStopsServer(t *testing.T) {
	taken, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer taken.Close()
	var logs bytes.Buffer
	cfg := config{listen: "127.0.0.1:0", metricsListen: taken.Addr().String(), data: t.TempDir()}
	err = run(context.Background(), cfg, slog.New(slog.NewJSONHandler(&logs, nil)), func(net.Addr, net.Addr) {
		t.Error("server came up with its metrics address taken")
	})
	if err == nil {
		t.Fatal("run returned nil")
	}
	ev := decodeLines(t, &logs)
	if len(ev) != 1 || ev[0]["msg"] != "server.start" || ev[0]["level"] != "ERROR" ||
		ev[0]["result"] != "failed" || ev[0]["error.code"] != "metrics_listen_failed" {
		t.Fatalf("log: %s", &logs)
	}
}

func TestMetricsCountRealTraffic(t *testing.T) {
	cfg := config{listen: "127.0.0.1:0", metricsListen: "127.0.0.1:0", data: t.TempDir()}
	addr, metricsAddr, _, _ := startRun(t, cfg)
	base, mbase := "http://"+addr.String(), "http://"+metricsAddr.String()

	resp, err := http.Get(base + "/api/v1/plan")
	if err != nil {
		t.Fatal(err)
	}
	var plan struct {
		TestID string `json:"test_id"`
	}
	json.NewDecoder(resp.Body).Decode(&plan)
	resp.Body.Close()

	mustDo(t, "GET", base+"/dl?bytes=1000&t="+plan.TestID, "", 200)
	mustDo(t, "GET", base+"/dl?bytes=234", "", 200) // untagged bytes count too
	mustDo(t, "POST", base+"/ul?t="+plan.TestID, strings.Repeat("x", 500), 200)
	mustDo(t, "POST", base+"/ul", strings.Repeat("y", 66), 200)
	mustDo(t, "GET", base+"/ping", "", 204)
	mustDo(t, "GET", base+"/nope", "", 404)
	mustDo(t, "DELETE", base+"/dl", "", 405)
	mustDo(t, "POST", base+"/api/v1/results", `{"test_id":"`+plan.TestID+`","download_mbps":938.2,"upload_mbps":41,
		"idle_ms":8,"loaded_down_ms":19,"loaded_up_ms":41.5,"streams":5}`, 201)
	mustDo(t, "POST", base+"/api/v1/results", `{"download_mbps":12}`, 201) // no loaded latency: grade none
	mustDo(t, "POST", base+"/api/v1/results", `nope`, 400)                 // not stored, not counted

	resp, err = http.Get(mbase + "/metrics")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || resp.Header.Get("Content-Type") != "text/plain; version=0.0.4; charset=utf-8" {
		t.Fatalf("scrape: %d %s", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
	got := samples(string(body))
	for series, want := range map[string]float64{
		`dyno_build_info{version="dev"}`:                              1,
		`dyno_bytes_sent_total`:                                       1234,
		`dyno_bytes_received_total`:                                   566,
		`dyno_streams_active{direction="download"}`:                   0,
		`dyno_streams_active{direction="upload"}`:                     0,
		`dyno_results_total{grade="B"}`:                               1,
		`dyno_results_total{grade="none"}`:                            1,
		`dyno_results_total{grade="A+"}`:                              0,
		`dyno_result_download_bytes_per_second_count`:                 2,
		`dyno_result_download_bytes_per_second_bucket{le="1.25e+08"}`: 2,
		`dyno_result_download_bytes_per_second_bucket{le="6.25e+07"}`: 1,
		`dyno_result_upload_bytes_per_second_sum`:                     41e6 / 8,
		`dyno_result_loaded_latency_seconds_count`:                    1, // the second result had none
		`dyno_result_loaded_latency_seconds_sum`:                      0.0415,
		`dyno_result_idle_latency_seconds_bucket{le="0.01"}`:          2,
		`dyno_requests_total{route="plan",code="200"}`:                1,
		`dyno_requests_total{route="dl",code="200"}`:                  2,
		`dyno_requests_total{route="ul",code="200"}`:                  2,
		`dyno_requests_total{route="ping",code="204"}`:                1,
		`dyno_requests_total{route="ui",code="404"}`:                  1,
		`dyno_requests_total{route="other",code="405"}`:               1,
		`dyno_requests_total{route="results",code="201"}`:             2,
		`dyno_requests_total{route="results",code="400"}`:             1,
	} {
		if v, ok := got[series]; !ok || v != want {
			t.Errorf("%s = %v (present %v), want %v", series, v, ok, want)
		}
	}
	for _, name := range []string{"process_resident_memory_bytes", "process_start_time_seconds", "go_goroutines"} {
		if got[name] <= 0 {
			t.Errorf("%s = %v", name, got[name])
		}
	}
	for s := range got {
		if strings.Contains(s, "/nope") {
			t.Errorf("raw path in a label: %s", s)
		}
	}

	// The metrics listener answers GET /metrics and nothing else.
	mustDo(t, "GET", mbase+"/", "", 404)
	mustDo(t, "POST", mbase+"/metrics", "", 404)
	mustDo(t, "GET", mbase+"/dl?bytes=1", "", 404)
}

func mustDo(t *testing.T, method, url, body string, status int) {
	t.Helper()
	req, _ := http.NewRequest(method, url, strings.NewReader(body))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if resp.StatusCode != status {
		t.Fatalf("%s %s: status %d, want %d", method, url, resp.StatusCode, status)
	}
}

// samples maps each sample's series (name and labels as written) to its
// value. The format itself is checked by the metrics package's tests.
func samples(text string) map[string]float64 {
	out := map[string]float64{}
	for line := range strings.Lines(text) {
		line = strings.TrimSpace(line)
		if line == "" || line[0] == '#' {
			continue
		}
		i := strings.LastIndexByte(line, ' ')
		v, err := strconv.ParseFloat(line[i+1:], 64)
		if err == nil {
			out[line[:i]] = v
		}
	}
	return out
}

func decodeLines(t *testing.T, b *bytes.Buffer) []map[string]any {
	t.Helper()
	var out []map[string]any
	for line := range strings.Lines(b.String()) {
		var ev map[string]any
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			t.Fatalf("not a JSON line: %q", line)
		}
		out = append(out, ev)
	}
	return out
}
