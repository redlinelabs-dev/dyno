package metrics_test

import (
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/redlinelabs-dev/dyno/server/internal/metrics"
)

// Streams opened and closed from many goroutines leave Serving true for the
// grace and false after it.
func TestIx_ServingAfterConcurrentStreams(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		m := metrics.New("test")
		var wg sync.WaitGroup
		for i := range 8 {
			d := metrics.Download
			if i%2 == 1 {
				d = metrics.Upload
			}
			wg.Go(func() {
				m.StreamOpened(d)
				time.Sleep(time.Second)
				m.StreamClosed(d)
			})
		}
		wg.Wait()
		if !m.Serving() {
			t.Fatal("Serving false right after the last close")
		}
		time.Sleep(metrics.ServingGrace + time.Millisecond)
		if m.Serving() {
			t.Fatal("Serving true after the grace")
		}
	})
}
