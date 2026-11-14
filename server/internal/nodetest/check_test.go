package nodetest_test

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"testing"
	"time"

	"github.com/redlinelabs-dev/dyno/server/internal/nodetest"
)

// wantCheckFail checks that Check failed with code and no phase.
func wantCheckFail(t *testing.T, err error, code string) {
	t.Helper()
	var ne *nodetest.Error
	if !errors.As(err, &ne) {
		t.Fatalf("Check returned err %v (%T), want a *nodetest.Error with code %q", err, err, code)
	}
	if ne.Code != code || ne.Phase != "" {
		t.Errorf("Check failed with code %q in phase %q, want code %q and no phase (err: %v)", ne.Code, ne.Phase, code, ne.Err)
	}
}

func TestN37_CheckMakesOneRequestAndReportsWhoAnswered(t *testing.T) {
	for _, secure := range []bool{false, true} {
		name := "http"
		if secure {
			name = "https"
		}
		t.Run(name, func(t *testing.T) {
			fe := newFarEnd(t, shortPlan())
			fe.TLS = secure
			fe.start()
			// Check uses only FarEnd and TLS from its options; the rest would
			// wreck it if it looked: no time for a ping, an hour of warm-up,
			// a Progress that must stay quiet.
			o := fe.options()
			o.Kind, o.MaxBytes, o.Warm, o.PingTimeout = nodetest.Light, 1, time.Hour, time.Nanosecond
			o.Progress = func(p nodetest.Progress) { t.Errorf("Check called Progress with %+v", p) }

			info, err := nodetest.Check(testCtx(t, 10*time.Second), o)
			if err != nil {
				t.Fatalf("Check: %v", err)
			}
			if want := (nodetest.FarEndInfo{Name: "fakedyno", Version: "v9.9.9"}); info != want {
				t.Errorf("Check = %+v, want the plan's server %+v", info, want)
			}
			rs := fe.recs()
			if len(rs) != 1 || rs[0].Method != "GET" || rs[0].Path != "/api/v1/plan" {
				t.Errorf("requests = %+v, want exactly one, GET /api/v1/plan", rs)
			}
		})
	}
}

func TestN38_CheckFailsTheWayRunsFirstStepDoes(t *testing.T) {
	t.Run("a bad far end", func(t *testing.T) {
		fe := newFarEnd(t, shortPlan()).start()
		for _, bad := range badFarEnds(fe) {
			t.Run(bad.name, func(t *testing.T) {
				_, err := nodetest.Check(testCtx(t, 10*time.Second), nodetest.Options{FarEnd: bad.farEnd})
				wantCheckFail(t, err, nodetest.CodeBadFarEnd)
			})
		}
		if n := len(fe.recs()); n != 0 {
			t.Errorf("bad far ends sent %d requests to the far end they named", n)
		}
	})

	t.Run("nothing listening", func(t *testing.T) {
		began := time.Now()
		_, err := nodetest.Check(testCtx(t, 20*time.Second), nodetest.Options{FarEnd: "http://" + closedAddr(t)})
		wantCheckFail(t, err, nodetest.CodeUnreachable)
		if took := time.Since(began); took > 2*time.Second {
			t.Errorf("took %v to find nothing listening, want under 2 s", took)
		}
	})

	t.Run("an unresolvable host", func(t *testing.T) {
		const host = "dyno-nodetest-n38.invalid"
		needFastLookupFailure(t, host)
		_, err := nodetest.Check(testCtx(t, 20*time.Second), nodetest.Options{FarEnd: "http://" + host + ":3966"})
		wantCheckFail(t, err, nodetest.CodeUnreachable)
	})

	for _, c := range notDynoPlans(t) {
		t.Run("not dyno: "+c.name, func(t *testing.T) {
			fe := newFarEnd(t, shortPlan())
			fe.PlanHook = c.hook
			fe.start()
			_, err := nodetest.Check(testCtx(t, 10*time.Second), fe.options())
			wantCheckFail(t, err, nodetest.CodeNotDyno)
			if n := len(fe.recs()); n != 1 {
				t.Errorf("%d requests, want just the plan", n)
			}
		})
	}

	t.Run("a plan that never answers", func(t *testing.T) {
		fe := newFarEnd(t, shortPlan())
		fe.PlanHook = func(w http.ResponseWriter, r *http.Request, n int) bool { fe.block(r); return true }
		fe.start()
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		began := time.Now()
		_, err := nodetest.Check(ctx, fe.options())
		wantCheckFail(t, err, nodetest.CodeTimeout)
		if took := time.Since(began); took < 900*time.Millisecond || took > 2*time.Second {
			t.Errorf("returned after %v, want about the 1 s deadline and within 2 s", took)
		}
	})

	t.Run("a canceled ctx", func(t *testing.T) {
		fe := newFarEnd(t, shortPlan())
		fe.PlanHook = func(w http.ResponseWriter, r *http.Request, n int) bool { fe.block(r); return true }
		fe.start()
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		time.AfterFunc(200*time.Millisecond, cancel)
		began := time.Now()
		_, err := nodetest.Check(ctx, fe.options())
		wantCheckFail(t, err, nodetest.CodeCanceled)
		if took := time.Since(began); took > 1200*time.Millisecond {
			t.Errorf("returned after %v, want within 1 s of the cancel at 200 ms", took)
		}
	})

	t.Run("a ctx canceled before the call", func(t *testing.T) {
		fe := newFarEnd(t, shortPlan()).start()
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_, err := nodetest.Check(ctx, fe.options())
		wantCheckFail(t, err, nodetest.CodeCanceled)
	})
}

func TestN39_CheckFollowsNoRedirect(t *testing.T) {
	for _, code := range []int{301, 302, 303, 307, 308} {
		t.Run(strconv.Itoa(code), func(t *testing.T) {
			target, hits := redirectTarget(t)
			fe := newFarEnd(t, shortPlan())
			fe.PlanHook = redirectTo(target, code)
			fe.start()
			_, err := nodetest.Check(testCtx(t, 10*time.Second), fe.options())
			wantCheckFail(t, err, nodetest.CodeNotDyno)
			if n := hits(); n != 0 {
				t.Errorf("the redirect's target got %d requests", n)
			}
			if n := len(fe.recs()); n != 1 {
				t.Errorf("%d requests to the far end, want just the plan", n)
			}
		})
	}
}
