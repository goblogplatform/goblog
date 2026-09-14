package directory

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

const fixtureIndex = `[{"name":"hello","display_name":"Hello","description":"Says hi","version":"1.0.0","author":"Jason","license":"GPL-3.0","source_url":"https://github.com/goblogplatform/goblog-plugin-hello","download_url":"https://raw.githubusercontent.com/goblogplatform/goblog-plugin-hello/v1.0.0/plugin.go","sha256":"abc","min_goblog_version":"0.2.6","install_type":"dynamic","released_at":"2026-09-14T00:00:00Z","detail_url":"DETAIL_URL"}]`

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
