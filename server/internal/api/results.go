package api

import (
	"encoding/json"
	"errors"
	"log"
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
// the same test and the figures the server derives (grade, RPM).
func (a *API) addResult(w http.ResponseWriter, r *http.Request) {
	var sub result.Submission
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxResultBody))
	if err := dec.Decode(&sub); err != nil {
		writeError(w, http.StatusBadRequest, "body must be a JSON result under 256 KiB: "+err.Error())
		return
	}
	if err := sub.Validate(); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	res := result.Result{At: time.Now().UTC().Truncate(time.Millisecond), Source: "client", Submission: sub}
	if down, up, ok := a.Runs.Take(sub.TestID); ok {
		res.ServerBytesDown, res.ServerBytesUp = down, up
	}
	res.Derive()
	res, err := a.Store.Add(res)
	if err != nil {
		log.Printf("storing result: %v", err)
		writeError(w, http.StatusInternalServerError, "could not store the result")
		return
	}
	w.Header().Set("Location", "/api/v1/results/"+strconv.FormatInt(res.ID, 10))
	writeJSON(w, http.StatusCreated, res)
}

// listResults returns {"results": [...]}, newest first. ?limit=N (default 50,
// at most 500) and ?before=<id> page backwards.
func (a *API) listResults(w http.ResponseWriter, r *http.Request) {
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
	list, err := a.Store.List(limit, before)
	if err != nil {
		log.Printf("listing results: %v", err)
		writeError(w, http.StatusInternalServerError, "could not read results")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"results": list})
}

// getResult returns one result including its detail samples.
func (a *API) getResult(w http.ResponseWriter, r *http.Request) {
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
		log.Printf("reading result %d: %v", id, err)
		writeError(w, http.StatusInternalServerError, "could not read the result")
		return
	}
	writeJSON(w, http.StatusOK, res)
}
