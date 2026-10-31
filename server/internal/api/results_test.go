package api

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"

	"github.com/redlinelabs-dev/dyno/server/internal/result"
	"github.com/redlinelabs-dev/dyno/server/internal/testrun"
)

const submission = `{"test_id":"%s","client":"Safari on iPhone","download_mbps":938.2,"upload_mbps":412,
	"idle_ms":8.1,"loaded_down_ms":19,"loaded_up_ms":41.5,"jitter_ms":0.8,"streams":5,"client_limited":false,
	"detail":{"timeline":[[0,1,2]]},"future_field":true}`

func TestPostResultAttachesServerCountsAndGrade(t *testing.T) {
	mux, a := newMux(t)
	id := testrun.NewID()
	c := a.Runs.Get(id)
	c.Down.Add(5000)
	c.Up.Add(700)

	w := do(mux, "POST", "/api/v1/results", strings.Replace(submission, "%s", id, 1))
	if w.Code != 201 {
		t.Fatalf("status %d: %s", w.Code, w.Body)
	}
	var got result.Result
	json.Unmarshal(w.Body.Bytes(), &got)
	if got.ID == 0 || got.Source != "client" || got.Grade != "B" || got.BufferbloatMs != 33.4 ||
		got.RPM != 1446 || got.ServerBytesDown != 5000 || got.ServerBytesUp != 700 {
		t.Fatalf("stored %+v", got)
	}
	if loc := w.Header().Get("Location"); loc != "/api/v1/results/1" {
		t.Errorf("Location = %q", loc)
	}

	one := do(mux, "GET", "/api/v1/results/1", "")
	if one.Code != 200 || !strings.Contains(one.Body.String(), `"detail":{"timeline":[[0,1,2]]}`) {
		t.Fatalf("GET one: %d %s", one.Code, one.Body)
	}
	list := do(mux, "GET", "/api/v1/results", "")
	var page struct{ Results []result.Result }
	json.Unmarshal(list.Body.Bytes(), &page)
	if len(page.Results) != 1 || page.Results[0].Detail != nil {
		t.Fatalf("list: %s", list.Body)
	}
}

func TestPostResultWithoutKnownTestStillStores(t *testing.T) {
	mux, _ := newMux(t)
	w := do(mux, "POST", "/api/v1/results", strings.Replace(submission, "%s", "nope", 1))
	if w.Code != 201 || !strings.Contains(w.Body.String(), `"server_bytes_down":0`) {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
}

func TestResultErrors(t *testing.T) {
	mux, _ := newMux(t)
	for _, c := range []struct {
		method, path, body string
		status             int
	}{
		{"POST", "/api/v1/results", `not json`, 400},
		{"POST", "/api/v1/results", `{"download_mbps":-5}`, 400},
		{"POST", "/api/v1/results", `{"client":"` + strings.Repeat("x", 300<<10) + `"}`, 400},
		{"GET", "/api/v1/results/99", "", 404},
		{"GET", "/api/v1/results/abc", "", 400},
		{"GET", "/api/v1/results?limit=0", "", 400},
		{"GET", "/api/v1/results?before=x", "", 400},
	} {
		if w := do(mux, c.method, c.path, c.body); w.Code != c.status {
			t.Errorf("%s %s: status %d, want %d (%s)", c.method, c.path, w.Code, c.status, w.Body)
		}
	}
}

// logTo sends a's events to a buffer as JSON lines.
func logTo(a *API) *bytes.Buffer {
	var b bytes.Buffer
	a.Log = slog.New(slog.NewJSONHandler(&b, nil))
	return &b
}

func oneEvent(t *testing.T, b *bytes.Buffer) map[string]any {
	t.Helper()
	lines := strings.Split(strings.TrimSuffix(b.String(), "\n"), "\n")
	if len(lines) != 1 {
		t.Fatalf("want one log line, got %d:\n%s", len(lines), b)
	}
	var ev map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &ev); err != nil {
		t.Fatalf("not JSON: %s", lines[0])
	}
	for k, v := range ev {
		switch v.(type) {
		case map[string]any, []any:
			t.Errorf("%s is nested: %v (ADR-0020: primitives only)", k, v)
		}
	}
	return ev
}

// ADR-0020: one result.store event per stored result, flat keys, units in
// the names, and no client label, IP or user agent.
func TestStoreEventOnSuccess(t *testing.T) {
	mux, a := newMux(t)
	logs := logTo(a)
	id := testrun.NewID()
	a.Runs.Get(id).Down.Add(5000)
	if w := do(mux, "POST", "/api/v1/results", strings.Replace(submission, "%s", id, 1)); w.Code != 201 {
		t.Fatalf("status %d", w.Code)
	}
	ev := oneEvent(t, logs)
	for k, want := range map[string]any{
		"level": "INFO", "msg": "result.store", "result": "succeeded", "status_code": 201.0, "source": "client",
		"result_id": 1.0, "grade": "B", "download_bits_per_second": 938.2e6, "upload_bits_per_second": 412e6,
		"idle_latency_ms": 8.1, "loaded_latency_ms": 41.5, "jitter_ms": 0.8, "bufferbloat_ms": 33.4,
		"server_bytes_sent": 5000.0, "server_bytes_received": 0.0, "client_limited": false, "streams": 5.0,
	} {
		if ev[k] != want {
			t.Errorf("%s = %v (%T), want %v", k, ev[k], ev[k], want)
		}
	}
	if d, ok := ev["duration_ms"].(float64); !ok || d < 0 {
		t.Errorf("duration_ms = %v", ev["duration_ms"])
	}
	for _, k := range []string{"client", "error.code", "error.message", "remote_addr", "user_agent"} {
		if _, ok := ev[k]; ok {
			t.Errorf("unexpected key %s", k)
		}
	}
}

func TestStoreEventOnFailure(t *testing.T) {
	for _, c := range []struct {
		body, level, code string
		figures           bool
	}{
		{`not json`, "WARN", "invalid_json", false},
		{`{"client":"` + strings.Repeat("x", 300<<10) + `"}`, "WARN", "body_too_large", false},
		{`{"download_mbps":-5}`, "WARN", "invalid_result", false},
	} {
		mux, a := newMux(t)
		logs := logTo(a)
		do(mux, "POST", "/api/v1/results", c.body)
		ev := oneEvent(t, logs)
		if ev["msg"] != "result.store" || ev["level"] != c.level || ev["result"] != "failed" ||
			ev["error.code"] != c.code || ev["error.message"] == "" || ev["status_code"] != 400.0 {
			t.Errorf("%s: %v", c.code, ev)
		}
		if _, has := ev["download_bits_per_second"]; has != c.figures {
			t.Errorf("%s: figures logged %v, want %v", c.code, has, c.figures)
		}
		if _, has := ev["duration_ms"]; !has {
			t.Errorf("%s: no duration_ms", c.code)
		}
	}

	// A write the database refuses is the operator's problem: error, not warn.
	mux, a := newMux(t)
	logs := logTo(a)
	a.Store.Close()
	if w := do(mux, "POST", "/api/v1/results", strings.Replace(submission, "%s", "x", 1)); w.Code != 500 {
		t.Fatalf("status %d", w.Code)
	}
	ev := oneEvent(t, logs)
	if ev["level"] != "ERROR" || ev["error.code"] != "store_write_failed" || ev["result"] != "failed" {
		t.Errorf("store failure: %v", ev)
	}
}
