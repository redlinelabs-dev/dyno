package measure

import (
	"bufio"
	"bytes"
	"compress/flate"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func newServer(t *testing.T, stall time.Duration) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	(&Endpoints{Stall: stall}).Register(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func TestDownloadSendsExactBytes(t *testing.T) {
	srv := newServer(t, time.Second)
	for _, n := range []int64{0, 1, writeChunk + 7, 2*PayloadSize + 123} {
		req, _ := http.NewRequest("GET", srv.URL+"/dl?bytes="+strconv.FormatInt(n, 10), nil)
		req.Header.Set("Accept-Encoding", "gzip")
		resp, err := srv.Client().Transport.RoundTrip(req) // RoundTrip: no transparent gunzip
		if err != nil {
			t.Fatal(err)
		}
		got, err := io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		if err != nil || got != n {
			t.Fatalf("bytes=%d: read %d, err %v", n, got, err)
		}
		for k, want := range map[string]string{
			"Content-Type":     "application/octet-stream",
			"Cache-Control":    "no-store, no-transform",
			"Content-Length":   strconv.FormatInt(n, 10),
			"Content-Encoding": "",
		} {
			if v := resp.Header.Get(k); v != want {
				t.Errorf("bytes=%d: %s = %q, want %q", n, k, v, want)
			}
		}
	}
}

func TestDownloadRejectsBadSizes(t *testing.T) {
	srv := newServer(t, time.Second)
	for _, q := range []string{"", "?bytes=", "?bytes=-1", "?bytes=nope", "?bytes=" + strconv.FormatInt(MaxBytes+1, 10)} {
		resp, err := http.Get(srv.URL + "/dl" + q)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("/dl%s: status %d, want 400", q, resp.StatusCode)
		}
	}
}

func TestPayloadIsIncompressible(t *testing.T) {
	var buf bytes.Buffer
	zw, _ := flate.NewWriter(&buf, flate.BestCompression)
	zw.Write(payload)
	zw.Close()
	if ratio := float64(buf.Len()) / float64(len(payload)); ratio < 0.99 {
		t.Fatalf("payload compresses to %.3f of its size", ratio)
	}
}

func TestUploadCountsBytes(t *testing.T) {
	srv := newServer(t, time.Second)
	const n = 10<<20 + 3
	resp, err := http.Post(srv.URL+"/ul", "application/octet-stream", bytes.NewReader(make([]byte, n)))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var got struct {
		Bytes     int64 `json:"bytes"`
		ElapsedUs int64 `json:"elapsed_us"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 200 || got.Bytes != n || got.ElapsedUs < 0 {
		t.Fatalf("status %d, body %+v", resp.StatusCode, got)
	}
	if !strings.HasPrefix(resp.Header.Get("Server-Timing"), "recv;dur=") {
		t.Errorf("Server-Timing = %q", resp.Header.Get("Server-Timing"))
	}
}

func TestUploadRejectsOversizedBody(t *testing.T) {
	r := httptest.NewRequest("POST", "/ul", nil)
	r.ContentLength = MaxBytes + 1
	w := httptest.NewRecorder()
	(&Endpoints{Stall: time.Second}).upload(w, r)
	if w.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status %d, want 413", w.Code)
	}
}

func TestWrongMethods(t *testing.T) {
	srv := newServer(t, time.Second)
	for _, c := range []struct{ method, path string }{{"POST", "/dl?bytes=1"}, {"GET", "/ul"}, {"POST", "/ping"}} {
		req, _ := http.NewRequest(c.method, srv.URL+c.path, nil)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusMethodNotAllowed {
			t.Errorf("%s %s: status %d, want 405", c.method, c.path, resp.StatusCode)
		}
	}
}

func TestPing(t *testing.T) {
	srv := newServer(t, time.Second)
	resp, err := http.Get(srv.URL + "/ping")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent || resp.Header.Get("Cache-Control") != "no-store, no-transform" {
		t.Fatalf("status %d, Cache-Control %q", resp.StatusCode, resp.Header.Get("Cache-Control"))
	}
}

// rawConn is a keep-alive HTTP/1.1 connection with nothing between the test
// and the socket, so stalls are real.
type rawConn struct {
	net.Conn
	br *bufio.Reader
}

func dial(t *testing.T, srv *httptest.Server) *rawConn {
	t.Helper()
	c, err := net.Dial("tcp", srv.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	return &rawConn{Conn: c, br: bufio.NewReader(c)}
}

func (c *rawConn) get(t *testing.T, path string) *http.Response {
	t.Helper()
	if _, err := io.WriteString(c, "GET "+path+" HTTP/1.1\r\nHost: t\r\n\r\n"); err != nil {
		t.Fatal(err)
	}
	resp, err := http.ReadResponse(c.br, nil)
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	return resp
}

// A client that stops reading a download is disconnected after the stall
// timeout instead of holding a goroutine forever.
func TestDownloadStallClosesConnection(t *testing.T) {
	const stall = 200 * time.Millisecond
	srv := newServer(t, stall)
	c := dial(t, srv)
	io.WriteString(c, "GET /dl?bytes=1073741824 HTTP/1.1\r\nHost: t\r\n\r\n")
	time.Sleep(10 * stall) // don't read; socket buffers fill and the server's write blocks
	c.SetReadDeadline(time.Now().Add(5 * time.Second))
	n, err := io.Copy(io.Discard, c)
	if errors.Is(err, syscall.ECONNRESET) {
		err = nil
	}
	if err != nil || n >= 1<<30 {
		t.Fatalf("read %d bytes, err %v; want the server to cut the download short", n, err)
	}
}

// The per-write deadline must not leak into the next request on the same
// keep-alive connection. net/http clears it after each request; this guards
// that behaviour, which download relies on.
func TestDeadlineResetForNextRequest(t *testing.T) {
	const stall = 100 * time.Millisecond
	srv := newServer(t, stall)
	c := dial(t, srv)
	if r := c.get(t, "/dl?bytes=1000"); r.StatusCode != 200 {
		t.Fatalf("dl status %d", r.StatusCode)
	}
	time.Sleep(5 * stall)
	if r := c.get(t, "/ping"); r.StatusCode != 204 {
		t.Fatalf("ping after idle: status %d", r.StatusCode)
	}
	if r := c.get(t, "/dl?bytes=1000"); r.StatusCode != 200 {
		t.Fatalf("second dl status %d", r.StatusCode)
	}
}

// An upload that stops sending gets a 408, not an empty 200 that would read as
// a finished upload.
func TestUploadStallTimesOut(t *testing.T) {
	const stall = 200 * time.Millisecond
	srv := newServer(t, stall)
	c := dial(t, srv)
	io.WriteString(c, "POST /ul HTTP/1.1\r\nHost: t\r\nContent-Length: 1048576\r\n\r\n0123456789")
	c.SetReadDeadline(time.Now().Add(5 * time.Second))
	resp, err := http.ReadResponse(c.br, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusRequestTimeout {
		t.Fatalf("status %d, want 408", resp.StatusCode)
	}
}
