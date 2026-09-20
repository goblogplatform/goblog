package directory

import (
	"fmt"
	"testing"
	"time"
)

func TestIPLimiter(t *testing.T) {
	now := time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC)
	l := newIPLimiter(2, time.Hour)
	l.now = func() time.Time { return now }
	if !l.Allow("a") || !l.Allow("a") {
		t.Fatal("first two attempts must pass")
	}
	if l.Allow("a") {
		t.Error("third attempt within the window must be refused")
	}
	if !l.Allow("b") {
		t.Error("another address is counted separately")
	}
	now = now.Add(61 * time.Minute)
	if !l.Allow("a") {
		t.Error("attempts outside the window no longer count")
	}
}

// TestIPLimiter_SweepPrunesExpiredAddresses checks that a burst of distinct,
// now-expired addresses is pruned once hits grows past sweepThreshold,
// rather than sitting in the map forever.
func TestIPLimiter_SweepPrunesExpiredAddresses(t *testing.T) {
	now := time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC)
	l := newIPLimiter(5, time.Minute)
	l.now = func() time.Time { return now }

	for i := 0; i < sweepThreshold+100; i++ {
		l.Allow(fmt.Sprintf("addr-%d", i))
	}
	if len(l.hits) != sweepThreshold+100 {
		t.Fatalf("expected %d addresses before the window elapses, got %d", sweepThreshold+100, len(l.hits))
	}

	now = now.Add(2 * time.Minute) // past window; every recorded hit is now stale
	l.Allow("addr-new")            // crosses sweepThreshold again, triggering the sweep

	if len(l.hits) != 1 {
		t.Errorf("sweep should have pruned every expired address, got %d entries left", len(l.hits))
	}
	if _, ok := l.hits["addr-new"]; !ok {
		t.Error("the address that triggered the sweep should survive it")
	}
}
