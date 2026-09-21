package directory

import (
	"net/netip"
	"sync"
	"time"
)

// sweepThreshold is how large hits is allowed to grow before Allow prunes
// every stale address at once, rather than only the one it was called for.
// Without this, an attacker who varies their address per request — trivial
// over IPv6, which hands a single attacker an effectively unlimited number
// of addresses — grows hits without bound.
const sweepThreshold = 1024

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

// bucket is the key ip counts against. An IPv6 address is reduced to its
// /64 — the prefix a single subscriber typically holds — so varying the
// low 64 bits does not hand one attacker 2^64 fresh allowances. IPv4
// (including IPv4-mapped IPv6) counts per address; anything unparseable
// is used as given.
func bucket(ip string) string {
	addr, err := netip.ParseAddr(ip)
	if err != nil {
		return ip
	}
	addr = addr.Unmap()
	if addr.Is4() {
		return addr.String()
	}
	return netip.PrefixFrom(addr, 64).Masked().String()
}

// Allow records an attempt from ip and reports whether it is within the
// limit. Refused attempts are not recorded, so a blocked client is not
// pushed further out by retrying.
func (l *ipLimiter) Allow(ip string) bool {
	ip = bucket(ip)
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	kept := l.hits[ip][:0]
	for _, t := range l.hits[ip] {
		if now.Sub(t) < l.window {
			kept = append(kept, t)
		}
	}
	allowed := len(kept) < l.limit
	if allowed {
		kept = append(kept, now)
	}
	if len(kept) == 0 {
		delete(l.hits, ip)
	} else {
		l.hits[ip] = kept
	}
	if len(l.hits) > sweepThreshold {
		l.sweep(now)
	}
	return allowed
}

// sweep drops every address whose most recent hit is already outside
// window, so a burst of distinct (and largely one-shot) addresses does not
// keep the map growing forever between the rare IPs that come back.
func (l *ipLimiter) sweep(now time.Time) {
	for ip, hits := range l.hits {
		if len(hits) == 0 || now.Sub(hits[len(hits)-1]) >= l.window {
			delete(l.hits, ip)
		}
	}
}
