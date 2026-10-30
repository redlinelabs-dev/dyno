package testrun

import "testing"

func TestCountAndTake(t *testing.T) {
	r := NewRegistry()
	id := NewID()
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

func TestInvalidIDsAreIgnored(t *testing.T) {
	r := NewRegistry()
	for _, id := range []string{"", "x", "ZZZZZZZZZZZZZZZZZZZZZZZZ", NewID() + "0"} {
		if r.Get(id) != nil {
			t.Errorf("Get(%q) tracked an invalid id", id)
		}
	}
}

func TestBounded(t *testing.T) {
	r := NewRegistry()
	first := NewID()
	r.Get(first)
	for range 2 * maxRuns {
		r.Get(NewID())
	}
	if n := len(r.runs); n > maxRuns {
		t.Fatalf("%d runs tracked, cap is %d", n, maxRuns)
	}
	if _, _, ok := r.Take(first); ok {
		t.Fatal("oldest run was not evicted")
	}
}
