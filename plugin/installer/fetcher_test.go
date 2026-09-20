package installer

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

const fixtureIndex = `[{"name":"hello","display_name":"Hello","description":"Says hi","version":"1.0.0","author":"Jason","license":"GPL-3.0","source_url":"https://github.com/goblogplatform/goblog-plugin-hello","download_url":"https://github.com/goblogplatform/goblog-plugin-hello/releases/download/v1.0.0/plugin.wasm","sha256":"abc","min_goblog_version":"0.2.6","install_type":"wasm","runtime":"wasm","allowed_hosts":["api.example.test"],"released_at":"2026-09-14T00:00:00Z","detail_url":"DETAIL_URL"}]`

const fixtureDetail = `{"name":"hello","display_name":"Hello","version":"1.0.0","readme_html":"<h1>Hello</h1>","changelog_html":"","releases":[{"version":"1.0.0","released_at":"2026-09-14T00:00:00Z","notes_html":"<p>First</p>","url":"https://github.com/goblogplatform/goblog-plugin-hello/releases/tag/v1.0.0"}]}`

// fixtureServer serves index.json and plugins/hello.json; the handlers can be
// swapped to simulate failures.
type fixtureServer struct {
	*httptest.Server
	index  atomic.Value // func(w http.ResponseWriter)
	detail atomic.Value
	hits   atomic.Int32
}

func newFixtureServer(t *testing.T) *fixtureServer {
	t.Helper()
	fs := &fixtureServer{}
	fs.index.Store(func(w http.ResponseWriter) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(strings.ReplaceAll(fixtureIndex, "DETAIL_URL", fs.URL+"/plugins/hello.json")))
	})
	fs.detail.Store(func(w http.ResponseWriter) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(fixtureDetail))
	})
	fs.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fs.hits.Add(1)
		switch r.URL.Path {
		case "/index.json":
			fs.index.Load().(func(http.ResponseWriter))(w)
		case "/plugins/hello.json":
			fs.detail.Load().(func(http.ResponseWriter))(w)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(fs.Close)
	return fs
}

func TestFetcher_RefreshAndLookup(t *testing.T) {
	srv := newFixtureServer(t)
	f := NewFetcher(srv.Client())

	if _, _, ok := f.Index(); ok {
		t.Fatal("expected nothing cached before the first refresh")
	}
	if err := f.Refresh(srv.URL + "/index.json"); err != nil {
		t.Fatalf("refresh: %v", err)
	}
	raw, entries, ok := f.Index()
	if !ok || len(entries) != 1 || entries[0].Name != "hello" || entries[0].Version != "1.0.0" {
		t.Fatalf("index: ok=%v entries=%+v", ok, entries)
	}
	if !strings.Contains(string(raw), `"name":"hello"`) {
		t.Errorf("raw bytes should be the served index verbatim, got %q", raw)
	}
	if e, ok := f.Entry("hello"); !ok || e.DisplayName != "Hello" {
		t.Errorf("Entry(hello): ok=%v e=%+v", ok, e)
	}
	if _, ok := f.Entry("nope"); ok {
		t.Error("Entry(nope) should not be found")
	}
	if f.FetchedAt().IsZero() {
		t.Error("FetchedAt should be set after a refresh")
	}

	d, err := f.Detail("hello")
	if err != nil {
		t.Fatalf("detail: %v", err)
	}
	if d.ReadmeHTML != "<h1>Hello</h1>" || len(d.Releases) != 1 || d.Releases[0].NotesHTML != "<p>First</p>" {
		t.Errorf("detail: %+v", d)
	}
	// Detail is cached: a second call does not hit the server.
	before := srv.hits.Load()
	if _, err := f.Detail("hello"); err != nil {
		t.Fatal(err)
	}
	if srv.hits.Load() != before {
		t.Error("second Detail call should be served from cache")
	}
	if _, err := f.Detail("nope"); err == nil {
		t.Error("Detail(nope) should fail")
	}
}

func TestFetcher_KeepsLastGoodCopyOnFailure(t *testing.T) {
	srv := newFixtureServer(t)
	f := NewFetcher(srv.Client())
	if err := f.Refresh(srv.URL + "/index.json"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Detail("hello"); err != nil {
		t.Fatal(err)
	}

	// HTTP 500: error returned, old index and details still served.
	srv.index.Store(func(w http.ResponseWriter) { w.WriteHeader(http.StatusInternalServerError) })
	if err := f.Refresh(srv.URL + "/index.json"); err == nil {
		t.Error("expected an error on HTTP 500")
	}
	if _, entries, ok := f.Index(); !ok || len(entries) != 1 {
		t.Errorf("index should be kept after a failed refresh: ok=%v n=%d", ok, len(entries))
	}
	if d, err := f.Detail("hello"); err != nil || d.ReadmeHTML == "" {
		t.Errorf("detail should be kept after a failed refresh: %v", err)
	}

	// Malformed JSON: same.
	srv.index.Store(func(w http.ResponseWriter) { w.Write([]byte(`{not json`)) })
	if err := f.Refresh(srv.URL + "/index.json"); err == nil {
		t.Error("expected an error on malformed JSON")
	}
	if _, entries, ok := f.Index(); !ok || len(entries) != 1 {
		t.Error("index should be kept after malformed JSON")
	}

	// Unreachable host: same.
	if err := f.Refresh("http://127.0.0.1:1/index.json"); err == nil {
		t.Error("expected an error for an unreachable host")
	}
	if _, _, ok := f.Index(); !ok {
		t.Error("index should be kept after a connection error")
	}
}

func TestFetcher_RefreshUpdatesCachedDetails(t *testing.T) {
	srv := newFixtureServer(t)
	f := NewFetcher(srv.Client())
	if err := f.Refresh(srv.URL + "/index.json"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Detail("hello"); err != nil {
		t.Fatal(err)
	}
	srv.detail.Store(func(w http.ResponseWriter) {
		w.Write([]byte(strings.Replace(fixtureDetail, "<h1>Hello</h1>", "<h1>Hello v2</h1>", 1)))
	})
	if err := f.Refresh(srv.URL + "/index.json"); err != nil {
		t.Fatal(err)
	}
	if d, _ := f.Detail("hello"); d.ReadmeHTML != "<h1>Hello v2</h1>" {
		t.Errorf("cached detail should be re-fetched on refresh, got %q", d.ReadmeHTML)
	}
}

func TestFetcher_Ensure(t *testing.T) {
	srv := newFixtureServer(t)
	f := NewFetcher(srv.Client())
	f.Ensure(srv.URL + "/index.json")
	if _, _, ok := f.Index(); !ok {
		t.Fatal("Ensure should fetch when nothing is cached")
	}
	before := srv.hits.Load()
	f.Ensure(srv.URL + "/index.json")
	if srv.hits.Load() != before {
		t.Error("Ensure should not fetch again when a copy is cached")
	}
}

// TestFetcher_EnsureThrottlesWhileCacheStaysEmpty verifies that when the
// registry is unreachable (so the cache never gets populated), Ensure only
// attempts a fetch once per ensureRetryInterval instead of on every call -
// otherwise every request served while the registry is down would block on
// its own synchronous HTTP fetch.
func TestFetcher_EnsureThrottlesWhileCacheStaysEmpty(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)

	f := NewFetcher(srv.Client())

	f.Ensure(srv.URL + "/index.json")
	f.Ensure(srv.URL + "/index.json")
	if got := hits.Load(); got != 1 {
		t.Errorf("two Ensure calls within the retry window should attempt one fetch, got %d", got)
	}
	if _, _, ok := f.Index(); ok {
		t.Fatal("index should still be empty; the server always fails")
	}

	// Simulate the retry window having elapsed.
	f.mu.Lock()
	f.lastAttempt = time.Now().Add(-time.Minute)
	f.mu.Unlock()

	f.Ensure(srv.URL + "/index.json")
	if got := hits.Load(); got != 2 {
		t.Errorf("Ensure should retry once the retry window has elapsed, got %d hits", got)
	}
}

// TestFetcher_EnsureConcurrentBurstFetchesOnce covers the first-contact case:
// many requests arriving at once on an empty cache must produce one fetch,
// not one per request.
func TestFetcher_EnsureConcurrentBurstFetchesOnce(t *testing.T) {
	var hits atomic.Int32
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		<-release // hold the first fetch open while the burst arrives
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)

	f := NewFetcher(srv.Client())
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			f.Ensure(srv.URL + "/index.json")
		}()
	}
	// Give the goroutines time to reach Ensure, then let the fetch finish.
	time.Sleep(50 * time.Millisecond)
	close(release)
	wg.Wait()
	if got := hits.Load(); got != 1 {
		t.Errorf("a concurrent burst on an empty cache should fetch once, got %d", got)
	}
}

// TestFetcher_EnsureUnreachableReturnsQuickly guards against a regression
// where Ensure blocks on a full HTTP timeout on every call: with an
// unreachable address, a second Ensure call (within the retry window) must
// not attempt another connection and should return essentially immediately.
func TestFetcher_EnsureUnreachableReturnsQuickly(t *testing.T) {
	f := NewFetcher(&http.Client{Timeout: 2 * time.Second})
	f.Ensure("http://127.0.0.1:1/index.json")

	start := time.Now()
	f.Ensure("http://127.0.0.1:1/index.json")
	if elapsed := time.Since(start); elapsed > 100*time.Millisecond {
		t.Errorf("second Ensure call should be throttled and return immediately, took %v", elapsed)
	}
}
