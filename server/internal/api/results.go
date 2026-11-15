package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/redlinelabs-dev/dyno/server/internal/result"
	"github.com/redlinelabs-dev/dyno/server/internal/store"
)

const (
	maxResultBody = 256 << 10
	defaultLimit  = 50
	maxLimit      = 10_000 // the list streams, so a long one costs no memory (ADR-0030)
)

// streamStall is how long one write of a streamed reply may take before the
// connection is dropped (ADR-0007), so a client that stops reading can't hold
// a handler, its connection and a page of rows for good.
const streamStall = 10 * time.Second

// addResult stores a client's result with the server's own byte counts for
// the same test and the figures the server derives (grade, RPM). Every call
// ends in exactly one result.store event (ADR-0020).
func (a *API) addResult(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	var sub result.Submission
	body := &objectBody{r: http.MaxBytesReader(w, r.Body, maxResultBody)}
	dec := json.NewDecoder(body)
	err := dec.Decode(&sub)
	if body.notObject(err) {
		const say = "body must be one JSON object"
		a.logStore(r.Context(), start, http.StatusBadRequest, nil, "invalid_json", errors.New(say))
		writeError(w, http.StatusBadRequest, say)
		return
	}
	if err == nil && dec.Decode(&struct{}{}) != io.EOF {
		err = errors.New("one JSON object only; found more after it")
	}
	if err != nil {
		code := "invalid_json"
		if _, ok := errors.AsType[*http.MaxBytesError](err); ok {
			code = "body_too_large"
		}
		a.logStore(r.Context(), start, http.StatusBadRequest, nil, code, err)
		writeError(w, http.StatusBadRequest, "body must be a JSON result under 256 KiB: "+err.Error())
		return
	}
	res := result.Result{At: time.Now().UTC().Truncate(time.Millisecond), Source: "client", Submission: sub}
	if err := res.Validate(); err != nil {
		a.logStore(r.Context(), start, http.StatusBadRequest, nil, "invalid_result", err)
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if down, up, ok := a.Runs.Take(sub.TestID); ok {
		res.ServerBytesDown, res.ServerBytesUp = down, up
	}
	res.Derive()
	res, err = a.Store.Add(res)
	if err != nil {
		a.logStore(r.Context(), start, http.StatusInternalServerError, &res, "store_write_failed", err)
		writeError(w, http.StatusInternalServerError, "could not store the result")
		return
	}
	a.Metrics.ObserveResult(res)
	a.logStore(r.Context(), start, http.StatusCreated, &res, "", nil)
	w.Header().Set("Location", "/api/v1/results/"+strconv.FormatInt(res.ID, 10))
	writeJSON(w, http.StatusCreated, res)
}

// logStore emits the result.store event. res is nil unless the result passed
// validation and was derived, so a rejected submission's figures (which can
// be out of range) and an ungraded "none" never reach the log; errCode is
// empty on success. A client's bad input is a warning; a failed write is an
// error, because the operator has to fix the disk or the database. The client
// label is left out: it is derived from the user agent.
func (a *API) logStore(ctx context.Context, start time.Time, status int, res *result.Result, errCode string, err error) {
	attrs := make([]slog.Attr, 0, 20)
	attrs = append(attrs,
		slog.String("result", outcome(err)),
		slog.Int("status_code", status),
		slog.Float64("duration_ms", sinceMs(start)),
		slog.String("source", "client"),
	)
	if res != nil {
		if res.ID != 0 {
			attrs = append(attrs, slog.Int64("result_id", res.ID))
		}
		grade := res.Grade
		if grade == "" {
			grade = "none"
		}
		attrs = append(attrs,
			slog.String("grade", grade),
			slog.Int64("download_bits_per_second", int64(math.Round(res.DownloadMbps*1e6))),
			slog.Int64("upload_bits_per_second", int64(math.Round(res.UploadMbps*1e6))),
			slog.Float64("idle_latency_ms", res.IdleMs),
			slog.Float64("loaded_latency_ms", max(res.LoadedDownMs, res.LoadedUpMs)),
			slog.Float64("jitter_ms", res.JitterMs),
			slog.Float64("bufferbloat_ms", res.BufferbloatMs),
			slog.Int64("server_bytes_sent", res.ServerBytesDown),
			slog.Int64("server_bytes_received", res.ServerBytesUp),
			slog.Bool("client_limited", res.ClientLimited),
			slog.Int("streams", res.Streams),
		)
	}
	level := slog.LevelInfo
	if err != nil {
		level = slog.LevelWarn
		if status >= 500 {
			level = slog.LevelError
		}
		attrs = append(attrs, slog.String("error.code", errCode), slog.String("error.message", err.Error()))
	}
	a.log().LogAttrs(ctx, level, "result.store", attrs...)
}

// listResults returns {"results": [...]}, newest first, without detail
// (docs/api.md, "GET /api/v1/results"). ?limit=N (default 50, at most 10,000)
// and ?before=<id> page backwards; ?device, ?from, ?to, ?source, ?schedule
// and ?outcome pick which results. The rows are sent as they are read, a page
// at a time, so the reply costs the same memory at any length.
//
// The first page is read before a byte is sent, so a store that fails at once
// is a proper 500. One that fails later can't change the 200 already sent:
// the handler aborts the reply, so the client sees a broken transfer, never a
// clean end with a JSON that looks like a shorter list.
func (a *API) listResults(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	q := r.URL.Query()
	f, err := parseResultFilter(q)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	f.Limit = defaultLimit
	if v := q.Get("limit"); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if ne, ok := errors.AsType[*strconv.NumError](err); ok && ne.Err == strconv.ErrRange {
			err = nil // too big for an int64: n is then the largest, and the cap applies
		}
		if err != nil || n < 1 {
			writeError(w, http.StatusBadRequest, "limit must be a positive integer")
			return
		}
		f.Limit = int(min(n, maxLimit))
	}
	if v := q.Get("before"); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			writeError(w, http.StatusBadRequest, "before must be a result id")
			return
		}
		f.Before = n
	}

	sw := newStreamWriter(w)
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	row := new(result.Result) // one for the whole reply, so no row is boxed on its own
	sent, gone := 0, false
	code := "store_read_failed"
	err = a.Store.Query(f, func(res result.Result) error {
		if err := r.Context().Err(); err != nil {
			gone = true
			return err
		}
		buf.Reset()
		if sent == 0 {
			buf.WriteString(`{"results":[`)
		} else {
			buf.WriteByte(',')
		}
		*row = res
		if err := enc.Encode(row); err != nil {
			code = "result_encode_failed"
			return err
		}
		buf.Truncate(buf.Len() - 1) // Encode ends the value with a newline
		if sent == 0 {
			h := w.Header()
			h.Set("Content-Type", "application/json")
			h.Set("Cache-Control", "no-store")
		}
		if _, err := sw.Write(buf.Bytes()); err != nil {
			gone = true
			return err
		}
		sent++
		return nil
	})
	switch {
	case err == nil && sent == 0:
		writeJSON(w, http.StatusOK, map[string]any{"results": []result.Result{}})
	case err == nil:
		buf.Reset()
		buf.WriteString("]}\n")
		sw.Write(buf.Bytes())
	case gone:
		// The client left or stopped reading: nobody is there to answer.
	case sent == 0:
		a.logReadFailure(r.Context(), "result.list", start, http.StatusInternalServerError, sent, code, err)
		writeError(w, http.StatusInternalServerError, "could not read results")
	default:
		a.logReadFailure(r.Context(), "result.list", start, http.StatusOK, sent, code, err)
		panic(http.ErrAbortHandler)
	}
}

// logReadFailure logs a failed read of the result list or the export, which
// ADR-0020 puts at ERROR: the database refused a read. status is what the
// client got, which is the 200 already sent when the failure came mid-reply.
func (a *API) logReadFailure(ctx context.Context, event string, start time.Time, status, sent int, code string, err error) {
	a.log().LogAttrs(ctx, slog.LevelError, event,
		slog.String("result", "failed"),
		slog.Int("status_code", status),
		slog.Float64("duration_ms", sinceMs(start)),
		slog.Int("sent_results", sent),
		slog.String("error.code", code),
		slog.String("error.message", err.Error()))
}

// parseResultFilter reads the parameters that pick which results a reply
// holds: device, from, to, source, schedule and outcome. limit and before are
// the list's own, so the export, which ignores them, doesn't read them. The
// error is the sentence for the 400.
func parseResultFilter(q url.Values) (store.Filter, error) {
	var f store.Filter
	f.Device = q.Get("device")
	if !result.ValidDevice(f.Device) {
		return f, errors.New("device must be up to 64 letters, digits, - or _")
	}
	from, haveFrom, err := timeParam(q, "from")
	if err != nil {
		return f, err
	}
	to, haveTo, err := timeParam(q, "to")
	if err != nil {
		return f, err
	}
	if haveFrom && haveTo && !to.After(from) {
		return f, errors.New("to must be after from")
	}
	if haveFrom {
		f.From = unzero(from)
	}
	if haveTo {
		f.To = unzero(to)
	}
	f.Source = q.Get("source")
	if f.Source != "" && f.Source != "client" && f.Source != "schedule" {
		return f, errors.New("source must be client or schedule")
	}
	if v := q.Get("schedule"); v != "" {
		id, err := strconv.ParseInt(v, 10, 64)
		if err != nil || id < 1 {
			return f, errors.New("schedule must be a schedule id")
		}
		f.Schedule = id
	}
	f.Outcome = q.Get("outcome")
	if f.Outcome != "" && f.Outcome != "succeeded" && f.Outcome != "failed" {
		return f, errors.New("outcome must be succeeded or failed")
	}
	return f, nil
}

// timeParam reads the RFC 3339 time in parameter name. present is false when
// the parameter is absent or empty.
func timeParam(q url.Values, name string) (t time.Time, present bool, err error) {
	v := q.Get(name)
	if v == "" {
		return t, false, nil
	}
	t, err = time.Parse(time.RFC3339, v)
	if err != nil {
		return t, false, errors.New(name + " must be a time in RFC 3339, like 2026-11-13T00:00:00Z (write + as %2B in an offset)")
	}
	return t, true, nil
}

// unzero keeps a time a client wrote as year 1 from reading as the store's
// zero time, which means no bound at all.
func unzero(t time.Time) time.Time {
	if t.IsZero() {
		return t.Add(time.Millisecond)
	}
	return t
}

// streamWriter sends a long reply in pieces, each given streamStall to be
// taken. net/http clears the write deadline after each request, so the next
// request on the connection doesn't inherit it.
type streamWriter struct {
	w  http.ResponseWriter
	rc *http.ResponseController
}

func newStreamWriter(w http.ResponseWriter) *streamWriter {
	return &streamWriter{w: w, rc: http.NewResponseController(w)}
}

func (s *streamWriter) Write(p []byte) (int, error) {
	s.rc.SetWriteDeadline(time.Now().Add(streamStall)) // not supported by every writer, and then not needed
	return s.w.Write(p)
}

// getResult returns one result including its detail samples.
func (a *API) getResult(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "id must be an integer")
		return
	}
	res, err := a.Store.Get(id)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "no result with that id")
		return
	}
	if err != nil {
		a.log().LogAttrs(r.Context(), slog.LevelError, "result.get",
			slog.String("result", "failed"),
			slog.Int("status_code", http.StatusInternalServerError),
			slog.Float64("duration_ms", sinceMs(start)),
			slog.Int64("result_id", id),
			slog.String("error.code", "store_read_failed"),
			slog.String("error.message", err.Error()))
		writeError(w, http.StatusInternalServerError, "could not read the result")
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func outcome(err error) string {
	if err != nil {
		return "failed"
	}
	return "succeeded"
}

// sinceMs is the time since start in milliseconds, to the microsecond.
func sinceMs(start time.Time) float64 {
	return float64(time.Since(start).Microseconds()) / 1e3
}
