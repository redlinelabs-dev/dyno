package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"reflect"
	"strconv"
	"strings"
	"time"

	"github.com/redlinelabs-dev/dyno/server/internal/nodetest"
	"github.com/redlinelabs-dev/dyno/server/internal/result"
	"github.com/redlinelabs-dev/dyno/server/internal/schedule"
	"github.com/redlinelabs-dev/dyno/server/internal/store"
)

// Scheduler is what the schedules API needs from the running scheduler
// (package scheduler).
type Scheduler interface {
	Changed()                        // schedules were added, edited or deleted
	RunNow(id int64) error           // queue a run of schedule id; store.ErrNotFound if there's none
	Status(id int64) schedule.Status // whether it is running or queued
}

const (
	maxScheduleBody = 4 << 10          // bytes in a request body
	maxChecks       = 4                // far-end checks that run at once, across every route
	checkTimeout    = 10 * time.Second // the longest one far-end check may take
	runCooldown     = time.Minute      // how long after a schedule's last result /run is refused
	noSchedule      = "no schedule with that id"
	codeStopping    = "shutting_down" // error.code of a check cut off because the server is stopping
)

// What a client is told when the server fails: a plain sentence, never the
// error's own text, which goes to the log (ADR-0020).
const (
	sayRead  = "could not read the schedules"
	sayWrite = "could not save the schedule"
	sayCheck = "could not check the far end"
	sayRun   = "could not queue the run"
)

// scheduleView is a schedule as the API returns it (docs/api.md): its
// settings, when it runs next, what the scheduler is doing with it, and its
// latest results.
type scheduleView struct {
	schedule.Schedule
	NextRun       *time.Time     `json:"next_run"`
	Running       bool           `json:"running"`
	Queued        bool           `json:"queued"`
	LastResult    *result.Result `json:"last_result"`
	LastSuccessAt *time.Time     `json:"last_success_at"`
}

// newView is sc with its next run and the scheduler's status, which need no
// read of the store.
func (a *API) newView(sc schedule.Schedule, now time.Time) scheduleView {
	v := scheduleView{Schedule: sc}
	if !sc.Paused {
		next := sc.Next(now)
		v.NextRun = &next
	}
	if a.Scheduler != nil {
		st := a.Scheduler.Status(sc.ID)
		v.Running, v.Queued = st.Running, st.Queued
	}
	return v
}

// addResults fills in v's last_result and last_success_at from the store.
func (a *API) addResults(v *scheduleView) error {
	last, err := a.Store.LastResult(v.ID)
	if errors.Is(err, store.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	v.LastResult = &last
	if last.Failure == nil { // the newest result is also the newest success
		v.LastSuccessAt = &last.At
		return nil
	}
	good, err := a.Store.LastSuccess(v.ID)
	if errors.Is(err, store.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	v.LastSuccessAt = &good.At
	return nil
}

// answer ends ev with sc as the API returns it, or with a 500 when its latest
// results can't be read. attrs go in the event either way: a change that was
// saved is recorded as saved even when the reply to it fails.
func (a *API) answer(ev *event, status int, sc schedule.Schedule, attrs ...slog.Attr) {
	v := a.newView(sc, time.Now())
	if err := a.addResults(&v); err != nil {
		ev.fail(codeRead, sayRead, err, attrs...)
		return
	}
	ev.ok(status, v, attrs...)
}

// savedAttrs is what an add or an update says it changed: the schedule as
// saved, without its name, which is free text (ADR-0020).
func savedAttrs(sc schedule.Schedule) []slog.Attr {
	return []slog.Attr{
		slog.String("far_end", sc.FarEnd),
		slog.String("kind", sc.Kind),
		slog.Int("every_minutes", sc.EveryMinutes),
		slog.Int("offset_minutes", sc.OffsetMinutes),
		slog.Bool("paused", sc.Paused),
	}
}

// changed tells the scheduler that schedules were added, edited or deleted.
func (a *API) changed() {
	if a.Scheduler != nil {
		a.Scheduler.Changed()
	}
}

// readBody decodes the request's one JSON object into dst. It returns the
// error.code to log and what to tell the client when the body can't be used,
// and "" for both when it can.
func readBody(w http.ResponseWriter, r *http.Request, dst any) (code, msg string) {
	body := &objectBody{r: http.MaxBytesReader(w, r.Body, maxScheduleBody)}
	dec := json.NewDecoder(body)
	dec.DisallowUnknownFields()
	err := dec.Decode(dst)
	if body.notObject(err) {
		return codeJSON, "the body must be one JSON object"
	}
	if err != nil {
		return bodyCode(err), bodyProblem(err)
	}
	var more json.RawMessage
	if err := dec.Decode(&more); err != io.EOF {
		if _, ok := errors.AsType[*http.MaxBytesError](err); ok {
			return codeTooLarge, bodyProblem(err)
		}
		return codeJSON, "the body must be one JSON object, with nothing after it"
	}
	return "", ""
}

// objectBody passes a request body through to a decoder and remembers its
// first byte that isn't white space. Go's decoder takes null into a struct as
// a no-op, so without it a body of null would pass as an empty object.
type objectBody struct {
	r     io.Reader
	first byte // 0 until a byte that isn't white space has been read
}

func (b *objectBody) Read(p []byte) (int, error) {
	n, err := b.r.Read(p)
	if b.first == 0 {
		for _, c := range p[:n] {
			if c != ' ' && c != '\t' && c != '\r' && c != '\n' {
				b.first = c
				break
			}
		}
	}
	return n, err
}

// notObject reports whether the body started with something other than an
// object, once the decoder has taken its first value and returned err. A body
// that is too large is left to say so, and an empty one has no first byte.
func (b *objectBody) notObject(err error) bool {
	if _, ok := errors.AsType[*http.MaxBytesError](err); ok {
		return false
	}
	return b.first != 0 && b.first != '{'
}

// bodyProblem says in a plain sentence why the decoder refused a body. It
// names the field when there is one, and leaves out Go's own wording.
func bodyProblem(err error) string {
	if _, ok := errors.AsType[*http.MaxBytesError](err); ok {
		return fmt.Sprintf("the body must be at most %d KiB", maxScheduleBody>>10)
	}
	if te, ok := errors.AsType[*json.UnmarshalTypeError](err); ok && te.Field != "" {
		return te.Field + " must be " + wantType(te.Type)
	}
	if name, ok := strings.CutPrefix(err.Error(), "json: unknown field "); ok {
		return "unknown field " + name
	}
	if errors.Is(err, io.EOF) {
		return "the body is empty; send a JSON object"
	}
	return "the body must be one JSON object"
}

// wantType is how a body problem says what type a field takes.
func wantType(t reflect.Type) string {
	switch t.Kind() {
	case reflect.String:
		return "a string"
	case reflect.Bool:
		return "true or false"
	case reflect.Int:
		return "a whole number"
	}
	return "another kind of value"
}

// invalidInput is an error about what the client sent, safe to show it.
type invalidInput struct{ error }

// saveError is an error of the store's write, as opposed to one of the reads
// before it: the first is logged as store_write_failed, the others as
// store_read_failed.
type saveError struct{ error }

func (e saveError) Unwrap() error { return e.error }

// checkFailure is the 422 body of a far end that failed its check.
type checkFailure struct {
	Error string `json:"error"`
	Code  string `json:"code"`
}

// checkReply is the 200 body of a far end that passed.
type checkReply struct {
	FarEnd  string `json:"far_end"`
	Name    string `json:"name"`
	Version string `json:"version"`
}

// takeCheck claims one of the maxChecks places for a check, without waiting.
func (a *API) takeCheck() bool {
	for {
		n := a.checks.Load()
		if n >= maxChecks {
			return false
		}
		if a.checks.CompareAndSwap(n, n+1) {
			return true
		}
	}
}

// check runs Check on farEnd, a normalized address, and reports whether the
// far end answered like a dyno. When it didn't, check has ended ev: 503 when
// this server can't check, maxChecks run already or the server is stopping, 422
// when the far end failed, and 500 for anything else.
func (a *API) check(ev *event, farEnd string) (nodetest.FarEndInfo, bool) {
	if a.Check == nil {
		ev.refuse(http.StatusServiceUnavailable, codeNoCheck, "this server can't check far ends")
		return nodetest.FarEndInfo{}, false
	}
	if !a.takeCheck() {
		ev.w.Header().Set("Retry-After", "1")
		ev.refuse(http.StatusServiceUnavailable, codeBusy,
			fmt.Sprintf("%d far-end checks are running already; try again in a second", maxChecks))
		return nodetest.FarEndInfo{}, false
	}
	defer a.checks.Add(-1)
	ctx, cancel := context.WithTimeout(ev.r.Context(), checkTimeout)
	defer cancel()
	info, err := a.Check(ctx, farEnd)
	if err == nil {
		return info, true
	}
	if ne, ok := errors.AsType[*nodetest.Error](err); ok && ne != nil { // not a nil *Error wrapped in an error
		// Cancelled while the request is still there and the timeout hasn't
		// passed: the server is stopping, and the far end did nothing wrong.
		if ne.Code == nodetest.CodeCanceled && ev.r.Context().Err() == nil {
			ev.w.Header().Set("Retry-After", "1")
			ev.refuse(http.StatusServiceUnavailable, codeStopping, "the server is stopping; try again in a moment")
			return nodetest.FarEndInfo{}, false
		}
		ev.farEndFailed(ne)
		return nodetest.FarEndInfo{}, false
	}
	ev.fail(codeCheck, sayCheck, err)
	return nodetest.FarEndInfo{}, false
}

// The schedules routes (docs/api.md, "Schedules").

// listSchedules returns {"schedules": [...]}, oldest first.
func (a *API) listSchedules(w http.ResponseWriter, r *http.Request) {
	ev := a.readEvent(w, r, "schedule.list")
	list, err := a.Store.Schedules()
	if err != nil {
		ev.fail(codeRead, sayRead, err)
		return
	}
	now := time.Now()
	views := make([]scheduleView, 0, len(list))
	for _, sc := range list {
		v := a.newView(sc, now)
		if err := a.addResults(&v); err != nil {
			ev.setID(sc.ID)
			ev.fail(codeRead, sayRead, err)
			return
		}
		views = append(views, v)
	}
	ev.ok(http.StatusOK, map[string]any{"schedules": views})
}

// getSchedule returns one schedule.
func (a *API) getSchedule(w http.ResponseWriter, r *http.Request) {
	ev := a.readEvent(w, r, "schedule.get")
	id, ok := ev.scheduleID()
	if !ok {
		return
	}
	sc, err := a.Store.Schedule(id)
	if errors.Is(err, store.ErrNotFound) {
		ev.refuse(http.StatusNotFound, codeNotFound, noSchedule)
		return
	}
	if err != nil {
		ev.fail(codeRead, sayRead, err)
		return
	}
	a.answer(ev, http.StatusOK, sc)
}

// checkFarEnd asks whether a dyno answers at an address and saves nothing.
func (a *API) checkFarEnd(w http.ResponseWriter, r *http.Request) {
	ev := a.readEvent(w, r, "schedule.check")
	var body struct {
		FarEnd string `json:"far_end"`
	}
	if code, msg := readBody(w, r, &body); msg != "" {
		ev.refuse(http.StatusBadRequest, code, msg)
		return
	}
	farEnd, err := schedule.NormalizeFarEnd(body.FarEnd)
	if err != nil {
		ev.refuse(http.StatusBadRequest, codeSchedule, err.Error())
		return
	}
	info, ok := a.check(ev, farEnd)
	if !ok {
		return
	}
	ev.ok(http.StatusOK, checkReply{FarEnd: farEnd, Name: info.Name, Version: info.Version})
}

// addSchedule checks the far end, then stores the schedule. A bad body is
// refused first, then a full store, so neither costs a check.
func (a *API) addSchedule(w http.ResponseWriter, r *http.Request) {
	ev := a.writeEvent(w, r, "schedule.add")
	var body schedule.Patch
	if code, msg := readBody(w, r, &body); msg != "" {
		ev.refuse(http.StatusBadRequest, code, msg)
		return
	}
	sc := schedule.Schedule{}.Apply(body)
	if err := sc.Validate(); err != nil {
		ev.refuse(http.StatusBadRequest, codeSchedule, err.Error())
		return
	}
	existing, err := a.Store.Schedules()
	if err != nil {
		ev.fail(codeRead, sayRead, err)
		return
	}
	full := fmt.Sprintf("%d schedules exist already; delete one to add another", schedule.MaxSchedules)
	if len(existing) >= schedule.MaxSchedules {
		ev.refuse(http.StatusConflict, codeLimit, full)
		return
	}
	if _, ok := a.check(ev, sc.FarEnd); !ok {
		return
	}
	sc, err = a.Store.AddSchedule(sc)
	if errors.Is(err, store.ErrFull) { // another add took the last place during the check
		ev.refuse(http.StatusConflict, codeLimit, full)
		return
	}
	if err != nil {
		ev.fail(codeWrite, sayWrite, err)
		return
	}
	a.changed()
	// A new schedule has no results, so there is nothing left to read that
	// could fail after the schedule is stored.
	ev.setID(sc.ID)
	w.Header().Set("Location", "/api/v1/schedules/"+strconv.FormatInt(sc.ID, 10))
	ev.ok(http.StatusCreated, a.newView(sc, time.Now()), savedAttrs(sc)...)
}

// merge reads schedule id and applies p to it, returning the schedule as it
// was and as it would be. The error is store.ErrNotFound, an invalidInput
// when the result doesn't validate, or the store's own.
func (a *API) merge(id int64, p schedule.Patch) (cur, merged schedule.Schedule, err error) {
	cur, err = a.Store.Schedule(id)
	if err != nil {
		return cur, cur, err
	}
	merged = cur.Apply(p)
	if err := merged.Validate(); err != nil {
		return cur, merged, invalidInput{err}
	}
	return cur, merged, nil
}

// mergeAndSave applies p to schedule id as it is now and stores the result.
// One patch does it at a time, so two changes to different fields both land.
// An error of the store's write is a saveError.
func (a *API) mergeAndSave(id int64, p schedule.Patch) (schedule.Schedule, error) {
	a.patchMu.Lock()
	defer a.patchMu.Unlock()
	_, merged, err := a.merge(id, p)
	if err != nil {
		return schedule.Schedule{}, err
	}
	saved, err := a.Store.UpdateSchedule(merged)
	if err != nil {
		return schedule.Schedule{}, saveError{err}
	}
	return saved, nil
}

// patchFailed ends ev for a PATCH that failed: 400 when the patch doesn't
// validate, 404 when the schedule is gone, 500 otherwise. A 500 tells the
// client say, and logs store_write_failed for an error of the write and
// store_read_failed for any other.
func patchFailed(ev *event, err error, say string) {
	if bad, ok := errors.AsType[invalidInput](err); ok {
		ev.refuse(http.StatusBadRequest, codeSchedule, bad.Error())
		return
	}
	if errors.Is(err, store.ErrNotFound) {
		ev.refuse(http.StatusNotFound, codeNotFound, noSchedule)
		return
	}
	code := codeRead
	if _, ok := errors.AsType[saveError](err); ok {
		code = codeWrite
	}
	ev.fail(code, say, err)
}

// patchSchedule changes the fields sent. Only a changed far end is checked.
func (a *API) patchSchedule(w http.ResponseWriter, r *http.Request) {
	ev := a.writeEvent(w, r, "schedule.update")
	id, ok := ev.scheduleID()
	if !ok {
		return
	}
	var p schedule.Patch
	if code, msg := readBody(w, r, &p); msg != "" {
		ev.refuse(http.StatusBadRequest, code, msg)
		return
	}
	cur, merged, err := a.merge(id, p)
	if err != nil {
		patchFailed(ev, err, sayRead)
		return
	}
	if p.FarEnd != nil && merged.FarEnd != cur.FarEnd {
		if _, ok := a.check(ev, merged.FarEnd); !ok {
			return
		}
	}
	// A check can take seconds, and another change may have landed meanwhile:
	// merge again, so this patch changes only what it carries.
	updated, err := a.mergeAndSave(id, p)
	if err != nil {
		patchFailed(ev, err, sayWrite)
		return
	}
	a.changed()
	a.answer(ev, http.StatusOK, updated, savedAttrs(updated)...)
}

// deleteSchedule deletes a schedule and keeps its results.
func (a *API) deleteSchedule(w http.ResponseWriter, r *http.Request) {
	ev := a.writeEvent(w, r, "schedule.delete")
	id, ok := ev.scheduleID()
	if !ok {
		return
	}
	err := a.Store.DeleteSchedule(id)
	if errors.Is(err, store.ErrNotFound) {
		ev.refuse(http.StatusNotFound, codeNotFound, noSchedule)
		return
	}
	if err != nil {
		ev.fail(codeWrite, sayWrite, err)
		return
	}
	a.changed()
	ev.ok(http.StatusNoContent, nil)
}

// runSchedule queues a run now, paused or not. A schedule whose last result
// is under a minute old is refused, so a client in a loop can't hammer a far
// end (ADR-0029).
func (a *API) runSchedule(w http.ResponseWriter, r *http.Request) {
	ev := a.writeEvent(w, r, "schedule.run_now")
	id, ok := ev.scheduleID()
	if !ok {
		return
	}
	if a.Scheduler == nil {
		ev.refuse(http.StatusServiceUnavailable, codeNoScheduler, "scheduled tests aren't running on this server")
		return
	}
	sc, err := a.Store.Schedule(id)
	if errors.Is(err, store.ErrNotFound) {
		ev.refuse(http.StatusNotFound, codeNotFound, noSchedule)
		return
	}
	if err != nil {
		ev.fail(codeRead, sayRead, err)
		return
	}
	last, err := a.Store.LastResult(id)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		ev.fail(codeRead, sayRead, err)
		return
	}
	if err == nil {
		// A result dated ahead of now, after the clock was set back, would
		// otherwise hold the client off for longer than the minute.
		if left := runCooldown - max(time.Since(last.At), 0); left > 0 {
			secs := int((left + time.Second - 1) / time.Second)
			w.Header().Set("Retry-After", strconv.Itoa(secs))
			ev.refuse(http.StatusTooManyRequests, codeTooSoon,
				fmt.Sprintf("this schedule's last result is less than a minute old; try again in %d s", secs))
			return
		}
	}
	err = a.Scheduler.RunNow(id)
	if errors.Is(err, store.ErrNotFound) {
		ev.refuse(http.StatusNotFound, codeNotFound, noSchedule)
		return
	}
	if err != nil {
		ev.fail(codeRun, sayRun, err)
		return
	}
	a.answer(ev, http.StatusAccepted, sc)
}
