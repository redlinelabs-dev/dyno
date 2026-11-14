package api

import (
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/redlinelabs-dev/dyno/server/internal/result"
)

// csvHeader is the export's first row, in the order appendCSVRow writes the
// columns. Lines end in CRLF, as RFC 4180 has it.
const csvHeader = "id,at,source,schedule_id,far_end,device,client,download_mbps,upload_mbps,idle_ms," +
	"loaded_down_ms,loaded_up_ms,jitter_ms,bufferbloat_ms,grade,rpm,streams,client_limited,path_changed," +
	"failure_code,failure_phase,failure_message,server_bytes_down,server_bytes_up\r\n"

// exportResults sends results as CSV (docs/api.md, "GET /api/v1/results.csv"):
// every result the filters select, oldest first, a page at a time, so the
// reply costs the same memory at any length.
//
// The first page is read before a byte is sent, so a store that fails at once
// is a proper 500. A CSV has no end marker, so one that stopped short would
// pass for a complete export. When the store fails later, the handler aborts
// the connection instead: the client sees a transfer that broke off.
func (a *API) exportResults(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	f, err := parseResultFilter(r.URL.Query())
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	f.Oldest = true

	sw := newStreamWriter(w)
	row := make([]byte, 0, 512)
	sent, gone := 0, false
	headers := func() {
		h := w.Header()
		h.Set("Content-Type", "text/csv; charset=utf-8")
		h.Set("Content-Disposition", `attachment; filename="dyno-results.csv"`)
		h.Set("Cache-Control", "no-store")
	}
	err = a.Store.Query(f, func(res result.Result) error {
		if err := r.Context().Err(); err != nil {
			gone = true
			return err
		}
		row = row[:0]
		if sent == 0 {
			headers()
			row = append(row, csvHeader...)
		}
		row = appendCSVRow(row, &res)
		if _, err := sw.Write(row); err != nil {
			gone = true
			return err
		}
		sent++
		return nil
	})
	switch {
	case err == nil && sent == 0:
		headers()
		sw.Write([]byte(csvHeader))
	case err == nil, gone:
		// Done, or the client left or stopped reading: nobody is there to answer.
	case sent == 0:
		a.logReadFailure(r.Context(), "result.export", start, http.StatusInternalServerError, sent, "store_read_failed", err)
		writeError(w, http.StatusInternalServerError, "could not read results")
	default:
		a.logReadFailure(r.Context(), "result.export", start, http.StatusOK, sent, "store_read_failed", err)
		panic(http.ErrAbortHandler)
	}
}

// appendCSVRow appends r as one CSV row, in csvHeader's order. Text cells go
// through appendCSVText; numbers, booleans and the time need no quoting and
// no guard, and a number that starts with "-" must stay a number.
func appendCSVRow(b []byte, r *result.Result) []byte {
	var fail result.Failure
	if r.Failure != nil {
		fail = *r.Failure
	}
	b = strconv.AppendInt(b, r.ID, 10)
	b = append(b, ',')
	b = r.At.UTC().AppendFormat(b, time.RFC3339Nano) // as the JSON writes it
	b = append(b, ',')
	b = appendCSVText(b, r.Source)
	b = append(b, ',')
	if r.ScheduleID != 0 {
		b = strconv.AppendInt(b, r.ScheduleID, 10)
	}
	b = append(b, ',')
	b = appendCSVText(b, r.FarEnd)
	b = append(b, ',')
	b = appendCSVText(b, r.Device)
	b = append(b, ',')
	b = appendCSVText(b, r.Client)
	for _, v := range [...]float64{r.DownloadMbps, r.UploadMbps, r.IdleMs, r.LoadedDownMs, r.LoadedUpMs, r.JitterMs, r.BufferbloatMs} {
		b = append(b, ',')
		b = strconv.AppendFloat(b, v, 'f', -1, 64)
	}
	b = append(b, ',')
	b = appendCSVText(b, r.Grade)
	b = append(b, ',')
	b = strconv.AppendInt(b, int64(r.RPM), 10)
	b = append(b, ',')
	b = strconv.AppendInt(b, int64(r.Streams), 10)
	b = append(b, ',')
	b = strconv.AppendBool(b, r.ClientLimited)
	b = append(b, ',')
	b = strconv.AppendBool(b, r.PathChanged)
	b = append(b, ',')
	b = appendCSVText(b, fail.Code)
	b = append(b, ',')
	b = appendCSVText(b, fail.Phase)
	b = append(b, ',')
	b = appendCSVText(b, fail.Message)
	b = append(b, ',')
	b = strconv.AppendInt(b, r.ServerBytesDown, 10)
	b = append(b, ',')
	b = strconv.AppendInt(b, r.ServerBytesUp, 10)
	return append(b, '\r', '\n')
}

// appendCSVText appends s as one text cell. A cell a spreadsheet would read
// as a formula, one that starts with =, +, -, @, a tab or a carriage return,
// gets a leading ' (ADR-0030). A cell with a comma, a double quote or a line
// break is quoted, with its quotes doubled (RFC 4180). This is written out
// rather than left to encoding/csv, whose writer drops a carriage return
// from a field when it ends lines in CRLF.
func appendCSVText(b []byte, s string) []byte {
	if s == "" {
		return b
	}
	s = strings.ToValidUTF8(s, string(utf8.RuneError))
	formula := strings.IndexByte("=+-@\t\r", s[0]) >= 0
	quote := strings.ContainsAny(s, ",\"\r\n")
	if quote {
		b = append(b, '"')
	}
	if formula {
		b = append(b, '\'')
	}
	if !quote {
		return append(b, s...)
	}
	for i := 0; i < len(s); i++ {
		if s[i] == '"' {
			b = append(b, '"')
		}
		b = append(b, s[i])
	}
	return append(b, '"')
}
