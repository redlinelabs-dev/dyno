package stats_test

import (
	"math"
	"testing"

	"github.com/redlinelabs-dev/dyno/server/internal/stats"
)

// A far end or a clock gone wrong must not make Add allocate without end or
// index out of range.
func TestSx_AddIgnoresTimesThatAreNotTimes(t *testing.T) {
	defer failOnPanic(t)
	b := stats.NewBuckets(0.1)
	b.Add(math.NaN(), 100)
	b.Add(math.Inf(1), 100)
	b.Add(1e18, 100)
	if len(b.Counts) != 0 {
		t.Fatalf("Counts = %v, want none: those times count nowhere", b.Counts)
	}
	b.Add(-5, 7)
	b.Add(math.Inf(-1), 3)
	if len(b.Counts) != 1 || b.Counts[0] != 10 {
		t.Errorf("Counts = %v, want [10]: a time before 0 counts in the first bucket", b.Counts)
	}
}

func TestSx_RateOverAnUnboundedWindowIsFiniteAndReturns(t *testing.T) {
	defer failOnPanic(t)
	b := stats.NewBuckets(0.1)
	b.Add(0.05, 1_000_000)
	for _, c := range []struct{ from, to float64 }{
		{0, math.Inf(1)}, {math.Inf(-1), 1}, {math.NaN(), 1}, {0, math.NaN()},
		{1e300, 1e301}, {math.Inf(1), math.Inf(1)},
	} {
		got := b.Rate(c.from, c.to)
		if math.IsNaN(got) || math.IsInf(got, 0) || got < 0 {
			t.Errorf("Rate(%v, %v) = %v, want a finite number", c.from, c.to, got)
		}
	}
}

func TestSx_UploadSeriesWithoutAStepIsEmpty(t *testing.T) {
	defer failOnPanic(t)
	streams := [][]stats.UploadReq{{{Start: 0, End: 1, Bytes: 1000}}}
	for _, step := range []float64{0, -1, math.NaN()} {
		if got := stats.UploadSeries(streams, 0, step, 1); len(got) != 0 {
			t.Errorf("UploadSeries with step %v = %v, want empty", step, got)
		}
	}
}

func TestSx_QuantileKeepsQInsideTheSamples(t *testing.T) {
	defer failOnPanic(t)
	a := []float64{3, 1, 2}
	if got := stats.Quantile(a, -1); got != 1 {
		t.Errorf("Quantile(a, -1) = %v, want the smallest, 1", got)
	}
	if got := stats.Quantile(a, 2); got != 3 {
		t.Errorf("Quantile(a, 2) = %v, want the largest, 3", got)
	}
	if got := stats.Quantile(a, math.NaN()); !math.IsNaN(got) {
		t.Errorf("Quantile(a, NaN) = %v, want NaN", got)
	}
}
