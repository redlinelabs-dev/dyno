// Package testrun counts the bytes the server itself moved for each test, so a
// stored result carries the server's view next to the client's (ADR-0002).
// Memory is bounded: at most maxRuns tests are tracked, oldest evicted first.
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

// Registry maps test ids to counters.
type Registry struct {
	mu   sync.Mutex
	runs map[string]*Counters
}

func NewRegistry() *Registry {
	return &Registry{runs: make(map[string]*Counters, maxRuns)}
}

// NewID returns a fresh random test id.
func NewID() string {
	var b [12]byte
	rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// ValidID reports whether id looks like one NewID made. Anything else is
// ignored, so a client can't fill the registry with junk keys.
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

// Get returns the counters for id, creating them if needed. It returns nil
// for an invalid id, so callers count nothing.
func (r *Registry) Get(id string) *Counters {
	if !ValidID(id) {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if c, ok := r.runs[id]; ok {
		return c
	}
	now := time.Now()
	if len(r.runs) >= maxRuns {
		r.evict(now)
	}
	c := &Counters{created: now}
	r.runs[id] = c
	return c
}

// Take removes and returns the totals for id; ok is false if the server never
// saw that test or has already forgotten it.
func (r *Registry) Take(id string) (down, up int64, ok bool) {
	r.mu.Lock()
	c, ok := r.runs[id]
	delete(r.runs, id)
	r.mu.Unlock()
	if !ok {
		return 0, 0, false
	}
	return c.Down.Load(), c.Up.Load(), true
}

// evict drops expired runs, or the oldest one if none have expired.
func (r *Registry) evict(now time.Time) {
	var oldest string
	for id, c := range r.runs {
		if now.Sub(c.created) > maxAge {
			delete(r.runs, id)
			continue
		}
		if oldest == "" || c.created.Before(r.runs[oldest].created) {
			oldest = id
		}
	}
	if len(r.runs) >= maxRuns {
		delete(r.runs, oldest)
	}
}
