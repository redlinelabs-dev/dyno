// Package api serves the versioned JSON API under /api/v1. Every client, the
// web UI included, uses only these routes (ADR-0002).
package api

import (
	"encoding/json"
	"net/http"
)

// Plan is the test plan the server issues. A client follows it rather than
// hard-coding endpoints or stream counts, so the server can tune a test for
// every client at once. Clients ignore fields they don't know.
type Plan struct {
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
// fast.com's and Cloudflare's roughly one-second requests. M2 tunes them
// against real links.
func NewPlan(version string) Plan {
	t := Transfer{
		Streams:         5,
		DurationMs:      10_000,
		WarmupMs:        1_500,
		StartBytes:      1 << 20,
		MaxBytes:        256 << 20,
		TargetRequestMs: 1_000,
	}
	up := t
	up.WarmupMs = 3_000
	return Plan{
		Server:    ServerInfo{Name: "dyno", Version: version},
		Endpoints: Endpoints{Download: "/dl", Upload: "/ul", Ping: "/ping"},
		Latency:   Latency{IdleSamples: 20, LoadedIntervalMs: 200},
		Download:  t,
		Upload:    up,
	}
}

// Register adds the /api/v1 routes to mux.
func Register(mux *http.ServeMux, version string) {
	plan, err := json.Marshal(NewPlan(version))
	if err != nil {
		panic(err)
	}
	plan = append(plan, '\n')
	mux.HandleFunc("GET /api/v1/plan", func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Type", "application/json")
		h.Set("Cache-Control", "no-store")
		w.Write(plan)
	})
}
