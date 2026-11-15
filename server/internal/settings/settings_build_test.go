package settings_test

import (
	"math"
	"testing"

	"github.com/redlinelabs-dev/dyno/server/internal/settings"
)

func hxF(v float64) *float64 { return &v }

// H26: Validate accepts a plan speed of -0, and Apply stores it as +0, for
// each plan field, and for a patch that carries both.
func TestHx_ApplyStoresNegativeZeroAsZero(t *testing.T) {
	negZero := math.Copysign(0, -1)
	for _, p := range []settings.Patch{
		{PlanDownMbps: hxF(negZero)},
		{PlanUpMbps: hxF(negZero)},
		{PlanDownMbps: hxF(negZero), PlanUpMbps: hxF(negZero)},
	} {
		if err := p.Validate(); err != nil {
			t.Fatalf("Validate refused -0: %v", err)
		}
		got := settings.Settings{}.Apply(p)
		if math.Signbit(got.PlanDownMbps) || math.Signbit(got.PlanUpMbps) || got.PlanDownMbps != 0 || got.PlanUpMbps != 0 {
			t.Errorf("Apply gave %+v (sign bits %v, %v), want both +0",
				got, math.Signbit(got.PlanDownMbps), math.Signbit(got.PlanUpMbps))
		}
	}
}

// Every other value passes through Apply as it was, and a field the patch
// doesn't carry stays as it is.
func TestHx_ApplyKeepsOtherValues(t *testing.T) {
	for _, v := range []float64{0, 0.5, 1, 940.25, settings.MaxPlanMbps} {
		got := settings.Settings{PlanDownMbps: 7, PlanUpMbps: 8, KeepDays: 9}.Apply(settings.Patch{
			PlanDownMbps: hxF(v), PlanUpMbps: hxF(v),
		})
		if got.PlanDownMbps != v || got.PlanUpMbps != v || got.KeepDays != 9 {
			t.Errorf("Apply with %v gave %+v, want both plan speeds %v and keep_days 9", v, got, v)
		}
		if math.Signbit(got.PlanDownMbps) || math.Signbit(got.PlanUpMbps) {
			t.Errorf("Apply with %v set a sign bit: %+v", v, got)
		}
	}
	got := settings.Settings{PlanDownMbps: 7, PlanUpMbps: 8}.Apply(settings.Patch{})
	if got != (settings.Settings{PlanDownMbps: 7, PlanUpMbps: 8}) {
		t.Errorf("an empty patch changed the settings to %+v", got)
	}
}
