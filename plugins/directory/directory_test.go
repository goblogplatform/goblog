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
	if len(pages) != 1 || pages[0].PageType != PageType || pages[0].Slug != "plugins" {
		t.Errorf("pages: %+v", pages)
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
	f.db.AutoMigrate(&blog.Page{}, &blog.Setting{}, &gplugin.PluginSetting{})
	f.db.Create(&blog.Setting{Key: "site_url", Value: "https://example.test"})
	p := New()
	p.newSource = func(string) registry.Source { return f.src }
	p.validator = f.val
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
	if len(pages) != 1 || pages[0].PageType != blog.PageTypeCustom {
		t.Errorf("the pre-existing page must be left alone: %+v", pages)
	}
}

func TestScheduledJob(t *testing.T) {
	f := newPluginFixture(t)
	r, _ := f.svc.Add(context.Background(), "o/hello", "")
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
	// separately and unaffected.
	target := (&url.URL{Path: path}).String()
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

	f.svc.Add(context.Background(), "o/zeta", "")
	f.svc.Add(context.Background(), "o/hello", "")
	ctx, _ = newRenderCtx(t, http.MethodGet, "/plugins", "", nil)
	_, data = f.p.RenderPage(ctx, PageType)
	html := content(t, data)
	for _, want := range []string{`href="/plugins/hello"`, `href="/plugins/zeta"`, "HELLO", "Says hi.", "1.0.0", "Jason", "MIT",
		`href="/plugins/index.json"`, `href="https://github.com/o/hello"`, `href="/plugins/submit"`, "★ 7"} {
		if !strings.Contains(html, want) {
			t.Errorf("listing missing %q in:\n%s", want, html)
		}
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
	r := seed(t, f.db, "o/evil", StatusApproved, func() registry.DetailDoc {
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
	f.svc.Add(context.Background(), "o/hello", "")
	ctx, w = newRenderCtx(t, http.MethodGet, "/plugins/index.json", "index.json", nil)
	f.p.RenderPage(ctx, PageType)
	raw, _ := f.svc.Index()
	if w.Code != http.StatusOK || w.Body.String() != string(raw) || w.Header().Get("Cache-Control") != "public, max-age=300" {
		t.Errorf("index.json: %d %q cache=%q", w.Code, w.Body.String(), w.Header().Get("Cache-Control"))
	}
}

func TestRenderPage_DetailAndDetailJSON(t *testing.T) {
	f := newPluginFixture(t)
	f.svc.Add(context.Background(), "o/hello", "")
	z, _ := f.svc.Submit(context.Background(), "o/zeta", "ip", "")

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
	seed(t, f.db, "o/wide", StatusApproved, func() registry.DetailDoc {
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
	cases := map[string]string{"api.example.test": "", "*": "any host", "*.example.test": "wildcard", "localhost": "local network",
		"127.0.0.1": "local network", "10.1.2.3": "local network", "172.20.0.1": "local network", "192.168.1.1:8080": "local network", "172.15.0.1": ""}
	for in, want := range cases {
		if got := hostNote(in); got != want {
			t.Errorf("hostNote(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestBasePathFollowsSlug(t *testing.T) {
	f := newPluginFixture(t)
	f.svc.Add(context.Background(), "o/hello", "")
	ctx, _ := newRenderCtx(t, http.MethodGet, "/extensions", "", nil)
	_, data := f.p.RenderPage(ctx, PageType)
	if html := content(t, data); !strings.Contains(html, `href="/extensions/hello"`) || !strings.Contains(html, `href="/extensions/submit"`) {
		t.Errorf("links must follow the page slug:\n%s", html)
	}
}
