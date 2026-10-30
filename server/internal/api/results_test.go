package api

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/redlinelabs-dev/dyno/server/internal/result"
	"github.com/redlinelabs-dev/dyno/server/internal/testrun"
)

const submission = `{"test_id":"%s","client":"Safari on iPhone","download_mbps":938.2,"upload_mbps":412,
	"idle_ms":8.1,"loaded_down_ms":19,"loaded_up_ms":41.5,"jitter_ms":0.8,"streams":5,"client_limited":false,
	"detail":{"timeline":[[0,1,2]]},"future_field":true}`

func TestPostResultAttachesServerCountsAndGrade(t *testing.T) {
	mux, a := newMux(t)
	id := testrun.NewID()
	c := a.Runs.Get(id)
	c.Down.Add(5000)
	c.Up.Add(700)

	w := do(mux, "POST", "/api/v1/results", strings.Replace(submission, "%s", id, 1))
	if w.Code != 201 {
		t.Fatalf("status %d: %s", w.Code, w.Body)
	}
	var got result.Result
	json.Unmarshal(w.Body.Bytes(), &got)
	if got.ID == 0 || got.Source != "client" || got.Grade != "B" || got.BufferbloatMs != 33.4 ||
		got.RPM != 1446 || got.ServerBytesDown != 5000 || got.ServerBytesUp != 700 {
		t.Fatalf("stored %+v", got)
	}
	if loc := w.Header().Get("Location"); loc != "/api/v1/results/1" {
		t.Errorf("Location = %q", loc)
	}

	one := do(mux, "GET", "/api/v1/results/1", "")
	if one.Code != 200 || !strings.Contains(one.Body.String(), `"detail":{"timeline":[[0,1,2]]}`) {
		t.Fatalf("GET one: %d %s", one.Code, one.Body)
	}
	list := do(mux, "GET", "/api/v1/results", "")
	var page struct{ Results []result.Result }
	json.Unmarshal(list.Body.Bytes(), &page)
	if len(page.Results) != 1 || page.Results[0].Detail != nil {
		t.Fatalf("list: %s", list.Body)
	}
}

func TestPostResultWithoutKnownTestStillStores(t *testing.T) {
	mux, _ := newMux(t)
	w := do(mux, "POST", "/api/v1/results", strings.Replace(submission, "%s", "nope", 1))
	if w.Code != 201 || !strings.Contains(w.Body.String(), `"server_bytes_down":0`) {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
}

func TestResultErrors(t *testing.T) {
	mux, _ := newMux(t)
	for _, c := range []struct {
		method, path, body string
		status             int
	}{
		{"POST", "/api/v1/results", `not json`, 400},
		{"POST", "/api/v1/results", `{"download_mbps":-5}`, 400},
		{"POST", "/api/v1/results", `{"client":"` + strings.Repeat("x", 300<<10) + `"}`, 400},
		{"GET", "/api/v1/results/99", "", 404},
		{"GET", "/api/v1/results/abc", "", 400},
		{"GET", "/api/v1/results?limit=0", "", 400},
		{"GET", "/api/v1/results?before=x", "", 400},
	} {
		if w := do(mux, c.method, c.path, c.body); w.Code != c.status {
			t.Errorf("%s %s: status %d, want %d (%s)", c.method, c.path, w.Code, c.status, w.Body)
		}
	}
}
