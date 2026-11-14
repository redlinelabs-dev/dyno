package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/redlinelabs-dev/dyno/server/internal/result"
	"github.com/redlinelabs-dev/dyno/server/internal/settings"
	"github.com/redlinelabs-dev/dyno/server/internal/store"
)

// m3Schedule is the part of a schedule these tests look at.
type m3Schedule struct {
	ID      int64  `json:"id"`
	FarEnd  string `json:"far_end"`
	Running bool   `json:"running"`
	Queued  bool   `json:"queued"`
}

// m3Server starts a server on loopback and returns its base URL, its data
// directory, its log and its stop func (see startRun).
func m3Server(t *testing.T) (base, dir string, logs *bytes.Buffer, stop func() error) {
	t.Helper()
	dir = t.TempDir()
	addr, _, logs, stop := startRun(t, config{listen: "127.0.0.1:0", data: dir})
	return "http://" + addr.String(), dir, logs, stop
}

// m3Req makes one API call and returns its status and body.
func m3Req(t *testing.T, method, url, body string) (int, []byte) {
	t.Helper()
	req, err := http.NewRequest(method, url, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	c := http.Client{Timeout: 20 * time.Second}
	resp, err := c.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, url, err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b
}

// m3AddSchedule adds a paused light schedule against farEnd (a base URL), so
// it runs only when asked to.
func m3AddSchedule(t *testing.T, base, farEnd string) m3Schedule {
	t.Helper()
	status, body := m3Req(t, "POST", base+"/api/v1/schedules",
		`{"far_end":"`+farEnd+`","kind":"light","paused":true}`)
	if status != http.StatusCreated {
		t.Fatalf("POST /api/v1/schedules: %d %s", status, body)
	}
	var s m3Schedule
	if err := json.Unmarshal(body, &s); err != nil || s.ID <= 0 {
		t.Fatalf("schedule %s: %v", body, err)
	}
	return s
}

func m3GetSchedule(t *testing.T, base string, id int64) m3Schedule {
	t.Helper()
	url := base + "/api/v1/schedules/" + m3ID(id)
	status, body := m3Req(t, "GET", url, "")
	if status != 200 {
		t.Fatalf("GET %s: %d %s", url, status, body)
	}
	var s m3Schedule
	if err := json.Unmarshal(body, &s); err != nil {
		t.Fatalf("schedule %s: %v", body, err)
	}
	return s
}

func m3GetResults(t *testing.T, base string) []result.Result {
	t.Helper()
	status, body := m3Req(t, "GET", base+"/api/v1/results", "")
	if status != 200 {
		t.Fatalf("GET /api/v1/results: %d %s", status, body)
	}
	var out struct {
		Results []result.Result `json:"results"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("results %s: %v", body, err)
	}
	return out.Results
}

func m3ID(n int64) string {
	b, _ := json.Marshal(n)
	return string(b)
}

// m3Wait polls every 200 ms until cond is true, and fails the test at the
// deadline.
func m3Wait(t *testing.T, within time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(within)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("not within %v: %s", within, what)
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// m3Events returns the log events named msg.
func m3Events(t *testing.T, logs *bytes.Buffer, msg string) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, ev := range decodeLines(t, logs) {
		if ev["msg"] == msg {
			out = append(out, ev)
		}
	}
	return out
}

func TestI1_ServerRunsSchedules(t *testing.T) {
	if testing.Short() {
		t.Skip("runs a real light test between two servers, about 6 s")
	}
	aBase, _, _, _ := m3Server(t)
	bBase, _, _, _ := m3Server(t)

	sc := m3AddSchedule(t, aBase, bBase)
	if sc.FarEnd != bBase {
		t.Errorf("far_end = %q, want %q", sc.FarEnd, bBase)
	}
	status, body := m3Req(t, "POST", aBase+"/api/v1/schedules/"+m3ID(sc.ID)+"/run", "")
	if status != http.StatusAccepted {
		t.Fatalf("POST run: %d %s", status, body)
	}

	seenRunning := false
	var results []result.Result
	m3Wait(t, 60*time.Second, "a stored result", func() bool {
		if m3GetSchedule(t, aBase, sc.ID).Running {
			seenRunning = true
		}
		results = m3GetResults(t, aBase)
		return len(results) > 0
	})
	if len(results) != 1 {
		t.Fatalf("%d results, want exactly 1: %+v", len(results), results)
	}
	r := results[0]
	if r.Source != "schedule" || r.ScheduleID != sc.ID || r.FarEnd != bBase {
		t.Errorf("source %q schedule_id %d far_end %q, want schedule, %d, %q", r.Source, r.ScheduleID, r.FarEnd, sc.ID, bBase)
	}
	if r.DownloadMbps <= 0 || r.UploadMbps <= 0 {
		t.Errorf("download %v, upload %v Mbit/s, want both above 0", r.DownloadMbps, r.UploadMbps)
	}
	if r.Failure != nil {
		t.Errorf("failure %+v, want none", *r.Failure)
	}
	if !seenRunning {
		t.Error("the schedule never showed running true while the run went")
	}
	// The result and the status are two reads, so give the status 2 s to settle.
	var after m3Schedule
	settleBy := time.Now().Add(2 * time.Second)
	for {
		after = m3GetSchedule(t, aBase, sc.ID)
		if !after.Running && !after.Queued {
			break
		}
		if time.Now().After(settleBy) {
			t.Errorf("2 s after the result: running %v queued %v, want neither", after.Running, after.Queued)
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
}

func TestI2_FarEndCheckIsWired(t *testing.T) {
	aBase, _, _, _ := m3Server(t)
	bBase, _, _, _ := m3Server(t)

	status, body := m3Req(t, "POST", aBase+"/api/v1/schedules/check", `{"far_end":"`+bBase+`"}`)
	if status != 200 {
		t.Fatalf("check of B: %d %s", status, body)
	}
	var ok struct {
		FarEnd  string `json:"far_end"`
		Version string `json:"version"`
	}
	if err := json.Unmarshal(body, &ok); err != nil {
		t.Fatalf("check reply %s: %v", body, err)
	}
	if ok.Version != version || ok.FarEnd != bBase {
		t.Errorf("check reply %s, want version %q and far_end %q", body, version, bBase)
	}

	// An address nothing listens on: bind a port, then let it go.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	dead := ln.Addr().String()
	ln.Close()
	status, body = m3Req(t, "POST", aBase+"/api/v1/schedules/check", `{"far_end":"`+dead+`"}`)
	var bad struct {
		Code string `json:"code"`
	}
	json.Unmarshal(body, &bad)
	if status != http.StatusUnprocessableEntity || bad.Code != "unreachable" {
		t.Errorf("check of %s: %d %s, want 422 unreachable", dead, status, body)
	}
}

func TestI3_ShutdownStopsScheduledRun(t *testing.T) {
	if testing.Short() {
		t.Skip("runs a real light test between two servers, about 1-2 s: it is cut off during the pings")
	}
	aBase, aDir, aLogs, aStop := m3Server(t)
	bBase, _, _, _ := m3Server(t)

	sc := m3AddSchedule(t, aBase, bBase)
	if status, body := m3Req(t, "POST", aBase+"/api/v1/schedules/"+m3ID(sc.ID)+"/run", ""); status != http.StatusAccepted {
		t.Fatalf("POST run: %d %s", status, body)
	}
	m3Wait(t, 30*time.Second, "the schedule running", func() bool {
		return m3GetSchedule(t, aBase, sc.ID).Running
	})

	done := make(chan error, 1)
	go func() { done <- aStop() }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("run returned %v, want nil", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("run didn't return within 10 s of its context ending")
	}

	// run has returned, so its store is closed and its log is quiet.
	st, err := store.Open(aDir)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	list, err := st.List(10, 0, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 0 {
		t.Errorf("A's store holds %d results after the cancelled run, want none: %+v", len(list), list)
	}
	for _, ev := range decodeLines(t, aLogs) {
		if ev["level"] == "ERROR" {
			t.Errorf("ERROR event in A's log: %v", ev)
		}
	}
	runs := m3Events(t, aLogs, "schedule.run")
	if len(runs) != 1 || runs[0]["result"] != "canceled" {
		t.Errorf("schedule.run events %v, want one with result canceled", runs)
	}
}

func TestI4_RetentionRunsWithServer(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	keep := 1
	if _, err := st.PatchSettings(settings.Patch{KeepDays: &keep}); err != nil {
		t.Fatal(err)
	}
	mk := func(at time.Time) result.Result {
		r := result.Result{At: at, Source: "client", Submission: result.Submission{
			Client: "m3 test", DownloadMbps: 100, UploadMbps: 10, IdleMs: 5, LoadedDownMs: 10, LoadedUpMs: 10, Streams: 4}}
		r.Derive()
		return r
	}
	old, err := st.Add(mk(time.Now().Add(-3 * 24 * time.Hour).UTC()))
	if err != nil {
		t.Fatal(err)
	}
	fresh, err := st.Add(mk(time.Now().UTC()))
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}

	addr, _, logs, stop := startRun(t, config{listen: "127.0.0.1:0", data: dir})
	base := "http://" + addr.String()
	m3Wait(t, 5*time.Second, "the old result deleted", func() bool {
		for _, r := range m3GetResults(t, base) {
			if r.ID == old.ID {
				return false
			}
		}
		return true
	})
	var ids []int64
	for _, r := range m3GetResults(t, base) {
		ids = append(ids, r.ID)
	}
	if len(ids) != 1 || ids[0] != fresh.ID {
		t.Errorf("results after the prune: ids %v, want only the fresh one (%d)", ids, fresh.ID)
	}
	if err := stop(); err != nil {
		t.Fatal(err)
	}
	prunes := m3Events(t, logs, "results.prune")
	if len(prunes) != 1 {
		t.Fatalf("%d results.prune events, want 1: %s", len(prunes), logs)
	}
	if p := prunes[0]; p["deleted_results"] != float64(1) || p["keep_days"] != float64(1) || p["level"] != "INFO" {
		t.Errorf("results.prune: %v, want INFO, deleted_results 1, keep_days 1", p)
	}
}

func TestI6_ScheduledRunGivesWayToOwnTraffic(t *testing.T) {
	if testing.Short() {
		t.Skip("holds a /dl open for 8 s, then waits out the grace and a run")
	}
	aBase, _, _, _ := m3Server(t)
	bBase, _, _, _ := m3Server(t)
	sc := m3AddSchedule(t, aBase, bBase)

	// A /dl that stays open: the client reads a little at a time, so the
	// server's stall timeout never cuts it off.
	// The timeout is far past the 8 s the test holds the /dl, so it only bounds
	// a hang.
	c := http.Client{Timeout: 60 * time.Second, Transport: &http.Transport{DisableKeepAlives: true}}
	resp, err := c.Get(aBase + "/dl?bytes=4294967296")
	if err != nil {
		t.Fatal(err)
	}
	closed := false
	defer func() {
		if !closed {
			resp.Body.Close()
		}
	}()
	if resp.StatusCode != 200 {
		t.Fatalf("/dl: %d", resp.StatusCode)
	}
	buf := make([]byte, 16<<10)
	readSome := func() {
		t.Helper()
		type read struct {
			n   int
			err error
		}
		done := make(chan read, 1)
		go func() {
			n, err := resp.Body.Read(buf)
			done <- read{n, err}
		}()
		select {
		case r := <-done:
			if r.n == 0 && r.err != nil {
				t.Fatalf("the /dl ended before the test closed it: %v", r.err)
			}
		case <-time.After(10 * time.Second):
			resp.Body.Close() // ends the blocked Read
			closed = true
			t.Fatalf("the /dl gave no data for 10 s")
		}
	}
	readSome() // it is sending

	status, body := m3Req(t, "POST", aBase+"/api/v1/schedules/"+m3ID(sc.ID)+"/run", "")
	if status != http.StatusAccepted {
		t.Fatalf("POST run: %d %s", status, body)
	}
	var queued m3Schedule
	json.Unmarshal(body, &queued)
	if !queued.Queued || queued.Running {
		t.Errorf("reply to run: queued %v running %v, want queued only", queued.Queued, queued.Running)
	}

	end := time.Now().Add(8 * time.Second)
	for time.Now().Before(end) {
		readSome()
		s := m3GetSchedule(t, aBase, sc.ID)
		if s.Running || !s.Queued {
			t.Fatalf("while the /dl is open: running %v queued %v, want queued only", s.Running, s.Queued)
		}
		time.Sleep(200 * time.Millisecond)
	}

	resp.Body.Close()
	closed = true
	m3Wait(t, 15*time.Second, "the run starting once the /dl ended", func() bool {
		return m3GetSchedule(t, aBase, sc.ID).Running
	})
}
