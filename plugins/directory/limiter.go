package directory

import (
	"sync"
	"time"
)

// ipLimiter allows at most limit attempts per address per window. It is
// in-memory on purpose: it only has to blunt a burst of public submissions
// (each of which costs GitHub API calls and a module instantiation), and
// resetting on restart is fine.
type ipLimiter struct {
	limit  int
	window time.Duration
	now    func() time.Time

	mu   sync.Mutex
	hits map[string][]time.Time
}

func newIPLimiter(limit int, window time.Duration) *ipLimiter {
	return &ipLimiter{limit: limit, window: window, now: time.Now, hits: map[string][]time.Time{}}
}

// Allow records an attempt from ip and reports whether it is within the
// limit. Refused attempts are not recorded, so a blocked client is not
// pushed further out by retrying.
func (l *ipLimiter) Allow(ip string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	kept := l.hits[ip][:0]
	for _, t := range l.hits[ip] {
		if now.Sub(t) < l.window {
			kept = append(kept, t)
		}
	}
	if len(kept) >= l.limit {
		l.hits[ip] = kept
		return false
	}
	l.hits[ip] = append(kept, now)
	return true
}
