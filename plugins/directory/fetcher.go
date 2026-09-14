package directory

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"sync"
	"time"
)

// maxIndexBytes caps what is read from the registry so a misbehaving index
// URL cannot exhaust memory.
const maxIndexBytes = 8 << 20

// ensureRetryInterval is how long Ensure waits after a fetch attempt (success
// or failure) before it is willing to try again while the cache is still
// empty. Without this, every request served while the registry is down (or
// during startup, before the first scheduled refresh) would trigger its own
// synchronous, blocking HTTP fetch.
const ensureRetryInterval = 30 * time.Second

// Fetcher keeps an in-memory copy of the directory index and of the detail
// JSON for plugins that have been viewed. Every fetch that fails keeps the
// previous copy, so the directory degrades to "slightly stale" rather than
// "empty" when the registry is unreachable.
type Fetcher struct {
	client    *http.Client
	userAgent string

	mu          sync.RWMutex
	raw         []byte           // index.json bytes, served verbatim
	entries     []Entry          // parsed raw
	byName      map[string]Entry // entries keyed by Name
	details     map[string]*Detail
	fetchedAt   time.Time
	lastAttempt time.Time // set at the start of every Refresh, success or failure
}

// NewFetcher returns a Fetcher using client (nil means a 10s-timeout default).
func NewFetcher(client *http.Client) *Fetcher {
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	return &Fetcher{client: client, userAgent: "goblog-directory", details: map[string]*Detail{}}
}

// SetUserAgent sets the User-Agent sent to the registry.
func (f *Fetcher) SetUserAgent(ua string) { f.userAgent = ua }

// Refresh downloads and parses indexURL, replacing the cached index on
// success and re-fetching the details of every plugin already cached. On any
// error the previous index and details are kept and the error returned.
func (f *Fetcher) Refresh(indexURL string) error {
	f.mu.Lock()
	f.lastAttempt = time.Now()
	f.mu.Unlock()

	raw, err := f.get(indexURL)
	if err != nil {
		return fmt.Errorf("fetch index: %w", err)
	}
	var entries []Entry
	if err := json.Unmarshal(raw, &entries); err != nil {
		return fmt.Errorf("parse index: %w", err)
	}
	byName := make(map[string]Entry, len(entries))
	for _, e := range entries {
		byName[e.Name] = e
	}

	f.mu.Lock()
	f.raw, f.entries, f.byName, f.fetchedAt = raw, entries, byName, time.Now()
	cached := make([]string, 0, len(f.details))
	for name := range f.details {
		cached = append(cached, name)
	}
	f.mu.Unlock()

	// Refresh details we already hold; a failure keeps the old detail.
	for _, name := range cached {
		e, ok := byName[name]
		if !ok {
			f.mu.Lock()
			delete(f.details, name) // plugin left the registry
			f.mu.Unlock()
			continue
		}
		if d, err := f.fetchDetail(e); err == nil {
			f.mu.Lock()
			f.details[name] = d
			f.mu.Unlock()
		}
	}
	return nil
}

// Ensure fetches the index if nothing is cached yet, throttled to at most one
// attempt per ensureRetryInterval so a registry outage does not turn every
// request into a blocking synchronous fetch. Fetch errors are logged here;
// the cache is simply left empty (or stale, if there is an older copy).
func (f *Fetcher) Ensure(indexURL string) {
	f.mu.RLock()
	empty := f.raw == nil
	recentAttempt := time.Since(f.lastAttempt) < ensureRetryInterval
	f.mu.RUnlock()
	if !empty || recentAttempt {
		return
	}
	if err := f.Refresh(indexURL); err != nil {
		log.Printf("Directory plugin: initial index fetch failed: %v", err)
	}
}

// Index returns the cached index bytes and entries; ok is false when nothing
// has been fetched successfully yet.
func (f *Fetcher) Index() (raw []byte, entries []Entry, ok bool) {
	f.mu.RLock()
	defer f.mu.RUnlock()
	return f.raw, f.entries, f.raw != nil
}

// Entry looks a plugin up by name in the cached index.
func (f *Fetcher) Entry(name string) (Entry, bool) {
	f.mu.RLock()
	defer f.mu.RUnlock()
	e, ok := f.byName[name]
	return e, ok
}

// FetchedAt is when the index was last fetched successfully (zero if never).
func (f *Fetcher) FetchedAt() time.Time {
	f.mu.RLock()
	defer f.mu.RUnlock()
	return f.fetchedAt
}

// Detail returns the detail JSON for a plugin, fetching and caching it on
// first use. It fails when the plugin is not in the index or the fetch fails
// and no cached copy exists.
func (f *Fetcher) Detail(name string) (*Detail, error) {
	f.mu.RLock()
	d, cached := f.details[name]
	e, listed := f.byName[name]
	f.mu.RUnlock()
	if cached {
		return d, nil
	}
	if !listed {
		return nil, fmt.Errorf("plugin %q is not in the index", name)
	}
	d, err := f.fetchDetail(e)
	if err != nil {
		return nil, err
	}
	f.mu.Lock()
	f.details[name] = d
	f.mu.Unlock()
	return d, nil
}

func (f *Fetcher) fetchDetail(e Entry) (*Detail, error) {
	raw, err := f.get(e.DetailURL)
	if err != nil {
		return nil, fmt.Errorf("fetch detail for %s: %w", e.Name, err)
	}
	var d Detail
	if err := json.Unmarshal(raw, &d); err != nil {
		return nil, fmt.Errorf("parse detail for %s: %w", e.Name, err)
	}
	return &d, nil
}

func (f *Fetcher) get(url string) ([]byte, error) {
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", f.userAgent)
	req.Header.Set("Accept", "application/json")
	resp, err := f.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s: HTTP %d", url, resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, maxIndexBytes))
}
