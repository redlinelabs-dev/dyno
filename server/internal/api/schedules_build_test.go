package api_test

import (
	"errors"
	"log/slog"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/redlinelabs-dev/dyno/server/internal/api"
	"github.com/redlinelabs-dev/dyno/server/internal/nodetest"
	"github.com/redlinelabs-dev/dyno/server/internal/schedule"
	"github.com/redlinelabs-dev/dyno/server/internal/store"
	"github.com/redlinelabs-dev/dyno/server/internal/testrun"
)

// Builder's own cases, beside the spec cases in schedules_test.go.

// cxPatchInBackground sends a PATCH on its own goroutine and returns the
// channel its reply arrives on.
func cxPatchInBackground(t *testing.T, env *schEnv, path, body string) <-chan schResp {
	t.Helper()
	done := make(chan schResp, 1)
	go func() {
		r, err := env.try("PATCH", path, body)
		if err != nil {
			t.Errorf("PATCH %s: %v", path, err)
		}
		done <- r
	}()
	return done
}

// cxWaitFor returns what arrives on done, or fails the test after 10 s.
func cxWaitFor(t *testing.T, done <-chan schResp) schResp {
	t.Helper()
	select {
	case r := <-done:
		return r
	case <-time.After(10 * time.Second):
		t.Fatal("a request never finished")
	}
	return schResp{}
}

// A PATCH that waits on a far-end check must not undo a change that lands
// while it waits.
func TestCx_PatchAfterACheckKeepsAChangeMadeDuringIt(t *testing.T) {
	defer schNoPanic(t)
	env := schNewEnv(t)
	sc := env.add(t, `{"far_end":"tailmeadow","name":"Gran's"}`)
	path := "/api/v1/schedules/" + strconv.FormatInt(sc.ID, 10)

	env.check.hold()
	moved := cxPatchInBackground(t, env, path, `{"far_end":"gran-house","name":"Moved"}`)
	env.check.waitEntered(t, 2) // the add's call, still counted, and this one
	env.do("PATCH", path, `{"paused":true}`).wantStatus(t, 200)
	env.check.release()

	r := cxWaitFor(t, moved)
	r.wantStatus(t, 200)
	if got := r.asSchedule(t); !got.Paused || got.Name != "Moved" || got.FarEnd != "http://gran-house:3966" {
		t.Errorf("reply to the PATCH that waited: %s, want it moved, renamed and paused", r.Body)
	}
	stored, err := env.st.Schedule(sc.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !stored.Paused || stored.Name != "Moved" || stored.FarEnd != "http://gran-house:3966" {
		t.Errorf("stored %+v, want the far end and name from the slow PATCH and the pause from the quick one", stored)
	}
}

func TestCx_PatchOfAScheduleDeletedDuringItsCheckIs404(t *testing.T) {
	defer schNoPanic(t)
	env := schNewEnv(t)
	sc := env.add(t, `{"far_end":"tailmeadow"}`)
	path := "/api/v1/schedules/" + strconv.FormatInt(sc.ID, 10)

	env.check.hold()
	done := cxPatchInBackground(t, env, path, `{"far_end":"gran-house"}`)
	env.check.waitEntered(t, 2)
	env.do("DELETE", path, "").wantStatus(t, 204)
	env.check.release()

	if r := cxWaitFor(t, done); r.Status != 404 {
		t.Errorf("PATCH of a schedule deleted during its check: status %d, want 404: %s", r.Status, r.Body)
	}
	env.expect(t, 0, 2, 2) // the add and the delete tell the scheduler, the refused PATCH doesn't
}

func TestCx_RunRefusalNeverWaitsMoreThanAMinute(t *testing.T) {
	defer schNoPanic(t)
	env := schNewEnv(t)
	sc := env.add(t, `{"far_end":"tailmeadow"}`)
	// A result dated ahead of now, as after the clock was set back.
	schStoreRun(t, env.st, sc.ID, sc.FarEnd, time.Now().Add(time.Hour), nil)

	r := env.do("POST", "/api/v1/schedules/"+strconv.FormatInt(sc.ID, 10)+"/run", "")
	r.wantStatus(t, 429)
	if got := r.Header.Get("Retry-After"); got != "60" {
		t.Errorf("Retry-After = %q, want 60", got)
	}
	if ran := env.sched.ran(); len(ran) != 0 {
		t.Errorf("RunNow was called with %v for a refused run", ran)
	}
}

func TestCx_WithoutACheckFuncANewFarEndIs503(t *testing.T) {
	defer schNoPanic(t)
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	cl := schServe(t, &api.API{Version: "1.2.3", Store: st, Runs: testrun.NewRegistry(), Log: slog.New(slog.DiscardHandler)})

	for _, c := range []struct{ path, body string }{
		{"/api/v1/schedules", `{"far_end":"tailmeadow"}`},
		{"/api/v1/schedules/check", `{"far_end":"tailmeadow"}`},
	} {
		if r := cl.do("POST", c.path, c.body); r.Status != 503 {
			t.Errorf("POST %s: status %d, want 503: %s", c.path, r.Status, r.Body)
		}
	}

	// A stored schedule can still be retimed: only a new far end needs the check.
	sc := schSeed(t, st, "tailmeadow")
	path := "/api/v1/schedules/" + strconv.FormatInt(sc.ID, 10)
	cl.do("PATCH", path, `{"paused":true}`).wantStatus(t, 200)
	if r := cl.do("PATCH", path, `{"far_end":"gran-house"}`); r.Status != 503 {
		t.Errorf("PATCH of the far end: status %d, want 503: %s", r.Status, r.Body)
	}
	if list, err := st.Schedules(); err != nil || len(list) != 1 || list[0].FarEnd != sc.FarEnd {
		t.Errorf("the store holds %+v, err %v; want the one schedule, unmoved", list, err)
	}
}

func TestCx_ALongHostGivesAShortDefaultName(t *testing.T) {
	defer schNoPanic(t)
	env := schNewEnv(t)
	label := strings.Repeat("a", 63)
	host := label + "." + label + "." + label // 191 bytes, which is a valid host
	got := env.add(t, `{"far_end":"`+host+`"}`)
	if got.Name != host[:schedule.MaxNameLen] || got.FarEnd != "http://"+host+":3966" {
		t.Errorf("name %q and far end %q, want the host cut to %d bytes as the name", got.Name, got.FarEnd, schedule.MaxNameLen)
	}
}

// Every way out of a check frees its place: the loop ends more checks, in
// failure, than there are places.
func TestCx_CheckPlacesAreFreedWhateverTheOutcome(t *testing.T) {
	defer schNoPanic(t)
	env := schNewEnv(t)
	for range 12 {
		env.check.setErr(&nodetest.Error{Code: nodetest.CodeUnreachable, Err: errors.New("no route")})
		env.do("POST", "/api/v1/schedules/check", `{"far_end":"tailmeadow"}`).wantStatus(t, 422)
		env.check.setErr(errors.New("boom"))
		env.do("POST", "/api/v1/schedules/check", `{"far_end":"tailmeadow"}`).wantStatus(t, 500)
		env.check.setErr(nil)
		env.do("POST", "/api/v1/schedules/check", `{"far_end":"tailmeadow"}`).wantStatus(t, 200)
	}
}

func TestCx_BodyProblemsAreWordedWithoutGoTypes(t *testing.T) {
	defer schNoPanic(t)
	env := schNewEnv(t)
	for _, c := range []struct{ body, want string }{
		{`{"far_end":"tailmeadow","every_minutes":"60"}`, "every_minutes must be a whole number"},
		{`{"far_end":"tailmeadow","name":5}`, "name must be a string"},
		{`{"far_end":"tailmeadow","paused":"yes"}`, "paused must be true or false"},
		{`{"far_end":"tailmeadow","colour":"red"}`, `unknown field "colour"`},
	} {
		r := env.do("POST", "/api/v1/schedules", c.body)
		r.wantStatus(t, 400)
		if got := r.errorText(t); got != c.want {
			t.Errorf("body %s: error %q, want %q", c.body, got, c.want)
		}
	}
	env.expect(t, 0, 0, 0)
}

// C38: a body that isn't a JSON object is 400 on every route that reads one,
// null included, which the decoder would otherwise take as an empty object.
// Nothing is stored or changed, and neither Check nor Changed is called.
func TestCx_BodyThatIsNotAnObjectIs400AndChangesNothing(t *testing.T) {
	defer schNoPanic(t)
	env := schNewEnv(t)
	sc := env.add(t, `{"far_end":"tailmeadow"}`)
	path := "/api/v1/schedules/" + strconv.FormatInt(sc.ID, 10)
	before, err := env.st.Schedule(sc.ID)
	if err != nil {
		t.Fatal(err)
	}

	for _, body := range []string{`null`, `[]`, `"x"`, `1`, `true`, " \n\tnull", `null {}`} {
		for _, c := range []struct{ method, path string }{
			{"POST", "/api/v1/schedules"},
			{"POST", "/api/v1/schedules/check"},
			{"PATCH", path},
		} {
			r := env.do(c.method, c.path, body)
			r.wantStatus(t, 400)
			if got := r.errorText(t); got != "the body must be one JSON object" {
				t.Errorf("%s %s with %q: error %q, want the one-JSON-object sentence", c.method, c.path, body, got)
			}
		}
	}
	env.expect(t, 1, 1, 1) // the add's own check and change, and nothing since
	after, err := env.st.Schedule(sc.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(after, before) {
		t.Errorf("the schedule is %+v after the refused PATCHes, want %+v", after, before)
	}

	// An empty object is still a patch that changes nothing.
	env.do("PATCH", path, `{}`).wantStatus(t, 200)
	env.expect(t, 1, 1, 2)
}
