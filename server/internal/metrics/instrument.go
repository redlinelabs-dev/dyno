package metrics

import (
	"io"
	"net/http"
)

// Instrument counts every request next serves in dyno_requests_total. next
// must be the http.ServeMux, which sets Request.Pattern; the pattern, never
// the raw path, picks the route label (RouteOf).
func (m *Metrics) Instrument(next http.Handler) http.Handler {
	if m == nil {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sw := &statusWriter{ResponseWriter: w}
		next.ServeHTTP(sw, r)
		m.CountRequest(RouteOf(r.Pattern), sw.status())
	})
}

// statusWriter records the status code a handler sends. Unwrap lets
// http.NewResponseController reach the connection for per-write deadlines
// and flushing, and ReadFrom keeps net/http's sendfile and copy path for
// io.Copy (the web UI's file server), so streaming is unchanged.
type statusWriter struct {
	http.ResponseWriter
	code int
}

func (w *statusWriter) WriteHeader(code int) {
	if w.code == 0 && code >= 200 { // 1xx responses aren't final
		w.code = code
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusWriter) Write(p []byte) (int, error) {
	if w.code == 0 {
		w.code = http.StatusOK
	}
	return w.ResponseWriter.Write(p)
}

func (w *statusWriter) ReadFrom(src io.Reader) (int64, error) {
	if w.code == 0 {
		w.code = http.StatusOK
	}
	if rf, ok := w.ResponseWriter.(io.ReaderFrom); ok {
		return rf.ReadFrom(src)
	}
	return io.Copy(writerOnly{w.ResponseWriter}, src)
}

func (w *statusWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// status is the code the client got: net/http sends 200 for a handler that
// wrote nothing.
func (w *statusWriter) status() int {
	if w.code == 0 {
		return http.StatusOK
	}
	return w.code
}

// writerOnly hides any ReadFrom method, so io.Copy can't loop back into it.
type writerOnly struct{ io.Writer }
