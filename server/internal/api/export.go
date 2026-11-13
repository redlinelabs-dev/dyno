package api

import "net/http"

// exportResults sends results as CSV (docs/api.md, "GET /api/v1/results.csv").
func (a *API) exportResults(w http.ResponseWriter, r *http.Request) { notImplemented(w) }
