// Package measure serves the endpoints a client measures against: /dl, /ul
// and /ping. The hot-path rules are ADR-0002's and the payload rules ADR-0003's:
// one random buffer made at startup, fixed scratch buffers for uploads, no
// compression, no caching, and a stall timeout on every read and write.
package measure

import (
	"crypto/rand"
	"errors"
	"io"
	"net/http"
	"os"
	"strconv"
	"sync"
	"time"

	"github.com/redlinelabs-dev/dyno/server/internal/metrics"
	"github.com/redlinelabs-dev/dyno/server/internal/testrun"
)

const (
	// PayloadSize is the random buffer every download is cut from. It is larger
	// than any compression window a proxy on the path is likely to use, so the
	// stream stays incompressible (docs/research/methodology-and-architecture.md §5).
	PayloadSize = 4 << 20
	writeChunk  = 256 << 10 // bytes handed to the socket per write
	readChunk   = 64 << 10  // upload scratch buffer size

	// MaxBytes caps a single download or upload request.
	MaxBytes = 16 << 30
)

var payload = func() []byte {
	b := make([]byte, PayloadSize)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return b
}()

var readBufs = sync.Pool{New: func() any { b := make([]byte, readChunk); return &b }}

// Endpoints holds the measurement handlers. Stall is how long one read or
// write may block before the connection is dropped. Runs, if set, counts the
// bytes of requests tagged with a test id (?t=, from the plan). Metrics, if
// set, counts every byte and in-flight stream, tagged or not.
type Endpoints struct {
	Stall   time.Duration
	Runs    *testrun.Registry
	Metrics *metrics.Metrics
}

func (e *Endpoints) counters(r *http.Request) *testrun.Counters {
	if e.Runs == nil {
		return nil
	}
	return e.Runs.Get(r.URL.Query().Get("t"))
}

// Register adds the measurement routes to mux.
func (e *Endpoints) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /dl", e.download)
	mux.HandleFunc("POST /ul", e.upload)
	mux.HandleFunc("GET /ping", ping)
}

// measurementHeaders keeps every proxy and cache on the path from touching
// the body: no caching, no transform, no response buffering in nginx, and
// Resource Timing readable from other origins.
func measurementHeaders(h http.Header) {
	h.Set("Cache-Control", "no-store, no-transform")
	h.Set("X-Accel-Buffering", "no")
	h.Set("Timing-Allow-Origin", "*")
}

// download streams ?bytes=N bytes of the random payload.
func (e *Endpoints) download(w http.ResponseWriter, r *http.Request) {
	e.Metrics.StreamOpened(metrics.Download)
	defer e.Metrics.StreamClosed(metrics.Download)
	n, err := strconv.ParseInt(r.URL.Query().Get("bytes"), 10, 64)
	if err != nil || n < 0 || n > MaxBytes {
		http.Error(w, "bytes must be an integer from 0 to "+strconv.FormatInt(MaxBytes, 10), http.StatusBadRequest)
		return
	}
	tally := e.counters(r)
	h := w.Header()
	measurementHeaders(h)
	h.Set("Content-Type", "application/octet-stream")
	h.Set("Content-Length", strconv.FormatInt(n, 10))
	w.WriteHeader(http.StatusOK)

	// net/http clears the write deadline after each request, so the next
	// request on this connection doesn't inherit it.
	rc := http.NewResponseController(w)
	off := 0
	for n > 0 {
		c := min(int64(writeChunk), int64(PayloadSize-off), n)
		rc.SetWriteDeadline(time.Now().Add(e.Stall))
		k, err := w.Write(payload[off : off+int(c)])
		e.Metrics.AddSent(k) // one atomic add per 256 KiB chunk
		if tally != nil {
			tally.Down.Add(int64(k))
		}
		if err != nil {
			return // client went away or stopped reading
		}
		n -= c
		off = (off + int(c)) % PayloadSize
	}
}

// upload drains the body and reports what the server received. Upload speed
// is computed from these numbers, never from client progress events (ADR-0003).
func (e *Endpoints) upload(w http.ResponseWriter, r *http.Request) {
	e.Metrics.StreamOpened(metrics.Upload)
	defer e.Metrics.StreamClosed(metrics.Upload)
	if r.ContentLength > MaxBytes {
		tooLarge(w)
		return
	}
	start := time.Now()
	rc := http.NewResponseController(w)
	bp := readBufs.Get().(*[]byte)
	defer readBufs.Put(bp)
	tally := e.counters(r)
	var total int64
	last := start
	for {
		rc.SetReadDeadline(time.Now().Add(e.Stall))
		k, err := r.Body.Read(*bp)
		if k > 0 {
			total += int64(k)
			last = time.Now()
			e.Metrics.AddReceived(k) // one atomic add per read of up to 64 KiB
			if tally != nil {
				tally.Up.Add(int64(k))
			}
		}
		if total > MaxBytes {
			tooLarge(w)
			return
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			// Answer explicitly: a handler that returns without writing gets
			// an empty 200, which would read as a finished upload.
			code := http.StatusBadRequest
			if errors.Is(err, os.ErrDeadlineExceeded) {
				code = http.StatusRequestTimeout
			}
			w.Header().Set("Connection", "close")
			http.Error(w, "upload stalled or aborted", code)
			return
		}
	}
	elapsed := last.Sub(start)

	h := w.Header()
	measurementHeaders(h)
	h.Set("Content-Type", "application/json")
	h.Set("Server-Timing", "recv;dur="+strconv.FormatFloat(float64(elapsed.Microseconds())/1e3, 'f', 3, 64))
	b := make([]byte, 0, 64)
	b = append(b, `{"bytes":`...)
	b = strconv.AppendInt(b, total, 10)
	b = append(b, `,"elapsed_us":`...)
	b = strconv.AppendInt(b, elapsed.Microseconds(), 10)
	b = append(b, "}\n"...)
	w.Write(b)
}

// ping is the latency probe: 204, no body.
func ping(w http.ResponseWriter, r *http.Request) {
	measurementHeaders(w.Header())
	w.WriteHeader(http.StatusNoContent)
}

func tooLarge(w http.ResponseWriter) {
	w.Header().Set("Connection", "close")
	http.Error(w, "upload larger than "+strconv.FormatInt(MaxBytes, 10)+" bytes", http.StatusRequestEntityTooLarge)
}
