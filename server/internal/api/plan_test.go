package api

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/redlinelabs-dev/dyno/server/internal/store"
	"github.com/redlinelabs-dev/dyno/server/internal/testrun"
)

func newMux(t *testing.T) (*http.ServeMux, *API) {
	t.Helper()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	a := &API{Version: "1.2.3", Store: st, Runs: testrun.NewRegistry(), Log: slog.New(slog.DiscardHandler)}
	mux := http.NewServeMux()
	a.Register(mux)
	return mux, a
}

func do(mux http.Handler, method, path, body string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest(method, path, strings.NewReader(body)))
	return w
}

// The plan is a public contract with native clients: changing a field name or
// removing one breaks them. Adding a field means updating this string.
const wantPlan = `{"test_id":"<id>","server":{"name":"dyno","version":"1.2.3"},` +
	`"endpoints":{"download":"/dl","upload":"/ul","ping":"/ping","results":"/api/v1/results"},` +
	`"latency":{"idle_samples":20,"loaded_interval_ms":200},` +
	`"download":{"streams":5,"duration_ms":10000,"warmup_ms":1500,"start_bytes":1048576,"max_bytes":268435456,"target_request_ms":1000},` +
	`"upload":{"streams":5,"duration_ms":10000,"warmup_ms":3000,"start_bytes":262144,"max_bytes":33554432,"target_request_ms":1000}}` + "\n"

func TestPlan(t *testing.T) {
	mux, _ := newMux(t)
	w := do(mux, "GET", "/api/v1/plan", "")
	body, _ := io.ReadAll(w.Body)
	if w.Code != 200 || w.Header().Get("Content-Type") != "application/json" {
		t.Fatalf("status %d, Content-Type %q", w.Code, w.Header().Get("Content-Type"))
	}
	var p Plan
	json.Unmarshal(body, &p)
	if !testrun.ValidID(p.TestID) {
		t.Fatalf("test_id %q is not a valid id", p.TestID)
	}
	if got := strings.Replace(string(body), p.TestID, "<id>", 1); got != wantPlan {
		t.Fatalf("plan changed:\n got %s\nwant %s", got, wantPlan)
	}
	again := do(mux, "GET", "/api/v1/plan", "")
	if bytes.Contains(again.Body.Bytes(), []byte(p.TestID)) {
		t.Fatal("two plans share a test_id")
	}
}

// ADR-0003: at most 5 load streams, so one of a browser's 6 per-host
// connections stays free for latency probes.
func TestPlanStreamCap(t *testing.T) {
	p := NewPlan("x", "y")
	for _, tr := range []Transfer{p.Download, p.Upload} {
		if tr.Streams < 1 || tr.Streams > 5 {
			t.Fatalf("streams = %d, want 1..5", tr.Streams)
		}
	}
}
