package api

import (
	"testing"
	"time"
)

func TestLimiterDelayGrowsAndResets(t *testing.T) {
	l := NewLimiter()
	now := time.Date(2026, 10, 18, 8, 0, 0, 0, time.UTC)
	l.now = func() time.Time { return now }
	want := []time.Duration{0, 0, 0, time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second, 8 * time.Second}
	for i, d := range want {
		l.Failed("10.0.0.1")
		if got := l.Delay("10.0.0.1"); got != d {
			t.Fatalf("after %d failures Delay = %v, want %v", i+1, got, d)
		}
	}
	l.Reset("10.0.0.1")
	if got := l.Delay("10.0.0.1"); got != 0 {
		t.Fatalf("after Reset Delay = %v, want 0", got)
	}
}

func TestLimiterIsPerSource(t *testing.T) {
	l := NewLimiter()
	for i := 0; i < 6; i++ {
		l.Failed("a")
	}
	if l.Delay("b") != 0 {
		t.Fatal("failures on a delayed b")
	}
	if l.Delay("a") == 0 {
		t.Fatal("a is not delayed")
	}
}

func TestLimiterSweepsIdleSources(t *testing.T) {
	l := NewLimiter()
	now := time.Date(2026, 10, 18, 8, 0, 0, 0, time.UTC)
	l.now = func() time.Time { return now }
	for i := 0; i < 6; i++ {
		l.Failed("a")
	}
	now = now.Add(11 * time.Minute)
	if l.Delay("a") != 0 {
		t.Fatal("an idle source is still delayed")
	}
	l.Sweep(now)
	if n := l.len(); n != 0 {
		t.Fatalf("entries after Sweep = %d, want 0", n)
	}
}
