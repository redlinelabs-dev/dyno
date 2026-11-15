package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"

	"github.com/redlinelabs-dev/dyno/server/internal/retention"
	"github.com/redlinelabs-dev/dyno/server/internal/settings"
)

// maxSettingsBody is the largest PATCH body, the same 4 KiB as a schedule's.
const maxSettingsBody = 4 << 10

// The settings routes (docs/api.md, "Settings").

// getSettings returns every setting. Like the other reads it logs only when
// the database fails (ADR-0020).
func (a *API) getSettings(w http.ResponseWriter, r *http.Request) {
	ev := a.readEvent(w, r, "settings.get")
	s, err := a.Store.Settings()
	if err != nil {
		ev.fail(codeRead, "could not read the settings", err)
		return
	}
	ev.ok(http.StatusOK, s)
}

// patchSettings changes the settings sent and replies with every one. A bad
// body is a 400 that names the field and changes nothing. When keep_days was
// sent, the results it now makes too old are deleted before the reply, and
// that prune logs its own results.prune event beside the PATCH's one.
func (a *API) patchSettings(w http.ResponseWriter, r *http.Request) {
	ev := a.writeEvent(w, r, "settings.patch")
	p, code, err := decodeSettingsPatch(w, r)
	if err != nil {
		ev.refuse(http.StatusBadRequest, code, err.Error())
		return
	}
	s, err := a.Store.PatchSettings(p)
	if err != nil {
		ev.fail(codeWrite, "could not save the settings", err)
		return
	}
	if p.KeepDays != nil {
		// The settings are saved whatever this does (ADR-0030): a failed prune
		// is logged, and the hourly one tries again. Shutdown never cancels a
		// handler's context, so the prune also ends when a.Stop does: it stops
		// between batches, and this still answers 200 with what was saved.
		ctx, cancel := context.WithCancel(r.Context())
		defer cancel()
		if a.Stop != nil {
			if a.Stop.Err() != nil {
				cancel() // AfterFunc would run this on another goroutine, after the prune began
			}
			defer context.AfterFunc(a.Stop, cancel)()
		}
		retention.Sweep(ctx, a.Store, a.log())
	}
	ev.ok(http.StatusOK, s, sentAttrs(p, s)...)
}

// sentAttrs is what a settings.patch event says changed: each setting the body
// sent, under its JSON name, with the value it has now, and no other.
func sentAttrs(p settings.Patch, s settings.Settings) []slog.Attr {
	var attrs []slog.Attr
	if p.PlanDownMbps != nil {
		attrs = append(attrs, slog.Float64("plan_down_mbps", s.PlanDownMbps))
	}
	if p.PlanUpMbps != nil {
		attrs = append(attrs, slog.Float64("plan_up_mbps", s.PlanUpMbps))
	}
	if p.KeepDays != nil {
		attrs = append(attrs, slog.Int("keep_days", s.KeepDays))
	}
	return attrs
}

// decodeSettingsPatch reads and validates a PATCH body: one JSON object of at
// most maxSettingsBody bytes, with known fields of the right type and in
// range. The error is the sentence for the 400, and names the field when
// there is one to name. The string is the error.code to log with it.
func decodeSettingsPatch(w http.ResponseWriter, r *http.Request) (settings.Patch, string, error) {
	var p settings.Patch
	body := &objectBody{r: http.MaxBytesReader(w, r.Body, maxSettingsBody)}
	dec := json.NewDecoder(body)
	dec.DisallowUnknownFields()
	err := dec.Decode(&p)
	if body.notObject(err) {
		return settings.Patch{}, codeJSON, errors.New("body must be one JSON object")
	}
	if err != nil {
		return p, bodyCode(err), settingsBodyError(err)
	}
	// Nothing may follow the object, not even a second one.
	if err := dec.Decode(&struct{}{}); err != io.EOF {
		if _, ok := errors.AsType[*http.MaxBytesError](err); ok {
			return p, codeTooLarge, settingsBodyError(err)
		}
		return p, codeJSON, errors.New("body must be one JSON object, with nothing after it")
	}
	if err := p.Validate(); err != nil {
		return p, codeSettings, err
	}
	return p, "", nil
}

// settingsBodyError words a decoder's error for the 400, without the Go type
// names it carries.
func settingsBodyError(err error) error {
	if _, ok := errors.AsType[*http.MaxBytesError](err); ok {
		return fmt.Errorf("body must be at most %d bytes", maxSettingsBody)
	}
	if ute, ok := errors.AsType[*json.UnmarshalTypeError](err); ok {
		if ute.Field == "" {
			return errors.New("body must be one JSON object")
		}
		want := "a number"
		if ute.Type != nil && ute.Type.String() == "int" {
			want = "a whole number"
		}
		return fmt.Errorf("%s must be %s (got %s)", ute.Field, want, ute.Value)
	}
	if errors.Is(err, io.EOF) {
		return errors.New("body must be one JSON object")
	}
	// The decoder words this one as: json: unknown field "name".
	if msg, ok := strings.CutPrefix(err.Error(), "json: unknown field "); ok {
		return errors.New("unknown field " + msg)
	}
	return errors.New("body must be one JSON object: " + err.Error())
}
