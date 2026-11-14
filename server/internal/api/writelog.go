package api

import (
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/redlinelabs-dev/dyno/server/internal/nodetest"
)

// The error.code of a failed outcome event (ADR-0020). A far end that fails
// its check logs the check's own code, which isn't listed here.
const (
	codeJSON        = "invalid_json"
	codeTooLarge    = "body_too_large"
	codeSchedule    = "invalid_schedule"
	codeSettings    = "invalid_settings"
	codeID          = "invalid_id"
	codeNotFound    = "not_found"
	codeLimit       = "schedule_limit"
	codeTooSoon     = "too_soon"
	codeBusy        = "checks_busy"
	codeNoCheck     = "checks_unavailable"
	codeNoScheduler = "scheduler_off"
	codeRead        = "store_read_failed"
	codeWrite       = "store_write_failed"
	codeCheck       = "check_failed"
	codeRun         = "run_failed"
)

// event is the one outcome event of a request (ADR-0020). A handler makes it at
// the top, and every path out of the handler ends in exactly one call to ok,
// refuse, farEndFailed or fail. Each logs the event and then answers, so a
// client that has its reply can already read the event, and no handler logs on
// its own.
type event struct {
	a     *API
	w     http.ResponseWriter
	r     *http.Request
	name  string    // domain.action, like schedule.add
	start time.Time // when the handler began, for duration_ms
	reads bool      // a read: logged only when it ends in a 500
	id    int64     // the schedule the path names, when it holds a positive integer
}

// writeEvent starts the event of a write, which logs on every outcome.
func (a *API) writeEvent(w http.ResponseWriter, r *http.Request, name string) *event {
	return &event{a: a, w: w, r: r, name: name, start: time.Now()}
}

// readEvent starts the event of a read, which is logged only when the server
// fails it with a 500, not on every page load.
func (a *API) readEvent(w http.ResponseWriter, r *http.Request, name string) *event {
	ev := a.writeEvent(w, r, name)
	ev.reads = true
	return ev
}

// setID notes the schedule the request is about. A number that can't be a
// schedule's id, 0 or below, is left out of the event.
func (ev *event) setID(id int64) {
	if id > 0 {
		ev.id = id
	}
}

// scheduleID reads the {id} of the path. When it isn't an integer, the event
// has ended with a 400 and ok is false.
func (ev *event) scheduleID() (int64, bool) {
	id, err := strconv.ParseInt(ev.r.PathValue("id"), 10, 64)
	if err != nil {
		ev.refuse(http.StatusBadRequest, codeID, "id must be an integer")
		return 0, false
	}
	ev.setID(id)
	return id, true
}

// ok ends the event with a success and answers status with body as JSON, or
// with no body at all when it is nil. attrs say what the request changed.
func (ev *event) ok(status int, body any, attrs ...slog.Attr) {
	ev.record(status, "", "", attrs)
	if body == nil {
		ev.w.WriteHeader(status)
		return
	}
	writeJSON(ev.w, status, body)
}

// refuse ends the event with a failure that isn't the server's: bad input, an
// unknown schedule, a limit, or a service this server can't give just now. It
// answers {"error": msg}, and msg is also the event's error.message.
func (ev *event) refuse(status int, code, msg string) {
	ev.record(status, code, msg, nil)
	writeError(ev.w, status, msg)
}

// farEndFailed ends the event with the 422 of a far end that failed its
// check, under the check's own code.
func (ev *event) farEndFailed(ne *nodetest.Error) {
	msg := ne.Error()
	ev.record(http.StatusUnprocessableEntity, ne.Code, msg, nil)
	writeJSON(ev.w, http.StatusUnprocessableEntity, checkFailure{Error: msg, Code: ne.Code})
}

// fail ends the event with a failure on the server's side. The log gets err's
// text; the client gets say, a plain sentence without it.
func (ev *event) fail(code, say string, err error, attrs ...slog.Attr) {
	ev.record(http.StatusInternalServerError, code, err.Error(), attrs)
	writeError(ev.w, http.StatusInternalServerError, say)
}

// record logs the event. A failure carries error.code and error.message, and
// extra is whatever else the outcome says. A read logs only a 500.
func (ev *event) record(status int, code, message string, extra []slog.Attr) {
	level := levelOf(status)
	if ev.reads && level != slog.LevelError {
		return
	}
	verdict := "succeeded"
	if status >= http.StatusBadRequest {
		verdict = "failed"
	}
	attrs := make([]slog.Attr, 0, 6+len(extra))
	attrs = append(attrs,
		slog.String("result", verdict),
		slog.Int("status_code", status),
		slog.Float64("duration_ms", sinceMs(ev.start)),
	)
	if ev.id > 0 {
		attrs = append(attrs, slog.Int64("schedule_id", ev.id))
	}
	if status >= http.StatusBadRequest {
		attrs = append(attrs, slog.String("error.code", code), slog.String("error.message", message))
	}
	attrs = append(attrs, extra...)
	ev.a.log().LogAttrs(ev.r.Context(), level, ev.name, attrs...)
}

// levelOf is the level ADR-0020 gives a reply: INFO for a success, ERROR for a
// 500, which the operator has to act on, and WARN for every other failure.
func levelOf(status int) slog.Level {
	switch {
	case status < http.StatusBadRequest:
		return slog.LevelInfo
	case status == http.StatusInternalServerError:
		return slog.LevelError
	}
	return slog.LevelWarn
}

// bodyCode is the error.code of a body the decoder refused: body_too_large
// when it ran past its limit, invalid_json for anything else.
func bodyCode(err error) string {
	if _, ok := errors.AsType[*http.MaxBytesError](err); ok {
		return codeTooLarge
	}
	return codeJSON
}
