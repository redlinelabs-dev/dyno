package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/redlinelabs-dev/dyno/server/internal/result"
	"github.com/redlinelabs-dev/dyno/server/internal/settings"
	"github.com/redlinelabs-dev/dyno/server/internal/store"
	"github.com/redlinelabs-dev/dyno/server/internal/testrun"
)

// hxResult is a client result stored at the given time.
func hxResult(at time.Time) result.Result {
	r := result.Result{At: at, Source: "client"}
	r.Client = "Chrome on Linux"
	r.DownloadMbps = 100
	return r
}

// hxSeed stores n client results a second apart, with ids 1 to n.
func hxSeed(t *testing.T, st *store.Store, n int) {
	t.Helper()
	t0 := time.Date(2026, 11, 10, 0, 0, 0, 0, time.UTC)
	for i := range n {
		if _, err := st.Add(hxResult(t0.Add(time.Duration(i) * time.Second))); err != nil {
			t.Fatal(err)
		}
	}
}

// hxWantEvent wants every key of want in a log event, with its value.
func hxWantEvent(t *testing.T, ev, want map[string]any) {
	t.Helper()
	for k, v := range want {
		if ev[k] != v {
			t.Errorf("%s = %v (%T), want %v", k, ev[k], ev[k], v)
		}
	}
}

// hxCloseOnWrite is a response recorder that closes the store when the
// handler first writes, so the read of the page after the one in hand fails.
type hxCloseOnWrite struct {
	*httptest.ResponseRecorder
	st   *store.Store
	once sync.Once
}

func (c *hxCloseOnWrite) Write(p []byte) (int, error) {
	c.once.Do(func() { c.st.Close() })
	return c.ResponseRecorder.Write(p)
}

func TestHx_CSVTextCells(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{"", ""},
		{"Chrome on Linux", "Chrome on Linux"},
		{"a,b", `"a,b"`},
		{`say "hi"`, `"say ""hi"""`},
		{"two\nlines", "\"two\nlines\""},
		{"a=b+c-d@e", "a=b+c-d@e"},
		{"=1+1", "'=1+1"},
		{"+1", "'+1"},
		{"-1", "'-1"},
		{"@x", "'@x"},
		{"\tx", "'\tx"},
		{"\rx", "\"'\rx\""},
		{`=HYPERLINK("x")`, `"'=HYPERLINK(""x"")"`},
		{"a\xffb", "a\xef\xbf\xbdb"}, // an invalid byte becomes U+FFFD, as encoding/json does
	} {
		if got := string(appendCSVText(nil, c.in)); got != c.want {
			t.Errorf("appendCSVText(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// The list is still the one object it was: the stream writes the bytes that
// encoding the whole list would.
func TestHx_ListReplyIsTheWholeListEncoded(t *testing.T) {
	mux, a := newMux(t)
	hxSeed(t, a.Store, 3)
	rows, err := a.Store.List(50, 0, "")
	if err != nil {
		t.Fatal(err)
	}
	enc, err := json.Marshal(map[string]any{"results": rows})
	if err != nil {
		t.Fatal(err)
	}
	w := do(mux, "GET", "/api/v1/results", "")
	if got, want := w.Body.String(), string(enc)+"\n"; got != want {
		t.Errorf("reply\n got %s\nwant %s", got, want)
	}
	if got := w.Header().Get("Content-Type"); got != "application/json" {
		t.Errorf("Content-Type %q", got)
	}
	if got := w.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control %q", got)
	}
}

func TestHx_ListStopsWhereTheStoreFailsAndLogsIt(t *testing.T) {
	_, a := newMux(t)
	logs := logTo(a)
	hxSeed(t, a.Store, 1200)
	w := &hxCloseOnWrite{ResponseRecorder: httptest.NewRecorder(), st: a.Store}
	var got any
	func() {
		defer func() { got = recover() }()
		a.listResults(w, httptest.NewRequest("GET", "/api/v1/results?limit=2000", nil))
	}()
	if got != http.ErrAbortHandler {
		t.Fatalf("the handler ended with %v, want a panic of http.ErrAbortHandler", got)
	}

	if w.Code != http.StatusOK {
		t.Errorf("status %d, want the 200 that was sent first", w.Code)
	}
	body := w.Body.String()
	if json.Valid([]byte(body)) {
		t.Error("the reply is valid JSON, which a client would take for a shorter list")
	}
	if !strings.HasPrefix(body, `{"results":[{"id":1200,`) {
		t.Errorf("the reply starts %.40q", body)
	}
	if n := strings.Count(body, `{"id":`); n != store.PageSize {
		t.Errorf("%d rows were sent, want the first page of %d", n, store.PageSize)
	}
	hxWantEvent(t, oneEvent(t, logs), map[string]any{
		"level": "ERROR", "msg": "result.list", "result": "failed", "status_code": 200.0,
		"sent_results": float64(store.PageSize), "error.code": "store_read_failed",
	})
}

// hxClosingWriter closes the store when the handler first writes, like
// hxCloseOnWrite, on a writer a real server can use: Unwrap keeps the response
// controller working.
type hxClosingWriter struct {
	http.ResponseWriter
	st   *store.Store
	once sync.Once
}

func (c *hxClosingWriter) Write(p []byte) (int, error) {
	c.once.Do(func() { c.st.Close() })
	return c.ResponseWriter.Write(p)
}

func (c *hxClosingWriter) Unwrap() http.ResponseWriter { return c.ResponseWriter }

// A list whose store fails after rows were sent must end the transfer with an
// error: a clean end of the chunked body would read as a complete reply.
func TestHx_ListFailingMidReplyBreaksTheTransferOnTheWire(t *testing.T) {
	_, a := newMux(t)
	logs := logTo(a)
	hxSeed(t, a.Store, 1200)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		a.listResults(&hxClosingWriter{ResponseWriter: w, st: a.Store}, r)
	}))
	defer srv.Close()
	// The client's timeout covers the body too, and the read is bounded, so a
	// handler that hangs or never stops sending fails the test and doesn't stall
	// it. A timeout is not the broken transfer that is wanted.
	const bound = 32 << 20
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Get(srv.URL + "/api/v1/results?limit=2000")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, bound))
	if ne, ok := errors.AsType[net.Error](err); ok && ne.Timeout() {
		t.Fatalf("reading the body timed out after %d bytes: %v", len(body), err)
	}
	if len(body) >= bound {
		t.Fatalf("the body ran to the %d-byte bound; the list was cut off by the store, not sent whole", bound)
	}
	if err == nil {
		t.Errorf("the body ended cleanly after %d bytes; want a read error", len(body))
	}
	if json.Valid(body) {
		t.Error("the body is valid JSON, which a client would take for a shorter list")
	}
	srv.Close() // waits for the handler, so the log is complete
	hxWantEvent(t, oneEvent(t, logs), map[string]any{
		"level": "ERROR", "msg": "result.list", "result": "failed", "status_code": 200.0,
		"sent_results": float64(store.PageSize), "error.code": "store_read_failed",
	})
}

// A CSV has no end marker, so a store that fails part way ends the
// connection: a file that stopped short must not look complete.
func TestHx_ExportAbortsWhereTheStoreFails(t *testing.T) {
	_, a := newMux(t)
	logs := logTo(a)
	hxSeed(t, a.Store, 1200)
	w := &hxCloseOnWrite{ResponseRecorder: httptest.NewRecorder(), st: a.Store}
	var got any
	func() {
		defer func() { got = recover() }()
		a.exportResults(w, httptest.NewRequest("GET", "/api/v1/results.csv", nil))
	}()
	if got != http.ErrAbortHandler {
		t.Fatalf("the handler ended with %v, want a panic of http.ErrAbortHandler", got)
	}
	if !strings.HasPrefix(w.Body.String(), "id,at,source,") {
		t.Errorf("the reply starts %.40q", w.Body.String())
	}
	hxWantEvent(t, oneEvent(t, logs), map[string]any{
		"level": "ERROR", "msg": "result.export", "result": "failed", "status_code": 200.0,
		"sent_results": float64(store.PageSize), "error.code": "store_read_failed",
	})
}

// A store that fails before the first page is a plain 500.
func TestHx_FirstPageFailureIsA500(t *testing.T) {
	for _, c := range []struct{ path, event string }{
		{"/api/v1/results", "result.list"},
		{"/api/v1/results.csv", "result.export"},
	} {
		mux, a := newMux(t)
		logs := logTo(a)
		a.Store.Close()
		w := do(mux, "GET", c.path, "")
		if w.Code != http.StatusInternalServerError || !strings.HasPrefix(w.Header().Get("Content-Type"), "application/json") {
			t.Errorf("GET %s: status %d, Content-Type %q, want a JSON 500", c.path, w.Code, w.Header().Get("Content-Type"))
		}
		if !strings.Contains(w.Body.String(), `"error"`) {
			t.Errorf("GET %s: body %s", c.path, w.Body)
		}
		hxWantEvent(t, oneEvent(t, logs), map[string]any{
			"level": "ERROR", "msg": c.event, "result": "failed", "status_code": 500.0,
			"sent_results": 0.0, "error.code": "store_read_failed",
		})
	}
}

func TestHx_LimitBeyondInt64IsTheCap(t *testing.T) {
	mux, a := newMux(t)
	hxSeed(t, a.Store, 3)
	for _, c := range []struct {
		query  string
		status int
	}{
		{"limit=99999999999999999999", http.StatusOK},
		{"limit=-99999999999999999999", http.StatusBadRequest},
		{"limit=0", http.StatusBadRequest},
		{"limit=1e3", http.StatusBadRequest},
	} {
		if w := do(mux, "GET", "/api/v1/results?"+c.query, ""); w.Code != c.status {
			t.Errorf("%s: status %d, want %d (%s)", c.query, w.Code, c.status, w.Body)
		}
	}
}

// The store reads a zero time as no bound, so year 1 has to stay a time.
func TestHx_ATimeInYearOneIsNotNoBound(t *testing.T) {
	mux, a := newMux(t)
	hxSeed(t, a.Store, 3)
	for _, c := range []struct {
		query string
		want  int
	}{
		{"to=0001-01-01T00:00:00Z", 0},
		{"from=0001-01-01T00:00:00Z", 3},
	} {
		w := do(mux, "GET", "/api/v1/results?"+c.query, "")
		var page struct{ Results []result.Result }
		if err := json.Unmarshal(w.Body.Bytes(), &page); err != nil || w.Code != http.StatusOK || len(page.Results) != c.want {
			t.Errorf("%s: status %d, %d results (%v), want %d", c.query, w.Code, len(page.Results), err, c.want)
		}
	}
}

func TestHx_SettingsStoreFailureIs500AndLogged(t *testing.T) {
	for _, c := range []struct{ method, body, event, code string }{
		{"GET", "", "settings.get", "store_read_failed"},
		{"PATCH", `{"keep_days": 5}`, "settings.patch", "store_write_failed"},
	} {
		mux, a := newMux(t)
		logs := logTo(a)
		a.Store.Close()
		w := do(mux, c.method, "/api/v1/settings", c.body)
		if w.Code != http.StatusInternalServerError || !strings.HasPrefix(w.Header().Get("Content-Type"), "application/json") {
			t.Errorf("%s: status %d, Content-Type %q, want a JSON 500", c.method, w.Code, w.Header().Get("Content-Type"))
		}
		hxWantEvent(t, oneEvent(t, logs), map[string]any{
			"level": "ERROR", "msg": c.event, "result": "failed", "status_code": 500.0, "error.code": c.code,
		})
	}
}

// wlNamed is the events in a log that are called msg.
func wlNamed(t *testing.T, b *bytes.Buffer, msg string) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, line := range strings.Split(b.String(), "\n") {
		if line == "" {
			continue
		}
		var ev map[string]any
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			t.Fatalf("not JSON: %s", line)
		}
		if ev["msg"] == msg {
			out = append(out, ev)
		}
	}
	return out
}

// A PATCH that deletes results logs the prune as the hourly one does, beside
// its own settings.patch event (ADR-0020, amended 2026-11-14). One that finds
// nothing old logs only its own.
func TestHx_PatchThatDeletesLogsThePrune(t *testing.T) {
	mux, a := newMux(t)
	logs := logTo(a)
	now := time.Now().UTC()
	for _, age := range []time.Duration{48 * time.Hour, time.Hour} {
		if _, err := a.Store.Add(hxResult(now.Add(-age))); err != nil {
			t.Fatal(err)
		}
	}
	if w := do(mux, "PATCH", "/api/v1/settings", `{"keep_days": 1}`); w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body)
	}
	prunes := wlNamed(t, logs, "results.prune")
	if len(prunes) != 1 {
		t.Fatalf("the PATCH logged %d results.prune events, want 1: %s", len(prunes), logs)
	}
	hxWantEvent(t, prunes[0], map[string]any{
		"level": "INFO", "msg": "results.prune", "result": "succeeded", "deleted_results": 1.0, "keep_days": 1.0,
	})
	if patches := wlNamed(t, logs, "settings.patch"); len(patches) != 1 {
		t.Errorf("the PATCH logged %d settings.patch events, want 1: %s", len(patches), logs)
	}

	logs.Reset()
	if w := do(mux, "PATCH", "/api/v1/settings", `{"keep_days": 30}`); w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body)
	}
	if got := wlNamed(t, logs, "results.prune"); len(got) != 0 {
		t.Errorf("a PATCH that deleted nothing logged a prune: %v", got)
	}
	hxWantEvent(t, oneEvent(t, logs), map[string]any{
		"level": "INFO", "msg": "settings.patch", "result": "succeeded", "status_code": 200.0, "keep_days": 30.0,
	})
}

// H24: a detail with invalid UTF-8 inside a JSON string is refused, and
// nothing is stored. A lone \udc00 escape is ASCII and is still taken.
func TestHx_PostWithInvalidUTF8DetailIs400AndStoresNothing(t *testing.T) {
	mux, a := newMux(t)
	w := do(mux, "POST", "/api/v1/results", "{\"idle_ms\":5,\"detail\":{\"x\":\"a\xff\xfeb\"}}")
	if w.Code != 400 {
		t.Fatalf("status %d: %s", w.Code, w.Body)
	}
	var body map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("the reply isn't JSON: %v: %q", err, w.Body)
	}
	if msg, _ := body["error"].(string); msg == "" {
		t.Errorf("reply %v, want an error message", body)
	}
	rs, err := a.Store.List(10, 0, "")
	if err != nil || len(rs) != 0 {
		t.Errorf("stored %d results (err %v), want none", len(rs), err)
	}

	w = do(mux, "POST", "/api/v1/results", `{"idle_ms":5,"detail":{"x":"\udc00"}}`)
	if w.Code != 201 {
		t.Fatalf("a lone surrogate escape: status %d: %s", w.Code, w.Body)
	}
}

// H25: a body that isn't a JSON object is 400 on PATCH /settings and on POST
// /results, null included, which the decoder would otherwise take as an empty
// object. Settings stay as they were, an old result stays however keep_days
// is set, and nothing is stored.
func TestHx_BodyThatIsNotAnObjectIs400AndChangesNothing(t *testing.T) {
	mux, a := newMux(t)
	one := 1
	if _, err := a.Store.PatchSettings(settings.Patch{KeepDays: &one}); err != nil {
		t.Fatal(err)
	}
	old, err := a.Store.Add(hxResult(time.Now().UTC().Add(-48 * time.Hour)))
	if err != nil {
		t.Fatal(err)
	}
	before, err := a.Store.Settings()
	if err != nil {
		t.Fatal(err)
	}
	logs := logTo(a)

	for _, body := range []string{`null`, `[]`, `"x"`, `1`, `true`, " \n\tnull", `null {}`} {
		for _, c := range []struct{ method, path, want, event string }{
			{"PATCH", "/api/v1/settings", "body must be one JSON object", "settings.patch"},
			{"POST", "/api/v1/results", "body must be one JSON object", "result.store"},
		} {
			logs.Reset()
			w := do(mux, c.method, c.path, body)
			if w.Code != http.StatusBadRequest {
				t.Errorf("%s %s with %q: status %d, want 400: %s", c.method, c.path, body, w.Code, w.Body)
				continue
			}
			var reply struct{ Error string }
			if err := json.Unmarshal(w.Body.Bytes(), &reply); err != nil || reply.Error != c.want {
				t.Errorf("%s %s with %q: reply %q (%v), want error %q", c.method, c.path, body, w.Body, err, c.want)
			}
			// Exactly one event, the route's own, a WARN for the client's bad input.
			ev := oneEvent(t, logs)
			hxWantEvent(t, ev, map[string]any{
				"level": "WARN", "msg": c.event, "result": "failed", "status_code": 400.0, "error.code": "invalid_json",
			})
			if msg, _ := ev["error.message"].(string); msg == "" {
				t.Errorf("%s %s with %q: the event has no error.message: %v", c.method, c.path, body, ev)
			}
			if d, ok := ev["duration_ms"].(float64); !ok || d < 0 {
				t.Errorf("%s %s with %q: duration_ms = %v, want a number of milliseconds", c.method, c.path, body, ev["duration_ms"])
			}
		}
	}

	after, err := a.Store.Settings()
	if err != nil || after != before {
		t.Errorf("settings are %+v (err %v) after the refused PATCHes, want %+v", after, err, before)
	}
	rs, err := a.Store.List(10, 0, "")
	if err != nil || len(rs) != 1 || rs[0].ID != old.ID {
		t.Errorf("the store holds %d results (err %v), want only the old one, not pruned and with nothing added", len(rs), err)
	}
	if got := wlNamed(t, logs, "results.prune"); len(got) != 0 {
		t.Errorf("a refused PATCH logged a prune: %v", got)
	}

	// An empty object is still a patch that changes nothing.
	if w := do(mux, "PATCH", "/api/v1/settings", `{}`); w.Code != http.StatusOK {
		t.Errorf("PATCH {}: status %d, want 200: %s", w.Code, w.Body)
	}
}

// H26: a plan speed of -0, in any spelling, is accepted as 0 and read back as
// 0, never -0: in the PATCH reply, in GET, and in GET after the store is
// closed and opened again.
func TestHx_NegativeZeroPlanSpeedIsReadBackAsZero(t *testing.T) {
	serve := func(st *store.Store) *http.ServeMux {
		a := &API{Version: "1.2.3", Store: st, Runs: testrun.NewRegistry(), Log: slog.New(slog.DiscardHandler)}
		mux := http.NewServeMux()
		a.Register(mux)
		return mux
	}
	for _, field := range []string{"plan_down_mbps", "plan_up_mbps"} {
		for _, num := range []string{"-0", "-0.0", "-0e5"} {
			dir := t.TempDir()
			st, err := store.Open(dir)
			if err != nil {
				t.Fatal(err)
			}
			mux := serve(st)
			w := do(mux, "PATCH", "/api/v1/settings", `{"`+field+`":`+num+`}`)
			if w.Code != http.StatusOK {
				t.Errorf("PATCH %s %s: status %d, want 200: %s", field, num, w.Code, w.Body)
			}
			if strings.Contains(w.Body.String(), "-0") {
				t.Errorf("PATCH %s %s: reply %s has a -0", field, num, w.Body)
			}
			check := func(when string, mux *http.ServeMux) {
				t.Helper()
				w := do(mux, "GET", "/api/v1/settings", "")
				if w.Code != http.StatusOK || strings.Contains(w.Body.String(), "-0") {
					t.Errorf("%s %s %s: GET gave status %d, %s; want 200 and no -0", field, num, when, w.Code, w.Body)
				}
				var got map[string]any
				if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil || got[field] != 0.0 {
					t.Errorf("%s %s %s: %s = %v (err %v), want 0", field, num, when, field, got[field], err)
				}
			}
			check("before reopening", mux)
			if err := st.Close(); err != nil {
				t.Fatal(err)
			}
			st, err = store.Open(dir)
			if err != nil {
				t.Fatal(err)
			}
			check("after reopening", serve(st))
			st.Close()
		}
	}
}
