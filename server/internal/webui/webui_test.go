package webui

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The files browsers and crawlers fetch by fixed name are in the build, with
// the content types they need.
func TestFixedNameFiles(t *testing.T) {
	h := Handler()
	for path, want := range map[string]string{
		"/robots.txt":           "text/plain",
		"/favicon.ico":          "image/",
		"/apple-touch-icon.png": "image/png",
		"/icon-192.png":         "image/png",
		"/icon-512.png":         "image/png",
		"/icon.svg":             "image/svg+xml",
		"/manifest.webmanifest": "application/manifest+json",
	} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusOK {
			t.Errorf("GET %s: status %d", path, rec.Code)
			continue
		}
		if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, want) {
			t.Errorf("GET %s: Content-Type %q, want %s", path, ct, want)
		}
	}
}

// The page asks not to be indexed (ADR-0023).
func TestIndexIsNoindex(t *testing.T) {
	rec := httptest.NewRecorder()
	Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if !strings.Contains(rec.Body.String(), `<meta name="robots" content="noindex">`) {
		t.Error("index.html has no robots noindex meta")
	}
}
