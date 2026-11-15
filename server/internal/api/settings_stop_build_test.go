package api_test

import (
	"context"
	"encoding/json"
	"log/slog"
	"testing"
	"time"

	"github.com/redlinelabs-dev/dyno/server/internal/api"
	"github.com/redlinelabs-dev/dyno/server/internal/result"
	"github.com/redlinelabs-dev/dyno/server/internal/settings"
	"github.com/redlinelabs-dev/dyno/server/internal/store"
	"github.com/redlinelabs-dev/dyno/server/internal/testrun"
)

// These tests are about a settings PATCH that prunes while the server is
// stopping (ADR-0030: a prune stops between batches when the server stops;
// ADR-0020: nothing is logged after server.stop). API.Stop is the signal.

// ssOldRows is how many old results the tests store: three batches of 500
// (store.PageSize) and a few over.
const ssOldRows = 3*store.PageSize + 50

// ssEnv serves an API over a store holding ssOldRows results 40 days old and
// one a minute old, with stop as its Stop.
func ssEnv(t *testing.T, stop context.Context) *evEnv {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	now := time.Now().UTC()
	for i := range ssOldRows {
		histAdd(t, st, histRes(now.Add(-40*24*time.Hour+time.Duration(i)*time.Second), "client", ""))
	}
	histAdd(t, st, histRes(now.Add(-time.Minute), "client", ""))

	logs := &evLog{}
	a := &api.API{
		Version: "1.2.3", Store: st, Runs: testrun.NewRegistry(),
		Log: slog.New(slog.NewJSONHandler(logs, nil)), Stop: stop,
	}
	cl := schServe(t, a)
	return &evEnv{schEnv: &schEnv{schClient: cl, st: st}, dir: dir, logs: logs}
}

// ssCount is how many results the store holds.
func ssCount(t *testing.T, st *store.Store) int {
	t.Helper()
	n := 0
	err := st.Query(store.Filter{}, func(result.Result) error { n++; return nil })
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func TestSs_PatchWithAStoppedServerAnswers200AndLeavesTheOldResults(t *testing.T) {
	defer schNoPanic(t)
	stop, stopNow := context.WithCancel(context.Background())
	stopNow()
	env := ssEnv(t, stop)

	r, lines := env.call(t, "PATCH", "/api/v1/settings", `{"keep_days": 7}`)
	r.wantStatus(t, 200)
	var reply settings.Settings
	if err := json.Unmarshal(r.Body, &reply); err != nil || reply.KeepDays != 7 {
		t.Errorf("the reply is %s, want every setting with keep_days 7", r.Body)
	}
	if saved, err := env.st.Settings(); err != nil || saved.KeepDays != 7 {
		t.Errorf("the store has %+v (err %v), want keep_days 7 saved", saved, err)
	}
	// Stop was done before the PATCH, so not one batch may have run.
	if n := ssCount(t, env.st); n != ssOldRows+1 {
		t.Errorf("%d results left, want all %d: the prune should have stopped before its first batch", n, ssOldRows+1)
	}

	patches := evNamed(lines, "settings.patch")
	if len(patches) != 1 || patches[0]["status_code"] != 200.0 {
		t.Errorf("settings.patch events: %v, want one with status_code 200", patches)
	}
	// A prune stopped before its first batch logs nothing; one stopped later
	// logs canceled. Never succeeded or failed.
	for _, p := range evNamed(lines, "results.prune") {
		if p["result"] != "canceled" {
			t.Errorf("results.prune: %v, want result canceled", p)
		}
	}
}

func TestSs_PatchWithNoStopDeletesEveryOldResult(t *testing.T) {
	defer schNoPanic(t)
	env := ssEnv(t, nil)

	r, lines := env.call(t, "PATCH", "/api/v1/settings", `{"keep_days": 7}`)
	r.wantStatus(t, 200)
	if n := ssCount(t, env.st); n != 1 {
		t.Errorf("%d results left, want only the fresh one", n)
	}
	prunes := evNamed(lines, "results.prune")
	if len(prunes) != 1 || prunes[0]["result"] != "succeeded" || prunes[0]["deleted_results"] != float64(ssOldRows) {
		t.Errorf("results.prune events: %v, want one succeeded deleting %d", prunes, ssOldRows)
	}
	if patches := evNamed(lines, "settings.patch"); len(patches) != 1 || patches[0]["status_code"] != 200.0 {
		t.Errorf("settings.patch events: %v, want one with status_code 200", patches)
	}
}
