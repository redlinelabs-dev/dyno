package api_test

import (
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/redlinelabs-dev/dyno/server/internal/api"
	"github.com/redlinelabs-dev/dyno/server/internal/nodetest"
	"github.com/redlinelabs-dev/dyno/server/internal/result"
	"github.com/redlinelabs-dev/dyno/server/internal/schedule"
	"github.com/redlinelabs-dev/dyno/server/internal/store"
	"github.com/redlinelabs-dev/dyno/server/internal/testrun"
	_ "modernc.org/sqlite"
)

// Builder's own cases for the events of the write routes (ADR-0020, amended
// 2026-11-14), beside the spec cases in events_test.go.

// wlRig is the API served straight through its mux, with no HTTP server, so a
// test can see what the log holds at the moment a handler writes its reply.
type wlRig struct {
	mux   *http.ServeMux
	st    *store.Store
	dir   string
	logs  *evLog
	sched *schFakeScheduler
	check *schFakeCheck
}

// wlNewRig is evNewEnv without the server.
func wlNewRig(t *testing.T, o evOpts) *wlRig {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	logs := &evLog{}
	sched := &schFakeScheduler{st: st, unknown: map[int64]bool{}, status: map[int64]schedule.Status{}}
	check := schNewCheck()
	a := &api.API{
		Version: "1.2.3", Store: st, Runs: testrun.NewRegistry(),
		Log: slog.New(slog.NewJSONHandler(logs, nil)),
	}
	switch {
	case o.noScheduler:
		// Schedules are kept but never run.
	case o.runErr != nil:
		a.Scheduler = &evFailingScheduler{schFakeScheduler: sched, err: o.runErr}
	default:
		a.Scheduler = sched
	}
	if !o.noCheck {
		a.Check = check.Check
	}
	mux := http.NewServeMux()
	a.Register(mux)
	return &wlRig{mux: mux, st: st, dir: dir, logs: logs, sched: sched, check: check}
}

// wlGate is a response writer that notes what the log holds as the first byte
// of the reply is written.
type wlGate struct {
	*httptest.ResponseRecorder
	t     *testing.T
	logs  *evLog
	seen  []map[string]any
	noted bool
}

func (g *wlGate) note() {
	if !g.noted {
		g.noted = true
		g.seen = g.logs.events(g.t)
	}
}

func (g *wlGate) WriteHeader(code int) {
	g.note()
	g.ResponseRecorder.WriteHeader(code)
}

func (g *wlGate) Write(p []byte) (int, error) {
	g.note()
	return g.ResponseRecorder.Write(p)
}

// wantLoggedFirst sends a request through the mux and wants its status, and
// the events called names, in any order, to be in the log by the time the
// first byte of the reply is written, with nothing logged after.
func (g *wlRig) wantLoggedFirst(t *testing.T, label, method, path, body string, status int, names ...string) {
	t.Helper()
	before := len(g.logs.events(t))
	gate := &wlGate{ResponseRecorder: httptest.NewRecorder(), t: t, logs: g.logs}
	g.mux.ServeHTTP(gate, httptest.NewRequest(method, path, strings.NewReader(body)))
	if gate.Code != status {
		t.Errorf("%s: status %d, want %d: %s", label, gate.Code, status, gate.Body)
		return
	}
	if !gate.noted {
		t.Errorf("%s: the handler wrote no reply", label)
		return
	}
	var got []string
	for _, ev := range gate.seen[before:] {
		got = append(got, fmt.Sprint(ev["msg"]))
	}
	slices.Sort(got)
	if want := slices.Sorted(slices.Values(names)); !slices.Equal(got, want) {
		t.Errorf("%s: %v were logged before the reply, want %v", label, got, want)
	}
	if after := g.logs.events(t); len(after) != len(gate.seen) {
		t.Errorf("%s: %d events were logged once the reply had begun", label, len(after)-len(gate.seen))
	}
}

// wlStoreResult stores a client result made age ago.
func wlStoreResult(t *testing.T, st *store.Store, age time.Duration) {
	t.Helper()
	r := result.Result{At: time.Now().UTC().Add(-age), Source: "client"}
	r.Client = "Chrome on Linux"
	r.DownloadMbps = 100
	if _, err := st.Add(r); err != nil {
		t.Fatal(err)
	}
}

// wlHideResults renames the results table in the store's file in dir, so that
// every read of a result fails while the schedules still read and write.
func wlHideResults(t *testing.T, dir string) {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(dir, "dyno.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if _, err := db.Exec("PRAGMA busy_timeout = 5000"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("ALTER TABLE results RENAME TO wl_results_hidden"); err != nil {
		t.Fatal(err)
	}
}

// wlSays sends a request and wants its status, the one sentence of its error
// body, and its Retry-After header, which is "" for none.
func wlSays(t *testing.T, env *evEnv, label, method, path, body string, status int, say, retryAfter string) {
	t.Helper()
	r := env.do(method, path, body)
	if r.Status != status {
		t.Errorf("%s: status %d, want %d: %s", label, r.Status, status, r.Body)
		return
	}
	if got := r.errorText(t); got != say {
		t.Errorf("%s: error %q, want %q", label, got, say)
	}
	if got := r.Header.Get("Retry-After"); got != retryAfter {
		t.Errorf("%s: Retry-After %q, want %q", label, got, retryAfter)
	}
}

// An id of 0 or below is no schedule: PATCH, DELETE and /run answer 404
// not_found for it, as for an id never added, and its event has no
// schedule_id. 400 invalid_id is for an id that isn't an integer, which
// includes one too big for an int64.
func TestCx_IDsOfZeroAndBelowAreNotFoundAndLogNoScheduleID(t *testing.T) {
	defer schNoPanic(t)
	env := evNewEnv(t, evOpts{})
	for _, c := range []struct{ msg, method, suffix, body string }{
		{"schedule.update", "PATCH", "", `{"paused":true}`},
		{"schedule.delete", "DELETE", "", ""},
		{"schedule.run_now", "POST", "/run", ""},
	} {
		for _, id := range []string{"0", "-1", "-9223372036854775808"} {
			label := c.method + " schedule " + id + c.suffix
			r, lines := env.call(t, c.method, "/api/v1/schedules/"+id+c.suffix, c.body)
			if r.Status != 404 {
				t.Errorf("%s: status %d, want 404: %s", label, r.Status, r.Body)
				continue
			}
			if got := r.errorText(t); got != "no schedule with that id" {
				t.Errorf("%s: error %q, want the sentence for an id never added", label, got)
			}
			if len(lines) != 1 {
				t.Errorf("%s: the request logged %d lines, want 1: %v", label, len(lines), lines)
			}
			evCheck(t, label, lines, evWant{msg: c.msg, status: 404, code: "not_found"})
		}
		label := c.method + " schedule 99999999999999999999" + c.suffix
		r, lines := env.call(t, c.method, "/api/v1/schedules/99999999999999999999"+c.suffix, c.body)
		if r.Status != 400 {
			t.Errorf("%s: status %d, want 400: %s", label, r.Status, r.Body)
			continue
		}
		evCheck(t, label, lines, evWant{msg: c.msg, status: 400, code: "invalid_id"})
	}
}

// Every handler logs its event before it writes the reply, on every kind of
// way out, so a client that has its answer can already read the event. A PATCH
// of keep_days has two events to log, its own and the prune's, and both come
// first.
func TestCx_EveryWriteLogsItsEventBeforeItWritesTheReply(t *testing.T) {
	defer schNoPanic(t)
	g := wlNewRig(t, evOpts{})
	wlStoreResult(t, g.st, 40*24*time.Hour) // old enough for the last PATCH of settings to delete

	g.wantLoggedFirst(t, "add", "POST", "/api/v1/schedules", `{"far_end":"tailmeadow"}`, 201, "schedule.add")
	g.wantLoggedFirst(t, "add, not JSON", "POST", "/api/v1/schedules", `{"far_end":`, 400, "schedule.add")
	g.wantLoggedFirst(t, "add, a bad far end", "POST", "/api/v1/schedules", `{"far_end":"ftp://x"}`, 400, "schedule.add")
	g.wantLoggedFirst(t, "update", "PATCH", "/api/v1/schedules/1", `{"paused":true}`, 200, "schedule.update")
	g.wantLoggedFirst(t, "update, a patch that leaves it invalid", "PATCH", "/api/v1/schedules/1", `{"every_minutes":45}`, 400, "schedule.update")
	g.wantLoggedFirst(t, "update, an id never added", "PATCH", "/api/v1/schedules/999", `{"paused":true}`, 404, "schedule.update")
	g.wantLoggedFirst(t, "update, an id that isn't a number", "PATCH", "/api/v1/schedules/abc", `{}`, 400, "schedule.update")
	g.wantLoggedFirst(t, "run", "POST", "/api/v1/schedules/1/run", "", 202, "schedule.run_now")
	g.wantLoggedFirst(t, "run, an id never added", "POST", "/api/v1/schedules/999/run", "", 404, "schedule.run_now")
	g.wantLoggedFirst(t, "delete, an id never added", "DELETE", "/api/v1/schedules/999", "", 404, "schedule.delete")
	g.wantLoggedFirst(t, "delete", "DELETE", "/api/v1/schedules/1", "", 204, "schedule.delete")
	g.wantLoggedFirst(t, "a read that goes as planned", "GET", "/api/v1/schedules", "", 200)
	g.wantLoggedFirst(t, "settings", "PATCH", "/api/v1/settings", `{"plan_down_mbps":500}`, 200, "settings.patch")
	g.wantLoggedFirst(t, "settings, out of range", "PATCH", "/api/v1/settings", `{"keep_days":-1}`, 400, "settings.patch")
	g.wantLoggedFirst(t, "settings that delete", "PATCH", "/api/v1/settings", `{"keep_days":7}`, 200, "results.prune", "settings.patch")

	// A far end that is down, and a check that fails here.
	g.check.setErr(&nodetest.Error{Code: nodetest.CodeUnreachable, Err: errors.New("no route")})
	g.wantLoggedFirst(t, "add, the far end is down", "POST", "/api/v1/schedules", `{"far_end":"tailmeadow"}`, 422, "schedule.add")
	g.check.setErr(errors.New("boom"))
	g.wantLoggedFirst(t, "add, the check fails here", "POST", "/api/v1/schedules", `{"far_end":"tailmeadow"}`, 500, "schedule.add")
	g.check.setErr(nil)

	full := wlNewRig(t, evOpts{})
	for i := range schedule.MaxSchedules {
		schSeed(t, full.st, fmt.Sprintf("host%d", i))
	}
	full.wantLoggedFirst(t, "add, 16 exist", "POST", "/api/v1/schedules", `{"far_end":"tailmeadow"}`, 409, "schedule.add")

	soon := wlNewRig(t, evOpts{})
	recent := schSeed(t, soon.st, "tailmeadow")
	schStoreRun(t, soon.st, recent.ID, recent.FarEnd, time.Now().Add(-20*time.Second), nil)
	soon.wantLoggedFirst(t, "run, 20 seconds after a result", "POST", evPath(recent.ID, "/run"), "", 429, "schedule.run_now")

	noCheck := wlNewRig(t, evOpts{noCheck: true})
	noCheck.wantLoggedFirst(t, "add, no Check", "POST", "/api/v1/schedules", `{"far_end":"tailmeadow"}`, 503, "schedule.add")

	noSched := wlNewRig(t, evOpts{noScheduler: true})
	idle := schSeed(t, noSched.st, "tailmeadow")
	noSched.wantLoggedFirst(t, "run, no Scheduler", "POST", evPath(idle.ID, "/run"), "", 503, "schedule.run_now")

	broken := wlNewRig(t, evOpts{runErr: errors.New("the scheduler broke")})
	stuck := schSeed(t, broken.st, "tailmeadow")
	broken.wantLoggedFirst(t, "run, RunNow fails", "POST", evPath(stuck.ID, "/run"), "", 500, "schedule.run_now")

	closed := wlNewRig(t, evOpts{})
	gone := schSeed(t, closed.st, "tailmeadow")
	closed.st.Close()
	closed.wantLoggedFirst(t, "add, the store is closed", "POST", "/api/v1/schedules", `{"far_end":"tailmeadow"}`, 500, "schedule.add")
	closed.wantLoggedFirst(t, "update, the store is closed", "PATCH", evPath(gone.ID, ""), `{"paused":true}`, 500, "schedule.update")
	closed.wantLoggedFirst(t, "delete, the store is closed", "DELETE", evPath(gone.ID, ""), "", 500, "schedule.delete")
	closed.wantLoggedFirst(t, "run, the store is closed", "POST", evPath(gone.ID, "/run"), "", 500, "schedule.run_now")
	closed.wantLoggedFirst(t, "settings, the store is closed", "PATCH", "/api/v1/settings", `{"keep_days":5}`, 500, "settings.patch")
	closed.wantLoggedFirst(t, "a read the store fails", "GET", "/api/v1/settings", "", 500, "settings.get")
}

// A failure's reply is the plain sentence it was before events were logged,
// with the headers it had: the log gets the error's text and the client never
// does.
func TestCx_FailureRepliesStayPlainSentences(t *testing.T) {
	defer schNoPanic(t)
	const missing = "no schedule with that id"

	env := evNewEnv(t, evOpts{})
	sc := env.add(t, `{"far_end":"tailmeadow"}`)
	wlSays(t, env, "an id that isn't a number", "PATCH", "/api/v1/schedules/abc", `{"paused":true}`, 400, "id must be an integer", "")
	wlSays(t, env, "update, an id never added", "PATCH", evPath(999, ""), `{"paused":true}`, 404, missing, "")
	wlSays(t, env, "delete, an id never added", "DELETE", evPath(999, ""), "", 404, missing, "")
	wlSays(t, env, "run, an id never added", "POST", evPath(999, "/run"), "", 404, missing, "")

	// A far end that fails its check says so in two keys.
	cerr := &nodetest.Error{Code: nodetest.CodeNotDyno, Err: errors.New("a web server answered")}
	env.check.setErr(cerr)
	r := env.do("POST", "/api/v1/schedules", `{"far_end":"elsewhere"}`)
	r.wantStatus(t, 422)
	if got := r.errorText(t); got != schErrText(t, cerr) {
		t.Errorf("a far end that isn't a dyno: error %q, want the check's sentence %q", got, schErrText(t, cerr))
	}
	if code := string(r.asObject(t)["code"]); code != `"not_dyno"` {
		t.Errorf("a far end that isn't a dyno: code %s, want not_dyno", code)
	}
	env.check.setErr(errors.New("boom"))
	wlSays(t, env, "a check that failed here", "POST", "/api/v1/schedules", `{"far_end":"elsewhere"}`, 500, "could not check the far end", "")
	env.check.setErr(nil)

	// A run that is too soon says how long to wait, in the sentence and in Retry-After.
	schStoreRun(t, env.st, sc.ID, sc.FarEnd, time.Now().Add(-20*time.Second), nil)
	r = env.do("POST", evPath(sc.ID, "/run"), "")
	r.wantStatus(t, 429)
	if got := r.errorText(t); !strings.HasPrefix(got, "this schedule's last result is less than a minute old; try again in ") || !strings.HasSuffix(got, " s") {
		t.Errorf("a run too soon: error %q", got)
	}
	if r.Header.Get("Retry-After") == "" {
		t.Error("a run too soon: no Retry-After")
	}

	full := evNewEnv(t, evOpts{})
	for i := range schedule.MaxSchedules {
		schSeed(t, full.st, fmt.Sprintf("host%d", i))
	}
	wlSays(t, full, "the 17th schedule", "POST", "/api/v1/schedules", `{"far_end":"tailmeadow"}`, 409, "16 schedules exist already; delete one to add another", "")

	off := evNewEnv(t, evOpts{noCheck: true})
	wlSays(t, off, "no Check function", "POST", "/api/v1/schedules", `{"far_end":"tailmeadow"}`, 503, "this server can't check far ends", "")
	still := evNewEnv(t, evOpts{noScheduler: true})
	parked := still.add(t, `{"far_end":"tailmeadow"}`)
	wlSays(t, still, "no Scheduler", "POST", evPath(parked.ID, "/run"), "", 503, "scheduled tests aren't running on this server", "")
	busy := evNewEnv(t, evOpts{})
	release := evHoldFourChecks(t, busy)
	defer release()
	wlSays(t, busy, "four checks running", "POST", "/api/v1/schedules", `{"far_end":"tailmeadow"}`, 503, "4 far-end checks are running already; try again in a second", "1")

	// What the server can't do itself, in plain words and never the error's.
	closed := evNewEnv(t, evOpts{})
	one := closed.add(t, `{"far_end":"tailmeadow"}`)
	closed.st.Close()
	wlSays(t, closed, "add", "POST", "/api/v1/schedules", `{"far_end":"tailmeadow"}`, 500, "could not read the schedules", "")
	wlSays(t, closed, "update", "PATCH", evPath(one.ID, ""), `{"paused":true}`, 500, "could not read the schedules", "")
	wlSays(t, closed, "delete", "DELETE", evPath(one.ID, ""), "", 500, "could not save the schedule", "")
	wlSays(t, closed, "run", "POST", evPath(one.ID, "/run"), "", 500, "could not read the schedules", "")
	wlSays(t, closed, "the list", "GET", "/api/v1/schedules", "", 500, "could not read the schedules", "")
	wlSays(t, closed, "one schedule", "GET", evPath(one.ID, ""), "", 500, "could not read the schedules", "")

	refused := evNewEnv(t, evOpts{})
	evRefuse(t, refused.dir, "schedules", "INSERT")
	wlSays(t, refused, "the insert is refused", "POST", "/api/v1/schedules", `{"far_end":"tailmeadow"}`, 500, "could not save the schedule", "")

	broken := evNewEnv(t, evOpts{runErr: errors.New("the scheduler broke")})
	stuck := broken.add(t, `{"far_end":"tailmeadow"}`)
	wlSays(t, broken, "RunNow fails", "POST", evPath(stuck.ID, "/run"), "", 500, "could not queue the run", "")
}

func TestHx_SettingsFailureRepliesStayPlainSentences(t *testing.T) {
	defer schNoPanic(t)
	closed := evNewEnv(t, evOpts{})
	closed.st.Close()
	wlSays(t, closed, "a PATCH the store can't save", "PATCH", "/api/v1/settings", `{"keep_days":5}`, 500, "could not save the settings", "")
	wlSays(t, closed, "a GET the store can't read", "GET", "/api/v1/settings", "", 500, "could not read the settings", "")
}

// No event says who asked (ADR-0020): not the client's address, its user agent,
// or a header that names it.
func TestCx_EventsCarryNothingThatNamesTheClient(t *testing.T) {
	defer schNoPanic(t)
	g := wlNewRig(t, evOpts{})
	secrets := []string{"203.0.113.77", "wl-agent", "198.51.100.9", "198.51.100.10", "wl-referrer"}
	send := func(method, path, body string) {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.RemoteAddr = "203.0.113.77:4242"
		req.Header.Set("User-Agent", "wl-agent/9.9")
		req.Header.Set("X-Forwarded-For", "198.51.100.9")
		req.Header.Set("X-Real-Ip", "198.51.100.10")
		req.Header.Set("Referer", "http://wl-referrer.example/")
		g.mux.ServeHTTP(httptest.NewRecorder(), req)
	}
	send("POST", "/api/v1/schedules", `{"far_end":"tailmeadow"}`)
	send("POST", "/api/v1/schedules", `{"far_end":`)
	send("PATCH", "/api/v1/schedules/1", `{"paused":true}`)
	send("PATCH", "/api/v1/schedules/999", `{"paused":true}`)
	send("POST", "/api/v1/schedules/1/run", "")
	send("DELETE", "/api/v1/schedules/1", "")
	send("PATCH", "/api/v1/settings", `{"keep_days":3}`)
	send("PATCH", "/api/v1/settings", `{"keep_days":-3}`)

	lines := g.logs.events(t)
	if len(lines) != 8 {
		t.Fatalf("eight writes logged %d events, want 8: %v", len(lines), lines)
	}
	for _, ev := range lines {
		for k, v := range ev {
			switch k {
			case "client", "device", "user_agent", "remote_addr", "client_ip", "ip", "host":
				t.Errorf("%v carries %s = %v, which names the client", ev["msg"], k, v)
			}
			text := fmt.Sprint(v)
			for _, secret := range secrets {
				if strings.Contains(text, secret) {
					t.Errorf("%v: %s = %q holds %q, which names the client", ev["msg"], k, text, secret)
				}
			}
		}
	}
}

// A PATCH that is saved, and whose reply then can't read the schedule's
// results, is one 500 event, and it still says what was saved.
func TestCx_AnUpdateSavedButNotAnsweredLogsOneErrorEventWithTheSavedValues(t *testing.T) {
	defer schNoPanic(t)
	env := evNewEnv(t, evOpts{})
	sc := env.add(t, `{"far_end":"tailmeadow","every_minutes":60}`)
	wlHideResults(t, env.dir)

	r, lines := env.call(t, "PATCH", evPath(sc.ID, ""), `{"every_minutes":120,"offset_minutes":7,"paused":true}`)
	r.wantStatus(t, 500)
	if got := r.errorText(t); got != "could not read the schedules" {
		t.Errorf("error %q, want the plain sentence", got)
	}
	ev := evCheck(t, "the update", lines, evWant{msg: "schedule.update", status: 500, code: "store_read_failed", id: sc.ID})
	for k, want := range map[string]any{
		"far_end": sc.FarEnd, "kind": "light", "every_minutes": 120.0, "offset_minutes": 7.0, "paused": true,
	} {
		if ev[k] != want {
			t.Errorf("%s = %v, want %v: the change was saved", k, ev[k], want)
		}
	}
	saved, err := env.st.Schedule(sc.ID)
	if err != nil || saved.EveryMinutes != 120 || saved.OffsetMinutes != 7 || !saved.Paused {
		t.Errorf("the store holds %+v (err %v), want the update saved", saved, err)
	}
	if got := env.sched.changes(); got != 2 {
		t.Errorf("the scheduler was told %d times, want 2: the add and the saved update", got)
	}
}

// A PATCH that sends nothing changes nothing, and its event still says what
// the schedule is.
func TestCx_AnUpdateThatSendsNothingLogsTheScheduleAsItIs(t *testing.T) {
	defer schNoPanic(t)
	env := evNewEnv(t, evOpts{})
	sc := env.add(t, `{"far_end":"tailmeadow","name":"Gran's","every_minutes":60,"offset_minutes":5,"paused":true}`)
	r, lines := env.call(t, "PATCH", evPath(sc.ID, ""), `{}`)
	r.wantStatus(t, 200)
	ev := evCheck(t, "an empty patch", lines, evWant{msg: "schedule.update", status: 200, id: sc.ID})
	evCheckSaved(t, "an empty patch", ev, r.asSchedule(t))
	evNoText(t, "an empty patch", ev, "Gran's")
}
