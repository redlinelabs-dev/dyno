// Package api serves the versioned JSON API under /api/v1. Every client, the
// web UI included, uses only these routes (ADR-0002).
package api

import (
	"encoding/json"
	"net/http"

	"github.com/redlinelabs-dev/dyno/server/internal/store"
	"github.com/redlinelabs-dev/dyno/server/internal/testrun"
)

// Plan is the test plan the server issues. A client follows it rather than
// hard-coding endpoints or stream counts, so the server can tune a test for
// every client at once. Clients ignore fields they don't know.
type Plan struct {
	// TestID tags this run's /dl and /ul requests (?t=) and its result, so
	// the server can store its own byte counts with the result.
	TestID    string     `json:"test_id"`
	Server    ServerInfo `json:"server"`
	Endpoints Endpoints  `json:"endpoints"`
	Latency   Latency    `json:"latency"`
	Download  Transfer   `json:"download"`
	Upload    Transfer   `json:"upload"`
}

type ServerInfo struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// Endpoints are paths relative to the origin that served the plan.
type Endpoints struct {
	Download string `json:"download"`
	Upload   string `json:"upload"`
	Ping     string `json:"ping"`
	Results  string `json:"results"`
}

// Latency says how to probe. Probes always run on their own connection,
// never on a load stream (ADR-0003).
type Latency struct {
	IdleSamples      int `json:"idle_samples"`
	LoadedIntervalMs int `json:"loaded_interval_ms"`
}

// Transfer describes one direction. Each stream sends requests back to back.
// A stream starts at StartBytes per request and doubles the size until one
// request takes TargetRequestMs, never above MaxBytes. Samples from the first
// WarmupMs are discarded.
type Transfer struct {
	Streams         int   `json:"streams"`
	DurationMs      int   `json:"duration_ms"`
	WarmupMs        int   `json:"warmup_ms"`
	StartBytes      int64 `json:"start_bytes"`
	MaxBytes        int64 `json:"max_bytes"`
	TargetRequestMs int   `json:"target_request_ms"`
}

// NewPlan returns the default plan. The numbers come from
// docs/research/methodology-and-architecture.md: 5 load streams leave one of
// a browser's 6 per-host connections for probes, the warmups match
// LibreSpeed's slow-start grace periods, and the request sizing follows
// fast.com's and Cloudflare's roughly one-second requests. Upload requests
// stop at 32 MiB because a browser holds the whole body in memory.
func NewPlan(version, testID string) Plan {
	down := Transfer{
		Streams:         5,
		DurationMs:      10_000,
		WarmupMs:        1_500,
		StartBytes:      1 << 20,
		MaxBytes:        256 << 20,
		TargetRequestMs: 1_000,
	}
	up := down
	up.WarmupMs = 3_000
	up.MaxBytes = 32 << 20
	return Plan{
		TestID:    testID,
		Server:    ServerInfo{Name: "dyno", Version: version},
		Endpoints: Endpoints{Download: "/dl", Upload: "/ul", Ping: "/ping", Results: "/api/v1/results"},
		Latency:   Latency{IdleSamples: 20, LoadedIntervalMs: 200},
		Download:  down,
		Upload:    up,
	}
}

// API holds what the /api/v1 handlers need.
type API struct {
	Version string
	Store   *store.Store
	Runs    *testrun.Registry
}

// Register adds the /api/v1 routes to mux.
func (a *API) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/plan", a.plan)
	mux.HandleFunc("POST /api/v1/results", a.addResult)
	mux.HandleFunc("GET /api/v1/results", a.listResults)
	mux.HandleFunc("GET /api/v1/results/{id}", a.getResult)
}

func (a *API) plan(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, NewPlan(a.Version, testrun.NewID()))
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	h := w.Header()
	h.Set("Content-Type", "application/json")
	h.Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}
