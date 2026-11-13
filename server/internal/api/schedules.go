package api

import (
	"net/http"

	"github.com/redlinelabs-dev/dyno/server/internal/schedule"
)

// Scheduler is what the schedules API needs from the running scheduler
// (package scheduler).
type Scheduler interface {
	Changed()                        // schedules were added, edited or deleted
	RunNow(id int64) error           // queue a run of schedule id; store.ErrNotFound if there's none
	Status(id int64) schedule.Status // whether it is running or queued
}

// The schedules routes (docs/api.md, "Schedules").

func (a *API) listSchedules(w http.ResponseWriter, r *http.Request)  { notImplemented(w) }
func (a *API) getSchedule(w http.ResponseWriter, r *http.Request)    { notImplemented(w) }
func (a *API) checkFarEnd(w http.ResponseWriter, r *http.Request)    { notImplemented(w) }
func (a *API) addSchedule(w http.ResponseWriter, r *http.Request)    { notImplemented(w) }
func (a *API) patchSchedule(w http.ResponseWriter, r *http.Request)  { notImplemented(w) }
func (a *API) deleteSchedule(w http.ResponseWriter, r *http.Request) { notImplemented(w) }
func (a *API) runSchedule(w http.ResponseWriter, r *http.Request)    { notImplemented(w) }

// notImplemented answers a route whose handler isn't built yet.
func notImplemented(w http.ResponseWriter) {
	writeError(w, http.StatusNotImplemented, "not implemented")
}
