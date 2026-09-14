package directory

import (
	"goblog/blog"
	gplugin "goblog/plugin"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestPlugin_Identity(t *testing.T) {
	p := New()
	if p.Name() != "directory" || p.DisplayName() == "" || p.Version() == "" {
		t.Errorf("identity: %q %q %q", p.Name(), p.DisplayName(), p.Version())
	}
	defaults := map[string]string{}
	for _, s := range p.Settings() {
		defaults[s.Key] = s.DefaultValue
	}
	if defaults["enabled"] != "false" {
		t.Errorf("must be disabled by default, got %q", defaults["enabled"])
	}
	if defaults["index_url"] != "https://goblogplatform.github.io/plugins/index.json" {
		t.Errorf("index_url default: %q", defaults["index_url"])
	}
	if defaults["refresh_minutes"] != "15" {
		t.Errorf("refresh_minutes default: %q", defaults["refresh_minutes"])
	}
	pages := p.Pages()
	if len(pages) != 1 || pages[0].PageType != PageType || pages[0].Slug != "plugins" {
		t.Errorf("pages: %+v", pages)
	}
	var _ gplugin.Plugin = p
}

func TestSettingsHelpers(t *testing.T) {
	if got := indexURL(map[string]string{}); got != "https://goblogplatform.github.io/plugins/index.json" {
		t.Errorf("indexURL default: %q", got)
	}
	if got := indexURL(map[string]string{"index_url": " https://example.test/i.json "}); got != "https://example.test/i.json" {
		t.Errorf("indexURL trims: %q", got)
	}
	cases := map[string]time.Duration{"": 15 * time.Minute, "abc": 15 * time.Minute, "0": 15 * time.Minute, "-3": 15 * time.Minute, "5": 5 * time.Minute}
	for in, want := range cases {
		if got := refreshInterval(map[string]string{"refresh_minutes": in}); got != want {
			t.Errorf("refreshInterval(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestOnInit_CreatesPageOnce(t *testing.T) {
	db, _ := gorm.Open(sqlite.Open(":memory:"))
	db.AutoMigrate(&blog.Page{})
	p := New()
	if err := p.OnInit(db); err != nil {
		t.Fatal(err)
	}
	if err := p.OnInit(db); err != nil {
		t.Fatal(err)
	}
	var pages []blog.Page
	db.Where("page_type = ?", PageType).Find(&pages)
	if len(pages) != 1 {
		t.Fatalf("expected exactly one directory page, got %d", len(pages))
	}
	pg := pages[0]
	if pg.Slug != "plugins" || pg.Title != "Plugins" || !pg.ShowInNav || pg.NavOrder != 30 || !pg.Enabled {
		t.Errorf("page: %+v", pg)
	}
}

func TestOnInit_SlugCollision(t *testing.T) {
	db, _ := gorm.Open(sqlite.Open(":memory:"))
	db.AutoMigrate(&blog.Page{})
	existing := blog.Page{Title: "Mine", Slug: "plugins", PageType: blog.PageTypeCustom, Enabled: true}
	if err := db.Create(&existing).Error; err != nil {
		t.Fatal(err)
	}
	p := New()
	if err := p.OnInit(db); err != nil {
		t.Fatalf("OnInit must not fail when the slug is already taken, got %v", err)
	}
	var pages []blog.Page
	db.Where("slug = ?", "plugins").Find(&pages)
	if len(pages) != 1 {
		t.Fatalf("expected exactly one page with slug %q, got %d", "plugins", len(pages))
	}
	if pages[0].PageType != blog.PageTypeCustom {
		t.Errorf("the pre-existing page must be left alone, got page_type %q", pages[0].PageType)
	}
}

func TestScheduledJob_RefreshesWhenStale(t *testing.T) {
	srv := newFixtureServer(t)
	p := New()
	p.fetcher = NewFetcher(srv.Client())
	jobs := p.ScheduledJobs()
	if len(jobs) != 1 || jobs[0].Interval != time.Minute {
		t.Fatalf("jobs: %+v", jobs)
	}
	settings := map[string]string{"enabled": "true", "index_url": srv.URL + "/index.json", "refresh_minutes": "15"}

	// Empty cache: the job fetches.
	if err := jobs[0].Run(nil, settings); err != nil {
		t.Fatal(err)
	}
	if _, _, ok := p.fetcher.Index(); !ok {
		t.Fatal("job should have fetched the index")
	}
	// Fresh cache: the job is a no-op.
	before := srv.hits.Load()
	if err := jobs[0].Run(nil, settings); err != nil {
		t.Fatal(err)
	}
	if srv.hits.Load() != before {
		t.Error("job should not refetch a fresh index")
	}
	// Stale cache: the job fetches again.
	p.fetcher.mu.Lock()
	p.fetcher.fetchedAt = time.Now().Add(-16 * time.Minute)
	p.fetcher.mu.Unlock()
	if err := jobs[0].Run(nil, settings); err != nil {
		t.Fatal(err)
	}
	if srv.hits.Load() == before {
		t.Error("job should refetch a stale index")
	}
}

func TestScheduledJob_NoopWhenDisabled(t *testing.T) {
	srv := newFixtureServer(t)
	p := New()
	p.fetcher = NewFetcher(srv.Client())
	jobs := p.ScheduledJobs()

	// enabled unset: no-op, no HTTP hit, cache stays empty.
	if err := jobs[0].Run(nil, map[string]string{"index_url": srv.URL + "/index.json"}); err != nil {
		t.Fatal(err)
	}
	if srv.hits.Load() != 0 {
		t.Error("job should not have hit the server while enabled is unset")
	}
	if _, _, ok := p.fetcher.Index(); ok {
		t.Error("cache should stay empty while enabled is unset")
	}

	// enabled = "false": same.
	if err := jobs[0].Run(nil, map[string]string{"enabled": "false", "index_url": srv.URL + "/index.json"}); err != nil {
		t.Fatal(err)
	}
	if srv.hits.Load() != 0 {
		t.Error("job should not have hit the server while enabled is false")
	}
	if _, _, ok := p.fetcher.Index(); ok {
		t.Error("cache should stay empty while enabled is false")
	}

	// enabled = "true": the job fetches.
	if err := jobs[0].Run(nil, map[string]string{"enabled": "true", "index_url": srv.URL + "/index.json"}); err != nil {
		t.Fatal(err)
	}
	if srv.hits.Load() == 0 {
		t.Error("job should have hit the server once enabled")
	}
	if _, _, ok := p.fetcher.Index(); !ok {
		t.Error("cache should be populated once enabled")
	}
}

func newRenderCtx(t *testing.T, path, subPath string, settings map[string]string) (*gplugin.HookContext, *httptest.ResponseRecorder) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, path, nil)
	return &gplugin.HookContext{GinContext: c, Settings: settings, SubPath: subPath}, w
}

func TestRenderPage_Listing(t *testing.T) {
	srv := newFixtureServer(t)
	p := New()
	p.fetcher = NewFetcher(srv.Client())
	settings := map[string]string{"index_url": srv.URL + "/index.json"}

	// Wrong page type: declined.
	ctx, _ := newRenderCtx(t, "/plugins", "", settings)
	if tmpl, _ := p.RenderPage(ctx, "other"); tmpl != "" {
		t.Errorf("expected other page types to be declined, got %q", tmpl)
	}

	// Listing: fetched on first request, rendered into page_content.html.
	ctx, _ = newRenderCtx(t, "/plugins", "", settings)
	tmpl, data := p.RenderPage(ctx, PageType)
	if tmpl != "page_content.html" || data["has_plugin_content"] != true {
		t.Fatalf("tmpl=%q data=%v", tmpl, data)
	}
	html, _ := data["plugin_content"].(string)
	for _, want := range []string{`href="/plugins/hello"`, "Hello", "Says hi", "1.0.0", "Jason", "GPL-3.0", "dynamic", `href="/plugins/index.json"`, `href="https://github.com/goblogplatform/goblog-plugin-hello"`} {
		if !strings.Contains(html, want) {
			t.Errorf("listing missing %q in:\n%s", want, html)
		}
	}
}

func TestRenderPage_ListingEscapesIndexStrings(t *testing.T) {
	srv := newFixtureServer(t)
	srv.index.Store(func(w http.ResponseWriter) {
		w.Write([]byte(`[{"name":"evil","display_name":"<script>alert(1)</script>","description":"x","version":"1","source_url":"javascript:alert(1)","install_type":"dynamic"}]`))
	})
	p := New()
	p.fetcher = NewFetcher(srv.Client())
	ctx, _ := newRenderCtx(t, "/plugins", "", map[string]string{"index_url": srv.URL + "/index.json"})
	_, data := p.RenderPage(ctx, PageType)
	html, _ := data["plugin_content"].(string)
	if strings.Contains(html, "<script>") {
		t.Errorf("display_name must be escaped:\n%s", html)
	}
	if strings.Contains(html, `href="javascript:`) {
		t.Errorf("unsafe source_url must be neutralised:\n%s", html)
	}
}

func TestRenderPage_ListingUnavailable(t *testing.T) {
	p := New()
	p.fetcher = NewFetcher(&http.Client{Timeout: time.Second})
	ctx, _ := newRenderCtx(t, "/plugins", "", map[string]string{"index_url": "http://127.0.0.1:1/index.json"})
	tmpl, data := p.RenderPage(ctx, PageType)
	html, _ := data["plugin_content"].(string)
	if tmpl != "page_content.html" || !strings.Contains(html, "unavailable") {
		t.Errorf("expected an unavailable message, tmpl=%q html=%q", tmpl, html)
	}
}

func TestRenderPage_IndexJSON(t *testing.T) {
	srv := newFixtureServer(t)
	p := New()
	p.fetcher = NewFetcher(srv.Client())
	settings := map[string]string{"index_url": srv.URL + "/index.json"}

	ctx, w := newRenderCtx(t, "/plugins/index.json", "index.json", settings)
	if tmpl, _ := p.RenderPage(ctx, PageType); tmpl != "" {
		t.Errorf("index.json should be written directly, got template %q", tmpl)
	}
	raw, _, _ := p.fetcher.Index()
	if w.Code != http.StatusOK || w.Body.String() != string(raw) {
		t.Errorf("index.json must be served verbatim: code=%d body=%q", w.Code, w.Body.String())
	}
	if ct := w.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Errorf("content-type %q", ct)
	}
	if cc := w.Header().Get("Cache-Control"); cc != "public, max-age=300" {
		t.Errorf("cache-control %q", cc)
	}

	// Nothing cached: 503 JSON error.
	p2 := New()
	p2.fetcher = NewFetcher(&http.Client{Timeout: time.Second})
	ctx, w = newRenderCtx(t, "/plugins/index.json", "index.json", map[string]string{"index_url": "http://127.0.0.1:1/index.json"})
	p2.RenderPage(ctx, PageType)
	if w.Code != http.StatusServiceUnavailable || !strings.Contains(w.Body.String(), "error") {
		t.Errorf("expected 503 JSON error, got %d %q", w.Code, w.Body.String())
	}
}

func TestRenderPage_Detail(t *testing.T) {
	srv := newFixtureServer(t)
	p := New()
	p.fetcher = NewFetcher(srv.Client())
	settings := map[string]string{"index_url": srv.URL + "/index.json"}

	ctx, _ := newRenderCtx(t, "/plugins/hello", "hello", settings)
	tmpl, data := p.RenderPage(ctx, PageType)
	if tmpl != "page_content.html" || data["title"] != "Hello" {
		t.Fatalf("tmpl=%q title=%v", tmpl, data["title"])
	}
	html, _ := data["plugin_content"].(string)
	for _, want := range []string{"<h1>Hello</h1>", "<p>First</p>", "abc", "0.2.6", `href="https://raw.githubusercontent.com/goblogplatform/goblog-plugin-hello/v1.0.0/plugin.go"`, `href="/plugins"`, "2026-09-14"} {
		if !strings.Contains(html, want) {
			t.Errorf("detail missing %q in:\n%s", want, html)
		}
	}
	if strings.Contains(html, "Changelog") {
		t.Error("empty changelog should not render a Changelog section")
	}

	// Unknown, invalid and nested names are declined (blog turns that into a 404).
	for _, sub := range []string{"nope", "Bad Name", "../x", "hello/extra"} {
		// Only the raw request line needs a valid (percent-encoded) path; SubPath
		// is what RenderPage actually inspects, so it stays unescaped.
		ctx, w := newRenderCtx(t, "/plugins/"+strings.ReplaceAll(sub, " ", "%20"), sub, settings)
		if tmpl, _ := p.RenderPage(ctx, PageType); tmpl != "" || w.Body.Len() != 0 {
			t.Errorf("%q: expected to be declined, got tmpl=%q body=%q", sub, tmpl, w.Body.String())
		}
	}
}

func TestRenderPage_DeclinedSubPathDoesNotFetch(t *testing.T) {
	srv := newFixtureServer(t)
	p := New()
	p.fetcher = NewFetcher(srv.Client())
	settings := map[string]string{"index_url": srv.URL + "/index.json"}

	// The cache is empty and no successful fetch has happened yet, so a
	// declined sub-path must not trigger Ensure's synchronous fetch.
	ctx, w := newRenderCtx(t, "/plugins/Bad%20Name", "Bad Name", settings)
	if tmpl, _ := p.RenderPage(ctx, PageType); tmpl != "" || w.Body.Len() != 0 {
		t.Errorf("declined sub-path should be declined, got tmpl=%q body=%q", tmpl, w.Body.String())
	}
	if srv.hits.Load() != 0 {
		t.Errorf("declined sub-path must not hit the registry, hits=%d", srv.hits.Load())
	}
}

func TestRenderPage_DetailEscapesIndexStrings(t *testing.T) {
	srv := newFixtureServer(t)
	srv.index.Store(func(w http.ResponseWriter) {
		w.Write([]byte(`[{"name":"hello","display_name":"<script>alert(1)</script>","description":"x","version":"1","source_url":"javascript:alert(1)","download_url":"javascript:alert(2)","install_type":"dynamic","detail_url":"` + srv.URL + `/plugins/hello.json"}]`))
	})
	srv.detail.Store(func(w http.ResponseWriter) {
		w.Write([]byte(`{"name":"hello","display_name":"Hello","version":"1","readme_html":"<b>ok</b>","releases":[{"version":"1","url":"javascript:alert(3)"}]}`))
	})
	p := New()
	p.fetcher = NewFetcher(srv.Client())
	ctx, _ := newRenderCtx(t, "/plugins/hello", "hello", map[string]string{"index_url": srv.URL + "/index.json"})
	_, data := p.RenderPage(ctx, PageType)
	html, _ := data["plugin_content"].(string)
	if strings.Contains(html, "<script>") {
		t.Errorf("display_name must be escaped:\n%s", html)
	}
	if strings.Contains(html, `href="javascript:`) {
		t.Errorf("unsafe URLs (source_url, download_url, release url) must be neutralised:\n%s", html)
	}
	if !strings.Contains(html, "<b>ok</b>") {
		t.Errorf("readme_html is trusted, sanitized-by-the-registry HTML and must pass through raw:\n%s", html)
	}
}

func TestRenderPage_DetailFetchFails(t *testing.T) {
	srv := newFixtureServer(t)
	srv.detail.Store(func(w http.ResponseWriter) { w.WriteHeader(http.StatusInternalServerError) })
	p := New()
	p.fetcher = NewFetcher(srv.Client())
	ctx, _ := newRenderCtx(t, "/plugins/hello", "hello", map[string]string{"index_url": srv.URL + "/index.json"})
	tmpl, data := p.RenderPage(ctx, PageType)
	html, _ := data["plugin_content"].(string)
	if tmpl != "page_content.html" || !strings.Contains(html, "unavailable") || !strings.Contains(html, "Hello") {
		t.Errorf("a listed plugin whose detail fails should still show its index entry with a notice, got %q", html)
	}
}
