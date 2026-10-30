package api

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

// The plan is a public contract with native clients: changing a field name or
// removing one breaks them. Adding a field means updating this string.
const wantPlan = `{"server":{"name":"dyno","version":"1.2.3"},` +
	`"endpoints":{"download":"/dl","upload":"/ul","ping":"/ping"},` +
	`"latency":{"idle_samples":20,"loaded_interval_ms":200},` +
	`"download":{"streams":5,"duration_ms":10000,"warmup_ms":1500,"start_bytes":1048576,"max_bytes":268435456,"target_request_ms":1000},` +
	`"upload":{"streams":5,"duration_ms":10000,"warmup_ms":3000,"start_bytes":1048576,"max_bytes":268435456,"target_request_ms":1000}}` + "\n"

func TestPlan(t *testing.T) {
	mux := http.NewServeMux()
	Register(mux, "1.2.3")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("GET", "/api/v1/plan", nil))
	body, _ := io.ReadAll(w.Body)
	if w.Code != 200 || w.Header().Get("Content-Type") != "application/json" {
		t.Fatalf("status %d, Content-Type %q", w.Code, w.Header().Get("Content-Type"))
	}
	if string(body) != wantPlan {
		t.Fatalf("plan changed:\n got %s\nwant %s", body, wantPlan)
	}
}

// ADR-0003: at most 5 load streams, so one of a browser's 6 per-host
// connections stays free for latency probes.
func TestPlanStreamCap(t *testing.T) {
	p := NewPlan("x")
	for _, tr := range []Transfer{p.Download, p.Upload} {
		if tr.Streams < 1 || tr.Streams > 5 {
			t.Fatalf("streams = %d, want 1..5", tr.Streams)
		}
	}
}
