package metrics_test

import (
	"testing"
	"testing/synctest"
	"time"

	"github.com/redlinelabs-dev/dyno/server/internal/metrics"
)

// m3Dirs names both directions for the table tests.
var m3Dirs = []struct {
	name string
	dir  metrics.Direction
}{
	{"download", metrics.Download},
	{"upload", metrics.Upload},
}

func TestI5_ServingFalseOnNewAndNil(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		m := metrics.New("test")
		if m.Serving() {
			t.Error("Serving on a new Metrics = true")
		}
		time.Sleep(time.Minute)
		if m.Serving() {
			t.Error("Serving on an idle Metrics after a minute = true")
		}
		var nilm *metrics.Metrics
		if nilm.Serving() {
			t.Error("Serving on a nil *Metrics = true")
		}
	})
}

func TestI5_ServingWhileOpenThenGrace(t *testing.T) {
	for _, d := range m3Dirs {
		t.Run(d.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				m := metrics.New("test")
				m.StreamOpened(d.dir)
				if !m.Serving() {
					t.Fatal("Serving false right after StreamOpened")
				}
				// An open stream counts for as long as it is open, grace or not.
				time.Sleep(time.Minute)
				if !m.Serving() {
					t.Fatal("Serving false with a stream open for a minute")
				}
				m.StreamClosed(d.dir)
				if !m.Serving() {
					t.Fatal("Serving false right after StreamClosed")
				}
				time.Sleep(metrics.ServingGrace - time.Millisecond)
				if !m.Serving() {
					t.Fatalf("Serving false %v before the grace ends", time.Millisecond)
				}
				time.Sleep(2 * time.Millisecond)
				if m.Serving() {
					t.Fatalf("Serving true after ServingGrace (%v)", metrics.ServingGrace)
				}
				time.Sleep(time.Minute)
				if m.Serving() {
					t.Fatal("Serving true a minute after the grace")
				}
			})
		})
	}
}

func TestI5_ServingGraceIs5Seconds(t *testing.T) {
	if metrics.ServingGrace != 5*time.Second {
		t.Fatalf("ServingGrace = %v, want 5s (ADR-0029)", metrics.ServingGrace)
	}
}

// With several streams open, Serving holds until the last one closes, and the
// grace runs from that last close.
func TestI5_ServingUntilLastStreamCloses(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		m := metrics.New("test")
		m.StreamOpened(metrics.Download)
		m.StreamOpened(metrics.Download)
		m.StreamOpened(metrics.Upload)

		m.StreamClosed(metrics.Download)
		time.Sleep(time.Minute)
		if !m.Serving() {
			t.Fatal("Serving false with 2 streams still open")
		}
		m.StreamClosed(metrics.Upload)
		time.Sleep(time.Minute)
		if !m.Serving() {
			t.Fatal("Serving false with 1 stream still open")
		}
		m.StreamClosed(metrics.Download)
		time.Sleep(metrics.ServingGrace - time.Millisecond)
		if !m.Serving() {
			t.Fatal("Serving false inside the grace of the last close")
		}
		time.Sleep(2 * time.Millisecond)
		if m.Serving() {
			t.Fatal("Serving true after the grace of the last close")
		}
	})
}

// Closing one stream early doesn't start a grace that ends before the last
// stream's own: the grace counts from the last close.
func TestI5_ServingGraceCountsFromLastClose(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		m := metrics.New("test")
		m.StreamOpened(metrics.Download)
		m.StreamOpened(metrics.Upload)
		m.StreamClosed(metrics.Download)
		time.Sleep(4 * time.Second)
		m.StreamClosed(metrics.Upload)
		time.Sleep(4 * time.Second) // 8 s after the first close, 4 s after the last
		if !m.Serving() {
			t.Fatal("Serving false 4 s after the last close")
		}
		time.Sleep(2 * time.Second)
		if m.Serving() {
			t.Fatal("Serving true 6 s after the last close")
		}
	})
}

// A stream opened inside the grace keeps Serving true, and its own close
// starts a new grace.
func TestI5_ServingReopenedInsideGrace(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		m := metrics.New("test")
		m.StreamOpened(metrics.Download)
		m.StreamClosed(metrics.Download)
		time.Sleep(3 * time.Second)
		m.StreamOpened(metrics.Upload)
		time.Sleep(10 * time.Second)
		if !m.Serving() {
			t.Fatal("Serving false with the upload stream open")
		}
		m.StreamClosed(metrics.Upload)
		time.Sleep(4 * time.Second)
		if !m.Serving() {
			t.Fatal("Serving false 4 s after the upload closed")
		}
		time.Sleep(2 * time.Second)
		if m.Serving() {
			t.Fatal("Serving true 6 s after the upload closed")
		}
	})
}
