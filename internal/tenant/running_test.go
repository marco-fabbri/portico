package tenant

import "testing"

func TestRunningCountsAcquiredTenants(t *testing.T) {
	s := &Service{running: map[string]bool{}}
	if s.Running() != 0 {
		t.Fatalf("Running() = %d at start", s.Running())
	}
	s.acquire("a")
	s.acquire("b")
	if s.Running() != 2 {
		t.Fatalf("Running() = %d with two acquired", s.Running())
	}
	s.release("a")
	if s.Running() != 1 {
		t.Fatalf("Running() = %d after one release", s.Running())
	}
}
