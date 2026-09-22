package directory

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"goblog/blog"
	gplugin "goblog/plugin"
	"goblog/plugins/directory/registry"

	"github.com/gin-gonic/gin"
)

func TestPlugin_Identity(t *testing.T) {
	p := New()
	if p.Name() != "directory" || p.DisplayName() == "" || p.Version() == "" {
		t.Errorf("identity: %q %q %q", p.Name(), p.DisplayName(), p.Version())
	}
	defaults, types := map[string]string{}, map[string]string{}
	for _, s := range p.Settings() {
		defaults[s.Key], types[s.Key] = s.DefaultValue, s.Type
	}
	if defaults["enabled"] != "false" || defaults["refresh_minutes"] != "360" || defaults["github_token"] != "" || types["github_token"] != "password" {
		t.Errorf("settings: defaults=%v types=%v", defaults, types)
	}
	if _, ok := defaults["index_url"]; ok {
		t.Error("index_url is gone: the directory is built here, not mirrored")
	}
	pages := p.Pages()
	if len(pages) != 2 || pages[0].PageType != PageType || pages[0].Slug != "plugins" {
		t.Errorf("pages: %+v", pages)
	}
	if pages[1].PageType != ThemePageType || pages[1].Slug != "themes" || pages[1].NavOrder != 31 {
		t.Errorf("theme page: %+v", pages[1])
	}
	var _ gplugin.Plugin = p
}

func TestRefreshInterval(t *testing.T) {
	cases := map[string]time.Duration{"": 360 * time.Minute, "abc": 360 * time.Minute, "0": 360 * time.Minute, "-3": 360 * time.Minute,
		"5": 15 * time.Minute, "15": 15 * time.Minute, "60": 60 * time.Minute}
	for in, want := range cases {
		if got := refreshInterval(map[string]string{"refresh_minutes": in}); got != want {
			t.Errorf("refreshInterval(%q) = %v, want %v", in, got, want)
		}
	}
}

// pluginFixture is an initialised plugin over sqlite with the fake GitHub
// from service_test.go behind it.
type pluginFixture struct {
	*fixture
	p *Plugin
}

func newPluginFixture(t *testing.T) *pluginFixture {
	t.Helper()
	f := newFixture(t)
	f.db.AutoMigrate(&blog.Page{}, &blog.PostType{}, &blog.Setting{}, &gplugin.PluginSetting{})
	f.db.Create(&blog.Setting{Key: "site_url", Value: "https://example.test"})
	p := New()
	p.newSource = func(string) registry.Source { return f.src }
	p.validator = f.val
	p.SetThemeValidator(f.tv)
	if err := p.OnInit(f.db); err != nil {
		t.Fatal(err)
	}
	f.svc = p.Service()
	return &pluginFixture{fixture: f, p: p}
}

func TestOnInit_MigratesCreatesPageAndService(t *testing.T) {
	f := newPluginFixture(t)
	if err := f.p.OnInit(f.db); err != nil {
		t.Fatal(err)
	}
	var pages []blog.Page
	f.db.Where("page_type = ?", PageType).Find(&pages)
	if len(pages) != 1 || pages[0].Slug != "plugins" || pages[0].Title != "Plugins" || !pages[0].ShowInNav || pages[0].NavOrder != 30 || !pages[0].Enabled {
		t.Errorf("pages: %+v", pages)
	}
	var themePages []blog.Page
	f.db.Where("page_type = ?", ThemePageType).Find(&themePages)
	if len(themePages) != 1 || themePages[0].Slug != "themes" || themePages[0].Title != "Themes" {
		t.Errorf("theme pages: %+v", themePages)
	}
	if f.p.Service() == nil || !f.db.Migrator().HasTable(&Repo{}) || !f.db.Migrator().HasTable(&Build{}) {
		t.Error("OnInit must migrate the tables and create the service")
	}
	if f.p.Hosted() {
		t.Error("not hosted until enabled")
	}
	f.db.Create(&gplugin.PluginSetting{PluginName: "directory", Key: "enabled", Value: "true"})
	f.db.Create(&gplugin.PluginSetting{PluginName: "directory", Key: "github_token", Value: "tok"})
	if !f.p.Hosted() || f.p.Token() != "tok" {
		t.Errorf("hosted=%v token=%q", f.p.Hosted(), f.p.Token())
	}
}

func TestOnInit_SlugCollision(t *testing.T) {
	db := testDB(t)
	db.AutoMigrate(&blog.Page{}, &blog.PostType{})
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
	if len(pages) != 1 || pages[0].PageType != blog.PageTypeCustom {
		t.Errorf("the pre-existing page must be left alone: %+v", pages)
	}
}

func TestOnInit_PostTypeSlugCollision(t *testing.T) {
	db := testDB(t)
	db.AutoMigrate(&blog.Page{}, &blog.PostType{})
	if err := db.Create(&blog.PostType{Name: "Themes", Slug: "themes"}).Error; err != nil {
		t.Fatal(err)
	}
	p := New()
	if err := p.OnInit(db); err != nil {
		t.Fatalf("OnInit must not fail when a post type has the slug, got %v", err)
	}
	var count int64
	db.Model(&blog.Page{}).Where("slug = ?", "themes").Count(&count)
	if count != 0 {
		t.Errorf("the post type's slug must be left alone, got %d pages at it", count)
	}
}

func TestScheduledJob(t *testing.T) {
	f := newPluginFixture(t)
	r, _ := f.svc.Add(context.Background(), KindPlugin, "o/hello", "")
	jobs := f.p.ScheduledJobs()
	if len(jobs) != 1 || jobs[0].Interval != time.Minute {
		t.Fatalf("jobs: %+v", jobs)
	}
	f.src.repos["o/hello"].stars = 50

	// Disabled: nothing happens.
	if err := jobs[0].Run(f.db, map[string]string{"enabled": "false"}); err != nil {
		t.Fatal(err)
	}
	if !f.svc.LastRefresh().IsZero() {
		t.Fatal("disabled plugin must not refresh")
	}
	// Enabled and never refreshed: refreshes.
	if err := jobs[0].Run(f.db, map[string]string{"enabled": "true", "refresh_minutes": "15"}); err != nil {
		t.Fatal(err)
	}
	if v, _, _ := f.svc.Get(r.ID); v.Stars != 50 {
		t.Errorf("stars after refresh = %d", v.Stars)
	}
	// Fresh: no-op.
	f.src.repos["o/hello"].stars = 51
	jobs[0].Run(f.db, map[string]string{"enabled": "true", "refresh_minutes": "15"})
	if v, _, _ := f.svc.Get(r.ID); v.Stars != 50 {
		t.Error("a fresh index must not be refreshed again")
	}
}

func newRenderCtx(t *testing.T, method, path, subPath string, form url.Values) (*gplugin.HookContext, *httptest.ResponseRecorder) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	// httptest.NewRequest builds the request by parsing a raw "METHOD
	// target HTTP/1.0" line; a literal space in path (as in the "Bad Name"
	// case below) breaks that parsing, so percent-encode it first. This
	// only affects how the request line is built — SubPath is passed
	// separately and unaffected. A "?query" suffix is kept as the query.
	path, rawQuery, _ := strings.Cut(path, "?")
	target := (&url.URL{Path: path, RawQuery: rawQuery}).String()
	if form != nil {
		c.Request = httptest.NewRequest(method, target, strings.NewReader(form.Encode()))
		c.Request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	} else {
		c.Request = httptest.NewRequest(method, target, nil)
	}
	c.Request.RemoteAddr = "203.0.113.5:1234"
	return &gplugin.HookContext{GinContext: c, Settings: map[string]string{"enabled": "true"}, SubPath: subPath}, w
}

func content(t *testing.T, data gin.H) string {
	t.Helper()
	html, _ := data["plugin_content"].(string)
	if data["has_plugin_content"] != true || html == "" {
		t.Fatalf("expected plugin content, got %v", data)
	}
	return html
}

func TestRenderPage_Listing(t *testing.T) {
	f := newPluginFixture(t)
	ctx, _ := newRenderCtx(t, http.MethodGet, "/plugins", "", nil)
	if tmpl, _ := f.p.RenderPage(ctx, "other"); tmpl != "" {
		t.Errorf("other page types are declined, got %q", tmpl)
	}

	// Empty directory.
	tmpl, data := f.p.RenderPage(ctx, PageType)
	if tmpl != "page_content.html" || !strings.Contains(content(t, data), "directory is empty") {
		t.Errorf("empty listing: %q %v", tmpl, data)
	}

	f.svc.Add(context.Background(), KindPlugin, "o/zeta", "")
	f.svc.Add(context.Background(), KindPlugin, "o/hello", "")
	ctx, _ = newRenderCtx(t, http.MethodGet, "/plugins", "", nil)
	_, data = f.p.RenderPage(ctx, PageType)
	html := content(t, data)
	for _, want := range []string{`href="/plugins/hello"`, `href="/plugins/zeta"`, "HELLO", "Says hi.", "v1.0.0", "Jason", "MIT",
		`href="/plugins/index.json"`, `href="https://github.com/o/hello"`, `href="/plugins/submit"`, "★ 7", "No network access"} {
		if !strings.Contains(html, want) {
			t.Errorf("listing missing %q in:\n%s", want, html)
		}
	}
	// The listing is a stacked list, not a table: a six-column table
	// squeezed the description into a narrow column and overflowed the
	// content container (#607).
	if strings.Contains(html, "<table") {
		t.Error("listing must not be a table")
	}
	if !strings.Contains(html, `<p class="mb-1">Says hi.</p>`) {
		t.Errorf("description must be a full-width paragraph in:\n%s", html)
	}
	if strings.Index(html, "/plugins/hello") > strings.Index(html, "/plugins/zeta") {
		t.Error("listing must be sorted by stars, hello (7) before zeta (1)")
	}
	if strings.Contains(html, "issues/new") {
		t.Error("the GitHub issue submission box is gone")
	}
}

func TestRenderPage_ListingEscapesStrings(t *testing.T) {
	f := newPluginFixture(t)
	r := seed(t, f.db, KindPlugin, "o/evil", StatusApproved, func() registry.DetailDoc {
		d := doc("evil", "1.0.0", 0)
		d.DisplayName, d.SourceURL = "<script>alert(1)</script>", "javascript:alert(1)"
		return d
	}())
	_ = r
	ctx, _ := newRenderCtx(t, http.MethodGet, "/plugins", "", nil)
	_, data := f.p.RenderPage(ctx, PageType)
	html := content(t, data)
	if strings.Contains(html, "<script>alert(1)") || strings.Contains(html, `href="javascript:`) {
		t.Errorf("unescaped output:\n%s", html)
	}
}

func TestRenderPage_IndexJSON(t *testing.T) {
	f := newPluginFixture(t)
	ctx, w := newRenderCtx(t, http.MethodGet, "/plugins/index.json", "index.json", nil)
	if tmpl, _ := f.p.RenderPage(ctx, PageType); tmpl != "" {
		t.Errorf("raw responses return no template, got %q", tmpl)
	}
	if w.Code != http.StatusOK || strings.TrimSpace(w.Body.String()) != "[]" || w.Header().Get("Content-Type") != "application/json" {
		t.Errorf("empty index: %d %q %q", w.Code, w.Body.String(), w.Header().Get("Content-Type"))
	}
	f.svc.Add(context.Background(), KindPlugin, "o/hello", "")
	ctx, w = newRenderCtx(t, http.MethodGet, "/plugins/index.json", "index.json", nil)
	f.p.RenderPage(ctx, PageType)
	raw, _ := f.svc.Index(KindPlugin)
	if w.Code != http.StatusOK || w.Body.String() != string(raw) || w.Header().Get("Cache-Control") != "public, max-age=300" {
		t.Errorf("index.json: %d %q cache=%q", w.Code, w.Body.String(), w.Header().Get("Cache-Control"))
	}
}

func TestRenderPage_DetailAndDetailJSON(t *testing.T) {
	f := newPluginFixture(t)
	f.svc.Add(context.Background(), KindPlugin, "o/hello", "")
	z, _ := f.svc.Submit(context.Background(), KindPlugin, "o/zeta", "ip", "")

	ctx, _ := newRenderCtx(t, http.MethodGet, "/plugins/hello", "hello", nil)
	tmpl, data := f.p.RenderPage(ctx, PageType)
	html := content(t, data)
	if tmpl != "page_content.html" || data["title"] != "HELLO" {
		t.Errorf("detail: %q title=%v", tmpl, data["title"])
	}
	for _, want := range []string{"<p># Hello</p>", "1.0.0", "MIT", `href="https://github.com/o/hello"`, "0.3.0", "No network access", "<p>notes</p>",
		`href="https://github.com/o/hello/releases/download/v1.0.0/plugin.wasm"`, registry.Sum([]byte("\x00asm hello"))} {
		if !strings.Contains(html, want) {
			t.Errorf("detail missing %q in:\n%s", want, html)
		}
	}

	ctx, w := newRenderCtx(t, http.MethodGet, "/plugins/hello.json", "hello.json", nil)
	if tmpl, _ := f.p.RenderPage(ctx, PageType); tmpl != "" || w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"readme_html": "<p># Hello</p>"`) {
		t.Errorf("hello.json: %q %d %s", tmpl, w.Code, w.Body.String())
	}

	// Pending, unknown and malformed names are declined (blog renders 404).
	for _, sp := range []string{"zeta", "zeta.json", "nope", "nope.json", "Bad Name", "../x"} {
		ctx, _ := newRenderCtx(t, http.MethodGet, "/plugins/"+sp, sp, nil)
		if tmpl, _ := f.p.RenderPage(ctx, PageType); tmpl != "" {
			t.Errorf("%q should be declined, got %q", sp, tmpl)
		}
	}
	_ = z
}

func TestRenderPage_DetailFlagsBroadHosts(t *testing.T) {
	f := newPluginFixture(t)
	seed(t, f.db, KindPlugin, "o/wide", StatusApproved, func() registry.DetailDoc {
		d := doc("wide", "1.0.0", 0)
		d.AllowedHosts = []string{"api.example.test", "*.example.test", "localhost"}
		return d
	}())
	ctx, _ := newRenderCtx(t, http.MethodGet, "/plugins/wide", "wide", nil)
	_, data := f.p.RenderPage(ctx, PageType)
	html := content(t, data)
	for _, want := range []string{"api.example.test", "wildcard", "local network"} {
		if !strings.Contains(html, want) {
			t.Errorf("missing %q in:\n%s", want, html)
		}
	}
}

func TestHostNote(t *testing.T) {
	for host, want := range map[string]string{
		"api.example.test": "", "*": "any host", "*.example.test": "wildcard", "api.*": "wildcard",
		"localhost": "local network", "LOCALHOST": "local network", "::1": "local network",
		"127.0.0.1": "local network", "10.1.2.3": "local network", "192.168.1.1": "local network", "169.254.169.254": "local network",
		"10.example.test": "local network", "192.0.2.1": "", "172.16.0.1": "local network", "172.32.0.1": "", "fd12::1": "local network", "fc00::1": "local network", "fe80::1": "local network",
		"fd.example.test": "", "feed::1": "", "2001:db8::1": "",
	} {
		if got := hostNote(host); got != want {
			t.Errorf("hostNote(%q) = %q, want %q", host, got, want)
		}
	}
}

func TestBasePathFollowsSlug(t *testing.T) {
	f := newPluginFixture(t)
	f.svc.Add(context.Background(), KindPlugin, "o/hello", "")
	ctx, _ := newRenderCtx(t, http.MethodGet, "/extensions", "", nil)
	_, data := f.p.RenderPage(ctx, PageType)
	if html := content(t, data); !strings.Contains(html, `href="/extensions/hello"`) || !strings.Contains(html, `href="/extensions/submit"`) {
		t.Errorf("links must follow the page slug:\n%s", html)
	}
}

func TestRenderPage_ThemesListingDetailAndIndex(t *testing.T) {
	f := newPluginFixture(t)
	f.svc.Add(context.Background(), KindPlugin, "o/hello", "")
	f.svc.Add(context.Background(), KindTheme, "o/ocean", "")

	ctx, _ := newRenderCtx(t, http.MethodGet, "/themes", "", nil)
	tmpl, data := f.p.RenderPage(ctx, ThemePageType)
	html := content(t, data)
	if tmpl != "page_content.html" {
		t.Fatalf("tmpl = %q", tmpl)
	}
	for _, want := range []string{`href="/themes/ocean"`, `src="https://raw.test/o/ocean/v1.0.0/screenshot.png"`, "OCEAN", "v1.0.0", "★ 3", `href="/themes/submit"`, `href="/themes/index.json"`} {
		if !strings.Contains(html, want) {
			t.Errorf("themes listing missing %q in:\n%s", want, html)
		}
	}
	if strings.Contains(html, "/themes/hello") || strings.Contains(html, "HELLO") {
		t.Error("plugins must not appear in the theme listing")
	}

	ctx, w := newRenderCtx(t, http.MethodGet, "/themes/index.json", "index.json", nil)
	f.p.RenderPage(ctx, ThemePageType)
	if !strings.Contains(w.Body.String(), `"kind": "theme"`) || strings.Contains(w.Body.String(), `"name": "hello"`) {
		t.Errorf("themes index = %s", w.Body.String())
	}

	ctx, _ = newRenderCtx(t, http.MethodGet, "/themes/ocean", "ocean", nil)
	_, data = f.p.RenderPage(ctx, ThemePageType)
	html = content(t, data)
	for _, want := range []string{`src="https://raw.test/o/ocean/v1.0.0/screenshot.png"`, "<p># Ocean</p>", "0.5.0", `href="https://github.com/o/ocean/archive/refs/tags/v1.0.0.zip"`, "content hash"} {
		if !strings.Contains(html, want) {
			t.Errorf("theme detail missing %q in:\n%s", want, html)
		}
	}
	if data["title"] != "OCEAN" {
		t.Errorf("title = %v", data["title"])
	}

	// Kinds never cross pages.
	ctx, _ = newRenderCtx(t, http.MethodGet, "/plugins/ocean", "ocean", nil)
	if tmpl, _ := f.p.RenderPage(ctx, PageType); tmpl != "" {
		t.Error("/plugins/<theme name> must 404")
	}
	ctx, _ = newRenderCtx(t, http.MethodGet, "/themes/hello", "hello", nil)
	if tmpl, _ := f.p.RenderPage(ctx, ThemePageType); tmpl != "" {
		t.Error("/themes/<plugin name> must 404")
	}
	ctx, _ = newRenderCtx(t, http.MethodGet, "/themes", "", nil)
	if tmpl, _ := f.p.RenderPage(ctx, "other"); tmpl != "" {
		t.Error("unknown page types are declined")
	}
}

func TestMatches(t *testing.T) {
	e := Entry{Name: "hello-world", DisplayName: "Hello World", Description: "Says hi to visitors.", Author: "Jason"}
	for _, q := range []string{"hello", "HELLO", "hello-w", "says HI", "jason", "  visitors "} {
		if !matches(e, q) {
			t.Errorf("%q should match", q)
		}
	}
	for _, q := range []string{"goodbye", "MIT", "hello  world"} {
		if matches(e, q) {
			t.Errorf("%q should not match", q)
		}
	}
	if !matches(e, "") {
		t.Error("an empty query matches everything")
	}
}

// TestRenderPage_ListingFilter: ?q= narrows the plugin and theme listings
// (#608); the pages carry a search form either way.
func TestRenderPage_ListingFilter(t *testing.T) {
	f := newPluginFixture(t)
	f.svc.Add(context.Background(), KindPlugin, "o/zeta", "")
	f.svc.Add(context.Background(), KindPlugin, "o/hello", "")
	f.svc.Add(context.Background(), KindTheme, "o/ocean", "")

	ctx, _ := newRenderCtx(t, http.MethodGet, "/plugins", "", nil)
	html := content(t, must(f.p.RenderPage(ctx, PageType)))
	if !strings.Contains(html, `<form`) || !strings.Contains(html, `name="q"`) || !strings.Contains(html, `action="/plugins"`) {
		t.Errorf("listing needs a search form posting to the page:\n%s", html)
	}

	ctx, _ = newRenderCtx(t, http.MethodGet, "/plugins?q=HELLO", "", nil)
	html = content(t, must(f.p.RenderPage(ctx, PageType)))
	if !strings.Contains(html, `href="/plugins/hello"`) || strings.Contains(html, `href="/plugins/zeta"`) {
		t.Errorf("filtered listing must list hello only:\n%s", html)
	}
	for _, want := range []string{"1 plugin matching", "HELLO", `value="HELLO"`} {
		if !strings.Contains(html, want) {
			t.Errorf("filtered listing missing %q in:\n%s", want, html)
		}
	}

	ctx, _ = newRenderCtx(t, http.MethodGet, "/plugins?q=nothing-here", "", nil)
	html = content(t, must(f.p.RenderPage(ctx, PageType)))
	if !strings.Contains(html, "No plugins match") || strings.Contains(html, "directory is empty") || !strings.Contains(html, `href="/plugins"`) {
		t.Errorf("no-match listing must say so and link back to the full list:\n%s", html)
	}

	ctx, _ = newRenderCtx(t, http.MethodGet, "/themes?q=ocean", "", nil)
	html = content(t, must(f.p.RenderPage(ctx, ThemePageType)))
	if !strings.Contains(html, `href="/themes/ocean"`) || !strings.Contains(html, "1 theme matching") || !strings.Contains(html, `action="/themes"`) {
		t.Errorf("themes listing must filter too:\n%s", html)
	}
	ctx, _ = newRenderCtx(t, http.MethodGet, "/themes?q=zzz", "", nil)
	if html = content(t, must(f.p.RenderPage(ctx, ThemePageType))); !strings.Contains(html, "No themes match") {
		t.Errorf("themes no-match:\n%s", html)
	}
}

func must(tmpl string, data gin.H) gin.H { return data }

// TestSearch: the plugin answers the site search with matching plugins and
// themes, linking to their pages under the slugs the admin gave them.
func TestSearch(t *testing.T) {
	f := newPluginFixture(t)
	var _ gplugin.Searcher = f.p
	f.svc.Add(context.Background(), KindPlugin, "o/zeta", "")
	f.svc.Add(context.Background(), KindPlugin, "o/hello", "")
	f.svc.Add(context.Background(), KindTheme, "o/ocean", "")
	f.db.Model(&blog.Page{}).Where("page_type = ?", ThemePageType).Update("slug", "skins")

	// Every fake repo is by "Jason": plugins first (most-starred first),
	// then themes.
	ctx, _ := newRenderCtx(t, http.MethodGet, "/search?q=jason", "", nil)
	ctx.DB = f.db
	got := f.p.Search(ctx, "jason")
	if len(got) != 3 {
		t.Fatalf("results = %+v", got)
	}
	if got[0].Title != "HELLO" || got[0].URL != "/plugins/hello" || got[0].Kind != "Plugin" || got[0].Summary != "Says hi." {
		t.Errorf("plugin result = %+v", got[0])
	}
	if got[1].URL != "/plugins/zeta" {
		t.Errorf("second result = %+v", got[1])
	}
	if got[2].Title != "OCEAN" || got[2].URL != "/skins/ocean" || got[2].Kind != "Theme" {
		t.Errorf("theme result = %+v", got[2])
	}
	if r := f.p.Search(ctx, "zeta"); len(r) != 1 || r[0].URL != "/plugins/zeta" {
		t.Errorf("zeta = %+v", r)
	}
	if r := f.p.Search(ctx, "nothing-here"); len(r) != 0 {
		t.Errorf("no match = %+v", r)
	}
	if r := New().Search(ctx, "jason"); r != nil {
		t.Error("an uninitialised plugin has nothing to search")
	}
}

// TestSitemap: every approved plugin and theme page is in the sitemap, under
// the slug the admin gave each page, with its release date as lastmod.
func TestSitemap(t *testing.T) {
	f := newPluginFixture(t)
	var _ gplugin.Sitemapper = f.p
	f.svc.Add(context.Background(), KindPlugin, "o/hello", "")
	f.svc.Add(context.Background(), KindTheme, "o/ocean", "")
	f.db.Model(&blog.Page{}).Where("page_type = ?", ThemePageType).Update("slug", "skins")
	ctx, _ := newRenderCtx(t, http.MethodGet, "/sitemap.xml", "", nil)
	ctx.DB = f.db
	got := f.p.Sitemap(ctx)
	if len(got) != 2 || got[0].Loc != "/plugins/hello" || got[1].Loc != "/skins/ocean" {
		t.Fatalf("urls = %+v", got)
	}
	if got[0].LastMod.IsZero() || got[0].LastMod.Year() != 2026 {
		t.Errorf("lastmod = %v", got[0].LastMod)
	}
	if New().Sitemap(ctx) != nil {
		t.Error("uninitialised plugin lists nothing")
	}
}

// TestRenderPage_DetailDescribesItself: a plugin or theme page gives the
// <head> its own description and title.
func TestRenderPage_DetailDescribesItself(t *testing.T) {
	f := newPluginFixture(t)
	f.svc.Add(context.Background(), KindPlugin, "o/hello", "")
	ctx, _ := newRenderCtx(t, http.MethodGet, "/plugins/hello", "hello", nil)
	_, data := f.p.RenderPage(ctx, PageType)
	if data["meta_description"] != "Says hi." || data["title"] != "HELLO" {
		t.Errorf("data = %v", data)
	}
	ctx, _ = newRenderCtx(t, http.MethodGet, "/plugins", "", nil)
	if _, data = f.p.RenderPage(ctx, PageType); data["meta_description"] != nil {
		t.Errorf("the listing keeps the site description: %v", data["meta_description"])
	}
}
