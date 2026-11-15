package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/redlinelabs-dev/dyno/server/internal/store"
)

// A server built with newServer has no scheduler and no far-end check, and
// says so with 503s (I7).
func TestIx_NewServerWithoutSchedulerAnswers503(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	srv := httptest.NewServer(newServer("test", time.Second, st, nil, nil).Handler)
	t.Cleanup(srv.Close)

	resp, err := http.Post(srv.URL+"/api/v1/schedules/check", "application/json",
		strings.NewReader(`{"far_end":"http://127.0.0.1:1"}`))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("POST /api/v1/schedules/check: status %d, want 503", resp.StatusCode)
	}
}
