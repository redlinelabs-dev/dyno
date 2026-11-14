package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/redlinelabs-dev/dyno/server/internal/nodetest"
	"github.com/redlinelabs-dev/dyno/server/internal/store"
)

// silentFarEnd listens on loopback, accepts connections and never answers
// them. accepted gets one value per connection, so a test knows a check has
// reached it.
func silentFarEnd(t *testing.T) (addr string, accepted <-chan struct{}) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	got := make(chan struct{}, 16)
	var mu sync.Mutex
	var conns []net.Conn
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			mu.Lock()
			conns = append(conns, c)
			mu.Unlock()
			select {
			case got <- struct{}{}:
			default:
			}
		}
	}()
	t.Cleanup(func() {
		ln.Close()
		mu.Lock()
		defer mu.Unlock()
		for _, c := range conns {
			c.Close()
		}
	})
	return ln.Addr().String(), got
}

type shutdownReply struct {
	status     int
	retryAfter string
	body       map[string]any
	err        error
}

// shutdownPost sends body to url and reports what came back.
func shutdownPost(url, body string) shutdownReply {
	c := &http.Client{Timeout: 20 * time.Second}
	resp, err := c.Post(url, "application/json", strings.NewReader(body))
	if err != nil {
		return shutdownReply{err: err}
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	r := shutdownReply{status: resp.StatusCode, retryAfter: resp.Header.Get("Retry-After")}
	json.Unmarshal(raw, &r.body)
	return r
}

// A far-end check still waiting on a far end that never answers when shutdown
// starts ends with the shutdown: its client gets a 503 with Retry-After while
// the server is still up, and nothing is logged after server.stop. Without
// that, Shutdown waits out its grace, Close cuts the connection, the client
// reads EOF, and the handler logs schedule.add after server.stop.
func TestSx_AFarEndCheckInFlightEndsWithShutdown(t *testing.T) {
	far, accepted := silentFarEnd(t)
	cfg := config{listen: "127.0.0.1:0", data: t.TempDir()}
	addr, _, logs, stop := startRun(t, cfg)
	base := "http://" + addr.String()
	body := `{"far_end":"http://` + far + `"}`

	add := make(chan shutdownReply, 1)
	check := make(chan shutdownReply, 1)
	go func() { add <- shutdownPost(base+"/api/v1/schedules", body) }()
	go func() { check <- shutdownPost(base+"/api/v1/schedules/check", body) }()
	for range 2 {
		select {
		case <-accepted:
		case r := <-add:
			t.Fatalf("POST /schedules answered %+v before shutdown", r)
		case r := <-check:
			t.Fatalf("POST /schedules/check answered %+v before shutdown", r)
		case <-time.After(5 * time.Second):
			t.Fatal("the far end never saw both checks")
		}
	}

	began := time.Now()
	if err := stop(); err != nil {
		t.Fatal(err)
	}
	if took := time.Since(began); took > shutdownGrace/2 {
		t.Errorf("shutdown took %v with checks in flight, want well inside the %v grace", took, shutdownGrace)
	}

	for name, ch := range map[string]chan shutdownReply{"POST /schedules": add, "POST /schedules/check": check} {
		var r shutdownReply
		select {
		case r = <-ch:
		case <-time.After(5 * time.Second):
			t.Fatalf("%s never got an answer", name)
		}
		if r.err != nil {
			t.Errorf("%s: no answer, %v (the connection was cut)", name, r.err)
			continue
		}
		if r.status != http.StatusServiceUnavailable || r.retryAfter != "1" {
			t.Errorf("%s: status %d, Retry-After %q, want 503 and 1", name, r.status, r.retryAfter)
		}
		if msg, _ := r.body["error"].(string); msg == "" {
			t.Errorf("%s: body %v has no error sentence", name, r.body)
		}
	}

	events := decodeLines(t, logs)
	last := events[len(events)-1]
	if last["msg"] != "server.stop" || last["result"] != "succeeded" {
		t.Errorf("the last event is %v, want a succeeded server.stop", last)
	}
	var logged bool
	for _, ev := range events {
		if ev["msg"] != "schedule.add" {
			continue
		}
		logged = true
		if ev["level"] != "WARN" || ev["status_code"] != 503.0 || ev["result"] != "failed" || ev["error.code"] != "shutting_down" {
			t.Errorf("schedule.add: %v, want WARN, failed, 503, shutting_down", ev)
		}
	}
	if !logged {
		t.Errorf("no schedule.add event before server.stop: %s", logs)
	}
}

// endsWith cancels the check's context when stop is done and leaves nothing
// behind when it isn't.
func TestSx_EndsWithCancelsTheCheckOnStopOnly(t *testing.T) {
	stopCtx, cancelStop := context.WithCancel(context.Background())
	ran := make(chan error, 1)
	f := endsWith(stopCtx, func(ctx context.Context, _ string) (nodetest.FarEndInfo, error) {
		<-ctx.Done()
		ran <- ctx.Err()
		return nodetest.FarEndInfo{}, ctx.Err()
	})
	go f(context.Background(), "x")
	cancelStop()
	select {
	case err := <-ran:
		if err == nil {
			t.Error("the check's context ended with no error")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the check's context didn't end when stop did")
	}

	// A check that returns on its own leaves stop's context alone.
	live, cancelLive := context.WithCancel(context.Background())
	defer cancelLive()
	g := endsWith(live, func(ctx context.Context, _ string) (nodetest.FarEndInfo, error) {
		return nodetest.FarEndInfo{Name: "ok"}, nil
	})
	if info, err := g(context.Background(), "x"); err != nil || info.Name != "ok" {
		t.Errorf("a check that passed returned %+v, %v", info, err)
	}
	if live.Err() != nil {
		t.Error("a finished check cancelled stop's context")
	}
}

// shutdownOldRows is how many old results the PATCH test seeds: 40 batches
// of 500, so a prune that shutdown stops leaves rows behind, and one it
// doesn't either deletes them all or (on a slow run) outlasts the grace and
// cuts the reply. Either way the test fails without the stop.
const shutdownOldRows = 20_000

// seedOldResults stores n results 40 days old in dir's database, by one
// statement, and returns a connection to the file for the test to watch it.
func seedOldResults(t *testing.T, dir string, n int) *sql.DB {
	t.Helper()
	st, err := store.Open(dir) // makes the file and its schema
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", "file:"+filepath.Join(dir, "dyno.db")+"?_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	db.SetMaxOpenConns(2)
	base := time.Now().Add(-40 * 24 * time.Hour).UnixMilli()
	_, err = db.Exec(`WITH RECURSIVE n(i) AS (SELECT 0 UNION ALL SELECT i+1 FROM n WHERE i < ?)
		INSERT INTO results (at, source, client, device, test_id, download_mbps, upload_mbps, idle_ms,
			loaded_down_ms, loaded_up_ms, jitter_ms, streams, client_limited, bufferbloat_ms, grade, rpm,
			server_bytes_down, server_bytes_up, schedule_id, far_end, path_changed,
			failure_code, failure_phase, failure_message)
		SELECT ?+i, 'client', 'c', '', '', 0, 0, 0, 0, 0, 0, 0, 0, 0, '', 0, 0, 0, 0, '', 0, '', '', '' FROM n`,
		n-1, base)
	if err != nil {
		t.Fatal(err)
	}
	return db
}

// A keep_days PATCH whose prune is still deleting when shutdown starts is
// stopped between batches: the client gets its 200 and the saved settings
// within the grace period, run returns, and nothing is logged after
// server.stop. Without that, Shutdown waits out its grace, Close cuts the
// connection, the client reads EOF, and settings.patch is logged after
// server.stop.
func TestSx_APatchPruneInFlightEndsWithShutdown(t *testing.T) {
	dir := t.TempDir()
	db := seedOldResults(t, dir, shutdownOldRows)
	addr, _, logs, stop := startRun(t, config{listen: "127.0.0.1:0", data: dir})

	type timed struct {
		shutdownReply
		at time.Time
	}
	reply := make(chan timed, 1)
	go func() {
		req, err := http.NewRequest(http.MethodPatch, "http://"+addr.String()+"/api/v1/settings", strings.NewReader(`{"keep_days": 7}`))
		if err != nil {
			reply <- timed{shutdownReply{err: err}, time.Now()}
			return
		}
		req.Header.Set("Content-Type", "application/json")
		resp, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
		if err != nil {
			reply <- timed{shutdownReply{err: err}, time.Now()}
			return
		}
		defer resp.Body.Close()
		raw, _ := io.ReadAll(resp.Body)
		r := shutdownReply{status: resp.StatusCode}
		json.Unmarshal(raw, &r.body)
		reply <- timed{r, time.Now()}
	}()

	// Begin shutdown once the first batch has gone, so the prune is under way.
	deadline := time.After(15 * time.Second)
	for {
		var first int64
		if err := db.QueryRow(`SELECT COALESCE(MIN(id), 0) FROM results`).Scan(&first); err != nil {
			t.Fatal(err)
		}
		if first > 1 {
			break
		}
		select {
		case r := <-reply:
			t.Fatalf("PATCH answered %+v before any batch was seen", r.shutdownReply)
		case <-deadline:
			t.Fatal("the PATCH's prune never deleted a batch")
		case <-time.After(2 * time.Millisecond):
		}
	}

	began := time.Now()
	if err := stop(); err != nil {
		t.Fatal(err)
	}
	if took := time.Since(began); took > 4*time.Second {
		t.Errorf("run took %v to return with a prune in flight, want under 4s", took)
	}

	var r timed
	select {
	case r = <-reply:
	case <-time.After(5 * time.Second):
		t.Fatal("the PATCH never got an answer")
	}
	if r.err != nil {
		t.Fatalf("the PATCH got no answer, %v (the connection was cut)", r.err)
	}
	if r.status != http.StatusOK {
		t.Errorf("the PATCH answered %d, want 200", r.status)
	}
	if got, _ := r.body["keep_days"].(float64); got != 7 {
		t.Errorf("the PATCH's reply is %v, want keep_days 7", r.body)
	}
	if late := r.at.Sub(began); late > 3*time.Second {
		t.Errorf("the PATCH answered %v after shutdown began, want under 3s", late)
	}

	var left int
	if err := db.QueryRow(`SELECT COUNT(*) FROM results`).Scan(&left); err != nil {
		t.Fatal(err)
	}
	if left == 0 {
		t.Fatalf("the prune deleted all %d old rows: shutdown didn't stop it between batches", shutdownOldRows)
	}

	events := decodeLines(t, logs)
	last := events[len(events)-1]
	if last["msg"] != "server.stop" || last["result"] != "succeeded" {
		t.Errorf("the last event is %v, want a succeeded server.stop", last)
	}
	var patches int
	for _, ev := range events {
		switch ev["msg"] {
		case "settings.patch":
			patches++
			if ev["status_code"] != 200.0 || ev["result"] != "succeeded" {
				t.Errorf("settings.patch: %v, want succeeded and 200", ev)
			}
		case "results.prune":
			if ev["result"] != "canceled" {
				t.Errorf("results.prune: %v, want canceled", ev)
			}
		}
	}
	if patches != 1 {
		t.Errorf("%d settings.patch events before server.stop, want 1: %s", patches, logs)
	}
}
