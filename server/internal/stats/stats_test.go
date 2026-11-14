package stats_test

import (
	"math"
	"slices"
	"testing"

	"github.com/redlinelabs-dev/dyno/server/internal/stats"
)

// bun's toBeCloseTo(x) with its default two digits: within 0.005.
const closeTo = 0.005

func near(t *testing.T, what string, got, want, tol float64) {
	t.Helper()
	if math.IsNaN(got) || math.Abs(got-want) > tol {
		t.Errorf("%s = %v, want %v (within %v)", what, got, want, tol)
	}
}

// failOnPanic turns a panic in the code under test into a failure of this
// test alone, so one broken function doesn't hide the results of the rest.
func failOnPanic(t *testing.T) {
	if r := recover(); r != nil {
		t.Errorf("panic: %v", r)
	}
}

// req is stats.test.ts's req: a request that moved mbit megabits.
func req(start, end, mbit float64) stats.UploadReq {
	return stats.UploadReq{Start: start, End: end, Bytes: mbit * 125_000}
}

func TestS1_MeanOfSamplesAndEmpty(t *testing.T) {
	defer failOnPanic(t)
	if got := stats.Mean([]float64{1, 2, 3}); got != 2 {
		t.Errorf("Mean([1 2 3]) = %v, want 2", got)
	}
	if got := stats.Mean([]float64{}); !math.IsNaN(got) {
		t.Errorf("Mean of an empty slice = %v, want NaN", got)
	}
	if got := stats.Mean(nil); !math.IsNaN(got) {
		t.Errorf("Mean(nil) = %v, want NaN", got)
	}
}

func TestS1_MedianOddEvenAndEmpty(t *testing.T) {
	defer failOnPanic(t)
	if got := stats.Median([]float64{3, 1, 2}); got != 2 {
		t.Errorf("Median([3 1 2]) = %v, want 2", got)
	}
	if got := stats.Median([]float64{4, 1, 3, 2}); got != 2.5 {
		t.Errorf("Median([4 1 3 2]) = %v, want 2.5", got)
	}
	if got := stats.Median([]float64{}); !math.IsNaN(got) {
		t.Errorf("Median of an empty slice = %v, want NaN", got)
	}
	if got := stats.Median(nil); !math.IsNaN(got) {
		t.Errorf("Median(nil) = %v, want NaN", got)
	}
}

func TestS1_JitterIsTheMeanAbsoluteStep(t *testing.T) {
	defer failOnPanic(t)
	near(t, "Jitter([10 12 9 9])", stats.Jitter([]float64{10, 12, 9, 9}), (2+3+0)/3.0, closeTo)
	if got := stats.Jitter([]float64{5}); got != 0 {
		t.Errorf("Jitter of one sample = %v, want 0", got)
	}
	if got := stats.Jitter(nil); got != 0 {
		t.Errorf("Jitter of no samples = %v, want 0", got)
	}
}

func TestS1_Mbps(t *testing.T) {
	defer failOnPanic(t)
	near(t, "Mbps(125e6 bytes, 1 s)", stats.Mbps(125_000_000, 1), 1000, 1e-9)
	for _, seconds := range []float64{0, -1} {
		if got := stats.Mbps(1_000_000, seconds); got != 0 {
			t.Errorf("Mbps over %v s = %v, want 0", seconds, got)
		}
	}
}

func TestS1_BucketRateCountsWholeBucketsInTheWindow(t *testing.T) {
	defer failOnPanic(t)
	b := stats.NewBuckets(0.1)
	for i := 0; i < 30; i++ {
		bytes := 12_500_000.0 // 1 Gbit/s
		if i < 10 {
			bytes = 1_000 // warm-up, slow
		}
		b.Add(float64(i)*0.1+0.05, bytes)
	}
	near(t, "Rate(1, 3)", b.Rate(1, 3), 1000, closeTo)
	if got := b.Rate(2.95, 3); got != 0 {
		t.Errorf("Rate(2.95, 3) = %v, want 0: no whole bucket inside", got)
	}
}

func TestS1_BucketRateCountsAStallAtTheEndAsZero(t *testing.T) {
	defer failOnPanic(t)
	b := stats.NewBuckets(0.1)
	for i := 0; i < 50; i++ {
		b.Add(float64(i)*0.1+0.05, 1_250_000) // 100 Mbit/s for 5 s, then nothing
	}
	near(t, "Rate(1.5, 10)", b.Rate(1.5, 10), 3.5*100/8.5, closeTo) // 3.5 s at 100 over 8.5 s
}

func TestS1_UploadRateIsEveryStreamsBytesAfterWarmupOverOneSharedWindow(t *testing.T) {
	defer failOnPanic(t)
	// Two streams at 100 and 200 Mbit/s. Stream a's request over warm-up at 3 s
	// counts in half; the count is complete up to b's last reply at 7.5 s.
	a, b := twoStreams()
	got, ok := stats.UploadRate([][]stats.UploadReq{a, b}, 3)
	if !ok {
		t.Fatal("UploadRate: ok is false with requests finished")
	}
	near(t, "UploadRate", got, (450+900)/4.5, closeTo)
}

// twoStreams is the two streams stats.test.ts's rate and chart tests share.
func twoStreams() (a, b []stats.UploadReq) {
	for i := 0; i < 8; i++ {
		a = append(a, req(float64(i)+0.5, float64(i)+1.5, 100))
	}
	for i := 0; i < 15; i++ {
		b = append(b, req(float64(i)/2, float64(i)/2+0.5, 100))
	}
	return a, b
}

func TestS1_UploadRateCountsTheGapsBetweenRequests(t *testing.T) {
	defer failOnPanic(t)
	// Each request sends 75 Mbit in 0.75 s and the next starts 0.25 s later, so
	// the stream delivers 300 Mbit in 3.75 s (80 Mbit/s), not the 100 it reaches
	// while a request is open.
	var reqs []stats.UploadReq
	for i := 0; i < 4; i++ {
		reqs = append(reqs, req(3+float64(i), 3.75+float64(i), 75))
	}
	got, ok := stats.UploadRate([][]stats.UploadReq{reqs}, 3)
	if !ok {
		t.Fatal("UploadRate: ok is false with requests finished")
	}
	near(t, "UploadRate", got, 80, closeTo)
}

func TestS1_UploadRateNeverAddsRatesTimedOverDifferentWindows(t *testing.T) {
	defer failOnPanic(t)
	// A 5 Mbit/s link shared by five streams that each manage one request,
	// finishing one after another as the link frees up. The old per-stream sum
	// read 6.1 here.
	var streams [][]stats.UploadReq
	for _, end := range []float64{5.41, 6.87, 7.5, 8.13, 8.92} {
		streams = append(streams, []stats.UploadReq{req(0.27, end, 8.39)})
	}
	got, ok := stats.UploadRate(streams, 3)
	if !ok {
		t.Fatal("UploadRate: ok is false with requests finished")
	}
	if got > 5 || got <= 4 {
		t.Errorf("UploadRate = %v, want above 4 and at most 5", got)
	}
}

func TestS1_UploadRateFallsBackToTheFirstRequestThenToNone(t *testing.T) {
	defer failOnPanic(t)
	got, ok := stats.UploadRate([][]stats.UploadReq{{req(0, 1, 1)}}, 3)
	if !ok {
		t.Fatal("UploadRate: ok is false with one request finished")
	}
	near(t, "UploadRate of one short request", got, 1, closeTo)

	if got, ok := stats.UploadRate([][]stats.UploadReq{{}, {}}, 3); ok {
		t.Errorf("UploadRate with no request finished = %v, ok true; want ok false", got)
	}
	if _, ok := stats.UploadRate(nil, 3); ok {
		t.Error("UploadRate of no streams: ok is true, want false")
	}
}

func TestS1_UploadBytesSpreadEachRequestEvenlyOverItsTimeAndKnownIsTheEarliestLastReply(t *testing.T) {
	defer failOnPanic(t)
	near(t, "UploadBytes over [1, 1.5)", stats.UploadBytes([][]stats.UploadReq{{req(0, 2, 8)}}, 1, 1.5), 2*125_000, closeTo)

	known, ok := stats.UploadKnown([][]stats.UploadReq{{req(0, 2, 8)}, {req(0, 1, 8)}})
	if !ok || known != 1 {
		t.Errorf("UploadKnown = %v, %v; want 1, true", known, ok)
	}
	if known, ok := stats.UploadKnown([][]stats.UploadReq{{}, {}}); ok {
		t.Errorf("UploadKnown with no request finished = %v, ok true; want ok false", known)
	}
}

func TestS1_UploadChartNeverShowsMoreThanTheResultsMethodAllows(t *testing.T) {
	defer failOnPanic(t)
	// A 5 Mbit/s link: five streams open 5 Mbit requests at 3 s and the replies
	// come a second apart. Spread evenly, the sample at 4 s read 11.4.
	var streams [][]stats.UploadReq
	for _, end := range []float64{4, 5, 6, 7, 8} {
		streams = append(streams, []stats.UploadReq{req(3, end, 5)})
	}
	rate, ok := stats.UploadRate(streams, 3)
	if !ok {
		t.Fatal("UploadRate: ok is false with requests finished")
	}
	near(t, "UploadRate", rate, 5, closeTo)

	series := stats.UploadSeries(streams, 3, 0.1, 1)
	if len(series) == 0 {
		t.Fatal("UploadSeries is empty with requests finished")
	}
	peak := 0.0
	for _, s := range series {
		peak = math.Max(peak, s[1])
	}
	if peak > 5+1e-9 {
		t.Errorf("chart peaks at %v, above the result's 5", peak)
	}
	last := series[len(series)-1]
	near(t, "last sample's time", last[0], 8, closeTo)
	near(t, "last sample's rate", last[1], 5, closeTo)
}

func TestS1_UploadChartSpreadsBytesWhenTheResultDoes(t *testing.T) {
	defer failOnPanic(t)
	a, b := twoStreams()
	series := stats.UploadSeries([][]stats.UploadReq{a, b}, 3, 0.1, 1)
	if len(series) == 0 {
		t.Fatal("UploadSeries is empty with requests finished")
	}
	last := series[len(series)-1]
	near(t, "last sample's time", last[0], 7.5, closeTo)
	near(t, "last sample's rate", last[1], 300, closeTo) // a at 100 plus b at 200 over [6.5, 7.5]

	if got := stats.UploadSeries([][]stats.UploadReq{{}, {}}, 3, 0.1, 1); len(got) != 0 {
		t.Errorf("UploadSeries with no request finished = %v, want empty", got)
	}
}

func TestS1_Quantile(t *testing.T) {
	defer failOnPanic(t)
	if got := stats.Quantile([]float64{1, 2, 3, 4}, 0.5); got != 2 {
		t.Errorf("Quantile([1 2 3 4], 0.5) = %v, want 2", got)
	}
	if got := stats.Quantile([]float64{1, 2, 3, 4}, 0.9); got != 4 {
		t.Errorf("Quantile([1 2 3 4], 0.9) = %v, want 4", got)
	}
	if got := stats.Quantile(nil, 0.5); !math.IsNaN(got) {
		t.Errorf("Quantile of no samples = %v, want NaN", got)
	}
}

func TestS2_MedianAndQuantileDoNotReorderTheirInput(t *testing.T) {
	defer failOnPanic(t)
	in := []float64{9, 1, 8, 2, 7, 3, 6}
	want := slices.Clone(in)
	stats.Median(in)
	if !slices.Equal(in, want) {
		t.Errorf("Median reordered its input: %v, was %v", in, want)
	}
	stats.Quantile(in, 0.9)
	if !slices.Equal(in, want) {
		t.Errorf("Quantile reordered its input: %v, was %v", in, want)
	}
	// An even count takes a different path to the middle; it must not sort either.
	even := []float64{4, 1, 3, 2}
	stats.Median(even)
	stats.Quantile(even, 0.5)
	if !slices.Equal(even, []float64{4, 1, 3, 2}) {
		t.Errorf("an even-length input was reordered: %v", even)
	}
}

// Every bucket holds a different byte count, so a sum over one bucket too many
// or too few shows. [a, b) are the buckets Rate must add, worked out by hand.
func TestS3_BucketRateTakesWholeBucketsOnly(t *testing.T) {
	defer failOnPanic(t)
	const width = 0.1
	b := stats.NewBuckets(width)
	counts := make([]float64, 60)
	for i := range counts {
		counts[i] = float64(i+1) * 10_000
		b.Add(float64(i)*width+width/2, counts[i])
	}
	for _, c := range []struct {
		name     string
		from, to float64
		a, b     int // buckets [a, b)
	}{
		{"both edges on buckets", 1, 3, 10, 30},
		{"from inside a bucket starts at the next", 1.05, 3, 11, 30},
		{"to inside a bucket stops at the last whole one", 1, 2.95, 10, 29},
		{"both inside", 1.05, 2.95, 11, 29},
		{"from just past a bucket edge", 1.01, 3, 11, 30},
		{"to just short of a bucket edge", 1, 2.99, 10, 29},
		// 0.3/0.1 is 2.9999999999999996 in floating point, a hair under its edge.
		{"to a hair under an edge counts it (1e-9 tolerance)", 0.1, 0.3, 1, 3},
		// 1.1/0.1 and 1.3/0.1 are exactly 11 and 13, so these edges need no
		// tolerance. A from that lands a hair past its edge is TestS3b's.
		{"edges that divide exactly are kept", 1.1, 1.3, 11, 13},
		{"one whole bucket", 0.5, 0.6, 5, 6},
		{"from before zero starts at the first bucket", -1, 0.5, 0, 5},
	} {
		sum := 0.0
		for _, v := range counts[c.a:c.b] {
			sum += v
		}
		want := stats.Mbps(sum, float64(c.b-c.a)*width)
		if got := b.Rate(c.from, c.to); math.Abs(got-want) > 1e-6*want {
			t.Errorf("%s: Rate(%v, %v) = %v, want %v (buckets %d to %d)", c.name, c.from, c.to, got, want, c.a, c.b-1)
		}
	}
}

// A start edge that floating point lands a hair past. 0.1 + 0.2 is
// 0.30000000000000004, so from/width is 3.0000000000000004 and a bare Ceil
// would start at bucket 4 instead of 3. The sum is worked out from variables:
// the compiler folds the constants 0.1 + 0.2 to exactly 0.3, and 0.3/0.1 is
// 2.9999999999999996, the other side of the edge. Every bucket holds a
// different byte count, as in S3, so a bucket too many or too few shows.
func TestS3b_BucketRateKeepsABucketWhoseStartEdgeLandsAHairPast(t *testing.T) {
	defer failOnPanic(t)
	const width = 0.1
	b := stats.NewBuckets(width)
	counts := make([]float64, 60)
	for i := range counts {
		counts[i] = float64(i+1) * 10_000
		b.Add(float64(i)*width+width/2, counts[i])
	}
	x, y := 0.1, 0.2
	from := x + y
	if math.Ceil(from/width) != 4 {
		t.Fatalf("setup: from = %v and from/width = %v, but this case needs the ratio just past 3", from, from/width)
	}
	for _, c := range []struct {
		name   string
		to     float64
		lo, hi int // buckets [lo, hi)
	}{
		{"to on an edge", 1, 3, 10},
		// 0.6/0.1 is 5.999999999999999, so the tolerance has to work on both edges at once.
		{"to a hair under an edge as well", 0.6, 3, 6},
	} {
		sum := 0.0
		for _, v := range counts[c.lo:c.hi] {
			sum += v
		}
		want := stats.Mbps(sum, float64(c.hi-c.lo)*width)
		if got := b.Rate(from, c.to); math.Abs(got-want) > 1e-6*want {
			t.Errorf("%s: Rate(%v, %v) = %v, want %v (buckets %d to %d)", c.name, from, c.to, got, want, c.lo, c.hi-1)
		}
	}
}

func TestS3_BucketRateWithoutAWholeBucketOrBeyondTheCountsIsZero(t *testing.T) {
	defer failOnPanic(t)
	b := stats.NewBuckets(0.1)
	for i := 0; i < 20; i++ {
		b.Add(float64(i)*0.1+0.05, 1_000_000)
	}
	// The first four have no whole bucket inside; the last two are past the
	// last count, which is empty buckets and never an index error.
	for _, c := range []struct{ from, to float64 }{
		{0.95, 1}, {1.02, 1.08}, {1, 1}, {2, 1},
		{5, 7}, {20, 30},
	} {
		if got := b.Rate(c.from, c.to); got != 0 {
			t.Errorf("Rate(%v, %v) = %v, want 0", c.from, c.to, got)
		}
	}
	// A window running past the last count counts the empty tail as zero.
	near(t, "Rate(1, 4) over 1 s of data", b.Rate(1, 4), stats.Mbps(10*1_000_000, 3), closeTo)
}

func TestS3_BucketsAddGrowsCountsByTime(t *testing.T) {
	defer failOnPanic(t)
	b := stats.NewBuckets(0.1)
	if b.Width != 0.1 {
		t.Errorf("Width = %v, want 0.1", b.Width)
	}
	b.Add(0.05, 10)
	b.Add(0.07, 5)
	b.Add(0.25, 20)
	want := []float64{15, 0, 20}
	if !slices.Equal(b.Counts, want) {
		t.Errorf("Counts = %v, want %v", b.Counts, want)
	}
}
