package testrun

import (
	"testing"
	"time"
)

func TestCountAndTake(t *testing.T) {
	r := NewRegistry()
	id := r.New()
	if !ValidID(id) {
		t.Fatalf("New returned %q", id)
	}
	r.Get(id).Down.Add(100)
	r.Get(id).Up.Add(7)
	down, up, ok := r.Take(id)
	if !ok || down != 100 || up != 7 {
		t.Fatalf("Take = %d, %d, %v", down, up, ok)
	}
	if _, _, ok := r.Take(id); ok {
		t.Fatal("second Take found the run again")
	}
}

func TestOnlyIssuedIDsAreCounted(t *testing.T) {
	r := NewRegistry()
	for _, id := range []string{"", "x", "ZZZZZZZZZZZZZZZZZZZZZZZZ", NewID(), NewID() + "0"} {
		if r.Get(id) != nil {
			t.Errorf("Get(%q) counted an id the server never issued", id)
		}
	}
	if len(r.runs) != 0 {
		t.Fatalf("%d runs tracked", len(r.runs))
	}
}

// A burst of plans must not wipe the counts of tests already moving bytes.
func TestFullRegistryKeepsLiveRuns(t *testing.T) {
	r := NewRegistry()
	live := r.New()
	r.Get(live).Down.Add(1)
	for range 2 * maxRuns {
		r.New()
	}
	if n := len(r.runs); n > maxRuns {
		t.Fatalf("%d runs tracked, cap is %d", n, maxRuns)
	}
	if down, _, ok := r.Take(live); !ok || down != 1 {
		t.Fatal("a live run was evicted")
	}
}

func TestFullOfLiveRunsCountsNothingNew(t *testing.T) {
	r := NewRegistry()
	for range maxRuns {
		r.Get(r.New()).Up.Add(1)
	}
	id := r.New()
	if r.Get(id) != nil {
		t.Fatal("a run was added past the cap")
	}
	if len(r.runs) != maxRuns {
		t.Fatalf("%d runs tracked", len(r.runs))
	}
}

func TestRunsExpire(t *testing.T) {
	r := NewRegistry()
	now := time.Now()
	r.now = func() time.Time { return now }
	id := r.New()
	r.Get(id).Down.Add(5)
	gone := r.New()
	now = now.Add(maxAge + time.Second)
	if r.Get(id) != nil {
		t.Fatal("Get returned an expired run")
	}
	if _, _, ok := r.Take(gone); ok {
		t.Fatal("Take returned an expired run")
	}

	// Expired runs make room even when every run moved bytes.
	for range maxRuns {
		r.Get(r.New()).Up.Add(1)
	}
	now = now.Add(maxAge + time.Second)
	if r.Get(r.New()) == nil {
		t.Fatal("expired runs weren't evicted to make room")
	}
}
