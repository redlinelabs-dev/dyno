package api_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"log/slog"
	"maps"
	"net/http"
	"net/http/httptest"
	"runtime/debug"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/redlinelabs-dev/dyno/server/internal/api"
	"github.com/redlinelabs-dev/dyno/server/internal/metrics"
	"github.com/redlinelabs-dev/dyno/server/internal/nodetest"
	"github.com/redlinelabs-dev/dyno/server/internal/result"
	"github.com/redlinelabs-dev/dyno/server/internal/schedule"
	"github.com/redlinelabs-dev/dyno/server/internal/store"
	"github.com/redlinelabs-dev/dyno/server/internal/testrun"
)

// schNoPanic turns a panic in a test, such as a stub that isn't built yet, into
// a failure of that test alone instead of an abort of the whole package.
func schNoPanic(t *testing.T) {
	if r := recover(); r != nil {
		t.Fatalf("panic: %v\n%s", r, debug.Stack())
	}
}

// schFakeScheduler is an api.Scheduler that records what it is asked.
type schFakeScheduler struct {
	st *store.Store // read at each Changed call, to see what was stored by then

	mu         sync.Mutex
	snapshots  [][]schedule.Schedule // the store's schedules at each Changed call
	runs       []int64               // the ids RunNow was called with
	unknown    map[int64]bool        // ids RunNow answers store.ErrNotFound for
	status     map[int64]schedule.Status
	queueOnRun bool // RunNow marks the schedule queued, as the real scheduler does
}

func (f *schFakeScheduler) Changed() {
	var snap []schedule.Schedule
	if f.st != nil {
		snap, _ = f.st.Schedules()
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.snapshots = append(f.snapshots, snap)
}

func (f *schFakeScheduler) RunNow(id int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.runs = append(f.runs, id)
	if f.unknown[id] {
		return store.ErrNotFound
	}
	if f.queueOnRun {
		f.status[id] = schedule.Status{Queued: true}
	}
	return nil
}

func (f *schFakeScheduler) Status(id int64) schedule.Status {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.status[id]
}

func (f *schFakeScheduler) setStatus(id int64, s schedule.Status) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.status[id] = s
}

func (f *schFakeScheduler) markUnknown(id int64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.unknown[id] = true
}

func (f *schFakeScheduler) setQueueOnRun(v bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.queueOnRun = v
}

// changes is how many times Changed was called.
func (f *schFakeScheduler) changes() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.snapshots)
}

// lastSnapshot is what the store held at the latest Changed call.
func (f *schFakeScheduler) lastSnapshot() []schedule.Schedule {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.snapshots) == 0 {
		return nil
	}
	return f.snapshots[len(f.snapshots)-1]
}

// ran is the ids RunNow was called with.
func (f *schFakeScheduler) ran() []int64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.runs)
}

// schFakeCheck is an API.Check that records each call and answers with what
// the test set.
type schFakeCheck struct {
	mu        sync.Mutex
	farEnds   []string        // every far end it was called with
	remaining []time.Duration // time left to each call's ctx deadline; -1 when it had none
	info      nodetest.FarEndInfo
	err       error
	gate      chan struct{} // once set, calls wait for it to close
	once      sync.Once
	entered   chan struct{}       // gets a value as each call starts
	hook      func(farEnd string) // runs inside each call, before it answers
}

func schNewCheck() *schFakeCheck {
	return &schFakeCheck{
		info:    nodetest.FarEndInfo{Name: "dyno", Version: "0.1.0"},
		entered: make(chan struct{}, 64),
	}
}

func (f *schFakeCheck) Check(ctx context.Context, farEnd string) (nodetest.FarEndInfo, error) {
	left := time.Duration(-1)
	if dl, ok := ctx.Deadline(); ok {
		left = time.Until(dl)
	}
	f.mu.Lock()
	f.farEnds = append(f.farEnds, farEnd)
	f.remaining = append(f.remaining, left)
	gate, info, err, hook := f.gate, f.info, f.err, f.hook
	f.mu.Unlock()
	select {
	case f.entered <- struct{}{}:
	default:
	}
	if hook != nil {
		hook(farEnd)
	}
	if gate != nil {
		select {
		case <-gate:
		case <-ctx.Done():
			return nodetest.FarEndInfo{}, ctx.Err()
		}
	}
	return info, err
}

// hold makes every call wait until release.
func (f *schFakeCheck) hold() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.gate = make(chan struct{})
}

// release lets the calls held by hold go, and every later call through.
func (f *schFakeCheck) release() {
	f.mu.Lock()
	gate := f.gate
	f.mu.Unlock()
	if gate != nil {
		f.once.Do(func() { close(gate) })
	}
}

func (f *schFakeCheck) setErr(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.err = err
}

func (f *schFakeCheck) setInfo(info nodetest.FarEndInfo) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.info = info
}

func (f *schFakeCheck) setHook(h func(farEnd string)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.hook = h
}

// calls is the far ends Check was called with, in order.
func (f *schFakeCheck) calls() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.farEnds)
}

// deadlines is the time that was left to each call's ctx deadline.
func (f *schFakeCheck) deadlines() []time.Duration {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.remaining)
}

// waitEntered waits until n more calls have started.
func (f *schFakeCheck) waitEntered(t *testing.T, n int) {
	t.Helper()
	for range n {
		select {
		case <-f.entered:
		case <-time.After(5 * time.Second):
			t.Fatalf("Check was not called %d more times in time", n)
		}
	}
}

// schResp is an HTTP reply, read to its end.
type schResp struct {
	Status int
	Header http.Header
	Body   []byte
}

func (r schResp) wantStatus(t *testing.T, status int) {
	t.Helper()
	if r.Status != status {
		t.Fatalf("status %d, want %d: %s", r.Status, status, r.Body)
	}
}

// schJSON is a schedule as the API returns it.
type schJSON struct {
	ID            int64          `json:"id"`
	Name          string         `json:"name"`
	FarEnd        string         `json:"far_end"`
	Kind          string         `json:"kind"`
	EveryMinutes  int            `json:"every_minutes"`
	OffsetMinutes int            `json:"offset_minutes"`
	Paused        bool           `json:"paused"`
	CreatedAt     time.Time      `json:"created_at"`
	UpdatedAt     time.Time      `json:"updated_at"`
	NextRun       *time.Time     `json:"next_run"`
	Running       bool           `json:"running"`
	Queued        bool           `json:"queued"`
	LastResult    *result.Result `json:"last_result"`
	LastSuccessAt *time.Time     `json:"last_success_at"`
}

// schKeys are the keys of a schedule's JSON, from docs/api.md.
var schKeys = []string{"id", "name", "far_end", "kind", "every_minutes", "offset_minutes", "paused",
	"created_at", "updated_at", "next_run", "running", "queued", "last_result", "last_success_at"}

func (r schResp) asSchedule(t *testing.T) schJSON {
	t.Helper()
	var s schJSON
	if err := json.Unmarshal(r.Body, &s); err != nil {
		t.Fatalf("not a schedule (%v): %s", err, r.Body)
	}
	return s
}

// asObject is the reply's top-level JSON object, each value left as it came.
func (r schResp) asObject(t *testing.T) map[string]json.RawMessage {
	t.Helper()
	var o map[string]json.RawMessage
	if err := json.Unmarshal(r.Body, &o); err != nil {
		t.Fatalf("not a JSON object (%v): %s", err, r.Body)
	}
	return o
}

// asList is the schedules of a {"schedules": [...]} reply, as typed values
// and as raw objects.
func (r schResp) asList(t *testing.T) ([]schJSON, []map[string]json.RawMessage) {
	t.Helper()
	var typed struct {
		Schedules []schJSON `json:"schedules"`
	}
	var raw struct {
		Schedules []map[string]json.RawMessage `json:"schedules"`
	}
	if err := json.Unmarshal(r.Body, &typed); err != nil {
		t.Fatalf("not a list of schedules (%v): %s", err, r.Body)
	}
	if err := json.Unmarshal(r.Body, &raw); err != nil {
		t.Fatalf("not a list of schedules (%v): %s", err, r.Body)
	}
	return typed.Schedules, raw.Schedules
}

// errorText is the reply's {"error": "..."} message.
func (r schResp) errorText(t *testing.T) string {
	t.Helper()
	var e struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(r.Body, &e); err != nil || e.Error == "" {
		t.Fatalf(`body is not {"error": "..."}: %s`, r.Body)
	}
	return e.Error
}

// schClient talks to a server over HTTP.
type schClient struct {
	t    *testing.T
	base string
	hc   *http.Client
}

// schServe serves a's routes on a real listener. A handler that panics
// fails its test; its stack is not printed.
func schServe(t *testing.T, a *api.API) *schClient {
	t.Helper()
	mux := http.NewServeMux()
	a.Register(mux)
	srv := httptest.NewUnstartedServer(mux)
	srv.Config.ErrorLog = log.New(io.Discard, "", 0)
	srv.Start()
	t.Cleanup(srv.Close)
	hc := &http.Client{Timeout: 15 * time.Second, Transport: &http.Transport{}}
	t.Cleanup(hc.CloseIdleConnections)
	return &schClient{t: t, base: srv.URL, hc: hc}
}

// try sends a request and reads the reply. It is safe to call from any
// goroutine.
func (c *schClient) try(method, path, body string) (schResp, error) {
	req, err := http.NewRequest(method, c.base+path, strings.NewReader(body))
	if err != nil {
		return schResp{}, err
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return schResp{}, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	return schResp{Status: resp.StatusCode, Header: resp.Header, Body: b}, err
}

// do is try, failing the test on a transport error. Call it from the test's
// own goroutine only.
func (c *schClient) do(method, path, body string) schResp {
	c.t.Helper()
	r, err := c.try(method, path, body)
	if err != nil {
		c.t.Fatalf("%s %s: %v", method, path, err)
	}
	return r
}

// schEnv is an API over a fresh store, with a fake scheduler and a fake Check.
type schEnv struct {
	*schClient
	st    *store.Store
	sched *schFakeScheduler
	check *schFakeCheck
}

func schNewEnv(t *testing.T) *schEnv {
	t.Helper()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	sched := &schFakeScheduler{st: st, unknown: map[int64]bool{}, status: map[int64]schedule.Status{}}
	check := schNewCheck()
	a := &api.API{
		Version: "1.2.3", Store: st, Runs: testrun.NewRegistry(), Log: slog.New(slog.DiscardHandler),
		Scheduler: sched, Check: check.Check,
	}
	cl := schServe(t, a)
	t.Cleanup(check.release) // runs before the server closes, so a parked check can't hold it up
	return &schEnv{schClient: cl, st: st, sched: sched, check: check}
}

// add posts a schedule and returns the 201 reply's schedule.
func (e *schEnv) add(t *testing.T, body string) schJSON {
	t.Helper()
	r := e.do("POST", "/api/v1/schedules", body)
	r.wantStatus(t, 201)
	return r.asSchedule(t)
}

// expect fails the test unless the store holds n schedules, Check has been
// called checks times and Changed changes times.
func (e *schEnv) expect(t *testing.T, n, checks, changes int) {
	t.Helper()
	list, err := e.st.Schedules()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != n {
		t.Errorf("the store holds %d schedules, want %d", len(list), n)
	}
	if got := len(e.check.calls()); got != checks {
		t.Errorf("Check was called %d times, want %d", got, checks)
	}
	if got := e.sched.changes(); got != changes {
		t.Errorf("Changed was called %d times, want %d", got, changes)
	}
}

// schSeed stores a schedule directly, as if it had been added earlier.
func schSeed(t *testing.T, st *store.Store, host string) schedule.Schedule {
	t.Helper()
	sc, err := st.AddSchedule(schedule.Schedule{
		Name: host, FarEnd: "http://" + host + ":3966", Kind: schedule.Light, EveryMinutes: 180,
	})
	if err != nil {
		t.Fatal(err)
	}
	return sc
}

// schStoreRun stores a scheduled test's result for schedule id, stored at
// the given time: a success, or a failure when fail is set.
func schStoreRun(t *testing.T, st *store.Store, id int64, farEnd string, at time.Time, fail *result.Failure) result.Result {
	t.Helper()
	r := result.Result{At: at.UTC().Truncate(time.Millisecond), Source: "schedule", ScheduleID: id, FarEnd: farEnd, Failure: fail}
	r.Client = "dyno/1.2.3"
	if fail == nil {
		r.DownloadMbps, r.UploadMbps, r.IdleMs, r.LoadedDownMs, r.LoadedUpMs = 940, 95, 8.4, 21, 39.5
		r.JitterMs, r.Streams = 0.8, 5
		r.Detail = []byte(`{"idle":[[0.05,8.1]]}`)
		r.Derive()
	}
	got, err := st.Add(r)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

// schNextGrid is the first time after t that is midnight UTC plus offset
// minutes plus a whole number of every minutes: the timetable of ADR-0029,
// worked out here by stepping, not by the code under test.
func schNextGrid(t time.Time, every, offset int) time.Time {
	t = t.UTC()
	c := time.Date(t.Year(), t.Month(), t.Day(), 0, offset, 0, 0, time.UTC)
	for !c.After(t) {
		c = c.Add(time.Duration(every) * time.Minute)
	}
	return c
}

// schInWindow fails the test unless got, a time truncated to the
// millisecond, is between before and after.
func schInWindow(t *testing.T, what string, got, before, after time.Time) {
	t.Helper()
	if got.Before(before.Truncate(time.Millisecond)) || got.After(after) {
		t.Errorf("%s = %s, want between %s and %s", what, got.Format(time.RFC3339Nano),
			before.Format(time.RFC3339Nano), after.Format(time.RFC3339Nano))
	}
}

// schErrText is err.Error(). It fails the test, rather than crash it, if
// Error panics (nodetest.Error's does until the engine lands).
func schErrText(t *testing.T, err error) string {
	t.Helper()
	var text string
	func() {
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("%T.Error() panicked: %v", err, r)
			}
		}()
		text = err.Error()
	}()
	return text
}

func TestC18_ListOfNoSchedulesIsAnEmptyArray(t *testing.T) {
	defer schNoPanic(t)
	env := schNewEnv(t)
	r := env.do("GET", "/api/v1/schedules", "")
	r.wantStatus(t, 200)
	obj := r.asObject(t)
	if len(obj) != 1 || strings.TrimSpace(string(obj["schedules"])) != "[]" {
		t.Fatalf(`body = %s, want {"schedules": []}`, r.Body)
	}
}

func TestC19_PostMinimalFillsDefaults(t *testing.T) {
	defer schNoPanic(t)
	env := schNewEnv(t)
	before := time.Now()
	r := env.do("POST", "/api/v1/schedules", `{"far_end": "tailmeadow"}`)
	after := time.Now()
	r.wantStatus(t, 201)
	got := r.asSchedule(t)

	if got.ID <= 0 {
		t.Fatalf("id = %d", got.ID)
	}
	if loc := r.Header.Get("Location"); loc != "/api/v1/schedules/"+strconv.FormatInt(got.ID, 10) {
		t.Errorf("Location = %q, want /api/v1/schedules/%d", loc, got.ID)
	}
	if got.Name != "tailmeadow" || got.FarEnd != "http://tailmeadow:3966" || got.Kind != "light" ||
		got.EveryMinutes != 180 || got.OffsetMinutes != 0 || got.Paused {
		t.Errorf("defaults not filled in: %s", r.Body)
	}
	// next_run is the first grid time after the request.
	first, last := schNextGrid(before, 180, 0), schNextGrid(after, 180, 0)
	if got.NextRun == nil || !(got.NextRun.Equal(first) || got.NextRun.Equal(last)) {
		t.Errorf("next_run = %v, want %s", got.NextRun, first.Format(time.RFC3339))
	}
	if got.Running || got.Queued {
		t.Errorf("a new schedule is running or queued: %s", r.Body)
	}
	obj := r.asObject(t)
	if string(obj["last_result"]) != "null" || string(obj["last_success_at"]) != "null" {
		t.Errorf("last_result and last_success_at should be null: %s", r.Body)
	}
	if !got.CreatedAt.Equal(got.UpdatedAt) {
		t.Errorf("created_at %s and updated_at %s differ", got.CreatedAt, got.UpdatedAt)
	}
	schInWindow(t, "created_at", got.CreatedAt, before, after)
	for _, key := range []string{"created_at", "updated_at", "next_run"} {
		if !strings.HasSuffix(string(obj[key]), `Z"`) {
			t.Errorf("%s = %s, want a UTC time", key, obj[key])
		}
	}

	if calls := env.check.calls(); !slices.Equal(calls, []string{"http://tailmeadow:3966"}) {
		t.Errorf("Check was called with %v, want once with http://tailmeadow:3966", calls)
	}
	if n := env.sched.changes(); n != 1 {
		t.Fatalf("Changed was called %d times, want once", n)
	}
	if snap := env.sched.lastSnapshot(); len(snap) != 1 || snap[0].ID != got.ID {
		t.Errorf("at the Changed call the store held %+v, want the new schedule", snap)
	}
	again := env.do("GET", "/api/v1/schedules/"+strconv.FormatInt(got.ID, 10), "")
	again.wantStatus(t, 200)
	if one := again.asSchedule(t); one.ID != got.ID || one.Name != got.Name || one.FarEnd != got.FarEnd {
		t.Errorf("GET right after: %s", again.Body)
	}
	list := env.do("GET", "/api/v1/schedules", "")
	list.wantStatus(t, 200)
	if all, _ := list.asList(t); len(all) != 1 || all[0].ID != got.ID {
		t.Errorf("the list right after: %s", list.Body)
	}
}

func TestC20_PostWithEveryFieldStoresEach(t *testing.T) {
	defer schNoPanic(t)
	env := schNewEnv(t)
	r := env.do("POST", "/api/v1/schedules",
		`{"far_end":"HTTPS://Dyno.Example:8443","name":"  Gran's  ","kind":"full","every_minutes":60,"offset_minutes":30,"paused":true}`)
	r.wantStatus(t, 201)
	got := r.asSchedule(t)
	if got.Name != "Gran's" || got.FarEnd != "https://dyno.example:8443" || got.Kind != "full" ||
		got.EveryMinutes != 60 || got.OffsetMinutes != 30 || !got.Paused {
		t.Errorf("reply does not hold what was sent: %s", r.Body)
	}
	if got.NextRun != nil {
		t.Errorf("a paused schedule has next_run %v, want null", got.NextRun)
	}
	stored, err := env.st.Schedule(got.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Name != "Gran's" || stored.FarEnd != "https://dyno.example:8443" || stored.Kind != schedule.Full ||
		stored.EveryMinutes != 60 || stored.OffsetMinutes != 30 || !stored.Paused {
		t.Errorf("stored %+v", stored)
	}
	if calls := env.check.calls(); !slices.Equal(calls, []string{"https://dyno.example:8443"}) {
		t.Errorf("Check was called with %v, want the normalized far end once", calls)
	}
}

func TestC21_FailedCheckIs422AndStoresNothing(t *testing.T) {
	defer schNoPanic(t)
	env := schNewEnv(t)
	for i, code := range []string{nodetest.CodeUnreachable, nodetest.CodeTimeout, nodetest.CodeNotDyno} {
		cerr := &nodetest.Error{Code: code, Err: errors.New("the far end said " + code)}
		env.check.setErr(cerr)
		r := env.do("POST", "/api/v1/schedules", `{"far_end":"tailmeadow"}`)
		r.wantStatus(t, 422)
		var body map[string]string
		if err := json.Unmarshal(r.Body, &body); err != nil {
			t.Fatalf("%s: body is not an object of strings: %s", code, r.Body)
		}
		if keys := slices.Sorted(maps.Keys(body)); !slices.Equal(keys, []string{"code", "error"}) {
			t.Errorf("%s: body has keys %v, want code and error", code, keys)
		}
		if body["code"] != code || body["error"] != schErrText(t, cerr) {
			t.Errorf("%s: body = %s, want code %q and the error's text", code, r.Body, code)
		}
		env.expect(t, 0, i+1, 0)
	}

	// An error that isn't a *nodetest.Error is the server's fault.
	env.check.setErr(errors.New("boom"))
	r := env.do("POST", "/api/v1/schedules", `{"far_end":"tailmeadow"}`)
	r.wantStatus(t, 500)
	env.expect(t, 0, 4, 0)
}

func TestC22_BadInputIs400BeforeAnyCheck(t *testing.T) {
	defer schNoPanic(t)
	env := schNewEnv(t)
	for _, c := range []struct{ label, body, field string }{
		{"invalid JSON", `{"far_end": `, ""},
		{"not JSON at all", `tailmeadow`, ""},
		{"an empty body", ``, ""},
		{"not an object", `["tailmeadow"]`, ""},
		{"two objects", `{"far_end":"tailmeadow"}{"far_end":"tailmeadow"}`, ""},
		{"an unknown field", `{"far_end":"tailmeadow","colour":"red"}`, "colour"},
		{"every_minutes as a string", `{"far_end":"tailmeadow","every_minutes":"60"}`, "every_minutes"},
		{"paused as a string", `{"far_end":"tailmeadow","paused":"yes"}`, "paused"},
		{"name as a number", `{"far_end":"tailmeadow","name":5}`, "name"},
		{"every 45", `{"far_end":"tailmeadow","every_minutes":45}`, "every_minutes"},
		{"offset equal to every", `{"far_end":"tailmeadow","every_minutes":60,"offset_minutes":60}`, "offset_minutes"},
		{"a negative offset", `{"far_end":"tailmeadow","offset_minutes":-1}`, "offset_minutes"},
		{"kind medium", `{"far_end":"tailmeadow","kind":"medium"}`, "kind"},
		{"a 65-byte name", `{"far_end":"tailmeadow","name":"` + strings.Repeat("a", 65) + `"}`, "name"},
		{"far end ftp://x", `{"far_end":"ftp://x"}`, "far_end"},
		{"no far_end", `{"name":"Gran's"}`, "far_end"},
		{"an empty far_end", `{"far_end":""}`, "far_end"},
		{"a body over 4 KiB", `{"far_end":"tailmeadow","name":"` + strings.Repeat("a", 5000) + `"}`, ""},
	} {
		r := env.do("POST", "/api/v1/schedules", c.body)
		if r.Status != 400 {
			t.Errorf("%s: status %d, want 400: %s", c.label, r.Status, r.Body)
			continue
		}
		if msg := r.errorText(t); !strings.Contains(msg, c.field) {
			t.Errorf("%s: error %q doesn't name %q", c.label, msg, c.field)
		}
	}
	env.expect(t, 0, 0, 0)
}

func TestC22_BodyLimitIsFourKiB(t *testing.T) {
	defer schNoPanic(t)
	env := schNewEnv(t)
	const base = `{"far_end":"tailmeadow"}`
	exact := base + strings.Repeat(" ", 4096-len(base))
	if r := env.do("POST", "/api/v1/schedules", exact); r.Status != 201 {
		t.Errorf("a body of exactly 4 KiB: status %d, want 201: %s", r.Status, r.Body)
	}
	if r := env.do("POST", "/api/v1/schedules", exact+" "); r.Status != 400 {
		t.Errorf("a body of 4 KiB and a byte: status %d, want 400: %s", r.Status, r.Body)
	}
	env.expect(t, 1, 1, 1)
}

func TestC23_SixteenSchedulesIs409AndNoCheck(t *testing.T) {
	defer schNoPanic(t)
	env := schNewEnv(t)
	for i := range schedule.MaxSchedules {
		schSeed(t, env.st, fmt.Sprintf("host%d", i))
	}
	r := env.do("POST", "/api/v1/schedules", `{"far_end":"tailmeadow"}`)
	r.wantStatus(t, 409)
	// Nothing was checked, stored or announced to the scheduler.
	env.expect(t, schedule.MaxSchedules, 0, 0)
}

// A schedule added while the check runs can take the last place: the store
// says so at the insert, and that is still a 409.
func TestC23_FullAtTheInsertIs409(t *testing.T) {
	defer schNoPanic(t)
	env := schNewEnv(t)
	for i := range schedule.MaxSchedules - 1 {
		schSeed(t, env.st, fmt.Sprintf("host%d", i))
	}
	taken := make(chan error, 1)
	env.check.setHook(func(string) {
		_, err := env.st.AddSchedule(schedule.Schedule{
			Name: "racer", FarEnd: "http://racer:3966", Kind: schedule.Light, EveryMinutes: 180,
		})
		taken <- err
	})
	r := env.do("POST", "/api/v1/schedules", `{"far_end":"tailmeadow"}`)
	select {
	case err := <-taken:
		if err != nil {
			t.Fatalf("the racing add failed: %v", err)
		}
	default:
		t.Fatalf("Check was never called; status %d: %s", r.Status, r.Body)
	}
	r.wantStatus(t, 409)
	if list, err := env.st.Schedules(); err != nil || len(list) != schedule.MaxSchedules {
		t.Errorf("the store holds %d schedules, err %v; want %d", len(list), err, schedule.MaxSchedules)
	}
	for _, sc := range schAll(t, env.st) {
		if sc.FarEnd == "http://tailmeadow:3966" {
			t.Error("the refused schedule was stored")
		}
	}
}

func schAll(t *testing.T, st *store.Store) []schedule.Schedule {
	t.Helper()
	list, err := st.Schedules()
	if err != nil {
		t.Fatal(err)
	}
	return list
}

func TestC24_AtMostFourChecksRunAtOnce(t *testing.T) {
	defer schNoPanic(t)
	env := schNewEnv(t)
	env.check.hold()
	replies := make(chan schResp, 4)
	failures := make(chan error, 4)
	for i := range 4 {
		go func() {
			r, err := env.try("POST", "/api/v1/schedules/check", fmt.Sprintf(`{"far_end":"host%d"}`, i))
			if err != nil {
				failures <- err
				return
			}
			replies <- r
		}()
	}
	env.check.waitEntered(t, 4)

	over := env.do("POST", "/api/v1/schedules/check", `{"far_end":"host4"}`)
	if over.Status != 503 || over.Header.Get("Retry-After") != "1" {
		t.Errorf("the fifth check: status %d, Retry-After %q; want 503 and 1: %s",
			over.Status, over.Header.Get("Retry-After"), over.Body)
	}
	if n := len(env.check.calls()); n != 4 {
		t.Errorf("Check was called %d times while 4 were held, want 4", n)
	}

	env.check.release()
	for range 4 {
		select {
		case r := <-replies:
			if r.Status != 200 {
				t.Errorf("a held check finished with %d: %s", r.Status, r.Body)
			}
		case err := <-failures:
			t.Errorf("a held check failed: %v", err)
		case <-time.After(10 * time.Second):
			t.Fatal("a held check never finished")
		}
	}

	// The four places are free again.
	env.do("POST", "/api/v1/schedules/check", `{"far_end":"host5"}`).wantStatus(t, 200)
}

func TestC24_AddingAScheduleTakesOneOfTheSameFourPlaces(t *testing.T) {
	defer schNoPanic(t)
	env := schNewEnv(t)
	env.check.hold()
	type outcome struct {
		resp schResp
		err  error
	}
	done := make(chan outcome, 4)
	send := func(path, body string) {
		go func() {
			r, err := env.try("POST", path, body)
			done <- outcome{r, err}
		}()
	}
	send("/api/v1/schedules/check", `{"far_end":"host0"}`)
	send("/api/v1/schedules/check", `{"far_end":"host1"}`)
	send("/api/v1/schedules", `{"far_end":"host2"}`)
	send("/api/v1/schedules", `{"far_end":"host3"}`)
	env.check.waitEntered(t, 4)

	for _, c := range []struct{ path, body string }{
		{"/api/v1/schedules", `{"far_end":"host4"}`},
		{"/api/v1/schedules/check", `{"far_end":"host5"}`},
	} {
		r := env.do("POST", c.path, c.body)
		if r.Status != 503 || r.Header.Get("Retry-After") != "1" {
			t.Errorf("POST %s as the fifth: status %d, Retry-After %q; want 503 and 1: %s",
				c.path, r.Status, r.Header.Get("Retry-After"), r.Body)
		}
	}
	if n := len(env.check.calls()); n != 4 {
		t.Errorf("Check was called %d times while 4 were held, want 4", n)
	}

	env.check.release()
	statuses := map[int]int{}
	for range 4 {
		select {
		case o := <-done:
			if o.err != nil {
				t.Errorf("a held request failed: %v", o.err)
				continue
			}
			statuses[o.resp.Status]++
		case <-time.After(10 * time.Second):
			t.Fatal("a held request never finished")
		}
	}
	if statuses[200] != 2 || statuses[201] != 2 {
		t.Errorf("the held requests finished with %v, want two 200s and two 201s", statuses)
	}
	env.expect(t, 2, 4, 2)
}

func TestC25_CheckAnswersWithTheFarEndsPlan(t *testing.T) {
	defer schNoPanic(t)
	env := schNewEnv(t)
	env.check.setInfo(nodetest.FarEndInfo{Name: "tail-dyno", Version: "0.9.3"})
	r := env.do("POST", "/api/v1/schedules/check", `{"far_end": "TailMeadow"}`)
	r.wantStatus(t, 200)
	var body map[string]string
	if err := json.Unmarshal(r.Body, &body); err != nil {
		t.Fatalf("body is not an object of strings: %s", r.Body)
	}
	want := map[string]string{"far_end": "http://tailmeadow:3966", "name": "tail-dyno", "version": "0.9.3"}
	if !maps.Equal(body, want) {
		t.Errorf("body = %s, want exactly %v", r.Body, want)
	}
	if calls := env.check.calls(); !slices.Equal(calls, []string{"http://tailmeadow:3966"}) {
		t.Errorf("Check was called with %v, want the normalized far end once", calls)
	}
	env.expect(t, 0, 1, 0) // a check saves nothing and tells the scheduler nothing
}

func TestC25_CheckRejectsABadAddressWithoutChecking(t *testing.T) {
	defer schNoPanic(t)
	env := schNewEnv(t)
	for _, c := range []struct{ label, body, field string }{
		{"ftp", `{"far_end":"ftp://x"}`, "far_end"},
		{"a path", `{"far_end":"http://h/path"}`, "far_end"},
		{"port 0", `{"far_end":"h:0"}`, "far_end"},
		{"an empty far_end", `{"far_end":""}`, "far_end"},
		{"no far_end", `{}`, "far_end"},
		{"invalid JSON", `{"far_end"`, ""},
		{"an unknown field", `{"far_end":"tailmeadow","colour":"red"}`, "colour"},
	} {
		r := env.do("POST", "/api/v1/schedules/check", c.body)
		if r.Status != 400 {
			t.Errorf("%s: status %d, want 400: %s", c.label, r.Status, r.Body)
			continue
		}
		if msg := r.errorText(t); !strings.Contains(msg, c.field) {
			t.Errorf("%s: error %q doesn't name %q", c.label, msg, c.field)
		}
	}
	env.expect(t, 0, 0, 0)
}

func TestC25_FailingCheckIs422(t *testing.T) {
	defer schNoPanic(t)
	env := schNewEnv(t)
	for i, code := range []string{nodetest.CodeUnreachable, nodetest.CodeTimeout, nodetest.CodeNotDyno} {
		cerr := &nodetest.Error{Code: code, Err: errors.New("the far end said " + code)}
		env.check.setErr(cerr)
		r := env.do("POST", "/api/v1/schedules/check", `{"far_end":"tailmeadow"}`)
		r.wantStatus(t, 422)
		var body map[string]string
		if err := json.Unmarshal(r.Body, &body); err != nil {
			t.Fatalf("%s: body is not an object of strings: %s", code, r.Body)
		}
		if keys := slices.Sorted(maps.Keys(body)); !slices.Equal(keys, []string{"code", "error"}) {
			t.Errorf("%s: body has keys %v, want code and error", code, keys)
		}
		if body["code"] != code || body["error"] != schErrText(t, cerr) {
			t.Errorf("%s: body = %s, want code %q and the error's text", code, r.Body, code)
		}
		env.expect(t, 0, i+1, 0)
	}
	env.check.setErr(errors.New("boom"))
	env.do("POST", "/api/v1/schedules/check", `{"far_end":"tailmeadow"}`).wantStatus(t, 500)
	env.expect(t, 0, 4, 0)
}

func TestC26_CheckGetsADeadlineOfAtMostTenSeconds(t *testing.T) {
	defer schNoPanic(t)
	env := schNewEnv(t)
	added := env.add(t, `{"far_end":"tailmeadow"}`)
	env.do("POST", "/api/v1/schedules/check", `{"far_end":"tailmeadow"}`).wantStatus(t, 200)
	env.do("PATCH", "/api/v1/schedules/"+strconv.FormatInt(added.ID, 10), `{"far_end":"gran-house"}`).wantStatus(t, 200)

	left := env.check.deadlines()
	if len(left) != 3 {
		t.Fatalf("Check was called %d times, want 3 (add, check, patch)", len(left))
	}
	for i, d := range left {
		if d <= 0 || d > 10*time.Second {
			t.Errorf("call %d: %v left to the ctx deadline (-1ns means none), want more than 0 and at most 10s", i+1, d)
		}
	}
}

func TestC27_GetOneSchedule(t *testing.T) {
	defer schNoPanic(t)
	env := schNewEnv(t)
	added := env.add(t, `{"far_end":"tailmeadow","name":"Gran's","every_minutes":60,"offset_minutes":5}`)
	other := env.add(t, `{"far_end":"gran-house"}`)

	r := env.do("GET", "/api/v1/schedules/"+strconv.FormatInt(added.ID, 10), "")
	r.wantStatus(t, 200)
	got := r.asSchedule(t)
	if got.ID != added.ID || got.Name != "Gran's" || got.FarEnd != "http://tailmeadow:3966" || got.Kind != "light" ||
		got.EveryMinutes != 60 || got.OffsetMinutes != 5 || got.Paused ||
		!got.CreatedAt.Equal(added.CreatedAt) || !got.UpdatedAt.Equal(added.UpdatedAt) {
		t.Errorf("GET one = %s, want the schedule that was added", r.Body)
	}
	if got.ID == other.ID {
		t.Errorf("got the other schedule: %s", r.Body)
	}

	for _, id := range []string{"999", "0", "-1"} {
		if r := env.do("GET", "/api/v1/schedules/"+id, ""); r.Status != 404 {
			t.Errorf("GET schedule %s: status %d, want 404: %s", id, r.Status, r.Body)
		}
	}
	for _, id := range []string{"abc", "1.5"} {
		if r := env.do("GET", "/api/v1/schedules/"+id, ""); r.Status != 400 {
			t.Errorf("GET schedule %s: status %d, want 400: %s", id, r.Status, r.Body)
		}
	}
}

func TestC28_PatchChangesOnlyTheFieldsSent(t *testing.T) {
	defer schNoPanic(t)
	env := schNewEnv(t)
	orig := env.add(t, `{"far_end":"tailmeadow","name":"Gran's","kind":"full","every_minutes":180,"offset_minutes":0}`)
	path := "/api/v1/schedules/" + strconv.FormatInt(orig.ID, 10)
	unchanged := func(label string, got schJSON) {
		t.Helper()
		if got.ID != orig.ID || got.Name != orig.Name || got.FarEnd != orig.FarEnd || got.Kind != orig.Kind ||
			!got.CreatedAt.Equal(orig.CreatedAt) {
			t.Errorf("%s changed a field that was not sent: %+v, was %+v", label, got, orig)
		}
	}

	// New timing: next_run moves to the new grid at once, and no check runs.
	time.Sleep(5 * time.Millisecond) // so updated_at, kept to the millisecond, can move
	before := time.Now()
	r := env.do("PATCH", path, `{"every_minutes":60,"offset_minutes":30}`)
	after := time.Now()
	r.wantStatus(t, 200)
	retimed := r.asSchedule(t)
	unchanged("retiming", retimed)
	if retimed.EveryMinutes != 60 || retimed.OffsetMinutes != 30 || retimed.Paused {
		t.Errorf("retiming: %s", r.Body)
	}
	first, last := schNextGrid(before, 60, 30), schNextGrid(after, 60, 30)
	if retimed.NextRun == nil || !(retimed.NextRun.Equal(first) || retimed.NextRun.Equal(last)) {
		t.Errorf("retiming: next_run = %v, want %s", retimed.NextRun, first.Format(time.RFC3339))
	}
	if !retimed.UpdatedAt.After(orig.UpdatedAt) {
		t.Errorf("retiming: updated_at %s did not move on from %s", retimed.UpdatedAt, orig.UpdatedAt)
	}
	if snap := env.sched.lastSnapshot(); env.sched.changes() != 2 || len(snap) != 1 ||
		snap[0].EveryMinutes != 60 || snap[0].OffsetMinutes != 30 {
		t.Errorf("Changed was called %d times; at the latest call the store held %+v", env.sched.changes(), snap)
	}

	// Pausing needs no working far end, and clears next_run.
	env.check.setErr(&nodetest.Error{Code: nodetest.CodeUnreachable, Err: errors.New("no route")})
	time.Sleep(5 * time.Millisecond)
	r = env.do("PATCH", path, `{"paused":true}`)
	r.wantStatus(t, 200)
	paused := r.asSchedule(t)
	unchanged("pausing", paused)
	if !paused.Paused || paused.EveryMinutes != 60 || paused.OffsetMinutes != 30 {
		t.Errorf("pausing: %s", r.Body)
	}
	if string(r.asObject(t)["next_run"]) != "null" {
		t.Errorf("pausing: next_run = %s, want null", r.asObject(t)["next_run"])
	}
	if !paused.UpdatedAt.After(retimed.UpdatedAt) {
		t.Errorf("pausing: updated_at %s did not move on from %s", paused.UpdatedAt, retimed.UpdatedAt)
	}
	if snap := env.sched.lastSnapshot(); env.sched.changes() != 3 || len(snap) != 1 || !snap[0].Paused {
		t.Errorf("Changed was called %d times; at the latest call the store held %+v", env.sched.changes(), snap)
	}

	// Resuming gives it a next run again.
	r = env.do("PATCH", path, `{"paused":false}`)
	r.wantStatus(t, 200)
	if resumed := r.asSchedule(t); resumed.Paused || resumed.NextRun == nil {
		t.Errorf("resuming: %s", r.Body)
	}

	if calls := env.check.calls(); len(calls) != 1 {
		t.Errorf("Check was called %d times, want only the one for the add: %v", len(calls), calls)
	}
}

func TestC29_PatchOfTheFarEndChecksTheNewAddress(t *testing.T) {
	defer schNoPanic(t)
	env := schNewEnv(t)
	orig := env.add(t, `{"far_end":"tailmeadow","name":"Gran's"}`)
	path := "/api/v1/schedules/" + strconv.FormatInt(orig.ID, 10)

	// A new far end is checked, as normalized.
	r := env.do("PATCH", path, `{"far_end":"Gran-House:8080"}`)
	r.wantStatus(t, 200)
	moved := r.asSchedule(t)
	if moved.FarEnd != "http://gran-house:8080" || moved.Name != "Gran's" {
		t.Errorf("after moving the far end: %s", r.Body)
	}
	if calls := env.check.calls(); !slices.Equal(calls, []string{"http://tailmeadow:3966", "http://gran-house:8080"}) {
		t.Errorf("Check was called with %v, want the add's far end then the new one, normalized", calls)
	}
	if snap := env.sched.lastSnapshot(); env.sched.changes() != 2 || len(snap) != 1 || snap[0].FarEnd != "http://gran-house:8080" {
		t.Errorf("Changed was called %d times; at the latest call the store held %+v", env.sched.changes(), snap)
	}

	// A far end that fails its check is a 422, and nothing changes, the
	// name sent with it included.
	cerr := &nodetest.Error{Code: nodetest.CodeUnreachable, Err: errors.New("no route to host")}
	env.check.setErr(cerr)
	time.Sleep(5 * time.Millisecond)
	r = env.do("PATCH", path, `{"far_end":"elsewhere","name":"Renamed"}`)
	r.wantStatus(t, 422)
	var body map[string]string
	if err := json.Unmarshal(r.Body, &body); err != nil {
		t.Fatalf("body is not an object of strings: %s", r.Body)
	}
	if body["code"] != nodetest.CodeUnreachable || body["error"] != schErrText(t, cerr) || len(body) != 2 {
		t.Errorf("body = %s, want the unreachable code and the error's text", r.Body)
	}
	stored, err := env.st.Schedule(orig.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.FarEnd != "http://gran-house:8080" || stored.Name != "Gran's" || !stored.UpdatedAt.Equal(moved.UpdatedAt) {
		t.Errorf("a failed check changed the schedule: %+v", stored)
	}
	if got := len(env.check.calls()); got != 3 {
		t.Errorf("Check was called %d times, want 3", got)
	}
	if env.sched.changes() != 2 {
		t.Errorf("Changed was called %d times, want it left at 2 by the failed patch", env.sched.changes())
	}

	// A far end that normalizes to the stored one is not checked again.
	env.check.setErr(nil)
	r = env.do("PATCH", path, `{"far_end":" HTTP://Gran-House:8080/ "}`)
	r.wantStatus(t, 200)
	if same := r.asSchedule(t); same.FarEnd != "http://gran-house:8080" {
		t.Errorf("far_end = %q", same.FarEnd)
	}
	if got := len(env.check.calls()); got != 3 {
		t.Errorf("Check was called %d times, want it left at 3 for the same far end", got)
	}

	// An address that can't be a far end is a 400, before any check.
	r = env.do("PATCH", path, `{"far_end":"ftp://x"}`)
	r.wantStatus(t, 400)
	if msg := r.errorText(t); !strings.Contains(msg, "far_end") {
		t.Errorf("error %q doesn't name far_end", msg)
	}
	if got := len(env.check.calls()); got != 3 {
		t.Errorf("Check was called %d times, want it left at 3 for a bad address", got)
	}
}

func TestC30_PatchNameDefaultsAndBadPatchesChangeNothing(t *testing.T) {
	defer schNoPanic(t)
	env := schNewEnv(t)
	orig := env.add(t, `{"far_end":"tailmeadow","name":"Gran's","every_minutes":60,"offset_minutes":30}`)
	path := "/api/v1/schedules/" + strconv.FormatInt(orig.ID, 10)

	// An empty name goes back to the far end's host; a name is trimmed.
	r := env.do("PATCH", path, `{"name":""}`)
	r.wantStatus(t, 200)
	if got := r.asSchedule(t); got.Name != "tailmeadow" {
		t.Errorf(`name "" gave %q, want the far end's host tailmeadow`, got.Name)
	}
	r = env.do("PATCH", path, `{"name":"  Office  "}`)
	r.wantStatus(t, 200)
	if got := r.asSchedule(t); got.Name != "Office" {
		t.Errorf(`name "  Office  " gave %q, want Office`, got.Name)
	}

	snapshot := env.do("GET", path, "")
	snapshot.wantStatus(t, 200)
	before := snapshot.asSchedule(t)
	for _, c := range []struct{ label, body, field string }{
		{"an offset past the new interval", `{"every_minutes":15}`, "offset_minutes"},
		{"an offset equal to every", `{"offset_minutes":60}`, "offset_minutes"},
		{"a negative offset", `{"offset_minutes":-1}`, "offset_minutes"},
		{"every 45", `{"every_minutes":45}`, "every_minutes"},
		{"kind medium", `{"kind":"medium"}`, "kind"},
		{"a 65-byte name", `{"name":"` + strings.Repeat("a", 65) + `"}`, "name"},
		{"a bad far end", `{"far_end":"ftp://x"}`, "far_end"},
		{"an unknown field", `{"colour":"red"}`, "colour"},
		{"every_minutes as a string", `{"every_minutes":"60"}`, "every_minutes"},
		{"paused as a string", `{"paused":"yes"}`, "paused"},
		{"invalid JSON", `{"paused":`, ""},
		{"two objects", `{"paused":true}{"paused":false}`, ""},
		{"a body over 4 KiB", `{"name":"` + strings.Repeat("a", 5000) + `"}`, ""},
	} {
		r := env.do("PATCH", path, c.body)
		if r.Status != 400 {
			t.Errorf("%s: status %d, want 400: %s", c.label, r.Status, r.Body)
			continue
		}
		if msg := r.errorText(t); !strings.Contains(msg, c.field) {
			t.Errorf("%s: error %q doesn't name %q", c.label, msg, c.field)
		}
	}
	afterReply := env.do("GET", path, "")
	afterReply.wantStatus(t, 200)
	after := afterReply.asSchedule(t)
	if after.Name != before.Name || after.FarEnd != before.FarEnd || after.Kind != before.Kind ||
		after.EveryMinutes != before.EveryMinutes || after.OffsetMinutes != before.OffsetMinutes ||
		after.Paused != before.Paused || !after.CreatedAt.Equal(before.CreatedAt) || !after.UpdatedAt.Equal(before.UpdatedAt) {
		t.Errorf("bad patches changed the schedule:\n before %+v\n  after %+v", before, after)
	}
	env.expect(t, 1, 1, 3) // the add and the two good patches

	r = env.do("PATCH", "/api/v1/schedules/999", `{"paused":true}`)
	if r.Status != 404 {
		t.Errorf("PATCH of an unknown id: status %d, want 404: %s", r.Status, r.Body)
	}
}

func TestC31_DeleteStopsTheScheduleAndKeepsItsResults(t *testing.T) {
	defer schNoPanic(t)
	env := schNewEnv(t)
	doomed := env.add(t, `{"far_end":"tailmeadow"}`)
	kept := env.add(t, `{"far_end":"gran-house"}`)
	path := "/api/v1/schedules/" + strconv.FormatInt(doomed.ID, 10)
	ok := schStoreRun(t, env.st, doomed.ID, doomed.FarEnd, time.Now().Add(-time.Hour), nil)
	failed := schStoreRun(t, env.st, doomed.ID, doomed.FarEnd, time.Now().Add(-30*time.Minute), schTimeout)

	r := env.do("DELETE", path, "")
	r.wantStatus(t, 204)
	if len(r.Body) != 0 {
		t.Errorf("a 204 has a body: %s", r.Body)
	}
	if snap := env.sched.lastSnapshot(); env.sched.changes() != 3 || len(snap) != 1 || snap[0].ID != kept.ID {
		t.Errorf("Changed was called %d times; at the latest call the store held %+v, want only the other schedule",
			env.sched.changes(), snap)
	}
	if r := env.do("GET", path, ""); r.Status != 404 {
		t.Errorf("GET after the delete: status %d, want 404: %s", r.Status, r.Body)
	}
	if r := env.do("DELETE", path, ""); r.Status != 404 {
		t.Errorf("second DELETE: status %d, want 404: %s", r.Status, r.Body)
	}
	if r := env.do("DELETE", "/api/v1/schedules/999", ""); r.Status != 404 {
		t.Errorf("DELETE of an unknown id: status %d, want 404: %s", r.Status, r.Body)
	}
	list := env.do("GET", "/api/v1/schedules", "")
	list.wantStatus(t, 200)
	if all, _ := list.asList(t); len(all) != 1 || all[0].ID != kept.ID {
		t.Errorf("the list after the delete: %s", list.Body)
	}

	for _, want := range []result.Result{ok, failed} {
		r := env.do("GET", "/api/v1/results/"+strconv.FormatInt(want.ID, 10), "")
		r.wantStatus(t, 200)
		var got result.Result
		if err := json.Unmarshal(r.Body, &got); err != nil {
			t.Fatalf("result %d: %v: %s", want.ID, err, r.Body)
		}
		if got.ScheduleID != doomed.ID || got.FarEnd != doomed.FarEnd || (got.Failure == nil) != (want.Failure == nil) {
			t.Errorf("result %d after the delete: %s, want schedule_id %d and far_end %s",
				want.ID, r.Body, doomed.ID, doomed.FarEnd)
		}
		if raw := r.asObject(t); string(raw["schedule_id"]) != strconv.FormatInt(doomed.ID, 10) {
			t.Errorf("result %d: schedule_id = %s, want %d", want.ID, raw["schedule_id"], doomed.ID)
		}
	}
}

var schTimeout = &result.Failure{Code: "timeout", Phase: "download", Message: "The far end stopped answering during the download."}

func TestC32_RunQueuesARunAndReportsStatus(t *testing.T) {
	defer schNoPanic(t)
	env := schNewEnv(t)
	sc := env.add(t, `{"far_end":"tailmeadow"}`)
	paused := env.add(t, `{"far_end":"gran-house","paused":true}`)
	run := func(id int64) schResp {
		t.Helper()
		return env.do("POST", "/api/v1/schedules/"+strconv.FormatInt(id, 10)+"/run", "")
	}

	// As the real scheduler does, RunNow marks the schedule queued.
	env.sched.setQueueOnRun(true)
	r := run(sc.ID)
	r.wantStatus(t, 202)
	if got := r.asSchedule(t); got.ID != sc.ID || !got.Queued || got.Running {
		t.Errorf("reply to a run: %s, want the schedule, queued", r.Body)
	}
	if ran := env.sched.ran(); !slices.Equal(ran, []int64{sc.ID}) {
		t.Errorf("RunNow was called with %v, want [%d]", ran, sc.ID)
	}

	// A paused schedule is run when asked.
	r = run(paused.ID)
	r.wantStatus(t, 202)
	if got := r.asSchedule(t); got.ID != paused.ID || !got.Paused || !got.Queued {
		t.Errorf("reply to a run of a paused schedule: %s", r.Body)
	}
	if ran := env.sched.ran(); !slices.Equal(ran, []int64{sc.ID, paused.ID}) {
		t.Errorf("RunNow was called with %v, want [%d %d]", ran, sc.ID, paused.ID)
	}

	// Running and queued come from Status, whatever RunNow did.
	env.sched.setQueueOnRun(false)
	env.sched.setStatus(sc.ID, schedule.Status{Running: true})
	r = run(sc.ID)
	r.wantStatus(t, 202)
	if got := r.asSchedule(t); !got.Running || got.Queued {
		t.Errorf("with Status running: %s, want running and not queued", r.Body)
	}
	env.sched.setStatus(sc.ID, schedule.Status{})
	r = run(sc.ID)
	r.wantStatus(t, 202)
	if got := r.asSchedule(t); got.Running || got.Queued {
		t.Errorf("with Status neither: %s, want neither", r.Body)
	}
}

func TestC32_RunOfAnUnknownScheduleIs404(t *testing.T) {
	defer schNoPanic(t)
	env := schNewEnv(t)
	if r := env.do("POST", "/api/v1/schedules/999/run", ""); r.Status != 404 {
		t.Errorf("run of an id that was never added: status %d, want 404: %s", r.Status, r.Body)
	}
	// One the scheduler has not heard of (RunNow says store.ErrNotFound) is 404 too.
	sc := env.add(t, `{"far_end":"tailmeadow"}`)
	env.sched.markUnknown(sc.ID)
	if r := env.do("POST", "/api/v1/schedules/"+strconv.FormatInt(sc.ID, 10)+"/run", ""); r.Status != 404 {
		t.Errorf("run of an id the scheduler doesn't know: status %d, want 404: %s", r.Status, r.Body)
	}
}

func TestC32_RunIsRefusedForAMinuteAfterTheLastResult(t *testing.T) {
	defer schNoPanic(t)
	env := schNewEnv(t)
	recent := env.add(t, `{"far_end":"tailmeadow"}`)
	recentFailure := env.add(t, `{"far_end":"gran-house"}`)
	old := env.add(t, `{"far_end":"office"}`)
	now := time.Now()
	schStoreRun(t, env.st, recent.ID, recent.FarEnd, now.Add(-20*time.Second), nil)
	schStoreRun(t, env.st, recentFailure.ID, recentFailure.FarEnd, now.Add(-20*time.Second), schTimeout)
	schStoreRun(t, env.st, old.ID, old.FarEnd, now.Add(-61*time.Second), nil)

	for _, sc := range []schJSON{recent, recentFailure} {
		r := env.do("POST", "/api/v1/schedules/"+strconv.FormatInt(sc.ID, 10)+"/run", "")
		if r.Status != 429 {
			t.Errorf("run %d seconds after a result (schedule %d): status %d, want 429: %s", 20, sc.ID, r.Status, r.Body)
			continue
		}
		secs, err := strconv.Atoi(r.Header.Get("Retry-After"))
		if err != nil || secs < 38 || secs > 41 {
			t.Errorf("schedule %d: Retry-After = %q, want a number of seconds from 38 to 41", sc.ID, r.Header.Get("Retry-After"))
		}
		if slices.Contains(env.sched.ran(), sc.ID) {
			t.Errorf("RunNow was called for schedule %d, which was refused", sc.ID)
		}
	}

	r := env.do("POST", "/api/v1/schedules/"+strconv.FormatInt(old.ID, 10)+"/run", "")
	r.wantStatus(t, 202)
	if ran := env.sched.ran(); !slices.Equal(ran, []int64{old.ID}) {
		t.Errorf("RunNow was called with %v, want only [%d]", ran, old.ID)
	}
}

func TestC33_RepliesShowStatusAndTheLastResults(t *testing.T) {
	defer schNoPanic(t)
	env := schNewEnv(t)
	a := schSeed(t, env.st, "a") // a success, then a failure
	b := schSeed(t, env.st, "b") // only a failure
	c := schSeed(t, env.st, "c") // never ran
	d := schSeed(t, env.st, "d") // a success
	t0 := time.Date(2026, 11, 13, 12, 0, 0, 123_000_000, time.UTC)
	schStoreRun(t, env.st, a.ID, a.FarEnd, t0, nil)
	a2 := schStoreRun(t, env.st, a.ID, a.FarEnd, t0.Add(time.Hour), nil)
	b1 := schStoreRun(t, env.st, b.ID, b.FarEnd, t0.Add(90*time.Minute), schTimeout)
	a3 := schStoreRun(t, env.st, a.ID, a.FarEnd, t0.Add(2*time.Hour), schTimeout)
	d1 := schStoreRun(t, env.st, d.ID, d.FarEnd, t0.Add(150*time.Minute), nil)
	client := result.Result{At: t0.Add(4 * time.Hour), Source: "client"} // the newest result of all, and nobody's
	client.Client, client.DownloadMbps, client.IdleMs = "Chrome on Linux", 500, 9
	if _, err := env.st.Add(client); err != nil {
		t.Fatal(err)
	}
	env.sched.setStatus(a.ID, schedule.Status{Running: true})
	env.sched.setStatus(b.ID, schedule.Status{Queued: true})

	type want struct {
		id              int64
		running, queued bool
		last            *result.Result // its newest result; nil for none
		success         *result.Result // its newest success, whose at is last_success_at; nil for none
	}
	wants := []want{
		{a.ID, true, false, &a3, &a2},
		{b.ID, false, true, &b1, nil},
		{c.ID, false, false, nil, nil},
		{d.ID, false, false, &d1, &d1},
	}
	check := func(where string, w want, got schJSON, raw map[string]json.RawMessage) {
		t.Helper()
		if got.ID != w.id || got.Running != w.running || got.Queued != w.queued {
			t.Errorf("%s: id %d, running %v, queued %v; want id %d, running %v, queued %v",
				where, got.ID, got.Running, got.Queued, w.id, w.running, w.queued)
		}
		if w.last == nil {
			if got.LastResult != nil || string(raw["last_result"]) != "null" {
				t.Errorf("%s: last_result = %s, want null", where, raw["last_result"])
			}
		} else {
			switch {
			case got.LastResult == nil || got.LastResult.ID != w.last.ID || got.LastResult.ScheduleID != w.id:
				t.Errorf("%s: last_result = %s, want result %d", where, raw["last_result"], w.last.ID)
			case (got.LastResult.Failure == nil) != (w.last.Failure == nil):
				t.Errorf("%s: last_result's failure = %+v, want %+v", where, got.LastResult.Failure, w.last.Failure)
			case w.last.Failure != nil && *got.LastResult.Failure != *w.last.Failure:
				t.Errorf("%s: last_result's failure = %+v, want %+v", where, *got.LastResult.Failure, *w.last.Failure)
			case w.last.Failure == nil && got.LastResult.DownloadMbps != w.last.DownloadMbps:
				t.Errorf("%s: last_result's download_mbps = %v, want %v", where, got.LastResult.DownloadMbps, w.last.DownloadMbps)
			}
			var last map[string]json.RawMessage
			if err := json.Unmarshal(raw["last_result"], &last); err != nil {
				t.Errorf("%s: last_result is not an object: %s", where, raw["last_result"])
			} else if _, has := last["detail"]; has {
				t.Errorf("%s: last_result has detail: %s", where, raw["last_result"])
			}
		}
		if w.success == nil {
			if got.LastSuccessAt != nil || string(raw["last_success_at"]) != "null" {
				t.Errorf("%s: last_success_at = %s, want null", where, raw["last_success_at"])
			}
		} else if got.LastSuccessAt == nil || !got.LastSuccessAt.Equal(w.success.At) {
			t.Errorf("%s: last_success_at = %v, want %s", where, got.LastSuccessAt, w.success.At.Format(time.RFC3339Nano))
		}
	}

	list := env.do("GET", "/api/v1/schedules", "")
	list.wantStatus(t, 200)
	typed, raws := list.asList(t)
	if len(typed) != len(wants) {
		t.Fatalf("the list has %d schedules, want %d: %s", len(typed), len(wants), list.Body)
	}
	for i, w := range wants {
		check(fmt.Sprintf("list[%d]", i), w, typed[i], raws[i])
	}
	for _, w := range wants {
		r := env.do("GET", "/api/v1/schedules/"+strconv.FormatInt(w.id, 10), "")
		r.wantStatus(t, 200)
		check(fmt.Sprintf("GET %d", w.id), w, r.asSchedule(t), r.asObject(t))
	}
}

func TestC34_ScheduleJSONHasExactlyTheDocumentedKeys(t *testing.T) {
	defer schNoPanic(t)
	env := schNewEnv(t)
	wantKeys := slices.Sorted(slices.Values(schKeys))
	check := func(where string, m map[string]json.RawMessage, paused bool) {
		t.Helper()
		if got := slices.Sorted(maps.Keys(m)); !slices.Equal(got, wantKeys) {
			t.Errorf("%s: keys %v, want %v", where, got, wantKeys)
		}
		if isNull := string(m["next_run"]) == "null"; isNull != paused {
			t.Errorf("%s: next_run = %s with paused %v, want null exactly when paused", where, m["next_run"], paused)
		}
	}
	created := env.do("POST", "/api/v1/schedules", `{"far_end":"tailmeadow"}`)
	created.wantStatus(t, 201)
	sc := created.asSchedule(t)
	path := "/api/v1/schedules/" + strconv.FormatInt(sc.ID, 10)
	check("POST reply", created.asObject(t), false)

	allThree := func(paused bool) {
		t.Helper()
		one := env.do("GET", path, "")
		one.wantStatus(t, 200)
		check("GET one", one.asObject(t), paused)
		list := env.do("GET", "/api/v1/schedules", "")
		list.wantStatus(t, 200)
		if _, raws := list.asList(t); len(raws) != 1 {
			t.Fatalf("the list has %d schedules: %s", len(raws), list.Body)
		} else {
			check("list entry", raws[0], paused)
		}
		run := env.do("POST", path+"/run", "")
		run.wantStatus(t, 202)
		check("run reply", run.asObject(t), paused)
	}
	allThree(false)

	// With a result stored, last_result and last_success_at are filled in; the keys stay.
	schStoreRun(t, env.st, sc.ID, sc.FarEnd, time.Now().Add(-2*time.Hour), nil)
	allThree(false)
	if raw := env.do("GET", path, "").asObject(t); string(raw["last_result"]) == "null" || string(raw["last_success_at"]) == "null" {
		t.Errorf("last_result and last_success_at should be filled in: %v", raw)
	}

	// Paused, next_run is null and the keys stay.
	patched := env.do("PATCH", path, `{"paused":true}`)
	patched.wantStatus(t, 200)
	check("PATCH reply", patched.asObject(t), true)
	allThree(true)
}

func TestC35_WithoutAScheduler(t *testing.T) {
	defer schNoPanic(t)
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	a := &api.API{Version: "1.2.3", Store: st, Runs: testrun.NewRegistry(), Log: slog.New(slog.DiscardHandler),
		Check: schNewCheck().Check} // no Scheduler
	cl := schServe(t, a)

	cl.do("GET", "/api/v1/schedules", "").wantStatus(t, 200)
	added := cl.do("POST", "/api/v1/schedules", `{"far_end":"tailmeadow"}`)
	added.wantStatus(t, 201)
	sc := added.asSchedule(t)
	if sc.Running || sc.Queued {
		t.Errorf("a schedule with no scheduler is running or queued: %s", added.Body)
	}
	path := "/api/v1/schedules/" + strconv.FormatInt(sc.ID, 10)
	one := cl.do("GET", path, "")
	one.wantStatus(t, 200)
	if got := one.asSchedule(t); got.ID != sc.ID || got.Running || got.Queued {
		t.Errorf("GET one: %s", one.Body)
	}
	patched := cl.do("PATCH", path, `{"name":"Renamed","paused":true}`)
	patched.wantStatus(t, 200)
	if got := patched.asSchedule(t); got.Name != "Renamed" || !got.Paused {
		t.Errorf("PATCH: %s", patched.Body)
	}
	if r := cl.do("POST", path+"/run", ""); r.Status != 503 {
		t.Errorf("run with no scheduler: status %d, want 503: %s", r.Status, r.Body)
	}
	if list, _ := cl.do("GET", "/api/v1/schedules", "").asList(t); len(list) != 1 || list[0].Name != "Renamed" {
		t.Errorf("the list after editing: %+v", list)
	}
	cl.do("DELETE", path, "").wantStatus(t, 204)
	if r := cl.do("GET", path, ""); r.Status != 404 {
		t.Errorf("GET after the delete: status %d, want 404: %s", r.Status, r.Body)
	}
}

func TestC36_RouteOfMapsEverySchedulesAndSettingsPattern(t *testing.T) {
	defer schNoPanic(t)
	for _, c := range []struct {
		pattern string
		want    metrics.Route
	}{
		{"GET /api/v1/schedules", metrics.RouteSchedules},
		{"POST /api/v1/schedules", metrics.RouteSchedules},
		{"POST /api/v1/schedules/check", metrics.RouteSchedules},
		{"GET /api/v1/schedules/{id}", metrics.RouteSchedules},
		{"PATCH /api/v1/schedules/{id}", metrics.RouteSchedules},
		{"DELETE /api/v1/schedules/{id}", metrics.RouteSchedules},
		{"POST /api/v1/schedules/{id}/run", metrics.RouteSchedules},
		{"GET /api/v1/settings", metrics.RouteSettings},
		{"PATCH /api/v1/settings", metrics.RouteSettings},
		{"GET /api/v1/results.csv", metrics.RouteResults},
		{"GET /api/v1/schedules/17", metrics.RouteOther}, // a raw path is never a label
	} {
		if got := metrics.RouteOf(c.pattern); got != c.want {
			t.Errorf("RouteOf(%q) = %d, want %d", c.pattern, got, c.want)
		}
	}
}

// The patterns Register uses, not a list kept beside them, decide the route.
func TestC36_RegisteredRoutesMapToTheirRoute(t *testing.T) {
	defer schNoPanic(t)
	mux := http.NewServeMux()
	(&api.API{}).Register(mux)
	for _, c := range []struct {
		method, path string
		want         metrics.Route
	}{
		{"GET", "/api/v1/schedules", metrics.RouteSchedules},
		{"POST", "/api/v1/schedules", metrics.RouteSchedules},
		{"POST", "/api/v1/schedules/check", metrics.RouteSchedules},
		{"GET", "/api/v1/schedules/7", metrics.RouteSchedules},
		{"PATCH", "/api/v1/schedules/7", metrics.RouteSchedules},
		{"DELETE", "/api/v1/schedules/7", metrics.RouteSchedules},
		{"POST", "/api/v1/schedules/7/run", metrics.RouteSchedules},
		{"GET", "/api/v1/settings", metrics.RouteSettings},
		{"PATCH", "/api/v1/settings", metrics.RouteSettings},
		{"GET", "/api/v1/results.csv", metrics.RouteResults},
		{"GET", "/api/v1/results", metrics.RouteResults},
		{"GET", "/api/v1/results/7", metrics.RouteResults},
		{"POST", "/api/v1/results", metrics.RouteResults},
	} {
		_, pattern := mux.Handler(httptest.NewRequest(c.method, c.path, nil))
		if pattern == "" {
			t.Errorf("%s %s matches no registered pattern", c.method, c.path)
			continue
		}
		if got := metrics.RouteOf(pattern); got != c.want {
			t.Errorf("%s %s (pattern %q): route %d, want %d", c.method, c.path, pattern, got, c.want)
		}
	}
}

func TestC36_InstrumentCountsSchedulesRequestsUnderTheirOwnRoute(t *testing.T) {
	defer schNoPanic(t)
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	a := &api.API{Version: "1.2.3", Store: st, Runs: testrun.NewRegistry(), Log: slog.New(slog.DiscardHandler)}
	mux := http.NewServeMux()
	a.Register(mux)
	m := metrics.New("1.2.3")
	srv := httptest.NewServer(m.Instrument(mux))
	t.Cleanup(srv.Close)

	for _, c := range []struct {
		method, path string
		status       int
	}{
		{"GET", "/api/v1/schedules", 200},
		{"GET", "/api/v1/schedules/99", 404},
		{"DELETE", "/api/v1/schedules/99", 404},
	} {
		req, err := http.NewRequest(c.method, srv.URL+c.path, nil)
		if err != nil {
			t.Fatal(err)
		}
		resp, err := srv.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		if resp.StatusCode != c.status {
			t.Fatalf("%s %s: status %d, want %d", c.method, c.path, resp.StatusCode, c.status)
		}
	}
	out := string(m.Append(nil))
	for _, line := range []string{
		`dyno_requests_total{route="schedules",code="200"} 1`,
		`dyno_requests_total{route="schedules",code="404"} 2`,
	} {
		if !strings.Contains(out, line+"\n") {
			t.Errorf("exposition has no line %q", line)
		}
	}
	if strings.Contains(out, `route="other"`) {
		t.Error("a schedules request was counted under route other")
	}
}
