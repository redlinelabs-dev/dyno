package settings_test

import (
	"fmt"
	"math"
	"strings"
	"testing"

	"github.com/redlinelabs-dev/dyno/server/internal/settings"
)

func histF(v float64) *float64 {
	return &v
}

func histI(v int) *int {
	return &v
}

// histPlanFields puts one value in one of the two plan speeds of a Patch.
var histPlanFields = []struct {
	name string
	set  func(v float64) settings.Patch
}{
	{"plan_down_mbps", func(v float64) settings.Patch { return settings.Patch{PlanDownMbps: &v} }},
	{"plan_up_mbps", func(v float64) settings.Patch { return settings.Patch{PlanUpMbps: &v} }},
}

var histNames = []string{"plan_down_mbps", "plan_up_mbps", "keep_days"}

// histCheckNames wants err to be set, to hold the JSON name want and to hold
// none of the other settings' names.
func histCheckNames(t *testing.T, what string, err error, want string) {
	t.Helper()
	if err == nil {
		t.Errorf("%s: accepted", what)
		return
	}
	for _, name := range histNames {
		has := strings.Contains(err.Error(), name)
		if name == want && !has {
			t.Errorf("%s: error %q does not hold the field name %q", what, err, want)
		}
		if name != want && has {
			t.Errorf("%s: error %q names %q, which is not the field at fault", what, err, name)
		}
	}
}

func TestH1_LimitsAreTheDocumentedOnes(t *testing.T) {
	if settings.MaxPlanMbps != 100000 || settings.MaxKeepDays != 36500 {
		t.Errorf("MaxPlanMbps = %v, MaxKeepDays = %v, want 100000 and 36500", settings.MaxPlanMbps, settings.MaxKeepDays)
	}
}

func TestH1_ZeroPatchIsValid(t *testing.T) {
	var p settings.Patch
	if err := p.Validate(); err != nil {
		t.Fatalf("zero Patch: %v", err)
	}
}

func TestH1_ValidateAcceptsPlanSpeedsFromZeroTo100000(t *testing.T) {
	for _, f := range histPlanFields {
		for _, v := range []float64{0, 0.5, 940.25, 99999.9, 100000} {
			if err := f.set(v).Validate(); err != nil {
				t.Errorf("%s = %v: %v", f.name, v, err)
			}
		}
	}
}

func TestH1_ValidateRejectsBadPlanSpeeds(t *testing.T) {
	for _, f := range histPlanFields {
		for _, v := range []float64{-1, 100000.1, math.NaN(), math.Inf(1), math.Inf(-1)} {
			histCheckNames(t, fmt.Sprintf("%s = %v", f.name, v), f.set(v).Validate(), f.name)
		}
	}
}

func TestH1_ValidateAcceptsKeepDaysFromZeroTo36500(t *testing.T) {
	for _, v := range []int{0, 1, 90, 36499, 36500} {
		p := settings.Patch{KeepDays: &v}
		if err := p.Validate(); err != nil {
			t.Errorf("keep_days = %d: %v", v, err)
		}
	}
}

func TestH1_ValidateRejectsBadKeepDays(t *testing.T) {
	for _, v := range []int{-1, 36501, -36500, math.MaxInt32} {
		p := settings.Patch{KeepDays: &v}
		histCheckNames(t, fmt.Sprintf("keep_days = %d", v), p.Validate(), "keep_days")
	}
}

func TestH1_ValidateNamesTheFieldAtFault(t *testing.T) {
	for _, c := range []struct {
		name  string
		patch settings.Patch
		bad   string
	}{
		{"keep_days beside a good plan speed", settings.Patch{PlanDownMbps: histF(500), KeepDays: histI(-1)}, "keep_days"},
		{"plan_up_mbps beside a good keep_days", settings.Patch{PlanUpMbps: histF(-1), KeepDays: histI(30)}, "plan_up_mbps"},
		{"plan_down_mbps beside good fields", settings.Patch{PlanDownMbps: histF(math.NaN()), PlanUpMbps: histF(50), KeepDays: histI(30)}, "plan_down_mbps"},
	} {
		histCheckNames(t, c.name, c.patch.Validate(), c.bad)
	}
}

func TestH2_ApplyCopiesOnlyNonNilFields(t *testing.T) {
	base := settings.Settings{PlanDownMbps: 500, PlanUpMbps: 50, KeepDays: 30}
	for _, c := range []struct {
		name  string
		patch settings.Patch
		want  settings.Settings
	}{
		{"an empty patch", settings.Patch{}, base},
		{"plan_down_mbps only", settings.Patch{PlanDownMbps: histF(940.5)}, settings.Settings{PlanDownMbps: 940.5, PlanUpMbps: 50, KeepDays: 30}},
		{"plan_up_mbps only", settings.Patch{PlanUpMbps: histF(120.25)}, settings.Settings{PlanDownMbps: 500, PlanUpMbps: 120.25, KeepDays: 30}},
		{"keep_days only", settings.Patch{KeepDays: histI(7)}, settings.Settings{PlanDownMbps: 500, PlanUpMbps: 50, KeepDays: 7}},
		{"two fields", settings.Patch{PlanUpMbps: histF(75), KeepDays: histI(365)}, settings.Settings{PlanDownMbps: 500, PlanUpMbps: 75, KeepDays: 365}},
		{"all three fields", settings.Patch{PlanDownMbps: histF(1000), PlanUpMbps: histF(100), KeepDays: histI(14)}, settings.Settings{PlanDownMbps: 1000, PlanUpMbps: 100, KeepDays: 14}},
		{"zeros that are set", settings.Patch{PlanDownMbps: histF(0), PlanUpMbps: histF(0), KeepDays: histI(0)}, settings.Settings{}},
		{"one zero that is set", settings.Patch{KeepDays: histI(0)}, settings.Settings{PlanDownMbps: 500, PlanUpMbps: 50}},
		{"values out of range are copied, not checked", settings.Patch{PlanUpMbps: histF(-1), KeepDays: histI(-5)}, settings.Settings{PlanDownMbps: 500, PlanUpMbps: -1, KeepDays: -5}},
	} {
		if got := base.Apply(c.patch); got != c.want {
			t.Errorf("%s: got %+v, want %+v", c.name, got, c.want)
		}
	}
	if base != (settings.Settings{PlanDownMbps: 500, PlanUpMbps: 50, KeepDays: 30}) {
		t.Errorf("Apply changed what it was called on: %+v", base)
	}
}

func TestH2_ApplyToTheZeroValue(t *testing.T) {
	got := settings.Settings{}.Apply(settings.Patch{PlanUpMbps: histF(50), KeepDays: histI(7)})
	want := settings.Settings{PlanUpMbps: 50, KeepDays: 7}
	if got != want {
		t.Errorf("got %+v, want %+v", got, want)
	}
}
