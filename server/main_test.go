package main

import (
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/redlinelabs-dev/dyno/server/internal/store"
)

func startServer(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewUnstartedServer(nil)
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	srv.Config = newServer("test", time.Second, st)
	srv.Start()
	t.Cleanup(srv.Close)
	return srv
}

func TestRoutes(t *testing.T) {
	srv := startServer(t)
	for _, c := range []struct {
		path   string
		status int
		has    string
	}{
		{"/", 200, "<title>dyno speed test</title>"},
		{"/api/v1/plan", 200, `"version":"test"`},
		{"/ping", 204, ""},
		{"/dl?bytes=10", 200, ""},
		{"/nope", 404, ""},
	} {
		resp, err := http.Get(srv.URL + c.path)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != c.status || !strings.Contains(string(body), c.has) {
			t.Errorf("GET %s: status %d, body %.80q", c.path, resp.StatusCode, body)
		}
	}
}

// The plan's endpoints must resolve on the server that issued it.
func TestPlanEndpointsResolve(t *testing.T) {
	srv := startServer(t)
	resp, err := http.Get(srv.URL + "/api/v1/plan")
	if err != nil {
		t.Fatal(err)
	}
	var plan struct{ Endpoints map[string]string }
	json.NewDecoder(resp.Body).Decode(&plan)
	resp.Body.Close()
	reqs := map[string]*http.Request{}
	reqs["download"], _ = http.NewRequest("GET", srv.URL+plan.Endpoints["download"]+"?bytes=1", nil)
	reqs["upload"], _ = http.NewRequest("POST", srv.URL+plan.Endpoints["upload"], strings.NewReader("x"))
	reqs["ping"], _ = http.NewRequest("GET", srv.URL+plan.Endpoints["ping"], nil)
	for name, req := range reqs {
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode/100 != 2 {
			t.Errorf("%s endpoint %s: status %d", name, req.URL.Path, resp.StatusCode)
		}
	}
}

func TestHealthcheck(t *testing.T) {
	srv := startServer(t)
	_, port, _ := net.SplitHostPort(srv.Listener.Addr().String())
	if err := check(":" + port); err != nil {
		t.Fatalf("healthy server: %v", err)
	}
	srv.Close()
	if err := check(":" + port); err == nil {
		t.Fatal("closed server reported healthy")
	}
}
