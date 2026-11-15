package api_test

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/redlinelabs-dev/dyno/server/internal/api"
	"github.com/redlinelabs-dev/dyno/server/internal/result"
	"github.com/redlinelabs-dev/dyno/server/internal/settings"
	"github.com/redlinelabs-dev/dyno/server/internal/store"
	"github.com/redlinelabs-dev/dyno/server/internal/testrun"
)

// histCSVHeader is the first row of results.csv, as docs/api.md gives it.
const histCSVHeader = "id,at,source,schedule_id,far_end,device,client,download_mbps,upload_mbps,idle_ms,loaded_down_ms,loaded_up_ms,jitter_ms,bufferbloat_ms,grade,rpm,streams,client_limited,path_changed,failure_code,failure_phase,failure_message,server_bytes_down,server_bytes_up"

// histT0 is where the seed data starts; seeded results are an hour apart.
var histT0 = time.Date(2026, 11, 10, 0, 0, 0, 0, time.UTC)

// histHour is h hours after histT0.
func histHour(h int) time.Time {
	return histT0.Add(time.Duration(h) * time.Hour)
}

// histZ is histHour(h) as RFC 3339 in UTC.
func histZ(h int) string {
	return histHour(h).Format(time.RFC3339)
}

// histSec is s seconds after histT0 as RFC 3339 in UTC.
func histSec(s int) string {
	return histT0.Add(time.Duration(s) * time.Second).Format(time.RFC3339)
}

// histIn is histHour(h) as RFC 3339 with a UTC offset given in seconds.
func histIn(h, offset int) string {
	return histHour(h).In(time.FixedZone("", offset)).Format(time.RFC3339)
}

func histNewClient() *http.Client {
	c := &http.Client{Timeout: 60 * time.Second}
	c.Transport = &http.Transport{DisableKeepAlives: true}
	return c
}

var histHTTP = histNewClient()

type histResp struct {
	status int
	header http.Header
	body   []byte
}

// histServe opens a store in dir and serves the API over it on a real
// listener. The returned func stops the server and closes the store; it also
// runs when the test ends.
func histServe(t *testing.T, dir string) (*store.Store, string, func()) {
	t.Helper()
	st, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	a := &api.API{Version: "1.2.3", Store: st, Runs: testrun.NewRegistry(), Log: slog.New(slog.DiscardHandler)}
	mux := http.NewServeMux()
	a.Register(mux)
	srv := httptest.NewServer(mux)
	var once sync.Once
	stop := func() {
		once.Do(func() {
			srv.Close()
			st.Close()
		})
	}
	t.Cleanup(stop)
	return st, srv.URL, stop
}

func histDo(t *testing.T, base, method, path, body string) histResp {
	t.Helper()
	req, err := http.NewRequest(method, base+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := histHTTP.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("%s %s: reading the reply: %v", method, path, err)
	}
	return histResp{resp.StatusCode, resp.Header, b}
}

// histJSON decodes a reply body into v.
func histJSON(t *testing.T, r histResp, v any) {
	t.Helper()
	if err := json.Unmarshal(r.body, v); err != nil {
		t.Fatalf("the reply is not the JSON expected: %v\n%s", err, r.body)
	}
}

// histQ builds a query string from name and value pairs, escaping both.
func histQ(kv ...string) string {
	v := url.Values{}
	for i := 0; i+1 < len(kv); i += 2 {
		v.Add(kv[i], kv[i+1])
	}
	if len(v) == 0 {
		return ""
	}
	return "?" + v.Encode()
}

func histWantJSON(t *testing.T, r histResp) {
	t.Helper()
	if ct := r.header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Errorf("Content-Type %q, want application/json", ct)
	}
}

// histWantError wants a reply with the status and a body of the form
// {"error": "..."}; it returns the message.
func histWantError(t *testing.T, r histResp, status int) string {
	t.Helper()
	if r.status != status {
		t.Errorf("status %d, want %d: %s", r.status, status, r.body)
		return ""
	}
	histWantJSON(t, r)
	var e struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(r.body, &e); err != nil || e.Error == "" {
		t.Errorf("body %s, want {\"error\": \"...\"}", r.body)
	}
	return e.Error
}

func histAdd(t *testing.T, st *store.Store, r result.Result) result.Result {
	t.Helper()
	got, err := st.Add(r)
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	return got
}

// histRes is a client result stored at the given time.
func histRes(at time.Time, source, device string) result.Result {
	r := result.Result{At: at, Source: source}
	r.Client = "Chrome on Linux"
	r.Device = device
	r.DownloadMbps = 940
	r.UploadMbps = 400
	r.IdleMs = 8
	r.LoadedDownMs = 20
	r.LoadedUpMs = 30
	r.JitterMs = 0.5
	r.Streams = 5
	r.ClientLimited = true
	r.Detail = []byte(`{"timeline":[1,2,3]}`)
	r.ServerBytesDown = 1 << 30
	r.ServerBytesUp = 1 << 29
	r.Derive()
	return r
}

// histSched is a scheduled result for one schedule. A failed run has no
// figures and carries its failure.
func histSched(at time.Time, scheduleID int64, failed bool) result.Result {
	r := histRes(at, "schedule", "")
	r.Client = "dyno/0.1.0"
	r.ScheduleID = scheduleID
	r.FarEnd = "http://tailmeadow:3966"
	if failed {
		r.DownloadMbps, r.UploadMbps = 0, 0
		r.IdleMs, r.LoadedDownMs, r.LoadedUpMs, r.JitterMs = 0, 0, 0, 0
		r.Streams, r.ClientLimited = 0, false
		r.BufferbloatMs, r.Grade, r.RPM = 0, "", 0
		r.ServerBytesDown, r.ServerBytesUp = 0, 0
		r.Failure = &result.Failure{Code: "unreachable", Phase: "latency", Message: "No connection to the far end."}
	}
	return r
}

type histRow struct {
	source string
	device string
	sched  int64
	failed bool
}

// histSeedTen stores ten results an hour apart from histT0, with ids 1 to 10:
// client results 1 (phone), 2 (laptop), 3 (phone) and 9 (no device), then
// scheduled ones: 4, 5 and 8 for schedule 1, 6 and 7 for schedule 2 and 10 for
// schedule 3. Results 5, 7 and 10 are failed runs.
func histSeedTen(t *testing.T, st *store.Store) {
	t.Helper()
	rows := []histRow{
		{"client", "phone", 0, false},
		{"client", "laptop", 0, false},
		{"client", "phone", 0, false},
		{"schedule", "", 1, false},
		{"schedule", "", 1, true},
		{"schedule", "", 2, false},
		{"schedule", "", 2, true},
		{"schedule", "", 1, false},
		{"client", "", 0, false},
		{"schedule", "", 3, true},
	}
	for i, row := range rows {
		r := histRes(histHour(i), row.source, row.device)
		if row.source == "schedule" {
			r = histSched(histHour(i), row.sched, row.failed)
		}
		want := int64(i + 1)
		if got := histAdd(t, st, r); got.ID != want {
			t.Fatalf("seed %d got id %d, want %d", i+1, got.ID, want)
		}
	}
}

// histSeedMany stores n small client results a second apart, with ids 1 to n.
func histSeedMany(t *testing.T, st *store.Store, n int) {
	t.Helper()
	for i := range n {
		at := histT0.Add(time.Duration(i) * time.Second)
		r := histRes(at, "client", "")
		r.Detail = nil
		histAdd(t, st, r)
	}
}

// histRange lists the ids from first to last, counting up or down.
func histRange(first, last int64) []int64 {
	step := int64(1)
	if last < first {
		step = -1
	}
	end := last + step
	var ids []int64
	for id := first; id != end; id += step {
		ids = append(ids, id)
	}
	return ids
}

func histHead(ids []int64) []int64 {
	if len(ids) > 12 {
		return ids[:12]
	}
	return ids
}

func histCheckIDs(t *testing.T, what string, got, want []int64) {
	t.Helper()
	if slices.Equal(got, want) {
		return
	}
	if len(got) <= 20 && len(want) <= 20 {
		t.Errorf("%s: ids %v, want %v", what, got, want)
		return
	}
	t.Errorf("%s: %d ids, want %d; first %v, want first %v", what, len(got), len(want), histHead(got), histHead(want))
}

// histListIDs fetches the JSON list with the query and returns the ids in
// the order sent.
func histListIDs(t *testing.T, base, query string) []int64 {
	t.Helper()
	r := histDo(t, base, "GET", "/api/v1/results"+query, "")
	if r.status != http.StatusOK {
		t.Fatalf("GET /api/v1/results%s: status %d: %s", query, r.status, r.body)
	}
	var page struct {
		Results []struct {
			ID int64 `json:"id"`
		} `json:"results"`
	}
	histJSON(t, r, &page)
	ids := []int64{}
	for _, row := range page.Results {
		ids = append(ids, row.ID)
	}
	return ids
}

// histCSVRecords fetches the export with the query and parses it with
// encoding/csv; the first record is the header.
func histCSVRecords(t *testing.T, base, query string) (histResp, [][]string) {
	t.Helper()
	r := histDo(t, base, "GET", "/api/v1/results.csv"+query, "")
	if r.status != http.StatusOK {
		t.Fatalf("GET /api/v1/results.csv%s: status %d: %s", query, r.status, r.body)
	}
	recs, err := csv.NewReader(bytes.NewReader(r.body)).ReadAll()
	if err != nil {
		t.Fatalf("GET /api/v1/results.csv%s: not CSV: %v\n%s", query, err, r.body)
	}
	return r, recs
}

// histRowsByColumn turns records into one map of cells per row after the
// header row, keyed by column name.
func histRowsByColumn(t *testing.T, recs [][]string) []map[string]string {
	t.Helper()
	if len(recs) == 0 {
		t.Fatal("the CSV has no header row")
	}
	var rows []map[string]string
	for _, rec := range recs[1:] {
		row := map[string]string{}
		for i, name := range recs[0] {
			row[name] = rec[i]
		}
		rows = append(rows, row)
	}
	return rows
}

func histCSVIDs(t *testing.T, rows []map[string]string) []int64 {
	t.Helper()
	ids := []int64{}
	for _, row := range rows {
		id, err := strconv.ParseInt(row["id"], 10, 64)
		if err != nil {
			t.Fatalf("id cell %q is not a number", row["id"])
		}
		ids = append(ids, id)
	}
	return ids
}

// histCheckExportHeaders checks what the export says about itself: its type,
// its file name and its first row.
func histCheckExportHeaders(t *testing.T, r histResp) {
	t.Helper()
	if got := r.header.Get("Content-Type"); got != "text/csv; charset=utf-8" {
		t.Errorf("Content-Type %q, want %q", got, "text/csv; charset=utf-8")
	}
	if got, want := r.header.Get("Content-Disposition"), `attachment; filename="dyno-results.csv"`; got != want {
		t.Errorf("Content-Disposition %q, want %q", got, want)
	}
	first, _, _ := strings.Cut(string(r.body), "\n")
	if got := strings.TrimSuffix(first, "\r"); got != histCSVHeader {
		t.Errorf("first row\n got %s\nwant %s", got, histCSVHeader)
	}
}

// histBad is a query the list and the export must both refuse.
type histBad struct{ name, query string }

var histBadQueries = []histBad{
	{"from is not a time", histQ("from", "yesterday")},
	{"to is not a time", histQ("to", "yesterday")},
	{"to equals from", histQ("from", histZ(3), "to", histZ(3))},
	{"to is before from", histQ("from", histZ(3), "to", histZ(2))},
	{"to is from in another offset", histQ("from", histZ(3), "to", histIn(3, 7200))},
	{"source is not client or schedule", histQ("source", "browser")},
	{"schedule is not a number", histQ("schedule", "x")},
	{"schedule is 0", histQ("schedule", "0")},
	{"outcome is not succeeded or failed", histQ("outcome", "ok")},
}

func TestH11_ListAnswersAsBefore(t *testing.T) {
	st, base, _ := histServe(t, t.TempDir())

	empty := histDo(t, base, "GET", "/api/v1/results", "")
	if empty.status != http.StatusOK {
		t.Fatalf("empty store: status %d: %s", empty.status, empty.body)
	}
	histWantJSON(t, empty)
	var shell map[string]json.RawMessage
	histJSON(t, empty, &shell)
	var none []json.RawMessage
	if err := json.Unmarshal(shell["results"], &none); err != nil || none == nil || len(none) != 0 {
		t.Errorf("empty store: body %s, want {\"results\": []}", empty.body)
	}

	// Ids 1 to 60, a minute apart; the even ones are the phone's.
	for i := range 60 {
		dev := "laptop"
		if i%2 == 1 {
			dev = "phone"
		}
		at := histT0.Add(time.Duration(i) * time.Minute)
		histAdd(t, st, histRes(at, "client", dev))
	}
	var phone []int64
	for id := int64(60); id >= 2; id -= 2 {
		phone = append(phone, id)
	}
	histCheckIDs(t, "default limit", histListIDs(t, base, ""), histRange(60, 11))
	histCheckIDs(t, "limit=3", histListIDs(t, base, histQ("limit", "3")), []int64{60, 59, 58})
	histCheckIDs(t, "before=11", histListIDs(t, base, histQ("before", "11")), histRange(10, 1))
	histCheckIDs(t, "before=30 and limit=5", histListIDs(t, base, histQ("before", "30", "limit", "5")), histRange(29, 25))
	histCheckIDs(t, "device=phone", histListIDs(t, base, histQ("device", "phone")), phone)
	histCheckIDs(t, "device=phone, before=20 and limit=3", histListIDs(t, base, histQ("device", "phone", "before", "20", "limit", "3")), []int64{18, 16, 14})
	histCheckIDs(t, "device=laptop and limit=2", histListIDs(t, base, histQ("device", "laptop", "limit", "2")), []int64{59, 57})
	histCheckIDs(t, "a device nobody has", histListIDs(t, base, histQ("device", "nobody")), []int64{})

	// A filter that matches nothing answers "results": [], as the empty store
	// does, never null. histListIDs would read both as no ids.
	nobody := histDo(t, base, "GET", "/api/v1/results"+histQ("device", "nobody"), "")
	if nobody.status != http.StatusOK {
		t.Fatalf("a device nobody has: status %d: %s", nobody.status, nobody.body)
	}
	var nobodyShell map[string]json.RawMessage
	histJSON(t, nobody, &nobodyShell)
	var nobodyRows []json.RawMessage
	if err := json.Unmarshal(nobodyShell["results"], &nobodyRows); err != nil || nobodyRows == nil || len(nobodyRows) != 0 {
		t.Errorf("a device nobody has: body %s, want {\"results\": []}", nobody.body)
	}

	raw := histDo(t, base, "GET", "/api/v1/results?limit=5", "")
	var rows struct {
		Results []map[string]json.RawMessage `json:"results"`
	}
	histJSON(t, raw, &rows)
	if len(rows.Results) != 5 {
		t.Fatalf("limit=5 returned %d rows", len(rows.Results))
	}
	for _, row := range rows.Results {
		for _, key := range []string{"id", "at", "source", "client", "device", "download_mbps", "upload_mbps", "idle_ms", "loaded_down_ms", "loaded_up_ms", "jitter_ms", "bufferbloat_ms", "grade", "rpm", "streams", "client_limited", "server_bytes_down", "server_bytes_up"} {
			if _, ok := row[key]; !ok {
				t.Errorf("row %s has no %q", row["id"], key)
			}
		}
		// No detail in a list, and nothing that only scheduled results have.
		for _, key := range []string{"detail", "schedule_id", "far_end", "path_changed", "failure"} {
			if _, ok := row[key]; ok {
				t.Errorf("row %s has %q", row["id"], key)
			}
		}
	}
}

func TestH12_FromAndToSelectByTime(t *testing.T) {
	st, base, _ := histServe(t, t.TempDir())
	histSeedTen(t, st)
	for _, c := range []struct {
		name  string
		query string
		want  []int64
	}{
		{"from is inclusive", histQ("from", histZ(3)), histRange(10, 4)},
		{"to is exclusive", histQ("to", histZ(3)), []int64{3, 2, 1}},
		{"from and to", histQ("from", histZ(2), "to", histZ(5)), []int64{5, 4, 3}},
		{"from with a positive offset", histQ("from", histIn(3, 7200)), histRange(10, 4)},
		{"to with a negative offset", histQ("to", histIn(3, -18000)), []int64{3, 2, 1}},
		{"from and to in different offsets", histQ("from", histIn(2, 3600), "to", histIn(5, -3600)), []int64{5, 4, 3}},
		{"a range holding nothing", histQ("from", histZ(100), "to", histZ(200)), []int64{}},
		{"a range before everything", histQ("from", histZ(-10), "to", histZ(0)), []int64{}},
		{"from before everything", histQ("from", histZ(-10)), histRange(10, 1)},
	} {
		t.Run(c.name, func(t *testing.T) {
			histCheckIDs(t, "ids", histListIDs(t, base, c.query), c.want)
		})
	}
}

func TestH12_FromAndToWorkToTheMillisecond(t *testing.T) {
	st, base, _ := histServe(t, t.TempDir())
	x := histHour(12)
	ms := time.Millisecond
	// Ids 1, 2 and 3 are stored at x - 1 ms, x and x + 1 ms.
	for _, at := range []time.Time{x.Add(-ms), x, x.Add(ms)} {
		histAdd(t, st, histRes(at, "client", ""))
	}
	stamp := func(at time.Time) string {
		return at.UTC().Format("2006-01-02T15:04:05.000Z07:00")
	}
	for _, c := range []struct {
		name  string
		query string
		want  []int64
	}{
		{"from x", histQ("from", stamp(x)), []int64{3, 2}},
		{"from x + 1 ms", histQ("from", stamp(x.Add(ms))), []int64{3}},
		{"to x", histQ("to", stamp(x)), []int64{1}},
		{"to x + 1 ms", histQ("to", stamp(x.Add(ms))), []int64{2, 1}},
		{"from x to x + 1 ms", histQ("from", stamp(x), "to", stamp(x.Add(ms))), []int64{2}},
	} {
		histCheckIDs(t, c.name, histListIDs(t, base, c.query), c.want)
	}
}

func TestH12_SourceScheduleAndOutcomeSelect(t *testing.T) {
	st, base, _ := histServe(t, t.TempDir())
	histSeedTen(t, st)
	for _, c := range []struct {
		name  string
		query string
		want  []int64
	}{
		{"source client", histQ("source", "client"), []int64{9, 3, 2, 1}},
		{"source schedule", histQ("source", "schedule"), []int64{10, 8, 7, 6, 5, 4}},
		{"schedule 1", histQ("schedule", "1"), []int64{8, 5, 4}},
		{"schedule 2", histQ("schedule", "2"), []int64{7, 6}},
		{"schedule 3", histQ("schedule", "3"), []int64{10}},
		{"a schedule with no results", histQ("schedule", "99"), []int64{}},
		{"outcome failed", histQ("outcome", "failed"), []int64{10, 7, 5}},
		{"outcome succeeded", histQ("outcome", "succeeded"), []int64{9, 8, 6, 4, 3, 2, 1}},
		{"source client and outcome failed", histQ("source", "client", "outcome", "failed"), []int64{}},
		{"source client and schedule 1", histQ("source", "client", "schedule", "1"), []int64{}},
		{"source schedule and schedule 2", histQ("source", "schedule", "schedule", "2"), []int64{7, 6}},
		{"schedule 1 and outcome failed", histQ("schedule", "1", "outcome", "failed"), []int64{5}},
		{"source schedule and outcome succeeded", histQ("source", "schedule", "outcome", "succeeded"), []int64{8, 6, 4}},
	} {
		t.Run(c.name, func(t *testing.T) {
			histCheckIDs(t, "ids", histListIDs(t, base, c.query), c.want)
		})
	}
}

func TestH12_FiltersCombineWithDeviceBeforeAndLimit(t *testing.T) {
	st, base, _ := histServe(t, t.TempDir())
	histSeedTen(t, st)
	for _, c := range []struct {
		name  string
		query string
		want  []int64
	}{
		{"device and source", histQ("device", "phone", "source", "client"), []int64{3, 1}},
		{"device and outcome", histQ("device", "phone", "outcome", "succeeded"), []int64{3, 1}},
		{"device and from", histQ("device", "phone", "from", histZ(1)), []int64{3}},
		{"device and to", histQ("device", "phone", "to", histZ(2)), []int64{1}},
		{"device and schedule", histQ("device", "phone", "schedule", "1"), []int64{}},
		{"device, before and limit", histQ("device", "phone", "before", "3", "limit", "1"), []int64{1}},
		{"before and source", histQ("before", "8", "source", "schedule"), []int64{7, 6, 5, 4}},
		{"before and outcome", histQ("before", "8", "outcome", "failed"), []int64{7, 5}},
		{"before and a range", histQ("before", "8", "from", histZ(2), "to", histZ(7)), []int64{7, 6, 5, 4, 3}},
		{"limit and source", histQ("limit", "2", "source", "schedule"), []int64{10, 8}},
		{"limit and outcome", histQ("limit", "2", "outcome", "failed"), []int64{10, 7}},
		{"limit and a range", histQ("limit", "3", "from", histZ(3), "to", histZ(9)), []int64{9, 8, 7}},
		{"limit, source and outcome", histQ("limit", "3", "source", "client", "outcome", "succeeded"), []int64{9, 3, 2}},
		{"limit, before and schedule", histQ("limit", "1", "before", "8", "schedule", "1"), []int64{5}},
		{"every parameter that fits together", histQ("from", histZ(3), "to", histZ(8), "source", "schedule", "schedule", "1", "outcome", "succeeded", "before", "8", "limit", "5"), []int64{4}},
	} {
		t.Run(c.name, func(t *testing.T) {
			histCheckIDs(t, "ids", histListIDs(t, base, c.query), c.want)
		})
	}
}

func TestH13_BadParametersAreRefused(t *testing.T) {
	st, base, _ := histServe(t, t.TempDir())
	histSeedTen(t, st)
	for _, c := range histBadQueries {
		t.Run(c.name, func(t *testing.T) {
			histWantError(t, histDo(t, base, "GET", "/api/v1/results"+c.query, ""), http.StatusBadRequest)
		})
	}
}

// A limit above the largest is no mistake, so it is no 400.
func TestH13_LimitAbove10000IsAccepted(t *testing.T) {
	st, base, _ := histServe(t, t.TempDir())
	histSeedTen(t, st)
	for _, limit := range []string{"10000", "10001", "99999", "1000000"} {
		histCheckIDs(t, "limit="+limit, histListIDs(t, base, histQ("limit", limit)), histRange(10, 1))
	}
}

func TestH13_LimitOver10000IsTreatedAs10000(t *testing.T) {
	if testing.Short() {
		t.Skip("stores 10,001 results")
	}
	st, base, _ := histServe(t, t.TempDir())
	const total = 10001
	histSeedMany(t, st, total)
	for _, c := range []struct {
		limit string
		want  int
	}{
		{"600", 600},
		{"10000", 10000},
		{"10001", 10000},
		{"99999", 10000},
	} {
		ids := histListIDs(t, base, histQ("limit", c.limit))
		if len(ids) != c.want {
			t.Errorf("limit=%s: %d results, want %d", c.limit, len(ids), c.want)
			continue
		}
		// The newest ones, newest first.
		last := int64(total - c.want + 1)
		if ids[0] != total || ids[len(ids)-1] != last {
			t.Errorf("limit=%s: ids run from %d to %d, want %d to %d", c.limit, ids[0], ids[len(ids)-1], int64(total), last)
		}
	}
}

func TestH14_ExportHeadersHeaderRowAndOrder(t *testing.T) {
	st, base, _ := histServe(t, t.TempDir())

	r, recs := histCSVRecords(t, base, "")
	histCheckExportHeaders(t, r)
	if len(recs) != 1 || strings.Join(recs[0], ",") != histCSVHeader {
		t.Errorf("an empty store exports %q, want the header row alone", recs)
	}

	histSeedTen(t, st)
	r, recs = histCSVRecords(t, base, "")
	histCheckExportHeaders(t, r)
	if len(recs) != 11 {
		t.Fatalf("%d records, want the header row and 10 results", len(recs))
	}
	if got := strings.Join(recs[0], ","); got != histCSVHeader {
		t.Errorf("header row %s", got)
	}
	histCheckIDs(t, "export order", histCSVIDs(t, histRowsByColumn(t, recs)), histRange(1, 10))

	// One row per selected result.
	_, recs = histCSVRecords(t, base, histQ("source", "client"))
	histCheckIDs(t, "source=client", histCSVIDs(t, histRowsByColumn(t, recs)), []int64{1, 2, 3, 9})
}

// The list and the export read in pages: no result is lost or sent twice where
// one page ends and the next begins.
func TestH14_ExportAndListSpanSeveralPages(t *testing.T) {
	st, base, _ := histServe(t, t.TempDir())
	const n = 1300
	histSeedMany(t, st, n)
	from, to := histSec(250), histSec(1100)

	_, recs := histCSVRecords(t, base, "")
	histCheckIDs(t, "export of everything", histCSVIDs(t, histRowsByColumn(t, recs)), histRange(1, n))
	_, recs = histCSVRecords(t, base, histQ("from", from, "to", to))
	histCheckIDs(t, "export of a range", histCSVIDs(t, histRowsByColumn(t, recs)), histRange(251, 1100))

	histCheckIDs(t, "list of everything", histListIDs(t, base, histQ("limit", "2000")), histRange(n, 1))
	histCheckIDs(t, "list of a range", histListIDs(t, base, histQ("limit", "2000", "from", from, "to", to)), histRange(1100, 251))
	histCheckIDs(t, "list before an id", histListIDs(t, base, histQ("limit", "600", "before", "1000")), histRange(999, 400))
}

// histH15Results are four results with figures that a rounded or shortened
// number would change, and times that a fixed layout would print differently:
// a client's with milliseconds and one on the whole second, a scheduled one
// whose path changed, and a failed scheduled one.
func histH15Results() []result.Result {
	kitchen := result.Result{At: time.Date(2026, 11, 10, 8, 30, 15, 408_000_000, time.UTC), Source: "client"}
	kitchen.Client, kitchen.Device = "Kitchen iPad", "Kq3-x_9"
	kitchen.DownloadMbps, kitchen.UploadMbps = 938.2371, 412.6
	kitchen.IdleMs, kitchen.LoadedDownMs, kitchen.LoadedUpMs, kitchen.JitterMs = 8.4, 21, 39.5, 0.82
	kitchen.Streams, kitchen.ClientLimited = 5, true
	kitchen.ServerBytesDown, kitchen.ServerBytesUp = 1_099_511_627_781, 98_765_432
	kitchen.Derive()

	phone := result.Result{At: time.Date(2026, 11, 10, 9, 0, 0, 0, time.UTC), Source: "client"}
	phone.Client = "Chrome on Android"
	phone.DownloadMbps, phone.UploadMbps = 12.5, 3.25
	phone.IdleMs, phone.JitterMs = 25.125, 1.5
	phone.Streams = 3
	phone.Derive()

	moved := result.Result{At: time.Date(2026, 11, 10, 10, 0, 0, 500_000_000, time.UTC), Source: "schedule"}
	moved.ScheduleID, moved.FarEnd, moved.Client = 7, "http://tailmeadow:3966", "dyno/0.1.0"
	moved.PathChanged = true
	moved.DownloadMbps, moved.UploadMbps = 480.1, 95.75
	moved.IdleMs, moved.LoadedDownMs, moved.LoadedUpMs, moved.JitterMs = 40.2, 44.5, 60.9, 2.125
	moved.Streams = 5
	moved.ServerBytesDown, moved.ServerBytesUp = 61_234_567, 12_345_678
	moved.Derive()

	failed := result.Result{At: time.Date(2026, 11, 10, 11, 0, 0, 1_000_000, time.UTC), Source: "schedule"}
	failed.ScheduleID, failed.FarEnd, failed.Client = 7, "http://tailmeadow:3966", "dyno/0.1.0"
	failed.Failure = &result.Failure{Code: "unreachable", Phase: "latency", Message: "The far end did not answer: connection refused."}

	return []result.Result{kitchen, phone, moved, failed}
}

// histAtTexts is each result's `at` as the JSON list writes it.
func histAtTexts(t *testing.T, base string) map[int64]string {
	t.Helper()
	r := histDo(t, base, "GET", "/api/v1/results?limit=100", "")
	if r.status != http.StatusOK {
		t.Fatalf("GET /api/v1/results: status %d: %s", r.status, r.body)
	}
	var page struct {
		Results []struct {
			ID int64  `json:"id"`
			At string `json:"at"`
		} `json:"results"`
	}
	histJSON(t, r, &page)
	out := map[int64]string{}
	for _, row := range page.Results {
		out[row.ID] = row.At
	}
	return out
}

// histCheckRow compares one CSV row with the result it came from; at is the
// text the JSON list gives for the result's time.
func histCheckRow(t *testing.T, row map[string]string, r result.Result, at string) {
	t.Helper()
	id := strconv.FormatInt(r.ID, 10)
	text := func(col, want string) {
		t.Helper()
		if row[col] != want {
			t.Errorf("result %s: %s = %q, want %q", id, col, row[col], want)
		}
	}
	text("id", id)
	text("at", at)
	if parsed, err := time.Parse(time.RFC3339, row["at"]); err != nil || !parsed.Equal(r.At) || !strings.HasSuffix(row["at"], "Z") {
		t.Errorf("result %s: at = %q, want RFC 3339 in UTC for %v", id, row["at"], r.At)
	}
	text("source", r.Source)
	scheduleID := ""
	if r.ScheduleID != 0 {
		scheduleID = strconv.FormatInt(r.ScheduleID, 10)
	}
	text("schedule_id", scheduleID)
	text("far_end", r.FarEnd)
	text("device", r.Device)
	text("client", r.Client)
	text("grade", r.Grade)
	text("client_limited", strconv.FormatBool(r.ClientLimited))
	text("path_changed", strconv.FormatBool(r.PathChanged))
	var f result.Failure
	if r.Failure != nil {
		f = *r.Failure
	}
	text("failure_code", f.Code)
	text("failure_phase", f.Phase)
	text("failure_message", f.Message)
	for _, c := range []struct {
		col string
		v   float64
	}{
		{"download_mbps", r.DownloadMbps},
		{"upload_mbps", r.UploadMbps},
		{"idle_ms", r.IdleMs},
		{"loaded_down_ms", r.LoadedDownMs},
		{"loaded_up_ms", r.LoadedUpMs},
		{"jitter_ms", r.JitterMs},
		{"bufferbloat_ms", r.BufferbloatMs},
	} {
		if got, err := strconv.ParseFloat(row[c.col], 64); err != nil || got != c.v {
			t.Errorf("result %s: %s = %q, want the number %v", id, c.col, row[c.col], c.v)
		}
	}
	for _, c := range []struct {
		col string
		v   int64
	}{
		{"rpm", int64(r.RPM)},
		{"streams", int64(r.Streams)},
		{"server_bytes_down", r.ServerBytesDown},
		{"server_bytes_up", r.ServerBytesUp},
	} {
		if got, err := strconv.ParseInt(row[c.col], 10, 64); err != nil || got != c.v {
			t.Errorf("result %s: %s = %q, want the whole number %d", id, c.col, row[c.col], c.v)
		}
	}
}

func TestH15_CellsHoldTheStoredValues(t *testing.T) {
	st, base, _ := histServe(t, t.TempDir())
	rs := histH15Results()
	for i := range rs {
		rs[i] = histAdd(t, st, rs[i])
	}
	atText := histAtTexts(t, base)
	_, recs := histCSVRecords(t, base, "")
	rows := histRowsByColumn(t, recs)
	if len(rows) != len(rs) {
		t.Fatalf("%d rows, want %d", len(rows), len(rs))
	}
	for i, row := range rows {
		histCheckRow(t, row, rs[i], atText[rs[i].ID])
	}

	// The same cells, by hand: the named cases of the spec.
	if rows[0]["schedule_id"] != "" || rows[0]["far_end"] != "" || rows[0]["path_changed"] != "false" || rows[0]["client_limited"] != "true" {
		t.Errorf("client result row: %v", rows[0])
	}
	if rows[2]["path_changed"] != "true" || rows[2]["schedule_id"] != "7" {
		t.Errorf("scheduled result row: %v", rows[2])
	}
	if rows[3]["failure_code"] != "unreachable" || rows[3]["failure_phase"] != "latency" || rows[3]["failure_message"] != "The far end did not answer: connection refused." {
		t.Errorf("failed run row: %v", rows[3])
	}
}

func TestH16_ExportTakesTheListsFiltersAndIgnoresLimitAndBefore(t *testing.T) {
	st, base, _ := histServe(t, t.TempDir())
	histSeedTen(t, st)
	for _, c := range []struct {
		name  string
		query string
		want  []int64
	}{
		{"no parameters", "", histRange(1, 10)},
		{"from and to", histQ("from", histZ(3), "to", histZ(6)), []int64{4, 5, 6}},
		{"from with an offset", histQ("from", histIn(8, 3600)), []int64{9, 10}},
		{"source client", histQ("source", "client"), []int64{1, 2, 3, 9}},
		{"source schedule", histQ("source", "schedule"), []int64{4, 5, 6, 7, 8, 10}},
		{"schedule 1", histQ("schedule", "1"), []int64{4, 5, 8}},
		{"outcome failed", histQ("outcome", "failed"), []int64{5, 7, 10}},
		{"outcome succeeded", histQ("outcome", "succeeded"), []int64{1, 2, 3, 4, 6, 8, 9}},
		{"device", histQ("device", "phone"), []int64{1, 3}},
		{"device and from", histQ("device", "phone", "from", histZ(1)), []int64{3}},
		{"a range holding nothing", histQ("from", histZ(100), "to", histZ(200)), []int64{}},
		{"several filters", histQ("from", histZ(3), "to", histZ(8), "source", "schedule", "schedule", "1", "outcome", "succeeded"), []int64{4, 8}},
		{"limit is ignored", histQ("limit", "2"), histRange(1, 10)},
		{"before is ignored", histQ("before", "5"), histRange(1, 10)},
		{"limit and before beside filters", histQ("limit", "1", "before", "3", "source", "schedule", "outcome", "succeeded"), []int64{4, 6, 8}},
	} {
		t.Run(c.name, func(t *testing.T) {
			_, recs := histCSVRecords(t, base, c.query)
			histCheckIDs(t, "CSV ids", histCSVIDs(t, histRowsByColumn(t, recs)), c.want)
			// The list takes the same filters; it lists newest first.
			if !strings.Contains(c.query, "limit") && !strings.Contains(c.query, "before") {
				newestFirst := slices.Clone(c.want)
				slices.Reverse(newestFirst)
				histCheckIDs(t, "JSON ids", histListIDs(t, base, c.query), newestFirst)
			}
		})
	}
}

func TestH16_ABadParameterIsJSONAndNoCSV(t *testing.T) {
	st, base, _ := histServe(t, t.TempDir())
	histSeedTen(t, st)
	bad := append(slices.Clone(histBadQueries), histBad{"device is not valid", histQ("device", "a b")})
	for _, c := range bad {
		t.Run(c.name, func(t *testing.T) {
			r := histDo(t, base, "GET", "/api/v1/results.csv"+c.query, "")
			histWantError(t, r, http.StatusBadRequest)
			if ct := r.header.Get("Content-Type"); strings.HasPrefix(ct, "text/csv") {
				t.Errorf("Content-Type %q on an error", ct)
			}
			if cd := r.header.Get("Content-Disposition"); cd != "" {
				t.Errorf("Content-Disposition %q on an error", cd)
			}
			if strings.HasPrefix(string(r.body), "id,at,source") {
				t.Errorf("CSV was sent: %.80s", r.body)
			}
		})
	}
}

func TestH17_CSVQuotingAndFormulaCells(t *testing.T) {
	st, base, _ := histServe(t, t.TempDir())
	cases := []struct{ client, device, wantClient, wantDevice string }{
		{`Kitchen, "big" iPad`, "", `Kitchen, "big" iPad`, ""},
		{"line one\nline two", "", "line one\nline two", ""},
		{"A, \"B\"\nC", "", "A, \"B\"\nC", ""},
		{"Chrome on Android", "", "Chrome on Android", ""},
		{`a=b+c-d@e`, "abc-def_1", `a=b+c-d@e`, "abc-def_1"},
		{`=HYPERLINK("x")`, "", `'=HYPERLINK("x")`, ""},
		{"=", "", "'=", ""},
		{"+1 555", "", "'+1 555", ""},
		{"-2+3", "", "'-2+3", ""},
		{"@SUM(1)", "", "'@SUM(1)", ""},
		{"\tTabbed", "", "'\tTabbed", ""},
		{"Plain", "-abc", "Plain", "'-abc"},
	}
	ids := make([]int64, len(cases))
	for i, c := range cases {
		r := histRes(histHour(i), "client", c.device)
		r.Client = c.client
		ids[i] = histAdd(t, st, r).ID
	}
	// A failed run whose message starts like a formula, and a number that
	// starts with a minus sign: only text gets the quote.
	failed := histSched(histHour(20), 1, true)
	failed.Failure = &result.Failure{Code: "internal", Phase: "", Message: "=1+1 went wrong"}
	failedID := histAdd(t, st, failed).ID
	negative := histRes(histHour(21), "client", "")
	negative.Client = "Negative"
	negative.DownloadMbps = -1.5
	negativeID := histAdd(t, st, negative).ID
	// A client label that starts with a carriage return.
	carriage := histRes(histHour(22), "client", "")
	carriage.Client = "\rReturn"
	carriageID := histAdd(t, st, carriage).ID

	_, recs := histCSVRecords(t, base, "")
	rows := histRowsByColumn(t, recs)
	byID := map[int64]map[string]string{}
	for _, row := range rows {
		id, err := strconv.ParseInt(row["id"], 10, 64)
		if err != nil {
			t.Fatalf("id cell %q is not a number", row["id"])
		}
		byID[id] = row
	}
	for i, c := range cases {
		row := byID[ids[i]]
		if row == nil {
			t.Errorf("no row for client %q", c.client)
			continue
		}
		if row["client"] != c.wantClient {
			t.Errorf("client %q reads back as %q, want %q", c.client, row["client"], c.wantClient)
		}
		if row["device"] != c.wantDevice {
			t.Errorf("device %q reads back as %q, want %q", c.device, row["device"], c.wantDevice)
		}
	}

	if row := byID[failedID]; row == nil {
		t.Error("no row for the failed run")
	} else {
		if row["failure_message"] != "'=1+1 went wrong" {
			t.Errorf("failure_message reads back as %q, want %q", row["failure_message"], "'=1+1 went wrong")
		}
		if row["failure_code"] != "internal" || row["failure_phase"] != "" || row["far_end"] != "http://tailmeadow:3966" {
			t.Errorf("failed run row: %v", row)
		}
	}
	if row := byID[negativeID]; row == nil {
		t.Error("no row for the result with a negative speed")
	} else if got, err := strconv.ParseFloat(row["download_mbps"], 64); err != nil || got != -1.5 {
		t.Errorf("a negative speed reads back as %q, want -1.5 with no quote", row["download_mbps"])
	}
	// encoding/csv's writer can drop a bare carriage return when it is set to
	// write CRLF line ends, so this only wants the quote in front.
	if row := byID[carriageID]; row == nil {
		t.Error("no row for the client label with a carriage return")
	} else if got := row["client"]; got != "'\rReturn" && got != "'Return" {
		t.Errorf("a client starting with a carriage return reads back as %q, want %q", got, "'\rReturn")
	}

	// No number cell has a quote, and an empty cell stays empty.
	for _, row := range rows {
		for _, col := range []string{"download_mbps", "upload_mbps", "idle_ms", "loaded_down_ms", "loaded_up_ms", "jitter_ms", "bufferbloat_ms"} {
			if _, err := strconv.ParseFloat(row[col], 64); err != nil {
				t.Errorf("result %s: %s = %q is not a number", row["id"], col, row[col])
			}
		}
		for _, col := range []string{"id", "rpm", "streams", "server_bytes_down", "server_bytes_up"} {
			if _, err := strconv.ParseInt(row[col], 10, 64); err != nil {
				t.Errorf("result %s: %s = %q is not a whole number", row["id"], col, row[col])
			}
		}
		if row["source"] == "client" && (row["schedule_id"] != "" || row["far_end"] != "" || row["failure_code"] != "") {
			t.Errorf("client result %s has cells that should be empty: %v", row["id"], row)
		}
	}
}

func histGetSettings(t *testing.T, base string) settings.Settings {
	t.Helper()
	r := histDo(t, base, "GET", "/api/v1/settings", "")
	if r.status != http.StatusOK {
		t.Fatalf("GET /api/v1/settings: status %d: %s", r.status, r.body)
	}
	var s settings.Settings
	histJSON(t, r, &s)
	return s
}

func histPatch(t *testing.T, base, body string) histResp {
	t.Helper()
	return histDo(t, base, "PATCH", "/api/v1/settings", body)
}

// histCheckSettingsFields wants a reply of exactly the three settings.
func histCheckSettingsFields(t *testing.T, r histResp) {
	t.Helper()
	var m map[string]any
	histJSON(t, r, &m)
	if len(m) != 3 {
		t.Errorf("the reply has %d fields, want plan_down_mbps, plan_up_mbps and keep_days only: %s", len(m), r.body)
	}
	for _, k := range []string{"plan_down_mbps", "plan_up_mbps", "keep_days"} {
		if _, ok := m[k]; !ok {
			t.Errorf("the reply has no %q: %s", k, r.body)
		}
	}
}

func TestH18_SettingsOfANewStoreAreAllZero(t *testing.T) {
	_, base, _ := histServe(t, t.TempDir())
	r := histDo(t, base, "GET", "/api/v1/settings", "")
	if r.status != http.StatusOK {
		t.Fatalf("status %d: %s", r.status, r.body)
	}
	histWantJSON(t, r)
	histCheckSettingsFields(t, r)
	var m map[string]any
	histJSON(t, r, &m)
	for _, k := range []string{"plan_down_mbps", "plan_up_mbps", "keep_days"} {
		if m[k] != float64(0) {
			t.Errorf("%s = %v, want 0", k, m[k])
		}
	}
}

func TestH19_PatchChangesOnlyTheFieldsSent(t *testing.T) {
	_, base, _ := histServe(t, t.TempDir())
	for _, step := range []struct {
		body string
		want settings.Settings
	}{
		{`{"plan_down_mbps": 500}`, settings.Settings{PlanDownMbps: 500}},
		{`{"plan_up_mbps": 50.5}`, settings.Settings{PlanDownMbps: 500, PlanUpMbps: 50.5}},
		{`{"keep_days": 30}`, settings.Settings{PlanDownMbps: 500, PlanUpMbps: 50.5, KeepDays: 30}},
		{`{}`, settings.Settings{PlanDownMbps: 500, PlanUpMbps: 50.5, KeepDays: 30}},
		{`{"plan_down_mbps": 0}`, settings.Settings{PlanUpMbps: 50.5, KeepDays: 30}},
		{`{"keep_days": 0, "plan_up_mbps": 100000}`, settings.Settings{PlanUpMbps: 100000}},
		{`{"plan_down_mbps": 100000, "plan_up_mbps": 0, "keep_days": 36500}`, settings.Settings{PlanDownMbps: 100000, KeepDays: 36500}},
	} {
		r := histPatch(t, base, step.body)
		if r.status != http.StatusOK {
			t.Fatalf("PATCH %s: status %d: %s", step.body, r.status, r.body)
		}
		histWantJSON(t, r)
		histCheckSettingsFields(t, r)
		var got settings.Settings
		histJSON(t, r, &got)
		if got != step.want {
			t.Errorf("PATCH %s replied %+v, want %+v", step.body, got, step.want)
		}
		if read := histGetSettings(t, base); read != step.want {
			t.Errorf("after PATCH %s, GET says %+v, want %+v", step.body, read, step.want)
		}
	}
}

// histPad pads body with spaces to size bytes.
func histPad(body string, size int) string {
	return body + strings.Repeat(" ", size-len(body))
}

func TestH19_BadBodiesAreRefusedAndChangeNothing(t *testing.T) {
	_, base, _ := histServe(t, t.TempDir())
	baseline := settings.Settings{PlanDownMbps: 500, PlanUpMbps: 50, KeepDays: 30}
	if r := histPatch(t, base, `{"plan_down_mbps": 500, "plan_up_mbps": 50, "keep_days": 30}`); r.status != http.StatusOK {
		t.Fatalf("setting the baseline: status %d: %s", r.status, r.body)
	}
	// field is a name the error has to hold, where the rules say it names one.
	for _, c := range []struct{ name, body, field string }{
		{"plan_down_mbps below range", `{"plan_down_mbps": -1}`, "plan_down_mbps"},
		{"plan_down_mbps above range", `{"plan_down_mbps": 100000.1}`, "plan_down_mbps"},
		{"plan_up_mbps below range", `{"plan_up_mbps": -0.5}`, "plan_up_mbps"},
		{"plan_up_mbps above range", `{"plan_up_mbps": 100001}`, "plan_up_mbps"},
		{"keep_days below range", `{"keep_days": -1}`, "keep_days"},
		{"keep_days above range", `{"keep_days": 36501}`, "keep_days"},
		{"a good field beside a bad one", `{"plan_down_mbps": 600, "keep_days": -1}`, "keep_days"},
		{"an unknown field", `{"plan_down": 5}`, "plan_down"},
		{"a good field beside an unknown one", `{"keep_days": 5, "bogus": 1}`, "bogus"},
		{"keep_days as a fraction", `{"keep_days": 1.5}`, "keep_days"},
		{"keep_days as a string", `{"keep_days": "7"}`, "keep_days"},
		{"plan_down_mbps as a string", `{"plan_down_mbps": "500"}`, "plan_down_mbps"},
		{"plan_up_mbps as a boolean", `{"plan_up_mbps": true}`, "plan_up_mbps"},
		{"keep_days as an array", `{"keep_days": [7]}`, "keep_days"},
		{"invalid JSON", `{"keep_days": `, ""},
		{"not JSON", `keep_days=7`, ""},
		{"an empty body", ``, ""},
		{"an array, not an object", `[]`, ""},
		{"two objects", `{"keep_days": 5}{"keep_days": 6}`, ""},
		{"two objects with a space between", `{"keep_days": 5} {"plan_down_mbps": 1}`, ""},
		{"junk after the object", `{"keep_days": 5} x`, ""},
		{"over 4 KiB, padding first", strings.Repeat(" ", 5000) + `{"keep_days": 5}`, ""},
		{"over 4 KiB, padding after", `{"keep_days": 5}` + strings.Repeat(" ", 5000), ""},
		{"4 KiB and one byte", histPad(`{"keep_days": 5}`, 4097), ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			msg := histWantError(t, histPatch(t, base, c.body), http.StatusBadRequest)
			if c.field != "" && !strings.Contains(msg, c.field) {
				t.Errorf("error %q does not name %q", msg, c.field)
			}
			if got := histGetSettings(t, base); got != baseline {
				t.Errorf("settings are now %+v, want them unchanged: %+v", got, baseline)
			}
		})
	}
}

func TestH19_ABodyOfExactly4KiBIsAccepted(t *testing.T) {
	_, base, _ := histServe(t, t.TempDir())
	r := histPatch(t, base, histPad(`{"plan_down_mbps": 777}`, 4096))
	if r.status != http.StatusOK {
		t.Fatalf("status %d: %s", r.status, r.body)
	}
	if got := histGetSettings(t, base); got != (settings.Settings{PlanDownMbps: 777}) {
		t.Errorf("settings are %+v, want plan_down_mbps 777", got)
	}
}

// A refused PATCH deletes nothing, even when it holds a keep_days that would.
func TestH19_ARefusedPatchDeletesNothing(t *testing.T) {
	st, base, _ := histServe(t, t.TempDir())
	now := time.Now().UTC()
	tenDays := 240 * time.Hour
	old := histAdd(t, st, histRes(now.Add(-tenDays), "client", "")).ID
	fresh := histAdd(t, st, histRes(now.Add(-time.Hour), "client", "")).ID
	for _, body := range []string{
		`{"keep_days": 1, "plan_up_mbps": -1}`,
		`{"keep_days": 1, "bogus": true}`,
		`{"keep_days": 1}{"keep_days": 2}`,
		`{"keep_days": 1, "plan_down_mbps": "fast"}`,
	} {
		if r := histPatch(t, base, body); r.status != http.StatusBadRequest {
			t.Errorf("PATCH %s: status %d, want 400", body, r.status)
		}
		histCheckIDs(t, "results after PATCH "+body, histListIDs(t, base, histQ("limit", "10")), []int64{fresh, old})
	}
}

func TestH20_PatchKeepDaysDeletesOldResultsAtOnce(t *testing.T) {
	st, base, _ := histServe(t, t.TempDir())
	now := time.Now().UTC()
	day := 24 * time.Hour
	add := func(age time.Duration, failed bool) int64 {
		at := now.Add(-age)
		r := histRes(at, "client", "")
		if failed {
			r = histSched(at, 4, true)
		}
		return histAdd(t, st, r).ID
	}
	old1 := add(40*day, false)
	old2 := add(30*day, true)
	old3 := add(8*day, false)
	keep1 := add(6*day, false)
	keep2 := add(day, false)
	keep3 := add(time.Hour, false)

	all := histQ("limit", "100")
	histCheckIDs(t, "before the PATCH", histListIDs(t, base, all), []int64{keep3, keep2, keep1, old3, old2, old1})

	r := histPatch(t, base, `{"keep_days": 7}`)
	if r.status != http.StatusOK {
		t.Fatalf("PATCH keep_days 7: status %d: %s", r.status, r.body)
	}
	histCheckIDs(t, "right after PATCH keep_days 7", histListIDs(t, base, all), []int64{keep3, keep2, keep1})
	for _, id := range []int64{old1, old2, old3} {
		if r := histDo(t, base, "GET", "/api/v1/results/"+strconv.FormatInt(id, 10), ""); r.status != http.StatusNotFound {
			t.Errorf("GET result %d, older than keep_days: status %d, want 404", id, r.status)
		}
	}
	for _, id := range []int64{keep1, keep2, keep3} {
		if r := histDo(t, base, "GET", "/api/v1/results/"+strconv.FormatInt(id, 10), ""); r.status != http.StatusOK {
			t.Errorf("GET result %d, newer than keep_days: status %d, want 200", id, r.status)
		}
	}

	// Lowering it again deletes more, at once.
	if r := histPatch(t, base, `{"keep_days": 2}`); r.status != http.StatusOK {
		t.Fatalf("PATCH keep_days 2: status %d: %s", r.status, r.body)
	}
	histCheckIDs(t, "right after PATCH keep_days 2", histListIDs(t, base, all), []int64{keep3, keep2})

	// Keeping everything again deletes nothing more.
	if r := histPatch(t, base, `{"keep_days": 0}`); r.status != http.StatusOK {
		t.Fatalf("PATCH keep_days 0: status %d: %s", r.status, r.body)
	}
	histCheckIDs(t, "after PATCH keep_days 0", histListIDs(t, base, all), []int64{keep3, keep2})
}

func TestH21_SettingsSurviveARestart(t *testing.T) {
	dir := t.TempDir()
	_, base, stop := histServe(t, dir)
	r := histPatch(t, base, `{"plan_down_mbps": 940.5, "plan_up_mbps": 40.25, "keep_days": 90}`)
	if r.status != http.StatusOK {
		t.Fatalf("PATCH: status %d: %s", r.status, r.body)
	}
	stop()

	_, base, _ = histServe(t, dir)
	want := settings.Settings{PlanDownMbps: 940.5, PlanUpMbps: 40.25, KeepDays: 90}
	if got := histGetSettings(t, base); got != want {
		t.Errorf("after a restart: %+v, want %+v", got, want)
	}
}
