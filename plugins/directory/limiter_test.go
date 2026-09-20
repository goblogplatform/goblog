package directory

import (
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
