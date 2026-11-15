package api_test

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/redlinelabs-dev/dyno/server/internal/api"
	"github.com/redlinelabs-dev/dyno/server/internal/nodetest"
	"github.com/redlinelabs-dev/dyno/server/internal/retention"
	"github.com/redlinelabs-dev/dyno/server/internal/schedule"
	"github.com/redlinelabs-dev/dyno/server/internal/settings"
	"github.com/redlinelabs-dev/dyno/server/internal/store"
	"github.com/redlinelabs-dev/dyno/server/internal/testrun"
	_ "modernc.org/sqlite"
)

// These tests are about the events the write routes log (ADR-0020, amended
// 2026-11-14): cases C37 and H22, and the PATCH half of H23. A route's event
// is counted by its name, so a results.prune beside a settings.patch doesn't
// count against it. The log is read as soon as the reply arrives, which holds
// the handlers to logging before they answer.

// evOpts changes what evNewEnv builds: noCheck leaves API.Check nil,
// noScheduler leaves API.Scheduler nil, and runErr makes RunNow fail with it.
type evOpts struct {
	noCheck     bool
	noScheduler bool
	runErr      error
}

// evFailingScheduler is a Scheduler whose RunNow fails with err; the rest is
// schFakeScheduler's.
type evFailingScheduler struct {
	*schFakeScheduler
	err error
}

func (f *evFailingScheduler) RunNow(int64) error { return f.err }

// evLog is where the API under test logs: a buffer that the server's
// goroutines can write while the test reads it.
type evLog struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (l *evLog) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.Write(p)
}

// events parses the JSON lines logged so far.
func (l *evLog) events(t *testing.T) []map[string]any {
	t.Helper()
	l.mu.Lock()
	text := l.buf.String()
	l.mu.Unlock()
	var out []map[string]any
	for _, line := range strings.Split(text, "\n") {
		if line == "" {
			continue
		}
		var ev map[string]any
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			t.Fatalf("log line is not JSON: %q", line)
		}
		out = append(out, ev)
	}
	return out
}

// evEnv is schEnv with the API's log captured, and the store's directory,
// where a test can break one kind of write.
type evEnv struct {
	*schEnv
	dir  string
	logs *evLog
}

func evNewEnv(t *testing.T, o evOpts) *evEnv {
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
	cl := schServe(t, a)
	t.Cleanup(check.release) // runs before the server closes, so a parked check can't hold it up
	return &evEnv{schEnv: &schEnv{schClient: cl, st: st, sched: sched, check: check}, dir: dir, logs: logs}
}

// call sends a request and returns the reply with the log lines it caused.
func (e *evEnv) call(t *testing.T, method, path, body string) (schResp, []map[string]any) {
	t.Helper()
	before := len(e.logs.events(t))
	r := e.do(method, path, body)
	return r, e.logs.events(t)[before:]
}

// evRefuse makes the store's file in dir abort every statement of the given
// kinds ("INSERT", "UPDATE" or "DELETE") on a table, through triggers made on
// a second connection that is closed again. Reads and every other write still
// work, and the store, which is open, notices at its next statement.
func evRefuse(t *testing.T, dir, table string, kinds ...string) {
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
	for _, kind := range kinds {
		ddl := fmt.Sprintf("CREATE TRIGGER ev_refuse_%s_%s BEFORE %s ON %s BEGIN SELECT RAISE(ABORT, 'refused by the test'); END",
			strings.ToLower(kind), table, kind, table)
		if _, err := db.Exec(ddl); err != nil {
			t.Fatalf("%s: %v", ddl, err)
		}
	}
}

// evRow is a request and the event it must log: status is the reply's status,
// code the event's error.code (empty for a success), and id its schedule_id
// (0 for none).
type evRow struct {
	label  string
	method string
	path   string
	body   string
	status int
	code   string
	id     int64
}

func evReq(label, method, path, body string, status int, code string, id int64) evRow {
	return evRow{label, method, path, body, status, code, id}
}

// evBody is a body to send, and the error.code that refusing it logs.
type evBody struct {
	label string
	body  string
	code  string
}

// evRefusals makes a row of each body: sent to the path, it must be a 400
// whose event has the body's code and the schedule_id id.
func evRefusals(method, path string, id int64, bodies []evBody) []evRow {
	rows := make([]evRow, 0, len(bodies))
	for _, b := range bodies {
		rows = append(rows, evReq(b.label, method, path, b.body, 400, b.code, id))
	}
	return rows
}

// evPath is the path of schedule id, with a suffix such as "/run".
func evPath(id int64, suffix string) string {
	return "/api/v1/schedules/" + strconv.FormatInt(id, 10) + suffix
}

// evNamed is the events among lines that are called msg.
func evNamed(lines []map[string]any, msg string) []map[string]any {
	var out []map[string]any
	for _, ev := range lines {
		if ev["msg"] == msg {
			out = append(out, ev)
		}
	}
	return out
}

// evLevel is the level ADR-0020 gives a reply: INFO for a success, ERROR for
// a 500, and WARN for every other failure, a 503 included.
func evLevel(status int) string {
	switch {
	case status < 400:
		return "INFO"
	case status == 500:
		return "ERROR"
	default:
		return "WARN"
	}
}

// evWant is what one outcome event must say: its name, the status of the
// reply (which is also its status_code), its error.code (empty for a success)
// and its schedule_id (0 for none). with names the other events the request
// may log beside it, each once: a keep_days PATCH that deletes rows logs a
// results.prune.
type evWant struct {
	msg    string
	status int
	code   string
	id     int64
	with   []string
}

// evNames is the names of lines, sorted.
func evNames(lines []map[string]any) []string {
	names := make([]string, 0, len(lines))
	for _, ev := range lines {
		names = append(names, fmt.Sprint(ev["msg"]))
	}
	slices.Sort(names)
	return names
}

// evCheck wants lines, what one request logged, to be exactly one event named
// w.msg, and the events of w.with, and nothing else; and the w.msg event to say
// what w says. A route that checks a far end (add, or an update of far_end)
// logs no schedule.check beside its own event. It returns the w.msg event, or
// nil when there wasn't exactly one.
func evCheck(t *testing.T, label string, lines []map[string]any, w evWant) map[string]any {
	t.Helper()
	if want := slices.Sorted(slices.Values(append([]string{w.msg}, w.with...))); !slices.Equal(evNames(lines), want) {
		t.Errorf("%s: the request logged the events %v, want exactly %v; it logged %v", label, evNames(lines), want, lines)
	}
	got := evNamed(lines, w.msg)
	if len(got) != 1 {
		return nil
	}
	ev := got[0]
	if want := evLevel(w.status); ev["level"] != want {
		t.Errorf("%s: level %v, want %s for a %d", label, ev["level"], want, w.status)
	}
	result := "succeeded"
	if w.status >= 400 {
		result = "failed"
	}
	if ev["result"] != result {
		t.Errorf("%s: result %v, want %s", label, ev["result"], result)
	}
	if ev["status_code"] != float64(w.status) {
		t.Errorf("%s: status_code %v, want %d", label, ev["status_code"], w.status)
	}
	if d, ok := ev["duration_ms"].(float64); !ok || d < 0 {
		t.Errorf("%s: duration_ms = %v, want a number of milliseconds", label, ev["duration_ms"])
	}
	code, hasCode := ev["error.code"]
	text, hasText := ev["error.message"]
	if w.status < 400 {
		if hasCode || hasText {
			t.Errorf("%s: a success carries error.code %v and error.message %v", label, code, text)
		}
	} else {
		if code != w.code {
			t.Errorf("%s: error.code %v, want %s", label, code, w.code)
		}
		if s, ok := text.(string); !ok || s == "" {
			t.Errorf("%s: error.message %v, want the error's text", label, text)
		}
	}
	if w.id > 0 {
		if ev["schedule_id"] != float64(w.id) {
			t.Errorf("%s: schedule_id %v, want %d", label, ev["schedule_id"], w.id)
		}
	} else if _, has := ev["schedule_id"]; has {
		t.Errorf("%s: schedule_id %v, want none: the path holds no schedule id", label, ev["schedule_id"])
	}
	if _, has := ev["name"]; has && w.status < 400 && strings.HasPrefix(w.msg, "schedule.") {
		t.Errorf("%s: the event carries name, which is free text", label)
	}
	for k, v := range ev {
		switch v.(type) {
		case map[string]any, []any:
			t.Errorf("%s: %s is nested: %v (ADR-0020: primitives only)", label, k, v)
		}
	}
	return ev
}

// evRun sends each row, wants the event named msg for it, and returns those
// events in order (nil where the row went wrong).
func evRun(t *testing.T, env *evEnv, msg string, rows []evRow) []map[string]any {
	t.Helper()
	evs := make([]map[string]any, len(rows))
	for i, c := range rows {
		r, lines := env.call(t, c.method, c.path, c.body)
		if r.Status != c.status {
			t.Errorf("%s: status %d, want %d: %s", c.label, r.Status, c.status, r.Body)
			continue
		}
		evs[i] = evCheck(t, c.label, lines, evWant{msg: msg, status: c.status, code: c.code, id: c.id})
	}
	return evs
}

// evQuiet sends each row and wants its status, and no log line at all.
func evQuiet(t *testing.T, env *evEnv, rows []evRow) {
	t.Helper()
	for _, c := range rows {
		r, lines := env.call(t, c.method, c.path, c.body)
		if r.Status != c.status {
			t.Errorf("%s: status %d, want %d: %s", c.label, r.Status, c.status, r.Body)
			continue
		}
		if len(lines) != 0 {
			t.Errorf("%s: a read that went as planned logged %v", c.label, lines)
		}
	}
}

// evNonPositive sends a request for schedule 0 and for schedule -1, which
// aren't schedule ids. ADR-0020: an id of 0 or below is not_found, like any id
// with no schedule, so each is a 404 not_found and its event has no schedule_id.
func evNonPositive(t *testing.T, env *evEnv, msg, method, suffix, body string) {
	t.Helper()
	for _, id := range []string{"0", "-1"} {
		label := method + " schedule " + id + suffix
		r, lines := env.call(t, method, "/api/v1/schedules/"+id+suffix, body)
		if r.Status != 404 {
			t.Errorf("%s: status %d, want 404 not_found: %s", label, r.Status, r.Body)
			continue
		}
		evCheck(t, label, lines, evWant{msg: msg, status: 404, code: "not_found"})
	}
}

// evCheckSaved wants a schedule.add or schedule.update event to carry the
// schedule as the reply shows it saved, whatever the request sent.
func evCheckSaved(t *testing.T, label string, ev map[string]any, saved schJSON) {
	t.Helper()
	if ev == nil {
		return
	}
	for k, want := range map[string]any{
		"far_end":        saved.FarEnd,
		"kind":           saved.Kind,
		"every_minutes":  float64(saved.EveryMinutes),
		"offset_minutes": float64(saved.OffsetMinutes),
		"paused":         saved.Paused,
	} {
		if ev[k] != want {
			t.Errorf("%s: %s = %v (%T), want %v", label, k, ev[k], ev[k], want)
		}
	}
}

// evNoText wants no string in the event to hold text, free text the event is
// meant to leave out. An empty text asks for nothing.
func evNoText(t *testing.T, label string, ev map[string]any, text string) {
	t.Helper()
	if text == "" {
		return
	}
	for k, v := range ev {
		if s, ok := v.(string); ok && strings.Contains(s, text) {
			t.Errorf("%s: %s = %q holds %q, which is free text", label, k, s, text)
		}
	}
}

// evMessageHolds wants the event's error.message, the Go error's text, to hold
// text.
func evMessageHolds(t *testing.T, label string, ev map[string]any, text string) {
	t.Helper()
	if ev == nil {
		return
	}
	if got, _ := ev["error.message"].(string); !strings.Contains(got, text) {
		t.Errorf("%s: error.message %q does not hold %q", label, got, text)
	}
}

// evCheckSent wants a settings.patch event to carry each setting in sent, by
// its JSON name with the value sent, and none of the others.
func evCheckSent(t *testing.T, label string, ev map[string]any, sent map[string]float64) {
	t.Helper()
	if ev == nil {
		return
	}
	for _, name := range []string{"plan_down_mbps", "plan_up_mbps", "keep_days"} {
		want, was := sent[name]
		got, has := ev[name]
		switch {
		case was && got != want:
			t.Errorf("%s: %s = %v, want %v, the value sent", label, name, got, want)
		case !was && has:
			t.Errorf("%s: %s = %v, but the body did not send it", label, name, got)
		}
	}
}

// evCheckPruneFailed wants a results.prune event for a prune the database
// refused: at ERROR, failed, with the given error.code and the error's text,
// which holds text.
func evCheckPruneFailed(t *testing.T, label string, ev map[string]any, code, text string) {
	t.Helper()
	if ev["msg"] != "results.prune" {
		t.Errorf("%s: msg = %v, want results.prune", label, ev["msg"])
	}
	if ev["level"] != "ERROR" {
		t.Errorf("%s: level = %v, want ERROR: the database refused it", label, ev["level"])
	}
	if ev["result"] != "failed" {
		t.Errorf("%s: result = %v, want failed", label, ev["result"])
	}
	if ev["error.code"] != code {
		t.Errorf("%s: error.code = %v, want %s", label, ev["error.code"], code)
	}
	if got, _ := ev["error.message"].(string); got == "" || !strings.Contains(got, text) {
		t.Errorf("%s: error.message = %q, want the error's text, holding %q", label, got, text)
	}
	for k, v := range ev {
		switch v.(type) {
		case map[string]any, []any:
			t.Errorf("%s: %s is nested: %v (ADR-0020: primitives only)", label, k, v)
		}
	}
}

// evHoldFourChecks starts four checks that don't answer until the returned
// func is called, which lets them finish and wants them to have answered 200.
// Four are all the server runs at once.
func evHoldFourChecks(t *testing.T, env *evEnv) (release func()) {
	t.Helper()
	env.check.hold()
	type outcome struct {
		resp schResp
		err  error
	}
	done := make(chan outcome, 4)
	for i := range 4 {
		go func() {
			r, err := env.try("POST", "/api/v1/schedules/check", fmt.Sprintf(`{"far_end":"host%d"}`, i))
			done <- outcome{r, err}
		}()
	}
	env.check.waitEntered(t, 4)
	return func() {
		env.check.release()
		for range 4 {
			select {
			case o := <-done:
				if o.err != nil {
					t.Errorf("a held check failed: %v", o.err)
				} else if o.resp.Status != 200 {
					t.Errorf("a held check finished with %d: %s", o.resp.Status, o.resp.Body)
				}
			case <-time.After(10 * time.Second):
				t.Error("a held check never finished")
				return
			}
		}
	}
}

func TestC37_AddLogsOneSucceededEventWithTheSavedValues(t *testing.T) {
	defer schNoPanic(t)
	env := evNewEnv(t, evOpts{})
	for _, c := range []struct{ label, body, secret string }{
		{"defaults", `{"far_end":"tailmeadow"}`, ""},
		{"every field", `{"far_end":"HTTPS://Dyno.Example:8443","name":"  Gran's  ","kind":"full","every_minutes":60,"offset_minutes":30,"paused":true}`, "Gran's"},
		{"a name, and zeros sent as zeros", `{"far_end":"Gran-House:8080","name":"Upstairs router","every_minutes":15,"offset_minutes":0,"paused":false}`, "Upstairs"},
	} {
		r, lines := env.call(t, "POST", "/api/v1/schedules", c.body)
		r.wantStatus(t, 201)
		saved := r.asSchedule(t)
		ev := evCheck(t, c.label, lines, evWant{msg: "schedule.add", status: 201, id: saved.ID})
		evCheckSaved(t, c.label, ev, saved)
		evNoText(t, c.label, ev, c.secret)
	}
}

func TestC37_AddBadInputLogsOneWarnEvent(t *testing.T) {
	defer schNoPanic(t)
	env := evNewEnv(t, evOpts{})
	evRun(t, env, "schedule.add", evRefusals("POST", "/api/v1/schedules", 0, []evBody{
		{"malformed JSON", `{"far_end": `, "invalid_json"},
		{"not JSON at all", `tailmeadow`, "invalid_json"},
		{"an empty body", ``, "invalid_json"},
		{"not an object", `["tailmeadow"]`, "invalid_json"},
		{"two objects", `{"far_end":"tailmeadow"}{"far_end":"tailmeadow"}`, "invalid_json"},
		{"an unknown field", `{"far_end":"tailmeadow","colour":"red"}`, "invalid_json"},
		{"every_minutes as a string", `{"far_end":"tailmeadow","every_minutes":"60"}`, "invalid_json"},
		{"paused as a string", `{"far_end":"tailmeadow","paused":"yes"}`, "invalid_json"},
		{"name as a number", `{"far_end":"tailmeadow","name":5}`, "invalid_json"},
		{"a body over 4 KiB", `{"far_end":"tailmeadow","name":"` + strings.Repeat("a", 5000) + `"}`, "body_too_large"},
		{"4 KiB and a byte", histPad(`{"far_end":"tailmeadow"}`, 4097), "body_too_large"},
		{"every 45", `{"far_end":"tailmeadow","every_minutes":45}`, "invalid_schedule"},
		{"offset equal to every", `{"far_end":"tailmeadow","every_minutes":60,"offset_minutes":60}`, "invalid_schedule"},
		{"a negative offset", `{"far_end":"tailmeadow","offset_minutes":-1}`, "invalid_schedule"},
		{"kind medium", `{"far_end":"tailmeadow","kind":"medium"}`, "invalid_schedule"},
		{"a 65-byte name", `{"far_end":"tailmeadow","name":"` + strings.Repeat("a", 65) + `"}`, "invalid_schedule"},
		{"a name with a line break", `{"far_end":"tailmeadow","name":"a\nb"}`, "invalid_schedule"},
		{"far end ftp://x", `{"far_end":"ftp://x"}`, "invalid_schedule"},
		{"no far_end", `{"name":"Gran's"}`, "invalid_schedule"},
		{"an empty far_end", `{"far_end":""}`, "invalid_schedule"},
	}))
}

func TestC37_AddAtTheLimitLogsScheduleLimit(t *testing.T) {
	defer schNoPanic(t)
	post := func(label string) evRow {
		return evReq(label, "POST", "/api/v1/schedules", `{"far_end":"tailmeadow"}`, 409, "schedule_limit", 0)
	}

	// Sixteen exist before the request, so it is refused before any check.
	full := evNewEnv(t, evOpts{})
	for i := range schedule.MaxSchedules {
		schSeed(t, full.st, fmt.Sprintf("host%d", i))
	}
	evRun(t, full, "schedule.add", []evRow{post("the 17th schedule")})

	// The last place is taken while the check runs, so the store refuses the insert.
	racing := evNewEnv(t, evOpts{})
	for i := range schedule.MaxSchedules - 1 {
		schSeed(t, racing.st, fmt.Sprintf("host%d", i))
	}
	taken := make(chan error, 1)
	racing.check.setHook(func(string) {
		_, err := racing.st.AddSchedule(schedule.Schedule{
			Name: "racer", FarEnd: "http://racer:3966", Kind: schedule.Light, EveryMinutes: 180,
		})
		taken <- err
	})
	evRun(t, racing, "schedule.add", []evRow{post("the insert finds it full")})
	select {
	case err := <-taken:
		if err != nil {
			t.Fatalf("the racing add failed: %v", err)
		}
	default:
		t.Error("Check was never called, so the insert was not what refused the request")
	}
}

func TestC37_AddWithAFailingCheckLogsTheChecksCode(t *testing.T) {
	defer schNoPanic(t)
	env := evNewEnv(t, evOpts{})
	post := func(label string, status int, code string) map[string]any {
		return evRun(t, env, "schedule.add", []evRow{
			evReq(label, "POST", "/api/v1/schedules", `{"far_end":"tailmeadow"}`, status, code, 0),
		})[0]
	}
	for _, code := range []string{nodetest.CodeUnreachable, nodetest.CodeTimeout, nodetest.CodeNotDyno} {
		cerr := &nodetest.Error{Code: code, Err: errors.New("the far end said " + code)}
		env.check.setErr(cerr)
		evMessageHolds(t, code, post(code, 422, code), schErrText(t, cerr))
	}

	// An error that isn't the far end's is this server's: a 500.
	env.check.setErr(errors.New("boom"))
	evMessageHolds(t, "a check that failed here", post("a check that failed here", 500, "check_failed"), "boom")
}

func TestC37_AddThatCannotGetACheckLogsOneWarnEvent(t *testing.T) {
	defer schNoPanic(t)
	post := func(label, code string) evRow {
		return evReq(label, "POST", "/api/v1/schedules", `{"far_end":"tailmeadow"}`, 503, code, 0)
	}

	off := evNewEnv(t, evOpts{noCheck: true})
	evRun(t, off, "schedule.add", []evRow{post("no Check function", "checks_unavailable")})

	env := evNewEnv(t, evOpts{})
	release := evHoldFourChecks(t, env)
	defer release()
	evRun(t, env, "schedule.add", []evRow{post("four checks running", "checks_busy")})
}

func TestC37_AddOnAFailingDatabaseLogsOneErrorEvent(t *testing.T) {
	defer schNoPanic(t)
	post := func(label, code string) evRow {
		return evReq(label, "POST", "/api/v1/schedules", `{"far_end":"tailmeadow"}`, 500, code, 0)
	}

	// A closed store refuses the read that comes first, the count behind the limit.
	closed := evNewEnv(t, evOpts{})
	closed.st.Close()
	evRun(t, closed, "schedule.add", []evRow{post("the store is closed", "store_read_failed")})

	// This one reads and checks, and refuses the insert.
	refused := evNewEnv(t, evOpts{})
	evRefuse(t, refused.dir, "schedules", "INSERT")
	evs := evRun(t, refused, "schedule.add", []evRow{post("the insert is refused", "store_write_failed")})
	evMessageHolds(t, "the insert is refused", evs[0], "refused by the test")
}

func TestC37_UpdateLogsOneSucceededEventWithTheSavedValues(t *testing.T) {
	defer schNoPanic(t)
	env := evNewEnv(t, evOpts{})
	sc := env.add(t, `{"far_end":"tailmeadow","name":"Gran's","every_minutes":180,"offset_minutes":5}`)
	for _, c := range []struct{ label, body, secret string }{
		{"pausing", `{"paused":true}`, ""},
		{"resuming", `{"paused":false}`, ""},
		{"retiming", `{"every_minutes":60,"offset_minutes":30}`, ""},
		{"a full test", `{"kind":"full"}`, ""},
		{"a new far end", `{"far_end":"Gran-House:8080"}`, ""},
		{"renaming", `{"name":"Upstairs router"}`, "Upstairs"},
		{"the default name", `{"name":""}`, ""},
		{"every field", `{"name":"Hall","far_end":"https://Dyno.Example/","kind":"light","every_minutes":15,"offset_minutes":3,"paused":true}`, "Hall"},
	} {
		r, lines := env.call(t, "PATCH", evPath(sc.ID, ""), c.body)
		r.wantStatus(t, 200)
		saved := r.asSchedule(t)
		ev := evCheck(t, c.label, lines, evWant{msg: "schedule.update", status: 200, id: sc.ID})
		evCheckSaved(t, c.label, ev, saved)
		evNoText(t, c.label, ev, c.secret)
	}
}

func TestC37_UpdateBadInputLogsOneWarnEventWithTheID(t *testing.T) {
	defer schNoPanic(t)
	env := evNewEnv(t, evOpts{})
	sc := env.add(t, `{"far_end":"tailmeadow","every_minutes":60,"offset_minutes":30}`)
	rows := evRefusals("PATCH", evPath(sc.ID, ""), sc.ID, []evBody{
		{"malformed JSON", `{"paused":`, "invalid_json"},
		{"two objects", `{"paused":true}{"paused":false}`, "invalid_json"},
		{"an empty body", ``, "invalid_json"},
		{"an unknown field", `{"colour":"red"}`, "invalid_json"},
		{"every_minutes as a string", `{"every_minutes":"60"}`, "invalid_json"},
		{"paused as a string", `{"paused":"yes"}`, "invalid_json"},
		{"a body over 4 KiB", `{"name":"` + strings.Repeat("a", 5000) + `"}`, "body_too_large"},
		{"4 KiB and a byte", histPad(`{"paused":true}`, 4097), "body_too_large"},
		{"an offset past the new interval", `{"every_minutes":15}`, "invalid_schedule"},
		{"an offset equal to every", `{"offset_minutes":60}`, "invalid_schedule"},
		{"a negative offset", `{"offset_minutes":-1}`, "invalid_schedule"},
		{"every 45", `{"every_minutes":45}`, "invalid_schedule"},
		{"kind medium", `{"kind":"medium"}`, "invalid_schedule"},
		{"a 65-byte name", `{"name":"` + strings.Repeat("a", 65) + `"}`, "invalid_schedule"},
		{"a bad far end", `{"far_end":"ftp://x"}`, "invalid_schedule"},
	})
	rows = append(rows,
		evReq("an id never added", "PATCH", evPath(999, ""), `{"paused":true}`, 404, "not_found", 999),
		evReq("an id that isn't a number", "PATCH", "/api/v1/schedules/abc", `{"paused":true}`, 400, "invalid_id", 0),
		evReq("an id with a fraction", "PATCH", "/api/v1/schedules/1.5", `{"paused":true}`, 400, "invalid_id", 0),
	)
	evRun(t, env, "schedule.update", rows)
	evNonPositive(t, env, "schedule.update", "PATCH", "", `{"paused":true}`)
}

func TestC37_UpdateWithAFailingCheckLogsTheChecksCode(t *testing.T) {
	defer schNoPanic(t)
	env := evNewEnv(t, evOpts{})
	sc := env.add(t, `{"far_end":"tailmeadow"}`)
	patch := func(label string, status int, code string) map[string]any {
		return evRun(t, env, "schedule.update", []evRow{
			evReq(label, "PATCH", evPath(sc.ID, ""), `{"far_end":"elsewhere"}`, status, code, sc.ID),
		})[0]
	}
	for _, code := range []string{nodetest.CodeUnreachable, nodetest.CodeTimeout, nodetest.CodeNotDyno} {
		cerr := &nodetest.Error{Code: code, Err: errors.New("the far end said " + code)}
		env.check.setErr(cerr)
		evMessageHolds(t, code, patch(code, 422, code), schErrText(t, cerr))
	}
	env.check.setErr(errors.New("boom"))
	evMessageHolds(t, "a check that failed here", patch("a check that failed here", 500, "check_failed"), "boom")
}

func TestC37_UpdateThatCannotGetACheckLogsOneWarnEvent(t *testing.T) {
	defer schNoPanic(t)

	// With no Check function a new far end can't be checked, but a pause needs no check.
	off := evNewEnv(t, evOpts{noCheck: true})
	quiet := schSeed(t, off.st, "tailmeadow")
	evRun(t, off, "schedule.update", []evRow{
		evReq("a new far end, no Check function", "PATCH", evPath(quiet.ID, ""), `{"far_end":"elsewhere"}`, 503, "checks_unavailable", quiet.ID),
	})
	r, lines := off.call(t, "PATCH", evPath(quiet.ID, ""), `{"paused":true}`)
	r.wantStatus(t, 200)
	evCheckSaved(t, "pausing, no Check function", evCheck(t, "pausing, no Check function", lines,
		evWant{msg: "schedule.update", status: 200, id: quiet.ID}), r.asSchedule(t))

	// With four checks running a new far end is refused, and a pause still isn't.
	env := evNewEnv(t, evOpts{})
	busy := schSeed(t, env.st, "tailmeadow")
	release := evHoldFourChecks(t, env)
	defer release()
	evRun(t, env, "schedule.update", []evRow{
		evReq("a new far end, four checks running", "PATCH", evPath(busy.ID, ""), `{"far_end":"elsewhere"}`, 503, "checks_busy", busy.ID),
	})
	r, lines = env.call(t, "PATCH", evPath(busy.ID, ""), `{"paused":true}`)
	r.wantStatus(t, 200)
	evCheckSaved(t, "pausing, four checks running", evCheck(t, "pausing, four checks running", lines,
		evWant{msg: "schedule.update", status: 200, id: busy.ID}), r.asSchedule(t))
}

func TestC37_UpdateOnAFailingDatabaseLogsOneErrorEvent(t *testing.T) {
	defer schNoPanic(t)
	patch := func(sc schJSON, label, code string) evRow {
		return evReq(label, "PATCH", evPath(sc.ID, ""), `{"paused":true}`, 500, code, sc.ID)
	}

	// A closed store refuses the read that comes first, the schedule itself.
	closed := evNewEnv(t, evOpts{})
	sc := closed.add(t, `{"far_end":"tailmeadow"}`)
	closed.st.Close()
	evRun(t, closed, "schedule.update", []evRow{patch(sc, "the store is closed", "store_read_failed")})

	// This one reads the schedule and refuses the update.
	refused := evNewEnv(t, evOpts{})
	sc = refused.add(t, `{"far_end":"tailmeadow"}`)
	evRefuse(t, refused.dir, "schedules", "UPDATE")
	evs := evRun(t, refused, "schedule.update", []evRow{patch(sc, "the update is refused", "store_write_failed")})
	evMessageHolds(t, "the update is refused", evs[0], "refused by the test")
}

func TestC37_DeleteLogsOneEventOnEveryPath(t *testing.T) {
	defer schNoPanic(t)
	env := evNewEnv(t, evOpts{})
	sc := env.add(t, `{"far_end":"tailmeadow"}`)
	evRun(t, env, "schedule.delete", []evRow{
		evReq("a schedule", "DELETE", evPath(sc.ID, ""), "", 204, "", sc.ID),
		evReq("the same schedule again", "DELETE", evPath(sc.ID, ""), "", 404, "not_found", sc.ID),
		evReq("an id never added", "DELETE", evPath(999, ""), "", 404, "not_found", 999),
		evReq("an id that isn't a number", "DELETE", "/api/v1/schedules/abc", "", 400, "invalid_id", 0),
		evReq("an id with a fraction", "DELETE", "/api/v1/schedules/1.5", "", 400, "invalid_id", 0),
	})
	evNonPositive(t, env, "schedule.delete", "DELETE", "", "")

	// A store that reads and refuses the delete.
	refused := evNewEnv(t, evOpts{})
	kept := refused.add(t, `{"far_end":"tailmeadow"}`)
	evRefuse(t, refused.dir, "schedules", "DELETE")
	evs := evRun(t, refused, "schedule.delete", []evRow{
		evReq("the delete is refused", "DELETE", evPath(kept.ID, ""), "", 500, "store_write_failed", kept.ID),
	})
	evMessageHolds(t, "the delete is refused", evs[0], "refused by the test")

	// A closed store: whether the route reads the schedule before it deletes
	// it is its own business, so either code will do.
	closed := evNewEnv(t, evOpts{})
	gone := closed.add(t, `{"far_end":"tailmeadow"}`)
	closed.st.Close()
	r, lines := closed.call(t, "DELETE", evPath(gone.ID, ""), "")
	r.wantStatus(t, 500)
	code := "store_write_failed"
	if got := evNamed(lines, "schedule.delete"); len(got) == 1 && got[0]["error.code"] == "store_read_failed" {
		code = "store_read_failed"
	}
	evCheck(t, "the store is closed", lines, evWant{msg: "schedule.delete", status: 500, code: code, id: gone.ID})
}

func TestC37_RunNowLogsOneEventOnEveryPath(t *testing.T) {
	defer schNoPanic(t)
	env := evNewEnv(t, evOpts{})
	sc := env.add(t, `{"far_end":"tailmeadow"}`)
	paused := env.add(t, `{"far_end":"gran-house","paused":true}`)
	soon := env.add(t, `{"far_end":"office"}`)
	lost := env.add(t, `{"far_end":"annex"}`)
	schStoreRun(t, env.st, soon.ID, soon.FarEnd, time.Now().Add(-20*time.Second), nil)
	env.sched.markUnknown(lost.ID)
	evRun(t, env, "schedule.run_now", []evRow{
		evReq("a schedule", "POST", evPath(sc.ID, "/run"), "", 202, "", sc.ID),
		evReq("a paused schedule", "POST", evPath(paused.ID, "/run"), "", 202, "", paused.ID),
		evReq("20 seconds after its last result", "POST", evPath(soon.ID, "/run"), "", 429, "too_soon", soon.ID),
		evReq("an id never added", "POST", evPath(999, "/run"), "", 404, "not_found", 999),
		evReq("an id the scheduler doesn't know", "POST", evPath(lost.ID, "/run"), "", 404, "not_found", lost.ID),
		evReq("an id that isn't a number", "POST", "/api/v1/schedules/abc/run", "", 400, "invalid_id", 0),
		evReq("an id with a fraction", "POST", "/api/v1/schedules/1.5/run", "", 400, "invalid_id", 0),
	})
	evNonPositive(t, env, "schedule.run_now", "POST", "/run", "")

	// A server that isn't running scheduled tests.
	off := evNewEnv(t, evOpts{noScheduler: true})
	idle := off.add(t, `{"far_end":"tailmeadow"}`)
	evRun(t, off, "schedule.run_now", []evRow{
		evReq("no Scheduler", "POST", evPath(idle.ID, "/run"), "", 503, "scheduler_off", idle.ID),
	})

	// A scheduler that fails in a way that isn't an unknown schedule.
	broken := evNewEnv(t, evOpts{runErr: errors.New("the scheduler broke")})
	stuck := broken.add(t, `{"far_end":"tailmeadow"}`)
	evs := evRun(t, broken, "schedule.run_now", []evRow{
		evReq("RunNow fails", "POST", evPath(stuck.ID, "/run"), "", 500, "run_failed", stuck.ID),
	})
	evMessageHolds(t, "RunNow fails", evs[0], "the scheduler broke")

	// A closed store refuses the read that comes first, the schedule itself.
	closed := evNewEnv(t, evOpts{})
	gone := closed.add(t, `{"far_end":"tailmeadow"}`)
	closed.st.Close()
	evRun(t, closed, "schedule.run_now", []evRow{
		evReq("the store is closed", "POST", evPath(gone.ID, "/run"), "", 500, "store_read_failed", gone.ID),
	})
}

func TestC37_ReadsLogNothingUnlessTheServerFails(t *testing.T) {
	defer schNoPanic(t)
	env := evNewEnv(t, evOpts{})
	sc := env.add(t, `{"far_end":"tailmeadow"}`)
	evQuiet(t, env, []evRow{
		evReq("the list", "GET", "/api/v1/schedules", "", 200, "", 0),
		evReq("one schedule", "GET", evPath(sc.ID, ""), "", 200, "", 0),
		evReq("an id never added", "GET", evPath(999, ""), "", 404, "", 0),
		evReq("id 0", "GET", evPath(0, ""), "", 404, "", 0),
		evReq("id -1", "GET", evPath(-1, ""), "", 404, "", 0),
		evReq("an id that isn't a number", "GET", "/api/v1/schedules/abc", "", 400, "", 0),
		evReq("a check that answers", "POST", "/api/v1/schedules/check", `{"far_end":"tailmeadow"}`, 200, "", 0),
		evReq("a check of a bad address", "POST", "/api/v1/schedules/check", `{"far_end":"ftp://x"}`, 400, "", 0),
		evReq("a check with a malformed body", "POST", "/api/v1/schedules/check", `{"far_end"`, 400, "", 0),
	})
	env.check.setErr(&nodetest.Error{Code: nodetest.CodeUnreachable, Err: errors.New("no route to host")})
	evQuiet(t, env, []evRow{
		evReq("a check that finds the far end down", "POST", "/api/v1/schedules/check", `{"far_end":"tailmeadow"}`, 422, "", 0),
	})

	// A check this server can't run is a 503, and a 503 on a read isn't logged
	// either: only a 500 is.
	const checkBody = `{"far_end":"tailmeadow"}`
	off := evNewEnv(t, evOpts{noCheck: true})
	evQuiet(t, off, []evRow{
		evReq("no Check function (checks_unavailable)", "POST", "/api/v1/schedules/check", checkBody, 503, "", 0),
	})
	busy := evNewEnv(t, evOpts{})
	release := evHoldFourChecks(t, busy)
	defer release()
	evQuiet(t, busy, []evRow{
		evReq("four checks running (checks_busy)", "POST", "/api/v1/schedules/check", checkBody, 503, "", 0),
	})
	stopping := evNewEnv(t, evOpts{})
	stopping.check.setErr(&nodetest.Error{Code: nodetest.CodeCanceled, Err: errors.New("canceled")})
	evQuiet(t, stopping, []evRow{
		evReq("a check cut off by shutdown (shutting_down)", "POST", "/api/v1/schedules/check", checkBody, 503, "", 0),
	})

	// A read the server fails is the operator's to hear about, and the event
	// carries what every event does: duration_ms, and the schedule's id when the
	// path holds one.
	closed := evNewEnv(t, evOpts{})
	one := closed.add(t, `{"far_end":"tailmeadow"}`)
	closed.st.Close()
	evRun(t, closed, "schedule.list", []evRow{
		evReq("the list, the store closed", "GET", "/api/v1/schedules", "", 500, "store_read_failed", 0),
	})
	evRun(t, closed, "schedule.get", []evRow{
		evReq("one schedule, the store closed", "GET", evPath(one.ID, ""), "", 500, "store_read_failed", one.ID),
	})
	broken := evNewEnv(t, evOpts{})
	broken.check.setErr(errors.New("boom"))
	evRun(t, broken, "schedule.check", []evRow{
		evReq("a check that failed here", "POST", "/api/v1/schedules/check", `{"far_end":"tailmeadow"}`, 500, "check_failed", 0),
	})
}

func TestH22_PatchLogsOneSucceededEventWithTheSettingsSent(t *testing.T) {
	defer schNoPanic(t)
	env := evNewEnv(t, evOpts{})
	for _, c := range []struct {
		body string
		sent map[string]float64
	}{
		{`{"plan_down_mbps": 500}`, map[string]float64{"plan_down_mbps": 500}},
		{`{"plan_up_mbps": 50.5}`, map[string]float64{"plan_up_mbps": 50.5}},
		{`{"keep_days": 30}`, map[string]float64{"keep_days": 30}},
		{`{}`, map[string]float64{}},
		{`{"plan_down_mbps": 500}`, map[string]float64{"plan_down_mbps": 500}}, // the same value again is still sent
		{`{"plan_down_mbps": 0}`, map[string]float64{"plan_down_mbps": 0}},
		{`{"keep_days": 0, "plan_up_mbps": 100000}`, map[string]float64{"keep_days": 0, "plan_up_mbps": 100000}},
		{`{"plan_down_mbps": 100000, "plan_up_mbps": 0, "keep_days": 36500}`, map[string]float64{"plan_down_mbps": 100000, "plan_up_mbps": 0, "keep_days": 36500}},
		{`{"plan_down_mbps": 940.5, "plan_up_mbps": 40.25, "keep_days": 90}`, map[string]float64{"plan_down_mbps": 940.5, "plan_up_mbps": 40.25, "keep_days": 90}},
	} {
		r, lines := env.call(t, "PATCH", "/api/v1/settings", c.body)
		r.wantStatus(t, 200)
		ev := evCheck(t, c.body, lines, evWant{msg: "settings.patch", status: 200})
		evCheckSent(t, c.body, ev, c.sent)
	}
}

func TestH22_PatchRefusedLogsOneWarnEvent(t *testing.T) {
	defer schNoPanic(t)
	env := evNewEnv(t, evOpts{})
	evRun(t, env, "settings.patch", evRefusals("PATCH", "/api/v1/settings", 0, []evBody{
		{"malformed JSON", `{"keep_days": `, "invalid_json"},
		{"not JSON", `keep_days=7`, "invalid_json"},
		{"an empty body", ``, "invalid_json"},
		{"an array, not an object", `[]`, "invalid_json"},
		{"a string", `"x"`, "invalid_json"},
		{"a number", `1`, "invalid_json"},
		{"a boolean", `true`, "invalid_json"},
		{"null", `null`, "invalid_json"},
		{"null after white space", " \n\tnull", "invalid_json"},
		{"null and an object", `null {}`, "invalid_json"},
		{"two objects", `{"keep_days": 5}{"keep_days": 6}`, "invalid_json"},
		{"junk after the object", `{"keep_days": 5} x`, "invalid_json"},
		{"an unknown field", `{"plan_down": 5}`, "invalid_json"},
		{"a good field beside an unknown one", `{"keep_days": 5, "bogus": 1}`, "invalid_json"},
		{"keep_days as a fraction", `{"keep_days": 1.5}`, "invalid_json"},
		{"keep_days as a string", `{"keep_days": "7"}`, "invalid_json"},
		{"plan_down_mbps as a string", `{"plan_down_mbps": "500"}`, "invalid_json"},
		{"plan_up_mbps as a boolean", `{"plan_up_mbps": true}`, "invalid_json"},
		{"keep_days as an array", `{"keep_days": [7]}`, "invalid_json"},
		{"over 4 KiB, padding first", strings.Repeat(" ", 5000) + `{"keep_days": 5}`, "body_too_large"},
		{"over 4 KiB, padding after", `{"keep_days": 5}` + strings.Repeat(" ", 5000), "body_too_large"},
		{"4 KiB and one byte", histPad(`{"keep_days": 5}`, 4097), "body_too_large"},
		{"plan_down_mbps below range", `{"plan_down_mbps": -1}`, "invalid_settings"},
		{"plan_down_mbps above range", `{"plan_down_mbps": 100000.1}`, "invalid_settings"},
		{"plan_up_mbps below range", `{"plan_up_mbps": -0.5}`, "invalid_settings"},
		{"plan_up_mbps above range", `{"plan_up_mbps": 100001}`, "invalid_settings"},
		{"keep_days below range", `{"keep_days": -1}`, "invalid_settings"},
		{"keep_days above range", `{"keep_days": 36501}`, "invalid_settings"},
		{"a good field beside a bad one", `{"plan_down_mbps": 600, "keep_days": -1}`, "invalid_settings"},
	}))
}

func TestH22_PatchOnAFailingDatabaseLogsOneErrorEvent(t *testing.T) {
	defer schNoPanic(t)
	patch := func(label string) evRow {
		return evReq(label, "PATCH", "/api/v1/settings", `{"plan_down_mbps": 500}`, 500, "store_write_failed", 0)
	}

	// A store that reads and refuses the save.
	refused := evNewEnv(t, evOpts{})
	evRefuse(t, refused.dir, "settings", "INSERT", "UPDATE", "DELETE")
	evs := evRun(t, refused, "settings.patch", []evRow{patch("the save is refused")})
	evMessageHolds(t, "the save is refused", evs[0], "refused by the test")

	closed := evNewEnv(t, evOpts{})
	closed.st.Close()
	evRun(t, closed, "settings.patch", []evRow{patch("the store is closed")})
}

func TestH22_GetLogsNothingUnlessTheServerFails(t *testing.T) {
	defer schNoPanic(t)
	env := evNewEnv(t, evOpts{})
	evQuiet(t, env, []evRow{evReq("the settings of a new store", "GET", "/api/v1/settings", "", 200, "", 0)})

	closed := evNewEnv(t, evOpts{})
	closed.st.Close()
	evRun(t, closed, "settings.get", []evRow{
		evReq("the store is closed", "GET", "/api/v1/settings", "", 500, "store_read_failed", 0),
	})
}

func TestH23_PatchWhoseDeleteFailsStillAnswers200AndLogsStoreDeleteFailed(t *testing.T) {
	defer schNoPanic(t)
	env := evNewEnv(t, evOpts{})
	day := 24 * time.Hour
	now := time.Now().UTC()
	old1 := histAdd(t, env.st, histRes(now.Add(-40*day), "client", "")).ID
	old2 := histAdd(t, env.st, histSched(now.Add(-30*day), 4, true)).ID
	fresh := histAdd(t, env.st, histRes(now.Add(-time.Hour), "client", "")).ID
	evRefuse(t, env.dir, "results", "DELETE")

	r, lines := env.call(t, "PATCH", "/api/v1/settings", `{"keep_days": 7}`)
	r.wantStatus(t, 200)
	var reply settings.Settings
	if err := json.Unmarshal(r.Body, &reply); err != nil || reply != (settings.Settings{KeepDays: 7}) {
		t.Errorf("the reply is %s, want every setting with keep_days 7", r.Body)
	}
	if saved, err := env.st.Settings(); err != nil || saved.KeepDays != 7 {
		t.Errorf("the store has %+v (err %v), want keep_days 7 saved", saved, err)
	}
	ev := evCheck(t, "the patch", lines, evWant{msg: "settings.patch", status: 200, with: []string{"results.prune"}})
	evCheckSent(t, "the patch", ev, map[string]float64{"keep_days": 7})

	prunes := evNamed(lines, "results.prune")
	if len(prunes) != 1 {
		t.Fatalf("the patch logged %d results.prune events, want 1; it logged %v", len(prunes), lines)
	}
	evCheckPruneFailed(t, "the prune after the patch", prunes[0], "store_delete_failed", "refused by the test")
	for _, id := range []int64{old1, old2, fresh} {
		if _, err := env.st.Get(id); err != nil {
			t.Errorf("result %d: Get returned %v; a refused delete leaves it", id, err)
		}
	}
}

func TestH23_PatchThatDeletesLogsOneSucceededPruneBesideThePatchEvent(t *testing.T) {
	defer schNoPanic(t)
	env := evNewEnv(t, evOpts{})
	day := 24 * time.Hour
	now := time.Now().UTC()
	for _, age := range []time.Duration{40 * day, 30 * day, time.Hour} {
		histAdd(t, env.st, histRes(now.Add(-age), "client", ""))
	}

	r, lines := env.call(t, "PATCH", "/api/v1/settings", `{"keep_days": 7}`)
	r.wantStatus(t, 200)
	evCheck(t, "the patch", lines, evWant{msg: "settings.patch", status: 200, with: []string{"results.prune"}})
	prunes := evNamed(lines, "results.prune")
	if len(prunes) != 1 {
		t.Fatalf("the patch logged %d results.prune events, want 1; it logged %v", len(prunes), lines)
	}
	prune := prunes[0]
	if prune["level"] != "INFO" || prune["result"] != "succeeded" {
		t.Errorf("the prune: level %v, result %v, want INFO and succeeded", prune["level"], prune["result"])
	}
	if prune["deleted_results"] != 2.0 || prune["keep_days"] != 7.0 {
		t.Errorf("the prune: deleted_results %v and keep_days %v, want 2 and 7", prune["deleted_results"], prune["keep_days"])
	}
	if d, ok := prune["duration_ms"].(float64); !ok || d < 0 {
		t.Errorf("the prune: duration_ms = %v, want a number of milliseconds", prune["duration_ms"])
	}

	// With nothing left to delete, the same patch logs no prune.
	r, lines = env.call(t, "PATCH", "/api/v1/settings", `{"keep_days": 7}`)
	r.wantStatus(t, 200)
	evCheck(t, "the second patch", lines, evWant{msg: "settings.patch", status: 200})
	if got := evNamed(lines, "results.prune"); len(got) != 0 {
		t.Errorf("a prune that deleted nothing logged %v", got)
	}
}

func TestH23_PatchAndHourlyPruneLogTheSameKeysForTheSameFailure(t *testing.T) {
	defer schNoPanic(t)
	env := evNewEnv(t, evOpts{})
	day := 24 * time.Hour
	now := time.Now().UTC()
	histAdd(t, env.st, histRes(now.Add(-40*day), "client", ""))
	histAdd(t, env.st, histRes(now.Add(-time.Hour), "client", ""))
	evRefuse(t, env.dir, "results", "DELETE")

	_, lines := env.call(t, "PATCH", "/api/v1/settings", `{"keep_days": 7}`)
	patched := evNamed(lines, "results.prune")
	if len(patched) != 1 {
		t.Fatalf("the patch logged %d results.prune events, want 1; it logged %v", len(patched), lines)
	}

	// The hourly job over the same store, refused the same way. It prunes as
	// it starts, so its first event is the one to read.
	hourly := &evLog{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		retention.Run(ctx, env.st, slog.New(slog.NewJSONHandler(hourly, nil)))
	}()
	deadline := time.Now().Add(10 * time.Second)
	for len(hourly.events(t)) == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("retention.Run did not return once its ctx was done")
	}
	ran := hourly.events(t)
	if len(ran) != 1 {
		t.Fatalf("the hourly prune logged %d lines, want 1: %v", len(ran), ran)
	}
	if got, want := slices.Sorted(maps.Keys(ran[0])), slices.Sorted(maps.Keys(patched[0])); !slices.Equal(got, want) {
		t.Errorf("the hourly prune logs the keys %v and the one after a patch %v; want the same", got, want)
	}
	for _, k := range []string{"level", "result", "error.code"} {
		if ran[0][k] != patched[0][k] {
			t.Errorf("%s is %v after the hourly prune and %v after a patch; want the same", k, ran[0][k], patched[0][k])
		}
	}
}
