// Package testrun counts the bytes the server itself moved for each test, so a
// stored result carries the server's view next to the client's (ADR-0002).
// Only ids the server issued with a plan are counted, each for maxAge, and at
// most maxRuns at a time.
package testrun

import (
	"crypto/rand"
	"encoding/hex"
	"sync"
	"sync/atomic"
	"time"
)

const (
	maxRuns = 64
	maxAge  = 10 * time.Minute
)

// Counters are the bytes the server sent (Down) and received (Up) for one test.
type Counters struct {
	Down    atomic.Int64
	Up      atomic.Int64
	created time.Time
}

func (c *Counters) idle() bool { return c.Down.Load() == 0 && c.Up.Load() == 0 }

// Registry maps test ids to counters.
type Registry struct {
	mu   sync.Mutex
	runs map[string]*Counters
	now  func() time.Time // time.Now, except in tests
}

func NewRegistry() *Registry {
	return &Registry{runs: make(map[string]*Counters, maxRuns), now: time.Now}
}

// NewID returns a fresh random test id.
func NewID() string {
	var b [12]byte
	rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// ValidID reports whether id looks like one NewID made.
func ValidID(id string) bool {
	if len(id) != 24 {
		return false
	}
	for i := 0; i < len(id); i++ {
		c := id[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// New issues a test id and starts counting for it. When the registry is full
// it drops an expired run, else the oldest run that hasn't moved a byte. If
// every run is live, the id is still returned but its bytes go uncounted:
// a burst of plans must not wipe the counts of tests already running.
func (r *Registry) New() string {
	id := NewID()
	r.mu.Lock()
	defer r.mu.Unlock()
	now := r.now()
	if len(r.runs) >= maxRuns && !r.evict(now) {
		return id
	}
	r.runs[id] = &Counters{created: now}
	return id
}

// Get returns the counters for an id New issued, or nil if the server didn't
// issue it, has stopped counting it, or it expired, so callers count nothing.
func (r *Registry) Get(id string) *Counters {
	r.mu.Lock()
	defer r.mu.Unlock()
	c, ok := r.runs[id]
	if !ok {
		return nil
	}
	if r.now().Sub(c.created) > maxAge {
		delete(r.runs, id)
		return nil
	}
	return c
}

// Take removes and returns the totals for id; ok is false if the server never
// issued that id or has forgotten it.
func (r *Registry) Take(id string) (down, up int64, ok bool) {
	r.mu.Lock()
	c, ok := r.runs[id]
	delete(r.runs, id)
	expired := ok && r.now().Sub(c.created) > maxAge
	r.mu.Unlock()
	if !ok || expired {
		return 0, 0, false
	}
	return c.Down.Load(), c.Up.Load(), true
}

// evict drops every expired run, or failing that the oldest idle one, and
// reports whether it freed a slot.
func (r *Registry) evict(now time.Time) bool {
	freed := false
	var oldest string
	for id, c := range r.runs {
		if now.Sub(c.created) > maxAge {
			delete(r.runs, id)
			freed = true
			continue
		}
		if c.idle() && (oldest == "" || c.created.Before(r.runs[oldest].created)) {
			oldest = id
		}
	}
	if !freed && oldest != "" {
		delete(r.runs, oldest)
		freed = true
	}
	return freed
}
