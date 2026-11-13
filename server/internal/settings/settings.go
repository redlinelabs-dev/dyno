// Package settings defines the server's own settings (ADR-0030): one home
// for each, which every client shares.
package settings

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
func (p Patch) Validate() error { panic("not implemented") }

// Apply returns s with p's non-nil fields copied in. It doesn't validate.
func (s Settings) Apply(p Patch) Settings { panic("not implemented") }
