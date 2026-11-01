package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"math"
	"net/http"
	"strconv"
	"time"

	"github.com/redlinelabs-dev/dyno/server/internal/result"
	"github.com/redlinelabs-dev/dyno/server/internal/store"
)

const (
	maxResultBody = 256 << 10
	defaultLimit  = 50
	maxLimit      = 500
)

// addResult stores a client's result with the server's own byte counts for
// the same test and the figures the server derives (grade, RPM). Every call
// ends in exactly one result.store event (ADR-0020).
func (a *API) addResult(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	var sub result.Submission
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxResultBody))
	err := dec.Decode(&sub)
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

// listResults returns {"results": [...]}, newest first. ?limit=N (default 50,
// at most 500) and ?before=<id> page backwards; ?device=<id> keeps one
// device's results.
func (a *API) listResults(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	q := r.URL.Query()
	limit, before := defaultLimit, int64(0)
	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 {
			writeError(w, http.StatusBadRequest, "limit must be a positive integer")
			return
		}
		limit = min(n, maxLimit)
	}
	if v := q.Get("before"); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			writeError(w, http.StatusBadRequest, "before must be a result id")
			return
		}
		before = n
	}
	device := q.Get("device")
	if !result.ValidDevice(device) {
		writeError(w, http.StatusBadRequest, "device must be up to 64 letters, digits, - or _")
		return
	}
	list, err := a.Store.List(limit, before, device)
	if err != nil {
		a.log().LogAttrs(r.Context(), slog.LevelError, "result.list",
			slog.String("result", "failed"),
			slog.Int("status_code", http.StatusInternalServerError),
			slog.Float64("duration_ms", sinceMs(start)),
			slog.String("error.code", "store_read_failed"),
			slog.String("error.message", err.Error()))
		writeError(w, http.StatusInternalServerError, "could not read results")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"results": list})
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
