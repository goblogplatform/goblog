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

// TestIPLimiter_IPv6ByPrefix: one IPv6 subscriber holds a whole /64, so
// addresses in the same /64 share a bucket; IPv4 (and IPv4-mapped IPv6)
// addresses are still counted individually, and a key that is not an
// address at all is used as given.
func TestIPLimiter_IPv6ByPrefix(t *testing.T) {
	l := newIPLimiter(1, time.Hour)
	if !l.Allow("2001:db8:1:2::1") {
		t.Fatal("first attempt from a /64 must pass")
	}
	if l.Allow("2001:db8:1:2:ffff::9") {
		t.Error("another address in the same /64 shares the bucket")
	}
	if !l.Allow("2001:db8:1:3::1") {
		t.Error("a different /64 is counted separately")
	}
	if !l.Allow("::ffff:203.0.113.5") || l.Allow("203.0.113.5") {
		t.Error("an IPv4-mapped address is its IPv4 address")
	}
	if !l.Allow("203.0.113.6") {
		t.Error("IPv4 neighbours are separate")
	}
	if !l.Allow("not an ip") || l.Allow("not an ip") {
		t.Error("an unparseable key is a bucket of its own")
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
