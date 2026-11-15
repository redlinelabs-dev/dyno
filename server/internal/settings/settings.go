// Package settings defines the server's own settings (ADR-0030): one home
// for each, which every client shares.
package settings

import "fmt"

// Limits.
const (
	MaxPlanMbps = 100_000 // Mbit/s
	MaxKeepDays = 36_500
)

// Settings are the server's settings. The zero value is the default.
type Settings struct {
	PlanDownMbps float64 `json:"plan_down_mbps"` // the plan's download speed; 0 means not set
	PlanUpMbps   float64 `json:"plan_up_mbps"`   // the plan's upload speed; 0 means not set
	KeepDays     int     `json:"keep_days"`      // results older than this are deleted; 0 keeps them all
}

// Patch is a change to some settings; nil fields stay as they are.
type Patch struct {
	PlanDownMbps *float64 `json:"plan_down_mbps"`
	PlanUpMbps   *float64 `json:"plan_up_mbps"`
	KeepDays     *int     `json:"keep_days"`
}

// Validate reports a field that is out of range, by its JSON name: a plan
// speed must be finite and from 0 to MaxPlanMbps, and KeepDays from 0 to
// MaxKeepDays.
func (p Patch) Validate() error {
	for _, f := range []struct {
		name string
		v    *float64
	}{
		{"plan_down_mbps", p.PlanDownMbps},
		{"plan_up_mbps", p.PlanUpMbps},
	} {
		// Written so that NaN, which fails both comparisons, is refused.
		if f.v != nil && !(*f.v >= 0 && *f.v <= MaxPlanMbps) {
			return fmt.Errorf("%s must be a number from 0 to %d", f.name, MaxPlanMbps)
		}
	}
	if p.KeepDays != nil && (*p.KeepDays < 0 || *p.KeepDays > MaxKeepDays) {
		return fmt.Errorf("keep_days must be a whole number from 0 to %d", MaxKeepDays)
	}
	return nil
}

// Apply returns s with p's non-nil fields copied in. It doesn't validate. A plan
// speed of -0, which Validate accepts as 0, is stored as 0, so no client ever
// reads a -0.
func (s Settings) Apply(p Patch) Settings {
	if p.PlanDownMbps != nil {
		s.PlanDownMbps = noNegativeZero(*p.PlanDownMbps)
	}
	if p.PlanUpMbps != nil {
		s.PlanUpMbps = noNegativeZero(*p.PlanUpMbps)
	}
	if p.KeepDays != nil {
		s.KeepDays = *p.KeepDays
	}
	return s
}

// noNegativeZero is v, with -0 as 0: they are equal, but -0 prints with its sign.
func noNegativeZero(v float64) float64 {
	if v == 0 {
		return 0
	}
	return v
}
